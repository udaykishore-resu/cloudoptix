package memstore

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// auditRepo implements ports.AuditRepository.
//
// The chain invariant lives in audit.Record.Seal/ComputeHash, which this
// adapter never bypasses: Append always looks up the tenant's current head
// and calls Seal with it, and Verify always recomputes every hash with
// ComputeHash rather than trusting the stored one. What this adapter owns is
// making Append atomic per tenant (auditMu.Lock for the whole
// read-head/seal/store sequence, so two concurrent Append calls for the same
// tenant can never seal against the same prevHash) and making Verify able to
// answer "did anything change" over a bounded time window without having to
// re-walk gigabytes of history from the very first record — see Verify's own
// comment for the trade-off that requires.
type auditRepo struct{ s *Store }

func (r *auditRepo) Append(ctx context.Context, rec audit.Record) (audit.Record, error) {
	if err := core.GuardTenant(ctx, rec.TenantID); err != nil {
		return audit.Record{}, err
	}
	r.s.auditMu.Lock()
	defer r.s.auditMu.Unlock()

	head := r.s.data.AuditHead[rec.TenantID]
	rec.Seal(head.Hash, head.Sequence+1)

	r.s.data.AuditRecords[rec.TenantID] = append(r.s.data.AuditRecords[rec.TenantID], deepCopy(rec))
	r.s.data.AuditHead[rec.TenantID] = auditHead{Hash: rec.Hash, Sequence: rec.Sequence}
	return deepCopy(rec), nil
}

func matchesAuditQuery(rec audit.Record, q audit.Query) bool {
	if len(q.Actions) > 0 && !containsVal(q.Actions, rec.Action) {
		return false
	}
	if len(q.Actors) > 0 && !containsVal(q.Actors, rec.Actor) {
		return false
	}
	if !q.SubjectID.IsZero() && rec.SubjectID != q.SubjectID {
		return false
	}
	if len(q.Outcomes) > 0 && !containsVal(q.Outcomes, rec.Outcome) {
		return false
	}
	if !q.From.IsZero() && rec.At.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && !rec.At.Before(q.To) {
		return false
	}
	if q.OnlyMachine != nil && rec.ActorMachine != *q.OnlyMachine {
		return false
	}
	return true
}

func (r *auditRepo) Query(ctx context.Context, q audit.Query) (ports.Page[audit.Record], error) {
	if err := core.GuardTenant(ctx, q.TenantID); err != nil {
		return ports.Page[audit.Record]{}, err
	}
	r.s.auditMu.RLock()
	items := make([]audit.Record, 0)
	for _, rec := range r.s.data.AuditRecords[q.TenantID] {
		if matchesAuditQuery(rec, q) {
			items = append(items, deepCopy(rec))
		}
	}
	r.s.auditMu.RUnlock()

	// Newest first, which is how every audit UI and export reads a log; the
	// sequence number is the tie-break-free ordering key since it is strictly
	// monotonic per tenant.
	keyOf := func(rec audit.Record) (string, string) { return fmt.Sprintf("%020d", rec.Sequence), rec.ID.String() }
	sortByCreatedThenID(items, keyOf)
	return paginate(items, ports.ListOptions{Limit: q.Limit, Cursor: q.Cursor}, keyOf), nil
}

// Verify walks the tenant's FULL chain from its origin, because a hash chain
// is only verifiable end to end: record N's PrevHash is meaningless without
// knowing record N-1 really produced that hash, all the way back to record 1.
// The [from, to) window narrows what counts toward RecordsChecked and what
// governs the reported window, but never where verification starts — a
// tamper anywhere in the tenant's history, even before `from`, is still
// detected, because a broken link before the window would make every record
// after it unverifiable too.
func (r *auditRepo) Verify(ctx context.Context, tenant core.TenantID, from, to time.Time) (audit.ChainVerification, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return audit.ChainVerification{}, err
	}
	r.s.auditMu.RLock()
	all := make([]audit.Record, len(r.s.data.AuditRecords[tenant]))
	copy(all, r.s.data.AuditRecords[tenant])
	r.s.auditMu.RUnlock()

	v := audit.ChainVerification{TenantID: tenant, Valid: true, VerifiedAt: timeNowUTC()}
	prev := ""
	for _, rec := range all {
		inWindow := (from.IsZero() || !rec.At.Before(from)) && (to.IsZero() || rec.At.Before(to))
		if inWindow {
			v.RecordsChecked++
		}
		if rec.PrevHash != prev {
			v.Valid = false
			seq := rec.Sequence
			v.FirstBreakAt = &seq
			v.BreakDetail = fmt.Sprintf(
				"record %d expected prev_hash %q but stored %q — a preceding record was altered or removed",
				rec.Sequence, prev, rec.PrevHash)
			return v, nil
		}
		if got := rec.ComputeHash(); got != rec.Hash {
			v.Valid = false
			seq := rec.Sequence
			v.FirstBreakAt = &seq
			v.BreakDetail = fmt.Sprintf(
				"record %d content hash mismatch: stored %q, recomputed %q — the record was altered",
				rec.Sequence, rec.Hash, got)
			return v, nil
		}
		prev = rec.Hash
	}
	return v, nil
}

func (r *auditRepo) Head(ctx context.Context, tenant core.TenantID) (string, int64, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return "", 0, err
	}
	r.s.auditMu.RLock()
	defer r.s.auditMu.RUnlock()
	head := r.s.data.AuditHead[tenant]
	return head.Hash, head.Sequence, nil
}
