// Package realtime owns the in-memory WebSocket fan-out hub used to push
// domain events to connected clients. Every event is scoped to a single
// account_id; events never cross account boundaries.
//
// Publishers are domain handlers that fire `hub.Publish` AFTER a successful
// DB commit. Publish is best-effort — slow/dead connections drop events
// silently (with a log line); the DB is the source of truth and clients
// fall back to polling if they miss something.
package realtime

import "time"

// Event is one fan-out message. Marshaled to JSON for the wire.
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
	At   time.Time      `json:"at"`
}

// Publisher is the narrow interface domain code uses to fire events.
// *InMemoryHub implements it. A later Plan may introduce alternate
// backends (Redis, NATS) that also satisfy this interface.
type Publisher interface {
	Publish(accountID string, event Event)
}

// HubConfig tunes the InMemoryHub timings and buffer sizes. Production
// defaults come from NewInMemoryHub when fields are zero; tests pass short
// values.
type HubConfig struct {
	HeartbeatInterval time.Duration // ping every N; default 30s
	StaleCullTimeout  time.Duration // close if no pong received in N; default 90s
	SendBuffer        int           // per-connection send channel size; default 32
}
