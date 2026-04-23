-- +goose Up
CREATE TABLE blob_objects (
    id                 TEXT PRIMARY KEY,
    account_id         TEXT NOT NULL REFERENCES accounts(id),
    key                TEXT NOT NULL,
    content_type       TEXT NOT NULL DEFAULT '',
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    sha256             TEXT NOT NULL DEFAULT '',
    created_by_user_id TEXT NOT NULL REFERENCES users(id),
    created_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_blob_objects_account_key ON blob_objects(account_id, key);
CREATE INDEX idx_blob_objects_account_id ON blob_objects(account_id);

-- +goose Down
DROP TABLE blob_objects;
