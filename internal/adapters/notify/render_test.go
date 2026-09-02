package notify

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestRender_EveryDocumentedEventTypeProducesNonEmptyContent(t *testing.T) {
	types := []ports.EventType{
		ports.EventCostAnomalyDetected, ports.EventCostSLOBreached, ports.EventRecommendationCreated,
		ports.EventApprovalRequested, ports.EventOptimizationExecuted, ports.EventOptimizationValidated,
		ports.EventOptimizationRolledBack, ports.EventCostRegressionDetected,
	}
	for _, ty := range types {
		t.Run(string(ty), func(t *testing.T) {
			r := render(ports.Event{Type: ty, TenantID: testTenant, SubjectID: core.NewID("res")}, "")
			assert.NotEmpty(t, r.Subject)
			assert.NotEmpty(t, r.Body)
			assert.NotEmpty(t, r.Severity)
		})
	}
}

func TestRender_UnknownEventTypeFallsBackToGeneric(t *testing.T) {
	r := render(ports.Event{Type: ports.EventType("cloudoptix.something.unmapped"), TenantID: testTenant}, "")
	assert.Contains(t, r.Subject, "cloudoptix.something.unmapped")
	assert.Equal(t, core.SeverityInfo, r.Severity)
}

func TestRender_CostRegressionDefaultsToCritical(t *testing.T) {
	r := render(ports.Event{Type: ports.EventCostRegressionDetected, TenantID: testTenant}, "")
	assert.Equal(t, core.SeverityCritical, r.Severity)
}

func TestRender_PayloadSeverityOverridesDefault(t *testing.T) {
	r := render(ports.Event{
		Type: ports.EventRecommendationCreated, TenantID: testTenant, // default INFO
		Payload: map[string]any{"severity": "HIGH"},
	}, "")
	assert.Equal(t, core.SeverityHigh, r.Severity)
}

func TestRender_InvalidPayloadSeverityFallsBackToDefault(t *testing.T) {
	r := render(ports.Event{
		Type: ports.EventRecommendationCreated, TenantID: testTenant,
		Payload: map[string]any{"severity": "NOT_A_SEVERITY"},
	}, "")
	assert.Equal(t, core.SeverityInfo, r.Severity)
}

func TestRender_LinkURLPrefersPayloadOverBase(t *testing.T) {
	r := render(ports.Event{
		Type: ports.EventRecommendationCreated, TenantID: testTenant,
		Payload: map[string]any{"link_url": "https://app.example.com/rec/123"},
	}, "https://app.example.com/dashboard")
	assert.Equal(t, "https://app.example.com/rec/123", r.LinkURL)
}

func TestRender_LinkURLFallsBackToBaseWhenPayloadHasNone(t *testing.T) {
	r := render(ports.Event{Type: ports.EventRecommendationCreated, TenantID: testTenant}, "https://app.example.com/dashboard")
	assert.Equal(t, "https://app.example.com/dashboard", r.LinkURL)
}

func TestPayloadMoneyLike_HandlesJSONRoundTrippedMoney(t *testing.T) {
	e := ports.Event{Payload: map[string]any{
		"amount": map[string]any{"micros": int64(150000000), "currency": "USD", "amount": 150.0, "display": "$150.00"},
	}}
	assert.Equal(t, "$150.00", payloadMoneyLike(e, "amount"))
}

func TestPayloadMoneyLike_MissingKeyIsUnknownNotZero(t *testing.T) {
	e := ports.Event{Payload: map[string]any{}}
	assert.Equal(t, "an unknown amount", payloadMoneyLike(e, "amount"))
}

func TestPayloadString_FallsBackWhenAbsentOrEmpty(t *testing.T) {
	e := ports.Event{Payload: map[string]any{"present": "value", "empty": ""}}
	assert.Equal(t, "value", payloadString(e, "present", "fallback"))
	assert.Equal(t, "fallback", payloadString(e, "empty", "fallback"))
	assert.Equal(t, "fallback", payloadString(e, "missing", "fallback"))
}
