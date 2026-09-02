// Package events implements ports.EventPublisher and ports.EventSubscriber.
//
// Two implementations live here. InProcess is an in-memory worker pool,
// used for local development, the demo tenant and every test in this
// codebase that needs a real (not mocked) event bus without an AWS account:
// it delivers at least once, retries a failing handler with backoff, and —
// because "the handler keeps failing" has to go somewhere — moves an event
// to its dead-letter list once retries are exhausted. AWS wires
// EventBridgePublisher (publish, over EventBridge PutEvents) to
// SQSSubscriber (consume, over SQS long polling) instead: the two form one
// logical bus the same way InProcess does, but split across a publish side
// and a consume side because that is how EventBridge and SQS actually work
// — a rule on the bus routes matching events to one or more queues, and each
// queue is its own independent, replayable subscription.
//
// # At-least-once is the only delivery guarantee either implementation makes
//
// Nothing here promises exactly-once. InProcess retries a handler that
// returned an error; SQS redelivers a message nobody deleted once its
// visibility timeout expires. Both of those can, and eventually will,
// deliver the same event to the same handler twice — a network partition
// between "the handler finished" and "the ack was recorded" is
// indistinguishable from "the handler never ran" to the transport, and a
// transport that tried to fix that by not retrying in the ambiguous case
// would silently drop real work instead. The IdempotencyKey field on
// ports.Event exists so a handler (or, for SQS, this adapter's own
// dedup cache) can recognise a redelivery and treat it as a no-op rather
// than double-applying it.
//
// # Why SQS's dead-letter handling is not reimplemented
//
// SQSSubscriber never deletes a message its handler failed to process. It
// deliberately does not run its own retry-count bookkeeping and does not
// move a message to a dead-letter queue itself: SQS already does exactly
// that, correctly, via a queue's native RedrivePolicy and
// maxReceiveCount — infrastructure the queue is provisioned with, not
// something client code can safely reimplement without racing the real
// mechanism. Reinventing it here would risk a message being "helped" into
// a client-side DLQ while SQS's own count is still ticking toward the same
// destination, producing duplicates or a message stuck between two
// bookkeeping systems that disagree.
//
// # Tenant scoping
//
// Every event carries core.TenantID, and both implementations refuse to
// publish an event with an empty one — an event with no tenant is not
// "platform-wide", it is a bug, because every consumer this package's
// events reach (audit, notify, the twin's cache invalidation) scopes its
// own side effects by that field. Publish fails closed rather than
// guessing.
//
// Traceability: REQ-EVT-001..008, SPEC-ARCH-004.
package events
