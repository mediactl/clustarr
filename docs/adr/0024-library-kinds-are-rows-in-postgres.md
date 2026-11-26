# ADR-0024: A kind a controller creates is a row in Postgres; a kind a person declares stays a custom resource

**Status:** Accepted, 2026-11-26 (owner: "Could we re-design clustarr to use significantly
less memory in the api server?", then "Yes" to losing `kubectl` for the kinds that move and
to Postgres as a required dependency, then the three rulings in the design's §6: copy at
each cutover, share the existing `clustarr-postgres` Cluster apart from `plex-db`, and move
Book, Audiobook and Issue too). Design:
`docs/superpowers/specs/2026-11-26-library-store-design.md`. Built on main the same day for
all six kinds (the design's §9 is the record); not deployed, and not yet proven on kind.

## Context

The design of record made every unit a custom resource and judged the object count "well
within etcd for a homelab" (its opening table of decisions, "Per-file state as `MediaFile`
CR"), with a revisit trigger at
50,000 media objects (§18). ADR-0016 folded three per-file kinds into MediaFile status and
ADR-0019 made the manager the only writer; neither changed where the objects live.

The trigger has fired. Measured on talos on 2026-11-26: 33,839 MediaFiles, 27,750 Episodes
and 6,890 Albums, about 72,000 clustarr objects in all. Each of the three kube-apiservers
holds every one decoded as generic maps, about 4.7 times its stored size: about 1.7 GiB of a
2.1-2.4 GiB live heap, resident at 4.7-5.2 GiB, on control planes whose apiserver limit was
raised from 4Gi to 6Gi that day after three OOM kills in one night. More than half of the
1.7 GiB is `metadata.managedFields`. The sizes come from decoding a copy of the live objects
locally, because talos runs its apiservers without profiling.

The writes cost as much. A server-side apply that changes nothing still runs in full: the
album refresh wave of 2026-11-26 held one control plane at 3.8 of 4 cores, and a rename loop
wrote one MediaFile 29 times a second for eight days from 2026-11-18. The manager, each ui
pod, four agent domains and cluster-plex each hold or read their own copy of the same objects
through the same apiservers; the manager already keeps its copy as compressed rows
(`pkg/k8s/rowcache`, 2026-11-03) because the decoded one did not fit.

MediaFile, Episode and Album are 68,479 of the objects and about 1.55 of the 1.7 GiB. The
kinds a person declares are a few thousand objects and under 0.2 GiB.

## Decision

**A kind a controller creates is a row in the library store; a kind a person declares stays
a custom resource.** The library kinds are MediaFile, Episode, Album, Book, Audiobook and
Issue. Series, Movie, Artist, Author, Comic and every settings and Live TV kind are unchanged.

**The library store is one Postgres database, required by every install**, in the
CloudNativePG Cluster the chart already renders for the release index (now always rendered),
never in `plex-db`. `pkg/library` owns its schema and every statement.

**The storage moves and the model does not.** A library object keeps its Go type, name, spec,
status, finalizers and owner. The manager's cache is fed from the store's list and change
feed where it was fed from the apiserver's; a client wrapper serves the six kinds from the
store and passes every other kind through; `pkg/k8s.PatchStatus` stays the one entry for
status writes.

**The store keeps server-side apply's rules without its records.** Each section of an object
is a set of writer documents, one per field manager. An apply replaces the writer's document
and takes the leaves it sets from every other; the object is their union. A version column
is the `resourceVersion`, and a precondition on it is a conflict where it is one today. A
write that changes nothing writes nothing.

**The manager stays the only writer of status**, now in both stores. The ui may update a
library object's spec, labels and annotations and nothing else, enforced by a column grant.
Agents and cluster-plex read with a read-only role.

**`kubectl` no longer reaches the library kinds.** The UI, read-only SQL views and a
`library` subcommand of the manager binary replace it.

**Each kind moves in its own wave, cold**, after a wave that proves the mechanisms the rest
depends on (design §4): the change feed never skips, the store merges writes as the
apiserver does, and the cache serves a kind that has no CRD.

## Alternatives considered

- **Keep the custom resources and trim them.** Marking single-writer status structures
  atomic shrinks the ownership records, and moving cached provider documents out of status
  shrinks the rest: about a third off by estimate (1.7 to roughly 1.1 GiB). It is days of
  work, not weeks. Rejected as the answer because the cost still follows the library and no
  write traffic leaves the apiserver. It remains open for Movie and Series.
- **Fewer, larger objects** (episodes inside their season or series). The cost is bytes, not
  object count; a long series passes the object size limit, and every episode change would
  rewrite the whole object.
- **An aggregated API server over Postgres.** It would keep `kubectl`, RBAC and client-go
  watches. Rejected: built on the apiserver library it brings the watch cache and the
  ownership records back into a process of ours, and without that library it is a large
  surface to reimplement for a view the UI already gives. A read-only one can be added later.
- **A second etcd-shaped store behind the apiserver** (per-resource storage overrides). The
  apiserver's memory is its own cache of the objects, so it would not fall.
- **NATS key-value buckets**, which the bus already provides. Rejected: no queries, so every
  reader that filters would scan or mirror whole buckets, and whole-store scans have already
  caused NATS memory spikes on talos (2026-10-27, 2026-11-17).
- **SQLite on a volume in the manager pod** (as ADR-0003 chose for the release index). The
  ui, four agent domains and cluster-plex read these kinds from other pods; the manager would
  become their database server.
- **Raise the limit and stop.** The limit is 6Gi and holds today. Rejected as the answer
  because the library grows and three control planes with four cores each pay it three times.

## Consequences

- Apiserver live heap falls to an estimated 0.6-0.7 GiB each once all six kinds have moved,
  and no library read or write passes through the apiserver or etcd.
- Postgres becomes a hard dependency of every install, where it was an option of the release
  index (ADR-0010). An etcd snapshot no longer holds the library; the database needs its own
  backup, and a Cluster with standbys needs a synchronous one, which the release index did
  not.
- "Custom resources are the interface" narrows to the kinds a person declares. `kubectl get
  mediafiles,episodes,albums` and every recipe that patches or annotates one go away.
- The parent of a library object lives in a different store. Garbage collection by owner
  reference becomes the parent's finalizer and a sweep.
- The tables and the change feed are a contract with cluster-plex, which deploys with each
  cutover.
- Envtest alone no longer proves a library kind; its suites run an embedded Postgres beside
  it.
- ADR-0016 and ADR-0019 keep their decisions (one remediation loop writes per-file work; the
  manager is the only writer; agents write nothing) with "status" and "CR" read as the
  store's for these six kinds. When the first wave lands, that is recorded as a refinement
  under the index and in the spec's §19.
- `remediation.ApplyMemo` (2026-11-26) and the row cache's apiserver path become unnecessary
  for the library kinds.

## Revisit triggers

- Wave 0 cannot make the store merge writes as the apiserver does, or cannot serve a kind
  without a CRD through the cache: the reconcilers would have to be rewritten, and the choice
  between this and trimming the custom resources is made again.
- `kubectl` access to the library kinds is missed in practice: add the read-only aggregated
  API over the store.
- Kubernetes stores custom resources in a compact form in the watch cache, or stops holding
  ownership records decoded: the premise weakens, though the write cost remains.
- The library database becomes the busiest thing clustarr runs: revisit one instance as the
  default.
