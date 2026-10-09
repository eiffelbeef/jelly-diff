package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/config"
	"github.com/eiffelbeef/jelly-diff/internal/jellyfin"
	"github.com/eiffelbeef/jelly-diff/internal/notify"
	"github.com/eiffelbeef/jelly-diff/internal/storage"
	syncworker "github.com/eiffelbeef/jelly-diff/internal/sync"
	"github.com/eiffelbeef/jelly-diff/web"
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

func setupTestServer(t *testing.T) (*Server, *storage.Store, *httptest.Server) {
	t.Helper()
	store := openTestStore(t)

	mockJellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["Username"] == "invalid" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"User": map[string]any{
					"Id":   "user-123",
					"Name": req["Username"],
				},
				"AccessToken": "test-user-token-456",
			})
		case strings.HasPrefix(r.URL.Path, "/Users/user-123/Views"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"Id": "lib1", "Name": "Movies"},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/Users/user-123/Items/movie-1"):
			auth := r.Header.Get("Authorization")
			if !strings.Contains(auth, `Token="test-user-token-456"`) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jellyfin.Item{
				ID:              "movie-1",
				Name:            "Inception",
				ProductionYear:  2010,
				Overview:        "A dream within a dream.",
				Genres:          []string{"Action", "Sci-Fi"},
				CommunityRating: 8.8,
			})
		case strings.HasPrefix(r.URL.Path, "/Items/movie-1/Images/Primary"):
			auth := r.Header.Get("Authorization")
			if !strings.Contains(auth, `Token="test-user-token-456"`) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-jpeg-data"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { mockJellyfin.Close() })

	cfg := &config.Config{
		JellyfinURL: mockJellyfin.URL,
	}
	client := jellyfin.NewClient(mockJellyfin.URL, "server-api-key")
	srv := New(cfg, store, nil, client, web.FS)
	return srv, store, mockJellyfin
}

func TestUnauthenticatedRedirect(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// GET / redirects to /login
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("expected redirect to /login, got %s", loc)
	}

	// GET /events/1 redirects to /login?next=/events/1
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/events/1", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fevents%2F1" {
		t.Fatalf("expected redirect with next, got %s", loc)
	}

	// GET /api/events returns 401 Unauthorized
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/events", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for API, got %d", rec.Code)
	}

	// GET /images/item-1 returns 401 Unauthorized
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/images/item-1", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for images, got %d", rec.Code)
	}
}

func TestAuthLifecycle(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// 1. GET /login returns 200 with form
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sign in with Jellyfin") {
		t.Fatalf("expected login title in HTML")
	}

	// Extract CSRF cookie
	var csrfCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookieName {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil {
		t.Fatalf("expected csrf cookie set on GET /login")
	}

	// 2. POST /login without CSRF token fails with 403
	form := url.Values{"username": {"alice"}, "password": {"secret"}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /login without csrf: expected 403, got %d", rec.Code)
	}

	// 3. POST /login with valid CSRF and credentials succeeds and sets session
	form.Set("csrf_token", csrfCookie.Value)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /login valid: expected 303, got %d", rec.Code)
	}

	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected session cookie set after successful login")
	}

	// 4. Authenticated request to / succeeds
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessionCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / authenticated: expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="/logout"`) {
		t.Errorf("expected page to display logout button")
	}

	// 5. GET /logout clears session and redirects
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(sessionCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /logout: expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("expected redirect to /login after logout, got %s", loc)
	}

	// Subsequent request to / should now redirect to /login
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessionCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected unauthenticated redirect after logout, got %d", rec.Code)
	}
}

func TestEventDetailAndImageProxy(t *testing.T) {
	srv, store, _ := setupTestServer(t)
	ctx := context.Background()

	// Seed an event
	snapID, err := store.InsertSnapshot(ctx, storage.Snapshot{
		TakenAt:     time.Now().UTC(),
		LibraryID:   "lib1",
		LibraryName: "Movies",
		ItemCount:   1,
	})
	if err != nil {
		t.Fatalf("insert snapshot failed: %v", err)
	}

	err = store.InsertEvent(ctx, storage.Event{
		SnapshotID:  snapID,
		JellyfinID:  "movie-1",
		EventType:   "added",
		OccurredAt:  time.Now().UTC(),
		Title:       "Inception",
		LibraryName: "Movies",
		MediaType:   "Movie",
		ImageTag:    "poster1",
	})
	if err != nil {
		t.Fatalf("insert event failed: %v", err)
	}

	// Create an active session
	sessToken := srv.sessions.Create("user-123", "alice", "test-user-token-456", []string{"Movies"})
	sessCookie := &http.Cookie{Name: sessionCookieName, Value: sessToken}

	// 1. GET /events/1 fetches live metadata on-demand as the user
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events/1", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /events/1: expected 200, got %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Inception") {
		t.Errorf("expected body to contain 'Inception'")
	}
	if !strings.Contains(body, "A dream within a dream.") {
		t.Errorf("expected live overview from Jellyfin mock")
	}
	if !strings.Contains(body, "Sci-Fi") {
		t.Errorf("expected live genre from Jellyfin mock")
	}
	if !strings.Contains(body, "/images/movie-1?tag=poster1") {
		t.Errorf("expected image src to point to proxy endpoint /images/movie-1")
	}

	// 2. GET /images/movie-1 proxies image using user's token
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/images/movie-1?tag=poster1", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /images/movie-1: expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("expected image/jpeg Content-Type, got %s", rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "fake-jpeg-data" {
		t.Errorf("expected proxied image data, got %s", rec.Body.String())
	}

	// 2b. GET /images with invalid or traversal ID is rejected by route constraint
	for _, badID := range []string{"../secret", "invalid/path", "bad$id", "item..1"} {
		badRec := httptest.NewRecorder()
		badReq := httptest.NewRequest(http.MethodGet, "/images/"+badID, nil)
		badReq.AddCookie(sessCookie)
		srv.ServeHTTP(badRec, badReq)
		if badRec.Code == http.StatusOK {
			t.Errorf("expected bad image ID %q to not return 200, got %d", badID, badRec.Code)
		}
	}

	// 3. GET /stats authenticated
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/stats", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /stats: expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="/logout"`) {
		t.Errorf("expected /stats to display logout button")
	}
}

func TestUserLibraryAccessControl(t *testing.T) {
	srv, store, _ := setupTestServer(t)
	ctx := context.Background()

	now := time.Now().UTC()

	snapID1, _ := store.InsertSnapshot(ctx, storage.Snapshot{TakenAt: now, LibraryID: "lib-mov", LibraryName: "Movies", ItemCount: 1})
	snapID2, _ := store.InsertSnapshot(ctx, storage.Snapshot{TakenAt: now, LibraryID: "lib-ani", LibraryName: "Anime", ItemCount: 1})

	_ = store.InsertEvent(ctx, storage.Event{SnapshotID: snapID1, JellyfinID: "m1", EventType: "added", OccurredAt: now, Title: "Inception", LibraryName: "Movies"})
	_ = store.InsertEvent(ctx, storage.Event{SnapshotID: snapID2, JellyfinID: "a1", EventType: "added", OccurredAt: now, Title: "Attack on Titan", LibraryName: "Anime"})

	// Session for uncle (only has access to "Movies")
	uncleToken := srv.sessions.Create("u-uncle", "uncle", "tok-uncle", []string{"Movies"})
	uncleCookie := &http.Cookie{Name: sessionCookieName, Value: uncleToken}

	// Session for weeb (has access to both "Movies" and "Anime")
	weebToken := srv.sessions.Create("u-weeb", "weeb", "tok-weeb", []string{"Movies", "Anime"})
	weebCookie := &http.Cookie{Name: sessionCookieName, Value: weebToken}

	// 1. Uncle loads feed: sees Inception, does NOT see Attack on Titan
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(uncleCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("uncle feed: expected 200, got %d", rec.Code)
	}
	bodyUncle := rec.Body.String()
	if !strings.Contains(bodyUncle, "Inception") {
		t.Errorf("uncle feed should contain Inception")
	}
	if strings.Contains(bodyUncle, "Attack on Titan") {
		t.Errorf("uncle feed should NOT contain Attack on Titan")
	}
	// Uncle's library filter dropdown: shows Movies, does NOT show Anime
	if strings.Contains(bodyUncle, `<option value="Anime"`) {
		t.Errorf("uncle filter dropdown should NOT contain Anime")
	}

	// 2. Uncle tries to filter by Anime directly via query param -> returns 0 events
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/?library=Anime", nil)
	req.AddCookie(uncleCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("uncle anime filter: expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Attack on Titan") {
		t.Errorf("uncle querying ?library=Anime should NOT see anime events")
	}

	// 3. Uncle tries to visit event detail for Anime event (event ID 2) -> 404
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/events/2", nil)
	req.AddCookie(uncleCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("uncle accessing anime event detail: expected 404, got %d", rec.Code)
	}

	// 4. Weeb loads feed: sees both Inception and Attack on Titan
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(weebCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("weeb feed: expected 200, got %d", rec.Code)
	}
	bodyWeeb := rec.Body.String()
	if !strings.Contains(bodyWeeb, "Inception") || !strings.Contains(bodyWeeb, "Attack on Titan") {
		t.Errorf("weeb feed should contain both Inception and Attack on Titan")
	}
	if !strings.Contains(bodyWeeb, `<option value="Anime"`) {
		t.Errorf("weeb filter dropdown should contain Anime")
	}

	// 5. Weeb visits event detail for Anime event -> 200
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/events/2", nil)
	req.AddCookie(weebCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("weeb accessing anime event detail: expected 200, got %d", rec.Code)
	}
}

func TestAPISyncAuthRateLimitingAndCooldown(t *testing.T) {
	srv, store, _ := setupTestServer(t)

	cfg := &config.Config{
		JellyfinURL:     srv.cfg.JellyfinURL,
		JellyfinAPIKey:  "server-api-key",
		SyncInterval:    time.Hour,
		MinSyncInterval: 10 * time.Second,
	}
	srv.cfg = cfg
	w := syncworker.NewWorker(cfg, srv.client, store, notify.NoOp{})
	w.SetRunning(true)
	srv.worker = w

	// 1. Unauthenticated request to POST /api/sync -> 401 Unauthorized
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without auth, got %d", rec.Code)
	}

	// 2. Authenticated with session cookie, but missing CSRF token -> 403 Forbidden
	sessToken := srv.sessions.Create("u1", "alice", "tok1", []string{"Movies"})
	sessCookie := &http.Cookie{Name: sessionCookieName, Value: sessToken}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without CSRF, got %d", rec.Code)
	}

	// Setup valid CSRF
	csrfVal := "test-csrf-token-12345678901234567890"
	csrfCookie := &http.Cookie{Name: csrfCookieName, Value: csrfVal}

	// 3. Worker on cooldown: last sync finished just now -> 429 Too Many Requests
	w.SetLastSyncFinished(time.Now())

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.AddCookie(sessCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set("X-CSRF-Token", csrfVal)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests on cooldown, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if retryAfter := rec.Header().Get("Retry-After"); retryAfter == "" {
		t.Errorf("expected Retry-After header on 429 response")
	}
	var errResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("expected JSON body on 429 response, got: %s", rec.Body.String())
	}
	if errResp["error"] != "sync is on cooldown" {
		t.Errorf("expected error 'sync is on cooldown', got %v", errResp["error"])
	}

	// 4. In-flight sync: worker is already syncing -> 409 Conflict
	w.SetLastSyncFinished(time.Now().Add(-20 * time.Second)) // cooldown elapsed
	w.SetSyncing(true)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.AddCookie(sessCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set("X-CSRF-Token", csrfVal)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when sync is in progress, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 5. Allowed manual sync: not syncing, cooldown elapsed -> 202 Accepted
	w.SetSyncing(false)
	w.SetLastSyncFinished(time.Now().Add(-20 * time.Second))

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.AddCookie(sessCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set("X-CSRF-Token", csrfVal)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted when sync allowed, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 6. API Key authentication via header without CSRF cookie -> 202 Accepted
	w.SetSyncing(false)
	w.SetLastSyncFinished(time.Now().Add(-20 * time.Second))

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.Header.Set("X-API-Key", "server-api-key")
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted with X-API-Key header, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestIconFilesAndRendering(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// Verify static icon files exist and are servable
	icons := []string{
		"/static/icons/sun.svg",
		"/static/icons/moon.svg",
		"/static/icons/device-desktop.svg",
		"/static/icons/sign-out.svg",
		"/static/icons/sync.svg",
		"/static/favicon.svg",
	}

	for _, path := range icons {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for %s, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<svg") {
			t.Fatalf("expected %s body to contain <svg>, got %s", path, rec.Body.String())
		}
	}

	// Authenticate and verify / renders icon inside button without hardcoding
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	srv.ServeHTTP(rec, req)

	var csrfCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookieName {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil {
		t.Fatal("expected csrf cookie")
	}

	loginForm := url.Values{
		"username":   {"testuser"},
		"password":   {"testpass"},
		"csrf_token": {csrfCookie.Value},
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	srv.ServeHTTP(rec, req)

	var sessCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil {
		t.Fatal("expected session cookie after login")
	}

	// GET / with default theme (auto)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="syncBtn"`) || !strings.Contains(body, `class="sync-label">Sync Now</span>`) {
		t.Fatalf("expected page to contain #syncBtn with sync-label")
	}
	if !strings.Contains(body, `id="themeToggle"`) {
		t.Fatalf("expected page to contain #themeToggle")
	}
	if !strings.Contains(body, `href="/logout"`) {
		t.Fatalf("expected page to contain logout button")
	}

	// GET / with dark theme cookie -> renders moon icon directly
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessCookie)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, `title="Theme: Dark"`) {
		t.Fatalf("expected page to have Theme: Dark title with dark cookie")
	}
}

func TestAPISyncStatusAndStats(t *testing.T) {
	srv, store, _ := setupTestServer(t)

	cfg := &config.Config{
		JellyfinURL:    srv.cfg.JellyfinURL,
		JellyfinAPIKey: "server-api-key",
		SyncInterval:   time.Hour,
	}
	srv.cfg = cfg
	w := syncworker.NewWorker(cfg, srv.client, store, notify.NoOp{})
	w.SetRunning(true)
	w.SetSyncSeq(4)
	w.SetLastSyncEvents(3)
	srv.worker = w

	sessToken := srv.sessions.Create("u1", "alice", "tok1", []string{"Movies"})
	sessCookie := &http.Cookie{Name: sessionCookieName, Value: sessToken}

	// 1. GET /api/sync unauthenticated -> 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated GET /api/sync, got %d", rec.Code)
	}

	// 2. GET /api/sync authenticated -> 200 with status
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for authenticated GET /api/sync, got %d", rec.Code)
	}
	var syncResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &syncResp); err != nil {
		t.Fatalf("decode GET /api/sync response: %v", err)
	}
	if syncResp["is_syncing"] != false {
		t.Errorf("expected is_syncing=false, got %v", syncResp["is_syncing"])
	}
	if syncResp["sync_seq"] != float64(4) {
		t.Errorf("expected sync_seq=4, got %v", syncResp["sync_seq"])
	}
	if syncResp["last_sync_events"] != float64(3) {
		t.Errorf("expected last_sync_events=3, got %v", syncResp["last_sync_events"])
	}

	// 3. While syncing: GET /api/sync returns is_syncing=true
	w.SetSyncing(true)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 while syncing, got %d", rec.Code)
	}
	syncResp = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &syncResp)
	if syncResp["is_syncing"] != true {
		t.Errorf("expected is_syncing=true, got %v", syncResp["is_syncing"])
	}
	w.SetSyncing(false)

	// 4. GET /api/stats authenticated -> includes IsSyncing, LastSyncEvents, SyncSeq
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /api/stats, got %d", rec.Code)
	}
	var statsResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &statsResp); err != nil {
		t.Fatalf("decode GET /api/stats response: %v", err)
	}
	if statsResp["IsSyncing"] != false {
		t.Errorf("expected IsSyncing=false, got %v", statsResp["IsSyncing"])
	}
	if statsResp["SyncSeq"] != float64(4) {
		t.Errorf("expected SyncSeq=4, got %v", statsResp["SyncSeq"])
	}
	if statsResp["LastSyncEvents"] != float64(3) {
		t.Errorf("expected LastSyncEvents=3, got %v", statsResp["LastSyncEvents"])
	}

	// 5. POST /api/sync returns sync_seq
	csrfVal := "test-csrf-token-12345678901234567890"
	csrfCookie := &http.Cookie{Name: csrfCookieName, Value: csrfVal}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	req.AddCookie(sessCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set("X-CSRF-Token", csrfVal)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", rec.Code)
	}
	var postResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &postResp)
	if postResp["sync_seq"] != float64(4) {
		t.Errorf("expected post response to include sync_seq=4, got %v", postResp["sync_seq"])
	}
}

func TestSessionPersistenceAcrossRestart(t *testing.T) {
	srv, store, mockJellyfin := setupTestServer(t)

	// User logs in on srv
	sessToken := srv.sessions.Create("u-bob", "bob", "tok-bob", []string{"Movies"})
	sessCookie := &http.Cookie{Name: sessionCookieName, Value: sessToken}

	// Request succeeds on original server
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessCookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on original server, got %d", rec.Code)
	}

	// Simulate container restart/rebuild by spinning up a new Server instance sharing the same store
	cfg := &config.Config{
		JellyfinURL: mockJellyfin.URL,
	}
	client := jellyfin.NewClient(mockJellyfin.URL, "server-api-key")
	restartedSrv := New(cfg, store, nil, client, web.FS)

	// Request to restarted server with the same session cookie should STILL succeed (not redirect to /login)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(sessCookie)
	restartedSrv.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on restarted server with persisted session, got %d", rec2.Code)
	}
	if strings.Contains(rec2.Header().Get("Location"), "/login") {
		t.Fatalf("unexpected redirect to login after restart")
	}
}

func TestDayLabelAndRelTime(t *testing.T) {
	now := time.Now()
	if dayLabel(now) != "Today" {
		t.Errorf("expected Today for now, got %s", dayLabel(now))
	}
	yesterday := now.AddDate(0, 0, -1)
	if dayLabel(yesterday) != "Yesterday" {
		t.Errorf("expected Yesterday for yesterday, got %s", dayLabel(yesterday))
	}
	pastDate := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	if dayLabel(pastDate) != "Wednesday, January 15, 2025" {
		t.Errorf("expected Wednesday, January 15, 2025, got %s", dayLabel(pastDate))
	}

	if relTime(time.Time{}) != "" {
		t.Errorf("expected empty string for zero time, got %s", relTime(time.Time{}))
	}
	if relTime(now.Add(-10*time.Second)) != "just now" {
		t.Errorf("expected just now, got %s", relTime(now.Add(-10*time.Second)))
	}
	if relTime(now.Add(-5*time.Minute)) != "5 minutes ago" {
		t.Errorf("expected 5 minutes ago, got %s", relTime(now.Add(-5*time.Minute)))
	}
	if relTime(now.Add(-2*time.Hour)) != "2 hours ago" {
		t.Errorf("expected 2 hours ago, got %s", relTime(now.Add(-2*time.Hour)))
	}
	if relTime(now.Add(-30*time.Hour)) != "yesterday" {
		t.Errorf("expected yesterday, got %s", relTime(now.Add(-30*time.Hour)))
	}
}


