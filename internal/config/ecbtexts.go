package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ECBTexts configures ecb.europa.eu texts, the Governing Council meeting
// calendar and the decision-day mode (ТЗ §6.1, §8.1–8.2). The section is
// optional: without enabled: true it is not read.
type ECBTexts struct {
	Enabled         bool          `yaml:"enabled"`
	BaseURL         string        `yaml:"base_url"`
	RSS             string        `yaml:"rss"`
	Lists           ECBLists      `yaml:"lists"`
	MeetingCalendar string        `yaml:"meeting_calendar"`
	Timezone        string        `yaml:"timezone"`
	Times           ECBTimes      `yaml:"times"`
	PollInterval    time.Duration `yaml:"poll_interval"`
	Agent           string        `yaml:"agent"`
	// CheckSeries is the stored DFR series a parsed decision is compared
	// with once in force; empty: no check.
	CheckSeries string     `yaml:"check_series"`
	DayMode     ECBDayMode `yaml:"day_mode"`

	// Feeds is the placeholder of configs written before phase 11
	// (feeds: "[[LOOKUP]]"); it is ignored and reported by Deprecations.
	Feeds any `yaml:"feeds"`

	Location *time.Location `yaml:"-"`
}

// ECBLists are the yearly lists of documents; "{year}" is substituted.
type ECBLists struct {
	Decisions  string `yaml:"decisions"`
	Statements string `yaml:"statements"`
	Accounts   string `yaml:"accounts"`
}

// ECBTimes are publication times (HH:MM, ECBTexts.Timezone).
type ECBTimes struct {
	Decision        string `yaml:"decision"`
	PressConference string `yaml:"press_conference"`
	Accounts        string `yaml:"accounts"`

	DecisionAt, PressConferenceAt, AccountsAt ClockTime `yaml:"-"`
}

// ECBDayMode is how a decision day is polled: from Lead before the
// decision the feed every RSSInterval and the yearly lists every
// ListInterval for FastFor, then the standard steps. A missing decision is
// alerted at AlertDecisionAt; the export waits for the statement until
// StatementWait and then goes out with the decision alone.
type ECBDayMode struct {
	Lead            time.Duration `yaml:"lead"`
	RSSInterval     time.Duration `yaml:"rss_interval"`
	ListInterval    time.Duration `yaml:"list_interval"`
	FastFor         time.Duration `yaml:"fast_for"`
	AlertDecisionAt string        `yaml:"alert_decision_at"`
	StatementWait   string        `yaml:"statement_wait"`

	AlertDecision, StatementBy ClockTime `yaml:"-"`
}

// Since is the clock time as a duration after midnight.
func (c ClockTime) Since() time.Duration {
	return time.Duration(c.Hour)*time.Hour + time.Duration(c.Minute)*time.Minute
}

func (e *ECBTexts) validate(cfg *Config) error {
	if !e.Enabled {
		return nil
	}
	var errs []error
	for key, v := range map[string]string{"base_url": e.BaseURL, "rss": e.RSS, "meeting_calendar": e.MeetingCalendar} {
		if v == "" || strings.Contains(v, "[[") {
			errs = append(errs, fmt.Errorf("ecb_texts.%s is empty", key))
		}
	}
	for key, v := range map[string]string{"decisions": e.Lists.Decisions, "statements": e.Lists.Statements, "accounts": e.Lists.Accounts} {
		if !strings.Contains(v, "{year}") {
			errs = append(errs, fmt.Errorf("ecb_texts.lists.%s %q: must contain {year}", key, v))
		}
	}
	loc, err := time.LoadLocation(e.Timezone)
	if err != nil || e.Timezone == "" {
		errs = append(errs, fmt.Errorf("ecb_texts.timezone %q: invalid", e.Timezone))
	}
	e.Location = loc
	clock := func(key, v string, dst *ClockTime) {
		t, err := parseClock(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("ecb_texts.%s: %w", key, err))
		}
		*dst = t
	}
	clock("times.decision", e.Times.Decision, &e.Times.DecisionAt)
	clock("times.press_conference", e.Times.PressConference, &e.Times.PressConferenceAt)
	clock("times.accounts", e.Times.Accounts, &e.Times.AccountsAt)
	clock("day_mode.alert_decision_at", e.DayMode.AlertDecisionAt, &e.DayMode.AlertDecision)
	clock("day_mode.statement_wait", e.DayMode.StatementWait, &e.DayMode.StatementBy)
	if e.PollInterval <= 0 {
		errs = append(errs, errors.New("ecb_texts.poll_interval must be positive"))
	}
	d := e.DayMode
	if d.Lead < 0 || d.RSSInterval <= 0 || d.ListInterval <= 0 || d.FastFor <= 0 {
		errs = append(errs, errors.New("ecb_texts.day_mode: lead must not be negative; rss_interval, list_interval and fast_for must be positive"))
	}
	if e.Times.DecisionAt.Since() > d.AlertDecision.Since() || d.AlertDecision.Since() > d.StatementBy.Since() ||
		e.Times.PressConferenceAt.Since() > d.StatementBy.Since() {
		errs = append(errs, errors.New("ecb_texts: want times.decision ≤ day_mode.alert_decision_at ≤ day_mode.statement_wait and times.press_conference ≤ day_mode.statement_wait"))
	}
	errs = append(errs, cfg.checkAgent("ecb_texts", e.Agent))
	return errors.Join(errs...)
}

// Deprecations are config keys that are read but no longer mean anything;
// the binaries log them at startup.
func (c *Config) Deprecations() []string {
	var out []string
	if c.ECBTexts.Feeds != nil {
		out = append(out, "ecb_texts.feeds is an obsolete placeholder and is ignored: delete it (README, «Тексты ЕЦБ»)")
	}
	return out
}
