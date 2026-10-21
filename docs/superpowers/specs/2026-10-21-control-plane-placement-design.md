# Control-plane placement: a three-replica core, everything else on workers

Date: 2026-10-21. Status: proposed, not built.

## 1. Goal

The owner wants four components on the three control-plane nodes, three replicas each, one pod per node:

- the Plex database, the `<release>-plex-db` CNPG Cluster;
- the NATS servers;
- the ui;
- the manager.

Every other pod clustarr runs stays on the worker nodes and keeps its current placement:

- the agents;
- markers;
- the download engines;
- the transcode pools and graft Jobs;
- the release-index Postgres;
- nats-box;
- cluster-plex's jobs.

The Plex database is not a cluster-plex resource. clustarr's chart renders it (`templates/plex-db.yaml`), and the cluster-plex subchart only connects to `<release>-plex-db-rw`. So this design lives in clustarr's chart and manager. cluster-plex changes one value: §5.1.

## 2. The cluster today (talos, 2026-10-21)

| Node | Role | Arch | CPU / memory | Memory used | Runs (clustarr-system) |
| --- | --- | --- | --- | --- | --- |
| talos-10-1-40-32 | control plane | arm64 | 3.95 / 14.9 GiB | 41% | nats-box |
| talos-10-1-40-33 | control plane | arm64 | 3.95 / 14.9 GiB | 59% | plex-0, clustarr-plex-db-1, clustarr-postgres-1 |
| talos-10-1-40-34 | control plane | arm64 | 3.95 / 14.9 GiB | 46% | clustarr-nats-0 |
| talos-88-ae-dd-75-3d-a0 (NUC) | worker | amd64 | 15.95 / 93 GiB | 5% | nats-1, torrent engine, transcode pools, manager, agents |
| talos-b8-ae-ed-76-79-61 | worker | amd64 | 3.95 / 15 GiB | 26% | usenet engine (pinned), markers |
| talos-f4-4d-30-6f-a7-93 | worker | amd64 | 3.95 / 15 GiB | 14% | nats-2, markers |

Facts that shape the design:

- **The control-plane nodes are untainted** (`allowSchedulingOnControlPlanes`). Only the `pin-amd64` post-renderer keeps clustarr's Helm-rendered pods off them, by giving each an `arch=amd64` nodeSelector. It skips a pod template that already has a required node affinity. Pods the manager builds at runtime never pass through it:
  - graft Jobs get no placement at all (`app/remediation/graft/job.go:51-56`);
  - cpu-class pool Jobs get none either;
  - an engine gets only its DownloadClient's `spec.nodeSelector`.

  So nothing guarantees today that an amd64-only `native` pod stays off the arm64 control-plane nodes.
- **The control-plane nodes are arm64.** The `native` image is amd64-only in practice. The `clustarr` image (manager and ui) is static Go: `CGO_ENABLED=0`, built on `$BUILDPLATFORM`, UPX-packed. `release.yml` already builds it for arm64. The talos deploys use `make docker-build`, which builds one architecture.
- **local-path and etcd share a filesystem** on each control-plane node: both `/var/mnt/local-path` and `/var/lib/etcd` are on the EPHEMERAL partition (`nvme0n1p4`, 1 TB NVMe).
- **The manager has one lease** (`manager.clustarr.io`), and controller-runtime keeps the caches of non-leaders warm.
  - A non-leader reports Ready (`cmd/manager/start_envtest_test.go:121-131`).
  - Every replica serves the External Metrics API and answers it from NATS.
  - **The Live TV tuner (`:5004`) runs on the leader only.** A non-leader is not listening on that port (`app/livetv/tuner/tuner.go:344-361`).
  - The `livetv` Service selects every manager pod. With three replicas, about two-thirds of Plex's tune requests would be refused.
- **The ui keeps no session state.**
  - Its SSE streams send complete frames.
  - The art signing key is the shared `<release>-ui-art-signing-key` Secret.
  - Rating keys are object UIDs.

  It already has `ui.replicas`.
- **The chart has no placement profiles, PDBs or spread for the manager and ui.**
  - The manager is hard-coded to one replica with maxSurge 1 / maxUnavailable 0. The schema forbids a `replicas` key, and `test/guards/installers_topology_test.go` pins it.
  - `plexDatabase` and `postgres` expose no affinity, although CNPG 1.30's `spec.affinity` supports all of it.
  - NATS takes placement only through `nats.podTemplate.merge` (a subchart, so no parent helpers).

## 3. The pattern: two placement profiles

The chart gains two named profiles and one helper. Each component names the profile it belongs to. Those three pieces are the whole pattern.

```yaml
placement:
  # Off by default: a kind or single-node install keeps today's scheduling.
  # talos turns it on.
  enabled: false
  profiles:
    controlPlane:
      replicas: 3            # one per control-plane node
      onePerNode: true       # required pod anti-affinity on kubernetes.io/hostname
      nodeAffinity:          # ANDed into every required node-affinity term
        - {key: node-role.kubernetes.io/control-plane, operator: Exists}
      tolerations:           # harmless while untainted; keeps working if the nodes are tainted later
        - {key: node-role.kubernetes.io/control-plane, operator: Exists, effect: NoSchedule}
    workers:
      onePerNode: false
      nodeAffinity:
        - {key: node-role.kubernetes.io/control-plane, operator: DoesNotExist}
        - {key: kubernetes.io/arch, operator: In, values: [amd64]}
      tolerations: []

manager:      {placement: controlPlane}
ui:           {placement: controlPlane}
plexDatabase: {placement: controlPlane, instances: 3}
postgres:     {placement: workers}
markers:      {placement: workers}
agents:       {<domain>: {placement: workers}}   # every agent block
```

**`clustarr.placement`** renders one pod's scheduling from the component's values, its profile and its selector labels.

- **Required node affinity.** Each of the component's own required terms gets the profile's expressions appended. With none, the profile's expressions form the single term. Terms OR and expressions AND, so the component's own conditions survive and the profile always holds. markers' `intel…/gpu DoesNotExist` becomes `DoesNotExist AND not control-plane AND amd64`.
- **One per node.** When `onePerNode` is set, a required pod anti-affinity on `kubernetes.io/hostname`, selecting the component's own `clustarr.selectorLabels`.
- **Passed through:** the component's `nodeSelector`, preferred affinities, and tolerations (appended to the profile's).
- **Off switch.** With `placement.enabled: false` the helper emits exactly what `clustarr.workload` emits today, so a default render doesn't change.

**Deployments in a `onePerNode` profile** (the manager and ui) also get the following:

- **`replicas`** from the profile. A component may set it lower, never higher, and `clustarr.validate` fails a render that asks for more replicas than the profile allows.
- **`strategy.rollingUpdate: {maxSurge: 0, maxUnavailable: 1}`.** With exactly three eligible nodes and one pod per node, a surge pod has nowhere to go and the rollout stalls. The manager's current maxSurge 1 is the case that would stall.
- **A PodDisruptionBudget with `maxUnavailable: 1`**, so a Talos upgrade that drains one control-plane node evicts one of each.

## 4. One pod per node, per component

- **Kubernetes Deployments** (manager, ui): required anti-affinity and replicas 3, from §3.
- **The NATS StatefulSet** keeps its hostname spread (maxSkew 1, DoNotSchedule) and gains a required anti-affinity.
  - Spread alone lets two servers share a node while a control-plane node is down.
  - Two servers on one node means one node failure loses quorum. Required anti-affinity leaves the third server Pending instead.
- **The CNPG Clusters:** `spec.affinity.enablePodAntiAffinity: true`, `podAntiAffinityType: required`, `topologyKey: kubernetes.io/hostname`. CNPG creates its own PDBs.

## 5. Per component

### 5.1 Plex database: 1 → 3 instances

`templates/plex-db.yaml` renders `spec.affinity` from the helper: the profile's node affinity, `tolerations`, required anti-affinity on the hostname. It also renders `instances` from the profile. The schema opens `plexDatabase` to `placement`, `synchronous`, `maxSlotWalKeepSize` and `backup` (below).

- **Scaling up is online.** Instance 1 is already on talos-10-1-40-33, a control-plane node, so it satisfies the new affinity and stays primary. CNPG clones instances 2 and 3 onto .32 and .34 with `pg_basebackup` and streams to them. The database is 258 MB. The Plex shim keeps using `<release>-plex-db-rw`, which follows the primary.

**Replication.** These are CNPG 1.30's recommendations, from its documentation through DeepWiki, with the field names checked against the CRD installed on talos:

| Setting | Value | Why |
| --- | --- | --- |
| `postgresql.synchronous.method` | `any` (quorum) | A commit waits for any `number` of the standbys. Here that is either of two. |
| `postgresql.synchronous.number` | `1` | RPO 0 (no committed write lost) while either standby is up. Costs one round trip between control-plane nodes per commit (0.6–1.1 ms measured); reads are unaffected. |
| `postgresql.synchronous.dataDurability` | `preferred` | If both standbys are down, the primary drops them from `synchronous_standby_names` and keeps writing. `required` would freeze Plex's writes whenever two control-plane nodes are out, as in a Talos upgrade that drains them. |
| `postgresql.synchronous.failoverQuorum` | `false` (unset) | With 3 instances and `number: 1`, a quorum-checked failover is refused when the primary and one standby fail together (`R + W` must exceed `N`; `internal/controller/replicas.go`, `evaluateQuorumCheck`). Plex would then stay down until someone acts. Without it, CNPG promotes the most advanced standby, which holds every synchronously committed write unless that standby is unreachable too. |
| `replicationSlots.highAvailability.enabled` | `true` (the default) | Each standby's slot keeps the WAL it still needs, across failovers. |
| `postgresql.parameters.max_slot_wal_keep_size` | `2GB` | CNPG's own recommendation whenever slots are on: the PostgreSQL default is unlimited. On talos this is a safety limit. local-path does not enforce a claim's size, so the "5 GiB" volume is the node's 952 GB EPHEMERAL filesystem, which etcd also uses (§10). A standby left down would otherwise let the primary keep WAL without bound. `wal_keep_size` stays at CNPG's 512MB. |
| `primaryUpdateStrategy` / `primaryUpdateMethod` | `unsupervised` / `switchover` | A rolling update (image or parameters) moves the primary to an updated standby instead of restarting it in place. That makes the write outage a switchover rather than a restart. |
| `enablePDB` | `true` (the default) | During a drain, one replica stops at a time and the primary is protected. |
| `walStorage` | not set | A separate WAL volume on the same NVMe buys nothing here. |

- **Backups.** The cluster has none today, and the Barman Cloud plugin isn't installed. CNPG calls a cluster without a backup or WAL archive unsafe, and replicas are not backups: a bad write, a dropped table or a broken shim migration replicates in milliseconds. The owner should choose:
  - the plugin with a daily `ScheduledBackup` plus WAL archiving (RPO 5 minutes, CNPG's `archive_timeout`) to an S3 target, such as a MinIO bucket on the NAS;
  - or accepting the loss of anything clustarr can't rebuild: watch state, play history, users' settings. Clustarr re-projects the library rows themselves.

  local-path has no CSI snapshots, so volume-snapshot backups aren't available.
- **Losing a control-plane node for good.** `kubectl cnpg destroy <release>-plex-db <n>` removes that instance's claim. Because of the required anti-affinity, CNPG can rebuild it only once a third control-plane node exists again. Until then the cluster runs on two instances, which `preferred` durability tolerates.
- **Plex follows the primary.** cluster-plex's preferred pod affinity changes from `cnpg.io/cluster: <release>-plex-db` to that plus `cnpg.io/instanceRole: primary`, so plex-0 schedules beside the primary rather than any instance. This is a talos values change; no cluster-plex chart change is needed. The affinity applies only when plex-0 is scheduled, so after a failover Plex stays put and queries cross nodes until it next restarts.
- **Before talos:** test that the shim reconnects through a switchover on the standalone rig, under load, as every shim change is tested before talos. A switchover resets the shim's connections.

### 5.2 NATS: move nats-1 and nats-2 onto control-plane nodes

The nats subchart can't call the parent's helpers. So the talos values write the profile literally under `nats.podTemplate.merge.spec`:

- `affinity.nodeAffinity` with the control-plane term;
- `affinity.podAntiAffinity` on the hostname, selecting `app.kubernetes.io/name: nats`;
- the tolerations.

`nats.natsBox.podTemplate.merge.spec` gets the workers profile the same way.

**Render-time check.** `clustarr.validate` checks the literal copy: with `placement.enabled`, a render whose `nats.podTemplate.merge.spec.affinity` lacks the control-plane expression fails, naming the key. The parent can read the subchart's values, so one source can't drift from the other.

**Migration.** Each server keeps its local-path claim on the node it was created on. Moving one means giving it a new, empty claim and letting JetStream re-replicate it. All 42 streams and 54 consumers are R3, so one server at a time is safe:

1. The upgrade rolls the StatefulSet from nats-2 down. nats-2's new pod requires a control-plane node while its claim's PV is pinned to f4, so it goes Pending. That Pending pod is the gate that keeps the roll one server at a time.
2. Check that every stream is current on the other two: `/jsz?streams=true` on each, and no stream without a leader.
3. Delete claim `clustarr-nats-js-clustarr-nats-2`. The pod schedules on a free control-plane node with a fresh 30 GiB claim, rejoins under the same server name, and catches up every R3 stream from its leaders.
4. Wait for `/healthz` to answer ok on all three before doing the same for nats-1.

nats-0 is already on .34 but its claim is 10 GiB, against 30 GiB everywhere else, so step 3 also runs for nats-0 last.

A power cut mid-step is the case of 2026-10-21, when two R3 streams were left without a leader after a power loss. The recovery is the same:

- move the RAFT group directories of the replicas stuck replaying a snapshot aside;
- leave the stream stores in place;
- let the candidate win with their empty votes.

### 5.3 ui: 1 → 3

`ui.placement: controlPlane` gives it the `onePerNode` treatment from §3: replicas 3, maxSurge 0, a PDB. The chart already injects the shared signing-key Secret. In kustomize that Secret is `optional: true`, so a second replica there would sign differently. The kustomize overlay that enables placement must create the Secret, and `clustarr.validate` refuses `ui.replicas > 1` without it.

Each replica runs its own informer cache and NATS watch: about 255 MiB and next to no CPU.

### 5.4 manager: 1 → 3, with the leader published as the Live TV endpoint

Three replicas with one lease give two warm standbys. The cache is already synced, so a failover takes seconds: the leaving leader releases the lease on a graceful exit, and a crash costs at most the 60 s lease. A replica starting cold takes up to 10 minutes to sync on this library.

**Live TV must reach the leader only.** The design publishes the leader as the Service's endpoint:

- **The Service.** `<release>-livetv` becomes a selector-less Service; kustomize's `config/manager/livetv-service.yaml` changes the same way.
- **The runnable.** A new leader-only runnable, `app/livetv/leaderendpoint`, waits until the tuner is listening (the tuner exposes a `Listening()` channel). It then writes the EndpointSlice `<release>-livetv`:
  - labels: `kubernetes.io/service-name: <release>-livetv` and `endpointslice.kubernetes.io/managed-by: clustarr.io/manager`;
  - one endpoint: `POD_IP` (new downward-API env beside `POD_NAME`), with `targetRef` naming its own pod;
  - the `livetv` port.

  It applies the slice under the manager's field manager.
- **Takeover.** A new leader overwrites the slice. The old address lingers only until the new leader's tuner listens, which is the same outage a single replica's restart has today.
- **RBAC.** `endpointslices` get, create, update and patch, by kubebuilder marker. RBAC is generated per identity, and only the manager's role gains it.
- **One replica too.** The mechanism also runs with one replica, so both installers have a single behaviour, and so do the chart/kustomize agreement tests.
- **NetworkPolicy.** `livetv.networkPolicy` keeps selecting the manager's pods. The tuner port is admitted from Plex's pods to all three, which is harmless on the two that aren't listening.

**Rejected alternatives:**

- *A leader label on the pod as the Service selector.* It needs `pods/patch`, and a leader whose container restarts in place keeps the label until something clears it.
- *Readiness tied to leadership.* Two permanently unready replicas break the Deployment's availability, its PDB and every rollout.

Everything else is already safe with three replicas:

- **External Metrics.** Every replica answers the External Metrics API from NATS. The APIService's Service already sets `publishNotReadyAddresses`.
- **Leader-only gauges.** The queue gauge and the dispatch ledger appear only on the leader's `/metrics`, so dashboards should take `max` across pods. A new `clustarr_manager_leader` gauge (1 on the leader) makes the leader visible.
- **Topology.** `EnsureTopology` runs on every replica before start, and is idempotent. `KeepTopology` and `WatchDeadLetters` are leader-only.

The owner accepted the two standbys (2026-10-21) and asked for the manager's memory to come down. The next subsection is that investigation.

#### 5.4.1 Manager memory: where it goes and how to cut it

**Measured on talos, 2026-10-21.** Sources: Mimir's Go runtime series for the manager pod, plus a scratch program that decoded `kubectl` dumps into clustarr's own API types, with `managedFields` stripped as the cache's transform does.

| Measure | Value |
| --- | --- |
| Resident memory | 716 MiB now; 1.23 GiB peak over 7 days |
| Heap allocated (live plus not-yet-collected) | 477 MiB; next GC at 605 MiB |
| Live heap (next GC / 2, `GOGC` 100) | about 300 MiB |
| MediaFile cache (10,955 objects) | 48.9 MiB |
| Episode cache (21,153 objects) | 47.6 MiB |
| Movie and Series caches | 12.3 MiB |
| Allocation rate | 26–28 MiB/s, a GC every 7–8 s |
| `GOGC` / `GOMEMLIMIT` | 100 / 3.2 GiB (the limit never binds) |

**The informer cache is not the problem.** The four big kinds cost about 110 MiB, and interning every string in them saves nothing, because most strings are unique per object.

The resident memory is the live heap doubled by `GOGC` 100, inflated further by allocation churn. 28 MiB/s is about one full MediaFile list every two seconds. Lists through the cached client deep-copy every object they return, and `MediaFileList` and `EpisodeList` are used in 31 and 22 non-test places. Only `app/remediation/subtitles` and `app/remediation/profilesource.go` pass `client.UnsafeDisableDeepCopy` today.

The other roughly 190 MiB of the live heap is outside the four caches, and attributing it needs a heap profile:

- the remediation loop's keys;
- the admission and dispatch ledgers;
- the artwork object index;
- the NATS client;
- informer indexes (8 on MediaFile).

**Reductions, in order:**

1. **Measure first.** Add a chart value `manager.pprofBindAddress`; the `--pprof-bind-address` flag already exists. Then take `inuse_space` and `alloc_space` profiles on talos through `kubectl port-forward` over an hour of normal work. They rank items 3 and 6, and they size whatever item 3 can't explain.
2. **Bound the GC.** Set `GOGC=50` and `GOMEMLIMIT=1GiB` in place of 3.2 GiB, keeping the 4 GiB limit.
   - Expected effect: the leader's heap target becomes about 450 MiB instead of about 600, and the 1.23 GiB peaks are capped near 1 GiB. Standbys allocate almost nothing, so they sit near their live heap.
   - Cost: roughly twice as many GC cycles on the leader, whose CPU is about 56m today. Watch `go_gc_cycles` and `rate(process_cpu_seconds_total)`.
   - These are two env values and need no code.
3. **Cut copying.** Pass `client.UnsafeDisableDeepCopy` on every whole-set List of MediaFiles and Episodes whose results are only read. The pattern already exists in `app/remediation/subtitles`.
   - Candidates for the profile to confirm:
     - `app/transcode/admission/{sweep,rebuild}.go`
     - `app/transcode/controller/transcodeprofile/{counts,controller}.go`
     - `app/remediation/{transcode,probe,downloads}/…`
     - `app/import/manager/scanapply/orphans.go`
   - The rule each converted call must keep: an object from an uncopied list is the cache's own object. It is never mutated, never passed to a write, and never retained past the reconcile.
4. **Scope the cache to the namespace.** The chart passes `--watch-namespace=<release namespace>`.
   - Today the manager runs with no `--watch-namespace` and caches every namespace's Jobs, Deployments, StatefulSets, HPAs and Leases.
   - The saving is small (a few MiB), and so is the churn from other namespaces' events. Nodes are cluster-scoped and unaffected.
5. **Intern only low-cardinality strings.** A cache transform for Episode and MediaFile canonicalises condition `type`, `status`, `reason` and `message`. For example, "no evidence the file's audio is wrong" is held 21,141 times. That is about 10 MiB. This is a value-preserving transform, so the server-side-apply complete-declaration rule is untouched. A transform that removes fields would break that rule: a reassert from the cache would release whatever it stripped.
6. **Later, an API question for the owner.** Conditions are 39% of an Episode's bytes: 5 per episode, mostly constant ("aired=true", "phase=Unmonitored", WrongLanguage False on 21,141 of 21,153). That weight lands on etcd, the apiserver and every client cache, the ui's included. Dropping conditions that only restate a status field would shrink all of them. It changes what `kubectl` shows, so it isn't part of this design.

**Standbys and warmth.** controller-runtime starts an informer when it is first asked for one: a controller's watch (leader-only), a field index (registered at setup, so every replica has one), or a cached read. So a standby today is warm only for the kinds that have an index, MediaFile and Episode among them. Those are the two whose initial list dominates the 10-minute start budget.

The design adds an `EveryReplica` warmer for exactly the kinds whose initial sync matters at failover: MediaFile, Episode, Movie, Series. It warms no other kind, so a standby's memory stays close to those caches plus its live overhead.

Expected per control-plane node after items 2–4: the leader at about 500–600 MiB, each standby at about 300–400 MiB, against 716 MiB, with peaks of 1.23 GiB, today. Item 1's profile will confirm or correct these figures. The talos requests stay at 500m / 1 GiB with the 4 GiB limit until it does.

### 5.5 Everything else: the workers profile

Every Helm-rendered pod template not named in §1 takes the workers profile:

- every agent;
- markers (its GPU exclusion and hard spread are kept, ANDed with the profile);
- the release-index Postgres;
- nats-box;
- the ui-less jobs.

The release-index Postgres is today on talos-10-1-40-33, a control-plane node, on a local-path claim. It moves with CNPG, never by hand:

1. Set `instances: 2` with the workers profile. CNPG builds instance 2 on a worker.
2. Promote it (`kubectl cnpg promote <release>-postgres <release>-postgres-2`).
3. Remove instance 1 (`kubectl cnpg destroy <release>-postgres 1`); its claim and pod go.
4. Set `instances: 1`.
5. Before and after, compare the row counts of the blocklist and release tables.

cluster-plex's own CronJobs and its plex-workers get the workers profile through cluster-plex's values. Where cluster-plex lacks a key, it gains one in cluster-plex's chart.

**Rule: a workload behind a LoadBalancer Service runs on the control plane** (owner, 2026-10-21). The control-plane nodes are the cluster's BGP peers, and a Service with `externalTrafficPolicy: Local` is answered only from a node that runs one of its pods.

- In clustarr that is Plex alone: `plex`, 10.1.1.102.
- The cluster's other LoadBalancers are outside clustarr: the Cilium gateway, kube-apiserver and tinkerbell.
- Plex keeps its current placement, which already is the control-plane profile: a required control-plane node, the toleration, one per node, and a preference for the node of the database's primary (§5.1).
- The talos values write it literally under `cluster-plex.plex`, as for NATS.
- `clustarr.validate` fails a render with `placement.enabled` where Plex's Service is a LoadBalancer but its affinity lacks the control-plane expression.
- `plex.replicas` stays 1; scaling Plex is outside this design.

### 5.6 Pods the manager builds

The chart passes the workers profile to the manager as `--worker-placement`, a JSON document of node-affinity expressions and tolerations. It's empty when placement is off. Every pod template the manager builds ANDs it into its required node affinity and appends the tolerations:

- **engines** (`app/grab/controller/downloadclient/workload.go`): DownloadClient `spec.nodeSelector` and `spec.tolerations` are kept as they are, so usenet stays pinned to b8;
- **transcode pool Jobs** (`app/transcode/controller/pool/template.go`): the GPU classes' required affinity gains the profile's expressions, and the cpu class gets them where it had none;
- **graft Jobs** (`app/remediation/graft/job.go`): from the same pool template.

This closes the gap in §2 whether or not the control-plane nodes are ever tainted. It follows the CLAUDE.md rule "a pod a controller builds at runtime escapes every guard the installers have", so it comes with a test of the rendered pod spec (§8).

## 6. Images

The manager and ui on arm64 need the `clustarr` image as a multi-arch manifest. `make docker-build` gains `docker-buildx-clustarr`:

```sh
docker buildx build --platform linux/amd64,linux/arm64 --push
```

with the same Dockerfile, which already cross-compiles. The talos deploy builds the clustarr image through it. The `native` image stays amd64; nothing on the control plane runs it.

**The UPX-packed arm64 binaries are the one unverified piece.** Before the manager or ui moves, a one-off pod on each control-plane node runs `manager --version` and `ui --version`. If they fail (UPX and a kernel with pages larger than 4 KiB), the arm64 build skips UPX: a build arg, since the image size only matters for pulls.

## 7. Retiring the pin-amd64 post-renderer

With `placement.enabled`:

- every clustarr pod template carries a required node affinity;
- every manager-built pod template carries the workers profile;
- cluster-plex's templates carry theirs.

The post-renderer then skips every object. One upgrade runs with it and one without; `helm template` must render the same objects both ways, and that is the proof. Until then it stays, because it overwrites an arch nodeSelector and must never see a control-plane component without a required node affinity. Every control-plane component has one by construction.

## 8. Tests and guards

The unify branch's rule applies (CLAUDE.md): guards the test batch hasn't written yet are marked † until written.

- **`TestPlacementProfilesRenderPerComponent`†** (chart): with placement on,
  - every pod template has the profile's expressions in every required term;
  - every `onePerNode` component has required hostname anti-affinity, maxSurge 0 and a PDB;
  - placement off renders today's bytes.
- **`TestNATSPlacementMatchesTheControlPlaneProfile`†:** the render-time check of §5.2 fails a drifted copy.
- **`TestEveryPodTheManagerBuildsCarriesTheWorkerPlacement`†:** engines, every pool class and graft Jobs. It renders the real builders, never a fixture shaped like the answer.
- **`TestLiveTVServiceRoutesToTheLeader`†** (envtest): two managers.
  - The slice names the leader's IP only after its tuner listens.
  - After the leader stops, the slice names the new leader.
- **`TestStandbyWarmsTheFailoverKinds`†** (envtest): a non-leader replica's cache has synced informers for MediaFile, Episode, Movie and Series, and for no kind outside the warmer's list and the indexed ones.
- **`installers_topology_test.go`:** the manager's pin becomes "1 with placement off, `profiles.controlPlane.replicas` with it on". Kustomize stays at one replica.
- **`TestChartAndKustomizeAgreePerComponent`:** covers the selector-less `livetv` Service and its RBAC.

## 9. Rollout on talos

Each step is its own upgrade, verified before the next one, and revertible by itself. The values come from the saved `talos-values.yaml` as always.

| # | Change | Verify | Revert |
| --- | --- | --- | --- |
| 0 | Push the multi-arch clustarr image; run the version pods on .32/.33/.34 | `--version` exits 0 on all three | n/a |
| 1 | Ship the code and chart with `placement.enabled: true`, every component on `workers` (none on controlPlane yet), the post-renderer still on | Nothing moves; graft and pool pods now carry the workers expressions | `placement.enabled: false` |
| 1b | Manager memory items 1–2 (§5.4.1): pprof on, `GOGC=50`, `GOMEMLIMIT=1GiB`; take the profiles | Resident memory and GC CPU after 24 h, against the 716 MiB / 56m baseline | Restore the env values |
| 2 | plexDatabase → controlPlane, 3 instances, the §5.1 replication settings | 3 instances ready, `synchronous_standby_names` lists both, replication lag about 0; one switchover drill while browsing Plex | `instances: 1`, `synchronous` unset |
| 3 | NATS → controlPlane (§5.2, one server at a time; nats-0's claim to 30 GiB) | `/healthz` ok on all three, no stream without a leader, the manager's `topology ensured` | Revert the values; the moved servers stay where they are |
| 4 | Release-index Postgres → workers (§5.5) | Row counts match; index RPCs answer | Promote back |
| 5 | ui → controlPlane | 3 ready; `/art/search` signed URLs work from any replica | `placement: workers` |
| 6 | manager → controlPlane, with the standby warmer and the item-3/4 changes the profile justified | `livetv` EndpointSlice names the leader; tune a channel; delete the leader pod; tune again within a minute; the standbys' memory near §5.4.1's estimate | `placement: workers` |
| 7 | Drop the post-renderer (§7) | Identical render; all pods placed as before | Re-add `--post-renderer` |

## 10. Risks

- **etcd shares a disk with the moved workloads.** CNPG WAL fsyncs and NATS's R3 file store add write load to the NVMe partition etcd fsyncs on. Watch `etcd_disk_wal_fsync_duration_seconds` (p99 under 10 ms) and `etcd_disk_backend_commit_duration_seconds` (p99 under 25 ms) through steps 2, 3 and 6, with an alert left in place.
  - NATS's file store syncs every 2 minutes, not per message; Postgres syncs per commit.
  - If etcd suffers, revert NATS first: it is the heaviest writer.
- **Memory on talos-10-1-40-33.** It runs Plex, the Plex primary and the observability stack (mimir, tempo, minio). Adding NATS (2 GiB, requests equal limits), a manager (about 1 GiB) and a ui (about 0.3 GiB) takes it from about 9.1 to about 12.7 GiB of 14.9.
  - The other two end near 10–11 GiB.
  - Step 4 frees the release index's share of .33.
  - If .33 is still tight, give the CNPG instances requests (250m / 1 GiB), so the scheduler sees them, and leave the observability stack's placement to its owner.
- **CPU requests.** NATS requests 1 CPU per server, against 3.95 allocatable per node. The control-plane components plus the existing pods come to roughly 3 CPU requested on .33.
- **local-path does not enforce a claim's size.** Every "5 GiB" or "30 GiB" claim on a control-plane node is a directory on the 952 GB EPHEMERAL filesystem that etcd also uses. A runaway writer fills etcd's disk, not its own volume. The writers that can run away:
  - Postgres's WAL retention, bounded by §5.1's `max_slot_wal_keep_size`;
  - NATS, bounded by JetStream's `max_file_store`.

## 11. Decisions and open questions

Decided by the owner, 2026-10-21:

1. **Plex stays on the control plane**, and so does any workload behind a LoadBalancer Service (§5.5).
2. **Plex database replication** follows CNPG's recommendations for three instances, as worked out in §5.1:
   - quorum-synchronous to one standby;
   - `preferred` durability;
   - no failover quorum;
   - HA slots with a WAL cap;
   - switchover updates.
3. **Tainting the control-plane nodes** is deferred. The design works without it, and a later taint needs only the tolerations it already renders.
4. **Two manager standbys** are accepted, with the memory work of §5.4.1.

Still open:

5. **A backup target for the Plex database** (§5.1): the Barman Cloud plugin to an S3 bucket, such as MinIO on the NAS, or no backup.
6. **Episode and MediaFile conditions** (§5.4.1 item 6): whether to stop writing conditions that only restate a status field.
