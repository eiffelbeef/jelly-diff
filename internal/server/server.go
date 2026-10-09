package server

import (
	"context"
	"crypto/subtle"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/eiffelbeef/jelly-diff/internal/config"
	"github.com/eiffelbeef/jelly-diff/internal/jellyfin"
	"github.com/eiffelbeef/jelly-diff/internal/storage"
	"github.com/eiffelbeef/jelly-diff/internal/sync"
)

// Server holds the HTTP server state.
type Server struct {
	router     *chi.Mux
	cfg        *config.Config
	store      *storage.Store
	worker     *sync.Worker
	client     *jellyfin.Client
	sessions   *SessionStore
	webFS      fs.FS
	icons      map[string]template.HTML
	feedTmpl   *template.Template
	detailTmpl *template.Template
	statsTmpl  *template.Template
	loginTmpl  *template.Template
}

// New creates a configured Server.
func New(cfg *config.Config, store *storage.Store, worker *sync.Worker, client *jellyfin.Client, webFS fs.FS) *Server {
	icons := loadIcons(webFS)
	tmplFuncs := template.FuncMap{
		"icon": func(name string) template.HTML {
			if icons != nil {
				if svg, ok := icons[name]; ok {
					return svg
				}
			}
			return ""
		},
		"relTime":    relTime,
		"add":        func(a, b int) int { return a + b },
		"sub":        func(a, b int) int { return a - b },
		"formatDate": func(t time.Time) string { return t.Format("Jan 02, 2006") },
	}

	parse := func(files ...string) *template.Template {
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = "templates/" + f
		}
		return template.Must(template.New("").Funcs(tmplFuncs).ParseFS(webFS, paths...))
	}

	s := &Server{
		cfg:        cfg,
		store:      store,
		worker:     worker,
		client:     client,
		sessions:   NewSessionStore(store),
		webFS:      webFS,
		icons:      icons,
		feedTmpl:   parse("base.html", "feed.html"),
		detailTmpl: parse("base.html", "detail.html"),
		statsTmpl:  parse("base.html", "stats.html"),
		loginTmpl:  parse("login.html"),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) routes() {
	r := chi.NewRouter()

	// Middleware
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)

	// Static assets (public)
	staticFS, _ := fs.Sub(s.webFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", cacheControl(http.FileServer(http.FS(staticFS)))))

	// Public routes
	r.Get("/health", s.handleHealth)
	r.Get("/login", s.handleLogin)
	r.Post("/login", s.handleLoginSubmit)
	r.Get("/logout", s.handleLogout)
	r.Post("/logout", s.handleLogout)

	// Authenticated routes
	r.Group(func(auth chi.Router) {
		auth.Use(s.requireAuth)

		// UI routes
		auth.Get("/", s.handleFeed)
		auth.Get("/events/{id}", s.handleEventDetail)
		auth.Get("/stats", s.handleStats)

		// Image proxy (fetched as logged-in Jellyfin user)
		auth.Get("/images/{id:[a-zA-Z0-9_-]+}", s.handleProxyImage)

		// API routes
		auth.Get("/api/events", s.handleAPIEvents)
		auth.Get("/api/stats", s.handleAPIStats)
		auth.Get("/api/sync", s.handleAPISyncStatus)
		auth.Post("/api/sync", s.handleAPISync)
	})

	s.router = r
}

type apiKeyAuthKey struct{}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && cookie.Value != "" {
			sess, ok := s.sessions.Get(cookie.Value)
			if ok {
				next.ServeHTTP(w, r.WithContext(withUserSession(r.Context(), sess)))
				return
			}
		}

		if s.cfg != nil && s.cfg.JellyfinAPIKey != "" {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = r.Header.Get("X-Emby-Token")
			}
			if apiKey == "" {
				if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
					apiKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}
			if apiKey != "" && subtle.ConstantTimeCompare([]byte(apiKey), []byte(s.cfg.JellyfinAPIKey)) == 1 {
				ctx := context.WithValue(r.Context(), apiKeyAuthKey{}, true)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/images/") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		nextURL := r.URL.RequestURI()
		if nextURL == "" || nextURL == "/" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(nextURL), http.StatusSeeOther)
		}
	})
}

// getSessionLibraries returns the library names accessible to the session user.
// Returns nil if session is unauthenticated, indicating unrestricted/internal access.
func (s *Server) getSessionLibraries(ctx context.Context, sess UserSession) []string {
	if sess.UserID == "" || sess.Token == "" {
		return nil
	}
	if sess.Libraries != nil {
		return sess.Libraries
	}

	libs, err := s.client.GetLibrariesForUser(ctx, sess.Token, sess.UserID)
	if err != nil {
		slog.Warn("fetch user libraries failed", "username", sess.Username, "err", err)
		return nil
	}

	names := make([]string, 0, len(libs))
	for _, l := range libs {
		names = append(names, l.Name)
	}
	s.sessions.SetLibraries(sess.ID, names)
	return names
}

// requestLogger is a minimal structured request logger using slog.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration", time.Since(start).String(),
		)
	})
}

// cacheControl sets revalidation headers for static assets so changes apply immediately.
func cacheControl(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

func loadIcons(webFS fs.FS) map[string]template.HTML {
	icons := make(map[string]template.HTML)
	entries, err := fs.ReadDir(webFS, "static/icons")
	if err != nil {
		return icons
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".svg") {
			continue
		}
		data, err := fs.ReadFile(webFS, "static/icons/"+entry.Name())
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".svg")
		html := template.HTML(data) //nolint:gosec
		icons[base] = html
		icons[entry.Name()] = html
	}
	return icons
}
