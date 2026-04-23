-- +goose Up
ALTER TABLE users ADD COLUMN is_operator INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- SQLite doesn't support DROP COLUMN; would need table rebuild. Skip for dev.
