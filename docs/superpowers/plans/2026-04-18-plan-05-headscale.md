# AgentHub Server — Plan 05: Headscale Integration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `devices.StubHeadscaler` with a real Headscale integration so a claimed device gets a genuine pre-auth key it can use to join an actual tailnet. End state: our server supervises a pinned `headscale v0.28.0` subprocess, auto-creates a Headscale user on first device claim for each of our users, mints a real 5-minute pre-auth key via Headscale's gRPC admin API, and proxies `/headscale/*` through to the Headscale HTTP listener so there's still one hostname / one cert / one firewall rule per the spec.

**Architecture:** Subprocess, not embedded library. We ship Headscale as a separate binary alongside ours (fetched by a dev-time script and shipped in the release artifact), run it as `headscale serve -c <generated-config>.yaml`, and talk to its admin API over a UNIX socket. A new `internal/headscale` package owns: the generated Headscale config.yaml, subprocess lifecycle via `supervisor.Service`, a gRPC client wrapper, a bridge table (`headscale_user_links`) mapping our `(account_id, user_id)` to the opaque `uint64` Headscale assigns its users, and a `Service` type that implements the existing `devices.Headscaler` interface. Embedding is rejected because Headscale's library-level API is explicitly unstable (spec §5 risk #1); the subprocess boundary means upgrades are "swap the binary + check config schema".

**Tech Stack:** `github.com/juanfont/headscale v0.28.0` (generated gRPC stubs only — not `hscontrol/*`), `google.golang.org/grpc` + `google.golang.org/protobuf` (brought in transitively), `os/exec` for subprocess lifecycle, `net/http/httputil.ReverseProxy` for the `/headscale` pass-through. No new web deps.

**Spec reference:** `docs/superpowers/specs/2026-04-16-agenthub-server-design.md` §5 (Headscale subsystem + risks), §6 (`headscale_user_links` bridge table, Headscale data namespace), §7 Flow B (device registration mints Headscale pre-auth key), §12 build-order item 5. Risk §5 #1 (upstream API churn) is mitigated by the subprocess boundary.

**What this plan does NOT do (deferred):**
- Embedded DERP. Plan 06. For now Headscale points at Tailscale's public DERP URLs (`https://controlplane.tailscale.com/derpmap/default`).
- Magic DNS / tailnet domain. Plan 06. `base_domain` stays empty.
- OIDC login into Headscale. Our own auth remains the front door; Headscale is a back-office.
- Postgres backend for Headscale. SQLite only (Headscale upstream explicitly deprecates Postgres).
- Hot-reload of Headscale config on our config change. Full restart only.
- TLS between us and Headscale. Everything loopback: `localhost:<internal-port>` for HTTP, UNIX socket for gRPC.

---

## Pinned facts about Headscale v0.28.0

Baked into this plan so the implementer isn't re-researching them:

- Single statically-linked Go binary. Release assets: `headscale_0.28.0_<os>_<arch>` (no Windows build).
- Subcommand `headscale serve -c <path>` is the long-running server.
- gRPC admin API over UNIX socket by default (no TLS, no token — socket permissions are the gate). Config: `unix_socket: <path>` + `unix_socket_permission: "0770"`.
- Proto file: `proto/headscale/v1/headscale.proto`. `option go_package = "github.com/juanfont/headscale/gen/go/v1";`. The canonical in-tree import path the Headscale CLI itself uses is `github.com/juanfont/headscale/gen/go/headscale/v1` — **Task 5 verifies which one resolves via `go mod tidy`** and pins the plan to the working one.
- Service: `headscale.v1.HeadscaleService`. Relevant RPCs:
  - `CreateUser(CreateUserRequest{name, display_name, email, picture_url}) → CreateUserResponse{user}`
  - `ListUsers(ListUsersRequest{id?, name?, email?}) → ListUsersResponse{users}` — there is **no** `GetUser` RPC.
  - `CreatePreAuthKey(CreatePreAuthKeyRequest{user uint64, reusable bool, ephemeral bool, expiration Timestamp, acl_tags []string}) → CreatePreAuthKeyResponse{pre_auth_key}`
  - `PreAuthKey.key` is the raw string the device uses.
- `User.id` is `uint64` — that's what we store in `headscale_user_links.headscale_user_id`.
- Startup has no readiness signal; poll `GET /health` on the HTTP listener (also a gRPC `Health` RPC).
- `disable_check_updates: true` prevents phone-home on startup — always set.
- SIGINT / SIGTERM triggers graceful shutdown via `http.Server.Shutdown`; SIGHUP reloads policy/DERP.
- Headscale owns its own SQLite DB (`database.sqlite.path`). Don't try to share tables with our DB.

---

## File structure added / modified by this plan

```
scripts/
└── fetch-headscale.sh                     # Dev helper: download v0.28.0 for host platform → ./bin/headscale

internal/db/migrations/sqlite/
└── 00005_headscale_links.sql              # headscale_user_links bridge table

internal/config/
├── config.go                              # Add HeadscaleConfig section + env overrides + validation
└── config_test.go                         # Coverage for the new section

internal/headscale/
├── types.go                               # Link + Options types + package doc
├── config.go / config_test.go             # Generate Headscale YAML from our config (golden-file test)
├── client.go / client_test.go             # UNIX-socket gRPC client wrapper
├── bridge.go / bridge_test.go             # headscale_user_links CRUD
├── service.go / service_test.go           # EnsureUser + MintPreAuthKey; implements devices.Headscaler
└── supervisor.go / supervisor_test.go     # Start/stop the `headscale serve` subprocess

cmd/agenthub-server/
└── main.go                                # Wire supervisor, construct real Headscaler, mount /headscale reverse proxy

test/integration/
└── headscale_test.go                      # Spawns real headscale binary; skips if absent

go.mod                                     # +github.com/juanfont/headscale v0.28.0
```

---

## Task 1: Dev helper — fetch Headscale binary

Plan 05 depends on the `headscale` binary being available on dev machines and in CI. Upstream releases don't publish a Homebrew tap, and we don't want to compile from source on every dev box, so a small fetch script pins the version.

**Files:**
- Create: `scripts/fetch-headscale.sh`
- Modify: `Makefile` (add `headscale-bin` target)

- [ ] **Step 1: Write `scripts/fetch-headscale.sh`**

```bash
#!/usr/bin/env bash
# Download a pinned Headscale binary for the host platform into ./bin/headscale.
# Usage: scripts/fetch-headscale.sh [VERSION]
set -euo pipefail

VERSION="${1:-0.28.0}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) echo "unsupported arch: $ARCH_RAW" >&2; exit 1 ;;
esac

case "$OS" in
    linux|darwin|freebsd) ;;
    *) echo "unsupported os: $OS (Headscale publishes no Windows binary)" >&2; exit 1 ;;
esac

BIN_NAME="headscale_${VERSION}_${OS}_${ARCH}"
URL="https://github.com/juanfont/headscale/releases/download/v${VERSION}/${BIN_NAME}"
OUT_DIR="$(cd "$(dirname "$0")/.." && pwd)/bin"
OUT="$OUT_DIR/headscale"

mkdir -p "$OUT_DIR"

if [[ -x "$OUT" ]] && "$OUT" version 2>/dev/null | grep -q "$VERSION"; then
    echo "headscale $VERSION already present at $OUT"
    exit 0
fi

echo "fetching $URL"
curl -fsSL -o "$OUT.download" "$URL"
chmod +x "$OUT.download"
mv "$OUT.download" "$OUT"

echo "installed $("$OUT" version | head -n1) → $OUT"
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x scripts/fetch-headscale.sh
```

- [ ] **Step 3: Add Makefile target**

Read the current Makefile (it's short — lives at repo root). Append a `headscale-bin` target. After the existing `clean:` target, add:

```make
headscale-bin:
	./scripts/fetch-headscale.sh
```

And add `headscale-bin` to the `.PHONY` line at the top.

- [ ] **Step 4: Run it to confirm**

```bash
make headscale-bin
./bin/headscale version
```

Expected output line: `0.28.0`

(If the script errors, the fix belongs here — don't silently skip.)

- [ ] **Step 5: Commit**

```bash
git add scripts/fetch-headscale.sh Makefile
git commit -m "chore: pin Headscale v0.28.0 via scripts/fetch-headscale.sh"
```

---

## Task 2: Migration 00005 — headscale_user_links bridge table

**Files:**
- Create: `internal/db/migrations/sqlite/00005_headscale_links.sql`

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up

CREATE TABLE headscale_user_links (
    account_id          TEXT NOT NULL REFERENCES accounts(id),
    user_id             TEXT NOT NULL REFERENCES users(id),
    headscale_user_id   INTEGER NOT NULL,
    headscale_user_name TEXT NOT NULL,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (account_id, user_id)
);

CREATE UNIQUE INDEX idx_headscale_user_links_hs_id ON headscale_user_links(headscale_user_id);

-- +goose Down
DROP TABLE headscale_user_links;
```

Rationale: `(account_id, user_id)` is our external key; `headscale_user_id` is the `uint64` Headscale assigns. `headscale_user_name` is redundant but convenient for logs — it's whatever we passed as `CreateUserRequest.name`.

- [ ] **Step 2: Verify migrations still apply**

Run: `go test ./internal/db/...`
Expected: PASS. (The existing migration tests iterate all `.sql` files under `sqlite/` and assert clean boot-up.)

- [ ] **Step 3: Commit**

```bash
git add internal/db/migrations/sqlite/00005_headscale_links.sql
git commit -m "feat(db/migrations): headscale_user_links bridge table"
```

---

## Task 3: Config extension — HeadscaleConfig

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Add the type + plug it into `Config`**

Open `internal/config/config.go`. Find the `type Config struct` block. Add:

```go
type HeadscaleConfig struct {
	Enabled             bool   `yaml:"enabled"`
	BinaryPath          string `yaml:"binary_path"`       // path to the `headscale` executable
	DataDir             string `yaml:"data_dir"`          // where Headscale stores its own SQLite + noise keys
	ServerURL           string `yaml:"server_url"`        // e.g. https://<hostname>/headscale — public URL embedded in tailnet configs
	ListenAddr          string `yaml:"listen_addr"`       // Headscale's HTTP listen — loopback only
	MetricsListenAddr   string `yaml:"metrics_listen_addr"`
	GRPCListenAddr      string `yaml:"grpc_listen_addr"`  // Required even with UNIX socket; loopback + grpc_allow_insecure=true
	UnixSocket          string `yaml:"unix_socket"`
	ShutdownTimeout     time.Duration `yaml:"shutdown_timeout"`
	ReadyTimeout        time.Duration `yaml:"ready_timeout"`
}
```

Then in the `Config` struct, add:

```go
	Headscale HeadscaleConfig `yaml:"headscale"`
```

- [ ] **Step 2: Add defaults**

In `func Default() Config`, add to the returned struct:

```go
		Headscale: HeadscaleConfig{
			Enabled:           false, // opt-in for Plan 05; Plan 04 StubHeadscaler is the default
			BinaryPath:        "./bin/headscale",
			DataDir:           "", // derived from top-level DataDir at load time
			ServerURL:         "http://127.0.0.1:18081",
			ListenAddr:        "127.0.0.1:18081",
			MetricsListenAddr: "", // disabled by default
			GRPCListenAddr:    "127.0.0.1:50443",
			UnixSocket:        "", // derived from DataDir at load time
			ShutdownTimeout:   10 * time.Second,
			ReadyTimeout:      10 * time.Second,
		},
```

- [ ] **Step 3: Derive empty paths in `Load`**

After the call to `applyEnv(&c)` in `Load`, add the derivation block:

```go
	// Derive Headscale paths from DataDir if the caller didn't override them.
	if c.Headscale.DataDir == "" {
		c.Headscale.DataDir = filepath.Join(c.DataDir, "headscale")
	}
	if c.Headscale.UnixSocket == "" {
		c.Headscale.UnixSocket = filepath.Join(c.Headscale.DataDir, "headscale.sock")
	}
```

- [ ] **Step 4: Add env overrides**

In `applyEnv`, append:

```go
	if v := os.Getenv("AGENTHUB_HEADSCALE_ENABLED"); v != "" {
		c.Headscale.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_BINARY_PATH"); v != "" {
		c.Headscale.BinaryPath = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_DATA_DIR"); v != "" {
		c.Headscale.DataDir = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_SERVER_URL"); v != "" {
		c.Headscale.ServerURL = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_LISTEN_ADDR"); v != "" {
		c.Headscale.ListenAddr = v
	}
	if v := os.Getenv("AGENTHUB_HEADSCALE_UNIX_SOCKET"); v != "" {
		c.Headscale.UnixSocket = v
	}
```

- [ ] **Step 5: Add validation**

In `Validate`, before the final error-collection:

```go
	if c.Headscale.Enabled {
		if c.Headscale.BinaryPath == "" {
			errs = append(errs, "headscale.binary_path: required when headscale.enabled")
		}
		if c.Headscale.ServerURL == "" {
			errs = append(errs, "headscale.server_url: required when headscale.enabled")
		}
		if c.Headscale.ListenAddr == "" {
			errs = append(errs, "headscale.listen_addr: required when headscale.enabled")
		}
	}
```

- [ ] **Step 6: Add a test**

Read `internal/config/config_test.go` to match its style. Append:

```go
func TestConfig_HeadscaleDefaults(t *testing.T) {
	c, err := Load(LoadOptions{})
	require.NoError(t, err)
	require.False(t, c.Headscale.Enabled)
	require.Equal(t, "./bin/headscale", c.Headscale.BinaryPath)
	require.NotEmpty(t, c.Headscale.DataDir)
	require.NotEmpty(t, c.Headscale.UnixSocket)
	require.Equal(t, "127.0.0.1:18081", c.Headscale.ListenAddr)
}

func TestConfig_HeadscaleValidation(t *testing.T) {
	c := Default()
	c.Headscale.Enabled = true
	c.Headscale.BinaryPath = ""
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "headscale.binary_path")
}
```

Imports at the top of the test file must include `github.com/stretchr/testify/require` (likely already there) and `testing`.

- [ ] **Step 7: Run**

```bash
go test ./internal/config/... -v
```

Expected: PASS for both new tests plus no regressions.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): HeadscaleConfig section"
```

---

## Task 4: Headscale YAML config generator (TDD, golden file)

The subprocess needs a config.yaml. We own it — generated at boot from our config, written under `HeadscaleConfig.DataDir`, and re-rendered every restart. We test it with a golden file so drift is visible in PRs.

**Files:**
- Create: `internal/headscale/types.go`
- Create: `internal/headscale/config.go`
- Create: `internal/headscale/config_test.go`
- Create: `internal/headscale/testdata/expected_config.yaml`

- [ ] **Step 1: Write `internal/headscale/types.go`**

```go
// Package headscale owns the subprocess lifecycle, gRPC admin client, and
// (account_id, user_id) ↔ headscale user bridge used by the claim flow.
// The real Plan-05 implementation of the devices.Headscaler interface
// lives here; StubHeadscaler in internal/devices stays in the tree for
// tests and for the default (opt-in) disabled mode.
package headscale

import "time"

// Link is one row of the headscale_user_links bridge table.
type Link struct {
	AccountID         string
	UserID            string
	HeadscaleUserID   uint64
	HeadscaleUserName string
	CreatedAt         time.Time
}

// Options configures the Service + supervisor. It mirrors the
// HeadscaleConfig fields the implementation actually needs so the
// package doesn't import config directly.
type Options struct {
	BinaryPath      string
	DataDir         string
	ServerURL       string
	ListenAddr      string
	GRPCListenAddr  string
	UnixSocket      string
	ShutdownTimeout time.Duration
	ReadyTimeout    time.Duration
}
```

- [ ] **Step 2: Write `internal/headscale/testdata/expected_config.yaml`**

This is the exact YAML we expect `RenderConfig` to produce from a canonical fixture `Options`. The only fields we parametrize (and therefore the only ones that vary between builds) are paths + addrs + URLs.

```yaml
server_url: http://127.0.0.1:18081
listen_addr: 127.0.0.1:18081
metrics_listen_addr: ""
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: true
unix_socket: /tmp/fixture/hs/headscale.sock
unix_socket_permission: "0770"
disable_check_updates: true
noise:
  private_key_path: /tmp/fixture/hs/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
derp:
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: true
  update_frequency: 24h
database:
  type: sqlite
  sqlite:
    path: /tmp/fixture/hs/db.sqlite
    write_ahead_log: true
log:
  level: info
  format: json
policy:
  mode: file
  path: ""
dns:
  magic_dns: false
  base_domain: ""
```

- [ ] **Step 3: Write failing `internal/headscale/config_test.go`**

```go
package headscale

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderConfig_MatchesGolden(t *testing.T) {
	got, err := RenderConfig(Options{
		BinaryPath:     "/usr/local/bin/headscale",
		DataDir:        "/tmp/fixture/hs",
		ServerURL:      "http://127.0.0.1:18081",
		ListenAddr:     "127.0.0.1:18081",
		GRPCListenAddr: "127.0.0.1:50443",
		UnixSocket:     "/tmp/fixture/hs/headscale.sock",
	})
	require.NoError(t, err)

	want, err := os.ReadFile(filepath.Join("testdata", "expected_config.yaml"))
	require.NoError(t, err)

	require.Equal(t, string(want), string(got))
}

func TestRenderConfig_RejectsEmptyOptions(t *testing.T) {
	_, err := RenderConfig(Options{})
	require.Error(t, err)
}
```

- [ ] **Step 4: Run to confirm FAIL**

Run: `go test ./internal/headscale/ -run TestRenderConfig -v`
Expected: FAIL — `RenderConfig` not defined.

- [ ] **Step 5: Write `internal/headscale/config.go`**

```go
package headscale

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"
)

// RenderConfig returns the YAML that Headscale's `serve` subcommand should
// load. Every value is plumbed from Options — no hidden environment
// lookups, no reads from the caller's Config struct — so the output is a
// pure function of the input and trivially testable.
//
// Plan 06 will extend this to enable embedded DERP; Plan 05 uses
// Tailscale's public DERP map only.
func RenderConfig(opts Options) ([]byte, error) {
	if opts.DataDir == "" || opts.ServerURL == "" || opts.ListenAddr == "" ||
		opts.GRPCListenAddr == "" || opts.UnixSocket == "" {
		return nil, fmt.Errorf("headscale.RenderConfig: DataDir, ServerURL, ListenAddr, GRPCListenAddr, UnixSocket are all required")
	}

	data := map[string]string{
		"ServerURL":      opts.ServerURL,
		"ListenAddr":     opts.ListenAddr,
		"GRPCListenAddr": opts.GRPCListenAddr,
		"UnixSocket":     opts.UnixSocket,
		"NoiseKeyPath":   filepath.Join(opts.DataDir, "noise_private.key"),
		"DBPath":         filepath.Join(opts.DataDir, "db.sqlite"),
	}

	var buf bytes.Buffer
	if err := configTmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("headscale.RenderConfig: %w", err)
	}
	return buf.Bytes(), nil
}

var configTmpl = template.Must(template.New("headscale-config").Parse(`server_url: {{.ServerURL}}
listen_addr: {{.ListenAddr}}
metrics_listen_addr: ""
grpc_listen_addr: {{.GRPCListenAddr}}
grpc_allow_insecure: true
unix_socket: {{.UnixSocket}}
unix_socket_permission: "0770"
disable_check_updates: true
noise:
  private_key_path: {{.NoiseKeyPath}}
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
derp:
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: true
  update_frequency: 24h
database:
  type: sqlite
  sqlite:
    path: {{.DBPath}}
    write_ahead_log: true
log:
  level: info
  format: json
policy:
  mode: file
  path: ""
dns:
  magic_dns: false
  base_domain: ""
`))
```

- [ ] **Step 6: Run to confirm PASS**

Run: `go test ./internal/headscale/ -run TestRenderConfig -v`
Expected: PASS for both test cases.

If the golden assertion fails on whitespace (e.g. trailing newline), update the golden file to match the rendered output exactly — the template is the source of truth.

- [ ] **Step 7: Commit**

```bash
git add internal/headscale/types.go internal/headscale/config.go internal/headscale/config_test.go internal/headscale/testdata/expected_config.yaml
git commit -m "feat(headscale): YAML config generator with golden-file test"
```

---

## Task 5: Add Headscale module dep + gRPC client wrapper (TDD)

Import Headscale's generated proto stubs, then write a thin wrapper that connects to the UNIX socket and exposes a `CreateUser` / `ListUsers` / `CreatePreAuthKey` surface.

**Files:**
- Modify: `go.mod` / `go.sum` (via `go get`)
- Create: `internal/headscale/client.go`
- Create: `internal/headscale/client_test.go`

- [ ] **Step 1: Add the module dependency**

Run the following. If the first import path doesn't resolve, try the second and delete the first:

```bash
go get github.com/juanfont/headscale@v0.28.0
go doc github.com/juanfont/headscale/gen/go/headscale/v1 2>&1 | head -5
```

Expected: the `go doc` call prints the package summary (confirming the import path). If `gen/go/headscale/v1` doesn't resolve, try `gen/go/v1`:

```bash
go doc github.com/juanfont/headscale/gen/go/v1 2>&1 | head -5
```

Whichever of the two works is the path to use everywhere in Task 5+. **Pick one, use it in all `import` blocks in this plan.** The plan below writes `gen/go/headscale/v1` — if the alternate is correct, s/`gen/go/headscale/v1`/`gen/go/v1`/ in your edits.

- [ ] **Step 2: Confirm the module is captured in go.mod**

Run: `grep 'juanfont/headscale' go.mod`
Expected: one line like `github.com/juanfont/headscale v0.28.0`.

- [ ] **Step 3: Write failing `internal/headscale/client_test.go`**

This test exercises the client constructor against a fake gRPC server wired over an in-memory `bufconn`. It only validates that our thin wrapper hands through — we're not testing Headscale itself.

```go
package headscale

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
)

// fakeHeadscaleServer implements just enough of HeadscaleService to answer
// a ListUsers round-trip from our wrapper.
type fakeHeadscaleServer struct {
	v1.UnimplementedHeadscaleServiceServer
	lastListUsers *v1.ListUsersRequest
}

func (f *fakeHeadscaleServer) ListUsers(_ context.Context, in *v1.ListUsersRequest) (*v1.ListUsersResponse, error) {
	f.lastListUsers = in
	if in.GetName() == "exists" {
		return &v1.ListUsersResponse{Users: []*v1.User{{Id: 42, Name: "exists"}}}, nil
	}
	return &v1.ListUsersResponse{}, nil
}

func TestClient_FindUserByName(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	fake := &fakeHeadscaleServer{}
	v1.RegisterHeadscaleServiceServer(srv, fake)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	c := &Client{conn: conn, rpc: v1.NewHeadscaleServiceClient(conn)}

	user, err := c.FindUserByName(context.Background(), "exists")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, uint64(42), user.GetId())

	missing, err := c.FindUserByName(context.Background(), "nobody")
	require.NoError(t, err)
	require.Nil(t, missing)
}
```

- [ ] **Step 4: Run to confirm FAIL**

Run: `go test ./internal/headscale/ -run TestClient_FindUserByName -v`
Expected: FAIL — `Client`, `FindUserByName` not defined.

- [ ] **Step 5: Write `internal/headscale/client.go`**

```go
package headscale

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
)

// Client is a thin wrapper around the generated HeadscaleServiceClient.
// It connects over Headscale's UNIX socket (no TLS, no API key — socket
// permissions are the gate) and exposes the narrow surface our claim
// flow actually uses: FindUserByName, CreateUser, CreatePreAuthKey.
type Client struct {
	conn *grpc.ClientConn
	rpc  v1.HeadscaleServiceClient
}

// Dial connects to the UNIX socket at socketPath. The caller is
// responsible for eventually calling Close.
func Dial(socketPath string) (*Client, error) {
	conn, err := grpc.NewClient(
		"passthrough:///unix",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("headscale.Dial: %w", err)
	}
	return &Client{conn: conn, rpc: v1.NewHeadscaleServiceClient(conn)}, nil
}

// Close shuts the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// FindUserByName returns the first User matching name, or nil if no user
// matches. Headscale has no GetUser RPC — ListUsers with a name filter is
// the documented idiom.
func (c *Client) FindUserByName(ctx context.Context, name string) (*v1.User, error) {
	resp, err := c.rpc.ListUsers(ctx, &v1.ListUsersRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("headscale.FindUserByName: %w", err)
	}
	for _, u := range resp.GetUsers() {
		if u.GetName() == name {
			return u, nil
		}
	}
	return nil, nil
}

// CreateUser creates a Headscale user.
func (c *Client) CreateUser(ctx context.Context, name, displayName, email string) (*v1.User, error) {
	resp, err := c.rpc.CreateUser(ctx, &v1.CreateUserRequest{
		Name:        name,
		DisplayName: displayName,
		Email:       email,
	})
	if err != nil {
		return nil, fmt.Errorf("headscale.CreateUser: %w", err)
	}
	return resp.GetUser(), nil
}

// CreatePreAuthKey mints a one-shot, non-reusable, non-ephemeral pre-auth
// key bound to the given Headscale user id, valid for ttl.
func (c *Client) CreatePreAuthKey(ctx context.Context, userID uint64, ttl time.Duration) (*v1.PreAuthKey, error) {
	resp, err := c.rpc.CreatePreAuthKey(ctx, &v1.CreatePreAuthKeyRequest{
		User:       userID,
		Reusable:   false,
		Ephemeral:  false,
		Expiration: timestampFromTime(time.Now().Add(ttl)),
	})
	if err != nil {
		return nil, fmt.Errorf("headscale.CreatePreAuthKey: %w", err)
	}
	return resp.GetPreAuthKey(), nil
}
```

- [ ] **Step 6: Add a tiny timestamp helper**

Create `internal/headscale/timestamp.go`:

```go
package headscale

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestampFromTime wraps timestamppb.New — exists only so the rest of
// the package can import "time" but not the protobuf-specific type.
func timestampFromTime(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}
```

- [ ] **Step 7: Tidy go.mod + run tests**

Run:
```bash
go mod tidy
go test ./internal/headscale/ -v
```

Expected: PASS for all tests in the package. If `go mod tidy` promotes any indirect deps, commit them as part of this task — dep-promotion on first gRPC import is expected.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/headscale/client.go internal/headscale/client_test.go internal/headscale/timestamp.go
git commit -m "feat(headscale): gRPC client wrapper over UNIX socket"
```

---

## Task 6: Bridge table CRUD (TDD)

**Files:**
- Create: `internal/headscale/bridge.go`
- Create: `internal/headscale/bridge_test.go`

- [ ] **Step 1: Write failing `internal/headscale/bridge_test.go`**

```go
package headscale

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
)

func withBridgeTestDB(t *testing.T) *sql.DB {
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

func TestBridge_CreateAndGet(t *testing.T) {
	db := withBridgeTestDB(t)

	err := CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1",
		HeadscaleUserID: 7, HeadscaleUserName: "u-u1",
	})
	require.NoError(t, err)

	got, err := GetLink(context.Background(), db, "acct1", "u1")
	require.NoError(t, err)
	require.Equal(t, uint64(7), got.HeadscaleUserID)
	require.Equal(t, "u-u1", got.HeadscaleUserName)
	require.False(t, got.CreatedAt.IsZero())
}

func TestBridge_GetMissing(t *testing.T) {
	db := withBridgeTestDB(t)
	_, err := GetLink(context.Background(), db, "acct1", "u1")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestBridge_DuplicateRejected(t *testing.T) {
	db := withBridgeTestDB(t)
	require.NoError(t, CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1", HeadscaleUserID: 7, HeadscaleUserName: "u-u1",
	}))
	err := CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1", HeadscaleUserID: 8, HeadscaleUserName: "u-u1-2",
	})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run to confirm FAIL**

Run: `go test ./internal/headscale/ -run TestBridge -v`
Expected: FAIL — `CreateLink`, `GetLink` not defined.

- [ ] **Step 3: Write `internal/headscale/bridge.go`**

```go
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
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/headscale/ -run TestBridge -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/headscale/bridge.go internal/headscale/bridge_test.go
git commit -m "feat(headscale): headscale_user_links CRUD"
```

---

## Task 7: Service — EnsureUser + MintPreAuthKey (TDD, fake client)

This is the production replacement for `devices.StubHeadscaler`. It implements `devices.Headscaler.MintPreAuthKey` by:
1. Looking up the `(account_id, user_id) → headscale_user_id` bridge row.
2. On miss: calling Headscale `CreateUser` and inserting the bridge row in a tx.
3. Calling Headscale `CreatePreAuthKey` for that user.
4. Translating the gRPC response into the `devices.PreAuthKey` our API already ships.

The test injects a fake gRPC client via an interface — no real Headscale required.

**Files:**
- Create: `internal/headscale/service.go`
- Create: `internal/headscale/service_test.go`

- [ ] **Step 1: Write failing `internal/headscale/service_test.go`**

```go
package headscale

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"

	"github.com/scottkw/agenthub-server/internal/devices"
)

// fakeHSClient implements the subset of *Client methods Service calls.
type fakeHSClient struct {
	users      map[string]*v1.User // by name
	nextUserID uint64
	preauths   []fakeMintCall
}

type fakeMintCall struct {
	UserID uint64
	TTL    time.Duration
}

func (f *fakeHSClient) FindUserByName(_ context.Context, name string) (*v1.User, error) {
	return f.users[name], nil
}

func (f *fakeHSClient) CreateUser(_ context.Context, name, _, _ string) (*v1.User, error) {
	f.nextUserID++
	u := &v1.User{Id: f.nextUserID, Name: name}
	if f.users == nil {
		f.users = map[string]*v1.User{}
	}
	f.users[name] = u
	return u, nil
}

func (f *fakeHSClient) CreatePreAuthKey(_ context.Context, userID uint64, ttl time.Duration) (*v1.PreAuthKey, error) {
	f.preauths = append(f.preauths, fakeMintCall{UserID: userID, TTL: ttl})
	return &v1.PreAuthKey{Key: "hs-preauth-" + time.Now().UTC().Format("150405.000")}, nil
}

func TestService_MintPreAuthKey_CreatesUserOnFirstCall(t *testing.T) {
	db := withBridgeTestDB(t)
	fake := &fakeHSClient{}
	svc := &Service{
		DB:         db,
		Client:     fake,
		ServerURL:  "http://127.0.0.1:18081",
		UserPrefix: "u-",
	}

	out, err := svc.MintPreAuthKey(context.Background(), devices.PreAuthKeyInput{
		AccountID: "acct1", UserID: "u1", DeviceID: "dev1", TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Key)
	require.Equal(t, "http://127.0.0.1:18081", out.ControlURL)
	require.NotEmpty(t, out.DERPMapJSON)
	require.WithinDuration(t, time.Now().Add(5*time.Minute), out.ExpiresAt, 10*time.Second)

	link, err := GetLink(context.Background(), db, "acct1", "u1")
	require.NoError(t, err)
	require.Equal(t, "u-u1", link.HeadscaleUserName)
	require.Equal(t, uint64(1), link.HeadscaleUserID)

	require.Len(t, fake.preauths, 1)
	require.Equal(t, uint64(1), fake.preauths[0].UserID)
}

func TestService_MintPreAuthKey_ReusesLinkedUser(t *testing.T) {
	db := withBridgeTestDB(t)
	fake := &fakeHSClient{}
	// Pre-seed link + Headscale user.
	require.NoError(t, CreateLink(context.Background(), db, Link{
		AccountID: "acct1", UserID: "u1",
		HeadscaleUserID: 42, HeadscaleUserName: "u-u1",
	}))
	fake.users = map[string]*v1.User{"u-u1": {Id: 42, Name: "u-u1"}}
	fake.nextUserID = 42

	svc := &Service{DB: db, Client: fake, ServerURL: "http://x", UserPrefix: "u-"}

	_, err := svc.MintPreAuthKey(context.Background(), devices.PreAuthKeyInput{
		AccountID: "acct1", UserID: "u1", DeviceID: "dev1", TTL: time.Minute,
	})
	require.NoError(t, err)

	// Didn't create a second link row.
	var rows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM headscale_user_links WHERE account_id='acct1' AND user_id='u1'`).Scan(&rows))
	require.Equal(t, 1, rows)

	// Minted against the linked user id.
	require.Len(t, fake.preauths, 1)
	require.Equal(t, uint64(42), fake.preauths[0].UserID)
}
```

- [ ] **Step 2: Run to confirm FAIL**

Run: `go test ./internal/headscale/ -run TestService -v`
Expected: FAIL — `Service`, `Service.MintPreAuthKey` not defined.

- [ ] **Step 3: Write `internal/headscale/service.go`**

```go
package headscale

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"

	"github.com/scottkw/agenthub-server/internal/devices"
)

// clientAPI is the subset of *Client methods Service uses. Declared as an
// interface so tests can swap a fake without a real gRPC connection.
type clientAPI interface {
	FindUserByName(ctx context.Context, name string) (*v1.User, error)
	CreateUser(ctx context.Context, name, displayName, email string) (*v1.User, error)
	CreatePreAuthKey(ctx context.Context, userID uint64, ttl time.Duration) (*v1.PreAuthKey, error)
}

// Service glues the bridge table, the Headscale gRPC client, and the
// devices.Headscaler interface together. Construct one and pass it to
// api.DeviceRoutes.
type Service struct {
	DB         *sql.DB
	Client     clientAPI
	ServerURL  string // what we hand to the claiming device as control_url
	UserPrefix string // name prefix for Headscale users — e.g. "u-" → "u-<our-user-id>"
}

// Compile-time check that *Service satisfies devices.Headscaler.
var _ devices.Headscaler = (*Service)(nil)

// MintPreAuthKey ensures our user has a linked Headscale user and returns
// a fresh one-shot pre-auth key for that user. Atomic-enough: if the
// Headscale CreateUser call succeeds but the link INSERT fails, we'll see
// the Headscale user on the next attempt via FindUserByName and link it
// then — duplicate-safe.
func (s *Service) MintPreAuthKey(ctx context.Context, in devices.PreAuthKeyInput) (devices.PreAuthKey, error) {
	link, err := s.ensureLink(ctx, in.AccountID, in.UserID)
	if err != nil {
		return devices.PreAuthKey{}, err
	}

	pak, err := s.Client.CreatePreAuthKey(ctx, link.HeadscaleUserID, in.TTL)
	if err != nil {
		return devices.PreAuthKey{}, fmt.Errorf("MintPreAuthKey: %w", err)
	}

	return devices.PreAuthKey{
		Key:         pak.GetKey(),
		ControlURL:  s.ServerURL,
		DERPMapJSON: `{"Regions":{}}`, // Plan 06 replaces this with the real DERP map.
		ExpiresAt:   time.Now().Add(in.TTL),
	}, nil
}

// ensureLink returns an existing link row, or creates the Headscale user
// and the link row if needed.
func (s *Service) ensureLink(ctx context.Context, accountID, userID string) (Link, error) {
	existing, err := GetLink(ctx, s.DB, accountID, userID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Link{}, fmt.Errorf("ensureLink: lookup: %w", err)
	}

	name := s.UserPrefix + userID

	// If Headscale already has a user with our name (e.g. leaked from an
	// earlier aborted attempt), reuse it instead of double-creating.
	found, err := s.Client.FindUserByName(ctx, name)
	if err != nil {
		return Link{}, fmt.Errorf("ensureLink: find: %w", err)
	}
	var hsUser *v1.User
	if found != nil {
		hsUser = found
	} else {
		hsUser, err = s.Client.CreateUser(ctx, name, "", "")
		if err != nil {
			return Link{}, fmt.Errorf("ensureLink: create: %w", err)
		}
	}

	link := Link{
		AccountID:         accountID,
		UserID:            userID,
		HeadscaleUserID:   hsUser.GetId(),
		HeadscaleUserName: hsUser.GetName(),
	}
	if err := CreateLink(ctx, s.DB, link); err != nil {
		return Link{}, fmt.Errorf("ensureLink: link: %w", err)
	}
	return link, nil
}
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/headscale/ -run TestService -v`
Expected: PASS for both `TestService_*` tests.

- [ ] **Step 5: Commit**

```bash
git add internal/headscale/service.go internal/headscale/service_test.go
git commit -m "feat(headscale): Service — EnsureUser + MintPreAuthKey"
```

---

## Task 8: Subprocess supervisor (TDD)

Runs `headscale serve -c <path>` as a child process: start, wait for `/health`, stop on context cancel with SIGINT→timeout→SIGKILL. Tests use a fake "binary" (a shell script invoked via `sh -c`) to keep the unit tests hermetic; Task 10's integration test is what exercises the real `headscale` binary.

**Files:**
- Create: `internal/headscale/supervisor.go`
- Create: `internal/headscale/supervisor_test.go`

- [ ] **Step 1: Write failing `internal/headscale/supervisor_test.go`**

```go
package headscale

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupervisor_FailsFastOnMissingBinary(t *testing.T) {
	sv := NewSupervisor(Options{
		BinaryPath:   "/nope/does-not-exist",
		DataDir:      t.TempDir(),
		ReadyTimeout: time.Second,
	}, "http://127.0.0.1:1")
	err := sv.Start(context.Background())
	require.Error(t, err)
}

func TestSupervisor_WaitsForHealthEndpoint(t *testing.T) {
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ready:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)

	// Use `sh` + an infinite `sleep` as a stand-in for the headscale binary.
	// The supervisor polls srv.URL/health regardless of what the child does.
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)

	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte("# placeholder\n"), 0o644))

	sv := NewSupervisor(Options{
		BinaryPath:   sh,
		DataDir:      dataDir,
		ReadyTimeout: 3 * time.Second,
	}, srv.URL)
	sv.argsForTest = []string{"-c", "sleep 30"}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		time.Sleep(200 * time.Millisecond)
		close(ready)
	}()

	require.NoError(t, sv.Start(ctx))
	cancel()
	_ = sv.Wait(context.Background())
}
```

Note: the test imports `os/exec` — add it to the import block. If the `sh` stand-in proves flaky (e.g. it exits before the ready signal), lengthen the `sleep` or tune `ReadyTimeout`. Do NOT skip the test to work around flakiness.

- [ ] **Step 2: Run to confirm FAIL**

Run: `go test ./internal/headscale/ -run TestSupervisor -v`
Expected: FAIL — `Supervisor`, `NewSupervisor`, `Start`, `Wait` not defined.

- [ ] **Step 3: Write `internal/headscale/supervisor.go`**

```go
package headscale

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Supervisor runs `headscale serve` as a managed subprocess. It writes a
// config.yaml into DataDir at Start, spawns the child, polls the health
// endpoint until ready, and on context cancel sends SIGINT followed by
// (after ShutdownTimeout) SIGKILL.
type Supervisor struct {
	opts      Options
	baseURL   string // e.g. http://127.0.0.1:18081 — where /health lives
	cmd       *exec.Cmd
	waitErrCh chan error
	log       *slog.Logger

	// argsForTest overrides the normal ["serve", "-c", <path>] args.
	// Unexported; test-only.
	argsForTest []string
}

// NewSupervisor returns a configured supervisor. Call Start to spawn,
// Wait to block for exit, or rely on context cancellation to trigger
// graceful shutdown.
func NewSupervisor(opts Options, baseURL string) *Supervisor {
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 10 * time.Second
	}
	return &Supervisor{opts: opts, baseURL: baseURL, log: slog.Default()}
}

// WithLogger wires a logger in. Otherwise slog.Default is used.
func (s *Supervisor) WithLogger(l *slog.Logger) *Supervisor { s.log = l; return s }

// Start generates the config, spawns the subprocess, and blocks until the
// /health endpoint responds 200 or ReadyTimeout elapses. Returns a fatal
// error if the binary is missing, the config can't be written, or the
// subprocess exits before becoming ready.
func (s *Supervisor) Start(ctx context.Context) error {
	if _, err := os.Stat(s.opts.BinaryPath); err != nil {
		return fmt.Errorf("headscale supervisor: binary %q: %w", s.opts.BinaryPath, err)
	}

	if err := os.MkdirAll(s.opts.DataDir, 0o750); err != nil {
		return fmt.Errorf("headscale supervisor: mkdir datadir: %w", err)
	}
	cfgBytes, err := RenderConfig(s.opts)
	if err != nil {
		return fmt.Errorf("headscale supervisor: render config: %w", err)
	}
	cfgPath := filepath.Join(s.opts.DataDir, "config.yaml")
	if err := os.WriteFile(cfgPath, cfgBytes, 0o640); err != nil {
		return fmt.Errorf("headscale supervisor: write config: %w", err)
	}

	args := s.argsForTest
	if args == nil {
		args = []string{"serve", "-c", cfgPath}
	}
	s.cmd = exec.CommandContext(ctx, s.opts.BinaryPath, args...)
	s.cmd.Stdout = logWriter{log: s.log, level: slog.LevelInfo, source: "headscale"}
	s.cmd.Stderr = logWriter{log: s.log, level: slog.LevelWarn, source: "headscale"}
	// Ensure SIGINT on our Cancel propagates cleanly (default is SIGKILL).
	s.cmd.Cancel = func() error { return s.cmd.Process.Signal(syscall.SIGINT) }
	s.cmd.WaitDelay = s.opts.ShutdownTimeout

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("headscale supervisor: start: %w", err)
	}

	s.waitErrCh = make(chan error, 1)
	go func() { s.waitErrCh <- s.cmd.Wait() }()

	// Poll /health.
	deadline := time.Now().Add(s.opts.ReadyTimeout)
	for {
		if time.Now().After(deadline) {
			_ = s.signalShutdown()
			return fmt.Errorf("headscale supervisor: not ready within %s", s.opts.ReadyTimeout)
		}
		select {
		case err := <-s.waitErrCh:
			return fmt.Errorf("headscale supervisor: subprocess exited before ready: %w", err)
		default:
		}
		resp, err := http.Get(s.baseURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				s.log.Info("headscale ready", "url", s.baseURL)
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Wait blocks until the subprocess exits. Returns the exit error if any.
// If ctx is cancelled before the subprocess exits, Wait returns ctx.Err().
func (s *Supervisor) Wait(ctx context.Context) error {
	if s.waitErrCh == nil {
		return nil
	}
	select {
	case err := <-s.waitErrCh:
		if err == nil {
			return nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("headscale exited: %s", ee.Error())
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Supervisor) signalShutdown() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Signal(syscall.SIGINT)
}

// logWriter routes subprocess stdout/stderr into a structured logger.
type logWriter struct {
	log    *slog.Logger
	level  slog.Level
	source string
}

func (w logWriter) Write(p []byte) (int, error) {
	msg := string(p)
	w.log.Log(nil, w.level, msg, "source", w.source)
	return len(p), nil
}

// compile-time assertion that logWriter satisfies io.Writer
var _ io.Writer = logWriter{}
```

- [ ] **Step 4: Run to confirm PASS**

Run: `go test ./internal/headscale/ -run TestSupervisor -v`
Expected: PASS.

If the health-polling loop loops too aggressively in CI, increase the sleep to 200ms — don't lower `ReadyTimeout`.

- [ ] **Step 5: Commit**

```bash
git add internal/headscale/supervisor.go internal/headscale/supervisor_test.go
git commit -m "feat(headscale): subprocess supervisor with /health readiness"
```

---

## Task 9: main.go wiring — supervisor + real Headscaler + /headscale reverse proxy

**Files:**
- Modify: `cmd/agenthub-server/main.go`

- [ ] **Step 1: Pull in the new imports**

Read `cmd/agenthub-server/main.go`. In the import block, add:

```go
	"net/http/httputil"
	"net/url"

	"github.com/scottkw/agenthub-server/internal/headscale"
```

- [ ] **Step 2: Refactor `StubHeadscaler` site**

Locate the block from Task 9 of Plan 04 that reads:

```go
	// Plan 04 uses StubHeadscaler; Plan 05 will swap in the real embedded
	// Headscale integration.
	headscaler := devices.StubHeadscaler{}
```

Replace it with a factory call:

```go
	headscaler, hsSupervisor, hsClient, err := buildHeadscaler(ctx, cfg, db, logger)
	if err != nil {
		return fmt.Errorf("headscale: %w", err)
	}
	if hsClient != nil {
		defer hsClient.Close()
	}
```

- [ ] **Step 3: Add the `buildHeadscaler` helper**

Append to the same file, alongside `buildAuthService`:

```go
// buildHeadscaler returns the Headscaler implementation the api package
// should use. When Headscale is disabled (cfg.Headscale.Enabled=false)
// we keep the StubHeadscaler and return nil for the supervisor/client.
// When enabled we start the subprocess, wait for /health, dial the
// gRPC UNIX socket, and return a *headscale.Service.
func buildHeadscaler(ctx context.Context, cfg config.Config, db dbpkg.DB, log *slog.Logger) (devices.Headscaler, *headscale.Supervisor, *headscale.Client, error) {
	if !cfg.Headscale.Enabled {
		log.Info("headscale disabled (StubHeadscaler in use)")
		return devices.StubHeadscaler{}, nil, nil, nil
	}

	opts := headscale.Options{
		BinaryPath:      cfg.Headscale.BinaryPath,
		DataDir:         cfg.Headscale.DataDir,
		ServerURL:       cfg.Headscale.ServerURL,
		ListenAddr:      cfg.Headscale.ListenAddr,
		GRPCListenAddr:  cfg.Headscale.GRPCListenAddr,
		UnixSocket:      cfg.Headscale.UnixSocket,
		ShutdownTimeout: cfg.Headscale.ShutdownTimeout,
		ReadyTimeout:    cfg.Headscale.ReadyTimeout,
	}
	sv := headscale.NewSupervisor(opts, "http://"+cfg.Headscale.ListenAddr).WithLogger(log)
	if err := sv.Start(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("start supervisor: %w", err)
	}

	client, err := headscale.Dial(cfg.Headscale.UnixSocket)
	if err != nil {
		_ = sv.Wait(ctx) // best-effort
		return nil, nil, nil, fmt.Errorf("dial grpc: %w", err)
	}

	svc := &headscale.Service{
		DB:         db.SQL(),
		Client:     client,
		ServerURL:  cfg.Headscale.ServerURL,
		UserPrefix: "u-",
	}
	return svc, sv, client, nil
}
```

- [ ] **Step 4: Add the reverse proxy mount**

Locate where routes are mounted (just before `front, err := newFrontend(cfg, router)`). Insert:

```go
	if cfg.Headscale.Enabled {
		hsURL, err := url.Parse("http://" + cfg.Headscale.ListenAddr)
		if err != nil {
			return fmt.Errorf("parse headscale listen_addr: %w", err)
		}
		proxy := httputil.NewSingleHostReverseProxy(hsURL)
		router.Mount("/headscale", http.StripPrefix("/headscale", proxy))
	}
```

This gives clients the "one hostname, one cert, one firewall rule" surface the spec promises.

- [ ] **Step 5: Register the supervisor with the app supervisor**

Locate `supervisor.Run(ctx, []supervisor.Service{ ... })` near the bottom of `run()`. Extend the slice with a Headscale entry when the subprocess is running:

```go
	services := []supervisor.Service{
		{Name: "httpfront", Start: front.Start},
	}
	if hsSupervisor != nil {
		services = append(services, supervisor.Service{
			Name: "headscale",
			Start: func(ctx context.Context) error {
				return hsSupervisor.Wait(ctx)
			},
		})
	}

	err = supervisor.Run(ctx, services)
```

(Replace the existing literal slice + `supervisor.Run(ctx, ...)` call accordingly.)

- [ ] **Step 6: Build + test**

```bash
go build ./...
go test ./...
```

Expected: green. Any regressions in Plan 04's `TestDevices_*` or `TestSessions_*` mean the wiring broke — diagnose before moving on.

- [ ] **Step 7: Commit**

```bash
git add cmd/agenthub-server/main.go
git commit -m "feat(cmd): supervise Headscale subprocess, mount /headscale proxy"
```

---

## Task 10: Integration test — spawn real Headscale (skip if absent)

Boots our binary with `AGENTHUB_HEADSCALE_ENABLED=1`, drives a signup → login → pair → claim flow, and verifies the returned `api_token` can authenticate AND that the returned `pre_auth_key` appears in Headscale's gRPC `ListPreAuthKeys`.

**Files:**
- Create: `test/integration/headscale_test.go`

- [ ] **Step 1: Write the test**

```go
package integration

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
)

// TestHeadscale_EndToEnd boots our binary with Headscale enabled, drives
// pair + claim, and verifies the returned pre-auth key is visible via
// Headscale's own admin gRPC API. The test is skipped if the headscale
// binary isn't on disk; CI runs `scripts/fetch-headscale.sh` in setup.
func TestHeadscale_EndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Windows Headscale binary")
	}

	hsBin, err := exec.LookPath("./bin/headscale")
	if err != nil {
		// Try $PATH.
		hsBin, err = exec.LookPath("headscale")
		if err != nil {
			t.Skip("headscale binary not found; run `make headscale-bin`")
		}
	}

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()
	hsDataDir := filepath.Join(dataDir, "headscale")
	hsSocket := filepath.Join(hsDataDir, "headscale.sock")

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18185",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18185/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18185/api/auth/reset",
		"AGENTHUB_HEADSCALE_ENABLED=1",
		"AGENTHUB_HEADSCALE_BINARY_PATH="+hsBin,
		"AGENTHUB_HEADSCALE_SERVER_URL=http://127.0.0.1:18285",
		"AGENTHUB_HEADSCALE_LISTEN_ADDR=127.0.0.1:18285",
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
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	base := "http://127.0.0.1:18185"
	waitReady(t, base+"/healthz")

	// Signup + verify + login.
	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email": "hs-e2e@example.com", "password": "topsecretpw", "account_name": "HSE2E",
	}, 200)
	vTok := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": vTok}, 200)
	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "hs-e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginBody, &login))

	// Pair code.
	req, _ := http.NewRequest("POST", base+"/api/devices/pair-code", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&pair))

	// Claim.
	claimBody := postExpect(t, base+"/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "hs-laptop", "platform": "darwin", "app_version": "0.1.0",
	}, 200)
	var claim struct {
		APIToken  string `json:"api_token"`
		Tailscale struct {
			PreAuthKey string `json:"pre_auth_key"`
			ControlURL string `json:"control_url"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(claimBody, &claim))
	require.True(t, strings.HasPrefix(claim.APIToken, "ahs_"))
	require.False(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "stub-"),
		"expected real headscale key, got stub: %q", claim.Tailscale.PreAuthKey)
	require.NotEmpty(t, claim.Tailscale.PreAuthKey)
	require.Equal(t, "http://127.0.0.1:18285", claim.Tailscale.ControlURL)

	// Verify the key exists in Headscale via its own gRPC API.
	hsConn, err := grpc.NewClient(
		"passthrough:///unix",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", hsSocket)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer hsConn.Close()

	hs := v1.NewHeadscaleServiceClient(hsConn)
	users, err := hs.ListUsers(context.Background(), &v1.ListUsersRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, users.GetUsers(), "Headscale should have one user after claim")

	keys, err := hs.ListPreAuthKeys(context.Background(), &v1.ListPreAuthKeysRequest{})
	require.NoError(t, err)

	found := false
	for _, k := range keys.GetPreAuthKeys() {
		if k.GetKey() == claim.Tailscale.PreAuthKey {
			found = true
			break
		}
	}
	require.True(t, found, "claim pre-auth key should be present in Headscale")

	// Verify /headscale proxy is wired (GET /headscale/health should 200 since
	// it forwards to Headscale's own /health).
	proxyResp, err := http.Get(base + "/headscale/health")
	require.NoError(t, err)
	defer proxyResp.Body.Close()
	require.Equal(t, http.StatusOK, proxyResp.StatusCode)
}
```

- [ ] **Step 2: Run it**

```bash
make headscale-bin
go test -race -timeout 120s ./test/integration/ -run TestHeadscale_EndToEnd -v
```

Expected: PASS. Expect ~3–5s for Headscale first-boot (SQLite migrations + key generation).

If it fails with "connection refused" on the UNIX socket, increase the readiness poll interval in `supervisor.go` Step 3 loop, or confirm `unix_socket_permission` on `DataDir/headscale`.

If ListPreAuthKeys returns an empty list, the bridge-table path isn't wired — check that `Service.MintPreAuthKey` is being called by re-reading the claim handler's `Headscaler` arg.

- [ ] **Step 3: Run the whole integration suite to confirm no regressions**

```bash
go test -race -timeout 180s ./test/integration/...
```

Expected: every test in `test/integration/` passes (Plan 04's devices-sessions E2E, API-token E2E, auth-flow E2E, boot test, and the new Headscale test).

- [ ] **Step 4: Commit**

```bash
git add test/integration/headscale_test.go
git commit -m "test: Headscale E2E — pair → claim → Headscale admin API"
```

---

## Task 11: Final smoke + tag v0.5.0-headscale

- [ ] **Step 1: Full local verification**

```bash
make test
go test -race -timeout 180s ./test/integration/...
make lint
```

Expected: all PASS. If `gofmt -l .` flags anything, `gofmt -w .` + `git commit -am "style: gofmt"`.

- [ ] **Step 2: Manual smoke — run the binary end-to-end with Headscale on**

```bash
make headscale-bin
make build

DATADIR=$(mktemp -d)
AGENTHUB_MODE=solo AGENTHUB_TLS_MODE=off \
  AGENTHUB_HTTP_PORT=18080 AGENTHUB_DATA_DIR=$DATADIR \
  AGENTHUB_MAIL_PROVIDER=noop \
  AGENTHUB_HEADSCALE_ENABLED=1 \
  AGENTHUB_HEADSCALE_BINARY_PATH=$(pwd)/bin/headscale \
  AGENTHUB_HEADSCALE_SERVER_URL=http://127.0.0.1:18081 \
  AGENTHUB_HEADSCALE_LISTEN_ADDR=127.0.0.1:18081 \
  ./bin/agenthub-server &
PID=$!
sleep 3

curl -s http://127.0.0.1:18080/healthz
echo
curl -s http://127.0.0.1:18080/headscale/health
echo

# Signup + force-verify + login (noop mailer has no link).
curl -s -X POST http://127.0.0.1:18080/api/auth/signup -H 'Content-Type: application/json' \
  -d '{"email":"hs-smoke@example.com","password":"password9","account_name":"HSSmoke"}'
echo
sqlite3 $DATADIR/agenthub.db "UPDATE users SET email_verified_at = datetime('now') WHERE email='hs-smoke@example.com';"

JWT=$(curl -s -X POST http://127.0.0.1:18080/api/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"hs-smoke@example.com","password":"password9"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

PAIR=$(curl -s -X POST http://127.0.0.1:18080/api/devices/pair-code -H "Authorization: Bearer $JWT" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["code"])')

curl -s -X POST http://127.0.0.1:18080/api/devices/claim -H 'Content-Type: application/json' \
  -d "{\"code\":\"$PAIR\",\"name\":\"smokebox\",\"platform\":\"darwin\"}" | python3 -m json.tool

# Headscale's own CLI should see the user + the key.
./bin/headscale -c $DATADIR/headscale/config.yaml users list
./bin/headscale -c $DATADIR/headscale/config.yaml preauthkeys list -u u-<your-user-id>

kill -INT $PID
wait $PID 2>/dev/null
```

Expected:
- `/headscale/health` returns HTTP 200 (proxy is live).
- The claim response's `tailscale.pre_auth_key` does NOT start with `stub-` — it's a real Headscale key.
- `./bin/headscale users list` shows one `u-<uuid>` user.
- `./bin/headscale preauthkeys list -u <that-user>` lists one key matching the one from the claim response.

- [ ] **Step 3: Tag**

```bash
git tag -a v0.5.0-headscale -m "Plan 05: Headscale integration (subprocess)

- 00005 migration: headscale_user_links
- internal/headscale: YAML config generator, gRPC UNIX-socket client, bridge table CRUD, Service (EnsureUser + MintPreAuthKey), subprocess supervisor
- devices.Headscaler now has a production impl (StubHeadscaler kept for cfg.Headscale.Enabled=false)
- /api/devices/claim mints real Headscale pre-auth keys when enabled
- /headscale/* proxied through the main HTTP frontend — one hostname, one cert
- scripts/fetch-headscale.sh pins Headscale v0.28.0 for dev + CI"
```

---

## Done state

- A fresh solo-mode server with `AGENTHUB_HEADSCALE_ENABLED=1` and a `headscale` binary on disk boots cleanly, starts Headscale as a managed child, and answers both `/healthz` (ours) and `/headscale/health` (proxied).
- Claiming a device returns a real `{"pre_auth_key":"<headscale key>", "control_url":"<our server_url>", "derp_map_json":"..."}` payload — no more `stub-` prefix.
- Headscale's own admin tools (`headscale users list`, `headscale preauthkeys list`, gRPC `ListPreAuthKeys`) see the user and key.
- `headscale_user_links` has one row per `(account_id, user_id)` that's ever claimed a device. Re-claiming doesn't duplicate the Headscale user.
- Server restart cleanly stops Headscale via SIGINT and regenerates `config.yaml` on boot.
- `cfg.Headscale.Enabled=false` still works — `StubHeadscaler` is the fallback. Plan 04's test suite stays green unchanged.
- `v0.5.0-headscale` tagged.

## Exit to Plan 06

Plan 06 ("DERP") turns on Headscale's embedded DERP server (`derp.server.enabled: true`), advertises our region in the DERP map we hand to devices, and replaces the stub `"Regions":{}` DERP map in `Service.MintPreAuthKey`'s return with Headscale's live map. The `/derp` HTTP endpoint gets proxied through `httpfront` the same way `/headscale` does in this plan. Plan 07 (realtime) and Plan 08 (blob) are orthogonal and can land in parallel with 06.
