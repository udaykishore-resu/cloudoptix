package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/migrations"
)

// migrationFile is one parsed *.up.sql or *.down.sql filename.
type migrationFile struct {
	Version  int64
	Name     string
	Filename string
	Down     bool
}

// parseMigrationFilename extracts the version, human name and direction
// from a migration filename of the form "0007_econ.up.sql" or
// "0007_econ.down.sql". It is a pure function — no filesystem, no database —
// specifically so it can be unit tested without a live Postgres instance.
func parseMigrationFilename(name string) (migrationFile, error) {
	base := name
	var down bool
	switch {
	case strings.HasSuffix(base, ".up.sql"):
		base = strings.TrimSuffix(base, ".up.sql")
	case strings.HasSuffix(base, ".down.sql"):
		base = strings.TrimSuffix(base, ".down.sql")
		down = true
	default:
		return migrationFile{}, fmt.Errorf("migration filename %q does not end in .up.sql or .down.sql", name)
	}
	idx := strings.IndexByte(base, '_')
	if idx <= 0 {
		return migrationFile{}, fmt.Errorf("migration filename %q has no version_name separator", name)
	}
	versionStr, rest := base[:idx], base[idx+1:]
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		return migrationFile{}, fmt.Errorf("migration filename %q has a non-numeric version: %w", name, err)
	}
	if version <= 0 {
		return migrationFile{}, fmt.Errorf("migration filename %q has a non-positive version %d", name, version)
	}
	if rest == "" {
		return migrationFile{}, fmt.Errorf("migration filename %q has an empty name", name)
	}
	return migrationFile{Version: version, Name: rest, Filename: name, Down: down}, nil
}

// loadUpMigrations reads every *.up.sql file out of an fs.FS (the embedded
// one in production, a temp directory in tests) and returns them sorted by
// version ascending. It rejects a version that appears twice, which catches
// a copy-paste mistake in a new migration's filename before it ever reaches
// a database.
func loadUpMigrations(fsys fs.FS, dir string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	seen := map[int64]string{}
	var out []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		mf, err := parseMigrationFilename(e.Name())
		if err != nil {
			return nil, err
		}
		if prior, ok := seen[mf.Version]; ok {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", mf.Version, prior, mf.Filename)
		}
		seen[mf.Version] = mf.Filename
		out = append(out, mf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrator applies the embedded migrations to a database.
type Migrator struct {
	db *DB
}

// NewMigrator builds a Migrator bound to db.
func NewMigrator(db *DB) *Migrator { return &Migrator{db: db} }

// migrationsAdvisoryLockKey is the advisory-lock key every migration run
// takes before touching schema_migrations. It is a fixed, arbitrary string
// rather than something derived from the target database name on purpose:
// two pods pointed at the same database must contend for the same lock
// regardless of how each one's connection string happens to be spelled.
const migrationsAdvisoryLockKey = "cloudoptix:schema_migrations"

// Up applies every migration whose version is not yet recorded in
// schema_migrations, in ascending order, each inside its own transaction.
// The whole run is additionally wrapped in a transaction-scoped Postgres
// advisory lock (see advisoryLock in db.go), so if two API pods start at
// once and both race to migrate a fresh database, the second one blocks on
// the lock until the first commits, then finds every version already
// applied and does nothing — rather than both running "CREATE TABLE
// tenants" concurrently and one of them failing on a duplicate-table error
// that looks like a deployment failure rather than the harmless race it is.
func (m *Migrator) Up(ctx context.Context) (applied []string, err error) {
	files, err := loadUpMigrations(migrations.FS, ".")
	if err != nil {
		return nil, err
	}

	tx, err := m.db.pool.Begin(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := advisoryLock(ctx, tx, migrationsAdvisoryLockKey); err != nil {
		return nil, mapErr(err)
	}

	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    BIGINT PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, mapErr(err)
	}

	rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, mapErr(err)
	}
	already := map[int64]bool{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		already[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}

	for _, mf := range files {
		if already[mf.Version] {
			continue
		}
		sqlBytes, err := fs.ReadFile(migrations.FS, mf.Filename)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", mf.Filename, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			return nil, fmt.Errorf("apply %s: %w", mf.Filename, mapErr(err))
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			mf.Version, mf.Name,
		); err != nil {
			return nil, fmt.Errorf("record %s: %w", mf.Filename, mapErr(err))
		}
		applied = append(applied, mf.Filename)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapErr(err)
	}
	committed = true
	return applied, nil
}

// AppliedVersions reports the versions currently recorded, for diagnostics
// and for tests that want to assert on migration state without re-parsing
// filenames.
func (m *Migrator) AppliedVersions(ctx context.Context) ([]int64, error) {
	rows, err := m.db.pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, v)
	}
	return out, mapErr(rows.Err())
}
