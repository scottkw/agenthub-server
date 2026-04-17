package devices

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
)

func withDevicesTestDB(t *testing.T) *sql.DB {
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

func TestCreateDevice_AndGet(t *testing.T) {
	db := withDevicesTestDB(t)

	err := CreateDevice(context.Background(), db, Device{
		ID: "dev1", AccountID: "acct1", UserID: "u1",
		Name: "laptop", Platform: "darwin", AppVersion: "0.1.0",
	})
	require.NoError(t, err)

	got, err := GetDeviceByID(context.Background(), db, "dev1")
	require.NoError(t, err)
	require.Equal(t, "dev1", got.ID)
	require.Equal(t, "laptop", got.Name)
	require.Equal(t, "darwin", got.Platform)
	require.False(t, got.CreatedAt.IsZero())
}

func TestGetDevice_NotFound(t *testing.T) {
	db := withDevicesTestDB(t)
	_, err := GetDeviceByID(context.Background(), db, "nope")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestListDevicesForAccount(t *testing.T) {
	db := withDevicesTestDB(t)

	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "a", AccountID: "acct1", UserID: "u1", Name: "one"}))
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "b", AccountID: "acct1", UserID: "u1", Name: "two"}))

	list, err := ListDevicesForAccount(context.Background(), db, "acct1")
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestListDevices_ExcludesSoftDeleted(t *testing.T) {
	db := withDevicesTestDB(t)
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "a", AccountID: "acct1", UserID: "u1", Name: "keep"}))
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "b", AccountID: "acct1", UserID: "u1", Name: "trash"}))

	require.NoError(t, SoftDeleteDevice(context.Background(), db, "b"))

	list, err := ListDevicesForAccount(context.Background(), db, "acct1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "a", list[0].ID)
}

func TestUpdateTailscaleInfo(t *testing.T) {
	db := withDevicesTestDB(t)
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "dev1", AccountID: "acct1", UserID: "u1"}))

	require.NoError(t, UpdateTailscaleInfo(context.Background(), db, "dev1", "ts-node-xyz"))

	got, err := GetDeviceByID(context.Background(), db, "dev1")
	require.NoError(t, err)
	require.Equal(t, "ts-node-xyz", got.TailscaleNodeID)
	require.False(t, got.LastSeenAt.IsZero())
}
