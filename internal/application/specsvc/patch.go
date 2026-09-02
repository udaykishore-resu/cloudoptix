package specsvc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// applyDottedPatch produces a new spec.Spec by applying a dotted-path JSON
// merge patch on top of base, without mutating base.
//
// KEY DESIGN DECISION: paths resolve against each struct field's `yaml` tag,
// not its `json` tag. The two disagree on this codebase — spec.Spec's json
// tags are snake_case (`availability_target`) while its yaml tags are
// camelCase (`availabilityTarget`) — and the yaml form is the one a customer
// actually writes, because ExportYAML/ImportYAML round-trip the specification
// through exactly those tags as the cloudoptix.yaml file a customer commits
// to their repository (see onboarding.RenderYAML). A patch path is a
// customer-facing address into that same document, so it has to speak the
// same vocabulary: "objectives.availabilityTarget", not
// "objectives.availability_target".
//
// A path segment may carry one or more bracketed indices
// (`workloads[0]`, `teams[0].members[1].email`) to reach into a slice;
// indexing past the current length grows the slice with zero values rather
// than erroring, so a patch can both append a new element and set its
// fields in the same call. Each leaf value is applied by round-tripping it
// through encoding/json into the target field's Go type, which is what lets
// one function handle every field type in the specification — scalars,
// slices, maps, and whole nested structs assigned in one path — without a
// type switch per case.
func applyDottedPatch(base spec.Spec, patch map[string]any) (spec.Spec, error) {
	// A JSON round trip is the deep copy: spec.Spec holds slices and maps
	// that a shallow copy would share with base, so mutating the copy would
	// silently mutate the active version's own in-memory Spec too. JSON tags
	// (unlike yaml) cover every field including Provenance and
	// OpenQuestions, so nothing about the source version is lost.
	raw, err := json.Marshal(base)
	if err != nil {
		return spec.Spec{}, fmt.Errorf("specsvc: copying specification: %w", err)
	}
	var out spec.Spec
	if err := json.Unmarshal(raw, &out); err != nil {
		return spec.Spec{}, fmt.Errorf("specsvc: copying specification: %w", err)
	}

	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic application order for reproducible output

	root := reflect.ValueOf(&out).Elem()
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return spec.Spec{}, fmt.Errorf("specsvc: patch contains an empty path")
		}
		if err := setPath(root, strings.Split(k, "."), patch[k]); err != nil {
			return spec.Spec{}, fmt.Errorf("specsvc: patch path %q: %w", k, err)
		}
	}
	return out, nil
}

var segmentRe = regexp.MustCompile(`^([A-Za-z0-9_]+)((?:\[\d+\])*)$`)
var indexRe = regexp.MustCompile(`\[(\d+)\]`)

// parseSegment splits one dotted-path segment into its field name and any
// trailing bracketed indices, e.g. "accounts[0]" -> ("accounts", [0]).
func parseSegment(seg string) (name string, indices []int, err error) {
	m := segmentRe.FindStringSubmatch(seg)
	if m == nil {
		return "", nil, fmt.Errorf("%q is not a valid path segment", seg)
	}
	name = m[1]
	for _, im := range indexRe.FindAllStringSubmatch(m[2], -1) {
		n, convErr := strconv.Atoi(im[1])
		if convErr != nil {
			return "", nil, fmt.Errorf("%q has an invalid index: %w", seg, convErr)
		}
		indices = append(indices, n)
	}
	return name, indices, nil
}

// setPath walks parts against v (a struct field, growing slices as needed)
// and applies value at the addressed leaf.
func setPath(v reflect.Value, parts []string, value any) error {
	name, indices, err := parseSegment(parts[0])
	if err != nil {
		return err
	}
	fv, err := fieldByPathName(v, name)
	if err != nil {
		return err
	}

	for _, idx := range indices {
		fv = derefAlloc(fv)
		if fv.Kind() != reflect.Slice {
			return fmt.Errorf("%q is not a list", name)
		}
		if idx < 0 {
			return fmt.Errorf("%q has a negative index", parts[0])
		}
		// Growing to the requested index (rather than requiring the caller
		// to have appended first) is what lets a single patch both create a
		// new element and set its fields, e.g. {"workloads[0].name": "api"}
		// against an empty Workloads slice.
		for fv.Len() <= idx {
			fv.Set(reflect.Append(fv, reflect.Zero(fv.Type().Elem())))
		}
		fv = fv.Index(idx)
	}

	if len(parts) == 1 {
		return setLeaf(fv, value)
	}

	fv = derefAlloc(fv)
	if fv.Kind() != reflect.Struct {
		return fmt.Errorf("%q does not resolve to a nested object", name)
	}
	return setPath(fv, parts[1:], value)
}

// derefAlloc dereferences a pointer field, allocating a zero value first if
// it is nil (spec.AWS.CUR is the one *T field in the specification).
func derefAlloc(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return v.Elem()
	}
	return v
}

// fieldByPathName resolves a path segment against v's exported fields,
// preferring the yaml tag name and falling back to the Go field name — see
// applyDottedPatch's doc comment for why yaml, not json.
func fieldByPathName(v reflect.Value, name string) (reflect.Value, error) {
	if v.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("cannot resolve %q on a non-object value", name)
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "-" {
			continue
		}
		if tag != "" && strings.EqualFold(tag, name) {
			return v.Field(i), nil
		}
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.IsExported() && strings.EqualFold(f.Name, name) {
			return v.Field(i), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("%q is not a field of the specification", name)
}

// setLeaf assigns value into fv by round-tripping it through encoding/json
// into fv's concrete type. This is what lets one patch applier accept a
// scalar, a slice, a map, or a whole sub-object at any addressable leaf
// without a type switch: json.Unmarshal already knows how to decode any
// Go-representable value into any Go type that can hold it, and rejects the
// ones that cannot (a string into a float field, say) with an error that
// names the mismatch.
//
// Object-shaped values have their keys translated to the target type's json
// vocabulary first, by yamlToJSONKeys. Without that step this function
// contradicted the whole file's stated contract: path *segments* resolve
// against yaml tags (see applyDottedPatch), but the keys inside an object
// assigned at a leaf were being decoded against json tags, and the two
// vocabularies disagree throughout spec.Spec. json.Unmarshal ignores a key
// it cannot match, so a caller writing the documented yaml spelling got a
// half-applied object and no error at all —
// {"automation.maintenanceWindows": [{"name":..., "startUtc":"02:00",
// "durationMinutes":240}]} produced a window with a name, no start time and
// a zero duration, which InMaintenanceWindow can never be inside. The demo
// tenant carried exactly that window, which is why nothing it declared as
// automatable ever actually ran.
func setLeaf(fv reflect.Value, value any) error {
	if !fv.CanSet() {
		return fmt.Errorf("field is not settable")
	}
	raw, err := json.Marshal(yamlToJSONKeys(fv.Type(), value))
	if err != nil {
		return fmt.Errorf("encoding patch value %v: %w", value, err)
	}
	ptr := reflect.New(fv.Type())
	if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
		return fmt.Errorf("value %v is not valid for this field: %w", value, err)
	}
	fv.Set(ptr.Elem())
	return nil
}

// yamlToJSONKeys rewrites the keys of an object-shaped patch value from the
// yaml vocabulary a customer writes into the json vocabulary encoding/json
// will decode with, walking the target type alongside the value.
//
// It rewrites only keys it can resolve against the target struct's yaml tag
// or Go field name, and leaves everything else untouched — so a caller who
// already wrote the json spelling ("start_utc") still works, and a genuinely
// unknown key still reaches json.Unmarshal to be ignored exactly as before.
// Translation stops at any type json handles for itself: a leaf scalar, a
// map field, or a type with its own UnmarshalJSON, none of which have struct
// fields to translate against.
func yamlToJSONKeys(t reflect.Type, value any) any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch v := value.(type) {
	case map[string]any:
		if t.Kind() != reflect.Struct {
			return value
		}
		out := make(map[string]any, len(v))
		for k, inner := range v {
			field, ok := structFieldByPatchName(t, k)
			if !ok {
				out[k] = inner
				continue
			}
			out[jsonFieldName(field)] = yamlToJSONKeys(field.Type, inner)
		}
		return out
	case []any:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return value
		}
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = yamlToJSONKeys(t.Elem(), inner)
		}
		return out
	case []map[string]any:
		// The shape an in-process caller writes most naturally; handled
		// explicitly because a []map[string]any is not a []any to a type
		// switch, and silently skipping it would leave exactly the
		// half-applied object this function exists to prevent.
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return value
		}
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = yamlToJSONKeys(t.Elem(), inner)
		}
		return out
	default:
		return value
	}
}

// structFieldByPatchName resolves one object key against a struct's fields
// using the same precedence fieldByPathName uses for path segments: the yaml
// tag first, then the Go field name.
func structFieldByPatchName(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "-" {
			continue
		}
		if tag != "" && strings.EqualFold(tag, name) {
			return f, true
		}
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.IsExported() && strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// jsonFieldName is the key encoding/json will look for when decoding into
// this field.
func jsonFieldName(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if tag != "" && tag != "-" {
		return tag
	}
	return f.Name
}
