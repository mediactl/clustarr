#!/usr/bin/env bash
# hack/dev-run.sh -- the processes `clustarr all` used to be (spec 2026-10-06
# §3.10): the manager, one agent per domain and the ui, against the current
# kube context's API server and a NATS at $NATS_URL (for kind:
# `kubectl -n clustarr-system port-forward svc/nats 4222` and
# NATS_URL=nats://127.0.0.1:4222). Manager and agent code never share a
# process, as in the cluster. One Ctrl-C stops them all: they share this
# script's process group, and the trap signals the group.
#
# Run it through `make run-dev`, which builds bin/ and the native assets
# first. The import and caption agents exit at start without the native
# libraries' environment below.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
: "${NATS_URL:?export NATS_URL (kubectl -n clustarr-system port-forward svc/nats 4222; NATS_URL=nats://127.0.0.1:4222)}"
export NATS_URL
export POD_NAMESPACE="${POD_NAMESPACE:-clustarr-system}"
assets="${NATIVE_ASSETS:-$(go env GOPATH)/bin/native-assets}"
export LD_LIBRARY_PATH="${assets}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export FFGO_SHIM_DIR="${assets}/lib"
export ORT_LIB_PATH="${assets}/lib/libonnxruntime.so"
export PAR2GO_LIB="${assets}/lib/libpar2shim.so"
native_img="${NATIVE_IMG:-ghcr.io/mediactl/clustarr/native:dev}"
data_dir="${DATA_DIR:-${repo}/.data}"
index="${XDG_CACHE_HOME:-${HOME}/.cache}/clustarr/releases.db"
mkdir -p "$(dirname "${index}")" "${data_dir}"
bin="${repo}/bin"

trap 'trap - INT TERM EXIT; kill 0 2>/dev/null; wait' INT TERM EXIT

"${bin}/manager" --leader-elect=false --nats-single-node --native-image="${native_img}" --autoscale=false --external-metrics-bind-address=0 --metrics-bind-address=0 --health-probe-bind-address=:8081 --data-dir="${data_dir}" &
"${bin}/agent" --domain=catalog --metrics-bind-address=0 --health-probe-bind-address=:8082 &
"${bin}/agent" --domain=events --metrics-bind-address=0 --health-probe-bind-address=:8083 &
"${bin}/agent" --domain=metadata --metrics-bind-address=0 --health-probe-bind-address=:8084 &
"${bin}/agent" --domain=import --metrics-bind-address=0 --health-probe-bind-address=:8085 --data-dir="${data_dir}" &
"${bin}/agent" --domain=index --metrics-bind-address=0 --health-probe-bind-address=:8086 --index-path="${index}" --facade-bind-address=:9696 &
"${bin}/agent" --domain=caption --metrics-bind-address=0 --health-probe-bind-address=:8087 --data-dir="${data_dir}" &
"${bin}/ui" --auth-mode=anonymous --bind-address=:8080 &

# The first process to exit ends the session; the trap stops the rest.
wait -n
