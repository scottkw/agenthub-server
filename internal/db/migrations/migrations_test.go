package migrations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestApplySQLite_CreatesAppMeta(t *testing.T) {
	dir := t.TempDir()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(dir, "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	require.NoError(t, Apply(context.Background(), d))

	var count int
	err = d.SQL().QueryRow("SELECT COUNT(*) FROM app_meta WHERE key='schema_created_at'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestApplySQLite_Idempotent(t *testing.T) {
	dir := t.TempDir()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(dir, "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	require.NoError(t, Apply(context.Background(), d))
	require.NoError(t, Apply(context.Background(), d)) // second apply must be a no-op
}
