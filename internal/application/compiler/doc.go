// Package compiler implements the CloudOptix Cost Compiler: it prices
// infrastructure changes before they are deployed, from Terraform plan JSON,
// raw Terraform HCL, CloudFormation (JSON and YAML), Kubernetes manifests and
// Helm-rendered output.
//
// The key design decision is the one stated on simulate.PricedChange itself
// and repeated here because everything in this package exists to uphold it:
// "unpriced" and "free" are different answers, and a resource whose cost
// depends on usage the compiler cannot observe (Lambda invocations, NAT bytes
// processed, S3 storage volume) is reported as usage-dependent with its
// assumptions stated, never as a fabricated fixed number and never as a
// silent zero. A gateway VPC endpoint that genuinely costs nothing and an MSK
// cluster the pricing catalog has no data for both produce a PricedChange —
// the first with AfterMonthly at exactly zero and Unpriced=false, the second
// with Unpriced=true and a reason — and a reviewer must be able to tell them
// apart at a glance.
//
// Every parser (terraform_plan.go, terraform_hcl.go, cloudformation.go,
// kubernetes.go) normalizes its dialect into the same intermediate shape,
// RawResource, keyed by a canonical resource-type string. That canonical
// string is deliberately the Terraform provider type name (aws_instance,
// aws_ebs_volume, ...): Terraform's naming is the most complete and widely
// recognized vocabulary for AWS resources, so CloudFormation types and
// Kubernetes kinds are translated onto it rather than inventing a fourth
// vocabulary, and pricer.go carries exactly one dispatch table instead of one
// per source dialect.
//
// Traceability: REQ-CC-001..008, SPEC-SIM-001 (simulate.CompilationResult).
package compiler

import (
	"strconv"
	"strings"
)

// asFloat coerces a decoded JSON/YAML scalar (float64, int, json.Number,
// string, bool) to a float64, returning false when the value cannot be
// interpreted as a number. Terraform plan JSON, CloudFormation JSON and YAML
// all decode numeric literals differently (float64 for JSON, int or string
// for some YAML numeric edge cases), and every pricing function needs one
// consistent coercion rather than repeating a type switch.
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case nil:
		return "", false
	default:
		return "", false
	}
}

func asBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0":
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}
