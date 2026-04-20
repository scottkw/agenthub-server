# AgentHub Server — Plan 07: Realtime WebSocket Hub

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an in-memory WebSocket fan-out hub scoped by `account_id` and a `/ws` endpoint so device A's UI can see `device.created` events live when device B claims into the account. End state: `ws://host/ws?token=<jwt>` opens a session; the device-claim flow calls `hub.Publish(accountID, Event{Type:"device.created", ...})` post-commit; all connections for that account receive the event as a JSON text frame.

**Architecture:** One new package `internal/realtime` that owns a `*InMemoryHub` struct + `Publisher` interface. The hub keeps connections in a `map[accountID]map[*conn]struct{}`, runs one goroutine per connection for `Publish`→write fan-out with a bounded channel (drop on overflow, log), pings every 30s, and culls connections that haven't pong'd within 90s. A new `/ws` HTTP handler in `internal/api` authenticates the JWT (header OR `?token=` query param for browser clients), upgrades the connection via `github.com/coder/websocket`, and calls `hub.Register`. The claim handler in `api/devices.go` gains a `Publisher` dependency and publishes `device.created` after a successful claim.

**Tech Stack:** `github.com/coder/websocket` for the WS upgrade (modern context-aware API, single small dep). No other new deps.

**Spec reference:** `docs/superpowers/specs/2026-04-16-agenthub-server-design.md` §5 (`internal/realtime` — "in-memory WebSocket fan-out hub, scoped by account_id. Heartbeat pings every 30s, stale-connection cull at 90s. Interface is pluggable for a later Redis/NATS-backed implementation."), §7 Flow C (realtime event fan-out: post-commit publish, never cross account boundaries), §12 build-order item 7.

**What this plan does NOT do (deferred):**
- Interface-level pluggability (`Hub` interface with multiple implementations). We ship the concrete `*InMemoryHub`; the Redis/NATS backend in a later scale-out plan introduces an interface at that time. The `Publisher` interface used by publishers IS defined here — that's the only touch point domain code needs.
- Multi-instance coordination. The hub is per-process; events fan out to connections on THIS instance only. Hosted-mode horizontal scaling is Plan 11+ territory.
- Session-activity or subscription-ending events. Only `device.created` lands in this plan. Future events (`session.updated`, `device.deleted`, etc.) slot in by adding more `hub.Publish` call sites.
- Event replay / at-least-once delivery. Lost events are lost; DB is the source of truth and UIs fall back to polling `/api/devices` / `/api/sessions` if they miss an event.
- Backpressure beyond "bounded per-connection channel + drop on overflow". Slow consumers get their channel filled, then drop events until they catch up (logged).

---

## Pinned facts about `github.com/coder/websocket`

- Latest stable is `v1.8.x` (early 2026). Pin whatever `go get github.com/coder/websocket@latest` resolves to — it's a small, stable package.
- Server upgrade: `c, err := websocket.Accept(w, r, nil)` — returns `*websocket.Conn`.
- Write: `c.Write(ctx, websocket.MessageText, data)` — context-cancellable.
- Read: `typ, data, err := c.Read(ctx)` — blocks until a frame arrives or ctx/conn dies.
- Ping/pong: `c.Ping(ctx)` — round-trips a ping; returns error on timeout or close.
- Close: `c.Close(websocket.StatusNormalClosure, "reason")`.
- No gorilla-style read/write deadlines — everything is context-driven.

---

## File structure added / modified

```
go.mod                                  # +github.com/coder/websocket

internal/realtime/
├── types.go                            # Event + Publisher interface + HubConfig
├── inmem.go / inmem_test.go            # InMemoryHub — Register, Publish, heartbeat, cull, Close

internal/api/
├── ws.go / ws_test.go                  # /ws handler — auth, upgrade, hub.Register
├── devices.go / devices_test.go        # claimDeviceHandler accepts Publisher, fires device.created

cmd/agenthub-server/
└── main.go                             # Construct hub, mount /ws, pass Publisher to DeviceRoutes

test/integration/
└── realtime_test.go                    # Open /ws, claim a device, assert device.created received
```

---

## Task 1: Dep + types scaffolding (inline-friendly)

**Files:**
- Modify: `go.mod` / `go.sum` (via `go get`)
- Create: `internal/realtime/types.go`

- [ ] **Step 1: Add the dep**

```bash
go get github.com/coder/websocket@latest
grep 'coder/websocket' go.mod
```

Expected: one line like `github.com/coder/websocket v1.8.x`.

- [ ] **Step 2: Write `internal/realtime/types.go`**

```go
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
```

- [ ] **Step 3: Verify compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum internal/realtime/types.go
git commit -m "feat(realtime): types + Publisher interface + coder/websocket dep"
```

---

## Task 2: InMemoryHub (TDD)

**Files:**
- Create: `internal/realtime/inmem.go`
- Create: `internal/realtime/inmem_test.go`

- [ ] **Step 1: Write failing `internal/realtime/inmem_test.go`**

```go
package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// wsServer spins up an httptest.Server whose handler accepts a WS upgrade
// and registers the connection with the given hub under the given account.
func wsServer(t *testing.T, hub *InMemoryHub, accountID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		hub.Register(r.Context(), accountID, c)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialWS(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Scheme = "ws"
	c, _, err := websocket.Dial(context.Background(), u.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "test cleanup") })
	return c
}

func readEvent(t *testing.T, c *websocket.Conn, timeout time.Duration) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, typ)
	var e Event
	require.NoError(t, json.Unmarshal(data, &e))
	return e
}

func TestInMemoryHub_PublishFansOutToAllConnections(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour})
	t.Cleanup(func() { _ = hub.Close() })

	srv := wsServer(t, hub, "acct1")
	c1 := dialWS(t, srv.URL)
	c2 := dialWS(t, srv.URL)

	// Give Register a moment to run.
	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 2 }, time.Second, 10*time.Millisecond)

	hub.Publish("acct1", Event{Type: "device.created", Data: map[string]any{"id": "x"}})

	e1 := readEvent(t, c1, 2*time.Second)
	e2 := readEvent(t, c2, 2*time.Second)
	require.Equal(t, "device.created", e1.Type)
	require.Equal(t, "device.created", e2.Type)
	require.Equal(t, "x", e1.Data["id"])
	require.Equal(t, "x", e2.Data["id"])
}

func TestInMemoryHub_EventsDoNotCrossAccounts(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour})
	t.Cleanup(func() { _ = hub.Close() })

	srv1 := wsServer(t, hub, "acctA")
	srv2 := wsServer(t, hub, "acctB")
	cA := dialWS(t, srv1.URL)
	cB := dialWS(t, srv2.URL)
	_ = cA
	_ = cB

	require.Eventually(t, func() bool {
		return hub.accountConnCount("acctA") == 1 && hub.accountConnCount("acctB") == 1
	}, time.Second, 10*time.Millisecond)

	hub.Publish("acctA", Event{Type: "x"})

	// cA should receive; cB should NOT. Give it 150ms for the fan-out to happen,
	// then assert cB's read times out.
	_ = readEvent(t, cA, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err := cB.Read(ctx)
	require.Error(t, err, "cB should not receive account-A's event")
}

func TestInMemoryHub_ConnectionRemovedOnClose(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour})
	t.Cleanup(func() { _ = hub.Close() })

	srv := wsServer(t, hub, "acct1")
	c := dialWS(t, srv.URL)
	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 1 }, time.Second, 10*time.Millisecond)

	require.NoError(t, c.Close(websocket.StatusNormalClosure, "client done"))

	// The server-side Register loop should notice the close and deregister.
	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestInMemoryHub_OverflowDropsEvents(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour, SendBuffer: 2})
	t.Cleanup(func() { _ = hub.Close() })

	srv := wsServer(t, hub, "acct1")
	c := dialWS(t, srv.URL)
	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 1 }, time.Second, 10*time.Millisecond)

	// Don't read anything; push a bunch of events so the buffer overflows.
	for i := 0; i < 50; i++ {
		hub.Publish("acct1", Event{Type: "spam", Data: map[string]any{"i": i}})
	}

	// Connection should still be alive (overflow drops, doesn't kill). Read
	// what the buffer held plus what made it through in-flight.
	got := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		typ, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			break
		}
		require.Equal(t, websocket.MessageText, typ)
		require.True(t, strings.Contains(string(data), `"spam"`))
		got++
		if got > 10 {
			break // we've seen enough; don't count every one
		}
	}
	require.Greater(t, got, 0, "should have received at least one event before overflow")
}

// accountConnCount is a test-only helper implemented in inmem.go; it reads
// the hub's bookkeeping behind the mutex so tests can assert registration.
var _ = (*InMemoryHub).accountConnCount // (declared here to satisfy import ordering; see inmem.go)

// Silence unused imports if the compiler ever complains during TDD red phase.
var _ = sync.Mutex{}
```

- [ ] **Step 2: Run to confirm FAIL**

Run: `go test ./internal/realtime/ -run TestInMemoryHub -v`
Expected: FAIL — `InMemoryHub`, `NewInMemoryHub`, `Register`, `Publish`, `Close`, `accountConnCount` all undefined.

- [ ] **Step 3: Write `internal/realtime/inmem.go`**

```go
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
	conns map[string]map[*conn]struct{} // accountID → set of conns
}

// conn wraps a single registered websocket with its per-connection send
// channel. Each conn owns one writer goroutine (writes from ch to the socket)
// and one heartbeat goroutine (pings + culls stale).
type conn struct {
	ws        *websocket.Conn
	accountID string
	ch        chan []byte
	done      chan struct{}
}

// NewInMemoryHub returns a hub with sensible production defaults when
// HubConfig fields are zero. slog.Default is used for logs; swap via
// WithLogger if needed.
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

// WithLogger replaces the default logger. Chain with the constructor.
func (h *InMemoryHub) WithLogger(l *slog.Logger) *InMemoryHub { h.log = l; return h }

// Register adds ws to the fan-out set for accountID and blocks until the
// connection closes or the hub shuts down. It spawns:
//   - writer goroutine: drains the per-conn channel to the socket
//   - heartbeat goroutine: pings + culls stale
// and runs a read loop on the caller's goroutine (this one) that discards
// client→server frames but detects close.
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

	// Read loop (we don't care about client messages, but a Read is the only
	// way to detect a client-side close cleanly with coder/websocket).
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

// Publish fans an event out to every registered connection for accountID.
// Never blocks: each per-connection send is a non-blocking channel push that
// drops on overflow.
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

// Close stops heartbeat/writer goroutines for all connections and terminates
// all Register calls. Idempotent.
func (h *InMemoryHub) Close() error {
	h.once.Do(func() { close(h.done) })
	// Grab a snapshot of conns and close them all.
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

// accountConnCount is a test-only helper exposing the current registered
// connection count for an account. Unexported-except-for-package-tests by
// virtue of a lowercase name — callers in non-test files should not use it.
func (h *InMemoryHub) accountConnCount(accountID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns[accountID])
}
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/realtime/ -run TestInMemoryHub -v`
Expected: PASS for all four TestInMemoryHub_* tests.

Flaky-test guidance: all tests use `require.Eventually` with 1-2s timeouts and 10ms polling, so modest CI slowness won't break them. If one does flake, investigate the race — don't just bump timeouts.

- [ ] **Step 5: Verify `*InMemoryHub` satisfies `Publisher`**

Append to `internal/realtime/inmem.go` at the bottom of the file:

```go
var _ Publisher = (*InMemoryHub)(nil)
```

Re-run tests. If the assertion fails, the `Publish` signature has drifted from the interface — fix whichever.

- [ ] **Step 6: Commit**

```bash
git add internal/realtime/inmem.go internal/realtime/inmem_test.go
git commit -m "feat(realtime): InMemoryHub with per-account fan-out, heartbeat, cull"
```

---

## Task 3: `/ws` WebSocket handler (TDD)

Authenticate the incoming request (JWT via `Authorization: Bearer ...` header OR `?token=...` query param for browser clients), upgrade via `websocket.Accept`, then hand off to `hub.Register(ctx, accountID, c)`.

**Files:**
- Create: `internal/api/ws.go`
- Create: `internal/api/ws_test.go`

- [ ] **Step 1: Write failing `internal/api/ws_test.go`**

```go
package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/realtime"
)

func dialTestWS(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(serverURL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/ws"
	if token != "" {
		q := u.Query()
		q.Set("token", token)
		u.RawQuery = q.Encode()
	}
	c, _, err := websocket.Dial(context.Background(), u.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "cleanup") })
	return c
}

func TestWS_AuthedClientReceivesPublishedEvent(t *testing.T) {
	r, mailer, svc := newRouterWithAuthInternal(t)
	hub := realtime.NewInMemoryHub(realtime.HubConfig{
		HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour,
	})
	t.Cleanup(func() { _ = hub.Close() })

	r.Mount("/ws", WSRoutes(svc, hub))

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	jwt := signUpAndLogin(t, r, mailer, "ws@example.com", "password9", "WS")

	// Need accountID too — login response has it.
	// signUpAndLogin only returns the JWT; grab accountID from the JWT via an API call.
	// The /api/auth/me endpoint isn't implemented in this plan series; instead, the
	// hub knows the accountID because /ws handler extracts it from the JWT claims.
	// Test strategy: dial /ws with the JWT, then hub.Publish on the accountID we
	// know was created by signUpAndLogin. That requires the accountID.
	//
	// Quickest path: decode JWT payload to extract account_id. signUpAndLogin
	// used `sess@example.com` — the auth test fixture wires account_name=WS
	// which creates exactly one account; pull it from the DB.
	var accountID string
	require.NoError(t, svc.DB().QueryRow(`SELECT id FROM accounts WHERE name='WS'`).Scan(&accountID))

	c := dialTestWS(t, server.URL, jwt)

	// Give Register time.
	require.Eventually(t, func() bool { return hub.AccountConnCountForTest(accountID) == 1 }, 2*time.Second, 20*time.Millisecond)

	hub.Publish(accountID, realtime.Event{Type: "hello", Data: map[string]any{"x": 1}})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, typ)
	var e realtime.Event
	require.NoError(t, json.Unmarshal(data, &e))
	require.Equal(t, "hello", e.Type)
	require.Equal(t, float64(1), e.Data["x"]) // JSON numbers → float64 when decoded into any
}

func TestWS_MissingTokenIsRejected(t *testing.T) {
	r, _, svc := newRouterWithAuthInternal(t)
	hub := realtime.NewInMemoryHub(realtime.HubConfig{})
	t.Cleanup(func() { _ = hub.Close() })
	r.Mount("/ws", WSRoutes(svc, hub))

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/ws"

	_, resp, err := websocket.Dial(context.Background(), u.String(), nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 401, resp.StatusCode)
}
```

- [ ] **Step 2: Run to confirm FAIL**

Run: `go test ./internal/api/ -run TestWS -v`
Expected: FAIL — `WSRoutes`, `AccountConnCountForTest` not defined.

- [ ] **Step 3: Expose `AccountConnCountForTest` on `*InMemoryHub`**

The hub already has `accountConnCount` (unexported) for its own tests. Expose an exported wrapper so tests in OTHER packages (like `internal/api`) can check registration.

Append to `internal/realtime/inmem.go`:

```go
// AccountConnCountForTest returns the registered connection count for an
// account. Exposed for cross-package tests; production code has no
// legitimate use for it.
func (h *InMemoryHub) AccountConnCountForTest(accountID string) int {
	return h.accountConnCount(accountID)
}
```

- [ ] **Step 4: Write `internal/api/ws.go`**

```go
package api

import (
	"net/http"

	"github.com/coder/websocket"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/realtime"
)

// WSRoutes returns an http.Handler for the /ws endpoint. Authenticates the
// request via JWT (header `Authorization: Bearer <jwt>` OR `?token=<jwt>`
// query param for browser clients that can't set custom WS headers),
// upgrades, and registers with the hub under the caller's account_id.
//
// Returned as http.Handler (not a chi router) so it can be mounted at an
// exact path without trailing-slash redirects that would break the upgrade
// handshake.
func WSRoutes(svc *auth.Service, hub *realtime.InMemoryHub) http.Handler {
	return wsHandler(svc, hub)
}

func wsHandler(svc *auth.Service, hub *realtime.InMemoryHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := resolveWSAuth(svc, r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Verify the session is still active (matches the RequireAuth middleware path).
		if err := auth.CheckSessionActive(r.Context(), svc.DB(), claims.SessionID); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			// websocket.Accept already wrote a response on failure.
			return
		}

		// Register blocks until the connection closes or the request context
		// is cancelled.
		hub.Register(r.Context(), claims.AccountID, c)
	}
}

// resolveWSAuth parses a JWT from the request. It accepts:
//   1. `Authorization: Bearer <jwt>` — for non-browser clients (daemon, CLI)
//   2. `?token=<jwt>` query param — for browser clients (the WebSocket
//      handshake has no general way to set custom headers).
//
// API tokens (`Authorization: Token ahs_...`) are NOT accepted on /ws in
// this plan — device-scoped tokens have no bearer-use case for realtime UI
// updates today. Add that path here when a machine client needs it.
func resolveWSAuth(svc *auth.Service, r *http.Request) (*auth.Claims, error) {
	if raw := bearerFromHeader(r); raw != "" {
		return svc.Signer().Parse(raw)
	}
	if raw := r.URL.Query().Get("token"); raw != "" {
		return svc.Signer().Parse(raw)
	}
	return nil, errMissingToken
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const pfx = "Bearer "
	if len(h) > len(pfx) && h[:len(pfx)] == pfx {
		return h[len(pfx):]
	}
	return ""
}

var errMissingToken = &wsAuthError{"no token on Authorization header or ?token= query param"}

type wsAuthError struct{ msg string }

func (e *wsAuthError) Error() string { return e.msg }
```

- [ ] **Step 5: Expose `svc.Signer()` if not already**

`auth.Service` has a `DB()` accessor (added in Plan 02). Check whether it also exposes `Signer()`. If not, add a method:

```go
// Signer returns the JWT signer used by this service. Exposed so handlers
// that can't use the standard middleware (e.g. /ws) can parse tokens
// directly.
func (s *Service) Signer() *JWTSigner {
	return s.cfg.Signer
}
```

Place it in `internal/auth/service.go` next to `DB()`.

- [ ] **Step 6: Run to confirm PASS**

Run: `go test ./internal/api/ -run TestWS -v`
Expected: PASS for both tests.

If the full api suite regresses, check that `newRouterWithAuthInternal` still works and that `signUpAndLogin` is the same helper used by devices/sessions tests (lives in `internal/api/devices_test.go` or `internal/api/auth_test.go`).

- [ ] **Step 7: Commit**

```bash
git add internal/api/ws.go internal/api/ws_test.go internal/auth/service.go internal/realtime/inmem.go
git commit -m "feat(api): /ws WebSocket handler with JWT auth"
```

---

## Task 4: Fire `device.created` on claim (TDD)

**Files:**
- Modify: `internal/api/devices.go`
- Modify: `internal/api/devices_test.go` (existing signature changes — keep existing tests passing)

- [ ] **Step 1: Update `DeviceRoutes` signature to accept a Publisher**

Open `internal/api/devices.go`. Current signature:

```go
func DeviceRoutes(svc *auth.Service, hs devices.Headscaler) http.Handler {
```

Change to:

```go
func DeviceRoutes(svc *auth.Service, hs devices.Headscaler, pub realtime.Publisher) http.Handler {
```

Add the import:

```go
	"github.com/scottkw/agenthub-server/internal/realtime"
```

Thread `pub` into `claimDeviceHandler`:

```go
	r.Post("/claim", claimDeviceHandler(svc, hs, pub))
```

Update `claimDeviceHandler`'s signature:

```go
func claimDeviceHandler(svc *auth.Service, hs devices.Headscaler, pub realtime.Publisher) http.HandlerFunc {
```

At the end of the handler — AFTER the `WriteJSON(w, http.StatusOK, ...)` call — is the wrong place because the response is already sent. Move the publish to happen right before the WriteJSON, so it's best-effort-before-reply. Actually: fire-and-forget is fine either way, because `Publish` is non-blocking. Put it RIGHT AFTER the success-error-check, BEFORE the response write:

Locate this block inside the handler:

```go
		WriteJSON(w, http.StatusOK, map[string]any{
			"device_id": out.Device.ID,
			...
		})
```

Insert immediately before that `WriteJSON`:

```go
		if pub != nil {
			pub.Publish(out.Device.AccountID, realtime.Event{
				Type: "device.created",
				Data: map[string]any{
					"device_id":  out.Device.ID,
					"user_id":    out.Device.UserID,
					"name":       out.Device.Name,
					"platform":   out.Device.Platform,
					"app_version": out.Device.AppVersion,
				},
			})
		}
```

- [ ] **Step 2: Update `internal/api/devices_test.go` to pass a nil (or fake) Publisher**

Read the test file. The existing tests call `DeviceRoutes(svc, devices.StubHeadscaler{})`. Replace each with `DeviceRoutes(svc, devices.StubHeadscaler{}, nil)` — `nil` is acceptable because the handler guards with `if pub != nil`.

Likewise the `newRouterWithDevices` fixture.

- [ ] **Step 3: Add a new test that asserts `device.created` fires**

Append to `internal/api/devices_test.go`:

```go
// capturingPublisher records every event. Lives alongside the other test
// helpers in this file.
type capturingPublisher struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	AccountID string
	Event     realtime.Event
}

func (c *capturingPublisher) Publish(accountID string, event realtime.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, capturedEvent{AccountID: accountID, Event: event})
}

func TestDevices_ClaimFiresDeviceCreatedEvent(t *testing.T) {
	r, mailer, svc := newRouterWithAuthInternal(t)
	pub := &capturingPublisher{}
	r.Mount("/api/devices", DeviceRoutes(svc, devices.StubHeadscaler{}, pub))

	jwt := signUpAndLogin(t, r, mailer, "evt@example.com", "password9", "Evt")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))

	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "evt-laptop", "platform": "darwin",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.events, 1, "expected exactly one published event")
	ev := pub.events[0]
	require.Equal(t, "device.created", ev.Event.Type)
	require.Equal(t, "evt-laptop", ev.Event.Data["name"])
	require.NotEmpty(t, ev.AccountID)
}
```

Add missing import at the top of the test file if not already present:

```go
	"sync"
	"github.com/scottkw/agenthub-server/internal/realtime"
```

- [ ] **Step 4: Run to confirm PASS (all api tests)**

Run: `go test ./internal/api/ -v`
Expected: PASS for everything — devices (existing), sessions (existing), auth (existing), oauth (existing), api-tokens (existing), ws (new), and the new `TestDevices_ClaimFiresDeviceCreatedEvent`.

- [ ] **Step 5: Commit**

```bash
git add internal/api/devices.go internal/api/devices_test.go
git commit -m "feat(api): claim handler publishes device.created realtime event"
```

---

## Task 5: cmd wiring — construct hub, mount /ws, thread Publisher

**Files:**
- Modify: `cmd/agenthub-server/main.go`

- [ ] **Step 1: Add imports**

Read main.go. Add to the import block:

```go
	"github.com/scottkw/agenthub-server/internal/realtime"
```

- [ ] **Step 2: Construct the hub**

After `headscaler, hsSupervisor, hsClient, err := buildHeadscaler(...)` and its error check, insert:

```go
	hub := realtime.NewInMemoryHub(realtime.HubConfig{}).WithLogger(logger)
	defer hub.Close()
```

- [ ] **Step 3: Update the `/api/devices` mount to pass the hub**

Locate:

```go
	router.With(rl).Mount("/api/devices", api.DeviceRoutes(authSvc, headscaler))
```

Replace with:

```go
	router.With(rl).Mount("/api/devices", api.DeviceRoutes(authSvc, headscaler, hub))
```

- [ ] **Step 4: Mount `/ws`**

After the `/api/sessions` mount, add:

```go
	router.Handle("/ws", api.WSRoutes(authSvc, hub))
```

`Handle` (not `Mount`) gives an exact-path match — chi's `Mount` is designed for sub-trees and can add trailing-slash redirect semantics that break a WebSocket upgrade handshake.

- [ ] **Step 5: Build + test**

```bash
go build ./...
go test ./...
```

Expected: green.

- [ ] **Step 6: Commit**

```bash
git add cmd/agenthub-server/main.go
git commit -m "feat(cmd): construct realtime hub, mount /ws, wire Publisher into claim"
```

---

## Task 6: E2E integration test

**Files:**
- Create: `test/integration/realtime_test.go`

- [ ] **Step 1: Write the integration test**

```go
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestRealtime_DeviceCreatedEventE2E boots the real binary, opens a
// WebSocket as device A, claims a device B, and asserts the WS client
// receives the device.created event.
func TestRealtime_DeviceCreatedEventE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal shutdown differs on Windows")
	}

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18187",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18187/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18187/api/auth/reset",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	base := "http://127.0.0.1:18187"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "rt-e2e@example.com", "password": "topsecretpw", "account_name": "RT",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "rt-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	// Open /ws with the token in the query param.
	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/ws"
	q := u.Query()
	q.Set("token", login.Token)
	u.RawQuery = q.Encode()

	wsCtx, wsCancel := context.WithCancel(context.Background())
	t.Cleanup(wsCancel)
	wsc, _, err := websocket.Dial(wsCtx, u.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = wsc.Close(websocket.StatusNormalClosure, "test done") })

	// Small delay so the hub Register finishes before we publish.
	time.Sleep(200 * time.Millisecond)

	// Issue a pair code and claim a device — this triggers device.created.
	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))

	_ = postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "rt-laptop",
	}, 200)

	// Expect device.created on the WebSocket.
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	typ, data, err := wsc.Read(readCtx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, typ)

	var event struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(data, &event))
	require.Equal(t, "device.created", event.Type)
	require.Equal(t, "rt-laptop", event.Data["name"])
}
```

- [ ] **Step 2: Run**

```bash
go test -race -timeout 120s ./test/integration/ -run TestRealtime_DeviceCreatedEventE2E -v
```

Expected: PASS.

- [ ] **Step 3: Run the full integration suite**

```bash
go test -race -timeout 180s ./test/integration/...
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add test/integration/realtime_test.go
git commit -m "test: realtime device.created E2E over /ws"
```

---

## Task 7: Final smoke + tag v0.7.0-realtime

- [ ] **Step 1: Full verification**

```bash
make test
go test -race -timeout 180s ./test/integration/...
make lint
```

Expected: all PASS. Run `gofmt -w .` + `git commit -am "style: gofmt"` if lint flags anything.

- [ ] **Step 2: Manual smoke**

```bash
make build
DATADIR=$(mktemp -d)
AGENTHUB_MODE=solo AGENTHUB_TLS_MODE=off \
  AGENTHUB_HTTP_PORT=18080 AGENTHUB_DATA_DIR=$DATADIR \
  AGENTHUB_MAIL_PROVIDER=noop ./bin/agenthub-server &
PID=$!
sleep 1

# Signup + force-verify + login (noop mailer).
curl -sS -X POST http://127.0.0.1:18080/api/auth/signup -H 'Content-Type: application/json' \
  -d '{"email":"rt-smoke@example.com","password":"password9","account_name":"RT"}' >/dev/null
sqlite3 $DATADIR/agenthub.db "UPDATE users SET email_verified_at = datetime('now') WHERE email='rt-smoke@example.com';"
JWT=$(curl -sS -X POST http://127.0.0.1:18080/api/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"rt-smoke@example.com","password":"password9"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

# Open a WS in one terminal, claim in another — or do it as one Go command for verification:
# (The integration test covers this; manual smoke is optional.)

# Instead, just prove the endpoint is up:
curl -s -o /dev/null -w "http=%{http_code}\n" "http://127.0.0.1:18080/ws?token=$JWT" \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ=='
# Expected http=101 (Switching Protocols)

kill -INT $PID
wait $PID 2>/dev/null
```

Expected:
- WebSocket upgrade returns `101 Switching Protocols`.
- Integration test covers the full end-to-end flow.

- [ ] **Step 3: Tag**

```bash
git tag -a v0.7.0-realtime -m "Plan 07: Realtime WebSocket hub

- internal/realtime: InMemoryHub scoped by account_id, heartbeat/cull, per-conn bounded send buffers
- Publisher interface for domain code; *InMemoryHub satisfies it
- /ws endpoint with JWT auth (header OR ?token= query param)
- Device claim publishes device.created post-commit
- E2E test covers claim → /ws fan-out"
```

---

## Done state

- A signed-in client can open `ws://host/ws?token=<jwt>` and receive a text JSON frame `{"type":"device.created","data":{...},"at":"..."}` whenever any device claims into the same account.
- Events stay within their account (no cross-tenant leakage).
- Slow WS clients drop events (logged) without killing the connection.
- Dead WS clients are culled within `StaleCullTimeout` (default 90s).
- `v0.7.0-realtime` tagged. All unit + integration + race + smoke green.

## Exit to Plan 08

Plan 08 ("Blob storage") adds `internal/blob` backed by `gocloud.dev/blob` with a `file://` implementation (solo mode). `/api/blobs/presign` mints a signed URL (for FS, an in-process endpoint); `/api/blobs/commit` records the `blob_objects` row and fires `blob.created` via `hub.Publish` — realtime fan-out pattern is now established and reusable. S3/R2 backend and signed-URL hardening land in Plan 11.
