package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/delivery"
	"macro-collector/internal/export"
	"macro-collector/internal/logging"
	"macro-collector/internal/store"
)

func exportCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	region := fs.String("region", "all", "region id from the config, or all")
	weekStr := fs.String("week", "", "ISO week, e.g. 2026-W43 (default: the last finished week)")
	dry := fs.Bool("dry-run", false, "render without writing files")
	noDeliver := fs.Bool("no-deliver", false, "write files only: no git push, webhook or Telegram message")
	_ = fs.Parse(args)
	regions, err := env.Config.SelectRegions(*region)
	if err != nil {
		return err
	}
	loc := env.Config.Location
	end := export.LastPeriodEnd(time.Now(), loc)
	if *weekStr != "" {
		var err error
		if end, err = export.PeriodEndOfWeek(*weekStr, loc); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	log := logging.For(env.Root, "export")
	var sink export.EventSink = export.LogSink{Log: log}
	var chain *delivery.Chain
	if !*noDeliver && !*dry {
		repo := delivery.NewRepo(env.Config, env.Root)
		if repo != nil {
			repo.Retry = nil // one attempt; the daemon's hourly job pushes what is left
		}
		if chain = delivery.New(ctx, env.Config, repo, delivery.NewWebhook(env.Config, env.Root), delivery.NewTelegram(env.Config, env.Root), env.Root); chain != nil {
			sink = chain
		}
	}
	var results []export.WeeklyResult
	var runErr error
	for _, r := range regions {
		x := &export.Weekly{Store: st, Config: env.Config, Region: r, Sink: sink, Log: log}
		res, err := x.Run(ctx, end, *dry)
		if err != nil {
			runErr = fmt.Errorf("region %s: %w", r.ID, err)
			break
		}
		results = append(results, res)
	}
	if chain != nil {
		chain.Wait() // git push and message before exit
	}
	if runErr != nil {
		return runErr
	}
	for i, res := range results {
		fmt.Printf("export %s %s: %s → %s (%s)\n", regions[i].ID, res.ISOWeek, res.From.In(loc).Format("2006-01-02 15:04"),
			res.To.In(loc).Format("2006-01-02 15:04"), loc)
		for _, f := range res.Files {
			path := f.Path
			if f.ArchiveOnly {
				path = "archive/" + res.ISOWeek + "/" + f.Path
			}
			fmt.Printf("  %-40s %6d chars\n", path, len([]rune(f.Body)))
		}
		if *dry {
			r := regions[i]
			ok := 0
			for j := range r.Agents {
				a := &r.Agents[j]
				if _, err := export.LoadPrompt(env.Config.PromptPath(r, a), r.ID+"/"+a.ID); err != nil {
					fmt.Printf("  %s: %v\n", export.InstructionsFile, err)
					continue
				}
				ok++
			}
			fmt.Printf("  %s: %d of %d agents\n", export.InstructionsFile, ok, len(r.Agents))
			fmt.Println("dry run: nothing written")
			continue
		}
		fmt.Printf("written %d files (with archive/%s/) to %s\n", len(res.Written)-len(res.Previous), res.ISOWeek, env.Config.Export.ExchangeDir)
		for _, p := range res.Previous {
			fmt.Printf("  previous report → %s\n", p)
		}
		fmt.Printf("  %s: %d of %d agents\n", export.InstructionsFile, len(res.Prompts), len(regions[i].Agents))
		for _, a := range res.NoPrompt {
			fmt.Printf("  ⚠ no prompt for %s: %s removed\n", a, export.InstructionsFile)
		}
		switch {
		case res.ForecastErr != "":
			fmt.Printf("  ⚠ forecast log: %s (scorecard not updated)\n", res.ForecastErr)
		case res.Forecast != nil:
			fmt.Printf("  %s: %d scored now, %d pending, %d reports without a usable block\n",
				res.Forecast.Path, res.Forecast.Score.Scored, res.Forecast.Score.Pending, len(res.Forecast.Import.Notes))
		}
	}
	return nil
}
