package classify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"macro-collector/internal/config"
)

const testKey = "AQ.test-key-not-real"

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "classify", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testPrompt(t *testing.T) Prompt {
	t.Helper()
	p, err := LoadPrompt(filepath.Join("..", "..", "prompts", "classify.md"), testTags)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var testTags = []string{"usa_monetary", "usa_fiscal", "usa_micro", "usa_politics", "energy_geopolitics", "eu_ecb", "eu_fiscal", "eu_macro", "eu_micro"}

func geminiConfig(endpoint string) config.Gemini {
	return config.Gemini{
		Tags:    testTags,
		Enabled: true, Endpoint: endpoint + "/v1beta/models/{model}:generateContent", Model: "gemini-3.5-flash-lite",
		BatchSize: 20, MinConfidence: 0.5, Timeout: 10 * time.Second, DailyRequestLimit: 200,
	}
}

// geminiServer answers with the given responses in turn (status, body) and
// records the requests.
type geminiServer struct {
	*httptest.Server
	mu        sync.Mutex
	responses [][2]any
	requests  []*http.Request
	bodies    [][]byte
}

func newGeminiServer(t *testing.T, responses ...[2]any) *geminiServer {
	t.Helper()
	gs := &geminiServer{responses: responses}
	gs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gs.mu.Lock()
		defer gs.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		gs.requests = append(gs.requests, r)
		gs.bodies = append(gs.bodies, body)
		if len(gs.responses) == 0 {
			t.Errorf("unexpected request %d", len(gs.requests))
			http.Error(w, "no response", http.StatusInternalServerError)
			return
		}
		resp := gs.responses[0]
		gs.responses = gs.responses[1:]
		w.WriteHeader(resp[0].(int))
		w.Write(resp[1].([]byte))
	}))
	t.Cleanup(gs.Close)
	return gs
}

func testGemini(t *testing.T, gs *geminiServer) *Gemini {
	g := NewGemini(geminiConfig(gs.URL), testKey, testPrompt(t), nil)
	g.sleep = func(context.Context, time.Duration) error { return nil }
	return g
}

// labeledResponse wraps a model answer in a generateContent response.
func modelResponse(answer string, finish string) []byte {
	b, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{
		"content":      map[string]any{"parts": []any{map[string]string{"text": answer}}, "role": "model"},
		"finishReason": finish,
	}}})
	return b
}

func TestLoadPrompt(t *testing.T) {
	p := testPrompt(t)
	if strings.Contains(p.Text, "<!--") || !strings.HasPrefix(p.Text, "## System") || !strings.Contains(p.Text, "## Examples") || len(p.Hash) != 64 {
		t.Errorf("prompt = %q…, hash %q", p.Text[:40], p.Hash)
	}
	bad := strings.Replace(p.Text, "- `eu_micro` —", "- `crypto` —", 1)
	path := filepath.Join(t.TempDir(), "classify.md")
	os.WriteFile(path, []byte(bad), 0o600)
	if _, err := LoadPrompt(path, testTags); err == nil || !strings.Contains(err.Error(), "differ from classify tags") {
		t.Errorf("tag mismatch err = %v", err)
	}
	os.WriteFile(path, []byte("<!-- ## System ## Input -->\nno sections"), 0o600)
	if _, err := LoadPrompt(path, testTags); err == nil || !strings.Contains(err.Error(), "section") {
		t.Errorf("sections inside the comment must not count: %v", err)
	}
}

func TestGeminiRequest(t *testing.T) {
	gs := newGeminiServer(t, [2]any{200, modelResponse(`[{"id":"1","tags":[{"tag":"usa_monetary","confidence":0.9}],"primary":"usa_monetary","is_junk":false,"reason":"Fed."}]`, "STOP")})
	g := testGemini(t, gs)
	long := strings.Repeat("я", maxPostRunes+100)
	res, err := g.Classify(context.Background(), []Post{{Ref: "marketnews_ua/1", Channel: "marketnews_ua", Text: long}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Primary != "usa_monetary" || res[0].By != ByGemini {
		t.Errorf("res = %+v", res)
	}
	r := gs.requests[0]
	if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-3.5-flash-lite:generateContent" ||
		r.Header.Get("x-goog-api-key") != testKey || r.URL.RawQuery != "" {
		t.Errorf("request %s %s, key header %q", r.Method, r.URL, r.Header.Get("x-goog-api-key"))
	}
	var body struct {
		SystemInstruction struct {
			Parts []struct{ Text string } `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string                  `json:"role"`
			Parts []struct{ Text string } `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			Temperature      float64        `json:"temperature"`
			ResponseMimeType string         `json:"responseMimeType"`
			Schema           map[string]any `json:"responseJsonSchema"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(gs.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body.SystemInstruction.Parts[0].Text != g.prompt.Text || body.GenerationConfig.Temperature != 0 ||
		body.GenerationConfig.ResponseMimeType != "application/json" || body.GenerationConfig.Schema["maxItems"] != float64(1) {
		t.Errorf("generation = %+v", body.GenerationConfig)
	}
	var in []geminiPost
	if err := json.Unmarshal([]byte(body.Contents[0].Parts[0].Text), &in); err != nil || body.Contents[0].Role != "user" {
		t.Fatalf("input = %q, %v", body.Contents[0].Parts[0].Text, err)
	}
	if in[0].ID != "1" || in[0].Channel != "marketnews_ua" || len([]rune(in[0].Text)) != maxPostRunes+2 {
		t.Errorf("post = %s / %s, %d runes", in[0].ID, in[0].Channel, len([]rune(in[0].Text)))
	}
	enum := body.GenerationConfig.Schema["items"].(map[string]any)["properties"].(map[string]any)["tags"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["tag"].(map[string]any)["enum"]
	if len(enum.([]any)) != len(testTags) {
		t.Errorf("tag enum = %v", enum)
	}
}

// Quality on the hand-labeled examples (§11.8) with the recorded answers of
// gemini-3.5-flash-lite (macroctl record-fixtures --source gemini).
func TestGeminiLabeledQuality(t *testing.T) {
	ls, err := LoadLabeled(filepath.Join("..", "..", "testdata", "classify", LabeledFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) < 30 {
		t.Fatalf("labeled examples = %d, want ≥ 30", len(ls))
	}
	posts := LabeledPosts(ls)
	var responses [][2]any
	for i := 0; i*20 < len(posts); i++ {
		responses = append(responses, [2]any{200, testdata(t, RecordedBatchFile(i))})
	}
	g := testGemini(t, newGeminiServer(t, responses...))
	var res []Result
	for start := 0; start < len(posts); start += 20 {
		r, err := g.Classify(context.Background(), posts[start:min(start+20, len(posts))])
		if err != nil {
			t.Fatal(err)
		}
		res = append(res, r...)
	}
	ok := agreement(t, ls, res)
	t.Logf("gemini: %d of %d labeled examples agree", ok, len(ls))
	// 34/38 with the recorded answers (2026-10-25); the misses are
	// borderline posts (Korean IPO, yen intervention, CEO interview digest,
	// Intel equity offering read as liquidity).
	if ok*100 < len(ls)*85 {
		t.Errorf("gemini agreement %d/%d < 85%%", ok, len(ls))
	}
}

// agreement counts results whose primary tag and junk flag match the label.
func agreement(t *testing.T, ls []Labeled, res []Result) int {
	t.Helper()
	ok := 0
	for i, l := range ls {
		r := res[i]
		primary := r.Primary == l.Primary || slices.Contains(l.Accept, r.Primary)
		junk := l.JunkAny || r.IsJunk == l.Junk
		if primary && junk {
			ok++
		} else if testing.Verbose() {
			t.Logf("disagree %s (%s): want %q junk %v, got %q junk %v", l.ID, l.Note, l.Primary, l.Junk, r.Primary, r.IsJunk)
		}
	}
	return ok
}

func TestGeminiErrors(t *testing.T) {
	posts := []Post{{Ref: "a/1", Channel: "a", Text: "x"}, {Ref: "a/2", Channel: "a", Text: "y"}}
	ans := func(id, tags string, junk bool) string {
		j := "false"
		if junk {
			j = "true"
		}
		return `{"id":"` + id + `","tags":[` + tags + `],"primary":null,"is_junk":` + j + `,"reason":""}`
	}
	two := "[" + ans("1", "", false) + "," + ans("2", "", true) + "]"
	quota := []byte(`{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	tests := []struct {
		name      string
		responses [][2]any
		wantErr   error  // nil = success
		answered  string // with success: which posts got an answer, "12", "1", …
		requests  int
	}{
		{"429 then ok", [][2]any{{429, quota}, {200, modelResponse(two, "STOP")}}, nil, "12", 2},
		{"503 three times", [][2]any{{503, []byte(`{"error":{"status":"UNAVAILABLE"}}`)}, {503, []byte(`{}`)}, {503, []byte(`{}`)}}, &APIError{}, "", 3},
		{"400 not retried", [][2]any{{400, []byte(`{"error":{"message":"bad","status":"INVALID_ARGUMENT"}}`)}}, &APIError{}, "", 1},
		{"403 not retried", [][2]any{{403, []byte(`{"error":{"status":"PERMISSION_DENIED"}}`)}}, &APIError{}, "", 1},
		{"cut answer", [][2]any{{200, modelResponse(`[{"id":"1"`, "MAX_TOKENS")}}, ErrInvalidAnswer, "", 1},
		{"broken json", [][2]any{{200, modelResponse(`[{"id":"1"`, "STOP")}}, ErrInvalidAnswer, "", 1},
		{"blocked", [][2]any{{200, []byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`)}}, ErrInvalidAnswer, "", 1},
		{"no known id", [][2]any{{200, modelResponse("["+ans("marketnews_ua/37102", "", false)+"]", "STOP")}}, ErrInvalidAnswer, "", 1},
		// Answers are matched by id: order does not matter, a missing,
		// unknown, repeated or invalid one leaves only its post unanswered.
		{"reversed order", [][2]any{{200, modelResponse("["+ans("2", "", true)+","+ans("1", "", false)+"]", "STOP")}}, nil, "12", 1},
		{"one missing", [][2]any{{200, modelResponse("["+ans("2", "", true)+"]", "STOP")}}, nil, "2", 1},
		{"skipped id", [][2]any{{200, modelResponse("["+ans("1", "", false)+","+ans("3", "", true)+"]", "STOP")}}, nil, "1", 1},
		{"repeated id", [][2]any{{200, modelResponse("["+ans("2", "", true)+","+ans("2", "", false)+"]", "STOP")}}, nil, "2", 1},
		{"unknown tag", [][2]any{{200, modelResponse("["+ans("1", `{"tag":"crypto","confidence":0.9}`, false)+","+ans("2", "", true)+"]", "STOP")}}, nil, "2", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gs := newGeminiServer(t, tt.responses...)
			res, err := testGemini(t, gs).Classify(context.Background(), posts)
			switch tt.wantErr.(type) {
			case nil:
				if err != nil || len(res) != 2 {
					t.Fatalf("res = %+v, err = %v", res, err)
				}
				answered := ""
				for i, r := range res {
					if r.By == ByGemini {
						answered += string(rune('1' + i))
					}
				}
				if answered != tt.answered || (strings.Contains(answered, "2") && !res[1].IsJunk) {
					t.Errorf("answered %q, want %q: %+v", answered, tt.answered, res)
				}
			case *APIError:
				var ae *APIError
				if !errors.As(err, &ae) {
					t.Errorf("err = %v, want APIError", err)
				}
			default:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
			}
			if len(gs.requests) != tt.requests {
				t.Errorf("requests = %d, want %d", len(gs.requests), tt.requests)
			}
			if err != nil && strings.Contains(err.Error(), testKey) {
				t.Errorf("API key in error: %v", err)
			}
		})
	}
}

// Tags under min_confidence are dropped; junk loses its tags; primary is the
// highest confidence, whatever the model put in "primary".
func TestGeminiThresholds(t *testing.T) {
	answer := `[{"id":"1","tags":[{"tag":"usa_fiscal","confidence":0.6},{"tag":"usa_monetary","confidence":0.9},{"tag":"eu_macro","confidence":0.4}],"primary":"usa_fiscal","is_junk":false,"reason":""},
	{"id":"2","tags":[{"tag":"usa_micro","confidence":0.8}],"primary":"usa_micro","is_junk":true,"reason":"ad"}]`
	gs := newGeminiServer(t, [2]any{200, modelResponse(answer, "STOP")})
	res, err := testGemini(t, gs).Classify(context.Background(), []Post{{Ref: "a/1"}, {Ref: "a/2"}})
	if err != nil {
		t.Fatal(err)
	}
	if r := res[0]; r.Primary != "usa_monetary" || len(r.Tags) != 2 || r.Tags[1].Tag != "usa_fiscal" {
		t.Errorf("thresholds = %+v", r)
	}
	if r := res[1]; !r.IsJunk || len(r.Tags) != 0 || r.Primary != "" {
		t.Errorf("junk = %+v", r)
	}
}
