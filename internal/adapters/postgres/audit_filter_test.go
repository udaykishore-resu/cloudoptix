package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestBuildAuditQueryBaseline(t *testing.T) {
	where, args := buildAuditQuery(audit.Query{TenantID: core.TenantID("t1")})
	if where != "tenant_id = $1" {
		t.Fatalf("unexpected where: %q", where)
	}
	if len(args) != 1 || args[0] != "t1" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestBuildAuditQueryToIsExclusive(t *testing.T) {
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildAuditQuery(audit.Query{TenantID: core.TenantID("t1"), To: to})
	if !strings.Contains(where, "at < $2") {
		t.Fatalf("expected exclusive upper bound at < $2, got %q", where)
	}
	if len(args) != 2 || args[1] != to {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestBuildAuditQueryFromIsInclusive(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	where, _ := buildAuditQuery(audit.Query{TenantID: core.TenantID("t1"), From: from})
	if !strings.Contains(where, "at >= $2") {
		t.Fatalf("expected inclusive lower bound at >= $2, got %q", where)
	}
}

func TestBuildAuditQueryActionsAndActors(t *testing.T) {
	where, args := buildAuditQuery(audit.Query{
		TenantID: core.TenantID("t1"),
		Actions:  []audit.Action{audit.ActionExecutionSucceeded, audit.ActionExecutionFailed},
		Actors:   []string{"alice", "bob"},
	})
	if !strings.Contains(where, "action = ANY($2::text[])") {
		t.Fatalf("expected action ANY clause, got %q", where)
	}
	if !strings.Contains(where, "actor = ANY($3::text[])") {
		t.Fatalf("expected actor ANY clause, got %q", where)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
}

func TestBuildAuditQueryOnlyMachine(t *testing.T) {
	machine := true
	where, args := buildAuditQuery(audit.Query{TenantID: core.TenantID("t1"), OnlyMachine: &machine})
	if !strings.Contains(where, "actor_machine = $2") {
		t.Fatalf("expected actor_machine clause, got %q", where)
	}
	if len(args) != 2 || args[1] != true {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestBuildAuditQuerySubjectID(t *testing.T) {
	where, args := buildAuditQuery(audit.Query{TenantID: core.TenantID("t1"), SubjectID: core.ID("res-1")})
	if !strings.Contains(where, "subject_id = $2") {
		t.Fatalf("expected subject_id clause, got %q", where)
	}
	if len(args) != 2 || args[1] != "res-1" {
		t.Fatalf("unexpected args: %v", args)
	}
}
