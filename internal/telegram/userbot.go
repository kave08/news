package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/updates"

	"github.com/kave08/news/internal/model"
)

// Config holds configuration for the Telegram userbot service.
type Config struct {
	APIID            int
	APIHash          string
	Phone            string
	Channels         []string
	SessionPath      string
	AllowedHashtags  []string
	StripMentions    []string
	StripPhrases     []string
	RetryBaseDelay   time.Duration
	RetryMaxDelay    time.Duration
	RetryMaxAttempts int
}

// Service monitors Telegram channels and posts messages to Mattermost.
type Service struct {
	cfg    Config
	poster Poster
	store  Store
	logger *slog.Logger
}

// NewService creates a new Telegram userbot service.
func NewService(cfg Config, poster Poster, store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(ioDiscard{}, nil))
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
	cfg.AllowedHashtags = normalizeList(cfg.AllowedHashtags)
	cfg.StripMentions = normalizeMentions(cfg.StripMentions)
	cfg.StripPhrases = normalizeList(cfg.StripPhrases)

	return &Service{cfg: cfg, poster: poster, store: store, logger: logger}
}

// Run connects to Telegram via MTProto and streams channel updates until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	dispatcher := tg.NewUpdateDispatcher()

	gaps := updates.New(updates.Config{
		Handler: dispatcher,
	})

	client := gotdtelegram.NewClient(s.cfg.APIID, s.cfg.APIHash, gotdtelegram.Options{
		UpdateHandler:  gaps,
		SessionStorage: &session.FileStorage{Path: s.cfg.SessionPath},
	})

	return client.Run(ctx, func(ctx context.Context) error {
		// Authenticate if necessary.
		flow := auth.NewFlow(
			auth.Constant(s.cfg.Phone, "", auth.CodeAuthenticatorFunc(func(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
				fmt.Fprintf(os.Stdout, "Telegram login code sent to %s. Enter code: ", s.cfg.Phone)
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					return strings.TrimSpace(scanner.Text()), nil
				}
				if err := scanner.Err(); err != nil {
					return "", fmt.Errorf("read login code: %w", err)
				}
				return "", fmt.Errorf("no login code provided")
			})),
			auth.SendCodeOptions{},
		)
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("telegram auth: %w", err)
		}

		// Build channelID → label map by resolving usernames.
		api := client.API()
		channelLabels := make(map[int64]string, len(s.cfg.Channels))
		var mu sync.Mutex

		for _, username := range s.cfg.Channels {
			username = strings.TrimPrefix(strings.TrimSpace(username), "@")
			if username == "" {
				continue
			}
			resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
			if err != nil {
				s.logger.Warn("resolve telegram channel failed", "username", username, "error", err)
				continue
			}
			for _, chat := range resolved.Chats {
				ch, ok := chat.(*tg.Channel)
				if !ok {
					continue
				}
				label := ch.Title
				if label == "" {
					label = "@" + username
				}
				mu.Lock()
				channelLabels[ch.ID] = label
				mu.Unlock()
				s.logger.Info("tracking telegram channel", "username", username, "channel_id", ch.ID, "label", label)
			}
		}

		// Register handler for new channel messages.
		dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, update *tg.UpdateNewChannelMessage) error {
			msg, ok := update.Message.(*tg.Message)
			if !ok {
				return nil
			}
			peer, ok := msg.PeerID.(*tg.PeerChannel)
			if !ok {
				return nil
			}

			mu.Lock()
			channelLabel := channelLabels[peer.ChannelID]
			mu.Unlock()

			// Only handle channels we're tracking.
			if channelLabel == "" {
				return nil
			}

			return s.handleMessage(ctx, peer.ChannelID, int64(msg.ID), channelLabel, msg.Message, time.Unix(int64(msg.Date), 0).UTC())
		})

		// Get self user ID for gaps manager.
		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("get self: %w", err)
		}

		return gaps.Run(ctx, api, self.ID, updates.AuthOptions{
			IsBot: false,
		})
	})
}

func (s *Service) handleMessage(ctx context.Context, channelID, messageID int64, channelLabel, text string, occurredAt time.Time) error {
	text = s.sanitizeText(text)

	if text == "" {
		_, err := s.store.UpsertTelegramMessage(ctx, channelID, messageID, channelLabel, "", text, occurredAt)
		if err != nil {
			return err
		}
		return s.store.MarkTelegramSkipped(ctx, channelID, messageID, "empty message")
	}

	if !s.matchesAllowedHashtag(text) {
		_, err := s.store.UpsertTelegramMessage(ctx, channelID, messageID, channelLabel, "", text, occurredAt)
		if err != nil {
			return err
		}
		return s.store.MarkTelegramSkipped(ctx, channelID, messageID, "missing allowed hashtag")
	}

	record, err := s.store.UpsertTelegramMessage(ctx, channelID, messageID, channelLabel, "", text, occurredAt)
	if err != nil {
		return fmt.Errorf("persist telegram message (%d,%d): %w", channelID, messageID, err)
	}
	if record.Status.IsTerminal() {
		return nil
	}

	relayMsg := model.RelayMessage{
		Source:      model.SourceTelegram,
		ChatID:      channelID,
		ChatLabel:   channelLabel,
		SenderLabel: channelLabel,
		Text:        text,
		OccurredAt:  occurredAt,
	}

	result, err := s.deliverWithRetry(ctx, relayMsg)
	if err != nil {
		storeErr := s.store.MarkTelegramFailed(ctx, channelID, messageID, err.Error())
		if storeErr != nil {
			return fmt.Errorf("mark telegram delivery failure: %w (original: %v)", storeErr, err)
		}
		return err
	}

	return s.store.MarkTelegramSent(ctx, channelID, messageID, result.RemoteID)
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
		s.logger.Warn("retrying telegram delivery", "channel_id", msg.ChatID, "attempt", attempt, "error", err)
		if err := sleepContext(ctx, delay); err != nil {
			return model.DeliveryResult{}, err
		}
		delay = minDuration(delay*2, s.cfg.RetryMaxDelay)
	}
	return model.DeliveryResult{}, lastErr
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
		line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
		if line != "" {
			sanitized = append(sanitized, line)
		}
	}
	return strings.TrimSpace(strings.Join(sanitized, "\n"))
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

func isRetryable(err error) bool {
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func normalizeList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		normalized = append(normalized, v)
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
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !strings.HasPrefix(v, "@") {
			v = "@" + v
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		normalized = append(normalized, v)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
