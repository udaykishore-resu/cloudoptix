# CloudOptix — Frontend

The web console for CloudOptix, a cloud architecture-economics platform: cost
intelligence, an architecture "digital twin", savings recommendations,
economic SLOs, an architecture simulator, an infra-change cost compiler, cost
regression testing, an AI cost copilot, and policy-gated automation —
backed by the Go API in `../` (see `../api/openapi.yaml`).

Built with Next.js 14 (App Router) + TypeScript (strict) + Tailwind CSS +
shadcn/ui + TanStack Query + Recharts + React Flow.

## Getting started

```bash
npm install
npm run dev
```

Open [http://localhost:3000](http://localhost:3000). By default the app runs
in **mock mode** — no backend required — with a realistic, internally
consistent fixture dataset (accounts, resources, costs, recommendations,
SLOs, policies, audit trail, etc.) served from an in-memory "world" so every
page renders fully populated, including edge cases like a degraded AWS
connection and a breached error budget.

```bash
npm run build   # production build
npm run start   # serve the production build
npm run lint     # ESLint
npx tsc --noEmit # type-check only
```

## Environment variables

All config is read in `src/lib/api/config.ts`. Create a `.env.local` to
override any of these (all are optional):

| Variable | Default | Purpose |
| --- | --- | --- |
| `NEXT_PUBLIC_API_MODE` | `mock` | `mock` serves fixture data client-side with no network calls; `live` calls the real backend via `NEXT_PUBLIC_API_BASE_URL`. |
| `NEXT_PUBLIC_API_BASE_URL` | `http://localhost:8080/api/v1` | Base URL for the Go API, used only when `NEXT_PUBLIC_API_MODE=live`. |
| `NEXT_PUBLIC_TENANT_ID` | `tn_01hz3k4x8y` | Tenant scoping header/context for live requests. |
| `NEXT_PUBLIC_MOCK_LATENCY_MS` | `380` | Simulated network latency in mock mode, so loading skeletons and suspense states are actually visible during development rather than flashing by. |

To point the app at a running backend:

```bash
# .env.local
NEXT_PUBLIC_API_MODE=live
NEXT_PUBLIC_API_BASE_URL=http://localhost:8080/api/v1
```

## Mock mode

Every data hook in `src/lib/api/*.ts` branches on `isMock()`
(`src/lib/api/client.ts`): in mock mode it calls a pure fixture builder from
`src/lib/mock/fixtures/*.ts`, seeded from a shared synthetic "world"
(`src/lib/mock/world.ts` — accounts, resources, services, environments) so
that IDs and relationships are consistent across pages (a resource shown in
the Resource Explorer references the same recommendation, cost, and twin-node
data it would elsewhere). Mutations (approve a recommendation, execute a
plan, save a policy, etc.) mutate these same in-memory fixture objects, so
state changes are reflected across the session without a backend.

This means `npm run dev` gives a fully-featured, explorable product with
zero setup — useful for design review, demos, and frontend development
against a stable contract while the backend evolves.

## Types

API types are generated from `../api/openapi.yaml` via `openapi-typescript`
into `src/types/api.generated.ts`, and `src/types/api.ts` re-exports the
accurately-generated schemas from it. Where the OpenAPI spec under-specifies
a type — an opaque `{[key: string]: unknown}` schema for something the Go
domain models concretely (e.g. `Finding`, `RiskAssessment`, `BlastRadius`,
`Driver`, `Candidate`, `RegressionCheck`), a field the backend always
populates but the schema marks fully optional (`AuditEntry`), or a plain
string enum the schema mistakenly types as an object (`BreachAction`) — a
hand-written, more accurate type is defined in `src/types/domain.ts` instead,
each with a comment citing the Go source it's based on. Regenerate the
OpenAPI types with:

```bash
npx openapi-typescript ../api/openapi.yaml -o src/types/api.generated.ts
```

## Project structure

```
src/app/
  (app)/                 routes sharing the sidebar/topbar shell (app-shell.tsx)
    page.tsx             executive overview
    costs/                cost intelligence
    architecture/          architecture digital twin (React Flow)
    resources/             resource explorer (virtualized table)
    recommendations/       savings recommendations
    economics/              unit economics
    slos/                   cost SLOs / error budgets
    simulator/              architecture simulator
    compiler/               infra-change cost compiler
    regression/             cost regression testing
    copilot/                AI cost copilot (streaming chat)
    automation/             execution plans & autonomous runs
    approvals/              policy-gated approvals queue
    policies/               policy editor / history / simulation
    audit/                  audit trail & chain verification
    settings/               tenant, users, AWS accounts, notifications
  onboarding/              standalone chat-driven spec onboarding (no sidebar)
    connect/                 AWS account connection instructions

src/components/
  ui/                     shadcn/ui primitives
  layout/                 app shell, sidebar, topbar, theme toggle
  shared/                 domain widgets (money/confidence/risk badges,
                          sparklines, percentile & savings-funnel charts,
                          command palette, YAML viewer, empty/error states)

src/lib/
  api/                    one module per domain; TanStack Query hooks that
                          branch mock vs. live
  mock/                   fixture builders + the shared synthetic world
  utils.ts, cn, formatters

src/types/
  api.generated.ts        raw openapi-typescript output (do not hand-edit)
  api.ts                  curated re-exports of accurate generated schemas
  domain.ts               hand-written overrides for contract gaps (commented)
```

## Design system

- **Tokens**: colors, radii, and elevation are CSS custom properties defined
  in `src/app/globals.css` and mapped into `tailwind.config.ts` (e.g.
  `bg-surface`, `bg-surface-sunken`, `text-muted-foreground`, semantic
  `success` / `warning` / `danger` colors for SLO/risk/severity states).
- **Themes**: dark and light palettes are both defined as tokens; the theme
  is toggled via `ThemeToggle` (`src/components/layout/theme-toggle.tsx`)
  using `next-themes`, and honors OS preference by default.
- **Typography**: `--font-sans` / `--font-mono` CSS variables list Inter /
  JetBrains Mono first with full system-font fallback stacks. Fonts are
  loaded from local/system stacks rather than `next/font/google`, so the
  app builds and runs correctly with no external network access.
- **Density**: tables, lists and stat tiles favor a dense, data-forward
  layout in line with tools like Datadog / Grafana Cloud / AWS Cost
  Explorer, rather than a marketing-site-style layout.
- **Traceability**: monetary and confidence values are rendered through
  shared components (`MoneyDisplay`, `ConfidenceBadge`, `ProvenanceChip`)
  that consistently expose the period, freshness, and confidence/citation
  behind a number rather than a bare figure.
- **Wide-viewport views**: the architecture digital twin and other
  graph-heavy views are wrapped in `WideViewportGate`
  (`src/components/shared/wide-viewport-gate.tsx`), which shows an explicit
  message on narrow viewports instead of silently degrading a graph layout.
- **Command palette**: `⌘K` / `Ctrl+K` opens `CommandPalette`
  (`src/components/shared/command-palette.tsx`) for keyboard-first
  navigation across all pages.
