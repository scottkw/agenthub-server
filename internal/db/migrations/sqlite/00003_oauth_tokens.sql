-- +goose Up

CREATE TABLE oauth_states (
    state         TEXT PRIMARY KEY,
    provider      TEXT NOT NULL CHECK (provider IN ('google','github')),
    redirect_uri  TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at    TEXT NOT NULL,
    consumed_at   TEXT NULL
);

CREATE TABLE oauth_identities (
    id                TEXT PRIMARY KEY,
    user_id           TEXT NOT NULL REFERENCES users(id),
    provider          TEXT NOT NULL CHECK (provider IN ('google','github')),
    provider_user_id  TEXT NOT NULL,
    email             TEXT NOT NULL,
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(provider, provider_user_id)
);

CREATE INDEX idx_oauth_identities_user_id ON oauth_identities(user_id);

CREATE TABLE api_tokens (
    id            TEXT PRIMARY KEY,
    account_id    TEXT NOT NULL REFERENCES accounts(id),
    user_id       TEXT NOT NULL REFERENCES users(id),
    device_id     TEXT NULL,
    name          TEXT NOT NULL DEFAULT '',
    token_hash    TEXT NOT NULL UNIQUE,
    scope         TEXT NOT NULL DEFAULT '[]',
    last_used_at  TEXT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at    TEXT NULL,
    revoked_at    TEXT NULL
);

CREATE INDEX idx_api_tokens_account_id ON api_tokens(account_id);
CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);

CREATE TABLE idempotency_keys (
    key            TEXT NOT NULL,
    scope          TEXT NOT NULL DEFAULT '',
    method         TEXT NOT NULL,
    path           TEXT NOT NULL,
    request_hash   TEXT NOT NULL,
    response_code  INTEGER NOT NULL,
    response_body  BLOB NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at     TEXT NOT NULL,
    PRIMARY KEY (key, scope)
);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE api_tokens;
DROP TABLE oauth_identities;
DROP TABLE oauth_states;
