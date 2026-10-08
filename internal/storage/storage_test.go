package storage

import (
	"context"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestInsertAndListEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	snap := Snapshot{
		TakenAt:     time.Now(),
		LibraryID:   "lib1",
		LibraryName: "Movies",
		ItemCount:   1,
	}
	snapshotID, err := s.InsertSnapshot(ctx, snap)
	if err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}

	ev := Event{
		SnapshotID:  snapshotID,
		JellyfinID:  "item1",
		EventType:   "added",
		OccurredAt:  time.Now(),
		Title:       "The Matrix",
		LibraryName: "Movies",
		MediaType:   "Movie",
	}
	if err := s.InsertEvent(ctx, ev); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	events, total, err := s.ListEvents(ctx, EventFilter{Page: 1, Limit: 25})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if len(events) != 1 || events[0].Title != "The Matrix" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestUpsertItemAndGetCurrentIDs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	item := Item{
		JellyfinID:  "item1",
		LibraryID:   "lib1",
		Title:       "Inception",
		LastSeenAt:  time.Now(),
	}
	if err := s.UpsertItem(ctx, item); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	ids, err := s.GetCurrentItemIDs(ctx, "lib1")
	if err != nil {
		t.Fatalf("get current ids: %v", err)
	}
	if !ids["item1"] {
		t.Fatal("expected item1 in current IDs")
	}

	// Mark removed
	if err := s.MarkItemRemoved(ctx, "item1"); err != nil {
		t.Fatalf("mark removed: %v", err)
	}
	ids, err = s.GetCurrentItemIDs(ctx, "lib1")
	if err != nil {
		t.Fatalf("get current ids after removal: %v", err)
	}
	if ids["item1"] {
		t.Fatal("item1 should not be in current IDs after removal")
	}
}

func TestListEventsAndStatsWithAllowedLibraries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	snapID1, _ := s.InsertSnapshot(ctx, Snapshot{TakenAt: now, LibraryID: "lib-mov", LibraryName: "Movies", ItemCount: 1})
	snapID2, _ := s.InsertSnapshot(ctx, Snapshot{TakenAt: now, LibraryID: "lib-ani", LibraryName: "Anime", ItemCount: 1})

	_ = s.UpsertItem(ctx, Item{JellyfinID: "m1", LibraryID: "lib-mov", Title: "Inception", LastSeenAt: now})
	_ = s.UpsertItem(ctx, Item{JellyfinID: "a1", LibraryID: "lib-ani", Title: "Naruto", LastSeenAt: now})

	_ = s.InsertEvent(ctx, Event{SnapshotID: snapID1, JellyfinID: "m1", EventType: "added", OccurredAt: now, Title: "Inception", LibraryName: "Movies"})
	_ = s.InsertEvent(ctx, Event{SnapshotID: snapID2, JellyfinID: "a1", EventType: "added", OccurredAt: now, Title: "Naruto", LibraryName: "Anime"})

	// 1. Unrestricted
	evs, total, err := s.ListEvents(ctx, EventFilter{})
	if err != nil || total != 2 || len(evs) != 2 {
		t.Fatalf("expected 2 total events unrestricted, got total=%d len=%d", total, len(evs))
	}

	// 2. Restricted to Movies
	evs, total, err = s.ListEvents(ctx, EventFilter{AllowedLibraries: []string{"Movies"}})
	if err != nil || total != 1 || len(evs) != 1 || evs[0].Title != "Inception" {
		t.Fatalf("expected only Inception for Movies, got total=%d, evs=%+v", total, evs)
	}

	// 3. Restricted to Anime
	evs, total, err = s.ListEvents(ctx, EventFilter{AllowedLibraries: []string{"Anime"}})
	if err != nil || total != 1 || len(evs) != 1 || evs[0].Title != "Naruto" {
		t.Fatalf("expected only Naruto for Anime, got total=%d, evs=%+v", total, evs)
	}

	// 4. Restricted to empty
	evs, total, err = s.ListEvents(ctx, EventFilter{AllowedLibraries: []string{}})
	if err != nil || total != 0 || len(evs) != 0 {
		t.Fatalf("expected 0 events for empty allowed list, got total=%d", total)
	}

	// 5. Tampering: user allowed Movies but requests Anime
	evs, total, err = s.ListEvents(ctx, EventFilter{Library: "Anime", AllowedLibraries: []string{"Movies"}})
	if err != nil || total != 0 || len(evs) != 0 {
		t.Fatalf("expected 0 events when filtering for unauthorized library, got total=%d", total)
	}

	// 6. Stats scoped to Movies
	stats, err := s.GetStats(ctx, []string{"Movies"})
	if err != nil {
		t.Fatalf("GetStats error: %v", err)
	}
	if stats.TotalItems != 1 {
		t.Errorf("expected 1 total item for Movies stats, got %d", stats.TotalItems)
	}
	if stats.AddedThisWeek != 1 {
		t.Errorf("expected 1 added this week for Movies stats, got %d", stats.AddedThisWeek)
	}
}

func TestSessionStorageCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	sess := SessionRecord{
		ID:        "sess-123",
		UserID:    "user-1",
		Username:  "alice",
		Token:     "tok-abc",
		Libraries: []string{"Movies", "TV Shows"},
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}

	// 1. Save session
	if err := s.SaveSession(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	// 2. Get session
	got, err := s.GetSession(ctx, "sess-123")
	if err != nil || got == nil {
		t.Fatalf("get session failed: %v", err)
	}
	if got.Username != "alice" || got.Token != "tok-abc" || len(got.Libraries) != 2 {
		t.Errorf("unexpected retrieved session: %+v", got)
	}

	// 3. Update libraries
	if err := s.UpdateSessionLibraries(ctx, "sess-123", []string{"Movies"}); err != nil {
		t.Fatalf("update session libraries: %v", err)
	}
	got, err = s.GetSession(ctx, "sess-123")
	if err != nil || got == nil || len(got.Libraries) != 1 || got.Libraries[0] != "Movies" {
		t.Errorf("expected updated libraries: %+v", got)
	}

	// 4. List active sessions
	active, err := s.ListActiveSessions(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("expected 1 active session, got %d (err: %v)", len(active), err)
	}

	// 5. Expired sessions
	expiredSess := SessionRecord{
		ID:        "sess-expired",
		UserID:    "user-2",
		Username:  "bob",
		Token:     "tok-expired",
		Libraries: []string{},
		CreatedAt: now.Add(-48 * time.Hour),
		ExpiresAt: now.Add(-24 * time.Hour),
	}
	_ = s.SaveSession(ctx, expiredSess)

	if err := s.DeleteExpiredSessions(ctx); err != nil {
		t.Fatalf("delete expired sessions: %v", err)
	}
	gotExpired, err := s.GetSession(ctx, "sess-expired")
	if err != nil || gotExpired != nil {
		t.Errorf("expected expired session to be deleted, got: %+v", gotExpired)
	}

	// 6. Delete session
	if err := s.DeleteSession(ctx, "sess-123"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	gotDeleted, err := s.GetSession(ctx, "sess-123")
	if err != nil || gotDeleted != nil {
		t.Errorf("expected deleted session to be nil, got: %+v", gotDeleted)
	}
}

func TestSaveLibrarySync(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	data := LibrarySyncData{
		Snapshot: Snapshot{
			TakenAt:     now,
			LibraryID:   "lib-sync",
			LibraryName: "Movies",
			ItemCount:   2,
		},
		Items: []Item{
			{JellyfinID: "item-1", LibraryID: "lib-sync", Title: "Movie 1", LastSeenAt: now},
			{JellyfinID: "item-2", LibraryID: "lib-sync", Title: "Movie 2", LastSeenAt: now},
		},
		AddedEvents: []Event{
			{JellyfinID: "item-1", EventType: "added", OccurredAt: now, Title: "Movie 1", LibraryName: "Movies"},
			{JellyfinID: "item-2", EventType: "added", OccurredAt: now, Title: "Movie 2", LibraryName: "Movies"},
		},
	}

	evs, err := s.SaveLibrarySync(ctx, data)
	if err != nil {
		t.Fatalf("SaveLibrarySync failed: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected 2 events returned, got %d", len(evs))
	}
	if evs[0].SnapshotID == 0 || evs[1].SnapshotID == 0 {
		t.Errorf("expected valid SnapshotID on returned events, got %d, %d", evs[0].SnapshotID, evs[1].SnapshotID)
	}

	// Verify items and events in DB
	items, err := s.GetCurrentItems(ctx, "lib-sync")
	if err != nil || len(items) != 2 {
		t.Fatalf("expected 2 current items, got %d (err: %v)", len(items), err)
	}
	list, total, err := s.ListEvents(ctx, EventFilter{Library: "Movies"})
	if err != nil || total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 listed events, got total=%d len=%d", total, len(list))
	}
}

