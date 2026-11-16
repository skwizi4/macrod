package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// SignatureHeader carries "sha256=<hex HMAC-SHA256 of the body>" when a
// secret is set.
const SignatureHeader = "X-Macro-Signature"

// Webhook posts each export as JSON (§21.6). URL and Secret come from env:
// the URL may carry a token.
type Webhook struct {
	URL     string
	Secret  string
	Timeout time.Duration // one attempt
	Retries int           // after the first attempt
	Backoff time.Duration // first wait between attempts, doubled each time; default 2s
	Dir     string        // the exchange directory: file paths are sent relative to it
	Title   func(region string) string
	Client  *http.Client
	Log     *slog.Logger
	Now     func() time.Time
}

// WebhookPayload is the body of the request.
type WebhookPayload struct {
	Event       string      `json:"event"` // "export"
	Kind        string      `json:"kind"`  // weekly | event_fomc | event_ecb
	Region      string      `json:"region"`
	RegionTitle string      `json:"region_title"`
	ISOWeek     string      `json:"iso_week"`
	Files       []string    `json:"files"` // current .md of the agents' folders, relative to the exchange directory
	Git         *WebhookGit `json:"git"`   // null: no git sink or nothing committed
	SentAt      time.Time   `json:"sent_at"`
}

// WebhookGit is the commit of the export.
type WebhookGit struct {
	Commit string `json:"commit"`
	URL    string `json:"url,omitempty"`
}

// Name implements Sink.
func (w *Webhook) Name() string { return "webhook" }

// Payload is the body sent for d.
func (w *Webhook) Payload(d *Delivery) WebhookPayload {
	e := d.Event
	p := WebhookPayload{Event: "export", Kind: e.Kind, Region: e.Region, RegionTitle: e.Region, ISOWeek: e.ISOWeek, Files: []string{}}
	if w.Title != nil {
		p.RegionTitle = w.Title(e.Region)
	}
	for _, f := range e.Files {
		rel, err := filepath.Rel(w.Dir, f)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		// Only the current files of the agents' folders: the archive and
		// JSON copies are the same export again.
		if strings.HasPrefix(rel, "archive/") || strings.HasPrefix(rel, "json/") || !strings.HasSuffix(rel, ".md") {
			continue
		}
		p.Files = append(p.Files, rel)
	}
	if g := d.Git; g != nil && g.Err == nil && g.Commit != "" {
		p.Git = &WebhookGit{Commit: g.Commit, URL: g.URL}
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	p.SentAt = now().UTC().Truncate(time.Second)
	return p
}

// Deliver implements Sink: POST with retries on network errors, 429 and
// 5xx; any other status fails at once.
func (w *Webhook) Deliver(ctx context.Context, d *Delivery) error {
	body, err := json.Marshal(w.Payload(d))
	if err != nil {
		return fmt.Errorf("webhook payload: %w", err)
	}
	wait := w.Backoff
	if wait == 0 {
		wait = 2 * time.Second
	}
	for attempt := 0; ; attempt++ {
		retry, err := w.post(ctx, body)
		if err == nil {
			w.log().Info("webhook delivered", "kind", d.Event.Kind, "region", d.Event.Region, "iso_week", d.Event.ISOWeek)
			return nil
		}
		if !retry || attempt >= w.Retries {
			d.Lines = append(d.Lines, "⚠ webhook не доставлен: "+err.Error())
			return err
		}
		select {
		case <-ctx.Done():
			d.Lines = append(d.Lines, "⚠ webhook не доставлен: "+ctx.Err().Error())
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// post makes one attempt; retry tells whether another may help. Errors
// never contain the URL: it may carry a token.
func (w *Webhook) post(ctx context.Context, body []byte) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("webhook: bad URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "macro-collector")
	req.Header.Set("X-Macro-Event", "export")
	if w.Secret != "" {
		req.Header.Set(SignatureHeader, Sign(w.Secret, body))
	}
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // drop the URL
		}
		return true, fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	default:
		return false, fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
}

func (w *Webhook) log() *slog.Logger {
	if w.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return w.Log
}

// Sign is the X-Macro-Signature value of body: "sha256=" and the hex
// HMAC-SHA256 under secret. A receiver recomputes it over the raw body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// CheckWebhookURL tells whether u is an absolute http(s) URL; the URL is
// not printed (it may carry a token), only its scheme and host.
func CheckWebhookURL(u string, signed bool) (string, error) {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return "", errors.New("WEBHOOK_URL: want an http(s) URL")
	}
	msg := p.Scheme + "://" + p.Host + " (not called: a test post would be a delivery)"
	if !signed {
		msg += ", unsigned: WEBHOOK_SECRET not set"
	}
	return msg, nil
}
