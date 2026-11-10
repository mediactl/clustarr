# Backlog search

**Status: design of 2026-11-10** (owner: "we need our own version of this:
Scheduled backlog searches for Radarr, Sonarr, Lidarr, Readarr and
Whisparr. Works through missing and cutoff-unmet items in small
rate-limited batches with per-item cooldowns and hourly caps, to avoid
overloading indexers" -- Huntarr's description).

## The gap

An item gets one live search, at its first automatic search. Every search
after that is `IndexOnly` (P12, 2026-10-06): the index agent answers it
from the local release index, which holds only what the indexers' RSS feeds
delivered since the index started. A missing movie whose only release was
posted before then, or an upgrade that appeared in a feed while the index
was down, is never found again unless a person presses "Search now". The
wanted sweep keeps the backoff (6 h doubling to 7 days) but never asks an
indexer.

The rule exists for a reason: the sweep once fired 104 live searches in one
second and got nzbgeek disabled. A backlog search has to ask the indexers
again, slowly, in a way an operator bounds.

## What it is

A namespaced kind, `BacklogSearch` (catalog.clustarr.io/v1alpha1), one per
namespace (the oldest wins; the others read `Ready=False, Overlap`). Every
`interval` the manager picks a few wanted items of its namespace that have
not been searched live for a while and lets each make one live search,
through the search planner's usual pacing and admission.

```yaml
apiVersion: catalog.clustarr.io/v1alpha1
kind: BacklogSearch
metadata: {name: default, namespace: clustarr-system}
spec:
  enabled: true          # false pauses it
  interval: 15m          # a cycle
  hourlyLimit: 20        # backlog searches started per rolling hour
  missing:     {perCycle: 5, cooldown: 168h}
  cutoffUnmet: {perCycle: 1, cooldown: 336h}
  kinds: []              # empty: every searchable kind
  seasonSearch: true     # two or more due episodes of a season: one season query
```

- **Candidates.** A monitored item of a listed kind, wanted (`wantedcron`'s
  rule: phase Wanted is missing, CutoffUnmet is below the cutoff), searched
  before (an item never searched gets its live search the usual way), no
  search in flight. An Episode's or Issue's container must be monitored too.
- **Cooldown.** An item is due when its last live search is older than its
  queue's `cooldown`. The last live search is a new status field,
  `lastLiveSearchAt`, on Movie, Episode, Album, Book, Audiobook and Issue,
  which the search planner writes when it dispatches a live search (the
  first, a redownload, a backlog search) and when a season search's answer
  is folded into an Episode. An item with none (searched before the field
  existed) is due.
- **Order.** Missing before cutoff-unmet; within a queue, the oldest last
  live search first (none first, then the oldest `lastSearchedAt`), so a
  backlog is worked through round-robin rather than at random.
- **A cycle** takes up to `missing.perCycle` units from the missing queue,
  then up to `cutoffUnmet.perCycle` from the other, never past what
  `hourlyLimit` leaves of the last hour. A unit is one search: one item, or
  one season.
- **Seasons.** With `seasonSearch`, an Episode due with at least one more
  due episode of its season is taken with them as one unit: the Series'
  automatic season search asks for the season (as it does for
  `ReplaceDue`), the episodes wait for it, and its answer is each one's
  search. One query per indexer instead of one per episode.
- **Never a burst.** A unit is a grant, not a search: the manager wakes the
  item's key, and the search planner dispatches the live search behind the
  namespace's live-search pace (`--search-live-per-minute`) and the
  dispatch ledger's admission, as any live search. The index agent still
  applies every Indexer's request delay and query limits.

## How it is built

- **The grant book** (`app/catalog/backlog.Book`) is leader-local, like the
  donor budget: item uid to a grant (its reason, when it was given, when it
  lapses: `max(2 x interval, 3 h)`, longer than the automatic season
  search's one-hour cooldown). It wakes each granted key through a channel
  the remediation loop reads at normal priority. A new leader starts empty;
  the next cycle grants again.
- **The planner** (`searchplan`): `View.Backlog` makes a granted, wanted
  item's video search live whatever its backoff, unless it waits for its
  season; `View.SeasonBacklog` is that wait; `SeasonFacts.BacklogDue`
  counts a season's granted episodes, and `automaticSeasons` asks for a
  season with two or more and no grab holding it. `Plan.LastLiveSearchAt`
  is stamped at a live dispatch and at a season fold. The stage spends a
  grant when it publishes the search (or folds the season's answer).
- **The controller** (`app/catalog/controller/backlogsearch`) runs a cycle
  per reconcile and requeues at the next. It lists candidates through a
  marker index (`itemread.BacklogIndex`, value the reason), never a whole
  kind from the row cache. Its status, under its own field manager
  `catalog-backlog`:

  | field | |
  | --- | --- |
  | `lastCycleAt`, `nextCycleAt` | the cycle clock |
  | `missing`, `cutoffUnmet` | `candidates`, `due` (past cooldown), `oldestLiveSearchAt` |
  | `searchesLastHour` | units granted in the last hour |
  | `recent[]` | the last 100 units: kind, name, season, reason, at |

  `recent` doubles as the hourly count after a leader change, so
  `hourlyLimit` is at most 100.
- **Metrics:** `clustarr_backlog_searches_total{reason,unit}` and
  `clustarr_backlog_items{reason,state}` (state `candidate` or `due`).

## Limits of this design

- A grant is spent at dispatch: a backlog search every indexer answered
  "paced" (`AllPaced`) loses its turn until the cooldown. The pace and the
  hourly limit should keep that rare.
- `lastLiveSearchAt` is stamped at dispatch, not at answer, since a search
  record does not say whether it was live.
- It counts searches, not indexer queries: one search asks every Indexer
  the namespace has. `hourlyLimit` should be read as "per indexer".
- No UI page yet; `kubectl get backlogsearches` and the status are the view.
