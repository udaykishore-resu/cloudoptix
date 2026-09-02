// Package policies embeds this directory's shipped policy packs so they
// travel inside the compiled binary rather than depending on a filesystem
// checkout being present at runtime.
//
// This mirrors migrations/embed.go and rules/rules.go exactly, and for the
// same reason: //go:embed can only reach files under the directory tree
// rooted at the file declaring it, so the directive has to live here rather
// than in the package that consumes it. Keeping the packs at the
// repository's conventional policies/ path — where the README, a reviewer
// and a customer copying one as a starting point all expect them — is worth
// this one file.
//
// Nothing about a shipped pack is special to the evaluation engine: each is
// an ordinary govern.Policy YAML document, loaded through the same
// governance.Service.LoadPolicyYAML entry point a tenant's own uploaded
// policy goes through.
//
// Traceability: REQ-GOV-002, SPEC-GOV-001.
package policies

import "embed"

// FS holds every policy pack in this directory: conservative.yaml,
// balanced.yaml, aggressive.yaml and regulated.yaml.
//
//go:embed *.yaml
var FS embed.FS

// Names lists the shipped packs, in ascending order of autonomy. The order
// is the one onboarding presents them in, so a tenant choosing a starting
// posture reads from most cautious to least.
func Names() []string {
	return []string{"conservative", "balanced", "aggressive", "regulated"}
}
