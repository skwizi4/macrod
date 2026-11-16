package doctor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunAndPrint(t *testing.T) {
	checks := []Check{
		{Name: "good", Run: func(context.Context) (string, error) { return "fine", nil }},
		{Name: "bad", Run: func(context.Context) (string, error) { return "", errors.New("boom") }},
		{Name: "slow", Run: func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }},
	}
	res := Run(context.Background(), checks, 20*time.Millisecond)
	var b bytes.Buffer
	if Print(&b, res) {
		t.Error("Print reported all ok")
	}
	out := b.String()
	for _, want := range []string{"✅ good", "fine", "❌ bad", "boom", "❌ slow", "deadline exceeded"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !Print(&bytes.Buffer{}, res[:1]) {
		t.Error("Print of passing checks reported a failure")
	}
}

func TestEnvSet(t *testing.T) {
	t.Setenv("DOCTOR_A", "secret-value")
	t.Setenv("DOCTOR_B", " ")
	_, err := EnvSet("DOCTOR_A", "DOCTOR_B").Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DOCTOR_B") || strings.Contains(err.Error(), "DOCTOR_A") {
		t.Errorf("err = %v", err)
	}
	if d, err := EnvSet("DOCTOR_A").Run(context.Background()); err != nil || strings.Contains(d, "secret") {
		t.Errorf("detail %q, err %v", d, err)
	}
}

func TestDBWritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "macro.db")
	if d, err := DBWritable(path).Run(context.Background()); err != nil || !strings.Contains(d, "not created yet") {
		t.Fatalf("missing db: %q, %v", d, err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if d, err := DBWritable(path).Run(context.Background()); err != nil || !strings.HasPrefix(d, "writable") {
		t.Fatalf("existing db: %q, %v", d, err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := DBWritable(path).Run(context.Background()); err == nil {
		t.Error("read-only db passed")
	}
}

func TestDiskFree(t *testing.T) {
	dir := t.TempDir()
	if _, err := DiskFree(dir, 1).Run(context.Background()); err != nil {
		t.Errorf("1 byte: %v", err)
	}
	if _, err := DiskFree(dir, 1<<62).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "want at least") {
		t.Errorf("huge minimum: %v", err)
	}
}

func TestGeminiModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models/gemini-x" || r.Header.Get("x-goog-api-key") != "k" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"name":"models/gemini-x"}`))
	}))
	defer srv.Close()
	endpoint := srv.URL + "/v1beta/models/{model}:generateContent"
	if _, err := GeminiModel(srv.Client(), endpoint, "gemini-x", "k").Run(context.Background()); err != nil {
		t.Errorf("good key: %v", err)
	}
	if _, err := GeminiModel(srv.Client(), endpoint, "gemini-x", "bad").Run(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("bad key: %v", err)
	}
}

func TestFileReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if _, err := FileReadable("k", "").Run(context.Background()); err == nil {
		t.Error("empty path passed")
	}
	if _, err := FileReadable("k", path).Run(context.Background()); err == nil {
		t.Error("missing file passed")
	}
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FileReadable("k", path).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "600") {
		t.Errorf("open mode: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := FileReadable("k", path).Run(context.Background()); err != nil || strings.Contains(d, "secret") {
		t.Errorf("%q, %v", d, err)
	}
}
