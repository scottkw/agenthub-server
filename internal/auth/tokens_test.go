package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerificationToken_CreateAndConsume(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateVerificationToken(context.Background(), db, VerificationTokenInput{
		ID:      "tok1",
		Purpose: PurposeEmailVerify,
		UserID:  "u1",
		Email:   "a@b.com",
		TTL:     24 * time.Hour,
	})
	require.NoError(t, err)
	require.NotEmpty(t, raw, "caller gets the raw token; DB stores only the hash")

	got, err := ConsumeVerificationToken(context.Background(), db, raw, PurposeEmailVerify)
	require.NoError(t, err)
	require.Equal(t, "u1", got.UserID)

	// Second consume fails.
	_, err = ConsumeVerificationToken(context.Background(), db, raw, PurposeEmailVerify)
	require.ErrorIs(t, err, ErrTokenNotFound)
}

func TestVerificationToken_Expired(t *testing.T) {
	db := withTestDB(t)

	// Use a TTL of 1 nanosecond plus a sleep to simulate expiry (Create rejects <=0).
	raw, err := CreateVerificationToken(context.Background(), db, VerificationTokenInput{
		ID:      "t2",
		Purpose: PurposePasswordReset,
		UserID:  "u1",
		Email:   "a@b.com",
		TTL:     1 * time.Nanosecond,
	})
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)

	_, err = ConsumeVerificationToken(context.Background(), db, raw, PurposePasswordReset)
	require.ErrorIs(t, err, ErrTokenExpired)
}

func TestVerificationToken_WrongPurpose(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateVerificationToken(context.Background(), db, VerificationTokenInput{
		ID: "t3", Purpose: PurposeEmailVerify, UserID: "u1", Email: "a@b.com", TTL: time.Hour,
	})
	require.NoError(t, err)

	_, err = ConsumeVerificationToken(context.Background(), db, raw, PurposePasswordReset)
	require.ErrorIs(t, err, ErrTokenNotFound)
}
