// Package notify sends messages through a Telegram bot (ТЗ §12.11; Bot API,
// LOOKUP 2026-11-06: https://api.telegram.org/bot<token>/METHOD, JSON body,
// answers {ok, result, description, error_code, parameters.retry_after}).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MaxText is the sendMessage text limit (characters).
const MaxText = 4096

// maxRetryAfter caps the one wait on HTTP 429.
const maxRetryAfter = time.Minute

// Telegram is a bot client. The token is part of the request URL, so errors
// never include the URL.
type Telegram struct {
	APIURL string // https://api.telegram.org
	Token  string
	ChatID string
	HTTP   *http.Client
}

// New returns a client with a 30 s timeout.
func New(apiURL, token, chatID string) *Telegram {
	return &Telegram{APIURL: apiURL, Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// APIError is an unsuccessful Bot API answer.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

type answer struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Send posts text to the chat, cut to MaxText characters. On 429 it waits
// retry_after once (at most a minute) and tries again.
func (t *Telegram) Send(ctx context.Context, text string) error {
	body := map[string]any{
		"chat_id":              t.ChatID,
		"text":                 cut(text),
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	_, err := t.call(ctx, "sendMessage", body)
	if e, ok := err.(*APIError); ok && e.Code == http.StatusTooManyRequests && e.RetryAfter > 0 {
		wait := min(time.Duration(e.RetryAfter)*time.Second, maxRetryAfter)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (stopped: %w)", err, ctx.Err())
		case <-time.After(wait):
		}
		_, err = t.call(ctx, "sendMessage", body)
	}
	return err
}

// Me checks the token (getMe) and returns the bot's username.
func (t *Telegram) Me(ctx context.Context) (string, error) {
	raw, err := t.call(ctx, "getMe", nil)
	if err != nil {
		return "", err
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		return "", fmt.Errorf("telegram getMe: %w", err)
	}
	return me.Username, nil
}

func (t *Telegram) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	var payload io.Reader
	httpMethod := http.MethodGet
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("telegram %s: %w", method, err)
		}
		payload, httpMethod = bytes.NewReader(raw), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, httpMethod, t.APIURL+"/bot"+t.Token+"/"+method, payload)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: bad api url", method)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		// *url.Error carries the URL with the token: keep only the cause.
		return nil, fmt.Errorf("telegram %s: request failed: %w", method, unwrapURL(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("telegram %s read: %w", method, unwrapURL(err))
	}
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, &APIError{Method: method, Code: resp.StatusCode, Description: "not a Bot API answer"}
	}
	if !a.OK {
		code := a.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return nil, &APIError{Method: method, Code: code, Description: a.Description, RetryAfter: a.Parameters.RetryAfter}
	}
	return a.Result, nil
}
