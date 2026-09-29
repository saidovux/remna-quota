-- +goose Up
ALTER TABLE account_bundles ADD COLUMN next_sync_at timestamptz NOT NULL DEFAULT 'epoch';
CREATE INDEX account_bundles_sync_due_idx ON account_bundles(next_sync_at, id COLLATE "C");

-- +goose Down
DROP INDEX account_bundles_sync_due_idx;
ALTER TABLE account_bundles DROP COLUMN next_sync_at;
