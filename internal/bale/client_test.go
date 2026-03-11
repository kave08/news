package bale

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGetMe(t *testing.T) {
	t.Parallel()

	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"id":         1,
				"first_name": "Relay",
				"username":   "relaybot",
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret", server.Client())
	user, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe returned error: %v", err)
	}

	if receivedPath != "/botsecret/getMe" {
		t.Fatalf("unexpected request path: %s", receivedPath)
	}
	if user.Username != "relaybot" {
		t.Fatalf("unexpected user: %+v", user)
	}
}

func TestClientGetUpdatesAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  401,
			"description": "unauthorized",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret", server.Client())
	_, err := client.GetUpdates(context.Background(), GetUpdatesRequest{Offset: 11})
	if err == nil {
		t.Fatal("expected API error, got nil")
	}
}

func TestClientGetUpdatesMalformedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret", server.Client())
	_, err := client.GetUpdates(context.Background(), GetUpdatesRequest{Offset: 11})
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}
