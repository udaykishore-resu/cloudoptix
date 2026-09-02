package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// notificationRepo implements ports.NotificationRepository.
type notificationRepo struct{ s *Store }

func (r *notificationRepo) Enqueue(ctx context.Context, n ports.Notification) error {
	if err := core.GuardTenant(ctx, n.TenantID); err != nil {
		return err
	}
	r.s.notifMu.Lock()
	defer r.s.notifMu.Unlock()
	if r.s.data.Notifications[n.TenantID] == nil {
		r.s.data.Notifications[n.TenantID] = map[core.ID]ports.Notification{}
	}
	if n.ID.IsZero() {
		n.ID = core.NewID("ntf")
	}
	r.s.data.Notifications[n.TenantID][n.ID] = deepCopy(n)
	return nil
}

// ClaimPending has no tenant argument for the same reason
// ExecutionRepository.ClaimDuePlans does not: it is a background delivery
// worker sweeping the whole outbound queue across every tenant. The
// exclusive notifMu.Lock held for the whole select-then-mutate body gives the
// same single-claim guarantee described there.
func (r *notificationRepo) ClaimPending(ctx context.Context, workerID string, limit int) ([]ports.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	r.s.notifMu.Lock()
	defer r.s.notifMu.Unlock()

	type candidate struct {
		tenant core.TenantID
		id     core.ID
	}
	var candidates []candidate
	for tenant, notifs := range r.s.data.Notifications {
		for id, n := range notifs {
			if n.SentAt == nil && n.Error == "" {
				candidates = append(candidates, candidate{tenant: tenant, id: id})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		ni := r.s.data.Notifications[candidates[i].tenant][candidates[i].id]
		nj := r.s.data.Notifications[candidates[j].tenant][candidates[j].id]
		if !ni.CreatedAt.Equal(nj.CreatedAt) {
			return ni.CreatedAt.Before(nj.CreatedAt)
		}
		return candidates[i].id < candidates[j].id
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	claimed := make([]ports.Notification, 0, len(candidates))
	for _, c := range candidates {
		n := r.s.data.Notifications[c.tenant][c.id]
		n.Attempts++
		_ = workerID
		r.s.data.Notifications[c.tenant][c.id] = n
		claimed = append(claimed, deepCopy(n))
	}
	return claimed, nil
}

func (r *notificationRepo) MarkSent(ctx context.Context, tenant core.TenantID, id core.ID, at time.Time) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.notifMu.Lock()
	defer r.s.notifMu.Unlock()
	n, ok := r.s.data.Notifications[tenant][id]
	if !ok {
		return core.NotFound("notification", id)
	}
	t := at
	n.SentAt = &t
	n.Error = ""
	r.s.data.Notifications[tenant][id] = n
	return nil
}

func (r *notificationRepo) MarkFailed(ctx context.Context, tenant core.TenantID, id core.ID, errMsg string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.notifMu.Lock()
	defer r.s.notifMu.Unlock()
	n, ok := r.s.data.Notifications[tenant][id]
	if !ok {
		return core.NotFound("notification", id)
	}
	n.Error = errMsg
	r.s.data.Notifications[tenant][id] = n
	return nil
}

func (r *notificationRepo) List(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[ports.Notification], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[ports.Notification]{}, err
	}
	r.s.notifMu.RLock()
	items := make([]ports.Notification, 0, len(r.s.data.Notifications[tenant]))
	for _, n := range r.s.data.Notifications[tenant] {
		items = append(items, deepCopy(n))
	}
	r.s.notifMu.RUnlock()

	keyOf := func(n ports.Notification) (string, string) { return n.CreatedAt.Format(sortTimeLayout), n.ID.String() }
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}
