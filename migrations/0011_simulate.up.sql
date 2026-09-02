-- 0011_simulate: architecture mutation, counterfactuals and the cost
-- compiler's outputs, plus the cost-regression test suites CI runs against
-- the compiler.
CREATE TABLE architecture_simulations (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name           TEXT NOT NULL DEFAULT '',
    scope          TEXT NOT NULL DEFAULT '',
    scope_id       TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL CHECK (kind IN
                      ('architecture_mutation', 'counterfactual', 'cost_compiler')),
    baseline_cost_micros BIGINT NOT NULL DEFAULT 0,
    currency       TEXT NOT NULL DEFAULT 'USD',
    weights        JSONB NOT NULL DEFAULT '{}'::jsonb,
    assumptions    JSONB NOT NULL DEFAULT '[]'::jsonb,
    requested_by   TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT '',
    error          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ
);

CREATE INDEX idx_simulations_tenant_created ON architecture_simulations (tenant_id, created_at DESC);
SELECT cloudoptix_enable_tenant_rls('architecture_simulations');

-- simulation_candidates is normalized out of Simulation.Candidates into its
-- own table (rather than a JSONB array on the parent) because candidates are
-- individually meaningful rows a reviewer compares side by side, and the
-- mutation engine writes them once and never again — no update-in-place
-- reason to keep them embedded.
CREATE TABLE simulation_candidates (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    simulation_id         TEXT NOT NULL REFERENCES architecture_simulations(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL DEFAULT '',
    summary               TEXT NOT NULL DEFAULT '',
    pattern               TEXT NOT NULL DEFAULT '',
    changes               JSONB NOT NULL DEFAULT '[]'::jsonb,
    current_monthly_cost_micros   BIGINT NOT NULL DEFAULT 0,
    projected_monthly_cost_micros BIGINT NOT NULL DEFAULT 0,
    monthly_delta_micros  BIGINT NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD',
    savings_pct           DOUBLE PRECISION NOT NULL DEFAULT 0,
    scores                JSONB NOT NULL DEFAULT '[]'::jsonb,
    composite_score       DOUBLE PRECISION NOT NULL DEFAULT 0,
    assumptions           JSONB NOT NULL DEFAULT '[]'::jsonb,
    risks                 JSONB NOT NULL DEFAULT '[]'::jsonb,
    blockers              JSONB NOT NULL DEFAULT '[]'::jsonb,
    migration_steps       JSONB NOT NULL DEFAULT '[]'::jsonb,
    confidence            DOUBLE PRECISION NOT NULL DEFAULT 0,
    recommended           BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX idx_simulation_candidates_simulation ON simulation_candidates (simulation_id, composite_score DESC);
SELECT cloudoptix_enable_tenant_rls('simulation_candidates');

CREATE TABLE counterfactuals (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scenario            JSONB NOT NULL DEFAULT '{}'::jsonb,
    question            TEXT NOT NULL DEFAULT '',
    current_state       JSONB NOT NULL DEFAULT '{}'::jsonb,
    proposed_state      JSONB NOT NULL DEFAULT '{}'::jsonb,
    cost_delta_micros   BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    cost_delta_pct      DOUBLE PRECISION NOT NULL DEFAULT 0,
    annual_cost_delta_micros BIGINT NOT NULL DEFAULT 0,
    performance_delta   TEXT NOT NULL DEFAULT '',
    reliability_delta   TEXT NOT NULL DEFAULT '',
    security_delta      TEXT NOT NULL DEFAULT '',
    risk                TEXT NOT NULL DEFAULT 'NONE',
    confidence          DOUBLE PRECISION NOT NULL DEFAULT 0,
    assumptions         JSONB NOT NULL DEFAULT '[]'::jsonb,
    caveats             JSONB NOT NULL DEFAULT '[]'::jsonb,
    narrative           TEXT NOT NULL DEFAULT '',
    computed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_counterfactuals_tenant_computed ON counterfactuals (tenant_id, computed_at DESC);
SELECT cloudoptix_enable_tenant_rls('counterfactuals');

CREATE TABLE compilations (
    id                     TEXT PRIMARY KEY,
    tenant_id              TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source                 TEXT NOT NULL CHECK (source IN
                              ('terraform_plan', 'terraform_hcl', 'cloudformation', 'kubernetes_manifest',
                               'helm_release', 'live_topology')),
    label                  TEXT NOT NULL DEFAULT '',
    changes                JSONB NOT NULL DEFAULT '[]'::jsonb,
    baseline_monthly_micros  BIGINT NOT NULL DEFAULT 0,
    projected_monthly_micros BIGINT NOT NULL DEFAULT 0,
    monthly_delta_micros     BIGINT NOT NULL DEFAULT 0,
    currency               TEXT NOT NULL DEFAULT 'USD',
    delta_pct              DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_count          INT NOT NULL DEFAULT 0,
    updated_count          INT NOT NULL DEFAULT 0,
    deleted_count          INT NOT NULL DEFAULT 0,
    unpriced_count         INT NOT NULL DEFAULT 0,
    coverage               DOUBLE PRECISION NOT NULL DEFAULT 0,
    confidence             DOUBLE PRECISION NOT NULL DEFAULT 0,
    assumptions            JSONB NOT NULL DEFAULT '[]'::jsonb,
    risks                  JSONB NOT NULL DEFAULT '[]'::jsonb,
    opportunities          JSONB NOT NULL DEFAULT '[]'::jsonb,
    pricing_date           TIMESTAMPTZ,
    compiled_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_ms            BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX idx_compilations_tenant_compiled ON compilations (tenant_id, compiled_at DESC);
SELECT cloudoptix_enable_tenant_rls('compilations');

CREATE TABLE regression_suites (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    version    INT NOT NULL DEFAULT 1,
    checks     JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

SELECT cloudoptix_enable_tenant_rls('regression_suites');

CREATE TABLE regression_reports (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    compilation_id   TEXT NOT NULL REFERENCES compilations(id) ON DELETE CASCADE,
    suite_name       TEXT NOT NULL DEFAULT '',
    verdict          TEXT NOT NULL CHECK (verdict IN ('PASS', 'WARNING', 'FAIL')),
    results          JSONB NOT NULL DEFAULT '[]'::jsonb,
    monthly_delta_micros BIGINT NOT NULL DEFAULT 0,
    annual_delta_micros  BIGINT NOT NULL DEFAULT 0,
    currency         TEXT NOT NULL DEFAULT 'USD',
    summary          TEXT NOT NULL DEFAULT '',
    required_action  TEXT NOT NULL DEFAULT '',
    evaluated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_regression_reports_compilation ON regression_reports (compilation_id);
SELECT cloudoptix_enable_tenant_rls('regression_reports');
