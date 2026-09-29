-- +goose Up
CREATE TABLE account_bundles (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    token TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    state JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (length(id) BETWEEN 1 AND 80),
    CHECK (length(token_hash) = 64),
    CHECK (jsonb_typeof(state) = 'object')
);

-- +goose Down
DROP TABLE account_bundles;
