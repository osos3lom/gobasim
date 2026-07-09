package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sawt-go/config"
	"sawt-go/database"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "sawt_session"

type contextKey string

const UsernameContextKey contextKey = "username"

type AuthManager struct {
	cfg     *config.Config
	queries *database.Queries
}

func NewAuthManager(cfg *config.Config, queries *database.Queries) *AuthManager {
	return &AuthManager{
		cfg:     cfg,
		queries: queries,
	}
}

func (a *AuthManager) computeHash(value string) string {
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// GenerateCookieValue creates a signed string "username:expiration:signature".
func (a *AuthManager) GenerateCookieValue(username string, duration time.Duration) string {
	expiration := time.Now().Add(duration).Unix()
	value := fmt.Sprintf("%s:%d", username, expiration)
	sig := a.computeHash(value)
	return fmt.Sprintf("%s:%s", value, sig)
}

// VerifyCookieValue parses and verifies a signed session cookie. Returns username if valid.
func (a *AuthManager) VerifyCookieValue(cookieValue string) (string, error) {
	// The format is username:expiration:signature.
	// Since expiration is digits-only and signature is hex-only, neither contains ':'.
	// We extract signature and expiration from the right of the string to support usernames containing ':'.
	lastColon := strings.LastIndex(cookieValue, ":")
	if lastColon == -1 {
		return "", fmt.Errorf("invalid cookie format")
	}
	signature := cookieValue[lastColon+1:]
	remaining := cookieValue[:lastColon]

	secondLastColon := strings.LastIndex(remaining, ":")
	if secondLastColon == -1 {
		return "", fmt.Errorf("invalid cookie format")
	}
	expStr := remaining[secondLastColon+1:]
	username := remaining[:secondLastColon]

	value := fmt.Sprintf("%s:%s", username, expStr)
	expectedSig := a.computeHash(value)

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return "", fmt.Errorf("signature verification failed")
	}

	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiration timestamp")
	}

	if time.Now().Unix() > exp {
		return "", fmt.Errorf("session expired")
	}

	return username, nil
}

// RequireAuth is a middleware that protects routes and redirects to /login if unauthenticated.
func (a *AuthManager) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		username, err := a.VerifyCookieValue(cookie.Value)
		if err != nil {
			// Clear invalid cookie
			http.SetCookie(w, &http.Cookie{
				Name:     SessionCookieName,
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
			})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// Inject username into request context using custom type context key
		ctx := context.WithValue(r.Context(), UsernameContextKey, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Login verifies password hashes and issues a session cookie on success.
func (a *AuthManager) Login(ctx context.Context, username, password string) (*http.Cookie, error) {
	user, err := a.queries.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("invalid username or password")
	}

	err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	if err != nil {
		return nil, fmt.Errorf("invalid username or password")
	}

	// Issue cookie valid for 24 hours
	cookieValue := a.GenerateCookieValue(user.Username, 24*time.Hour)
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    cookieValue,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   a.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	}, nil
}
