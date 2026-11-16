package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/export"
	"macro-collector/internal/gitsync"
)

// receiver records the requests and answers with codes in turn (the last
// one repeats).
type receiver struct {
	mu     sync.Mutex
	codes  []int
	bodies [][]byte
	heads  []http.Header
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := io.ReadAll(req.Body)
	r.bodies = append(r.bodies, b)
	r.heads = append(r.heads, req.Header.Clone())
	code := r.codes[min(len(r.bodies)-1, len(r.codes)-1)]
	w.WriteHeader(code)
}

func newHook(t *testing.T, codes ...int) (*Webhook, *receiver) {
	t.Helper()
	r := &receiver{codes: codes}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &Webhook{URL: srv.URL + "/hook?token=s3cret", Secret: "key", Timeout: 5 * time.Second, Retries: 3,
		Backoff: time.Millisecond, Dir: "/d/exchange", Log: discard,
		Title: func(r string) string { return map[string]string{"usa": "США"}[r] },
		Now:   func() time.Time { return time.Date(2026, 11, 15, 15, 0, 30, 0, time.UTC) }}, r
}

var hookExport = export.Exported{Kind: export.KindWeekly, Region: "usa", ISOWeek: "2026-W46", Files: []string{
	filepath.FromSlash("/d/exchange/usa/master/markets_usa.md"), filepath.FromSlash("/d/exchange/archive/2026-W46/usa/master/markets_usa.md"),
	filepath.FromSlash("/d/exchange/json/usa/master/markets_usa.json"), filepath.FromSlash("/d/exchange/usa/news/current_week.md"),
	filepath.FromSlash("/d/exchange/archive/2026-W46/news_filtered.md"), filepath.FromSlash("/elsewhere/x.md"),
}}

func TestWebhookPayloadAndSignature(t *testing.T) {
	hook, r := newHook(t, http.StatusNoContent)
	n := &fakeNotify{}
	g := &fakeGit{res: gitsync.Result{Committed: true, Pushed: true, Commit: "abc1234"}}
	c := chain(g, n, hook)
	_ = c.Exported(context.Background(), hookExport)
	c.Wait()
	if len(r.bodies) != 1 {
		t.Fatalf("requests %d", len(r.bodies))
	}
	want := `{"event":"export","kind":"weekly","region":"usa","region_title":"США","iso_week":"2026-W46",` +
		`"files":["usa/master/markets_usa.md","usa/news/current_week.md"],` +
		`"git":{"commit":"abc1234","url":"https://github.com/example/macro_exchange/commit/abc1234"},"sent_at":"2026-11-15T15:00:30Z"}`
	if string(r.bodies[0]) != want {
		t.Errorf("body\n%s\nwant\n%s", r.bodies[0], want)
	}
	h := r.heads[0]
	if h.Get(SignatureHeader) != Sign("key", r.bodies[0]) || !strings.HasPrefix(h.Get(SignatureHeader), "sha256=") ||
		h.Get("Content-Type") != "application/json" || h.Get("X-Macro-Event") != "export" {
		t.Errorf("headers %v", h)
	}
	// Telegram still goes after the webhook, without a webhook line on success.
	if len(n.sent) != 1 || strings.Contains(n.sent[0], "webhook") || !strings.Contains(n.sent[0], "git: abc1234") {
		t.Errorf("sent %q", n.sent)
	}
}

func TestWebhookRetries(t *testing.T) {
	hook, r := newHook(t, http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusOK)
	d := &Delivery{Event: hookExport}
	if err := hook.Deliver(context.Background(), d); err != nil || len(r.bodies) != 3 || len(d.Lines) != 0 {
		t.Fatalf("err %v, requests %d, lines %q", err, len(r.bodies), d.Lines)
	}
	var p WebhookPayload
	if err := json.Unmarshal(r.bodies[2], &p); err != nil || p.Git != nil {
		t.Errorf("payload without git: %+v %v", p, err)
	}
}

func TestWebhookFailures(t *testing.T) {
	// 400: no retry; the message says so; Telegram still goes out.
	hook, r := newHook(t, http.StatusBadRequest)
	n := &fakeNotify{}
	c := chain(nil, n, hook)
	_ = c.Exported(context.Background(), hookExport)
	c.Wait()
	if len(r.bodies) != 1 || len(n.sent) != 1 || !strings.Contains(n.sent[0], "⚠ webhook не доставлен: webhook: HTTP 400") {
		t.Fatalf("requests %d, sent %q", len(r.bodies), n.sent)
	}
	// 5xx every time: 1 + Retries attempts.
	hook, r = newHook(t, http.StatusBadGateway)
	if err := hook.Deliver(context.Background(), &Delivery{Event: hookExport}); err == nil || len(r.bodies) != 4 {
		t.Fatalf("err %v, requests %d", err, len(r.bodies))
	}
	// A dead receiver: the error never shows the URL (it carries a token).
	hook, _ = newHook(t, http.StatusOK)
	hook.URL, hook.Retries = "http://127.0.0.1:1/hook?token=s3cret", 0
	d := &Delivery{Event: hookExport}
	err := hook.Deliver(context.Background(), d)
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(strings.Join(d.Lines, ""), "s3cret") {
		t.Fatalf("err %v, lines %q", err, d.Lines)
	}
}

// A panicking sink does not stop the next ones.
type panicSink struct{}

func (panicSink) Name() string                             { return "boom" }
func (panicSink) Deliver(context.Context, *Delivery) error { panic(errors.New("boom")) }

func TestChainSurvivesPanic(t *testing.T) {
	n := &fakeNotify{}
	c := chain(nil, n, panicSink{})
	_ = c.Exported(context.Background(), hookExport)
	c.Wait()
	if len(n.sent) != 1 {
		t.Fatalf("sent %q", n.sent)
	}
}

func TestCheckWebhookURL(t *testing.T) {
	if msg, err := CheckWebhookURL("https://hooks.example.com/x?token=s3cret", true); err != nil || strings.Contains(msg, "s3cret") || !strings.HasPrefix(msg, "https://hooks.example.com ") {
		t.Errorf("%q %v", msg, err)
	}
	if msg, _ := CheckWebhookURL("http://h/x", false); !strings.Contains(msg, "unsigned") {
		t.Errorf("%q", msg)
	}
	for _, bad := range []string{"", "hooks.example.com/x", "ftp://h/x", "http://"} {
		if _, err := CheckWebhookURL(bad, true); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
