PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS snapshots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    taken_at     DATETIME NOT NULL,
    library_id   TEXT NOT NULL,
    library_name TEXT NOT NULL,
    item_count   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS items (
    jellyfin_id      TEXT PRIMARY KEY,
    library_id       TEXT NOT NULL,
    title            TEXT NOT NULL,
    sort_title       TEXT NOT NULL DEFAULT '',
    media_type       TEXT NOT NULL DEFAULT '',
    year             INTEGER NOT NULL DEFAULT 0,
    image_tag        TEXT NOT NULL DEFAULT '',
    added_at         DATETIME NOT NULL DEFAULT '',
    last_seen_at     DATETIME NOT NULL,
    is_removed       BOOLEAN NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id  INTEGER NOT NULL REFERENCES snapshots(id),
    jellyfin_id  TEXT NOT NULL,
    event_type   TEXT NOT NULL CHECK(event_type IN ('added', 'removed')),
    occurred_at  DATETIME NOT NULL,
    title        TEXT NOT NULL,
    library_name TEXT NOT NULL,
    media_type   TEXT NOT NULL DEFAULT '',
    image_tag    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    username   TEXT NOT NULL,
    token      TEXT NOT NULL,
    libraries  TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_occurred_at ON events(occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_library     ON events(library_name);
CREATE INDEX IF NOT EXISTS idx_events_type        ON events(event_type);
CREATE INDEX IF NOT EXISTS idx_events_jellyfin_id ON events(jellyfin_id);
CREATE INDEX IF NOT EXISTS idx_items_library      ON items(library_id);
CREATE INDEX IF NOT EXISTS idx_items_library_rem  ON items(library_id, is_removed);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
