# Plex-native GUIDs from the Plex provider

Date: 2026-10-06. Status: approved design (owner, 2026-10-06), not built.
Scope: movies, shows, seasons and episodes. A combined movies-and-TV provider
root was considered and left out for now (section 9).

## 1. Goal

Plex Web shows **Add to Watchlist** (and the other Discover features) only for
an item whose own `guid` is a `plex://` GUID. Every item clustarr's provider
(`ui/plex`, ADR-0012) matches today carries clustarr's own GUID,
`tv.plex.agents.custom.clustarr.movies://movie/<uid>`, so none of them has
the button.

The provider answers a movie, show, season or episode with its real
`plex://<type>/<id>` GUID whenever clustarr knows that id, while Plex keeps
taking all of the item's metadata from clustarr. Items with no known Plex id
keep clustarr's own GUID. A UI flag turns the behaviour off.

## 2. Evidence (spikes on kind-cluster-plex, 2026-10-06)

The research note says a provider's `guid` must be
`{provider identifier}://{type}/{ratingKey}`
(`docs/research/plex-metadata-provider.md` §6; the reference code's
`constructGuid` throws otherwise). PMS 1.43.4 does not enforce that rule.
Both spikes were reverted afterwards.

1. **A `plex://` entry in `Guid[]` is not enough.** Arrival (item 66) was
   given `plex://movie/5d776b83fb0d55001f56a04b` as an extra `Guid[]` entry,
   written into Plex's database. PMS reported the entry, but Plex Web showed
   no Watchlist action. Zootopia, matched by Plex's own agent on another
   server, did show it.
2. **The item's own `guid` decides.** With Arrival's `metadata_items.guid`
   set to that `plex://` GUID, Plex Web showed **Add to Watchlist**.
3. **PMS accepts a `plex://` match from a custom provider and keeps reading
   from clustarr.** A throwaway UI build answered Arrival's match with the
   `plex://` GUID.
   - Fix Match offered and applied it.
   - PMS then fetched the item from clustarr as
     `GET /plex/movies/library/metadata/5d776b83fb0d55001f56a04b`: the Plex id
     taken from the GUID, not clustarr's `ratingKey`.
   - It fetched `/extras` with clustarr's own `ratingKey`, taken from `key`.
   - Refresh Metadata re-ran the match and kept the `plex://` GUID. The
     stored tagline, studio and ratings were clustarr's.
   - PMS also merged one id from Plex's cloud (`tvdb://219`) into `Guid[]`.
4. **Plex's cloud resolves ids with a Plex token.**
   - `GET https://metadata.provider.plex.tv/library/metadata/matches?type=1&guid=tmdb://329865`
     returns Arrival as `plex://movie/5d776b83fb0d55001f56a04b`.
   - `type=2&guid=tvdb://121361` returns `plex://show/5d9c086c46115600200aa2fe`.
   - `/library/metadata/<show id>/children` lists the show's seasons
     (`plex://season/...`) with `index` = season number.
   - `/grandchildren?includeGuids=1` lists its episodes, paged
     (`X-Plex-Container-Size`, `totalSize`), each with `parentIndex`, `index`
     and `Guid[]` (`tvdb://<episode id>`, `tmdb://...`).

Only a movie was proved end to end. Shows, seasons and episodes are proved
during implementation (section 8).

## 3. Plex metadata client

`pkg/metadata/clients/plex`, following the conventions of `pkg/metadata`:
- every response body is read through `metadata.ReadBody`/`CappedTransport`;
- the rate limiter is injected (`Config.Limiter`) and never defaulted;
- `metadata.ErrNotFound` when Plex has no match;
- the token is sent in the `X-Plex-Token` header, never in the URL, and
  redacted from errors.

It answers three questions:
- **Movie:** `PlexMovieID(ctx, ids)`. It asks `matches?type=1` with
  `tmdb://`, else `imdb://`, and returns the bare 24-hex id.
- **Show:** `PlexShowID(ctx, ids)`. It asks `matches?type=2` with `tvdb://`,
  else `tmdb://`, else `imdb://`.
- **Seasons and episodes:** `PlexShowChildren(ctx, showID)`. It fetches the
  seasons (`/children`) and every page of episodes (`/grandchildren`,
  `includeGuids=1`). It returns `[]{season number, id}` and
  `[]{season, episode, tvdb episode id, id}`, bounded by a page cap that
  covers the longest show in the library with room to spare.

A match is accepted only when Plex returns exactly one result whose
`Guid[]` contains the id that was asked for. Anything else is
`ErrNotFound`. clustarr never guesses a Plex id.

The client is enabled by a new MetadataProvider `spec.type: plex` (enum
addition). Its Secret's `token` key holds the Plex account token; the
watchlist import list's `plex-token` Secret can be named. Without a ready
`plex` MetadataProvider, nothing in this design changes behaviour. It is not
seeded by `metadataprovider.SeedDefaults`, since it needs a key.

## 4. Where the ids live

| Item | Field | Writer |
|---|---|---|
| Movie | `status.metadata.externalIDs["plex"]` (existing map) | metadata gateway, `k8s.ManagerCatalogarrMetadata` |
| Series | `status.metadata.externalIDs["plex"]` (existing map) | metadata gateway |
| Season | **new** `SeriesMetadata.PlexSeasons []PlexSeasonRef` (`{number int32, id string}`), `+kubebuilder:validation:MaxItems=400` like `seasonImages`, and on `status.metadata` for the same reason: the Series controller stays the only writer of `status.seasons` | metadata gateway |
| Episode | **new** `EpisodeStatus.PlexID string` | Series reconciler, `k8s.ManagerCatalogarrSeries`, beside `tvdbID` |

- **Enrichment.** The gateway's enrichment (`app/catalog/metadata/enrich.go`,
  where resolvers already add ids such as `trakt` without overriding the
  primary provider's) asks the `plex` client for the Movie's or Series' id,
  and for a Series its seasons. A failed lookup keeps the id stored before,
  the rule `enrich_envtest_test.go` already holds for resolver ids.
- **Episodes.** The gateway's episode lookup (`rpc.catalogarr.metadata.lookup`,
  which `series.Reconciler.syncEpisodes` calls) sets a new
  `metadata.Episode.PlexID`. Each episode is joined to Plex's list by TVDB
  episode id, else by (season, episode) when exactly one of Plex's episodes
  carries that pair. An ambiguous join leaves `PlexID` empty.
- **Episode field writes.** `DesiredEpisodes` carries `PlexID` through, and
  the Series reconciler's apply sends it unconditionally with the other
  provider fields (the complete-declaration rule in CLAUDE.md), so an empty
  value is an explicit `""`, never an omission.
- **Caching.** Lookups ride the gateway's existing L1/L2 caches under the
  item's normal refresh. Raise `pkg/metadata.SchemaVersion` once, so every
  Movie and Series refreshes once and gains its Plex ids.
- **Request volume.** One match request per movie. Per show: one match, one
  seasons request and ceil(episodes / page) episode pages. On the owner's
  library (~150 shows) that is a few hundred requests per full pass, spread
  across refreshes by the limiter.

## 5. Plex provider (`ui/plex`)

New UI flag `--plex-guids` (default `true`). It is wired through
`ui.Options.Plex`, both UI commands (`TestBothUICommandsWireEveryUIOption`)
and the chart value `ui.plex.plexGuids`.

- **GUID choice.** `plexGUID(type, id)` returns `plex://<type>/<id>` when the
  flag is on and the id is set, else the current `GUID(identifier, type,
  ratingKey)`. It is used for `guid`, `parentGuid` and `grandparentGuid` on
  movie, show, season and episode objects, in match results and metadata
  answers alike, and for `Similar` items in the catalog.
- **ratingKey and key are unchanged.** They stay clustarr's own, as in the
  spike. PMS used them for `/extras`.
- **Resolving a Plex id.** A 24-hex lowercase id is not a valid clustarr
  ratingKey (`ratingKeyPattern` is a UUID), so the two cannot collide. Every
  ratingKey route (metadata, images, children, grandchildren) resolves the
  path value through `ParseRatingKey` first and through a new
  `projection.Index.ByPlexID(id)` second. `ByPlexID` returns a Movie, a
  Series, a (Series, season) pair or an Episode, indexed from the fields in
  section 4 at each index build.
- **Root checks.** A Plex id resolves only on the root that declares its
  type (`rootDef.declares`), as a clustarr ratingKey does, so the movies
  root never answers a show.
- **Match by GUID.** `splitGuid` learns the `plex` scheme. A match request
  whose `guid` hint is `plex://movie/<id>` or `plex://show/<id>` resolves
  through `ByPlexID` before the title rules (rule 1 of `match.go`).
- **Flag off.** Every answer is byte-identical to today's. Plex-id paths
  still resolve, so items PMS already holds under `plex://` keep refreshing
  after the flag is turned off.

## 6. Migrating items PMS already holds

Items matched before this change keep clustarr's GUID until PMS matches them
again. The spike showed Refresh Metadata re-runs the match.
- **To prove:** does a refresh switch an existing item from clustarr's GUID
  to the `plex://` GUID the match now returns? Prove it on one movie and one
  show.
- **If it does not:** the documented fallback is a one-time Fix Match pass,
  `PUT /library/metadata/<id>/match?guid=plex://...` per item, the call the
  spike used. The owner runs it with a script in `hack/`; nothing in clustarr
  writes to Plex.

## 7. Risks

- **Plex may enforce its GUID rule later.** Turning `--plex-guids` off goes
  back to clustarr's own GUIDs for new matches, and existing `plex://` items
  keep resolving (section 5).
- **PMS consults Plex's cloud for `plex://` items** (spike 3's merged
  `tvdb://219`). clustarr's metadata still wins for every field it supplies.
- **Watchlist and import lists.** Adding an owned item to the Watchlist puts
  it on the owner's Plex watchlist, which the `plex` import lists read. An
  import list finds a library item by provider id
  (`importlist.libraryByID`), so nothing is added twice. A test asserts that
  a watchlisted item the library already holds creates nothing.

## 8. Testing

- **Plex client.** Fixtures are recorded from the live service, with the
  token stripped, under `test/data/metadata/plex/`: Arrival's match, Game of
  Thrones' match, seasons and two episode pages, and a no-match.
  - Asserts the ids parsed.
  - Asserts the one-result-with-the-asked-id rule.
  - Asserts paging until `totalSize`.
  - Asserts the token appears in no error and no URL.
- **Gateway.**
  - Unit: the episode join, covering the TVDB-id case, the
    (season, episode) fallback and the ambiguous case.
  - Envtest: `externalIDs["plex"]` and `plexSeasons` are written under
    `ManagerCatalogarrMetadata`, and Episode `status.plexID` under
    `ManagerCatalogarrSeries`. Both are checked through `managedFields`, on
    objects that already have status.
  - Envtest: a failed Plex lookup releases nothing.
- **Provider.**
  - The GUID choice with the flag on and off (off: byte-identical to the
    current goldens).
  - Every ratingKey route resolves a Plex id for each type.
  - The wrong root refuses a Plex id.
  - Match by a `plex://` hint.
  - Parent and grandparent GUIDs on a season and an episode.
  - A show where only some episodes have a Plex id.
- **Live proof (kind-cluster-plex).**
  - One movie, one show with its seasons and episodes, and one partly
    resolved show: matched, refreshed, with **Add to Watchlist** visible in
    Plex Web.
  - Section 6's migration question answered.
  - The flag turned off and back on.
  - This work is not done until this proof passes.

## 9. Not in scope

- **A combined provider root** declaring types 1-4 under one identifier. It
  is possible under Plex's protocol, but it would bar movie-only and TV-only
  fallbacks in a combine group, and move every item to a new GUID scheme.
  Deferred by the owner (2026-10-06).
- **Collections.** clustarr does not declare the `collection` feature.
- **Writing to Plex.** Any migration beyond PMS's own re-match is the
  owner's script (section 6).

## As built (2026-10-06)

- **Commits:** `9c9d7933`..`e2026f63` on main. Plan:
  `docs/superpowers/plans/2026-10-06-plex-native-guids.md`. The
  whole-branch review's three fixes are in the same range:
  - a Plex id two items claim is published for neither (`20844a25`);
  - an Episode's `plexID` is released when Plex answers without one
    (`Episode.PlexConsulted`, `aece8c1a`);
  - the Plex lookup inside the episode RPC gets at most half the caller's
    remaining time (`e2026f63`).
- **Deployed to kind-cluster-plex:** helm revisions 118-121, controller and
  media images `e2026f6`, with a `plex` MetadataProvider over the watchlist
  import list's `plex-token` Secret.
  - The gateway builds its registry once, at start, so it had to be
    restarted after the provider was created.
  - The ~130 items that reached SchemaVersion 2 before that restart were
    refreshed again with `clustarr.io/refresh-metadata`.
  - Result: 977 of the 978 Movies and Series (830 and 148) carry
    `externalIDs["plex"]`, and 11,611 of 15,517 Episodes carry
    `status.plexID`. Episodes without one are mostly those Plex's catalogue
    numbers differently or lacks.
- **§6 answered: Refresh Metadata alone does not migrate an item.**
  - Arrival, refreshed with `force=1`, kept clustarr's GUID although the
    match now answers `plex://`.
  - The Fix Match fallback (`PUT /library/metadata/<id>/match?guid=plex://...`)
    moved Arrival, Overlord and Neon Genesis Evangelion. A show's seasons
    and episodes followed over the next refresh: Overlord's 4 seasons and
    52 episodes, and Evangelion's 26 episodes, are `plex://` in Plex's
    database.
  - Items matched before this change therefore need that one-time Fix
    Match pass to gain Watchlist.
- **Partly resolved shows:** Neon Genesis Evangelion (26 of its 32 clustarr
  episodes have a Plex id) and Shōgun (10 of 34). Each episode without a
  Plex id keeps clustarr's GUID, as `TestAPartlyResolvedShowMixesGUIDs`
  pins.
- **Plex Web:** Arrival shows **Add to Watchlist** (and Play Trailer), and
  Overlord shows **Remove from Watchlist**: it was already on the owner's
  watchlist, and Plex now links the library item to it. The metadata is
  still clustarr's (tagline, studio, ratings).
- **The switch:** `ui.plex.plexGuids=false` (revision 120) made matches
  offer clustarr's GUID again, while
  `/plex/movies/library/metadata/5d776b83fb0d55001f56a04b` still answered
  200. Back to `true` (revision 121) restored `plex://` matches.
