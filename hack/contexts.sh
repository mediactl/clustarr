#!/usr/bin/env bash
# hack/contexts.sh DEST FFGO_DIR FFGO_REF
#
# Exports a clean tree of the ffgo fork at its pinned tag into DEST/ffgo:
# the --build-context directory every image build gets while go.mod replaces
# ffgo with a local directory (spec 2026-10-06 §7.6 item 3, §10.1.5). A build
# context ships whatever is in its directory, and the working tree is edited
# by other sessions, so a working tree is never passed: the ref is
# `git archive`d.
#
# ffgo must be a clean checkout of FFGO_REF, because go.mod builds against
# that working tree and the image must be the same code. par2go needs no
# context: it is a published module (v0.1.0, no replace; upgrade guide U1),
# which `go mod download` fetches like any other.
set -euo pipefail
usage="usage: hack/contexts.sh DEST FFGO_DIR FFGO_REF"
dest=${1:?$usage} ffgo_dir=${2:?$usage} ffgo_ref=${3:?$usage}
die() { printf 'contexts: %s\n' "$*" >&2; exit 1; }

got=$(git -C "${ffgo_dir}" describe --tags --exact-match --dirty 2>/dev/null || true)
[[ "${got}" == "${ffgo_ref}" ]] ||
  die "${ffgo_dir} is at '${got:-no tag}', not a clean ${ffgo_ref}: check out the tag and commit or drop local edits before building images"
ffgo_commit=$(git -C "${ffgo_dir}" rev-parse "${ffgo_ref}^{commit}")

mkdir -p "${dest}"
# export_tree NAME DIR COMMIT: unpack beside the destination, then swap it in.
export_tree() {
  local tmp
  tmp=$(mktemp -d "${dest}/.$1.XXXXXX")
  git -C "$2" archive "$3" | tar -x -C "${tmp}"
  rm -rf "${dest}/.$1.old"
  if [[ -e "${dest}/$1" ]]; then mv "${dest}/$1" "${dest}/.$1.old"; fi
  mv "${tmp}" "${dest}/$1"
  rm -rf "${dest}/.$1.old"
}
export_tree ffgo "${ffgo_dir}" "${ffgo_commit}"
printf '%s\n' "${dest}/ffgo ${ffgo_ref} (${ffgo_commit})"
