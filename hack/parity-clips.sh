#!/usr/bin/env bash
# Copyright 2026 The Clustarr Authors. SPDX-License-Identifier: GPL-3.0-or-later
#
# parity-clips.sh cuts the ffgo parity harness's inputs (test/parity): the
# first 60 seconds, stream-copied, of one library file per class the
# standard tells apart, chosen from a cluster's MediaFile probe summaries
# (the median-sized Matroska file of each class; a file squasharr or
# another tool already transcoded is never chosen). It also writes the
# hevc-mkv TranscodeProfile's spec as profile.json, which the argv engine
# runs with. Stream copy only: no encode, little CPU.
#
#   CLUSTARR_PARITY_DIR=~/parity KUBE_CONTEXT=kind-cluster-plex hack/parity-clips.sh
#
# The library is read through the kind node's NFS mount of /data, so the
# clips need no mount on this host.
set -euo pipefail
dir=${CLUSTARR_PARITY_DIR:?set CLUSTARR_PARITY_DIR}
ctx=${KUBE_CONTEXT:-kind-cluster-plex}
node=${KIND_NODE:-cluster-plex-control-plane}
profile=${PROFILE:-hevc-mkv}
mkdir -p "$dir"

kubectl --context "$ctx" get transcodeprofile "$profile" -o jsonpath='{.spec}' > "$dir/profile.json"
mount=$(docker exec "$node" sh -c "grep ' nfs4\? ' /proc/mounts | grep -m1 '/volume2/data ' | cut -d' ' -f2")
[ -n "$mount" ] || { echo "no NFS mount of the library on $node" >&2; exit 1; }

kubectl --context "$ctx" get mediafiles -A -o json | python3 -c '
import json, sys
classes = {}
for m in json.load(sys.stdin)["items"]:
    mi = m.get("status", {}).get("mediaInfo") or {}
    p, size = m["spec"]["path"], m["spec"].get("sizeBytes", 0)
    if not mi or not p.endswith(".mkv") or mi.get("transcodeProfile") or "Lavc" in (mi.get("videoEncoder") or ""):
        continue
    audio = [a.get("codec") for a in mi.get("audio", [])]
    subs = [s.get("codec") for s in mi.get("subtitles", [])]
    vc, bd, hdr, dv = mi.get("videoCodec"), mi.get("videoBitDepth"), mi.get("hdr"), mi.get("doviProfile")
    def add(c):
        classes.setdefault(c, []).append((size, p))
    if vc == "h264" and bd == 8 and hdr == "none": add("sdr-h264")
    if vc == "hevc" and bd == 8: add("hevc8")
    if hdr == "hdr10" and dv is None: add("hdr10")
    if hdr == "hdr10plus": add("hdr10plus")
    if dv == 7: add("dv7")
    if dv == 8 and mi.get("doviBLCompatID") == 1: add("dv81")
    if dv == 5: add("dv5")
    if vc == "h264" and bd == 10: add("hi10p")
    if "truehd" in audio and "eac3" in audio: add("truehd-eac3")
    if "hdmv_pgs_subtitle" in subs: add("pgs")
    if mi.get("attachments", 0) > 0: add("fonts")
for c, files in sorted(classes.items()):
    files.sort()
    print(c + "\t" + files[len(files) // 2][1])
' | while IFS=$'\t' read -r class path; do
  out="$dir/$class.mkv"
  if [ -s "$out" ]; then echo "$class: kept $out"; continue; fi
  echo "$class: $path"
  # Matroska streams from a pipe; -t 60 stops ffmpeg after a minute, but
  # docker exec does not hand the closed pipe back to the container, so the
  # read is bounded too: a minute of a 2160p remux is about 600 MB.
  docker exec "$node" head -c "${CLIP_MAX_BYTES:-750000000}" "$mount${path#/data}" 2>/dev/null |
    ffmpeg -hide_banner -loglevel error -nostdin -y -i pipe:0 -map 0 -c copy -t 60 "$out" || true
  [ -s "$out" ] || { echo "$class: no clip" >&2; rm -f "$out"; }
done
ls -la "$dir"
