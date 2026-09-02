package onboarding_test

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/deterministic"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/application/onboarding"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/tenancy"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// loadConversationTurns reads a testdata conversation file: one user
// message per non-empty, non-comment line.
func loadConversationTurns(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var turns []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		turns = append(turns, line)
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, turns, "testdata file %s produced no turns", path)
	return turns
}

// driveConversation runs Start (with the first turn as the initial message)
// followed by Send for every remaining turn, returning the final state and
// every reply seen along the way.
func driveConversation(t *testing.T, svc *onboarding.Service, turns []string) (ports.OnboardingState, []string) {
	t.Helper()
	require.NotEmpty(t, turns)

	state, err := svc.Start(context.Background(), ports.StartOnboardingInput{
		Actor: "test-user", ActorEmail: "test@example.com", InitialMessage: turns[0],
	})
	require.NoError(t, err)
	replies := []string{state.Reply}

	for _, msg := range turns[1:] {
		state, err = svc.Send(context.Background(), state.ConversationID, msg)
		require.NoError(t, err)
		replies = append(replies, state.Reply)
	}
	return state, replies
}

func newTestService(t *testing.T) (*onboarding.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	svc := onboarding.New(store, deterministic.New(), nil)
	return svc, store
}

// TestTestdataConversations drives every shipped example conversation
// end-to-end and asserts each one reaches an approvable specification. This
// is what proves the deterministic provider alone — no API key, no network
// — can carry onboarding all the way from an opening message to a tenant.
func TestTestdataConversations(t *testing.T) {
	files := []string{
		"testdata/fintech_payments.txt",
		"testdata/ecommerce_dontknow.txt",
		"testdata/healthcare_compliance.txt",
		"testdata/direct_edit_demo.txt",
	}
	for _, path := range files {
		t.Run(path, func(t *testing.T) {
			turns := loadConversationTurns(t, path)
			svc, _ := newTestService(t)
			state, replies := driveConversation(t, svc, turns)

			for _, r := range replies {
				assert.NotEmpty(t, r, "every turn must produce a reply")
			}

			summary, err := svc.Summarize(context.Background(), state.ConversationID)
			require.NoError(t, err)
			assert.True(t, summary.CanApprove, "blocking reasons: %v", summary.BlockingReasons)
			assert.NotEmpty(t, summary.SpecYAML)

			result, err := svc.Approve(context.Background(), ports.ApproveOnboardingInput{
				ConversationID: state.ConversationID, Actor: "test-user", ActorEmail: "test@example.com",
				TenantName: "Test Tenant", TenantSlug: "test-tenant-" + state.ConversationID.String()[:8],
				Plan: tenancy.PlanTrial,
			})
			require.NoError(t, err)
			assert.Equal(t, tenancy.StateActive, result.Tenant.State)
			assert.NotEmpty(t, result.NextSteps.ExternalID)
			assert.NotEmpty(t, result.NextSteps.RoleNames)
			assert.NotEmpty(t, result.NextSteps.PolicyDocuments)
			for scope, doc := range result.NextSteps.PolicyDocuments {
				assert.Contains(t, doc, "Version", "policy document for scope %s must be well-formed JSON", scope)
			}
		})
	}
}

// TestDirectEditDemo_ChecksSpecificInteractions goes beyond "does it reach
// approval" and checks the exact behaviours the task calls out by name:
// "show me what you know" and a direct edit revising a value stated earlier
// in the same conversation.
func TestDirectEditDemo_ChecksSpecificInteractions(t *testing.T) {
	turns := loadConversationTurns(t, "testdata/direct_edit_demo.txt")
	svc, _ := newTestService(t)

	state, err := svc.Start(context.Background(), ports.StartOnboardingInput{
		Actor: "test-user", InitialMessage: turns[0],
	})
	require.NoError(t, err)

	for _, msg := range turns[1:] {
		state, err = svc.Send(context.Background(), state.ConversationID, msg)
		require.NoError(t, err)
		if msg == "show me what you know" {
			assert.Contains(t, state.Reply, "Confirmed", "show-me-what-you-know must recap known fields")
		}
	}

	// The final turn is the direct edit; availability should now reflect it.
	assert.InDelta(t, 0.9999, state.Draft.Objectives.AvailabilityTarget, 1e-9)
	assert.Contains(t, state.Reply, "availability target")
}

// TestApprove_RefusesIncompleteSpecification proves approval is not just a
// formality: a conversation that never supplies a required field must be
// refused, not silently waved through.
func TestApprove_RefusesIncompleteSpecification(t *testing.T) {
	svc, _ := newTestService(t)
	state, err := svc.Start(context.Background(), ports.StartOnboardingInput{
		Actor: "test-user", InitialMessage: "We are Acme Corp.",
	})
	require.NoError(t, err)

	_, err = svc.Approve(context.Background(), ports.ApproveOnboardingInput{
		ConversationID: state.ConversationID, Actor: "test-user",
		TenantName: "Acme", TenantSlug: "acme-incomplete",
	})
	require.Error(t, err)
}

// TestCancel_AbandonsConversation exercises the Cancel path directly (not
// covered by the testdata conversations, which all run to approval).
func TestCancel_AbandonsConversation(t *testing.T) {
	svc, _ := newTestService(t)
	state, err := svc.Start(context.Background(), ports.StartOnboardingInput{Actor: "test-user"})
	require.NoError(t, err)

	err = svc.Cancel(context.Background(), state.ConversationID, "customer changed their mind")
	require.NoError(t, err)

	_, err = svc.Send(context.Background(), state.ConversationID, "hello again")
	require.Error(t, err, "a cancelled conversation must refuse further messages")
}

// TestState_ReadsWithoutSendingAMessage checks State is a pure read.
func TestState_ReadsWithoutSendingAMessage(t *testing.T) {
	svc, _ := newTestService(t)
	started, err := svc.Start(context.Background(), ports.StartOnboardingInput{
		Actor: "test-user", InitialMessage: "We are Acme Corp.",
	})
	require.NoError(t, err)

	again, err := svc.State(context.Background(), started.ConversationID)
	require.NoError(t, err)
	assert.Equal(t, started.ConversationID, again.ConversationID)
	assert.Equal(t, started.Draft.Organization.Name, again.Draft.Organization.Name)
	assert.Empty(t, again.Reply, "State must not fabricate a new assistant reply")
}
