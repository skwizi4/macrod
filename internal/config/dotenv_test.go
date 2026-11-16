package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	in := "# comment\r\n" +
		"FRED_API_KEY=abc123\r\n" +
		"\r\n" +
		"export TG_PHONE=\"+421 900\"\n" +
		"EMPTY=\n" +
		"QUOTED='x=y'\n"
	got, err := parseDotEnv(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"FRED_API_KEY": "abc123", // no trailing \r
		"TG_PHONE":     "+421 900",
		"EMPTY":        "",
		"QUOTED":       "x=y",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseDotEnvMalformed(t *testing.T) {
	if _, err := parseDotEnv(strings.NewReader("A=1\nNOEQUALS\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v, want line 2 error", err)
	}
}

func TestLoadDotEnvKeepsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("MACRO_TEST_KEEP=file\nMACRO_TEST_NEW=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACRO_TEST_KEEP", "env")
	t.Setenv("MACRO_TEST_NEW", "")
	os.Unsetenv("MACRO_TEST_NEW")

	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("MACRO_TEST_KEEP"); v != "env" {
		t.Errorf("MACRO_TEST_KEEP = %q, want env", v)
	}
	if v := os.Getenv("MACRO_TEST_NEW"); v != "file" {
		t.Errorf("MACRO_TEST_NEW = %q, want file", v)
	}
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}
