# AgentHub Server — Plan 02: Core Password Auth & Tenancy

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver end-to-end password-based auth on top of the Plan 01 foundation. A user can sign up, verify their email, log in, log out, reset their password, and have their JWT validated by a middleware on protected routes. Backed by persistent SQLite tables for users, accounts, memberships, auth sessions, and verification tokens.

**Architecture:** Three new internal packages — `internal/tenancy` (User/Account/Membership types + CRUD), `internal/auth` (password hashing, JWT signing/parsing, session CRUD, verification tokens, HTTP middleware), `internal/mail` (Mailer interface + `noop` and `smtp` implementations). HTTP handlers live in `internal/api/auth.go` and are mounted by `main.go`. Each package depends on `internal/db.DB` (not on a concrete driver). Query layer is hand-rolled `database/sql` for Plan 02; sqlc arrives in Plan 08 when Postgres parity requires it.

**Tech Stack:** `golang.org/x/crypto/argon2` (password hashing), `github.com/golang-jwt/jwt/v5` (JWT), `github.com/google/uuid` (UUIDv7), `net/smtp` (stdlib SMTP), existing deps from Plan 01.

**Spec reference:** `docs/superpowers/specs/2026-04-16-agenthub-server-design.md` — §5 (auth / tenancy / mail subsystems), §6 (data model: users, accounts, memberships, auth_sessions, verification_tokens), §7 (Flow A: signup), §9 (logging of auth events).

**What this plan does NOT do (deferred):**
- OAuth login (Google, GitHub) — Plan 03
- API tokens for machine clients — Plan 03
- Rate limiting — Plan 03
- Idempotency keys — Plan 03
- Device pairing, session metadata — Plan 04
- Admin impersonation, audit log UI — Plan 08
- Postgres backend — Plan 09
- `oauth_identities`, `api_tokens`, `idempotency_keys` tables — Plan 03 adds these

---

## File structure added by this plan

```
internal/
├── auth/
│   ├── password.go / password_test.go        # argon2id hashing
│   ├── jwt.go / jwt_test.go                  # JWT sign/parse (HS256)
│   ├── keys.go / keys_test.go                # JWT signing-key bootstrap (app_meta)
│   ├── sessions.go / sessions_test.go        # auth_sessions CRUD + jti revocation
│   ├── tokens.go / tokens_test.go            # verification_tokens CRUD
│   ├── middleware.go / middleware_test.go    # RequireAuth HTTP middleware
│   ├── service.go / service_test.go          # Signup/Verify/Login/Logout/Reset orchestration
│   └── errors.go                             # Typed auth errors (ErrInvalidCredentials, etc.)
├── tenancy/
│   ├── users.go / users_test.go              # User CRUD, email normalization
│   ├── accounts.go / accounts_test.go        # Account CRUD
│   ├── memberships.go / memberships_test.go  # Membership CRUD
│   └── types.go                              # User, Account, Membership, Role
├── mail/
│   ├── mail.go                               # Mailer interface + Message
│   ├── noop.go / noop_test.go                # Dev/solo Mailer (logs + discards)
│   └── smtp.go / smtp_test.go                # SMTP Mailer
└── api/
    ├── auth.go / auth_test.go                # HTTP handlers for /api/auth/*
    └── errors.go                             # Shared JSON error shape + write helper
internal/db/migrations/sqlite/
└── 00002_auth_tables.sql
internal/config/
└── config.go                                 # extended with MailConfig + AuthConfig
test/integration/
└── auth_flow_test.go                         # End-to-end signup → verify → login → reset
```

**Shape rule for this plan:** `auth` and `tenancy` packages expose small, focused types and functions. Each package depends on `db.DB` via the interface from Plan 01 — never on a concrete driver. HTTP layer never talks to the DB directly; it calls into service functions.

---

## Task 1: Migration 00002 — auth & tenancy tables

**Files:**
- Create: `internal/db/migrations/sqlite/00002_auth_tables.sql`

- [ ] **Step 1: Write the migration**

Exact content:
```sql
-- +goose Up

CREATE TABLE accounts (
    id          TEXT PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    plan        TEXT NOT NULL DEFAULT 'self_hosted',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at  TEXT NULL
);

CREATE TABLE users (
    id                 TEXT PRIMARY KEY,
    email              TEXT NOT NULL UNIQUE,          -- lowercased on insert
    password_hash      TEXT NULL,                     -- argon2id encoded, NULL if OAuth-only
    email_verified_at  TEXT NULL,
    name               TEXT NOT NULL DEFAULT '',
    avatar_url         TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at         TEXT NULL
);

CREATE TABLE memberships (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES accounts(id),
    user_id     TEXT NOT NULL REFERENCES users(id),
    role        TEXT NOT NULL CHECK (role IN ('owner','admin','member')),
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(account_id, user_id)
);

CREATE TABLE auth_sessions (
    id          TEXT PRIMARY KEY,           -- JWT jti
    user_id     TEXT NOT NULL REFERENCES users(id),
    account_id  TEXT NOT NULL REFERENCES accounts(id),
    user_agent  TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    issued_at   TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at  TEXT NOT NULL,
    revoked_at  TEXT NULL
);

CREATE INDEX idx_auth_sessions_user_id ON auth_sessions(user_id);

CREATE TABLE verification_tokens (
    id            TEXT PRIMARY KEY,
    purpose       TEXT NOT NULL CHECK (purpose IN ('email_verify','password_reset')),
    user_id       TEXT NULL REFERENCES users(id),
    email         TEXT NOT NULL,               -- carried separately so purpose=email_verify can look up before user exists (not used in plan 02, but future-proofed)
    token_hash    TEXT NOT NULL UNIQUE,        -- sha256(base64url(token))
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at    TEXT NOT NULL,
    consumed_at   TEXT NULL
);

CREATE INDEX idx_verification_tokens_user_purpose ON verification_tokens(user_id, purpose);

-- +goose Down
DROP TABLE verification_tokens;
DROP TABLE auth_sessions;
DROP TABLE memberships;
DROP TABLE users;
DROP TABLE accounts;
```

- [ ] **Step 2: Verify migration applies and rolls back**

```bash
go test ./internal/db/migrations/... -count=1 -v
```

The existing `TestApplySQLite_CreatesAppMeta` and `TestApplySQLite_Idempotent` tests migrate up through ALL migrations — they must still pass with the new file present.

Expected: both existing tests PASS (goose applies 00001 then 00002 silently).

- [ ] **Step 3: Commit**

```bash
git add internal/db/migrations/sqlite/00002_auth_tables.sql
git commit -m "feat(db/migrations): add auth and tenancy tables

accounts, users, memberships, auth_sessions, verification_tokens.
Soft-delete columns on users and accounts per spec §6. Foreign keys
enforced (pragma foreign_keys=ON from sqlite.Open). Unique constraint
on lowercased email; membership unique on (account_id, user_id).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: UUIDv7 generator + google/uuid dep

**Files:**
- Create: `internal/ids/ids.go`
- Create: `internal/ids/ids_test.go`

- [ ] **Step 1: Add dep**

```bash
go get github.com/google/uuid@latest
```

- [ ] **Step 2: Write the failing test**

`internal/ids/ids_test.go`:
```go
package ids

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNew_UniqueAndTimeOrdered(t *testing.T) {
	a := New()
	time.Sleep(2 * time.Millisecond)
	b := New()

	require.NotEqual(t, a, b)
	require.Len(t, a, 36) // standard uuid string length
	require.True(t, a < b, "UUIDv7 ids must be lexicographically time-ordered: %s < %s", a, b)
}
```

- [ ] **Step 3: Run — expect FAIL**

```bash
go test ./internal/ids/...
```

- [ ] **Step 4: Implement**

`internal/ids/ids.go`:
```go
// Package ids generates UUIDv7 identifiers for domain rows.
// UUIDv7 ids are lexicographically time-ordered which makes them
// index-friendly in both SQLite and Postgres.
package ids

import "github.com/google/uuid"

// New returns a new UUIDv7 as its canonical string form.
func New() string {
	u, err := uuid.NewV7()
	if err != nil {
		// NewV7 only fails on crypto/rand failure, which is a fatal system
		// condition. Convert to a panic so we don't return malformed ids.
		panic("ids.New: uuid.NewV7 failed: " + err.Error())
	}
	return u.String()
}
```

- [ ] **Step 5: Run — expect PASS**

```bash
go test ./internal/ids/... && go vet ./... && go test ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/ids go.mod go.sum
git commit -m "feat(ids): UUIDv7 generator for domain rows

Time-ordered ids work identically across SQLite and Postgres and
are index-friendly. Panics on crypto/rand failure (fatal system
condition) rather than returning a bad id.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Argon2id password hashing (TDD)

**Files:**
- Create: `internal/auth/password.go`
- Create: `internal/auth/password_test.go`

- [ ] **Step 1: Write the failing test**

`internal/auth/password_test.go`:
```go
package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashAndVerify_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(hash, "$argon2id$"))

	ok, err := VerifyPassword("correct-horse-battery-staple", hash)
	require.NoError(t, err)
	require.True(t, ok)

	wrong, err := VerifyPassword("trombone", hash)
	require.NoError(t, err)
	require.False(t, wrong)
}

func TestHashPassword_DifferentSaltsProduceDifferentHashes(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	require.NotEqual(t, h1, h2, "argon2id must salt each hash uniquely")
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	_, err := VerifyPassword("any", "not-a-hash")
	require.Error(t, err)
}

func TestHashPassword_Empty(t *testing.T) {
	_, err := HashPassword("")
	require.Error(t, err)
}
```

- [ ] **Step 2: Add dep**

```bash
go get golang.org/x/crypto/argon2@latest
```

- [ ] **Step 3: Run — expect FAIL**

```bash
go test ./internal/auth/... -run Password
```

- [ ] **Step 4: Implement**

`internal/auth/password.go`:
```go
// Package auth implements password hashing, JWT sessions, verification
// tokens, and HTTP auth middleware.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// OWASP 2024 recommendation for argon2id with 64MB memory:
// memory=65536 KiB, iterations=3, parallelism=4.
const (
	argonMemory      uint32 = 64 * 1024
	argonIterations  uint32 = 3
	argonParallelism uint8  = 4
	argonSaltLen     int    = 16
	argonKeyLen      uint32 = 32
)

// HashPassword returns an encoded argon2id hash.
// Format: $argon2id$v=19$m=65536,t=3,p=4$<salt-b64>$<hash-b64>
func HashPassword(pw string) (string, error) {
	if pw == "" {
		return "", errors.New("HashPassword: empty password")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("HashPassword: read rand: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)

	b64 := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key),
	), nil
}

// VerifyPassword returns true iff the supplied password matches the encoded hash.
// Non-nil error means the hash was malformed; the boolean is meaningful only
// when err is nil.
func VerifyPassword(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("VerifyPassword: not an argon2id hash")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("VerifyPassword: version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("VerifyPassword: unexpected argon2 version %d", version)
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, fmt.Errorf("VerifyPassword: params: %w", err)
	}

	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("VerifyPassword: salt: %w", err)
	}
	expected, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("VerifyPassword: hash: %w", err)
	}

	got := argon2.IDKey([]byte(pw), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(got, expected) == 1, nil
}
```

- [ ] **Step 5: Run — expect PASS**

```bash
go test ./internal/auth/... -run Password -v
```

All four tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/auth go.mod go.sum
git commit -m "feat(auth): argon2id password hashing

OWASP 2024 params (m=64MB, t=3, p=4). Salt + encoded output follow
the PHC string format so we can rotate parameters later by reading
them back from the hash. Constant-time comparison for verification.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Mail package — interface + noop (TDD)

**Files:**
- Create: `internal/mail/mail.go`
- Create: `internal/mail/noop.go`
- Create: `internal/mail/noop_test.go`

- [ ] **Step 1: Write the failing test**

`internal/mail/noop_test.go`:
```go
package mail

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoop_Send_LogsAndDiscards(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	m := NewNoop(logger)
	err := m.Send(context.Background(), Message{
		To:      "user@example.com",
		Subject: "hello",
		Text:    "world",
	})
	require.NoError(t, err)
	require.Contains(t, buf.String(), "user@example.com")
	require.Contains(t, buf.String(), "hello")
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
go test ./internal/mail/...
```

- [ ] **Step 3: Write the interface**

`internal/mail/mail.go`:
```go
// Package mail defines the Mailer interface and ships two implementations:
// noop (dev/solo without SMTP) and smtp.
package mail

import "context"

// Message is a single outbound email. HTML body is optional.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer sends transactional email. Implementations:
//   - NewNoop: logs and discards (default for solo mode with mail.provider=noop)
//   - NewSMTP: classic SMTP with AUTH
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}
```

- [ ] **Step 4: Write the noop impl**

`internal/mail/noop.go`:
```go
package mail

import (
	"context"
	"log/slog"
)

type noop struct{ log *slog.Logger }

// NewNoop returns a Mailer that logs the outbound message at INFO and
// returns nil. Useful for self-hosters who don't want to configure SMTP.
func NewNoop(log *slog.Logger) Mailer { return &noop{log: log} }

func (n *noop) Send(_ context.Context, msg Message) error {
	n.log.Info("mail.noop.send",
		"to", msg.To,
		"subject", msg.Subject,
		"text_bytes", len(msg.Text),
		"html_bytes", len(msg.HTML),
	)
	return nil
}
```

- [ ] **Step 5: Run — expect PASS**

```bash
go test ./internal/mail/... -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/mail
git commit -m "feat(mail): Mailer interface and noop implementation

Interface: Send(ctx, Message). Noop logs outbound messages and
returns nil — the default for solo mode when mail.provider=noop.
SMTP implementation lands in the next task.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Mail SMTP implementation (TDD with in-process test server)

**Files:**
- Create: `internal/mail/smtp.go`
- Create: `internal/mail/smtp_test.go`

- [ ] **Step 1: Write the failing test**

`internal/mail/smtp_test.go`:
```go
package mail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSMTPServer accepts one message, parses enough to verify the From/To,
// captures the DATA body, and closes.
type fakeSMTPServer struct {
	addr     string
	received chan fakeMsg
	done     chan struct{}
}

type fakeMsg struct {
	from string
	to   []string
	body string
}

func startFakeSMTP(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &fakeSMTPServer{
		addr:     ln.Addr().String(),
		received: make(chan fakeMsg, 1),
		done:     make(chan struct{}),
	}

	go func() {
		defer close(s.done)
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)

		write := func(s string) { fmt.Fprintf(w, "%s\r\n", s); _ = w.Flush() }

		write("220 fake.smtp")

		var msg fakeMsg
		inData := false
		var dataBuf strings.Builder

		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")

			if inData {
				if line == "." {
					msg.body = dataBuf.String()
					write("250 OK")
					inData = false
					continue
				}
				dataBuf.WriteString(line + "\r\n")
				continue
			}

			switch {
			case strings.HasPrefix(strings.ToUpper(line), "EHLO"), strings.HasPrefix(strings.ToUpper(line), "HELO"):
				write("250-fake.smtp")
				write("250 AUTH PLAIN LOGIN")
			case strings.HasPrefix(strings.ToUpper(line), "AUTH"):
				// accept any credentials
				write("235 auth ok")
			case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
				msg.from = line[len("MAIL FROM:"):]
				write("250 OK")
			case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
				msg.to = append(msg.to, line[len("RCPT TO:"):])
				write("250 OK")
			case strings.HasPrefix(strings.ToUpper(line), "DATA"):
				write("354 go ahead")
				inData = true
			case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
				write("221 bye")
				s.received <- msg
				return
			case strings.HasPrefix(strings.ToUpper(line), "STARTTLS"):
				write("502 not supported")
			default:
				write("250 OK")
			}

			if _, ok := r.(io.Reader); !ok {
				break
			}
		}
	}()

	return s
}

func TestSMTP_Send_DeliversMessage(t *testing.T) {
	srv := startFakeSMTP(t)

	host, port, _ := net.SplitHostPort(srv.addr)

	m := NewSMTP(SMTPConfig{
		Host:     host,
		Port:     portAsInt(t, port),
		Username: "user",
		Password: "pw",
		From:     "AgentHub <noreply@agenthub.app>",
	})

	err := m.Send(context.Background(), Message{
		To:      "rcpt@example.com",
		Subject: "Hello",
		Text:    "Body content.",
	})
	require.NoError(t, err)

	select {
	case msg := <-srv.received:
		require.Contains(t, msg.from, "noreply@agenthub.app")
		require.Contains(t, strings.Join(msg.to, ","), "rcpt@example.com")
		require.Contains(t, msg.body, "Subject: Hello")
		require.Contains(t, msg.body, "Body content.")
	case <-time.After(3 * time.Second):
		t.Fatal("server did not receive message")
	}
}

func portAsInt(t *testing.T, p string) int {
	t.Helper()
	n := 0
	_, err := fmt.Sscanf(p, "%d", &n)
	require.NoError(t, err)
	return n
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
go test ./internal/mail/... -run SMTP
```

- [ ] **Step 3: Implement**

`internal/mail/smtp.go`:
```go
package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPConfig holds connection + auth + envelope info for outbound mail.
type SMTPConfig struct {
	Host      string
	Port      int
	Username  string
	Password  string
	From      string // e.g. "AgentHub <noreply@agenthub.app>"
	UseTLS    bool   // implicit TLS (port 465); false for STARTTLS-or-plain
	TLSConfig *tls.Config
}

type smtpMailer struct{ cfg SMTPConfig }

// NewSMTP returns a Mailer that dials cfg.Host:cfg.Port per Send.
// Connection is NOT pooled — Plan 02's auth volume doesn't require it;
// pooling can be added later without changing the interface.
func NewSMTP(cfg SMTPConfig) Mailer { return &smtpMailer{cfg: cfg} }

func (m *smtpMailer) Send(_ context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	body := buildRFC5322(m.cfg.From, msg)

	if err := smtp.SendMail(addr, auth, m.cfg.From, []string{msg.To}, body); err != nil {
		return fmt.Errorf("smtp.SendMail: %w", err)
	}
	return nil
}

func buildRFC5322(from string, msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + msg.Subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(msg.Text)
	return []byte(b.String())
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
go test ./internal/mail/... -v
```

Both the noop test and the SMTP test pass.

- [ ] **Step 5: Commit**

```bash
git add internal/mail
git commit -m "feat(mail): SMTP Mailer implementation

Uses net/smtp with optional PLAIN AUTH. Messages formatted per
RFC 5322 with MIME text/plain. Connection-per-send is fine for
Plan 02's auth volume; pooling is a later optimization that
doesn't change the interface.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: JWT signing key bootstrap (TDD)

**Files:**
- Create: `internal/auth/keys.go`
- Create: `internal/auth/keys_test.go`

- [ ] **Step 1: Write the failing test**

`internal/auth/keys_test.go`:
```go
package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateJWTKey_FirstCallGenerates(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	k1, err := LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)
	require.Len(t, k1, 32, "JWT HS256 key must be 32 bytes")

	// Second call returns the same key.
	k2, err := LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)
	require.Equal(t, k1, k2)
}
```

- [ ] **Step 2: Run — expect FAIL**

```bash
go test ./internal/auth/... -run JWTKey
```

- [ ] **Step 3: Implement**

`internal/auth/keys.go`:
```go
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
)

const jwtKeyMetaName = "jwt_signing_key_v1"

// LoadOrCreateJWTKey returns the server's HS256 signing key. If the key is
// absent, it is generated from crypto/rand and stored in app_meta. The key
// is 32 bytes (256 bits) — HS256 input.
//
// Rotation in later plans will write a new versioned key (e.g. v2), verify
// against both, and then retire v1. Plan 02 ships only v1.
func LoadOrCreateJWTKey(ctx context.Context, db *sql.DB) ([]byte, error) {
	var encoded string
	row := db.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key = ?", jwtKeyMetaName)
	switch err := row.Scan(&encoded); {
	case err == nil:
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode jwt key: %w", err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("jwt key wrong length: %d", len(b))
		}
		return b, nil
	case errors.Is(err, sql.ErrNoRows):
		// Fall through to create.
	default:
		return nil, fmt.Errorf("lookup jwt key: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate jwt key: %w", err)
	}

	_, err := db.ExecContext(ctx,
		"INSERT INTO app_meta (key, value) VALUES (?, ?)",
		jwtKeyMetaName, base64.StdEncoding.EncodeToString(key),
	)
	if err != nil {
		return nil, fmt.Errorf("store jwt key: %w", err)
	}
	return key, nil
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
go test ./internal/auth/... -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): bootstrap HS256 JWT signing key from app_meta

Generates a 32-byte random key on first boot and stores it in the
app_meta bookkeeping table under key='jwt_signing_key_v1'. Later
plans can rotate by writing v2 and updating verifier to accept both.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: JWT signing & parsing (TDD)

**Files:**
- Create: `internal/auth/jwt.go`
- Create: `internal/auth/jwt_test.go`

- [ ] **Step 1: Add dep**

```bash
go get github.com/golang-jwt/jwt/v5@latest
```

- [ ] **Step 2: Write the failing test**

`internal/auth/jwt_test.go`:
```go
package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignAndParse_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	signer := NewJWTSigner(key, "agenthub-server")

	token, err := signer.Sign(Claims{
		SessionID: "sess-1",
		UserID:    "user-1",
		AccountID: "acct-1",
		TTL:       1 * time.Hour,
	})
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := signer.Parse(token)
	require.NoError(t, err)
	require.Equal(t, "sess-1", claims.SessionID)
	require.Equal(t, "user-1", claims.UserID)
	require.Equal(t, "acct-1", claims.AccountID)
	require.True(t, claims.ExpiresAt.After(time.Now()))
}

func TestParse_Expired(t *testing.T) {
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")
	token, err := signer.Sign(Claims{
		SessionID: "s",
		UserID:    "u",
		AccountID: "a",
		TTL:       -1 * time.Second, // already expired
	})
	require.NoError(t, err)

	_, err = signer.Parse(token)
	require.Error(t, err)
}

func TestParse_WrongKey(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	k2[0] = 1
	signer := NewJWTSigner(k1, "agenthub-server")
	tampered := NewJWTSigner(k2, "agenthub-server")

	tok, err := signer.Sign(Claims{SessionID: "s", UserID: "u", AccountID: "a", TTL: time.Hour})
	require.NoError(t, err)

	_, err = tampered.Parse(tok)
	require.Error(t, err)
}

func TestParse_WrongAlgorithm(t *testing.T) {
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")
	// Handcraft a token with alg=none — must be rejected.
	bad := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1In0."
	_, err := signer.Parse(bad)
	require.Error(t, err)
}
```

- [ ] **Step 3: Run — expect FAIL**

```bash
go test ./internal/auth/... -run JWT -v
```

- [ ] **Step 4: Implement**

`internal/auth/jwt.go`:
```go
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the set of facts we put into every AgentHub JWT.
type Claims struct {
	SessionID string        // token jti; matches auth_sessions.id
	UserID    string        // sub
	AccountID string        // aid
	TTL       time.Duration // time to live (from now) — input to Sign
	ExpiresAt time.Time     // output from Parse
	IssuedAt  time.Time
}

// JWTSigner wraps a symmetric HS256 key + an issuer string.
type JWTSigner struct {
	key    []byte
	issuer string
}

// NewJWTSigner builds a signer for a single HS256 key.
func NewJWTSigner(key []byte, issuer string) *JWTSigner {
	return &JWTSigner{key: key, issuer: issuer}
}

type innerClaims struct {
	AccountID string `json:"aid"`
	jwt.RegisteredClaims
}

// Sign returns a signed JWT string. ExpiresAt on the returned token equals now+TTL.
func (s *JWTSigner) Sign(c Claims) (string, error) {
	if c.SessionID == "" || c.UserID == "" || c.AccountID == "" {
		return "", errors.New("Sign: SessionID, UserID, and AccountID are required")
	}
	if c.TTL <= 0 {
		return "", errors.New("Sign: TTL must be > 0")
	}
	now := time.Now().UTC()
	claims := innerClaims{
		AccountID: c.AccountID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   c.UserID,
			ID:        c.SessionID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(c.TTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.key)
}

// Parse verifies the signature, algorithm, issuer, and expiry, and returns
// the facts. Returns an error on any verification failure.
func (s *JWTSigner) Parse(token string) (Claims, error) {
	var out innerClaims
	_, err := jwt.ParseWithClaims(token, &out, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		return s.key, nil
	},
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil {
		return Claims{}, err
	}
	return Claims{
		SessionID: out.ID,
		UserID:    out.Subject,
		AccountID: out.AccountID,
		ExpiresAt: out.ExpiresAt.Time,
		IssuedAt:  out.IssuedAt.Time,
	}, nil
}
```

- [ ] **Step 5: Run — expect PASS**

```bash
go test ./internal/auth/... -run JWT -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/auth go.mod go.sum
git commit -m "feat(auth): HS256 JWT signer with strict verification

Sign(Claims) produces issuer/sub/aid/jti/iat/exp. Parse enforces
expiration, matching issuer, and HS256-only (algorithm-confusion
and alg=none are rejected). jti maps to auth_sessions.id for
revocation lookup in the middleware layer.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Tenancy types + User/Account/Membership CRUD (TDD)

**Files:**
- Create: `internal/tenancy/types.go`
- Create: `internal/tenancy/users.go`
- Create: `internal/tenancy/users_test.go`
- Create: `internal/tenancy/accounts.go`
- Create: `internal/tenancy/accounts_test.go`
- Create: `internal/tenancy/memberships.go`
- Create: `internal/tenancy/memberships_test.go`

Two sub-tasks in order. Step groups within each are standard TDD.

### Step 1: Write `types.go`

Exact content:
```go
// Package tenancy owns the identity and organization layer: users, accounts,
// and the memberships that connect them.
package tenancy

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type User struct {
	ID              string
	Email           string
	Name            string
	AvatarURL       string
	PasswordHash    string    // may be empty for OAuth-only users
	EmailVerifiedAt time.Time // zero if unverified
	CreatedAt       time.Time
}

type Account struct {
	ID        string
	Slug      string
	Name      string
	Plan      string
	CreatedAt time.Time
}

type Membership struct {
	ID        string
	AccountID string
	UserID    string
	Role      Role
	CreatedAt time.Time
}
```

### Step 2: Write failing user-CRUD test

`internal/tenancy/users_test.go`:
```go
package tenancy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestUsers_CreateAndGet(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	u := User{
		ID:           "u1",
		Email:        "Person@Example.com",
		Name:         "A Person",
		PasswordHash: "argon2id$…",
	}
	require.NoError(t, CreateUser(context.Background(), d.SQL(), u))

	// Email stored lowercased; lookup is case-insensitive.
	got, err := GetUserByEmail(context.Background(), d.SQL(), "PERSON@EXAMPLE.COM")
	require.NoError(t, err)
	require.Equal(t, "u1", got.ID)
	require.Equal(t, "person@example.com", got.Email)

	// Duplicate email rejected.
	err = CreateUser(context.Background(), d.SQL(), User{ID: "u2", Email: "PERSON@EXAMPLE.COM"})
	require.Error(t, err)
}

func TestUsers_MarkVerified(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	require.NoError(t, CreateUser(context.Background(), d.SQL(), User{ID: "u", Email: "a@b.com"}))
	require.NoError(t, MarkEmailVerified(context.Background(), d.SQL(), "u"))

	got, err := GetUserByID(context.Background(), d.SQL(), "u")
	require.NoError(t, err)
	require.False(t, got.EmailVerifiedAt.IsZero())
}
```

### Step 3: Run — expect FAIL

```bash
go test ./internal/tenancy/...
```

### Step 4: Implement `users.go`

```go
package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const sqliteTimeFmt = "2006-01-02 15:04:05"

// CreateUser inserts a new user. Email is lowercased before insert.
func CreateUser(ctx context.Context, db *sql.DB, u User) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, name, avatar_url)
		VALUES (?, ?, ?, ?, ?)`,
		u.ID, strings.ToLower(u.Email), nullIfEmpty(u.PasswordHash), u.Name, u.AvatarURL,
	)
	if err != nil {
		return fmt.Errorf("CreateUser: %w", err)
	}
	return nil
}

// GetUserByEmail looks up by lowercased email.
func GetUserByEmail(ctx context.Context, db *sql.DB, email string) (User, error) {
	return scanUser(db.QueryRowContext(ctx, selectUser+" WHERE email = ? AND deleted_at IS NULL",
		strings.ToLower(email)))
}

// GetUserByID looks up by primary key.
func GetUserByID(ctx context.Context, db *sql.DB, id string) (User, error) {
	return scanUser(db.QueryRowContext(ctx, selectUser+" WHERE id = ? AND deleted_at IS NULL", id))
}

// MarkEmailVerified sets email_verified_at = now() for the given user.
func MarkEmailVerified(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET email_verified_at = datetime('now') WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("MarkEmailVerified: %w", err)
	}
	return nil
}

// UpdatePasswordHash stores a new argon2id-encoded hash.
func UpdatePasswordHash(ctx context.Context, db *sql.DB, id, hash string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ? AND deleted_at IS NULL`, hash, id)
	if err != nil {
		return fmt.Errorf("UpdatePasswordHash: %w", err)
	}
	return nil
}

const selectUser = `SELECT id, email, COALESCE(password_hash, ''), COALESCE(email_verified_at, ''), name, avatar_url, created_at FROM users`

func scanUser(row *sql.Row) (User, error) {
	var u User
	var verifiedAt string
	var createdAt string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &verifiedAt, &u.Name, &u.AvatarURL, &createdAt)
	if err != nil {
		return User{}, err
	}
	if verifiedAt != "" {
		if t, err := time.Parse(sqliteTimeFmt, verifiedAt); err == nil {
			u.EmailVerifiedAt = t
		}
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		u.CreatedAt = t
	}
	return u, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

### Step 5: Write `accounts.go` and `accounts_test.go`

`internal/tenancy/accounts_test.go`:
```go
package tenancy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestAccounts_CreateAndGet(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	a := Account{ID: "a1", Slug: "team-a", Name: "Team A", Plan: "free"}
	require.NoError(t, CreateAccount(context.Background(), d.SQL(), a))

	got, err := GetAccountByID(context.Background(), d.SQL(), "a1")
	require.NoError(t, err)
	require.Equal(t, "team-a", got.Slug)

	err = CreateAccount(context.Background(), d.SQL(), Account{ID: "a2", Slug: "team-a", Name: "dupe slug"})
	require.Error(t, err)
}
```

`internal/tenancy/accounts.go`:
```go
package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateAccount inserts a new account row.
func CreateAccount(ctx context.Context, db *sql.DB, a Account) error {
	plan := a.Plan
	if plan == "" {
		plan = "self_hosted"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO accounts (id, slug, name, plan) VALUES (?, ?, ?, ?)`,
		a.ID, a.Slug, a.Name, plan,
	)
	if err != nil {
		return fmt.Errorf("CreateAccount: %w", err)
	}
	return nil
}

// GetAccountByID looks up by primary key.
func GetAccountByID(ctx context.Context, db *sql.DB, id string) (Account, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, slug, name, plan, created_at FROM accounts WHERE id = ? AND deleted_at IS NULL`, id)
	var a Account
	var createdAt string
	if err := row.Scan(&a.ID, &a.Slug, &a.Name, &a.Plan, &createdAt); err != nil {
		return Account{}, err
	}
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		a.CreatedAt = t
	}
	return a, nil
}
```

### Step 6: Write `memberships.go` and `memberships_test.go`

`internal/tenancy/memberships_test.go`:
```go
package tenancy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMemberships_Add(t *testing.T) {
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	require.NoError(t, CreateUser(context.Background(), d.SQL(), User{ID: "u", Email: "u@x.com"}))
	require.NoError(t, CreateAccount(context.Background(), d.SQL(), Account{ID: "a", Slug: "a", Name: "A"}))

	m := Membership{ID: "m", AccountID: "a", UserID: "u", Role: RoleOwner}
	require.NoError(t, AddMembership(context.Background(), d.SQL(), m))

	// Duplicate (account,user) rejected.
	err = AddMembership(context.Background(), d.SQL(), Membership{ID: "m2", AccountID: "a", UserID: "u", Role: RoleMember})
	require.Error(t, err)

	got, err := GetMembershipByAccountUser(context.Background(), d.SQL(), "a", "u")
	require.NoError(t, err)
	require.Equal(t, RoleOwner, got.Role)
}
```

`internal/tenancy/memberships.go`:
```go
package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AddMembership inserts a membership row.
func AddMembership(ctx context.Context, db *sql.DB, m Membership) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO memberships (id, account_id, user_id, role) VALUES (?, ?, ?, ?)`,
		m.ID, m.AccountID, m.UserID, string(m.Role),
	)
	if err != nil {
		return fmt.Errorf("AddMembership: %w", err)
	}
	return nil
}

// GetMembershipByAccountUser returns the membership for a given account+user pair.
func GetMembershipByAccountUser(ctx context.Context, db *sql.DB, accountID, userID string) (Membership, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, account_id, user_id, role, created_at FROM memberships WHERE account_id = ? AND user_id = ?`,
		accountID, userID)
	var m Membership
	var role string
	var createdAt string
	if err := row.Scan(&m.ID, &m.AccountID, &m.UserID, &role, &createdAt); err != nil {
		return Membership{}, err
	}
	m.Role = Role(role)
	if t, err := time.Parse(sqliteTimeFmt, createdAt); err == nil {
		m.CreatedAt = t
	}
	return m, nil
}
```

### Step 7: Run — all tenancy tests PASS

```bash
go test ./internal/tenancy/... -v && go test ./... && go vet ./...
```

### Step 8: Commit

```bash
git add internal/tenancy
git commit -m "feat(tenancy): User, Account, Membership types + CRUD

Hand-rolled database/sql for SQLite; Postgres support lands with
sqlc in Plan 09. Email is lowercased on insert; lookups are
case-insensitive. Duplicate emails, duplicate account slugs, and
duplicate (account_id, user_id) memberships are all rejected via
the migration's UNIQUE constraints.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Auth sessions (jti revocation) (TDD)

**Files:**
- Create: `internal/auth/sessions.go`
- Create: `internal/auth/sessions_test.go`
- Create: `internal/auth/errors.go`

- [ ] **Step 1: Write `errors.go`**

```go
package auth

import "errors"

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrEmailNotVerified   = errors.New("auth: email not verified")
	ErrSessionRevoked     = errors.New("auth: session revoked")
	ErrSessionExpired     = errors.New("auth: session expired")
	ErrTokenExpired       = errors.New("auth: verification token expired or consumed")
	ErrTokenNotFound      = errors.New("auth: verification token not found")
)
```

- [ ] **Step 2: Write the failing test**

`internal/auth/sessions_test.go`:
```go
package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/stretchr/testify/require"
)

func withTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))
	// Seed one user + one account (FKs).
	_, err = d.SQL().ExecContext(context.Background(),
		`INSERT INTO users (id, email) VALUES ('u1', 'a@b.com')`)
	require.NoError(t, err)
	_, err = d.SQL().ExecContext(context.Background(),
		`INSERT INTO accounts (id, slug, name) VALUES ('acct1', 'a', 'A')`)
	require.NoError(t, err)
	return d.SQL()
}

func TestCreateSession_AndCheck(t *testing.T) {
	db := withTestDB(t)

	sess, err := CreateSession(context.Background(), db, SessionInput{
		ID:        "sess1",
		UserID:    "u1",
		AccountID: "acct1",
		UserAgent: "ua",
		IP:        "1.2.3.4",
		TTL:       time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, "sess1", sess.ID)

	require.NoError(t, CheckSessionActive(context.Background(), db, "sess1"))
}

func TestRevokeSession(t *testing.T) {
	db := withTestDB(t)

	_, err := CreateSession(context.Background(), db, SessionInput{
		ID:        "sess1",
		UserID:    "u1",
		AccountID: "acct1",
		TTL:       time.Hour,
	})
	require.NoError(t, err)

	require.NoError(t, RevokeSession(context.Background(), db, "sess1"))

	err = CheckSessionActive(context.Background(), db, "sess1")
	require.ErrorIs(t, err, ErrSessionRevoked)
}

func TestCheckSession_Expired(t *testing.T) {
	db := withTestDB(t)

	// Insert an already-expired session directly.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO auth_sessions (id, user_id, account_id, expires_at) VALUES ('s', 'u1', 'acct1', datetime('now','-1 hour'))`)
	require.NoError(t, err)

	err = CheckSessionActive(context.Background(), db, "s")
	require.ErrorIs(t, err, ErrSessionExpired)
}
```

- [ ] **Step 3: Implement `sessions.go`**

```go
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SessionInput struct {
	ID        string // matches JWT jti
	UserID    string
	AccountID string
	UserAgent string
	IP        string
	TTL       time.Duration
}

type Session struct {
	ID        string
	UserID    string
	AccountID string
	ExpiresAt time.Time
}

// CreateSession inserts a new auth_sessions row with expires_at = now+TTL.
func CreateSession(ctx context.Context, db *sql.DB, in SessionInput) (Session, error) {
	if in.TTL <= 0 {
		return Session{}, errors.New("CreateSession: TTL must be > 0")
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO auth_sessions (id, user_id, account_id, user_agent, ip, expires_at)
		VALUES (?, ?, ?, ?, ?, datetime('now', ?))`,
		in.ID, in.UserID, in.AccountID, in.UserAgent, in.IP,
		fmt.Sprintf("+%d seconds", int(in.TTL.Seconds())),
	)
	if err != nil {
		return Session{}, fmt.Errorf("CreateSession: %w", err)
	}
	return Session{
		ID:        in.ID,
		UserID:    in.UserID,
		AccountID: in.AccountID,
		ExpiresAt: time.Now().Add(in.TTL),
	}, nil
}

// CheckSessionActive returns nil if the session exists, is not revoked, and
// has not expired. Returns typed errors otherwise.
func CheckSessionActive(ctx context.Context, db *sql.DB, id string) error {
	var expiresAt string
	var revokedAt sql.NullString
	row := db.QueryRowContext(ctx,
		`SELECT expires_at, revoked_at FROM auth_sessions WHERE id = ?`, id)
	err := row.Scan(&expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionRevoked // treat unknown jti like revoked
	}
	if err != nil {
		return fmt.Errorf("CheckSessionActive: %w", err)
	}
	if revokedAt.Valid {
		return ErrSessionRevoked
	}
	t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
	if err != nil {
		return fmt.Errorf("CheckSessionActive: parse expires_at: %w", err)
	}
	if time.Now().After(t) {
		return ErrSessionExpired
	}
	return nil
}

// RevokeSession marks the session revoked immediately. Idempotent.
func RevokeSession(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE auth_sessions SET revoked_at = datetime('now') WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("RevokeSession: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run — expect PASS**

```bash
go test ./internal/auth/... -v && go test ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): auth_sessions CRUD with revocation and expiry checks

Session id is the JWT jti. CheckSessionActive is the revocation
gate the middleware will call on every protected request.
Unknown jti is treated as revoked (fail-closed).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Verification tokens (email_verify, password_reset) (TDD)

**Files:**
- Create: `internal/auth/tokens.go`
- Create: `internal/auth/tokens_test.go`

- [ ] **Step 1: Write the failing test**

`internal/auth/tokens_test.go`:
```go
package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVerificationToken_CreateAndConsume(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateVerificationToken(context.Background(), db, VerificationTokenInput{
		ID:      "tok1",
		Purpose: PurposeEmailVerify,
		UserID:  "u1",
		Email:   "a@b.com",
		TTL:     24 * time.Hour,
	})
	require.NoError(t, err)
	require.NotEmpty(t, raw, "caller gets the raw token; DB stores only the hash")

	got, err := ConsumeVerificationToken(context.Background(), db, raw, PurposeEmailVerify)
	require.NoError(t, err)
	require.Equal(t, "u1", got.UserID)

	// Second consume fails.
	_, err = ConsumeVerificationToken(context.Background(), db, raw, PurposeEmailVerify)
	require.ErrorIs(t, err, ErrTokenNotFound)
}

func TestVerificationToken_Expired(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateVerificationToken(context.Background(), db, VerificationTokenInput{
		ID:      "t2",
		Purpose: PurposePasswordReset,
		UserID:  "u1",
		Email:   "a@b.com",
		TTL:     -1 * time.Second, // already expired
	})
	require.NoError(t, err)

	_, err = ConsumeVerificationToken(context.Background(), db, raw, PurposePasswordReset)
	require.ErrorIs(t, err, ErrTokenExpired)
}

func TestVerificationToken_WrongPurpose(t *testing.T) {
	db := withTestDB(t)

	raw, err := CreateVerificationToken(context.Background(), db, VerificationTokenInput{
		ID: "t3", Purpose: PurposeEmailVerify, UserID: "u1", Email: "a@b.com", TTL: time.Hour,
	})
	require.NoError(t, err)

	_, err = ConsumeVerificationToken(context.Background(), db, raw, PurposePasswordReset)
	require.ErrorIs(t, err, ErrTokenNotFound)
}
```

- [ ] **Step 2: Implement `tokens.go`**

```go
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

type Purpose string

const (
	PurposeEmailVerify   Purpose = "email_verify"
	PurposePasswordReset Purpose = "password_reset"
)

type VerificationTokenInput struct {
	ID      string
	Purpose Purpose
	UserID  string
	Email   string
	TTL     time.Duration
}

type VerificationToken struct {
	ID      string
	Purpose Purpose
	UserID  string
	Email   string
}

// CreateVerificationToken generates a 32-byte random token, stores sha256(token)
// in DB, and returns the raw token to the caller for delivery via email.
// The raw token is never persisted.
func CreateVerificationToken(ctx context.Context, db *sql.DB, in VerificationTokenInput) (string, error) {
	if in.ID == "" || in.Email == "" {
		return "", errors.New("CreateVerificationToken: ID and Email required")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	rawStr := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256Hex(rawStr)

	var userID any
	if in.UserID == "" {
		userID = nil
	} else {
		userID = in.UserID
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO verification_tokens (id, purpose, user_id, email, token_hash, expires_at)
		VALUES (?, ?, ?, ?, ?, datetime('now', ?))`,
		in.ID, string(in.Purpose), userID, in.Email, hash,
		fmt.Sprintf("+%d seconds", int(in.TTL.Seconds())),
	)
	if err != nil {
		return "", fmt.Errorf("CreateVerificationToken: %w", err)
	}
	return rawStr, nil
}

// ConsumeVerificationToken atomically verifies and marks the token consumed,
// returning the token row. Returns ErrTokenNotFound for mismatched
// hash/purpose, and ErrTokenExpired for an expired token.
func ConsumeVerificationToken(ctx context.Context, db *sql.DB, raw string, purpose Purpose) (VerificationToken, error) {
	hash := sha256Hex(raw)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return VerificationToken{}, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT id, purpose, COALESCE(user_id,''), email, expires_at, consumed_at
		FROM verification_tokens
		WHERE token_hash = ? AND purpose = ?`,
		hash, string(purpose))

	var tok VerificationToken
	var tokPurpose string
	var expiresAt string
	var consumedAt sql.NullString
	err = row.Scan(&tok.ID, &tokPurpose, &tok.UserID, &tok.Email, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return VerificationToken{}, ErrTokenNotFound
	}
	if err != nil {
		return VerificationToken{}, fmt.Errorf("ConsumeVerificationToken lookup: %w", err)
	}
	if consumedAt.Valid {
		return VerificationToken{}, ErrTokenNotFound
	}

	t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
	if err != nil {
		return VerificationToken{}, fmt.Errorf("parse expires_at: %w", err)
	}
	if time.Now().After(t) {
		return VerificationToken{}, ErrTokenExpired
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE verification_tokens SET consumed_at = datetime('now') WHERE id = ?`, tok.ID,
	); err != nil {
		return VerificationToken{}, fmt.Errorf("mark consumed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return VerificationToken{}, fmt.Errorf("commit: %w", err)
	}

	tok.Purpose = Purpose(tokPurpose)
	return tok, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/auth/... -v && go test ./...
```

All pass.

- [ ] **Step 4: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): verification tokens (email_verify + password_reset)

CreateVerificationToken returns a raw token to the caller; only the
sha256 of the raw token lives in the DB. ConsumeVerificationToken
runs atomically under a tx so double-consume races produce
ErrTokenNotFound deterministically.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Auth middleware (TDD)

**Files:**
- Create: `internal/auth/middleware.go`
- Create: `internal/auth/middleware_test.go`

- [ ] **Step 1: Write the failing test**

`internal/auth/middleware_test.go`:
```go
package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedActiveSession(t *testing.T, db *sql.DB, jti string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO auth_sessions (id, user_id, account_id, expires_at)
		 VALUES (?, 'u1', 'acct1', datetime('now', '+1 hour'))`, jti)
	require.NoError(t, err)
}

func TestRequireAuth_ValidToken(t *testing.T) {
	db := withTestDB(t)
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")

	seedActiveSession(t, db, "jti1")
	tok, err := signer.Sign(Claims{SessionID: "jti1", UserID: "u1", AccountID: "acct1", TTL: time.Hour})
	require.NoError(t, err)

	gotCtx := make(chan context.Context, 1)
	h := RequireAuth(signer, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCtx <- r.Context()
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, 200, rr.Code)
	ctx := <-gotCtx
	require.Equal(t, "u1", UserID(ctx))
	require.Equal(t, "acct1", AccountID(ctx))
	require.Equal(t, "jti1", SessionID(ctx))
}

func TestRequireAuth_MissingHeader(t *testing.T) {
	db := withTestDB(t)
	signer := NewJWTSigner(make([]byte, 32), "agenthub-server")

	h := RequireAuth(signer, db)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequireAuth_RevokedSession(t *testing.T) {
	db := withTestDB(t)
	key := make([]byte, 32)
	signer := NewJWTSigner(key, "agenthub-server")

	seedActiveSession(t, db, "jti2")
	require.NoError(t, RevokeSession(context.Background(), db, "jti2"))

	tok, _ := signer.Sign(Claims{SessionID: "jti2", UserID: "u1", AccountID: "acct1", TTL: time.Hour})
	h := RequireAuth(signer, db)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
```

- [ ] **Step 2: Implement `middleware.go`**

```go
package auth

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxAccountID
	ctxSessionID
)

// RequireAuth is HTTP middleware that validates a Bearer JWT, checks that the
// jti maps to an un-revoked un-expired auth_sessions row, and injects the
// user/account/session ids into the request context on success. On any
// failure it writes 401 Unauthorized.
func RequireAuth(signer *JWTSigner, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := bearerToken(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			claims, err := signer.Parse(tok)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if err := CheckSessionActive(r.Context(), db, claims.SessionID); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
			ctx = context.WithValue(ctx, ctxAccountID, claims.AccountID)
			ctx = context.WithValue(ctx, ctxSessionID, claims.SessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserID returns the authenticated user id from the request context.
// Empty string if the request didn't pass RequireAuth.
func UserID(ctx context.Context) string     { return ctxString(ctx, ctxUserID) }
func AccountID(ctx context.Context) string  { return ctxString(ctx, ctxAccountID) }
func SessionID(ctx context.Context) string  { return ctxString(ctx, ctxSessionID) }

func ctxString(ctx context.Context, k ctxKey) string {
	if v, ok := ctx.Value(k).(string); ok {
		return v
	}
	return ""
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}
```

- [ ] **Step 3: Run — expect PASS**

```bash
go test ./internal/auth/... -v
```

- [ ] **Step 4: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): RequireAuth middleware with jti revocation check

Bearer JWT is parsed and verified; jti is then checked against
auth_sessions for revocation or expiry. On success, user/account/
session ids flow through request context via typed accessors.
Any failure returns 401 with a neutral body — no error-shape leak.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: Auth service orchestrators (TDD)

The service layer coordinates tenancy + auth + mail for each flow. No HTTP yet.

**Files:**
- Create: `internal/auth/service.go`
- Create: `internal/auth/service_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/auth/service_test.go`:
```go
package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/scottkw/agenthub-server/internal/tenancy"
	"github.com/stretchr/testify/require"
)

type capturingMailer struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (c *capturingMailer) Send(_ context.Context, m mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func newServiceStack(t *testing.T) (*Service, *sql.DB, *capturingMailer) {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	key, err := LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)

	mailer := &capturingMailer{}
	svc := NewService(Config{
		DB:     d.SQL(),
		Signer: NewJWTSigner(key, "agenthub-server"),
		Mailer: mailer,
		Log:    slog.Default(),
		TTL:    Lifetimes{Session: time.Hour, EmailVerify: 24 * time.Hour, PasswordReset: time.Hour},
		From:   "AgentHub <no-reply@test>",
		VerifyURLPrefix: "http://localhost/verify",
		ResetURLPrefix:  "http://localhost/reset",
	})
	return svc, d.SQL(), mailer
}

func TestSignup_CreatesUserAccountMembershipAndSendsVerifyEmail(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	out, err := svc.Signup(context.Background(), SignupInput{
		Email:       "Alice@Example.com",
		Password:    "correct-horse-battery-staple",
		AccountName: "Alice's Team",
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.UserID)
	require.NotEmpty(t, out.AccountID)

	// Verify DB rows.
	u, err := tenancy.GetUserByID(context.Background(), db, out.UserID)
	require.NoError(t, err)
	require.Equal(t, "alice@example.com", u.Email)
	require.True(t, strings.HasPrefix(u.PasswordHash, "$argon2id$"))

	_, err = tenancy.GetAccountByID(context.Background(), db, out.AccountID)
	require.NoError(t, err)

	m, err := tenancy.GetMembershipByAccountUser(context.Background(), db, out.AccountID, out.UserID)
	require.NoError(t, err)
	require.Equal(t, tenancy.RoleOwner, m.Role)

	// Verify email sent.
	require.Len(t, mailer.msgs, 1)
	require.Contains(t, mailer.msgs[0].Text, "http://localhost/verify?token=")
}

func TestSignup_DuplicateEmail(t *testing.T) {
	svc, _, _ := newServiceStack(t)
	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "pwpwpwpw", AccountName: "x"})
	require.NoError(t, err)
	_, err = svc.Signup(context.Background(), SignupInput{Email: "A@B.COM", Password: "pwpwpwpw", AccountName: "y"})
	require.Error(t, err)
}

func TestVerifyEmail_MarksUserVerified(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	out, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "pwpwpwpw", AccountName: "x"})
	require.NoError(t, err)

	// Extract the raw token from the mail body.
	body := mailer.msgs[0].Text
	idx := strings.Index(body, "token=")
	require.NotEqual(t, -1, idx)
	raw := strings.TrimSpace(body[idx+len("token="):])

	require.NoError(t, svc.VerifyEmail(context.Background(), raw))

	u, err := tenancy.GetUserByID(context.Background(), db, out.UserID)
	require.NoError(t, err)
	require.False(t, u.EmailVerifiedAt.IsZero())
}

func TestLogin_SuccessAndWrongPassword(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	out, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "correctpassword", AccountName: "x"})
	require.NoError(t, err)

	// Before verification, login should fail with ErrEmailNotVerified.
	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "correctpassword"})
	require.ErrorIs(t, err, ErrEmailNotVerified)

	// Verify.
	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	require.NoError(t, svc.VerifyEmail(context.Background(), raw))

	// Right password.
	tok, err := svc.Login(context.Background(), LoginInput{Email: "A@B.com", Password: "correctpassword"})
	require.NoError(t, err)
	require.NotEmpty(t, tok.Token)

	// Session row exists and is active.
	require.NoError(t, CheckSessionActive(context.Background(), db, tok.SessionID))
	_ = out // silence unused

	// Wrong password.
	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "wrong"})
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestLogout_RevokesSession(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "pwpwpwpw", AccountName: "x"})
	require.NoError(t, err)
	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	require.NoError(t, svc.VerifyEmail(context.Background(), raw))

	tok, err := svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "pwpwpwpw"})
	require.NoError(t, err)

	require.NoError(t, svc.Logout(context.Background(), tok.SessionID))
	err = CheckSessionActive(context.Background(), db, tok.SessionID)
	require.ErrorIs(t, err, ErrSessionRevoked)
}

func TestPasswordResetFlow(t *testing.T) {
	svc, _, mailer := newServiceStack(t)

	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "oldpassword", AccountName: "x"})
	require.NoError(t, err)
	// Consume verify token.
	verifyBody := mailer.msgs[0].Text
	rawVerify := strings.TrimSpace(verifyBody[strings.Index(verifyBody, "token=")+len("token="):])
	require.NoError(t, svc.VerifyEmail(context.Background(), rawVerify))

	// Request reset.
	require.NoError(t, svc.RequestPasswordReset(context.Background(), "a@b.com"))
	require.Len(t, mailer.msgs, 2)
	resetBody := mailer.msgs[1].Text
	rawReset := strings.TrimSpace(resetBody[strings.Index(resetBody, "token=")+len("token="):])

	// Perform reset.
	require.NoError(t, svc.ResetPassword(context.Background(), rawReset, "newpassword"))

	// Old password fails.
	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "oldpassword"})
	require.ErrorIs(t, err, ErrInvalidCredentials)

	// New password works.
	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "newpassword"})
	require.NoError(t, err)
}

func TestRequestPasswordReset_UnknownEmail_NoError(t *testing.T) {
	// Avoid user enumeration: unknown email silently succeeds.
	svc, _, mailer := newServiceStack(t)
	err := svc.RequestPasswordReset(context.Background(), "nobody@example.com")
	require.NoError(t, err)
	// No email sent.
	require.Len(t, mailer.msgs, 0)
}

// sentinel to avoid unused-import complaints in the skeleton.
var _ = errors.New
var _ = ids.New
```

- [ ] **Step 2: Implement `service.go`**

```go
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/scottkw/agenthub-server/internal/tenancy"
)

type Lifetimes struct {
	Session       time.Duration
	EmailVerify   time.Duration
	PasswordReset time.Duration
}

type Config struct {
	DB              *sql.DB
	Signer          *JWTSigner
	Mailer          mail.Mailer
	Log             *slog.Logger
	TTL             Lifetimes
	From            string // email From header
	VerifyURLPrefix string // e.g. https://agenthub.app/api/auth/verify
	ResetURLPrefix  string // e.g. https://agenthub.app/api/auth/reset
}

type Service struct{ cfg Config }

func NewService(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{cfg: cfg}
}

type SignupInput struct {
	Email       string
	Password    string
	AccountName string
}
type SignupOutput struct {
	UserID    string
	AccountID string
}

func (s *Service) Signup(ctx context.Context, in SignupInput) (SignupOutput, error) {
	if in.Email == "" || in.Password == "" {
		return SignupOutput{}, fmt.Errorf("Signup: email and password required")
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return SignupOutput{}, err
	}

	userID := ids.New()
	accountID := ids.New()
	membershipID := ids.New()
	tokenID := ids.New()
	slug := slugify(in.AccountName, accountID)

	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return SignupOutput{}, err
	}
	defer tx.Rollback()

	if err := tenancyCreateUserTx(ctx, tx, tenancy.User{
		ID:           userID,
		Email:        in.Email,
		PasswordHash: hash,
	}); err != nil {
		return SignupOutput{}, err
	}
	if err := tenancyCreateAccountTx(ctx, tx, tenancy.Account{
		ID:   accountID,
		Slug: slug,
		Name: in.AccountName,
		Plan: "self_hosted",
	}); err != nil {
		return SignupOutput{}, err
	}
	if err := tenancyAddMembershipTx(ctx, tx, tenancy.Membership{
		ID:        membershipID,
		AccountID: accountID,
		UserID:    userID,
		Role:      tenancy.RoleOwner,
	}); err != nil {
		return SignupOutput{}, err
	}
	if err := tx.Commit(); err != nil {
		return SignupOutput{}, err
	}

	raw, err := CreateVerificationToken(ctx, s.cfg.DB, VerificationTokenInput{
		ID:      tokenID,
		Purpose: PurposeEmailVerify,
		UserID:  userID,
		Email:   strings.ToLower(in.Email),
		TTL:     s.cfg.TTL.EmailVerify,
	})
	if err != nil {
		return SignupOutput{}, err
	}

	verifyURL := s.cfg.VerifyURLPrefix + "?token=" + url.QueryEscape(raw)
	if err := s.cfg.Mailer.Send(ctx, mail.Message{
		To:      strings.ToLower(in.Email),
		Subject: "Verify your AgentHub email",
		Text:    "Click to verify your email: " + verifyURL,
	}); err != nil {
		// Email failure doesn't abort signup — user can request resend later.
		s.cfg.Log.Warn("signup.mail.send_failed", "error", err)
	}

	return SignupOutput{UserID: userID, AccountID: accountID}, nil
}

func (s *Service) VerifyEmail(ctx context.Context, rawToken string) error {
	tok, err := ConsumeVerificationToken(ctx, s.cfg.DB, rawToken, PurposeEmailVerify)
	if err != nil {
		return err
	}
	return tenancy.MarkEmailVerified(ctx, s.cfg.DB, tok.UserID)
}

type LoginInput struct {
	Email    string
	Password string
	UserAgent string
	IP        string
}
type LoginOutput struct {
	Token     string
	SessionID string
	ExpiresAt time.Time
	UserID    string
	AccountID string
}

func (s *Service) Login(ctx context.Context, in LoginInput) (LoginOutput, error) {
	u, err := tenancy.GetUserByEmail(ctx, s.cfg.DB, in.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginOutput{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginOutput{}, err
	}
	if u.PasswordHash == "" {
		return LoginOutput{}, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(in.Password, u.PasswordHash)
	if err != nil || !ok {
		return LoginOutput{}, ErrInvalidCredentials
	}
	if u.EmailVerifiedAt.IsZero() {
		return LoginOutput{}, ErrEmailNotVerified
	}

	// Find the user's primary account (first membership).
	row := s.cfg.DB.QueryRowContext(ctx,
		`SELECT account_id FROM memberships WHERE user_id = ? ORDER BY created_at LIMIT 1`, u.ID)
	var accountID string
	if err := row.Scan(&accountID); err != nil {
		return LoginOutput{}, fmt.Errorf("Login: resolve account: %w", err)
	}

	sessionID := ids.New()
	if _, err := CreateSession(ctx, s.cfg.DB, SessionInput{
		ID:        sessionID,
		UserID:    u.ID,
		AccountID: accountID,
		UserAgent: in.UserAgent,
		IP:        in.IP,
		TTL:       s.cfg.TTL.Session,
	}); err != nil {
		return LoginOutput{}, err
	}

	tok, err := s.cfg.Signer.Sign(Claims{
		SessionID: sessionID,
		UserID:    u.ID,
		AccountID: accountID,
		TTL:       s.cfg.TTL.Session,
	})
	if err != nil {
		return LoginOutput{}, err
	}

	return LoginOutput{
		Token:     tok,
		SessionID: sessionID,
		ExpiresAt: time.Now().Add(s.cfg.TTL.Session),
		UserID:    u.ID,
		AccountID: accountID,
	}, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return RevokeSession(ctx, s.cfg.DB, sessionID)
}

func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	u, err := tenancy.GetUserByEmail(ctx, s.cfg.DB, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // avoid user-enumeration
	}
	if err != nil {
		return err
	}

	raw, err := CreateVerificationToken(ctx, s.cfg.DB, VerificationTokenInput{
		ID:      ids.New(),
		Purpose: PurposePasswordReset,
		UserID:  u.ID,
		Email:   u.Email,
		TTL:     s.cfg.TTL.PasswordReset,
	})
	if err != nil {
		return err
	}
	resetURL := s.cfg.ResetURLPrefix + "?token=" + url.QueryEscape(raw)
	if err := s.cfg.Mailer.Send(ctx, mail.Message{
		To:      u.Email,
		Subject: "Reset your AgentHub password",
		Text:    "Click to reset: " + resetURL,
	}); err != nil {
		s.cfg.Log.Warn("reset.mail.send_failed", "error", err)
	}
	return nil
}

func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	if newPassword == "" {
		return errors.New("ResetPassword: new password required")
	}
	tok, err := ConsumeVerificationToken(ctx, s.cfg.DB, rawToken, PurposePasswordReset)
	if err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return tenancy.UpdatePasswordHash(ctx, s.cfg.DB, tok.UserID, hash)
}

// --- Tx helpers: mirror tenancy's Create* but take a *sql.Tx so Signup can
// commit atomically. Keeping them unexported here avoids a cross-package
// hack; duplication is small and localized. ---

func tenancyCreateUserTx(ctx context.Context, tx *sql.Tx, u tenancy.User) error {
	var hash any
	if u.PasswordHash == "" {
		hash = nil
	} else {
		hash = u.PasswordHash
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, name, avatar_url) VALUES (?, ?, ?, ?, ?)`,
		u.ID, strings.ToLower(u.Email), hash, u.Name, u.AvatarURL,
	)
	if err != nil {
		return fmt.Errorf("createUserTx: %w", err)
	}
	return nil
}

func tenancyCreateAccountTx(ctx context.Context, tx *sql.Tx, a tenancy.Account) error {
	plan := a.Plan
	if plan == "" {
		plan = "self_hosted"
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (id, slug, name, plan) VALUES (?, ?, ?, ?)`,
		a.ID, a.Slug, a.Name, plan,
	)
	if err != nil {
		return fmt.Errorf("createAccountTx: %w", err)
	}
	return nil
}

func tenancyAddMembershipTx(ctx context.Context, tx *sql.Tx, m tenancy.Membership) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (id, account_id, user_id, role) VALUES (?, ?, ?, ?)`,
		m.ID, m.AccountID, m.UserID, string(m.Role),
	)
	if err != nil {
		return fmt.Errorf("addMembershipTx: %w", err)
	}
	return nil
}

// slugify makes a URL-safe account slug from the account name; falls back to
// the account id prefix if the name is empty or produces an empty slug.
func slugify(name, accountID string) string {
	var b strings.Builder
	prev := '-'
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prev = r
		case r == ' ' || r == '-' || r == '_':
			if prev != '-' {
				b.WriteRune('-')
				prev = '-'
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" && len(accountID) >= 8 {
		slug = "acct-" + accountID[:8]
	}
	// Append a suffix of the account id to avoid slug collisions across users.
	if len(accountID) >= 6 {
		slug = slug + "-" + accountID[:6]
	}
	return slug
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/auth/... -v -run Service
go test ./internal/auth/... -v
go test ./...
```

All auth tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/auth
git commit -m "feat(auth): service orchestrators for signup/verify/login/logout/reset

Signup is transactional across users + accounts + memberships
and sends a verification email via the injected Mailer. Login
issues a fresh auth_sessions row + JWT whose jti references it.
Password reset and verify share the tokens subsystem and are
user-enumeration-safe.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: Shared JSON error helper (inline, small)

**Files:**
- Create: `internal/api/errors.go`

Exact content:
```go
package api

import (
	"encoding/json"
	"net/http"
)

// WriteError writes a uniform JSON error response. Callers set the status
// code and a stable machine-readable `code` string; `message` is for humans.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// WriteJSON writes a JSON body with the given status.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

Commit:
```bash
git add internal/api/errors.go
git commit -m "feat(api): shared WriteError and WriteJSON helpers

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 14: Auth HTTP handlers (TDD)

**Files:**
- Create: `internal/api/auth.go`
- Create: `internal/api/auth_test.go`

- [ ] **Step 1: Write the failing test**

`internal/api/auth_test.go`:
```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/stretchr/testify/require"
)

type stubMailer struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (s *stubMailer) Send(_ context.Context, m mail.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return nil
}

func newRouterWithAuth(t *testing.T) (*chi.Mux, *stubMailer) {
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
		From:            "AgentHub <test@test>",
		VerifyURLPrefix: "http://t/verify",
		ResetURLPrefix:  "http://t/reset",
	})

	r := chi.NewRouter()
	r.Mount("/api/auth", AuthRoutes(svc))
	return r, mailer
}

func doJSON(t *testing.T, r http.Handler, method, path string, body any, headers ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	var bs []byte
	if body != nil {
		var err error
		bs, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(bs))
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestAuthRoutes_SignupVerifyLoginLogout(t *testing.T) {
	r, mailer := newRouterWithAuth(t)

	// Signup.
	rr := doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email":        "a@b.com",
		"password":     "password1",
		"account_name": "Team",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Extract verify token.
	require.Len(t, mailer.msgs, 1)
	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])

	// Verify.
	rr = doJSON(t, r, "POST", "/api/auth/verify", map[string]string{"token": raw})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Login.
	rr = doJSON(t, r, "POST", "/api/auth/login", map[string]string{
		"email":    "a@b.com",
		"password": "password1",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var loginResp struct{ Token string `json:"token"` }
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &loginResp))
	require.NotEmpty(t, loginResp.Token)

	// Logout.
	rr = doJSON(t, r, "POST", "/api/auth/logout", nil,
		[2]string{"Authorization", "Bearer " + loginResp.Token})
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
}

func TestAuthRoutes_LoginWrongPassword(t *testing.T) {
	r, mailer := newRouterWithAuth(t)
	_ = mailer
	_ = doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email": "a@b.com", "password": "password1", "account_name": "T",
	})
	rr := doJSON(t, r, "POST", "/api/auth/login", map[string]string{"email": "a@b.com", "password": "wrong"})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
```

- [ ] **Step 2: Implement `auth.go`**

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
)

// AuthRoutes returns a chi router mounting the /api/auth/* endpoints.
func AuthRoutes(svc *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Post("/signup", signupHandler(svc))
	r.Post("/verify", verifyHandler(svc))
	r.Post("/login", loginHandler(svc))
	r.Post("/reset-request", resetRequestHandler(svc))
	r.Post("/reset", resetHandler(svc))
	// Logout requires auth — mount under its own sub-router with middleware.
	r.Group(func(sub chi.Router) {
		sub.Use(auth.RequireAuthFromService(svc))
		sub.Post("/logout", logoutHandler(svc))
	})
	return r
}

type signupReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	AccountName string `json:"account_name"`
}

func signupHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in signupReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		out, err := svc.Signup(r.Context(), auth.SignupInput{
			Email: in.Email, Password: in.Password, AccountName: in.AccountName,
		})
		if err != nil {
			WriteError(w, http.StatusBadRequest, "signup_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{
			"user_id":    out.UserID,
			"account_id": out.AccountID,
		})
	}
}

type verifyReq struct {
	Token string `json:"token"`
}

func verifyHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in verifyReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := svc.VerifyEmail(r.Context(), in.Token); err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenNotFound), errors.Is(err, auth.ErrTokenExpired):
				WriteError(w, http.StatusBadRequest, "verify_failed", "token invalid or expired")
			default:
				WriteError(w, http.StatusInternalServerError, "verify_failed", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "verified"})
	}
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func loginHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in loginReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		out, err := svc.Login(r.Context(), auth.LoginInput{
			Email: in.Email, Password: in.Password,
			UserAgent: r.UserAgent(), IP: r.RemoteAddr,
		})
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrInvalidCredentials):
				WriteError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
			case errors.Is(err, auth.ErrEmailNotVerified):
				WriteError(w, http.StatusForbidden, "email_not_verified", "please verify your email first")
			default:
				WriteError(w, http.StatusInternalServerError, "login_failed", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"token":      out.Token,
			"session_id": out.SessionID,
			"user_id":    out.UserID,
			"account_id": out.AccountID,
			"expires_at": out.ExpiresAt,
		})
	}
}

func logoutHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := auth.SessionID(r.Context())
		if sessionID == "" {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "no session")
			return
		}
		if err := svc.Logout(r.Context(), sessionID); err != nil {
			WriteError(w, http.StatusInternalServerError, "logout_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type resetRequestReq struct {
	Email string `json:"email"`
}

func resetRequestHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in resetRequestReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := svc.RequestPasswordReset(r.Context(), in.Email); err != nil {
			WriteError(w, http.StatusInternalServerError, "reset_request_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

type resetReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func resetHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in resetReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := svc.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenNotFound), errors.Is(err, auth.ErrTokenExpired):
				WriteError(w, http.StatusBadRequest, "reset_failed", "token invalid or expired")
			default:
				WriteError(w, http.StatusInternalServerError, "reset_failed", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
```

- [ ] **Step 3: Add `RequireAuthFromService` helper in `internal/auth/middleware.go`**

Append:
```go
// RequireAuthFromService returns a RequireAuth middleware using the Service's
// signer + db. Convenience wrapper so HTTP handlers don't need to know about
// the internals.
func RequireAuthFromService(svc *Service) func(http.Handler) http.Handler {
	return RequireAuth(svc.cfg.Signer, svc.cfg.DB)
}
```

The `cfg` field on `Service` is unexported; expose it via a small accessor or make the fields used here package-visible. Simplest: add the method above to the same package, where `Service.cfg` is directly accessible. That works because `middleware.go` and `service.go` are in the same `auth` package.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/api/... -v
go test ./...
```

All pass.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/auth/middleware.go
git commit -m "feat(api): /api/auth/* HTTP handlers

POST /signup, /verify, /login, /logout, /reset-request, /reset.
Logout is behind RequireAuth so it needs a valid token and jti.
Error responses use the shared WriteError shape with stable codes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 15: Config extensions + main.go wiring

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/agenthub-server/main.go`

Config adds `mail:` and `auth:` sections; main.go constructs the auth service and mounts `/api/auth`.

- [ ] **Step 1: Extend `Config`**

Append to `internal/config/config.go`'s existing `Config` struct:

```go
type Config struct {
	Mode     Mode       `yaml:"mode"`
	Hostname string     `yaml:"hostname"`
	DataDir  string     `yaml:"data_dir"`
	HTTP     HTTPConfig `yaml:"http"`
	TLS      TLSConfig  `yaml:"tls"`
	DB       DBConfig   `yaml:"db"`
	Obs      ObsConfig  `yaml:"observability"`
	Mail     MailConfig `yaml:"mail"`     // NEW
	Auth     AuthConfig `yaml:"auth"`     // NEW
}

type MailConfig struct {
	Provider string     `yaml:"provider"` // "noop" | "smtp"
	From     string     `yaml:"from"`
	SMTP     SMTPConfig `yaml:"smtp"`
}

type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"` // resolved from env via PasswordEnv if set
	PasswordEnv string `yaml:"password_env"`
}

type AuthConfig struct {
	Issuer             string        `yaml:"issuer"`               // default "agenthub-server"
	SessionTTL         time.Duration `yaml:"session_ttl"`          // default 24h
	EmailVerifyTTL     time.Duration `yaml:"email_verify_ttl"`     // default 24h
	PasswordResetTTL   time.Duration `yaml:"password_reset_ttl"`   // default 1h
	VerifyURLPrefix    string        `yaml:"verify_url_prefix"`    // e.g. https://agenthub.app/api/auth/verify
	ResetURLPrefix     string        `yaml:"reset_url_prefix"`     // e.g. https://agenthub.app/api/auth/reset
}
```

Add `"time"` to the single `import` block.

Update `Default()` to initialize:
```go
		Mail: MailConfig{
			Provider: "noop",
		},
		Auth: AuthConfig{
			Issuer:           "agenthub-server",
			SessionTTL:       24 * time.Hour,
			EmailVerifyTTL:   24 * time.Hour,
			PasswordResetTTL: time.Hour,
		},
```

Update `applyEnv` to add env var mappings. Append these blocks:
```go
	if v := os.Getenv("AGENTHUB_MAIL_PROVIDER"); v != "" {
		c.Mail.Provider = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_FROM"); v != "" {
		c.Mail.From = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_HOST"); v != "" {
		c.Mail.SMTP.Host = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Mail.SMTP.Port = n
		}
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_USER"); v != "" {
		c.Mail.SMTP.Username = v
	}
	if v := os.Getenv("AGENTHUB_MAIL_SMTP_PASS"); v != "" {
		c.Mail.SMTP.Password = v
	}
	if v := os.Getenv("AGENTHUB_VERIFY_URL_PREFIX"); v != "" {
		c.Auth.VerifyURLPrefix = v
	}
	if v := os.Getenv("AGENTHUB_RESET_URL_PREFIX"); v != "" {
		c.Auth.ResetURLPrefix = v
	}
```

After `applyEnv` resolves the file-configured env indirection for SMTP password:
```go
	if c.Mail.SMTP.PasswordEnv != "" {
		if v := os.Getenv(c.Mail.SMTP.PasswordEnv); v != "" {
			c.Mail.SMTP.Password = v
		}
	}
```

Add that block at the end of `applyEnv`.

- [ ] **Step 2: Add a `TestConfig_MailDefaults` test**

Append to `internal/config/config_test.go`:
```go
func TestConfig_MailAndAuthDefaults(t *testing.T) {
	c := Default()
	require.Equal(t, "noop", c.Mail.Provider)
	require.Equal(t, "agenthub-server", c.Auth.Issuer)
	require.Equal(t, 24*time.Hour, c.Auth.SessionTTL)
	require.Equal(t, time.Hour, c.Auth.PasswordResetTTL)
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 3: Extend `main.go`**

The existing `main.go` (Plan 01) needs new imports and new wiring between opening the DB and mounting routes. Insert a helper `buildAuthService` and mount `/api/auth`:

New imports to add to the single import block:
```go
	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/mail"
)
```

After `logger.Info("migrations applied", ...)` and before `router := chi.NewRouter()`:

```go
	authSvc, err := buildAuthService(ctx, cfg, db, logger)
	if err != nil {
		return fmt.Errorf("build auth service: %w", err)
	}
```

After `router.Mount("/healthz", ...)` add:
```go
	router.Mount("/api/auth", api.AuthRoutes(authSvc))
```

Add the helper at the bottom of `main.go`:
```go
func buildAuthService(ctx context.Context, cfg config.Config, db dbpkg.DB, log *slog.Logger) (*auth.Service, error) {
	key, err := auth.LoadOrCreateJWTKey(ctx, db.SQL())
	if err != nil {
		return nil, err
	}
	signer := auth.NewJWTSigner(key, cfg.Auth.Issuer)

	var mailer mail.Mailer
	switch cfg.Mail.Provider {
	case "smtp":
		mailer = mail.NewSMTP(mail.SMTPConfig{
			Host:     cfg.Mail.SMTP.Host,
			Port:     cfg.Mail.SMTP.Port,
			Username: cfg.Mail.SMTP.Username,
			Password: cfg.Mail.SMTP.Password,
			From:     cfg.Mail.From,
		})
	default:
		mailer = mail.NewNoop(log)
	}

	return auth.NewService(auth.Config{
		DB:              db.SQL(),
		Signer:          signer,
		Mailer:          mailer,
		Log:             log,
		TTL:             auth.Lifetimes{Session: cfg.Auth.SessionTTL, EmailVerify: cfg.Auth.EmailVerifyTTL, PasswordReset: cfg.Auth.PasswordResetTTL},
		From:            cfg.Mail.From,
		VerifyURLPrefix: cfg.Auth.VerifyURLPrefix,
		ResetURLPrefix:  cfg.Auth.ResetURLPrefix,
	}), nil
}
```

- [ ] **Step 4: Build and test**

```bash
make build
go test ./... -count=1
go vet ./...
```

All pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config cmd/agenthub-server/main.go
git commit -m "feat(cmd,config): mount /api/auth with mail + auth config

Adds MailConfig (provider=noop|smtp) and AuthConfig (issuer, TTLs,
URL prefixes) to Config with env overrides and sensible defaults.
main.go builds an auth.Service from the DB + config and mounts
its chi subrouter at /api/auth.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 16: End-to-end auth integration test

**Files:**
- Create: `test/integration/auth_flow_test.go`

- [ ] **Step 1: Write the test**

Create `test/integration/auth_flow_test.go` with exactly this content. It relies on `buildBinary` and `projectRoot` defined in `boot_test.go` (same package) from Plan 01.

```go
package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoot_AuthSignupVerifyLoginReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal shutdown differs on Windows; core auth is unit-tested")
	}

	smtp := newMiniSMTP(t)
	binary := buildBinary(t)
	dataDir := t.TempDir()

	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AGENTHUB_MODE=solo",
		"AGENTHUB_TLS_MODE=off",
		"AGENTHUB_HTTP_PORT=18182",
		"AGENTHUB_DATA_DIR="+dataDir,
		"AGENTHUB_LOG_LEVEL=warn",
		"AGENTHUB_MAIL_PROVIDER=smtp",
		"AGENTHUB_MAIL_FROM=AgentHub <noreply@test.local>",
		"AGENTHUB_MAIL_SMTP_HOST=127.0.0.1",
		"AGENTHUB_MAIL_SMTP_PORT="+smtp.Port,
		"AGENTHUB_VERIFY_URL_PREFIX=http://127.0.0.1:18182/api/auth/verify",
		"AGENTHUB_RESET_URL_PREFIX=http://127.0.0.1:18182/api/auth/reset",
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

	base := "http://127.0.0.1:18182"
	waitReady(t, base+"/healthz")

	_ = postExpect(t, base+"/api/auth/signup", map[string]string{
		"email":        "e2e@example.com",
		"password":     "topsecretpw",
		"account_name": "E2E",
	}, 200)

	verifyToken := smtp.WaitForToken(t, "/api/auth/verify", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/verify", map[string]string{"token": verifyToken}, 200)

	loginBody := postExpect(t, base+"/api/auth/login", map[string]string{
		"email": "e2e@example.com", "password": "topsecretpw",
	}, 200)
	var login struct{ Token string `json:"token"` }
	require.NoError(t, json.Unmarshal(loginBody, &login))
	require.NotEmpty(t, login.Token)

	// Logout with bearer.
	req, _ := http.NewRequest("POST", base+"/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	_ = resp.Body.Close()

	_ = postExpect(t, base+"/api/auth/reset-request", map[string]string{"email": "e2e@example.com"}, 200)
	resetToken := smtp.WaitForToken(t, "/api/auth/reset", 5*time.Second)
	_ = postExpect(t, base+"/api/auth/reset", map[string]string{"token": resetToken, "password": "newpassword9"}, 200)

	// Old pw fails.
	_ = postExpect(t, base+"/api/auth/login", map[string]string{"email": "e2e@example.com", "password": "topsecretpw"}, 401)
	// New pw works.
	_ = postExpect(t, base+"/api/auth/login", map[string]string{"email": "e2e@example.com", "password": "newpassword9"}, 200)
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "server did not become ready")
}

func postExpect(t *testing.T, url string, body any, wantStatus int) []byte {
	t.Helper()
	bs, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(bs))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, wantStatus, resp.StatusCode, "body=%s", string(raw))
	return raw
}

// --- miniSMTP: accepts one connection, reads the DATA body into bodies chan,
// replies 250 to everything, closes cleanly on QUIT. ---

type miniSMTP struct {
	Port   string
	bodies chan string
}

func newMiniSMTP(t *testing.T) *miniSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	s := &miniSMTP{Port: port, bodies: make(chan string, 8)}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *miniSMTP) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	w := conn
	write := func(line string) { _, _ = fmt.Fprintf(w, "%s\r\n", line) }

	write("220 mini.smtp")
	var body strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				s.bodies <- body.String()
				body.Reset()
				write("250 OK")
				inData = false
				continue
			}
			body.WriteString(line + "\n")
			continue
		}
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			write("250-mini.smtp")
			write("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(up, "AUTH"):
			write("235 auth ok")
		case strings.HasPrefix(up, "MAIL FROM"), strings.HasPrefix(up, "RCPT TO"):
			write("250 OK")
		case strings.HasPrefix(up, "DATA"):
			write("354 send it")
			inData = true
		case strings.HasPrefix(up, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

func (s *miniSMTP) WaitForToken(t *testing.T, urlSubstring string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		select {
		case body := <-s.bodies:
			if !strings.Contains(body, urlSubstring) {
				continue
			}
			idx := strings.Index(body, "token=")
			require.NotEqual(t, -1, idx, "no token= in body: %s", body)
			tok := body[idx+len("token="):]
			if nl := strings.IndexAny(tok, " \r\n"); nl != -1 {
				tok = tok[:nl]
			}
			return strings.TrimSpace(tok)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("miniSMTP: no %q email within %s", urlSubstring, timeout)
		}
	}
}

// The projectRoot and buildBinary helpers are defined in boot_test.go
// (same package), so they're available here without reimport.
```

- [ ] **Step 2: Run the test**

```bash
go test -race -timeout 60s ./test/integration/... -v
```

Expected: both `TestBoot_SoloMode_HealthzOK` (from Plan 01) and `TestBoot_AuthSignupVerifyLoginReset` pass.

- [ ] **Step 3: Commit**

```bash
git add test/integration
git commit -m "test: end-to-end auth flow integration

Boots the binary with MAIL_PROVIDER=smtp pointing at an in-test
mini-SMTP server, signs up, extracts the verification token from
the captured email, verifies, logs in, logs out, resets password,
and asserts old password fails while new password succeeds.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 17: Final smoke, lint, tag

- [ ] **Step 1: Run full test suite + lint**

```bash
make test
go test -race -timeout 120s ./test/integration/...
make lint
```

All PASS. If `gofmt` complains, run `gofmt -w .` and re-commit as `style: gofmt`.

- [ ] **Step 2: Manual smoke — full auth flow from shell**

```bash
DATADIR=$(mktemp -d)
AGENTHUB_MODE=solo AGENTHUB_TLS_MODE=off \
AGENTHUB_HTTP_PORT=18080 AGENTHUB_DATA_DIR=$DATADIR \
AGENTHUB_MAIL_PROVIDER=noop \
AGENTHUB_LOG_LEVEL=info \
./bin/agenthub-server &
PID=$!
sleep 1

# Signup — the server will log the verification URL because mail.provider=noop.
curl -s -X POST http://127.0.0.1:18080/api/auth/signup \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"demopassword","account_name":"Demo"}'
echo

# The logs printed a URL; extract the token and:
# curl -s -X POST http://127.0.0.1:18080/api/auth/verify -H 'Content-Type: application/json' -d '{"token":"…"}'

kill -INT $PID
wait $PID 2>/dev/null
```

Expected: signup returns 200 with `{"user_id":"…","account_id":"…"}`. Log shows `mail.noop.send` with the verify URL.

- [ ] **Step 3: Tag**

```bash
git tag -a v0.2.0-auth -m "Plan 02: core password auth + tenancy complete

- users, accounts, memberships, auth_sessions, verification_tokens
- argon2id password hashing (OWASP 2024 params)
- HS256 JWT with app_meta signing-key bootstrap
- Mailer interface with noop + SMTP implementations
- Auth service: signup, verify, login, logout, password reset
- RequireAuth middleware with jti revocation check
- /api/auth/* HTTP handlers mounted by main.go
- End-to-end integration test exercises full flow via in-test SMTP"
```

Do not push — left to operator discretion.

---

## Done state

At the end of Plan 02:

- `POST /api/auth/signup` creates user + account + membership transactionally, sends verify email.
- `POST /api/auth/verify` consumes the email_verify token and marks the user verified.
- `POST /api/auth/login` issues a JWT tied to a new auth_sessions row; only works after email is verified.
- `POST /api/auth/logout` (requires Bearer JWT) revokes the session.
- `POST /api/auth/reset-request` + `POST /api/auth/reset` rotate a user's password.
- Protected routes anywhere in the app can use `auth.RequireAuth(...)` middleware to require a valid token + non-revoked session.
- All flows covered by unit tests and one end-to-end integration test that exercises them against a real subprocess of the binary using an in-test SMTP server.

## Exit to Plan 03

Plan 03 ("Auth extensions") adds OAuth login (Google, GitHub via `golang.org/x/oauth2`), long-lived API tokens for machine clients, per-IP rate limiting on auth endpoints using `golang.org/x/time/rate`, and idempotency keys (`idempotency_keys` table + middleware) for resource-creating POSTs.
