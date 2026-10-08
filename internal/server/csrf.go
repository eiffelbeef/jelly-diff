package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
)

const csrfCookieName = "jd_csrf"
const csrfHeaderName = "X-CSRF-Token"
const csrfFormField  = "csrf_token"

// newCSRFToken generates a 32-byte random hex token.
func newCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("csrf: random read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// csrfTokenFromRequest returns the CSRF token stored in the request cookie.
// If no cookie exists, a new token is minted.
func csrfTokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil {
		return c.Value
	}
	return newCSRFToken()
}

// setCSRFCookie writes a CSRF cookie on the response.
func setCSRFCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // must be readable by JS for header injection
		SameSite: http.SameSiteLaxMode,
	})
}

// validateCSRF checks the submitted token against the cookie.
func validateCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	// Check header first (AJAX), then form field
	submitted := r.Header.Get(csrfHeaderName)
	if submitted == "" {
		submitted = r.FormValue(csrfFormField)
	}
	if submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(cookie.Value)) == 1
}
