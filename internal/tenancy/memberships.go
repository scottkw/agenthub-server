package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AddMembership inserts a membership row.
func AddMembership(ctx context.Context, db *sql.DB, m Membership) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO memberships (id, account_id, user_id, role) VALUES (?, ?, ?, ?)`,
		m.ID, m.AccountID, m.UserID, string(m.Role),
	)
	if err != nil {
		return fmt.Errorf("AddMembership: %w", err)
	}
	return nil
}

// GetMembershipByAccountUser returns the membership for a given account+user pair.
func GetMembershipByAccountUser(ctx context.Context, db *sql.DB, accountID, userID string) (Membership, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, account_id, user_id, role, created_at FROM memberships WHERE account_id = ? AND user_id = ?`,
		accountID, userID)
	var m Membership
	var role string
	var createdAt string
	if err := row.Scan(&m.ID, &m.AccountID, &m.UserID, &role, &createdAt); err != nil {
		return Membership{}, err
	}
	m.Role = Role(role)
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		m.CreatedAt = t
	}
	return m, nil
}
