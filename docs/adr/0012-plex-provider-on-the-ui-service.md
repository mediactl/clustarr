# ADR-0012: The Plex Metadata Provider is served read-only by the ui service, unauthenticated per protocol, and must not be publicly exposed

**Status:** Accepted, 2026-09-24

## Context

Plex Media Server (PMS ≥ 1.43.0) introduced Custom Metadata Providers: an
HTTP protocol a movie or TV library agent can point at instead of Plex's own
TMDB/TVDB agents (`docs/research/plex-metadata-provider.md`, verified
against Plex's own example provider and its published docs). A running PMS
that already trusts Clustarr's catalog decisions — quality, custom formats,
which release was grabbed — can source its *display* metadata (title,
artwork, ratings) from the same catalog instead of duplicating a second
provider's opinion of what a Movie or Series is.

Three facts about the protocol itself, all from the research note and none
negotiable, shape every placement option:

1. **Only unauthenticated requests are currently supported.** Plex states
   this plainly, with a note that public-facing providers should wait for a
   future version. There is no token, header or query parameter this
   protocol will ever check.
2. Only movie and TV libraries exist today; the protocol carries no way to
   describe subtitle or audio streams, so captionarr's sidecars keep
   reaching Plex through the filesystem exactly as before (unaffected by
   this ADR).
3. Every image URL (`thumb`, `art`, `Image[].url`) must be an absolute,
   publicly-reachable-to-PMS URL: "A publicly accessible URL to the
   default poster/thumbnail". The provider therefore needs to know its own
   externally reachable address, the same problem the TMDB example provider
   solves with a `BASE_URL` setting.

Where to run this server was the open question. Three services already read
the catalog read-only in this codebase's shape: `ui` (an informer-backed
cache over the same CRDs), the metadata gateway inside `catalogarr` (a
controller process, not a stateless reader) and nothing else — every other
service either writes to the catalog or has no read path to it at all.

## Decision

The Plex provider is mounted on the **existing `ui` service**, as two more
routes alongside the pages `ui` already serves: `GET /plex/movies` (type 1,
`identifier: tv.plex.agents.custom.clustarr.movies`) and `GET /plex/tv`
(types 2/3/4, `identifier: tv.plex.agents.custom.clustarr.tv`), per Plex's
own one-parent-type-per-provider recommendation. It reads the same
`ui/projection.Index` the Library page already builds from `Options.Reader`
— no new cache, no new watch, no new dependency on the cluster.

It is gated by two flags on the `ui` command, `--plex-provider` (bool,
default true — the routes exist either way) and `--external-url` (env
`CLUSTARR_EXTERNAL_URL`), the absolute base every `thumb`/`art`/`Image[]`
URL is built on (spec §D.1). With `--plex-provider` on and no
`--external-url`, both roots answer `503` with a body naming the flag,
logged once at startup: no thumb/art/`Image[].url` this provider could ever
hand back would resolve to anything PMS could fetch without it, so refusing
at the root is enough to keep every other route from ever being reached
with an unusable base. Readiness is unaffected — a missing `--external-url`
is a configuration gap for the Plex integration specifically, not evidence
`ui`'s cache has failed to sync.

`ui/plex` (task D1) holds no `client.Client` and no `*actions.Actions`; it
is built from an `IndexFunc` closure alone, so `ui/guard_test.go`'s existing
AST ban on a write selector or a `pkg/k8s` import anywhere under `ui/`
covers this package for free, with no new exception carved into that guard.
This is the same shape `ui/art.go` (ADR-0011) already established for
serving artwork read-only from `ui`: extend the one service that is
structurally incapable of writing, rather than build a second one.

**It must never sit on a public ingress**, and this is stated three times on
purpose — `charts/clustarr/README.md`'s "Plex provider" section, the
`values.yaml` comment beside `ui.plex.externalURL` and the ingress note it
already carries for `ui.auth.mode`, and `docs/observability.md`'s Exposure
section — because unlike the rest of `ui` (which at least has
`--auth-mode` as a named, if currently single-valued, knob operators are
told to gate with ingress authentication) there is no way to gate this path
at all. Anyone who can reach `ui.plex.externalURL` reads the whole catalog.

## Alternatives considered

**A new, separate `clustarr plex` service/binary role**, its own Deployment
and Service. Rejected: it would need its own read-only cache
(`ui.NewClusterReader`'s standalone `cache.Cache`, no manager, already
solves this once) duplicated for no benefit, a new RBAC role
(`config/rbac`) granting exactly the same `get/list/watch` verbs `ui`
already has, a new chart component, and a second thing to explain in the
exposure story above instead of one. The protocol's own "unauthenticated,
whole catalog" shape does not get safer by moving to a different pod; it
only gets a second attack surface with the identical blast radius.

**Mount it on `catalogarr`** (or another controller-runtime service),
reusing the metadata gateway's own already-resolved `status.metadata`.
Rejected: every controller service in this codebase writes status somewhere
(`app/catalog/controller/*`, the metadata gateway's own work-queue handler),
and CLAUDE.md's invariant — one controller-writer per resource, enforced by
field managers and `pkg/k8s.PatchStatus` — is a property of *services*, not
individually of handlers within one; adding a read-only HTTP surface to a
process that also holds a controller-runtime `Client.Writer` is one accident
away from that surface reaching for it, with no structural guard the way
`ui/guard_test.go` gives `ui/`. It would also mean `catalogarr`'s pod, whose
Service today serves only `/metrics`, `/healthz` and `/readyz` behind
cluster-only RBAC-gated scraping, would need a second, differently-exposed
port and Service for unauthenticated external-ish traffic — exactly the
`ui`-shaped problem `ui` already exists to hold.

**Require authentication anyway, ignoring that Plex will not send it.**
Rejected outright: the research note is unambiguous that PMS sends no
credential of any kind today, so any auth requirement here would simply
mean PMS can never use the provider, not that the provider is protected.

## Consequences

`ui` remains the one service in Clustarr that answers unauthenticated
external traffic by design, and now does so on two independent surfaces
with two different trust models on one process: the web UI (anonymous
today, ingress-authenticated per operator instruction) and the Plex
provider (unauthenticated forever, by protocol, never to be ingress-exposed
at all). An operator who reflexively ingresses "the `ui` Service" without
reading the README exposes the catalog to the Internet; there is no code-
level guard against this, since Kubernetes ingress is configured entirely
outside anything Clustarr renders by default — the chart ships no `Ingress`
resource for `ui` at all, and the exposure notes are the only defence.

`ui.plex.enabled`/`ui.plex.externalURL` (chart) and
`--plex-provider`/`--external-url` (kustomize, `config/manager/ui.yaml`,
matching the binary's own flag names for parity) both default to the
provider being *reachable* (routes mounted) but *inert* (503, no external
URL) — the safer failure mode than defaulting to a guessed hostname that
happens to be wrong, or silently serving relative URLs PMS could never
resolve.

The metadata this provider can fill is bounded by what `status.metadata`
already carries (research §11's field table): `SeriesMetadata` has no
first-aired date field yet, so a show's `originallyAvailableAt` falls back
to its earliest episode's air date; there is no cast, crew, tagline or
theme song anywhere in the catalog, so those Plex fields are always omitted,
never blanked (spec §D.5) — a gap in the catalog's own metadata, not a
provider limitation this ADR can fix by itself.

## Revisit triggers

Plex shipping authentication for custom providers (the announcement says
"currently" unauthenticated), at which point this provider should require
it and the exposure warnings above should be revisited rather than assumed
permanent. `ui` gaining a write path of its own (any change to
`ui/guard_test.go`'s ban), which would mean this ADR's "structurally
incapable of writing" argument for keeping the provider on `ui` no longer
holds and the alternative of a separate read-only service should be
reconsidered. Music or book libraries becoming a supported provider type in
Plex's protocol, which would extend `ui/plex` past the catalog kinds it
reads today. Phase H's real-PMS run settling the 0- vs 1-based paging
question research §3 and spec §D.2 both mark unverified.
