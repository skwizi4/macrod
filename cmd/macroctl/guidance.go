package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"

	"macro-collector/internal/app"
	"macro-collector/internal/delivery"
	"macro-collector/internal/guidance"
	"macro-collector/internal/store"
)

const guidanceUsage = "guidance list [--all] | guidance approve <id> | guidance reject <id> | guidance scan [--ticker GOOGL] [--dry-run]   (capex guidance candidates, phase 14b; only an approved one reaches the export)"

// guidanceCmd lists, decides and scans capex guidance candidates.
func guidanceCmd(env *app.Env, args []string) error {
	if len(args) == 0 || !slices.Contains([]string{"list", "approve", "reject", "scan"}, args[0]) {
		return errors.New("usage: " + guidanceUsage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("guidance "+sub, flag.ExitOnError)
	all := fs.Bool("all", false, "list: decided candidates too")
	ticker := fs.String("ticker", "", "scan: only this ticker of edgar.guidance_tickers")
	dry := fs.Bool("dry-run", false, "scan: print the candidates without storing them")
	_ = fs.Parse(args[1:])
	cfg := env.Config
	if len(cfg.EDGAR.GuidanceTickers) == 0 {
		return errors.New("edgar.guidance_tickers is empty in the config")
	}
	var id int64
	if sub == "approve" || sub == "reject" {
		if fs.NArg() != 1 {
			return errors.New("usage: " + guidanceUsage)
		}
		var err error
		if id, err = strconv.ParseInt(fs.Arg(0), 10, 64); err != nil || id <= 0 {
			return fmt.Errorf("guidance %s: id %q: want a number from guidance list", sub, fs.Arg(0))
		}
	}
	if *ticker != "" && !slices.Contains(cfg.EDGAR.GuidanceTickers, *ticker) {
		return fmt.Errorf("guidance scan: %s is not in edgar.guidance_tickers", *ticker)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	svc := delivery.NewGuidance(cfg, st, env.Root)

	switch sub {
	case "list":
		statuses := []string{store.GuidancePending}
		if *all {
			statuses = nil
		}
		cands, err := st.GuidanceCandidates(ctx, statuses...)
		if err != nil {
			return err
		}
		current, err := svc.Current(ctx)
		if err != nil {
			return err
		}
		printCurrent(current)
		if len(cands) == 0 {
			fmt.Println("no candidates" + map[bool]string{false: " pending", true: ""}[*all])
		}
		for _, c := range cands {
			printCandidate(c)
		}
	case "approve", "reject":
		c, err := svc.Decide(ctx, id, sub == "approve", "cli")
		if errors.Is(err, store.ErrDecided) {
			return fmt.Errorf("guidance #%d is already %s", id, c.Status)
		}
		if err != nil {
			return err
		}
		fmt.Printf("guidance #%d %s: %s %d, %s\n", c.ID, c.Status, c.Ticker, c.Year, guidance.Range(c.Low, c.High))
		if c.Status == store.GuidanceApproved {
			fmt.Println("the next export of Micro uses it for implied Q4 (newer given than config.yaml wins)")
		}
	case "scan":
		cands, err := svc.Scan(ctx, *ticker, *dry)
		if err != nil {
			return err
		}
		verb := "new candidates stored"
		if *dry {
			verb = "candidates found (dry run, nothing stored)"
		}
		fmt.Printf("guidance scan: %d %s\n", len(cands), verb)
		for _, c := range cands {
			printCandidate(c)
		}
	}
	return nil
}

func printCurrent(vs []guidance.Value) {
	fmt.Println("in force (config.yaml + approved, newer given wins):")
	if len(vs) == 0 {
		fmt.Println("  none")
	}
	for _, v := range vs {
		from := "config.yaml"
		if !v.FromConfig() {
			from = fmt.Sprintf("approved #%d", v.CandidateID)
		}
		fmt.Printf("  %s %d: %s, given %s (%s; %s)\n", v.Ticker, v.Year, guidance.Range(v.Low, v.High), v.Given.Format("2006-01-02"), from, v.Source)
	}
	fmt.Println()
}

func printCandidate(c store.GuidanceCandidate) {
	id := "—"
	if c.ID != 0 {
		id = "#" + strconv.FormatInt(c.ID, 10)
	}
	status := c.Status
	if status == "" {
		status = store.GuidancePending
	}
	fmt.Printf("%s [%s] %s %d: %s, given %s\n", id, status, c.Ticker, c.Year, guidance.Range(c.Low, c.High), c.Given.Format("2006-01-02"))
	if c.Note != "" {
		fmt.Printf("    %s\n", c.Note)
	}
	fmt.Printf("    «%s»\n    %s\n    %s\n", c.Quote, c.Source(), c.URL)
	if !c.DecidedAt.IsZero() {
		fmt.Printf("    decided %s via %s\n", c.DecidedAt.Format("2006-01-02 15:04"), c.DecidedVia)
	}
}
