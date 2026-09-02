package memstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// executionRepo implements ports.ExecutionRepository.
type executionRepo struct{ s *Store }

func (r *executionRepo) CreatePlan(ctx context.Context, p execute.Plan) error {
	if err := core.GuardTenant(ctx, p.TenantID); err != nil {
		return err
	}
	r.s.execMu.Lock()
	defer r.s.execMu.Unlock()
	if r.s.data.Plans[p.TenantID] == nil {
		r.s.data.Plans[p.TenantID] = map[core.ID]execute.Plan{}
	}
	if _, exists := r.s.data.Plans[p.TenantID][p.ID]; exists {
		return core.NewError(core.ErrAlreadyExists, "plan_exists", "execution plan %s already exists", p.ID)
	}
	r.s.data.Plans[p.TenantID][p.ID] = deepCopy(p)
	return nil
}

func (r *executionRepo) GetPlan(ctx context.Context, tenant core.TenantID, id core.ID) (execute.Plan, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.Plan{}, err
	}
	r.s.execMu.RLock()
	defer r.s.execMu.RUnlock()
	p, ok := r.s.data.Plans[tenant][id]
	if !ok {
		return execute.Plan{}, core.NotFound("execution_plan", id)
	}
	return deepCopy(p), nil
}

func (r *executionRepo) UpdatePlan(ctx context.Context, p execute.Plan) error {
	if err := core.GuardTenant(ctx, p.TenantID); err != nil {
		return err
	}
	r.s.execMu.Lock()
	defer r.s.execMu.Unlock()
	if _, ok := r.s.data.Plans[p.TenantID][p.ID]; !ok {
		return core.NotFound("execution_plan", p.ID)
	}
	r.s.data.Plans[p.TenantID][p.ID] = deepCopy(p)
	return nil
}

func (r *executionRepo) ListPlans(ctx context.Context, tenant core.TenantID, states []execute.PlanState, opts ports.ListOptions) (ports.Page[execute.Plan], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[execute.Plan]{}, err
	}
	r.s.execMu.RLock()
	items := make([]execute.Plan, 0)
	for _, p := range r.s.data.Plans[tenant] {
		if len(states) == 0 || containsVal(states, p.State) {
			items = append(items, deepCopy(p))
		}
	}
	r.s.execMu.RUnlock()

	keyOf := func(p execute.Plan) (string, string) { return p.CreatedAt.Format(sortTimeLayout), p.ID.String() }
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

// ClaimDuePlans and ClaimPlansAwaitingValidation intentionally carry no
// tenant argument: the interface models a background worker sweeping across
// every tenant's due work, mirroring a Postgres adapter's
// "UPDATE ... WHERE state = ... RETURNING *" run against the whole table
// under FOR UPDATE SKIP LOCKED. The single execMu.Lock() held for the whole
// select-then-mutate body here gives the equivalent guarantee for free: two
// goroutines calling this concurrently serialise on the lock, and the second
// one's scan runs only after the first has already moved its claimed plans
// out of the eligible state, so the two claim sets are always disjoint.
func (r *executionRepo) ClaimDuePlans(ctx context.Context, now time.Time, workerID string, limit int) ([]execute.Plan, error) {
	if limit <= 0 {
		limit = 50
	}
	r.s.execMu.Lock()
	defer r.s.execMu.Unlock()

	type candidate struct {
		tenant core.TenantID
		id     core.ID
		due    time.Time
	}
	var candidates []candidate
	for tenant, plans := range r.s.data.Plans {
		for id, p := range plans {
			if p.State != execute.PlanApproved && p.State != execute.PlanScheduled {
				continue
			}
			due := p.CreatedAt
			if p.ScheduledFor != nil {
				if now.Before(*p.ScheduledFor) {
					continue
				}
				due = *p.ScheduledFor
			}
			candidates = append(candidates, candidate{tenant: tenant, id: id, due: due})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].due.Equal(candidates[j].due) {
			return candidates[i].due.Before(candidates[j].due)
		}
		return candidates[i].id < candidates[j].id
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	claimed := make([]execute.Plan, 0, len(candidates))
	for _, c := range candidates {
		p := r.s.data.Plans[c.tenant][c.id]
		p.State = execute.PlanPreflight
		startedAt := now
		p.StartedAt = &startedAt
		r.s.data.Plans[c.tenant][c.id] = p
		r.s.data.ExecLeases[c.id] = workerID
		claimed = append(claimed, deepCopy(p))
	}
	return claimed, nil
}

// ClaimPlansAwaitingValidation claims executed plans whose declared
// observation window has elapsed since execution finished, transitioning
// them to PlanValidating under the same single-lock atomicity as
// ClaimDuePlans.
func (r *executionRepo) ClaimPlansAwaitingValidation(ctx context.Context, now time.Time, workerID string, limit int) ([]execute.Plan, error) {
	if limit <= 0 {
		limit = 50
	}
	r.s.execMu.Lock()
	defer r.s.execMu.Unlock()

	type candidate struct {
		tenant core.TenantID
		id     core.ID
		ready  time.Time
	}
	var candidates []candidate
	for tenant, plans := range r.s.data.Plans {
		for id, p := range plans {
			if p.State != execute.PlanExecuted || p.FinishedAt == nil {
				continue
			}
			ready := p.FinishedAt.Add(p.Validation.ObservationWindow)
			if now.Before(ready) {
				continue
			}
			candidates = append(candidates, candidate{tenant: tenant, id: id, ready: ready})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].ready.Equal(candidates[j].ready) {
			return candidates[i].ready.Before(candidates[j].ready)
		}
		return candidates[i].id < candidates[j].id
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	claimed := make([]execute.Plan, 0, len(candidates))
	for _, c := range candidates {
		p := r.s.data.Plans[c.tenant][c.id]
		p.State = execute.PlanValidating
		r.s.data.Plans[c.tenant][c.id] = p
		r.s.data.ExecLeases[c.id] = workerID
		claimed = append(claimed, deepCopy(p))
	}
	return claimed, nil
}

func snapshotKey(planID, resourceID core.ID) string { return fmt.Sprintf("%s/%s", planID, resourceID) }

func (r *executionRepo) SaveSnapshot(ctx context.Context, snap execute.Snapshot) error {
	if err := core.GuardTenant(ctx, snap.TenantID); err != nil {
		return err
	}
	r.s.execMu.Lock()
	defer r.s.execMu.Unlock()
	if r.s.data.Snapshots[snap.TenantID] == nil {
		r.s.data.Snapshots[snap.TenantID] = map[string]execute.Snapshot{}
	}
	r.s.data.Snapshots[snap.TenantID][snapshotKey(snap.PlanID, snap.ResourceID)] = deepCopy(snap)
	return nil
}

func (r *executionRepo) GetSnapshot(ctx context.Context, tenant core.TenantID, planID core.ID, resourceID core.ID) (execute.Snapshot, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.Snapshot{}, err
	}
	r.s.execMu.RLock()
	defer r.s.execMu.RUnlock()
	snap, ok := r.s.data.Snapshots[tenant][snapshotKey(planID, resourceID)]
	if !ok {
		return execute.Snapshot{}, core.NotFound("execution_snapshot", planID)
	}
	return deepCopy(snap), nil
}

func (r *executionRepo) SaveValidation(ctx context.Context, v execute.ValidationResult) error {
	if err := core.GuardTenant(ctx, v.TenantID); err != nil {
		return err
	}
	r.s.execMu.Lock()
	defer r.s.execMu.Unlock()
	if r.s.data.Validations[v.TenantID] == nil {
		r.s.data.Validations[v.TenantID] = map[core.ID]execute.ValidationResult{}
	}
	r.s.data.Validations[v.TenantID][v.PlanID] = deepCopy(v)
	return nil
}

func (r *executionRepo) GetValidation(ctx context.Context, tenant core.TenantID, planID core.ID) (execute.ValidationResult, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.ValidationResult{}, err
	}
	r.s.execMu.RLock()
	defer r.s.execMu.RUnlock()
	v, ok := r.s.data.Validations[tenant][planID]
	if !ok {
		return execute.ValidationResult{}, core.NotFound("validation_result", planID)
	}
	return deepCopy(v), nil
}
