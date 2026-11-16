package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/store"
)

const (
	licensedValue = "98765.4321" // a Yahoo level that must not reach the demo
	telegramText  = "TELEGRAM-POST-TEXT"
	reactionValue = "55555.125"
)

var frontend = fstest.MapFS{"index.html": {Data: []byte("<html>")}, "app.js": {Data: []byte("//")}}

func fixture(t *testing.T) *Data {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvDataDir, dir)
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "us-equities.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Dashboard.DemoAllow = []string{"fred/DGS10", "derived/NET_LIQUIDITY"}
	ctx := context.Background()
	w, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	seed := func(source, id, units string, v float64) {
		err := w.Write(ctx, func(tx *sql.Tx) error {
			if err := store.UpsertSeriesMeta(tx, store.SeriesMeta{Source: source, SeriesID: id, Label: id, Agent: "x", Frequency: "d", Units: units}); err != nil {
				return err
			}
			_, err := store.UpsertObservations(tx, source, id, []model.Observation{{Date: day.AddDate(0, 0, -1), Value: v}, {Date: day, Value: v}}, day)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("fred", "DGS10", "Percent", 4.25)
	seed("derived", "NET_LIQUIDITY", "Billions of U.S. Dollars", 5800)
	seed("fred", "BAMLH0A0HYM2", "Percent", 98765.4321)
	seed("yahoo", "^VIX", "", 98765.4321)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	writeReports(t, cfg)
	r, err := store.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return &Data{Config: cfg, Store: r, Now: func() time.Time { return day }}
}

func writeReports(t *testing.T, cfg *config.Config) {
	t.Helper()
	archive := filepath.Join(cfg.Export.ExchangeDir, "archive", "2026-W43")
	write := func(rel, body string) {
		path := filepath.Join(archive, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := &cfg.Regions[0]
	for i := range r.Agents {
		a := &r.Agents[i]
		for _, rep := range a.Reports {
			rel := r.Path(a, rep.File)
			write(rel, "# "+rep.Kind+" "+telegramText)
			doc := `{"header":{"note":"` + telegramText + `"},"secret":` + reactionValue + `}`
			switch rep.Kind {
			case config.ReportMicro:
				doc = `{"header":{},"capex":{"has_data":true},"financials":{"rows":[]},"week":[{"reaction":{"pct":{"value":` + reactionValue + `}}}]}`
			case config.ReportFiscal:
				doc = `{"header":{},"supply":{"groups":[]},"coupons":[],"bills":[],"auction_window":6,"curve":` + reactionValue + `}`
			case config.ReportKeyMetrics:
				rows := []string{}
				for _, row := range rep.Rows {
					v := "1"
					if row.ID == "^VIX" {
						v = licensedValue
					}
					rows = append(rows, `{"label":"`+row.Label+`","value":{"value":`+v+`}}`)
				}
				doc = `{"rows":[` + strings.Join(rows, ",") + `]}`
			}
			write(filepath.Join("json", strings.TrimSuffix(rel, ".md")+".json"), doc)
		}
	}
}

func server(t *testing.T, d *Data, a Auth) http.Handler {
	t.Helper()
	s := &Server{Data: d, Auth: a, Static: frontend, Log: slog.New(slog.DiscardHandler)}
	return s.Handler()
}

func get(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	h := server(t, fixture(t), Auth{})
	cases := []struct {
		url  string
		code int
	}{
		{"/", 200},
		{"/mode.js", 200},
		{"/api/meta", 200},
		{"/api/series/fred/DGS10", 200},
		{"/api/series/fred/DGS10?from=2026-10-20&transform=level", 200},
		{"/api/series/yahoo/%5EVIX", 200},
		{"/api/series/fred/NOT_CONFIGURED", 404},
		{"/api/series/fred/DGS10?transform=nope", 400},
		{"/api/series/fred/DGS10?from=yesterday", 400},
		{"/api/reports/usa/monetary", 200},
		{"/api/reports/usa/monetary?week=2026-W43", 200},
		{"/api/reports/usa/monetary?week=2020-W01", 404},
		{"/api/reports/usa/monetary?week=../../etc", 400},
		{"/api/reports/nowhere/monetary", 404},
		{"/api/reports/usa/%2E%2E%2F%2E%2E", 404},
		{"/api/calendar", 200},
		{"/api/calendar?days=0", 400},
		{"/api/calendar?days=x", 400},
		{"/api/status", 200},
		{"/api/unknown", 404},
	}
	for _, tc := range cases {
		if rec := get(t, h, tc.url); rec.Code != tc.code {
			t.Errorf("%s: %d, want %d: %s", tc.url, rec.Code, tc.code, rec.Body.String())
		}
	}
}

func TestOnlyReadMethodsAndSecurityHeaders(t *testing.T) {
	h := server(t, fixture(t), Auth{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/meta", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
	got := get(t, h, "/").Header()
	if !strings.Contains(got.Get("Content-Security-Policy"), "script-src 'self'") || got.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", got)
	}
}

func TestSeriesAnswer(t *testing.T) {
	d := fixture(t)
	s, err := d.Series(context.Background(), "fred", "DGS10", "", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if s.Unit != "percent" || s.Transform != "level" || len(s.Points) != 1 || s.Points[0].Value != 4.25 {
		t.Fatalf("%+v", s)
	}
	body, _ := json.Marshal(s.Points[0])
	if string(body) != `["2026-10-20",4.25]` {
		t.Fatalf("point: %s", body)
	}
}

func TestBasicAuth(t *testing.T) {
	h := server(t, fixture(t), Auth{User: "macro", Password: "pw"})
	if rec := get(t, h, "/api/meta"); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no credentials: %d", rec.Code)
	}
	for pass, want := range map[string]int{"wrong": http.StatusUnauthorized, "pw": http.StatusOK} {
		req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
		req.SetBasicAuth("macro", pass)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("password %q: %d, want %d", pass, rec.Code, want)
		}
	}
}

func TestCheckListen(t *testing.T) {
	with := Auth{Password: "pw"}
	cases := []struct {
		addr string
		auth Auth
		ok   bool
	}{
		{"127.0.0.1:8080", Auth{}, true},
		{"[::1]:8080", Auth{}, true},
		{"localhost:8080", Auth{}, true},
		{"0.0.0.0:8080", Auth{}, false},
		{":8080", Auth{}, false},
		{"192.168.1.5:8080", Auth{}, false},
		{"0.0.0.0:8080", with, true},
		{"nonsense", Auth{}, false},
	}
	for _, tc := range cases {
		if err := CheckListen(tc.addr, tc.auth); (err == nil) != tc.ok {
			t.Errorf("%s %v: %v", tc.addr, tc.auth, err)
		}
	}
}

func TestRedact(t *testing.T) {
	in := `Get "https://x/y?api_key=abc123&file_type=json": 500; https://api.telegram.org/bot123:AAbb-c/send token=zzz`
	got := redact(in)
	for _, leak := range []string{"abc123", "123:AAbb", "zzz"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s leaks in %s", leak, got)
		}
	}
	if !strings.Contains(got, "file_type=json") {
		t.Errorf("over-redacted: %s", got)
	}
}

func TestStatusHasNoDatabasePath(t *testing.T) {
	d := fixture(t)
	body := get(t, server(t, d, Auth{}), "/api/status").Body.String()
	if strings.Contains(body, d.Config.DBPath) || strings.Contains(body, filepath.Dir(d.Config.DBPath)) {
		t.Fatalf("status shows a path: %s", body)
	}
}

func TestServerDoesNotChangeTheDatabase(t *testing.T) {
	d := fixture(t)
	before, err := os.ReadFile(d.Config.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	h := server(t, d, Auth{})
	for _, url := range []string{"/api/meta", "/api/series/fred/DGS10", "/api/status", "/api/calendar", "/api/reports/usa/monetary"} {
		get(t, h, url)
	}
	after, err := os.ReadFile(d.Config.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the database file changed")
	}
}

// siteFiles returns the site's files by relative path.
func siteFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDemoSitePublishesOnlyOpenData(t *testing.T) {
	d := fixture(t)
	d.Demo = true
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := d.BuildSite(context.Background(), dir, frontend); err != nil {
		t.Fatal(err)
	}
	files := siteFiles(t, dir)
	for path, body := range files {
		for _, secret := range []string{licensedValue, telegramText, reactionValue} {
			if strings.Contains(body, secret) {
				t.Errorf("%s contains %s", path, secret)
			}
		}
		if strings.HasPrefix(path, "api/series/") {
			parts := strings.Split(path, "/")
			if parts[2]+"/"+parts[3] != "fred/DGS10" && parts[2]+"/"+parts[3] != "derived/NET_LIQUIDITY" {
				t.Errorf("series outside demo_allow: %s", path)
			}
		}
	}
	if _, ok := files["api/status.json"]; ok {
		t.Error("the demo publishes the status")
	}
	for path := range files {
		if strings.HasPrefix(path, "api/reports/usa/news") || strings.HasPrefix(path, "api/reports/usa/monetary") {
			t.Errorf("private report published: %s", path)
		}
	}
	if !strings.Contains(files["api/series/fred/DGS10/level.json"], "4.25") {
		t.Error("an allowed series is missing")
	}
	if !strings.Contains(files["mode.js"], `"static"`) {
		t.Error("mode.js is not static")
	}
	var rep Reports
	if err := json.Unmarshal([]byte(files["api/reports/usa/micro/latest.json"]), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 1 || rep.Files[0].MD != "" || !strings.Contains(string(rep.Files[0].JSON), "capex") {
		t.Fatalf("micro in the demo: %+v", rep)
	}
	if !strings.HasPrefix(files["data/meta.js"], `window.__macroData("meta",`) {
		t.Errorf("data script: %.40s", files["data/meta.js"])
	}
}

func TestDemoMetaHidesWhatIsClosed(t *testing.T) {
	d := fixture(t)
	d.Demo = true
	views := map[string]bool{}
	charts := map[string]bool{}
	for _, p := range d.Meta().Regions[0].Pages {
		for _, v := range p.Views {
			views[v.View] = v.Hidden
		}
		for _, c := range p.Charts {
			charts[c.Title] = c.Hidden
		}
	}
	for _, v := range []string{"status", "fed_path", "reactions", "surprises", "topics", "reports"} {
		if !views[v] {
			t.Errorf("view %s is open in the demo", v)
		}
	}
	for _, v := range []string{"key_metrics", "auctions", "supply", "capex", "financials", "calendar"} {
		if views[v] {
			t.Errorf("view %s is closed in the demo", v)
		}
	}
	if !charts["Волатильность"] || charts["Net Liquidity"] {
		t.Errorf("charts: %v", charts)
	}
}

func TestDemoReportsEndpointIsCut(t *testing.T) {
	d := fixture(t)
	d.Demo = true
	rep, err := d.Reports("usa", "master", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Files {
		if f.Report != config.ReportKeyMetrics {
			t.Errorf("published %s", f.Report)
		}
		if f.MD != "" || strings.Contains(string(f.JSON), licensedValue) {
			t.Errorf("%s: %s", f.Report, f.JSON)
		}
	}
}

func TestSiteFolderRules(t *testing.T) {
	d := fixture(t)
	ctx := context.Background()
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "thesis.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.BuildSite(ctx, foreign, frontend); err == nil {
		t.Fatal("wrote into a foreign folder")
	}
	if _, err := os.Stat(filepath.Join(foreign, "thesis.txt")); err != nil {
		t.Fatal("touched a foreign file")
	}

	dir := filepath.Join(t.TempDir(), "site")
	if _, err := d.BuildSite(ctx, dir, frontend); err != nil {
		t.Fatal(err)
	}
	if _, ok := siteFiles(t, dir)["api/status.json"]; !ok {
		t.Fatal("no status in a full site")
	}
	d.Demo = true
	if _, err := d.BuildSite(ctx, dir, frontend); err != nil {
		t.Fatal(err)
	}
	if _, ok := siteFiles(t, dir)["api/status.json"]; ok {
		t.Fatal("a demo build kept a file of the full site")
	}
}

func TestRegionWithoutDashboardHasGenericPages(t *testing.T) {
	d := fixture(t)
	d.Config.Regions[0].Dashboard = config.RegionDashboard{}
	pages := d.Meta().Regions[0].Pages
	if len(pages) != 3 || pages[0].ID != "overview" || pages[2].ID != "reports" {
		t.Fatalf("%+v", pages)
	}
}

func TestRatesOnlyPresetHasPages(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	cfg, err := config.LoadAt(filepath.Join("..", "..", "presets", "rates-only.yaml"), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	d := &Data{Config: cfg, Now: time.Now}
	if pages := d.Meta().Regions[0].Pages; len(pages) != 3 || pages[0].ID != "curve" {
		t.Fatalf("%+v", pages)
	}
}

// The encoding exists twice (Go for files, JavaScript for requests).
func TestEncodeSegmentMatchesFrontend(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	app, err := filepath.Abs(filepath.Join("..", "..", "web", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := []string{"DGS10", "^VIX", "GC=F", "BTC-USD", "RSP:SPY", "a b/ё.", "2026-W43", "NET_LIQUIDITY"}
	raw, _ := json.Marshal(inputs)
	script := `globalThis.window=globalThis;globalThis.location={protocol:"http:"};` +
		`require(process.argv[1]);console.log(JSON.stringify(` + string(raw) + `.map(window.Macro.enc)))`
	out, err := exec.Command(node, "-e", script, app).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v: %s", err, out)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	for i, in := range inputs {
		if want := EncodeSegment(in); got[i] != want {
			t.Errorf("%q: js %q, go %q", in, got[i], want)
		}
	}
}

// The export files carry third-party text: no markup of it may survive.
func TestMarkdownEscapesThirdPartyText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "web", "markdown.js"))
	if err != nil {
		t.Fatal(err)
	}
	src := "<img src=x onerror=alert(1)>\n\n[click](javascript:alert(1))\n\n| a |\n|---|\n| <script>x</script> |\n\n[ok](https://example.com/?a=1\"onmouseover=\"x)"
	raw, _ := json.Marshal(src)
	out, err := exec.Command(node, "-e", `globalThis.window=globalThis;require(process.argv[1]);console.log(window.MacroMarkdown.render(`+string(raw)+`))`, script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v: %s", err, out)
	}
	html := string(out)
	for _, bad := range []string{"<img", "<script", "href=\"javascript", `"onmouseover="`} {
		if strings.Contains(html, bad) {
			t.Errorf("%q survives: %s", bad, html)
		}
	}
}

func TestCheckDemoSite(t *testing.T) {
	d := fixture(t)
	ctx := context.Background()
	allow := d.Config.Dashboard.DemoAllow
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := d.BuildSite(ctx, dir, frontend); err != nil {
		t.Fatal(err)
	}
	if err := CheckDemoSite(dir, allow); err == nil || !strings.Contains(err.Error(), "meta.demo is false") {
		t.Fatalf("a full build passed: %v", err)
	}
	d.Demo = true
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.BuildSite(ctx, dir, frontend); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Fatal("a rebuild removed .git")
	}
	if _, err := os.Stat(filepath.Join(dir, ".nojekyll")); err != nil {
		t.Error("no .nojekyll")
	}
	if err := CheckDemoSite(dir, allow); err != nil {
		t.Fatalf("demo build: %v", err)
	}
	closed := filepath.Join(dir, "api", "series", "yahoo", EncodeSegment("^NDX"), "level.json")
	if err := os.MkdirAll(filepath.Dir(closed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(closed, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api", "status.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckDemoSite(dir, allow)
	if err == nil || !strings.Contains(err.Error(), "yahoo/^NDX is not in dashboard.demo_allow") || !strings.Contains(err.Error(), "status.json") {
		t.Fatalf("closed files passed: %v", err)
	}
}
