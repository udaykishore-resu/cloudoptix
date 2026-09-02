package awssim

import (
	"encoding/json"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// defaultSimLifecycleRuleID mirrors the live executor's own default rule id
// (internal/adapters/aws/executor/s3.go), so a recommendation that omits
// rule_id lands on the same managed rule in the simulator as it would on
// real S3.
const defaultSimLifecycleRuleID = "cloudoptix-lifecycle-policy"

// simTransitionCatalogClass maps the S3 API's TransitionStorageClass
// spelling — which is what a recommendation's transition_storage_class
// parameter carries, because that parameter is the executor's vocabulary —
// onto the lower-case storage-class keys this simulator prices against.
//
// The mapping is duplicated here rather than shared with the rule package on
// purpose: an adapter that imported an application package to borrow a
// lookup table would invert the dependency direction the whole layout rests
// on. An unknown class returns false and the transition is refused, matching
// what S3 itself does with a class it does not recognise.
func simTransitionCatalogClass(apiClass string) (string, bool) {
	switch apiClass {
	case "STANDARD_IA":
		return "standard_ia", true
	case "ONEZONE_IA":
		return "onezone_ia", true
	case "INTELLIGENT_TIERING":
		return "intelligent_tiering", true
	case "GLACIER", "GLACIER_IR":
		return "glacier", true
	case "DEEP_ARCHIVE":
		return "deep_archive", true
	}
	return "", false
}

// simLifecycleColdShare is the fraction of a bucket's Standard bytes this
// simulator moves when a transition rule is applied.
//
// A real transition moves objects once they pass the rule's age threshold,
// which needs per-object write timestamps the estate does not model. Rather
// than pretend to a precision it does not have, the simulator applies the
// same documented cold-share assumption the S3 rules price their estimates
// against (see rule_s3_no_lifecycle.go and rule_s3_intelligent_tiering.go,
// which use 0.6 and 0.5) — so an executed transition lands in the same
// neighbourhood as the saving that was predicted for it, and the validation
// step compares two figures derived from the same stated assumption rather
// than a prediction against a zero.
const simLifecycleColdShare = 0.55

// applyS3LifecycleSpec applies one managed lifecycle rule to a bucket,
// identified by rule_id exactly as the live executor does, and models what
// that rule's clauses actually recover: a transition moves the cold share of
// the bucket's Standard bytes into the target class, and a non-current
// version expiry sweeps the version history that S3MonthlyCost bills at the
// Standard rate until something clears it.
//
// Reading the parameters rather than ignoring them is the point. The
// previous version set a boolean and zeroed non-current versions whatever
// the recommendation asked for, so a tiering recommendation "succeeded" by
// performing a different change than the one it proposed, and a second
// recommendation against the same bucket reported "already applied" and did
// nothing at all.
var applyS3LifecycleSpec = actionSpec{
	action:          optimize.ActionApplyS3Lifecycle,
	requiredActions: []string{"s3:GetBucketLifecycleConfiguration", "s3:PutBucketLifecycleConfiguration"},
	kind:            cloud.KindS3Bucket,
	awsAction:       "s3:PutBucketLifecycleConfiguration",
	titleFmt:        "Apply a lifecycle policy to bucket %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskLow,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		b, ok := e.S3Buckets[id]
		if !ok {
			return nil, false
		}
		// storage_gib is captured as JSON rather than as a live map: the
		// captured map becomes the rollback step's Parameters, which are
		// persisted and may cross a JSON boundary before restore reads them
		// back, and a string survives that round trip with its types intact.
		storage, _ := json.Marshal(b.StorageGiB)
		return map[string]any{
			"has_lifecycle_policy":   b.HasLifecyclePolicy,
			"lifecycle_rule_ids":     strings.Join(b.LifecycleRuleIDs, ","),
			"noncurrent_version_gib": b.NonCurrentVersionGiB,
			"storage_gib_json":       string(storage),
		}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		b, ok := e.S3Buckets[id]
		return ok && containsString(b.LifecycleRuleIDs, simLifecycleRuleID(params))
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		b := e.S3Buckets[id]
		applied := map[string]any{}

		if days, ok := paramInt(params, "transition_days"); ok && days >= 0 {
			apiClass, _ := paramStr(params, "transition_storage_class")
			target, known := simTransitionCatalogClass(apiClass)
			if !known {
				return nil, core.Invalid("apply_s3_lifecycle: transition_storage_class %q is not a storage class an object can transition into", apiClass)
			}
			moved := b.StorageGiB["standard"] * simLifecycleColdShare
			if moved > 0 {
				b.StorageGiB["standard"] -= moved
				b.StorageGiB[target] += moved
			}
			applied["transition_storage_class"] = apiClass
			applied["transition_days"] = days
			applied["transitioned_gib"] = moved
		}
		if days, ok := paramInt(params, "noncurrent_expiration_days"); ok && days > 0 {
			applied["noncurrent_expiration_days"] = days
			applied["expired_noncurrent_gib"] = b.NonCurrentVersionGiB
			b.NonCurrentVersionGiB = 0
		}
		if len(applied) == 0 {
			// An enabled lifecycle rule with no transition and no expiry
			// changes nothing and costs nothing to add, which is exactly why
			// it is worth refusing: it would report success for a
			// recommendation whose parameters said nothing an executor could
			// act on.
			return nil, core.Invalid("apply_s3_lifecycle: the recommendation names no lifecycle clause to apply " +
				"(expected transition_days with transition_storage_class, and/or noncurrent_expiration_days)")
		}

		ruleID := simLifecycleRuleID(params)
		if !containsString(b.LifecycleRuleIDs, ruleID) {
			b.LifecycleRuleIDs = append(b.LifecycleRuleIDs, ruleID)
		}
		b.HasLifecyclePolicy = true
		applied["rule_id"] = ruleID
		applied["new_monthly_cost_micros"] = e.S3MonthlyCost(b).Micros()
		return applied, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		b, ok := e.S3Buckets[id]
		if !ok {
			return core.NotFound("s3_bucket", id)
		}
		hp, _ := before["has_lifecycle_policy"].(bool)
		ncv, _ := paramFloat(before, "noncurrent_version_gib")
		b.HasLifecyclePolicy = hp
		b.NonCurrentVersionGiB = ncv
		if ids, ok := paramStr(before, "lifecycle_rule_ids"); ok {
			b.LifecycleRuleIDs = splitNonEmpty(ids, ",")
		}
		if raw, ok := paramStr(before, "storage_gib_json"); ok && raw != "" {
			var storage map[string]float64
			if err := json.Unmarshal([]byte(raw), &storage); err != nil {
				return core.Invalid("rollback snapshot for %s has an undecodable storage_gib_json: %v", id, err)
			}
			b.StorageGiB = storage
		}
		return nil
	},
}

// simLifecycleRuleID reads the managed rule's id from a recommendation's
// parameters, falling back to the same default the live executor uses.
func simLifecycleRuleID(params map[string]any) string {
	if id, ok := paramStr(params, "rule_id"); ok && id != "" {
		return id
	}
	return defaultSimLifecycleRuleID
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}

// abortMultipartUploadsSpec discards a bucket's stalled incomplete
// multipart uploads. This is irreversible: once a real AbortMultipartUpload
// call runs, the already-uploaded parts are gone and the upload cannot be
// resumed, so the simulator refuses to roll it back rather than
// pretending the discarded parts come back.
var abortMultipartUploadsSpec = actionSpec{
	action:          optimize.ActionAbortMultipartUploads,
	requiredActions: []string{"s3:ListMultipartUploads", "s3:AbortMultipartUpload"},
	kind:            cloud.KindS3Bucket,
	awsAction:       "s3:AbortMultipartUpload",
	titleFmt:        "Abort stalled multipart uploads in bucket %s",

	rollbackFeasible: false,
	infeasibleReason: "aborted multipart upload parts cannot be resumed once discarded",
	dataLossRisk:     core.RiskMedium,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		b, ok := e.S3Buckets[id]
		if !ok {
			return nil, false
		}
		return map[string]any{
			"incomplete_multipart_count": b.IncompleteMultipartCount,
			"incomplete_multipart_gib":   b.IncompleteMultipartGiB,
		}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		b, ok := e.S3Buckets[id]
		return ok && b.IncompleteMultipartCount == 0 && b.IncompleteMultipartGiB == 0
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		b := e.S3Buckets[id]
		b.IncompleteMultipartCount = 0
		b.IncompleteMultipartGiB = 0
		return map[string]any{"incomplete_multipart_count": 0, "new_monthly_cost_micros": e.S3MonthlyCost(b).Micros()}, nil
	},
}

// setLogRetentionSpec assigns a finite retention period to a CloudWatch
// log group that never expires (RetentionDays == 0). Actual AWS pricing
// does not vary by retention setting — only ingested and stored bytes are
// billed — so the saving comes from what finally imposing a retention
// window on a "never expire" group models honestly: the accumulated
// StoredGiB is capped at what the ingest rate would produce over the new
// window, which is the same effect the change has on a real bill over
// time.
var setLogRetentionSpec = actionSpec{
	action:          optimize.ActionSetLogRetention,
	requiredActions: []string{"logs:DescribeLogGroups", "logs:PutRetentionPolicy"},
	kind:            cloud.KindLogGroup,
	awsAction:       "logs:PutRetentionPolicy",
	titleFmt:        "Set a finite retention period on log group %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskLow,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		g, ok := e.LogGroups[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"retention_days": g.RetentionDays, "stored_gib": g.StoredGiB}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		g, ok := e.LogGroups[id]
		target, pok := paramInt(params, "retention_days")
		return ok && pok && g.RetentionDays == target
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		g := e.LogGroups[id]
		target, ok := paramInt(params, "retention_days")
		if !ok || target <= 0 {
			return nil, core.Invalid("set_log_retention requires a positive retention_days parameter")
		}
		g.RetentionDays = target
		capped := g.IngestGBPerMonth * float64(target) / core.AverageDaysPerMonth
		if capped < g.StoredGiB {
			g.StoredGiB = capped
		}
		return map[string]any{
			"retention_days": target, "stored_gib": g.StoredGiB,
			"new_monthly_cost_micros": e.LogGroupMonthlyCost(g).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		g, ok := e.LogGroups[id]
		if !ok {
			return core.NotFound("log_group", id)
		}
		rd, ok := paramInt(before, "retention_days")
		if !ok {
			return core.Invalid("rollback snapshot for %s is missing retention_days", id)
		}
		sg, _ := paramFloat(before, "stored_gib")
		g.RetentionDays = rd
		g.StoredGiB = sg
		return nil
	},
}

// resizeLambdaMemorySpec changes a function's configured memory, which
// scales its GB-second billing directly.
var resizeLambdaMemorySpec = actionSpec{
	action:          optimize.ActionResizeLambdaMemory,
	requiredActions: []string{"lambda:GetFunctionConfiguration", "lambda:UpdateFunctionConfiguration"},
	kind:            cloud.KindLambdaFunction,
	awsAction:       "lambda:UpdateFunctionConfiguration",
	titleFmt:        "Right-size memory for Lambda function %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskLow,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		f, ok := e.LambdaFunctions[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"memory_mb": f.MemoryMB}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		f, ok := e.LambdaFunctions[id]
		target, pok := paramInt(params, "memory_mb")
		return ok && pok && f.MemoryMB == target
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		f := e.LambdaFunctions[id]
		target, ok := paramInt(params, "memory_mb")
		if !ok || target <= 0 {
			return nil, core.Invalid("resize_lambda_memory requires a positive memory_mb parameter")
		}
		f.MemoryMB = target
		return map[string]any{"memory_mb": target, "new_monthly_cost_micros": e.LambdaMonthlyCost(f).Micros()}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		f, ok := e.LambdaFunctions[id]
		if !ok {
			return core.NotFound("lambda_function", id)
		}
		mb, ok := paramInt(before, "memory_mb")
		if !ok {
			return core.Invalid("rollback snapshot for %s is missing memory_mb", id)
		}
		f.MemoryMB = mb
		return nil
	},
}

// removeProvisionedConcurrencySpec drops a function's provisioned
// concurrency to zero, removing the around-the-clock GB-second charge
// LambdaMonthlyCost adds for it.
var removeProvisionedConcurrencySpec = actionSpec{
	action:          optimize.ActionRemoveProvisionedConcurrency,
	requiredActions: []string{"lambda:GetProvisionedConcurrencyConfig", "lambda:DeleteProvisionedConcurrencyConfig"},
	kind:            cloud.KindLambdaFunction,
	awsAction:       "lambda:DeleteProvisionedConcurrencyConfig",
	titleFmt:        "Remove pointless provisioned concurrency from %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskLow,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		f, ok := e.LambdaFunctions[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"provisioned_concurrency": f.ProvisionedConcurrency}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		f, ok := e.LambdaFunctions[id]
		return ok && f.ProvisionedConcurrency == 0
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		f := e.LambdaFunctions[id]
		f.ProvisionedConcurrency = 0
		return map[string]any{"provisioned_concurrency": 0, "new_monthly_cost_micros": e.LambdaMonthlyCost(f).Micros()}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		f, ok := e.LambdaFunctions[id]
		if !ok {
			return core.NotFound("lambda_function", id)
		}
		pc, ok := paramInt(before, "provisioned_concurrency")
		if !ok {
			return core.Invalid("rollback snapshot for %s is missing provisioned_concurrency", id)
		}
		f.ProvisionedConcurrency = pc
		return nil
	},
}
