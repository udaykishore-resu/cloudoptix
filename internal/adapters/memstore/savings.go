package memstore

import (
	"context"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// savingsRepo implements ports.SavingsRepository.
type savingsRepo struct{ s *Store }

func (r *savingsRepo) Save(ctx context.Context, rec execute.SavingsRecord) error {
	if err := core.GuardTenant(ctx, rec.TenantID); err != nil {
		return err
	}
	r.s.savingsMu.Lock()
	defer r.s.savingsMu.Unlock()
	if r.s.data.SavingsRecords[rec.TenantID] == nil {
		r.s.data.SavingsRecords[rec.TenantID] = map[core.ID]execute.SavingsRecord{}
	}
	r.s.data.SavingsRecords[rec.TenantID][rec.RecommendationID] = deepCopy(rec)
	return nil
}

func (r *savingsRepo) Get(ctx context.Context, tenant core.TenantID, recommendationID core.ID) (execute.SavingsRecord, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.SavingsRecord{}, err
	}
	r.s.savingsMu.RLock()
	defer r.s.savingsMu.RUnlock()
	rec, ok := r.s.data.SavingsRecords[tenant][recommendationID]
	if !ok {
		return execute.SavingsRecord{}, core.NotFound("savings_record", recommendationID)
	}
	return deepCopy(rec), nil
}

func (r *savingsRepo) matchingRecords(tenant core.TenantID, period core.Period) []execute.SavingsRecord {
	r.s.savingsMu.RLock()
	defer r.s.savingsMu.RUnlock()
	out := make([]execute.SavingsRecord, 0)
	for _, rec := range r.s.data.SavingsRecords[tenant] {
		if !period.IsZero() && !period.Contains(rec.CreatedAt) {
			continue
		}
		out = append(out, deepCopy(rec))
	}
	return out
}

func (r *savingsRepo) List(ctx context.Context, tenant core.TenantID, period core.Period) ([]execute.SavingsRecord, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	out := r.matchingRecords(tenant, period)
	sortByCreatedThenID(out, func(rec execute.SavingsRecord) (string, string) {
		return rec.CreatedAt.Format(sortTimeLayout), rec.ID.String()
	})
	return out, nil
}

func (r *savingsRepo) Funnel(ctx context.Context, tenant core.TenantID, period core.Period) (execute.Funnel, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return execute.Funnel{}, err
	}
	records := r.matchingRecords(tenant, period)
	return execute.BuildFunnel(tenant, period, records), nil
}

func (r *savingsRepo) SaveOutcome(ctx context.Context, o execute.Outcome) error {
	if err := core.GuardTenant(ctx, o.TenantID); err != nil {
		return err
	}
	r.s.savingsMu.Lock()
	defer r.s.savingsMu.Unlock()
	if o.ID.IsZero() {
		o.ID = core.NewID("otc")
	}
	r.s.data.Outcomes[o.TenantID] = append(r.s.data.Outcomes[o.TenantID], deepCopy(o))
	return nil
}

func (r *savingsRepo) ListOutcomes(ctx context.Context, tenant core.TenantID, ruleID optimize.RuleID, limit int) ([]execute.Outcome, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.savingsMu.RLock()
	items := make([]execute.Outcome, 0)
	for _, o := range r.s.data.Outcomes[tenant] {
		if ruleID == "" || o.RuleID == ruleID {
			items = append(items, deepCopy(o))
		}
	}
	r.s.savingsMu.RUnlock()

	sort.Slice(items, func(i, j int) bool { return items[i].ObservedAt.After(items[j].ObservedAt) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (r *savingsRepo) SaveCalibration(ctx context.Context, c execute.RuleCalibration) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	r.s.savingsMu.Lock()
	defer r.s.savingsMu.Unlock()
	if r.s.data.Calibrations[c.TenantID] == nil {
		r.s.data.Calibrations[c.TenantID] = map[optimize.RuleID]execute.RuleCalibration{}
	}
	r.s.data.Calibrations[c.TenantID][c.RuleID] = deepCopy(c)
	return nil
}

func (r *savingsRepo) LoadCalibrations(ctx context.Context, tenant core.TenantID) (map[optimize.RuleID]execute.RuleCalibration, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.savingsMu.RLock()
	defer r.s.savingsMu.RUnlock()
	out := make(map[optimize.RuleID]execute.RuleCalibration, len(r.s.data.Calibrations[tenant]))
	for k, v := range r.s.data.Calibrations[tenant] {
		out[k] = deepCopy(v)
	}
	return out, nil
}
