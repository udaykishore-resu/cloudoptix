// This file implements ports.Executor against the Estate.
//
// The design decision: sixteen distinct AWS actions (resize an instance,
// delete a snapshot, apply an S3 lifecycle policy...) share exactly one
// shape — check the resource still matches what was planned, capture its
// prior state, change one thing about it, confirm the change stuck — so
// this file factors that shape into one generic engine (genericExecutor)
// driven by a small per-action table (actionSpec) rather than writing the
// same four-step Plan/Preflight/Apply/Rollback dance sixteen times. Each
// concrete action (executor_compute.go, executor_storage.go,
// executor_serverless.go, executor_network.go) contributes only the handful
// of functions that are actually action-specific: how to read the target's
// current state, whether a given mutation has already landed, how to apply
// it, and how to undo it.
//
// Idempotency is semantic rather than ledger-based: Apply's mutate step
// asks "does the estate already match the desired end state?" before doing
// anything, so retrying an Apply call with the same step (same
// IdempotencyKey, same Parameters) is always safe — a resize that already
// landed is detected and skipped, not reapplied. Rollback works the same
// way in reverse, restoring the exact attribute values captured in the
// plan's snapshot at Plan time; deletions (a volume, a snapshot, a
// released Elastic IP, aborted multipart parts) are marked infeasible to
// roll back, matching AWS itself — CloudOptix will still make the change
// when it is worth it, but the approval screen says plainly that the
// change is irreversible rather than pretending a delete can be undone.
package awssim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// actionSpec is the per-action table genericExecutor is driven by. Every
// field is either a fact about the action (its ActionType, the IAM actions
// it needs, whether it can be undone) or a small pure function over the
// Estate that knows how to read, check, change or restore exactly the
// attributes that action cares about — nothing in this struct knows
// anything about plans, steps or snapshots, which is what keeps each
// concrete spec in the other executor_*.go files down to a screenful.
type actionSpec struct {
	action          optimize.ActionType
	requiredActions []string
	kind            cloud.Kind // the resource kind this action targets
	awsAction       string     // the underlying AWS API call, for the approval screen
	titleFmt        string     // fmt.Sprintf template taking the native ID

	rollbackFeasible bool
	infeasibleReason string         // required when rollbackFeasible is false
	dataLossRisk     core.RiskLevel // carried on the rollback plan

	// deleteAction marks actions whose mutate step removes nativeID from the
	// estate entirely (delete_volume, delete_snapshot, release_elastic_ip).
	// A missing resource is then treated as "already done" rather than an
	// error, which is what makes a retried delete idempotent.
	deleteAction bool

	// captureState reads the attributes this action cares about off the live
	// resource. false means the resource does not exist. Its return value
	// serves three purposes: the plan's Snapshot.Attributes, the rollback
	// step's Parameters (restore reads exactly this shape back), and the
	// existence check every other method is built on.
	captureState func(e *Estate, nativeID string) (map[string]any, bool)

	// extraPrecondition checks an action-specific safety condition beyond
	// "the resource exists" — e.g. delete_volume requires the volume to be
	// unattached. nil means there is none.
	extraPrecondition func(e *Estate, nativeID string) error

	// isApplied reports whether the resource already matches what params
	// asks for, which is what makes Apply's mutate step idempotent: calling
	// it twice with the same params mutates once and reports "already
	// applied, nothing to do" the second time.
	isApplied func(e *Estate, nativeID string, params map[string]any) bool

	// mutate performs the change. Called only when the resource exists, its
	// extra precondition (if any) passed, and isApplied said no — so it
	// never needs to re-check any of that itself. Called with the estate's
	// write lock held.
	mutate func(e *Estate, nativeID string, params map[string]any) (map[string]any, error)

	// restore reverses mutate using exactly the map captureState produced
	// before the change. Never called when rollbackFeasible is false.
	restore func(e *Estate, nativeID string, before map[string]any) error
}

// genericExecutor implements ports.Executor for one actionSpec.
type genericExecutor struct{ spec actionSpec }

var _ ports.Executor = (*genericExecutor)(nil)

// NewExecutors returns one Executor per mutating action this package
// implements, in no particular order.
func NewExecutors() []ports.Executor {
	specs := []actionSpec{
		resizeInstanceSpec, stopInstanceSpec, scheduleShutdownSpec, resizeRDSSpec, resizeNodeGroupSpec,
		deleteVolumeSpec, modifyVolumeTypeSpec, resizeVolumeSpec, deleteSnapshotSpec, releaseElasticIPSpec,
		applyS3LifecycleSpec, abortMultipartUploadsSpec, setLogRetentionSpec,
		resizeLambdaMemorySpec, removeProvisionedConcurrencySpec,
		createVPCEndpointSpec,
	}
	out := make([]ports.Executor, 0, len(specs))
	for _, s := range specs {
		out = append(out, &genericExecutor{spec: s})
	}
	return out
}

// NewExecutor returns the single executor for one action, if this package
// implements it.
func NewExecutor(action optimize.ActionType) (ports.Executor, bool) {
	for _, e := range NewExecutors() {
		if e.Action() == action {
			return e, true
		}
	}
	return nil, false
}

// Action reports the ActionType this executor handles.
func (g *genericExecutor) Action() optimize.ActionType { return g.spec.action }

// RequiredActions lists the IAM actions a real deployment would need.
func (g *genericExecutor) RequiredActions() []string { return g.spec.requiredActions }

// Plan builds the four-step forward plan (precondition, snapshot, mutate,
// verify) and its rollback plan, without mutating the estate. The plan's
// Snapshot and the rollback step's Parameters are both populated from the
// same captureState call, taken now, at Plan time — which is what lets
// Rollback restore exactly the state the plan was built against even if
// Apply's own snapshot step re-captures a moment later.
func (g *genericExecutor) Plan(ctx context.Context, in ports.ExecutionPlanInput) (execute.Plan, error) {
	estate, err := FromSession(in.Session, in.Resource.Region)
	if err != nil {
		return execute.Plan{}, err
	}
	nativeID := in.Resource.NativeID

	estate.mu.RLock()
	before, ok := g.spec.captureState(estate, nativeID)
	estate.mu.RUnlock()
	if !ok {
		return execute.Plan{}, core.NotFound(string(g.spec.kind), nativeID)
	}

	now := time.Now().UTC()
	planID := core.NewID("plan")
	baseKey := fmt.Sprintf("%s:%s", g.spec.action, nativeID)
	title := fmt.Sprintf(g.spec.titleFmt, nativeID)

	params := map[string]any{}
	for k, v := range in.Recommendation.Parameters {
		params[k] = v
	}

	steps := []execute.Step{
		{
			ID: core.NewID("step"), Ordinal: 1, Kind: execute.StepPrecondition, Name: "verify-preconditions",
			Describe:  fmt.Sprintf("confirm %s still matches the plan's assumptions", nativeID),
			AWSAction: g.spec.awsAction, Target: nativeID, IdempotencyKey: baseKey + ":precondition",
			State: execute.StepPending, AbortOnFailure: true,
		},
		{
			ID: core.NewID("step"), Ordinal: 2, Kind: execute.StepSnapshot, Name: "capture-state",
			Describe: "capture pre-change state for rollback", Target: nativeID,
			IdempotencyKey: baseKey + ":snapshot", State: execute.StepPending, AbortOnFailure: true,
		},
		{
			ID: core.NewID("step"), Ordinal: 3, Kind: execute.StepMutate, Name: string(g.spec.action),
			Describe: title, AWSAction: g.spec.awsAction, Target: nativeID, Parameters: params,
			IdempotencyKey: baseKey + ":mutate", State: execute.StepPending, AbortOnFailure: true,
		},
		{
			ID: core.NewID("step"), Ordinal: 4, Kind: execute.StepVerify, Name: "verify-change",
			Describe: "confirm the change took effect", Target: nativeID,
			IdempotencyKey: baseKey + ":verify", State: execute.StepPending, AbortOnFailure: false,
		},
	}

	snapshot := execute.Snapshot{
		ID: core.NewID("snap"), TenantID: in.TenantID, PlanID: planID, ResourceID: in.Resource.ID,
		ResourceARN: in.Resource.ARN, CapturedAt: now, Attributes: before, Digest: digestOf(before),
	}

	rollback := &execute.RollbackPlan{
		ID: core.NewID("rbplan"), TenantID: in.TenantID, PlanID: planID,
		Feasible: g.spec.rollbackFeasible, InfeasibleReason: g.spec.infeasibleReason,
		DataLossRisk: g.spec.dataLossRisk, EstimatedDuration: 30 * time.Second,
		Summary: fmt.Sprintf("restore %s to its state before %s", nativeID, g.spec.action), CreatedAt: now,
	}
	if g.spec.rollbackFeasible {
		rollback.Steps = []execute.Step{{
			ID: core.NewID("step"), Ordinal: 1, Kind: execute.StepMutate, Name: "rollback-" + string(g.spec.action),
			Describe: fmt.Sprintf("restore %s to its pre-change state", nativeID), AWSAction: g.spec.awsAction,
			Target: nativeID, Parameters: before, IdempotencyKey: baseKey + ":rollback",
			State: execute.StepPending, AbortOnFailure: true,
		}}
	}

	return execute.Plan{
		ID: planID, TenantID: in.TenantID, RecommendationID: in.Recommendation.ID, Action: g.spec.action,
		Title: title, AccountID: in.Account.AccountID, Region: in.Resource.Region,
		Environment: in.Resource.Environment, ResourceIDs: []core.ID{in.Resource.ID},
		Steps: steps, Snapshots: []execute.Snapshot{snapshot}, Rollback: rollback,
		ExpectedMonthlySaving: in.Recommendation.EstimatedMonthlySaving, BaselineMonthlyCost: in.Resource.MonthlyCost,
		State: execute.PlanDraft, DryRun: in.DryRun, RequestedBy: in.RequestedBy, CreatedAt: now,
	}, nil
}

// checkPreconditions is shared by Preflight and Apply's precondition step:
// the resource must exist (unless this is a delete action, in which case
// "already gone" is not a failure) and, if the spec declares one, its
// extra precondition must pass.
func (g *genericExecutor) checkPreconditions(estate *Estate, nativeID string) error {
	if _, ok := g.spec.captureState(estate, nativeID); !ok {
		if g.spec.deleteAction {
			return nil
		}
		return core.NotFound(string(g.spec.kind), nativeID)
	}
	if g.spec.extraPrecondition != nil {
		return g.spec.extraPrecondition(estate, nativeID)
	}
	return nil
}

// Preflight re-checks the plan's assumptions immediately before execution,
// however long ago the plan was approved.
func (g *genericExecutor) Preflight(ctx context.Context, session ports.AWSSession, plan execute.Plan) error {
	estate, err := FromSession(session, plan.Region)
	if err != nil {
		return err
	}
	nativeID := mutateTarget(plan)
	if nativeID == "" {
		return core.Invalid("plan %s has no mutate step to preflight", plan.ID)
	}
	estate.mu.RLock()
	defer estate.mu.RUnlock()
	return g.checkPreconditions(estate, nativeID)
}

// Apply performs one step. Only the mutate step actually changes AWS
// state; precondition, snapshot and verify steps read the estate and
// report what they see. The mutate step is where dry-run and idempotency
// are enforced: a dry run reports what would change without changing it,
// and a step whose target already matches its Parameters is reported as
// already applied rather than reapplied.
func (g *genericExecutor) Apply(ctx context.Context, session ports.AWSSession, plan execute.Plan, step execute.Step) (map[string]any, error) {
	estate, err := FromSession(session, plan.Region)
	if err != nil {
		return nil, err
	}

	switch step.Kind {
	case execute.StepPrecondition:
		estate.mu.RLock()
		defer estate.mu.RUnlock()
		if err := g.checkPreconditions(estate, step.Target); err != nil {
			return nil, err
		}
		return map[string]any{"checked": true}, nil

	case execute.StepSnapshot:
		estate.mu.RLock()
		defer estate.mu.RUnlock()
		before, ok := g.spec.captureState(estate, step.Target)
		if !ok {
			return map[string]any{"already_absent": true}, nil
		}
		return before, nil

	case execute.StepMutate:
		estate.mu.Lock()
		defer estate.mu.Unlock()
		if _, exists := g.spec.captureState(estate, step.Target); !exists {
			if g.spec.deleteAction {
				return map[string]any{"idempotent": true, "already_deleted": true}, nil
			}
			return nil, core.NotFound(string(g.spec.kind), step.Target)
		}
		if g.spec.extraPrecondition != nil {
			if err := g.spec.extraPrecondition(estate, step.Target); err != nil {
				return nil, err
			}
		}
		if g.spec.isApplied(estate, step.Target, step.Parameters) {
			return map[string]any{"idempotent": true}, nil
		}
		if plan.DryRun {
			return map[string]any{"dry_run": true, "would_apply": step.Parameters}, nil
		}
		return g.spec.mutate(estate, step.Target, step.Parameters)

	case execute.StepVerify:
		estate.mu.RLock()
		defer estate.mu.RUnlock()
		mutateParams := map[string]any{}
		if s, ok := firstStepOfKind(plan, execute.StepMutate); ok {
			mutateParams = s.Parameters
		}
		applied := g.spec.deleteAction
		if _, ok := g.spec.captureState(estate, step.Target); ok {
			applied = g.spec.isApplied(estate, step.Target, mutateParams)
		}
		return map[string]any{"applied": applied}, nil

	default:
		return map[string]any{}, nil
	}
}

// Rollback reverses one rollback step, restoring exactly the attributes
// the plan captured before the forward mutation ran. It refuses outright
// for actions the spec marked irreversible, the same way AWS itself
// refuses to un-delete a volume.
func (g *genericExecutor) Rollback(ctx context.Context, session ports.AWSSession, plan execute.Plan, step execute.Step) error {
	if !g.spec.rollbackFeasible {
		return fmt.Errorf("awssim: rollback of %s is infeasible: %s", g.spec.action, g.spec.infeasibleReason)
	}
	estate, err := FromSession(session, plan.Region)
	if err != nil {
		return err
	}
	estate.mu.Lock()
	defer estate.mu.Unlock()
	return g.spec.restore(estate, step.Target, step.Parameters)
}

// firstStepOfKind returns the first step of the given kind in a plan.
func firstStepOfKind(plan execute.Plan, kind execute.StepKind) (execute.Step, bool) {
	for _, s := range plan.Steps {
		if s.Kind == kind {
			return s, true
		}
	}
	return execute.Step{}, false
}

// mutateTarget returns the native id the plan's mutate step targets.
func mutateTarget(plan execute.Plan) string {
	if s, ok := firstStepOfKind(plan, execute.StepMutate); ok {
		return s.Target
	}
	return ""
}

// digestOf computes a short, deterministic fingerprint of a captured
// attribute map, used as execute.Snapshot.Digest. Keys are sorted first so
// the digest does not depend on Go's randomized map iteration order.
func digestOf(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%v;", k, m[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// paramStr reads a string parameter.
func paramStr(params map[string]any, key string) (string, bool) {
	v, ok := params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// paramFloat reads a numeric parameter, accepting any of the concrete
// numeric types a caller might reasonably hand in (a literal in a test, or
// a float64 as JSON unmarshaling always produces).
func paramFloat(params map[string]any, key string) (float64, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// paramInt reads an integer parameter via paramFloat, so the same set of
// concrete input types is accepted.
func paramInt(params map[string]any, key string) (int, bool) {
	f, ok := paramFloat(params, key)
	if !ok {
		return 0, false
	}
	return int(f), true
}
