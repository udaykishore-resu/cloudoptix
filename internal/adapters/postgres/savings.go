package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// SavingsRepository is the pgx-backed ports.SavingsRepository.
type SavingsRepository struct{ db *DB }

// NewSavingsRepository builds a SavingsRepository over db.
func NewSavingsRepository(db *DB) *SavingsRepository { return &SavingsRepository{db: db} }

var _ ports.SavingsRepository = (*SavingsRepository)(nil)

// Save upserts on (tenant, recommendation_id) — see
// migrations/0010_execute.up.sql's comment: a savings record is one row per
// recommendation, mutated in place as it moves down the ladder, not a new
// row per stage transition.
func (r *SavingsRepository) Save(ctx context.Context, rec execute.SavingsRecord) error {
	if err := core.GuardTenant(ctx, rec.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, rec.TenantID, func(ctx context.Context) error {
		id := rec.ID
		if id.IsZero() {
			id = core.NewID("svr")
		}
		potential, currency := moneyMicros(rec.PotentialMonthly)
		approved, _ := moneyMicros(rec.ApprovedMonthly)
		executed, _ := moneyMicros(rec.ExecutedMonthly)
		validated, _ := moneyMicros(rec.ValidatedMonthly)
		realized, _ := moneyMicros(rec.RealizedMonthly)
		baseline, _ := moneyMicros(rec.BaselineCost)
		postChange, _ := moneyMicros(rec.PostChangeCost)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO savings_records (id, tenant_id, recommendation_id, plan_id, rule_id, action,
				resource_id, application_id, environment, stage, potential_monthly_micros,
				approved_monthly_micros, executed_monthly_micros, validated_monthly_micros,
				realized_monthly_micros, currency, baseline_cost_micros, post_change_cost_micros,
				measured_window_start, measured_window_end, stage_history, lost, lost_reason, created_at,
				updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$24)
			ON CONFLICT (tenant_id, recommendation_id) DO UPDATE SET
				plan_id = EXCLUDED.plan_id, stage = EXCLUDED.stage,
				potential_monthly_micros = EXCLUDED.potential_monthly_micros,
				approved_monthly_micros = EXCLUDED.approved_monthly_micros,
				executed_monthly_micros = EXCLUDED.executed_monthly_micros,
				validated_monthly_micros = EXCLUDED.validated_monthly_micros,
				realized_monthly_micros = EXCLUDED.realized_monthly_micros,
				baseline_cost_micros = EXCLUDED.baseline_cost_micros,
				post_change_cost_micros = EXCLUDED.post_change_cost_micros,
				measured_window_start = EXCLUDED.measured_window_start,
				measured_window_end = EXCLUDED.measured_window_end, stage_history = EXCLUDED.stage_history,
				lost = EXCLUDED.lost, lost_reason = EXCLUDED.lost_reason
		`, string(id), string(rec.TenantID), string(rec.RecommendationID), string(rec.PlanID),
			string(rec.RuleID), string(rec.Action), string(rec.ResourceID), string(rec.ApplicationID),
			string(rec.Environment), string(rec.Stage), potential, approved, executed, validated,
			realized, currency, baseline, postChange, zeroToNil(rec.MeasuredWindow.Start),
			zeroToNil(rec.MeasuredWindow.End), toJSON(rec.StageHistory), rec.Lost, rec.LostReason,
			orNow(rec.CreatedAt))
		return mapErr(err)
	})
}

func (r *SavingsRepository) Get(ctx context.Context, tenant core.TenantID, recommendationID core.ID) (execute.SavingsRecord, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.SavingsRecord{}, err
	}
	var out execute.SavingsRecord
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, savingsSelectSQL+` WHERE tenant_id = $1 AND recommendation_id = $2`,
			string(tenant), string(recommendationID))
		rec, err := scanSavingsRecord(row)
		if err != nil {
			return mapErr(err)
		}
		out = rec
		return nil
	})
	return out, err
}

func (r *SavingsRepository) List(ctx context.Context, tenant core.TenantID, period core.Period) ([]execute.SavingsRecord, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []execute.SavingsRecord
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		recs, err := loadSavingsRecords(ctx, r.db.querier(ctx), tenant, period)
		if err != nil {
			return err
		}
		out = recs
		return nil
	})
	return out, err
}

// Funnel delegates the rollup arithmetic to execute.BuildFunnel so this
// adapter and the memstore one can never disagree about how the funnel is
// computed — the same reuse pattern cost.NewBreakdown establishes.
func (r *SavingsRepository) Funnel(ctx context.Context, tenant core.TenantID, period core.Period) (execute.Funnel, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.Funnel{}, err
	}
	var out execute.Funnel
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		recs, err := loadSavingsRecords(ctx, r.db.querier(ctx), tenant, period)
		if err != nil {
			return err
		}
		out = execute.BuildFunnel(tenant, period, recs)
		return nil
	})
	return out, err
}

func loadSavingsRecords(ctx context.Context, q Querier, tenant core.TenantID, period core.Period) ([]execute.SavingsRecord, error) {
	sql := savingsSelectSQL + ` WHERE tenant_id = $1`
	args := []any{string(tenant)}
	if !period.IsZero() {
		args = append(args, period.Start, period.End)
		sql += ` AND created_at >= $2 AND created_at < $3`
	}
	sql += ` ORDER BY id`
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []execute.SavingsRecord
	for rows.Next() {
		rec, err := scanSavingsRecord(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, rec)
	}
	return out, mapErr(rows.Err())
}

func (r *SavingsRepository) SaveOutcome(ctx context.Context, o execute.Outcome) error {
	if err := core.GuardTenant(ctx, o.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, o.TenantID, func(ctx context.Context) error {
		id := o.ID
		if id.IsZero() {
			id = core.NewID("otc")
		}
		predicted, currency := moneyMicros(o.PredictedMonthlySaving)
		actual, _ := moneyMicros(o.ActualMonthlySaving)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO optimization_outcomes (id, tenant_id, rule_id, action, resource_kind, environment,
				predicted_monthly_saving_micros, actual_monthly_saving_micros, currency,
				predicted_confidence, predicted_risk, verdict, rolled_back, performance_impact_pct,
				availability_impact_pct, saving_ratio, observed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		`, string(id), string(o.TenantID), string(o.RuleID), string(o.Action), o.ResourceKind,
			string(o.Environment), predicted, actual, currency, float64(o.PredictedConfidence),
			string(o.PredictedRisk), string(o.Verdict), o.RolledBack, o.PerformanceImpact,
			o.AvailabilityImpact, o.SavingRatio, orNow(o.ObservedAt))
		return mapErr(err)
	})
}

func (r *SavingsRepository) ListOutcomes(ctx context.Context, tenant core.TenantID, ruleID optimize.RuleID, limit int) ([]execute.Outcome, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	var out []execute.Outcome
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := `SELECT id, tenant_id, rule_id, action, resource_kind, environment,
				predicted_monthly_saving_micros, actual_monthly_saving_micros, currency,
				predicted_confidence, predicted_risk, verdict, rolled_back, performance_impact_pct,
				availability_impact_pct, saving_ratio, observed_at
			FROM optimization_outcomes WHERE tenant_id = $1`
		args := []any{string(tenant)}
		if ruleID != "" {
			args = append(args, string(ruleID))
			sql += ` AND rule_id = $2`
		}
		args = append(args, limit)
		sql += ` ORDER BY observed_at DESC LIMIT $` + strconv.Itoa(len(args))
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var o execute.Outcome
			var predicted, actual int64
			var currency string
			if err := rows.Scan(&o.ID, &o.TenantID, &o.RuleID, &o.Action, &o.ResourceKind, &o.Environment,
				&predicted, &actual, &currency, &o.PredictedConfidence, &o.PredictedRisk, &o.Verdict,
				&o.RolledBack, &o.PerformanceImpact, &o.AvailabilityImpact, &o.SavingRatio,
				&o.ObservedAt); err != nil {
				return mapErr(err)
			}
			o.PredictedMonthlySaving = moneyFromMicros(predicted, currency)
			o.ActualMonthlySaving = moneyFromMicros(actual, currency)
			out = append(out, o)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *SavingsRepository) SaveCalibration(ctx context.Context, c execute.RuleCalibration) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, c.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO rule_calibrations (tenant_id, rule_id, samples, success_rate, rollback_rate,
				mean_saving_ratio, median_saving_ratio, confidence_multiplier, saving_multiplier, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (tenant_id, rule_id) DO UPDATE SET
				samples = EXCLUDED.samples, success_rate = EXCLUDED.success_rate,
				rollback_rate = EXCLUDED.rollback_rate, mean_saving_ratio = EXCLUDED.mean_saving_ratio,
				median_saving_ratio = EXCLUDED.median_saving_ratio,
				confidence_multiplier = EXCLUDED.confidence_multiplier,
				saving_multiplier = EXCLUDED.saving_multiplier, updated_at = EXCLUDED.updated_at
		`, string(c.TenantID), string(c.RuleID), c.Samples, c.SuccessRate, c.RollbackRate,
			c.MeanSavingRatio, c.MedianSavingRatio, c.ConfidenceMultiplier, c.SavingMultiplier,
			orNow(c.UpdatedAt))
		return mapErr(err)
	})
}

func (r *SavingsRepository) LoadCalibrations(ctx context.Context, tenant core.TenantID) (map[optimize.RuleID]execute.RuleCalibration, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	out := map[optimize.RuleID]execute.RuleCalibration{}
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, `
			SELECT tenant_id, rule_id, samples, success_rate, rollback_rate, mean_saving_ratio,
				median_saving_ratio, confidence_multiplier, saving_multiplier, updated_at
			FROM rule_calibrations WHERE tenant_id = $1
		`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c execute.RuleCalibration
			if err := rows.Scan(&c.TenantID, &c.RuleID, &c.Samples, &c.SuccessRate, &c.RollbackRate,
				&c.MeanSavingRatio, &c.MedianSavingRatio, &c.ConfidenceMultiplier, &c.SavingMultiplier,
				&c.UpdatedAt); err != nil {
				return mapErr(err)
			}
			out[c.RuleID] = c
		}
		return mapErr(rows.Err())
	})
	return out, err
}

const savingsSelectSQL = `
	SELECT id, tenant_id, recommendation_id, plan_id, rule_id, action, resource_id, application_id,
		environment, stage, potential_monthly_micros, approved_monthly_micros, executed_monthly_micros,
		validated_monthly_micros, realized_monthly_micros, currency, baseline_cost_micros,
		post_change_cost_micros, measured_window_start, measured_window_end, stage_history, lost,
		lost_reason, created_at, updated_at
	FROM savings_records`

func scanSavingsRecord(row rowScanner) (execute.SavingsRecord, error) {
	var rec execute.SavingsRecord
	var potential, approved, executed, validated, realized, baseline, postChange int64
	var currency string
	var windowStart, windowEnd *time.Time
	var stageHistory []byte
	if err := row.Scan(&rec.ID, &rec.TenantID, &rec.RecommendationID, &rec.PlanID, &rec.RuleID,
		&rec.Action, &rec.ResourceID, &rec.ApplicationID, &rec.Environment, &rec.Stage, &potential,
		&approved, &executed, &validated, &realized, &currency, &baseline, &postChange, &windowStart,
		&windowEnd, &stageHistory, &rec.Lost, &rec.LostReason, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return execute.SavingsRecord{}, err
	}
	rec.PotentialMonthly = moneyFromMicros(potential, currency)
	rec.ApprovedMonthly = moneyFromMicros(approved, currency)
	rec.ExecutedMonthly = moneyFromMicros(executed, currency)
	rec.ValidatedMonthly = moneyFromMicros(validated, currency)
	rec.RealizedMonthly = moneyFromMicros(realized, currency)
	rec.BaselineCost = moneyFromMicros(baseline, currency)
	rec.PostChangeCost = moneyFromMicros(postChange, currency)
	rec.MeasuredWindow = core.NewPeriod(nilToZero(windowStart), nilToZero(windowEnd))
	if err := fromJSON(stageHistory, &rec.StageHistory); err != nil {
		return execute.SavingsRecord{}, err
	}
	return rec, nil
}
