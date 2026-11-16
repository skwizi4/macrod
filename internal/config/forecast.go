package config

import (
	"errors"
	"fmt"
	"regexp"
)

// Forecast is the forecast log (phase 13): the forecast block of the
// masters' reports, scored a week later against the instrument's price.
type Forecast struct {
	Enabled bool `yaml:"enabled"`
	// Agent is the id of the agent whose report holds the block; default
	// master. A region without it has no forecast log.
	Agent string `yaml:"agent"`
	// RangePct: a range forecast hits if the week's move is below it, in %;
	// default 1.
	RangePct    float64              `yaml:"range_pct"`
	Instruments []ForecastInstrument `yaml:"instruments"`
}

// ForecastInstrument is a name the masters forecast and the series it is
// scored on.
type ForecastInstrument struct {
	Name  string `yaml:"name"`  // as in the block: NASDAQ, GER40
	Price string `yaml:"price"` // a configured series: "^NDX" or "source/id"
	// Fallback is used when Price has no close on a scoring day; its level
	// differs, so the invalidation level is not checked on it.
	Fallback string `yaml:"fallback"`
}

const (
	defaultForecastAgent = "master"
	defaultRangePct      = 1.0
)

var instrumentRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func (c *Config) validateForecast() error {
	f := &c.Forecast
	if f.Agent == "" {
		f.Agent = defaultForecastAgent
	}
	if f.RangePct == 0 {
		f.RangePct = defaultRangePct
	}
	if !f.Enabled {
		return nil
	}
	var errs []error
	if f.RangePct < 0 {
		errs = append(errs, fmt.Errorf("forecast.range_pct %v: must be positive", f.RangePct))
	}
	if len(f.Instruments) == 0 {
		errs = append(errs, errors.New("forecast.instruments: empty"))
	}
	names := map[string]bool{}
	for i, in := range f.Instruments {
		where := fmt.Sprintf("forecast.instruments[%d]", i)
		if !instrumentRe.MatchString(in.Name) {
			errs = append(errs, fmt.Errorf("%s: name %q: want letters, digits, _ . -", where, in.Name))
		}
		if names[in.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name %q", where, in.Name))
		}
		names[in.Name] = true
		if _, ok := c.ResolveSeries(in.Price); !ok {
			errs = append(errs, fmt.Errorf("%s: price %q is not a configured series", where, in.Price))
		}
		if in.Fallback != "" {
			if _, ok := c.ResolveSeries(in.Fallback); !ok {
				errs = append(errs, fmt.Errorf("%s: fallback %q is not a configured series", where, in.Fallback))
			}
		}
	}
	return errors.Join(errs...)
}

// ForecastInstrument returns the instrument named name.
func (c *Config) ForecastInstrument(name string) (ForecastInstrument, bool) {
	for _, in := range c.Forecast.Instruments {
		if in.Name == name {
			return in, true
		}
	}
	return ForecastInstrument{}, false
}

// AgentByID returns the agent of r with id, or nil.
func (r *Region) AgentByID(id string) *Agent {
	for i := range r.Agents {
		if r.Agents[i].ID == id {
			return &r.Agents[i]
		}
	}
	return nil
}
