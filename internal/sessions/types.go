// Package sessions owns agent_sessions: metadata rows that describe an
// AgentHub terminal session. The actual I/O flows peer-to-peer over the
// tailnet; this package only tracks existence, label, status, and activity.
package sessions

import "time"

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
)

type AgentSession struct {
	ID             string
	AccountID      string
	DeviceID       string
	Label          string
	Status         Status
	CWD            string
	StartedAt      time.Time
	EndedAt        time.Time
	LastActivityAt time.Time
}
