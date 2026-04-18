package headscale

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
)

func withBridgeTestDB(t *testing.T) *sql.DB {
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
	return db
}

func TestBridge_CreateAndGet(t *testing.T) {
	db := withBridgeTestDB(t)

	err := CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1",
		HeadscaleUserID: 7, HeadscaleUserName: "u-u1",
	})
	require.NoError(t, err)

	got, err := GetLink(context.Background(), db, "acct1", "u1")
	require.NoError(t, err)
	require.Equal(t, uint64(7), got.HeadscaleUserID)
	require.Equal(t, "u-u1", got.HeadscaleUserName)
	require.False(t, got.CreatedAt.IsZero())
}

func TestBridge_GetMissing(t *testing.T) {
	db := withBridgeTestDB(t)
	_, err := GetLink(context.Background(), db, "acct1", "u1")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestBridge_DuplicateRejected(t *testing.T) {
	db := withBridgeTestDB(t)
	require.NoError(t, CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1", HeadscaleUserID: 7, HeadscaleUserName: "u-u1",
	}))
	err := CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1", HeadscaleUserID: 8, HeadscaleUserName: "u-u1-2",
	})
	require.Error(t, err)
}
