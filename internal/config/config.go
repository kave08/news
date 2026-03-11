package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaleBaseURL     = "https://tapi.bale.ai"
	defaultSQLitePath      = "./data/relay.db"
	defaultBalePollTimeout = 20 * time.Second
	defaultMattermostMode  = MattermostModeWebhook
	defaultNewsInterval    = 15 * time.Minute
	defaultNewsMaxArticles = 5
	defaultNewsUserAgent   = "NewsBot/1.0"
	MattermostModeWebhook  = "webhook"
	MattermostModeAPI      = "api"
)

var ErrInvalidMattermostMode = errors.New("invalid MATTERMOST_MODE")

type Config struct {
	Bale       BaleConfig
	Mattermost MattermostConfig
	News       NewsConfig
	SQLitePath string
	LogLevel   slog.Level
}

type BaleConfig struct {
	Token           string
	BaseURL         string
	AllowedChatIDs  map[int64]struct{}
	AllowedHashtags []string
	StripMentions   []string
	StripPhrases    []string
	PollTimeout     time.Duration
}

type MattermostConfig struct {
	Mode       string
	WebhookURL string
	BaseURL    string
	BotToken   string
	ChannelID  string
}

type NewsConfig struct {
	Enabled             bool
	Interval            time.Duration
	MaxArticlesPerCycle int
	RequestTimeout      time.Duration
	FetchDelayMin       time.Duration
	FetchDelayMax       time.Duration
	UserAgent           string
	Sites               []NewsSiteConfig
}

type NewsSiteConfig struct {
	Name            string   `json:"name"`
	URL             string   `json:"url"`
	ArticleSelector string   `json:"article_selector"`
	TitleSelector   string   `json:"title_selector"`
	SummarySelector string   `json:"summary_selector"`
	LinkSelector    string   `json:"link_selector"`
	DateSelector    string   `json:"date_selector"`
	Keywords        []string `json:"keywords"`
}

func Load() (Config, error) {
	return LoadFromLookupEnv(func(key string) (string, bool) {
		value, ok := lookupEnv(key)
		return value, ok
	})
}

func LoadFromLookupEnv(lookup func(string) (string, bool)) (Config, error) {
	var cfg Config
	var validationErrs []string

	cfg.Bale.Token = strings.TrimSpace(env(lookup, "BALE_BOT_TOKEN", ""))
	cfg.Bale.BaseURL = normalizeURL(env(lookup, "BALE_API_BASE_URL", defaultBaleBaseURL))
	cfg.Bale.PollTimeout = defaultBalePollTimeout
	cfg.SQLitePath = strings.TrimSpace(env(lookup, "SQLITE_PATH", defaultSQLitePath))
	cfg.Mattermost.Mode = strings.ToLower(strings.TrimSpace(env(lookup, "MATTERMOST_MODE", defaultMattermostMode)))
	cfg.LogLevel = parseLogLevel(env(lookup, "LOG_LEVEL", "info"))
	cfg.News.Interval = defaultNewsInterval
	cfg.News.MaxArticlesPerCycle = defaultNewsMaxArticles
	cfg.News.RequestTimeout = 30 * time.Second
	cfg.News.FetchDelayMin = time.Second
	cfg.News.FetchDelayMax = 5 * time.Second
	cfg.News.UserAgent = defaultNewsUserAgent

	if timeoutRaw := strings.TrimSpace(env(lookup, "BALE_POLL_TIMEOUT_SEC", "")); timeoutRaw != "" {
		timeoutSec, err := strconv.Atoi(timeoutRaw)
		if err != nil || timeoutSec <= 0 {
			validationErrs = append(validationErrs, "BALE_POLL_TIMEOUT_SEC must be a positive integer")
		} else {
			cfg.Bale.PollTimeout = time.Duration(timeoutSec) * time.Second
		}
	}

	allowedChatIDs, err := parseChatIDs(env(lookup, "BALE_ALLOWED_CHAT_IDS", ""))
	if err != nil {
		validationErrs = append(validationErrs, err.Error())
	} else {
		cfg.Bale.AllowedChatIDs = allowedChatIDs
	}
	cfg.Bale.AllowedHashtags = parseStringList(env(lookup, "BALE_ALLOWED_HASHTAGS", ""))
	cfg.Bale.StripMentions = normalizeMentions(parseStringList(env(lookup, "BALE_STRIP_MENTIONS", "")))
	cfg.Bale.StripPhrases = parseStringList(env(lookup, "BALE_STRIP_PHRASES", ""))

	if cfg.Bale.Token == "" {
		validationErrs = append(validationErrs, "BALE_BOT_TOKEN is required")
	}

	switch cfg.Mattermost.Mode {
	case MattermostModeWebhook:
		cfg.Mattermost.WebhookURL = strings.TrimSpace(env(lookup, "MATTERMOST_WEBHOOK_URL", ""))
		if cfg.Mattermost.WebhookURL == "" {
			validationErrs = append(validationErrs, "MATTERMOST_WEBHOOK_URL is required when MATTERMOST_MODE=webhook")
		} else if _, err := url.ParseRequestURI(cfg.Mattermost.WebhookURL); err != nil {
			validationErrs = append(validationErrs, "MATTERMOST_WEBHOOK_URL must be a valid URL")
		}
	case MattermostModeAPI:
		cfg.Mattermost.BaseURL = normalizeURL(env(lookup, "MATTERMOST_BASE_URL", ""))
		cfg.Mattermost.BotToken = strings.TrimSpace(env(lookup, "MATTERMOST_BOT_TOKEN", ""))
		cfg.Mattermost.ChannelID = strings.TrimSpace(env(lookup, "MATTERMOST_CHANNEL_ID", ""))
		if cfg.Mattermost.BaseURL == "" {
			validationErrs = append(validationErrs, "MATTERMOST_BASE_URL is required when MATTERMOST_MODE=api")
		}
		if cfg.Mattermost.BotToken == "" {
			validationErrs = append(validationErrs, "MATTERMOST_BOT_TOKEN is required when MATTERMOST_MODE=api")
		}
		if cfg.Mattermost.ChannelID == "" {
			validationErrs = append(validationErrs, "MATTERMOST_CHANNEL_ID is required when MATTERMOST_MODE=api")
		}
	default:
		validationErrs = append(validationErrs, ErrInvalidMattermostMode.Error())
	}

	if newsEnabledRaw := strings.TrimSpace(env(lookup, "NEWS_ENABLED", "")); newsEnabledRaw != "" {
		enabled, err := strconv.ParseBool(newsEnabledRaw)
		if err != nil {
			validationErrs = append(validationErrs, "NEWS_ENABLED must be a boolean")
		} else {
			cfg.News.Enabled = enabled
		}
	}

	if cfg.News.Enabled {
		if intervalRaw := strings.TrimSpace(env(lookup, "NEWS_INTERVAL", "")); intervalRaw != "" {
			interval, err := time.ParseDuration(intervalRaw)
			if err != nil || interval <= 0 {
				validationErrs = append(validationErrs, "NEWS_INTERVAL must be a positive duration")
			} else {
				cfg.News.Interval = interval
			}
		}

		if maxArticlesRaw := strings.TrimSpace(env(lookup, "NEWS_MAX_ARTICLES_PER_CYCLE", "")); maxArticlesRaw != "" {
			maxArticles, err := strconv.Atoi(maxArticlesRaw)
			if err != nil || maxArticles <= 0 {
				validationErrs = append(validationErrs, "NEWS_MAX_ARTICLES_PER_CYCLE must be a positive integer")
			} else {
				cfg.News.MaxArticlesPerCycle = maxArticles
			}
		}

		if requestTimeoutRaw := strings.TrimSpace(env(lookup, "NEWS_REQUEST_TIMEOUT_SEC", "")); requestTimeoutRaw != "" {
			requestTimeout, err := strconv.Atoi(requestTimeoutRaw)
			if err != nil || requestTimeout <= 0 {
				validationErrs = append(validationErrs, "NEWS_REQUEST_TIMEOUT_SEC must be a positive integer")
			} else {
				cfg.News.RequestTimeout = time.Duration(requestTimeout) * time.Second
			}
		}

		if fetchDelayMinRaw := strings.TrimSpace(env(lookup, "NEWS_FETCH_DELAY_MIN_SEC", "")); fetchDelayMinRaw != "" {
			delay, err := strconv.Atoi(fetchDelayMinRaw)
			if err != nil || delay < 0 {
				validationErrs = append(validationErrs, "NEWS_FETCH_DELAY_MIN_SEC must be a non-negative integer")
			} else {
				cfg.News.FetchDelayMin = time.Duration(delay) * time.Second
			}
		}

		if fetchDelayMaxRaw := strings.TrimSpace(env(lookup, "NEWS_FETCH_DELAY_MAX_SEC", "")); fetchDelayMaxRaw != "" {
			delay, err := strconv.Atoi(fetchDelayMaxRaw)
			if err != nil || delay < 0 {
				validationErrs = append(validationErrs, "NEWS_FETCH_DELAY_MAX_SEC must be a non-negative integer")
			} else {
				cfg.News.FetchDelayMax = time.Duration(delay) * time.Second
			}
		}

		cfg.News.UserAgent = strings.TrimSpace(env(lookup, "NEWS_USER_AGENT", defaultNewsUserAgent))
		if cfg.News.UserAgent == "" {
			cfg.News.UserAgent = defaultNewsUserAgent
		}

		if cfg.News.FetchDelayMax < cfg.News.FetchDelayMin {
			validationErrs = append(validationErrs, "NEWS_FETCH_DELAY_MAX_SEC must be greater than or equal to NEWS_FETCH_DELAY_MIN_SEC")
		}

		sitesRaw := strings.TrimSpace(env(lookup, "NEWS_SITES_JSON", ""))
		if sitesRaw == "" {
			cfg.News.Sites = defaultNewsSites()
		} else {
			sites, err := parseNewsSitesJSON(sitesRaw)
			if err != nil {
				validationErrs = append(validationErrs, err.Error())
			} else {
				cfg.News.Sites = sites
			}
		}
	}

	if len(validationErrs) > 0 {
		return Config{}, errors.New(strings.Join(validationErrs, "; "))
	}

	return cfg, nil
}

func parseChatIDs(raw string) (map[int64]struct{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("BALE_ALLOWED_CHAT_IDS is required")
	}

	allowed := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		chatID, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("BALE_ALLOWED_CHAT_IDS contains invalid chat id %q", part)
		}
		allowed[chatID] = struct{}{}
	}

	if len(allowed) == 0 {
		return nil, errors.New("BALE_ALLOWED_CHAT_IDS is required")
	}

	return allowed, nil
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		values = append(values, part)
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func normalizeMentions(mentions []string) []string {
	if len(mentions) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(mentions))
	seen := make(map[string]struct{}, len(mentions))
	for _, mention := range mentions {
		mention = strings.TrimSpace(mention)
		if mention == "" {
			continue
		}
		if !strings.HasPrefix(mention, "@") {
			mention = "@" + mention
		}
		if _, ok := seen[mention]; ok {
			continue
		}
		seen[mention] = struct{}{}
		normalized = append(normalized, mention)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func parseNewsSitesJSON(raw string) ([]NewsSiteConfig, error) {
	var sites []NewsSiteConfig
	if err := json.Unmarshal([]byte(raw), &sites); err != nil {
		return nil, fmt.Errorf("NEWS_SITES_JSON must be valid JSON: %w", err)
	}
	return validateNewsSites(sites)
}

func validateNewsSites(sites []NewsSiteConfig) ([]NewsSiteConfig, error) {
	if len(sites) == 0 {
		return nil, errors.New("NEWS_SITES_JSON must contain at least one site")
	}

	validated := make([]NewsSiteConfig, 0, len(sites))
	for _, site := range sites {
		site.Name = strings.TrimSpace(site.Name)
		site.URL = normalizeURL(site.URL)
		site.ArticleSelector = strings.TrimSpace(site.ArticleSelector)
		site.TitleSelector = strings.TrimSpace(site.TitleSelector)
		site.SummarySelector = strings.TrimSpace(site.SummarySelector)
		site.LinkSelector = strings.TrimSpace(site.LinkSelector)
		site.DateSelector = strings.TrimSpace(site.DateSelector)

		if site.Name == "" {
			return nil, errors.New("news site name is required")
		}
		if site.URL == "" {
			return nil, fmt.Errorf("news site %q URL is required", site.Name)
		}
		if _, err := url.ParseRequestURI(site.URL); err != nil {
			return nil, fmt.Errorf("news site %q URL must be valid", site.Name)
		}
		if site.ArticleSelector == "" || site.TitleSelector == "" || site.LinkSelector == "" {
			return nil, fmt.Errorf("news site %q must include article_selector, title_selector, and link_selector", site.Name)
		}

		keywords := make([]string, 0, len(site.Keywords))
		for _, keyword := range site.Keywords {
			keyword = strings.TrimSpace(keyword)
			if keyword == "" {
				continue
			}
			keywords = append(keywords, keyword)
		}
		site.Keywords = keywords
		validated = append(validated, site)
	}

	return validated, nil
}

func defaultNewsSites() []NewsSiteConfig {
	sites, err := validateNewsSites([]NewsSiteConfig{
		{
			Name:            "BBC Persian",
			URL:             "https://www.bbc.com/persian",
			ArticleSelector: ".bbc-1fdatix",
			TitleSelector:   "h3",
			SummarySelector: "p",
			LinkSelector:    "a[href]",
			DateSelector:    "time",
			Keywords:        []string{"جنگ", "ایران", "حمله", "پهپاد", "اسرائیل", "آمریکا"},
		},
		{
			Name:            "Iran International",
			URL:             "https://www.iranintl.com",
			ArticleSelector: ".news-item",
			TitleSelector:   ".news-item__title",
			SummarySelector: ".news-item__excerpt",
			LinkSelector:    "a[href]",
			DateSelector:    ".news-item__date",
			Keywords:        []string{"جنگ", "ایران", "حمله", "پهپاد", "اسرائیل", "آمریکا"},
		},
		{
			Name:            "Mehr News",
			URL:             "https://www.mehrnews.com",
			ArticleSelector: ".news-box",
			TitleSelector:   ".title",
			SummarySelector: ".intro",
			LinkSelector:    "a[href]",
			DateSelector:    ".pub-time",
			Keywords:        []string{"جنگ", "ایران", "حمله", "پهپاد", "اسرائیل", "آمریکا"},
		},
	})
	if err != nil {
		panic(err)
	}
	return sites
}

func env(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func lookupEnv(key string) (string, bool) {
	return syscallEnv(key)
}
