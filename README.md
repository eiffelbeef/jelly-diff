# 🪼 jelly-diff

> Track what gets added to and removed from your Jellyfin media library.

> [!NOTE]
> **Personal project disclaimer:** This application was LLM-coded as a personal project to solve a specific itch. It is reviewed and maintained with agentic AI tooling.

`jelly-diff` periodically snapshots your [Jellyfin](https://jellyfin.org) libraries and provides a clean, GitHub-style chronological feed of all media mutations — additions, removals, upgrades, and library statistics.

---

## Quick Start

### Docker Compose (Recommended)

1. **`docker-compose.yml`:**
   ```yaml
   services:
     jelly-diff:
       image: ghcr.io/eiffelbeef/jelly-diff:latest
       restart: unless-stopped
       container_name: jelly-diff
       ports:
         - "6363:6363"
       volumes:
         - ./data:/data
       environment:
         - JELLYFIN_URL=http://your-jellyfin-host:8096
         - JELLYFIN_API_KEY=your_api_key_here
         - SYNC_INTERVAL=1h
         - MIN_SYNC_INTERVAL=10m
   ```

2. **Run:**
   ```bash
   docker compose up -d
   ```

3. **Open:** [http://localhost:6363](http://localhost:6363) and log in with your Jellyfin user credentials.

---

## Configuration

All configuration is supplied via environment variables (or a local `.env` file):

| Variable | Required | Default | Description |
|---|---|---|---|
| `JELLYFIN_URL` | ✅ | — | Base URL of Jellyfin server (`http://jellyfin:8096`) |
| `JELLYFIN_API_KEY` | ✅ | — | Jellyfin server API key (Dashboard → API Keys) |
| `SYNC_INTERVAL` | | `1h` | Automatic sync frequency (`30m`, `1h`, `6h`, …) |
| `MIN_SYNC_INTERVAL` | | `10m` | Cooldown between manual syncs (`1m`, `10m`, …) |
| `DB_PATH` | | `/data/jelly-diff.db` | SQLite database file location |
| `LISTEN_ADDR` | | `:6363` | HTTP listen address |
| `WEBHOOK_URL` | | — | Optional webhook URL for change notifications |
| `WEBHOOK_TYPE` | | `generic` | Webhook format: `discord`, `slack`, or `generic` |
| `LOG_LEVEL` | | `info` | Log level: `debug`, `info`, `warn`, `error` |

---

## Features

- **GitHub-style Activity Feed:** Chronological, day-grouped timeline of library changes.
- **Smart Grouping:** Groups season batches and audio albums instead of flooding individual items.
- **Upgrade Detection:** Replaced or upgraded files suppress false delete/add cycles.
- **Jellyfin-scoped Auth:** Authenticate with Jellyfin accounts; item visibility and posters match user permissions.
- **Server-rendered SVG Charts:** Zero frontend charting libraries; pure server-side SVG activity graph.
- **Webhooks:** Instant diff alerts delivered to Discord, Slack, or generic HTTP targets.

---

## Development

```bash
# Run unit & race detector tests
go test -race ./...

# Compile standalone binary
make build
```

---

## License

[MIT](LICENSE)
