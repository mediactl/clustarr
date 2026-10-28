# MediUX image sets, looked up and cached

Status: built, 2026-10-22 (the "As built" section at the end), from the owner's request ("find a way to
automate the lookup and download of the collection set yaml file based on
given media; cache everything so we don't hammer the api"). Builds on
`2026-10-21-image-sets-design.md` (the Image Set dialog, `spec.artwork`,
`Series.spec.seasonArtwork`, `pkg/imageset`).

## How Kometa and the others do it (verified 2026-10-22, from source)

- **Kometa has no MediUX integration.** Its guide
  (https://kometa.wiki/en/latest/kometa/guides/mediux/) is: open the set on
  mediux.pro, click its YAML button, paste the result into a metadata file.
  The YAML is Kometa's image-files format, keyed by TheTVDB id for shows and
  TMDB id for films, with `url_poster`/`url_background` per item, per season
  and per episode -- what `pkg/imageset` parses. Kometa cannot set collection
  images from it.
- **The site scrapers are dead ends.** `scrape-mediux` and the original
  `Mediux-Posters` scraped mediux.pro pages; their author calls that "flaky
  at best" and moved to the API.
- **MediUX has an API, and it is Directus.** The MediUX team's own tool,
  AURA (https://github.com/mediux-team/AURA, `backend/mediux/`), and
  `Mediux-Posters` since its `mediux-api` branch, both use a Directus GraphQL
  endpoint with a personal bearer token:
  - AURA posts to `https://images.mediux.io/graphql`
    (`make_request.go`: `MediuxApiURL`), `Authorization: Bearer <token>`,
    `User-Agent: aura/1.0`; `https://api.mediux.pro` is its "public URL".
    Mediux-Posters takes the base URL from settings and posts to
    `<base>/graphql`.
  - Collections: `shows`, `show_sets`, `movies`, `movie_sets`,
    `collections`, `collection_sets`, `content_ids`. A show's or film's id
    **is its TMDB id** (`shows_by_id(id: $tmdb_id)`); a show carries
    `tvdb_id`, `imdb_id`, `trakt_id`, and
    `shows(filter: {tvdb_id: {_eq: $tvdb}})` maps a TheTVDB id to it.
  - A `show_set` has `set_title`, `user_created { username }`,
    `date_created`, `date_updated`, `popularity`, `popularity_global`,
    `show_poster`, `show_backdrop`, `season_posters[] { season {
    season_number } }` and `titlecards[] { episode { episode_number
    season_id { season_number } } }`; each image is a Directus file:
    `id`, `modified_on`, `filesize`, `src`, `blurhash`, `language {
    display_name }`. A `movie_set` has `movie_poster`/`movie_backdrop`; a
    `collection_set` the collection's poster and backdrop plus its films'.
    AURA's queries are `backend/mediux/gen_*.graphql`; the show-sets one
    filters to sets with at least one image.
  - An image is `<host>/assets/<file id>?v=<modified_on formatted>` with
    `&key=jpg` for AURA's "optimized" quality, `&key=thumb` for a
    thumbnail, nothing for the original (`image_get_url.go`).
    `https://api.mediux.pro/assets/<id>` answers anonymously (`image/webp`;
    verified 2026-10-21, what the YAML export links). AURA sends the bearer
    token to `images.mediux.io` and `api.mediux.io` only.
  - **Verified live with the shared token, 2026-10-22** (fixtures in
    `test/data/metadata/mediux`): `images.mediux.io/graphql` answers it;
    `api.mediux.pro/graphql` answers "Invalid user credentials." (another
    auth realm). The assets on `images.mediux.io` are served
    **anonymously**: `?key=jpg` a 378 KB JPEG, `?key=thumb` a 46 KB AVIF,
    no key the 509 KB WebP original; `api.mediux.pro/assets/<id>` serves
    the original anonymously but refuses `key=jpg`. A file's `src` is its
    versioned path, `/<id>?v=20240112193259`. `content_ids` holds 17,134
    entries. Cyberpunk: Edgerunners has 14 sets; `popularity_global` can be
    null. The client is `pkg/metadata/clients/mediux`.
  - **Tokens:** "Personal tokens aren't available to everyone yet"
    (AURA's MediUX settings page). A shared testing token is pinned in the
    MediUX Discord (`repo-chats > mediux-team/aura`); personal tokens come
    with the new site. The API served a snapshot, not the live site, during
    the 2025 prerelease.
  - **Limits:** none documented. AURA has no client-side throttle; it caches
    every image it downloads on disk by asset id and `modified_on`, keeps the
    list of TMDB ids that have sets (`content_ids`) in memory, and retries
    its download queue. Mediux-Posters sends a `User-Agent` naming itself.

## Design

### A `mediux` MetadataProvider

`MetadataProviderType` gains `mediux`: `secretRef` key `token`, `baseURL`
default `https://images.mediux.io` (the `graphql` and `assets` paths are
the client's), and under `spec.mediux`:

- `preferredCreators []string`: MediUX usernames whose sets win, in order
  (AURA lists the creators a user follows first; we have no user context,
  so the owner names them).
- `languages []string`: image languages to prefer, BCP-47, default the
  provider's `language`; a set whose poster language is in it outranks one
  whose is not.
- `quality`: `optimized` (default, `key=jpg`) or `original`.
- `refresh`: how often an item's choice is reconsidered, default 7 days.

A keyless provider is `Ready=False, MissingSecret`, so nothing asks MediUX
until the owner has a token. The seeds (`metadataprovider.SeedDefaults`)
do not seed it.

### The client, `pkg/metadata/clients/mediux`

A GraphQL client over `net/http`: `POST <base>/graphql` with the bearer
token and `User-Agent: clustarr/<version>`, bodies read through
`metadata.ReadBody`, a caller-owned `*rate.Limiter`
(`Config.Limiter`, never defaulted on, as every client), `RateLimitedError`
on 429 with `Retry-After`, `ErrNotFound` for an id MediUX does not have,
`ErrUnauthorized` for 401/403 (the provider goes `Ready=False, Auth`).

Queries, copied from AURA's and recorded as fixtures once a token exists:

- `ShowByTVDB(tvdb) -> tmdb id` (`shows(filter: {tvdb_id})`);
- `ShowSets(tmdb) -> []ShowSet` (AURA's `getShowItemSetsByTMDBID`);
- `MovieSets(tmdb) -> []MovieSet`; `CollectionSets(tmdb) -> []CollectionSet`;
- `ShowSet(id)`, `MovieSet(id)`, `CollectionSet(id)` for a set the owner
  names;
- `ContentIDs() -> {movies, shows}` (`content_ids(limit: -1)`), the one
  cheap question "does MediUX have anything for this item at all".

A set maps to `imageset.Entry`: the show poster and backdrop to
`spec.artwork`-shaped overrides, season posters and titlecards to
`SeasonArtworkOverride` by season and episode number, each URL
`<public>/assets/<id>?v=<modified_on>` plus `&key=jpg` for optimized. The
`v=` keeps the proxy's cache honest when a creator replaces an image.

### Choosing a set

`imageset.Choose(sets, policy)`: drop sets with no poster; rank by
preferred creator (earlier wins), then language match, then completeness
(a season poster for every season the series has, a titlecard for every
episode with a file), then `popularity`, then `date_updated`. A set the
owner named through the Image Set dialog (by id or URL) beats the ranking.
The choice and its reasons are logged once per item.

### Where it runs, and who writes what

ADR-0019: the manager decides, the metadata agent asks, a record answers.

- **Planner.** The Series' and Movie's item keys in the remediation loop
  gain an image-set stage: when a `mediux` provider is Ready and the item
  has a TMDB id (a Series' via its `externalIDs["tmdb"]`, else the
  TVDB-to-TMDB lookup), and its `status.imageSet` is absent or older than
  `refresh`, publish `catalog-imageset` on `CLUSTARR_WORK_CATALOG` with the
  item ref and ids, under the item's pace.
- **Worker** (agent `metadata`): consult the cache (below), ask MediUX for
  what the cache lacks, choose, and answer in the `clustarr-image-sets`
  record (`pkg/records`, compare-and-swap): `{provider, setID, creator,
  setUpdatedAt, artwork[], seasonArtwork[], checkedAt}` or
  `{notFound: true, checkedAt}`.
- **Incorporation.** The loop writes `status.imageSet` on the Series or
  Movie under `catalog` -- the chosen set's id, creator, and its overrides
  -- never the spec: `spec.artwork` and `spec.seasonArtwork` stay the
  owner's, and an automatic choice must never overwrite a pasted one.
- **Serving.** The Plex provider's season poster, season background and
  episode thumbnail read `spec.seasonArtwork` first, then
  `status.imageSet.seasonArtwork`, then TheTVDB's; the item's poster and
  backdrop likewise `spec.artwork`, then `status.imageSet.artwork` (which
  the artwork fetch treats as a `custom` source, so the object store holds
  them and Plex never hotlinks), then the providers'. The series page shows
  "Images: MediUX set <title> by <creator>" with a link, and the Image Set
  dialog's paste still wins over it.

### Caching, so MediUX is asked once

Three layers, all in NATS, all read-through, the agent's alone to write:

1. **`clustarr-mediux` KV** (file-backed, `BucketSpec.Durable`, keys through
   `events.KVKeyToken`):
   - `ids/show/<tvdb>` -> tmdb id (no TTL: an id mapping does not change);
   - `sets/show/<tmdb>`, `sets/movie/<tmdb>`, `sets/collection/<tmdb>` ->
     the raw set list as MediUX answered it, with `fetchedAt`; TTL
     `refresh` (7 days), and a **negative entry** for "no sets" with a
     24-hour TTL, so a show MediUX has nothing for is asked once a day at
     most, not once per refresh;
   - `content/<kind>` -> the `content_ids` list, TTL 24 hours: a lookup for
     an item not in it is answered from the list, with no set query at all.
     One request a day covers the whole library's "is there anything?".
2. **Images** stay behind the ui's `/art/search` proxy for seasons and
   episodes (1-hour memory cache, 32 MiB, the `v=` in the URL as the
   version), and in the artwork object store for the item's own poster and
   backdrop. A later cut can fetch season and episode images into the store
   under their asset id, as AURA caches them on disk, if the proxy's churn
   shows in the metrics.
3. **Pacing.** One limiter per provider in the agent (`spec.rateLimit`,
   default 30 requests a minute, burst 5 -- no limit is published, so the
   default is conservative and the owner can raise it), the sweep that
   publishes image-set tasks capped like the donor search (at most 20
   first-time lookups per namespace per sweep), and a 429 pausing the
   provider for `Retry-After` (else 15 minutes) through the provider's
   health, as the indexers' limits do. Every request carries the
   `User-Agent`, so MediUX can see who is asking.

Worst case for the owner's library (261 series, ~1,150 movies) on first
run: one `content_ids` call, then one set query per item MediUX has
anything for, over an hour at the default pace; afterwards one call per
item per week, and none for items the daily `content_ids` list excludes.

### Not in this cut

- Collection sets applied to Plex collections (the `collection_sets`
  query is in the client; the provider's collection answers can carry a
  poster from it later).
- Fetching every set image into the object store.
- A per-set "pin this set" on the series page beyond the paste: the dialog
  takes a set id or URL and asks the agent for that set, which is the same
  worker path with the choice fixed.

## Open questions for the owner

1. **A token.** Nothing can be verified or recorded without one; the
   shared testing token is pinned in the MediUX Discord under
   `repo-chats > mediux-team/aura`. The client's fixtures are recorded
   from live answers once it exists, as the other clients' were.
2. **Endpoint.** AURA uses `images.mediux.io`; the YAML export and
   Mediux-Posters use `api.mediux.pro`. Default to AURA's, with `baseURL`
   to override.
3. **Automatic or opt-in per series?** The design applies the best set to
   every item with a TMDB id once a provider is configured; a
   `spec.imageSet: none` opt-out per item, or an allowlist of creators
   only, are both one field. **Owner, 2026-10-22: automatic**, with
   `spec.imageSet: none` as the opt-out.

## As built (2026-10-22)

- **API.** `MetadataProviderType` `mediux` (secretRef key `token`;
  `spec.mediux{preferredCreators, languages, quality: optimized|original,
  refresh}`); `Movie.spec.imageSet`/`Series.spec.imageSet` (`auto`, the
  default when unset, or `none`); `Movie.status.imageSet` and
  `Series.status.imageSet` (`ImageSetStatus`: provider, setID, title,
  creator, url, updatedAt, checkedAt, notFound, message, artwork (9),
  seasonArtwork (1000)), owned by the field manager `catalog-imageset`
  alone. The seeds do not seed the provider; the provider controller pings
  with `content_ids(limit: 1)`.
- **Client.** `pkg/metadata/clients/mediux` as designed, plus `ChooseMovie`,
  `SetURL` (`https://mediux.pro/sets/<id>`), `Ping`, `Capabilities`,
  `DefaultRate` 0.5/s burst 5, and `MarshalJSON` on `File` and `ShowSet`
  writing the wire's nested shape: the cache stores sets as JSON and the
  decoders read only that shape, so without it a cached set came back with
  no season posters or titlecards and the completeness terms scored zero
  (the mediuxsets test caught it: the second choice for Edgerunners was
  41472, not 41471).
- **Provider.** `pkg/metadata.ImageSetProvider` (`ImageSetQuery` ->
  `ImageSet{Images[]{Type, Season, Episode, URL}}`), the registry's
  `ImageSets` slot; `pkg/metadata/mediuxsets.Source` is MediUX over the
  cache (`mediuxcache.Cache`, or `Direct` without a bucket), with the
  provider's creators and languages (`spec.language`'s display name when
  `languages` is empty) and the query's seasons and episodes. The gateway
  builds it through `BuildRegistryWith(..., RegistryDeps{MediuxKV})`.
- **Worker.** `app/catalog/worker/imagesets` on the `catalog-imageset`
  durable (`CLUSTARR_WORK_CATALOG`, `clustarr.work.catalog.imageset.>`,
  8 slots, dispatched), answering `clustarr-image-sets`
  (`pkg/records/agentrecords.ImageSets`, 256 KiB values); NotFound, a
  failure (failed, transient) and a rate limit (retry, or failed on the last
  delivery) as the markers worker does; no provider acks unanswered.
- **Stage.** `app/remediation/imageset` (`StageImageSet`, between metadata
  and artwork): lists the namespace's `mediux` providers (enabled, Ready;
  the first's `refresh`), builds `ImageSetInputs` (a movie's TMDB id; a
  series' TMDB id from `status.metadata.externalIDs`, its TheTVDB id, its
  seasons and episode counts), and `app/catalog/imagesets.Plan` decides:
  `none` clears the status; no provider or no id leaves it; an answer for
  the item's ids newer than `checkedAt` is incorporated (a failure keeps
  the previous images, message set, retried after an hour); a check older
  than the refresh asks again, admitted through the dispatch ledger, with a
  request record and task, republished unclaimed inside the window and
  asked afresh after the hour's timeout. The record source
  `S27/clustarr-image-sets` wakes the item.
- **Readers.** `ui/plex.seasonOverride` reads `spec.seasonArtwork` then
  `status.imageSet.seasonArtwork`; `app/catalog/artwork.WithImageSet` puts
  the set's poster and background below `spec.artwork` as custom sources,
  so the object store holds them and Plex never hotlinks the item's own
  images; the item page's "Images" fact shows the pasted set, else the
  chosen one with a link; `/art/search` allows `images.mediux.io`.
- **Pinning and deferral (same day).** `spec.imageSetID` pins a set by its
  MediUX id: the Image Set dialog's policy form takes the id or the set's
  page URL (and the "choose automatically" box, `spec.imageSet`), the
  request carries it (`ImageSetInputs.SetID`, part of `SameItem`, so a
  changed pin is asked for at once, past the refresh), and the source
  fetches the set as it is with no choosing. A MediUX `Retry-After` over
  five minutes defers the record (`Answers.Defer`) and acks; the planner
  waits for `DeferredUntil` and republishes the task under
  `MsgIDForImageSetAt` inside the republish window (`Decision.Resume`),
  else asks afresh after the request timeout.
- **Not built:** the pacing cap of 20 first-time lookups per namespace per
  sweep (the ledger's 8 slots, the agent's 0.5/s limiter and the daily
  content list bound the burst instead), a 429 pausing the provider's
  health condition, collection sets, fetching season and episode images
  into the store.

## Composed sets (2026-10-27)

Owner, 2026-10-27: "use MediUX for images, falling back to TMDB, TVDB,
etc. Ideally, we would find all image types by combining various sets for
a series or movie." One chosen set leaves every slot it lacks to the
providers even when another MediUX set has it: of Cyberpunk: Edgerunners'
14 sets, four have no show poster, two carry only titlecards, three only
a backdrop or season posters (`test/data/metadata/mediux`).

**What MediUX has.** A show set: show poster, show backdrop, season
posters (specials included) and titlecards. A movie set: poster and
backdrop. A collection set: the collection's poster and backdrop, and a
poster and backdrop for each of its films (AURA reads them as a film's
`posters`/`backdrops` with `collection_set`). MediUX has no logos,
banners, clear art or season backgrounds; those stay TMDB's, TheTVDB's and
fanart.tv's, as does every slot no set fills.

**Composition** (`mediux.Compose`), one image per slot:

- The **anchor** is the set the look is built around: the owner's pinned
  set, else the best set with an item poster by preferred creator, poster
  language (a wanted language, then none, then another), completeness
  (season posters, then titlecards), a film's own set over a collection's,
  popularity, recency. Its images are taken first.
- The **poster group** (item poster, backdrop, season posters) follows the
  set that supplies the poster -- the anchor's own, else the best set with
  one -- and fills a slot that set lacks from its creator's other sets,
  then from any set by preferred creator, the image's language and the
  set's own poster-group completeness (poster, season posters, backdrop),
  so a backdrop comes from the set that also gave the season posters
  rather than from a more popular set of one image.
- **Titlecards** are chosen per season: the season's own anchor is the set
  by the anchor's creator, else the preferred creators', with the most
  cards for that season; its gaps fill from the others in the same order,
  so a season mixes styles only where no one set covers it.
- `popularity` is MediUX's rank (1 is the most popular; `popularity_global`
  grows with it), so a lower one wins and none ranks last. The first cut
  ranked it upside down.

**Links.** A set's page is `https://mediux.io/<kind>-set/<id>` (AURA's
`getMediuxBaseUrl`), which the pin form accepts beside the older
`mediux.pro/sets/<id>`.

**Records and status.** Each image carries its `scope` (item, season,
episode: a specials poster is season 0, which the first cut read as the
item's own poster) and the `setID` it came from; the answer and
`status.imageSet.sets` list every set that supplied an image (anchor
first, then by images supplied, at most 32), with `status.imageSet`'s own
`setID`, `title` and `creator` the anchor's. A pinned set is the anchor
and keeps every image it has, poster or no poster, while the item's other
sets fill its gaps; a pin MediUX does not find (gone, or a collection set
the film's list lacks, which no movie-set lookup finds) is passed over and
named in the reason, never a NotFound that clears the item's images.
Only the series' own slots are filled when its shape is known: season
posters of its seasons and the specials, titlecards within each season's
episode count, so a set with cards past a season's end neither leads the
season nor fills the status' 1,000 entries.

**Films in collections.** A film's sets are its movie sets plus the
collection sets' images of that film, asked in a second query
(`movies_by_id { posters(filter collection_set) ... }`, AURA's shape); a
GraphQL error from that query alone (`mediux.ErrQuery`, read from the
body of Directus' HTTP 400 too) leaves the film with its movie sets, since it was not recorded live, while a limit, an auth
failure or a transport error fails the lookup as before.

**Fallback** is per slot and unchanged: `spec.artwork` and
`spec.seasonArtwork` first, then the composed `status.imageSet`, then the
providers' images (TMDB, TheTVDB, fanart.tv by priority).

## Sources

- Kometa MediUX guide: https://kometa.wiki/en/latest/kometa/guides/mediux/
- AURA (MediUX team): https://github.com/mediux-team/AURA --
  `backend/mediux/make_request.go`, `gen_show_sets_by_tmdbid.graphql`,
  `gen_tmdb_id_by_tvdb_id_show.graphql`, `gen_show_set_by_setid.graphql`,
  `image_get_url.go`, `image_download.go`; docs
  https://mediux-team.github.io/AURA/settings/mediux
- Mediux-Posters: https://github.com/Buried-In-Code/Mediux-Posters --
  `mediux_posters/mediux/service.py`, issue #35 (API beta)
- scrape-mediux: https://github.com/zhyph/scrape-mediux (site scraping,
  superseded)
- plex-poster-set-helper: https://github.com/bbrown430/plex-poster-set-helper
