package news

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kave08/news/internal/model"
	"github.com/kave08/news/internal/store"
)

func TestRunCyclePostsAndDedupesAcrossRestart(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `
			<html><body>
				<article class="item">
					<h2>Iran war update</h2>
					<p>Latest summary</p>
					<a href="/story-1">Read</a>
					<time datetime="2099-01-01T10:00:00Z"></time>
				</article>
			</body></html>
		`)
	}))
	defer server.Close()

	dbPath := filepath.Join(t.TempDir(), "news.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := sqliteStore.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	firstPoster := &fakePoster{}
	service := newTestService(sqliteStore, firstPoster, server.URL, 5)
	if err := service.runCycle(context.Background()); err != nil {
		t.Fatalf("runCycle returned error: %v", err)
	}
	if firstPoster.calls != 1 {
		t.Fatalf("expected 1 post, got %d", firstPoster.calls)
	}
	if err := sqliteStore.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	reopened, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	secondPoster := &fakePoster{}
	restarted := newTestService(reopened, secondPoster, server.URL, 5)
	if err := restarted.runCycle(context.Background()); err != nil {
		t.Fatalf("runCycle returned error: %v", err)
	}
	if secondPoster.calls != 0 {
		t.Fatalf("expected no repost after restart, got %d", secondPoster.calls)
	}
}

func TestRunCycleSkipsOldParsedArticle(t *testing.T) {
	t.Parallel()

	oldDate := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<article class="item"><h2>Iran update</h2><a href="/story-1">Read</a><time datetime="`+oldDate+`"></time></article>`)
	}))
	defer server.Close()

	sqliteStore := newTestStore(t)
	defer sqliteStore.Close()

	service := newTestService(sqliteStore, &fakePoster{}, server.URL, 5)
	if err := service.runCycle(context.Background()); err != nil {
		t.Fatalf("runCycle returned error: %v", err)
	}

	record, err := sqliteStore.GetNewsByArticleKey(context.Background(), articleKey("Test Site", "Iran update", server.URL+"/story-1"))
	if err != nil {
		t.Fatalf("GetNewsByArticleKey returned error: %v", err)
	}
	if record.Status != model.NewsStatusSkipped {
		t.Fatalf("unexpected status: %s", record.Status)
	}
}

func TestRunCycleHonorsMaxArticlesPerCycle(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `
			<article class="item"><h2>Iran update 1</h2><a href="/story-1">Read</a></article>
			<article class="item"><h2>Iran update 2</h2><a href="/story-2">Read</a></article>
		`)
	}))
	defer server.Close()

	sqliteStore := newTestStore(t)
	defer sqliteStore.Close()

	poster := &fakePoster{}
	service := newTestService(sqliteStore, poster, server.URL, 1)
	if err := service.runCycle(context.Background()); err != nil {
		t.Fatalf("runCycle returned error: %v", err)
	}
	if poster.calls != 1 {
		t.Fatalf("expected exactly 1 post, got %d", poster.calls)
	}
}

func TestRunCycleRetriesTemporaryPostFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<article class="item"><h2>Iran update</h2><a href="/story-1">Read</a></article>`)
	}))
	defer server.Close()

	sqliteStore := newTestStore(t)
	defer sqliteStore.Close()

	poster := &fakePoster{
		errs: []error{
			tempError{error: errors.New("retry one")},
			tempError{error: errors.New("retry two")},
			nil,
		},
	}
	service := newTestService(sqliteStore, poster, server.URL, 5)
	if err := service.runCycle(context.Background()); err != nil {
		t.Fatalf("runCycle returned error: %v", err)
	}
	if poster.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", poster.calls)
	}
}

type fakePoster struct {
	errs  []error
	calls int
}

func (p *fakePoster) Check(context.Context) error {
	return nil
}

func (p *fakePoster) PostNews(_ context.Context, _ model.NewsArticle) (model.DeliveryResult, error) {
	p.calls++
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return model.DeliveryResult{}, err
		}
	}
	return model.DeliveryResult{Mode: "test", RemoteID: "post-id"}, nil
}

type tempError struct {
	error
}

func (e tempError) Temporary() bool {
	return true
}

func newTestService(sqliteStore *store.SQLiteStore, poster *fakePoster, siteURL string, maxArticles int) *Service {
	return NewService(
		poster,
		sqliteStore,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{
			Interval:            time.Minute,
			MaxArticlesPerCycle: maxArticles,
			RequestTimeout:      time.Second,
			FetchDelayMin:       0,
			FetchDelayMax:       0,
			UserAgent:           "test-agent",
			RetryBaseDelay:      time.Millisecond,
			RetryMaxDelay:       5 * time.Millisecond,
			RetryMaxAttempts:    3,
			MaxArticleAge:       24 * time.Hour,
			Sites: []SiteConfig{{
				Name:            "Test Site",
				URL:             siteURL,
				ArticleSelector: ".item",
				TitleSelector:   "h2",
				LinkSelector:    "a[href]",
				DateSelector:    "time",
				Keywords:        []string{"iran"},
			}},
		},
		&http.Client{Timeout: time.Second},
	)
}

func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()

	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := sqliteStore.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	return sqliteStore
}
