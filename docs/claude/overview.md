# Clustarr record: Overview: what Clustarr is, what to read, services and commands (full text)

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

A distributed, event-driven media stack for Kubernetes: controllers and CRDs that
manage a media collection end to end, from wanting something through finding,
downloading, importing, transcoding and subtitling it.

Custom resources are the interface. `kubectl get movies,series,mediafiles` is
the UI -- an item's grabs are its `status.downloads[]` (`--field-selector
status.downloadPhase!=` is the download queue), a file's transcode and subtitles
its MediaFile's status -- and the web UI is a view over the same resources.

## Read before designing anything

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` — the design of record.
  Field-level CRD definitions, event subjects, data flows, milestones.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` — adds
  `import`, observability and the UI. **Where the two disagree, the amendment
  wins.**
- The unify branch's designs (2026-10-06/07), each with an "as built" record of
  where the code moved: `2026-10-06-manager-agent-split-design.md` (ADR-0018,
  ADR-0017), `2026-10-06-mediafile-remediation-loop-design.md` (ADR-0016),
  `2026-10-07-agents-report-over-nats-design.md` (ADR-0019) and
  `2026-10-07-unbranded-names-design.md` (Wave U; its §6 maps every old name to
  its domain name). The design of record and the amendment carry dated "as built
  on the unify branch" notes where these made them false.
- `docs/adr/` — why NATS, why GPL-3.0, why worker pools for transcode, why one
  manager, and so on. `docs/adr/README.md` is the index and the supersede
  lifecycle.
- `docs/research/*.md` — nine verified research notes (TRaSH quality model,
  Prowlarr/Cardigann, queue comparison, torrent/usenet, controller-runtime,
  ffmpeg, Bazarr, metadata providers, naming). Long: `grep -n '^#'` first, then
  read the section. They contain verified API shapes — prefer them over guessing.

The spec is authoritative for names, fields and behaviour. Do not invent fields;
do not drop them.

## Services

One Go module, five binaries (ADR-0018,
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`):

| Binary | Image | Runs |
| --- | --- | --- |
| `cmd/manager` (`internal/cli/manager`) | `clustarr` (static, distroless) | the control plane (ADR-0019): every reconciler and leader-only runnable of the six services, one lease (`manager.clustarr.io`) and one cache, registered by `app/<svc>/manager`; the MediaFile remediation loop with its file and item keys (`app/remediation`, ADR-0016) and transcode admission; task admission and routing (`app/dispatch`) and the intake of the agents' candidates, scan observations and advisories (`app/intake`); ensures the NATS topology; serves `external.metrics.k8s.io` and owns the agents' HPAs (`app/autoscale`); the object-store maintenance loop (`app/maintenance`) |
| `cmd/agent --domain <d>` (`internal/cli/agent`) | `native` (cgo) | one domain per agent process, never leader-elected, registered by `app/<svc>/agent`, read-only to Kubernetes (`k8s.ReadOnly`): `catalog` (search answers, overlay renders), `events` (RSS matcher), `metadata` (gateway, TheIntroDB markers, artwork fetch), `import` (scan observations, inspect and execute imports, list snapshots, recycle sweep, probe tasks, the NFO writes of ADR-0022), `index` (release index and its blocklist, search/download/query/blocklist RPC, RSS poll, Torznab facade), `caption` (subtitle fetch, embedded extraction), `torrent-engine` and `usenet-engine` (rendered by the manager's DownloadClient controller: engine commands in, transfer and engine records out), and the optional `trailers` (ADR-0023: fetches a trailer for the items Plex has none for, with yt-dlp, in the `trailers` image -- native plus that one program -- and records it in `clustarr-trailers`) |
| `cmd/ui` (`internal/cli/ui`) | `clustarr` | the web UI and the Plex provider |
| `cmd/markers` | `native` | segment detection (`app/segments/worker`) on ffgo and ONNX Runtime |
| `cmd/transcode` | `native` | transcode pool and graft Jobs (`app/transcode/worker`) on ffgo |

The `native` image UPX-packs its Go binaries; the `clustarr` image packs
neither of its two (owner, 2026-11-04 for the manager, 2026-11-16 for the
ui: a packed ELF is decompressed into anonymous memory at exec, so the whole
file was resident and unevictable in the pod, where the unpacked one is
demand-paged and file-backed); `native` is FROM scratch with FFmpeg 9's
shared libraries, the ffgo shim and ONNX Runtime, and neither ships `ffmpeg`,
`ffprobe` or `par2` (ADR-0017). `make run-dev` runs the manager, one agent per
domain and the ui as local processes; there is no `clustarr all`.

| Dir | Owns |
| --- | --- |
| `app/catalog/` | Media domain (movies, series, music, books, comics, audiobooks), metadata gateway, release decisions, artwork render (agent `catalog`) |
| `app/import/` | Everything entering the library: root-folder rescan, import lists, completed-download import |
| `app/indexer/` | Indexer aggregation, Cardigann engine, local release index (SQLite FTS5) |
| `app/grab/` | Download clients: torrent (anacrolix) and usenet engines |
| `app/transcode/` | Transcode to HEVC (the MP4 standard) on worker-pool and graft Jobs; admission (`app/transcode/admission`) |
| `app/caption/` | Subtitles, Bazarr-equivalent, distributed |
| `app/remediation/` | The MediaFile remediation loop (ADR-0016): file and item keys, the planners' adapters, records incorporation, the item stages (metadata, artwork, search, grab, downloads) |
| `app/dispatch/`, `app/intake/` | Task admission and routing; the intake of grab candidates, scan observations and task advisories (ADR-0019) |
| `app/autoscale/`, `app/maintenance/` | The agents' HPAs and the External Metrics API (ADR-0018); the object-store maintenance loop (ADR-0020) |
| `app/segments/` | markers' segment analysis |
| `app/livetv/` | Live TV: the IPTVProvider and IPTVChannel controllers and the HDHomeRun tuner, in the manager |
| `ui/` | Server-rendered web UI (templ + htmx + SSE); serves artwork at `/art` and the Plex Metadata Provider roots over a read-only bus; never writes status |

API groups: `{catalog,index,download,transcode,subtitle}.clustarr.io/v1alpha1`,
and `clustarr.io/v1alpha1` for Live TV.
Module `github.com/mediactl/clustarr`. Licence GPL-3.0 (lets us port *arr and
Bazarr logic verbatim — keep the header on every file).

**Names.** The domain names -- `catalog`, `import`, `index`, `grab`,
`transcode`, `caption`, `markers` -- are the only internal names: Go
identifiers, field managers, streams, subjects, durables, RPC subjects,
labels, Secrets and leases. The per-service *arr brands were swapped out
cold turkey on 2026-10-07 (Wave U), with no migration of stored names and
no code that reads an old one; `make brand-gate` (`hack/brand-gate.sh`;
`TestNoBrandNameOutsideTheRecords`†) fails on any brand outside the dated records
(`docs/superpowers`, `docs/research`, the ADR bodies, the cold-cutover runbook and
the chart README's "Upgrading from 0.4.x"). `clustarr` itself is the product
name and stays.

## Commands

```bash
make generate      # deepcopy + apply configurations (flattened to api/applyconfiguration/<group>/<version>)
make manifests     # CRDs + one RBAC ClusterRole per identity (RBAC_ROLES) into config/
make build         # bin/manager, bin/ui, bin/agent (cgo), bin/markers, bin/transcode (never bare `go build`)
make native-assets # FFmpeg 9 libs + libffshim, ONNX Runtime, and the test-only par2 CLI (no ffmpeg or ffprobe program)
make chart-deps    # vendor the chart's nats and cloudnative-pg tarballs into charts/clustarr/charts
make test          # unit + envtest + native suites; sets KUBEBUILDER_ASSETS, CLUSTARR_PG_ASSETS, LD_LIBRARY_PATH, FFGO_SHIM_DIR and CLUSTARR_REQUIRE_TOOLS=1
make lint          # golangci-lint v2
make brand-gate    # no *arr brand outside the dated records
make docker-build docker-build-native docker-selfcheck  # the clustarr and native images, and their checks (hack/image-checks.sh)
make run-dev       # manager, ui and one agent per domain against your kubeconfig and a NATS at $NATS_URL
make cardigann-bundle  # re-pack .data/Definitions into app/indexer/bundle/embedded/definitions.zip
make kind-up       # local cluster with NATS, then: make install deploy (make kind-load for the images)
make e2e           # end-to-end suite against that kind cluster (test/e2e, tag e2e)
```

Tools live in `$(go env GOPATH)/bin`: `controller-gen` v0.22.0, `setup-envtest`,
`kustomize`, `golangci-lint-v2`; `make native-assets` also needs curl, xz, unzip
and a C compiler (it compiles `libffshim.so` against the pinned FFmpeg). envtest
assets are Kubernetes 1.37.0. `go.mod` replaces the ffgo module path with the
fork's published tag (`github.com/mediactl/ffgo v0.0.0-clustarr.N`, since
2026-10-27; before that a `../ffgo-unify` directory, which `make contexts`
exported as a build context), so `go mod download` fetches it in the images
too; bump the tag in go.mod and go.sum to take a new fork release. The
same shape carries the ONNX Runtime binding
(`github.com/mediactl/onnxruntime-purego v0.0.0-clustarr.N`, branch
`clustarr`: upstream plus `NewEnvWithLogger` and `Env.SetLogLevel`, ORT's
custom logger, offered upstream as shota3506/onnxruntime-purego#18;
`pkg/ortlog` routes ORT's lines under the `onnx` subsystem through it).
anacrolix/torrent is upstream's again since 2026-11-26, at a commit of its
master and not a release (`v1.63.1-0.20261126050437-7ed5d797828d`; owner,
2026-11-26: "Pin upstream master by pseudo-version", over a fork rebased on
v1.63.0). The fork it replaces (`github.com/mediactl/torrent
v1.62.0-clustarr.1`) was v1.62.0 plus upstream's then open PR 1081, which
resets a dropped torrent's announce state at its re-add --
anacrolix/torrent#1048, under which a published or re-grabbed torrent never
got the tracker's answer; until 2026-11-10 `announce.go` tagged a re-add's
tracker URLs with a fragment instead. Upstream merged it on 2026-11-24, after
v1.63.0, and the same day fixed a panic it introduced (`f601c604`, an input
update queued for a dropped torrent), which the fork never had. The pin pulls
anacrolix/generics and multiless at unreleased commits too. **Move to the
first release after v1.63.0** and nothing else changes. The fork's tag must
stay fetchable: every commit before this one names it in go.mod.
**A tree-wide `go get -u` does not build here** (2026-11-26; owner chose the
ceilings: "Take Exactly what 0.37.3 declares and the latest templ release with
a rebuild"). Three things it takes too far: `k8s.io/kube-openapi` (after
2026-08-21 it uses structured-merge-diff v7, and every `k8s.io/*` v0.37 module
and controller-runtime v0.25 use v6: apimachinery stops compiling), so
kube-openapi, `k8s.io/utils` and `sigs.k8s.io/json` -- unversioned modules
Kubernetes pins by commit -- stay at the commits `k8s.io/apimachinery`'s own
go.mod declares, and move only with a Kubernetes minor (0.38 also needs the
apply configurations regenerated and a controller-runtime built for it);
`github.com/anacrolix/stm` (v0.6.0 dropped the method dht v2.24.0 calls: hold
v0.5.0 until a dht release without stm, which its master already is); and
`github.com/a-h/templ`, whose runtime and generator move together
(`ui.md`, "Format a template").
controller-runtime is upstream's again since 2026-11-26 (`gotchas.md`, "The
manager talked to the apiserver in CBOR"). **A third-party gap is never patched in this tree** (owner,
2026-11-10): take the upstream release that has the fix, else a
`mediactl/` fork rebased on the upstream tag with the fix as its own
commit and the PR opened upstream, and drop the replace when upstream
ships it.

**ONNX Runtime's library comes from Microsoft's NuGet package** (2026-11-26;
owner: "this should be investigated and fixed", then "swap the downloads to
use https://github.com/syntax-syndicate/onnxruntime for now").
`github.com/microsoft/onnxruntime` and its release files have answered 404
since about 2026-11-17 (Microsoft, to The Register: "We have made some
repositories private while we investigate malicious content"), and every
`native`, `native-debug` and `trailers` image build failed at the release
tarball's download once the layer cache had lapsed; the release workflow
then publishes nothing, the `clustarr` image included. The fork was no
download: a fork carries the tags, not the release files, and
`github.com/onnxruntime` holds execution providers and demos only.
`images/Dockerfile.native`'s `ort` stage and `hack/native-assets.sh` now
fetch `microsoft.ml.onnxruntime.<version>.nupkg` from api.nuget.org and
take `runtimes/linux-{x64,arm64}/native/libonnxruntime.so` and the
package's licence files. It is the same build: the x64 library is byte for
byte the one from the release tarball, and both libraries, stripped as the
staging stage strips them, are byte for byte those in
`native:sha-d08a9f9`. The pins are the package's SHA-256
(`ORT_NUPKG_SHA256`) and each library's own (`ORT_SHA256_AMD64`,
`ORT_SHA256_ARM64`, which were the tarballs' sums before). Nothing is known
about what the malicious content was; a version bump waits for Microsoft's
account of it.

