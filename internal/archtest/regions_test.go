package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// exceptions are the region literals the code may keep: file → literal →
// why. An exception nothing uses fails the test, so the list cannot go stale.
var exceptions = map[string]map[string]string{
	filepath.Join("internal", "export", "news.go"): {
		"eu_only": "contract name of the drop reason: the JSON schema and the News header carry it (U2 golden)",
	},
}

// logCalls are selector names of logging calls: a message may mention
// anything.
var logCalls = map[string]bool{"Debug": true, "Info": true, "Warn": true, "Error": true, "Printf": true, "Println": true, "Print": true}

func isLogCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); ok {
		switch {
		case x.Name == "slog" || x.Name == "log":
			return true
		case x.Name == "fmt" && sel.Sel.Name == "Errorf":
			return true
		}
	}
	return logCalls[sel.Sel.Name]
}

// regionLiteral reports whether a string names a region: exactly, as a tag
// prefix or as a path segment. Text that merely contains the letters, a URL
// or a sentence does not.
func regionLiteral(s string) bool {
	if strings.Contains(s, "://") {
		return false
	}
	if s == "usa" || s == "eu" || strings.HasPrefix(s, "usa_") || strings.HasPrefix(s, "eu_") {
		return true
	}
	if strings.Contains(s, "/") {
		for _, seg := range strings.Split(s, "/") {
			if seg == "usa" || seg == "eu" {
				return true
			}
		}
	}
	return false
}

// findLiterals returns the region literals of a Go file's code. Comments are
// not part of the walk; struct tags and logging calls are skipped.
func findLiterals(src string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			if x.Tag != nil {
				return false // a tag is the name of a contract field
			}
		case *ast.CallExpr:
			if isLogCall(x) {
				return false
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil && regionLiteral(v) {
					out = append(out, v)
				}
			}
		}
		return true
	})
	return out, nil
}

func TestFindLiterals(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		found      int
	}{
		{"comparison", `func f(r string) bool { return r != "usa" }`, 1},
		{"assignment to a field", `func f(d *D) { d.Region = "eu" }`, 1},
		{"function argument", `func f() { g("usa") }`, 1},
		{"tag prefix", `var x = strings.HasPrefix(t, "usa_")`, 1},
		{"path segment", `var x = "archive/usa/x.md"`, 1},
		{"composite literal", `var x = D{Region: "usa"}`, 1},
		{"slog message", `func f() { slog.Warn("usa", "k", "eu") }`, 0},
		{"logger method", `func f() { l.Warn("usa", "k", "eu_x") }`, 0},
		{"error text", `func f() error { return fmt.Errorf("usa") }`, 0},
		{"comment", "// usa\nvar x = 1 // eu_x", 0},
		{"substring", `var x = "thousand usa-style" + "museum"`, 0},
		{"url", `var x = "https://example.com/eu/feed"`, 0},
		{"struct tag", "type T struct { A int `json:\"eu_only\"` }", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findLiterals("package p\n" + tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.found {
				t.Errorf("found %q, want %d", got, tt.found)
			}
		})
	}
}

// ТЗ §21.3: no region is named in the code; regions are configured.
func TestNoRegionLiterals(t *testing.T) {
	root := filepath.Join("..", "..")
	used := map[string]map[string]bool{}
	for _, dir := range []string{"cmd", "internal", "presets", "templates"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lits, err := findLiterals(string(src))
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			rel, _ := filepath.Rel(root, path)
			for _, lit := range lits {
				if _, ok := exceptions[rel][lit]; ok {
					if used[rel] == nil {
						used[rel] = map[string]bool{}
					}
					used[rel][lit] = true
					continue
				}
				t.Errorf("%s: region literal %q (configure it in regions:, or add an exception with a reason)", rel, lit)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for file, lits := range exceptions {
		for lit := range lits {
			if !used[file][lit] {
				t.Errorf("exception %s: %q is not used any more", file, lit)
			}
		}
	}
}

// The frontend draws regions from /api/meta and must not name them.
func TestWebHasNoRegionLiterals(t *testing.T) {
	dir := filepath.Join("..", "..", "web")
	literal := regexp.MustCompile(`["'` + "`" + `](usa|eu)(_[a-z]+|/[^"'` + "`" + `]*)?["'` + "`" + `]`)
	for _, name := range []string{"app.js", "views.js", "markdown.js", "index.html"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if m := literal.Find(raw); m != nil {
			t.Errorf("%s: region literal %s", name, m)
		}
	}
}
