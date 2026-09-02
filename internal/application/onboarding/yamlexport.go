package onboarding

import (
	"gopkg.in/yaml.v3"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// RenderYAML renders s as the cloudoptix.yaml document a customer commits
// to their repository. It marshals the Spec struct directly, which is what
// keeps the file's shape and this package's in-memory model from ever
// drifting apart — the yaml tags on spec.Spec ARE the file format.
// Provenance and OpenQuestions are excluded by their own `yaml:"-"` tags:
// they are onboarding-conversation state, not configuration a customer
// hand-edits.
func RenderYAML(s spec.Spec) ([]byte, error) {
	out, err := yaml.Marshal(s)
	if err != nil {
		return nil, err
	}
	return out, nil
}
