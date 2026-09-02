package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// AuditRepository is the pgx-backed ports.AuditRepository.
//
// The hash chain's correctness never depends on this type: audit.Record.Seal
// and audit.VerifyChain own the cryptographic invariant, exactly as they do
// in the memstore reference. What this adapter owns is making Append
// atomic — read-current-head, seal, insert must happen as one unit per
// tenant, or two concurrent Append calls could both read the same head and
// seal two records against the same prevHash, producing a fork the chain
// can never detect as invalid (both forks verify individually; only a
// three-way race would even notice). advisoryLock (db.go) is what rules
// that out: it takes a transaction-scoped Postgres lock keyed on the tenant
// ID before the head is read, so a second Append for the same tenant simply
// waits its turn rather than racing. audit_logs' own UNIQUE(tenant_id,
// sequence) constraint (migrations/0013_audit.up.sql) is the backstop if
// that lock discipline ever has a bug — the INSERT itself would then fail
// instead of silently corrupting the chain.
type AuditRepository struct{ db *DB }

// NewAuditRepository builds an AuditRepository over db.
func NewAuditRepository(db *DB) *AuditRepository { return &AuditRepository{db: db} }

var _ ports.AuditRepository = (*AuditRepository)(nil)

// Append seals rec against the tenant's current chain head and inserts it,
// all inside a transaction serialised by a per-tenant advisory lock (see the
// type doc comment). It always opens its own transaction rather than
// participating in a caller's UnitOfWork: the advisory lock must be held for
// the read-head/seal/insert sequence specifically, and a caller composing
// this into a larger transaction could otherwise hold the lock far longer
// than the sequence needs, or — if the audit write itself is meant to be
// atomic with unrelated work — end up wrongly convinced that failing to
// append rolls back everything else. Every other repository in this package
// participates in an ambient transaction; Append is the one legitimate
// exception, and its own doc comment on ports.AuditRepository says so.
func (r *AuditRepository) Append(ctx context.Context, rec audit.Record) (audit.Record, error) {
	if err := core.GuardTenant(ctx, rec.TenantID); err != nil {
		return audit.Record{}, err
	}
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return audit.Record{}, mapErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := setScope(ctx, tx, string(rec.TenantID)); err != nil {
		return audit.Record{}, mapErr(err)
	}
	// Lock BEFORE reading the head: locking after the read would let two
	// transactions both read the same head and then serialise only on the
	// write, by which point each has already sealed against a now-stale
	// prevHash.
	if err := advisoryLock(ctx, tx, string(rec.TenantID)); err != nil {
		return audit.Record{}, mapErr(err)
	}
	prevHash, sequence, err := headTx(ctx, tx, rec.TenantID)
	if err != nil {
		return audit.Record{}, mapErr(err)
	}
	rec.Seal(prevHash, sequence+1)

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (id, tenant_id, sequence, action, outcome, actor, actor_roles,
			actor_machine, ip_address, user_agent, request_id, trace_id, subject_kind, subject_id,
			subject_name, before, after, aws_operation, aws_account_id, aws_region, aws_request_id,
			recommendation_id, plan_id, approval_id, policy_decision_id, spec_version_id, message,
			metadata, error, at, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,
			$25,$26,$27,$28,$29,$30,$31,$32)
	`, string(rec.ID), string(rec.TenantID), rec.Sequence, string(rec.Action), string(rec.Outcome),
		rec.Actor, toJSON(rec.ActorRoles), rec.ActorMachine, rec.IPAddress, rec.UserAgent, rec.RequestID,
		rec.TraceID, rec.SubjectKind, string(rec.SubjectID), rec.SubjectName, toJSON(rec.Before),
		toJSON(rec.After), rec.AWSOperation, string(rec.AWSAccountID), string(rec.AWSRegion),
		rec.AWSRequestID, string(rec.RecommendationID), string(rec.PlanID), string(rec.ApprovalID),
		string(rec.PolicyDecisionID), string(rec.SpecVersionID), rec.Message, toJSON(rec.Metadata),
		rec.Error, rec.At, rec.PrevHash, rec.Hash); err != nil {
		return audit.Record{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return audit.Record{}, mapErr(err)
	}
	committed = true
	return rec, nil
}

// headTx reads the tenant's current chain head inside tx, so it observes
// the row Append's own advisory lock is already protecting. A tenant with
// no audit history yet has no row to find; that is not an error, it is
// sequence 0 / prevHash "" — exactly what Seal expects as the starting
// state for the very first record.
func headTx(ctx context.Context, tx pgx.Tx, tenant core.TenantID) (prevHash string, sequence int64, err error) {
	row := tx.QueryRow(ctx, `
		SELECT hash, sequence FROM audit_logs WHERE tenant_id = $1 ORDER BY sequence DESC LIMIT 1
	`, string(tenant))
	if err := row.Scan(&prevHash, &sequence); err != nil {
		if isNoRows(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	return prevHash, sequence, nil
}

// Head reports the tenant's current chain head outside of any lock: it is a
// read-only convenience for callers that want to display or log the current
// position, not a step in the Append sequence (Append reads its own head
// under the advisory lock via headTx, and must not reuse this method — a
// second, unlocked read here would defeat the whole point of the lock).
func (r *AuditRepository) Head(ctx context.Context, tenant core.TenantID) (string, int64, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return "", 0, err
	}
	var hash string
	var sequence int64
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		row := r.db.querier(ctx).QueryRow(ctx,
			`SELECT hash, sequence FROM audit_logs WHERE tenant_id = $1 ORDER BY sequence DESC LIMIT 1`,
			string(tenant))
		if err := row.Scan(&hash, &sequence); err != nil {
			if isNoRows(err) {
				return nil
			}
			return mapErr(err)
		}
		return nil
	})
	return hash, sequence, err
}

// buildAuditQuery turns audit.Query into a WHERE fragment and its bound
// arguments. Every column is qualified with no table alias needed — Query
// never joins another table, unlike cost.go's buildCostFilter — but the
// convention of a pure, independently-testable builder is kept for the same
// reason it is everywhere else in this package: filter logic this easy to
// get subtly wrong (an inverted comparison on q.To, say) deserves a unit
// test that doesn't need a database.
func buildAuditQuery(q audit.Query) (string, []any) {
	conds := []string{"tenant_id = $1"}
	args := []any{string(q.TenantID)}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if len(q.Actions) > 0 {
		conds = append(conds, "action = ANY("+arg(toStringSlice(q.Actions))+"::text[])")
	}
	if len(q.Actors) > 0 {
		conds = append(conds, "actor = ANY("+arg(q.Actors)+"::text[])")
	}
	if !q.SubjectID.IsZero() {
		conds = append(conds, "subject_id = "+arg(string(q.SubjectID)))
	}
	if len(q.Outcomes) > 0 {
		conds = append(conds, "outcome = ANY("+arg(toStringSlice(q.Outcomes))+"::text[])")
	}
	if !q.From.IsZero() {
		conds = append(conds, "at >= "+arg(q.From))
	}
	if !q.To.IsZero() {
		// rec.At.Before(q.To), matching memstore's matchesAuditQuery exactly:
		// To is an exclusive upper bound, not inclusive.
		conds = append(conds, "at < "+arg(q.To))
	}
	if q.OnlyMachine != nil {
		conds = append(conds, "actor_machine = "+arg(*q.OnlyMachine))
	}
	where := conds[0]
	for _, c := range conds[1:] {
		where += " AND " + c
	}
	return where, args
}

// Query lists audit records newest-first, keyset-paginated on sequence — the
// strictly monotonic, tie-break-free ordering key per tenant (mirroring
// memstore's auditRepo.Query, which uses the zero-padded sequence as its
// sort key for exactly this reason). Cursor pagination here walks toward
// smaller sequence numbers as pages advance, since "newest first" means the
// next page picks up where the last (oldest-so-far) row left off.
func (r *AuditRepository) Query(ctx context.Context, q audit.Query) (ports.Page[audit.Record], error) {
	if err := core.GuardTenant(ctx, q.TenantID); err != nil {
		return ports.Page[audit.Record]{}, err
	}
	opts := ports.ListOptions{Limit: q.Limit, Cursor: q.Cursor}.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[audit.Record]{}, err
	}
	var page ports.Page[audit.Record]
	err = r.db.WithTenant(ctx, q.TenantID, func(ctx context.Context) error {
		where, args := buildAuditQuery(q)
		if after != nil {
			seq, convErr := strconv.ParseInt(after[0], 10, 64)
			if convErr != nil {
				return core.Invalid("malformed pagination cursor")
			}
			args = append(args, seq)
			where += " AND sequence < $" + strconv.Itoa(len(args))
		}
		sql := auditSelectSQL + ` WHERE ` + where + ` ORDER BY sequence DESC LIMIT ` + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []audit.Record
		for rows.Next() {
			rec, err := scanAuditRecord(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, rec)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		if len(items) > opts.Limit {
			items = items[:opts.Limit]
			page.NextCursor = encodeCursor(strconv.FormatInt(items[len(items)-1].Sequence, 10))
		}
		page.Items = items
		return nil
	})
	return page, err
}

// Verify walks the tenant's FULL chain from its origin and recomputes every
// hash — see memstore's auditRepo.Verify for why a hash chain cannot be
// verified from the middle: record N's PrevHash is only meaningful once
// record N-1 has itself been shown to produce that hash, all the way back
// to sequence 1. [from, to) narrows what counts toward RecordsChecked and
// what the report describes as its window, never where the walk starts —
// tampering before the window is still caught, because a broken link
// upstream of `from` makes every record after it unverifiable too.
//
// This reads the whole chain into memory and delegates to
// audit.VerifyChain rather than re-implementing the walk in SQL, so the
// verification logic lives in exactly one place and can never drift between
// adapters. A tenant's chain is bounded by its retention policy, not
// unbounded, so this is the same trade-off memstore's own implementation
// makes, not a new one introduced here.
func (r *AuditRepository) Verify(ctx context.Context, tenant core.TenantID, from, to time.Time) (audit.ChainVerification, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return audit.ChainVerification{}, err
	}
	var records []audit.Record
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx,
			auditSelectSQL+` WHERE tenant_id = $1 ORDER BY sequence`, string(tenant))
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			rec, err := scanAuditRecord(rows)
			if err != nil {
				return mapErr(err)
			}
			records = append(records, rec)
		}
		return mapErr(rows.Err())
	})
	if err != nil {
		return audit.ChainVerification{}, err
	}
	v := audit.VerifyChain(tenant, records)
	// RecordsChecked from VerifyChain counts the whole chain (it must, to
	// verify it); overwrite it with the count actually inside [from, to) so
	// the report's RecordsChecked answers "how many records did this window
	// cover", matching memstore's Verify field-for-field, while Valid /
	// FirstBreakAt / BreakDetail stay whatever the full-chain walk found.
	if !from.IsZero() || !to.IsZero() {
		windowed := 0
		for _, rec := range records {
			if (from.IsZero() || !rec.At.Before(from)) && (to.IsZero() || rec.At.Before(to)) {
				windowed++
			}
		}
		v.RecordsChecked = windowed
	}
	return v, nil
}

const auditSelectSQL = `
	SELECT id, tenant_id, sequence, action, outcome, actor, actor_roles, actor_machine, ip_address,
		user_agent, request_id, trace_id, subject_kind, subject_id, subject_name, before, after,
		aws_operation, aws_account_id, aws_region, aws_request_id, recommendation_id, plan_id,
		approval_id, policy_decision_id, spec_version_id, message, metadata, error, at, prev_hash, hash
	FROM audit_logs`

func scanAuditRecord(row rowScanner) (audit.Record, error) {
	var rec audit.Record
	var actorRoles, before, after, metadata []byte
	if err := row.Scan(&rec.ID, &rec.TenantID, &rec.Sequence, &rec.Action, &rec.Outcome, &rec.Actor,
		&actorRoles, &rec.ActorMachine, &rec.IPAddress, &rec.UserAgent, &rec.RequestID, &rec.TraceID,
		&rec.SubjectKind, &rec.SubjectID, &rec.SubjectName, &before, &after, &rec.AWSOperation,
		&rec.AWSAccountID, &rec.AWSRegion, &rec.AWSRequestID, &rec.RecommendationID, &rec.PlanID,
		&rec.ApprovalID, &rec.PolicyDecisionID, &rec.SpecVersionID, &rec.Message, &metadata, &rec.Error,
		&rec.At, &rec.PrevHash, &rec.Hash); err != nil {
		return audit.Record{}, err
	}
	if err := fromJSON(actorRoles, &rec.ActorRoles); err != nil {
		return audit.Record{}, err
	}
	if err := fromJSON(before, &rec.Before); err != nil {
		return audit.Record{}, err
	}
	if err := fromJSON(after, &rec.After); err != nil {
		return audit.Record{}, err
	}
	if err := fromJSON(metadata, &rec.Metadata); err != nil {
		return audit.Record{}, err
	}
	return rec, nil
}
