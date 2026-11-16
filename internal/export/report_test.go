package export

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"macro-collector/internal/config"
	"macro-collector/internal/transform"
)

var updateJSON = flag.Bool("update-json", false, "rewrite docs/schema/*.json and testdata/export/json/*.json (never the Markdown golden files)")

// reportModels are the report models, by report name.
var reportModels = map[string]func() any{
	"markets":          func() any { return &MarketsReport{} },
	"key_metrics":      func() any { return &KeyMetricsReport{} },
	"calendar":         func() any { return &CalendarReport{} },
	"monetary":         func() any { return &MonetaryReport{} },
	"fiscal_liquidity": func() any { return &FiscalReport{} },
	"sentiment":        func() any { return &SentimentReport{} },
	"news":             func() any { return &NewsReport{} },
	"news_filtered":    func() any { return &NewsFilteredReport{} },
	"uncategorized":    func() any { return &UncategorizedReport{} },
	"micro":            func() any { return &MicroReport{} },
	"fomc_diff":        func() any { return &FOMCDiffReport{} },
	"ecb_diff":         func() any { return &ECBDiffReport{} },
	"fiscal_bonds":     func() any { return &BondsReport{} },
	"micro_annual":     func() any { return &IssuerMicroReport{} },
}

// reportOfPath names the report of each JSON file of an export.
var reportOfPath = map[string]string{
	"usa/master/markets_usa.json":            "markets",
	"usa/master/key_metrics_usa.json":        "key_metrics",
	"usa/master/calendar_usa.json":           "calendar",
	"usa/monetary/current_week.json":         "monetary",
	"usa/monetary/fomc_diff.json":            "fomc_diff",
	"usa/fiscal_liquidity/current_week.json": "fiscal_liquidity",
	"usa/news/current_week.json":             "news",
	"usa/news/sentiment.json":                "sentiment",
	"usa/micro/current_week.json":            "micro",
	// Europe (phase 11).
	"eu/monetary_macro/current_week.json": "monetary",
	"eu/monetary_macro/ecb_diff.json":     "ecb_diff",
	"eu/fiscal_bonds/current_week.json":   "fiscal_bonds",
	"eu/micro/current_week.json":          "micro_annual",
	"eu/news/current_week.json":           "news",
	"eu/news/sentiment.json":              "sentiment",
	"eu/master/markets_eu.json":           "markets",
	"eu/master/calendar_eu.json":          "calendar",
	"eu/master/key_metrics_eu.json":       "key_metrics",
	"uncategorized.json":                  "uncategorized",
	"news_filtered.json":                  "news_filtered",
}

var schemaUnits = []string{unitPercent, unitBP, unitPP, unitUSDBn, unitUSD, unitEURBn, unitEUR, unitThous, unitCount, unitIndex, unitRatio, unitSigma, unitMoves, unitCorrelation, unitTimes}

var (
	quantityType = reflect.TypeFor[Quantity]()
	dayType      = reflect.TypeFor[Day]()
	timeType     = reflect.TypeFor[time.Time]()
	kindType     = reflect.TypeFor[kind]()
)

// schemaGen builds a JSON Schema from a model by reflection.
type schemaGen struct {
	t    *testing.T
	defs map[string]any
}

func (g *schemaGen) of(typ reflect.Type) map[string]any {
	switch typ {
	case quantityType:
		g.defs["Quantity"] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"unit", "value"},
			"properties": map[string]any{
				"value": map[string]any{"type": []string{"number", "null"}},
				"unit":  map[string]any{"type": "string", "enum": schemaUnits},
			},
		}
		return map[string]any{"$ref": "#/$defs/Quantity"}
	case dayType:
		return map[string]any{"type": []string{"string", "null"}, "pattern": `^\d{4}-\d{2}-\d{2}$`}
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case kindType:
		return map[string]any{"type": "string", "enum": kindNames[:]}
	}
	switch typ.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": []string{"array", "null"}, "items": g.of(typ.Elem())}
	case reflect.Pointer:
		return map[string]any{"anyOf": []any{map[string]any{"type": "null"}, g.of(typ.Elem())}}
	case reflect.Struct:
		name := typ.Name()
		if name == "" {
			g.t.Fatalf("anonymous struct in a report model: %v", typ)
		}
		if _, seen := g.defs[name]; !seen {
			g.defs[name] = nil // cycles
			props, required := map[string]any{}, []string{}
			g.fields(typ, props, &required)
			g.defs[name] = map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
		}
		return map[string]any{"$ref": "#/$defs/" + name}
	}
	g.t.Fatalf("type %v is not supported in a report model", typ)
	return nil
}

// fields collects the JSON fields of a struct, embedded structs flattened.
func (g *schemaGen) fields(typ reflect.Type, props map[string]any, required *[]string) {
	for f := range typ.Fields() {
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			g.fields(f.Type, props, required)
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "" || name == "-" {
			g.t.Fatalf("%s.%s: every field of a report model is in the JSON, with a name", typ.Name(), f.Name)
		}
		if f.Name == "SchemaVersion" {
			props[name] = map[string]any{"const": SchemaVersion}
		} else {
			props[name] = g.of(f.Type)
		}
		if opts != "omitempty" {
			*required = append(*required, name)
		}
	}
}

// generateSchema is the JSON Schema (Draft 2020-12) of a report model.
func generateSchema(t *testing.T, title string, model any) []byte {
	t.Helper()
	g := &schemaGen{t: t, defs: map[string]any{}}
	root := g.of(reflect.TypeOf(model).Elem())
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["title"] = title
	root["$defs"] = g.defs
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func compileSchema(t *testing.T, name string, schema []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	url := "mem:///" + name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(url)
	if err != nil {
		t.Fatalf("schema %s: %v", name, err)
	}
	return s
}

func schemaPath(report string) string {
	return filepath.Join("..", "..", "docs", "schema", report+".json")
}

func validateJSON(t *testing.T, name string, schema *jsonschema.Schema, data string) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := schema.Validate(inst); err != nil {
		t.Errorf("%s does not match its schema: %v", name, err)
	}
}

// loadSchema compiles docs/schema/<report>.json.
func loadSchema(t *testing.T, report string) *jsonschema.Schema {
	t.Helper()
	b, err := os.ReadFile(schemaPath(report))
	if err != nil {
		t.Fatal(err)
	}
	return compileSchema(t, report, b)
}

// checkReportJSON checks the JSON files of a render: valid by their schema,
// and rendering the decoded JSON gives the same Markdown (the JSON and the
// Markdown are one model, D18).
func checkReportJSON(t *testing.T, files []File) {
	t.Helper()
	bodies := map[string]string{}
	for _, f := range files {
		bodies[f.Path] = f.Body
	}
	for _, f := range files {
		if f.Report == "" {
			continue
		}
		validateJSON(t, f.Path, loadSchema(t, f.Report), f.Body)
		newModel, ok := reportModels[f.Report]
		if !ok {
			t.Fatalf("report %s is not registered", f.Report)
		}
		model := newModel()
		dec := json.NewDecoder(strings.NewReader(f.Body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(model); err != nil {
			t.Errorf("%s: decode: %v", f.Path, err)
			continue
		}
		got, err := renderReport(f.Report, model)
		if err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		var md string
		for p, body := range bodies {
			if strings.HasSuffix(p, ".md") && jsonPath(p) == f.Path {
				md = body
			}
		}
		if got != md {
			t.Errorf("%s: Markdown rendered from the JSON differs from the file", f.Path)
		}
	}
}

// mdFiles leaves the Markdown files.
func mdFiles(files []File) []File {
	var out []File
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".md") {
			out = append(out, f)
		}
	}
	return out
}

func TestSchemasUpToDate(t *testing.T) {
	for report, newModel := range reportModels {
		got := generateSchema(t, report, newModel())
		path := schemaPath(report)
		if *updateJSON {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date (go test ./internal/export -run SchemasUpToDate -update-json)", path)
		}
	}
}

// TestLiveExportJSON checks every JSON file of a real export, for phase
// acceptance: MACRO_EXPORT_DIR=<exchange dir> go test -run LiveExportJSON.
func TestLiveExportJSON(t *testing.T) {
	dir := os.Getenv("MACRO_EXPORT_DIR")
	if dir == "" {
		t.Skip("MACRO_EXPORT_DIR is not set")
	}
	root := filepath.Join(dir, "json")
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		report, ok := reportOfPath[filepath.ToSlash(rel)]
		if !ok {
			return fmt.Errorf("no report for %s", rel)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		validateJSON(t, rel, loadSchema(t, report), string(b))
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d JSON files valid", n)
	if n == 0 {
		t.Error("no JSON files found")
	}
}

func TestQuantityMissingIsNull(t *testing.T) {
	for _, v := range []transform.Value{{}, some(math.NaN()), some(math.Inf(1))} {
		b, err := json.Marshal(qty(v, unitPercent))
		if err != nil || string(b) != `{"value":null,"unit":"percent"}` {
			t.Errorf("qty(%v) = %s, %v", v, b, err)
		}
	}
	if q := qty(some(0), unitBP); q.Value == nil || !q.val().OK {
		t.Error("zero is a value")
	}
}

func TestKindUnits(t *testing.T) {
	for _, c := range []struct {
		k             kind
		level, change string
	}{{kindRate, unitPercent, unitBP}, {kindPct, unitPercent, unitPP}, {kindBn, unitUSDBn, unitUSDBn}, {kindThous, unitThous, unitThous},
		{kindCount, unitCount, unitCount}, {kindUSD0, unitUSD, unitUSD}, {kindRatio, unitRatio, unitRatio}, {kindBP, unitBP, unitBP}, {kindPx, unitIndex, unitIndex},
		{kindBnEUR, unitEURBn, unitEURBn}, {kindEUR, unitEUR, unitEUR}} {
		if c.k.levelUnit() != c.level || c.k.changeUnit() != c.change {
			t.Errorf("%v: %s, %s", c.k, c.k.levelUnit(), c.k.changeUnit())
		}
	}
	var k kind
	if err := k.UnmarshalText([]byte("nope")); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestDayJSON(t *testing.T) {
	type doc struct {
		D Day `json:"d"`
	}
	ny, _ := time.LoadLocation("America/New_York")
	b, _ := json.Marshal(doc{Day{time.Date(2026, 10, 30, 23, 0, 0, 0, ny)}})
	if string(b) != `{"d":"2026-10-30"}` {
		t.Errorf("day = %s", b)
	}
	if b, _ = json.Marshal(doc{}); string(b) != `{"d":null}` {
		t.Errorf("zero day = %s", b)
	}
	var d doc
	if err := json.Unmarshal([]byte(`{"d":"2026-10-30"}`), &d); err != nil || d.D.Format(time.DateOnly) != "2026-10-30" {
		t.Errorf("decoded %v, %v", d.D, err)
	}
	if err := json.Unmarshal([]byte(`{"d":null}`), &d); err != nil || !d.D.IsZero() {
		t.Errorf("decoded null %v, %v", d.D, err)
	}
}

func TestReportFilesHeader(t *testing.T) {
	bts, _ := time.LoadLocation("Europe/Bratislava")
	h := Header{
		SchemaVersion: SchemaVersion, Region: "usa", Agent: "master", File: "markets", ISOWeek: "2026-W43",
		Period:      Period{From: time.Date(2026, 10, 18, 16, 0, 0, 0, bts), To: time.Date(2026, 10, 25, 16, 0, 0, 0, bts), TZ: bts.String()},
		GeneratedAt: time.Date(2026, 10, 25, 16, 0, 4, 0, bts), Kind: KindWeekly,
		SourcesOK: []string{"yahoo", "fred"}, SourcesFailed: []string{}, StaleSeries: []string{}, Title: "USA Markets — 2026-W43",
	}
	md, js, err := reportFiles(builtinTemplates, "header", filepath.Join("usa", "master", "markets_usa.md"), "header", &h, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nregion: usa\nagent: master\nfile: markets\niso_week: 2026-W43\n" +
		"period: 2026-10-18 16:00 → 2026-10-25 16:00 (Europe/Bratislava)\ngenerated_at: 2026-10-25T16:00:04+01:00\nkind: weekly\n" +
		"sources_ok: [yahoo, fred]\nsources_failed: []\nstale_series: []\n---\n\n# USA Markets — 2026-W43\n\n"
	if md.Body != want {
		t.Errorf("header:\n%s", md.Body)
	}
	if js.Path != filepath.Join("json", "usa", "master", "markets_usa.json") || js.Report != "header" {
		t.Errorf("json file = %s, %s", js.Path, js.Report)
	}
	validateJSON(t, js.Path, compileSchema(t, "header", generateSchema(t, "header", &h)), js.Body)

	h.StaleSeries = nil
	md, _, err = reportFiles(builtinTemplates, "header", "x.md", "header", &h, false)
	if err != nil || strings.Contains(md.Body, "stale_series") {
		t.Errorf("no stale series: %v\n%s", err, md.Body)
	}
}

// A Yahoo symbol with units in the config is written by them; the others by
// the symbol, as before.
func TestYahooKind(t *testing.T) {
	for _, tt := range []struct {
		symbol, units string
		kind          kind
		relative      bool
	}{
		{"MIT10.AS", config.UnitsRate, kindRate, false}, // a yield: changes in basis points
		{"EURUSD=X", config.UnitsFX, kindFX, true},
		{"TTF=F", config.UnitsEURPerMWh, kindEUR, true},
		{"^GDAXI", "EUR", kindPx, true}, // the stored quote currency is not a unit
		{"^VIX", "USD", kindPx, false},
		{"CL=F", "USD", kindUSD, true},
	} {
		k, rel := yahooKind(tt.symbol, tt.units)
		if k != tt.kind || rel != tt.relative {
			t.Errorf("yahooKind(%s, %q) = %v, %v", tt.symbol, tt.units, k, rel)
		}
	}
	if got := kindFX.level(some(1.16342)); got != "1.1634" {
		t.Errorf("fx level = %s", got)
	}
	if got := kindBnEUR.level(some(5913.173)); got != "5 913 млрд €" {
		t.Errorf("euro amount = %q", got)
	}
	s, err := ScaleOf("ecb", "ECB_ASSETS", config.UnitsMillionsEUR)
	if err != nil || s.Factor != 0.001 || s.UnitOf(transform.Level) != unitEURBn {
		t.Errorf("scale of a euro amount = %+v, %v", s, err)
	}
	if s, _ := ScaleOf("yahoo", "MIT10.AS", config.UnitsRate); s.ChangeScale != 100 || s.Relative {
		t.Errorf("scale of a yield = %+v", s)
	}
	if s, _ := ScaleOf("bundesbank", "BUND_10Y", config.UnitsRate); s.ChangeScale != 100 || s.UnitOf(transform.D1W) != unitBP {
		t.Errorf("scale of a Bund yield = %+v", s)
	}
}
