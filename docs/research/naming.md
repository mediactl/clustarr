# Clustarr pre-design research: *arr ecosystem conventions, naming, layout

Date: 2026-09-18. Everything below marked **(verified)** was pulled from the live Radarr/Sonarr/Prowlarr/Lidarr/Readarr OpenAPI specs (downloaded to `scratchpad/openapi/*.json` and parsed with `parse.py`), the Servarr wiki markdown source, TRaSH-Guides, Jellyfin/Audiobookshelf/Kavita docs, DeepWiki over the upstream repos, the GitHub search API (`gh api search/repositories`), RDAP, and `go list -m -versions` / `go doc`. Items marked **(unverified)** are from memory and should be checked before being relied on.

---

## 0. TL;DR

| Concern | Recommendation |
|---|---|
| Service names | **catalogarr** (inventory), **indexarr** (indexer/search), **grabarr** (download), **squasharr** (transcode), **captionarr** (subtitles); metadata stays a library (`pkg/metadata`) until it needs its own scaling, reserved name **lorearr** |
| Go module | `github.com/mediactl/clustarr` (the repo `mediactl/clustarr` already exists on GitHub, pushed 2026-09-18) |
| K8s API groups | Per-concern groups under one domain: `catalog.clustarr.io`, `index.clustarr.io`, `download.clustarr.io`, `transcode.clustarr.io`, `subtitle.clustarr.io` (+ `common` Go package, not a group). Register **clustarr.io** (RDAP: unregistered as of today; `.dev`/`.app` also free; `.com` is taken, registered 2025-11-19). |
| Kinds | Typed media Kinds (`Movie`, `Series`, `Episode`, `Artist`, `Album`, `Author`, `Book`, `Audiobook`), not a unified `MediaItem`; shared Go struct `MediaSpecCommon` + `MediaRef` for cross-service targeting. `Download` (not `Grab`), `TranscodeJob`, `SubtitleJob` (brief said `SubtitleTask`; `*Job` matches `batch/v1` and `TranscodeJob`). |
| Layout | kubebuilder **multigroup** layout: `api/<group>/<version>/`, `internal/<service>/`, `cmd/<service>/`, `pkg/` for shared importable code. Do not put service dirs at the repo root. |
| Compat API | Expose a Radarr/Sonarr **v3-shaped** REST facade later (per media type: `/radarr/api/v3/...`, `/sonarr/api/v3/...`) so Jellyseerr/Overseerr, Prowlarr, Bazarr, Recyclarr, autobrr, Unpackerr work unchanged. Same JSON field names as listed in Part C. |
| Licensing gotcha | Radarr/Sonarr/Prowlarr/Lidarr/Readarr/Bazarr are **GPL-3.0**. Borrow *behaviour and field names*, not code (no porting `FileNameBuilder`, parser regexes, or `openapi.json`-generated server stubs) unless Clustarr itself is GPL-3.0. |
| Storage convention | One RWX volume mounted at the **same path in every pod** (`/data`), TRaSH layout `/data/{torrents,usenet,media}/<type>`; hardlinks for torrents, atomic move for usenet. This makes *arr "Remote Path Mappings" unnecessary in-cluster (keep as an escape hatch for external clients). |

---

# Part A: *arr ecosystem conventions Clustarr should be compatible with

## A1. Root folders and folder-per-title

- A **root folder** is a top-level library directory (`/data/media/movies`). Radarr/Sonarr `RootFolderResource` **(verified)**: `id, path, accessible, freeSpace, unmappedFolders[]`. Lidarr/Readarr add per-root defaults **(verified)**: `name, defaultMetadataProfileId, defaultQualityProfileId, defaultMonitorOption, defaultNewItemMonitorOption, defaultTags[], totalSpace`; Readarr adds Calibre fields `isCalibreLibrary, host, port, urlBase, username, password, library, outputFormat, outputProfile, useSsl`.
- Rules from the wiki **(verified)**: a root folder must not overlap the download client's output directory; NFS mounts want `nolock`, SMB mounts want `nobrl`; avoid cloud/rate-limited mounts.
- Every media item stores `rootFolderPath` + `path` (absolute item folder) + `folder` (leaf folder name). Radarr `MovieResource.path`, Sonarr `SeriesResource.path`, Lidarr `ArtistResource.path`, Readarr `AuthorResource.path` **(verified)**.
- **Folder-per-title** is universal: one folder per movie, per series (with season subfolders), per artist (album subfolders), per author (book subfolders). Plex, Jellyfin and Emby all expect this.

TRaSH recommended physical layout **(verified)**, which Clustarr should adopt as the in-cluster convention:

```
/data
├── torrents/{movies,tv,music,books}
├── usenet/{complete,incomplete}/{movies,tv,music,books}
└── media/{movies,tv,music,books}
```

## A2. Naming schemes (tokens)

### Radarr (movies) **(verified: wiki + openapi)**
`NamingConfigResource`: `renameMovies, replaceIllegalCharacters, colonReplacementFormat (delete|dash|spaceDash|spaceDashSpace|smart), standardMovieFormat, movieFolderFormat`.

Tokens: `{Movie Title}`, `{Movie CleanTitle}`, `{Movie OriginalTitle}`, `{Movie TitleThe}`, `{Movie CleanTitleThe}`, `{Movie Collection}`, `{Movie Certification}`, `{Release Year}`, `{ImdbId}`, `{TmdbId}`, `{Quality Full}` ("Bluray-2160p Proper"), `{Quality Title}`, `{Quality Proper}`, `{Quality Real}`, `{MediaInfo Simple}`, `{MediaInfo Full}`, `{MediaInfo AudioCodec}`, `{MediaInfo AudioChannels}`, `{MediaInfo VideoCodec}`, `{MediaInfo VideoBitDepth}`, `{MediaInfo VideoDynamicRangeType}` ("DV HDR10"), `{MediaInfo 3D}`, `{Release Group}`, `{Edition Tags}`, `{Custom Formats}`, `{Original Title}`, `{Original Filename}`. Folder format supports the title/year/id tokens only.

Token modifiers: a token wrapped in extra characters is emitted only when non-empty, e.g. `{-Release Group}` -> `-RlsGrp` or nothing, `{[Quality Full]}`, `{edition-{Edition Tags}}`, `{tmdb-{TmdbId}}`. Separator/case dropdown: Space/Period/Underscore/Dash; Default/Upper/Lower. `{MediaInfo Full:EN+DE}` filters languages, `-` prefix excludes.

TRaSH recommended formats **(verified)**:
```
# Standard Movie Format (Jellyfin variant; Plex uses {tmdb-{TmdbId}}, Emby [tmdb-{TmdbId}])
{Movie CleanTitle} {(Release Year)} [tmdbid-{TmdbId}] - {{Edition Tags}} {[MediaInfo 3D]}{[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}{-Release Group}
# -> The Movie Title (2010) [tmdbid-65567] - {Ultimate Extended Edition} [3D][CF Name][Bluray-2160p Proper][EAC3 Atmos 5.1][DV HDR10][x265]-RlsGrp

# Movie Folder Format
{Movie CleanTitle} ({Release Year})                      # minimum
{Movie CleanTitle} ({Release Year}) {tmdb-{TmdbId}}      # Plex
{Movie CleanTitle} ({Release Year}) [tmdb-{TmdbId}]      # Emby
{Movie CleanTitle} ({Release Year}) [tmdbid-{TmdbId}]    # Jellyfin
```
The brief's `[imdbid-tt...]` form is the Jellyfin IMDb variant (`[imdbid-{ImdbId}]`).

### Sonarr (TV) **(verified)**
`NamingConfigResource`: `renameEpisodes, replaceIllegalCharacters, colonReplacementFormat(int), customColonReplacementFormat, multiEpisodeStyle(int: Extend `S01E01-02-03`, Duplicate `S01E01.S01E02`, Repeat `S01E01E02E03`, Scene `S01E01-E02-E03`, Range `S01E01-03`, PrefixedRange `S01E01-E03` [default]), standardEpisodeFormat, dailyEpisodeFormat, animeEpisodeFormat, seriesFolderFormat, seasonFolderFormat, specialsFolderFormat`.

Code defaults: standard `{Series Title} - S{season:00}E{episode:00} - {Episode Title} {Quality Full}`, daily `{Series Title} - {Air-Date} - {Episode Title} {Quality Full}`, series folder `{Series Title}`, season folder `Season {season}`, specials `Specials`, colon `Smart` (`: ` -> ` - `, else `-`).

Series types **(verified enum)**: `standard | daily | anime`. Anime uses absolute numbering (`{absolute:000}`); daily uses `{Air-Date}` (`2020-09-03`) / `{Air Date}` (`2020 09 03`).

Tokens: `{Series Title}`, `{Series CleanTitle}`, `{Series TitleYear}`, `{Series CleanTitleYear}`, `{Series TitleWithoutYear}`, `{Series CleanTitleWithoutYear}`, `{Series TitleThe}`, `{Series CleanTitleThe}`, `{Series TitleTheYear}`, `{Series TitleFirstCharacter}`, `{Series Year}`, `{ImdbId}`, `{TvdbId}`, `{TmdbId}`, `{TvMazeId}`, `{season:0}`, `{season:00}`, `{episode:0}`, `{episode:00}`, `{absolute:0|00|000}`, `{Air-Date}`, `{Air Date}`, `{Episode Title}`, `{Episode CleanTitle}` (`{Episode CleanTitle:90}` truncates to 90 chars), `{Quality Full|Title|Proper|Real}`, `{MediaInfo Simple|Full|AudioCodec|AudioChannels|AudioLanguages|AudioLanguagesAll|SubtitleLanguages|VideoCodec|VideoBitDepth|VideoDynamicRange|VideoDynamicRangeType}`, `{Release Group}`, `{Release Hash}`, `{Custom Formats}`, `{Original Title}`, `{Original Filename}`.

TRaSH recommended **(verified)**:
```
# Standard
{Series CleanTitleWithoutYear} {(Series Year)} - S{season:00}E{episode:00} - {Episode CleanTitle:90} {[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo VideoCodec]}{-Release Group}
# -> The Series Title! (2010) - S01E01 - Episode Title 1 [AMZN WEBDL-1080p Proper][DV HDR10][DTS 5.1][x264]-RlsGrp
# Daily: ... - {Air-Date} - ...          -> ... - 2013-10-30 - ...
# Anime
{Series CleanTitleWithoutYear} {(Series Year)} - S{season:00}E{episode:00} - {absolute:000} - {Episode CleanTitle:90} {[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{MediaInfo AudioLanguages}{[MediaInfo VideoDynamicRangeType]}[{Mediainfo VideoCodec }{MediaInfo VideoBitDepth}bit]{-Release Group}
# -> The Series Title! (2010) - S01E01 - 001 - Episode Title 1 [iNTERNAL HDTV-720p v2][HDR10][10bit][x264][DTS 5.1][JA]-RlsGrp
# Series folder: {Series CleanTitleWithoutYear} {(Series Year)} [tvdbid-{TvdbId}]   (Jellyfin) | {tvdb-{TvdbId}} (Plex) | [tvdb-{TvdbId}] (Emby); imdb variants exist
# Season folder: Season {season:00}      Specials folder: Specials  (Jellyfin/Plex: "Season 00")
```

### Lidarr (music) **(verified: code defaults via DeepWiki + wiki)**
`NamingConfigResource`: `renameTracks, replaceIllegalCharacters, colonReplacementFormat, standardTrackFormat, multiDiscTrackFormat, artistFolderFormat, includeArtistName, includeAlbumTitle, includeQuality, replaceSpaces, separator, numberStyle`.
Code defaults: `StandardTrackFormat = {Album Title} ({Release Year})/{Artist Name} - {Album Title} - {track:00} - {Track Title}`; `MultiDiscTrackFormat = {Album Title} ({Release Year})/{Medium Format} {medium:00}/{Artist Name} - {Album Title} - {track:00} - {Track Title}`; `ArtistFolderFormat = {Artist Name}`. Note the track format *contains the album folder* (path relative to the artist folder).
Tokens: `{Artist Name|CleanName|NameThe|CleanNameThe|Genre|NameFirstCharacter|MbId|Disambiguation}`, `{Album Title|CleanTitle|TitleThe|CleanTitleThe|Type|Genre|MbId|Disambiguation}`, `{track:0|00}`, `{Track Title|CleanTitle|ArtistName|ArtistCleanName|ArtistNameThe|ArtistMbId}`, `{medium:0|00}`, `{Medium Name}`, `{Medium Format}`, `{Release Year}`, `{Release Group}`, `{Quality Full|Title|Proper}`, `{MediaInfo AudioCodec|AudioChannels|AudioBitRate|AudioBitsPerSample|AudioSampleRate}`, `{Original Title|Filename}`, `{Custom Formats}`, `{Custom Format:Name}`.
Metadata profile **(verified)**: primary album types Album/Single/EP/Broadcast/Other; secondary Compilation/Soundtrack/Spokenword/Interview/Live/Remix/DJ-Mix/Mixtape/Demo; release statuses Official/Promotional/Bootleg/Pseudo-Release. Qualities: `MP3_008..MP3_320, MP3_VBR, MP3_VBR_V2, AAC_192/256/320/VBR, VORBIS_Q5..Q10, FLAC, FLAC_24, ALAC, ALAC_24, WAVPACK, APE, WAV, WMA, Unknown`. Grab unit is the **album** (`Release` has `artistName, albumTitle, discography`).

### Readarr (books/audiobooks) **(verified)** (project retired 2023, still the reference)
`NamingConfigResource`: `renameBooks, replaceIllegalCharacters, colonReplacementFormat, standardBookFormat, authorFolderFormat, includeAuthorName, includeBookTitle, includeQuality, replaceSpaces, separator, numberStyle`. Wiki default `{Book Title}\{Author Name}` (book folder / author-named file), `AuthorFolderFormat = {Author Name}`.
Tokens: `{Author Name|NameThe|CleanName|SortName|Disambiguation}`, `{Book Title|TitleThe|CleanTitle|Subtitle|Series|SeriesPosition|SeriesTitle}`, `{PartNumber}`, `{PartCount}`, `{Release Year}`, `{Release YearFirst}`, `{Edition Year}`, `{Quality Full|Title}`, `{MediaInfo AudioCodec|AudioChannels|AudioBitRate|AudioBitsPerSample|AudioSampleRate}`, `{Release Group}`, `{Original Title|Filename}`. Qualities: ebook EPUB/MOBI/AZW3/PDF, audiobook MP3/M4B/FLAC. Metadata profile: `minPopularity, skipMissingDate, skipMissingIsbn, skipPartsAndSets, skipSeriesSecondary, allowedLanguages (ISO 639-3 csv), minPages, ignored[]`. Grab unit is the **book**; `Edition` (isbn13, asin, format, isEbook, language, publisher) is the per-format child.

### Illegal characters and "CleanTitle" **(verified)**
Characters `: \ / > < ? * | "` are replaced (default) or removed. `CleanTitle` strips punctuation such as apostrophes (`The Series Name's Title` -> `The Series Names Title`) but keeps `!`. `TitleThe` moves a leading "The" to the end (`Series Name, The`). Clustarr's `pkg/naming` should implement: the token grammar (`{Token}`, `{Token:00}`, `{Token:90}` truncation, wrapper-only-when-present, `{MediaInfo Full:EN+DE}` filters), separator/case modes, colon replacement enum, multi-episode styles, and per-media-type token providers.

## A3. Media server expectations

| Server | Movies | TV | Provider-ID tag syntax | Subtitle flags | Notes |
|---|---|---|---|---|---|
| Jellyfin **(verified)** | folder per movie `Film (2018) [imdbid-tt1234567]` or `[tmdbid-12345]`; file = folder name; versions ` - 1080p`, ` - Director's Cut`; extras folders `behind the scenes, deleted scenes, interviews, scenes, samples, shorts, featurettes, clips, extras, trailers, theme-music, backdrops`; NFO `<filename>.nfo` | `Series (2010) [tvdbid-xxx]/Season 01/Series S01E01 Title.mkv`; multi `S01E01-E02`; `Season 00` = specials; never `S01`/`SE01` | `[imdbid-tt..]`, `[tmdbid-..]`, `[tvdbid-..]` (square brackets, `id` suffix) | `Film.en.srt`, `.en.default.srt`, `.en.forced.srt`, `.en.sdh.srt` | Books: `Books/{Audiobooks,Books,Comics}/Author/Book/`, formats azw/azw3/cb7/cbr/cbt/cbz/epub/mobi/pdf; `content.opf`/`metadata.opf`, `ComicInfo.xml`; music: one album per folder, embedded tags win, `cover.jpg`/`folder.jpg` |
| Plex **(verified via secondary sources; support.plex.tv returned 403)** | `Batman Begins (2005) {tmdb-272}/Batman Begins (2005) {tmdb-272}.mp4`; editions `{edition-Director's Cut}` (Plex Pass) | `Band of Brothers (2001)/Season 01/Band of Brothers (2001) - s01e01 - Currahee.mkv`; always the English word "Season" | `{imdb-tt..}`, `{tmdb-..}`, `{tvdb-..}` (curly braces, no `id` suffix) | `.en.forced.srt`, `.en.sdh.srt`, `.en.cc.srt` **(unverified)** | Plex ignores NFO unless an agent (XBMCnfoImporter) is installed |
| Emby | same as Jellyfin but `[tmdb-..]`/`[imdb-..]` (no `id` suffix) per TRaSH **(verified)** | `[tvdb-..]` | square brackets, no `id` suffix | same as Jellyfin | Radarr's "Emby" NFO writer is the Kodi writer |
| Kodi/NFO **(verified via Radarr/Sonarr consumers)** | `<MovieFileName>.nfo` or `movie.nfo` (`UseMovieNfo`); images `poster.jpg`, `fanart.jpg`, `banner/clearart/discart/keyart/landscape/logo/backdrop/clearlogo` | `tvshow.nfo`, `<EpisodeFileName>.nfo`, `poster.jpg`, `fanart.jpg`, `banner.jpg`, `seasonXX-poster.jpg`, `season-specials-poster.jpg`, `<EpisodeFileName>-thumb.jpg` | NFO `<uniqueid type="tmdb" default="true">`, `type="imdb"`, `type="tvdb"` | n/a | Movie NFO fields: title, originaltitle, sorttitle, ratings, plot, runtime, thumb, fanart, mpaa, id, uniqueid, genre, set(tmdbcolid), tag, status, credits, director, premiered, year, studio, trailer, watched, fileinfo/streamdetails, actor. tvshow: title, rating, plot, mpaa, id, uniqueid(tvdb default, imdb, tmdb, tvmaze), genre, tag, status, premiered, enddate, studio, actor, episodeguide; episode: title, season, episode, aired, plot, displayseason/displayepisode/displayafterseason, uniqueid, thumb, watched, rating, fileinfo/streamdetails |
| Audiobookshelf **(verified)** | n/a | n/a | `[B002V0QK4C]` Audible ASIN | n/a | `{Author}/{Series}/{Book}` or `{Author}/{Book}`; book folder parses `Vol 1 - 1994 - Wizards First Rule {Sam Tsoutsouvas}` (sequence, year, title, subtitle after ` - `, narrator in `{}`); disc subfolders `Disc 1`/`CD 2`/`Disk 3`; authors `Last, First` or `A & B`; `desc.txt`, `reader.txt`, OPF/NFO; m4b/m4a/mp3 |
| Kavita/Komga (comics/manga) **(verified Kavita)** | n/a | n/a | n/a | n/a | `Library/Series Name/Series Name v01.cbz`, chapters `c001`, `Vol. 0001 Ch. 0001`, ranges `c001-006`, decimals `025.5`, `Specials/` subfolder; `ComicInfo.xml`; cbz/cbr/cb7/cbt/pdf/epub; Komga: one library per non-overlapping root, series folder per series, "One-Shots" directory |

**Subtitle sidecar naming across the stack (verified)**: Radarr/Sonarr import extras as `{MediaFileName}[.{Title}][.{Copy}].{lang2}[.{tags}].{ext}` (tags e.g. `forced`, `sdh`; `Copy` = `2`, `3` for duplicates). Bazarr writes `Movie.en.srt`, `Movie.en.forced.srt`, `Movie.en.hi.srt` with `hi_extension` default `hi`, options `hi|cc|sdh`; `Movie.pt-br.srt` for regional. Jellyfin documents `sdh`. Recommendation: Clustarr's LanguageProfile defaults `hiTag: sdh` (widest player support), language code = ISO 639-1 with `-REGION` where needed.

## A4. "Import" (completed download handling)

Flow in Radarr/Sonarr **(verified via DeepWiki + wiki)**:
1. **Track**: `CompletedDownloadService.Check()` polls each download client; items are matched to a grab by `downloadId` (torrent infohash / NZB id) and must be in the client **category** configured for the app. `TrackedDownloadState`: `downloading | importBlocked | importPending | importing | imported | failedPending | failed | ignored`; `TrackedDownloadStatus`: `ok | warning | error`; `QueueStatus`: `unknown | queued | paused | downloading | completed | failed | warning | delay | downloadClientUnavailable | fallback` **(verified enums)**.
2. **Resolve path**: apply Remote Path Mappings (`host, remotePath, localPath`; "dumb find/replace", case-sensitive) to the client's reported `outputPath`.
3. **Decide**: `DownloadedMoviesImportService.ProcessPath()` scans video files, `ImportDecisionMaker` runs import specs (sample, still unpacking, free space, matches grab/history, quality is an upgrade over existing file, already imported, not a movie/episode match, "Episode Title Required" wait up to 48h for TBA titles in Sonarr `always|bulkSeasonReleases|never`).
4. **Transfer** (`ImportApprovedMovies`/`ImportApprovedEpisodes`): `ImportMode.Auto` -> **Move** if the client says `CanMoveFiles` (usenet), else **Copy**; with `copyUsingHardlinks=true` (default on) the copy becomes a **hardlink** when source and destination are on the same filesystem, falling back to a real copy. Torrents are never moved so seeding continues.
5. **Place**: build the path with the naming config; create folder; upgrade replaces the old file (old file to **recycle bin** `recycleBin`, `recycleBinCleanupDays`); optionally `chmod` (`setPermissionsLinux`, `chmodFolder` e.g. `755`, `chownGroup`); optionally `fileDate` (`none|cinemas|release`; Sonarr `none|localAirDate|utcAirDate`).
6. **Extras**: `importExtraFiles` + `extraFileExtensions` (srt/sub/idx/nfo/jpg...) copied and renamed alongside; metadata consumers write NFO/images.
7. **Record**: history `downloadFolderImported` with `droppedPath, importedPath, downloadClient, downloadClientName, releaseGroup, customFormatScore, size, indexerFlags, fileId`; then `removeCompletedDownloads` (torrents only once the client reports complete/stopped, i.e. seed goals met), notifications fire.
8. **Manual import** (`/api/v3/manualimport`): user picks folder -> `ManualImportResource {path, relativePath, folderName, name, size, movie, movieFileId, releaseGroup, quality, languages, qualityWeight, downloadId, customFormats, customFormatScore, indexerFlags, rejections[]}`.

`MediaManagementConfigResource` (Radarr, **verified**): `autoUnmonitorPreviouslyDownloadedMovies, recycleBin, recycleBinCleanupDays, downloadPropersAndRepacks (preferAndUpgrade|doNotUpgrade|doNotPrefer), createEmptyMovieFolders, deleteEmptyFolders, fileDate, rescanAfterRefresh (always|afterManual|never), autoRenameFolders, pathsDefaultStatic, setPermissionsLinux, chmodFolder, chownGroup, skipFreeSpaceCheckWhenImporting, minimumFreeSpaceWhenImporting, copyUsingHardlinks, useScriptImport, scriptImportPath, importExtraFiles, extraFileExtensions, enableMediaInfo`.

Permissions (TRaSH, **verified**): per-app user + shared group with `UMASK 002` -> dirs `775`, files `664`; or single user `UMASK 022` -> `755`/`644`. Hardlinks require one filesystem and do not work on exFAT or across mounts. Atomic move = rename(2), not copy+delete.

**Kubernetes translation**: every pod that touches media (download workers, importer, transcode workers, subtitle workers, media server) mounts the **same RWX PVC at `/data`** (NFS/CephFS/hostPath on a single node). Hardlinks work within one export; they do not work across two PVCs even if backed by the same NFS server. Use `fsGroup` + `fsGroupChangePolicy: OnRootMismatch` in the PodSecurityContext instead of chmod loops; keep `chmodFolder/chownGroup` as optional post-import steps for external media servers. Free-space checks use `statfs` on the root folder path.

## A5. The "release" model

Radarr `ReleaseResource` **(verified, 44 props)**: `guid, quality{quality{id,name,source,resolution,modifier},revision{version,real,isRepack}}, customFormats[], customFormatScore, qualityWeight, age (days), ageHours, ageMinutes, size (bytes), indexerId, indexer, releaseGroup, subGroup, releaseHash, title, sceneSource, movieTitles[], languages[{id,name}], mappedMovieId, approved, temporarilyRejected, rejected, tmdbId, imdbId, rejections[] (strings), publishDate, commentUrl, downloadUrl, infoUrl, movieRequested, downloadAllowed, releaseWeight, edition, magnetUrl, infoHash, seeders?, leechers?, protocol (unknown|usenet|torrent), indexerFlags, movieId?, downloadClientId?, downloadClient?, shouldOverride?`. Sonarr adds TV mapping: `fullSeason, seasonNumber, episodeNumbers[], absoluteEpisodeNumbers[], mappedSeasonNumber, mappedEpisodeNumbers[], mappedAbsoluteEpisodeNumbers[], mappedSeriesId, mappedEpisodeInfo[], tvdbId, tvRageId, airDate, seriesTitle, isDaily, isAbsoluteNumbering, isPossibleSpecialEpisode, special, languageWeight, sceneMapping, episodeRequested`. Lidarr/Readarr add `artistName, albumTitle, discography` / `authorName, bookTitle`. Prowlarr's `ReleaseResource` (indexer-side, no decision fields) adds `files, grabs, sortTitle, tvMazeId, posterUrl, categories[{id,name,subCategories}], fileName, indexerFlags[]string`.

Core `ReleaseInfo` (domain object) **(verified)**: `Guid, Title, Size, DownloadUrl, InfoUrl, CommentUrl, IndexerId, Indexer, IndexerPriority, DownloadProtocol, TmdbId, ImdbId, PublishDate, Origin, Source, Container, Codec, Resolution, Languages, IndexerFlags, PendingReleaseReason?`; `TorrentInfo : ReleaseInfo` adds `MagnetUrl, InfoHash, Seeders, Peers`. `Age` etc. are derived from `PublishDate`.

Torznab/Newznab wire format **(verified spec)**: RSS items with `title, guid, link, pubDate, enclosure(url,length,type=application/x-bittorrent|x-nzb)` plus `<torznab:attr name=... value=...>` for `seeders, leechers, peers, infohash, magneturl, size, category, minimumratio, minimumseedtime, downloadvolumefactor, uploadvolumefactor, grabs, files, imdb, tvdbid, tmdbid, genre, year, language, coverurl, poster`. Functions: `t=caps|search|tvsearch|movie|music|book`, params `q, cat, limit, offset, extended, apikey`, `tvsearch: rid|tvdbid|tvmazeid|imdbid|tmdbid, season, ep`, `movie: imdbid|tmdbid`, `music: artist, album, label, track, year, genre`, `book: author, title, publisher, year`. `caps` returns `<server>`, `<limits max default>`, `<searching>` with `available`/`supportedParams`, `<categories>` tree.

Prowlarr capability model **(verified enums)**: `SearchParam: q`; `TvSearchParam: q, season, ep, imdbId, tvdbId, rId, tvMazeId, traktId, tmdbId, doubanId, genre, year`; `MovieSearchParam: q, imdbId, tmdbId, imdbTitle, imdbYear, traktId, genre, doubanId, year`; `MusicSearchParam: q, album, artist, label, year, genre, track`; `BookSearchParam: q, title, author, publisher, genre, year`; `IndexerPrivacy: public|semiPrivate|private`; `IndexerCapabilityResource {limitsMax, limitsDefault, categories[], supportsRawSearch, searchParams[], tvSearchParams[], movieSearchParams[], musicSearchParams[], bookSearchParams[]}`; `IndexerResource {name, implementation (Newznab|Torznab|Cardigann...), definitionName, indexerUrls[], legacyUrls[], language, encoding, enable, redirect, supportsRss, supportsSearch, supportsRedirect, supportsPagination, appProfileId, protocol, privacy, capabilities, priority (1-50, lower wins), downloadClientId, status{disabledTill, mostRecentFailure, initialFailure}, sortName, tags, fields[]}`. App profile: `{enableRss, enableAutomaticSearch, enableInteractiveSearch, minimumSeeders}`. Prowlarr endpoints: `/api/v1/search?query=&type=&indexerIds=&categories=&limit=&offset=`, `/api/v1/search/bulk` (grab), `/api/v1/indexer/{id}/newznab` and `/{id}/api` (Torznab proxy per indexer), `/{id}/download`, `/api/v1/indexer/categories`, `/api/v1/indexerstats`, `/api/v1/indexerstatus`, `/api/v1/indexerproxy` (FlareSolverr/HTTP/SOCKS), `/api/v1/applications` (sync levels `disabled|addOnly|fullSync`), `/api/v1/history` (`releaseGrabbed|indexerQuery|indexerRss|indexerAuth|indexerInfo`).

Newznab standard categories **(verified from Prowlarr source)**: `1000 Console, 2000 Movies (2010 Foreign, 2020 Other, 2030 SD, 2040 HD, 2045 UHD, 2050 BluRay, 2060 3D, 2070 DVD, 2080 WEB-DL, 2090 x265), 3000 Audio (3010 MP3, 3020 Video, 3030 Audiobook, 3040 Lossless, 3050 Other, 3060 Foreign), 4000 PC, 5000 TV (5010 WEB-DL, 5020 Foreign, 5030 SD, 5040 HD, 5045 UHD, 5050 Other, 5060 Sport, 5070 Anime, 5080 Documentary, 5090 x265), 6000 XXX, 7000 Books (7010 Mags, 7020 EBook, 7030 Comics, 7040 Technical, 7050 Other, 7060 Foreign), 8000 Other (8010 Misc, 8020 Hashed)`. Indexer-specific categories are mapped onto these (Cardigann definitions carry the mapping; custom ids >= 100000 exist in the wild).

Radarr `Quality` table **(verified from Quality.cs)**: id/name/source/resolution/modifier: `0 Unknown, 1 SDTV(tv,480), 2 DVD(dvd,0), 3 WEBDL-1080p, 4 HDTV-720p, 5 WEBDL-720p, 6 Bluray-720p, 7 Bluray-1080p, 8 WEBDL-480p, 9 HDTV-1080p, 10 Raw-HD(tv,1080,rawhd), 12 WEBRip-480p, 14 WEBRip-720p, 15 WEBRip-1080p, 16 HDTV-2160p, 17 WEBRip-2160p, 18 WEBDL-2160p, 19 Bluray-2160p, 20 Bluray-480p, 21 Bluray-576p, 22 BR-DISK(bluray,1080,brdisk), 23 DVD-R(dvd,480,remux), 24 WORKPRINT, 25 CAM, 26 TELESYNC, 27 TELECINE, 28 DVDSCR(dvd,480,screener), 29 REGIONAL(dvd,480,regional), 30 Remux-1080p(bluray,1080,remux), 31 Remux-2160p(bluray,2160,remux)`. Ranking order (low->high): Unknown, WORKPRINT, CAM, TELESYNC, TELECINE, DVDSCR, REGIONAL, SDTV, DVD, DVDR, HDTV720p, HDTV1080p, HDTV2160p, WEBDL480p, WEBDL720p, WEBDL1080p, WEBDL2160p, WEBRip480p, WEBRip720p, WEBRip1080p, WEBRip2160p, Bluray480p, Bluray576p, Bluray720p, Bluray1080p, Bluray2160p, Remux1080p, Remux2160p, BRDISK, RAWHD. Enums: `QualitySource: unknown, cam, telesync, telecine, workprint, dvd, tv, webdl, webrip, bluray`; `Modifier: none, regional, screener, rawhd, brdisk, remux`. Sonarr's table differs (no CAM/TELESYNC etc.) **(unverified detail)**.

Go release parsing: `github.com/moistari/rls` v0.6.0 **(verified via go doc)** returns `Release{Type, Artist, Title, Subtitle, Alt, Platform, Arch, Source, Resolution, Collection, Year, Month, Day, Series, Episode, Version, Disc, Codec[], HDR[], Audio[], Channels, Other[], Cut[], Edition[], Language[], Size, Region, Container, Genre, ID, Group, Meta[], Site, Sum, Pass, Req, Ext}` with `rls.Compare`, `rls.MustClean`, `rls.MustNormalize`, `SeriesEpisodes() [][]int`. It covers movies/TV/music/books/apps/games; map `Source+Resolution+Cut/Edition` to the `Quality` table above.

## A6. Decision engine: rejections, delay profiles, pending releases

`DownloadDecision {Release, Rejections[]{Reason string, Type permanent|temporary}}` **(verified)**. Radarr specifications and reasons (permanent unless noted) **(verified list)**:
`AcceptableSize: BelowMinimumSize | UnknownRuntime | AboveMaximumSize`; `AlreadyImported: AlreadyImportedSameHash | AlreadyImportedSameName`; `Availability`; `CustomFormatAllowedByProfile: CustomFormatMinimumScore`; `Delay: MinimumAgeDelay` (**temporary**); `FreeSpace: MinimumFreeSpace`; `History: HistoryRecentCutoffMet | HistoryCdhDisabledCutoffMet | HistoryHigherPreference | HistoryHigherRevision | HistoryCutoffMet | HistoryCustomFormatCutoffMet | HistoryCustomFormatScore | HistoryCustomFormatScoreIncrement | HistoryUpgradesNotAllowed`; `IndexerTag: NoMatchingTag`; `Language: WantedLanguage`; `MaximumSize: MaximumSizeExceeded`; `MinimumAge: MinimumAge` (**temporary**, usenet); `NotSample: Sample`; `Proper: PropersDisabled | ProperForOldFile`; `Protocol: ProtocolDisabled`; `QualityAllowedByProfile: QualityNotWanted`; `Queue: QueueCutoffMet | QueueHigherPreference | QueueHigherRevision | ...`; plus (from Sonarr/Radarr common set, **unverified names**) `Blocklisted`, `Retention`, `UpgradeDisk` (existing file already better), `ReleaseRestrictions` (must/must-not contain), `Monitored` (item/episode unmonitored; search bypasses), `RawDisk` (BR-DISK), `SeasonPackOnly`, `FullSeason` (season pack for still-airing season), `MultiSeason`, `SameEpisodesGrab`, `AnimeVersionUpgrade`.

Rule: RSS sync obeys **all** specs; user-triggered searches skip "monitored"-type checks and interactive grabs may `shouldOverride` mismatches.

Prioritisation (`DownloadDecisionComparer`, **unverified order**): quality (+ revision/proper/repack) -> custom format score -> preferred protocol from the delay profile -> indexer priority -> seeders/leechers (torrent) or age (usenet) -> size.

`DelayProfileResource` **(verified)**: `id, enableUsenet, enableTorrent, preferredProtocol (usenet|torrent), usenetDelay (min), torrentDelay (min), bypassIfHighestQuality, bypassIfAboveCustomFormatScore, minimumCustomFormatScore, order, tags[]`. Semantics **(verified wiki)**: the timer starts when the first matching release is seen; the release is stored as a *pending release* (`PendingReleaseReason: Delay | DownloadClientUnavailable | Fallback` **(unverified enum)**), re-evaluated each RSS sync; after the delay the best pending release is grabbed; `Fallback` = preferred protocol failed/unavailable so the other protocol is used after its delay. Profiles are ordered, matched by tags, default profile (id 1) matches everything and cannot be deleted; `POST /delayprofile/reorder/{id}`.

Indexer options (global) **(verified)**: `minimumAge` (usenet, minutes), `retention` (days, 0 = unlimited), `maximumSize` (MB, 0 = unlimited), `rssSyncInterval` (10-120 min, 0 disables), `preferIndexerFlags`, `allowHardcodedSubs`, `whitelistedHardcodedSubs`. Per-indexer: `enableRss, enableAutomaticSearch, enableInteractiveSearch, priority, downloadClientId, tags, seedRatio/seedTime (torrent)`.

Quality profile **(verified)**: `{name, upgradeAllowed, cutoff (quality id), items[{quality|name+items[] group, allowed}], minFormatScore, cutoffFormatScore, minUpgradeFormatScore, formatItems[{format, score}], language}`; "cutoff" = stop upgrading once reached. Release profiles (Sonarr/Lidarr/Readarr; Radarr has the endpoint too) `{name, enabled, required, ignored, indexerId, tags}` with `/regex/i` support; Lidarr/Readarr also `preferred` term scores (Sonarr moved these to Custom Formats).

## A7. Queue, history, blocklist

- **Queue** = the set of tracked downloads (grabbed but not yet imported/failed). `QueueResource` **(verified)**: `id, movieId, movie, languages, quality, customFormats, customFormatScore, size, title, estimatedCompletionTime, added, status (QueueStatus), trackedDownloadStatus, trackedDownloadState, statusMessages[{title, messages[]}], errorMessage, downloadId, protocol, downloadClient, downloadClientHasPostImportCategory, indexer, outputPath, sizeleft, timeleft`. Endpoints: `GET /queue`, `GET /queue/details`, `GET /queue/status` (`totalCount, count, unknownCount, errors, warnings, unknownErrors, unknownWarnings`), `DELETE /queue/{id}?removeFromClient=&blocklist=&skipRedownload=`, `DELETE /queue/bulk`, `POST /queue/grab/{id}` (force-import pending/blocked items).
- **History** = append-only event log. `HistoryResource` **(verified)**: `id, movieId, sourceTitle, languages, quality, customFormats, customFormatScore, qualityCutoffNotMet, date, downloadId, eventType, data map[string]string, movie`. Radarr `MovieHistoryEventType: unknown, grabbed, downloadFolderImported, downloadFailed, movieFileDeleted, movieFolderImported, movieFileRenamed, downloadIgnored`; Sonarr `EpisodeHistoryEventType: ..., seriesFolderImported, ..., episodeFileDeleted, episodeFileRenamed, ...`; Lidarr `EntityHistoryEventType: grabbed, artistFolderImported, trackFileImported, downloadFailed, trackFileDeleted, trackFileRenamed, albumImportIncomplete, downloadImported, trackFileRetagged, downloadIgnored`; Readarr analogous with `book*`. `data` keys **(verified)**: grabbed -> `indexer, nzbInfoUrl, releaseGroup, age, ageHours, ageMinutes, publishedDate, downloadClient, downloadClientName, size, downloadUrl, guid, tmdbId, imdbId, protocol, customFormatScore, movieMatchType, releaseSource, indexerFlags, indexerId, releaseHash, torrentInfoHash`; downloadFolderImported -> `fileId, droppedPath, importedPath, downloadClient, downloadClientName, releaseGroup, customFormatScore, size, indexerFlags`; downloadFailed/downloadIgnored -> `downloadClient, downloadClientName, message, releaseGroup, size, indexer`; movieFileDeleted -> `reason (Manual|MissingFromDisk|Upgrade), releaseGroup, size, indexerFlags`; movieFileRenamed -> `sourcePath, sourceRelativePath, path, relativePath, ...`. Endpoints: `GET /history?page&pageSize&sortKey&eventType&downloadId`, `GET /history/movie?movieId=`, `GET /history/since?date=`, `POST /history/failed/{id}` (mark as failed).
- **Blocklist** = releases never to grab again. Added on download failure (with `redownloadFailed` triggering a new search), on "remove and blocklist" from the queue, or on manual import rejection. `BlocklistResource` **(verified)**: `id, movieId, sourceTitle, languages, quality, customFormats, date, protocol, indexer, message, movie`; core also stores `torrentInfoHash, publishedDate, size, indexerFlags`. Matching is by infohash (torrent) or title (usenet). Endpoints: `GET /blocklist`, `GET /blocklist/movie?movieId=`, `DELETE /blocklist/{id}`, `DELETE /blocklist/bulk`.

Failed download handling **(verified wiki)**: detect failure (client reports failed / indexer "fail downloads" extension list) -> history `downloadFailed` -> optionally remove from client -> blocklist the release -> optionally search for a replacement (`redownloadFailed`, `redownloadFailedFromInteractiveSearch`).

## A8. RSS sync vs automatic search vs interactive search **(verified)**

| Mode | Trigger | Indexer call | Scope | Decision | Result |
|---|---|---|---|---|---|
| **RSS sync** | scheduler every `rssSyncInterval` (15-60 min typical; the wiki: "24-100 queries per day") | `t=search` (no `q`) / Torznab RSS per indexer with `enableRss` | everything new on the indexer, matched by parsing titles to *monitored* items; "only covers the future" | full spec set; delay profiles create pending releases | auto-grab best approved release per item |
| **Automatic search** | commands `MoviesSearch{movieIds}`, `MissingMoviesSearch`, `CutOffUnmetMoviesSearch`; Sonarr `EpisodeSearch{episodeIds}`, `SeasonSearch{seriesId,seasonNumber}`, `SeriesSearch{seriesId}`, `MissingEpisodeSearch`, `CutoffUnmetEpisodeSearch`; also "search on add" and post-failure redownload | id-based `t=movie&tmdbid=`/`imdbid=`, `t=tvsearch&tvdbid=&season=&ep=`, `t=music&artist=&album=`, `t=book&author=&title=`, with text fallback (`q=<title> <year>`) and scene/alternate titles; per indexer with `enableAutomaticSearch` | one item / season / series | same specs minus "monitored"; season packs considered for full seasons | auto-grab best approved; sets `lastSearchTime` |
| **Interactive search** | user: `GET /api/v3/release?movieId=` (or `episodeId`/`seasonNumber`/`albumId`/`bookId`) | as automatic search but per indexer with `enableInteractiveSearch`; results cached briefly | one item | all releases returned with `approved/rejected/temporarilyRejected/rejections[]`, sorted by `releaseWeight` | user grabs via `POST /api/v3/release {guid, indexerId, movieId?, downloadClientId?, shouldOverride?}` |
| **Push** | external (autobrr, IRC announce) `POST /api/v3/release/push {title, downloadUrl|magnetUrl, protocol, publishDate, indexer, size, ...}` | none | one release | normal decision | grabbed or rejected synchronously |

Sonarr's `ReleaseSearchService` builds `SearchCriteria` by series type (Daily: air date; Anime: absolute numbers + scene mapping; Standard: season/episode), filters indexers by tags, and de-duplicates results by `guid`. Prowlarr's `AppProfile` mirrors the three flags per indexer.

## A9. REST API v3 resource naming (for a compatible facade)

Radarr v3 paths **(verified, full list in scratchpad)**: `alttitle, autotagging, blocklist(/bulk,/movie), calendar, collection, command, config/{downloadclient,host,importlist,indexer,mediamanagement,metadata,naming(/examples),ui}, credit, customfilter, customformat(/bulk,/schema), delayprofile(/reorder/{id}), diskspace, downloadclient(/action/{name},/bulk,/schema,/test,/testall), exclusions(/bulk,/paged), extrafile, filesystem(/mediafiles,/type), health, history(/failed/{id},/movie,/since), importlist(/action,/bulk,/movie,/schema,/test,/testall), indexer(/action,/bulk,/schema,/test,/testall), indexerflag, language, localization, log(/file), manualimport, mediacover/{movieId}/{filename}, metadata(...), movie(/editor,/import,/lookup,/lookup/imdb,/lookup/tmdb,/{id},/{id}/folder), moviefile(/bulk,/editor), notification(...), parse, qualitydefinition(/limits,/update), qualityprofile(/schema), queue(/bulk,/details,/grab/bulk,/grab/{id},/status), release(/push), releaseprofile, remotepathmapping, rename, rootfolder, system/{backup,restart,routes,shutdown,status,task}, tag(/detail), update, wanted/{cutoff,missing}`, plus `/feed/v3/calendar/radarr.ics`, `/ping`, `/login`. Sonarr swaps `movie->series(/editor,/import,/lookup,/{id},/{id}/folder)`, adds `episode(/monitor)`, `episodefile(/bulk,/editor)`, `seasonpass`, `languageprofile`, `importlistexclusion`, `history/series`, `wanted/*/{id}`. Lidarr/Readarr use `/api/v1` with `artist, album(/monitor,/lookup), albumstudio, track, trackfile, retag, search, metadataprofile, config/metadataprovider` / `author, book(/monitor), bookfile, bookshelf, edition, series, retag, search`. Prowlarr is `/api/v1` (list in A5).

Conventions: singular lowercase resource names, `{id}` paths, `schema` = provider templates, `test`/`testall` = connection tests, `action/{name}` = provider-specific RPC, `bulk` = batch edit/delete, `editor` = mass edit, `/command` = async jobs (`CommandResource {name, commandName, message, body, priority normal|high|low, status queued|started|completed|failed|aborted|cancelled|orphaned, result, queued, started, ended, duration, exception, trigger unspecified|manual|scheduled, ...}`), `X-Api-Key` header or `?apikey=` auth, paging via `page, pageSize, sortKey, sortDirection, filterKey, filterValue`, and provider objects share the `{id, name, implementation, implementationName, configContract, infoLink, message, tags, presets, fields[{order,name,label,unit,helpText,value,type,advanced,selectOptions,section,hidden,privacy,placeholder,isFloat}]}` shape (Indexer, DownloadClient, ImportList, Notification, Metadata, IndexerProxy, Application).

Minimum viable compat surface (what Jellyseerr/Overseerr, Prowlarr sync, Bazarr, Recyclarr, autobrr, Unpackerr, Huntarr hit): `system/status` (needs `appName`, `version`, `instanceName`), `health`, `movie`/`series` (GET list, GET id, POST add, PUT, DELETE), `movie/lookup?term=tmdb:123`, `series/lookup?term=tvdb:123`, `qualityprofile`, `rootfolder`, `tag`, `language`, `languageprofile` (Sonarr v3 legacy), `command` (`MoviesSearch`, `RefreshMovie`, `SeriesSearch`, `RescanSeries`, `RssSync`), `queue`, `history`, `release`, `release/push`, `indexer` (+`indexer/schema`, `indexer/test`) for Prowlarr full-sync, `downloadclient`, `customformat` + `qualityprofile` PUT for Recyclarr, `wanted/missing`, `wanted/cutoff`, `calendar`, `episode?seriesId=`, `episodefile`, `moviefile`, `config/naming`, `config/mediamanagement`, `remotepathmapping`, `notification` (Bazarr uses none but reads `episodefile`/`moviefile` and `history`), and SignalR (`/signalr/messages`) which Bazarr uses for live updates (can be omitted; Bazarr falls back to polling).

## A10. Bazarr (subtitles) **(verified)**

- Data model: movies/series/episodes mirrored from Radarr/Sonarr (`TableMovies`, `TableShows`, `TableEpisodes`) every `radarr.movies_sync`/`sonarr.series_sync` = 60 min, full update `Daily`; excluded by tags/series types.
- **Language profile** (`TableLanguagesProfiles`): `name, items[]{id, language (alpha2), forced 'True'|'False', hi 'True'|'False', audio_exclude, audio_only_include}, cutoff (item id that satisfies the profile), mustContain[], mustNotContain[], originalFormat bool, tag`. Config defaults: `hi_extension 'hi' (hi|cc|sdh)`, `subfolder 'current' (current|relative|absolute) + subfolder_custom`, `minimum_score 90` (series, %), `minimum_score_movie 70`, `upgrade_subs true`, `days_to_upgrade_subs 7`, `upgrade_frequency 12h`, `wanted_search_frequency 6h` (series and movies), `adaptive_searching true`, `utf8_encode true`, `use_embedded_subs true`, `ignore_pgs_subs/ignore_vobsub_subs/ignore_ass_subs false`, `chmod_enabled false, chmod '0640'`, `subsync.use_subsync false, subsync_threshold 90, subsync_movie_threshold 70`.
- **Scoring** (`subliminal_patch/score.py`, verified): episode `hash 359, series 160, year 90, season 30, episode 30, source 25, release_group 20, audio_codec 1, resolution 1, video_codec 1, hearing_impaired 1, streaming_service 1` (max without hash = 357); movie `hash 179, title 60, year 40, source 30, edition 30, release_group 15, audio_codec 1, resolution 1, video_codec 1, hearing_impaired 1, streaming_service 1` (max 178). `percent = score*100/max_score`; equivalents: `imdb_id` -> series+year+season+episode (movie: title+year), `tvdb_id` -> +title, `series_imdb_id/series_tvdb_id` -> series+year; framerates 23.976/23.98/24.0 treated equal.
- Workflow: wanted search (missing per profile) on a schedule + on import (SignalR/webhook), upgrade pass for subs below cutoff within `days_to_upgrade_subs`, manual search/download (`/api/providers/movies|episodes`), post-download subsync/translation tools (`/api/subtitles`), post-processing command with `{{directory}}, {{episode}}, {{episode_name}}, {{subtitles}}, {{subtitles_language}}, {{subtitles_language_code2}}, {{subtitles_language_code3}}, {{episode_language}}, {{score}}, {{subtitle_id}}, {{provider}}, {{series_id}}, {{episode_id}}, {{radarr_id}}`.
- Providers via `subliminal_patch` (opensubtitlescom, subsource, addic7ed, podnapisi, titlovi, ...; 60+ in the community catalog). OpenSubtitles.com REST API is the main one (hash + imdb/tmdb id search, daily quota per account).
- Output naming: `{video basename}.{lang}[.forced][.{hi_extension}].{ext}` in `current` folder mode.

---

# Part B: Names, layout, API groups, Kinds

## B1. Constraints and collision survey

Constraints: DNS label (lowercase, `[a-z0-9-]`, <= 63), valid Go package identifier (so no hyphens: `catalogarr` not `catalog-arr`), pronounceable, `-arr` suffix, no collision with a well-known project. Survey via `gh api search/repositories?q=<name> in:name` on 2026-09-18 (count | notable hits, stars, language, last push):

| Name | Hits | Notable | Verdict |
|---|---|---|---|
| clustarr | 5 | `mediactl/clustarr` (this project, pushed today); dormant org `clustarr/{ansible,frontend,backend,docs}` 2020, 1 star each | OK (org name taken on GitHub; `clustarr.io`/`.dev`/`.app` unregistered per RDAP; `.com` registered 2025-11-19 by someone else) |
| downloadarr | 18 | none >1 star with this exact name (LibraryDownloadarr 56 is a different name) | usable, generic |
| grabarr | 7 | all 0-1 star | **free enough** |
| fetcharr | 14 | egg82/fetcharr 340 (Java), Fetcharr org 32 | taken |
| pullarr / leecharr | 1 / 2 | trivial | usable |
| indexarr | 17 | gabryk91/Indexarr 7 (C#, Prowlarr dashboard), techn0phil/indexarr 3 (Go), indexarr-rs 16 (Rust) | small collisions, none a search engine |
| searcharr | 68 | toddrob99/searcharr 305 (Telegram bot) | taken |
| findarr / trackarr / nabarr / scoutarr / seekarr / huntarr | 48/14/17/-/-/- | findarr 64, trackarr 187, nabarr 43 (Go, l3uddz), huntarr popular | taken |
| querarr / sniffarr / probarr | 0 | | free |
| inventarr | 7 | unrelated typos, 0 stars | **free** |
| inventorri | 3 | Inventor (CAD) ribbon tools | free but unpronounceable, reject |
| catalogarr / ledgarr / codexarr | 0 | | **free** |
| vaultarr / hoardarr / holdarr | 1/7/1 | vaultarr 4 (Python) | usable |
| shelfarr / collectarr / librarr / stasharr / keeparr / curatarr | 12/8/217/4/1/- | shelfarr 342, collectarr 64 + org, librarr 278 (Go), stasharr 133 | taken |
| libraryarr / archivarr / storarr | 0-relevant / 3 / 4 | archivarr has two Go repos (2 stars) | avoid |
| transcodarr | 14 | reedylab/transcodarr 25 (Python, "transcoding orchestrator for the *arr ecosystem", active 2026), JacquesToT 4, a Rust one | same-niche collision |
| encodarr | 3 | BrenekH/encodarr 73 (**Go**, distributed encoding) | taken |
| compressarr / convertarr / shrinkarr / packarr | 8/26/2/4 | OFark/Compressarr 45, Kirari04/convertarr 5 (Go) | taken / weak |
| squasharr / hevcarr / renderarr | 0 | | **free** (hevcarr is codec-specific, reject) |
| codarr / recodarr / cruncharr | 8/2/1 | 0-star Go repos exist for codarr and recodarr | usable |
| subarr | 3924 | derekantrican/subarr 413, coaxk/subarr 144 | taken |
| subsarr / sublarr / subdarr | 2/2/1 | 17 / 44 / 6 stars | taken |
| subtitlarr | 3 | 0-1 star Python | usable |
| captionarr / captarr / subtarr / titlarr | 0 | | **free** |
| metarr | 28 | several 0-star Go repos, alrico88/metarr | avoid |
| listarr / wantarr | 126 / 3 | listarr 20, wantarr 79 (Go) | taken |
| lorearr | 0 | | **free** (metadata) |
| infoarr / wisharr | 4 / 1 | unrelated | usable |
| mediarr | 41 | l3uddz/mediarr 35 (Go) | taken |

## B2. Candidates and recommendation per service

| Service | Recommended | Alternatives | Why |
|---|---|---|---|
| Inventory / library of desired+owned media | **catalogarr** | inventarr, ledgarr | zero collisions; "catalog" is exactly a curated list of titles with metadata and desired state; avoids "library", which media servers already use for their scan roots; `inventarr` keeps the brief's word if preferred |
| Indexer aggregation + search | **indexarr** | querarr, sniffarr | semantically perfect for "Prowlarr + Elasticsearch" (index = indexer and search index); the three existing indexarr repos are small dashboards/tools, not a search engine; `querarr` if a clean namespace is wanted |
| Download orchestration | **grabarr** | downloadarr, pullarr | "grab" is the *arr verb for sending a release to a client (`grabbed` history event, `/queue/grab`); short; only 0-1 star collisions. `downloadarr` (user's pick) is acceptable but longer and generic |
| Transcoding | **squasharr** | transcodarr, recodarr | zero collisions; evokes "squash to HEVC 10-bit". `transcodarr` is the most literal but collides with an active same-niche project (reedylab, 25 stars). `encodarr` is taken by a Go project |
| Subtitles | **captionarr** | subtitlarr, subtarr | zero collisions, clear; `subtitlarr` only trivial collisions but 10 chars and awkward; every `sub*arr` short form is taken |
| Metadata + import lists (if split out) | **lorearr** (reserve) | infoarr, wisharr (import lists only) | Start as `pkg/metadata` + `catalog.clustarr.io/ImportList` reconciled by catalogarr. Split into its own deployment only when TMDB/TVDB/MusicBrainz rate limiting or caching needs independent scaling (Servarr runs "Skyhook", a shared metadata proxy, for exactly this reason). Import lists are simply sources of `Movie`/`Series` objects and belong to the catalog |

Binary/package/deploy naming table (all DNS-safe and valid Go identifiers):

| Service | Go pkg / binary | Deployment | K8s group | NATS subject prefix |
|---|---|---|---|---|
| catalogarr | `catalogarr` | `catalogarr` | `catalog.clustarr.io` | `clustarr.catalog.>` |
| indexarr | `indexarr` | `indexarr` | `index.clustarr.io` | `clustarr.index.>` |
| grabarr | `grabarr` | `grabarr` (+ `grabarr-worker`) | `download.clustarr.io` | `clustarr.download.>` |
| squasharr | `squasharr` | `squasharr` (+ `squasharr-worker`) | `transcode.clustarr.io` | `clustarr.transcode.>` |
| captionarr | `captionarr` | `captionarr` | `subtitle.clustarr.io` | `clustarr.subtitle.>` |

Name the **API groups by concern noun**, not by the -arr binary name: groups are a stable public contract, binaries can be renamed or merged (Kubernetes uses `apps`, `batch`, `networking.k8s.io`, not `kube-controller-manager.k8s.io`).

## B3. Top-level name findings

- GitHub org `clustarr` exists but is dormant (last push Oct 2020, 1 star per repo, Python/JS/Ansible). No trademark-level conflict; the user's `mediactl/clustarr` repo is already the project home. Consider asking GitHub for the dormant org name later; not required.
- RDAP (2026-09-18): `clustarr.io` 404 (unregistered), `clustarr.dev` 404, `clustarr.app` 404, `clustarr.com` registered 2025-11-19 (Porkbun nameservers), not by this project. **Register `clustarr.io` before the first CRD ships**; the group name is baked into every manifest.
- Go module path: `github.com/mediactl/clustarr`. If a vanity import path is wanted later, `clustarr.io/clustarr` can be added without breaking the group name.

## B4. Repository layout (Go + kubebuilder multigroup)

kubebuilder v4 multigroup layout **(verified)**: `kubebuilder init --domain clustarr.io --repo github.com/mediactl/clustarr`, `kubebuilder edit --multigroup=true` (PROJECT gets `multigroup: true`), then `kubebuilder create api --group catalog --version v1alpha1 --kind Movie` scaffolds `api/catalog/v1alpha1/movie_types.go` (`+groupName=catalog.clustarr.io`) and `internal/controller/catalog/movie_controller.go`; `config/crd/bases/catalog.clustarr.io_movies.yaml`.

```
clustarr/
  go.mod                         # module github.com/mediactl/clustarr
  PROJECT                        # domain: clustarr.io, multigroup: true
  cmd/
    catalogarr/main.go           # one binary per service (separate scaling + RBAC)
    indexarr/main.go
    grabarr/main.go              # controller; workers run the same binary with `worker` subcommand
    squasharr/main.go
    captionarr/main.go
    clustarr/main.go             # optional all-in-one for dev/kind (runs every manager in-process)
  api/
    common/v1alpha1/             # Go-only shared types (no CRDs): MediaRef, QualityModel, Language, Conditions helpers
    catalog/v1alpha1/            # Movie, Series, Episode, Artist, Album, Author, Book, Audiobook, RootFolder, QualityProfile, ImportList, MetadataProvider
    index/v1alpha1/              # Indexer
    download/v1alpha1/           # DownloadClient, Download, DelayProfile, RemotePathMapping
    transcode/v1alpha1/          # TranscodeProfile, TranscodeJob
    subtitle/v1alpha1/           # LanguageProfile, SubtitleProvider, SubtitleJob
  internal/
    catalogarr/{controller,importlist,metadata,naming}/
    indexarr/{controller,newznab,torznab,cardigann,search,rss}/
    grabarr/{controller,client/{torrent,usenet,qbittorrent,sabnzbd},importer,queue}/
    squasharr/{controller,worker,ffmpeg,profile}/
    captionarr/{controller,provider,scoring,sync}/
    compat/{radarr,sonarr,lidarr,readarr,prowlarr}/   # *arr v3/v1-shaped REST facade (optional, later)
  pkg/                           # importable by other projects
    naming/                      # token engine (Part A2)
    release/                     # release parsing (wraps moistari/rls), quality mapping, categories
    quality/                     # Quality table, profiles, custom formats, TRaSH subset
    decision/                    # specifications + rejections (A6), prioritisation
    events/                      # NATS subjects, CloudEvents envelope, JetStream stream names
    mediainfo/                   # ffprobe wrapper -> MediaInfo tokens
    fsops/                       # hardlink/copy/atomic-move, permissions, free space
    arrapi/                      # shared JSON types for the compat facade
  config/{crd,rbac,manager,samples,default}/   # kustomize from kubebuilder
  charts/clustarr/               # Helm chart wrapping config/
  hack/ docs/ test/e2e/
```

Rationale versus the brief's flat layout (`api, cmd, downloadarr, indexarr, inventorri, transcodarr, pkg`): Go convention is `cmd/` for mains, `internal/` for non-importable service code, `pkg/` for libraries; root-level service directories make every service importable by everyone and fight kubebuilder's scaffolder. If per-service visibility at the root matters, `services/<name>/` is the acceptable compromise (still not importable across services if each has an `internal/`).

## B5. Kubernetes API group naming

Rules **(verified, k8s api-conventions)**: group = lowercase DNS subdomain; use a domain you control; `*.k8s.io`, bare `apps`/`batch`, and the empty group are reserved. Kind = UpperCamelCase singular; resource = lowercase plural; JSON fields lowerCamelCase, no abbreviations except `id`; references `fooRef`/`fooRefs`/`fooName`; units in names (`timeoutSeconds`, `sizeBytes`); string enums CamelCase (`Torrent`, `Usenet`); avoid negative booleans; prefer small enums over bools; conditions `{type, status, reason, message, observedGeneration, lastTransitionTime}` with adjective/past-tense types (`Ready`, `Imported`, `Failed`).

Options:
1. `clustarr.io` single group, all Kinds. Simple, but Kind names must be unique across the whole system: `Series` (TV) clashes with book `Series` (Readarr has `SeriesResource`), `Release` (album release vs indexer release), `Profile` overloads.
2. `media.clustarr.io` + `clustarr.io`. Halfway; still one bag for non-media Kinds.
3. **Per-concern groups `<concern>.clustarr.io`** (recommended). Precedent: `serving.knative.dev`/`eventing.knative.dev`, `networking.istio.io`/`security.istio.io`, `cert-manager.io` + `acme.cert-manager.io`, `apiextensions.crossplane.io`/`pkg.crossplane.io`, `kafka.strimzi.io`, `postgresql.cnpg.io`. Each controller owns exactly one group, RBAC is per group, CRDs can version independently (`catalog` will churn more than `transcode`), and kubebuilder multigroup maps 1:1.

Groups: `catalog.clustarr.io`, `index.clustarr.io`, `download.clustarr.io`, `transcode.clustarr.io`, `subtitle.clustarr.io`. Shared Go types live in `api/common/v1alpha1` (no CRDs, no group). Start every group at `v1alpha1`; promote to `v1beta1` per group.

Categories for kubectl: `+kubebuilder:resource:categories={clustarr,media}` on media Kinds (`kubectl get media -A`), `categories={clustarr}` on everything else. Labels: `clustarr.io/media-type=movie|series|episode|album|book|audiobook`, `catalog.clustarr.io/tmdb-id`, `catalog.clustarr.io/tvdb-id`, `catalog.clustarr.io/root-folder=<name>`, `download.clustarr.io/protocol=torrent|usenet`. Annotations reserved for tool metadata (`clustarr.io/last-search-time` is **not** an annotation; it is status).

## B6. CRD Kinds

Decision: **typed media Kinds, not a unified `MediaItem`**. Typed Kinds get real schemas and validation (Series has seasons; Album has releases and media; Book has editions; Audiobook has narrator/ASIN), match every upstream API, and let `kubectl get movies` be meaningful. Cross-service objects (`Download`, `TranscodeJob`, `SubtitleJob`) reference their target with a typed `MediaRef{apiGroup, kind, name, namespace?}` and consume a shared `MediaSpecCommon` embedded in every media Kind. `Episode` is its own Kind (Sonarr's unit for monitoring/files/grabs; a `Series.status.episodes[]` would blow the ~1.5 MiB object limit for long-running shows and thrash watches); `Track` is **not** a Kind (Lidarr grabs albums, tracks are `Album.status.tracks[]`, bounded); `Edition` is `Book.spec.editions[]`.

| Kind | Group | Plural / short | Scope | Spec highlights | Status highlights |
|---|---|---|---|---|---|
| `Movie` | catalog | movies / mov | Namespaced | `MediaSpecCommon`, `title, year, tmdbId, imdbId, originalLanguage, minimumAvailability (Announced|InCinemas|Released), editionPreference?` | `phase`, `status (TBA|Announced|InCinemas|Released|Deleted)`, `isAvailable`, `metadata{overview, images, runtime, certification, genres, collection{tmdbId,name}, inCinemas, digitalRelease, physicalRelease, ratings, popularity}`, `file{path, relativePath, sizeBytes, quality, languages, releaseGroup, edition, mediaInfo, customFormatScore, dateAdded}`, `lastSearchTime`, `sizeOnDiskBytes`, `conditions[]` (`Ready`, `MetadataRefreshed`, `HasFile`, `CutoffMet`) |
| `Series` | catalog | series / ser | Namespaced | common + `tvdbId, imdbId, tmdbId, tvMazeId, seriesType (Standard|Daily|Anime), seasonFolder, monitorNewItems (All|None), seasons[]{number, monitored}, useSceneNumbering` | `status (Continuing|Ended|Upcoming|Deleted)`, `network, airTime, firstAired, lastAired, nextAiring, previousAiring`, `seasons[]{number, episodeCount, episodeFileCount, sizeOnDiskBytes, percent}`, `statistics` |
| `Episode` | catalog | episodes / ep | Namespaced (ownerRef -> Series) | `seriesRef, seasonNumber, episodeNumber, absoluteEpisodeNumber?, tvdbId, monitored` | `title, airDate, airDateUtc, overview, runtime, finaleType, hasFile, file{...}, sceneNumbering{season, episode, absolute, unverified}, lastSearchTime, grabDate` |
| `Artist` | catalog | artists / art | Namespaced | common + `musicBrainzId (foreignArtistId), metadataProfileRef, monitorNewItems (All|None|New)` | `artistType, disambiguation, status (Continuing|Ended), genres, members, links, statistics` |
| `Album` | catalog | albums / alb | Namespaced (ownerRef -> Artist) | `artistRef, musicBrainzReleaseGroupId, monitored, anyReleaseOk, preferredReleaseId?` | `albumType (Album|Single|EP|Broadcast|Other), secondaryTypes[], releaseDate, releases[]{mbId, title, status (Official|Promotional|Bootleg|PseudoRelease), country[], label[], format, trackCount, mediumCount, media[]{number,name,format}}, tracks[]{mbRecordingId, number, absoluteNumber, mediumNumber, title, durationSeconds, explicit, hasFile, file{...}}, statistics` |
| `Author` | catalog | authors / au | Namespaced | common + `goodreadsId/hardcoverId (foreignAuthorId), metadataProfileRef, monitorNewItems` | `sortName, sortNameLastFirst, status, genres, links, statistics` |
| `Book` | catalog | books / bk | Namespaced (ownerRef -> Author) | `authorRef, foreignBookId, monitored, anyEditionOk, editions[]{foreignEditionId, isbn13, asin, format, isEbook, language, monitored}, series[]{title, position}` | `releaseDate, pageCount, genres, file{...}, statistics` |
| `Audiobook` | catalog | audiobooks / abk | Namespaced | as `Book` but `narrator, asin, targetLayout (audiobookshelf)`; separate Kind because users want an ebook *and* an audiobook of the same title with different root folders/quality profiles (Readarr conflated them) | `durationSeconds, chapters, files[]` |
| `RootFolder` | catalog | rootfolders / rf | Namespaced | `path, mediaType (Movie|Series|Music|Book|Audiobook|Comic), defaults{qualityProfileRef, metadataProfileRef?, monitor, tags[]}, volumeClaimRef?, folderFormat?` | `accessible, freeSpaceBytes, totalSpaceBytes, unmappedFolders[]{name, path, relativePath}` |
| `QualityProfile` | catalog | qualityprofiles / qp | Namespaced (or Cluster) | `mediaType, upgradesAllowed, cutoff (quality name), items[]{quality | group{name, qualities[]}, allowed}, minFormatScore, cutoffFormatScore, minUpgradeFormatScore, formatItems[]{customFormatRef, score}, language, trashPreset?` | `conditions` (validation) |
| `ImportList` | catalog | importlists / il | Namespaced | `mediaType, type (Tmdb|Trakt|Plex|Imdb|Simkl|Spotify|LastFm|Goodreads|Program|Other), config (typed union), secretRef, enabled, enableAutomaticAdd, monitor, searchOnAdd, rootFolderRef, qualityProfileRef, minimumAvailability, refreshIntervalMinutes, exclusions` | `lastRefreshTime, itemCount, conditions` |
| `MetadataProvider` | catalog | metadataproviders / mp | Cluster or Namespaced | `type (Tmdb|Tvdb|MusicBrainz|OpenLibrary|Hardcover|Audible|AniList), endpoint, secretRef, rateLimit{rps, burst}, cacheTtlSeconds, language, country` | `healthy, lastError, quota` |
| `Indexer` | index | indexers / idx | Namespaced | `implementation (Newznab|Torznab|Cardigann), definition (cardigann id), url, apiKeySecretRef, protocol (Usenet|Torrent), privacy (Public|SemiPrivate|Private), enable, enableRss, enableAutomaticSearch, enableInteractiveSearch, priority (1-50), categories[] (newznab ids), minimumSeeders, seedRatio, seedTimeMinutes, packSeedTimeMinutes, minimumAgeMinutes, retentionDays, maximumSizeMB, rssIntervalMinutes, tags[], proxyRef?, downloadClientRef?` | `capabilities{limitsMax, limitsDefault, categories[], searchParams, tvSearchParams, movieSearchParams, musicSearchParams, bookSearchParams, supportsRawSearch}, disabledTill, mostRecentFailure, initialFailure, stats{queries, grabs, rssQueries, authQueries, failed*, avgResponseMs}, lastRssSyncTime` |
| `DownloadClient` | download | downloadclients / dlc | Namespaced | `type (Embedded|QBittorrent|Transmission|Deluge|RTorrent|Sabnzbd|Nzbget|Embedded), protocol, host/port/urlBase/useSsl, secretRef, categories{movie, series, music, book}, postImportCategory?, priority, enable, removeCompletedDownloads, removeFailedDownloads, addPaused, initialState, seedGoals, directory (/data/torrents)` | `healthy, version, freeSpaceBytes, activeCount` |
| `Download` | download | downloads / dl | Namespaced | `targetRef (MediaRef, e.g. Movie or Episode[s] or Album or Book), release{guid, title, indexerRef, protocol, downloadUrl|magnetUrl, infoHash, sizeBytes, publishDate, quality, languages, customFormatScore, releaseGroup, seeders, leechers, indexerFlags}, downloadClientRef?, importMode (Auto|Move|Copy|Hardlink), delay{untilTime, reason (Delay|DownloadClientUnavailable|Fallback)}?, blocklistOnFailure, requestedBy (Rss|AutomaticSearch|InteractiveSearch|Push|Redownload)` | `phase (Pending|Grabbed|Downloading|Completed|Importing|Imported|Failed|Ignored|Blocklisted)`, `trackedStatus (Ok|Warning|Error)`, `statusMessages[]{title, messages[]}`, `downloadId` (client id / infohash), `outputPath`, `sizeBytes, sizeLeftBytes, estimatedCompletionTime, grabbedAt, completedAt, importedAt`, `imported[]{path, relativePath, quality, ...}`, `conditions[]` (`Grabbed`, `Downloaded`, `Imported`, `Failed`, `Blocklisted`) |
| `DelayProfile` | download | delayprofiles / dp | Namespaced | `order, tags[], enableUsenet, enableTorrent, preferredProtocol, usenetDelayMinutes, torrentDelayMinutes, bypassIfHighestQuality, bypassIfAboveCustomFormatScore, minimumCustomFormatScore` | |
| `RemotePathMapping` | download | remotepathmappings / rpm | Namespaced | `downloadClientRef, remotePath, localPath` (only for external clients) | |
| `TranscodeProfile` | transcode | transcodeprofiles / tp | Namespaced | `video{codec (hevc|av1), encoder (libx265|hevc_nvenc|hevc_qsv|hevc_vaapi), preset, crf, bitDepth (10), pixelFormat (yuv420p10le), maxHeight, hdrPassthrough, tonemap}, audio{codec (aac|copy), bitrateKbps, channels, keepLanguages[]}, subtitles{copyText, extractToSidecar, dropImageBased}, container (mkv|mp4), skipIf{alreadyHevc, sizeBelowMB, bitrateBelowKbps}, output{replaceOriginal, keepOriginalDays, suffix}, tags[]` | |
| `TranscodeJob` | transcode | transcodejobs / tj | Namespaced (ownerRef -> media Kind) | `targetRef, sourcePath, profileRef, priority, ttlSecondsAfterFinished, backoffLimit, nodeSelector/affinity for GPU` | `phase (Pending|Scheduled|Running|Succeeded|Failed|Skipped)`, `worker (pod/node)`, `progress{percent, fps, speed, etaSeconds, frame}`, `startTime, completionTime`, `input/output mediaInfo, inputBytes, outputBytes`, `conditions` |
| `LanguageProfile` | subtitle | languageprofiles / lp | Namespaced | `items[]{language (BCP-47), forced (Never|Always|Both), hearingImpaired (Never|Always|Both|Also), excludeIfAudioMatches, onlyIfAudioMatches}, cutoff{language, forced, hi}, mustContain[], mustNotContain[], keepOriginalFormat, hiTag (sdh|hi|cc), subtitleFolder (Alongside|Relative|Absolute), minimumScorePercent{series 90, movie 70}, upgrade{enabled, lookbackDays 7, manualToo}, tags[]` | |
| `SubtitleProvider` | subtitle | subtitleproviders / sp | Namespaced | `type (OpenSubtitlesCom|Subsource|Addic7ed|Podnapisi|...), secretRef, languages[], enabled, priority, rateLimit` | `healthy, quota{used, limit, resetTime}, throttledUntil` |
| `SubtitleJob` | subtitle | subtitlejobs / sj | Namespaced (ownerRef -> media Kind) | `targetRef, filePath, wanted[]{language, forced, hi}, mode (Wanted|Upgrade|Manual), providerRefs?, ttlSecondsAfterFinished` | `phase`, `results[]{language, forced, hi, provider, subtitleId, score, scorePercent, path, matches[], synced}`, `conditions` |

Not CRDs: **Queue** (= `kubectl get downloads` filtered on non-terminal phases), **History** (= Kubernetes Events for humans + a JetStream stream `CLUSTARR_HISTORY` with CloudEvents `clustarr.<group>.<kind>.<event>` persisted by a small history sink to the SQL/KV store the other research track picks), **Blocklist** (= `Download` objects with phase `Blocklisted`/`Failed` and `spec.blocklistOnFailure`; the decision engine lists them by label `download.clustarr.io/target=<kind>-<name>`; add a TTL controller). **CustomFormat** *is* a candidate CRD but belongs to the quality-profile research track (`catalog.clustarr.io/CustomFormat` with the TRaSH subset baked in as presets).

Naming choices worth stating: `Download` over `Grab` (noun, lifecycle object; "Grabbed" becomes a condition); `TranscodeJob`/`SubtitleJob` over `*Task` (matches `batch/v1 Job` vocabulary and the k8s "past-tense/adjective" condition rule); `LanguageProfile` (Bazarr term) not `SubtitleProfile`; `RootFolder` kept verbatim for *arr familiarity; `MetadataProvider` (not `MetadataSource`) because Lidarr/Readarr already expose `/config/metadataprovider`.

## B7. Condition and phase vocabulary (shared)

- Conditions: `Ready`, `Reconciled`, `MetadataRefreshed`, `HasFile`, `CutoffMet`, `Grabbed`, `Downloaded`, `Imported`, `Transcoded`, `Subtitled`, `Failed`, `Blocklisted`, `Degraded` (indexer/client health). Reasons CamelCase from the rejection list (`QualityNotWanted`, `MinimumAgeDelay`, ...).
- Always set `status.observedGeneration`; use `metav1.Condition` + `meta.SetStatusCondition`.
- Phases are for humans/printer columns only; controllers branch on conditions.

---

# Part C: Go data model to borrow (concrete)

```go
// api/common/v1alpha1 (no CRDs; embedded by every group)

// Protocol mirrors *arr DownloadProtocol (unknown|usenet|torrent) with k8s enum casing.
// +kubebuilder:validation:Enum=Usenet;Torrent
type Protocol string

const (
    ProtocolUsenet  Protocol = "Usenet"
    ProtocolTorrent Protocol = "Torrent"
)

// MediaRef points a cross-service object at its catalog target.
type MediaRef struct {
    // +kubebuilder:validation:Enum=Movie;Series;Episode;Artist;Album;Author;Book;Audiobook
    Kind      string `json:"kind"`
    Name      string `json:"name"`
    Namespace string `json:"namespace,omitempty"`
    // Episodes lets one Download/SubtitleJob cover a season pack.
    EpisodeNames []string `json:"episodeNames,omitempty"`
}

// MediaSpecCommon is embedded (inline) in every catalog media Kind.
type MediaSpecCommon struct {
    Monitored          bool                         `json:"monitored"`
    QualityProfileRef  corev1.LocalObjectReference  `json:"qualityProfileRef"`
    RootFolderRef      corev1.LocalObjectReference  `json:"rootFolderRef"`
    // Folder overrides the folder name computed from the naming format.
    Folder             string                       `json:"folder,omitempty"`
    Tags               []string                     `json:"tags,omitempty"`
    AddOptions         *AddOptions                  `json:"addOptions,omitempty"` // searchOnAdd, monitor mode
}

// Quality mirrors Radarr Quality{id,name,source,resolution,modifier} + Revision.
type Quality struct {
    Name       string        `json:"name"`                 // "Bluray-1080p", "FLAC", "EPUB"
    Source     QualitySource `json:"source,omitempty"`     // Unknown|Cam|Telesync|Telecine|Workprint|Dvd|Tv|Webdl|Webrip|Bluray
    Resolution int32         `json:"resolution,omitempty"` // 480, 576, 720, 1080, 2160
    Modifier   Modifier      `json:"modifier,omitempty"`   // None|Regional|Screener|RawHD|BRDisk|Remux
}
type Revision struct {
    Version  int32 `json:"version"`  // 1 = original, 2+ = proper
    Real     int32 `json:"real"`     // REAL count
    IsRepack bool  `json:"isRepack"`
}
type QualityModel struct {
    Quality  Quality  `json:"quality"`
    Revision Revision `json:"revision"`
}

type Language struct {
    Code string `json:"code"` // BCP-47: "en", "pt-BR", "ja"
    Name string `json:"name,omitempty"`
}

// MediaInfo mirrors MediaInfoResource (ffprobe-derived).
type MediaInfo struct {
    AudioBitrate         int64   `json:"audioBitrate,omitempty"`
    AudioChannels        float64 `json:"audioChannels,omitempty"` // 5.1
    AudioCodec           string  `json:"audioCodec,omitempty"`    // "EAC3 Atmos"
    AudioLanguages       string  `json:"audioLanguages,omitempty"`
    AudioStreamCount     int32   `json:"audioStreamCount,omitempty"`
    VideoBitDepth        int32   `json:"videoBitDepth,omitempty"` // 8, 10
    VideoBitrate         int64   `json:"videoBitrate,omitempty"`
    VideoCodec           string  `json:"videoCodec,omitempty"`    // "x265", "h265", "AV1"
    VideoFps             float64 `json:"videoFps,omitempty"`
    VideoDynamicRange    string  `json:"videoDynamicRange,omitempty"`     // "HDR"
    VideoDynamicRangeType string `json:"videoDynamicRangeType,omitempty"` // "DV HDR10", "HDR10+", "HLG"
    Resolution           string  `json:"resolution,omitempty"`    // "1920x1080"
    RunTime              string  `json:"runTime,omitempty"`       // "1:52:03"
    ScanType             string  `json:"scanType,omitempty"`
    Subtitles            string  `json:"subtitles,omitempty"`     // "en/de"
}

// MediaFile mirrors MovieFileResource / EpisodeFileResource / TrackFileResource / BookFileResource.
type MediaFile struct {
    Path                string       `json:"path"`
    RelativePath        string       `json:"relativePath"`
    SizeBytes           int64        `json:"sizeBytes"`
    DateAdded           metav1.Time  `json:"dateAdded"`
    SceneName           string       `json:"sceneName,omitempty"`   // original release title
    ReleaseGroup        string       `json:"releaseGroup,omitempty"`
    Edition             string       `json:"edition,omitempty"`
    Languages           []Language   `json:"languages,omitempty"`
    Quality             QualityModel `json:"quality"`
    CustomFormats       []string     `json:"customFormats,omitempty"`
    CustomFormatScore   int32        `json:"customFormatScore"`
    IndexerFlags        []string     `json:"indexerFlags,omitempty"`
    ReleaseType         string       `json:"releaseType,omitempty"` // SingleEpisode|MultiEpisode|SeasonPack
    MediaInfo           *MediaInfo   `json:"mediaInfo,omitempty"`
    OriginalFilePath    string       `json:"originalFilePath,omitempty"`
    QualityCutoffNotMet bool         `json:"qualityCutoffNotMet"`
}

// Release is the indexer-side record (Prowlarr ReleaseResource + TorrentInfo), reused
// in Download.spec.release and on the NATS search-result subject.
type Release struct {
    GUID          string       `json:"guid"`
    Title         string       `json:"title"`
    SortTitle     string       `json:"sortTitle,omitempty"`
    IndexerRef    string       `json:"indexerRef"`
    IndexerPriority int32      `json:"indexerPriority,omitempty"`
    Protocol      Protocol     `json:"protocol"`
    SizeBytes     int64        `json:"sizeBytes"`
    PublishDate   metav1.Time  `json:"publishDate"`
    DownloadURL   string       `json:"downloadUrl,omitempty"`
    MagnetURL     string       `json:"magnetUrl,omitempty"`
    InfoHash      string       `json:"infoHash,omitempty"`
    InfoURL       string       `json:"infoUrl,omitempty"`
    CommentURL    string       `json:"commentUrl,omitempty"`
    PosterURL     string       `json:"posterUrl,omitempty"`
    Seeders       *int32       `json:"seeders,omitempty"`
    Leechers      *int32       `json:"leechers,omitempty"`
    Grabs         *int32       `json:"grabs,omitempty"`
    Files         *int32       `json:"files,omitempty"`
    Categories    []int32      `json:"categories,omitempty"`   // newznab ids
    IndexerFlags  []string     `json:"indexerFlags,omitempty"` // freeleech, halfleech, internal, scene, ...
    MinimumRatio  *float64     `json:"minimumRatio,omitempty"`
    MinimumSeedTimeSeconds *int64 `json:"minimumSeedTimeSeconds,omitempty"`
    DownloadVolumeFactor *float64 `json:"downloadVolumeFactor,omitempty"`
    UploadVolumeFactor   *float64 `json:"uploadVolumeFactor,omitempty"`
    ImdbID        string       `json:"imdbId,omitempty"`
    TmdbID        int64        `json:"tmdbId,omitempty"`
    TvdbID        int64        `json:"tvdbId,omitempty"`
    TvMazeID      int64        `json:"tvMazeId,omitempty"`
    // Parsed is filled by pkg/release (moistari/rls) and pkg/quality.
    Parsed        *ParsedRelease `json:"parsed,omitempty"`
}

type ParsedRelease struct {
    Titles                 []string     `json:"titles,omitempty"`
    Year                   int32        `json:"year,omitempty"`
    Quality                QualityModel `json:"quality"`
    Languages              []Language   `json:"languages,omitempty"`
    ReleaseGroup           string       `json:"releaseGroup,omitempty"`
    ReleaseHash            string       `json:"releaseHash,omitempty"`
    Edition                string       `json:"edition,omitempty"`
    SeasonNumber           *int32       `json:"seasonNumber,omitempty"`
    EpisodeNumbers         []int32      `json:"episodeNumbers,omitempty"`
    AbsoluteEpisodeNumbers []int32      `json:"absoluteEpisodeNumbers,omitempty"`
    AirDate                string       `json:"airDate,omitempty"`
    FullSeason             bool         `json:"fullSeason,omitempty"`
    IsDaily                bool         `json:"isDaily,omitempty"`
    IsAbsoluteNumbering    bool         `json:"isAbsoluteNumbering,omitempty"`
    Special                bool         `json:"special,omitempty"`
    HardcodedSubs          string       `json:"hardcodedSubs,omitempty"`
    ArtistName, AlbumTitle string       // music
    AuthorName, BookTitle  string       // books
    Discography            bool         `json:"discography,omitempty"`
}

// Decision output (pkg/decision), surfaced on interactive search and Download.status.
type Rejection struct {
    Reason  string `json:"reason"`          // CamelCase from the A6 list, e.g. QualityNotWanted
    Message string `json:"message"`
    // +kubebuilder:validation:Enum=Permanent;Temporary
    Type    string `json:"type"`
}
type ReleaseDecision struct {
    Release             Release     `json:"release"`
    TargetRef           *MediaRef   `json:"targetRef,omitempty"`
    Approved            bool        `json:"approved"`
    TemporarilyRejected bool        `json:"temporarilyRejected"`
    Rejections          []Rejection `json:"rejections,omitempty"`
    CustomFormatScore   int32       `json:"customFormatScore"`
    QualityWeight       int32       `json:"qualityWeight"`
    ReleaseWeight       int32       `json:"releaseWeight"` // sort key for interactive search
}

// download.clustarr.io/v1alpha1
type DownloadSpec struct {
    TargetRef         MediaRef                     `json:"targetRef"`
    Release           Release                      `json:"release"`
    DownloadClientRef *corev1.LocalObjectReference `json:"downloadClientRef,omitempty"`
    // +kubebuilder:validation:Enum=Auto;Move;Copy;Hardlink
    ImportMode        string                       `json:"importMode,omitempty"`
    Delay             *DownloadDelay               `json:"delay,omitempty"`
    BlocklistOnFailure bool                        `json:"blocklistOnFailure,omitempty"`
    // +kubebuilder:validation:Enum=Rss;AutomaticSearch;InteractiveSearch;Push;Redownload
    RequestedBy       string                       `json:"requestedBy"`
}
type DownloadDelay struct {
    Until  metav1.Time `json:"until"`
    // +kubebuilder:validation:Enum=Delay;DownloadClientUnavailable;Fallback
    Reason string      `json:"reason"`
}
type DownloadStatus struct {
    ObservedGeneration      int64              `json:"observedGeneration,omitempty"`
    // +kubebuilder:validation:Enum=Pending;Grabbed;Downloading;Completed;Importing;Imported;Failed;Ignored;Blocklisted
    Phase                   string             `json:"phase,omitempty"`
    // +kubebuilder:validation:Enum=Ok;Warning;Error
    TrackedStatus           string             `json:"trackedStatus,omitempty"`
    StatusMessages          []StatusMessage    `json:"statusMessages,omitempty"` // {title, messages[]}
    DownloadID              string             `json:"downloadId,omitempty"`     // infohash or NZB id in the client
    DownloadClient          string             `json:"downloadClient,omitempty"`
    OutputPath              string             `json:"outputPath,omitempty"`
    SizeBytes               int64              `json:"sizeBytes,omitempty"`
    SizeLeftBytes           int64              `json:"sizeLeftBytes,omitempty"`
    EstimatedCompletionTime *metav1.Time       `json:"estimatedCompletionTime,omitempty"`
    GrabbedAt, CompletedAt, ImportedAt *metav1.Time
    ImportedFiles           []MediaFile        `json:"importedFiles,omitempty"`
    Conditions              []metav1.Condition `json:"conditions,omitempty"`
}

// catalog.clustarr.io/v1alpha1 naming config (per RootFolder or cluster default)
type NamingConfig struct {
    Rename                   bool   `json:"rename"`
    ReplaceIllegalCharacters bool   `json:"replaceIllegalCharacters"`
    // +kubebuilder:validation:Enum=Delete;Dash;SpaceDash;SpaceDashSpace;Smart;Custom
    ColonReplacement         string `json:"colonReplacement"`
    CustomColonReplacement   string `json:"customColonReplacement,omitempty"`
    // +kubebuilder:validation:Enum=Extend;Duplicate;Repeat;Scene;Range;PrefixedRange
    MultiEpisodeStyle        string `json:"multiEpisodeStyle,omitempty"`
    // +kubebuilder:validation:Enum=Plex;Jellyfin;Emby;Kodi
    MediaServerDialect       string `json:"mediaServerDialect,omitempty"` // picks {tmdb-}/[tmdbid-]/[tmdb-] presets
    MovieFormat, MovieFolderFormat                         string
    StandardEpisodeFormat, DailyEpisodeFormat, AnimeEpisodeFormat string
    SeriesFolderFormat, SeasonFolderFormat, SpecialsFolderFormat  string
    StandardTrackFormat, MultiDiscTrackFormat, ArtistFolderFormat string
    StandardBookFormat, AuthorFolderFormat                        string
    AudiobookFolderFormat                                         string // "{Author Name}/{Book Series}/{Book SeriesPosition} - {Release Year} - {Book Title} {{Narrator}}"
}
```

Interface sketches (pkg-level, service-agnostic):

```go
// pkg/naming
type Builder interface {
    // Path renders folder + filename for the target using the tokens each Kind exposes.
    Path(ctx context.Context, cfg NamingConfig, target TokenSource, file MediaFile) (dir, filename string, err error)
    Examples(cfg NamingConfig) map[string]string // /config/naming/examples equivalent
}
type TokenSource interface{ Tokens() map[string]TokenValue } // "Movie CleanTitle" -> value

// pkg/release
func Parse(title string) ParsedRelease            // wraps moistari/rls + quality mapping
func Categories(mediaType string) []int32         // newznab ids to request

// internal/indexarr: one implementation per Indexer.spec.implementation
type Indexer interface {
    Caps(ctx) (Capabilities, error)
    RSS(ctx) ([]Release, error)                       // t=search without q
    Search(ctx, q SearchQuery) ([]Release, error)     // typed: MovieQuery{ImdbID,TmdbID,Text,Year} | TVQuery{TvdbID,Season,Episode,AbsoluteEpisode,AirDate} | MusicQuery{Artist,Album} | BookQuery{Author,Title}
    Download(ctx, r Release) (payload []byte, kind string, err error) // .torrent/.nzb bytes or magnet
}

// internal/grabarr: one implementation per DownloadClient.spec.type
type DownloadClient interface {
    Protocol() Protocol
    Add(ctx, r Release, category string, opts AddOptions) (downloadID string, err error)
    Items(ctx) ([]ClientItem, error) // {DownloadID, Title, Category, Status(Queued|Paused|Downloading|Completed|Failed|Warning), OutputPath, TotalBytes, RemainingBytes, ETA, CanMoveFiles, CanBeRemoved, SeedRatio, SeedTime, Message}
    Remove(ctx, downloadID string, deleteData bool) error
    Test(ctx) error
}

// pkg/decision
type Specification interface {
    Name() string
    Type() RejectionType // Permanent|Temporary
    Evaluate(ctx, d *Candidate) *Rejection // nil = accept
}
```

---

# Part D: Go libraries (versions verified with `go list -m -versions ... | tail -1` on 2026-09-18)

| Module | Version | Purpose | Notes |
|---|---|---|---|
| sigs.k8s.io/controller-runtime | v0.25.1 | controllers, webhooks, envtest | pair with `sigs.k8s.io/controller-runtime/tools/setup-envtest` v0.25.1 |
| sigs.k8s.io/controller-tools | v0.22.0 | controller-gen (CRDs, RBAC, deepcopy) | |
| sigs.k8s.io/kubebuilder/v4 | v4.16.0 | scaffolding, `edit --multigroup=true` | |
| k8s.io/api, k8s.io/apimachinery, k8s.io/client-go | v0.37.0 stable (newest tag v0.38.0-alpha.0) | types, clients | use the version controller-runtime v0.25.1 pins |
| github.com/moistari/rls | v0.6.0 | release-name parsing (movies/TV/music/books) | `rls.Release` fields verified via go doc |
| github.com/razsteinmetz/go-ptn | v1.0.0 | fallback PTN-style parser | simpler than rls |
| golift.io/starr | v1.4.1 | typed Go models of Radarr/Sonarr/Lidarr/Readarr/Prowlarr APIs | reference for JSON contracts and compat tests (MIT); `radarr.Release`, `radarr.QueueRecord` verified |
| github.com/devopsarr/{radarr,sonarr,prowlarr,lidarr,readarr}-go | v1.2.1 / v1.1.1 / v1.2.1 / v1.2.1 / v1.2.1 | OpenAPI-generated *arr clients | alternative contract reference |
| github.com/gosimple/slug | v1.15.0 | `titleSlug` generation | |
| golang.org/x/text | v0.42.0 | unicode normalisation for CleanTitle | |
| github.com/mrobinsn/go-newznab | v1.2.0 | Newznab/Torznab client | old; likely write our own on top of `mmcdole/gofeed` v1.4.2 |
| github.com/anacrolix/torrent | v1.61.0 | embedded torrent client | per brief |
| github.com/Tensai75/nzbparser | v0.1.0 | NZB XML parsing | usenet |
| github.com/javi11/nntppool | v1.5.5 | NNTP connection pool | usenet |
| github.com/autobrr/go-qbittorrent, github.com/hekmon/transmissionrpc/v3, github.com/autobrr/go-deluge, github.com/autobrr/go-rtorrent, golift.io/nzbget | v1.18.0, v3.0.0, v1.4.0, v1.12.0, v0.1.6 | external DownloadClient adapters | |
| golift.io/xtractr, github.com/nwaples/rardecode/v2, github.com/bodgit/sevenzip | v0.6.1, v2.4.1, v1.6.5 | archive extraction on import (Unpackerr uses xtractr) | |
| gopkg.in/vansante/go-ffprobe.v2 | v2.3.1 | ffprobe JSON -> MediaInfo tokens | ffmpeg 9.0.1 on the dev box |
| github.com/u2takey/ffmpeg-go | v0.5.0 | ffmpeg command builder | |
| github.com/asticode/go-astisub | v0.45.0 | subtitle parse/convert (srt/ssa/vtt/ttml) | |
| github.com/cyruzin/golang-tmdb | v1.9.4 | TMDB client | TVDB/MusicBrainz/OpenLibrary: no maintained Go clients found; write thin ones |
| github.com/nats-io/nats.go / nats-server/v2 | v1.53.1 / v2.15.0 | events, JetStream work queues | server for embedded tests |
| github.com/cloudevents/sdk-go/v2 | v2.16.2 | event envelope | |
| github.com/ThreeDotsLabs/watermill (+ watermill-nats/v2) | v1.5.3 (+ v2.2.0) | pub/sub abstraction | optional |
| github.com/redis/go-redis/v9, github.com/hibiken/asynq, github.com/riverqueue/river | v9.23.0-beta.1, v0.26.0, v0.47.0 | queue-track alternatives | for the queue research track |
| github.com/oapi-codegen/oapi-codegen/v2 (+ runtime v1.7.0), github.com/danielgtaylor/huma/v2, github.com/go-chi/chi/v5 | v2.8.0, v2.39.1, v5.3.2 | REST facade + OpenAPI | do **not** generate server stubs from the GPL `openapi.json`; hand-write the compat schema |
| github.com/onsi/ginkgo/v2, github.com/onsi/gomega, github.com/stretchr/testify | v2.33.0, v1.43.1, v1.12.1 | tests | |

Unresolved on the proxy: `github.com/opensubtitles/opensubtitles-go`, `github.com/yenc/yenc`, `github.com/pioz/tvdb` (no tagged versions).

---

# Sources

- Radarr/Sonarr/Prowlarr/Lidarr/Readarr OpenAPI: https://raw.githubusercontent.com/Radarr/Radarr/develop/src/Radarr.Api.V3/openapi.json , .../Sonarr/Sonarr/develop/src/Sonarr.Api.V3/openapi.json , .../Prowlarr/Prowlarr/develop/src/Prowlarr.Api.V1/openapi.json , .../Lidarr/Lidarr/develop/src/Lidarr.Api.V1/openapi.json , .../Readarr/Readarr/develop/src/Readarr.Api.V1/openapi.json (parsed locally)
- Radarr Quality table: https://raw.githubusercontent.com/Radarr/Radarr/develop/src/NzbDrone.Core/Qualities/Quality.cs
- Newznab categories: https://raw.githubusercontent.com/Prowlarr/Prowlarr/develop/src/NzbDrone.Core/Indexers/NewznabStandardCategory.cs
- Servarr wiki source: https://raw.githubusercontent.com/Servarr/Wiki/master/{radarr,sonarr,lidarr,readarr}/settings.md , radarr/faq.md
- TRaSH: https://trash-guides.info/File-and-Folder-Structure/Hardlinks-and-Instant-Moves/ , https://trash-guides.info/Radarr/Radarr-recommended-naming-scheme/ , https://trash-guides.info/Sonarr/Sonarr-recommended-naming-scheme/
- Jellyfin: https://jellyfin.org/docs/general/server/media/{movies,shows,music,books}/
- Plex (secondary; support.plex.tv returns 403): https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/ , .../naming-and-organizing-your-tv-show-files/
- Audiobookshelf: https://audiobookshelf.org/docs/documentation/libraries/book-library/directory-structure/
- Kavita: https://wiki.kavitareader.com/guides/scanner/manga/ ; Komga: https://komga.org/docs/guides/libraries/
- Torznab spec: https://torznab.github.io/spec-1.3-draft/torznab/Specification-v1.3.html
- Bazarr: https://raw.githubusercontent.com/morpheus65535/bazarr/master/custom_libs/subliminal_patch/score.py , .../bazarr/app/config.py , https://wiki.bazarr.media/Additional-Configuration/Settings/
- Kubernetes API conventions: https://raw.githubusercontent.com/kubernetes/community/master/contributors/devel/sig-architecture/api-conventions.md ; kubebuilder multigroup: https://book.kubebuilder.io/migration/multi-group
- DeepWiki Q&A over Radarr/Radarr, Sonarr/Sonarr, Prowlarr/Prowlarr, Lidarr/Lidarr, Readarr/Readarr, morpheus65535/bazarr
- GitHub name survey: `gh api search/repositories?q=<name>+in:name` (2026-09-18); org page https://github.com/clustarr
- RDAP: https://rdap.org/domain/clustarr.{io,dev,app,com}
- Go versions: `go list -m -versions` against the module proxy; `go doc github.com/moistari/rls Release`; `go doc golift.io/starr/radarr Release`
