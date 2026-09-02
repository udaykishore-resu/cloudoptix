package learning

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-learning-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func newTestService(t *testing.T) (*Service, ports.SavingsRepository) {
	t.Helper()
	repos := memstore.New().Repositories()
	svc, err := NewService(Deps{Savings: repos.Savings, Clock: core.FixedClock{T: testNow}})
	require.NoError(t, err)
	return svc, repos.Savings
}

// fakeKnowledgeStore records what was indexed, so a test can assert that a
// calibration pass actually fed the RAG corpus rather than merely computing
// numbers nobody downstream can retrieve.
type fakeKnowledgeStore struct{ indexed []ports.Document }

func (f *fakeKnowledgeStore) Index(_ context.Context, docs []ports.Document) error {
	f.indexed = append(f.indexed, docs...)
	return nil
}
func (f *fakeKnowledgeStore) Search(context.Context, core.TenantID, string, int, []string) ([]ports.RetrievedDocument, error) {
	return nil, nil
}
func (f *fakeKnowledgeStore) Delete(context.Context, core.TenantID, []string) error { return nil }
func (f *fakeKnowledgeStore) Count(context.Context, core.TenantID) (int, error) {
	return len(f.indexed), nil
}

var _ ports.KnowledgeStore = (*fakeKnowledgeStore)(nil)

func TestNewService_RequiresSavings(t *testing.T) {
	_, err := NewService(Deps{})
	require.Error(t, err)
}

// TestRecalibrate_MinimumSampleGuard proves the same guarantee at this
// package's own level (automation's learn_test.go proves it end to end
// through automation.Service.Learn): fewer than the minimum sample count
// keeps a rule's multipliers neutral regardless of how bad the few outcomes
// look.
func TestRecalibrate_MinimumSampleGuard(t *testing.T) {
	svc, savings := newTestService(t)
	const rule optimize.RuleID = "rule.ec2.rightsize"

	for i := 0; i < 3; i++ {
		require.NoError(t, savings.SaveOutcome(ctxFor(testTenant), execute.Outcome{
			TenantID: testTenant, RuleID: rule, Verdict: execute.VerdictFailure,
			RolledBack: true, SavingRatio: 0, ObservedAt: testNow,
		}))
	}

	result, err := svc.Recalibrate(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	calib := result.Calibrations[rule]
	assert.Equal(t, 3, calib.Samples)
	assert.Equal(t, 1.0, calib.ConfidenceMultiplier, "below minSamples, the multiplier must stay neutral")
}

// TestRecalibrate_FeedsKnowledgeStoreWhenConfigured proves calibrated rules
// are written back to the RAG corpus as "outcomes"-sourced documents when a
// KnowledgeStore is wired in, and that Recalibrate still succeeds and
// computes real calibrations without one.
func TestRecalibrate_FeedsKnowledgeStoreWhenConfigured(t *testing.T) {
	repos := memstore.New().Repositories()
	kb := &fakeKnowledgeStore{}
	svc, err := NewService(Deps{Savings: repos.Savings, Knowledge: kb, Clock: core.FixedClock{T: testNow}})
	require.NoError(t, err)

	const rule optimize.RuleID = "rule.ec2.rightsize"
	for i := 0; i < 5; i++ {
		require.NoError(t, repos.Savings.SaveOutcome(ctxFor(testTenant), execute.Outcome{
			TenantID: testTenant, RuleID: rule, Verdict: execute.VerdictSuccess,
			SavingRatio: 0.9, ObservedAt: testNow,
		}))
	}

	result, err := svc.Recalibrate(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 1, result.RulesCalibrated)
	require.Len(t, kb.indexed, 1)
	assert.Equal(t, "outcomes", kb.indexed[0].Source)
	assert.Equal(t, testTenant, kb.indexed[0].TenantID)
	assert.Contains(t, kb.indexed[0].Content, string(rule))
}

// TestRecalibrate_WithoutKnowledgeStoreStillComputes proves the optional
// dependency narrows what gets indexed, not what gets computed.
func TestRecalibrate_WithoutKnowledgeStoreStillComputes(t *testing.T) {
	svc, savings := newTestService(t)
	const rule optimize.RuleID = "rule.ec2.rightsize"
	for i := 0; i < 5; i++ {
		require.NoError(t, savings.SaveOutcome(ctxFor(testTenant), execute.Outcome{
			TenantID: testTenant, RuleID: rule, Verdict: execute.VerdictSuccess,
			SavingRatio: 0.9, ObservedAt: testNow,
		}))
	}
	result, err := svc.Recalibrate(ctxFor(testTenant), testTenant)
	require.NoError(t, err)
	assert.Equal(t, 1, result.RulesCalibrated)
}

// TestRecordOutcome_DefaultsIDAndTimestamp proves RecordOutcome does not
// require its caller to pre-fill bookkeeping fields.
func TestRecordOutcome_DefaultsIDAndTimestamp(t *testing.T) {
	svc, savings := newTestService(t)
	require.NoError(t, svc.RecordOutcome(ctxFor(testTenant), execute.Outcome{
		TenantID: testTenant, RuleID: "rule.x", Verdict: execute.VerdictSuccess,
	}))
	all, err := savings.ListOutcomes(ctxFor(testTenant), testTenant, "rule.x", 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.False(t, all[0].ID.IsZero())
	assert.Equal(t, testNow, all[0].ObservedAt)
}
