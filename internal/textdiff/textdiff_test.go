package textdiff

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func texts(ss []sentence) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.text)
	}
	return out
}

func TestSplit(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"The U.S. economy grew. Inflation rose 2.5 percent.",
			[]string{"The U.S. economy grew.", "Inflation rose 2.5 percent."}},
		{"The range is 3-1/2 to 3-3/4 percent. Voting were Jerome H. Powell, Chair; and others.",
			[]string{"The range is 3-1/2 to 3-3/4 percent.", "Voting were Jerome H. Powell, Chair; and others."}},
		{`He said "rates will rise." Then he left.`,
			[]string{`He said "rates will rise."`, "Then he left."}},
		{"Voting were Jerome H. Powell and Lorie K. Logan. Next.",
			[]string{"Voting were Jerome H. Powell and Lorie K. Logan.", "Next."}},
		{"Released at 2:00 p.m. Eastern time. Next one.",
			[]string{"Released at 2:00 p.m. Eastern time.", "Next one."}},
		{"Is it? Yes! 2026 was strong.",
			[]string{"Is it?", "Yes!", "2026 was strong."}},
		{"First paragraph.\n\nSecond paragraph, no dot", []string{"First paragraph.", "Second paragraph, no dot"}},
	}
	for _, tt := range tests {
		if got := texts(split(tt.in)); !slices.Equal(got, tt.want) {
			t.Errorf("split(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
	if s := split("Alpha.\n\nBeta. Gamma."); len(s) != 3 || s[0].para != 0 || s[1].para != 1 || s[2].para != 1 {
		t.Errorf("paragraphs = %+v", s)
	}
}

func TestCompare(t *testing.T) {
	old := "Rates were held at 4 percent. Growth is solid. Inflation is elevated.\n\nVoting were all."
	new := "Rates were raised to 4.25 percent. Growth is solid. Productivity is strong.\n\nVoting were all."
	d := Compare(old, new)
	var kinds []Kind
	for _, e := range d.Entries {
		kinds = append(kinds, e.Kind)
	}
	// "Inflation is elevated." vs "Productivity is strong." share 1 word (Dice 0.33): removed + added.
	want := []Kind{Changed, Equal, Removed, Added, Equal}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v (%+v)", kinds, want, d.Entries)
	}
	if c, a, r := d.Stats(); c != 1 || a != 1 || r != 1 {
		t.Errorf("stats = %d %d %d", c, a, r)
	}
	if got := Words(d.Entries[0].Old, d.Entries[0].New); got != "Rates were ~~held at 4~~ **raised to 4.25** percent." {
		t.Errorf("words = %q", got)
	}
	if !Compare(old, old).Identical() {
		t.Error("same text not identical")
	}
	// Non-breaking hyphens and spacing do not count as changes.
	if !Compare("At 3\u20111/2  to 4 percent.", "At 3-1/2 to 4 percent.").Identical() {
		t.Error("typographic variants differ")
	}
	if got := Words("a * b", "a * c"); got != `a \* ~~b~~ **c**` {
		t.Errorf("escape: %q", got)
	}
}

func readText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "textdiff", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Golden files: real FOMC statements of 2026 (testdata/textdiff), rerun with -update.
func TestGoldenStatements(t *testing.T) {
	for _, pair := range [][2]string{{"20260617", "20260729"}, {"20260729", "20260916"}, {"20260429", "20260617"}} {
		t.Run(pair[0]+"_"+pair[1], func(t *testing.T) {
			got := Compare(readText(t, "statement_"+pair[0]+".txt"), readText(t, "statement_"+pair[1]+".txt")).Markdown()
			path := filepath.Join("..", "..", "testdata", "textdiff", "diff_"+pair[0]+"_"+pair[1]+".md")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("diff differs from %s:\n%s", path, got)
			}
		})
	}
}

func TestGoldenJulySeptemberContent(t *testing.T) {
	md := Compare(readText(t, "statement_20260729.txt"), readText(t, "statement_20260916.txt")).Markdown()
	for _, s := range []string{
		"by a ~~9~~ **12** – ~~3~~ **0** vote",
		"~~maintain~~ **raise**",
		"- Voting against the monetary policy action were Beth M. Hammack",
		// The removed voting paragraph stays a paragraph of its own.
		"price stability.\n\n~~Voting against",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("missing %q in\n%s", s, md)
		}
	}
}
