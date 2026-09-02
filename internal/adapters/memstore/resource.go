package memstore

import (
	"context"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// resourceRepo implements ports.ResourceRepository.
type resourceRepo struct{ s *Store }

func (r *resourceRepo) UpsertBatch(ctx context.Context, tenant core.TenantID, resources []cloud.Resource) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	r.s.resourceMu.Lock()
	defer r.s.resourceMu.Unlock()

	if r.s.data.Resources[tenant] == nil {
		r.s.data.Resources[tenant] = map[core.ID]cloud.Resource{}
	}
	if r.s.data.ResourceByKey[tenant] == nil {
		r.s.data.ResourceByKey[tenant] = map[string]core.ID{}
	}
	byKey := r.s.data.ResourceByKey[tenant]
	byID := r.s.data.Resources[tenant]

	n := 0
	for _, res := range resources {
		if res.TenantID != tenant {
			return n, core.NewError(core.ErrTenantMismatch, "tenant_mismatch",
				"resource %s belongs to tenant %s, not %s", res.NativeID, res.TenantID, tenant)
		}
		key := res.Key()
		if existingID, ok := byKey[key]; ok {
			// Idempotent on Key(): keep the existing CloudOptix id and
			// FirstSeenAt, refresh everything else, exactly as a re-scan of
			// an already-known resource should behave.
			existing := byID[existingID]
			res.ID = existing.ID
			if existing.FirstSeenAt.After(time.Time{}) {
				res.FirstSeenAt = existing.FirstSeenAt
			}
		} else {
			if res.ID.IsZero() {
				res.ID = core.NewID("res")
			}
			if res.FirstSeenAt.IsZero() {
				res.FirstSeenAt = res.LastSeenAt
			}
			byKey[key] = res.ID
		}
		byID[res.ID] = deepCopy(res)
		n++
	}
	return n, nil
}

func (r *resourceRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.Resource, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Resource{}, err
	}
	r.s.resourceMu.RLock()
	defer r.s.resourceMu.RUnlock()
	res, ok := r.s.data.Resources[tenant][id]
	if !ok {
		return cloud.Resource{}, core.NotFound("resource", id)
	}
	return deepCopy(res), nil
}

func (r *resourceRepo) GetByNativeID(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, native string) (cloud.Resource, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.Resource{}, err
	}
	r.s.resourceMu.RLock()
	defer r.s.resourceMu.RUnlock()
	for _, res := range r.s.data.Resources[tenant] {
		if res.AccountID == accountID && res.Region == region && res.NativeID == native {
			return deepCopy(res), nil
		}
	}
	return cloud.Resource{}, core.NotFound("resource", native)
}

// matchesResourceFilter evaluates every field of ports.ResourceFilter. An
// unset field (zero value, empty slice, empty string) is a wildcard, matching
// the convention every filter type in this package follows.
func matchesResourceFilter(res cloud.Resource, f ports.ResourceFilter) bool {
	if res.Deleted && !f.IncludeDeleted {
		return false
	}
	if len(f.AccountIDs) > 0 && !containsVal(f.AccountIDs, res.AccountID) {
		return false
	}
	if len(f.Regions) > 0 && !containsVal(f.Regions, res.Region) {
		return false
	}
	if len(f.Kinds) > 0 && !containsVal(f.Kinds, res.Kind) {
		return false
	}
	if len(f.Categories) > 0 && !containsVal(f.Categories, res.Kind.Category()) {
		return false
	}
	if len(f.Environments) > 0 && !containsVal(f.Environments, res.Environment) {
		return false
	}
	if !f.ApplicationID.IsZero() && res.ApplicationID != f.ApplicationID {
		return false
	}
	if !f.WorkloadID.IsZero() && res.WorkloadID != f.WorkloadID {
		return false
	}
	if len(f.States) > 0 && !containsVal(f.States, res.State) {
		return false
	}
	if f.TagKey != "" {
		v, ok := res.Tags.Get(f.TagKey)
		if !ok {
			return false
		}
		if f.TagValue != "" && !strings.EqualFold(v, f.TagValue) {
			return false
		}
	}
	if f.Search != "" {
		needle := strings.ToLower(f.Search)
		haystack := strings.ToLower(res.DisplayName() + " " + res.NativeID + " " + string(res.ARN) + " " + res.Owner)
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	if !f.MinMonthlyCost.IsZero() && res.MonthlyCost.LessThan(f.MinMonthlyCost) {
		return false
	}
	return true
}

func containsVal[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (r *resourceRepo) filtered(tenant core.TenantID, f ports.ResourceFilter) []cloud.Resource {
	r.s.resourceMu.RLock()
	defer r.s.resourceMu.RUnlock()
	out := make([]cloud.Resource, 0)
	for _, res := range r.s.data.Resources[tenant] {
		if matchesResourceFilter(res, f) {
			out = append(out, deepCopy(res))
		}
	}
	return out
}

func (r *resourceRepo) List(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter, opts ports.ListOptions) (ports.Page[cloud.Resource], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[cloud.Resource]{}, err
	}
	items := r.filtered(tenant, f)
	keyOf := func(res cloud.Resource) (string, string) {
		return res.LastSeenAt.Format(sortTimeLayout), res.ID.String()
	}
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *resourceRepo) LoadInventory(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (*cloud.Inventory, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	items := r.filtered(tenant, f)
	return cloud.NewInventory(items), nil
}

func (r *resourceRepo) MarkAbsent(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, kinds []cloud.Kind, seenKeys []string, at time.Time) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(seenKeys))
	for _, k := range seenKeys {
		seen[k] = true
	}
	kindSet := make(map[cloud.Kind]bool, len(kinds))
	for _, k := range kinds {
		kindSet[k] = true
	}

	r.s.resourceMu.Lock()
	defer r.s.resourceMu.Unlock()
	marked := 0
	for id, res := range r.s.data.Resources[tenant] {
		if res.Deleted || res.AccountID != accountID || res.Region != region || !kindSet[res.Kind] {
			continue
		}
		if seen[res.Key()] {
			continue
		}
		res.Deleted = true
		res.LastSeenAt = at
		r.s.data.Resources[tenant][id] = res
		marked++
	}
	return marked, nil
}

func (r *resourceRepo) Count(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (int, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return 0, err
	}
	return len(r.filtered(tenant, f)), nil
}

func (r *resourceRepo) ReplaceRelationships(ctx context.Context, tenant core.TenantID, accountID core.AccountID, region core.Region, edges []cloud.Relationship) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.resourceMu.Lock()
	defer r.s.resourceMu.Unlock()

	// Replace only the slice of edges this scan actually covers, scoped by
	// which endpoint resource belongs to the account/region pair — the same
	// partial-scan safety MarkAbsent applies to resources: a scan of one
	// account/region must never blow away edges another scan discovered.
	inScope := func(id core.ID) bool {
		res, ok := r.s.data.Resources[tenant][id]
		return ok && res.AccountID == accountID && res.Region == region
	}
	kept := r.s.data.Relationships[tenant][:0:0]
	for _, e := range r.s.data.Relationships[tenant] {
		if !inScope(e.FromID) && !inScope(e.ToID) {
			kept = append(kept, e)
		}
	}
	for _, e := range edges {
		if e.ID.IsZero() {
			e.ID = core.NewID("rel")
		}
		e.TenantID = tenant
		kept = append(kept, deepCopy(e))
	}
	r.s.data.Relationships[tenant] = kept
	return nil
}

func (r *resourceRepo) LoadTopology(ctx context.Context, tenant core.TenantID, f ports.ResourceFilter) (*cloud.Topology, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	// The filter narrows which resources are IN scope; edges are kept only
	// when both endpoints survive the filter, so a topology scoped to
	// production never shows an edge into a staging resource it excluded.
	items := r.filtered(tenant, f)
	inScope := make(map[core.ID]bool, len(items))
	for _, res := range items {
		inScope[res.ID] = true
	}
	r.s.resourceMu.RLock()
	edges := make([]cloud.Relationship, 0)
	for _, e := range r.s.data.Relationships[tenant] {
		if inScope[e.FromID] && inScope[e.ToID] {
			edges = append(edges, deepCopy(e))
		}
	}
	r.s.resourceMu.RUnlock()
	return cloud.NewTopology(edges), nil
}
