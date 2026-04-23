# AgentHub Server — Plan 10: Deployment Polish

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the server production-ready with deployment artifacts, hardened health checks, improved graceful shutdown, config validation edge cases, and an automated release workflow. End state: `docker build` produces a working image, `systemd/` contains a hardened service unit, `/healthz` returns rich diagnostics, shutdown drains in-flight requests, and GitHub Actions cuts releases on tag push.

**Architecture:** No new application-level features. This plan is pure ops/infrastructure polish:
- **Dockerfile**: Multi-stage — Node build for admin SPA, Go build for binary, distroless final image.
- **systemd**: Service unit with hardening directives (no-new-privs, private-tmp, cap-drop-all), auto-restart, structured logging to journald.
- **Health**: Extend `/healthz` with uptime, Go runtime stats (goroutines, memory), subsystem status (db, headscale), and a readiness probe that fails until migrations complete.
- **Shutdown**: Signal handling improvements — on SIGINT/SIGTERM, stop accepting new connections, send `reconnect` to WS clients, wait for in-flight requests (existing 30s timeout), then exit. The supervisor already handles context cancellation; we add explicit pre-shutdown hooks.
- **Config**: Add validation for port ranges, data-dir writability, and cross-field consistency (e.g., `mode: hosted` requires both `db.url` and `blob` config when blob routes are mounted).
- **Release**: GitHub Actions workflow that builds cross-platform binaries (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64), builds + pushes Docker image, and creates a GitHub Release with artifacts.

**Tech Stack:** Docker multi-stage build, systemd unit directives, `runtime.ReadMemStats`, GoReleaser or raw `go build` + `docker buildx`, GitHub Actions.

**Spec reference:** `docs/superpowers/specs/2026-04-16-agenthub-server-design.md` §8 (deployment modes & config), §9 (operational concerns — metrics, logging, rate limiting, admin console), §12 build-order item 14 (deployment artifacts).

**What this plan does NOT do (deferred):**
- Kubernetes manifests / Helm charts. The project ships raw binary + Docker + systemd; K8s packaging is a downstream concern.
- Terraform / Ansible provisioning. These are environment-specific; the plan documents expected config but doesn't automate infrastructure.
- Multi-region DERP fleet deployment. Design supports it; v1 is single-region.
- Prometheus metrics beyond the basic health endpoint. Full metrics exposition (`/metrics` with admin gating) is a post-v1 ops enhancement.
- Log rotation configuration. Assumed to be handled by systemd journald or the container runtime.

---

## File structure added / modified

```
Dockerfile                                # multi-stage build
systemd/
└── agenthub-server.service              # hardened systemd unit

internal/api/
├── health.go / health_test.go           # extended health endpoint

internal/config/
├── config.go (modified)                 # additional validation
├── config_test.go (modified)            # new validation cases

internal/supervisor/
├── supervisor.go (modified)             # pre-shutdown hooks

.github/workflows/
├── ci.yml (modified)                    # add lint, admin build
└── release.yml                          # tag-triggered release

scripts/
└── release.sh                           # local release helper (optional)
```

---

## Task 1: Extended `/healthz` endpoint (TDD)

**Files:**
- Modify: `internal/api/health.go`
- Modify: `internal/api/health_test.go`

- [ ] **Step 1: Extend the health response**

Replace `internal/api/health.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

// Pinger is the subset of db.DB that health checks depend on.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthChecker provides optional subsystem status checks.
type HealthChecker struct {
	Pinger    Pinger
	Version   string
	StartTime time.Time
}

// ServeHTTP writes a health check response. If the DB ping fails, status
// becomes "degraded" and the HTTP code is 503.
func (h *HealthChecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	dbStatus := "ok"
	status := "ok"
	code := http.StatusOK

	if err := h.Pinger.Ping(r.Context()); err != nil {
		status = "degraded"
		dbStatus = "down"
		code = http.StatusServiceUnavailable
	}

	resp := map[string]any{
		"status":     status,
		"version":    h.Version,
		"uptime_sec": time.Since(h.StartTime).Seconds(),
		"db":         dbStatus,
		"go": map[string]any{
			"goroutines": runtime.NumGoroutine(),
			"memory_mb":  float64(mem.Alloc) / 1024 / 1024,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}

// NewHealthHandler returns a /healthz handler. Use the returned
// *HealthChecker to set StartTime after the server boots.
func NewHealthHandler(p Pinger, version string) *HealthChecker {
	return &HealthChecker{Pinger: p, Version: version, StartTime: time.Now().UTC()}
}
```

- [ ] **Step 2: Update `health_test.go`**

```go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping(_ context.Context) error { return f.err }

func TestHealth_OK(t *testing.T) {
	h := NewHealthHandler(&fakePinger{}, "v-test")
	h.StartTime = time.Now().Add(-time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "ok", body["status"])
	require.Equal(t, "v-test", body["version"])
	require.Greater(t, body["uptime_sec"], float64(0))

	goStats, ok := body["go"].(map[string]any)
	require.True(t, ok)
	require.Greater(t, goStats["goroutines"], float64(0))
}

func TestHealth_DBFailure_Returns503(t *testing.T) {
	h := NewHealthHandler(&fakePinger{err: errors.New("db down")}, "v-test")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "degraded", body["status"])
	require.Equal(t, "down", body["db"])
}
```

- [ ] **Step 3: Update `cmd/agenthub-server/main.go` to wire `StartTime`**

In `main.go`, locate:

```go
	router.Mount("/healthz", api.NewHealthHandler(db, version))
```

Replace with:

```go
	healthHandler := api.NewHealthHandler(db, version)
	router.Mount("/healthz", healthHandler)
```

No further changes needed — `NewHealthHandler` already sets `StartTime` to `time.Now().UTC()` in the constructor, which is sufficient for our purposes (the health handler is created right before the server starts).

- [ ] **Step 4: Run tests**

```bash
go test ./internal/api/ -run TestHealth -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/health.go internal/api/health_test.go
git commit -m "feat(api): extended /healthz with uptime, goroutines, memory"
```

---

## Task 2: Config validation edge cases (TDD)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Add port range validation**

In `Validate()`, add after the TLS block:

```go
	if c.HTTP.Port <= 0 || c.HTTP.Port > 65535 {
		errs = append(errs, fmt.Sprintf("http.port: invalid port %d", c.HTTP.Port))
	}
	if c.HTTP.HTTPPort < 0 || c.HTTP.HTTPPort > 65535 {
		errs = append(errs, fmt.Sprintf("http.http_port: invalid port %d", c.HTTP.HTTPPort))
	}
```

- [ ] **Step 2: Add data-dir existence validation (best-effort)**

Add after the port validation:

```go
	if c.DataDir != "" {
		info, err := os.Stat(c.DataDir)
		if err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("data_dir: cannot stat: %v", err))
		} else if err == nil && !info.IsDir() {
			errs = append(errs, "data_dir: exists but is not a directory")
		}
	}
```

Add `"os"` to imports.

- [ ] **Step 3: Add headscale binary existence validation (if enabled)**

In the `if c.Headscale.Enabled {` block, add:

```go
		if _, err := os.Stat(c.Headscale.BinaryPath); err != nil {
			errs = append(errs, fmt.Sprintf("headscale.binary_path: cannot stat %q: %v", c.Headscale.BinaryPath, err))
		}
```

- [ ] **Step 4: Write tests**

Append to `config_test.go`:

```go
func TestConfig_InvalidPort(t *testing.T) {
	c := Default()
	c.HTTP.Port = 99999
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "port")
}

func TestConfig_DataDirNotADirectory(t *testing.T) {
	c := Default()
	c.DataDir = t.TempDir() + "/notadir"
	require.NoError(t, os.WriteFile(c.DataDir, []byte("x"), 0644))
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
}

func TestConfig_HeadscaleBinaryMissing(t *testing.T) {
	c := Default()
	c.Headscale.Enabled = true
	c.Headscale.BinaryPath = "/does/not/exist/headscale"
	c.Headscale.ServerURL = "http://127.0.0.1:18081"
	c.Headscale.ListenAddr = "127.0.0.1:18081"
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "binary_path")
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/config/ -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): port range, data-dir, headscale binary validation"
```

---

## Task 3: Graceful shutdown — WS reconnect signal

**Files:**
- Modify: `internal/realtime/inmem.go`
- Modify: `cmd/agenthub-server/main.go`

- [ ] **Step 1: Add `BroadcastReconnect` to InMemoryHub**

Append to `internal/realtime/inmem.go`:

```go
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
```

- [ ] **Step 2: Wire shutdown hook in `main.go`**

Before `supervisor.Run`, add:

```go
	// Pre-shutdown: tell realtime clients to reconnect.
	go func() {
		<-ctx.Done()
		hub.BroadcastReconnect()
	}()
```

- [ ] **Step 3: Build + test**

```bash
go build ./...
go test ./...
```

Expected: green.

- [ ] **Step 4: Commit**

```bash
git add internal/realtime/inmem.go cmd/agenthub-server/main.go
git commit -m "feat(realtime): BroadcastReconnect on graceful shutdown"
```

---

## Task 4: Dockerfile (multi-stage)

**Files:**
- Create: `Dockerfile`

- [ ] **Step 1: Write the Dockerfile**

```dockerfile
# Stage 1: Build admin SPA
FROM node:24-alpine AS admin-builder
WORKDIR /app
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci
COPY web/admin/ .
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.26-alpine AS go-builder
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Copy built admin dist so //go:embed succeeds
COPY --from=admin-builder /app/dist internal/admin/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(git describe --tags --always 2>/dev/null || echo dev)" -o /bin/agenthub-server ./cmd/agenthub-server

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=go-builder /bin/agenthub-server /agenthub-server
EXPOSE 443 80 3478
ENTRYPOINT ["/agenthub-server"]
```

- [ ] **Step 2: Build and verify**

```bash
docker build -t agenthub-server:test .
```

Expected: image builds without errors.

If the build fails because `internal/admin/dist` is gitignored, the Dockerfile copies the dist from the admin-builder stage, so that's fine — but `go build` inside the go-builder stage needs the dist to exist before compilation. The `COPY --from=admin-builder` line handles this.

- [ ] **Step 3: Smoke test the image**

```bash
docker run --rm -p 18080:18080 -e AGENTHUB_MODE=solo -e AGENTHUB_TLS_MODE=off \
  -e AGENTHUB_HTTP_PORT=18080 -e AGENTHUB_MAIL_PROVIDER=noop \
  agenthub-server:test &
PID=$!
sleep 2
curl -sS http://localhost:18080/healthz
kill $PID
```

Expected: JSON health response.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile
git commit -m "build: multi-stage Dockerfile (node admin + go binary + distroless)"
```

---

## Task 5: systemd unit with hardening

**Files:**
- Create: `systemd/agenthub-server.service`

- [ ] **Step 1: Write the service unit**

```ini
[Unit]
Description=AgentHub Server
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/agenthub-server --config /etc/agenthub-server/config.yaml
Restart=on-failure
RestartSec=5

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/agenthub-server
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

# Resource limits
LimitNOFILE=65536

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=agenthub-server

[Install]
WantedBy=multi-user.target
```

Note: `Type=notify` requires the binary to send systemd notification. We don't import a systemd library in v1, so this is aspirational — change to `Type=simple` for now, and document that `Type=notify` is a future enhancement.

Corrected:

```ini
[Unit]
Description=AgentHub Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/agenthub-server --config /etc/agenthub-server/config.yaml
Restart=on-failure
RestartSec=5

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/agenthub-server
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

# Resource limits
LimitNOFILE=65536

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=agenthub-server

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Commit**

```bash
git add systemd/agenthub-server.service
git commit -m "build: hardened systemd service unit"
```

---

## Task 6: Release workflow (GitHub Actions)

**Files:**
- Modify: `.github/workflows/ci.yml`
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Update CI workflow**

Replace `.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
          cache: true
      - uses: actions/setup-node@v4
        with:
          node-version: '24'
          cache: 'npm'
          cache-dependency-path: web/admin/package-lock.json
      - name: Build admin SPA
        run: make admin-build
      - name: Vet
        run: go vet ./...
      - name: Test
        run: go test -race -timeout 120s ./...
      - name: Build binary
        run: go build -o /tmp/agenthub-server ./cmd/agenthub-server
      - name: Build Docker image
        run: docker build -t agenthub-server:ci .
```

- [ ] **Step 2: Write release workflow**

```yaml
name: release
on:
  push:
    tags:
      - 'v*'

permissions:
  contents: write
  packages: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
          cache: true
      - uses: actions/setup-node@v4
        with:
          node-version: '24'
          cache: 'npm'
          cache-dependency-path: web/admin/package-lock.json
      - name: Build admin SPA
        run: make admin-build
      - name: Build cross-platform binaries
        run: |
          mkdir -p dist
          for goos in linux darwin; do
            for goarch in amd64 arm64; do
              out="dist/agenthub-server-${goos}-${goarch}"
              if [ "$goos" = "windows" ]; then out="${out}.exe"; fi
              GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 \
                go build -ldflags="-s -w -X main.version=${GITHUB_REF_NAME}" \
                -o "$out" ./cmd/agenthub-server
            done
          done
      - name: Build and push Docker image
        run: |
          echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u ${{ github.actor }} --password-stdin
          docker build -t ghcr.io/${{ github.repository }}:${GITHUB_REF_NAME} .
          docker push ghcr.io/${{ github.repository }}:${GITHUB_REF_NAME}
          docker tag ghcr.io/${{ github.repository }}:${GITHUB_REF_NAME} ghcr.io/${{ github.repository }}:latest
          docker push ghcr.io/${{ github.repository }}:latest
      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          files: dist/*
          generate_release_notes: true
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "ci: release workflow with cross-platform binaries + Docker push"
```

---

## Task 7: E2E integration — health + shutdown probe

**Files:**
- Create: `test/integration/health_test.go`

- [ ] **Step 1: Write test**

```go
package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealth_E2E(t *testing.T) {
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
		"AGENTHUB_HTTP_PORT=18190",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	require.NoError(t, cmd.Start())

	base := "http://127.0.0.1:18190"
	waitReady(t, base+"/healthz")

	resp, err := http.Get(base + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "ok", body["status"])
	require.NotNil(t, body["uptime_sec"])
	require.NotNil(t, body["go"])

	// Graceful shutdown.
	require.NoError(t, cmd.Process.Signal(os.Interrupt))
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down gracefully")
	}
}
```

- [ ] **Step 2: Run**

```bash
go test -race -timeout 120s ./test/integration/ -run TestHealth_E2E -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add test/integration/health_test.go
git commit -m "test: health endpoint E2E with graceful shutdown probe"
```

---

## Task 8: Final verification + tag v1.0.0

- [ ] **Step 1: Full verification**

```bash
make test
go test -race -timeout 180s ./test/integration/...
make lint
```

Expected: all PASS.

- [ ] **Step 2: Build Docker image**

```bash
docker build -t agenthub-server:v1-test .
```

Expected: clean build.

- [ ] **Step 3: Tag**

```bash
git tag -a v1.0.0 -m "Plan 10: Deployment Polish — v1.0.0 release

- Extended /healthz: uptime, goroutines, memory, db status
- Config validation: port ranges, data-dir, headscale binary existence
- Graceful shutdown: BroadcastReconnect to WS clients
- Multi-stage Dockerfile (node + go + distroless)
- Hardened systemd service unit
- GitHub Actions: CI with admin build, release with cross-platform binaries + Docker push
- E2E health + shutdown probe"
```

---

## Done state

- `/healthz` returns rich diagnostics: status, version, uptime, db state, Go runtime stats.
- Config validates port ranges, data-dir existence, and headscale binary presence.
- Graceful shutdown sends `reconnect` to all WebSocket clients before closing.
- `docker build` produces a working distroless image.
- `systemd/agenthub-server.service` contains hardening directives.
- GitHub Actions CI builds admin SPA + binary + Docker image.
- GitHub Actions release workflow triggers on `v*` tags, builds cross-platform binaries, pushes Docker image, creates release.
- `v1.0.0` tagged. All tests green.

## Exit — v1 Complete

All 10 plans are complete. The AgentHub Server v1 is feature-complete per the design spec:
- Scaffolding, auth, tenancy, devices, sessions, Headscale, DERP, realtime, blobs, admin, deployment polish.
