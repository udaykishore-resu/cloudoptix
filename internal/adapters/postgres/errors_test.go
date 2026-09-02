package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestMapErrNil(t *testing.T) {
	if got := mapErr(nil); got != nil {
		t.Fatalf("mapErr(nil) = %v, want nil", got)
	}
}

func TestMapErrNoRows(t *testing.T) {
	got := mapErr(pgx.ErrNoRows)
	if !errors.Is(got, core.ErrNotFound) {
		t.Fatalf("mapErr(pgx.ErrNoRows) = %v, want core.ErrNotFound", got)
	}
}

func TestMapErrPgError(t *testing.T) {
	cases := []struct {
		name string
		code string
		want error
	}{
		{"unique_violation", sqlStateUniqueViolation, core.ErrAlreadyExists},
		{"foreign_key_violation", sqlStateForeignKeyViolation, core.ErrInvalidInput},
		{"check_violation", sqlStateCheckViolation, core.ErrInvalidInput},
		{"not_null_violation", sqlStateNotNullViolation, core.ErrInvalidInput},
		{"invalid_text_repr", sqlStateInvalidTextRepr, core.ErrInvalidInput},
		{"serialization_failure", sqlStateSerializationFail, core.ErrConflict},
		{"deadlock_detected", sqlStateDeadlockDetected, core.ErrConflict},
		{"lock_not_available", sqlStateLockNotAvailable, core.ErrConflict},
		{"insufficient_privilege_rls", sqlStateInsufficientPriv, core.ErrTenantMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pgErr := &pgconn.PgError{Code: tc.code, Message: "boom", ConstraintName: "some_constraint"}
			got := mapErr(pgErr)
			if !errors.Is(got, tc.want) {
				t.Fatalf("mapErr(code=%s) = %v, want wrapping %v", tc.code, got, tc.want)
			}
			// The underlying pgconn.PgError must still be reachable via
			// errors.As, so callers that need SQLSTATE-level detail (metrics,
			// logging) are not cut off by the sentinel mapping.
			var unwrapped *pgconn.PgError
			if !errors.As(got, &unwrapped) {
				t.Fatalf("mapErr(code=%s) lost the underlying *pgconn.PgError", tc.code)
			}
		})
	}
}

func TestMapErrUnknownCodePassthrough(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "99999", Message: "something else"}
	got := mapErr(pgErr)
	// An unmapped SQLSTATE is returned as-is (wrapped only by pgx itself),
	// not silently swallowed into a generic sentinel that would hide a
	// class of failure this package has not seen yet.
	if !errors.Is(got, pgErr) {
		t.Fatalf("mapErr(unknown code) = %v, want passthrough of %v", got, pgErr)
	}
}

func TestMapErrPlainErrorPassthrough(t *testing.T) {
	plain := errors.New("network reset")
	got := mapErr(plain)
	if got != plain {
		t.Fatalf("mapErr(plain error) = %v, want unchanged %v", got, plain)
	}
}
