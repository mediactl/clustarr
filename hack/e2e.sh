#!/usr/bin/env bash
#
# hack/e2e.sh -- the one command Phase H's gate asks for: bring up kind, build
# and load every image (including the fixtures), install the CRDs, apply
# config/e2e, wait for readiness, seed the probe clip onto /data, run
# `make e2e`, and on any failure dump diagnostics into test/e2e/artifacts/.
#
# A scenario never installs anything itself (test/e2e/main_test.go's TestMain
# refuses to run otherwise) -- every step below is what makes that true.
#
# Requires: kind, kubectl, docker, go, kustomize. The machine needs Internet
# for the one-time image builds (apt, Go modules); nothing that runs INSIDE
# the cluster ever reaches the network.
#
# Environment:
#   KIND_CLUSTER_NAME   cluster name                    (default: clustarr)
#   CLUSTARR_DATA_DIR   host dir mounted at /data       (default: <repo>/.data)
#   FIXTURES_IMG        the fixture image tag           (default: ghcr.io/mediactl/clustarr/e2e-fixtures:dev)
#   E2E_SKIP_BUILD      set to 1 to reuse existing images and a running cluster
#   E2E_ARGS            extra args appended to `go test` (e.g. -run TestLibraryRescan)

set -uo pipefail # deliberately not -e: the test step's exit code is captured, not fatal

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}" || exit 1

CLUSTER_NAME="${KIND_CLUSTER_NAME:-clustarr}"
NAMESPACE="clustarr-system"
CONTEXT="kind-${CLUSTER_NAME}"
# Exported, and never left to hack/kind.sh's $PWD-relative default: test/e2e
# reads this to translate a cluster path under /data into a host path, and it
# must mean the same directory in both processes.
export CLUSTARR_DATA_DIR="${CLUSTARR_DATA_DIR:-${REPO_ROOT}/.data}"
FIXTURES_IMG="${FIXTURES_IMG:-ghcr.io/mediactl/clustarr/e2e-fixtures:dev}"
ARTIFACTS_DIR="${REPO_ROOT}/test/e2e/artifacts"
KUSTOMIZE="$(go env GOPATH)/bin/kustomize"

# Every workload readiness waits on, in the order it is most useful to see
# fail: the stubs first (a broken fixture image is the cheapest failure), then
# the services.
WORKLOADS=(
  deployment/tmdb-stub
  deployment/tvdb-stub
  deployment/torznab-stub
  deployment/seeder
  deployment/nntp-stub-a
  deployment/nntp-stub-b
  deployment/catalogarr
  deployment/catalogarr-metadata
  deployment/importarr
  deployment/importarr-worker
  deployment/indexarr
  deployment/grabarr
  deployment/squasharr
  deployment/captionarr
  deployment/captionarr-worker
  deployment/ui
  statefulset/nats
)

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

need kind
need kubectl
need docker
need go
[[ -x "${KUSTOMIZE}" ]] || need kustomize
[[ -x "${KUSTOMIZE}" ]] || KUSTOMIZE="$(command -v kustomize)"

if [[ "${E2E_SKIP_BUILD:-0}" != "1" ]]; then
  log "cluster up (hack/kind.sh)"
  make kind-up || die "hack/kind.sh up failed"

  log "building the controller, media and fixture images"
  make docker-build || die "make docker-build failed"
  docker build -f images/Dockerfile.e2e-fixtures -t "${FIXTURES_IMG}" . \
    || die "fixtures image build failed"

  log "loading images into kind"
  hack/kind.sh load || die "hack/kind.sh load failed"
  kind load docker-image --name "${CLUSTER_NAME}" "${FIXTURES_IMG}" \
    || die "loading the fixtures image failed"

  log "installing CRDs"
  make install || die "make install failed"
fi

log "applying config/e2e"
"${KUSTOMIZE}" build config/e2e | kubectl --context "${CONTEXT}" apply --server-side --force-conflicts -f - \
  || die "config/e2e apply failed"

log "seeding the probe clip into ${CLUSTARR_DATA_DIR}/.e2e-fixtures"
mkdir -p "${CLUSTARR_DATA_DIR}"
# --user: the fixture image runs as uid 65532, but the hostPath directory
# belongs to whoever ran hack/kind.sh, and a hostPath mount ignores fsGroup.
docker run --rm --user "$(id -u):$(id -g)" \
  -v "${CLUSTARR_DATA_DIR}:/data" \
  "${FIXTURES_IMG}" seed --dir=/data/.e2e-fixtures \
  || die "seeding the fixture clip failed"

log "waiting for every workload in ${NAMESPACE}"
for workload in "${WORKLOADS[@]}"; do
  kubectl --context "${CONTEXT}" -n "${NAMESPACE}" rollout status "${workload}" --timeout=300s \
    || die "${workload} never became ready"
done

# `make e2e` is a plain `go test` with no -count=1, and Go will happily replay
# a cached PASS for an unchanged test binary -- reporting green without ever
# touching the cluster. The Makefile target's contract is fixed, so drop the
# cached results here instead. It costs the next `make test` a recompile.
log "clearing the go test cache so make e2e cannot replay a cached PASS"
go clean -testcache || die "go clean -testcache failed"

# Always go through the Makefile target: it is the one definition of the e2e
# `go test` line, and E2E_ARGS is forwarded as a make command-line variable so
# this script never has to restate (and eventually drift from) that command.
log "running make e2e"
make e2e E2E_ARGS="${E2E_ARGS:-}"
status=$?

if [[ "${status}" -ne 0 ]]; then
  log "the e2e suite failed (exit ${status}); dumping diagnostics into ${ARTIFACTS_DIR}"
  mkdir -p "${ARTIFACTS_DIR}"

  for workload in "${WORKLOADS[@]}"; do
    name="${workload##*/}"
    kubectl --context "${CONTEXT}" -n "${NAMESPACE}" logs "${workload}" \
      --all-containers --tail=-1 --prefix >"${ARTIFACTS_DIR}/${name}.log" 2>&1
    # A CrashLoopBackOff is the failure mode that has actually bitten this
    # harness -- both the single-node JetStream sizing defect and the missing
    # RBAC rules presented that way -- and the current container of a crash
    # loop holds nothing: the output that names the cause belongs to the
    # container that already died. --previous is best effort (it errors when
    # there is no prior container, which is the healthy case), so its file is
    # removed again when it holds only that error.
    if ! kubectl --context "${CONTEXT}" -n "${NAMESPACE}" logs "${workload}" \
      --all-containers --tail=-1 --prefix --previous \
      >"${ARTIFACTS_DIR}/${name}.previous.log" 2>&1; then
      rm -f "${ARTIFACTS_DIR}/${name}.previous.log"
    fi
  done

  kubectl --context "${CONTEXT}" -n "${NAMESPACE}" get events --sort-by=.lastTimestamp \
    >"${ARTIFACTS_DIR}/events.txt" 2>&1
  kubectl --context "${CONTEXT}" -n "${NAMESPACE}" get pods -o wide \
    >"${ARTIFACTS_DIR}/pods.txt" 2>&1
  # `get pods -o wide` shows restart counts but not WHY: OOMKilled, a failed
  # probe or an unpullable image are only in describe's per-container Last
  # State and Events.
  kubectl --context "${CONTEXT}" -n "${NAMESPACE}" describe pods \
    >"${ARTIFACTS_DIR}/pods-describe.txt" 2>&1

  : >"${ARTIFACTS_DIR}/resources.yaml"
  for group in catalog.clustarr.io index.clustarr.io download.clustarr.io \
      transcode.clustarr.io subtitle.clustarr.io; do
    kinds="$(kubectl --context "${CONTEXT}" api-resources --api-group="${group}" -o name | paste -sd, -)"
    if [[ -n "${kinds}" ]]; then
      kubectl --context "${CONTEXT}" -n "${NAMESPACE}" get "${kinds}" -o yaml \
        >>"${ARTIFACTS_DIR}/resources.yaml" 2>&1
    fi
  done

  # The fixture indexer's request log is the only record of what indexarr
  # actually ASKED it. Without it, "the Search returned nothing" cannot be
  # told apart from "indexarr never issued a query", and by the time anyone
  # looks the cluster is usually gone.
  if [[ -f "${CLUSTARR_DATA_DIR}/.e2e-fixtures/torznab/requests.jsonl" ]]; then
    cp "${CLUSTARR_DATA_DIR}/.e2e-fixtures/torznab/requests.jsonl" \
       "${ARTIFACTS_DIR}/torznab-requests.jsonl" 2>/dev/null || true
  fi

  # Same reasoning, for the two usenet fixtures: the only record of which
  # of nntp-stub-a/nntp-stub-b actually served or denied a given article on
  # this run (test/e2e/download_test.go's cross-server-failover proof).
  for name in nntp-a nntp-b; do
    if [[ -f "${CLUSTARR_DATA_DIR}/.e2e-fixtures/${name}/requests.jsonl" ]]; then
      cp "${CLUSTARR_DATA_DIR}/.e2e-fixtures/${name}/requests.jsonl" \
         "${ARTIFACTS_DIR}/${name}-requests.jsonl" 2>/dev/null || true
    fi
  done

  # NATS's monitor port is not published by kind, so reach /jsz through a
  # port-forward rather than assuming the host can route to the ClusterIP.
  kubectl --context "${CONTEXT}" -n "${NAMESPACE}" port-forward svc/nats 18222:8222 >/dev/null 2>&1 &
  pf_pid=$!
  sleep 2
  curl -s "http://127.0.0.1:18222/jsz?streams=true&consumers=true" \
    >"${ARTIFACTS_DIR}/nats-jsz.json" 2>&1 || true
  kill "${pf_pid}" 2>/dev/null || true
  wait "${pf_pid}" 2>/dev/null

  log "diagnostics written to ${ARTIFACTS_DIR}"
fi

exit "${status}"
