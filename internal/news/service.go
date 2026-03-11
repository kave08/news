package news

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/kave08/news/internal/model"
)

type Poster interface {
	Check(ctx context.Context) error
	PostNews(ctx context.Context, article model.NewsArticle) (model.DeliveryResult, error)
}

type Store interface {
	Init(ctx context.Context) error
	UpsertDiscoveredNews(ctx context.Context, article model.NewsArticle) (model.NewsRecord, error)
	MarkNewsSkipped(ctx context.Context, articleKey string, reason string) error
	MarkNewsDeliveryFailure(ctx context.Context, articleKey string, errMessage string) error
	MarkNewsDeliverySuccess(ctx context.Context, articleKey string, result model.DeliveryResult) error
}

type Config struct {
	Interval            time.Duration
	MaxArticlesPerCycle int
	RequestTimeout      time.Duration
	FetchDelayMin       time.Duration
	FetchDelayMax       time.Duration
	UserAgent           string
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	RetryMaxAttempts    int
	MaxArticleAge       time.Duration
	Sites               []SiteConfig
}

type SiteConfig struct {
	Name            string
	URL             string
	ArticleSelector string
	TitleSelector   string
	SummarySelector string
	LinkSelector    string
	DateSelector    string
	Keywords        []string
}

type Service struct {
	poster     Poster
	store      Store
	logger     *slog.Logger
	cfg        Config
	httpClient *http.Client
	rng        *rand.Rand
}

func NewService(poster Poster, store Store, logger *slog.Logger, cfg Config, httpClient *http.Client) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Minute
	}
	if cfg.MaxArticlesPerCycle <= 0 {
		cfg.MaxArticlesPerCycle = 5
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = time.Second
	}
	if cfg.RetryMaxDelay <= 0 {
		cfg.RetryMaxDelay = 10 * time.Second
	}
	if cfg.RetryMaxAttempts <= 0 {
		cfg.RetryMaxAttempts = 3
	}
	if cfg.MaxArticleAge <= 0 {
		cfg.MaxArticleAge = 24 * time.Hour
	}
	if strings.TrimSpace(cfg.UserAgent) == "" {
		cfg.UserAgent = "NewsBot/1.0"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.RequestTimeout}
	}

	return &Service{
		poster:     poster,
		store:      store,
		logger:     logger,
		cfg:        cfg,
		httpClient: httpClient,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.store.Init(ctx); err != nil {
		return fmt.Errorf("initialize store: %w", err)
	}
	if err := s.poster.Check(ctx); err != nil {
		return fmt.Errorf("Mattermost preflight failed: %w", err)
	}

	if err := s.runCycle(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.runCycle(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Service) runCycle(ctx context.Context) error {
	remaining := s.cfg.MaxArticlesPerCycle
	for index, site := range s.cfg.Sites {
		if remaining <= 0 {
			return nil
		}
		if index > 0 {
			if err := s.sleepBetweenFetches(ctx); err != nil {
				return err
			}
		}

		articles, err := s.fetchSite(ctx, site)
		if err != nil {
			s.logger.Error("fetch news site failed", "site", site.Name, "error", err)
			continue
		}

		for _, article := range articles {
			if remaining <= 0 {
				return nil
			}

			posted, err := s.processArticle(ctx, article)
			if err != nil {
				return err
			}
			if posted {
				remaining--
			}
		}
	}

	return nil
}

func (s *Service) processArticle(ctx context.Context, article model.NewsArticle) (bool, error) {
	record, err := s.store.UpsertDiscoveredNews(ctx, article)
	if err != nil {
		return false, fmt.Errorf("persist discovered article %s: %w", article.ArticleKey, err)
	}
	if record.Status.IsTerminal() {
		return false, nil
	}

	if !article.PublishedAt.IsZero() && article.PublishedAt.Before(time.Now().Add(-s.cfg.MaxArticleAge)) {
		if err := s.store.MarkNewsSkipped(ctx, article.ArticleKey, "article older than max age"); err != nil {
			return false, err
		}
		return false, nil
	}

	result, err := s.deliverWithRetry(ctx, article)
	if err != nil {
		storeErr := s.store.MarkNewsDeliveryFailure(ctx, article.ArticleKey, err.Error())
		if storeErr != nil {
			return false, fmt.Errorf("mark news delivery failure: %w (original error: %v)", storeErr, err)
		}
		return false, nil
	}

	if err := s.store.MarkNewsDeliverySuccess(ctx, article.ArticleKey, result); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) deliverWithRetry(ctx context.Context, article model.NewsArticle) (model.DeliveryResult, error) {
	var lastErr error
	delay := s.cfg.RetryBaseDelay

	for attempt := 1; attempt <= s.cfg.RetryMaxAttempts; attempt++ {
		result, err := s.poster.PostNews(ctx, article)
		if err == nil {
			return result, nil
		}

		lastErr = err
		if !isRetryable(err) || attempt == s.cfg.RetryMaxAttempts {
			break
		}

		s.logger.Warn("retrying news delivery", "article_key", article.ArticleKey, "attempt", attempt, "error", err)
		if err := sleepContext(ctx, delay); err != nil {
			return model.DeliveryResult{}, err
		}
		delay = minDuration(delay*2, s.cfg.RetryMaxDelay)
	}

	return model.DeliveryResult{}, lastErr
}

func (s *Service) fetchSite(ctx context.Context, site SiteConfig) ([]model.NewsArticle, error) {
	var lastErr error
	delay := s.cfg.RetryBaseDelay

	for attempt := 1; attempt <= s.cfg.RetryMaxAttempts; attempt++ {
		articles, err := s.fetchSiteOnce(ctx, site)
		if err == nil {
			return articles, nil
		}

		lastErr = err
		if !isRetryable(err) || attempt == s.cfg.RetryMaxAttempts {
			break
		}

		s.logger.Warn("retrying news fetch", "site", site.Name, "attempt", attempt, "error", err)
		if err := sleepContext(ctx, delay); err != nil {
			return nil, err
		}
		delay = minDuration(delay*2, s.cfg.RetryMaxDelay)
	}

	return nil, lastErr
}

func (s *Service) fetchSiteOnce(ctx context.Context, site SiteConfig) ([]model.NewsArticle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(site.URL), nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", site.Name, err)
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if isRetryable(err) {
			return nil, temporaryError{error: fmt.Errorf("execute request for %s: %w", site.Name, err)}
		}
		return nil, fmt.Errorf("execute request for %s: %w", site.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &HTTPStatusError{
			URL:        site.URL,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse %s HTML: %w", site.Name, err)
	}

	return extractArticles(site, doc), nil
}

func extractArticles(site SiteConfig, doc *goquery.Document) []model.NewsArticle {
	articles := make([]model.NewsArticle, 0)
	baseURL, _ := url.Parse(site.URL)

	doc.Find(site.ArticleSelector).Each(func(_ int, selection *goquery.Selection) {
		title := strings.TrimSpace(selection.Find(site.TitleSelector).First().Text())
		if title == "" {
			return
		}

		summary := ""
		if selector := strings.TrimSpace(site.SummarySelector); selector != "" {
			summary = strings.TrimSpace(selection.Find(selector).First().Text())
		}

		link := strings.TrimSpace(selection.Find(site.LinkSelector).First().AttrOr("href", ""))
		if link == "" {
			return
		}

		articleURL := resolveURL(baseURL, link)
		if articleURL == "" {
			return
		}

		publishedRaw := extractPublishedRaw(selection, site.DateSelector)
		publishedAt := parsePublishedAt(publishedRaw)

		if !matchesKeyword(title, summary, site.Keywords) {
			return
		}

		article := model.NewsArticle{
			ArticleKey:     articleKey(site.Name, title, articleURL),
			SiteName:       site.Name,
			SiteURL:        site.URL,
			Title:          title,
			Summary:        summary,
			URL:            articleURL,
			PublishedAt:    publishedAt,
			PublishedAtRaw: strings.TrimSpace(publishedRaw),
		}
		articles = append(articles, article)
	})

	return articles
}

func extractPublishedRaw(selection *goquery.Selection, selector string) string {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return ""
	}

	dateSelection := selection.Find(selector).First()
	if dateSelection.Length() == 0 {
		return ""
	}
	for _, attr := range []string{"datetime", "content", "title"} {
		if value := strings.TrimSpace(dateSelection.AttrOr(attr, "")); value != "" {
			return value
		}
	}
	return strings.TrimSpace(dateSelection.Text())
}

func matchesKeyword(title string, summary string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}

	content := strings.ToLower(strings.TrimSpace(title + " " + summary))
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

func articleKey(siteName string, title string, articleURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(siteName) + "\n" + strings.TrimSpace(title) + "\n" + strings.TrimSpace(articleURL)))
	return hex.EncodeToString(sum[:])
}

func resolveURL(baseURL *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if baseURL == nil {
		return parsed.String()
	}
	return baseURL.ResolveReference(parsed).String()
}

func parsePublishedAt(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		time.RFC1123Z,
		time.RFC1123,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func (s *Service) sleepBetweenFetches(ctx context.Context) error {
	delay := s.cfg.FetchDelayMin
	if s.cfg.FetchDelayMax > s.cfg.FetchDelayMin {
		jitter := s.cfg.FetchDelayMax - s.cfg.FetchDelayMin
		delay += time.Duration(s.rng.Int63n(int64(jitter) + 1))
	}
	if delay <= 0 {
		return nil
	}
	return sleepContext(ctx, delay)
}

type HTTPStatusError struct {
	URL        string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("GET %s failed with status %d: %s", e.URL, e.StatusCode, strings.TrimSpace(e.Body))
}

func (e *HTTPStatusError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

type temporaryError struct {
	error
}

func (e temporaryError) Temporary() bool {
	return true
}

func isRetryable(err error) bool {
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
