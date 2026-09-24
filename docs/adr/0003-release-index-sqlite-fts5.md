# ADR-0003: The release index is SQLite FTS5 on an RWO volume, behind a Store interface

**Status:** Superseded by ADR-0010, 2026-09-24

## Context

indexarr aggregates many remote indexers into one searchable corpus. Releases arrive from RSS
sync every 15 minutes and from scatter/gather searches, are published to `CLUSTARR_RELEASES`, and
must then be queryable: full-text over release titles, filtered by category, indexer, age, size
and seeders, ranked, and paged. Homelab volume is tens of thousands of rows with a retention
window measured in days — not a search cluster's worth of data, but far more than can be scanned
linearly per query.

The index is a cache, not a system of record. Custom resources hold desired state; the releases
themselves are reproducible by re-querying indexers. Losing the index costs a re-sync, nothing
more. That framing is what makes a single-writer, single-volume design acceptable.

## Decision

A SQLite database with an FTS5 virtual table over release titles, using the `modernc.org/sqlite`
pure-Go driver, on an RWO PersistentVolume mounted by the single indexarr replica. All access goes
through a `Store` interface — `Upsert`, `Search`, `Prune`, `Stats` — so the engine is replaceable
without touching callers.

indexarr is a single writer by construction (see ADR-0004's one-writer principle applied to this
component). That is what allows SQLite to be used without WAL contention across pods, and it is
enforced by deployment shape, not by convention.

## Alternatives considered

**NATS KV plus bleve, index rebuilt per pod.** Every replica maintains its own bleve index from
the KV stream. Tempting because it removes the volume entirely, but bleve's in-memory structures
are unbounded in practice for our corpus: memory grows with the index and there is no eviction
knob that maps to "keep 7 days". Two judges independently flagged the RAM profile as the failure
mode, and a search backend that OOMs during a library scan is worse than one that is briefly
unavailable.

**Single bleve index on a volume.** Same RAM behaviour, and adds an index format we would have to
migrate ourselves, with no query language anyone else can debug.

**cgo SQLite (`mattn/go-sqlite3`).** Faster, but drags cgo into the indexarr image, which is the
one service that currently builds as a distroless static binary. The pure-Go driver keeps that
property; the performance difference is irrelevant at our row counts.

**Postgres full-text search from the start.** Correct for multi-replica, but requires an operator
and a database for a cache that is rebuildable in minutes. Deferred rather than rejected — it is
the documented scale-out path, and the reason the `Store` interface exists.

**Elasticsearch / OpenSearch.** Out of scope on footprint alone for a homelab target.

## Consequences

The index is a single point of unavailability: while indexarr restarts, search returns errors and
catalogarr retries with backoff rather than failing a grab. That is an accepted, documented
degradation and is the main reason searches are request/reply with deadlines rather than fire-and-
forget.

indexarr cannot be scaled horizontally. Query throughput is bounded by one process and one
volume. FTS5 tokenisation is not release-title-aware out of the box, so title normalisation
happens before insert, and changing that normalisation means rebuilding the index — cheap,
because it is a cache.

Backups are unnecessary. The volume can be deleted and the index rebuilt from indexer RSS.

## Revisit triggers

Search latency at p95 exceeds a second at steady-state corpus size; the index no longer fits
comfortably on a single node's storage; or indexarr restart windows become disruptive enough that
multiple replicas are required. Any of those means implementing the Postgres FTS `Store` and
running indexarr as a scaled Deployment; no caller changes.
