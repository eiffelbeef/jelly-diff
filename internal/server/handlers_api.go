package server

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/eiffelbeef/jelly-diff/internal/storage"
	"github.com/eiffelbeef/jelly-diff/internal/sync"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encoding error", http.StatusInternalServerError)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"ok": "false", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAPISync(w http.ResponseWriter, r *http.Request) {
	isAPIKey, _ := r.Context().Value(apiKeyAuthKey{}).(bool)
	if !isAPIKey && !validateCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid CSRF token"})
		return
	}

	if s.worker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sync worker unavailable"})
		return
	}

	result, remaining := s.worker.TriggerSync()
	switch result {
	case sync.TriggerAlreadyRunning:
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "sync already in progress",
			"sync_seq": s.worker.SyncSeq(),
		})
	case sync.TriggerCooldown:
		secs := int(math.Ceil(remaining.Seconds()))
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":               "sync is on cooldown",
			"retry_after_seconds": secs,
		})
	case sync.TriggerUnavailable:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "sync worker unavailable",
		})
	case sync.TriggerAccepted:
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":   "sync triggered",
			"sync_seq": s.worker.SyncSeq(),
		})
	}
}

type syncStatusResponse struct {
	IsSyncing      bool       `json:"is_syncing"`
	LastSyncAt     *time.Time `json:"last_sync_at,omitempty"`
	LastSyncEvents int        `json:"last_sync_events"`
	SyncSeq        int64      `json:"sync_seq"`
}

func (s *Server) handleAPISyncStatus(w http.ResponseWriter, r *http.Request) {
	resp := syncStatusResponse{}
	if s.worker != nil {
		resp.IsSyncing = s.worker.IsSyncing()
		lastFinished := s.worker.LastSyncFinished()
		if !lastFinished.IsZero() {
			resp.LastSyncAt = &lastFinished
		}
		resp.LastSyncEvents = s.worker.LastSyncEvents()
		resp.SyncSeq = s.worker.SyncSeq()
	}
	if resp.LastSyncAt == nil {
		if ls, _ := s.store.GetLastSyncAt(r.Context()); ls != nil {
			resp.LastSyncAt = ls
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	sess, _ := userSessionFromContext(r.Context())
	userLibs := s.getSessionLibraries(r.Context(), sess)
	stats, err := s.store.GetStats(r.Context(), userLibs)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type apiStatsResponse struct {
		*storage.Stats
		IsSyncing      bool  `json:"IsSyncing"`
		LastSyncEvents int   `json:"LastSyncEvents"`
		SyncSeq        int64 `json:"SyncSeq"`
	}

	resp := apiStatsResponse{
		Stats: stats,
	}
	if s.worker != nil {
		resp.IsSyncing = s.worker.IsSyncing()
		resp.LastSyncEvents = s.worker.LastSyncEvents()
		resp.SyncSeq = s.worker.SyncSeq()
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	sess, _ := userSessionFromContext(r.Context())
	userLibs := s.getSessionLibraries(r.Context(), sess)

	f := storage.EventFilter{
		Library:          q.Get("library"),
		EventType:        q.Get("type"),
		Query:            q.Get("q"),
		AllowedLibraries: userLibs,
	}

	if fromStr := q.Get("from"); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err == nil {
			f.From = t
		}
	}
	if toStr := q.Get("to"); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err == nil {
			f.To = t
		}
	}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.Limit, _ = strconv.Atoi(q.Get("limit"))

	events, total, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"total":  total,
		"page":   f.Page,
		"limit":  f.Limit,
	})
}
