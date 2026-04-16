package tenancy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestAccounts_CreateAndGet(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	a := Account{ID: "a1", Slug: "team-a", Name: "Team A", Plan: "free"}
	require.NoError(t, CreateAccount(context.Background(), d.SQL(), a))

	got, err := GetAccountByID(context.Background(), d.SQL(), "a1")
	require.NoError(t, err)
	require.Equal(t, "team-a", got.Slug)

	err = CreateAccount(context.Background(), d.SQL(), Account{ID: "a2", Slug: "team-a", Name: "dupe slug"})
	require.Error(t, err)
}
