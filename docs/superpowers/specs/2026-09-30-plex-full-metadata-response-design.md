# The Plex provider answers with the full Metadata Response

**Date:** 2026-09-30
**Status:** Proposed. The owner approved the design in conversation on
2026-09-30 and chose:

- **Storage is hybrid.** Scalars and small lists go in `status.metadata`.
  People and similar titles go in a NATS KV document per item.
- **Language support is the configured language plus the original.**
  `MetadataProvider.spec.language` and `spec.region` are honored, the
  original-language fields are added when Plex asks in another language,
  and `X-Plex-Country` picks the content rating.
- **Media Queries are out of scope for now.**

**Source:** Plex's PMS API documentation (developer.plex.tv/pms, API
1.2.3), sections "Metadata Providers", "MediaProvider Response" and
"Metadata Response". They were read in full from the OpenAPI document on
2026-09-30. Research note: `docs/research/plex-metadata-provider.md`.

## 1. Why

In Plex, a movie matched through clustarr's provider shows title, year,
runtime, genres, ratings, summary and artwork, and nothing else. For
"Weekend" (2011, tmdb 79120) in "Clustarr Movies" on kind-cluster-plex:

- no content rating, studio, tagline or country;
- no cast, director, writer or producer;
- no similar titles.

Seasons carry the series' art, and episodes carry no image at all.

Three causes, found by tracing the item from Plex's library database back
to the provider response and then to the Movie's status (2026-09-30):

1. **A bug: no Movie ever gets a certification.**
   - `tmdb.mapReleaseDates` copies each release's certification into the
     client model (`pkg/metadata/clients/tmdb/tmdb.go`).
   - Nothing promotes one to `Movie.Certification`.
   - `app/catalog/metadata/patch.go` then writes the empty value, and drops
     the per-release certifications when it writes `ReleaseDates`.
   - `Registry.Lookup` also passes an empty region to every call.
2. **The catalog never fetches most of the schema.**
   - TMDB's detail call appends only `release_dates,external_ids,alternative_titles`.
     Tagline, companies, countries, `adult` and credits are dropped or never
     requested.
   - TVDB's series call never decodes network, companies, content ratings,
     characters, season artwork or season types.
   - TVDB's episode list maps no image.
   - Both clients hard-code English, ignoring the `MetadataProvider`'s
     `spec.language` and `spec.region`.
3. **`ui/plex` emits a subset.**
   - It declares `tagline`, `studio`, `theme`, `isAdult` and `Country` but
     never fills them.
   - It has no people arrays, `Similar`, `Studio[]`, `SeasonType`,
     `parentArt`, `grandparentArt` or episode `snapshot`.
   - It reads neither `X-Plex-Language`, `X-Plex-Country` nor the
     response-customization parameters.
   - It decodes `episodeOrder` and ignores it.

"Video / Audio: None" in the same screenshot is not the provider's. It is
Plex's media analysis, which was still working through the new library, at
about 28 items a minute. No provider can supply streams.

## 2. The target: every field of the schema

Everything the docs define for movie, show, season and episode must be
emitted whenever clustarr has a source for it.

**Core fields (every type)**
- `ratingKey`, `key`, `guid`, `type`, `title`, `originallyAvailableAt`
- `thumb`, `art`, `contentRating`, `originalTitle`, `titleSort`, `year`,
  `summary`, `isAdult`

**By type**
- Movie, show and episode: `duration`.
- Movie and show: `tagline`, `studio`, `theme`.
- Season and episode:
  - `parentRatingKey`, `parentKey`, `parentGuid`, `parentType`,
    `parentTitle`, `parentThumb`, `index`;
  - `parentArt`, which appears in the documentation's examples but not its
    tables.
- Episode:
  - `grandparentRatingKey`, `grandparentKey`, `grandparentGuid`,
    `grandparentType`, `grandparentTitle`, `grandparentThumb`,
    `parentIndex`;
  - `grandparentArt`, also examples-only.

**Arrays**
- `Image[]`, with `type` and `url`, plus `alt`.
- `OriginalImage[]`.
- `Genre[]`, with `tag` and `originalTag`.
- `Guid[]`.
- `Country[]`.
- `Role[]`, `Director[]`, `Producer[]`, `Writer[]`, each with `tag`,
  `thumb`, `role` and `order`.
- `Similar[]`, with `guid` and `tag`.
- `Studio[]`.
- `Rating[]`, already emitted.
- `Network[]`, for shows.
- `SeasonType[]`, for shows, with `id`, `source`, `tag` and `title`.
- `Children`, already emitted.

**Protocol parameters**
- `X-Plex-Language` and `X-Plex-Country`, as a header or a query parameter.
- `includeFields`, `excludeFields`, `includeElements` and
  `excludeElements`.
- `episodeOrder`.

## 3. Catalog changes

The metadata gateway stays the sole writer of `status.metadata` on Movie
and Series, and of the Episode metadata leaves it owns today.

### 3.1 The certification fix

- `ReleaseDate` gains `certification` (optional string).
- `MovieMetadata` gains `certifications []Certification{country, rating}`,
  with `MaxItems=60` and `listMapKey=country`. It holds one entry per
  country: that country's theatrical certification, else its first
  non-empty one.
- `certification` becomes the configured region's rating, falling back to
  the origin country's, then the US one.
- The gateway passes `spec.region` to every lookup. Today it passes an
  empty string.
- Series take their certifications from TVDB's `contentRatings`, the same
  way.

### 3.2 Language

- The TMDB and TVDB clients take `FetchOptions{Language, Region}`. The type
  already exists in `pkg/metadata/provider.go` and nothing passes it yet.
  The registry fills it from the `MetadataProvider`.
- When the original language differs from `spec.language`, the gateway
  makes one more call in the original language. That call supplies:
  - `originalTitle`;
  - `originalGenres []string` (`MaxItems=30`, in genre order);
  - the images whose language is the original one.
- `Image` gains an optional `language` (ISO 639-1), so both sets live in
  the existing `images` list (`MaxItems=50`).

### 3.3 Movies (TMDB)

`append_to_response` becomes
`release_dates,external_ids,alternative_titles,credits,recommendations`,
with images fetched as they are today.

| Field | Source | Stored as |
| --- | --- | --- |
| tagline | `tagline` | `tagline string` |
| studios | `production_companies[].name` | `studios []string`, `MaxItems=10` |
| countries | `production_countries[].name` | `countries []string`, `MaxItems=10` |
| isAdult | `adult` | `adult bool` |
| certifications | `release_dates` | §3.1 |
| people | `credits.cast`, `credits.crew` | KV document (§4) |
| similar | `recommendations.results` | KV document (§4) |

`Studio[]` keeps TMDB's order, and `studio` is the first entry.

### 3.4 Series and seasons (TVDB)

`/series/{id}/extended` is called as today (`meta=translations`). Its
extended response already carries `companies`, `contentRatings`,
`characters`, `originalNetwork`, `latestNetwork`, `seasons` (with images)
and `seasonTypes`; the client stops dropping them. Field names and artwork
type ids are taken from the recorded fixtures (§6), not from memory.

| Field | Source | Stored as |
| --- | --- | --- |
| networks | `originalNetwork`, `latestNetwork` | `networks []string`, `MaxItems=5`; `network` stays the first |
| studios | `companies` of type "Production Company" | `studios []string`, `MaxItems=10` |
| countries | `originalCountry`, as a country name | `countries []string`, `MaxItems=5` |
| certifications | `contentRatings[]{country, name}` | §3.1 |
| season artwork | `seasons[].image`, plus the season-poster and season-background artworks | `seasonImages []SeasonImage{season, type, url}`, `MaxItems=400` |
| season types | `seasonTypes[]` | `seasonTypes []SeasonTypeRef{id, name}`, `MaxItems=10` |
| people | `characters` (cast and crew) | KV document (§4) |
| tagline | TMDB `tv/{tmdbID}` when a TMDB id and key exist | `tagline string` |

Season artwork lives on `SeriesMetadata`, not on `status.seasons`, so the
Series controller stays the only writer of `status.seasons`.

### 3.5 Episodes (TVDB)

- **Still image:** the episode list's `image` becomes
  `EpisodeStatus.images []Image` (`MaxItems=4`, type `screenshot`).
- **People:** the gateway fetches `/episodes/{id}/extended` for
  `characters` (guest stars, directors, writers) only for episodes that
  have a file. This bounds the number of calls to the library, not the
  catalog, and the results go to the KV document.

### 3.6 Caps and objects

- Every new list gets `+kubebuilder:validation:MaxItems`, with the same
  truncation in the gateway, per CLAUDE.md's invariant.
- No floats are added.
- People never go into a CRD. That is the point of §4: every service
  caches Episodes, and the Episode list is already about 57 MB.

## 4. The extended-metadata KV bucket

- **Bucket:** `clustarr-metadata-extended`, file storage, no TTL. It is
  added to the topology in `pkg/events` and must fit `ForSingleNode`'s
  memory budget.
- **Key:** `events.KVKeyToken("<kind>/<uid>")`, where kind is `movie`,
  `series` or `episode`.
- **Writer:** the metadata gateway alone, on every refresh. It replaces the
  document whole and deletes it when the item is deleted, as the
  metadata-cache entries are.
- **Reader:** `ui/plex`, through the ui's existing read-only bus
  connection, with `Get` only.

Document, as JSON:

```json
{
  "role":     [{"name": "", "character": "", "order": 1, "photo": "https://image.tmdb.org/..."}],
  "director": [{"name": "", "job": "Director", "photo": ""}],
  "writer":   [{"name": "", "job": "Screenplay", "photo": ""}],
  "producer": [{"name": "", "job": "Producer", "photo": ""}],
  "similar":  [{"title": "", "year": 2011, "tmdbID": 0, "tvdbID": 0}]
}
```

- **Caps:** `role` 50, each other list 20, and 64 KiB for the whole
  document. The gateway truncates, cast by `order`.
- **Crew mapping (TMDB):**
  - department "Directing" with job "Director" → `director`;
  - department "Writing" → `writer`;
  - job "Producer" or "Executive Producer" → `producer`.
- **Crew mapping (TVDB):** `characters[].peopleType` goes to the list of
  the same name.
- **A missing document is not an error.** The item simply has no people or
  similar titles.

**Guards**
- `TestUINeverWrites` gains the KV write methods (`Put`, `Create`,
  `Update`, `Delete`, `Purge`) in its banned list.
- A contract test runs against a real embedded NATS server. It covers the
  key grammar for each kind, a round-trip, and a missing key returning
  "none".

## 5. The provider (`ui/plex`)

`plex.Options` gains:

- `Extended func(ctx context.Context, kind, uid string) (Extended, bool, error)`,
  the KV read. When it is nil, nothing is emitted from it.
- `PhotoURL func(src string) string`, the ui's signed `/art/search` proxy
  (`ui/searchart.go`), made absolute with the external URL. Person photos
  go through it and are never hot-linked. The proxy already allows
  `image.tmdb.org` and `artworks.thetvdb.com`.

### 5.1 Fields by type

- **Movie.** Every §2 field that has a source:
  - `contentRating` (§5.2);
  - `tagline`, `studio` and `Studio[]`;
  - `Country[]`, with full names;
  - `isAdult`, emitted only when true;
  - `Role`, `Director`, `Writer`, `Producer`, `Similar`;
  - `Image[]` with `alt` set to the title;
  - `OriginalImage[]` and `Genre.originalTag` (§5.3).
- **Show.** The same set, plus:
  - `Network[]`, where `studio` is the first studio;
  - `SeasonType[]`, one entry: the order clustarr stores for the series
    (`EffectiveEpisodeOrder`), `source: "tvdb"`.
- **Season.**
  - Its own poster from `seasonImages`, falling back to the series'.
  - `parentArt` is the series' background.
  - "Specials" for season 0.
- **Episode.**
  - A `snapshot` image from `images`.
  - `parentArt`, `grandparentArt`, `grandparentThumb`.
  - `contentRating` inherited from the series.
  - People from the episode's KV document.
- **Similar.** An item in the catalog gets its clustarr guid
  (`<identifier>://<type>/<uid>`). Any other item gets `tmdb://N` or
  `tvdb://N`. `tag` is the title.
- **`theme`.** Emitted only from a source verified reachable when this is
  built. The candidate is Plex's own TV theme host for shows. With no
  verified source it is omitted, and the reason goes in the research note.
- **`backgroundSquare`.** No source exists in TMDB, TVDB or fanart.tv, so
  it is not emitted. This is recorded in the research note.

### 5.2 `X-Plex-Country`

`contentRating` is chosen from `certifications`:

1. the requested country;
2. otherwise `spec.region`;
3. otherwise the origin country;
4. otherwise the US.

A rating from any country other than the US is written `cc/rating` in
lower case (`gb/18`), as the docs require. The value is read from the
header or the query parameter, the header winning.

### 5.3 `X-Plex-Language`

- The response is always in the configured language.
- When the request's language differs from the item's `originalLanguage`,
  it adds:
  - `originalTitle`;
  - `OriginalImage[]`, the images whose `language` is the original;
  - `Genre[].originalTag`, pairing `genres[i]` with `originalGenres[i]`.
- With no request language, it answers as for `spec.language`.

### 5.4 Response Customization

`includeFields`, `excludeFields`, `includeElements` and `excludeElements`
are applied after rendering, as a filter over each Metadata object, its
`Children` and the objects inside `Children`:

- fields are the object's scalar keys;
- elements are its array keys (`Image`, `Role`, `Children`, …).

The four fields `ratingKey`, `key`, `guid` and `type` are never removed,
because PMS cannot use an object without them. The filter applies to the
metadata, children and match routes. An unknown name is ignored.

### 5.5 `episodeOrder`

`episodeOrder` equal to the stored order, or absent, answers as today. Any
other order returns the show with no seasons (`Children` empty,
`/children` empty), as the docs specify ("if no seasons exist for the
requested episodeOrder, no season data should be returned").

## 6. Tests

- **Clients.** Fixtures are recorded from the live TMDB and TVDB APIs,
  never hand-written, per CLAUDE.md's fixture-shaped-like-the-answer
  gotcha. They cover:
  - Weekend (tmdb 79120), whose certification is UK-only;
  - a movie with a US certification;
  - a series with several networks, companies and content ratings;
  - an episode with guest stars and a still.
  Each mapping asserts every new field.
- **Gateway.**
  - `certification` follows the region → origin → US order.
  - The caps truncate.
  - The KV document is written and replaced whole, and deleted with its
    item.
  - An envtest per kind runs against an object that already has status, so
    a release of an existing field would show.
- **Provider.**
  - A golden response per type asserts every §2 field.
  - Tables cover `X-Plex-Country` (US, GB prefix, fallback chain), language
    (same versus other), each response-customization parameter, the
    protected four fields, and `episodeOrder` both stored and other.
- **Guards.** `TestUINeverWrites` with the KV methods, and the KV contract
  test against real NATS.
- **Falsification.** Remove the certification promotion, and the Weekend
  test must fail by name.

## 7. Proof on kind-cluster-plex

1. Refresh Weekend and one TV series with the `clustarr.io/refresh-metadata`
   annotation.
2. The Plex item then shows `gb/18`-style content rating, studio, tagline,
   country, cast with photos, director, writer and similar titles, all
   from the provider.
3. The series' seasons show their own posters, and episodes show stills.
4. Read-only queries on Plex's library database confirm that
   `content_rating`, `studio`, `tagline` and the tag tables are populated
   for the matched item.

## 8. Out of scope

- **Media Queries** (the `queryParser` feature of a general media
  provider). The owner deferred them on 2026-09-30.
- Storing more than one language.
- Serving episode orders clustarr does not store.
- `backgroundSquare`, for which no source exists (§5.1).
