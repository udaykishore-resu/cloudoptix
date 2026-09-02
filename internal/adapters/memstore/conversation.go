package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// conversationRepo implements ports.ConversationRepository.
type conversationRepo struct{ s *Store }

func (r *conversationRepo) Create(ctx context.Context, c ports.Conversation) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	r.s.convMu.Lock()
	defer r.s.convMu.Unlock()
	if r.s.data.Conversations[c.TenantID] == nil {
		r.s.data.Conversations[c.TenantID] = map[core.ID]ports.Conversation{}
	}
	if _, exists := r.s.data.Conversations[c.TenantID][c.ID]; exists {
		return core.NewError(core.ErrAlreadyExists, "conversation_exists", "conversation %s already exists", c.ID)
	}
	r.s.data.Conversations[c.TenantID][c.ID] = deepCopy(c)
	return nil
}

func (r *conversationRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (ports.Conversation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Conversation{}, err
	}
	r.s.convMu.RLock()
	defer r.s.convMu.RUnlock()
	c, ok := r.s.data.Conversations[tenant][id]
	if !ok {
		return ports.Conversation{}, core.NotFound("conversation", id)
	}
	return deepCopy(c), nil
}

func (r *conversationRepo) AppendTurn(ctx context.Context, tenant core.TenantID, id core.ID, t ports.Turn) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.convMu.Lock()
	defer r.s.convMu.Unlock()
	c, ok := r.s.data.Conversations[tenant][id]
	if !ok {
		return core.NotFound("conversation", id)
	}
	if t.ID.IsZero() {
		t.ID = core.NewID("trn")
	}
	c.Turns = append(c.Turns, deepCopy(t))
	c.UpdatedAt = timeNowUTC()
	r.s.data.Conversations[tenant][id] = c
	return nil
}

func (r *conversationRepo) Update(ctx context.Context, c ports.Conversation) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	r.s.convMu.Lock()
	defer r.s.convMu.Unlock()
	if _, ok := r.s.data.Conversations[c.TenantID][c.ID]; !ok {
		return core.NotFound("conversation", c.ID)
	}
	c.UpdatedAt = timeNowUTC()
	r.s.data.Conversations[c.TenantID][c.ID] = deepCopy(c)
	return nil
}

func (r *conversationRepo) List(ctx context.Context, tenant core.TenantID, kind ports.ConversationKind, opts ports.ListOptions) (ports.Page[ports.Conversation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[ports.Conversation]{}, err
	}
	r.s.convMu.RLock()
	items := make([]ports.Conversation, 0)
	for _, c := range r.s.data.Conversations[tenant] {
		if kind == "" || c.Kind == kind {
			items = append(items, deepCopy(c))
		}
	}
	r.s.convMu.RUnlock()

	keyOf := func(c ports.Conversation) (string, string) { return c.CreatedAt.Format(sortTimeLayout), c.ID.String() }
	sortByCreatedThenID(items, keyOf)
	return paginate(items, opts, keyOf), nil
}
