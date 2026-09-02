package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
)

// policyRepo implements ports.PolicyRepository.
type policyRepo struct{ s *Store }

func (r *policyRepo) Save(ctx context.Context, p govern.Policy) error {
	if err := core.GuardTenant(ctx, p.TenantID); err != nil {
		return err
	}
	r.s.policyMu.Lock()
	defer r.s.policyMu.Unlock()
	if r.s.data.Policies[p.TenantID] == nil {
		r.s.data.Policies[p.TenantID] = map[core.ID]govern.Policy{}
	}
	r.s.data.Policies[p.TenantID][p.ID] = deepCopy(p)
	return nil
}

func (r *policyRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Policy, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Policy{}, err
	}
	r.s.policyMu.RLock()
	defer r.s.policyMu.RUnlock()
	p, ok := r.s.data.Policies[tenant][id]
	if !ok {
		return govern.Policy{}, core.NotFound("policy", id)
	}
	return deepCopy(p), nil
}

func (r *policyRepo) GetActive(ctx context.Context, tenant core.TenantID) (govern.Policy, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Policy{}, err
	}
	r.s.policyMu.RLock()
	defer r.s.policyMu.RUnlock()
	id, ok := r.s.data.PolicyActive[tenant]
	if !ok {
		return govern.Policy{}, core.NotFound("active_policy", tenant)
	}
	return deepCopy(r.s.data.Policies[tenant][id]), nil
}

func (r *policyRepo) ListVersions(ctx context.Context, tenant core.TenantID, name string) ([]govern.Policy, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.policyMu.RLock()
	defer r.s.policyMu.RUnlock()
	out := make([]govern.Policy, 0)
	for _, p := range r.s.data.Policies[tenant] {
		if p.Name == name {
			out = append(out, deepCopy(p))
		}
	}
	sortByCreatedThenID(out, func(p govern.Policy) (string, string) { return fmtInt(p.Version), p.ID.String() })
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (r *policyRepo) Activate(ctx context.Context, tenant core.TenantID, id core.ID, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.policyMu.Lock()
	defer r.s.policyMu.Unlock()
	p, ok := r.s.data.Policies[tenant][id]
	if !ok {
		return core.NotFound("policy", id)
	}
	p.Enabled = true
	p.ActivatedAt = timeNowUTC()
	_ = by
	r.s.data.Policies[tenant][id] = p
	r.s.data.PolicyActive[tenant] = id
	return nil
}

func (r *policyRepo) SaveDecision(ctx context.Context, d govern.Decision) error {
	if err := core.GuardTenant(ctx, d.TenantID); err != nil {
		return err
	}
	r.s.policyMu.Lock()
	defer r.s.policyMu.Unlock()
	if r.s.data.Decisions[d.TenantID] == nil {
		r.s.data.Decisions[d.TenantID] = map[core.ID]govern.Decision{}
	}
	if d.ID.IsZero() {
		d.ID = core.NewID("pd")
	}
	r.s.data.Decisions[d.TenantID][d.ID] = deepCopy(d)
	return nil
}

func (r *policyRepo) GetDecision(ctx context.Context, tenant core.TenantID, id core.ID) (govern.Decision, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return govern.Decision{}, err
	}
	r.s.policyMu.RLock()
	defer r.s.policyMu.RUnlock()
	d, ok := r.s.data.Decisions[tenant][id]
	if !ok {
		return govern.Decision{}, core.NotFound("policy_decision", id)
	}
	return deepCopy(d), nil
}

func (r *policyRepo) ListDecisions(ctx context.Context, tenant core.TenantID, recommendationID core.ID) ([]govern.Decision, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.policyMu.RLock()
	defer r.s.policyMu.RUnlock()
	out := make([]govern.Decision, 0)
	for _, d := range r.s.data.Decisions[tenant] {
		if d.RecommendationID == recommendationID {
			out = append(out, deepCopy(d))
		}
	}
	sortByCreatedThenID(out, func(d govern.Decision) (string, string) {
		return d.DecidedAt.Format(sortTimeLayout), d.ID.String()
	})
	return out, nil
}
