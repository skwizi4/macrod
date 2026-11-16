package guidance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

func testService(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ny, _ := time.LoadLocation("America/New_York")
	cfg := &config.Config{Location: time.UTC}
	cfg.Calendar.ReleaseLocation = ny
	cfg.EDGAR.GuidanceTickers = []string{"GOOGL"}
	return &Service{Store: st, Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC) }}
}

func addCandidate(t *testing.T, s *Service, low float64) store.GuidanceCandidate {
	t.Helper()
	var out []store.GuidanceCandidate
	c := store.GuidanceCandidate{Ticker: "GOOGL", Year: 2026, Low: low, High: low + 10, Quote: "q", URL: "u",
		DocSource: "transcript", DocRef: fmt.Sprint("acc", low), DocTitle: "GOOGL call", Given: d("2026-10-28")}
	if err := s.Store.Write(context.Background(), func(tx *sql.Tx) (err error) {
		out, err = Insert(tx, []store.GuidanceCandidate{c}, s.now())
		return err
	}); err != nil || len(out) != 1 {
		t.Fatalf("insert %v, %v", out, err)
	}
	return out[0]
}

// fakeTG answers getUpdates with the queued batches, then blocks until the
// request ends; it records the other calls.
type fakeTG struct {
	mu      sync.Mutex
	batches []string
	status  []int // status of each getUpdates; 0: 200
	calls   []string
	offsets []float64
}

func (f *fakeTG) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	if method != "getUpdates" {
		raw, _ := json.Marshal(body)
		f.calls = append(f.calls, method+" "+string(raw))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":500}}`))
		return
	}
	f.offsets = append(f.offsets, body["offset"].(float64))
	if len(f.status) > 0 {
		code := f.status[0]
		f.status = f.status[1:]
		f.mu.Unlock()
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"Conflict: terminated by other getUpdates request"}`, code)
		return
	}
	if len(f.batches) > 0 {
		b := f.batches[0]
		f.batches = f.batches[1:]
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":` + b + `}`))
		return
	}
	f.mu.Unlock()
	<-r.Context().Done()
}

func (f *fakeTG) snapshot() ([]string, []float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]float64(nil), f.offsets...)
}

func press(id string, chat int64, msg int64, data string) *notify.Callback {
	return &notify.Callback{ID: id, ChatID: fmt.Sprint(chat), MessageID: msg, FromID: 1, Data: data}
}

// The polling loop is internal/bot; here the presses it delivers.
func TestHandleCallbackOnlyFromOwnChat(t *testing.T) {
	s := testService(t)
	c1 := addCandidate(t, s, 195)
	c2 := addCandidate(t, s, 210)
	fake := &fakeTG{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s.TG = notify.New(srv.URL, "1:t", "42")
	ctx := context.Background()
	for _, cb := range []*notify.Callback{
		press("cq100", 42, 11, fmt.Sprintf("g:a:%d", c1.ID)),
		press("cq101", 666, 12, fmt.Sprintf("g:a:%d", c2.ID)), // another chat
		press("cq102", 42, 11, fmt.Sprintf("g:r:%d", c1.ID)),  // second press: already decided
		press("cq103", 42, 13, "junk"),
	} {
		s.HandleCallback(ctx, cb)
	}
	calls, _ := fake.snapshot()
	joined := strings.Join(calls, "\n")
	for _, want := range []string{`"text":"✅ Подтверждён: GOOGL 2026, 195–205 млрд $"`, `"text":"Нет доступа"`, `"text":"Уже решён: подтверждён"`, `"text":"Неизвестная кнопка"`, `"message_id":11`} {
		if !strings.Contains(joined, want) {
			t.Errorf("calls lack %s:\n%s", want, joined)
		}
	}
	got1, _, _ := s.Store.GuidanceCandidate(ctx, c1.ID)
	got2, _, _ := s.Store.GuidanceCandidate(ctx, c2.ID)
	if got1.Status != store.GuidanceApproved || got1.DecidedVia != "telegram" || got2.Status != store.GuidancePending {
		t.Fatalf("statuses %s/%s, %s", got1.Status, got1.DecidedVia, got2.Status)
	}
	conf, _ := s.Store.ConfirmedGuidance(ctx)
	if len(conf) != 1 || conf[0].CandidateID != c1.ID {
		t.Fatalf("confirmed %+v", conf)
	}
}

func TestDecideEditsMessage(t *testing.T) {
	s := testService(t)
	c := addCandidate(t, s, 195)
	fake := &fakeTG{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s.TG = notify.New(srv.URL, "1:t", "42")
	s.Announce(context.Background(), []store.GuidanceCandidate{c})
	stored, _, _ := s.Store.GuidanceCandidate(context.Background(), c.ID)
	if stored.TGMessageID != 500 {
		t.Fatalf("message id %d", stored.TGMessageID)
	}
	if _, err := s.Decide(context.Background(), c.ID, false, "cli"); err != nil {
		t.Fatal(err)
	}
	calls, _ := fake.snapshot()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "sendMessage") || !strings.Contains(calls[0], `"callback_data":"g:a:1"`) ||
		!strings.HasPrefix(calls[1], "editMessageText") || !strings.Contains(calls[1], "❌ Отклонён") || !strings.Contains(calls[1], "через macroctl") {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
	if conf, _ := s.Store.ConfirmedGuidance(context.Background()); len(conf) != 0 {
		t.Fatalf("reject confirmed %+v", conf)
	}
}
