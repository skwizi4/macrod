package guidance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/model"
	"macro-collector/internal/notify"
	"macro-collector/internal/sources/edgar"
	"macro-collector/internal/sources/irtranscript"
	"macro-collector/internal/store"
)

// Source is the slog source and the fetch_state key prefix.
const Source = "guidance"

// Service finds, announces and decides guidance candidates.
type Service struct {
	Store  *store.Store
	Config *config.Config
	TG     *notify.Telegram // nil: Telegram is off
	Log    *slog.Logger
	Now    func() time.Time // nil: time.Now
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Enabled reports whether ticker is searched (edgar.guidance_tickers).
func (s *Service) Enabled(ticker string) bool {
	return slices.Contains(s.Config.EDGAR.GuidanceTickers, ticker)
}

// Current is the guidance in force: config.yaml merged with approvals.
func (s *Service) Current(ctx context.Context) ([]Value, error) {
	approved, err := s.Store.ConfirmedGuidance(ctx)
	if err != nil {
		return nil, err
	}
	return Effective(s.Config.EDGAR.CapexGuidance, approved), nil
}

// DocOf is the searched form of a stored transcript or 8-K.
func (s *Service) DocOf(d model.Document) Doc {
	return Doc{Source: d.Source, Ref: d.ExternalID, Title: d.Title, URL: d.URL,
		Given: model.DayIn(d.PublishedAt, s.Config.Calendar.ReleaseLocation), Text: d.Body}
}

// Insert stores candidates inside a write and returns the new ones with
// their ids (a candidate stored before is not returned again).
func Insert(tx *sql.Tx, cands []store.GuidanceCandidate, now time.Time) ([]store.GuidanceCandidate, error) {
	var out []store.GuidanceCandidate
	for _, c := range cands {
		id, inserted, err := store.InsertGuidanceCandidate(tx, c, now)
		if err != nil {
			return out, err
		}
		if inserted {
			c.ID, c.Status, c.FoundAt = id, store.GuidancePending, now
			out = append(out, c)
		}
	}
	return out, nil
}

// Announce sends each new candidate to Telegram with ✅/❌ buttons and
// records the message id. A failure is logged; the candidate stays in
// `macroctl guidance list`.
func (s *Service) Announce(ctx context.Context, cands []store.GuidanceCandidate) {
	if s.TG == nil || len(cands) == 0 {
		return
	}
	current, err := s.Current(ctx)
	if err != nil {
		s.Log.Error("guidance announce", "err", err)
		return
	}
	for _, c := range cands {
		msg, err := s.TG.SendButtons(ctx, Message(c, current), Buttons(c.ID))
		if err != nil {
			s.Log.Warn("guidance candidate not sent to telegram", "id", c.ID, "ticker", c.Ticker, "err", err)
			continue
		}
		if err := s.Store.Write(ctx, func(tx *sql.Tx) error { return store.SetGuidanceMessage(tx, c.ID, msg) }); err != nil {
			s.Log.Error("store guidance message id", "id", c.ID, "err", err)
		}
		s.Log.Info("guidance candidate sent", "id", c.ID, "ticker", c.Ticker, "year", c.Year, "low", c.Low, "high", c.High)
	}
}

// Buttons are the ✅/❌ keyboard of a candidate.
func Buttons(id int64) [][]notify.Button {
	n := strconv.FormatInt(id, 10)
	return [][]notify.Button{{{Text: "✅ Подтвердить", Data: "g:a:" + n}, {Text: "❌ Отклонить", Data: "g:r:" + n}}}
}

// Decide approves or rejects a pending candidate and, when it has a
// Telegram message, edits it to the decision (best-effort, logged).
func (s *Service) Decide(ctx context.Context, id int64, approve bool, via string) (store.GuidanceCandidate, error) {
	var c store.GuidanceCandidate
	err := s.Store.Write(ctx, func(tx *sql.Tx) (err error) {
		c, err = store.DecideGuidanceCandidate(tx, id, approve, via, s.now())
		return err
	})
	if err != nil {
		return c, err
	}
	s.Log.Info("guidance candidate decided", "id", id, "status", c.Status, "via", via, "ticker", c.Ticker, "year", c.Year)
	if s.TG != nil && c.TGMessageID != 0 {
		if err := s.TG.EditText(ctx, c.TGMessageID, Decided(c, s.Config.Location), nil); err != nil {
			s.Log.Warn("guidance message not edited", "id", id, "err", err)
		}
	}
	return c, nil
}

// Scan searches the stored transcripts and earnings 8-Ks of the guidance
// tickers (all when ticker is "") and stores the new candidates, unless
// dry. It sends nothing to Telegram.
func (s *Service) Scan(ctx context.Context, ticker string, dry bool) ([]store.GuidanceCandidate, error) {
	current, err := s.Current(ctx)
	if err != nil {
		return nil, err
	}
	var cands []store.GuidanceCandidate
	for _, kind := range [][2]string{{irtranscript.Source, "transcript"}, {edgar.Source, "8-K"}} {
		docs, err := s.Store.LatestDocuments(ctx, kind[0], kind[1], 1000)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			if !s.Enabled(d.Author) || (ticker != "" && d.Author != ticker) {
				continue
			}
			cands = append(cands, Candidates(d.Author, s.DocOf(d), current)...)
		}
	}
	cands = newestPerYear(cands)
	if dry {
		return cands, nil
	}
	var added []store.GuidanceCandidate
	err = s.Store.Write(ctx, func(tx *sql.Tx) (err error) {
		added, err = Insert(tx, cands, s.now())
		return err
	})
	return added, err
}

// HandleCallback decides the candidate of a ✅/❌ press and answers it.
// Presses are accepted only from TG_CHAT_ID (the bot of internal/bot
// delivers them; phase 14b, D28).
func (s *Service) HandleCallback(ctx context.Context, cb *notify.Callback) {
	answer := func(text string) {
		if err := s.TG.AnswerCallback(ctx, cb.ID, text); err != nil {
			s.Log.Warn("telegram answerCallbackQuery", "err", err)
		}
	}
	if cb.ChatID != s.TG.ChatID {
		s.Log.Warn("guidance press from another chat ignored", "chat", cb.ChatID, "from", cb.FromID)
		answer("Нет доступа")
		return
	}
	id, approve, ok := parseData(cb.Data)
	if !ok {
		answer("Неизвестная кнопка")
		return
	}
	c, err := s.Decide(ctx, id, approve, "telegram")
	switch {
	case errors.Is(err, store.ErrDecided):
		answer("Уже решён: " + statusRU(c.Status))
		if cb.MessageID == c.TGMessageID {
			return // edited at the decision
		}
		if err := s.TG.EditText(ctx, cb.MessageID, Decided(c, s.Config.Location), nil); err != nil {
			s.Log.Warn("guidance message not edited", "id", id, "err", err)
		}
	case errors.Is(err, store.ErrNoCandidate):
		answer("Кандидат не найден")
	case err != nil:
		s.Log.Error("guidance decide", "id", id, "err", err)
		answer("Ошибка, попробуйте ещё раз или macroctl guidance")
	default:
		mark := "✅ Подтверждён"
		if !approve {
			mark = "❌ Отклонён"
		}
		answer(fmt.Sprintf("%s: %s %d, %s", mark, c.Ticker, c.Year, Range(c.Low, c.High)))
		if c.TGMessageID != cb.MessageID { // Decide edited the stored message; this press came from another
			if err := s.TG.EditText(ctx, cb.MessageID, Decided(c, s.Config.Location), nil); err != nil {
				s.Log.Warn("guidance message not edited", "id", id, "err", err)
			}
		}
	}
}

func parseData(data string) (id int64, approve, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "g" || (parts[1] != "a" && parts[1] != "r") {
		return 0, false, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return 0, false, false
	}
	return id, parts[1] == "a", true
}

func statusRU(status string) string {
	switch status {
	case store.GuidanceApproved:
		return "подтверждён"
	case store.GuidanceRejected:
		return "отклонён"
	}
	return status
}

// newestPerYear keeps, for each ticker and year, the candidates of the
// newest document: a scan of the history offers the latest guidance only.
func newestPerYear(cands []store.GuidanceCandidate) []store.GuidanceCandidate {
	newest := map[string]time.Time{}
	key := func(c store.GuidanceCandidate) string { return fmt.Sprint(c.Ticker, c.Year) }
	for _, c := range cands {
		if k := key(c); c.Given.After(newest[k]) {
			newest[k] = c.Given
		}
	}
	return slices.DeleteFunc(cands, func(c store.GuidanceCandidate) bool { return c.Given.Before(newest[key(c)]) })
}
