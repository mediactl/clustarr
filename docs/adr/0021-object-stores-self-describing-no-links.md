# ADR-0021: Object stores hold self-describing objects under deterministic names, never links

**Status:** Accepted, 2026-10-07. Most of it is already built. The remainder (fingerprint
metadata and two guards) is implemented with ADR-0020's Wave N, after the current plan's
implementation waves and before the test batch.

## Context

Clustarr keeps two JetStream object stores: `clustarr-artwork` (ADR-0011: originals, overlays
and overrides) and `clustarr-fingerprints` (segment detection's audio fingerprints). Whether
to use object links was settled as owner decision (a) of the 2026-09-24 design
(`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md:326-327`), and again on
2026-10-07: "names + metadata, no object links". It has never had an ADR of its own.

An external review (2026-10-07) argued the same position and listed metadata optimisations.
Each point was checked against the branch and nats.go v1.53.1:

| Review point | Verdict | Evidence |
|---|---|---|
| Links cost two lookups: the link's info, then the target's | **Confirmed** | nats.go `jetstream/object.go:861` (`Get` on a link resolves `Opts.Link` and gets the target) |
| A link sees no watch event when its target changes | **Confirmed** (the update is published on the target's meta subject only) | nats.go object store design |
| A link dangles after its target is deleted | **Confirmed**, and nats.go refuses to *create* a link to a deleted object (`ErrNoLinkToDeleted`, `object.go:1013-1014`) | |
| `AddLink` refuses a name that ever held a regular object | **Confirmed**: it reads the name's info with `GetObjectInfoShowDeleted` and returns `ErrObjectAlreadyExists` unless that is a link (`object.go:1023-1026`) | |
| Keys are deterministic, one hop | **Confirmed**: `events.ArtworkKey(kind, uid, imageType, variant)`; fingerprints `<probeHash>.<start\|end>[.v<FingerprintVersion>]` (`events.FingerprintKeyScheme`, `pkg/segments.FingerprintKey`) | `pkg/events/subjects.go:247` |
| A guard `TestNoObjectLinks` bans `AddLink`/`AddBucketLink` | **Designed, not yet written**: specified by the split design (`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md:2686`), deferred with the Wave 4f tests to the batch. Today `events.ObjectStore` simply exposes no link method, and `ui/guard_test.go`'s banned selectors include `AddLink` | |
| A: 304s answered from RAM through `objindex` | **Already built** (Wave 4f): `/art` sets the ETag from the index entry's digest and answers `If-None-Match` with no store call (`ui/art.go:86-125`); bytes come from a digest-keyed cache, and chunks are read only on a miss | |
| B: self-describing objects | **Already built for artwork.** The proposed keys are exactly `events.ArtworkMetaKey*` (`subjects.go:225-236`): meta-version, kind, uid, namespace, name, image-type, variant, language, width, height, profile, original-digest. They come from one builder, `app/catalog/artwork.ObjectMeta` (`meta.go:90-121`), which also records the overlay's inputs digest. The reaper's audit reads `meta-version` (`audit.go:277`). ADR-0019 D13 derives `status.artwork` and `status.overlay` from this metadata through a manager-side `objindex` (wave A5) | |
| C: `SetMeta` updates without re-uploading chunks, and a `Put` replaces all metadata | **Already built, and the gotcha is wider than stated.** `UpdateMeta` *also* replaces `Headers` and `Metadata` wholesale (`object.go:1224-1225`), so a partial map on `SetMeta` loses keys exactly as a partial `Put` would. Both callers (`fetcher.go:339-343`, `handler.go:600-608`) build the complete map through `artwork.ObjectMeta` | |
| D: fingerprints carry no metadata | **Confirmed**: `app/segments/worker/cache.go:59` puts `events.ObjectMeta{}`. Its premise is weaker than stated: the analyzer's version is already in the key (`.v<FingerprintVersion>`, W7.5), so a parameter change makes a new key, not a mismatched read | |

The review's latencies (about 78 µs for a metadata direct get, about 2.1 ms for a full `Get`
with its ephemeral ordered consumer) are its own measurements. They were not reproduced here, but
they match the mechanism: `Info` is one direct get of the rolled-up meta message, while `Get`
creates a consumer over the chunk subjects.

## Decision

1. **No object links, ever.**
   - `events.ObjectStore` exposes no link method.
   - `TestNoObjectLinks` (an AST guard over every non-generated Go file) bans the `AddLink`
     and `AddBucketLink` selectors, and any use of nats.go's `jetstream.ObjectStore` outside
     `pkg/events/natsbus`.
   - An alias or a shared image is a deterministic name the writer computes. Shared images are
     written once per name, since the stores are deduplicated by digest at the reader's cache,
     not by links.

2. **Every object is self-describing, through one builder per store.**
   - **Each store's builder:** artwork's is `app/catalog/artwork.ObjectMeta`. Fingerprints gain
     `pkg/segments.FingerprintMeta`.
   - **Keys for fingerprints:** `clustarr.io/meta-version`, `clustarr.io/kind: fingerprint`,
     `clustarr.io/probe-hash`, `clustarr.io/window: start|end`,
     `clustarr.io/fingerprint-version`, the analyzer's parameters (window seconds, sample rate,
     channels) and `clustarr.io/written-by` (binary and version).
   - **Shared keys** are named in `pkg/events` beside the artwork keys, and every store's bucket
     metadata names its key scheme (`clustarr.io/key-scheme`, already set on both stores).

3. **A write sends the complete metadata, on `Put` and on `SetMeta` alike.**
   - Both replace headers and metadata wholesale, so neither takes a partial map.
   - Outside `pkg/events` and tests, an `events.ObjectMeta{...}` literal may appear only inside a
     store's builder (guard `TestObjectMetadataComesFromItsStoresBuilder`).
   - Raising a store's `meta-version` is how a scheme change is rolled out. The store's audit
     (artwork's reaper; the manager's maintenance loop for fingerprints, ADR-0020 N5) backfills
     older objects with `SetMeta`, never by re-uploading chunks.

4. **Readers decide from metadata and read chunks only for bytes.**
   - An index (`objindex`) or `Info` answers existence, freshness, dimensions, variant
     selection, staleness (`original-digest` against the original's digest) and audits.
   - `Get` runs only when the payload itself is served or processed.
   - The fingerprint cache checks `fingerprint-version` and the parameters from `Info` before
     `Get`, and recomputes on a mismatch. This guards a `FingerprintVersion` change made
     without the key suffix.

## Alternatives considered

**Links for stable "served" names** (one name pointing at the overlay or the original): this
costs two lookups per read, emits no watch event under the link's name, and can neither
replace a name once used for an object nor point at a deleted one. The ui already picks the
variant from the index in memory (`chooseArt`), which costs nothing per request.

**Metadata in a KV bucket beside the store:** a second write per object with no atomicity
against the chunks, and a second watch. The object's own meta message already is a KV-like
record with a watch.

**No metadata for fingerprints**, since the version is in the key: cheap today. But it leaves
the maintenance purge (ADR-0020) and any audit unable to tell what an object is without
parsing its name, and it leaves nothing to check when a parameter changes without a version
bump.

## Consequences

- Nothing about artwork changes: this ADR records what Wave 4f and ADR-0019 D13 built and
  planned.
- Fingerprint objects gain about 300 bytes of metadata each. A fingerprint written before
  this change reads as `meta-version` absent and is backfilled by the maintenance audit, or
  simply expires under the store's 90-day `MaxAge`.
- Two guards hold the rules: `TestNoObjectLinks` and
  `TestObjectMetadataComesFromItsStoresBuilder`.

## Implementation

Joins ADR-0020's Wave N:

| Task | Content |
|---|---|
| N7 | `pkg/segments.FingerprintMeta` and its keys in `pkg/events`; `app/segments/worker/cache.go` puts through it and checks it from `Info` before `Get`; the maintenance audit (N5) backfills absent `meta-version` with `SetMeta` |
| N8 | The guards: `TestNoObjectLinks` (with the `jetstream.ObjectStore` confinement), `TestObjectMetadataComesFromItsStoresBuilder`, `TestSetMetaSendsTheCompleteMap` (contract: a `SetMeta` with a partial map loses the omitted keys on both buses, documenting the wholesale replace) |

## Revisit triggers

- nats.go or nats-server gains server-side link resolution with watch propagation.
- An object store holds data whose owner the metadata cannot name (a content-addressed object
  shared by several owners would need a reference count, which neither links nor metadata
  provide).
