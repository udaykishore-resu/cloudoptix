package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// discoveryRunRepo implements ports.DiscoveryRunRepository.
type discoveryRunRepo struct{ s *Store }

func (r *discoveryRunRepo) Create(ctx context.Context, run ports.DiscoveryRun) error {
	if err := core.GuardTenant(ctx, run.TenantID); err != nil {
		return err
	}
	r.s.discoveryMu.Lock()
	defer r.s.discoveryMu.Unlock()
	if r.s.data.DiscoveryRuns[run.TenantID] == nil {
		r.s.data.DiscoveryRuns[run.TenantID] = map[core.ID]ports.DiscoveryRun{}
	}
	r.s.data.DiscoveryRuns[run.TenantID][run.ID] = deepCopy(run)
	return nil
}

func (r *discoveryRunRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryRun{}, err
	}
	r.s.discoveryMu.RLock()
	defer r.s.discoveryMu.RUnlock()
	run, ok := r.s.data.DiscoveryRuns[tenant][id]
	if !ok {
		return ports.DiscoveryRun{}, core.NotFound("discovery_run", id)
	}
	return deepCopy(run), nil
}

func (r *discoveryRunRepo) Update(ctx context.Context, run ports.DiscoveryRun) error {
	if err := core.GuardTenant(ctx, run.TenantID); err != nil {
		return err
	}
	r.s.discoveryMu.Lock()
	defer r.s.discoveryMu.Unlock()
	if _, ok := r.s.data.DiscoveryRuns[run.TenantID][run.ID]; !ok {
		return core.NotFound("discovery_run", run.ID)
	}
	r.s.data.DiscoveryRuns[run.TenantID][run.ID] = deepCopy(run)
	return nil
}

func (r *discoveryRunRepo) ListRecent(ctx context.Context, tenant core.TenantID, limit int) ([]ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.discoveryMu.RLock()
	items := make([]ports.DiscoveryRun, 0, len(r.s.data.DiscoveryRuns[tenant]))
	for _, run := range r.s.data.DiscoveryRuns[tenant] {
		items = append(items, deepCopy(run))
	}
	r.s.discoveryMu.RUnlock()

	sortByCreatedThenID(items, func(run ports.DiscoveryRun) (string, string) {
		return run.StartedAt.Format(sortTimeLayout), run.ID.String()
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (r *discoveryRunRepo) Latest(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (ports.DiscoveryRun, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.DiscoveryRun{}, err
	}
	r.s.discoveryMu.RLock()
	defer r.s.discoveryMu.RUnlock()
	var best *ports.DiscoveryRun
	for _, run := range r.s.data.DiscoveryRuns[tenant] {
		if run.AccountID != accountID {
			continue
		}
		if best == nil || run.StartedAt.After(best.StartedAt) {
			v := run
			best = &v
		}
	}
	if best == nil {
		return ports.DiscoveryRun{}, core.NotFound("discovery_run", accountID)
	}
	return deepCopy(*best), nil
}
