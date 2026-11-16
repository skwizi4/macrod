package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/config"
	"macro-collector/internal/status"
	"macro-collector/internal/store"
)

func statusCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	all := fs.Bool("all", false, "list every series, not only problems")
	_ = fs.Parse(args)

	ctx := context.Background()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	rep, err := status.Build(ctx, env.Config, st, time.Now())
	if err != nil {
		return err
	}
	loc := env.Config.Location

	fmt.Printf("db: %s — %.1f MB, schema v%d\n", rep.DBPath, float64(rep.DBBytes)/(1<<20), rep.SchemaVersion)
	for _, s := range rep.Summaries() {
		fmt.Printf("%s: %d series, last successful fetch %s, errors %d, stale %d, never fetched %d\n",
			s.Source, s.Series, fmtTime(s.LastOK, loc), s.Errors, s.Stale, s.Never)
	}
	printCalendarStatus(rep, env.Config.Calendar.ReleaseLocation, loc)
	printPostsStatus(rep, loc)
	printCompaniesStatus(rep, loc)
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATE\tSERIES\tFREQ\tLAST OBS\tLAST FETCH\tERROR")
	shown := 0
	for _, r := range rep.Rows {
		if r.OK() && !*all {
			continue
		}
		shown++
		fmt.Fprintf(w, "%s\t%s/%s\t%s\t%s\t%s\t%s\n",
			state(r), r.Source, r.ID, dash(r.Frequency), fmtDate(r.LastObs), fmtTime(r.LastOK, loc), dash(r.Error))
	}
	if shown == 0 {
		fmt.Println("all series ok (use --all to list them)")
		return nil
	}
	return w.Flush()
}

func printCalendarStatus(rep status.Report, et, loc *time.Location) {
	for _, group := range [][]status.State{rep.Calendars, rep.Texts} {
		for _, s := range group {
			line := fmt.Sprintf("%s: last successful %s", s.Source, fmtTime(s.LastOK, loc))
			if s.Error != "" {
				line += fmt.Sprintf(", ERROR at %s: %s", fmtTime(s.ErrorAt, loc), s.Error)
			}
			fmt.Println(line)
		}
	}
	for _, s := range rep.Datasets {
		line := fmt.Sprintf("%s: last successful %s", s.Source, fmtTime(s.LastOK, loc))
		if s.Error != "" {
			line += fmt.Sprintf(", ERROR at %s: %s", fmtTime(s.ErrorAt, loc), s.Error)
		}
		fmt.Println(line)
	}
	fmt.Printf("auctions: last %d days %d with results, %d past without results\n",
		status.AuctionDays, rep.AuctionsHeld, rep.AuctionsPending)
	for _, x := range rep.Exports {
		line := fmt.Sprintf("exports: weekly %s %s, fomc event %s", x.Region, fmtTime(x.Weekly, loc), fmtTime(x.FOMC, loc))
		if !x.ECB.IsZero() || len(rep.ECBDocs) > 0 {
			line += ", ecb event " + fmtTime(x.ECB, loc)
		}
		fmt.Println(line)
	}
	if len(rep.Calendars) == 0 {
		fmt.Println("calendar: never refreshed (macroctl calendar --refresh)")
	}
	var docs []string
	for _, t := range slices.Sorted(maps.Keys(rep.Documents)) {
		docs = append(docs, fmt.Sprintf("%s %d", t, rep.Documents[t]))
	}
	fmt.Printf("fed documents: %s\n", dash(strings.Join(docs, ", ")))
	if len(rep.ECBDocs) > 0 {
		var ecb []string
		for _, t := range slices.Sorted(maps.Keys(rep.ECBDocs)) {
			ecb = append(ecb, fmt.Sprintf("%s %d", t, rep.ECBDocs[t]))
		}
		fmt.Printf("ecb documents: %s\n", strings.Join(ecb, ", "))
	}

	next := "—"
	for _, e := range rep.Upcoming {
		if e.Importance == "high" {
			next = e.Name + " " + e.ScheduledAt.In(et).Format("2006-01-02 15:04") + " ET"
			break
		}
	}
	fmt.Printf("events: next %d days %d (next high: %s), polling now %d, timed out in %d days %d\n",
		status.UpcomingDays, len(rep.Upcoming), next, rep.Polling, status.TimeoutDays, len(rep.Timeouts))
	for _, e := range rep.Timeouts {
		fmt.Printf("  TIMEOUT %s %s ET\n", e.Name, e.ScheduledAt.In(et).Format("2006-01-02 15:04"))
	}
}

func state(r status.Row) string {
	switch {
	case r.Error != "":
		return "ERROR"
	case !r.Fetched():
		return "NEVER"
	case r.Stale:
		return "STALE"
	}
	return "ok"
}

func fmtDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format(time.DateOnly)
}

func fmtTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func printPostsStatus(rep status.Report, loc *time.Location) {
	for _, p := range rep.PostSources {
		line := fmt.Sprintf("%s: %d posts (%d in %d days), last post %s, last successful fetch %s",
			p.Source, p.Posts, p.Recent, status.PostDays, fmtTime(p.LastPost, loc), fmtTime(p.LastOK, loc))
		if p.Error != "" {
			line += fmt.Sprintf(", ERROR at %s: %s", fmtTime(p.ErrorAt, loc), p.Error)
		}
		fmt.Println(line)
	}
	c := rep.Classify
	fmt.Printf("posts in %d days: gemini %d, keywords %d, junk %d, uncategorized %d, not classified %d, duplicates %d\n",
		status.PostDays, c.Gemini, c.Keywords, c.Junk, c.Uncategorized, c.Pending, c.Duplicates)
	key := "set"
	if os.Getenv(config.EnvGeminiKey) == "" {
		key = "NOT SET (keywords only)"
	}
	if !rep.GeminiEnabled {
		key = "disabled in config (keywords only)"
	}
	fmt.Printf("gemini: requests today %d of %d (Pacific day), key %s\n", rep.GeminiToday, rep.GeminiLimit, key)
}

func printCompaniesStatus(rep status.Report, loc *time.Location) {
	for _, c := range rep.Companies {
		last := "—"
		if r := c.LastReport; r.Accession != "" {
			last = r.ReportDate.Format(time.DateOnly) + " " + r.Session
			if r.DocumentID == 0 {
				last += " (press release not stored yet)"
			}
		}
		filed := "—"
		if !c.LastFiled.IsZero() {
			filed = c.LastFiled.Format(time.DateOnly)
		}
		line := fmt.Sprintf("%s: %d earnings reports, last %s; %d facts, newest filing %s; last successful fetch %s",
			c.Source, c.Reports, last, c.Facts, filed, fmtTime(c.LastOK, loc))
		if c.Error != "" {
			line += fmt.Sprintf(", ERROR at %s: %s", fmtTime(c.ErrorAt, loc), c.Error)
		}
		fmt.Println(line)
	}
}
