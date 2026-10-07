# Cold cutover: chart 0.4.x to the unify branch on kind-cluster-plex

**Status:** written 2026-10-07; nothing here has run. Every step that writes to the cluster
needs the owner's explicit OK, given for this cutover ("Build, don't deploy"). Nothing in this
document is automated. The new release assumes only that steps 5-10 ran before step 11 installs
it.

**Why this exists.** The owner's decision of 2026-10-07: cold turkey everywhere. The unify branch
(`unify-manager-agent`: the manager, the agents, the ui, the remediation loop, agents that never
write Kubernetes, and the unbranded names) carries **no migration code**. It does not adopt
Downloads, TranscodeJobs, SubtitleRequests or AudioGrafts, does not read a 0.4.x name, does not
hand a durable over, and has no rollback or export tooling. Whatever a running 0.4.x install
leaves behind that the new release would trip over is removed once, by hand, here.

**Accepted losses** (owner, 2026-10-07):

- Every Download (122 on 2026-10-06): its spec and status, its `download.clustarr.io/blocklisted`
  label and `blocklistedUntil`, held imports, pauses and priorities. In-flight and seeding
  transfers stop. Their bytes stay on disk; the new engines remove each transfer they find with
  no claim after `UnidentifiedGrace` (10 minutes), keeping its data (ADR-0019's design, §6.7). A re-grab of
  the same release gets the same entry id as the old Download's name, so an engine that still
  holds the transfer claims it instead of adding a second one.
- Every TranscodeJob, SubtitleRequest and AudioGraft, and so their history: transcode verdicts,
  subtitle search backoff (`attempts`, `nextSearchAt`, `lastError`), graft state. The remediation
  loop recomputes transcode, subtitle and graft state from the files and their probes.
- Every dead letter on `CLUSTARR_DLQ` (step 8): its stream and durable are gone, so none can be
  replayed.

**Kept as they are:** Movies, Series, Episodes, Artists, Albums, Authors, Books, Audiobooks,
Comics, Issues, MediaFiles, every profile, provider, indexer, list, root folder and download
client, the files under `/data`, the release index (`<fullname>-index` PVC), the artwork and
fingerprint object stores, and the KV buckets the new topology still declares.

**Names used below.** Kube context `kind-cluster-plex`, namespace `clustarr-system`, Helm release
`clustarr` (fullname `clustarr`). Old names are chart 0.4.x's, from main at the time of the
cutover; re-check each list against the live cluster in step 1 before deleting anything. Set
`K="kubectl --context kind-cluster-plex -n clustarr-system"` and `H="helm --kube-context
kind-cluster-plex -n clustarr-system"`. `nats` is the NATS CLI pointed at the release's server
(the NATS subchart's nats-box, or `$K port-forward svc/clustarr-nats 4222` and `--server
nats://127.0.0.1:4222`).

**Plex.** cluster-plex depends on the `clustarr-ui` Service on port 8080, the art signing key,
Secret `plex-token` and the catalog CRDs, all kept. If Plex itself must be redeployed, it is
redeployed only through cluster-plex's `k8s/overlays/kind-cluster-plex`
(`kubectl --context kind-cluster-plex apply -k k8s/overlays/kind-cluster-plex`), never
`make deploy-kind` or `make e2e`, which install stand-in catalog CRDs over the live ones.

---

## Order of operations

| Step | What | Why it is here |
| --- | --- | --- |
| 0 | Preconditions and freeze | nothing starts before the owner's OK and a green branch |
| 1 | Record (read-only) | the counts and names every later step checks against |
| 2 | Protect data | Helm would otherwise delete the index PVC |
| 3 | Build and load images | a `kind load` stalls etcd; do it while the old system is healthy |
| 4 | Quiesce | let imports finish; nothing new starts |
| 5 | Stop every old writer | no old controller or engine may see what follows |
| 6 | Strip finalizers, delete the four kinds, delete their CRDs | only after step 5, so `removeDataOnDelete` never runs |
| 7 | Old Kubernetes leftovers | leases, engine workloads, pool and graft Jobs |
| 8 | Old NATS state | the new transcode stream cannot be created while the old one exists |
| 9 | Stale `managedFields` | renamed managers must not leave co-owners behind |
| 10 | The facade key | Torznab clients keep working |
| 11 | Install the new release | CRDs first, then the chart from fresh values |
| 12 | Verify | |
| 13 | Afterwards | |

---

## 0. Preconditions and freeze

- The owner's OK for this cutover, and separately for resuming transcoding afterwards (step 12):
  the loop re-plans every untranscoded file under the deployed `standard.Version`.
- The branch's final gate (split plan W10.8) and the test batch are green on the commit being
  deployed. Note its short SHA; every image and the chart come from `git archive <sha>`.
- Ask every other session that deploys to kind-cluster-plex to stop (`ListAgents`; the clustarr
  sessions). No `helm upgrade` by anyone until step 12 passes.
- Downloads are live (owner, 2026-10-07). Expect to lose whatever is downloading at step 5.

## 1. Record (read-only)

Save everything under one directory outside the repository.

```bash
$H get values clustarr -o yaml > values-0.4.yaml        # the base of step 11's values, never --reuse-values
$H get manifest clustarr > manifest-0.4.yaml
$K get downloads,transcodejobs,subtitlerequests,audiografts -o yaml > folded-kinds.yaml   # a record, not a restore source
$K get downloads -o json | jq -r '.items[] | [.metadata.name, .status.phase, (.status.outputPath // ""), ((.metadata.labels // {})["download.clustarr.io/blocklisted"] // "")] | @tsv' > downloads.tsv
for k in movies series episodes mediafiles downloadclients indexers importlists rootfolders; do echo "$k $($K get $k --no-headers | wc -l)"; done > counts.txt
$K get leases,deploy,statefulsets,jobs,serviceaccounts,secrets -o wide > objects.txt
nats stream ls -a > streams.txt
for s in $(nats stream ls -n); do echo "== $s"; nats consumer ls "$s" -n; done > consumers.txt
nats kv ls -n > buckets.txt
```

`downloads.tsv` lists each lost Download's payload path: after the cutover the owner can import
one by hand (a LibraryScan whose `spec.subpath` names it) or delete it. Check `objects.txt` for
the facade Secret (`clustarr-indexarr-facade`) and note whether it exists; never print its data
into a file that leaves the cluster.

## 2. Protect data

```bash
$K annotate pvc clustarr-index helm.sh/resource-policy=keep --overwrite   # Helm would delete it (PV reclaim Delete)
$K get pvc clustarr-data -o jsonpath='{.metadata.annotations}'           # keep, or values use existingClaim
$K get secret clustarr-ui-art-signing-key -o jsonpath='{.metadata.annotations}'   # helm.sh/resource-policy: keep
```

## 3. Build and load images

Build the `clustarr` and `native` images (and `native-debug` if wanted) from `git archive <sha>`
(split plan W6.4-W6.6, `make docker-selfcheck`), then `kind load docker-image --name
cluster-plex <image>` for each. A load unpacks gigabytes onto the node's disk, which etcd shares;
wait until `$K get leases -w` stops showing holder transitions before going on (CLAUDE.md, "On
kind, loading an image into the node stalls etcd").

## 4. Quiesce

- Let any import in flight finish: the `importarr-worker` logs show no `fileimport` handling, and
  no Download reads `Completed` with `status.import.phase: importing`.
- A transcode in flight is lost at step 5; its source is untouched, because the worker swaps a
  verified output in by rename and nothing before that rename touches the source. If the owner
  prefers to let running encodes finish, wait until no TranscodeJob reads `Running` first. A
  `<stem>.part.<ext>` left behind by a killed encode is the rescan's to clean up
  (`fsops.IsPart`).

## 5. Stop every old writer

The new release has no legacy-lease gate (`--legacy-lease-check` and `k8s.NewGatedLeaseLock`
were removed with the cold turkey, 2026-10-07). This step is the only guard against two
generations writing at once.

```bash
for d in catalogarr catalogarr-metadata importarr importarr-worker indexarr grabarr squasharr captionarr captionarr-worker segmentarr-worker; do
  $K scale deploy/clustarr-$d --replicas=0
done
# The engines, which honour removeDataOnDelete themselves (grabarr is down, so nothing scales them back up):
$K get statefulsets,deployments -l app.kubernetes.io/component=grabarr-engine -o name | xargs -r -n1 $K scale --replicas=0
# Transcode pool Jobs, and graft and reduce Jobs (they hold GPUs and write /data):
$K delete jobs -l app.kubernetes.io/managed-by=squasharr --cascade=background
$K delete jobs -l squasharr.clustarr.io/audiograft --cascade=background --ignore-not-found
```

Wait until every pod of those workloads is gone (longest grace about 120 s) and each of the six
leases `{catalogarr,importarr,indexarr,grabarr,squasharr,captionarr}.clustarr.io` shows an empty
`holderIdentity` or a `renewTime` more than 60 s old. Leave `clustarr-ui` running (read-only;
Helm replaces it in step 11; it logs errors about the kinds step 6 deletes). Do not restart NATS:
on a single node its non-Durable buckets live in memory, and step 8 decides what goes.

Check: `$K get pods` shows only `clustarr-ui`, NATS and anything not clustarr's.

## 6. Strip finalizers, delete the four kinds, delete their CRDs

Only now, with no 0.4.x controller and no engine running. A Download carries
`download.clustarr.io/download` (grabarr's `removeDataOnDelete` path, which deletes
`status.outputPath`) and `download.clustarr.io/engine` (the engine's, which removes the transfer
and, by default, its data); a TranscodeJob carries `squasharr.clustarr.io/task-withdrawal`.
With their controllers stopped and the finalizers stripped, deleting the objects runs no code, so
no byte under `/data` is removed.

1. Nothing kept may be owned by an object about to go (the garbage collector would follow):

   ```bash
   for r in $(kubectl --context kind-cluster-plex api-resources --verbs=list --namespaced -o name); do
     $K get "$r" -o json 2>/dev/null \
       | jq -r '.items[] | select(any(.metadata.ownerReferences[]?; .kind == "Download" or .kind == "TranscodeJob" or .kind == "SubtitleRequest" or .kind == "AudioGraft")) | "\(.kind)/\(.metadata.name)"'
   done
   ```

   Expect nothing but graft or reduce Jobs (already deleted in step 5) and their pods. Stop and
   ask if anything else appears.

2. Strip every finalizer, then delete the objects:

   ```bash
   for k in downloads transcodejobs subtitlerequests audiografts; do
     $K get $k -o name | xargs -r -I{} $K patch {} --type=merge -p '{"metadata":{"finalizers":null}}'
     $K delete $k --all --wait=true
     test "$($K get $k --no-headers 2>/dev/null | wc -l)" -eq 0 || echo "STOP: $k remain"
   done
   ```

   Pace it if etcd complains (`xargs -P1` with a short sleep); 122 Downloads, about 2,500
   SubtitleRequests and about 630 TranscodeJobs on 2026-10-06.

3. Delete the CRDs (Helm never deletes a CRD):

   ```bash
   kubectl --context kind-cluster-plex delete crd downloads.download.clustarr.io transcodejobs.transcode.clustarr.io \
     subtitlerequests.subtitle.clustarr.io audiografts.transcode.clustarr.io
   ```

   `downloadclients.download.clustarr.io`, `transcodeprofiles.transcode.clustarr.io` and the
   subtitle profile and provider CRDs stay. A CRD stuck in `Terminating` means an object kept a
   finalizer: repeat step 2 for it.

## 7. Old Kubernetes leftovers

Helm deletes what chart 0.4.x rendered and 0.5.0 does not (the ten Deployments, their Services,
ServiceAccounts, ServiceMonitors, ClusterRoles and bindings, the engine ServiceAccount
`clustarr-grabarr-engine` and its role) at step 11. Delete by hand what a controller created:

```bash
$K delete lease catalogarr.clustarr.io importarr.clustarr.io indexarr.clustarr.io grabarr.clustarr.io squasharr.clustarr.io captionarr.clustarr.io --ignore-not-found
# Engine workloads: the new manager renders <client>-engine with component grab-engine, and a
# selector is immutable, so the old workload must go. Its scratch claims (StatefulSet claim
# templates, retention Retain) and <client>-scratch stay and are re-bound by name.
$K delete statefulsets,deployments -l app.kubernetes.io/component=grabarr-engine
$K get pvc    # confirm every scratch claim is still Bound
$K get jobs -l app.kubernetes.io/managed-by=squasharr     # nothing left from step 5
```

Dead-letter annotations on kept objects name messages step 8 purges; remove them so the
`DeadLettered` condition clears:

```bash
for r in $(kubectl --context kind-cluster-plex api-resources --namespaced -o name | grep -E '\.clustarr\.io$'); do
  $K get "$r" -o json | jq -r '.items[] | select(.metadata.annotations["clustarr.io/dead-lettered"]) | .metadata.name' \
    | xargs -r -I{} $K annotate "$r" {} clustarr.io/dead-lettered- clustarr.io/dead-letter-seq-
done
```

## 8. Old NATS state

With every old process stopped and the new release not yet installed. The new manager creates
its whole topology at start; it never deletes or renames an old object, and two of them would
stop it: `CLUSTARR_WORK_SQUASHARR` and the new `CLUSTARR_WORK_TRANSCODE` both take
`clustarr.work.transcode.>`, which JetStream refuses to overlap, and the old memory streams count
against the single-node memory budget the new ones need.

1. **Decide the event replay first.** The new `catalog-history` (on `CLUSTARR_EVENTS`) and
   `catalog-rss-matcher` (on `CLUSTARR_RELEASES`) are new durables with `DeliverAll`: they read
   each stream from its first retained message (up to 7 days of events; the release firehose's
   window). Recommended: once the old durables have nothing pending, purge both streams, so the
   history sink does not record a week twice and the RSS matcher does not re-match every retained
   release into a burst of grabs against the indexers (CLAUDE.md, the 2026-10-06 nzbgeek burst).
   The alternative is to let them replay; a re-matched release that an old Download was fetching
   resumes through the engine's name match, but the grab burst is not paced.

   ```bash
   nats consumer info CLUSTARR_EVENTS catalogarr-history -j | jq '.num_pending, .num_ack_pending'      # 0 0
   nats consumer info CLUSTARR_RELEASES catalogarr-rss-matcher -j | jq '.num_pending, .num_ack_pending'  # 0 0
   nats stream purge CLUSTARR_EVENTS -f
   nats stream purge CLUSTARR_RELEASES -f
   ```

2. **Delete the six old work streams** (their durables go with them: `catalogarr-search-high`,
   `-search-normal`, `-grab`, `-metadata`, `-artwork-fetch`, `-artwork-render`, `-markers`,
   `-segments-plan`, `-segments-result`; `importarr-scan`, `-list`, `-fileimport`; `indexarr-rss`;
   `captionarr-fetch-high`, `-fetch-normal`; `squasharr-transcode-results` and every
   `squasharr-transcode-<profile>-<class>` pool durable; `segmentarr-analyze`):

   ```bash
   for s in CLUSTARR_WORK_CATALOGARR CLUSTARR_WORK_IMPORTARR CLUSTARR_WORK_INDEXARR CLUSTARR_WORK_CAPTIONARR CLUSTARR_WORK_SQUASHARR CLUSTARR_WORK_SEGMENTARR; do
     nats stream rm "$s" -f
   done
   ```

   `CLUSTARR_WORK_SEGMENTARR` is file-backed and holds segment plan and analysis tasks only; the
   analysis results live in the `clustarr-segments` bucket, which stays, and fingerprints in the
   `clustarr-fingerprints` object store, which stays.

3. **Delete the old durables on kept streams:**

   ```bash
   nats consumer rm CLUSTARR_EVENTS catalogarr-history -f
   nats consumer rm CLUSTARR_EVENTS catalogarr-redownload -f
   nats consumer rm CLUSTARR_RELEASES catalogarr-rss-matcher -f
   ```

   `clustarr-dlq-projector` on `CLUSTARR_DLQ` keeps its name; leave it.

4. **Delete the old dead-letter watchers** on `CLUSTARR_ADVISORIES`, named
   `clustarr-dlq-watch-<stream>-<durable>`: every one whose stream is one of step 2's or whose
   durable is one of steps 2 and 3. Keep `clustarr-dlq-watch-CLUSTARR_DLQ-clustarr-dlq-projector`.

   ```bash
   nats consumer ls CLUSTARR_ADVISORIES -n | grep -E 'ARR-|-(catalogarr|importarr|indexarr|captionarr|squasharr|segmentarr)-' \
     | xargs -r -n1 nats consumer rm CLUSTARR_ADVISORIES -f
   nats stream purge CLUSTARR_ADVISORIES -f      # advisories about consumers that no longer exist
   ```

5. **Purge the dead letters** (accepted loss: their streams and durables are gone, so none can be
   replayed, and the four deleted kinds' payloads are v1 shapes nothing decodes):

   ```bash
   nats stream purge CLUSTARR_DLQ -f
   ```

6. **Delete the KV buckets the new topology no longer declares:** `clustarr-leases` and
   `clustarr-pending` (the KV grab lease and the keep-best pending grab, retired by ADR-0019). Also
   delete `clustarr-transcode-leases`, whose keys are TranscodeJob UIDs (the new release keys
   them by MediaFile UID and recreates the bucket at start). Every other `clustarr-*` bucket stays.

   ```bash
   nats kv del clustarr-leases -f
   nats kv del clustarr-pending -f
   nats kv del clustarr-transcode-leases -f
   ```

7. Compare `nats stream ls`, `nats kv ls` and each stream's consumers with the new topology
   (`events.Default()` at the deployed commit, run through `ForSingleNode`): nothing old remains
   that the new topology does not declare, apart from what this step chose to keep.

## 9. Stale `managedFields`

Server-side apply forces ownership, so a renamed manager's old entry would stay a silent
co-owner: when the new manager stops sending a field, the old entry keeps it alive (CLAUDE.md,
"two managers can co-own a field"), and a field the old manager alone owns is never pruned by the
new manager's complete declarations. So rename each old manager's entries to the manager that
applies those fields in the deployed build, merging into an existing entry of that name, and drop
entries of managers nothing applies any more.

**The map** (old → new), from Wave U's table (`docs/superpowers/specs/2026-10-07-unbranded-names-design.md`
§2.5), adjusted for the fold and ADR-0019:

| Old | New | Note |
| --- | --- | --- |
| `catalogarr`, `catalogarr-series`, `catalogarr-worker`, `catalogarr-metadata`, `catalogarr-grab`, `catalogarr-fanout`, `catalogarr-artwork`, `catalogarr-classify` | `catalog`, `catalog-series`, `catalog-worker`, `catalog-metadata`, `catalog-grab`, `catalog-fanout`, `catalog-artwork`, `catalog-classify` | `catalog-classify` owning `spec.qualityProfileRef` is what the import list reads as "classified" |
| `catalogarr-markers` | `catalog` | the loop writes `status.markers` under `catalog` (loop spec D21); merged into the `catalog` status entry |
| `importarr`, `importarr-worker`, `indexarr`, `indexarr-worker`, `grabarr`, `squasharr`, `captionarr` | `import`, `import-worker`, `index`, `index-worker`, `grab`, `transcode`, `caption` | |
| `grabarr-engine`, `captionarr-worker`, `squasharr-pool` | dropped | they wrote only Downloads, SubtitleRequests and pool Jobs, all deleted |

Before using it, check it against the deployed build: every new name must be in
`k8s.FieldManagers()` and not in `k8s.RetiredFieldManagers()` at that commit, and must still be
the writer of those paths (`TestEveryFieldManagerHasItsHome`). A name the build no longer applies
is merged into the manager that now writes its paths, or dropped if none does; a dropped entry's
fields stay as values with no owner until something writes them.

**The rewrite**, per object (dry-run on one object of each kind first, and inspect the result):

```bash
MAP='{"catalogarr":"catalog","catalogarr-series":"catalog-series","catalogarr-worker":"catalog-worker","catalogarr-metadata":"catalog-metadata","catalogarr-grab":"catalog-grab","catalogarr-fanout":"catalog-fanout","catalogarr-artwork":"catalog-artwork","catalogarr-classify":"catalog-classify","catalogarr-markers":"catalog","importarr":"import","importarr-worker":"import-worker","indexarr":"index","indexarr-worker":"index-worker","grabarr":"grab","squasharr":"transcode","captionarr":"caption"}'
DROP='["grabarr-engine","captionarr-worker","squasharr-pool"]'
rewrite='($map|fromjson) as $m | ($drop|fromjson) as $d
  | [.metadata.managedFields[] | select(([.manager] | inside($d)) | not) | .manager = ($m[.manager] // .manager)]
  | group_by([.manager, .operation, .apiVersion, (.subresource // "")])
  | map(reduce .[] as $e (null; if . == null then $e
        else (.fieldsV1 = ((.fieldsV1 // {}) * ($e.fieldsV1 // {}))) | (.time = ([.time, $e.time] | max)) end))
  | if length == 0 then [{}] else . end
  | [{op: "replace", path: "/metadata/managedFields", value: .}]'
patch_one() {   # $1 = resource, $2 = name; a MediaFile's list can pass 128 KiB, so the patch goes by file
  $K get "$1" "$2" -o json | jq -c --arg map "$MAP" --arg drop "$DROP" "$rewrite" > /tmp/mf-patch.json
  $K patch "$1" "$2" --type=json --patch-file /tmp/mf-patch.json
}
for r in $(kubectl --context kind-cluster-plex api-resources --namespaced -o name | grep -E '\.clustarr\.io$') secrets persistentvolumeclaims; do
  $K get "$r" -o json | jq -r '.items[] | select([.metadata.managedFields[]?.manager] | any(test("arr($|-)"))) | .metadata.name' \
    | while read -r n; do patch_one "$r" "$n"; sleep 0.05; done
done
```

The loop covers every object whose `managedFields` names an old manager: every kept kind of the
`catalog`, `index`, `download`, `transcode` and `subtitle` groups, plus the Secrets and
PersistentVolumeClaims in `clustarr-system` (Trakt token and indexer session Secrets, the facade
Secret, the usenet scratch claims). It patches at most 20 objects a second (about 33,000
objects: about 28 minutes), because etcd shares the node's disk on kind. Run `patch_one` by hand
on one object of each kind first and read the result (`$K get <r> <n> --show-managed-fields -o
yaml`): the renamed entries are there, merged, and any entry the patch itself added (a
`kubectl-patch` Update) owns no field. Afterwards re-run the selection: it must print nothing.

## 10. The facade key

The Torznab facade's API-key Secret was `clustarr-indexarr-facade`; the new release reads
`clustarr-index-facade` (or the name the chart's facade `apiKeySecret` value gives) and creates
it with a new random key when it is absent. Pick one:

- **Keep the key (recommended).** Before step 11, copy the old Secret's data into the new name,
  so the index agent finds it and generates nothing:

  ```bash
  $K get secret clustarr-indexarr-facade -o json \
    | jq '{apiVersion, kind, type, data, metadata: {name: "clustarr-index-facade", namespace: .metadata.namespace, labels: {"app.kubernetes.io/component": "index"}}}' \
    | $K apply -f -
  ```

  Clients keep their key; only the facade's address changes to the index agent's Service.
- **Re-key.** Let the release generate a key, read it back
  (`$K get secret clustarr-index-facade -o jsonpath='{.data.apikey}' | base64 -d`) and give it
  to every client.

Either way, update every Torznab client (each tool configured with the facade's
`/{indexer}/api` or `/search/api` URL) to the new Service address, and delete
`clustarr-indexarr-facade` only once every client works (step 13).

## 11. Install the new release

1. **CRDs first** (Helm never upgrades CRDs), from the same `git archive <sha>`:

   ```bash
   kubectl --context kind-cluster-plex apply --server-side --force-conflicts -f config/crd/bases/
   ```

   The four deleted CRDs are not in that directory any more; check none came back.
2. **Values.** Refresh first: run `$H get values clustarr -o yaml` again and diff it with step 1's
   copy (another session may have upgraded meanwhile). Map it onto chart 0.5.0's keys with the
   chart README's "Upgrading from 0.4.x" table (split spec §10.2.8; `values.schema.json` rejects
   every 0.4.x key): the image tags (`image.clustarr.tag`, `image.native.tag`, quoted), the data
   claim (`storage.data.existingClaim`), the ui's Plex provider and external URL, the NATS config
   (`max_payload` 8Mi), the per-service resources moved to the manager, the agents and markers,
   and the facade Secret name if step 10 kept the old one. Never `--reuse-values`.
3. **Upgrade:**

   ```bash
   $H upgrade clustarr <chart dir from the archive> -f values-0.5.yaml --timeout 15m --wait
   ```

   Helm creates the manager, the agent Deployments, markers and the ui, and deletes the ten old
   Deployments, their Services, ServiceAccounts and RBAC. The manager creates the whole NATS
   topology, the probe, records and intake buckets and streams included.

## 12. Verify

- **Leadership:** lease `manager.clustarr.io` is held by the manager pod; none of step 7's leases
  exists again.
- **Kinds:** `kubectl api-resources | grep -E 'downloads|transcodejobs|subtitlerequests|audiografts'`
  prints only `downloadclients`. `kubectl get movies,series,mediafiles` answers.
- **No refused writes:** the manager logs no `Invalid value` or `field not declared in schema` for
  `mediafiles` or any item kind. The kept MediaFiles carry 0.4.x's status shapes (a transcode block
  without `phase`, sidecars keyed by `path`), which the branch's CRD must accept.
- **Field ownership:** a sample MediaFile's `managedFields` carries `import-worker` for spec and
  only `catalog` under status; a sample Movie, Series and Episode carries no `*arr` manager.
- **No mass rework:** MediaFiles stay `Probed=True` (no `ProbeVersion` raise, so no mass
  re-probe); `status.markers` and `status.mediaInfo` of a sample of files are unchanged from step
  1 (cluster-plex reseeds Plex on a change: its logs show no reseed wave); the loop's first pass
  over every file is paced by `--remediation-bulk-writes-per-second` (about 9 minutes for 13,754
  files at 25/s), and etcd's fsync latency stays sane.
- **Engines:** each DownloadClient's `<client>-engine` is re-rendered (component `grab-engine`,
  `/usr/bin/agent --domain=...`, the native image), Ready, and `EngineReady=True`; its scratch
  claims are re-bound. Transfers from 0.4.x appear in `status.unidentifiedTransfers` and go after
  10 minutes; their bytes are still on disk (`downloads.tsv`).
- **Subtitles:** wanted files are dispatched through the backlog gate, not all at once (D22): no
  burst on `CLUSTARR_WORK_CAPTION`, providers not throttled across the board.
- **Transcode:** only with the owner's OK to resume. One pool Job per profile and class appears
  (`/usr/bin/transcode`, component `transcode`, the native image), measures its device and takes a
  task; `TranscodeProfile.status.waitingFiles` counts the backlog.
- **Searches and grabs:** the wanted sweep runs once, index-only for items searched before; no
  indexer is disabled for timeouts; any grab is an entry in its owner's `status.downloads`.
- **Index:** exactly one `agent-index` pod mounts `clustarr-index`; the facade answers a client
  with the step 10 key; indexer health is read from the agent's records.
- **Autoscaling:** `kubectl get apiservice v1beta1.external.metrics.k8s.io` is Available; the HPAs
  exist at `maxReplicas` 1 on this one-node cluster.
- **Plex:** the ui is Ready, `clustarr-ui:8080` answers the Plex provider, and a Plex refresh of
  one movie and one show succeeds.
- **No media programs:** no pod runs an `ffmpeg`, `ffprobe` or `par2` executable.

## 13. Afterwards

- Delete `clustarr-indexarr-facade` once every Torznab client works.
- Import or delete the payloads `downloads.tsv` lists, as the owner decides.
- Record the cutover (date, SHA, Helm revision) where deployments are recorded, and tell the
  other sessions that deploys may resume.

There is no rollback path back to 0.4.x: the four kinds, their CRDs, the old streams and the old
names are gone (owner, 2026-10-07). A failed upgrade is fixed forward, or the cluster is restored
from its own backup.
