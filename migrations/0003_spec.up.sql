-- 0003_spec: the onboarding specification, spec.Spec's persisted form.
--
-- specs is a thin grouping row: the domain has no independent "Spec"
-- aggregate, only a family of spec.Version snapshots sharing one spec_id
-- (see internal/domain/spec/spec.go). The row exists so spec_versions has
-- something to foreign-key against and so a spec identifier is stable
-- before any version of it has been approved.
--
-- spec_versions is intentionally insert/update-in-place for drafts and
-- append-only for approved versions: SpecRepository has no Update method by
-- design (a draft is replaced wholesale by SaveDraft; an approved version is
-- frozen). The partial unique index enforces the single-active-version
-- invariant the domain comment promises: at most one row per (tenant,
-- spec_id) may be 'approved' at a time, because Approve() supersedes the
-- prior active version in the same transaction.
CREATE TABLE specs (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_specs_tenant ON specs (tenant_id);
SELECT cloudoptix_attach_updated_at('specs');
SELECT cloudoptix_enable_tenant_rls('specs');

CREATE TABLE spec_versions (
    id               TEXT PRIMARY KEY,
    tenant_id        TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    spec_id          TEXT NOT NULL REFERENCES specs(id) ON DELETE CASCADE,
    version          INT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN
                        ('draft', 'validating', 'pending_review', 'approved', 'superseded', 'rejected')),
    -- spec_document is the full spec.Spec struct (organization, application,
    -- aws, workloads, business, objectives, ... — twelve nested sections).
    -- Modelling each as its own column would mean a schema migration for
    -- every new spec field the onboarding agent learns to ask about; the
    -- document is only ever read and written whole by the application, never
    -- filtered by a nested field in SQL, so JSONB with no GIN index costs
    -- nothing and buys that flexibility.
    spec_document    JSONB NOT NULL,
    checksum         TEXT NOT NULL,
    parent_id        TEXT REFERENCES spec_versions(id),
    diff             JSONB NOT NULL DEFAULT '[]'::jsonb,
    validation       JSONB NOT NULL DEFAULT '{}'::jsonb,
    completeness     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_by      TEXT NOT NULL DEFAULT '',
    approved_at      TIMESTAMPTZ,
    approval_id      TEXT,
    rejected_reason  TEXT NOT NULL DEFAULT '',
    conversation_id  TEXT,
    UNIQUE (tenant_id, spec_id, version)
);

CREATE INDEX idx_spec_versions_tenant_spec ON spec_versions (tenant_id, spec_id, version DESC);
-- GetActive walks straight to the one approved row per spec without scanning
-- history; the partial index also IS the single-active-version constraint.
CREATE UNIQUE INDEX uq_spec_versions_one_active ON spec_versions (tenant_id, spec_id) WHERE status = 'approved';
SELECT cloudoptix_enable_tenant_rls('spec_versions');
