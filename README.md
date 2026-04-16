# agenthub-server

Single-binary server for AgentHub: Headscale coordination, DERP relay,
auth, DB, object storage, realtime, billing, and admin console.

See `docs/superpowers/specs/2026-04-16-agenthub-server-design.md`.

## Build

    make build

## Run

    ./bin/agenthub-server --config config.example.yaml
