package pricing

import (
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// SmallerCandidates returns every instance type in spec's family that is
// smaller than spec, ordered closest-to-farthest (the next size down first).
// This is the order a rightsizing rule should try: prefer the smallest step
// that clears the headroom check, rather than jumping straight to the
// smallest available size.
func (c *Catalog) SmallerCandidates(spec ports.InstanceSpec) []ports.InstanceSpec {
	return c.laddercandidates(spec, false)
}

// LargerCandidates returns every instance type in spec's family that is
// larger than spec, ordered closest-to-farthest. Used when a resource is
// under-provisioned (saturated CPU/memory) rather than idle.
func (c *Catalog) LargerCandidates(spec ports.InstanceSpec) []ports.InstanceSpec {
	return c.laddercandidates(spec, true)
}

func (c *Catalog) laddercandidates(spec ports.InstanceSpec, larger bool) []ports.InstanceSpec {
	family := c.b.InstanceFamilies[spec.Family]
	if len(family) == 0 {
		return nil
	}
	pos := -1
	for i, t := range family {
		if t == norm(spec.Type) {
			pos = i
			break
		}
	}
	if pos < 0 {
		return nil
	}
	var out []ports.InstanceSpec
	if larger {
		for i := pos + 1; i < len(family); i++ {
			if s, ok := c.InstanceSpec(family[i]); ok {
				out = append(out, s)
			}
		}
	} else {
		for i := pos - 1; i >= 0; i-- {
			if s, ok := c.InstanceSpec(family[i]); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// gravitonFamily maps an x86 EC2 family onto the newest Graviton (arm64)
// family this catalog carries. Every x86 line the catalog prices resolves to
// exactly one Graviton target; a family with no Graviton line in the catalog
// (there is none in this data set — every covered x86 family has a Graviton
// counterpart) would resolve to "" and GravitonEquivalent would report false.
var gravitonFamily = map[string]string{
	"t2": "t4g", "t3": "t4g", "t3a": "t4g",
	"m4": "m6g", "m5": "m6g", "m5a": "m6g", "m6i": "m6g", "m7i": "m6g",
	"c4": "c7g", "c5": "c7g", "c6i": "c7g",
	"r4": "r6g", "r5": "r6g", "r6i": "r6g", "r7i": "r6g",
}

// GravitonEquivalent returns the same-size Graviton (arm64) instance type for
// an x86_64 instance type, and whether one exists in the catalog. An
// instance type that is already arm64, or whose family has no Graviton line,
// or whose specific size is not offered in the Graviton family (e.g. a
// 16xlarge in a family whose Graviton line tops out at 4xlarge), returns
// ("", false) — never a fabricated or size-mismatched substitute.
func (c *Catalog) GravitonEquivalent(instanceType string) (string, bool) {
	it, ok := c.instanceType(instanceType)
	if !ok {
		return "", false
	}
	if strings.EqualFold(it.Architecture, "arm64") {
		return "", false
	}
	target, ok := gravitonFamily[it.Family]
	if !ok {
		return "", false
	}
	candidate := target + "." + it.Size
	if _, ok := c.instanceType(candidate); !ok {
		return "", false
	}
	return candidate, true
}
