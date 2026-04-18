package headscale

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

// CreateLink inserts a new headscale_user_links row.
func CreateLink(ctx context.Context, db *sql.DB, l Link) error {
	if l.AccountID == "" || l.UserID == "" || l.HeadscaleUserID == 0 || l.HeadscaleUserName == "" {
		return fmt.Errorf("CreateLink: AccountID, UserID, HeadscaleUserID, HeadscaleUserName required")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO headscale_user_links (account_id, user_id, headscale_user_id, headscale_user_name)
		VALUES (?, ?, ?, ?)`,
		l.AccountID, l.UserID, l.HeadscaleUserID, l.HeadscaleUserName,
	)
	if err != nil {
		return fmt.Errorf("CreateLink: %w", err)
	}
	return nil
}

// GetLink looks up a single link by (account_id, user_id). Returns
// sql.ErrNoRows when no row matches (callers test with errors.Is).
func GetLink(ctx context.Context, db *sql.DB, accountID, userID string) (Link, error) {
	row := db.QueryRowContext(ctx, `
		SELECT account_id, user_id, headscale_user_id, headscale_user_name, created_at
		FROM headscale_user_links
		WHERE account_id = ? AND user_id = ?`, accountID, userID)
	var l Link
	var createdAt string
	if err := row.Scan(&l.AccountID, &l.UserID, &l.HeadscaleUserID, &l.HeadscaleUserName, &createdAt); err != nil {
		return Link{}, err
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		l.CreatedAt = t
	}
	return l, nil
}
