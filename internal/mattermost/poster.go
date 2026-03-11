package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/kave08/news/internal/model"
)

const (
	ModeWebhook = "webhook"
	ModeAPI     = "api"
)

type Poster interface {
	Check(ctx context.Context) error
	Post(ctx context.Context, msg model.RelayMessage) (model.DeliveryResult, error)
	PostNews(ctx context.Context, article model.NewsArticle) (model.DeliveryResult, error)
}

type HTTPStatusError struct {
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s %s failed with status %d: %s", e.Method, e.URL, e.StatusCode, strings.TrimSpace(e.Body))
}

func (e *HTTPStatusError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

type WebhookPoster struct {
	webhookURL string
	httpClient *http.Client
}

type APIPoster struct {
	baseURL    string
	token      string
	channelID  string
	httpClient *http.Client
}

func NewWebhookPoster(webhookURL string, httpClient *http.Client) *WebhookPoster {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &WebhookPoster{
		webhookURL: strings.TrimSpace(webhookURL),
		httpClient: httpClient,
	}
}

func NewAPIPoster(baseURL, token, channelID string, httpClient *http.Client) *APIPoster {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &APIPoster{
		baseURL:    normalizeAPIBaseURL(baseURL),
		token:      strings.TrimSpace(token),
		channelID:  strings.TrimSpace(channelID),
		httpClient: httpClient,
	}
}

func (p *WebhookPoster) Check(context.Context) error {
	parsed, err := url.ParseRequestURI(p.webhookURL)
	if err != nil {
		return fmt.Errorf("parse Mattermost webhook URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("Mattermost webhook URL must include scheme and host")
	}
	return nil
}

func (p *WebhookPoster) Post(ctx context.Context, msg model.RelayMessage) (model.DeliveryResult, error) {
	return p.postText(ctx, FormatMessage(msg))
}

func (p *WebhookPoster) PostNews(ctx context.Context, article model.NewsArticle) (model.DeliveryResult, error) {
	return p.postText(ctx, FormatNewsMessage(article))
}

func (p *WebhookPoster) postText(ctx context.Context, text string) (model.DeliveryResult, error) {
	payload, err := json.Marshal(map[string]string{
		"text": text,
	})
	if err != nil {
		return model.DeliveryResult{}, fmt.Errorf("marshal Mattermost webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return model.DeliveryResult{}, fmt.Errorf("build Mattermost webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return model.DeliveryResult{}, fmt.Errorf("execute Mattermost webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return model.DeliveryResult{}, &HTTPStatusError{
			Method:     http.MethodPost,
			URL:        p.webhookURL,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	return model.DeliveryResult{Mode: ModeWebhook}, nil
}

func (p *APIPoster) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/users/me", nil)
	if err != nil {
		return fmt.Errorf("build Mattermost preflight request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute Mattermost preflight request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &HTTPStatusError{
			Method:     http.MethodGet,
			URL:        p.baseURL + "/users/me",
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	return nil
}

func (p *APIPoster) Post(ctx context.Context, msg model.RelayMessage) (model.DeliveryResult, error) {
	return p.postMessage(ctx, FormatMessage(msg), map[string]any{
		"source":               "bale",
		"bale_update_id":       msg.UpdateID,
		"bale_message_id":      msg.BaleMessageID,
		"bale_chat_id":         msg.ChatID,
		"bale_chat_label":      msg.ChatLabel,
		"bale_sender_id":       msg.SenderID,
		"bale_sender_label":    msg.SenderLabel,
		"bale_unsupported":     msg.NormalizedUnsupportedKinds(),
		"bale_occurred_at_utc": msg.OccurredAt.UTC().Format(timeFormat),
	})
}

func (p *APIPoster) PostNews(ctx context.Context, article model.NewsArticle) (model.DeliveryResult, error) {
	props := map[string]any{
		"source":    "news",
		"news_site": article.SiteName,
		"news_url":  article.NormalizedURL(),
	}
	if article.ArticleKey != "" {
		props["news_key"] = article.ArticleKey
	}
	if publishedLabel := article.PublishedLabel(); publishedLabel != "" {
		props["news_published"] = publishedLabel
	}
	if !article.PublishedAt.IsZero() {
		props["news_published_at_utc"] = article.PublishedAt.UTC().Format(timeFormat)
	}
	return p.postMessage(ctx, FormatNewsMessage(article), props)
}

func (p *APIPoster) postMessage(ctx context.Context, message string, props map[string]any) (model.DeliveryResult, error) {
	payload := map[string]any{
		"channel_id": p.channelID,
		"message":    message,
		"props":      props,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return model.DeliveryResult{}, fmt.Errorf("marshal Mattermost post payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/posts", bytes.NewReader(raw))
	if err != nil {
		return model.DeliveryResult{}, fmt.Errorf("build Mattermost post request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if isTemporaryNetworkError(err) {
			return model.DeliveryResult{}, temporaryError{error: fmt.Errorf("execute Mattermost post request: %w", err)}
		}
		return model.DeliveryResult{}, fmt.Errorf("execute Mattermost post request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return model.DeliveryResult{}, &HTTPStatusError{
			Method:     http.MethodPost,
			URL:        p.baseURL + "/posts",
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil {
		return model.DeliveryResult{}, fmt.Errorf("decode Mattermost post response: %w", err)
	}

	return model.DeliveryResult{
		Mode:     ModeAPI,
		RemoteID: created.ID,
	}, nil
}

func FormatMessage(msg model.RelayMessage) string {
	sourceName := "Bale"
	if msg.Source == model.SourceTelegram {
		sourceName = "Telegram"
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("**%s** · %s · %s", sourceName, fallbackLabel(msg.ChatLabel, msg.ChatID), fallbackSender(msg.SenderLabel, msg.SenderID)))

	if text := msg.NormalizedText(); text != "" {
		lines = append(lines, text)
	}

	if unsupported := msg.NormalizedUnsupportedKinds(); len(unsupported) > 0 {
		lines = append(lines, fmt.Sprintf("_Unsupported %s content omitted: %s_", sourceName, strings.Join(unsupported, ", ")))
	}

	return strings.Join(lines, "\n")
}

func FormatNewsMessage(article model.NewsArticle) string {
	lines := []string{fmt.Sprintf("**News** · %s", strings.TrimSpace(article.SiteName))}

	if title := article.NormalizedTitle(); title != "" {
		lines = append(lines, fmt.Sprintf("**%s**", title))
	}
	if summary := article.NormalizedSummary(); summary != "" {
		lines = append(lines, summary)
	}
	if articleURL := article.NormalizedURL(); articleURL != "" {
		lines = append(lines, articleURL)
	}
	if published := article.PublishedLabel(); published != "" {
		lines = append(lines, fmt.Sprintf("_Published: %s_", published))
	}

	return strings.Join(lines, "\n")
}

type temporaryError struct {
	error
}

func (e temporaryError) Temporary() bool {
	return true
}

func normalizeAPIBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(raw, "/api/v4") {
		return raw
	}
	return raw + "/api/v4"
}

func fallbackLabel(label string, id int64) string {
	label = strings.TrimSpace(label)
	if label != "" {
		return label
	}
	return fmt.Sprintf("%d", id)
}

func fallbackSender(label string, id int64) string {
	label = strings.TrimSpace(label)
	if label != "" {
		return label
	}
	if id != 0 {
		return fmt.Sprintf("%d", id)
	}
	return "unknown sender"
}

func isTemporaryNetworkError(err error) bool {
	var netErr net.Error
	return errorsAs(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

const timeFormat = "2006-01-02T15:04:05Z07:00"
