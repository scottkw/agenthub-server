package tenancy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMemberships_Add(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	require.NoError(t, CreateUser(context.Background(), d.SQL(), User{ID: "u", Email: "u@x.com"}))
	require.NoError(t, CreateAccount(context.Background(), d.SQL(), Account{ID: "a", Slug: "a", Name: "A"}))

	m := Membership{ID: "m", AccountID: "a", UserID: "u", Role: RoleOwner}
	require.NoError(t, AddMembership(context.Background(), d.SQL(), m))

	// Duplicate (account,user) rejected.
	err = AddMembership(context.Background(), d.SQL(), Membership{ID: "m2", AccountID: "a", UserID: "u", Role: RoleMember})
	require.Error(t, err)

	got, err := GetMembershipByAccountUser(context.Background(), d.SQL(), "a", "u")
	require.NoError(t, err)
	require.Equal(t, RoleOwner, got.Role)
}
