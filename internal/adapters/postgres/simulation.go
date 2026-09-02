package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// SimulationRepository is the pgx-backed ports.SimulationRepository.
type SimulationRepository struct{ db *DB }

// NewSimulationRepository builds a SimulationRepository over db.
func NewSimulationRepository(db *DB) *SimulationRepository { return &SimulationRepository{db: db} }

var _ ports.SimulationRepository = (*SimulationRepository)(nil)

// SaveSimulation upserts the simulation row and replaces its candidate set
// wholesale, in one transaction — matching migrations/0011_simulate.up.sql's
// comment: the mutation engine writes candidates once and never updates
// them in place, so a delete-then-insert of the whole set is exactly as
// cheap as a diff would be and far simpler.
func (r *SimulationRepository) SaveSimulation(ctx context.Context, sim simulate.Simulation) error {
	if err := core.GuardTenant(ctx, sim.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, sim.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		baseline, currency := moneyMicros(sim.BaselineCost)
		if _, err := q.Exec(ctx, `
			INSERT INTO architecture_simulations (id, tenant_id, name, scope, scope_id, kind,
				baseline_cost_micros, currency, weights, assumptions, requested_by, status, error,
				created_at, completed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (id) DO UPDATE SET
				weights = EXCLUDED.weights, status = EXCLUDED.status, error = EXCLUDED.error,
				completed_at = EXCLUDED.completed_at
		`, string(sim.ID), string(sim.TenantID), sim.Name, sim.Scope, string(sim.ScopeID), string(sim.Kind),
			baseline, currency, toJSON(sim.Weights), toJSON(sim.Assumptions), sim.RequestedBy, sim.Status,
			sim.Error, orNow(sim.CreatedAt), zeroToNil(sim.CompletedAt)); err != nil {
			return mapErr(err)
		}
		if _, err := q.Exec(ctx, `DELETE FROM simulation_candidates WHERE tenant_id = $1 AND simulation_id = $2`,
			string(sim.TenantID), string(sim.ID)); err != nil {
			return mapErr(err)
		}
		for _, c := range sim.Candidates {
			if err := insertCandidate(ctx, q, sim.TenantID, sim.ID, c); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertCandidate(ctx context.Context, q Querier, tenant core.TenantID, simID core.ID, c simulate.Candidate) error {
	id := c.ID
	if id.IsZero() {
		id = core.NewID("cnd")
	}
	current, currency := moneyMicros(c.CurrentMonthlyCost)
	projected, _ := moneyMicros(c.ProjectedMonthlyCost)
	delta, _ := moneyMicros(c.MonthlyDelta)
	_, err := q.Exec(ctx, `
		INSERT INTO simulation_candidates (id, tenant_id, simulation_id, name, summary, pattern, changes,
			current_monthly_cost_micros, projected_monthly_cost_micros, monthly_delta_micros, currency,
			savings_pct, scores, composite_score, assumptions, risks, blockers, migration_steps,
			confidence, recommended)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, string(id), string(tenant), string(simID), c.Name, c.Summary, c.Pattern, toJSON(c.Changes),
		current, projected, delta, currency, c.SavingsPct, toJSON(c.Scores), c.Composite,
		toJSON(c.Assumptions), toJSON(c.Risks), toJSON(c.Blockers), toJSON(c.MigrationSteps),
		float64(c.Confidence), c.Recommended)
	return mapErr(err)
}

func (r *SimulationRepository) GetSimulation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Simulation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.Simulation{}, err
	}
	var out simulate.Simulation
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		row := q.QueryRow(ctx, simulationSelectSQL+` WHERE tenant_id = $1 AND id = $2`, string(tenant), string(id))
		sim, err := scanSimulation(row)
		if err != nil {
			return mapErr(err)
		}
		cands, err := loadCandidates(ctx, q, id)
		if err != nil {
			return err
		}
		sim.Candidates = cands
		out = sim
		return nil
	})
	return out, err
}

func loadCandidates(ctx context.Context, q Querier, simID core.ID) ([]simulate.Candidate, error) {
	rows, err := q.Query(ctx, `
		SELECT id, tenant_id, simulation_id, name, summary, pattern, changes,
			current_monthly_cost_micros, projected_monthly_cost_micros, monthly_delta_micros, currency,
			savings_pct, scores, composite_score, assumptions, risks, blockers, migration_steps,
			confidence, recommended
		FROM simulation_candidates WHERE simulation_id = $1 ORDER BY composite_score DESC
	`, string(simID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []simulate.Candidate
	for rows.Next() {
		var c simulate.Candidate
		var changes, scores, assumptions, risks, blockers, migrationSteps []byte
		var current, projected, delta int64
		var currency string
		if err := rows.Scan(&c.ID, &c.TenantID, &c.SimulationID, &c.Name, &c.Summary, &c.Pattern, &changes,
			&current, &projected, &delta, &currency, &c.SavingsPct, &scores, &c.Composite, &assumptions,
			&risks, &blockers, &migrationSteps, &c.Confidence, &c.Recommended); err != nil {
			return nil, mapErr(err)
		}
		c.CurrentMonthlyCost = moneyFromMicros(current, currency)
		c.ProjectedMonthlyCost = moneyFromMicros(projected, currency)
		c.MonthlyDelta = moneyFromMicros(delta, currency)
		if err := fromJSON(changes, &c.Changes); err != nil {
			return nil, err
		}
		if err := fromJSON(scores, &c.Scores); err != nil {
			return nil, err
		}
		if err := fromJSON(assumptions, &c.Assumptions); err != nil {
			return nil, err
		}
		if err := fromJSON(risks, &c.Risks); err != nil {
			return nil, err
		}
		if err := fromJSON(blockers, &c.Blockers); err != nil {
			return nil, err
		}
		if err := fromJSON(migrationSteps, &c.MigrationSteps); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

func (r *SimulationRepository) ListSimulations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[simulate.Simulation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[simulate.Simulation]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[simulate.Simulation]{}, err
	}
	var page ports.Page[simulate.Simulation]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where := `tenant_id = $1`
		args := []any{string(tenant)}
		if after != nil {
			args = append(args, after[0])
			where += ` AND id > $2`
		}
		args = append(args, opts.Limit+1)
		sql := simulationSelectSQL + ` WHERE ` + where + ` ORDER BY id LIMIT $` + strconv.Itoa(len(args))
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []simulate.Simulation
		for rows.Next() {
			sim, err := scanSimulation(rows)
			if err != nil {
				return mapErr(err)
			}
			// Candidates are intentionally omitted from the list view: a
			// simulation can carry dozens of candidates each with their own
			// change/risk/step arrays, and the list screen only needs the
			// summary row — GetSimulation is the detail view that pays for
			// the join.
			items = append(items, sim)
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

func (r *SimulationRepository) SaveCounterfactual(ctx context.Context, c simulate.Counterfactual) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, c.TenantID, func(ctx context.Context) error {
		delta, currency := moneyMicros(c.CostDelta)
		annualDelta, _ := moneyMicros(c.AnnualCostDelta)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO counterfactuals (id, tenant_id, scenario, question, current_state, proposed_state,
				cost_delta_micros, currency, cost_delta_pct, annual_cost_delta_micros, performance_delta,
				reliability_delta, security_delta, risk, confidence, assumptions, caveats, narrative,
				computed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
			ON CONFLICT (id) DO UPDATE SET narrative = EXCLUDED.narrative
		`, string(c.ID), string(c.TenantID), toJSON(c.Scenario), c.Question, toJSON(c.CurrentState),
			toJSON(c.ProposedState), delta, currency, c.CostDeltaPct, annualDelta, c.PerformanceDelta,
			c.ReliabilityDelta, c.SecurityDelta, string(c.Risk), float64(c.Confidence),
			toJSON(c.Assumptions), toJSON(c.Caveats), c.Narrative, orNow(c.ComputedAt))
		return mapErr(err)
	})
}

func (r *SimulationRepository) GetCounterfactual(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Counterfactual, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.Counterfactual{}, err
	}
	var out simulate.Counterfactual
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT id, tenant_id, scenario, question, current_state, proposed_state, cost_delta_micros,
				currency, cost_delta_pct, annual_cost_delta_micros, performance_delta, reliability_delta,
				security_delta, risk, confidence, assumptions, caveats, narrative, computed_at
			FROM counterfactuals WHERE tenant_id = $1 AND id = $2
		`, string(tenant), string(id))
		var c simulate.Counterfactual
		var scenario, currentState, proposedState, assumptions, caveats []byte
		var delta, annualDelta int64
		var currency string
		if err := row.Scan(&c.ID, &c.TenantID, &scenario, &c.Question, &currentState, &proposedState,
			&delta, &currency, &c.CostDeltaPct, &annualDelta, &c.PerformanceDelta, &c.ReliabilityDelta,
			&c.SecurityDelta, &c.Risk, &c.Confidence, &assumptions, &caveats, &c.Narrative,
			&c.ComputedAt); err != nil {
			return mapErr(err)
		}
		c.CostDelta = moneyFromMicros(delta, currency)
		c.AnnualCostDelta = moneyFromMicros(annualDelta, currency)
		if err := fromJSON(scenario, &c.Scenario); err != nil {
			return err
		}
		if err := fromJSON(currentState, &c.CurrentState); err != nil {
			return err
		}
		if err := fromJSON(proposedState, &c.ProposedState); err != nil {
			return err
		}
		if err := fromJSON(assumptions, &c.Assumptions); err != nil {
			return err
		}
		if err := fromJSON(caveats, &c.Caveats); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

func (r *SimulationRepository) SaveCompilation(ctx context.Context, c simulate.CompilationResult) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, c.TenantID, func(ctx context.Context) error {
		baseline, currency := moneyMicros(c.BaselineMonthly)
		projected, _ := moneyMicros(c.ProjectedMonthly)
		delta, _ := moneyMicros(c.MonthlyDelta)
		// c.AnnualDelta has no column here (unlike regression_reports, which
		// does store one): it is exactly MonthlyDelta * 12 (see
		// simulate.CompilationResult), so scanCompilation recomputes it from
		// monthly_delta_micros on read instead of trusting a second stored
		// figure that could silently drift if compiler logic ever changes
		// what "annual" means.
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO compilations (id, tenant_id, source, label, changes, baseline_monthly_micros,
				projected_monthly_micros, monthly_delta_micros, currency, delta_pct, created_count,
				updated_count, deleted_count, unpriced_count, coverage, confidence, assumptions, risks,
				opportunities, pricing_date, compiled_at, duration_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
			ON CONFLICT (id) DO NOTHING
		`, string(c.ID), string(c.TenantID), string(c.Source), c.Label, toJSON(c.Changes), baseline,
			projected, delta, currency, c.DeltaPct, c.CreatedCount, c.UpdatedCount, c.DeletedCount,
			c.UnpricedCount, c.Coverage, float64(c.Confidence), toJSON(c.Assumptions), toJSON(c.Risks),
			toJSON(c.Opportunities), zeroToNil(c.PricingDate), orNow(c.CompiledAt), c.DurationMS)
		return mapErr(err)
	})
}

func (r *SimulationRepository) GetCompilation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.CompilationResult, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.CompilationResult{}, err
	}
	var out simulate.CompilationResult
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, compilationSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		c, err := scanCompilation(row)
		if err != nil {
			return mapErr(err)
		}
		out = c
		return nil
	})
	return out, err
}

func (r *SimulationRepository) ListCompilations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[simulate.CompilationResult], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[simulate.CompilationResult]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[simulate.CompilationResult]{}, err
	}
	var page ports.Page[simulate.CompilationResult]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		where := `tenant_id = $1`
		args := []any{string(tenant)}
		if after != nil {
			args = append(args, after[0])
			where += ` AND id > $2`
		}
		args = append(args, opts.Limit+1)
		sql := compilationSelectSQL + ` WHERE ` + where + ` ORDER BY id LIMIT $` + strconv.Itoa(len(args))
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []simulate.CompilationResult
		for rows.Next() {
			c, err := scanCompilation(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, c)
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

func (r *SimulationRepository) SaveRegressionSuite(ctx context.Context, suite simulate.RegressionSuite) error {
	if err := core.GuardTenant(ctx, suite.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, suite.TenantID, func(ctx context.Context) error {
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO regression_suites (id, tenant_id, name, version, checks, enabled, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id, name) DO UPDATE SET
				version = EXCLUDED.version, checks = EXCLUDED.checks, enabled = EXCLUDED.enabled
		`, string(suite.ID), string(suite.TenantID), suite.Name, suite.Version, toJSON(suite.Checks),
			suite.Enabled, orNow(suite.CreatedAt))
		return mapErr(err)
	})
}

func (r *SimulationRepository) GetRegressionSuite(ctx context.Context, tenant core.TenantID, name string) (simulate.RegressionSuite, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.RegressionSuite{}, err
	}
	var out simulate.RegressionSuite
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx,
			`SELECT id, tenant_id, name, version, checks, enabled, created_at FROM regression_suites WHERE tenant_id = $1 AND name = $2`,
			string(tenant), name)
		s, err := scanRegressionSuite(row)
		if err != nil {
			return mapErr(err)
		}
		out = s
		return nil
	})
	return out, err
}

func (r *SimulationRepository) ListRegressionSuites(ctx context.Context, tenant core.TenantID) ([]simulate.RegressionSuite, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	var out []simulate.RegressionSuite
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx,
			`SELECT id, tenant_id, name, version, checks, enabled, created_at FROM regression_suites WHERE tenant_id = $1 ORDER BY name`,
			string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			s, err := scanRegressionSuite(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, s)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func scanRegressionSuite(row rowScanner) (simulate.RegressionSuite, error) {
	var s simulate.RegressionSuite
	var checks []byte
	if err := row.Scan(&s.ID, &s.TenantID, &s.Name, &s.Version, &checks, &s.Enabled, &s.CreatedAt); err != nil {
		return simulate.RegressionSuite{}, err
	}
	if err := fromJSON(checks, &s.Checks); err != nil {
		return simulate.RegressionSuite{}, err
	}
	return s, nil
}

func (r *SimulationRepository) SaveRegressionReport(ctx context.Context, rep simulate.RegressionReport) error {
	if err := core.GuardTenant(ctx, rep.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, rep.TenantID, func(ctx context.Context) error {
		id := rep.ID
		if id.IsZero() {
			id = core.NewID("rgr")
		}
		monthlyDelta, currency := moneyMicros(rep.MonthlyDelta)
		annualDelta, _ := moneyMicros(rep.AnnualDelta)
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO regression_reports (id, tenant_id, compilation_id, suite_name, verdict, results,
				monthly_delta_micros, annual_delta_micros, currency, summary, required_action, evaluated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		`, string(id), string(rep.TenantID), string(rep.CompilationID), rep.SuiteName, string(rep.Verdict),
			toJSON(rep.Results), monthlyDelta, annualDelta, currency, rep.Summary, rep.RequiredAction,
			orNow(rep.EvaluatedAt))
		return mapErr(err)
	})
}

func (r *SimulationRepository) GetRegressionReport(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.RegressionReport, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.RegressionReport{}, err
	}
	var out simulate.RegressionReport
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx, `
			SELECT id, tenant_id, compilation_id, suite_name, verdict, results, monthly_delta_micros,
				annual_delta_micros, currency, summary, required_action, evaluated_at
			FROM regression_reports WHERE tenant_id = $1 AND id = $2
		`, string(tenant), string(id))
		var rep simulate.RegressionReport
		var results []byte
		var monthlyDelta, annualDelta int64
		var currency string
		if err := row.Scan(&rep.ID, &rep.TenantID, &rep.CompilationID, &rep.SuiteName, &rep.Verdict,
			&results, &monthlyDelta, &annualDelta, &currency, &rep.Summary, &rep.RequiredAction,
			&rep.EvaluatedAt); err != nil {
			return mapErr(err)
		}
		rep.MonthlyDelta = moneyFromMicros(monthlyDelta, currency)
		rep.AnnualDelta = moneyFromMicros(annualDelta, currency)
		if err := fromJSON(results, &rep.Results); err != nil {
			return err
		}
		out = rep
		return nil
	})
	return out, err
}

const simulationSelectSQL = `
	SELECT id, tenant_id, name, scope, scope_id, kind, baseline_cost_micros, currency, weights,
		assumptions, requested_by, status, error, created_at, completed_at
	FROM architecture_simulations`

func scanSimulation(row rowScanner) (simulate.Simulation, error) {
	var sim simulate.Simulation
	var weights, assumptions []byte
	var baseline int64
	var currency string
	var completedAt *time.Time
	if err := row.Scan(&sim.ID, &sim.TenantID, &sim.Name, &sim.Scope, &sim.ScopeID, &sim.Kind, &baseline,
		&currency, &weights, &assumptions, &sim.RequestedBy, &sim.Status, &sim.Error, &sim.CreatedAt,
		&completedAt); err != nil {
		return simulate.Simulation{}, err
	}
	sim.BaselineCost = moneyFromMicros(baseline, currency)
	sim.CompletedAt = nilToZero(completedAt)
	if err := fromJSON(weights, &sim.Weights); err != nil {
		return simulate.Simulation{}, err
	}
	if err := fromJSON(assumptions, &sim.Assumptions); err != nil {
		return simulate.Simulation{}, err
	}
	return sim, nil
}

const compilationSelectSQL = `
	SELECT id, tenant_id, source, label, changes, baseline_monthly_micros, projected_monthly_micros,
		monthly_delta_micros, currency, delta_pct, created_count, updated_count, deleted_count,
		unpriced_count, coverage, confidence, assumptions, risks, opportunities, pricing_date,
		compiled_at, duration_ms
	FROM compilations`

func scanCompilation(row rowScanner) (simulate.CompilationResult, error) {
	var c simulate.CompilationResult
	var changes, assumptions, risks, opportunities []byte
	var baseline, projected, delta int64
	var currency string
	var pricingDate *time.Time
	if err := row.Scan(&c.ID, &c.TenantID, &c.Source, &c.Label, &changes, &baseline, &projected, &delta,
		&currency, &c.DeltaPct, &c.CreatedCount, &c.UpdatedCount, &c.DeletedCount, &c.UnpricedCount,
		&c.Coverage, &c.Confidence, &assumptions, &risks, &opportunities, &pricingDate, &c.CompiledAt,
		&c.DurationMS); err != nil {
		return simulate.CompilationResult{}, err
	}
	c.BaselineMonthly = moneyFromMicros(baseline, currency)
	c.ProjectedMonthly = moneyFromMicros(projected, currency)
	c.MonthlyDelta = moneyFromMicros(delta, currency)
	c.AnnualDelta = moneyFromMicros(delta*12, currency)
	c.PricingDate = nilToZero(pricingDate)
	if err := fromJSON(changes, &c.Changes); err != nil {
		return simulate.CompilationResult{}, err
	}
	if err := fromJSON(assumptions, &c.Assumptions); err != nil {
		return simulate.CompilationResult{}, err
	}
	if err := fromJSON(risks, &c.Risks); err != nil {
		return simulate.CompilationResult{}, err
	}
	if err := fromJSON(opportunities, &c.Opportunities); err != nil {
		return simulate.CompilationResult{}, err
	}
	return c, nil
}
