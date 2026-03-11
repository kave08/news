package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	values, err := parseDotEnv(`
# comment
BALE_BOT_TOKEN=plain-token
MATTERMOST_MODE=webhook
MATTERMOST_WEBHOOK_URL="https://mattermost.example/hooks/abc"
export LOG_LEVEL='debug'
`)
	if err != nil {
		t.Fatalf("parseDotEnv returned error: %v", err)
	}

	if values["BALE_BOT_TOKEN"] != "plain-token" {
		t.Fatalf("unexpected token: %q", values["BALE_BOT_TOKEN"])
	}
	if values["MATTERMOST_WEBHOOK_URL"] != "https://mattermost.example/hooks/abc" {
		t.Fatalf("unexpected webhook url: %q", values["MATTERMOST_WEBHOOK_URL"])
	}
	if values["LOG_LEVEL"] != "debug" {
		t.Fatalf("unexpected log level: %q", values["LOG_LEVEL"])
	}
}

func TestLoadDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	t.Setenv("BALE_BOT_TOKEN", "already-set")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("BALE_BOT_TOKEN=file-token\nMATTERMOST_MODE=webhook\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv returned error: %v", err)
	}

	if got := os.Getenv("BALE_BOT_TOKEN"); got != "already-set" {
		t.Fatalf("expected existing env to win, got %q", got)
	}
	if got := os.Getenv("MATTERMOST_MODE"); got != "webhook" {
		t.Fatalf("expected MATTERMOST_MODE to be loaded, got %q", got)
	}
}
