package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const apiTokenPrefix = "ahs_"

type APITokenInput struct {
	ID        string
	AccountID string
	UserID    string
	DeviceID  string
	Name      string
	Scope     []string
	ExpiresAt *time.Time
}

type APIToken struct {
	ID         string
	AccountID  string
	UserID     string
	DeviceID   string
	Name       string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// CreateAPIToken generates a random 32-byte token with ahs_ prefix, stores
// sha256(raw) in the DB, and returns the raw token + the record. The raw is
// the caller's only chance to see it.
func CreateAPIToken(ctx context.Context, db *sql.DB, in APITokenInput) (string, APIToken, error) {
	if in.ID == "" || in.AccountID == "" || in.UserID == "" {
		return "", APIToken{}, errors.New("CreateAPIToken: ID, AccountID, UserID required")
	}

	raw, err := GenerateAPITokenRaw()
	if err != nil {
		return "", APIToken{}, err
	}
	hash := HashAPIToken(raw)

	scopeJSON := `[]`
	if len(in.Scope) > 0 {
		parts := make([]string, 0, len(in.Scope))
		for _, s := range in.Scope {
			parts = append(parts, fmt.Sprintf("%q", s))
		}
		scopeJSON = "[" + strings.Join(parts, ",") + "]"
	}

	var expiresAt any
	if in.ExpiresAt != nil {
		expiresAt = in.ExpiresAt.UTC().Format("2006-01-02 15:04:05")
	}

	var deviceID any
	if in.DeviceID != "" {
		deviceID = in.DeviceID
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO api_tokens (id, account_id, user_id, device_id, name, token_hash, scope, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.AccountID, in.UserID, deviceID, in.Name, hash, scopeJSON, expiresAt,
	)
	if err != nil {
		return "", APIToken{}, fmt.Errorf("CreateAPIToken: %w", err)
	}

	rec := APIToken{
		ID: in.ID, AccountID: in.AccountID, UserID: in.UserID,
		DeviceID: in.DeviceID, Name: in.Name,
	}
	if in.ExpiresAt != nil {
		rec.ExpiresAt = *in.ExpiresAt
	}
	return raw, rec, nil
}

// LookupAPIToken returns the token record if raw matches an active non-expired
// non-revoked row. Otherwise ErrTokenNotFound or ErrTokenExpired.
// Updates last_used_at as a side effect.
func LookupAPIToken(ctx context.Context, db *sql.DB, raw string) (APIToken, error) {
	hash := sha256Hex(raw)
	row := db.QueryRowContext(ctx, `
		SELECT id, account_id, user_id, COALESCE(device_id,''), name, created_at,
		       COALESCE(last_used_at,''), COALESCE(expires_at,''), revoked_at
		FROM api_tokens
		WHERE token_hash = ?`, hash)
	var rec APIToken
	var createdAt, lastUsedAt, expiresAt string
	var revokedAt sql.NullString
	err := row.Scan(&rec.ID, &rec.AccountID, &rec.UserID, &rec.DeviceID, &rec.Name,
		&createdAt, &lastUsedAt, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrTokenNotFound
	}
	if err != nil {
		return APIToken{}, fmt.Errorf("LookupAPIToken: %w", err)
	}
	if revokedAt.Valid {
		return APIToken{}, ErrTokenNotFound
	}
	if expiresAt != "" {
		t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
		if err != nil {
			return APIToken{}, fmt.Errorf("parse expires_at: %w", err)
		}
		if time.Now().After(t) {
			return APIToken{}, ErrTokenExpired
		}
		rec.ExpiresAt = t
	}
	if createdAt != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
			rec.CreatedAt = t
		}
	}
	if lastUsedAt != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", lastUsedAt); err == nil {
			rec.LastUsedAt = t
		}
	}

	_, _ = db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = datetime('now') WHERE id = ?`, rec.ID)
	return rec, nil
}

// RevokeAPIToken marks the token revoked immediately. Idempotent.
func RevokeAPIToken(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = datetime('now') WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("RevokeAPIToken: %w", err)
	}
	return nil
}

// GenerateAPITokenRaw returns a fresh raw token with the ahs_ prefix.
// Exposed so callers that need to insert an api_tokens row inside a
// *sql.Tx (e.g. devices.ClaimDevice) can reuse our format.
func GenerateAPITokenRaw() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return apiTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashAPIToken returns the hex-encoded sha256 of raw, matching the
// storage format used by CreateAPIToken / LookupAPIToken.
func HashAPIToken(raw string) string {
	return sha256Hex(raw)
}

// ListAPITokens returns all non-revoked tokens for a given (account, user).
func ListAPITokens(ctx context.Context, db *sql.DB, accountID, userID string) ([]APIToken, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, account_id, user_id, COALESCE(device_id,''), name, created_at,
		       COALESCE(last_used_at,''), COALESCE(expires_at,'')
		FROM api_tokens
		WHERE account_id = ? AND user_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC`, accountID, userID)
	if err != nil {
		return nil, fmt.Errorf("ListAPITokens: %w", err)
	}
	defer rows.Close()

	var out []APIToken
	for rows.Next() {
		var rec APIToken
		var createdAt, lastUsedAt, expiresAt string
		if err := rows.Scan(&rec.ID, &rec.AccountID, &rec.UserID, &rec.DeviceID, &rec.Name,
			&createdAt, &lastUsedAt, &expiresAt); err != nil {
			return nil, err
		}
		if createdAt != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
				rec.CreatedAt = t
			}
		}
		if lastUsedAt != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", lastUsedAt); err == nil {
				rec.LastUsedAt = t
			}
		}
		if expiresAt != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", expiresAt); err == nil {
				rec.ExpiresAt = t
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
