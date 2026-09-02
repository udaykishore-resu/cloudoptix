package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ExecutionRepository is the pgx-backed ports.ExecutionRepository.
type ExecutionRepository struct{ db *DB }

// NewExecutionRepository builds an ExecutionRepository over db.
func NewExecutionRepository(db *DB) *ExecutionRepository { return &ExecutionRepository{db: db} }

var _ ports.ExecutionRepository = (*ExecutionRepository)(nil)

// CreatePlan writes the plan row, its forward steps (phase='forward'), and,
// if the caller already constructed one, its rollback plan and reverse
// steps (phase='rollback') — all in one transaction, because a plan without
// its rollback story committed is exactly the state Plan.Executable refuses
// to run from, and a partial write must never look like a complete one.
//
// p.Snapshots is deliberately not persisted here: the memstore reference
// implementation this package matches keeps that field decoupled from
// SaveSnapshot/GetSnapshot too (SaveSnapshot writes into its own map, never
// back into the stored Plan struct), so Snapshots is not this schema's
// source of truth for snapshot data — the execution_snapshots table and its
// own SaveSnapshot/GetSnapshot pair are.
func (r *ExecutionRepository) CreatePlan(ctx context.Context, p execute.Plan) error {
	if err := core.GuardTenant(ctx, p.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		expected, currency := moneyMicros(p.ExpectedMonthlySaving)
		baseline, _ := moneyMicros(p.BaselineMonthlyCost)
		if _, err := q.Exec(ctx, `
			INSERT INTO execution_plans (id, tenant_id, recommendation_id, action, title, account_id,
				region, environment, resource_ids, validation, expected_monthly_saving_micros,
				baseline_monthly_cost_micros, currency, state, state_reason, approval_id,
				policy_decision_id, scheduled_for, dry_run, requested_by, created_at, started_at,
				finished_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),NULLIF($17,''),$18,
				$19,$20,$21,$22,$23)
		`, string(p.ID), string(p.TenantID), string(p.RecommendationID), string(p.Action), p.Title,
			string(p.AccountID), string(p.Region), string(p.Environment), toJSON(p.ResourceIDs),
			toJSON(p.Validation), expected, baseline, currency, string(p.State), p.StateReason,
			string(p.ApprovalID), string(p.PolicyDecisionID), p.ScheduledFor, p.DryRun, p.RequestedBy,
			orNow(p.CreatedAt), p.StartedAt, p.FinishedAt); err != nil {
			return mapErr(err)
		}
		if err := replaceSteps(ctx, q, p.TenantID, p.ID, "forward", p.Steps); err != nil {
			return err
		}
		if p.Rollback != nil {
			if err := saveRollback(ctx, q, p.TenantID, p.ID, *p.Rollback); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ExecutionRepository) GetPlan(ctx context.Context, tenant core.TenantID, id core.ID) (execute.Plan, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.Plan{}, err
	}
	var out execute.Plan
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		row := q.QueryRow(ctx, planSelectSQL+` WHERE tenant_id = $1 AND id = $2`, string(tenant), string(id))
		p, err := scanPlan(row)
		if err != nil {
			return mapErr(err)
		}
		steps, err := loadSteps(ctx, q, id, "forward")
		if err != nil {
			return err
		}
		p.Steps = steps
		rb, err := loadRollback(ctx, q, tenant, id)
		if err != nil {
			return err
		}
		p.Rollback = rb
		out = p
		return nil
	})
	return out, err
}

// UpdatePlan rewrites the plan row and reconciles its step/rollback tables
// to whatever p now holds. replaceSteps deletes and reinserts rather than
// diffing: an execution plan's step count never changes after creation
// (Plan.Executable requires len(Steps) > 0 up front and nothing appends to
// it later), so this is a full replace of a small, bounded list, not the
// large-batch case ReplaceRelationships exists to avoid.
func (r *ExecutionRepository) UpdatePlan(ctx context.Context, p execute.Plan) error {
	if err := core.GuardTenant(ctx, p.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, p.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		expected, currency := moneyMicros(p.ExpectedMonthlySaving)
		baseline, _ := moneyMicros(p.BaselineMonthlyCost)
		tag, err := q.Exec(ctx, `
			UPDATE execution_plans SET recommendation_id=$3, action=$4, title=$5, account_id=$6, region=$7,
				environment=$8, resource_ids=$9, validation=$10, expected_monthly_saving_micros=$11,
				baseline_monthly_cost_micros=$12, currency=$13, state=$14, state_reason=$15,
				approval_id=NULLIF($16,''), policy_decision_id=NULLIF($17,''), scheduled_for=$18,
				dry_run=$19, started_at=$20, finished_at=$21
			WHERE tenant_id = $1 AND id = $2
		`, string(p.TenantID), string(p.ID), string(p.RecommendationID), string(p.Action), p.Title,
			string(p.AccountID), string(p.Region), string(p.Environment), toJSON(p.ResourceIDs),
			toJSON(p.Validation), expected, baseline, currency, string(p.State), p.StateReason,
			string(p.ApprovalID), string(p.PolicyDecisionID), p.ScheduledFor, p.DryRun, p.StartedAt,
			p.FinishedAt)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("execution_plan", p.ID)
		}
		if err := replaceSteps(ctx, q, p.TenantID, p.ID, "forward", p.Steps); err != nil {
			return err
		}
		if p.Rollback != nil {
			if err := saveRollback(ctx, q, p.TenantID, p.ID, *p.Rollback); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *ExecutionRepository) ListPlans(ctx context.Context, tenant core.TenantID, states []execute.PlanState, opts ports.ListOptions) (ports.Page[execute.Plan], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[execute.Plan]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[execute.Plan]{}, err
	}
	var page ports.Page[execute.Plan]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where := `tenant_id = $1`
		args := []any{string(tenant)}
		if len(states) > 0 {
			args = append(args, toStringSlice(states))
			where += ` AND state = ANY($` + strconv.Itoa(len(args)) + `::text[])`
		}
		if after != nil {
			args = append(args, after[0])
			where += ` AND id > $` + strconv.Itoa(len(args))
		}
		args = append(args, opts.Limit+1)
		sql := planSelectSQL + ` WHERE ` + where + ` ORDER BY id LIMIT $` + strconv.Itoa(len(args))
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []execute.Plan
		for rows.Next() {
			p, err := scanPlan(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, p)
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

// ClaimDuePlans and ClaimPlansAwaitingValidation both run under
// WithSystemScope: they are the background sweep across every tenant's due
// work, the same shape the ports.ExecutionRepository doc comment describes.
// The lease (claimed_by/claimed_until) plus a state-transitioning UPDATE ...
// RETURNING is what makes two workers polling the same instant safe: the
// second worker's UPDATE simply matches the rows the first one already
// moved out of the eligible state — no separate locking primitive needed.
func (r *ExecutionRepository) ClaimDuePlans(ctx context.Context, now time.Time, workerID string, limit int) ([]execute.Plan, error) {
	if limit <= 0 {
		limit = 50
	}
	var claimed []execute.Plan
	err := r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		rows, err := q.Query(ctx, `
			UPDATE execution_plans SET state = 'preflight', started_at = $1, claimed_by = $2,
				claimed_until = $1 + interval '5 minutes'
			WHERE id IN (
				SELECT id FROM execution_plans
				WHERE state IN ('approved','scheduled') AND (scheduled_for IS NULL OR scheduled_for <= $1)
					AND (claimed_until IS NULL OR claimed_until < $1)
				ORDER BY COALESCE(scheduled_for, created_at), id
				LIMIT $3
				FOR UPDATE SKIP LOCKED
			)
			RETURNING `+planReturningColumns, now, workerID, limit)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPlan(rows)
			if err != nil {
				return mapErr(err)
			}
			claimed = append(claimed, p)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		for i, p := range claimed {
			steps, err := loadSteps(ctx, q, p.ID, "forward")
			if err != nil {
				return err
			}
			claimed[i].Steps = steps
			rb, err := loadRollback(ctx, q, p.TenantID, p.ID)
			if err != nil {
				return err
			}
			claimed[i].Rollback = rb
		}
		return nil
	})
	return claimed, err
}

// ClaimPlansAwaitingValidation claims executed plans whose declared
// observation window (Plan.Validation.ObservationWindow, an interval
// serialised inside the validation JSONB blob) has elapsed since execution
// finished. The interval has to be extracted from JSONB in the WHERE
// clause — it is not a separate column — via a jsonb_extract_path cast to
// bigint nanoseconds, matching how toJSON(p.Validation) round-trips
// time.Duration (a plain integer number of nanoseconds under encoding/json).
func (r *ExecutionRepository) ClaimPlansAwaitingValidation(ctx context.Context, now time.Time, workerID string, limit int) ([]execute.Plan, error) {
	if limit <= 0 {
		limit = 50
	}
	var claimed []execute.Plan
	err := r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		rows, err := q.Query(ctx, `
			UPDATE execution_plans SET state = 'validating', claimed_by = $2,
				claimed_until = $1 + interval '5 minutes'
			WHERE id IN (
				SELECT id FROM execution_plans
				WHERE state = 'executed' AND finished_at IS NOT NULL
					AND finished_at + make_interval(secs => COALESCE((validation->>'observation_window')::bigint, 0) / 1000000000.0) <= $1
					AND (claimed_until IS NULL OR claimed_until < $1)
				ORDER BY finished_at, id
				LIMIT $3
				FOR UPDATE SKIP LOCKED
			)
			RETURNING `+planReturningColumns, now, workerID, limit)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPlan(rows)
			if err != nil {
				return mapErr(err)
			}
			claimed = append(claimed, p)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		for i, p := range claimed {
			steps, err := loadSteps(ctx, q, p.ID, "forward")
			if err != nil {
				return err
			}
			claimed[i].Steps = steps
			rb, err := loadRollback(ctx, q, p.TenantID, p.ID)
			if err != nil {
				return err
			}
			claimed[i].Rollback = rb
		}
		return nil
	})
	return claimed, err
}

func (r *ExecutionRepository) SaveSnapshot(ctx context.Context, snap execute.Snapshot) error {
	if err := core.GuardTenant(ctx, snap.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, snap.TenantID, func(ctx context.Context) error {
		id := snap.ID
		if id.IsZero() {
			id = core.NewID("snp")
		}
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO execution_snapshots (id, tenant_id, plan_id, resource_id, resource_arn, captured_at,
				attributes, backup_refs, digest)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (plan_id, resource_id) DO UPDATE SET
				resource_arn = EXCLUDED.resource_arn, captured_at = EXCLUDED.captured_at,
				attributes = EXCLUDED.attributes, backup_refs = EXCLUDED.backup_refs, digest = EXCLUDED.digest
		`, string(id), string(snap.TenantID), string(snap.PlanID), string(snap.ResourceID),
			string(snap.ResourceARN), orNow(snap.CapturedAt), toJSON(snap.Attributes),
			toJSON(snap.BackupRefs), snap.Digest)
		return mapErr(err)
	})
}

func (r *ExecutionRepository) GetSnapshot(ctx context.Context, tenant core.TenantID, planID core.ID, resourceID core.ID) (execute.Snapshot, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.Snapshot{}, err
	}
	var out execute.Snapshot
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT id, tenant_id, plan_id, resource_id, resource_arn, captured_at, attributes, backup_refs,
				digest
			FROM execution_snapshots WHERE tenant_id = $1 AND plan_id = $2 AND resource_id = $3
		`, string(tenant), string(planID), string(resourceID))
		s, err := scanSnapshot(row)
		if err != nil {
			return mapErr(err)
		}
		out = s
		return nil
	})
	return out, err
}

func (r *ExecutionRepository) SaveValidation(ctx context.Context, v execute.ValidationResult) error {
	if err := core.GuardTenant(ctx, v.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, v.TenantID, func(ctx context.Context) error {
		id := v.ID
		if id.IsZero() {
			id = core.NewID("val")
		}
		predicted, currency := moneyMicros(v.PredictedMonthlySaving)
		observed, _ := moneyMicros(v.ObservedMonthlySaving)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO validation_results (id, tenant_id, plan_id, verdict, explanation,
				baseline_window_start, baseline_window_end, observed_window_start, observed_window_end,
				checks, predicted_monthly_saving_micros, observed_monthly_saving_micros, currency,
				saving_accuracy, rollback_triggered, rollback_reason, evaluated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			ON CONFLICT (plan_id) DO UPDATE SET
				verdict = EXCLUDED.verdict, explanation = EXCLUDED.explanation,
				baseline_window_start = EXCLUDED.baseline_window_start,
				baseline_window_end = EXCLUDED.baseline_window_end,
				observed_window_start = EXCLUDED.observed_window_start,
				observed_window_end = EXCLUDED.observed_window_end, checks = EXCLUDED.checks,
				predicted_monthly_saving_micros = EXCLUDED.predicted_monthly_saving_micros,
				observed_monthly_saving_micros = EXCLUDED.observed_monthly_saving_micros,
				saving_accuracy = EXCLUDED.saving_accuracy, rollback_triggered = EXCLUDED.rollback_triggered,
				rollback_reason = EXCLUDED.rollback_reason, evaluated_at = EXCLUDED.evaluated_at
		`, string(id), string(v.TenantID), string(v.PlanID), string(v.Verdict), v.Explanation,
			zeroToNil(v.BaselineWindow.Start), zeroToNil(v.BaselineWindow.End),
			zeroToNil(v.ObservedWindow.Start), zeroToNil(v.ObservedWindow.End), toJSON(v.Checks),
			predicted, observed, currency, v.SavingAccuracy, v.RollbackTriggered, v.RollbackReason,
			orNow(v.EvaluatedAt))
		return mapErr(err)
	})
}

func (r *ExecutionRepository) GetValidation(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.ValidationResult, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.ValidationResult{}, err
	}
	var out execute.ValidationResult
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT id, tenant_id, plan_id, verdict, explanation, baseline_window_start,
				baseline_window_end, observed_window_start, observed_window_end, checks,
				predicted_monthly_saving_micros, observed_monthly_saving_micros, currency, saving_accuracy,
				rollback_triggered, rollback_reason, evaluated_at
			FROM validation_results WHERE tenant_id = $1 AND plan_id = $2
		`, string(tenant), string(planID))
		v, err := scanValidation(row)
		if err != nil {
			return mapErr(err)
		}
		out = v
		return nil
	})
	return out, err
}

// replaceSteps deletes and reinserts a plan's steps for one phase. See
// UpdatePlan's doc comment for why replace-rather-than-diff is correct for
// this small, bounded list.
func replaceSteps(ctx context.Context, q Querier, tenant core.TenantID, planID core.ID, phase string, steps []execute.Step) error {
	if _, err := q.Exec(ctx, `DELETE FROM execution_steps WHERE tenant_id = $1 AND plan_id = $2 AND phase = $3`,
		string(tenant), string(planID), phase); err != nil {
		return mapErr(err)
	}
	for i, s := range steps {
		id := s.ID
		if id.IsZero() {
			id = core.NewID("stp")
		}
		key := s.IdempotencyKey
		if key == "" {
			key = string(planID) + "/" + phase + "/" + strconv.Itoa(i)
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO execution_steps (id, tenant_id, plan_id, phase, ordinal, kind, name, describe,
				aws_action, target, parameters, idempotency_key, state, attempts, max_retries, started_at,
				finished_at, error, output, abort_on_failure)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		`, string(id), string(tenant), string(planID), phase, s.Ordinal, string(s.Kind), s.Name,
			s.Describe, s.AWSAction, s.Target, toJSON(s.Parameters), key, string(s.State), s.Attempts,
			s.MaxRetries, s.StartedAt, s.FinishedAt, s.Error, toJSON(s.Output), s.AbortOnFailure); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func loadSteps(ctx context.Context, q Querier, planID core.ID, phase string) ([]execute.Step, error) {
	rows, err := q.Query(ctx, `
		SELECT id, ordinal, kind, name, describe, aws_action, target, parameters, idempotency_key, state,
			attempts, max_retries, started_at, finished_at, error, output, abort_on_failure
		FROM execution_steps WHERE plan_id = $1 AND phase = $2 ORDER BY ordinal
	`, string(planID), phase)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []execute.Step
	for rows.Next() {
		var s execute.Step
		var parameters, output []byte
		if err := rows.Scan(&s.ID, &s.Ordinal, &s.Kind, &s.Name, &s.Describe, &s.AWSAction, &s.Target,
			&parameters, &s.IdempotencyKey, &s.State, &s.Attempts, &s.MaxRetries, &s.StartedAt,
			&s.FinishedAt, &s.Error, &output, &s.AbortOnFailure); err != nil {
			return nil, mapErr(err)
		}
		if err := fromJSON(parameters, &s.Parameters); err != nil {
			return nil, err
		}
		if err := fromJSON(output, &s.Output); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

func saveRollback(ctx context.Context, q Querier, tenant core.TenantID, planID core.ID, rb execute.RollbackPlan) error {
	id := rb.ID
	if id.IsZero() {
		id = core.NewID("rbp")
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO rollback_plans (id, tenant_id, plan_id, feasible, infeasible_reason,
			estimated_duration_ms, data_loss_risk, summary, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (plan_id) DO UPDATE SET
			feasible = EXCLUDED.feasible, infeasible_reason = EXCLUDED.infeasible_reason,
			estimated_duration_ms = EXCLUDED.estimated_duration_ms, data_loss_risk = EXCLUDED.data_loss_risk,
			summary = EXCLUDED.summary
	`, string(id), string(tenant), string(planID), rb.Feasible, rb.InfeasibleReason,
		rb.EstimatedDuration.Milliseconds(), string(rb.DataLossRisk), rb.Summary, orNow(rb.CreatedAt)); err != nil {
		return mapErr(err)
	}
	return replaceSteps(ctx, q, tenant, planID, "rollback", rb.Steps)
}

func loadRollback(ctx context.Context, q Querier, tenant core.TenantID, planID core.ID) (*execute.RollbackPlan, error) {
	row := q.QueryRow(ctx, `
		SELECT id, tenant_id, plan_id, feasible, infeasible_reason, estimated_duration_ms, data_loss_risk,
			summary, created_at
		FROM rollback_plans WHERE tenant_id = $1 AND plan_id = $2
	`, string(tenant), string(planID))
	var rb execute.RollbackPlan
	var durationMS int64
	if err := row.Scan(&rb.ID, &rb.TenantID, &rb.PlanID, &rb.Feasible, &rb.InfeasibleReason, &durationMS,
		&rb.DataLossRisk, &rb.Summary, &rb.CreatedAt); err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, mapErr(err)
	}
	rb.EstimatedDuration = time.Duration(durationMS) * time.Millisecond
	steps, err := loadSteps(ctx, q, planID, "rollback")
	if err != nil {
		return nil, err
	}
	rb.Steps = steps
	return &rb, nil
}

func scanSnapshot(row rowScanner) (execute.Snapshot, error) {
	var s execute.Snapshot
	var attributes, backupRefs []byte
	if err := row.Scan(&s.ID, &s.TenantID, &s.PlanID, &s.ResourceID, &s.ResourceARN, &s.CapturedAt,
		&attributes, &backupRefs, &s.Digest); err != nil {
		return execute.Snapshot{}, err
	}
	if err := fromJSON(attributes, &s.Attributes); err != nil {
		return execute.Snapshot{}, err
	}
	if err := fromJSON(backupRefs, &s.BackupRefs); err != nil {
		return execute.Snapshot{}, err
	}
	return s, nil
}

func scanValidation(row rowScanner) (execute.ValidationResult, error) {
	var v execute.ValidationResult
	var checks []byte
	var predicted, observed int64
	var currency string
	var baselineStart, baselineEnd, observedStart, observedEnd *time.Time
	if err := row.Scan(&v.ID, &v.TenantID, &v.PlanID, &v.Verdict, &v.Explanation, &baselineStart,
		&baselineEnd, &observedStart, &observedEnd, &checks, &predicted, &observed, &currency,
		&v.SavingAccuracy, &v.RollbackTriggered, &v.RollbackReason, &v.EvaluatedAt); err != nil {
		return execute.ValidationResult{}, err
	}
	v.BaselineWindow = core.NewPeriod(nilToZero(baselineStart), nilToZero(baselineEnd))
	v.ObservedWindow = core.NewPeriod(nilToZero(observedStart), nilToZero(observedEnd))
	v.PredictedMonthlySaving = moneyFromMicros(predicted, currency)
	v.ObservedMonthlySaving = moneyFromMicros(observed, currency)
	if err := fromJSON(checks, &v.Checks); err != nil {
		return execute.ValidationResult{}, err
	}
	return v, nil
}

const planReturningColumns = `id, tenant_id, recommendation_id, action, title, account_id, region,
	environment, resource_ids, validation, expected_monthly_saving_micros, baseline_monthly_cost_micros,
	currency, state, state_reason, COALESCE(approval_id,''), COALESCE(policy_decision_id,''),
	scheduled_for, dry_run, requested_by, created_at, started_at, finished_at`

const planSelectSQL = `SELECT ` + planReturningColumns + ` FROM execution_plans`

func scanPlan(row rowScanner) (execute.Plan, error) {
	var p execute.Plan
	var resourceIDs, validation []byte
	var micros, baselineMicros int64
	var currency string
	if err := row.Scan(&p.ID, &p.TenantID, &p.RecommendationID, &p.Action, &p.Title, &p.AccountID,
		&p.Region, &p.Environment, &resourceIDs, &validation, &micros, &baselineMicros, &currency,
		&p.State, &p.StateReason, &p.ApprovalID, &p.PolicyDecisionID, &p.ScheduledFor, &p.DryRun,
		&p.RequestedBy, &p.CreatedAt, &p.StartedAt, &p.FinishedAt); err != nil {
		return execute.Plan{}, err
	}
	p.ExpectedMonthlySaving = moneyFromMicros(micros, currency)
	p.BaselineMonthlyCost = moneyFromMicros(baselineMicros, currency)
	if err := fromJSON(resourceIDs, &p.ResourceIDs); err != nil {
		return execute.Plan{}, err
	}
	if err := fromJSON(validation, &p.Validation); err != nil {
		return execute.Plan{}, err
	}
	return p, nil
}
