# Clustarr record: Music, books, comics and audiobooks

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Music, books, comics and audiobooks

The four non-video libraries reached the movies' and TV's parity on
2026-11-10 (owner: "just like we did the movies and TV";
`docs/superpowers/specs/2026-11-10-non-video-libraries-design.md`, D1-D15,
with the plan's per-task "Done" notes naming every leftover). The shape:

- **Containers are loop keys.** Artist, Author and Comic are container item
  keys of the remediation loop beside Series (`remediation.KindArtist`,
  `KindAuthor`, `KeyKind.Container()`); each pass fans its children out
  behind the in-process `app/remediation/childsync` memo (due again only
  when the spec generation or `metadata.refreshedAt` moved;
  `status.addOptionsApplied` is no input since 2026-11-10, every container
  keying its fan-outs under it true, so `childsync.Key` dropped it; a subpackage because `app/remediation` imports the three
  controller packages for their indexes), folds them into
  `rollup.Container` (one `ContainerRollup` inline on the three kinds:
  phase, missing/downloading/cutoff-unmet child counts, next and previous
  release dates) and records phase edges as Events (`rollup.Reporter`).
  The standalone artist and author controllers and
  `metadataset.Incorporator` are gone. A container's monitoring gates its
  children (`itemread.Monitored`: an Album's Artist, a Book's or an
  Audiobook's Author, an Issue's Comic), as a Series gates its episodes.
- **One leaf vocabulary.** Album, Book, Audiobook and Issue read one
  `commonv1.LeafPhase` (Pending, Unavailable, Wanted, Delayed, Downloading,
  Imported, CutoffUnmet, CutoffUnevaluated, Unmonitored) and a
  `cutoffReason` through `rollup.LeafPhase`, with `hasFile`,
  `fileQuality`, `fileFormatScore`, `fileFormat` (Book, Issue), an Album's
  `trackSummary` ("files/tracks", reason `TracksMissing` while short) and an
  Audiobook's `partCount`. Issue's Mylar `state` and the leaves' HasFile and
  CutoffMet conditions went cold turkey (2026-11-10); the leaves skip idle
  items at start as Episode does (`rollup.LeafIdleAtStart`).
- **Every album track is attributed, never guessed** (D6): an album
  download's or folder's audio files are attributed to the selected
  release's tracks by their tags (disc and track number, recording id), then
  their name (`release.ParseTrackFileName`), then a lone file's one track;
  a file for no track or a second file for one track is `needsPerson`
  naming the track (`fileimport.AttributeTrack`; the rescan does the same
  and lists an unattributable file in `status.unmatched`). An audiobook's
  parts are ordered by `MediaFileSpec.partIndex` (tags' track number, else
  the name's part through `release.PartOf`, else path order). The probe
  records a music or audiobook file's format tags, sample rate and bit depth
  and a comic archive's or PDF's page count (`pkg/mediainfo/pagecount`, the
  import agent's alone; `ProbeVersion` 4, which re-probed every file once).
- **Naming is kind-neutral** (D7): `{PartNumber}`, `{PartCount}`, `{Special
  Version}`; `catalogctx.Album/Book/Audiobook/Issue` contexts and the
  `TrackFilePath`/`BookFilePath`/`AudiobookFilePath`/`IssueFilePath` rules
  under the container folder the import places by; the naming planner and
  rename actuator cover album tracks, books, audiobook parts and issues
  (`renameFiles` for every kind; `renameTranscoded` stays video-only).
  `{PartNumber}` is an audiobook part's place among its parts (1..n in the
  Audiobook controller's play order, `catalogctx.PartNumbers`; a lone part
  renders none), never `spec.partIndex`, which keeps its disc*1000+track
  ordering key (2026-11-10: a tagged disc 1 track 3 had imported as
  "Title (1003).mp3"); the import numbers an inspect's parts once the walk
  has found them all, and the loop in `mediafile.LoadNaming`'s audiobook
  case (`audiobookPartNumber`, over the Audiobook's MediaFiles through
  `mfindex.FilesOf`, number and count from one read); the naming planner's
  S12/MediaFile source (`app/remediation/naming/partwake.go`) wakes a
  part's siblings at low priority on any change of its mediaRef, partIndex
  or path, since a numbered part's path is the tie-break among equal keys
  (a disc rip without disc tags keys every disc's track 1 as 1001);
  `release.PartOf` reads parts 1..99. The audiobook page shows the same
  ordinal (never `spec.partIndex`). The rename actuator forgets a
  remembered collision after a minute (6760dc20), so a sibling whose
  target the renamed part vacated is tried again at its next pass instead
  of never. A manual
  assignment keeps the file's track attribution and part. `.fb2` and
  `.djvu` are books to `pkg/fsops`, probed without FFmpeg, but
  `pkg/quality`'s book ladder has no tier for them, so such a file is held
  for a manual import (owner's call).
- **A Comic is searched as a volume** (D10): the Comic key's pack-first
  search is its one season (`searchplan.VolumeNumber`,
  `status.volumeSearch`, a `SeasonSearchState`), its Issues wait for it and
  fold its answer, the grab stage reads the answered volume as a season
  answer and `pkg/decision` approves a volume-search release whose series is
  the comic's whatever its issue or year (`volumeRejection`); grabplan's
  coverage and lifecycle's supersede take an Episode or an Issue. Comic
  `spec.addOptions` (monitor mode, `searchForMissing`), `specialVersion`
  (`volumeAsIssue` numbering) and `nextPullDate`; Audiobook `addOptions`
  and `spec.authorRef`, linked once from its Book's Author. Since wave C
  (2026-11-10) a volume search is also a planned Search: the Search CEL
  admits `spec.season` 1 on a comic (`catalogv1.VolumeSearchSeason`;
  `SearchSpec.PlannedBySeries` covers a comic's grabBest one,
  `PlannedByComic` names it, `FansOut` the Search that fans out per child),
  the Comic key's season planner serves it as a Series' (trigger "volume",
  S31 wakes the Comic), and the ui's `actions.SearchVolume` and
  `InteractiveVolumeSearch` create it. The Search controller's half landed
  the same day (bd9f4d79): it reads the container by the Search's kind (a
  Comic gone is `ComicGone`), mirrors `status.volumeSearch` into the
  Search's `status.seasons` with the Series' Event reasons and comic-worded
  messages, and runs an interactive volume Search as one season task of
  the Comic (`SearchSpec.FansOut` decides the fan-out), so both buttons
  work end to end; a release naming an issue the comic lacks reads
  WrongItem in the modal's other-titles band, and a coverage rejection
  names the covered items' kind ("the release covers no issue of the
  comic"; `grabplan.CoverageRejections`' nouns, b903ea47).
- **Every kind's `status.path` is the folder the import places files in**
  (§5.5, wave F, 2026-11-10): one exported rule in `pkg/naming/catalogctx`
  -- `ItemFolder` (movie, series, artist, author, audiobook, comic: a pinned
  `spec.folder` joined as written on both sides, else the kind's preset,
  every component sanitized and held strictly under the root), `BookFolder`
  (an author's book under its Author's folder, a standalone one
  `{Book Title}`), `AlbumFolder` (the album segment below the Artist's
  folder) -- which the `*FilePath` functions join their file onto, so the
  controller's folder and the import's file path cannot diverge (a "?" in a
  title had put them one character apart; an agreement test per kind holds
  them). A preset that renders nothing degrades to the root; a known title
  the rule cannot render (only dots, or nothing once sanitized) reads Ready
  False reason `FolderUnrenderable` with `reassertKnownStatus` and a
  minute's requeue, never an endless backoff. A changed `status.path` moves
  no file: the sanitizer is its own fixed point (`pkg/naming` trims after
  the 255-byte cut as well as before), so a stale path renders the same
  file path and the rename planner's proposal does not move; the ui's
  folder, the delete's bound and the nfo stage's folder become the real one
  at the item's next pass.
- **A root scan creates Artists, Authors and Audiobooks from their folders,
  and tracks attribute by their tags whatever the folder depth** (wave G,
  D16-D20, §5.7, §9.7, 2026-11-11; owner's music root is 390 FLAT artist
  folders the old matcher refused file by file, and `scanapply` created
  movies and series only). The rescan agent (`rescan/identify.go`) resolves
  a first segment no item holds by identity first -- a `[mbid-…]`,
  `[olid-OL…A]` or `[ASIN B0…]` token in the folder's name, else the
  `MUSICBRAINZ_ARTISTID` up to five of its files agree on, validated by the
  gateway's lookup -- then by the provider's search read strictly: the hits
  whose `release.TitleNorm` equals the folder's, and exactly one at the top
  score among them (`SearchHit.Score`/`Disambiguation`, MusicBrainz's,
  carried since W7.0), else the folder is one `UnmatchedFile{folder: true}`
  with the hits as `Name (disambiguation) [id]` candidates (`no_match`,
  `ambiguous_title`, `lookup_failed`), its files counted in
  `scanprogress.AwaitingChildren` (per container kind; `AwaitingEpisodes` is
  gone), never guessed (amendment A1.5 as written; the never-guess comments
  in `rescan/match.go` and `nonvideo.go` were reworded). The walk proposes
  one `present` observation per folder carrying `schema.ItemCreate`
  (`musicBrainzID`/`openLibraryID`/`asin`+`region`, `folder` pinned,
  `Evidence: folder`) and the manager's applier (`scanapply/create.go`:
  `applyArtist`, `applyAuthor`, `applyAudiobook`, modelled on `applySeries`)
  creates it with the root's `defaults.qualityProfileRef` (required),
  `monitored`, `monitorNewItems` and `tags`, `addOptions{monitor: all,
  searchForMissing: false}`, an Author's `allowedLanguages: [en]`, an
  Audiobook's `authorRef` to the one Author of the name, under one lock per
  item; an ImportExclusion of kind `artist` or `author` (the delete dialog
  writes them now) refuses it, and a refused folder is listed as a folder
  row. **An Artist applied by SSA must send `metadataProfile` as an object**:
  the CRD defaults its type lists only when the object is present, and an
  empty profile accepts no album. Identification RPCs run under
  `IdentifyTimeout` (2 min) on their own goroutine while the walk heartbeats
  (they queue on the metadata agent's one MusicBrainz limiter beside the
  fan-outs the walk's creates start; a 10 s deadline failed fast). An
  Album's track listing (the lookup RPC, `album.Reconciler.syncTracks`) is
  answered from the gateway's metadata cache, where the Album's own refresh
  left the whole release list (`metadata.cachedAlbum`; a miss asks
  MusicBrainz and caches nothing), and a failed one holds that Album's next
  lookup back for as long as it has been failing, 30 s to 1 h, jittered
  (`album.ReleaseCache.Failed`; owner, 2026-11-15: 1,082 of talos's 1,092
  Albums retried it every 30 s on MusicBrainz's one request a second behind
  a 10 s deadline, almost every request was abandoned mid-call, and the
  music scan matched none of 3,249 files). An Artist's album list is
  cached by the gateway for a day after a fetch (`albumlist` keys,
  `metadata.lookupAlbums`) and a failed listing holds the Artist the same
  way (`lookuphold`, the rule both share; 28 of 145 Artists had no albums
  after five days). The Author and Comic fan-outs still retry their RPCs
  every 30 s. A music
  file is probed before it is matched, and `MatchAlbum(absPath, rel, tags,
  cands)` takes the Artist the first segment names (as written, CleanTitle
  or TitleNorm, so 宇多田ヒカル and !!! keep their albums) and the Album by
  the release-group tag, the release-id tag (`AlbumCandidate.ReleaseIDs`),
  the album folder at three or more segments, else the ALBUM tag (a DATE
  before the first release refuses; two hits `ambiguous_title`, none
  `no_child_item` naming the tag), a title compared as cleaned, then
  spelled loosely (spacing, "&" or "+", hyphens, a Roman numeral, interior
  articles: "19NaughtyIII", "SremmLife II", "Flesh and Blood"), then as one
  of the group's releases' own titles ("F-1 Trillion: Long Bed",
  `AlbumCandidate.ReleaseTitles`), then without an edition note
  ("(Expanded Edition)", "Deluxe", "CD", "(2015 SHM-SACD)", a vinyl or
  hi-res note "(180g 24/96)", a trailing "Chopped & Screwed ..."; never
  "(Live ...)" or a remix), then one side of a double A-side's "A / B",
  the first tier naming an album deciding (`albumTitleTiers`), a tag DATE a year before the album's
  first release still the album's (a pre-release's), and two albums of one
  title told apart by the one holding the file's track (`byTrackTitle`;
  owner, 2026-11-16: 230 of 386 held files, and Boavista's single and
  album). **An ALBUM tag naming a release group the artist's metadata
  profile leaves out creates that album** (owner, 2026-11-16: "Ensure
  that Unmatched has no remaining music entries"; the library is a
  person's picks, a few tracks of a compilation, mixtape or single):
  `rescan.tagAlbum` matches the file by MatchAlbum's own rules against the
  artist's every release group (the gateway's album-list cache, asked once
  a walk per artist), those an Album holds aside, and proposes one album
  create a walk (`schema.ItemCreate` kind album), which
  `scanapply.applyAlbum` makes as the fan-out would (`names.Album`, owned
  by the Artist) and unmonitored, never searched; its files await its
  tracks until the next scan of the folder (an artist's own folder scan is
  asked for once in its life, so a full root scan places them);
  the first file in walk order holds a
  track and a second is `duplicate_track` (the album's recorded MediaFiles
  read once per walk; two scans at once can still race, open). A track is
  never attributed by a number its title tag contradicts
  (`fileimport.AttributeTrackIn`, `TitlesAgree`: featured-artist credits,
  "&", apostrophes and spacing aside, any script): the scan asks the
  gateway once a walk for the group's every release and takes the one
  whose track at that number carries the title -- the file's own edition --
  then the title alone, and holds a file whose title no release has; and an
  album's selection goes, among releases holding the same files, to the one
  with fewer tracks (`album.SelectRelease`; without files still the most).
  On talos (owner, 2026-11-16) the flat scene rips were numbered against
  the largest edition each album selected (Old's 4x12" vinyl numbered per
  side, a 24-track deluxe, an a cappella edition): one matched track in six
  was held and one in twenty would have been renamed to another song. The
  title rule is `release.TrackTitlesAgree` (featured-artist and producer
  credits with a colon, a co-star or "Scored By", " F/ X", remaster and
  bonus-track notes, the album's own version notes -- "(Album Version
  (Explicit))", "(Explicit Version)", "(Original Mix)", never a single
  version, radio edit or session -- and "part"/"pt" aside; where the number
  agrees too, also a bracketed "(with X)", a bare " Ft X", "(Skit)" or
  "(from ...)", one part of MusicBrainz's "Interlude / Song", a title
  ID3v1 cut at 30 characters, the same words in another order and a typo
  of one or two letters in a title of eight or more,
  `release.TrackTitlesAgreeAtNumber`), and a selection tie on the
  same files goes to fewer tracks, then fewer media, then digital or CD
  over vinyl. **An album file's track is correctable**: MediaFile
  `spec.mediaRef`'s CEL rule keeps kind, name and keys immutable but not
  the track, a full scan attributes a recorded album file again within its
  recorded Album (`rescan.reattributeTrack`) and the applier writes the
  corrected track (the walk's holds follow, so a shifted album is put right
  in one pass); an incremental scan does not look again. And **an album file
  whose title tag is another song than its track is never renamed**:
  status.naming reads `TrackTitleMismatch` with no expectedPath
  (`mediafile.RenderNaming`), the guard for every attribution made before
  2026-11-16 (3,664 on talos, by number alone). A file whose
  Album lists no tracks yet is counted awaiting, and a container's owed
  folder scan waits for its children (`folderscan.Request.ChildrenSettled`:
  every listed Album with its document and a TracksSynced answer, a
  transient gateway failure being no answer, every Book with its document;
  `ChildrenWait` 24 h), since on talos the albums' track lists arrive over
  hours and a scan 30 s after the listing would attribute nothing.
  `RootFolder.naming.organizeFolders` (default false, needs `renameFiles`)
  lets the rename pass move a non-video file into its canonical folder
  strictly under its container's (`specwrite.RenameFile`'s `RenameOrganize`
  mode: `fsops.MkdirUnder`, the sidecars along, `PruneEmptyDirs` up to the
  container, a move into the container itself made, a symlink on the way a
  Collision), where the 2026-09-24 naming design's D5 still holds every
  video move and every default; a two-medium release renders Lidarr's
  `{Album Title}{ (Release Year)}/{Medium Format }{medium:00}/…` (`Disc NN`
  when the release records no format, `catalogctx.DefaultMediumFormat`;
  `MultiDisc` reads the media off `status.tracks` when the capped releases
  list lacks the selected release). The ui's Unmatched page draws a folder
  row with one "Add as artist/author/audiobook" button per candidate and a
  "Search Add New" link (both pin `spec.folder` and take the root's
  defaults and tags); one folder holds one item (`projection.FolderHolder`,
  `checkFolderFree`). Nothing scans without a request (no root has a
  scanSchedule) and nothing moves until both naming fields are set; the
  owner's call before the first music scan is `defaults.monitored` on the
  music root (spec Q9: every created artist's albums are monitored, so the
  wanted sweep eventually searches every missing album of 390 artists).
- **A supplementary provider's own page** (§9.3, §9.4, 2026-11-10):
  `metadata.PageProvider` (`Page(ctx, kind, ids)`, a Link of the provider's
  own type -- Hardcover's for an Author or Book Open Library answered,
  Metron's for a ComicVine-keyed Comic through its crosswalk) is asked by
  the gateway's `enrich` after the primary lookup when the document names
  no page of that type; `ComicMetadata.Links` carries it to the comic page
  and `IssueStatus.Volume` a chapter's tankobon volume (written by the Comic
  fan-out on every apply). `pkg/metadata.SchemaVersion` stays 8 (a raise
  refreshes every Movie and Series once), so a document already cached
  shows its new links at its next refresh or a per-item Refresh (owner's
  call).
- **Metadata parity** (§5.4): every non-video document carries
  `schemaVersion`, `language` (the primary provider's `spec.language`, else
  en; an Audiobook's `fetchLanguage`), ratings through `enrichRatings` over
  the kind's `RatingsProvider`s (sources musicbrainz, audnexus, hardcover,
  anilist, comicvine beside the video seven), images chosen per (type,
  language); MusicBrainz pages its browses (Muse's 175 albums came as 25),
  carries aliases and the artist credit; keyless `wikipedia` (an artist's or
  author's biography through its Wikidata sitelink when the primary's
  overview is empty) and `audible` (the Add New audiobook search in the
  root's region) are seeded like the other keyless types; ComicVine pages
  its issues and the fan-out writes each Issue's store date, overview,
  cover, credits and ids; Open Library gives an author's remote ids; an
  Artist's and an Audiobook's extended documents hold members/similar and
  narrators/authors. `pkg/metadata.SchemaVersion` stayed 8 so only the
  non-video documents (reading 0) refreshed once.
- **Music images are square, and album covers are the archive's 1200-pixel
  JPEG** (owner, 2026-11-16, "artist images are missing", "what's filling
  the artwork object store?"): an Artist's images come from fanart.tv
  alone (MusicBrainz has none; MetadataProvider `fanart`, Secret key
  `apiKey`), its square `artistthumb` offered as its poster too -- an
  artist fanart.tv has no portrait for takes its Wikidata image (P18) at
  Commons' ~1000-pixel rendition (`wikipedia.Client.Portrait`, a
  `PortraitProvider` the gateway asks with the artist's links when no
  artwork provider gave a poster; 111 of the 157 on talos) -- and the
  gateway keeps a URL once per image type (`imageRole`), not once, or the
  poster repeating the thumb's URL was dropped and no Artist had one; an
  Album's front cover is the Cover Art Archive's `thumbnails["1200"]`
  (`coverart.coverURL`), and the fetcher takes an original's
  `<id>-1200.jpg` rendition (`Fetcher.getCover`): 655 originals of up to
  18 MB were 2.5 GB of the 5 GiB `clustarr-artwork` store, which filled at
  03:30Z, after which every artwork Put failed (discard New), the fetch
  durable's 32 slots sat in its 1-6 h backoff (a store fault redelivers)
  and nothing was stored for any kind.
- **An anime's poster is the textless one with its logo drawn on it**
  (owner, 2026-11-17: "For anime posters, we should prefer posters with no
  text - where we will overlay the english logo at the bottom center",
  after github.com/elfhosted/PostersPlus). For an anime Movie or Series
  (`itemread.Anime`) whose metadata lists both a poster naming no language
  -- how TMDB, TheTVDB and fanart.tv list one without text -- and a logo in
  the titles' language (`status.metadata.language`, else English),
  `catalogartwork.Item.Sources` (which the artwork stage plans from, where
  it called `ResolveSources`) makes the poster a *titled* source: the
  untagged posters in order, at most six (`Titled`, `Source.Candidates`),
  the logo (`Source.Logo`, also the item's logo), and the poster
  `ResolveSources` chose as the one to fall back to (`Source.Plain`). Both
  or neither: without the pair the item keeps the poster it had. The
  owner's poster wins -- a `spec.artwork` override or a pinned image set
  (`spec.imageSetID`) -- and a set the mediux provider chose by itself
  yields its poster, which carries the set maker's title, keeping its other
  images. **"No language" is a provider's word, so the fetcher reads each
  candidate for text** (owner, same day: "follow the OCR pattern for
  finding posters with no text"; TMDB's first untagged poster for Your Name
  carries its handwritten title): `Fetcher.fetchTitled`
  (`app/catalog/metadata/artwork`) fits each candidate into the poster box
  and asks `Fetcher.Text` how much of it is text -- PaddleOCR's
  text-detection model on ONNX Runtime, `pkg/segments/textdet`, the one
  markers finds credits with, wired in the metadata agent as the caption
  agent wires its recognizer (`app/catalog/agent/metadata/postertext.go`:
  built at the first titled poster from `$ORT_LIB_PATH`, proven by `agent
  --self-check`). 3 thousandths of the pixels or more is a title
  (`titleTextPermille`; of 44 posters of three items the fourteen textless
  ones read 0-1, the untagged ones with a title 11-30, tagged ones 3-54,
  though a title drawn as artwork can read 0). The first candidate without
  text is drawn (`title.go`): a shade over the lower half, the logo trimmed
  of its transparent margins, fitted to 78% of the width and 24% of the
  height, 5% above the bottom edge, white when 85% of its opaque pixels are
  dark; when every candidate carries text the plain poster is stored as it
  is. Either is the poster *original*, so the rating overlay, the ui and
  Plex take it as any poster. The object records `clustarr.io/title`, the
  source's `TitleMark` -- `TitleVersion` and a hash of the logo, the
  candidates and the plain poster, the question rather than the answer --
  and `clustarr.io/titled` when the logo was drawn; `PlanFetch` judges a
  titled source by that mark alone, never by the URL and kind the object
  came from, since it cannot know which candidate carried text. Raise
  `TitleVersion` when the threshold or the drawing changes. What cannot be
  judged is not guessed: a logo that cannot be had, no detector
  (`ErrNoTextDetector`) or one that fails all fail the poster's fetch,
  which keeps the stored poster and rests the source for `FailureRetry`.
  The detector holds about 180 MiB outside the Go heap, so agent-metadata
  takes `AUTOMEMLIMIT` 0.60 in both installers, as the import and caption
  agents do.
- **Every Movie and Series takes the titled poster, its logo is TMDB's,
  and the poster it falls back to is in the titles' language** (owner,
  2026-11-17: "TMDB seems way more reliable for this", "We should always
  overlay the title image ourselves to standardize the positioning", "we
  can standardize on TMDB for english logos, still use mediux for season
  art, square art, episode art and backdrops (when found, else fallback to
  others)", "tmdb may also be used for falling back to english language
  posters when no xx/no language poster is found for overlay";
  SchemaVersion 9). A Livid Lady's Guide to Getting Even showed a Japanese
  poster and no logo: TheTVDB lists a Japanese ClearLogo alone (left out,
  not the titles' language), so the titled pair never formed, and its
  Japanese poster, one vote above two without text, led. What changed:
  - `Item.TitledPoster` is every Movie and Series whose owner chose no
    poster, no longer an anime alone. A pinned set's or a `spec.artwork`
    poster still wins; a set the mediux provider chose yields its poster to
    a titled one and keeps its background, season posters and titlecards.
  - A series asked TMDB for its tagline and keywords only:
    `tmdb.SeriesExtras` appends `images` to that one request
    (`include_image_language=<titles'>,null`, as a movie asks), and
    `enrich` (`withExtraImages`) puts TMDB's logos and posters in the
    titles' language ahead of the series provider's own and the rest --
    posters without text, backdrops -- after them (171 of 750 series had
    no logo; TMDB has an English one for 160 and an untagged one for 4).
  - Every image says the language of its text: `tvdb.Series` tags its
    posters, backgrounds and banners (`jpn` -> `ja`, none -> ""), and a
    film's own poster and backdrop take the tag TMDB's images list gives
    them (`listedLanguage`). Both were listed without one, so TheTVDB's
    Japanese poster and every film's English poster read as "no language"
    and were the first candidates tried for a logo, the text check alone
    turning them away.
  - The plain poster (`Item.Sources`) is the first provider poster in the
    titles' language when the provider's first is not, with no poster
    without text and as the titled source's `Plain`.
  **The text check stays**: of the first "no language" TMDB poster of 48
  anime series sampled that day, three carried a title (Chinese-release
  posters) and one a credits line. Not built: TMDB's series images are
  best effort like the keywords, so a refresh in which TMDB fails drops
  them until the next, and a series whose logo was TMDB's falls back to
  its plain poster for that long; **square art does not exist** -- the
  catalog has no image type for it, the mediux client reads posters,
  backdrops, season posters and titlecards only, and the Plex provider
  sends no `backgroundSquare` (`ui/plex/images.go`).
- **Sized originals are stored fitted** (owner, 2026-11-16: "We can greatly
  reduce the image cache by compressing jpegs and resizing images"): the
  fetcher scales a poster into 500x750, a fanart, thumb or screenshot into
  1280x720 and a banner into 758x140 (`catalogartwork.FitBox`; aspect kept,
  never up, so a square album cover is 500x500) and encodes it as a JPEG at
  quality 85 (`Fetcher.fit`); a transparent one stays a PNG, an image that
  fits is encoded again only for a tenth saved, one over 40 megapixels is
  stored as fetched, and logos, clearart, disc art and headshots are
  untouched. Each such object records `clustarr.io/fit`
  (`catalogartwork.FitVersion`), and the item key's plan fetches an
  original without the current one again (`Object.Unfit`), so the first
  deploy, or a raised version, refits the store as items are next planned.
- **Plex reads music metadata from NFO files, not a provider** (ADR-0022,
  D12, D13): every PMS build through 1.43.5.11079 refuses a custom provider
  declaring artist/album/track (types 8, 9, 10: HTTP 400 "The provider
  supports unsupported metadata types", spike §7.6), so the `/plex/music`
  and `/plex/audiobooks` roots are built to the inferred shapes behind
  `--plex-music-provider` (chart `ui.plex.musicProvider`, false) and PMS
  1.43.5.11029's NFO reading is the live route: `pkg/nfo` renders Kodi-shape
  `artist.nfo`/`album.nfo` (an Author as the artist, an Audiobook as the
  album), the item key's `nfo` stage (`app/remediation/nfo`, after artwork,
  field manager `catalog-nfo`, `status.nfo{hash,path,files,writtenAt}`)
  plans a write when the rendered hash moves, and the import agent's
  `import-nfo` consumer writes the bytes it was handed beside the music with
  `folder.jpg`/`artist.jpg` copied from the artwork store, never over a file
  it did not write (the task's `Owned` names and the record's last answer
  are the allow-list), answering in the `clustarr-nfo` record.
  `RootFolder.naming.writeNFO: false` plans nothing and removes nothing. The
  Plex side (cluster-plex: `type: artist` sections on the Plex Music Scanner
  with the NFO agent and "Prefer local metadata", §10) is not built yet.
- **The ui is at parity** (§9, W4, 2026-11-10): six library tabs, a page
  per kind under its literal route, kind-aware file cells, Add New for
  every tab and the Edit dialog's kind blocks -- the UI section above,
  "The four non-video libraries' pages".
- **Deploying it:** the CRDs go on by hand first, as every CRD change on
  talos does (wave G: rootfolders, libraryscans, importexclusions); `ProbeVersion`
  4 re-probes the library once through the low lane; the nfo stage writes
  into every music and audiobook root at the first pass.

