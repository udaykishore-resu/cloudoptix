package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeSQSClient is a minimal in-memory stand-in for SQSClient. Its queue is a
// simple slice guarded by a mutex; ReceiveMessage returns (and removes)
// everything currently queued, up to MaxNumberOfMessages, mimicking real SQS
// closely enough for this adapter's own logic to be exercised without a real
// queue.
type fakeSQSClient struct {
	mu       sync.Mutex
	pending  []sqstypes.Message
	deleted  []string // ReceiptHandle values passed to DeleteMessage
	visExtds int32    // count of ChangeMessageVisibility calls
	recvCh   chan struct{}
}

func newFakeSQSClient() *fakeSQSClient {
	return &fakeSQSClient{recvCh: make(chan struct{}, 64)}
}

func (f *fakeSQSClient) enqueue(msgs ...sqstypes.Message) {
	f.mu.Lock()
	f.pending = append(f.pending, msgs...)
	f.mu.Unlock()
}

func (f *fakeSQSClient) ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	var batch []sqstypes.Message
	limit := int(params.MaxNumberOfMessages)
	if limit <= 0 || limit > len(f.pending) {
		limit = len(f.pending)
	}
	batch, f.pending = f.pending[:limit], f.pending[limit:]
	f.mu.Unlock()

	if len(batch) > 0 {
		select {
		case f.recvCh <- struct{}{}:
		default:
		}
	} else {
		// avoid a hot spin loop in the subscriber's own polling loop when
		// there is nothing to receive; a real long poll would block instead.
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (f *fakeSQSClient) DeleteMessage(_ context.Context, params *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	f.deleted = append(f.deleted, awssdk.ToString(params.ReceiptHandle))
	f.mu.Unlock()
	return &sqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQSClient) ChangeMessageVisibility(_ context.Context, _ *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	atomic.AddInt32(&f.visExtds, 1)
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeSQSClient) wasDeleted(receiptHandle string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rh := range f.deleted {
		if rh == receiptHandle {
			return true
		}
	}
	return false
}

func newTestSQSSubscriber(client SQSClient) *SQSSubscriber {
	return &SQSSubscriber{
		client: client, QueueURL: "https://sqs.example/queue", VisibilityTimeout: 30,
		WaitTimeSeconds: 1, DedupWindow: time.Minute, Logger: slog.Default(), dedup: newDedupCache(),
	}
}

func sqsMessageFor(t *testing.T, e ports.Event, receiptHandle string) sqstypes.Message {
	t.Helper()
	body, err := json.Marshal(e)
	require.NoError(t, err)
	return sqstypes.Message{
		Body: awssdk.String(string(body)), MessageId: awssdk.String(string(e.ID)),
		ReceiptHandle: awssdk.String(receiptHandle),
	}
}

func TestSQSSubscriber_process_DeletesOnSuccessSkipsOnTypeMismatch(t *testing.T) {
	ctx := context.Background()
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)

	var calls int32
	handler := ports.EventHandler(func(_ context.Context, e ports.Event) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	matching := sqsMessageFor(t, ports.Event{ID: "e1", Type: ports.EventCostUpdated, TenantID: "t1"}, "rh-1")
	sub.process(ctx, matching, map[ports.EventType]bool{ports.EventCostUpdated: true}, handler)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a message whose type matches the subscriber's filter must be dispatched")
	assert.True(t, client.wasDeleted("rh-1"), "a successfully handled message must be deleted")

	mismatch := sqsMessageFor(t, ports.Event{ID: "e2", Type: ports.EventSpecApproved, TenantID: "t1"}, "rh-2")
	sub.process(ctx, mismatch, map[ports.EventType]bool{ports.EventCostUpdated: true}, handler)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a message of a non-subscribed type must not be dispatched")
	assert.False(t, client.wasDeleted("rh-2"), "a skipped message must be left for whichever consumer's filter actually matches it")
}

func TestSQSSubscriber_process_DoesNotDeleteOnHandlerFailure(t *testing.T) {
	ctx := context.Background()
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)

	handler := ports.EventHandler(func(context.Context, ports.Event) error {
		return assertError{"handler failed"}
	})
	msg := sqsMessageFor(t, ports.Event{ID: "e3", Type: ports.EventCostUpdated, TenantID: "t1"}, "rh-3")
	sub.process(ctx, msg, nil, handler)

	assert.False(t, client.wasDeleted("rh-3"), "a failed handler must leave the message undeleted so SQS's own redrive policy can move it toward a DLQ")
}

func TestSQSSubscriber_process_MissingTenantIsLeftUndeleted(t *testing.T) {
	ctx := context.Background()
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)

	called := false
	handler := ports.EventHandler(func(context.Context, ports.Event) error {
		called = true
		return nil
	})
	msg := sqsMessageFor(t, ports.Event{ID: "e4", Type: ports.EventCostUpdated, TenantID: ""}, "rh-4")
	sub.process(ctx, msg, nil, handler)

	assert.False(t, called, "a message with no tenant_id must never reach the handler")
	assert.False(t, client.wasDeleted("rh-4"))
}

// TestSQSSubscriber_process_DedupesRedeliveryByIdempotencyKey is the required
// "event redelivery deduplication" scenario: a message whose IdempotencyKey
// this subscriber has already seen inside DedupWindow must be acknowledged
// (deleted) without invoking the handler a second time, since the handler
// already did the work the first time this same idempotency key arrived.
func TestSQSSubscriber_process_DedupesRedeliveryByIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)

	var calls int32
	handler := ports.EventHandler(func(context.Context, ports.Event) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	e := ports.Event{ID: "e5", Type: ports.EventCostUpdated, TenantID: "t1", IdempotencyKey: "idem-key-abc"}
	first := sqsMessageFor(t, e, "rh-5-first")
	sub.process(ctx, first, nil, handler)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "the first delivery of a new idempotency key must reach the handler")
	assert.True(t, client.wasDeleted("rh-5-first"))

	// Simulate SQS redelivering the same logical message (e.g. the
	// visibility timeout expired before the first delete call landed) under
	// a different receipt handle, as real SQS redeliveries always carry.
	redelivered := sqsMessageFor(t, e, "rh-5-redelivered")
	sub.process(ctx, redelivered, nil, handler)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "a redelivery carrying an already-seen idempotency key must NOT be dispatched to the handler again")
	assert.True(t, client.wasDeleted("rh-5-redelivered"), "a deduped redelivery must still be deleted so it does not loop forever")
}

func TestSQSSubscriber_process_DifferentIdempotencyKeysBothDispatch(t *testing.T) {
	ctx := context.Background()
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)

	var calls int32
	handler := ports.EventHandler(func(context.Context, ports.Event) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	sub.process(ctx, sqsMessageFor(t, ports.Event{ID: "e6", Type: ports.EventCostUpdated, TenantID: "t1", IdempotencyKey: "key-1"}, "rh-6"), nil, handler)
	sub.process(ctx, sqsMessageFor(t, ports.Event{ID: "e7", Type: ports.EventCostUpdated, TenantID: "t1", IdempotencyKey: "key-2"}, "rh-7"), nil, handler)

	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "two distinct idempotency keys must both reach the handler")
}

func TestSQSSubscriber_extendVisibility_ExtendsWhileHandlerRuns(t *testing.T) {
	ctx := context.Background()
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)
	sub.VisibilityTimeout = 1 // seconds, so the 2/3 interval is well under our sleep below

	msg := sqsMessageFor(t, ports.Event{ID: "e8", Type: ports.EventCostUpdated, TenantID: "t1"}, "rh-8")
	stop := sub.extendVisibility(ctx, msg)
	time.Sleep(1200 * time.Millisecond)
	stop()

	assert.GreaterOrEqual(t, atomic.LoadInt32(&client.visExtds), int32(1), "a long-running handler's message visibility must be extended at least once")
}

func TestSQSSubscriber_Subscribe_EndToEndViaFakeClient(t *testing.T) {
	client := newFakeSQSClient()
	sub := newTestSQSSubscriber(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got ports.Event
	var mu sync.Mutex
	done := make(chan struct{})
	var once sync.Once
	require.NoError(t, sub.Subscribe(ctx, []ports.EventType{ports.EventCostUpdated}, func(_ context.Context, e ports.Event) error {
		mu.Lock()
		got = e
		mu.Unlock()
		once.Do(func() { close(done) })
		return nil
	}))

	client.enqueue(sqsMessageFor(t, ports.Event{ID: "e9", Type: ports.EventCostUpdated, TenantID: "tenant-e2e"}, "rh-9"))

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber never dispatched the enqueued message")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "tenant-e2e", string(got.TenantID))
}
