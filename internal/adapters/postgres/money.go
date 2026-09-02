// Package postgres is the pgx-backed implementation of every repository
// interface in internal/ports.
//
// The design decision that shapes every file here: nothing under
// internal/domain or internal/ports may import this package (that is the
// whole point of the hexagonal boundary — see internal/ports/repositories.go),
// which means this package cannot add `db:"..."` struct tags to a domain
// type, and pgx's reflection-based row scanners (RowToStructByName) rely on
// exactly those tags. So every repository here scans into small unexported
// row structs with explicit column-ordered fields, then converts to and from
// the domain type by hand in a toXxx/fromXxx pair. It is more code per table
// than a generic scanner would need, but it is code that fails to compile
// the moment a domain field is renamed, rather than code that fails silently
// at 2am when a column comes back zeroed.
//
// Traceability: SPEC-ARCH-003 (hexagonal ports), SPEC-COST-001 (monetary
// precision), SPEC-SEC-003 (tenant isolation).
package postgres

import "github.com/udaykishore-resu/cloudoptix/internal/domain/core"

// moneyMicros splits a core.Money into the (micros, currency) pair every
// money-carrying column pair stores. See migrations/0005_resources.up.sql
// for why the database mirrors core.Money's own representation exactly
// rather than using NUMERIC or FLOAT: round-tripping through anything else
// would reintroduce the precision drift core.Money exists to prevent.
func moneyMicros(m core.Money) (int64, string) {
	return m.Micros(), string(m.Currency())
}

// moneyFromMicros is the inverse of moneyMicros. An empty currency column
// (a row written before a currency was known, or a zero-valued struct)
// defaults to USD exactly as core.MoneyFromMicros does, so this never
// produces an unusable Money value.
func moneyFromMicros(micros int64, currency string) core.Money {
	return core.MoneyFromMicros(micros, core.Currency(currency))
}
