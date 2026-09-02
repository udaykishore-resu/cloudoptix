package postgres

import (
	"context"
	"strconv"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// ConversationRepository is the pgx-backed ports.ConversationRepository.
//
// Turns are normalized into their own table (conversation_turns) rather than
// a JSONB array on conversations, for the reason migrations/0012's comment
// gives: AppendTurn is the hot path — one INSERT per chat message — and a
// JSONB-array column would force a read-modify-write of the whole
// conversation on every single message, getting more expensive the longer a
// conversation runs. The ordinal column is what lets turns be read back in
// order without relying on insertion order or a timestamp that two turns
// could tie on.
type ConversationRepository struct{ db *DB }

// NewConversationRepository builds a ConversationRepository over db.
func NewConversationRepository(db *DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}

var _ ports.ConversationRepository = (*ConversationRepository)(nil)

func (r *ConversationRepository) Create(ctx context.Context, c ports.Conversation) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, c.TenantID, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		_, err := q.Exec(ctx, `
			INSERT INTO conversations (id, tenant_id, kind, title, actor, spec_id, state, created_at,
				updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, string(c.ID), string(c.TenantID), string(c.Kind), c.Title, c.Actor, nilableID(c.SpecID), c.State,
			orNow(c.CreatedAt), orNow(c.UpdatedAt))
		if err != nil {
			return mapErr(err)
		}
		for i, t := range c.Turns {
			if err := insertTurn(ctx, q, c.TenantID, c.ID, int64(i), t); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func (r *ConversationRepository) Get(ctx context.Context, tenant core.TenantID, id core.ID) (ports.Conversation, error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Conversation{}, err
	}
	var out ports.Conversation
	err := r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		row := q.QueryRow(ctx, conversationSelectSQL+` WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		c, err := scanConversation(row)
		if err != nil {
			return mapErr(err)
		}
		turns, err := loadTurns(ctx, q, tenant, id)
		if err != nil {
			return err
		}
		c.Turns = turns
		out = c
		return nil
	})
	return out, err
}

// AppendTurn assigns t the next ordinal after the conversation's current
// last turn — read under the same transaction the INSERT runs in, so two
// concurrent AppendTurn calls for the same conversation cannot both compute
// the same ordinal (the second would only see the first's write once it
// commits and takes the row lock the UPDATE below acquires). It also bumps
// conversations.updated_at, matching memstore's AppendTurn exactly.
func (r *ConversationRepository) AppendTurn(ctx context.Context, tenant core.TenantID, id core.ID, t ports.Turn) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		q := r.db.querier(ctx)
		tag, err := q.Exec(ctx, `UPDATE conversations SET updated_at = now() WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("conversation", id)
		}
		var nextOrdinal int64
		row := q.QueryRow(ctx,
			`SELECT COALESCE(MAX(ordinal), -1) + 1 FROM conversation_turns WHERE tenant_id = $1 AND conversation_id = $2`,
			string(tenant), string(id))
		if err := row.Scan(&nextOrdinal); err != nil {
			return mapErr(err)
		}
		if t.ID.IsZero() {
			t.ID = core.NewID("trn")
		}
		return mapErr(insertTurn(ctx, q, tenant, id, nextOrdinal, t))
	})
}

func (r *ConversationRepository) Update(ctx context.Context, c ports.Conversation) error {
	if err := core.GuardTenant(ctx, c.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, c.TenantID, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx, `
			UPDATE conversations SET kind = $3, title = $4, actor = $5, spec_id = $6, state = $7,
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2
		`, string(c.TenantID), string(c.ID), string(c.Kind), c.Title, c.Actor, nilableID(c.SpecID), c.State)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("conversation", c.ID)
		}
		return nil
	})
}

// List keyset-paginates on id, matching every other List method in this
// package, and deliberately omits Turns from each item — the same trade-off
// SimulationRepository.ListSimulations makes for candidates: a conversation
// list screen shows title/state/updated_at, not the full transcript, and
// Get is the one call site that pays for loading turns.
func (r *ConversationRepository) List(ctx context.Context, tenant core.TenantID, kind ports.ConversationKind, opts ports.ListOptions) (ports.Page[ports.Conversation], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[ports.Conversation]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[ports.Conversation]{}, err
	}
	var page ports.Page[ports.Conversation]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		conds := []string{"tenant_id = $1"}
		args := []any{string(tenant)}
		if kind != "" {
			args = append(args, string(kind))
			conds = append(conds, "kind = $"+strconv.Itoa(len(args)))
		}
		if after != nil {
			args = append(args, after[0])
			conds = append(conds, "id > $"+strconv.Itoa(len(args)))
		}
		where := conds[0]
		for _, c := range conds[1:] {
			where += " AND " + c
		}
		sql := conversationSelectSQL + ` WHERE ` + where + ` ORDER BY id LIMIT ` + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []ports.Conversation
		for rows.Next() {
			c, err := scanConversation(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, c)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
		if len(items) > opts.Limit {
			items = items[:opts.Limit]
			page.NextCursor = encodeCursor(string(items[len(items)-1].ID))
		}
		page.Items = items
		return nil
	})
	return page, err
}

func insertTurn(ctx context.Context, q Querier, tenant core.TenantID, conversationID core.ID, ordinal int64, t ports.Turn) error {
	if t.ID.IsZero() {
		t.ID = core.NewID("trn")
	}
	_, err := q.Exec(ctx, `
		INSERT INTO conversation_turns (id, tenant_id, conversation_id, ordinal, role, content, at,
			tool_calls, tool_results, retrieved, citations, spec_patch, provenance, input_tokens,
			output_tokens, latency_ms, model, grounded, grounding_issues, degraded)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, string(t.ID), string(tenant), string(conversationID), ordinal, string(t.Role), t.Content,
		orNow(t.At), toJSON(t.ToolCalls), toJSON(t.ToolResults), toJSON(t.Retrieved), toJSON(t.Citations),
		toJSON(t.SpecPatch), toJSON(t.Provenance), t.InputTokens, t.OutputTokens, t.LatencyMS, t.Model,
		t.Grounded, toJSON(t.GroundingIssues), t.Degraded)
	return err
}

func loadTurns(ctx context.Context, q Querier, tenant core.TenantID, conversationID core.ID) ([]ports.Turn, error) {
	rows, err := q.Query(ctx, `
		SELECT id, role, content, at, tool_calls, tool_results, retrieved, citations, spec_patch,
			provenance, input_tokens, output_tokens, latency_ms, model, grounded, grounding_issues,
			degraded
		FROM conversation_turns WHERE tenant_id = $1 AND conversation_id = $2 ORDER BY ordinal
	`, string(tenant), string(conversationID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var turns []ports.Turn
	for rows.Next() {
		var t ports.Turn
		var toolCalls, toolResults, retrieved, citations, specPatch, provenance, groundingIssues []byte
		if err := rows.Scan(&t.ID, &t.Role, &t.Content, &t.At, &toolCalls, &toolResults, &retrieved,
			&citations, &specPatch, &provenance, &t.InputTokens, &t.OutputTokens, &t.LatencyMS, &t.Model,
			&t.Grounded, &groundingIssues, &t.Degraded); err != nil {
			return nil, mapErr(err)
		}
		if err := fromJSON(toolCalls, &t.ToolCalls); err != nil {
			return nil, err
		}
		if err := fromJSON(toolResults, &t.ToolResults); err != nil {
			return nil, err
		}
		if err := fromJSON(retrieved, &t.Retrieved); err != nil {
			return nil, err
		}
		if err := fromJSON(citations, &t.Citations); err != nil {
			return nil, err
		}
		if err := fromJSON(specPatch, &t.SpecPatch); err != nil {
			return nil, err
		}
		if err := fromJSON(provenance, &t.Provenance); err != nil {
			return nil, err
		}
		if err := fromJSON(groundingIssues, &t.GroundingIssues); err != nil {
			return nil, err
		}
		turns = append(turns, t)
	}
	return turns, mapErr(rows.Err())
}

const conversationSelectSQL = `
	SELECT id, tenant_id, kind, title, actor, spec_id, state, created_at, updated_at
	FROM conversations`

func scanConversation(row rowScanner) (ports.Conversation, error) {
	var c ports.Conversation
	var specID *string
	if err := row.Scan(&c.ID, &c.TenantID, &c.Kind, &c.Title, &c.Actor, &specID, &c.State, &c.CreatedAt,
		&c.UpdatedAt); err != nil {
		return ports.Conversation{}, err
	}
	if specID != nil {
		c.SpecID = core.ID(*specID)
	}
	return c, nil
}

// nilableID maps a possibly-zero core.ID onto a nullable TEXT column: a
// zero ID means "not linked to anything" (an onboarding conversation with
// no draft yet), and that is a SQL NULL, not the empty string — spec_id has
// no NOT NULL constraint precisely so this distinction can exist.
func nilableID(id core.ID) any {
	if id.IsZero() {
		return nil
	}
	return string(id)
}
