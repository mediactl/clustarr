# Clustarr

Clustarr is a Kubernetes-native, event-driven media stack in a single Go module. It manages a
media collection end to end — finding releases on indexers, downloading them over usenet and
BitTorrent, importing and naming the files, transcoding them to HEVC 10-bit with AAC audio, and
fetching subtitles — with custom resources as the interface and a NATS JetStream bus underneath.

There is no web UI, by design. `kubectl get movies,downloads,transcodejobs,subtitlerequests -A`
is the UI. Everything a user configures is a custom resource; everything a user waits on is a
custom resource with a status and conditions.

It takes its domain logic from the projects that already got it right: release parsing, quality
decisions and naming from Radarr/Sonarr/Lidarr/Readarr, indexer definitions from Prowlarr,
subtitle scoring from Bazarr, and an opinionated quality model that is a strict subset of the
TRaSH Guides.

## Status: pre-alpha, incomplete

Read this before evaluating anything else. **No controller reconciles anything yet.** What exists
today is:

- **API types** — 28 custom resource kinds across five API groups, with generated deepcopy and
  server-side-apply configurations, CEL validation, and CRDs that install into a real apiserver
  (verified in envtest).
- **The event bus** — `pkg/events`: the JetStream abstraction, its stream/consumer topology, a
  NATS implementation, an in-memory implementation, and a shared contract suite both pass.
- **Deployment manifests** — kustomize bases, a Helm umbrella chart, NATS manifests, container
  images and a kind script.

There is no catalog reconciliation, no indexer querying, no downloading, no importing, no
transcoding and no subtitle fetching. Installing Clustarr today gets you CRDs and a NATS server.
The ordered plan — M0 scaffold through M6 Prowlarr parity — is in
[§16 of the design spec](docs/superpowers/specs/2026-09-18-clustarr-design.md); M0 is the part
that is landing now.

## Services

Seven services, one `clustarr` binary, invoked as `clustarr <service> --role <role>`:

| Service | Responsibility |
|---|---|
| **catalogarr** | Inventory (movies, series, episodes, music, books, comics, audiobooks), metadata, import lists, quality/delay decisions and the file importer. |
| **importarr** | Everything entering the library: root-folder rescan, import lists and completed-download import. |
| **indexarr** | Aggregates Torznab/Newznab and Cardigann indexers into one search API, with a local release index and RSS sync. |
| **grabarr** | Download clients: the embedded anacrolix BitTorrent engine and an embedded usenet pipeline, plus download lifecycle and blocklisting. |
| **squasharr** | Transcoding to HEVC 10-bit + AAC on CPU (libx265) or GPU (hevc_nvenc) workers, with verification and replacement. |
| **captionarr** | Subtitle search, scoring, fetching and post-processing across providers — Bazarr's behaviour, distributed. |
| **ui** | Server-rendered web UI (templ + htmx + SSE) over the same custom resources `kubectl` sees; never writes status and owns no CRD. |

## Architecture

Custom resources are the interface and the source of truth for desired state. A `Movie` is an
object; so are `Indexer`, `QualityProfile`, `RootFolder`, `Download`, `TranscodeJob` and
`SubtitleRequest`. The default coupling between components is a watch: a controller reacts to
objects changing, and each object's status has exactly one controller allowed to write it, which
makes ownership provable rather than conventional
([ADR-0004](docs/adr/0004-one-cr-per-human-visible-unit.md)). The message bus carries what does
not belong in etcd — short, high-churn tasks, rate-limited outbound work like indexer searches and
metadata refreshes, delayed work such as grab delay windows, and durable domain events other
services replay ([ADR-0001](docs/adr/0001-nats-jetstream-for-events-and-work-queues.md)).
Transcodes are neither: they run for hours and want a GPU, so each one is a `batch/v1` Job
created suspended and unsuspended against a slot budget, which buys real scheduling, `kubectl
logs` and cancellation for free ([ADR-0005](docs/adr/0005-transcodes-as-batch-jobs.md)). Media
itself never travels over the bus — all services share one RWX volume at `/data`, so imports can
hardlink and moves stay atomic ([ADR-0006](docs/adr/0006-single-rwx-data-volume.md)).

## Quick start (kind)

Requires Go 1.27, Docker, `kind` and `kubectl`.

```sh
make kind-up    # create a kind cluster, mount ./.data at /data, install NATS with JetStream
make install    # server-side apply the 28 CRDs
make deploy     # apply the namespace, PVC, RBAC, NATS and manager manifests
```

Then:

```sh
kubectl api-resources --api-group=catalog.clustarr.io
kubectl get crds | grep clustarr.io
```

`make kind-down` tears the cluster back down. Note that `make deploy` currently gets you the
deployment shape, not a working system — see the status section above.

Other useful targets: `make generate manifests` (regenerate deepcopy, apply configurations and
CRDs), `make test` (unit plus envtest), `make lint`, `make build`, `make docker-build`.
`make help` lists everything.

## Repo layout

```
api/                 API types, one directory per group, all v1alpha1
  common/            shared Go types, no CRDs
  catalog/ index/ download/ transcode/ subtitle/
  applyconfiguration/  generated server-side-apply configurations
cmd/clustarr/        the single cobra binary
pkg/
  events/            JetStream bus: topology, natsbus, membus, contract suite, typed payloads
  crdcheck/          envtest CRD install verification
  version/           build metadata
config/              kustomize: crd, default, manager, rbac, nats, keda, prometheus, samples
charts/clustarr/     Helm umbrella chart (NATS, optional NACK and KEDA)
images/              Dockerfile.controller, Dockerfile.media, Dockerfile.media-cuda
hack/                kind.sh, licence boilerplate
docs/
  superpowers/specs/ the design spec
  adr/               architecture decision records
  research/          the research notes behind the spec
```

## Documentation

- **[Design spec](docs/superpowers/specs/2026-09-18-clustarr-design.md)** — authoritative. API
  shapes, controller contracts, subject grammar, milestones, deferred work and risks.
- **Architecture decision records:**
  - [ADR-0001 — NATS JetStream for events and work queues](docs/adr/0001-nats-jetstream-for-events-and-work-queues.md)
  - [ADR-0002 — GPL-3.0 licence](docs/adr/0002-gpl-3-0-licence.md)
  - [ADR-0003 — Release index on SQLite FTS5](docs/adr/0003-release-index-sqlite-fts5.md)
  - [ADR-0004 — One custom resource per human-visible unit](docs/adr/0004-one-cr-per-human-visible-unit.md)
  - [ADR-0005 — Transcodes as batch/v1 Jobs](docs/adr/0005-transcodes-as-batch-jobs.md)
  - [ADR-0006 — One RWX volume at /data](docs/adr/0006-single-rwx-data-volume.md)
  - [ADR-0007 — Single-replica metadata gateway](docs/adr/0007-single-replica-metadata-gateway.md)
  - [ADR-0008 — Delay-profile grab semantics](docs/adr/0008-delay-profile-grab-semantics.md)
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
