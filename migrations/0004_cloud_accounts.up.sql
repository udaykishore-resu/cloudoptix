-- 0004_cloud_accounts: onboarded AWS accounts, the environment vocabulary,
-- and the business-facing groupings (applications, workloads) that
-- resources get attributed to.
--
-- environments is deliberately not a foreign key target. core.Environment
-- (internal/domain/core/identity.go) is a closed, code-defined enumeration —
-- production, staging, development, test, sandbox, dr, unknown — normalized
-- by NormalizeEnvironment() so it never needs a tenant to add a value. This
-- table exists only because the task's schema list names it and because it
-- is a convenient place to hang the one thing that IS per-tenant about an
-- environment: a human label and whether the tenant's own policy treats it
-- as production-grade. Every other table stores the environment code
-- directly as a CHECK-constrained TEXT column rather than an FK into this
-- table, because coupling every resource/workload/account write to a
-- per-tenant environment catalogue that no domain repository manages would
-- be a foreign key with no writer on the other end.
CREATE TABLE environments (
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code          TEXT NOT NULL CHECK (code IN
                    ('production', 'staging', 'development', 'test', 'sandbox', 'dr', 'unknown')),
    label         TEXT NOT NULL DEFAULT '',
    is_production BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, code)
);

SELECT cloudoptix_enable_tenant_rls('environments');

CREATE TABLE aws_accounts (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    account_id         TEXT NOT NULL,
    alias              TEXT NOT NULL DEFAULT '',
    environment        TEXT NOT NULL CHECK (environment IN
                          ('production', 'staging', 'development', 'test', 'sandbox', 'dr', 'unknown')),
    regions            JSONB NOT NULL DEFAULT '[]'::jsonb,
    access_mode        TEXT NOT NULL CHECK (access_mode IN ('assume_role', 'simulated')),
    role_arns          JSONB NOT NULL DEFAULT '{}'::jsonb, -- RoleScope -> ARN
    external_id        TEXT NOT NULL DEFAULT '',
    session_prefix     TEXT NOT NULL DEFAULT '',
    state              TEXT NOT NULL CHECK (state IN
                          ('pending', 'verifying', 'connected', 'degraded', 'failed', 'suspended')),
    state_reason       TEXT NOT NULL DEFAULT '',
    granted_scopes     JSONB NOT NULL DEFAULT '[]'::jsonb,
    missing_actions    JSONB NOT NULL DEFAULT '[]'::jsonb,
    is_payer           BOOLEAN NOT NULL DEFAULT false,
    cur_bucket         TEXT NOT NULL DEFAULT '',
    cur_prefix         TEXT NOT NULL DEFAULT '',
    connected_at       TIMESTAMPTZ,
    last_verified_at   TIMESTAMPTZ,
    last_discovery_at  TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, account_id)
);

CREATE INDEX idx_aws_accounts_tenant ON aws_accounts (tenant_id);
SELECT cloudoptix_attach_updated_at('aws_accounts');
SELECT cloudoptix_enable_tenant_rls('aws_accounts');

CREATE TABLE applications (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    slug          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    business_unit TEXT NOT NULL DEFAULT '',
    domain        TEXT NOT NULL DEFAULT '',
    criticality   TEXT NOT NULL DEFAULT 'UNSET' CHECK (criticality IN
                     ('TIER_0', 'TIER_1', 'TIER_2', 'TIER_3', 'UNSET')),
    owner         TEXT NOT NULL DEFAULT '',
    environments  JSONB NOT NULL DEFAULT '[]'::jsonb,
    match_rules   JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug)
);

CREATE INDEX idx_applications_tenant ON applications (tenant_id);
SELECT cloudoptix_attach_updated_at('applications');
SELECT cloudoptix_enable_tenant_rls('applications');

CREATE TABLE workloads (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    type           TEXT NOT NULL,
    platform       TEXT NOT NULL DEFAULT 'unknown',
    environment    TEXT NOT NULL CHECK (environment IN
                      ('production', 'staging', 'development', 'test', 'sandbox', 'dr', 'unknown')),
    criticality    TEXT NOT NULL DEFAULT 'UNSET' CHECK (criticality IN
                      ('TIER_0', 'TIER_1', 'TIER_2', 'TIER_3', 'UNSET')),
    owner          TEXT NOT NULL DEFAULT '',
    team           TEXT NOT NULL DEFAULT '',
    cluster        TEXT NOT NULL DEFAULT '',
    namespace      TEXT NOT NULL DEFAULT '',
    match_rules    JSONB NOT NULL DEFAULT '[]'::jsonb,
    slo            JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workloads_tenant_app ON workloads (tenant_id, application_id);
SELECT cloudoptix_attach_updated_at('workloads');
SELECT cloudoptix_enable_tenant_rls('workloads');
