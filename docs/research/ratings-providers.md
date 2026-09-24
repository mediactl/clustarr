# Clustarr research: ratings providers (mdblist recorded; omdb BLOCKED under R5)

Date: 2026-09-24. Task: C1 (`docs/superpowers/plans/2026-09-24-index-artwork-ratings-plex.md`,
ruling R5) and its follow-up. Scope: the exact response shapes `pkg/metadata/clients/mdblist`
and `pkg/metadata/clients/omdb` are built against, recorded live before either client is
written, per spec §C.3 ("Until recorded, no field name from memory is to be relied on").

## Status

- **MDBList: recorded and built (2026-09-24).** The responses below were recorded from
  `https://api.mdblist.com` with a real key into `test/data/metadata/mdblist/`, and
  `pkg/metadata/clients/mdblist` is written against them.
- **OMDb: BLOCKED.** No `OMDB_API_KEY` has been available. Nothing about OMDb below is
  recorded; its client is not written, and a `MetadataProviderType: omdb` CR reports
  `Ready=False, InvalidSpec` (`ErrProviderAwaitingFixtures`).

## MDBList (recorded 2026-09-24)

Fixtures (key stripped; `user.json` trimmed to its quota fields, personal fields replaced):

| File | Request | Status |
| --- | --- | --- |
| `movie_27205.json` | `GET /tmdb/movie/27205` (Inception) | 200 |
| `show_1399.json` | `GET /tmdb/show/1399` (Game of Thrones); `GET /tvdb/show/121361` returned a byte-identical body | 200 |
| `movie_notfound.json` | `GET /tmdb/movie/999999999` | 404 |
| `invalid_key.json` | `GET /tmdb/movie/27205` with a made-up key | 401 (`/user` with the same key: 403) |
| `user.json` | `GET /user` | 200 |

- **Auth:** `?apikey=<key>`.
- **Media document:** a large object (`title`, `year`, `ids{imdb,trakt,tmdb,tvdb,mal,mdblist}`,
  `type` `"movie"|"show"`, `score`, `score_average`, `watch_providers`, ...) whose
  `ratings[]` entries are `{source, value, score, votes, url}`; a Rotten Tomatoes entry adds
  `fresh`. Any of `value`, `score`, `votes`, `url` may be `null`, and `url` is sometimes a
  number (imdb's was `96`), so a client must not decode it as a string.
- **`ratings[].source`** values seen: `imdb`, `metacritic`, `metacriticuser`, `trakt`,
  `tomatoes`, `popcorn`, `tmdb`, `letterboxd`, `rogerebert`, `myanimelist`. Rotten Tomatoes is
  **two** sources: `tomatoes` is the critic score (Tomatometer, with `fresh`) and `popcorn`
  the audience score -- the split the CRD's `rottenTomatoesCritic`/`rottenTomatoesAudience`
  needs.
- **Units:** `value` is on each source's own scale -- imdb 8.8 (/10), letterboxd 4.2 (/5),
  and metacritic 74, trakt 87, tmdb 83, tomatoes 86, popcorn 91 (/100). `score` is `value`
  normalised to /100 (letterboxd 4.2 -> 84) but is `null` for rogerebert even with a `value`,
  so the client converts `value` with a per-source factor onto `catalogv1alpha1.Rating`'s
  centis (/10 sources to 0-1000, /100 sources to 0-10000).
- **Not found:** 404 `{"error":"Item not found"}`. **Bad key:** 401
  `{"error":"Invalid API key"}` on a media call, 403 on `/user`.
- **Quota:** every response carries `X-RateLimit-Limit: 1000`, `X-RateLimit-Remaining` and
  `X-RateLimit-Reset` (Unix seconds; `1790294400`, midnight UTC). The quota is per key: a
  second key on the same account reported its own 999 remaining. `GET /user` reports
  `api_requests`, `api_requests_count`, `rate_limit`, `rate_limit_remaining` and
  `rate_limit_reset` and **does not spend quota** (the count stayed at 4 across three `/user`
  calls), which is why it is the probe.
- No 429 was recorded (it would take a spent quota); the client treats HTTP 429 per RFC 6585
  and rests the key until `X-RateLimit-Reset`, else `Retry-After`, else an hour.

## OMDb recording steps (for whoever runs this next, with a key set)

1. **OMDb**, with `OMDB_API_KEY` set:
   - `curl "http://www.omdbapi.com/?i=tt1375666&apikey=$OMDB_API_KEY"` for the same movie
     (Inception, imdb id tt1375666 -- matches `test/data/metadata/tmdb/movie_27205.json`'s
     `imdb_id`).
   - `curl "http://www.omdbapi.com/?i=tt0944947&apikey=$OMDB_API_KEY"` for the same show
     (Game of Thrones, imdb id tt0944947 -- matches `test/data/metadata/tvdb/series_121361.json`).
   - The same call against an id that does not exist, to record OMDb's `{"Response":"False",
     "Error":"..."}` shape (OMDb's documented convention; unverified here -- confirm the exact
     field names on the real response before relying on them).
   - Save to `test/data/metadata/omdb/movie_tt1375666.json`,
     `test/data/metadata/omdb/series_tt0944947.json`,
     `test/data/metadata/omdb/notfound.json`.
   - **Strip the key** from every saved body and from the file name before committing.
2. Record OMDb's exact field names, units (`imdbRating` is documented as a string like `"8.8"`
   -- confirm), how it encodes "no rating" (documented as the literal `"N/A"` -- confirm; a
   client must not parse `"N/A"` as `0`), and any quota header, into this file.
3. Only then write `pkg/metadata/clients/omdb/{omdb.go,omdb_test.go}` against the recorded
   fixtures, following `pkg/metadata/clients/mdblist`: `ReadBody` capped at
   `metadata.MaxResponseBytes` (through `httpjson`), `metadata.ErrNotFound` on the unknown-id
   fixture, the key never in an error string; replace `ErrProviderAwaitingFixtures` in
   `app/catalog/controller/metadataprovider/registry.go`'s `buildSupplementary`, wire it into
   both `BuildRegistry`s (`app/catalog/metadata/registry.go` and the controller's -- see the
   sibling-copy warning in both files' `isSupplementary` doc comments), and update the chart
   README's ratings-provider table.

## OMDb: UNRECORDED -- must not be relied on until the steps above are done

- OMDb's full response shape for a movie and a series (`Ratings[]`, `imdbRating`, `imdbVotes`,
  `Metascore`, `Type`, and whatever else the live response carries).
- OMDb's `Ratings[].Source` values (documented elsewhere as `"Internet Movie Database"`,
  `"Rotten Tomatoes"`, `"Metacritic"` -- unverified here) and how each maps onto this CRD's
  `RatingSource` enum (`imdb`, `rottenTomatoesCritic`, `metacritic` per spec §C.2's table --
  OMDb's single `"Rotten Tomatoes"` entry is presumably critic score only, but this must be
  confirmed, not assumed).
- OMDb's exact not-found shape and HTTP status.
- OMDb's rate limit and any quota header it sends (OMDb's public tiers are documented elsewhere
  as a flat daily request cap with no advertised header; unverified here).
