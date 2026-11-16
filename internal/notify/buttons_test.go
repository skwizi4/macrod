package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestButtonsEditAnswerUpdates(t *testing.T) {
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+token+"/")
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies[method] = b
		switch method {
		case "sendMessage":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"chat":{"id":42}}}`))
		case "getUpdates":
			_, _ = w.Write([]byte(`{"ok":true,"result":[
				{"update_id":10,"callback_query":{"id":"cq1","from":{"id":5},"message":{"message_id":77,"chat":{"id":42}},"data":"g:a:1"}},
				{"update_id":11,"message":{"message_id":3,"chat":{"id":42},"from":{"id":5},"caption":"ALV","document":{"file_id":"F1","file_name":"a.xlsx","mime_type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet","file_size":10},"reply_to_message":{"message_id":2}}}]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer srv.Close()
	tg := New(srv.URL, token, "42")
	ctx := context.Background()

	id, err := tg.SendButtons(ctx, "text", [][]Button{{{Text: "✅", Data: "g:a:1"}, {Text: "❌", Data: "g:r:1"}}})
	if err != nil || id != 77 {
		t.Fatalf("SendButtons = %d, %v", id, err)
	}
	kb := bodies["sendMessage"]["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	if row := kb[0].([]any); len(row) != 2 || row[1].(map[string]any)["callback_data"] != "g:r:1" {
		t.Fatalf("keyboard %v", kb)
	}

	if err := tg.EditText(ctx, 77, "done", nil); err != nil {
		t.Fatal(err)
	}
	e := bodies["editMessageText"]
	if e["chat_id"] != "42" || e["message_id"] != float64(77) || e["text"] != "done" {
		t.Fatalf("edit %v", e)
	}
	if kb := e["reply_markup"].(map[string]any)["inline_keyboard"].([]any); len(kb) != 0 {
		t.Fatalf("edit keeps buttons %v", kb)
	}

	if err := tg.AnswerCallback(ctx, "cq1", "ok"); err != nil || bodies["answerCallbackQuery"]["callback_query_id"] != "cq1" {
		t.Fatalf("answer %v, %v", bodies["answerCallbackQuery"], err)
	}

	ups, err := tg.Updates(ctx, 10, 30*time.Second)
	if err != nil || len(ups) != 2 {
		t.Fatalf("updates = %+v, %v", ups, err)
	}
	g := bodies["getUpdates"]
	if g["offset"] != float64(10) || g["timeout"] != float64(30) || fmt.Sprint(g["allowed_updates"]) != "[callback_query message]" {
		t.Fatalf("getUpdates body %v", g)
	}
	cb := ups[0].Callback
	if ups[0].ID != 10 || cb == nil || cb.ID != "cq1" || cb.ChatID != "42" || cb.MessageID != 77 || cb.FromID != 5 || cb.Data != "g:a:1" {
		t.Fatalf("callback %+v", cb)
	}
	m := ups[1].Message
	if ups[1].ID != 11 || ups[1].Callback != nil || m == nil || m.ID != 3 || m.ChatID != "42" || m.FromID != 5 || m.Text != "ALV" ||
		m.ReplyTo != 2 || m.Document == nil || m.Document.FileID != "F1" || m.Document.Name != "a.xlsx" || m.Document.Size != 10 {
		t.Fatalf("second update %+v %+v", ups[1], m)
	}
}

// The long poll outlives the 30 s client timeout of other calls and ends
// at once with ctx.
func TestUpdatesLongPollTimeoutAndCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()
	defer close(release)
	tg := New(srv.URL, token, "42")
	tg.HTTP.Timeout = 50 * time.Millisecond // shorter than the long poll
	go func() { time.Sleep(150 * time.Millisecond); release <- struct{}{} }()
	if ups, err := tg.Updates(context.Background(), 0, time.Second); err != nil || len(ups) != 0 {
		t.Fatalf("long poll = %v, %v; want it to outlive the client timeout", ups, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := tg.Updates(ctx, 0, 30*time.Second); err == nil {
		t.Fatal("cancelled poll returned no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %s", d)
	}
}

func TestForceReplyReplyDownload(t *testing.T) {
	bodies := map[string]map[string]any{}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/file/bot"+token+"/") {
			_, _ = w.Write([]byte("XLSX"))
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/bot"+token+"/")
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies[method] = b
		switch method {
		case "getFile":
			if b["file_id"] == "big" {
				_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"big","file_path":"documents/b.xlsx","file_size":30000000}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"F1","file_path":"documents/file_7.xlsx","file_size":4}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":90}}`))
		}
	}))
	defer srv.Close()
	tg := New(srv.URL, token, "42")
	ctx := context.Background()

	id, err := tg.SendForceReply(ctx, "Ifo?", "88.4 91.2")
	if err != nil || id != 90 {
		t.Fatalf("SendForceReply = %d, %v", id, err)
	}
	mk := bodies["sendMessage"]["reply_markup"].(map[string]any)
	if mk["force_reply"] != true || mk["input_field_placeholder"] != "88.4 91.2" {
		t.Fatalf("markup %v", mk)
	}
	if err := tg.Reply(ctx, 7, "ok"); err != nil {
		t.Fatal(err)
	}
	if rp := bodies["sendMessage"]["reply_parameters"].(map[string]any); rp["message_id"] != float64(7) {
		t.Fatalf("reply_parameters %v", rp)
	}
	data, err := tg.Download(ctx, "F1")
	if err != nil || string(data) != "XLSX" || paths[len(paths)-1] != "/file/bot"+token+"/documents/file_7.xlsx" {
		t.Fatalf("Download = %q, %v, %v", data, err, paths)
	}
	if _, err := tg.Download(ctx, "big"); err == nil || !strings.Contains(err.Error(), "20 MB") {
		t.Fatalf("big file: %v", err)
	}
	if strings.Contains(fmt.Sprint(err), token) {
		t.Fatal("token in error")
	}
}
