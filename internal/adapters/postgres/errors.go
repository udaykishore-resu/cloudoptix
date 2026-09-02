package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Postgres SQLSTATE codes this package cares about. Named here rather than
// scattered as string literals so mapErr is the one place that has to be
// right, and the one place a new code gets added.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateNotNullViolation    = "23502"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlockDetected    = "40P01"
	sqlStateInsufficientPriv    = "42501" // RLS policy rejection
	sqlStateInvalidTextRepr     = "22P02"
	sqlStateLockNotAvailable    = "55P03"
)

// mapErr translates a pgx/pgconn error into one of the core.Err* sentinels
// every layer above this package checks with errors.Is. A nil input maps to
// nil so mapErr(err) is always safe to return directly.
//
// The mapping exists because the application and HTTP layers must not
// import pgx — core.HTTPStatus and core.Retryable already know how to turn
// a core.Err* sentinel into a status code and a retry decision, and this is
// the only place that bridges Postgres's error vocabulary into that one.
// isNoRows reports whether err is exactly "no matching row", for the rare
// call site (loadRollback) that needs to distinguish "does not exist yet"
// from every other error and return (nil, nil) rather than propagate a
// NotFound — a plan legitimately has no rollback plan until one is
// constructed, and that is not an error condition its caller (GetPlan)
// should have to unwrap.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return core.NewError(core.ErrNotFound, "not_found", "no matching row").Wrap(err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateUniqueViolation:
			return core.NewError(core.ErrAlreadyExists, "already_exists", "%s", pgErr.Message).
				WithDetail("constraint", pgErr.ConstraintName).Wrap(err)
		case sqlStateForeignKeyViolation, sqlStateCheckViolation, sqlStateNotNullViolation, sqlStateInvalidTextRepr:
			return core.NewError(core.ErrInvalidInput, "invalid_input", "%s", pgErr.Message).
				WithDetail("constraint", pgErr.ConstraintName).Wrap(err)
		case sqlStateSerializationFail, sqlStateDeadlockDetected, sqlStateLockNotAvailable:
			return core.NewError(core.ErrConflict, "conflict", "%s", pgErr.Message).Wrap(err)
		case sqlStateInsufficientPriv:
			// A row-level-security policy rejected the statement. Every
			// repository method calls core.GuardTenant before this ever runs,
			// so reaching this branch means the primary guard already caught
			// the mismatch — RLS is confirming it, not discovering it fresh —
			// but the caller-facing error is still ErrTenantMismatch: the
			// caller asked for another tenant's data or tried to write into
			// it, and the platform's answer to that is always "not found",
			// never "forbidden" (see core.HTTPStatus's comment on why).
			return core.NewError(core.ErrTenantMismatch, "tenant_mismatch", "%s", pgErr.Message).Wrap(err)
		}
	}
	return err
}
