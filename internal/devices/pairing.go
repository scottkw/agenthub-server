package devices

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/ids"
)

// ErrPairCodeInvalid is returned when a claim code is unknown, expired, or
// already consumed.
var ErrPairCodeInvalid = errors.New("pair code invalid or expired")

// pairCodeLen is the length of the human-shared code. 10 chars of base32
// (no padding) gives ~50 bits of entropy — well over what's needed for a
// 5-minute single-use code, and still typeable.
const pairCodeLen = 10

// base32 alphabet excluding I, O, 0, 1 to avoid visual ambiguity.
const pairCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// preAuthKeyTTL is how long the (stubbed) Headscale pre-auth key is valid.
// Spec §7 Flow B calls for 5 minutes.
const preAuthKeyTTL = 5 * time.Minute

// PairCodeInput is the caller-supplied input for IssuePairCode.
type PairCodeInput struct {
	AccountID string
	UserID    string
	TTL       time.Duration
}

// IssuePairCode creates a short-lived pair code row and returns it. The
// caller (authed user on device A) shares the code with device B out-of-band.
func IssuePairCode(ctx context.Context, db *sql.DB, in PairCodeInput) (PairCode, error) {
	if in.AccountID == "" || in.UserID == "" {
		return PairCode{}, fmt.Errorf("IssuePairCode: AccountID and UserID required")
	}
	if in.TTL == 0 {
		in.TTL = preAuthKeyTTL
	}

	code, err := randomPairCode()
	if err != nil {
		return PairCode{}, err
	}
	expires := time.Now().Add(in.TTL).UTC()

	_, err = db.ExecContext(ctx, `
		INSERT INTO device_pair_codes (code, account_id, user_id, expires_at)
		VALUES (?, ?, ?, ?)`,
		code, in.AccountID, in.UserID, expires.Format(sqliteTimeFmt),
	)
	if err != nil {
		return PairCode{}, fmt.Errorf("IssuePairCode: %w", err)
	}

	return PairCode{
		Code:      code,
		AccountID: in.AccountID,
		UserID:    in.UserID,
		ExpiresAt: expires,
	}, nil
}

// ClaimDevice consumes a pair code and creates (device + api_token) atomically,
// then mints a (stubbed) Headscale pre-auth key. Returns the full claim payload
// including the one-shot-visible raw API token.
func ClaimDevice(ctx context.Context, db *sql.DB, hs Headscaler, in ClaimInput) (ClaimOutput, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Validate + consume pair code (atomic via row lock implied by UPDATE
	//    ... WHERE consumed_at IS NULL).
	var accountID, userID, expiresStr string
	err = tx.QueryRowContext(ctx, `
		SELECT account_id, user_id, expires_at FROM device_pair_codes
		WHERE code = ? AND consumed_at IS NULL`, in.Code).Scan(&accountID, &userID, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimOutput{}, ErrPairCodeInvalid
	}
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: lookup code: %w", err)
	}
	expires, err := time.Parse(sqliteTimeFmt, expiresStr)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: parse expires_at: %w", err)
	}
	if time.Now().After(expires) {
		return ClaimOutput{}, ErrPairCodeInvalid
	}

	deviceID := ids.New()

	_, err = tx.ExecContext(ctx, `
		UPDATE device_pair_codes
		SET consumed_at = datetime('now'), consumed_by_device_id = ?
		WHERE code = ? AND consumed_at IS NULL`,
		deviceID, in.Code,
	)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: consume code: %w", err)
	}

	// 2. Insert device.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO devices (id, account_id, user_id, name, platform, app_version)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deviceID, accountID, userID, in.Name, in.Platform, in.AppVersion,
	)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: insert device: %w", err)
	}

	// 3. Mint API token bound to the device. We can't call auth.CreateAPIToken
	//    inside the tx because it takes *sql.DB; instead inline the same logic
	//    against the tx.
	tokenID := ids.New()
	raw, err := auth.GenerateAPITokenRaw()
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: gen token: %w", err)
	}
	hash := auth.HashAPIToken(raw)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_tokens (id, account_id, user_id, device_id, name, token_hash, scope)
		VALUES (?, ?, ?, ?, ?, ?, '[]')`,
		tokenID, accountID, userID, deviceID, "device:"+in.Name, hash,
	)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: insert token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: commit: %w", err)
	}

	// 4. Mint pre-auth key. Outside the tx because the real Plan-05 impl
	//    will hit an external system; failures here leave a device row
	//    without a usable key, which is recoverable (the user can reclaim).
	preauth, err := hs.MintPreAuthKey(ctx, PreAuthKeyInput{
		AccountID: accountID, UserID: userID, DeviceID: deviceID, TTL: preAuthKeyTTL,
	})
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: mint pre-auth key: %w", err)
	}

	return ClaimOutput{
		Device: Device{
			ID: deviceID, AccountID: accountID, UserID: userID,
			Name: in.Name, Platform: in.Platform, AppVersion: in.AppVersion,
		},
		APIToken:   raw,
		APITokenID: tokenID,
		PreAuthKey: preauth,
	}, nil
}

func randomPairCode() (string, error) {
	buf := make([]byte, pairCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	out := make([]byte, pairCodeLen)
	for i := range buf {
		out[i] = pairCodeAlphabet[int(buf[i])%len(pairCodeAlphabet)]
	}
	return string(out), nil
}
