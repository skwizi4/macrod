package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

// Inline keyboards and callback queries (phase 14b; Bot API 10.4, LOOKUP
// 2026-11-13: sendMessage reply_markup InlineKeyboardMarkup, callback_data
// 1–64 bytes, getUpdates offset/timeout/allowed_updates, answerCallbackQuery
// must be called after every press, editMessageText chat_id + message_id).

// Button is an inline keyboard button with callback data (1–64 bytes).
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Callback is a pressed button.
type Callback struct {
	ID        string
	ChatID    string // chat of the message with the button
	MessageID int64
	FromID    int64
	Data      string
}

// Update is a getUpdates entry: a pressed button or a message (both nil
// for other kinds).
type Update struct {
	ID       int64
	Callback *Callback
	Message  *Message
}

func keyboard(rows [][]Button) map[string]any {
	if rows == nil {
		rows = [][]Button{} // an empty keyboard removes the buttons
	}
	return map[string]any{"inline_keyboard": rows}
}

func cut(text string) string {
	if utf8.RuneCountInString(text) > MaxText {
		text = string([]rune(text)[:MaxText-1]) + "…"
	}
	return text
}

// SendButtons posts text with an inline keyboard and returns the message id.
func (t *Telegram) SendButtons(ctx context.Context, text string, rows [][]Button) (int64, error) {
	raw, err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id":              t.ChatID,
		"text":                 cut(text),
		"link_preview_options": map[string]bool{"is_disabled": true},
		"reply_markup":         keyboard(rows),
	})
	if err != nil {
		return 0, err
	}
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, fmt.Errorf("telegram sendMessage: %w", err)
	}
	return m.MessageID, nil
}

// EditText replaces the text of a message of the chat and its keyboard
// (nil rows: no buttons).
func (t *Telegram) EditText(ctx context.Context, messageID int64, text string, rows [][]Button) error {
	_, err := t.call(ctx, "editMessageText", map[string]any{
		"chat_id":              t.ChatID,
		"message_id":           messageID,
		"text":                 cut(text),
		"link_preview_options": map[string]bool{"is_disabled": true},
		"reply_markup":         keyboard(rows),
	})
	return err
}

// AnswerCallback stops the client's progress bar, showing text (≤ 200).
func (t *Telegram) AnswerCallback(ctx context.Context, id, text string) error {
	if utf8.RuneCountInString(text) > 200 {
		text = string([]rune(text)[:199]) + "…"
	}
	_, err := t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text})
	return err
}

// Updates long-polls getUpdates for callback queries from offset, waiting
// up to timeout. The request ends with ctx; its client allows timeout +
// 10 s, so the long poll is not cut by the 30 s timeout of other calls.
func (t *Telegram) Updates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	long := *t
	base := t.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	hc := *base
	hc.Timeout = timeout + 10*time.Second
	long.HTTP = &hc
	raw, err := long.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout / time.Second),
		"allowed_updates": []string{"callback_query", "message"},
	})
	if err != nil {
		return nil, err
	}
	var ups []struct {
		UpdateID      int64 `json:"update_id"`
		CallbackQuery *struct {
			ID   string `json:"id"`
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
			Message *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
			Data string `json:"data"`
		} `json:"callback_query"`
		Message *rawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &ups); err != nil {
		return nil, fmt.Errorf("telegram getUpdates: %w", err)
	}
	out := make([]Update, 0, len(ups))
	for _, u := range ups {
		up := Update{ID: u.UpdateID}
		if q := u.CallbackQuery; q != nil {
			cb := &Callback{ID: q.ID, FromID: q.From.ID, Data: q.Data}
			if q.Message != nil {
				cb.ChatID, cb.MessageID = strconv.FormatInt(q.Message.Chat.ID, 10), q.Message.MessageID
			}
			up.Callback = cb
		}
		if m := u.Message; m != nil {
			up.Message = m.message()
		}
		out = append(out, up)
	}
	return out, nil
}
