# Cold cutover: the library kinds from custom resources to the library store

**Status:** written 2026-11-26; nothing here has run. Every step that writes to a cluster needs
the owner's explicit OK, given for this cutover. Nothing in this document is automated.

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
