package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// systemScope is the sentinel tenant-scope value the four legitimately
// cross-tenant repository methods use (ExecutionRepository.ClaimDuePlans,
// ClaimPlansAwaitingValidation, ApprovalRepository.ExpireOverdue,
// NotificationRepository.ClaimPending) plus tenant bootstrap
// (TenantRepository.Create/List). See migrations/0001_foundation.up.sql's
// comment on cloudoptix_system_scope() for the full rationale: it is a
// value core.TenantID.Validate() can never accept (it starts with an
// underscore), so a session that never explicitly asks for system scope can
// never land in it by omission — an unscoped session sees nothing, not
// everything.
const systemScope = "__cloudoptix_system__"

// Querier is the subset of *pgxpool.Pool and pgx.Tx every repository method
// in this package needs. Writing repository code against this interface
// rather than concretely against *pgxpool.Pool is what lets a method run
// unchanged whether it opened its own transaction or is participating in one
// a UnitOfWork already started.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB wraps a pgxpool.Pool with the tenant-scoping and health-check machinery
// every repository in this package builds on.
type DB struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Config bounds the connection pool. Zero values fall back to sane
// production defaults rather than pgxpool's own defaults, which are tuned
// for a single long-lived process, not one API pod among many sharing a
// database.
type Config struct {
	DSN               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// Open establishes the connection pool. It does not run migrations — that
// is Migrator's job (migrate.go) — because a read replica or a short-lived
// worker process should be able to open a DB without ever attempting a
// schema change.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	pgCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pgCfg.MaxConns = cfg.MaxConns
	} else {
		pgCfg.MaxConns = 20
	}
	if cfg.MinConns > 0 {
		pgCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pgCfg.MaxConnLifetime = cfg.MaxConnLifetime
	} else {
		pgCfg.MaxConnLifetime = time.Hour
	}
	if cfg.MaxConnIdleTime > 0 {
		pgCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	} else {
		pgCfg.MaxConnIdleTime = 30 * time.Minute
	}
	if cfg.HealthCheckPeriod > 0 {
		pgCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	}

	pool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	return &DB{pool: pool, log: log}, nil
}

// Close releases the pool. Safe to call once at process shutdown.
func (d *DB) Close() { d.pool.Close() }

// Pool exposes the underlying pool for the migration runner and for
// operational tooling; repository code should not need it directly.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// HealthCheck reports whether the database is reachable, for the platform's
// /healthz endpoint and for readiness probes.
func (d *DB) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := d.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: health check: %w", err)
	}
	return nil
}

type txCtxKey struct{}

func txFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx)
	return tx, ok
}

func withTxContext(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// querier returns whichever Querier the current call should use: the
// transaction a WithTenant/WithSystemScope/UnitOfWork.Do call already put in
// context, or the bare pool as a last resort (used only by the migration
// runner and tests that talk to the database outside any tenant scope).
func (d *DB) querier(ctx context.Context) Querier {
	if tx, ok := txFromContext(ctx); ok {
		return tx
	}
	return d.pool
}

// WithTenant is the mechanism REQUIRED by every tenant-scoped repository
// method: it sets the RLS session variable cloudoptix.tenant_id for the
// exact connection the enclosed queries run on, then runs fn.
//
// It is callback-shaped rather than "return a scoped context" because
// set_config(..., true) (the `true` means "local to this transaction") only
// has an effect for statements that run on the *same physical connection and
// transaction* it was set on — and pgxpool hands out a different connection
// per pool.Query call unless one is explicitly held open across a
// transaction. A context-only API cannot make that guarantee; the caller
// could trivially issue a query against the pool directly and silently lose
// tenant scoping. Wrapping the queries in the callback is what makes "every
// query fn issues runs inside the scoped transaction" true by construction
// rather than by discipline.
//
// If ctx already carries a transaction — because this call is nested inside
// a UnitOfWork.Do, or inside another WithTenant/WithSystemScope call — that
// transaction is reused rather than a new one opened (Postgres does not
// support nested transactions), and the tenant scope is re-asserted on it.
// Re-asserting on every call, even a nested one, is what lets one UnitOfWork
// transaction legitimately touch two different tenants in sequence (tenant
// bootstrap plus the first spec write, for example) without either write
// silently running under the wrong scope.
func (d *DB) WithTenant(ctx context.Context, tenant core.TenantID, fn func(ctx context.Context) error) error {
	if err := tenant.Validate(); err != nil {
		return core.Invalid("postgres: %v", err)
	}
	return d.runScoped(ctx, string(tenant), fn)
}

// WithSystemScope runs fn under the system-scope sentinel described at the
// top of this file. Use it ONLY for the handful of repository methods that
// are cross-tenant by design (see the sentinel's doc comment) — every other
// method must call WithTenant.
func (d *DB) WithSystemScope(ctx context.Context, fn func(ctx context.Context) error) error {
	return d.runScoped(ctx, systemScope, fn)
}

func (d *DB) runScoped(ctx context.Context, scope string, fn func(ctx context.Context) error) error {
	if tx, ok := txFromContext(ctx); ok {
		if err := setScope(ctx, tx, scope); err != nil {
			return mapErr(err)
		}
		return fn(ctx)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := setScope(ctx, tx, scope); err != nil {
		return mapErr(err)
	}
	if err := fn(withTxContext(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapErr(err)
	}
	committed = true
	return nil
}

// setScope issues set_config rather than a textual `SET LOCAL
// cloudoptix.tenant_id = <value>`, because set_config takes its value as a
// bound parameter — a tenant slug or id can never be interpreted as SQL,
// where string-formatting a SET statement would require careful quoting to
// achieve the same.
func setScope(ctx context.Context, tx pgx.Tx, scope string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('cloudoptix.tenant_id', $1, true)`, scope)
	return err
}

// advisoryLock takes a session-scoped Postgres advisory lock for the
// duration of fn, keyed on an arbitrary string (hashed to the bigint
// pg_advisory_xact_lock wants). It is what serialises the two operations in
// this codebase that must never race across processes: the migration runner
// (one key per database) and AuditRepository.Append (one key per tenant).
// The transaction-scoped form (xact_lock) is used deliberately over the
// session form: the lock releases automatically at commit or rollback, so a
// crashed worker can never leave a lock held forever.
func advisoryLock(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return err
}
