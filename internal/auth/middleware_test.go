package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/tenancy"
)

func TestRequireOperator(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	key, _ := LoadOrCreateJWTKey(context.Background(), d.SQL())
	signer := NewJWTSigner(key, "test")
	svc := NewService(Config{DB: d.SQL(), Signer: signer})

	ctx := context.Background()
	require.NoError(t, tenancy.CreateAccount(ctx, d.SQL(), tenancy.Account{ID: "acct1", Name: "A", Slug: "a"}))
	u := tenancy.User{ID: ids.New(), Email: "norm@example.com", Name: "Norm"}
	require.NoError(t, tenancy.CreateUser(ctx, d.SQL(), u))
	op := tenancy.User{ID: ids.New(), Email: "op@example.com", Name: "Op"}
	require.NoError(t, tenancy.CreateUser(ctx, d.SQL(), op))
	_, err = d.SQL().ExecContext(ctx, `UPDATE users SET is_operator = 1 WHERE id = ?`, op.ID)
	require.NoError(t, err)

	// Create a session for op so CheckSessionActive passes.
	sess, err := CreateSession(ctx, d.SQL(), SessionInput{ID: ids.New(), UserID: op.ID, AccountID: "acct1", TTL: time.Hour})
	require.NoError(t, err)
	tok, err := signer.Sign(Claims{UserID: op.ID, AccountID: "acct1", SessionID: sess.ID, TTL: time.Hour})
	require.NoError(t, err)

	normSess, err := CreateSession(ctx, d.SQL(), SessionInput{ID: ids.New(), UserID: u.ID, AccountID: "acct1", TTL: time.Hour})
	require.NoError(t, err)
	normTok, err := signer.Sign(Claims{UserID: u.ID, AccountID: "acct1", SessionID: normSess.ID, TTL: time.Hour})
	require.NoError(t, err)

	handler := RequireAuth(signer, d.SQL())(RequireOperator(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// Operator succeeds.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	// Non-operator gets 403.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer "+normTok)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	require.Equal(t, http.StatusForbidden, rr2.Code)
}
