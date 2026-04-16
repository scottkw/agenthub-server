package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashAndVerify_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(hash, "$argon2id$"))

	ok, err := VerifyPassword("correct-horse-battery-staple", hash)
	require.NoError(t, err)
	require.True(t, ok)

	wrong, err := VerifyPassword("trombone", hash)
	require.NoError(t, err)
	require.False(t, wrong)
}

func TestHashPassword_DifferentSaltsProduceDifferentHashes(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	require.NotEqual(t, h1, h2, "argon2id must salt each hash uniquely")
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	_, err := VerifyPassword("any", "not-a-hash")
	require.Error(t, err)
}

func TestHashPassword_Empty(t *testing.T) {
	_, err := HashPassword("")
	require.Error(t, err)
}
