package sessions

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
)

func withSessionsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	db := d.SQL()
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO accounts (id, slug, name) VALUES ('acct1', 'a', 'A')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO users (id, email) VALUES ('u1', 'a@b.com')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO devices (id, account_id, user_id, name) VALUES ('dev1', 'acct1', 'u1', 'laptop')`)
	require.NoError(t, err)
	return db
}

func TestCreate_AndGet(t *testing.T) {
	db := withSessionsTestDB(t)

	err := Create(context.Background(), db, AgentSession{
		ID: "s1", AccountID: "acct1", DeviceID: "dev1",
		Label: "build", CWD: "/home/u/proj",
	})
	require.NoError(t, err)

	got, err := GetByID(context.Background(), db, "s1")
	require.NoError(t, err)
	require.Equal(t, "s1", got.ID)
	require.Equal(t, StatusRunning, got.Status)
	require.Equal(t, "build", got.Label)
}

func TestListForAccount(t *testing.T) {
	db := withSessionsTestDB(t)
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "a", AccountID: "acct1", DeviceID: "dev1", Label: "one"}))
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "b", AccountID: "acct1", DeviceID: "dev1", Label: "two"}))

	list, err := ListForAccount(context.Background(), db, "acct1")
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestTouchActivity(t *testing.T) {
	db := withSessionsTestDB(t)
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "s", AccountID: "acct1", DeviceID: "dev1"}))

	// Force last_activity_at back by a few seconds so we can see it move.
	_, err := db.ExecContext(context.Background(),
		`UPDATE agent_sessions SET last_activity_at = datetime('now','-1 hour') WHERE id = 's'`)
	require.NoError(t, err)

	before, err := GetByID(context.Background(), db, "s")
	require.NoError(t, err)

	require.NoError(t, TouchActivity(context.Background(), db, "s"))

	after, err := GetByID(context.Background(), db, "s")
	require.NoError(t, err)
	require.True(t, after.LastActivityAt.After(before.LastActivityAt),
		"before=%v after=%v", before.LastActivityAt, after.LastActivityAt)
}

func TestEnd(t *testing.T) {
	db := withSessionsTestDB(t)
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "s", AccountID: "acct1", DeviceID: "dev1"}))

	require.NoError(t, End(context.Background(), db, "s"))

	got, err := GetByID(context.Background(), db, "s")
	require.NoError(t, err)
	require.Equal(t, StatusStopped, got.Status)
	require.False(t, got.EndedAt.IsZero())
}
