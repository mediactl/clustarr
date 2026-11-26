# Cold cutover: the library kinds from custom resources to the library store

**Status:** written 2026-11-26 and run on talos the same day (the last section says how it went
and where it left this text). Every step that writes to a cluster needs the owner's explicit OK,
given for this cutover. Nothing in this document is automated.

**Why this exists.** ADR-0024: MediaFile, Episode, Album, Book, Audiobook and Issue are rows in
Postgres, not custom resources. The release that carries it has no migration (owner, 2026-10-07:
cold turkey): it never reads one of those kinds from the apiserver. What a running install holds
as custom resources is copied into the store once, by hand, here, with `hack/librarycopy` (owner,
2026-11-26: "Copy"), and the custom resources and their CRDs are then removed.

**What is copied.** Every object of the six kinds, as it stands: its uid, creation time,
generation, finalizers, owner references, labels, annotations, spec, status and its ownership
records (`managedFields`), so the manager's first apply to it merges with what the apiserver
recorded. Nothing is recomputed.

**What is lost.** Each object's `resourceVersion` (the store gives it a new one) and its
Kubernetes Events. `kubectl get mediafiles,episodes,albums,books,audiobooks,issues` stops
working; the UI, `psql` with the read-only role and the tables themselves replace it.

**Names used below.** Kube context `admin@talos`, namespace `clustarr-system`, Helm release
`clustarr`. Set `K="kubectl --context admin@talos -n clustarr-system"`. On talos the release is
Terraform's: values change in the cluster workspace, not with `helm` by hand.

## Before the day

1. **The images and charts exist.** clustarr's `clustarr`, `native` and `ui` images from the
   commit that carries ADR-0024; cluster-plex's image and chart from its commit that reads the
   store (`pkg/clustarrlib`); clustarr's chart packaged with that cluster-plex chart version.
2. **The database is ready.** `clustarr-postgres` (three instances on talos) gets a synchronous
   standby: `postgres.cluster.synchronous: {method: any, number: 1, dataDurability: preferred}`
   (the new chart sets this itself when the value is unset). Check its free space first: the
   library's documents are about 400 MiB with their ownership records, beside what the release
   index holds.
   `$K exec clustarr-postgres-1 -c postgres -- psql -U postgres -d clustarr -c "select pg_size_pretty(pg_database_size('clustarr'))"`
3. **Decide the roles.** Either every process connects as the app role (the chart's default,
   nothing to do), or declare `clustarr_ui` and `clustarr_ro` in `postgres.cluster.managedRoles`
   with a password Secret each, and name them in `library.ui` and `library.readOnly` with a
   Secret holding each role's address. The manager grants each its share when it starts.
4. **Announce it.** Live TV is down while the manager is (about ten minutes after it starts);
   nothing is grabbed, imported or transcoded between steps 1 and 6 below.

## The cutover

1. **Stop every writer and reader of the six kinds.**
   `$K scale deploy/clustarr-manager deploy/clustarr-ui --replicas=0`, each agent Deployment to 0,
   and cluster-plex's manager (the `plex` StatefulSet's pods keep serving Plex; its watcher only
   stops projecting). Wait for the pods to go. The transcode pools and the download engines may
   keep running: they write no Kubernetes object.
2. **Count what is there.** One request per kind, served from the apiserver's cache:
   `$K get --raw '/apis/catalog.clustarr.io/v1alpha1/namespaces/clustarr-system/mediafiles?resourceVersion=0' | jq '.items|length'`
   (and `episodes`, `albums`, `books`, `audiobooks`, `issues`). Keep the six numbers.
3. **Copy.** From a checkout of the new commit, with the store's address as the app role:
   `kubectl --context admin@talos -n clustarr-system port-forward svc/clustarr-postgres-rw 5432:5432 &`
   `CLUSTARR_LIBRARY_DSN="$($K get secret clustarr-postgres-app -o jsonpath='{.data.uri}' | base64 -d | sed 's/@[^/]*/@127.0.0.1:5432/')" go run ./hack/librarycopy --namespace clustarr-system`
   It pages each kind from the apiserver (etcd reads past the first page: this is the one time
   that is accepted), prints a line a kind, and can be run again: a row already there is left.
4. **Check the copy.** The six "read" numbers equal step 2's, and
   `select kind, seq from library.head` shows each kind's sequence at its count. Spot-check one
   object of each kind against `$K get <kind> <name> -o json` (ignore `resourceVersion`).
5. **Remove the custom resources.** They carry finalizers and no controller is running to
   remove them, so strip the finalizers, then delete the CRDs, which deletes the objects:
   for each kind, `$K get <kind> -o name | xargs -n 200 $K patch --type merge -p '{"metadata":{"finalizers":null}}'`,
   then `kubectl --context admin@talos delete crd mediafiles.catalog.clustarr.io episodes.catalog.clustarr.io albums.catalog.clustarr.io books.catalog.clustarr.io audiobooks.catalog.clustarr.io issues.catalog.clustarr.io`.
   Until this step the old release can be started again with nothing lost; after it, going back
   means restoring an etcd snapshot.
6. **Install the new release.** Apply the remaining CRDs of the new commit by hand first, as
   ever on talos; then the chart with the new images, `library.*` as decided, and cluster-plex's
   `plex.clustarr.library.secretName`. The manager creates the `library` schema's triggers and
   grants (the copy already made the tables), lists every kind from the store and takes the lease.
7. **Watch it settle.** The manager's lease and restarts; `clustarr_remediation_passes_total` by
   kind; a library page and an episode page in the UI; cluster-plex's log line that its
   MediaFile and Episode informers synced; a new grab imported end to end. Each kube-apiserver's
   `go_memstats_next_gc_bytes` should fall to roughly a third within a few minutes of step 5.

## After

- Delete `hack/librarycopy` once every install has been cut over.
- A person reads the library with the read-only role:
  `select name, body->'spec'->>'path' from library.mediafile where body->'spec'->'mediaRef'->>'name' = '<episode>'`.
  A hand repair is a spec patch or an annotation through the UI; a hand `UPDATE` moves the
  feed correctly (the trigger stamps it) but skips the store's validation and ownership
  records, so it is the last resort and never touches `managed`.


## As run on talos, 2026-11-26

Owner: "Commit and push the changes and deploy to the talos cluster when the CI completes the
build. Ensure the migations are properly run to move all existing (removed) CRs to the postgresql
DB", then "Do it" for the delete. clustarr 710ef869 (images `sha-710ef86`), cluster-plex 7148cac,
charts clustarr 0.6.18 and cluster-plex 0.6.12. Times are UTC.

| When | What |
| --- | --- |
| 10:57 | manager and ui scaled to 0 (the agents and Plex left running: they write none of the six kinds) |
| 10:58-11:03 | the copy, from a pod in the cluster: 33,814 MediaFiles, 27,693 Episodes, 6,890 Albums, 148 Books, no Audiobook or Issue; each count the apiserver's `apiserver_resource_objects` |
| 11:03 | `librarycopy --verify`: every row decodes into its Go type and none would be refused a write; six objects compared with the apiserver's, identical with their ownership records |
| 11:04 | `pg_dump -n library -Fc` to the primary's volume (`/var/lib/postgresql/data/library-cutover-20261126.dump` on clustarr-postgres-2) |
| 11:05 | HelmChartProxy patched (version and valuesTemplate); Helm rev 273 deployed 11:06; the manager took the lease 11:05:42 |
| 11:06 | cluster-plex's watch synced; its first projection applied 0 and deleted 0 |
| 11:10 | the manager's first writes through the store (status applies by `catalog-grab`) |
| 11:19 | etcd snapshot (`talosctl etcd snapshot`, 728 MB, revision 90336663) |
| 11:24-11:52 | finalizers stripped from the 34,731 stale Books, Albums and Episodes in the apiserver |
| 11:52-12:01 | the six CRDs deleted, and their objects with them |

**Where it left the steps above.**

- **The order: copy, install, check, and only then delete.** The custom resources stayed in the
  apiserver, stale, for an hour beside the running new release, so the old release could still
  have been started again. Nothing in the new binaries minds the CRDs being there.
- **The copy ran in the cluster, not through a port-forward.** `kubectl port-forward` to Postgres
  exits ("lost connection to pod") when a client closes a connection, which a copy of 68,000 rows
  does many times. A pod (`natsio/nats-box`, the manager's ServiceAccount, the app Secret's `uri`
  as `CLUSTARR_LIBRARY_DSN`, an amd64 node) with a static build of `hack/librarycopy` copied in
  by `kubectl cp` took under five minutes. The manager's role must still hold the six kinds when
  it runs, so it runs before the new chart.
- **Counting**: `apiserver_resource_objects{group="catalog.clustarr.io"}` from `/metrics`, not a
  list of each kind.
- **A second run of the copy moves a kind's sequence** by the rows it finds already there (the
  trigger runs before the insert that is then skipped): Book's head read 296 for 148 rows. The
  feed ignores an entry that names no row version, so nothing follows from it, but step 4's
  "sequence at its count" holds only for a kind copied once.
- **The ui stays at 0 through the Helm run** when its autoscaler is on (the manifest names no
  replica count): `kubectl scale deploy/clustarr-ui --replicas=1` after it.
- **Jobs owned by a MediaFile.** A running graft Job names its MediaFile as its owner, and the
  apiserver's garbage collector deletes a Job whose owner it can still look up and finds gone,
  which is what the CRD's deletion makes of it for some minutes. The two running Jobs had their
  `ownerReferences` taken off before the CRDs were deleted and put back after; both ran on. Once
  the kind has no CRD the collector cannot resolve the owner and leaves such a Job alone (checked
  on a kind cluster of the same version before relying on it).
- **Finalizers by name from the store**: `select name from library.episode` and `kubectl patch`
  by name, a hundred to a call and four calls at a time, so nothing was listed from etcd. It
  took 28 minutes at about 20 objects a second with the apiservers near their 6Gi limit.
- **The kubeconfig's current context was a kind cluster** by then (the e2e rig's): every command
  named `--context admin@talos`, and the copy pod never read a kubeconfig.

**Measured after.** Row counts unchanged an hour on and nothing marked for deletion; no manager
or agent restart; the manager's start-up pass over the library took about 25 minutes at its
750m CPU limit with no reconcile error. Each apiserver's `go_memstats_next_gc_bytes` two minutes
after the delete: 2.26, 1.64 and 2.02 GiB (4.94 GiB on the one read just before it, during the
strip). Not measured: where they settle; a Live TV channel tuning; a Movie's RSS candidate
settling under b12001fe.

**Still to do.** cluster-bootstrap's `clustarr/talos-values.yaml` (the live valuesTemplate byte
for byte) and `chart_version = "0.6.18"` are uncommitted in its working tree, and
`~/src/tfc/cluster` holds the same two, uncommitted too (cluster-bootstrap-31, 12:05Z). `hack/librarycopy` and `Store.CopyIn` go once no install is
left to cut over. No backup is configured for `clustarr-postgres`, which now holds the library.
