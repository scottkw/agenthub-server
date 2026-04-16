package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignAndParse_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	signer := NewJWTSigner(key, "agenthub-server")

	token, err := signer.Sign(Claims{
		SessionID: "sess-1",
		UserID:    "user-1",
		AccountID: "acct-1",
		TTL:       1 * time.Hour,
	})
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := signer.Parse(token)
	require.NoError(t, err)
	require.Equal(t, "sess-1", claims.SessionID)
	require.Equal(t, "user-1", claims.UserID)
	require.Equal(t, "acct-1", claims.AccountID)
	require.True(t, claims.ExpiresAt.After(time.Now()))
}

func TestParse_Expired(t *testing.T) {
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")
	token, err := signer.Sign(Claims{
		SessionID: "s",
		UserID:    "u",
		AccountID: "a",
		TTL:       1 * time.Nanosecond,
	})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	_, err = signer.Parse(token)
	require.Error(t, err)
}

func TestParse_WrongKey(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	k2[0] = 1
	signer := NewJWTSigner(k1, "agenthub-server")
	tampered := NewJWTSigner(k2, "agenthub-server")

	tok, err := signer.Sign(Claims{SessionID: "s", UserID: "u", AccountID: "a", TTL: time.Hour})
	require.NoError(t, err)

	_, err = tampered.Parse(tok)
	require.Error(t, err)
}

func TestParse_WrongAlgorithm(t *testing.T) {
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")
	// Handcraft a token with alg=none — must be rejected.
	bad := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1In0."
	_, err := signer.Parse(bad)
	require.Error(t, err)
}
