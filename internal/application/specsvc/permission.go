package specsvc

import (
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// pathPermission names the extra permission a patch path requires beyond the
// baseline core.PermSpecWrite every ProposeRevision call needs, or "" when
// the baseline is enough.
//
// Only automation and governance get a dedicated check. Both sections
// change what CloudOptix is allowed to do to a customer's infrastructure
// without a human in the loop each time — automation.* decides whether a
// change executes at all, governance.* decides who has to sign off on it —
// so a role that can edit the rest of the specification (add a workload,
// change a business objective) is not automatically trusted to loosen
// either one. Every other section is covered by PermSpecWrite alone: an
// architect who can revise objectives or business context should not be
// blocked path-by-path for sections that carry no comparable escalation
// risk.
func pathPermission(path string) core.Permission {
	top := path
	if i := strings.IndexAny(top, ".["); i >= 0 {
		top = top[:i]
	}
	switch strings.ToLower(top) {
	case "automation":
		return core.PermAutomationWrite
	case "governance":
		return core.PermPolicyWrite
	default:
		return ""
	}
}
