package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/store"
)

const inputUsage = "input <series> <value> --period YYYY-MM   (a value typed by hand for a series of manual.series: Ifo; stored with source=manual)"

// inputArgs splits the arguments of `input`: the flag may stand anywhere
// and a negative value ("-35.9") is not a flag.
func inputArgs(args []string) (series, value, period string, err error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--period" || a == "-period":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--period needs a value")
			}
			i++
			period = args[i]
		case strings.HasPrefix(a, "--period=") || strings.HasPrefix(a, "-period="):
			_, period, _ = strings.Cut(a, "=")
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 || period == "" {
		return "", "", "", fmt.Errorf("usage: %s", inputUsage)
	}
	return pos[0], pos[1], period, nil
}

// inputCmd stores one manual value. The daemon may be running: the write
// goes through the store's writer like every other one.
func inputCmd(env *app.Env, args []string) error {
	id, raw, period, err := inputArgs(args)
	if err != nil {
		return err
	}
	value, err := strconv.ParseFloat(strings.Replace(raw, ",", ".", 1), 64)
	if err != nil {
		return fmt.Errorf("value %q is not a number", raw)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := collect.ManualInput(ctx, env.Config, st, env.Root, id, value, period, time.Now())
	if err != nil {
		return err
	}
	what := "stored"
	switch {
	case res.Revised > 0:
		what = fmt.Sprintf("revised (was %v)", res.Old)
	case res.Unchanged > 0:
		what = "unchanged"
	}
	fmt.Printf("%s/%s %s = %v — %s (%s)\n", config.SourceManual, res.Series.ID, period, value, what, res.Series.Label)
	return nil
}
