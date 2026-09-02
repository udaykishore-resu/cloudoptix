package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// applicationRepo implements ports.ApplicationRepository.
type applicationRepo struct{ s *Store }

func (r *applicationRepo) UpsertApplication(ctx context.Context, a cloud.Application) error {
	if err := core.GuardTenant(ctx, a.TenantID); err != nil {
		return err
	}
	r.s.appMu.Lock()
	defer r.s.appMu.Unlock()
	if r.s.data.Applications[a.TenantID] == nil {
		r.s.data.Applications[a.TenantID] = map[core.ID]cloud.Application{}
	}
	r.s.data.Applications[a.TenantID][a.ID] = deepCopy(a)
	return nil
}

func (r *applicationRepo) GetApplication(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Application, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Application{}, err
	}
	r.s.appMu.RLock()
	defer r.s.appMu.RUnlock()
	a, ok := r.s.data.Applications[tenant][id]
	if !ok {
		return cloud.Application{}, core.NotFound("application", id)
	}
	return deepCopy(a), nil
}

func (r *applicationRepo) ListApplications(ctx context.Context, tenant core.TenantID) ([]cloud.Application, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.appMu.RLock()
	defer r.s.appMu.RUnlock()
	out := make([]cloud.Application, 0, len(r.s.data.Applications[tenant]))
	for _, a := range r.s.data.Applications[tenant] {
		out = append(out, deepCopy(a))
	}
	sortByCreatedThenID(out, func(a cloud.Application) (string, string) {
		return a.CreatedAt.Format(sortTimeLayout), a.ID.String()
	})
	return out, nil
}

func (r *applicationRepo) UpsertWorkload(ctx context.Context, w cloud.Workload) error {
	if err := core.GuardTenant(ctx, w.TenantID); err != nil {
		return err
	}
	r.s.appMu.Lock()
	defer r.s.appMu.Unlock()
	if r.s.data.Workloads[w.TenantID] == nil {
		r.s.data.Workloads[w.TenantID] = map[core.ID]cloud.Workload{}
	}
	r.s.data.Workloads[w.TenantID][w.ID] = deepCopy(w)
	return nil
}

func (r *applicationRepo) GetWorkload(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Workload, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Workload{}, err
	}
	r.s.appMu.RLock()
	defer r.s.appMu.RUnlock()
	w, ok := r.s.data.Workloads[tenant][id]
	if !ok {
		return cloud.Workload{}, core.NotFound("workload", id)
	}
	return deepCopy(w), nil
}

func (r *applicationRepo) ListWorkloads(ctx context.Context, tenant core.TenantID, applicationID core.ID) ([]cloud.Workload, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.appMu.RLock()
	defer r.s.appMu.RUnlock()
	out := make([]cloud.Workload, 0)
	for _, w := range r.s.data.Workloads[tenant] {
		if applicationID.IsZero() || w.ApplicationID == applicationID {
			out = append(out, deepCopy(w))
		}
	}
	sortByCreatedThenID(out, func(w cloud.Workload) (string, string) {
		return w.CreatedAt.Format(sortTimeLayout), w.ID.String()
	})
	return out, nil
}
