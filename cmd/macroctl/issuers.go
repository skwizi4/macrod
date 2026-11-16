package main

import (
	"errors"
	"fmt"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
)

// runIssuers loads the annual facts and press releases of the EU Micro
// issuers (prices come with yahoo).
func runIssuers(env *app.Env) error {
	c := collect.NewIssuerClients(env.Config, env.Root)
	if c == nil {
		fmt.Println("issuers: disabled in config")
		return nil
	}
	ctx, done, st, err := openForRun(env)
	if err != nil {
		return err
	}
	defer done()
	start := time.Now()
	var errs []error
	for _, r := range collect.RunIssuers(ctx, env.Config, st, c, env.Root, start) {
		status := "ok"
		if r.Err != nil {
			status = "error: " + r.Err.Error()
			errs = append(errs, r.Err)
		}
		fmt.Printf("issuers %s: facts changed %d, quarterly facts changed %d, releases stored %d, report dates added or moved %d — %s\n", r.Ticker, r.Facts, r.Quarters, r.Releases, r.Dates, status)
	}
	return errors.Join(errs...)
}
