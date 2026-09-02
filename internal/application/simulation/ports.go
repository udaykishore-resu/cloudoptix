package simulation

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// InventoryLoader is the minimal read access the mutation and counterfactual
// engines need. It is satisfied by ports.ResourceRepository without any
// adapter — Go interface satisfaction is structural — but is declared
// narrowly here on purpose: a test fakes exactly these two methods instead of
// the whole repository surface ResourceRepository carries for discovery,
// tombstoning and pagination that this package never touches.
type InventoryLoader interface {
	LoadInventory(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (*cloud.Inventory, error)
	LoadTopology(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (*cloud.Topology, error)
}

// SimulationStore is the persistence surface this package needs, satisfied
// by ports.SimulationRepository. Narrowed the same way as InventoryLoader and
// for the same reason.
type SimulationStore interface {
	SaveSimulation(ctx context.Context, s simulate.Simulation) error
	GetSimulation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Simulation, error)
	ListSimulations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[simulate.Simulation], error)

	SaveCounterfactual(ctx context.Context, c simulate.Counterfactual) error

	SaveCompilation(ctx context.Context, c simulate.CompilationResult) error
	GetCompilation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.CompilationResult, error)

	SaveRegressionSuite(ctx context.Context, s simulate.RegressionSuite) error
	GetRegressionSuite(ctx context.Context, tenant core.TenantID, name string) (simulate.RegressionSuite, error)
	ListRegressionSuites(ctx context.Context, tenant core.TenantID) ([]simulate.RegressionSuite, error)
	SaveRegressionReport(ctx context.Context, r simulate.RegressionReport) error
}
