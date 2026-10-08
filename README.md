# Clustarr

Clustarr is a Kubernetes-native, event-driven media stack in a single Go module. It manages a
media collection end to end — finding releases on indexers, downloading them over usenet and
BitTorrent, importing and naming the files, transcoding them to HEVC 10-bit with AAC audio, and
fetching subtitles — with custom resources as the interface and a NATS JetStream bus underneath.

`kubectl get movies,series,mediafiles -A` is the ground truth: everything a user configures is a
custom resource, and everything a user waits on is status on one — an item's grabs in its
`status.downloads[]` (`kubectl get movies --field-selector status.downloadPhase!=` is the download
queue), a file's transcode and subtitles in its MediaFile's status. There is also a
server-rendered web UI (templ + htmx + SSE) as a view over those same resources — it writes no
status and owns no CRD of its own, so anything it does, `kubectl` can do too. Its authentication
mode is chosen explicitly (`--auth-mode`; the chart's `ui.auth.mode`) and it refuses to serve
without one; the only mode, `anonymous`, serves every request without a login, so it must sit
behind whatever ingress authentication the cluster already runs.

It takes its domain logic from the projects that already got it right: release parsing, quality
decisions and naming from Radarr/Sonarr/Lidarr/Readarr, indexer definitions from Prowlarr,
subtitle scoring from Bazarr, and an opinionated quality model that is a strict subset of the
TRaSH Guides.

## Status: pre-alpha

Every service reconciles against envtest, and the unify branch has rebuilt the process topology
around one manager (below). What that does **not** mean yet:

- **Not proven end to end.** Only the first milestone's end-to-end scenarios have ever run on a
  kind cluster; every scenario written since is in `test/e2e`, never executed.
- **Tests are behind the code on this branch.** Its waves were built implementation-first (owner,
  2026-10-07): the tests and guards they name are written in one batch afterwards, and until
  then `go vet ./...` fails on the test packages they broke.
- **Not deployed.** Moving a running 0.4.x install onto this branch is a one-time cold cutover,
  done by hand ([`docs/runbooks/unify-cold-cutover.md`](docs/runbooks/unify-cold-cutover.md)).

The ordered milestones are in
[§16 of the design spec](docs/superpowers/specs/2026-09-18-clustarr-design.md).

## Services

Eight services, five binaries: `cmd/manager` runs every reconciler, `cmd/agent --domain <d>`
runs each domain's workers, `cmd/ui`, `cmd/markers` and `cmd/transcode`
([ADR-0018](docs/adr/0018-manager-agents-and-ui.md)). The manager is the control plane and the
only writer of every custom resource; agents execute the tasks it routes and report back over
NATS, and never write the Kubernetes API
([ADR-0019](docs/adr/0019-agents-never-write-kubernetes.md)).

| Service | Responsibility | Runs in |
|---|---|---|
| **catalog** | Inventory (movies, series, episodes, music, books, comics, audiobooks), the metadata gateway, and release decisions: quality/delay profiles, search, grab and RSS matching. | `manager` + agents `catalog`, `events`, `metadata` |
| **import** | Everything entering the library: root-folder rescan, import lists and completed-download import. | `manager` + agent `import` |
| **index** | Aggregates Torznab/Newznab and Cardigann indexers into one search API, with a local release index, its blocklist and RSS sync. | `manager` + agent `index` |
| **grab** | Download clients: the embedded anacrolix BitTorrent engine and an embedded usenet pipeline; a grab is an entry in its item's status. | `manager` + agents `torrent-engine`, `usenet-engine` |
| **transcode** | Transcoding to HEVC (MP4, AAC plus Dolby surround) in-process on FFmpeg 9 through ffgo, on CPU, NVIDIA or Intel worker pools, with verification and replacement. | `manager` + `transcode` pool and graft Jobs |
| **caption** | Subtitle search, scoring, fetching and post-processing across providers — Bazarr's behaviour, distributed. | `manager` + agent `caption` |
| **markers** | Skip-segment detection (intros, credits) for Plex, in-process on ffgo and ONNX Runtime. | `markers` |
| **ui** | Server-rendered web UI (templ + htmx + SSE) and the Plex Custom Metadata Provider, over the same custom resources `kubectl` sees; never writes status and owns no CRD. | `ui` |

Two images carry them: `ghcr.io/mediactl/clustarr` (distroless, static: `manager`, `ui`) and
`ghcr.io/mediactl/clustarr/native` (FROM scratch: `agent`, `markers`, `transcode` on FFmpeg 9's
shared libraries). No image ships or runs `ffmpeg`, `ffprobe` or `par2`
([ADR-0017](docs/adr/0017-no-external-media-programs.md); usenet repair still runs a `par2`
binary, which the image does not carry, until par2go lands as a library). The queue-driven
agents scale to zero on HorizontalPodAutoscalers the manager drives from its own External
Metrics API ([`docs/autoscaling.md`](docs/autoscaling.md)).

## Architecture

Custom resources are the interface and the source of truth for desired state. A `Movie` is an
object; so are `Indexer`, `QualityProfile`, `RootFolder`, `DownloadClient` and `MediaFile`,
whose status carries each file's probe, transcode, subtitles and graft
([ADR-0016](docs/adr/0016-per-file-work-is-mediafile-status.md)), while an item's status carries
its grabs (ADR-0019). Every lifecycle is a state machine in the manager's reconcile loop, and
each object's status has exactly one writer, which makes ownership provable rather than
conventional. The message bus carries what does not belong in etcd — tasks the manager admits
and routes to an agent domain, rate-limited outbound work like indexer searches and metadata
refreshes, delayed work such as grab delay windows, the release firehose, and the workers'
answers, which land in compare-and-swap records the manager incorporates
([ADR-0001](docs/adr/0001-nats-jetstream-for-events-and-work-queues.md)). Transcodes run for
hours and want a GPU, so they run on long-lived worker-pool Jobs per profile and hardware class,
fed by a JetStream queue under the manager's admission
([ADR-0009](docs/adr/0009-transcode-worker-pools-over-jetstream.md)). Media itself never travels
over the bus — every process that touches files shares one RWX volume at `/data`, so imports can
hardlink and moves stay atomic ([ADR-0006](docs/adr/0006-single-rwx-data-volume.md)).

## Quick start (kind)

Requires Go 1.27, Docker, `kind` and `kubectl`.

```sh
make kind-up    # create a kind cluster, mount ./.data at /data, install NATS with JetStream
make install    # server-side apply the 27 CRDs
make deploy     # the namespace, PVCs, RBAC, NATS, the manager, six agents, markers and the ui
```

Then:

```sh
kubectl api-resources --api-group=catalog.clustarr.io
kubectl get crds | grep clustarr.io
```

`make kind-down` tears the cluster back down. `make docker-build docker-build-native` builds the
two images and `make kind-load` loads them; `make run-dev` instead runs the manager, one agent per
domain and the ui as local processes against the current kube context and a NATS at `$NATS_URL`.

Other useful targets: `make generate manifests` (regenerate deepcopy, apply configurations, CRDs
and RBAC), `make native-assets` (FFmpeg 9's libraries and the ffgo shim the native tests load),
`make test` (unit, envtest and native suites), `make lint`, `make build` (the five binaries into
`bin/`), `make brand-gate`. `make help` lists everything.

## Repo layout

```
api/                  API types, one directory per group, all v1alpha1
  common/             shared Go types, no CRDs
  catalog/ index/ download/ transcode/ subtitle/
  applyconfiguration/ generated server-side-apply configurations, <group>/<version>
cmd/{manager,agent,ui,markers,transcode}/  the five binaries (internal/cli holds their command trees)
app/                  per service: manager/ and agent/ registrations over leaf packages;
                      remediation/ (the MediaFile loop), dispatch/, intake/, autoscale/, maintenance/
pkg/
  events/             JetStream bus: topology, natsbus, membus, contract suite, typed payloads
  records/            the compare-and-swap records workers answer in
  ffruntime/          the one FFmpeg runtime gate
  ...                 release parsing, quality, decisions, naming, metadata and subtitle clients
ui/                   the web UI and the Plex provider
config/               kustomize: crd, default, manager, rbac, autoscaling, nats, prometheus, postgres, e2e, samples
charts/clustarr/      Helm umbrella chart (NATS, optional CloudNativePG)
images/               Dockerfile.clustarr (manager, ui), Dockerfile.native (agent, markers, transcode), Dockerfile.e2e-fixtures
hack/                 kind.sh, dev-run.sh, native-assets.sh, image-checks.sh, brand-gate.sh, licence boilerplate
docs/
  superpowers/specs/  the design spec, its amendment and the designs since
  adr/                architecture decision records
  research/           the research notes behind the spec
  runbooks/           operator procedures
```

## Documentation

- **[Design spec](docs/superpowers/specs/2026-09-18-clustarr-design.md)** — authoritative for API
  shapes, controller contracts, subject grammar, milestones, deferred work and risks; its dated
  "as built on the unify branch" notes say where the code has moved on.
- **[Architecture decision records](docs/adr/README.md)** — the index, from NATS JetStream
  (ADR-0001) and GPL-3.0 (ADR-0002) through one manager and domain agents (ADR-0018) and agents
  that never write Kubernetes (ADR-0019), with each one's status.
- **Operations:** [autoscaling](docs/autoscaling.md), [observability](docs/observability.md),
  [GPU nodes](docs/gpu-nodes.md), the [Helm chart](charts/clustarr/README.md) and the
  [cold cutover runbook](docs/runbooks/unify-cold-cutover.md).
- **[Research notes](docs/research/)** — the comparisons the spec is built on: queue, indexers,
  downloads, metadata, naming, quality, subtitles, transcode, Kubernetes.

## Licence

GPL-3.0-or-later. See [LICENSE](LICENSE).

This is a deliberate choice, not a default. Clustarr ports logic directly from the *arr projects
and Bazarr — release-title regexes, custom-format specifications, decision-engine ordering, naming
grammars, subtitle scoring weights and hearing-impaired detection. All of those are GPL-3.0, and a
port is a derivative work, so GPL-3.0 is the licence that makes the implementation strategy legal.
Reimplementing them under a permissive licence would mean discarding a decade of accumulated edge
cases. TRaSH Guides data is MIT and is marked as such where it is bundled. The reasoning is in
[ADR-0002](docs/adr/0002-gpl-3-0-licence.md).
