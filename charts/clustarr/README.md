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
| Kubernetes | >= 1.31 (CEL validation rules in the CRDs, `podFailurePolicy` on transcode Jobs); 1.37 for scale-to-zero (see [Autoscaling](#autoscaling)) |
| Helm | 3.x or 4.x (this chart ships `values.schema.json`, validated by every release) |
| Storage | One `ReadWriteMany`-capable `StorageClass` (CephFS preferred; NFS or Longhorn RWX acceptable) for `/data`, or a claim you already run. See [Storage](#storage). |

The chart declares two conditional dependencies (`nats`, `cloudnative-pg`);
`helm dependency build charts/clustarr` must run before
`lint`/`template`/`install`, even with both left at their defaults, because
Helm checks that every declared dependency is present under `charts/`
regardless of whether its `condition` is true.

## Installing

```sh
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm repo add cloudnative-pg https://cloudnative-pg.github.io/charts
helm dependency build charts/clustarr

# CRDs are not managed by Helm (see Upgrading below) -- apply them first.
kubectl apply --server-side -f config/crd/bases/

helm install clustarr charts/clustarr --namespace clustarr-system --create-namespace
```

Read the `NOTES.txt` Helm prints after install (or `helm get notes
clustarr`): it lists every workload and its replica count for the values you
actually used, the Torznab facade API key command, and a storage warning
when `storage.data.storageClass` is unset.

## Upgrading

Helm never manages CRDs (its own documented limitation, not a Clustarr
choice): re-apply `config/crd/bases/` before every `helm upgrade`, or a
controller can start rejecting objects against a schema the cluster has not
seen yet.

## Upgrading from 0.4.x

Chart 0.5.0 replaces the per-service Deployments with the manager, one agent
per domain, markers and the ui (spec 2026-10-06), and its schema rejects every
retired key with a message naming its replacement. There is no in-place
upgrade: the move is a cold cutover, done once, by
[`docs/runbooks/unify-cold-cutover.md`](../../docs/runbooks/unify-cold-cutover.md).
Write the 0.5.0 values file from `helm get values <release> -o yaml` with the
keys below renamed and the retired ones deleted, and pass it with `-f`, never
`--reuse-values`.

| 0.4.x key | 0.5.0 key |
| --- | --- |
| `image.controller` | `image.clustarr` |
| `image.transcoder` | `image.native` |
| `image.media` | gone: everything that used it runs `image.native` |
| `image.transcoderCuda` | gone: there is no CUDA image ([ADR 0015](../../docs/adr/0015-no-cuda-image.md)) |
| `catalogarr`, `importarr`, `grabarr`, `squasharr`, `captionarr` | `manager` (one block; size its `resources` for the merged controllers) |
| `squasharr.slots`, `.intelRenderGroups`, `.gpuNodeLabelNvidia`, `.gpuNodeLabelIntel` | `manager.*` |
| `squasharr.workerEngine` | gone (no template read it) |
| `catalogarrMetadata` | `agents.metadata` (no `replicas`: always one) |
| `importarrWorker` | `agents.import` |
| `indexarr.replicas` | gone: the index agent always runs one |
| `indexarr.facade`, `indexarr.resources`, scheduling | `agents.index.*` |
| `indexarr.facade.apiKeySecret`'s default, `<release>-indexarr-facade` | `<release>-index-facade`, created by the manager |
| `indexarr.cardigann` | `manager.cardigann` |
| `captionarrWorker` (incl. `ackWaitSeconds`) | `agents.caption` (the grace comes from the code) |
| `segmentarrWorker` (incl. `concurrency`) | `markers` (`concurrency: N` becomes `consumerSlots: {markers-analyze: N}`) |
| `keda` | `autoscaling.enabled` |
| the engine ServiceAccount `<release>-grabarr-engine` | `<release>-grab-engine` |

## Storage

One `ReadWriteMany` PersistentVolumeClaim, `<release>-data`, is mounted at
`/data` in every media-touching pod (the manager, the import and caption
agents, markers, the download engines and the transcode pools). Import is a
hardlink or an atomic rename inside this one filesystem, which is why it must
be exactly one volume shared by all of them, never one per workload, and
why `clustarr.validate` in
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
case `test/guards`' installer-parity tests render against
(`TestChartAndKustomizeAgreePerComponent`, and
`TestManagerStampsAClaimTheInstallerCreates` with its siblings once the test
batch writes them) -- making
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

The index agent's release index (`storage.index`, `5Gi` default,
`ReadWriteOnce`) is a second, separate PVC, and one reason the index agent
always runs one replica with strategy `Recreate`: a second replica could
neither bind an RWO volume nor safely share the SQLite database. Like
`/data`, it carries `helm.sh/resource-policy: keep`, so `helm uninstall`
leaves it, and every release the RSS feeds collected, behind. See
[Postgres release index](#postgres-release-index) for the alternative.

## Postgres release index

`postgres.enabled=false` is the default: the index agent keeps its local SQLite
FTS5 database on the `storage.index` PVC above (ADR-0003). Set
`postgres.enabled=true` to run the release index on a
[CloudNativePG](https://cloudnative-pg.io/) `Cluster` instead (ADR-0010):
`agent-index` gets `CLUSTARR_INDEX_DSN` from a Secret rather than a volume and
the `storage.index` PVC is not rendered. It still runs exactly one replica
with strategy `Recreate`: its search RPC, RSS polls, Torznab facade and
per-host rate limiter are one process (spec 2026-10-06, R4 amendment; ADR-0018's
refinement of ADR-0010), so a second replica would pace every tracker twice.

This chart renders the `Cluster` itself
(`templates/postgres-cluster.yaml`, named `<release>-postgres`) as a normal
resource whenever `postgres.enabled`, with `postgres.cluster.instances`
Postgres replicas (CNPG's own streaming replication) and a
`postgres.cluster.storage.size` volume. It
bootstraps a `clustarr` database owned by a `clustarr` role and creates
`<release>-postgres-app` holding that role's DSN under key `uri` -- what
the index agent reads by default; `postgres.existingSecret` points it at a DSN
Secret you already have instead. Like `pvc.yaml`'s `/data` claim, it carries
`helm.sh/resource-policy: keep`, so neither `helm uninstall` nor any upgrade
ever deletes your database -- remove it yourself when you actually want it
gone.

**CloudNativePG must already be `Ready` before `postgres.enabled=true` is
applied.** Its admission webhook fails closed until its own Deployment is
up, so a `Cluster` submitted while the operator isn't running yet is
rejected outright. `--wait` does **not** solve this on its own: it only
waits on the resources *this release* creates, so `helm install --set
cloudnative-pg.enabled=true --set postgres.enabled=true --wait` still
submits the `Cluster` in the very same pass as the operator's own
Deployment, before `--wait` has anything to wait *for* yet -- the render
below fails immediately rather than let you find that out from a rejected
apply. There are two supported paths:

- **This chart also installs the operator** (`cloudnative-pg.enabled=true`):
  install it first, alone, then add the Postgres index in a second step.
  `clustarr.validate` refuses the render outright if you set both
  `cloudnative-pg.enabled=true` and `postgres.enabled=true` on a first
  install, with this same two-step instruction in the error message.

  ```sh
  helm install clustarr charts/clustarr --namespace clustarr-system \
    --create-namespace --set cloudnative-pg.enabled=true \
    --set postgres.enabled=false --wait

  helm upgrade clustarr charts/clustarr --namespace clustarr-system \
    --set cloudnative-pg.enabled=true --set postgres.enabled=true
  ```

- **CloudNativePG already runs elsewhere in the cluster** (the common case;
  see [Requirements](#requirements)): leave `cloudnative-pg.enabled=false`
  and wait on the existing operator's own Deployment yourself before
  installing with `postgres.enabled=true`:

  ```sh
  kubectl wait --for=condition=Available deployment/cnpg-controller-manager \
    -n cnpg-system --timeout=120s

  helm install clustarr charts/clustarr --namespace clustarr-system \
    --create-namespace --set postgres.enabled=true
  ```

## GOMEMLIMIT

Every workload the chart renders gets `GOMEMLIMIT` set to a share of its own
`resources.limits.memory`, computed at render time (`clustarr.gomemlimit` in
`templates/_helpers.tpl`) as a literal byte count: 80%, except `agent-import`
and `agent-caption` (60%) and `markers` (50%), whose FFmpeg, par2 and ONNX
Runtime allocations live outside the Go heap. The Downward API's
`resourceFieldRef` can only report 100% of a container's own limit, so the
share is computed here, from the same quantity `values.yaml` sets. Change a
memory limit and its `GOMEMLIMIT` moves with it on the next `helm upgrade`;
`config/manager` carries the same figures as literals, and
`TestGOMEMLIMITIsItsShareOfTheLimit` (written by the test batch that follows the
unify branch's implementation waves) is to hold the two installers together.

This does **not** cover the torrent/usenet engine StatefulSets and
Deployments the manager's DownloadClient controller creates per
`DownloadClient` at runtime -- those get their resources from
`DownloadClient.spec.resources`, a CRD field this chart never renders. The
same 80% calculation is made again in `app/grab/controller/downloadclient`
(Go, not a template) from whatever memory limit that `DownloadClient`
carries.

## Autoscaling

`autoscaling.enabled=true` (the default) lets the manager scale the
queue-driven workloads itself (spec 2026-10-06 §9). It owns one
HorizontalPodAutoscaler per Deployment labelled `autoscale.clustarr.io/domain`
(`agent-catalog`, `agent-events`, `agent-import`, `agent-caption`, `markers`)
and scales each on JetStream consumer lag: to zero while its queues are empty
(`agent-events` to one, its stream cannot buffer through zero) and up to the
number of nodes its pods can schedule on. The lag comes from an External
Metrics API the manager serves itself: `templates/autoscaling.yaml` renders the
`v1beta1.external.metrics.k8s.io` APIService, its `<release>-external-metrics`
Service and a `kube-system` RoleBinding that lets the manager read the
front-proxy client CA. The manager creates its own CA and serving certificate
in `<release>-external-metrics-tls` and injects the APIService's `caBundle`.

The chart never renders an HPA, and the autoscaled Deployments carry no
`replicas`: the manager's reconciler owns both, and Helm's server-side apply
would fight it. On a cluster without the `HPAScaleToZero` feature (on by
default from Kubernetes 1.37) the manager keeps one replica of each and records
a `ScaleToZeroUnavailable` Event.

Only one APIService may hold `v1beta1.external.metrics.k8s.io`. Where KEDA or
prometheus-adapter already does, set `autoscaling.enabled=false`: the chart
then renders `agents.<domain>.replicas` and `markers.replicas`, no APIService,
and passes `--autoscale=false` to the manager. Helm refuses to adopt an
APIService it does not own, so the conflict is a loud install error.

`agents.<domain>.consumerSlots` and `markers.consumerSlots` set a consumer's
per-pod handler count (`CLUSTARR_CONSUMER_SLOTS`), which is also the HPA's
per-pod target, for example `--set markers.consumerSlots.markers-analyze=3`.
If the manager is down, `kubectl delete ns` of its namespace can hang on the
unavailable APIService; `kubectl delete apiservice v1beta1.external.metrics.k8s.io`
releases it. The metric, the HPA's shape, who owns which object and the failure
modes are [`docs/autoscaling.md`](../../docs/autoscaling.md) (ADR-0018).

## Ratings providers

The metadata gateway's `enrichRatings` (in `agent-metadata`) fills `status.metadata.ratings` on a Movie or Series
(and Series' `status.metadata.firstAired`) from every enabled `MetadataProvider` that declares
ratings sources, in ascending `spec.priority` order: a higher-priority provider's value for a
source is never overwritten by a lower-priority one, and a provider that fails or is disabled
leaves whatever a previous refresh already had (spec §C.2).

| `spec.type` | Sources | `secretRef` key | Status |
| --- | --- | --- | --- |
| `tmdb` | `tmdb` (movies only -- reuses the same fetch that fills `status.metadata` itself; this client has no TMDB series lookup, so it never rates a Series) | `apiKey` | implemented |
| `mdblist` | `imdb`, `tmdb`, `rottenTomatoesCritic`, `rottenTomatoesAudience`, `metacritic`, `trakt`, `letterboxd`, for movies (by TMDB id) and series (by TMDB, else TVDB id) | `apiKey`, optional `apiKeySecondary` | implemented. The quota is per key per day (1000 on the free plan); a second key is spent once the first is out, and a key that is out rests until its `X-RateLimit-Reset`. The probe (`GET /user`) spends no quota |
| `omdb` | `imdb`, `rottenTomatoesCritic`, `metacritic` | `apiKey` | **not yet implemented** -- the CRD accepts the type, but ruling R5 (`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md` §C.3) blocks writing the client until a response shape is recorded (`docs/research/ratings-providers.md`); a CR of this type reports `Ready=False` with a message naming the block |

`spec.baseURL` overrides apply the same way they do for every other provider (an e2e stub can
point at itself); `spec.rateLimit` overrides the per-host limiter the controller holds, as for
every provider -- clustarr never defaults a limiter on inside a client library.

## Cardigann definitions

The manager's bundle loader applies Cardigann indexer definitions as
`IndexerDefinition`s at startup, so an `Indexer`'s `spec.definition` can name any of them. By default
they come from the corpus compiled into the binary (Prowlarr's definitions,
packed by `hack/pack-cardigann`); nothing needs mounting.

To pin, trim or update that set without a new image, mount a directory of
definition files -- what `hack/sync-cardigann` writes -- from an existing
ConfigMap or PVC:

```sh
kubectl -n clustarr-system create configmap cardigann-defs --from-file=defs/
helm upgrade --install clustarr charts/clustarr -n clustarr-system \
  --set manager.cardigann.definitions.configMap=cardigann-defs
```

The chart mounts it read-only into the manager at `/etc/clustarr/cardigann` and sets
`CLUSTARR_CARDIGANN_DEFINITIONS_DIR`. The directory **replaces** the embedded
corpus rather than adding to it, and only files directly in it (`*.yml`,
`*.yaml`) are read. A ConfigMap holds at most 1 MiB, so for the whole corpus
use `existingClaim`. To add or override a single definition on top of the
corpus, create an `IndexerDefinition` instead of mounting anything.
`manager.cardigann.bundled: false` loads no corpus at all when nothing is
mounted. The manager reads the directory once at startup, so restart it after
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

The manager and the index agent then use that cookie as the session whenever the captcha appears,
and checks it with the definition's `login.test` on every renewal. When the
tracker expires it, the condition turns to `CredentialsRejected`; sign in
again and replace the cookie.

## Plex provider

`ui` can serve Plex's Custom Metadata Provider protocol (PMS ≥ 1.43.0,
movie and TV libraries only, `docs/research/plex-metadata-provider.md`) read
over the same catalog `ui` already renders the Library page from -- Movie,
Series and Episode `status.metadata`, `status.artwork` and their ratings.

**Adding it in Plex:** *Settings → Metadata Agents → Add Provider*, and
enter each root's URL separately -- one provider per parent type
(Plex's own recommendation, so it can be combined with other providers):

- `http://<host>/plex/movies` for movies
- `http://<host>/plex/tv` for TV shows, seasons and episodes

`<host>` is wherever `ui.plex.externalURL` points (below); PMS must be able
to reach it, unauthenticated, over plain HTTP.

**It is unauthenticated by the Plex protocol itself.** There is no token,
header or query parameter for auth -- Plex states plainly that "only
unauthenticated requests are currently supported". Anyone who can reach
`ui.plex.externalURL` can read the *entire* catalog: every title, every
poster and backdrop URL, every rating. **Never put this Service, or a path
that routes to it, behind a public ingress.** It belongs on the cluster's
private network only -- the same `ClusterIP` Service the rest of `ui`
already uses, reachable by PMS from inside that network (a Tailscale/VPN
overlay, a private ingress class, or PMS itself running in-cluster), never
through the public ingress `ui.auth.mode` already warns you to keep the
rest of the UI off.

**Photo URLs are signed with a kept Secret.** Cast photos, season posters
and episode stills are provider-hosted, so the provider hands Plex signed
`/art/search` URLs, and Plex stores them. The chart creates
`<release>-ui-art-signing-key` once (64 random characters, carried over on
upgrade and kept on uninstall, `helm.sh/resource-policy: keep`) and gives it
to `ui` as `CLUSTARR_ART_SIGNING_KEY`, so a stored URL survives a restart
and every replica verifies it. Deleting the Secret rotates the key: every
photo URL Plex holds then answers `403` until Plex refreshes the item.

**`ui.plex.enabled`** (default `true`) maps to `--plex-provider`. `false`
unmounts `/plex` entirely -- a plain `404`, not the `503` below -- and
silences the startup warning an empty `externalURL` would otherwise log.

**`ui.plex.externalURL`** (default `""`, env `CLUSTARR_EXTERNAL_URL`) maps
to `--external-url`: the absolute base every `thumb`, `art` and `Image[]`
URL this provider hands Plex is built on (`GET /art/{kind}/{uid}/{type}`,
ADR-0011). It is empty by default because no shipped value could ever guess
an operator's own reachable hostname -- with it empty, both roots answer
`503` with a body naming the flag, logged once at startup, and readiness is
unaffected. Set it to wherever PMS can reach this Service, for example the
in-cluster DNS name if PMS runs as a pod too:

```sh
helm upgrade --install clustarr charts/clustarr -n clustarr-system \
  --set ui.plex.externalURL=http://clustarr-ui.clustarr-system.svc.cluster.local:8080
```

## Plex (cluster-plex)

`cluster-plex.enabled=true` installs Plex beside clustarr, in the release's
namespace, from cluster-plex's chart (`oci://ghcr.io/mediactl/charts/cluster-plex`,
a dependency pinned in `Chart.yaml`; its own `values.yaml` documents every key
under `cluster-plex:`). Every Plex pod runs against one library in PostgreSQL:
`templates/plex-db.yaml` renders the CloudNativePG Cluster `<release>-plex-db`
(Postgres 15, `plexDatabase`), kept on uninstall like the release index's.
CloudNativePG must be Ready first, exactly as for `postgres.enabled` above.

The defaults wire it to this release: clustarr's ui as the movie and TV metadata
providers, the two libraries on them, the library read-only from
`storage.data`'s claim at `/media` (clustarr's `/data/media`), and
cluster-plex following clustarr's catalog in this namespace with the plex.tv
account in Secret `plex-token`. Values that name this release's resources are
templates assuming the default fullname; set `cluster-plex.storage.media.existingClaim`
to `storage.data.existingClaim` when you bring your own claim (the default is
`<release>-data`). Pin `cluster-plex.plex.machineIdentifier` to keep a server
identity clients already know, and set `cluster-plex.image.tag` and
`cluster-plex.remux.image.tag` to the images you run.

Live TV (cluster-plex 0.4.0) is on with `livetv.enabled`: `cluster-plex.plex.liveTV.enabled`
(default true) has the Plex lease holder add this release's IPTVProviders to
Plex as DVRs, one per block, and every Plex pod serve each block's port on a
link-local address to the `<release>-livetv` Service. Set both false together.

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
| `image.registry` | Registry prefix for both images. | `ghcr.io` |
| `image.pullPolicy` | `Always` \| `IfNotPresent` \| `Never`. | `IfNotPresent` |
| `image.clustarr.repository`/`.tag` | The static image: `manager` and `ui`. An empty tag falls back to `.Chart.AppVersion`. | `mediactl/clustarr`, `""` |
| `image.native.repository`/`.tag` | The FROM-scratch image: `agent`, `markers` and `transcode` on FFmpeg 9 and ONNX Runtime. Every agent, markers, the download engines and the transcode pools (NVIDIA pools included: the NVIDIA container runtime injects the driver, [ADR 0015](../../docs/adr/0015-no-cuda-image.md)) run it. | `mediactl/clustarr/native`, `""` |
| `imagePullSecrets` | `[{name: ...}, ...]`. | `[]` |
| `natsUrl` | External NATS URL; leave empty to use the bundled `nats` subchart. | `""` |
| `natsSingleNode` | Force single-node (R1) JetStream topology; `null` derives it from the bundled subchart. Only the manager ensures topology. | `null` |
| `nats.enabled` | Install the bundled `nats` subchart. | `true` |
| `nats.config.cluster.replicas` | NATS cluster size; R3 in production, drop to 1 on kind. | `3` |
| `nats.config.jetstream.fileStore.pvc.size` | JetStream file store PVC size. | `20Gi` |
| `postgres.enabled` | Run the index agent's release index on a CloudNativePG `Cluster` instead of local SQLite. See [Postgres release index](#postgres-release-index). | `false` |
| `postgres.cluster.instances` | CNPG `Cluster.spec.instances` (Postgres streaming replicas). | `1` |
| `postgres.cluster.storage.size`/`.storageClass` | The `Cluster`'s own PVC. | `5Gi`, `""` |
| `postgres.existingSecret` | An existing Secret (key `uri`) to use instead of the one CNPG's `bootstrap.initdb` creates. Setting it also stops the chart rendering its own `Cluster`. | `""` |
| `cloudnative-pg.enabled` | Install the CloudNativePG operator as a chart dependency. Most clusters install it once, cluster-wide, instead -- leave this `false` and set only `postgres.enabled=true` in that case. | `false` |
| `autoscaling.enabled` | The manager scales the queue-driven workloads through its own External Metrics API. See [Autoscaling](#autoscaling). | `true` |
| `metrics.serviceMonitor.enabled` | Create `ServiceMonitor`s (needs the Prometheus Operator CRDs). | `false` |
| `metrics.prometheusRule.enabled` | Create a `PrometheusRule` alerting on any NATS slow-consumer drop (`ClustarrNATSSlowConsumer`, ADR-0020 §3; needs the Prometheus Operator CRDs). | `false` |
| `storage.data.existingClaim` | Bring your own `/data` claim instead of provisioning one. | `""` |
| `storage.data.storageClass` | `StorageClass` for the chart-provisioned `/data` claim. **Read [Storage](#storage) before installing for real.** | `""` |
| `storage.data.accessMode` | Must be `ReadWriteMany` when `existingClaim` is unset (`clustarr.validate` fails otherwise). | `ReadWriteMany` |
| `storage.data.size` | `/data` PVC size. | `100Gi` |
| `storage.index.existingClaim`/`.storageClass`/`.size` | The index agent's `ReadWriteOnce` SQLite index PVC, kept on uninstall. Not rendered when `postgres.enabled`. | `""`, `""`, `5Gi` |
| `podSecurityContext` | Pod-level `securityContext` for every PVC-mounting workload (`fsGroup` chowns a fresh PVC root to uid 1000). | see `values.yaml` |
| `securityContext` | Container-level `securityContext` (non-root, read-only rootfs, all capabilities dropped). | see `values.yaml` |
| `manager.slots` | Transcode admission per hardware class (`--slots`), e.g. `cpu=2,nvidia=1,intel=1`. | `cpu=2,nvidia=1,intel=1` |
| `manager.intelRenderGroups` | GIDs of the host group owning `/dev/dri/renderD*` on the Intel GPU nodes (`--intel-render-groups`), added to every Intel transcode pod as `supplementalGroups`. Host-specific, so no default. | `[]` |
| `manager.gpuNodeLabelNvidia`/`.gpuNodeLabelIntel` | Override the node labels that mark GPU nodes. Empty uses `nvidia.com/gpu.present` and `intel.feature.node.kubernetes.io/gpu`. | `""` |
| `manager.cardigann.bundled` | Load the Cardigann corpus compiled into the binary (`--cardigann-bundled`). See [Cardigann definitions](#cardigann-definitions). | `true` |
| `manager.cardigann.definitions.configMap`/`.existingClaim`/`.subPath` | A ConfigMap or PVC of definition files, mounted read-only into the manager as `--cardigann-definitions-dir`, **replacing** the corpus; one, not both. | `""` |
| `manager.graftConcurrency` | Audio grafts running at once in the namespace (`--graft-concurrency`): the admission ledger's graft slots, each a CPU pod of a cpu pool's size. | `2` |
| `manager.remediation.concurrency` | MediaFiles and items the remediation loop reconciles at once (`--remediation-concurrency`, 1-64). | `16` |
| `manager.remediation.bulkWritesPerSecond` | MediaFile status applies per second that no grant, answer, intent or first probe caused (`--remediation-bulk-writes-per-second`); `0` disables the pacing. | `25` |
| `manager.remediation.ioWorkers` | Goroutines for the loop's `/data` calls (`--remediation-io-workers`, 1-64). | `8` |
| `manager.resources`/`.nodeSelector`/`.tolerations`/`.affinity` | The manager's resources (also the input to [GOMEMLIMIT](#gomemlimit)) and scheduling; the default memory includes 128Mi for the Live TV tuner's stream buffers. It always runs one replica. | see `values.yaml` |
| `livetv.enabled` | Live TV: the IPTVProvider and IPTVChannel controllers and the HDHomeRun tuner in the manager, the `<release>-livetv` Service (port 80 to the tuner), and the NetworkPolicy below. `false` renders `--livetv-bind-address=0` and none of them. | `true` |
| `livetv.port` | The tuner's container port (`--livetv-bind-address=:<port>`). | `5004` |
| `livetv.portRange` | The ports the blocks get (`--livetv-port-range`, `low-high`). cluster-plex serves each on a link-local address inside its Plex pods, never on a node. | `47000-47999` |
| `livetv.networkPolicy.enabled` | A NetworkPolicy on the manager pod admitting the tuner port only from `allowFrom`; every other manager port stays open. `false` for a CNI that enforces none. | `true` |
| `livetv.networkPolicy.allowFrom` | NetworkPolicyPeers allowed to reach the tuner port. Narrow the default with a `namespaceSelector` naming Plex's namespace. | Plex's pods (`app.kubernetes.io/component: plex`) in any namespace |
| `agents.<domain>.enabled` | Render that agent (`catalog`, `events`, `metadata`, `import`, `index`, `caption`). | `true` |
| `agents.<domain>.replicas` | Replicas with `autoscaling.enabled=false` (`catalog`, `events`, `import`, `caption`). `metadata` and `index` always run one. | `1` |
| `agents.<domain>.consumerSlots` | Per-pod handler slots by consumer (`CLUSTARR_CONSUMER_SLOTS`), also the HPA's per-pod target; only that domain's consumers. | `{}` |
| `agents.<domain>.resources`/`.nodeSelector`/`.tolerations`/`.affinity` | Standard resources and scheduling. | see `values.yaml` |
| `agents.index.facade.service.type`/`.port` | The Torznab facade Service (`/{indexer}/api`, `/{indexer}/download`, `/search/api`). | `ClusterIP`, `8080` |
| `agents.index.facade.apiKeySecret` | Secret holding the facade's API keys, named to the manager and the index agent alike; empty means `<release>-index-facade`, created by the manager with one random key. | `""` |
| `markers.enabled`/`.replicas`/`.consumerSlots`/`.resources` | Skip-segment detection; `consumerSlots.markers-analyze` is its per-pod analyses. | `true`, `1`, `{}` |
| `ui.auth.mode` | The web UI's authentication mode (`--auth-mode`), chosen explicitly: the binary refuses to serve without one. `anonymous`, the only mode, serves every request without a login -- put the Service behind ingress authentication and never expose it directly. | `anonymous` |
| `ui.plex.enabled` | Mount the Plex Custom Metadata Provider routes (`--plex-provider`). See [Plex provider](#plex-provider). | `true` |
| `ui.plex.externalURL` | Absolute base every `thumb`/`art`/`Image[]` URL is built on (`--external-url`). Empty means the provider roots answer `503` naming the flag. **Unauthenticated by protocol -- never a public ingress hostname.** | `""` |
| `ui.service.type`/`.port` | The web UI's Service. | `ClusterIP`, `8080` |

`imagePullSecrets`, `nodeSelector`, `tolerations` and `affinity` are plain
Kubernetes structures and intentionally not constrained further by the
schema beyond their shape.
