# ADR-0018: One manager runs every reconciler; workers run as one agent per domain; agents autoscale without KEDA

**Status:** Accepted, 2026-10-06 (supersedes ADR-0013). Recorded 2026-10-07, after ADR-0019
and Wave U had changed what the agents write and what every name is; the body says where.

## Context

ADR-0013 kept one controller Deployment per service until the system was production ready,
with the unified topology designed (`docs/superpowers/specs/2026-09-24-unified-manager-design.md`)
and deferred, because one pod per component made a crash loop or a log stream attributable
without tooling. On 2026-10-06 the owner chose to adopt the unified topology now, and to go
further: every reconciler in one manager, every worker in one agent binary run per domain, the
segment and transcode workers renamed `markers` and `transcode` on the in-process ffgo
pattern, each binary linking only what it needs, `cmd/clustarr` and `clustarr all` removed,
and an autoscaler that reaches zero without KEDA (`.superpowers/unify/rulings.md`, R1-R14).
The premise ADR-0013 rested on, that per-service isolation was worth more than one cache and
one lease during development, is the owner's to weigh, and the owner weighed it the other way.
The design is `docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`.

## Decision

- **Five binaries, two images.** `cmd/manager` and `cmd/ui` (static, `CGO_ENABLED=0`, in the
  distroless `clustarr` image, `images/Dockerfile.clustarr`); `cmd/agent` (cgo, for the SQLite
  piece completion seeding torrents rely on, R13), `cmd/markers` and `cmd/transcode` (in the
  FROM-scratch `native` image, `images/Dockerfile.native`, with FFmpeg 9's shared libraries,
  the ffgo shim and ONNX Runtime). Every Go binary is UPX-packed and every staged shared
  library stripped (2026-10-07). No image sets an ENTRYPOINT; every container names its
  binary (`pkg/binpath`).
- **The manager** runs every controller and leader-only runnable of the six services under
  one lease (`manager.clustarr.io`) and one cache, registered by `app/<service>/manager`
  (`internal/cli/manager`). It is the only process that ensures the NATS topology, and it
  re-ensures it after a NATS restart (`busconn.KeepTopology`). It serves
  `external.metrics.k8s.io/v1beta1` itself (`app/autoscale/extmetrics`) and owns one HPA per
  autoscaled agent Deployment under the field manager `clustarr-autoscale`
  (`app/autoscale`). ADR-0016 put the MediaFile remediation loop here, and ADR-0019 made the
  manager the control plane: the only writer of every CR and Secret, the router and
  admitter of every task (`app/dispatch`), and the reader of every agent's records, intake
  and advisories (`app/intake`).
- **The agent** runs exactly one domain per process (`catalog`, `events`, `metadata`,
  `import`, `index`, `caption`, `torrent-engine`, `usenet-engine`; `internal/cli/agent`),
  registered by `app/<service>/agent`, never leader-elected; its subscriptions bind durables
  and never create them. Since ADR-0019 an agent is an executor: its Kubernetes client is
  read-only (`k8s.ReadOnly`), its generated role grants get, list and watch only, and it
  reports over NATS. ADR-0019 supersedes the agent rows of the split design's §5.2 and §4.6
  this ADR adopted.
- **Names.** The split itself kept every field manager and every NATS stream, consumer and
  bucket name (R7), adding two: `clustarr-autoscale`, and the pinned default field owner
  `clustarr` for a write that names none. Wave U then renamed every internal *arr name to its
  domain name, cold turkey (`docs/superpowers/specs/2026-10-07-unbranded-names-design.md` §6
  maps the old names to the new).
- **Autoscaling.** One external metric, `clustarr_consumer_lag` (JetStream `CONSUMER.INFO`
  pending plus ack-pending per durable); HPAs from 0 (1 for `events`) to the number of nodes
  the pod template can run on, clamped by each consumer's `MaxAckPending` over its per-pod
  slots. The consumers whose downstream is throttled (search, subtitle fetch) keep their
  consumer-wide cap at one pod's slots, so `catalog` and `caption` scale 0..1. The installers
  render the APIService without a CA; the manager injects only `spec.caBundle`.
- **Guards.** Each binary's dependencies are held by a `go list -deps` guard (split design
  §4.5): the manager and the ui link no worker, client, sqlite, torrent, usenet or FFmpeg code.

## Alternatives considered

- **Keep the per-service Deployments (ADR-0013).** The owner declined it.
- **One binary with roles (`clustarr all`).** Every process would link everything: 94 MB
  static, and no way to keep FFmpeg's dynamic loader out of the distroless controller.
- **A combined-domain agent for development (`--domain all`).** It would bring `clustarr all`
  back under another name and cost field-index unions and probe-name rules; `make run-dev`
  starts one agent per domain instead (OD1).
- **KEDA, or prometheus-adapter.** An operator and its CRDs, or a Prometheus, for one metric
  the manager already holds; the owner asked for neither.
- **`k8s.io/apiserver` for the metrics API.** A large dependency for three routes; the
  hand-rolled server is security-sensitive and is to be tested for 401, 403 and metric
  selection instead.

## Consequences

- One informer cache and one lease instead of six; a manager fault stops every reconciler at
  once, and the External Metrics API with it (HPAs hold their last scale).
- Agents at zero replicas cold-start on the first queued item.
- The index agent stays at one replica with `Recreate` under Postgres too, until indexers
  share a limiter (refines ADR-0010).
- Transcode pool pods pull the larger `native` image (refines ADR-0015, ADR-0009).
- A namespace deleted while the manager is down leaves the APIService behind;
  `docs/autoscaling.md` gives the manual recovery.
- Nothing is migrated in place: the first deploy is the one-time cold cutover,
  `docs/runbooks/unify-cold-cutover.md` (owner, 2026-10-07).

## Revisit triggers

- Indexers gain a shared KV limiter: the index agent and catalog search could scale out.
- The manager's cache outgrows its memory limit on a real library.
- A fault that needs per-service isolation to diagnose.
- `HPAScaleToZero` changes status in a supported Kubernetes release.
