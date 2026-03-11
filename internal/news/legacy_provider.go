package news

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/kave08/news/internal/model"
)

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

type LegacyHTMLProviderConfig struct {
	RequestTimeout   time.Duration
	FetchDelayMin    time.Duration
	FetchDelayMax    time.Duration
	UserAgent        string
	RetryBaseDelay   time.Duration
	RetryMaxDelay    time.Duration
	RetryMaxAttempts int
	Sites            []SiteConfig
}

type LegacyHTMLProvider struct {
	logger     *slog.Logger
	cfg        LegacyHTMLProviderConfig
	httpClient *http.Client
	rng        *rand.Rand
}

func NewLegacyHTMLProvider(logger *slog.Logger, cfg LegacyHTMLProviderConfig, httpClient *http.Client) *LegacyHTMLProvider {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.FetchDelayMin < 0 {
		cfg.FetchDelayMin = 0
	}
	if cfg.FetchDelayMax < cfg.FetchDelayMin {
		cfg.FetchDelayMax = cfg.FetchDelayMin
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
	if strings.TrimSpace(cfg.UserAgent) == "" {
		cfg.UserAgent = "NewsBot/1.0"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.RequestTimeout}
	}

	return &LegacyHTMLProvider{
		logger:     logger,
		cfg:        cfg,
		httpClient: httpClient,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (p *LegacyHTMLProvider) FetchLatest(ctx context.Context, limit int) ([]model.NewsArticle, error) {
	if limit <= 0 {
		return nil, nil
	}

	articles := make([]model.NewsArticle, 0, limit)
	for index, site := range p.cfg.Sites {
		if len(articles) >= limit {
			break
		}
		if index > 0 {
			if err := p.sleepBetweenFetches(ctx); err != nil {
				return nil, err
			}
		}

		siteArticles, err := p.fetchSite(ctx, site)
		if err != nil {
			p.logger.Error("fetch news site failed", "site", site.Name, "error", err)
			continue
		}

		remaining := limit - len(articles)
		if len(siteArticles) > remaining {
			siteArticles = siteArticles[:remaining]
		}
		articles = append(articles, siteArticles...)
	}

	return articles, nil
}

func (p *LegacyHTMLProvider) fetchSite(ctx context.Context, site SiteConfig) ([]model.NewsArticle, error) {
	var lastErr error
	delay := p.cfg.RetryBaseDelay

	for attempt := 1; attempt <= p.cfg.RetryMaxAttempts; attempt++ {
		articles, err := p.fetchSiteOnce(ctx, site)
		if err == nil {
			return articles, nil
		}

		lastErr = err
		if !isRetryable(err) || attempt == p.cfg.RetryMaxAttempts {
			break
		}

		p.logger.Warn("retrying news fetch", "site", site.Name, "attempt", attempt, "error", err)
		if err := sleepContext(ctx, delay); err != nil {
			return nil, err
		}
		delay = minDuration(delay*2, p.cfg.RetryMaxDelay)
	}

	return nil, lastErr
}

func (p *LegacyHTMLProvider) fetchSiteOnce(ctx context.Context, site SiteConfig) ([]model.NewsArticle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(site.URL), nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", site.Name, err)
	}
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := p.httpClient.Do(req)
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

func (p *LegacyHTMLProvider) sleepBetweenFetches(ctx context.Context) error {
	delay := p.cfg.FetchDelayMin
	if p.cfg.FetchDelayMax > p.cfg.FetchDelayMin {
		jitter := p.cfg.FetchDelayMax - p.cfg.FetchDelayMin
		delay += time.Duration(p.rng.Int63n(int64(jitter) + 1))
	}
	if delay <= 0 {
		return nil
	}
	return sleepContext(ctx, delay)
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

		articles = append(articles, model.NewsArticle{
			ArticleKey:     articleKey(site.Name, title, articleURL),
			SiteName:       site.Name,
			SiteURL:        site.URL,
			Title:          title,
			Summary:        summary,
			URL:            articleURL,
			PublishedAt:    publishedAt,
			PublishedAtRaw: strings.TrimSpace(publishedRaw),
		})
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
