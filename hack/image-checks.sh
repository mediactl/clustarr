#!/usr/bin/env bash
# hack/image-checks.sh IMAGE CHECK... -- the one definition of the image
# checks (spec 2026-10-06 §10.1.4). The Makefile's docker-selfcheck, ci.yml
# and release.yml all call it; none restates a check.
#
#   version:<bin>                 /usr/bin/<bin> --version, read-only, no capabilities, uid 1000
#   self-check:transcode:<class>  transcode --self-check=<class>, as a pool pod runs it
#   self-check:agent              agent --self-check
#   self-check:markers            markers --self-check
#   self-check:par2-child         agent par2-repair --self-check
#   no-shell                      the image has no /bin/sh
#   notices                       (debug image) every licence notice exists and is non-empty
#   no-media-executables          (debug image) no ffmpeg, ffprobe or par2 file anywhere
#   ffmpeg-libraries              (debug image) exactly the seven FFmpeg libraries ffgo loads
set -euo pipefail
usage="usage: hack/image-checks.sh IMAGE CHECK..."
img=${1:?$usage}
shift
[[ $# -gt 0 ]] || { echo "${usage}" >&2; exit 2; }
# The par2 integration (§11.1 step 12) adds par2cmdline-turbo's and par2go's notices.
notices=(
  /usr/share/licenses/ffmpeg/LICENSE.txt
  /usr/share/licenses/ffmpeg/SOURCE
  /usr/share/licenses/clustarr/LICENSE
  /usr/share/licenses/ffgo/LICENSE
  /usr/share/licenses/ffgo/SOURCE
  /usr/share/licenses/onnxruntime/LICENSE
  /usr/share/doc/libc6/copyright
)
locked() { docker run --rm --read-only --cap-drop=ALL --user 1000:1000 "$@"; }
for check in "$@"; do
  echo "== ${img}: ${check}" >&2
  case "${check}" in
    version:*)
      locked --entrypoint "/usr/bin/${check#version:}" "${img}" --version ;;
    self-check:transcode:*)
      locked --entrypoint /usr/bin/transcode "${img}" --self-check="${check#self-check:transcode:}" >/dev/null ;;
    self-check:agent|self-check:markers)
      locked --entrypoint "/usr/bin/${check#self-check:}" "${img}" --self-check ;;
    self-check:par2-child)
      locked --entrypoint /usr/bin/agent "${img}" par2-repair --self-check ;;
    no-shell)
      if docker run --rm --entrypoint /bin/sh "${img}" -c true >/dev/null 2>&1; then
        echo "${img} has a shell" >&2; exit 1
      fi ;;
    notices)
      docker run --rm --entrypoint /bin/sh "${img}" -c \
        'set -e; for f in "$@"; do test -s "$f" || { echo "missing notice $f" >&2; exit 1; }; done; cat /usr/share/licenses/*/SOURCE' \
        sh "${notices[@]}" ;;
    no-media-executables)
      found=$(docker run --rm --entrypoint /bin/sh "${img}" -c 'find / -xdev \( -name ffmpeg -o -name ffprobe -o -name par2 \) -type f')
      [[ -z "${found}" ]] || { echo "${img} carries a media executable: ${found}" >&2; exit 1; } ;;
    ffmpeg-libraries)
      got=$(docker run --rm --entrypoint /bin/sh "${img}" -c \
        'for f in /usr/lib/libav* /usr/lib/libsw* /usr/lib/libpostproc*; do [ -e "$f" ] && basename "$f"; done; true' |
        sed -E 's/\.so.*$//' | sort -u | tr '\n' ' ')
      [ "${got}" = "libavcodec libavdevice libavfilter libavformat libavutil libswresample libswscale " ] ||
        { echo "image-checks: ${img} stages FFmpeg libraries ffgo does not load: ${got}" >&2; exit 1; } ;;
    *)
      echo "hack/image-checks.sh: unknown check ${check}" >&2; exit 2 ;;
  esac
done
echo "ok: ${img}" >&2
