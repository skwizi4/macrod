// Package bot is the daemon's one Telegram poller (phase 14c, D29): a
// single getUpdates long poll serves the capex guidance buttons (14b), the
// Ifo values typed in reply to a reminder and the quarterly files of the
// issuers without a downloadable file (Allianz). Two pollers of one bot
// get 409, so every handler lives here.
package bot

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/notify"
	"macro-collector/internal/store"
)

// OffsetKey is the fetch_state row holding the next getUpdates offset
// (the "notify" family is not a data source: no alerts, no status row).
const OffsetKey = "notify:updates"

// Handler takes the messages it knows. Handle returns ok=false for a
// message that is not its own; otherwise reply is sent back to it.
type Handler interface {
	Handle(ctx context.Context, m *notify.Message) (reply string, ok bool)
	Help() string // one line of the formats it takes
}

// Bot long-polls Telegram until ctx is done. Only TG_CHAT_ID is served;
// the offset is kept in fetch_state so a restart neither loses nor
// repeats an update.
type Bot struct {
	TG       *notify.Telegram
	Store    *store.Store
	Log      *slog.Logger
	Callback func(ctx context.Context, cb *notify.Callback) // ✅/❌ presses; nil: answered as unknown
	Handlers []Handler
	Timeout  time.Duration                                   // long poll; 0: 30 s
	Sleep    func(ctx context.Context, d time.Duration) bool // nil: real wait; false when ctx is done
	Now      func() time.Time                                // nil: time.Now
}

// Backoff after a failed getUpdates: 409 (another poller of this bot,
// e.g. a second daemon) waits a minute, other errors 5 s; both double up
// to 10 minutes.
const (
	conflictWait = time.Minute
	errorWait    = 5 * time.Second
	maxWait      = 10 * time.Minute
)

func (b *Bot) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Run polls until ctx is done.
func (b *Bot) Run(ctx context.Context) {
	timeout := b.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	var offset int64
	if st, ok, err := b.Store.FetchStateOf(ctx, OffsetKey); err == nil && ok {
		offset, _ = strconv.ParseInt(st.Cursor, 10, 64)
	}
	b.Log.Info("telegram bot started", "offset", offset, "handlers", len(b.Handlers), "buttons", b.Callback != nil)
	var wait time.Duration
	for ctx.Err() == nil {
		ups, err := b.TG.Updates(ctx, offset, timeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var api *notify.APIError
			first := errorWait
			if errors.As(err, &api) && api.Code == 409 {
				first = conflictWait
				b.Log.Warn("telegram getUpdates conflict: another poller or a webhook uses this bot", "err", err)
			} else {
				b.Log.Warn("telegram getUpdates failed", "err", err)
			}
			wait = min(max(wait*2, first), maxWait)
			if !b.sleep(ctx, wait) {
				break
			}
			continue
		}
		wait = 0
		if len(ups) == 0 {
			continue
		}
		for _, u := range ups {
			switch {
			case u.Callback != nil:
				b.press(ctx, u.Callback)
			case u.Message != nil:
				b.message(ctx, u.Message)
			}
			offset = max(offset, u.ID+1)
		}
		if err := b.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.SetFetchOK(tx, OffsetKey, strconv.FormatInt(offset, 10), b.now())
		}); err != nil {
			b.Log.Error("store telegram offset", "err", err)
		}
	}
	b.Log.Info("telegram bot stopped")
}

func (b *Bot) sleep(ctx context.Context, d time.Duration) bool {
	if b.Sleep != nil {
		return b.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (b *Bot) press(ctx context.Context, cb *notify.Callback) {
	if b.Callback != nil {
		b.Callback(ctx, cb)
		return
	}
	if err := b.TG.AnswerCallback(ctx, cb.ID, "Неизвестная кнопка"); err != nil {
		b.Log.Warn("telegram answerCallbackQuery", "err", err)
	}
}

// message passes m to the first handler that takes it; a message nobody
// takes is answered with the formats. Messages of other chats and of bots
// are ignored (logged): the bot never writes to another chat.
func (b *Bot) message(ctx context.Context, m *notify.Message) {
	if m.ChatID != b.TG.ChatID {
		b.Log.Warn("telegram message from another chat ignored", "chat", m.ChatID, "from", m.FromID)
		return
	}
	if m.FromBot {
		return
	}
	reply := ""
	for _, h := range b.Handlers {
		if r, ok := h.Handle(ctx, m); ok {
			reply = r
			break
		}
	}
	if reply == "" {
		reply = b.help()
	}
	if err := b.TG.Reply(ctx, m.ID, reply); err != nil {
		b.Log.Warn("telegram reply not sent", "err", err)
	}
}

func (b *Bot) help() string {
	lines := []string{"Не понял сообщение. Что я принимаю:"}
	for _, h := range b.Handlers {
		lines = append(lines, "• "+h.Help())
	}
	if len(b.Handlers) == 0 {
		lines = append(lines, "• только кнопки ✅/❌ под кандидатами guidance")
	}
	return strings.Join(lines, "\n")
}
