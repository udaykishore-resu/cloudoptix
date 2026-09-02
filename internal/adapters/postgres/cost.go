package postgres

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// CostRepository is the pgx-backed ports.CostRepository.
type CostRepository struct{ db *DB }

// NewCostRepository builds a CostRepository over db.
func NewCostRepository(db *DB) *CostRepository { return &CostRepository{db: db} }

var _ ports.CostRepository = (*CostRepository)(nil)

const costUpsertBatchSize = 1000

// UpsertBatch is a plain multi-row INSERT, not an upsert-on-conflict: unlike
// ResourceRepository, cost_records carries no natural business key (a CUR
// line has no stable identity CloudOptix can dedupe on across ingestion
// runs), so re-ingesting the same billing period is a caller-level concern,
// not this method's. Before inserting, it calls
// cloudoptix_ensure_cost_records_partition once per distinct calendar month
// present in the batch — see migrations/0006_cost.up.sql for why that call
// has to happen before the INSERT rather than relying solely on the
// pre-created rolling window or the DEFAULT partition.
func (r *CostRepository) UpsertBatch(ctx context.Context, tenant core.TenantID, records []cost.Record) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	inserted := 0
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		months := map[time.Time]bool{}
		for _, rec := range records {
			months[time.Date(rec.Period.Start.Year(), rec.Period.Start.Month(), 1, 0, 0, 0, 0, time.UTC)] = true
		}
		for m := range months {
			if _, err := q.Exec(ctx, `SELECT cloudoptix_ensure_cost_records_partition($1)`, m); err != nil {
				return mapErr(err)
			}
		}
		for start := 0; start < len(records); start += costUpsertBatchSize {
			end := start + costUpsertBatchSize
			if end > len(records) {
				end = len(records)
			}
			n, err := insertCostChunk(ctx, q, tenant, records[start:end])
			if err != nil {
				return err
			}
			inserted += n
		}
		return nil
	})
	return inserted, err
}

const costColumnCount = 23

func insertCostChunk(ctx context.Context, q Querier, tenant core.TenantID, chunk []cost.Record) (int, error) {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO cost_records (
		id, tenant_id, account_id, region, availability_zone, period_start, period_end, granularity,
		service, usage_type, operation, resource_id, resource_arn, charge_type, basis, amount_micros,
		amount_currency, usage_quantity, usage_unit, tags, environment, source, ingested_at
	) VALUES `)
	args := make([]any, 0, len(chunk)*costColumnCount)
	for i, rec := range chunk {
		if rec.TenantID.IsZero() {
			rec.TenantID = tenant
		}
		if rec.TenantID != tenant {
			return 0, core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
				"cost record belongs to tenant %s, not %s", rec.TenantID, tenant)
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		base := len(args)
		sb.WriteByte('(')
		for c := 0; c < costColumnCount; c++ {
			if c > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(base + c + 1))
		}
		sb.WriteByte(')')

		id := rec.ID
		if id.IsZero() {
			id = core.NewID("cst")
		}
		micros, currency := moneyMicros(rec.Amount)
		args = append(args,
			string(id), string(rec.TenantID), string(rec.AccountID), string(rec.Region), rec.AZ,
			rec.Period.Start, rec.Period.End, string(rec.Granularity), rec.Service, rec.UsageType,
			rec.Operation, nullableID(rec.ResourceID), string(rec.ResourceARN), string(rec.ChargeType),
			string(rec.Basis), micros, currency, rec.UsageQty, rec.UsageUnit, toJSON(rec.Tags),
			string(rec.Environment), rec.Source, orNow(rec.IngestedAt),
		)
	}
	tag, err := q.Exec(ctx, sb.String(), args...)
	if err != nil {
		return 0, mapErr(err)
	}
	return int(tag.RowsAffected()), nil
}

// resolvedBasis is the amortization basis a filter effectively selects. An
// unset filter defaults to amortized rather than "any basis": CUR-style
// ingestion can store one usage line under several bases at once (amortized,
// unblended, net_amortized), and summing across all of them for an
// unfiltered query would double- or triple-count the same underlying usage.
// Matches memstore's costRepo.resolvedBasis exactly so both adapters answer
// the same query identically.
func resolvedBasis(f ports.CostFilter) cost.AmortizationBasis {
	if f.Basis != "" {
		return f.Basis
	}
	return cost.BasisAmortized
}

// buildCostFilter is the pure filter-to-SQL builder for CostFilter. Every
// column is qualified with the "c." alias: Breakdown's "application" and
// "resource" dimensions LEFT JOIN resources, which has its own tenant_id,
// account_id, region and environment columns, so an unqualified condition
// here would compile against the unjoined callers (Series/Total/ByResource)
// but fail with "column reference is ambiguous" the moment Breakdown joins
// — qualifying unconditionally means every caller uses the same FROM
// cost_records c shape and the filter never has two behaviours to keep in
// sync.
func buildCostFilter(tenant core.TenantID, f ports.CostFilter) (string, []any) {
	conds := []string{"c.tenant_id = $1", "c.basis = $2"}
	args := []any{string(tenant), string(resolvedBasis(f))}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if !f.Period.IsZero() {
		conds = append(conds, "c.period_start >= "+arg(f.Period.Start)+" AND c.period_start < "+arg(f.Period.End))
	}
	if len(f.AccountIDs) > 0 {
		conds = append(conds, "c.account_id = ANY("+arg(toStringSlice(f.AccountIDs))+"::text[])")
	}
	if len(f.Regions) > 0 {
		conds = append(conds, "c.region = ANY("+arg(toStringSlice(f.Regions))+"::text[])")
	}
	if len(f.Services) > 0 {
		conds = append(conds, "c.service = ANY("+arg(f.Services)+"::text[])")
	}
	if len(f.Environments) > 0 {
		conds = append(conds, "c.environment = ANY("+arg(toStringSlice(f.Environments))+"::text[])")
	}
	if len(f.ResourceIDs) > 0 {
		conds = append(conds, "c.resource_id = ANY("+arg(toStringSlice(f.ResourceIDs))+"::text[])")
	}
	if len(f.ChargeTypes) > 0 {
		conds = append(conds, "c.charge_type = ANY("+arg(toStringSlice(f.ChargeTypes))+"::text[])")
	}
	if !f.ApplicationID.IsZero() {
		conds = append(conds, "c.resource_id IN (SELECT id FROM resources WHERE tenant_id = $1 AND application_id = "+arg(string(f.ApplicationID))+")")
	}
	if f.TagKey != "" {
		if f.TagValue != "" {
			conds = append(conds, "c.tags @> "+arg(string(toJSON(map[string]string{f.TagKey: f.TagValue})))+"::jsonb")
		} else {
			conds = append(conds, "c.tags ? "+arg(f.TagKey))
		}
	}
	return strings.Join(conds, " AND "), args
}

// bucketGranularity narrows a requested granularity to one of the three
// series can be reported at, defaulting to daily — the same default
// memstore's costRepo.bucketGranularity uses, so a caller sees identical
// buckets from either adapter.
func bucketGranularity(g cost.Granularity) cost.Granularity {
	switch g {
	case cost.GranularityHourly, cost.GranularityMonthly:
		return g
	default:
		return cost.GranularityDaily
	}
}

func bucketStart(t time.Time, g cost.Granularity) time.Time {
	u := t.UTC()
	switch g {
	case cost.GranularityHourly:
		return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), 0, 0, 0, time.UTC)
	case cost.GranularityMonthly:
		return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}
}

func bucketNext(t time.Time, g cost.Granularity) time.Time {
	switch g {
	case cost.GranularityHourly:
		return t.Add(time.Hour)
	case cost.GranularityMonthly:
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

// Series buckets matched records by period.Start into a time-ordered series,
// filling every bucket in the requested (or observed) span with a zero
// amount rather than omitting it — the same fill-the-gaps behaviour
// memstore's Series produces, since a chart built from a series with holes
// in it renders a misleading flat line instead of a visible zero.
func (r *CostRepository) Series(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (cost.Series, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Series{}, err
	}
	g := bucketGranularity(f.Granularity)
	var out cost.Series
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildCostFilter(tenant, f)
		period := f.Period
		if period.IsZero() {
			row := r.db.querier(ctx).QueryRow(ctx, `SELECT min(c.period_start), max(c.period_end) FROM cost_records c WHERE `+where, args...)
			var start, end *time.Time
			if err := row.Scan(&start, &end); err != nil {
				return mapErr(err)
			}
			if start == nil || end == nil {
				out = cost.Series{Granularity: g, Currency: core.USD}
				return nil
			}
			period = core.NewPeriod(*start, *end)
		}

		bucketExpr := dateTruncExpr(g)
		rows, err := r.db.querier(ctx).Query(ctx,
			`SELECT `+bucketExpr+` AS bucket, SUM(c.amount_micros), MIN(c.amount_currency)
				FROM cost_records c WHERE `+where+` GROUP BY bucket`, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		sums := map[time.Time]int64{}
		ccy := core.USD
		for rows.Next() {
			var b time.Time
			var micros int64
			var currency *string
			if err := rows.Scan(&b, &micros, &currency); err != nil {
				return mapErr(err)
			}
			sums[b] = micros
			if currency != nil && *currency != "" {
				ccy = core.Currency(*currency)
			}
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}

		var points []cost.Point
		for b := bucketStart(period.Start, g); b.Before(period.End); b = bucketNext(b, g) {
			amt := core.MoneyFromMicros(sums[b], ccy)
			points = append(points, cost.Point{Period: core.NewPeriod(b, bucketNext(b, g)), Amount: amt})
		}
		out = cost.Series{Granularity: g, Points: points, Currency: ccy}
		return nil
	})
	return out, err
}

// dateTruncExpr renders the SQL bucket-boundary expression matching
// bucketStart's Go-side truncation exactly, so a row's bucket key here and a
// point's Period.Start after Series reassembles it never disagree.
func dateTruncExpr(g cost.Granularity) string {
	switch g {
	case cost.GranularityHourly:
		return "date_trunc('hour', c.period_start)"
	case cost.GranularityMonthly:
		return "date_trunc('month', c.period_start)"
	default:
		return "date_trunc('day', c.period_start)"
	}
}

var costBreakdownDimensions = map[string]string{
	"service":     "c.service",
	"account":     "c.account_id",
	"region":      "c.region",
	"environment": "c.environment",
	"usage_type":  "c.usage_type",
}

// Breakdown delegates the share/sort computation to cost.NewBreakdown so
// this adapter and the memstore one can never disagree about how a share is
// computed — see cost.NewBreakdown's own doc comment.
func (r *CostRepository) Breakdown(ctx context.Context, tenant core.TenantID, f ports.CostFilter, dimension string) (cost.Breakdown, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cost.Breakdown{}, err
	}
	var out cost.Breakdown
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildCostFilter(tenant, f)
		q := r.db.querier(ctx)

		amounts := map[string]core.Money{}
		labels := map[string]string{}
		ccy := core.USD

		switch dimension {
		case "service", "account", "region", "environment", "usage_type":
			col := costBreakdownDimensions[dimension]
			rows, err := q.Query(ctx, `
				SELECT COALESCE(NULLIF(`+col+`,''),'__unknown__'), SUM(c.amount_micros), MIN(c.amount_currency)
				FROM cost_records c WHERE `+where+` GROUP BY 1`, args...)
			if err != nil {
				return mapErr(err)
			}
			defer rows.Close()
			for rows.Next() {
				var key string
				var micros int64
				var currency string
				if err := rows.Scan(&key, &micros, &currency); err != nil {
					return mapErr(err)
				}
				if currency != "" {
					ccy = core.Currency(currency)
				}
				amounts[key] = core.MoneyFromMicros(micros, ccy)
			}
			if err := rows.Err(); err != nil {
				return mapErr(err)
			}
		case "application":
			rows, err := q.Query(ctx, `
				SELECT COALESCE(r.application_id,'__unattributed__'), SUM(c.amount_micros), MIN(c.amount_currency)
				FROM cost_records c LEFT JOIN resources r ON r.tenant_id = c.tenant_id AND r.id = c.resource_id
				WHERE `+where+`
				GROUP BY 1`, args...)
			if err != nil {
				return mapErr(err)
			}
			defer rows.Close()
			for rows.Next() {
				var key string
				var micros int64
				var currency string
				if err := rows.Scan(&key, &micros, &currency); err != nil {
					return mapErr(err)
				}
				if currency != "" {
					ccy = core.Currency(currency)
				}
				if key == "" {
					key = "__unattributed__"
				}
				amounts[key] = core.MoneyFromMicros(micros, ccy)
			}
			if err := rows.Err(); err != nil {
				return mapErr(err)
			}
		case "resource":
			rows, err := q.Query(ctx, `
				SELECT c.resource_id, SUM(c.amount_micros), MIN(c.amount_currency),
					MIN(COALESCE(NULLIF(r.name,''), r.tags->>'Name', r.native_id))
				FROM cost_records c LEFT JOIN resources r ON r.tenant_id = c.tenant_id AND r.id = c.resource_id
				WHERE `+where+` AND c.resource_id IS NOT NULL
				GROUP BY c.resource_id`, args...)
			if err != nil {
				return mapErr(err)
			}
			defer rows.Close()
			for rows.Next() {
				var key string
				var micros int64
				var currency, label string
				if err := rows.Scan(&key, &micros, &currency, &label); err != nil {
					return mapErr(err)
				}
				if currency != "" {
					ccy = core.Currency(currency)
				}
				amounts[key] = core.MoneyFromMicros(micros, ccy)
				labels[key] = label
			}
			if err := rows.Err(); err != nil {
				return mapErr(err)
			}
		default:
			return core.Invalid("cost breakdown: unknown dimension %q", dimension)
		}

		period := f.Period
		if period.IsZero() && len(amounts) > 0 {
			row := q.QueryRow(ctx, `SELECT min(c.period_start), max(c.period_end) FROM cost_records c WHERE `+where, args...)
			var start, end time.Time
			if err := row.Scan(&start, &end); err != nil {
				return mapErr(err)
			}
			period = core.NewPeriod(start, end)
		}
		b := cost.NewBreakdown(dimension, period, amounts)
		for i := range b.Items {
			b.Items[i].Label = labels[b.Items[i].Key]
		}
		out = b
		return nil
	})
	return out, err
}

func (r *CostRepository) Total(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (core.Money, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return core.Money{}, err
	}
	total := core.ZeroUSD()
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildCostFilter(tenant, f)
		row := r.db.querier(ctx).QueryRow(ctx,
			`SELECT COALESCE(SUM(c.amount_micros),0), COALESCE(MIN(c.amount_currency),'USD') FROM cost_records c WHERE `+where, args...)
		var micros int64
		var currency string
		if err := row.Scan(&micros, &currency); err != nil {
			return mapErr(err)
		}
		total = moneyFromMicros(micros, currency)
		return nil
	})
	return total, err
}

func (r *CostRepository) ByResource(ctx context.Context, tenant core.TenantID, f ports.CostFilter) (map[core.ID]core.Money, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	out := map[core.ID]core.Money{}
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildCostFilter(tenant, f)
		rows, err := r.db.querier(ctx).Query(ctx, `
			SELECT c.resource_id, SUM(c.amount_micros), MIN(c.amount_currency)
			FROM cost_records c WHERE `+where+` AND c.resource_id IS NOT NULL GROUP BY c.resource_id`, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, currency string
			var micros int64
			if err := rows.Scan(&id, &micros, &currency); err != nil {
				return mapErr(err)
			}
			out[core.ID(id)] = moneyFromMicros(micros, currency)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *CostRepository) LatestIngestedAt(ctx context.Context, tenant core.TenantID) (time.Time, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return time.Time{}, err
	}
	var out time.Time
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		var t *time.Time
		if err := r.db.querier(ctx).QueryRow(ctx, `SELECT max(ingested_at) FROM cost_records WHERE tenant_id = $1`,
			string(tenant)).Scan(&t); err != nil {
			return mapErr(err)
		}
		out = nilToZero(t)
		return nil
	})
	return out, err
}

func (r *CostRepository) SaveAnomalies(ctx context.Context, tenant core.TenantID, anomalies []cost.Anomaly) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if len(anomalies) == 0 {
		return nil
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		for _, a := range anomalies {
			id := a.ID
			if id.IsZero() {
				id = core.NewID("anm")
			}
			expected, _ := moneyMicros(a.Expected)
			actual, _ := moneyMicros(a.Actual)
			delta, currency := moneyMicros(a.Delta)
			if _, err := q.Exec(ctx, `
				INSERT INTO cost_anomalies (id, tenant_id, detected_at, period_start, period_end, dimension,
					key, expected_micros, actual_micros, delta_micros, currency, delta_pct, score, severity,
					explanation, contributors, acknowledged)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
				ON CONFLICT (id) DO UPDATE SET
					expected_micros = EXCLUDED.expected_micros, actual_micros = EXCLUDED.actual_micros,
					delta_micros = EXCLUDED.delta_micros, delta_pct = EXCLUDED.delta_pct,
					score = EXCLUDED.score, severity = EXCLUDED.severity, explanation = EXCLUDED.explanation,
					contributors = EXCLUDED.contributors
			`, string(id), string(tenant), orNow(a.DetectedAt), a.Period.Start, a.Period.End, a.Dimension,
				a.Key, expected, actual, delta, currency, a.DeltaPct, a.Score, string(a.Severity),
				a.Explanation, toJSON(a.Contributors), a.Acknowledged); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (r *CostRepository) ListAnomalies(ctx context.Context, tenant core.TenantID, from, to time.Time, opts ports.ListOptions) (ports.Page[cost.Anomaly], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[cost.Anomaly]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[cost.Anomaly]{}, err
	}
	var page ports.Page[cost.Anomaly]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where := []string{"tenant_id = $1"}
		args := []any{string(tenant)}
		if !from.IsZero() {
			args = append(args, from)
			where = append(where, "detected_at >= $"+strconv.Itoa(len(args)))
		}
		if !to.IsZero() {
			args = append(args, to)
			where = append(where, "detected_at < $"+strconv.Itoa(len(args)))
		}
		if after != nil {
			args = append(args, after[0])
			where = append(where, "id > $"+strconv.Itoa(len(args)))
		}
		sql := anomalySelectSQL + " WHERE " + strings.Join(where, " AND ") + " ORDER BY id LIMIT " + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []cost.Anomaly
		for rows.Next() {
			a, err := scanAnomaly(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, a)
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

func (r *CostRepository) AcknowledgeAnomaly(ctx context.Context, tenant core.TenantID, id core.ID, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	_ = by // acknowledger identity belongs in the audit trail, not the anomaly row
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx,
			`UPDATE cost_anomalies SET acknowledged = true WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("anomaly", id)
		}
		return nil
	})
}

const anomalySelectSQL = `
	SELECT id, tenant_id, detected_at, period_start, period_end, dimension, key, expected_micros,
		actual_micros, delta_micros, currency, delta_pct, score, severity, explanation, contributors,
		acknowledged
	FROM cost_anomalies`

func scanAnomaly(row rowScanner) (cost.Anomaly, error) {
	var a cost.Anomaly
	var start, end time.Time
	var expected, actual, delta int64
	var currency string
	var contributors []byte
	if err := row.Scan(&a.ID, &a.TenantID, &a.DetectedAt, &start, &end, &a.Dimension, &a.Key, &expected,
		&actual, &delta, &currency, &a.DeltaPct, &a.Score, &a.Severity, &a.Explanation, &contributors,
		&a.Acknowledged); err != nil {
		return cost.Anomaly{}, err
	}
	a.Period = core.NewPeriod(start, end)
	a.Expected = moneyFromMicros(expected, currency)
	a.Actual = moneyFromMicros(actual, currency)
	a.Delta = moneyFromMicros(delta, currency)
	if err := fromJSON(contributors, &a.Contributors); err != nil {
		return cost.Anomaly{}, err
	}
	return a, nil
}
