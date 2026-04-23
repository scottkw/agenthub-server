package blobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/tenancy"
)

func TestBlobs_CRUD(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	ctx := context.Background()
	acctID := ids.New()
	require.NoError(t, tenancy.CreateAccount(ctx, d.SQL(), tenancy.Account{ID: acctID, Name: "Blob", Slug: "blob"}))
	userID := ids.New()
	require.NoError(t, tenancy.CreateUser(ctx, d.SQL(), tenancy.User{ID: userID, Email: "blob@example.com", Name: "Blob User"}))

	obj := BlobObject{
		ID:              ids.New(),
		AccountID:       acctID,
		Key:             "test-key-1",
		ContentType:     "text/plain",
		SizeBytes:       42,
		SHA256:          "deadbeef",
		CreatedByUserID: userID,
	}
	require.NoError(t, Create(ctx, d.SQL(), obj))

	got, err := GetByID(ctx, d.SQL(), acctID, obj.ID)
	require.NoError(t, err)
	require.Equal(t, obj.Key, got.Key)
	require.Equal(t, obj.ContentType, got.ContentType)
	require.Equal(t, obj.SizeBytes, got.SizeBytes)

	list, err := ListForAccount(ctx, d.SQL(), acctID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, Delete(ctx, d.SQL(), acctID, obj.ID))
	_, err = GetByID(ctx, d.SQL(), acctID, obj.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
