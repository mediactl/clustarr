# Clustarr research: metadata sources + import lists

Date: 2026-09-18. Scope: what Radarr/Sonarr/Lidarr/Readarr/Prowlarr-family apps use for
metadata and import lists, what Clustarr should call directly, and a Go-oriented design for
`pkg/metadata` and `pkg/importlist`. Everything marked **(verified)** was checked against the
primary docs, the repo (via DeepWiki), or `go list -m` today; **(unverified)** is prior
knowledge that I could not confirm because the site was behind bot protection.

---

## 0. TL;DR for architects

1. **Do not depend on the *arr metadata proxies.** Sonarr's SkyHook is closed source and Sonarr-only;
   Radarr's `api.radarr.video` is open source (RadarrAPI.TMDB) but undocumented ("Documentation may
   come sometime"); Lidarr's `api.lidarr.audio` is open source but needs a MusicBrainz Postgres replica
   + Solr; Readarr's `api.bookinfo.club` is dead and Readarr itself was **retired 2025-06-27**.
   Go straight to TMDB, TheTVDB v4, MusicBrainz + Cover Art Archive, fanart.tv, Open Library /
   Hardcover, ComicVine / Metron / MangaDex / AniList / Kitsu, and Audnexus. Put our own cache in front.
2. **Model after the *arr internals, not their proxies.** Copy `MovieMetadata`, `Series`/`Episode`,
   `Artist`/`Album`/`AlbumRelease`/`Track`, `Author`/`Book`/`Edition` field sets; copy the
   `ShouldRefresh*` heuristics; copy `MinimumAvailability` + `MovieStatusType` derivation from TMDB
   release types; copy `ImportListItemInfo` (ids-first list items), `ListSyncLevel`, and exclusions.
3. **Identifier crosswalk is the spine.** Every entity carries an `ExternalIDs` set (IMDb, TMDB, TVDB,
   TVMaze, TVRage, Wikidata, MusicBrainz {artist, release-group, release, recording}, ISBN-10/13, ASIN,
   OpenLibrary work/edition, Goodreads, Hardcover, ComicVine, Metron, GCD, AniDB, AniList, MAL, Kitsu,
   MangaDex, MangaUpdates, Trakt, Plex GUID). Sources of crosswalk: TMDB `/find` + `/external_ids`,
   TVDB `remoteIds`, Kitsu `/mappings`, Fribb/Kometa anime JSON (daily), MusicBrainz URL relations,
   Open Library edition identifiers, MDBList `ids`, Wikidata.
4. **Rate limits decide the architecture.** TMDB ~40 rps; TVDB JWT/1 month; MusicBrainz 1 rps/IP with a
   mandatory contact User-Agent; AniList 30 rpm (degraded, nominally 90); AniDB 1 req/2 s with 24 h bans;
   ComicVine 200/resource/hour; Metron 20 rpm + 5000/day; Open Library 1 rps (3 rps with contact UA);
   Hardcover 60 rpm + 5000/day; MDBList 1000/day free; Trakt 1000 GET/5 min + 1 write/s; MangaDex 5 rps.
   A distributed system MUST funnel each provider through one rate-limited gateway (or a shared token
   bucket in NATS KV/Redis) or it will get itself banned.
5. **Import lists are cheap, id-bearing, and periodic.** Trakt (device OAuth; access tokens now live
   **24 h** since 2025-03-20, refresh tokens are single-use), Plex Discover watchlist (`X-Plex-Token`),
   TMDB v3 discover / v4 lists, MDBList, StevenLu JSON, IMDb (only top250/popular/`ur*` via proxy — `ls*`
   custom lists now require login), Simkl, another *arr instance, custom JSON/RSS. Effective cadence in
   the *arrs: scheduler ticks every 5 min, each list enforces `MinRefreshInterval` 6–12 h.
6. **Go libraries worth adopting (verified versions):** `github.com/cyruzin/golang-tmdb v1.9.4`
   (2026-07-15, active, append_to_response typed), `github.com/mxrk/tvdb v0.3.1` (2026-01-06, TVDB v4,
   `WithPin`, typed models), `github.com/icco/trakt v1.0.4` (2026-08-02, device flow + refresh + sync
   only), `github.com/LukeHagar/plexgo v0.29.2` (2026-08-21, has `Provider.GetWatchlist` against
   `discover.provider.plex.tv`), `github.com/rl404/verniy v0.3.4` (AniList), `github.com/Khan/genqlient
   v0.8.1` (typed GraphQL for Hardcover/AniList), `golang.org/x/time v0.16.0` (rate), `github.com/maypok86/otter
   v1.2.4` (in-proc cache), `github.com/redis/go-redis/v9` (shared cache), `github.com/hashicorp/go-retryablehttp
   v0.7.8`, `github.com/mmcdole/gofeed v1.4.2` (RSS lists), `github.com/gocarina/gocsv` (IMDb CSV).
   Write our own thin clients for MusicBrainz, Open Library, Audnexus, ComicVine, Metron, MangaDex,
   Kitsu, MDBList, StevenLu (the existing Go clients are dead: `gomusicbrainz` 2018, `pioz/tvdb` 2022 v3-era,
   `42minutes/go-trakt` 2016, `hobeone/gotrakt` 2014).

---

## 1. How the *arr apps actually get metadata (verified via DeepWiki + web)

### 1.1 Radarr → `https://api.radarr.video/v1` (RadarrAPI.TMDB)

- Proxy class `NzbDrone.Core.MetadataSource.SkyHook.SkyHookProxy` (the class is still called SkyHook
  in Radarr even though the backend is Radarr's own service). Routes it calls: `movie/{tmdbId}`,
  `movie/imdb/{imdbId}`, `POST movie/bulk` (list of TMDB ids), `movie/collection/{tmdbId}`,
  `movie/changes/{timestamp}` (changed-since feed), `search?q=` (also accepts `imdb:`/`tmdb:` prefixes).
  Import lists also go through it: `list/imdb/{top250|popular|ur########}`, TMDB v3/v4 pass-through
  (`api/3/discover/movie?with_companies=`, `with_keywords=`, `api/4/list/{id}`, `api/4/account/{id}/...`).
- Backend repo: `github.com/Radarr/RadarrAPI.TMDB` ("Backend Stuff for Radarr regarding TMDB (Discover,
  lists, Mappings, etc.)"; deployed manually to `api.radarr.video`, staging at `staging.api.radarr.video`;
  README says "Documentation may come sometime"; returns JSON when `Accept: application/json`). No usage
  policy for third parties is published → treat as **not for Clustarr**.
- `MovieMetadata` (C#) fields: `TmdbId, ImdbId, Title, OriginalTitle, CleanTitle, CleanOriginalTitle,
  SortTitle, Overview, Runtime, Year, SecondaryYear, Images (List<MediaCover>), Genres, Keywords,
  Ratings {Imdb, Tmdb, Metacritic, RottenTomatoes, Trakt each {Votes, Value, Type}}, Certification,
  CollectionTmdbId, CollectionTitle, Studio, Website, YouTubeTrailerId, Status (MovieStatusType),
  InCinemas, PhysicalRelease, DigitalRelease, AlternativeTitles (SourceType, Title, CleanTitle),
  Translations (Title, Overview, Language, CleanTitle), OriginalLanguage, Recommendations (List<int>
  tmdb ids), Popularity, LastInfoSync`.
  `MediaCover.CoverType` enum: `unknown, poster, banner, fanart, screenshot, headshot, clearlogo`.
- `Movie` (library row, not metadata): `MinimumAvailability` ∈ `{tba, announced, inCinemas, released}`;
  `Movie.GetReleaseDate()` picks the effective date for the chosen availability.
- `MovieStatusType` derivation in `SkyHookProxy.MapMovie` (copy this):
  1. start `Announced`;
  2. `InCinemas` if in-cinemas date is past;
  3. `Released` if in cinemas > 90 days and no physical/digital date;
  4. `Released` if physical date past; 5. `Released` if digital date past.
- Refresh policy `ShouldRefreshMovie.ShouldRefresh(MovieMetadata)`: refresh if `LastInfoSync` > 180 d;
  never if < 12 h; `Announced`/`InCinemas` refresh often; `Released` with physical release within
  last 30 d refresh; otherwise skip. `TaskManager` schedules `RefreshMovieCommand` every **24 h**;
  `RssSyncInterval` default 30 min; `ImportListSyncCommand` every **5 min** (per-list minimums apply).

### 1.2 Sonarr → SkyHook `https://skyhook.sonarr.tv/v1/tvdb/...` (closed source)

- Routes: `shows/en/{tvdbId}` and `search/en/?term=` (language segment `en`). No changed-since
  endpoint is exposed to the client. SkyHook caches TVDB and is explicitly for Sonarr; third-party
  wrappers exist but it is not a supported public API → **not for Clustarr**.
- `ShowResource` (SkyHook JSON) fields: `tvdbId, title, overview, slug, firstAired, lastAired, tvRageId,
  tvMazeId, tmdbId, imdbId, malIds (int[]), aniListIds (int[]), status ("continuing"|"ended"|"upcoming"),
  runtime, timeOfDay {hours, minutes}, network, originalLanguage, originalCountry, actors[{name,
  character, image}], genres, contentRating, rating {count, value}, images[{coverType, url}],
  seasons[{seasonNumber, images}], episodes[{tvdbId, seasonNumber, episodeNumber, absoluteEpisodeNumber,
  airedAfterSeasonNumber, airedBeforeSeasonNumber, airedBeforeEpisodeNumber, title, airDate, airDateUtc,
  runtime, finaleType, rating, overview, image}]`.
- `Series` (C#): `TvdbId, TvRageId, TvMazeId, ImdbId, TmdbId, MalIds, AniListIds, Title, CleanTitle,
  SortTitle, Status (SeriesStatusType), Overview, AirTime, Network, Images, Seasons, Year, Runtime,
  Ratings, Genres, Certification, SeriesType (standard|daily|anime), FirstAired, LastAired,
  OriginalLanguage, Actors`. `Episode`: `TvdbId, SeasonNumber, EpisodeNumber, AbsoluteEpisodeNumber,
  SceneAbsoluteEpisodeNumber, SceneSeasonNumber, SceneEpisodeNumber, AiredAfterSeasonNumber,
  AiredBeforeSeasonNumber, AiredBeforeEpisodeNumber, Title, AirDate, AirDateUtc, Overview, Monitored,
  Ratings, Images, Runtime, FinaleType`.
- Anime: `SeriesType.Anime` switches parsing to absolute numbering; `SceneMappingService` +
  `XemProxy` (thexem.info) map scene numbering ↔ TVDB numbering (`SceneMapping {TvdbId, Title,
  SearchTerm, SeasonNumber, SceneSeasonNumber}`).
- Refresh policy `ShouldRefreshSeries`: refresh if `LastInfoSync` > 30 d; refresh if any aired episode
  is titled "TBA"; never if < 6 h; refresh if status ≠ Ended; refresh if Ended but last episode aired
  < 30 d ago; else skip. `TaskManager`: `RefreshSeriesCommand` every **12 h**, `RssSyncInterval`
  default 15 min, `ImportListSyncCommand` every 5 min.

### 1.3 Lidarr → `https://api.lidarr.audio/api/v0.4/` (LidarrAPI.Metadata, open source Python)

- Routes: `artist/{mbid}`, `album/{mbid}` (release-group), `search/artist?query=`, `search/album?query=`,
  `search/all?query=`, `POST spotify/lookup`, `recent/artist`, `recent/album`, `POST artist/{mbid}/refresh`.
- Upstreams: MusicBrainz **Postgres replica** (not the public WS), Solr for search, fanart.tv,
  TheAudioDB, Wikipedia/Wikidata (overviews), Last.fm. Cloudflare CDN + Redis/Postgres caches.
- `ArtistResource`: `id, artistname, artistaliases, sortname, disambiguation, overview, type, status,
  genres, images[{CoverType, Url}], links[{target, type}], rating{Count, Value}, albums, oldids`.
  `AlbumResource`: `id, title, disambiguation, overview, artistid, artists, type, secondarytypes,
  releasedate, images, links, genres, rating, releases[{id, title, status, country, label, media[{Format,
  Name, Position}], track_count, disambiguation, tracks[{id, recordingid, trackname, tracknumber,
  trackposition, mediumnumber, durationms, oldids}], oldids}], oldids`.
- Lidarr models: `Artist{ForeignArtistId (MB artist), ArtistName, Overview, Images, Links, Genres,
  Members, Disambiguation, Ratings}`, `Album{ForeignAlbumId (MB release-group), Title, Overview,
  Disambiguation, ReleaseDate, Images, Links, Genres, AlbumType, SecondaryTypes, Ratings}`,
  `AlbumRelease{ForeignReleaseId (MB release), Title, Status, TrackCount, Media, Disambiguation, Country,
  Label}`, `Track{ForeignRecordingId, ForeignTrackId, Title, MediumNumber, AbsoluteTrackNumber}`.
  `MetadataProfile{PrimaryAlbumTypes, SecondaryAlbumTypes, ReleaseStatuses}` — this is the thing
  to copy for music: users pick which MB types they want (Album/EP/Single/Broadcast/Other;
  Studio/Compilation/Soundtrack/Live/Remix/...; Official/Promotion/Bootleg/Pseudo-Release).
- Import lists: Last.fm (top artists/albums, returns `Mbid`), Spotify (Spotify id → MBID via the
  proxy's `spotify/lookup`), Headphones, MusicBrainz series/list, Lidarr sync. Item shape:
  `ImportListItemInfo{ArtistMusicBrainzId, AlbumMusicBrainzId, Artist, Album}`.

### 1.4 Readarr → `api.bookinfo.club` (dead) — project retired 2025-06-27

- Readarr's `BookInfoProxy` wrapped Goodreads (`ForeignAuthorId` = Goodreads author id, `ForeignBookId`
  = Goodreads work id, `ForeignEditionId` = Goodreads edition id). The Goodreads public API was shut in
  Dec 2020; the proxy relied on scraping; the domain no longer resolves. Servarr wiki marks Readarr
  "Retired"; the retirement notice cites unusable metadata and stalled Open Library migration.
- Community continuations: **rreading-glasses** public instance `https://api.bookinfo.pro`
  (Goodreads-backed drop-in for the proxy API; used by forks such as Bookshelf/Chaptarr).
- Models to copy: `AuthorMetadata{ForeignAuthorId, TitleSlug, Name, Overview, Disambiguation, Gender,
  Hometown, Born, Died, Status, Images, Links, Genres, Ratings, Aliases}`, `Book{ForeignBookId, TitleSlug,
  Title, ReleaseDate, Links, Genres, Ratings, CleanTitle, Monitored, AnyEditionOk, LastInfoSync, Added,
  SeriesLinks[{Series, Position}]}`, `Edition{ForeignEditionId, Isbn13, Asin, Title, TitleSlug, Language,
  Overview, Format, IsEbook, Disambiguation, Publisher, PageCount, ReleaseDate, Images, Links, Ratings,
  Monitored, ManualAdd}`. The work/edition split (`AnyEditionOk`) is essential for books.

---

## 2. Primary metadata sources Clustarr should call directly

### 2.1 TMDB API v3 (movies + TV) — **(verified)**

- Base `https://api.themoviedb.org/3`; auth either `?api_key=` (v3 key) or `Authorization: Bearer <v4
  read access token>` (recommended; also unlocks v4 list/account endpoints on `/4`).
- Rate limit: docs say limits "sit somewhere in the **40 requests per second** range" (legacy 40/10 s
  removed 2019-12-16); HTTP **429** when exceeded. Plan for a token bucket at ~30 rps per API key.
- `append_to_response=videos,images,...` comma-separated; each appended sub-resource accepts its own
  query params; `language=` filters images unless `include_image_language=en,null` is given.
- `GET /find/{external_id}?external_source=imdb_id|tvdb_id|wikidata_id|facebook_id|instagram_id|
  tiktok_id|twitter_id|youtube_id` → `{movie_results[], tv_results[], tv_episode_results[],
  tv_season_results[], person_results[]}`.
- `GET /movie/{id}/release_dates` → `{id, results[{iso_3166_1, release_dates[{type, release_date,
  certification, note, iso_639_1, descriptors[]}]}]}`; `type`: **1 Premiere, 2 Theatrical (limited),
  3 Theatrical, 4 Digital, 5 Physical, 6 TV**. Radarr's In-Cinemas = earliest type 2/3 (with country
  preference), Digital = type 4, Physical = type 5. Also `/movie/{id}/external_ids` (imdb_id,
  wikidata_id, facebook/instagram/twitter).
- `GET /tv/{id}/external_ids` → `{id, imdb_id, freebase_mid, freebase_id, tvdb_id, tvrage_id,
  wikidata_id, facebook_id, instagram_id, twitter_id}` (verified field list).
- `GET /tv/{id}/episode_groups` → `{id, results[{id (string), name, description, type, episode_count,
  group_count, network{id,name,logo_path,origin_country}}]}`; `type`: **1 Original air date, 2 Absolute,
  3 DVD, 4 Digital, 5 Story arc, 6 Production, 7 TV** (the TMDB analogue of TVDB season orders).
- Other endpoints used for the normalized model (well known; not re-verified today): `/movie/{id}`,
  `/tv/{id}`, `/tv/{id}/season/{n}`, `/movie/{id}/alternative_titles`, `/tv/{id}/alternative_titles`,
  `/movie/{id}/translations`, `/movie/{id}/images`, `/movie/{id}/keywords`, `/movie/{id}/videos`,
  `/movie/{id}/credits`, `/movie/{id}/watch/providers`, `/collection/{id}`, `/search/movie`,
  `/search/tv`, `/search/multi`, `/discover/movie` (`with_companies`, `with_keywords`, `with_genres`,
  `primary_release_date.gte`, `sort_by=popularity.desc`, `region`, `certification_country`), `/movie/changes`
  and `/tv/changes` (`start_date`/`end_date`, ≤14-day window) for incremental refresh, `/configuration`
  (`images.secure_base_url`, `poster_sizes`, `backdrop_sizes`), images at
  `https://image.tmdb.org/t/p/{size}{file_path}`.
- Go: `github.com/cyruzin/golang-tmdb v1.9.4` — `Init(apiKey)`, `InitV4(bearer)`,
  `GetMovieDetails(id, map[string]string)`, `GetFindByID(id, opts)`, `GetMovieReleaseDates(id)`,
  `GetTVDetails`, `GetTVExternalIDs`, `GetSearchMovies`, `GetDiscoverMovie/TV`, `GetChangesMovie/TV`,
  `GetMovieChanges`, `GetCollectionDetails`, `GetListDetails`, `SetClientAutoRetry()`,
  `SetClientConfig(http.Client)`, `SetCustomBaseURL`; `MovieDetails` embeds typed
  `*MovieReleaseDatesAppend`, `*MovieAlternativeTitlesAppend`, `*MovieTranslationsAppend`,
  `*MovieExternalIDsAppend`, `*MovieImagesAppend`, `*MovieKeywordsAppend`, `*MovieVideosAppend`,
  `*MovieCreditsAppend` so `append_to_response` results are typed. No per-call `context.Context`
  (wrap our http.Client with a ctx-aware transport / rate limiter).

### 2.2 TheTVDB v4 — **(verified)**

- Base `https://api4.thetvdb.com/v4`. `POST /login {apikey, pin?}` → `{data:{token}, status}`; the JWT
  lives ≈ **1 month**. Two licensing models: **negotiated** (project key, fees by usage) or
  **user-supported** (project key + each end user's subscriber PIN; subscription **$11.99/year**).
  Clustarr should support both: `TVDBCredentials{apiKey, pin?}` in a Secret.
- Endpoints: `/search` (`query|q, type=movie|series|person|company, year, company, country, director,
  language, primaryType, network, remote_id, offset, limit`) → `SearchResult{objectID, aliases, country,
  id, image_url, name, first_air_time, overview, primary_language, primary_type, status, type, tvdb_id,
  year, slug, overviews{}, translations{}, network, remote_ids[], thumbnail}`; `/search/remoteid/{id}`
  (IMDb/EIDR/TMDB id → series/movie/person); `/series/{id}`, `/series/{id}/extended?meta=translations|
  episodes&short=true`; `/series/{id}/episodes/{season-type}?page=&season=&episodeNumber=&airDate=`
  with **season-type ∈ {default, official, dvd, absolute, alternate, regional}** (+ `altdvd` in the UI;
  `absolute` is what anime needs); `/series/{id}/artworks?lang=&type=`; `/series/{id}/translations/{lang}`;
  `/seasons/{id}/extended`; `/episodes/{id}/extended`; `/movies/{id}/extended`; `/artwork/types`;
  `/updates?since=<unix>&type=series|episodes|seasons|artwork|movies|translatedseries...&action=update|delete&page=`
  → `{recordType, recordId, methodInt, method, extraInfo, userId, timeStamp, entityType}`.
- `SeriesExtendedRecord`: `id, name, slug, image, nameTranslations[], overviewTranslations[],
  aliases[{language,name}], firstAired, lastAired, nextAired, score, status{id,name,recordType,keepUpdated},
  originalCountry, originalLanguage, defaultSeasonType, isOrderLocked, lastUpdated, averageRuntime,
  episodes[], artworks[], remoteIds[{id, type, sourceName}], characters[], seasons[{id, seriesId,
  type{id,name,type,alternateName}, number, image, imageType, companies, lastUpdated}], genres[],
  trailers[], lists[], companies, airsDays{}, airsTime, latestNetwork, originalNetwork, translations,
  tags[], contentRatings[]`. `EpisodeBaseRecord`: `id, seriesId, name, aired, runtime, overview, image,
  imageType, isMovie, seasons, number, absoluteNumber, seasonNumber, lastUpdated, finaleType,
  airsAfterSeason, airsBeforeSeason, airsBeforeEpisode, year, linkedMovie`. `ArtworkBaseRecord`:
  `id, image, thumbnail, language, type, score, width, height, includesText`; artwork type ids
  1 banner, 2 poster, 3 background (fetch `/artwork/types` for the full table; season/episode types
  differ). `remoteIds[].sourceName` values come from `/sources` (IMDB, TheMovieDB.com, EIDR, Official
  Website, Wikidata, ... — match by name, not by numeric `type`).
- Go: `github.com/mxrk/tvdb v0.3.1` — `New(apiKey, WithPin(pin), WithHTTPClient, WithBaseURL,
  WithToken)`, `Login(ctx)`, `Search(ctx, SearchOptions)`, `SearchByRemoteId`, `GetSeriesExtended(ctx,
  id, *SeriesExtendedOptions)`, `GetSeriesEpisodes(ctx, id, seasonType, opts)`, `GetSeriesArtworks`,
  `GetSeriesTranslation`, `GetEpisodeExtended`, `GetUpdates(ctx, UpdatesOptions)`, `GetMovieExtended`,
  typed `models.SeriesExtendedRecord` etc., `APIError.IsUnauthorized()` for token expiry. Context-aware.
  Alternative: `github.com/dashotv/tvdb v0.5.2` (Speakeasy-generated, 2024-07). `github.com/pioz/tvdb`
  is v3-era (2022) — skip.

### 2.3 MusicBrainz + Cover Art Archive + fanart.tv (music) — **(verified)**

- WS base `https://musicbrainz.org/ws/2/`, `fmt=json`. **Rate: ~1 request/second per IP on average**,
  global 300 rps; violators get **503**. User-Agent is mandatory in the form
  `Application/Version ( contact-url-or-email )`; anonymous/generic UAs are throttled at 50 rps
  globally shared. Max `limit=100`.
- Entities: artist, release-group, release, recording, work, label, area, url, series, genre.
  `inc=` values (from musicbrainzngs VALID_INCLUDES, mirrors the WS): artist → `release-groups,
  releases, recordings, works, aliases, tags, genres, ratings, url-rels, artist-rels, annotation`;
  release-group → `artists, releases, artist-credits, aliases, tags, genres, ratings, url-rels`;
  release → `artists, labels, recordings, release-groups, media, artist-credits, discids, isrcs,
  aliases, recording-level-rels, work-level-rels, url-rels`; recording → `artists, releases, isrcs,
  artist-credits, work-rels, url-rels`.
- Browse: `release-group?artist={mbid}&type=album|single|ep|broadcast|other&limit=100&offset=`;
  `release?release-group={mbid}&inc=media+recordings+labels`. Search (Lucene): `artist?query=artist:"x"`,
  `release-group?query=releasegroup:"x" AND artist:"y"`. Non-MBID lookups: `discid/{id}`, `isrc/{isrc}`,
  `iswc/{iswc}`; barcode via `release?query=barcode:...`.
- Release-group primary types: `Album, Single, EP, Broadcast, Other`; secondary: `Compilation,
  Soundtrack, Spokenword, Interview, Audiobook, Audio drama, Live, Remix, DJ-mix, Mixtape/Street, Demo,
  Field recording`. Release status: `official, promotion, bootleg, pseudo-release` (+ `withdrawn`,
  `cancelled` added later — (unverified)). Note **Audiobook** is an MB secondary type: MB is a viable
  secondary source for audiobook releases with ISRC/barcode.
- Cover Art Archive: `https://coverartarchive.org/release/{mbid}/front[-250|-500|-1200]`,
  `/release-group/{mbid}/front`, `/release/{mbid}` (JSON index) → **307 redirect** to archive.org;
  sizes 250/500/1200/full; **no rate limiting rules** at CAA. Store the redirected archive.org URL.
- fanart.tv v3: `https://webservice.fanart.tv/v3/{movies/{tmdb|imdb}, tv/{tvdbId}, music/{mb-artist},
  music/albums/{mb-release-group}}?api_key=&client_key=` → image types (movies `hdmovielogo, movielogo,
  hdmovieclearart, movieart, moviedisc, movieposter, moviebackground, moviebanner, moviethumb`; tv `hdtvlogo,
  clearlogo, hdclearart, clearart, tvthumb, showbackground, seasonposter, seasonthumb, seasonbanner,
  tvbanner, tvposter, characterart`; music `artistbackground, artistthumb, musiclogo, hdmusiclogo,
  musicbanner, albumcover, cdart`), image `{id, url, lang, likes, season, disc, disc_type}`; swap
  `fanart`→`preview` in URL for thumbnails. This is where the *arrs' `clearlogo` cover type comes from.
- Go: `github.com/michiwend/gomusicbrainz` last commit **2018-10-12** → write our own (~400 lines:
  lookup/browse/search with `inc`, rate limiter, UA). No maintained fanart.tv or CAA Go client → own.

### 2.4 Books — **(verified unless noted)**

- **Goodreads API: dead** (closed to new keys Dec 2020; Readarr scraped it and died with it).
- **Open Library** (Internet Archive, CC0 data): `https://openlibrary.org/search.json?q=&title=&author=
  &fields=key,title,author_name,isbn,cover_i,first_publish_year,editions&limit=`;
  `/search/authors.json?q=`; `/isbn/{isbn}.json`; `/works/{OLID}.json`; `/books/{OLID}.json`;
  `/authors/{OLID}.json`; `/authors/{OLID}/works.json`; legacy `/api/books?bibkeys=ISBN:..&jscmd=data
  &format=json`. Covers: `https://covers.openlibrary.org/b/isbn/{isbn}-L.jpg`, `/b/id/{cover_i}-M.jpg`,
  `/b/olid/{olid}-S.jpg`, `/a/olid/{author}-L.jpg`. Edition identifiers: `isbn_10, isbn_13, lccn,
  oclc_numbers, goodreads, librarything, amazon (ASIN), openlibrary`. Rate: **1 rps unidentified,
  3 rps with a User-Agent containing app name + contact email**; monthly bulk dumps (`ol_dump_*`) for
  offline crosswalk. Free, no key → primary book source.
- **Hardcover** (`https://api.hardcover.app/v1/graphql`, GraphQL/Hasura, `Authorization: Bearer <PAT>`):
  Free plan **60 req/min** (burst 10), **5000/day**; PATs get 2× burst vs legacy JWT; rate-limit headers
  on every response. Terms restrict commercial use (unverified). Rich editions with `isbn_13, isbn_10,
  asin`, and cross ids (Goodreads, OpenLibrary) — good secondary/enrichment source and the natural
  Goodreads replacement for series/ratings.
- **Google Books** `https://www.googleapis.com/books/v1/volumes?q=isbn:{isbn}&key=`: quota reported as
  1,000–10,000 requests/day per key (changed over time); `volumeInfo.industryIdentifiers[{type:
  ISBN_10|ISBN_13|OTHER, identifier}]`. Fallback only.
- **rreading-glasses** (`https://api.bookinfo.pro`, Readarr-proxy-compatible, Goodreads-backed): useful
  for migrating Readarr libraries (Goodreads work ids) but a community instance with no SLA.

### 2.5 Comics / manga — **(verified)**

- **ComicVine** `https://comicvine.gamespot.com/api/{volumes|volume/4050-{id}|issues|issue/4000-{id}|
  publishers|publisher/4010-{id}|search?resources=volume,issue}?api_key=&format=json&field_list=&limit=
  (max 100)&offset=`. **200 requests per resource per hour** + undisclosed per-second velocity
  detection; **non-commercial only**; set a real User-Agent (they block default UAs — community
  report). Volume fields: `id, name, start_year, publisher{id,name}, count_of_issues, description, deck,
  image{...}, aliases, first_issue, last_issue, site_detail_url, date_added, date_last_updated`; issue:
  `id, issue_number, cover_date, store_date, name, volume{id,name}, description, deck, image,
  character_credits, story_arc_credits, site_detail_url`.
- **Metron** `https://metron.cloud/api/` (DRF, Basic auth or token): **20 req/min**, **5000/day**
  (25k for OpenCollective donors); resources `series, issue, publisher, imprint, arc, character, creator,
  team, universe`; lookup filters `cv_id` (ComicVine) and `gcd_id` (Grand Comics Database) → best
  crosswalk for comics; issue has `upc, isbn, sku, cover_hash, cv_id, gcd_id`.
- **MangaDex** `https://api.mangadex.org` (no auth for reads): **~5 rps per IP**, mandatory User-Agent, no
  `Via` header, `offset+limit ≤ 10000`, `limit ≤ 100`; `X-RateLimit-*` headers. `manga.attributes.links`
  carries external ids keyed `al` (AniList), `ap`, `bw`, `mu` (MangaUpdates), `nu`, `kt` (Kitsu), `amz`,
  `ebj`, `mal`, `cdj`, `raw`, `engtl` (keys unverified today but stable for years).
- **MangaUpdates** `https://api.mangaupdates.com/v1`: `POST /series/search {search, page, perpage, type,
  genre, orderby}`, `GET /series/{id}`, `/series/{id}/rss`, `POST /releases/search`; `type ∈ {Manga,
  Manhwa, Manhua, Novel, Artbook, Doujinshi, OEL,...}`; no numeric rate limit, "reasonable spacing +
  caching".
- **AniList** `https://graphql.anilist.co` (GraphQL, public reads without auth): nominal **90 req/min**,
  **currently degraded to 30 req/min** (`X-RateLimit-Limit: 30` live), 1-minute timeout on excess with
  `Retry-After`/`X-RateLimit-Reset`. `Media{id, idMal, type: ANIME|MANGA, format: TV|TV_SHORT|MOVIE|
  SPECIAL|OVA|ONA|MUSIC|MANGA|NOVEL|ONE_SHOT, title{romaji,english,native}, synonyms, externalLinks}`.
  Go: `github.com/rl404/verniy v0.3.4` (2025-05) or `genqlient`.
- **Kitsu** `https://kitsu.io/api/edge` (JSON:API, `Accept: application/vnd.api+json`, no auth for
  reads, `page[limit] ≤ 20`): `/anime?filter[text]=`, `/manga`, `/mappings?filter[externalSite]=anidb
  &filter[externalId]=`, `/anime/{id}/mappings`; `externalSite ∈ {anidb, anilist/anime, anilist/manga,
  myanimelist/anime, myanimelist/manga, thetvdb, thetvdb/series, thetvdb/season, trakt, hulu, imdb,
  mangaupdates}` → cheap crosswalk for anime/manga.

### 2.6 Audiobooks — **(verified)**

- **Audnexus** public `https://api.audnex.us` (GPL-3, `github.com/laxamentumtech/audnexus`, Bun +
  MongoDB + Redis; self-hostable): `GET /books/{asin}?region=us|uk|ca|au|de|fr|es|in|it|jp&update=0|1
  &seedAuthors=`, `/books/{asin}/chapters`, `/authors/{asin}`, `/authors?name=`. Book: `asin, title,
  subtitle, authors[{asin,name}], narrators[{name}], publisherName, releaseDate, runtimeLengthMin,
  summary, description, image, genres[{asin,name,type: genre|tag}], seriesPrimary{asin,name,position},
  seriesSecondary, rating, language, formatType, isAdult, copyright, isbn, literatureType`. Self-hosted
  default limit 100 req/min per source (`MAX_REQUESTS`), HTTP 429 `RATE_LIMIT_EXCEEDED`. Upstream is
  Audible's (unofficial) catalog API. **Recommendation: run our own Audnexus in-cluster** (it is a
  first-class Helm-able service: Bun + Mongo + Redis) and key audiobooks by **Audible ASIN** (+ `isbn`
  when present, + MB release when a match exists).

### 2.7 Anime crosswalk — **(verified)**

- **AniDB** `http://api.anidb.net:9001/httpapi?request=anime&client={registered}&clientver=1&protover=1
  &aid=`: requires client registration; **≤ 1 request / 2 s**, heavy caching required, repeated fetches
  of the same dataset in a day ⇒ ban (decays after ~24 h). Daily title dump
  `https://anidb.net/api/anime-titles.xml.gz` for local title→aid search. Use only via a single
  in-cluster gateway with a persistent cache.
- **Anime-Lists/anime-lists** (XML): `https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/
  anime-list-full.xml`; `<anime anidbid tvdbid defaulttvdbseason episodeoffset tmdbtv tmdbseason tmdbid
  imdbid>` with `<mapping-list><mapping anidbseason tvdbseason start end offset>` and special `tvdbid`
  values `movie|hentai|OVA|web|tv special|unknown`. The canonical AniDB↔TVDB episode-offset table
  (what Plex/Kodi/Shoko use).
- **Fribb/anime-lists** (JSON, daily): `https://raw.githubusercontent.com/Fribb/anime-lists/master/
  anime-list-full.json` = anime-offline-database (manami) ⨝ Anime-Lists by AniDB id, plus TMDB lookups.
  Fields: `type, anidb_id, anilist_id, animecountdown_id, animenewsnetwork_id, anime-planet_id,
  anisearch_id, imdb_id, kitsu_id, livechart_id, mal_id, simkl_id, themoviedb_id, thetvdb_id (+season)`.
  *Out of date (found at gap fix X6b against the live file on 2026-09-23):* the file now carries
  `tvdb_id` (not `thetvdb_id`), `imdb_id` as a list, `themoviedb_id` as an object (`{"tv": n}` or
  `{"movie": [n...]}`), and `season`/`episode_offset` objects keyed `tvdb`/`tmdb`, every field
  optional. `pkg/metadata/clients/animelists` reads that shape and uses it as a series-level id
  resolver only; see its package doc for the first-season-TV-entry rule.
- **Kometa-Team/Anime-IDs** (JSON, daily 00:00 UTC): `https://raw.githubusercontent.com/Kometa-Team/
  Anime-IDs/master/anime_ids.json`, keyed by AniDB id → `tvdb_id, tvdb_season, tvdb_epoffset,
  tmdb_show_id, tmdb_movie_id, imdb_id, mal_id, anilist_id`.
- Sonarr's approach: SkyHook already emits `malIds`/`aniListIds` on the show and Sonarr uses XEM for
  scene numbering. Clustarr: keep TVDB as the canonical series id (release names/indexers use TVDB
  numbering), attach `anidb/anilist/mal/kitsu` via the daily JSON, and store `absolute` order episodes
  from TVDB `/series/{id}/episodes/absolute`.

### 2.8 IMDb — **(verified)**

- No public API. **Non-commercial datasets** `https://datasets.imdbws.com/` (daily): `title.basics.tsv.gz
  (tconst, titleType, primaryTitle, originalTitle, isAdult, startYear, endYear, runtimeMinutes, genres)`,
  `title.akas (titleId, ordering, title, region, language, types, attributes, isOriginalTitle)`,
  `title.episode (tconst, parentTconst, seasonNumber, episodeNumber)`, `title.ratings (tconst,
  averageRating, numVotes)`, `name.basics`, `title.crew`, `title.principals`. "Personal and
  non-commercial use" licence — fine for a self-hosted open-source stack; great for an offline
  IMDb-id ↔ title/year resolver and ratings without OMDb.
- User lists: `ls########` exports now require login → Radarr throws for `ls` ids; only `top250`,
  `popular`, and watchlists `ur########` work, via `api.radarr.video/v1/list/imdb/{id}` (a Radarr-hosted
  scrape). Clustarr option: accept a user-uploaded IMDb CSV export (columns `Const, Title, Year,
  Title Type, IMDb Rating, ...`) or a cookie-authenticated fetch of `https://www.imdb.com/list/{ls}/export`.

### 2.9 Wikidata as a free crosswalk hub — (unverified property ids, from memory)

`https://query.wikidata.org/sparql` / `wbgetentities`. Properties: P345 IMDb, P4947 TMDb movie,
P4983 TMDb TV, P4835 TheTVDB series, P434 MusicBrainz artist, P436 MB release group, P212 ISBN-13,
P957 ISBN-10, P648 Open Library, P2969 Goodreads work, P5646 AniDB, P4086 MyAnimeList, P8729 AniList,
P5905 Comic Vine. TMDB returns `wikidata_id` directly, so a Wikidata hop closes most gaps. Rate-limit
politely (they ask for a UA and ≤ ~1 rps sustained).

---

## 3. Import lists

### 3.1 Trakt — **(verified)**

- Headers: `Content-Type: application/json`, `trakt-api-version: 2`, `trakt-api-key: <client_id>`,
  `Authorization: Bearer <access_token>` for user endpoints.
- **Device flow**: `POST https://api.trakt.tv/oauth/device/code {client_id}` → `{device_code, user_code,
  verification_url, expires_in, interval}`; poll `POST /oauth/device/token {code, client_id, client_secret}`
  every `interval` s → `{access_token, token_type, expires_in, refresh_token, scope, created_at}`;
  statuses **400 pending, 404 invalid code, 409 already used, 410 expired, 418 denied, 429 slow down**.
  Refresh: `POST /oauth/token {refresh_token, client_id, client_secret, redirect_uri, grant_type:
  "refresh_token"}`. **Since 2025-03-20 access tokens expire in 24 hours** (was 3 months; apiary text
  says 7 days — trust the announcement and honour `expires_in`); refresh tokens are **single-use**
  (each refresh returns a new pair). Rate limits: **1000 GET / 5 min**, **1 POST/PUT/DELETE per second**;
  `X-Ratelimit` header JSON. Pagination headers `X-Pagination-Page|Limit|Page-Count|Item-Count`;
  `?extended=full` (`full,images` for art).
- Lists: `/users/{id}/watchlist/{movies|shows}`, `/users/{id}/lists/{list_id}/items/{type}`,
  `/users/{id}/collection/{type}`, `/users/{id}/watched/{type}`, `/movies/{trending|popular|anticipated|
  boxoffice}`, `/movies/{watched|played|collected|recommended}/{daily|weekly|monthly|yearly|all}`,
  `/shows/...` same, `/recommendations/{movies|shows}` (auth). Filters `years=, genres=, languages=,
  countries=, ratings=, certifications=, networks=, limit=`. Item: `{rank, id, listed_at, notes, type,
  movie{title, year, ids{trakt, slug, imdb, tmdb}}}` / `show{... ids{trakt, slug, tvdb, imdb, tmdb}}`.
- How the *arrs avoid shipping a client_secret: authorize at `https://trakt.tv/oauth/authorize?
  redirect_uri=https://auth.servarr.com/v1/trakt/auth` (Sonarr: `trakt_sonarr`), callback returns
  `access_token, refresh_token, expires_in` to the UI; renewal via `https://auth.servarr.com/v1/trakt/renew
  ?refresh=` (Sonarr `trakt_sonarr/renew`). `TraktSettingsBase{AccessToken, RefreshToken, Expires,
  AuthUser, Limit=100, Rating, Certification, Genres, Years, TraktAdditionalParameters}`; Trakt lists
  refresh at most every **12 h**; token refreshed when < 5 min to expiry.
  **Clustarr**: use the **device flow** (no browser redirect needed for a cluster service; client id/secret
  live in a Secret; the controller exposes `user_code` + URL in the CR status) and store tokens in a
  Secret owned by the ImportList CR; refresh proactively (24 h tokens!).
- Go: `github.com/icco/trakt v1.0.4` (device code, poll, refresh, `/sync/*` rows, zero deps) or
  `gitlab.com/ydkn/go-trakt` (fuller: Users/Lists/Watchlist services; 2026-04, untagged). The list
  surface is small; a hand-written client on `net/http` is reasonable.

### 3.2 Plex watchlist — **(verified)**

- `GET https://discover.provider.plex.tv/library/sections/watchlist/all?includeFields=title,type,year,
  ratingKey&excludeElements=Image&includeGuids=1&sort=watchlistedAt:desc&type=1|2 (movie|show)
  &X-Plex-Token=&X-Plex-Client-Identifier=&X-Plex-Container-Start=&X-Plex-Container-Size=`
  (+ `context[device][product|platform|platformVersion|version]`, `clientID`). The old
  `metadata.provider.plex.tv/library/sections/watchlist/all` returns 404 now. Response
  `MediaContainer.Metadata[{ratingKey, guid, type, title, year, Guid[{id: "imdb://tt...", "tmdb://...",
  "tvdb://..."}]}]`. Plex RSS watchlists (`https://rss.plex.tv/...`) carry the same `imdb://`/`tmdb://`
  guids in `<guid>`. *arr min refresh 6 h. Plex OAuth PIN flow (`plex.tv/api/v2/pins`) to obtain the
  token; Sonarr pings `plex.tv` to keep it alive.
- Go: `github.com/LukeHagar/plexgo v0.29.2` has `Provider.GetWatchlist`, `SearchDiscover`,
  `AddToWatchlist`, `RemoveFromWatchlist` (Speakeasy-generated; big). A 60-line hand client is fine too.

### 3.3 TMDB lists / discover — **(verified via Radarr)**

- v3 discover: `/discover/movie?with_companies=`, `?with_keywords=`, `?with_people=` (person),
  `/collection/{id}`, `/movie/popular|top_rated|upcoming|now_playing` (Radarr `TMDbPopularListType:
  Theaters, Popular, Top, Upcoming`); v4 (needs user access token): `/4/list/{id}`,
  `/4/account/{account_id}/movie/{watchlist|recommendations|rated|favorites}` (Radarr
  `TMDbUserListType: Watchlist, Recommendations, Rated, Favorite`). Min refresh 12 h.

### 3.4 MDBList — **(verified)**

- `https://api.mdblist.com` with `?apikey=` (or OAuth bearer). Quotas: **1000 req/day free**, 10k/25k/
  100k/250k for €1/2/3/5 supporters; headers `X-RateLimit-Limit|Remaining|Reset`, `Retry-After`.
  Lists: `GET /lists/user`, `/lists/user/{userid|username}`, `/lists/{listid}`, `/lists/{username}/{listslug}`,
  `/lists/top`, `/lists/search?query=`; items `GET /lists/{listid}/items?limit=&offset=&append_to_response=
  &filter_genre=&sort=&order=` (max 1000, `X-Has-More`) → `{id, rank, title, imdb_id, tvdb_id, ids{mdblist,
  imdb, tmdb, tvdb}, language, mediatype: movie|show, release_year, spoken_language, country}` (note
  `adult` also appears in live responses). Media info `GET /{imdb|tmdb|trakt|tvdb|mal|mdblist}/{movie|show|
  any}/{id}` and batch `POST /{provider}/{type} {"ids":[...]}` → `{title, year, released,
  released_digital, description, runtime, score, score_average, ids{imdb,tmdb,tvdb,trakt,mal,mdblist},
  type, ratings[{source: imdb|tmdb|metacritic|trakt|tomatoes|letterboxd|rogerebert|myanimelist, value,
  score, votes, url}], watch_providers[], language, spoken_language, country, certification, commonsense,
  age_rating, status, trailer, poster, backdrop}`. MDBList is the cheapest **multi-source ratings +
  id crosswalk** endpoint (one call gives imdb/tmdb/tvdb/trakt/mal + 8 rating sources).

### 3.5 Others — **(verified)**

- **StevenLu** `https://popular-movies-data.stevenlu.com/movies.json` (moved from S3 on 2023-09-11;
  nightly; `movies-YYYYMMDD.json` history; also `all-movies.json`); items `{title, imdb_id, poster_url}`.
- **Letterboxd**: official API (`https://api.letterboxd.com/api/v0`, OAuth2) is **by request only and
  explicitly not granted for private/personal projects**; only options are user CSV export
  (`watchlist.csv`: `Date, Name, Year, Letterboxd URI`) or scraping `letterboxd.com/{user}/watchlist/`
  (page HTML has `data-film-slug`; the film page has TMDB/IMDb links). Treat as ToS-risky; ship the CSV
  importer first, HTML scraper as opt-in.
- **Simkl** (Sonarr): OAuth via `https://simkl.com/oauth/authorize` → servarr proxy; min refresh 6 h.
- **Another Radarr/Sonarr/Lidarr** (`/api/v3/movie`, `/api/v3/series`, `/api/v1/artist` with `ProfileIds,
  TagIds, RootFolderPaths` filters; min refresh **15 min**) — Clustarr should expose the same shape so
  existing *arrs can pull from it, and consume it for migration.
- **Custom JSON** (`CustomImport`, 6 h) and **Plex RSS** — accept `{title, year, imdb_id|tmdb_id|tvdb_id}`.
- **AniList / MAL** user lists for anime (Sonarr's `ImportListItemInfo` already has `MalId`, `AniListId`):
  AniList `MediaListCollection(userName, type: ANIME, status: PLANNING)`; MAL v2 `users/@me/animelist`.
- **Last.fm / Spotify / MusicBrainz collections** for music (Lidarr set), **Goodreads shelves are gone**
  → Hardcover lists (`GraphQL lists/list_books`) and Open Library reading logs (`/people/{user}/books/
  want-to-read.json`).

### 3.6 Sync semantics to copy — **(verified)**

- Base list settings (Radarr `ImportListDefinition`): `Enabled, EnableAuto (EnableAutomaticAdd),
  Monitor (movieOnly|movieAndCollection|none), MinimumAvailability, QualityProfileId, RootFolderPath,
  Tags, SearchOnAdd`; Sonarr adds `ShouldMonitor ∈ {All, Future, Missing, Existing, FirstSeason,
  LastSeason, LatestSeason, Pilot, Recent, MonitorSpecials, UnmonitorSpecials, None}`, `MonitorNewItems
  ∈ {All, None}`, `SeriesType`, `SeasonFolder`, `SearchForMissingEpisodes`.
- `ImportListSyncService`: dedupe fetched items by `TmdbId`, then `ImdbId`, then `Title+Year`; skip
  items in `ImportListExclusion{TmdbId, MovieTitle, MovieYear}` (Sonarr: `TvdbId, Title, Year`); resolve
  title-only items via metadata search; add with list defaults.
- `ListSyncLevel` (Config): `disabled | logOnly | keepAndUnmonitor | removeAndKeep | removeAndDelete` —
  what to do with library items no longer on any enabled list.
- Cadence: scheduler tick 5 min; per-list `MinRefreshInterval` (Trakt 12 h, TMDb 12 h, IMDb 12 h,
  Plex 6 h, Simkl 6 h, Custom 6 h, *arr-sync 15 min). Lists that are `Enabled` but not `EnableAuto`
  only populate a "Discover" view.

---

## 4. Go design for `pkg/metadata`

### 4.1 Package layout

```
pkg/metadata/
  ids/            ExternalIDs, Kind, normalisation (tt-prefix, ISBN-10→13, ASIN regex)
  model/          normalized types (below)
  provider/       interfaces + registry + errors (ErrNotFound, ErrRateLimited, ErrAuth)
  cache/          Cache interface; otter (in-proc) + redis/NATS-KV (shared) impls; TTL policy
  httpx/          rate-limited, retrying, UA-stamped http.Client factory (x/time/rate + retryablehttp)
  crosswalk/      Resolver: fan-out across providers/datasets, persisted in KV
  refresh/        ShouldRefresh policies ported from Radarr/Sonarr
  providers/
    tmdb/  tvdb/  musicbrainz/  coverart/  fanart/  openlibrary/  hardcover/  googlebooks/
    audnexus/  comicvine/  metron/  mangadex/  mangaupdates/  anilist/  kitsu/  anidb/
    mdblist/  imdbdatasets/  animelists/ (Fribb/Kometa JSON loader)  wikidata/
```

### 4.2 Identifiers

```go
package ids

type Source string

const (
    IMDb          Source = "imdb"      // tt1234567 (also nm/ for people)
    TMDb          Source = "tmdb"      // int, movie vs tv disambiguated by Kind
    TVDb          Source = "tvdb"      // int series id (never season/movie ids)
    TVMaze        Source = "tvmaze"
    TVRage        Source = "tvrage"
    Wikidata      Source = "wikidata"  // Q23572
    Trakt         Source = "trakt"     // int
    PlexGUID      Source = "plex"      // plex://movie/5d77682f...
    MBArtist      Source = "mb-artist" // uuid
    MBReleaseGrp  Source = "mb-release-group"
    MBRelease     Source = "mb-release"
    MBRecording   Source = "mb-recording"
    ISBN13        Source = "isbn13"    // canonical for books (ISBN-10 converted)
    ASIN          Source = "asin"      // Amazon/Audible, 10 chars
    OpenLibWork   Source = "olwork"    // OL45804W
    OpenLibEd     Source = "oledition" // OL7353617M
    Goodreads     Source = "goodreads" // legacy work id (Readarr migration)
    Hardcover     Source = "hardcover"
    ComicVine     Source = "comicvine" // 4050-xxxxx volume / 4000-xxxxx issue
    Metron        Source = "metron"
    GCD           Source = "gcd"
    AniDB         Source = "anidb"
    AniList       Source = "anilist"
    MAL           Source = "mal"
    Kitsu         Source = "kitsu"
    MangaDex      Source = "mangadex"  // uuid
    MangaUpdates  Source = "mangaupdates"
)

// ExternalIDs is a small ordered map; JSON-serialised into CR status and cache keys.
type ExternalIDs map[Source]string

func (e ExternalIDs) Has(s Source) bool
func (e ExternalIDs) Merge(o ExternalIDs) (changed bool)  // never overwrite a non-empty value with a different one; report conflicts
func Normalize(s Source, v string) (string, error)         // "TT0111161"→"tt0111161", ISBN-10→ISBN-13, trims "4050-"
```

### 4.3 Normalized model (borrowing the *arr field sets)

```go
package model

type Kind string // "movie" | "series" | "artist" | "album" | "book" | "audiobook" | "comic" | "manga"

type Image struct {
    Type   ImageType // poster, banner, fanart(backdrop), screenshot, headshot, clearlogo, clearart, disc, thumb, cover
    URL    string    // remote URL (TMDB t/p, TVDB artworks, fanart.tv, CAA archive.org, OL covers)
    Lang   string    // ISO-639-1 or "" (textless)
    Width, Height int
    Season *int      // TVDB/fanart season art
}

type Rating struct { Source string; Value float64; Votes int; Type string /* user|critic */ }
type Ratings map[string]Rating  // "imdb","tmdb","metacritic","rottentomatoes","trakt","letterboxd","mal","anilist","mb"

type AltTitle struct { Title, Language, Country, Type string } // Type: alternative|translation|scene|aka

type Translation struct { Language, Title, Overview string }

type Provenance struct {
    Provider   string    // "tmdb", "tvdb", ...
    FetchedAt  time.Time
    ETag       string    // or lastUpdated from the provider (TVDB lastUpdated, TMDB changes)
    Version    string    // provider record version if any
}

// ---------- Movies (Radarr MovieMetadata) ----------
type ReleaseType int // TMDB: 1 Premiere, 2 TheatricalLimited, 3 Theatrical, 4 Digital, 5 Physical, 6 TV

type ReleaseDate struct {
    Country       string // ISO-3166-1
    Type          ReleaseType
    Date          time.Time
    Certification string
    Note          string
}

type MovieStatus string // tba | announced | inCinemas | released
type Availability string // tba | announced | inCinemas | released  (Radarr MinimumAvailability)

type Movie struct {
    IDs               ids.ExternalIDs
    Title, OriginalTitle, SortTitle, CleanTitle string
    OriginalLanguage  string
    Overview          string
    Year              int
    SecondaryYear     *int
    Runtime           int // minutes
    Genres, Keywords  []string
    Images            []Image
    Ratings           Ratings
    Certification     string      // for the configured region
    ReleaseDates      []ReleaseDate
    InCinemas, DigitalRelease, PhysicalRelease *time.Time // derived per region preference
    Status            MovieStatus                          // derived, Radarr rules
    Collection        *Collection // {IDs, Title, Overview, Images}
    Studio, Website   string
    TrailerYouTubeID  string
    AlternativeTitles []AltTitle
    Translations      []Translation
    Recommendations   []int // tmdb ids
    Popularity        float64
    Credits           *Credits // optional (cast/crew)
    Provenance        []Provenance
}

// ---------- Series (Sonarr Series/Episode + TVDB v4 season types) ----------
type SeriesType string   // standard | daily | anime
type SeriesStatus string // continuing | ended | upcoming | deleted
type SeasonOrder string  // official | dvd | absolute | alternate | regional | altdvd  (TVDB season-type)

type Series struct {
    IDs              ids.ExternalIDs // tvdb (canonical), imdb, tmdb, tvmaze, tvrage, anidb, anilist, mal, kitsu
    Title, SortTitle, CleanTitle, Slug string
    Overview         string
    Status           SeriesStatus
    Type             SeriesType
    Network, OriginalNetwork string
    OriginalLanguage, OriginalCountry string
    AirTime          string // "21:00"
    AirDays          []time.Weekday
    Runtime          int
    Year             int
    FirstAired, LastAired, NextAired *time.Time
    Genres           []string
    Certification    string
    Ratings          Ratings
    Images           []Image
    Actors           []Actor // {Name, Character, Image}
    AlternativeTitles []AltTitle // incl. scene names / XEM
    Seasons          []Season    // {Number, Images, Order}
    DefaultOrder     SeasonOrder
    Episodes         []Episode   // in DefaultOrder + absolute numbers
    Provenance       []Provenance
}

type Episode struct {
    IDs                  ids.ExternalIDs // tvdb episode id, imdb, tmdb episode id
    SeasonNumber, EpisodeNumber int
    AbsoluteNumber       *int
    SceneSeason, SceneEpisode, SceneAbsolute *int // from XEM/scene mapping
    AiredAfterSeason, AiredBeforeSeason, AiredBeforeEpisode *int // specials placement (TVDB)
    Title, Overview      string
    AirDate              *time.Time // date only in series tz
    AirDateUTC           *time.Time
    Runtime              int
    FinaleType           string // series|season|midseason
    Image                *Image
    Ratings              Ratings
    LastUpdated          time.Time
}

// ---------- Music (Lidarr Artist/Album/AlbumRelease/Track) ----------
type PrimaryAlbumType string   // Album | EP | Single | Broadcast | Other
type SecondaryAlbumType string // Compilation | Soundtrack | Spokenword | Interview | Audiobook | Live | Remix | DJ-mix | Mixtape/Street | Demo | Audio drama | Field recording
type ReleaseStatus string      // official | promotion | bootleg | pseudo-release | withdrawn | cancelled

type Artist struct {
    IDs           ids.ExternalIDs // mb-artist (canonical), wikidata, discogs, spotify(optional)
    Name, SortName, Disambiguation, Overview string
    Type          string // Person | Group | Orchestra | Choir | Character | Other
    Status        string // active | ended
    Country       string
    Begin, End    *time.Time
    Genres, Tags  []string
    Members       []ArtistMember
    Links         []Link // {Type, URL} from url-rels
    Images        []Image // fanart.tv artistthumb/artistbackground/musiclogo
    Ratings       Ratings
    Aliases       []string
    Provenance    []Provenance
}

type Album struct { // MusicBrainz release group
    IDs            ids.ExternalIDs // mb-release-group (canonical)
    ArtistIDs      []string        // mb-artist
    Title, Disambiguation, Overview string
    PrimaryType    PrimaryAlbumType
    SecondaryTypes []SecondaryAlbumType
    ReleaseDate    *time.Time // earliest release
    Genres         []string
    Images         []Image    // CAA front, fanart albumcover/cdart
    Ratings        Ratings
    Links          []Link
    Releases       []AlbumRelease
    Provenance     []Provenance
}

type AlbumRelease struct { // MusicBrainz release
    IDs          ids.ExternalIDs // mb-release, barcode, asin
    Title, Disambiguation string
    Status       ReleaseStatus
    Date         *time.Time
    Country      []string
    Labels       []string
    CatalogNo    string
    Media        []Medium // {Position, Format, Name, TrackCount, Tracks []Track}
    TrackCount   int
}

type Track struct {
    IDs          ids.ExternalIDs // mb-track, mb-recording, isrc
    Title        string
    Position     int     // on medium
    Absolute     int     // across media
    MediumNumber int
    Duration     time.Duration
    ArtistCredit string
}

// ---------- Books (Readarr Author/Book/Edition) ----------
type Author struct {
    IDs         ids.ExternalIDs // olauthor (canonical), goodreads (legacy), hardcover, wikidata, viaf
    Name, SortName, Disambiguation, Overview string
    Born, Died  *time.Time
    Links       []Link
    Images      []Image
    Genres      []string
    Aliases     []string
    Ratings     Ratings
    Provenance  []Provenance
}

type Book struct { // == Open Library *work* / Goodreads *work* / Hardcover *book*
    IDs         ids.ExternalIDs // olwork (canonical), goodreads, hardcover, wikidata
    AuthorIDs   []string
    Title, SortTitle, CleanTitle, Overview string
    FirstPublished *time.Time
    Genres, Subjects []string
    Series      []SeriesLink // {SeriesName, SeriesIDs, Position float64}
    Ratings     Ratings
    Links       []Link
    Editions    []Edition
    Provenance  []Provenance
}

type Edition struct {
    IDs         ids.ExternalIDs // oledition, isbn13, isbn10, asin, googlebooks, lccn, oclc
    Title, Subtitle, Language, Publisher string
    Format      string // hardcover | paperback | ebook | audiobook
    IsEbook     bool
    PageCount   int
    ReleaseDate *time.Time
    Images      []Image // covers.openlibrary.org
    Ratings     Ratings
}

// ---------- Audiobooks (Audnexus) ----------
type Audiobook struct {
    IDs            ids.ExternalIDs // asin (canonical) + region, isbn13, olwork/hardcover link, mb-release (rare)
    Region         string          // us|uk|ca|au|de|fr|es|in|it|jp
    Title, Subtitle string
    Authors        []NamedRef      // {Name, IDs{asin}}
    Narrators      []string
    Publisher      string
    ReleaseDate    *time.Time
    Runtime        time.Duration   // runtimeLengthMin
    Summary, Description string
    Language       string
    Format         string          // formatType: unabridged|abridged
    Genres, Tags   []string        // audnexus genres type=genre|tag
    Series         []SeriesLink    // seriesPrimary/seriesSecondary {asin,name,position}
    Rating         *Rating
    Image          *Image
    IsAdult        bool
    Chapters       []Chapter       // {Title, StartOffsetMs, LengthMs} from /books/{asin}/chapters
    Provenance     []Provenance
}

// ---------- Comics / manga ----------
type ComicVolume struct { // ComicVine volume / Metron series / MangaDex manga / MangaUpdates series
    IDs           ids.ExternalIDs // comicvine (4050-), metron, gcd, mangadex, mangaupdates, anilist, mal, kitsu
    Kind          string          // comic | manga | manhwa | manhua | novel | webtoon
    Title, SortTitle, Description string
    AltTitles     []AltTitle
    Publisher     string
    StartYear, EndYear *int
    IssueCount    int
    Status        string // ongoing | completed | hiatus | cancelled
    Demographic   string // shounen | seinen | ... (manga)
    ContentRating string // safe | suggestive | erotica | pornographic (MangaDex)
    OriginalLanguage string
    Genres, Tags  []string
    Images        []Image
    Ratings       Ratings
    Issues        []ComicIssue // {IDs, Number string, Title, CoverDate, StoreDate, Volume, Chapter float, Image}
    Provenance    []Provenance
}
```

### 4.4 Provider interfaces

```go
package provider

type SearchQuery struct {
    Text      string
    Year      *int
    Language  string // preferred metadata language
    Region    string // release dates / certification country
    Limit     int
    IDs       ids.ExternalIDs // if set, resolve by id first (search may be skipped)
}

type SearchHit struct {
    Kind   model.Kind
    IDs    ids.ExternalIDs
    Title  string
    Year   int
    Poster string
    Score  float64 // provider-relative
}

// Common to every provider.
type Provider interface {
    Name() string                    // "tmdb"
    Kinds() []model.Kind
    // Capabilities lets the crosswalk resolver know what a provider can look up directly.
    Capabilities() Capabilities      // {LookupBy: []ids.Source, Search bool, Changes bool, Artwork bool}
}

type MovieProvider interface {
    Provider
    SearchMovies(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    GetMovie(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.Movie, error)   // full detail; opts.Language/Region/IncludeImages/IncludeCredits
    ResolveMovieIDs(ctx context.Context, id ids.ExternalIDs) (ids.ExternalIDs, error)            // e.g. TMDB /find + /external_ids
    MovieChanges(ctx context.Context, since time.Time) (iter.Seq2[ids.ExternalIDs, error], error) // optional; ErrUnsupported
}

type SeriesProvider interface {
    Provider
    SearchSeries(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    GetSeries(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.Series, error)
    GetEpisodes(ctx context.Context, id ids.ExternalIDs, order model.SeasonOrder) ([]model.Episode, error)
    ResolveSeriesIDs(ctx context.Context, id ids.ExternalIDs) (ids.ExternalIDs, error)
    SeriesChanges(ctx context.Context, since time.Time) (iter.Seq2[ids.ExternalIDs, error], error)
}

type ArtistProvider interface {
    Provider
    SearchArtists(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    SearchAlbums(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    GetArtist(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.Artist, error)
    ListAlbums(ctx context.Context, artist ids.ExternalIDs, f AlbumFilter) ([]model.Album, error)       // MB browse release-group?artist=&type=
    GetAlbum(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.Album, error)          // with releases+tracks
}

type BookProvider interface {
    Provider
    SearchBooks(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    SearchAuthors(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    GetAuthor(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.Author, error)
    ListBooks(ctx context.Context, author ids.ExternalIDs) ([]model.Book, error)
    GetBook(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.Book, error)             // work + editions
    GetEdition(ctx context.Context, id ids.ExternalIDs) (*model.Edition, error)                          // by isbn13/asin/oledition
}

type AudiobookProvider interface {
    Provider
    SearchAudiobooks(ctx context.Context, q SearchQuery) ([]SearchHit, error)    // Audnexus has only /authors?name=; audible search is scraped -> may return ErrUnsupported
    GetAudiobook(ctx context.Context, id ids.ExternalIDs, region string) (*model.Audiobook, error)
    GetChapters(ctx context.Context, id ids.ExternalIDs, region string) ([]model.Chapter, error)
    GetNarratorOrAuthor(ctx context.Context, id ids.ExternalIDs) (*model.Author, error)
}

type ComicProvider interface {
    Provider
    SearchVolumes(ctx context.Context, q SearchQuery) ([]SearchHit, error)
    GetVolume(ctx context.Context, id ids.ExternalIDs, opts FetchOptions) (*model.ComicVolume, error)
    ListIssues(ctx context.Context, id ids.ExternalIDs) ([]model.ComicIssue, error)
}

// ArtworkProvider is orthogonal (fanart.tv, CAA, OL covers): enrich Images for any entity.
type ArtworkProvider interface {
    Provider
    Artwork(ctx context.Context, kind model.Kind, id ids.ExternalIDs) ([]model.Image, error)
}

// Crosswalk-only sources (Kitsu mappings, Fribb JSON, Wikidata, MDBList, IMDb datasets).
type IDResolver interface {
    Provider
    Resolve(ctx context.Context, kind model.Kind, id ids.ExternalIDs) (ids.ExternalIDs, error)
}
```

### 4.5 Aggregation, caching, rate limiting

```go
// Registry composes providers per kind with a priority order and merge policy.
type Registry struct {
    Movies    []MovieProvider     // [tmdb] (+ mdblist for ratings, fanart for art, imdbdatasets for ratings)
    Series    []SeriesProvider    // [tvdb, tmdb] tvdb canonical for numbering; tmdb for backdrops/keywords
    Artists   []ArtistProvider    // [musicbrainz] (+ coverart, fanart, wikidata overview)
    Books     []BookProvider      // [openlibrary, hardcover, googlebooks]
    Audiobooks []AudiobookProvider // [audnexus]
    Comics    []ComicProvider     // [comicvine, metron] / manga: [mangadex, anilist, mangaupdates, kitsu]
    Artwork   []ArtworkProvider
    Resolvers []IDResolver
}

// Merge policy: first provider wins for scalar fields it returns non-empty; images/ratings/alt titles
// are unioned and de-duplicated by (Type, URL) / (Source); IDs are merged with conflict reporting.

// Cache wraps a provider and is keyed "<provider>/<kind>/<source>:<id>/<lang>/<region>".
type Cache interface {
    Get(ctx context.Context, key string, v any) (hit bool, age time.Duration, err error)
    Set(ctx context.Context, key string, v any, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
}
// Two tiers: L1 otter (per pod, 5-15 min), L2 redis or NATS JetStream KV (shared, TTL below).
// TTL policy (from *arr refresh heuristics, per entity state):
//   movie announced/inCinemas: 12h; released & physical within 30d: 24h; old: 7d (hard refresh at 180d)
//   series continuing: 6h; ended & lastAired < 30d: 24h; ended long ago: 30d (or TVDB /updates-driven)
//   artist/album: 7d (MB changes slowly; use LidarrAPI-style "recent" not available -> periodic)
//   book/edition: 30d; audiobook: 30d; comic volume ongoing: 24h, completed: 30d
//   search results: 1h; id crosswalk: 90d (immutable in practice, but allow manual purge)
//   images: URL only, cached forever; bytes fetched by the artwork service on demand
// TVDB /updates?since= and TMDB /movie/changes should drive targeted invalidation instead of TTL expiry.

// Per-provider limiter shared cluster-wide. Option A (simple): all metadata calls go through one
// "metadata" Deployment (replicas=1..N with leader-elected outbound gateway) so an in-process
// x/time/rate.Limiter is sufficient. Option B: a NATS KV/Redis token bucket. Start with A.
type Limits struct {                     // tokens/sec, burst
    TMDB        rate.Limit // 30, burst 40
    TVDB        rate.Limit // 10, burst 20 (no published limit; be polite)
    MusicBrainz rate.Limit // 1, burst 1 + UA "Clustarr/<ver> ( https://github.com/... )"
    OpenLibrary rate.Limit // 3 with UA, else 1
    Hardcover   rate.Limit // 1 (60/min), daily budget 5000
    AniList     rate.Limit // 0.5 (30/min), honour X-RateLimit-Remaining / Retry-After
    AniDB       rate.Limit // 0.5 (1 per 2s), plus per-aid daily cache
    ComicVine   rate.Limit // ~0.05 per resource (200/h), burst 3
    Metron      rate.Limit // 0.33 (20/min), daily 5000
    MangaDex    rate.Limit // 4
    MDBList     rate.Limit // daily budget 1000 (free) - prefer batch POST
    Trakt       rate.Limit // 3 (1000/5min), writes 1/s
    Audnexus    rate.Limit // self-hosted: 100/min per source default
}
```

### 4.6 Crosswalk resolver

```go
// Resolve tries, in order, to fill missing ids for an entity:
//  movie:  tmdb→/external_ids (imdb, wikidata); imdb→tmdb /find; tvdb movie remoteIds; mdblist /{provider}/movie/{id} (imdb,tmdb,tvdb,trakt,mal); wikidata
//  series: tvdb→remoteIds (IMDB, TheMovieDB.com, EIDR, Wikidata); tmdb→/tv/{id}/external_ids (tvdb, imdb, tvrage, wikidata); imdb→tmdb /find?external_source=imdb_id; tvdb→tmdb /find?external_source=tvdb_id;
//          anime: anidb↔tvdb/tmdb/imdb/anilist/mal/kitsu via Fribb anime-list-full.json + Kometa anime_ids.json (both daily), Kitsu /mappings, AniList Media.idMal
//  music:  mb-artist url-rels (wikidata, discogs, official homepage); spotify id → mb via url-rels "streaming"; barcode/isrc → mb release/recording
//  books:  isbn10↔isbn13 (pure function); isbn→olwork+oledition (/isbn/{isbn}.json → works[0].key); oledition.identifiers{goodreads, amazon(asin), librarything, lccn, oclc}; hardcover editions(isbn_13, asin) + book.identifiers
//  audiobooks: asin → audnexus isbn → book crosswalk; MB release (secondary type Audiobook) by barcode when known
//  comics: comicvine id ↔ metron via ?cv_id=; metron.gcd_id; manga: mangadex links {al, mu, kt, mal} → anilist/mangaupdates/kitsu
// Persist resolved sets in KV with 90d TTL and expose them on CR status.externalIDs so other services (indexer, subtitles) never re-resolve.
```

### 4.7 Sketch of a provider implementation (TMDB movie, using golang-tmdb)

```go
func (p *tmdbProvider) GetMovie(ctx context.Context, id ids.ExternalIDs, o provider.FetchOptions) (*model.Movie, error) {
    tmdbID, ok := id[ids.TMDb]
    if !ok {
        resolved, err := p.ResolveMovieIDs(ctx, id) // GetFindByID(imdb, {"external_source":"imdb_id"})
        if err != nil { return nil, err }
        tmdbID = resolved[ids.TMDb]
    }
    if err := p.limiter.Wait(ctx); err != nil { return nil, err }
    d, err := p.c.GetMovieDetails(atoi(tmdbID), map[string]string{
        "language":               o.Language,                       // "en-US"
        "append_to_response":     "release_dates,alternative_titles,translations,external_ids,images,keywords,videos,credits,recommendations",
        "include_image_language": o.Language[:2] + ",null",
    })
    if err != nil { return nil, mapErr(err) } // 404→ErrNotFound, 429→ErrRateLimited (retry-after)
    m := mapMovie(d)                                               // fills IDs{tmdb, imdb, wikidata}, ReleaseDates from d.MovieReleaseDatesAppend
    m.InCinemas, m.DigitalRelease, m.PhysicalRelease = deriveReleases(m.ReleaseDates, o.Region /* "US" */, d.OriginCountry)
    m.Status = deriveStatus(m, p.clock.Now())                      // Radarr rules (announced/inCinemas/released; 90-day cinema rule)
    return m, nil
}
```

---

## 5. Go design for `pkg/importlist`

### 5.1 Types

```go
package importlist

type ItemKind string // movie | series | album | artist | book | audiobook | comic

type Item struct {
    Kind       ItemKind
    IDs        ids.ExternalIDs // at least one of imdb/tmdb/tvdb/mb-*/isbn13/asin/anilist/mal
    Title      string          // fallback for title-only lists (StevenLu has imdb_id; Letterboxd CSV has none)
    Year       int
    ReleaseDate *time.Time
    Rank       int
    Source     string          // list name for provenance / ListSyncLevel
    Extra      map[string]string // e.g. trakt "listed_at", mdblist "rank"
}

type Page struct { Items []Item; Next string /* opaque cursor */ }

type ImportList interface {
    Name() string                                 // "trakt-watchlist"
    Kinds() []ItemKind
    MinRefreshInterval() time.Duration            // Trakt/TMDb/IMDb 12h, Plex/Simkl/Custom 6h, arr-sync 15m, StevenLu 24h
    Fetch(ctx context.Context, cursor string) (Page, error) // paged; implementations do their own rate limiting via httpx
    // Optional:
    //   Authenticator: Begin(ctx) (DeviceCode, error); Poll(ctx, code) (*Token, error); Refresh(ctx, tok) (*Token, error)
    //   Validator: Test(ctx) error
}

type ItemStore interface { // per-list snapshot for ListSyncLevel + dedupe (like ImportListMovieService)
    Replace(ctx context.Context, list string, items []Item) error
    ListItems(ctx context.Context, list string) ([]Item, error)
    Missing(ctx context.Context, libraryIDs []ids.ExternalIDs) ([]ids.ExternalIDs, error) // ids in library but on no enabled list
}
```

### 5.2 Implementations to ship (priority order)

| List | Auth | Item ids | Cadence | Notes |
|---|---|---|---|---|
| Trakt watchlist / lists / collection / trending / popular / anticipated / recommended | device OAuth (24 h tokens, single-use refresh) | trakt, imdb, tmdb, tvdb | 12 h | `extended=full` for year/status |
| Plex Discover watchlist | X-Plex-Token (PIN flow) | imdb, tmdb, tvdb via `Guid[]` | 6 h | `discover.provider.plex.tv`; `type=1` movie, `2` show |
| TMDB discover / popular / collection / company / keyword / person / v4 list / account watchlist | v4 read token (+ user token for account) | tmdb | 12 h | |
| MDBList user/public lists, top | apikey | imdb, tmdb, tvdb, trakt, mal | 12 h | daily quota; also ratings |
| StevenLu | none | imdb | 24 h | `movies.json` nightly |
| IMDb CSV upload / `ur` watchlist / top250 | cookie or file | imdb | 12 h / on upload | `ls` lists need login |
| Another Radarr/Sonarr/Lidarr/Clustarr | api key | tmdb / tvdb / mb | 15 min | migration path |
| Custom JSON / RSS (gofeed) | none | any | 6 h | `guid` `imdb://`/`tmdb://` |
| Simkl | OAuth | imdb, tmdb, tvdb, mal, anilist | 6 h | anime-friendly |
| AniList / MAL planning lists | OAuth (AniList) / client id (MAL) | anilist, mal → tvdb via crosswalk | 12 h | anime series |
| Last.fm / Spotify / MusicBrainz collection | key / OAuth | mb-artist, mb-release-group | 12 h | Spotify needs MB resolver (url-rels) |
| Hardcover want-to-read / OL reading log | PAT / none | hardcover, isbn13, olwork | 12 h | Goodreads replacement |
| Letterboxd CSV (watchlist.csv) | file | title+year → TMDB search | on upload | scraping opt-in only |

### 5.3 Reconciler: list items → inventory CRs

```go
// CRD (inventory API group), one per kind; the ImportList CR carries the defaults that Radarr calls
// ImportListDefinition. Sketch:
//
// apiVersion: importlists.clustarr.io/v1alpha1
// kind: ImportList
// spec:
//   type: trakt            # trakt|plex|tmdb|mdblist|stevenlu|imdb|arr|custom|simkl|anilist|lastfm|hardcover
//   kinds: [movie]         # which inventory kinds this list may create
//   schedule: "12h"        # >= provider MinRefreshInterval; controller clamps
//   enabled: true
//   automaticAdd: true     # false => discover-only (populate status.items, create nothing)
//   secretRef: {name: trakt-creds}      # client id/secret + tokens (controller writes refreshed tokens back)
//   trakt: {listType: watchlist|userList|popular|trending..., user: "me", listSlug: "", limit: 100, filters: {years: "2020-2026", genres: [...], ratings: "60-100"}}
//   defaults:              # applied to created inventory CRs (== ImportListBase settings)
//     qualityProfileRef: {name: hd-1080p}
//     rootFolder: /media/movies
//     monitor: movieOnly | movieAndCollection | none            # series: all|future|missing|existing|firstSeason|lastSeason|latestSeason|pilot|recent|monitorSpecials|unmonitorSpecials|none
//     monitorNewItems: all | none
//     minimumAvailability: released                            # movies only
//     seriesType: standard | daily | anime                     # series only
//     seasonFolder: true
//     searchOnAdd: true
//     tags: [from-trakt]
//   syncLevel: disabled | logOnly | keepAndUnmonitor | removeAndKeep | removeAndDelete   # ListSyncLevel, per list (Radarr has it global)
// status:
//   conditions: [Ready, AuthPending(user_code+verification_url), RateLimited, Error]
//   lastSyncTime, nextSyncTime, itemCount, createdCount, skippedCount
//   auth: {userCode, verificationURL, expiresAt}   # device-flow surfacing
//   items: [{kind, ids, title, year, state: added|exists|excluded|unresolved}]  # capped (e.g. 500) — full list in ItemStore
//
// apiVersion: importlists.clustarr.io/v1alpha1
// kind: ImportExclusion       # == ImportListExclusion
// spec: {kind: movie, ids: {tmdb: "603"}, title: "The Matrix", year: 1999, reason: "..."}

type Reconciler struct {
    Lists    Registry             // type → constructor(spec, secret) ImportList
    Meta     *metadata.Registry   // to resolve title-only items and to fill ids before creating CRs
    Store    ItemStore
    Client   client.Client        // controller-runtime
}

// Reconcile(ImportList):
//  1. if !enabled → skip. if now < lastSync + max(schedule, list.MinRefreshInterval()) → requeue.
//  2. build list from spec+secret; if it needs auth and has no token → run Begin(), publish userCode in status, requeue every `interval`.
//  3. page through Fetch; collect Items; Store.Replace.
//  4. normalise: ids.Normalize each id; dedupe by (kind, tmdb) → (kind, imdb) → (kind, tvdb) → (kind, title+year) (Radarr order).
//  5. skip ImportExclusion matches; skip items whose ids already match an inventory CR (index CRs by every external id via a field indexer on status.externalIDs).
//  6. for items lacking a canonical id (movie: tmdb; series: tvdb; album: mb-release-group; book: olwork; audiobook: asin): Meta.Resolve; if still unresolved → status.items[].state=unresolved (don't create).
//  7. if automaticAdd: create inventory CR (Movie/Series/Album/Book/Audiobook/ComicVolume) with spec from defaults, `metadata.labels[importlists.clustarr.io/source]=<list>`, ownerRef *not* set (library items outlive lists); annotate with list membership for syncLevel.
//  8. syncLevel: compute library CRs labelled from any list but present on no enabled list's latest snapshot → unmonitor / delete CR (keep or delete files via inventory spec.deleteFiles) / log.
//  9. emit events (ItemAdded, ItemSkippedExcluded, Unresolved) to NATS for the UI.
// Concurrency: one worker per ImportList; provider limiters shared. Trakt tokens: refresh when expiresAt-now < 10 min; write new refresh_token back to the Secret atomically (single-use!).
```

### 5.4 Trakt device-flow controller contract

```go
type DeviceAuth interface {
    Begin(ctx context.Context) (DeviceCode, error)               // POST /oauth/device/code
    Poll(ctx context.Context, dc DeviceCode) (*Token, error)      // POST /oauth/device/token; ErrPending(400) ErrSlowDown(429) ErrExpired(410) ErrDenied(418)
    Refresh(ctx context.Context, t Token) (*Token, error)         // POST /oauth/token grant_type=refresh_token (new pair; old refresh token dies)
}
type DeviceCode struct{ DeviceCode, UserCode, VerificationURL string; ExpiresAt time.Time; Interval time.Duration }
type Token struct{ AccessToken, RefreshToken, Scope string; ExpiresAt time.Time; Username string }
```
`github.com/icco/trakt v1.0.4` implements exactly `RequestDeviceCode / PollForToken / RefreshToken`
(with `Token.ExpiresAt()`); reuse it or copy its ~200 lines.

---

## 6. Go libraries (versions from `go list -m` today)

| Module | Version (date) | Use | Verdict |
|---|---|---|---|
| github.com/cyruzin/golang-tmdb | v1.9.4 (2026-07-15) | TMDB v3/v4, typed append_to_response | **adopt** (no ctx; wrap http.Client) |
| github.com/mxrk/tvdb | v0.3.1 (2026-01-06) | TVDB v4 (login+pin, search, remoteid, episodes by season-type, artworks, updates) | **adopt** |
| github.com/dashotv/tvdb | v0.5.2 (2024-07-08) | TVDB v4 Speakeasy-generated | alternative |
| github.com/pioz/tvdb | pseudo v0.0.0-20221212 | TVDB **v3** | skip (v3 API) |
| github.com/michiwend/gomusicbrainz | pseudo v0.0.0-20181012 | MusicBrainz | skip (dead); write own |
| github.com/icco/trakt | v1.0.4 (2026-08-02) | Trakt device auth + refresh + /sync | **adopt for auth**; add list endpoints |
| gitlab.com/ydkn/go-trakt | pseudo v0.0.0-20260418 | Trakt full (Users/Lists/Watchlist) | alternative, untagged |
| github.com/42minutes/go-trakt / hobeone/gotrakt / BrenekH/go-traktdeviceauth | 2016 / 2014 / 2022 | Trakt | skip |
| github.com/LukeHagar/plexgo | v0.29.2 (2026-08-21) | Plex incl. `Provider.GetWatchlist` (discover) | adopt or hand-roll 60 lines |
| github.com/jrudio/go-plex-client | pseudo 2025-01-27 | Plex PMS | skip for watchlist |
| github.com/rl404/verniy | v0.3.4 (2025-05-11) | AniList GraphQL | adopt (or genqlient) |
| github.com/Khan/genqlient | v0.8.1 (2025-05-18) | typed GraphQL (Hardcover, AniList) | **adopt** |
| github.com/hasura/go-graphql-client | v0.16.0 | GraphQL (reflection-based) | alternative |
| github.com/nstratos/go-myanimelist | v0.9.5 (2023-03-29) | MAL v2 | ok if MAL lists wanted |
| github.com/darenliang/jikan-go | v1.2.3 (2022-03-24) | Jikan (MAL unofficial) | optional |
| go.felesatra.moe/anidb | v1.2.2 (2021-04-01) | AniDB HTTP API + titles dump | usable; small |
| golang.org/x/time | v0.16.0 (2026-08-19) | `rate.Limiter` per provider | **adopt** |
| github.com/hashicorp/go-retryablehttp | v0.7.8 (2025-06-18) | retries + Retry-After | **adopt** |
| github.com/maypok86/otter | v1.2.4 (2024-11-22) | L1 in-proc cache (W-TinyLFU, TTL) | **adopt** (or ristretto/v2 v2.4.2, golang-lru/v2 v2.0.7) |
| github.com/redis/go-redis/v9 | v9.23.0-beta.1 (latest tag; use latest stable v9.x) | L2 shared cache / token bucket | adopt if Redis chosen; else NATS KV |
| github.com/eko/gocache/lib/v4 | v4.4.0 | multi-tier cache facade | optional |
| github.com/mmcdole/gofeed | v1.4.2 (2026-08-20) | RSS/Atom import lists (Plex RSS, custom) | **adopt** |
| github.com/gocarina/gocsv | pseudo 2026-09-08 | IMDb / Letterboxd CSV | adopt |
| github.com/PuerkitoBio/goquery | v1.13.0 (2026-08-27) | Letterboxd HTML (opt-in) | optional |
| github.com/gocolly/colly/v2 | v2.3.0 | scraping | avoid (ToS) |
| github.com/jarcoal/httpmock | v1.4.2 | provider tests with recorded JSON | **adopt** for tests |
| github.com/luckylittle/mdblist-cli | pseudo 2025-09-10 | MDBList (CLI, Go) | reference only; write own client |
| github.com/lusoris/goenvoy | pseudo 2026-09-06 | has metadata/tvdb + others | reference only (app repo, not a lib) |

Not found / write ourselves: MusicBrainz, Cover Art Archive, fanart.tv, Open Library, Hardcover
(genqlient), Audnexus, ComicVine, Metron, MangaDex (`darylhjd/mangodex` is 2021), MangaUpdates,
Kitsu (JSON:API), MDBList, StevenLu, Wikidata, IMDb datasets loader, anime-lists JSON loader.

---

## 7. Recommendations (concrete)

1. **Canonical ids per kind**: movie=TMDB, series=TVDB (episode numbering + indexer conventions), album=MB
   release-group (+ MB release chosen by MetadataProfile), artist=MB artist, book=Open Library work
   (+ ISBN-13 per edition), audiobook=Audible ASIN(+region), comic=ComicVine volume id (Metron as
   secondary via cv_id), manga=MangaDex id (AniList id as secondary). Store the full `ExternalIDs` set
   on every inventory CR's `status.externalIDs` and index it.
2. **One metadata gateway service** ("metadatarr"/"librarian") owning outbound provider clients, rate
   limiters, and the L1/L2 cache; other controllers call it via gRPC/NATS request-reply, never the
   providers directly. This is the only way to respect MusicBrainz 1 rps / AniDB 1-per-2s / ComicVine
   200-per-hour across many pods.
3. **Refresh by change feeds where they exist** (TVDB `/updates?since=&type=series,episodes`, TMDB
   `/movie/changes` + `/tv/changes` 14-day windows), TTLs otherwise; port Radarr/Sonarr `ShouldRefresh*`
   rules verbatim (numbers in §4.5).
4. **Region-aware movie availability**: keep the full `release_dates` array, derive
   `InCinemas/Digital/Physical` for the user's region with TMDB-origin-country fallback, and compute
   `MovieStatus` with Radarr's 90-day cinema rule; `MinimumAvailability` lives on the inventory CR spec.
5. **Anime**: TVDB `absolute` order + Fribb/Kometa daily JSON for AniDB/AniList/MAL ids; treat XEM as
   optional (thexem.info has been unreliable); allow AniList as a title/alias source for parsing.
6. **Books**: Open Library first (CC0, 3 rps with UA), Hardcover for series/ratings/Goodreads ids,
   Google Books as ISBN fallback. Model Work ↔ Editions with `anyEditionOk` like Readarr. Offer a
   Readarr migration path by mapping Goodreads work ids through Hardcover/OL identifiers.
7. **Audiobooks**: deploy Audnexus in-cluster (Bun+Mongo+Redis) and key on ASIN+region; link to the book
   work via `isbn` when Audnexus provides it.
8. **Import lists as CRDs** with the field set in §5.3; device-flow state surfaced in `status.auth`;
   tokens in owned Secrets; `syncLevel` per list; `ImportExclusion` CRD; scheduler tick 5 min with
   per-type minimum intervals; discover-only mode when `automaticAdd=false`.
9. **Trakt**: register a Clustarr OAuth app (client id/secret in a Secret), use the device flow, refresh
   proactively (24 h access tokens, single-use refresh tokens — persist the new pair transactionally).
   Do not depend on `auth.servarr.com`.
10. **Legal/ToS flags to document**: TVDB needs a negotiated key or user PINs ($11.99/yr); ComicVine and
    IMDb datasets are non-commercial; Letterboxd forbids private-project API use; MusicBrainz/AniDB/
    Open Library require a contact User-Agent; Hardcover/MDBList have daily quotas.

---

## 8. Sources

- TMDB docs: https://developer.themoviedb.org/docs/rate-limiting , /docs/append-to-response ,
  /reference/find-by-id , /reference/movie-release-dates , /reference/tv-series-external-ids ,
  /reference/tv-series-episode-groups
- TheTVDB v4: https://thetvdb.github.io/v4-api/ , https://raw.githubusercontent.com/thetvdb/v4-api/main/docs/swagger.yml ,
  https://support.thetvdb.com/kb/faq.php?id=62 (licensed vs user-supported), https://thetvdb.com/subscribe
- MusicBrainz: https://musicbrainz.org/doc/MusicBrainz_API/Rate_Limiting ,
  https://python-musicbrainzngs.readthedocs.io/en/v0.7.1/api/ (inc/type enums), https://musicbrainz.org/doc/Cover_Art_Archive/API
- fanart.tv: https://fanarttv.docs.apiary.io/
- Trakt: https://trakt.docs.apiary.io/ , https://github.com/trakt/trakt-api/discussions/495 (24 h tokens, 2025-03-20)
- Plex watchlist migration: https://github.com/Ombi-app/Ombi/issues/5310 , Sonarr `PlexTvService` (DeepWiki)
- Radarr/Sonarr/Lidarr/Readarr internals: DeepWiki queries on Radarr/Radarr, Sonarr/Sonarr, Lidarr/Lidarr,
  Lidarr/LidarrAPI.Metadata, Readarr/Readarr; https://github.com/Radarr/RadarrAPI.TMDB ;
  https://wiki.servarr.com/readarr/status (Retired); https://github.com/readarr/readarr (retirement notice)
- SkyHook URLs: https://github.com/CmdrShepard/Skyhook , https://forums.sonarr.tv/t/why-do-tvdb-api-calls-go-through-skyhook/16104
- Books: https://openlibrary.org/developers/api , https://docs.hardcover.app/api/getting-started/ ,
  https://developers.google.com/books/docs/v1/using
- Comics/manga: https://comicvine.gamespot.com/api/documentation , https://comicvine.gamespot.com/forums/api-developers-2334/api-rate-limiting-1746419/ ,
  https://metron-project.github.io/blog/api-best-practices , https://api.mangadex.org/docs/2-limitations/ ,
  https://api.mangaupdates.com/ , https://docs.anilist.co/guide/rate-limiting , https://kitsu.docs.apiary.io/
- Audiobooks: https://audnex.us/ , https://github.com/laxamentumtech/audnexus
- Anime crosswalk: https://github.com/Anime-Lists/anime-lists , https://github.com/Fribb/anime-lists ,
  https://github.com/Kometa-Team/Anime-IDs , https://wiki.anidb.net/HTTP_API_Definition
- IMDb datasets: https://data.imdb.com/non-commercial-datasets/
- MDBList: https://mdblist.docs.apiary.io/ , https://docs.mdblist.com/docs/api
- StevenLu: https://github.com/sjlu/popular-movies
- Letterboxd: https://letterboxd.com/api-beta/
- Go modules: `go list -m -json <mod>@latest` on 2026-09-18 (Go 1.27)
