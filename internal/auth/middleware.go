package auth

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/scottkw/agenthub-server/internal/tenancy"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxAccountID
	ctxSessionID
	ctxDeviceID
	ctxIsOperator
)

// RequireAuth is HTTP middleware that validates a Bearer JWT, checks that the
// jti maps to an un-revoked un-expired auth_sessions row, and injects the
// user/account/session ids into the request context on success. On any
// failure it writes 401 Unauthorized.
func RequireAuth(signer *JWTSigner, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := bearerToken(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			claims, err := signer.Parse(tok)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if err := CheckSessionActive(r.Context(), db, claims.SessionID); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
			ctx = context.WithValue(ctx, ctxAccountID, claims.AccountID)
			ctx = context.WithValue(ctx, ctxSessionID, claims.SessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserID returns the authenticated user id from the request context.
// Empty string if the request didn't pass RequireAuth.
func UserID(ctx context.Context) string    { return ctxString(ctx, ctxUserID) }
func AccountID(ctx context.Context) string { return ctxString(ctx, ctxAccountID) }
func SessionID(ctx context.Context) string { return ctxString(ctx, ctxSessionID) }
func DeviceID(ctx context.Context) string  { return ctxString(ctx, ctxDeviceID) }

func ctxString(ctx context.Context, k ctxKey) string {
	if v, ok := ctx.Value(k).(string); ok {
		return v
	}
	return ""
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// IsOperator returns true if the authenticated user is an operator.
func IsOperator(ctx context.Context) bool {
	if v, ok := ctx.Value(ctxIsOperator).(bool); ok {
		return v
	}
	return false
}

// RequireOperator wraps an auth-gated handler and returns 403 Forbidden if
// the authenticated user is not an operator.
func RequireOperator(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid := UserID(r.Context())
			if uid == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ok, err := tenancy.IsOperator(r.Context(), svc.cfg.DB, uid)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxIsOperator, true)))
		})
	}
}

// RequireAuthFromService returns a RequireAuth middleware using the Service's
// signer + db. Convenience wrapper so HTTP handlers don't need to know about
// the internals.
func RequireAuthFromService(svc *Service) func(http.Handler) http.Handler {
	return RequireAuth(svc.cfg.Signer, svc.cfg.DB)
}
