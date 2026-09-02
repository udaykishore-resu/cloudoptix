package onboarding

// fieldSchema describes one extractable property: its JSON Schema type and
// the human label used everywhere else in this package (questions, review
// sections, FieldState labels), so the two never drift apart.
type fieldSchema struct {
	Name        string // JSON Schema property name; must match internal/adapters/llm/deterministic's extractor registry.
	Type        string // "string" | "array" | "number" | "boolean"
	Items       string // element type when Type == "array"
	Description string
}

// allFields is the complete extractable field set, in the order the
// deterministic provider's registry and the review UI both use. A field
// absent from a given turn's schema (see buildSchema) is simply not asked
// about that turn; nothing here ever changes based on stage, only which
// subset is requested.
var allFields = []fieldSchema{
	{"organization_name", "string", "", "The customer's company or organization name."},
	{"industry", "string", "", "The company's industry sector."},
	{"company_size", "string", "", "Company size: startup, small, midmarket or enterprise."},
	{"business_regions", "array", "string", "Geographic regions the business operates in."},
	{"application_name", "string", "", "The name of the application or platform being optimized."},
	{"application_description", "string", "", "A one-sentence description of what the application does."},
	{"domain", "string", "", "The application's business domain, e.g. checkout, payments, claims."},
	{"architecture_style", "string", "", "Overall architecture style: microservices, monolith or serverless."},
	{"compute_platforms", "array", "string", "AWS compute platforms in use: eks, ecs, fargate, lambda, ec2."},
	{"databases", "array", "string", "Databases in use."},
	{"caches", "array", "string", "Caching layers in use."},
	{"messaging", "array", "string", "Messaging/queueing services in use."},
	{"aws_account_ids", "array", "string", "Twelve-digit AWS account IDs in scope."},
	{"aws_regions", "array", "string", "AWS region codes in scope."},
	{"environments", "array", "string", "Deployment environments: production, staging, development, test, sandbox."},
	{"business_transactions", "array", "object", "Named business transactions with their monthly volume."},
	{"cost_reduction_target", "number", "", "Target cost reduction as a fraction, e.g. 0.25 for 25%."},
	{"monthly_budget", "number", "", "Target monthly AWS spend in US dollars."},
	{"availability_target", "number", "", "Target availability as a fraction, e.g. 0.999."},
	{"max_latency_ms", "number", "", "Maximum acceptable latency in milliseconds."},
	{"risk_tolerance", "string", "", "Appetite for optimization risk: low, medium or high."},
	{"spot_allowed", "boolean", "", "Whether EC2 Spot capacity may be used."},
	{"automation_enabled", "boolean", "", "Whether CloudOptix may execute approved changes automatically."},
	{"governance_requires_approval", "boolean", "", "Whether production changes require human approval."},
	{"compliance_frameworks", "array", "string", "Compliance frameworks the estate must meet."},
}

// fieldByName indexes allFields for quick lookup while applying an
// extraction result.
var fieldByName = func() map[string]fieldSchema {
	m := make(map[string]fieldSchema, len(allFields))
	for _, f := range allFields {
		m[f.Name] = f
	}
	return m
}()

// buildSchema renders a JSON Schema object requesting exactly the named
// properties. Passing the full field set every turn (rather than narrowing
// to the current stage) is what lets an answer volunteered ahead of
// schedule still be captured: the schema never forbids a property, it only
// ever omits one this package has no further interest in.
func buildSchema(names []string) map[string]any {
	props := make(map[string]any, len(names))
	for _, n := range names {
		f, ok := fieldByName[n]
		if !ok {
			continue
		}
		prop := map[string]any{"type": f.Type, "description": f.Description}
		if f.Type == "array" {
			itemType := f.Items
			if itemType == "" {
				itemType = "string"
			}
			prop["items"] = map[string]any{"type": itemType}
		}
		props[n] = prop
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
	}
}

// fullSchema requests every field this package knows how to extract. Every
// Send call uses it: the agent always looks at the whole conversation for
// everything it might care about, never just the fields for the current
// stage.
func fullSchema() map[string]any {
	names := make([]string, len(allFields))
	for i, f := range allFields {
		names[i] = f.Name
	}
	return buildSchema(names)
}
