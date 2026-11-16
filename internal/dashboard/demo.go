package dashboard

import (
	"encoding/json"
	"fmt"

	"macro-collector/internal/config"
)

// demoReports are the report kinds with public parts.
var demoReports = []string{config.ReportKeyMetrics, config.ReportFiscal, config.ReportMicro}

// demoFields are the parts of those reports built only from public data: an
// allow list, so a new field stays private until someone looks at it.
var demoFields = map[string][]string{
	config.ReportFiscal: {"auction_window", "coupons", "bills", "supply"},
	config.ReportMicro:  {"capex", "financials"},
}

// demoReport cuts a report JSON to what site --demo may publish.
func (d *Data) demoReport(r *config.Region, kind string, raw json.RawMessage) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("dashboard demo %s: %w", kind, err)
	}
	out := map[string]any{}
	if kind == config.ReportKeyMetrics {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(doc["rows"], &rows); err != nil {
			return nil, fmt.Errorf("dashboard demo %s: %w", kind, err)
		}
		meta := d.keyRows(r)
		keep := []map[string]json.RawMessage{}
		for i, row := range rows {
			if i < len(meta) && !meta[i].Hidden && meta[i].Ref != "" {
				keep = append(keep, row)
			}
		}
		out["rows"] = keep
	}
	for _, f := range demoFields[kind] {
		if v, ok := doc[f]; ok {
			out[f] = v
		}
	}
	return json.Marshal(out)
}
