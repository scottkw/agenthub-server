package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateJWTKey_FirstCallGenerates(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	k1, err := LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)
	require.Len(t, k1, 32, "JWT HS256 key must be 32 bytes")

	// Second call returns the same key.
	k2, err := LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)
	require.Equal(t, k1, k2)
}
