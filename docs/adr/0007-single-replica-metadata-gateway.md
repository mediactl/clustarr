# ADR-0007: The metadata gateway is a single replica that owns all outbound provider clients

**Status:** Accepted, 2026-09-18

## Context

Clustarr talks to a lot of third-party metadata providers: TMDB and TVDB for film and television,
MusicBrainz and Cover Art Archive for music, Open Library for books, Audnexus for audiobooks,
ComicVine for comics, MangaDex for manga. Every one of them constrains us in a way that is a
property of *the deployment*, not of a process:

- MusicBrainz asks for one request per second, from the application as a whole.
- OpenSubtitles allows on the order of twenty downloads per day on a free account.
- ComicVine's terms are non-commercial and its rate limits are enforced per API key.
- TVDB requires a subscriber PIN and has licence terms attached to the key.

A library scan generates thousands of metadata refreshes. If each replica holds its own HTTP
client and limiter, the effective rate is the limit times the replica count, and the first symptom
is a revoked API key rather than a 429.

## Decision

One metadata gateway, deployed as a single replica, owns every outbound provider client, every
credential, every rate limiter and every response cache. Other services never call a provider
directly; they go through the gateway by request/reply for interactive lookups and through a work
queue for bulk refreshes. `MetadataProvider` custom resources configure it — endpoint, credentials
reference, limits — and the gateway is the only thing that reads those secrets.

A single replica is the point, not an implementation detail: it makes the limiter a local,
in-process token bucket with no distributed coordination, and makes "are we within our quota"
answerable by looking at one process.

## Alternatives considered

**Per-service provider clients with a shared limiter in NATS KV.** Each service calls providers
directly but takes a token from a shared bucket first. Workable, and it is the documented scale-out
path, but the KV round trip is on the hot path of every request, the window arithmetic under
concurrent CAS is fiddly, and a limiter bug becomes a revoked API key rather than a slow request.
More replicas also means more connection pools against providers that count connections.

**No gateway; rely on provider-side limits and retry on 429.** Works until a provider responds to
sustained abuse by suspending the key. TVDB and ComicVine in particular treat quota as a terms
matter, not a throttling matter.

**Multiple gateway replicas sharded by provider.** Preserves one limiter per provider while
allowing horizontal scale. Rejected for now as complexity without demonstrated need; it is the
natural next step if the gateway saturates.

## Consequences

The gateway is a deliberate bottleneck, and that is the accepted trade. It is also a single point
of failure: during a restart or rollout, metadata lookups fail and callers retry with backoff
rather than fanning out to providers themselves. Metadata staleness during a short outage is
harmless; a revoked key is not.

Concentration has real benefits. Caching is shared, so a series refreshed for the catalog is not
fetched again for subtitles. Credentials live in one place, which makes RBAC on the secret
trivial. Provider quirks are implemented once, and quota usage is observable from one process.

Callers must treat metadata as an asynchronous, failable dependency — request/reply with
deadlines, bulk work on the queue — never as a blocking call inside a reconcile.

## Revisit triggers

Gateway saturation — request latency or queue depth showing it is the constraint rather than the
provider limits; a provider that genuinely requires per-user rather than per-deployment
credentials, which breaks the single-key assumption; or multi-tenancy, where quota is per tenant
and one gateway per deployment is no longer the right unit. The first step in any of those cases is
sharding by provider, with KV token buckets as the coordination mechanism already used for
subtitle providers.
