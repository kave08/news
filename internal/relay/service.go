package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/kave08/news/internal/bale"
	"github.com/kave08/news/internal/model"
)

type UpdateSource interface {
	GetMe(ctx context.Context) (bale.User, error)
	GetUpdates(ctx context.Context, req bale.GetUpdatesRequest) ([]bale.Update, error)
}

type Poster interface {
	Check(ctx context.Context) error
	Post(ctx context.Context, msg model.RelayMessage) (model.DeliveryResult, error)
}

type Store interface {
	Init(ctx context.Context) error
	GetLastConfirmedUpdateID(ctx context.Context) (int64, error)
	SetLastConfirmedUpdateID(ctx context.Context, updateID int64) error
	UpsertReceived(ctx context.Context, msg model.RelayMessage) (model.MessageRecord, error)
	MarkIgnored(ctx context.Context, updateID int64, reason string) error
	MarkSkipped(ctx context.Context, updateID int64, reason string) error
	MarkDeliveryFailure(ctx context.Context, updateID int64, errMessage string) error
	MarkDeliverySuccess(ctx context.Context, updateID int64, result model.DeliveryResult) error
}

type Config struct {
	AllowedChatIDs     map[int64]struct{}
	AllowedHashtags    []string
	StripMentions      []string
	StripPhrases       []string
	PollTimeout        time.Duration
	RetryBaseDelay     time.Duration
	RetryMaxDelay      time.Duration
	RetryMaxAttempts   int
	AllowedUpdateKinds []string
}

type Service struct {
	source UpdateSource
	poster Poster
	store  Store
	logger *slog.Logger
	cfg    Config
}

func NewService(source UpdateSource, poster Poster, store Store, logger *slog.Logger, cfg Config) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 20 * time.Second
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
	if len(cfg.AllowedUpdateKinds) == 0 {
		cfg.AllowedUpdateKinds = []string{"message", "channel_post"}
	}
	cfg.AllowedHashtags = normalizeList(cfg.AllowedHashtags)
	cfg.StripMentions = normalizeMentions(cfg.StripMentions)
	cfg.StripPhrases = normalizeList(cfg.StripPhrases)

	return &Service{
		source: source,
		poster: poster,
		store:  store,
		logger: logger,
		cfg:    cfg,
	}
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.store.Init(ctx); err != nil {
		return fmt.Errorf("initialize store: %w", err)
	}

	if _, err := s.source.GetMe(ctx); err != nil {
		return fmt.Errorf("Bale preflight failed: %w", err)
	}
	if err := s.poster.Check(ctx); err != nil {
		return fmt.Errorf("Mattermost preflight failed: %w", err)
	}

	lastConfirmed, err := s.store.GetLastConfirmedUpdateID(ctx)
	if err != nil {
		return fmt.Errorf("load last confirmed update id: %w", err)
	}
	nextOffset := lastConfirmed + 1
	backoffDelay := s.cfg.RetryBaseDelay

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		updates, err := s.source.GetUpdates(ctx, bale.GetUpdatesRequest{
			Offset:         nextOffset,
			Limit:          100,
			Timeout:        int(s.cfg.PollTimeout / time.Second),
			AllowedUpdates: s.cfg.AllowedUpdateKinds,
		})
		if err != nil {
			s.logger.Error("poll Bale updates failed", "error", err, "next_offset", nextOffset)
			if err := sleepContext(ctx, backoffDelay); err != nil {
				return err
			}
			backoffDelay = minDuration(backoffDelay*2, s.cfg.RetryMaxDelay)
			continue
		}

		backoffDelay = s.cfg.RetryBaseDelay
		if len(updates) == 0 {
			continue
		}

		sort.Slice(updates, func(i, j int) bool {
			return updates[i].UpdateID < updates[j].UpdateID
		})

		for _, update := range updates {
			if err := s.ProcessUpdate(ctx, update); err != nil {
				s.logger.Error("process Bale update failed", "error", err, "update_id", update.UpdateID)
				if err := sleepContext(ctx, backoffDelay); err != nil {
					return err
				}
				backoffDelay = minDuration(backoffDelay*2, s.cfg.RetryMaxDelay)
				break
			}
			backoffDelay = s.cfg.RetryBaseDelay
			nextOffset = update.UpdateID + 1
		}
	}
}

func (s *Service) ProcessUpdate(ctx context.Context, update bale.Update) error {
	msg := relayMessageFromUpdate(update)
	record, err := s.store.UpsertReceived(ctx, msg)
	if err != nil {
		return fmt.Errorf("persist received update %d: %w", update.UpdateID, err)
	}

	if record.Status.IsTerminal() {
		return s.confirmCursor(ctx, update.UpdateID)
	}

	if updateMessage(update) == nil {
		if err := s.store.MarkSkipped(ctx, update.UpdateID, "unsupported update type"); err != nil {
			return err
		}
		return s.confirmCursor(ctx, update.UpdateID)
	}

	if !s.isAllowedChat(msg.ChatID) {
		if err := s.store.MarkIgnored(ctx, update.UpdateID, "chat not allowlisted"); err != nil {
			return err
		}
		return s.confirmCursor(ctx, update.UpdateID)
	}

	msg.Text = s.sanitizeText(msg.Text)

	if msg.NormalizedText() == "" && len(msg.NormalizedUnsupportedKinds()) == 0 {
		if err := s.store.MarkSkipped(ctx, update.UpdateID, "empty message"); err != nil {
			return err
		}
		return s.confirmCursor(ctx, update.UpdateID)
	}

	if !s.matchesAllowedHashtag(msg.NormalizedText()) {
		if err := s.store.MarkSkipped(ctx, update.UpdateID, "missing allowed hashtag"); err != nil {
			return err
		}
		return s.confirmCursor(ctx, update.UpdateID)
	}

	result, err := s.deliverWithRetry(ctx, msg)
	if err != nil {
		storeErr := s.store.MarkDeliveryFailure(ctx, update.UpdateID, err.Error())
		if storeErr != nil {
			return fmt.Errorf("mark delivery failure: %w (original error: %v)", storeErr, err)
		}
		return err
	}

	if err := s.store.MarkDeliverySuccess(ctx, update.UpdateID, result); err != nil {
		return err
	}
	return s.confirmCursor(ctx, update.UpdateID)
}

func (s *Service) deliverWithRetry(ctx context.Context, msg model.RelayMessage) (model.DeliveryResult, error) {
	var lastErr error
	delay := s.cfg.RetryBaseDelay

	for attempt := 1; attempt <= s.cfg.RetryMaxAttempts; attempt++ {
		result, err := s.poster.Post(ctx, msg)
		if err == nil {
			return result, nil
		}

		lastErr = err
		if !isRetryable(err) || attempt == s.cfg.RetryMaxAttempts {
			break
		}

		s.logger.Warn("retrying Mattermost delivery", "update_id", msg.UpdateID, "attempt", attempt, "error", err)
		if err := sleepContext(ctx, delay); err != nil {
			return model.DeliveryResult{}, err
		}
		delay = minDuration(delay*2, s.cfg.RetryMaxDelay)
	}

	return model.DeliveryResult{}, lastErr
}

func (s *Service) confirmCursor(ctx context.Context, updateID int64) error {
	if err := s.store.SetLastConfirmedUpdateID(ctx, updateID); err != nil {
		return fmt.Errorf("store confirmed update id %d: %w", updateID, err)
	}
	return nil
}

func (s *Service) isAllowedChat(chatID int64) bool {
	_, allowed := s.cfg.AllowedChatIDs[chatID]
	return allowed
}

func (s *Service) matchesAllowedHashtag(text string) bool {
	if len(s.cfg.AllowedHashtags) == 0 {
		return true
	}
	for _, hashtag := range s.cfg.AllowedHashtags {
		if strings.Contains(text, hashtag) {
			return true
		}
	}
	return false
}

func (s *Service) sanitizeText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	for _, mention := range s.cfg.StripMentions {
		text = strings.ReplaceAll(text, mention, "")
	}
	for _, phrase := range s.cfg.StripPhrases {
		text = strings.ReplaceAll(text, phrase, "")
	}

	lines := strings.Split(text, "\n")
	sanitized := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sanitized = append(sanitized, line)
	}

	return strings.TrimSpace(strings.Join(sanitized, "\n"))
}

func relayMessageFromUpdate(update bale.Update) model.RelayMessage {
	msg := updateMessage(update)
	if msg == nil {
		return model.RelayMessage{
			UpdateID:         update.UpdateID,
			ChatLabel:        "unknown chat",
			SenderLabel:      "unknown sender",
			UnsupportedKinds: []string{"non_message_update"},
			OccurredAt:       time.Unix(0, 0).UTC(),
		}
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}

	senderID, senderLabel := senderDetails(msg)
	return model.RelayMessage{
		Source:           model.SourceBale,
		UpdateID:         update.UpdateID,
		BaleMessageID:    msg.MessageID,
		ChatID:           msg.Chat.ID,
		ChatLabel:        msg.Chat.DisplayName(),
		SenderID:         senderID,
		SenderLabel:      senderLabel,
		Text:             text,
		UnsupportedKinds: unsupportedKinds(msg),
		OccurredAt:       time.Unix(msg.Date, 0).UTC(),
	}
}

func updateMessage(update bale.Update) *bale.Message {
	if update.Message != nil {
		return update.Message
	}
	if update.ChannelPost != nil {
		return update.ChannelPost
	}
	return nil
}

func normalizeList(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeMentions(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.HasPrefix(value, "@") {
			value = "@" + value
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func senderDetails(msg *bale.Message) (int64, string) {
	if msg.From != nil {
		return msg.From.ID, msg.From.DisplayName()
	}
	if msg.SenderChat != nil {
		return msg.SenderChat.ID, msg.SenderChat.DisplayName()
	}
	return 0, "unknown sender"
}

func unsupportedKinds(msg *bale.Message) []string {
	var kinds []string
	if len(msg.Photo) > 0 {
		kinds = append(kinds, "photo")
	}
	if msg.Video != nil {
		kinds = append(kinds, "video")
	}
	if msg.Document != nil {
		kinds = append(kinds, "document")
	}
	if msg.Audio != nil {
		kinds = append(kinds, "audio")
	}
	if msg.Voice != nil {
		kinds = append(kinds, "voice")
	}
	if msg.Sticker != nil {
		kinds = append(kinds, "sticker")
	}
	if msg.Animation != nil {
		kinds = append(kinds, "animation")
	}
	if msg.VideoNote != nil {
		kinds = append(kinds, "video_note")
	}
	if msg.Contact != nil {
		kinds = append(kinds, "contact")
	}
	if msg.Location != nil {
		kinds = append(kinds, "location")
	}
	return kinds
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
