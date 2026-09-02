-- 0014_recommendation_conflicts: the conflict-group columns that stop
-- overlapping recommendations from being summed.
--
-- Three rules can each be right that one EKS node group is oversized —
-- shrink the node count, shrink the node size, shrink the pod requests that
-- force the node count — and at most one of them can be applied. Before
-- these columns existed the dashboard added all three together, so the
-- headline "identified waste" figure named money the estate did not hold.
-- See internal/domain/optimize/conflict.go for the model.
--
-- The columns are denormalized onto recommendations rather than modelled as
-- a conflict_groups table with a join, for the same reason category and
-- environment already are (0008_optimize.up.sql): every read that needs them
-- is a filtered scan of recommendations for one tenant, and the grouping is
-- recomputed wholesale by each analysis run rather than edited row by row,
-- so a separate table would buy referential tidiness at the cost of a join
-- on the platform's hottest list query.
--
-- alternative_ids is a JSONB array of recommendation ids rather than a
-- foreign-keyed child table on purpose: the ids it holds are all members of
-- the same batch this row was written in, they are rewritten as a unit on
-- every analysis run, and a member that is later superseded should leave a
-- dangling id the reader skips (see Service.loadAlternatives) rather than
-- cascade-deleting the surviving row's list.
ALTER TABLE recommendations
    ADD COLUMN conflict_domain           TEXT   NOT NULL DEFAULT '',
    ADD COLUMN conflict_group_id         TEXT   NOT NULL DEFAULT '',
    ADD COLUMN mutually_exclusive        BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN alternative_ids           JSONB  NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN preferred_alternative_id  TEXT   NOT NULL DEFAULT '';

-- The summary query filters open rows on "is this a primary", i.e.
-- preferred_alternative_id = ''. A partial index on the primaries is what
-- keeps that roll-up a scan of the rows it actually sums rather than of
-- every open recommendation including the alternatives it discards.
CREATE INDEX idx_recommendations_tenant_primary
    ON recommendations (tenant_id, status)
    WHERE preferred_alternative_id = '';

CREATE INDEX idx_recommendations_conflict_group
    ON recommendations (tenant_id, conflict_group_id)
    WHERE conflict_group_id <> '';
