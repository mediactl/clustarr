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

- **D16. A root scan creates Artists, Authors and Audiobooks from their
  folders, as it creates Movies and Series** (owner, 2026-11-10, after the
  music and audio roots went live on talos and the rescan listed every file
  as `unrecognised_layout`: /data/media/music is 390 artist folders with no
  album folder, and `scanapply` creates movies and series only). The rule
  is amendment A1.5 as written -- identity first, then the catalog, then the
  gateway, never a speculative item -- and Lidarr's Library Import (its
  `IdentificationService` fetches MusicBrainz candidates for an unmapped
  folder and the person confirms the match). For the first segment under a
  music root, in order: a `[mbid-<uuid>]`/`{mbid-<uuid>}` token in the folder
  name; else the `MUSICBRAINZ_ARTISTID` tag that up to five of the folder's
  audio files agree on, each whose album artist (else artist) tag is the
  folder's name by `release.CleanTitle`, validated by the gateway's lookup
  (`RPCMetadataLookup`, kind artist), which also gives the canonical name;
  else the gateway's artist search (`RPCMetadataSearch`, text the folder
  name) read strictly: the hits whose `release.TitleNorm` equals the folder
  name's, and exactly one of them at the search's top score, is the artist;
  none is `no_match`; two or more is `ambiguous_title`, listed once in
  `status.unmatched` for the folder (`UnmatchedFile.folder: true`) with the
  hits as candidates (`Name (disambiguation) [mbid]`), its files counted,
  not listed. `pkg/metadata.SearchHit` carries `score` and `disambiguation`
  for it (MusicBrainz's, dropped until now). An author folder under a book
  root goes the same way through Open Library (`[olid-OL…A]` token, else
  the search; no tag). An audiobook folder (`<author>/<title>`) is an
  Audiobook when it names an ASIN (`[ASIN B0…]`, `[B0…]`) in the root's
  `defaults.region`, which is why the old refusal ("the region would be a
  guess", `rescan/nonvideo.go`) no longer holds, else when Audible's search
  for the title in that region answers exactly one hit whose title equals
  the folder's and whose authors name the author folder; it links
  `spec.authorRef` as Add New does (exactly one Author of the name in the
  namespace, else none) and creates no Author. The agent proposes
  (`schema.ItemCreate` gains `musicBrainzID`, `openLibraryID`, `asin`,
  `region` and `folder`; one `present` observation per folder, the Series'
  shape, the folder's other files counted as awaiting the item's children)
  and the manager's applier creates: `spec.folder` pinned to the on-disk
  folder as a scanned Series' is, the root's `defaults.qualityProfileRef`
  (required, as for every scan create), `defaults.monitored`,
  `defaults.monitorNewItems` and `defaults.tags` (which the movie and
  series creates still do not read), `addOptions.monitor: all` and
  `searchForMissing: false` (a scan never starts a search storm; the owner
  sets `defaults.monitored: false` on a root before the first scan of a
  library whose missing albums are not wanted), an Author's
  `metadataProfile.allowedLanguages: [en]` as Add New's. An ImportExclusion
  of kind `artist` (MusicBrainz) or `author` (Open Library) refuses the
  create, written by the delete dialog as a Movie's is.
- **D17. Tracks attribute by their tags whatever the folder depth** (D6
  extended to the rescan; Lidarr's `TrackGroupingService` groups by tag,
  never by folder). Under a music root the walk probes every audio file
  before it matches (the probe is the header read `fileimport.
  FrozenFileQualityProbe` already runs after the match), the first segment
  names the Artist (status.path, `spec.folder`, then its names), and the
  file's Album is, in order: the `MUSICBRAINZ_RELEASEGROUPID` tag naming an
  Album of that Artist; the `MUSICBRAINZ_ALBUMID` tag naming a release of
  one (`AlbumCandidate` carries the group's release ids); the album folder
  when the file sits in one (today's rule, segment two's title and year);
  else the `ALBUM` tag's `CleanTitle` against the Artist's albums, the year
  narrowing only when both sides know one. Two albums is `ambiguous_title`,
  none is `no_child_item` naming the tag ("no album of 2Pac is titled
  ..."), honest for a compilation or live album the artist's metadata
  profile does not list. The track inside the album is `AttributeTrack` as
  before. An Album whose `status.tracks` has not arrived counts its files
  as awaiting (the `AwaitingEpisodes` precedent, generalised to
  `awaitingChildren` with the kind's noun), never unmatched, and the
  Artist asks for its folder scan only once its albums are settled (D18).
  Audiobooks keep `<author>/<title>/<parts>`; books keep `MatchBook`.
- **D18. The folder scan a container owes waits for its children's
  lists.** `folderscan.Request` gains `ChildrenSettled`: a Series passes
  true (its episodes come whole from the fan-out), an Artist passes "every
  Album the fan-out listed has `status.tracks` or a MetadataReady False
  condition", an Author "every Book has `status.metadata`"; `Pass` stays
  `Settling` with a message naming how many are pending until then. On
  talos 390 artists' albums are thousands of MusicBrainz lookups at one a
  second, hours after the artists exist, so a scan asked 30 s after the
  albums were listed would attribute nothing.
- **D19. The rename pass re-lays a flat folder when the root asks.**
  `RootFolder.spec.naming.organizeFolders` (default false; needs
  `renameFiles`): under it a non-video file whose canonical path lies in
  another folder strictly under its container's folder (an album track
  under its Artist's folder, a book under its Author's, a part under its
  Audiobook's, an issue under its Comic's) is moved there -- the target
  folder created, `fsops.MoveNoReplace`, the sidecars beside it, the empty
  source folders pruned up to the container's folder, never the container
  folder or the root -- where the probe-driven naming design's D5 held
  every move to another folder (`mediafilespec.RenameHeld`, "files only, not
  folders"). Video keeps D5. Album-level files (`cover.jpg`, `.cue`, `.log`)
  stay where they are; the nfo stage writes the album's own. The actuator's
  remembered hold is keyed by the mode too, so turning the field on retries
  what was held. Lidarr's rename moves a track into its computed album
  folder; this is that, opt-in. And the music preset learns Lidarr's
  multi-disc shape: a release of two or more media renders
  `{Album Title}{ (Release Year)}/{Medium Format} {medium:00}/{Artist Name}
  - {Album Title} - {track:00}{ - Track Title}` (`{medium:00}`, `{Medium
  Format}` tokens; `multiDiscTrackFile` override), since the one-disc preset
  collides every disc's track 1 (All Eyez on Me).
- **D20. The Unmatched page confirms a folder.** An unmatched folder row
  (D16's `folder: true`) offers its candidates as "Add as artist" (author,
  audiobook) buttons, each Add New's create with the hit's id and title,
  the root and the folder pinned (`AddRequest.Folder`), and a "Search" link
  to Add New filled with the folder's name -- Lidarr's Interactive Import
  confirmation, no automatic pick. The library-scan page's progress line
  says how many files await their items' children.

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

### 5.7 Scanning an existing library (D16-D18, wave G)

The walk under a music root (`rescan/nonvideo_scan.go`) runs per first
segment: an existing Artist by `status.path`, `spec.folder` or name takes
it; else `rescan/identify.go` resolves the folder (token, agreeing tags,
strict search) and proposes one `ItemCreate{kind: artist}` observation
(`Evidence: folder`), counting the folder's files as awaiting albums; a
folder it cannot resolve is one `UnmatchedFile{folder: true}` with the hits
as candidates. A file whose Artist exists is probed (tags) and matched to
an Album by D17 through `MatchAlbum(absPath, rel, tags, cands)`, then to a
track by `AttributeTrack`. A book root does the same for its author
folders through Open Library and keeps `MatchBook`; an audiobook root
proposes `ItemCreate{kind: audiobook}` from an ASIN token or Audible's one
hit, the file recorded in the same observation with its `PartIndex`. The
applier (`scanapply/create.go`) adds `createName`, `ensureItem`,
`applyArtist`, `applyAuthor` and `applyAudiobook` beside the movie's and
series' (names through `pkg/names`, the exclusion check by the kind's
provider key, the write pace), returns after an artist or author create as
after a series' (its files await the folder scan the container owes), and
its role gains create/update/patch on artists, authors and audiobooks.
`folderscan.Pass` takes `ChildrenSettled` (D18). The agent's search is one
`RPCMetadataSearch` per unmapped folder under the metadata agent's
MusicBrainz limiter (390 folders is about seven minutes), its answer not
cached; the lookup validating a tag is `RPCMetadataLookup`. The
never-guess comments in `rescan/match.go` and `rescan/nonvideo.go`
are reworded to A1.5's text: the gateway is asked, and only an exact,
unique answer is taken.

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
the grab check's refusal reason reaches no log. **Wave F (nonvideo-implement-f,
sixteen agents, 2026-11-10 night):** every kind's `status.path` through
`catalogctx.ItemFolder`/`BookFolder`/`AlbumFolder` with `FolderUnrenderable`
and the idempotent sanitizer (fb37ec34, 2b63e9bf, 2278c17f; the Album's by
the controlling session); a standalone Book's import-list exclusion
(a0432444: `ExclusionKindBook` by Open Library work id, read by the sync),
the Album's written but withheld from the dialog until the Artist fan-out
honours it (3511d14c); a fileless standalone book shows neither scan nor
rename (`views.Detail.HasFolder`, 77ea5f3d); the grab check's refusal
reason at settle (`grabplan.DecideContext`, fd6d561d, wired in the stage by
the controlling session); `ui.Run`'s silent-connection shutdown and the art
stream's subscribe-before-200 (59cff4f5, 1596e211); the pre-existing test
reds repaired against the behaviour as built -- the metadata gateway's 17
envtests over a records harness (738def0b), the search controller's two
(5fa4de09), the metrics catalogue held to docs/observability.md (9e4677d2,
5268eb7c), the schema guards (ff9d3191), crdcheck's Live TV rule (925794e1,
77e14734); the book modal's return URL and the doc debts (8a19b362,
053b2f23). Still open: the Artist fan-out honouring an Album's exclusion
(and the Author's a Book's); a MangaDex exclusion pair for comics; the
import's own non-video folder fallbacks re-implementing the folder rule
(fileimport/nonvideo.go); the movie controller's two MetadataTask
assertions (red since the ledger); `catalog.ImageSets.v1`'s wire name
(owner). Live on talos as Helm rev 249 since 2026-11-10 23:15Z (images
sha-f1e2c57 over the rev 248 chart, no CRD change): FolderUnrenderable
fired for no item, and the nine Series whose titles carry a `?` or `"` took
the sanitized `status.path` with no file to move. **Browser check done (2026-11-10, the
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

### 9.7 Unmatched folders (D20)

`ui/views/unmatched.templ` draws a folder entry (`UnmatchedFile.folder`)
as the folder, its reason and one button per candidate ("Add as artist:
Nirvana (90s US grunge band)"), each a form to `POST /library/{tab}/add`
with the hit's provider id and title, the scan's root and `folder` = the
entry's path, which `actions.AddRequest.Folder` pins into `spec.folder`
(Artist, Author, Audiobook); a "Search" link opens Add New with `?q=` the
folder's base name and `&folder=`, which the create form carries hidden.
A file entry keeps the manual-assign form. `views.LibraryScanProgress`
shows `awaitingChildren` ("412 files await their artists' albums").

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
   second of unknown origin -- not the §7.6 spike, which ran against a
   local PMS, and nothing in cluster-plex names music (clustarr-c0,
   2026-11-10); the ui's own root is tv.plex.agents.custom.clustarr.music,
   flagged off on talos -- so read the agents table in PostgreSQL (schema
   plex, id 10's creation) before provisioning; /media/providers lists no custom provider at all, so
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

**Wave G (scan an existing library; D16-D20; plan W7, 2026-11-10/11):**
W7.0 serial (the API, schema and naming tokens: `ItemCreate`'s new fields,
`SearchHit.score`/`disambiguation`, `ExclusionKind` artist/author,
`naming.organizeFolders`, `UnmatchedFile.folder`, `scanprogress`
`awaitingChildren`, `{medium:00}`/`{Medium Format}` and the multi-disc
preset, `make generate manifests`, the scanapply role's RBAC markers and
the chart copy), then in parallel W7.1 identify (`rescan/identify.go`,
`nonvideo_scan.go`, the package docs), W7.2 attribute (`rescan/nonvideo.go`
`MatchAlbum` with tags, `AlbumCandidate` ids, `fileimport/tracks.go`),
W7.3 apply (`scanapply/create.go`, `applier.go`, `librarydelete`'s
exclusions for Artist and Author), W7.4 settle (`folderscan.Request.
ChildrenSettled`, the Artist's and Author's computation, the Series' true),
W7.5 re-lay (`specwrite.RenameFile`'s mode, `fsops` mkdir and prune, the
actuator's mode and memo key, `openScanOver` on the target folder, the
2026-09-24 naming design's D5 as-built note, `TrackFilePath`'s multi-disc
case), W7.6 the Unmatched page and Add New's folder pin; each task
implement, adversarial review, fix; the controlling session gates, pushes
and documents. CRDs by hand on talos first (ImportExclusion, RootFolder,
LibraryScan).

**Wave G as built (2026-11-11, eighteen agents, none lost; main 587465aa,
every task reviewed and fixed):** W7.0 316b562c, a8ba3bee, e95f0d70,
cd5d1e1e, 88924128 and its fix round ef1900e6, 666d1df6, dfa3f61b; W7.1+W7.2
292fcb2b and b6d9b72f; W7.3 76831332 and ca2b568b; W7.4 bd494579 and
22046800; W7.5 822e5db5 and 9f7673d2; W7.6 c9f86c07 and 19e2bf46; the
controlling session's 587465aa. Where the build departs from the text
above: (D16) the agent's lookups and searches run under `IdentifyTimeout`
(2 min) on their own goroutine while the walk heartbeats, since they queue
on the metadata agent's one MusicBrainz limiter beside the fan-outs the
walk's own creates start (a 10 s deadline failed fast past ten queued
requests); a gateway that cannot be asked is the new reason `lookup_failed`,
a second file for one track `duplicate_track` (the album's recorded
MediaFiles read once per walk through the import agent's target index, the
first file in walk order holding the track); a merged MusicBrainz id is the
artist it became; no special-purpose artist (Various Artists) is proposed;
an ASIN in a file's name is its audiobook; a refused create (excluded, no
default profile, a name collision, another root) is listed as the folder
(`Folder: true`) for artists and authors, one entry per part for an
audiobook, a series' still as its file; a second folder of one audiobook is
used when the item exists, else re-observed, never dead-lettered, and one
create decision runs per item at a time (`itemLocks`); an Artist created by
server-side apply must send `metadataProfile` as an object, since the CRD
defaults its lists only when the object is present (an empty profile
accepts no album). (D17) the ALBUM tag's DATE refuses only a date before
the album's first release and picks among albums sharing a title, never
narrowing when the folder agrees; an artist folder named in any script,
symbols only or with its token keeps its albums (`nameEq`: the folder as
written, CleanTitle, TitleNorm); a settled list never holds a file: an
Artist synced with no albums reads `no_child_item`, an Album whose
TracksSynced answered empty too, and both wait at most 24 h on a transient
answer (`childrenWait`, mirrored by `folderscan.ChildrenWait`). (D18) the
built rule is "every listed Album carries its document and a TracksSynced
answer" -- a transient gateway failure (RPCError, RPCTimeout, NoAgent,
AgentBusy) is no answer, where the first cut read every False but three
reasons as listed and the talos limiter's fast-fail would have spent the
artist's one scan on albums with no tracks; the Album's title is required
too, since `MatchAlbum` matches on `status.metadata`; the wait is bounded
by `ChildrenWait` (24 h) from the Unknown condition's first transition.
(D19) the medium folder renders `{Medium Format }{medium:00}` (the space in
the format's wrapper), and a medium the selected release records no format
for, the media read off `status.tracks` included (the gateway's releases
list is capped at 50 before the selected release), is `Disc NN`
(`catalogctx.DefaultMediumFormat`), never a bare `02`; a move whose target
is the container folder itself (an audiobook part or issue in a disc
subfolder, a standalone book in a subfolder of its own) is made
(`fsops.MkdirUnder` answers the root itself), and a symbolic link anywhere
on the way is a Collision, never followed; a source outside its container
stays held (a standalone book at `<root>/x.epub` does not organize; owner's
call). (D20) one folder holds one item: a confirmation is refused (400)
when another Artist, Author or Audiobook of the root pins or resolves to
the folder, and the projection drops the folder row once one does; a
confirmed folder takes the root's `defaults.tags` as the scan's create
does (Add New of those three kinds too, since `AddRequest.Tags`); the
"Search Add New" link starts every hit form on the root's defaults
(`views.AddFormDefaults`); the scan page's awaiting line is read out of the
Ready message's Summary text (no status field carries it). Still open after
wave G: a series refusal is still listed as its proposing file while the
walk counts it awaiting (a one-line change in `scanapply.refusal` plus the
CRD doc and a series folder row in the ui); two scans at once (a root's
and an Artist's folder scan) can still record two files for one track,
since the applier has no taken-track guard (its edit: list the album's
MediaFiles by `mfindex.ItemKey` and refuse a second holder as
`duplicate_track`); the 24 h wait has no shorter escape for a release group
MusicBrainz answers 404 (the gateway's error is a plain string; a not-found
flag on `MetadataResponse` and a TracksSynced reason would end it); the
album package's `TracksSynced` condition name is repeated by folderscan and
rescan (export `AlbumConditionTracksSynced`); `scanprogress.Kinds` has no
audiobook phrase; Add New of a Movie, Series or Comic still ignores the
root's `defaults.tags`; two confirmations of one folder inside the ui's
cache-sync window can both pass (an uncached reader closes it); the walk's
second audiobook folder keeps the first's `Folder` (the applier tolerates
it). Q8 and Q9 stand. The fileimport album test's two-medium fixture places
each disc under `Disc NN` since 587465aa. **Live on talos as Helm rev 251
since 2026-11-11 05:34Z** (chart 0.6.14 from 587465aa, images sha-587465a,
the rootfolders, libraryscans and importexclusions CRDs by hand first;
clustarr-df): every pod rolled clean, no manager error, no scan run yet;
the music root keeps `defaults.monitored: true` pending the owner's Q9.

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
- **Q8.** D17 attributes a track only to the Album's selected release
  (`status.tracks`), as the download import does; Lidarr matches across
  every release of the group and picks the release the files fit. A flat
  library tagged for a deluxe edition will read `TracksMissing` against the
  standard release until `anyReleaseOk`'s selection moves on the files that
  did attribute. Built as designed; a cross-release attribution is the
  next step if the owner's library shows it.
- **Q9.** D16 monitors a scan-created artist's albums `all` with no search
  on add: the wanted sweep and the BacklogSearch then search every missing
  album of 390 artists over time. Set the music root's
  `defaults.monitored: false` before the first scan to add them unmonitored
  (the Artist gates its Albums), and monitor the ones wanted by hand.
