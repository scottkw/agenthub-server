// Package blobs owns the blob_objects table: metadata for objects stored
// via the internal/blob storage abstraction.
package blobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

// BlobObject is the metadata row for one stored object.
type BlobObject struct {
	ID              string
	AccountID       string
	Key             string
	ContentType     string
	SizeBytes       int64
	SHA256          string
	CreatedByUserID string
	CreatedAt       time.Time
}

// Create inserts a blob_objects row.
func Create(ctx context.Context, db *sql.DB, obj BlobObject) error {
	if obj.ID == "" || obj.AccountID == "" || obj.Key == "" || obj.CreatedByUserID == "" {
		return fmt.Errorf("blobs.Create: ID, AccountID, Key, CreatedByUserID required")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO blob_objects (id, account_id, key, content_type, size_bytes, sha256, created_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		obj.ID, obj.AccountID, obj.Key, obj.ContentType, obj.SizeBytes, obj.SHA256, obj.CreatedByUserID,
	)
	if err != nil {
		return fmt.Errorf("blobs.Create: %w", err)
	}
	return nil
}

// GetByID returns a blob object by ID, scoped to account (returns sql.ErrNoRows if missing or wrong account).
func GetByID(ctx context.Context, db *sql.DB, accountID, id string) (BlobObject, error) {
	row := db.QueryRowContext(ctx, selectBlob+` WHERE id = ? AND account_id = ?`, id, accountID)
	return scanBlob(row)
}

// ListForAccount returns all blob objects for an account, newest first.
func ListForAccount(ctx context.Context, db *sql.DB, accountID string) ([]BlobObject, error) {
	rows, err := db.QueryContext(ctx, selectBlob+` WHERE account_id = ? ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("blobs.ListForAccount: %w", err)
	}
	defer rows.Close()

	var out []BlobObject
	for rows.Next() {
		b, err := scanBlobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Delete removes a blob object row by ID, scoped to account.
func Delete(ctx context.Context, db *sql.DB, accountID, id string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM blob_objects WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return fmt.Errorf("blobs.Delete: %w", err)
	}
	return nil
}

const selectBlob = `
	SELECT id, account_id, key, content_type, size_bytes, sha256, created_by_user_id, created_at
	FROM blob_objects`

func scanBlob(row *sql.Row) (BlobObject, error) {
	var b BlobObject
	var createdAt string
	if err := row.Scan(&b.ID, &b.AccountID, &b.Key, &b.ContentType,
		&b.SizeBytes, &b.SHA256, &b.CreatedByUserID, &createdAt); err != nil {
		return BlobObject{}, err
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		b.CreatedAt = t
	}
	return b, nil
}

func scanBlobRows(rows *sql.Rows) (BlobObject, error) {
	var b BlobObject
	var createdAt string
	if err := rows.Scan(&b.ID, &b.AccountID, &b.Key, &b.ContentType,
		&b.SizeBytes, &b.SHA256, &b.CreatedByUserID, &createdAt); err != nil {
		return BlobObject{}, err
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		b.CreatedAt = t
	}
	return b, nil
}
