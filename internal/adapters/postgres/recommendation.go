package postgres

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RecommendationRepository is the pgx-backed ports.RecommendationRepository.
type RecommendationRepository struct{ db *DB }

// NewRecommendationRepository builds a RecommendationRepository over db.
func NewRecommendationRepository(db *DB) *RecommendationRepository {
	return &RecommendationRepository{db: db}
}

var _ ports.RecommendationRepository = (*RecommendationRepository)(nil)

// SaveBatch upserts the finding embedded in each recommendation and then the
// recommendation itself, in that order and in the same transaction: a
// recommendation's FK to findings would otherwise be able to point at a row
// that never committed. application_id is resolved from the finding's
// resource_id via a correlated subquery rather than carried on Finding
// (which has no such field, per optimize.Finding) — see
// migrations/0008_optimize.up.sql's comment on why the column is
// denormalized onto recommendations at all.
func (r *RecommendationRepository) SaveBatch(ctx context.Context, tenant core.TenantID, recs []optimize.Recommendation) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		for _, rec := range recs {
			if rec.TenantID != tenant {
				return core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
					"recommendation %s belongs to tenant %s, not %s", rec.ID, rec.TenantID, tenant)
			}
			if rec.ID.IsZero() {
				rec.ID = core.NewID("rec")
			}
			f := rec.Finding
			if f.ID.IsZero() {
				f.ID = core.NewID("fnd")
			}
			cur, _ := moneyMicros(f.CurrentMonthlyCost)
			savingMicros, currency := moneyMicros(f.EstimatedMonthlySaving)
			if _, err := q.Exec(ctx, `
				INSERT INTO findings (id, tenant_id, rule_id, rule_name, category, resource_id,
					resource_name, resource_kind, account_id, region, environment, severity, summary,
					detail, evidence, current_monthly_cost_micros, estimated_monthly_saving_micros,
					currency, detected_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
				ON CONFLICT (id) DO UPDATE SET
					rule_name = EXCLUDED.rule_name, resource_name = EXCLUDED.resource_name,
					severity = EXCLUDED.severity, summary = EXCLUDED.summary, detail = EXCLUDED.detail,
					evidence = EXCLUDED.evidence, current_monthly_cost_micros = EXCLUDED.current_monthly_cost_micros,
					estimated_monthly_saving_micros = EXCLUDED.estimated_monthly_saving_micros
			`, string(f.ID), string(tenant), string(f.RuleID), f.RuleName, string(f.Category),
				string(f.ResourceID), f.ResourceName, string(f.ResourceKind), string(f.AccountID),
				string(f.Region), string(f.Environment), string(f.Severity), f.Summary, f.Detail,
				toJSON(f.Evidence), cur, savingMicros, currency, orNow(f.DetectedAt)); err != nil {
				return mapErr(err)
			}

			recSavingMicros, recCurrency := moneyMicros(rec.EstimatedMonthlySaving)
			annualMicros, _ := moneyMicros(rec.EstimatedAnnualSaving)
			implMicros, _ := moneyMicros(rec.ImplementationCost)
			if _, err := q.Exec(ctx, `
				INSERT INTO recommendations (id, tenant_id, finding_id, title, rationale, action,
					parameters, current_state, proposed_state, estimated_monthly_saving_micros,
					estimated_annual_saving_micros, implementation_cost_micros, currency, payback_days,
					confidence, confidence_basis, risk, blast_radius, reversibility, complexity,
					priority_score, rank, status, status_reason, snoozed_until, requires_approval,
					policy_decision_id, auto_executable, narrative, maintenance_window, supersedes_id,
					account_id, application_id, resource_id, environment, category, rule_id, created_at,
					updated_at, conflict_domain, conflict_group_id, mutually_exclusive, alternative_ids,
					preferred_alternative_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,
					$23,$24,$25,$26,NULLIF($27,''),$28,$29,$30,NULLIF($31,''),$32,
					(SELECT application_id FROM resources WHERE tenant_id = $2 AND id = $33),
					$33,$34,$35,$36,$37,$37,$38,$39,$40,$41,$42)
				ON CONFLICT (id) DO UPDATE SET
					title = EXCLUDED.title, rationale = EXCLUDED.rationale, action = EXCLUDED.action,
					parameters = EXCLUDED.parameters, current_state = EXCLUDED.current_state,
					proposed_state = EXCLUDED.proposed_state,
					estimated_monthly_saving_micros = EXCLUDED.estimated_monthly_saving_micros,
					estimated_annual_saving_micros = EXCLUDED.estimated_annual_saving_micros,
					implementation_cost_micros = EXCLUDED.implementation_cost_micros,
					payback_days = EXCLUDED.payback_days, confidence = EXCLUDED.confidence,
					confidence_basis = EXCLUDED.confidence_basis, risk = EXCLUDED.risk,
					blast_radius = EXCLUDED.blast_radius, reversibility = EXCLUDED.reversibility,
					complexity = EXCLUDED.complexity, priority_score = EXCLUDED.priority_score,
					rank = EXCLUDED.rank, status = EXCLUDED.status, status_reason = EXCLUDED.status_reason,
					snoozed_until = EXCLUDED.snoozed_until, requires_approval = EXCLUDED.requires_approval,
					policy_decision_id = EXCLUDED.policy_decision_id,
					auto_executable = EXCLUDED.auto_executable, narrative = EXCLUDED.narrative,
					maintenance_window = EXCLUDED.maintenance_window,
					conflict_domain = EXCLUDED.conflict_domain,
					conflict_group_id = EXCLUDED.conflict_group_id,
					mutually_exclusive = EXCLUDED.mutually_exclusive,
					alternative_ids = EXCLUDED.alternative_ids,
					preferred_alternative_id = EXCLUDED.preferred_alternative_id
			`, string(rec.ID), string(tenant), string(f.ID), rec.Title, rec.Rationale, string(rec.Action),
				toJSON(rec.Parameters), toJSON(rec.CurrentState), toJSON(rec.ProposedState),
				recSavingMicros, annualMicros, implMicros, recCurrency, rec.PaybackDays,
				float64(rec.Confidence), toJSON(rec.ConfidenceBasis), toJSON(rec.Risk),
				toJSON(rec.BlastRadius), reversibilityOrDefault(rec.Reversibility),
				complexityOrDefault(rec.Complexity), rec.PriorityScore, rec.Rank, string(rec.Status),
				rec.StatusReason, nilableTime(rec.SnoozedUntil), rec.RequiresApproval,
				string(rec.PolicyDecisionID), rec.AutoExecutable, rec.Narrative, rec.MaintenanceWindow,
				string(rec.SupersedesID), string(f.AccountID), string(f.ResourceID), string(f.Environment),
				string(f.Category), string(f.RuleID), orNow(rec.CreatedAt),
				string(rec.ConflictDomain), rec.ConflictGroupID, rec.MutuallyExclusive,
				toJSON(rec.AlternativeIDs), string(rec.PreferredAlternativeID)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func reversibilityOrDefault(r optimize.Reversibility) string {
	if r == "" {
		return "fast"
	}
	return string(r)
}

func complexityOrDefault(c optimize.Complexity) string {
	if c == "" {
		return "low"
	}
	return string(c)
}

func nilableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func (r *RecommendationRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (optimize.Recommendation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return optimize.Recommendation{}, err
	}
	var out optimize.Recommendation
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, recommendationSelectSQL+` WHERE r.tenant_id = $1 AND r.id = $2`,
			string(tenant), string(id))
		rec, err := scanRecommendation(row)
		if err != nil {
			return mapErr(err)
		}
		out = rec
		return nil
	})
	return out, err
}

// List applies RecommendationFilter and keyset-paginates on id, matching
// every other List method in this package. Recommendations are read far
// more often by "highest priority open items" than by any cursor-stable
// order, but the ports.Page contract is a stable cursor across all list
// methods, so id remains the tiebreaker key here too; callers that want
// priority order pass opts.SortBy (handled below) and accept that a second
// page is a continuation of the id-ordered tail, not a priority-ordered one
// — exactly the same trade-off ResourceRepository.List makes.
func (r *RecommendationRepository) List(ctx context.Context, tenant core.TenantID, f ports.RecommendationFilter, opts ports.ListOptions) (ports.Page[optimize.Recommendation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[optimize.Recommendation]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[optimize.Recommendation]{}, err
	}
	var page ports.Page[optimize.Recommendation]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where, args := buildRecommendationFilter(tenant, f)
		if after != nil {
			args = append(args, after[0])
			where += " AND r.id > $" + strconv.Itoa(len(args))
		}
		order := "r.id"
		if opts.SortBy == "priority" {
			order = "r.priority_score DESC, r.id"
		}
		sql := recommendationSelectSQL + " WHERE " + where + " ORDER BY " + order + " LIMIT " + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []optimize.Recommendation
		for rows.Next() {
			rec, err := scanRecommendation(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, rec)
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

func (r *RecommendationRepository) UpdateStatus(ctx context.Context, tenant core.TenantID, id core.ID, status optimize.Status, reason, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	_ = by // audited by the calling service, not stored redundantly here
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE recommendations SET status = $3, status_reason = $4 WHERE tenant_id = $1 AND id = $2
		`, string(tenant), string(id), string(status), reason)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("recommendation", id)
		}
		return nil
	})
}

func (r *RecommendationRepository) Update(ctx context.Context, rec optimize.Recommendation) error {
	if err := core.GuardTenant(ctx, rec.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, rec.TenantID, func(ctx context.Context) error {
		savingMicros, currency := moneyMicros(rec.EstimatedMonthlySaving)
		annualMicros, _ := moneyMicros(rec.EstimatedAnnualSaving)
		implMicros, _ := moneyMicros(rec.ImplementationCost)
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE recommendations SET title=$3, rationale=$4, action=$5, parameters=$6, current_state=$7,
				proposed_state=$8, estimated_monthly_saving_micros=$9, estimated_annual_saving_micros=$10,
				implementation_cost_micros=$11, currency=$12, payback_days=$13, confidence=$14,
				confidence_basis=$15, risk=$16, blast_radius=$17, reversibility=$18, complexity=$19,
				priority_score=$20, rank=$21, status=$22, status_reason=$23, snoozed_until=$24,
				requires_approval=$25, policy_decision_id=NULLIF($26,''), auto_executable=$27,
				narrative=$28, maintenance_window=$29, conflict_domain=$30, conflict_group_id=$31,
				mutually_exclusive=$32, alternative_ids=$33, preferred_alternative_id=$34
			WHERE tenant_id = $1 AND id = $2
		`, string(rec.TenantID), string(rec.ID), rec.Title, rec.Rationale, string(rec.Action),
			toJSON(rec.Parameters), toJSON(rec.CurrentState), toJSON(rec.ProposedState), savingMicros,
			annualMicros, implMicros, currency, rec.PaybackDays, float64(rec.Confidence),
			toJSON(rec.ConfidenceBasis), toJSON(rec.Risk), toJSON(rec.BlastRadius),
			reversibilityOrDefault(rec.Reversibility), complexityOrDefault(rec.Complexity),
			rec.PriorityScore, rec.Rank, string(rec.Status), rec.StatusReason, nilableTime(rec.SnoozedUntil),
			rec.RequiresApproval, string(rec.PolicyDecisionID), rec.AutoExecutable, rec.Narrative,
			rec.MaintenanceWindow, string(rec.ConflictDomain), rec.ConflictGroupID,
			rec.MutuallyExclusive, toJSON(rec.AlternativeIDs), string(rec.PreferredAlternativeID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("recommendation", rec.ID)
		}
		return nil
	})
}

// SupersedeStale matches the memstore reference semantics exactly: a
// recommendation is only ever superseded if it is not already terminal and
// was created before the cutoff, which keeps a recommendation someone is
// actively reviewing from being silently swept away by the next analysis
// run landing mid-review.
func (r *RecommendationRepository) SupersedeStale(ctx context.Context, tenant core.TenantID, before time.Time, keepIDs []core.ID) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	marked := 0
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE recommendations SET status = 'superseded'
			WHERE tenant_id = $1 AND created_at < $2 AND NOT (id = ANY($3::text[]))
				AND status NOT IN ('executed', 'rejected', 'rolled_back', 'superseded', 'dismissed', 'failed')
		`, string(tenant), before, toStringSlice(keepIDs))
		if err != nil {
			return mapErr(err)
		}
		marked = int(tag.RowsAffected())
		return nil
	})
	return marked, err
}

func (r *RecommendationRepository) Summary(ctx context.Context, tenant core.TenantID) (ports.RecommendationSummary, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.RecommendationSummary{}, err
	}
	sum := ports.RecommendationSummary{
		TotalMonthlySaving: core.ZeroUSD(),
		ByCategory:         map[optimize.Category]int{},
		SavingByCategory:   map[optimize.Category]core.Money{},
		ByRisk:             map[core.RiskLevel]int{},
	}
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		rows, err := q.Query(ctx, `
			SELECT category, COALESCE(risk->>'level',''), estimated_monthly_saving_micros, currency,
				auto_executable, preferred_alternative_id
			FROM recommendations WHERE tenant_id = $1 AND status = 'open'
		`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var category, riskLevel, currency, preferredAlternativeID string
			var micros int64
			var autoExec bool
			if err := rows.Scan(&category, &riskLevel, &micros, &currency, &autoExec, &preferredAlternativeID); err != nil {
				return mapErr(err)
			}
			sum.Open++
			sum.ByCategory[optimize.Category(category)]++
			if riskLevel != "" {
				sum.ByRisk[core.RiskLevel(riskLevel)]++
			}
			if autoExec {
				sum.AutoExecutable++
			}
			// A non-empty preferred_alternative_id is the persisted form of
			// optimize.Recommendation.CountsTowardTotal() == false: this row
			// is one of several mutually exclusive answers to the same
			// problem, and CloudOptix recommends a different one. It is
			// counted above and excluded from the money below — see
			// ports.RecommendationSummary.
			if preferredAlternativeID != "" {
				sum.MutuallyExclusiveAlternatives++
				continue
			}
			amt := moneyFromMicros(micros, currency)
			sum.TotalMonthlySaving = sum.TotalMonthlySaving.MustAdd(amt)
			sum.SavingByCategory[optimize.Category(category)] = sum.SavingByCategory[optimize.Category(category)].MustAdd(amt)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		var awaiting int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM recommendations WHERE tenant_id = $1 AND status = 'under_review'`,
			string(tenant)).Scan(&awaiting); err != nil {
			return mapErr(err)
		}
		sum.AwaitingApproval = awaiting
		return nil
	})
	return sum, err
}

// buildRecommendationFilter is the pure filter-to-SQL builder for
// RecommendationFilter, mirroring buildResourceFilter's shape and unit-test
// coverage.
func buildRecommendationFilter(tenant core.TenantID, f ports.RecommendationFilter) (string, []any) {
	conds := []string{"r.tenant_id = $1"}
	args := []any{string(tenant)}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if len(f.Statuses) > 0 {
		conds = append(conds, "r.status = ANY("+arg(toStringSlice(f.Statuses))+"::text[])")
	}
	if len(f.Categories) > 0 {
		conds = append(conds, "r.category = ANY("+arg(toStringSlice(f.Categories))+"::text[])")
	}
	if len(f.Actions) > 0 {
		conds = append(conds, "r.action = ANY("+arg(toStringSlice(f.Actions))+"::text[])")
	}
	if len(f.RuleIDs) > 0 {
		conds = append(conds, "r.rule_id = ANY("+arg(toStringSlice(f.RuleIDs))+"::text[])")
	}
	if len(f.Environments) > 0 {
		conds = append(conds, "r.environment = ANY("+arg(toStringSlice(f.Environments))+"::text[])")
	}
	if len(f.AccountIDs) > 0 {
		conds = append(conds, "r.account_id = ANY("+arg(toStringSlice(f.AccountIDs))+"::text[])")
	}
	if !f.ApplicationID.IsZero() {
		conds = append(conds, "r.application_id = "+arg(string(f.ApplicationID)))
	}
	if !f.ResourceID.IsZero() {
		conds = append(conds, "r.resource_id = "+arg(string(f.ResourceID)))
	}
	if !f.MinSaving.IsZero() {
		micros, _ := moneyMicros(f.MinSaving)
		conds = append(conds, "r.estimated_monthly_saving_micros >= "+arg(micros))
	}
	if f.MinConfidence > 0 {
		conds = append(conds, "r.confidence >= "+arg(f.MinConfidence))
	}
	if f.MaxRisk != "" {
		conds = append(conds, riskOrderExpr+" <= "+arg(core.RiskLevel(f.MaxRisk).Order()))
	}
	if f.AutoExecutableOnly {
		conds = append(conds, "r.auto_executable = true")
	}
	return strings.Join(conds, " AND "), args
}

// riskOrderExpr mirrors core.RiskLevel.Order() in SQL so MaxRisk filtering
// does not require pulling every row back into Go to compare. The ELSE
// branch (2, "MEDIUM") matches Order()'s own default for an unrecognized
// level.
const riskOrderExpr = `(CASE r.risk->>'level'
	WHEN 'NONE' THEN 0 WHEN 'LOW' THEN 1 WHEN 'MEDIUM' THEN 2 WHEN 'HIGH' THEN 3 WHEN 'CRITICAL' THEN 4
	ELSE 2 END)`

const recommendationSelectSQL = `
	SELECT r.id, r.tenant_id, r.finding_id, r.title, r.rationale, r.action, r.parameters,
		r.current_state, r.proposed_state, r.estimated_monthly_saving_micros,
		r.estimated_annual_saving_micros, r.implementation_cost_micros, r.currency, r.payback_days,
		r.confidence, r.confidence_basis, r.risk, r.blast_radius, r.reversibility, r.complexity,
		r.priority_score, r.rank, r.status, r.status_reason, r.snoozed_until, r.requires_approval,
		COALESCE(r.policy_decision_id,''), r.auto_executable, r.narrative, r.maintenance_window,
		COALESCE(r.supersedes_id,''), r.created_at, r.updated_at,
		r.conflict_domain, r.conflict_group_id, r.mutually_exclusive, r.alternative_ids,
		r.preferred_alternative_id,
		f.id, f.tenant_id, f.rule_id, f.rule_name, f.category, f.resource_id, f.resource_name,
		f.resource_kind, f.account_id, f.region, f.environment, f.severity, f.summary, f.detail,
		f.evidence, f.current_monthly_cost_micros, f.estimated_monthly_saving_micros, f.currency,
		f.detected_at
	FROM recommendations r JOIN findings f ON f.id = r.finding_id`

func scanRecommendation(row rowScanner) (optimize.Recommendation, error) {
	var rec optimize.Recommendation
	var f optimize.Finding
	var parameters, currentState, proposedState, confidenceBasis, risk, blastRadius, alternativeIDs []byte
	var savingMicros, annualMicros, implMicros int64
	var currency string
	var snoozedUntil *time.Time
	var fCurMicros, fSavingMicros int64
	var fCurrency string
	var evidence []byte
	var findingID string // r.finding_id: redundant with f.id below, kept only to consume the column

	if err := row.Scan(
		&rec.ID, &rec.TenantID, &findingID, &rec.Title,
		&rec.Rationale, &rec.Action, &parameters, &currentState, &proposedState, &savingMicros,
		&annualMicros, &implMicros, &currency, &rec.PaybackDays, &rec.Confidence, &confidenceBasis,
		&risk, &blastRadius, &rec.Reversibility, &rec.Complexity, &rec.PriorityScore, &rec.Rank,
		&rec.Status, &rec.StatusReason, &snoozedUntil, &rec.RequiresApproval, &rec.PolicyDecisionID,
		&rec.AutoExecutable, &rec.Narrative, &rec.MaintenanceWindow, &rec.SupersedesID, &rec.CreatedAt,
		&rec.UpdatedAt,
		&rec.ConflictDomain, &rec.ConflictGroupID, &rec.MutuallyExclusive, &alternativeIDs,
		&rec.PreferredAlternativeID,
		&f.ID, &f.TenantID, &f.RuleID, &f.RuleName, &f.Category, &f.ResourceID, &f.ResourceName,
		&f.ResourceKind, &f.AccountID, &f.Region, &f.Environment, &f.Severity, &f.Summary, &f.Detail,
		&evidence, &fCurMicros, &fSavingMicros, &fCurrency, &f.DetectedAt,
	); err != nil {
		return optimize.Recommendation{}, err
	}
	_ = findingID
	rec.EstimatedMonthlySaving = moneyFromMicros(savingMicros, currency)
	rec.EstimatedAnnualSaving = moneyFromMicros(annualMicros, currency)
	rec.ImplementationCost = moneyFromMicros(implMicros, currency)
	rec.SnoozedUntil = snoozedUntil
	if err := fromJSON(parameters, &rec.Parameters); err != nil {
		return optimize.Recommendation{}, err
	}
	if err := fromJSON(currentState, &rec.CurrentState); err != nil {
		return optimize.Recommendation{}, err
	}
	if err := fromJSON(proposedState, &rec.ProposedState); err != nil {
		return optimize.Recommendation{}, err
	}
	if err := fromJSON(confidenceBasis, &rec.ConfidenceBasis); err != nil {
		return optimize.Recommendation{}, err
	}
	if err := fromJSON(risk, &rec.Risk); err != nil {
		return optimize.Recommendation{}, err
	}
	if err := fromJSON(blastRadius, &rec.BlastRadius); err != nil {
		return optimize.Recommendation{}, err
	}
	if err := fromJSON(alternativeIDs, &rec.AlternativeIDs); err != nil {
		return optimize.Recommendation{}, err
	}
	f.CurrentMonthlyCost = moneyFromMicros(fCurMicros, fCurrency)
	f.EstimatedMonthlySaving = moneyFromMicros(fSavingMicros, fCurrency)
	if err := fromJSON(evidence, &f.Evidence); err != nil {
		return optimize.Recommendation{}, err
	}
	rec.Finding = f
	return rec, nil
}
