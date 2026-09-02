package postgres

import (
	"context"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// NotificationRepository is the pgx-backed ports.NotificationRepository.
type NotificationRepository struct{ db *DB }

// NewNotificationRepository builds a NotificationRepository over db.
func NewNotificationRepository(db *DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

var _ ports.NotificationRepository = (*NotificationRepository)(nil)

func (r *NotificationRepository) Enqueue(ctx context.Context, n ports.Notification) error {
	if err := core.GuardTenant(ctx, n.TenantID); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, n.TenantID, func(ctx context.Context) error {
		id := n.ID
		if id.IsZero() {
			id = core.NewID("ntf")
		}
		_, err := r.db.querier(ctx).Exec(ctx, `
			INSERT INTO notifications (id, tenant_id, channel, target, secret_ref, subject, body, blocks,
				severity, event_type, link_url, created_at, sent_at, attempts, error)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		`, string(id), string(n.TenantID), n.Channel, n.Target, n.SecretRef, n.Subject, n.Body,
			toJSON(n.Blocks), string(n.Severity), string(n.EventType), n.LinkURL, orNow(n.CreatedAt),
			nilableTime(n.SentAt), n.Attempts, n.Error)
		return mapErr(err)
	})
}

// ClaimPending is a cross-tenant background sweep — the outbound delivery
// worker's queue scan — so it runs under WithSystemScope like
// ExecutionRepository.ClaimDuePlans, and uses the same FOR UPDATE SKIP
// LOCKED pattern so N delivery workers can drain the queue concurrently
// without two of them ever claiming the same notification.
//
// The candidate filter is `sent_at IS NULL AND error = ”`, matching
// memstore's notificationRepo.ClaimPending exactly: a notification that has
// already recorded a failure (error <> ”) is not picked up again here. That
// looks surprising for a "queue for retry" method, but it is the existing,
// intentional contract this adapter must not silently change — a caller
// that wants to retry a failed notification clears its Error first (there
// is no separate "requeue" method in the port), the same way memstore
// requires.
func (r *NotificationRepository) ClaimPending(ctx context.Context, workerID string, limit int) ([]ports.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	_ = workerID // recorded by the caller's own logging/tracing, not persisted per row
	var out []ports.Notification
	err := r.db.WithSystemScope(ctx, func(ctx context.Context) error {
		rows, err := r.db.querier(ctx).Query(ctx, `
			UPDATE notifications SET attempts = attempts + 1
			WHERE id IN (
				SELECT id FROM notifications
				WHERE sent_at IS NULL AND error = ''
				ORDER BY created_at, id
				LIMIT `+limitPlaceholder(1)+`
				FOR UPDATE SKIP LOCKED
			)
			RETURNING `+notificationColumns, limit)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanNotification(rows)
			if err != nil {
				return mapErr(err)
			}
			out = append(out, n)
		}
		return mapErr(rows.Err())
	})
	return out, err
}

func (r *NotificationRepository) MarkSent(ctx context.Context, tenant core.TenantID, id core.ID, at time.Time) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx,
			`UPDATE notifications SET sent_at = $3, error = '' WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id), orNow(at))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("notification", id)
		}
		return nil
	})
}

func (r *NotificationRepository) MarkFailed(ctx context.Context, tenant core.TenantID, id core.ID, errMsg string) error {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return err
	}
	return r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		tag, err := r.db.querier(ctx).Exec(ctx,
			`UPDATE notifications SET error = $3 WHERE tenant_id = $1 AND id = $2`,
			string(tenant), string(id), errMsg)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return core.NotFound("notification", id)
		}
		return nil
	})
}

func (r *NotificationRepository) List(ctx context.Context, tenant core.TenantID, opts ports.ListOptions) (ports.Page[ports.Notification], error) {
	if err := core.GuardTenant(ctx, tenant); err != nil {
		return ports.Page[ports.Notification]{}, err
	}
	opts = opts.Normalize()
	after, err := expectCursor(opts.Cursor, 1)
	if err != nil {
		return ports.Page[ports.Notification]{}, err
	}
	var page ports.Page[ports.Notification]
	err = r.db.WithTenant(ctx, tenant, func(ctx context.Context) error {
		sql := `SELECT ` + notificationColumns + ` FROM notifications WHERE tenant_id = $1`
		args := []any{string(tenant)}
		if after != nil {
			args = append(args, after[0])
			sql += ` AND id > $2`
		}
		sql += ` ORDER BY id LIMIT ` + limitPlaceholder(len(args)+1)
		args = append(args, opts.Limit+1)
		rows, err := r.db.querier(ctx).Query(ctx, sql, args...)
		if err != nil {
			return mapErr(err)
		}
		defer rows.Close()
		var items []ports.Notification
		for rows.Next() {
			n, err := scanNotification(rows)
			if err != nil {
				return mapErr(err)
			}
			items = append(items, n)
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

const notificationColumns = `id, tenant_id, channel, target, secret_ref, subject, body, blocks,
	severity, event_type, link_url, created_at, sent_at, attempts, error`

func scanNotification(row rowScanner) (ports.Notification, error) {
	var n ports.Notification
	var blocks []byte
	if err := row.Scan(&n.ID, &n.TenantID, &n.Channel, &n.Target, &n.SecretRef, &n.Subject, &n.Body,
		&blocks, &n.Severity, &n.EventType, &n.LinkURL, &n.CreatedAt, &n.SentAt, &n.Attempts,
		&n.Error); err != nil {
		return ports.Notification{}, err
	}
	if err := fromJSON(blocks, &n.Blocks); err != nil {
		return ports.Notification{}, err
	}
	return n, nil
}
