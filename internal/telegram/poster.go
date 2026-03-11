package telegram

import (
	"context"

	"github.com/kave08/news/internal/model"
)

// Poster is what the Telegram service needs to deliver messages.
type Poster interface {
	Post(ctx context.Context, msg model.RelayMessage) (model.DeliveryResult, error)
}
