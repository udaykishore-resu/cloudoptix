// Package notify implements ports.Notifier for the channels CloudOptix
// actually ships — SMTP email, Amazon SES email, a Slack incoming webhook,
// and a generic HMAC-signed outbound webhook — plus the Dispatcher that
// decides, for one domain event, which tenant-configured channels should
// hear about it, what the message should say, and whether now is even an
// appropriate time to say it.
//
// # Where secrets live
//
// A tenant's specification names channels (an email address, a Slack
// channel, a webhook endpoint) but never carries the credential that lets
// CloudOptix actually reach them: spec.NotificationChannel.SecretRef is a
// reference string, resolved to a value only at send time via
// ports.SecretResolver, and only inside the Notifier that channel type
// needs it for. This is the same reason the specification is designed to be
// committed to a customer's git repository — a Slack incoming-webhook URL
// or an HMAC signing key is exactly as sensitive as a password, and a
// specification is not a secrets store. Concretely: for Slack and generic
// webhook the resolved secret carries the actual delivery endpoint itself
// (not merely a signing key), since spec.NotificationChannel's own doc
// comment is explicit that "webhook URLs and Slack tokens are never stored
// in the specification itself" — Target on those channel types is a
// display label, not the address. Email is the deliberate exception: SMTP
// resolves per-tenant relay credentials from SecretRef when one is
// configured, but SESNotifier signs with platform-level AWS credentials
// supplied at construction (the same aws.Config pattern the events adapters
// use for EventBridge and SQS) because CloudOptix, not the tenant, owns the
// sending identity for its own outbound mail.
//
// # Dispatch is not delivery
//
// Dispatcher.Dispatch turns one ports.Event into zero or more rendered
// ports.Notification records and hands them to ports.NotificationRepository
// — it never calls a Notifier directly. A separate call, SendPending, claims
// whatever is due and actually sends it. Splitting these two steps is what
// makes delivery durable across a process restart and retryable without
// re-deriving "who should hear about this and what should it say" every
// time: that decision is made once, at dispatch time, against the spec as
// it existed then.
//
// # Retrying against a repository that has no retry state of its own
//
// The reference NotificationRepository (internal/adapters/memstore) treats
// MarkFailed as terminal: a notification with a non-empty Error is excluded
// from every future ClaimPending sweep, so calling MarkFailed on a
// transient failure would silently strand the message forever with no
// built-in re-enqueue. SendPending therefore mirrors the discipline
// automation's own retry loop already uses (core.Retryable(err)): a
// retryable failure is left unmarked — Attempts already advanced by
// ClaimPending itself, SentAt and Error both still zero — so the very next
// worker sweep picks it up again, while a non-retryable failure or one that
// has exhausted MaxSendAttempts is marked failed for good, on the theory
// that a channel actively rejecting the message (invalid address, revoked
// webhook) will not start accepting it by being asked again sooner.
//
// # Quiet hours and deduplication fail toward fewer, not more, messages
//
// Both exist to protect a human's attention, not the platform's throughput,
// so both are asymmetric: a quiet-hours window suppresses everything below
// SeverityCritical (a critical alert always gets through — the one case
// where waking someone up is the point) and never re-delivers whatever it
// suppressed once the window ends, since a stale "here is what happened six
// hours ago" alert is often worse than none. Deduplication is a short,
// bounded, in-memory window keyed on tenant, event type, subject and
// channel — deliberately not persisted, matching the same
// best-effort-only-for-the-common-case reasoning events/dedup.go documents
// for SQS redelivery: it stops a noisy retry storm of the same underlying
// condition from paging someone once per retry, it does not promise
// exactly-once notification across a process restart.
//
// Traceability: REQ-NOT-001..010, SPEC-ARCH-005.
package notify
