package export

import "macro-collector/internal/config"

// builtinTemplates are the templates without a templates_dir.
var builtinTemplates = func() *Templates {
	t, err := parseTemplates("")
	if err != nil {
		panic(err)
	}
	return t
}()

// renderReport renders a kind with its built-in template.
func renderReport(report string, model any) (string, error) {
	return builtinTemplates.render(report, report, model)
}

// newsReportOf is the configured news report of the first region.
func newsReportOf(cfg *config.Config) *config.Report {
	o, _ := cfg.Regions[0].Owner(config.ReportNews)
	return o.Report
}

// useReport makes a kind the report being built, as Render does.
func (w *week) useReport(kind string) {
	o, _ := w.region.Owner(kind)
	w.agent, w.rep = o.Agent, o.Report
}
