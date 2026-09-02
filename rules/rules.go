// Package rules is the versioned, documented YAML rule pack that configures
// the CloudOptix optimization engine (internal/application/optimization).
//
// The pack lives as data, not code, on purpose: an SRE or a FinOps lead
// tuning a threshold — "our staging fleet actually runs hot, raise the
// underutilization CPU ceiling to 55%" — should be able to review that change
// as a one-line YAML diff, not a Go pull request. Splitting the pack into one
// file per category (compute, storage, database, network, serverless,
// kubernetes, observability, commitment) rather than one monolith keeps each
// diff scoped to the team that owns that part of the estate.
//
// The files are embedded into the binary with go:embed so the shipped engine
// never depends on a rules/ directory existing on disk at runtime; Load
// parses them into the RuleDef catalogue the Registry is built from.
//
// Traceability: REQ-OPT-002, SPEC-OPT-002 (rule configurability).
package rules

import (
	"embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var FS embed.FS

// packFiles is the fixed, ordered list of rule-pack files. Ordering here
// controls nothing about evaluation (the Registry re-sorts by rule ID for
// determinism) — it only controls the order Load reports a duplicate ID in,
// which keeps error messages reproducible.
var packFiles = []string{
	"compute.yaml",
	"storage.yaml",
	"database.yaml",
	"network.yaml",
	"serverless.yaml",
	"kubernetes.yaml",
	"observability.yaml",
	"commitment.yaml",
}

// RuleDef is one rule's declarative configuration: its identity, its default
// thresholds, and whether it ships enabled. It is the on-disk twin of
// ports.RuleInfo, minus the fields (Calibration) that only exist once a rule
// has run.
type RuleDef struct {
	ID          string             `yaml:"id"`
	Name        string             `yaml:"name"`
	Category    string             `yaml:"category"`
	Action      string             `yaml:"action"`
	Description string             `yaml:"description"`
	Kinds       []string           `yaml:"kinds"`
	Enabled     bool               `yaml:"enabled"`
	Thresholds  map[string]float64 `yaml:"thresholds"`
}

type rulePackFile struct {
	Version int       `yaml:"version"`
	Pack    string    `yaml:"pack"`
	Rules   []RuleDef `yaml:"rules"`
}

// Pack is the full, parsed rule catalogue, keyed by rule ID.
type Pack struct {
	Defs map[string]RuleDef
}

// Load parses every embedded YAML file into a Pack. It fails closed on a
// malformed file or a duplicate rule ID across packs — a silently dropped
// duplicate would mean one rule's thresholds shadow another's without either
// author knowing.
func Load() (Pack, error) {
	p := Pack{Defs: map[string]RuleDef{}}
	for _, name := range packFiles {
		raw, err := FS.ReadFile(name)
		if err != nil {
			return Pack{}, fmt.Errorf("rules: reading %s: %w", name, err)
		}
		var f rulePackFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return Pack{}, fmt.Errorf("rules: parsing %s: %w", name, err)
		}
		for _, def := range f.Rules {
			if def.ID == "" {
				return Pack{}, fmt.Errorf("rules: %s contains a rule with no id", name)
			}
			if _, dup := p.Defs[def.ID]; dup {
				return Pack{}, fmt.Errorf("rules: duplicate rule id %q (found again in %s)", def.ID, name)
			}
			p.Defs[def.ID] = def
		}
	}
	return p, nil
}

// IDs returns every rule id in the pack, sorted, for deterministic iteration
// and for tests that assert on the full catalogue.
func (p Pack) IDs() []string {
	out := make([]string, 0, len(p.Defs))
	for id := range p.Defs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
