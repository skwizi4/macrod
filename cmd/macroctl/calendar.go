package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/fedtexts"
	"macro-collector/internal/sources/fred"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/store"
)

func calendarCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("calendar", flag.ExitOnError)
	days := fs.Int("days", 14, "days ahead to show")
	refresh := fs.Bool("refresh", false, "update the calendar from FRED, federalreserve.gov, Treasury and ecb.europa.eu first")
	all := fs.Bool("all", false, "also show rescheduled events")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	var refreshErr error
	if *refresh {
		fredClient, err := fred.New(os.Getenv(fred.EnvAPIKey), fred.WithLogger(logging.For(env.Root, "fred")))
		if err != nil {
			return err
		}
		fedClient := fedtexts.New(httpx.WithLogger(logging.For(env.Root, "fed")))
		sum := collect.RefreshCalendars(ctx, env.Config, st, fredClient, fedClient, newTreasuryClient(env),
			collect.NewEarningsClient(env.Config, env.Root), env.Root)
		printCalendarSummary(sum)
		refreshErr = sum.Err()
	}

	now := time.Now()
	evs, err := st.Releases(ctx, now, now.AddDate(0, 0, *days))
	if err != nil {
		return err
	}
	et, local := env.Config.Calendar.ReleaseLocation, env.Config.Location
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ET\t%s\tEVENT\tIMPORTANCE\tSTATUS\tRELATED\n", strings.ToUpper(local.String()))
	shown, hidden := 0, 0
	for _, e := range evs {
		if e.Status == model.StatusRescheduled && !*all {
			hidden++
			continue
		}
		shown++
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ScheduledAt.In(et).Format("Mon 2006-01-02 15:04"), e.ScheduledAt.In(local).Format("15:04"),
			e.Name, dash(e.Importance), e.Status, related(e.Related))
	}
	if shown == 0 {
		fmt.Printf("no events in the next %d days (run with --refresh to update the calendar)\n", *days)
	} else if err := w.Flush(); err != nil {
		return err
	}
	if hidden > 0 {
		fmt.Printf("%d rescheduled events hidden (--all to show)\n", hidden)
	}
	return refreshErr
}

func printCalendarSummary(sum collect.CalendarSummary) {
	if sum.FREDErr != nil {
		fmt.Printf("fred calendar: FAILED: %v\n", sum.FREDErr)
	} else {
		r := sum.FRED
		fmt.Printf("fred calendar %s…%s: %d releases, %d events (new %d, rescheduled %d), release lookups %d\n",
			r.From.Format(time.DateOnly), r.To.Format(time.DateOnly), r.Releases, r.Events, r.Inserted, r.Rescheduled, r.Lookups)
		if len(r.LookupsFailed) > 0 {
			fmt.Printf("  release lookup FAILED for: %s\n", strings.Join(r.LookupsFailed, ", "))
		}
	}
	if sum.FedErr != nil {
		fmt.Printf("fed calendar: FAILED: %v\n", sum.FedErr)
	} else {
		r := sum.Fed
		fmt.Printf("fed calendar %s…%s: %d events (new %d, rescheduled %d)\n",
			r.From.Format(time.DateOnly), r.To.Format(time.DateOnly), r.Events, r.Inserted, r.Rescheduled)
	}
	if sum.MeetingsErr != nil {
		fmt.Printf("fomc meetings: FAILED: %v\n", sum.MeetingsErr)
	} else {
		fmt.Printf("fomc meetings %s…%s (for FED_PATH): %d decisions (new %d)\n",
			sum.Meetings.From.Format(time.DateOnly), sum.Meetings.To.Format(time.DateOnly), sum.Meetings.Events, sum.Meetings.Inserted)
	}
	if sum.ECBErr != nil {
		fmt.Printf("ecb meetings: FAILED: %v\n", sum.ECBErr)
	} else if sum.ECB.Events > 0 || sum.ECB.Inserted > 0 {
		fmt.Printf("ecb meetings %s…%s: %d events (new %d, rescheduled %d)\n",
			sum.ECB.From.Format(time.DateOnly), sum.ECB.To.Format(time.DateOnly), sum.ECB.Events, sum.ECB.Inserted, sum.ECB.Rescheduled)
	}
	for _, name := range slices.Sorted(maps.Keys(sum.Release.Calendars)) {
		if err := sum.Release.Errors[name]; err != nil {
			fmt.Printf("%s calendar: FAILED: %v\n", name, err)
			continue
		}
		r := sum.Release.Calendars[name]
		fmt.Printf("%s calendar %s…%s: %d events (new %d, rescheduled %d)\n",
			name, r.From.Format(time.DateOnly), r.To.Format(time.DateOnly), r.Events, r.Inserted, r.Rescheduled)
	}
	var partial *sources.PartialError
	if sum.TreasuryErr != nil && !errors.As(sum.TreasuryErr, &partial) {
		fmt.Printf("treasury calendar: FAILED: %v\n", sum.TreasuryErr)
	} else {
		r := sum.Treasury
		fmt.Printf("treasury calendar %s…%s: %d events (new %d, rescheduled %d)\n",
			r.From.Format(time.DateOnly), r.To.Format(time.DateOnly), r.Events, r.Inserted, r.Rescheduled)
		if partial != nil {
			fmt.Printf("  FAILED part (series kept as stored): %v\n", partial)
		}
	}
	if r := sum.Earnings; !r.From.IsZero() || sum.EarningsErr != nil {
		fmt.Printf("earnings calendar %s…%s: %d reports in window (new %d, rescheduled %d)",
			r.From.Format(time.DateOnly), r.To.Format(time.DateOnly), r.Events, r.Inserted, r.Rescheduled)
		if len(sum.EarningsNoDate) > 0 {
			fmt.Printf("; no announced date: %s", strings.Join(sum.EarningsNoDate, ", "))
		}
		fmt.Println()
		if sum.EarningsErr != nil {
			fmt.Printf("  FAILED: %v\n", sum.EarningsErr)
		}
	}
	fmt.Println()
}

func related(r model.Related) string {
	switch {
	case r.Meeting != "" && r.SEP:
		return "meeting " + r.Meeting + " (SEP)"
	case r.Meeting != "":
		return "meeting " + r.Meeting
	case r.CUSIP != "":
		return "cusip " + r.CUSIP
	case r.Ticker != "":
		s := r.Ticker
		if r.ReportTime != "" {
			s += " " + r.ReportTime
		}
		if r.Estimated {
			s += " (estimated date)"
		}
		return s
	case len(r.Series) > 4:
		return fmt.Sprintf("%s +%d", strings.Join(r.Series[:4], " "), len(r.Series)-4)
	default:
		return strings.Join(r.Series, " ")
	}
}
