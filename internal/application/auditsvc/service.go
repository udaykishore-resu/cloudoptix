package auditsvc

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Deps is every dependency Service needs.
type Deps struct {
	Audit  ports.AuditRepository
	Clock  core.Clock
	Logger *slog.Logger
}

// Service implements ports.AuditService.
type Service struct {
	d Deps
}

var _ ports.AuditService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	if d.Audit == nil {
		return nil, fmt.Errorf("auditsvc: NewService missing required dependency: Audit")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

// Record converts a transport-facing ports.AuditEntry into an audit.Record
// and appends it, which is what seals it against the tenant's hash chain.
// The tenant scope comes from the caller's principal, not from the entry
// itself — ports.AuditEntry deliberately carries no tenant field, since a
// caller-supplied tenant on a write path is exactly the kind of value a
// cross-tenant write bug would get wrong silently.
func (s *Service) Record(ctx context.Context, r ports.AuditEntry) error {
	tenant, ok := core.TenantFrom(ctx)
	if !ok {
		return core.NewError(core.ErrUnauthenticated, "no_principal", "request has no authenticated principal")
	}
	at := r.At
	if at.IsZero() {
		at = s.d.Clock.Now()
	}
	rec := audit.Record{
		ID: r.ID, TenantID: tenant, Action: audit.Action(r.Action), Outcome: audit.Outcome(r.Outcome),
		Actor: r.Actor, ActorMachine: r.Machine,
		SubjectKind: r.Subject, SubjectID: r.SubjectID,
		Before: r.Before, After: r.After, Message: r.Message, Metadata: r.Metadata, At: at,
	}
	_, err := s.d.Audit.Append(ctx, rec)
	if err != nil {
		return fmt.Errorf("auditsvc: recording %s: %w", r.Action, err)
	}
	return nil
}

// Query maps a ports.AuditQuery onto the domain audit.Query and translates
// the result page back to ports.AuditEntry.
func (s *Service) Query(ctx context.Context, tenant core.TenantID, q ports.AuditQuery) (ports.Page[ports.AuditEntry], error) {
	aq := audit.Query{
		TenantID: tenant, Actions: toActions(q.Actions), Actors: q.Actors,
		SubjectID: q.SubjectID, Outcomes: toOutcomes(q.Outcomes),
		From: q.From, To: q.To, Limit: q.Limit, Cursor: q.Cursor,
	}
	page, err := s.d.Audit.Query(ctx, aq)
	if err != nil {
		return ports.Page[ports.AuditEntry]{}, err
	}
	items := make([]ports.AuditEntry, 0, len(page.Items))
	for _, r := range page.Items {
		items = append(items, toEntry(r))
	}
	return ports.Page[ports.AuditEntry]{Items: items, NextCursor: page.NextCursor, Total: page.Total}, nil
}

// Verify delegates to the repository's chain verification. The return type
// is `any` (matching ports.AuditService) rather than audit.ChainVerification
// specifically, because ports is not permitted to import the audit domain
// package's result type into its own signature here — the concrete value
// returned is always an audit.ChainVerification, and a caller type-asserts
// it.
func (s *Service) Verify(ctx context.Context, tenant core.TenantID, from, to time.Time) (any, error) {
	return s.d.Audit.Verify(ctx, tenant, from, to)
}

// Timeline assembles the complete, causally-ordered story of one change:
// the recommendation's creation, the policy decision, the approval
// request and grant, the plan, each execution step with its AWS operation,
// validation, any rollback, and savings realized. audit.Query has no
// recommendation-id filter (see audit.Query), so this pulls the tenant's
// records in bulk and selects the ones that carry this recommendation's id
// — either as audit.Record.RecommendationID, the linked-identifier field
// audit.Record documents existing for exactly this purpose, or as the
// subject itself for the "recommendation created" record — then sorts by
// Sequence, the tenant's strictly monotonic write order, which is causal
// order by construction.
func (s *Service) Timeline(ctx context.Context, tenant core.TenantID, recommendationID core.ID) ([]ports.AuditEntry, error) {
	records, err := s.allRecords(ctx, tenant)
	if err != nil {
		return nil, err
	}

	var story []audit.Record
	for _, r := range records {
		if r.RecommendationID == recommendationID ||
			(r.SubjectKind == "recommendation" && r.SubjectID == recommendationID) {
			story = append(story, r)
		}
	}
	sort.Slice(story, func(i, j int) bool { return story[i].Sequence < story[j].Sequence })

	out := make([]ports.AuditEntry, 0, len(story))
	for _, r := range story {
		out = append(out, toEntry(r))
	}
	return out, nil
}

// allRecords pages through the tenant's full audit trail. It exists only for
// Timeline, which has no way to filter server-side by recommendation id (see
// its own doc comment) and so has to look at everything.
func (s *Service) allRecords(ctx context.Context, tenant core.TenantID) ([]audit.Record, error) {
	const pageSize = 500
	var all []audit.Record
	cursor := ""
	for {
		page, err := s.d.Audit.Query(ctx, audit.Query{TenantID: tenant, Limit: pageSize, Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("auditsvc: reading audit trail for tenant %s: %w", tenant, err)
		}
		all = append(all, page.Items...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	return all, nil
}

func toActions(ss []string) []audit.Action {
	if len(ss) == 0 {
		return nil
	}
	out := make([]audit.Action, len(ss))
	for i, s := range ss {
		out[i] = audit.Action(s)
	}
	return out
}

func toOutcomes(ss []string) []audit.Outcome {
	if len(ss) == 0 {
		return nil
	}
	out := make([]audit.Outcome, len(ss))
	for i, s := range ss {
		out[i] = audit.Outcome(s)
	}
	return out
}

func toEntry(r audit.Record) ports.AuditEntry {
	return ports.AuditEntry{
		ID: r.ID, Sequence: r.Sequence, Action: string(r.Action), Outcome: string(r.Outcome),
		Actor: r.Actor, Machine: r.ActorMachine, Subject: r.SubjectKind, SubjectID: r.SubjectID,
		Message: r.Message, Before: r.Before, After: r.After, Metadata: r.Metadata,
		At: r.At, Hash: r.Hash,
	}
}
