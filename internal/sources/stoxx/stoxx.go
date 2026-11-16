// Package stoxx reads the historical index files STOXX publishes as text
// (spec 6.1; LOOKUP 2026-11-10): "Date;Symbol;Indexvalue", one row per
// trading day, the whole history in one file. Used for VDAX-NEW
// (h_v1x.txt) and VSTOXX (h_v2tx.txt), which Yahoo does not carry.
package stoxx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state prefix).
const Source = "stoxx"

const (
	header     = "Date;Symbol;Indexvalue"
	dateLayout = "02.01.2006"
)

// Client reads the files of one directory.
type Client struct {
	http     *httpx.Client
	endpoint string // directory of the files
	log      *slog.Logger
}

// New returns a client of the files under endpoint.
func New(endpoint string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, endpoint: strings.TrimRight(endpoint, "/"), log: log}
}

// Name implements sources.NumericSource.
func (c *Client) Name() string { return Source }

// URL is the address of a file.
func (c *Client) URL(file string) string { return c.endpoint + "/" + file }

// FetchSeries implements sources.NumericSource: the rows of the file key
// dated since or later.
func (c *Client) FetchSeries(ctx context.Context, key string, since time.Time) ([]model.Observation, error) {
	body, err := c.http.Get(ctx, c.URL(key))
	if err != nil {
		return nil, fmt.Errorf("stoxx fetch %s: %w", key, err)
	}
	obs, _, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("stoxx fetch %s: %w", key, err)
	}
	for len(obs) > 0 && obs[0].Date.Before(since) {
		obs = obs[1:]
	}
	return obs, nil
}

// Parse reads a file: the header, then rows of one symbol in date order
// with a positive value. Anything else (an HTML error page, a second
// symbol, a gap in the columns) is an error: nothing of the file is used.
func Parse(body []byte) (obs []model.Observation, symbol string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != header {
		return nil, "", fmt.Errorf("first line is not %q", header)
	}
	for n := 2; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Split(line, ";")
		if len(f) != 3 {
			return nil, "", fmt.Errorf("line %d: %d fields, want 3", n, len(f))
		}
		d, err := time.Parse(dateLayout, f[0])
		if err != nil {
			return nil, "", fmt.Errorf("line %d: date %q", n, f[0])
		}
		if symbol == "" {
			symbol = f[1]
		}
		if f[1] != symbol || symbol == "" {
			return nil, "", fmt.Errorf("line %d: symbol %q in a file of %q", n, f[1], symbol)
		}
		v, err := strconv.ParseFloat(f[2], 64)
		if err != nil || math.IsNaN(v) || v <= 0 {
			return nil, "", fmt.Errorf("line %d: value %q", n, f[2])
		}
		if len(obs) > 0 && !d.After(obs[len(obs)-1].Date) {
			return nil, "", fmt.Errorf("line %d: date %s is not after the previous one", n, f[0])
		}
		obs = append(obs, model.Observation{Date: d, Value: v})
	}
	if err := sc.Err(); err != nil {
		return nil, "", err
	}
	if len(obs) == 0 {
		return nil, "", errors.New("no rows")
	}
	return obs, symbol, nil
}

// Fixtures are the files recorded in testdata/stoxx, cut to the rows of
// FixtureFrom…FixtureTo; `macroctl record-fixtures --source stoxx`
// refreshes them.
var Fixtures = []string{"h_v1x.txt", "h_v2tx.txt"}

// The recorded window.
const (
	FixtureFrom = "2026-01-01"
	FixtureTo   = "2026-11-09"
)

// RecordFixtures fetches every fixture into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("stoxx record mkdir %s: %w", dir, err)
	}
	from, _ := time.Parse(time.DateOnly, FixtureFrom)
	to, _ := time.Parse(time.DateOnly, FixtureTo)
	for _, file := range Fixtures {
		body, err := c.http.Get(ctx, c.URL(file))
		if err != nil {
			return fmt.Errorf("stoxx record %s: %w", file, err)
		}
		obs, symbol, err := Parse(body)
		if err != nil {
			return fmt.Errorf("stoxx record %s: %w", file, err)
		}
		var b strings.Builder
		b.WriteString(header + "\n")
		for _, o := range obs {
			if o.Date.Before(from) || o.Date.After(to) {
				continue
			}
			fmt.Fprintf(&b, "%s;%s;%s\n", o.Date.Format(dateLayout), symbol, strconv.FormatFloat(o.Value, 'f', -1, 64))
		}
		path := filepath.Join(dir, file)
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return fmt.Errorf("stoxx record %s: %w", file, err)
		}
		c.log.Info("fixture recorded", "file", path, "bytes", b.Len())
	}
	return nil
}
