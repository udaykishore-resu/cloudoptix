package memstore

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// recommendationRepo implements ports.RecommendationRepository.
type recommendationRepo struct{ s *Store }

func (r *recommendationRepo) SaveBatch(ctx context.Context, tenant core.TenantID, recs []optimize.Recommendation) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.recMu.Lock()
	defer r.s.recMu.Unlock()
	if r.s.data.Recommendations[tenant] == nil {
		r.s.data.Recommendations[tenant] = map[core.ID]optimize.Recommendation{}
	}
	for _, rec := range recs {
		if rec.TenantID != tenant {
			return core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
				"recommendation %s belongs to tenant %s, not %s", rec.ID, rec.TenantID, tenant)
		}
		if rec.ID.IsZero() {
			rec.ID = core.NewID("rec")
		}
		r.s.data.Recommendations[tenant][rec.ID] = deepCopy(rec)
	}
	return nil
}

func (r *recommendationRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (optimize.Recommendation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return optimize.Recommendation{}, err
	}
	r.s.recMu.RLock()
	defer r.s.recMu.RUnlock()
	rec, ok := r.s.data.Recommendations[tenant][id]
	if !ok {
		return optimize.Recommendation{}, core.NotFound("recommendation", id)
	}
	return deepCopy(rec), nil
}

// resourceApplications is shared with costRepo's own lookup, kept as a
// separate copy here rather than a common method on Store so each repo file
// stays independently readable; both fully release resourceMu before their
// caller takes their own lock.
func (r *recommendationRepo) resourceApplications(tenant core.TenantID) map[core.ID]core.ID {
	r.s.resourceMu.RLock()
	defer r.s.resourceMu.RUnlock()
	out := make(map[core.ID]core.ID, len(r.s.data.Resources[tenant]))
	for id, res := range r.s.data.Resources[tenant] {
		if !res.ApplicationID.IsZero() {
			out[id] = res.ApplicationID
		}
	}
	return out
}

func matchesRecommendationFilter(rec optimize.Recommendation, f ports.RecommendationFilter, resourceApp map[core.ID]core.ID) bool {
	if len(f.Statuses) > 0 && !containsVal(f.Statuses, rec.Status) {
		return false
	}
	if len(f.Categories) > 0 && !containsVal(f.Categories, rec.Finding.Category) {
		return false
	}
	if len(f.Actions) > 0 && !containsVal(f.Actions, rec.Action) {
		return false
	}
	if len(f.RuleIDs) > 0 && !containsVal(f.RuleIDs, rec.Finding.RuleID) {
		return false
	}
	if len(f.Environments) > 0 && !containsVal(f.Environments, rec.Finding.Environment) {
		return false
	}
	if len(f.AccountIDs) > 0 && !containsVal(f.AccountIDs, rec.Finding.AccountID) {
		return false
	}
	if !f.ApplicationID.IsZero() && resourceApp[rec.Finding.ResourceID] != f.ApplicationID {
		return false
	}
	if !f.ResourceID.IsZero() && rec.Finding.ResourceID != f.ResourceID {
		return false
	}
	if !f.MinSaving.IsZero() && rec.EstimatedMonthlySaving.LessThan(f.MinSaving) {
		return false
	}
	if f.MinConfidence > 0 && float64(rec.Confidence) < f.MinConfidence {
		return false
	}
	if f.MaxRisk != "" && rec.Risk.Level.Order() > f.MaxRisk.Order() {
		return false
	}
	if f.AutoExecutableOnly && !rec.AutoExecutable {
		return false
	}
	return true
}

func (r *recommendationRepo) List(ctx context.Context, tenant core.TenantID, f ports.RecommendationFilter, opts ports.ListOptions) (ports.Page[optimize.Recommendation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[optimize.Recommendation]{}, err
	}
	var resourceApp map[core.ID]core.ID
	if !f.ApplicationID.IsZero() {
		resourceApp = r.resourceApplications(tenant)
	}

	r.s.recMu.RLock()
	items := make([]optimize.Recommendation, 0)
	for _, rec := range r.s.data.Recommendations[tenant] {
		if matchesRecommendationFilter(rec, f, resourceApp) {
			items = append(items, deepCopy(rec))
		}
	}
	r.s.recMu.RUnlock()

	keyOf := func(rec optimize.Recommendation) (string, string) {
		return rec.CreatedAt.Format(sortTimeLayout), rec.ID.String()
	}
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *recommendationRepo) UpdateStatus(ctx context.Context, tenant core.TenantID, id core.ID, status optimize.Status, reason, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.recMu.Lock()
	defer r.s.recMu.Unlock()
	rec, ok := r.s.data.Recommendations[tenant][id]
	if !ok {
		return core.NotFound("recommendation", id)
	}
	rec.Status = status
	rec.StatusReason = reason
	rec.UpdatedAt = time.Now().UTC()
	_ = by
	r.s.data.Recommendations[tenant][id] = rec
	return nil
}

func (r *recommendationRepo) Update(ctx context.Context, rec optimize.Recommendation) error {
	if err := core.GuardTenant(ctx, rec.TenantID); err != nil {
		return err
	}
	r.s.recMu.Lock()
	defer r.s.recMu.Unlock()
	if _, ok := r.s.data.Recommendations[rec.TenantID][rec.ID]; !ok {
		return core.NotFound("recommendation", rec.ID)
	}
	rec.UpdatedAt = time.Now().UTC()
	r.s.data.Recommendations[rec.TenantID][rec.ID] = deepCopy(rec)
	return nil
}

func (r *recommendationRepo) SupersedeStale(ctx context.Context, tenant core.TenantID, before time.Time, keepIDs []core.ID) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	keep := make(map[core.ID]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}
	r.s.recMu.Lock()
	defer r.s.recMu.Unlock()
	n := 0
	for id, rec := range r.s.data.Recommendations[tenant] {
		if keep[id] || rec.Status.Terminal() || !rec.CreatedAt.Before(before) {
			continue
		}
		rec.Status = optimize.StatusSuperseded
		rec.UpdatedAt = time.Now().UTC()
		r.s.data.Recommendations[tenant][id] = rec
		n++
	}
	return n, nil
}

func (r *recommendationRepo) Summary(ctx context.Context, tenant core.TenantID) (ports.RecommendationSummary, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.RecommendationSummary{}, err
	}
	r.s.recMu.RLock()
	defer r.s.recMu.RUnlock()

	sum := ports.RecommendationSummary{
		TotalMonthlySaving: core.ZeroUSD(),
		ByCategory:         map[optimize.Category]int{},
		SavingByCategory:   map[optimize.Category]core.Money{},
		ByRisk:             map[core.RiskLevel]int{},
	}
	for _, rec := range r.s.data.Recommendations[tenant] {
		switch rec.Status {
		case optimize.StatusOpen:
			sum.Open++
			sum.ByCategory[rec.Finding.Category]++
			sum.ByRisk[rec.Risk.Level]++
			if rec.AutoExecutable {
				sum.AutoExecutable++
			}
			// Money is summed over primaries only; the alternatives are
			// counted above and reconciled below. See
			// ports.RecommendationSummary for why the two differ.
			if !rec.CountsTowardTotal() {
				sum.MutuallyExclusiveAlternatives++
				continue
			}
			sum.TotalMonthlySaving = sum.TotalMonthlySaving.MustAdd(rec.EstimatedMonthlySaving)
			sum.SavingByCategory[rec.Finding.Category] = sum.SavingByCategory[rec.Finding.Category].MustAdd(rec.EstimatedMonthlySaving)
		case optimize.StatusUnderReview:
			sum.AwaitingApproval++
		}
	}
	return sum, nil
}
