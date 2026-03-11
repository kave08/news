package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kave08/news/internal/model"
)

func TestSQLiteStoreLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	lastConfirmed, err := store.GetLastConfirmedUpdateID(ctx)
	if err != nil {
		t.Fatalf("GetLastConfirmedUpdateID returned error: %v", err)
	}
	if lastConfirmed != 0 {
		t.Fatalf("unexpected initial last confirmed update id: %d", lastConfirmed)
	}

	msg := testMessage(10)
	record, err := store.UpsertReceived(ctx, msg)
	if err != nil {
		t.Fatalf("UpsertReceived returned error: %v", err)
	}
	if record.Status != model.StatusReceived {
		t.Fatalf("unexpected status: %s", record.Status)
	}

	if err := store.MarkDeliveryFailure(ctx, msg.UpdateID, "temporary failure"); err != nil {
		t.Fatalf("MarkDeliveryFailure returned error: %v", err)
	}
	if err := store.MarkDeliverySuccess(ctx, msg.UpdateID, model.DeliveryResult{RemoteID: "post-1"}); err != nil {
		t.Fatalf("MarkDeliverySuccess returned error: %v", err)
	}
	if err := store.SetLastConfirmedUpdateID(ctx, msg.UpdateID); err != nil {
		t.Fatalf("SetLastConfirmedUpdateID returned error: %v", err)
	}

	record, err = store.GetByUpdateID(ctx, msg.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusSent {
		t.Fatalf("unexpected final status: %s", record.Status)
	}
	if record.Attempts != 2 {
		t.Fatalf("unexpected attempts: %d", record.Attempts)
	}
}

func TestSQLiteStoreUpsertIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	msg := testMessage(20)
	first, err := store.UpsertReceived(ctx, msg)
	if err != nil {
		t.Fatalf("first UpsertReceived returned error: %v", err)
	}
	second, err := store.UpsertReceived(ctx, model.RelayMessage{
		UpdateID: 20,
		Text:     "changed",
	})
	if err != nil {
		t.Fatalf("second UpsertReceived returned error: %v", err)
	}

	if first.CreatedAt != second.CreatedAt {
		t.Fatalf("expected idempotent insert, got different timestamps")
	}
	if second.Text != msg.Text {
		t.Fatalf("expected original text to remain, got %q", second.Text)
	}
}

func TestSQLiteStorePersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "relay.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	msg := testMessage(30)
	if _, err := store.UpsertReceived(ctx, msg); err != nil {
		t.Fatalf("UpsertReceived returned error: %v", err)
	}
	if err := store.MarkSkipped(ctx, msg.UpdateID, "empty"); err != nil {
		t.Fatalf("MarkSkipped returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	reopened, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Init(ctx); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	record, err := reopened.GetByUpdateID(ctx, msg.UpdateID)
	if err != nil {
		t.Fatalf("GetByUpdateID returned error: %v", err)
	}
	if record.Status != model.StatusSkipped {
		t.Fatalf("unexpected persisted status: %s", record.Status)
	}
}

func TestSQLiteStoreNewsLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	article := model.NewsArticle{
		ArticleKey:     "article-1",
		SiteName:       "BBC Persian",
		SiteURL:        "https://example.com",
		Title:          "Headline",
		Summary:        "Summary",
		URL:            "https://example.com/article",
		PublishedAtRaw: "today",
	}
	record, err := store.UpsertDiscoveredNews(ctx, article)
	if err != nil {
		t.Fatalf("UpsertDiscoveredNews returned error: %v", err)
	}
	if record.Status != model.NewsStatusDiscovered {
		t.Fatalf("unexpected status: %s", record.Status)
	}

	if err := store.MarkNewsDeliveryFailure(ctx, article.ArticleKey, "temporary failure"); err != nil {
		t.Fatalf("MarkNewsDeliveryFailure returned error: %v", err)
	}
	if err := store.MarkNewsDeliverySuccess(ctx, article.ArticleKey, model.DeliveryResult{RemoteID: "post-1"}); err != nil {
		t.Fatalf("MarkNewsDeliverySuccess returned error: %v", err)
	}

	record, err = store.GetNewsByArticleKey(ctx, article.ArticleKey)
	if err != nil {
		t.Fatalf("GetNewsByArticleKey returned error: %v", err)
	}
	if record.Status != model.NewsStatusSent {
		t.Fatalf("unexpected final status: %s", record.Status)
	}
	if record.Attempts != 2 {
		t.Fatalf("unexpected attempts: %d", record.Attempts)
	}
}

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	return store
}

func testMessage(updateID int64) model.RelayMessage {
	return model.RelayMessage{
		UpdateID:         updateID,
		BaleMessageID:    updateID + 100,
		ChatID:           -1001,
		ChatLabel:        "Bale Team",
		SenderID:         42,
		SenderLabel:      "Ali",
		Text:             "hello",
		UnsupportedKinds: []string{"photo"},
		OccurredAt:       time.Unix(1_700_000_000, 0).UTC(),
	}
}
