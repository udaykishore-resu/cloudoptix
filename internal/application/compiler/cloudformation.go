package compiler

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// cfnTemplate is the subset of a CloudFormation template this compiler reads.
type cfnTemplate struct {
	Resources map[string]cfnResource `json:"Resources" yaml:"Resources"`
}

type cfnResource struct {
	Type       string         `json:"Type" yaml:"Type"`
	Properties map[string]any `json:"Properties" yaml:"Properties"`
}

// ParseCloudFormationJSON reads a CloudFormation template's Resources section
// and normalizes each into a RawResource. A template is a declared target
// state, not a diff against what is deployed — CloudFormation change sets
// carry that information but a plain template never does — so every resource
// is emitted as ChangeCreate, matching raw Terraform HCL's same honest
// limitation (see terraform_hcl.go's doc comment).
func ParseCloudFormationJSON(content []byte, fallbackRegion core.Region) ([]RawResource, []string, error) {
	var tmpl cfnTemplate
	if err := json.Unmarshal(content, &tmpl); err != nil {
		return nil, nil, fmt.Errorf("compiler: invalid CloudFormation JSON: %w", err)
	}
	return cfnToRaw(tmpl, fallbackRegion)
}

// ParseCloudFormationYAML reads a CloudFormation template authored in YAML.
//
// CloudFormation's YAML dialect adds short-form intrinsic function tags
// (!Ref, !GetAtt, !Sub, !Join, ...) that are not part of the YAML 1.1/1.2
// spec. gopkg.in/yaml.v3 (the only YAML parser available under this
// package's dependency constraints) decodes an unrecognised tag on a scalar
// as its plain string value with the tag discarded rather than erroring, so
// `InstanceType: !Ref InstanceTypeParam` decodes as the literal string
// "InstanceTypeParam" — not the parameter's actual value. This parser does
// not resolve parameters, so a property driven by an intrinsic function is
// read as that function's literal argument text and is very likely to miss
// the pricing catalog; the resulting resource is reported as Unpriced rather
// than silently priced from a parameter's name. A plain scalar property
// (`InstanceType: m5.large`) is read correctly.
func ParseCloudFormationYAML(content []byte, fallbackRegion core.Region) ([]RawResource, []string, error) {
	var tmpl cfnTemplate
	if err := yaml.Unmarshal(content, &tmpl); err != nil {
		return nil, nil, fmt.Errorf("compiler: invalid CloudFormation YAML: %w", err)
	}
	return cfnToRaw(tmpl, fallbackRegion)
}

func cfnToRaw(tmpl cfnTemplate, fallbackRegion core.Region) ([]RawResource, []string, error) {
	if len(tmpl.Resources) == 0 {
		return nil, nil, fmt.Errorf("compiler: CloudFormation template has no Resources section")
	}
	var out []RawResource
	var warnings []string
	for logicalID, res := range tmpl.Resources {
		normalizedProps := normalizeYAMLProperties(res.Properties)
		terraformType, known := cfnTypeToTerraform[res.Type]
		if !known {
			// Still emit the resource so it shows up in the change set and
			// counts against coverage — an unrecognised CFN type becomes
			// Unpriced downstream, keyed by its own CFN type name rather than
			// silently vanishing from the report.
			terraformType = res.Type
		}
		attrs := translateCFNProperties(res.Type, normalizedProps)
		out = append(out, RawResource{
			Address: fmt.Sprintf("%s (%s)", logicalID, res.Type),
			Type:    terraformType,
			Action:  simulate.ChangeCreate,
			Region:  regionFromAttrs(attrs, fallbackRegion),
			After:   attrs,
			Tags:    cfnTags(res.Properties),
		})
	}
	return out, warnings, nil
}

// translateCFNProperties renames the PascalCase properties this compiler
// reads onto the snake_case keys the pricing functions expect, via
// cfnPropertyAliases, and passes every other property through unchanged
// (harmless: pricing functions only ever look up specific known keys).
func translateCFNProperties(cfnType string, props map[string]any) Attrs {
	out := Attrs{}
	for k, v := range props {
		out[k] = v
	}
	aliases := cfnPropertyAliases[cfnType]
	for cfnKey, ourKey := range aliases {
		if v, ok := props[cfnKey]; ok {
			out[ourKey] = v
		}
	}
	return out
}

// normalizeYAMLProperties converts the map[string]interface{} shape
// yaml.v3 sometimes nests as map[interface{}]interface{} (for a document
// decoded through an intermediate any) into plain map[string]any so Attrs'
// accessors, written against JSON's decoding shape, work identically for a
// YAML-sourced template. json.Unmarshal never produces this shape, so this is
// a YAML-only concern.
func normalizeYAMLProperties(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = normalizeYAMLValue(v)
	}
	return out
}

func normalizeYAMLValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return normalizeYAMLProperties(t)
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if ks, ok := k.(string); ok {
				out[ks] = normalizeYAMLValue(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeYAMLValue(e)
		}
		return out
	case int:
		return float64(t)
	default:
		return v
	}
}

// cfnTags reads a CloudFormation "Tags" property, which is a list of
// {Key, Value} objects rather than Terraform's flat map, into the flat map
// Attrs.Tags() and the risk/regression checks expect.
func cfnTags(props map[string]any) map[string]string {
	raw, ok := props["Tags"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, item := range list {
		entry := normalizeYAMLValue(item)
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		key, _ := asString(m["Key"])
		val, _ := asString(m["Value"])
		if key != "" {
			out[key] = val
		}
	}
	return out
}
