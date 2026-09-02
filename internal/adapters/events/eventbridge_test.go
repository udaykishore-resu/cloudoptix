package events

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeEventBridgeClient is a hand-rolled EventBridgeClient that records every
// PutEvents call it received and returns a caller-supplied response (or a
// simple all-succeeded response by default), so tests can assert on exactly
// what was sent without a real AWS account.
type fakeEventBridgeClient struct {
	calls []*eventbridge.PutEventsInput
	// respond, when set, is consulted per-call so a test can simulate a
	// partial per-entry failure or a hard transport error.
	respond func(*eventbridge.PutEventsInput) (*eventbridge.PutEventsOutput, error)
}

func (f *fakeEventBridgeClient) PutEvents(_ context.Context, params *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.calls = append(f.calls, params)
	if f.respond != nil {
		return f.respond(params)
	}
	out := &eventbridge.PutEventsOutput{Entries: make([]ebtypes.PutEventsResultEntry, len(params.Entries))}
	for i := range params.Entries {
		out.Entries[i] = ebtypes.PutEventsResultEntry{EventId: awssdk.String("evt-id")}
	}
	return out, nil
}

func newTestEventBridgePublisher(client EventBridgeClient) *EventBridgePublisher {
	return &EventBridgePublisher{client: client, EventBusName: "cloudoptix-bus", Source: "cloudoptix.platform"}
}

func TestEventBridgePublisher_Publish_RefusesEventWithNoTenant(t *testing.T) {
	f := &fakeEventBridgeClient{}
	p := newTestEventBridgePublisher(f)
	err := p.Publish(context.Background(), ports.Event{Type: ports.EventCostUpdated})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
	assert.Empty(t, f.calls, "must not call PutEvents at all when the event is rejected up front")
}

func TestEventBridgePublisher_Publish_SendsTenantScopedDetail(t *testing.T) {
	f := &fakeEventBridgeClient{}
	p := newTestEventBridgePublisher(f)

	err := p.Publish(context.Background(), ports.Event{
		Type: ports.EventCostAnomalyDetected, TenantID: "tenant-9", SubjectID: core.NewID("res"),
	})
	require.NoError(t, err)
	require.Len(t, f.calls, 1)
	require.Len(t, f.calls[0].Entries, 1)

	entry := f.calls[0].Entries[0]
	require.NotNil(t, entry.Detail)
	assert.Contains(t, *entry.Detail, `"tenant_id":"tenant-9"`, "the raw EventBridge Detail JSON must carry tenant_id so a downstream rule can scope on it directly")
	assert.Equal(t, string(ports.EventCostAnomalyDetected), *entry.DetailType)
	assert.Equal(t, "cloudoptix.platform", *entry.Source)
	assert.Equal(t, "cloudoptix-bus", *entry.EventBusName)
	require.Len(t, entry.Resources, 1)
	assert.Equal(t, "tenant-9", entry.Resources[0])
}

func TestEventBridgePublisher_PublishBatch_ChunksAboveTenEntries(t *testing.T) {
	f := &fakeEventBridgeClient{}
	p := newTestEventBridgePublisher(f)

	evts := make([]ports.Event, 23)
	for i := range evts {
		evts[i] = ports.Event{Type: ports.EventCostUpdated, TenantID: "tenant-batch"}
	}
	require.NoError(t, p.PublishBatch(context.Background(), evts))

	require.Len(t, f.calls, 3, "23 entries at a limit of 10 must become 3 PutEvents calls (10, 10, 3)")
	assert.Len(t, f.calls[0].Entries, 10)
	assert.Len(t, f.calls[1].Entries, 10)
	assert.Len(t, f.calls[2].Entries, 3)
}

func TestEventBridgePublisher_PublishBatch_AggregatesPartialEntryFailures(t *testing.T) {
	f := &fakeEventBridgeClient{
		respond: func(in *eventbridge.PutEventsInput) (*eventbridge.PutEventsOutput, error) {
			out := &eventbridge.PutEventsOutput{Entries: make([]ebtypes.PutEventsResultEntry, len(in.Entries))}
			for i := range in.Entries {
				if i == 1 {
					out.FailedEntryCount = 1
					out.Entries[i] = ebtypes.PutEventsResultEntry{
						ErrorCode: awssdk.String("InternalFailure"), ErrorMessage: awssdk.String("boom"),
					}
					continue
				}
				out.Entries[i] = ebtypes.PutEventsResultEntry{EventId: awssdk.String("ok")}
			}
			return out, nil
		},
	}
	p := newTestEventBridgePublisher(f)

	evts := []ports.Event{
		{Type: ports.EventCostUpdated, TenantID: "t1", ID: "e0"},
		{Type: ports.EventCostUpdated, TenantID: "t1", ID: "e1"},
		{Type: ports.EventCostUpdated, TenantID: "t1", ID: "e2"},
	}
	err := p.PublishBatch(context.Background(), evts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "e1")
	assert.Contains(t, err.Error(), "InternalFailure")
	assert.Contains(t, err.Error(), "boom")
}

func TestEventBridgePublisher_PublishBatch_HardTransportErrorStopsFurtherChunks(t *testing.T) {
	calls := 0
	f := &fakeEventBridgeClient{
		respond: func(*eventbridge.PutEventsInput) (*eventbridge.PutEventsOutput, error) {
			calls++
			return nil, assertError{"network unreachable"}
		},
	}
	p := newTestEventBridgePublisher(f)

	evts := make([]ports.Event, 15)
	for i := range evts {
		evts[i] = ports.Event{Type: ports.EventCostUpdated, TenantID: "t1"}
	}
	err := p.PublishBatch(context.Background(), evts)
	require.Error(t, err)
	assert.Equal(t, 1, calls, "a hard transport error on the first chunk must stop the loop rather than attempting the second chunk")
}

type assertError struct{ msg string }

func (e assertError) Error() string { return e.msg }
