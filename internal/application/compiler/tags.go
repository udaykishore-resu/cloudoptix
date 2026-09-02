package compiler

import (
	"sort"
	"strings"
)

// tagWarningPrefix marks a machine-readable warning entry carrying a
// resource's tag keys.
//
// Port mismatch this works around: simulate.PricedChange — defined in
// internal/domain/simulate, which this package may not modify — has no Tags
// field, only Warnings ([]string). The cost-regression require_tags check
// (regression.go) needs to see which tags a changed resource carries, and by
// the time RunRegression runs it only has the persisted CompilationResult to
// work from, not the original RawResource set. Piggy-backing a structured
// entry on Warnings — written once here, read once in regression.go — is the
// documented workaround; nothing else in this package or outside it should
// depend on this format.
const tagWarningPrefix = "tags:"

// tagsWarning renders a resource's tags into the sentinel-prefixed warning
// entry, sorted so the output (and therefore any test asserting on it) is
// deterministic.
func tagsWarning(tags map[string]string) string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return tagWarningPrefix + strings.Join(keys, ",")
}

// parseTagWarning recovers the tag key set from a PricedChange's Warnings, or
// reports found=false if no such entry is present (which happens only for a
// delete, which priceRawResource never tags).
func parseTagWarning(warnings []string) (tags map[string]bool, found bool) {
	for _, w := range warnings {
		if !strings.HasPrefix(w, tagWarningPrefix) {
			continue
		}
		keys := strings.TrimPrefix(w, tagWarningPrefix)
		tags = map[string]bool{}
		if keys != "" {
			for _, k := range strings.Split(keys, ",") {
				tags[k] = true
			}
		}
		return tags, true
	}
	return nil, false
}
