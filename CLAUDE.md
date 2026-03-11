# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

There is no Makefile; use the Go toolchain directly.

- `go run ./cmd/relay` — run the service locally (reads `.env` if present)
- `go build ./cmd/relay` — compile the relay binary
- `go test ./...` — full test suite
- `go test -race ./...` — test with race detector (use for concurrency changes)
- `go test ./internal/relay -run TestProcessUpdate` — run a single test
- `gofmt -w ./cmd ./internal` — format source files

## Architecture

**Entry point**: `cmd/relay/main.go` → `internal/app/app.go` (service wiring and concurrent orchestration)

Two long-running services run concurrently via goroutines, both cancellable via shared context:

1. **Relay Service** (`internal/relay/service.go`): Polls Bale (Persian messaging platform) for updates from allowlisted chats and posts them to Mattermost. Filters by chat ID, optional hashtags, sanitizes text. Tracks cursor position in SQLite to avoid reprocessing after restart.

2. **News Service** (`internal/news/service.go`): Periodically scrapes configured news sites and posts new articles to Mattermost. Uses article key (SHA256 of site/title/URL) for deduplication. Enabled only when `NEWS_ENABLED=true`.

**Key packages:**

| Package | Purpose |
|---|---|
| `internal/config` | Env-based config loading and validation (auto-loads `.env`) |
| `internal/bale` | HTTP client for Bale Bot API (GetMe, GetUpdates) |
| `internal/mattermost` | Message posting — webhook mode or API mode |
| `internal/store` | SQLite persistence for relay cursor, message records, article deduplication |
| `internal/model` | Shared domain types (RelayMessage, NewsArticle, status enums) |
| `internal/news` | News providers — `legacy_provider.go` (goquery HTML scraper), `bbc_provider.go` (disabled) |

**Mattermost poster modes** (set via `MATTERMOST_MODE`):
- `webhook` (default): POST to webhook URL, no auth required
- `api`: POST to `/api/v4/posts`, requires `MATTERMOST_BOT_TOKEN` and `MATTERMOST_CHANNEL_ID`

**SQLite tables**: `relay_state` (KV cursor), `messages` (relay records), `news_articles` (news records). All status flows are one-directional: relay goes `received → sent|ignored|skipped|failed`; news goes `discovered → sent|skipped|failed`.

**Retry pattern**: Both services use exponential backoff with configurable base/max delays and max attempts. Context cancellation stops retries immediately.

## Testing Patterns

- Tests live next to code in `_test.go` files
- Call `t.Parallel()` for isolated tests
- Use `httptest` to mock Bale and Mattermost HTTP endpoints
- Use `t.TempDir()` for isolated SQLite databases per test

## Configuration

Copy `.env.example` to `.env`. Required variables:
- `BALE_BOT_TOKEN`
- `BALE_ALLOWED_CHAT_IDS` (comma-separated chat IDs)
- `MATTERMOST_WEBHOOK_URL` (webhook mode) or `MATTERMOST_BASE_URL` + `MATTERMOST_BOT_TOKEN` + `MATTERMOST_CHANNEL_ID` (API mode)

Runtime SQLite database at `./data/relay.db` — not committed to version control.
