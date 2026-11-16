package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/dashboard"
	"macro-collector/internal/delivery"
	"macro-collector/internal/export"
	"macro-collector/internal/store"
	"macro-collector/web"
)

func siteCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("site", flag.ExitOnError)
	out := fs.String("out", "site", "output folder")
	demo := fs.Bool("demo", false, "only open data: dashboard.demo_allow, EDGAR facts, official schedules (D24)")
	publish := fs.Bool("publish", false, "build the demo site into site.dir, check it, commit and push to site.repo")
	_ = fs.Parse(args)
	if *publish {
		return sitePublish(env)
	}

	ctx := context.Background()
	st, err := store.OpenReadOnly(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if *demo && len(env.Config.Dashboard.DemoAllow) == 0 {
		fmt.Println("warning: dashboard.demo_allow is empty: every series chart is hidden")
	}
	d := &dashboard.Data{Config: env.Config, Store: st, Now: time.Now, Demo: *demo}
	n, err := d.BuildSite(ctx, *out, web.FS)
	if err != nil {
		return err
	}
	fmt.Printf("site: %d files in %s (demo: %v)\n", n, *out, *demo)
	return nil
}

// sitePublish is what the daemon does after the weekly export, now: the
// first publication or a repeat after a failure.
func sitePublish(env *app.Env) error {
	cfg := env.Config
	if !cfg.Site.Enabled {
		return errors.New("site --publish: site.enabled is false")
	}
	ctx := context.Background()
	st, err := store.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	s := delivery.NewSite(ctx, cfg, st, delivery.NewSiteRepo(cfg, env.Root), delivery.NewTelegram(cfg, env.Root), env.Root)
	now := time.Now()
	iso := export.ISOWeek(export.LastPeriodEnd(now, cfg.Location).Add(-time.Minute), cfg.Location)
	res, err := s.Publish(ctx, iso)
	if err != nil {
		return err
	}
	if res.Pushed {
		fmt.Printf("site: %d files, commit %s pushed → %s\n", res.Files, res.Commit, cfg.Site.URL)
	} else {
		fmt.Printf("site: %d files, nothing changed (%s)\n", res.Files, cfg.Site.URL)
	}
	return nil
}
