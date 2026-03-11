package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/kave08/news/internal/model"
)

var ErrNotFound = errors.New("message record not found")

const stateKeyLastConfirmedUpdateID = "last_confirmed_update_id"

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if err := ensureParentDir(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) Init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS relay_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			update_id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,
			sender_id INTEGER NOT NULL,
			sender_label TEXT NOT NULL,
			chat_label TEXT NOT NULL,
			text TEXT NOT NULL,
			unsupported_kinds TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			mattermost_remote_id TEXT NOT NULL DEFAULT '',
			occurred_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(status)`,
		`CREATE TABLE IF NOT EXISTS news_articles (
			article_key TEXT PRIMARY KEY,
			site_name TEXT NOT NULL,
			site_url TEXT NOT NULL,
			title TEXT NOT NULL,
			summary TEXT NOT NULL,
			article_url TEXT NOT NULL,
			published_at TEXT NOT NULL,
			published_at_raw TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			mattermost_remote_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_news_articles_status ON news_articles(status)`,
		`CREATE TABLE IF NOT EXISTS telegram_messages (
			channel_id    INTEGER NOT NULL,
			message_id    INTEGER NOT NULL,
			channel_label TEXT NOT NULL DEFAULT '',
			sender_label  TEXT NOT NULL DEFAULT '',
			text          TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'received',
			attempts      INTEGER NOT NULL DEFAULT 0,
			error_log     TEXT NOT NULL DEFAULT '',
			remote_id     TEXT NOT NULL DEFAULT '',
			occurred_at   DATETIME NOT NULL,
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (channel_id, message_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_messages_status ON telegram_messages(status)`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("initialize sqlite schema: %w", err)
		}
	}

	return nil
}

func (s *SQLiteStore) GetLastConfirmedUpdateID(ctx context.Context) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM relay_state WHERE key = ?`, stateKeyLastConfirmedUpdateID)

	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("query last confirmed update id: %w", err)
	}

	var updateID int64
	if _, err := fmt.Sscan(raw, &updateID); err != nil {
		return 0, fmt.Errorf("parse last confirmed update id: %w", err)
	}
	return updateID, nil
}

func (s *SQLiteStore) SetLastConfirmedUpdateID(ctx context.Context, updateID int64) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO relay_state(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		stateKeyLastConfirmedUpdateID,
		fmt.Sprintf("%d", updateID),
	)
	if err != nil {
		return fmt.Errorf("store last confirmed update id: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpsertReceived(ctx context.Context, msg model.RelayMessage) (model.MessageRecord, error) {
	record, err := s.GetByUpdateID(ctx, msg.UpdateID)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return model.MessageRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO messages (
			update_id, chat_id, message_id, sender_id, sender_label, chat_label,
			text, unsupported_kinds, status, attempts, last_error, mattermost_remote_id,
			occurred_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.UpdateID,
		msg.ChatID,
		msg.BaleMessageID,
		msg.SenderID,
		msg.SenderLabel,
		msg.ChatLabel,
		msg.NormalizedText(),
		strings.Join(msg.NormalizedUnsupportedKinds(), ","),
		model.StatusReceived,
		0,
		"",
		"",
		msg.OccurredAt.UTC().Format(time.RFC3339Nano),
		now,
		now,
	)
	if err != nil {
		return model.MessageRecord{}, fmt.Errorf("insert received message %d: %w", msg.UpdateID, err)
	}

	return s.GetByUpdateID(ctx, msg.UpdateID)
}

func (s *SQLiteStore) MarkIgnored(ctx context.Context, updateID int64, reason string) error {
	return s.updateStatus(ctx, updateID, model.StatusIgnored, false, "", reason)
}

func (s *SQLiteStore) MarkSkipped(ctx context.Context, updateID int64, reason string) error {
	return s.updateStatus(ctx, updateID, model.StatusSkipped, false, "", reason)
}

func (s *SQLiteStore) MarkDeliveryFailure(ctx context.Context, updateID int64, errMessage string) error {
	return s.updateStatus(ctx, updateID, model.StatusFailed, true, "", errMessage)
}

func (s *SQLiteStore) MarkDeliverySuccess(ctx context.Context, updateID int64, result model.DeliveryResult) error {
	return s.updateStatus(ctx, updateID, model.StatusSent, true, result.RemoteID, "")
}

func (s *SQLiteStore) GetByUpdateID(ctx context.Context, updateID int64) (model.MessageRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT
			update_id, chat_id, message_id, sender_id, sender_label, chat_label,
			text, unsupported_kinds, status, attempts, last_error, mattermost_remote_id,
			occurred_at, created_at, updated_at
		FROM messages
		WHERE update_id = ?`,
		updateID,
	)

	var record model.MessageRecord
	var unsupportedKinds string
	var occurredAt string
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&record.UpdateID,
		&record.ChatID,
		&record.BaleMessageID,
		&record.SenderID,
		&record.SenderLabel,
		&record.ChatLabel,
		&record.Text,
		&unsupportedKinds,
		&record.Status,
		&record.Attempts,
		&record.LastError,
		&record.MattermostRemoteID,
		&occurredAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.MessageRecord{}, ErrNotFound
		}
		return model.MessageRecord{}, fmt.Errorf("query message %d: %w", updateID, err)
	}

	record.UnsupportedKinds = splitKinds(unsupportedKinds)
	record.OccurredAt = parseTimestamp(occurredAt)
	record.CreatedAt = parseTimestamp(createdAt)
	record.UpdatedAt = parseTimestamp(updatedAt)
	return record, nil
}

func (s *SQLiteStore) UpsertDiscoveredNews(ctx context.Context, article model.NewsArticle) (model.NewsRecord, error) {
	record, err := s.GetNewsByArticleKey(ctx, article.ArticleKey)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return model.NewsRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	publishedAt := ""
	if !article.PublishedAt.IsZero() {
		publishedAt = article.PublishedAt.UTC().Format(time.RFC3339Nano)
	}

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO news_articles (
			article_key, site_name, site_url, title, summary, article_url, published_at,
			published_at_raw, status, attempts, last_error, mattermost_remote_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		article.ArticleKey,
		strings.TrimSpace(article.SiteName),
		strings.TrimSpace(article.SiteURL),
		article.NormalizedTitle(),
		article.NormalizedSummary(),
		article.NormalizedURL(),
		publishedAt,
		strings.TrimSpace(article.PublishedAtRaw),
		model.NewsStatusDiscovered,
		0,
		"",
		"",
		now,
		now,
	)
	if err != nil {
		return model.NewsRecord{}, fmt.Errorf("insert discovered article %s: %w", article.ArticleKey, err)
	}

	return s.GetNewsByArticleKey(ctx, article.ArticleKey)
}

func (s *SQLiteStore) GetNewsByArticleKey(ctx context.Context, articleKey string) (model.NewsRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT
			article_key, site_name, site_url, title, summary, article_url, published_at,
			published_at_raw, status, attempts, last_error, mattermost_remote_id, created_at, updated_at
		FROM news_articles
		WHERE article_key = ?`,
		articleKey,
	)

	var record model.NewsRecord
	var publishedAt string
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&record.ArticleKey,
		&record.SiteName,
		&record.SiteURL,
		&record.Title,
		&record.Summary,
		&record.URL,
		&publishedAt,
		&record.PublishedAtRaw,
		&record.Status,
		&record.Attempts,
		&record.LastError,
		&record.MattermostRemoteID,
		&createdAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.NewsRecord{}, ErrNotFound
		}
		return model.NewsRecord{}, fmt.Errorf("query news article %s: %w", articleKey, err)
	}

	record.PublishedAt = parseTimestamp(publishedAt)
	record.CreatedAt = parseTimestamp(createdAt)
	record.UpdatedAt = parseTimestamp(updatedAt)
	return record, nil
}

func (s *SQLiteStore) MarkNewsSkipped(ctx context.Context, articleKey string, reason string) error {
	return s.updateNewsStatus(ctx, articleKey, model.NewsStatusSkipped, false, "", reason)
}

func (s *SQLiteStore) MarkNewsDeliveryFailure(ctx context.Context, articleKey string, errMessage string) error {
	return s.updateNewsStatus(ctx, articleKey, model.NewsStatusFailed, true, "", errMessage)
}

func (s *SQLiteStore) MarkNewsDeliverySuccess(ctx context.Context, articleKey string, result model.DeliveryResult) error {
	return s.updateNewsStatus(ctx, articleKey, model.NewsStatusSent, true, result.RemoteID, "")
}

func (s *SQLiteStore) updateStatus(
	ctx context.Context,
	updateID int64,
	status model.RelayStatus,
	incrementAttempts bool,
	remoteID string,
	lastError string,
) error {
	statement := `UPDATE messages
		SET status = ?, last_error = ?, mattermost_remote_id = ?, updated_at = ?`
	args := []any{
		status,
		lastError,
		remoteID,
		time.Now().UTC().Format(time.RFC3339Nano),
	}

	if incrementAttempts {
		statement += `, attempts = attempts + 1`
	}

	statement += ` WHERE update_id = ?`
	args = append(args, updateID)

	result, err := s.db.ExecContext(ctx, statement, args...)
	if err != nil {
		return fmt.Errorf("update message %d status to %s: %w", updateID, status, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rows affected for message %d: %w", updateID, err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *SQLiteStore) UpsertTelegramMessage(ctx context.Context, channelID, messageID int64, channelLabel, senderLabel, text string, occurredAt time.Time) (model.TelegramRecord, error) {
	record, err := s.GetTelegramMessage(ctx, channelID, messageID)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return model.TelegramRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO telegram_messages (
			channel_id, message_id, channel_label, sender_label, text,
			status, attempts, error_log, remote_id, occurred_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		channelID, messageID, channelLabel, senderLabel, text,
		model.StatusReceived, 0, "", "",
		occurredAt.UTC().Format(time.RFC3339Nano),
		now, now,
	)
	if err != nil {
		return model.TelegramRecord{}, fmt.Errorf("insert telegram message (%d,%d): %w", channelID, messageID, err)
	}

	return s.GetTelegramMessage(ctx, channelID, messageID)
}

func (s *SQLiteStore) GetTelegramMessage(ctx context.Context, channelID, messageID int64) (model.TelegramRecord, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT channel_id, message_id, channel_label, sender_label, text,
			status, attempts, error_log, remote_id, occurred_at, created_at, updated_at
		FROM telegram_messages
		WHERE channel_id = ? AND message_id = ?`,
		channelID, messageID,
	)

	var record model.TelegramRecord
	var occurredAt, createdAt, updatedAt string
	if err := row.Scan(
		&record.ChannelID, &record.MessageID,
		&record.ChannelLabel, &record.SenderLabel, &record.Text,
		&record.Status, &record.Attempts, &record.ErrorLog, &record.RemoteID,
		&occurredAt, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TelegramRecord{}, ErrNotFound
		}
		return model.TelegramRecord{}, fmt.Errorf("query telegram message (%d,%d): %w", channelID, messageID, err)
	}

	record.OccurredAt = parseTimestamp(occurredAt)
	record.CreatedAt = parseTimestamp(createdAt)
	record.UpdatedAt = parseTimestamp(updatedAt)
	return record, nil
}

func (s *SQLiteStore) MarkTelegramSent(ctx context.Context, channelID, messageID int64, remoteID string) error {
	return s.updateTelegramStatus(ctx, channelID, messageID, model.StatusSent, true, remoteID, "")
}

func (s *SQLiteStore) MarkTelegramFailed(ctx context.Context, channelID, messageID int64, errMsg string) error {
	return s.updateTelegramStatus(ctx, channelID, messageID, model.StatusFailed, true, "", errMsg)
}

func (s *SQLiteStore) MarkTelegramSkipped(ctx context.Context, channelID, messageID int64, reason string) error {
	return s.updateTelegramStatus(ctx, channelID, messageID, model.StatusSkipped, false, "", reason)
}

func (s *SQLiteStore) updateTelegramStatus(
	ctx context.Context,
	channelID, messageID int64,
	status model.RelayStatus,
	incrementAttempts bool,
	remoteID string,
	errorLog string,
) error {
	stmt := `UPDATE telegram_messages SET status = ?, error_log = ?, remote_id = ?, updated_at = ?`
	args := []any{status, errorLog, remoteID, time.Now().UTC().Format(time.RFC3339Nano)}

	if incrementAttempts {
		stmt += `, attempts = attempts + 1`
	}
	stmt += ` WHERE channel_id = ? AND message_id = ?`
	args = append(args, channelID, messageID)

	result, err := s.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("update telegram message (%d,%d) status to %s: %w", channelID, messageID, status, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rows affected for telegram message (%d,%d): %w", channelID, messageID, err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func ensureParentDir(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sqlite directory %s: %w", dir, err)
	}
	return nil
}

func (s *SQLiteStore) updateNewsStatus(
	ctx context.Context,
	articleKey string,
	status model.NewsStatus,
	incrementAttempts bool,
	remoteID string,
	lastError string,
) error {
	statement := `UPDATE news_articles
		SET status = ?, last_error = ?, mattermost_remote_id = ?, updated_at = ?`
	args := []any{
		status,
		lastError,
		remoteID,
		time.Now().UTC().Format(time.RFC3339Nano),
	}

	if incrementAttempts {
		statement += `, attempts = attempts + 1`
	}

	statement += ` WHERE article_key = ?`
	args = append(args, articleKey)

	result, err := s.db.ExecContext(ctx, statement, args...)
	if err != nil {
		return fmt.Errorf("update news article %s status to %s: %w", articleKey, status, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rows affected for news article %s: %w", articleKey, err)
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}

	return nil
}

func sqliteDSN(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "./data/relay.db"
	}
	if strings.HasPrefix(path, "file:") {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		return path + separator + "_busy_timeout=5000&_journal_mode=WAL"
	}
	return fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL", path)
}

func splitKinds(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func parseTimestamp(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
