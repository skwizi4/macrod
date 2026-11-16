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
	"macro-collector/internal/dashboard"
	"macro-collector/internal/store"
	"macro-collector/web"
)

func serveCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address; not loopback needs MACRO_DASHBOARD_PASSWORD")
	_ = fs.Parse(args)

	user := os.Getenv("MACRO_DASHBOARD_USER")
	if user == "" {
		user = "macro"
	}
	auth := dashboard.Auth{User: user, Password: os.Getenv("MACRO_DASHBOARD_PASSWORD")}
	if err := dashboard.CheckListen(*addr, auth); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.OpenReadOnly(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	srv := &dashboard.Server{
		Data:   &dashboard.Data{Config: env.Config, Store: st, Now: time.Now},
		Auth:   auth,
		Static: web.FS,
		Log:    env.Root,
	}
	fmt.Printf("dashboard: http://%s (auth: %v)\n", *addr, auth.Enabled())
	return srv.Serve(ctx, *addr)
}
