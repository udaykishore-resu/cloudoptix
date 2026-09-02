package simulation

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/application/compiler"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Service implements ports.SimulationService.
type Service struct {
	Pricing   ports.PricingCatalog
	Resources InventoryLoader
	Store     SimulationStore
	Compiler  *compiler.Compiler
	Clock     core.Clock
	Logger    *slog.Logger
}

var _ ports.SimulationService = (*Service)(nil)

// New wires a Service over a pricing catalog, an inventory source and a
// simulation store, using the system clock and a discarding logger.
func New(pricing ports.PricingCatalog, resources InventoryLoader, store SimulationStore) *Service {
	return &Service{
		Pricing:   pricing,
		Resources: resources,
		Store:     store,
		Compiler:  compiler.New(pricing),
		Clock:     core.SystemClock{},
		Logger:    slog.Default(),
	}
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock.Now()
	}
	return time.Now().UTC()
}

// Compile prices an infrastructure change set via the Cost Compiler and
// persists the result.
func (s *Service) Compile(ctx context.Context, tenant core.TenantID, in ports.CompileRequest) (simulate.CompilationResult, error) {
	result, err := s.Compiler.Compile(tenant, in)
	if err != nil {
		return simulate.CompilationResult{}, err
	}
	if err := s.Store.SaveCompilation(ctx, result); err != nil {
		return simulate.CompilationResult{}, err
	}
	return result, nil
}

// GetCompilation returns a previously compiled result.
func (s *Service) GetCompilation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.CompilationResult, error) {
	return s.Store.GetCompilation(ctx, tenant, id)
}

// RunRegression evaluates a named cost-regression suite against a prior
// compilation and persists the report.
func (s *Service) RunRegression(ctx context.Context, tenant core.TenantID, compilationID core.ID, suiteName string) (simulate.RegressionReport, error) {
	result, err := s.Store.GetCompilation(ctx, tenant, compilationID)
	if err != nil {
		return simulate.RegressionReport{}, err
	}
	suite, err := s.Store.GetRegressionSuite(ctx, tenant, suiteName)
	if err != nil {
		return simulate.RegressionReport{}, err
	}
	report := compiler.EvaluateRegression(tenant, compilationID, suite, result)
	if err := s.Store.SaveRegressionReport(ctx, report); err != nil {
		return simulate.RegressionReport{}, err
	}
	return report, nil
}

// UpsertRegressionSuite stores a suite version.
func (s *Service) UpsertRegressionSuite(ctx context.Context, suite simulate.RegressionSuite) (simulate.RegressionSuite, error) {
	if suite.ID.IsZero() {
		suite.ID = core.NewID("rsuite")
	}
	if suite.CreatedAt.IsZero() {
		suite.CreatedAt = s.now()
	}
	suite.Version++
	if err := s.Store.SaveRegressionSuite(ctx, suite); err != nil {
		return simulate.RegressionSuite{}, err
	}
	return suite, nil
}

// ListRegressionSuites lists a tenant's cost test suites.
func (s *Service) ListRegressionSuites(ctx context.Context, tenant core.TenantID) ([]simulate.RegressionSuite, error) {
	return s.Store.ListRegressionSuites(ctx, tenant)
}

// GetSimulation returns a stored mutation-engine run.
func (s *Service) GetSimulation(ctx context.Context, tenant core.TenantID, id core.ID) (simulate.Simulation, error) {
	return s.Store.GetSimulation(ctx, tenant, id)
}

// ListSimulations pages through a tenant's simulation history.
func (s *Service) ListSimulations(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[simulate.Simulation], error) {
	return s.Store.ListSimulations(ctx, tenant, opts)
}
