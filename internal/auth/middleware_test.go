package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedActiveSession(t *testing.T, db *sql.DB, jti string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO auth_sessions (id, user_id, account_id, expires_at)
		 VALUES (?, 'u1', 'acct1', datetime('now', '+1 hour'))`, jti)
	require.NoError(t, err)
}

func TestRequireAuth_ValidToken(t *testing.T) {
	db := withTestDB(t)
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")

	seedActiveSession(t, db, "jti1")
	tok, err := signer.Sign(Claims{SessionID: "jti1", UserID: "u1", AccountID: "acct1", TTL: time.Hour})
	require.NoError(t, err)

	gotCtx := make(chan context.Context, 1)
	h := RequireAuth(signer, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCtx <- r.Context()
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, 200, rr.Code)
	ctx := <-gotCtx
	require.Equal(t, "u1", UserID(ctx))
	require.Equal(t, "acct1", AccountID(ctx))
	require.Equal(t, "jti1", SessionID(ctx))
}

func TestRequireAuth_MissingHeader(t *testing.T) {
	db := withTestDB(t)
	signer := NewJWTSigner(make([]byte, 32), "agenthub-server")

	h := RequireAuth(signer, db)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequireAuth_RevokedSession(t *testing.T) {
	db := withTestDB(t)
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")

	seedActiveSession(t, db, "jti2")
	require.NoError(t, RevokeSession(context.Background(), db, "jti2"))

	tok, _ := signer.Sign(Claims{SessionID: "jti2", UserID: "u1", AccountID: "acct1", TTL: time.Hour})
	h := RequireAuth(signer, db)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
