package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

// Create inserts a new agent_sessions row. Status defaults to "running".
func Create(ctx context.Context, db *sql.DB, s AgentSession) error {
	if s.ID == "" || s.AccountID == "" || s.DeviceID == "" {
		return fmt.Errorf("sessions.Create: ID, AccountID, DeviceID required")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO agent_sessions (id, account_id, device_id, label, cwd)
		VALUES (?, ?, ?, ?, ?)`,
		s.ID, s.AccountID, s.DeviceID, s.Label, s.CWD,
	)
	if err != nil {
		return fmt.Errorf("sessions.Create: %w", err)
	}
	return nil
}

// GetByID fetches a session by primary key. Returns sql.ErrNoRows if absent.
func GetByID(ctx context.Context, db *sql.DB, id string) (AgentSession, error) {
	row := db.QueryRowContext(ctx, selectSession+` WHERE id = ?`, id)
	return scanSession(row)
}

// ListForAccount returns all sessions for the account, newest first.
func ListForAccount(ctx context.Context, db *sql.DB, accountID string) ([]AgentSession, error) {
	rows, err := db.QueryContext(ctx,
		selectSession+` WHERE account_id = ? ORDER BY started_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("sessions.ListForAccount: %w", err)
	}
	defer rows.Close()

	var out []AgentSession
	for rows.Next() {
		s, err := scanSessionRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TouchActivity stamps last_activity_at = now(). Called by the device as
// it sees activity on the underlying terminal session.
func TouchActivity(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE agent_sessions SET last_activity_at = datetime('now') WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sessions.TouchActivity: %w", err)
	}
	return nil
}

// End marks the session stopped and stamps ended_at.
func End(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE agent_sessions
		SET status = 'stopped', ended_at = datetime('now')
		WHERE id = ? AND status = 'running'`, id)
	if err != nil {
		return fmt.Errorf("sessions.End: %w", err)
	}
	return nil
}

const selectSession = `
	SELECT id, account_id, device_id, label, status, cwd,
	       started_at, COALESCE(ended_at, ''), last_activity_at
	FROM agent_sessions`

func scanSession(row *sql.Row) (AgentSession, error) {
	var s AgentSession
	var status, startedAt, endedAt, lastActivity string
	if err := row.Scan(&s.ID, &s.AccountID, &s.DeviceID, &s.Label, &status,
		&s.CWD, &startedAt, &endedAt, &lastActivity); err != nil {
		return AgentSession{}, err
	}
	s.Status = Status(status)
	s.StartedAt = mustParseTime(startedAt)
	s.LastActivityAt = mustParseTime(lastActivity)
	if endedAt != "" {
		s.EndedAt = mustParseTime(endedAt)
	}
	return s, nil
}

func scanSessionRows(rows *sql.Rows) (AgentSession, error) {
	var s AgentSession
	var status, startedAt, endedAt, lastActivity string
	if err := rows.Scan(&s.ID, &s.AccountID, &s.DeviceID, &s.Label, &status,
		&s.CWD, &startedAt, &endedAt, &lastActivity); err != nil {
		return AgentSession{}, err
	}
	s.Status = Status(status)
	s.StartedAt = mustParseTime(startedAt)
	s.LastActivityAt = mustParseTime(lastActivity)
	if endedAt != "" {
		s.EndedAt = mustParseTime(endedAt)
	}
	return s, nil
}

func mustParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(sqliteTimeFmt, s); err == nil {
		return t
	}
	return time.Time{}
}
