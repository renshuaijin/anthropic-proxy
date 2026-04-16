# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run Commands

```bash
# Build binary
make build

# Run locally (requires config.yaml)
make run
# Or directly: ./bin/anthropic-proxy -config config.yaml

# Run tests with race detection
make test

# Run linter
make vet

# Docker
make docker-up   # starts on 127.0.0.1:${PORT:-8087}
make docker-down
```

## Architecture

Reverse proxy for Anthropic-compatible APIs with automatic retry on upstream overload.

- `cmd/anthropic-proxy` — CLI entrypoint, loads config and starts HTTP server
- `internal/config` — YAML config parsing with env var overrides (`PROVIDER`, `UPSTREAM_URL`, `LISTEN_ADDR`, `CONFIG_FILE`)
- `internal/provider` — Defines `Rule` type for overload detection; `Match()` finds matching rule by status code + optional body substring
- `internal/proxy` — Core handler: forwards requests, buffers error responses to check for overload, retries with linear backoff (`delay + N × jitter`)
- `internal/storage` — SQLite storage for request logging
- `internal/web` — Web UI and API endpoints (`/web`, `/api/logs`)

## Retry Logic (internal/proxy)

The handler locks in retry parameters from the **first** matching overload rule and reuses them for all subsequent retries on that request. Connection errors fall back to the first rule's parameters. Successful (2xx) responses stream directly without buffering.

## Configuration

- `config.yaml` defines providers, each with `upstream` URL and `overload_rules`
- Env vars override: `PROVIDER` (which provider to use), `UPSTREAM_URL`, `LISTEN_ADDR`, `CONFIG_FILE`
- Each rule: `status` (required), `body_contains` (optional), `max_retries`, `delay`, `jitter` (defaults: 10, 2s, 1s)

## Request Logging

Enable in `config.yaml`:

```yaml
logging:
  enabled: true
  database_path: ./logs.db
  max_age_days: 7
```

- Web UI at `/web` — view request history with filtering
- API at `/api/logs` — paginated JSON list
- SSE responses log metadata only (status, duration, retries)
- Error responses log full body
