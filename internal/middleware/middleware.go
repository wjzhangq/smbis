package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/wj/smbis/internal/model"
)

// SessionCookieName is the name of the HTTP cookie used to store session IDs.
const SessionCookieName = "smb_session"

// contextKey is an unexported type for context keys in this package.
type contextKey int

const (
	identityKey contextKey = iota
)

// SetIdentity stores an Identity in the context and returns the updated context.
func SetIdentity(ctx context.Context, id *model.Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// GetIdentity retrieves the Identity from the context. Returns nil if not set.
func GetIdentity(ctx context.Context) *model.Identity {
	id, _ := ctx.Value(identityKey).(*model.Identity)
	return id
}

// MustGetIdentity retrieves the Identity from the context. Panics if not set.
// Use only in handlers that are guaranteed to be behind auth middleware.
func MustGetIdentity(ctx context.Context) *model.Identity {
	id := GetIdentity(ctx)
	if id == nil {
		panic("middleware: identity not set in context")
	}
	return id
}

// SessionGetter is the interface required by RequireAuth to look up sessions.
type SessionGetter interface {
	GetSession(ctx context.Context, id string) (*model.Session, error)
}

// CLIKeyChecker is the interface required by CLIAuth to validate CLI API keys.
type CLIKeyChecker interface {
	GetCLIKeyByValue(ctx context.Context, keyValue string) (*model.CLIKey, error)
	UpdateCLIKeyLastUsed(ctx context.Context, id string) error
}

// responseWriter wraps http.ResponseWriter to capture the status code written.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Logger returns a middleware that logs each request with method, path, status,
// duration, remote address, and user-agent using structured slog logging.
// Info is used for 1xx/2xx/3xx, Warn for 4xx, and Error for 5xx responses.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration", duration.String(),
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		}

		switch {
		case rw.status >= 500:
			slog.Error("request", args...)
		case rw.status >= 400:
			slog.Warn("request", args...)
		default:
			slog.Info("request", args...)
		}
	})
}

// Recover returns a middleware that catches panics, logs the stack trace, and
// returns a 500 Internal Server Error response.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				slog.Error("panic recovered",
					"error", rec,
					"stack", string(stack),
					"method", r.Method,
					"path", r.URL.Path,
				)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequireAuth returns a middleware that validates the session cookie. If the
// session is valid it sets the Identity in the request context and calls next.
// Otherwise it redirects the client to /login with a 302 response.
func RequireAuth(sg SessionGetter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}

			session, err := sg.GetSession(r.Context(), cookie.Value)
			if err != nil || session == nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}

			if session.ExpiresAt.Before(time.Now()) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}

			identity := &model.Identity{
				IsAdmin:  session.IsAdmin,
				UserID:   session.UserID,
				Username: session.UserID, // Username resolved from UserID; callers may enrich if needed.
			}

			ctx := SetIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin is a middleware that must be used after RequireAuth. It checks
// whether the authenticated identity has admin privileges. If not, it returns
// a 403 Forbidden response.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetIdentity(r.Context())
		if id == nil || !id.IsAdmin {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CLIAuth returns a middleware that authenticates requests using a Bearer token
// in the Authorization header (format: "Bearer smb_xxx"). On success it sets
// an admin Identity in the context and calls next. On failure it returns a 401
// JSON response.
func CLIAuth(ck CLIKeyChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeJSONError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			const bearerPrefix = "Bearer "
			if !strings.HasPrefix(authHeader, bearerPrefix) {
				writeJSONError(w, http.StatusUnauthorized, "invalid authorization header format")
				return
			}

			keyValue := strings.TrimPrefix(authHeader, bearerPrefix)
			if keyValue == "" {
				writeJSONError(w, http.StatusUnauthorized, "empty bearer token")
				return
			}

			cliKey, err := ck.GetCLIKeyByValue(r.Context(), keyValue)
			if err != nil || cliKey == nil {
				writeJSONError(w, http.StatusUnauthorized, "invalid api key")
				return
			}

			if cliKey.RevokedAt != nil {
				writeJSONError(w, http.StatusUnauthorized, "api key has been revoked")
				return
			}

			if err := ck.UpdateCLIKeyLastUsed(r.Context(), cliKey.ID); err != nil {
				slog.Warn("failed to update cli key last_used_at",
					"key_id", cliKey.ID,
					"error", err,
				)
			}

			identity := &model.Identity{
				IsAdmin:  true,
				UserID:   "admin",
				Username: "admin",
			}

			ctx := SetIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AuditLog logs a structured audit event for an action performed on a target.
// The actor is derived from the Identity in the request context, defaulting to
// "anonymous" if no identity is present. Extra must be key-value pairs suitable
// for slog structured logging.
func AuditLog(r *http.Request, action string, target string, extra ...any) {
	actor := "anonymous"
	if id := GetIdentity(r.Context()); id != nil {
		actor = id.Username
		if actor == "" {
			actor = id.UserID
		}
	}

	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	} else {
		// X-Forwarded-For may be a comma-separated list; use the first entry.
		if idx := strings.Index(ip, ","); idx != -1 {
			ip = strings.TrimSpace(ip[:idx])
		}
	}

	args := []any{
		"actor", actor,
		"action", action,
		"target", target,
		"ip", ip,
		"user_agent", r.UserAgent(),
	}
	args = append(args, extra...)

	slog.Info("audit", args...)
}

// writeJSONError writes a JSON-encoded error response with the given status code.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
