package compiler

import (
	"strconv"
	"strings"
)

// Attrs is the normalized attribute bag every pricing function reads from,
// regardless of whether it originated as Terraform plan JSON's "after"
// object, a CloudFormation "Properties" map, or a Kubernetes container's
// "resources.requests". Keeping one loosely-typed accessor here is what lets
// pricer.go stay ignorant of which IaC dialect produced the value.
type Attrs map[string]any

// Str reads a string attribute, returning def when absent or not a string.
func (a Attrs) Str(key, def string) string {
	if a == nil {
		return def
	}
	if v, ok := a[key]; ok {
		if s, ok := asString(v); ok && s != "" {
			return s
		}
	}
	return def
}

// Float reads a numeric attribute.
func (a Attrs) Float(key string, def float64) float64 {
	if a == nil {
		return def
	}
	if v, ok := a[key]; ok {
		if f, ok := asFloat(v); ok {
			return f
		}
	}
	return def
}

// Int reads a numeric attribute truncated to int.
func (a Attrs) Int(key string, def int) int {
	return int(a.Float(key, float64(def)))
}

// Bool reads a boolean attribute.
func (a Attrs) Bool(key string, def bool) bool {
	if a == nil {
		return def
	}
	if v, ok := a[key]; ok {
		if b, ok := asBool(v); ok {
			return b
		}
	}
	return def
}

// Has reports whether the key is present with a non-nil value.
func (a Attrs) Has(key string) bool {
	if a == nil {
		return false
	}
	v, ok := a[key]
	return ok && v != nil
}

// Map reads a nested object attribute.
func (a Attrs) Map(key string) Attrs {
	if a == nil {
		return nil
	}
	switch v := a[key].(type) {
	case map[string]any:
		return Attrs(v)
	case Attrs:
		return v
	default:
		return nil
	}
}

// List reads a nested list attribute. Terraform plan JSON represents
// repeated nested blocks (root_block_device, ebs_block_device) this way.
func (a Attrs) List(key string) []any {
	if a == nil {
		return nil
	}
	switch v := a[key].(type) {
	case []any:
		return v
	default:
		return nil
	}
}

// FirstMap returns the first element of a list attribute as Attrs, which is
// how Terraform plan JSON represents a singular nested block (root_block_device
// is a one-element list even though HCL syntax makes it look like a single
// block).
func (a Attrs) FirstMap(key string) Attrs {
	list := a.List(key)
	if len(list) == 0 {
		return nil
	}
	if m, ok := list[0].(map[string]any); ok {
		return Attrs(m)
	}
	return nil
}

// Tags extracts a tag map from the common shapes IaC dialects use: Terraform's
// flat map, CloudFormation's list-of-{Key,Value} pairs (already normalized to
// a flat map by the CFN parser before this is called), and Kubernetes labels.
func (a Attrs) Tags() map[string]string {
	m := a.Map("tags")
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := asString(v); ok {
			out[k] = s
		}
	}
	return out
}

// parseK8sQuantity parses the subset of the Kubernetes "resource.Quantity"
// grammar that appears in a container's resources.requests/limits: a bare
// number, a millicpu suffix ("500m", CPU only), a binary suffix (Ki/Mi/Gi/Ti)
// and a decimal suffix (K/M/G/T with an optional trailing "i" for the binary
// forms already covered). It returns the value in whole units (cores for CPU,
// bytes for memory) so callers convert to the unit the pricing catalog wants.
//
// What it does NOT support, deliberately: exponent notation ("1e3"), the
// scientific/exponent Quantity form AWS's own kubectl output sometimes emits,
// and negative quantities (meaningless for a resource request). An
// unparseable string returns (0, false) rather than a guessed value, so a
// caller can fall back to its own documented default instead of silently
// pricing a zero-CPU container.
func parseK8sQuantity(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.HasSuffix(s, "m") && !strings.ContainsAny(s, "KMGTiE") {
		// Millicpu: "500m" == 0.5 cores. This suffix is CPU-only; a memory
		// quantity never ends in a bare "m".
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		if err != nil {
			return 0, false
		}
		return v / 1000, true
	}
	suffixes := []struct {
		suf   string
		scale float64
	}{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
		{"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf.suf) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, suf.suf), 64)
			if err != nil {
				return 0, false
			}
			return v * suf.scale, true
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
