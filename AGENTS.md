# Repository Guidelines

## Project Structure & Module Organization
`cmd/relay/main.go` is the executable entrypoint. Core wiring lives in `internal/app`, configuration loading and validation in `internal/config`, Bale API integration in `internal/bale`, Mattermost delivery in `internal/mattermost`, relay orchestration in `internal/relay`, SQLite persistence in `internal/store`, and shared types in `internal/model`. Keep new packages under `internal/` unless they are meant to be imported externally. Product notes and feature prompts belong in `docs/`, for example `docs/prd.md`.

## Build, Test, and Development Commands
Use the Go toolchain directly; there is no Makefile.

- `go run ./cmd/relay` runs the service locally using environment variables.
- `go build ./cmd/relay` builds the relay binary for deployment checks.
- `go test ./...` runs the full test suite across all packages.
- `go test ./internal/relay -run TestProcessUpdate` runs targeted relay tests while iterating.
- `gofmt -w ./cmd ./internal` formats source files before review.

## Coding Style & Naming Conventions
Follow standard Go formatting and import ordering via `gofmt`. Use lower-case package names, `CamelCase` for exported identifiers, and concise nouns for types such as `Service`, `Config`, and `Poster`. Prefer explicit error returns over panics. Keep functions small and dependency-driven; this codebase already favors constructor-style setup such as `NewClient`, `NewAPIPoster`, and `NewSQLiteStore`.

## Testing Guidelines
Tests use Go’s built-in `testing` package and live next to the code in `_test.go` files. Match existing patterns: call `t.Parallel()` for isolated tests, use `httptest` for HTTP clients, and `t.TempDir()` for SQLite-backed tests. Add coverage for config validation, retry behavior, persistence, and message formatting whenever those paths change. Run `go test ./...` before opening a PR; use `go test -race ./...` for concurrency-sensitive changes.

## Commit & Pull Request Guidelines
Git history currently only contains `Initial commit`, so there is no mature convention yet. Use short, imperative commit subjects such as `Add relay retry logging` or `Validate Mattermost API mode`. PRs should include a clear summary, any required environment-variable changes, linked issues, and test evidence. If message formatting changes, include a sample rendered payload or screenshot from Mattermost.

## Security & Configuration Tips
Do not commit secrets or local runtime data. `BALE_BOT_TOKEN`, `MATTERMOST_*`, and the default SQLite file at `./data/relay.db` should stay out of version control. Validate webhook/API settings locally before deployment.
