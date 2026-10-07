# Series and season search, and the interactive results order (Wave S)

**Status:** Built, 2026-10-07 (implement-only: the tests are the batch's), from the owner's
decisions of the same day (§1). The plan is `docs/superpowers/plans/2026-10-07-series-search.md`
(S1-S12). Where the build departs from this text, "As built" at the end says so.

**Basis:**

- Worktree `/home/appkins/src/mediactl/clustarr-unify`, branch `unify-manager-agent`, at
  `de3c1988` (W6.17 done; Wave N in progress in the same worktree, touching only
  `pkg/events/natsbus`, `pkg/obs/metrics` and the chart). Symbols are cited by name; line
  numbers drift.
- `docs/research/arr-search-pipeline.md` (2026-10-07): how Sonarr v4 and Radarr build searches,
  filter results and search a series, verified against their source, and the live findings on
  kind-cluster-plex. "The research" below.
- ADR-0019 and its design (`docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`,
  "the ADR-0019 design"): the manager decides, agents execute; a Series owns every episode grab
  (§6.1); the item key's search planner (§7.3), the grab planner (§6.6), dispatch admission and
  the `search` Pacer class (§5.4); search answers are records (§7.4).
- Main's `a245eaf7` (Sonarr's season table, the episode details modal, the interactive search,
  `ui/actions/search.go`). It is not on this branch yet; the ui half of this design builds on it
  (§10, and the plan's precondition).

**What this design amends.** The ADR-0019 design's §6.2 sentence "Searches stay per item (an
Episode is searched, not its Series)" gains an exception: a Series is searched per season when a
person asks for it (§4). CLAUDE.md's "A season has no search of its own" and "a Series is not a
Search fan-out kind" become "a season is searched as one query, planned by the Series" (§10.6).
`ContainerKind` is unchanged: a Series is still not a fan-out container.

CLAUDE.md's invariants bind throughout: one complete declaration per field manager per apply;
every status list and string capped; no float in `api/`; a typed client cannot send a value a
CRD default overrides (so `spec.season` is a pointer with no default); every KV key through
`events.KVKeyToken`; and, above all here, **a pool of searches is never fired at an indexer in
one burst** (the 2026-10-06 wanted-sweep incident).

---

## 1. Owner decisions (2026-10-07, binding)

The owner asked how Sonarr handles series searches ("our series search button currently fails
saying series is unsupported") and complained that searches show easily filtered noise
("Monster (2004)" listing "Pocket Monsters", "Monster Eater", FLAC singles). The research
answered both; the owner chose to build the result on the unify branch as a planned piece:

1. **Series and season search.** One season query per monitored season:
   `tvsearch&tvdbid&season=N` where the indexer supports ids, and the title alone for text-only
   and anime indexers. Judge packs and singles against the season's wanted episodes. Prefer a
   pack on a quality and score tie (Sonarr's `CompareEpisodeCount`). Never grab an episode twice
   in one batch. Leave the episodes no result covers to the paced wanted sweep: **no per-episode
   burst**.
2. **Interactive results order.** Approved releases first, then rejected ones. Identity
   mismatches (`WrongItem`, `UnknownItem`, `UnableToParse`) go last, collapsed behind "N
   releases for other titles". The season view defaults to packs. Identity rejections are kept,
   never dropped, so a person can override a mislabelled release.
3. **Yearless clean-title collisions.** When two library items share a clean title, a yearless
   match is ambiguous (a gap Sonarr itself has).

## 2. Decisions in brief

| # | Decision | Section | Cost if wrong |
|---|---|---|---|
| D1 | A series or season search is a `Search` on `mediaRef.kind: series`, with an optional `spec.season`. With `grabBest` it is the Series key's to plan; without it, it must name a season and is an interactive season search the Search controller runs as one task. | §3 | A second request shape (an annotation on the Series) would be needed for intents a Search cannot carry. Nothing found needs one. |
| D2 | The **Series key's season planner** (the search stage, applied to Series) plans every grabBest series or season search: one season task in flight per Series, each reserving the namespace's `search` pace slot and admitted through the dispatch ledger on `catalog-search-high`. Its state is `Series.status.seasonSearch` under `catalog-grab`. | §4 | Serial seasons make a 30-season series search take minutes. Raising the in-flight count later is a one-constant change; the reverse (a burst) is what the 2026-10-06 incident cost. |
| D3 | One query per indexer per season: `t=tvsearch&tvdbid=<series>&season=<scene season>` where the indexer's tv-search caps take `tvdbid`; else `q=<title>`, with the structured `season` parameter only for a non-anime series on an indexer whose caps list it; an anime series on a text-only indexer gets the title alone plus its anime categories. | §5 | Reading "the title alone" as dropping the season parameter for standard series too would return every season from a text-only indexer: more noise and more page truncation. The choice is one condition in `buildQuery`. |
| D4 | The agent judges a season answer in **season-search identity mode**: a release must be of the series and cover at least one episode of the season, read through TheXEM. It records each release's coverage (`ParsedFacts.Coverage`, scene-mapped) so the manager judges packs without reparsing. | §6.1 | Without the agent's coverage the manager would reparse titles, without the scene table. |
| D5 | The grab planner judges every candidate against every episode its coverage names: P23 narrowing stays for episodes that do not want it (a file at cutoff, unmonitored, unaired), but **a release overlapping an episode a live grab already covers is refused whole** unless it upgrades that grab, so no episode is grabbed twice. | §6.3 | A pack found after singles were grabbed is not taken for its other episodes; those wait for the sweep. That is Sonarr's `QueueSpecification`. |
| D6 | Pack preference is Sonarr's `CompareEpisodeCount`, in both `decision.Rank` and the grab planner: after quality, revision and score, a whole-season release first; then anime prefers more episodes and every other series fewer. | §6.4 | Standard series stop preferring a two-episode release over a single on a tie (a behaviour change for episode searches, matching Sonarr). |
| D7 | A season search counts as each wanted episode's search: the Episode key folds the season answer into its `lastSearchedAt` and attempts, so its next search is index-only; an Episode whose season is queued or in flight waits for it. Uncovered episodes are left to their own paced, backed-off sweep. Nothing publishes a per-episode search for a season. | §7 | An episode the season query missed gets no live search of its own until the index (fed by RSS and by the season query's own results) has it. |
| D8 | Results are banded in the agent before the record's cap and again in the manager after the item-state rules: approved, rejected, then other titles. `Search.status.otherTitles` counts the trailing band; the ui collapses it. Applies to every interactive search, movies included. | §8 | Banding only in the ui would let identity noise push real rejected releases past the 200-candidate cap. |
| D9 | The yearless collision rule lives in `pkg/decision`'s item half (`itemRejection`): a release matched by title only, naming no year, whose matched title another library item of the kind also carries (`Identity.Namesakes`), is `UnknownItem`. The agent fills `Namesakes` from the title indexes the RSS matcher already keeps. | §9 | Ids and id queries still identify such a release, so only text-found yearless releases are affected; a namesake added later re-judges only new searches. |
| D10 | Not adopted from Sonarr: the per-episode anime season burst, `SeasonPackOnlySpecification`, and an automatic season search in the wanted sweep (the owner question, §17). Adopted: `FullSeasonSpecification` (a full-season pack with an unaired episode is refused). | §6.5, §15 | |

## 3. Requesting a series or season search

### 3.1 Three shapes of one kind

A person (the ui, or `kubectl`) asks with a `Search`, as for every other kind. The Search keeps
its role of record: a short-lived, TTL-deleted object `kubectl get searches` shows.

| Spec | Meaning (Sonarr) | Planned by | Answers in |
|---|---|---|---|
| `mediaRef: {kind: series, name}`, `grabBest: true` | series search: every monitored season with a wanted episode, ascending, specials excluded (`SeriesSearchService` with seasons monitored) | the Series key (§4) | `Series.status.seasonSearch`, mirrored to `Search.status.seasons`; grabs on the Series |
| the same, `season: N` | season search (`SeasonSearch(monitoredOnly: true)`), specials included when named | the Series key (§4) | as above, one season |
| `mediaRef: {kind: series, name}`, `season: N`, no `grabBest` | interactive season search (`SeasonSearch(interactive: true)`) | the Search controller, one task (§8) | `Search.status.results`; picks in `spec.grab` go to the Series as candidates |

A Search on a series with neither `season` nor `grabBest` is refused at admission: Sonarr has no
interactive series search, and its results would be every season's releases at once.

### 3.2 API (`api/catalog/v1alpha1/search_types.go`)

```go
// SearchSpec defines the desired state of Search.
//
// +kubebuilder:validation:XValidation:rule="(has(self.query) ? 1 : 0) + (has(self.mediaRef) ? 1 : 0) == 1",message="exactly one of query or mediaRef must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.season) || (has(self.mediaRef) && self.mediaRef.kind == 'series')",message="season needs mediaRef.kind series"
// +kubebuilder:validation:XValidation:rule="!has(self.mediaRef) || self.mediaRef.kind != 'series' || has(self.season) || (has(self.grabBest) && self.grabBest)",message="a series search either grabs (grabBest) or names a season: an interactive search is per season"
// +kubebuilder:validation:XValidation:rule="!has(self.mediaRef) || self.mediaRef.kind != 'series' || !has(self.grabBest) || !self.grabBest || !has(self.grab) || size(self.grab) == 0",message="a series search with grabBest grabs through the series; spec.grab needs an interactive season search"
type SearchSpec struct {
	// ... existing fields unchanged ...

	// Season narrows a series search to one season (0 is the specials). With
	// grabBest the series searches that season and grabs what it finds;
	// without it this is an interactive season search, its results in
	// status.results. Only with mediaRef.kind series.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Season *int32 `json:"season,omitempty"`
}

// PlannedBySeries reports a Search the Series key plans (§4): a series or
// season search with grabBest.
func (s SearchSpec) PlannedBySeries() bool {
	return s.MediaRef != nil && s.MediaRef.Kind == commonv1.MediaKindSeries && s.GrabBest
}
```

Every CEL rule guards an optional field with `has()` (CLAUDE.md, Phase D2: an unguarded optional
field in a rule raised "no such key" and the apiserver rejected every write). `season` is a pointer with no default: season 0 is real,
and a Go client must be able to send it (CLAUDE.md, typed-client gotcha). An edit of a Search's
spec bumps its generation; a planned Search is re-read by the Series on every pass (§4.4), and an
interactive one starts over, as today (`taskSeq`).

### 3.3 kubectl

```sh
# search the whole series (every monitored season with a wanted episode)
kubectl create -f - <<'EOF'
apiVersion: catalog.clustarr.io/v1alpha1
kind: Search
metadata: {generateName: monster-, namespace: media}
spec: {mediaRef: {kind: series, name: monster}, grabBest: true}
EOF

# search season 1 and grab;  interactive season 1
spec: {mediaRef: {kind: series, name: monster}, season: 1, grabBest: true}
spec: {mediaRef: {kind: series, name: monster}, season: 1}

kubectl get search monster-x7k2p -o jsonpath='{.status.seasons}'
kubectl get series monster -o jsonpath='{.status.seasonSearch}'
```

### 3.4 The ui

The series header's search button already creates `Search{mediaRef: series, grabBest}`
(`actions.SearchNow`); today it fails in the agent (`kind series is not searchable`), and after
this design the Series plans it. Each season row gains Sonarr's two buttons: the magnifier
(`actions.SearchSeason`, grabBest with `season`) and the person (`actions.InteractiveSeasonSearch`,
`season` without grabBest), which opens the season's results modal (§10).

## 4. The Series key's season planner

### 4.1 Why the Series key

ADR-0019's rule is that the manager decides and agents query. The Series key already owns every
episode grab (§6.1 of the ADR-0019 design), reads every Episode of the series through
`series.EpisodeBySeriesRefIndex`, and runs the grab planner that must judge the answers. Putting
the season plan on the same key means one serialised reconcile decides which seasons to search,
when, and what each answer grabs, against one fresh view of the series' entries. The Search
controller cannot: it would need the Pacer and the ledger, and it is not the owner of the grabs.

The planner is a second mode of the search stage (`app/remediation/search`), which today applies
to the searchable item kinds; it now also applies to `KindSeries`, whose pass runs
`searchplan.DecideSeasons` instead of `searchplan.Decide`. A Series has no automatic search of
its own: the stage plans only person-requested searches.

### 4.2 State (`Series.status.seasonSearch`, under `catalog-grab`)

New file `api/catalog/v1alpha1/series_search_types.go`:

```go
// SeasonSearchState is the person-requested series or season search a
// Series serves or last served (Wave S): one season task in flight at a
// time, paced. Written by the remediation loop's search stage under
// catalog-grab, one complete set with pendingGrabs.
type SeasonSearchState struct {
	// Request is the Search this state serves.
	Request SeasonSearchRequest `json:"request"`
	// StartedAt is when the Series took the request.
	StartedAt metav1.Time `json:"startedAt"`
	// FinishedAt is when every season reached a final phase.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Seasons are the seasons asked for, ascending, each with its outcome.
	// +listType=map
	// +listMapKey=number
	// +kubebuilder:validation:MaxItems=200
	Seasons []SeasonSearchSeason `json:"seasons"`
	// Dispatch is the season task in flight (the season whose phase is
	// Searching); Seq > AnsweredSeq while one is out.
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
}

// SeasonSearchRequest names the Search a SeasonSearchState serves.
type SeasonSearchRequest struct {
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=36
	UID types.UID `json:"uid"`
}

// SeasonSearchPhase is where one season of a series search is.
// +kubebuilder:validation:Enum=Queued;Searching;Answered;Skipped;Failed;Canceled
type SeasonSearchPhase string

// SeasonSearchReason says why a season was skipped, failed or canceled.
// +kubebuilder:validation:Enum=NotMonitored;NothingWanted;AllDownloading;NoSuchSeason;AllPaced;TimedOut;AgentFailed;SearchDeleted
type SeasonSearchReason string

// SeasonSearchSeason is one season of a series search.
type SeasonSearchSeason struct {
	// +kubebuilder:validation:Minimum=0
	Number int32             `json:"number"`
	Phase  SeasonSearchPhase `json:"phase"`
	// +optional
	Reason SeasonSearchReason `json:"reason,omitempty"`
	// Seq is the dispatch seq this season's task carried; its record
	// answers at it, and the grab stage reads the record only at it.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Seq int64 `json:"seq,omitempty"`
	// Attempts counts the season's dispatches: a season every indexer
	// paced goes once more to the back of the queue (§4.5).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	AnsweredAt *metav1.Time `json:"answeredAt,omitempty"`
	// Candidates and Approved are the answer's counts.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Candidates int32 `json:"candidates,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Approved int32 `json:"approved,omitempty"`
}
```

`SeriesStatus` gains `SeasonSearch *SeasonSearchState` (`+optional`). `status.lastSearchedAt`,
which exists on Series and which nothing writes on this branch, becomes the planner's: the latest
`answeredAt` of a finished request. Both join `catalog-grab`'s set on a Series beside the grab
stage's `pendingGrabs`; the search stage's `DeclaresSets(KindSeries)` returns `catalog-grab`, so
a pass in which either stage failed applies none of the set and releases neither stage's fields.

Size: a season entry is about 150 bytes at its caps, so 200 seasons add about 30 KB to a Series
whose worst case the ADR-0019 design puts at about 495 KB of entries (§6.3). The batch's
`TestItemAtEveryCapFitsTheBudget` builds the Series with `seasonSearch` at its caps; if the
Series measures over `ContainerStatusBudgetBytes` (1 MiB), the plan lowers `Seasons`' cap to
100 before touching any entry cap.

### 4.3 The request queue

- **Which request.** The Series reads the Searches naming it through a new manager field index,
  `remediation.search.series` (value: `spec.mediaRef.name` of a Search whose
  `spec.PlannedBySeries()`), and serves the oldest (by `creationTimestamp`, then name) that is
  not deleting and not yet served.
- **One at a time.** A new request starts only when no state exists, or the state's request is
  finished **and** its Search reads `Completed` or `Failed`, or is gone. This handshake lets the
  Search controller copy a finished outcome before the state is replaced (§4.6). A later Search
  waits, phase `Running` with `Ready=False, reason Queued` on the Search, naming the one served.
- **Starting.** The planner lists the request's seasons: the named one, or every season of
  `status.seasons` but 0. Each starts `Queued`, or final at once as `Skipped` with a reason:
  `NoSuchSeason` (not in `status.seasons`); `NotMonitored` (the series, or every episode of the
  season, unmonitored: Sonarr's `monitoredOnly`); `NothingWanted` (no monitored, aired episode
  is missing or below its cutoff: `wantedcron.CandidateOf`'s rule); `AllDownloading` (every
  wanted episode is covered by a live entry). A query that cannot grab anything is not sent.
- **Canceling.** A served Search that is deleted (or deleting) cancels the rest: the in-flight
  dispatch closes `Withdrawn`, the `Searching` season and every `Queued` one become `Canceled,
  SearchDeleted`, and the state finishes. An answer arriving after that is not grab input (the
  grab stage reads `Answered` seasons only): deleting the Search withdraws the request. Seasons
  already `Answered` stay grab input inside their window.

### 4.4 One season at a time

`searchplan.DecideSeasons(v SeasonView, admit, pace) SeasonPlan` is pure, in
`app/catalog/searchplan/season.go`, and reuses `Decide`'s timing:

1. **Incorporate.** A record at `agentrecords.SeasonSearchKey(series uid, n)` whose seq equals
   the dispatch's: the season becomes `Answered` (`answeredAt`, `candidates`, `approved`), the
   dispatch closes (`answeredSeq = seq`) and the ledger is told (`DispatchAnswered`). A record
   that failed (`state: failed`) makes it `Failed, AgentFailed`. A record with `allPaced` sends
   the season once more to the back of the queue (`attempts` 2), and a second all-paced answer
   makes it `Answered, AllPaced` (it counts for no episode, §7).
2. **Timeout and republish.** As `Decide`: closed `Withdrawn` after `searchplan.Timeout` (15 min)
   as `Failed, TimedOut`; republished under its Msg-Id every `searchplan.RepublishEvery` (2 min)
   while in flight, from the stage's publish book, or rebuilt after a leader change.
3. **Next season.** With nothing in flight, the lowest `Queued` season is due. It reserves the
   namespace's pace slot first (`records.SearchClass(ns)`, key `<series uid>/season-<n>`): a slot
   more than a second away leaves the state as is, with `Dispatch.delivery` waiting, reason
   `Paced`, and `Due` at the slot. Then admission:
   `dispatch.Ledger.Admit(searchplan.SeasonDurable, key, seq)`, with `SeasonDurable =
   events.ConsumerCatalogSearchHigh` and key `{Series, ns, name, history.DispatchSubSeasonSearch}`;
   refused, the delivery shows the ledger's reason. Admitted: the season becomes `Searching`
   with its `seq`, the dispatch is written, and the task is published after the apply
   (`remediation.DispatchPublish` with `Pace`/`PaceKey`, so the reservation is consumed).
4. **Finish.** Every season final: `finishedAt`, and `lastSearchedAt` = the newest `answeredAt`.

`Seq` is `max(record seq + 1, dispatch seq + 1, now in ms)` as `Decide`'s `records`. A season
task is user-invoked and live: never `IndexOnly`, never a donor search.

### 4.5 Pacing and the no-burst rule

A series search sends exactly one season task per Series at a time, each after a slot in the
same namespace class the wanted sweep's live searches use (20 per minute by default,
`--search-live-per-minute`). Ten seasons are ten queries per indexer spread over at least 30
seconds and serialised behind each answer. Two series searched at once interleave in the class.
Nothing in this design publishes an Episode search: the uncovered episodes are the sweep's
(§7). The interactive season search (§8) is one task, published by the Search controller
without pacing, exactly as an interactive episode search is.

The lane is `high` (a person is waiting), on `catalog-search-high`, admitted against its
`2 × MaxAckPending` budget. A new `search.SeasonLedgerSource` rebuilds that durable's
outstanding season dispatches from every Series' `status.seasonSearch.dispatch` after a leader
change. The Search controller's interactive tasks on the same durable stay unledgered, as today.

### 4.6 The Search's view of it

The Search controller is the sole writer of a planned Search's status (`catalog`; no results,
no indexer outcomes, so nothing under `catalog-worker`). It watches Series (a map function over
the same index) and renders:

- `phase: Running` and `startedAt` when created; `Ready=False, Queued` while another request is
  served; `Ready=False, Searching` while its own is.
- `status.seasons[]` (new, below): each season's phase, reason and counts copied from the
  Series' state, plus `grabbed`, the number of the Series' entries whose release GUID is among
  that season record's approved candidates and whose `grabbedAt` is at or after the season's
  `answeredAt`.
- `Completed` with `finishedAt` when the Series' state for its UID is finished; `Failed`
  (`SeriesGone`) when the Series is gone; `Failed` (`Stalled`) when the served state shows no
  transition for `SeriesSearchStallTimeout` (30 min). The Search controller's five-minute
  `SearchRunningTimeout` does not apply to a planned Search: a series search legitimately runs
  longer.
- TTL from `finishedAt`, as a container Search's.

```go
// SearchSeason is one season of a planned series search, as the Search
// reports it.
type SearchSeason struct {
	// +kubebuilder:validation:Minimum=0
	Number int32             `json:"number"`
	Phase  SeasonSearchPhase `json:"phase"`
	// +optional
	Reason SeasonSearchReason `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Results int32 `json:"results,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Approved int32 `json:"approved,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Grabbed int32 `json:"grabbed,omitempty"`
}

// on SearchStatus:
	// Seasons is a planned series search's progress, one entry per season.
	// +optional
	// +listType=map
	// +listMapKey=number
	// +kubebuilder:validation:MaxItems=200
	Seasons []SearchSeason `json:"seasons,omitempty"`
```

`bestApproved` returns nothing for a planned Search (it has no results), so `handlePicks` never
runs for one; its grabs are the Series' own.

## 5. The query per indexer

### 5.1 The task

`schema.SearchTask` (still `catalog.SearchTask.v2`; the fields are optional, so an older agent
decodes it) gains `Season *int32`. A season task carries `MediaRef{Kind: series, Name}`, `Item`
= the Series (UID included), no `Container`, `Season`, `Reason: SearchReasonSeason` (new,
`"season"`), `UserInvoked: true`, `Seq`, the Series' `QualityProfile`, the enabled
`Protocols`, the `IndexerPriorities` and `Scope = BlockScopeOf(series)`. Msg-Id:
`events.MsgIDForSeasonSearch(series uid, n, seq)` = `season-search/<uid>/<n>/<seq>`, which
`history.ParseDispatchID` reads as `DispatchID{UID, Seq, Sub: DispatchSubSeasonSearch, Kind:
"Series"}` so the advisory intake resolves a nak or a dead letter to the Series. Subject:
`events.WorkSearchSubject(PriorityHigh, MediaKey("series", ns, name))`.

An interactive season search's task is the same with `SearchRef` set (keyed by the Search's UID,
as every interactive task) and `Reason: SearchReasonInteractive`.

### 5.2 The agent's request

`app/catalog/worker/search` accepts a task on `MediaKindSeries` only with `Season` set
(`SeasonSearchable(task)`); a series task without one fails its record with "a series search
names a season". `snapshotSeason` reads the Series and the season's Episodes (through
`rssmatcher.IndexEpisodeSeriesSeason`, which the catalog domain now declares) and fills
`TargetIDs` and the decision `Target` (§6.1). `BuildSearchRequest` for a season:

- `Kind: MediaKindEpisode` (the tv-search mode, `modeFor`), `IDs[tvdb]` = the Series' TVDB id,
  `Season` = the season's **scene** season (TheXEM: the scene season most of the season's
  episodes map to; the stored number when the series is unmapped), `Episode` nil, `Year`.
- `Text` = the title alone: no `SxxEyy`, no number (`resolvedText` already returns the bare
  title when `Episode` is nil).
- `Anime` (new on `schema.SearchRequest`, optional) = the Series' type is anime. An anime season
  keeps its `Season` (today's anime rule, which drops it, is for an episode request carrying an
  absolute number); the index agent decides whether to send it (§5.3).

### 5.3 The index agent's query (`app/indexer/search.buildQuery`)

| Indexer's tv-search caps | Standard series | Anime series |
|---|---|---|
| `tvdbid` (and `season`) | `t=tvsearch&tvdbid=<tvdb>&season=<n>` | `t=tvsearch&tvdbid=<tvdb>&season=<n>` |
| text only, `season` listed | `t=tvsearch&q=<title>&season=<n>` (a definition renders it into its keywords, e.g. `S01`) | `t=tvsearch&q=<title>`, plus `spec.animeCategories` |
| text only, no `season` | `t=tvsearch&q=<title>` | `t=tvsearch&q=<title>`, plus `spec.animeCategories` |
| neither (no tv-search) | skipped (`skipNoIDParam`, as today) | as standard |

`buildQuery` computes the id half first, then sets the season parameter only when the query is
an id query, or when it is a text query for a non-anime request; `isAnimeRequest` becomes
`req.Anime || <today's absolute shape>`. Ids still win wherever they work: an indexer that takes
`tvdbid` never receives `q`. One query per indexer per season, paged by the index agent as today.

**Why the season parameter on a text query for a standard series.** The owner's "the title alone
for text-only indexers" is read as "no id and no number in the text": the structured `season`
parameter is what Sonarr's own title tier sends beside the title for a season (research §5, the
season criteria's title tier), and without it a text-only indexer returns every season of the
show, all of them judged `WrongItem`, and the pack itself is more likely past the indexer's page. For anime, scene packs
are named by absolute range or "Batch", never `S01`, so the season token would hide them; anime
gets the title alone. Every season's text query is the same string; the season parameter is the
only thing that differs per season.

nyaa's definition takes its category from the indexer's settings, never from the request
(research, "Findings"): the owner's `spec.settings.cat-id: "1_2"` already narrows it, and nothing
here changes that.

## 6. Judging the season's answers

### 6.1 In the agent: season-search identity and coverage

`EpisodeIdentity(series, episodes...)` already takes every episode of a target. A season task
passes every episode of the season (monitored or not: identity asks "is this release for this
season", not "is it wanted"), and `Identity` gains:

```go
// SeasonSearch is true when the candidates answer a search for a whole
// season: the numbering half then accepts a release covering at least one
// of Episodes (or Absolute), a single, a multi-episode release, a
// full-season pack or a multi-season pack naming the season, instead of
// every one of them. Set by the season search's caller only, like
// SingleEpisodeSearch; the two are never both true.
SeasonSearch bool
```

`numberingRejection` uses `coversAnyInSeason` / `coversAnyAbsolute` in that mode; a release of
another season, or of no episode of this one, is still `WrongItem`. The size check sums the
runtimes of the episodes the release covers (`Target.RuntimeByEpisode`, episode number to
minutes, the season's), so a single episode is not measured against a whole season's runtime.
`seasonPackRejection` (R-3) is untouched: a single-episode search still refuses a pack.

Each decision of a video kind gains its **coverage**: `decision.CoverageOf(parsed, scene)` exports
the internal `releaseCoverage` (full seasons, season and episode pairs, absolute numbers, read
through the series' TheXEM table first), and the agent records it on every scored release:

```go
// schema.ParsedFacts gains:
	// Coverage is what the release covers in the series' stored numbering,
	// after its scene mapping (decision.CoverageOf); nil for a non-episode
	// kind or an unparsed title.
	Coverage *ReleaseCoverage `json:"coverage,omitempty"`

// ReleaseCoverage: at most MaxCoverageNumbers (1000) entries in each list.
type ReleaseCoverage struct {
	FullSeasons []int32         `json:"fullSeasons,omitempty"`
	Episodes    []SeasonEpisode `json:"episodes,omitempty"`
	Absolute    []int32         `json:"absolute,omitempty"`
}
type SeasonEpisode struct {
	Season  int32 `json:"season"`
	Episode int32 `json:"episode"`
}
```

The record is keyed `agentrecords.SeasonSearchKey(series uid, n)` =
`events.RecordSubKey(series uid, "season-<n>")`, with the header's `Sub` set and the new
`SearchRecord.Season`; `agentrecords.Searches().KeyOf` becomes `(r.Task, r.Sub)`. Its `Item` is
the Series, so S29 (the stage's record source) wakes the Series key.

### 6.2 Coverage in the manager

`grabplan.Candidate` and `schema.Candidate` gain `Season *int32` (the season a season answer or a
season search's pick is for). `grabplan.FromSeasonRecord(rec, season, grabbedBy)` turns a season
record into candidates (approved releases only, `NoDelay: true`: a person asked, and Sonarr's
delay specification passes a user-invoked search). `coverage(c)` on a Series becomes:

1. A candidate whose `Release.Parsed.Coverage` is set covers every Episode of the Series it
   names (any season: a multi-season pack is judged against every episode it brings), plus its
   `Targets`.
2. Without coverage (a record from an older agent): today's rule for an episode answer (the
   target and the same-season parsed numbers); for a season candidate, the searched season's
   episodes the parsed facts name (`FullSeason`: all of them).

### 6.3 The grab planner: narrowing, and no episode twice

The Series' grab stage gathers, besides today's inputs, each `Answered` season of its
`status.seasonSearch` whose record answers at the season's `seq` and is younger than
`grabplan.CandidateWindow` (30 min), so a stage failure on the pass that brought an answer does
not lose it. Each candidate is then re-checked per covered episode, as today
(`ItemStateRejections` against the episode's file, the queue, monitoring, availability), with
three changes:

1. **A live grab refuses the whole release.** If any covered episode is covered by a live video
   entry (any phase before `Imported`) and the release is not an upgrade of that entry's release
   (`quality.Profile.UpgradeDecision`), the release is refused, not narrowed: its payload would
   bring that episode a second time. P23's narrowing still applies to every other reason an
   episode does not want it (a file at its cutoff, `TranscodedFinal`, unmonitored, unaired).
   A person's pick is refused the same way unless `spec.override` (the rejection is
   `QueueHigherPreference`, permanent). This is Sonarr's `QueueSpecification`, and with the
   existing in-pass `taken` set (Sonarr's `IsEpisodeProcessed`) it means no episode is grabbed
   twice, inside a pass or across the passes of one series search.
2. **A full-season pack with an unaired episode is refused** (`decision.ReasonFullSeasonNotAired`,
   permanent: Sonarr's `FullSeasonSpecification`), unless picked by hand: a release that claims
   a whole season that has not finished airing is incomplete or mislabelled.
3. **A pack covering more wanted episodes than an entry can name (200) is refused**, never
   truncated (`grabplan.numbers` truncates today, which would attribute the rest of the payload
   to nothing).

One exported helper, `grabplan.CoverageRejections(items []ItemCheck, season int32, fullSeason,
manual, override bool) []commonv1.Rejection`, folds the per-episode results into the release's
(nil: take it, narrowed to the episodes that pass), so the Search controller shows exactly what
the planner would decide (§8.1).

### 6.4 Pack preference

Both `decision.Rank` (the agent's order, which ranks the record and an interactive answer) and
`grabplan.better` (the decision) order by quality tier, revision (unless the profile does not
prefer it), format score, and then `decision.CompareEpisodeCount`:

```go
// CompareEpisodeCount is Sonarr's DownloadDecisionComparer.CompareEpisodeCount:
// a whole-season release ranks first; then an anime series prefers more
// episodes and any other series fewer. <0 when a ranks first.
func CompareEpisodeCount(aWhole bool, aCount int, bWhole bool, bCount int, anime bool) int
```

"Whole season" is `Parsed.FullSeason`, or a coverage that names every aired episode of a season
(an anime batch). `RankKey` gains `Anime` (from the new `Target.Anime`); `grabplan.View` gains
`Anime` (the Series' type). A Comic keeps "more issues first". With quality, revision and score
equal, a pack beats the singles; with the singles at a better quality, they win, and the pack is
then refused by the overlap rule, leaving nothing grabbed twice: the same outcome as Sonarr.

Evaluate fills `RankKey` for every decision whose title parsed, not only approved ones, so the
rejected band sorts by the same comparator (Sonarr sorts every mapped decision).

### 6.5 Not adopted

- `SeasonPackOnlySpecification` (singles refused once a season is older than a setting): the
  owner's rule is to judge packs and singles and prefer packs on a tie.
- Sonarr's anime season search's per-episode absolute queries (`SearchAnime(isSeasonSearch:
  true)`): one query set per aired episode is the burst the no-burst rule forbids.

## 7. Uncovered episodes

The season planner never publishes an Episode search. An episode the season's answer did not
cover stays wanted, and its own planner (`searchplan.Decide`) searches it on its backoff
(6 h · 2^n, capped at 7 d), paced in the same class. Two changes keep that from becoming the
burst it replaces:

- **The season answer counts as the episode's search.** The Episode key's search stage reads its
  Series' `status.seasonSearch`: a season of its own number `Answered` (not `AllPaced`) after the
  Episode's `lastSearchedAt` is folded in once, while the Episode is wanted, as one attempt at
  `answeredAt` (`searchplan.View.SeasonAnsweredAt`). Its next search is then `IndexOnly`, against
  the release index, which already holds every release the season query returned (the fan-out
  upserts its results) as well as RSS. Sonarr updates `LastSearchTime` the same way.
- **An episode waits for its season.** While its season is `Queued` or `Searching`, the Episode
  starts no search of its own except a redownload (`View.SeasonPending`), so a person who turns a
  season on and searches it at once sends one query, not one per episode.

S24, the Series-to-Episode wake, gains a second signature, `rollup.SeasonSearchSignature`
(season number to phase): a change enqueues the changed seasons' Episode keys at low priority,
so they fold the answer without waiting for the sweep.

## 8. The interactive season search, and the results order of every search

### 8.1 The season search

The Search controller runs it as it runs an episode's: `interactiveTask` carries `Season`; the
record is keyed by the Search's UID; `incorporate` writes `results`, `indexerOutcomes` and
`finishedAt` under `catalog-worker` by compare-and-swap. What differs is the item-state pass,
`seasonItemState`: for each result it resolves the covered episodes from `Parsed.Coverage`
(the series' Episodes through `series.EpisodeBySeriesRefIndex`), runs the same per-episode checks
the grab planner runs, and folds them with `grabplan.CoverageRejections`, so a release the
planner would refuse reads rejected with the planner's reason. It also sets
`ReleaseDecision.CoversSeason` on a release covering every aired episode of the searched season:

```go
// on commonv1.ReleaseDecision:
	// CoversSeason marks, on a season search's results, a release covering
	// every aired episode of the searched season: a full-season pack or an
	// anime batch. The ui's season view lists these by default.
	// +optional
	CoversSeason bool `json:"coversSeason,omitempty"`
```

A pick (`spec.grab`) is sent to the Series as a `Candidate` with `Season` set and no `Targets`
(§6.2 resolves its coverage), `Manual`, `Override` from `spec.override`, as every pick.

### 8.2 Bands, for every interactive search

```go
// pkg/decision
type ResultBand int
const (
	BandApproved ResultBand = iota
	BandRejected
	BandOtherTitle // any WrongItem, UnknownItem or UnableToParse rejection
)
func BandOf(approved bool, rejs []common.Rejection) ResultBand
```

- **In the agent**, `RankAndCap` orders approved (by `Rank`), rejected (by `Rank`, now that
  rejected decisions carry a `RankKey`), then other titles (arrival order), **before** the
  record's 200-candidate cap: identity noise is what the cap cuts first.
- **In the manager**, `withItemState` re-bands after the item-state rules (an item-state
  rejection moves a release from approved to rejected, never into other titles), caps at
  `spec.limit`, stamps `rank` as the position, and writes `status.otherTitles`:

```go
// on SearchStatus:
	// OtherTitles is how many of status.results, at their end, are for other
	// titles: a WrongItem, UnknownItem or UnableToParse rejection. The ui
	// collapses them; they stay so a person can override a mislabelled one.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=200
	OtherTitles int32 `json:"otherTitles,omitempty"`
```

A wrong-season or wrong-episode release of the right series is `WrongItem` today and falls in
the other-titles band under the owner's rule. The label stays the owner's ("N releases for other
titles"); Review Focus 6 asks whether that reads right.

## 9. Yearless clean-title collisions

**Where.** `pkg/decision`'s `itemRejection`, the title step, after ids (step 1) and the id-query
vouch (step 2) have not decided. `Identity` gains:

```go
// Namesakes are the item's titles another library item of its kind (in its
// namespace) also carries, keyed by the same clean form. A release matched
// to the item only through one of them, naming no year, cannot say which
// item it is: UnknownItem. Ids and an id query still identify it.
Namesakes []string
```

**The rule.** For a release whose title key matched one of the item's keys: if the matched key is
a namesake's bare (yearless) key and the release names no year (`parsed.Year < 1800`, and for a
series no year inside the parsed title, which `targetTitleKeys`' `<title><year>` form already
tells apart), the verdict is `UnknownItem`: "the library holds another <kind> titled %q; a
release naming no year cannot say which it is". A release naming the year matches the
year-suffixed key (series) or passes `movieYearRejection` (movies) and is unaffected. The rule
applies to Movie, Episode and Series targets.

**Where the agent gets it.** `app/catalog/worker/search.Namesakes(ctx, c, ns, item) []string`
looks each of the item's titles up in the title indexes the RSS matcher already keeps:
`IndexSeriesTitleYear` (yearless key `SeriesTitleKey(title, 0)`) and a new yearless
`IndexMovieTitle` (`release.CleanTitle(title)`, beside `IndexMovieTitleYear`), and keeps the
titles another item answers to. The catalog domain declares `rssmatcher.SearchIndexes()` (the
three it reads: `IndexMovieTitle`, `IndexSeriesTitleYear`, `IndexEpisodeSeriesSeason`) in its
registration, as the events domain declares all fourteen; the agent registers each once. The RSS
matcher already refuses an ambiguous yearless series title (`matchSeries`, "two series answer to
one title"); this closes the same gap on the search path, which Sonarr leaves open.

**What it does not do.** A namesake that is not in the library is not known, so "Monster S01E01"
from a 2025 show still matches Monster (2004) when only the 2004 series is in the library; the
owner's rule is about library items.

## 10. The ui

The ui half needs main's `a245eaf7` on the branch (the season table, the episode modal,
`ui/actions/search.go`'s `InteractiveSearch` and `GrabRelease`, `GET /searches/{ns}/{name}`).

1. **Actions** (`ui/actions/search.go`): `SearchSeason(ctx, c, ns, series string, season int32)`
   (grabBest, `season`) and `InteractiveSeasonSearch(ctx, c, ns, series string, season int32)`
   (`season`, no grabBest), both through `uiSearch` with the season set. `SearchNow` is
   unchanged; on a series it now works. No new RBAC: the ui already creates and patches searches.
2. **Routes** (`ui/routes.go`): `POST /library/{namespace}/series/{name}/seasons/{n}/search` (the
   magnifier, answered into `#action-status` as an episode's) and
   `POST /library/{namespace}/series/{name}/seasons/{n}/search/interactive` (the person, answered
   with the season's results modal); `GET /searches/{namespace}/{name}` gains `?filter=packs|all`.
3. **The season header** (`views.SeasonHeader`) gains the two buttons and a badge from the
   Series' `status.seasonSearch` for that season: Queued, Searching, or "Searched: N approved"
   with its age.
4. **The results panel** (`views.SearchPanelView`, every interactive search): approved rows, then
   rejected rows, then a collapsed `<details>` "N releases for other titles" holding the last
   `status.otherTitles` rows, each still grabbable with its override confirm. Rows keep their
   rank order; the ui sorts nothing.
5. **The season view** (`views.SeasonSearchModal`): the same panel with a filter, "Season packs"
   (default: `coversSeason`, or `releaseType: seasonPack`) and "All". With no pack in the
   results, the empty state says so and offers "Show all N releases".
6. **CLAUDE.md** (the UI paragraph): "A season has no search of its own…" becomes a description of
   the two season buttons and the Series' planner; the wanted-sweep gotcha gains a line that a
   season search is one paced query per season and counts as each episode's search.

## 11. Metrics and Events

| Series | Type | Labels | Counted by |
|---|---|---|---|
| `clustarr_season_searches_total` | counter | `trigger` (`series`, `season`, `interactive`), `outcome` (`answered`, `skipped`, `failed`, `timedOut`, `paced`, `canceled`) | the search stage, once per season reaching a final phase; the Search controller, per interactive season answer |
| `clustarr_search_results_total` | counter | `band` (`approved`, `rejected`, `otherTitles`) | the Search controller, per incorporated interactive answer (every kind) |
| `clustarr_search_decisions_total` (existing) | counter | `decision` gains `overlapsLive`, `seasonNotAired`, `tooManyEpisodes` under `reason="grabplan"` | the grab stage |

No label names a title, series or release. `clustarr_dispatch_waiting{consumer}` already shows
a paced or refused season task. Both new series go into `docs/observability.md`'s catalogue and
`metrics_test.go`'s `wantSeries`.

Events: on the Search (the Search controller's recorder), `SeriesSearchQueued`,
`SeriesSearchStarted` and `SeriesSearchFinished` (Normal, "searched 3 seasons, skipped 2,
grabbed 2 releases"), `SeriesSearchStalled` (Warning). On the Series (recorder `series`, an
`ItemEvent`), `SeasonSearchFailed` (Warning, the season and the reason).

## 12. Failure modes

| Case | What happens | What shows |
|---|---|---|
| The agent is older than the manager (skew) and refuses a series task | the record fails; the season is `Failed, AgentFailed`; the next season goes on | the Search's `seasons[]`; a `SeasonSearchFailed` Event |
| The index agent is older and ignores `Anime` | an anime season on a text-only indexer is queried with its season token; fewer anime packs come back | nothing is wrong, only thinner; gone once the index agent is current |
| No indexer takes `tvdbid` or a text query for the series | every indexer is skipped (`skipNoIDParam`); the season is `Answered` with no candidates | indexer outcomes; the Search's counts |
| Every indexer paced (`allPaced`) | the season goes to the back once, then reads `Answered, AllPaced` and counts for no episode | the season's reason |
| A record lost (NATS loss), or no agent takes the task | the dispatch closes `Withdrawn` after 15 min, `Failed, TimedOut`; the next season goes on | `delivery` on the Series' dispatch while waiting (`NoAgent`); the Event |
| A leader change mid-search | the ledger rebuilds from `seasonSearch.dispatch`; the publish book is empty, so the task is rebuilt and republished under its Msg-Id (absorbed if it was published) | nothing |
| The Series at its entry cap (24) | the grab planner pends or refuses (`AtCapacity`), never truncates; the search goes on | the Series' Event |
| A pack covering more than 200 wanted episodes (a daily show's year) | refused, `tooManyEpisodes` | the decision metric; the episodes go to the sweep |
| The served Search is deleted | the rest is canceled; a late answer is not grab input; answered seasons stay so inside their window | the Series' state |
| Two Searches on one series | served in creation order; the second reads `Queued` | the Search's Ready condition |
| A season whose scene numbering splits it across two scene seasons | the query names the scene season most of its episodes map to; the other part's episodes are judged `WrongItem` or not returned, and go to the sweep | the season's counts |
| Text-only standard series without a `season` parameter in the indexer's caps | every season comes back; other seasons are `WrongItem`, collapsed under "other titles" | the collapsed count |
| An unmonitored series or season | `Skipped, NotMonitored`; nothing is queried | the Search's `seasons[]` |
| A namesake removed from the library | only searches after the removal are judged without it | nothing to repair |

## 13. What changes for movies (and the other kinds)

- **Movies:** the interactive results order (bands, `otherTitles`, the collapse) and the namesake
  rule (a yearless, text-found release of a title two library movies share is `UnknownItem`).
  Nothing else: one search, one query, as today.
- **Episodes:** the bands, the namesake rule through their Series, Sonarr's `CompareEpisodeCount`
  (a standard series now prefers a single over a multi-episode release on a tie), the live-grab
  refusal, and the season-search fold (§7).
- **Albums, books, audiobooks, issues, comics:** the bands only. A Comic keeps "more issues first".

## 14. API changes, consolidated

| Where | Change | Markers |
|---|---|---|
| `SearchSpec` | `season *int32`; three CEL rules; `PlannedBySeries()` | `+optional`, `Minimum=0`; rules in §3.2 |
| `SearchStatus` | `otherTitles int32`; `seasons []SearchSeason` | `Minimum=0`, `Maximum=200`; `listType=map`, `listMapKey=number`, `MaxItems=200` |
| `SearchSeason` (new) | `number`, `phase`, `reason`, `results`, `approved`, `grabbed` | `Minimum=0` on every count |
| `SeriesStatus` | `seasonSearch *SeasonSearchState` (catalog-grab); `lastSearchedAt` gains its writer | `+optional` |
| `SeasonSearchState`, `SeasonSearchRequest`, `SeasonSearchSeason`, `SeasonSearchPhase`, `SeasonSearchReason` (new, `series_search_types.go`) | §4.2 | `seasons`: `listType=map`, `listMapKey=number`, `MaxItems=200`; name 253, uid 36; enums |
| `commonv1.ReleaseDecision` | `coversSeason bool` | `+optional` |
| `schema.SearchTask` | `Season *int32`; `SearchReasonSeason` | JSON-optional |
| `schema.SearchRecord` | `Season *int32`; header `Sub` `season-<n>`; `KeyOf` = `(Task, Sub)` | |
| `schema.ParsedFacts` | `Coverage *ReleaseCoverage` (`ReleaseCoverage`, `SeasonEpisode` new; `MaxCoverageNumbers` 1000 per list) | |
| `schema.Candidate` | `Season *int32` | |
| `schema.SearchRequest` | `Anime bool` | |
| `pkg/events` | `MsgIDForSeasonSearch` | |
| `pkg/records/agentrecords` | `SeasonSearchKey` | |
| `app/catalog/history` | `DispatchSubSeasonSearch`; `ParseDispatchID` reads `season-search/…` | |
| `pkg/decision` | `Identity.SeasonSearch`, `Identity.Namesakes`, `Target.Anime`, `Target.RuntimeByEpisode`, `RankKey.Anime`, `CoverageOf`, `Coverage`, `CompareEpisodeCount`, `ResultBand`/`BandOf`, `ReasonFullSeasonNotAired` | |

Every new status list is capped; every new string is capped or an enum; no float; no
`+kubebuilder:default` (the planner and the controller always write concrete values).

## 15. Not in Wave S

- An automatic season search: the wanted sweep, a season turned on, and `addOptions.searchForMissing`
  (unread on this branch) still search per episode (§17).
- Sonarr's per-season alias titles (v5's season-title aliases) and `MultiSeasonSpecification`.
- An interactive series search (Sonarr has none; the CEL rule refuses it).
- Comic issue packs by volume: their fan-out is unchanged.

## 16. Review Focus

1. **No episode twice.** Check the live-grab refusal (§6.3 rule 1) against every candidate path:
   season answers, episode answers, RSS candidates, a person's pick, a delayed grab come due. The
   in-pass `taken` set covers one pass; the refusal must cover the next pass of the same series
   search, where the first pass's entries are already live. An upgrade is the only exception.
2. **No burst.** Exactly one season task per Series in flight, every one behind a `search` pace
   slot and the ledger; nothing in Wave S publishes an Episode search; the interactive season
   search is one task. Count the publishes in `DecideSeasons` and the stage.
3. **The complete declaration on a Series.** `catalog-grab` now carries `pendingGrabs` (grab
   stage) and `seasonSearch` plus `lastSearchedAt` (search stage). Every path of the search
   stage on a Series, the deleting path included, must send all of its fields; a failed stage
   must keep the set from applying (`DeclaresSets`). Test against a Series that already has both.
4. **The fold is once.** An Episode folds a season answer only when `answeredAt` is newer than its
   `lastSearchedAt`, never moves `lastSearchedAt` back, and counts one attempt; a paced answer
   counts none; a replaced `seasonSearch` loses nothing the Episode needed.
5. **Identity modes do not leak.** `SeasonSearch` is set only by the season snapshot; the RSS
   matcher and the episode search are unchanged; `Namesakes` acts only on a yearless,
   title-matched release that neither ids nor an id query identified.
6. **Bands and the label.** The agent bands before its cap; the manager re-bands after item state;
   `otherTitles` counts exactly the trailing band. A wrong-season release of the right series
   lands under "N releases for other titles": does the label read right, or should the band split
   wrong-series from wrong-episode?
7. **The handshake.** A planned Search's outcome is copied before the Series replaces its state;
   a Search deleted mid-run cancels cleanly; a stalled state fails the Search, not the Series.

## 17. Owner question

**Should the wanted sweep search a season as one query when several of its episodes are due a
first live search?** Today a season turned on (or a series added with episodes monitored) sends
one live, paced query per episode per indexer, the first time each is searched. Sonarr does the
same only on a person's "search missing", where it groups a season's episodes into one season
search. Wave S's planner can serve a synthetic request (no Search object) when two or more
episodes of a season are due their first live search, and `addOptions.searchForMissing` could use
the same path. **Recommendation:** yes, as a follow-up after Wave S lands and the season
planner has run on the live cluster: it removes most first-time live queries, which is the cost
the 2026-10-06 incident was about, and needs no API beyond §14 (a request reason on
`SeasonSearchState`).

## As built (2026-10-07)

Deviations from the text above, each a ruling of the implementing session:

- **`SeasonSearchRequest.UID` is a `string`**, not `types.UID`: controller-gen cannot apply
  `MaxLength` to `types.UID` and refuses the type. The value is the Search's uid all the same.
- **The search worker's indexes live in a leaf, `app/catalog/worker/titleindex`**
  (`IndexMovieTitle`, `IndexSeriesTitleYear`, `IndexEpisodeSeriesSeason`, their value functions,
  `SeriesTitleKey`, `SeasonKey`, `SearchIndexes`), because the RSS matcher imports the search
  worker and the worker cannot import the matcher. `rssmatcher` re-exports the names (unchanged
  strings) and `rssmatcher.SearchIndexes`; the catalog domain declares
  `titleindex.SearchIndexes()`.
- **`SeasonPlan.Finals` is `[]searchplan.SeasonFinal`** (the season and the request it served),
  so the stage labels `clustarr_season_searches_total{trigger}` for a state replaced in the same
  pass. A trigger whose Search is gone is inferred: `season` for a one-season state.
- **Season facts** read the union of `status.seasons` and the Episodes' seasons; "wanted" counts a
  monitored, aired episode that is missing or below its cutoff (`wantedcron.CandidateOf`) **or**
  covered by a live video grab, since `CandidateOf` reads a Downloading episode as not wanted and
  `AllDownloading` could otherwise never be reached.
- **"Back of the queue"** for a season every indexer paced: the next due season is the lowest
  `Queued` one with the fewest attempts, then by number; the map-keyed list is not reordered.
  A season reading `Searching` with no dispatch in flight is queued again rather than stall.
- **A planned Search stalls** (`Failed, Stalled`) also when the Series never takes it -- no state,
  or another request's finished state -- for `SeriesSearchStallTimeout`.
- **`FullSeasonNotAired` is exempt for a person's pick** in `grabplan.exempt` and the Search
  controller's `resolveGrab`, as `TranscodedFinal` is, so the manual exemption of
  `CoverageRejections` reaches a pick without `spec.override`.
- **The planner's per-episode re-check** adds, for an automatic candidate only, a `NotMonitored`
  and an `Availability` ("has not aired") rejection; the Search controller's season display runs
  the item-state rules alone, as an interactive search judges neither.
- **The in-pass `taken` set covers every episode a payload brings** (its coverage), not only the
  episodes it is taken for, so a pack narrowed past an episode still cannot bring it twice.
- **Pack preference:** `RankKey.EpisodeCount` counts absolute numbers too (an anime batch), and a
  season search's release covering every episode of the season ranks as whole. In the grab
  planner an anime batch is whole when its coverage names every aired episode of a season and
  more than one.
- **Season size:** a pack naming several whole seasons is measured as that many of the searched
  season, the only season whose runtimes the search knows.
- **The namesake message** names a series for an Episode target.
- No RBAC changed: the manager's role already granted `searches` reads and `series/status`.
