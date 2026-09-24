# Clustarr research note: Plex custom metadata providers

Date: 2026-09-24. Scope: the HTTP protocol a Plex Media Server (PMS >= 1.43.0)
speaks to a **custom metadata provider** registered under Settings → Metadata
Agents → Add Provider, and what a `clustarr plex` provider server would have to
serve so that a Plex movie or TV library takes its metadata from clustarr's
Movie, Series and Episode resources.

Verified against three sources, cited inline as [D], [P] and [A]:

- [D] Plex's official example, `github.com/plexinc/tmdb-example-provider`
  (TypeScript): `docs/MediaProvider.md`, `docs/API Endpoints.md`,
  `docs/Metadata.md`, `src/models/{MediaProvider,Metadata}.ts`,
  `src/utils/guid.ts`, `src/routes/tvRoutes.ts`, `src/services/{Match,Metadata}Service.ts`,
  `src/mappers/TMDBMapper.ts`, `src/app.ts` and its tests. The example implements
  TV only (types 2, 3, 4); movie (type 1) shapes come from the docs.
- [P] `https://developer.plex.tv/pms/#section/API-Info/Metadata-Providers`
  (fetched 2026-09-24). Same content as [D] plus the response paging headers.
- [A] `https://forums.plex.tv/t/announcement-custom-metadata-providers/934384`
  for version, registration and current limits.

Anything not stated by one of the three is marked **UNVERIFIED**. Nothing here
was observed against a running PMS.

---

## 1. Availability, registration, limits [A]

- Introduced in **PMS 1.43.0** ("currently in beta" at the announcement).
- Registration: **Settings → Metadata Agents → Add Provider → enter the URL** of
  the running provider. The URL is the provider *root* (section 2); everything
  else is discovered from it.
- **Only Movie and TV libraries** are supported today; music is "planned but
  requires additional development work".
- **"Only unauthenticated requests are currently supported."** Plex advises
  public-facing providers to wait for authentication. There is no token, no
  header, no query parameter for auth.
- **No stream metadata**: "Metadata support for 'streams' is not yet
  implemented. This would be used to support providing subtitles." A provider
  cannot describe audio/subtitle streams.
- **No provider preferences**: only the fixed query parameters/headers of
  section 3 are passed (language, country, paging, ordering).
- Legacy (Python) agents "will be completely removed from new PMS releases in
  2026"; this protocol is the replacement. "Expect some bugs early on."

---

## 2. The provider root: `GET {root}` → `{"MediaProvider": {...}}` [D MediaProvider.md]

| Field | Type | Required | Meaning |
|---|---|---|---|
| `identifier` | string | Yes | Unique id; **must** start with `tv.plex.agents.custom.` and the suffix may contain only `[a-zA-Z0-9.]` (ASCII letters, digits, periods). Avoid generic suffixes (`tmdb`, `tvdb`) — the user may have other providers. |
| `title` | string | Yes | Human-readable name shown in Plex |
| `version` | string | No | "The version of the API being called" |
| `Types` | array | Yes | `[{ "type": <int>, "Scheme": [{ "scheme": <string> }] }]` |
| `Feature` | array | Yes | `[{ "type": <string>, "key": <path> }]` |

`Scheme[].scheme` "should be identical to the provider identifier".

Numeric types (`Types[].type`, and the match request's `type`):

| name | number |
|---|---|
| `movie` | 1 |
| `show` | 2 |
| `season` | 3 |
| `episode` | 4 |
| `collection` | 18 |

Features (`Feature[].type`):

| type | required | `key` is |
|---|---|---|
| `metadata` | Yes | path of the metadata endpoint (section 5), example `/library/metadata` |
| `match` | Yes | path of the match endpoint (section 4), example `/library/metadata/matches` |
| `collection` | No | path of the collection endpoint, example `/library/collections` (movie libraries only; not covered further here) |

**One parent type per provider** is recommended, not required: "in order to
combine providers, each provider in a combine group must support the types the
other supports as well … If you wish to support both movie and TV Shows,
consider creating two separate providers." A TV provider **must** declare
types 2, 3 **and** 4. The example's root is:

```json
{ "MediaProvider": {
    "identifier": "tv.plex.agents.custom.example.themoviedb.tv",
    "title": "TheMovieDB Example TV Provider", "version": "1.0.0",
    "Types": [ { "type": 2, "Scheme": [ { "scheme": "tv.plex.agents.custom.example.themoviedb.tv" } ] },
               { "type": 3, "Scheme": [ ... same ... ] }, { "type": 4, "Scheme": [ ... same ... ] } ],
    "Feature": [ { "type": "metadata", "key": "/library/metadata" },
                 { "type": "match",    "key": "/library/metadata/matches" } ] } }
```

**Feature keys are relative to the provider root URL.** The example mounts its
router at `/tv` (`app.use('/tv', tvRoutes)` in `src/app.ts`), serves the root
at `GET /tv`, yet advertises `key: "/library/metadata"` and answers at
`/tv/library/metadata/...`. That only works if PMS joins `key` onto the URL
the user entered. Inferred from the example's layout, not stated in prose.

Doc drift to be aware of: `MediaProvider.md`'s JSON example uses
`tv.plex.agents.johnz.tmdb` without the `.custom.` segment, contradicting its
own prose two paragraphs later; the example code and its test
(`/^tv\.plex\.agents\.custom\./`) follow the prose. Use the prefix.

---

## 3. Conventions common to every endpoint [D API Endpoints.md, P]

Headers, **also accepted as query parameters** of the same name (the example
reads `req.headers['x-plex-language']` then `req.query['X-Plex-Language']`):

| Header | Support required | Meaning |
|---|---|---|
| `X-Plex-Language` | No | IETF tag with region, e.g. `en-US`, `de-DE` |
| `X-Plex-Country` | No | ISO 3166 two-letter code; certification country, may pick release dates |
| `X-Plex-Container-Size` | Yes | paging: maximum items in one response |
| `X-Plex-Container-Start` | Yes | paging: starting index |

Paging: `MediaContainer.totalSize` is the size of the whole set, `size` the
count in this response. Defaults when absent: page size **20**; start "will
always be the 1 (the first item)" — the example parses the default as `'1'`
while every `MediaContainer` example carries `"offset": 0`. Whether the start
index is 0- or 1-based is **UNVERIFIED**; treat `X-Plex-Container-Start` as
what PMS sends back to you and honour it as an offset. Only `/children` and
`/grandchildren` **require** paging; "passing no paging headers here will only
return the first 20". [P] adds `X-Plex-Container-Focus-Key` (request, "key to
center results on") and the **response** headers `X-Plex-Container-Start`
(actual offset) and `X-Plex-Container-Total-Size` ("optional but typical").

Return codes: `200` item/match returned; `404` unknown `ratingKey` ("Metadata
feature only"); `400` malformed request; `500` internal error.

Response customization (`includeFields`, `excludeFields`, `includeElements`,
`excludeElements`, plus [P]'s `includeOptionalFields`/`includeOptionalElements`)
is optional: "you can safely ignore when these parameters are passed", and
"these inclusions/exclusions are treated as requests, not guarantees". PMS
does send them — the documented match body carries
`"includeElements": "Metadata,Children"` and
`"includeFields": "guid,parentGuid,title,parentTitle,thumb,parentThumb,index,originallyAvailableAt,year,type"`.

Every response is `{"MediaContainer": {...}}` with `offset`, `totalSize`,
`identifier`, `size` and a `Metadata` (or, for images, `Image`) array.
`identifier` is "the provider identifier"; the docs' examples show Plex's own
`tv.plex.provider.metadata`, the example code returns its own identifier.

---

## 4. Match: `POST {match key}` [D API Endpoints.md]

Body is JSON. "Support Required?" is whether the provider must honour the
attribute; "Optional" is whether PMS may omit it.

| Attribute | Type | Support required | Optional | Meaning |
|---|---|---|---|---|
| `type` | int | Yes | No | 1, 2, 3 or 4 |
| `title` | string | Yes | No\* | \*movies and shows only |
| `parentTitle` | string | Yes | No\* | the show's title, \*seasons only |
| `grandparentTitle` | string | Yes | No\* | the show's title, \*episodes only |
| `includeChildren` | int 1/0 | Yes (shows/seasons) | Yes\* | return `Children`; \*only optional for movie and episode |
| `episodeOrder` | string | No | Yes | a `SeasonType.id`; if no seasons exist for it, return no season data |
| `year` | int | Yes | Yes | release year |
| `guid` | string | Yes | Yes | external id, e.g. `tvdb://12345`, `imdb://tt1234567`, `tmdb://105` |
| `index` | int | Yes | Yes\* | season number (type 3) or episode number (type 4) |
| `parentIndex` | int | Yes | Yes\* | season number (type 4) |
| `filename` | string | No | Yes | relative media path, e.g. `/Movies/Back to the Future (1985).mp4`; "for TV Shows and Seasons this will return the first file found" |
| `date` | string | Yes | Yes\* | episode air date, sent "if `index` and `parentIndex` are not available" |
| `manual` | int 1/0 | Yes | Yes | `1`: return the best matches ordered by confidence (the example caps at 5); absent/`0`: best match only |
| `includeAdult` | int 1/0 | No | Yes | filter explicit results unless `1` |

The example's `MatchRequest` type mirrors this list exactly and its episode
matcher throws (→ 500) when neither `index`+`parentIndex` nor `date` is
present; a 400 would fit the return-code table better.

Response: a `MediaContainer` whose `Metadata[]` holds `Metadata` objects
(section 5). "By default should only return the best result only." The
documented season match (body `{"parentTitle":"Adventure Time","type":3,
"index":8,"filename":"...","includeChildren":1,...}`) returns, per the
`includeFields` it asked for: `guid`, `type`, `thumb`, `title`, `parentTitle`,
`parentThumb`, `parentGuid`, `index`, `originallyAvailableAt`, `year` and a
`Children` block of episodes each with `guid`, `type`, `thumb`, `title`,
`parentTitle`, `parentThumb`, `parentGuid`, `index`, `originallyAvailableAt`,
`year`. **Note `ratingKey` and `key` are absent from that response** even
though section 5 marks them required; PMS asked for a trimmed set, so it must
recover the `ratingKey` from `guid` (section 6). Whether a match result
*without* `guid` is usable is **UNVERIFIED**; the example always returns the
full object, which is the safe choice.

Match handling in the example (`MatchService`): type 2 with `guid` → resolve
the external id (`tvdb://`, `imdb://`, `tmdb://`) first, else search by
`title`+`year`; type 3 → find the show by `parentTitle`, then season `index`;
type 4 → find the show by `grandparentTitle`, then `parentIndex`/`index`, else
scan seasons for an episode whose `air_date === date`.

---

## 5. Metadata: `GET {metadata key}/{ratingKey}` [D Metadata.md, API Endpoints.md]

Query parameters:

| Param | Support required | Meaning |
|---|---|---|
| `includeChildren` | Yes (shows and seasons) | `1` → include `Children` (a show returns its seasons, a season its episodes) |
| `episodeOrder` | No | a `SeasonType.id`; return the seasons of that ordering, none if it has none. (`Metadata.md` once calls it `episodeOrdering`; the route and [P] use `episodeOrder`.) |

Returns `{"MediaContainer": {"offset":0,"totalSize":1,"identifier":...,"size":1,"Metadata":[<one object>]}}`.

### 5.1 Core attributes, all four types

| Field | Type | Required | Meaning |
|---|---|---|---|
| `ratingKey` | string | **Yes** | provider-unique id; charset `[a-zA-Z0-9_-]` |
| `key` | string | **Yes** | "API endpoint path to retrieve this metadata". The examples use `/library/metadata/{ratingKey}` for movies and episodes and `/library/metadata/{ratingKey}/children` for shows and seasons (`constructMetadataKeyWithChildren`). |
| `guid` | string | **Yes** | `{scheme}://{type}/{ratingKey}` (section 6) |
| `type` | string | **Yes** | `movie`, `show`, `season`, `episode` |
| `title` | string | **Yes** | |
| `originallyAvailableAt` | string | **Yes** | ISO 8601 `YYYY-MM-DD` (the example emits `''` when TMDB has none, which is how it satisfies "required") |
| `thumb` | string | No | "A publicly accessible URL to the default poster/thumbnail" |
| `art` | string | No | "A publicly accessible URL to the default background artwork" |
| `contentRating` | string | No | `PG`, `R`, `TV-PG`; non-US: prefix the lower-case country and a slash, e.g. `za/15` (the example renders `${country.toLowerCase()}/${rating}` for any non-US `X-Plex-Country`) |
| `originalTitle` | string | No | title in the original language when the requested language differs |
| `titleSort` | string | No | only to override PMS's own sort title ("Quiet Place, A") |
| `year` | int | No | |
| `summary` | string | No | full synopsis |
| `isAdult` | bool | No | |

Movie, show, episode: `duration` (int, **milliseconds**; the example does
`runtime * 60 * 1000`, and averages `episode_run_time` for a show).

Movie and show: `tagline`, `studio` (a single string, "primary production
studio"), `theme` (URL to an MP3, ~30 s).

Season and episode (all required unless noted):

| Field | Meaning |
|---|---|
| `parentRatingKey` | the show for a season, the season for an episode |
| `parentKey` | its metadata path |
| `parentGuid` | its guid |
| `parentType` | `show` for a season, `season` for an episode |
| `parentTitle` | its title |
| `parentThumb` (optional) | its poster URL |
| `index` | season number for a season, episode number for an episode |

Episode only (all required unless noted): `grandparentRatingKey`,
`grandparentKey`, `grandparentGuid`, `grandparentType` (`show`),
`grandparentTitle`, `grandparentThumb` (optional), `parentIndex` (season
number). The docs' examples additionally carry `parentArt` and `grandparentArt`
too; only the fields listed above are in the tables.

### 5.2 Child arrays

| Array | Status | Object |
|---|---|---|
| `Image` | highly recommended | `{ "type", "url", "alt"? }` — see section 7 |
| `OriginalImage` | recommended | same shape, original-language assets when the requested language differs |
| `Genre` | recommended | `{ "tag", "originalTag"? }` |
| `Guid` | optional | `{ "id": "provider://id" }`, e.g. `imdb://tt0088763`, `tmdb://105`, `tvdb://299`. "Internally supported providers": `imdb`, `tmdb`, `tvdb`. Used to match across combined providers. |
| `Collection` | optional, movies only, needs the `collection` feature | `{ "guid", "key", "tag", "summary"?, "art"?, "thumb"? }` |
| `Country` | optional | `{ "tag": "United States of America" }` (full name) |
| `Role`, `Director`, `Producer`, `Writer` | recommended | people: `{ "tag": <full name>, "thumb"?: <photo URL>, "role"?: <character or "Director">, "order"?: <int> }` |
| `Similar` | optional | `{ "guid", "tag"? }` |
| `Studio` | optional | `{ "tag" }` (the examples carry both the scalar `studio` and this array) |
| `Rating` | optional | see below |
| `Network` | optional, shows only | `{ "tag": "Cartoon Network" }` |
| `SeasonType` | optional, shows only | `{ "id", "source", "tag", "title" }`; requires `episodeOrder` support |
| `Children` | required for shows and seasons when `includeChildren=1` | `{ "size": <int>, "Metadata": [...] }` |

**`Rating` object, exactly:**

```json
{ "image": "imdb://image.rating", "type": "audience", "value": 8.5 }
```

- `image` (string, required): a badge identifier from a **closed** list —
  `imdb://image.rating`, `themoviedb://image.rating`,
  `rottentomatoes://image.rating.ripe` (critic), `rottentomatoes://image.rating.upright`
  (audience). "Adding new types is not currently supported."
- `type` (string, required): `audience` or `critic`; "always `audience` for
  user-generated ratings".
- `value` (float, required): 0–10 (the TMDB example passes `vote_average`
  straight through, e.g. `8.321`).

There is no field for the vote count and no way to show a clustarr-branded
badge.

**`SeasonType`** example ids from Plex's own provider: `tmdbAiring`,
`tvdbAiring`, `tvdbDvd`, `tvdbAbsolute` (`source` `tmdb`/`tvdb`, `tag`
`Aired`/`DVD`/`Absolute`, `title` "TheTVDB (DVD)"). The example provider names
its own from TMDB episode groups.

**`Children`**: "it is expected that all the child objects be returned in
this array" — it is not paged; trim the child objects to the required
attributes if the show is huge. Children is nested *inside* the parent's
`Metadata` object, not at the container level.

---

## 6. GUIDs and ratingKeys [D Metadata.md, guid.ts]

`{scheme}://{metadataType}/{ratingKey}`, e.g.
`tv.plex.agents.custom.johnz.tmdb://movie/tmdb-movie-19934`,
`tv.plex.agents.custom.barkley.tvdb://show/78874`.

- `scheme` = the provider `identifier` (with its `tv.plex.agents.custom.` prefix).
- `metadataType` = `movie` | `show` | `season` | `episode`.
- `ratingKey`: **`[a-zA-Z0-9_-]` only** — no periods, no slashes, no colons.
  `validateRatingKey` enforces `/^[a-zA-Z0-9_-]+$/` and `constructGuid` throws
  otherwise. It "should match the `ratingKey` attribute" and is what PMS puts
  in `GET {metadata key}/{ratingKey}`.

The example encodes structure into the key (`tmdb-show-15260`,
`tmdb-season-15260-1`, `tmdb-episode-15260-1-5`, and
`tmdb-season-15260-1-<groupType>-<groupId>` for alternate orderings) and
parses it back with regexes; nothing in the protocol requires that.

External ids in `Guid[].id` and the match body's `guid` use the bare provider
name as scheme: `imdb://tt0088763`, `tmdb://105`, `tvdb://152831`.

---

## 7. Images [D Metadata.md, API Endpoints.md]

`Image` object: `type` (required), `url` (required, "Full URL to the image
asset"), `alt` (optional, "typically movie title").

`type` values: `background`, `backgroundSquare`, `clearLogo`, `coverPoster`,
`snapshot`. Guidance: "supply at least `coverPoster` (or `snapshot` for
episode items) and `background`. `clearLogo` and `backgroundSquare` are also
utilized inside Plex client applications for movies and TV shows and should be
supplied to provide the best experience". The docs' examples carry
`background`, `backgroundSquare`, `clearLogo`, `coverPoster` on movies and
shows; `background`, `backgroundSquare`, `coverPoster` on seasons; a single
`snapshot` on episodes.

Images endpoint (recommended, part of the metadata feature):
`GET {metadata key}/{ratingKey}/images` →

```json
{ "MediaContainer": { "offset": 0, "totalSize": 3, "identifier": "...", "size": 3,
    "Image": [ { "type": "coverPoster", "url": "https://..." },
               { "type": "background", "url": "https://..." },
               { "type": "clearLogo",  "url": "https://....png" } ] } }
```

It lists **all** available assets (the example returns every TMDB poster,
backdrop, logo and still, in every language it fetched), whereas
`Metadata.Image[]` in the example carries one of each type. The example sets
`thumb` to the same URL as its `coverPoster` and `art` to its `background`.

**UNVERIFIED**: whether PMS downloads `thumb`/`art`, `Image[].url`, the
`/images` listing, or all three, and which wins when they differ. The docs
say only that `thumb`/`art` are "publicly accessible" URLs and `Image` is
"highly recommended". **No size, aspect-ratio or format guidance** appears in
any of the three sources; the example passes TMDB `original` sizes.

---

## 8. Children and grandchildren [D API Endpoints.md, tvRoutes.ts]

`GET {metadata key}/{ratingKey}/children` and `.../grandchildren`, for shows
and seasons: a `MediaContainer` of `Metadata` objects — a show's children are
its seasons and its grandchildren its episodes; a season's children are its
episodes. Both **must** honour `X-Plex-Container-Size`/`X-Plex-Container-Start`
(defaults 20 / first item) and accept `episodeOrder` and the language/country
headers. The example does not set the [P] response headers
(`X-Plex-Container-Start`, `X-Plex-Container-Total-Size`); it returns the
counts in the container only.

---

## 9. The flow PMS follows

Inferred from the feature definitions and the example; the order of PMS's
calls was not observed (**UNVERIFIED** beyond what the docs imply):

1. `GET {root}` once at Add Provider time to read `identifier`, `Types` and
   `Feature` keys.
2. Per library item: `POST {match key}` with the hints of section 4; PMS keeps
   the winning `guid` (`{scheme}://{type}/{ratingKey}`).
3. `GET {metadata key}/{ratingKey}` (with `includeChildren=1` for shows and
   seasons, `episodeOrder=<id>` if the user chose one) for the full object.
4. `GET .../{ratingKey}/images` for the artwork gallery.
5. `GET .../{ratingKey}/children` / `/grandchildren`, paged, for seasons and
   episodes.

"Fix match…" in the Plex UI is the `manual: 1` form of step 2.

---

## 10. Open questions (UNVERIFIED)

- Feature-key resolution relative to the entered root URL (section 2) is
  inferred from the example's mount point only.
- 0- vs 1-based `X-Plex-Container-Start`.
- Whether PMS needs `ratingKey`/`key` in a match result or derives everything
  from `guid`; whether `thumb`/`art` or `Image[]` drives artwork; image sizes.
- How PMS behaves when `originallyAvailableAt` is `""` (the example emits it).
- Whether PMS sends `filename` for movies in practice (docs: support not
  required, may be sent).
- Any request timeout or retry policy on the PMS side.
- Whether an `http://` (non-TLS) provider URL is accepted; the example runs on
  `http://localhost:3000` and the announcement says "locally running".

---

## 11. What this means for clustarr

**Endpoints.** A `clustarr plex` HTTP server (a reader over the same
controller-runtime cache the ui uses; it never writes) exposes **two roots**,
one per parent type, so each can be combined with other providers:

| Path | Serves |
|---|---|
| `GET /plex/movies` | root, `Types: [1]`, identifier e.g. `tv.plex.agents.custom.clustarr.movies` |
| `POST /plex/movies/library/metadata/matches` | match type 1 |
| `GET /plex/movies/library/metadata/{rk}` | Movie |
| `GET /plex/movies/library/metadata/{rk}/images` | Movie artwork |
| `GET /plex/tv` | root, `Types: [2,3,4]`, identifier e.g. `tv.plex.agents.custom.clustarr.tv` |
| `POST /plex/tv/library/metadata/matches` | match types 2, 3, 4 |
| `GET /plex/tv/library/metadata/{rk}` | Series / season / Episode, `includeChildren`, `episodeOrder` |
| `GET /plex/tv/library/metadata/{rk}/images` | artwork |
| `GET /plex/tv/library/metadata/{rk}/children`, `/grandchildren` | paged |

The user enters `http://<service>/plex/movies` and `http://<service>/plex/tv`
as two providers. Feature `key`s stay `/library/metadata` and
`/library/metadata/matches` (section 2). Every response is the
`{"MediaContainer": ...}` envelope with the provider's own `identifier`;
unknown `ratingKey` → 404; both paging headers and their query-parameter forms
must be read, and `X-Plex-Container-Start`/`X-Plex-Container-Total-Size`
should be set on children responses.

**ratingKeys.** The charset is `[a-zA-Z0-9_-]`. Kubernetes object **UIDs**
(`[0-9a-f-]`) fit; object **names do not** in general, because a DNS-1123
name may contain `.`. Use `metadata.uid` for Movie, Series and Episode. Seasons
have no clustarr object (Episodes carry `spec.seasonNumber`), so a season key
must be synthesised — e.g. `<series-uid>-s<NN>` (the `-` and digits are
legal) — and parsed back. The guid is then
`tv.plex.agents.custom.clustarr.tv://episode/<uid>`. Movie/Series/Episode
`key` values follow the example: `/library/metadata/<rk>` for movies and
episodes, `/library/metadata/<rk>/children` for series and seasons.

**Matching against clustarr's spec/status.** Type 1 with `guid`
`tmdb://N` → `Movie.spec.tmdbID`; `imdb://ttN` → `status.metadata.externalIDs`;
else `title` + `year` (±1, as `pkg/decision/identity.go` already does). Type
2 `tvdb://N` → `Series.spec.tvdbID`, `tmdb://`/`imdb://` via
`status.metadata.externalIDs`, else title/year. Type 3 → the Series by
`parentTitle`, season `index`. Type 4 → Series by `grandparentTitle`, then
`Episode.spec.seasonNumber == parentIndex && spec.episodeNumber == index`,
falling back to `status.airDate == date`. `manual: 1` returns a ranked list.

**Fields clustarr can fill today** (from `api/catalog/v1alpha1`):

| Plex | Movie (`status.metadata`) | Series (`status.metadata`) | Episode (`status`) |
|---|---|---|---|
| `title`, `originalTitle`, `titleSort` | `title`, `originalTitle`, `sortTitle` | `title`, `sortTitle` | `title` |
| `summary` | `overview` | `overview` | `overview` |
| `year` | `year` | `year` | `airDate.UTC().Year()` |
| `contentRating` | `certification` | `certification` | inherit the series' |
| `duration` | `runtimeMinutes * 60000` | `runtimeMinutes * 60000` | `runtimeMinutes * 60000` |
| `Genre[]` | `genres` | `genres` | — |
| `Guid[]` | `spec.tmdbID` → `tmdb://`, `externalIDs` | `spec.tvdbID` → `tvdb://`, `externalIDs` | `tvdbID` → `tvdb://` |
| `studio` / `Network[]` | — | `network` | — |
| `Collection[]` | `collection.{tmdbID,name}` (needs the `collection` feature) | — | — |
| `Image[]`, `thumb`, `art` | `images[]` | `images[]` | — |
| `originallyAvailableAt` | earliest of `inCinemas`/`digitalRelease`/`physicalRelease` | **none** | `airDate` |

`Image.type` maps `poster` → `coverPoster`, `fanart` → `background`, `logo`
→ `clearLogo`, `screenshot` → `snapshot`; `thumb` = the `coverPoster` URL and
`art` = the `background` URL, as the example does. Nothing in clustarr yields
`backgroundSquare`. Episodes carry no images in `EpisodeStatus`, so their
`snapshot` is empty unless the metadata gateway starts storing stills.

**Required fields clustarr cannot fill:** `originallyAvailableAt` is required
on every type and `SeriesMetadata` has **no first-aired date** — it must be
added to the gateway (TVDB and TMDB both provide it) or derived from the
earliest Episode `airDate`; a synthesised season takes its earliest episode's
`airDate`. **Optional fields with no clustarr source:** `Rating[]` (no ratings
in `MovieMetadata`/`SeriesMetadata`/`EpisodeStatus`, and the badge list is
closed to `imdb`/`themoviedb`/`rottentomatoes` anyway), `Role`/`Director`/
`Writer`/`Producer` (no cast or crew), `tagline`, `Country[]`, `theme`,
`Similar[]`, `isAdult`, `OriginalImage[]`. Rendering a rating overlay in
Plex therefore needs the gateway to keep TMDB's `vote_average` (as
`themoviedb://image.rating`, type `audience`) and any IMDb rating as
`imdb://image.rating`; there is no way to display clustarr's own quality or
custom-format score as a badge.

**URLs must be absolute and reachable from PMS.** `thumb`, `art` and
`Image[].url` are "publicly accessible"/"full" URLs. clustarr's `Image.url`
values are the providers' own absolute URLs (TMDB, fanart.tv), so pass-through
works only if the PMS host has Internet access; an air-gapped PMS needs the
provider to proxy or cache images under its own absolute base URL, which
means the server must know its externally reachable address (the example
carries a `BASE_URL` setting for exactly this).

**Scope and security.** Only movies and TV are possible today [A]; Music,
Books and the rest stay on Plex's own agents. The server must be
**unauthenticated**, so it belongs on the cluster network reachable by PMS
(a `ClusterIP` Service, or the existing ui Service on a distinct path) and
never on a public ingress; it reveals the whole catalog to anyone who can
reach it. No subtitle/stream information can be offered through it, so
captionarr's sidecars reach Plex through the filesystem as before.
