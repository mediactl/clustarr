#!/usr/bin/env bash
# hack/native-assets.sh DIR | --print-pins
#
# The native test assets, the analogue of `make pg-assets` (spec 2026-10-06
# §10.1.6). DIR/lib: FFmpeg 9's shared libraries and libffshim, ONNX Runtime
# and libpar2shim. DIR/bin: the test-only ffmpeg, ffprobe and par2
# executables the parity tests compare against and test/par2test.CreateSet
# runs. No image ships those executables. (The ffmpeg and ffprobe CLIs stay
# until the goldens recorder and the last exec implementation are gone,
# upgrade guide U2; the par2 CLI stays for creating test sets.)
#
# Pins: FFmpeg and ONNX Runtime from images/Dockerfile.native's ARG defaults,
# so the tests load what the image ships; libffshim from the ffgo module
# go.mod resolves (the shim/ffshim.c the Go side is built against);
# libpar2shim from par2go's release asset for the go.mod version, checked
# against hack/par2go/SHA256SUMS (upgrade guide U1; skipped, with a note,
# while go.mod does not require par2go); the par2 CLI from PAR2_CLI_VERSION
# below.
#
# Idempotent: DIR/.stamp records every input, and a matching stamp exits at
# once. The work happens under mktemp -d in $TMPDIR (scratch areas are
# shared between sessions), and DIR is swapped in with one rename.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
df="${repo}/images/Dockerfile.native"
PAR2_CLI_VERSION=v1.5.0
PAR2_CLI_SHA256_AMD64=5a9f64386813456693c2ea1fb7649436fe7544bbdf97fd73b3483dfcc8aca464
PAR2_CLI_SHA256_ARM64=3afb2f0b319fc4e6353c6d4261994757cbe3189f968a72420b4ba1d63f9c9a70

arg() {
  local v
  v=$(sed -nE "s/^ARG $1=(.+)$/\1/p" "${df}" | head -n1)
  [[ -n "${v}" ]] || { echo "native-assets: images/Dockerfile.native has no ARG $1 default" >&2; exit 1; }
  printf '%s' "${v}"
}
case "$(uname -m)" in
  x86_64) arch=AMD64 ff=linux64 ort=x64 p2=amd64 ;;
  aarch64) arch=ARM64 ff=linuxarm64 ort=aarch64 p2=arm64 ;;
  *) echo "native-assets: unsupported machine $(uname -m)" >&2; exit 1 ;;
esac
FFMPEG_RELEASE=$(arg FFMPEG_RELEASE)
FFMPEG_VERSION=$(arg FFMPEG_VERSION)
FFMPEG_BRANCH=$(arg FFMPEG_BRANCH)
FFMPEG_SHA256=$(arg "FFMPEG_SHA256_${arch}")
ORT_VERSION=$(arg ORT_VERSION)
ORT_SHA256=$(arg "ORT_SHA256_${arch}")
par2_sha="PAR2_CLI_SHA256_${arch}"
PAR2_CLI_SHA256=${!par2_sha}
# Empty until go.mod requires par2go (plan W0.26).
PAR2GO_VERSION=$(cd "${repo}" && go list -m -f '{{.Version}}' github.com/mediactl/par2go 2>/dev/null || true)
PAR2GO_SUMS="${repo}/hack/par2go/SHA256SUMS"
pins=(FFMPEG_RELEASE FFMPEG_VERSION FFMPEG_BRANCH FFMPEG_SHA256 ORT_VERSION ORT_SHA256 PAR2_CLI_VERSION PAR2_CLI_SHA256 PAR2GO_VERSION)

if [[ "${1:-}" == --print-pins ]]; then
  for k in "${pins[@]}"; do printf '%s=%s\n' "${k}" "${!k}"; done
  exit 0
fi

dir=${1:?usage: hack/native-assets.sh DIR | --print-pins}
mkdir -p "$(dirname "${dir}")"
dir="$(cd "$(dirname "${dir}")" && pwd)/$(basename "${dir}")"
ffgo_dir=$(cd "${repo}" && go list -m -f '{{.Dir}}' github.com/obinnaokechukwu/ffgo)
stamp=$(
  for k in "${pins[@]}"; do printf '%s=%s\n' "${k}" "${!k}"; done
  sha256sum < "${PAR2GO_SUMS}"
  cat "${ffgo_dir}/shim/ffshim.c" "${ffgo_dir}/shim/ffshim.h" | sha256sum
  sha256sum < "${BASH_SOURCE[0]}"
)
stamp=$(printf '%s' "${stamp}" | sha256sum | cut -d' ' -f1)
if [[ -f "${dir}/.stamp" && "$(cat "${dir}/.stamp")" == "${stamp}" ]]; then
  echo "native-assets: ${dir} is current" >&2
  exit 0
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/native-assets.XXXXXX")
trap 'rm -rf "${work}"' EXIT
out="${work}/out"
mkdir -p "${out}/lib" "${out}/bin"

echo "native-assets: FFmpeg ${FFMPEG_VERSION}" >&2
curl -fsSL -o "${work}/ff.tar.xz" \
  "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}/ffmpeg-${FFMPEG_VERSION}-${ff}-gpl-shared-${FFMPEG_BRANCH}.tar.xz"
echo "${FFMPEG_SHA256}  ${work}/ff.tar.xz" | sha256sum -c - >/dev/null
mkdir -p "${work}/ff"
tar -xJf "${work}/ff.tar.xz" --strip-components=1 -C "${work}/ff"
cp -a "${work}"/ff/lib/lib*.so* "${out}/lib/"
install -m 0755 "${work}/ff/bin/ffmpeg" "${work}/ff/bin/ffprobe" "${out}/bin/"

echo "native-assets: libffshim from ${ffgo_dir}/shim" >&2
gcc -shared -fPIC -O2 -Wall -DFFSHIM_HAVE_AVDEVICE=1 -DFFSHIM_HAVE_AVFILTER=1 \
  -I"${work}/ff/include" -o "${out}/lib/libffshim.so" "${ffgo_dir}/shim/ffshim.c" \
  -L"${out}/lib" -lavutil -lavcodec -lavformat -lavdevice -lavfilter -Wl,-rpath,"${dir}/lib"

echo "native-assets: ONNX Runtime ${ORT_VERSION}" >&2
curl -fsSL -o "${work}/ort.tgz" \
  "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-${ort}-${ORT_VERSION}.tgz"
echo "${ORT_SHA256}  ${work}/ort.tgz" | sha256sum -c - >/dev/null
mkdir -p "${work}/ort"
tar -xzf "${work}/ort.tgz" --strip-components=1 -C "${work}/ort"
install -m 0644 "${work}/ort/lib/libonnxruntime.so.${ORT_VERSION}" "${out}/lib/libonnxruntime.so"

echo "native-assets: par2cmdline-turbo ${PAR2_CLI_VERSION} (a test-only CLI)" >&2
curl -fsSL -o "${work}/par2.zip" \
  "https://github.com/animetosho/par2cmdline-turbo/releases/download/${PAR2_CLI_VERSION}/par2cmdline-turbo-${PAR2_CLI_VERSION#v}-linux-${p2}.zip"
echo "${PAR2_CLI_SHA256}  ${work}/par2.zip" | sha256sum -c - >/dev/null
unzip -p "${work}/par2.zip" par2 > "${out}/bin/par2"
chmod 0755 "${out}/bin/par2"

if [[ -n "${PAR2GO_VERSION}" ]]; then
  echo "native-assets: libpar2shim ${PAR2GO_VERSION} (the par2go release asset)" >&2
  curl -fsSL -o "${work}/libpar2shim-linux-${p2}.so" \
    "https://github.com/mediactl/par2go/releases/download/${PAR2GO_VERSION}/libpar2shim-linux-${p2}.so"
  grep " libpar2shim-linux-${p2}.so\$" "${PAR2GO_SUMS}" | (cd "${work}" && sha256sum -c - >/dev/null)
  install -m 0644 "${work}/libpar2shim-linux-${p2}.so" "${out}/lib/libpar2shim.so"
else
  echo "native-assets: go.mod does not require github.com/mediactl/par2go yet; no libpar2shim" >&2
fi

printf '%s\n' "${stamp}" > "${out}/.stamp"
next=$(mktemp -d "${dir}.new.XXXXXX")
cp -a "${out}/." "${next}/"
rm -rf "${dir}.old"
if [[ -e "${dir}" ]]; then mv "${dir}" "${dir}.old"; fi
mv "${next}" "${dir}"
rm -rf "${dir}.old"
echo "native-assets: ${dir}" >&2
