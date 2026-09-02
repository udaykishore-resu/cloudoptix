#!/usr/bin/env bash
# check-architecture-deps.sh — enforces the one dependency rule this
# codebase's layering depends on: internal/domain and internal/application
# must never import internal/adapters. Everything in domain/application is
# supposed to be adapter-agnostic business logic, wired to a concrete
# adapter (Postgres, the real AWS SDK, awssim, memstore) only at the
# composition root, internal/app — the one package permitted to import
# both sides, and the reason this check can stay a blunt rule with a
# single documented exemption rather than a graph of allowances. An import
# that violates this is a structural regression: it means a "swap Postgres
# for something else in tests" or "run the whole platform against awssim"
# story has quietly stopped being true for at least one package, and
# nobody will notice until a test that assumed it breaks in a confusing
# way, far from the actual cause.
#
# Usage: .github/scripts/check-architecture-deps.sh
# Exit status: 0 if clean, 1 and a listing of every violation otherwise.
# Run from the repository root (CI does; a human can too, from anywhere —
# it locates the repo root itself via `go list`).

set -euo pipefail

MODULE="github.com/udaykishore-resu/cloudoptix"
FORBIDDEN_PREFIX="${MODULE}/internal/adapters"
VIOLATIONS=0

if ! command -v go >/dev/null 2>&1; then
  echo "error: go is required on PATH to run this check" >&2
  exit 2
fi

REPO_ROOT="$(go env GOMOD 2>/dev/null | xargs -r dirname || true)"
if [[ -z "${REPO_ROOT}" || ! -d "${REPO_ROOT}/internal/domain" ]]; then
  # go env GOMOD needs to run from inside the module; fall back to
  # searching upward from this script's own location.
  REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fi
cd "${REPO_ROOT}"

echo "Checking internal/domain and internal/application for imports of internal/adapters..."
echo "(module: ${MODULE})"
echo

for LAYER in internal/domain internal/application; do
  if [[ ! -d "${LAYER}" ]]; then
    continue
  fi
  # `go list -f` prints one package's full import list per invocation;
  # -deps would pull in the transitive closure (stdlib, third-party) which
  # is both slower and not what this check is about — we only care about
  # DIRECT imports naming internal/adapters anywhere in the import path,
  # since an indirect import (domain -> application -> adapters, if that
  # ever existed) would already be flagged at the layer that imports
  # adapters directly.
  while IFS= read -r PKG; do
    [[ -z "${PKG}" ]] && continue
    IMPORTS="$(go list -f '{{join .Imports "\n"}}' "${PKG}" 2>/dev/null || true)"
    while IFS= read -r IMPORT; do
      [[ -z "${IMPORT}" ]] && continue
      if [[ "${IMPORT}" == "${FORBIDDEN_PREFIX}"* ]]; then
        echo "VIOLATION: ${PKG#$MODULE/} imports ${IMPORT#$MODULE/}"
        VIOLATIONS=$((VIOLATIONS + 1))
      fi
    done <<< "${IMPORTS}"
  done < <(go list "./${LAYER}/..." 2>/dev/null || true)
done

echo
if [[ "${VIOLATIONS}" -gt 0 ]]; then
  echo "FAILED: ${VIOLATIONS} import(s) of internal/adapters found inside internal/domain or internal/application."
  echo "Move the adapter-specific logic behind an internal/ports interface and inject the concrete adapter at the composition root instead."
  exit 1
fi

echo "OK: no internal/domain or internal/application package imports internal/adapters."
