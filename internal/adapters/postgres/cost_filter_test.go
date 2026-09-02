package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestBuildCostFilterDefaultsToAmortizedBasis(t *testing.T) {
	where, args := buildCostFilter(core.TenantID("t1"), ports.CostFilter{})
	if !strings.Contains(where, "c.basis = $2") {
		t.Fatalf("expected c.basis = $2, got %q", where)
	}
	if args[1] != string(cost.BasisAmortized) {
		t.Fatalf("expected default basis amortized, got %v", args[1])
	}
}

func TestBuildCostFilterExplicitBasisWins(t *testing.T) {
	_, args := buildCostFilter(core.TenantID("t1"), ports.CostFilter{Basis: cost.BasisUnblended})
	if args[1] != string(cost.BasisUnblended) {
		t.Fatalf("expected explicit basis to win, got %v", args[1])
	}
}

func TestBuildCostFilterEveryColumnQualified(t *testing.T) {
	// Every condition must carry the c. alias: Breakdown's application/resource
	// dimensions LEFT JOIN resources, which shares column names
	// (tenant_id, account_id, region, environment) with cost_records, so an
	// unqualified condition here compiles for the unjoined callers but fails
	// with "ambiguous column" the moment it is reused in a joined query.
	f := ports.CostFilter{
		Period:       core.NewPeriod(time.Now(), time.Now().Add(time.Hour)),
		AccountIDs:   []core.AccountID{"111111111111"},
		Regions:      []core.Region{"us-east-1"},
		Services:     []string{"Amazon EC2"},
		Environments: []core.Environment{core.EnvProduction},
		TagKey:       "team",
		TagValue:     "payments",
	}
	where, _ := buildCostFilter(core.TenantID("t1"), f)
	for _, cond := range strings.Split(where, " AND ") {
		trimmed := strings.TrimSpace(cond)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "c.") {
			t.Fatalf("condition not qualified with c.: %q (full where: %q)", trimmed, where)
		}
	}
}

func TestBuildCostFilterApplicationSubqueryUnqualified(t *testing.T) {
	// The ApplicationID subquery filters against the *resources* table's own
	// tenant_id/application_id, which is a fully self-contained inner query —
	// it must NOT be qualified with c., since c is cost_records, not resources.
	where, _ := buildCostFilter(core.TenantID("t1"), ports.CostFilter{ApplicationID: core.ID("app_1")})
	if !strings.Contains(where, "c.resource_id IN (SELECT id FROM resources WHERE tenant_id = $1 AND application_id = $3)") {
		t.Fatalf("unexpected application subquery shape: %q", where)
	}
}

func TestBucketGranularityDefaultsToDaily(t *testing.T) {
	cases := map[cost.Granularity]cost.Granularity{
		cost.GranularityHourly:  cost.GranularityHourly,
		cost.GranularityMonthly: cost.GranularityMonthly,
		cost.GranularityDaily:   cost.GranularityDaily,
		"":                      cost.GranularityDaily,
		"weekly":                cost.GranularityDaily,
	}
	for in, want := range cases {
		if got := bucketGranularity(in); got != want {
			t.Errorf("bucketGranularity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBucketStartAndNextRoundTrip(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 37, 22, 0, time.UTC)

	day := bucketStart(ts, cost.GranularityDaily)
	if day != time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected daily bucket start: %v", day)
	}
	if next := bucketNext(day, cost.GranularityDaily); next != time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected daily bucket next: %v", next)
	}

	hour := bucketStart(ts, cost.GranularityHourly)
	if hour != time.Date(2026, 3, 15, 14, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected hourly bucket start: %v", hour)
	}

	month := bucketStart(ts, cost.GranularityMonthly)
	if month != time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected monthly bucket start: %v", month)
	}
	if next := bucketNext(month, cost.GranularityMonthly); next != time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("unexpected monthly bucket next: %v", next)
	}
}
