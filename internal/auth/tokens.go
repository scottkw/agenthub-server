package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

type Purpose string

const (
	PurposeEmailVerify   Purpose = "email_verify"
	PurposePasswordReset Purpose = "password_reset"
)

type VerificationTokenInput struct {
	ID      string
	Purpose Purpose
	UserID  string
	Email   string
	TTL     time.Duration
}

type VerificationToken struct {
	ID      string
	Purpose Purpose
	UserID  string
	Email   string
}

// CreateVerificationToken generates a 32-byte random token, stores sha256(token)
// in DB, and returns the raw token to the caller for delivery via email.
// The raw token is never persisted.
func CreateVerificationToken(ctx context.Context, db *sql.DB, in VerificationTokenInput) (string, error) {
	if in.ID == "" || in.Email == "" {
		return "", errors.New("CreateVerificationToken: ID and Email required")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	rawStr := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256Hex(rawStr)

	var userID any
	if in.UserID == "" {
		userID = nil
	} else {
		userID = in.UserID
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO verification_tokens (id, purpose, user_id, email, token_hash, expires_at)
		VALUES (?, ?, ?, ?, ?, datetime('now', ?))`,
		in.ID, string(in.Purpose), userID, in.Email, hash,
		fmt.Sprintf("+%d seconds", int(in.TTL.Seconds())),
	)
	if err != nil {
		return "", fmt.Errorf("CreateVerificationToken: %w", err)
	}
	return rawStr, nil
}

// ConsumeVerificationToken atomically verifies and marks the token consumed,
// returning the token row. Returns ErrTokenNotFound for mismatched
// hash/purpose, and ErrTokenExpired for an expired token.
func ConsumeVerificationToken(ctx context.Context, db *sql.DB, raw string, purpose Purpose) (VerificationToken, error) {
	hash := sha256Hex(raw)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return VerificationToken{}, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT id, purpose, COALESCE(user_id,''), email, expires_at, consumed_at
		FROM verification_tokens
		WHERE token_hash = ? AND purpose = ?`,
		hash, string(purpose))

	var tok VerificationToken
	var tokPurpose string
	var expiresAt string
	var consumedAt sql.NullString
	err = row.Scan(&tok.ID, &tokPurpose, &tok.UserID, &tok.Email, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return VerificationToken{}, ErrTokenNotFound
	}
	if err != nil {
		return VerificationToken{}, fmt.Errorf("ConsumeVerificationToken lookup: %w", err)
	}
	if consumedAt.Valid {
		return VerificationToken{}, ErrTokenNotFound
	}

	t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
	if err != nil {
		return VerificationToken{}, fmt.Errorf("parse expires_at: %w", err)
	}
	if time.Now().After(t) {
		return VerificationToken{}, ErrTokenExpired
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE verification_tokens SET consumed_at = datetime('now') WHERE id = ?`, tok.ID,
	); err != nil {
		return VerificationToken{}, fmt.Errorf("mark consumed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return VerificationToken{}, fmt.Errorf("commit: %w", err)
	}

	tok.Purpose = Purpose(tokPurpose)
	return tok, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}
