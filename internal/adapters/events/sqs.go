package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// receiveMessageBatchLimit is SQS's own maximum for MaxNumberOfMessages —
// "batch delivery" means one long-poll call can return up to this many
// messages, all dispatched to the handler concurrently.
const receiveMessageBatchLimit = 10

// maxLongPollSeconds is SQS's own ceiling for WaitTimeSeconds.
const maxLongPollSeconds = 20

// SQSClient is the subset of *sqs.Client this package calls.
type SQSClient interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, params *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// SQSSubscriber implements ports.EventSubscriber over one SQS queue. See
// doc.go for why it deliberately does not implement its own retry-count or
// dead-letter bookkeeping: that is the queue's own RedrivePolicy's job, and
// this adapter's only responsibility on failure is to leave the message
// undeleted so SQS's native mechanism can do it.
type SQSSubscriber struct {
	client   SQSClient
	QueueURL string

	// VisibilityTimeout is the initial value SQS grants on receipt. It is
	// extended automatically (see visibilityExtendLoop) for as long as the
	// handler is still running, so a slow handler does not have the message
	// become visible to another consumer out from under it.
	VisibilityTimeout int32
	// WaitTimeSeconds enables long polling (up to maxLongPollSeconds); 0
	// falls back to short polling, which this adapter never sets on its own.
	WaitTimeSeconds int32
	// DedupWindow bounds how long a message's IdempotencyKey is remembered.
	// A key seen again inside the window is treated as a redelivery of
	// already-processed work and acknowledged (deleted) without invoking the
	// handler a second time — this is on top of, not instead of, the
	// handler's own responsibility to be idempotent, since the window is
	// bounded and best-effort (in-memory, per process).
	DedupWindow time.Duration

	Logger *slog.Logger

	dedup *dedupCache
}

var _ ports.EventSubscriber = (*SQSSubscriber)(nil)

// NewSQSSubscriber builds a subscriber for one queue.
func NewSQSSubscriber(cfg awssdk.Config, queueURL string, logger *slog.Logger) *SQSSubscriber {
	if logger == nil {
		logger = slog.Default()
	}
	return &SQSSubscriber{
		client: sqs.NewFromConfig(cfg), QueueURL: queueURL,
		VisibilityTimeout: 30, WaitTimeSeconds: maxLongPollSeconds, DedupWindow: 10 * time.Minute,
		Logger: logger, dedup: newDedupCache(),
	}
}

// Subscribe starts a background long-polling loop that dispatches matching
// messages to handler until ctx is cancelled. It returns immediately — the
// loop runs in its own goroutine — matching the port's contract that
// Subscribe registers a handler rather than blocking the caller.
func (s *SQSSubscriber) Subscribe(ctx context.Context, types []ports.EventType, handler ports.EventHandler) error {
	if handler == nil {
		return core.Invalid("events: cannot subscribe a nil handler")
	}
	set := make(map[ports.EventType]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	go s.loop(ctx, set, handler)
	return nil
}

func (s *SQSSubscriber) loop(ctx context.Context, types map[ports.EventType]bool, handler ports.EventHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		out, err := s.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: awssdk.String(s.QueueURL), MaxNumberOfMessages: receiveMessageBatchLimit,
			WaitTimeSeconds: s.WaitTimeSeconds, VisibilityTimeout: s.VisibilityTimeout,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Logger.Warn("events: SQS ReceiveMessage failed", "queue", s.QueueURL, "error", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		var wg sync.WaitGroup
		for _, msg := range out.Messages {
			msg := msg
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.process(ctx, msg, types, handler)
			}()
		}
		wg.Wait()
	}
}

func (s *SQSSubscriber) process(ctx context.Context, msg sqstypes.Message, types map[ports.EventType]bool, handler ports.EventHandler) {
	if msg.Body == nil {
		return
	}
	var e ports.Event
	if err := json.Unmarshal([]byte(*msg.Body), &e); err != nil {
		s.Logger.Error("events: SQS message body is not a valid event; leaving it for the queue's own redrive policy", "queue", s.QueueURL, "error", err)
		return // do not delete: an unparseable message must not be silently discarded
	}
	if e.TenantID == "" {
		s.Logger.Error("events: SQS message carries no tenant_id; leaving it for the queue's own redrive policy", "queue", s.QueueURL, "message_id", awssdk.ToString(msg.MessageId))
		return
	}
	if len(types) > 0 && !types[e.Type] {
		return // not for this subscriber; leave it for whichever consumer's filter it does match
	}
	if e.IdempotencyKey != "" && s.dedup.seen(e.IdempotencyKey, s.DedupWindow) {
		s.deleteMessage(ctx, msg)
		return
	}

	stopExtending := s.extendVisibility(ctx, msg)
	err := handler(ctx, e)
	stopExtending()

	if err != nil {
		s.Logger.Warn("events: handler failed; leaving message for redelivery", "type", e.Type, "tenant", e.TenantID, "error", err)
		return // never delete on failure — see doc.go
	}
	s.deleteMessage(ctx, msg)
}

func (s *SQSSubscriber) deleteMessage(ctx context.Context, msg sqstypes.Message) {
	if msg.ReceiptHandle == nil {
		return
	}
	if _, err := s.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: awssdk.String(s.QueueURL), ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		s.Logger.Warn("events: SQS DeleteMessage failed", "queue", s.QueueURL, "error", err)
	}
}

// extendVisibility periodically pushes a message's visibility timeout
// forward while its handler is still running, so a handler that legitimately
// takes longer than VisibilityTimeout does not have the message reappear
// for another consumer mid-processing. It returns a function that stops the
// extension loop; the caller must call it once the handler returns.
func (s *SQSSubscriber) extendVisibility(ctx context.Context, msg sqstypes.Message) func() {
	if msg.ReceiptHandle == nil || s.VisibilityTimeout <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	interval := time.Duration(s.VisibilityTimeout) * time.Second * 2 / 3
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := s.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
					QueueUrl: awssdk.String(s.QueueURL), ReceiptHandle: msg.ReceiptHandle,
					VisibilityTimeout: s.VisibilityTimeout,
				})
				if err != nil {
					s.Logger.Warn("events: extending SQS message visibility failed", "queue", s.QueueURL, "error", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
