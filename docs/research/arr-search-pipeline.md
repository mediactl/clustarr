# How Sonarr v4 and Radarr build searches, filter results and search a series (2026-10-07)

Researched against source (Sonarr `develop` with `v5-develop` diffs, Radarr `develop`); paths are
under `src/NzbDrone.Core/` unless they start with `frontend/`. Prompted by an interactive search for
Monster (2004, anime) listing "Pocket Monsters", "Monster Eater" and FLAC singles, and by the series
search button failing with `unsupported media kind "series"`.

## Findings on clustarr (live, 2026-10-07)

- Our identity check already rejects every unrelated release (`WrongItem`: title matches none of
  the item's known titles; `UnableToParse` for music), as strictly as Sonarr's "Unknown/Wrong
  series". Nothing wrong is grabbed.
- The noise comes from **nyaasi**, which supports text queries only (`q`, `season`, `ep`): 122
  results for one episode against 9 and 6 from the two newznab indexers (`tvdbid`). nyaa matches
  substrings, so `q=Monster 9` returns every title containing "monster".
- nyaa's definition sends `c: "{{ .Config.cat-id }}"`: the category comes from the indexer's
  settings, never from the request, and its default `0_0` is every category (music, literature,
  live action). Fixed live by setting `spec.settings.cat-id: "1_2"` (Anime, English-translated).
- A Search on a Series is not a fan-out kind (`ContainerKind`), so the series button fails in the
  worker (`snapshot.go`, `unsupported media kind`).
- The interactive list showed rejected releases first.

## 1. Query construction

**Sonarr** (`Indexers/Newznab/NewznabRequestGenerator.cs`, also used for Torznab):
- **What it sends** follows the indexer's caps. ID support comes from `SupportsTvdbSearch`, `SupportsImdbSearch`, `SupportsTvRageSearch`, `SupportsTvMazeSearch` and `SupportsTmdbSearch`; text support from `SupportsTvTitleSearch` (`title`) or `SupportsTvQuerySearch` (`q`).
- **ID query:** `AddTvIdPageableRequests` sends `t=tvsearch&tvdbid=…&season=N&ep=M`. It uses one ID, preferring tvdb, then imdb, rid, tvmaze and tmdb. With `SupportsAggregateIdSearch` it sends all of them in one request.
- **Text is a fallback tier.** `AddTitlePageableRequests` comes after `AddTier()`, and `HttpIndexerBase.FetchReleases` stops at the first tier that returned anything. A scene alias for a scene season is the exception: it uses `SearchMode.SearchTitle` and runs first.
- **Titles:** `SceneMappingService.GetSceneNames` (services.sonarr.tv and TheXEM; English only in v4) plus `series.Title`. `GetCleanSceneTitle` drops a leading "The", turns "&" into "and" and punctuation into "+". No year is added.
- **Anime** (`GetSearchRequests(AnimeEpisodeSearchCriteria)`):
  - It sends `t=tvsearch&tvdbid=X&q=<abs:00>`, plus `t=search&q=<title>+<abs:00>` for each scene title, all in one tier. The text query is therefore always sent, and that is where the noise comes from.
  - `AnimeStandardFormatSearch` adds `season`/`ep` variants.
  - `AnimeCategories` are empty by default.
  - The absolute number is `SceneAbsoluteEpisodeNumber ?? AbsoluteEpisodeNumber`.

**Radarr** (`AddMovieIdPageableRequests`): the first tier is `t=movie&tmdbid=`/`imdbid=`. The second tier runs only when the first returned nothing; it is `t=search&q=<title> <year>`, with the year dropped only under the `RemoveYear` setting. Queries use the Title, the OriginalTitle and translations in the profile's languages. Alternative titles are used only for matching.

## 2. Mapping and filtering

**Sonarr.** `DownloadDecisionMaker.GetDecisions` runs `Parser.ParseTitle`, then `ParsingService.Map(parsed, tvdbId, rageId, imdbId, searchCriteria)`. `FindSeries` tries, in order:
1. the scene mapping, matching when its TVDB ID equals the searched series;
2. an exact clean title (`CleanSeriesTitle`, which keeps digits);
3. the indexer's tvdbid, then rageid, then imdb attribute, trusted as given;
4. library-wide lookups: `FindByTitle`, `GetSeriesByAllTitles`, title plus exact year.

Outcomes:
- **No match:** `UnknownSeries`, or `MatchesAnotherSeries` when an alias names another TVDB ID.
- **Another library series:** `WrongSeries` (`Search/SeriesSpecification`).
- **Right series, wrong episodes:** `UnknownEpisode`, "Episode wasn't requested", or `SingleEpisodeSearchMatchSpecification` (wrong season, wrong episode, full season pack).

**Gap in Sonarr:** a yearless "Monster S01E01" from the 2025 show matches the 2004 series.

**Radarr.** `FindMovie` checks the indexer's imdb attribute, then its tmdb attribute; each requires the parsed year to equal Year or SecondaryYear, or the release to carry no year. It then compares clean titles against the searched movie's title, original title, alternative titles and translations, again with an exact year or SecondaryYear. It never falls back to a library-wide lookup during a search. Outcomes are `UnknownMovie` and `WrongMovie`. There is no ±1 year tolerance in release matching.

In both apps an unparseable release becomes `UnableToParse` during a search and is silently dropped during RSS.

## 3. Interactive search display

- **Nothing is filtered on the server.** `ReleaseController` passes every decision through `MapDecisions`.
- **Unmatched releases go last.** `PrioritizeDecisions` sorts matched releases and appends the unmatched ones.
- **Default filters:** `selectedFilterKey: 'all'` for an episode or movie search. The season view defaults to `season-pack`, so single episodes and rejected releases are hidden until the user changes the filter.
- **Rejected rows** get a red icon with a popover. Grabbing an unknown-series release asks for confirmation, and Override Match lets the user assign it.

## 4. Other filters

- Release profiles: must contain, must not contain.
- `MinimumSeeders` per indexer.
- Categories exist only as the request's `cat=`; results are not filtered by category.
- `DeDupeDecisions` keeps one decision per GUID, the one with the fewest rejections.
- `IsPossibleSpecialEpisode` falls back to `FindByTitleInexact`, a substring match, which lets wrong specials through.

## 5. Series and season search (Sonarr v4)

**Which episodes.**
- `SeriesSearchService`: when no season is monitored, it runs one search per monitored, missing, aired episode. Otherwise it runs `SeasonSearch(monitoredOnly: true)` for each monitored season in ascending order, with `ProcessDecisions` after each.
- The bulk missing and cutoff searches group episodes by season: more than one episode becomes a season search.
- Monitoring and cutoff are enforced in the decision engine (`MonitoredEpisodeSpecification`, `UpgradableSpecification`).

**Season query.**
- `GetSceneSeasonMappings` makes one `SeasonSearchCriteria` per scene season: `tvsearch&tvdbid&season=N` with no `ep`, and the title tier as fallback.
- There is no per-episode fallback; the single episodes that come back are judged individually.
- `SeasonPackOnlySpecification` rejects single episodes once the season is older than `SeasonSearchMaximumSingleEpisodeAge`.
- `FullSeasonSpecification` rejects a pack whose episodes have not all aired.

**Anime** (`SearchAnimeSeason`): one `AnimeSeasonSearchCriteria`, which needs `AnimeStandardFormatSearch` in v4, plus `SearchAnime(isSeasonSearch: true)` for every aired episode. That is one absolute-number query set per episode, which clustarr's no-burst rule forbids. v5 adds season-title aliases and skips the per-episode searches once the season has fully aired.

**Preferring packs** (`ProcessDownloadDecisions.ProcessDecisions`):
- `DownloadDecisionComparer` orders by quality, then custom-format score, then protocol, then `CompareEpisodeCount`.
- `CompareEpisodeCount` puts season packs first only on a tie; after that, anime prefers more episodes and other series prefer fewer.
- `IsEpisodeProcessed` skips any release that overlaps an episode already grabbed in the batch.

**Pacing.** Seasons run one after another. Each criteria object goes to all eligible indexers in parallel, with pages fetched in sequence (at most 30 pages, or 1000 results). Each indexer is held to 2 s between requests (`RateLimitService`). Nothing caps how many queries a search makes.

**Interactive season search** runs `SeasonSearch(monitoredOnly: false, interactive: true)`, and the UI shows season packs by default.

## Recommendation for clustarr (unify branch, owner 2026-10-07)

- **Series and season search** becomes a planned piece on the unify branch, in A4's search planner and grab planner:
  - Run one season query per monitored season: `tvsearch&tvdbid&season=N` where the indexer supports IDs, and the title alone (anime, text-only indexers) elsewhere.
  - Judge packs and single episodes against the season's wanted episodes, prefer a pack on a quality and score tie, and never grab an episode twice in one batch.
  - Leave episodes no result covers to the paced wanted sweep. Unlike Sonarr's anime path, issue no per-episode burst.
- **Interactive results:**
  - List approved releases first.
  - Put rejected releases after them.
  - Put identity mismatches (`WrongItem`, `UnknownItem`, `UnableToParse`) last, collapsed behind "N releases for other titles".
  - Default the season view to packs.
- **Keep the identity rejections** rather than dropping them, as both apps do, so a person can still override a mislabelled release.
- **Close the yearless-collision gap Sonarr has:** when two library items share a clean title, read a yearless match as ambiguous.
