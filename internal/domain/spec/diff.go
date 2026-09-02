package spec

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
)

// Diff computes the change set between two specification versions.
//
// The diff is structural rather than textual: it flattens both specifications
// into dotted paths and compares values, so a reordered YAML block produces no
// diff and a changed availability target always does. Each change is annotated
// with its downstream impact, because "availabilityTarget: 0.999 -> 0.9999"
// means nothing to a reviewer unless they are told it will suppress
// single-AZ recommendations.
//
// Traceability: REQ-SPEC-011, SPEC-ONB-005.
func Diff(before, after Spec) []Change {
	beforeMap := flatten(before)
	afterMap := flatten(after)

	paths := map[string]bool{}
	for k := range beforeMap {
		paths[k] = true
	}
	for k := range afterMap {
		paths[k] = true
	}
	ordered := make([]string, 0, len(paths))
	for k := range paths {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	var changes []Change
	for _, p := range ordered {
		b, hadBefore := beforeMap[p]
		a, hadAfter := afterMap[p]
		switch {
		case hadBefore && hadAfter && b == a:
			continue
		case !hadBefore && hadAfter:
			changes = append(changes, Change{
				Path: p, Kind: ChangeAdded, After: a,
				Severity: severityFor(p), Impact: impactFor(p, "", a),
			})
		case hadBefore && !hadAfter:
			changes = append(changes, Change{
				Path: p, Kind: ChangeRemoved, Before: b,
				Severity: severityFor(p), Impact: impactFor(p, b, ""),
			})
		default:
			changes = append(changes, Change{
				Path: p, Kind: ChangeModified, Before: b, After: a,
				Severity: severityFor(p), Impact: impactFor(p, b, a),
			})
		}
	}
	return SortChanges(changes)
}

// severityFor rates how consequential a change to a path is. The ratings
// encode which fields, if wrong, cost money or cause outages.
func severityFor(path string) core.Severity {
	switch {
	case strings.HasPrefix(path, "automation."),
		strings.HasPrefix(path, "governance."),
		strings.HasPrefix(path, "security."),
		strings.HasPrefix(path, "aws.accounts"),
		path == "aws.accessMode":
		return core.SeverityHigh
	case strings.HasPrefix(path, "objectives."),
		strings.HasPrefix(path, "optimization."),
		strings.HasPrefix(path, "business.transactions"):
		return core.SeverityMedium
	case strings.HasPrefix(path, "workloads"),
		strings.HasPrefix(path, "notifications."),
		strings.HasPrefix(path, "teams"):
		return core.SeverityLow
	default:
		return core.SeverityInfo
	}
}

// impactFor explains a change's downstream consequences in one line.
func impactFor(path, before, after string) string {
	switch {
	case path == "automation.enabled":
		if after == "true" {
			return "CloudOptix may execute approved optimizations automatically within policy"
		}
		return "all optimizations become advisory; nothing will execute automatically"
	case path == "automation.autoRollback":
		if after == "false" {
			return "a regression after an automated change will alert rather than self-heal"
		}
		return "regressions after automated changes will trigger an automatic rollback"
	case path == "governance.productionChangesRequireApproval":
		if after == "false" {
			return "production changes may proceed without a named human approver"
		}
		return "every production change will wait for human approval"
	case path == "optimization.riskTolerance":
		return fmt.Sprintf("recommendation ranking and architecture scoring shift from %s to %s weighting", before, after)
	case strings.HasPrefix(path, "objectives.availabilityTarget"):
		return "reliability-reducing recommendations will be filtered against the new target"
	case strings.HasPrefix(path, "objectives.costReductionTarget"):
		return "the savings funnel and executive dashboard retarget against this goal"
	case strings.HasPrefix(path, "objectives.costSlos"):
		return "cost SLO evaluation and economic error budgets change"
	case strings.HasPrefix(path, "aws.accounts") && strings.HasSuffix(path, ".regions"):
		return "discovery scope changes; resources outside the new region set stop being tracked"
	case strings.HasPrefix(path, "aws.accounts"):
		return "account onboarding and discovery scope are affected"
	case path == "security.awsAccessMode":
		return "how CloudOptix authenticates to this estate changes"
	case strings.HasPrefix(path, "business.transactions"):
		return "cost-per-transaction denominators change; unit economics history is not comparable across this edit"
	case strings.HasPrefix(path, "optimization.excluded"):
		return "the set of resources or actions CloudOptix will propose changes for narrows or widens"
	default:
		return ""
	}
}

// flatten converts a specification into dotted path -> scalar string pairs by
// round-tripping through YAML. Using the marshalled form rather than
// reflection over the struct means the diff automatically covers new fields as
// the specification schema grows.
//
// YAML rather than JSON, deliberately. The paths this produces are the ones a
// customer sees: they match the keys in the cloudoptix.yaml they edit, the
// paths the review UI renders, the paths severityFor and impactFor below key
// on, and the paths the revision-patch resolver accepts. Flattening through
// JSON instead would produce snake_case paths for the same fields, which
// silently breaks every one of those lookups — a diff would still be produced,
// but `automation.autoRollback` would be rated as an ordinary informational
// change rather than the high-severity automation edit it is, and the reviewer
// would not be told what turning it off means.
//
// It also means the two fields tagged `yaml:"-"` — the provenance map and the
// open-question list — are excluded for free. They are conversation state, not
// specification content, and must never appear in a version diff.
func flatten(s Spec) map[string]string {
	raw, err := yaml.Marshal(s)
	if err != nil {
		return map[string]string{}
	}
	var generic map[string]any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	walk("", generic, out)
	return out
}

func walk(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(join(prefix, k), t[k], out)
		}
	case []any:
		for i, item := range t {
			walk(fmt.Sprintf("%s[%d]", prefix, i), item, out)
		}
	case nil:
		// absent values produce no path, so an omitted field and an explicit
		// null are treated identically
	default:
		out[prefix] = fmt.Sprintf("%v", t)
	}
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// ComputeChecksum fingerprints a specification's content, ignoring
// conversation state. Two specifications with the same checksum configure the
// platform identically.
func ComputeChecksum(s Spec) string {
	flat := flatten(s)
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(flat[k])
		b.WriteByte('\n')
	}
	return govern.Checksum(b.String())
}

// HasMaterialChanges reports whether a diff contains anything a reviewer must
// see, as opposed to cosmetic edits. It is what decides whether a new version
// needs re-approval or can be applied as a minor revision.
func HasMaterialChanges(changes []Change) bool {
	for _, c := range changes {
		if c.Severity.Order() >= core.SeverityMedium.Order() {
			return true
		}
	}
	return false
}
