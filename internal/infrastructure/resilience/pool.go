package resilience

import (
	"context"
	"errors"
	"sync"
)

// ErrPoolClosed is returned by Submit once Close has been called.
var ErrPoolClosed = errors.New("resilience: worker pool closed")

// Pool is a bounded worker pool: a fixed number of goroutines drain a
// fixed-capacity queue. Discovery scans and optimization analysis runs fan
// out per-region and per-resource work across a tenant's whole estate; an
// unbounded goroutine-per-task approach would let one tenant with forty
// thousand resources starve the process for every other tenant's request. A
// bounded pool makes the concurrency budget an explicit, tunable number
// instead of an emergent property of load.
type Pool struct {
	tasks  chan func(context.Context)
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// closeMu guards closed/close-the-channel so Submit can never race Close
	// into sending on an already-closed channel: Submit holds a read lock for
	// the duration of its send, Close takes the write lock (which waits for
	// all in-flight Submits to finish) before closing the channel.
	closeMu sync.RWMutex
	closed  bool
}

// NewPool starts workers goroutines draining a queue of the given capacity.
// Submit blocks (respecting the caller's context) once the queue is full,
// which is the backpressure signal that tells a discovery orchestrator to
// slow down rather than buffering unbounded work in memory.
func NewPool(workers, queueCapacity int) *Pool {
	if workers < 1 {
		workers = 1
	}
	if queueCapacity < 0 {
		queueCapacity = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		tasks:  make(chan func(context.Context), queueCapacity),
		ctx:    ctx,
		cancel: cancel,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case task, ok := <-p.tasks:
			if !ok {
				return
			}
			// A task's own panic must not take down the whole pool — one bad
			// discoverer or rule must not stop every other tenant's work in
			// flight on the same shared pool.
			func() {
				defer func() { _ = recover() }()
				task(p.ctx)
			}()
		}
	}
}

// Submit enqueues a task, blocking until a slot is free, the pool is closed,
// or ctx is done. The task receives the pool's own lifetime context merged
// with the caller's cancellation via ctx — in practice callers pass a context
// derived from the request that submitted the work so cancelling the request
// also abandons queued-but-not-yet-started work.
func (p *Pool) Submit(ctx context.Context, task func(context.Context)) error {
	p.closeMu.RLock()
	defer p.closeMu.RUnlock()
	if p.closed {
		return ErrPoolClosed
	}
	select {
	case p.tasks <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrPoolClosed
	}
}

// Close stops accepting new work and waits for in-flight and already-queued
// tasks to finish. It is safe to call more than once.
func (p *Pool) Close() {
	p.closeMu.Lock()
	if !p.closed {
		p.closed = true
		close(p.tasks)
	}
	p.closeMu.Unlock()
	p.wg.Wait()
}

// Shutdown is like Close but abandons queued (not yet started) tasks and
// returns as soon as in-flight tasks finish or ctx is done — the pool
// equivalent of http.Server.Shutdown, used by the server package's graceful
// shutdown sequence.
func (p *Pool) Shutdown(ctx context.Context) error {
	p.cancel() // signals workers to stop pulling new queued tasks
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
