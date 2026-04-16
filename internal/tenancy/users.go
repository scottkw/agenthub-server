package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

// CreateUser inserts a new user. Email is lowercased before insert.
func CreateUser(ctx context.Context, db *sql.DB, u User) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, name, avatar_url)
		VALUES (?, ?, ?, ?, ?)`,
		u.ID, strings.ToLower(u.Email), nullIfEmpty(u.PasswordHash), u.Name, u.AvatarURL,
	)
	if err != nil {
		return fmt.Errorf("CreateUser: %w", err)
	}
	return nil
}

// GetUserByEmail looks up by lowercased email.
func GetUserByEmail(ctx context.Context, db *sql.DB, email string) (User, error) {
	return scanUser(db.QueryRowContext(ctx, selectUser+" WHERE email = ? AND deleted_at IS NULL",
		strings.ToLower(email)))
}

// GetUserByID looks up by primary key.
func GetUserByID(ctx context.Context, db *sql.DB, id string) (User, error) {
	return scanUser(db.QueryRowContext(ctx, selectUser+" WHERE id = ? AND deleted_at IS NULL", id))
}

// MarkEmailVerified sets email_verified_at = now() for the given user.
func MarkEmailVerified(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET email_verified_at = datetime('now') WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("MarkEmailVerified: %w", err)
	}
	return nil
}

// UpdatePasswordHash stores a new argon2id-encoded hash.
func UpdatePasswordHash(ctx context.Context, db *sql.DB, id, hash string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ? AND deleted_at IS NULL`, hash, id)
	if err != nil {
		return fmt.Errorf("UpdatePasswordHash: %w", err)
	}
	return nil
}

const selectUser = `SELECT id, email, COALESCE(password_hash, ''), COALESCE(email_verified_at, ''), name, avatar_url, created_at FROM users`

func scanUser(row *sql.Row) (User, error) {
	var u User
	var verifiedAt string
	var createdAt string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &verifiedAt, &u.Name, &u.AvatarURL, &createdAt)
	if err != nil {
		return User{}, err
	}
	if verifiedAt != "" {
		if t, err := time.Parse(sqliteTimeFmt, verifiedAt); err == nil {
			u.EmailVerifiedAt = t
		}
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		u.CreatedAt = t
	}
	return u, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
