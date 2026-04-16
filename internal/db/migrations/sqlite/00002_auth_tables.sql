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
    email              TEXT NOT NULL UNIQUE,
    password_hash      TEXT NULL,
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
    id          TEXT PRIMARY KEY,
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
    email         TEXT NOT NULL,
    token_hash    TEXT NOT NULL UNIQUE,
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
