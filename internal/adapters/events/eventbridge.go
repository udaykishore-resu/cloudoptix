package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// putEventsBatchLimit is EventBridge's own PutEvents entry-count limit per
// call. PublishBatch chunks a larger slice into calls this size rather than
// failing a batch that happens to be larger than one API call allows.
const putEventsBatchLimit = 10

// EventBridgeClient is the subset of *eventbridge.Client this package
// calls, declared narrowly so a test can supply a fake without pulling in
// the real SDK client's construction requirements.
type EventBridgeClient interface {
	PutEvents(ctx context.Context, params *eventbridge.PutEventsInput, optFns ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

// EventBridgePublisher implements ports.EventPublisher over Amazon
// EventBridge. Every ports.Event is marshalled whole into the entry's
// Detail JSON — including TenantID — so a rule or a downstream consumer
// reading raw EventBridge payloads sees the same tenant scoping this
// package's own InProcess bus enforces at the API boundary.
type EventBridgePublisher struct {
	client EventBridgeClient
	// EventBusName targets a specific bus (a tenant-dedicated bus, or a
	// shared platform bus); empty uses the account's default bus.
	EventBusName string
	// Source is the EventBridge "source" field every entry is published
	// under, e.g. "cloudoptix.platform".
	Source string
	Logger *slog.Logger
}

var _ ports.EventPublisher = (*EventBridgePublisher)(nil)

// NewEventBridgePublisher builds a publisher from an AWS config (loaded by
// the caller, typically via github.com/aws/aws-sdk-go-v2/config.LoadDefaultConfig
// against CloudOptix's own platform account — this bus carries CloudOptix's
// own domain events, not a customer's AWS account activity).
func NewEventBridgePublisher(cfg awssdk.Config, eventBusName, source string, logger *slog.Logger) *EventBridgePublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &EventBridgePublisher{
		client: eventbridge.NewFromConfig(cfg), EventBusName: eventBusName, Source: source, Logger: logger,
	}
}

// Publish sends one event.
func (p *EventBridgePublisher) Publish(ctx context.Context, e ports.Event) error {
	return p.PublishBatch(ctx, []ports.Event{e})
}

// PublishBatch sends every event, chunked to EventBridge's own batch limit,
// and reports a single aggregate error naming every entry EventBridge
// itself rejected (a partial PutEvents failure does not stop the remaining
// chunks from being sent).
func (p *EventBridgePublisher) PublishBatch(ctx context.Context, evts []ports.Event) error {
	var failures []string
	for start := 0; start < len(evts); start += putEventsBatchLimit {
		end := start + putEventsBatchLimit
		if end > len(evts) {
			end = len(evts)
		}
		chunk := evts[start:end]
		entries := make([]ebtypes.PutEventsRequestEntry, 0, len(chunk))
		for _, e := range chunk {
			if e.TenantID == "" {
				return core.Invalid("events: cannot publish an event with no tenant_id (type %s)", e.Type)
			}
			if e.ID == "" {
				e.ID = string(core.NewID("evt"))
			}
			entry, err := p.buildEntry(e)
			if err != nil {
				return fmt.Errorf("events: encoding event %s: %w", e.ID, err)
			}
			entries = append(entries, entry)
		}

		out, err := p.client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: entries})
		if err != nil {
			return fmt.Errorf("events: PutEvents failed: %w", err)
		}
		if out.FailedEntryCount > 0 {
			for i, r := range out.Entries {
				if r.ErrorCode == nil {
					continue
				}
				msg := ""
				if r.ErrorMessage != nil {
					msg = *r.ErrorMessage
				}
				failures = append(failures, fmt.Sprintf("%s: %s (%s)", chunk[i].ID, *r.ErrorCode, msg))
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("events: %d entr(y/ies) rejected by EventBridge: %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// buildEntry marshals a ports.Event into an EventBridge entry. Detail
// carries the whole event as JSON (tenant_id included) rather than only a
// summary, so a rule filtering on detail.tenant_id or detail.type can be
// written directly against the same field names ports.Event exposes over
// HTTP.
func (p *EventBridgePublisher) buildEntry(e ports.Event) (ebtypes.PutEventsRequestEntry, error) {
	detail, err := json.Marshal(e)
	if err != nil {
		return ebtypes.PutEventsRequestEntry{}, err
	}
	entry := ebtypes.PutEventsRequestEntry{
		Source: awssdk.String(p.Source), DetailType: awssdk.String(string(e.Type)),
		Detail: awssdk.String(string(detail)), Resources: []string{string(e.TenantID)},
	}
	if p.EventBusName != "" {
		entry.EventBusName = awssdk.String(p.EventBusName)
	}
	if !e.OccurredAt.IsZero() {
		entry.Time = awssdk.Time(e.OccurredAt)
	}
	return entry, nil
}
