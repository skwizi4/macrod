package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"macro-collector/internal/app"
	"macro-collector/internal/collect"
	"macro-collector/internal/config"
	"macro-collector/internal/logging"
	"macro-collector/internal/model"
	"macro-collector/internal/sources"
	"macro-collector/internal/sources/httpx"
	"macro-collector/internal/sources/rss"
	"macro-collector/internal/sources/telegram"
	"macro-collector/internal/store"
)

// postsPrefix maps --source to the source name prefix.
func postsPrefix(source string) string {
	switch source {
	case "telegram":
		return telegram.SourcePrefix
	case "rss":
		return rss.SourcePrefix
	}
	return ""
}

// runPosts fetches channels and feeds of source (telegram | rss | posts),
// then classifies and deduplicates. With reload the cursors are reset
// first, so history_days of history is loaded again.
func runPosts(env *app.Env, source string, reload bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	posts, err := collect.NewPosts(env.Config, st, env.Root, os.Getenv(config.EnvGeminiKey))
	if err != nil {
		return err
	}
	posts = posts.Only(postsPrefix(source))
	if len(posts.Sources) == 0 {
		fmt.Printf("%s: no sources in config\n", source)
		return nil
	}
	if reload {
		now := time.Now()
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			for _, s := range posts.Sources {
				if err := store.SetFetchOK(tx, s.Name(), "", now); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	sum := posts.Run(ctx)
	printPosts(env, sum)
	if ctx.Err() != nil {
		return fmt.Errorf("interrupted; stored posts are consistent, run again to continue")
	}
	if n := len(sum.Failed()); n > 0 || sum.Err != nil {
		return fmt.Errorf("%d sources failed: %w", n, sum.Error())
	}
	return nil
}

func printPosts(env *app.Env, sum collect.PostsSummary) {
	for _, r := range sum.Fetch {
		if r.Err != nil {
			fmt.Printf("%-28s FAILED: %v\n", r.Source, r.Err)
			continue
		}
		fmt.Printf("%-28s new %d, updated %d, unchanged %d\n", r.Source, len(r.Inserted), r.Updated, r.Unchanged)
	}
	printClassify(sum)
}

func printClassify(sum collect.PostsSummary) {
	fmt.Println("classify:", sum.Classify)
	fmt.Printf("dedup: hashed %d, duplicates in checked window %d, changed %d\n", sum.Dedup.Hashed, sum.Dedup.Duplicates, sum.Dedup.Changed)
	if sum.Err != nil {
		fmt.Println("ERROR:", sum.Err)
	}
}

// classifyCmd classifies pending posts; --reprocess first drops the
// classification of posts published since --since.
func classifyCmd(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("classify", flag.ExitOnError)
	reprocess := fs.Bool("reprocess", false, "classify again posts published since --since")
	sinceStr := fs.String("since", "", "with --reprocess: start date YYYY-MM-DD (default: 30 days ago)")
	keywordsOnly := fs.Bool("keywords-only", false, "do not use Gemini")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	key := os.Getenv(config.EnvGeminiKey)
	if *keywordsOnly {
		key = ""
	}
	posts, err := collect.NewPosts(env.Config, st, env.Root, key)
	if err != nil {
		return err
	}
	if *reprocess {
		since := time.Now().AddDate(0, 0, -30)
		if *sinceStr != "" {
			if since, err = time.ParseInLocation(time.DateOnly, *sinceStr, env.Config.Location); err != nil {
				return fmt.Errorf("--since: %w", err)
			}
		}
		var n int64
		if err := st.Write(ctx, func(tx *sql.Tx) error {
			n, err = store.ResetClassification(tx, since)
			return err
		}); err != nil {
			return err
		}
		fmt.Printf("reset %d posts published since %s\n", n, since.Format(time.DateOnly))
	} else if *sinceStr != "" {
		return fmt.Errorf("--since needs --reprocess")
	}
	sum := posts.Process(ctx, collect.PostsSummary{})
	printClassify(sum)
	return sum.Err
}

// sourceCheck fetches a channel or feed that may not be in the config yet
// and prints what it would bring; nothing is stored.
func sourceCheck(env *app.Env, args []string) error {
	fs := flag.NewFlagSet("source-check", flag.ExitOnError)
	days := fs.Int("days", env.Config.Posts.HistoryDays, "history to look at, days")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: source-check [--days 30] tg:<handle> | rss:<url>")
	}
	arg := fs.Arg(0)
	var src sources.TextSource
	switch {
	case strings.HasPrefix(arg, "tg:"):
		handle := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(arg, "tg:"), "https://t.me/"), "@")
		handle = strings.TrimPrefix(handle, "s/")
		src = telegram.New(env.Config.Telegram.BaseURL, config.Channel{Handle: handle, Region: env.Config.RegionIDs()[0], Type: "news", HistoryDays: *days},
			telegram.NewHTTP(logging.For(env.Root, "telegram")), nil)
	case strings.HasPrefix(arg, "rss:"):
		src = rss.New(config.RSSFeed{Name: "check", URL: strings.TrimPrefix(arg, "rss:"), Region: env.Config.RegionIDs()[0], Type: "news", HistoryDays: *days},
			httpx.New(httpx.WithLogger(logging.For(env.Root, "rss"))), nil)
	default:
		return fmt.Errorf("want tg:<handle> or rss:<url>, got %q", arg)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	docs, _, err := src.FetchDocuments(ctx, "")
	if err != nil {
		return fmt.Errorf("%s: FAILED: %w", arg, err)
	}
	fmt.Printf("%s: ok, %d posts in the last %d days\n", arg, len(docs), *days)
	if len(docs) == 0 {
		fmt.Println("  no posts in this period: try a larger --days, or check that the source is active")
		return nil
	}
	loc := env.Config.Location
	fmt.Printf("  first %s, last %s, language %s\n", docs[0].PublishedAt.In(loc).Format("2006-01-02 15:04"),
		docs[len(docs)-1].PublishedAt.In(loc).Format("2006-01-02 15:04"), language(docs))
	for i := len(docs) - 1; i >= 0 && i >= len(docs)-3; i-- {
		fmt.Printf("  %s  %s\n", docs[i].PublishedAt.In(loc).Format("2006-01-02 15:04"), preview(docs[i]))
	}
	fmt.Println("  to add it: one line in config.yaml (telegram.channels or rss_feeds), then restart macrod")
	return nil
}

func preview(d model.Document) string {
	s := d.Title
	if s == "" {
		s = d.Body
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 90 {
		s = string(r[:90]) + "…"
	}
	return s
}

// language guesses the main language by script: Latin is English;
// Cyrillic with і ї є ґ is Ukrainian, otherwise Russian.
func language(docs []model.Document) string {
	var latin, cyr, ukr int
	for _, d := range docs {
		for _, r := range d.Title + " " + d.Body {
			switch {
			case unicode.In(r, unicode.Cyrillic):
				cyr++
				if strings.ContainsRune("іїєґІЇЄҐ", r) {
					ukr++
				}
			case unicode.In(r, unicode.Latin):
				latin++
			}
		}
	}
	switch {
	case latin > cyr:
		return "English (Latin script)"
	case cyr == 0:
		return "unknown"
	case ukr*100 > cyr:
		return "Ukrainian"
	}
	return "Russian"
}

// runViews re-reads the views and photo flags of Telegram posts of the
// last days (scheduler.ViewsDays by default, as the daemon does before the
// weekly export; more to fill a past week).
func runViews(env *app.Env, days int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, env.Config.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	posts := &collect.Posts{Sources: collect.PostSources(env.Config, env.Root), Store: st}
	n, err := posts.RefreshViews(ctx, time.Now().AddDate(0, 0, -days))
	fmt.Printf("telegram views: %d posts updated\n", n)
	return err
}
