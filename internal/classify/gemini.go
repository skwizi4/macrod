package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"macro-collector/internal/config"
	"macro-collector/internal/sources/httpx"
)

// maxPostRunes caps the text of one post sent to Gemini: the topic is clear
// from the start of a long article, and the batch stays small.
const maxPostRunes = 3000

// ErrInvalidAnswer is a response that cannot be used at all: not JSON,
// blocked or cut, or without a single valid answer. Single bad answers in
// a batch only leave their posts unanswered.
var ErrInvalidAnswer = errors.New("invalid gemini answer")

// APIError is a non-2xx response of the Gemini API (LOOKUP M3).
type APIError struct {
	Status  int
	State   string // RESOURCE_EXHAUSTED, UNAVAILABLE, ...
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gemini: status %d %s: %s", e.Status, e.State, e.Message)
}

func (e *APIError) retryable() bool {
	return e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Gemini classifies a batch with one generateContent request (LOOKUP M1).
type Gemini struct {
	cfg     config.Gemini
	key     string
	prompt  Prompt
	http    *http.Client
	retries int
	backoff time.Duration
	sleep   func(context.Context, time.Duration) error
	log     *slog.Logger
}

// NewGemini returns the classifier; key is GEMINI_API_KEY and must not be
// empty.
func NewGemini(cfg config.Gemini, key string, prompt Prompt, log *slog.Logger) *Gemini {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Gemini{
		cfg: cfg, key: key, prompt: prompt,
		http:    &http.Client{Timeout: cfg.Timeout},
		retries: 2, backoff: 2 * time.Second, sleep: httpx.SleepCtx, log: log,
	}
}

// Model is the configured model id, stored with cached answers.
func (g *Gemini) Model() string { return g.cfg.Model }

type geminiPost struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// answer is one element of the model's JSON array.
type answer struct {
	ID      string  `json:"id"`
	Tags    []Tag   `json:"tags"`
	Primary *string `json:"primary"`
	IsJunk  bool    `json:"is_junk"`
	Reason  string  `json:"reason"`
}

// Classify sends posts as one request. Network errors, 408, 429 and 5xx
// are retried; the error of the last attempt is returned. A post without a
// valid answer gets a Result with an empty By: the caller classifies it
// another way.
func (g *Gemini) Classify(ctx context.Context, posts []Post) ([]Result, error) {
	body, err := g.request(posts)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		text, err := g.call(ctx, body)
		if err == nil {
			return g.parse(posts, text)
		}
		var ae *APIError
		if ctx.Err() != nil || errors.Is(err, ErrInvalidAnswer) || (errors.As(err, &ae) && !ae.retryable()) || attempt >= g.retries {
			return nil, err
		}
		wait := g.backoff<<attempt + time.Duration(rand.Int64N(int64(g.backoff)))
		g.log.Warn("gemini request failed, retrying", "attempt", attempt+1, "wait", wait, "err", err)
		if serr := g.sleep(ctx, wait); serr != nil {
			return nil, err
		}
	}
}

func (g *Gemini) request(posts []Post) ([]byte, error) {
	in := make([]geminiPost, len(posts))
	for i, p := range posts {
		text := p.Text
		if r := []rune(text); len(r) > maxPostRunes {
			text = string(r[:maxPostRunes]) + " …"
		}
		// Ids are positions in the batch: post refs with gaps (Telegram ids
		// of posts without text) made the model "fix" the sequence.
		in[i] = geminiPost{ID: strconv.Itoa(i + 1), Channel: p.Channel, Text: text}
	}
	input, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("gemini request: %w", err)
	}
	return json.Marshal(map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]string{"text": g.prompt.Text}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": string(input)}}}},
		"generationConfig": map[string]any{
			"temperature":        0,
			"responseMimeType":   "application/json",
			"responseJsonSchema": schema(len(posts), g.cfg.Tags),
		},
	})
}

// schema is the JSON Schema of the answer: exactly n objects, tags only from
// classify.tags (LOOKUP M1).
func schema(n int, tags []string) map[string]any {
	primary := make([]any, 0, len(tags)+1)
	for _, t := range tags {
		primary = append(primary, t)
	}
	primary = append(primary, nil)
	return map[string]any{
		"type": "array", "minItems": n, "maxItems": n,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
				"tags": map[string]any{"type": "array", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tag":        map[string]any{"type": "string", "enum": tags},
						"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
					},
					"required": []string{"tag", "confidence"},
				}},
				"primary": map[string]any{"type": []string{"string", "null"}, "enum": primary},
				"is_junk": map[string]any{"type": "boolean"},
				"reason":  map[string]any{"type": "string"},
			},
			"required": []string{"id", "tags", "primary", "is_junk", "reason"},
		},
	}
}

// call posts the request and returns the answer text.
func (g *Gemini) call(ctx context.Context, body []byte) (string, error) {
	raw, err := g.do(ctx, body)
	if err != nil {
		return "", err
	}
	return answerText(raw)
}

// do posts the request and returns the raw 200 response.
func (g *Gemini) do(ctx context.Context, body []byte) ([]byte, error) {
	url := strings.ReplaceAll(g.cfg.Endpoint, "{model}", g.cfg.Model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.key)
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini %s: %w", g.cfg.Model, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("gemini %s read: %w", g.cfg.Model, err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, &APIError{Status: resp.StatusCode, State: e.Error.Status, Message: e.Error.Message}
	}
	return raw, nil
}

// answerText extracts the model's JSON text from a generateContent response.
func answerText(raw []byte) (string, error) {
	var r struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidAnswer, err)
	}
	if r.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("%w: prompt blocked: %s", ErrInvalidAnswer, r.PromptFeedback.BlockReason)
	}
	if len(r.Candidates) == 0 || len(r.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("%w: no candidates", ErrInvalidAnswer)
	}
	if fr := r.Candidates[0].FinishReason; fr != "STOP" {
		return "", fmt.Errorf("%w: finish reason %s", ErrInvalidAnswer, fr)
	}
	var b strings.Builder
	for _, p := range r.Candidates[0].Content.Parts {
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// parse matches answers to posts by id (the position in the batch). An
// answer is used if its tags are known with confidence in [0, 1]; tags below
// min_confidence are dropped and junk posts get no tags. Posts without a
// usable answer keep an empty By. The channel's default_tag is added by the
// Runner, since a cached answer may serve another channel.
func (g *Gemini) parse(posts []Post, text string) ([]Result, error) {
	var as []answer
	if err := json.Unmarshal([]byte(text), &as); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidAnswer, err)
	}
	out := make([]Result, len(posts))
	var problems []string
	used := 0
	for _, a := range as {
		n, err := strconv.Atoi(a.ID)
		if err != nil || n < 1 || n > len(posts) || out[n-1].By != "" {
			problems = append(problems, fmt.Sprintf("unknown or repeated id %q", a.ID))
			continue
		}
		res, err := g.result(a)
		if err != nil {
			problems = append(problems, fmt.Sprintf("post %s: %v", a.ID, err))
			continue
		}
		out[n-1] = res
		used++
	}
	if used == 0 {
		return nil, fmt.Errorf("%w: no usable answer for %d posts (%s)", ErrInvalidAnswer, len(posts), strings.Join(problems, "; "))
	}
	if used < len(posts) || len(problems) > 0 {
		g.log.Warn("gemini answered part of the batch", "posts", len(posts), "answered", used, "problems", strings.Join(problems, "; "))
	}
	return out, nil
}

func (g *Gemini) result(a answer) (Result, error) {
	res := Result{IsJunk: a.IsJunk, Reason: a.Reason, By: ByGemini}
	seen := make(map[string]bool)
	for _, t := range a.Tags {
		if !slices.Contains(g.cfg.Tags, t.Tag) || t.Score < 0 || t.Score > 1 {
			return Result{}, fmt.Errorf("tag %q confidence %v", t.Tag, t.Score)
		}
		if !a.IsJunk && t.Score >= g.cfg.MinConfidence && !seen[t.Tag] {
			res.Tags = append(res.Tags, t)
			seen[t.Tag] = true
		}
	}
	if a.Primary != nil && *a.Primary != "" && !slices.Contains(g.cfg.Tags, *a.Primary) {
		return Result{}, fmt.Errorf("primary %q", *a.Primary)
	}
	res.setPrimary(g.cfg.Tags)
	return res, nil
}
