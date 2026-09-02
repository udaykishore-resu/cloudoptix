package postgres

import (
	"strings"
	"testing"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestBuildResourceFilterBaseline confirms the zero-value filter still scopes
// to the tenant and defaults to excluding tombstoned rows — the two
// conditions every other test in this file assumes are always present.
func TestBuildResourceFilterBaseline(t *testing.T) {
	where, args := buildResourceFilter(core.TenantID("t1"), ports.ResourceFilter{})

	if !strings.Contains(where, "tenant_id = $1") {
		t.Fatalf("expected tenant_id = $1 in where clause, got %q", where)
	}
	if !strings.Contains(where, "deleted = false") {
		t.Fatalf("expected deleted = false by default, got %q", where)
	}
	if len(args) != 1 || args[0] != "t1" {
		t.Fatalf("expected args = [t1], got %v", args)
	}
}

// TestBuildResourceFilterIncludeDeleted confirms IncludeDeleted suppresses
// the deleted = false condition rather than, say, negating it — a filter
// asking to see tombstones must see ALL rows, not only tombstones.
func TestBuildResourceFilterIncludeDeleted(t *testing.T) {
	where, _ := buildResourceFilter(core.TenantID("t1"), ports.ResourceFilter{IncludeDeleted: true})
	if strings.Contains(where, "deleted") {
		t.Fatalf("expected no deleted condition when IncludeDeleted is set, got %q", where)
	}
}

// TestBuildResourceFilterPlaceholderOrder verifies placeholders are assigned
// in the exact order conditions are appended, since every call site
// (List/Count/LoadInventory/LoadTopology) binds args positionally against
// this string and any drift between the two is a silent wrong-row bug that
// compiles fine and only shows up as bad query results.
func TestBuildResourceFilterPlaceholderOrder(t *testing.T) {
	f := ports.ResourceFilter{
		AccountIDs: []core.AccountID{"111111111111"},
		Regions:    []core.Region{"us-east-1"},
		Search:     "prod-db",
	}
	where, args := buildResourceFilter(core.TenantID("t1"), f)

	wantArgs := []any{"t1", []string{"111111111111"}, []string{"us-east-1"}, "%prod-db%", "%prod-db%"}
	if len(args) != len(wantArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(wantArgs), len(args), args)
	}
	for i := range wantArgs {
		gotSlice, gotIsSlice := args[i].([]string)
		wantSlice, wantIsSlice := wantArgs[i].([]string)
		if gotIsSlice != wantIsSlice {
			t.Fatalf("arg %d: type mismatch, got %T want %T", i, args[i], wantArgs[i])
		}
		if wantIsSlice {
			if len(gotSlice) != len(wantSlice) || gotSlice[0] != wantSlice[0] {
				t.Fatalf("arg %d: got %v want %v", i, gotSlice, wantSlice)
			}
			continue
		}
		if args[i] != wantArgs[i] {
			t.Fatalf("arg %d: got %v want %v", i, args[i], wantArgs[i])
		}
	}

	if !strings.Contains(where, "account_id = ANY($2::text[])") {
		t.Fatalf("expected account_id at $2, got %q", where)
	}
	if !strings.Contains(where, "region = ANY($3::text[])") {
		t.Fatalf("expected region at $3, got %q", where)
	}
	if !strings.Contains(where, "name ILIKE $4") || !strings.Contains(where, "native_id ILIKE $5") {
		t.Fatalf("expected search at $4/$5, got %q", where)
	}
}

// TestBuildResourceFilterCategoryExpandsToKinds confirms a Category filter
// (not a stored column) compiles down to the set of Kinds in that category,
// since resources.kind is what's actually indexed and queried.
func TestBuildResourceFilterCategoryExpandsToKinds(t *testing.T) {
	f := ports.ResourceFilter{Categories: []cloud.Category{cloud.CategoryDatabase}}
	where, args := buildResourceFilter(core.TenantID("t1"), f)

	if !strings.Contains(where, "kind = ANY($2::text[])") {
		t.Fatalf("expected kind = ANY($2::text[]) for category expansion, got %q", where)
	}
	kinds, ok := args[1].([]string)
	if !ok {
		t.Fatalf("expected args[1] to be []string, got %T", args[1])
	}
	if len(kinds) == 0 {
		t.Fatal("expected at least one kind in the database category")
	}
	for _, k := range kinds {
		if cloud.Kind(k).Category() != cloud.CategoryDatabase {
			t.Fatalf("kind %s is not in category database", k)
		}
	}
}

// TestBuildResourceFilterTagKeyOnly confirms a bare tag-key filter (no
// value) compiles to the `tags ? key` existence operator, distinct from the
// containment operator used when a value is also given.
func TestBuildResourceFilterTagKeyOnly(t *testing.T) {
	where, args := buildResourceFilter(core.TenantID("t1"), ports.ResourceFilter{TagKey: "team"})
	if !strings.Contains(where, "tags ? $2") {
		t.Fatalf("expected tags ? $2, got %q", where)
	}
	if args[1] != "team" {
		t.Fatalf("expected args[1] = team, got %v", args[1])
	}
}

// TestBuildResourceFilterTagKeyAndValue confirms a key+value filter uses
// jsonb containment rather than the existence operator.
func TestBuildResourceFilterTagKeyAndValue(t *testing.T) {
	where, _ := buildResourceFilter(core.TenantID("t1"), ports.ResourceFilter{TagKey: "team", TagValue: "payments"})
	if !strings.Contains(where, "tags @> $2::jsonb") {
		t.Fatalf("expected tags @> $2::jsonb, got %q", where)
	}
}

// TestBuildResourceFilterApplicationAndWorkloadID confirms a zero-valued ID
// is treated as "not filtered" (IsZero), not as an actual empty-string
// equality condition that would match nothing.
func TestBuildResourceFilterApplicationAndWorkloadID(t *testing.T) {
	where, args := buildResourceFilter(core.TenantID("t1"), ports.ResourceFilter{})
	if strings.Contains(where, "application_id") || strings.Contains(where, "workload_id") {
		t.Fatalf("expected no application/workload condition for zero IDs, got %q", where)
	}

	f := ports.ResourceFilter{ApplicationID: core.ID("app_123"), WorkloadID: core.ID("wl_456")}
	where, args = buildResourceFilter(core.TenantID("t1"), f)
	if !strings.Contains(where, "application_id = $2") || !strings.Contains(where, "workload_id = $3") {
		t.Fatalf("expected application_id = $2 and workload_id = $3, got %q", where)
	}
	if args[1] != "app_123" || args[2] != "wl_456" {
		t.Fatalf("unexpected args: %v", args)
	}
}

// TestBuildResourceFilterMinMonthlyCost confirms the money filter converts
// through moneyMicros rather than binding a core.Money struct directly,
// which pgx cannot serialize.
func TestBuildResourceFilterMinMonthlyCost(t *testing.T) {
	where, args := buildResourceFilter(core.TenantID("t1"), ports.ResourceFilter{
		MinMonthlyCost: core.USDollars(42),
	})
	if !strings.Contains(where, "monthly_cost_micros >= $2") {
		t.Fatalf("expected monthly_cost_micros >= $2, got %q", where)
	}
	if args[1] != int64(42_000_000) {
		t.Fatalf("expected 42_000_000 micros, got %v", args[1])
	}
}
