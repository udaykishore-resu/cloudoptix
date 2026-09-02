-- 0006_cost: billed cost (cost.Record) and detected anomalies (cost.Anomaly).
--
-- cost_records is declarative range-partitioned by month on period_start
-- because it is, by a wide margin, the largest table in the schema — every
-- Cost Explorer/CUR line item for every tenant, every day, forever (subject
-- to the tenant's retention_days quota). Partitioning buys three things a
-- single 500M-row table would not give us: DELETE-by-retention becomes a
-- partition DROP (instant, no vacuum bloat) instead of a row-by-row delete;
-- the query planner prunes partitions outside a period filter before
-- touching an index at all, which is exactly the CostFilter.Period shape
-- every caller uses; and VACUUM/ANALYZE run per-partition instead of
-- stalling on one enormous table. The primary key has to carry the
-- partition key (period_start) alongside id — that is a Postgres
-- partitioning requirement, not a modelling choice, and it does not weaken
-- uniqueness because id is already globally unique on its own
-- (core.NewID's entropy).
--
-- cloudoptix_ensure_cost_records_partition(month) creates the partition for
-- one calendar month if it does not already exist. CostRepository.UpsertBatch
-- calls it for every distinct month present in a batch before inserting, so
-- ingesting a new month never race-fails against partition creation; the
-- DEFAULT partition below is the second line of defence for the rare row
-- that arrives for a month nobody pre-created (a backfill, a clock-skewed
-- ingestion) — inserts still succeed, just without partition pruning until
-- the month's own partition is created and Postgres is asked to move rows
-- into it, which the retention job is expected to do.
CREATE TABLE cost_records (
    id            TEXT NOT NULL,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    account_id    TEXT NOT NULL,
    region        TEXT NOT NULL DEFAULT '',
    availability_zone TEXT NOT NULL DEFAULT '',

    period_start  TIMESTAMPTZ NOT NULL,
    period_end    TIMESTAMPTZ NOT NULL,
    granularity   TEXT NOT NULL CHECK (granularity IN ('hourly', 'daily', 'monthly')),

    service       TEXT NOT NULL,
    usage_type    TEXT NOT NULL DEFAULT '',
    operation     TEXT NOT NULL DEFAULT '',

    resource_id   TEXT REFERENCES resources(id) ON DELETE SET NULL,
    resource_arn  TEXT NOT NULL DEFAULT '',

    charge_type   TEXT NOT NULL,
    basis         TEXT NOT NULL CHECK (basis IN ('amortized', 'unblended', 'net_amortized', 'blended')),
    -- See 0005_resources.up.sql's comment on resources.monthly_cost_micros:
    -- the same bigint-micros-plus-currency representation applies to every
    -- Money column in this schema.
    amount_micros   BIGINT NOT NULL,
    amount_currency TEXT NOT NULL DEFAULT 'USD',
    usage_quantity  DOUBLE PRECISION NOT NULL DEFAULT 0,
    usage_unit      TEXT NOT NULL DEFAULT '',

    tags          JSONB NOT NULL DEFAULT '{}'::jsonb,
    environment   TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT '',
    ingested_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (id, period_start)
) PARTITION BY RANGE (period_start);

-- The natural query shape (CostFilter: period + service, sometimes account)
-- drives this composite index; period_start leads because every query
-- carries a period and partition pruning already narrows on it, so the
-- index's job is to narrow further within a partition.
CREATE INDEX idx_cost_records_tenant_period_service ON cost_records (tenant_id, period_start, service);
CREATE INDEX idx_cost_records_tenant_account ON cost_records (tenant_id, account_id, period_start);
CREATE INDEX idx_cost_records_tenant_resource ON cost_records (tenant_id, resource_id, period_start)
    WHERE resource_id IS NOT NULL;
SELECT cloudoptix_enable_tenant_rls('cost_records');

CREATE TABLE cost_records_default PARTITION OF cost_records DEFAULT;

CREATE OR REPLACE FUNCTION cloudoptix_ensure_cost_records_partition(p_month date) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    part_name text := 'cost_records_' || to_char(p_month, 'YYYY_MM');
    start_ts  timestamptz := date_trunc('month', p_month)::timestamptz;
    end_ts    timestamptz := (date_trunc('month', p_month) + interval '1 month')::timestamptz;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF cost_records FOR VALUES FROM (%L) TO (%L)',
            part_name, start_ts, end_ts
        );
    END IF;
END;
$$;

-- Pre-create a rolling window around "now" so a fresh deployment can ingest
-- immediately; UpsertBatch additionally calls the function above per batch
-- for months outside this window (a historical backfill, a report run
-- against next month's forecast).
DO $$
DECLARE
    m date;
BEGIN
    FOR m IN SELECT generate_series(
        date_trunc('month', now()) - interval '6 months',
        date_trunc('month', now()) + interval '2 months',
        interval '1 month'
    )::date
    LOOP
        PERFORM cloudoptix_ensure_cost_records_partition(m);
    END LOOP;
END;
$$;

CREATE TABLE cost_anomalies (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    detected_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    period_start  TIMESTAMPTZ NOT NULL,
    period_end    TIMESTAMPTZ NOT NULL,
    dimension     TEXT NOT NULL,
    key           TEXT NOT NULL,
    expected_micros BIGINT NOT NULL DEFAULT 0,
    actual_micros   BIGINT NOT NULL DEFAULT 0,
    delta_micros    BIGINT NOT NULL DEFAULT 0,
    currency        TEXT NOT NULL DEFAULT 'USD',
    delta_pct     DOUBLE PRECISION NOT NULL DEFAULT 0,
    score         DOUBLE PRECISION NOT NULL DEFAULT 0,
    severity      TEXT NOT NULL CHECK (severity IN ('INFO', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    explanation   TEXT NOT NULL DEFAULT '',
    contributors  JSONB NOT NULL DEFAULT '[]'::jsonb,
    acknowledged  BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX idx_cost_anomalies_tenant_period ON cost_anomalies (tenant_id, period_start DESC);
CREATE INDEX idx_cost_anomalies_tenant_ack ON cost_anomalies (tenant_id, acknowledged);
SELECT cloudoptix_enable_tenant_rls('cost_anomalies');
