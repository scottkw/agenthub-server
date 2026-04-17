# AgentHub Server — Plan 04: Devices & Sessions

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the AgentHub-specific domain layer: device registry with pair-code/claim onboarding, and agent-session metadata CRUD. End state: a signed-in user can issue a short-lived pair code from one device; a second device can claim it (no auth) to receive an `ahs_` API token, a device row, and a stubbed Headscale pre-auth-key payload; that device then creates/updates/ends `agent_sessions` using its API token.

**Architecture:** Two new domain packages behind the established patterns:
- `internal/devices/` — CRUD (`devices.go`), pair-code + claim flow (`pairing.go`), and a `Headscaler` interface (`headscale.go`) with a `StubHeadscaler` implementation. Real Headscale integration lands in Plan 05; this plan binds the interface but always uses the stub.
- `internal/sessions/` — `agent_sessions` CRUD (metadata only — no realtime fan-out; that lands in Plan 07).

Cross-cutting: extend `auth.RequireAuthOrToken` to inject `device_id` into the request context when the caller authed with a device-scoped `ahs_` token, and add `auth.DeviceID(ctx)` + `auth.RequireDeviceToken` so session handlers can gate on "caller is a device".

**Tech Stack:** existing deps only (`chi`, `database/sql`, `modernc.org/sqlite`, `golang.org/x/crypto/rand`). No new third-party packages.

**Spec reference:** `docs/superpowers/specs/2026-04-16-agenthub-server-design.md` §5 (devices, sessions), §6 (`devices`, `agent_sessions` tables), §7 Flow B (device registration critical path), §12 build-order item 4.

**What this plan does NOT do (deferred):**
- Real Headscale integration (pre-auth-key minting, bridge table, tsnet joining). Plan 05. The `Headscaler` interface returns a stub key.
- Realtime `device.created` / `session.updated` fan-out. Plan 07.
- DERP map content beyond an empty-JSON placeholder. Plan 06.
- Blob object storage (`blob_objects`). Plan 08.
- Admin SPA surfaces. Plan 09.

---

## File structure added by this plan

```
internal/db/migrations/sqlite/
└── 00004_devices_sessions.sql

internal/devices/
├── types.go                       # Device, PairCode, ClaimInput/Output
├── headscale.go / headscale_test.go       # Headscaler interface + StubHeadscaler
├── devices.go / devices_test.go           # device CRUD + UpdateTailscaleInfo
└── pairing.go / pairing_test.go           # IssuePairCode, ClaimDevice

internal/sessions/
├── types.go                       # AgentSession type + Status const
└── sessions.go / sessions_test.go         # Create, Get, List, UpdateActivity, End

internal/auth/
├── tokenauth_middleware.go        # extended: inject device_id into context
└── context.go                     # extended: DeviceID(ctx) helper + RequireDeviceToken

internal/api/
├── devices.go / devices_test.go           # /api/devices/* routes
└── sessions.go / sessions_test.go         # /api/sessions/* routes

cmd/agenthub-server/
└── main.go                        # mount /api/devices + /api/sessions, construct Headscaler

test/integration/
└── devices_sessions_test.go               # E2E: signup → login → pair → claim → session lifecycle
```

---

## Task 1: Migration 00004 — devices, device_pair_codes, agent_sessions

**Files:**
- Create: `internal/db/migrations/sqlite/00004_devices_sessions.sql`

- [ ] **Step 1: Write the migration**

Exact content of `internal/db/migrations/sqlite/00004_devices_sessions.sql`:

```sql
-- +goose Up

CREATE TABLE devices (
    id                 TEXT PRIMARY KEY,
    account_id         TEXT NOT NULL REFERENCES accounts(id),
    user_id            TEXT NOT NULL REFERENCES users(id),
    name               TEXT NOT NULL DEFAULT '',
    platform           TEXT NOT NULL DEFAULT '',
    app_version        TEXT NOT NULL DEFAULT '',
    tailscale_node_id  TEXT NULL,
    last_seen_at       TEXT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at         TEXT NULL
);

CREATE INDEX idx_devices_account_id ON devices(account_id);
CREATE INDEX idx_devices_user_id ON devices(user_id);

CREATE TABLE device_pair_codes (
    code                   TEXT PRIMARY KEY,
    account_id             TEXT NOT NULL REFERENCES accounts(id),
    user_id                TEXT NOT NULL REFERENCES users(id),
    created_at             TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at             TEXT NOT NULL,
    consumed_at            TEXT NULL,
    consumed_by_device_id  TEXT NULL
);

CREATE INDEX idx_device_pair_codes_account_id ON device_pair_codes(account_id);

CREATE TABLE agent_sessions (
    id               TEXT PRIMARY KEY,
    account_id       TEXT NOT NULL REFERENCES accounts(id),
    device_id        TEXT NOT NULL REFERENCES devices(id),
    label            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','stopped')),
    cwd              TEXT NOT NULL DEFAULT '',
    started_at       TEXT NOT NULL DEFAULT (datetime('now')),
    ended_at         TEXT NULL,
    last_activity_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_sessions_account_id ON agent_sessions(account_id);
CREATE INDEX idx_agent_sessions_device_id ON agent_sessions(device_id);

-- +goose Down
DROP TABLE agent_sessions;
DROP TABLE device_pair_codes;
DROP TABLE devices;
```

> Note: `api_tokens.device_id` exists but has no FK to `devices(id)` because the column predates this migration. Adding a FK to an existing SQLite column requires a table rebuild, which is out of scope for this plan. Referential integrity for `api_tokens.device_id → devices.id` is enforced in application code (`devices.ClaimDevice` inserts both rows in one tx).

- [ ] **Step 2: Verify migrations apply cleanly**

Run: `go test ./internal/db/...`

Expected: PASS. The existing migration tests apply all migrations under `internal/db/migrations/sqlite/` and verify the DB opens.

- [ ] **Step 3: Commit**

```bash
git add internal/db/migrations/sqlite/00004_devices_sessions.sql
git commit -m "feat(db/migrations): devices, device_pair_codes, agent_sessions"
```

---

## Task 2: devices package — types + Headscaler interface + stub (TDD)

**Files:**
- Create: `internal/devices/types.go`
- Create: `internal/devices/headscale.go`
- Create: `internal/devices/headscale_test.go`

- [ ] **Step 1: Write `internal/devices/types.go`**

```go
// Package devices owns the device registry and the pair-code / claim
// onboarding flow. Each device is bound to an account and a user, and
// (after Plan 05) to a Headscale node via tailscale_node_id.
package devices

import "time"

// Device is a single registered AgentHub client (desktop, CLI, etc).
type Device struct {
	ID              string
	AccountID       string
	UserID          string
	Name            string
	Platform        string
	AppVersion      string
	TailscaleNodeID string
	LastSeenAt      time.Time
	CreatedAt       time.Time
}

// PairCode is a short-lived, single-use code a signed-in device issues so
// another device can attach itself to the same account.
type PairCode struct {
	Code      string
	AccountID string
	UserID    string
	ExpiresAt time.Time
}

// ClaimInput is the payload a not-yet-authenticated device sends to redeem
// a pair code. The pair code itself authenticates the request.
type ClaimInput struct {
	Code       string
	Name       string
	Platform   string
	AppVersion string
}

// ClaimOutput is what the server returns after a successful claim: a fresh
// device row, the (one-shot-visible) API token raw value, and a Tailscale
// pre-auth-key payload for the client to join the tailnet.
type ClaimOutput struct {
	Device      Device
	APIToken    string // raw ahs_ token — caller's only view
	APITokenID  string
	PreAuthKey  PreAuthKey
}
```

- [ ] **Step 2: Write failing `internal/devices/headscale_test.go`**

```go
package devices

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStubHeadscaler_MintPreAuthKey(t *testing.T) {
	var hs Headscaler = StubHeadscaler{}

	out, err := hs.MintPreAuthKey(context.Background(), PreAuthKeyInput{
		AccountID: "acct1",
		UserID:    "u1",
		DeviceID:  "dev1",
		TTL:       5 * time.Minute,
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out.Key, "stub-"), "got %q", out.Key)
	require.NotEmpty(t, out.ControlURL)
	require.NotEmpty(t, out.DERPMapJSON)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), out.ExpiresAt, 5*time.Second)
}
```

- [ ] **Step 3: Run test to confirm it fails**

Run: `go test ./internal/devices/ -run TestStubHeadscaler -v`
Expected: FAIL — `Headscaler`, `StubHeadscaler`, `PreAuthKeyInput`, `PreAuthKey` not defined.

- [ ] **Step 4: Write `internal/devices/headscale.go`**

```go
package devices

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// Headscaler is the subset of Headscale functionality the claim flow needs.
// Plan 05 will provide a real implementation backed by the embedded
// Headscale library; Plan 04 uses StubHeadscaler which returns a fake key
// so the rest of the device-onboarding pipeline can be built and tested
// without the Headscale dependency.
type Headscaler interface {
	MintPreAuthKey(ctx context.Context, in PreAuthKeyInput) (PreAuthKey, error)
}

// PreAuthKeyInput describes the pre-auth key the device will use to join
// the tailnet.
type PreAuthKeyInput struct {
	AccountID string
	UserID    string
	DeviceID  string
	TTL       time.Duration
}

// PreAuthKey is what the server hands back to the claiming device so it
// can configure its embedded tsnet / Tailscale client.
type PreAuthKey struct {
	Key         string
	ControlURL  string
	DERPMapJSON string
	ExpiresAt   time.Time
}

// StubHeadscaler returns a fake pre-auth-key payload. It lets us wire and
// test the rest of the claim flow before Plan 05 lands the real Headscale
// integration. Never ship a solo/hosted binary with this in production.
type StubHeadscaler struct{}

func (StubHeadscaler) MintPreAuthKey(_ context.Context, in PreAuthKeyInput) (PreAuthKey, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return PreAuthKey{}, fmt.Errorf("rand: %w", err)
	}
	return PreAuthKey{
		Key:         "stub-" + base64.RawURLEncoding.EncodeToString(buf),
		ControlURL:  "https://stub.invalid/headscale",
		DERPMapJSON: `{"Regions":{}}`,
		ExpiresAt:   time.Now().Add(in.TTL),
	}, nil
}
```

- [ ] **Step 5: Run test to confirm it passes**

Run: `go test ./internal/devices/ -run TestStubHeadscaler -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/devices/types.go internal/devices/headscale.go internal/devices/headscale_test.go
git commit -m "feat(devices): Headscaler interface + StubHeadscaler"
```

---

## Task 3: devices package — CRUD (TDD)

**Files:**
- Create: `internal/devices/devices.go`
- Create: `internal/devices/devices_test.go`

- [ ] **Step 1: Write failing `internal/devices/devices_test.go`**

```go
package devices

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

func withDevicesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	db := d.SQL()
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO accounts (id, slug, name) VALUES ('acct1', 'a', 'A')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO users (id, email) VALUES ('u1', 'a@b.com')`)
	require.NoError(t, err)
	return db
}

func TestCreateDevice_AndGet(t *testing.T) {
	db := withDevicesTestDB(t)

	err := CreateDevice(context.Background(), db, Device{
		ID: "dev1", AccountID: "acct1", UserID: "u1",
		Name: "laptop", Platform: "darwin", AppVersion: "0.1.0",
	})
	require.NoError(t, err)

	got, err := GetDeviceByID(context.Background(), db, "dev1")
	require.NoError(t, err)
	require.Equal(t, "dev1", got.ID)
	require.Equal(t, "laptop", got.Name)
	require.Equal(t, "darwin", got.Platform)
	require.False(t, got.CreatedAt.IsZero())
}

func TestGetDevice_NotFound(t *testing.T) {
	db := withDevicesTestDB(t)
	_, err := GetDeviceByID(context.Background(), db, "nope")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestListDevicesForAccount(t *testing.T) {
	db := withDevicesTestDB(t)

	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "a", AccountID: "acct1", UserID: "u1", Name: "one"}))
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "b", AccountID: "acct1", UserID: "u1", Name: "two"}))

	list, err := ListDevicesForAccount(context.Background(), db, "acct1")
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestListDevices_ExcludesSoftDeleted(t *testing.T) {
	db := withDevicesTestDB(t)
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "a", AccountID: "acct1", UserID: "u1", Name: "keep"}))
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "b", AccountID: "acct1", UserID: "u1", Name: "trash"}))

	require.NoError(t, SoftDeleteDevice(context.Background(), db, "b"))

	list, err := ListDevicesForAccount(context.Background(), db, "acct1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "a", list[0].ID)
}

func TestUpdateTailscaleInfo(t *testing.T) {
	db := withDevicesTestDB(t)
	require.NoError(t, CreateDevice(context.Background(), db, Device{ID: "dev1", AccountID: "acct1", UserID: "u1"}))

	require.NoError(t, UpdateTailscaleInfo(context.Background(), db, "dev1", "ts-node-xyz"))

	got, err := GetDeviceByID(context.Background(), db, "dev1")
	require.NoError(t, err)
	require.Equal(t, "ts-node-xyz", got.TailscaleNodeID)
	require.False(t, got.LastSeenAt.IsZero())
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/devices/ -run 'TestCreateDevice|TestGetDevice|TestListDevices|TestUpdateTailscaleInfo' -v`
Expected: FAIL — `CreateDevice`, `GetDeviceByID`, `ListDevicesForAccount`, `SoftDeleteDevice`, `UpdateTailscaleInfo` not defined.

- [ ] **Step 3: Write `internal/devices/devices.go`**

```go
package devices

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

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
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/devices/ -run 'TestCreateDevice|TestGetDevice|TestListDevices|TestUpdateTailscaleInfo' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/devices/devices.go internal/devices/devices_test.go
git commit -m "feat(devices): CRUD + soft delete + UpdateTailscaleInfo"
```

---

## Task 4: devices package — pair-code + claim flow (TDD)

The claim flow has to be atomic: validate+consume the pair code, create the device row, create the API token, and mint the (stubbed) pre-auth key — all in one transaction so a crash between steps doesn't leak half-created state.

**Files:**
- Create: `internal/devices/pairing.go`
- Create: `internal/devices/pairing_test.go`

- [ ] **Step 1: Write failing `internal/devices/pairing_test.go`**

```go
package devices

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIssuePairCode_Roundtrip(t *testing.T) {
	db := withDevicesTestDB(t)

	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, pc.Code, pairCodeLen)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), pc.ExpiresAt, 5*time.Second)

	// Round-trip: the row exists and is not yet consumed.
	var consumed sql.NullString
	err = db.QueryRowContext(context.Background(),
		`SELECT consumed_at FROM device_pair_codes WHERE code = ?`, pc.Code).Scan(&consumed)
	require.NoError(t, err)
	require.False(t, consumed.Valid)
}

func TestClaimDevice_HappyPath(t *testing.T) {
	db := withDevicesTestDB(t)
	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)

	out, err := ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{
		Code: pc.Code, Name: "laptop", Platform: "darwin", AppVersion: "0.1.0",
	})
	require.NoError(t, err)

	require.NotEmpty(t, out.Device.ID)
	require.Equal(t, "laptop", out.Device.Name)
	require.True(t, strings.HasPrefix(out.APIToken, "ahs_"), "got %q", out.APIToken)
	require.NotEmpty(t, out.APITokenID)
	require.True(t, strings.HasPrefix(out.PreAuthKey.Key, "stub-"))

	// api_tokens row has device_id set.
	var devID string
	err = db.QueryRowContext(context.Background(),
		`SELECT COALESCE(device_id,'') FROM api_tokens WHERE id = ?`, out.APITokenID).Scan(&devID)
	require.NoError(t, err)
	require.Equal(t, out.Device.ID, devID)

	// pair code is marked consumed.
	var consumed sql.NullString
	err = db.QueryRowContext(context.Background(),
		`SELECT consumed_at FROM device_pair_codes WHERE code = ?`, pc.Code).Scan(&consumed)
	require.NoError(t, err)
	require.True(t, consumed.Valid)
}

func TestClaimDevice_UnknownCode(t *testing.T) {
	db := withDevicesTestDB(t)
	_, err := ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: "bogus", Name: "x"})
	require.ErrorIs(t, err, ErrPairCodeInvalid)
}

func TestClaimDevice_ExpiredCode(t *testing.T) {
	db := withDevicesTestDB(t)
	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: -time.Minute, // already expired
	})
	require.NoError(t, err)

	_, err = ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: pc.Code, Name: "x"})
	require.ErrorIs(t, err, ErrPairCodeInvalid)
}

func TestClaimDevice_ReusedCode(t *testing.T) {
	db := withDevicesTestDB(t)
	pc, err := IssuePairCode(context.Background(), db, PairCodeInput{
		AccountID: "acct1", UserID: "u1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)

	_, err = ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: pc.Code, Name: "first"})
	require.NoError(t, err)

	_, err = ClaimDevice(context.Background(), db, StubHeadscaler{}, ClaimInput{Code: pc.Code, Name: "second"})
	require.ErrorIs(t, err, ErrPairCodeInvalid)
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/devices/ -run TestIssuePairCode -v ; go test ./internal/devices/ -run TestClaimDevice -v`
Expected: FAIL — `IssuePairCode`, `PairCodeInput`, `ClaimDevice`, `ErrPairCodeInvalid`, `pairCodeLen` not defined.

- [ ] **Step 3: Write `internal/devices/pairing.go`**

```go
package devices

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/ids"
)

// ErrPairCodeInvalid is returned when a claim code is unknown, expired, or
// already consumed.
var ErrPairCodeInvalid = errors.New("pair code invalid or expired")

// pairCodeLen is the length of the human-shared code. 10 chars of base32
// (no padding) gives ~50 bits of entropy — well over what's needed for a
// 5-minute single-use code, and still typeable.
const pairCodeLen = 10

// base32 alphabet excluding I, O, 0, 1 to avoid visual ambiguity.
const pairCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// preAuthKeyTTL is how long the (stubbed) Headscale pre-auth key is valid.
// Spec §7 Flow B calls for 5 minutes.
const preAuthKeyTTL = 5 * time.Minute

// PairCodeInput is the caller-supplied input for IssuePairCode.
type PairCodeInput struct {
	AccountID string
	UserID    string
	TTL       time.Duration
}

// IssuePairCode creates a short-lived pair code row and returns it. The
// caller (authed user on device A) shares the code with device B out-of-band.
func IssuePairCode(ctx context.Context, db *sql.DB, in PairCodeInput) (PairCode, error) {
	if in.AccountID == "" || in.UserID == "" {
		return PairCode{}, fmt.Errorf("IssuePairCode: AccountID and UserID required")
	}
	if in.TTL == 0 {
		in.TTL = preAuthKeyTTL
	}

	code, err := randomPairCode()
	if err != nil {
		return PairCode{}, err
	}
	expires := time.Now().Add(in.TTL).UTC()

	_, err = db.ExecContext(ctx, `
		INSERT INTO device_pair_codes (code, account_id, user_id, expires_at)
		VALUES (?, ?, ?, ?)`,
		code, in.AccountID, in.UserID, expires.Format(sqliteTimeFmt),
	)
	if err != nil {
		return PairCode{}, fmt.Errorf("IssuePairCode: %w", err)
	}

	return PairCode{
		Code:      code,
		AccountID: in.AccountID,
		UserID:    in.UserID,
		ExpiresAt: expires,
	}, nil
}

// ClaimDevice consumes a pair code and creates (device + api_token) atomically,
// then mints a (stubbed) Headscale pre-auth key. Returns the full claim payload
// including the one-shot-visible raw API token.
func ClaimDevice(ctx context.Context, db *sql.DB, hs Headscaler, in ClaimInput) (ClaimOutput, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Validate + consume pair code (atomic via row lock implied by UPDATE
	//    ... WHERE consumed_at IS NULL).
	var accountID, userID, expiresStr string
	err = tx.QueryRowContext(ctx, `
		SELECT account_id, user_id, expires_at FROM device_pair_codes
		WHERE code = ? AND consumed_at IS NULL`, in.Code).Scan(&accountID, &userID, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimOutput{}, ErrPairCodeInvalid
	}
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: lookup code: %w", err)
	}
	expires, err := time.Parse(sqliteTimeFmt, expiresStr)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: parse expires_at: %w", err)
	}
	if time.Now().After(expires) {
		return ClaimOutput{}, ErrPairCodeInvalid
	}

	deviceID := ids.New()

	_, err = tx.ExecContext(ctx, `
		UPDATE device_pair_codes
		SET consumed_at = datetime('now'), consumed_by_device_id = ?
		WHERE code = ? AND consumed_at IS NULL`,
		deviceID, in.Code,
	)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: consume code: %w", err)
	}

	// 2. Insert device.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO devices (id, account_id, user_id, name, platform, app_version)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deviceID, accountID, userID, in.Name, in.Platform, in.AppVersion,
	)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: insert device: %w", err)
	}

	// 3. Mint API token bound to the device. We can't call auth.CreateAPIToken
	//    inside the tx because it takes *sql.DB; instead inline the same logic
	//    against the tx.
	tokenID := ids.New()
	raw, err := auth.GenerateAPITokenRaw()
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: gen token: %w", err)
	}
	hash := auth.HashAPIToken(raw)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_tokens (id, account_id, user_id, device_id, name, token_hash, scope)
		VALUES (?, ?, ?, ?, ?, ?, '[]')`,
		tokenID, accountID, userID, deviceID, "device:"+in.Name, hash,
	)
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: insert token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: commit: %w", err)
	}

	// 4. Mint pre-auth key. Outside the tx because the real Plan-05 impl
	//    will hit an external system; failures here leave a device row
	//    without a usable key, which is recoverable (the user can reclaim).
	preauth, err := hs.MintPreAuthKey(ctx, PreAuthKeyInput{
		AccountID: accountID, UserID: userID, DeviceID: deviceID, TTL: preAuthKeyTTL,
	})
	if err != nil {
		return ClaimOutput{}, fmt.Errorf("ClaimDevice: mint pre-auth key: %w", err)
	}

	return ClaimOutput{
		Device: Device{
			ID: deviceID, AccountID: accountID, UserID: userID,
			Name: in.Name, Platform: in.Platform, AppVersion: in.AppVersion,
		},
		APIToken:   raw,
		APITokenID: tokenID,
		PreAuthKey: preauth,
	}, nil
}

func randomPairCode() (string, error) {
	buf := make([]byte, pairCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	out := make([]byte, pairCodeLen)
	for i := range buf {
		out[i] = pairCodeAlphabet[int(buf[i])%len(pairCodeAlphabet)]
	}
	return string(out), nil
}
```

- [ ] **Step 4: Expose `GenerateAPITokenRaw` + `HashAPIToken` from `internal/auth/apitokens.go`**

The claim flow needs to insert into `api_tokens` inside a `*sql.Tx`, but the existing `auth.CreateAPIToken` takes `*sql.DB`. Instead of threading tx through that function (scope creep), expose two small helpers.

Modify `internal/auth/apitokens.go` to add these exports at the bottom of the file:

```go
// GenerateAPITokenRaw returns a fresh raw token with the ahs_ prefix.
// Exposed so callers that need to insert an api_tokens row inside a
// *sql.Tx (e.g. devices.ClaimDevice) can reuse our format.
func GenerateAPITokenRaw() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return apiTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashAPIToken returns the hex-encoded sha256 of raw, matching the
// storage format used by CreateAPIToken / LookupAPIToken.
func HashAPIToken(raw string) string {
	return sha256Hex(raw)
}
```

Also refactor `CreateAPIToken` to use the new helpers (so both code paths stay in sync). Replace the body section that generates + hashes:

Locate in `internal/auth/apitokens.go`:

```go
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", APIToken{}, fmt.Errorf("rand: %w", err)
	}
	raw := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	hash := sha256Hex(raw)
```

Replace with:

```go
	raw, err := GenerateAPITokenRaw()
	if err != nil {
		return "", APIToken{}, err
	}
	hash := HashAPIToken(raw)
```

- [ ] **Step 5: Run the devices tests to confirm PASS**

Run: `go test ./internal/devices/ -v`
Expected: PASS (all tests, including the roundtrip checks that query the `api_tokens` and `device_pair_codes` rows).

- [ ] **Step 6: Run existing auth tests to confirm no regression**

Run: `go test ./internal/auth/ -v`
Expected: PASS — the `CreateAPIToken` refactor is behavior-preserving.

- [ ] **Step 7: Commit**

```bash
git add internal/devices/pairing.go internal/devices/pairing_test.go internal/auth/apitokens.go
git commit -m "feat(devices): pair-code issuance + atomic claim flow"
```

---

## Task 5: sessions package — agent_sessions CRUD (TDD)

**Files:**
- Create: `internal/sessions/types.go`
- Create: `internal/sessions/sessions.go`
- Create: `internal/sessions/sessions_test.go`

- [ ] **Step 1: Write `internal/sessions/types.go`**

```go
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
```

- [ ] **Step 2: Write failing `internal/sessions/sessions_test.go`**

```go
package sessions

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
)

func withSessionsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	db := d.SQL()
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO accounts (id, slug, name) VALUES ('acct1', 'a', 'A')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO users (id, email) VALUES ('u1', 'a@b.com')`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO devices (id, account_id, user_id, name) VALUES ('dev1', 'acct1', 'u1', 'laptop')`)
	require.NoError(t, err)
	return db
}

func TestCreate_AndGet(t *testing.T) {
	db := withSessionsTestDB(t)

	err := Create(context.Background(), db, AgentSession{
		ID: "s1", AccountID: "acct1", DeviceID: "dev1",
		Label: "build", CWD: "/home/u/proj",
	})
	require.NoError(t, err)

	got, err := GetByID(context.Background(), db, "s1")
	require.NoError(t, err)
	require.Equal(t, "s1", got.ID)
	require.Equal(t, StatusRunning, got.Status)
	require.Equal(t, "build", got.Label)
}

func TestListForAccount(t *testing.T) {
	db := withSessionsTestDB(t)
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "a", AccountID: "acct1", DeviceID: "dev1", Label: "one"}))
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "b", AccountID: "acct1", DeviceID: "dev1", Label: "two"}))

	list, err := ListForAccount(context.Background(), db, "acct1")
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestTouchActivity(t *testing.T) {
	db := withSessionsTestDB(t)
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "s", AccountID: "acct1", DeviceID: "dev1"}))

	// Force last_activity_at back by a few seconds so we can see it move.
	_, err := db.ExecContext(context.Background(),
		`UPDATE agent_sessions SET last_activity_at = datetime('now','-1 hour') WHERE id = 's'`)
	require.NoError(t, err)

	before, err := GetByID(context.Background(), db, "s")
	require.NoError(t, err)

	require.NoError(t, TouchActivity(context.Background(), db, "s"))

	after, err := GetByID(context.Background(), db, "s")
	require.NoError(t, err)
	require.True(t, after.LastActivityAt.After(before.LastActivityAt),
		"before=%v after=%v", before.LastActivityAt, after.LastActivityAt)
}

func TestEnd(t *testing.T) {
	db := withSessionsTestDB(t)
	require.NoError(t, Create(context.Background(), db, AgentSession{ID: "s", AccountID: "acct1", DeviceID: "dev1"}))

	require.NoError(t, End(context.Background(), db, "s"))

	got, err := GetByID(context.Background(), db, "s")
	require.NoError(t, err)
	require.Equal(t, StatusStopped, got.Status)
	require.False(t, got.EndedAt.IsZero())
}
```

- [ ] **Step 3: Run to confirm failure**

Run: `go test ./internal/sessions/ -v`
Expected: FAIL — `Create`, `GetByID`, `ListForAccount`, `TouchActivity`, `End` not defined.

- [ ] **Step 4: Write `internal/sessions/sessions.go`**

```go
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
```

- [ ] **Step 5: Run to confirm PASS**

Run: `go test ./internal/sessions/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sessions/types.go internal/sessions/sessions.go internal/sessions/sessions_test.go
git commit -m "feat(sessions): agent_sessions CRUD"
```

---

## Task 6: auth middleware — inject device_id into context (TDD)

`RequireAuthOrToken` currently puts `user_id`, `account_id`, `session_id` into context. We need `device_id` too, so session handlers can tell "this request came from device X" without a second DB lookup.

**Files:**
- Modify: `internal/auth/middleware.go` (add `ctxDeviceID` + `DeviceID(ctx)` helper)
- Modify: `internal/auth/tokenauth_middleware.go` (inject device_id on Token auth path)
- Create: new test in `internal/auth/tokenauth_middleware_test.go` (additive)

- [ ] **Step 1: Find the current `ctxKey` constants**

Read `internal/auth/middleware.go` to locate the `ctxUserID`, `ctxAccountID`, `ctxSessionID` definitions and the corresponding `UserID(ctx)` / `AccountID(ctx)` / `SessionID(ctx)` helpers. These live alongside each other in one file.

- [ ] **Step 2: Add a failing test first**

Append to `internal/auth/tokenauth_middleware_test.go`:

```go
func TestRequireAuthOrToken_InjectsDeviceID(t *testing.T) {
	db := withTestDB(t)
	key, err := LoadOrCreateJWTKey(context.Background(), db)
	require.NoError(t, err)
	signer := NewJWTSigner(key, "test")

	raw, rec, err := CreateAPIToken(context.Background(), db, APITokenInput{
		ID: "tok1", AccountID: "acct1", UserID: "u1", DeviceID: "dev1", Name: "d",
	})
	require.NoError(t, err)
	require.Equal(t, "dev1", rec.DeviceID)

	var sawDeviceID string
	h := RequireAuthOrToken(signer, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawDeviceID = DeviceID(r.Context())
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Token "+raw)
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "dev1", sawDeviceID)
}
```

The imports needed (add at top of file if not already present): `"net/http"`, `"net/http/httptest"`, `"context"`, `"github.com/stretchr/testify/require"`, `"testing"`.

- [ ] **Step 3: Run to confirm failure**

Run: `go test ./internal/auth/ -run TestRequireAuthOrToken_InjectsDeviceID -v`
Expected: FAIL — `DeviceID` not defined (or returns empty string and the test asserts "dev1").

- [ ] **Step 4: Add `ctxDeviceID` + `DeviceID(ctx)` helper**

Open `internal/auth/middleware.go`. The context keys are declared as:

```go
type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxAccountID
	ctxSessionID
)
```

Add `ctxDeviceID` to that const block:

```go
const (
	ctxUserID ctxKey = iota
	ctxAccountID
	ctxSessionID
	ctxDeviceID
)
```

Then add a `DeviceID` helper alongside the existing `UserID` / `AccountID` / `SessionID` one-liners (same file):

```go
func DeviceID(ctx context.Context) string   { return ctxString(ctx, ctxDeviceID) }
```

- [ ] **Step 5: Inject device_id in the Token branch of `RequireAuthOrToken`**

In `internal/auth/tokenauth_middleware.go`, locate the `case strings.HasPrefix(h, "Token "):` branch. After the existing `context.WithValue(... ctxSessionID ...)` line and before `next.ServeHTTP(...)`, add:

```go
				if rec.DeviceID != "" {
					ctx = context.WithValue(ctx, ctxDeviceID, rec.DeviceID)
				}
```

- [ ] **Step 6: Run to confirm PASS**

Run: `go test ./internal/auth/ -run TestRequireAuthOrToken_InjectsDeviceID -v ; go test ./internal/auth/ -v`
Expected: PASS for the new test; no regressions in existing auth tests.

- [ ] **Step 7: Commit**

```bash
git add internal/auth/middleware.go internal/auth/tokenauth_middleware.go internal/auth/tokenauth_middleware_test.go
git commit -m "feat(auth): inject device_id into context for device-scoped tokens"
```

---

## Task 7: /api/devices HTTP routes (TDD)

Routes:
- `POST /api/devices/pair-code` — auth required — issue a pair code.
- `POST /api/devices/claim` — NOT auth'd — the pair code authenticates.
- `GET /api/devices` — auth required — list devices for caller's account.
- `GET /api/devices/{id}` — auth required — fetch one device.
- `POST /api/devices/{id}/tailscale-info` — device-token required — device reports its tailscale_node_id.
- `DELETE /api/devices/{id}` — auth required — soft delete.

Claim is unauthenticated but must be rate-limited; that wiring lives in Task 9 (main.go). This task only cares about handler logic.

**Files:**
- Create: `internal/api/devices.go`
- Create: `internal/api/devices_test.go`

- [ ] **Step 1: Write failing `internal/api/devices_test.go`**

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/devices"
	"github.com/scottkw/agenthub-server/internal/mail"
)

func newRouterWithDevices(t *testing.T) (*chi.Mux, *stubMailer) {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	key, err := auth.LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)

	mailer := &stubMailer{}
	svc := auth.NewService(auth.Config{
		DB:              d.SQL(),
		Signer:          auth.NewJWTSigner(key, "agenthub-server"),
		Mailer:          mailer,
		TTL:             auth.Lifetimes{Session: time.Hour, EmailVerify: time.Hour, PasswordReset: time.Hour},
		From:            "x@test", VerifyURLPrefix: "http://t/verify", ResetURLPrefix: "http://t/reset",
	})

	r := chi.NewRouter()
	r.Mount("/api/auth", AuthRoutes(svc))
	r.Mount("/api/tokens", APITokenRoutes(svc))
	r.Mount("/api/devices", DeviceRoutes(svc, devices.StubHeadscaler{}))
	return r, mailer
}

// signUpAndLogin returns a Bearer JWT suitable for Authorization headers.
func signUpAndLogin(t *testing.T, r *chi.Mux, mailer *stubMailer, email, pw, acct string) string {
	t.Helper()
	rr := doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email": email, "password": pw, "account_name": acct,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	// pluck verify token from mail body
	require.NotEmpty(t, mailer.msgs)
	body := mailer.msgs[len(mailer.msgs)-1].Text
	vTok := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	_ = doJSON(t, r, "POST", "/api/auth/verify", map[string]string{"token": vTok})

	lr := doJSON(t, r, "POST", "/api/auth/login", map[string]string{"email": email, "password": pw})
	require.Equal(t, http.StatusOK, lr.Code, lr.Body.String())
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(lr.Body.Bytes(), &login))
	return login.Token
}

func TestDevices_PairAndClaim(t *testing.T) {
	r, mailer := newRouterWithDevices(t)
	jwt := signUpAndLogin(t, r, mailer, "dev@example.com", "password9", "Dev")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Issue pair code.
	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var pair struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))
	require.NotEmpty(t, pair.Code)

	// Claim (no auth header).
	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "laptop", "platform": "darwin", "app_version": "0.1.0",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var claim struct {
		DeviceID  string `json:"device_id"`
		APIToken  string `json:"api_token"`
		Tailscale struct {
			ControlURL  string `json:"control_url"`
			PreAuthKey  string `json:"pre_auth_key"`
			DERPMapJSON string `json:"derp_map_json"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &claim))
	require.True(t, strings.HasPrefix(claim.APIToken, "ahs_"), "got %q", claim.APIToken)
	require.True(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "stub-"))

	// List devices (bearer auth).
	rr = doJSON(t, r, "GET", "/api/devices", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Devices []map[string]any `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Devices, 1)

	// Report tailscale-info (device-token auth).
	tokH := [2]string{"Authorization", "Token " + claim.APIToken}
	rr = doJSON(t, r, "POST", "/api/devices/"+claim.DeviceID+"/tailscale-info",
		map[string]string{"tailscale_node_id": "ts-node-xyz"}, tokH)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// Fetch device, confirm it took.
	rr = doJSON(t, r, "GET", "/api/devices/"+claim.DeviceID, nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, "ts-node-xyz", got["tailscale_node_id"])
}

func TestDevices_ClaimRejectsUnknownCode(t *testing.T) {
	r, _ := newRouterWithDevices(t)
	rr := doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": "NEVER", "name": "x",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestDevices_TailscaleInfoRequiresDeviceToken(t *testing.T) {
	r, mailer := newRouterWithDevices(t)
	jwt := signUpAndLogin(t, r, mailer, "ts@example.com", "password9", "TS")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Bearer JWT (no device) is rejected.
	rr := doJSON(t, r, "POST", "/api/devices/any-id/tailscale-info",
		map[string]string{"tailscale_node_id": "x"}, authH)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}

func TestDevices_SoftDelete(t *testing.T) {
	r, mailer := newRouterWithDevices(t)
	jwt := signUpAndLogin(t, r, mailer, "del@example.com", "password9", "Del")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Pair + claim so there's a device to delete.
	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))
	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "gone",
	})
	var claim struct {
		DeviceID string `json:"device_id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &claim))

	rr = doJSON(t, r, "DELETE", "/api/devices/"+claim.DeviceID, nil, authH)
	require.Equal(t, http.StatusNoContent, rr.Code)

	// List should now be empty.
	rr = doJSON(t, r, "GET", "/api/devices", nil, authH)
	var list struct {
		Devices []map[string]any `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Devices, 0)
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/api/ -run TestDevices -v`
Expected: FAIL — `DeviceRoutes`, `devices.StubHeadscaler` accessors in api package not wired.

- [ ] **Step 3: Write `internal/api/devices.go`**

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/devices"
)

// DeviceRoutes mounts the /api/devices/* endpoints. The claim endpoint is
// NOT behind auth (the pair code authenticates); mount-time caller is
// expected to apply rate-limit middleware around the whole router.
func DeviceRoutes(svc *auth.Service, hs devices.Headscaler) http.Handler {
	r := chi.NewRouter()

	// Public (code-authenticated): claim.
	r.Post("/claim", claimDeviceHandler(svc, hs))

	// Bearer/Token authed: everything else.
	r.Group(func(sub chi.Router) {
		sub.Use(auth.RequireAuthOrTokenFromService(svc))
		sub.Post("/pair-code", issuePairCodeHandler(svc))
		sub.Get("/", listDevicesHandler(svc))
		sub.Get("/{id}", getDeviceHandler(svc))
		sub.Delete("/{id}", deleteDeviceHandler(svc))
		sub.Post("/{id}/tailscale-info", tailscaleInfoHandler(svc))
	})
	return r
}

func issuePairCodeHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, err := devices.IssuePairCode(r.Context(), svc.DB(), devices.PairCodeInput{
			AccountID: auth.AccountID(r.Context()),
			UserID:    auth.UserID(r.Context()),
			TTL:       5 * time.Minute,
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "pair_code_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"code":       pc.Code,
			"expires_at": pc.ExpiresAt,
		})
	}
}

type claimDeviceReq struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

func claimDeviceHandler(svc *auth.Service, hs devices.Headscaler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in claimDeviceReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		out, err := devices.ClaimDevice(r.Context(), svc.DB(), hs, devices.ClaimInput{
			Code: in.Code, Name: in.Name, Platform: in.Platform, AppVersion: in.AppVersion,
		})
		if err != nil {
			if errors.Is(err, devices.ErrPairCodeInvalid) {
				WriteError(w, http.StatusBadRequest, "invalid_code", "pair code invalid or expired")
				return
			}
			WriteError(w, http.StatusInternalServerError, "claim_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"device_id": out.Device.ID,
			"api_token": out.APIToken,
			"tailscale": map[string]any{
				"control_url":   out.PreAuthKey.ControlURL,
				"pre_auth_key":  out.PreAuthKey.Key,
				"derp_map_json": out.PreAuthKey.DERPMapJSON,
				"expires_at":    out.PreAuthKey.ExpiresAt,
			},
		})
	}
}

func listDevicesHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := devices.ListDevicesForAccount(r.Context(), svc.DB(), auth.AccountID(r.Context()))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, d := range list {
			out = append(out, deviceJSON(d))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"devices": out})
	}
}

func getDeviceHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		d, err := devices.GetDeviceByID(r.Context(), svc.DB(), id)
		if err != nil {
			WriteError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if d.AccountID != auth.AccountID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		WriteJSON(w, http.StatusOK, deviceJSON(d))
	}
}

func deleteDeviceHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		d, err := devices.GetDeviceByID(r.Context(), svc.DB(), id)
		if err != nil || d.AccountID != auth.AccountID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if err := devices.SoftDeleteDevice(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type tailscaleInfoReq struct {
	TailscaleNodeID string `json:"tailscale_node_id"`
}

func tailscaleInfoHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only a device-scoped token may report its own tailscale info.
		ctxDev := auth.DeviceID(r.Context())
		if ctxDev == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"this endpoint requires a device-scoped ahs_ token")
			return
		}
		id := chi.URLParam(r, "id")
		if id != ctxDev {
			WriteError(w, http.StatusForbidden, "wrong_device",
				"token is bound to a different device")
			return
		}
		var in tailscaleInfoReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := devices.UpdateTailscaleInfo(r.Context(), svc.DB(), id, in.TailscaleNodeID); err != nil {
			WriteError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deviceJSON(d devices.Device) map[string]any {
	return map[string]any{
		"id":                d.ID,
		"account_id":        d.AccountID,
		"user_id":           d.UserID,
		"name":              d.Name,
		"platform":          d.Platform,
		"app_version":       d.AppVersion,
		"tailscale_node_id": d.TailscaleNodeID,
		"last_seen_at":      d.LastSeenAt,
		"created_at":        d.CreatedAt,
	}
}
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/api/ -run TestDevices -v`
Expected: PASS for all four `TestDevices_*` tests.

- [ ] **Step 5: Commit**

```bash
git add internal/api/devices.go internal/api/devices_test.go
git commit -m "feat(api): /api/devices pair-code, claim, CRUD, tailscale-info"
```

---

## Task 8: /api/sessions HTTP routes (TDD)

Routes:
- `POST /api/sessions` — device-token required — create an `agent_sessions` row for the caller's device.
- `GET /api/sessions` — auth (bearer or token) — list sessions for account.
- `POST /api/sessions/{id}/activity` — device-token required — touch last_activity_at.
- `POST /api/sessions/{id}/end` — device-token required — mark stopped.

**Files:**
- Create: `internal/api/sessions.go`
- Create: `internal/api/sessions_test.go`

- [ ] **Step 1: Write failing `internal/api/sessions_test.go`**

```go
package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/devices"
)

// routerWithSvc mounts /api/auth, /api/tokens, /api/devices, /api/sessions
// behind a single auth.Service (returned for the rare test that needs it).
// Reuses newRouterWithAuthInternal from auth_test.go.
func routerWithSvc(t *testing.T) (*chi.Mux, *stubMailer, *auth.Service) {
	t.Helper()
	r, mailer, svc := newRouterWithAuthInternal(t)
	r.Mount("/api/devices", DeviceRoutes(svc, devices.StubHeadscaler{}))
	r.Mount("/api/sessions", SessionRoutes(svc))
	return r, mailer, svc
}

func claimTestDevice(t *testing.T, r *chi.Mux, jwtBearer string) (deviceID, apiToken string) {
	t.Helper()
	authH := [2]string{"Authorization", "Bearer " + jwtBearer}

	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))

	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "laptop",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var claim struct {
		DeviceID string `json:"device_id"`
		APIToken string `json:"api_token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &claim))
	return claim.DeviceID, claim.APIToken
}

func TestSessions_CreateListActivityEnd(t *testing.T) {
	r, mailer, _ := routerWithSvc(t)
	jwt := signUpAndLogin(t, r, mailer, "sess@example.com", "password9", "Sess")
	_, apiTok := claimTestDevice(t, r, jwt)

	tokH := [2]string{"Authorization", "Token " + apiTok}
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Create via device token.
	rr := doJSON(t, r, "POST", "/api/sessions", map[string]string{
		"label": "build", "cwd": "/home/u/proj",
	}, tokH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, "running", created.Status)

	// List via bearer JWT.
	rr = doJSON(t, r, "GET", "/api/sessions", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Sessions, 1)

	// Touch activity.
	rr = doJSON(t, r, "POST", "/api/sessions/"+created.ID+"/activity", nil, tokH)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// End.
	rr = doJSON(t, r, "POST", "/api/sessions/"+created.ID+"/end", nil, tokH)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// List — should now show stopped.
	rr = doJSON(t, r, "GET", "/api/sessions", nil, authH)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Equal(t, "stopped", list.Sessions[0]["status"])
}

func TestSessions_CreateRequiresDeviceToken(t *testing.T) {
	r, mailer, _ := routerWithSvc(t)
	jwt := signUpAndLogin(t, r, mailer, "nodev@example.com", "password9", "NoDev")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "POST", "/api/sessions", map[string]string{"label": "x"}, authH)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/api/ -run TestSessions -v`
Expected: FAIL — `SessionRoutes` not defined.

- [ ] **Step 3: Write `internal/api/sessions.go`**

```go
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/sessions"
)

// SessionRoutes mounts /api/sessions/*. Create/activity/end require a
// device-scoped ahs_ token (caller IS the device); list accepts any auth.
func SessionRoutes(svc *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuthOrTokenFromService(svc))
	r.Get("/", listSessionsHandler(svc))
	r.Post("/", createSessionHandler(svc))
	r.Post("/{id}/activity", touchSessionHandler(svc))
	r.Post("/{id}/end", endSessionHandler(svc))
	return r
}

type createSessionReq struct {
	Label string `json:"label"`
	CWD   string `json:"cwd"`
}

func createSessionHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		devID := auth.DeviceID(r.Context())
		if devID == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"creating a session requires a device-scoped ahs_ token")
			return
		}
		var in createSessionReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		id := ids.New()
		err := sessions.Create(r.Context(), svc.DB(), sessions.AgentSession{
			ID: id, AccountID: auth.AccountID(r.Context()),
			DeviceID: devID, Label: in.Label, CWD: in.CWD,
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "create_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"id":     id,
			"status": string(sessions.StatusRunning),
		})
	}
}

func listSessionsHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := sessions.ListForAccount(r.Context(), svc.DB(), auth.AccountID(r.Context()))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, s := range list {
			out = append(out, map[string]any{
				"id":               s.ID,
				"account_id":       s.AccountID,
				"device_id":        s.DeviceID,
				"label":            s.Label,
				"status":           string(s.Status),
				"cwd":              s.CWD,
				"started_at":       s.StartedAt,
				"ended_at":         s.EndedAt,
				"last_activity_at": s.LastActivityAt,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
	}
}

func touchSessionHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.DeviceID(r.Context()) == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"activity reports require a device-scoped ahs_ token")
			return
		}
		id := chi.URLParam(r, "id")
		s, err := sessions.GetByID(r.Context(), svc.DB(), id)
		if err != nil || s.AccountID != auth.AccountID(r.Context()) || s.DeviceID != auth.DeviceID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err := sessions.TouchActivity(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "activity_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func endSessionHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.DeviceID(r.Context()) == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"ending a session requires a device-scoped ahs_ token")
			return
		}
		id := chi.URLParam(r, "id")
		s, err := sessions.GetByID(r.Context(), svc.DB(), id)
		if err != nil || s.AccountID != auth.AccountID(r.Context()) || s.DeviceID != auth.DeviceID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err := sessions.End(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "end_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/api/ -run TestSessions -v`
Expected: PASS for both `TestSessions_*` tests.

- [ ] **Step 5: Run the full api test suite**

Run: `go test ./internal/api/ -v`
Expected: PASS for everything — no regression in `TestDevices_*`, `TestAuth*`, `TestOAuth*`, `TestAPITokens*`, `TestHealth*`.

- [ ] **Step 6: Commit**

```bash
git add internal/api/sessions.go internal/api/sessions_test.go
git commit -m "feat(api): /api/sessions create/list/activity/end"
```

---

## Task 9: cmd wiring — mount /api/devices and /api/sessions

**Files:**
- Modify: `cmd/agenthub-server/main.go`

- [ ] **Step 1: Locate the existing router-wiring block**

In `cmd/agenthub-server/main.go`, find the section right after `router.Mount("/api/tokens", api.APITokenRoutes(authSvc))`. That's where the new mounts go. The `rl` (rate-limit) and `idem` middleware variables are already in scope from the `/api/auth` wiring a few lines up.

- [ ] **Step 2: Add Headscaler construction**

Above the router-mount block (right after `authSvc` is built), add:

```go
	// Plan 04 uses StubHeadscaler; Plan 05 will swap in the real embedded
	// Headscale integration.
	headscaler := devices.StubHeadscaler{}
```

Add the import: `"github.com/scottkw/agenthub-server/internal/devices"`.

- [ ] **Step 3: Mount the device routes**

Directly after `router.Mount("/api/tokens", api.APITokenRoutes(authSvc))`, add:

```go
	// /api/devices: claim is unauthenticated (pair code authenticates) but
	// is rate-limited to slow brute-force of the code. Other endpoints use
	// the router's own auth middleware.
	router.With(rl).Mount("/api/devices", api.DeviceRoutes(authSvc, headscaler))

	// /api/sessions: all endpoints are authed; no rate-limit (machine traffic).
	router.Mount("/api/sessions", api.SessionRoutes(authSvc))
```

- [ ] **Step 4: Verify the build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: PASS across every package.

- [ ] **Step 6: Commit**

```bash
git add cmd/agenthub-server/main.go
git commit -m "feat(cmd): mount /api/devices + /api/sessions with StubHeadscaler"
```

---

## Task 10: End-to-end integration test — pair → claim → session lifecycle

**Files:**
- Create: `test/integration/devices_sessions_test.go`

- [ ] **Step 1: Write the integration test**

```go
package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDevicesAndSessions_EndToEnd(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18184",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18184/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18184/api/auth/reset",
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

	base := "http://127.0.0.1:18184"
	waitReady(t, base+"/healthz")

	// 1. Signup + verify + login (device A).
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "e2e-dev@example.com", "password": "topsecretpw", "account_name": "E2ED",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "e2e-dev@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	bearer := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+login.Token) }

	// 2. Issue pair code from device A.
	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	bearer(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))
	require.NotEmpty(t, pair.Code)

	// 3. Claim from device B (no auth).
	claimBody := postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "laptop-B", "platform": "darwin", "app_version": "0.1.0",
	}, 200)
	var claim struct {
		DeviceID  string `json:"device_id"`
		APIToken  string `json:"api_token"`
		Tailscale struct {
			ControlURL string `json:"control_url"`
			PreAuthKey string `json:"pre_auth_key"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(claimBody, &claim))
	require.True(t, strings.HasPrefix(claim.APIToken, "ahs_"))
	require.True(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "stub-"))

	deviceTok := func(req *http.Request) { req.Header.Set("Authorization", "Token "+claim.APIToken) }

	// 4. Report tailscale-info as device B.
	tsReq, _ := http.NewRequest("POST",
		base+"/api/devices/"+claim.DeviceID+"/tailscale-info",
		strings.NewReader(`{"tailscale_node_id":"ts-node-e2e"}`))
	deviceTok(tsReq)
	tsReq.Header.Set("Content-Type", "application/json")
	tsResp, err := http.DefaultClient.Do(tsReq)
	require.NoError(t, err)
	defer tsResp.Body.Close()
	require.Equal(t, 204, tsResp.StatusCode)

	// 5. Create a session via device B.
	sReq, _ := http.NewRequest("POST", base+"/api/sessions",
		strings.NewReader(`{"label":"e2e","cwd":"/tmp"}`))
	deviceTok(sReq)
	sReq.Header.Set("Content-Type", "application/json")
	sResp, err := http.DefaultClient.Do(sReq)
	require.NoError(t, err)
	defer sResp.Body.Close()
	require.Equal(t, 200, sResp.StatusCode)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(sResp.Body).Decode(&created))

	// 6. List sessions via bearer JWT (device A's UI).
	listReq, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	bearer(listReq)
	listResp, err := http.DefaultClient.Do(listReq)
	require.NoError(t, err)
	defer listResp.Body.Close()
	require.Equal(t, 200, listResp.StatusCode)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&list))
	require.Len(t, list.Sessions, 1)
	require.Equal(t, "running", list.Sessions[0]["status"])

	// 7. End the session via device B.
	endReq, _ := http.NewRequest("POST", base+"/api/sessions/"+created.ID+"/end", nil)
	deviceTok(endReq)
	endResp, err := http.DefaultClient.Do(endReq)
	require.NoError(t, err)
	defer endResp.Body.Close()
	require.Equal(t, 204, endResp.StatusCode)
}
```

- [ ] **Step 2: Run the integration test**

Run: `go test -race -timeout 120s ./test/integration/ -run TestDevicesAndSessions_EndToEnd -v`
Expected: PASS.

- [ ] **Step 3: Run the whole integration suite, no regressions**

Run: `go test -race -timeout 120s ./test/integration/...`
Expected: PASS (with existing `TestOAuth_EndToEnd` skipped, `TestAPIToken_EndToEnd` passing).

- [ ] **Step 4: Commit**

```bash
git add test/integration/devices_sessions_test.go
git commit -m "test: pair → claim → session lifecycle E2E"
```

---

## Task 11: Final smoke + tag v0.4.0-devices-sessions

- [ ] **Step 1: Full local verification**

```bash
make test
go test -race -timeout 120s ./test/integration/...
make lint
```

Expected: all PASS. If `gofmt -l .` flags anything, run `gofmt -w .` and commit as `style: gofmt`.

- [ ] **Step 2: Manual smoke — pair and claim against the real binary**

```bash
DATADIR=$(mktemp -d)
AGENTHUB_MODE=solo AGENTHUB_TLS_MODE=off \
AGENTHUB_HTTP_PORT=18080 AGENTHUB_DATA_DIR=$DATADIR \
AGENTHUB_MAIL_PROVIDER=noop ./bin/agenthub-server &
PID=$!
sleep 1

# Signup (flow A).
curl -s -X POST http://127.0.0.1:18080/api/auth/signup \
  -H 'Content-Type: application/json' \
  -d '{"email":"smoke@example.com","password":"password9","account_name":"Smoke"}'
echo

# Mark verified directly via SQLite (since mail.provider=noop has no link).
sqlite3 $DATADIR/agenthub.db "UPDATE users SET email_verified_at = datetime('now') WHERE email='smoke@example.com';"

# Login.
JWT=$(curl -s -X POST http://127.0.0.1:18080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"smoke@example.com","password":"password9"}' | jq -r .token)
echo "JWT=$JWT"

# Pair code.
PAIR=$(curl -s -X POST http://127.0.0.1:18080/api/devices/pair-code \
  -H "Authorization: Bearer $JWT" | jq -r .code)
echo "PAIR=$PAIR"

# Claim.
CLAIM=$(curl -s -X POST http://127.0.0.1:18080/api/devices/claim \
  -H 'Content-Type: application/json' \
  -d "{\"code\":\"$PAIR\",\"name\":\"smokebox\",\"platform\":\"darwin\"}")
echo "$CLAIM"
TOK=$(echo "$CLAIM" | jq -r .api_token)

# Create + list + end session.
SID=$(curl -s -X POST http://127.0.0.1:18080/api/sessions \
  -H "Authorization: Token $TOK" -H 'Content-Type: application/json' \
  -d '{"label":"smoke","cwd":"/tmp"}' | jq -r .id)
curl -s -H "Authorization: Bearer $JWT" http://127.0.0.1:18080/api/sessions | jq .
curl -s -X POST -H "Authorization: Token $TOK" \
  http://127.0.0.1:18080/api/sessions/$SID/end -o /dev/null -w "end=%{http_code}\n"

kill -INT $PID
wait $PID 2>/dev/null
```

Expected:
- `pair-code` returns a 10-char code and an `expires_at` ~5 minutes in the future.
- `claim` returns `{device_id, api_token=ahs_…, tailscale: {pre_auth_key=stub-…, control_url=https://stub.invalid/headscale, …}}`.
- session list shows the one session with `status: "running"` before end, and `status: "stopped"` plus a non-null `ended_at` after end.

- [ ] **Step 3: Tag**

```bash
git tag -a v0.4.0-devices-sessions -m "Plan 04: devices + agent_sessions domain

- 00004 migration: devices, device_pair_codes, agent_sessions
- internal/devices: Headscaler interface (StubHeadscaler for now), CRUD, pair-code, atomic claim (device + api_token + pre-auth key)
- internal/sessions: agent_sessions CRUD (metadata only)
- auth middleware: RequireAuthOrToken now injects device_id on device-scoped ahs_ tokens
- /api/devices: pair-code, claim (code-authed, rate-limited), list, get, tailscale-info, soft-delete
- /api/sessions: create/list/activity/end gated on device-token vs bearer as appropriate
- E2E integration test covering the full pair → claim → session lifecycle"
```

---

## Done state

- A signed-in user on device A can call `/api/devices/pair-code` and receive a 10-char short-lived code.
- A not-yet-authenticated device B can call `/api/devices/claim` with that code and receive an `ahs_` API token, a `devices` row, and a stubbed Headscale pre-auth-key payload.
- The `device_pair_codes` row is atomically consumed; reuse returns 400.
- The `api_tokens` row created by claim has `device_id` set, and `RequireAuthOrToken` injects that device_id into the request context.
- Device B can `POST /api/sessions` (create), `POST /api/sessions/{id}/activity` (touch), `POST /api/sessions/{id}/end` (stop). Device A's UI can `GET /api/sessions` to see them.
- Full test suite passes (unit + integration); `v0.4.0-devices-sessions` tagged.

## Exit to Plan 05

Plan 05 replaces `StubHeadscaler` with a real implementation backed by the embedded Headscale library: add `internal/headscale`, the `headscale_user_links` bridge table, and auto-create Headscale users on signup. The device claim flow will then mint real pre-auth keys instead of `stub-…` ones, with no changes required to `internal/devices`, `internal/api/devices.go`, or the device-side client contract.
