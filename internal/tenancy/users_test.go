package tenancy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/stretchr/testify/require"
)

func TestUsers_CreateAndGet(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	u := User{
		ID:           "u1",
		Email:        "Person@Example.com",
		Name:         "A Person",
		PasswordHash: "argon2id$…",
	}
	require.NoError(t, CreateUser(context.Background(), d.SQL(), u))

	got, err := GetUserByEmail(context.Background(), d.SQL(), "PERSON@EXAMPLE.COM")
	require.NoError(t, err)
	require.Equal(t, "u1", got.ID)
	require.Equal(t, "person@example.com", got.Email)

	err = CreateUser(context.Background(), d.SQL(), User{ID: "u2", Email: "PERSON@EXAMPLE.COM"})
	require.Error(t, err)
}

func TestUsers_MarkVerified(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	require.NoError(t, CreateUser(context.Background(), d.SQL(), User{ID: "u", Email: "a@b.com"}))
	require.NoError(t, MarkEmailVerified(context.Background(), d.SQL(), "u"))

	got, err := GetUserByID(context.Background(), d.SQL(), "u")
	require.NoError(t, err)
	require.False(t, got.EmailVerifiedAt.IsZero())
}

func TestIsOperator(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	ctx := context.Background()
	u := User{ID: ids.New(), Email: "op@example.com", Name: "Op"}
	require.NoError(t, CreateUser(ctx, d.SQL(), u))

	ok, err := IsOperator(ctx, d.SQL(), u.ID)
	require.NoError(t, err)
	require.False(t, ok)

	_, err = d.SQL().ExecContext(ctx, `UPDATE users SET is_operator = 1 WHERE id = ?`, u.ID)
	require.NoError(t, err)

	ok, err = IsOperator(ctx, d.SQL(), u.ID)
	require.NoError(t, err)
	require.True(t, ok)
}
