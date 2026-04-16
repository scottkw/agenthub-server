package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func withTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))
	// Seed one user + one account (FKs).
	_, err = d.SQL().ExecContext(context.Background(),
		`INSERT INTO users (id, email) VALUES ('u1', 'a@b.com')`)
	require.NoError(t, err)
	_, err = d.SQL().ExecContext(context.Background(),
		`INSERT INTO accounts (id, slug, name) VALUES ('acct1', 'a', 'A')`)
	require.NoError(t, err)
	return d.SQL()
}

func TestCreateSession_AndCheck(t *testing.T) {
	db := withTestDB(t)

	sess, err := CreateSession(context.Background(), db, SessionInput{
		ID:        "sess1",
		UserID:    "u1",
		AccountID: "acct1",
		UserAgent: "ua",
		IP:        "1.2.3.4",
		TTL:       time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, "sess1", sess.ID)

	require.NoError(t, CheckSessionActive(context.Background(), db, "sess1"))
}

func TestRevokeSession(t *testing.T) {
	db := withTestDB(t)

	_, err := CreateSession(context.Background(), db, SessionInput{
		ID:        "sess1",
		UserID:    "u1",
		AccountID: "acct1",
		TTL:       time.Hour,
	})
	require.NoError(t, err)

	require.NoError(t, RevokeSession(context.Background(), db, "sess1"))

	err = CheckSessionActive(context.Background(), db, "sess1")
	require.ErrorIs(t, err, ErrSessionRevoked)
}

func TestCheckSession_Expired(t *testing.T) {
	db := withTestDB(t)

	// Insert an already-expired session directly.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO auth_sessions (id, user_id, account_id, expires_at) VALUES ('s', 'u1', 'acct1', datetime('now','-1 hour'))`)
	require.NoError(t, err)

	err = CheckSessionActive(context.Background(), db, "s")
	require.ErrorIs(t, err, ErrSessionExpired)
}
