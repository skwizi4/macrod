package collect

import (
	"fmt"
	"strings"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/irxlsx"
)

// FileIssuer finds the issuer a sent quarterly file belongs to (phase
// 14c): ticker as given, or, when empty, the only issuer whose quarterly
// source is file. The quarter comes from period ("Q3 2026"), else from the
// file name patterns (quarterly.links).
func FileIssuer(cfg *config.Config, ticker, name, period string) (config.Issuer, irxlsx.File, error) {
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		return config.Issuer{}, irxlsx.File{}, fmt.Errorf("%s: нужен файл .xlsx", name)
	}
	var is config.Issuer
	if ticker != "" {
		var ok bool
		if is, ok = cfg.Issuers.Issuer(strings.ToUpper(ticker)); !ok || is.Quarterly == nil {
			return is, irxlsx.File{}, fmt.Errorf("у %s нет секции quarterly в issuers конфига (есть: %s)", strings.ToUpper(ticker), strings.Join(quarterlyTickers(cfg, false), ", "))
		}
	} else {
		files := quarterlyTickers(cfg, true)
		if len(files) != 1 {
			return is, irxlsx.File{}, fmt.Errorf("укажите тикер (подпись файла или аргумент): квартальный файл присылают для %s", orNone(files))
		}
		is, _ = cfg.Issuers.Issuer(files[0])
	}
	f := irxlsx.File{Name: name}
	if period != "" {
		fy, q, ok := irxlsx.ParseQuarter(period)
		if !ok {
			return is, f, fmt.Errorf("квартал %q: нужен вид «Q3 2026»", period)
		}
		f.FY, f.Q = fy, q
		return is, f, nil
	}
	got, ok := irxlsx.FileOf(name, is.Quarterly.LinkRes)
	if !ok {
		return is, f, fmt.Errorf("квартал не виден из имени %s: укажите его, например «%s Q3 2026»", name, is.Ticker)
	}
	return is, got, nil
}

func quarterlyTickers(cfg *config.Config, fileOnly bool) []string {
	var out []string
	for _, c := range cfg.Issuers.Companies {
		if c.Quarterly != nil && (!fileOnly || c.Quarterly.FromFile()) {
			out = append(out, c.Ticker)
		}
	}
	return out
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "никого (нет quarterly.source: file)"
	}
	return strings.Join(s, ", ")
}

// QuarterSummary is what a sent file gave, a line per field: the quarter
// and a year earlier (billions in the company's currency; EPS per share).
func QuarterSummary(is config.Issuer, f irxlsx.File, pairs map[string]irxlsx.Pair) []string {
	q := is.Quarterly
	labels := map[string]string{config.QuarterRevenue: q.RevenueLabel, config.QuarterOperating: q.OperatingName,
		config.QuarterNet: "чистая прибыль акционерам", config.QuarterEPS: "EPS базовая"}
	cur := is.CurrencyOf()
	var out []string
	for _, field := range config.QuarterFields {
		p, ok := pairs[field]
		if !ok {
			continue
		}
		if field == config.QuarterEPS {
			out = append(out, fmt.Sprintf("%s: %.2f %s (год назад %.2f)", labels[field], p.Cur, cur, p.Ago))
			continue
		}
		out = append(out, fmt.Sprintf("%s: %.2f млрд %s (год назад %.2f)", labels[field], p.Cur/1e9, cur, p.Ago/1e9))
	}
	return out
}
