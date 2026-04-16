# AgentHub Server — Plan 01: Foundation & HTTP Frontend

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a runnable `agenthub-server` binary that loads config, opens a SQLite DB, applies migrations, serves HTTPS via certmagic (or plain HTTP when TLS is off), and responds on `/healthz`.

**Architecture:** Single Go binary. Packages live under `internal/`. Lifecycle is supervised by an `errgroup`-based supervisor. SQLite is the v1 backend (Postgres lands in Plan 08). Migrations via `pressly/goose`. Routing via `chi`. TLS auto-provisioning via `certmagic`. Tests use `testify` and `httptest`.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go SQLite, no CGo), `github.com/pressly/goose/v3`, `github.com/go-chi/chi/v5`, `github.com/caddyserver/certmagic`, `github.com/stretchr/testify`, `gopkg.in/yaml.v3`.

**Spec reference:** `docs/superpowers/specs/2026-04-16-agenthub-server-design.md` — §4 (architecture), §5 (root, config, db, supervisor, httpfront, obs), §8 (config precedence, secrets, running), §9 (logging, metrics, TLS).

**What this plan does NOT do (deferred to later plans):**
- Auth, tenancy, domain tables (Plan 02)
- Metrics endpoint, full audit log (Plan 02+)
- Postgres driver (Plan 08)
- Integration with Stripe / OAuth / email / Headscale / DERP / realtime (Plans 02–10)

---

## File structure after this plan

```
agenthub-server/
├── cmd/agenthub-server/main.go
├── go.mod / go.sum
├── Makefile
├── .gitignore
├── .github/workflows/ci.yml
├── README.md
├── config.example.yaml
├── deploy/
│   ├── systemd/agenthub-server.service
│   └── docker/Dockerfile
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── obs/
│   │   ├── logger.go
│   │   └── logger_test.go
│   ├── db/
│   │   ├── db.go
│   │   ├── sqlite/
│   │   │   ├── sqlite.go
│   │   │   └── sqlite_test.go
│   │   └── migrations/
│   │       ├── migrations.go
│   │       └── sqlite/
│   │           └── 00001_init.sql
│   ├── supervisor/
│   │   ├── supervisor.go
│   │   └── supervisor_test.go
│   ├── httpfront/
│   │   ├── httpfront.go
│   │   └── httpfront_test.go
│   └── api/
│       ├── health.go
│       └── health_test.go
└── test/integration/
    └── boot_test.go
```

Each file has one responsibility. `cmd/agenthub-server/main.go` is only wiring — no logic. Logic lives in `internal/*`.

---

## Task 1: Initialize Go module and repo scaffolding

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `Makefile`
- Create: `README.md`
- Create: `cmd/agenthub-server/main.go` (placeholder)

- [ ] **Step 1: Initialize the module**

Run from `/Users/ken/dev/agenthub-server`:
```bash
go mod init github.com/scottkw/agenthub-server
```

Expected: creates `go.mod` with `module github.com/scottkw/agenthub-server` and `go 1.26`.

- [ ] **Step 2: Create `.gitignore`**

```
# Binaries
/bin/
/agenthub-server
*.exe

# Data
*.db
*.db-shm
*.db-wal
/data/
/certs/

# Build
/dist/

# Go
vendor/
coverage.out

# Editors
.idea/
.vscode/
*.swp
.DS_Store
```

- [ ] **Step 3: Create `Makefile`**

```makefile
.PHONY: build test lint run clean

BINARY := bin/agenthub-server
PKG := ./...

build:
	go build -o $(BINARY) ./cmd/agenthub-server

test:
	go test -race -timeout 60s $(PKG)

lint:
	go vet $(PKG)
	gofmt -l -d .

run: build
	./$(BINARY) --config config.example.yaml

clean:
	rm -rf bin/ coverage.out
```

- [ ] **Step 4: Create placeholder `cmd/agenthub-server/main.go`**

```go
package main

import "fmt"

func main() {
	fmt.Println("agenthub-server: scaffolding ready")
}
```

- [ ] **Step 5: Create minimal `README.md`**

```markdown
# agenthub-server

Single-binary server for AgentHub: Headscale coordination, DERP relay,
auth, DB, object storage, realtime, billing, and admin console.

See `docs/superpowers/specs/2026-04-16-agenthub-server-design.md`.

## Build

    make build

## Run

    ./bin/agenthub-server --config config.example.yaml
```

- [ ] **Step 6: Verify build**

```bash
make build && ./bin/agenthub-server
```

Expected output: `agenthub-server: scaffolding ready`.

- [ ] **Step 7: Commit**

```bash
git init
git add .
git commit -m "chore: initialize Go module and project scaffolding"
```

---

## Task 2: Add CI workflow

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Write the workflow**

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
      - name: Vet
        run: go vet ./...
      - name: Test
        run: go test -race -timeout 120s ./...
      - name: Build
        run: go build -o /tmp/agenthub-server ./cmd/agenthub-server
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add GitHub Actions workflow for vet/test/build"
```

---

## Task 3: Structured logger (TDD)

**Files:**
- Create: `internal/obs/logger.go`
- Test: `internal/obs/logger_test.go`

- [ ] **Step 1: Write the failing test**

`internal/obs/logger_test.go`:
```go
package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newWithWriter(&buf, Options{Format: FormatJSON, Level: slog.LevelInfo})

	l.Info("hello", "key", "value")

	var obj map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &obj))
	require.Equal(t, "hello", obj["msg"])
	require.Equal(t, "value", obj["key"])
}

func TestNewLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := newWithWriter(&buf, Options{Format: FormatText, Level: slog.LevelInfo})

	l.Info("hello", "key", "value")

	require.Contains(t, buf.String(), "hello")
	require.Contains(t, buf.String(), "key=value")
}

func TestNewLogger_LevelFilters(t *testing.T) {
	var buf bytes.Buffer
	l := newWithWriter(&buf, Options{Format: FormatJSON, Level: slog.LevelWarn})

	l.Info("filtered")
	l.Warn("kept")

	out := buf.String()
	require.NotContains(t, out, "filtered")
	require.Contains(t, strings.ToLower(out), "kept")
}
```

- [ ] **Step 2: Add testify dep**

```bash
go get github.com/stretchr/testify@latest
```

- [ ] **Step 3: Run test — expect FAIL**

```bash
go test ./internal/obs/...
```

Expected: compilation failure because `newWithWriter`, `Options`, `FormatJSON`, `FormatText` are not defined.

- [ ] **Step 4: Write the implementation**

`internal/obs/logger.go`:
```go
// Package obs provides structured logging, metrics, and tracing primitives.
package obs

import (
	"io"
	"log/slog"
	"os"
)

type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

type Options struct {
	Format Format
	Level  slog.Level
}

// NewLogger builds a slog.Logger that writes to stderr with the given options.
func NewLogger(opts Options) *slog.Logger {
	return newWithWriter(os.Stderr, opts)
}

func newWithWriter(w io.Writer, opts Options) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{Level: opts.Level}
	var h slog.Handler
	switch opts.Format {
	case FormatText:
		h = slog.NewTextHandler(w, handlerOpts)
	default:
		h = slog.NewJSONHandler(w, handlerOpts)
	}
	return slog.New(h)
}
```

- [ ] **Step 5: Run test — expect PASS**

```bash
go test ./internal/obs/...
```

Expected: `ok github.com/scottkw/agenthub-server/internal/obs`.

- [ ] **Step 6: Commit**

```bash
git add internal/obs go.mod go.sum
git commit -m "feat(obs): add structured slog logger with json/text formats"
```

---

## Task 4: Typed Config struct with defaults (TDD)

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:
```go
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefault_SoloMode(t *testing.T) {
	c := Default()
	require.Equal(t, ModeSolo, c.Mode)
	require.Equal(t, DriverSQLite, c.DB.Driver)
	require.NotEmpty(t, c.DataDir)
	require.Equal(t, 443, c.HTTP.Port)
	require.Equal(t, TLSModeAuto, c.TLS.Mode)
	require.Equal(t, "info", c.Obs.LogLevel)
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
go test ./internal/config/...
```

Expected: compilation failure.

- [ ] **Step 3: Write the implementation**

`internal/config/config.go`:
```go
// Package config defines the typed server configuration and its loaders.
package config

import (
	"os"
	"path/filepath"
)

type Mode string

const (
	ModeSolo   Mode = "solo"
	ModeHosted Mode = "hosted"
)

type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

type TLSMode string

const (
	TLSModeAuto TLSMode = "auto"
	TLSModeFile TLSMode = "file"
	TLSModeOff  TLSMode = "off"
)

type Config struct {
	Mode     Mode        `yaml:"mode"`
	Hostname string      `yaml:"hostname"`
	DataDir  string      `yaml:"data_dir"`
	HTTP     HTTPConfig  `yaml:"http"`
	TLS      TLSConfig   `yaml:"tls"`
	DB       DBConfig    `yaml:"db"`
	Obs      ObsConfig   `yaml:"observability"`
}

type HTTPConfig struct {
	Port     int `yaml:"port"`
	HTTPPort int `yaml:"http_port"` // for ACME / redirect
}

type TLSConfig struct {
	Mode     TLSMode `yaml:"mode"`
	Email    string  `yaml:"email"`
	CertFile string  `yaml:"cert_file"`
	KeyFile  string  `yaml:"key_file"`
}

type DBConfig struct {
	Driver Driver `yaml:"driver"`
	URL    string `yaml:"url"`
}

type ObsConfig struct {
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
}

func Default() Config {
	return Config{
		Mode:     ModeSolo,
		Hostname: "localhost",
		DataDir:  defaultDataDir(),
		HTTP: HTTPConfig{
			Port:     443,
			HTTPPort: 80,
		},
		TLS: TLSConfig{
			Mode: TLSModeAuto,
		},
		DB: DBConfig{
			Driver: DriverSQLite,
		},
		Obs: ObsConfig{
			LogLevel:  "info",
			LogFormat: "json",
		},
	}
}

func defaultDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "agenthub-server")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".local", "share", "agenthub-server")
}
```

- [ ] **Step 4: Run test — expect PASS**

```bash
go test ./internal/config/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): typed Config struct with solo-mode defaults"
```

---

## Task 5: Config YAML + env loader with precedence (TDD)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:
```go
func TestLoad_YAMLAndEnvPrecedence(t *testing.T) {
	yamlPath := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(
		"mode: hosted\nhostname: example.test\ndb:\n  driver: postgres\n  url: postgres://yaml\n",
	), 0o600))

	t.Setenv("AGENTHUB_DB_URL", "postgres://env")
	t.Setenv("AGENTHUB_HOSTNAME", "")

	c, err := Load(LoadOptions{ConfigPath: yamlPath})
	require.NoError(t, err)

	// YAML overrides defaults.
	require.Equal(t, ModeHosted, c.Mode)
	require.Equal(t, "example.test", c.Hostname)
	// Env overrides YAML.
	require.Equal(t, "postgres://env", c.DB.URL)
	require.Equal(t, DriverPostgres, c.DB.Driver)
}

func TestLoad_NoConfigFile_ReturnsDefaults(t *testing.T) {
	c, err := Load(LoadOptions{ConfigPath: ""})
	require.NoError(t, err)
	require.Equal(t, ModeSolo, c.Mode)
}
```

Required imports (adjust existing block):
```go
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
go test ./internal/config/...
```

Expected: `Load` / `LoadOptions` undefined.

- [ ] **Step 3: Add yaml dep**

```bash
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 4: Implement `Load`**

Append to `internal/config/config.go`:
```go
import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

type LoadOptions struct {
	ConfigPath string
}

// Load resolves configuration from defaults, optional YAML, and environment.
// Precedence (highest wins): env → YAML → defaults.
func Load(opts LoadOptions) (Config, error) {
	c := Default()

	if opts.ConfigPath != "" {
		b, err := os.ReadFile(opts.ConfigPath)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}

	applyEnv(&c)

	return c, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("AGENTHUB_MODE"); v != "" {
		c.Mode = Mode(v)
	}
	if v := os.Getenv("AGENTHUB_HOSTNAME"); v != "" {
		c.Hostname = v
	}
	if v := os.Getenv("AGENTHUB_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("AGENTHUB_HTTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.HTTP.Port = n
		}
	}
	if v := os.Getenv("AGENTHUB_TLS_MODE"); v != "" {
		c.TLS.Mode = TLSMode(v)
	}
	if v := os.Getenv("AGENTHUB_TLS_EMAIL"); v != "" {
		c.TLS.Email = v
	}
	if v := os.Getenv("AGENTHUB_DB_DRIVER"); v != "" {
		c.DB.Driver = Driver(v)
	}
	if v := os.Getenv("AGENTHUB_DB_URL"); v != "" {
		c.DB.URL = v
		if c.DB.Driver == "" {
			c.DB.Driver = DriverPostgres
		}
	}
	if v := os.Getenv("AGENTHUB_LOG_LEVEL"); v != "" {
		c.Obs.LogLevel = v
	}
	if v := os.Getenv("AGENTHUB_LOG_FORMAT"); v != "" {
		c.Obs.LogFormat = v
	}
}
```

Merge the two `import` blocks into one at the top of the file.

- [ ] **Step 5: Run test — expect PASS**

```bash
go test ./internal/config/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config go.mod go.sum
git commit -m "feat(config): YAML + env loader with env-overrides-YAML precedence"
```

---

## Task 6: Config validation (TDD)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:
```go
func TestValidate_HostedRequiresDBURL(t *testing.T) {
	c := Default()
	c.Mode = ModeHosted
	c.DB.Driver = DriverPostgres
	c.DB.URL = "" // missing
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "db.url")
}

func TestValidate_TLSAutoRequiresEmail(t *testing.T) {
	c := Default()
	c.TLS.Mode = TLSModeAuto
	c.TLS.Email = ""
	c.Hostname = "example.com"
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "tls.email")
}

func TestValidate_Solo_AllowsMissingTLSEmail_WhenOff(t *testing.T) {
	c := Default()
	c.TLS.Mode = TLSModeOff
	require.NoError(t, c.Validate())
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
go test ./internal/config/...
```

Expected: `c.Validate` undefined.

- [ ] **Step 3: Implement `Validate`**

Append to `internal/config/config.go`:
```go
import "errors"

// Validate returns an error describing any invalid or inconsistent config.
func (c Config) Validate() error {
	var errs []string

	switch c.Mode {
	case ModeSolo, ModeHosted:
	default:
		errs = append(errs, fmt.Sprintf("mode: invalid value %q", c.Mode))
	}

	switch c.DB.Driver {
	case DriverSQLite:
		// URL optional (defaults to DataDir/agenthub.db at open time).
	case DriverPostgres:
		if c.DB.URL == "" {
			errs = append(errs, "db.url: required when db.driver=postgres")
		}
	default:
		errs = append(errs, fmt.Sprintf("db.driver: invalid value %q", c.DB.Driver))
	}

	if c.Mode == ModeHosted && c.DB.URL == "" {
		errs = append(errs, "db.url: required when mode=hosted")
	}

	switch c.TLS.Mode {
	case TLSModeOff:
	case TLSModeAuto:
		if c.TLS.Email == "" {
			errs = append(errs, "tls.email: required when tls.mode=auto (ACME registration)")
		}
	case TLSModeFile:
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			errs = append(errs, "tls.cert_file and tls.key_file: required when tls.mode=file")
		}
	default:
		errs = append(errs, fmt.Sprintf("tls.mode: invalid value %q", c.TLS.Mode))
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.New("config invalid: " + strings.Join(errs, "; "))
}
```

Add `"strings"` to the import block if not already present. Merge all `import` blocks into one.

- [ ] **Step 4: Run test — expect PASS**

```bash
go test ./internal/config/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): add Validate with mode/driver/tls invariants"
```

---

## Task 7: DB interface (TDD)

**Files:**
- Create: `internal/db/db.go`

- [ ] **Step 1: Write the interface**

`internal/db/db.go`:
```go
// Package db defines the storage interface implemented by sqlite and postgres.
package db

import (
	"context"
	"database/sql"
)

// DB is the database handle abstraction used by the rest of the server.
// Implementations wrap database/sql with driver-specific setup (pragmas, schema, etc).
type DB interface {
	// SQL returns the underlying *sql.DB for query execution.
	SQL() *sql.DB

	// Driver returns a short identifier: "sqlite" or "postgres".
	Driver() string

	// Ping verifies the connection is healthy.
	Ping(ctx context.Context) error

	// Close releases all resources.
	Close() error
}
```

- [ ] **Step 2: Verify package compiles**

```bash
go build ./internal/db/...
```

Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/db
git commit -m "feat(db): define DB interface (SQL, Driver, Ping, Close)"
```

---

## Task 8: SQLite driver implementation (TDD)

**Files:**
- Create: `internal/db/sqlite/sqlite.go`
- Test: `internal/db/sqlite/sqlite_test.go`

- [ ] **Step 1: Add SQLite dep**

```bash
go get modernc.org/sqlite@latest
```

- [ ] **Step 2: Write the failing test**

`internal/db/sqlite/sqlite_test.go`:
```go
package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpen_CreatesFileAndPings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	d, err := Open(Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	require.Equal(t, "sqlite", d.Driver())
	require.NotNil(t, d.SQL())
	require.NoError(t, d.Ping(context.Background()))

	// WAL is configured — we can query the pragma.
	var mode string
	require.NoError(t, d.SQL().QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)

	var fk int
	require.NoError(t, d.SQL().QueryRow("PRAGMA foreign_keys").Scan(&fk))
	require.Equal(t, 1, fk)
}

func TestOpen_InMemory(t *testing.T) {
	d, err := Open(Options{Path: ":memory:"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, d.Ping(context.Background()))
}
```

- [ ] **Step 3: Run test — expect FAIL**

```bash
go test ./internal/db/sqlite/...
```

Expected: `Open` / `Options` undefined.

- [ ] **Step 4: Write the implementation**

`internal/db/sqlite/sqlite.go`:
```go
// Package sqlite implements the db.DB interface using modernc.org/sqlite
// (pure-Go SQLite driver, no CGo).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Options struct {
	// Path is the file path (or ":memory:"). Directory must exist.
	Path string
	// MaxOpenConns defaults to 1 if zero (SQLite is single-writer).
	MaxOpenConns int
}

type db struct {
	sql *sql.DB
}

func Open(opts Options) (*db, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("sqlite.Open: Path is required")
	}
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = 1
	}

	// Configure via DSN query params: WAL + foreign keys + busy timeout.
	dsn := opts.Path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)

	if err := sqlDB.PingContext(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}

	return &db{sql: sqlDB}, nil
}

func (d *db) SQL() *sql.DB               { return d.sql }
func (d *db) Driver() string             { return "sqlite" }
func (d *db) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }
func (d *db) Close() error               { return d.sql.Close() }
```

- [ ] **Step 5: Run test — expect PASS**

```bash
go test ./internal/db/sqlite/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/db go.mod go.sum
git commit -m "feat(db/sqlite): pure-Go sqlite driver with WAL and foreign_keys"
```

---

## Task 9: Migration runner via goose (TDD)

**Files:**
- Create: `internal/db/migrations/migrations.go`
- Create: `internal/db/migrations/migrations_test.go`
- Create: `internal/db/migrations/sqlite/00001_init.sql`

- [ ] **Step 1: Add goose dep**

```bash
go get github.com/pressly/goose/v3@latest
```

- [ ] **Step 2: Write the first migration**

`internal/db/migrations/sqlite/00001_init.sql`:
```sql
-- +goose Up
CREATE TABLE app_meta (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO app_meta (key, value) VALUES ('schema_created_at', datetime('now'));

-- +goose Down
DROP TABLE app_meta;
```

- [ ] **Step 3: Write the failing test**

`internal/db/migrations/migrations_test.go`:
```go
package migrations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestApplySQLite_CreatesAppMeta(t *testing.T) {
	dir := t.TempDir()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(dir, "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	require.NoError(t, Apply(context.Background(), d))

	var count int
	err = d.SQL().QueryRow("SELECT COUNT(*) FROM app_meta WHERE key='schema_created_at'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestApplySQLite_Idempotent(t *testing.T) {
	dir := t.TempDir()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(dir, "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	require.NoError(t, Apply(context.Background(), d))
	require.NoError(t, Apply(context.Background(), d)) // second apply must be a no-op
}
```

- [ ] **Step 4: Run test — expect FAIL**

```bash
go test ./internal/db/migrations/...
```

Expected: `Apply` undefined.

- [ ] **Step 5: Write the runner**

`internal/db/migrations/migrations.go`:
```go
// Package migrations applies schema migrations using pressly/goose against
// a dialect-specific embedded SQL directory.
package migrations

import (
	"context"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/scottkw/agenthub-server/internal/db"
)

//go:embed sqlite/*.sql
var sqliteFS embed.FS

// Apply runs all pending migrations for d.Driver() against the database.
func Apply(ctx context.Context, d db.DB) error {
	goose.SetBaseFS(nil) // reset between calls in tests
	switch d.Driver() {
	case "sqlite":
		goose.SetBaseFS(sqliteFS)
		if err := goose.SetDialect("sqlite3"); err != nil {
			return fmt.Errorf("set dialect: %w", err)
		}
		return goose.UpContext(ctx, d.SQL(), "sqlite")
	default:
		return fmt.Errorf("migrations.Apply: unsupported driver %q", d.Driver())
	}
}
```

- [ ] **Step 6: Run test — expect PASS**

```bash
go test ./internal/db/migrations/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/db/migrations go.mod go.sum
git commit -m "feat(db/migrations): embed sqlite migrations and apply via goose"
```

---

## Task 10: Supervisor with graceful shutdown (TDD)

**Files:**
- Create: `internal/supervisor/supervisor.go`
- Test: `internal/supervisor/supervisor_test.go`

- [ ] **Step 1: Write the failing test**

`internal/supervisor/supervisor_test.go`:
```go
package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRun_AllServicesStartAndStop(t *testing.T) {
	var startedA, startedB int32
	svcs := []Service{
		{
			Name: "A",
			Start: func(ctx context.Context) error {
				atomic.AddInt32(&startedA, 1)
				<-ctx.Done()
				return nil
			},
		},
		{
			Name: "B",
			Start: func(ctx context.Context) error {
				atomic.AddInt32(&startedB, 1)
				<-ctx.Done()
				return nil
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, svcs) }()

	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), atomic.LoadInt32(&startedA))
	require.Equal(t, int32(1), atomic.LoadInt32(&startedB))

	cancel()
	require.NoError(t, <-done)
}

func TestRun_FirstFailureCancelsOthers(t *testing.T) {
	sentinel := errors.New("boom")
	var cancelled int32

	svcs := []Service{
		{
			Name: "fails",
			Start: func(ctx context.Context) error {
				return sentinel
			},
		},
		{
			Name: "watcher",
			Start: func(ctx context.Context) error {
				<-ctx.Done()
				atomic.StoreInt32(&cancelled, 1)
				return nil
			},
		},
	}

	err := Run(context.Background(), svcs)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, int32(1), atomic.LoadInt32(&cancelled))
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
go test ./internal/supervisor/...
```

- [ ] **Step 3: Write the implementation**

`internal/supervisor/supervisor.go`:
```go
// Package supervisor runs a set of long-lived services under a shared context
// and propagates the first error as the group failure.
package supervisor

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// Service is a named long-lived goroutine. Start must block until ctx is
// cancelled, unless it returns an error. A nil error on return is treated as
// an orderly shutdown.
type Service struct {
	Name  string
	Start func(ctx context.Context) error
}

// Run starts all services concurrently. Returns when:
//   - ctx is cancelled (returns ctx.Err() unless a service already failed), or
//   - any service returns a non-nil error (other services are signalled to
//     stop via context cancellation, and the first error is returned).
func Run(ctx context.Context, services []Service) error {
	g, gctx := errgroup.WithContext(ctx)

	for _, svc := range services {
		svc := svc
		if svc.Start == nil {
			return fmt.Errorf("supervisor: service %q has nil Start", svc.Name)
		}
		g.Go(func() error {
			if err := svc.Start(gctx); err != nil {
				return fmt.Errorf("%s: %w", svc.Name, err)
			}
			return nil
		})
	}

	err := g.Wait()
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
```

- [ ] **Step 4: Add errgroup dep**

```bash
go get golang.org/x/sync/errgroup@latest
```

- [ ] **Step 5: Run test — expect PASS**

```bash
go test ./internal/supervisor/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/supervisor go.mod go.sum
git commit -m "feat(supervisor): errgroup lifecycle with first-error cancellation"
```

---

## Task 11: Health handler (TDD)

**Files:**
- Create: `internal/api/health.go`
- Test: `internal/api/health_test.go`

- [ ] **Step 1: Write the failing test**

`internal/api/health_test.go`:
```go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

func TestHealth_OK(t *testing.T) {
	h := NewHealthHandler(fakePinger{nil}, "test-1.2.3")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	require.Equal(t, "ok", body["status"])
	require.Equal(t, "test-1.2.3", body["version"])
	require.Equal(t, "ok", body["db"])
}

func TestHealth_DBFailure_Returns503(t *testing.T) {
	h := NewHealthHandler(fakePinger{errors.New("connection refused")}, "v1")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	require.Equal(t, "degraded", body["status"])
	require.Equal(t, "down", body["db"])
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
go test ./internal/api/...
```

- [ ] **Step 3: Write the implementation**

`internal/api/health.go`:
```go
// Package api contains HTTP handlers for the public JSON API.
package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// Pinger is the subset of db.DB that health checks depend on.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewHealthHandler returns a /healthz handler that reports process + db status.
func NewHealthHandler(p Pinger, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]string{
			"status":  "ok",
			"version": version,
			"db":      "ok",
		}
		code := http.StatusOK

		if err := p.Ping(r.Context()); err != nil {
			resp["status"] = "degraded"
			resp["db"] = "down"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
```

- [ ] **Step 4: Run test — expect PASS**

```bash
go test ./internal/api/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat(api): /healthz handler with db-ping 503 degradation"
```

---

## Task 12: HTTP frontend with plain-HTTP mode (TDD)

**Files:**
- Create: `internal/httpfront/httpfront.go`
- Test: `internal/httpfront/httpfront_test.go`

- [ ] **Step 1: Add chi dep**

```bash
go get github.com/go-chi/chi/v5@latest
```

- [ ] **Step 2: Write the failing test**

`internal/httpfront/httpfront_test.go`:
```go
package httpfront

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestServer_PlainHTTP_ServesRoutes(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})

	srv, err := New(Options{
		Mode:    ModePlain,
		Address: "127.0.0.1:0",
		Handler: r,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	// Give the listener a moment to bind.
	require.Eventually(t, func() bool {
		return srv.Addr() != ""
	}, time.Second, 10*time.Millisecond)

	resp, err := http.Get("http://" + srv.Addr() + "/ping")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "pong", string(body))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	require.NoError(t, <-errCh)
}
```

- [ ] **Step 3: Run test — expect FAIL**

```bash
go test ./internal/httpfront/...
```

- [ ] **Step 4: Write the implementation**

`internal/httpfront/httpfront.go`:
```go
// Package httpfront wires the TLS/routing frontend for the server. It
// exposes a single Start/Stop lifecycle over either plain HTTP (for tests
// and solo mode with tls.mode: off) or HTTPS via certmagic (solo/hosted auto).
package httpfront

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
)

type Mode string

const (
	ModePlain Mode = "plain"
	ModeAuto  Mode = "auto"
	ModeFile  Mode = "file"
)

type Options struct {
	Mode     Mode
	Address  string // for ModePlain and ModeFile ("host:port"); "" for ModeAuto
	Handler  http.Handler
	CertFile string // ModeFile
	KeyFile  string // ModeFile
	Email    string // ModeAuto — ACME registration
	Domains  []string // ModeAuto — served hostnames
}

type Server struct {
	opts   Options
	srv    *http.Server
	addr   atomic.Pointer[string]
}

func New(opts Options) (*Server, error) {
	if opts.Handler == nil {
		return nil, fmt.Errorf("httpfront.New: Handler is required")
	}
	switch opts.Mode {
	case ModePlain, ModeFile:
		if opts.Address == "" {
			return nil, fmt.Errorf("httpfront.New: Address required for mode %q", opts.Mode)
		}
	case ModeAuto:
		if opts.Email == "" || len(opts.Domains) == 0 {
			return nil, fmt.Errorf("httpfront.New: Email and Domains required for auto TLS")
		}
	default:
		return nil, fmt.Errorf("httpfront.New: unknown mode %q", opts.Mode)
	}
	return &Server{opts: opts}, nil
}

// Addr returns the bound address (useful when Address used ":0" for a
// random port). Empty until Start has bound the listener.
func (s *Server) Addr() string {
	if p := s.addr.Load(); p != nil {
		return *p
	}
	return ""
}

// Start binds the listener and serves until ctx is cancelled, then shuts
// down gracefully with a 30s drain.
func (s *Server) Start(ctx context.Context) error {
	handler := s.opts.Handler

	switch s.opts.Mode {
	case ModePlain:
		return s.servePlain(ctx, handler)
	case ModeFile:
		return s.serveFile(ctx, handler)
	case ModeAuto:
		return s.serveAuto(ctx, handler)
	}
	return fmt.Errorf("httpfront.Start: unreachable")
}

func (s *Server) servePlain(ctx context.Context, h http.Handler) error {
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	addr := ln.Addr().String()
	s.addr.Store(&addr)

	s.srv = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s.runAndShutdown(ctx, func() error { return s.srv.Serve(ln) })
}

func (s *Server) serveFile(ctx context.Context, h http.Handler) error {
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	addr := ln.Addr().String()
	s.addr.Store(&addr)

	s.srv = &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s.runAndShutdown(ctx, func() error {
		return s.srv.ServeTLS(ln, s.opts.CertFile, s.opts.KeyFile)
	})
}

func (s *Server) serveAuto(ctx context.Context, h http.Handler) error {
	certmagic.DefaultACME.Email = s.opts.Email
	certmagic.DefaultACME.Agreed = true

	// HTTPS on 443, HTTP-01 challenges + redirect on 80.
	s.addr.Store(ptr("0.0.0.0:443"))

	// HTTPS returns only when the underlying server errors or is stopped.
	errCh := make(chan error, 1)
	go func() {
		errCh <- certmagic.HTTPS(s.opts.Domains, h)
	}()

	select {
	case <-ctx.Done():
		// certmagic.HTTPS has no graceful stop hook; exit when ctx is done.
		return ctx.Err()
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (s *Server) runAndShutdown(ctx context.Context, run func() error) error {
	errCh := make(chan error, 1)
	go func() { errCh <- run() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func ptr[T any](v T) *T { return &v }
```

- [ ] **Step 5: Add certmagic dep**

```bash
go get github.com/caddyserver/certmagic@latest
```

- [ ] **Step 6: Run test — expect PASS**

```bash
go test ./internal/httpfront/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/httpfront go.mod go.sum
git commit -m "feat(httpfront): plain/file/auto TLS frontend with graceful shutdown"
```

---

## Task 13: Wire main.go end-to-end

**Files:**
- Modify: `cmd/agenthub-server/main.go`

- [ ] **Step 1: Replace placeholder with the real entrypoint**

`cmd/agenthub-server/main.go`:
```go
// Command agenthub-server boots the server: config → logger → db →
// migrations → supervisor → HTTP frontend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/api"
	"github.com/scottkw/agenthub-server/internal/config"
	dbpkg "github.com/scottkw/agenthub-server/internal/db"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/httpfront"
	"github.com/scottkw/agenthub-server/internal/obs"
	"github.com/scottkw/agenthub-server/internal/supervisor"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agenthub-server:", err)
		os.Exit(1)
	}
}

func run() error {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(config.LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := obs.NewLogger(obs.Options{
		Format: obs.Format(cfg.Obs.LogFormat),
		Level:  parseLevel(cfg.Obs.LogLevel),
	})
	logger.Info("boot", "mode", cfg.Mode, "version", version)

	db, err := openDB(cfg)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := migrations.Apply(ctx, db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("migrations applied", "driver", db.Driver())

	router := chi.NewRouter()
	router.Mount("/healthz", api.NewHealthHandler(db, version))

	front, err := newFrontend(cfg, router)
	if err != nil {
		return fmt.Errorf("http frontend: %w", err)
	}

	err = supervisor.Run(ctx, []supervisor.Service{
		{
			Name:  "httpfront",
			Start: front.Start,
		},
	})
	if err != nil && err != context.Canceled {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

func openDB(cfg config.Config) (dbpkg.DB, error) {
	switch cfg.DB.Driver {
	case config.DriverSQLite:
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir data_dir: %w", err)
		}
		return sqlite.Open(sqlite.Options{Path: filepath.Join(cfg.DataDir, "agenthub.db")})
	default:
		return nil, fmt.Errorf("unsupported db driver %q (postgres lands in Plan 08)", cfg.DB.Driver)
	}
}

func newFrontend(cfg config.Config, h http.Handler) (*httpfront.Server, error) {
	switch cfg.TLS.Mode {
	case config.TLSModeOff:
		return httpfront.New(httpfront.Options{
			Mode:    httpfront.ModePlain,
			Address: fmt.Sprintf("0.0.0.0:%d", cfg.HTTP.Port),
			Handler: h,
		})
	case config.TLSModeFile:
		return httpfront.New(httpfront.Options{
			Mode:     httpfront.ModeFile,
			Address:  fmt.Sprintf("0.0.0.0:%d", cfg.HTTP.Port),
			Handler:  h,
			CertFile: cfg.TLS.CertFile,
			KeyFile:  cfg.TLS.KeyFile,
		})
	case config.TLSModeAuto:
		return httpfront.New(httpfront.Options{
			Mode:    httpfront.ModeAuto,
			Handler: h,
			Email:   cfg.TLS.Email,
			Domains: []string{cfg.Hostname},
		})
	}
	return nil, fmt.Errorf("unknown tls.mode %q", cfg.TLS.Mode)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

- [ ] **Step 2: Build**

```bash
make build
```

Expected: `bin/agenthub-server` produced, no errors.

- [ ] **Step 3: Smoke test plain HTTP**

```bash
AGENTHUB_MODE=solo \
AGENTHUB_TLS_MODE=off \
AGENTHUB_HTTP_PORT=18080 \
AGENTHUB_DATA_DIR=$(mktemp -d) \
./bin/agenthub-server &
SERVER_PID=$!
sleep 1
curl -s http://127.0.0.1:18080/healthz
kill $SERVER_PID
wait $SERVER_PID 2>/dev/null
```

Expected response (JSON, all on one line):
```json
{"db":"ok","status":"ok","version":"dev"}
```

- [ ] **Step 4: Commit**

```bash
git add cmd/agenthub-server/main.go
git commit -m "feat(cmd): wire config → logger → db → migrations → supervisor → httpfront"
```

---

## Task 14: End-to-end integration test

**Files:**
- Create: `test/integration/boot_test.go`

- [ ] **Step 1: Write the test**

`test/integration/boot_test.go`:
```go
// Package integration boots the binary as a subprocess and exercises real
// HTTP endpoints. Kept out of ./internal so it only runs via `go test ./test/...`.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoot_SoloMode_HealthzOK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-signal shutdown differs on Windows; covered by unit tests")
	}

	binary := buildBinary(t)
	dataDir := t.TempDir()

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18181",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resp *http.Response
	var err error
	require.Eventually(t, func() bool {
		resp, err = http.Get("http://127.0.0.1:18181/healthz")
		return err == nil && resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "server did not become ready")

	_ = ctx
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(body, &obj))
	require.Equal(t, "ok", obj["status"])
	require.Equal(t, "ok", obj["db"])
}

func buildBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "agenthub-server")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/agenthub-server")
	cmd.Dir = projectRoot(t)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run())
	return out
}

func projectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	// test/integration → project root
	return filepath.Dir(filepath.Dir(wd))
}
```

- [ ] **Step 2: Run the test**

```bash
go test -race -timeout 60s ./test/integration/...
```

Expected: PASS (takes ~3-10s because it builds + boots the binary).

- [ ] **Step 3: Commit**

```bash
git add test/integration
git commit -m "test: add integration test booting binary and calling /healthz"
```

---

## Task 15: Example config, systemd unit, Dockerfile

**Files:**
- Create: `config.example.yaml`
- Create: `deploy/systemd/agenthub-server.service`
- Create: `deploy/docker/Dockerfile`

- [ ] **Step 1: Write `config.example.yaml`**

```yaml
# agenthub-server example configuration.
# All values shown are the defaults for solo mode, except hostname and tls.email.

mode: solo                         # solo | hosted

hostname: agenthub.example.com     # required for tls.mode=auto
data_dir: /var/lib/agenthub-server

http:
  port: 443
  http_port: 80                    # for ACME + redirect when tls.mode=auto

tls:
  mode: auto                       # auto | file | off
  email: admin@example.com         # required when mode=auto
  # cert_file: /etc/ssl/certs/agenthub.crt
  # key_file: /etc/ssl/private/agenthub.key

db:
  driver: sqlite                   # sqlite | postgres (postgres lands in Plan 08)
  # url: postgres://user:pass@host/db

observability:
  log_level: info                  # debug | info | warn | error
  log_format: json                 # json | text
```

- [ ] **Step 2: Write `deploy/systemd/agenthub-server.service`**

```ini
[Unit]
Description=AgentHub Server
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
User=agenthub
Group=agenthub
ExecStart=/usr/local/bin/agenthub-server --config /etc/agenthub-server/config.yaml
Restart=on-failure
RestartSec=5s

# Hardening
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
NoNewPrivileges=true
ReadWritePaths=/var/lib/agenthub-server
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
LockPersonality=true
RestrictRealtime=true
RestrictSUIDSGID=true

# Graceful shutdown
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=45s

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 3: Write `deploy/docker/Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1.6
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/agenthub-server ./cmd/agenthub-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/agenthub-server /usr/local/bin/agenthub-server
USER nonroot:nonroot
EXPOSE 443 80 3478/udp
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/agenthub-server"]
```

- [ ] **Step 4: Verify the Docker build (optional, skip if no local Docker)**

```bash
docker build -f deploy/docker/Dockerfile -t agenthub-server:dev .
```

Expected: image builds cleanly.

- [ ] **Step 5: Commit**

```bash
git add config.example.yaml deploy/
git commit -m "deploy: add example config, systemd unit, and Dockerfile"
```

---

## Task 16: Flesh out README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Replace with full README**

```markdown
# agenthub-server

Single-binary server for [AgentHub](https://github.com/scottkw/agenthub):
Headscale coordination, DERP relay, auth, relational DB, object storage,
realtime fan-out, Stripe billing, and operator admin console — all in one
compiled Go binary.

Runs in two modes:
- **`solo`** — self-hosted default. SQLite + local FS + single default
  account. One binary, one data directory.
- **`hosted`** — multi-tenant SaaS on a VPS. Postgres + S3/R2 + many
  accounts. Same binary, different config.

See [`docs/superpowers/specs/2026-04-16-agenthub-server-design.md`](docs/superpowers/specs/2026-04-16-agenthub-server-design.md) for the design.

## Status

This is Plan 01 of the implementation series: foundation + HTTP frontend.
Subsequent plans add auth, devices, Headscale, DERP, realtime, blob, admin
SPA, Postgres, S3, and billing.

## Quick start (development)

    go build -o bin/agenthub-server ./cmd/agenthub-server

    # Plain HTTP on :18080, SQLite in a temp dir
    AGENTHUB_MODE=solo \
    AGENTHUB_TLS_MODE=off \
    AGENTHUB_HTTP_PORT=18080 \
    AGENTHUB_DATA_DIR=$(mktemp -d) \
    ./bin/agenthub-server

    curl http://127.0.0.1:18080/healthz
    # {"db":"ok","status":"ok","version":"dev"}

## Config

See [`config.example.yaml`](config.example.yaml). Precedence, highest wins:

1. Environment variables (`AGENTHUB_*`)
2. `--config` YAML file
3. Compiled defaults

## Tests

    make test                         # unit tests
    go test ./test/integration/...    # boots the binary and hits /healthz

## Deployment

- `deploy/systemd/agenthub-server.service` — hardened systemd unit.
- `deploy/docker/Dockerfile` — multi-stage, distroless image.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: expand README with dev quickstart and config guide"
```

---

## Task 17: Final smoke and tag

- [ ] **Step 1: Run full test suite**

```bash
make test
go test -race -timeout 120s ./test/integration/...
```

Expected: all PASS.

- [ ] **Step 2: Run lint**

```bash
make lint
```

Expected: no diff output, no vet warnings.

- [ ] **Step 3: Boot once manually**

```bash
rm -rf /tmp/ahs-smoke && mkdir -p /tmp/ahs-smoke
AGENTHUB_MODE=solo AGENTHUB_TLS_MODE=off \
AGENTHUB_HTTP_PORT=18080 AGENTHUB_DATA_DIR=/tmp/ahs-smoke \
./bin/agenthub-server &
PID=$!
sleep 2
curl -s http://127.0.0.1:18080/healthz | tee /dev/stderr
ls -la /tmp/ahs-smoke/   # should contain agenthub.db
kill $PID
```

Expected:
- `curl` returns `{"db":"ok","status":"ok","version":"dev"}`
- `ls` shows `agenthub.db` (plus WAL files).

- [ ] **Step 4: Tag**

```bash
git tag -a v0.1.0-foundation -m "Plan 01: foundation + HTTP frontend complete"
```

Do not push — left to operator discretion.

---

## Done state

At the end of this plan:

- `agenthub-server` builds cleanly on macOS + Linux with a single `go build`.
- Boots with empty config → serves plain HTTP in CI/dev, auto-TLS with certmagic in prod.
- SQLite opened with WAL + `foreign_keys=ON`; one migration creates `app_meta`.
- `/healthz` returns 200 with `{status, version, db}`; 503 when the DB is unreachable.
- Supervisor propagates first-subsystem failure as group failure with graceful 30s drain.
- systemd unit and distroless Dockerfile ready for deploy.
- CI passes on vet + test + build.

## Exit to Plan 02

Plan 02 ("Auth & Tenancy") picks up here by adding the first real domain tables (`users`, `accounts`, `memberships`, `auth_sessions`, `oauth_identities`, `verification_tokens`, `api_tokens`, `idempotency_keys`), an email+password + OAuth auth subsystem, JWT sessions with revocation, email verification and password reset flows via `internal/mail`, and new `/api/auth/*` endpoints mounted on the chi router wired here.
