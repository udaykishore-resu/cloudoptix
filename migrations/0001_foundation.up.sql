-- 0001_foundation: extensions and the helper functions every later migration
-- builds on.
--
-- Two decisions worth recording:
--
--  1. Row-level security is applied through cloudoptix_enable_tenant_rls(),
--     one function called once per table, rather than four lines of DDL
--     copy-pasted forty times. Every tenant-scoped table in this schema
--     carries a `tenant_id TEXT NOT NULL` column and gets RLS enabled with a
--     policy keyed on current_setting('cloudoptix.tenant_id'). RLS here is
--     defence in depth, not the primary guard: internal/adapters/postgres
--     calls core.GuardTenant on every method before it touches the database,
--     and every query is already parameterised with $1=tenant_id. RLS exists
--     for the failure mode where a query is missing that WHERE clause — a
--     bug in a future migration, a raw psql session during an incident, a
--     forgotten filter in a new repository method — because relying on
--     either layer alone has a real failure mode: the application guard is
--     bypassed by any code path that reaches the database directly (a
--     migration, an analyst's psql session, a future service that shares the
--     connection pool), and RLS alone is bypassed by anyone with BYPASSRLS
--     or table ownership, and enforces nothing if the *session variable*
--     itself is wrong — which is exactly the class of bug the application
--     guard catches first. Two independent, differently-shaped checks are
--     required before either one's blind spot matters.
--
--  2. current_setting(..., true) is used everywhere (the `missing_ok` form),
--     never the raising form. A connection that never called
--     DB.WithTenant(ctx, tenantID) has cloudoptix.tenant_id unset;
--     current_setting(..., true) then returns NULL, and `tenant_id = NULL`
--     is never true in SQL, so the row set is empty. The alternative —
--     letting current_setting raise when unset — sounds stricter but isn't:
--     it turns "someone forgot to scope the session" into an error message
--     that a caller can catch and mishandle, instead of the silent,
--     unconditionally-safe empty result set a security boundary should fail
--     into.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- cloudoptix_current_tenant reads the session's tenant scope, set once per
-- transaction by DB.WithTenant. STABLE (not IMMUTABLE) because the same call
-- can return different values across transactions, but IS constant within
-- one — which is what lets the planner use it safely in an index condition.
CREATE OR REPLACE FUNCTION cloudoptix_current_tenant() RETURNS text
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('cloudoptix.tenant_id', true), '');
$$;

-- cloudoptix_system_scope names the narrow, explicit escape hatch a handful
-- of ports methods legitimately need: ExecutionRepository.ClaimDuePlans,
-- ClaimPlansAwaitingValidation, ApprovalRepository.ExpireOverdue and
-- NotificationRepository.ClaimPending all poll a queue *across every
-- tenant* by design — one worker claims the oldest due work platform-wide,
-- and there is no single tenant to scope the session to. Rather than
-- running these through a second database role with BYPASSRLS (which would
-- make RLS meaningless for whatever query accidentally runs on that
-- connection), the Go call path sets the same session variable everything
-- else uses to a reserved sentinel — '__cloudoptix_system__' — via
-- db.WithSystemScope(ctx), used only inside the implementations of those
-- four methods. The sentinel cannot collide with a real tenant: it starts
-- with an underscore, and core.TenantID.Validate() (internal/domain/core)
-- requires the first character to be alphanumeric. A repository method that
-- forgets to call WithTenant at all still fails closed to an empty result —
-- current_setting returns NULL, which matches neither a real tenant_id nor
-- the sentinel — so the only way to reach cross-tenant visibility is the
-- one call this comment names, not an omission.
CREATE OR REPLACE FUNCTION cloudoptix_system_scope() RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT current_setting('cloudoptix.tenant_id', true) = '__cloudoptix_system__';
$$;

-- cloudoptix_enable_tenant_rls turns on RLS for a table that has a plain
-- `tenant_id` column and installs the single policy every such table uses.
-- FORCE ROW LEVEL SECURITY matters as much as ENABLE: without it the table
-- owner (the role migrations run as, which is often also the application's
-- connection role in a small deployment) is exempt from its own policies.
CREATE OR REPLACE FUNCTION cloudoptix_enable_tenant_rls(tbl regclass) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tbl);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tbl);
  EXECUTE format(
    'CREATE POLICY tenant_isolation ON %s USING (tenant_id = cloudoptix_current_tenant() OR cloudoptix_system_scope()) WITH CHECK (tenant_id = cloudoptix_current_tenant() OR cloudoptix_system_scope())',
    tbl
  );
END;
$$;

-- cloudoptix_set_updated_at is the generic trigger body for every table that
-- carries an updated_at column, so "touch the timestamp on write" is not
-- reimplemented per table and cannot be forgotten on a new one.
CREATE OR REPLACE FUNCTION cloudoptix_set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;

-- cloudoptix_attach_updated_at wires the trigger above onto one table.
CREATE OR REPLACE FUNCTION cloudoptix_attach_updated_at(tbl regclass) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format(
    'CREATE TRIGGER trg_updated_at BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION cloudoptix_set_updated_at()',
    tbl
  );
END;
$$;

-- schema_migrations records applied migration versions. The migration
-- runner (internal/adapters/postgres/migrate.go) takes a Postgres advisory
-- lock keyed on this table's OID before reading or writing it, so two API
-- pods booting at once serialise instead of racing to apply the same
-- version twice.
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     BIGINT PRIMARY KEY,
    name        TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
