package bot

import (
	"context"
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

	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

// fakeTG answers getUpdates with the queued batches, then blocks until the
// request ends; it records the other calls and serves files.
type fakeTG struct {
	mu      sync.Mutex
	batches []string
	status  []int // status of each getUpdates; 0: 200
	calls   []string
	offsets []float64
	files   map[string][]byte // file_id → content
	nextID  int64
}

func (f *fakeTG) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if i := strings.Index(r.URL.Path, "/file/bot"); i >= 0 {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		data := f.files[id]
		f.mu.Unlock()
		_, _ = w.Write(data)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	switch method {
	case "getUpdates":
	case "getFile":
		id := fmt.Sprint(body["file_id"])
		n := len(f.files[id])
		f.mu.Unlock()
		fmt.Fprintf(w, `{"ok":true,"result":{"file_id":%q,"file_path":"documents/%s","file_size":%d}}`, id, id, n)
		return
	default:
		raw, _ := json.Marshal(body)
		f.calls = append(f.calls, method+" "+string(raw))
		f.nextID++
		id := 500 + f.nextID
		f.mu.Unlock()
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, id)
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

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "macro.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func pressJSON(update int, chat int64, data string) string {
	return fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"cq%d","from":{"id":1},"message":{"message_id":11,"chat":{"id":%d}},"data":%q}}`, update, update, chat, data)
}

func textJSON(update int, chat int64, msg int64, text string, isBot bool) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%d},"from":{"id":1,"is_bot":%v},"text":%q}}`, update, msg, chat, isBot, text)
}

// echo takes messages starting with "echo".
type echo struct{ got []string }

func (e *echo) Handle(_ context.Context, m *notify.Message) (string, bool) {
	if !strings.HasPrefix(m.Text, "echo") {
		return "", false
	}
	e.got = append(e.got, m.Text)
	return "got " + m.Text, true
}

func (e *echo) Help() string { return "echo <текст>" }

// runUntil runs the bot until it has polled n times.
func runUntil(t *testing.T, b *Bot, fake *fakeTG, n int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, offs := fake.snapshot(); len(offs) >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

// One poll serves presses and messages; other chats and bots are ignored;
// the offset is stored and a restart goes on from it.
func TestRunRoutesUpdatesAndKeepsOffset(t *testing.T) {
	st := testStore(t)
	fake := &fakeTG{batches: []string{"[" + strings.Join([]string{
		pressJSON(100, 42, "g:a:1"),
		textJSON(101, 42, 21, "echo hi", false),
		textJSON(102, 666, 22, "echo stranger", false), // another chat
		textJSON(103, 42, 23, "echo from a bot", true),
		textJSON(104, 42, 24, "что-то", false),
	}, ",") + "]"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var presses []string
	h := &echo{}
	b := &Bot{TG: notify.New(srv.URL, "1:t", "42"), Store: st, Log: discard(), Timeout: time.Second, Handlers: []Handler{h},
		Callback: func(_ context.Context, cb *notify.Callback) { presses = append(presses, cb.Data) }}
	runUntil(t, b, fake, 2)

	calls, offs := fake.snapshot()
	if len(offs) < 2 || offs[0] != 0 || offs[1] != 105 {
		t.Fatalf("offsets %v, want 0 then 105", offs)
	}
	if fmt.Sprint(presses) != "[g:a:1]" || fmt.Sprint(h.got) != "[echo hi]" {
		t.Fatalf("presses %v, handled %v", presses, h.got)
	}
	joined := strings.Join(calls, "\n")
	if len(calls) != 2 || !strings.Contains(calls[0], `"text":"got echo hi"`) || !strings.Contains(calls[0], `"message_id":21`) ||
		!strings.Contains(calls[1], "Не понял сообщение") || !strings.Contains(calls[1], `echo \u003cтекст\u003e`) || strings.Contains(joined, "666") {
		t.Fatalf("calls:\n%s", joined)
	}
	if s, ok, _ := st.FetchStateOf(context.Background(), OffsetKey); !ok || s.Cursor != "105" {
		t.Fatalf("stored offset %+v", s)
	}

	fake2 := &fakeTG{}
	srv2 := httptest.NewServer(fake2)
	defer srv2.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	(&Bot{TG: notify.New(srv2.URL, "1:t", "42"), Store: st, Log: discard(), Timeout: time.Second}).Run(ctx2)
	if _, offs := fake2.snapshot(); len(offs) == 0 || offs[0] != 105 {
		t.Fatalf("restart offsets %v", offs)
	}
}

func TestRunConflictBackoff(t *testing.T) {
	fake := &fakeTG{status: []int{409, 409, 500, 409}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &Bot{TG: notify.New(srv.URL, "1:t", "42"), Store: testStore(t), Log: discard(), Timeout: time.Second,
		Sleep: func(_ context.Context, d time.Duration) bool {
			waits = append(waits, d)
			if len(waits) == 4 {
				cancel()
				return false
			}
			return true
		}}
	b.Run(ctx)
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
}

// A press without a guidance service is answered, not dropped.
func TestPressWithoutGuidance(t *testing.T) {
	fake := &fakeTG{batches: []string{"[" + pressJSON(7, 42, "g:a:1") + "]"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	runUntil(t, &Bot{TG: notify.New(srv.URL, "1:t", "42"), Store: testStore(t), Log: discard(), Timeout: time.Second}, fake, 2)
	if calls, _ := fake.snapshot(); len(calls) != 1 || !strings.Contains(calls[0], "Неизвестная кнопка") {
		t.Fatalf("calls %v", calls)
	}
}
