import type { AWSOnboardingInstructions, FieldState, OnboardingState, OnboardingSummary, OpenQuestion, SummarySection } from "@/types/api";
import type { Turn } from "@/types/domain";

const STAGES = ["organization", "application", "aws", "workloads", "business", "objectives", "governance", "review"] as const;
type Stage = (typeof STAGES)[number];

interface ConvState {
  turnIndex: number;
  turns: Turn[];
  stage: Stage;
}

const conversations = new Map<string, ConvState>();

const SCRIPT: { agentOpening?: string; ask: string; suggestions: string[]; collect: FieldState[]; infer: FieldState[]; needsConfirm: FieldState[]; unknown: FieldState[]; openQuestions: OpenQuestion[] }[] = [
  {
    agentOpening:
      "Hi — I'm the CloudOptix onboarding assistant. I'll build your architecture-economics specification through a short conversation, then you'll review and approve it before anything connects to AWS. Let's start with the basics: what's your organization called, and what does the team building on AWS do?",
    ask: "Got it. Which application or platform should CloudOptix start with — the one whose cost and architecture matter most right now?",
    suggestions: ["Acme Corp, we run an e-commerce platform", "We're a B2B SaaS company", "Skip — use what you can infer"],
    collect: [{ path: "organization.name", label: "Organization name", value: "Acme Corp", provenance: "CONFIRMED" }],
    infer: [{ path: "organization.industry", label: "Industry", value: "E-commerce / retail", provenance: "INFERRED", rationale: "Inferred from the organization description and typical AWS service usage patterns for this vertical." }],
    needsConfirm: [],
    unknown: [{ path: "organization.size", label: "Organization size", provenance: "UNKNOWN" }],
    openQuestions: [],
  },
  {
    ask: "Thanks. Now the AWS side: how many AWS accounts do you run, and do you use AWS Organizations with a dedicated payer account?",
    suggestions: ["checkout — our commerce platform", "We have one main app called Acme Platform", "Multiple applications, let's start broad"],
    collect: [{ path: "application.name", label: "Primary application", value: "checkout", provenance: "CONFIRMED" }],
    infer: [{ path: "application.criticality", label: "Criticality", value: "TIER_0", provenance: "INFERRED", rationale: "Named as the primary application and described as revenue-critical — treated as Tier 0 pending your confirmation." }],
    needsConfirm: [{ path: "application.criticality", label: "Criticality tier", value: "TIER_0", provenance: "REQUIRES_USER_CONFIRMATION", rationale: "Please confirm this is your highest-criticality tier before we apply stricter governance defaults to it." }],
    unknown: [],
    openQuestions: [{ path: "application.owner", question: "Who owns this application day to day?", why: "Used to route approvals and notifications.", required: false, blocking: false }],
  },
  {
    ask: "Understood. Roughly what does a typical month of AWS spend look like, and do you already have any cost or reliability targets in mind — a monthly budget ceiling, for instance?",
    suggestions: ["4 accounts, prod/staging/dev + a shared-services account", "Single account for now", "We use AWS Organizations, payer is our shared-services account"],
    collect: [
      { path: "aws.account_count", label: "AWS accounts", value: "4", provenance: "CONFIRMED" },
      { path: "aws.uses_organizations", label: "AWS Organizations", value: "Yes", provenance: "CONFIRMED" },
    ],
    infer: [{ path: "aws.primary_region", label: "Primary region", value: "us-east-1", provenance: "INFERRED", rationale: "Inferred as the primary region from account naming conventions; confirm if traffic is concentrated elsewhere." }],
    needsConfirm: [],
    unknown: [{ path: "aws.regions", label: "All active regions", provenance: "UNKNOWN" }],
    openQuestions: [],
  },
  {
    ask: "That's helpful context. Last governance question: should CloudOptix be allowed to automatically apply low-risk, reversible changes (like deleting unattached volumes), or should everything go through human approval to start?",
    suggestions: ["~$180K/month, target ceiling around $210K", "No hard budget yet", "We want p99 latency under 200ms on checkout"],
    collect: [{ path: "business.declared_monthly_spend", label: "Declared monthly spend", value: "$180,000", provenance: "CONFIRMED" }],
    infer: [{ path: "objectives.cost_ceiling", label: "Suggested cost SLO ceiling", value: "$210,000/mo", provenance: "INFERRED", rationale: "Set at roughly 15% above declared spend as a starting error-budget ceiling — adjust in the SLO screen after approval." }],
    needsConfirm: [{ path: "objectives.cost_ceiling", label: "Cost SLO ceiling", value: "$210,000/mo", provenance: "REQUIRES_USER_CONFIRMATION", rationale: "This becomes an enforced Cost SLO once approved — please confirm the number before we activate it." }],
    unknown: [],
    openQuestions: [{ path: "objectives.latency_target", question: "Is there a latency or availability target we should track alongside cost?", why: "Feeds the Cost SLO and validation-check defaults.", required: false, blocking: false }],
  },
  {
    ask: "One more thing before we move to review: any resource types or accounts that should never be touched automatically, even with the safest policy?",
    suggestions: ["Auto-execute low-risk, reversible changes in non-prod only", "Require approval for everything to start", "Auto-execute everywhere policy allows it"],
    collect: [{ path: "automation.enabled", label: "Automation enabled", value: "Yes — non-production only", provenance: "CONFIRMED" }],
    infer: [{ path: "governance.default_policy", label: "Default policy stance", value: "balanced", provenance: "INFERRED", rationale: "Balanced stance selected based on your automation preference — you can switch to conservative or aggressive any time after approval." }],
    needsConfirm: [],
    unknown: [],
    openQuestions: [],
  },
  {
    ask: "Thanks — that's everything CloudOptix needs to draft a specification. I've put together a summary on the right. Take a look, and when you're ready, approve it to generate your AWS connection instructions, or keep chatting if anything needs to change.",
    suggestions: ["Looks good — take me to review", "Change the default policy to conservative", "What happens if I approve this?"],
    collect: [{ path: "governance.protected_resources", label: "Protected resources", value: "Production RDS clusters, payer account", provenance: "CONFIRMED" }],
    infer: [],
    needsConfirm: [],
    unknown: [],
    openQuestions: [],
  },
];

function computeCompleteness(state: ConvState) {
  const idx = Math.min(state.turnIndex, SCRIPT.length - 1);
  const confirmed = SCRIPT.slice(0, idx + 1).reduce((s, x) => s + x.collect.length, 0);
  const inferred = SCRIPT.slice(0, idx + 1).reduce((s, x) => s + x.infer.length, 0);
  const needsConfirmation = SCRIPT.slice(0, idx + 1).reduce((s, x) => s + x.needsConfirm.length, 0);
  const unknown = SCRIPT.slice(0, idx + 1).reduce((s, x) => s + x.unknown.length, 0);
  const total = 18;
  const score = Math.min(1, (confirmed + inferred * 0.7) / total);
  return { total_fields: total, confirmed, inferred, unknown, needs_confirmation: needsConfirmation, score, ready_for_review: idx >= SCRIPT.length - 1, blocking_gaps: idx >= SCRIPT.length - 1 ? [] : ["Governance stance not yet confirmed"] };
}

function aggregateBuckets(state: ConvState) {
  const idx = Math.min(state.turnIndex, SCRIPT.length - 1);
  const slice = SCRIPT.slice(0, idx + 1);
  return {
    collected: slice.flatMap((s) => s.collect),
    inferred: slice.flatMap((s) => s.infer),
    needsConfirmation: slice.flatMap((s) => s.needsConfirm),
    unknown: slice.flatMap((s) => s.unknown),
    openQuestions: slice.flatMap((s) => s.openQuestions),
  };
}

function buildDraftSpec(state: ConvState) {
  const b = aggregateBuckets(state);
  const get = (path: string) => b.collected.find((f) => f.path === path)?.value ?? b.inferred.find((f) => f.path === path)?.value;
  return {
    api_version: "cloudoptix.io/v1",
    kind: "Specification",
    organization: { name: get("organization.name") ?? "Acme Corp", industry: get("organization.industry") ?? "E-commerce / retail" },
    application: { name: get("application.name") ?? "checkout", criticality: get("application.criticality") ?? "TIER_0" },
    aws: { account_count: 4, uses_organizations: true, primary_region: get("aws.primary_region") ?? "us-east-1" },
    business: { declared_monthly_spend: get("business.declared_monthly_spend") ?? "$180,000" },
    objectives: { cost_ceiling: get("objectives.cost_ceiling") ?? "$210,000/mo" },
    automation: { enabled: true, scope: "non_production_only" },
    governance: { default_policy: get("governance.default_policy") ?? "balanced", protected_resources: ["production RDS clusters", "payer account"] },
    open_questions: b.openQuestions,
  };
}

export function startConversation(): { id: string; state: OnboardingState } {
  const id = `conv_${Date.now().toString(36)}`;
  const st: ConvState = { turnIndex: 0, turns: [{ role: "assistant", text: SCRIPT[0].agentOpening!, at: new Date().toISOString() }], stage: "organization" };
  conversations.set(id, st);
  return { id, state: toState(id, st, SCRIPT[0].agentOpening!) };
}

function toState(id: string, st: ConvState, reply: string): OnboardingState {
  const b = aggregateBuckets(st);
  const idx = Math.min(st.turnIndex, SCRIPT.length - 1);
  return {
    conversation_id: id,
    reply,
    stage: STAGES[idx],
    draft: buildDraftSpec(st) as unknown as OnboardingState["draft"],
    completeness: computeCompleteness(st) as unknown as OnboardingState["completeness"],
    collected: b.collected as unknown as OnboardingState["collected"],
    inferred: b.inferred as unknown as OnboardingState["inferred"],
    unknown: b.unknown as unknown as OnboardingState["unknown"],
    needs_confirmation: b.needsConfirmation as unknown as OnboardingState["needs_confirmation"],
    open_questions: b.openQuestions as unknown as OnboardingState["open_questions"],
    validation: { issues: [] },
    ready_for_review: idx >= SCRIPT.length - 1,
    suggestions: SCRIPT[Math.min(st.turnIndex, SCRIPT.length - 1)].suggestions,
    degraded: false,
  };
}

export function sendMessage(id: string, message: string): OnboardingState {
  const st = conversations.get(id);
  if (!st) throw new Error("conversation not found");
  st.turns.push({ role: "user", text: message, at: new Date().toISOString() });
  const wasLast = st.turnIndex >= SCRIPT.length - 1;
  const nextIdx = Math.min(st.turnIndex + 1, SCRIPT.length - 1);
  const reply = wasLast
    ? "The specification is ready for review whenever you are — use the review panel to approve, edit, or keep chatting."
    : SCRIPT[nextIdx].ask;
  st.turnIndex = nextIdx;
  st.turns.push({ role: "assistant", text: reply, at: new Date().toISOString() });
  return toState(id, st, reply);
}

export function getState(id: string): OnboardingState {
  const st = conversations.get(id);
  if (!st) throw new Error("conversation not found");
  const lastAssistant = [...st.turns].reverse().find((t) => t.role === "assistant");
  return toState(id, st, lastAssistant?.text ?? "");
}

export function getTurns(id: string): Turn[] {
  return conversations.get(id)?.turns ?? [];
}

const SPEC_YAML = `api_version: cloudoptix.io/v1
kind: Specification
organization:
  name: Acme Corp
  industry: E-commerce / retail
application:
  name: checkout
  criticality: TIER_0            # requires_user_confirmation
aws:
  account_count: 4
  uses_organizations: true
  primary_region: us-east-1      # inferred
business:
  declared_monthly_spend: "$180,000"
objectives:
  cost_ceiling: "$210,000/mo"    # requires_user_confirmation
automation:
  enabled: true
  scope: non_production_only
governance:
  default_policy: balanced
  protected_resources:
    - production RDS clusters
    - payer account
`;

export function buildSummary(id: string): OnboardingSummary {
  const st = conversations.get(id);
  if (!st) throw new Error("conversation not found");
  const b = aggregateBuckets(st);
  const sections: SummarySection[] = [
    { title: "Organization", fields: b.collected.filter((f) => f.path?.startsWith("organization")) },
    { title: "Application", fields: [...b.collected, ...b.needsConfirmation].filter((f) => f.path?.startsWith("application")) },
    { title: "AWS footprint", fields: [...b.collected, ...b.inferred].filter((f) => f.path?.startsWith("aws")) },
    { title: "Business & objectives", fields: [...b.collected, ...b.needsConfirmation].filter((f) => f.path?.startsWith("business") || f.path?.startsWith("objectives")) },
    { title: "Governance", fields: [...b.collected, ...b.inferred].filter((f) => f.path?.startsWith("governance") || f.path?.startsWith("automation")) },
  ];
  return {
    conversation_id: id,
    spec: buildDraftSpec(st) as unknown as OnboardingSummary["spec"],
    spec_yaml: SPEC_YAML,
    completeness: computeCompleteness(st) as unknown as OnboardingSummary["completeness"],
    validation: { issues: [{ path: "application.criticality", code: "requires_confirmation", message: "Criticality tier needs your explicit confirmation before governance defaults apply", severity: "MEDIUM", hint: "Confirm or edit in the field list above" }] },
    sections,
    what_happens_next: [
      "A new tenant is created and this specification becomes its active, versioned configuration.",
      "You'll be taken to the AWS connection screen to create a least-privilege IAM role in each account.",
      "Once verified, CloudOptix runs an initial discovery scan — read-only, no changes are made to your infrastructure.",
      "The balanced governance policy activates: reversible, low-risk changes in non-production may auto-execute; everything else requires approval.",
      "Nothing is deleted, stopped, resized or purchased until you explicitly approve a recommendation or execution plan.",
    ],
    can_approve: true,
    blocking_reasons: [],
  };
}

export function buildAwsInstructions(): AWSOnboardingInstructions {
  const externalId = "cldx-ext-7f3a9c21b6e84d0a";
  return {
    external_id: externalId,
    trusted_principal_arn: "arn:aws:iam::984212004471:role/cloudoptix-platform-execution",
    role_names: { read: "CloudOptixReadOnlyRole", analyze: "CloudOptixAnalyzeRole", plan: "CloudOptixPlanRole", execute: "CloudOptixExecuteRole" } as unknown as AWSOnboardingInstructions["role_names"],
    policy_documents: {
      read: JSON.stringify(
        {
          Version: "2012-10-17",
          Statement: [
            { Sid: "ReadOnlyDiscovery", Effect: "Allow", Action: ["ec2:Describe*", "rds:Describe*", "s3:GetBucket*", "s3:ListBucket", "lambda:GetFunction*", "lambda:ListFunctions", "dynamodb:Describe*", "dynamodb:ListTables", "cloudwatch:GetMetric*", "cloudwatch:ListMetrics", "ce:GetCostAndUsage", "tag:GetResources"], Resource: "*" },
          ],
        },
        null,
        2
      ),
      analyze: JSON.stringify({ Version: "2012-10-17", Statement: [{ Sid: "CostAndUsageAnalysis", Effect: "Allow", Action: ["ce:GetCostAndUsage", "ce:GetCostForecast", "ce:GetReservationUtilization", "cur:DescribeReportDefinitions", "s3:GetObject"], Resource: "*" }] }, null, 2),
      plan: JSON.stringify({ Version: "2012-10-17", Statement: [{ Sid: "DryRunPlanning", Effect: "Allow", Action: ["ec2:*"], Resource: "*", Condition: { Bool: { "aws:RequestTag/DryRun": "true" } } }] }, null, 2),
      execute: JSON.stringify(
        {
          Version: "2012-10-17",
          Statement: [
            { Sid: "ExecuteApprovedChanges", Effect: "Allow", Action: ["ec2:ModifyInstanceAttribute", "ec2:StopInstances", "ec2:DeleteVolume", "ec2:ModifyVolume", "ec2:ReleaseAddress", "rds:ModifyDBInstance", "s3:PutLifecycleConfiguration", "logs:PutRetentionPolicy", "lambda:UpdateFunctionConfiguration"], Resource: "*", Condition: { StringEquals: { "aws:PrincipalTag/CloudOptixApproved": "true" } } },
          ],
        },
        null,
        2
      ),
    } as unknown as AWSOnboardingInstructions["policy_documents"],
    cloudformation_url: `https://console.aws.amazon.com/cloudformation/home#/stacks/create?stackName=CloudOptix-Integration&templateURL=https://cloudoptix-cf-templates.s3.amazonaws.com/v1/cloudoptix-role.yaml&param_ExternalId=${externalId}`,
    terraform_module: `module "cloudoptix" {\n  source              = "cloudoptix/integration/aws"\n  version             = "~> 1.4"\n  external_id         = "${externalId}"\n  trusted_principal   = "arn:aws:iam::984212004471:role/cloudoptix-platform-execution"\n  enabled_scopes      = ["read", "analyze", "plan", "execute"]\n}`,
    instructions: [
      "Deploy the CloudFormation stack (or Terraform module) in each AWS account you want CloudOptix to manage.",
      "Each scope creates its own IAM role — read and analyze are required; plan and execute are optional and can be granted later.",
      "The external ID above must match exactly; it prevents the confused-deputy problem in cross-account role assumption.",
      "Verification checks each role's trust policy and attached permissions against what CloudOptix expects, and lists anything missing.",
    ],
  };
}
