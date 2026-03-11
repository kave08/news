package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kave08/news/internal/bale"
	"github.com/kave08/news/internal/config"
	"github.com/kave08/news/internal/mattermost"
	"github.com/kave08/news/internal/news"
	"github.com/kave08/news/internal/relay"
	"github.com/kave08/news/internal/store"
	"github.com/kave08/news/internal/telegram"
)

const (
	baleHTTPTimeoutMargin = 45 * time.Second
	baleHTTPMinTimeout    = time.Minute
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	baleClient := bale.NewClient(cfg.Bale.BaseURL, cfg.Bale.Token, newBaleHTTPClient(cfg.Bale.PollTimeout))

	var poster mattermost.Poster
	switch cfg.Mattermost.Mode {
	case config.MattermostModeWebhook:
		poster = mattermost.NewWebhookPoster(cfg.Mattermost.WebhookURL, &http.Client{Timeout: 15 * time.Second})
	case config.MattermostModeAPI:
		poster = mattermost.NewAPIPoster(
			cfg.Mattermost.BaseURL,
			cfg.Mattermost.BotToken,
			cfg.Mattermost.ChannelID,
			&http.Client{Timeout: 15 * time.Second},
		)
	default:
		return config.ErrInvalidMattermostMode
	}

	sqliteStore, err := store.NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer sqliteStore.Close()

	service := relay.NewService(
		baleClient,
		poster,
		sqliteStore,
		logger,
		relay.Config{
			AllowedChatIDs:     cfg.Bale.AllowedChatIDs,
			AllowedHashtags:    append([]string(nil), cfg.Bale.AllowedHashtags...),
			StripMentions:      append([]string(nil), cfg.Bale.StripMentions...),
			StripPhrases:       append([]string(nil), cfg.Bale.StripPhrases...),
			PollTimeout:        cfg.Bale.PollTimeout,
			RetryBaseDelay:     time.Second,
			RetryMaxDelay:      10 * time.Second,
			RetryMaxAttempts:   3,
			AllowedUpdateKinds: []string{"message", "channel_post"},
		},
	)

	runners := map[string]serviceRunner{
		"relay": service,
	}
	if cfg.News.Enabled {
		provider, err := newNewsProvider(cfg.News, logger)
		if err != nil {
			return err
		}

		runners["news"] = news.NewService(
			poster,
			sqliteStore,
			logger,
			provider,
			news.Config{
				Interval:            cfg.News.Interval,
				MaxArticlesPerCycle: cfg.News.MaxArticlesPerCycle,
				RetryBaseDelay:      time.Second,
				RetryMaxDelay:       10 * time.Second,
				RetryMaxAttempts:    3,
				MaxArticleAge:       24 * time.Hour,
			},
		)
	}

	if cfg.Telegram.Enabled {
		tgService := telegram.NewService(
			telegram.Config{
				APIID:            cfg.Telegram.APIID,
				APIHash:          cfg.Telegram.APIHash,
				Phone:            cfg.Telegram.Phone,
				Channels:         append([]string(nil), cfg.Telegram.Channels...),
				SessionPath:      cfg.Telegram.SessionPath,
				AllowedHashtags:  append([]string(nil), cfg.Telegram.AllowedHashtags...),
				StripMentions:    append([]string(nil), cfg.Telegram.StripMentions...),
				StripPhrases:     append([]string(nil), cfg.Telegram.StripPhrases...),
				RetryBaseDelay:   cfg.Telegram.RetryBaseDelay,
				RetryMaxDelay:    cfg.Telegram.RetryMaxDelay,
				RetryMaxAttempts: cfg.Telegram.RetryMaxAttempts,
			},
			poster,
			sqliteStore,
			logger,
		)
		runners["telegram"] = tgService
	}

	return runServices(ctx, runners)
}

type serviceRunner interface {
	Run(context.Context) error
}

func runServices(ctx context.Context, runners map[string]serviceRunner) error {
	if len(runners) == 1 {
		for _, runner := range runners {
			return runner.Run(ctx)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(runners))
	var wg sync.WaitGroup

	for name, runner := range runners {
		wg.Add(1)
		go func(name string, runner serviceRunner) {
			defer wg.Done()
			err := runner.Run(ctx)
			if err == nil || errors.Is(err, context.Canceled) {
				return
			}

			select {
			case errCh <- fmt.Errorf("%s service: %w", name, err):
				cancel()
			default:
			}
		}(name, runner)
	}

	wg.Wait()
	close(errCh)

	if err, ok := <-errCh; ok {
		return err
	}
	return ctx.Err()
}

func newNewsProvider(cfg config.NewsConfig, logger *slog.Logger) (news.Provider, error) {
	switch cfg.Provider {
	case "", config.NewsProviderLegacyHTML:
		return news.NewLegacyHTMLProvider(
			logger,
			news.LegacyHTMLProviderConfig{
				RequestTimeout:   cfg.RequestTimeout,
				FetchDelayMin:    cfg.FetchDelayMin,
				FetchDelayMax:    cfg.FetchDelayMax,
				UserAgent:        cfg.UserAgent,
				RetryBaseDelay:   time.Second,
				RetryMaxDelay:    10 * time.Second,
				RetryMaxAttempts: 3,
				Sites:            toNewsSites(cfg.Sites),
			},
			&http.Client{Timeout: cfg.RequestTimeout},
		), nil
	case config.NewsProviderBBCApproved:
		return news.NewBBCApprovedProvider(), nil
	default:
		return nil, fmt.Errorf("unsupported news provider %q", cfg.Provider)
	}
}

func toNewsSites(sites []config.NewsSiteConfig) []news.SiteConfig {
	newsSites := make([]news.SiteConfig, 0, len(sites))
	for _, site := range sites {
		newsSites = append(newsSites, news.SiteConfig{
			Name:            site.Name,
			URL:             site.URL,
			ArticleSelector: site.ArticleSelector,
			TitleSelector:   site.TitleSelector,
			SummarySelector: site.SummarySelector,
			LinkSelector:    site.LinkSelector,
			DateSelector:    site.DateSelector,
			Keywords:        append([]string(nil), site.Keywords...),
		})
	}
	return newsSites
}

func newBaleHTTPClient(pollTimeout time.Duration) *http.Client {
	timeout := pollTimeout + baleHTTPTimeoutMargin
	if timeout < baleHTTPMinTimeout {
		timeout = baleHTTPMinTimeout
	}
	return &http.Client{Timeout: timeout}
}
