# AgentHub Server — Design Spec

**Date:** 2026-04-16
**Status:** Draft, awaiting user review
**Repo:** `~/dev/agenthub-server`

## 1. Summary

AgentHub Server is a single compiled Go binary (`agenthub-server`) that provides the server-side control plane, coordination, relay, auth, data, storage, realtime, and billing services for the AgentHub client. It replaces what would otherwise be a multi-container stack (Headscale + NginX Proxy Manager + Supabase + custom API) with one process that can be run either as a hosted multi-tenant SaaS or as a self-hosted single-tenant server.

The same binary runs in two modes selected by config:

- **`solo`** — self-hosted default. SQLite + local filesystem + single default account. One binary, one data dir.
- **`hosted`** — Ken's SaaS. Postgres + S3-compatible object store + many accounts. Same binary, different config.

State backends are behind interfaces (`internal/db`, `internal/blob`, eventually `internal/realtime` for horizontal scale), so the mode switch is a config change, not a rewrite.

## 2. Goals and non-goals

### Goals

- Single compiled Go binary matching AgentHub client's "single executable" ethos.
- Full-service: Headscale coordination, DERP relay, auth, relational DB, object storage, realtime fan-out, Stripe billing, operator admin console.
- Usable by both a hobbyist self-hoster ("download binary, run") and Ken's hosted SaaS with managed Postgres + R2/S3.
- No horizontal-scaling rewrite required — interfaces in place so scale-out is additive.

### Non-goals (v1)

- User-facing web portal (signup/billing happens in the native client).
- Edge functions / user-deployed server-side code.
- Multi-region DERP fleet (design supports it; v1 ships single-region).
- Web UI that duplicates native-client functionality.
- A Supabase-compatible PostgREST/GraphQL surface.
- Metering / usage-based billing. Scaffolding only: per-account subscription record + entitlement flags.

## 3. Constraints and prior decisions

- **Client already embeds `tailscale.com v1.96.3` (tsnet).** The server's Headscale endpoint is the control URL the client's tsnet points at. No client-side change required beyond config.
- **Scale target: "don't paint into a corner, don't over-engineer."** Single beefy VPS must work. Horizontal scale must be a refactor of specific subsystems (realtime hub, sessions), not a rewrite.
- **Tenancy model:** application-level enforcement via `account_id` scoping — not Postgres RLS — because SQLite has no RLS and we want one code path across both DBs.
- **Licensing:** Headscale (BSD-3), Tailscale DERP (BSD-3), Caddy/certmagic (Apache-2). All embed-compatible with a proprietary single-binary.

## 4. High-level architecture

```
                            ┌──────────────────────────┐
                            │   agenthub-server (Go)   │
                            │                          │
Internet ── :443 (HTTPS) ──▶│  TLS (certmagic/ACME)    │
                            │         │                │
                            │         ├── /headscale/* ── Headscale control plane
                            │         ├── /derp        ── DERP relay
                            │         ├── /api/*       ── AgentHub JSON API
                            │         ├── /ws          ── Realtime WebSocket hub
                            │         └── /admin/*     ── Embedded React admin SPA
                            │                          │
                            │  Subsystems:             │
                            │   ┌─ Auth (JWT, OAuth)   │
                            │   ├─ Tenancy / accounts  │
                            │   ├─ Billing (Stripe)    │
                            │   ├─ Device registry     │
                            │   ├─ Session metadata    │
                            │   ├─ Storage adapter ───────▶ Local FS OR S3/R2/B2
                            │   └─ DB adapter  ────────────▶ SQLite OR Postgres
                            └──────────────────────────┘
```

**Port surface:** `:443` TCP (all HTTPS), `:80` TCP (ACME + redirect), `:3478` UDP (STUN for DERP). Nothing else.

**What is NOT in the binary:** Postgres (when used), the object store (when used), SMTP/transactional email. These are always external services in hosted mode; solo mode uses SQLite + local FS + optional SMTP.

## 5. Component breakdown

All subsystems live under `internal/` and expose small, testable interfaces.

### Root / lifecycle

- `cmd/agenthub-server` — entrypoint. Parses config, wires dependencies, runs the supervisor.
- `internal/supervisor` — `errgroup`-based lifecycle manager. Starts subsystems in dependency order, handles graceful shutdown via context cancellation on SIGINT/SIGTERM.
- `internal/config` — typed `Config` loaded from flags → env → YAML → defaults (highest precedence first). Fails fast on invalid config graphs (e.g., `mode: hosted` with no `db.url`).

### Data & storage adapters (the pluggable layer)

- `internal/db` — `DB` interface. Implementations: `db/sqlite`, `db/postgres`. Migrations via `golang-migrate` with embedded SQL. Queries via `sqlc` (type-safe Go for both dialects).
- `internal/blob` — `Blob` interface backed by `gocloud.dev/blob`. Supports `file://` (solo), `s3://`, and `r2://` (via S3-compatible endpoint).

### Domain subsystems

- `internal/tenancy` — `accounts`, `memberships`, invitations. Every domain row carries `account_id`. Solo mode bootstraps a single default account.
- `internal/auth` — email+password (argon2id), email verification, password reset, OAuth (Google + GitHub via `golang.org/x/oauth2`), JWT-based sessions with revocable `jti` → `auth_sessions` row lookup. Middleware for per-route auth and role checks.
- `internal/billing` — Stripe SDK wrapper. Checkout session creation, webhook handler, `account.HasEntitlement(name)` lookup. No metering in v1. Solo mode: all entitlements true.
- `internal/devices` — device registry: name, platform, last-seen, app version, Tailscale node-ID cross-reference.
- `internal/sessions` — AgentHub session *metadata* (status, label, last activity). The actual terminal I/O flows peer-to-peer over the tailnet; server only tracks what exists.

### Tailscale / network subsystems

- `internal/headscale` — imports Headscale as a Go library, runs its HTTP handlers in-process, and backs its storage with the same DB adapter. A bridge table (`headscale_user_links`) maps our `users` → Headscale users.
- `internal/derp` — wraps `tailscale.com/derp` + `tailscale.com/derp/derphttp` to run a DERP server in-process. Advertises itself in the Headscale DERP map.

### Transport layer

- `internal/realtime` — in-memory WebSocket fan-out hub, scoped by `account_id`. Heartbeat pings every 30s, stale-connection cull at 90s. Interface is pluggable for a later Redis/NATS-backed implementation.
- `internal/api` — REST handlers (`net/http` + `chi`). JSON in/out. Request logging, rate limiting, auth middleware, idempotency-key support.
- `internal/admin` — serves the embedded React admin SPA from `embed.FS`, sharing the `/api/*` surface under the hood with operator-role gating.
- `internal/httpfront` — single TLS + routing frontend. Caddy-as-library (programmatic) for HTTP/3 + ACME, or `certmagic` + `chi` for a lighter surface. **Choice deferred to early implementation spike.**

### Cross-cutting

- `internal/mail` — SMTP + pluggable transactional providers (Resend, Postmark). `none` mode valid for self-hosters who don't want outbound email.
- `internal/obs` — `slog` structured logging, Prometheus metrics on `/metrics` (admin-gated), optional OTel traces.

### Two component-level risks flagged

1. **Headscale as a library is not officially supported.** Upstream is importable Go but API stability between versions isn't guaranteed. Mitigation: pin version, wrap behind `internal/headscale`, keep blast radius of upgrades contained. Fallback: run Headscale's `main()` in a goroutine or as a subprocess if upstream becomes hostile to embedding.
2. **Caddy-as-library vs. `certmagic`+`chi` is a deferred decision.** Caddy-as-library gives HTTP/3 and battle-tested ACME out of the box but is a heavy dep. `certmagic` alone is leaner. Decide during implementation spike.

## 6. Data model

### ID strategy

UUIDv7 everywhere (time-ordered, index-friendly). `TEXT` in SQLite, `UUID` in Postgres. One Go type (`string`), one generator.

### Tenancy rule

Every domain row carries `account_id`. Enforced at the query layer (not Postgres RLS) because SQLite has no RLS and we want a single code path. `sqlc`-generated helpers inject `account_id` from request context where possible, making "forgot to scope by account" a compile-time mistake.

### Soft deletes

Only on `accounts`, `users`, `devices` (`deleted_at TIMESTAMPTZ NULL`). Everything else hard-deletes.

### Core tables

- **`accounts`** — tenant unit. `id`, `slug` (unique), `name`, `plan` (`free`/`pro`/`team`/`self_hosted`), timestamps.
- **`users`** — humans, global across accounts. `id`, `email` (unique, lowercased), `password_hash` (argon2id, nullable for OAuth-only), `email_verified_at`, `name`, `avatar_url`, timestamps.
- **`memberships`** — users↔accounts N:N with role. `id`, `account_id`, `user_id`, `role` (`owner`/`admin`/`member`), timestamps. Unique on (`account_id`, `user_id`).
- **`auth_sessions`** — active JWTs for revocation. `id`, `user_id`, `account_id`, `token_hash`, `user_agent`, `ip`, `expires_at`, `revoked_at`. JWT `jti` maps to row.
- **`oauth_identities`** — `user_id`, `provider`, `provider_user_id`. Unique on (`provider`, `provider_user_id`).
- **`verification_tokens`** — unified for email-verify / password-reset / invite / device-pair. `purpose`, `user_id` (nullable), `email`, `token_hash`, `expires_at`, `consumed_at`.
- **`api_tokens`** — long-lived machine tokens for the AgentHub client daemon. `id`, `account_id`, `user_id`, `device_id` (nullable), `token_hash`, `scope` (JSON array), `last_used_at`, `expires_at` (nullable).
- **`idempotency_keys`** — `key` + `account_id` + `response_body` + `created_at`. 24h retention. Used by resource-creating POSTs.

### Billing tables

- **`stripe_customers`** — `account_id` (unique), `stripe_customer_id`.
- **`subscriptions`** — `account_id`, `stripe_subscription_id`, `status`, `plan`, `current_period_end`, raw Stripe object in `data JSONB`. Stripe is source of truth; this is a cache for fast entitlement checks.

### AgentHub domain tables

- **`devices`** — `id`, `account_id`, `user_id`, `name`, `platform`, `app_version`, `tailscale_node_id` (nullable), `last_seen_at`, timestamps.
- **`agent_sessions`** — metadata only. `id`, `account_id`, `device_id`, `label`, `status` (`running`/`stopped`), `cwd`, `started_at`, `ended_at`, `last_activity_at`.
- **`blob_objects`** — object metadata. `id`, `account_id`, `key`, `content_type`, `size_bytes`, `sha256`, `created_by_user_id`, timestamps.

### Audit log

- **`audit_events`** — `id`, `account_id`, `actor_user_id` (nullable), `action`, `target_type`, `target_id`, `ip`, `user_agent`, `metadata JSONB`, `created_at`. Append-only. Drives admin console audit view.

### Headscale data

Headscale's own schema kept as-is in a separate namespace (`headscale_*` prefix in SQLite / separate schema in Postgres). **Do not fork Headscale's schema.** A bridge table (`headscale_user_links`) maps our `(account_id, user_id)` → Headscale user. On user signup, we auto-create the Headscale user and link it.

### Migrations

- `migrations/001_init.sql`, `002_*.sql`, … forward-only.
- Dual-dialect test harness: every migration + query runs against both SQLite and Postgres with identical assertions.
- Headscale migrations live in their own dir, applied by `internal/headscale`.
- `sqlc` configs per dialect, with dialect-specific overrides where necessary. 90%+ of queries are expected to be dialect-agnostic.

## 7. Key data flows

### Flow A — First-time signup from the native client

1. Client POSTs `/api/auth/signup` `{email, password, account_name}`.
2. Server creates `users`, `accounts`, `memberships`, `stripe_customers` (hosted), Headscale user + link, verification token, sends email.
3. Returns `{user_id, account_id, tmp_jwt}`. Client is restricted until email verified.
4. User clicks email link → `/api/auth/verify?token=…` flips `email_verified_at`.
5. Client realtime-notified or polls `/api/auth/me` and unlocks.

### Flow B — Device registration (critical path)

1. Signed-in device A POSTs `/api/devices/pair-code` → server returns short-lived pairing code.
2. New device B POSTs `/api/devices/claim` with code + device metadata.
3. Server creates `devices` row, mints Headscale pre-auth key (5-min TTL, device-tagged), issues long-lived `api_tokens` record, returns `{api_token, tailscale: {control_url, pre_auth_key, derp_map}}`.
4. Device B's embedded tsnet joins the tailnet using the returned credentials; reports `tailscale_node_id` back via `/api/devices/{id}/tailscale-info`.
5. Server fires `device.created` realtime event → device A's UI updates live.

The Headscale control URL presented to the client is `https://<host>/headscale` — same origin as the API. One hostname, one cert, one firewall rule.

### Flow C — Realtime event fan-out

1. Client opens `wss://<host>/ws` with JWT.
2. Hub validates, registers connection under `account:{id}`.
3. Domain mutations call `realtime.Publish(accountID, event)` *after* DB commit. Publish failures log-and-move-on; DB is source of truth.
4. Hub fans out to all connections for that account. Events never cross account boundaries.
5. Heartbeat pings every 30s; stale connections culled at 90s.

### Flow D — Object upload (presign + commit)

1. Client POSTs `/api/objects/presign` with `{content_type, size_bytes, sha256}`. Server validates quota, signs PUT URL, returns `{put_url, object_id}`.
2. Client PUTs bytes directly to R2/S3 (or to a loopback blob endpoint in solo mode).
3. Client POSTs `/api/objects/{object_id}/commit`. Server HEAD-checks the blob, inserts `blob_objects` row.
4. Downloads: `GET /api/objects/{id}` → access check → signed GET URL.

Two-phase avoids proxying large files through the Go process and prevents orphaned metadata-less blobs.

### Flow E — Stripe subscription change

1. Client POSTs `/api/billing/checkout` → server creates Stripe Checkout Session → returns `checkout_url` → client opens in default browser.
2. User completes checkout on Stripe → redirect to `/api/billing/return` (simple confirmation page).
3. Stripe webhook hits `/api/billing/webhook` → handler validates signature, upserts `subscriptions` row, fires `billing.updated` realtime event.
4. Client refreshes from realtime push; entitlement checks now reflect new plan.

Webhook and API are independent. API never infers subscription status from the checkout response — only from the webhook. Stripe is source of truth.

### Error-handling posture

- Client-visible errors: structured JSON `{error: {code, message, details}}`. Codes are stable; messages are human-readable.
- **Let it crash at boundaries.** DB/storage errors surface as 503s, not silent fallbacks.
- Idempotency keys on resource-creating POSTs, 24h cache in `idempotency_keys` table. Stripe webhook uses Stripe's `event.id`.
- Webhook handlers return 200 once work is *enqueued*; processing errors surface in audit log for admin replay.

## 8. Deployment modes & config

### Config precedence

Flags → env (`AGENTHUB_*`) → `config.yaml` → compiled defaults.

### Solo mode bootstrap

Empty config valid. On first start:
1. Creates `$XDG_DATA_HOME/agenthub-server/` (or `/var/lib/agenthub-server/` as a service).
2. Opens `agenthub.db` (SQLite, WAL, `foreign_keys=ON`).
3. Runs migrations.
4. Seeds `default` account if none exists. Prints a one-time admin bootstrap URL to stdout.
5. Obtains LE cert via certmagic (HTTP-01 or DNS-01).
6. Starts all subsystems.

Alternative unattended bootstrap via `AGENTHUB_BOOTSTRAP_ADMIN_EMAIL` + `AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD` env vars.

### Hosted mode config

Declares `db.url` (Postgres), `blob` (S3/R2 bucket), `billing.stripe.*`, `mail.*`, `oauth.*`. Server validates the config graph on startup and refuses partial configs (e.g., `mode: hosted` without `db.url` is fatal).

### Secrets

All secrets via env vars only. Config references env names (`secret_key_env: STRIPE_SECRET_KEY`). Never logged, redacted in admin diagnostics.

### Running

1. **systemd + raw binary** (recommended): shipped with `systemd/agenthub-server.service` unit, `Restart=on-failure`, hardening directives.
2. **Docker** (single container, distroless base) for users who prefer it.
3. **Ken's production**: systemd-managed, Terraform/Ansible-provisioned, managed Postgres + R2 + Resend.

### Upgrades

Atomic binary swap + systemd restart. Graceful drain: stop accepting connections → send `reconnect` to WS clients → finish in-flight requests (30s timeout) → exit. Migrations run on startup, forward-only, CI-tested in both dialects.

### Backups

- **Solo:** `sqlite3 .backup` + rsync of blobs dir. Single directory = entire state.
- **Hosted:** managed Postgres provider snapshots; R2/S3 object versioning. `data_dir` on the VPS holds only Headscale's private key + ACME cache.

### Multi-region DERP (future)

Same binary in `services: [derp]` mode, no DB/API, authed to main control plane via shared secret, registered in the DERP map. Deferred past v1.

## 9. Operational concerns

- **TLS & certs:** certmagic with HTTP-01 default, DNS-01 optional. Renewals automatic, cert-expiry metric exposed.
- **Logging:** `slog` JSON (hosted) or text (solo TTY). Per-request correlation IDs. Secret-aware sanitizer. No JWTs / webhook payloads / passwords in logs.
- **Metrics:** Prometheus `/metrics`, admin-gated. HTTP, DB, Headscale node count, DERP throughput, realtime hub connections, Stripe webhook timing, cert expiry.
- **Tracing:** optional OTel HTTP + DB instrumentation.
- **Rate limiting:** in-process token-bucket per-IP (auth endpoints) and per-account (API endpoints). Redis-backed drop-in later.
- **Admin console v1:** user list + impersonate-as-support, account list + plan override, audit log search, subscription summary, health indicators, realtime connection count. Nothing else in v1.
- **Target SLOs:** 99% of `/api/*` p50 < 200ms / p99 < 1s under nominal load; < 1% non-auth error rate. Measured from day one.

## 10. Testing strategy

- **Unit tests.** Table-driven per package. Heaviest coverage on `auth`, `tenancy`, `billing` — the security-sensitive layer.
- **DB dialect parity tests.** `internal/db/parity_test.go` runs identical assertions against both SQLite and Postgres (Postgres via `testcontainers-go`). Every migration + query has a parity test.
- **Integration tests.** Full binary in-process in `mode: solo`. Covers: signup → verify → login → device-pair → realtime event → object upload/download → subscription cancel. Stripe webhooks tested via canned event fixtures.
- **E2E (real tailnet).** Nightly CI boots two tsnet clients against a throwaway server instance, confirms they can ping each other. The core-product canary.
- **Load/smoke.** `k6` script for signup + login + WS connect + device-create, run pre-release.
- **Security tests.** Argon2id parameters pinned + benchmarked. JWT tests prevent `alg: none` and algorithm-confusion attacks.
- **Coverage target:** 80%+ on domain subsystems. Matches project CLAUDE.md.

## 11. Open questions / deferred decisions

1. **Caddy-as-library vs. certmagic + chi** — decide during an early implementation spike.
2. **Subdomain split for Headscale/DERP** — v1 uses a single hostname with path routing. Easy to split later if operational clarity demands it.
3. **Billing depth** — v1 ships subscription record + entitlement flags only. Usage-based metering deferred until a real use case emerges.
4. **Multi-region DERP fleet** — design supports it, v1 ships single-region.
5. **Object-store abstraction in solo mode** — local FS via `gocloud.dev/blob`'s `fileblob` works, but signed URLs for local FS require an in-process endpoint. Validate during implementation.

## 12. Build-order sketch (not the plan, just the shape)

This is a hint for the implementation plan, not a commitment:

1. Scaffolding: `cmd/agenthub-server`, `internal/config`, `internal/supervisor`, `internal/obs`, `internal/db` (SQLite only first), migrations, `sqlc`.
2. Auth + tenancy: `internal/auth`, `internal/tenancy`, JWT middleware, email (`internal/mail`), verification flow.
3. HTTP frontend: `internal/httpfront`, `internal/api` with basic routes, TLS via certmagic.
4. Devices + sessions domain: `internal/devices`, `internal/sessions`, pair-code + claim flow.
5. Headscale integration: `internal/headscale`, bridge table, pre-auth-key minting.
6. DERP: `internal/derp`.
7. Realtime: `internal/realtime`, `/ws` endpoint, fan-out wired into mutations.
8. Blob storage: `internal/blob` (local FS first), presign/commit flow.
9. Admin SPA: React + Vite, embedded via `embed.FS`.
10. Postgres backend: `db/postgres` impl, dialect parity test harness.
11. S3/R2 blob backend: extend `internal/blob`, tested against MinIO.
12. Billing: `internal/billing`, Stripe checkout + webhook.
13. Integration + E2E test harness.
14. Deployment artifacts: systemd unit, Dockerfile, release workflow.

## 13. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Headscale library API churn between upstream versions | Pin version; isolate behind `internal/headscale`; subprocess fallback available |
| SQLite/Postgres behavior divergence | Dual-dialect parity test harness on every query and migration |
| Single-binary becomes a scaling bottleneck | Interfaces in place (`realtime`, `blob`, `db`); swap implementations, not rewrites |
| ACME rate limits during dev iteration | certmagic's staging environment in non-prod config |
| Stripe webhook missed during deploy | Webhooks are idempotent via Stripe `event.id`; Stripe retries non-2xx |
| Self-hoster email delivery complexity | `mail.provider: none` supported; admin creates users manually |
| Embedding Headscale raises a license question | BSD-3; embed-compatible with a proprietary binary per its terms |
