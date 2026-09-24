# ADR-0010: The release index engine is selectable -- SQLite FTS5 by default, Postgres FTS via CloudNativePG for multi-replica indexarr

**Status:** Accepted, 2026-09-24

## Context

ADR-0003 named its own revisit triggers: search latency growing past a second at steady state, the
index outgrowing a single node's storage, or indexarr's restart windows becoming disruptive enough
that multiple replicas are required. It also named the way out in the same breath -- "implementing
the Postgres FTS `Store` and running indexarr as a scaled Deployment; no caller changes" -- because
`pkg/relindex.Store` (`Upsert`, `Search`, `Prune`, `Stats`) was written as the seam that would let
the engine change without touching `app/indexer`'s controllers, the RSS worker or the search
fan-out.

Milestone M7's index/artwork/ratings/Plex design (2026-09-24, §A) is the first caller of that
trigger, ahead of any of ADR-0003's named symptoms actually firing: the owner asked for a
Postgres-backed option so a homelab that already runs Postgres for other services (or wants
indexarr to survive a pod restart without a search outage) can choose it, and so indexarr is not
permanently pinned to one replica by its storage engine.

## Decision

`pkg/relindex` gains a second `Store` implementation, `OpenPostgres(ctx, dsn)`, over
`github.com/jackc/pgx/v5` (`pgx/v5/stdlib` + `database/sql`), schema and semantics equivalent to
the SQLite store field for field (design §A.1) -- a generated `tsvector` column with a GIN index in
place of FTS5's virtual table, `plainto_tsquery` in place of the hand-rolled `matchExpr` sanitiser
FTS5's raw MATCH syntax needed. Both engines run one shared contract suite
(`pkg/relindex/storetest`, design §A.2) so neither can drift from the other's documented behaviour.

A new flag, `--index-dsn` (env `CLUSTARR_INDEX_DSN`), selects the engine at startup: non-empty
opens Postgres and ignores `--index-path` (with a log line saying so); empty keeps SQLite at
`--index-path`, unchanged from ADR-0003, including the RWO PVC and the single Recreate replica.
This is a pivot, not a migration path -- there is no online move from one engine to the other, only
a choice made (or changed) at deploy time, because the index is a rebuildable cache (ADR-0003's own
framing: "losing the index costs a re-sync, nothing more").

Selecting Postgres also, unconditionally, turns on leader election for every indexarr controller
and for the retention sweep runnable (ruling R1): under SQLite this stays a no-op, because §3 pins
indexarr to one replica regardless; under Postgres more than one replica may now run, and every
writer that would otherwise double up -- the reconcilers, the sweep -- must become a cluster
singleton the moment that is possible, not only when an operator remembers to pass a flag.

The chart and kustomize both ship an *optional* CloudNativePG `Cluster` (`postgres.enabled` /
`config/postgres`, design §A.4) that provisions a Postgres database for this to point at, but
`--index-dsn` itself has no opinion about where the DSN's target runs -- any reachable Postgres
DSN with `pg_trgm`-free `tsvector`/`tsquery` support works, CNPG or otherwise.

## Alternatives considered

**Keep SQLite the only engine and wait for a revisit trigger to actually fire.** Consistent with
ADR-0003's own stance ("deferred rather than rejected"), but the owner asked for it now, ahead of
the triggers, so this is available rather than mandatory.

**Migrate the index in place (SQLite to Postgres) rather than a from-empty pivot.** Rejected on the
same grounds ADR-0003 gives for skipping backups: the index is fully reproducible from indexer RSS
and searches, so a migration tool would spend real engineering effort moving data that costs
nothing to regenerate. A `--index-dsn` switch just starts empty and re-syncs.

**Drop SQLite once Postgres exists, to avoid maintaining two engines.** Rejected: SQLite requires no
operator, no second database to run and back up, and stays the right default for a single-replica
homelab install, which is still the common case. `Store` existing as an interface was exactly the
point of ADR-0003's deferral -- maintaining two thin implementations behind one contract suite is
cheap, and removing the zero-dependency default is not.

**Let indexarr auto-detect and always run leader-elected, DSN or not.** Rejected: leader election
adds nothing when there is exactly one replica to elect (ADR-0003's shape) and would make the
restart of that one replica wait out a lease for no benefit. Keying it on the DSN keeps the SQLite
path exactly as cheap as ADR-0003 left it.

## Consequences

The `Store` interface's payoff from ADR-0003 is realised: no caller outside `pkg/relindex` and the
two `Open*` constructors changed. `indexarr.replicas` is pinned to `1` by `clustarr.validate`
(chart) and by convention (kustomize) only while `postgres.enabled`/the Postgres DSN is unset;
Postgres mode lifts that pin.

A Postgres-backed indexarr now depends on an operator (CloudNativePG, if the bundled optional
dependency is used) and a running database, which is more to operate than a single PVC -- an
explicit trade a homelab install does not have to make, since the default stays SQLite.

Two engines mean two sets of index-specific behaviour to keep equivalent: title normalisation,
category matching, pruning cutoffs. `storetest.Run` is the guard that keeps them from silently
diverging; a behavioural difference between engines is a storetest gap until proven otherwise.

The chart and kustomize each gained one more moving part (a conditional dependency and a Helm hook;
a Component and a strategic-merge patch, respectively) to keep in step, which is what
`TestChartAndKustomizeAgreePerComponent`'s postgres-enabled case exists to hold together.

## Revisit triggers

The Postgres store's query plan or index choice needs revisiting once real corpus sizes are
observed running against it (nothing here changes SQLite's own, ADR-0003-documented boundaries).
Removing SQLite as the default is not on the table absent a reason a homelab install would notice --
an extra operator dependency is a cost, not a convenience, for the common single-replica case.
