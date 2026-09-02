-- 0013_audit: the tamper-evident, hash-chained log every consequential
-- platform action writes to.
--
-- Two invariants this migration enforces at the database level, because an
-- audit trail that only the application layer chooses to respect is not
-- actually tamper-evident:
--
--  1. sequence is unique per tenant and monotonically assigned by
--     AuditRepository.Append under a per-tenant advisory lock (see
--     internal/adapters/postgres/audit.go) — the UNIQUE constraint here is
--     what turns "the application is supposed to serialise this" into
--     something the database itself refuses to violate even if that lock
--     discipline has a bug.
--  2. UPDATE and DELETE are rejected outright by a trigger, not merely
--     unused by the application. A hash-chain is only tamper-*evident* —
--     VerifyChain (internal/domain/audit/audit.go) detects an edit after
--     the fact by recomputing hashes — but detection after the fact is a
--     forensic tool, not a control. The trigger is what stops an ordinary
--     UPDATE statement (a bug, a well-meaning support script, a compromised
--     application credential) from silently editing history in the first
--     place; VerifyChain remains the tool that catches a change made
--     through a path this trigger cannot see — a superuser session that
--     disables triggers, or an edit to the on-disk files directly — which
--     is why the package doc for internal/domain/audit also calls for
--     write-once object storage with a retention lock in production: no
--     single mechanism inside one mutable database is a complete answer.
CREATE TABLE audit_logs (
    id                 TEXT PRIMARY KEY,
    -- ON DELETE RESTRICT, not CASCADE like every other table here: the audit
    -- trigger below would turn a cascading delete into an opaque exception
    -- mid-cascade anyway, so the FK says up front, in the ordinary
    -- "violates foreign key constraint" way, that a tenant with audit
    -- history cannot be hard-deleted — only archived (tenancy.StateArchived).
    tenant_id          TEXT NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    sequence           BIGINT NOT NULL,

    action             TEXT NOT NULL,
    outcome            TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied', 'partial')),

    actor              TEXT NOT NULL DEFAULT '',
    actor_roles        JSONB NOT NULL DEFAULT '[]'::jsonb,
    actor_machine      BOOLEAN NOT NULL DEFAULT false,
    ip_address         TEXT NOT NULL DEFAULT '',
    user_agent         TEXT NOT NULL DEFAULT '',
    request_id         TEXT NOT NULL DEFAULT '',
    trace_id           TEXT NOT NULL DEFAULT '',

    subject_kind       TEXT NOT NULL DEFAULT '',
    subject_id         TEXT NOT NULL DEFAULT '',
    subject_name       TEXT NOT NULL DEFAULT '',

    before             JSONB,
    after              JSONB,

    aws_operation      TEXT NOT NULL DEFAULT '',
    aws_account_id     TEXT NOT NULL DEFAULT '',
    aws_region         TEXT NOT NULL DEFAULT '',
    aws_request_id     TEXT NOT NULL DEFAULT '',

    recommendation_id  TEXT NOT NULL DEFAULT '',
    plan_id            TEXT NOT NULL DEFAULT '',
    approval_id        TEXT NOT NULL DEFAULT '',
    policy_decision_id TEXT NOT NULL DEFAULT '',
    spec_version_id    TEXT NOT NULL DEFAULT '',

    message            TEXT NOT NULL DEFAULT '',
    metadata           JSONB NOT NULL DEFAULT '{}'::jsonb,
    error              TEXT NOT NULL DEFAULT '',

    at                 TIMESTAMPTZ NOT NULL DEFAULT now(),

    prev_hash          TEXT NOT NULL,
    hash               TEXT NOT NULL,

    UNIQUE (tenant_id, sequence)
);

-- audit_logs by (tenant, sequence) is the primary access pattern: the chain
-- walk VerifyChain performs, and the natural "most recent activity" read.
CREATE INDEX idx_audit_logs_tenant_sequence ON audit_logs (tenant_id, sequence);
CREATE INDEX idx_audit_logs_tenant_at ON audit_logs (tenant_id, at DESC);
CREATE INDEX idx_audit_logs_tenant_action ON audit_logs (tenant_id, action, at DESC);
CREATE INDEX idx_audit_logs_tenant_subject ON audit_logs (tenant_id, subject_id) WHERE subject_id <> '';
CREATE INDEX idx_audit_logs_tenant_actor ON audit_logs (tenant_id, actor);
SELECT cloudoptix_enable_tenant_rls('audit_logs');

CREATE OR REPLACE FUNCTION cloudoptix_audit_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only: % is not permitted (tenant=%, sequence=%)',
        TG_OP, OLD.tenant_id, OLD.sequence;
END;
$$;

CREATE TRIGGER trg_audit_logs_immutable
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION cloudoptix_audit_immutable();
