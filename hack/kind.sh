#!/usr/bin/env bash
#
# hack/kind.sh -- local kind cluster for Clustarr development.
#
#   hack/kind.sh up      create the cluster, mount /data, create the namespace,
#                        bring up NATS (JetStream, 1 replica, file store PVC)
#   hack/kind.sh load    load the locally built :dev images into the cluster
#   hack/kind.sh down    delete the cluster
#
# Environment:
#   KIND_CLUSTER_NAME   cluster name                       (default: clustarr)
#   CLUSTARR_DATA_DIR   host directory mounted at /data    (default: $PWD/.data)
#   KIND_NODE_IMAGE     kindest/node image                 (default: kind's default)
#   KIND_NATS           kustomize | helm | none            (default: kustomize)
#                       kustomize: apply config/nats (the same manifests that
#                                  config/default ships, so `make deploy` is a
#                                  no-op re-apply).
#                       helm:      install nats/nats ${NATS_CHART_VERSION} into
#                                  the namespace. NOTE: config/default also
#                                  includes ../nats; drop that line (or deploy
#                                  config/manager + config/rbac only) or
#                                  `make deploy` will conflict with the Helm
#                                  release.
#   NATS_CHART_VERSION  nats Helm chart version            (default: 2.14.6)
#   IMG / MEDIA_IMG     image tags for `load`              (defaults match the Makefile)
#
# Idempotent: every step is skip-if-present or apply-if-changed.

set -euo pipefail

CLUSTER_NAME="${KIND_CLUSTER_NAME:-clustarr}"
DATA_DIR="${CLUSTARR_DATA_DIR:-${PWD}/.data}"
NODE_IMAGE="${KIND_NODE_IMAGE:-}"
NAMESPACE="clustarr-system"
KIND_NATS="${KIND_NATS:-kustomize}"
NATS_CHART_VERSION="${NATS_CHART_VERSION:-2.14.6}"
NATS_HELM_REPO="https://nats-io.github.io/k8s/helm/charts/"
IMG="${IMG:-ghcr.io/mediactl/clustarr:dev}"
MEDIA_IMG="${MEDIA_IMG:-ghcr.io/mediactl/clustarr-media:dev}"
CONTEXT="kind-${CLUSTER_NAME}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log()  { printf '==> %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}

kc() { kubectl --context "${CONTEXT}" "$@"; }

ensure_data_dir() {
  # Mirrors the §11 layout so RootFolder prefixes exist on first boot. Pods run
  # as uid/gid 1000; hostPath volumes ignore fsGroup, so make it group-writable.
  local d
  for d in torrents torrents/.state usenet media/movies media/tv media/music \
           media/books media/audiobooks media/comics .recycle; do
    mkdir -p "${DATA_DIR}/${d}"
  done
  chmod -R g+rwX "${DATA_DIR}" 2>/dev/null || true
}

create_cluster() {
  if cluster_exists; then
    log "kind cluster '${CLUSTER_NAME}' already exists, skipping create"
    return
  fi
  log "creating kind cluster '${CLUSTER_NAME}' (hostPath ${DATA_DIR} -> /data)"
  local image_line=""
  if [[ -n "${NODE_IMAGE}" ]]; then
    image_line="  image: ${NODE_IMAGE}"
  fi
  kind create cluster --name "${CLUSTER_NAME}" --wait 120s --config=- <<KIND
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
${image_line}
  extraMounts:
  - hostPath: ${DATA_DIR}
    containerPath: /data
KIND
}

ensure_namespace() {
  log "ensuring namespace ${NAMESPACE}"
  kc create namespace "${NAMESPACE}" --dry-run=client -o yaml | kc apply -f -
}

ensure_data_pv() {
  # kind's default StorageClass (rancher local-path) refuses ReadWriteMany, so
  # pre-create a hostPath PV pre-bound to the clustarr-data claim that
  # config/default creates. It carries the default class name so the claim
  # binds statically instead of triggering dynamic provisioning.
  log "ensuring hostPath PersistentVolume clustarr-data"
  kc apply -f - <<PV
apiVersion: v1
kind: PersistentVolume
metadata:
  name: clustarr-data
  labels:
    app.kubernetes.io/part-of: clustarr
spec:
  storageClassName: standard
  capacity:
    storage: 500Gi
  accessModes:
  - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  claimRef:
    namespace: ${NAMESPACE}
    name: clustarr-data
  hostPath:
    path: /data
    type: DirectoryOrCreate
PV
}

install_nats_kustomize() {
  log "applying config/nats (JetStream, 1 replica, file store PVC)"
  kc apply --server-side -k "${REPO_ROOT}/config/nats"
}

install_nats_helm() {
  need helm
  log "installing nats/nats ${NATS_CHART_VERSION} via Helm"
  helm repo add nats "${NATS_HELM_REPO}" --force-update >/dev/null
  helm repo update nats >/dev/null
  helm --kube-context "${CONTEXT}" upgrade --install nats nats/nats \
    --version "${NATS_CHART_VERSION}" \
    --namespace "${NAMESPACE}" \
    --values - <<VALUES
# Single replica, JetStream on, file store in a PVC (R1 on kind, §3/§11).
config:
  cluster:
    enabled: false
  jetstream:
    enabled: true
    fileStore:
      enabled: true
      pvc:
        enabled: true
        size: 20Gi
    memoryStore:
      enabled: false
  monitor:
    enabled: true
    port: 8222
container:
  merge:
    resources:
      requests: {cpu: 100m, memory: 256Mi}
      limits: {cpu: "1", memory: 1Gi}
natsBox:
  enabled: true
promExporter:
  enabled: false
reloader:
  enabled: false
VALUES
}

wait_for_nats() {
  log "waiting for statefulset/nats"
  kc -n "${NAMESPACE}" rollout status statefulset/nats --timeout=180s
}

cmd_up() {
  need kind
  need kubectl
  ensure_data_dir
  create_cluster
  kc cluster-info >/dev/null
  ensure_namespace
  ensure_data_pv
  case "${KIND_NATS}" in
    kustomize) install_nats_kustomize; wait_for_nats ;;
    helm)      install_nats_helm;      wait_for_nats ;;
    none)      log "KIND_NATS=none: skipping NATS" ;;
    *)         die "KIND_NATS must be kustomize, helm or none (got '${KIND_NATS}')" ;;
  esac

  cat >&2 <<NEXT

kind cluster '${CLUSTER_NAME}' is ready (context ${CONTEXT}).
  /data on the node  -> ${DATA_DIR}
  NATS               -> nats://nats.${NAMESPACE}.svc:4222 (${KIND_NATS})

Next:
  make docker-build          # build ${IMG} and ${MEDIA_IMG}
  hack/kind.sh load          # load them into the cluster
  make install               # apply CRDs   (kustomize build config/crd)
  make deploy                # apply config/default (namespace, PVCs, RBAC, managers, NATS)
  kubectl -n ${NAMESPACE} get pods -w
NEXT
}

cmd_load() {
  need kind
  need docker
  cluster_exists || die "kind cluster '${CLUSTER_NAME}' does not exist; run 'hack/kind.sh up'"
  local img
  for img in "${IMG}" "${MEDIA_IMG}"; do
    if docker image inspect "${img}" >/dev/null 2>&1; then
      log "loading ${img}"
      kind load docker-image --name "${CLUSTER_NAME}" "${img}"
    else
      log "image ${img} not found locally, skipping (run 'make docker-build')"
    fi
  done
}

cmd_down() {
  need kind
  if cluster_exists; then
    log "deleting kind cluster '${CLUSTER_NAME}'"
    kind delete cluster --name "${CLUSTER_NAME}"
  else
    log "kind cluster '${CLUSTER_NAME}' does not exist, nothing to do"
  fi
  log "data directory ${DATA_DIR} left in place"
}

usage() {
  sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

main() {
  case "${1:-}" in
    up)          cmd_up ;;
    load)        cmd_load ;;
    down)        cmd_down ;;
    -h|--help|help) usage 0 ;;
    "")          usage 1 ;;
    *)           die "unknown subcommand '${1}' (expected up, load or down)" ;;
  esac
}

main "$@"
