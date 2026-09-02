-- 0005_resources: the CloudOptix Resource Model (cloud.Resource), its graph
-- (cloud.Relationship) and the utilisation summaries the rule engine reads
-- (ports.ResourceMetrics / ports.MetricSeries).
--
-- resources is the largest non-partitioned table in the schema and the one
-- every optimization rule, the twin builder and the dashboard query against
-- constantly, so its indexes are built for the four access patterns the
-- codebase actually has, not a generic "index everything" pass:
--   * (tenant, account, region, kind)   -- the discovery scan's own shape
--   * (tenant, application)             -- "what does this app cost"
--   * (tenant, kind) partial on !deleted -- inventory loads, which exclude
--     tombstones almost every time
--   * tags/attributes GIN               -- AttributionRule matching and the
--     copilot's ad-hoc tag lookups, both of which query by key/value pair
--
-- native_key is a generated column mirroring cloud.Resource.Key(): it is
-- what discovery upserts on to stay idempotent, so it is unique rather than
-- merely indexed.
CREATE TABLE resources (
    id                   TEXT PRIMARY KEY,
    tenant_id            TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    account_id           TEXT NOT NULL,
    region               TEXT NOT NULL DEFAULT '',
    availability_zone    TEXT NOT NULL DEFAULT '',
    kind                 TEXT NOT NULL,
    arn                  TEXT NOT NULL DEFAULT '',
    native_id            TEXT NOT NULL,
    name                 TEXT NOT NULL DEFAULT '',
    state                TEXT NOT NULL DEFAULT 'unknown',
    instance_type        TEXT NOT NULL DEFAULT '',
    engine               TEXT NOT NULL DEFAULT '',
    engine_version       TEXT NOT NULL DEFAULT '',
    capacity             JSONB NOT NULL DEFAULT '{}'::jsonb,
    purchase_model       TEXT NOT NULL DEFAULT 'unknown',
    tags                 JSONB NOT NULL DEFAULT '{}'::jsonb,

    environment          TEXT NOT NULL DEFAULT 'unknown' CHECK (environment IN
                            ('production', 'staging', 'development', 'test', 'sandbox', 'dr', 'unknown')),
    environment_source   TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (environment_source IN
                            ('CONFIRMED', 'INFERRED', 'UNKNOWN', 'REQUIRES_USER_CONFIRMATION')),
    application_id       TEXT REFERENCES applications(id) ON DELETE SET NULL,
    workload_id          TEXT REFERENCES workloads(id) ON DELETE SET NULL,
    owner                TEXT NOT NULL DEFAULT '',
    cost_center          TEXT NOT NULL DEFAULT '',
    criticality          TEXT NOT NULL DEFAULT 'UNSET' CHECK (criticality IN
                            ('TIER_0', 'TIER_1', 'TIER_2', 'TIER_3', 'UNSET')),

    attributes           JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at           TIMESTAMPTZ,
    first_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    discovered_by        TEXT NOT NULL DEFAULT '',
    deleted              BOOLEAN NOT NULL DEFAULT false,

    -- Money is bigint micros + a currency code, never float or numeric: see
    -- internal/domain/core/money.go for the accumulation-drift rationale.
    -- Storing the exact same representation the domain type already uses
    -- means MoneyFromMicros(row.micros, row.currency) round-trips a Resource
    -- with zero conversion, and the audit hash of a monetary figure computed
    -- in Go matches one recomputed from a row fetched later verbatim.
    monthly_cost_micros   BIGINT NOT NULL DEFAULT 0,
    monthly_cost_currency TEXT NOT NULL DEFAULT 'USD',
    cost_source           TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (cost_source IN
                             ('CONFIRMED', 'INFERRED', 'UNKNOWN', 'REQUIRES_USER_CONFIRMATION')),

    native_key TEXT GENERATED ALWAYS AS (tenant_id || '/' || account_id || '/' || region || '/' || kind || '/' || native_id) STORED
);

CREATE UNIQUE INDEX uq_resources_native_key ON resources (native_key);
CREATE INDEX idx_resources_tenant_account_region_kind ON resources (tenant_id, account_id, region, kind);
CREATE INDEX idx_resources_tenant_application ON resources (tenant_id, application_id) WHERE application_id IS NOT NULL;
CREATE INDEX idx_resources_tenant_workload ON resources (tenant_id, workload_id) WHERE workload_id IS NOT NULL;
CREATE INDEX idx_resources_tenant_kind_active ON resources (tenant_id, kind) WHERE deleted = false;
CREATE INDEX idx_resources_tags_gin ON resources USING GIN (tags);
CREATE INDEX idx_resources_attributes_gin ON resources USING GIN (attributes);
CREATE INDEX idx_resources_arn ON resources (tenant_id, arn) WHERE arn <> '';
SELECT cloudoptix_enable_tenant_rls('resources');

-- resource_relationships is the architecture graph (Topology). account_id
-- and region are denormalized from the "from" endpoint purely so
-- ResourceRepository.ReplaceRelationships(tenant, accountID, region, edges)
-- — which replaces exactly the edge set one discovery scan of one
-- account/region produced — can delete its old edges with an index-only
-- scan instead of joining through resources twice.
CREATE TABLE resource_relationships (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    from_id       TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    to_id         TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    weight        DOUBLE PRECISION NOT NULL DEFAULT 1,
    confidence    DOUBLE PRECISION NOT NULL DEFAULT 1,
    source        TEXT NOT NULL DEFAULT 'INFERRED' CHECK (source IN
                     ('CONFIRMED', 'INFERRED', 'UNKNOWN', 'REQUIRES_USER_CONFIRMATION')),
    attributes    JSONB NOT NULL DEFAULT '{}'::jsonb,
    account_id    TEXT NOT NULL DEFAULT '',
    region        TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, from_id, to_id, kind)
);

CREATE INDEX idx_relationships_from ON resource_relationships (tenant_id, from_id);
CREATE INDEX idx_relationships_to ON resource_relationships (tenant_id, to_id);
CREATE INDEX idx_relationships_scan_scope ON resource_relationships (tenant_id, account_id, region);
SELECT cloudoptix_enable_tenant_rls('resource_relationships');

-- resource_metrics stores both shapes MetricRepository serves: the
-- percentile summary (`kind = 'summary'`, one row per resource per window,
-- ResourceMetrics marshalled whole into `summary` because it is a bag of a
-- dozen optional named Percentiles and every consumer reads it as a unit)
-- and a raw point series (`kind = 'series'`, one row per resource per named
-- metric, `points` bounded to the short raw-retention window the package
-- comment on ports.MetricRepository describes).
CREATE TABLE resource_metrics (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    resource_id   TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('summary', 'series')),
    metric_name   TEXT NOT NULL DEFAULT '',
    namespace     TEXT NOT NULL DEFAULT '',
    dimensions    JSONB NOT NULL DEFAULT '{}'::jsonb,
    unit          TEXT NOT NULL DEFAULT '',
    window_start  TIMESTAMPTZ NOT NULL,
    window_end    TIMESTAMPTZ NOT NULL,
    summary       JSONB NOT NULL DEFAULT '{}'::jsonb,
    points        JSONB NOT NULL DEFAULT '[]'::jsonb,
    coverage      DOUBLE PRECISION NOT NULL DEFAULT 0,
    source        TEXT NOT NULL DEFAULT '',
    collected_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_resource_metrics_summary ON resource_metrics (tenant_id, resource_id)
    WHERE kind = 'summary';
CREATE UNIQUE INDEX uq_resource_metrics_series ON resource_metrics (tenant_id, resource_id, metric_name, namespace)
    WHERE kind = 'series';
CREATE INDEX idx_resource_metrics_resource ON resource_metrics (tenant_id, resource_id);
SELECT cloudoptix_enable_tenant_rls('resource_metrics');
