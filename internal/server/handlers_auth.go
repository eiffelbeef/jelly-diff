package server

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type loginData struct {
	CSRFToken string
	Error     string
	Username  string
	Next      string
	ServerURL string
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, redirect to /
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if _, ok := s.sessions.Get(c.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}

	csrfToken := csrfTokenFromRequest(r)
	setCSRFCookie(w, csrfToken)

	data := loginData{
		CSRFToken: csrfToken,
		Next:      r.URL.Query().Get("next"),
		ServerURL: s.cfg.JellyfinURL,
	}

	s.renderLogin(w, http.StatusOK, data)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !validateCSRF(r) {
		s.renderLogin(w, http.StatusForbidden, loginData{
			Error:     "Invalid or expired CSRF token. Please try again.",
			ServerURL: s.cfg.JellyfinURL,
		})
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	nextURL := r.FormValue("next")

	if username == "" {
		s.renderLogin(w, http.StatusBadRequest, loginData{
			Error:     "Username is required.",
			ServerURL: s.cfg.JellyfinURL,
		})
		return
	}

	authResult, err := s.client.Authenticate(r.Context(), username, password)
	if err != nil {
		slog.Warn("login failed", "username", username, "err", err)
		csrfToken := csrfTokenFromRequest(r)
		setCSRFCookie(w, csrfToken)
		s.renderLogin(w, http.StatusUnauthorized, loginData{
			CSRFToken: csrfToken,
			Error:     err.Error(),
			Username:  username,
			Next:      nextURL,
			ServerURL: s.cfg.JellyfinURL,
		})
		return
	}

	var libNames []string
	if libs, libErr := s.client.GetLibrariesForUser(r.Context(), authResult.AccessToken, authResult.UserID); libErr == nil {
		for _, l := range libs {
			libNames = append(libNames, l.Name)
		}
	} else {
		slog.Warn("fetch user libraries at login failed", "username", username, "err", libErr)
	}

	sessionID := s.sessions.Create(authResult.UserID, authResult.Username, authResult.AccessToken, libNames)
	setSessionCookie(w, sessionID)

	target := "/"
	if nextURL != "" && strings.HasPrefix(nextURL, "/") && !strings.HasPrefix(nextURL, "//") {
		target = nextURL
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		s.sessions.Delete(c.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, data loginData) {
	s.render(w, s.loginTmpl, "login.html", status, data)
}

func (s *Server) handleProxyImage(w http.ResponseWriter, r *http.Request) {
	sess, ok := userSessionFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "missing image id", http.StatusBadRequest)
		return
	}

	resp, err := s.client.ProxyImage(r.Context(), sess.Token, id, r.URL.Query())
	if err != nil {
		slog.Error("proxy image failed", "id", id, "err", err)
		http.Error(w, "image fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		return
	}

	for _, h := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}

	_, _ = io.Copy(w, resp.Body)
}
