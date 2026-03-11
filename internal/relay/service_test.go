package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kave08/news/internal/bale"
	"github.com/kave08/news/internal/mattermost"
	"github.com/kave08/news/internal/model"
	"github.com/kave08/news/internal/store"
)

func TestProcessUpdateAllowedText(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestService(t)
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 10,
		Message: &bale.Message{
			MessageID: 100,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "hello",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	record, err := sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusSent {
		t.Fatalf("unexpected record status: %s", record.Status)
	}
	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(poster.posts))
	}
}

func TestProcessUpdateAllowedChannelPost(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestServiceWithConfig(t, Config{
		AllowedChatIDs:     map[int64]struct{}{5875733190: {}},
		AllowedHashtags:    []string{"#پیام_دریافتی"},
		PollTimeout:        time.Second,
		RetryBaseDelay:     time.Millisecond,
		RetryMaxDelay:      5 * time.Millisecond,
		RetryMaxAttempts:   3,
		AllowedUpdateKinds: []string{"message", "channel_post"},
	})
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 11,
		ChannelPost: &bale.Message{
			MessageID: 110,
			SenderChat: &bale.Chat{
				ID:    5875733190,
				Title: "Alarm Channel",
			},
			Chat: bale.Chat{
				ID:    5875733190,
				Title: "Alarm Channel",
				Type:  "channel",
			},
			Date: 1_700_000_000,
			Text: "#پیام_دریافتی متن تست",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	record, err := sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusSent {
		t.Fatalf("unexpected record status: %s", record.Status)
	}
	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(poster.posts))
	}
	if poster.posts[0].ChatID != 5875733190 {
		t.Fatalf("unexpected chat id: %d", poster.posts[0].ChatID)
	}
}

func TestProcessUpdateSkipsMessageWithoutAllowedHashtag(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestServiceWithConfig(t, Config{
		AllowedChatIDs:     map[int64]struct{}{-1001: {}},
		AllowedHashtags:    []string{"#پیام_دریافتی"},
		PollTimeout:        time.Second,
		RetryBaseDelay:     time.Millisecond,
		RetryMaxDelay:      5 * time.Millisecond,
		RetryMaxAttempts:   3,
		AllowedUpdateKinds: []string{"message"},
	})
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 15,
		Message: &bale.Message{
			MessageID: 150,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "hello",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	record, err := sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusSkipped {
		t.Fatalf("unexpected record status: %s", record.Status)
	}
	if len(poster.posts) != 0 {
		t.Fatalf("expected no posts, got %d", len(poster.posts))
	}
}

func TestProcessUpdateStripsConfiguredMentions(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestServiceWithConfig(t, Config{
		AllowedChatIDs:     map[int64]struct{}{-1001: {}},
		AllowedHashtags:    []string{"#پیام_دریافتی"},
		StripMentions:      []string{"@tehran_alarm"},
		PollTimeout:        time.Second,
		RetryBaseDelay:     time.Millisecond,
		RetryMaxDelay:      5 * time.Millisecond,
		RetryMaxAttempts:   3,
		AllowedUpdateKinds: []string{"message"},
	})
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 16,
		Message: &bale.Message{
			MessageID: 160,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "@tehran_alarm #پیام_دریافتی متن تست",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(poster.posts))
	}
	if strings.Contains(poster.posts[0].Text, "@tehran_alarm") {
		t.Fatalf("expected mention to be stripped, got %q", poster.posts[0].Text)
	}
	if !strings.Contains(poster.posts[0].Text, "#پیام_دریافتی") {
		t.Fatalf("expected hashtag to remain, got %q", poster.posts[0].Text)
	}
}

func TestProcessUpdateStripsConfiguredPhrases(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestServiceWithConfig(t, Config{
		AllowedChatIDs:     map[int64]struct{}{-1001: {}},
		AllowedHashtags:    []string{"#پیام_دریافتی"},
		StripPhrases:       []string{"پاینده باد ایران 🇮🇷"},
		PollTimeout:        time.Second,
		RetryBaseDelay:     time.Millisecond,
		RetryMaxDelay:      5 * time.Millisecond,
		RetryMaxAttempts:   3,
		AllowedUpdateKinds: []string{"message"},
	})
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 17,
		Message: &bale.Message{
			MessageID: 170,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "#پیام_دریافتی پاینده باد ایران 🇮🇷 متن تست",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(poster.posts))
	}
	if strings.Contains(poster.posts[0].Text, "پاینده باد ایران 🇮🇷") {
		t.Fatalf("expected phrase to be stripped, got %q", poster.posts[0].Text)
	}
	if !strings.Contains(poster.posts[0].Text, "متن تست") {
		t.Fatalf("expected remaining text, got %q", poster.posts[0].Text)
	}
}

func TestProcessUpdateDisallowedChat(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestService(t)
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 20,
		Message: &bale.Message{
			MessageID: 200,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -2002, Title: "Other Team"},
			Date:      1_700_000_000,
			Text:      "hello",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	record, err := sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusIgnored {
		t.Fatalf("unexpected record status: %s", record.Status)
	}
	if len(poster.posts) != 0 {
		t.Fatalf("expected no posts, got %d", len(poster.posts))
	}
}

func TestProcessUpdateUnsupportedMediaPlaceholder(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestService(t)
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 30,
		Message: &bale.Message{
			MessageID: 300,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Photo:     []bale.PhotoSize{{FileID: "file-1"}},
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(poster.posts))
	}
	formatted := mattermost.FormatMessage(poster.posts[0])
	if !strings.Contains(formatted, "Unsupported Bale content omitted: photo") {
		t.Fatalf("unexpected formatted placeholder: %s", formatted)
	}
}

func TestProcessUpdateRetriesTransientFailure(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestService(t)
	defer sqliteStore.Close()

	poster.errs = []error{
		tempError{error: errors.New("retry one")},
		tempError{error: errors.New("retry two")},
		nil,
	}

	update := bale.Update{
		UpdateID: 40,
		Message: &bale.Message{
			MessageID: 400,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "hello",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate returned error: %v", err)
	}

	record, err := sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Attempts != 1 {
		t.Fatalf("store should record a single successful delivery attempt, got %d", record.Attempts)
	}
	if poster.calls != 3 {
		t.Fatalf("expected 3 poster calls, got %d", poster.calls)
	}
}

func TestProcessUpdateRestartAfterFailure(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "relay.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := sqliteStore.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	firstPoster := &fakePoster{errs: []error{errors.New("hard failure")}}
	firstService := newServiceForTest(t, sqliteStore, firstPoster)

	update := bale.Update{
		UpdateID: 50,
		Message: &bale.Message{
			MessageID: 500,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "hello",
		},
	}

	if err := firstService.ProcessUpdate(context.Background(), update); err == nil {
		t.Fatal("expected failure on first processing attempt")
	}

	record, err := sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusFailed {
		t.Fatalf("unexpected failed status: %s", record.Status)
	}

	secondPoster := &fakePoster{}
	secondService := newServiceForTest(t, sqliteStore, secondPoster)
	if err := secondService.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("ProcessUpdate on restart returned error: %v", err)
	}

	record, err = sqliteStore.GetByUpdateID(context.Background(), update.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusSent {
		t.Fatalf("unexpected final status: %s", record.Status)
	}
	if record.Attempts != 2 {
		t.Fatalf("unexpected attempts after retry: %d", record.Attempts)
	}
}

func TestProcessUpdateDuplicateUpdateIDDoesNotRedeliver(t *testing.T) {
	t.Parallel()

	service, sqliteStore, poster := newTestService(t)
	defer sqliteStore.Close()

	update := bale.Update{
		UpdateID: 60,
		Message: &bale.Message{
			MessageID: 600,
			From:      &bale.User{ID: 42, FirstName: "Ali"},
			Chat:      bale.Chat{ID: -1001, Title: "Bale Team"},
			Date:      1_700_000_000,
			Text:      "hello",
		},
	}

	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("first ProcessUpdate returned error: %v", err)
	}
	if err := service.ProcessUpdate(context.Background(), update); err != nil {
		t.Fatalf("second ProcessUpdate returned error: %v", err)
	}
	if poster.calls != 1 {
		t.Fatalf("expected 1 poster call, got %d", poster.calls)
	}
}

type fakePoster struct {
	posts []model.RelayMessage
	errs  []error
	calls int
}

func (p *fakePoster) Check(context.Context) error {
	return nil
}

func (p *fakePoster) Post(_ context.Context, msg model.RelayMessage) (model.DeliveryResult, error) {
	p.calls++
	p.posts = append(p.posts, msg)

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

func newTestService(t *testing.T) (*Service, *store.SQLiteStore, *fakePoster) {
	t.Helper()

	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := sqliteStore.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	poster := &fakePoster{}
	return newServiceForTest(t, sqliteStore, poster), sqliteStore, poster
}

func newTestServiceWithConfig(t *testing.T, cfg Config) (*Service, *store.SQLiteStore, *fakePoster) {
	t.Helper()

	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := sqliteStore.Init(context.Background()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	poster := &fakePoster{}
	return NewService(
		nil,
		poster,
		sqliteStore,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg,
	), sqliteStore, poster
}

func newServiceForTest(t *testing.T, sqliteStore *store.SQLiteStore, poster *fakePoster) *Service {
	t.Helper()

	return NewService(
		nil,
		poster,
		sqliteStore,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{
			AllowedChatIDs:     map[int64]struct{}{-1001: {}},
			PollTimeout:        time.Second,
			RetryBaseDelay:     time.Millisecond,
			RetryMaxDelay:      5 * time.Millisecond,
			RetryMaxAttempts:   3,
			AllowedUpdateKinds: []string{"message"},
		},
	)
}
