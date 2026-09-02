# syntax=docker/dockerfile:1.7

# CloudOptix API/worker image. Three binaries ship in one image:
#
#   /app/cloudoptix-api      the HTTP API server
#   /app/cloudoptix-worker   the background cycles, selected by --workers
#   /app/coptx               the CLI (spec, cost, policy, demo)
#
# One image rather than three because they share the entire application
# library — three images would be three copies of the same ~40MB of compiled
# Go differing only in which main package is linked, and three separate
# things to keep at the same version. Which binary a container runs is a
# `command` in the Deployment (see helm/cloudoptix/values.yaml), which is
# also what lets a debugging shell-less container run `coptx` against the
# same build the API is running.
#
# The API applies the embedded migrations itself at startup when
# storage=postgres (see internal/app/storage.go), so there is no separate
# `migrate` entrypoint: a schema check that runs in a different process from
# the one that needs the schema can succeed while the pod still starts
# against the wrong one.

ARG GO_VERSION=1.25
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# ---------------------------------------------------------------------------
# deps: cached separately from the source copy so `docker build` only
# re-downloads modules when go.mod/go.sum actually change.
# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION}-bookworm AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# ---------------------------------------------------------------------------
# build: CGO disabled — the final stage has no C library, and CloudOptix's
# dependencies (database/sql driver, AWS SDK, JWT/crypto) are all pure Go —
# so a static binary is both correct and what makes the distroless
# static-nonroot base below possible at all.
# ---------------------------------------------------------------------------
FROM golang:${GO_VERSION}-bookworm AS build
ARG VERSION
ARG COMMIT
ARG BUILD_DATE
WORKDIR /src
COPY --from=deps /go/pkg/mod /go/pkg/mod
COPY go.mod go.sum ./
COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    for pkg in cloudoptix-api cloudoptix-worker coptx; do \
      go build \
        -trimpath \
        -ldflags "-s -w \
          -X main.version=${VERSION} \
          -X main.commit=${COMMIT} \
          -X main.buildDate=${BUILD_DATE}" \
        -o "/out/${pkg}" \
        "./cmd/${pkg}" || exit 1; \
    done

# ---------------------------------------------------------------------------
# final: distroless static, nonroot variant — no shell, no package manager,
# no libc, and runs as uid/gid 65532 by construction (not by a USER
# directive we could get wrong). This is the smallest attack surface
# available for a statically-linked Go binary; helm/cloudoptix's
# securityContext (readOnlyRootFilesystem, all capabilities dropped) is
# built assuming exactly this base.
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS final

ARG VERSION
LABEL org.opencontainers.image.title="cloudoptix" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/udaykishore-resu/cloudoptix" \
      org.opencontainers.image.licenses="Apache-2.0"

# Embedded migrations (migrations/embed.go) are compiled into the binary
# via go:embed — nothing to copy here beyond the binary itself. The base
# config.yaml the config package's doc comment describes as "ships with the
# image" is instead supplied by the chart's rendered ConfigMap volume
# mount (see helm/cloudoptix/templates/configmap.yaml); no default
# config.yaml is baked into this image, so `--config` is always required
# at runtime.
COPY --from=build /out/cloudoptix-api /app/cloudoptix-api
COPY --from=build /out/cloudoptix-worker /app/cloudoptix-worker
COPY --from=build /out/coptx /app/coptx
# api/openapi.yaml is served verbatim from GET /openapi.yaml. internal/app
# looks for it beside the executable as well as in the working directory,
# which is why it lands in /app rather than /app/api.
COPY --from=build /src/api/openapi.yaml /app/openapi.yaml

USER 65532:65532
WORKDIR /app
EXPOSE 8080
# The API is the default because it is what `docker run cloudoptix` should
# do. A worker or the CLI is selected by overriding `command`, which is how
# helm/cloudoptix's per-component Deployments pick theirs.
ENTRYPOINT ["/app/cloudoptix-api"]
CMD []
