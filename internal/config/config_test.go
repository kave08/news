package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadFromLookupEnvWebhookMode(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFromLookupEnv(lookupFromMap(map[string]string{
		"BALE_BOT_TOKEN":         "token",
		"BALE_ALLOWED_CHAT_IDS":  "1001, -1002",
		"BALE_ALLOWED_HASHTAGS":  "#news, #urgent",
		"BALE_STRIP_MENTIONS":    "@tehran_alarm, bot_account",
		"BALE_STRIP_PHRASES":     "first phrase, second phrase",
		"MATTERMOST_MODE":        "webhook",
		"MATTERMOST_WEBHOOK_URL": "https://mattermost.example/hooks/abc",
		"BALE_POLL_TIMEOUT_SEC":  "30",
		"LOG_LEVEL":              "debug",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookupEnv returned error: %v", err)
	}

	if cfg.Bale.PollTimeout != 30*time.Second {
		t.Fatalf("unexpected Bale poll timeout: %v", cfg.Bale.PollTimeout)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("unexpected log level: %v", cfg.LogLevel)
	}
	if cfg.News.Enabled {
		t.Fatal("news should be disabled by default")
	}
	if len(cfg.Bale.AllowedChatIDs) != 2 {
		t.Fatalf("unexpected allowed chat count: %d", len(cfg.Bale.AllowedChatIDs))
	}
	if len(cfg.Bale.AllowedHashtags) != 2 || cfg.Bale.AllowedHashtags[0] != "#news" {
		t.Fatalf("unexpected allowed hashtags: %+v", cfg.Bale.AllowedHashtags)
	}
	if len(cfg.Bale.StripMentions) != 2 || cfg.Bale.StripMentions[1] != "@bot_account" {
		t.Fatalf("unexpected strip mentions: %+v", cfg.Bale.StripMentions)
	}
	if len(cfg.Bale.StripPhrases) != 2 || cfg.Bale.StripPhrases[0] != "first phrase" {
		t.Fatalf("unexpected strip phrases: %+v", cfg.Bale.StripPhrases)
	}
	if _, ok := cfg.Bale.AllowedChatIDs[-1002]; !ok {
		t.Fatalf("expected chat id -1002 to be allowed")
	}
}

func TestLoadFromLookupEnvAPIModeValidation(t *testing.T) {
	t.Parallel()

	_, err := LoadFromLookupEnv(lookupFromMap(map[string]string{
		"BALE_BOT_TOKEN":        "token",
		"BALE_ALLOWED_CHAT_IDS": "1001",
		"MATTERMOST_MODE":       "api",
	}))
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
}

func TestLoadFromLookupEnvInvalidChatIDs(t *testing.T) {
	t.Parallel()

	_, err := LoadFromLookupEnv(lookupFromMap(map[string]string{
		"BALE_BOT_TOKEN":         "token",
		"BALE_ALLOWED_CHAT_IDS":  "abc",
		"MATTERMOST_MODE":        "webhook",
		"MATTERMOST_WEBHOOK_URL": "https://mattermost.example/hooks/abc",
	}))
	if err == nil {
		t.Fatal("expected chat id validation error, got nil")
	}
}

func TestLoadFromLookupEnvNewsDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFromLookupEnv(lookupFromMap(map[string]string{
		"BALE_BOT_TOKEN":         "token",
		"BALE_ALLOWED_CHAT_IDS":  "1001",
		"MATTERMOST_MODE":        "webhook",
		"MATTERMOST_WEBHOOK_URL": "https://mattermost.example/hooks/abc",
		"NEWS_ENABLED":           "true",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookupEnv returned error: %v", err)
	}

	if !cfg.News.Enabled {
		t.Fatal("expected news to be enabled")
	}
	if cfg.News.Interval != 15*time.Minute {
		t.Fatalf("unexpected news interval: %v", cfg.News.Interval)
	}
	if cfg.News.MaxArticlesPerCycle != 5 {
		t.Fatalf("unexpected max articles: %d", cfg.News.MaxArticlesPerCycle)
	}
	if len(cfg.News.Sites) != 3 {
		t.Fatalf("unexpected site count: %d", len(cfg.News.Sites))
	}
}

func TestLoadFromLookupEnvNewsJSONOverride(t *testing.T) {
	t.Parallel()

	cfg, err := LoadFromLookupEnv(lookupFromMap(map[string]string{
		"BALE_BOT_TOKEN":         "token",
		"BALE_ALLOWED_CHAT_IDS":  "1001",
		"MATTERMOST_MODE":        "webhook",
		"MATTERMOST_WEBHOOK_URL": "https://mattermost.example/hooks/abc",
		"NEWS_ENABLED":           "true",
		"NEWS_SITES_JSON":        `[{"name":"Custom","url":"https://example.com","article_selector":".item","title_selector":"h2","link_selector":"a[href]","keywords":["iran"]}]`,
	}))
	if err != nil {
		t.Fatalf("LoadFromLookupEnv returned error: %v", err)
	}

	if len(cfg.News.Sites) != 1 {
		t.Fatalf("unexpected site count: %d", len(cfg.News.Sites))
	}
	if cfg.News.Sites[0].Name != "Custom" {
		t.Fatalf("unexpected site: %+v", cfg.News.Sites[0])
	}
}

func TestLoadFromLookupEnvNewsValidation(t *testing.T) {
	t.Parallel()

	_, err := LoadFromLookupEnv(lookupFromMap(map[string]string{
		"BALE_BOT_TOKEN":              "token",
		"BALE_ALLOWED_CHAT_IDS":       "1001",
		"MATTERMOST_MODE":             "webhook",
		"MATTERMOST_WEBHOOK_URL":      "https://mattermost.example/hooks/abc",
		"NEWS_ENABLED":                "true",
		"NEWS_INTERVAL":               "bad",
		"NEWS_FETCH_DELAY_MIN_SEC":    "10",
		"NEWS_FETCH_DELAY_MAX_SEC":    "1",
		"NEWS_SITES_JSON":             `[]`,
		"NEWS_MAX_ARTICLES_PER_CYCLE": "0",
	}))
	if err == nil {
		t.Fatal("expected news validation error, got nil")
	}
	if !strings.Contains(err.Error(), "NEWS_INTERVAL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func lookupFromMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
