# agenthub-server

Single-binary server for [AgentHub](https://github.com/scottkw/agenthub):
Headscale coordination, DERP relay, auth, relational DB, object storage,
realtime fan-out, and operator admin console — all in one compiled Go binary.

Runs in two modes:
- **`solo`** — self-hosted default. SQLite + local FS + single default
  account. One binary, one data directory.
- **`hosted`** — multi-tenant SaaS on a VPS. Postgres + S3/R2 + many
  accounts. Same binary, different config.

See [`docs/superpowers/specs/2026-04-16-agenthub-server-design.md`](docs/superpowers/specs/2026-04-16-agenthub-server-design.md) for the design.

## Status — v1.0.0 Complete

All 10 implementation plans are landed and tagged:

- **Plan 01** `v0.1.0-foundation` — foundation, config, migrations, SQLite, HTTP frontend, supervisor.
- **Plan 02** `v0.2.0-auth` — email+password auth, email verification, password reset, JWT sessions, accounts/memberships.
- **Plan 03** `v0.3.0-auth-extensions` — Google/GitHub OAuth, `ahs_` API tokens, per-IP rate limit, `Idempotency-Key` response cache.
- **Plan 04** `v0.4.0-devices-sessions` — device registry, pair-code/claim onboarding flow, `agent_sessions` metadata CRUD.
- **Plan 05** `v0.5.0-headscale` — real Headscale v0.28.0 integration as a managed subprocess. Device claim mints actual tailnet pre-auth keys; `/headscale/*` proxied through the main frontend.
- **Plan 06** `v0.6.0-derp` — Headscale embedded DERP relay. `/derp/*` proxied; claim response returns a real `tailcfg.DERPMap`.
- **Plan 07** `v0.7.0-realtime` — in-memory WebSocket hub scoped by account. `/ws` endpoint (JWT auth); device claim publishes `device.created`. Pluggable `Publisher` interface for later Redis/NATS backends.
- **Plan 08** `v0.8.0-blobs` — object storage with two-phase upload (`presign → PUT → commit`), `blob.created` realtime event, file backend with S3-compatible `Blob` interface.
- **Plan 09** `v0.9.0-admin` — embedded React admin SPA at `/admin`, operator-gated `/api/admin/*` endpoints (users, accounts, health).
- **Plan 10** `v1.0.0` — deployment polish: multi-stage Dockerfile, hardened systemd unit, extended `/healthz` (uptime, goroutines, memory), graceful shutdown with WS reconnect, cross-platform release workflow.

## Quick start (development)

    make build

    # Plain HTTP on :18080, SQLite in a temp dir
    AGENTHUB_MODE=solo \
    AGENTHUB_TLS_MODE=off \
    AGENTHUB_HTTP_PORT=18080 \
    AGENTHUB_DATA_DIR=$(mktemp -d) \
    ./bin/agenthub-server

    curl http://127.0.0.1:18080/healthz
    # {"status":"ok","version":"dev","uptime_sec":2.04,"db":"ok",
    #  "go":{"goroutines":10,"memory_mb":8.9}}

## Config

See [`config.example.yaml`](config.example.yaml). Precedence, highest wins:

1. Environment variables (`AGENTHUB_*`)
2. `--config` YAML file
3. Compiled defaults

## Tests

    make test                                   # unit tests
    make lint                                   # go vet + gofmt
    go test -race -timeout 180s ./test/integration/...  # E2E (boots binary)

## Deployment

### Docker

    docker build -t agenthub-server .
    docker run -p 443:443 -p 80:80 -v /var/lib/agenthub-server:/data \
      -e AGENTHUB_MODE=solo -e AGENTHUB_DATA_DIR=/data \
      agenthub-server

Multi-stage Dockerfile: Node build for admin SPA → Go build for binary → distroless runtime image.

### systemd

    sudo cp bin/agenthub-server /usr/local/bin/
    sudo mkdir -p /etc/agenthub-server /var/lib/agenthub-server
    sudo cp systemd/agenthub-server.service /etc/systemd/system/
    sudo systemctl daemon-reload
    sudo systemctl enable --now agenthub-server

The included service unit uses strict hardening: `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`, `CapabilityBoundingSet=` (empty), and `SystemCallFilter=@system-service`.

### GitHub Releases

Pushing a `v*` tag triggers the release workflow:

- Cross-platform binaries: linux/darwin × amd64/arm64
- Docker image pushed to `ghcr.io/scottkw/agenthub-server:<tag>`
- GitHub Release with binary artifacts and auto-generated notes

## API Overview

| Endpoint | Auth | Description |
|----------|------|-------------|
| `POST /api/auth/signup` | — | Create account + user |
| `POST /api/auth/login` | — | JWT session |
| `POST /api/auth/oauth/{google,github}` | — | OAuth login |
| `GET /api/devices` | Bearer | List devices |
| `POST /api/devices/pair-code` | Bearer | Issue pair code |
| `POST /api/devices/claim` | — | Redeem pair code, get token |
| `GET /api/sessions` | Bearer/Token | List agent sessions |
| `POST /api/sessions` | Token | Create session (device) |
| `GET /ws?token=` | JWT query | Realtime event stream |
| `POST /api/blobs/presign` | Bearer | Get upload URL |
| `PUT /api/blobs/upload/{id}` | Bearer | Upload bytes |
| `POST /api/blobs/{id}/commit` | Bearer | Record metadata |
| `GET /api/admin/users` | Operator | List all users |
| `GET /api/admin/accounts` | Operator | List all accounts |
| `GET /admin` | Operator | Admin SPA (React) |
| `GET /healthz` | — | Health + runtime stats |

## License

Proprietary. Copyright © Ken Scott.
