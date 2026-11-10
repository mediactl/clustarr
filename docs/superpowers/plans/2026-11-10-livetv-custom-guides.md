# Live TV Custom Guides Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every admitted IPTV channel goes to Plex on a per-category DVR, numbered alphabetically from the category's base, with a guide clustarr renders from Gracenote (Plex's guide service) first and iptv-epg.org second, carrying every image, and which cluster-plex builds into the DVR's guide database itself.

**Architecture:** clustarr gains an automatic lineup (a pure planner in `pkg/iptv/lineup` and a controller that writes each channel's number and category), a Plex grid client and fetch loop in the tuner, one XMLTV output profile (`xmltv.Listing`) that three readers share, and a block per category. cluster-plex gains `xmltvGuide: manager`: an XMLTV reader into `guidebuild`, a builder/Job/swap beside the cloud one, and the swap hook refusing PMS's own loads of a built guide. Both halves are proven on the kind rig before talos.

**Tech Stack:** Go 1.25, controller-runtime (clustarr fork), server-side apply through `pkg/k8s`, NATS object store (`clustarr-livetv`), `encoding/xml`, pgx (cluster-plex), templ + htmx (ui).

**Spec:** `docs/superpowers/specs/2026-11-10-livetv-custom-guides-design.md` (clustarr). The cluster-plex contract is its §5.8.

## Global Constraints

- GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go file (clustarr); cluster-plex files carry no header.
- Every status write goes through `pkg/k8s.PatchStatus`; every spec write by the manager through `pkg/k8s.Apply`; each manager sends its complete owned set on every apply (CLAUDE.md "Server-side apply replaces…").
- No `float32`/`float64` in `api/`; every status list `MaxItems`-capped; `make generate manifests` after any `api/` change, run serially, and the chart's RBAC copy regenerated (`charts/clustarr/templates/rbac.yaml` is held byte-identical to `config/rbac/manager_role.yaml`).
- The ui never writes status and holds no bus requester (`TestUINeverWrites`); its reads of the store go through `ui/livetvstore.go`'s `read`.
- Metrics carry the `clustarr_` prefix, base units, no title/path labels; a new series is listed in `docs/observability.md` and in `pkg/obs/metrics/metrics_test.go`'s `registeredElsewhere`.
- Plex image hosts reach the browser only through `/art/search` (ADR-0011).
- `metav1.Time` fields read through `.UTC()` before any calendar field.
- Commits: pathspec commits only (`git commit -m '…' -- paths`), `git add` new files first, never `git stash`, never push from a task, no `go get`/`go mod tidy` in parallel tasks (none is needed). Generated files (`api/applyconfiguration`, `config/crd`, `config/rbac`, the chart's RBAC copy) commit with the task that regenerated them.
- Envtests are written in their task and run in the final gate only (owner's rule, memory `envtests-single-final-run`); each task runs its package's unit tests with `go test`.
- cluster-plex's tests that need PostgreSQL read `CLUSTERPLEX_TEST_POSTGRES_DSN`; a task that cannot run them says so in its report.
- The talos deploy is the owner's (cluster-bootstrap Terraform, `tfaa`); nothing in this plan touches talos. The kind rig (`kind-cluster-plex`, namespace `media`) is the proving ground; its claim stays in place.

## Decisions this plan makes (the spec left them open)

- D3's "Sports Events" category is **deferred**: argon's package feeds rename themselves per game ("NFL : 15 - MNF 8:15pm Bills at Vikings") and a channel's identity is its name key, so the category would churn its IPTVChannels and renumber daily. It needs stream-id keys and event-name programmes: a follow-up design, noted in §4 D3 by Task 20.
- `spec.lineup.prefer` and `spec.lineup.languages` are not built. The duplicate rule is fixed (Task 2); a language is left out through `spec.lineup.exclude` patterns.
- The lineup manager (`clustarr-livetv-lineup`) owns every automatic channel's `spec.number`, `spec.active`, the `clustarr.io/origin: lineup` label on the ones it created, and `status.lineup`; it never writes `spec.name` or `spec.group`. The shown name is `spec.name`, else `status.lineup.name` (the trimmed name); the guide class group is `spec.group`, else `status.lineup.category`.
- A category may be named by several rules; the first carries the base.
- The dry run (`enabled: false` with categories set) writes `status.categories` on the provider, `status.lineup` on existing channels, and the whole plan as the store object `lineup-plan/<ns>/<name>`; no spec is written.
- A block per category uses no packer: `start` is `base+1`, the device id `hdhr.BlockDeviceID(ns, name, "cat/"+category)`.
- Grids are fetched by the tuner (it owns the Plex lineup fetch and the store already) under the fetch key `"grids"`.
- cluster-plex's planner takes a provider flavour (`guidebuild.Input.Provider`); the XMLTV body a Job builds from is cached in `clusterplex.epg_cache` under `xmltv/<dvr>/<hash>`; the swap hook treats any DVR the manager builds as its own, cloud or XMLTV.

## Review Focus

1. A category rule whose `names` regex does not compile: the provider reads `Valid=False` reason `InvalidLineup` naming the rule, nothing is written, the last applied lineup stands. (Task 2's `TestInvalidRuleIsAnError`, Task 7's `TestAnInvalidLineupWritesNothing`.)
2. A playlist entry that leaves and comes back inside 7 days keeps its IPTVChannel (and a person's edits on it); one gone 7 days is deleted only when the lineup created it. (Task 7's `TestAnAbsentEntryIsDeactivatedThenDeletedAfterSevenDays`.)
3. The guide service answering 429 mid-fetch: the hold is recorded, no render waits, the channels whose grids are stored keep them and the rest fall to the next source for this render. (Task 8's `TestAHeldGridFetchNeverHoldsARender`.)
4. A station whose grid comes back with no programmes for the window: the channel reads from its next source, never an empty schedule. (Task 8's `TestAnEmptyGridFallsToTheNextSource`.)
5. PMS's first load of a new XMLTV DVR must go through (it pairs the channel map); only loads after the map is saved and the DVR wanted are refused. (Task 15's `TestTheFirstLoadIsImportedAndTheNextIsSkipped`.)

---

## File structure

clustarr:

- `api/clustarr/v1alpha1/iptvprovider_types.go`: `LineupSpec`, `LineupCategory`, `GuideSources`, `PlexGuideSource`, `CategoryStatus`, `GuideSourcesStatus`, `PlexSourceStatus`; `BlockStatus.Category`.
- `api/clustarr/v1alpha1/iptvchannel_types.go`: `IPTVChannelSpec.Category`, `ChannelLineupStatus`, `ChannelPlexStatus.Lineup`.
- `pkg/iptv/lineup/` (new): the pure planner: trim, exclude, duplicates, classify, sort, number.
- `pkg/iptv/plexgrid/` (new): the guide service's grid client and its reduction to `Listing`s.
- `pkg/iptv/xmltv/listing.go` (new): `Listing`, the profile renderer; `enrich.go` reorders every programme and knows `image`.
- `pkg/iptv/livetvstore/`: `GridObject`, `LineupPlanObject`; `IndexProgramme` images and Plex ids.
- `app/livetv/controller/lineup/` (new): the lineup controller.
- `app/livetv/controller/iptvprovider/blocks.go`: the category packing.
- `app/livetv/tuner/`: `grids.go` (new: pairing over lineups, the fetch loop, the merge), `render.go` (source step), `config.go` (new Config fields).
- `ui/`: the Channels page (category, rule, duplicate, source, override, dry run), Sources page (guide sources), TV Guide images, `searchart.go` allowlist.
- `docs/examples/iptvprovider-argon-lineup.yaml` (new), `docs/livetv.md` or CLAUDE.md Live TV section, the spec's §4 D3 note.

cluster-plex:

- `cmd/manager/config_provision.go`: `LiveTVConfig.XMLTVGuide`.
- `pkg/plex/livetv/converge.go`: `XMLTVGuideBuilder`, the XMLTV built-guide branch.
- `pkg/plex/xmltvguide/` (new): the XMLTV reader into `guidebuild.Input`.
- `pkg/plex/guidebuild/build.go`: `Input.Provider`, credits/country/rating/news columns.
- `cmd/manager/xmltvguide.go` (new): `xmltvGuides`, the builder and Job plan; `guides.go`, `guidewriter.go`: "built" by DVR.
- `hack/guidebuild/main.go`: `-xmltv`.
- `charts/cluster-plex/values.yaml`, `templates/storage.yaml`, `templates/rbac.yaml`, `docs/configuration.md`.

Wave order (tasks in a wave touch disjoint files and import nothing from each other): W1 = Tasks 1, 2, 4, 12, 13. W2 = Tasks 3, 5, 14. W3 = Tasks 6, 7, 8, 15. W4 = Tasks 9, 10, 16, 17. W5 = Tasks 11, 18, 19, 20.

---

### Task 1: The API (clustarr, serial)

**Files:**
- Modify: `api/clustarr/v1alpha1/iptvprovider_types.go` (after `PlexGuide`, :30-60; `IPTVProviderSpec` :94; `IPTVProviderStatus` :294; `BlockStatus` :459), `api/clustarr/v1alpha1/iptvchannel_types.go` (`IPTVChannelSpec` :26; `IPTVChannelStatus` :189; `ChannelPlexStatus` :148), `api/clustarr/v1alpha1/defaults.go`.
- Generated: `api/applyconfiguration/clustarr/v1alpha1/*`, `config/crd/bases/clustarr.io_iptv*.yaml`, `zz_generated.deepcopy.go`.
- Test: `pkg/crdcheck/livetv_cel_test.go` (envtest, final gate), `pkg/crdcheck` sweeps (`TestNoCRDDefaultIsUnreachableFromGo`, `TestEveryStatusListIsCapped`).

**Interfaces:**
- Produces (Go, json):
  - `IPTVProviderSpec.Lineup *LineupSpec` `lineup`; `LineupSpec{Enabled bool "enabled"; Trim []string "trim" (MaxItems=32, items MaxLength=256); Exclude []string "exclude" (MaxItems=128, items MaxLength=256); Categories []LineupCategory "categories" (MaxItems=64, listType=atomic)}`.
  - `LineupCategory{Name string "name" (1-64); Base *int32 "base" (1-99999); Groups []string "groups" (MaxItems=64, items ≤256); Names []string "names" (MaxItems=256, items ≤256); Exclude []string "exclude" (MaxItems=64, items ≤256)}`.
  - `IPTVProviderSpec.GuideSources *GuideSources` `guideSources`; `GuideSources{Plex *PlexGuideSource "plex"}`; `PlexGuideSource{CredentialsRef corev1.SecretKeySelector "credentialsRef"; Lineups []string "lineups" (MaxItems=32, items Pattern `^[0-9a-f]{24}$`); Refresh *metav1.Duration "refresh"; Rate *int32 "rate" (1-10)}` with accessors `RefreshOrDefault() time.Duration` (24h) and `RateOrDefault() int32` (2) in `defaults.go`.
  - `IPTVProviderStatus.Categories []CategoryStatus` `categories` (MaxItems=64, listType=map, listMapKey=name); `CategoryStatus{Name string; Base int32; Channels int32; DeviceID string (Pattern `^[0-9A-F]{8}$`, optional); Reason string (≤64, optional: "" or `TooMany`)}`.
  - `IPTVProviderStatus.GuideSources *GuideSourcesStatus` `guideSources`; `GuideSourcesStatus{Plex *PlexSourceStatus}`; `PlexSourceStatus{Lineups int32; Stations int32; Grids int32; FetchedAt *metav1.Time; HoldUntil *metav1.Time; Error string (≤1024)}`.
  - `BlockStatus.Category string` `category` (≤64, optional).
  - `IPTVChannelSpec.Category *string` `category` (≤64; a pointer so `""` is "off every DVR").
  - `IPTVChannelStatus.Lineup *ChannelLineupStatus` `lineup`; `ChannelLineupStatus{Name string (≤256); Category string (≤64); Rule string (≤64); Number string (≤16); DuplicateOf string (≤64, the winning entry's key); MissingSince *metav1.Time}`.
  - `ChannelPlexStatus.Lineup string` `lineup` (≤64).
  - `defaults.go`: `ConditionLineupApplied = "LineupApplied"`, `ConditionGuideSourcesReady = "GuideSourcesReady"`; reasons `ReasonInvalidLineup`, `ReasonDryRun`, `ReasonApplied`, `ReasonTooMany`; `OriginLineup = "lineup"` beside the ui's origin value (the label is `clustarr.io/origin`, `ui/actions.LabelOrigin`; move the key to `defaults.go` as `LabelOrigin` and have `ui/actions` reference it); `DefaultGridRefresh = 24*time.Hour`, `DefaultGridRate = 2`.
- CEL on `LineupSpec`: `!has(self.categories) || self.categories.filter(c, has(c.base)).map(c, c.base).size() == self.categories.filter(c, has(c.base)).map(c, c.base).toSet().size()` message "category bases must be unique"; and the first rule of a name carries a base: `!has(self.categories) || self.categories.all(c, has(c.base) || self.categories.exists(d, d.name == c.name && has(d.base)))` message "a category's first rule must carry base".

- [ ] **Step 1: Write the CEL test**

In `pkg/crdcheck/livetv_cel_test.go` add `TestLineupCEL`: a provider with two categories of base 1000 is refused with "category bases must be unique"; one rule `{name: News}` without base and no sibling with one is refused with "a category's first rule must carry base"; a second `News` rule without base beside a first with base 1000 is accepted; `rate: 11` and a 23-hex lineup id are refused; `spec.category: ""` on a channel is accepted and round-trips as the empty string (a dynamic client, as `TestLiveTVCEL` does).

- [ ] **Step 2: Add the types and markers** as listed in Interfaces, with doc comments naming the spec section (§5.1).

- [ ] **Step 3: Regenerate**

Run: `make generate manifests` from the clustarr root. Expected: clean; `git status` shows only `api/`, `config/crd/bases/clustarr.io_iptv*.yaml` and `config/rbac` unchanged.

- [ ] **Step 4: Build and sweep**

Run: `go build ./... && go test ./pkg/crdcheck/ -run 'TestNoCRDDefaultIsUnreachableFromGo|TestEveryStatusListIsCapped' -count=1`. Expected: PASS (the envtest-backed tests skip without assets; they run in the final gate).

- [ ] **Step 5: Commit** (pathspec: the two types files, `defaults.go`, `zz_generated.deepcopy.go`, `api/applyconfiguration/clustarr/v1alpha1/`, `config/crd/bases/`, the CEL test, `ui/actions/actions.go` if the label moved).

---

### Task 2: The lineup planner `pkg/iptv/lineup`

**Files:**
- Create: `pkg/iptv/lineup/lineup.go`, `pkg/iptv/lineup/lineup_test.go`, `docs/examples/iptvprovider-argon-lineup.yaml` (a complete IPTVProvider manifest whose `spec.lineup` and `spec.guideSources` are the controlling session's `scratchpad gv/lineup-values.yaml`, handed over in the task brief; `server`, `credentialsRef`, `guideURL` as talos has them, the Secret names only).
- Fixture: `test/data/iptv/lineup_names.txt` (the trimmed names of §2.5's groups: copy `scratchpad gv/names.txt`'s ENTERTAINMENT, SPORTS NETWORK, NEWS NETWORK, KIDS NETWORK and the first 60 of TV, as `group\tname` lines) — names only, no URLs.

**Interfaces:**
- Produces:
  ```go
  type Rule struct { Category string; Base int; Groups, Names, Exclude []string }
  type Spec struct { Trim, Exclude []string; Rules []Rule; MaxChannels int }   // MaxChannels 0 = 400
  type Override struct { Category *string; Name string }                     // a person's spec.category and spec.name
  type Entry struct { Key, Name, Group, Logo string }                         // from m3u.Entry
  type Assignment struct { Key, Name, Group, Category, Rule, Number string; Base, Index int }
  type Category struct { Name string; Base, Channels int; TooMany bool }
  type Dropped struct { Key, Name, Reason, DuplicateOf string }                // Reason: "excluded" | "duplicate" | "unmatched" | "off" | "tooMany"
  type Plan struct { Channels []Assignment; Categories []Category; Dropped []Dropped }
  func Compile(s Spec) (*Planner, error)      // compiles every regexp once; an error names the rule and pattern
  func (p *Planner) Plan(entries []Entry, overrides map[string]Override) Plan
  func TrimName(name string, trim []*regexp.Regexp) string
  ```
- Rules, fixed in this task: a `Trim` pattern is a Go regexp applied in order and replaced by ""; the trimmed name is also collapsed of doubled spaces and trimmed of space. `Exclude` and a rule's `Exclude` are regexps tested (case-insensitive) against the trimmed name and the group. A rule matches when (`Groups` empty or the entry's group is listed exactly) and (`Names` empty or any pattern matches the trimmed name, case-insensitive) and no exclude matches. Duplicates: entries of one trimmed name (compared through `release.TitleNorm`) keep one — the one whose raw name carries the higher resolution marker (`4K` > `1080p` > `720p` > `480p` > none), then the one whose group comes first in the winning rule's `Groups`, then playlist order. An override `Category` of `""` is Reason `off`; a non-empty override wins over the rules; the override `Name` replaces the trimmed name for sorting and display. Sorting within a category: `release.TitleNorm(name)`, then the name. Numbers: `strconv.Itoa(base+1+index)`; a category whose count exceeds MaxChannels is `TooMany` and its entries are `Dropped` with Reason `tooMany`. `Categories` is in rule order (first occurrence), including empty ones.

- [ ] **Step 1: Write the failing tests** (`package lineup_test`):
  - `TestTrimNameStripsTheMarkers`: `"USA: CNN [1080p]"` → `"CNN"`; `"HBO HITS EAST"` → `"HBO HITS"`; `"VIS: FOX SPORTS 1 (E)"` → `"FOX SPORTS 1"` under the spec's default trim list (copy it from `docs/examples/iptvprovider-argon-lineup.yaml`'s `trim`, Task 20 writes that file from this list).
  - `TestDuplicatesPreferHDThenGroupOrderThenPlaylistOrder`: CNN in NEWS NETWORK [1080p] and CNN in TV [720p] → the NEWS one assigned, the TV one Dropped `duplicate` with `DuplicateOf` the winner's key.
  - `TestFirstMatchingRuleWins` and `TestASecondRuleOfANameJoinsTheCategory` (News base 1000 from the first rule; the second rule carries 0).
  - `TestNumbersAreAlphabeticalFromBasePlusOne`: BBC NEWS, CNN, ABC NEWS at base 1000 → ABC NEWS 1001, BBC NEWS 1002, CNN 1003; `TitleNorm` order ignores case and punctuation ("E!" before "ESPN").
  - `TestAnOverrideMovesAndAnEmptyOverrideTurnsOff`.
  - `TestTooManyRefusesTheCategory` (MaxChannels 2, three entries → Category TooMany, three Dropped `tooMany`, no Assignment).
  - `TestInvalidRuleIsAnError`: `Names: ["("]` → `Compile` error containing the rule name and the pattern.
  - `TestUnmatchedEntriesAreDroppedNotGuessed`.
  - `TestTheArgonSampleClassifies`: the fixture under the example file's rules (parsed with `sigs.k8s.io/yaml` into the CRD type from Task 1 — the two tasks land in one wave, so read the YAML into a local struct mirroring `LineupSpec`'s json names and add `FromSpec(s *clustarrv1.LineupSpec, maxChannels int) Spec` only once Task 1 is merged, in Task 7): asserts no Dropped `unmatched` among SPORTS NETWORK, NEWS NETWORK, KIDS NETWORK except the `✶` headers, and prints the per-category counts (`t.Log`) for the owner's reading.

- [ ] **Step 2: Run** `go test ./pkg/iptv/lineup/ -count=1`. Expected: FAIL to compile.

- [ ] **Step 3: Implement** `lineup.go` per the Interfaces block.

- [ ] **Step 4: Run** the tests. Expected: PASS.

- [ ] **Step 5: Commit** (`pkg/iptv/lineup/`, `test/data/iptv/lineup_names.txt`).

---

### Task 3: The grid client `pkg/iptv/plexgrid`

**Files:**
- Create: `pkg/iptv/plexgrid/plexgrid.go`, `plexgrid_test.go`; fixture `test/data/plexgrid/grid-sample.json` (a 2-hour cut of the real answer: run `python3 -I` over `scratchpad gv/mt-fortnight.json` keeping the first 6 `MediaContainer.Metadata` items and their `Media`; the shape, not the week).
- Depends on: `pkg/iptv/xmltv.Listing` (Task 4) — define the reduction target as `plexgrid.Programme{Start, Stop time.Time; xmltv.Listing}`; until Task 4 lands, write this task against the `Listing` field set in Task 4's Interfaces (both land in the same wave; the integration build is Task 8's).

**Interfaces:**
- Produces:
  ```go
  const BaseURL = "https://epg.provider.plex.tv"
  type RateLimitedError struct{ RetryAfter time.Duration }   // Error(), errors.As
  type Client struct { BaseURL, Token string; HTTP *http.Client; Limiter interface{ Wait(context.Context) error } }  // caller owns the limiter (CLAUDE.md)
  func (c *Client) Grid(ctx context.Context, gridKey string, from, to time.Time) ([]Programme, error)
  func Reduce(items []Item) []Programme                       // exported for the test; Item is the decoded shape
  ```
- `Grid` GETs `/grid/bulk?channelGridKey=<key>&startDate=<YYYY-MM-DD>&endDate=<YYYY-MM-DD>` with `X-Plex-Token` and `Accept: application/json`, body capped at 32 MiB (`ErrResponseTooLarge`), 429 → `*RateLimitedError` (Retry-After seconds, 0 when absent), 404 → `ErrNotFound`.
- Reduction, per item and airing: `Start`/`Stop` from the airing's `beginsAt`/`endsAt` (Unix seconds); `Listing.Title` = the show's title for an episode (`grandparentTitle`), the item's for a movie; `SubTitle` = an episode's own title; `Desc` = summary; `Season`/`Episode` = `parentIndex`/`index` (0 when absent); `Date` = year (an episode's `originallyAvailableAt` year when present); `Categories` = genres' tags, plus `Sports` when `Team` is non-empty; `Poster` = `grandparentThumb` for an episode else `thumb`; `Backdrop` = `grandparentArt` else `art`; `Still` = an episode's `thumb`; `PlexGUID` = `guid`, `PlexShowGUID` = `grandparentGUID`, `PlexSeasonGUID` = `plex://season/<grandparentRatingKey>/<parentIndex>` when both known; `Rating` = `contentRating` (system `VCHIP` when it is TV-xx, else `MPAA`); `Live`, `Premiere`, `New` from the airing (`new` = premiere); `PreviouslyShown` = not premiere; `LengthMinutes` = duration/60000; `Kind` = `movie` when the item's `type` is "movie", else `episode`; `OriginallyAvailableAt` the date string.

- [ ] **Step 1: Write the failing tests**: `TestGridReducesTheRecordedAnswer` (the fixture: counts, the first programme's Title/SubTitle/Season/Episode/Poster/Still/PlexGUID, a movie's Kind), `TestGridHonoursTheLimiterAndSendsTheToken` (httptest records the header and the query's dates, a counting limiter), `TestA429IsARateLimitedError` (Retry-After 90 → 90s), `TestABodyOverTheCapIsRefused`.

- [ ] **Step 2: Run** `go test ./pkg/iptv/plexgrid/ -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement.** Decode only the fields named above (a typed struct, not `map[string]any`).

- [ ] **Step 4: Run** the tests. Expected: PASS.

- [ ] **Step 5: Commit** (`pkg/iptv/plexgrid/`, `test/data/plexgrid/`).

---

### Task 4: The XMLTV profile: `xmltv.Listing`, the enricher's order, the index's images

**Files:**
- Create: `pkg/iptv/xmltv/listing.go`, `listing_test.go`.
- Modify: `pkg/iptv/xmltv/enrich.go` (`dtdOrder` has `image` already; `enrich` must emit every programme in DTD order, not only changed ones; know `image` and `episode-num system="plex*"` as elements it keeps), `pkg/iptv/xmltv/build.go` (channel element: a second `<display-name>` "<number> <name>"), `pkg/iptv/livetvstore/index.go` (`IndexProgramme` gains `Poster, Backdrop, Still, PlexGUID string`; `IndexGuide` reads `<image type=...>` and `episode-num system="plex"`), `test/data/iptv/guide_built.golden.xml` (regenerate with `-update` and read the diff: only the second display-name and element order may move).
- Test: `listing_test.go`, `enrich_test.go`, `xmltv_test.go`, `pkg/iptv/livetvstore/index_test.go`.

**Interfaces:**
- Produces:
  ```go
  type Kind string  // KindMovie = "movie", KindEpisode = "episode"
  type Credit struct { Name, Role string }
  type Listing struct {
      Kind Kind; Title, SubTitle, Desc string
      Actors []Credit; Directors, Writers, Producers []string
      Date string                       // a year "2021" or "20210315"
      Categories []string
      Poster, Backdrop, Still string
      Season, Episode int               // 1-based; 0 unknown
      ProgID string                     // dd_progid, optional
      PlexGUID, PlexShowGUID, PlexSeasonGUID string
      PreviouslyShown, Premiere, New, Live bool
      Rating, RatingSystem string       // "TV-14"/"VCHIP", "PG-13"/"MPAA"
      LengthMinutes int
      OriginallyAvailableAt string      // YYYY-MM-DD, optional
  }
  func (l Listing) Inner() []byte       // the programme's inner XML in DTD order, escaped
  func (p Programme) Listing() (Listing, bool)   // the inverse for a built guide's programme (used by IndexGuide and tests); false when Inner does not parse
  ```
- `Inner()` writes, in `dtdOrder`: `title`, `sub-title` (when set), `desc`, `credits` (actor with `role` attr, director, writer, producer), `date`, each `category` (the source's, then `Movie` for a movie or `Series` for an episode, never doubled), `length units="minutes"`, `icon src=Poster`, `episode-num system="xmltv_ns"` (`<s-1>.<e-1>.` when both known; `.<e-1>.` when only the episode), `episode-num system="onscreen"` (`S03E05`, `E12`), `episode-num system="dd_progid"` when ProgID set, `episode-num system="plex"`, `"plex.show"`, `"plex.season"` when set, `previously-shown`, `premiere`, `new`, `live`, `rating system=…` with `<value>`, `image type="poster" size="3" orient="P"`, `image type="backdrop" size="3" orient="L"`, `image type="still" size="3" orient="L"` (each when set). An episode without numbers and without ProgID gets no episode-num: the enricher's `progID` rule gives it a `dd_progid` (`EP`/`SH`/`MV`/`SP` as today), so PMS files it as an episode or a movie the same way for every source.

- [ ] **Step 1: Write the failing tests**:
  - `TestListingInnerIsInDTDOrderAndRoundTrips`: a full `Listing` → `Inner()` → `Programme{Inner}.Listing()` equals the input; the element names come out in `dtdOrder` order (parse with `splitElements`-like scan in the test: a regexp over `<([a-z-]+)[ >]`).
  - `TestAMovieCarriesNoEpisodeNumAndCategoryMovie`, `TestAnEpisodeWithoutNumbersCarriesNone` (the enricher adds dd_progid: assert on `Enrich(l.Inner(), ch, start, stop)` containing `system="dd_progid"` and `EP`).
  - `TestTheEnricherReordersAnUnchangedProgramme` (iptv-epg.org's alphabetical inner — `title` last — comes out `title` first even when nothing else changes).
  - `TestIndexGuideReadsImagesAndThePlexID`.
  - The golden: `go test ./pkg/iptv/xmltv/ -run TestBuildGuide -update` then inspect the diff.

- [ ] **Step 2: Run** `go test ./pkg/iptv/xmltv/ ./pkg/iptv/livetvstore/ -count=1`. Expected: FAIL.

- [ ] **Step 3: Implement** `listing.go`; in `enrich.go` make the DTD-order re-emit unconditional (today it is gated on `changed`) and add `plex`, `plex.show`, `plex.season` to the episode-num systems `facts` passes through untouched; in `build.go` write the second `display-name`; in `index.go` read the new elements.

- [ ] **Step 4: Run** the tests. Expected: PASS; `TestBuildGuide` green against the regenerated golden.

- [ ] **Step 5: Commit** (`pkg/iptv/xmltv/`, `pkg/iptv/livetvstore/`, `test/data/iptv/guide_built.golden.xml`).

---

### Task 5: The store objects, the tuner's config fields and entries, the wiring stubs

**Files:**
- Modify: `pkg/iptv/livetvstore/livetvstore.go` (`GridObject(key types.NamespacedName, gridKey string) string` = `grid/<ns>/<name>/<gridKey>`; `LineupPlanObject(key) string` = `lineup-plan/<ns>/<name>`; `LineupPlan{PlannedAt time.Time; DryRun bool; Error string; Plan lineup.Plan}`, gzip JSON through `Encode`/`Decode`), `app/livetv/tuner/tuner.go` (`func (t *Tuner) Entries(key types.NamespacedName) ([]m3u.Entry, bool)` — the admitted candidates of the last render, `filter.Candidates` already computed in `render.go`:97; keep them on `provider` as `candidates []m3u.Entry` and return a clone), `app/livetv/tuner/config.go` (declare, unused until Tasks 6 and 8: `Config.GuideSources *GuideSourcesConfig`, `GuideSourcesConfig{Lineups []string; Token, TokenError string; Refresh time.Duration; Rate int}`, `Config.Trim []string`, `ChannelConfig.Category string`, `BlockConfig.Category string`, `Snapshot.GuideSources GuideSourcesState`, `GuideSourcesState{Lineups, Stations, Grids int; FetchedAt, HoldUntil time.Time; Error string}`), `app/livetv/controller/iptvprovider/reconciler.go` (declare `PlanReader func(ctx context.Context, key types.NamespacedName) (*livetvstore.LineupPlan, bool, error)` on the Reconciler, unused until Task 6), `app/livetv/manager/register.go` (a third notifier channel `lineupEvents` fed by the same `Notify`, kept for Task 7; `PlanReader` injected from `bus.ObjectStore(events.BucketLiveTV)` through a small `readLineupPlan` helper in `app/livetv/manager`).
- Test: `pkg/iptv/livetvstore/livetvstore_test.go` (names and round-trip), `app/livetv/tuner/tuner_test.go` (`TestEntriesAreTheAdmittedCandidatesAfterTheFirstFetch`).

**Interfaces:** Consumes `lineup.Plan` (Task 2). Produces the names above; `Entries` returns `ok=false` before the first playlist fetch. Tasks 6, 7 and 8 edit none of `config.go`'s declarations, only use them.

- [ ] **Step 1: Write the failing tests.** `go test ./pkg/iptv/livetvstore/ ./app/livetv/tuner/ -run 'TestGridObject|TestLineupPlan|TestEntries' -count=1` → FAIL.
- [ ] **Step 2: Implement.** `notifiers` in `register.go` gains a third channel; a dropped notification (full buffer) is counted as today.
- [ ] **Step 3: Run** the tests → PASS. `go build ./...`.
- [ ] **Step 4: Commit.**

---

### Task 6: The provider controller: a block per category, the lineup's conditions, the guide-source status

**Files:**
- Modify (this package only): `app/livetv/controller/iptvprovider/blocks.go` (`planBlocks` :118), `status.go` (`render`, `withBlocks`), `config.go` (`buildConfig`: `ChannelConfig.Name = spec.name else status.lineup.name`, `Group = spec.group else status.lineup.category`, `Category = status.lineup.category`, `Config.GuideSources` from `spec.guideSources.plex` with the token read like `plexToken`, `Config.Trim = spec.lineup.trim`, `BlockConfig.Category`), `reconciler.go` (`resolve` reads the guide-source Secret key; `PlanReader` used).
- Test: `blocks_internal_test.go` (`TestCategoriesAreBlocksOfTheirOwn`: channels with `status.lineup.category` News/Sports and numbers 1001-1003/2001-2002 → two blocks, Start "1001" and "2001", `Category`, `DeviceID == hdhr.BlockDeviceID(ns, name, "cat/News")`, `FriendlyName == "<friendly> News"`, `GuidePath "/xmltv.xml"`; a channel with no category is in no block; `TestACategoryBlockKeepsItsDeviceIDAcrossARenumbering`; `TestLineupOffPacksAsBefore`), `status_internal_test.go` (`TestCategoryStatusMirrorsThePlanAndTooMany`: `status.categories` from the plan object's `Categories` (a fake `PlanReader`), `Channels` counted from the channels' `status.lineup.category`).

**Interfaces:**
- Consumes: Task 1's types; Task 5's `LineupPlanObject`, `PlanReader` and the tuner config fields.
- Produces: `status.blocks[].category`; `status.categories[]`; `status.guideSources.plex`; conditions `LineupApplied` (reason `Applied` / `DryRun` / `InvalidLineup` / `TooMany`, from the plan object's state: `Dry`, its `Error` field — Task 7 writes `LineupPlan.Error` for a `Compile` failure) and `GuideSourcesReady` (True when the last grid pass had no error and no hold; reason `RateLimited` while `HoldUntil` is in the future; `SecretNotFound`/`SecretKeyNotFound` from `resolve`). Both conditions only while the spec sets the respective block (so a provider without them stays at five conditions).
- The category path in `planBlocks`: when `spec.lineup.enabled` and a plan object exists: numbers grouped by the channels' `status.lineup.category`, one block each with `Start = strconv(base+1)` from `status.categories`/the plan, no `blocks.Next`; the cross-provider overlap check runs as today over the blocks' spans; a category with no active channel has no block. When `spec.lineup` is unset or disabled: today's path untouched.

- [ ] **Step 1: Write the failing tests** named above (fake client, as `blocks_internal_test.go` does).
- [ ] **Step 2: Run** `go test ./app/livetv/controller/iptvprovider/ -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS; `go build ./...`.
- [ ] **Step 5: Commit.**

---

### Task 7: The lineup controller `app/livetv/controller/lineup`

**Files:**
- Create: `app/livetv/controller/lineup/doc.go` (RBAC markers: iptvproviders get/list/watch; iptvproviders/status get/patch/update; iptvchannels get/list/watch/create/patch/update/delete; events create/patch), `reconciler.go`, `plan.go`, `reconciler_test.go` (fake client), `reconciler_envtest_test.go` (envtest, final gate).
- Modify: `pkg/k8s/fieldmanager.go` (`ManagerLiveTVLineup FieldManager = "clustarr-livetv-lineup"`, in `FieldManagers()`), `app/livetv/manager/register.go` (wire it with `Entries: tn.Entries`, `Store: bus.ObjectStore(events.BucketLiveTV)`, `Events: lineupEvents` — the channel Task 5 left), `pkg/iptv/lineup/lineup.go` (`FromSpec(s *clustarrv1.LineupSpec, maxChannels int) Spec`, now that the API is merged; Task 2's YAML-reading test switches to it), `config/rbac/manager_role.yaml` + `charts/clustarr/templates/rbac.yaml` (`make manifests`, then copy the role into the chart as the existing test demands), `Makefile` `RBAC_PATHS_manager` if `./app/livetv/...` does not already cover the new package (it does: check).

**Interfaces:**
- Consumes: `lineup.Compile`/`Plan` (Task 2), `tuner.Entries` (Task 5), `livetvstore.LineupPlanObject`, `index.ChannelsOf`.
- Produces:
  ```go
  type Reconciler struct {
      client.Client
      Entries func(types.NamespacedName) ([]m3u.Entry, bool)
      Store   events.ObjectStore
      Events  <-chan event.GenericEvent
      Now     func() time.Time
  }
  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error   // For IPTVProvider (GenerationChanged), Watches IPTVChannel → its provider, source.Channel(Events)
  const DeleteAbsentAfter = 7 * 24 * time.Hour
  ```
- Behaviour, per reconcile of a provider with `spec.lineup` set:
  1. `lineup.Compile(FromSpec(...))`; an error writes `LineupPlan{Error: …, PlannedAt}` to the store and returns (the provider controller surfaces `InvalidLineup`); nothing else is written.
  2. `Entries`: `ok=false` → requeue in 30 s.
  3. Overrides from the existing channels (`spec.category`, `spec.name`), keyed by `spec.key`; `Plan`.
  4. Write the plan object (`DryRun = !enabled`).
  5. Dry run: for each existing channel with an assignment or a drop, apply `status.lineup` (`Name`, `Category`, `Rule`, `Number`, `DuplicateOf`) under `ManagerLiveTVLineup` through `PatchStatus`; return.
  6. Enabled: for each assignment, `k8s.Apply` an `IPTVChannel` apply configuration: name `actions.ChannelObjectName(provider, key)` (move that function to `pkg/iptv/names.go` as `ChannelObjectName` so the controller and the ui share it), `spec.providerRef`, `spec.key`, `spec.number`, `spec.active: true`, label `clustarr.io/origin: lineup` only when the object does not exist yet (an existing channel keeps its label; read first); then `PatchStatus` `status.lineup`. For each Dropped entry whose channel exists: `spec.active: false` through the same apply (providerRef, key, number as it was, active false) and `status.lineup` with the reason in `Rule` ("" ) and `DuplicateOf`. For a channel of this provider whose key is in no entry at all (gone from the playlist): `spec.active: false`, `status.lineup.missingSince` set once; when `missingSince` is older than `DeleteAbsentAfter` and the label is `lineup`, delete it. A category `TooMany`: its entries are Dropped and so deactivated; an Event `LineupCategoryTooMany` on the provider.
  7. One Event `LineupApplied` with the counts when anything changed.
- Every apply under `ManagerLiveTVLineup` sends exactly these spec fields; never `name`, `group`, `logo`, `epg`, `language`, `category`.

- [ ] **Step 1: Write the failing tests** (fake client, `package lineup`):
  - `TestADryRunWritesThePlanAndStatusButNoSpec`.
  - `TestAnEnabledLineupCreatesNumbersAndActivatesChannels` (three entries, one existing channel with a person's `spec.name`: the name survives, the number moves, the new ones carry the origin label, the existing one keeps its own).
  - `TestAnAbsentEntryIsDeactivatedThenDeletedAfterSevenDays` (Now advanced; a channel without the lineup label is deactivated and never deleted).
  - `TestAnInvalidLineupWritesNothing` (plan object carries the error; no channel touched).
  - `TestAnOverrideCategoryMovesTheChannel` and `TestAnEmptyOverrideDeactivates`.
  - `TestTooManyDeactivatesTheCategory`.
  - The envtest: `TestTheLineupManagerOwnsOnlyItsFields` reads `managedFields` through a direct client after a ui merge patch of `spec.name` and a second lineup pass: `clustarr-livetv-lineup` owns `f:number`, `f:active`, `f:providerRef`, `f:key` and nothing else; `clustarr-ui` owns `f:name`.

- [ ] **Step 2: Run** `go test ./app/livetv/controller/lineup/ -count=1` → FAIL.
- [ ] **Step 3: Implement**, then `make manifests` and copy the role into the chart (`TestManagerRoleChartMatchesConfig` or its sibling names the mechanism; run `go test ./test/guards/... -run Role -count=1`).
- [ ] **Step 4: Run** the package tests → PASS; `go build ./... && go vet ./app/livetv/...`.
- [ ] **Step 5: Commit** (the package, `pkg/k8s/fieldmanager.go` + its test, `register.go`, `pkg/iptv/names.go`, `ui/actions/livetv.go`, `config/rbac/manager_role.yaml`, the chart's rbac).

---

### Task 8: The tuner: stations over lineups, the grid fetch loop, the merge

**Files:**
- Create: `app/livetv/tuner/grids.go`, `grids_test.go` (`package tuner_test`, an httptest guide service like `plexlineup_test.go`'s `plexCloud`, redirected through `Options.Fetch`), `grids_internal_test.go`.
- Modify: `app/livetv/tuner/render.go` (the source step), `tuner.go` (`dispatch`: a `"grids"` fetch key; `provider` fields `lineups map[string]plexguide.Lineup`, `grids gridState{holdUntil time.Time; fetched map[gridKey]time.Time; err string}`), `plexlineup.go` (fetch every lineup of `GuideSources.Lineups` and of `Plex.LineupID`, cached as `plex-lineup/<ns>/<name>/<lineupID>` — keep `PlexLineupObject` for the cloud one, add `PlexLineupObjectFor(key, lineupID)`), `metrics.go` (`clustarr_livetv_grid_calls_total{provider,result}` result = ok|held|error|empty, `clustarr_livetv_grid_hold_seconds{provider}` gauge), `docs/observability.md` + `pkg/obs/metrics/metrics_test.go` `registeredElsewhere`.

**Interfaces:**
- Consumes: `plexgrid.Client` (Task 3), `xmltv.Listing` (Task 4), `livetvstore.GridObject` (Task 5), `Config.GuideSources` and `ChannelConfig.Category` (Task 6).
- Produces, in the render (between step 7's pairing and the guide build):
  1. **Stations.** `plexguide.Pair` runs over each configured lineup in order (the cloud lineup first when `Plex.Enabled`, then `GuideSources.Lineups`); a channel's station is the first lineup's `Manual` or `Automatic` pairing; `ChannelState.Plex` records it with `Pairing.Lineup` (add the field to `plexguide.Pairing`), the channel controller writes `status.plex.lineup` (one-line change in `iptvchannel/reconciler.go`'s `plexStatus`).
  2. **Grids.** `wantedGrids(p) []string`: the paired stations' grid keys. `fetchGrids(ctx, p)` (the `"grids"` dispatch key, due when any wanted key's store object is older than `Refresh` or absent, and `holdUntil` has passed): for each due key, `plexgrid.Client.Grid(ctx, key, today, today+13d)` under a `rate.Limiter` of `Rate` per second (`golang.org/x/time/rate`, already a dependency: check `go.mod`), store the programmes as gzip JSON (`[]plexgrid.Programme`) at `GridObject`; a `*RateLimitedError` sets `holdUntil = now + max(RetryAfter, 1m)` and ends the pass; after the pass, `scheduleRender`. A fetch never blocks a render (it runs in `dispatch` like the playlist fetch).
  3. **Merge.** In the per-channel loop: a channel with a station whose grid object exists and holds ≥1 programme in the window → its programmes are `xmltv.Programme{Channel: number, Start, Stop, Inner: Listing.Inner()}` under the guide name `"plex"` (a second entry in `loaded`), `st.Guide = "plex/<gridKey>"`, `lc.Map = &xmltv.Mapping{Guide: "plex", ChannelID: number}`; else today's path (xmltv by id/name → `"xmltv/<id>"`, panel, dummy). The grid programmes are loaded in `loadProgrammes` beside the cached guide (read each wanted `GridObject`; cache the decoded slices on `provider` by object digest so a render decodes only moved ones).
  4. `Snapshot.GuideSources` filled (`Lineups`, `Stations` paired, `Grids` stored, `FetchedAt`, `HoldUntil`, `Error`).
- `lc.Group` for the enricher: `ChannelConfig.Group` else `ChannelConfig.Category` (Task 6 set `Group` already by that rule in `buildConfig`; nothing to do here beyond passing it).

- [ ] **Step 1: Write the failing tests**:
  - `TestAPairedStationsGridFeedsItsChannel` (the fake service answers the fixture grid for the station WABC is paired to in the argon fixture; the device's `xmltv.xml` carries that channel's programmes with `episode-num system="plex"` and `image type="still"`; `status.guide` reads `plex/<gridKey>`).
  - `TestAHeldGridFetchNeverHoldsARender` (the service answers 429 Retry-After 120: the render completes within the test's second, the snapshot's `HoldUntil` is ~2 min out, the channel reads from xmltv).
  - `TestAnEmptyGridFallsToTheNextSource`.
  - `TestGridsRefreshDailyNotPerRender` (two renders, one GET).
  - `TestStationsPairOverLineupsInOrder` (a station found only in the second lineup pairs with `Lineup` set to it).

- [ ] **Step 2: Run** `go test ./app/livetv/tuner/ -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS; `go test ./pkg/obs/metrics/ -run TestCatalogueMatchesTheObservabilityDoc -count=1` → PASS.
- [ ] **Step 5: Commit.**

---

### Task 9: The ui: Channels page, Sources page, TV Guide images, the allowlist

**Files:**
- Modify: `ui/projection/livetv.go` (`ChannelRow` gains `Category, Rule, DuplicateOf, LineupName, Source string; LineupOn, DryRun bool`; `SourceRow` gains `GuideSources *PlexSourceStatus`, `Categories []CategoryStatus`), `ui/livetv_channels.go` (the edit form's `category` key → `ChannelEdit.Category *string` and the merge patch `spec.category` (null clears, `""` is off); the `number` field disabled and not patched while `LineupOn`; the playlist pane rows show the plan's category or drop reason from the `LineupPlanObject`, read through `ui/livetvstore.go`'s new `LineupPlan(ctx, key) (*livetvstore.LineupPlan, bool)`; a "Dry run" banner with the per-category counts when `DryRun`), `ui/views/livetv_channels.templ` (columns Category, Source; a `select` for the category override listing the provider's category names plus "Off" and "Automatic"), `ui/livetv_sources.go` + `livetv_sources.templ` (a guide-sources row: lineups, stations, grids, fetched, hold, error; the categories table under the blocks card), `ui/livetv_guide.go` + `livetv_guide.templ` (the programme dialog shows the still or poster through `s.searchArt.URL`, the backdrop as the dialog's background, a "Open in Plex" link `https://app.plex.tv/desktop/#!/provider/tv.plex.provider.epg/details?key=/library/metadata/<id>` when `PlexGUID` is set — the id is the guid's last path segment), `ui/actions/livetv.go` (`ChannelEdit.Category`), `ui/searchart.go` (`metadata-static.plex.tv`, `provider-static.plex.tv` in `searchArtHosts`).
- Test: `ui/livetv_channels_test.go` (`TestTheChannelsPageShowsCategoryAndSource`, `TestANumberCannotBeEditedUnderAnAutomaticLineup`, `TestTheDryRunBannerCountsTheCategories`), `ui/actions/livetv_test.go` (`TestACategoryOverridePatchesSpecCategory`: `"Off"` → `""`, `"Automatic"` → null), `ui/livetv_guide_test.go` (`TestTheProgrammeDialogShowsTheStillAndThePlexLink`), `ui/searchart_test.go` (the two hosts allowed).
- Format templates with `go run github.com/a-h/templ/cmd/templ@v0.3.1020 fmt <file>` and regenerate (`make templ` or the Makefile's target; check).

**Interfaces:** Consumes Task 1's status fields, Task 5's `LineupPlanObject`, Task 4's `IndexProgramme` images. The views use shadcn-templ components only (`select`, `badge`, `alert` for the banner; the project skill `.claude/skills/shadcn-templ/SKILL.md`).

- [ ] **Step 1: Write the failing tests.** `go test ./ui/ ./ui/actions/ -run 'LiveTV|Channel|Programme|SearchArt' -count=1` → FAIL.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** → PASS; `go test ./ui/... -run TestUINeverWrites -count=1` → PASS.
- [ ] **Step 4: Commit.**

---

### Task 10: The channel controller's lineup fields and the example lineup

**Files:**
- Modify: `app/livetv/controller/iptvchannel/reconciler.go` (`status.plex.lineup` from `Pairing.Lineup`; the skip-unchanged check compares it).
- Validate: `docs/examples/iptvprovider-argon-lineup.yaml` (Task 2 wrote it) is a complete IPTVProvider the CRD accepts: `TestTheExampleLineupIsValid` in `pkg/crdcheck/livetv_cel_test.go` creates it on the envtest apiserver (final gate) and `TestTheExampleLineupDecodes` in `pkg/iptv/lineup/lineup_test.go` decodes it into `clustarrv1.IPTVProvider` and `Compile`s `FromSpec` of its lineup.
- Test: `app/livetv/controller/iptvchannel/reconciler_envtest_test.go` (`status.plex.lineup` round-trips; final gate).

- [ ] **Step 1: Write the tests; run; FAIL.**
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `go test ./pkg/iptv/lineup/ ./app/livetv/controller/iptvchannel/ -count=1` → PASS.
- [ ] **Step 4: Commit.**

---

### Task 11: clustarr docs, CLAUDE.md, the spec's as-built notes

**Files:**
- Modify: `CLAUDE.md` (the Live TV section: one paragraph on the automatic lineup, the guide sources and the XMLTV profile, naming the packages and field managers; the Sports Events deferral), `docs/superpowers/specs/2026-11-10-livetv-custom-guides-design.md` (§4 D3 "deferred" note with the reason; an "As built" section listing the decisions above and the commit subjects), `docs/superpowers/specs/2026-10-07-iptv-live-tv-design.md` and `2026-11-03-livetv-plex-native-guide-design.md` (a dated line pointing at the new spec), `charts/clustarr/README.md` (the `lineup` and `guideSources` fields, the dry-run flow).

- [ ] **Step 1: Write.** `make brand-gate` → clean.
- [ ] **Step 2: Commit.**

---

### Task 12: cluster-plex: the config key and the chart

**Files (cluster-plex):**
- Modify: `cmd/manager/config_provision.go` (`LiveTVConfig.XMLTVGuide string "xmltvGuide"`, constants `XMLTVGuidePMS = "pms"`, `XMLTVGuideManager = "manager"`, default `pms`, validated in `loadLiveTV`; `GuideWriter()` is also true when `XMLTVGuide == manager`; `BuildsGuides() bool` = either manager), `cmd/manager/config_test.go`, `charts/cluster-plex/values.yaml` (`plex.liveTV.xmltvGuide: pms` with a comment), `templates/storage.yaml` (render it), `templates/rbac.yaml` (the jobs grant when either guide mode is `manager` and `cloudGuideBuild != inProcess`), `docs/configuration.md` ("### The managers build an XMLTV guide" beside the cloud section).
- Test: `cmd/manager/config_test.go` (`TestXMLTVGuideDefaultsToPMSAndValidates`), `helm lint --strict charts/cluster-plex`, `helm template` with `xmltvGuide: manager` shows the jobs rule.

- [ ] **Step 1: Write the failing test; run `go test ./cmd/manager/ -run TestXMLTVGuide -count=1` → FAIL.**
- [ ] **Step 2: Implement; helm lint.**
- [ ] **Step 3: Run → PASS. Commit.**

---

### Task 13: cluster-plex: `guidebuild` learns the XMLTV flavour

**Files (cluster-plex):**
- Modify: `pkg/plex/guidebuild/build.go` (`Input.Provider string` — `ProviderCloud = "tv.plex.providers.epg.cloud"`, `ProviderXMLTV = "tv.plex.providers.epg.xmltv"`, empty reads as cloud; `Sections.News int64`; `epgcloud.Item` gains `Role, Director, Writer, Producer, Country []Tag` (json `Role`, `Director`, `Writer`, `Producer`, `Country`, the shapes Plex's own JSON uses) and `Airing.Live` exists already), `build_test.go`.
- Rows under `ProviderXMLTV`, as the rig measured (spec §2.3): tag types star=6 (actor then director, `tags_star` = the first three), writer=5 (`tags_writer`), producer=7, country=8 (`tags_country`); `channelTitle` = `"<VCN> <CallSign>"`; `at:channelIdentifier` = the station's `ID` (the number) and no `at:gridKey`; an episode's section is News (4) when a genre is `News`, Sports when `Sports`, else Shows; a movie stays in Movies; the show's `content_rating` and `year` as the cloud path does; the genre tag's `key` empty and tags keyed by name (`tags.key` = "" for XMLTV: confirm against the rig's DVR 8 `tags` rows in the oracle task and adjust); `at:premiere="1"` on an episode airing whose item has no previously-shown (`Airing.Premiere` carries it from the reader).
- Under `ProviderCloud` nothing changes (`build_test.go`'s existing tests hold).

**Interfaces:** Produces `Input.Provider`, `Sections.News`, the five credit/country tag slices; consumed by Task 14's reader and Task 15's builder.

- [ ] **Step 1: Write the failing tests** (`TestXMLTVFlavourWritesCreditsCountryAndNewsSection`, `TestXMLTVChannelTitleAndIdentifier`, `TestTheCloudFlavourIsUnchanged` = the existing suite).
- [ ] **Step 2: Run** `go test ./pkg/plex/guidebuild/ -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS. **Commit.**

---

### Task 14: cluster-plex: `pkg/plex/xmltvguide`, the reader

**Files (cluster-plex):**
- Create: `pkg/plex/xmltvguide/xmltvguide.go`, `xmltvguide_test.go`, `testdata/profile.xml` (a hand-written XMLTV in clustarr's profile: 3 channels, ~12 programmes covering: an episode with xmltv_ns + plex guids + still + backdrop; one with onscreen only; one with dd_progid EP; one with original-air-date only; a movie with MPAA rating, date, poster; a sports event with live and premiere; a repeat with previously-shown; credits of every kind; two airings of one episode; two airings of one id-less movie), `hack/guiderig/xmltv-gen.py` (copy of the session's `gv/x1/gen.py`: the 48-programme probe generator; the oracle's reference comes from it).

**Interfaces:**
- Produces:
  ```go
  type Channel struct { ID, Number, CallSign, Name, Icon string }   // ID = the <channel id>, Number = the lineup's GuideNumber
  func Read(r io.Reader, lineup map[string]string /* channel id → GuideNumber */, now time.Time) (guidebuild.Input, []Channel, error)
  ```
- `Read` streams the XMLTV with `encoding/xml` (body ≤ 64 MiB, gzip-sniffed), and fills `Input{Provider: ProviderXMLTV, Stations: one epgcloud.Station per channel {ID: number, VCN: number, CallSign: first display-name, Title: first display-name, Thumb: icon}, Channels: {GridKey: "", VCN: number}, Grids: map[number][][]epgcloud.Item (one week slice)}`, items keyed and shaped exactly as PMS's XMLTV reader keys them (spec §2.3): an episode when xmltv_ns, onscreen `SxxExx`, dd_progid `EP…`, or original-air-date gives it one; `plex`/`plex.show`/`plex.season` episode-nums set the item's, show's and season's GUIDs, else `tv.plex.xmltv://episode/<title>/<s>/<e>`, `…/show/<title>`, `…/season/<title>/<s>`, `…/movie/<title>`; the dd_progid-EP season is the current year; an original-air-date-only episode is keyed by the date with index -1; `sub-title` → an episode's title; `desc` → summary; the first `icon` → the show's/movie's `Thumb`; `image type=poster/backdrop/still` → `GrandparentThumb`/`GrandparentArt` and the episode's `Thumb`/`Art` (a movie's poster/backdrop → its `Thumb`/`Art`); categories → `Genre` tags (the `Movie`/`Series` classifiers dropped); credits → the five tag slices; `date` → `Year`; the first `rating` value → `ContentRating`; `length` → `Duration` ms; `previously-shown` absent on an episode → the airing's `Premiere`; `live` → `Airing.Live`. Items repeat by GUID (a second airing joins the first item's `Media`); an id-less movie seen twice keeps the first description.
- The channel id → number map comes from the device's `lineup.json` (Task 15 fetches it); `Read` ignores a channel not in the map and reports it in the returned `[]Channel` with `Number ""` (the builder logs it).

- [ ] **Step 1: Write the failing tests** against `testdata/profile.xml`: one assertion per row of spec §2.3's table (guids, sections through `guidebuild.Plan`, thumbs on the show and the episode, the premiere flag, two airings of one item, the id-less movie), plus `TestAChannelOutsideTheLineupIsReported`.
- [ ] **Step 2: Run** `go test ./pkg/plex/xmltvguide/ -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS. **Commit** (the package, the generator script).

---

### Task 15: cluster-plex: the XMLTV builder, Job plan and the swap hook

**Files (cluster-plex):**
- Create: `cmd/manager/xmltvguide.go`, `xmltvguide_test.go`.
- Modify: `pkg/plex/livetv/converge.go` (`XMLTVGuideBuilder interface{ Want(dvrUUID, guideURL, guideHash string) }`, `Converger.XMLTVGuides XMLTVGuideBuilder`; in `converge`'s switch for an XMLTV block (`b.Lineup == ""`) when `XMLTVGuides != nil`: after the map save `cancelLoadsUntil` as the cloud path, then `cancelBuiltLoads` + `XMLTVGuides.Want(dvr.UUID, uri+b.GuidePath, b.GuideHash)` instead of `ReloadGuide` on a guide-hash move; the `emptyGuide` reload stays, since the first fill is PMS's), `converge_test.go`, `cmd/manager/livetv.go` (`conv.XMLTVGuides = m.xmltv` when set), `cmd/manager/main.go` (`m.xmltv = newXMLTVGuides(m)` when `LiveTV.Enabled && XMLTVGuide == manager`; its fetcher on every pod is unnecessary — the body is cached by the builder — so only `runBuilder` on the leader in `leaderwork.go`), `cmd/manager/guides.go` (`loadsNotOurs`: "the managers build this guide" when `m.builds(g)`: cloud under `CloudGuide == manager`, or an XMLTV DVR in `m.xmltv.wanted(dvr)`), `cmd/manager/guidewriter.go` (`builtGuides` lists every `epg_*` schema whose comment is `builtComment`, whatever the provider), `pkg/plex/bootstrap/guides.go` (`guideLoadedAt` recognizes `builtComment`, so `ImportGuide` never replaces a built guide with an older SQLite load), `cloudguidejob.go` (`runGuideBuild` reads the plan's `provider`: `cloudInput` for cloud, `xmltvInput` for XMLTV — a plan JSON gains `"provider"`; the stamp covers the plan bytes as today).

**Interfaces:**
- Consumes: Task 13's flavour, Task 14's `Read`, the cloud Job machinery (`stageInJob`, `plexbootstrap.SwapStaged`, `cloudPlanStamp`, `renderCloudJob`).
- Produces:
  ```go
  type xmltvPlan struct { Provider string `json:"provider"`; DVR string `json:"dvr"`; URL string `json:"url"`; Hash string `json:"hash"`; Lineup map[string]string `json:"lineup"` }  // channel id → GuideNumber
  type xmltvGuides struct { m *Manager; mu sync.Mutex; wants map[string]xmltvWant; built map[string]cloudBuilt; kick chan struct{} }
  func (x *xmltvGuides) Want(dvrUUID, guideURL, guideHash string)
  func (x *xmltvGuides) wanted(dvrUUID string) bool
  func (x *xmltvGuides) runBuilder(ctx context.Context)
  const xmltvBuildFloor = 30 * time.Minute; xmltvBuildEvery = 24 * time.Hour
  func xmltvInput(ctx context.Context, conn, p xmltvPlan) (guidebuild.Input, error)   // reads `xmltv/<dvr>/<hash>` from epg_cache
  ```
- `build(dvr)`: GET the device's `lineup.json` (sibling of the guide URL) → the id map (`GuideNumber` → the channel id is the XMLTV `<channel id>`, which clustarr sets to the number, so the map is identity; keep the map so a future id scheme works); GET the XMLTV (capped 64 MiB), `CachePut("xmltv/<dvr>/<hash>", body)`; write the plan at `plan/<dvr>`; `stageInJob` + `SwapStaged` (or in-process under `CloudGuideBuild == inProcess`); `MarkGuideRefreshed`; `CachePrune` older xmltv bodies of the DVR. `due`: the hash moved and the last build is older than `xmltvBuildFloor`, or older than `xmltvBuildEvery`; `failed` as the cloud's.

- [ ] **Step 1: Write the failing tests**:
  - `converge_test.go`: `TestAnXMLTVBlockIsWantedAfterItsMapSaveAndNeverReloaded` (fake PMS: with `XMLTVGuides` set, a guide-hash move posts no `reloadGuide` and calls `Want` with the URL and hash; without it, `reloadGuide` as today).
  - `xmltvguide_test.go`: `TestAHashMoveInsideTheFloorWaits`, `TestTheFirstLoadIsImportedAndTheNextIsSkipped` (the guide socket: before `Want`, `swap <load file>` of an XMLTV DVR answers `ok`/import as today; after `Want`, `skip the managers build this guide`), `TestBuiltGuidesListsXMLTVSchemas` (the writer file carries an XMLTV schema built with `builtComment`; needs the DSN, else skips by name).
- [ ] **Step 2: Run** `go test ./pkg/plex/livetv/ ./cmd/manager/ -run 'XMLTV|FirstLoad|BuiltGuides' -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS; `go build ./... && go vet ./cmd/manager/ ./pkg/plex/...`. **Commit.**

---

### Task 16: cluster-plex: the oracle `-xmltv` and the rig proof

**Files (cluster-plex):**
- Modify: `hack/guidebuild/main.go` (`-xmltv <file>`: builds through `xmltvguide.Read` with a `-lineup <json>` map (the device's `lineup.json`) into the staging schema and compares with `-ref` as today; the compare tolerates nothing new), `docs/guide-rig.md` (a "The XMLTV oracle" section: the recipe below).
- Rig recipe (kind-cluster-plex, ns `media`, claimed; memory `livetv-custom-guide-direction-2026-11-10` and `built-cloud-guide-exact-2026-11-10` hold the access notes): regenerate the 48-programme guide with `hack/guiderig/xmltv-gen.py` into the `xmltvprobe-x1` ConfigMap, let PMS reload DVR 8 (`POST /livetv/dvrs/8/reloadGuide` through the port-forward with `.LocalAdminToken` — a read-only probe is not enough here; the rig is throwaway), copy `epg_fde0a670482b45e8941c59cf6c7385c3` to a `-ref` schema with `hack/guiderig/rigsql.sh`, run the oracle with the same XMLTV; iterate Task 13/14's keys until equal on every column PMS fills, then list the additions (stills, backdrops, plex guids, news section) in the report.

- [ ] **Step 1: Add the flag and a unit test** (`TestXMLTVOracleBuildsFromAFile` against `pkg/plex/xmltvguide/testdata/profile.xml`, DSN-gated).
- [ ] **Step 2: Run the oracle on the rig** per the recipe; record the diff iterations in the report; fix the reader/planner in place (commit under this task).
- [ ] **Step 3: Flip the rig** to `xmltvGuide: manager` (the rig's config, `rig-config.job.yaml`), re-point DVR 8 (the `rig-x1` provider's block), watch one build Job stage and swap, then `GET /tv.plex.providers.epg.xmltv:8/grid` shows `thumb`, `art`, `grandparentArt` and a `plex://` guid on the profile programmes, and a forced `reloadGuide` from PMS is refused at the hook ("skip the managers build this guide" in the manager log) while the built guide stands.
- [ ] **Step 4: Commit** (the oracle, the docs).

---

### Task 17: clustarr: the end-to-end render on the rig's shape (unit level)

**Files:**
- Modify: `app/livetv/tuner/tuner_test.go` (`TestACategoryDeviceServesItsGuideInTheProfile`: a provider with two category blocks; each device's `lineup.json` holds only its category's numbers; its `xmltv.xml` carries the two `display-name`s per channel and DTD-ordered programmes; the `guideHash` of one block moves when only its channel's grid changes), `app/livetv/controller/iptvprovider/reconciler_envtest_test.go` (final gate: a provider with `spec.lineup` enabled and three channels carrying `status.lineup` publishes two category blocks with the `cat/` device ids).
- Also: `test/e2e/livetv_test.go` scenario 19 gains the category lineup (written, not run, as the rest of e2e).

- [ ] **Step 1: Write; run `go test ./app/livetv/tuner/ -run Category -count=1` → FAIL (before the integration edits) / PASS after.** Any integration gap between Tasks 6-8 found here is fixed in this task.
- [ ] **Step 2: Commit.**

---

### Task 18: The final gate (clustarr)

- [ ] `make generate manifests` clean; `go build ./...`; `go vet ./app/livetv/... ./pkg/iptv/... ./ui/...`.
- [ ] `make test` once (envtests included: Tasks 1, 7, 10, 17's), `make lint`, `make brand-gate`, `helm lint --strict charts/clustarr`.
- [ ] A failure is fixed in a commit naming the task it belongs to.

---

### Task 19: The final gate (cluster-plex)

- [ ] `go build ./... && go vet ./...`; `go test ./... -count=1` with `CLUSTERPLEX_TEST_POSTGRES_DSN` set (the rig's PostgreSQL through `hack/guiderig/rigsql.sh`'s forward, or the local embedded one the repo's Makefile provides: check `make test`); `helm lint --strict charts/cluster-plex`.

---

### Task 20: The rollout kit (no deploy)

**Files:**
- Create (clustarr): `docs/runbooks/livetv-category-cutover.md`: spec §5.10's steps as a checklist with the exact talos edits: the IPTVProvider's `spec.lineup` (from `docs/examples/iptvprovider-argon-lineup.yaml`), `spec.guideSources.plex` (the `plex-token` Secret's `token` key already exists), `spec.plexGuide.enabled: false` at the flip; cluster-plex's `plex.liveTV.xmltvGuide: manager`; the CRDs applied by hand first (`kubectl apply --server-side --field-manager=clustarr-deploy -f config/crd/bases/clustarr.io_iptv*.yaml`); what to watch (`status.categories`, `status.guideSources.plex`, the `LineupApplied` condition, the build Jobs, PMS's DVR list); the rollback (`spec.lineup.enabled: false`, `plexGuide.enabled: true` — the cloud block's device id is unchanged, so its DVR returns as it was minus its recording rules).
- Staged for the owner (scratchpad, not committed anywhere): the cluster-bootstrap `resources.yaml` fragment with the provider's new fields, from the example file.
- Modify: the spec's §4 D3 note (if Task 11 did not), the memory file `livetv-custom-guide-direction-2026-11-10.md` ("built; deploy via the runbook").

- [ ] **Step 1: Write the runbook; commit.**
- [ ] **Step 2: Hand the owner** the dry-run instructions: deploy clustarr, set `spec.lineup` with `enabled: false`, read the Channels page.

---

## Self-review

- Spec coverage: §5.1 → T1; §5.2 → T2, T7; §5.3 → T3, T5, T8, T10; §5.4 → T4; §5.5 → T6; §5.6 → T8, T17; §5.7 → T9; §5.8 → T12-T16; §5.9/§5.10 → T16, T20; D3 deferred → T11/T20; D11 allowlist → T9.
- Names used across tasks: `lineup.Compile/Plan/Spec/Rule/Assignment/Dropped/Category/Plan` (T2 → T7, T6 reads the plan object); `livetvstore.GridObject/LineupPlanObject/LineupPlan` (T5 → T6, T7, T8, T9); `plexgrid.Client.Grid/Programme/RateLimitedError` (T3 → T8); `xmltv.Listing.Inner/Programme.Listing` (T4 → T3's `Programme`, T8, T14's input shape is cluster-plex's own); `tuner.Entries` (T5 → T7); `Config.GuideSources/GuideSourcesConfig/ChannelConfig.Category/BlockConfig.Category/Snapshot.GuideSources` (T6 → T8); `k8s.ManagerLiveTVLineup` (T7); `guidebuild.Input.Provider/ProviderXMLTV/Sections.News` (T13 → T14, T15); `xmltvguide.Read` (T14 → T15, T16); `XMLTVGuideBuilder.Want` (T15).
- Review Focus items 1-5 each name their test in T2/T7, T7, T8, T8, T15.
- Proportion: the plan decides files, names, signatures, the fixed rules and the tests; bodies are left to the implementer except the row rules PMS's reader fixed (T13, T14), which are the spec's measurements restated as assertions.
