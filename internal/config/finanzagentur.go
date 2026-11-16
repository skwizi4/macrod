package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Finanzagentur is the section of the Bund auctions (spec 6.1; phase 11):
// the results workbook of the current year, the history since 1999 (backfill)
// and the issuance calendar page that links the auction plan.
type Finanzagentur struct {
	Enabled      bool   `yaml:"enabled"`
	ResultsURL   string `yaml:"results_url"`
	HistoryURL   string `yaml:"history_url"`
	CalendarPage string `yaml:"calendar_page"`
	Agent        string `yaml:"agent"`     // series tag: the region of the auctions and their folder
	PollCron     string `yaml:"poll_cron"` // config timezone: the daily safety poll (D8)
	Timezone     string `yaml:"timezone"`  // of the auction days and the bidding close
	// CalendarImportance lists the securities of the issuance outlook that
	// go to the calendar, with their importance; others (Bubills) do not.
	CalendarImportance map[string]string `yaml:"calendar_importance"`

	Location *time.Location `yaml:"-"` // resolved Timezone
}

func (f *Finanzagentur) validate() error {
	if !f.Enabled {
		return nil
	}
	var errs []error
	for k, u := range map[string]string{"results_url": f.ResultsURL, "history_url": f.HistoryURL, "calendar_page": f.CalendarPage} {
		if !strings.HasPrefix(u, "https://") {
			errs = append(errs, fmt.Errorf("finanzagentur.%s %q: want an https URL", k, u))
		}
	}
	if f.Agent == "" {
		errs = append(errs, errors.New("finanzagentur.agent is empty"))
	}
	if _, err := cron.ParseStandard(f.PollCron); err != nil {
		errs = append(errs, fmt.Errorf("finanzagentur.poll_cron %q: %w", f.PollCron, err))
	}
	loc, err := time.LoadLocation(f.Timezone)
	if err != nil || f.Timezone == "" {
		errs = append(errs, fmt.Errorf("finanzagentur.timezone %q: invalid", f.Timezone))
	}
	f.Location = loc
	for sec, imp := range f.CalendarImportance {
		if imp != "high" && imp != "medium" && imp != "low" {
			errs = append(errs, fmt.Errorf("finanzagentur.calendar_importance %s: %q: want high, medium or low", sec, imp))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) validateFinanzagenturAgent() error {
	if !c.Finanzagentur.Enabled || c.Finanzagentur.Agent == "" {
		return nil
	}
	return c.checkAgent("finanzagentur", c.Finanzagentur.Agent)
}
