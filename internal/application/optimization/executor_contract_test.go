package optimization

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	awsexecutor "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/executor"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/awssim"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
	rulepack "github.com/udaykishore-resu/cloudoptix/rules"
)

// This file is the contract test between the rules in this package and the
// executors that carry their recommendations out.
//
// It exists because five rule/executor pairs had silently drifted apart.
// optimize.Recommendation's own doc comment states the contract — parameter
// keys are the EXECUTOR's vocabulary, not the rule's — but nothing asserted
// it, so a rule could invent a spelling (target_instance_type, services,
// active_hours_utc, target_class, target_storage_type) and the
// recommendation would pass validation, pass policy, pass approval, pass
// preflight, and only then fail at the mutate step with a missing-parameter
// error. Two of the five did not even fail: they applied an S3 lifecycle
// rule containing no clause at all and reported success with a saving
// attached, which is worse than failing.
//
// The test lives in this package rather than under tests/ so it can use the
// same fixtures the rule unit tests use; importing the two executor
// adapters from a _test file introduces no dependency in the shipped binary
// and no import cycle, since neither adapter imports this package.
//
// Traceability: REQ-OPT-016, SPEC-OPT-010.

// actionsWithNoExecutorYet enumerates the actions CloudOptix recognises and
// recommends but cannot yet perform. optimize.ActionType's own doc comment
// describes that state and what it means for the product: "A recommendation
// whose action type has no executor can be approved but never executed, and
// the UI says so plainly."
//
// It is written out rather than inferred as "whatever has no executor",
// precisely so it cannot grow by accident. Shipping an action ahead of its
// executor is a legitimate thing to do; doing it without anyone noticing is
// not, and editing this map is the deliberate act the test asks for in
// exchange.
var actionsWithNoExecutorYet = map[optimize.ActionType]string{
	optimize.ActionTerminateInstance:   "no rule proposes outright termination; stop_instance is the reversible form CloudOptix recommends instead",
	optimize.ActionDeregisterAMI:       "deregistering an image and cleaning up its backing snapshots is a two-step operation no executor implements yet",
	optimize.ActionRemoveRDSReplica:    "removing a read replica requires the application's own read routing to change first",
	optimize.ActionStopRDS:             "a stopped RDS instance restarts itself after seven days, so this needs a re-stop scheduler before it can be honest",
	optimize.ActionEnableSpot:          "moving capacity to Spot is a launch-template and capacity-rebalance change, not one API call",
	optimize.ActionPurchaseCommitment:  "buying a commitment spends money on a multi-year term; CloudOptix recommends it and deliberately holds no credential able to do it",
	optimize.ActionSwitchDynamoBilling: "switching billing mode is limited to once per 24h per table and needs a cooldown ledger no executor keeps yet",
	optimize.ActionSwitchLambdaArch:    "an architecture switch needs the function's deployment package rebuilt for arm64 first",
	optimize.ActionRemoveNATGateway:    "removing a NAT gateway means rewriting every route table that points at it",
	optimize.ActionAdjustPodResources:  "pod resource requests live in the customer's Kubernetes manifests, which CloudOptix does not write to",
}

// rulesUnreachableAtShippedPrices names rules that can never fire against
// the price book this repository ships, so no fixture can make them produce
// a recommendation to check. The entry states why, because "no fixture" and
// "no possible fixture" are very different facts and only the second one is
// acceptable.
var rulesUnreachableAtShippedPrices = map[optimize.RuleID]string{
	RuleIDRDSGp2Gp3: "the price book carries the same per-GiB rate for RDS gp2 and gp3 ($0.115), " +
		"which is what AWS actually charges; the real gp3 saving comes from no longer paying for " +
		"provisioned IOPS, a dimension this rule does not price. It therefore computes a zero " +
		"saving and correctly declines on every input.",
}

// executorRegistered reports whether either adapter implements an action.
// Either, not both: set_log_retention is implemented in the simulator and
// deliberately absent from the live executor (see
// aws/executor/logs_unsupported.go), and modify_rds_storage is the mirror
// case, so requiring both would assert something neither adapter claims.
func executorRegistered(a optimize.ActionType) bool {
	if _, ok := awssim.NewExecutor(a); ok {
		return true
	}
	_, ok := awsexecutor.NewExecutor(a)
	return ok
}

func contractRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewDefaultRegistry(nil)
	require.NoError(t, err)
	return reg
}

// TestEveryRuleActionIsAdvisoryOrExecutableOrKnownUnbuilt is the static half
// of the contract: every registered rule's declared action falls into
// exactly one of three states, and the third one has to be written down.
func TestEveryRuleActionIsAdvisoryOrExecutableOrKnownUnbuilt(t *testing.T) {
	for _, rule := range contractRegistry(t).Rules() {
		action := rule.Info().Action
		t.Run(string(rule.ID()), func(t *testing.T) {
			if action == optimize.ActionAdvisoryOnly {
				return // architecture advice a human applies; nothing to execute
			}
			_, hasContract := optimize.ParameterContractFor(action)
			if executorRegistered(action) {
				assert.True(t, hasContract,
					"action %s has an executor but no declared parameter contract; add one to "+
						"optimize.actionParameterContracts so rules can be checked against it", action)
				return
			}
			_, known := actionsWithNoExecutorYet[action]
			assert.True(t, known,
				"rule %s emits action %s, which no executor implements and which is not listed as "+
					"known-unbuilt. Either build the executor, make the rule advisory_only, or add the "+
					"action to actionsWithNoExecutorYet with the reason it cannot be executed yet.",
				rule.ID(), action)
			assert.False(t, hasContract,
				"action %s declares a parameter contract that no executor reads", action)
		})
	}
}

// TestParameterContractsAndExecutorsAgree closes the drift in the other
// direction: a contract declared for an action nothing implements is a
// contract nobody checks, and would let the test above pass on a promise.
func TestParameterContractsAndExecutorsAgree(t *testing.T) {
	for _, action := range optimize.ExecutableActions() {
		assert.True(t, executorRegistered(action),
			"a parameter contract is declared for %s but no adapter registers an executor for it", action)
		_, known := actionsWithNoExecutorYet[action]
		assert.False(t, known,
			"%s is listed as having no executor yet, but one is registered; remove it from actionsWithNoExecutorYet", action)
	}
}

// TestEveryRuleSatisfiesItsExecutorParameterContract is the dynamic half: it
// walks every registered rule, builds a representative recommendation from a
// resource shaped to make that rule fire, and asserts the parameters the
// rule emits are ones the executor for its action can actually consume.
//
// A rule whose action has an executor MUST have a fixture. That requirement
// is the anti-drift property: a rule that happens not to fire on today's
// demo estate is exactly the rule whose parameter shape nobody would notice
// was wrong, so an absent fixture fails loudly rather than passing quietly.
// Rules whose action is advisory or has no executor need no fixture — there
// is no parameter shape to get wrong — and the static test above already
// covers them.
func TestEveryRuleSatisfiesItsExecutorParameterContract(t *testing.T) {
	fixtures := contractFixtures()
	for _, rule := range contractRegistry(t).Rules() {
		rule := rule
		t.Run(string(rule.ID()), func(t *testing.T) {
			if why, unreachable := rulesUnreachableAtShippedPrices[rule.ID()]; unreachable {
				_, hasFixture := fixtures[rule.ID()]
				require.False(t, hasFixture,
					"rule %s is listed as unreachable at shipped prices but has a fixture; "+
						"if it can fire, remove the exemption", rule.ID())
				t.Skipf("%s cannot produce a recommendation at the shipped prices: %s", rule.ID(), why)
			}

			build, ok := fixtures[rule.ID()]
			if !ok {
				require.False(t, executorRegistered(rule.Info().Action),
					"rule %s emits %s, which an executor implements, but has no fixture in "+
						"contractFixtures — its parameter shape is therefore never checked against "+
						"that executor. Add one.", rule.ID(), rule.Info().Action)
				return
			}

			ctx, resource := build()
			require.True(t, rule.Applies(resource),
				"the fixture for %s builds a resource the rule does not even apply to", rule.ID())
			findings, err := rule.Evaluate(ctx, resource)
			require.NoError(t, err)
			require.NotEmpty(t, findings,
				"the fixture for %s no longer makes the rule fire, so its action is never checked; "+
					"fix the fixture rather than deleting the case", rule.ID())

			action := rule.BuildAction(ctx, resource, findings[0])
			require.NotEqual(t, optimize.ActionType(""), action.Type,
				"a fired rule must always name an action, even if only advisory_only")
			if action.Type == optimize.ActionAdvisoryOnly {
				return
			}
			require.Equal(t, rule.Info().Action, action.Type,
				"the rule catalogue advertises %s but the built action is %s; the two must agree or "+
					"policy rules that target an action by name will not match what actually runs",
				rule.Info().Action, action.Type)

			contract, declared := optimize.ParameterContractFor(action.Type)
			require.True(t, declared,
				"rule %s built action %s, which has no declared parameter contract", rule.ID(), action.Type)
			satisfied, why := contract.Satisfied(action.Type, action.Parameters)
			assert.True(t, satisfied, "rule %s: %s", rule.ID(), why)
		})
	}
}

// --- fixtures ---------------------------------------------------------------

// contractCase builds one rule's representative evaluation context and the
// resource it fires on.
type contractCase func() (EvalContext, cloud.Resource)

// metricsFor is the one-resource metrics map every fixture needs, with a
// window long enough to clear the longest min_window_hours any rule
// declares (336h, ec2-never-used-instance).
func contractMetrics(id core.ID, m ports.ResourceMetrics) map[core.ID]ports.ResourceMetrics {
	m.ResourceID = id
	if m.Coverage == 0 {
		m.Coverage = 1.0
	}
	if m.Window.Start.IsZero() {
		m.Window = core.PeriodOfDays(testNow, 30)
	}
	return map[core.ID]ports.ResourceMetrics{id: m}
}

// contractCtx wires one resource (plus any companions it needs in the
// inventory) and its metrics into an EvalContext.
func contractCtx(r cloud.Resource, metrics map[core.ID]ports.ResourceMetrics, sp spec.Spec, companions ...cloud.Resource) EvalContext {
	inv := cloud.NewInventory(append([]cloud.Resource{r}, companions...))
	return testEvalContext(inv, nil, metrics, sp)
}

func contractFixtures() map[optimize.RuleID]contractCase {
	return map[optimize.RuleID]contractCase{
		RuleIDEC2Rightsize: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEC2Instance, "m5.4xlarge")
			r.MonthlyCost = core.USDollars(560)
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{CPU: pct(4, 8, 10, 5)}), testSpec()), r
		},

		RuleIDEC2OversizedDeclared: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEC2Instance, "m5.4xlarge")
			r.Name = "nightly-batch"
			r.MonthlyCost = core.USDollars(560)
			sp := testSpec()
			sp.Workloads = []spec.Workload{{Name: "nightly-batch", Type: "batch"}}
			return contractCtx(r, nil, sp), r
		},

		RuleIDEC2PrevGeneration: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEC2Instance, "m4.2xlarge")
			r.MonthlyCost = core.USDollars(290)
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{CPU: pct(30, 45, 50, 33)}), testSpec()), r
		},

		RuleIDEC2BurstCredit: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEC2Instance, "t3.2xlarge")
			r.MonthlyCost = core.USDollars(600) // sustained burst billing well above a fixed instance
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{CPU: pct(60, 75, 85, 62)}), testSpec()), r
		},

		RuleIDEC2NeverUsed: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEC2Instance, "m5.large")
			r.MonthlyCost = core.USDollars(70)
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{CPU: pct(0.4, 0.8, 1.1, 0.5)}), testSpec()), r
		},

		RuleIDEC2ScheduleOffHours: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEC2Instance, "m5.xlarge")
			r.Environment = core.EnvStaging
			r.MonthlyCost = core.USDollars(140)
			cpu := pct(2, 40, 60, 12)
			cpu.Seasonal = true
			cpu.PeakHours = []int{8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{CPU: cpu}), testSpec()), r
		},

		RuleIDEBSUnattached: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEBSVolume, "gp3")
			r.State = cloud.StateAvailable
			r.FirstSeenAt = testNow.Add(-45 * 24 * time.Hour)
			r.Capacity.StorageGiB = 500
			r.MonthlyCost = core.USDollars(40)
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDEBSOverprovisioned: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEBSVolume, "gp3")
			r.State = cloud.StateInUse
			r.Capacity.StorageGiB = 2000
			r.MonthlyCost = core.USDollars(160)
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{DiskUsed: pct(8, 12, 15, 9)}), testSpec()), r
		},

		RuleIDEBSGp2Gp3: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEBSVolume, "gp2")
			r.State = cloud.StateInUse
			r.Capacity.StorageGiB = 1000
			r.Capacity.ThroughputMiBps = 60
			r.MonthlyCost = core.USDollars(100)
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDEBSOrphanedSnapshot: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEBSSnapshot, "")
			r.State = cloud.StateAvailable
			r.CreatedAt = testNow.Add(-200 * 24 * time.Hour)
			// A volume_id naming a volume the inventory no longer holds is
			// exactly the orphan condition the rule looks for.
			r.Attributes = map[string]string{"volume_id": "vol-deleted-long-ago"}
			r.MonthlyCost = core.USDollars(12)
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDEBSSnapshotRetention: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEBSSnapshot, "")
			r.State = cloud.StateAvailable
			r.CreatedAt = testNow.Add(-400 * 24 * time.Hour)
			r.MonthlyCost = core.USDollars(9)
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDEIPUnattached: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindElasticIP, "")
			r.MonthlyCost = core.USDollars(4)
			r.State = cloud.StateAvailable
			r.FirstSeenAt = testNow.Add(-60 * 24 * time.Hour)
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDRDSOversized: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindRDSInstance, "db.r5.8xlarge")
			r.MonthlyCost = core.USDollars(9000)
			r.Engine = "postgres"
			r.Attributes = map[string]string{"multi_az": "true"}
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{CPU: pct(8, 15, 20, 9)}), testSpec()), r
		},

		RuleIDEKSConsolidation: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindEKSNodeGroup, "m5.2xlarge")
			r.MonthlyCost = core.USDollars(11200)
			r.Capacity.InstanceCount = 40
			r.Attributes = map[string]string{"packed_fraction": "0.30"}
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDS3NoLifecycle: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindS3Bucket, "")
			r.MonthlyCost = core.USDollars(4600)
			r.Capacity.StorageGiB = 20000
			r.Capacity.ObjectCount = 5_000_000
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDS3NoncurrentVersions: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindS3Bucket, "")
			r.MonthlyCost = core.USDollars(900)
			r.Attributes = map[string]string{
				"versioning_enabled":      "true",
				"non_current_version_gib": "2500",
			}
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDS3IntelligentTiering: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindS3Bucket, "")
			// Large average object size (10 GiB/object) clears the
			// monitoring-fee break-even by a wide margin.
			r.MonthlyCost = core.USDollars(11500)
			r.Capacity.StorageGiB = 50000
			r.Capacity.ObjectCount = 5000
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDS3WrongStorageClass: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindS3Bucket, "")
			r.MonthlyCost = core.USDollars(1840)
			r.Capacity.StorageGiB = 8000
			r.Capacity.ObjectCount = 2_000_000
			r.Attributes = map[string]string{"storage_class": "standard"}
			// A near-zero request rate over two million objects is well under
			// one access per object per ten months.
			reqs := pct(0.0001, 0.0002, 0.0003, 0.0001)
			return contractCtx(r, contractMetrics(r.ID, ports.ResourceMetrics{Requests: reqs}), testSpec()), r
		},

		RuleIDS3IncompleteMultipart: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindS3Bucket, "")
			r.MonthlyCost = core.USDollars(300)
			r.Attributes = map[string]string{
				"incomplete_multipart_count": "180",
				"incomplete_multipart_gib":   "900",
			}
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDCWLogRetention: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindLogGroup, "")
			r.MonthlyCost = core.USDollars(120)
			r.Capacity.StorageGiB = 4000
			r.Capacity.RetentionDays = 0
			r.FirstSeenAt = testNow.Add(-600 * 24 * time.Hour)
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDLambdaMemoryCostCurve: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindLambdaFunction, "")
			r.MonthlyCost = core.USDollars(1600)
			r.Capacity.MemoryMB = 3008
			r.Attributes = map[string]string{
				"avg_duration_ms":       "80",
				"invocations_per_month": "40000000",
				"architecture":          "x86_64",
			}
			return contractCtx(r, nil, testSpec()), r
		},

		RuleIDLambdaUnusedProvisionedConcurrency: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindLambdaFunction, "")
			r.MonthlyCost = core.USDollars(1400)
			r.Capacity.MemoryMB = 2048
			r.Capacity.Concurrency = 100
			// Requests is present because ResourceMetrics.HasSignal counts
			// only CPU/Memory/Requests/IOPS as evidence a resource was
			// observed at all; a concurrency series on its own reads as no
			// telemetry, and the rule's sufficiency guard would decline.
			m := ports.ResourceMetrics{Concurrency: pct(2, 4, 6, 3), Requests: pct(1, 3, 5, 2)}
			return contractCtx(r, contractMetrics(r.ID, m), testSpec()), r
		},

		RuleIDNATVPCEndpoint: func() (EvalContext, cloud.Resource) {
			r := mkResource(cloud.KindNATGateway, "")
			r.MonthlyCost = core.USDollars(1900)
			r.State = cloud.StateAvailable
			r.Attributes = map[string]string{
				"gb_processed_month":           "40000",
				"s3_dynamodb_traffic_fraction": "0.8",
				"vpc_id":                       "vpc-demo",
			}
			return contractCtx(r, nil, testSpec()), r
		},
	}
}

// --- round trip -------------------------------------------------------------

// TestRepairedPairsRoundTripThroughTheSimulatedExecutor drives the actual
// parameter maps four repaired rules emit all the way through the simulated
// executor for their action, and asserts the estate changed in the way the
// recommendation promised.
//
// The contract test above proves the required keys are present. This proves
// the values in them are ones the executor can act on — which is the half
// that was really broken: "services": []string{"s3","dynamodb"} would have
// satisfied a bare key-presence check for a differently-named key, and
// "target_class": "standard_ia" produced a lifecycle rule that was valid,
// enabled, and did nothing.
func TestRepairedPairsRoundTripThroughTheSimulatedExecutor(t *testing.T) {
	fixtures := contractFixtures()

	cases := []struct {
		rule optimize.RuleID
		// seed populates a simulated estate with the resource the executor
		// will look for, and returns the native id to target.
		seed func(*awssim.Estate) string
		// assertApplied checks the estate actually moved.
		assertApplied func(*testing.T, *awssim.Estate, string)
	}{
		{
			rule: RuleIDEC2ScheduleOffHours,
			seed: func(e *awssim.Estate) string {
				e.EC2Instances["i-roundtrip"] = &awssim.EC2Instance{
					Base:         awssim.Base{ID: "i-roundtrip", Region: regionUSEast1, State: cloud.StateRunning, Tags: core.Tags{}},
					InstanceType: "m5.xlarge", Platform: "linux",
				}
				return "i-roundtrip"
			},
			assertApplied: func(t *testing.T, e *awssim.Estate, id string) {
				inst := e.EC2Instances[id]
				require.NotNil(t, inst)
				assert.Equal(t, cloud.StateStopped, inst.State)
				// The tag must carry the schedule this instance's own
				// telemetry produced, not the executor's generic default —
				// that substitution is exactly what the missing "schedule"
				// key used to cause, silently.
				assert.Equal(t, "run 08:00-18:00 UTC daily; stopped otherwise", inst.Tags["cloudoptix:schedule"])
			},
		},
		{
			rule: RuleIDNATVPCEndpoint,
			seed: func(e *awssim.Estate) string {
				e.Subnets["subnet-rt"] = &awssim.Subnet{
					Base:  awssim.Base{ID: "subnet-rt", Region: regionUSEast1, Tags: core.Tags{}},
					VPCID: "vpc-rt",
				}
				e.NATGateways["nat-roundtrip"] = &awssim.NATGateway{
					Base:     awssim.Base{ID: "nat-roundtrip", Region: regionUSEast1, AZ: "us-east-1a", Tags: core.Tags{}},
					SubnetID: "subnet-rt", GBProcessedPerMonth: 40000,
				}
				return "nat-roundtrip"
			},
			assertApplied: func(t *testing.T, e *awssim.Estate, id string) {
				ep, ok := e.VPCEndpoints["vpce-"+id]
				require.True(t, ok, "the endpoint the recommendation proposed must exist")
				assert.Equal(t, "com.amazonaws.us-east-1.s3", ep.ServiceName,
					"the executor must create the service the rule named, not its own fallback")
				assert.Less(t, e.NATGateways[id].GBProcessedPerMonth, 40000.0)
			},
		},
		{
			rule: RuleIDS3NoLifecycle,
			seed: func(e *awssim.Estate) string {
				e.S3Buckets["bucket-roundtrip"] = &awssim.S3Bucket{
					Base:       awssim.Base{ID: "bucket-roundtrip", Region: regionUSEast1, Tags: core.Tags{}},
					StorageGiB: map[string]float64{"standard": 20000},
				}
				return "bucket-roundtrip"
			},
			assertApplied: func(t *testing.T, e *awssim.Estate, id string) {
				b := e.S3Buckets[id]
				require.NotNil(t, b)
				assert.Greater(t, b.StorageGiB["standard_ia"], 0.0,
					"the transition the recommendation proposed must actually move bytes")
				assert.Less(t, b.StorageGiB["standard"], 20000.0)
			},
		},
		{
			rule: RuleIDS3NoncurrentVersions,
			seed: func(e *awssim.Estate) string {
				e.S3Buckets["bucket-versions"] = &awssim.S3Bucket{
					Base:                 awssim.Base{ID: "bucket-versions", Region: regionUSEast1, Tags: core.Tags{}},
					StorageGiB:           map[string]float64{"standard": 1000},
					NonCurrentVersionGiB: 2500,
				}
				return "bucket-versions"
			},
			assertApplied: func(t *testing.T, e *awssim.Estate, id string) {
				b := e.S3Buckets[id]
				require.NotNil(t, b)
				assert.Zero(t, b.NonCurrentVersionGiB)
				assert.Equal(t, 1000.0, b.StorageGiB["standard"],
					"expiring non-current versions must not touch the bucket's current objects")
			},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.rule), func(t *testing.T) {
			build, ok := fixtures[tc.rule]
			require.True(t, ok)
			ctx, resource := build()
			rule, ok := contractRegistry(t).Get(tc.rule)
			require.True(t, ok)
			findings, err := rule.Evaluate(ctx, resource)
			require.NoError(t, err)
			require.NotEmpty(t, findings)
			action := rule.BuildAction(ctx, resource, findings[0])

			estate := awssim.NewEstate(core.AccountID("111111111111"), "round-trip",
				[]core.Region{regionUSEast1}, pricing.New())
			nativeID := tc.seed(estate)
			session := awssim.NewSession(estate, cloud.ScopeExecute, time.Hour)

			exec, ok := awssim.NewExecutor(action.Type)
			require.True(t, ok, "no simulated executor for %s", action.Type)

			target := resource
			target.NativeID = nativeID
			target.Region = regionUSEast1
			plan, err := exec.Plan(t.Context(), ports.ExecutionPlanInput{
				TenantID: testTenant,
				Recommendation: optimize.Recommendation{
					ID: core.NewID("rec"), Action: action.Type, Parameters: action.Parameters,
				},
				Resource: target,
				Account:  cloud.AWSAccount{AccountID: estate.AccountID},
				Session:  session, RequestedBy: "contract-test",
			})
			require.NoError(t, err)

			for _, step := range plan.Steps {
				_, err := exec.Apply(t.Context(), session, plan, step)
				require.NoError(t, err, "step %s (%s) rejected the parameters the rule emitted", step.Name, step.Kind)
			}
			tc.assertApplied(t, estate, nativeID)
		})
	}
}

// TestRulePackAndRuleCodeAgreeOnAction closes the third place an action can
// drift. The YAML pack is what the rules catalogue in the docs and the
// policy-authoring UI read to tell an operator which verb a rule produces,
// and a policy rule that targets an action by name matches against what
// actually runs. A pack saying resize_node_group where the code emits
// advisory_only would have an operator write a policy for a change that
// never arrives.
func TestRulePackAndRuleCodeAgreeOnAction(t *testing.T) {
	pack, err := rulepack.Load()
	require.NoError(t, err)
	for _, rule := range contractRegistry(t).Rules() {
		def, ok := pack.Defs[string(rule.ID())]
		require.True(t, ok, "rule %s has no entry in the YAML pack", rule.ID())
		assert.Equal(t, def.Action, string(rule.Info().Action),
			"rule %s: the pack declares %q and the code emits %q", rule.ID(), def.Action, rule.Info().Action)
		assert.Equal(t, def.Category, string(rule.Info().Category),
			"rule %s: the pack declares category %q and the code reports %q", rule.ID(), def.Category, rule.Info().Category)
	}
}
