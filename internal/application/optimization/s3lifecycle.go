package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// This file holds the one place the apply_s3_lifecycle parameter shape is
// built, shared by the four rules that emit that action.
//
// It exists because those four rules previously each invented their own
// spelling. Three of them emitted target_class carrying a pricing-catalog
// name ("standard_ia", "intelligent_tiering"); the executor reads
// transition_storage_class carrying an S3 API name ("STANDARD_IA",
// "INTELLIGENT_TIERING") and only builds a transition at all when
// transition_days is also present. So the plan did not fail loudly — it did
// something worse, PUTting an enabled lifecycle rule with no transition and
// no expiration onto the bucket and reporting success. The fourth emitted
// noncurrent_expiration_days, which nothing anywhere read.
//
// Two things follow from that, and both are encoded here rather than left to
// each rule to remember:
//
//   - The transition class and the day count travel together, because one
//     without the other is not a lifecycle transition.
//   - Every rule gets its own rule_id. apply_s3_lifecycle manages exactly one
//     rule inside the bucket's configuration, keyed by rule_id (see
//     internal/adapters/aws/executor/s3.go), so four rules sharing the
//     default id would each silently overwrite the last one applied. Distinct
//     ids are what let a bucket carry both a tiering transition and a
//     non-current-version expiry — two changes that genuinely compose, and
//     which the conflict model deliberately keeps in separate domains.

// S3TransitionStorageClass converts a pricing-catalog storage-class name —
// the lower-case spelling this package prices against and that discovery
// reports — into the S3 API's TransitionStorageClass spelling, which is what
// the executor puts on the wire.
//
// The mapping is explicit rather than an upper-casing trick: only some
// storage classes are valid transition targets (STANDARD is not; an object
// cannot transition "up"), and returning false for an unmappable class lets
// the caller decline to emit a transition instead of shipping a rule AWS
// will reject.
func S3TransitionStorageClass(catalogClass string) (string, bool) {
	switch catalogClass {
	case "standard_ia":
		return "STANDARD_IA", true
	case "onezone_ia", "one_zone_ia":
		return "ONEZONE_IA", true
	case "intelligent_tiering":
		return "INTELLIGENT_TIERING", true
	case "glacier", "glacier_flexible":
		return "GLACIER", true
	case "glacier_ir", "glacier_instant_retrieval":
		return "GLACIER_IR", true
	case "deep_archive", "glacier_deep_archive":
		return "DEEP_ARCHIVE", true
	}
	return "", false
}

// s3LifecycleRuleID namespaces the managed lifecycle rule by the CloudOptix
// rule that asked for it, so two recommendations against one bucket produce
// two lifecycle rules rather than one overwriting the other.
func s3LifecycleRuleID(rule optimize.RuleID) string {
	return fmt.Sprintf("cloudoptix-%s", rule)
}

// s3TransitionParameters builds the executor's parameter map for a
// transition-to-a-cheaper-class lifecycle rule. It returns ok=false when the
// target class is not a valid transition target, which the caller must treat
// as "do not propose an executable change" rather than as "propose one with
// a missing field".
func s3TransitionParameters(rule optimize.RuleID, bucket, targetCatalogClass string, afterDays int) (map[string]any, bool) {
	class, ok := S3TransitionStorageClass(targetCatalogClass)
	if !ok {
		return nil, false
	}
	return map[string]any{
		"bucket":                   bucket,
		"rule_id":                  s3LifecycleRuleID(rule),
		"transition_days":          afterDays,
		"transition_storage_class": class,
	}, true
}
