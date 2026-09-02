package memstore

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// specRepo implements ports.SpecRepository.
type specRepo struct{ s *Store }

func (r *specRepo) SaveDraft(ctx context.Context, v spec.Version) error {
	if err := core.GuardTenant(ctx, v.TenantID); err != nil {
		return err
	}
	r.s.specMu.Lock()
	defer r.s.specMu.Unlock()

	if r.s.data.SpecVersions[v.TenantID] == nil {
		r.s.data.SpecVersions[v.TenantID] = map[core.ID]map[int]spec.Version{}
	}
	if r.s.data.SpecLatest[v.TenantID] == nil {
		r.s.data.SpecLatest[v.TenantID] = map[core.ID]int{}
	}
	if r.s.data.SpecVersions[v.TenantID][v.SpecID] == nil {
		r.s.data.SpecVersions[v.TenantID][v.SpecID] = map[int]spec.Version{}
	}
	if v.Version == 0 {
		v.Version = r.s.data.SpecLatest[v.TenantID][v.SpecID] + 1
	}
	r.s.data.SpecVersions[v.TenantID][v.SpecID][v.Version] = deepCopy(v)
	if v.Version > r.s.data.SpecLatest[v.TenantID][v.SpecID] {
		r.s.data.SpecLatest[v.TenantID][v.SpecID] = v.Version
	}
	return nil
}

func (r *specRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return spec.Version{}, err
	}
	r.s.specMu.RLock()
	defer r.s.specMu.RUnlock()
	for _, versions := range r.s.data.SpecVersions[tenant] {
		for _, v := range versions {
			if v.ID == id {
				return deepCopy(v), nil
			}
		}
	}
	return spec.Version{}, core.NotFound("spec_version", id)
}

func (r *specRepo) GetVersion(ctx context.Context, tenant core.TenantID, specID core.ID, version int) (spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return spec.Version{}, err
	}
	r.s.specMu.RLock()
	defer r.s.specMu.RUnlock()
	v, ok := r.s.data.SpecVersions[tenant][specID][version]
	if !ok {
		return spec.Version{}, core.NotFound("spec_version", specID)
	}
	return deepCopy(v), nil
}

func (r *specRepo) GetActive(ctx context.Context, tenant core.TenantID) (spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return spec.Version{}, err
	}
	r.s.specMu.RLock()
	defer r.s.specMu.RUnlock()
	for _, versions := range r.s.data.SpecVersions[tenant] {
		for _, v := range versions {
			if v.Status == spec.StatusApproved {
				return deepCopy(v), nil
			}
		}
	}
	return spec.Version{}, core.NotFound("active_spec", tenant)
}

func (r *specRepo) GetLatest(ctx context.Context, tenant core.TenantID, specID core.ID) (spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return spec.Version{}, err
	}
	r.s.specMu.RLock()
	defer r.s.specMu.RUnlock()
	latest, ok := r.s.data.SpecLatest[tenant][specID]
	if !ok {
		return spec.Version{}, core.NotFound("spec", specID)
	}
	return deepCopy(r.s.data.SpecVersions[tenant][specID][latest]), nil
}

func (r *specRepo) ListVersions(ctx context.Context, tenant core.TenantID, specID core.ID) ([]spec.Version, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.specMu.RLock()
	defer r.s.specMu.RUnlock()
	versions := r.s.data.SpecVersions[tenant][specID]
	out := make([]spec.Version, 0, len(versions))
	for _, v := range versions {
		out = append(out, deepCopy(v))
	}
	sortByCreatedThenID(out, func(v spec.Version) (string, string) {
		// Ascending by version number, which is what a review history reads
		// naturally as; encode with fixed width so lexical and numeric order
		// agree.
		return fmtInt(v.Version), v.ID.String()
	})
	// sortByCreatedThenID sorts descending; reverse for ascending version order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Approve implements the atomic freeze-and-supersede described on
// ports.SpecRepository: it builds the whole next generation of the specId's
// version map in a private scratch copy first, and only replaces the live
// map with it as the single final assignment. Every reader either sees the
// old generation (no active version demoted yet) or the fully-updated one
// (previous active superseded AND the new version approved); there is no
// window in which two versions are simultaneously active, and a panic before
// the final line leaves the live map completely untouched.
func (r *specRepo) Approve(ctx context.Context, tenant core.TenantID, v spec.Version) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.specMu.Lock()
	defer r.s.specMu.Unlock()

	existing := r.s.data.SpecVersions[tenant][v.SpecID]
	if existing == nil {
		return core.NotFound("spec", v.SpecID)
	}
	next := make(map[int]spec.Version, len(existing))
	for ver, val := range existing {
		next[ver] = val
	}
	for ver, val := range next {
		if val.Status == spec.StatusApproved && ver != v.Version {
			val.Status = spec.StatusSuperseded
			next[ver] = val
		}
	}
	approved := deepCopy(v)
	approved.Status = spec.StatusApproved
	next[v.Version] = approved

	r.s.data.SpecVersions[tenant][v.SpecID] = next
	if v.Version > r.s.data.SpecLatest[tenant][v.SpecID] {
		r.s.data.SpecLatest[tenant][v.SpecID] = v.Version
	}
	return nil
}

func (r *specRepo) Reject(ctx context.Context, tenant core.TenantID, id core.ID, reason, by string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.specMu.Lock()
	defer r.s.specMu.Unlock()

	for specID, versions := range r.s.data.SpecVersions[tenant] {
		for ver, v := range versions {
			if v.ID == id {
				v.Status = spec.StatusRejected
				v.RejectedReason = reason
				v.ApprovedBy = by
				r.s.data.SpecVersions[tenant][specID][ver] = v
				return nil
			}
		}
	}
	return core.NotFound("spec_version", id)
}

// fmtInt zero-pads to 6 digits: specification versions never approach that
// count, and a fixed width means lexical sort matches numeric sort.
func fmtInt(n int) string { return fmt.Sprintf("%06d", n) }
