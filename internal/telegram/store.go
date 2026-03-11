package telegram

import (
	"context"
	"time"

	"github.com/kave08/news/internal/model"
)

// Store is what the Telegram service needs for persistence.
type Store interface {
	UpsertTelegramMessage(ctx context.Context, channelID, messageID int64, channelLabel, senderLabel, text string, occurredAt time.Time) (model.TelegramRecord, error)
	GetTelegramMessage(ctx context.Context, channelID, messageID int64) (model.TelegramRecord, error)
	MarkTelegramSent(ctx context.Context, channelID, messageID int64, remoteID string) error
	MarkTelegramFailed(ctx context.Context, channelID, messageID int64, errMsg string) error
	MarkTelegramSkipped(ctx context.Context, channelID, messageID int64, reason string) error
}
