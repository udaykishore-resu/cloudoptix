package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretResolver resolves a secret reference ("secretref:...") to its value.
// It is the same shape as ports.SecretResolver; config does not import ports
// (config is loaded before the application wiring that constructs adapters
// exists) so the interface is redeclared here structurally — any
// ports.SecretResolver satisfies it without an adapter.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// Secret is a configuration field that must never hold a literal value once
// it has come from a file on disk. It holds one of:
//   - a reference of the form "env:VAR_NAME" or "secretref:opaque-id", which
//     is safe to commit because it names where the value lives, not the
//     value itself; or
//   - a literal value, which is only ever accepted when it arrived through
//     process environment variables or command-line flags — inputs that by
//     construction cannot be a file checked into source control.
//
// UnmarshalYAML rejects anything that is not a reference, which is what makes
// "secrets are read from the environment or a secret reference, never from a
// committed file" an enforced property rather than a convention: a config.yaml
// with `database.password: hunter2` fails to load instead of silently working.
type Secret struct {
	ref       string // "env:X" | "secretref:X", set when the value must be resolved later
	value     string // resolved (or directly-provided) literal value
	resolved  bool
	fromEnv   bool // set via an environment variable or flag override — literal values are legal here
	fieldName string
}

const (
	envRefPrefix       = "env:"
	secretRefPrefix    = "secretref:"
	developmentLiteral = "dev:" // explicit opt-in escape hatch for local, non-production literals
)

// NewLiteralSecret builds a Secret already holding a resolved value. It is
// for use by code paths that are provably not reading from a committed file —
// environment variable and flag overrides — and for tests.
func NewLiteralSecret(value string) Secret {
	return Secret{value: value, resolved: true, fromEnv: true}
}

// NewSecretRef builds a Secret holding an unresolved reference.
func NewSecretRef(ref string) Secret {
	return Secret{ref: ref}
}

// IsSet reports whether the secret has any reference or value at all.
func (s Secret) IsSet() bool { return s.ref != "" || s.value != "" }

// Reference reports the raw reference string ("" if none), for diagnostics
// that must never print the resolved value.
func (s Secret) Reference() string { return s.ref }

// Resolve fetches the underlying value: "env:X" reads the environment
// variable X, "secretref:X" delegates to resolver, "dev:X" (development mode
// only, checked by the caller via Config.Environment) returns X verbatim, and
// an already-literal Secret (set via environment/flag override) returns
// itself unchanged.
func (s *Secret) Resolve(ctx context.Context, getenv func(string) (string, bool), resolver SecretResolver) error {
	if s.resolved {
		return nil
	}
	switch {
	case s.ref == "":
		return fmt.Errorf("config: secret %q has no value and no reference set", s.fieldName)
	case strings.HasPrefix(s.ref, envRefPrefix):
		name := strings.TrimPrefix(s.ref, envRefPrefix)
		v, ok := getenv(name)
		if !ok {
			return fmt.Errorf("config: secret %q references environment variable %q, which is not set", s.fieldName, name)
		}
		s.value = v
	case strings.HasPrefix(s.ref, secretRefPrefix):
		if resolver == nil {
			return fmt.Errorf("config: secret %q references %q but no secret resolver is configured", s.fieldName, s.ref)
		}
		v, err := resolver.Resolve(ctx, strings.TrimPrefix(s.ref, secretRefPrefix))
		if err != nil {
			return fmt.Errorf("config: resolving secret %q: %w", s.fieldName, err)
		}
		s.value = v
	case strings.HasPrefix(s.ref, developmentLiteral):
		s.value = strings.TrimPrefix(s.ref, developmentLiteral)
	default:
		return fmt.Errorf("config: secret %q has an unrecognised reference %q (expected env:, secretref:, or dev: prefix)", s.fieldName, s.ref)
	}
	s.resolved = true
	return nil
}

// Value returns the resolved value. It panics if called before Resolve
// succeeds, because a caller reaching for a secret's value before resolution
// is a programming error that must fail loudly in development rather than
// silently connect with an empty password in production.
func (s Secret) Value() string {
	if !s.resolved {
		panic(fmt.Sprintf("config: secret %q read before Resolve", s.fieldName))
	}
	return s.value
}

// String implements fmt.Stringer with a redacted form so a Secret dropped
// into a log statement, an error message, or %+v of its parent struct never
// leaks the value.
func (s Secret) String() string {
	if !s.IsSet() {
		return "<unset>"
	}
	return "***REDACTED***"
}

// MarshalJSON redacts the same way String does, so a Config accidentally
// serialised into a health endpoint or a debug dump cannot leak secrets.
func (s Secret) MarshalJSON() ([]byte, error) {
	if !s.IsSet() {
		return json.Marshal(nil)
	}
	return json.Marshal("***REDACTED***")
}

// UnmarshalYAML enforces the file-provenance rule: a plain scalar in a YAML
// document is only accepted when it is itself a reference.
func (s *Secret) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("config: secret field must be a string reference: %w", err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, envRefPrefix) && !strings.HasPrefix(raw, secretRefPrefix) && !strings.HasPrefix(raw, developmentLiteral) {
		return fmt.Errorf(
			"config: secret fields may not hold a literal value in a config file — got a %d-character value at line %d; use \"env:VAR_NAME\" or \"secretref:id\" instead (never commit a secret)",
			len(raw), node.Line)
	}
	s.ref = raw
	s.resolved = false
	return nil
}

// SetFieldName is called by the loader after decoding so error and panic
// messages can name the offending field.
func (s *Secret) SetFieldName(name string) { s.fieldName = name }
