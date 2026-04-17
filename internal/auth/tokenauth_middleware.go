package auth

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
)

// RequireAuthOrToken accepts either "Authorization: Bearer <jwt>" (session
// auth) OR "Authorization: Token ahs_<...>" (API token auth). Injects user_id,
// account_id, and a synthetic session_id into the request context on success.
// On any failure it writes 401 Unauthorized.
//
// For API tokens, session_id is "api-token:<token-id>" so downstream handlers
// can distinguish the two if they need to (e.g. to forbid certain actions
// for API token clients).
func RequireAuthOrToken(signer *JWTSigner, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			switch {
			case strings.HasPrefix(h, "Bearer "):
				tok := strings.TrimSpace(h[len("Bearer "):])
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
			case strings.HasPrefix(h, "Token "):
				tok := strings.TrimSpace(h[len("Token "):])
				if !strings.HasPrefix(tok, apiTokenPrefix) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				rec, err := LookupAPIToken(r.Context(), db, tok)
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				ctx := context.WithValue(r.Context(), ctxUserID, rec.UserID)
				ctx = context.WithValue(ctx, ctxAccountID, rec.AccountID)
				ctx = context.WithValue(ctx, ctxSessionID, "api-token:"+rec.ID)
				if rec.DeviceID != "" {
					ctx = context.WithValue(ctx, ctxDeviceID, rec.DeviceID)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
			default:
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			}
		})
	}
}

// RequireAuthOrTokenFromService wraps RequireAuthOrToken using the Service's
// signer and db.
func RequireAuthOrTokenFromService(svc *Service) func(http.Handler) http.Handler {
	return RequireAuthOrToken(svc.cfg.Signer, svc.cfg.DB)
}
