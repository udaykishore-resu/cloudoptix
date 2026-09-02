package memstore

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// awsAccountRepo implements ports.AWSAccountRepository.
type awsAccountRepo struct{ s *Store }

func (r *awsAccountRepo) Create(ctx context.Context, a cloud.AWSAccount) error {
	if err := core.GuardTenant(ctx, a.TenantID); err != nil {
		return err
	}
	r.s.awsMu.Lock()
	defer r.s.awsMu.Unlock()
	if r.s.data.AWSAccounts[a.TenantID] == nil {
		r.s.data.AWSAccounts[a.TenantID] = map[core.ID]cloud.AWSAccount{}
	}
	if _, exists := r.s.data.AWSAccounts[a.TenantID][a.ID]; exists {
		return core.NewError(core.ErrAlreadyExists, "account_exists", "aws account %s already registered", a.ID)
	}
	r.s.data.AWSAccounts[a.TenantID][a.ID] = deepCopy(a)
	return nil
}

func (r *awsAccountRepo) Get(ctx context.Context, tenant core.TenantID, id core.ID) (cloud.AWSAccount, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.AWSAccount{}, err
	}
	r.s.awsMu.RLock()
	defer r.s.awsMu.RUnlock()
	a, ok := r.s.data.AWSAccounts[tenant][id]
	if !ok {
		return cloud.AWSAccount{}, core.NotFound("aws_account", id)
	}
	return deepCopy(a), nil
}

func (r *awsAccountRepo) GetByAccountID(ctx context.Context, tenant core.TenantID, accountID core.AccountID) (cloud.AWSAccount, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return cloud.AWSAccount{}, err
	}
	r.s.awsMu.RLock()
	defer r.s.awsMu.RUnlock()
	for _, a := range r.s.data.AWSAccounts[tenant] {
		if a.AccountID == accountID {
			return deepCopy(a), nil
		}
	}
	return cloud.AWSAccount{}, core.NotFound("aws_account", accountID)
}

func (r *awsAccountRepo) Update(ctx context.Context, a cloud.AWSAccount) error {
	if err := core.GuardTenant(ctx, a.TenantID); err != nil {
		return err
	}
	r.s.awsMu.Lock()
	defer r.s.awsMu.Unlock()
	if _, ok := r.s.data.AWSAccounts[a.TenantID][a.ID]; !ok {
		return core.NotFound("aws_account", a.ID)
	}
	r.s.data.AWSAccounts[a.TenantID][a.ID] = deepCopy(a)
	return nil
}

func (r *awsAccountRepo) List(ctx context.Context, tenant core.TenantID) ([]cloud.AWSAccount, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return nil, err
	}
	r.s.awsMu.RLock()
	defer r.s.awsMu.RUnlock()
	out := make([]cloud.AWSAccount, 0, len(r.s.data.AWSAccounts[tenant]))
	for _, a := range r.s.data.AWSAccounts[tenant] {
		out = append(out, deepCopy(a))
	}
	sortByCreatedThenID(out, func(a cloud.AWSAccount) (string, string) {
		return a.CreatedAt.Format(sortTimeLayout), a.ID.String()
	})
	return out, nil
}

func (r *awsAccountRepo) Delete(ctx context.Context, tenant core.TenantID, id core.ID) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	r.s.awsMu.Lock()
	defer r.s.awsMu.Unlock()
	if _, ok := r.s.data.AWSAccounts[tenant][id]; !ok {
		return core.NotFound("aws_account", id)
	}
	delete(r.s.data.AWSAccounts[tenant], id)
	return nil
}
