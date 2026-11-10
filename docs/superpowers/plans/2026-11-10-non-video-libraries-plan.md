# Non-video libraries: implementation plan

The task list for `docs/superpowers/specs/2026-11-10-non-video-libraries-design.md`
(the spec; its D-numbers and §-numbers are cited below). Every task lands on
`main` by a pathspec commit of its own files (`git commit -m '...' -- <paths>`,
new files `git add`ed first), gated by `go build ./api/... ./app/... ./pkg/...
./cmd/... ./internal/...` plus `go build ./ui/...` when the ui tree builds,
`go vet` and plain `go test -count=1` of the touched packages (no
`KUBEBUILDER_ASSETS`: the envtest run is the one final run, owner
2026-10-08), `make brand-gate`, and `make generate manifests` clean when
`api/` moved. Tasks in one parallel group own disjoint files; a task that
needs a file another group member owns says so and waits for the next
serial step. Nothing here pushes; the controlling session pushes.

Status: W0 done (e6fc628d, bc888053). The maps the Understand phase wrote
(`understand-{api,controllers,metadata,ui,plex}.json`, file:line facts and
gaps per subsystem) are in the controlling session's scratchpad and are
handed to each task's prompt.

## W1: the manager (parallel; disjoint packages)

### W1.1 Artist and Author join the remediation loop (D2, §5.1)

Owns `app/remediation/` (every non-test file but `search/`, `naming/`,
`rename/`, `nfo/`), `app/catalog/controller/artist/`,
`app/catalog/controller/author/`, `app/catalog/manager/register.go`,
`app/catalog/metadataset/`, `app/remediation/metadata/stage.go`'s and
`app/remediation/artwork/stage.go`'s kind lists.

1. `itemkeys.go`: `KindArtist`, `KindAuthor` as container keys (beside
   `KindSeries`, `KindComic`); `KeyKind.Container()` true for them; the
   MediaKind map.
2. The container pass (`items.go`, `itemstage.go`, `itemeffects.go`,
   `itemsources.go`, `indexes.go`): an Artist/Author pass runs child sync,
   the metadata and artwork stages, the `nfo` stage (W3.3's package;
   register the call once it exists, else a TODO naming W3.4), the rollup
   apply. Child sync is the fan-out moved from `artist/reconciler.go` and
   `author/reconciler.go` into pure functions the pass calls (`artist.Fanout`,
   `author.Fanout` already exist in `fanout.go`), with a `childSyncMemo`
   (`app/remediation/childsync.go`: in-process, keyed by UID, due when spec
   generation, `metadata.refreshedAt` or add options moved since the last
   complete fan-out; the Series' `episodeSyncMemo` pattern) shared by Comic
   (W1.5 calls it). Sources: an Artist wakes on its own spec/metadata, on
   its Albums' rollup fields (through the `albumArtistRef` index), on the
   RootFolder defaults; the Album item key wakes its Artist as Episode wakes
   its Series. A folder scan after the first complete fan-out
   (`folderscan.Probe`, as `manager/items.go:65-68` does for Series).
3. Delete the standalone reconcilers' registration (`register.go:369-391`)
   and the artist/author path of `metadataset.Incorporator`; keep each
   package's pure functions (fan-out, path, rollup, itemevent). Metadata
   publication goes through `itempass.Admit` and the ledger like Album's.
4. `app/remediation/metadata/stage.go:66-70` and `artwork/stage.go:69-73`,
   `artwork/sources.go:44-51`: Artist and Author in the kind lists.
5. Tests: `TestEveryItemKindIsALoopKey`, the child-sync memo table, the
   Artist/Author pass envtests written (they skip without assets).

### W1.2 Rollups, phases, cutoffReason, events, SkipInitial (D4, D5, D14, §5.2)

Owns `app/catalog/controller/rollup/`, `app/catalog/controller/itemstatus/`,
`app/catalog/controller/{album,book,audiobook,issue}/` (every file; W1.1
owns artist/author, W1.5 comic), `app/catalog/controller/movie/report.go`
(to generalise, not to change Movie's behaviour).

1. `rollup.LeafPhase(in LeafInputs) (commonv1.LeafPhase, cutoffReason
   string)`: Movie's precedence (spec D4); inputs monitored (own and
   container's -- W1.3's `itemread.Monitored` supplies it; until then the
   item's own), metadataReady, released (release date passed, or no date),
   hasFile, cutoffMet, profileResolved, tracksMissing, downloading,
   pendingGrab. Table-driven test over every bucket.
2. `rollup.Container(children ...)` for the three containers: phase
   (Pending until metadata, Unmonitored, Ready), missing/downloading/
   cutoff-unmet counts, next/previous release date, writing the
   `ContainerRollup` fields beside the kind's own counts.
3. Each leaf reconciler/pass writes `phase`, `cutoffReason`, `hasFile`,
   `fileQuality`, `fileFormatScore` (Book: freeze from the MediaFile's spec
   as Issue does; remove `book/filestate.go:36-44`'s extension fallback),
   `fileFormat` (Book, Issue), Album `trackSummary` and `hasFile`,
   Audiobook `partCount`; the retired conditions are dropped on the first
   pass (`retiredLeafConditions`, the Issue's `retiredIssueConditions` made
   shared); `itemstatus/apply.go:17-19` lists eight kinds; `rollup/item.go:30`
   and `rollup/doc.go` corrected.
4. Events: `report.go`'s `normal`/`warn`/phase-edge recording made
   kind-neutral (`rollup.Reporter` over `client.Object`), called from every
   leaf and container reconcile for phase and condition edges.
5. `rollup.Watch.SkipInitial` for Album, Book, Audiobook and Issue
   (`leaf.idleAtStart`: unmonitored, or Imported with no entry in flight
   and no pending grab), as `episode/reconciler.go:186-215`.
6. Tests per step; the album, book, audiobook and issue `phase_test.go`
   rewritten over `rollup.LeafPhase`.

### W1.3 Monitoring, pack rules, the volume search (D3, D10, §5.3)

Owns `app/catalog/itemread/`, `app/catalog/controller/backlogsearch/`,
`app/catalog/grabplan/`, `app/catalog/searchplan/`, `app/grab/lifecycle/`,
`pkg/rankscore/`, `app/remediation/search/` (every file),
`app/catalog/worker/search/` (the season task for a Comic volume).

1. `itemread.Monitored` reads the Artist for an Album and the Author for a
   Book (`itemread.go:411-439`); `backlogsearch/controller.go:304-318` the
   same; `itemread.HasFile` true for an Album with any track file
   (`itemread.go:577-596`, reading `status.hasFile`).
2. `grabplan/coverage.go:168,177,194,292` and `lifecycle/supersede.go:42-48`
   accept `Ref.Kind` Episode or Issue (`refkind.Child(kind)`); `grabplan`'s
   `Holds` and `rankscore.WholeSeason` apply to Series and Comic; part sets
   stay Movie-only.
3. `searchplan`: `DecideSeasons`, `PackFirst`, `SeasonFacts`, `Automatic`
   take a `ContainerView` whose seasons are, for a Comic, the one volume
   (number 1, every monitored Issue); the search stage (`search/stage.go`)
   runs it for `KindComic` and mirrors it in `status.volumeSearch`; an Issue
   with its volume queued or searching waits (`View.SeasonPackFirst` for
   Issue); the Comic's answer folds into each Issue's `lastSearchedAt` and
   `lastLiveSearchAt`. The search worker's season task for a Comic queries
   the volume (title, year, no season number) through the existing
   container query path.
4. Tests: monitoring tables for Album/Book; coverage and supersede tables
   with Issues; the volume search's `DecideSeasons` table; the search
   stage's Comic wake test.

### W1.4 Naming and rename for every kind (D7, §5.5)

Owns `app/remediation/naming/`, `app/remediation/rename/`,
`app/catalog/controller/mediafile/naming.go`, `pkg/naming/` (only if a
preset needs a token the kinds lack: `{Part}` for audiobook parts,
`{Special Version}` for issues).

1. `naming/adapter.go:57-60`: plan for a MediaFile whose owner is Album
   (track: `TrackFile` under the album folder, context from the Album's
   track by `mediaRef.track`), Book (`BookFile`), Audiobook (`AudiobookFile`
   with `spec.partIndex`), Issue (`IssueFile` with the Comic's
   `specialVersion`).
2. `mediafile/naming.go:216,234` `namingOwnerOf` and
   `rename/actuator.go:267-291` `rootFolderRef`: resolve Album through its
   Artist, Book through its Author or own `rootFolderRef`, Issue through its
   Comic, Audiobook its own.
3. `RootFolder.naming.renameFiles` read for every kind; `renameTranscoded`
   stays video-only.
4. Tests: a naming table per kind (the presets' goldens), the owner/root
   resolution table.

### W1.5 Comic and Audiobook parity (D10, D11, §5.1, §5.3)

Owns `app/catalog/controller/comic/` (every file but the Issue-field writes
in `fanout.go`/`reconciler.go:498-547`, which W2.4 owns), `app/catalog/
controller/audiobook/` is W1.2's -- so this task's audiobook work goes in
`app/catalog/controller/audiobook/addoptions.go` and `parts.go` (new files)
and `app/remediation/downloads/` for the parts' ordering.

1. Comic `addOptions`: applied once (`status.addOptionsApplied`), the
   monitor mode choosing which Issues start monitored (Mylar's), and
   `searchForMissing` asking the Issues' searches as Series does;
   `spec.kind: manga` and `spec.specialVersion` read as D10 says (naming
   token; `volumeAsIssue` numbering); `nextPullDate` from the next Issue's
   store date; `syncIssues` behind W1.1's `childSyncMemo` (import it; if
   W1.1 has not landed, a local memo of the same shape, replaced in W6).
2. Audiobook `addOptions.searchForMissing` once; `authorRef` resolved from
   the Book's author when `bookRef` resolves; `fileRefs` ordered by
   `spec.partIndex`, `partCount`; `status.cutoffReason` through W1.2's
   `rollup.LeafPhase` once it lands (until then the existing phase).
3. Tests per rule.

## W2: metadata (W2a serial, then W2b parallel, then W2c serial)

### W2a Model and interfaces (one agent)

Owns `pkg/metadata/model.go`, `pkg/metadata/provider.go`,
`pkg/metadata/refresh.go`, `pkg/metadata/registry.go`.

1. Model fields the clients will fill (spec §5.4): Artist begin/end/
   country/aliases/links/biographySource; Album genres, externalIDs,
   label(s), catalogNumber, artistCredit, overview, ratings; ReleaseSummary
   title/disambiguation/catalogNumber; Author born/died/links; Book
   subtitle, languages, subjects, pageCount, publisher, links; Audiobook
   tags, isAdult, ratings; ComicVolume genres, status, altTitles, endYear,
   originalLanguage, sortTitle, volumeNumber; Issue overview, coverURL,
   storeDate, credits, sourceIDs.
2. `AudiobookProvider.Search(ctx, query) ([]AudiobookHit, error)`,
   `ComicProvider.Search` (ComicVine volume search, MangaDex manga search),
   a `BiographyProvider` interface (wikipedia), `ChaptersProvider` surfaced
   through the registry's Audiobook lookup.
3. `pkg/metadata.SchemaVersion` documented as every kind's; `Outdated(doc)`
   helpers for the six non-video documents.

### W2b Clients (parallel; each owns its client directory and fixtures)

- **W2b.1 MusicBrainz, Cover Art Archive, fanart.tv**
  (`pkg/metadata/clients/{musicbrainz,coverart,fanart}`): `Albums()` pages
  release groups by offset with the primary-type filter; `artistIncludes`
  gains annotation, url-rels, aliases, ratings; `mapArtist`/`mapAlbum` fill
  the W2a fields (life-span `ended` -> status); fanart `artistthumb` ->
  poster and thumb, `musicbanner` -> banner, `hdmusiclogo` -> clearLogo.
  Recorded fixtures for a paged artist.
- **W2b.2 Wikipedia** (`pkg/metadata/clients/wikipedia`, new): Wikidata
  entity -> enwiki sitelink -> REST summary; `Config.Limiter` injected,
  capped bodies, `ErrNotFound`; fixtures recorded from the live API for one
  artist and one author.
- **W2b.3 Audnexus and Audible** (`pkg/metadata/clients/{audnexus,audible}`):
  Audnexus `Chapters()` in the lookup path, abridged/isbn/tags/isAdult/
  ratings mapped, `Authors()` by ASIN; the Audible catalog search client
  (keyless, per region, `response_groups=product_desc,contributors,media,
  product_attrs,series`) answering `Search`; fixtures recorded.
- **W2b.4 ComicVine, Metron, MangaDex** (`pkg/metadata/clients/{comicvine,
  metron,mangadex}`): `Volume` requests `image`; `Issues()` pages by
  offset; issue `description`, `image`, `store_date`, `person_credits`
  mapped; Metron and MangaDex fill the same fields where they carry them;
  MangaDex honours the language; `Search` for volumes/manga.
- **W2b.5 Open Library and Hardcover** (`pkg/metadata/clients/{openlibrary,
  hardcover}`): Book subtitle, languages, subjects, pageCount, publisher,
  `Edition.IsEbook`; Author born/died/links (Wikidata id when present);
  Hardcover ratings; Hardcover `Books()` by Hardcover author id.

### W2c Gateway (one agent)

Owns `app/catalog/metadata/` (every file), `app/catalog/controller/comic/
fanout.go` and `reconciler.go:498-547` (the Issue fields), `app/remediation/
metadata/stage.go`'s outdated check, `ui/actions/add.go`'s search kinds are
W4c's -- not here.

1. Every non-video builder in `patch.go` sets `schemaVersion`, `language`
   (`fetchLanguage` for Audiobook), `ratings` (`enrichRatings` with the new
   sources, `target.go:148-166` knownRatings), images through
   `selectImages` per (type, language), and the W2a fields.
2. The outdated-refresh path (Movie/Series reconcilers') moves into the
   metadata stage for all eight kinds.
3. Biography enrichment: annotation else wikipedia through the url-rel;
   Author likewise.
4. `rpc.go` search verbs: `comic` and `audiobook`; `writeExtended` for
   Artist (members, similar) and Audiobook (narrators/authors as people).
5. The Comic fan-out writes Issue overview/coverURL/storeDate/credits/
   sourceIDs under `catalog-fanout`; `foreignLeaves` extended for any new
   catalog-owned leaf inside a metadata struct.
6. Tests: builder goldens per kind; ratings enum round-trip; the fan-out's
   managedFields assertion.

## W3: import, probe, NFO (parallel)

### W3.1 The probe (D9, §6)

Owns `pkg/mediainfo/` (every file), `test/data/mediainfo/` fixtures.

1. `native/probe.go` + `map.go`: format tags into `MediaInfo.Tags` (ID3,
   Vorbis and iTunes spellings of title, artist, album_artist, album,
   track `N/M`, disc `N/M`, date, the four MusicBrainz ids), audio
   `sampleRateHz` and `bitDepth` (bits per raw sample), `pageCount` for
   `.cbz/.cbr/.cb7/.cbt` (archive image entries, listing only) and `.pdf`
   (a bounded `/Type /Page` scan); `ProbeVersion` 4.
2. Fixtures: a tagged FLAC and MP3 (generated with ffmpeg in the fixture
   step, as the video fixtures are), a two-image CBZ, a two-page PDF.
3. Tests: `TestAnAudioFileProbesWithoutVideo`, `TestTagsAreBounded`,
   `TestPageCountOfACBZ`, the native suite under `make test`'s variables.

### W3.2 Track attribution and parts (D6, D11, §6)

Owns `app/import/` (every file), `pkg/release/track.go` and `part.go`
(new: `ParseTrackFileName`, `ParsePartNumber`), `app/import/importplan/`.

1. `fileimport.resolveAlbum` + `inspectNonVideo`: probe every audio file of
   an album download, attribute by tags' (disc, number), else the name
   (`NN - Title`, `D-NN Title`, `DNN`), else the recording id;
   `InspectedFile.Track`; execute writes `mediaRef.track`; a file for no
   track or a second file for one track is `needsPerson` naming the track;
   the rescan (`rescan/nonvideo.go`) attributes the same way.
2. Audiobook parts: `spec.partIndex` from tags' track number, else the
   name's part number, else path order; the execute and the rescan set it.
3. Book quality frozen at import (`fileFormat`, `fileQuality`,
   `fileFormatScore` on the MediaFile spec as for Issue).
4. Tests: attribution table; part ordering; book freeze.

### W3.3 pkg/nfo, the import-nfo handler, the nfo stage (D13, §8)

Owns `pkg/nfo/` (new), `app/import/worker/nfo/` (new), `app/import/agent/
register.go` (the consumer line), `app/remediation/nfo/` (new stage
package; its registration in the item pass is W3.4).

1. `pkg/nfo`: `RenderArtist(a *Artist, albums []Album) []byte`,
   `RenderAlbum(a *Album, artist *Artist) []byte`, `RenderAuthorAsArtist`,
   `RenderAudiobookAsAlbum(ab, author)`; the element sets of spec §8.1
   exactly, UTF-8, `standalone="yes"`, elements omitted when empty; `Hash`
   over documents and image digests; `Files(kind)` names (`artist.nfo`,
   `artist.jpg`, `album.nfo`, `folder.jpg`); goldens for an artist, a
   two-disc album, an author, an audiobook.
2. `app/import/worker/nfo`: the `import-nfo` handler: `fsops.AtomicWrite`
   each file (mode 0644, under the root's UMASK), fetch each image from the
   artwork object store read-only, write only names the record's last
   answer lists or the task names, heartbeat while fetching, answer the
   `NFORecord` through `records.Answerer` (`pkg/records`), `Superseded`
   checks by seq; registered in `register.go` beside the recycle handler.
3. `app/remediation/nfo`: the planner (`Plan(item, children, root,
   artwork, record) Decision`: write when `status.nfo.hash` differs and the
   folder exists and the root's `writeNFO` is on; `NFOWriteTask` built
   with the rendered bytes and image object names; records incorporation
   into `status.nfo`), following `app/remediation/imageset`'s shape.
4. Tests: goldens; the handler against membus and an in-memory object
   store; the planner's decision table.

**Done 2026-11-10 (steps 1-3 and W3.4's registration for Album and
Audiobook):** `pkg/nfo` (f54c285c), the `import-nfo` consumer (8a7b540c,
8d6152de), `app/remediation/nfo` with `remediation.StageNFO` last in
`StageOrder`, `k8s.ManagerCatalogNFO` and the stage registered in
`app/remediation/manager`; `Applies` names Album and Audiobook and gains
Artist and Author when W1.1 makes them keys (`nfo.Artist` and
`nfo.AuthorAsArtist` are ready for it).

### W3.4 Wire the nfo stage (serial, after W1.1 and W3.3)

Owns `app/remediation/itemstage.go`, `itemeffects.go`, `items.go` lines the
stage needs. Registers the nfo stage in the Artist, Album, Author and
Audiobook passes (Author and Audiobook only under an audiobook root);
envtest written, unit tests of the pass's stage order.

## W4: the ui (W4a serial, W4b parallel, W4c serial)

Rules every task obeys: shadcn-templ components only (`.claude/skills/
shadcn-templ/SKILL.md`), templ formatted with `go run
github.com/a-h/templ/cmd/templ@v0.3.1020 fmt <file>` (never the PATH
binary; format only the files you touch), modals as DefaultOpen dialogs
swapped into `#<kind>-modal`, self-polling panels on plain `outerHTML`
swaps, children listed through a reader field index, no raw `<input>`/
`<select>`/`<button>`, hidden inputs and forms excepted. The library
pages (`ui/projection/library*.go`, `ui/library_view.go`,
`ui/library_page.go`, `ui/views/library*.templ`) were being reworked by
another session on 2026-11-10; W4a reads their landed state first.

### W4a Shared infrastructure (one agent)

Owns `ui/projection/library.go` (Tab constants, `Tabs()`, `RootFolderKinds`,
`describeLibraryItem`, `ProviderID`, `containerStatus`), `ui/reader.go`
(indexes `albumArtistRef`, `bookAuthorRef`, `issueComicRef`,
`audiobookAuthorRef`), `ui/routes.go` (one line registering
`s.libraryKindRoutes(mux)` from a new `ui/routes_library_kinds.go` that
each W4b task appends to), `ui/views/helpers.go`, `ui/views/detail.templ`
(`actionBar` per kind, `providerLinks` logos), `ui/static/providers/`
(MusicBrainz, Discogs, Wikipedia, Open Library, Hardcover, ComicVine,
Metron, MangaDex, AniList, MyAnimeList, Audible, Audnexus), `ui/views/
filecells.templ` (new: `FileCells` for video/audio/book kinds),
`ui/library_view.go` (`statusLabel`, `hasFolder`, year labels),
`ui/library_find.go` (id prefixes).

### W4b Pages (parallel; each owns its own files)

- **W4b.1 Music**: `ui/artist.go`, `ui/album.go`, `ui/album_search.go`,
  `ui/views/artist.templ`, `ui/views/album.templ`, `ui/views/album_rows.go`
  (spec §9.2: albums table grouped by type with status cells and two
  searches; album page with tracks table, files, neighbours; the album
  search modal with music columns); routes appended to
  `routes_library_kinds.go` (`/library/{ns}/album/{name}`,
  `/album/{name}/details`, `/album/{name}/search/interactive`).
- **W4b.2 Books**: `ui/book.go`, `ui/book_page.go`, `ui/views/book.templ`,
  `ui/views/book_page.templ`, `ui/author.go` (hero facts) (spec §9.3: the
  book page with editions, files, neighbours in series order; the Author
  hero facts and logos; a standalone Book's page).
- **W4b.3 Comics**: `ui/comic.go`, `ui/issue.go`, `ui/issue_page.go`,
  `ui/views/comic.templ`, `ui/views/issue.templ`, `ui/views/issue_page.templ`
  (spec §9.4: issues table, issue page, volume search button and badge,
  the issue search modal).
- **W4b.4 Audiobooks**: `ui/audiobook.go`, `ui/views/audiobook.templ` (spec
  §9.5: hero, parts table, chapters, files, the audiobook search modal).

### W4c Shared actions and dialogs (one agent)

Owns `ui/add.go`, `ui/actions/` (every file), `ui/views/add.templ`,
`ui/views/edit.templ`, `ui/views/delete.templ`, `ui/views/library_find.templ`,
`ui/routes.go`'s `handleLibraryItem`/`handleSetMonitored`/`neighbours`
dispatch, `ui/views/downloads.templ`'s and `ui/dashboard.go`'s owner links,
`config/rbac/ui_role.yaml` and the chart copy, `test/guards/ui_rbac_test.go`.

1. Add New: Comics tab (ComicVine + MangaDex search), Audiobooks tab
   (Audible catalog), Books tab adds Author or standalone Book;
   `AddableKinds`, `buildItem`, `justAdded`, `Grants()` (create on comics,
   audiobooks, books), the role and chart copy.
2. Edit dialog kind fields (artist/author metadata profiles, comic kind and
   special version, audiobook region/author/book links; standalone Book
   editable); Delete lists files for every kind.
3. Owner links from Downloads and the Dashboard open the album, book,
   issue and audiobook pages; `handleSetMonitored` row replies for Album,
   Issue, Audiobook.
4. Markup tests per page; a browser check of every new modal (CLAUDE.md).

## W5: the Plex roots (serial)

### W5.1 Registration spike (no cluster, no talos)

Run `plexinc/pms-docker` (1.43.5.11079 or the newest beta tag), unclaimed,
with a stub provider on the host serving the `/plex/music` root response
declaring types 8, 9, 10; `POST /media/providers/metadata?uri=`; record
whether PMS accepts it, and if so bind a music library (`type: artist`,
Plex Music Scanner) to an agent built on it with two tagged FLAC files and
capture every request PMS makes (match bodies, metadata, children,
images). Write the findings as an "as built" note under spec §7.6. Then
tear the container down.

**Done 2026-11-10 12:50Z:** PMS 1.43.5.11079 refused every declaration of
8, 9 or 10 (spec §7.6's as-run note); W5.2 and W5.3 proceed behind the flag
and are the last implementation group.

### W5.2 Roots, index, keys (§7.1, §7.2, §7.5)

Owns `ui/plex/` and `ui/projection/index.go`, `ui/searchart.go`'s host
allowlist, `internal/cli/ui` and the chart for the flag. `rootDef.music`,
the two roots behind `--plex-music-provider` (chart
`ui.plex.musicProvider`), `ratingKeyPattern` and `ParseRatingKey` for the
three grammars, `Index` lists and maps for Artist/Album/Author/Audiobook,
`ByMBID`, `ByASIN`, `albumsByArtist`, `audiobooksByAuthor`, the file-name
rule for album and audiobook MediaFiles; `plex.metadataProviders` uris
commented out with the reason; `root_music.json`, `root_audiobooks.json`
goldens, the 503 loop over four roots.

### W5.3 Match, metadata, children, images (§7.3, §7.4)

Same owner, after W5.2: `match.go` types 8, 9, 10 (both roots), `metadata.go`
builders for artist/album/track (and the audiobook mapping), `children.go`
artist->albums, album->tracks, lazily paged grandchildren, `images.go`
resolution for the four kinds, `/extras` empty, goldens per type,
`TestMusicRootsRegisterOnlyUnderTheFlag`.

## W6: records (serial, last)

CLAUDE.md (the Services table's `import-nfo`, the UI section's four tabs
and pages, the Plex paragraph, the Transcoding section's file-kind guard),
`docs/adr/00NN-nfo-files-written-by-the-import-agent.md` with the index,
the spec's "as built" notes per wave, `test/e2e/nonvideo_test.go`
scenarios 9-11 extended (written, not run), the remaining-work plan's
items closed (`:1265`, `:1268`, `:1285`), the W1.5 local memo replaced by
W1.1's if both landed.

## Order and gates

```
W0 ──► W1.1 W1.2 W1.3 W1.4 W1.5 ─┐
   ├─► W2a ──► W2b.1..5 ──► W2c ─┼─► W3.4 ──► W4a ──► W4b.1..4 ──► W4c ─┐
   └─► W3.1 W3.2 W3.3 ───────────┘        └─► W5.1 ──► W5.2 ──► W5.3 ──┼─► W6
```

Each group's commits are pushed by the controlling session once the whole
group's gate is green on the merged tree (`go build ./...`, `go vet` and
`go test -count=1` over every touched package, `make brand-gate`, `make
generate manifests` clean). Review (adversarial, per group) runs on the
merged tree before the push: correctness against the spec's D-decisions,
the SSA complete-declaration rule for every new status field, the
one-writer rule, bounded lists and strings, and no agent writing a
Kubernetes object.
