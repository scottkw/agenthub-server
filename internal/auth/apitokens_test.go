package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAPIToken_CreateAndLookup(t *testing.T) {
	db := withTestDB(t)

	raw, rec, err := CreateAPIToken(context.Background(), db, APITokenInput{
		ID:        "t1",
		AccountID: "acct1",
		UserID:    "u1",
		Name:      "my cli",
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(raw, "ahs_"))
	require.Equal(t, "t1", rec.ID)

	got, err := LookupAPIToken(context.Background(), db, raw)
	require.NoError(t, err)
	require.Equal(t, "t1", got.ID)
	require.Equal(t, "u1", got.UserID)
	require.Equal(t, "acct1", got.AccountID)
}

func TestAPIToken_WrongTokenRejected(t *testing.T) {
	db := withTestDB(t)
	_, err := LookupAPIToken(context.Background(), db, "ahs_doesnotexist")
	require.ErrorIs(t, err, ErrTokenNotFound)
}

func TestAPIToken_Revoked(t *testing.T) {
	db := withTestDB(t)
	raw, _, err := CreateAPIToken(context.Background(), db, APITokenInput{ID: "t", AccountID: "acct1", UserID: "u1"})
	require.NoError(t, err)

	require.NoError(t, RevokeAPIToken(context.Background(), db, "t"))
	_, err = LookupAPIToken(context.Background(), db, raw)
	require.ErrorIs(t, err, ErrTokenNotFound)
}

func TestAPIToken_Expired(t *testing.T) {
	db := withTestDB(t)
	exp := time.Now().Add(-time.Minute)
	raw, _, err := CreateAPIToken(context.Background(), db, APITokenInput{
		ID: "t", AccountID: "acct1", UserID: "u1", ExpiresAt: &exp,
	})
	require.NoError(t, err)

	_, err = LookupAPIToken(context.Background(), db, raw)
	require.ErrorIs(t, err, ErrTokenExpired)
}

func TestAPIToken_ListForUser(t *testing.T) {
	db := withTestDB(t)
	_, _, err := CreateAPIToken(context.Background(), db, APITokenInput{ID: "a", AccountID: "acct1", UserID: "u1", Name: "alpha"})
	require.NoError(t, err)
	_, _, err = CreateAPIToken(context.Background(), db, APITokenInput{ID: "b", AccountID: "acct1", UserID: "u1", Name: "beta"})
	require.NoError(t, err)

	list, err := ListAPITokens(context.Background(), db, "acct1", "u1")
	require.NoError(t, err)
	require.Len(t, list, 2)
}
