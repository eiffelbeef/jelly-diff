# AGENTS.md — jelly-diff

> **jelly-diff** is a self-hosted Go web application that tracks what has been
> added to and removed from a [Jellyfin](https://jellyfin.org/) media library
> over time. It exposes a clean, GitHub-style web UI and is designed to be
> deployed with a single `docker compose up` command.

---

## Table of Contents

1. [Project Vision](#1-project-vision)
2. [Functional Requirements](#2-functional-requirements)
3. [Non-Functional Requirements](#3-non-functional-requirements)
4. [Architecture Overview](#4-architecture-overview)
5. [Directory Structure](#5-directory-structure)
6. [Data Model](#6-data-model)
7. [Component Specifications](#7-component-specifications)
   - 7.1 [Jellyfin Sync Worker](#71-jellyfin-sync-worker)
   - 7.2 [HTTP Server & Web UI](#72-http-server--web-ui)
   - 7.3 [Authentication & Session Management](#73-authentication--session-management)
   - 7.4 [Storage Layer](#74-storage-layer)
8. [API Contract](#8-api-contract)
9. [Configuration](#9-configuration)
10. [Docker & Deployment](#10-docker--deployment)
11. [Coding Conventions](#11-coding-conventions)
12. [Testing Strategy](#12-testing-strategy)
13. [Implementation Status](#13-implementation-status)

---

## 1. Project Vision

Jellyfin does not natively expose a diff-style changelog of library mutations.
`jelly-diff` fills this gap by periodically snapshotting the Jellyfin library
state and computing the delta between consecutive snapshots. Users can browse a
chronological activity feed ("what was added/removed today?"), search across all
events, and filter by library, media type, date range, or event type.

The aesthetic goal is **GitHub-flavoured minimalism**: monochrome base palette,
clear typographic hierarchy, diff-style colour accents for adds (+green) and
removes (-red), and zero JavaScript frameworks (server-side HTML with minimal
vanilla JS for interactivity).

---

## 2. Functional Requirements

### F-1 Library Snapshot & Diff
- Connect to a Jellyfin server via its HTTP API using an API key.
- Periodically fetch the full item list for every configured library.
- Persist the snapshot to a local SQLite database in atomic transactions.
- Compute a diff against the previous snapshot and record each changed item as
  an `Event` (type: `added` | `removed`).
- Intelligent handling for TV series (season grouping) and audio tracks (album grouping).
- Upgrade detection to suppress false add/remove cycles when media files are upgraded.

### F-2 Authentication & Scoped Access
- Sign in with Jellyfin user credentials.
- Multi-user support with per-user library visibility matching Jellyfin permissions.
- Secure, persistent sessions stored in SQLite with sliding expiration.
- Proxied media cover images and live on-demand rich metadata fetched using the user's token.

### F-3 Web UI — Activity Feed
- Display a chronological, paginated feed of events.
- Each event shows: event type badge, media title, library name, media type
  (movie / episode / album / book / …), thumbnail, and relative timestamp.
- Group events by day (similar to GitHub's commit list).

### F-4 Web UI — Detail View
- Clicking an event opens a detail page with live rich metadata (year,
  genres, rating, overview, poster) and a link back to Jellyfin.

### F-5 Filtering & Search
- Filter by: library, media type, event type (added/removed), date range.
- Full-text search across titles, series names, and album names.

### F-6 Statistics Dashboard
- Summary cards: total items tracked, items added this week/month, items
  removed this week/month.
- SVG bar chart (pure server-side SVG, no JS libraries) showing add/remove counts per
  day over the last 30 days.

### F-7 Manual Sync Trigger
- A "Sync Now" button in the UI and a `POST /api/sync` endpoint that
  triggers an immediate snapshot + diff cycle with configurable rate limiting and cooldown.

### F-8 Notifications
- Optional webhook notifications (Discord, Slack, generic HTTP) fired on each diff
  cycle when changes are detected.

---

## 3. Non-Functional Requirements

| Concern | Requirement |
|---|---|
| Language | Go 1.25+ |
| Dependencies | Minimal; prefer stdlib. Allowed third-party: `modernc.org/sqlite`, `go-chi/chi/v5`. |
| Deployment | Single binary + embedded assets; Docker image ≤ 50 MB. |
| Storage | SQLite file with WAL mode; zero external databases required. |
| Performance | Atomic sync transactions; complete in < 500 ms for libraries up to 50 000 items. Pre-compiled HTML templates. |
| Security | API keys compared with constant-time comparison. CSRF tokens on mutating endpoints. Cookie flags `HttpOnly` and `SameSite=Lax`. |
| Portability | `linux/amd64`, `linux/arm64` Docker images via multi-platform build. |

---

## 4. Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                    jelly-diff process                │
│                                                      │
│  ┌──────────────┐      ┌────────────────────────┐   │
│  │  Sync Worker │─────▶│   Storage (SQLite)     │   │
│  │  (ticker +   │      │  - schema.sql          │   │
│  │   manual)    │      │  - snapshots, items    │   │
│  └──────┬───────┘      │  - events, sessions    │   │
│         │              └───────────┬────────────┘   │
│         │ Jellyfin API             │                 │
│         ▼                          ▼                 │
│  ┌──────────────┐      ┌────────────────────────┐   │
│  │  Jellyfin    │      │   HTTP Server (chi)    │   │
│  │  Client      │      │  - UI & Pre-parsed AST │   │
│  └──────────────┘      │  - REST API & Sessions │   │
│                         └────────────────────────┘   │
└─────────────────────────────────────────────────────┘
         ▲                          │
  Jellyfin Server            Browser / curl
```

The application runs as a **single binary** with two concurrent systems:
1. The **sync worker** (background ticker + manual trigger channel).
2. The **HTTP server** (foreground chi router with pre-compiled templates).

Both share a thread-safe `*storage.Store` connection pool.

---

## 5. Directory Structure

```
jelly-diff/
├── AGENTS.md                   ← this file
├── README.md
├── LICENSE
│
├── cmd/
│   └── jelly-diff/
│       └── main.go             ← binary entry point; wires everything together
│
├── internal/
│   ├── config/
│   │   └── config.go           ← config struct, env/file loading
│   │
│   ├── jellyfin/
│   │   ├── client.go           ← Jellyfin HTTP API client & auth
│   │   ├── models.go           ← Jellyfin API response types
│   │   └── client_test.go
│   │
│   ├── storage/
│   │   ├── db.go               ← SQLite connection & schema initialization
│   │   ├── schema.sql          ← embedded canonical DDL & indexes
│   │   ├── queries.go          ← all SQL queries & atomic batch sync
│   │   ├── models.go           ← internal DB row types
│   │   └── storage_test.go
│   │
│   ├── sync/
│   │   ├── worker.go           ← ticker loop, diff engine, season/album grouping
│   │   └── worker_test.go
│   │
│   ├── server/
│   │   ├── server.go           ← chi router setup, middleware, pre-parsed templates
│   │   ├── handlers_ui.go      ← HTML page handlers (feed, detail, stats)
│   │   ├── handlers_api.go     ← JSON API handlers
│   │   ├── handlers_auth.go    ← login/logout & image proxy
│   │   ├── session.go          ← SQLite-backed session store
│   │   ├── csrf.go             ← CSRF token helpers & constant-time validation
│   │   └── server_test.go
│   │
│   └── notify/
│       ├── notifier.go         ← notification interface
│       └── webhook.go          ← Discord / Slack / generic webhook impl
│
├── web/
│   ├── web.go                  ← //go:embed static templates
│   ├── templates/
│   │   ├── base.html           ← base layout (nav, footer, theme toggle)
│   │   ├── feed.html           ← activity feed page
│   │   ├── detail.html         ← event detail page
│   │   ├── stats.html          ← statistics dashboard
│   │   └── login.html          ← Jellyfin authentication page
│   │
│   └── static/
│       ├── style.css           ← GitHub-palette stylesheet with dark/light themes
│       ├── app.js              ← vanilla JS (sync trigger, live polling, filters)
│       ├── favicon.svg         ← vector favicon
│       └── icons/              ← embedded SVG icons (sun, moon, desktop, sync, etc.)
│
├── Dockerfile                  ← multi-stage build (golang:alpine -> alpine)
├── docker-compose.yml          ← production compose definition
├── .env.example
└── Makefile
```

---

## 6. Data Model

The schema is defined in `internal/storage/schema.sql` and initialized automatically on startup.

### Table: `snapshots`

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | Auto-increment |
| `taken_at` | DATETIME | UTC timestamp of the snapshot |
| `library_id` | TEXT | Jellyfin library collection ID |
| `library_name` | TEXT | Human-readable library name |
| `item_count` | INTEGER | Total items tracked in this snapshot |

### Table: `items`

Stores the latest known state of every item seen at least once.

| Column | Type | Notes |
|---|---|---|
| `jellyfin_id` | TEXT PK | Jellyfin item ID |
| `library_id` | TEXT | Library identifier |
| `title` | TEXT | Display title |
| `sort_title` | TEXT | Formatted sort key |
| `media_type` | TEXT | `Movie`, `Episode`, `Audio`, `Book`, … |
| `year` | INTEGER | Production year |
| `image_tag` | TEXT | Primary image tag for posters/covers |
| `added_at` | DATETIME | Jellyfin `DateCreated` |
| `last_seen_at` | DATETIME | Timestamp of last snapshot including this item |
| `is_removed` | BOOLEAN | `1` once a removal is recorded |

### Table: `events`

Immutable log of all library mutations.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | Auto-increment |
| `snapshot_id` | INTEGER | FK → `snapshots.id` |
| `jellyfin_id` | TEXT | Target item or container ID (Series/Album ID) |
| `event_type` | TEXT | `added` or `removed` |
| `occurred_at` | DATETIME | Snapshot timestamp |
| `title` | TEXT | Denormalised display title |
| `library_name` | TEXT | Denormalised library name |
| `media_type` | TEXT | Denormalised media type |
| `image_tag` | TEXT | Denormalised image tag for cover rendering |

### Table: `sessions`

Stores authenticated Jellyfin user sessions.

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | 256-bit random hex session ID |
| `user_id` | TEXT | Jellyfin user ID |
| `username` | TEXT | Jellyfin username |
| `token` | TEXT | Jellyfin user access token |
| `libraries` | TEXT | JSON array of accessible library names |
| `created_at` | DATETIME | Session creation timestamp |
| `expires_at` | DATETIME | Sliding expiration timestamp (30 days) |

### Indexes
- `idx_events_occurred_at` ON `events(occurred_at DESC)`
- `idx_events_library` ON `events(library_name)`
- `idx_events_type` ON `events(event_type)`
- `idx_events_jellyfin_id` ON `events(jellyfin_id)`
- `idx_items_library` ON `items(library_id)`
- `idx_items_library_rem` ON `items(library_id, is_removed)`
- `idx_sessions_expires_at` ON `sessions(expires_at)`

---

## 7. Component Specifications

### 7.1 Jellyfin Sync Worker

**File:** `internal/sync/worker.go`

- Executes on startup and intervals defined by `SYNC_INTERVAL`.
- Supports manual sync triggering with cooldown protection (`MIN_SYNC_INTERVAL`).
- Computes library diffs in a single pass in memory.
- Calls `storage.Store.SaveLibrarySync` to persist snapshot, item upserts, metadata updates, and events in a single SQLite transaction.
- Groups episode additions by season into unified events (`Series — Season X (N episodes)`).
- Groups audio tracks by album into album events.
- Performs upgrade matching by comparing fingerprints between disappeared and new items.

### 7.2 HTTP Server & Web UI

**File:** `internal/server/`

- Built with `go-chi/chi/v5`.
- Pre-compiles all HTML templates at startup using `template.Must`.
- GitHub-inspired monochrome UI with light, dark, and auto system themes.
- SVG bar chart rendered purely server-side with zero frontend charting libraries.
- CSRF protection on mutating endpoints with `crypto/subtle.ConstantTimeCompare`.
- Proxies Jellyfin images via `/images/{id}` using the user's session token.

### 7.3 Authentication & Session Management

**Files:** `internal/server/handlers_auth.go`, `internal/server/session.go`

- Authenticates against Jellyfin `/Users/AuthenticateByName`.
- Fetches and caches user-accessible libraries to scope feed, stats, and search queries.
- Persists session records to SQLite with in-memory caching for zero-overhead request authorization.

### 7.4 Storage Layer

**Files:** `internal/storage/`

- Uses `modernc.org/sqlite` (pure Go, CGO-free).
- Embeds `schema.sql` directly into the package.
- `Open(path string) (*Store, error)` initializes the schema automatically.
- No external migration utilities or multi-step migration directories.

---

## 8. API Contract

### Public Routes
- `GET /health` — health check `{ "ok": true }`
- `GET /login` — login HTML page
- `POST /login` — authenticate credentials against Jellyfin
- `GET /logout`, `POST /logout` — terminate session

### Authenticated Routes
- `GET /` — activity feed (HTML)
- `GET /events/{id}` — event detail view (HTML)
- `GET /stats` — dashboard with summary cards & SVG chart (HTML)
- `GET /images/{id}` — proxied media image
- `GET /api/events` — paginated, filtered event list (JSON)
- `GET /api/stats` — statistics summary (JSON)
- `GET /api/sync` — current sync status & sequence counter (JSON)
- `POST /api/sync` — trigger manual sync (JSON, CSRF or API key protected)

---

## 9. Configuration

Configured via environment variables or `.env`:

| Variable | Required | Default | Description |
|---|---|---|---|
| `JELLYFIN_URL` | ✅ | — | Base URL of Jellyfin server (`http://jellyfin:8096`) |
| `JELLYFIN_API_KEY` | ✅ | — | Jellyfin server API key |
| `SYNC_INTERVAL` | | `1h` | Automatic sync frequency (`30m`, `1h`, `6h`, …) |
| `MIN_SYNC_INTERVAL` | | `10m` | Minimum cooldown between syncs (`30s`, `1m`, `10m`, …) |
| `DB_PATH` | | `/data/jelly-diff.db` | SQLite database file path |
| `LISTEN_ADDR` | | `:6363` | HTTP listen address |
| `WEBHOOK_URL` | | — | Optional webhook URL |
| `WEBHOOK_TYPE` | | `generic` | `discord`, `slack`, or `generic` |
| `LOG_LEVEL` | | `info` | `debug`, `info`, `warn`, `error` |

---

## 10. Docker & Deployment

Single container deployment with `docker compose up -d`:

```yaml
services:
  jelly-diff:
    image: ghcr.io/eiffelbeef/jelly-diff:latest
    restart: unless-stopped
    ports:
      - "6363:6363"
    volumes:
      - ./data:/data
    environment:
      - JELLYFIN_URL=${JELLYFIN_URL}
      - JELLYFIN_API_KEY=${JELLYFIN_API_KEY}
      - SYNC_INTERVAL=${SYNC_INTERVAL:-1h}
      - MIN_SYNC_INTERVAL=${MIN_SYNC_INTERVAL:-10m}
      - DB_PATH=/data/jelly-diff.db
      - LISTEN_ADDR=:6363
      - LOG_LEVEL=${LOG_LEVEL:-info}
    env_file:
      - .env
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:6363/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
```

---

## 11. Coding Conventions

- **Go style**: Idiomatic Go, `gofmt`, strict error wrapping with `%w`.
- **Performance**: Single-transaction database syncs, pre-compiled templates, zero redundant allocations.
- **Security**: Constant-time token comparisons, CSRF token validation, scoped user queries.
- **Simplicity & DRY**: No duplicate loops or formatting; shared base structures.

---

## 12. Testing Strategy

- `internal/storage`: In-memory SQLite tests for schema creation, CRUD, and atomic batch sync transactions.
- `internal/sync`: Mock Jellyfin server verifying diff calculation, upgrades, season/album grouping, and cooldowns.
- `internal/server`: Mock HTTP server tests verifying auth flow, session persistence, template rendering, and rate limits.
- `internal/jellyfin`: Client tests for pagination, header generation, and error handling.

---

## 13. Implementation Status

All milestones are completed and verified:

- [x] M1 — Skeleton, config loading, and Docker definition
- [x] M2 — Storage layer, schema consolidation, and atomic transaction syncing
- [x] M3 — Jellyfin client, authentication, and paginated fetch
- [x] M4 — Sync worker, diff logic, season grouping, and upgrade detection
- [x] M5 — HTTP server, authentication middleware, and JSON APIs
- [x] M6 — Web UI, pre-compiled templates, GitHub theme, and SVG chart
- [x] M7 — Webhook notifications (Discord, Slack, generic)
- [x] M8 — Release polish, DRY refactoring, and documentation update

---

*Release v1.0.0 Ready*
