// Package executor implements ports.Executor against real AWS accounts, one
// file per AWS service client (ec2.go, rds.go, eks.go, s3.go, lambda.go),
// mirroring internal/adapters/awssim's executor design: every action shares
// the same four-step shape — check the resource still matches what was
// planned, capture its prior state, change one thing about it, confirm the
// change stuck — so this file factors that shape into one generic engine
// (genericExecutor[C], parameterized over the narrow client interface C an
// action's service needs) driven by a small per-action table (spec[C])
// rather than writing the same Plan/Preflight/Apply/Rollback dance sixteen
// times.
//
// Two things are real here that the simulator could only pretend: mutate
// functions call the actual AWS API and, where AWS itself requires it (an
// EC2 resize needs the instance stopped first, an RDS class change needs the
// instance available again before it is considered done), they block on the
// SDK's own generated waiters — service_test.go fakes them by never blocking
// at all, but production traffic really does wait. And idempotency is a live
// concern, not a simulated one: every mutate step re-reads the resource's
// current state (captureState) and compares it against the desired
// parameters (isApplied) before issuing any mutating call, so a retried
// Apply on the same step is always safe — a change that already landed is
// detected and skipped, never reapplied. AWS's own DryRun request field
// exists on some EC2 calls but not uniformly across RDS, EKS, S3 or Lambda,
// so plan.DryRun is honored the same way for every action: the mutate step
// reports what it would do without calling AWS at all, which is a stronger
// and more uniform guarantee than relying on each service's own inconsistent
// dry-run support.
//
// Deletions (a volume, a snapshot, a released Elastic IP, aborted multipart
// parts) and any growth-only change AWS itself cannot reverse (EBS and RDS
// storage can be grown but never shrunk back) are marked rollback-infeasible
// — the approval screen states that plainly rather than pretending a delete,
// or a shrink AWS refuses to perform, can be undone. This is one deliberate
// point of divergence from awssim, whose in-memory Estate can freely shrink
// a volume back down: the real executor cannot, so resize_volume and
// modify_rds_storage are rollback-infeasible here even though awssim's
// equivalent specs mark rollback as feasible.
//
// One AWS action — set_log_retention — is a documented skip rather than an
// implementation: no CloudWatch Logs client is available among this
// codebase's allowed dependencies (see logs_unsupported.go), so there is no
// service call this file could make. Every other action awssim documents,
// plus modify_rds_storage (which targets RDS AllocatedStorage/StorageType
// and has no awssim equivalent at all — awssim's resize_rds_instance only
// ever touches DBInstanceClass), is implemented for real.
//
// Traceability: REQ-EXE-001..014, SPEC-AUTO-001.
package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	awssts "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// spec is the per-action table genericExecutor[C] is driven by, generic over
// C — the narrow client interface (ec2API, rdsAPI, eksAPI, s3API, lambdaAPI)
// the action's underlying service needs. Every field is either a fact about
// the action or a small function that knows how to read, check, change or
// restore exactly the attributes that action cares about; nothing here knows
// anything about plans, steps or snapshots, which is what keeps each
// concrete spec in ec2.go/rds.go/eks.go/s3.go/lambda.go down to a screenful.
type spec[C any] struct {
	action          optimize.ActionType
	requiredActions []string
	kind            cloud.Kind // the resource kind this action targets
	awsAction       string     // the underlying AWS API call, for the approval screen
	titleFmt        string     // fmt.Sprintf template taking the native ID

	rollbackFeasible bool
	infeasibleReason string         // required when rollbackFeasible is false
	dataLossRisk     core.RiskLevel // carried on the rollback plan

	// deleteAction marks actions whose mutate step removes nativeID from AWS
	// entirely (delete_volume, delete_snapshot, release_elastic_ip,
	// remove_provisioned_concurrency). A missing resource is then treated as
	// "already done" rather than an error, which is what makes a retried
	// delete idempotent.
	deleteAction bool

	// identityParams lets a spec bake extra identifying fields the
	// resource's own NativeID does not carry — e.g. resize_node_group needs
	// the EKS cluster name alongside the node group's own NativeID, which
	// discovery stores as a plain resource attribute rather than folding
	// into NativeID (see eks.go's own doc comment). nil for every action
	// whose NativeID alone is enough to address the resource.
	identityParams func(r cloud.Resource) map[string]any

	// captureState reads the attributes this action cares about off the live
	// AWS resource, given the merged parameters (which may carry
	// identityParams' extras). false means the resource does not exist. Its
	// return value serves three purposes: the plan's Snapshot.Attributes,
	// the rollback step's Parameters (restore reads exactly this shape
	// back), and the existence check every other method is built on — so
	// captureState should copy forward into its result any identity field
	// (e.g. "cluster_name") that restore will need later, since restore
	// receives only what captureState returned, not params.
	captureState func(ctx context.Context, c C, nativeID string, params map[string]any, region core.Region) (map[string]any, bool, error)

	// extraPrecondition checks an action-specific safety condition beyond
	// "the resource exists", purely from the already-captured state — e.g.
	// delete_volume requires the volume to be unattached. nil means there is
	// none.
	extraPrecondition func(current map[string]any) error

	// isApplied reports whether the resource already matches what params
	// asks for, purely from the already-captured state — which is what
	// makes Apply's mutate step idempotent: calling it twice with the same
	// params mutates once and reports "already applied, nothing to do" the
	// second time. Delete-style actions always return false here (existence
	// alone, checked before isApplied is ever consulted, is what makes
	// those idempotent).
	isApplied func(current, params map[string]any) bool

	// mutate performs the change, including blocking on any AWS waiter the
	// operation itself requires before the change is considered landed.
	// Called only when the resource exists, its extra precondition (if any)
	// passed, and isApplied said no — so it never needs to re-check any of
	// that itself, and never needs to honor plan.DryRun itself since the
	// generic engine never calls it on a dry run.
	mutate func(ctx context.Context, c C, nativeID string, params map[string]any, region core.Region) (map[string]any, error)

	// restore reverses mutate using exactly the map captureState produced
	// before the change (including any identity fields captureState copied
	// forward). Never called when rollbackFeasible is false.
	restore func(ctx context.Context, c C, nativeID string, before map[string]any, region core.Region) error
}

// genericExecutor implements ports.Executor for one spec[C].
type genericExecutor[C any] struct {
	spec      spec[C]
	newClient func(cfg any) C
}

func (g *genericExecutor[C]) Action() optimize.ActionType { return g.spec.action }
func (g *genericExecutor[C]) RequiredActions() []string   { return g.spec.requiredActions }

// client resolves the aws.Config for session/region and builds this
// executor's narrow client from it. The generic parameter is a config
// factory rather than a concrete aws.Config type so this file stays free of
// an AWS SDK import — each concrete newClient (in ec2.go etc.) does the real
// type assertion.
func (g *genericExecutor[C]) client(session ports.AWSSession, region core.Region) (C, error) {
	var zero C
	cfg, err := awssts.FromSession(session, region)
	if err != nil {
		return zero, err
	}
	return g.newClient(cfg), nil
}

// buildParams merges a recommendation's typed parameters with any
// identity extras this action's spec needs, which is what lets captureState
// and mutate address a resource whose NativeID alone (e.g. an EKS node
// group's bare name) is not enough to call the AWS API with.
func (g *genericExecutor[C]) buildParams(rec optimize.Recommendation, r cloud.Resource) map[string]any {
	params := map[string]any{}
	for k, v := range rec.Parameters {
		params[k] = v
	}
	if g.spec.identityParams != nil {
		for k, v := range g.spec.identityParams(r) {
			params[k] = v
		}
	}
	return params
}

// Plan builds the four-step forward plan (precondition, snapshot, mutate,
// verify) and its rollback plan, without mutating AWS. The plan's Snapshot
// and the rollback step's Parameters are both populated from the same
// captureState call, taken now, at Plan time — which is what lets Rollback
// restore exactly the state the plan was built against even if Apply's own
// snapshot step re-captures a moment later.
func (g *genericExecutor[C]) Plan(ctx context.Context, in ports.ExecutionPlanInput) (execute.Plan, error) {
	client, err := g.client(in.Session, in.Resource.Region)
	if err != nil {
		return execute.Plan{}, err
	}
	nativeID := in.Resource.NativeID
	params := g.buildParams(in.Recommendation, in.Resource)

	before, ok, err := g.spec.captureState(ctx, client, nativeID, params, in.Resource.Region)
	if err != nil {
		return execute.Plan{}, err
	}
	if !ok {
		return execute.Plan{}, core.NotFound(string(g.spec.kind), nativeID)
	}

	now := time.Now().UTC()
	planID := core.NewID("plan")
	baseKey := fmt.Sprintf("%s:%s", g.spec.action, nativeID)
	title := fmt.Sprintf(g.spec.titleFmt, nativeID)

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
// "already gone" is not a failure) and, if the spec declares one, its extra
// precondition must pass.
func (g *genericExecutor[C]) checkPreconditions(ctx context.Context, client C, nativeID string, params map[string]any, region core.Region) error {
	current, ok, err := g.spec.captureState(ctx, client, nativeID, params, region)
	if err != nil {
		return err
	}
	if !ok {
		if g.spec.deleteAction {
			return nil
		}
		return core.NotFound(string(g.spec.kind), nativeID)
	}
	if g.spec.extraPrecondition != nil {
		return g.spec.extraPrecondition(current)
	}
	return nil
}

// mutateParams returns the mutate step's Parameters — the one place in the
// plan that carries both the recommendation's own inputs and any identity
// extras Plan baked in via identityParams, which is why Preflight and every
// non-mutate Apply case reads params from here rather than from the step
// they were actually called for.
func mutateParams(plan execute.Plan) map[string]any {
	if s, ok := firstStepOfKind(plan, execute.StepMutate); ok {
		return s.Parameters
	}
	return map[string]any{}
}

// Preflight re-checks the plan's assumptions immediately before execution,
// however long ago the plan was approved.
func (g *genericExecutor[C]) Preflight(ctx context.Context, session ports.AWSSession, plan execute.Plan) error {
	client, err := g.client(session, plan.Region)
	if err != nil {
		return err
	}
	nativeID := mutateTarget(plan)
	if nativeID == "" {
		return core.Invalid("plan %s has no mutate step to preflight", plan.ID)
	}
	return g.checkPreconditions(ctx, client, nativeID, mutateParams(plan), plan.Region)
}

// Apply performs one step. Only the mutate step actually changes AWS state;
// precondition, snapshot and verify steps read AWS and report what they see.
// The mutate step is where dry-run and idempotency are enforced: a dry run
// reports what would change without calling AWS at all, and a step whose
// target already matches its Parameters is reported as already applied
// rather than reapplied.
func (g *genericExecutor[C]) Apply(ctx context.Context, session ports.AWSSession, plan execute.Plan, step execute.Step) (map[string]any, error) {
	client, err := g.client(session, plan.Region)
	if err != nil {
		return nil, err
	}
	params := mutateParams(plan)

	switch step.Kind {
	case execute.StepPrecondition:
		if err := g.checkPreconditions(ctx, client, step.Target, params, plan.Region); err != nil {
			return nil, err
		}
		return map[string]any{"checked": true}, nil

	case execute.StepSnapshot:
		before, ok, err := g.spec.captureState(ctx, client, step.Target, params, plan.Region)
		if err != nil {
			return nil, err
		}
		if !ok {
			return map[string]any{"already_absent": true}, nil
		}
		return before, nil

	case execute.StepMutate:
		current, exists, err := g.spec.captureState(ctx, client, step.Target, params, plan.Region)
		if err != nil {
			return nil, err
		}
		if !exists {
			if g.spec.deleteAction {
				return map[string]any{"idempotent": true, "already_deleted": true}, nil
			}
			return nil, core.NotFound(string(g.spec.kind), step.Target)
		}
		if g.spec.extraPrecondition != nil {
			if err := g.spec.extraPrecondition(current); err != nil {
				return nil, err
			}
		}
		if g.spec.isApplied(current, step.Parameters) {
			return map[string]any{"idempotent": true}, nil
		}
		if plan.DryRun {
			return map[string]any{"dry_run": true, "would_apply": step.Parameters}, nil
		}
		return g.spec.mutate(ctx, client, step.Target, step.Parameters, plan.Region)

	case execute.StepVerify:
		applied := g.spec.deleteAction
		current, exists, err := g.spec.captureState(ctx, client, step.Target, params, plan.Region)
		if err != nil {
			return nil, err
		}
		if exists {
			applied = g.spec.isApplied(current, params)
		}
		return map[string]any{"applied": applied}, nil

	default:
		return map[string]any{}, nil
	}
}

// Rollback reverses one rollback step, restoring exactly the attributes the
// plan captured before the forward mutation ran. It refuses outright for
// actions the spec marked irreversible, the same way AWS itself refuses to
// un-delete a volume or shrink one back down.
func (g *genericExecutor[C]) Rollback(ctx context.Context, session ports.AWSSession, plan execute.Plan, step execute.Step) error {
	if !g.spec.rollbackFeasible {
		return fmt.Errorf("aws executor: rollback of %s is infeasible: %s", g.spec.action, g.spec.infeasibleReason)
	}
	client, err := g.client(session, plan.Region)
	if err != nil {
		return err
	}
	return g.spec.restore(ctx, client, step.Target, step.Parameters, plan.Region)
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
// numeric types a caller might reasonably hand in (a literal in a test, or a
// float64 as JSON unmarshaling always produces).
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
	case int32:
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

// paramBool reads a boolean parameter, accepting a real bool or the strings
// "true"/"false" (a recommendation's Parameters map is JSON in transit, and
// some callers round-trip a bool as its string form).
func paramBool(params map[string]any, key string) (bool, bool) {
	v, ok := params[key]
	if !ok {
		return false, false
	}
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch b {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// ctxWithTimeout bounds one blocking AWS call or waiter. A single Apply call
// must never hang the whole execution engine waiting on one stuck resource.
func ctxWithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
