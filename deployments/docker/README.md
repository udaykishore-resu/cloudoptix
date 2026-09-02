# deployments/docker

## `Dockerfile` — the Go binary

Multi-stage: a cached `deps` stage, a `build` stage producing a static,
stripped binary (`CGO_ENABLED=0`, `-ldflags "-s -w -X main.version=..."`),
and a `gcr.io/distroless/static-debian12:nonroot` final stage — no shell,
no package manager, uid/gid 65532 by construction.

Build with version stamping:

```sh
docker build \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse HEAD) \
  --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -f deployments/docker/Dockerfile -t cloudoptix .
```

### The three binaries

The image contains three `main` packages, all built from the same
application library:

| Binary | Package | What it does |
|---|---|---|
| `/app/cloudoptix-api` | `cmd/cloudoptix-api` | Serves the HTTP API. Applies the embedded migrations at startup on the Postgres path, then listens. `--seed-demo` seeds the demo tenant (idempotent); `--migrate-only` applies migrations and exits, which is what the chart's pre-deploy hook runs. |
| `/app/cloudoptix-worker` | `cmd/cloudoptix-worker` | Runs background cycles, selected with `--workers=discovery,cost,optimization,automation,validation,notification,learning` (default all). `--once` runs each selected cycle once and exits, for a CronJob. |
| `/app/coptx` | `cmd/coptx` | The CLI: `spec validate`, `spec diff`, `cost compile`, `cost test`, `policy validate`, `policy simulate`, `demo seed`, `demo run`, `version`. |

`ENTRYPOINT` is the API, so `docker run cloudoptix` serves the API. A worker
or the CLI is selected by overriding `command` — see
`helm/cloudoptix/values.yaml`, where every component names its binary in
`command` and its own flags in `args`.

There is deliberately no `migrate` binary. The API applies migrations itself
at startup (`internal/app/storage.go`), and a separate migration binary would
be a second implementation of the same step, free to drift from the one that
actually gates whether a pod can serve traffic. `--migrate-only` runs that
exact code path and exits.

### Configuration

Everything is driven by `internal/infrastructure/config`. The four settings
that decide what the process runs against:

| Variable | Values | Default |
|---|---|---|
| `CLOUDOPTIX_STORAGE` | `memory` \| `postgres` | `memory` |
| `CLOUDOPTIX_CACHE` | `memory` \| `redis` | `memory` |
| `CLOUDOPTIX_EVENTS` | `inprocess` \| `aws` | `inprocess` |
| `CLOUDOPTIX_AWS_MODE` | `simulated` \| `live` | `simulated` |

The defaults are the zero-infrastructure demo path, and
`config.Config.Validate` refuses `memory` storage, `simulated` AWS and the
`scripted` LLM provider when `CLOUDOPTIX_ENVIRONMENT=production` — so those
defaults are convenient locally and impossible to ship by accident.

## `Dockerfile.frontend` — the Next.js app

Standard three-stage Node build (`deps` → `build` → `final`) rather than
Next's `output: "standalone"` minimal-runtime pattern, because
`frontend/next.config.mjs` does not set `output: "standalone"` and this
change does not touch frontend source (same scope boundary as above,
applied to the frontend instead of the Go backend). Adding that one line
to `next.config.mjs` would let a follow-up change shrink this image by
copying only `.next/standalone` + `.next/static` instead of the full
`node_modules`.

Build-time `NEXT_PUBLIC_API_BASE_URL` bakes the API's public URL into the
static bundle at build time (standard Next.js behaviour for
`NEXT_PUBLIC_*` variables — they are not runtime-configurable once built),
so a distinct image per environment's API URL is the expected shape unless
a later change moves the frontend to fetch that value at runtime instead.
