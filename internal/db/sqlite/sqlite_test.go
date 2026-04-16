package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpen_CreatesFileAndPings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	d, err := Open(Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	require.Equal(t, "sqlite", d.Driver())
	require.NotNil(t, d.SQL())
	require.NoError(t, d.Ping(context.Background()))

	// WAL is configured — we can query the pragma.
	var mode string
	require.NoError(t, d.SQL().QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)

	var fk int
	require.NoError(t, d.SQL().QueryRow("PRAGMA foreign_keys").Scan(&fk))
	require.Equal(t, 1, fk)
}

func TestOpen_InMemory(t *testing.T) {
	d, err := Open(Options{Path: ":memory:"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, d.Ping(context.Background()))
}
