DROP INDEX IF EXISTS idx_recommendations_conflict_group;
DROP INDEX IF EXISTS idx_recommendations_tenant_primary;

ALTER TABLE recommendations
    DROP COLUMN IF EXISTS preferred_alternative_id,
    DROP COLUMN IF EXISTS alternative_ids,
    DROP COLUMN IF EXISTS mutually_exclusive,
    DROP COLUMN IF EXISTS conflict_group_id,
    DROP COLUMN IF EXISTS conflict_domain;
