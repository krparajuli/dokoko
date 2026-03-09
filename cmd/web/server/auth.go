package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	authpkg "dokoko.ai/dokoko/internal/auth"
	"dokoko.ai/dokoko/pkg/logger"
)

const (
	sessionCookieName = "dokoko_session"
	sessionTTL        = 24 * time.Hour
)

// loginHandler handles POST /api/auth/login.
// Body: {"username": "...", "password": "..."}
// On success: sets httpOnly session cookie, returns {username, role}.
func (h *handler) loginHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil || body.Username == "" {
		jsonErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	user, err := h.authStore.Authenticate(body.Username, body.Password)
	if err != nil {
		jsonErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	sess := h.authStore.CreateSession(user.Username, user.Role, sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	jsonOK(w, map[string]string{
		"username": user.Username,
		"role":     string(user.Role),
	})
}

// logoutHandler handles POST /api/auth/logout.
// Deletes the session and clears the cookie.
func (h *handler) logoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		h.authStore.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	jsonOK(w, map[string]string{"message": "logged out"})
}

// meHandler handles GET /api/auth/me.
// Returns the authenticated user's info from the request context.
func (h *handler) meHandler(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFromContext(r.Context())
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	jsonOK(w, map[string]string{
		"username": sess.Username,
		"role":     string(sess.Role),
	})
}

// authMiddleware validates session cookies for all /api/* routes except the
// login endpoint, health check, and OPTIONS preflight requests.
func authMiddleware(store *authpkg.Store, log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Pass through: preflight, login, health, and non-API paths.
			if r.Method == http.MethodOptions ||
				r.URL.Path == "/api/auth/login" ||
				r.URL.Path == "/api/health" ||
				!strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				jsonErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			sess, ok := store.GetSession(cookie.Value)
			if !ok {
				jsonErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r.WithContext(contextWithSession(r.Context(), sess)))
		})
	}
}

type contextKey string

const userContextKey contextKey = "auth_user"

// contextWithSession stores the session in the request context.
func contextWithSession(ctx context.Context, s *authpkg.Session) context.Context {
	return context.WithValue(ctx, userContextKey, s)
}

// sessionFromContext retrieves the session from the request context.
func sessionFromContext(ctx context.Context) (*authpkg.Session, bool) {
	s, ok := ctx.Value(userContextKey).(*authpkg.Session)
	return s, ok
}
