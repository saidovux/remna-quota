-- +goose Up
CREATE TABLE subscription_bundles (
    id TEXT PRIMARY KEY,
    external_ref TEXT NOT NULL DEFAULT '',
    token TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    state JSONB NOT NULL,
    CHECK (length(id) BETWEEN 1 AND 80),
    CHECK (length(token_hash) = 64),
    CHECK (jsonb_typeof(state) = 'object')
);
CREATE INDEX subscription_bundles_external_ref_id ON subscription_bundles (external_ref, id COLLATE "C");
CREATE INDEX subscription_bundles_id_cursor ON subscription_bundles (id COLLATE "C");

-- +goose Down
DROP TABLE subscription_bundles;
