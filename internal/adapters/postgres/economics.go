package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// EconomicsRepository is the pgx-backed ports.EconomicsRepository.
type EconomicsRepository struct{ db *DB }

// NewEconomicsRepository builds an EconomicsRepository over db.
func NewEconomicsRepository(db *DB) *EconomicsRepository { return &EconomicsRepository{db: db} }

var _ ports.EconomicsRepository = (*EconomicsRepository)(nil)

func (r *EconomicsRepository) SaveFootprints(ctx context.Context, tenant core.TenantID, fps []econ.Footprint) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if len(fps) == 0 {
		return nil
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		for _, fp := range fps {
			id := fp.ID
			if id.IsZero() {
				id = core.NewID("fpt")
			}
			direct, currency := moneyMicros(fp.Direct)
			indirect, _ := moneyMicros(fp.Indirect)
			shared, _ := moneyMicros(fp.Shared)
			total, _ := moneyMicros(fp.Total)
			unattributed, _ := moneyMicros(fp.Unattributed)
			priorTotal, _ := moneyMicros(fp.PriorTotal)
			if _, err := q.Exec(ctx, `
				INSERT INTO economic_footprints (id, tenant_id, scope, scope_id, label, period_start,
					period_end, direct_micros, indirect_micros, shared_micros, total_micros,
					unattributed_micros, currency, coverage, components, by_service, by_class,
					prior_total_micros, change_pct, computed_at, confidence)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
				ON CONFLICT (tenant_id, scope, scope_id, period_start, period_end) DO UPDATE SET
					label = EXCLUDED.label, direct_micros = EXCLUDED.direct_micros,
					indirect_micros = EXCLUDED.indirect_micros, shared_micros = EXCLUDED.shared_micros,
					total_micros = EXCLUDED.total_micros, unattributed_micros = EXCLUDED.unattributed_micros,
					coverage = EXCLUDED.coverage, components = EXCLUDED.components,
					by_service = EXCLUDED.by_service, by_class = EXCLUDED.by_class,
					prior_total_micros = EXCLUDED.prior_total_micros, change_pct = EXCLUDED.change_pct,
					computed_at = EXCLUDED.computed_at, confidence = EXCLUDED.confidence
			`, string(id), string(tenant), string(fp.Scope), string(fp.ScopeID), fp.Label,
				fp.Period.Start, fp.Period.End, direct, indirect, shared, total, unattributed, currency,
				fp.Coverage, toJSON(fp.Components), toJSON(fp.ByService), toJSON(fp.ByClass),
				priorTotal, fp.ChangePct, orNow(fp.ComputedAt), float64(fp.Confidence)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (r *EconomicsRepository) GetFootprint(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID, period core.Period) (econ.Footprint, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.Footprint{}, err
	}
	var out econ.Footprint
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		// Exact period match first (SaveFootprints' own upsert key), falling
		// back to the most recently computed footprint for the scope — the
		// same two-tier lookup the memstore reference performs.
		row := q.QueryRow(ctx, footprintSelectSQL+`
			WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3 AND period_start = $4 AND period_end = $5
		`, string(tenant), string(scope), string(scopeID), period.Start, period.End)
		fp, err := scanFootprint(row)
		if err == nil {
			out = fp
			return nil
		}
		if !isNoRows(err) {
			return mapErr(err)
		}
		row = q.QueryRow(ctx, footprintSelectSQL+`
			WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3
			ORDER BY computed_at DESC LIMIT 1
		`, string(tenant), string(scope), string(scopeID))
		fp, err = scanFootprint(row)
		if err != nil {
			if isNoRows(err) {
				return core.NotFound("footprint", scopeID)
			}
			return mapErr(err)
		}
		out = fp
		return nil
	})
	return out, err
}

func (r *EconomicsRepository) ListFootprints(ctx context.Context, tenant core.TenantID, scope econ.Scope, period core.Period) ([]econ.Footprint, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []econ.Footprint
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := footprintSelectSQL + ` WHERE tenant_id = $1 AND scope = $2`
		args := []any{string(tenant), string(scope)}
		if !period.IsZero() {
			// Overlap: existing.start < period.end AND existing.end > period.start.
			args = append(args, period.End, period.Start)
			sql += ` AND period_start < $3 AND period_end > $4`
		}
		sql += ` ORDER BY computed_at DESC`
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			fp, err := scanFootprint(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, fp)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *EconomicsRepository) UpsertTransaction(ctx context.Context, t econ.BusinessTransaction) error {
	if err := core.GuardTenant(ctx, t.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, t.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO business_transactions (id, tenant_id, name, description, application_id,
				workload_ids, path_share, volume_source, provenance, criticality, created_at, updated_at)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$11)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name, description = EXCLUDED.description,
				application_id = EXCLUDED.application_id, workload_ids = EXCLUDED.workload_ids,
				path_share = EXCLUDED.path_share, volume_source = EXCLUDED.volume_source,
				provenance = EXCLUDED.provenance, criticality = EXCLUDED.criticality
		`, string(t.ID), string(t.TenantID), t.Name, t.Description, string(t.ApplicationID),
			toJSON(t.WorkloadIDs), toJSON(t.PathShare), toJSON(t.VolumeSource),
			provenanceOrUnknown(t.Provenance), criticalityOrUnset(t.Criticality), orNow(t.CreatedAt))
		return mapErr(err)
	})
}

func provenanceOrUnknown(p core.Provenance) string {
	if p == "" {
		return string(core.ProvenanceUnknown)
	}
	return string(p)
}

func (r *EconomicsRepository) GetTransaction(ctx context.Context, tenant core.TenantID, id core.ID) (econ.BusinessTransaction, error) {
	return r.getOneTransaction(ctx, tenant, `id = $2`, string(id))
}

func (r *EconomicsRepository) GetTransactionByName(ctx context.Context, tenant core.TenantID, name string) (econ.BusinessTransaction, error) {
	return r.getOneTransaction(ctx, tenant, `name = $2`, name)
}

func (r *EconomicsRepository) getOneTransaction(ctx context.Context, tenant core.TenantID, where string, arg any) (econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.BusinessTransaction{}, err
	}
	var out econ.BusinessTransaction
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, transactionSelectSQL+` WHERE tenant_id = $1 AND `+where,
			string(tenant), arg)
		t, err := scanTransaction(row)
		if err != nil {
			return mapErr(err)
		}
		out = t
		return nil
	})
	return out, err
}

func (r *EconomicsRepository) ListTransactions(ctx context.Context, tenant core.TenantID) ([]econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []econ.BusinessTransaction
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, transactionSelectSQL+` WHERE tenant_id = $1 ORDER BY name`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTransaction(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, t)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *EconomicsRepository) SaveUnitEconomics(ctx context.Context, tenant core.TenantID, ue []econ.UnitEconomics) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	if len(ue) == 0 {
		return nil
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		for _, u := range ue {
			id := u.ID
			if id.IsZero() {
				id = core.NewID("une")
			}
			total, currency := moneyMicros(u.TotalCost)
			perUnit, _ := moneyMicros(u.CostPerUnit)
			direct, _ := moneyMicros(u.DirectPerUnit)
			shared, _ := moneyMicros(u.SharedPerUnit)
			prior, _ := moneyMicros(u.PriorCostPerUnit)
			if _, err := q.Exec(ctx, `
				INSERT INTO unit_economics (id, tenant_id, transaction_id, name, period_start, period_end,
					volume, total_cost_micros, cost_per_unit_micros, direct_per_unit_micros,
					shared_per_unit_micros, currency, prior_cost_per_unit_micros, change_pct, drivers,
					confidence, volume_provenance, computed_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			`, string(id), string(tenant), string(u.TransactionID), u.Name, u.Period.Start, u.Period.End,
				u.Volume, total, perUnit, direct, shared, currency, prior, u.ChangePct, toJSON(u.Drivers),
				float64(u.Confidence), provenanceOrUnknown(u.VolumeProvenance), orNow(u.ComputedAt)); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (r *EconomicsRepository) ListUnitEconomics(ctx context.Context, tenant core.TenantID, transactionID core.ID, from, to time.Time) ([]econ.UnitEconomics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []econ.UnitEconomics
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where := `tenant_id = $1 AND transaction_id = $2`
		args := []any{string(tenant), string(transactionID)}
		if !from.IsZero() {
			args = append(args, from)
			where += ` AND period_start >= $3`
		}
		if !to.IsZero() {
			args = append(args, to)
			where += ` AND period_start < $` + strconv.Itoa(len(args))
		}
		sql := unitEconomicsSelectSQL + ` WHERE ` + where + ` ORDER BY period_start`
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUnitEconomics(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, u)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *EconomicsRepository) UpsertCostSLO(ctx context.Context, s econ.CostSLO) error {
	if err := core.GuardTenant(ctx, s.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, s.TenantID, func(ctx context.Context) error {
		target, currency := moneyMicros(s.Target)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO cost_slos (id, tenant_id, name, description, kind, direction, scope, scope_id,
				transaction_id, target_micros, target_currency, target_ratio, window_kind,
				error_budget_pct, breach_actions, owner, enabled, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)
			ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name, description = EXCLUDED.description, kind = EXCLUDED.kind,
				direction = EXCLUDED.direction, scope = EXCLUDED.scope, scope_id = EXCLUDED.scope_id,
				transaction_id = EXCLUDED.transaction_id, target_micros = EXCLUDED.target_micros,
				target_ratio = EXCLUDED.target_ratio, window_kind = EXCLUDED.window_kind,
				error_budget_pct = EXCLUDED.error_budget_pct, breach_actions = EXCLUDED.breach_actions,
				owner = EXCLUDED.owner, enabled = EXCLUDED.enabled
		`, string(s.ID), string(s.TenantID), s.Name, s.Description, string(s.Kind), string(s.Direction),
			string(s.Scope), string(s.ScopeID), string(s.TransactionID), target, currency, s.TargetRatio,
			string(s.Window), s.ErrorBudgetPct, toJSON(s.BreachActions), s.Owner, s.Enabled,
			orNow(s.CreatedAt))
		return mapErr(err)
	})
}

func (r *EconomicsRepository) GetCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) (econ.CostSLO, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.CostSLO{}, err
	}
	var out econ.CostSLO
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, costSLOSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		s, err := scanCostSLO(row)
		if err != nil {
			return mapErr(err)
		}
		out = s
		return nil
	})
	return out, err
}

func (r *EconomicsRepository) ListCostSLOs(ctx context.Context, tenant core.TenantID) ([]econ.CostSLO, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []econ.CostSLO
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, costSLOSelectSQL+` WHERE tenant_id = $1 ORDER BY name`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanCostSLO(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, s)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *EconomicsRepository) DeleteCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `DELETE FROM cost_slos WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("cost_slo", id)
		}
		return nil
	})
}

func (r *EconomicsRepository) SaveBudgetState(ctx context.Context, b econ.EconomicErrorBudget) error {
	if err := core.GuardTenant(ctx, b.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, b.TenantID, func(ctx context.Context) error {
		id := b.ID
		if id.IsZero() {
			id = core.NewID("eeb")
		}
		target, currency := moneyMicros(b.Target)
		budgetAmount, _ := moneyMicros(b.BudgetAmount)
		actual, _ := moneyMicros(b.Actual)
		consumed, _ := moneyMicros(b.Consumed)
		remaining, _ := moneyMicros(b.Remaining)
		eow, _ := moneyMicros(b.ProjectedEndOfWindow)
		overage, _ := moneyMicros(b.ProjectedOverage)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO economic_error_budgets (id, tenant_id, slo_id, slo_name, kind, period_start,
				period_end, target_micros, budget_amount_micros, actual_micros, consumed_micros,
				remaining_micros, currency, consumed_ratio, burn_rate, projected_eow_micros,
				projected_overage_micros, exhaustion_date, state, triggered_actions, explanation,
				evaluated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
			ON CONFLICT (tenant_id, slo_id, period_start) DO UPDATE SET
				actual_micros = EXCLUDED.actual_micros, consumed_micros = EXCLUDED.consumed_micros,
				remaining_micros = EXCLUDED.remaining_micros, consumed_ratio = EXCLUDED.consumed_ratio,
				burn_rate = EXCLUDED.burn_rate, projected_eow_micros = EXCLUDED.projected_eow_micros,
				projected_overage_micros = EXCLUDED.projected_overage_micros,
				exhaustion_date = EXCLUDED.exhaustion_date, state = EXCLUDED.state,
				triggered_actions = EXCLUDED.triggered_actions, explanation = EXCLUDED.explanation,
				evaluated_at = EXCLUDED.evaluated_at
		`, string(id), string(b.TenantID), string(b.SLOID), b.SLOName, string(b.Kind), b.Period.Start,
			b.Period.End, target, budgetAmount, actual, consumed, remaining, currency, b.ConsumedRatio,
			b.BurnRate, eow, overage, b.ExhaustionDate, string(b.State), toJSON(b.TriggeredActions),
			b.Explanation, orNow(b.EvaluatedAt))
		return mapErr(err)
	})
}

func (r *EconomicsRepository) ListBudgetStates(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []econ.EconomicErrorBudget
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		// Most recent evaluation per SLO: DISTINCT ON rides
		// idx_error_budgets_tenant_slo (tenant_id, slo_id, evaluated_at DESC).
		rows, err := r.db.querier(ctx).Query(ctx, `
			SELECT DISTINCT ON (slo_id) `+budgetColumns+`
			FROM economic_error_budgets WHERE tenant_id = $1 ORDER BY slo_id, evaluated_at DESC
		`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBudget(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, b)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *EconomicsRepository) SaveEfficiencyScore(ctx context.Context, s econ.EfficiencyScore) error {
	if err := core.GuardTenant(ctx, s.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, s.TenantID, func(ctx context.Context) error {
		id := s.ID
		if id.IsZero() {
			id = core.NewID("efs")
		}
		totalSpend, currency := moneyMicros(s.TotalSpend)
		waste, _ := moneyMicros(s.IdentifiedWaste)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO efficiency_scores (id, tenant_id, scope, scope_id, label, period_start,
				period_end, score, grade, factors, prior_score, delta, waste_ratio, total_spend_micros,
				identified_waste_micros, currency, computed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		`, string(id), string(s.TenantID), string(s.Scope), string(s.ScopeID), s.Label, s.Period.Start,
			s.Period.End, s.Score, s.Grade, toJSON(s.Factors), s.PriorScore, s.Delta, s.WasteRatio,
			totalSpend, waste, currency, orNow(s.ComputedAt))
		return mapErr(err)
	})
}

func (r *EconomicsRepository) GetEfficiencyScore(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID) (econ.EfficiencyScore, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.EfficiencyScore{}, err
	}
	var out econ.EfficiencyScore
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, efficiencyScoreSelectSQL+`
			WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3 ORDER BY computed_at DESC LIMIT 1
		`, string(tenant), string(scope), string(scopeID))
		s, err := scanEfficiencyScore(row)
		if err != nil {
			if isNoRows(err) {
				return core.NotFound("efficiency_score", scopeID)
			}
			return mapErr(err)
		}
		out = s
		return nil
	})
	return out, err
}

func (r *EconomicsRepository) ListEfficiencyScores(ctx context.Context, tenant core.TenantID, scope econ.Scope) ([]econ.EfficiencyScore, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []econ.EfficiencyScore
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, efficiencyScoreSelectSQL+`
			WHERE tenant_id = $1 AND scope = $2 ORDER BY computed_at DESC
		`, string(tenant), string(scope))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanEfficiencyScore(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, s)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

const footprintSelectSQL = `
	SELECT id, tenant_id, scope, scope_id, label, period_start, period_end, direct_micros,
		indirect_micros, shared_micros, total_micros, unattributed_micros, currency, coverage,
		components, by_service, by_class, prior_total_micros, change_pct, computed_at, confidence
	FROM economic_footprints`

func scanFootprint(row rowScanner) (econ.Footprint, error) {
	var fp econ.Footprint
	var start, end time.Time
	var direct, indirect, shared, total, unattributed, priorTotal int64
	var currency string
	var components, byService, byClass []byte
	if err := row.Scan(&fp.ID, &fp.TenantID, &fp.Scope, &fp.ScopeID, &fp.Label, &start, &end, &direct,
		&indirect, &shared, &total, &unattributed, &currency, &fp.Coverage, &components, &byService,
		&byClass, &priorTotal, &fp.ChangePct, &fp.ComputedAt, &fp.Confidence); err != nil {
		return econ.Footprint{}, err
	}
	fp.Period = core.NewPeriod(start, end)
	fp.Direct = moneyFromMicros(direct, currency)
	fp.Indirect = moneyFromMicros(indirect, currency)
	fp.Shared = moneyFromMicros(shared, currency)
	fp.Total = moneyFromMicros(total, currency)
	fp.Unattributed = moneyFromMicros(unattributed, currency)
	fp.PriorTotal = moneyFromMicros(priorTotal, currency)
	if err := fromJSON(components, &fp.Components); err != nil {
		return econ.Footprint{}, err
	}
	if err := fromJSON(byService, &fp.ByService); err != nil {
		return econ.Footprint{}, err
	}
	if err := fromJSON(byClass, &fp.ByClass); err != nil {
		return econ.Footprint{}, err
	}
	return fp, nil
}

const transactionSelectSQL = `
	SELECT id, tenant_id, name, description, COALESCE(application_id,''), workload_ids, path_share,
		volume_source, provenance, criticality, created_at, updated_at
	FROM business_transactions`

func scanTransaction(row rowScanner) (econ.BusinessTransaction, error) {
	var t econ.BusinessTransaction
	var workloadIDs, pathShare, volumeSource []byte
	if err := row.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.ApplicationID, &workloadIDs,
		&pathShare, &volumeSource, &t.Provenance, &t.Criticality, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return econ.BusinessTransaction{}, err
	}
	if err := fromJSON(workloadIDs, &t.WorkloadIDs); err != nil {
		return econ.BusinessTransaction{}, err
	}
	if err := fromJSON(pathShare, &t.PathShare); err != nil {
		return econ.BusinessTransaction{}, err
	}
	if err := fromJSON(volumeSource, &t.VolumeSource); err != nil {
		return econ.BusinessTransaction{}, err
	}
	return t, nil
}

const unitEconomicsSelectSQL = `
	SELECT id, tenant_id, transaction_id, name, period_start, period_end, volume, total_cost_micros,
		cost_per_unit_micros, direct_per_unit_micros, shared_per_unit_micros, currency,
		prior_cost_per_unit_micros, change_pct, drivers, confidence, volume_provenance, computed_at
	FROM unit_economics`

func scanUnitEconomics(row rowScanner) (econ.UnitEconomics, error) {
	var u econ.UnitEconomics
	var start, end time.Time
	var total, perUnit, direct, shared, prior int64
	var currency string
	var drivers []byte
	if err := row.Scan(&u.ID, &u.TenantID, &u.TransactionID, &u.Name, &start, &end, &u.Volume, &total,
		&perUnit, &direct, &shared, &currency, &prior, &u.ChangePct, &drivers, &u.Confidence,
		&u.VolumeProvenance, &u.ComputedAt); err != nil {
		return econ.UnitEconomics{}, err
	}
	u.Period = core.NewPeriod(start, end)
	u.TotalCost = moneyFromMicros(total, currency)
	u.CostPerUnit = moneyFromMicros(perUnit, currency)
	u.DirectPerUnit = moneyFromMicros(direct, currency)
	u.SharedPerUnit = moneyFromMicros(shared, currency)
	u.PriorCostPerUnit = moneyFromMicros(prior, currency)
	if err := fromJSON(drivers, &u.Drivers); err != nil {
		return econ.UnitEconomics{}, err
	}
	return u, nil
}

const costSLOSelectSQL = `
	SELECT id, tenant_id, name, description, kind, direction, scope, scope_id,
		COALESCE(transaction_id,''), target_micros, target_currency, target_ratio, window_kind,
		error_budget_pct, breach_actions, owner, enabled, created_at, updated_at
	FROM cost_slos`

func scanCostSLO(row rowScanner) (econ.CostSLO, error) {
	var s econ.CostSLO
	var target int64
	var currency string
	var breachActions []byte
	if err := row.Scan(&s.ID, &s.TenantID, &s.Name, &s.Description, &s.Kind, &s.Direction, &s.Scope,
		&s.ScopeID, &s.TransactionID, &target, &currency, &s.TargetRatio, &s.Window, &s.ErrorBudgetPct,
		&breachActions, &s.Owner, &s.Enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return econ.CostSLO{}, err
	}
	s.Target = moneyFromMicros(target, currency)
	if err := fromJSON(breachActions, &s.BreachActions); err != nil {
		return econ.CostSLO{}, err
	}
	return s, nil
}

const budgetColumns = `id, tenant_id, slo_id, slo_name, kind, period_start, period_end, target_micros,
	budget_amount_micros, actual_micros, consumed_micros, remaining_micros, currency, consumed_ratio,
	burn_rate, projected_eow_micros, projected_overage_micros, exhaustion_date, state,
	triggered_actions, explanation, evaluated_at`

func scanBudget(row rowScanner) (econ.EconomicErrorBudget, error) {
	var b econ.EconomicErrorBudget
	var start, end time.Time
	var target, budgetAmount, actual, consumed, remaining, eow, overage int64
	var currency string
	var triggeredActions []byte
	if err := row.Scan(&b.ID, &b.TenantID, &b.SLOID, &b.SLOName, &b.Kind, &start, &end, &target,
		&budgetAmount, &actual, &consumed, &remaining, &currency, &b.ConsumedRatio, &b.BurnRate, &eow,
		&overage, &b.ExhaustionDate, &b.State, &triggeredActions, &b.Explanation, &b.EvaluatedAt); err != nil {
		return econ.EconomicErrorBudget{}, err
	}
	b.Period = core.NewPeriod(start, end)
	b.Target = moneyFromMicros(target, currency)
	b.BudgetAmount = moneyFromMicros(budgetAmount, currency)
	b.Actual = moneyFromMicros(actual, currency)
	b.Consumed = moneyFromMicros(consumed, currency)
	b.Remaining = moneyFromMicros(remaining, currency)
	b.ProjectedEndOfWindow = moneyFromMicros(eow, currency)
	b.ProjectedOverage = moneyFromMicros(overage, currency)
	if err := fromJSON(triggeredActions, &b.TriggeredActions); err != nil {
		return econ.EconomicErrorBudget{}, err
	}
	return b, nil
}

const efficiencyScoreSelectSQL = `
	SELECT id, tenant_id, scope, scope_id, label, period_start, period_end, score, grade, factors,
		prior_score, delta, waste_ratio, total_spend_micros, identified_waste_micros, currency,
		computed_at
	FROM efficiency_scores`

func scanEfficiencyScore(row rowScanner) (econ.EfficiencyScore, error) {
	var s econ.EfficiencyScore
	var start, end time.Time
	var totalSpend, waste int64
	var currency string
	var factors []byte
	if err := row.Scan(&s.ID, &s.TenantID, &s.Scope, &s.ScopeID, &s.Label, &start, &end, &s.Score,
		&s.Grade, &factors, &s.PriorScore, &s.Delta, &s.WasteRatio, &totalSpend, &waste, &currency,
		&s.ComputedAt); err != nil {
		return econ.EfficiencyScore{}, err
	}
	s.Period = core.NewPeriod(start, end)
	s.TotalSpend = moneyFromMicros(totalSpend, currency)
	s.IdentifiedWaste = moneyFromMicros(waste, currency)
	if err := fromJSON(factors, &s.Factors); err != nil {
		return econ.EfficiencyScore{}, err
	}
	return s, nil
}
