-- 0009_govern: policy-as-code, the decisions it produces, and the human
-- approval workflow.
CREATE TABLE policies (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    version        INT NOT NULL,
    rules          JSONB NOT NULL DEFAULT '[]'::jsonb,
    default_effect TEXT NOT NULL CHECK (default_effect IN
                      ('auto_execute', 'require_approval', 'prohibit', 'advisory_only')),
    enabled        BOOLEAN NOT NULL DEFAULT false,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at   TIMESTAMPTZ,
    checksum       TEXT NOT NULL DEFAULT '',
    UNIQUE (tenant_id, name, version)
);

CREATE INDEX idx_policies_tenant_name ON policies (tenant_id, name, version DESC);
-- GetActive(tenant): exactly one policy may be enabled at a time per the
-- application invariant Policy.Activate() enforces; the partial unique
-- index makes that invariant a constraint rather than only a convention.
CREATE UNIQUE INDEX uq_policies_one_active ON policies (tenant_id) WHERE enabled = true;
SELECT cloudoptix_enable_tenant_rls('policies');

CREATE TABLE policy_decisions (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    recommendation_id  TEXT NOT NULL DEFAULT '',
    policy_id          TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    policy_version     INT NOT NULL DEFAULT 0,
    policy_checksum    TEXT NOT NULL DEFAULT '',
    effect             TEXT NOT NULL CHECK (effect IN
                          ('auto_execute', 'require_approval', 'prohibit', 'advisory_only')),
    matched_rules      JSONB NOT NULL DEFAULT '[]'::jsonb,
    deciding_rule      TEXT NOT NULL DEFAULT '',
    reason             TEXT NOT NULL DEFAULT '',
    explanation        JSONB NOT NULL DEFAULT '[]'::jsonb,
    requires_approval  BOOLEAN NOT NULL DEFAULT false,
    approvers          JSONB NOT NULL DEFAULT '[]'::jsonb,
    min_approvals      INT NOT NULL DEFAULT 0,
    require_distinct_approver BOOLEAN NOT NULL DEFAULT false,
    maintenance_windows JSONB NOT NULL DEFAULT '[]'::jsonb,
    input_digest       TEXT NOT NULL DEFAULT '',
    decided_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_policy_decisions_tenant_recommendation ON policy_decisions (tenant_id, recommendation_id);
SELECT cloudoptix_enable_tenant_rls('policy_decisions');

CREATE TABLE approvals (
    id                 TEXT PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    subject_kind       TEXT NOT NULL CHECK (subject_kind IN
                          ('recommendation', 'execution_plan', 'spec', 'policy', 'aws_connection', 'commitment_purchase')),
    subject_id         TEXT NOT NULL,
    title              TEXT NOT NULL DEFAULT '',
    summary            TEXT NOT NULL DEFAULT '',
    context            JSONB NOT NULL DEFAULT '{}'::jsonb,
    policy_decision_id TEXT,
    required_roles     JSONB NOT NULL DEFAULT '[]'::jsonb,
    min_approvals      INT NOT NULL DEFAULT 1,
    require_distinct_approver BOOLEAN NOT NULL DEFAULT false,
    state              TEXT NOT NULL CHECK (state IN
                          ('pending', 'approved', 'rejected', 'expired', 'cancelled')),
    requested_by       TEXT NOT NULL DEFAULT '',
    requested_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    decided_at         TIMESTAMPTZ,
    execute_after      TIMESTAMPTZ
);

CREATE INDEX idx_approvals_tenant_subject ON approvals (tenant_id, subject_kind, subject_id);
-- ListPending's shape: pending requests ordered by how soon they expire, so
-- an approver dashboard can surface the most urgent one first.
CREATE INDEX idx_approvals_tenant_pending ON approvals (tenant_id, expires_at) WHERE state = 'pending';
SELECT cloudoptix_enable_tenant_rls('approvals');

-- approval_responses is govern.Response, normalized out of Request.Responses
-- into its own append-only table: a response is itself a fact worth
-- indexing on its own (who voted, when) independent of the parent request's
-- current state.
CREATE TABLE approval_responses (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
    principal   TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT '',
    approved    BOOLEAN NOT NULL,
    comment     TEXT NOT NULL DEFAULT '',
    ip_address  TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (approval_id, principal) -- Request.Decide: a principal votes at most once
);

CREATE INDEX idx_approval_responses_approval ON approval_responses (approval_id);
SELECT cloudoptix_enable_tenant_rls('approval_responses');
