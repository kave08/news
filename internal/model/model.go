package model

import (
	"strings"
	"time"
)

type RelayStatus string

const (
	StatusReceived RelayStatus = "received"
	StatusSent     RelayStatus = "sent"
	StatusIgnored  RelayStatus = "ignored"
	StatusSkipped  RelayStatus = "skipped"
	StatusFailed   RelayStatus = "failed"
)

func (s RelayStatus) IsTerminal() bool {
	return s == StatusSent || s == StatusIgnored || s == StatusSkipped
}

type RelayMessage struct {
	UpdateID         int64
	BaleMessageID    int64
	ChatID           int64
	ChatLabel        string
	SenderID         int64
	SenderLabel      string
	Text             string
	UnsupportedKinds []string
	OccurredAt       time.Time
}

func (m RelayMessage) NormalizedText() string {
	return strings.TrimSpace(m.Text)
}

func (m RelayMessage) NormalizedUnsupportedKinds() []string {
	if len(m.UnsupportedKinds) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(m.UnsupportedKinds))
	normalized := make([]string, 0, len(m.UnsupportedKinds))
	for _, kind := range m.UnsupportedKinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		normalized = append(normalized, kind)
	}

	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

type DeliveryResult struct {
	RemoteID string
	Mode     string
}

type NewsStatus string

const (
	NewsStatusDiscovered NewsStatus = "discovered"
	NewsStatusSent       NewsStatus = "sent"
	NewsStatusSkipped    NewsStatus = "skipped"
	NewsStatusFailed     NewsStatus = "failed"
)

func (s NewsStatus) IsTerminal() bool {
	return s == NewsStatusSent || s == NewsStatusSkipped
}

type NewsArticle struct {
	ArticleKey     string
	SiteName       string
	SiteURL        string
	Title          string
	Summary        string
	URL            string
	PublishedAt    time.Time
	PublishedAtRaw string
}

func (a NewsArticle) NormalizedTitle() string {
	return strings.TrimSpace(a.Title)
}

func (a NewsArticle) NormalizedSummary() string {
	return strings.TrimSpace(a.Summary)
}

func (a NewsArticle) NormalizedURL() string {
	return strings.TrimSpace(a.URL)
}

func (a NewsArticle) PublishedLabel() string {
	if raw := strings.TrimSpace(a.PublishedAtRaw); raw != "" {
		return raw
	}
	if !a.PublishedAt.IsZero() {
		return a.PublishedAt.UTC().Format(time.RFC3339)
	}
	return ""
}

type NewsRecord struct {
	NewsArticle
	Status             NewsStatus
	Attempts           int
	LastError          string
	MattermostRemoteID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type MessageRecord struct {
	RelayMessage
	Status             RelayStatus
	Attempts           int
	LastError          string
	MattermostRemoteID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
