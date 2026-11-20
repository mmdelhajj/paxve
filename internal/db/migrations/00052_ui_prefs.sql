-- Appearance ("comfort for the eyes"): each user's look of the app (theme comfort / classic / dark / high contrast,
-- text size normal / large / extra large, row density compact / comfortable), and the organisation's default for
-- users who have not chosen one. Same Magic layout; only colours, sizes and spacing change.

-- +goose Up
ALTER TABLE users   ADD COLUMN ui_prefs   jsonb;
ALTER TABLE tenants ADD COLUMN ui_default jsonb;
GRANT UPDATE (ui_default) ON tenants TO paxve_app;

-- +goose Down
REVOKE UPDATE (ui_default) ON tenants FROM paxve_app;
ALTER TABLE tenants DROP COLUMN ui_default;
ALTER TABLE users   DROP COLUMN ui_prefs;
