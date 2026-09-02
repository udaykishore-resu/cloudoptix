/**
 * Friendly aliases over the generated OpenAPI types (api.generated.ts) for
 * the schemas that ARE fully and accurately specified in api/openapi.yaml.
 *
 * A number of nested schemas are declared opaque in the OpenAPI document
 * even though the underlying Go structs are fully typed, and TwinEdge's
 * documented fields don't match its actual json tags — those are typed by
 * hand in ./domain.ts instead, from the Go source directly. See that file's
 * header comment for the full list. Anything not overridden there is
 * generated faithfully from openapi.yaml here and should stay that way —
 * re-run `npm run gen:api` after any contract change rather than hand-editing
 * api.generated.ts.
 */
import type { components, operations, paths } from "./api.generated";

export type Schemas = components["schemas"];
export type { operations, paths };

// --- Shared value objects ---------------------------------------------------
export type Money = Schemas["Money"];
export type Period = Schemas["Period"];
export type Percentiles = Schemas["Percentiles"];
export type Confidence = Schemas["Confidence"];
export type Severity = Schemas["Severity"];
export type RiskLevel = Schemas["RiskLevel"];
export type Criticality = Schemas["Criticality"];
export type Provenance = Schemas["Provenance"];
export type Tags = Record<string, string>;
export type ValidationIssue = Schemas["ValidationIssue"];
export type ValidationResult = Schemas["ValidationResult"];
export type Problem = Schemas["Problem"];
export type PageEnvelope<T> = { items: T[]; next_cursor?: string; total?: number };

// --- Onboarding --------------------------------------------------------------
export type OnboardingState = Schemas["OnboardingState"];
export type OnboardingSummary = Schemas["OnboardingSummary"];
export type OnboardingResult = Schemas["OnboardingResult"];
export type FieldState = Schemas["FieldState"];
export type OpenQuestion = Schemas["OpenQuestion"];
export type SummarySection = Schemas["SummarySection"];
export type Spec = Schemas["Spec"];
export type SpecVersion = Schemas["SpecVersion"];
export type SpecCompleteness = Schemas["SpecCompleteness"];
export type SpecChange = Schemas["SpecChange"];
export type AWSOnboardingInstructions = Schemas["AWSOnboardingInstructions"];

// --- Tenancy / accounts --------------------------------------------------------
export type Tenant = Schemas["Tenant"];
export type User = Schemas["User"];
export type Membership = Schemas["Membership"];
export type AWSAccount = Schemas["AWSAccount"];

// --- Discovery -------------------------------------------------------------
export type DiscoveryRun = Schemas["DiscoveryRun"];
export type DiscoveryStatus = Schemas["DiscoveryStatus"];
export type ServiceScanResult = Schemas["ServiceScanResult"];

// --- Cost intelligence -----------------------------------------------------
export type CostSummary = Schemas["CostSummary"];
export type CostSeries = Schemas["CostSeries"];
export type CostPoint = Schemas["CostPoint"];
export type CostBreakdown = Schemas["CostBreakdown"];
export type BreakdownItem = Schemas["BreakdownItem"];
export type CostForecast = Schemas["CostForecast"];
export type CostAnomaly = Schemas["CostAnomaly"];
export type CostExplanation = Schemas["CostExplanation"];
export type Contribution = Schemas["Contribution"];
export type LinkedChange = Schemas["LinkedChange"];
export type IngestResult = Schemas["IngestResult"];

// --- Economics ---------------------------------------------------------------
export type Footprint = Schemas["Footprint"];
export type FootprintComponent = Schemas["FootprintComponent"];
export type EconomicsScope = Schemas["EconomicsScope"];
export type BusinessTransaction = Schemas["BusinessTransaction"];
// NOTE: UnitEconomics, Driver, CostSLO, BreachAction, EconomicErrorBudget,
// ExecutiveSummary, EfficiencyScore and EfficiencyFactor are all hand-typed
// in ./domain — see that file's header comment for why.
export type EconomicsResult = Schemas["EconomicsResult"];

// --- Recommendations (requests only; response bodies are in ./domain) -------
export type AnalyzeRequest = Schemas["AnalyzeRequest"];
export type AnalyzeResult = Schemas["AnalyzeResult"];

// --- Copilot ---------------------------------------------------------------
export type CopilotAnswer = Schemas["CopilotAnswer"];
export type Citation = Schemas["Citation"];
export type ToolResult = Schemas["ToolResult"];
export type RetrievedDocument = Schemas["RetrievedDocument"];
export type Conversation = Schemas["Conversation"];

// --- Audit -------------------------------------------------------------------
export type AuditEntry = Schemas["AuditEntry"];

// --- Health --------------------------------------------------------------
export type WhoAmIResponse = Schemas["WhoAmIResponse"];
