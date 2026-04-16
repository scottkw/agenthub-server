package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SessionInput struct {
	ID        string // matches JWT jti
	UserID    string
	AccountID string
	UserAgent string
	IP        string
	TTL       time.Duration
}

type Session struct {
	ID        string
	UserID    string
	AccountID string
	ExpiresAt time.Time
}

// CreateSession inserts a new auth_sessions row with expires_at = now+TTL.
func CreateSession(ctx context.Context, db *sql.DB, in SessionInput) (Session, error) {
	if in.TTL <= 0 {
		return Session{}, errors.New("CreateSession: TTL must be > 0")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO auth_sessions (id, user_id, account_id, user_agent, ip, expires_at)
		VALUES (?, ?, ?, ?, ?, datetime('now', ?))`,
		in.ID, in.UserID, in.AccountID, in.UserAgent, in.IP,
		fmt.Sprintf("+%d seconds", int(in.TTL.Seconds())),
	)
	if err != nil {
		return Session{}, fmt.Errorf("CreateSession: %w", err)
	}
	return Session{
		ID:        in.ID,
		UserID:    in.UserID,
		AccountID: in.AccountID,
		ExpiresAt: time.Now().Add(in.TTL),
	}, nil
}

// CheckSessionActive returns nil if the session exists, is not revoked, and
// has not expired. Returns typed errors otherwise.
func CheckSessionActive(ctx context.Context, db *sql.DB, id string) error {
	var expiresAt string
	var revokedAt sql.NullString
	row := db.QueryRowContext(ctx,
		`SELECT expires_at, revoked_at FROM auth_sessions WHERE id = ?`, id)
	err := row.Scan(&expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionRevoked // treat unknown jti like revoked
	}
	if err != nil {
		return fmt.Errorf("CheckSessionActive: %w", err)
	}
	if revokedAt.Valid {
		return ErrSessionRevoked
	}
	t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
	if err != nil {
		return fmt.Errorf("CheckSessionActive: parse expires_at: %w", err)
	}
	if time.Now().After(t) {
		return ErrSessionExpired
	}
	return nil
}

// RevokeSession marks the session revoked immediately. Idempotent.
func RevokeSession(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE auth_sessions SET revoked_at = datetime('now') WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("RevokeSession: %w", err)
	}
	return nil
}
