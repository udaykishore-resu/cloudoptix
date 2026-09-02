package compiler

import (
	"log/slog"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Compiler is the Cost Compiler engine: parse an infrastructure-as-code
// input, price every changed resource, and detect the structural hazards and
// opportunities a headline dollar delta does not show. It holds no state
// across calls and performs no I/O beyond the pricing catalog, which is what
// makes it safe to call from a stateless HTTP handler or a CI job with no
// database in reach.
type Compiler struct {
	Pricing ports.PricingCatalog
	Clock   core.Clock
	Logger  *slog.Logger
}

// New builds a Compiler over a pricing catalog, using the system clock and a
// discarding logger. Callers that want deterministic timestamps in tests or
// structured logging in production set Clock/Logger on the returned value.
func New(pricing ports.PricingCatalog) *Compiler {
	return &Compiler{Pricing: pricing, Clock: core.SystemClock{}, Logger: slog.Default()}
}

// Compile prices one infrastructure change set end to end.
func (c *Compiler) Compile(tenant core.TenantID, req ports.CompileRequest) (simulate.CompilationResult, error) {
	started := c.now()
	if req.Region == "" {
		return simulate.CompilationResult{}, core.Invalid("compiler: a target region is required to price a change set")
	}

	raws, err := c.parse(req)
	if err != nil {
		return simulate.CompilationResult{}, err
	}

	pc := &pricerCtx{
		pricing:         c.Pricing,
		req:             req,
		launchTemplates: map[string]Attrs{},
		taskDefs:        map[string]Attrs{},
	}
	for _, rr := range raws {
		switch rr.Type {
		case "aws_launch_template", "aws_launch_configuration":
			pc.launchTemplates[resourceKey(rr.Address)] = rr.Effective()
		case "aws_ecs_task_definition":
			pc.taskDefs[resourceKey(rr.Address)] = rr.Effective()
		}
	}

	changes := make([]simulate.PricedChange, 0, len(raws))
	for _, rr := range raws {
		pcd := priceRawResource(pc, rr)
		pcd.Warnings = append(pcd.Warnings, rr.Warnings...)
		changes = append(changes, pcd)
	}

	result := simulate.CompilationResult{
		ID:          core.NewID("cmp"),
		TenantID:    tenant,
		Source:      req.Source,
		Label:       req.Label,
		Changes:     changes,
		PricingDate: c.Pricing.PricingDate(),
		CompiledAt:  started,
	}
	// CompilationResult carries no Environment field of its own, yet the
	// require_tags and forbidden_resource regression checks need to know
	// whether a check scoped to (say) production even applies. Rather than
	// widen the domain type (out of scope for this package), the request's
	// environment travels as an ordinary, machine-readable Assumption; see
	// regression.go's checkApplies for the one place that reads it back.
	result.Assumptions = append(result.Assumptions, simulate.Assumption{
		Key: environmentAssumptionKey, Label: "Target environment", Value: string(req.Environment), Provenance: core.ProvenanceConfirmed,
	})
	result.Summarize()

	result.Risks = DetectRisks(raws, result.Changes, req)
	result.Opportunities = DetectOpportunities(pc, raws, result.Changes)

	result.DurationMS = c.now().Sub(started).Milliseconds()
	return result, nil
}

func (c *Compiler) now() time.Time {
	if c.Clock != nil {
		return c.Clock.Now()
	}
	return time.Now().UTC()
}

// parse dispatches to the parser matching req.Source. CloudFormation and
// Kubernetes/Helm both carry ambiguity a SourceKind alone cannot resolve
// (CloudFormation content can be JSON or YAML; Helm-rendered output is just
// Kubernetes YAML), which parse() itself sniffs from the content.
func (c *Compiler) parse(req ports.CompileRequest) ([]RawResource, error) {
	switch req.Source {
	case simulate.SourceTerraformPlan:
		raws, warnings, err := ParseTerraformPlanJSON(req.Content, req.Region)
		c.logParseWarnings(warnings)
		return raws, err
	case simulate.SourceTerraformHCL:
		raws, warnings, err := ParseTerraformHCL(req.Content, req.Region)
		c.logParseWarnings(warnings)
		return raws, err
	case simulate.SourceCloudFormation:
		if looksLikeJSON(req.Content) {
			raws, warnings, err := ParseCloudFormationJSON(req.Content, req.Region)
			c.logParseWarnings(warnings)
			return raws, err
		}
		raws, warnings, err := ParseCloudFormationYAML(req.Content, req.Region)
		c.logParseWarnings(warnings)
		return raws, err
	case simulate.SourceKubernetes, simulate.SourceHelmRelease:
		raws, warnings, err := ParseKubernetesManifest(req.Content, req.Region)
		c.logParseWarnings(warnings)
		return raws, err
	default:
		return nil, core.Invalid("compiler: unsupported source kind %q", req.Source)
	}
}

func (c *Compiler) logParseWarnings(warnings []string) {
	if c.Logger == nil {
		return
	}
	for _, w := range warnings {
		c.Logger.Warn("compiler: parse warning", "warning", w)
	}
}

func looksLikeJSON(content []byte) bool {
	for _, b := range content {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// resourceKey extracts the short name a Terraform address or CloudFormation
// logical-id-style address is keyed by, for the launch-template/task-def
// cross-reference lookups: "aws_launch_template.web" and
// "module.foo.aws_launch_template.web[0]" both key as "web"; a CloudFormation
// address of the form "MyLaunchTemplate (AWS::EC2::LaunchTemplate)" keys as
// "MyLaunchTemplate".
func resourceKey(address string) string {
	if i := strings.Index(address, " ("); i > 0 {
		return address[:i]
	}
	base := BaseAddress(address)
	if i := strings.LastIndex(base, "."); i >= 0 {
		return base[i+1:]
	}
	return base
}

// environmentAssumptionKey is the well-known key under which the compiled
// request's target environment is stashed in CompilationResult.Assumptions.
const environmentAssumptionKey = "environment"

func requestEnvironment(result simulate.CompilationResult) core.Environment {
	for _, a := range result.Assumptions {
		if a.Key == environmentAssumptionKey {
			return core.Environment(a.Value)
		}
	}
	return ""
}
