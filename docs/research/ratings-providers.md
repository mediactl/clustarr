# Clustarr research: ratings providers (mdblist, omdb) -- SKELETON, BLOCKED under R5

Date: 2026-09-24. Task: C1 (`docs/superpowers/plans/2026-09-24-index-artwork-ratings-plex.md`,
ruling R5). Scope: the exact response shapes `pkg/metadata/clients/mdblist` and
`pkg/metadata/clients/omdb` must be built against, recorded live before either client is
written, per spec §C.3 ("Until recorded, no field name from memory is to be relied on; the
implementation task blocks on the recording").

## Status: BLOCKED

At C1's dispatch (2026-09-24) neither `MDBLIST_API_KEY` nor `OMDB_API_KEY` was set in the
environment. Ruling R5 is explicit: with neither key present, C1 records nothing, does not
write either client from memory, and reports the block. **This file is a skeleton** naming
exactly what the next attempt must record and where, not a substitute for having recorded it.
Everything below marked `UNRECORDED` is unverified for this task's purposes -- it must not be
used to write `mdblist.go` or `omdb.go`.

Everything else C1 covers landed without this: the `RatingsProvider` interface
(`pkg/metadata/provider.go`), `Registry.Ratings` (`pkg/metadata/registry.go`), TMDB's own
`RatingsProvider` implementation (`pkg/metadata/clients/tmdb/tmdb.go`, movie ratings only --
see its `RatingSources` doc comment for why series is out of scope too), `enrichRatings`'
fallthrough (`app/catalog/metadata/enrich.go`), `patch.go`'s rendering of `status.metadata.ratings`
and Series' `firstAired`, and the registry/prober wiring that accepts `MetadataProviderMDBList`
and `MetadataProviderOMDb` as recognised types but refuses to construct either client
(`ErrProviderAwaitingFixtures`, `app/catalog/controller/metadataprovider/prober.go`) -- so a CR
of either type reports a definite `Ready=False/InvalidSpec`, not a silent no-op or an
`Unknown/ProviderNotImplemented` as if the type were never heard of.

## What already exists (not a substitute for this file)

`docs/research/metadata.md` §3.4 has an earlier, general **(verified)** note about MDBList from
the Phase B metadata research pass (2026-09-18), predating this task and this file's specific
scope. It documents:

- Base `https://api.mdblist.com`, `?apikey=` or OAuth bearer.
- Quotas: 1000 req/day free (10k/25k/100k/250k for paid tiers); headers
  `X-RateLimit-Limit|Remaining|Reset`, `Retry-After`.
- A media-info shape from `GET /{imdb|tmdb|trakt|tvdb|mal|mdblist}/{movie|show|any}/{id}`:
  `{title, year, released, released_digital, description, runtime, score, score_average,
  ids{imdb,tmdb,tvdb,trakt,mal,mdblist}, type, ratings[{source, value, score, votes, url}],
  watch_providers[], language, spoken_language, country, certification, commonsense, age_rating,
  status, trailer, poster, backdrop}`, with `ratings[].source` one of
  `imdb|tmdb|metacritic|trakt|tomatoes|letterboxd|rogerebert|myanimelist`.

This is useful context for shaping the recording session (it tells you roughly what to expect
and where the ratings array lives), but it is **not** a recorded fixture, was not checked
against `/tmdb/movie/{id}` and `/tmdb/show/{id}` specifically (the two calls spec §C.2 keys
mdblist by), does not cover the `rottenTomatoesCritic` vs `rottenTomatoesAudience` split this
CRD's `RatingSource` enum requires (the note's `tomatoes` source is singular), and was not
re-verified for this task. Ruling R5 requires an independent recording under `test/data/` before
either client is coded; this note does not satisfy that.

No prior research note exists for OMDb beyond one passing mention (metadata.md line 377, about
IMDb datasets, not OMDb's own API shape).

## Recording steps (for whoever runs this next, with a key set)

Per the task brief:

1. **MDBList**, with `MDBLIST_API_KEY` set:
   - `curl "https://api.mdblist.com/tmdb/movie/{tmdbID}?apikey=$MDBLIST_API_KEY"` for one real
     movie (Inception, tmdb id 27205, matches the existing TMDB fixture already in
     `test/data/metadata/tmdb/movie_27205.json` -- keeps the two providers' fixtures
     cross-referenceable in a future combined-fallthrough test).
   - `curl "https://api.mdblist.com/tmdb/show/{tmdbID}?apikey=$MDBLIST_API_KEY"` for one real
     show (Game of Thrones, tmdb id 1399 -- also the id already in
     `test/data/metadata/tvdb/series_121361.json`'s `remoteIds`).
   - The same call against an id that does not exist, to record the not-found shape.
   - Save to `test/data/metadata/mdblist/movie_27205.json`,
     `test/data/metadata/mdblist/show_1399.json`,
     `test/data/metadata/mdblist/movie_notfound.json` (naming to match the existing
     `test/data/metadata/tmdb/` convention).
   - **Strip the key** from every saved body and from the file name before committing.
2. **OMDb**, with `OMDB_API_KEY` set:
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
3. Rewrite this file from the recorded bodies: exact field names, units (OMDb's `imdbRating` is
   documented as a string like `"8.8"`, not a number -- confirm), how each API encodes "no
   rating" or "N/A" (OMDb is documented to use the literal string `"N/A"` for an absent
   `imdbRating`/`Metascore` -- confirm against a real response, since a client must not parse
   `"N/A"` as `0`, which `enrichRatings`' "zero value with zero votes is omitted" rule would then
   wrongly treat as a real zero rather than skip), and quota/rate-limit header names.
4. Only then write `pkg/metadata/clients/mdblist/{mdblist.go,mdblist_test.go}` and
   `pkg/metadata/clients/omdb/{omdb.go,omdb_test.go}` against the recorded fixtures, following
   this task's other clients' conventions (`Config{APIKey, BaseURL, HTTP, Limiter}`, `Limiter`
   required -- nil is a construction error, never a default; `ReadBody` capped at
   `metadata.MaxResponseBytes`; `metadata.ErrNotFound` on the unknown-id fixture; API key never
   interpolated into an error string), remove `ErrProviderAwaitingFixtures` from
   `app/catalog/controller/metadataprovider/registry.go`'s `buildSupplementary` switch for both
   types, wire real construction into both `BuildRegistry`s
   (`app/catalog/metadata/registry.go` and
   `app/catalog/controller/metadataprovider/registry.go` -- see the sibling-copy warning in
   both files' `isSupplementary` doc comments) and `NewProber`, and add both to the chart
   README's ratings-provider table (`charts/clustarr/README.md`).

## UNRECORDED -- must not be relied on until the steps above are done

- MDBList's exact field names for `GET /tmdb/movie/{id}` and `GET /tmdb/show/{id}` specifically
  (as opposed to the generic `/{provider}/{type}/{id}` form documented in metadata.md §3.4).
- Whether MDBList's `ratings[].source` value `"tomatoes"` is actually two sources
  (`rottenTomatoesCritic`/`rottenTomatoesAudience`) under different `source` strings, or one
  combined entry this client would need to split by another field (the metadata.md note lists
  only a single `tomatoes` source, but this CRD's `RatingSource` enum requires the audience/critic
  split) -- **this must be resolved by inspecting a real recorded response**, not guessed.
- MDBList's units: whether `ratings[].score`/`.value` are already 0-100, 0-10, or provider-native
  (mixed scales across entries in the same array, per the metadata.md note's own listed sources)
  -- ValueCentis' int32 0-1000/0-10000 split (per `api/catalog/v1alpha1/shared_types.go`'s doc
  comment) requires knowing this per source before any conversion is written.
- MDBList's not-found response's HTTP status and body shape.
- MDBList's quota/rate-limit header names on a live response (metadata.md lists
  `X-RateLimit-Limit|Remaining|Reset` and `Retry-After`, carried over from the general research
  pass but not re-verified against these two specific endpoints).
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
