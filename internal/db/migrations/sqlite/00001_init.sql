-- +goose Up
CREATE TABLE app_meta (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO app_meta (key, value) VALUES ('schema_created_at', datetime('now'));

-- +goose Down
DROP TABLE app_meta;
