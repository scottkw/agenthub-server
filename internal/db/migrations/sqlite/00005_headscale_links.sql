-- +goose Up

CREATE TABLE headscale_user_links (
    account_id          TEXT NOT NULL REFERENCES accounts(id),
    user_id             TEXT NOT NULL REFERENCES users(id),
    headscale_user_id   INTEGER NOT NULL,
    headscale_user_name TEXT NOT NULL,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (account_id, user_id)
);

CREATE UNIQUE INDEX idx_headscale_user_links_hs_id ON headscale_user_links(headscale_user_id);

-- +goose Down
DROP TABLE headscale_user_links;
