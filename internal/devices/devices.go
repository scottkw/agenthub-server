package devices

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

// CreateDevice inserts a new device row.
func CreateDevice(ctx context.Context, db *sql.DB, d Device) error {
	if d.ID == "" || d.AccountID == "" || d.UserID == "" {
		return fmt.Errorf("CreateDevice: ID, AccountID, UserID required")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO devices (id, account_id, user_id, name, platform, app_version)
		VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.AccountID, d.UserID, d.Name, d.Platform, d.AppVersion,
	)
	if err != nil {
		return fmt.Errorf("CreateDevice: %w", err)
	}
	return nil
}

// GetDeviceByID returns the device (excluding soft-deleted rows).
func GetDeviceByID(ctx context.Context, db *sql.DB, id string) (Device, error) {
	row := db.QueryRowContext(ctx,
		selectDevice+` WHERE id = ? AND deleted_at IS NULL`, id)
	return scanDevice(row)
}

// ListDevicesForAccount returns all non-deleted devices for an account,
// newest first.
func ListDevicesForAccount(ctx context.Context, db *sql.DB, accountID string) ([]Device, error) {
	rows, err := db.QueryContext(ctx,
		selectDevice+` WHERE account_id = ? AND deleted_at IS NULL ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("ListDevicesForAccount: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		d, err := scanDeviceRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SoftDeleteDevice sets deleted_at = now() if not already deleted. Idempotent.
func SoftDeleteDevice(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE devices SET deleted_at = datetime('now') WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("SoftDeleteDevice: %w", err)
	}
	return nil
}

// UpdateTailscaleInfo records the Tailscale node id and stamps last_seen_at.
// Called by a device after joining the tailnet.
func UpdateTailscaleInfo(ctx context.Context, db *sql.DB, id, nodeID string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE devices SET tailscale_node_id = ?, last_seen_at = datetime('now')
		WHERE id = ? AND deleted_at IS NULL`,
		nodeID, id,
	)
	if err != nil {
		return fmt.Errorf("UpdateTailscaleInfo: %w", err)
	}
	return nil
}

const selectDevice = `
	SELECT id, account_id, user_id, name, platform, app_version,
	       COALESCE(tailscale_node_id, ''), COALESCE(last_seen_at, ''), created_at
	FROM devices`

func scanDevice(row *sql.Row) (Device, error) {
	var d Device
	var lastSeen, createdAt string
	if err := row.Scan(&d.ID, &d.AccountID, &d.UserID, &d.Name, &d.Platform,
		&d.AppVersion, &d.TailscaleNodeID, &lastSeen, &createdAt); err != nil {
		return Device{}, err
	}
	if lastSeen != "" {
		if t, err := time.Parse(sqliteTimeFmt, lastSeen); err == nil {
			d.LastSeenAt = t
		}
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		d.CreatedAt = t
	}
	return d, nil
}

func scanDeviceRows(rows *sql.Rows) (Device, error) {
	var d Device
	var lastSeen, createdAt string
	if err := rows.Scan(&d.ID, &d.AccountID, &d.UserID, &d.Name, &d.Platform,
		&d.AppVersion, &d.TailscaleNodeID, &lastSeen, &createdAt); err != nil {
		return Device{}, err
	}
	if lastSeen != "" {
		if t, err := time.Parse(sqliteTimeFmt, lastSeen); err == nil {
			d.LastSeenAt = t
		}
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		d.CreatedAt = t
	}
	return d, nil
}
