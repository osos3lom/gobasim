package web

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const (
	CSRFCookieName = "sawt_csrf"
	csrfFieldName  = "csrf_token"
	csrfTokenLen   = 64 // hex chars (32 random bytes)
)

// ensureCSRFToken returns the request's CSRF token, minting and setting a new
// cookie when none exists. The cookie is intentionally not HttpOnly so the
// double-submit pattern works, but the token itself grants nothing — it only
// has to match the form field.
func (s *Server) ensureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(CSRFCookieName); err == nil && len(c.Value) == csrfTokenLen {
		return c.Value
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing means the platform RNG is broken; nothing
		// sensible to serve at that point.
		panic("csrf: failed to read random bytes: " + err.Error())
	}
	token := hex.EncodeToString(buf)

	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

// requireCSRF rejects state-changing requests whose csrf_token form field does
// not match the sawt_csrf cookie (double-submit cookie pattern).
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CSRFCookieName)
		if err != nil || len(cookie.Value) != csrfTokenLen {
			http.Error(w, "CSRF token missing — reload the page and try again", http.StatusForbidden)
			return
		}

		field := r.FormValue(csrfFieldName)
		if field == "" || !hmac.Equal([]byte(field), []byte(cookie.Value)) {
			http.Error(w, "CSRF token mismatch — reload the page and try again", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
