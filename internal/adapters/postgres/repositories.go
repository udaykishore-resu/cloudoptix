package postgres

import (
	"context"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// New assembles the full ports.Repositories set over one *DB — the single
// constructor the application layer calls to get a working, pgx-backed
// implementation of every repository interface. Every individual
// NewXRepository constructor is still exported (and used directly by tests
// that only need one repository), but production wiring should call this
// one function so a new repository added here is never accidentally left
// out of the bundle the application layer actually receives.
func New(db *DB) ports.Repositories {
	return ports.Repositories{
		Tenants:         NewTenantRepository(db),
		Users:           NewUserRepository(db),
		Specs:           NewSpecRepository(db),
		AWSAccounts:     NewAWSAccountRepository(db),
		Resources:       NewResourceRepository(db),
		Applications:    NewApplicationRepository(db),
		Costs:           NewCostRepository(db),
		Metrics:         NewMetricRepository(db),
		Recommendations: NewRecommendationRepository(db),
		Policies:        NewPolicyRepository(db),
		Approvals:       NewApprovalRepository(db),
		Executions:      NewExecutionRepository(db),
		Savings:         NewSavingsRepository(db),
		Economics:       NewEconomicsRepository(db),
		Simulations:     NewSimulationRepository(db),
		Audit:           NewAuditRepository(db),
		DiscoveryRuns:   NewDiscoveryRunRepository(db),
		Conversations:   NewConversationRepository(db),
		Notifications:   NewNotificationRepository(db),
	}
}

// unitOfWork is the pgx-backed ports.UnitOfWork: it opens one transaction,
// puts it in context so every repository method's db.querier(ctx) call
// picks it up instead of the bare pool (see db.go's querier and
// WithTenant/WithSystemScope, which reuse a context-carried transaction
// rather than opening a second one), and commits or rolls back around fn.
//
// The repository set handed to fn is the SAME set New(db) builds — not a
// second, "transactional" implementation — because every repository method
// already defers to db.querier(ctx) for its actual statements; the
// transaction-vs-pool decision lives entirely in db.go, not in any
// individual repository type. That is what makes one UnitOfWork.Do body
// able to call, say, repos.Specs.Approve and then repos.Tenants.Create and
// have both participate in the same transaction with no special-casing in
// either method.
type unitOfWork struct {
	db    *DB
	repos ports.Repositories
}

// NewUnitOfWork builds a ports.UnitOfWork over db, bundling the same
// repository set New(db) would.
func NewUnitOfWork(db *DB) ports.UnitOfWork {
	return &unitOfWork{db: db, repos: New(db)}
}

var _ ports.UnitOfWork = (*unitOfWork)(nil)

// Do runs fn inside one transaction. AuditRepository.Append is the sole
// exception to "every repository call inside fn joins this transaction": it
// deliberately always opens its own transaction (see audit.go's comment on
// Append) because its advisory-lock discipline requires exclusive control
// of exactly when that transaction begins and ends. A caller that appends
// an audit record inside a UnitOfWork.Do body still gets a correctly
// chained, durable record — it simply commits independently of the rest of
// fn, which is the documented trade-off on ports.AuditRepository.Append,
// not a bug introduced here.
func (u *unitOfWork) Do(ctx context.Context, fn func(ctx context.Context, repos ports.Repositories) error) error {
	if _, ok := txFromContext(ctx); ok {
		// Already inside a transaction — a nested UnitOfWork.Do, or a
		// UnitOfWork.Do called from within a WithTenant/WithSystemScope
		// callback. Postgres has no nested transactions, so the existing one
		// is reused rather than opening (and later committing/rolling back)
		// a second one that would only ever see fn's writes at the wrong
		// boundary.
		return fn(ctx, u.repos)
	}
	tx, err := u.db.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(withTxContext(ctx, tx), u.repos); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapErr(err)
	}
	committed = true
	return nil
}
