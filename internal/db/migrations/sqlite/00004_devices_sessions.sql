-- +goose Up

CREATE TABLE devices (
    id                 TEXT PRIMARY KEY,
    account_id         TEXT NOT NULL REFERENCES accounts(id),
    user_id            TEXT NOT NULL REFERENCES users(id),
    name               TEXT NOT NULL DEFAULT '',
    platform           TEXT NOT NULL DEFAULT '',
    app_version        TEXT NOT NULL DEFAULT '',
    tailscale_node_id  TEXT NULL,
    last_seen_at       TEXT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at         TEXT NULL
);

CREATE INDEX idx_devices_account_id ON devices(account_id);
CREATE INDEX idx_devices_user_id ON devices(user_id);

CREATE TABLE device_pair_codes (
    code                   TEXT PRIMARY KEY,
    account_id             TEXT NOT NULL REFERENCES accounts(id),
    user_id                TEXT NOT NULL REFERENCES users(id),
    created_at             TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at             TEXT NOT NULL,
    consumed_at            TEXT NULL,
    consumed_by_device_id  TEXT NULL
);

CREATE INDEX idx_device_pair_codes_account_id ON device_pair_codes(account_id);

CREATE TABLE agent_sessions (
    id               TEXT PRIMARY KEY,
    account_id       TEXT NOT NULL REFERENCES accounts(id),
    device_id        TEXT NOT NULL REFERENCES devices(id),
    label            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','stopped')),
    cwd              TEXT NOT NULL DEFAULT '',
    started_at       TEXT NOT NULL DEFAULT (datetime('now')),
    ended_at         TEXT NULL,
    last_activity_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_sessions_account_id ON agent_sessions(account_id);
CREATE INDEX idx_agent_sessions_device_id ON agent_sessions(device_id);

-- +goose Down
DROP TABLE agent_sessions;
DROP TABLE device_pair_codes;
DROP TABLE devices;
