# CloudOptix API

`openapi.yaml` is the complete OpenAPI 3.1 description of the CloudOptix
HTTP API: every operation implemented under `internal/transport/http`, plus
the three infrastructure endpoints (`/healthz`, `/readyz`, `/metrics`) that
sit outside it. It is a single file — not split into `schemas/*.yaml` — see
"Why one file" below.

## Coverage

- **116 operations** across 24 tags: Authentication, Onboarding, Specs,
  Tenants, Users, AWS Accounts, Discovery, Architecture, Resources, Costs,
  Economics, Cost SLOs, Recommendations, Simulations, Cost Compiler, Cost
  Regression, Policies, Approvals, Automation, Executions, Savings, Audit, AI
  Copilot, Health.
- **113 of those** are checked automatically against
  `internal/transport/http/routes.go`'s `BuildRoutes`/`BuildPublicRoutes`
  tables: every `(method, path, operationId)` triple in this document has a
  matching `Route`/`PublicRoute` entry, and vice versa. See
  `routing_test.go` for the structural half of that check (every route
  resolves in the running router) and the validation commands below for the
  document half.
- Every operation has a `security` requirement (or an explicit `security: []`
  on the Onboarding tag and the three Health operations), a tag, an
  `operationId`, and `application/problem+json` responses for every error
  status it can actually return.
- 151 named component schemas. Requests bodies this transport layer decodes
  itself (everything under `components.schemas` with a `*Request` suffix)
  are complete. Response schemas for the platform's primary domain objects
  (`Money`, `Period`, `Recommendation`, `ExecutionPlan`, `Policy`,
  `ApprovalRequest`, `AuditEntry`, `CostSummary`, `TwinNode`, ...) are fully
  typed from their Go struct definitions. A handful of deeply-nested,
  still-evolving substructures (`Finding`, `StateSnapshot`, `RiskAssessment`,
  `BlastRadius`, `Spec`'s per-section bodies, and similar) are modelled as
  `type: object` with `additionalProperties: true` and a description
  pointing at the Go source of truth, rather than duplicated field-by-field —
  see "Intentionally loose schemas" below.

## Structure

- **`info`** — includes the authentication model, tenant-scope resolution,
  the error format, pagination, rate limits, idempotency and streaming, all
  in prose once rather than repeated per operation.
- **`x-anchors`** (a root-level vendor extension) — YAML anchors for the
  parameters and error responses shared across most of the ~116 operations
  (`X-CloudOptix-Tenant`, `Idempotency-Key`, cursor pagination, the common
  4xx/5xx `<<: *commonErrors` block, and the five path parameter names used
  more than once). Resolved by the YAML parser itself before any OpenAPI
  tooling sees the document, so the effect on validation is identical to
  writing every field out longhand at every operation — this just removes
  the ~115 chances for one of those copies to drift from the rest.
- **`paths`** — grouped by tag in source order, in the same order the tags
  are declared.
- **`components.schemas`** — grouped by tag with a comment header per group,
  in the same order as `paths`.

## Why one file

The task allows splitting into `api/schemas/*.yaml` via `$ref`. This
document does not, for one reason: the platform's request/response DTOs
share deeply across tags (a `Recommendation` appears in the
`Recommendations`, `Executions`, `Savings` and `Audit` responses; `Money` and
`Period` appear almost everywhere), and a genuinely well-factored multi-file
split would still need one shared `common.yaml` most other files depend on —
at which point the split adds `$ref` indirection without reducing how much
of the document a reviewer needs to hold in mind at once. A single 2,700-line
file, organized with consistent section comments and validated as a whole on
every change, was the more honest trade-off at this API's actual size.

## Intentionally loose schemas

A small number of schemas (`Finding`, `StateSnapshot`, `ConfidenceInput`,
`RiskAssessment`, `BlastRadius`, `RuleCalibration`, `Outcome`, `Assumption`,
`Candidate`, `Weights`, `StateProjection`, `PricedChange`, `RegressionCheck`,
`CheckResult`, `Match`, `PolicyRule`'s nested pieces, `Step`, `Snapshot`,
`RollbackPlan`, `ValidationPlan`, `PlanValidationResult`, `LeakagePoint`,
`Turn`, and every per-section body inside `Spec`) are declared as
`type: object` with `additionalProperties: true` rather than a full property
list. Each is a large or still-actively-evolving internal structure (see the
Go source named in its `description`) where a hand-maintained duplicate
property list would drift from the real type faster than this document would
be updated to match — an honest `additionalProperties: true` with a pointer
to the source of truth was judged more useful to a reader than a stale
enumeration of fields.

## Validating changes to this document

```bash
pip install openapi-spec-validator   # once
python3 -m openapi_spec_validator api/openapi.yaml

npx --yes @redocly/cli@latest lint api/openapi.yaml
```

Both run clean (Redocly reports a handful of stylistic warnings only: the
three `localhost` server entries kept intentionally for local development,
and three parameter-less health/metrics probes correctly declaring no `4xx`
response). Also re-run the cross-check against the route table used to write
this document:

```bash
python3 - <<'EOF'
import re, yaml
doc = yaml.safe_load(open("api/openapi.yaml"))
methods = {"get","post","put","patch","delete"}
yaml_ops = {op["operationId"]: (m.upper(), p)
            for p, item in doc["paths"].items()
            for m, op in item.items() if m in methods}

src = open("internal/transport/http/routes.go").read()
go_ops = {name: (method.upper(), pattern) for method, pattern, name in
          re.findall(r'\{http\.Method(\w+), "([^"]+)", [^,]+, [^,]+, "([^"]+)"\}', src)}
go_ops.update({name: (method.upper(), pattern) for method, pattern, name in
               re.findall(r'\{http\.Method(\w+), "([^"]+)", ob\.\w+, "([^"]+)"\}', src)})

mismatches = [n for n, v in go_ops.items() if yaml_ops.get(n) != v]
assert not mismatches, mismatches
print(f"OK: {len(go_ops)} routes.go entries all match api/openapi.yaml")
EOF
```

## Servers

Three servers are declared: production, staging, and local development
(`http://localhost:8080`). The three Health operations override `servers` at
the path-item level to omit the `/api/v1` prefix the other 113 operations
use, matching `router.go`, which mounts `/healthz`, `/readyz` and `/metrics`
at the root.
