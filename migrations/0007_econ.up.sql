-- 0007_econ: Architecture Economics — footprints, business transactions and
-- their unit economics, cost SLOs and error budgets, and the Cloud
-- Efficiency Score.
CREATE TABLE business_transactions (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    application_id TEXT REFERENCES applications(id) ON DELETE SET NULL,
    workload_ids   JSONB NOT NULL DEFAULT '[]'::jsonb,
    path_share     JSONB NOT NULL DEFAULT '{}'::jsonb,
    volume_source  JSONB NOT NULL DEFAULT '{}'::jsonb,
    provenance     TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (provenance IN
                      ('CONFIRMED', 'INFERRED', 'UNKNOWN', 'REQUIRES_USER_CONFIRMATION')),
    criticality    TEXT NOT NULL DEFAULT 'UNSET' CHECK (criticality IN
                      ('TIER_0', 'TIER_1', 'TIER_2', 'TIER_3', 'UNSET')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX idx_business_transactions_tenant ON business_transactions (tenant_id);
SELECT cloudoptix_attach_updated_at('business_transactions');
SELECT cloudoptix_enable_tenant_rls('business_transactions');

CREATE TABLE unit_economics (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    transaction_id        TEXT NOT NULL REFERENCES business_transactions(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL,
    period_start          TIMESTAMPTZ NOT NULL,
    period_end            TIMESTAMPTZ NOT NULL,
    volume                DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_cost_micros     BIGINT NOT NULL DEFAULT 0,
    cost_per_unit_micros  BIGINT NOT NULL DEFAULT 0,
    direct_per_unit_micros BIGINT NOT NULL DEFAULT 0,
    shared_per_unit_micros BIGINT NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD',
    prior_cost_per_unit_micros BIGINT NOT NULL DEFAULT 0,
    change_pct            DOUBLE PRECISION NOT NULL DEFAULT 0,
    drivers               JSONB NOT NULL DEFAULT '[]'::jsonb,
    confidence            DOUBLE PRECISION NOT NULL DEFAULT 0,
    volume_provenance     TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (volume_provenance IN
                             ('CONFIRMED', 'INFERRED', 'UNKNOWN', 'REQUIRES_USER_CONFIRMATION')),
    computed_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ListUnitEconomics(tenant, transactionID, from, to) is the read path: the
-- trend line behind every unit-economics chart.
CREATE INDEX idx_unit_economics_tenant_tx_period ON unit_economics (tenant_id, transaction_id, period_start DESC);
SELECT cloudoptix_enable_tenant_rls('unit_economics');

CREATE TABLE economic_footprints (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scope          TEXT NOT NULL,
    scope_id       TEXT NOT NULL DEFAULT '',
    label          TEXT NOT NULL DEFAULT '',
    period_start   TIMESTAMPTZ NOT NULL,
    period_end     TIMESTAMPTZ NOT NULL,

    direct_micros       BIGINT NOT NULL DEFAULT 0,
    indirect_micros     BIGINT NOT NULL DEFAULT 0,
    shared_micros       BIGINT NOT NULL DEFAULT 0,
    total_micros        BIGINT NOT NULL DEFAULT 0,
    unattributed_micros BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    coverage            DOUBLE PRECISION NOT NULL DEFAULT 0,

    -- Components can run to hundreds of rows per footprint (one per
    -- contributing resource); it is written and read whole with the
    -- footprint and never filtered by a single component in SQL, so it is
    -- one JSONB array rather than a child table.
    components     JSONB NOT NULL DEFAULT '[]'::jsonb,
    by_service     JSONB NOT NULL DEFAULT '{}'::jsonb,
    by_class       JSONB NOT NULL DEFAULT '{}'::jsonb,

    prior_total_micros BIGINT NOT NULL DEFAULT 0,
    change_pct     DOUBLE PRECISION NOT NULL DEFAULT 0,
    computed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    confidence     DOUBLE PRECISION NOT NULL DEFAULT 0,
    UNIQUE (tenant_id, scope, scope_id, period_start, period_end)
);

CREATE INDEX idx_econ_footprints_tenant_scope_period ON economic_footprints (tenant_id, scope, period_start DESC);
SELECT cloudoptix_enable_tenant_rls('economic_footprints');

CREATE TABLE cost_slos (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL CHECK (kind IN
                        ('absolute_spend', 'cost_per_transaction', 'cost_per_request', 'cost_per_customer', 'waste_ratio', 'efficiency_score')),
    direction         TEXT NOT NULL CHECK (direction IN ('at_most', 'at_least')),
    scope             TEXT NOT NULL,
    scope_id          TEXT NOT NULL DEFAULT '',
    transaction_id    TEXT REFERENCES business_transactions(id) ON DELETE SET NULL,
    target_micros     BIGINT NOT NULL DEFAULT 0,
    target_currency   TEXT NOT NULL DEFAULT 'USD',
    target_ratio      DOUBLE PRECISION NOT NULL DEFAULT 0,
    window_kind       TEXT NOT NULL CHECK (window_kind IN
                        ('calendar_month', 'rolling_30d', 'rolling_7d', 'calendar_quarter')),
    error_budget_pct  DOUBLE PRECISION NOT NULL DEFAULT 0.05,
    breach_actions    JSONB NOT NULL DEFAULT '[]'::jsonb,
    owner             TEXT NOT NULL DEFAULT '',
    enabled           BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_cost_slos_tenant ON cost_slos (tenant_id);
SELECT cloudoptix_attach_updated_at('cost_slos');
SELECT cloudoptix_enable_tenant_rls('cost_slos');

CREATE TABLE economic_error_budgets (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    slo_id                TEXT NOT NULL REFERENCES cost_slos(id) ON DELETE CASCADE,
    slo_name              TEXT NOT NULL DEFAULT '',
    kind                  TEXT NOT NULL,
    period_start          TIMESTAMPTZ NOT NULL,
    period_end            TIMESTAMPTZ NOT NULL,
    target_micros         BIGINT NOT NULL DEFAULT 0,
    budget_amount_micros  BIGINT NOT NULL DEFAULT 0,
    actual_micros         BIGINT NOT NULL DEFAULT 0,
    consumed_micros       BIGINT NOT NULL DEFAULT 0,
    remaining_micros      BIGINT NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD',
    consumed_ratio        DOUBLE PRECISION NOT NULL DEFAULT 0,
    burn_rate             DOUBLE PRECISION NOT NULL DEFAULT 0,
    projected_eow_micros  BIGINT NOT NULL DEFAULT 0,
    projected_overage_micros BIGINT NOT NULL DEFAULT 0,
    exhaustion_date       TIMESTAMPTZ,
    state                 TEXT NOT NULL CHECK (state IN
                            ('healthy', 'watch', 'at_risk', 'exhausted', 'breached', 'unknown')),
    triggered_actions     JSONB NOT NULL DEFAULT '[]'::jsonb,
    explanation           TEXT NOT NULL DEFAULT '',
    evaluated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slo_id, period_start)
);

-- The policy engine and the SLO dashboard both want "every budget's current
-- state", i.e. the latest evaluation per SLO — the ORDER BY DESC on
-- evaluated_at is what ListBudgetStates' "most recent per SLO" query rides.
CREATE INDEX idx_error_budgets_tenant_slo ON economic_error_budgets (tenant_id, slo_id, evaluated_at DESC);
SELECT cloudoptix_enable_tenant_rls('economic_error_budgets');

CREATE TABLE efficiency_scores (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scope             TEXT NOT NULL,
    scope_id          TEXT NOT NULL DEFAULT '',
    label             TEXT NOT NULL DEFAULT '',
    period_start      TIMESTAMPTZ NOT NULL,
    period_end        TIMESTAMPTZ NOT NULL,
    score             DOUBLE PRECISION NOT NULL DEFAULT 0,
    grade             TEXT NOT NULL DEFAULT 'F',
    factors           JSONB NOT NULL DEFAULT '[]'::jsonb,
    prior_score       DOUBLE PRECISION NOT NULL DEFAULT 0,
    delta             DOUBLE PRECISION NOT NULL DEFAULT 0,
    waste_ratio       DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_spend_micros BIGINT NOT NULL DEFAULT 0,
    identified_waste_micros BIGINT NOT NULL DEFAULT 0,
    currency          TEXT NOT NULL DEFAULT 'USD',
    computed_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_efficiency_scores_tenant_scope ON efficiency_scores (tenant_id, scope, scope_id, computed_at DESC);
SELECT cloudoptix_enable_tenant_rls('efficiency_scores');
