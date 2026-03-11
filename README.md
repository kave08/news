# News Relay

> A single Go service that moves operational messages and curated news into Mattermost.

## Overview

This project exists for teams that receive alerts in Bale or Telegram but coordinate in Mattermost. It runs one long-lived process, reads from selected upstream sources, applies simple business rules, stores delivery state in SQLite, and posts clean messages into a Mattermost channel.

Today the binary can run three flows at the same time:

- Bale relay for allowlisted groups, private chats, and channel posts
- Telegram userbot for selected public channels
- Optional news ingestion with SQLite-backed deduplication

## Core Behavior

### Bale relay

- Polls Bale `message` and `channel_post` updates
- Forwards only allowlisted chat or channel IDs
- Optionally requires one of `BALE_ALLOWED_HASHTAGS`
- Removes configured mentions and fixed phrases before posting
- Retries transient Mattermost failures and persists state to avoid reposts

### Telegram userbot

- Connects through MTProto using a real Telegram account session
- Watches configured channel usernames
- Reuses the same hashtag, mention, and phrase filtering model as Bale
- Stores sent, skipped, and failed deliveries separately in SQLite

### News service

- Runs only when `NEWS_ENABLED=true`
- Fetches articles on an interval, deduplicates them, and posts new matches
- `NEWS_PROVIDER=legacy_html` is the active runtime path
- `NEWS_PROVIDER=bbc_approved` is intentionally disabled until an approved BBC feed or API exists

## Repository Layout

```text
cmd/relay            entrypoint
internal/app         runtime wiring and concurrent service startup
internal/bale        Bale API client and update types
internal/config      .env loading, validation, defaults
internal/mattermost  webhook/API posting and formatting
internal/news        provider-driven news ingestion
internal/relay       Bale filtering, cleanup, retry, delivery
internal/store       SQLite state for Bale, Telegram, and news
internal/telegram    Telegram MTProto userbot
internal/model       shared message and article models
scripts              local helpers and config checks
```

## Runtime Model

```text
Bale -----------\
Telegram ------- +--> Mattermost
News providers --/
        |
      SQLite
```

SQLite keeps source-specific state in separate tables, including `messages`, `telegram_messages`, and `news_articles`.

## Quick Start

### 1. Prepare Go

If your machine has a broken global Go proxy for this repo:

```zsh
source scripts/use-goenv.zsh
```

### 2. Create local config

```zsh
cp .env.example .env
```

The binary auto-loads `.env` from the repo root.

Minimal Bale + Mattermost webhook setup:

```env
BALE_BOT_TOKEN=replace-me
BALE_ALLOWED_CHAT_IDS=4864878071,5875733190
BALE_ALLOWED_HASHTAGS=#پیام_دریافتی
BALE_STRIP_MENTIONS=@tehran_alarm
BALE_STRIP_PHRASES="پاینده باد ایران 🇮🇷"
MATTERMOST_MODE=webhook
MATTERMOST_WEBHOOK_URL=https://mattermost.example/hooks/replace-me
SQLITE_PATH=./data/relay.db
LOG_LEVEL=info
NEWS_ENABLED=false
TELEGRAM_ENABLED=false
```

If a value contains spaces and you plan to use shell-based tools such as `./scripts/check-config.sh`, wrap it in quotes.

### 3. Validate and run

```zsh
./scripts/check-config.sh
go run ./cmd/relay
```

### 4. Test

```zsh
go test ./...
```

## Configuration Notes

- `BALE_ALLOWED_CHAT_IDS` accepts a comma-separated list of Bale IDs. Use the value returned by Bale APIs, not the web URL alone.
- Bale relay supports channel forwarding through `channel_post`, so Bale channels and groups can share the same pipeline.
- `BALE_ALLOWED_HASHTAGS` is optional. When set, at least one configured hashtag must appear in the cleaned message.
- `BALE_STRIP_MENTIONS` and `BALE_STRIP_PHRASES` are applied before delivery to Mattermost.
- Mattermost delivery supports `MATTERMOST_MODE=webhook` and `MATTERMOST_MODE=api`.
- `NEWS_SITES_JSON` fully replaces the built-in legacy HTML source list.
- `TELEGRAM_ENABLED=true` requires `TELEGRAM_API_ID`, `TELEGRAM_API_HASH`, `TELEGRAM_PHONE`, and `TELEGRAM_CHANNELS`.

## Development

Useful commands:

```zsh
gofmt -w ./cmd ./internal
go test ./...
go run ./cmd/relay
```

The runtime is intentionally conservative: explicit config validation, bounded HTTP timeouts, retry with backoff, and SQLite-backed deduplication.

## Security

- Never commit `.env`, tokens, webhook URLs, or Telegram session files.
- Rotate Bale tokens and Mattermost webhooks immediately if they were pasted into chat or logs.
- Keep runtime data under `./data` out of version control.
- Review permissions and terms before enabling external news sources in production.
