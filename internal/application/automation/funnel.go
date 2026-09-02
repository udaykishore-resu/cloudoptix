package automation

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
)

// Funnel reports the six-stage savings lifecycle for a period. The
// aggregation and leakage analysis themselves live in execute.BuildFunnel
// (domain logic, pure and independently testable); this method is a thin
// pass-through to ports.SavingsRepository.Funnel, which every adapter
// implements by loading the tenant's SavingsRecord rows for the period and
// calling that same domain function — this package holds no funnel logic of
// its own to avoid two implementations drifting apart.
func (s *Service) Funnel(ctx context.Context, tenant core.TenantID, period core.Period) (execute.Funnel, error) {
	return s.d.Savings.Funnel(ctx, tenant, period)
}
