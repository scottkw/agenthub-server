package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthStateStore_CreateAndConsume(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateOAuthState(context.Background(), db, OAuthStateInput{
		Provider:    OAuthProviderGoogle,
		RedirectURI: "http://client/after",
		TTL:         5 * time.Minute,
	})
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	got, err := ConsumeOAuthState(context.Background(), db, raw)
	require.NoError(t, err)
	require.Equal(t, OAuthProviderGoogle, got.Provider)
	require.Equal(t, "http://client/after", got.RedirectURI)

	_, err = ConsumeOAuthState(context.Background(), db, raw)
	require.ErrorIs(t, err, ErrTokenNotFound)
}

func TestOAuthStateStore_Expired(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateOAuthState(context.Background(), db, OAuthStateInput{
		Provider:    OAuthProviderGitHub,
		RedirectURI: "x",
		TTL:         1 * time.Nanosecond,
	})
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)

	_, err = ConsumeOAuthState(context.Background(), db, raw)
	require.ErrorIs(t, err, ErrTokenExpired)
}

func TestProviderConfig_Google(t *testing.T) {
	cfg := GoogleConfig{ClientID: "cid", ClientSecret: "sec", RedirectURL: "http://x/cb"}
	oc := cfg.OAuth2()
	require.Equal(t, "cid", oc.ClientID)
	require.NotEmpty(t, oc.Endpoint.AuthURL)
	require.Contains(t, oc.Scopes, "openid")
	require.Contains(t, oc.Scopes, "email")
}

func TestProviderConfig_GitHub(t *testing.T) {
	cfg := GitHubConfig{ClientID: "cid", ClientSecret: "sec", RedirectURL: "http://x/cb"}
	oc := cfg.OAuth2()
	require.Equal(t, "cid", oc.ClientID)
	require.NotEmpty(t, oc.Endpoint.AuthURL)
	require.Contains(t, oc.Scopes, "user:email")
}
