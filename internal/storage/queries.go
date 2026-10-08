package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ── Snapshots ────────────────────────────────────────────────────────────────

const queryInsertSnapshot = `
INSERT INTO snapshots (taken_at, library_id, library_name, item_count)
VALUES (?, ?, ?, ?)`

// InsertSnapshot persists a new snapshot and returns its row ID.
func (s *Store) InsertSnapshot(ctx context.Context, snap Snapshot) (int64, error) {
	res, err := s.db.ExecContext(ctx, queryInsertSnapshot,
		snap.TakenAt.UTC().Format(time.RFC3339),
		snap.LibraryID,
		snap.LibraryName,
		snap.ItemCount,
	)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}
	return res.LastInsertId()
}

const queryGetLastSyncAt = `
SELECT MAX(taken_at) FROM snapshots`

// GetLastSyncAt returns the timestamp of the most recent snapshot, or nil.
func (s *Store) GetLastSyncAt(ctx context.Context) (*time.Time, error) {
	var raw sql.NullString
	if err := s.db.QueryRowContext(ctx, queryGetLastSyncAt).Scan(&raw); err != nil {
		return nil, fmt.Errorf("get last sync: %w", err)
	}
	if !raw.Valid {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return nil, fmt.Errorf("parse last sync time: %w", err)
	}
	return &t, nil
}

// ── Items ─────────────────────────────────────────────────────────────────────

// ItemMeta holds the minimal fields needed for diff and metadata-backfill comparisons.
type ItemMeta struct {
	Title     string
	ImageTag  string
	SortTitle string
	MediaType string
	Year      int
}

const queryGetCurrentItems = `
SELECT jellyfin_id, title, image_tag, sort_title, media_type, year FROM items WHERE library_id = ? AND is_removed = 0`

// GetCurrentItems returns a map of jellyfin_id → ItemMeta for all non-removed
// items in a library, loaded in a single query.
func (s *Store) GetCurrentItems(ctx context.Context, libraryID string) (map[string]ItemMeta, error) {
	rows, err := s.db.QueryContext(ctx, queryGetCurrentItems, libraryID)
	if err != nil {
		return nil, fmt.Errorf("get current items: %w", err)
	}
	defer rows.Close()
	m := make(map[string]ItemMeta)
	for rows.Next() {
		var id string
		var meta ItemMeta
		if err := rows.Scan(&id, &meta.Title, &meta.ImageTag, &meta.SortTitle, &meta.MediaType, &meta.Year); err != nil {
			return nil, err
		}
		m[id] = meta
	}
	return m, rows.Err()
}

// GetCurrentItemIDs returns a set of non-removed item IDs for a library.
func (s *Store) GetCurrentItemIDs(ctx context.Context, libraryID string) (map[string]bool, error) {
	items, err := s.GetCurrentItems(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(items))
	for id := range items {
		ids[id] = true
	}
	return ids, nil
}

const queryUpsertItem = `
INSERT INTO items
  (jellyfin_id, library_id, title, sort_title, media_type, year,
   image_tag, added_at, last_seen_at, is_removed)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(jellyfin_id) DO UPDATE SET
  library_id       = excluded.library_id,
  title            = excluded.title,
  sort_title       = excluded.sort_title,
  media_type       = excluded.media_type,
  year             = excluded.year,
  image_tag        = excluded.image_tag,
  added_at         = excluded.added_at,
  last_seen_at     = excluded.last_seen_at,
  is_removed       = 0`

// UpsertItem inserts or updates an item record.
func (s *Store) UpsertItem(ctx context.Context, item Item) error {
	_, err := s.db.ExecContext(ctx, queryUpsertItem,
		item.JellyfinID,
		item.LibraryID,
		item.Title,
		item.SortTitle,
		item.MediaType,
		item.Year,
		item.ImageTag,
		item.AddedAt.UTC().Format(time.RFC3339),
		item.LastSeenAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upsert item %s: %w", item.JellyfinID, err)
	}
	return nil
}

const queryMarkRemoved = `
UPDATE items SET is_removed = 1 WHERE jellyfin_id = ?`

// MarkItemRemoved sets is_removed = 1 for an item.
func (s *Store) MarkItemRemoved(ctx context.Context, jellyfinID string) error {
	_, err := s.db.ExecContext(ctx, queryMarkRemoved, jellyfinID)
	return err
}

const queryGetItem = `
SELECT jellyfin_id, library_id, title, sort_title, media_type, year,
       image_tag, added_at, last_seen_at, is_removed
FROM items WHERE jellyfin_id = ?`

// GetItem fetches a single item by its Jellyfin ID.
func (s *Store) GetItem(ctx context.Context, jellyfinID string) (*Item, error) {
	row := s.db.QueryRowContext(ctx, queryGetItem, jellyfinID)
	return scanItem(row)
}

func scanItem(row *sql.Row) (*Item, error) {
	var it Item
	var addedAt, lastSeenAt string
	err := row.Scan(
		&it.JellyfinID, &it.LibraryID, &it.Title, &it.SortTitle,
		&it.MediaType, &it.Year, &it.ImageTag,
		&addedAt, &lastSeenAt, &it.IsRemoved,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan item: %w", err)
	}
	it.AddedAt, _ = time.Parse(time.RFC3339, addedAt)
	it.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeenAt)
	return &it, nil
}

// ── Events ────────────────────────────────────────────────────────────────────

const queryInsertEvent = `
INSERT INTO events (snapshot_id, jellyfin_id, event_type, occurred_at, title, library_name, media_type, image_tag)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// InsertEvent records a new add/remove event.
func (s *Store) InsertEvent(ctx context.Context, event Event) error {
	_, err := s.db.ExecContext(ctx, queryInsertEvent,
		event.SnapshotID,
		event.JellyfinID,
		event.EventType,
		event.OccurredAt.UTC().Format(time.RFC3339),
		event.Title,
		event.LibraryName,
		event.MediaType,
		event.ImageTag,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

const queryUpdateEventMetadata = `
UPDATE events SET title = ?, image_tag = ?
WHERE jellyfin_id = ? AND event_type = 'added'`

// UpdateEventMetadata refreshes the denormalised title and image_tag on the
// "added" event(s) for an item whose metadata improved since it was first seen.
func (s *Store) UpdateEventMetadata(ctx context.Context, jellyfinID, title, imageTag string) error {
	_, err := s.db.ExecContext(ctx, queryUpdateEventMetadata, title, imageTag, jellyfinID)
	if err != nil {
		return fmt.Errorf("update event metadata %s: %w", jellyfinID, err)
	}
	return nil
}

const queryUpdateEventJellyfinID = `
UPDATE events
SET jellyfin_id = ?,
    image_tag = CASE WHEN ? != '' THEN ? ELSE image_tag END
WHERE jellyfin_id = ?`

// UpdateEventJellyfinID updates events pointing to oldID to point to newID,
// and refreshes the image_tag if a new tag is given.
func (s *Store) UpdateEventJellyfinID(ctx context.Context, oldID, newID, newImageTag string) error {
	_, err := s.db.ExecContext(ctx, queryUpdateEventJellyfinID, newID, newImageTag, newImageTag, oldID)
	if err != nil {
		return fmt.Errorf("update event jellyfin id %s -> %s: %w", oldID, newID, err)
	}
	return nil
}

// SaveLibrarySync atomically saves a snapshot, upserts items, updates metadata/upgrades,
// marks removed items, and inserts all new events in a single SQLite transaction.
func (s *Store) SaveLibrarySync(ctx context.Context, data LibrarySyncData) ([]Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin sync tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Insert snapshot
	res, err := tx.ExecContext(ctx, queryInsertSnapshot,
		data.Snapshot.TakenAt.UTC().Format(time.RFC3339),
		data.Snapshot.LibraryID,
		data.Snapshot.LibraryName,
		data.Snapshot.ItemCount,
	)
	if err != nil {
		return nil, fmt.Errorf("insert snapshot: %w", err)
	}
	snapID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("snapshot last insert id: %w", err)
	}

	// 2. Upsert items
	if len(data.Items) > 0 {
		stmt, err := tx.PrepareContext(ctx, queryUpsertItem)
		if err != nil {
			return nil, fmt.Errorf("prepare upsert item: %w", err)
		}
		defer stmt.Close()

		for _, item := range data.Items {
			if _, err := stmt.ExecContext(ctx,
				item.JellyfinID,
				item.LibraryID,
				item.Title,
				item.SortTitle,
				item.MediaType,
				item.Year,
				item.ImageTag,
				item.AddedAt.UTC().Format(time.RFC3339),
				item.LastSeenAt.UTC().Format(time.RFC3339),
			); err != nil {
				return nil, fmt.Errorf("upsert item %s: %w", item.JellyfinID, err)
			}
		}
	}

	// 3. Metadata updates
	if len(data.MetadataUpdates) > 0 {
		stmt, err := tx.PrepareContext(ctx, queryUpdateEventMetadata)
		if err != nil {
			return nil, fmt.Errorf("prepare update event metadata: %w", err)
		}
		defer stmt.Close()

		for _, mu := range data.MetadataUpdates {
			if _, err := stmt.ExecContext(ctx, mu.Title, mu.ImageTag, mu.EventID); err != nil {
				return nil, fmt.Errorf("update event metadata %s: %w", mu.EventID, err)
			}
		}
	}

	// 4. Upgrades (re-linking events to new jellyfin id)
	if len(data.EventUpgrades) > 0 {
		stmt, err := tx.PrepareContext(ctx, queryUpdateEventJellyfinID)
		if err != nil {
			return nil, fmt.Errorf("prepare update event jellyfin id: %w", err)
		}
		defer stmt.Close()

		for _, u := range data.EventUpgrades {
			if _, err := stmt.ExecContext(ctx, u.NewTargetID, u.NewImageTag, u.NewImageTag, u.OldID); err != nil {
				return nil, fmt.Errorf("update event jellyfin id %s -> %s: %w", u.OldID, u.NewTargetID, err)
			}
		}
	}

	// 5. Mark removed (upgraded old IDs + actual removals)
	allRemovedIDs := append(data.UpgradedOldIDs, data.RemovedIDs...)
	if len(allRemovedIDs) > 0 {
		stmt, err := tx.PrepareContext(ctx, queryMarkRemoved)
		if err != nil {
			return nil, fmt.Errorf("prepare mark removed: %w", err)
		}
		defer stmt.Close()

		for _, id := range allRemovedIDs {
			if _, err := stmt.ExecContext(ctx, id); err != nil {
				return nil, fmt.Errorf("mark item removed %s: %w", id, err)
			}
		}
	}

	// 6. Insert events
	totalEvents := len(data.AddedEvents) + len(data.RemovedEvents)
	allEvents := make([]Event, 0, totalEvents)

	if totalEvents > 0 {
		stmt, err := tx.PrepareContext(ctx, queryInsertEvent)
		if err != nil {
			return nil, fmt.Errorf("prepare insert event: %w", err)
		}
		defer stmt.Close()

		for _, ev := range data.AddedEvents {
			ev.SnapshotID = snapID
			res, err := stmt.ExecContext(ctx,
				ev.SnapshotID,
				ev.JellyfinID,
				ev.EventType,
				ev.OccurredAt.UTC().Format(time.RFC3339),
				ev.Title,
				ev.LibraryName,
				ev.MediaType,
				ev.ImageTag,
			)
			if err != nil {
				return nil, fmt.Errorf("insert added event %s: %w", ev.Title, err)
			}
			ev.ID, _ = res.LastInsertId()
			allEvents = append(allEvents, ev)
		}

		for _, ev := range data.RemovedEvents {
			ev.SnapshotID = snapID
			res, err := stmt.ExecContext(ctx,
				ev.SnapshotID,
				ev.JellyfinID,
				ev.EventType,
				ev.OccurredAt.UTC().Format(time.RFC3339),
				ev.Title,
				ev.LibraryName,
				ev.MediaType,
				ev.ImageTag,
			)
			if err != nil {
				return nil, fmt.Errorf("insert removed event %s: %w", ev.Title, err)
			}
			ev.ID, _ = res.LastInsertId()
			allEvents = append(allEvents, ev)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit sync tx: %w", err)
	}

	return allEvents, nil
}




const queryGetEvent = `
SELECT id, snapshot_id, jellyfin_id, event_type, occurred_at, title, library_name, media_type, image_tag
FROM events WHERE id = ?`

// GetEvent fetches a single event by ID.
func (s *Store) GetEvent(ctx context.Context, id int64) (*Event, error) {
	row := s.db.QueryRowContext(ctx, queryGetEvent, id)
	var ev Event
	var occurredAt string
	err := row.Scan(&ev.ID, &ev.SnapshotID, &ev.JellyfinID, &ev.EventType,
		&occurredAt, &ev.Title, &ev.LibraryName, &ev.MediaType, &ev.ImageTag)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get event: %w", err)
	}
	ev.OccurredAt, _ = time.Parse(time.RFC3339, occurredAt)
	return &ev, nil
}

// ListEvents returns a paginated, filtered list of events and total count.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, int, error) {
	if f.Limit <= 0 {
		f.Limit = 25
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	if f.Page <= 0 {
		f.Page = 1
	}

	where, args := buildEventWhere(f)

	// Count query
	var total int
	countQ := "SELECT COUNT(*) FROM events" + where
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count events: %w", err)
	}

	// Data query
	offset := (f.Page - 1) * f.Limit
	dataQ := "SELECT id, snapshot_id, jellyfin_id, event_type, occurred_at, " +
		"title, library_name, media_type, image_tag FROM events" +
		where + " ORDER BY occurred_at DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, offset)

	rows, err := s.db.QueryContext(ctx, dataQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var ev Event
		var occurredAt string
		if err := rows.Scan(&ev.ID, &ev.SnapshotID, &ev.JellyfinID, &ev.EventType,
			&occurredAt, &ev.Title, &ev.LibraryName, &ev.MediaType, &ev.ImageTag); err != nil {
			return nil, 0, err
		}
		ev.OccurredAt, _ = time.Parse(time.RFC3339, occurredAt)
		events = append(events, ev)
	}
	return events, total, rows.Err()
}

func buildEventWhere(f EventFilter) (string, []any) {
	var clauses []string
	var args []any

	if f.AllowedLibraries != nil {
		if len(f.AllowedLibraries) == 0 {
			clauses = append(clauses, "1 = 0")
		} else if f.Library != "" {
			allowed := false
			for _, al := range f.AllowedLibraries {
				if al == f.Library {
					allowed = true
					break
				}
			}
			if !allowed {
				clauses = append(clauses, "1 = 0")
			} else {
				clauses = append(clauses, "library_name = ?")
				args = append(args, f.Library)
			}
		} else {
			clause, cArgs := inClause(f.AllowedLibraries)
			clauses = append(clauses, fmt.Sprintf("library_name IN (%s)", clause))
			args = append(args, cArgs...)
		}
	} else if f.Library != "" {
		clauses = append(clauses, "library_name = ?")
		args = append(args, f.Library)
	}

	if f.EventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, f.EventType)
	}
	if !f.From.IsZero() {
		clauses = append(clauses, "occurred_at >= ?")
		args = append(args, f.From.UTC().Format(time.RFC3339))
	}
	if !f.To.IsZero() {
		clauses = append(clauses, "occurred_at <= ?")
		args = append(args, f.To.UTC().Format(time.RFC3339))
	}
	if f.Query != "" {
		clauses = append(clauses, "title LIKE ?")
		args = append(args, "%"+f.Query+"%")
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// GetLibraryNames returns a distinct sorted list of all library names seen in events.
func (s *Store) GetLibraryNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT library_name FROM events ORDER BY library_name")
	if err != nil {
		return nil, fmt.Errorf("get library names: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

func inClause(items []string) (string, []any) {
	placeholders := make([]string, len(items))
	args := make([]any, len(items))
	for i, item := range items {
		placeholders[i] = "?"
		args[i] = item
	}
	return strings.Join(placeholders, ", "), args
}

// ── Stats ─────────────────────────────────────────────────────────────────────

// GetStats returns the aggregated statistics for the dashboard.
// If allowedLibraries is non-nil, statistics are scoped to those libraries.
func (s *Store) GetStats(ctx context.Context, allowedLibraries []string) (*Stats, error) {
	stats := &Stats{}

	if allowedLibraries != nil && len(allowedLibraries) == 0 {
		return stats, nil
	}

	var libClause string
	var libArgs []any
	if allowedLibraries != nil {
		libClause, libArgs = inClause(allowedLibraries)
	}

	// 1. Total items
	if allowedLibraries != nil {
		itemQ := fmt.Sprintf(`
SELECT COUNT(*) FROM items
WHERE is_removed = 0
  AND library_id IN (
    SELECT DISTINCT library_id FROM snapshots WHERE library_name IN (%s)
  )`, libClause)
		if err := s.db.QueryRowContext(ctx, itemQ, libArgs...).Scan(&stats.TotalItems); err != nil {
			return nil, fmt.Errorf("total items: %w", err)
		}
	} else {
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM items WHERE is_removed = 0",
		).Scan(&stats.TotalItems); err != nil {
			return nil, fmt.Errorf("total items: %w", err)
		}
	}

	// 2. Added / removed counts for week and month
	periodQ := `
SELECT
  COUNT(*) FILTER (WHERE event_type = 'added'   AND occurred_at >= datetime('now', '-7 days'))  AS added_week,
  COUNT(*) FILTER (WHERE event_type = 'added'   AND occurred_at >= datetime('now', '-30 days')) AS added_month,
  COUNT(*) FILTER (WHERE event_type = 'removed' AND occurred_at >= datetime('now', '-7 days'))  AS removed_week,
  COUNT(*) FILTER (WHERE event_type = 'removed' AND occurred_at >= datetime('now', '-30 days')) AS removed_month
FROM events`
	var periodArgs []any
	if allowedLibraries != nil {
		periodQ += fmt.Sprintf(" WHERE library_name IN (%s)", libClause)
		periodArgs = libArgs
	}

	if err := s.db.QueryRowContext(ctx, periodQ, periodArgs...).Scan(
		&stats.AddedThisWeek, &stats.AddedThisMonth,
		&stats.RemovedThisWeek, &stats.RemovedThisMonth,
	); err != nil {
		return nil, fmt.Errorf("period stats: %w", err)
	}

	// Last sync
	lastSync, err := s.GetLastSyncAt(ctx)
	if err != nil {
		return nil, err
	}
	stats.LastSyncAt = lastSync

	// 3. Daily chart (last 30 days)
	chartQ := `
SELECT
  date(occurred_at) AS day,
  SUM(CASE WHEN event_type = 'added'   THEN 1 ELSE 0 END) AS added,
  SUM(CASE WHEN event_type = 'removed' THEN 1 ELSE 0 END) AS removed
FROM events
WHERE occurred_at >= date('now', '-30 days')`
	var chartArgs []any
	if allowedLibraries != nil {
		chartQ += fmt.Sprintf(" AND library_name IN (%s)", libClause)
		chartArgs = libArgs
	}
	chartQ += " GROUP BY day ORDER BY day ASC"

	rows, err := s.db.QueryContext(ctx, chartQ, chartArgs...)
	if err != nil {
		return nil, fmt.Errorf("daily chart: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dc DayCount
		if err := rows.Scan(&dc.Date, &dc.Added, &dc.Removed); err != nil {
			return nil, err
		}
		stats.DailyChart = append(stats.DailyChart, dc)
	}
	return stats, rows.Err()
}

// ── Sessions ─────────────────────────────────────────────────────────────────

const queryInsertSession = `
INSERT INTO sessions (id, user_id, username, token, libraries, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  user_id = excluded.user_id,
  username = excluded.username,
  token = excluded.token,
  libraries = excluded.libraries,
  expires_at = excluded.expires_at`

// SaveSession persists a user session to SQLite.
func (s *Store) SaveSession(ctx context.Context, sess SessionRecord) error {
	libsJSON, err := json.Marshal(sess.Libraries)
	if err != nil {
		libsJSON = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, queryInsertSession,
		sess.ID,
		sess.UserID,
		sess.Username,
		sess.Token,
		string(libsJSON),
		sess.CreatedAt.UTC().Format(time.RFC3339),
		sess.ExpiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

const queryUpdateSessionLibraries = `
UPDATE sessions SET libraries = ? WHERE id = ?`

// UpdateSessionLibraries updates the cached libraries list for a session.
func (s *Store) UpdateSessionLibraries(ctx context.Context, id string, libraries []string) error {
	libsJSON, err := json.Marshal(libraries)
	if err != nil {
		libsJSON = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, queryUpdateSessionLibraries, string(libsJSON), id)
	if err != nil {
		return fmt.Errorf("update session libraries: %w", err)
	}
	return nil
}

const queryGetSession = `
SELECT id, user_id, username, token, libraries, created_at, expires_at
FROM sessions
WHERE id = ?`

// GetSession retrieves a session by its ID. Returns nil, nil if not found.
func (s *Store) GetSession(ctx context.Context, id string) (*SessionRecord, error) {
	var rec SessionRecord
	var libsJSON string
	var createdStr, expiresStr string
	err := s.db.QueryRowContext(ctx, queryGetSession, id).Scan(
		&rec.ID,
		&rec.UserID,
		&rec.Username,
		&rec.Token,
		&libsJSON,
		&createdStr,
		&expiresStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	if libsJSON != "" {
		_ = json.Unmarshal([]byte(libsJSON), &rec.Libraries)
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	rec.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
	return &rec, nil
}

const queryDeleteSession = `
DELETE FROM sessions WHERE id = ?`

// DeleteSession removes a session from SQLite.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, queryDeleteSession, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

const queryListActiveSessions = `
SELECT id, user_id, username, token, libraries, created_at, expires_at
FROM sessions
WHERE expires_at > ?`

// ListActiveSessions returns all sessions that have not yet expired.
func (s *Store) ListActiveSessions(ctx context.Context) ([]SessionRecord, error) {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, queryListActiveSessions, nowStr)
	if err != nil {
		return nil, fmt.Errorf("list active sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionRecord
	for rows.Next() {
		var rec SessionRecord
		var libsJSON, createdStr, expiresStr string
		if err := rows.Scan(
			&rec.ID,
			&rec.UserID,
			&rec.Username,
			&rec.Token,
			&libsJSON,
			&createdStr,
			&expiresStr,
		); err != nil {
			return nil, err
		}
		if libsJSON != "" {
			_ = json.Unmarshal([]byte(libsJSON), &rec.Libraries)
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		rec.ExpiresAt, _ = time.Parse(time.RFC3339, expiresStr)
		sessions = append(sessions, rec)
	}
	return sessions, rows.Err()
}

const queryDeleteExpiredSessions = `
DELETE FROM sessions WHERE expires_at <= ?`

// DeleteExpiredSessions cleans up any expired sessions.
func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, queryDeleteExpiredSessions, nowStr)
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

