// Package zew reads the ZEW financial market survey workbook (spec 6.1;
// LOOKUP 2026-11-10): konjunktur.xls, an old binary Excel file (BIFF) with
// one row per month. It is the only package that imports
// github.com/extrame/xls (D25).
//
// The file is made by hand, so it is checked as a whole before any of it is
// used: the columns are found by their headers, every value must be a
// balance between −100 and +100, the months must follow each other without
// gaps. A file that fails gives an error and no observations.
package zew

import (
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
	"sync"
	"time"

	"github.com/extrame/xls"

	"macro-collector/internal/model"
	"macro-collector/internal/sources/httpx"
)

// Source is the source name (observations.source, fetch_state prefix).
const Source = "zew"

const (
	dateHeader = "Date"
	meanRow    = "Mean:" // the footer under the last month
	// memoFor: the series of one run share one download (robots.txt asks
	// for a 15 s crawl delay).
	memoFor = 10 * time.Minute
)

// Client reads the workbook.
type Client struct {
	http  *httpx.Client
	url   string
	sheet string
	log   *slog.Logger

	mu     sync.Mutex
	memo   map[string][]model.Observation
	memoAt time.Time
}

// New returns a client of the workbook at url; the data is on sheet.
func New(url, sheet string, h *httpx.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{http: h, url: url, sheet: sheet, log: log}
}

// Name implements sources.NumericSource.
func (c *Client) Name() string { return Source }

// FetchSeries implements sources.NumericSource: the months of the column
// whose header is key, dated since or later.
func (c *Client) FetchSeries(ctx context.Context, key string, since time.Time) ([]model.Observation, error) {
	cols, err := c.columns(ctx)
	if err != nil {
		return nil, fmt.Errorf("zew fetch: %w", err)
	}
	obs, ok := cols[key]
	if !ok {
		return nil, fmt.Errorf("zew fetch: no column %q on sheet %q (columns: %s)", key, c.sheet, strings.Join(headers(cols), "; "))
	}
	for len(obs) > 0 && obs[0].Date.Before(since) {
		obs = obs[1:]
	}
	return obs, nil
}

func (c *Client) columns(ctx context.Context) (map[string][]model.Observation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.memo != nil && time.Since(c.memoAt) < memoFor {
		return c.memo, nil
	}
	body, err := c.http.Get(ctx, c.url)
	if err != nil {
		return nil, err
	}
	cols, err := Parse(body, c.sheet)
	if err != nil {
		return nil, err
	}
	c.memo, c.memoAt = cols, time.Now()
	return cols, nil
}

func headers(cols map[string][]model.Observation) []string {
	var out []string
	for h := range cols {
		out = append(out, h)
	}
	return out
}

// Parse reads the sheet of the workbook into its columns by header. The
// observation of a month is dated its first day.
func Parse(body []byte, sheetName string) (cols map[string][]model.Observation, err error) {
	// The library panics on files it does not understand.
	defer func() {
		if r := recover(); r != nil {
			cols, err = nil, fmt.Errorf("read xls: %v", r)
		}
	}()
	wb, err := xls.OpenReader(bytes.NewReader(body), "utf-8")
	if err != nil {
		return nil, fmt.Errorf("read xls: %w", err)
	}
	var sheet *xls.WorkSheet
	for i := 0; i < wb.NumSheets(); i++ {
		if s := wb.GetSheet(i); s != nil && s.Name == sheetName {
			sheet = s
		}
	}
	if sheet == nil {
		return nil, fmt.Errorf("no sheet %q", sheetName)
	}
	var rows [][]string
	for r := 0; r <= int(sheet.MaxRow); r++ {
		row := sheet.Row(r)
		if row == nil {
			rows = append(rows, nil)
			continue
		}
		cells := make([]string, row.LastCol())
		for i := row.FirstCol(); i < row.LastCol(); i++ {
			cells[i] = strings.TrimSpace(row.Col(i))
		}
		rows = append(rows, cells)
	}
	return parseRows(rows)
}

// parseRows checks the table and splits it into columns.
func parseRows(rows [][]string) (map[string][]model.Observation, error) {
	if len(rows) == 0 || len(rows[0]) < 2 || rows[0][0] != dateHeader {
		return nil, fmt.Errorf("the first row does not start with %q", dateHeader)
	}
	type column struct {
		idx  int
		name string
	}
	var columns []column
	for i, h := range rows[0][1:] {
		if h != "" {
			columns = append(columns, column{i + 1, h})
		}
	}
	if len(columns) == 0 {
		return nil, errors.New("no value columns")
	}
	out := map[string][]model.Observation{}
	var prev time.Time
	for n, row := range rows[1:] {
		line := n + 2
		if len(row) == 0 || row[0] == "" {
			if blank(row) {
				continue
			}
			return nil, fmt.Errorf("row %d: values without a date", line)
		}
		if row[0] == meanRow {
			continue
		}
		month, err := parseMonth(row[0])
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", line, err)
		}
		if !prev.IsZero() && !month.Equal(prev.AddDate(0, 1, 0)) {
			return nil, fmt.Errorf("row %d: month %s does not follow %s", line, month.Format("2006-01"), prev.Format("2006-01"))
		}
		prev = month
		for _, c := range columns {
			if c.idx >= len(row) || row[c.idx] == "" {
				return nil, fmt.Errorf("row %d (%s): no value in column %q", line, month.Format("2006-01"), c.name)
			}
			v, err := strconv.ParseFloat(row[c.idx], 64)
			if err != nil || math.IsNaN(v) || v < -100 || v > 100 {
				return nil, fmt.Errorf("row %d (%s): value %q in column %q is not a balance between -100 and 100", line, month.Format("2006-01"), row[c.idx], c.name)
			}
			out[c.name] = append(out[c.name], model.Observation{Date: month, Value: v})
		}
	}
	if prev.IsZero() {
		return nil, errors.New("no months")
	}
	return out, nil
}

func blank(row []string) bool {
	for _, c := range row {
		if c != "" {
			return false
		}
	}
	return true
}

// parseMonth reads the date cell: "2026.10" as the library formats it, or
// the Excel day number behind it.
func parseMonth(s string) (time.Time, error) {
	if y, m, ok := strings.Cut(s, "."); ok && len(y) == 4 && len(m) == 2 {
		t, err := time.Parse("2006.01", s)
		if err == nil {
			return t, nil
		}
	}
	if days, err := strconv.ParseFloat(s, 64); err == nil && days > 20000 && days < 80000 {
		t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days))
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, fmt.Errorf("date %q: want YYYY.MM", s)
}

// FixtureFile is the workbook recorded in testdata/zew; `macroctl
// record-fixtures --source zew` refreshes it.
const FixtureFile = "konjunktur.xls"

// RecordFixtures fetches the workbook into dir.
func (c *Client) RecordFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("zew record mkdir %s: %w", dir, err)
	}
	body, err := c.http.Get(ctx, c.url)
	if err != nil {
		return fmt.Errorf("zew record: %w", err)
	}
	if _, err := Parse(body, c.sheet); err != nil {
		return fmt.Errorf("zew record: %w", err)
	}
	path := filepath.Join(dir, FixtureFile)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("zew record: %w", err)
	}
	c.log.Info("fixture recorded", "file", path, "bytes", len(body))
	return nil
}
