package simulation

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeInventoryLoader satisfies InventoryLoader entirely in memory, applying
// just enough of ResourceFilter (application/workload/account scoping) for
// the tests in this package to exercise scopeFilter and counterfactualFilter
// meaningfully, without pulling in a database.
type fakeInventoryLoader struct {
	resources []cloud.Resource
	edges     []cloud.Relationship
}

func (f *fakeInventoryLoader) LoadInventory(_ context.Context, _ core.TenantID, filter ports.ResourceFilter) (*cloud.Inventory, error) {
	var out []cloud.Resource
	for _, r := range f.resources {
		if filter.ApplicationID != "" && r.ApplicationID != filter.ApplicationID {
			continue
		}
		if filter.WorkloadID != "" && r.WorkloadID != filter.WorkloadID {
			continue
		}
		if len(filter.AccountIDs) > 0 {
			match := false
			for _, a := range filter.AccountIDs {
				if r.AccountID == a {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, r)
	}
	return cloud.NewInventory(out), nil
}

func (f *fakeInventoryLoader) LoadTopology(_ context.Context, _ core.TenantID, _ ports.ResourceFilter) (*cloud.Topology, error) {
	return cloud.NewTopology(f.edges), nil
}

// fakeSimulationStore satisfies SimulationStore entirely in memory.
type fakeSimulationStore struct {
	simulations     map[core.ID]simulate.Simulation
	counterfactuals map[core.ID]simulate.Counterfactual
	compilations    map[core.ID]simulate.CompilationResult
	suites          map[string]simulate.RegressionSuite
	reports         []simulate.RegressionReport
}

func newFakeSimulationStore() *fakeSimulationStore {
	return &fakeSimulationStore{
		simulations:     map[core.ID]simulate.Simulation{},
		counterfactuals: map[core.ID]simulate.Counterfactual{},
		compilations:    map[core.ID]simulate.CompilationResult{},
		suites:          map[string]simulate.RegressionSuite{},
	}
}

func (s *fakeSimulationStore) SaveSimulation(_ context.Context, sim simulate.Simulation) error {
	s.simulations[sim.ID] = sim
	return nil
}

func (s *fakeSimulationStore) GetSimulation(_ context.Context, _ core.TenantID, id core.ID) (simulate.Simulation, error) {
	sim, ok := s.simulations[id]
	if !ok {
		return simulate.Simulation{}, fmt.Errorf("simulation %s not found", id)
	}
	return sim, nil
}

func (s *fakeSimulationStore) ListSimulations(_ context.Context, _ core.TenantID, _ ports.ListOptions) (ports.Page[simulate.Simulation], error) {
	var items []simulate.Simulation
	for _, sim := range s.simulations {
		items = append(items, sim)
	}
	return ports.Page[simulate.Simulation]{Items: items, Total: len(items)}, nil
}

func (s *fakeSimulationStore) SaveCounterfactual(_ context.Context, c simulate.Counterfactual) error {
	s.counterfactuals[c.ID] = c
	return nil
}

func (s *fakeSimulationStore) SaveCompilation(_ context.Context, c simulate.CompilationResult) error {
	s.compilations[c.ID] = c
	return nil
}

func (s *fakeSimulationStore) GetCompilation(_ context.Context, _ core.TenantID, id core.ID) (simulate.CompilationResult, error) {
	c, ok := s.compilations[id]
	if !ok {
		return simulate.CompilationResult{}, fmt.Errorf("compilation %s not found", id)
	}
	return c, nil
}

func (s *fakeSimulationStore) SaveRegressionSuite(_ context.Context, suite simulate.RegressionSuite) error {
	s.suites[suite.Name] = suite
	return nil
}

func (s *fakeSimulationStore) GetRegressionSuite(_ context.Context, _ core.TenantID, name string) (simulate.RegressionSuite, error) {
	suite, ok := s.suites[name]
	if !ok {
		return simulate.RegressionSuite{}, fmt.Errorf("regression suite %s not found", name)
	}
	return suite, nil
}

func (s *fakeSimulationStore) ListRegressionSuites(_ context.Context, _ core.TenantID) ([]simulate.RegressionSuite, error) {
	var out []simulate.RegressionSuite
	for _, suite := range s.suites {
		out = append(out, suite)
	}
	return out, nil
}

func (s *fakeSimulationStore) SaveRegressionReport(_ context.Context, r simulate.RegressionReport) error {
	s.reports = append(s.reports, r)
	return nil
}
