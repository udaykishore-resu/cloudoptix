-- 0008_optimize: findings, the recommendations built from them, and the
-- optimization rule catalogue.
--
-- optimization_rules has no corresponding Go repository: internal/ports has
-- no RuleRepository, because rule logic in this codebase is compiled Go
-- (internal/domain/optimize and its sibling rule packages), not
-- data-driven. This table exists for the operational metadata around a rule
-- — its category, whether it is enabled for a tenant, and a place for the
-- learning loop's calibration to join against by rule_id for reporting —
-- and is written by operational tooling and migrations, not by application
-- code, hence no ON UPDATE trigger tied to a repository method.
--
-- findings and recommendations are split into two tables even though
-- Recommendation embeds exactly one Finding, because findings are the
-- deterministic, evidence-bearing fact ("this volume has been unattached
-- for 41 days") and recommendations are the derived judgment call
-- ("delete it, here is the risk and the rollback story") — keeping them
-- separate preserves every finding CloudOptix ever produced, including ones
-- a later stage decided not to promote into a recommendation at all, which
-- is what lets someone audit "why didn't this get flagged" after the fact.
CREATE TABLE optimization_rules (
    id          TEXT PRIMARY KEY, -- optimize.RuleID
    category    TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    definition  JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

SELECT cloudoptix_attach_updated_at('optimization_rules');
-- Not tenant-scoped: the rule catalogue is platform-wide. Per-tenant
-- enable/disable and calibration live on rule_calibrations
-- (0010_execute.up.sql), which IS tenant-scoped.

CREATE TABLE findings (
    id                        TEXT PRIMARY KEY,
    tenant_id                 TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    rule_id                   TEXT NOT NULL,
    rule_name                 TEXT NOT NULL DEFAULT '',
    category                  TEXT NOT NULL,
    resource_id               TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    resource_name             TEXT NOT NULL DEFAULT '',
    resource_kind             TEXT NOT NULL DEFAULT '',
    account_id                TEXT NOT NULL DEFAULT '',
    region                    TEXT NOT NULL DEFAULT '',
    environment               TEXT NOT NULL DEFAULT 'unknown',
    severity                  TEXT NOT NULL CHECK (severity IN ('INFO', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    summary                   TEXT NOT NULL DEFAULT '',
    detail                    TEXT NOT NULL DEFAULT '',
    evidence                  JSONB NOT NULL DEFAULT '[]'::jsonb,
    current_monthly_cost_micros     BIGINT NOT NULL DEFAULT 0,
    estimated_monthly_saving_micros BIGINT NOT NULL DEFAULT 0,
    currency                  TEXT NOT NULL DEFAULT 'USD',
    detected_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_findings_tenant_resource ON findings (tenant_id, resource_id);
CREATE INDEX idx_findings_tenant_rule ON findings (tenant_id, rule_id, detected_at DESC);
SELECT cloudoptix_enable_tenant_rls('findings');

CREATE TABLE recommendations (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    finding_id        TEXT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,

    title             TEXT NOT NULL DEFAULT '',
    rationale         TEXT NOT NULL DEFAULT '',
    action            TEXT NOT NULL,
    parameters        JSONB NOT NULL DEFAULT '{}'::jsonb,

    current_state     JSONB NOT NULL DEFAULT '{}'::jsonb,
    proposed_state    JSONB NOT NULL DEFAULT '{}'::jsonb,

    estimated_monthly_saving_micros BIGINT NOT NULL DEFAULT 0,
    estimated_annual_saving_micros  BIGINT NOT NULL DEFAULT 0,
    implementation_cost_micros      BIGINT NOT NULL DEFAULT 0,
    currency          TEXT NOT NULL DEFAULT 'USD',
    payback_days      DOUBLE PRECISION NOT NULL DEFAULT 0,

    confidence        DOUBLE PRECISION NOT NULL DEFAULT 0,
    confidence_basis  JSONB NOT NULL DEFAULT '[]'::jsonb,
    risk              JSONB NOT NULL DEFAULT '{}'::jsonb,
    blast_radius      JSONB NOT NULL DEFAULT '{}'::jsonb,
    reversibility     TEXT NOT NULL DEFAULT 'fast' CHECK (reversibility IN ('instant', 'fast', 'slow', 'none')),
    complexity        TEXT NOT NULL DEFAULT 'low' CHECK (complexity IN
                        ('trivial', 'low', 'medium', 'high', 'project')),

    priority_score    DOUBLE PRECISION NOT NULL DEFAULT 0,
    rank              INT NOT NULL DEFAULT 0,

    status            TEXT NOT NULL CHECK (status IN
                        ('open', 'under_review', 'approved', 'rejected', 'scheduled', 'executing',
                         'executed', 'validating', 'validated', 'failed', 'rolled_back', 'superseded',
                         'snoozed', 'dismissed')),
    status_reason     TEXT NOT NULL DEFAULT '',
    snoozed_until     TIMESTAMPTZ,

    requires_approval BOOLEAN NOT NULL DEFAULT true,
    policy_decision_id TEXT,
    auto_executable   BOOLEAN NOT NULL DEFAULT false,

    narrative         TEXT NOT NULL DEFAULT '',
    maintenance_window TEXT NOT NULL DEFAULT '',
    supersedes_id     TEXT REFERENCES recommendations(id) ON DELETE SET NULL,

    -- Denormalized straight off the embedded Finding, so
    -- RecommendationFilter (environments, account_ids, application_id,
    -- resource_id) and the priority-ordered dashboard list never have to
    -- join back to findings/resources for the columns every list query
    -- filters or sorts on.
    account_id        TEXT NOT NULL DEFAULT '',
    application_id    TEXT NOT NULL DEFAULT '',
    resource_id       TEXT NOT NULL DEFAULT '',
    environment       TEXT NOT NULL DEFAULT 'unknown',
    category          TEXT NOT NULL DEFAULT '',
    rule_id           TEXT NOT NULL DEFAULT '',

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The dashboard's primary query: "open recommendations for this tenant,
-- highest priority first" — exactly (tenant, status, priority_score DESC).
CREATE INDEX idx_recommendations_tenant_status_priority
    ON recommendations (tenant_id, status, priority_score DESC);
CREATE INDEX idx_recommendations_tenant_application ON recommendations (tenant_id, application_id)
    WHERE application_id <> '';
CREATE INDEX idx_recommendations_tenant_resource ON recommendations (tenant_id, resource_id);
SELECT cloudoptix_attach_updated_at('recommendations');
SELECT cloudoptix_enable_tenant_rls('recommendations');
