package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

const token = "123:SECRET-token"

func TestSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/bot"+token+"/sendMessage" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"ok":false,"error_code":404,"description":"Not Found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	tg := New(srv.URL, token, "42")
	if err := tg.Send(context.Background(), "Недельный экспорт 2026-W45 готов"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "42" || got["text"] != "Недельный экспорт 2026-W45 готов" {
		t.Errorf("body %v", got)
	}
	if lp, _ := got["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Errorf("link preview %v", got["link_preview_options"])
	}

	long := strings.Repeat("я", MaxText+10)
	if err := tg.Send(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(got["text"].(string)); n != MaxText {
		t.Errorf("long text cut to %d runes", n)
	}
}

func TestSendErrorsHideToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()
	err := New(srv.URL, token, "1").Send(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
	// unreachable host: *url.Error would print the URL with the token
	err = New("http://127.0.0.1:1", token, "1").Send(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
}

func TestSendRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	if err := New(srv.URL, token, "1").Send(context.Background(), "x"); err != nil || calls.Load() != 2 {
		t.Errorf("err %v, calls %d", err, calls.Load())
	}
}

func TestMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/bot"+token+"/getMe" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"macro_bot"}}`))
	}))
	defer srv.Close()
	if u, err := New(srv.URL, token, "1").Me(context.Background()); err != nil || u != "macro_bot" {
		t.Errorf("%q, %v", u, err)
	}
	if _, err := New(srv.URL, "bad", "1").Me(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: %v", err)
	}
}
