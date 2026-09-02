-- 0010_execute: the safe-change machinery — execution plans and their
-- steps, pre-change snapshots, rollback plans, post-change validation, the
-- savings ladder, and the learning loop's outcome/calibration history.
CREATE TABLE execution_plans (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    recommendation_id  TEXT NOT NULL DEFAULT '',
    action             TEXT NOT NULL,
    title              TEXT NOT NULL DEFAULT '',

    account_id         TEXT NOT NULL DEFAULT '',
    region             TEXT NOT NULL DEFAULT '',
    environment        TEXT NOT NULL DEFAULT 'unknown',
    resource_ids       JSONB NOT NULL DEFAULT '[]'::jsonb,

    validation         JSONB NOT NULL DEFAULT '{}'::jsonb,

    expected_monthly_saving_micros BIGINT NOT NULL DEFAULT 0,
    baseline_monthly_cost_micros   BIGINT NOT NULL DEFAULT 0,
    currency           TEXT NOT NULL DEFAULT 'USD',

    state              TEXT NOT NULL CHECK (state IN
                          ('draft', 'awaiting_approval', 'approved', 'scheduled', 'preflight', 'executing',
                           'executed', 'validating', 'validated', 'failed', 'rolling_back', 'rolled_back',
                           'rollback_failed', 'cancelled')),
    state_reason       TEXT NOT NULL DEFAULT '',
    approval_id        TEXT,
    policy_decision_id TEXT,
    scheduled_for      TIMESTAMPTZ,
    dry_run            BOOLEAN NOT NULL DEFAULT false,

    -- claimed_by/claimed_until implement ClaimDuePlans/
    -- ClaimPlansAwaitingValidation as a lease: a worker claims a batch of
    -- due plans by writing its id and a near-future expiry in one UPDATE ...
    -- WHERE claimed_until < now() RETURNING, so two workers racing on the
    -- same poll interval can never both pick up the same plan — the second
    -- worker's UPDATE simply matches zero rows.
    claimed_by         TEXT NOT NULL DEFAULT '',
    claimed_until      TIMESTAMPTZ,

    requested_by       TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at         TIMESTAMPTZ,
    finished_at        TIMESTAMPTZ
);

CREATE INDEX idx_execution_plans_tenant_state ON execution_plans (tenant_id, state);
-- ClaimDuePlans scans for approved/scheduled plans whose scheduled_for has
-- arrived and whose lease is free; this partial index keeps that scan small
-- regardless of how many terminal-state plans have accumulated.
CREATE INDEX idx_execution_plans_due ON execution_plans (scheduled_for)
    WHERE state IN ('approved', 'scheduled');
SELECT cloudoptix_enable_tenant_rls('execution_plans');

-- execution_steps unifies execute.Step (the forward plan) and
-- RollbackPlan.Steps (the reverse plan) in one table distinguished by
-- `phase`, rather than two near-identical tables: a step is a step whichever
-- direction it runs, and the execution engine updates step state the same
-- way regardless of phase.
CREATE TABLE execution_steps (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id          TEXT NOT NULL REFERENCES execution_plans(id) ON DELETE CASCADE,
    phase            TEXT NOT NULL CHECK (phase IN ('forward', 'rollback')),
    ordinal          INT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('precondition', 'snapshot', 'mutate', 'wait', 'verify')),
    name             TEXT NOT NULL DEFAULT '',
    describe         TEXT NOT NULL DEFAULT '',
    aws_action       TEXT NOT NULL DEFAULT '',
    target           TEXT NOT NULL DEFAULT '',
    parameters       JSONB NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key  TEXT NOT NULL,
    state            TEXT NOT NULL CHECK (state IN
                        ('pending', 'running', 'succeeded', 'failed', 'skipped', 'rolled_back')),
    attempts         INT NOT NULL DEFAULT 0,
    max_retries      INT NOT NULL DEFAULT 0,
    started_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    error            TEXT NOT NULL DEFAULT '',
    output           JSONB NOT NULL DEFAULT '{}'::jsonb,
    abort_on_failure BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (plan_id, phase, ordinal),
    -- The idempotency key is what makes a retried Apply() call safe: two
    -- attempts at the same step must resolve to the same key, and the key
    -- must be unique across the whole tenant so a retry can never collide
    -- with an unrelated step's key by coincidence.
    UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX idx_execution_steps_plan ON execution_steps (plan_id, phase, ordinal);
-- GIN on parameters: the execution console lets an operator search steps by
-- a parameter value (e.g. "every step that touched instance type m5.large")
-- without re-deriving it from the resource.
CREATE INDEX idx_execution_steps_parameters_gin ON execution_steps USING GIN (parameters);
SELECT cloudoptix_enable_tenant_rls('execution_steps');

CREATE TABLE rollback_plans (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id            TEXT NOT NULL REFERENCES execution_plans(id) ON DELETE CASCADE,
    feasible           BOOLEAN NOT NULL DEFAULT false,
    infeasible_reason  TEXT NOT NULL DEFAULT '',
    estimated_duration_ms BIGINT NOT NULL DEFAULT 0,
    data_loss_risk     TEXT NOT NULL DEFAULT 'NONE' CHECK (data_loss_risk IN
                          ('NONE', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    summary            TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id)
);

SELECT cloudoptix_enable_tenant_rls('rollback_plans');

CREATE TABLE execution_snapshots (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id       TEXT NOT NULL REFERENCES execution_plans(id) ON DELETE CASCADE,
    resource_id   TEXT NOT NULL,
    resource_arn  TEXT NOT NULL DEFAULT '',
    captured_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    attributes    JSONB NOT NULL DEFAULT '{}'::jsonb,
    backup_refs   JSONB NOT NULL DEFAULT '{}'::jsonb,
    digest        TEXT NOT NULL DEFAULT '',
    UNIQUE (plan_id, resource_id)
);

CREATE INDEX idx_execution_snapshots_plan ON execution_snapshots (plan_id);
SELECT cloudoptix_enable_tenant_rls('execution_snapshots');

CREATE TABLE validation_results (
    id                               TEXT PRIMARY KEY,
    tenant_id                        TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plan_id                          TEXT NOT NULL REFERENCES execution_plans(id) ON DELETE CASCADE,
    verdict                          TEXT NOT NULL CHECK (verdict IN
                                        ('success', 'partial_success', 'failure', 'inconclusive')),
    explanation                      TEXT NOT NULL DEFAULT '',
    baseline_window_start            TIMESTAMPTZ,
    baseline_window_end              TIMESTAMPTZ,
    observed_window_start            TIMESTAMPTZ,
    observed_window_end              TIMESTAMPTZ,
    checks                           JSONB NOT NULL DEFAULT '[]'::jsonb,
    predicted_monthly_saving_micros  BIGINT NOT NULL DEFAULT 0,
    observed_monthly_saving_micros   BIGINT NOT NULL DEFAULT 0,
    currency                         TEXT NOT NULL DEFAULT 'USD',
    saving_accuracy                  DOUBLE PRECISION NOT NULL DEFAULT 0,
    rollback_triggered                BOOLEAN NOT NULL DEFAULT false,
    rollback_reason                   TEXT NOT NULL DEFAULT '',
    evaluated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (plan_id)
);

SELECT cloudoptix_enable_tenant_rls('validation_results');

-- savings_records is execute.SavingsRecord: one row per recommendation,
-- updated in place as it moves down the ladder (Advance mutates the Go
-- struct and the repository persists the new state plus an appended
-- history entry — see stage_history).
CREATE TABLE savings_records (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    recommendation_id   TEXT NOT NULL,
    plan_id             TEXT NOT NULL DEFAULT '',
    rule_id             TEXT NOT NULL DEFAULT '',
    action              TEXT NOT NULL DEFAULT '',
    resource_id         TEXT NOT NULL DEFAULT '',
    application_id      TEXT NOT NULL DEFAULT '',
    environment         TEXT NOT NULL DEFAULT 'unknown',

    stage               TEXT NOT NULL CHECK (stage IN
                           ('potential', 'approved', 'planned', 'executed', 'validated', 'realized')),

    potential_monthly_micros  BIGINT NOT NULL DEFAULT 0,
    approved_monthly_micros   BIGINT NOT NULL DEFAULT 0,
    executed_monthly_micros   BIGINT NOT NULL DEFAULT 0,
    validated_monthly_micros  BIGINT NOT NULL DEFAULT 0,
    realized_monthly_micros   BIGINT NOT NULL DEFAULT 0,
    currency                  TEXT NOT NULL DEFAULT 'USD',

    baseline_cost_micros      BIGINT NOT NULL DEFAULT 0,
    post_change_cost_micros   BIGINT NOT NULL DEFAULT 0,
    measured_window_start     TIMESTAMPTZ,
    measured_window_end       TIMESTAMPTZ,

    stage_history       JSONB NOT NULL DEFAULT '[]'::jsonb,
    lost                BOOLEAN NOT NULL DEFAULT false,
    lost_reason         TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, recommendation_id)
);

-- List(tenant, period) and Funnel(tenant, period) both scan every record
-- whose creation falls in a window and fold by stage; this index serves
-- both without needing a stage-specific one, since Funnel folds every stage
-- from the same row set.
CREATE INDEX idx_savings_records_tenant_created ON savings_records (tenant_id, created_at);
SELECT cloudoptix_attach_updated_at('savings_records');
SELECT cloudoptix_enable_tenant_rls('savings_records');

CREATE TABLE optimization_outcomes (
    id                       TEXT PRIMARY KEY,
    tenant_id                TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    rule_id                  TEXT NOT NULL,
    action                   TEXT NOT NULL DEFAULT '',
    resource_kind            TEXT NOT NULL DEFAULT '',
    environment              TEXT NOT NULL DEFAULT 'unknown',

    predicted_monthly_saving_micros BIGINT NOT NULL DEFAULT 0,
    actual_monthly_saving_micros    BIGINT NOT NULL DEFAULT 0,
    currency                 TEXT NOT NULL DEFAULT 'USD',
    predicted_confidence     DOUBLE PRECISION NOT NULL DEFAULT 0,
    predicted_risk           TEXT NOT NULL DEFAULT 'NONE',

    verdict                  TEXT NOT NULL CHECK (verdict IN
                                ('success', 'partial_success', 'failure', 'inconclusive')),
    rolled_back              BOOLEAN NOT NULL DEFAULT false,
    performance_impact_pct   DOUBLE PRECISION NOT NULL DEFAULT 0,
    availability_impact_pct  DOUBLE PRECISION NOT NULL DEFAULT 0,
    saving_ratio             DOUBLE PRECISION NOT NULL DEFAULT 0,
    observed_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ListOutcomes(tenant, ruleID, limit): the calibrator's read path, most
-- recent observations first.
CREATE INDEX idx_optimization_outcomes_tenant_rule ON optimization_outcomes (tenant_id, rule_id, observed_at DESC);
SELECT cloudoptix_enable_tenant_rls('optimization_outcomes');

CREATE TABLE rule_calibrations (
    tenant_id              TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    rule_id                TEXT NOT NULL,
    samples                INT NOT NULL DEFAULT 0,
    success_rate           DOUBLE PRECISION NOT NULL DEFAULT 0,
    rollback_rate          DOUBLE PRECISION NOT NULL DEFAULT 0,
    mean_saving_ratio      DOUBLE PRECISION NOT NULL DEFAULT 0,
    median_saving_ratio    DOUBLE PRECISION NOT NULL DEFAULT 0,
    confidence_multiplier  DOUBLE PRECISION NOT NULL DEFAULT 1,
    saving_multiplier      DOUBLE PRECISION NOT NULL DEFAULT 1,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, rule_id)
);

SELECT cloudoptix_attach_updated_at('rule_calibrations');
SELECT cloudoptix_enable_tenant_rls('rule_calibrations');
