package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/transform"
)

// siteMarker marks a folder written by BuildSite: only such a folder may be
// cleared, and a clear means a demo build keeps no file of a full one.
const siteMarker = ".macro-site"

// siteCalendarDays is the calendar horizon of a static site.
const siteCalendarDays = 60

// EncodeSegment makes a key segment safe for file names and static hosts; the
// same rule is enc in web/app.js.
func EncodeSegment(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "~%02X", c)
		}
	}
	return b.String()
}

type siteWriter struct {
	dir   string
	files int
}

// put writes one answer for http (api/*.json) and for file:// (data/*.js).
func (w *siteWriter) put(v any, parts ...string) error {
	segs := make([]string, len(parts))
	for i, p := range parts {
		segs[i] = EncodeSegment(p)
	}
	key := strings.Join(segs, "/")
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("dashboard site %s: %w", key, err)
	}
	quoted, err := json.Marshal(key)
	if err != nil {
		return err
	}
	if err := w.file(filepath.Join("api", filepath.FromSlash(key)+".json"), body); err != nil {
		return err
	}
	script := append(append(append([]byte("window.__macroData("), quoted...), ','), body...)
	return w.file(filepath.Join("data", filepath.FromSlash(key)+".js"), append(script, ");\n"...))
}

func (w *siteWriter) file(rel string, body []byte) error {
	path := filepath.Join(w.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("dashboard site: %w", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("dashboard site: %w", err)
	}
	w.files++
	return nil
}

// prepareSiteDir refuses a folder that is not an earlier site and empties one
// that is, keeping its .git: the published site is a git working tree.
func prepareSiteDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return fmt.Errorf("dashboard site: %w", err)
	}
	entries = slices.DeleteFunc(entries, func(e os.DirEntry) bool { return e.Name() == ".git" })
	if len(entries) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, siteMarker)); err != nil {
		return fmt.Errorf("dashboard site: %s is not empty and is not an earlier site", dir)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("dashboard site: %w", err)
		}
	}
	return nil
}

// BuildSite writes the frontend and every answer it asks for into dir and
// returns the number of files.
func (d *Data) BuildSite(ctx context.Context, dir string, static fs.FS) (int, error) {
	if err := prepareSiteDir(dir); err != nil {
		return 0, err
	}
	w := &siteWriter{dir: dir}
	if err := w.file(siteMarker, []byte("macroctl site\n")); err != nil {
		return 0, err
	}
	// GitHub Pages: serve the files as they are, without Jekyll.
	if err := w.file(".nojekyll", nil); err != nil {
		return 0, err
	}
	err := fs.WalkDir(static, ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasSuffix(path, ".go") {
			return err
		}
		body, err := fs.ReadFile(static, path)
		if err != nil {
			return err
		}
		return w.file(filepath.FromSlash(path), body)
	})
	if err != nil {
		return 0, fmt.Errorf("dashboard site: %w", err)
	}
	if err := w.file("mode.js", []byte(staticModeScript)); err != nil {
		return 0, err
	}
	meta := d.Meta()
	if err := w.put(meta, "meta"); err != nil {
		return 0, err
	}
	for i := range d.Config.Regions {
		if err := d.siteRegion(ctx, w, &d.Config.Regions[i], meta.Regions[i]); err != nil {
			return 0, err
		}
	}
	events, err := d.Calendar(ctx, siteCalendarDays)
	if err != nil {
		return 0, err
	}
	if err := w.put(events, "calendar"); err != nil {
		return 0, err
	}
	if !d.Demo {
		st, err := d.Status(ctx)
		if err != nil {
			return 0, err
		}
		if err := w.put(st, "status"); err != nil {
			return 0, err
		}
	}
	return w.files, nil
}

func (d *Data) siteRegion(ctx context.Context, w *siteWriter, r *config.Region, meta RegionMeta) error {
	done := map[[3]string]bool{}
	series := func(source, id, code string) error {
		key := [3]string{source, id, code}
		if done[key] {
			return nil
		}
		done[key] = true
		s, err := d.Series(ctx, source, id, code, time.Time{})
		if err != nil {
			return err
		}
		return w.put(s, "series", source, id, code)
	}
	agents := map[string]bool{}
	for _, p := range meta.Pages {
		for _, ch := range p.Charts {
			for _, s := range ch.Series {
				if !s.Hidden {
					if err := series(s.Source, s.ID, s.Transform); err != nil {
						return err
					}
				}
			}
		}
		for _, v := range p.Views {
			if v.Agent != "" && !v.Hidden {
				agents[v.Agent] = true
			}
		}
	}
	for _, k := range meta.KeyRows {
		if k.Ref == "" || k.Hidden {
			continue
		}
		source, id, _ := strings.Cut(k.Ref, "/")
		code := k.Transform
		if code == "" {
			code = transform.Level
		}
		if err := series(source, id, code); err != nil {
			return err
		}
	}
	if d.Demo { // only what the shown views read, only the last week
		for agent := range agents {
			if err := d.siteReports(w, r.ID, agent, ""); err != nil {
				return err
			}
		}
		return nil
	}
	weeks, err := d.Weeks()
	if err != nil {
		return err
	}
	for j := range r.Agents {
		for _, week := range append([]string{""}, weeks...) {
			if err := d.siteReports(w, r.ID, r.Agents[j].ID, week); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Data) siteReports(w *siteWriter, region, agent, week string) error {
	rep, err := d.Reports(region, agent, week)
	if err != nil {
		return err
	}
	if d.Demo {
		rep.Weeks = []string{rep.Week}
	}
	name := week
	if name == "" {
		name = "latest"
	}
	return w.put(rep, "reports", region, agent, name)
}

// CheckDemoSite verifies a site --demo build before it goes public: the
// meta says demo, there is no status page, and every series file is a
// reference of allow (dashboard.demo_allow). It reads the files as built,
// so a bug in the demo filter cannot publish a closed series.
func CheckDemoSite(dir string, allow []string) error {
	var meta struct {
		Demo bool `json:"demo"`
	}
	body, err := os.ReadFile(filepath.Join(dir, "api", "meta.json"))
	if err != nil {
		return fmt.Errorf("demo site check: %w", err)
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return fmt.Errorf("demo site check meta: %w", err)
	}
	if !meta.Demo {
		return errors.New("demo site check: meta.demo is false (a full build)")
	}
	var errs []error
	for _, name := range []string{filepath.Join("api", "status.json"), filepath.Join("data", "status.js")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			errs = append(errs, fmt.Errorf("demo site check: %s is published", name))
		}
	}
	for _, root := range []string{"api", "data"} {
		base := filepath.Join(dir, root, "series")
		err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == base {
				return fs.SkipDir
			}
			if err != nil || e.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(base, path)
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) != 3 {
				errs = append(errs, fmt.Errorf("demo site check: unexpected series file %s", rel))
				return nil
			}
			source, err1 := decodeSegment(parts[0])
			id, err2 := decodeSegment(parts[1])
			if err := errors.Join(err1, err2); err != nil {
				errs = append(errs, fmt.Errorf("demo site check %s: %w", rel, err))
				return nil
			}
			if ref := source + "/" + id; !slices.Contains(allow, ref) {
				errs = append(errs, fmt.Errorf("demo site check: series %s is not in dashboard.demo_allow", ref))
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("demo site check: %w", err))
		}
	}
	return errors.Join(errs...)
}

// decodeSegment reverses EncodeSegment.
func decodeSegment(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("bad segment %q", s)
		}
		v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err != nil {
			return "", fmt.Errorf("bad segment %q", s)
		}
		b.WriteByte(byte(v))
		i += 2
	}
	return b.String(), nil
}
