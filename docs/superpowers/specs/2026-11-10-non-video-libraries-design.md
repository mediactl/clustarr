# Music, books, comics and audiobooks: parity with movies and TV

**Date:** 2026-11-10. **Status:** design of record for the four non-video
libraries; supersedes the "Comic and Audiobook cards sit on the Books tab"
decision of `2026-09-23-library-page-design.md` (§Decisions) and amends the
design of record's §4.2 for the seven non-video kinds and §D (the Plex
provider). Where this document and an earlier one disagree, this one wins.

**Owner's brief (2026-11-10):** "Begin implementing clustarr's music, books,
comics and audiobooks libraries just like we did the movies and TV. The
concepts are the same with differences in metadata sources, issue to series
to author hierarchy, etc. Ensure all UI elements match the other libraries
as closely as possible, customizing based on the current flavor (category).
Implement custom metadata providers for music and audiobooks like you do
movies and tv, with an additional caveat that music custom metadata is in
its infancy. Audiobooks will use a variant of the music metadata provider.
Latest Plex change to support this: NFO File Support: Plex can read artist
and album metadata from NFO files placed inside your music library
directories."

## 0. What is true today (verified 2026-11-10)

**In the code.** All seven kinds -- Artist, Album, Author, Book, Audiobook,
Comic, Issue -- are CRDs with deepcopy, apply configurations and bases;
Album, Book, Audiobook and Issue are remediation-loop leaf keys and Comic a
container key, riding the same staged pass as Movie, Episode and Series
(grab, downloads, search, metadata, artwork). Artist and Author are the
only item kinds outside the loop: standalone controller-runtime
controllers (`app/catalog/manager/register.go:369-391`) that fan children
out by RPC, publish their metadata task straight to the bus with no
dispatch-ledger admission, and incorporate records themselves through
`metadataset.Incorporator`. The search agent, grab planner, lifecycle,
wanted cron and BacklogSearch cover the six searchable kinds and six grab
owners. Metadata clients exist and are fixture-tested for MusicBrainz,
Cover Art Archive, fanart.tv, Open Library, Hardcover, Audnexus,
ComicVine, Metron, MangaDex, AniList, Kitsu and Anime-Lists. Import
attributes non-video files by their extension (`importplan.nonVideoKind`),
resolves an album's folder from the artist's and attributes a track only
for a one-track album (`fileimport.resolveAlbum`); naming has
`TrackFile`, `BookFile`, `AudiobookFile` and `IssueFile` presets; quality
has the `music`, `book`, `audiobook` and `comic` ladders and the built-in
`music-standard`, `music-lossless`, `ebook`, `audiobook` and `comic`
profiles. The ui has `music` and `books` tabs, cards for Artist, Author,
Comic, Audiobook and a standalone Book, an Author page with Sonarr's books
table, book modal and interactive search, and an Artist page with a plain
album list; Comic and Audiobook fall through to the movie-shaped detail
with no case of their own. The Plex provider serves `/plex/movies` and
`/plex/tv` only. The §3 parity tables record every gap by file and line.

**At Plex.** Plex's Custom Metadata Provider protocol admits only Movie and
TV libraries: the 2025-12-09 announcement says "Currently only Movie and TV
Libraries are supported. Music libraries will come later but require a bit
more work to get there"
(forums.plex.tv/t/announcement-custom-metadata-providers/934384, post 1);
the developer docs' custom-provider type table lists movie 1, show 2,
season 3, episode 4 and say "music libraries are currently not supported"
(developer.plex.tv/pms, Metadata Providers); a user converting the Audnexus
audiobook agent on 2026-04-22 got "The provider supports unsupported
metadata types" (post 65), the same refusal PMS 1.43.4 gave clustarr's
type-18 declaration on 2026-10-06. Plex's own type table (developer.plex.tv
"Types") numbers `artist` 8, `album` 9 and `track` 10, and its match hints
name "artists, and albums" and "the album number" for tracks. No evidence of
music support for custom providers exists through the thread's last post
(2026-10-29). **This is the "infancy" the owner names: the protocol's music
shapes are unpublished, so §7 builds them from Plex's own type numbers and
its music items' shapes, marked inferred, behind a flag, and begins with a
registration spike.**

**Music NFO reading shipped.** PMS 1.43.5.11029 (Plex Pass beta, 2026-10-06;
also 1.43.5.11079): "NEW: (Metadata) Added support for reading artist and
album metadata from NFO files in music libraries (PM-6153)". The agent
follows Kodi's `artist.nfo` and `album.nfo` ("following the Kodi examples
will get you started: kodi.wiki/view/NFO_files/Templates", drzoidberg33,
forums.plex.tv/t/plex-nfo-agent-forum-preview/936104 post 530). Observed in
that thread (posts 531-553): the album's title, `<releasedate>`, `<review>`
(Plex's album description) and the artist's `<biography>` are read; a
Jellyfin-style `<artist>`/`<albumartist>` element is not (Kodi names the
album artist in `<albumArtistCredits>`); "I'll add those artist fields as
fallbacks before this goes to a public release"; MusicBrainz ids,
`<type>` and multiple artists were asked about and not answered. The
support article (support.plex.tv/articles/using-nfo-metadata-files-with-plex)
still says "Music: NFO support for music libraries is not currently
available" and documents movie and TV elements only; the NFO agent "will
not work with Syncing Watch State and Ratings"; it is combined with other
providers through a Custom Metadata Agent (Settings > Agents) whose primary
provider matches. The talos PMS runs 1.43.5.11029 (since 2026-10-22), so the
reader is live there. Plex has no audiobook or book library type: audiobooks
live in Music libraries (author as artist, book as album, the legacy
Audnexus agent's convention), and books and comics have no Plex library at
all.

## 1. Goal and scope

Bring the four libraries to the movies' and TV's standard, flavour by
flavour:

| Flavour | Container | Leaf | Unit of a file | Metadata | Plex |
| --- | --- | --- | --- | --- | --- |
| Music | Artist | Album | a track (one MediaFile per track, `mediaRef.track`) | MusicBrainz, Cover Art Archive, fanart.tv, Wikipedia (biographies, §5.4) | `/plex/music` root (flagged, §7) and `artist.nfo`/`album.nfo` (§8) |
| Books | Author | Book | the book (one MediaFile) | Open Library, Hardcover | none (no Plex library type) |
| Comics | Comic (a volume or manga) | Issue | the issue (one MediaFile) | ComicVine, Metron, MangaDex, AniList, Kitsu | none |
| Audiobooks | Author (optional) | Audiobook | a part (ordered MediaFiles) | Audnexus, Audible catalog (search, §5.6), Open Library through the Book link | `/plex/audiobooks` root (flagged, §7) and `artist.nfo`/`album.nfo` written as author/book (§8) |

In scope: the CRD parity of §4, the manager's parity of §5 (loop keys,
rollups, monitoring, phases, naming, events), the metadata parity of §5.4,
import and probe of §6, the ui of §9, the Plex roots of §7, the NFO writer
of §8, and the cluster-plex changes they need, listed in §10 for that repo.
Out of scope: a music transcode (an album's files are never transcoded,
subtitled or marked; the file-kind guard of §4.8 makes that explicit),
MediUX image sets and rating overlays for non-video kinds (MediUX has no
music, books or comics), Plex collections for music (Plex's provider docs:
collections in movie libraries only), lyrics, ReplayGain and Plex's sonic
analysis.

## 2. Decisions

- **D1. Four tabs.** The Library sub-menu reads Movies, TV, Music, Books,
  Comics, Audiobooks (`projection.Tab` gains `comics` and `audiobooks`;
  `Tabs()` is the sidebar's order). A Comic is carded on Comics, an
  Audiobook on Audiobooks, an Author and a standalone Book on Books, an
  Artist on Music. Each tab's Rescan covers its own RootFolder kinds
  (`RootFolderKinds`), its Add New adds its own kind (§9.6), and
  `addLabel`/`findNoun`/`tabLabel` name it. Supersedes
  `2026-09-23-library-page-design.md`'s "Comic and Audiobook cards sit on
  the Books tab".
- **D2. Artist and Author are remediation-loop item keys**, container kinds
  beside Series and Comic (`remediation.KindArtist`, `KindAuthor`): their
  metadata and artwork run as the loop's stages under the dispatch ledger,
  their records are incorporated in the loop's one apply, and the standalone
  controllers and `metadataset.Incorporator`'s artist/author path are
  deleted. The child fan-out (Artist->Album, Author->Book) becomes the
  container pass's child stage, as `series.episodeSync` is, with a sync memo
  (`childSyncMemo`: refetch only when spec, metadata refresh or add options
  moved since the last complete fan-out) -- Comic's `syncIssues` takes the
  same memo. A folder scan follows a container's first fan-out
  (`folderscan.Probe`, as the Series gets).
- **D3. Monitoring cascades from the container.** An Album, Book or Issue
  is searched and grabbed automatically only when it and its Artist, Author
  or Comic are monitored (`itemread.Monitored` consults the container for
  Album and Book as it does for Episode and Issue; the backlog's container
  check too), which is what the RSS matcher already does. The container's
  monitor toggle does not rewrite its children's flags (Lidarr's and
  Readarr's semantics; `monitorNewItems` and the add-time `monitor` mode
  cover new children).
- **D4. One leaf phase vocabulary and `cutoffReason`.** Album, Book,
  Audiobook and Issue share `LeafPhase`: `Pending` (no metadata yet),
  `Unavailable` (not released yet), `Wanted`, `Delayed`, `Downloading`,
  `Imported`, `CutoffUnmet`, `CutoffUnevaluated`, `Unmonitored`, with
  Movie's precedence (Downloading, Unmonitored, Pending, Imported, Delayed,
  CutoffUnevaluated, CutoffUnmet, Unavailable, Wanted). Issue's Mylar
  `state` enum is replaced by `phase` (cold turkey; nothing else reads it;
  `archived`, `ignored` and `failed` were never produced). Every leaf gains
  `status.cutoffReason` (Pending, ProfileUnresolved, CutoffMet, CutoffUnmet,
  TracksMissing) and drops the constant HasFile/CutoffMet conditions as
  Episode did on 2026-11-03; `Ready` and `DeadLettered` stay.
- **D5. One file vocabulary on every leaf**: `hasFile`, `fileRef` (Book,
  Issue), `fileRefs` (Audiobook, ordered by `partIndex`), `tracks[].fileRef`
  (Album, every attributed track), `fileQuality *Quality`,
  `fileFormatScore int32`, `fileFormat string` (Book, Issue: the extension
  as the quality ladder names it). Album's `quality`/`formatScore` are
  renamed to `fileQuality`/`fileFormatScore` (the lowest-ranked track's);
  Audiobook's `quality` likewise; Book gains both; Issue gains the score.
  Printer columns follow.
- **D6. Album tracks are attributed one file each.** The importer attributes
  every audio file to a track of the selected release by (disc, number)
  from the probe's tags, else the file name's `NN - Title` or `D-NN`, else
  the tags' MusicBrainz recording id (`mediaRef.track`); a file matching no
  track, or a second file for one track, is `needsPerson` ("track N not on
  the selected release"), never guessed. `AlbumStatus.trackFileCount`
  counts attributed tracks, `hasFile` is true when any track has a file,
  and Lidarr's clause holds: an album with a track missing is
  `CutoffUnmet`, reason `TracksMissing`, so it is searched again. This closes
  the remaining-work item "album track attribution needs an API ruling".
- **D7. Naming and rename are kind-neutral.** The naming planner and rename
  actuator (`app/remediation/naming`, `app/remediation/rename`,
  `mediafile.namingOwnerOf`) cover Album tracks, Books, Audiobook parts and
  Issues through the presets that already exist, under
  `RootFolder.naming.renameFiles` as video files are.
- **D8. Metadata documents gain what Movie and Series have** where the
  sources carry it (§4, §5.4): `schemaVersion` on all six (so a learned
  field re-refreshes once), `language`, `ratings` (RatingSource gains
  `musicbrainz`, `audnexus`, `hardcover`, `anilist`, `comicvine`), genres and
  externalIDs on Album, `label`, Artist life-span and country, Author born
  and died, Book subtitle and languages, Audiobook abridged, isbn and
  chapters, Comic volumeNumber, genres, status and alternate titles, Issue
  overview, cover and store date. Images are chosen per (type, language) as
  Movie's are. Artist and album overviews come from MusicBrainz's
  annotation, and an artist's biography from Wikipedia through the
  MusicBrainz url-rel (§5.4), since Plex's artist page and the NFO's
  `<biography>` want one.
- **D9. The probe records audio and page facts.** `MediaInfo` gains
  `sampleRateHz`, `bitDepth` (audio), `pageCount` (comics: the images in the
  archive; PDF: its page count; ebooks: none) and `tags` (title, artist,
  albumArtist, album, trackNumber, discNumber, date, musicBrainz recording
  and release ids, from the container's format tags), bounded as every
  MediaInfo string is; `ProbeVersion` rises so every file is probed once.
  The transcode, subtitles, graft and markers blocks and their printer
  columns are video-only by a kind guard (`MediaFile.VideoKind`), and the
  planners read it rather than each testing the kind.
- **D10. Comics.** Comic gains `addOptions{monitor: all|future|missing|
  existing|none, searchForMissing}` and `addOptionsApplied`; `spec.kind:
  manga` selects MangaDex's chapter numbering and sets
  `status.metadata.manga` when the provider says nothing;
  `spec.specialVersion` is a naming token (`{Special Version}`) and, for
  `volumeAsIssue`, numbers Issues by volume (Mylar's rule), nothing more.
  Issues get Episodes' pack rules: `grabplan`'s coverage rules and
  `lifecycle.supersede` filter on `Ref.Kind` Episode or Issue, a whole
  volume ranks as a whole season does (`rankscore.WholeSeason` for a Comic),
  and a Comic with two or more issues missing is asked for as a volume
  pack first (`searchplan.PackFirst` for Comic, one query per volume, the
  issues waiting for it as episodes wait for a season) -- the Series' season
  machinery with the season layer removed. `nextPullDate` is written from the
  next issue's store date.
- **D11. Audiobooks.** Audiobook stays an ASIN-keyed kind. It gains
  `addOptions{searchForMissing}`, `spec.authorRef` (optional; set from the
  Book's author when `bookRef` resolves, else by Add New when the Audible
  author matches an Author by name) for the Plex root's artist level and
  the ui's author link, `status.partCount`, and `fileRefs` ordered by the
  parts' probed `trackNumber`, else the name's part number, else path. The
  gateway fetches Audnexus chapters into `status.metadata.chapters`. Add
  New searches Audible's catalog (`api.audible.com/1.0/catalog/products`,
  keyless, per region) and adds by ASIN (§5.6).
- **D12. Plex music and audiobook roots, flagged.** `ui/plex` gains
  `/plex/music` (`tv.plex.agents.custom.clustarr.music`, types 8, 9, 10)
  and `/plex/audiobooks` (`...clustarr.audiobooks`, types 8, 9, 10), with
  match, metadata, children, grandchildren and images for artist, album and
  track, built as §7 describes and served only under
  `--plex-music-provider` (chart `ui.plex.musicProvider`, default false,
  and the chart's `plex.metadataProviders` carries their uris commented out
  with the reason). The first task is a registration spike against a local
  PMS container; the roots stay off on talos until a PMS build accepts a
  provider declaring 8, 9 and 10.
- **D13. The import agent writes `artist.nfo` and `album.nfo`** (Kodi's
  shape, §8) for every Artist and Album, and for every Author and Audiobook
  under an audiobook root, beside the files, with the cover as `folder.jpg`
  and the artist image as `artist.jpg`, planned by the item key's `nfo`
  stage and written through an `import-nfo` task whose answer is the
  `clustarr-nfo` record; `status.nfo{hash, writtenAt}` under `catalog`. The
  manager renders every byte; the agent writes them. `RootFolder.naming.
  writeNFO` (default true for music and audiobook roots) turns it off.
- **D14. Events, SkipInitial, memos.** Every non-video reconcile emits
  Kubernetes Events on phase and condition edges as Movie does; the Album,
  Book, Audiobook and Issue watches skip idle items at start
  (`rollup.Watch.SkipInitial`: unmonitored or imported with nothing in
  flight); Comic, Artist and Author stop refetching their child lists every
  pass (D2's memo).
- **D15. Cold turkey.** As with every change since 2026-10-07: no
  migration code, no legacy names; a renamed status field (D4, D5) is a CRD
  change applied by hand on talos before the deploy
  (`kubectl apply --server-side --field-manager=clustarr-deploy -f
  config/crd/bases/`), and the old field's values are rebuilt by the next
  reconcile. The chart's values are edited with the deploy.

## 3. Parity tables (what movies and TV have, per flavour, before this design)

Legend: Y built, P partial, N missing. Lines name the as-built code.

### 3.1 CRDs and manager

| Element | Movie/Series/Episode | Artist/Album | Author/Book | Comic/Issue | Audiobook |
| --- | --- | --- | --- | --- | --- |
| Loop item key | Y | P (Album only; Artist standalone, `register.go:369-391`) | P (Book only) | Y (Comic container, Issue leaf) | Y |
| Container rollup (phase, missing/downloading counts, next date) | Y (`series/rollup.go:63-130`) | N (albumCount/albumFileCount only) | N | N (`issueFileCount`, `downloadingIssueCount`; `nextPullDate` never written) | n/a |
| Monitoring gate from container | Y (`itemread.Monitored` Episode) | N (RSS yes, planner no) | N | Y | n/a |
| Leaf phases Pending/Unavailable/CutoffUnevaluated | Y | N (six values) | N | N (Mylar `state`) | N |
| `cutoffReason`, retired constant conditions | Y (Episode 2026-11-03) | N | N | N | N |
| Downloads/entries/searchDispatch/lastLiveSearchAt | Y | Y | Y | Y | Y |
| Pack rules (coverage, supersede, whole-season rank, pack-first search) | Y (Series) | n/a | n/a | P (`DownloadEntry.Pack` counts issues; the rest Episode-only, `coverage.go:168-292`, `supersede.go:42-48`) | n/a |
| Naming planner / rename actuator | Y | N (`naming/adapter.go:57-60`, `rename/actuator.go:267-291`) | N | N | N |
| Events on phase edges | Y (`movie/report.go`) | N | N | N | N |
| SkipInitial at start | Y (Episode) | N | N | N | N |
| Child-list sync memo | Y (`series/episodesync.go`) | N (RPC every pass) | N | N (`comic/reconciler.go:435-492`) | n/a |
| Folder scan after fan-out | Y (Series) | N | N | N | n/a |
| Add options (monitor mode, search on add) | Y | Y | Y | N (no AddOptions) | N |

### 3.2 Metadata

| Element | Movie/Series | Artist | Album | Author | Book | Audiobook | Comic/Issue |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `schemaVersion` outdated refresh | Y | N | N | N | N | N | N |
| `language` on the document | Y | N | N | N | N | N | N |
| Ratings | Y | N (MB computed, dropped) | N | N | N (Hardcover dropped) | N (Audnexus dropped) | N (AniList dropped) |
| Genres / externalIDs | Y | Y / Y | N / N | Y / Y | Y / Y | Y / Y | N / Y |
| Overview | Y | N (no annotation, no biography source) | N | Y | Y | Y | Y / N (Issue: title, date only) |
| Images chosen per (type, language) | Y | N (first 50) | N | N | N | N | N |
| Poster exists | Y | N (fanart maps artistthumb to thumb) | Y (CAA) | Y (OL) | Y (edition) | Y | P (MangaDex only; ComicVine volume requests no image) |
| People / extended document | Y | N | N | N | N | N (narrators are strings) | N |
| Plex ids | Y | N | N | N | N | N | N |
| Child listing complete | Y | N (one page of 25 release groups) | n/a | Y | n/a | n/a | N (ComicVine one page) |
| Add New search | Y | Y | n/a | Y | N (standalone) | N | N |

### 3.3 Import, probe, search

| Element | Video | Music | Books | Comics | Audiobooks |
| --- | --- | --- | --- | --- | --- |
| Rescan attribution to existing items | Y | Y (`rescan/nonvideo.go`) | Y | Y | Y |
| Download import | Y | P (`resolveAlbum`: one-track attribution only) | Y | Y | Y (parts by path order) |
| Probe fields the ui and Plex need | Y | N (no tags, sample rate, bit depth) | n/a | N (no page count) | N (no tags) |
| Frozen quality on the MediaFile | Y | Y | P (`book/filestate.go:36-44` falls back to the extension) | Y | Y |
| Search identity rules | Y | Y (`search/nonvideo.go`) | Y | Y | Y |
| Blocks that must not run (transcode, subtitles, markers) | run | guarded case by case | same | same | same |

### 3.4 UI

| Element | Movie | Series/Episode | Artist/Album | Author/Book | Comic/Issue | Audiobook |
| --- | --- | --- | --- | --- | --- | --- |
| Tab | Y | Y | Y (Music) | Y (Books) | N (on Books) | N (on Books) |
| Card: poster, monitor, profile, stripe, year, overlay | Y | Y | P (no year; no rollup stripe) | P | P (`itemDetail` no case) | P |
| Hover actions, bulk editor, A-Z, typeahead | Y | Y | Y (typeahead id prefix mbid) | Y (olid) | P (no id prefix) | P |
| Add New | Y | Y | Y (Artist) | Y (Author) | N | N |
| Item page: hero, facts, provider logos, action bar | Y | Y | P (`ParentDetail`: path, overview, genres; no links) | P | N (poster, title, status, profile only) | N |
| Interactive search on the item page | Y | n/a (season/episode) | N | N (Author n/a; standalone Book: route, no button) | N | N |
| Children table with status cells and two searches | n/a | Y (`series.templ`) | N (plain list, `children.templ:107-161`) | Y-minus (`book.templ`, no file cells) | N (404) | n/a |
| Child page (episode page analogue) | n/a | Y (`episode_page.go`) | N (Album 404) | P (modal) | N | n/a |
| Interactive search modal with banded results and grab | Y | Y | N | Y (Book) | N | N |
| File table with per-file cells | Y | Y (languages, score, codec, transcode, subtitles, markers) | N | P (video-centric rows) | N | N (fileRefs unread) |
| Edit dialog with kind fields | Y | Y | P (profile, monitored) | P (standalone Book refused) | P | P |
| Delete dialog listing files | Y | Y | N (no files listed) | N | N | N |
| Rename preview, Refresh & Scan | Y | Y | Y | Y | N (`hasFolder` false) | N |
| Children listed through a field index | n/a | Y | N (namespace List) | N | N | n/a |
| Downloads/Dashboard owner links resolve | Y | Y | N (Album 404) | N (author's Book 404) | N | Y |
| Plex provider root | Y | Y | N | n/a | n/a | N |

## 4. CRD changes (`api/catalog/v1alpha1`, `api/common/v1alpha1`)

Every field below is additive unless marked **rename** or **remove**. Lists
keep or gain `MaxItems`; no floats; defaults reachable from Go (pointer
plus accessor where the zero means something). `make generate manifests`
and the flatten script follow; `pkg/crdcheck` already walks these packages.

### 4.1 Shared

- `commonv1.LeafPhase` (string enum): `Pending; Unavailable; Wanted;
  Delayed; Downloading; Imported; CutoffUnmet; CutoffUnevaluated;
  Unmonitored`. Album, Book, Audiobook and Issue use it as `status.phase`.
- `commonv1.CutoffReason` gains `TracksMissing` (an Album with a track
  without a file).
- `catalogv1.RatingSource` enum gains `musicbrainz; audnexus; hardcover;
  anilist; comicvine`.
- `catalogv1.ContainerRollup` (embedded in Artist, Author and Comic status):
  `phase ContainerPhase{Ready, Pending, Unmonitored}`, `childCount`,
  `childFileCount`, `missingChildCount` (monitored, released, no file),
  `downloadingChildCount`, `nextReleaseDate *metav1.Time`,
  `previousReleaseDate *metav1.Time`, `cutoffUnmetChildCount`. The kind's
  own named counts (`albumCount`, `bookCount`, `issueFileCount`) stay as
  they are and the rollup is read through accessors, so `kubectl get`'s
  columns keep their names.
- `MetadataDocument` fields on every non-video metadata struct:
  `schemaVersion int32`, `language string`, `ratings []Rating` (MaxItems
  8), `refreshedAt`.
- `commonv1.MediaInfo`: `sampleRateHz int32`, `bitDepth int32` on
  `AudioStream`; `pageCount int32`; `tags *MediaTags{title, artist,
  albumArtist, album, trackNumber int32, trackTotal int32, discNumber
  int32, discTotal int32, date string, musicBrainzRecordingID,
  musicBrainzReleaseID, musicBrainzReleaseGroupID, musicBrainzArtistID
  string}`, each string clipped to `MaxStreamTitle`. `mediainfo.ProbeVersion`
  4.
- `MediaFile`: `func (m *MediaFile) VideoKind() bool` (movie or episode);
  the transcode, subtitles, graft and markers blocks are documented as
  video-only, their printer columns keep `priority=1`, and
  `MediaFileSpec.PartIndex int32` (an audiobook part's or a multi-part
  movie's order; the audiobook rollup reads it).

### 4.2 Artist

- Spec: `addOptions.searchForMissing *bool` default true (as Movie's);
  `transcodeProfileRef` is never added.
- Status: `ContainerRollup` fields; `metadata` gains `schemaVersion,
  language, ratings, begin, end (dates as strings "YYYY[-MM[-DD]]"),
  country, status` (written from MusicBrainz's life-span `ended`),
  `aliases` (MaxItems 32), `links []Link{type, url}` (MaxItems 32: wikidata,
  wikipedia, discogs, official homepage, bandcamp, last.fm, spotify, apple,
  youtube), `biographySource string`; `status.nfo *NFOStatus{hash,
  writtenAt, path, files}`. (As built in W0: no `childSync` field; the
  child-sync memo is in-process, as the Series' `episodeSyncMemo` is.)

### 4.3 Album

- Spec unchanged.
- Status: **rename** `quality`->`fileQuality`, `formatScore`->
  `fileFormatScore`; `hasFile bool`; `phase LeafPhase`; `cutoffReason`;
  `tracks[].fileRef` for every attributed track (already in the type);
  `metadata` gains `schemaVersion, language, ratings, genres (30),
  externalIDs (musicbrainz-release-group, discogs, spotify, barcode, asin),
  label, labels (8), catalogNumber, artistCredit string, overview` (the
  release group's annotation); `releases[]` gains `title, disambiguation,
  catalogNumber`; `status.nfo`.
- Printer columns: Artist, Title, Monitored, Phase, Tracks
  (`trackFileCount/len(tracks)` as a string column `status.trackSummary`),
  Quality (`.status.fileQuality.name`), Download, Age.

### 4.4 Author

- Spec: `addOptions.searchForMissing *bool` default true.
- Status: `ContainerRollup`; `metadata` gains `schemaVersion, language,
  ratings, born, died, links (32)`; `status.nfo` (written only under an
  audiobook root, §8). (As built in W0: no `childSync` field.)

### 4.5 Book

- Spec: CEL `has(self.authorRef) || (has(self.qualityProfileRef) &&
  has(self.rootFolderRef))` (a standalone book names its profile and
  root).
- Status: `phase LeafPhase`; `cutoffReason`; `fileQuality`,
  `fileFormatScore` beside `fileFormat`; `metadata` gains `schemaVersion,
  language, ratings, subtitle (written), languages (16), subjects (50, the
  keywords), pageCount, publisher, links (16)`; `editions[].isEbook`
  written.

### 4.6 Audiobook

- Spec: `authorRef *string`; `addOptions{searchForMissing *bool default
  true}`.
- Status: `phase LeafPhase`; `cutoffReason`; **rename** `quality`->
  `fileQuality`, plus `fileFormatScore`; `partCount int32`;
  `metadata` gains `schemaVersion, fetchLanguage (its `language` is the
  recording's own), ratings, abridged (written),
  isbn (written), chapters (written; MaxItems 200), rating removed in
  favour of ratings, tags (32), isAdult`, `narrators` stays (strings) and
  `narratorRefs` is not added; `status.nfo`; `addOptionsApplied`.

### 4.7 Comic and Issue

- Comic spec: `addOptions{monitor ComicMonitorMode{all, future, missing,
  existing, none} default all; searchForMissing *bool default true}`.
- Comic status: `ContainerRollup` (`nextPullDate` kept and written);
  `metadata` gains `schemaVersion, language, ratings, volumeNumber
  (written), genres (30), status ComicRunStatus{continuing, ended},
  alternateTitles (16), startYear, endYear, originalLanguage, sortTitle,
  countOfIssues`; `volumeSearch *SeasonSearchState` (the Comic's pack-first
  search state: the Series' type reused with the one season that is the
  whole volume, so the season planner serves both); `addOptionsApplied`.
  (As built in W0: no `childSync` field; `countOfIssues` not added, since
  `issueCount` already exists.)
- Issue status: **remove** `state`; `phase LeafPhase`; `cutoffReason`;
  `fileFormat`, `fileFormatScore` beside `fileQuality`; `overview`,
  `coverURL` (the provider's image, through `/art/search`'s signing as
  episode stills are), `storeDate`, `credits []Credit{name, role}`
  (MaxItems 32), `sourceIDs map` (comicvine, metron, mangadex). Printer
  columns Comic, Number, Title, Monitored, Phase, Date, Age.

### 4.8 RootFolder and QualityProfile

- `RootFolder.naming.writeNFO *bool` (default true when `spec.kind` is
  `music` or `audiobook`, otherwise ignored); `naming.renameFiles` is read
  for every kind.
- `RootFolder.defaults.region` (audiobook roots: the Audible region Add New
  searches, default `us`).

## 5. The manager

### 5.1 Item keys and the staged pass

`app/remediation/itemkeys.go` adds `KindArtist` and `KindAuthor` as
container keys. The container pass for an Artist runs: child sync (D2's
memo; the gateway's list verb, the fan-out's monitor rules unchanged,
Albums created spec-only with owner references as today), the metadata and
artwork stages (through `itempass.Admit` and the ledger), the `nfo` stage
(§8), the rollup. Author likewise; Comic gains the memo and the `nfo`
stage runs for none of its kinds. `app/catalog/controller/{artist,author}`
keep their pure functions (fan-out filters, path rules, rollups) and lose
their reconcilers and their bus publication; `register.go` registers
nothing for them; `metadataset.Incorporator` keeps only what another
caller needs or is deleted (`TestOnlyTheLoopIncorporatesRecords`†).

(As built 2026-11-10, W1.1: D2's memo is the subpackage
`app/remediation/childsync` -- `app/remediation` imports the artist, author
and comic packages for their indexes, so they import the memo from a leaf
package -- shared by Artist, Author and Comic; `metadataset.Incorporator`
is deleted; the nfo stage runs for Artist, Album, Author and Audiobook, an
Author's only under an audiobook root, as §8.1 says. D1, 308f974c, the same
day: the Series, Artist and Author flip `status.addOptionsApplied` only with
their first complete fan-out -- the stored value on every early return,
`stored || fan.synced` after -- where fbab16f7 and a625715f had flipped it
on the metadata-less first pass, so every child was created under
`monitorNewItems`; and they key every fan-out under the flag true, since the
memo records only complete fan-outs and a child's own Create wakes the
container before the flip reaches the cache, where a key read as false
refetched to create nothing; the Comic still keys the flag as read.)

### 5.2 Rollups, phases, events

`app/catalog/controller/rollup` gains `LeafPhase(...)` shared by the four
leaves (Movie's precedence) and `Container(...)` for the three containers;
`itemstatus/apply.go` lists eight kinds. Each leaf's `cutoffReason` is
written beside `phase`; the constant conditions are dropped on the first
pass that finds them (`retiredLeafConditions`, as Episode's). Every kind's
reconcile records phase and condition edges through `report.Edges` (Movie's
`report.go` made kind-neutral). `rollup.Watch.SkipInitial` takes
`leaf.idleAtStart` (unmonitored, or imported with no entry in flight).

### 5.3 Monitoring, search and grab

`itemread.Monitored` reads the Artist for an Album and the Author for a
Book; `backlogsearch`'s container check follows; `grabplan.Holds`,
`coverage.go`'s FullSeason/PackDownloading/EpisodeDownloading and
`lifecycle.supersede` take `Ref.Kind` Episode or Issue (`refKind.Child()`);
`rankscore.WholeSeason` applies to Series and Comic; `searchplan.PackFirst`
and `DecideSeasons` are generalised to a container with one "season" (the
volume), read by the Comic container pass and mirrored in
`status.volumeSearch`; the Comic's S24 arm wakes Issues on the
DownloadsSignature as today and on the volume search's answer. A Search CR
on an Artist, Author or Comic still fans out per child (`ContainerKind`),
and a Comic's "Search volume" button asks for the pack (§9.4).

### 5.4 Metadata

In `app/catalog/metadata` and `pkg/metadata`:

- Every non-video builder sets `schemaVersion` (`pkg/metadata.SchemaVersion`
  is one number for every kind; the outdated-refresh path the Movie and
  Series reconcilers run moves into the metadata stage and applies to all
  eight kinds), `language` (the fetch language: `MetadataProvider.spec.
  language`, else `en`), `ratings` through `enrichRatings` with the new
  sources, and images through `selectImages` per (type, language).
- MusicBrainz: `Albums()` pages release groups by offset to the end,
  filtered server-side by the profile's primary types; `artistIncludes` adds
  `annotation`, `url-rels`, `aliases`, `ratings`; `mapArtist` sets begin, end,
  country, status, aliases, links and `Overview` from the annotation;
  `mapAlbum` sets genres, externalIDs, label(s), catalog number, artist
  credit, overview and ratings. The cost is bounded: one request per 25
  release groups at 1 rps, and nothing new per album.
- Biographies: a keyless `wikipedia` client (`pkg/metadata/clients/
  wikipedia`): from the MusicBrainz url-rel to Wikidata (`Q...`), the
  entity's `enwiki` sitelink (`wikidata.org/wiki/Special:EntityData/Q.json`,
  one request), then `en.wikipedia.org/api/rest_v1/page/summary/<title>`
  (one request) -> `Artist.Overview` when the annotation is empty, source
  recorded in `biographySource`. The same path serves an Author whose Open
  Library record names a Wikidata id. No key, 200 rps public limit, cached
  in the gateway's L2 as every provider answer is. **Owner's call (Q1):**
  this is the one new external source the design adds.
- fanart.tv: `artistthumb` maps to `poster` as well as `thumb`, so an
  Artist has a poster; `musicbanner` to `banner`; `hdmusiclogo` to
  `clearLogo`. Cover Art Archive stays the album's poster and gains the
  `back` as `backgroundSquare`-free "disc" where CAA names one.
- Audnexus: `Audiobook()` is followed by `Chapters()` in the gateway (one
  request, cached), `mapAudiobook` sets abridged, isbn (validated), tags,
  isAdult, ratings; `Authors()` by ASIN is used by Add New's author link
  (D11).
- Audible catalog (`pkg/metadata/clients/audible`, keyless):
  `GET https://api.audible.<tld>/1.0/catalog/products?title=&author=&
  num_results=25&response_groups=product_desc,contributors,media,
  product_attrs,series`, per region tld, one limiter per host, body
  capped; it answers Add New's audiobook search (title, authors, narrators,
  cover, ASIN, release) and nothing else. **Owner's call (Q2):** Audible's
  catalog API is public but undocumented; Audnexus has no search.
- ComicVine: `Volume` requests `image`; `Issues()` pages by offset;
  `Issue` fields `description, image, store_date, person_credits` reach the
  Comic's fan-out, which writes Issue `overview, coverURL, storeDate,
  credits` under `catalog-fanout` beside `sourceID, title, date`. Metron and
  MangaDex fill the same where they carry them; MangaDex honours
  `spec.language` for titles and descriptions.
- Open Library and Hardcover: `Book.Subtitle`, `Languages`, `Subjects`,
  `PageCount`, `Publisher`, `Edition.IsEbook` written; Hardcover's rating as
  `ratings`; `spec.hardcoverID` resolves the Hardcover author for the book
  list when set (its `Books()` takes the Hardcover id).
- Extended documents: `writeExtended` writes an Artist document (members
  and similar artists from MusicBrainz `artist-rels` and `url-rels`; empty
  when none) and an Audiobook document (narrators and authors as people),
  read by the Plex roots; nothing else reads them.
- Add New: the metadata search RPC gains `comic` (ComicVine volume search
  and MangaDex manga search, merged, ranked by title then year) and
  `audiobook` (Audible catalog); `AudiobookProvider` gains `Search`.

### 5.5 Naming and rename

`app/remediation/naming/adapter.go` plans a rename for every MediaFile whose
owner kind has a preset: Album (TrackFile under the album folder), Book
(BookFile), Audiobook (AudiobookFile with the part index), Issue
(IssueFile with the Comic's `specialVersion`). `mediafile.namingOwnerOf`
and the rename actuator's `rootFolderRef` resolve the container's root for
each kind (Album through its Artist, Book through its Author or its own
`rootFolderRef`, Issue through its Comic). `renameTranscoded` is video-only
and stays so.

### 5.6 Add New and classification

`ui/actions.AddItem` and `buildItem` learn Comic (source + sourceID from
the search hit: comicvine volume id or mangadex uuid; the Comics root and
profile kinds) and Audiobook (ASIN, region from the root's `defaults.region`,
`authorRef` when an Author's name matches the hit's first author exactly,
`bookRef` when an Open Library ISBN lookup finds the work -- one request,
best effort); `justAdded` knows both; the role gains `create` on comics and
audiobooks (`actions.Grants()`, `ui_role.yaml`, the chart copy).
(As built 2026-11-10, D2 d1d60a8f/97feb025: the name matches through
`release.TitleNorm` and links only when exactly one Author of the name
exists in the namespace; the Audnexus lookup by ASIN -- the ui's third bound
request, `ui.Options.MetadataLookup` -- renders the name when the hit
carries none and runs only after the add's inputs are checked; no `bookRef`,
since Add New resolves no Book and the ISBN lookup is not built.)

## 6. Import and probe (the import agent)

- The native probe (`pkg/mediainfo/native`) maps the container's format tags
  into `MediaInfo.Tags` (title, artist, album_artist, album, track `N/M`,
  disc `N/M`, date, `MUSICBRAINZ_TRACKID` (the recording), `MUSICBRAINZ_
  ALBUMID`, `MUSICBRAINZ_RELEASEGROUPID`, `MUSICBRAINZ_ARTISTID`, with the
  iTunes and ID3 spellings), each audio stream's sample rate and bits per
  raw sample, and for a `.cbz`/`.cbr`/`.cb7`/`.cbt` the count of image
  entries (`archive/zip`, `rardecode`, `7z` listing only, no extraction) and
  for a `.pdf` the page count (a bounded scan for `/Type /Page`), as
  `pageCount`. A file whose probe decodes no video returns no frame error
  (`mediainfo.go:73-77`), which the native suite proves on a FLAC and an
  MP3 fixture under `test/data/mediainfo/` (`TestAnAudioFileProbesWithoutVideo`).
- Track attribution (D6) in `fileimport.resolveAlbum` and the inspect:
  every audio file of an album download is probed (the inspect's `probed`
  path, as hashed-name packs are) and attributed by (disc, number) from the
  tags, else `release.ParseTrackFileName` (`NN - Title`, `D-NN Title`,
  `DNN`), else the recording id tag; the plan carries `InspectedFile.Track`
  and the execute writes `mediaRef.track`; a file for no track or a track
  with two files is a `needsPerson` rejection naming the track. The rescan
  (`rescan/nonvideo.go`) attributes the same way for files already in the
  album folder. The remaining-work item at `remaining-work.md:1268` closes.
- Audiobook parts: `fileRefs` ordered by `MediaFileSpec.partIndex`, which
  the import sets from the tags' track number, else the name's part number
  (`release.PartOf`, as built -- the movie part-set parser, read by
  `fileimport.PartIndexOf`: Part 01, pt1, 01 of 12, CD1, Disc 1), else the
  path's sort order; `partCount` counts them.
- Book quality: `book/filestate.go`'s fallback is removed; the import
  freezes `fileFormat`, `fileQuality` and `fileFormatScore` for a Book as it
  does for an Issue (`importplan`'s nonVideoKind path already judges by
  extension), and the rescan's materialisation does the same.
- `fsops.Classifier` is unchanged; a `.pdf` in a comic root is a comic and
  in a book root a book, by the root's kind (as today).
- The NFO writer (§8) is the import domain's `import-nfo` consumer.

## 7. The Plex music and audiobook roots (`ui/plex`)

Everything in this section that Plex has not published is marked
*inferred*; the shapes come from Plex's own metadata items for music
(what `metadata.provider.plex.tv` answers for an artist, album and track)
and from the video protocol's conventions, and will be corrected by the
spike's findings and later by Plex's documentation.

### 7.1 Roots

| Root | Identifier | Types | Children routes | Collections |
| --- | --- | --- | --- | --- |
| `/plex/music` | `tv.plex.agents.custom.clustarr.music` | 8 artist, 9 album, 10 track (Plex's own type numbers; the custom-provider table publishes none) | `/children` (artist->albums, album->tracks), `/grandchildren` (artist->tracks, paged) | none |
| `/plex/audiobooks` | `tv.plex.agents.custom.clustarr.audiobooks` | 8, 9, 10 | same, Author->Audiobooks->parts | none |

`rootDef` gains `music bool` (the music container routes, as `tv` keys the
tv ones) and the two roots register only when `Options.MusicProvider` is
true (`--plex-music-provider`). The root response, 503 without an external
URL, `match` and `metadata` Features and the headers follow the video
roots.

### 7.2 Keys and GUIDs

| Type | ratingKey | GUID |
| --- | --- | --- |
| artist | the Artist UID; audiobooks: the Author UID, or `a-<16 hex of sha256(authorName)>` when the Audiobook has no `authorRef` | `<identifier>://artist/<key>` |
| album | the Album UID; audiobooks: the Audiobook UID | `<identifier>://album/<key>` |
| track | `<albumUID>-t<recordingMBID>` (a recording may be on several albums); audiobooks: `<audiobookUID>-p<NN>` | `<identifier>://track/<key>` |

`ratingKeyPattern` admits the three grammars; `ParseRatingKey`/`resolveKey`
and `providerType` learn them. `Guid[]` carries `mbid://<id>` for the
artist (MusicBrainz artist id), album (release-group id) and track
(recording id) *(inferred: the scheme Plex's own music items carry)*, and
for audiobooks `asin://<ASIN>` on the album and `isbn://` when known. No
`plex://` GUIDs: the plex client resolves no music ids and `--plex-guids`
is a no-op on these roots (Q5).

### 7.3 Match (`POST /match`)

Request fields as the video roots' `matchRequest` (`type`, `title`,
`parentTitle`, `grandparentTitle`, `year`, `guid`, `index`, `parentIndex`,
`filename`, `manual`). Rules, in order, each answering only what the root
declares:

- type 8: `guid` `mbid://` -> `Index.ByMBID(artist)`; else `filename` ->
  the artist of the album the file belongs to (`matchfile` rule 0 for
  audio: a MediaFile of kind album whose path ends with the name); else
  `title` normalised (`release.TitleNorm` plus MusicBrainz sort-name and
  "The" handling; a disambiguation in parentheses dropped) against every
  Artist's name and aliases; several equal matches answer every candidate
  ranked (manual) or none (automatic), never a guess.
- type 9: `guid` release-group -> `ByMBID(album)`; else `filename`; else
  `parentTitle` (artist) + `title` (album) + `year` within one of the
  release date; the artist resolved first narrows the albums.
- type 10: `filename` first (the one certain rule for a track); else
  `grandparentTitle`/`parentTitle` resolve the album and `parentIndex`
  (disc) + `index` (track) pick the track, else `title` against the
  tracks' titles.
- Audiobooks root: the same with Author/Audiobook/part; `title` + author
  name; `asin://` guids.

The response is the matched items' `Metadata` with `score` as the video
roots answer.

### 7.4 Metadata *(inferred shapes)*

- **artist** (`type: "artist"`): `ratingKey, key, guid, title, titleSort
  (sortName), summary (overview), thumb/art, Image[] (coverPoster from
  the Artist's poster else thumb, background from fanart, clearLogo),
  Genre[], Country[] (metadata.country), Guid[] mbid, Similar[] (the
  extended document), Role[] is not emitted (members are `Similar`-shaped
  by Plex), Children: albums` when `includeChildren`.
- **album** (`type: "album"`): `title, titleSort, summary (overview),
  year, originallyAvailableAt (releaseDate), studio (label), thumb (cover),
  Image[] (coverPoster), Genre[], Guid[] mbid, parentRatingKey/parentKey/
  parentGuid/parentTitle/parentThumb (the artist), index` (none), `Children:
  tracks` when `includeChildren`; `Rating[]` from `ratings`
  (`musicbrainz` as a badge-less rating).
- **track** (`type: "track"`): `title, index (track number), parentIndex
  (disc), duration (ms), originalTitle (the track's artist credit when it
  differs from the album artist), Guid[] mbid recording, parent* (album),
  grandparent* (artist), thumb (the album's cover)`. Tracks without a file
  are listed too (as the tv root lists every Episode), since Plex matches
  the file it has to the track it names (Q4).
- Audiobooks: author as artist (summary = the Author's overview; without
  an Author object, `Metadata.authors[0].name` and no summary), audiobook
  as album (summary = overview, `studio` = publisher, `originallyAvailableAt`
  = releaseDate, `Genre[]`, `Role[]` = narrators as "Narrator", `Writer[]`
  = authors), part as track (`title` = the chapter's title when the parts
  and chapters align one to one, else "Part N"; `duration` from the
  part's probe).
- `/extras` answers an empty list for every music key. Images go through
  `/art` as the video roots do, and `images.go`'s `resolveArtwork` learns
  Artist, Album, Author, Audiobook (a track's are its album's); the
  `/art/search` host allowlist gains `assets.fanart.tv`,
  `coverartarchive.org`'s archive.org redirect targets and Audible's image
  host for `sourced` match results.
- `/grandchildren` for an artist is built lazily per page from the albums'
  tracks (never every track at once).

### 7.5 Index

`ui/projection/index.go` lists Artists, Albums, Authors and Audiobooks,
indexes `ByUID` for them, `albumsByArtist`, `audiobooksByAuthor` (through
`authorRef`), `ByMBID(kind, id)`, `ByASIN`, and the file-name rule for
album and audiobook MediaFiles (`fileOf` already buckets them), each behind
the existing 5 s TTL; the music lists are built only when the roots are on,
so a cluster with the flag off pays nothing.

### 7.6 Spike and rollout

Task one of the Plex wave runs `plexinc/pms-docker:1.43.5.11079` (or the
newest beta tag at the time) locally, unclaimed, with a stub provider
serving the `/plex/music` root response, and records whether `POST
/media/providers/metadata?uri=` accepts a provider declaring types 8, 9 and
10, and if so which match request bodies PMS sends for a two-track FLAC
album placed in a music library bound to that agent (the Plex Music Scanner
on a library of `type: artist`). The findings go into §7's as-built note;
until a PMS build accepts the root, `--plex-music-provider` stays false
everywhere and the chart's `plex.metadataProviders` lists the two uris
commented out. **The spike never touches the talos PMS.**

**As run, 2026-11-10 12:50Z** (`plexinc/pms-docker:beta`, PMS
1.43.5.11079-b3b13e506 built 2026-10-22, unclaimed, host network; a stub
provider on 127.0.0.1:8099 answering the MediaProvider root with match and
metadata features): `POST /media/providers/metadata?uri=` registered the
control root declaring type 1 (HTTP 200, id 7, `online: true`, PMS fetched
the root once) and refused the roots declaring [8, 9, 10], [8], [9], [10]
and [1, 8, 9, 10] alike, each `HTTP 400 The provider supports unsupported
metadata types` after fetching the root. The same PMS lists its own
`tv.plex.agents.music` with MetadataType 8, 9, 10, so the type table knows
them and the refusal is the custom-provider whitelist. So no PMS build
through 1.43.5.11079 accepts a music provider; W5.2 and W5.3 build the
roots to the inferred shapes behind the flag, and the chart's uris stay
commented out until a build accepts the registration (re-run this spike
against each new beta: the stub is `stub.py` in the session's scratchpad,
the recipe above).

## 8. NFO files for Plex's music agent

### 8.1 What is written

For every Artist with a resolved path and every Album with a folder, and
under an audiobook RootFolder for every Author folder and Audiobook folder
(the author is the artist, the audiobook the album):

```
<ArtistFolder>/artist.nfo
<ArtistFolder>/artist.jpg          the artist's poster (else thumb), when one is stored
<ArtistFolder>/<AlbumFolder>/album.nfo
<ArtistFolder>/<AlbumFolder>/folder.jpg   the album's cover, when one is stored
```

`artist.nfo` (Kodi's `<artist>`; the elements Kodi's wiki lists, written
only when the value exists):

```xml
<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>
<artist>
  <name/>                         status.metadata.name
  <musicBrainzArtistID/>          spec.musicBrainzID
  <sortname/>                     metadata.sortName
  <type/>                         metadata.type (Person, Group, ...)
  <disambiguation/>
  <genre/>...                     one per genre
  <style/>...                     none (MusicBrainz has no styles)
  <yearsactive/>                  "begin-end" when either is known
  <born/> <formed/>               begin (Person -> born, else formed)
  <died/> <disbanded/>            end
  <biography/>                    metadata.overview
  <thumb aspect="poster"/>        the poster's source URL (the provider's), when the store has none local
  <album><title/><year/><musicbrainzreleasegroupid/></album>...   every Album of the artist
</artist>
```

`album.nfo` (Kodi's `<album>`, plus the two Jellyfin-style fallbacks Plex's
developer said he would add, so an older or newer reader finds the artist):

```xml
<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>
<album>
  <title/>                        metadata.title
  <musicbrainzalbumid/>           metadata.selectedReleaseID
  <musicbrainzreleasegroupid/>    spec.releaseGroupID
  <artistdesc/>                   the artist's name
  <genre/>...
  <compilation>true|false</compilation>   secondaryTypes has compilation
  <review/>                       metadata.overview (Plex's album description)
  <type/>                         albumType (Album, EP, Single, ...)
  <releasestatus/>                the selected release's status
  <releasedate>YYYY-MM-DD</releasedate>   metadata.releaseDate
  <originalreleasedate/>          the release group's first release date
  <label/>                        metadata.label
  <year/>                         the release date's year
  <rating max="10"/><votes/>      ratings[musicbrainz] when present
  <thumb/>                        the cover's source URL
  <albumArtistCredits><artist/><musicBrainzArtistID/></albumArtistCredits>
  <artist/> <albumartist/>        the fallbacks (post 540)
  <track><disc/><position/><title/><duration>mm:ss</duration><musicBrainzTrackID/></track>...   status.tracks
</album>
```

Audiobooks: `<artist>` from the Author (name, sortName, biography =
overview, `<born>`, `<died>`, thumb) or, without an Author object, the
Audiobook's first author name alone; `<album>` from the Audiobook (title,
review = overview, releasedate, label = publisher, `<type>Audiobook</type>`,
genre, albumArtistCredits = the authors, `<track>` per part with the
chapter title when parts and chapters align, else "Part N", and the probed
duration). The narrators have no Kodi element and are not written (Q6:
`<style>` would be a misuse).

### 8.2 Who writes it

`pkg/nfo` renders both documents from the CRs (pure functions, golden
tests), and `nfo.Hash` is the sha256 of the rendered bytes plus the image
object digests. The item key's `nfo` stage (`app/remediation/nfo`) plans a
write when `status.nfo.hash` differs from the rendered hash and the folder
exists (`status.path` resolved), publishes an `NFOWriteTask{kind, ns, name,
folder, files: [{name, bytes}], images: [{name, object}]}` on the import
domain's `CLUSTARR_WORK_IMPORT` (`import-nfo` durable, one delivery at a
time per folder, `HandlerTimeout` set) through the dispatch ledger, and the
import agent writes each file with `fsops.AtomicWrite` (mode 0644 under the
root's UMASK), fetches each image from the `clustarr-artwork` object store
read-only (it already holds a bus) and writes it beside the NFO, never over
a file it did not write before (an operator's own `folder.jpg` is kept:
the first write records the file names in `status.nfo.files`, and only those
are rewritten), and answers in the `clustarr-nfo` record (`records.Answerer`,
compare-and-swap); the loop writes `status.nfo` under `catalog-nfo`, the
stage's own field manager, as `catalog-imageset` is `status.imageSet`'s (as
built 2026-11-10: a stage-owned field keeps its own manager so a release of
it touches no other manager's field). A rename of
the folder (§5.5) re-plans the write at the new path and the old files go
with the recycled folder. `RootFolder.naming.writeNFO: false` plans nothing
and removes nothing. The manager writes no file (ADR-0019) and the agent
decides nothing: it writes the bytes it was handed.

### 8.3 Plex side (cluster-plex, §10)

A music library in Plex uses Plex's own agent with NFO reading, not a
clustarr provider, until §7's roots are accepted: cluster-plex's provisioner
creates `type: artist` sections with the "Plex Music Scanner" and the agent
Plex names for NFO music (the provider ids PMS lists at
`GET /media/providers/metadata`; to be captured read-only from the talos PMS
before the change), `Prefer local metadata` on, sonic analysis off (already
forced). Whether Plex's music agent reads `musicbrainz*` ids from the NFO is
unknown (post 548 unanswered); the files are written either way.

## 9. The web UI

Every page is the shadcn-templ component set the rest of the ui uses
(`.claude/skills/shadcn-templ/SKILL.md`), templ formatted with the pinned
`go run github.com/a-h/templ/cmd/templ@v0.3.1020 fmt <file>`, modals as
DefaultOpen dialogs swapped into a page-level `#<kind>-modal` region
(portals.js), self-polling panels on plain `outerHTML` swaps, live lists
morphing, and each new child listing through a field index registered in
`ui/reader.go` (`albumArtistRef`, `bookAuthorRef`, `issueComicRef`,
`audiobookAuthorRef`) -- never a namespace List filtered in memory.

**As built (2026-11-10, wave B: W4a 05d74595/d178c8dd/3f86929f, W4b.1
28c48ef3, W4b.2 03de5c63, W4b.3 65e1e2e0/d60aa8ea, W4b.4 4a8ede4c, W4c
63c57845/21e30255/b51e98e1/58c490fb; the controlling session's 819dc23f):**
§9.1-§9.6 page by page, with these departures, each a wave C task unless
marked owner: the album, audiobook and issue search modals poll and grab
through routes of their own (`ui/album_search.go`,
`/library/{ns}/audiobook/{name}/search/panel/...`,
`/library/{ns}/issue/{name}/search/...`), since `views.SearchPanel` has no
column set and `/searches` draws Sonarr's columns; the album bar renders no
Edit or Delete (no album EditItem, Album not deletable); a standalone
Book's bar has Refresh without scan and no Rename (`hasFolder` reads no
Book), and the Edit dialog also renders on an author's book; "Search
volume" creates a plain grabBest Search on the Comic, fanning out per
Issue, since the Search CEL admits `spec.season` on a series alone and
`DecideSeasons` serves a Comic's automatic state only; the Metron and
Hardcover logos have no link (no client records a page URL); the search
tables' Peers/Grabs is Peers (ReleaseInfo carries no grab count -- owner:
the indexer agent would record newznab's `grabs`), and the audiobook's
Bitrate is size over runtime, titled so; Add New's hits show title, year,
cover and source only (`SearchHit` carries no publisher, issue count,
authors or narrators), one comic provider answers a search (no source
selector on the request), and an Audiobook is added with no authorRef;
`handleSetMonitored` answers no Audiobook row (no row lists audiobooks);
the children fragment (`ui/children.go`, `views.ChildrenPage`) is reached
by no page and awaits deletion. **Wave C (nonvideo-implement-c, nine
agents, 2026-11-10, closed most of these):** one panel per kind (d87083a4,
aeab9c63: `views.SearchPanel.Kind`/`Columns`, `releaseTableFor` in
`ui/views/search_tables.templ`, the per-kind routes gone, a book's modal
drawing the format columns); the children fragment deleted and an Album
editable, a standalone Book a folder kind and an author's book not editable,
`containerFiles` bounded at 500 (57846bf7, 4bb329c4); the planned volume
pack search's api, planner, stage, actions and comic page (df9d009e; the
Search controller's half is wave D's, until then a planned one is failed
as SeriesGone); the Hardcover page link recorded by its client and answered
for an Open Library-keyed item (76246285, 39e70633; the Metron page reaches
`ComicVolume.Links` and waits on a `ComicMetadata.Links` field, wave D's);
`SearchHit`'s publisher, issue count, start year, authors, narrators and
release date, `MetadataRequest.Source`, the gateway's author-by-ASIN lookup
(a38899bf; the ui half is wave D's); `{PartNumber}` as the part's place,
the manual assignment's attribution, `.fb2`/`.djvu` (dffe9cff); the Series
fan-out on childsync (fbab16f7; its add-options flag defect, shared with
the Artist and Author, is wave D's fix); `/art`'s retry on every stall
(8a7d0566). **Wave D (nonvideo-implement-d, twenty agents, 2026-11-10: the
five reviews wave C lost, D1-D6, each reviewed and fixed):** the
add-options flag on the Series, Artist and Author (308f974c, §5.1's note);
Add New's hit details, per-source comic searches and the audiobook author
link (d1d60a8f, 97feb025, §5.6's note); the Search controller serving a
Comic's planned volume Search and running its interactive one as one
season task (bd9f4d79; both comic-page buttons work end to end; messages
say "comic'" where "comic's" is wanted, open); `ComicMetadata.Links`,
`IssueStatus.Volume` and `metadata.PageProvider` -- Hardcover for an Open
Library-keyed Author or Book, Metron for a ComicVine-keyed Comic through
its crosswalk -- so the Metron and Hardcover logos link (1deae283,
ca89d7af; `SchemaVersion` kept at 8, so a cached document shows them at
its next refresh); the dead per-kind panel types, `ChildRow`/`ChildrenURL`,
`BookRow.ReturnURL` to the author's page (`views.AuthorURL`), the album
page's own Edit over the album's card, the hero's Size fallback to the
card's bytes, and a standalone book's folder read from its file when
`status.path` is the root (6d0056b9; the Book reconciler's `book.Path`
still writes the root for a book without an author, open); the part
numbered in `mediafile.LoadNaming`'s audiobook case with the S12/MediaFile
sibling wake and `release.PartOf` reading 1..99 (11dfc511, 09928e4a,
a17e2391), the usenet engine's extension list derived from `pkg/fsops`
(a4e615f8), the two stale rescan envtests re-aligned (76ab8e3d). Still
open after wave D: Album Delete (`DeletableKinds` and `librarydelete` know
no Album; a standalone Book is likewise not deletable though §9.3 says
so); grabplan's coverage messages worded for episodes; the rename
actuator's remembered collision never retried once the target is free;
`metron.Client.Page` as the client's own method (the gateway adapts it);
FB2/DJVU quality tiers (owner); the Comic memo keyed on the flag; the ui's
audiobook Parts table showing the ordinal (done by the controlling
session's integration commit). **Wave E (nonvideo-implement-e, ten agents,
2026-11-10):** a standalone Book's `status.path` is its own folder,
`book.Path` agreeing with the import's `catalogctx.BookFilePath` byte for
byte (ba7c7256; the ui admits the folder once the book has a file); an
Album and a standalone Book are deletable (87afc7de, a196a7ed:
`DeletableKinds`, a `librarydelete` kindSpec each with a `Keep` bound on the
artist's folder, the manager role's `delete` on albums and books; a deleted
album of a listed release group returns at the Artist's next fan-out as in
Lidarr); coverage rejections name the covered kind (b903ea47; the caller is
`app/catalog/controller/search/season.go`'s seasonItemState); a remembered
rename collision expires after a minute (6760dc20); `childsync.Key` carries
no add-options flag and `metron.Client.Page` is the client's own
PageProvider (7450a5f9, edcfd506). Still open: Album and Book import-list
exclusions; a fileless standalone book's page still offers Refresh & Scan
and Rename (dead until it has a file); the sibling controllers' `status.path`
joins no sanitized component where the import's does (a "?" in a title);
the grab check's refusal reason reaches no log. **Browser check done (2026-11-10, the
controlling session, headless Chrome against bin/ui at 819dc23f over an
envtest apiserver filled by `ui/preview_harness_test.go`, with talos's NATS
port-forwarded):** the artist, comic, issue, audiobook, author, book and
standalone-book pages render in the Plex look with the six-tab sidebar; the
Edit dialog's `intersect once` block fetch lands inside the portaled dialog
on every kind (the artist's profile types, the comic's kind and special
version, the audiobook's ten-region select and Author/Book selects, the
standalone book's profile and root), each form carrying the "fields" marker;
the issue's interactive search opens its modal in `#issue-modal` and the
panel self-polls (`load delay:2s`, outerHTML); the editions collapsible
opens; and Add New's three new searches answered live through talos's
metadata gateway (MangaDex "Berserk" with source badges, Open Library "The
Dispossessed" as a standalone Book, Audible "Wizard of Earthsea"). MangaDex
hits carry no cover (volume covers not built), so their cards are blank.
A second run at 6150558b (after waves C and D, the same rig): the album
page renders its own Edit with the album block (the artist's profile,
`anyReleaseOk`, the release select) and its Interactive modal polls the
shared `/searches/{ns}/{name}`, as the comic's volume modal does with
`filter=all`; Add New's hit details wait on the catalog agent that fills
them (talos's gateway at rev 244 predates `SearchHit`'s new fields).

### 9.1 Tabs, cards, toolbar, bulk, typeahead

D1's tabs. Cards: the year label reads an Author's nothing, an Artist's
nothing, a Comic's start year, an Audiobook's release year, a standalone
Book's release year; the stripe and the Status fact come from the container
rollup (`containerStatus`, the `seriesStatus` rule over missing and
downloading children) so Missing, Wanted and Cutoff Unmet filters match
containers; the bulk editor is unchanged and its refresh scans every kind
with a folder (Comic and Audiobook have `status.path`, so `hasFolder` reads
it). The typeahead's id prefixes gain `comicvine:`, `mangadex:`, `asin:`,
`olid:` beside `mbid:`; `LibraryItem.ProviderID` is set for Comic
(`<source>:<sourceID>`), Audiobook (`asin:`) and Book (`olid:` work).
`statusLabel` reads an unmonitored container as Unmonitored, not Missing.

### 9.2 Music

- **Artist page** (`/library/{ns}/artist/{name}`): the item page's hero on
  the artist's fanart with the poster (thumb), name, sort name,
  disambiguation, type, country, years active, genres, the Status fact from
  the rollup ("12 albums, 3 missing"), provider logos (MusicBrainz,
  Discogs, Wikipedia, Bandcamp/Last.fm/Spotify when the links carry them),
  the action bar (Refresh & Scan, Search, Rename, Edit, Delete), and the
  **Albums table** (Lidarr's): grouped by album type (Albums, EPs, Singles,
  Broadcasts, Other) under collapsible headers, each row the monitor
  bookmark, title (opens the album page, preloaded), release date, type
  badge, the status cell (`albumStatus`: downloading, pending, quality with
  "n/N tracks", amber below the cutoff or with tracks missing, unreleased,
  unmonitored, missing) and the two searches (`searchButtons('album')`), with
  the `#album-modal` region on the page. Edit's kind fields: metadata profile
  (primary types, secondary types, release statuses), monitor new items.
- **Album page** (`/library/{ns}/album/{name}`, the episode page's
  analogue): hero on the artist's fanart with the cover, title, artist (a
  link), release date, type, label, duration, track summary, MusicBrainz
  (release group and release) logos, the facts (Status with `cutoffReason`,
  Quality, Profile, Path, Monitored), the action bar (Refresh, Search,
  Interactive, Rename, Edit, Delete), the **Tracks table** (disc and number,
  title, duration, artist credit when it differs, the file's cells: codec,
  bitrate, sample rate and bit depth, size, and a status dot for attributed
  or missing), the Files section, and the neighbouring albums. Its
  interactive search modal (`views.AlbumSearchModal`, `#album-modal`) lists
  music columns: Quality (FLAC 24bit, MP3-320), Size, Age, Indexer,
  Peers/Grabs, Score, with the banded results and grab of every other modal.
- The Downloads and Dashboard owner links open the album page.

### 9.3 Books

- **Author page**: today's page plus the hero facts (born, died, genres,
  Open Library and Hardcover logos) and the books table's title opening the
  **Book page** (`/library/{ns}/book/{name}`): hero with the cover, title,
  subtitle, author, series and position, release date, pages, publisher,
  ISBN, languages, logos (Open Library, Hardcover, Goodreads when an id is
  known), facts, the action bar (Refresh, Search, Interactive, Rename,
  Edit, Delete), the Editions collapsible (id, title, format, language,
  publisher, pages, release; the pinned one marked), the Files table
  (format, size, score), and the neighbouring books in series order. The
  modal stays for the row's details and search tab. A standalone Book is
  addable (Add New's Books tab offers "Author" and "Book"), editable
  (profile, root, monitored) and deletable.
- Interactive search columns: Format (epub, mobi, azw3, pdf), Size, Age,
  Indexer, Peers/Grabs, Score.

### 9.4 Comics

- **Comic page** (`/library/{ns}/comic/{name}`): hero on the cover (and
  background when a provider has one) with title, publisher, start and end
  year, volume number, issue count, status, manga and reading direction,
  age rating, genres, logos (ComicVine, Metron, MangaDex, AniList, MAL),
  facts, the action bar (Refresh & Scan, Search, Search volume -- the pack
  search, with the `status.volumeSearch` badge as a season header has --
  Rename, Edit, Delete) and the **Issues table** (Mylar's): number, title
  (opens the issue page), cover date, store date, the status cell
  (`issueStatus`, the episode's cases) and the two searches
  (`searchButtons('issue')`, `#issue-modal`). Edit's kind fields: kind
  (comic/manga), special version, monitor new issues.
- **Issue page** (`/library/{ns}/issue/{name}`): hero with the cover (the
  issue's `coverURL` through `/art/search`, else the comic's), title,
  number, comic, dates, credits, facts, the action bar (Refresh, Search,
  Interactive), the Files table (format, pages, size, score), and the
  neighbouring issues. Interactive search columns: Format (cbz, cbr, pdf),
  Size, Age, Indexer, Peers/Grabs, Score.
- Add New on the Comics tab searches ComicVine volumes and MangaDex manga
  (publisher, start year, issue count, cover in the hits).

### 9.5 Audiobooks

- **Audiobook page** (`/library/{ns}/audiobook/{name}`): hero with the
  cover, title, subtitle, authors (the Author link when `authorRef`),
  narrators, series and position, runtime, publisher, release date,
  abridged, language, logos (Audible, Audnexus, Open Library when
  `bookRef`), facts (Status with `cutoffReason`, Quality, Profile, Path,
  Monitored, Parts), the action bar (Refresh & Scan, Search, Interactive,
  Rename, Edit, Delete), the **Parts table** (part, title, duration, codec,
  bitrate, size), the Chapters collapsible (title, start), the Files section
  and the audio note is not shown (no dub state). Edit's kind fields:
  region, author link, book link. Interactive search columns: Format (m4b,
  mp3, flac), Bitrate, Size, Age, Indexer, Peers/Grabs, Score.
- Add New on the Audiobooks tab searches Audible's catalog in the root's
  region (title, authors, narrators, release, cover).

### 9.6 Shared pieces

- `views.actionBar` takes the kind's buttons; `views.providerLinks` gains
  the logos under `ui/static/providers/` (MusicBrainz, Discogs, Wikipedia,
  Open Library, Hardcover, ComicVine, Metron, MangaDex, AniList,
  MyAnimeList, Audible, Audnexus; each from Simple Icons where it is CC0,
  else the site's own mark, as TheTVDB's).
- `views.fileRows`/`FileRow` become kind-aware (`views.FileCells`: video
  cells for movie and episode; audio cells for album and audiobook; format
  and pages for book and issue), shared by every file table.
- `handleSetMonitored` answers htmx row replies for Album, Issue and
  Audiobook as for Episode and Book; `handleLibraryItem` dispatches Album,
  Book, Issue and Audiobook to their pages; `neighbours` covers them.
- The Delete dialog lists a container's files for every kind
  (`d.Files` from the kind's MediaFiles).
- The Settings page's MetadataProvider form is unchanged (generic).

## 10. cluster-plex (the other repo; a separate deliverable)

Recorded here so the two charts move together; built in cluster-plex under
its own design:

1. `provision`: section type `artist` (8), scanner "Plex Music Scanner",
   `Validate` admits `artist`, music library preferences (prefer local
   metadata on; sonic and loudness analysis off; album sort), the agent id
   for Plex's NFO music provider captured read-only from the talos PMS.
2. `plexproject`: informers and `FieldPaths` for Artist, Album, Audiobook,
   Author; `Resolve` for mediaRef kind `album` (artist 8 -> album 9 -> track
   10, `index` = track number, disc as PMS stores it, captured from the
   owner's PMS) and `audiobook`; `insertItem`/`locate` for tracks;
   `plexseed.MediaRows` for an audio-only file (no video stream row; the
   lead track's codec and channels on `media_items`).
3. The chart's `libraries` gains `Clustarr Music` (`type: artist`,
   `/media/music`) and `Clustarr Audiobooks` (`/media/audiobooks`), the
   provider ids commented out until §7's roots are on.
   (Read-only on talos's PMS, 2026-11-10 night, GET /system/agents?mediaType=8:
   the artist agents are com.plexapp.agents.none (Personal Media Artists,
   primary), com.plexapp.agents.lastfm (primary, prefs), tv.plex.agents.music
   (Plex Music), tv.plex.agents.none and tv.plex.agents.nfo.music -- listed
   twice, as "Plex NFO Music" (id 9) and as "Clustarr Music" (id 10), the
   second of unknown origin and worth asking the cluster-plex sessions about
   before provisioning; /media/providers lists no custom provider at all, so
   the movies' and tv's registrations are not read there. The sections today
   are 1 movie and 2 show on the clustarr custom providers.)

## 11. Waves and tasks

Each wave lands on `main` by pathspec commits, gated by `go build ./...`,
`go vet` of the touched packages, `make brand-gate`, `make generate
manifests` clean, and plain `go test` of the touched packages (the envtest
run is the single final one, owner 2026-10-08). CRD changes are applied on
talos by hand before the deploy that carries them.

- **W0 (serial): API.** §4 in full; `make generate manifests`; the apply
  configurations and bases (the chart's `crds/` is populated only at release
  time, its README); `pkg/crdcheck` green; the Terraform values note for the
  renamed fields. **Done 2026-11-10 (e6fc628d):** every §4 field,
  `commonv1.LeafPhase` with the kinds' old constant names kept as aliases of
  it, `issue.State` reading Unavailable from the date, the NFO schema,
  consumer and bucket; the call sites follow the renames and nothing else
  moved yet.
- **W1: the manager.** D2 (Artist/Author loop keys, child sync memo,
  folder scan), D3 (monitoring), D4/D5 (phases, cutoffReason, file
  vocabulary, Issue phase), D7 (naming/rename), D10 (comic add options,
  volume pack rules, nextPullDate), D11 (audiobook add options, authorRef,
  parts), D14 (events, SkipInitial), the rollups and `itemstatus` for eight
  kinds, stale comments corrected.
  **Done 2026-11-10 (wave A, eight agents in parallel, gated and pushed by
  the controlling session through 5fc69889):** W1.1 c2ca352b/6251f9f5, W1.2
  086815d2..ee1fb3e3, W1.3 45f2ffad..43d01ce7 with 18f44eeb, W1.4
  39b435b8/53c39b5a/e556ce9e, W1.5 0a5d4c5a/7fbd7f41 with 2ab57111; the
  plan's per-task "Done" notes list what each left over.
- **W2: metadata.** §5.4: schemaVersion and language everywhere, ratings
  and the enum, image selection, MusicBrainz paging and includes, the
  wikipedia client, fanart mapping, Audnexus chapters and fields, the
  audible client and the search RPC's comic and audiobook verbs, ComicVine
  image and paging and the Issue fan-out fields, Open Library/Hardcover
  fields, the Artist and Audiobook extended documents.
  **Done 2026-11-10** (inline through 42881a2e, then d32353b1 and c4dc98eb:
  the fetch language, enrichRatings for the six kinds, the Artist and
  Audiobook extended documents, Hardcover as a Book RatingsProvider,
  Audnexus's tags/abridged/ISBN). Not built: Audnexus `Authors()` by ASIN,
  MusicBrainz `artist-rels` for members and similar artists (the Artist's
  extended document is written empty), MangaDex volume covers, Hardcover
  author ids in the Author's book-list request and fan-out.
  **Wave B (L2, 2026-11-10):** Audnexus's author by ASIN (6f8701b0,
  `metadata.AudiobookAuthorProvider`, `Registry.AudiobookAuthors`; nothing
  dispatches to it until wave C's Add New link), MusicBrainz's member-of-band
  relations as `Artist.Members` (5a5fea2d; a member's MBID and tenure are
  decoded and dropped, `metadata.Person` carrying neither), MangaDex's
  issues by chapter (df45c62f, `schema.MetadataRequest.Numbering`, which the
  Comic reconciler sends for a manga since 819dc23f). Still not built:
  MangaDex volume covers, Hardcover author ids.
- **W3: import and probe.** §6: probe tags, sample rate, bit depth, page
  count, `ProbeVersion`; track attribution and part ordering; book quality
  freezing; `pkg/nfo` and the `import-nfo` consumer; the `nfo` stage.
  **Done 2026-11-10** (inline through 49c57629, then 6f913034, 0b0857c1,
  7ef5ef2c, 5fc69889); the page counter is `pkg/mediainfo/pagecount`, the
  import agent's alone, so the manager links no archive reader. Not built:
  an ebook (`.epub/.mobi/.azw3`) probe, so a Book's file still fails its
  probe and is never named.
  **Wave B (L1, 2026-11-10):** the ebook probe answer (9628fa31: the fsops
  book extensions, answered without FFmpeg) and the import placing a
  non-video file where the loop's rename would (1b3572b9, through
  `catalogctx`; a tagged audiobook part still renders its ordering key as
  `{PartNumber}`, wave C).
- **W4: the ui.** §9 page by page: tabs and cards; Artist and Album; Author
  and Book; Comic and Issue; Audiobook; Add New for comics, audiobooks and
  standalone books; shared cells, logos, dialogs, reader indexes, projection
  rollups and owner links.
  **Done 2026-11-10 (wave B, ten agents: W4a, then W4b.1-4 in parallel,
  then W4c, with L1-L3 and the e2e extension beside them; gated and pushed
  by the controlling session through 819dc23f):** §9's as-built note lists
  the commits and the departures, the plan's W4 Done notes the leftovers;
  wave C (nonvideo-implement-c) took the departures -- §9's "Wave C"
  sentence lists what it closed -- and wave D (nonvideo-implement-d) the
  rest, with the five reviews wave C lost to the usage limit. **Done
  2026-11-10 (wave D):** §9's "Wave D" paragraph lists the commits and
  what stays open.
- **W5: Plex.** §7.6's spike first, then the roots, index, match, metadata,
  children, images, goldens and tests, the flag and chart values.
  **Done 2026-11-10, flagged off** (ea0d54d4, 7a8d17aa, 2c8676fb, 1df72500,
  95a9055a): built to §7's inferred shapes behind `--plex-music-provider`;
  the flag is parsed and logged but not yet routed into `ui.PlexOptions`
  (W4a wires it). ADR-0022 records the decision.
- **W6: records.** CLAUDE.md (the Services table's `import-nfo`, the UI
  section's four tabs and pages, the Plex paragraph), ADR for the NFO
  writer (why the agent writes files the manager renders), the e2e
  scenarios 9-11 updated (written, not run), this document's "as built"
  notes.
  **Progress 2026-11-10:** ADR-0022 (53559009); CLAUDE.md's UI section
  and Music section (after wave B); scenario 11 extended for the parity
  (edbcb9e1, with the nonvideostub's Cover Art Archive, Wikidata/Wikipedia
  and Audible routes, 9fc4eab4, and config/e2e's coverart, wikipedia and
  audible providers, 819dc23f; written, not run; scenarios 9 and 10 touch
  no non-video kind). Left: the remaining-work items and the final notes
  after wave C.

Order: W0, then W1 and W2 and W3 in parallel (disjoint packages: W1 owns
`app/remediation`, `app/catalog/controller`, `app/catalog/{searchplan,
grabplan,itemread}`, `app/grab/lifecycle`; W2 owns `app/catalog/metadata`,
`pkg/metadata`, `app/catalog/worker/search`'s rpc; W3 owns `app/import`,
`pkg/mediainfo`, `pkg/nfo`, `pkg/release`'s track and part parsers, `pkg/fsops`),
then W4 and W5 in parallel (W4 owns `ui/` but `ui/plex` and `ui/projection/
index.go`, W5 owns those two), then W6. The `nfo` stage's planner is W3's
and its registration in the item pass W1's, agreed on the `NFOWriteTask`
schema in `pkg/events/schema` written in W0.

## 12. Tests and guards

- `pkg/crdcheck`: the new enums, caps and defaults (`TestNoCRDDefaultIs
  UnreachableFromGo`, `TestEveryStatusListIsCapped`).
- `app/catalog/controller/rollup`: `LeafPhase` and `Container` tables for
  every kind; `TestLeafPhaseHasMoviesPrecedence`.
- `app/remediation`: `TestEveryItemKindIsALoopKey` (the eight kinds plus
  Artist and Author), `TestNoStandaloneItemController`†, the Artist and
  Author pass envtests written for the final run.
- `pkg/nfo`: goldens for an artist, an album with two discs, an author and
  an audiobook; `TestNFOWritesOnlyKnownElements`; `TestNFOHashMovesWithEveryField`.
- `app/import/worker/fileimport`: track attribution table (tags, names,
  recording ids, two files for one track, a file for no track); part
  ordering; `TestBookQualityIsFrozenAtImport`.
- `pkg/mediainfo/native`: `TestAnAudioFileProbesWithoutVideo` on FLAC and
  MP3 fixtures; `TestTagsAreBounded`; `TestPageCountOfACBZ`.
- `ui`: markup tests per page (hero facts, table cells, modal regions,
  owner links), `TestTabsAreSix`, the role guard for the new creates; a
  browser check of every new modal (CLAUDE.md's rule). As built: the
  per-page markup tests W4b named, `TestEveryTabHasAnAddPage`,
  `TestEveryKindsLibraryPathLandsOnItsPage`, `TestEditFieldsRouteServesEach
  KindsBlock`, `TestNonVideoPagesWaitForAnItemAddNewJustCreated`; the tab
  count is `chrome_test`'s `len(Tabs())`; the browser check is pending.
- `ui/plex`: `root_music.json`, `root_audiobooks.json`, match and metadata
  goldens per type, the 503 loop over four roots, `TestMusicRootsRegister
  OnlyUnderTheFlag`, ratingKey grammar tables; the spike's record in §7.
- `test/e2e/nonvideo_test.go`: scenarios 9-11 extended for the tabs, pages
  and NFO files (written, never run, as every scenario since Phase C).

## 13. Open questions for the owner

- **Q1.** Wikipedia (through MusicBrainz's Wikidata url-rel) as the artist
  biography source: it is the one new external source here. Alternatives:
  MusicBrainz annotations alone (sparse), Last.fm (keyed), TheAudioDB
  (keyed). Built as designed unless refused.
- **Q2.** Audible's public catalog API for Add New audiobook search:
  keyless and stable for years but undocumented. Alternative: add by ASIN
  only. Built as designed unless refused.
- **Q3.** The Plex roots are built and flagged off. Should a registration
  spike also be tried against the talos PMS once the local one passes, or
  wait for Plex's announcement?
- **Q4.** Should an album's `Children` list every track or only tracks with
  files? Designed: every track (the tv root's rule).
- **Q5.** `plex://` GUIDs for music (Discover and Watchlist for artists)
  need the plex client to match artists and albums by `mbid://`; not built.
- **Q6.** Narrators have no Kodi NFO element; they are not written. Plex's
  agent may learn one; revisit when its field list is published.
- **Q7.** Issue's `state` becomes `phase` cold turkey (D4). Any reader
  outside this repo? None found in cluster-plex.
