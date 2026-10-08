package server

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/eiffelbeef/jelly-diff/internal/jellyfin"
	"github.com/eiffelbeef/jelly-diff/internal/storage"
)

const eventsPerPage = 25

func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m <= 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h <= 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days <= 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("Jan 2, 2006")
	}
}

func dayLabel(t time.Time) string {
	y, mo, d := t.Date()
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	if y == now.Year() && mo == now.Month() && d == now.Day() {
		return "Today"
	}
	if y == yesterday.Year() && mo == yesterday.Month() && d == yesterday.Day() {
		return "Yesterday"
	}
	return t.Format("Monday, January 2, 2006")
}

type dayGroup struct {
	Label  string
	Date   time.Time
	Events []storage.Event
}

func groupByDay(events []storage.Event) []dayGroup {
	var groups []dayGroup
	for _, ev := range events {
		day := time.Date(ev.OccurredAt.Local().Year(), ev.OccurredAt.Local().Month(), ev.OccurredAt.Local().Day(), 0, 0, 0, 0, ev.OccurredAt.Local().Location())
		if len(groups) > 0 && groups[len(groups)-1].Date.Equal(day) {
			groups[len(groups)-1].Events = append(groups[len(groups)-1].Events, ev)
		} else {
			groups = append(groups, dayGroup{
				Label:  dayLabel(day),
				Date:   day,
				Events: []storage.Event{ev},
			})
		}
	}
	return groups
}

func themeFromRequest(r *http.Request) string {
	if c, err := r.Cookie("theme"); err == nil && c.Value != "" {
		return c.Value
	}
	return "auto"
}

type baseData struct {
	JellyfinURL string
	CSRFToken   string
	LastSyncAt  string
	Username    string
	Theme       string
}

func (s *Server) newBaseData(w http.ResponseWriter, r *http.Request, sess UserSession) baseData {
	csrfToken := csrfTokenFromRequest(r)
	setCSRFCookie(w, csrfToken)

	lastSync := ""
	if ls, _ := s.store.GetLastSyncAt(r.Context()); ls != nil {
		lastSync = relTime(*ls)
	}

	return baseData{
		JellyfinURL: s.cfg.JellyfinURL,
		CSRFToken:   csrfToken,
		LastSyncAt:  lastSync,
		Username:    sess.Username,
		Theme:       themeFromRequest(r),
	}
}

func (s *Server) render(w http.ResponseWriter, tmpl *template.Template, name string, status int, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("execute template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *Server) renderError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

// ── Feed ──────────────────────────────────────────────────────────────────────

type feedData struct {
	baseData
	DayGroups  []dayGroup
	Page       int
	TotalPages int
	Total      int
	Libraries  []string
	Filter     feedFilter
}

type feedFilter struct {
	Library   string
	EventType string
	Query     string
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	sess, _ := userSessionFromContext(r.Context())
	userLibs := s.getSessionLibraries(r.Context(), sess)

	f := storage.EventFilter{
		Library:          q.Get("library"),
		EventType:        q.Get("type"),
		Query:            q.Get("q"),
		AllowedLibraries: userLibs,
		Page:             page,
		Limit:            eventsPerPage,
	}

	events, total, err := s.store.ListEvents(r.Context(), f)
	if err != nil {
		slog.Error("list events", "err", err)
		s.renderError(w, http.StatusInternalServerError, "could not load events")
		return
	}

	allLibs, _ := s.store.GetLibraryNames(r.Context())
	var visibleLibs []string
	if userLibs != nil {
		userLibSet := make(map[string]bool, len(userLibs))
		for _, l := range userLibs {
			userLibSet[l] = true
		}
		for _, l := range allLibs {
			if userLibSet[l] {
				visibleLibs = append(visibleLibs, l)
			}
		}
	} else {
		visibleLibs = allLibs
	}

	totalPages := int(math.Ceil(float64(total) / float64(eventsPerPage)))
	if totalPages < 1 {
		totalPages = 1
	}

	data := feedData{
		baseData:   s.newBaseData(w, r, sess),
		DayGroups:  groupByDay(events),
		Page:       page,
		TotalPages: totalPages,
		Total:      total,
		Libraries:  visibleLibs,
		Filter: feedFilter{
			Library:   f.Library,
			EventType: f.EventType,
			Query:     f.Query,
		},
	}

	s.render(w, s.feedTmpl, "base", http.StatusOK, data)
}

// ── Detail ────────────────────────────────────────────────────────────────────

type detailData struct {
	baseData
	Event    *storage.Event
	Overview string
	Genres   []string
	Rating   float64
	Year     int
	AddedAt  time.Time
}

func (s *Server) handleEventDetail(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, "invalid event id")
		return
	}

	ev, err := s.store.GetEvent(r.Context(), id)
	if err != nil || ev == nil {
		s.renderError(w, http.StatusNotFound, "event not found")
		return
	}

	sess, _ := userSessionFromContext(r.Context())
	userLibs := s.getSessionLibraries(r.Context(), sess)
	if userLibs != nil {
		allowed := false
		for _, l := range userLibs {
			if l == ev.LibraryName {
				allowed = true
				break
			}
		}
		if !allowed {
			s.renderError(w, http.StatusNotFound, "event not found")
			return
		}
	}

	// Fetch live rich metadata from Jellyfin as the authenticated user on-demand
	var liveItem *jellyfin.Item
	if sess.Token != "" && sess.UserID != "" {
		var fetchErr error
		liveItem, fetchErr = s.client.GetItemForUser(r.Context(), sess.Token, sess.UserID, ev.JellyfinID)
		if fetchErr != nil {
			slog.Debug("get live item for user failed", "id", ev.JellyfinID, "err", fetchErr)
		}
	}

	overview := ""
	var genres []string
	rating := 0.0
	year := 0
	var addedAt time.Time

	if liveItem != nil {
		overview = liveItem.Overview
		genres = liveItem.Genres
		rating = liveItem.CommunityRating
		year = liveItem.ProductionYear
		if liveItem.DateCreated != "" {
			addedAt, _ = time.Parse(time.RFC3339, liveItem.DateCreated)
		}
	}

	data := detailData{
		baseData: s.newBaseData(w, r, sess),
		Event:    ev,
		Overview: overview,
		Genres:   genres,
		Rating:   rating,
		Year:     year,
		AddedAt:  addedAt,
	}

	s.render(w, s.detailTmpl, "base", http.StatusOK, data)
}

// ── Stats ─────────────────────────────────────────────────────────────────────

type statsData struct {
	baseData
	Stats    *storage.Stats
	ChartSVG template.HTML
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	sess, _ := userSessionFromContext(r.Context())
	userLibs := s.getSessionLibraries(r.Context(), sess)

	stats, err := s.store.GetStats(r.Context(), userLibs)
	if err != nil {
		slog.Error("get stats", "err", err)
		s.renderError(w, http.StatusInternalServerError, "could not load stats")
		return
	}

	data := statsData{
		baseData: s.newBaseData(w, r, sess),
		Stats:    stats,
		ChartSVG: template.HTML(buildChartSVG(stats.DailyChart)), //nolint:gosec
	}

	s.render(w, s.statsTmpl, "base", http.StatusOK, data)
}

// buildChartSVG renders a stacked bar chart as an inline SVG string.
func buildChartSVG(data []storage.DayCount) string {
	if len(data) == 0 {
		return `<svg width="100%" height="120" xmlns="http://www.w3.org/2000/svg"><text x="50%" y="60" text-anchor="middle" fill="#6e7681" font-family="sans-serif" font-size="14">No data yet</text></svg>`
	}

	const (
		svgW   = 800
		svgH   = 120
		barGap = 2
	)

	var maxVal int
	for _, d := range data {
		if d.Added+d.Removed > maxVal {
			maxVal = d.Added + d.Removed
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	n := len(data)
	barW := float64(svgW)/float64(n) - barGap

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg width="100%%" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Daily activity chart">`, svgW, svgH)

	for i, d := range data {
		x := float64(i) * (barW + barGap)
		totalH := float64(d.Added+d.Removed) / float64(maxVal) * float64(svgH-20)
		addedH := float64(d.Added) / float64(maxVal) * float64(svgH-20)
		removedH := totalH - addedH

		if removedH > 0 {
			y := float64(svgH) - removedH
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="#f85149" rx="2"><title>%s: %d removed</title></rect>`,
				x, y, barW, removedH, d.Date, d.Removed)
		}
		if addedH > 0 {
			y := float64(svgH) - totalH
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="#2ea043" rx="2"><title>%s: %d added</title></rect>`,
				x, y, barW, addedH, d.Date, d.Added)
		}
	}

	sb.WriteString(`</svg>`)
	return sb.String()
}

