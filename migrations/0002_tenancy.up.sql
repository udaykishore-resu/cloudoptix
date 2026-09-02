-- 0002_tenancy: the platform's isolation boundary (tenants) and the
-- identities that operate inside it (users, memberships) and the customer
-- company records a tenant contains (organizations).
--
-- tenants.id doubles as the tenant scope value itself, so its RLS policy
-- compares `id`, not a separate `tenant_id` column — a tenant is not scoped
-- to another tenant, it IS the scope. users carries no tenant_id at all: a
-- person (one OIDC subject) can hold memberships in several tenants
-- simultaneously (REQ-TEN, "a consultant working across three customers has
-- one identity and three memberships"), so the user row itself belongs to no
-- single tenant and RLS does not apply to it — isolation instead lives on
-- memberships, which is the table every tenant-scoped query actually joins
-- through.
CREATE TABLE tenants (
    id                   TEXT PRIMARY KEY,
    slug                 TEXT NOT NULL UNIQUE,
    name                 TEXT NOT NULL,
    plan                 TEXT NOT NULL CHECK (plan IN ('trial', 'standard', 'enterprise', 'internal')),
    quotas               JSONB NOT NULL DEFAULT '{}'::jsonb,
    state                TEXT NOT NULL CHECK (state IN ('onboarding', 'active', 'suspended', 'archived')),
    spec_id              TEXT,
    active_spec_version  INT NOT NULL DEFAULT 0,
    active_policy_id     TEXT,
    demo                 BOOLEAN NOT NULL DEFAULT false,
    data_region          TEXT NOT NULL DEFAULT '',
    encryption_key_arn   TEXT NOT NULL DEFAULT '',
    primary_contact      TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at         TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

SELECT cloudoptix_attach_updated_at('tenants');

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants
    USING (id = cloudoptix_current_tenant() OR cloudoptix_system_scope())
    WITH CHECK (id = cloudoptix_current_tenant() OR cloudoptix_system_scope());
-- Platform-admin support flows (TenantRepository.Create for a tenant that by
-- definition has no session scoped to it yet, and List for cross-tenant
-- listing) run under the cloudoptix_system_scope() sentinel described in
-- 0001_foundation.up.sql, via db.WithSystemScope(ctx) — see
-- internal/adapters/postgres/db.go.

CREATE TABLE organizations (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    industry         TEXT NOT NULL DEFAULT '',
    size             TEXT NOT NULL DEFAULT '',
    business_regions JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_organizations_tenant ON organizations (tenant_id);
SELECT cloudoptix_attach_updated_at('organizations');
SELECT cloudoptix_enable_tenant_rls('organizations');

-- users is the global identity record; see the header comment for why it is
-- not tenant-scoped or RLS-protected.
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    subject       TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    last_login_at TIMESTAMPTZ,
    disabled      BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_users_email ON users (lower(email));
SELECT cloudoptix_attach_updated_at('users');

-- memberships is the actual tenant-isolation boundary for user access: a
-- row exists for every (user, tenant) grant, is tenant-scoped and RLS
-- protected, and is where "does this session's tenant know this user"
-- questions are answered from.
CREATE TABLE memberships (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    roles      JSONB NOT NULL DEFAULT '[]'::jsonb,
    team       TEXT NOT NULL DEFAULT '',
    granted_by TEXT NOT NULL DEFAULT '',
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    UNIQUE (tenant_id, user_id)
);

CREATE INDEX idx_memberships_user ON memberships (user_id);
CREATE INDEX idx_memberships_tenant ON memberships (tenant_id);
SELECT cloudoptix_enable_tenant_rls('memberships');
