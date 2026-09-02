package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// defaultQueueCapacity bounds how many published-but-not-yet-delivered
// events InProcess buffers. A bound (rather than an unbounded channel)
// means a stalled or crash-looping set of subscribers turns into
// backpressure on Publish, not an unbounded memory leak.
const defaultQueueCapacity = 1024

// DeadLetterEntry is one event that exhausted every retry for one
// subscriber. It is retained (not merely logged) so an operator — or a
// test — can inspect exactly what failed, why, and how many times it was
// tried.
type DeadLetterEntry struct {
	Event      ports.Event
	Subscriber int // index into the subscriber list, stable for the process lifetime
	Err        string
	Attempts   int
	FailedAt   time.Time
}

type subscription struct {
	index   int
	types   map[ports.EventType]bool
	handler ports.EventHandler
}

// InProcess is an in-memory, worker-pool-backed implementation of
// ports.EventPublisher and ports.EventSubscriber. See doc.go for the
// delivery guarantees and the reasoning behind the dead-letter design.
type InProcess struct {
	workers     int
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	clock       core.Clock
	logger      *slog.Logger

	queue chan ports.Event

	mu       sync.RWMutex
	subs     []subscription
	nextSub  int
	inFlight sync.WaitGroup

	dlMu       sync.Mutex
	deadLetter []DeadLetterEntry

	startOnce sync.Once
	stop      chan struct{}
	stopOnce  sync.Once
}

var (
	_ ports.EventPublisher  = (*InProcess)(nil)
	_ ports.EventSubscriber = (*InProcess)(nil)
)

// Option configures an InProcess bus at construction.
type Option func(*InProcess)

// WithWorkers sets the worker pool size. The default is 4.
func WithWorkers(n int) Option { return func(p *InProcess) { p.workers = n } }

// WithRetry sets the maximum delivery attempts per subscriber per event
// (including the first) and the exponential backoff bounds between
// attempts. The default is 3 attempts, 50ms base, 2s max — small enough
// that a test suite exercising retry and dead-lettering does not become
// slow, large enough that a real handler's transient failure gets a
// meaningful number of chances.
func WithRetry(maxAttempts int, base, max time.Duration) Option {
	return func(p *InProcess) { p.maxAttempts, p.baseBackoff, p.maxBackoff = maxAttempts, base, max }
}

// WithClock overrides the clock used to stamp dead-letter entries. Tests
// that need deterministic timestamps pass a core.FixedClock.
func WithClock(c core.Clock) Option { return func(p *InProcess) { p.clock = c } }

// WithLogger overrides the logger a delivery failure and dead-lettering are
// reported through.
func WithLogger(l *slog.Logger) Option { return func(p *InProcess) { p.logger = l } }

// New builds an InProcess bus and starts its worker pool. Call Close to
// stop the workers; a bus that is never closed leaks its goroutines exactly
// as a channel-based worker pool always does — this is not different from
// any other long-lived background service in the platform.
func New(opts ...Option) *InProcess {
	p := &InProcess{
		workers: 4, maxAttempts: 3, baseBackoff: 50 * time.Millisecond, maxBackoff: 2 * time.Second,
		clock: core.SystemClock{}, logger: slog.Default(),
		queue: make(chan ports.Event, defaultQueueCapacity), stop: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(p)
	}
	p.start()
	return p
}

func (p *InProcess) start() {
	p.startOnce.Do(func() {
		for i := 0; i < p.workers; i++ {
			go p.workerLoop()
		}
	})
}

func (p *InProcess) workerLoop() {
	for {
		select {
		case e := <-p.queue:
			p.deliver(e)
		case <-p.stop:
			return
		}
	}
}

// Close stops the worker pool. Events already queued when Close is called
// are drained (workers keep consuming from the channel until it is empty)
// before the pool actually stops, so Close does not silently discard
// in-flight work.
func (p *InProcess) Close() {
	p.stopOnce.Do(func() {
		p.inFlight.Wait()
		close(p.stop)
	})
}

// Wait blocks until every currently-queued event has finished being
// delivered (successfully or dead-lettered) to every subscriber. It exists
// for tests: Publish is asynchronous by design, and a test asserting on a
// handler's side effect needs a way to know delivery has actually happened
// rather than sleeping and hoping.
func (p *InProcess) Wait() { p.inFlight.Wait() }

// Publish enqueues one event for asynchronous, at-least-once delivery to
// every matching subscriber. It fails closed on a missing tenant — see
// doc.go — before the event ever reaches the queue, so a caller finds out
// immediately rather than the failure surfacing later as a silently
// undelivered event.
func (p *InProcess) Publish(ctx context.Context, e ports.Event) error {
	if e.TenantID == "" {
		return core.Invalid("events: cannot publish an event with no tenant_id (type %s)", e.Type)
	}
	if e.ID == "" {
		e.ID = string(core.NewID("evt"))
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = p.clock.Now()
	}
	p.inFlight.Add(1)
	select {
	case p.queue <- e:
		return nil
	case <-ctx.Done():
		p.inFlight.Done()
		return ctx.Err()
	case <-p.stop:
		p.inFlight.Done()
		return fmt.Errorf("events: bus is closed")
	}
}

// PublishBatch publishes every event, stopping at the first error. Partial
// publication on error is reported by the returned error's context (which
// index failed is not tracked further than that) — callers that need
// per-event outcomes should call Publish individually.
func (p *InProcess) PublishBatch(ctx context.Context, evts []ports.Event) error {
	for i, e := range evts {
		if err := p.Publish(ctx, e); err != nil {
			return fmt.Errorf("events: publishing batch entry %d: %w", i, err)
		}
	}
	return nil
}

// Subscribe registers a handler for the given event types. Multiple
// subscribers may register for overlapping types; each one is delivered to
// independently — a slow or failing subscriber's retries do not block or
// affect any other subscriber's delivery of the same event, only its own.
func (p *InProcess) Subscribe(ctx context.Context, types []ports.EventType, handler ports.EventHandler) error {
	if handler == nil {
		return core.Invalid("events: cannot subscribe a nil handler")
	}
	set := make(map[ports.EventType]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	p.mu.Lock()
	sub := subscription{index: p.nextSub, types: set, handler: handler}
	p.nextSub++
	p.subs = append(p.subs, sub)
	p.mu.Unlock()
	return nil
}

// deliver dispatches one event to every currently-registered subscriber
// whose type set matches, each with its own retry budget, then marks the
// event's in-flight work done regardless of outcome — a dead-lettered
// delivery still counts as "finished" for Wait's purposes.
func (p *InProcess) deliver(e ports.Event) {
	defer p.inFlight.Done()
	p.mu.RLock()
	subs := make([]subscription, len(p.subs))
	copy(subs, p.subs)
	p.mu.RUnlock()

	for _, sub := range subs {
		if len(sub.types) > 0 && !sub.types[e.Type] {
			continue
		}
		p.deliverToOne(e, sub)
	}
}

func (p *InProcess) deliverToOne(e ports.Event, sub subscription) {
	var lastErr error
	for attempt := 1; attempt <= p.maxAttempts; attempt++ {
		if err := sub.handler(context.Background(), e); err == nil {
			return
		} else {
			lastErr = err
		}
		if attempt < p.maxAttempts {
			time.Sleep(backoff(attempt, p.baseBackoff, p.maxBackoff))
		}
	}
	p.logger.Warn("events: handler exhausted retries, dead-lettering", "type", e.Type, "tenant", e.TenantID, "subscriber", sub.index, "attempts", p.maxAttempts, "error", lastErr)
	p.dlMu.Lock()
	p.deadLetter = append(p.deadLetter, DeadLetterEntry{
		Event: e, Subscriber: sub.index, Err: lastErr.Error(), Attempts: p.maxAttempts, FailedAt: p.clock.Now(),
	})
	p.dlMu.Unlock()
}

// DeadLetters returns every event that exhausted its retries, across every
// subscriber, since this bus was created.
func (p *InProcess) DeadLetters() []DeadLetterEntry {
	p.dlMu.Lock()
	defer p.dlMu.Unlock()
	out := make([]DeadLetterEntry, len(p.deadLetter))
	copy(out, p.deadLetter)
	return out
}

func backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}
