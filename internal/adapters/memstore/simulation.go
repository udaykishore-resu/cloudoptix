package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// simulationRepo implements ports.SimulationRepository.
type simulationRepo struct{ s *Store }

func (r *simulationRepo) SaveSimulation(ctx context.Context, sim simulate.Simulation) error {
	if err := core.GuardTenant(ctx, sim.TenantID); err != nil {
		return err
	}
	r.s.simMu.Lock()
	defer r.s.simMu.Unlock()
	if r.s.data.Simulations[sim.TenantID] == nil {
		r.s.data.Simulations[sim.TenantID] = map[core.ID]simulate.Simulation{}
	}
	r.s.data.Simulations[sim.TenantID][sim.ID] = deepCopy(sim)
	return nil
}

func (r *simulationRepo) GetSimulation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Simulation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.Simulation{}, err
	}
	r.s.simMu.RLock()
	defer r.s.simMu.RUnlock()
	sim, ok := r.s.data.Simulations[tenant][id]
	if !ok {
		return simulate.Simulation{}, core.NotFound("simulation", id)
	}
	return deepCopy(sim), nil
}

func (r *simulationRepo) ListSimulations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[simulate.Simulation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[simulate.Simulation]{}, err
	}
	r.s.simMu.RLock()
	items := make([]simulate.Simulation, 0, len(r.s.data.Simulations[tenant]))
	for _, sim := range r.s.data.Simulations[tenant] {
		items = append(items, deepCopy(sim))
	}
	r.s.simMu.RUnlock()

	keyOf := func(sim simulate.Simulation) (string, string) {
		return sim.CreatedAt.Format(sortTimeLayout), sim.ID.String()
	}
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *simulationRepo) SaveCounterfactual(ctx context.Context, c simulate.Counterfactual) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	r.s.simMu.Lock()
	defer r.s.simMu.Unlock()
	if r.s.data.Counterfactuals[c.TenantID] == nil {
		r.s.data.Counterfactuals[c.TenantID] = map[core.ID]simulate.Counterfactual{}
	}
	r.s.data.Counterfactuals[c.TenantID][c.ID] = deepCopy(c)
	return nil
}

func (r *simulationRepo) GetCounterfactual(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Counterfactual, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.Counterfactual{}, err
	}
	r.s.simMu.RLock()
	defer r.s.simMu.RUnlock()
	c, ok := r.s.data.Counterfactuals[tenant][id]
	if !ok {
		return simulate.Counterfactual{}, core.NotFound("counterfactual", id)
	}
	return deepCopy(c), nil
}

func (r *simulationRepo) SaveCompilation(ctx context.Context, c simulate.CompilationResult) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	r.s.simMu.Lock()
	defer r.s.simMu.Unlock()
	if r.s.data.Compilations[c.TenantID] == nil {
		r.s.data.Compilations[c.TenantID] = map[core.ID]simulate.CompilationResult{}
	}
	r.s.data.Compilations[c.TenantID][c.ID] = deepCopy(c)
	return nil
}

func (r *simulationRepo) GetCompilation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.CompilationResult, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.CompilationResult{}, err
	}
	r.s.simMu.RLock()
	defer r.s.simMu.RUnlock()
	c, ok := r.s.data.Compilations[tenant][id]
	if !ok {
		return simulate.CompilationResult{}, core.NotFound("compilation", id)
	}
	return deepCopy(c), nil
}

func (r *simulationRepo) ListCompilations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[simulate.CompilationResult], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[simulate.CompilationResult]{}, err
	}
	r.s.simMu.RLock()
	items := make([]simulate.CompilationResult, 0, len(r.s.data.Compilations[tenant]))
	for _, c := range r.s.data.Compilations[tenant] {
		items = append(items, deepCopy(c))
	}
	r.s.simMu.RUnlock()

	keyOf := func(c simulate.CompilationResult) (string, string) {
		return c.CompiledAt.Format(sortTimeLayout), c.ID.String()
	}
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *simulationRepo) SaveRegressionSuite(ctx context.Context, suite simulate.RegressionSuite) error {
	if err := core.GuardTenant(ctx, suite.TenantID); err != nil {
		return err
	}
	r.s.simMu.Lock()
	defer r.s.simMu.Unlock()
	if r.s.data.RegressionSuites[suite.TenantID] == nil {
		r.s.data.RegressionSuites[suite.TenantID] = map[string]simulate.RegressionSuite{}
	}
	r.s.data.RegressionSuites[suite.TenantID][suite.Name] = deepCopy(suite)
	return nil
}

func (r *simulationRepo) GetRegressionSuite(ctx context.Context, tenant core.TenantID, name string) (simulate.RegressionSuite, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.RegressionSuite{}, err
	}
	r.s.simMu.RLock()
	defer r.s.simMu.RUnlock()
	suite, ok := r.s.data.RegressionSuites[tenant][name]
	if !ok {
		return simulate.RegressionSuite{}, core.NotFound("regression_suite", name)
	}
	return deepCopy(suite), nil
}

func (r *simulationRepo) ListRegressionSuites(ctx context.Context, tenant core.TenantID) ([]simulate.RegressionSuite, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.simMu.RLock()
	defer r.s.simMu.RUnlock()
	out := make([]simulate.RegressionSuite, 0, len(r.s.data.RegressionSuites[tenant]))
	for _, suite := range r.s.data.RegressionSuites[tenant] {
		out = append(out, deepCopy(suite))
	}
	sortByCreatedThenID(out, func(s simulate.RegressionSuite) (string, string) { return s.CreatedAt.Format(sortTimeLayout), s.Name })
	return out, nil
}

func (r *simulationRepo) SaveRegressionReport(ctx context.Context, rep simulate.RegressionReport) error {
	if err := core.GuardTenant(ctx, rep.TenantID); err != nil {
		return err
	}
	r.s.simMu.Lock()
	defer r.s.simMu.Unlock()
	if r.s.data.RegressionReports[rep.TenantID] == nil {
		r.s.data.RegressionReports[rep.TenantID] = map[core.ID]simulate.RegressionReport{}
	}
	r.s.data.RegressionReports[rep.TenantID][rep.ID] = deepCopy(rep)
	return nil
}

func (r *simulationRepo) GetRegressionReport(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.RegressionReport, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return simulate.RegressionReport{}, err
	}
	r.s.simMu.RLock()
	defer r.s.simMu.RUnlock()
	rep, ok := r.s.data.RegressionReports[tenant][id]
	if !ok {
		return simulate.RegressionReport{}, core.NotFound("regression_report", id)
	}
	return deepCopy(rep), nil
}
