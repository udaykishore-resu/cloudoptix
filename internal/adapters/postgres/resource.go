package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ResourceRepository is the pgx-backed ports.ResourceRepository.
type ResourceRepository struct{ db *DB }

// NewResourceRepository builds a ResourceRepository over db.
func NewResourceRepository(db *DB) *ResourceRepository { return &ResourceRepository{db: db} }

var _ ports.ResourceRepository = (*ResourceRepository)(nil)

// resourceUpsertBatchSize bounds how many resource rows one multi-row INSERT
// statement carries. Each row binds 30 parameters; Postgres's protocol
// limit is 65535 bound parameters per statement, so 1000 rows (30000
// params) leaves comfortable headroom while still turning a 50,000-row
// discovery scan into 50 round trips instead of 50,000 — the difference
// between UpsertBatch finishing in seconds and it dominating the whole
// discovery run.
const resourceUpsertBatchSize = 1000

// UpsertBatch is idempotent on Resource.Key() (native_key, a generated
// column — see migrations/0005_resources.up.sql). It builds one multi-row
// `INSERT ... ON CONFLICT (native_key) DO UPDATE` statement per chunk of
// resourceUpsertBatchSize rows rather than looping one statement per row: a
// per-row round trip against a 30-50k row discovery scan is the exact
// "unusable" case the ports doc comment on this method calls out.
func (r *ResourceRepository) UpsertBatch(ctx context.Context, tenant core.TenantID, resources []cloud.Resource) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	if len(resources) == 0 {
		return 0, nil
	}
	upserted := 0
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		for start := 0; start < len(resources); start += resourceUpsertBatchSize {
			end := start + resourceUpsertBatchSize
			if end > len(resources) {
				end = len(resources)
			}
			n, err := upsertResourceChunk(ctx, r.db.querier(ctx), tenant, resources[start:end])
			if err != nil {
				return err
			}
			upserted += n
		}
		return nil
	})
	return upserted, err
}

const resourceColumnCount = 32

func upsertResourceChunk(ctx context.Context, q Querier, tenant core.TenantID, chunk []cloud.Resource) (int, error) {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO resources (
		id, tenant_id, account_id, region, availability_zone, kind, arn, native_id, name, state,
		instance_type, engine, engine_version, capacity, purchase_model, tags, environment,
		environment_source, application_id, workload_id, owner, cost_center, criticality, attributes,
		created_at, first_seen_at, last_seen_at, discovered_by, deleted, monthly_cost_micros,
		monthly_cost_currency, cost_source
	) VALUES `)

	args := make([]any, 0, len(chunk)*resourceColumnCount)
	for i, res := range chunk {
		if res.TenantID.IsZero() {
			res.TenantID = tenant
		}
		if err := res.Validate(); err != nil {
			return 0, err
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		base := len(args)
		sb.WriteByte('(')
		for c := 0; c < resourceColumnCount; c++ {
			if c > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(base + c + 1))
		}
		sb.WriteByte(')')

		micros, currency := moneyMicros(res.MonthlyCost)
		id := res.ID
		if id.IsZero() {
			id = core.NewID("res")
		}
		args = append(args,
			string(id), string(res.TenantID), string(res.AccountID), string(res.Region), res.AZ,
			string(res.Kind), string(res.ARN), res.NativeID, res.Name, string(res.State),
			res.InstanceType, res.Engine, res.EngineVersion, toJSON(res.Capacity), string(res.Purchase),
			toJSON(res.Tags), string(res.Environment), string(res.EnvironmentSource),
			nullableID(res.ApplicationID), nullableID(res.WorkloadID), res.Owner, res.CostCenter,
			criticalityOrUnset(res.Criticality), toJSON(res.Attributes), zeroToNil(res.CreatedAt),
			orNow(res.FirstSeenAt), orNow(res.LastSeenAt), res.DiscoveredBy, res.Deleted, micros,
			currency, costSourceOrUnknown(res.CostSource),
		)
	}

	sb.WriteString(`
		ON CONFLICT (native_key) DO UPDATE SET
			id = resources.id, -- native_key already identifies the row; keep the original surrogate id
			arn = EXCLUDED.arn, name = EXCLUDED.name, state = EXCLUDED.state,
			instance_type = EXCLUDED.instance_type, engine = EXCLUDED.engine,
			engine_version = EXCLUDED.engine_version, capacity = EXCLUDED.capacity,
			purchase_model = EXCLUDED.purchase_model, tags = EXCLUDED.tags,
			environment = EXCLUDED.environment, environment_source = EXCLUDED.environment_source,
			application_id = EXCLUDED.application_id, workload_id = EXCLUDED.workload_id,
			owner = EXCLUDED.owner, cost_center = EXCLUDED.cost_center,
			criticality = EXCLUDED.criticality, attributes = EXCLUDED.attributes,
			availability_zone = EXCLUDED.availability_zone, last_seen_at = EXCLUDED.last_seen_at,
			discovered_by = EXCLUDED.discovered_by, deleted = EXCLUDED.deleted,
			monthly_cost_micros = EXCLUDED.monthly_cost_micros,
			monthly_cost_currency = EXCLUDED.monthly_cost_currency, cost_source = EXCLUDED.cost_source
	`)

	tag, err := q.Exec(ctx, sb.String(), args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return int(tag.RowsAffected()), nil
}

func nullableID(id core.ID) any {
	if id.IsZero() {
		return nil
	}
	return string(id)
}

// costSourceOrUnknown defaults an empty Provenance to UNKNOWN, mirroring
// criticalityOrUnset: the cost_source CHECK constraint has no empty-string
// case, and a resource discovered before cost attribution ran legitimately
// has a zero-valued CostSource.
func costSourceOrUnknown(p core.Provenance) string {
	if p == "" {
		return string(core.ProvenanceUnknown)
	}
	return string(p)
}

func (r *ResourceRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Resource, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Resource{}, err
	}
	var out cloud.Resource
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, resourceSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		res, err := scanResource(row)
		if err != nil {
			return mapErr(err)
		}
		out = res
		return nil
	})
	return out, err
}

func (r *ResourceRepository) GetByNativeID(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, native string) (cloud.Resource, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Resource{}, err
	}
	var out cloud.Resource
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx,
			resourceSelectSQL+` WHERE tenant_id = $1 AND account_id = $2 AND region = $3 AND native_id = $4`,
			string(tenant), string(accountID), string(region), native)
		res, err := scanResource(row)
		if err != nil {
			return mapErr(err)
		}
		out = res
		return nil
	})
	return out, err
}

func (r *ResourceRepository) List(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter, opts ports.ListOptions) (ports.Page[cloud.Resource], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[cloud.Resource]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[cloud.Resource]{}, err
	}
	var page ports.Page[cloud.Resource]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildResourceFilter(tenant, f)
		if after != nil {
			args = append(args, after[0])
			where += fmt.Sprintf(" AND id > $%d", len(args))
		}
		sql := resourceSelectSQL + " WHERE " + where + " ORDER BY id LIMIT " + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []cloud.Resource
		for rows.Next() {
			res, err := scanResource(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, res)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		if len(items) > opts.Limit {
			items = items[:opts.Limit]
			page.NextCursor = encodeCursor(string(items[len(items)-1].ID))
		}
		page.Items = items
		return nil
	})
	return page, err
}

// LoadInventory returns every resource matching f in one shot, unpaginated,
// for the rule engine and twin builder — both need the whole filtered set
// at once to evaluate cross-resource rules and build the graph.
func (r *ResourceRepository) LoadInventory(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (*cloud.Inventory, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var resources []cloud.Resource
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildResourceFilter(tenant, f)
		rows, err := r.db.querier(ctx).Query(ctx, resourceSelectSQL+" WHERE "+where+" ORDER BY id", args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			res, err := scanResource(rows)
			if err != nil {
				return mapErr(err)
			}
			resources = append(resources, res)
		}
		return mapErr(rows.Err())
	})
	if err != nil {
		return nil, err
	}
	return cloud.NewInventory(resources), nil
}

// MarkAbsent tombstones every resource of the scanned kinds in this
// account/region that discovery did NOT see in this run. seenKeys are
// native_key values (Resource.Key()); a resource whose native_key is not in
// that set has been removed from AWS or has moved out of scope, and marking
// it deleted (rather than hard-deleting) keeps its cost history intact for
// the trailing-window reports that read it.
func (r *ResourceRepository) MarkAbsent(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, kinds []cloud.Kind, seenKeys []string, at time.Time) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	if len(kinds) == 0 {
		return 0, nil // an empty kind set scans nothing, so it tombstones nothing
	}
	kindStrs := make([]string, len(kinds))
	for i, k := range kinds {
		kindStrs[i] = string(k)
	}
	marked := 0
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE resources SET deleted = true, last_seen_at = last_seen_at
			WHERE tenant_id = $1 AND account_id = $2 AND region = $3 AND kind = ANY($4::text[])
				AND deleted = false AND NOT (native_key = ANY($5::text[]))
		`, string(tenant), string(accountID), string(region), kindStrs, seenKeys)
		if err != nil {
			return mapErr(err)
		}
		marked = int(tag.RowsAffected())
		return nil
	})
	_ = at // at is the caller-declared "as of" instant for the audit trail, not stored on the row itself
	return marked, err
}

func (r *ResourceRepository) Count(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	var count int
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildResourceFilter(tenant, f)
		row := r.db.querier(ctx).QueryRow(ctx, `SELECT count(*) FROM resources WHERE `+where, args...)
		return mapErr(row.Scan(&count))
	})
	return count, err
}

// ReplaceRelationships deletes and reinserts, in one transaction, exactly
// the edge set one discovery scan of one account/region produced. Replacing
// rather than diffing is correct here because relationship discovery is
// necessarily a full re-derivation each scan (security-group rules can be
// removed, a routing target can change) — there is no reliable notion of
// "this specific edge is still true" to diff against.
func (r *ResourceRepository) ReplaceRelationships(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, edges []cloud.Relationship) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		if _, err := q.Exec(ctx,
			`DELETE FROM resource_relationships WHERE tenant_id = $1 AND account_id = $2 AND region = $3`,
			string(tenant), string(accountID), string(region)); err != nil {
			return mapErr(err)
		}
		const chunkSize = 2000 // 9 params/row
		for start := 0; start < len(edges); start += chunkSize {
			end := start + chunkSize
			if end > len(edges) {
				end = len(edges)
			}
			if err := insertRelationshipChunk(ctx, q, tenant, accountID, region, edges[start:end]); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertRelationshipChunk(ctx context.Context, q Querier, tenant core.TenantID, accountID core.AccountID, region core.Region, chunk []cloud.Relationship) error {
	if len(chunk) == 0 {
		return nil
	}
	const cols = 13 // id, tenant_id, from_id, to_id, kind, weight, confidence, source, attributes, account_id, region, first_seen_at, last_seen_at
	var sb strings.Builder
	sb.WriteString(`INSERT INTO resource_relationships
		(id, tenant_id, from_id, to_id, kind, weight, confidence, source, attributes, account_id, region, first_seen_at, last_seen_at)
		VALUES `)
	args := make([]any, 0, len(chunk)*cols)
	for i, e := range chunk {
		if i > 0 {
			sb.WriteByte(',')
		}
		base := len(args)
		sb.WriteByte('(')
		for c := 0; c < cols; c++ {
			if c > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(base + c + 1))
		}
		sb.WriteByte(')')
		id := e.ID
		if id.IsZero() {
			id = core.NewID("rel")
		}
		args = append(args,
			string(id), string(tenant), string(e.FromID), string(e.ToID), string(e.Kind),
			e.EffectiveWeight(), float64(e.Confidence), string(e.Source), toJSON(e.Attributes),
			string(accountID), string(region), orNow(e.FirstSeenAt), orNow(e.LastSeenAt),
		)
	}
	sb.WriteString(` ON CONFLICT (tenant_id, from_id, to_id, kind) DO UPDATE SET
		weight = EXCLUDED.weight, confidence = EXCLUDED.confidence, source = EXCLUDED.source,
		attributes = EXCLUDED.attributes, last_seen_at = EXCLUDED.last_seen_at`)
	_, err := q.Exec(ctx, sb.String(), args...)
	return mapErr(err)
}

// LoadTopology loads the relationship graph touching the resources f
// selects: every edge with at least one endpoint in the filtered resource
// set. Narrowing by endpoint membership (rather than loading the tenant's
// whole graph, which can run to hundreds of thousands of edges) is what
// keeps a blast-radius calculation scoped to one application fast.
func (r *ResourceRepository) LoadTopology(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (*cloud.Topology, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var edges []cloud.Relationship
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildResourceFilter(tenant, f)
		rows, err := r.db.querier(ctx).Query(ctx, `
			SELECT id, tenant_id, from_id, to_id, kind, weight, confidence, source, attributes,
				first_seen_at, last_seen_at
			FROM resource_relationships
			WHERE tenant_id = $1 AND (
				from_id IN (SELECT id FROM resources WHERE `+where+`) OR
				to_id IN (SELECT id FROM resources WHERE `+where+`)
			)
		`, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanRelationship(rows)
			if err != nil {
				return mapErr(err)
			}
			edges = append(edges, e)
		}
		return mapErr(rows.Err())
	})
	if err != nil {
		return nil, err
	}
	return cloud.NewTopology(edges), nil
}

// buildResourceFilter renders a ports.ResourceFilter into a `tenant_id = $1
// AND ...` WHERE fragment plus its positional arguments, starting from
// $1=tenant. It is a pure function — no I/O — so it is unit tested directly
// against expected SQL fragments without a database.
func buildResourceFilter(tenant core.TenantID, f ports.ResourceFilter) (string, []any) {
	conds := []string{"tenant_id = $1"}
	args := []any{string(tenant)}

	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if len(f.AccountIDs) > 0 {
		conds = append(conds, "account_id = ANY("+arg(toStringSlice(f.AccountIDs))+"::text[])")
	}
	if len(f.Regions) > 0 {
		conds = append(conds, "region = ANY("+arg(toStringSlice(f.Regions))+"::text[])")
	}
	if len(f.Kinds) > 0 {
		conds = append(conds, "kind = ANY("+arg(toStringSlice(f.Kinds))+"::text[])")
	}
	if len(f.Categories) > 0 {
		// Category is derived from Kind (Kind.Category()), not stored as its
		// own column, so a category filter expands to the set of kinds in
		// those categories at query-build time rather than needing a
		// generated/indexed category column purely for this filter.
		var kinds []string
		for _, k := range allResourceKinds() {
			for _, c := range f.Categories {
				if k.Category() == c {
					kinds = append(kinds, string(k))
					break
				}
			}
		}
		conds = append(conds, "kind = ANY("+arg(kinds)+"::text[])")
	}
	if len(f.Environments) > 0 {
		conds = append(conds, "environment = ANY("+arg(toStringSlice(f.Environments))+"::text[])")
	}
	if !f.ApplicationID.IsZero() {
		conds = append(conds, "application_id = "+arg(string(f.ApplicationID)))
	}
	if !f.WorkloadID.IsZero() {
		conds = append(conds, "workload_id = "+arg(string(f.WorkloadID)))
	}
	if len(f.States) > 0 {
		conds = append(conds, "state = ANY("+arg(toStringSlice(f.States))+"::text[])")
	}
	if f.TagKey != "" {
		if f.TagValue != "" {
			conds = append(conds, "tags @> "+arg(string(toJSON(map[string]string{f.TagKey: f.TagValue})))+"::jsonb")
		} else {
			conds = append(conds, "tags ? "+arg(f.TagKey))
		}
	}
	if f.Search != "" {
		conds = append(conds, "(name ILIKE "+arg("%"+f.Search+"%")+" OR native_id ILIKE "+arg("%"+f.Search+"%")+")")
	}
	if !f.MinMonthlyCost.IsZero() {
		micros, _ := moneyMicros(f.MinMonthlyCost)
		conds = append(conds, "monthly_cost_micros >= "+arg(micros))
	}
	if !f.IncludeDeleted {
		conds = append(conds, "deleted = false")
	}
	return strings.Join(conds, " AND "), args
}

func toStringSlice[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func allResourceKinds() []cloud.Kind {
	return []cloud.Kind{
		cloud.KindEC2Instance, cloud.KindEBSVolume, cloud.KindEBSSnapshot, cloud.KindElasticIP,
		cloud.KindAMI, cloud.KindAutoScalingGroup, cloud.KindRDSInstance, cloud.KindRDSCluster,
		cloud.KindRDSSnapshot, cloud.KindDynamoDBTable, cloud.KindS3Bucket, cloud.KindLambdaFunction,
		cloud.KindECSCluster, cloud.KindECSService, cloud.KindECSTaskDef, cloud.KindEKSCluster,
		cloud.KindEKSNodeGroup, cloud.KindK8sWorkload, cloud.KindK8sNamespace, cloud.KindALB,
		cloud.KindNLB, cloud.KindTargetGroup, cloud.KindCloudFront, cloud.KindAPIGateway,
		cloud.KindNATGateway, cloud.KindVPC, cloud.KindSubnet, cloud.KindSecurityGroup,
		cloud.KindVPCEndpoint, cloud.KindTransitGateway, cloud.KindRoute53Zone, cloud.KindElastiCache,
		cloud.KindMSKCluster, cloud.KindSQSQueue, cloud.KindSNSTopic, cloud.KindKinesisStream,
		cloud.KindEventBus, cloud.KindLogGroup, cloud.KindCloudTrail, cloud.KindConfigRecorder,
		cloud.KindKMSKey, cloud.KindSecret, cloud.KindUnknown,
	}
}

const resourceSelectSQL = `
	SELECT id, tenant_id, account_id, region, availability_zone, kind, arn, native_id, name, state,
		instance_type, engine, engine_version, capacity, purchase_model, tags, environment,
		environment_source, COALESCE(application_id,''), COALESCE(workload_id,''), owner, cost_center,
		criticality, attributes, created_at, first_seen_at, last_seen_at, discovered_by, deleted,
		monthly_cost_micros, monthly_cost_currency, cost_source
	FROM resources`

func scanResource(row rowScanner) (cloud.Resource, error) {
	var res cloud.Resource
	var capacity, tags, attributes []byte
	var createdAt *time.Time
	var micros int64
	var currency string
	if err := row.Scan(&res.ID, &res.TenantID, &res.AccountID, &res.Region, &res.AZ, &res.Kind, &res.ARN,
		&res.NativeID, &res.Name, &res.State, &res.InstanceType, &res.Engine, &res.EngineVersion,
		&capacity, &res.Purchase, &tags, &res.Environment, &res.EnvironmentSource, &res.ApplicationID,
		&res.WorkloadID, &res.Owner, &res.CostCenter, &res.Criticality, &attributes, &createdAt,
		&res.FirstSeenAt, &res.LastSeenAt, &res.DiscoveredBy, &res.Deleted, &micros, &currency,
		&res.CostSource); err != nil {
		return cloud.Resource{}, err
	}
	res.CreatedAt = nilToZero(createdAt)
	res.MonthlyCost = moneyFromMicros(micros, currency)
	if err := fromJSON(capacity, &res.Capacity); err != nil {
		return cloud.Resource{}, err
	}
	if err := fromJSON(tags, &res.Tags); err != nil {
		return cloud.Resource{}, err
	}
	if err := fromJSON(attributes, &res.Attributes); err != nil {
		return cloud.Resource{}, err
	}
	return res, nil
}

func scanRelationship(row rowScanner) (cloud.Relationship, error) {
	var e cloud.Relationship
	var attrs []byte
	var confidence float64
	if err := row.Scan(&e.ID, &e.TenantID, &e.FromID, &e.ToID, &e.Kind, &e.Weight, &confidence, &e.Source,
		&attrs, &e.FirstSeenAt, &e.LastSeenAt); err != nil {
		return cloud.Relationship{}, err
	}
	e.Confidence = core.Confidence(confidence)
	if err := fromJSON(attrs, &e.Attributes); err != nil {
		return cloud.Relationship{}, err
	}
	return e, nil
}
