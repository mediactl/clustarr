# clustarr

Kubernetes-native, event-driven media automation: catalog, indexers,
downloads, transcoding and subtitles as CRDs and controllers. See
[`docs/superpowers/specs/2026-09-18-clustarr-design.md`](../../docs/superpowers/specs/2026-09-18-clustarr-design.md)
(and its amendment) for the design of record; this file documents the chart
only.

This is the umbrella chart for a production install. `config/default`
(plain kustomize) is the smaller, dependency-free alternative used by
`hack/e2e.sh` and local kind clusters; prefer this chart for anything real --
it swaps `config/default`'s single-node dev NATS StatefulSet for the
upstream `nats` chart (clustered R3, config reloader,
`prometheus-nats-exporter`), and it is the one `values.schema.json` and
`clustarr.validate` protect.

## Requirements

| | |
| --- | --- |
| Kubernetes | >= 1.31 (CEL validation rules in the CRDs, `podFailurePolicy` on transcode Jobs) |
| Helm | 3.x (this chart ships `values.schema.json`, validated by every Helm 3 release) |
| Storage | One `ReadWriteMany`-capable `StorageClass` (CephFS preferred; NFS or Longhorn RWX acceptable) for `/data`, or a claim you already run. See [Storage](#storage). |

The chart declares three conditional dependencies (`nats`, `nack`, `keda`);
`helm dependency build charts/clustarr` must run before `lint`/`template`/
`install`, even with all three left at their defaults, because Helm checks
that every declared dependency is present under `charts/` regardless of
whether its `condition` is true.

## Installing

```sh
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm repo add kedacore https://kedacore.github.io/charts
helm dependency build charts/clustarr

# CRDs are not managed by Helm (see Upgrading below) -- apply them first.
kubectl apply --server-side -f config/crd/bases/

helm install clustarr charts/clustarr --namespace clustarr-system --create-namespace
```

Read the `NOTES.txt` Helm prints after install (or `helm get notes
clustarr`): it lists every service and replica count for the values you
actually used, the Torznab facade API key command, and a storage warning
when `storage.data.storageClass` is unset.

## Upgrading

Helm never manages CRDs (its own documented limitation, not a Clustarr
choice): re-apply `config/crd/bases/` before every `helm upgrade`, or a
controller can start rejecting objects against a schema the cluster has not
seen yet.

## Storage

One `ReadWriteMany` PersistentVolumeClaim, `<release>-data`, is mounted at
`/data` in every media-touching pod (catalogarr, importarr-worker, grabarr
and its download engines, squasharr and its transcode Jobs, captionarr and
captionarr-worker). Import is a hardlink or an atomic rename inside this one
filesystem, which is why it must be exactly one volume shared by all of
them, never one per service, and why `clustarr.validate` in
`templates/_helpers.tpl` fails the render outright if
`storage.data.accessMode` is anything but `ReadWriteMany` while the chart is
provisioning its own claim. CephFS is preferred; NFS or Longhorn RWX are
acceptable. exFAT and SMB are not: neither can represent the hardlinks or
atomic renames the importer, the usenet publisher and the transcode worker
all depend on.

`storage.data.size` defaults to `100Gi` -- large enough to hold a starter
library plus in-flight downloads and transcodes without immediately needing
a resize, small enough not to over-claim a homelab's storage pool by
default. Resize it for your own library before installing; growing a bound
RWX PVC later depends entirely on whether your CSI driver supports online
expansion.

`storage.data.storageClass` is left unset by default, which is standard
Helm chart practice and matches `config/default/pvc.yaml`'s own,
deliberately identical choice (see that file's comment): an unset
`storageClassName` binds against whatever `StorageClass` your cluster marks
default. **This chart does not, and by design cannot, fail the render when
that is left unset**, even though an unset default is the single most
common way this install goes wrong: most clusters' *default* `StorageClass`
provisions `ReadWriteOnce` block storage (EBS, GCE PD, Azure Disk, local
`StorageClass`es), which cannot satisfy a `ReadWriteMany` claim, and the
PVC simply sits `Pending` forever with no indication from Helm at all. A
hard `fail` here was considered and rejected: `helm lint`/`helm template`
with the chart's own shipped values (no `--set` at all) is exactly the case
CI protects (`.github/workflows/release.yml`'s `chart-lint` job) and the
case `cmd/clustarr`'s installer-parity tests render against
(`TestChartAndKustomizeAgreePerComponent`,
`TestGrabarrEnginesMountAClaimTheInstallerCreates`, and friends) -- making
the *shipped default* fail would break exactly the safety net meant to
catch a real regression, and would leave this chart refusing to install
where `config/default` installs fine, an installer disagreement rather than
a fix. So: **always set `storage.data.storageClass` to your RWX class, or
`storage.data.existingClaim` to a claim you already created, before
installing for real** -- `values.schema.json` still rejects `storage.data.size`
and `storage.data.accessMode` being empty or malformed, and Helm's own
post-install `NOTES.txt` output repeats this warning when neither is set,
but nothing will stop the install from starting. After install, always
check the PVC actually reached `Bound`:

```sh
kubectl -n clustarr-system get pvc clustarr-data
```

indexarr's release index (`storage.index`, `5Gi` default, `ReadWriteOnce`)
is a second, separate PVC: it is why `indexarr.replicas` is pinned to `1`
(`clustarr.validate` fails the render on any other value) -- a second replica
could neither bind an RWO volume nor safely share the SQLite database.

## GOMEMLIMIT

Every Clustarr service the chart renders gets `GOMEMLIMIT` set to 80% of its
own `resources.limits.memory`, computed once at render time
(`clustarr.gomemlimit` in `templates/_helpers.tpl`) as a literal byte count
and baked into the container's env. This exists because the Kubernetes
Downward API's `resourceFieldRef` can only ever report *100%* of a
container's own limit -- there is no way to ask it for a fraction -- so the
only place left to compute 80% of a value the chart already knows is the
chart itself, at render time, from the identical `resources.limits.memory`
quantity `values.yaml` sets. Change a service's memory limit and its
`GOMEMLIMIT` moves with it on the next `helm template`/`helm upgrade`; the
plain kustomize install (`config/manager/*.yaml`) mirrors the same 80%
figures as hand-computed literals, since kustomize has no equivalent
templating step.

This does **not** cover the torrent/usenet engine StatefulSets and
Deployments `grabarr`'s controller creates per `DownloadClient` at runtime --
those get their resources from `DownloadClient.spec.resources`, a CRD field
this chart never renders and has no visibility into. The identical 80%
calculation needs to be made again in
`grabarr/controller/downloadclient` (Go, not a template) from whatever
memory limit that `DownloadClient` actually carries; see the gap-fixes
X12a report for the exact citation.

## Autoscaling (KEDA)

`keda.enabled=false` is the default, supported mode: fixed replicas
everywhere, transcodes gated by squasharr's own slot budget instead of a
scaler. Setting `keda.enabled=true` installs the upstream KEDA operator (as
a chart dependency) **and** the Clustarr `ScaledObject`s in
`templates/scaledobject.yaml`; `keda.prometheusAddress` must then point at a
Prometheus that scrapes `prometheus-nats-exporter`, because the native
`nats-jetstream` scaler cannot yet add `num_pending` and `num_ack_pending`
together (kedacore/keda#8166 is still open) -- the triggers use the
`prometheus` scaler against that exporter instead.

The bundled KEDA dependency is pinned to **2.20.2**, the latest stable
release as of 2026-09-23
([github.com/kedacore/keda releases](https://github.com/kedacore/keda/releases),
published 2026-07-31). KEDA's own published Kubernetes compatibility matrix
([keda.sh/docs/2.20/operate/cluster](https://keda.sh/docs/2.20/operate/cluster/),
"N-2" testing policy) tests 2.20 against Kubernetes v1.33-v1.35. Kubernetes
1.37 shipped 2026-08-26, after 2.20.2 -- so as of this writing **no released
KEDA version has been tested against 1.37**, and 2.20.2 is simply the
newest one that exists. client-go-based operators are generally
forward-compatible within a few minor versions of API server skew, and
there is no newer KEDA release to pick instead; revisit this pin once one
lists 1.36 or 1.37 in its compatibility table.

## Cardigann definitions

indexarr applies Cardigann indexer definitions as `IndexerDefinition`s at
startup, so an `Indexer`'s `spec.definition` can name any of them. By default
they come from the corpus compiled into the binary (Prowlarr's definitions,
packed by `hack/pack-cardigann`); nothing needs mounting.

To pin, trim or update that set without a new image, mount a directory of
definition files -- what `hack/sync-cardigann` writes -- from an existing
ConfigMap or PVC:

```sh
kubectl -n clustarr-system create configmap cardigann-defs --from-file=defs/
helm upgrade --install clustarr charts/clustarr -n clustarr-system \
  --set indexarr.cardigann.definitions.configMap=cardigann-defs
```

The chart mounts it read-only at `/etc/clustarr/cardigann` and sets
`CLUSTARR_CARDIGANN_DEFINITIONS_DIR`. The directory **replaces** the embedded
corpus rather than adding to it, and only files directly in it (`*.yml`,
`*.yaml`) are read. A ConfigMap holds at most 1 MiB, so for the whole corpus
use `existingClaim`. To add or override a single definition on top of the
corpus, create an `IndexerDefinition` instead of mounting anything.
`indexarr.cardigann.bundled: false` loads no corpus at all when nothing is
mounted. indexarr reads the directory once at startup, so restart it after
changing the files.

**Trackers with a login captcha.** Clustarr never solves a captcha (45 bundled
definitions declare one). When the tracker's login page shows it, the
`Indexer` reports `Authenticated=False` with reason `CaptchaRequired`, and the
condition's message names the captcha. The workaround is a browser session:
sign in to the tracker in a browser, copy the Cookie header of a request it
makes, and store it under the `cookie` key of the Secret the Indexer's
`spec.secretRef` names:

```sh
kubectl -n <namespace> patch secret <indexer-secret> --type merge \
  -p '{"stringData":{"cookie":"uid=...; pass=..."}}'
```

indexarr then uses that cookie as the session whenever the captcha appears,
and checks it with the definition's `login.test` on every renewal. When the
tracker expires it, the condition turns to `CredentialsRejected`; sign in
again and replace the cookie.

## Values

The full reference is `values.yaml` itself -- every value has a comment
explaining what it does and why its default is what it is, organised into
the same sections as this table. `values.schema.json` enforces the types,
enums and formats below (and a handful of cross-field invariants also
enforced at render time by `clustarr.validate` in
`templates/_helpers.tpl`, noted where they overlap) so a bad value fails
immediately with a message naming the exact key, rather than deep inside a
template.

| Key | Description | Default |
| --- | --- | --- |
| `nameOverride`, `fullnameOverride` | Override the chart/release-derived name. | `""` |
| `image.registry` | Registry prefix for all three images. | `ghcr.io` |
| `image.pullPolicy` | `Always` \| `IfNotPresent` \| `Never`. | `IfNotPresent` |
| `image.controller.repository`/`.tag` | Distroless image (indexarr, ui). Empty tag falls back to `.Chart.AppVersion`. | `mediactl/clustarr`, `""` |
| `image.media.repository`/`.tag` | debian-slim + ffmpeg/libx265/ffprobe/par2 (everything touching files). | `mediactl/clustarr/media`, `""` |
| `image.mediaCuda.repository`/`.tag` | `media` on an nvidia/cuda base, for NVENC transcode Jobs. | `mediactl/clustarr/media-cuda`, `""` |
| `imagePullSecrets` | `[{name: ...}, ...]`. | `[]` |
| `natsUrl` | External NATS URL; leave empty to use the bundled `nats` subchart. | `""` |
| `natsSingleNode` | Force single-node (R1) JetStream topology; `null` derives it from the bundled subchart. | `null` |
| `nats.enabled` | Install the bundled `nats` subchart. | `true` |
| `nats.config.cluster.replicas` | NATS cluster size; R3 in production, drop to 1 on kind. | `3` |
| `nats.config.jetstream.fileStore.pvc.size` | JetStream file store PVC size. | `20Gi` |
| `nack.enabled` | Install the NATS Kubernetes controller (not required; `pkg/events` provisions its own topology). | `false` |
| `keda.enabled` | Install KEDA and the Clustarr `ScaledObject`s. See [Autoscaling](#autoscaling-keda). | `false` |
| `keda.prometheusAddress` | Prometheus queried for JetStream consumer lag; **required** when `keda.enabled=true`. | `http://prometheus-operated.monitoring.svc:9090` |
| `keda.captionarrWorker.minReplicas`/`.maxReplicas`/`.threshold` | Scaling bounds for the subtitle fetch consumer. | `1`, `8`, `20` |
| `metrics.serviceMonitor.enabled` | Create `ServiceMonitor`s (needs the Prometheus Operator CRDs). | `false` |
| `storage.data.existingClaim` | Bring your own `/data` claim instead of provisioning one. | `""` |
| `storage.data.storageClass` | `StorageClass` for the chart-provisioned `/data` claim. **Read [Storage](#storage) before installing for real.** | `""` |
| `storage.data.accessMode` | Must be `ReadWriteMany` when `existingClaim` is unset (`clustarr.validate` fails otherwise). | `ReadWriteMany` |
| `storage.data.size` | `/data` PVC size. | `100Gi` |
| `storage.index.existingClaim`/`.storageClass`/`.size` | indexarr's `ReadWriteOnce` SQLite index PVC. | `""`, `""`, `5Gi` |
| `podSecurityContext` | Pod-level `securityContext` for every PVC-mounting workload (`fsGroup` chowns a fresh PVC root to uid 1000). | see `values.yaml` |
| `securityContext` | Container-level `securityContext` (non-root, read-only rootfs, all capabilities dropped). | see `values.yaml` |
| `<service>.enabled` | Render this service's Deployment at all. | `true` |
| `<service>.replicas` | Replica count. `catalogarrMetadata.replicas` and `indexarr.replicas` are pinned to `1` by both the schema and `clustarr.validate`. | see `values.yaml` |
| `<service>.resources` | Standard `requests`/`limits`; also the input to [GOMEMLIMIT](#gomemlimit). | see `values.yaml` |
| `<service>.nodeSelector`/`.tolerations`/`.affinity` | Standard Kubernetes scheduling knobs. | `{}`, `[]`, `{}` |
| `indexarr.facade.service.type`/`.port` | The Torznab facade Service (`/{indexer}/api`, `/{indexer}/download`, `/search/api`). | `ClusterIP`, `8080` |
| `indexarr.facade.apiKeySecret` | Secret holding the facade's API keys; empty means `<release>-indexarr-facade`, created by indexarr itself with one random key. | `""` |
| `indexarr.cardigann.bundled` | Load the Cardigann corpus compiled into the binary (`--cardigann-bundled`). `false` renders `CLUSTARR_CARDIGANN_BUNDLED=false`; `true` renders nothing. Moot when `indexarr.cardigann.definitions` mounts a directory. See [Cardigann definitions](#cardigann-definitions). | `true` |
| `indexarr.cardigann.definitions.configMap`/`.existingClaim` | An existing ConfigMap or PVC of definition files, mounted read-only and passed as `--cardigann-definitions-dir`, **replacing** the embedded corpus. Off while both are empty; `clustarr.validate` refuses both. | `""`, `""` |
| `indexarr.cardigann.definitions.subPath` | Directory inside that ConfigMap or claim holding the files; empty is its root. | `""` |
| `squasharr.slots` | `--slots` budget string, e.g. `cpu=2,nvidia=1,intel=1`. | `cpu=2,nvidia=1,intel=1` |
| `squasharr.intelRenderGroups` | GIDs of the host group owning `/dev/dri/renderD*` on the Intel GPU nodes (`--intel-render-groups`), added to every Intel transcode Job's pod as `supplementalGroups`. Host-specific, so no default; leave empty with a runtime that has `device_ownership_from_security_context`. | `[]` |
| `captionarrWorker.ackWaitSeconds` | Also `terminationGracePeriodSeconds`, so a worker can drain its in-flight fetch on `SIGTERM` instead of losing it to redelivery. | `120` |
| `ui.service.type`/`.port` | The web UI's Service. Ships with no login of its own -- put it behind ingress authentication. | `ClusterIP`, `8080` |

`imagePullSecrets`, `nodeSelector`, `tolerations` and `affinity` are plain
Kubernetes structures and intentionally not constrained further by the
schema beyond their shape.
