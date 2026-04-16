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

This is **Plan 01** of the implementation series: foundation + HTTP frontend.
Subsequent plans add auth, devices, Headscale, DERP, realtime, blob, admin
SPA, Postgres, S3, and billing. See `docs/superpowers/plans/`.

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
