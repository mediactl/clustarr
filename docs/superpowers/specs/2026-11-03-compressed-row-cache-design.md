# Compressed row cache for the manager's large kinds

**Date:** 2026-11-03
**Status:** built (`pkg/k8s/rowcache`), wired into `cmd/manager` for Episode,
MediaFile, Movie and Series
**Owner's ask:** "Implement the pattern" -- a compressed in-memory informer
cache with zstd, the SQLite design as inspiration.

## 1. Problem

A controller-runtime informer cache holds every object of a kind as a decoded
Go struct, plus the store's keys and its index sets, and GOGC multiplies all
of it. Measured on talos on 2026-11-03 (rev 189, 43,949 objects of the four
large kinds), the manager's live heap held about 230 MB that way: 189 MB of
decoded Episode, MediaFile, Movie and Series objects, 37 MB of store keys and
maps, 26 MB of index sets. Their compact JSON is the same size (135 MB), so
serializing alone gains nothing. Compressed per row with a dictionary built
from the kind's own objects, the same data is 21 MB (4.9-8.3x per kind), and
compressing without one 58 MB (1.8-2.7x), because the repetition that makes a
1.6 KB Episode compressible lies across objects, not within one.

## 2. Design

`rowcache.New(Options{Kinds})` is a `cache.NewCacheFunc` for
`manager.Options.NewCache`. It wraps controller-runtime's own cache and
changes two things, both through public seams.

**The informer.** `cache.Options.NewInformer` lets the cache construct a
kind's informer. For a listed kind it builds `rowcache.Informer`, clustarr's
own `SharedIndexInformer` around client-go's reflector and DeltaFIFO
(`toolscache.New(&Config{...})`). The FIFO's transform (`rowify`) runs the
cache's own transform (managedFields stripped) and then turns the object into
a `Row`: namespace, name, the labels map, the JSON compressed as one zstd
frame, its size, and the values every registered index function gave for the
object, computed while it was still decoded. The decoded object lives only for
the watch decode. The store is client-go's `threadSafeMap` holding rows, keyed
by `rowKey`; the index functions controller-runtime registers through
`IndexField` are wrapped in adapters that read the row's precomputed values
(a row stored before its index existed is decoded once).

One writer applies deltas (`process`, under `mu`): the store is updated and
every listener gets a notification carrying rows. Listeners decode in their
own goroutines, one decode per listener per event, so a late handler's replay
of the whole store -- every controller's, since controllers start after the
cache has synced -- runs off the writer's lock and in parallel. A `Sync`
delta (resync) carries the stored row itself and writes nothing. A tombstone
stays a `DeletedFinalStateUnknown`, its object decoded.

**The reader.** The wrapping `Cache` answers `Get` and `List` for a listed
kind from the rows: a field selector goes through the index (controller-
runtime's `field:<name>` index names and `<ns>/<value>` keys, exact matches
only, `byIndexes` mirrored), a namespace through the namespace index, a label
selector against the row's labels, and only the matches are decoded, each
into a fresh object with its GroupVersionKind set as the informer cache sets
it. Every other kind is the inner cache's own Get and List.

**The dictionary.** Once a kind's initial list is in, the informer samples up
to 2,000 rows, builds a zstd dictionary from them in-process
(`zstd.BuildDict`: the samples' entropy tables, 128 KiB of them concatenated
as history, which measures within 5% of zstd's own trainer), switches the
encoder to it and compresses every row again, one at a time under the
writer's lock. Nothing is embedded in the binary and nothing persisted: a
process always decodes with the dictionaries it compressed with (a frame
names its dictionary; the decoder keeps every one). A kind with fewer than 64
rows keeps no dictionary.

## 3. Costs and limits

- Every read decodes: about 100 µs for a MediaFile, tens of milliseconds for
  a long series' episodes. The remediation loop's steady reads are a few
  milliseconds a second. A whole-collection List of a large kind is 1-1.5 s
  of decode: the two ledger rebuilds at start, the four rare re-plan sources
  (dubs, subtitles, transcode, probe) and the transcode-profile overlap memo
  pay it; the import-list apply's per-item List of every MediaFile and
  Episode of the namespace should move to the item and series indexes.
- Every watch event is encoded and compressed, about 100 µs; the 44,000-object
  initial sync is about five seconds of CPU.
- A handler's own resync period is not honoured; every handler resyncs at
  the informer's period (controller-runtime's SyncPeriod, 10 h by default).
- The store's items are `*Row`, never the kind: nothing may read
  `GetIndexer().List()` expecting objects. Only typed reads are served from
  rows; a metadata-only or unstructured read of a listed kind gets its own
  ordinary informer from the inner cache, as before.
- Only namespaced kinds; `New` refuses a cluster-scoped one.
- `cache.Options.ReaderFailOnMissingInformer` is not honoured for listed kinds
  (the manager does not set it).

## 4. Expected effect

Live heap for the four kinds: ~230 MB -> ~55-70 MB (rows 21 MB, row structs
~15 MB, keys ~6 MB, index sets 26 MB), so the manager's live heap from
295-325 MiB to about 140 MiB and its start peak from ~700 MiB to ~350-400 MiB.
Under GOGC 100 with a memory limit the RSS graph will not move (the GC is
pinned to the limit); the live heap and the limit that can be set are what
change. Metrics: `clustarr_rowcache_rows`, `_bytes`, `_object_bytes`,
`_dictionary_bytes` by kind, and `clustarr_rowcache_decodes_total` by kind and
reason (read, event, index, sample).

## 4a. As built: the start-up flood (same day, rev 194-196)

The first deploy OOM-looped the manager at 768Mi during its start, and the
1Gi pod's live heap peaked at 507 MiB. A GC-forced profile three minutes in
(593 MB in use) showed where the start-up flood lived, and none of it was
the settled rows (22 MiB for all four kinds, as designed):

- **233 MB: three watch map functions listing every MediaFile.** The
  transcode and subtitles adapters' `videoFilesLow` (one List per
  TranscodeProfile or SubtitleProfile event) and `filesWithSubtitles` (per
  local SubtitleProvider event) listed the whole kind to compute wake keys.
  Under the typed cache that was free (`UnsafeDisableDeepCopy` pointers);
  with rows each List decoded 14,600 files into a copy held for the map
  function's duration, and the three ran in parallel at start. Fixed: the
  cache answers `rowcache.Keys` and `rowcache.Metadata` from the rows
  (`KeyLister`, `MetadataLister`: the same narrowing, no decode; any other
  reader falls back to List), the adapters take `mgr.GetCache()` for them,
  and a `remediation.mediafile.kind` index (spec.mediaRef.kind) selects the
  video files so `transcodeplan.EligibleKinds` and `subtitleplan.EligibleKinds`
  never need the object.
- **About 230 MB: the reflector's watch-list temporary store.** client-go
  collects the whole initial list as decoded objects until the
  end-of-list bookmark, then hands it to the FIFO where it became rows.
  Fixed: the informer wraps the cache's lister-watcher so each watch
  event's object becomes a `rowEvent` (name, namespace, resource version,
  labels and the row, about 300 bytes) before the reflector stores it; the
  FIFO's transform unwraps it, `rowKey` keys it, and the reflector runs with
  no expected type since the stream no longer carries the kind. A plain
  list (the no-streaming fallback, a relist) still decodes the collection
  whole.
- **The replays and reads decoded copies.** Fixed: one decode per event
  shared by every handler (`decoded`, a once cell in the notification,
  alive until the last handler has dispatched it, as a shared informer
  hands one object to all handlers); List decodes straight into the list's
  Items instead of into objects copied in afterwards; the inflate buffer is
  pooled. Reads during the flood ran at 1,100 decodes a second from the
  remediation loop's passes and the controllers' Gets, which is CPU, not
  retention.
- Also seen, not from this change: about 110 MB under the NATS object-store
  watch at start (the artwork objindex listing 53,000 objects), and the
  zstd encoder and decoder states at about 30 MB for the four codecs.
- The GC headroom shrank in the same apply: the values stopped pinning
  GOMEMLIMIT, so automemlimit's 90% (691MiB of 768Mi) left 77 MiB for
  non-heap memory and GC overshoot where 640MiB had left 128.

## 4b. As built: the second round (rev 197's start, same day)

Rev 197 (the 4a fixes) started at 768Mi with a Go limit of 614MiB: rows
in at 95 s, dictionaries by 3 min, then a peak of 746 MiB RSS with a
550 MiB live heap at 3 min 20 s, 22 MiB short of the limit, and a GC
pinned to the limit afterwards (34 cycles a minute, a quarter of the CPU)
while the live heap sat at 330-420 MiB. A GC-forced profile at seven
minutes (294 MB) and a benchmark of the dictionary build (5 MB allocated,
0.5 MB retained) put the peak on the readers that run when the lease is
won and list whole kinds, plus three avoidable holdings:

- **The leader-start readers list whole kinds.** The search and season
  ledger rebuilds (every Movie, Episode, Album, Book, Audiobook and Issue;
  every Series), the MAL-Dubs source's first pass (every Series and Movie),
  the transcode-profile and subtitle-profile overlap checks (every
  MediaFile, once per profile) and the root-folder file map each decoded a
  collection and held it. Now: the ledger rebuilds list through marker
  indexes that name the few items whose dispatch is in flight
  (`itemread.SearchInFlightIndex`, `SeasonSearchInFlightIndex`); the dubs
  source reads keys through an anime marker and a MAL-id index
  (`itemread.AnimeIndex`, `MALIndex`); the overlap checks read labels
  through `rowcache.Metadata` by the media-kind index; the file maps read
  keys through `rowcache.Keys`. All of them take `mgr.GetCache()`, the
  reader that answers without a decode; any other reader falls back to
  List.
- **The index strings were held twice** (22 MB of `ns/value` and
  `__all_namespaces/value` keys per row, beside the store's own): the
  values are interned at rowify, so a row's copy and the store's map key
  are one string, and a row's own key is built once instead of per store
  operation.
- **The dictionary sweep ran under the replays**, so the pre-dictionary
  rows stayed alive from the handlers' queues beside the new ones: the
  sweep now waits for every listener's queue to drain (two minutes at
  most).
- **zstd states were 31 MB**: the fastest level (within 2% of the default
  level's ratio with a dictionary, measured), one encoder state per codec
  and two decoders instead of one per CPU.

## 4c. As built: the third round (the GOGC 20 start, same day)

The owner deployed GOGC 20 at 23:11Z (768Mi, AUTOMEMLIMIT 0.8, a 500m CPU
limit). Loki's timestamps and the minute samples put the start's phases
where they are: the watch-list stream and the dictionaries took 52 s and
the live heap went from 50 to 128 MiB with never more than 11 MiB of
garbage above it (the collector ran 57 cycles a minute); the lease took 75
s to pass from the old pod; then the controllers registered 157 event
sources in four seconds and the replays ran until the remediation loop's
workers started 2 min 46 s after the lease, at 41 to 50 MB/s of
allocation and 0.65 GC cores against the 500m limit, the container
throttled two thirds of the time. The 313 MiB peak sat inside that window,
before a single pass, and was reachable memory: a GC-forced profile of
the settled pod (320 MiB) put the relay's 64 MiB stream ring first, then
rows and row structs 54, index sets 36, the rows' retained strings 24, the
Live TV playlist cache 23, the loop's queue 15. An explicit collection at
a phase boundary frees nothing the pacer does not free within seconds.
Three changes:

- **The relay ring is off the heap** (`pkg/offheap`, which the copy
  buffers use too): an anonymous mapping the collector never scans and no
  heap goal counts, released once the ring is closed and its last viewer
  has left (`ring.Leave`), never at a cycle.
- **Late handlers share one replay** (`replay.go`). Four handlers replayed
  every Episode, two every MediaFile, six every Movie and eight every
  Series: 155,000 decodes. The first late handler opens a replay of the
  store's row pointers, the handlers registering within `replayQuiet`
  (500 ms of quiet, `replayWindow` 5 s at most) join it, delivery starts
  when the window closes, each member reads at its own cursor no more than
  `replayLead` (128) rows ahead of the slowest, and a row's object is
  dropped once every member has dispatched it, so the shared objects alive
  are never more than the lead. A member removed while blocked in its own
  callback frees the others at once. A handler registering after the
  store changed (the store's generation moved) replays on its own, since
  the snapshot would be stale for it. Every member is told of the sync
  after its last row, as before.
- **A pass reads each object once** (`app/remediation/passreader.go`). The
  planners gather on their own and several read the file's item and its
  Series, about 77 decodes a file pass; a repeat Get within the pass is
  answered from the first decode by the same object (a shallow copy of its
  struct, the slices beneath shared, as `UnsafeDisableDeepCopy` already
  shares them), a NotFound remembered, Lists passed through; the item pass
  reads the same way. `clustarr_remediation_pass_reads_total{kind,result}`
  counts the reads and the memo's answers.

Expected: the replay phase's decodes down four times on Episodes and the
loop's workers starting within a minute of the lease; the drain's
allocation down three to four times; one stream's 64 MiB out of the live
heap and the GC goal.

## 4d. As built: CBOR to the apiserver (2026-11-04)

The owner asked for client-go's `ClientsAllowCBOR` and `ClientsPreferCBOR`.
talos's apiserver (1.37) serves and accepts CBOR (`CBORServingAndStorage`
is on), and client-go 0.37 negotiates it behind the two gates. controller-
runtime 0.25, though, builds the codec factories of its client and its cache
with `serializer.NewCodecFactory(scheme)` alone, without the CBOR serializer,
and overwrites the REST config's negotiated serializer with them, so neither
gate reaches its clients: under `ClientsPreferCBOR` every lister-watcher it
builds asks for CBOR and cannot decode the answer, no kind syncs, and the
manager never becomes ready (seen live at 04:05Z and 04:24Z with the two
variables set by hand on the Deployment). A first cut gave the row cache its
own CBOR lister-watcher for the four kinds (rev 206, where the bytes are);
the owner wanted CBOR everywhere, so `go.mod` now replaces controller-runtime
with the clustarr fork (`github.com/mediactl/controller-runtime
v0.25.1-clustarr.3`, branch `clustarr`, three commits on v0.25.1) whose two
factories go through `rest.CodecFactoryForGeneratedClient`, client-go's own
helper that adds the CBOR serializer when `ClientsAllowCBOR` is on, and
whose typed client decodes an apply's answer by its content type (rev 207,
on the first commit alone, exited at its first server-side apply: client-go
sends the apply as `application/apply-patch+cbor` and the apiserver answers
in CBOR, which upstream's JSON-only read refused; the second commit's strict
direct CBOR decode then refused `metadata.managedFields`, which the apply
configurations do not declare, so the third transcodes a CBOR answer
through a generic map and the JSON path, dropping unknown fields as upstream
did), and
`cbor.go` turns both gates on at init through client-go's own gates unless
the environment names one. Every controller, informer and write in the
manager then speaks CBOR; the row cache's own stream went. Tests:
controller-runtime's own client on the fork asks for CBOR first and decodes
a CBOR answer, and JSON with PreferCBOR off; a watch answered as a CBOR
sequence decodes through the fork's factory; the gates' default.

## 5. Tests

`pkg/k8s/rowcache`: the store holds rows and every reader gets an object (a
fake controller source, the real Episode type, controller-runtime's index
shape; falsified by storing rows uncompressed, four tests fail), tombstones
and resyncs, List narrowing before decoding (the read counter moves by exactly
the rows answered), the dictionary shrinking rows in place and applying to
rows stored after it, the lifecycle, and `New`'s kind resolution.
