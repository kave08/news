# News Relay

> A small Go service that moves important information into Mattermost: Bale group messages for team coordination, and optional curated news updates for monitoring fast-moving events.

## Why This Exists

This project solves a business communication gap. Teams that operate in Bale often still need their operational timeline, alerts, or monitoring feed to land in Mattermost, where broader teams collaborate. The service keeps those channels in sync and can also enrich the Mattermost stream with filtered news from configured public sources.

In practice, the app can:

- relay Bale messages from allowlisted chats into a Mattermost channel
- preserve delivery state so messages are not reposted after restart
- optionally crawl selected news sites and post only relevant new articles
- run both flows inside one long-running process

## What The Service Does

### Bale Relay

The relay polls Bale updates, filters by allowed chat IDs, normalizes message content, retries transient Mattermost failures, and records every delivery attempt in SQLite.

### Optional News Monitor

When `NEWS_ENABLED=true`, the same process starts a second service that fetches configured news inputs on an interval, deduplicates them, stores them in SQLite, and posts new items to the same Mattermost destination. The current built-in runtime path is the legacy HTML provider for non-BBC sources.

## Technical Shape

The codebase follows a simple service-oriented layout:

```text
cmd/relay            main entrypoint
internal/app         process wiring and concurrent service startup
internal/config      env loading and validation
internal/relay       Bale polling and message delivery workflow
internal/news        news crawling, parsing, filtering, deduplication
internal/mattermost  webhook/API posting and message formatting
internal/store       SQLite persistence for relay and news state
internal/model       shared domain models
```

Runtime flow:

```text
Bale API -----> relay service ---\
                                  > Mattermost
News websites -> news service ---/
                     |
                   SQLite
```

## Quick Start

### 1. Install dependencies

If your global Go proxy is blocked in this repo, use the local helper:

```zsh
source scripts/use-goenv.zsh
go mod tidy
```

### 2. Prepare configuration

Copy the example file and fill your local secrets:

```zsh
cp .env.example .env
```

The binary auto-loads `.env` from the repo root if the file exists. Process environment variables still override values from `.env`.

Validate the required fields before running:

```zsh
./scripts/check-config.sh
```

You can also skip `.env` and export variables in your shell.

Webhook mode:

```env
BALE_BOT_TOKEN=replace-me
BALE_ALLOWED_CHAT_IDS=-1001234567890
BALE_ALLOWED_HASHTAGS=#پیام_دریافتی
BALE_STRIP_MENTIONS=@tehran_alarm
BALE_STRIP_PHRASES=پاینده باد ایران 🇮🇷
MATTERMOST_MODE=webhook
MATTERMOST_WEBHOOK_URL=https://mattermost.example/hooks/replace-me
SQLITE_PATH=./data/relay.db
LOG_LEVEL=info
NEWS_ENABLED=false
```

API mode:

```env
BALE_BOT_TOKEN=replace-me
BALE_ALLOWED_CHAT_IDS=-1001234567890
MATTERMOST_MODE=api
MATTERMOST_BASE_URL=https://mattermost.example
MATTERMOST_BOT_TOKEN=replace-me
MATTERMOST_CHANNEL_ID=replace-me
SQLITE_PATH=./data/relay.db
LOG_LEVEL=info
NEWS_ENABLED=false
```

Optional news settings:

```env
NEWS_ENABLED=true
NEWS_PROVIDER=legacy_html
NEWS_INTERVAL=15m
NEWS_MAX_ARTICLES_PER_CYCLE=5
NEWS_REQUEST_TIMEOUT_SEC=30
NEWS_FETCH_DELAY_MIN_SEC=1
NEWS_FETCH_DELAY_MAX_SEC=5
NEWS_USER_AGENT=NewsBot/1.0
```

`NEWS_SITES_JSON` can fully replace the built-in source list when `NEWS_PROVIDER=legacy_html`.

### 3. Run the service

```zsh
go run ./cmd/relay
```

### 4. Run tests

```zsh
go test ./...
```

## Configuration Notes

- `BALE_ALLOWED_CHAT_IDS` accepts a comma-separated list.
- `BALE_ALLOWED_HASHTAGS` is optional; if set, only messages containing at least one configured hashtag are forwarded.
- `BALE_STRIP_MENTIONS` is optional; configured mentions are removed from Bale text before sending to Mattermost.
- `BALE_STRIP_PHRASES` is optional; exact matching phrases are removed from Bale text before sending to Mattermost.
- `MATTERMOST_MODE` supports `webhook` and `api`.
- `SQLITE_PATH` defaults to `./data/relay.db`.
- News crawling is disabled by default and must be explicitly enabled.
- `NEWS_PROVIDER` supports `legacy_html` and `bbc_approved`.
- The built-in default news sources are legacy HTML sources and do not include BBC.
- `bbc_approved` is reserved for an explicit BBC-approved feed/API path and is intentionally not runnable in this repo.
- Built-in legacy HTML sources are defaults only; production selectors should be reviewed regularly because site markup can change.

## Security

- Never commit tokens, webhook URLs, or local `.env` files.
- Keep SQLite runtime data out of version control.
- Prefer bot-scoped Mattermost credentials instead of personal credentials.
- Review news source terms and permissions before enabling crawler mode in production.

## Development

Useful commands:

```zsh
gofmt -w ./cmd ./internal
go test ./...
go run ./cmd/relay
```

The service is intentionally conservative: explicit config validation, bounded HTTP timeouts, retry with backoff, and persistent state to prevent duplicate delivery.
