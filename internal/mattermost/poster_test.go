package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kave08/news/internal/model"
)

func TestFormatMessageUnsupportedOnly(t *testing.T) {
	t.Parallel()

	formatted := FormatMessage(model.RelayMessage{
		ChatLabel:        "Bale Team",
		SenderLabel:      "Ali",
		UnsupportedKinds: []string{"photo", "photo", "document"},
	})

	if !strings.Contains(formatted, "Unsupported Bale content omitted: photo, document") {
		t.Fatalf("unexpected formatted message: %s", formatted)
	}
}

func TestWebhookPosterPost(t *testing.T) {
	t.Parallel()

	var payload struct {
		Text string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	poster := NewWebhookPoster(server.URL, server.Client())
	if err := poster.Check(context.Background()); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	_, err := poster.Post(context.Background(), model.RelayMessage{
		ChatLabel:   "Bale Team",
		SenderLabel: "Ali",
		Text:        "hello",
	})
	if err != nil {
		t.Fatalf("Post returned error: %v", err)
	}
	if !strings.Contains(payload.Text, "hello") {
		t.Fatalf("unexpected payload text: %s", payload.Text)
	}
}

func TestWebhookPosterPostNews(t *testing.T) {
	t.Parallel()

	var payload struct {
		Text string `json:"text"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	poster := NewWebhookPoster(server.URL, server.Client())
	if _, err := poster.PostNews(context.Background(), model.NewsArticle{
		SiteName:       "BBC Persian",
		Title:          "Headline",
		Summary:        "Summary",
		URL:            "https://example.com/article",
		PublishedAtRaw: "today",
	}); err != nil {
		t.Fatalf("PostNews returned error: %v", err)
	}

	if !strings.Contains(payload.Text, "**News**") || !strings.Contains(payload.Text, "Headline") {
		t.Fatalf("unexpected payload text: %s", payload.Text)
	}
}

func TestAPIPosterCheckAndPost(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("unexpected auth header: %s", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "user"})
		case "/api/v4/posts":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode post payload: %v", err)
			}
			if payload["channel_id"] != "channel-id" {
				t.Fatalf("unexpected channel id: %+v", payload["channel_id"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "post-id"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	poster := NewAPIPoster(server.URL, "token", "channel-id", client)

	if err := poster.Check(context.Background()); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}

	result, err := poster.Post(context.Background(), model.RelayMessage{
		UpdateID:      10,
		BaleMessageID: 99,
		ChatID:        -1001,
		ChatLabel:     "Bale Team",
		SenderID:      42,
		SenderLabel:   "Ali",
		Text:          "hello",
		OccurredAt:    time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("Post returned error: %v", err)
	}
	if result.RemoteID != "post-id" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAPIPosterPostNews(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/posts":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode post payload: %v", err)
			}
			props, _ := payload["props"].(map[string]any)
			if props["source"] != "news" {
				t.Fatalf("unexpected props: %+v", props)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "post-id"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	poster := NewAPIPoster(server.URL, "token", "channel-id", server.Client())
	result, err := poster.PostNews(context.Background(), model.NewsArticle{
		ArticleKey:     "article-1",
		SiteName:       "BBC Persian",
		Title:          "Headline",
		URL:            "https://example.com/article",
		PublishedAtRaw: "today",
	})
	if err != nil {
		t.Fatalf("PostNews returned error: %v", err)
	}
	if result.RemoteID != "post-id" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
