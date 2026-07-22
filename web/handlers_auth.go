package web

import (
	"context"
	"net"
	"net/http"
)

type peerIPKeyType struct{}

var peerIPKey peerIPKeyType

// capturePeerIP records the real TCP peer address into the request context
// before middleware.RealIP overwrites RemoteAddr from X-Forwarded-For. The login
// limiter keys on this real peer so a spoofed X-Forwarded-For can't mint an
// unlimited supply of fresh login buckets (C5).
func capturePeerIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerIPKey, host)))
	})
}

// clientIP extracts the host portion of RemoteAddr (RealIP middleware has
// already resolved proxy headers upstream).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// peerIP returns the real TCP peer captured by capturePeerIP, falling back to
// clientIP (the RealIP-resolved address) when unavailable.
func peerIP(r *http.Request) string {
	if v, ok := r.Context().Value(peerIPKey).(string); ok && v != "" {
		return v
	}
	return clientIP(r)
}

func (s *Server) handleGetLogin(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "login.html", map[string]interface{}{
		"Error":     "",
		"CSRFToken": s.ensureCSRFToken(w, r),
	})
}

func (s *Server) handlePostLogin(w http.ResponseWriter, r *http.Request) {
	if allowed, _ := s.loginLimiter.Allow(peerIP(r)); !allowed {
		s.renderError(w, http.StatusTooManyRequests, "Too many login attempts — try again in a few minutes.")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	cookie, err := s.auth.Login(r.Context(), username, password)
	if err != nil {
		s.renderTemplate(w, "login.html", map[string]interface{}{
			"Error":     err.Error(),
			"CSRFToken": s.ensureCSRFToken(w, r),
		})
		return
	}

	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
