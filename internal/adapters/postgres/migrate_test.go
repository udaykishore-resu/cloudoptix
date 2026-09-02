package postgres

import (
	"testing"
	"testing/fstest"

	"github.com/udaykishore-resu/cloudoptix/migrations"
)

func TestParseMigrationFilename(t *testing.T) {
	cases := []struct {
		name    string
		want    migrationFile
		wantErr bool
	}{
		{
			name: "0001_foundation.up.sql",
			want: migrationFile{Version: 1, Name: "foundation", Filename: "0001_foundation.up.sql", Down: false},
		},
		{
			name: "0013_audit.down.sql",
			want: migrationFile{Version: 13, Name: "audit", Filename: "0013_audit.down.sql", Down: true},
		},
		{
			name: "0010_execute.up.sql",
			want: migrationFile{Version: 10, Name: "execute", Filename: "0010_execute.up.sql", Down: false},
		},
		{name: "not_a_migration.txt", wantErr: true},
		{name: "nounderscore.up.sql", wantErr: true},
		{name: "0001_.up.sql", wantErr: true},    // empty name
		{name: "abc_foo.up.sql", wantErr: true},  // non-numeric version
		{name: "0000_foo.up.sql", wantErr: true}, // non-positive version
		{name: "-1_foo.up.sql", wantErr: true},   // non-positive version
		{name: "0002_two_words.up.sql", want: migrationFile{Version: 2, Name: "two_words", Filename: "0002_two_words.up.sql"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMigrationFilename(tc.name)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseMigrationFilename(%q) = %+v, want error", tc.name, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMigrationFilename(%q) unexpected error: %v", tc.name, err)
			}
			if got != tc.want {
				t.Fatalf("parseMigrationFilename(%q) = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

func TestLoadUpMigrationsOrdersByVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_b.up.sql":   &fstest.MapFile{Data: []byte("-- b")},
		"0002_b.down.sql": &fstest.MapFile{Data: []byte("-- b down")},
		"0010_c.up.sql":   &fstest.MapFile{Data: []byte("-- c")},
		"0001_a.up.sql":   &fstest.MapFile{Data: []byte("-- a")},
	}
	got, err := loadUpMigrations(fsys, ".")
	if err != nil {
		t.Fatalf("loadUpMigrations: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loadUpMigrations returned %d files, want 3 (down.sql must be excluded)", len(got))
	}
	wantOrder := []int64{1, 2, 10}
	for i, mf := range got {
		if mf.Version != wantOrder[i] {
			t.Fatalf("loadUpMigrations[%d].Version = %d, want %d (not sorted numerically)", i, mf.Version, wantOrder[i])
		}
	}
}

func TestLoadUpMigrationsRejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_a.up.sql": &fstest.MapFile{Data: []byte("-- a")},
		"0001_b.up.sql": &fstest.MapFile{Data: []byte("-- b")},
	}
	if _, err := loadUpMigrations(fsys, "."); err == nil {
		t.Fatalf("loadUpMigrations with duplicate version 1: expected an error")
	}
}

func TestLoadUpMigrationsRejectsMalformedFilename(t *testing.T) {
	fsys := fstest.MapFS{
		"not_numbered.up.sql": &fstest.MapFile{Data: []byte("-- x")},
	}
	if _, err := loadUpMigrations(fsys, "."); err == nil {
		t.Fatalf("loadUpMigrations with a malformed filename: expected an error")
	}
}

// TestEmbeddedMigrationsParse guards against a real migration file added to
// migrations/ that this package cannot parse — every *.up.sql the embedded
// FS actually ships must load cleanly and in strictly increasing version
// order, since that ordering is what Migrator.Up applies them in.
func TestEmbeddedMigrationsParse(t *testing.T) {
	files, err := loadUpMigrations(migrations.FS, ".")
	if err != nil {
		t.Fatalf("loadUpMigrations(embedded): %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no embedded *.up.sql migrations found")
	}
	for i := 1; i < len(files); i++ {
		if files[i].Version <= files[i-1].Version {
			t.Fatalf("embedded migrations not strictly increasing at index %d: %d then %d",
				i, files[i-1].Version, files[i].Version)
		}
	}
	// Every *.up.sql must have a matching *.down.sql: a migration a rollback
	// cannot undo is a trap for whoever runs it next.
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(embedded): %v", err)
	}
	downs := map[string]bool{}
	const downSuffix = ".down.sql"
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > len(downSuffix) && e.Name()[len(e.Name())-len(downSuffix):] == downSuffix {
			downs[e.Name()] = true
		}
	}
	for _, mf := range files {
		wantDown := mf.Filename[:len(mf.Filename)-len(".up.sql")] + ".down.sql"
		if !downs[wantDown] {
			t.Errorf("migration %s has no matching down migration %s", mf.Filename, wantDown)
		}
	}
}
