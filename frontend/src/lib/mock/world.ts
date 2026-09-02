/**
 * The mock world: one deterministic, internally-consistent estate that every
 * fixture generator (costs, recommendations, twin, economics, ...) reads
 * from, so the numbers agree with each other across pages the way a real
 * backend's would.
 */
import type { Resource, TwinEdge } from "@/types/domain";
import { KIND_CATALOG, KIND_CATEGORY } from "./kinds";
import { ri, rf, rpick, rbool, rskew, rid, resetRng } from "./rng";

export interface Account {
  id: string;
  accountId: string;
  alias: string;
  environment: "production" | "staging" | "development" | "shared_services";
  regions: string[];
  isPayer: boolean;
  state: "connected" | "degraded" | "pending";
  grantedScopes: ("read" | "analyze" | "plan" | "execute")[];
  missingActions: string[];
  connectedAt: string;
  lastVerifiedAt: string;
}

export interface Workload {
  id: string;
  applicationId: string;
  name: string;
  resourceIds: string[];
}

export interface Application {
  id: string;
  name: string;
  description: string;
  criticality: "TIER_0" | "TIER_1" | "TIER_2" | "TIER_3";
  owner: string;
  team: string;
  workloadIds: string[];
}

export const ACCOUNTS: Account[] = [
  {
    id: "acct_prod01",
    accountId: "411223344556",
    alias: "prod",
    environment: "production",
    regions: ["us-east-1", "us-west-2"],
    isPayer: true,
    state: "connected",
    grantedScopes: ["read", "analyze", "plan", "execute"],
    missingActions: [],
    connectedAt: "2026-03-14T09:00:00Z",
    lastVerifiedAt: "2026-08-31T04:00:00Z",
  },
  {
    id: "acct_stg01",
    accountId: "411223344557",
    alias: "staging",
    environment: "staging",
    regions: ["us-east-1"],
    isPayer: false,
    state: "connected",
    grantedScopes: ["read", "analyze", "plan", "execute"],
    missingActions: [],
    connectedAt: "2026-03-14T09:04:00Z",
    lastVerifiedAt: "2026-08-31T04:00:00Z",
  },
  {
    id: "acct_dev01",
    accountId: "411223344558",
    alias: "dev",
    environment: "development",
    regions: ["us-east-1"],
    isPayer: false,
    state: "connected",
    grantedScopes: ["read", "analyze", "plan"],
    missingActions: ["ec2:ModifyInstanceAttribute", "rds:ModifyDBInstance"],
    connectedAt: "2026-03-14T09:05:00Z",
    lastVerifiedAt: "2026-08-31T04:00:00Z",
  },
  {
    id: "acct_shared01",
    accountId: "411223344559",
    alias: "shared-services",
    environment: "shared_services",
    regions: ["us-east-1", "eu-west-1"],
    isPayer: false,
    state: "degraded",
    grantedScopes: ["read", "analyze"],
    missingActions: ["ec2:StopInstances", "ec2:TerminateInstances", "rds:ModifyDBInstance", "s3:PutLifecycleConfiguration"],
    connectedAt: "2026-03-14T09:07:00Z",
    lastVerifiedAt: "2026-08-30T22:00:00Z",
  },
];

const APP_DEFS: { name: string; desc: string; crit: Application["criticality"]; team: string; owner: string }[] = [
  { name: "checkout", desc: "Cart, payment authorization and order capture", crit: "TIER_0", team: "commerce-platform", owner: "priya.nair@acme.io" },
  { name: "catalog", desc: "Product catalog, search and pricing", crit: "TIER_0", team: "commerce-platform", owner: "priya.nair@acme.io" },
  { name: "identity", desc: "Authentication, sessions and account management", crit: "TIER_0", team: "platform-security", owner: "marcus.reyes@acme.io" },
  { name: "fulfillment", desc: "Warehouse routing and shipment tracking", crit: "TIER_1", team: "logistics", owner: "hana.kobayashi@acme.io" },
  { name: "notifications", desc: "Email, SMS and push delivery", crit: "TIER_1", team: "growth-eng", owner: "leo.fischer@acme.io" },
  { name: "analytics", desc: "Event pipeline and BI warehouse loads", crit: "TIER_2", team: "data-platform", owner: "sofia.moreau@acme.io" },
  { name: "internal-tools", desc: "Support console and admin dashboards", crit: "TIER_2", team: "platform-eng", owner: "devon.clarke@acme.io" },
  { name: "recommendations", desc: "Personalization and ranking models", crit: "TIER_1", team: "ml-platform", owner: "amara.okafor@acme.io" },
];

interface World {
  accounts: Account[];
  applications: Application[];
  workloads: Workload[];
  resources: Resource[];
  edges: TwinEdge[];
  sharedResourceIds: string[];
}

let cached: World | null = null;

function isoDaysAgo(days: number, jitterHours = 0): string {
  const now = new Date("2026-08-31T06:00:00Z").getTime();
  const t = now - days * 86400000 + (jitterHours ? ri(-jitterHours, jitterHours) * 3600000 : 0);
  return new Date(t).toISOString();
}

function pickAccountForEnv(env: string): Account {
  return ACCOUNTS.find((a) => a.environment === env) ?? ACCOUNTS[0];
}

function newResourceBase(
  kind: string,
  account: Account,
  region: string,
  app: Application | null,
  workload: Workload | null,
  env: string
): Resource {
  const spec = KIND_CATALOG[kind];
  const cost = spec.costRange[1] === 0 ? 0 : rskew(spec.costRange[0], spec.costRange[1], 1.6);
  const idle = rbool(0.16);
  const util = idle ? rskew(1, 12, 1.2) : rskew(15, 92, 0.8);
  const nativeId = `${kind.split(".")[1]}-${rid("").slice(1, 9)}`;
  return {
    id: rid("res"),
    tenant_id: "tn_01hz3k4x8y",
    account_id: account.accountId,
    region,
    availability_zone: `${region}${rpick(["a", "b", "c"])}`,
    kind,
    arn: `arn:aws:${spec.service.toLowerCase()}:${region}:${account.accountId}:${nativeId}`,
    native_id: nativeId,
    name: `${app ? app.name + "-" : ""}${spec.label.toLowerCase().replace(/\s+/g, "-")}-${ri(1, 99)}`,
    state: idle ? "idle" : "running",
    instance_type: undefined,
    capacity: {},
    purchase_model: "on_demand",
    tags: {
      Environment: env,
      Application: app?.name ?? "platform",
      Team: app?.team ?? "platform-eng",
      ManagedBy: "terraform",
    },
    environment: env,
    environment_source: "CONFIRMED",
    application_id: app?.id,
    application: app?.name,
    workload_id: workload?.id,
    workload: workload?.name,
    owner: app?.owner ?? "platform-eng@acme.io",
    cost_center: app ? `CC-${app.team.toUpperCase()}` : "CC-PLATFORM",
    criticality: app?.criticality ?? "TIER_2",
    attributes: {},
    first_seen_at: isoDaysAgo(ri(90, 420)),
    last_seen_at: isoDaysAgo(0, 2),
    discovered_by: "ec2-adapter",
    deleted: false,
    monthly_cost: { micros: Math.round(cost * 1_000_000), currency: "USD", amount: cost, display: fmtUSD(cost) },
    cost_source: "CONFIRMED",
    cpu: { min: Math.max(0, util - 8), p50: util, p90: Math.min(100, util + 18), p95: Math.min(100, util + 24), p99: Math.min(100, util + 30), max: Math.min(100, util + 35) },
    memory: { min: Math.max(0, util - 4), p50: Math.min(100, util + 6), p90: Math.min(100, util + 20), p95: Math.min(100, util + 26), p99: Math.min(100, util + 30), max: Math.min(100, util + 33) },
    finding_count: 0,
    potential_saving: { micros: 0, currency: "USD", amount: 0, display: "$0.00" },
  };
}

function fmtUSD(v: number): string {
  return `$${v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

function attachFinding(r: Resource, savingFraction: number) {
  const saving = r.monthly_cost.amount * savingFraction;
  r.finding_count = (r.finding_count ?? 0) + 1;
  const prior = r.potential_saving?.amount ?? 0;
  const total = prior + saving;
  r.potential_saving = { micros: Math.round(total * 1_000_000), currency: "USD", amount: total, display: fmtUSD(total) };
}

function buildWorld(): World {
  resetRng(20260831);
  const applications: Application[] = APP_DEFS.map((d) => ({
    id: rid("app"),
    name: d.name,
    description: d.desc,
    criticality: d.crit,
    owner: d.owner,
    team: d.team,
    workloadIds: [],
  }));

  const workloads: Workload[] = [];
  const resources: Resource[] = [];
  const edges: TwinEdge[] = [];
  const sharedResourceIds: string[] = [];

  const workloadNamesFor = (app: string) => {
    const map: Record<string, string[]> = {
      checkout: ["cart-api", "payments-api", "order-writer"],
      catalog: ["catalog-api", "search-indexer"],
      identity: ["auth-api", "session-store"],
      fulfillment: ["routing-engine", "tracking-api"],
      notifications: ["dispatch-worker", "template-api"],
      analytics: ["event-collector", "warehouse-loader"],
      "internal-tools": ["admin-console", "support-api"],
      recommendations: ["ranking-service", "feature-store"],
    };
    return map[app] ?? [`${app}-api`];
  };

  applications.forEach((app, ai) => {
    const names = workloadNamesFor(app.name);
    names.forEach((wname, wi) => {
      const env = wi === names.length - 1 && ai % 3 === 0 ? "staging" : "production";
      const account = pickAccountForEnv(env);
      const region = rpick(account.regions);
      const wl: Workload = { id: rid("wl"), applicationId: app.id, name: wname, resourceIds: [] };
      workloads.push(wl);
      app.workloadIds.push(wl.id);

      const addRes = (kind: string, count = 1) => {
        const out: Resource[] = [];
        for (let i = 0; i < count; i++) {
          const r = newResourceBase(kind, account, region, app, wl, env);
          resources.push(r);
          wl.resourceIds.push(r.id);
          out.push(r);
        }
        return out;
      };

      // Entry tier
      const isApi = rbool(0.55);
      const entry = isApi ? addRes("aws.apigateway.api", 1) : addRes("aws.elbv2.application", 1);

      // Compute tier
      const useServerless = app.name === "notifications" || app.name === "analytics" || rbool(0.35);
      let compute: Resource[];
      if (useServerless) {
        compute = addRes("aws.lambda.function", ri(2, 4));
      } else if (rbool(0.4)) {
        compute = addRes("aws.ecs.service", ri(1, 2));
      } else {
        compute = addRes("aws.ec2.instance", ri(2, 5));
        // attach EBS to each EC2
        compute.forEach((c) => {
          const [vol] = addRes("aws.ebs.volume", 1);
          edges.push({ from: vol.id, to: c.id, kind: "attached_to", weight: 1, confidence: 1 });
        });
      }
      compute.forEach((c) => edges.push({ from: entry[0].id, to: c.id, kind: "routes_to", weight: rf(), confidence: 0.9 + rf() * 0.1 }));

      // Data tier
      const useDynamo = rbool(0.4);
      const data = useDynamo ? addRes("aws.dynamodb.table", 1) : addRes("aws.rds.instance", 1);
      compute.forEach((c) => edges.push({ from: c.id, to: data[0].id, kind: "reads_from", weight: rf(), confidence: 0.85 + rf() * 0.15 }));

      if (rbool(0.5)) {
        const [cache] = addRes("aws.elasticache.cluster", 1);
        compute.forEach((c) => edges.push({ from: c.id, to: cache.id, kind: "reads_from", weight: rf() * 0.6, confidence: 0.8 }));
      }
      if (rbool(0.35)) {
        const [q] = addRes("aws.sqs.queue", 1);
        compute.forEach((c) => edges.push({ from: c.id, to: q.id, kind: "writes_to", weight: rf() * 0.4, confidence: 0.75 }));
      }
      if (rbool(0.3)) {
        const [bucket] = addRes("aws.s3.bucket", 1);
        compute.forEach((c) => edges.push({ from: c.id, to: bucket.id, kind: "writes_to", weight: rf() * 0.3, confidence: 0.7 }));
      }
      const [logs] = addRes("aws.logs.log_group", 1);
      [...compute, ...data].forEach((c) => edges.push({ from: c.id, to: logs.id, kind: "writes_to", weight: 0.15, confidence: 0.95 }));
    });
  });

  // Shared platform resources, owned by the shared-services account.
  const sharedAccount = ACCOUNTS[3];
  const sharedKinds: [string, number][] = [
    ["aws.ec2.nat_gateway", 3],
    ["aws.ec2.vpc", 2],
    ["aws.ec2.vpc_endpoint", 4],
    ["aws.eks.cluster", 1],
    ["aws.eks.nodegroup", 2],
    ["aws.msk.cluster", 1],
    ["aws.cloudfront.distribution", 2],
    ["aws.s3.bucket", 3],
    ["aws.logs.log_group", 2],
    ["aws.kms.key", 4],
    ["aws.secretsmanager.secret", 6],
    ["aws.ebs.snapshot", 8],
    ["aws.rds.snapshot", 5],
    ["aws.ec2.elastic_ip", 3],
    ["aws.ec2.image", 6],
  ];
  sharedKinds.forEach(([kind, count]) => {
    for (let i = 0; i < count; i++) {
      const region = rpick(sharedAccount.regions);
      const r = newResourceBase(kind, sharedAccount, region, null, null, "shared_services");
      resources.push(r);
      sharedResourceIds.push(r.id);
    }
  });
  // NAT gateways carry indirect egress cost for every production workload —
  // wire every production compute node to a shared NAT so the economics
  // engine has something to attribute indirect cost through.
  const nats = resources.filter((r) => r.kind === "aws.ec2.nat_gateway");
  const prodCompute = resources.filter((r) => r.environment === "production" && ["compute"].includes(KIND_CATEGORY(r.kind)));
  prodCompute.forEach((c) => {
    const nat = rpick(nats);
    edges.push({ from: c.id, to: nat.id, kind: "depends_on", weight: rf() * 0.5, confidence: 0.7 });
  });

  // Attach findings deterministically: every idle/low-utilisation resource
  // gets a potential saving so recommendations, resources and economics all
  // agree on which resources are wasteful.
  resources.forEach((r) => {
    const cpu = r.cpu?.p50 ?? 50;
    if (r.state === "idle" || cpu < 8) {
      attachFinding(r, rskew(0.55, 0.95, 1));
    } else if (cpu < 22 && rbool(0.5)) {
      attachFinding(r, rskew(0.25, 0.5, 1));
    } else if (r.kind === "aws.ebs.snapshot" || r.kind === "aws.ec2.image") {
      if (rbool(0.4)) attachFinding(r, rskew(0.6, 1, 1));
    } else if (r.kind === "aws.ec2.elastic_ip" && rbool(0.3)) {
      attachFinding(r, 1);
    } else if (rbool(0.08)) {
      attachFinding(r, rskew(0.1, 0.3, 1));
    }
  });

  return { accounts: ACCOUNTS, applications, workloads, resources, edges, sharedResourceIds };
}

export function getWorld(): World {
  if (!cached) cached = buildWorld();
  return cached;
}

export function moneyOf(amount: number): Resource["monthly_cost"] {
  return { micros: Math.round(amount * 1_000_000), currency: "USD", amount, display: fmtUSD(amount) };
}
export { fmtUSD };
