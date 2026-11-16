package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/delivery"
	"macro-collector/internal/ingest"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/store"
)

// newEDGARClient is the EDGAR client of the config (User-Agent, rate).
func newEDGARClient(env *app.Env) (*edgar.Client, error) {
	c, ok := collect.NewEDGARClient(env.Config, env.Root).(*edgar.Client)
	if !ok {
		return nil, fmt.Errorf("edgar: disabled in config")
	}
	return c, nil
}

// runEDGAR collects earnings reports and XBRL facts of the companies.
// Backfill re-reads every company's facts; fetch reads them only when a
// new 10-Q/10-K is not stored yet.
func runEDGAR(env *app.Env, mode ingest.Mode) error {
	client, err := newEDGARClient(env)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rep, took := collect.RunEDGAR(ctx, env.Config, st, client, delivery.NewGuidance(env.Config, st, env.Root), env.Root, mode)
	newReports, facts := 0, 0
	for _, r := range rep.Results {
		newReports += r.NewReports
		facts += r.Facts
		status := "ok"
		if r.Err != nil {
			status = "FAILED: " + r.Err.Error()
		}
		last := "—"
		if len(r.Reports) > 0 {
			l := r.Reports[0]
			last = fmt.Sprintf("%s %s (%s)", l.ReportDate.Format(time.DateOnly), l.Session, l.Accession)
		}
		factsState := "facts current"
		if !r.FactsCurrent {
			factsState = "facts of the latest 10-Q/10-K not available yet"
		}
		fmt.Printf("  %-5s reports %d (new %d), last %s; facts read %v, changed %d, %s — %s\n",
			r.Ticker, len(r.Reports), r.NewReports, last, r.FactsRead, r.Facts, factsState, status)
	}
	fmt.Printf("edgar: %d companies in %s — failed %d; new reports %d, fact rows changed %d\n",
		len(rep.Results), took.Round(time.Second), len(rep.Failed()), newReports, facts)
	if rep.Interrupted {
		return fmt.Errorf("interrupted; stored data is consistent, run again to continue")
	}
	if n := len(rep.Failed()); n > 0 {
		return fmt.Errorf("%d companies failed", n)
	}
	return nil
}
