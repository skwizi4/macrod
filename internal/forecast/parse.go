// Package forecast is the forecast log (phase 13, ТЗ §15): it reads the
// forecast block of the masters' reports, scores it a week later against
// the instrument's closes and renders forecast_scorecard.md.
package forecast

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"macro-collector/internal/store"
)

// Directions of a forecast.
const (
	Up    = "up"
	Down  = "down"
	Range = "range"
)

// ErrNoBlock: the report has no yaml block with a forecast key.
var ErrNoBlock = errors.New("no forecast block")

type rawBlock struct {
	Forecast []rawItem `yaml:"forecast"`
}

type rawItem struct {
	Instrument   string    `yaml:"instrument"`
	Direction    string    `yaml:"direction"`
	Invalidation yaml.Node `yaml:"invalidation"`
}

// Parse returns the forecasts of a report: the last fenced ```yaml block
// with a top-level forecast key. known tells the instruments of the config.
// The block must hold nothing else; each item needs a known instrument
// (once), a direction up|down|range and a bare positive number as the
// invalidation level.
func Parse(md []byte, known func(string) bool) ([]store.ForecastItem, error) {
	var block string
	found := false
	for _, b := range yamlBlocks(md) {
		var probe map[string]any
		if yaml.Unmarshal([]byte(b), &probe) != nil {
			if strings.Contains(b, "forecast:") {
				block, found = b, true // a broken forecast block: report its error
			}
			continue
		}
		if _, ok := probe["forecast"]; ok {
			block, found = b, true
		}
	}
	if !found {
		return nil, ErrNoBlock
	}
	dec := yaml.NewDecoder(strings.NewReader(block))
	dec.KnownFields(true)
	var raw rawBlock
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("forecast block: %w", err)
	}
	if len(raw.Forecast) == 0 {
		return nil, errors.New("forecast block: empty list")
	}
	seen := map[string]bool{}
	var out []store.ForecastItem
	for i, r := range raw.Forecast {
		where := fmt.Sprintf("forecast[%d]", i)
		switch {
		case r.Instrument == "":
			return nil, fmt.Errorf("%s: no instrument", where)
		case !known(r.Instrument):
			return nil, fmt.Errorf("%s: unknown instrument %q", where, r.Instrument)
		case seen[r.Instrument]:
			return nil, fmt.Errorf("%s: instrument %q twice", where, r.Instrument)
		}
		seen[r.Instrument] = true
		if r.Direction != Up && r.Direction != Down && r.Direction != Range {
			return nil, fmt.Errorf("%s: direction %q: want up, down or range", where, r.Direction)
		}
		level, err := number(&r.Invalidation)
		if err != nil {
			return nil, fmt.Errorf("%s: invalidation: %w", where, err)
		}
		out = append(out, store.ForecastItem{Instrument: r.Instrument, Direction: r.Direction, Invalidation: level})
	}
	return out, nil
}

// number is a bare positive number: an int or float scalar.
func number(n *yaml.Node) (float64, error) {
	if n.Kind == 0 {
		return 0, errors.New("missing")
	}
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && n.Tag != "!!float") {
		return 0, fmt.Errorf("%q: want a bare number", n.Value)
	}
	v, err := strconv.ParseFloat(n.Value, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%q: want a positive number", n.Value)
	}
	return v, nil
}

// yamlBlocks returns the bodies of the fenced blocks tagged yaml or yml.
func yamlBlocks(md []byte) []string {
	var out []string
	var cur []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !in {
			if lang, ok := strings.CutPrefix(line, "```"); ok {
				lang = strings.ToLower(strings.TrimSpace(lang))
				if lang == "yaml" || lang == "yml" {
					in, cur = true, nil
				}
			}
			continue
		}
		if line == "```" {
			out = append(out, strings.Join(cur, "\n"))
			in = false
			continue
		}
		cur = append(cur, sc.Text())
	}
	if in { // the report ends inside the block: a copy without the closing fence
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}
