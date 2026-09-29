-- +goose Up
ALTER TABLE account_bundles ADD COLUMN external_ref text NOT NULL DEFAULT '';
UPDATE account_bundles SET external_ref=COALESCE(state->>'external_ref','');
CREATE INDEX account_bundles_directory_idx ON account_bundles (id COLLATE "C");
CREATE INDEX account_bundles_external_ref_idx ON account_bundles (external_ref, id COLLATE "C");

-- +goose Down
DROP INDEX account_bundles_external_ref_idx;
DROP INDEX account_bundles_directory_idx;
ALTER TABLE account_bundles DROP COLUMN external_ref;
