package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/config"
	"github.com/eiffelbeef/jelly-diff/internal/jellyfin"
	"github.com/eiffelbeef/jelly-diff/internal/notify"
	"github.com/eiffelbeef/jelly-diff/internal/storage"
)

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSyncUpgradeSameYear(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Pre-seed an existing item and event in DB
	err := store.UpsertItem(ctx, storage.Item{
		JellyfinID: "old-1080p",
		LibraryID:  "lib1",
		Title:      "Halloween",
		MediaType:  "Movie",
		Year:       1978,
		LastSeenAt: time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}
	snapID, _ := store.InsertSnapshot(ctx, storage.Snapshot{
		TakenAt:     time.Now().Add(-2 * time.Hour),
		LibraryID:   "lib1",
		LibraryName: "Movies",
		ItemCount:   1,
	})
	_ = store.InsertEvent(ctx, storage.Event{
		SnapshotID:  snapID,
		JellyfinID:  "old-1080p",
		EventType:   "added",
		OccurredAt:  time.Now().Add(-2 * time.Hour),
		Title:       "Halloween",
		LibraryName: "Movies",
		MediaType:   "Movie",
		ImageTag:    "old-tag",
	})

	// Jellyfin mock returns a new ID for the same movie with the same year (upgrade)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib1", "Name": "Movies", "CollectionType": "movies"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{
						"Id":             "new-4k",
						"Name":           "Halloween",
						"Type":           "Movie",
						"ProductionYear": 1978,
						"ImageTags":      map[string]any{"Primary": "new-4k-tag"},
					},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	events, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib1", Name: "Movies"})
	if err != nil {
		t.Fatalf("syncLibrary failed: %v", err)
	}

	// Should be detected as an upgrade: 0 events emitted
	if len(events) != 0 {
		t.Fatalf("expected 0 events for upgrade, got %d: %+v", len(events), events)
	}

	// Verify old item is marked removed
	oldItem, err := store.GetItem(ctx, "old-1080p")
	if err != nil || oldItem == nil || !oldItem.IsRemoved {
		t.Fatalf("expected old item to be marked removed, got: %+v (err: %v)", oldItem, err)
	}

	// Verify new item exists and is not removed
	newItem, err := store.GetItem(ctx, "new-4k")
	if err != nil || newItem == nil || newItem.IsRemoved {
		t.Fatalf("expected new item to exist and not be removed, got: %+v (err: %v)", newItem, err)
	}

	// Verify existing event's JellyfinID and ImageTag were updated to the new item!
	evs, _, err := store.ListEvents(ctx, storage.EventFilter{Limit: 10})
	if err != nil || len(evs) != 1 {
		t.Fatalf("expected 1 event in store, got %d (err: %v)", len(evs), err)
	}
	if evs[0].JellyfinID != "new-4k" {
		t.Fatalf("expected event JellyfinID to be updated to new-4k, got: %s", evs[0].JellyfinID)
	}
	if evs[0].ImageTag != "new-4k-tag" {
		t.Fatalf("expected event ImageTag to be updated to new-4k-tag, got: %s", evs[0].ImageTag)
	}
}

func TestSyncDifferentYearNotAnUpgrade(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Pre-seed an existing item in DB: Halloween (1978)
	err := store.UpsertItem(ctx, storage.Item{
		JellyfinID: "halloween-1978",
		LibraryID:  "lib1",
		Title:      "Halloween",
		MediaType:  "Movie",
		Year:       1978,
		LastSeenAt: time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	// Jellyfin mock returns Halloween (2018) instead of 1978 (different year)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib1", "Name": "Movies", "CollectionType": "movies"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{
						"Id":             "halloween-2018",
						"Name":           "Halloween",
						"Type":           "Movie",
						"ProductionYear": 2018,
					},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	events, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib1", Name: "Movies"})
	if err != nil {
		t.Fatalf("syncLibrary failed: %v", err)
	}

	// Because the years differ (1978 vs 2018), this must NOT be suppressed as an upgrade:
	// exactly 2 events: 1 added (2018) and 1 removed (1978)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 added, 1 removed), got %d: %+v", len(events), events)
	}
}

func TestSyncUpgradeEpisode(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Pre-seed an episode in DB with ep key stored in SortTitle (including year)
	err := store.UpsertItem(ctx, storage.Item{
		JellyfinID: "ep-old",
		LibraryID:  "lib2",
		Title:      "Pilot",
		SortTitle:  "ep|breaking bad|1|1|2008",
		MediaType:  "Episode",
		Year:       2008,
		LastSeenAt: time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	// Jellyfin mock returns upgraded episode with a new ID and same year
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib2", "Name": "TV Shows", "CollectionType": "tvshows"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{
						"Id":                "ep-new-4k",
						"Name":              "Pilot",
						"Type":              "Episode",
						"SeriesId":          "series1",
						"SeriesName":        "Breaking Bad",
						"SeasonId":          "season1",
						"SeasonName":        "Season 1",
						"ParentIndexNumber": 1,
						"IndexNumber":       1,
						"ProductionYear":    2008,
					},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	events, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib2", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("syncLibrary failed: %v", err)
	}

	// Should be detected as an upgrade: 0 events emitted
	if len(events) != 0 {
		t.Fatalf("expected 0 events for episode upgrade, got %d: %+v", len(events), events)
	}

	// Verify old item is marked removed
	oldItem, err := store.GetItem(ctx, "ep-old")
	if err != nil || oldItem == nil || !oldItem.IsRemoved {
		t.Fatalf("expected old episode to be marked removed: %+v", oldItem)
	}

	// Verify new item exists and is not removed
	newItem, err := store.GetItem(ctx, "ep-new-4k")
	if err != nil || newItem == nil || newItem.IsRemoved {
		t.Fatalf("expected new episode to exist and not be removed: %+v", newItem)
	}
}

func TestSyncDifferentYearEpisodeNotAnUpgrade(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Pre-seed an episode with 1959
	err := store.UpsertItem(ctx, storage.Item{
		JellyfinID: "ep-1959",
		LibraryID:  "lib2",
		Title:      "Where Is Everybody?",
		SortTitle:  "ep|the twilight zone|1|1|1959",
		MediaType:  "Episode",
		Year:       1959,
		LastSeenAt: time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	// Mock returns an episode with the same title/series/season/ep but different year (2019 reboot)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib2", "Name": "TV Shows", "CollectionType": "tvshows"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{
						"Id":                "ep-2019",
						"Name":              "Where Is Everybody?",
						"Type":              "Episode",
						"SeriesId":          "series-tz",
						"SeriesName":        "The Twilight Zone",
						"SeasonId":          "season-tz-1",
						"SeasonName":        "Season 1",
						"ParentIndexNumber": 1,
						"IndexNumber":       1,
						"ProductionYear":    2019,
					},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	events, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib2", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("syncLibrary failed: %v", err)
	}

	// Different year -> not an upgrade, 2 events emitted (1 added, 1 removed)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
}

func TestSyncImagesSeriesPosterAndAlbumCover(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib-tv", "Name": "TV Shows", "CollectionType": "tvshows"},
					{"Id": "lib-music", "Name": "Music", "CollectionType": "music"},
				},
			})
		case "/Users/admin/Items":
			if r.URL.Query().Get("ParentId") == "lib-tv" {
				json.NewEncoder(w).Encode(map[string]any{
					"Items": []map[string]any{
						{
							"Id":                    "ep1",
							"Name":                  "Pilot",
							"Type":                  "Episode",
							"SeriesId":              "series-123",
							"SeriesName":            "Severance",
							"SeriesPrimaryImageTag": "series-poster-tag-xyz",
							"SeasonId":              "season-1",
							"SeasonName":            "Season 1",
							"ParentIndexNumber":     1,
							"IndexNumber":           1,
							"ProductionYear":        2022,
							"ImageTags":             map[string]any{"Primary": "ep-screenshot-tag-123"},
						},
					},
					"TotalRecordCount": 1,
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"Items": []map[string]any{
						{
							"Id":                   "track1",
							"Name":                 "Song 1",
							"Type":                 "Audio",
							"AlbumId":              "album-456",
							"Album":                "Album Title",
							"AlbumPrimaryImageTag": "album-cover-tag-789",
							"ProductionYear":       2023,
							"ImageTags":            map[string]any{"Primary": "track-tag-000"},
						},
					},
					"TotalRecordCount": 1,
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	// 1. Sync TV library
	tvEvents, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-tv", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("sync TV failed: %v", err)
	}
	if len(tvEvents) != 1 {
		t.Fatalf("expected 1 TV event, got %d", len(tvEvents))
	}
	// Verify it used the series ID and series poster image tag, NOT the episode screenshot!
	if tvEvents[0].JellyfinID != "series-123" {
		t.Errorf("expected TV event JellyfinID to be series ID 'series-123', got %q", tvEvents[0].JellyfinID)
	}
	if tvEvents[0].ImageTag != "series-poster-tag-xyz" {
		t.Errorf("expected TV event ImageTag to be series poster tag 'series-poster-tag-xyz', got %q", tvEvents[0].ImageTag)
	}

	// 2. Sync Music library
	musicEvents, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-music", Name: "Music"})
	if err != nil {
		t.Fatalf("sync Music failed: %v", err)
	}
	if len(musicEvents) != 1 {
		t.Fatalf("expected 1 Music event, got %d", len(musicEvents))
	}
	// Verify it used the album ID and album cover image tag, NOT the track tag!
	if musicEvents[0].JellyfinID != "album-456" {
		t.Errorf("expected Music event JellyfinID to be album ID 'album-456', got %q", musicEvents[0].JellyfinID)
	}
	if musicEvents[0].ImageTag != "album-cover-tag-789" {
		t.Errorf("expected Music event ImageTag to be album cover tag 'album-cover-tag-789', got %q", musicEvents[0].ImageTag)
	}
}

func TestSyncShowAndAlbumTitlesAndSearch(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	var tvItems []map[string]any
	var musicItems []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib-tv", "Name": "TV Shows", "CollectionType": "tvshows"},
					{"Id": "lib-music", "Name": "Music", "CollectionType": "music"},
				},
			})
		case "/Users/admin/Items":
			if r.URL.Query().Get("ParentId") == "lib-tv" {
				json.NewEncoder(w).Encode(map[string]any{
					"Items":            tvItems,
					"TotalRecordCount": len(tvItems),
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"Items":            musicItems,
					"TotalRecordCount": len(musicItems),
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	// Pre-populate items
	tvItems = []map[string]any{
		{
			"Id":                    "ep-succ-1",
			"Name":                  "Lifeboats",
			"Type":                  "Episode",
			"SeriesId":              "series-succ",
			"SeriesName":            "Succession",
			"SeriesPrimaryImageTag": "series-img-tag",
			"SeasonId":              "season-succ-1",
			"SeasonName":            "Season 1",
			"ParentIndexNumber":     1,
			"IndexNumber":           3,
			"ProductionYear":        2018,
			"ImageTags":             map[string]any{"Primary": "ep-img-tag"},
		},
	}
	musicItems = []map[string]any{
		{
			"Id":                   "track-dreams",
			"Name":                 "Dreams",
			"Type":                 "Audio",
			"AlbumId":              "album-rumours",
			"Album":                "Rumours",
			"AlbumPrimaryImageTag": "album-img-tag",
			"ProductionYear":       1977,
			"ImageTags":            map[string]any{"Primary": "track-img-tag"},
		},
	}

	// 1. Initial sync
	tvEvs, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-tv", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("sync TV failed: %v", err)
	}
	if len(tvEvs) != 1 {
		t.Fatalf("expected 1 TV event, got %d", len(tvEvs))
	}
	expectedTVTitle := "Succession — S01E03 — Lifeboats"
	if tvEvs[0].Title != expectedTVTitle {
		t.Errorf("expected TV title %q, got %q", expectedTVTitle, tvEvs[0].Title)
	}

	musicEvs, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-music", Name: "Music"})
	if err != nil {
		t.Fatalf("sync Music failed: %v", err)
	}
	if len(musicEvs) != 1 {
		t.Fatalf("expected 1 Music event, got %d", len(musicEvs))
	}
	expectedMusicTitle := "Rumours — Dreams"
	if musicEvs[0].Title != expectedMusicTitle {
		t.Errorf("expected Music title %q, got %q", expectedMusicTitle, musicEvs[0].Title)
	}

	// 2. Search queries
	// Search for series name "Succession"
	results, count, err := store.ListEvents(ctx, storage.EventFilter{Query: "Succession"})
	if err != nil {
		t.Fatalf("search Succession failed: %v", err)
	}
	if count != 1 || len(results) != 1 || results[0].Title != expectedTVTitle {
		t.Errorf("expected 1 result for 'Succession', got count=%d, results=%+v", count, results)
	}

	// Search for episode title "Lifeboats"
	results, count, err = store.ListEvents(ctx, storage.EventFilter{Query: "Lifeboats"})
	if err != nil {
		t.Fatalf("search Lifeboats failed: %v", err)
	}
	if count != 1 || len(results) != 1 {
		t.Errorf("expected 1 result for 'Lifeboats', got count=%d", count)
	}

	// Search for season/episode code "S01E03"
	results, count, err = store.ListEvents(ctx, storage.EventFilter{Query: "S01E03"})
	if err != nil {
		t.Fatalf("search S01E03 failed: %v", err)
	}
	if count != 1 || len(results) != 1 {
		t.Errorf("expected 1 result for 'S01E03', got count=%d", count)
	}

	// Search for album name "Rumours"
	results, count, err = store.ListEvents(ctx, storage.EventFilter{Query: "Rumours"})
	if err != nil {
		t.Fatalf("search Rumours failed: %v", err)
	}
	if count != 1 || len(results) != 1 || results[0].Title != expectedMusicTitle {
		t.Errorf("expected 1 result for 'Rumours', got count=%d, results=%+v", count, results)
	}

	// Search for song title "Dreams"
	results, count, err = store.ListEvents(ctx, storage.EventFilter{Query: "Dreams"})
	if err != nil {
		t.Fatalf("search Dreams failed: %v", err)
	}
	if count != 1 || len(results) != 1 {
		t.Errorf("expected 1 result for 'Dreams', got count=%d", count)
	}

	// Search for nonexistent query
	results, count, err = store.ListEvents(ctx, storage.EventFilter{Query: "NonexistentQuery"})
	if err != nil {
		t.Fatalf("search nonexistent failed: %v", err)
	}
	if count != 0 || len(results) != 0 {
		t.Errorf("expected 0 results, got count=%d", count)
	}

	// 3. Removal: clear items and sync again
	tvItems = []map[string]any{}
	musicItems = []map[string]any{}

	remTVEvs, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-tv", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("sync TV removals failed: %v", err)
	}
	if len(remTVEvs) != 1 {
		t.Fatalf("expected 1 TV removal event, got %d", len(remTVEvs))
	}
	if remTVEvs[0].EventType != "removed" || remTVEvs[0].Title != expectedTVTitle {
		t.Errorf("expected removal event with title %q, got %+v", expectedTVTitle, remTVEvs[0])
	}

	remMusicEvs, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-music", Name: "Music"})
	if err != nil {
		t.Fatalf("sync Music removals failed: %v", err)
	}
	if len(remMusicEvs) != 1 {
		t.Fatalf("expected 1 Music removal event, got %d", len(remMusicEvs))
	}
	if remMusicEvs[0].EventType != "removed" || remMusicEvs[0].Title != expectedMusicTitle {
		t.Errorf("expected removal event with title %q, got %+v", expectedMusicTitle, remMusicEvs[0])
	}

	// Search series name should now find 2 events (added + removed)
	results, count, err = store.ListEvents(ctx, storage.EventFilter{Query: "Succession"})
	if err != nil {
		t.Fatalf("search Succession after removal failed: %v", err)
	}
	if count != 2 || len(results) != 2 {
		t.Errorf("expected 2 events for 'Succession' (added+removed), got count=%d", count)
	}
}

func TestSyncMetadataBackfill(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// currentItems controls what the mock returns.
	var currentItems []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib1", "Name": "Movies", "CollectionType": "movies"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items":            currentItems,
				"TotalRecordCount": len(currentItems),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	// ── Sync 1: Jellyfin returns the file before metadata is ready ──────────────
	currentItems = []map[string]any{
		{
			"Id":             "movie-1",
			"Name":           "some.mkv",        // filename, no real title yet
			"Type":           "Movie",
			"ProductionYear": 0,                 // year not yet known
			"ImageTags":      map[string]any{},  // no poster yet
		},
	}

	evs1, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib1", Name: "Movies"})
	if err != nil {
		t.Fatalf("sync 1 failed: %v", err)
	}
	if len(evs1) != 1 || evs1[0].Title != "some.mkv" {
		t.Fatalf("sync 1: expected added event with filename title, got %+v", evs1)
	}

	// Verify the event has the filename
	eventsAfterSync1, _, _ := store.ListEvents(ctx, storage.EventFilter{})
	if len(eventsAfterSync1) != 1 || eventsAfterSync1[0].Title != "some.mkv" {
		t.Fatalf("expected event title 'some.mkv', got %q", eventsAfterSync1[0].Title)
	}

	// ── Sync 2: Jellyfin now has the real metadata ──────────────────────────────
	currentItems = []map[string]any{
		{
			"Id":             "movie-1",
			"Name":           "Alien",
			"Type":           "Movie",
			"ProductionYear": 1979,
			"ImageTags":      map[string]any{"Primary": "poster-tag-abc"},
		},
	}

	evs2, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib1", Name: "Movies"})
	if err != nil {
		t.Fatalf("sync 2 failed: %v", err)
	}
	// No new add/remove events — the item is already tracked
	if len(evs2) != 0 {
		t.Fatalf("sync 2: expected 0 events, got %d: %+v", len(evs2), evs2)
	}

	// The existing event should now have the real title and image tag
	eventsAfterSync2, _, _ := store.ListEvents(ctx, storage.EventFilter{})
	if len(eventsAfterSync2) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(eventsAfterSync2))
	}
	if eventsAfterSync2[0].Title != "Alien" {
		t.Errorf("expected event title 'Alien', got %q", eventsAfterSync2[0].Title)
	}
	if eventsAfterSync2[0].ImageTag != "poster-tag-abc" {
		t.Errorf("expected event image_tag 'poster-tag-abc', got %q", eventsAfterSync2[0].ImageTag)
	}
}

func TestWorkerTriggerRateLimitingAndCooldown(t *testing.T) {
	store := openTestStore(t)
	cfg := &config.Config{
		SyncInterval:    time.Hour,
		MinSyncInterval: 100 * time.Millisecond,
	}
	worker := NewWorker(cfg, nil, store, notify.NoOp{})

	// 1. When worker is not running
	res, _ := worker.TriggerSync()
	if res != TriggerUnavailable {
		t.Fatalf("expected TriggerUnavailable when worker is not running, got %v", res)
	}

	// 2. Mark worker running
	worker.SetRunning(true)

	// Simulate a sync that just finished now
	worker.SetLastSyncFinished(time.Now())
	res, remaining := worker.TriggerSync()
	if res != TriggerCooldown {
		t.Fatalf("expected TriggerCooldown, got %v", res)
	}
	if remaining <= 0 || remaining > 100*time.Millisecond {
		t.Fatalf("expected remaining duration between 0 and 100ms, got %v", remaining)
	}

	// 3. Simulate sync in progress
	worker.mu.Lock()
	worker.isSyncing = true
	worker.mu.Unlock()

	res, _ = worker.TriggerSync()
	if res != TriggerAlreadyRunning {
		t.Fatalf("expected TriggerAlreadyRunning, got %v", res)
	}

	worker.mu.Lock()
	worker.isSyncing = false
	worker.mu.Unlock()

	// 4. Cooldown expired
	worker.SetLastSyncFinished(time.Now().Add(-200 * time.Millisecond))
	res, rem := worker.TriggerSync()
	if res != TriggerAccepted {
		t.Fatalf("expected TriggerAccepted after cooldown, got %v", res)
	}
	if rem != 0 {
		t.Fatalf("expected 0 remaining duration, got %v", rem)
	}
	if !worker.IsSyncing() {
		t.Fatalf("expected worker to be marked isSyncing=true after trigger accepted")
	}

	// Immediate next trigger while syncing should be rejected as already running
	res, _ = worker.TriggerSync()
	if res != TriggerAlreadyRunning {
		t.Fatalf("expected immediate consecutive trigger to return TriggerAlreadyRunning, got %v", res)
	}
}

func TestSyncNewSeriesDoesNotEmitDuplicateSeriesEvent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Jellyfin mock returns both the Series object and 8 Episode objects (typical Jellyfin response)
	var currentItems []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib-tv", "Name": "TV Shows"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items":            currentItems,
				"TotalRecordCount": len(currentItems),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	cfg := &config.Config{SyncInterval: time.Hour}
	worker := NewWorker(cfg, client, store, notify.NoOp{})

	// 1. Initial addition: 1 Series item + 8 Episode items for Season 1
	currentItems = []map[string]any{
		{
			"Id":             "series-carrie",
			"Name":           "Carrie",
			"Type":           "Series",
			"ProductionYear": 2026,
			"ImageTags":      map[string]any{"Primary": "poster-carrie"},
		},
	}
	for i := 1; i <= 8; i++ {
		currentItems = append(currentItems, map[string]any{
			"Id":                    fmt.Sprintf("ep-%d", i),
			"Name":                  fmt.Sprintf("Episode %d", i),
			"Type":                  "Episode",
			"SeriesId":              "series-carrie",
			"SeriesName":            "Carrie",
			"SeasonId":              "season-1",
			"SeasonName":            "Season 1",
			"ParentIndexNumber":     1,
			"IndexNumber":           i,
			"ProductionYear":        2026,
			"SeriesPrimaryImageTag": "poster-carrie",
		})
	}

	evs, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-tv", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("sync TV library failed: %v", err)
	}

	// Must produce exactly ONE event: "Carrie — Season 1 (8 episodes)"
	// NOT two events (one for "Carrie" and one for "Carrie — Season 1 (8 episodes)")
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event, got %d: %+v", len(evs), evs)
	}
	expectedTitle := "Carrie — Season 1 (8 episodes)"
	if evs[0].Title != expectedTitle {
		t.Errorf("expected title %q, got %q", expectedTitle, evs[0].Title)
	}
	if evs[0].MediaType != "Episode" {
		t.Errorf("expected media type Episode, got %q", evs[0].MediaType)
	}
	if evs[0].JellyfinID != "series-carrie" {
		t.Errorf("expected JellyfinID series-carrie, got %q", evs[0].JellyfinID)
	}

	// Verify database events list
	dbEvents, total, err := store.ListEvents(ctx, storage.EventFilter{})
	if err != nil {
		t.Fatalf("list events failed: %v", err)
	}
	if total != 1 || len(dbEvents) != 1 {
		t.Fatalf("expected 1 event in store, got %d", total)
	}
	if dbEvents[0].Title != expectedTitle {
		t.Errorf("expected db event title %q, got %q", expectedTitle, dbEvents[0].Title)
	}

	// 2. Removal: delete the show and all episodes
	currentItems = []map[string]any{}
	remEvs, err := worker.syncLibrary(ctx, jellyfin.Library{ID: "lib-tv", Name: "TV Shows"})
	if err != nil {
		t.Fatalf("sync removal failed: %v", err)
	}
	// Verify that no removal event was created for the "Series" container item
	for _, rev := range remEvs {
		if rev.MediaType == "Series" {
			t.Errorf("expected no Series removal event, but got %+v", rev)
		}
	}
}

func TestWorkerSyncSeqAndLastSyncEvents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users":
			json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "admin", "Policy": map[string]any{"IsAdministrator": true}},
			})
		case "/Users/admin/Views":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib1", "Name": "Movies", "CollectionType": "movies"},
				},
			})
		case "/Users/admin/Items":
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{
						"Id":             "m1",
						"Name":           "Movie 1",
						"Type":           "Movie",
						"ProductionYear": 2021,
					},
					{
						"Id":             "m2",
						"Name":           "Movie 2",
						"Type":           "Movie",
						"ProductionYear": 2022,
					},
				},
				"TotalRecordCount": 2,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := jellyfin.NewClient(srv.URL, "testkey")
	worker := NewWorker(&config.Config{SyncInterval: time.Hour}, client, store, notify.NoOp{})

	if worker.SyncSeq() != 1 {
		t.Fatalf("expected initial SyncSeq 1, got %d", worker.SyncSeq())
	}
	if worker.LastSyncEvents() != 0 {
		t.Fatalf("expected initial LastSyncEvents 0, got %d", worker.LastSyncEvents())
	}

	worker.runSync(ctx)

	if worker.SyncSeq() != 2 {
		t.Fatalf("expected SyncSeq 2 after runSync, got %d", worker.SyncSeq())
	}
	if worker.LastSyncEvents() != 2 {
		t.Fatalf("expected LastSyncEvents 2, got %d", worker.LastSyncEvents())
	}
}








