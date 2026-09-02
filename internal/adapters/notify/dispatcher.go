package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// dedupWindow bounds how long an identical alert (same tenant, event type,
// subject and channel) is suppressed after its first occurrence — long
// enough to absorb a flapping condition's retries, short enough that a
// second, genuinely new occurrence hours later is never silently dropped.
const dedupWindow = 15 * time.Minute

// maxSendAttempts is the ceiling SendPending enforces itself, since the
// reference NotificationRepository has no attempt-limit concept of its own
// — see doc.go for why a retryable failure is otherwise left reclaimable
// forever.
const maxSendAttempts = 8

// Deps wires a Dispatcher to the rest of the platform. Notifiers maps a
// channel *type* ("email", "slack", "webhook") — not a channel name — to
// the one concrete implementation active for that type; a deployment
// chooses SMTP or SES for "email" at wiring time, not per-tenant.
type Deps struct {
	Specs         ports.SpecRepository
	Notifications ports.NotificationRepository
	Notifiers     map[string]ports.Notifier

	// LinkBase, when set, is used as a notification's LinkURL for any event
	// whose payload does not already carry one — typically the tenant's
	// dashboard URL for the relevant object. Optional.
	LinkBase string

	Clock  core.Clock
	Logger *slog.Logger
}

// Dispatcher turns domain events into rendered, routed, deduplicated
// outbound notifications, and separately drains whatever is due for
// delivery. See doc.go for why those are two different calls rather than
// one.
type Dispatcher struct {
	d     Deps
	dedup *alertDedup
}

// NewDispatcher builds a Dispatcher. Panics on a nil Specs or Notifications
// dependency — both are load-bearing for every call this type makes, the
// same fail-fast-at-construction discipline the rest of the platform's
// service constructors use for their own required Deps.
func NewDispatcher(d Deps) *Dispatcher {
	if d.Specs == nil {
		panic("notify: Dispatcher requires Deps.Specs")
	}
	if d.Notifications == nil {
		panic("notify: Dispatcher requires Deps.Notifications")
	}
	if d.Notifiers == nil {
		d.Notifiers = map[string]ports.Notifier{}
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Dispatcher{d: d, dedup: newAlertDedup()}
}

// Dispatch renders e against the tenant's current specification and enqueues
// one ports.Notification per matching, un-suppressed channel. Its signature
// matches ports.EventHandler, so it can be registered directly against an
// events.InProcess bus or SQS subscriber: bus.Subscribe(ctx, nil,
// dispatcher.Dispatch).
//
// A channel this event type has no subscription for, a channel whose
// Severities filter excludes the rendered severity, a channel silenced by
// quiet hours, or an alert this Dispatcher already delivered within
// dedupWindow are all treated the same way: quietly skipped, not an error —
// none of them mean anything went wrong, only that nothing needed sending.
// The only real errors this returns are ones that mean the platform itself
// could not determine what to do (no active spec, a broken repository).
func (dp *Dispatcher) Dispatch(ctx context.Context, e ports.Event) error {
	if e.TenantID == "" {
		return core.Invalid("notify: cannot dispatch an event with no tenant_id")
	}
	v, err := dp.d.Specs.GetActive(ctx, e.TenantID)
	if err != nil {
		return fmt.Errorf("notify: loading active specification for tenant %s: %w", e.TenantID, err)
	}
	notif := v.Spec.Notifications

	names := notif.Subscriptions[string(e.Type)]
	if len(names) == 0 {
		return nil // nothing subscribed to this event type
	}

	now := dp.d.Clock.Now()
	msg := render(e, dp.d.LinkBase)

	quiet, qerr := inQuietHours(notif.QuietHoursUTC, now)
	if qerr != nil {
		dp.d.Logger.Warn("notify: unparsable quiet_hours_utc, treating as never-quiet", "tenant", e.TenantID, "value", notif.QuietHoursUTC, "error", qerr)
		quiet = false
	}

	var enqueued int
	for _, name := range names {
		ch, ok := findChannel(notif.Channels, name)
		if !ok {
			dp.d.Logger.Warn("notify: subscription references unknown channel", "tenant", e.TenantID, "channel", name, "event_type", e.Type)
			continue
		}
		if !severityAllowed(ch, msg.Severity) {
			continue
		}
		// Quiet hours suppress everything except a critical alert — see
		// doc.go: the one case where interrupting someone is the point.
		if quiet && msg.Severity != core.SeverityCritical {
			continue
		}
		key := dedupKey(e.TenantID, e.Type, e.SubjectID, ch.Name)
		if dp.dedup.seen(key, dedupWindow, now) {
			continue
		}

		n := ports.Notification{
			ID: core.NewID("ntf"), TenantID: e.TenantID, Channel: ch.Type, Target: ch.Target,
			SecretRef: ch.SecretRef, Subject: msg.Subject, Body: msg.Body, Blocks: msg.Blocks,
			Severity: msg.Severity, EventType: e.Type, LinkURL: msg.LinkURL, CreatedAt: now,
		}
		if err := dp.d.Notifications.Enqueue(ctx, n); err != nil {
			return fmt.Errorf("notify: enqueuing notification for channel %s: %w", ch.Name, err)
		}
		enqueued++
	}
	dp.d.Logger.Debug("notify: dispatched event", "tenant", e.TenantID, "event_type", e.Type, "enqueued", enqueued, "quiet_hours", quiet)
	return nil
}

// SendPending claims up to limit due notifications and attempts delivery
// through the Notifier registered for each one's channel type. It returns
// how many were sent and how many were left pending or permanently failed
// (a partial batch failure is not itself an error — see the per-notification
// handling below — so a non-nil error here means the claim itself failed,
// not that some sends failed).
func (dp *Dispatcher) SendPending(ctx context.Context, workerID string, limit int) (sent, failed int, err error) {
	batch, err := dp.d.Notifications.ClaimPending(ctx, workerID, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("notify: claiming pending notifications: %w", err)
	}
	for _, n := range batch {
		notifier, ok := dp.d.Notifiers[n.Channel]
		if !ok {
			dp.markTerminal(ctx, n, fmt.Sprintf("no notifier registered for channel type %q", n.Channel))
			failed++
			continue
		}
		sendErr := notifier.Send(ctx, n)
		if sendErr == nil {
			if merr := dp.d.Notifications.MarkSent(ctx, n.TenantID, n.ID, dp.d.Clock.Now()); merr != nil {
				dp.d.Logger.Error("notify: MarkSent failed after a successful send", "tenant", n.TenantID, "id", n.ID, "error", merr)
			}
			sent++
			continue
		}
		if core.Retryable(sendErr) && n.Attempts < maxSendAttempts {
			// Deliberately not calling MarkFailed: see doc.go for why doing
			// so would strand this notification forever against the
			// reference repository. Leaving it unmarked keeps it a
			// ClaimPending candidate for the next sweep; Attempts was
			// already advanced by ClaimPending itself.
			dp.d.Logger.Warn("notify: send failed, will retry", "tenant", n.TenantID, "id", n.ID, "channel", n.Channel, "attempts", n.Attempts, "error", sendErr)
			failed++
			continue
		}
		dp.markTerminal(ctx, n, sendErr.Error())
		failed++
	}
	return sent, failed, nil
}

func (dp *Dispatcher) markTerminal(ctx context.Context, n ports.Notification, reason string) {
	if err := dp.d.Notifications.MarkFailed(ctx, n.TenantID, n.ID, reason); err != nil {
		dp.d.Logger.Error("notify: MarkFailed itself failed", "tenant", n.TenantID, "id", n.ID, "error", err)
	}
}

func findChannel(channels []spec.NotificationChannel, name string) (spec.NotificationChannel, bool) {
	for _, c := range channels {
		if c.Name == name {
			return c, true
		}
	}
	return spec.NotificationChannel{}, false
}

// severityAllowed reports whether a channel's own Severities filter (when
// set) permits sev. An empty filter means "every severity", matching the
// zero-value spec.NotificationChannel producing the most permissive, not
// most restrictive, behavior — a channel a tenant configured without
// thinking about severities at all should still receive alerts.
func severityAllowed(ch spec.NotificationChannel, sev core.Severity) bool {
	if len(ch.Severities) == 0 {
		return true
	}
	for _, s := range ch.Severities {
		if strings.EqualFold(s, string(sev)) {
			return true
		}
	}
	return false
}

func dedupKey(tenant core.TenantID, evtType ports.EventType, subject core.ID, channel string) string {
	return string(tenant) + "|" + string(evtType) + "|" + subject.String() + "|" + channel
}

// inQuietHours parses a "HH:MM-HH:MM" UTC range (the same 24-hour, zero-padded
// convention spec.MaintenanceWindow.StartUTC uses) and reports whether now
// falls inside it. An empty windowSpec means no quiet hours are configured
// (never quiet). The range wraps midnight correctly (e.g. "22:00-06:00")
// using the same logic governance.InMaintenanceWindow uses for maintenance
// windows, reimplemented locally rather than imported: quiet hours and
// maintenance windows are different concepts that happen to share a time
// format, and importing an application package's unexported helper from an
// adapter package would be a much steeper layering violation than the few
// lines of duplication here.
func inQuietHours(windowSpec string, now time.Time) (bool, error) {
	if windowSpec == "" {
		return false, nil
	}
	parts := strings.SplitN(windowSpec, "-", 2)
	if len(parts) != 2 {
		return false, fmt.Errorf("notify: quiet_hours_utc %q is not in HH:MM-HH:MM form", windowSpec)
	}
	start, err := parseHHMM(parts[0])
	if err != nil {
		return false, err
	}
	end, err := parseHHMM(parts[1])
	if err != nil {
		return false, err
	}
	u := now.UTC()
	minuteOfDay := u.Hour()*60 + u.Minute()
	startMin := start.hour*60 + start.minute
	endMin := end.hour*60 + end.minute

	if startMin == endMin {
		return false, nil // a zero-width window silences nothing
	}
	if startMin < endMin {
		return minuteOfDay >= startMin && minuteOfDay < endMin, nil
	}
	// wraps midnight, e.g. 22:00-06:00
	return minuteOfDay >= startMin || minuteOfDay < endMin, nil
}

type hhmm struct{ hour, minute int }

func parseHHMM(s string) (hhmm, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return hhmm{}, fmt.Errorf("notify: %q is not HH:MM", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return hhmm{}, fmt.Errorf("notify: %q has an invalid hour", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return hhmm{}, fmt.Errorf("notify: %q has an invalid minute", s)
	}
	return hhmm{hour: h, minute: m}, nil
}
