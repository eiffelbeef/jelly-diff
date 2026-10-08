package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/storage"
)

const sessionCookieName = "jd_session"
const sessionDuration = 30 * 24 * time.Hour // 30 days

// UserSession represents an active authenticated user session.
type UserSession struct {
	ID        string
	UserID    string
	Username  string
	Token     string
	Libraries []string // Human-readable library names visible to the user
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionStore provides thread-safe session management backed by SQLite.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]UserSession
	store    *storage.Store
}

// NewSessionStore creates a new SessionStore. If store is provided, active sessions are preloaded from the database.
func NewSessionStore(store *storage.Store) *SessionStore {
	ss := &SessionStore{
		sessions: make(map[string]UserSession),
		store:    store,
	}
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.DeleteExpiredSessions(ctx)
		if list, err := store.ListActiveSessions(ctx); err == nil {
			for _, rec := range list {
				ss.sessions[rec.ID] = UserSession{
					ID:        rec.ID,
					UserID:    rec.UserID,
					Username:  rec.Username,
					Token:     rec.Token,
					Libraries: rec.Libraries,
					CreatedAt: rec.CreatedAt,
					ExpiresAt: rec.ExpiresAt,
				}
			}
		}
	}
	return ss
}

// Create stores a new session and returns the session ID.
func (s *SessionStore) Create(userID, username, token string, libraries []string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand read failed: " + err.Error())
	}
	id := hex.EncodeToString(b)

	now := time.Now().UTC()
	sess := UserSession{
		ID:        id,
		UserID:    userID,
		Username:  username,
		Token:     token,
		Libraries: libraries,
		CreatedAt: now,
		ExpiresAt: now.Add(sessionDuration),
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.store.SaveSession(ctx, storage.SessionRecord{
			ID:        sess.ID,
			UserID:    sess.UserID,
			Username:  sess.Username,
			Token:     sess.Token,
			Libraries: sess.Libraries,
			CreatedAt: sess.CreatedAt,
			ExpiresAt: sess.ExpiresAt,
		})
	}

	return id
}

// SetLibraries updates the cached allowed library names for a session.
func (s *SessionStore) SetLibraries(id string, libraries []string) {
	s.mu.Lock()
	if sess, ok := s.sessions[id]; ok {
		sess.Libraries = libraries
		s.sessions[id] = sess
	}
	s.mu.Unlock()

	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.store.UpdateSessionLibraries(ctx, id, libraries)
	}
}

// Get returns the session if it exists and has not expired.
func (s *SessionStore) Get(id string) (UserSession, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		if s.store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			rec, err := s.store.GetSession(ctx, id)
			if err == nil && rec != nil {
				if time.Now().UTC().After(rec.ExpiresAt) {
					_ = s.store.DeleteSession(ctx, id)
					return UserSession{}, false
				}
				sess = UserSession{
					ID:        rec.ID,
					UserID:    rec.UserID,
					Username:  rec.Username,
					Token:     rec.Token,
					Libraries: rec.Libraries,
					CreatedAt: rec.CreatedAt,
					ExpiresAt: rec.ExpiresAt,
				}
				s.mu.Lock()
				s.sessions[id] = sess
				s.mu.Unlock()
				return sess, true
			}
		}
		return UserSession{}, false
	}

	if time.Now().UTC().After(sess.ExpiresAt) {
		s.Delete(id)
		return UserSession{}, false
	}

	return sess, true
}

// Delete removes a session.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()

	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.store.DeleteSession(ctx, id)
	}
}

type sessionContextKey struct{}

// withUserSession attaches the UserSession to the request context.
func withUserSession(ctx context.Context, sess UserSession) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, sess)
}

// userSessionFromContext extracts the UserSession from the request context.
func userSessionFromContext(ctx context.Context) (UserSession, bool) {
	sess, ok := ctx.Value(sessionContextKey{}).(UserSession)
	return sess, ok
}

// setSessionCookie writes the HTTP-only session cookie.
func setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
	})
}

// clearSessionCookie deletes the session cookie.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
