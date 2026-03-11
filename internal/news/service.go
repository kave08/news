package news

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

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

type Provider interface {
	FetchLatest(ctx context.Context, limit int) ([]model.NewsArticle, error)
}

type Config struct {
	Interval            time.Duration
	MaxArticlesPerCycle int
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	RetryMaxAttempts    int
	MaxArticleAge       time.Duration
}

type Service struct {
	poster   Poster
	store    Store
	provider Provider
	logger   *slog.Logger
	cfg      Config
}

func NewService(poster Poster, store Store, logger *slog.Logger, provider Provider, cfg Config) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Minute
	}
	if cfg.MaxArticlesPerCycle <= 0 {
		cfg.MaxArticlesPerCycle = 5
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

	return &Service{
		poster:   poster,
		store:    store,
		provider: provider,
		logger:   logger,
		cfg:      cfg,
	}
}

func (s *Service) Run(ctx context.Context) error {
	if s.provider == nil {
		return errors.New("news provider is required")
	}
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
	articles, err := s.provider.FetchLatest(ctx, s.cfg.MaxArticlesPerCycle)
	if err != nil {
		return err
	}

	remaining := s.cfg.MaxArticlesPerCycle
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

type HTTPStatusError struct {
	URL        string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("GET %s failed with status %d: %s", e.URL, e.StatusCode, strings.TrimSpace(e.Body))
}

func (e *HTTPStatusError) Temporary() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
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

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
