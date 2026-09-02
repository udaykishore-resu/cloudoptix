package economics

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Service implements ports.EconomicsService.
type Service struct {
	Repos ports.Repositories
	Clock core.Clock

	// FootprintLookbackDays is the window length used when a caller asks for
	// a footprint, unit-economics figure or efficiency score without naming
	// one explicitly (Compute always uses its caller-supplied period).
	// Defaults to 30, matching the trailing-month convention the costing and
	// twin packages already use.
	FootprintLookbackDays int
}

var _ ports.EconomicsService = (*Service)(nil)

// NewService builds a Service with the platform default window.
func NewService(repos ports.Repositories) *Service {
	return &Service{Repos: repos, Clock: core.SystemClock{}, FootprintLookbackDays: 30}
}

func (s *Service) clock() core.Clock {
	if s.Clock == nil {
		return core.SystemClock{}
	}
	return s.Clock
}

func (s *Service) defaultPeriod() core.Period {
	days := s.FootprintLookbackDays
	if days <= 0 {
		days = 30
	}
	return core.PeriodOfDays(s.clock().Now(), days)
}

// resourcesForScope resolves the resource set a scope directly owns, plus a
// human label for it. Every scope in econ.Scope maps onto a resource set one
// of two ways: most map onto an existing ports.ResourceFilter dimension
// (account, environment, application, workload, a single resource); the two
// that do not — business capability and transaction — are defined in terms of
// the others (a capability is the union of the applications sharing its
// Application.Domain label, a transaction is the union of the workloads on
// its critical path) rather than needing their own repository query.
func (s *Service) resourcesForScope(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID) ([]cloud.Resource, string, error) {
	switch scope {
	case econ.ScopeOrganization:
		inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{})
		if err != nil {
			return nil, "Organization", err
		}
		return inv.All(), "Organization", nil

	case econ.ScopeAccount:
		accountID := core.AccountID(scopeID)
		label := string(accountID)
		if a, err := s.Repos.AWSAccounts.GetByAccountID(ctx, tenant, accountID); err == nil && a.Alias != "" {
			label = a.Alias
		}
		inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{AccountIDs: []core.AccountID{accountID}})
		if err != nil {
			return nil, label, err
		}
		return inv.All(), label, nil

	case econ.ScopeEnvironment:
		env := core.Environment(scopeID)
		inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{Environments: []core.Environment{env}})
		if err != nil {
			return nil, string(env), err
		}
		return inv.All(), string(env), nil

	case econ.ScopeApplication:
		label := string(scopeID)
		if app, err := s.Repos.Applications.GetApplication(ctx, tenant, scopeID); err == nil {
			label = app.Name
		}
		inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{ApplicationID: scopeID})
		if err != nil {
			return nil, label, err
		}
		return inv.All(), label, nil

	case econ.ScopeWorkload:
		label := string(scopeID)
		if w, err := s.Repos.Applications.GetWorkload(ctx, tenant, scopeID); err == nil {
			label = w.Name
		}
		inv, err := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{WorkloadID: scopeID})
		if err != nil {
			return nil, label, err
		}
		return inv.All(), label, nil

	case econ.ScopeResource, econ.ScopeAPI:
		// The domain model has no separate "API" aggregate — an API surface
		// is itself a discovered resource (an API Gateway or an ALB), so an
		// API-scoped footprint is a single-resource footprint exactly like a
		// resource-scoped one. This is a deliberate simplification, not an
		// oversight: modelling API as a distinct entity would need its own
		// repository and onboarding flow that nothing in ports requires yet.
		res, err := s.Repos.Resources.Get(ctx, tenant, scopeID)
		if err != nil {
			return nil, string(scopeID), err
		}
		return []cloud.Resource{res}, res.DisplayName(), nil

	case econ.ScopeBusinessCapability:
		// Application.Domain ("ecommerce", "claims", "payments") is the only
		// place in the model a business capability is named, so a capability
		// footprint is the union of every application sharing that label,
		// interpreting scopeID as the domain string rather than an ID.
		apps, err := s.Repos.Applications.ListApplications(ctx, tenant)
		if err != nil {
			return nil, string(scopeID), err
		}
		var out []cloud.Resource
		for _, app := range apps {
			if app.Domain != string(scopeID) {
				continue
			}
			inv, ierr := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{ApplicationID: app.ID})
			if ierr != nil {
				continue
			}
			out = append(out, inv.All()...)
		}
		return out, string(scopeID), nil

	case econ.ScopeTransaction:
		tx, err := s.Repos.Economics.GetTransaction(ctx, tenant, scopeID)
		if err != nil {
			return nil, string(scopeID), err
		}
		var out []cloud.Resource
		for _, wid := range tx.WorkloadIDs {
			inv, ierr := s.Repos.Resources.LoadInventory(ctx, tenant, ports.ResourceFilter{WorkloadID: wid})
			if ierr != nil {
				continue
			}
			out = append(out, inv.All()...)
		}
		return out, tx.Name, nil

	default:
		return nil, string(scopeID), core.Invalid("unsupported economics scope %q", scope)
	}
}

// UpsertTransaction validates and persists a business transaction
// definition, minting an identifier and timestamps on first save.
func (s *Service) UpsertTransaction(ctx context.Context, t econ.BusinessTransaction) (econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, t.TenantID); err != nil {
		return econ.BusinessTransaction{}, err
	}
	if t.Name == "" {
		return econ.BusinessTransaction{}, core.Invalid("a business transaction must be named")
	}
	if len(t.WorkloadIDs) == 0 {
		return econ.BusinessTransaction{}, core.Invalid("transaction %q must name at least one workload on its critical path", t.Name)
	}
	now := s.clock().Now()
	if t.ID.IsZero() {
		t.ID = core.NewID("tx")
		t.CreatedAt = now
	} else if existing, err := s.Repos.Economics.GetTransaction(ctx, t.TenantID, t.ID); err == nil {
		t.CreatedAt = existing.CreatedAt
	}
	t.UpdatedAt = now
	if err := s.Repos.Economics.UpsertTransaction(ctx, t); err != nil {
		return econ.BusinessTransaction{}, err
	}
	return t, nil
}

// ListTransactions delegates to the repository.
func (s *Service) ListTransactions(ctx context.Context, tenant core.TenantID) ([]econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return s.Repos.Economics.ListTransactions(ctx, tenant)
}

// ListFootprints delegates to the repository, serving whatever Compute has
// most recently persisted for the scope and period rather than recomputing
// live — a dashboard listing every application's footprint would otherwise
// pay for N live computations on every page load.
func (s *Service) ListFootprints(ctx context.Context, tenant core.TenantID, scope econ.Scope, period core.Period) ([]econ.Footprint, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return s.Repos.Economics.ListFootprints(ctx, tenant, scope, period)
}

// UpsertCostSLO validates and persists a Cost SLO definition.
func (s *Service) UpsertCostSLO(ctx context.Context, slo econ.CostSLO) (econ.CostSLO, error) {
	if err := core.GuardTenant(ctx, slo.TenantID); err != nil {
		return econ.CostSLO{}, err
	}
	if slo.Name == "" {
		return econ.CostSLO{}, core.Invalid("a cost SLO must be named")
	}
	if slo.Kind == "" {
		return econ.CostSLO{}, core.Invalid("cost SLO %q must name a kind", slo.Name)
	}
	if slo.Direction == "" {
		slo.Direction = econ.DirectionAtMost
	}
	moneyKind := slo.Kind == econ.SLOAbsoluteSpend || slo.Kind == econ.SLOCostPerTransaction ||
		slo.Kind == econ.SLOCostPerRequest || slo.Kind == econ.SLOCostPerCustomer
	if moneyKind && slo.Target.IsZero() {
		return econ.CostSLO{}, core.Invalid("cost SLO %q of kind %s requires a money Target", slo.Name, slo.Kind)
	}
	if !moneyKind && slo.TargetRatio <= 0 {
		return econ.CostSLO{}, core.Invalid("cost SLO %q of kind %s requires a positive TargetRatio", slo.Name, slo.Kind)
	}
	now := s.clock().Now()
	if slo.ID.IsZero() {
		slo.ID = core.NewID("slo")
		slo.CreatedAt = now
	} else if existing, err := s.Repos.Economics.GetCostSLO(ctx, slo.TenantID, slo.ID); err == nil {
		slo.CreatedAt = existing.CreatedAt
	}
	slo.UpdatedAt = now
	if err := s.Repos.Economics.UpsertCostSLO(ctx, slo); err != nil {
		return econ.CostSLO{}, err
	}
	return slo, nil
}

// ListCostSLOs delegates to the repository.
func (s *Service) ListCostSLOs(ctx context.Context, tenant core.TenantID) ([]econ.CostSLO, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return s.Repos.Economics.ListCostSLOs(ctx, tenant)
}

// DeleteCostSLO delegates to the repository.
func (s *Service) DeleteCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return s.Repos.Economics.DeleteCostSLO(ctx, tenant, id)
}

// BudgetStates returns the most recently evaluated error-budget position for
// every Cost SLO, as last recorded by EvaluateSLOs. It intentionally does not
// recompute — a caller that wants a fresh number calls EvaluateSLOs, which is
// the more expensive path because it prices every SLO's actual value live.
func (s *Service) BudgetStates(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return s.Repos.Economics.ListBudgetStates(ctx, tenant)
}

// moneyOfRatio smuggles a dimensionless 0..1 ratio through core.Money's exact
// arithmetic so that ratio-denominated SLOs (waste ratio, efficiency score)
// can reuse the same Scale/Div/Ratio machinery as money-denominated ones,
// without econ.EvaluateBudget growing a second, float-based code path. The
// result is never persisted or rendered as currency.
func moneyOfRatio(r float64) core.Money { return core.USDollars(r) }
