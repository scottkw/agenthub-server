package devices

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssuePairCode_Roundtrip(t *testing.T) {
	db := withDevicesTestDB(t)

	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, pc.Code, pairCodeLen)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), pc.ExpiresAt, 5*time.Second)

	// Round-trip: the row exists and is not yet consumed.
	var consumed sql.NullString
	err = db.QueryRowContext(context.Background(),
		`SELECT consumed_at FROM device_pair_codes WHERE code = ?`, pc.Code).Scan(&consumed)
	require.NoError(t, err)
	require.False(t, consumed.Valid)
}

func TestClaimDevice_HappyPath(t *testing.T) {
	db := withDevicesTestDB(t)
	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)

	out, err := ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{
		Code: pc.Code, Name: "laptop", Platform: "darwin", AppVersion: "0.1.0",
	})
	require.NoError(t, err)

	require.NotEmpty(t, out.Device.ID)
	require.Equal(t, "laptop", out.Device.Name)
	require.True(t, strings.HasPrefix(out.APIToken, "ahs_"), "got %q", out.APIToken)
	require.NotEmpty(t, out.APITokenID)
	require.True(t, strings.HasPrefix(out.PreAuthKey.Key, "stub-"))

	// api_tokens row has device_id set.
	var devID string
	err = db.QueryRowContext(context.Background(),
		`SELECT COALESCE(device_id,'') FROM api_tokens WHERE id = ?`, out.APITokenID).Scan(&devID)
	require.NoError(t, err)
	require.Equal(t, out.Device.ID, devID)

	// pair code is marked consumed.
	var consumed sql.NullString
	err = db.QueryRowContext(context.Background(),
		`SELECT consumed_at FROM device_pair_codes WHERE code = ?`, pc.Code).Scan(&consumed)
	require.NoError(t, err)
	require.True(t, consumed.Valid)
}

func TestClaimDevice_UnknownCode(t *testing.T) {
	db := withDevicesTestDB(t)
	_, err := ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: "bogus", Name: "x"})
	require.ErrorIs(t, err, ErrPairCodeInvalid)
}

func TestClaimDevice_ExpiredCode(t *testing.T) {
	db := withDevicesTestDB(t)
	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: -time.Minute, // already expired
	})
	require.NoError(t, err)

	_, err = ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: pc.Code, Name: "x"})
	require.ErrorIs(t, err, ErrPairCodeInvalid)
}

func TestClaimDevice_ReusedCode(t *testing.T) {
	db := withDevicesTestDB(t)
	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)

	_, err = ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: pc.Code, Name: "first"})
	require.NoError(t, err)

	_, err = ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: pc.Code, Name: "second"})
	require.ErrorIs(t, err, ErrPairCodeInvalid)
}
