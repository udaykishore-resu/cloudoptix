package memstore

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
)

func mkPlan(tenant core.TenantID, state execute.PlanState) execute.Plan {
	return execute.Plan{
		ID:       core.NewID("pln"),
		TenantID: tenant,
		Title:    "resize instance",
		State:    state,
		Rollback: &execute.RollbackPlan{Feasible: true},
		Steps:    []execute.Step{{ID: core.NewID("stp"), Kind: execute.StepMutate}},
	}
}

func TestExecutionRepo_ClaimDuePlansConcurrentWorkersNeverDoubleClaim(t *testing.T) {
	s := New()
	repo := s.Repositories().Executions
	ctx := ctxFor(tenantA)

	const total = 100
	planIDs := make([]core.ID, 0, total)
	for i := 0; i < total; i++ {
		p := mkPlan(tenantA, execute.PlanApproved)
		require.NoError(t, repo.CreatePlan(ctx, p))
		planIDs = append(planIDs, p.ID)
	}

	const workers = 8
	var wg sync.WaitGroup
	claimedBy := make([][]core.ID, workers)
	now := time.Now().UTC().Add(time.Minute)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				claimed, err := repo.ClaimDuePlans(ctx, now, "worker", 3)
				require.NoError(t, err)
				if len(claimed) == 0 {
					return
				}
				for _, p := range claimed {
					claimedBy[worker] = append(claimedBy[worker], p.ID)
				}
			}
		}(w)
	}
	wg.Wait()

	seen := map[core.ID]int{}
	totalClaimed := 0
	for _, ids := range claimedBy {
		for _, id := range ids {
			seen[id]++
			totalClaimed++
		}
	}
	assert.Equal(t, total, totalClaimed, "every plan must be claimed exactly once in total")
	for _, id := range planIDs {
		assert.Equal(t, 1, seen[id], "plan %s must be claimed by exactly one worker", id)
	}

	// Every claimed plan must have moved out of the claimable state, and no
	// plan should still be claimable.
	again, err := repo.ClaimDuePlans(ctx, now, "worker", total)
	require.NoError(t, err)
	assert.Empty(t, again)

	for _, id := range planIDs {
		p, err := repo.GetPlan(ctx, tenantA, id)
		require.NoError(t, err)
		assert.Equal(t, execute.PlanPreflight, p.State)
	}
}

func TestExecutionRepo_ClaimDuePlansRespectsScheduledFor(t *testing.T) {
	s := New()
	repo := s.Repositories().Executions
	ctx := ctxFor(tenantA)

	now := time.Now().UTC()
	future := now.Add(time.Hour)

	due := mkPlan(tenantA, execute.PlanScheduled)
	due.ScheduledFor = &now
	notDue := mkPlan(tenantA, execute.PlanScheduled)
	notDue.ScheduledFor = &future

	require.NoError(t, repo.CreatePlan(ctx, due))
	require.NoError(t, repo.CreatePlan(ctx, notDue))

	claimed, err := repo.ClaimDuePlans(ctx, now, "worker", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, due.ID, claimed[0].ID)
}

func TestExecutionRepo_ClaimPlansAwaitingValidation(t *testing.T) {
	s := New()
	repo := s.Repositories().Executions
	ctx := ctxFor(tenantA)

	finishedLongAgo := time.Now().UTC().Add(-time.Hour)
	finishedRecently := time.Now().UTC()

	ready := mkPlan(tenantA, execute.PlanExecuted)
	ready.FinishedAt = &finishedLongAgo
	ready.Validation.ObservationWindow = 10 * time.Minute

	notReady := mkPlan(tenantA, execute.PlanExecuted)
	notReady.FinishedAt = &finishedRecently
	notReady.Validation.ObservationWindow = time.Hour

	require.NoError(t, repo.CreatePlan(ctx, ready))
	require.NoError(t, repo.CreatePlan(ctx, notReady))

	claimed, err := repo.ClaimPlansAwaitingValidation(ctx, time.Now().UTC(), "worker", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, ready.ID, claimed[0].ID)
	assert.Equal(t, execute.PlanValidating, claimed[0].State)
}

func TestExecutionRepo_TenantIsolation(t *testing.T) {
	s := New()
	repo := s.Repositories().Executions

	p := mkPlan(tenantA, execute.PlanDraft)
	require.NoError(t, repo.CreatePlan(ctxFor(tenantA), p))

	_, err := repo.GetPlan(ctxFor(tenantB), tenantA, p.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantMismatch)
}
