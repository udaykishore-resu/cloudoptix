package memstore

import (
	"context"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// tenantRepo implements ports.TenantRepository.
type tenantRepo struct{ s *Store }

func (r *tenantRepo) Create(ctx context.Context, t tenancy.Tenant) error {
	if err := core.GuardTenant(ctx, t.ID); err != nil {
		return err
	}
	r.s.tenantMu.Lock()
	defer r.s.tenantMu.Unlock()

	if _, exists := r.s.data.Tenants[t.ID]; exists {
		return core.NewError(core.ErrAlreadyExists, "tenant_exists", "tenant %s already exists", t.ID)
	}
	if owner, exists := r.s.data.TenantSlugs[t.Slug]; exists && owner != t.ID {
		return core.NewError(core.ErrAlreadyExists, "slug_taken", "tenant slug %q is already in use", t.Slug)
	}
	r.s.data.Tenants[t.ID] = deepCopy(t)
	if t.Slug != "" {
		r.s.data.TenantSlugs[t.Slug] = t.ID
	}
	return nil
}

func (r *tenantRepo) Get(ctx context.Context, id core.TenantID) (tenancy.Tenant, error) {
	if err := core.GuardTenant(ctx, id); err != nil {
		return tenancy.Tenant{}, err
	}
	r.s.tenantMu.RLock()
	defer r.s.tenantMu.RUnlock()
	t, ok := r.s.data.Tenants[id]
	if !ok {
		return tenancy.Tenant{}, core.NotFound("tenant", id)
	}
	return deepCopy(t), nil
}

func (r *tenantRepo) GetBySlug(ctx context.Context, slug string) (tenancy.Tenant, error) {
	r.s.tenantMu.RLock()
	id, ok := r.s.data.TenantSlugs[slug]
	if !ok {
		r.s.tenantMu.RUnlock()
		return tenancy.Tenant{}, core.NotFound("tenant", slug)
	}
	t := r.s.data.Tenants[id]
	r.s.tenantMu.RUnlock()

	if err := core.GuardTenant(ctx, t.ID); err != nil {
		return tenancy.Tenant{}, err
	}
	return deepCopy(t), nil
}

func (r *tenantRepo) Update(ctx context.Context, t tenancy.Tenant) error {
	if err := core.GuardTenant(ctx, t.ID); err != nil {
		return err
	}
	r.s.tenantMu.Lock()
	defer r.s.tenantMu.Unlock()

	existing, ok := r.s.data.Tenants[t.ID]
	if !ok {
		return core.NotFound("tenant", t.ID)
	}
	if existing.Slug != t.Slug {
		if owner, exists := r.s.data.TenantSlugs[t.Slug]; exists && owner != t.ID {
			return core.NewError(core.ErrAlreadyExists, "slug_taken", "tenant slug %q is already in use", t.Slug)
		}
		delete(r.s.data.TenantSlugs, existing.Slug)
		r.s.data.TenantSlugs[t.Slug] = t.ID
	}
	r.s.data.Tenants[t.ID] = deepCopy(t)
	return nil
}

func (r *tenantRepo) List(ctx context.Context, opts ports.ListOptions) (ports.Page[tenancy.Tenant], error) {
	p, ok := core.PrincipalFrom(ctx)
	if !ok {
		return ports.Page[tenancy.Tenant]{}, core.NewError(core.ErrUnauthenticated, "no_principal", "request has no authenticated principal")
	}
	if !p.HasRole(core.RolePlatformAdmin) {
		return ports.Page[tenancy.Tenant]{}, core.Forbidden("listing every tenant requires a platform admin")
	}
	r.s.tenantMu.RLock()
	items := make([]tenancy.Tenant, 0, len(r.s.data.Tenants))
	for _, t := range r.s.data.Tenants {
		items = append(items, deepCopy(t))
	}
	r.s.tenantMu.RUnlock()

	sortByCreatedThenID(items, func(t tenancy.Tenant) (string, string) {
		return t.CreatedAt.Format(sortTimeLayout), t.ID.String()
	})
	page := paginate(items, opts, func(t tenancy.Tenant) (string, string) {
		return t.CreatedAt.Format(sortTimeLayout), t.ID.String()
	})
	return page, nil
}

func (r *tenantRepo) CreateOrganization(ctx context.Context, o tenancy.Organization) error {
	if err := core.GuardTenant(ctx, o.TenantID); err != nil {
		return err
	}
	r.s.tenantMu.Lock()
	defer r.s.tenantMu.Unlock()
	if r.s.data.Orgs[o.TenantID] == nil {
		r.s.data.Orgs[o.TenantID] = map[core.ID]tenancy.Organization{}
	}
	r.s.data.Orgs[o.TenantID][o.ID] = deepCopy(o)
	return nil
}

func (r *tenantRepo) ListOrganizations(ctx context.Context, tenant core.TenantID) ([]tenancy.Organization, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.tenantMu.RLock()
	defer r.s.tenantMu.RUnlock()
	out := make([]tenancy.Organization, 0, len(r.s.data.Orgs[tenant]))
	for _, o := range r.s.data.Orgs[tenant] {
		out = append(out, deepCopy(o))
	}
	return out, nil
}

// userRepo implements ports.UserRepository.
//
// Users are deliberately not tenant-scoped objects: package tenancy's own doc
// comment says a consultant working three customers has one identity and
// three Memberships. So unlike every other repository here, Upsert and the
// two identity lookups do not call core.GuardTenant — there is no single
// tenant to guard against. Isolation still applies where it matters:
// ListByTenant, AddMembership and RemoveMembership all guard the tenant they
// are scoped to, and ListByTenant only ever returns a user's membership for
// the requested tenant, never their memberships elsewhere.
type userRepo struct{ s *Store }

func (r *userRepo) Upsert(ctx context.Context, u tenancy.User) error {
	if _, ok := core.PrincipalFrom(ctx); !ok {
		return core.NewError(core.ErrUnauthenticated, "no_principal", "request has no authenticated principal")
	}
	r.s.userMu.Lock()
	defer r.s.userMu.Unlock()

	if existing, ok := r.s.data.Users[u.ID]; ok {
		delete(r.s.data.UsersBySubject, existing.Subject)
		delete(r.s.data.UsersByEmail, existing.Email)
	}
	r.s.data.Users[u.ID] = deepCopy(u)
	if u.Subject != "" {
		r.s.data.UsersBySubject[u.Subject] = u.ID
	}
	if u.Email != "" {
		r.s.data.UsersByEmail[u.Email] = u.ID
	}
	return nil
}

func (r *userRepo) GetBySubject(ctx context.Context, subject string) (tenancy.User, error) {
	r.s.userMu.RLock()
	defer r.s.userMu.RUnlock()
	id, ok := r.s.data.UsersBySubject[subject]
	if !ok {
		return tenancy.User{}, core.NotFound("user", subject)
	}
	return deepCopy(r.s.data.Users[id]), nil
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (tenancy.User, error) {
	r.s.userMu.RLock()
	defer r.s.userMu.RUnlock()
	id, ok := r.s.data.UsersByEmail[email]
	if !ok {
		return tenancy.User{}, core.NotFound("user", email)
	}
	return deepCopy(r.s.data.Users[id]), nil
}

func (r *userRepo) ListByTenant(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[tenancy.User], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[tenancy.User]{}, err
	}
	r.s.userMu.RLock()
	items := make([]tenancy.User, 0)
	for _, u := range r.s.data.Users {
		for _, m := range u.Memberships {
			if m.TenantID == tenant {
				items = append(items, deepCopy(u))
				break
			}
		}
	}
	r.s.userMu.RUnlock()

	keyOf := func(u tenancy.User) (string, string) { return u.CreatedAt.Format(sortTimeLayout), u.ID.String() }
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}

func (r *userRepo) AddMembership(ctx context.Context, userID core.ID, m tenancy.Membership) error {
	if err := core.GuardTenant(ctx, m.TenantID); err != nil {
		return err
	}
	r.s.userMu.Lock()
	defer r.s.userMu.Unlock()

	u, ok := r.s.data.Users[userID]
	if !ok {
		return core.NotFound("user", userID)
	}
	replaced := false
	for i, existing := range u.Memberships {
		if existing.TenantID == m.TenantID {
			u.Memberships[i] = m
			replaced = true
			break
		}
	}
	if !replaced {
		u.Memberships = append(u.Memberships, m)
	}
	r.s.data.Users[userID] = deepCopy(u)
	return nil
}

func (r *userRepo) RemoveMembership(ctx context.Context, userID core.ID, tenant core.TenantID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.userMu.Lock()
	defer r.s.userMu.Unlock()

	u, ok := r.s.data.Users[userID]
	if !ok {
		return core.NotFound("user", userID)
	}
	out := u.Memberships[:0:0]
	for _, m := range u.Memberships {
		if m.TenantID != tenant {
			out = append(out, m)
		}
	}
	u.Memberships = out
	r.s.data.Users[userID] = deepCopy(u)
	return nil
}

// sortTimeLayout is used everywhere a time.Time is folded into a lexically
// sortable string sort key: RFC3339Nano sorts identically to chronological
// order because it is fixed-width per field and zero-padded.
const sortTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// sortByCreatedThenID performs the stable sort every List method in this
// package uses: primary key descending (newest first, matching every other
// CloudOptix list endpoint), secondary key (id) ascending to make the order
// deterministic when two rows share a primary key — which happens constantly
// in tests that create fixtures in a tight loop faster than the clock ticks.
func sortByCreatedThenID[T any](items []T, keyOf func(T) (string, string)) {
	sort.SliceStable(items, func(i, j int) bool {
		ak, aid := keyOf(items[i])
		bk, bid := keyOf(items[j])
		if ak != bk {
			return ak > bk // descending: newest first
		}
		return aid < bid
	})
}
