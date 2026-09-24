# ADR-0011: Artwork lives in a JetStream object store, one bucket, two writers split by variant

**Status:** Accepted, 2026-09-24

## Context

Design amendment 1 §A3.4 originally specified cover art as: "Cover art is
fetched by the metadata gateway and cached on the `/data` volume, then served
by the UI from disk." That was never built past the sentence — Phase G and
the gap fixes shipped the Library page hotlinking `status.metadata.images`
URLs straight into `<img src>`, with `referrerpolicy="no-referrer"` as the
only mitigation (2026-09-23-library-page-design.md, decision 1).

Hotlinking has three problems once ratings overlays (this plan's task C)
enter the picture. First, it leaks every viewer's IP address to whichever
CDN TMDB, TVDB, MusicBrainz or ComicVine happens to front images with, on
every page load, for as long as the item stays in the library. Second, the
art a card shows is whatever the provider currently serves at that URL, not
a version Clustarr controls — a provider reshuffling its CDN paths silently
breaks posters across the library, and there is no way to composite a
rating-badge overlay onto a URL Clustarr does not own the bytes of. Third,
`Cache-Control` and `ETag` are the provider's, not Clustarr's, so tuning how
long a browser or a CDN in front of the ui may cache a poster is not
possible at all.

The `/data` disk-cache alternative the amendment sketched has its own cost:
`ui` has never mounted `/data` and has no other reason to (amendment §A3.1:
"ui touches no media file and needs no ffmpeg/ffprobe toolchain"), so
serving from disk would give it an RWX mount, a filesystem GC problem (who
deletes a stale cached file, and when, across however many ui replicas are
reading it) and a hand-rolled digest/ETag scheme — the exact kind of shared,
mutable, multi-writer state ADR-0001 already chose JetStream over ad hoc
coordination for everywhere else in this system.

## Decision

Artwork — both the metadata gateway's fetched originals and C3's
rating-badge overlays — lives in one JetStream object-store bucket,
`clustarr-artwork` (`events.BucketArtwork`, spec §B.2), capped at
`ArtworkMaxBytes` (5 GiB). Objects are keyed by `events.ArtworkKey(kind, uid,
imageType, variant)` — `"<kind>/<uid>/<imageType>/<variant>"` — where
`variant` is `events.ArtworkVariantOriginal` or `...Overlay`. Two writers,
split cleanly by variant: the metadata gateway (`app/catalog/metadata/artwork`,
task B2) is the only writer of `original` objects, and the overlay renderer
(task C3, `k8s.ManagerCatalogarrArtwork`) is the only writer of `overlay`
objects — the same "one writer per thing" shape ADR-0004 already applies to
every CRD, carried into the object store.

`ui` gains a read-only bus connection (task B3): `cmd/clustarr`'s ui command
calls `k8s.ConnectBus` and hands `Bus.ObjectStore(events.BucketArtwork)` into
`ui.Options.Artwork` as a plain `events.ObjectStore` interface value —
`ui/` itself still never imports `pkg/k8s` (`ui/guard_test.go`'s existing
guard), and the interface's write methods (`Put`, `PutBytes`, `UpdateMeta`,
`Seal`, `AddLink`, `Purge`, alongside the pre-existing `Update`/`Delete`/
`DeleteAllOf`/`Apply`) are added to that guard's banned-selector list so a
future change reaching for the object store's write half is caught the same
way a Kubernetes write already is. `ui/art.go` serves
`GET /art/{kind}/{uid}/{type}`: the overlay object when one exists, else the
original, else 404 — never a provider's URL. Every template-facing image URL
(`projection.ArtURL`, `ui/detail.go`'s backdrop) points here instead of at
`status.metadata.images` or `status.artwork[].sourceURL` directly, so a
browser's request always lands on this ui and a provider never sees a
Clustarr viewer. The route's `ETag` and `Cache-Control` are digest-driven:
`?v=<digest>` gets `public, max-age=31536000, immutable` only when the
digest named in the URL is the one actually served, and `no-cache`
otherwise — correctness before performance, since a stale `?v` (an overlay
re-rendered since a page was cached) must revalidate, not serve a wrong
image as if it were permanent.

This does not reverse ADR-0006 ("Storage is one RWX volume at `/data` …; no
object store"). ADR-0006 governs the primary media library — the video,
audio, book and comic files a movie or episode *is* — which stays on `/data`
under TRaSH naming, hardlink-else-copy, exactly as before. Artwork is small,
wholly derived, disposable and re-fetchable from the provider at any time; it
was never a candidate for `/data`'s hardlink/rename semantics, and putting it
in JetStream's object store reuses infrastructure ADR-0001 already committed
Clustarr to, rather than adding a second storage mechanism to the primary
media path.

## Alternatives considered

**Keep the `/data` disk cache the amendment specified.** Rejected for the
reasons in Context: an RWX mount `ui` has no other reason for, hand-rolled
multi-replica cache coherency and GC, and a digest/ETag scheme JetStream's
object store already provides (`ObjectInfo.Digest` is the hex SHA-256,
computed and verified by the store itself).

**Keep hotlinking provider URLs, only drop `referrerpolicy`.** Rejected:
`referrerpolicy="no-referrer"` addresses the request header, not the
request itself — the provider's CDN still sees every viewer's IP on every
page load, still owns the caching headers, and still owns whether the URL
resolves at all next month. It also cannot serve a rendered overlay, since
there are no bytes to composite onto.

**A general S3-compatible object store (e.g. MinIO) as a new dependency.**
Rejected: adds a new stateful service, a new Go client dependency and a
second storage system to operate, for a feature (content-addressed blob
storage with digests and headers) JetStream's object store already provides
and that `pkg/events`' bus abstraction already wraps consistently with every
other piece of shared state in Clustarr.

## Consequences

`ui` now holds a NATS connection it never had before, but it stays
read-only by construction: `ui/art.go`'s `handleArt` only ever calls `Get`,
and the AST guard now enforces that syntactically across the whole package.
Kubernetes RBAC is unaffected — this is a NATS-side capability, not a
`ui_role.yaml` grant, so the ui role gains no new verbs (spec §B.8).

A nil `ui.Options.Artwork` (no NATS endpoint reachable — a developer running
`clustarr ui` standalone, or a NATS outage) is legal and degrades cleanly:
`/art` answers 404 for every request, the same way a nil `Options.Reader`
already degrades the rest of the page to empty rows rather than failing the
process.

**SSRF surface, carried over from the B2 review.** `spec.artwork` lets an
operator override an image with an arbitrary URL, and the metadata gateway
fetches it from inside the cluster with no private-address deny-list today —
only a response-shape check (a 200 with an image Content-Type that decodes)
and redaction of query strings and userinfo in logs and Events. This is
acceptable while `spec.artwork` is reachable only the way any other spec
field is — `kubectl`/direct API-server access — because that caller already
has whatever cluster access an SSRF through it could reach. It stops being
acceptable the moment a less-trusted caller can set the field, which is why
`ui/actions` does not expose it and must not gain a write path to it without
first adding the deny-list.

The bucket is capped (`ArtworkMaxBytes`, 5 GiB); the reaper (task B2,
`app/catalog/metadata/artwork/reaper.go`) deletes objects no catalog item's
`status.artwork`/`status.overlay` references any more, so the cap is a
backstop against a reaper bug, not the primary control.

## Revisit triggers

`ui/actions` (or any other lower-trust caller) gaining a write path to
`spec.artwork`, at which point the SSRF surface above must gain a
private-address deny-list before that path ships. The artwork bucket
approaching `ArtworkMaxBytes` in a real deployment, which would mean either
the reaper has a gap or the cap needs raising. A second UI-like consumer
needing these same images without a NATS connection of its own, which would
argue for `ui/art.go`'s route (or an equivalent) becoming a shared library
rather than duplicated per consumer.
