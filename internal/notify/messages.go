package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Messages and files (phase 14c; Bot API 10.4, LOOKUP 2026-11-13:
// getUpdates allowed_updates "message"; Message message_id, chat.id,
// from.id/is_bot, text, caption, document{file_id, file_name, mime_type,
// file_size}, reply_to_message; getFile → File.file_path, download at
// <api>/file/bot<token>/<file_path>, link valid ≥ 1 h, bots download up to
// 20 MB; getFile may drop the file name, so it is taken from Document;
// sendMessage reply_parameters{message_id}, ForceReply{force_reply,
// input_field_placeholder 1–64}).

// MaxDownload is the Bot API limit of a downloaded file.
const MaxDownload = 20 << 20

// Message is an incoming message.
type Message struct {
	ID       int64
	ChatID   string
	FromID   int64
	FromBot  bool
	Text     string // text, or the caption of a document
	Document *Document
	ReplyTo  int64 // message_id of the replied message; 0: not a reply
}

// Document is a file sent as a document.
type Document struct {
	FileID string
	Name   string
	MIME   string
	Size   int64
}

type rawMessage struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	Text     string `json:"text"`
	Caption  string `json:"caption"`
	Document *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"document"`
	ReplyTo *struct {
		MessageID int64 `json:"message_id"`
	} `json:"reply_to_message"`
}

func (m *rawMessage) message() *Message {
	out := &Message{ID: m.MessageID, ChatID: strconv.FormatInt(m.Chat.ID, 10), Text: m.Text}
	if m.From != nil {
		out.FromID, out.FromBot = m.From.ID, m.From.IsBot
	}
	if out.Text == "" {
		out.Text = m.Caption
	}
	if d := m.Document; d != nil {
		out.Document = &Document{FileID: d.FileID, Name: d.FileName, MIME: d.MimeType, Size: d.FileSize}
	}
	if m.ReplyTo != nil {
		out.ReplyTo = m.ReplyTo.MessageID
	}
	return out
}

func messageID(raw json.RawMessage, method string) (int64, error) {
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, fmt.Errorf("telegram %s: %w", method, err)
	}
	return m.MessageID, nil
}

// SendForceReply posts text that opens the reply field in the client
// (placeholder: 1–64 characters, "" for none) and returns the message id.
func (t *Telegram) SendForceReply(ctx context.Context, text, placeholder string) (int64, error) {
	markup := map[string]any{"force_reply": true}
	if placeholder != "" {
		markup["input_field_placeholder"] = placeholder
	}
	raw, err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id":              t.ChatID,
		"text":                 cut(text),
		"link_preview_options": map[string]bool{"is_disabled": true},
		"reply_markup":         markup,
	})
	if err != nil {
		return 0, err
	}
	return messageID(raw, "sendMessage")
}

// Reply posts text as an answer to message replyTo of the chat (0: a
// plain message). A deleted original does not fail the answer.
func (t *Telegram) Reply(ctx context.Context, replyTo int64, text string) error {
	body := map[string]any{
		"chat_id":              t.ChatID,
		"text":                 cut(text),
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	if replyTo != 0 {
		body["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	_, err := t.call(ctx, "sendMessage", body)
	return err
}

// Download fetches a file the chat sent (getFile, then the file link),
// refusing files over MaxDownload.
func (t *Telegram) Download(ctx context.Context, fileID string) ([]byte, error) {
	raw, err := t.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return nil, err
	}
	var f struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("telegram getFile: %w", err)
	}
	if f.FilePath == "" {
		return nil, fmt.Errorf("telegram getFile: no file_path (file over %d MB?)", MaxDownload>>20)
	}
	if f.FileSize > MaxDownload {
		return nil, fmt.Errorf("telegram file: %d bytes, over the %d MB limit", f.FileSize, MaxDownload>>20)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.APIURL+"/file/bot"+t.Token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram file: bad api url")
	}
	hc := *t.client()
	hc.Timeout = 2 * time.Minute
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram file: request failed: %w", unwrapURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("telegram file read: %w", unwrapURL(err))
	}
	if len(data) > MaxDownload {
		return nil, fmt.Errorf("telegram file: over the %d MB limit", MaxDownload>>20)
	}
	return data, nil
}

func (t *Telegram) client() *http.Client {
	if t.HTTP == nil {
		return http.DefaultClient
	}
	return t.HTTP
}
