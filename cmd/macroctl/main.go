// Command macroctl is the CLI for one-off operations (ТЗ §13).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"macro-collector/internal/app"
	"macro-collector/internal/classify"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/logging"
	"macro-collector/internal/sources/ecbtexts"
	"macro-collector/internal/sources/fred"

	// Embedded zone database: the Docker image has no /usr/share/zoneinfo.
	_ "time/tzdata"
)

type command struct {
	usage string
	run   func(env *app.Env, args []string) error
}

var commands = map[string]command{
	"backfill": {
		usage: "backfill --source fred|treasury|yahoo|cftc|cboe|prediction|telegram|rss|finanzagentur|ecb|bundesbank|eurostat|stoxx|zew [--since 2000-01-01]   (telegram|rss: reload history_days)",
		run:   backfill,
	},
	"fetch": {
		usage: "fetch --source fred|fed|ecb_texts|treasury|yahoo|cftc|cboe|prediction|intraday|telegram|rss|posts|views|finanzagentur|ecb|bundesbank|eurostat|stoxx|zew|all   (views: Telegram views of the last 8 days)",
		run:   fetch,
	},
	"calendar": {
		usage: "calendar [--days 14] [--refresh] [--all]",
		run:   calendarCmd,
	},
	"export": {
		usage: "export [--region usa|eu|all] [--week 2026-W43] [--dry-run] [--no-deliver]   (writes exchange/, then git push, webhook and Telegram if configured)",
		run:   exportCmd,
	},
	"diff": {
		usage: "diff --type fomc|ecb [--latest | --a <id> --b <id>]",
		run:   diffCmd,
	},
	"status": {
		usage: "status [--all]",
		run:   statusCmd,
	},
	"classify": {
		usage: "classify [--reprocess [--since 2026-10-01]] [--keywords-only]   (tags pending posts)",
		run:   classifyCmd,
	},
	"source-check": {
		usage: "source-check [--days 30] tg:<handle> | rss:<url>   (try a channel or feed before adding it; stores nothing)",
		run:   sourceCheck,
	},
	"serve": {
		usage: "serve [--addr 127.0.0.1:8080]   (read-only dashboard; not loopback needs MACRO_DASHBOARD_PASSWORD)",
		run:   serveCmd,
	},
	"site": {
		usage: "site [--out site] [--demo] | site --publish   (static dashboard; --demo: only open data; --publish: demo build to site.repo now)",
		run:   siteCmd,
	},
	"forecast": {
		usage: forecastUsage,
		run:   forecastCmd,
	},
	"guidance": {
		usage: guidanceUsage,
		run:   guidanceCmd,
	},
	"doctor": {
		usage: "doctor [--local] [--min-free-gb 1] [--timeout 30s]   (config, keys, one request per source, database, disk; exit 1 on failure)",
		run:   doctorCmd,
	},
	"issuers": {
		usage: issuersUsage,
		run:   issuersCmd,
	},
	"input": {
		usage: inputUsage,
		run:   inputCmd,
	},
	"record-fixtures": {
		usage: "record-fixtures [--source fred|treasury|yahoo|cftc|gemini|edgar|edgar-instances|finanzagentur|ecb|bundesbank|eurostat|stoxx|zew] [--dir testdata/<source>]   (overwrites fixtures with live responses; gemini: answers on testdata/classify/labeled.json)",
		run:   recordFixtures,
	},
}

func recordFixtures(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("record-fixtures", flag.ExitOnError)
	source := fs.String("source", "fred", "source: fred | treasury | ecb_texts | yahoo | cftc | gemini | edgar | edgar-instances | finanzagentur | ecb | bundesbank | eurostat | stoxx | zew")
	dir := fs.String("dir", "", "output directory (default testdata/<source>)")
	_ = fs.Parse(args)
	if *dir == "" {
		*dir = filepath.Join("testdata", *source)
		if *source == "gemini" {
			*dir = filepath.Join("testdata", "classify")
		}
	}

	if isNumericSource(env.Config, *source) {
		src, ok := collect.NewNumericSource(env.Config, *source, env.Root)
		if !ok {
			return fmt.Errorf("source %s is not enabled in the config", *source)
		}
		rec, ok := src.(interface {
			RecordFixtures(ctx context.Context, dir string) error
		})
		if !ok {
			return fmt.Errorf("source %s records no fixtures", *source)
		}
		return rec.RecordFixtures(context.Background(), *dir)
	}
	switch *source {
	case "finanzagentur":
		return recordBundAuctions(env, *dir)
	case "ecb_texts":
		e := &env.Config.ECBTexts
		if !e.Enabled {
			return fmt.Errorf("ecb_texts is not enabled in the config")
		}
		site := collect.ECBSite(env.Config, collect.NewECBClient(env.Config, env.Root), env.Root)
		return site.RecordFixtures(context.Background(), *dir, e.RSS, e.MeetingCalendar, map[string]string{
			ecbtexts.DocDecision: e.Lists.Decisions, ecbtexts.DocStatement: e.Lists.Statements, ecbtexts.DocAccounts: e.Lists.Accounts,
		}, time.Now().In(e.Location).Year())
	case "fred":
		client, err := fred.New(os.Getenv(fred.EnvAPIKey), fred.WithLogger(logging.For(env.Root, "fred")))
		if err != nil {
			return err
		}
		return client.RecordFixtures(context.Background(), *dir)
	case "treasury":
		return newTreasuryClient(env).RecordFixtures(context.Background(), *dir)
	case "yahoo":
		return newYahooClient(env).RecordFixtures(context.Background(), *dir)
	case "cftc":
		return newCFTCClient(env).RecordFixtures(context.Background(), *dir)
	case "gemini":
		g, err := newGemini(env)
		if err != nil {
			return err
		}
		return g.RecordFixtures(context.Background(), *dir)
	case "edgar":
		c, err := newEDGARClient(env)
		if err != nil {
			return err
		}
		return c.RecordFixtures(context.Background(), *dir)
	case "edgar-instances": // phase 5d: trimmed XBRL instances next to the edgar fixtures
		c, err := newEDGARClient(env)
		if err != nil {
			return err
		}
		if *dir == filepath.Join("testdata", "edgar-instances") {
			*dir = filepath.Join("testdata", "edgar")
		}
		return c.RecordInstanceFixtures(context.Background(), *dir, collect.InstanceSelects(env.Config))
	}
	return fmt.Errorf("--source must be fred, treasury, yahoo, cftc, gemini, edgar, edgar-instances or %s, got %q",
		strings.Join(numericSourceNames(env.Config), ", "), *source)
}

// newGemini is the Gemini classifier of the config; it needs GEMINI_API_KEY.
func newGemini(env *app.Env) (*classify.Gemini, error) {
	cfg := env.Config.Classify.Gemini
	key := os.Getenv(config.EnvGeminiKey)
	if key == "" {
		return nil, fmt.Errorf("%s is not set (.env)", config.EnvGeminiKey)
	}
	prompt, err := classify.LoadPrompt(cfg.PromptPath, cfg.Tags)
	if err != nil {
		return nil, err
	}
	return classify.NewGemini(cfg, key, prompt, logging.For(env.Root, "classify")), nil
}

func main() {
	global := flag.NewFlagSet("macroctl", flag.ExitOnError)
	configPath := global.String("config", "config.yaml", "path to config.yaml")
	envPath := global.String("env", ".env", "path to .env (missing file is fine)")
	dataDir := global.String("data-dir", "", "data directory; overrides env "+config.EnvDataDir+" and data_dir in the config")
	global.Usage = func() { usage(global) }
	_ = global.Parse(os.Args[1:])

	if global.NArg() == 0 {
		usage(global)
		os.Exit(2)
	}
	name := global.Arg(0)
	if name == initCommand {
		if err := runInit(*configPath, global.Args()[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "macroctl %s: %v\n", name, err)
			os.Exit(1)
		}
		return
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "macroctl: unknown command %q\n\n", name)
		usage(global)
		os.Exit(2)
	}

	env, err := app.Setup("macroctl", *configPath, *envPath, *dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "macroctl:", err)
		os.Exit(1)
	}
	err = cmd.run(env, global.Args()[1:])
	env.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "macroctl %s: %v\n", name, err)
		os.Exit(1)
	}
}

func usage(fs *flag.FlagSet) {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("usage: macroctl [--config config.yaml] [--env .env] [--data-dir DIR] <command> [flags]\n\ncommands:\n")
	fmt.Fprintf(&b, "  %s\n", initUsage)
	for _, n := range names {
		fmt.Fprintf(&b, "  %s\n", commands[n].usage)
	}
	b.WriteString("\nglobal flags:\n")
	fmt.Fprint(os.Stderr, b.String())
	fs.SetOutput(os.Stderr)
	fs.PrintDefaults()
}
