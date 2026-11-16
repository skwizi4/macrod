package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/store"
)

const issuersUsage = `issuers file [TICKER] <path.xlsx> [--period "Q3 2026"]   (quarterly Excel of an issuer, e.g. Allianz: read like a downloaded one; without TICKER — the only issuer with quarterly.source: file)`

// issuersCmd: `issuers file` stores the quarterly numbers of a workbook
// the user has (phase 14c, D29). The daemon may be running: the write goes
// through the store's writer.
func issuersCmd(env *app.Env, args []string) error {
	if len(args) == 0 || args[0] != "file" {
		return fmt.Errorf("usage: %s", issuersUsage)
	}
	var pos []string
	period := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--period" || a == "-period":
			if i+1 >= len(args) {
				return fmt.Errorf("--period needs a value")
			}
			i++
			period = args[i]
		case strings.HasPrefix(a, "--period="):
			period = strings.TrimPrefix(a, "--period=")
		default:
			pos = append(pos, a)
		}
	}
	var ticker, path string
	switch len(pos) {
	case 1:
		path = pos[0]
	case 2:
		ticker, path = pos[0], pos[1]
	default:
		return fmt.Errorf("usage: %s", issuersUsage)
	}
	is, f, err := collect.FileIssuer(env.Config, ticker, filepath.Base(path), period)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	pairs, n, err := collect.IssuerQuarterlyFile(ctx, st, is, f, data, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("%s Q%d %d (%s): facts changed %d\n", is.Name, f.Q, f.FY, f.Name, n)
	for _, l := range collect.QuarterSummary(is, f, pairs) {
		fmt.Println("  " + l)
	}
	return nil
}
