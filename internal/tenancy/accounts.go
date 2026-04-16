package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateAccount inserts a new account row.
func CreateAccount(ctx context.Context, db *sql.DB, a Account) error {
	plan := a.Plan
	if plan == "" {
		plan = "self_hosted"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO accounts (id, slug, name, plan) VALUES (?, ?, ?, ?)`,
		a.ID, a.Slug, a.Name, plan,
	)
	if err != nil {
		return fmt.Errorf("CreateAccount: %w", err)
	}
	return nil
}

// GetAccountByID looks up by primary key.
func GetAccountByID(ctx context.Context, db *sql.DB, id string) (Account, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, slug, name, plan, created_at FROM accounts WHERE id = ? AND deleted_at IS NULL`, id)
	var a Account
	var createdAt string
	if err := row.Scan(&a.ID, &a.Slug, &a.Name, &a.Plan, &createdAt); err != nil {
		return Account{}, err
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		a.CreatedAt = t
	}
	return a, nil
}
