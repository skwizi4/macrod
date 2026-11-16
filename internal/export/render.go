package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"text/template"
	"time"

	"macro-collector/internal/config"
	"macro-collector/templates"
)

// Templates are the Markdown templates of the report models. Their functions
// see only their arguments: all data comes from the model.
type Templates struct{ set *template.Template }

// parseTemplates reads the built-in templates and then dir, whose blocks
// replace the built-in ones of the same name; dir "" is none.
func parseTemplates(dir string) (*Templates, error) {
	set, err := template.New("").Funcs(templateFuncs).Option("missingkey=error").ParseFS(templates.FS, "*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("export templates: %w", err)
	}
	if dir != "" {
		files, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
		if err != nil {
			return nil, fmt.Errorf("export templates %s: %w", dir, err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("export templates: no *.tmpl in %s", dir)
		}
		if set, err = set.ParseFiles(files...); err != nil {
			return nil, fmt.Errorf("export templates %s: %w", dir, err)
		}
	}
	return &Templates{set: set}, nil
}

// LoadTemplates reads the templates of the config and checks that every
// report has its template.
func LoadTemplates(cfg *config.Config) (*Templates, error) {
	t, err := parseTemplates(cfg.TemplatesDir)
	if err != nil {
		return nil, err
	}
	var errs []error
	for i := range cfg.Regions {
		for _, a := range cfg.Regions[i].Agents {
			for _, rep := range a.Reports {
				if t.set.Lookup(rep.TemplateName()) == nil {
					errs = append(errs, fmt.Errorf("export templates: region %s, agent %s: no template %q", cfg.Regions[i].ID, a.ID, rep.TemplateName()))
				}
			}
		}
	}
	return t, errors.Join(errs...)
}

var templateFuncs = template.FuncMap{
	"period":  formatPeriod,
	"rfc3339": func(t time.Time) string { return t.Format(time.RFC3339) },
	"list":    list,
	"isSet":   isSet,
	"flags":   flags,
	"obsDate": func(d Day, freq string) string { return obsDate(d.Time, freq) },
	"date":    func(d Day) string { return d.Format(time.DateOnly) },
	"minute":  func(t time.Time) string { return t.Format("2006-01-02 15:04") },

	"level":      func(k kind, q Quantity) string { return k.level(q.val()) },
	"change":     func(k kind, q Quantity, withUnit bool) string { return k.change(q.val(), withUnit) },
	"pct1":       func(q Quantity) string { return pct1(q.val()) },
	"signedPct1": func(q Quantity) string { return signedPct1(q.val()) },
	"z":          func(q Quantity) string { return val(q.val(), 1, true) },

	"marketChange": marketChange,

	"releaseWhen":      releaseWhen,
	"factCurrent":      factCurrentCell,
	"factPrevious":     factPreviousCell,
	"factChange":       factChangeCell,
	"factRevision":     factRevisionCell,
	"documentHeading":  documentHeading,
	"growthCell":       growthCell,
	"laborChange":      laborChange,
	"laborAverage":     laborAverage,
	"growthRates":      growthRates,
	"fedPathFlags":     fedPathFlags,
	"impliedCell":      impliedCell,
	"contractMonth":    func(d Day) string { return contractMonth(d.Time) },
	"byMonth":          func(d Day) string { return byMonth(d.Time) },
	"bp":               func(q Quantity) string { return bp(q.val()) },
	"rate2":            func(q Quantity) string { return rate2(q.val()) },
	"moves":            func(q Quantity) string { return moves(q.val()) },
	"moveChance":       func(q Quantity) string { return moveChance(q.val()) },
	"keyValue":         keyValue,
	"keyChange1W":      keyChange1W,
	"keyChange4W":      keyChange4W,
	"calendarHourNote": calendarHourNote,
	"calendarDaily":    calendarDailyList,
	"calendarPrevious": calendarPrevious,
	"stars":            func(s string) string { return stars[s] },
	"weekday":          func(t time.Time) string { return ruWeekdays[t.Weekday()] },
	"dateOf":           func(t time.Time) string { return t.Format(time.DateOnly) },
	"clock":            func(t time.Time) string { return t.Format("15:04") },
	"levelCell":        levelCell,
	"billions":         func(q Quantity) string { return billions(q.val(), false) },
	"billionsSigned":   func(q Quantity) string { return billions(q.val(), true) },
	"eurBn":            func(q Quantity) string { return eurBillions(q.val()) },
	"bondKind":         bondKind,
	"couponWhen":       couponWhen,
	"couponName":       couponName,
	"couponTail":       couponTail,
	"yield3":           yield3,
	"ppUnit":           ppUnit,
	"num2":             func(q Quantity) string { return num(q.val().V, 2, false) },
	"signed2":          func(q Quantity) string { return val(q.val(), 2, true) },
	"bucket":           func(s string) string { return strings.ReplaceAll(s, " to ", "–") },
	"treasuryNextLine": treasuryNextLine,
	"fomcETime":        fomcETime,
	"fomcLink":         fomcLink,
	"fomcNoticeLine":   fomcNoticeLine,
	"ecbTime":          ecbTime,
	"ecbLink":          ecbLink,
	"ecbNoticeLine":    ecbNoticeLine,
	"ecbDiffMarkdown":  ecbDiffMarkdown,
	"ecbRate":          ecbRateCell,
	"ecbDecisionCell":  ecbDecisionCell,
	"decisionCell":     decisionCell,
	"decisionVote":     decisionVote,
	"decisionAgainst":  decisionAgainst,
	"diffMarkdown":     diffMarkdown,
	"val2":             func(q Quantity) string { return val(q.val(), 2, false) },
	"val3":             func(q Quantity) string { return val(q.val(), 3, false) },
	"signedInt":        func(q Quantity) string { return val(q.val(), 0, true) },
	"whole":            func(q Quantity) string { return val(q.val(), 0, false) },
	"regimeName":       regimeName,
	"join":             strings.Join,
	"inList":           slices.Contains[[]string],
	"reactionCell":     reactionCell,
	"surpriseCell":     surpriseCell,
	"etShort":          func(t time.Time) string { return t.Format("02.01 15:04") },
	"moveDay":          moveDay,
	"dailyCells":       dailyMovesCells,
	"dailyTop":         dailyMovesTop,
	"predictionCell":   predictionCell,
	"hikeCells":        hikeCells,
	"resonanceCell":    resonanceCell,
	"compactViews":     compactViews,
	"mdCell":           mdCell,
	"postViews":        postViews,
	"postTail":         postTail,
	"dropTitle":        dropTitle,
	"signedCount":      signedCount,
	"risingList":       risingList,
	"pct0":             func(q Quantity) string { return num(q.val().V, 0, false) },
	"minus":            func(a, b int) int { return a - b },
	"moneyCell":        moneyCell,
	"epsCell":          epsCell,
	"epsYoY":           epsYoYCell,
	"reactionMicro":    microReactionCell,
	"issuerReaction":   issuerReactionCell,
	"issuerCap":        issuerCapCell,
	"issuerSession":    issuerSession,
	"amountCell":       amountCell,
	"amount":           amountOnly,
	"qPct":             qPct,
	"qSigned":          qSigned,
	"moneyValue":       moneyValueCell,
	"bnLabel":          bnLabel,
	"capChange":        capChangeCell,
	"reportTime":       reportTimeCell,
	"seasonWhen":       seasonWhen,
	"signed0Bn":        func(q Quantity) string { return num(q.val().V, 0, true) },
	"num0":             func(x float64) string { return num(x, 0, false) },
	"bn":               bnCell,
	"capexYTD":         capexYTD,
	"capexTotal":       capexTotalCell,
	"capexGrowth":      capexGrowthCell,
	"capexTotalYTD":    capexTotalYTD,
	"capexTrailing":    capexTrailingLine,
	"capexNote":        capexNoteLine,
	"lateList":         lateList,
	"slashCell":        func(s string) string { return strings.ReplaceAll(s, "|", "/") },
	"bnSigned":         bnSigned,
	"marginCell":       marginCell,
	"pairCell":         pairCell,
	"grossCell":        grossCell,
	"netIncomeYoY":     netIncomeYoY,
	"buybacksCell":     buybacksCell,
	"dividendsCell":    dividendsCell,
	"returnCell":       returnCell,
	"returnShareCell":  returnShareCell,
	"rpoFromInstance":  rpoFromInstance,
	"noRPOLine":        noRPOLine,
	"segmentMargin":    segmentMargin,
	"segmentProfit":    segmentProfit,
	"pathChange":       pathChange,
	"signedWhole":      func(q Quantity) string { return num(q.val().V, 0, true) },
	"reportName":       func(title string) string { name, _, _ := strings.Cut(title, " — "); return name },
}

// formatPeriod is "2026-10-18 16:00 → 2026-10-25 16:00 (Europe/Bratislava)".
func formatPeriod(p Period) string {
	return fmt.Sprintf("%s → %s (%s)", p.From.Format("2006-01-02 15:04"), p.To.Format("2006-01-02 15:04"), p.TZ)
}

// isSet tells a nil slice from an empty one: some lines are written for an
// empty list and left out for none.
func isSet(x any) bool {
	v := reflect.ValueOf(x)
	return v.IsValid() && !v.IsNil()
}

// endsWithPosts are the reports whose last post leaves blank lines.
var endsWithPosts = map[string]bool{"news": true, "news_filtered": true, "uncategorized": true}

// render executes the template name of a report of the kind report.
func (t *Templates) render(report, name string, model any) (string, error) {
	var b strings.Builder
	if err := t.set.ExecuteTemplate(&b, name, model); err != nil {
		return "", fmt.Errorf("export render %s: %w", name, err)
	}
	if endsWithPosts[report] {
		return strings.TrimRight(b.String(), "\n") + "\n", nil
	}
	return b.String(), nil
}

// reportJSON is the indented JSON of a model, with a final newline.
func reportJSON(model any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(model); err != nil {
		return "", fmt.Errorf("export json: %w", err)
	}
	return b.String(), nil
}

// jsonPath is where the JSON of the Markdown file at path goes (D23).
func jsonPath(path string) string {
	return filepath.Join("json", strings.TrimSuffix(path, ".md")+".json")
}

// reportFiles renders a model into its Markdown file at path and its JSON
// file; report names the kind and the schema, name the template.
func reportFiles(t *Templates, name, path, report string, model any, archiveOnly bool) (md, js File, err error) {
	body, err := t.render(report, name, model)
	if err != nil {
		return File{}, File{}, err
	}
	data, err := reportJSON(model)
	if err != nil {
		return File{}, File{}, fmt.Errorf("%s: %w", path, err)
	}
	return File{Path: path, Body: body, ArchiveOnly: archiveOnly},
		File{Path: jsonPath(path), Body: data, ArchiveOnly: archiveOnly, Report: report}, nil
}
