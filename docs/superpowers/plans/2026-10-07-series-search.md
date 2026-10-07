# Series and season search, and the interactive results order (Wave S) -- Implementation Plan, S1-S12

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or
> superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. **Execution mode: implement
> only**, as every wave on this branch: no task writes a test. Each task lists the tests it needs
> under **Tests (deferred to the batch)**, by Go test name with a one-line assertion; the test
> batch writes them. A task never weakens or deletes an existing test or guard; where its change
> breaks one, it names it there ("rewrite") and leaves the file as it is.

**Goal:** A person can search a whole series or one season, as Sonarr does, with one paced query
per season per indexer, packs and singles judged against the season's wanted episodes, a pack
preferred on a tie, no episode grabbed twice, and no per-episode burst; every interactive search
lists approved, then rejected, then "N releases for other titles" collapsed; and a yearless
release of a title two library items share is ambiguous.

**Architecture:** A `Search` on `mediaRef.kind: series` (with `spec.season` for one season) is
the request. With `grabBest` it is planned by the Series key: the remediation loop's search stage
gains a Series mode (`searchplan.DecideSeasons`) that serves one request at a time from
`Series.status.seasonSearch` (under `catalog-grab`), dispatches one season task at a time behind
the namespace's `search` pace slot and the dispatch ledger on `catalog-search-high`, and
incorporates each answer from `clustarr-searches` at `RecordSubKey(series uid, "season-<n>")`.
The catalog agent answers a season task in season-search identity mode and records each
release's scene-mapped coverage; the index agent sends `tvsearch&tvdbid&season` to id indexers
and the title to text-only ones (the season parameter only for non-anime). The Series' grab stage
judges the answers through `grabplan` (coverage, P23 narrowing, a live grab refusing the whole
release, `CompareEpisodeCount`). Without `grabBest` the Search controller runs one interactive
season task. Results are banded in the agent before its cap and in the Search controller after
the item-state rules.

**Spec:** `docs/superpowers/specs/2026-10-07-series-search-design.md` ("the design"; §n below is
its section). Research: `docs/research/arr-search-pipeline.md`. ADR-0019 and its design
(`docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`) for the loop, the
stages, records, dispatch and the Pacer.

---

## Global Constraints

**Inherited, verbatim in force:** the split plan's and the A-wave plan's Global Constraints
(`docs/superpowers/plans/2026-10-07-agents-report-over-nats.md`, "Global Constraints"): branch
hygiene R14; path-scoped commits, `git add` of every new file first, never `git add -A`, never a
bare `git commit`, never `git stash`, never `git reset --hard`, never `git push`; never `go get`
or `go mod tidy` (no task here adds a module); GPL header from `hack/boilerplate.go.txt` on every
new `.go` file, then a blank line, then `package` (snippets below omit it);
`$(go env GOPATH)/bin/gofumpt -w` and `$(go env GOPATH)/bin/goimports -local
github.com/mediactl/clustarr -w` on every touched `.go` file; RBAC markers move with the code that
needs the grant, and every task that adds a marker or an API type runs `make generate manifests`
and syncs each changed role into `charts/clustarr/templates/rbac.yaml`; R7 names never change;
build, don't deploy; no live-cluster action of any kind; no retired brand in any new text
(`make brand-gate`).

**Specific to Wave S:**

- **Implement-only gates.** Every task ends with `go build ./...` green, `make build` green, and
  `make generate manifests` leaving `git status --porcelain -- api config charts` empty after the
  task's own commit. A task that changes templ files runs `make templ` (and `make css` when it adds
  Tailwind classes) and commits the generated files.
- **Other sessions commit in this worktree.** Before a commit whose pathspec names a directory,
  run `git status --short -- <dir>` and stop if a file you did not touch is modified there.
- **Linkage.** `cmd/manager` never links ffgo, purego, onnx, anacrolix, `pkg/download/usenet` or
  `pkg/relindex`. No agent root (`app/catalog/agent/{catalog,events,metadata}`,
  `app/import/agent`, `app/indexer/agent`, `app/caption/agent`, `app/grab/agent/{torrent,usenet}`,
  `cmd/markers`, `cmd/transcode`) links `app/catalog/grabplan`, `app/catalog/searchplan`,
  `app/catalog/seriessearch`, `app/intake`, `app/dispatch` or `app/remediation`.
- **No burst (CLAUDE.md, 2026-10-06).** No code in this wave publishes an Episode search, and the
  season planner never has more than one season task in flight per Series. A task that seems to
  need either stops and reports.
- **Precondition for S10 and S11.** Main's `a245eaf7` (the season table, the episode modal,
  `ui/actions/search.go`, `GET /searches/{ns}/{name}`, CLAUDE.md's season-table text) must be on
  the branch: `git merge-base --is-ancestor a245eaf7 HEAD`. If it is not, the controlling session
  rebases the branch onto local main between tasks (R14) before S10; a task never rebases,
  merges or fetches.

## Order and dependencies

S1 → S2 → S3 → S5 → S6 → S7 → S8 → S9 → S10 → S11 → S12. S4 (`app/indexer/search` only) runs
beside S2 and S3. Shared files: `pkg/decision` (S2 then S3), `pkg/obs/metrics/domain.go` (S7
then S9), `api/` and `pkg/events/schema` (S1 only). Wave S runs after Wave N and before W10 and
the test batch (the A-wave plan's order note).

## Review Focus

The design's §16, items 1-7: no episode twice across passes; no burst; the complete
`catalog-grab` declaration on a Series; the once-only fold; identity modes that do not leak;
bands before the cap; the planned Search's handshake.

---

### Task S1: API and wire types

**Spec:** §3.2, §4.2, §4.6, §5.1, §6.1, §8.1, §8.2, §14.

**Files:**

- Create: `api/catalog/v1alpha1/series_search_types.go`
- Modify: `api/catalog/v1alpha1/search_types.go`, `api/catalog/v1alpha1/series_types.go`,
  `api/common/v1alpha1/release_types.go`, `pkg/events/schema/catalog.go`,
  `pkg/events/schema/records_catalog.go`, `pkg/events/schema/intake.go`,
  `pkg/events/schema/index.go`, `pkg/events/agents.go`,
  `pkg/records/agentrecords/agentrecords.go`, `app/catalog/history/dispatch.go`,
  `pkg/k8s/fieldmanager.go` (the `ManagerCatalogGrab` comment lists `status.seasonSearch` and a
  Series' `status.lastSearchedAt`)
- Generated: `api/catalog/v1alpha1/zz_generated.deepcopy.go`,
  `api/common/v1alpha1/zz_generated.deepcopy.go`, `api/applyconfiguration/...`,
  `config/crd/bases/catalog.clustarr.io_{searches,series}.yaml` and every CRD embedding
  `ReleaseDecision`

**Interfaces:**

- Produces:

  ```go
  package v1alpha1 // api/catalog

  // SearchSpec: three new XValidation rules (design §3.2, verbatim) and
  Season *int32 `json:"season,omitempty"` // +optional, Minimum=0
  func (s SearchSpec) PlannedBySeries() bool

  // SearchStatus gains:
  OtherTitles int32          `json:"otherTitles,omitempty"` // +optional, Minimum=0, Maximum=200
  Seasons     []SearchSeason `json:"seasons,omitempty"`     // listType=map, listMapKey=number, MaxItems=200
  type SearchSeason struct {
  	Number   int32              `json:"number"`
  	Phase    SeasonSearchPhase  `json:"phase"`
  	Reason   SeasonSearchReason `json:"reason,omitempty"`
  	Results  int32              `json:"results,omitempty"`
  	Approved int32              `json:"approved,omitempty"`
  	Grabbed  int32              `json:"grabbed,omitempty"`
  }

  // series_search_types.go, exactly the design's §4.2:
  type SeasonSearchState struct {
  	Request    SeasonSearchRequest  `json:"request"`
  	StartedAt  metav1.Time          `json:"startedAt"`
  	FinishedAt *metav1.Time         `json:"finishedAt,omitempty"`
  	Seasons    []SeasonSearchSeason `json:"seasons"` // listType=map, listMapKey=number, MaxItems=200
  	Dispatch   *Dispatch            `json:"dispatch,omitempty"`
  }
  type SeasonSearchRequest struct {
  	Name string    `json:"name"` // MaxLength=253
  	UID  types.UID `json:"uid"`  // MaxLength=36
  }
  type SeasonSearchPhase string  // Enum=Queued;Searching;Answered;Skipped;Failed;Canceled
  type SeasonSearchReason string // Enum=NotMonitored;NothingWanted;AllDownloading;NoSuchSeason;AllPaced;TimedOut;AgentFailed;SearchDeleted
  const (
  	SeasonSearchQueued    SeasonSearchPhase = "Queued"
  	SeasonSearchSearching SeasonSearchPhase = "Searching"
  	SeasonSearchAnswered  SeasonSearchPhase = "Answered"
  	SeasonSearchSkipped   SeasonSearchPhase = "Skipped"
  	SeasonSearchFailed    SeasonSearchPhase = "Failed"
  	SeasonSearchCanceled  SeasonSearchPhase = "Canceled"
  )
  const (
  	SeasonSearchNotMonitored   SeasonSearchReason = "NotMonitored"
  	SeasonSearchNothingWanted  SeasonSearchReason = "NothingWanted"
  	SeasonSearchAllDownloading SeasonSearchReason = "AllDownloading"
  	SeasonSearchNoSuchSeason   SeasonSearchReason = "NoSuchSeason"
  	SeasonSearchAllPaced       SeasonSearchReason = "AllPaced"
  	SeasonSearchTimedOut       SeasonSearchReason = "TimedOut"
  	SeasonSearchAgentFailed    SeasonSearchReason = "AgentFailed"
  	SeasonSearchSearchDeleted  SeasonSearchReason = "SearchDeleted"
  )
  type SeasonSearchSeason struct {
  	Number     int32              `json:"number"`
  	Phase      SeasonSearchPhase  `json:"phase"`
  	Reason     SeasonSearchReason `json:"reason,omitempty"`
  	Seq        int64              `json:"seq,omitempty"`
  	Attempts   int32              `json:"attempts,omitempty"` // Maximum=2
  	AnsweredAt *metav1.Time       `json:"answeredAt,omitempty"`
  	Candidates int32              `json:"candidates,omitempty"`
  	Approved   int32              `json:"approved,omitempty"`
  }
  func (p SeasonSearchPhase) Final() bool // Answered, Skipped, Failed, Canceled
  // SeriesStatus gains:
  SeasonSearch *SeasonSearchState `json:"seasonSearch,omitempty"`

  package v1alpha1 // api/common
  // ReleaseDecision gains:
  CoversSeason bool `json:"coversSeason,omitempty"`

  package schema
  const SearchReasonSeason SearchReason = "season"
  // SearchTask gains:   Season *int32 `json:"season,omitempty"`
  // SearchRecord gains: Season *int32 `json:"season,omitempty"`
  // ParsedFacts gains:  Coverage *ReleaseCoverage `json:"coverage,omitempty"`
  // Candidate gains:    Season *int32 `json:"season,omitempty"`
  // SearchRequest gains: Anime bool `json:"anime,omitempty"`
  const MaxCoverageNumbers = 1000
  type ReleaseCoverage struct {
  	FullSeasons []int32         `json:"fullSeasons,omitempty"`
  	Episodes    []SeasonEpisode `json:"episodes,omitempty"`
  	Absolute    []int32         `json:"absolute,omitempty"`
  }
  type SeasonEpisode struct {
  	Season  int32 `json:"season"`
  	Episode int32 `json:"episode"`
  }
  func SeasonSub(season int32) string // "season-<n>"

  package events
  // MsgIDForSeasonSearch is a season task's Msg-Id, "season-search/<series uid>/<season>/<seq>".
  func MsgIDForSeasonSearch(seriesUID string, season int32, seq int64) string

  package agentrecords
  // SeasonSearchKey is a season task's record key: RecordSubKey(series uid, schema.SeasonSub(n)).
  func SeasonSearchKey(seriesUID string, season int32) string
  // Searches().KeyOf becomes func(r) (string, string) { return r.Task, r.Sub }

  package history
  const DispatchSubSeasonSearch = "season-search"
  // ParseDispatchID reads "season-search/<uid>/<season>/<seq>" as
  // DispatchID{UID, Seq, Sub: DispatchSubSeasonSearch, Kind: "Series"}.
  ```

- [ ] **Step 1: Catalog types.** Add the design's §3.2 field, rules and method to `SearchSpec`,
  `OtherTitles` and `Seasons` (with `SearchSeason`) to `SearchStatus`, the new file's types, and
  `SeasonSearch` to `SeriesStatus` with the doc comments of design §4.2 and §4.6. No
  `+kubebuilder:default` on any of them. Every CEL rule guards `grabBest`, `grab` and `season`
  with `has()`.
- [ ] **Step 2: `ReleaseDecision.CoversSeason`** with design §8.1's comment.
- [ ] **Step 3: Wire types.** The schema fields above, all JSON-optional; `SearchTask` stays
  `catalog.SearchTask.v2` and `SearchRecord` `catalog.Search.v1` (additive). `ParsedFacts.Coverage`'s
  comment states the 1000-entry cap per list.
- [ ] **Step 4: Keys and ids.** `MsgIDForSeasonSearch`, `SeasonSearchKey`, `SeasonSub`,
  `Searches().KeyOf`, `DispatchSubSeasonSearch` and the `ParseDispatchID` case (four parts,
  `parts[0] == "season-search"`, `parts[2]` a non-negative number).
- [ ] **Step 5: Generate.** `make generate manifests`; `git add` the new file and the generated
  output; gates.

**Tests (deferred to the batch):**

- `TestSearchSeasonRules` (`pkg/crdcheck`, a dynamic client so absent fields stay absent): a
  `season` without kind series is refused; kind series with neither `season` nor `grabBest` is
  refused; kind series with `grabBest` and `spec.grab` is refused; `season: 0` with kind series
  is accepted, with and without `grabBest`.
- `TestSeasonSearchKeyIsAValidKVKeyAndInjective` (`pkg/records/agentrecords`, against the
  embedded NATS contract server): two seasons and two series give four distinct legal keys.
- `TestParseDispatchIDReadsASeasonSearch` (`app/catalog/history`).
- `TestSearchRecordKeyOfCarriesTheSub` (`pkg/records/agentrecords`): an episode record keys as
  before, a season record under its sub.
- Existing guards, unchanged and expected green: `TestEveryStatusListIsCapped`,
  `TestNoCRDDefaultIsUnreachableFromGo`.

- [ ] **Step 6: Commit.**

  ```bash
  git add api/catalog/v1alpha1/series_search_types.go
  git commit -m 'feat(api): series and season search -- Search spec.season with its CEL rules, status.seasons and otherTitles, Series status.seasonSearch, ReleaseDecision.coversSeason; the season task, record key, Msg-Id and coverage wire types (Wave S S1, tests deferred to the batch)' -- api config pkg/events/schema pkg/events/agents.go pkg/records/agentrecords app/catalog/history pkg/k8s/fieldmanager.go
  ```

---

### Task S2: `pkg/decision` -- season-search identity, coverage, namesakes

**Spec:** §6.1, §9.

**Files:**

- Modify: `pkg/decision/types.go`, `pkg/decision/identity.go`, `pkg/decision/identity_scene.go`,
  `pkg/decision/size.go`, `pkg/decision/evaluate.go`

**Interfaces:**

- Consumes: S1 (nothing from it directly; `pkg/decision` imports no schema).
- Produces:

  ```go
  package decision

  // Identity gains:
  SeasonSearch bool     // design §6.1: overlap, not cover-all; never with SingleEpisodeSearch
  Namesakes    []string // design §9

  // Target gains:
  Anime            bool        // the series is anime (CompareEpisodeCount, S3)
  RuntimeByEpisode map[int]int // a season search's episode number -> minutes

  type SeasonEpisode struct{ Season, Episode int }
  // Coverage is what a release covers in the series' stored numbering,
  // after its scene mapping.
  type Coverage struct {
  	FullSeasons []int
  	Episodes    []SeasonEpisode
  	Absolute    []int
  }
  // CoverageOf is releaseCoverage, exported: p read through the scene table.
  func CoverageOf(p *release.ParsedRelease, scene []SceneMapping) Coverage

  // Decision gains:
  Coverage *Coverage // set for Episode and Series targets whose title parsed
  ```

- [ ] **Step 1: Season-search numbering.** In `identity_scene.go` add `coversAnyInSeason(season
  int, episodes []int) bool` (a pack of the season, or any listed episode) and
  `coversAnyAbsolute([]int) bool`; `numberingRejection` uses them when `id.SeasonSearch`, the
  cover-all forms otherwise. Messages name "the season" in that mode.
- [ ] **Step 2: Coverage.** Export the coverage as `Coverage` and `CoverageOf` (the internal
  `coverage` keeps its maps; a method renders the sorted exported form). `Evaluate` (and
  `EvaluateRelease`) set `Decision.Coverage` from the identity index's scene table it already
  builds once per call, for `MediaKindEpisode` and `MediaKindSeries` targets.
- [ ] **Step 3: Size for a season search.** `targetRuntimeMinutes`: when
  `t.Identity.SeasonSearch` and `t.RuntimeByEpisode` is set, sum the runtimes of the covered
  episodes of the searched season (45-minute fallback per unknown episode); a full-season pack
  sums every entry. Every other path is unchanged.
- [ ] **Step 4: Namesakes.** `newIdentityIndex` keys `id.Namesakes` with `titleKey` into a set.
  In `itemRejection`'s title loop, a match whose key is in that set, for a release with
  `parsed.Year < minPlausibleYear`, is `ReasonUnknownItem` with the design's §9 message; a match
  on the series' `<title><year>` key is not a namesake key, so a year in the title passes. Steps 1
  and 2 of `itemRejection` (ids, the id-query vouch) run first and are unchanged.

**Tests (deferred to the batch):**

- `TestSeasonSearchAcceptsAnySingleOfTheSeason` (`pkg/decision`): S01E05 and a two-episode
  release pass in season mode; S02E01 is `WrongItem`; a multi-season pack naming S01 passes.
- `TestSeasonSearchReadsSceneNumbers`: an anime scene-numbered single of the season, through a
  TheXEM table, passes; an absolute batch overlapping the season passes.
- `TestSingleEpisodeSearchIsUnchanged`: the existing R-3 corpus with `SeasonSearch` false.
- `TestCoverageOfMatchesTheNumberingHalf`: for the existing identity corpus, the episodes
  `CoverageOf` names are those `numberingRejection` accepts.
- `TestASeasonSearchSizesBySumOfCoveredRuntimes` (`size_test.go`).
- `TestAYearlessNamesakeIsUnknownItem`: "Monster.S01E01" against Monster (2004) with namesake
  "Monster" is `UnknownItem`; "Monster.2004.S01E01" passes; the same release from an id-query
  indexer passes; a movie "Dune" with no year and namesake "Dune" is `UnknownItem`, "Dune 2021"
  passes.

- [ ] **Step 5: Commit.**

  ```bash
  git commit -m 'feat(decision): season-search identity accepts any episode of the season, every episode decision carries its scene-mapped coverage, a season search sizes by the covered runtimes, and a yearless title-only match on a library namesake is UnknownItem (Wave S S2, tests deferred to the batch)' -- pkg/decision
  ```

---

### Task S3: `pkg/decision` -- `CompareEpisodeCount`, rank keys for rejected releases, bands, `FullSeasonNotAired`

**Spec:** §6.3 rule 2, §6.4, §8.2.

**Files:**

- Create: `pkg/decision/bands.go`
- Modify: `pkg/decision/rank.go`, `pkg/decision/types.go`, `pkg/decision/evaluate.go`,
  `pkg/decision/reasons.go`

**Interfaces:**

- Produces:

  ```go
  package decision

  // CompareEpisodeCount is Sonarr's DownloadDecisionComparer.CompareEpisodeCount
  // (design §6.4): <0 when a ranks first.
  func CompareEpisodeCount(aWhole bool, aCount int, bWhole bool, bCount int, anime bool) int

  // RankKey gains:
  Anime bool

  var ReasonFullSeasonNotAired = Reason{"FullSeasonNotAired", common.RejectionPermanent}
  // FullSeasonNotAired is the rejection of a full-season release of season,
  // which has an episode not yet aired (Sonarr's FullSeasonSpecification).
  func FullSeasonNotAired(season int) common.Rejection

  type ResultBand int
  const (
  	BandApproved ResultBand = iota
  	BandRejected
  	BandOtherTitle
  )
  // BandOf: approved; else other-title when any rejection is WrongItem,
  // UnknownItem or UnableToParse; else rejected.
  func BandOf(approved bool, rejs []common.Rejection) ResultBand
  func (b ResultBand) String() string // "approved", "rejected", "otherTitles" (the metric label)
  ```

- [ ] **Step 1: The comparator.** `less` replaces its `EpisodeCount` step with
  `CompareEpisodeCount(whole(a), a.Rank.EpisodeCount, whole(b), b.Rank.EpisodeCount,
  a.Rank.Anime)`, where `whole` reads `EpisodeCount`'s season-pack sentinel. `Evaluate` sets
  `RankKey.Anime` from `Target.Anime`.
- [ ] **Step 2: Rank keys for rejected releases.** `Evaluate` fills `RankKey` for every decision
  whose title parsed, approved or not (the comment on `RankKey` says so). `Rank` is unchanged;
  callers still rank bands separately.
- [ ] **Step 3: Bands.** `bands.go` with `ResultBand`, `BandOf` (through `Reason.Of` for the three
  codes) and `String`.
- [ ] **Step 4: `FullSeasonNotAired`.** The reason and its constructor in `reasons.go` ("full
  season pack of season %d, which has not finished airing"). It is not run by `Evaluate`: the
  grab planner and the Search controller apply it from item state (S8, S9).

**Tests (deferred to the batch):**

- `TestCompareEpisodeCountIsSonarrs` (`pkg/decision`): a pack beats singles on a tie; a
  standard series prefers a single over a two-episode release; anime prefers the two-episode
  release.
- `TestRankPutsAPackFirstOnATie` (`rank_test.go`) and `TestRankKeepsQualityAheadOfPacks`.
- `TestRejectedDecisionsCarryARankKey` (`evaluate_test.go`).
- `TestBandOfPutsIdentityRejectionsLast` (`bands_test.go`): `WrongItem`, `UnknownItem`,
  `UnableToParse` are other titles, `FullSeason` and quality rejections are rejected.
- Rewrite: any `rank_test.go` case asserting "more episodes first" for a standard series.

- [ ] **Step 5: Commit.**

  ```bash
  git add pkg/decision/bands.go
  git commit -m 'feat(decision): Sonarr'"'"'s CompareEpisodeCount -- a pack first on a tie, then anime more episodes and other series fewer; rejected decisions carry rank keys; result bands (approved, rejected, other titles); FullSeasonNotAired (Wave S S3, tests deferred to the batch)' -- pkg/decision
  ```

---

### Task S4: The index agent's season query

**Spec:** §5.3. **Parallel with:** S2, S3.

**Files:**

- Modify: `app/indexer/search/query.go`

**Interfaces:**

- Consumes: S1 `schema.SearchRequest.Anime`.
- Produces: `buildQuery` and `isAnimeRequest` as below; no new symbol.

- [ ] **Step 1: Id half first.** In `buildQuery`, set the id parameters and `anyID` before the
  season and episode parameters.
- [ ] **Step 2: The season parameter.** Set `q.Season` (where `paramSupported(caps, mode,
  "season")`) only when `anyID`, or when the query falls to text and the request is not anime.
  Set `q.Episode` as today.
- [ ] **Step 3: Anime.** `isAnimeRequest(req)` is `req.Anime || <today's absolute shape>`, so an
  anime season request adds `spec.animeCategories` too. Update `buildQuery`'s doc comment with the
  design's §5.3 table.

**Tests (deferred to the batch):**

- `TestBuildQueryForASeason` (`app/indexer/search`, table): an id indexer gets
  `tvdbid&season`, no `q`, for both series types; a text indexer listing `season` gets
  `q=<title>&season` for a standard series and `q=<title>` without season, with the anime
  categories, for anime; a text indexer without `season` gets `q=<title>`.
- `TestAnEpisodeQueryIsUnchanged`: today's episode and anime-episode cases.

- [ ] **Step 4: Commit.**

  ```bash
  git commit -m 'feat(index): a season request queries tvsearch&tvdbid&season where the indexer takes ids, the title with the season parameter on a text-only indexer, and the title alone with its anime categories for an anime series (Wave S S4, tests deferred to the batch)' -- app/indexer/search/query.go
  ```

---

### Task S5: The search agent -- season tasks, namesakes, coverage, bands

**Spec:** §5.2, §6.1, §8.2 (the agent half), §9 (where the agent gets it).

**Files:**

- Create: `app/catalog/worker/search/season.go`, `app/catalog/worker/search/namesakes.go`
- Modify: `app/catalog/worker/search/{worker.go,snapshot.go,request.go,rank.go,identity.go}`,
  `app/catalog/worker/rssmatcher/index.go` (`IndexMovieTitle` and `SearchIndexes`),
  `app/catalog/agent/catalog/register.go` (declares `rssmatcher.SearchIndexes()`)

**Interfaces:**

- Consumes: S1 schema and keys, S2 `Identity.{SeasonSearch,Namesakes}`, `Target.{Anime,
  RuntimeByEpisode}`, `Decision.Coverage`, S3 `BandOf`.
- Produces:

  ```go
  package search // app/catalog/worker/search

  // SeasonSearchable reports a task the worker answers as a season search:
  // MediaRef kind series with Season set.
  func SeasonSearchable(task schema.SearchTask) bool
  // snapshotSeason reads the Series and the season's Episodes
  // (rssmatcher.IndexEpisodeSeriesSeason) and builds the season snapshot:
  // IDs (tvdb, scene season, Episode nil, Anime, Title, Year), Target
  // (Kind series, Monitored, Available, Anime, RuntimeByEpisode,
  // Identity = EpisodeIdentity(series, every episode of the season) with
  // SeasonSearch, SceneMappings and Namesakes) and Owner = the Series.
  func (w *Worker) snapshotSeason(ctx context.Context, ns string, ref commonv1.MediaRef, season int32) (itemSnapshot, error)
  // SceneSeason is the scene season most of eps map to in scene, the stored
  // season when none maps.
  func SceneSeason(scene []decision.SceneMapping, season int32, eps []*catalogv1alpha1.Episode) int32
  // Namesakes are item's titles another Movie or Series in ns also carries
  // (design §9), through rssmatcher.IndexMovieTitle and IndexSeriesTitleYear.
  func Namesakes(ctx context.Context, c client.Reader, ns string, item client.Object) ([]string, error)

  package rssmatcher
  const IndexMovieTitle = "rssmatcher.clustarr.io/movie-title" // release.CleanTitle of every title, yearless
  // SearchIndexes are the indexes the catalog domain's search worker reads:
  // IndexMovieTitle, IndexSeriesTitleYear, IndexEpisodeSeriesSeason.
  func SearchIndexes() []k8s.FieldIndex
  ```

- [ ] **Step 1: Indexes.** Add `IndexMovieTitle` (extractor: `release.CleanTitle` of the
  title, original title and every alternate title, deduplicated) to `FieldIndexes()` (fourteen
  now) and `SearchIndexes()`. The catalog domain's `Register` returns
  `catalogagent.Registration{Indexes: rssmatcher.SearchIndexes()}`; the agent registers each
  index once whatever domains it runs.
- [ ] **Step 2: Accept a season task.** `handleSearchTask`: a task with `SeasonSearchable` takes
  the season path (`snapshotSeason`); a kind-series task without `Season` fails its record with
  "a series search names a season". The answer's key is `agentrecords.SeasonSearchKey(item uid,
  season)` for an automatic season task (header `Sub` = `schema.SeasonSub(n)`, `Season` set) and
  the Search's UID for an interactive one (`Season` set); the superseded check reads that key.
  `answer` carries the full KV key and the sub.
- [ ] **Step 3: The request.** `BuildSearchRequest`, episode branch: `req.Anime = ids.Anime`;
  `req.Season = ids.Season` unless the request is an anime episode request (Episode set), so an
  anime season keeps its season for id indexers. `resolvedText` already returns the title alone
  when `Episode` is nil; its comment says so for a season.
- [ ] **Step 4: Namesakes everywhere.** `snapshot` (Movie, Episode) and `snapshotSeason` fill
  `Identity.Namesakes` through `Namesakes` (an Episode's from its Series). A lookup error is
  logged and leaves `Namesakes` empty: no namesake known is today's behaviour, never a failed
  search. `Target.Anime` is set for Episodes too.
- [ ] **Step 5: Coverage and bands in the record.** `parsedFacts` copies `Decision.Coverage` into
  `ParsedFacts.Coverage` (each list clamped to `schema.MaxCoverageNumbers`). `RankAndCap` orders
  approved (`decision.Rank`), rejected (`decision.Rank`), then other titles (arrival order), by
  `decision.BandOf`, **before** the cap.

**Tests (deferred to the batch):**

- `TestASeasonTaskAnswersAtItsSeasonKey` (`app/catalog/worker/search`, envtest): a season
  task's record is at `SeasonSearchKey`, with `Season` and `Sub`, and a series task without a
  season fails its record.
- `TestASeasonRequestIsTitleAloneWithTheSceneSeason` (`request_test.go`): `Season` is the scene
  season, `Episode` nil, `Text` the title, `Anime` set for anime with the season kept.
- `TestSceneSeasonIsTheMajorityMapping`.
- `TestNamesakesFindsAnotherLibraryItemByTitle` (envtest): two Series titled "Monster"; a Movie
  pair "Dune"; an unrelated title has none.
- `TestRankAndCapBandsBeforeTheCap` (`rank_test.go`): with 250 decisions, 60 of them
  `WrongItem`, the cap drops other titles first.
- `TestTheRecordCarriesCoverage`.
- Rewrite: `rank_test.go` cases asserting rejected releases in arrival order.

- [ ] **Step 6: Commit.**

  ```bash
  git add app/catalog/worker/search/season.go app/catalog/worker/search/namesakes.go
  git commit -m 'feat(search): the catalog agent answers a season task -- season-search identity over the season'"'"'s episodes, the scene season and the title alone on the wire, its record under the series'"'"' season key; every answer carries coverage, library namesakes and approved, rejected, other-title bands before the cap (Wave S S5, tests deferred to the batch)' -- app/catalog/worker/search app/catalog/worker/rssmatcher/index.go app/catalog/agent/catalog/register.go
  ```

---

### Task S6: `searchplan` -- the season planner, and the Episode's fold

**Spec:** §4.3-§4.5, §7.

**Files:**

- Create: `app/catalog/searchplan/season.go`
- Modify: `app/catalog/searchplan/plan.go`, `app/catalog/searchplan/doc.go`

**Interfaces:**

- Consumes: S1 types and keys; `dispatch.Decision`, `dispatch.Render`, `dispatch.Key`.
- Produces:

  ```go
  package searchplan // pure

  // SeasonDurable is the consumer every season task is admitted to: a person
  // is waiting, so the high lane.
  const SeasonDurable = events.ConsumerCatalogSearchHigh
  // SeriesSearchStallTimeout fails a planned Search whose served state shows
  // no transition for this long (the Search controller reads it).
  const SeriesSearchStallTimeout = 30 * time.Minute

  // SeasonRequest is one open planned Search naming the series.
  type SeasonRequest struct {
  	Search   catalogv1alpha1.SeasonSearchRequest
  	Season   *int32
  	Created  time.Time
  	Deleting bool
  }
  // SeasonFacts is one season of the series as the planner reads it.
  type SeasonFacts struct {
  	Number      int32
  	Monitored   bool // the series and at least one episode of the season
  	Wanted      int  // monitored, aired, missing or below its cutoff
  	Downloading int  // of Wanted, covered by a live entry
  }
  type SeasonView struct {
  	Series     schema.ItemRef
  	Now        time.Time
  	Monitored  bool
  	Anime      bool
  	Profile    string
  	Protocols  []commonv1.Protocol
  	Priorities map[string]int32
  	Scope      string
  	State      *catalogv1alpha1.SeasonSearchState
  	// Requests are the open planned Searches, oldest first.
  	Requests []SeasonRequest
  	// ServedSettled: the state's Search reads Completed or Failed, or is gone.
  	ServedSettled bool
  	// ServedGone: the state's Search is gone or deleting.
  	ServedGone bool
  	Seasons    []SeasonFacts
  	// Records are the records at each season's key, any seq.
  	Records      map[int32]*schema.SearchRecord
  	LastSearched *metav1.Time
  	InFlight     *Task
  	Key          dispatch.Key
  }
  type SeasonPlan struct {
  	State          *catalogv1alpha1.SeasonSearchState
  	LastSearchedAt *metav1.Time
  	Publish        *Task
  	Answered       bool
  	AnsweredSeq    int64
  	Waiting        dispatch.Reason
  	Due            time.Time
  	// Started: a request was taken this pass; Finals: the seasons that
  	// reached a final phase this pass (the metric and the Events).
  	Started bool
  	Finals  []catalogv1alpha1.SeasonSearchSeason
  }
  // DecideSeasons is the Series key's season plan (design §4.3, §4.4).
  // pace reserves the season's slot in the namespace's search class.
  func DecideSeasons(v SeasonView, admit func(durable string, seq int64) dispatch.Decision, pace func(season int32) time.Time) SeasonPlan

  // View gains (the Episode's fold, design §7):
  SeasonPending    bool
  SeasonAnsweredAt *metav1.Time
  ```

- [ ] **Step 1: Taking a request.** With no state, or a finished state whose `ServedSettled`,
  take the oldest non-deleting request: list its seasons (the named one, or every `Seasons`
  number but 0), each `Queued` or final at once as `Skipped` with the design's §4.3 reason
  (`NoSuchSeason`, `NotMonitored`, `NothingWanted`, `AllDownloading`); `StartedAt = now`.
  `Started = true`.
- [ ] **Step 2: Canceling.** `ServedGone` on an unfinished state: the in-flight dispatch closes
  `Withdrawn` (the plan's `Answered` and `AnsweredSeq` tell the ledger), the `Searching` season
  and every `Queued` one become `Canceled, SearchDeleted`, and the state finishes; a record that
  arrives later is not incorporated.
- [ ] **Step 3: Incorporating.** As `Decide` step 1, at the `Searching` season's record when its
  seq equals the dispatch's: `Answered` with counts, `answeredAt` from `FinishedAt`; a failed
  record `Failed, AgentFailed`; `AllPaced` with `attempts` 1 back to `Queued` at the end of the
  order (`attempts` 2 on its next dispatch), with `attempts` 2 `Answered, AllPaced`.
- [ ] **Step 4: Timeout and republish.** `Decide` steps 2 and 3, with the season `Failed,
  TimedOut` on timeout; a republish rebuilds the task from the state when `InFlight` is nil.
- [ ] **Step 5: Dispatch.** With nothing in flight and a `Queued` season: `pace(n)` (more than a
  second away: `Waiting = Paced`, `Due` = the slot, the dispatch's delivery rendered waiting);
  then `admit(SeasonDurable, seq)`; admitted, the season goes `Searching` with its `seq`, a new
  `Dispatch{Seq, AnsweredSeq: previous, DispatchedAt}` rendered with `dispatch.Render`, and
  `Publish` = the season task: `MediaRef{Kind: series}`, `Item` = the Series, `Season`,
  `Reason: SearchReasonSeason`, `UserInvoked`, profile, protocols, priorities, scope; subject
  `WorkSearchSubject(PriorityHigh, MediaKey("series", ns, name))`; Msg-Id
  `MsgIDForSeasonSearch`; `Live: true`. Never more than one season `Searching`.
- [ ] **Step 6: Finish.** Every season final: `FinishedAt = now`, `LastSearchedAt` = the newest
  `answeredAt` (or the stored one). `Finals` lists each season that became final this pass.
- [ ] **Step 7: The Episode's fold.** `Decide`, before step 2: when `SeasonAnsweredAt` is set,
  newer than `LastSearched`, `v.Wanted`, and no dispatch is in flight, set `LastSearchedAt` to it
  and bump `Attempts` at it (once: the next pass sees `LastSearched` equal). In `purposeDue`, a
  video search (not a redownload, not a donor) is not due while `SeasonPending`; `Due` stays
  zero (S7's wake brings the pass).

**Tests (deferred to the batch):**

- `TestDecideSeasonsDispatchesOneSeasonAtATime` (`app/catalog/searchplan`): three queued
  seasons publish one task per pass, each only after the previous answer.
- `TestDecideSeasonsWaitsForItsPaceSlot` and `TestDecideSeasonsWaitsForAdmission`.
- `TestDecideSeasonsSkipsWhatCannotGrab`: unmonitored, nothing wanted, all downloading, no such
  season, specials omitted from a whole-series request.
- `TestDecideSeasonsRequeuesAnAllPacedSeasonOnce`.
- `TestDecideSeasonsTimesOutAndGoesOn`.
- `TestDecideSeasonsCancelsWhenTheSearchIsDeleted`.
- `TestDecideSeasonsServesRequestsInOrderAfterTheHandshake`: a second request waits until the
  first's Search reads Completed.
- `TestTheEpisodeFoldsASeasonAnswerOnce` and `TestAPacedSeasonAnswerCountsForNoEpisode`.
- `TestAnEpisodeWaitsForItsPendingSeason` (a redownload does not wait).

- [ ] **Step 8: Commit.**

  ```bash
  git add app/catalog/searchplan/season.go
  git commit -m 'feat(search): the season planner -- one paced, admitted season task in flight per series, requests served in order after their Search settles, skipped, paced, timed-out and canceled seasons; an Episode folds its season'"'"'s answer once and waits while its season is pending (Wave S S6, tests deferred to the batch)' -- app/catalog/searchplan
  ```

---

### Task S7: The search stage on a Series; sources, ledger and wakes

**Spec:** §4.1-§4.5, §7 (the wake), §11 (`clustarr_season_searches_total`, `SeasonSearchFailed`).

**Files:**

- Create: `app/catalog/seriessearch/{doc.go,index.go}`, `app/remediation/search/season.go`
- Modify: `app/remediation/search/{stage.go,sources.go,doc.go}`, `app/remediation/indexes.go`
  (registers `seriessearch`), `app/remediation/itemsources.go` (S24 gains the season signature),
  `app/catalog/controller/rollup/entries.go` (`SeasonSearchSignature`),
  `app/remediation/manager/register.go` (`SeasonLedgerSource`), `pkg/obs/metrics/domain.go`

**Interfaces:**

- Consumes: S1, S6; `remediation.{ItemStage, Watcher, SetDeclarer, DispatchPublish,
  DispatchAnswered, ItemEvent}`; `dispatch.{Ledger, Source, Outstanding}`; `records.SearchClass`.
- Produces:

  ```go
  package seriessearch // leaf: the manager's index of planned Searches by series

  // IndexSearchesBySeries indexes a Search whose spec.PlannedBySeries() by
  // spec.mediaRef.name.
  const IndexSearchesBySeries = "remediation.search.series"
  func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error
  // Of lists ns's planned Searches naming series, oldest first.
  func Of(ctx context.Context, r client.Reader, ns, series string) ([]catalogv1alpha1.Search, error)

  package search // app/remediation/search
  // Applies: the searchable kinds and KindSeries. DeclaresSets(KindSeries)
  // returns catalog-grab (seasonSearch, lastSearchedAt).
  func (s *Stage) planSeries(ctx context.Context, env *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error)
  // SeasonLedgerSource rebuilds catalog-search-high's outstanding season
  // dispatches from every Series' status.seasonSearch.dispatch.
  type SeasonLedgerSource struct{}

  package rollup
  // SeasonSearchSignature is a Series' season search as season -> phase.
  func SeasonSearchSignature(s *catalogv1alpha1.SeasonSearchState) uint64
  // SeasonsChanged are the seasons whose phase differs between o and n.
  func SeasonsChanged(o, n *catalogv1alpha1.SeasonSearchState) []int32

  package metrics
  SeasonSearchesTotal = newCounterVec("clustarr_season_searches_total",
  	"Season searches that reached a final phase, by trigger and outcome.", "trigger", "outcome")
  ```

- [ ] **Step 1: The index.** `seriessearch` (no imports from `app/remediation`), registered from
  `remediation.RegisterIndexes`.
- [ ] **Step 2: Gather a Series.** `planSeries` gathers `searchplan.SeasonView`: the Series'
  monitoring, profile (`itemread.ConfigOf`, `itemread.Profile`), protocols, priorities, scope
  (`schema.BlockScopeOf`), `status.seasonSearch`; the requests (`seriessearch.Of`, mapped with
  `Deleting` from `deletionTimestamp`); `ServedSettled` and `ServedGone` from the served Search
  in the cache (gone, deleting, or phase `Completed`/`Failed`); per season (`status.seasons`) the
  facts from the Episodes (`series.EpisodeBySeriesRefIndex`): monitored, wanted
  (`wantedcron.CandidateOf(ep).Reason != ""`, aired by `itemread.Available`), downloading
  (`itemread.Covers` over the live entries of this pass's downloads outcome); the record at each
  listed season's `SeasonSearchKey`; the publish book's task for the dispatch in flight. Then
  `searchplan.DecideSeasons` with `admit` = `Ledger.Admit(SeasonDurable, key, seq)` and `pace` =
  `env.Pacer.Reserve(records.SearchClass(ns), "<series uid>/season-<n>")`.
- [ ] **Step 3: The result.** The `catalog-grab` set's `seasonSearch` and `lastSearchedAt`
  (both always sent, `nil` when absent: a complete declaration); `DispatchAnswered` on
  incorporation or withdrawal; the `DispatchPublish` (with `Pace` and `PaceKey`) of the task;
  `Waiting` to the ledger; `Due`. Per `Finals` entry, `metrics.SeasonSearchesTotal` (`trigger`
  `series` or `season` by the request, `outcome` from phase and reason) and, for `Failed`, a
  Warning `ItemEvent` `SeasonSearchFailed` (recorder `series`). A deleting Series re-sends its
  stored set and dispatches nothing.
- [ ] **Step 4: The Episode's inputs.** `gather` for an Episode reads its Series'
  `status.seasonSearch`: `SeasonPending` when its season is `Queued` or `Searching`;
  `SeasonAnsweredAt` when its season is `Answered` with no `AllPaced` reason.
- [ ] **Step 5: Sources.** In `Sources`: S31, a `Source{Name: "S31/Search", Object:
  &catalogv1alpha1.Search{}}` whose handler enqueues the named Series key at `PriorityUser` on any
  create, update or delete of a Search with `PlannedBySeries()`. S29's mapping is unchanged (a
  season record's item is the Series). In `itemsources.go`, S24's Series branch also compares
  `rollup.SeasonSearchSignature` and enqueues, at low priority, the Episode keys of
  `rollup.SeasonsChanged`.
- [ ] **Step 6: The ledger source.** `SeasonLedgerSource{}` (`Durable() = searchplan.SeasonDurable`,
  `Rebuild` lists Series with an in-flight `seasonSearch.dispatch`, key `{Series, ns, name,
  history.DispatchSubSeasonSearch}`), registered beside `LedgerSource` in
  `app/remediation/manager/register.go`.
- [ ] **Step 7: RBAC.** The stage's markers gain `searches` `get;list;watch` and `series/status`
  `get;update;patch`. `make manifests`; sync the manager role into the chart.

**Tests (deferred to the batch):**

- `TestASeriesSearchPlansOneSeasonAtATime` (`app/remediation/search`, envtest with the loop):
  a planned Search on a three-season Series publishes one high-lane task per answer, each behind
  a pace slot; the Search's record wakes the Series.
- `TestTheSeriesSetIsCompleteOnEveryPath` (envtest against a Series that already has
  `pendingGrabs` and `seasonSearch`): a failed grab stage or search stage applies none of
  `catalog-grab`'s set; a deleting Series keeps both; `managedFields` show `catalog-grab` owning
  exactly its paths.
- `TestS31WakesTheSeriesOnAPlannedSearch` and `TestS24WakesTheChangedSeasonsEpisodes`.
- `TestTheSeasonLedgerRebuildsAfterALeaderChange`.
- `TestNoEpisodeSearchIsPublishedForASeason` (a guard: no `WorkSearchSubject` call in
  `season.go` names an Episode).
- `TestCatalogueMatchesTheAmendmentTable` (rewrite): `wantSeries` gains
  `clustarr_season_searches_total`.

- [ ] **Step 8: Commit.**

  ```bash
  git add app/catalog/seriessearch app/remediation/search/season.go
  git commit -m 'feat(remediation): the Series key plans a person'"'"'s series or season search -- the search stage'"'"'s Series mode over status.seasonSearch, planned Searches found by index and woken by S31, the season ledger source on catalog-search-high, S24 waking the changed seasons'"'"' episodes, clustarr_season_searches_total (Wave S S7, tests deferred to the batch)' -- app/catalog/seriessearch app/remediation app/catalog/controller/rollup/entries.go pkg/obs/metrics/domain.go config charts/clustarr/templates/rbac.yaml
  ```

---

### Task S8: `grabplan` and the grab stage -- coverage, no episode twice, pack preference

**Spec:** §6.2-§6.4.

**Files:**

- Create: `app/catalog/grabplan/coverage.go`
- Modify: `app/catalog/grabplan/{plan.go,candidates.go,narrow.go,doc.go}`,
  `app/remediation/grab/gather.go`

**Interfaces:**

- Consumes: S1 (`Candidate.Season`, `ParsedFacts.Coverage`, `SeasonSearchState`), S3
  (`CompareEpisodeCount`, `FullSeasonNotAired`, `ReasonQueueHigherPreference`).
- Produces:

  ```go
  package grabplan

  // Candidate gains:
  Season *int32 // the season a season answer or a season search's pick is for
  // View gains:
  Anime bool

  // FromSeasonRecord turns a season answer into candidates: each approved
  // release, GrabbedBy, Season set, NoDelay (a person asked).
  func FromSeasonRecord(rec *schema.SearchRecord, season int32, grabbedBy commonv1.GrabSource) []Candidate

  // ItemCheck is one covered item's re-check for CoverageRejections.
  type ItemCheck struct {
  	// Rejections are the item's item-state rejections (and not monitored,
  	// not aired, for an automatic candidate).
  	Rejections []commonv1.Rejection
  	// Live: a live video entry covers the item and the release does not
  	// upgrade it.
  	Live bool
  	// Unaired: the item has not aired.
  	Unaired bool
  }
  // CoverageRejections folds the covered items into the release's verdict
  // (design §6.3): nil takes it, narrowed to the items with no rejection;
  // otherwise the rejections that refuse it -- QueueHigherPreference when
  // any item is Live (exempt with override), FullSeasonNotAired for a
  // full-season release with an Unaired item (exempt when manual), else the
  // first item's when every item is rejected.
  func CoverageRejections(items []ItemCheck, season int32, fullSeason, manual, override bool) []commonv1.Rejection
  ```

- [ ] **Step 1: Coverage.** `coverage(c)` on a Series: with `Release.Parsed.Coverage`, every
  covered Episode it names (`FullSeasons`, `Episodes`, `Absolute` against `Covered.Absolute`)
  plus `Targets`; without it, today's rule, and for a `Season` candidate the searched season's
  episodes its parsed facts name (`FullSeason`: all). `FromParked` copies `Season`.
- [ ] **Step 2: The verdict.** `check` builds an `ItemCheck` per covered item (`Live` from the
  live video entries covering it and `quality.Profile.UpgradeDecision` against each entry's
  release, `Unaired` from `!Covered.Wanted`) and calls `CoverageRejections`; a refusal settles
  `refused` under decision `overlapsLive` or `seasonNotAired`; the narrowed items are the ones
  with no rejection (P23 unchanged otherwise).
- [ ] **Step 3: More than an entry can name.** A candidate whose narrowed items exceed 200
  episodes or issues is refused (`tooManyEpisodes`); `numbers` no longer truncates.
- [ ] **Step 4: Pack preference.** `better` replaces its items-count step with
  `decision.CompareEpisodeCount(whole(a), len(a.items), whole(b), len(b.items), d.v.Anime)` for a
  Series (whole: `Parsed.FullSeason`, or the coverage names every aired episode of a season);
  a Comic keeps more-first.
- [ ] **Step 5: Gather the season answers.** `gather` sets `View.Anime` from the Series' type and,
  for each `Answered` season of `status.seasonSearch` whose `answeredAt` is inside
  `CandidateWindow`, reads `agentrecords.SeasonSearchKey(series uid, n)`; a record at the
  season's `seq` becomes `FromSeasonRecord(rec, n, GrabSourceSearch)`.
- [ ] **Step 6: Metrics.** `Plan.Decisions` gains `overlapsLive`, `seasonNotAired`,
  `tooManyEpisodes`, counted through the existing `clustarr_search_decisions_total{reason="grabplan"}`.

**Tests (deferred to the batch):**

- `TestASeasonAnswerGrabsAPackOverSinglesOnATie` (`app/catalog/grabplan`).
- `TestBetterSinglesWinAndThePackIsThenRefused`: singles at a higher tier are grabbed and the
  pack overlapping them is refused, not narrowed.
- `TestALiveGrabRefusesAnOverlappingReleaseUnlessItUpgrades` (across two passes).
- `TestAFullSeasonPackWithAnUnairedEpisodeIsRefused` and its manual exemption.
- `TestAPackOverTwoHundredEpisodesIsRefused`.
- `TestCoverageReadsSceneMappedNumbers` and `TestAMultiSeasonPackIsJudgedAgainstEveryEpisode`.
- `TestASeasonPickWithoutTargetsCoversByCoverage`.
- `TestTheGrabStageReadsAnsweredSeasonsInsideTheWindow` (`app/remediation/grab`).
- Rewrite: `TestAPackIsNarrowedToTheWantedEpisodes` where it relied on narrowing past a queued
  episode.

- [ ] **Step 7: Commit.**

  ```bash
  git add app/catalog/grabplan/coverage.go
  git commit -m 'feat(grab): the Series judges season answers -- coverage from the agent'"'"'s scene-mapped facts, a live grab refuses an overlapping release whole unless it upgrades, a full-season pack with an unaired episode is refused, no truncation past 200 episodes, Sonarr'"'"'s CompareEpisodeCount (Wave S S8, tests deferred to the batch)' -- app/catalog/grabplan app/remediation/grab
  ```

---

### Task S9: The Search controller -- planned Searches, the interactive season search, bands

**Spec:** §4.6, §8, §11 (`clustarr_search_results_total`, the Search Events).

**Files:**

- Create: `app/catalog/controller/search/series.go`, `app/catalog/controller/search/season.go`
- Modify: `app/catalog/controller/search/{reconciler.go,answers.go}`, `pkg/obs/metrics/domain.go`

**Interfaces:**

- Consumes: S1, S3 (`BandOf`), S6 (`SeriesSearchStallTimeout`), S7 (`seriessearch`), S8
  (`CoverageRejections`).
- Produces:

  ```go
  package search // app/catalog/controller/search

  // reconcileSeriesSearch renders a planned Search from its Series'
  // status.seasonSearch (design §4.6).
  func (r *Reconciler) reconcileSeriesSearch(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error)
  // seasonItemState is withItemState for an interactive season search:
  // per-episode checks folded by grabplan.CoverageRejections, CoversSeason set.
  func (r *Reconciler) seasonItemState(ctx context.Context, s *catalogv1alpha1.Search, cands []schema.ScoredRelease) []commonv1.ReleaseDecision
  // banded orders results approved, rejected, other titles, caps at limit,
  // stamps Rank, and returns the trailing band's size.
  func banded(results []commonv1.ReleaseDecision, limit int) ([]commonv1.ReleaseDecision, int32)
  // searchesOfSeries maps a Series to its planned Searches (the watch).
  func (r *Reconciler) searchesOfSeries(ctx context.Context, o client.Object) []reconcile.Request

  package metrics
  SearchResultsTotal = newCounterVec("clustarr_search_results_total",
  	"Interactive search results incorporated, by band.", "band")
  ```

- [ ] **Step 1: Route planned Searches.** In `Reconcile`, before `startSearch`:
  `s.Spec.PlannedBySeries()` goes to `reconcileSeriesSearch`. `statusUpdate` gains `seasons`
  (and `finishedAt` for a planned Search, as for a container Search). `bestApproved` returns ""
  for a planned Search; `stuck` does not apply to one.
- [ ] **Step 2: `reconcileSeriesSearch`.** Phase `Running`, `startedAt` on first sight; read the
  Series (gone: `Failed`, `SeriesGone`); its state for another request: `Ready=False, Queued`
  naming it (Event `SeriesSearchQueued` once); its state for this one: `Ready=False, Searching`
  (Event `SeriesSearchStarted` once), `status.seasons` copied, each `grabbed` counted from the
  Series' entries whose release GUID is among that season record's approved candidates with
  `grabbedAt >= answeredAt`; finished: `Completed`, `finishedAt`, Event `SeriesSearchFinished`
  with the counts; no transition for `SeriesSearchStallTimeout`: `Failed`, `Stalled`, Warning
  `SeriesSearchStalled`. TTL as a container Search. Watch Series through
  `searchesOfSeries` (`seriessearch.Of`).
- [ ] **Step 3: The interactive season task.** `interactiveTask` sets `Season` from
  `s.Spec.Season`; `Item` is the Series; no `Container`.
- [ ] **Step 4: Item state and bands.** `incorporate` calls `seasonItemState` for a Search with
  `spec.season`, `withItemState` otherwise; both end in `banded`, and the apply under
  `catalog-worker` sends `results` and `otherTitles` together. `seasonItemState` resolves each
  result's covered Episodes from `Parsed.Coverage` (`series.EpisodeBySeriesRefIndex`), runs
  `decision.ItemStateRejections` per Episode (its current file, the Series' entries as its
  queue), builds `grabplan.ItemCheck`s, folds them with `grabplan.CoverageRejections`, and sets
  `CoversSeason` when the coverage names every aired Episode of `spec.season`. Each incorporated
  answer adds its band counts to `metrics.SearchResultsTotal`; a season answer also adds
  `clustarr_season_searches_total{trigger="interactive"}`.
- [ ] **Step 5: Picks of a season search.** `publishPick` sets `Candidate.Season` from
  `spec.season` and no `Targets` when the item is the Series.

**Tests (deferred to the batch):**

- `TestAPlannedSearchMirrorsTheSeries` (`app/catalog/controller/search`, envtest): seasons,
  phases, `grabbed`, Completed with `finishedAt`, TTL from it.
- `TestAQueuedPlannedSearchWaitsAndThenRuns` and `TestAPlannedSearchFailsWhenStalled`.
- `TestAPlannedSearchNeverPicks` (no `handlePicks`, no results written).
- `TestBandedPutsOtherTitlesLastAndCountsThem` (unit) and
  `TestIncorporateWritesOtherTitlesWithResults` (envtest, `managedFields`: both under
  `catalog-worker`).
- `TestASeasonResultIsRejectedAsThePlannerWould`: a pack overlapping a live entry reads
  `QueueHigherPreference`; a pack of an airing season reads `FullSeasonNotAired`; a pack with
  some episodes at cutoff reads approved.
- `TestCoversSeasonMarksPacksAndBatches`.
- `TestASeasonPickReachesTheSeriesWithItsSeason`.
- `TestCatalogueMatchesTheAmendmentTable` (rewrite): `wantSeries` gains
  `clustarr_search_results_total`.

- [ ] **Step 6: Commit.**

  ```bash
  git add app/catalog/controller/search/series.go app/catalog/controller/search/season.go
  git commit -m 'feat(search): planned series Searches mirror their Series'"'"' season search; the interactive season search carries its season and judges each result as the grab planner would; every interactive answer is banded approved, rejected, other titles with status.otherTitles; clustarr_search_results_total (Wave S S9, tests deferred to the batch)' -- app/catalog/controller/search pkg/obs/metrics/domain.go
  ```

---

### Task S10: The ui -- season buttons, the season modal, the banded panel

**Spec:** §3.4, §10 (items 1-5). **Precondition:** `a245eaf7` on the branch (Global Constraints).

**Files:**

- Create: `ui/season_search.go`, `ui/views/season_search.templ` (+ `season_search_templ.go`)
- Modify: `ui/actions/search.go`, `ui/actions/doc.go`, `ui/routes.go`, `ui/series.go`,
  `ui/episode.go` (`searchPanel`, `releaseRow`), `ui/views/{series.templ,episode.templ,series_rows.go}`
  and their generated `_templ.go`, `ui/static/app.css` (only if `make css` changes it)

**Interfaces:**

- Consumes: S1 (`Search.spec.season`, `status.otherTitles`, `ReleaseDecision.coversSeason`,
  `Series.status.seasonSearch`).
- Produces:

  ```go
  package actions
  // SearchSeason is a season row's magnifier: a Search with grabBest and
  // spec.season, which the Series plans.
  func SearchSeason(ctx context.Context, c Creator, namespace, series string, season int32) (*catalogv1alpha1.Search, error)
  // InteractiveSeasonSearch is a season row's person: a Search with
  // spec.season and no grabBest, its results in status.results.
  func InteractiveSeasonSearch(ctx context.Context, c Creator, namespace, series string, season int32) (*catalogv1alpha1.Search, error)
  func (a *Actions) SearchSeason(ctx context.Context, namespace, series string, season int32) (*catalogv1alpha1.Search, error)
  func (a *Actions) InteractiveSeasonSearch(ctx context.Context, namespace, series string, season int32) (*catalogv1alpha1.Search, error)

  package ui
  // POST /library/{namespace}/series/{name}/seasons/{n}/search
  func (s *Server) handleSeasonSearch(w http.ResponseWriter, r *http.Request)
  // POST /library/{namespace}/series/{name}/seasons/{n}/search/interactive
  func (s *Server) handleSeasonInteractiveSearch(w http.ResponseWriter, r *http.Request)

  package views
  // SearchPanel gains: OtherTitles int; Season bool; Filter string ("packs" | "all"); Packs int
  // ReleaseRow gains:  CoversSeason bool; OtherTitle bool
  // SeasonRow gains:   Search SeasonSearchBadge
  type SeasonSearchBadge struct{ Phase, Label string }
  templ SeasonSearchModal(series Detail, season SeasonRow, p SearchPanel)
  ```

- [ ] **Step 1: Actions.** Both through `uiSearch` with `Spec.Season` set (`validateItem` with kind
  series; a negative season is `ErrInvalid`). The doc comment of `SearchNow` says a series search
  is now planned by the Series.
- [ ] **Step 2: Routes and handlers.** The two POST routes beside the season routes:
  `handleSeasonSearch` answers `#action-status` with "Searching season N" (as an episode's search
  now); `handleSeasonInteractiveSearch` creates the Search and answers the season modal with the
  panel polling. `GET /searches/{namespace}/{name}` reads `?filter=packs|all` (default `packs`
  for a Search with `spec.season`, `all` otherwise).
- [ ] **Step 3: The panel.** `searchPanel` marks the last `status.otherTitles` rows `OtherTitle`;
  `SearchPanelView` renders approved and rejected rows, then a `<details>` "N releases for other
  titles" holding the rest (each row keeps its grab button and override confirm). For a season
  Search it renders the filter ("Season packs" / "All") and, under `packs`, only rows with
  `CoversSeason` or `releaseType: seasonPack`; with none, "No season packs" and a "Show all N
  releases" link to `?filter=all`.
- [ ] **Step 4: The season header.** `SeasonHeader` gains the magnifier and person buttons
  (`data-action="season-search"`, `"season-interactive-search"`) and the badge from
  `status.seasonSearch` (Queued, Searching, "Searched: N approved" with its age).
- [ ] **Step 5: Generate.** `make templ`; `make css` when classes were added; commit the output.

**Tests (deferred to the batch):**

- `TestSearchSeasonAndInteractiveSeasonSearchCreateTheRightSpec` (`ui/actions`, fake client).
- `TestTheSeasonButtonsPostToTheirRoutes` (`ui`, httptest).
- `TestThePanelCollapsesOtherTitles` and `TestTheSeasonPanelDefaultsToPacks`,
  `TestTheSeasonPanelOffersAllWhenNoPackExists`.
- `TestTheSeasonBadgeReadsTheSeriesState`.
- `TestUINeverWrites` and `TestUIRoleGrantsOnlyReadsAndActionWrites` unchanged and green (no new
  verb).

- [ ] **Step 6: Commit.**

  ```bash
  git add ui/season_search.go ui/views/season_search.templ ui/views/season_search_templ.go
  git commit -m 'feat(ui): Sonarr'"'"'s season searches -- each season row gets the automatic and interactive search and a search badge; the season modal lists packs by default; every results panel lists approved, rejected, then N releases for other titles collapsed (Wave S S10, tests deferred to the batch)' -- ui
  ```

---

### Task S11: Docs

**Spec:** §10 item 6, §11, and the design's own status. **Precondition:** `a245eaf7` on the
branch.

**Files:**

- Modify: `CLAUDE.md` (the UI paragraph's season-table text and the wanted-sweep gotcha),
  `docs/adr/0019-agents-never-write-kubernetes.md` (one "Amended 2026-10-07 (owner, Wave S)"
  bullet under Consequences: a Series is searched per season on a person's request, by its own
  key, paced; searches otherwise stay per item), `docs/observability.md` (two catalogue rows and
  the new `decision` values), `docs/superpowers/specs/2026-10-07-series-search-design.md` (Status:
  built, with an "As built" section naming any deviation)

- [ ] **Step 1: CLAUDE.md.** Replace "A season has no search of its own: a Series is not a Search
  fan-out kind, and N episode searches at once is the indexer burst the wanted-sweep gotcha
  forbids." with the season buttons, the Series' planner (`status.seasonSearch`, one paced query
  per season, `Search.spec.season`), and the bands with `otherTitles`. Add to the wanted-sweep
  gotcha: a season search is one paced query per season, counts as each episode's search, and
  nothing publishes per-episode searches for it.
- [ ] **Step 2: ADR-0019, observability, the design.** As listed. `make brand-gate`.

**Tests (deferred to the batch):** none (docs).

- [ ] **Step 3: Commit.**

  ```bash
  git commit -m 'docs: Wave S as built -- CLAUDE.md'"'"'s season searches and the wanted-sweep note, ADR-0019 amended for the season search, the two metric series in the observability catalogue (Wave S S11)' -- CLAUDE.md docs/adr/0019-agents-never-write-kubernetes.md docs/observability.md docs/superpowers/specs/2026-10-07-series-search-design.md
  ```

---

### Task S12: Gate

**Spec:** all; Review Focus.

- [ ] **Step 1: Build and generators.**

  ```bash
  go build ./... && make build
  make generate manifests && git status --porcelain -- api config charts   # prints nothing
  make brand-gate
  ```

- [ ] **Step 2: Linkage.**

  ```bash
  go list -deps ./cmd/manager | grep -E 'ffgo|purego|onnx|anacrolix|pkg/download/usenet|pkg/relindex'   # nothing
  for p in ./app/catalog/agent/catalog ./app/catalog/agent/events ./app/catalog/agent/metadata ./app/indexer/agent; do
    go list -deps "$p" | grep -E 'app/catalog/(grabplan|searchplan|seriessearch)|app/remediation|app/dispatch|app/intake'
  done   # nothing
  ```

- [ ] **Step 3: The rules, by grep.**

  ```bash
  grep -rn 'SeasonSearch *= *true\|SeasonSearch: *true' app/ pkg/ | grep -v _test     # only snapshotSeason
  grep -rn 'SingleEpisodeSearch = true' app/ | grep -v _test                          # only snapshot's Episode branch
  grep -rn 'WorkSearchSubject' app/remediation/search/ app/catalog/searchplan/ | grep -v _test   # the item task and the season task only
  grep -rn 'PriorityHigh' app/catalog/searchplan/ | grep -v _test                    # the season task only
  grep -rn 'func numbers' -A20 app/catalog/grabplan/narrow.go | grep -c 'maxCovered]'  # 0: no truncation
  ```

- [ ] **Step 4: Review Focus.** Walk the design's §16 items 1-7 against the code and record a
  one-line finding for each in the wave's report.
- [ ] **Step 5: The deferred tests.** Append every "Tests (deferred to the batch)" name of S1-S10,
  with its task id, to the controlling session's ledger (`.superpowers/unify/progress.md`, not
  committed), and the non-compiling test files any task left.
- [ ] **Step 6: Commit** only fixes the gate found, path-scoped to the files fixed:
  `git commit -m 'fix: Wave S gate -- <what> (Wave S S12)' -- <paths>`.
