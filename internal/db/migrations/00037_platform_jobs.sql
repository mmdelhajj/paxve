-- +goose Up
-- Plan apps are enforced: an organisation may use the core apps, the apps of its plan (NULL = every app) and
-- the apps it already had switched on when its plan's list was narrowed (grandfathered, kept until the owner
-- removes them). Background jobs skip expired and blocked organisations.

ALTER TABLE tenants ADD COLUMN grandfathered_apps text[] NOT NULL DEFAULT '{}';
-- the platform owner's own organisation (set by the server from PLATFORM_ADMINS): its jobs always run
ALTER TABLE tenants ADD COLUMN platform_owner boolean NOT NULL DEFAULT false;

-- +goose StatementBegin
CREATE FUNCTION tenant_jobs_ok(tid uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT coalesce((SELECT t.platform_owner OR tenant_state(t) NOT IN ('expired', 'blocked') FROM tenants t WHERE t.id = tid), false)
$$;
-- +goose StatementEnd
GRANT EXECUTE ON FUNCTION tenant_jobs_ok(uuid) TO paxve_app;

-- +goose StatementBegin
-- grandfather_plan_apps keeps the apps an organisation already uses when its plan no longer includes them
-- (p_code NULL = every plan). Returns how many organisations changed.
CREATE FUNCTION grandfather_plan_apps(p_code text) RETURNS int
LANGUAGE plpgsql AS $$
DECLARE n int;
BEGIN
  UPDATE tenants t SET grandfathered_apps = g.apps
  FROM (
    SELECT t2.id, array_agg(DISTINCT x.a ORDER BY x.a) AS apps
    FROM tenants t2 JOIN platform_plans pp ON pp.code = t2.plan
    CROSS JOIN LATERAL (
      SELECT unnest(t2.grandfathered_apps) AS a
      UNION SELECT unnest(c.apps) FROM companies c WHERE c.tenant_id = t2.id AND c.is_active) x
    WHERE pp.apps IS NOT NULL AND (p_code IS NULL OR t2.plan = p_code)
      AND x.a <> ALL (pp.apps) AND x.a NOT IN ('gen', 'acc', 'col')
    GROUP BY t2.id) g
  WHERE t.id = g.id AND t.grandfathered_apps IS DISTINCT FROM g.apps;
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END $$;
-- +goose StatementEnd

SELECT grandfather_plan_apps(NULL);

-- +goose Down
DROP FUNCTION IF EXISTS grandfather_plan_apps(text);
DROP FUNCTION IF EXISTS tenant_jobs_ok(uuid);
ALTER TABLE tenants DROP COLUMN IF EXISTS grandfathered_apps;
ALTER TABLE tenants DROP COLUMN IF EXISTS platform_owner;
