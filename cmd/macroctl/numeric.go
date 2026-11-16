package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/ingest"
	"macro-collector/internal/store"
)

// numericSourceNames are the --source values served by runNumeric.
func numericSourceNames(cfg *config.Config) []string {
	var out []string
	for _, s := range cfg.NumericSources() {
		out = append(out, s.Name)
	}
	return out
}

func isNumericSource(cfg *config.Config, name string) bool {
	return slices.Contains(numericSourceNames(cfg), name)
}

// runNumeric loads the series of the numeric sources in names; a source
// that is not enabled in the config is an error when asked for by name and
// skipped under --source all.
func runNumeric(env *app.Env, mode ingest.Mode, all bool, names ...string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	runners := collect.NumericRunners(env.Config, st, env.Root)
	failed := 0
	for _, name := range names {
		i := slices.IndexFunc(runners, func(r collect.NumericRunner) bool { return r.Name() == name })
		if i < 0 {
			if all {
				continue
			}
			return fmt.Errorf("source %s is not enabled in the config", name)
		}
		sum := collect.RunNumeric(ctx, env.Config, st, runners[i], env.Root, mode)
		rep := sum.FRED
		inserted, revised := sum.Rows()
		fmt.Printf("%s: %d series in %s — failed %d; rows inserted %d, revised %d\n",
			name, len(rep.Results), sum.Duration.Round(100_000_000), len(rep.Failed()), inserted, revised)
		for _, r := range rep.Results {
			if r.Err != nil {
				fmt.Printf("  FAILED %s: %v\n", r.ID, r.Err)
				continue
			}
			fmt.Printf("  %-22s +%d new, %d revised, %d unchanged\n", r.ID, r.Inserted, r.Revised, r.Unchanged)
		}
		printDerived(sum)
		if sum.Interrupted {
			return fmt.Errorf("interrupted; stored data is consistent, run again to continue")
		}
		failed += sum.Failures()
	}
	if failed > 0 {
		return fmt.Errorf("%d series failed", failed)
	}
	return nil
}
