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
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	baleClient := bale.NewClient(cfg.Bale.BaseURL, cfg.Bale.Token, &http.Client{
		Timeout: cfg.Bale.PollTimeout + 10*time.Second,
	})

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
			PollTimeout:        cfg.Bale.PollTimeout,
			RetryBaseDelay:     time.Second,
			RetryMaxDelay:      10 * time.Second,
			RetryMaxAttempts:   3,
			AllowedUpdateKinds: []string{"message"},
		},
	)

	runners := map[string]serviceRunner{
		"relay": service,
	}
	if cfg.News.Enabled {
		runners["news"] = news.NewService(
			poster,
			sqliteStore,
			logger,
			news.Config{
				Interval:            cfg.News.Interval,
				MaxArticlesPerCycle: cfg.News.MaxArticlesPerCycle,
				RequestTimeout:      cfg.News.RequestTimeout,
				FetchDelayMin:       cfg.News.FetchDelayMin,
				FetchDelayMax:       cfg.News.FetchDelayMax,
				UserAgent:           cfg.News.UserAgent,
				RetryBaseDelay:      time.Second,
				RetryMaxDelay:       10 * time.Second,
				RetryMaxAttempts:    3,
				MaxArticleAge:       24 * time.Hour,
				Sites:               toNewsSites(cfg.News.Sites),
			},
			&http.Client{Timeout: cfg.News.RequestTimeout},
		)
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
