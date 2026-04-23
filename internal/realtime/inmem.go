package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// InMemoryHub is the per-process WebSocket fan-out implementation. Connections
// are indexed by accountID; Publish drops events for a connection whose
// per-connection send buffer is full (log-and-move-on — DB is source of truth).
type InMemoryHub struct {
	cfg  HubConfig
	log  *slog.Logger
	done chan struct{}
	once sync.Once

	mu    sync.Mutex
	conns map[string]map[*conn]struct{}
}

type conn struct {
	ws        *websocket.Conn
	accountID string
	ch        chan []byte
	done      chan struct{}
}

func NewInMemoryHub(cfg HubConfig) *InMemoryHub {
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.StaleCullTimeout == 0 {
		cfg.StaleCullTimeout = 90 * time.Second
	}
	if cfg.SendBuffer == 0 {
		cfg.SendBuffer = 32
	}
	return &InMemoryHub{
		cfg:   cfg,
		log:   slog.Default(),
		done:  make(chan struct{}),
		conns: map[string]map[*conn]struct{}{},
	}
}

func (h *InMemoryHub) WithLogger(l *slog.Logger) *InMemoryHub { h.log = l; return h }

// Register adds ws to the fan-out set for accountID and blocks until the
// connection closes or the hub shuts down.
func (h *InMemoryHub) Register(ctx context.Context, accountID string, ws *websocket.Conn) {
	c := &conn{
		ws:        ws,
		accountID: accountID,
		ch:        make(chan []byte, h.cfg.SendBuffer),
		done:      make(chan struct{}),
	}

	h.mu.Lock()
	set, ok := h.conns[accountID]
	if !ok {
		set = map[*conn]struct{}{}
		h.conns[accountID] = set
	}
	set[c] = struct{}{}
	h.mu.Unlock()

	defer h.deregister(c)

	go h.writer(c)
	go h.heartbeat(ctx, c)

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.done:
			return
		default:
		}
		_, _, err := ws.Read(ctx)
		if err != nil {
			return
		}
	}
}

func (h *InMemoryHub) Publish(accountID string, event Event) {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		h.log.Warn("realtime.Publish marshal failed", "err", err, "type", event.Type)
		return
	}

	h.mu.Lock()
	set := h.conns[accountID]
	targets := make([]*conn, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	for _, c := range targets {
		select {
		case c.ch <- data:
		default:
			h.log.Warn("realtime: send buffer full, dropping event",
				"account_id", accountID, "type", event.Type)
		}
	}
}

func (h *InMemoryHub) Close() error {
	h.once.Do(func() { close(h.done) })
	h.mu.Lock()
	all := make([]*conn, 0)
	for _, set := range h.conns {
		for c := range set {
			all = append(all, c)
		}
	}
	h.mu.Unlock()

	for _, c := range all {
		_ = c.ws.Close(websocket.StatusGoingAway, "hub shutting down")
	}
	return nil
}

func (h *InMemoryHub) deregister(c *conn) {
	h.mu.Lock()
	if set, ok := h.conns[c.accountID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.conns, c.accountID)
		}
	}
	h.mu.Unlock()
	close(c.done)
}

func (h *InMemoryHub) writer(c *conn) {
	for {
		select {
		case <-c.done:
			return
		case <-h.done:
			return
		case data, ok := <-c.ch:
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.ws.Write(ctx, websocket.MessageText, data)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				h.log.Debug("realtime: write failed, connection will cull",
					"account_id", c.accountID, "err", err)
				_ = c.ws.Close(websocket.StatusInternalError, "write failed")
				return
			}
		}
	}
}

func (h *InMemoryHub) heartbeat(ctx context.Context, c *conn) {
	t := time.NewTicker(h.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-h.done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, cancel := context.WithTimeout(ctx, h.cfg.StaleCullTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				h.log.Debug("realtime: ping failed, culling",
					"account_id", c.accountID, "err", err)
				_ = c.ws.Close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
		}
	}
}

// accountConnCount is a test-only helper exposing current registered count.
func (h *InMemoryHub) accountConnCount(accountID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns[accountID])
}

// AccountConnCountForTest is the exported variant used by tests in other
// packages (e.g. internal/api).
func (h *InMemoryHub) AccountConnCountForTest(accountID string) int {
	return h.accountConnCount(accountID)
}

// BroadcastReconnect sends a "reconnect" event to every connection and
// then closes them gracefully. Called during shutdown so clients know to
// reconnect to a fresh instance.
func (h *InMemoryHub) BroadcastReconnect() {
	event := Event{
		Type: "reconnect",
		Data: map[string]any{"reason": "server_shutdown"},
		At:   time.Now().UTC(),
	}
	data, err := json.Marshal(event)
	if err != nil {
		h.log.Warn("realtime.BroadcastReconnect marshal failed", "err", err)
		return
	}

	h.mu.Lock()
	var all []*conn
	for _, set := range h.conns {
		for c := range set {
			all = append(all, c)
		}
	}
	h.mu.Unlock()

	for _, c := range all {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.ws.Write(ctx, websocket.MessageText, data)
		cancel()
		_ = c.ws.Close(websocket.StatusGoingAway, "server shutdown")
	}
}

// Compile-time assertion that *InMemoryHub satisfies Publisher.
var _ Publisher = (*InMemoryHub)(nil)
