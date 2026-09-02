package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
)

// economicsRepo implements ports.EconomicsRepository.
type economicsRepo struct{ s *Store }

func (r *economicsRepo) SaveFootprints(ctx context.Context, tenant core.TenantID, fps []econ.Footprint) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	for _, fp := range fps {
		r.s.data.Footprints[tenant] = append(r.s.data.Footprints[tenant], deepCopy(fp))
	}
	return nil
}

func (r *economicsRepo) GetFootprint(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID, period core.Period) (econ.Footprint, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.Footprint{}, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	var best *econ.Footprint
	for i := range r.s.data.Footprints[tenant] {
		fp := r.s.data.Footprints[tenant][i]
		if fp.Scope != scope || fp.ScopeID != scopeID {
			continue
		}
		if fp.Period.Start.Equal(period.Start) && fp.Period.End.Equal(period.End) {
			return deepCopy(fp), nil
		}
		if best == nil || fp.ComputedAt.After(best.ComputedAt) {
			f := fp
			best = &f
		}
	}
	if best != nil {
		return deepCopy(*best), nil
	}
	return econ.Footprint{}, core.NotFound("footprint", scopeID)
}

func (r *economicsRepo) ListFootprints(ctx context.Context, tenant core.TenantID, scope econ.Scope, period core.Period) ([]econ.Footprint, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	out := make([]econ.Footprint, 0)
	for _, fp := range r.s.data.Footprints[tenant] {
		if fp.Scope != scope {
			continue
		}
		if !period.IsZero() && !fp.Period.Overlaps(period) {
			continue
		}
		out = append(out, deepCopy(fp))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComputedAt.After(out[j].ComputedAt) })
	return out, nil
}

func (r *economicsRepo) UpsertTransaction(ctx context.Context, t econ.BusinessTransaction) error {
	if err := core.GuardTenant(ctx, t.TenantID); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	if r.s.data.Transactions[t.TenantID] == nil {
		r.s.data.Transactions[t.TenantID] = map[core.ID]econ.BusinessTransaction{}
	}
	r.s.data.Transactions[t.TenantID][t.ID] = deepCopy(t)
	return nil
}

func (r *economicsRepo) GetTransaction(ctx context.Context, tenant core.TenantID, id core.ID) (econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.BusinessTransaction{}, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	t, ok := r.s.data.Transactions[tenant][id]
	if !ok {
		return econ.BusinessTransaction{}, core.NotFound("business_transaction", id)
	}
	return deepCopy(t), nil
}

func (r *economicsRepo) GetTransactionByName(ctx context.Context, tenant core.TenantID, name string) (econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.BusinessTransaction{}, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	for _, t := range r.s.data.Transactions[tenant] {
		if t.Name == name {
			return deepCopy(t), nil
		}
	}
	return econ.BusinessTransaction{}, core.NotFound("business_transaction", name)
}

func (r *economicsRepo) ListTransactions(ctx context.Context, tenant core.TenantID) ([]econ.BusinessTransaction, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	out := make([]econ.BusinessTransaction, 0, len(r.s.data.Transactions[tenant]))
	for _, t := range r.s.data.Transactions[tenant] {
		out = append(out, deepCopy(t))
	}
	sortByCreatedThenID(out, func(t econ.BusinessTransaction) (string, string) {
		return t.CreatedAt.Format(sortTimeLayout), t.ID.String()
	})
	return out, nil
}

func (r *economicsRepo) SaveUnitEconomics(ctx context.Context, tenant core.TenantID, ue []econ.UnitEconomics) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	for _, u := range ue {
		r.s.data.UnitEconomics[tenant] = append(r.s.data.UnitEconomics[tenant], deepCopy(u))
	}
	return nil
}

func (r *economicsRepo) ListUnitEconomics(ctx context.Context, tenant core.TenantID, transactionID core.ID, from, to time.Time) ([]econ.UnitEconomics, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	out := make([]econ.UnitEconomics, 0)
	for _, u := range r.s.data.UnitEconomics[tenant] {
		if u.TransactionID != transactionID {
			continue
		}
		if !from.IsZero() && u.Period.Start.Before(from) {
			continue
		}
		if !to.IsZero() && !u.Period.Start.Before(to) {
			continue
		}
		out = append(out, deepCopy(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period.Start.Before(out[j].Period.Start) })
	return out, nil
}

func (r *economicsRepo) UpsertCostSLO(ctx context.Context, s econ.CostSLO) error {
	if err := core.GuardTenant(ctx, s.TenantID); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	if r.s.data.CostSLOs[s.TenantID] == nil {
		r.s.data.CostSLOs[s.TenantID] = map[core.ID]econ.CostSLO{}
	}
	r.s.data.CostSLOs[s.TenantID][s.ID] = deepCopy(s)
	return nil
}

func (r *economicsRepo) GetCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) (econ.CostSLO, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.CostSLO{}, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	s, ok := r.s.data.CostSLOs[tenant][id]
	if !ok {
		return econ.CostSLO{}, core.NotFound("cost_slo", id)
	}
	return deepCopy(s), nil
}

func (r *economicsRepo) ListCostSLOs(ctx context.Context, tenant core.TenantID) ([]econ.CostSLO, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	out := make([]econ.CostSLO, 0, len(r.s.data.CostSLOs[tenant]))
	for _, s := range r.s.data.CostSLOs[tenant] {
		out = append(out, deepCopy(s))
	}
	sortByCreatedThenID(out, func(s econ.CostSLO) (string, string) { return s.CreatedAt.Format(sortTimeLayout), s.ID.String() })
	return out, nil
}

func (r *economicsRepo) DeleteCostSLO(ctx context.Context, tenant core.TenantID, id core.ID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	if _, ok := r.s.data.CostSLOs[tenant][id]; !ok {
		return core.NotFound("cost_slo", id)
	}
	delete(r.s.data.CostSLOs[tenant], id)
	return nil
}

func (r *economicsRepo) SaveBudgetState(ctx context.Context, b econ.EconomicErrorBudget) error {
	if err := core.GuardTenant(ctx, b.TenantID); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	r.s.data.BudgetStates[b.TenantID] = append(r.s.data.BudgetStates[b.TenantID], deepCopy(b))
	return nil
}

func (r *economicsRepo) ListBudgetStates(ctx context.Context, tenant core.TenantID) ([]econ.EconomicErrorBudget, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	out := make([]econ.EconomicErrorBudget, len(r.s.data.BudgetStates[tenant]))
	for i, b := range r.s.data.BudgetStates[tenant] {
		out[i] = deepCopy(b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EvaluatedAt.After(out[j].EvaluatedAt) })
	return out, nil
}

func (r *economicsRepo) SaveEfficiencyScore(ctx context.Context, s econ.EfficiencyScore) error {
	if err := core.GuardTenant(ctx, s.TenantID); err != nil {
		return err
	}
	r.s.econMu.Lock()
	defer r.s.econMu.Unlock()
	r.s.data.EfficiencyScores[s.TenantID] = append(r.s.data.EfficiencyScores[s.TenantID], deepCopy(s))
	return nil
}

func (r *economicsRepo) GetEfficiencyScore(ctx context.Context, tenant core.TenantID, scope econ.Scope, scopeID core.ID) (econ.EfficiencyScore, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return econ.EfficiencyScore{}, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	var best *econ.EfficiencyScore
	for i := range r.s.data.EfficiencyScores[tenant] {
		s := r.s.data.EfficiencyScores[tenant][i]
		if s.Scope != scope || s.ScopeID != scopeID {
			continue
		}
		if best == nil || s.ComputedAt.After(best.ComputedAt) {
			v := s
			best = &v
		}
	}
	if best == nil {
		return econ.EfficiencyScore{}, core.NotFound("efficiency_score", scopeID)
	}
	return deepCopy(*best), nil
}

func (r *economicsRepo) ListEfficiencyScores(ctx context.Context, tenant core.TenantID, scope econ.Scope) ([]econ.EfficiencyScore, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.econMu.RLock()
	defer r.s.econMu.RUnlock()
	out := make([]econ.EfficiencyScore, 0)
	for _, s := range r.s.data.EfficiencyScores[tenant] {
		if s.Scope == scope {
			out = append(out, deepCopy(s))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComputedAt.After(out[j].ComputedAt) })
	return out, nil
}
