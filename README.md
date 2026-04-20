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

Plans 01–07 of the implementation series are landed. Subsequent plans add
blob, admin SPA, Postgres, S3, and billing. See
`docs/superpowers/plans/`.

- **Plan 01** `v0.1.0-foundation` — foundation, config, migrations, SQLite, HTTP frontend.
- **Plan 02** `v0.2.0-auth` — email+password auth, email verification, password reset, JWT sessions, accounts/memberships.
- **Plan 03** `v0.3.0-auth-extensions` — Google/GitHub OAuth, `ahs_` API tokens, per-IP rate limit, `Idempotency-Key` response cache.
- **Plan 04** `v0.4.0-devices-sessions` — device registry, pair-code/claim onboarding flow, `agent_sessions` metadata CRUD. Headscale pre-auth-key minting is stubbed pending Plan 05.
- **Plan 05** `v0.5.0-headscale` — real Headscale v0.28.0 integration as a managed subprocess. Device claim mints actual tailnet pre-auth keys; `/headscale/*` proxied through the main frontend. Embedded DERP lands in Plan 06.
- **Plan 06** `v0.6.0-derp` — Headscale embedded DERP relay. `/derp/*` proxied through the main frontend; claim response returns a real `tailcfg.DERPMap` instead of the empty stub.
- **Plan 07** `v0.7.0-realtime` — in-memory WebSocket hub scoped by account. `/ws` endpoint (JWT auth); device claim publishes `device.created` post-commit. Pluggable `Publisher` interface for later Redis/NATS backends.

## Quick start (development)

    make build

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

    make test                                   # unit tests
    go test -race -timeout 60s ./test/integration/...   # boots the binary and hits /healthz

## Deployment

- [`deploy/systemd/agenthub-server.service`](deploy/systemd/agenthub-server.service) — hardened systemd unit.
- [`deploy/docker/Dockerfile`](deploy/docker/Dockerfile) — multi-stage, distroless image.

## License

Proprietary. Copyright © Ken Scott.
