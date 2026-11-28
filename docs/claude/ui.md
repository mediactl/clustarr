# Clustarr record: UI

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## UI

The library's media types -- Movies, TV, Music, Books, Comics, Audiobooks
(six since 2026-11-10, `projection.Tabs()`) -- are the Library
entry's sub-menu in the sidebar (shadcn-templ `sidebar.MenuSub` under the
Library `sidebar.MenuItem`, since 2026-09-24; before that a tab strip in
the top bar). Each sub-entry swaps the page body through htmx so the
sidebar stays put.
**The four non-video libraries' pages are the movies' and TV's** (owner,
2026-11-10, "just like we did the movies and TV"; the design's §9, built as
plan W4a-W4c by ten agents in wave B): the Music tab lists Artists, the
Books tab Authors and standalone Books, the Comics tab Comics, the
Audiobooks tab Audiobooks, each card carrying the container rollup's status
(`projection.containerStatus`: downloading, missing, cutoff-unmet,
downloaded, unmonitored, so the Missing and Wanted presets match
containers) and its provider id (`ProviderID`: a Comic's `<source>:<id>`,
an Audiobook's `asin:`, a Book's `olid:`, which the typeahead matches by
bare id or under its own prefix). Every kind has a page under its literal
route, which beats the shared `/library/{ns}/{kind}/{name}` (`routes.go`'s
`kindPage` sends the kind there however it is reached): an artist's
(`ui/artist.go`) with Lidarr's albums table grouped by type and the album
page (`ui/album.go`: the tracks table, a music-column search modal); an
author's with its hero facts and the book page (`ui/book_page.go`:
editions, files, neighbours in series order; a standalone Book is added,
edited and shown from its own card); a comic's (`ui/comic.go`) with Mylar's
issues table, the volume search button and the `status.volumeSearch`
badge, and the issue page (`ui/issue_page.go`, its ComicVine or MangaDex
cover through `/art/search`); an audiobook's (`ui/audiobook.go`) with its
parts and chapters. Each child is listed through a reader field index
(`ui/reader.go`: `AlbumArtistRefField`, `BookAuthorRefField`,
`IssueComicRefField`, `AudiobookAuthorRefField`; a Book's and an
Audiobook's `spec.authorRef` are not selectable at the apiserver, so those
two work on the ui's cache alone), never a List filtered in memory. The
Files table is kind-aware (`views.FileHeads`/`FileCells`: video cells for
a movie or episode, codec, bitrate and sample rate for an album or
audiobook, format and pages for a book or issue), the action bar is
composed by name (`views.ItemActions` over `views.Action` slots;
Interactive only for a leaf), and the provider logos under
`ui/static/providers` cover MusicBrainz, Discogs, Wikipedia, Open Library,
Hardcover, ComicVine, Metron, MangaDex, AniList, MyAnimeList, Audible,
Audnexus and Goodreads (Simple Icons where CC0, else drawn). Add New
serves every tab: Comics (ComicVine, then MangaDex; a hit's source badge
names the Comic's immutable `spec.source`), Audiobooks (Audible, in the
root's `defaults.region`) and on the Books tab an Author or a standalone
Book (`?kind=book`); a page whose item Add New just created answers the
"being added" page until the library view lists it
(`notFoundUnlessJustAdded`; never an author's book, which Add New does not
make). The Edit dialog fetches each kind's block as it opens
(`GET .../edit/fields`, `ui/editfields.go`: an artist's metadata profile
and monitor-new-albums, an author's languages, pages and skips, a comic's
kind, special version and monitor-new-issues, an audiobook's region and
its Author and Book links, a standalone book's profile and root) and saves
a kind's fields only under the block's "fields" marker, so a save before
the block lands leaves them alone; the Delete dialog counts a container's
files from its children's MediaFiles (`containerFiles`, one cached Get per
file). `--plex-music-provider` reaches the Plex roots
(`ui.PlexOptions.MusicProvider`, off). Since wave C (2026-11-10, the
same day): **one interactive-search panel serves every kind** --
`views.SearchPanel` carries the Search's kind and `SearchPanelView` draws
that kind's release table (`releaseTableFor`, `ui/views/search_tables.templ`:
the video columns, the album's music columns, the audiobook's with its
estimated bitrate, the format columns for a book or issue), every modal
polling and grabbing through the shared `/searches/{ns}/{name}` and `/grab`
routes, the video markup byte-identical; the children fragment is gone
(`ui/children.go`, `views.ChildrenPage`); an Album is editable
(`actions.EditItem`: monitored, the profile or the artist's, `anyReleaseOk`,
the release pin) and a standalone Book is a folder kind (Refresh & Scan,
Rename and Edit on its bar; an author's book renders no Edit); a container
page reads its children's files up to `maxContainerFiles` (500; a series
page renders in 4 ms at 50 files, 13 at 500, 3 at 1,100 with nothing read)
and the Delete dialog counts a series from its rollup past that; a Comic's
"Volume" and "Interactive" buttons create a planned volume Search
(`spec.season` 1 on the Comic, `catalogv1.VolumeSearchSeason`; the Music
section below). Since wave D (2026-11-10, the same day): Add New shows a
comic hit's publisher, issue count and start year and an audiobook hit's
authors, narrators and release date, asks ComicVine then MangaDex (one
search per source, `MetadataRequest.Source`, a source without a provider
skipped) and merges the hits under their source badges, and links an added
audiobook to the one Author of the hit's author's name in its namespace
(`release.TitleNorm`; the Audnexus lookup by ASIN through the third bound
request, `ui.Options.MetadataLookup`, only renders the name when the hit
carries none, and runs after the add's inputs are checked; no `bookRef`,
since Add New resolves no Book); the album page renders its own Edit over
the album's card (`ui.albumItem`; the artist's card is `AlbumView.Artist`);
a book row's forms return to the author's page (`views.AuthorURL`); the
per-kind search-panel types are gone; the comic page's Metron logo links
`status.metadata.links[type=metron]`; and an author's book whose
`spec.rootFolderRef` overrides its author's root is not a standalone book
(`bookHasFolder`: no authorRef); since wave E (ba7c7256) the Book
reconciler writes a standalone book's `status.path` as its own folder,
`<root>/{Book Title}`, where the import places its file (`book.Path` and
`catalogctx.BookFilePath` agree byte for byte, a test holds them), a book
written before it taking its folder at its key's next pass, and the ui
admits the folder to Refresh & Scan and Rename only once the book has a
file (`itemFolder`; its file's directory is the belt for the old path).
**An Album and a standalone Book are deletable** (wave E, 87afc7de,
a196a7ed; design §9.2, §9.3): `catalogv1.DeletableKinds` takes both and
`librarydelete` gains a kindSpec each -- an album's files delete removes
its track files and its own subfolder, never its artist's folder (a `Keep`
bound; refused while the artist's folder is unknown), an author's book is
deleted with its author, a standalone book's folder is its own; the delete
dialog names an album's shared folder only when the album's own lies
strictly inside it. A deleted album of a provider-listed release group
comes back at the Artist's next fan-out, monitored per its monitor mode,
as in Lidarr: unmonitor the album or the artist first to be rid of it. The
manager role's `delete` on albums and books lands with it (`config/rbac`,
the chart copy).
In the TV pane, only series should be shown. Clicking a series should present a page with the seasons and episodes.
A season's episodes are Sonarr's table (2026-10-07, `ui/series.go`,
`ui/episode.go`, `ui/views/episode.templ`): the monitor bookmark, number,
title (the episode's page, below), air
date, Sonarr's status cell (`episodeStatus`: downloading, pending, the
file's quality -- amber below the cutoff -- TBA, unaired, unmonitored,
missing; read from the Episode's status, which the catalog derives from
its Series' grab entries -- `activeDownloadRef`, `pendingGrab` -- and its
MediaFile's status) and two searches: the magnifier is "search now" (a
Search with `grabBest`), the person an interactive search -- a Search
without `grabBest` (`actions.InteractiveSearch`) whose ranked
`status.results` the modal polls (`GET /searches/{ns}/{name}`) and whose
download button adds the release to `spec.grab` (`actions.GrabRelease`: a
merge patch of the whole list under the read resourceVersion, so a
concurrent grab conflicts; `spec.override` for a permanent rejection,
after a confirm). The Search controller sends each pick to the item's
owner as a manual Candidate, and the panel polls until `status.grabbed`
names the grab entry it became (`downloadRef`) or the owner's refusal.
The ui role patches `searches` for it. Since 2026-11-04 (owner: "Each
item should also include Languages, Score, Video and Audio Codec,
Transcode status, Subtitle status, Marker status") the row also carries
its file's columns, read from the Episode's MediaFile by name
(`ui.episodeFiles`: a cached Get per file, never a List; `ui/episodefile.go`,
`views.EpisodeFile`, `views.Cell`): the languages its probe's tagged audio
tracks carry, and the frozen `spec.languages` only while the probe names
none (`ui.fileLanguages`, 2026-11-27, the modal's Files table too: a file a
library scan found before 2026-10-27 froze as its series' original
language whatever it carries, so 84 dual-audio files of JoJo's Bizarre
Adventure read "Japanese" and the owner asked why its Japanese-only files
were not marked for a graft; the item's `status.audio`, graft and cutoff
have always read the probe) (three as badges,
the rest behind a count), the format score, the probe's video ("HEVC 10-bit
HDR10", warned when `MediaFile.UndecodableVideo`) and its first audio track
as Radarr labels it ("EAC3 - 5.1") with the count of the others, and the
transcode, subtitles and markers as toned cells -- `status.transcode`'s
phase (Succeeded as Transcoded, and Transcoded for a tagged file the loop
holds no block for), `status.subtitles`' phase naming the languages still
missing, the segments' kinds ("intro · credits") -- each detail in its
tooltip, and every cell empty for a file the ui cannot read. Languages and
score fold away below a laptop's width, the rest below a desktop's. The
monitor toggle's reply reads the file too, so the swapped row keeps them. A
season is searched as one
query, planned by the Series (Wave S, 2026-10-07,
`docs/superpowers/specs/2026-10-07-series-search-design.md`): each season
header has Sonarr's two buttons -- the magnifier (`actions.SearchSeason`, a
Search with `grabBest` and `spec.season`) and the person
(`actions.InteractiveSeasonSearch`, `spec.season` without `grabBest`), whose
results open in the season's modal listing season packs first
(`?filter=packs|all`, `ReleaseDecision.coversSeason`) -- plus a badge from
`Series.status.seasonSearch`. The series header's "search now" is the
whole-series search. A planned Search (`SearchSpec.PlannedBySeries`) is
served by the Series key's season planner (`searchplan.DecideSeasons`,
the search stage's Series mode): one request at a time, one season task in
flight, each behind the namespace's live-search pace slot and admitted on
`catalog-search-high`; the Search mirrors it in `status.seasons`. With no
planned Search waiting it also serves the automatic season search (owner,
2026-10-27; the design's §17): every `searchplan.PackFirst` season (in
`status.seasons`: not the specials, monitored, no next airing, two or more
episodes without a file) with a wanted episode never searched is asked for
as a season, a pack ahead of its episodes, in a state whose request names
no Search (`searchplan.Automatic`, uid empty; never canceled, no
handshake, its task not user-invoked), an hour after the last automatic
state finished (`AutomaticCooldown`). An Episode never searched waits for
such a season (`View.SeasonPackFirst`; a redownload does not), since a
single grabbed first makes the grab planner refuse the pack as
overlapping; the season's answer folds in as its search, as a planned
one's does. A season answered with approved releases asks for the next
pass at once (`ItemResult.Again`, 2026-11-02): the grab stage runs before
the search stage and reads a season answer only from the stored
`status.seasonSearch`, which wakes nothing, so the answer otherwise waited
for an unrelated wake and aged out of `grabplan.CandidateWindow` ungrabbed
-- 62 series' automatic answers of 2026-10-28, 91 Days' 101 approved
releases among them, after which their episodes' searches went IndexOnly
and found nothing. A pack still downloading holds every episode it covers
(owner, 2026-11-02: "we shouldn't queue individual episode downloads
unless the season pack fails or ends up missing episodes";
`decision.ReasonPackDownloading`, `DownloadEntry.Pack`): no release is
grabbed beside it for one of them, an upgrade or another pack included,
only `spec.override`; once it has failed the episodes are wanted again,
and once it has imported their files decide. **A pack replaces the
singles of its season** (owner, 2026-11-03: "When a season pack is
downloaded, we should *wait* to queue any episodes and remove any existing
episode downloads for that season ... Episodes within the season pack
should *replace* single episode files within the season/series folder",
which reversed the same owner's 2026-11-02 "skip season pack
downloads/searches if single episodes are already downloading"): a pack is
grabbed over every single of an episode it covers that is downloading,
upgrade or not (`decision.queueRejection`'s pack), unless under
`audio.completeFirst` the single carries every wanted language and the pack
does not (`decision.AudioOutranked`, `grabplan.Holds`,
`decision.ReasonEpisodeDownloading`), and only such a single holds its
season against the automatic season search (`holdsSeason`); once the pack
is live, the lifecycle removes each automatic single-episode entry it
covers that is still transferring and was grabbed no later
(`lifecycle.supersede`, Event `SupersededByPack`: data removed, nothing
blocklisted, no redownload owed), a person's pick and a complete single
kept; and at import the pack's file (an entry of several episodes, or a
release titled as a whole season) replaces a file a single-episode release
brought (`spec.importedFrom.releaseTitle`), transcoded or not, past the
transcoded rule and the upgrade table (`importplan.packRefit`), unless its
probe lacks a language of the audio policy, wanted or optional, that the
file carries, or the file's HDR; a file an earlier pack brought, or one
recording no release, keeps the rules. A single still upgrades a live
single. A Series is still not a Search fan-out kind (`ContainerKind`). Every interactive
search's results are banded -- approved, rejected, then the identity
mismatches (`WrongItem`, `UnknownItem`, `UnableToParse`) last, counted by
`status.otherTitles` and collapsed behind "N releases for other titles",
never dropped -- in the agent before its cap and again after the
item-state rules; and a yearless, title-only match on a title another
library item carries is `UnknownItem` (`decision.Identity.Namesakes`).
An episode has a page of its own, the movie page for one episode (owner,
2026-11-03; `ui/episode_page.go`, `ui/views/episode_page.templ`):
`/library/{ns}/episode/{name}`, which its title in the season table opens
and preloads, and where the details route sends anything but htmx. Its
toolbar refreshes and scans the series (an episode's metadata is its
series') and runs the episode's automatic and interactive searches (the
modal, on its Search tab, in the page's `#episode-modal`); its hero is on
the series' fanart with the episode's still (TVDB's screenshot through
`/art/search`, else the series' poster), the bookmark, title, series,
season and `S01E03`, absolute number, air date, runtime, finale and
provider links (its own TVDB page, then the series' under a "Series"
caption), and the movie page's facts -- the Status fact Transcoded
from `status.cutoffReason` whatever the phase -- then the movie page's own
Files, extra files and Subtitles sections, and the episodes either side
of it. Edit, Image Set, Delete and Rename stay on the series' page. An item
page's provider links are each site's logo, not a generic link icon
(owner, 2026-11-04; `views.providerLinks`, `views.Link.Provider`): IMDb's
yellow badge, TMDB's stacked mark and TheTVDB's dark-background logo,
embedded under `ui/static/providers` (IMDb's letters and TMDB's mark from
Simple Icons, CC0; TheTVDB's from its own site).
An author's books are the same table (2026-10-07, `ui/book.go`,
`ui/views/book.templ`): bookmark, title opening the book's modal
(`/library/{ns}/book/{name}/details`, `#book-modal`), release date,
`bookStatus` (an episode's cases with `unreleased` for TBA and unaired --
an undated book reads missing) and both searches, the interactive one
through the same `startInteractiveSearch` and `/searches` routes. The
episode and book templates share `searchButtons`, `searchTab`,
`fileTable` and `statusCell` (`views.RowStatus`); an artist's albums are
`AlbumRows` (`views.AlbumRow`, the same table, since 2026-11-10). A movie's interactive search is Radarr's (2026-10-20,
`ui/movie_search.go`): "Interactive Search" beside "Search Movie" in the
movie page's toolbar opens `views.MovieSearchModal` ("Interactive Search -"
and the title) in `#movie-modal`, the same results panel and `/searches` routes.
Those modals (and the season search's) are shadcn-templ dialogs open by
default, whose portal moves them to `<body>` a microtask after htmx swaps
them in and before htmx settles the swap, so htmx processed only the empty
portal owner and wired nothing inside: an interactive search's panel never
polled past "Searching indexers…" and the modal's forms fell back to full
posts (2026-10-19). `ui/static/portals.js` has htmx process every portaled
node a loaded swap owns. A test of the rendered markup cannot see this --
the poll attributes were always there -- so check such a change in a
browser.
The live pages' stream containers (#unmatched-rows, #import-lists-rows)
morph each frame
in (idiomorph's htmx extension, `hx-swap="morph:innerHTML"`, 2026-10-21):
a node whose markup is unchanged is left alone, so a frame no longer tears
down every row's hover state and open menu. **The library tabs do not
paginate** (owner, 2026-11-10: "We should probably not paginate the
libraries so we can mass select and scroll more smoothly";
`ui/library_page.go`): a tab renders every card its view keeps (1,051
movies, 2.6 MB of markup, 225 KB gzipped -- the page is gzipped,
`compressed`), each card `content-visibility: auto`, and its stream sends
no markup: a `library-manifest` event lists the cards in order with each
one's `views.CardFingerprint` (`views.CardID`), only when that list moves,
and `ui/static/library.js` fetches the new and changed cards from `POST
/library/{tab}/cards`, morphs them in with `Idiomorph.morph` (a card keeps
its loaded poster), drops the rest and reorders, re-applying the selection.
The fingerprint must cover every field the card renders
(`TestCardFingerprintMovesWithEveryRenderedField`, by reflection over
`LibraryItem`). The A-Z letters link to their first title's card
(`#card-...`); `?page`, `?per`, `?pages` and `?jump` mean nothing there any
more. Every DOM change on a page has shadcn-templ's lifecycle observer
rescan the document for each registered component and walk every element
it started, so no card renders a component with a script: the cards' title
tooltip is one `<template>` (`views.cardTooltip`) that library.js clones
into a card when the pointer first rests on it or focus enters it, naming
its link the trigger, then replays the hover or `focusin` the tooltip
missed (owner, 2026-11-10: a tooltip per card was 40% of the page's load,
each found its trigger by a document-wide query, and a mutation cost 10 ms
over a thousand cards, 2.4 ms without them). Code that mutates the grid
often still batches it or waits for the pointer to rest, as the card
action bar does. A self-polling panel
(the interactive search's `hx-trigger="load delay:2s"`) must stay a plain
`outerHTML` swap: a morphed node whose attributes did not change is not
re-processed, so its `load` would never fire again. The preload extension
is on for the whole page (`hx-ext="preload"` on `<body>`): a library
card's link and the Library sub-menu's entries carry `preload="mouseover"`,
and the pages they open answer `Cache-Control: private, max-age=10` and
`Vary: HX-Request` (`preloadable`, `ui/routes.go`), without which the
browser would fetch the page a second time at the click; a page nothing
preloads sends no Cache-Control.
The look is Plex's native one (2026-10-22, after the iOS, tvOS and web
apps): one dark canvas, every surface a translucent white over it so the
artwork behind a page shows through the glass, Plex Gold the one accent,
12px corners. The tokens are rgba in `ui/static/input.css` (and
`ui/theme/plex.json`, kept in step), so a `bg-card` or `bg-muted/50` is
glass wherever it is used; the frosted surfaces -- the sidebar, the top
bar, a page toolbar (`glass`), every card and `.panel`, every dialog,
menu, popover and tooltip, and the charts' tooltip (a bare
`.recharts-tooltip-wrapper > div` that `chart.js` paints `bg-background`)
-- take their blur, and a floating one its popover glass, from unlayered
`[data-slot=...]` rules there, never from an edit to a vendored component
or a class of a view's own (owner, 2026-11-04). Write only the standard
`backdrop-filter`: Tailwind's minifier folded a pair written with
`-webkit-backdrop-filter` into the prefixed one alone, which Chrome and
Firefox ignore, so nothing blurred outside Safari until 2026-11-04; it adds
the prefix itself. A blurred card is a containing block for fixed
descendants, which is safe only because every floating layer portals to
`<body>`. An item page's fanart is the page's
backdrop: the hero's `<img data-backdrop class="page-backdrop">` is fixed
over the viewport at z-index -1 with a `page-backdrop-shade` fading it
into the canvas, which is why the canvas is the html element's background
and the body and the sidebar's inset are transparent -- an opaque
background on either paints over the art (both did, the first two tries).
Library posters (shadcn items, not cards) do not blur (hundreds a page, a
layer each) and lift under
the pointer (`[data-library-grid]` rules, off under reduced motion). Check
such a change in a browser with a fresh profile: `/static` is cached for
an hour, so a reload shows the old stylesheet.
Series monitoring is Sonarr's (2026-09-29): an episode is searched only
when its Series and the episode itself are monitored (the Episode
reconciler reads its Series, a Series' `spec.monitored` change wakes its
episodes, and the search worker drops an automatic task for an item no
longer monitored, as the *arrs' Monitored*Specification does); a season
toggle writes `spec.seasons[n].monitored`, which the
Series controller applies once to every episode of the season
(`series.SeasonCascade`, recorded in `status.seasons[].appliedMonitored`, so
an episode toggled afterwards keeps its flag) and to the season's new
episodes; and a monitored series monitors all of its seasons but the
specials (owner, 2026-10-27): a series is added with `addOptions.monitor:
unmonitorSpecials` -- the CRD default, the rescan's, an import list's and
Add New's first choice -- and the monitor toggle on an unmonitored series
(card, page, bulk editor, Edit dialog) also writes every non-special
season's `spec.seasons[n].monitored` (`actions.MonitorSeries`,
`AllSeasonsMonitored`, under the read resourceVersion), which the cascade
applies. A series already monitored keeps its season flags: the owner chose
not to monitor the 6,967 missing episodes of the 331 series already in the
library. From 2026-09-29 to then a series was added with `none` and
searched nothing until a season or an episode was turned on, after the
rescan's first `all` default queued 5,718 episode searches for a 147-show
library. Specials never count
toward the series: `status.episodeCount`, `episodeFileCount`, `nextAiring`
and `previousAiring` leave season 0 out (`series.Rollup`, as Sonarr's series
statistics do), and only the Specials row of `status.seasons` counts them.
A series' library status is read from its episodes
(`projection.seriesStatus`): Downloading while an episode's grab is in
flight, Missing while `status.missingEpisodeCount` (monitored, aired, no
file -- phase Wanted) is above zero, Downloaded once files are on disk and
nothing is missing, else Unmonitored. A Series has no file of its own and
its phase is only Ready, Pending or Unmonitored, so the movie rule read
every monitored series as Missing until 2026-09-30.

Each item should show the cover art, monitored status and selected quality profile

The **Live TV** section (2026-10-27, the direct-tuners and ui design's
§5; Dispatcharr's pages in the Plex look) is a sidebar entry after Library
with three sub-entries swapped like the library's (every sub-entry carries
the whole menu out of band, `#app-nav`). **Channels** (`/livetv/channels`)
lists every IPTVChannel by number with its resolved name, group and logo
(`ui/projection.LiveTVChannels`: the overrides, else the guide index and
the playlist), edits number, name, active and the EPG mapping in place
(`actions.EditChannels`: the channel read again through the apiserver,
refused as a conflict, said in `#action-status`, when its
`metadata.generation` is not the one the page read, else one merge patch
under the version just read -- never the page's `resourceVersion`, which
the controller's status write after every edit made stale within a
second), edits in bulk (a delete asked about first), copies each
channel's tuner stream URL and each block's HDHR/M3U/EPG URL, and pages
the provider's playlist beside it, where an entry the provider's filters
admit (`pkg/iptv/filter`) is added as a channel at the next free number
(`actions.AddChannel`). The **M3U/EPG Manager** (`/livetv/sources`) lists
each provider's playlist, guide and blocks, refreshing every 10 s, with
"Refresh now" (the `clustarr.io/livetv-refresh` annotation) and the
settings forms of both kinds (`iptvproviders`, `iptvchannels`, which the
Settings page does not list). The **TV Guide** (`/livetv/guide`) draws the
guide index in six-hour windows at 7.5 px a minute, loaded as they scroll
into view, times in the browser's zone (`ui/static/guide.js`). The ui reads
the `clustarr-livetv` store through `Info` and `Get`, decoding an object
again only when its digest moves (`ui/livetvstore.go`), and serves logos
at `/livetv/logo/...` from the tuner's stored copies. There is no playback
in the browser and no DVR page (design §5.8, §5.9).

**Activity** (owner, 2026-11-10: "Move pipeline and downloads into a new
group Activity ... Downloads, Transcodes, Subtitles, Markers. Each page
should have tabs like Running, Failed, etc like on the dashboard. We can
remove the pipeline page entirely") is a sidebar entry after Live TV whose
sub-entries swap the page body as the Library's do (`ui/activity.go`,
`ui/views/activity.templ`): `/activity/{downloads,transcodes,subtitles,markers}`,
each the dashboard's tabbed card (`sectionCard`) over the projection's
per-state lists (`projection.Dashboard`, now with `Subtitles`, `Markers`
and `Downloads.Completed`; `ui/projection/activity.go`), 25 rows a page,
refreshed every 10 s by a hidden `[data-list-poller]` that
`ui/static/dashboard.js` tells which tab and pages are open
(`/activity/{section}/list`, out of band). Downloads keeps the clients and
each entry's actions; subtitles are Searching, Wanted, Failed (Blocked, or
a language whose fetch failed) and Done; markers are Pending (probed, no
result yet), Found, None found and Error. A list holds at most
`projection.DashboardRows` (500) rows while its tab counts all. The
pipeline page, `/events/pipeline`, `/events/downloads` and Options
`Entries`, `Subscribe` and `SubscribeDownloads` are gone; `/pipeline` and
`/downloads` redirect. The e2e scenarios that read them (`test/e2e`
ui, subtitle and transcode) still do, and are Phase H's to rewrite.

**The Activity pages were redrawn on 2026-11-27** (owner: "selected tab
should be both saved on page refresh and reflected in the url"; "the tabs
should be simplified to use the shadcn-templ tabs component and drop the
overly fancy styling"; "each item in the list should follow the dashboard
page's styling and how it leveraged the progress component. It should look
similar to a table where each entry is concise"; "the item buttons should
be consolidated into a grouped, selectable action button that is aligned to
the right of each row"; "the current subtitle text ... should be moved into
an info button that presents a shadcn-templ popover listing the expected
files"). They no longer use the dashboard's `sectionCard`:

- **Tabs** are `tabs` as it comes (the default pill list), a tab its name
  and its count in a badge, under `data-activity-tabs` -- not
  `data-dashboard-tabs`, which the dashboard's frosted tile is keyed on.
  The open tab is `?tab=<value>` and its page `&page=<n>`
  (`ui/static/dashboard.js` replaces the URL at a tab or a pager click,
  never at a poll; `activityView` reads both, and the poller's
  `tab-<section>` / `page-<list>` win when present). A pager links
  `/activity/{section}?tab=&page=`.
- **A list is a grid its rows share** (`views.activityList`, rows
  `grid-cols-subgrid`), not a `<table>`: from 48rem of card (`@3xl/activity`)
  a row is one line -- name, line, state, actions -- with a header; narrower
  it is two lines. Every row is `views.activityRow`: the name linking to the
  item, the info button, the line (`rowProgress`, the dashboard's bar, while
  a transfer or a transcode moves; a detail or a failure otherwise; a
  download past its transfer has no bar), a state badge toned by meaning
  (`downloadPhaseTone`, `stateTone`).
- **A download's actions are one button**: a `buttongroup` of its first
  action (`downloadActionList`: pause or unpause, else import again, else
  remove) and a `dropdownmenu` of the rest, each still the same
  `hx-post` into `#action-status` with the layout's confirm.
- **The info button** opens a `popover` (vendored 2026-11-27 with
  `shadcn-templ add popover`): a download's expected files (its entry's
  episodes in order as `S02E01`, issues as `#n`, else the item), then item,
  size, indexer, client, when and by what it was grabbed, the transfer and
  swarm, the import with its detail link, the failure or note; a
  transcode's profile, class, plan, reason and message; a file's state,
  detail and when.
- **Menus and popovers in a polled list.** They are portaled to `<body>`
  and stay there closed, so: every one has an id from its row
  (`views.rowID`), the poll's trigger waits while one is open
  (`activityPollTrigger`, `window.activityFloatOpen`), and before the card
  is swapped `dashboard.js` removes every portal the old rows own
  (`htmx:oobBeforeSwap`; an out-of-band swap's element is `detail.target`,
  the event's own target is the requester by `oobAfterSwap`). Checked in
  Chromium against talos at 1600px and 430px: no stray portal and no
  duplicate id after a redraw. The poll's answer for Downloads is about
  1.2 MB of markup (88 KB gzipped) for four tabs of 25 rows, most of it the
  menus' and popovers' class strings.

The ui's home page is the **Dashboard** (2026-10-22: `ui/dashboard.go`,
`ui/views/dashboard.templ`). `/` redirects to `/dashboard`, the sidebar's
first entry. It is a section per subject (owner, 2026-11-04), in the order
work flows: Downloads, Transcodes, then Plex's and Live TV's charts, each
section a grid of eighths on a wide screen.

Downloads and Transcodes are two half-row cards each, laid out as
shadcn's "Bar Chart - Interactive" (`views.sectionCard`): the title and
its one-line description at the header's left, square tabs at its right
(one size on every card -- 4rem on a phone, 5 on a laptop, 6 on a wide
screen -- so every header is as tall; on a phone a line of their own),
each tab's colour a line along its bottom edge, dim until it is open, the
open tab a frosted tile (input.css: the page's four UltraBlur corner
colours fixed to the viewport as the html background is, so the tab shows
the patch of canvas behind it, under a thin white frost, with a faint 1px
rim and a soft shadow -- settled with the owner over six passes on
2026-11-04, "looks *perfect*"; change its numbers only on the owner's
word), its
name and its figure (owner, 2026-11-04: "the tabs should be square ...
the same size for all cards"; "moving the colored lines to the bottom";
"Card height should be identical for all cards"). The cards of a grid row
stretch to the tallest.

- The first card's tabs are its lists from the projection round
  (`Projection.Dashboard`): Running, Queued and Failed grab entries;
  Running, Pending (Pending, Planned, Queued), Failed and Skipped
  transcodes (MediaFile `status.transcode`). A tab counts its list and
  opens its rows, every list a row a line, four to a page
  (`ui.listPerPage`; five until the cards had to fit beside their charts)
  with a pager under it, one page or many (up to
  `projection.DashboardRows`, 100); a row's bar is the progress component
  in full, its details the label and its percent the value.
- The second card's tabs are the section's charts (`dashChartTabs`): a
  series or a chart a tab, its figure the range's total, peak or latest.

A section's grid holds two `display: contents` groups, its live cards and
its charts, and two hidden pollers refresh them out of band: the summary
every 10 s (`/dashboard/summary`), the charts every 60 s and at a range
pick. `ui/static/dashboard.js` adds every card's open tab and every paged
list's page to those requests (`tab-<card>`, `page-<list>`), and a pager's
link asks for its page at once, so a refresh never snaps either back.

The charts are shadcn-templ's chart component (`ui/components/chart`, an
SVG runtime in the component bundle). They chart range queries the ui runs
against a Prometheus-compatible API (`ui/prom`): `--metrics-url`, chart
value `ui.metrics.url`, e.g. Mimir's `/prometheus`. Without one the charts
show as not configured. They cover:

- Plex sessions, bandwidth and plays, from cluster-plex's
  `clusterplex_plex_*`;
- Live TV;
- transcodes finished, transcode workers and the transcode backlog;
- grabs and imports.

The range is 6h, 24h or 7d (`/dashboard/charts?range=`), refreshed every
60 s. A gauge is charted as each step's peak (`max_over_time`). Answers are
cached for 30 s and capped at 4 MiB, and only the ui queries the store,
never the browser. A cached answer is shared by every render, so never
modify its points in place: rescaling them did, and turned 40 Mb/s into 0
from the second render on.

Each library page has an **Add New** (`/library/{tab}/add`, since
2026-09-29): it searches its kind through catalog's metadata search RPC
(TMDB movies, TVDB series, MusicBrainz artists, Open Library authors) and
adds the chosen item through `ui/actions.AddItem`; an item the library
already holds is found by `LibraryItem.ProviderID` and opened instead.
The top bar (2026-10-06, after Sonarr's) is shadcn's navigation menu: the
sidebar trigger and the breadcrumbs (`#page-crumbs`) on the left, the
search centred as an input group (`#header-find`), an Add New menu on the
right; a tab's htmx swap carries the crumbs and search out of band beside
`#library-subnav`. The search is a typeahead (2026-10-01, Sonarr's and
Radarr's header search): on a tab `GET /library/{tab}/find?q=`, elsewhere
`GET /library/find?q=` over every tab, answers a dropdown of up to 10
**monitored** items (`projection.Find`: titles through
`release.TitleNorm`, ranked exact, prefix, word prefix, anywhere; a
`tmdb:603`-style or bare provider id first), from the projection alone,
with the count (`#library-find-count`) out of band;
`ui/static/find.js` gives it arrow keys, Enter and Escape and places the
dropdown `position: fixed`. No match links to Add New with `?q=`, which
fills and runs its search. The page toolbar is shadcn's menubar
(`views.toolbar`; Options, Sort and Filter are its menus). An item page's
bar -- a movie's, series', season's or episode's (`views.actionBar`, owner
2026-11-04) -- is one shadcn-templ button group at its large size, the
group itself the glass with no panel around it, centred under the top bar
(owner, same day: "Simplify the toolbar ... like the large variant", then
"a tad bit smaller"): outline buttons joined at their borders, each an
icon over one word (Refresh, Search, Interactive, Preview,
Rename, Edit, Images, Delete) with the whole action as its title. An
action's form is hidden and named by its button's `form` attribute
(`views.actionForm`), so the button is the group's own child and takes the
joined borders while htmx still catches the submit; a dialog's trigger
stands in the group itself, its portal hidden beside it. The library has
Sonarr's mass editor: Select marks cards (`ui/static/library.js`, client
side, re-applied over every stream frame) and a bottom bar posts the
selection as `item=namespace/kind/name` to `POST /library/{tab}/bulk`
(monitor, unmonitor, search, refresh, delete), which accepts only items on
that tab in the projection, validates all before writing, and runs each
item page's own `ui/actions` call; Options (poster size, details) are the
reader's own, in localStorage. Every card has its actions on hover
(Sonarr's poster buttons): the card is a div with a stretched link
(`data-card-link`), and Refresh & Scan, Search, Monitor/Unmonitor post
through htmx to the item's own routes with `HX-Target: action-status`,
which `finishActionWith` answers in place (200 with `ActionStatus` or
`ActionError`, never a redirect; the monitor toggle skips its row reply);
Delete opens the page's delete dialog for that item alone. Since
2026-11-10 one bar serves the page (`views.cardActionBar`): library.js
moves it into the card the pointer rests on (60 ms) or focus enters, and
names that card's route at `htmx:configRequest` in place of the
placeholder `/library/card-action`; the card's `data-monitored` picks
Monitor or Unmonitor by CSS. Four buttons a card had been half of every
card's 10 KB. **The library filters as Radarr and Sonarr do** (owner,
2026-11-10: "many more filtering options ... including custom filter
options"): per-tab presets (`projection.LibraryFilters`, TV adding
Continuing Only and Ended Only) and sorts (`LibrarySorts`: Added, Size on
Disk, Rating, Runtime, Release Date; TV Network, Next and Previous Airing,
Episode Progress; an unknown value sorts last either way), and custom
filters: rows of field, predicate and value over about 35 fields a tab
(`projection.LibraryFields`: genres, keywords, studio, original and audio
language, certification, ratings, size, codec, HDR, quality, release dates,
series status and type, network, airings, episode counts...; the
projection reads them off each Movie, Series and the movie's MediaFile),
every row kept, a row's values any of them. A filter is the page's URL, a
repeated `?f=<field>:<op>:<value>[|<value>]` (`projection.Condition`;
`ParseConditions` drops what a tab cannot test), so the page, its stream
and a shared link agree; the active filter is chips under the toolbar,
each removing itself. The builder is a dialog (`views.filterDialog`) whose
rows `GET /library/{tab}/filter/row` draws and redraws on a field or
predicate change, applied by a plain GET of `/library/{tab}/filter`, which
redirects to the page's URL. Saved custom filters are the reader's own, per
tab, in localStorage (`clustarr.library.filters`), listed in the Filter
menu: the ui has no CRD to keep them in. TestUINeverWrites reads any
`.Delete(` selector as a write, `slices.Delete` included. Tailwind's
`group-hover` applies only under `(hover: hover)` -- headless Chromium
reports `none`, so a screenshot shows no hover bar unless the rule is
injected. The vendored shadcn-templ components are
the registry's own (`shadcn-templ add … --overwrite`, 2026-10-06: hooks are
`data-templ-*` and `window.templ`, the select's hidden input follows its
trigger and a read-only select is `ReadOnly` with a disabled trigger, so
its value still posts); Clustarr no longer keeps its own navigationmenu or
scrollarea. The registry's catalogue and docs index is the `shadcn-templ`
project skill (`.claude/skills/shadcn-templ/SKILL.md`): read it before
choosing a component or running `shadcn-templ add`. Since 2026-11-04
(owner: "determine what can migrate to a
native shadcn-templ component"; "use the select component from
shadcn-templ"; "all inputs/forms ... input groups or field groups") every
control a person sees is a component: `shadcn-templ add` (the registry
inlines its `cn-*` styles for the base-vega style as it writes, so a
vendored file never carries them) brought collapsible, progress, spinner,
toast, kbd, toggle, toggle-group and alert-dialog beside the earlier set,
and the views hold no raw `<input>`, `<select>`, `<textarea>`, `<label>`,
`<details>` or `<button>` -- only hidden inputs and the `<form>` elements
themselves, which have no component. The rules the sweep settled: a select
is `select`, never `nativeselect` (vendored once and removed); a disclosure
is `collapsible`, and one that loads on first open asks on
`views.expandOnce` (the collapsible's `collapsible-open-change[detail.open]
once`, since a collapsible fires no `toggle`); a destructive question is an
`alertdialog` (an item's Delete, Settings' and Import Lists' deletes, a bulk
channel delete), and every `hx-confirm` asks through the layout's one
(`views.confirmDialog`, `ui/static/confirm.js`), so nothing calls
`window.confirm`; a card action's answer into `#action-status` is a toast
marker (`ActionStatus`, `ActionErrorToast`; `toast.Toaster` in the layout)
with the tests' `data-action-*` on its wrapper, while `ActionError` stays
the inline alert an error page shows; an empty list is `views.emptyState`
(icon, title, a line) and an empty section `views.emptyNote`; a progress
bar is `progress`, its percent on `data-progress`; a segmented filter (the
season search's packs|all, the dashboard's range) is a `togglegroup`; the
alert dialog and the toast take their glass from input.css's `[data-slot]`
rules as the dialog does. Not swapped, on purpose: the library's A-Z bar,
the guide grid's geometry, the channel logos (`avatar.js` hides the image
until it loads, which would flash the number tile on every refresh), the
header search's dropdown shell (`command` and `combobox` filter client-side
over their own items while find.js's list is server-filled), the sort and
filter menus (menubar radio items carry no `href`), the Live TV copy
button (`copy-button` is a docs-site part, not a registry item) and the
monitor bookmarks (`toggle` is `type="button"`, and theirs are submits).
Format a template with the project's templ, `go run
github.com/a-h/templ/cmd/templ@v0.3.1118 fmt <file>` (the pin moved from
v0.3.1020 on 2026-11-26, owner: "the latest templ release with a rebuild";
v0.3.1100 removed the runtime functions v0.3.1020's generated files called,
so the runtime in go.mod and the generator in the Makefile move together and
every `*_templ.go` is regenerated with them). Every source was formatted by
v0.3.1118 the same day (it laid fifteen out differently from v0.3.1020: ten
vendored components and the add, detail, edit, movie search and season
search views), so `fmt -fail ui` reports nothing on a clean tree and a
formatter that rewrites a file nobody touched is the wrong version. **From v0.3.1100 the generator writes no whitespace between
neighbouring elements** (`</svg> <span>` became `</svg><span>`) and ends a
computed `style` attribute with `;`: two inline elements side by side keep a
gap only from their parent (`flex` or `grid` with a `gap`, which is what every
page had on 2026-11-26: 410 pages and 249 fragments from the ui tests were
laid out in Chromium old against new and no element moved), never from the
space between them in the template.
**The settings forms follow their own choices** (owner, 2026-11-16:
"selecting an option like music or movies limits which fields are valid ...
filter which input groups and specific inputs display based on the selected
options"). A kind's overlay (`ui/forms/kinds.go`) states it three ways
beside a group's `When`: `Only` (a field, or everything under an object,
shown only while a condition holds: a movie root's series type is not
drawn), `OptionsWhen` (one option of an enum or of a checkbox group) and
`RefOptions` (a live object offered by its `Option.Class`: a root folder
gets the quality profiles of its media kind, an indexer the download
clients of its protocol). A `When` is `path=v1|v2`, `path!=…` with `Not`,
joined by `And`; a field of several values has each, and **a field a
condition hides reads as unset**, so what depends on it follows
(`ui/static/settings.js` repeats the pass until nothing moves). Hide only
what is certainly meaningless for the choice, and state it against the
CRD's own rules: `TestEveryConditionWatchesARealFieldAndItsRealValues`
holds every condition to a field the form carries and to values it can
take. A hidden input still posts (the choices that hide are immutable or
shaped by the kind's `Ensure`); only a hidden option of a checkbox group
(`Checks`, an array of an enumerated string whose order means nothing)
posts nothing. **An Indexer's settings are its definition's**
(`ui/forms/definition.go`): the Definition section is built from the
chosen IndexerDefinition's YAML (`cardigann.Load`), each declared setting
an input of its type posting into `spec.settings` at map index 1000+i, a
password or a credential by name (`SecretSetting`: username, cookie,
passkey...) into the Indexer's Secret under the setting's name, which the
engine reads over `spec.settings`; undeclared settings, and a credential
an indexer keeps in `spec.settings`, stay in the free "Other settings"
table so an edit never loses one. The section is a `forms.Dynamic`: htmx
fetches `GET /settings/indexer-definition` again when the select changes,
it carries `__meta.protocol` (every Cardigann definition is a torrent
site's) for the torrent-only fields and clients, and offers the
definition's first address to Base URL. A save drops settings that only
restate the definition's defaults (`PruneDefinitionDefaults`). **Sections
are accordion areas** on the Settings page and in every form: a Card that
is a `collapsible`, its header shadcn's own (title, description, and Add
and the chevron in `card.Action`), the Settings page's closed until
opened and remembered in localStorage (`clustarr.settings.open`,
`/settings#<slug>` opens one), a form's Advanced ones closed; a submit
first opens a closed section holding an input the browser will refuse.
The registry's `accordion` is not used: under the pinned CLI
(v2.0.0-beta.10) `shadcn-templ bundle` concatenates scripts
alphabetically, so `accordion.js` runs ahead of `baseui/` and its
uncaught error stops every component on every page; check the bundle's
order before adding any component that sorts before `baseui`. A map's
rows decode by index, not position (`schema.decodeMap`: until then
removing a middle row dropped every row after it on save).
An import list likewise finds an item by provider id
(`importlist.loadLibrary`) and writes only the items it added itself:
one added by hand, by a rescan or by another list is recorded as listed
and never changed or removed by it (Radarr), since item names hash the
title as well as the id. **The one exception is `spec.listed`** (owner,
2026-11-17: "import lists should be able to add additional tags and
metadata to each item. This way it's easy to bundle them back into a
collection"): its `tags`, `labels` and `annotations` go on every Movie and
Series the list names, whoever added it, at each sync
(`app/import/controller/importlist/listed.go`), under a field manager of
the list's own (`k8s.ManagerImportListed`, "import-listed/<list>", a
family `FieldManager.Valid` takes by prefix). Movie and Series
`spec.tags` are a set (`+listType=set`), so the list's tags sit beside the
ones an item was added with and beside another list's, a changed block
replaces only the list's own, and an empty apply takes them off: when the
item leaves the list, when the block goes, and when the list is deleted,
which the finalizer `catalog.clustarr.io/listed-items` holds until its
marks are off. A disabled list keeps what it put. Each remembered item
records the block it carries (`StoredItem.Listed`), so a sync writes only
where the block moved or someone removed a mark. The marks are
written eight at a time (`listedWriters`), and an item that carries them
with nothing recorded is taken as marked without a write: on talos a write
is a quarter of a second and the list's reconcile has five minutes, so the
first run on `great-films`' 755 films, one write at a time, ended past its
deadline at 593, saved no record, and every retry wrote the same films
again (2026-11-17). A label or annotation key
under `clustarr.io/`, `kubernetes.io/` or `k8s.io/` is refused (CEL, and
again in the controller): those are read as instructions, and
`catalog.clustarr.io/delete` on a list's 755 films would remove them.
cluster-plex writes an item's `spec.tags` as Plex labels beside its
keywords, so a smart PlexCollection filtering `label is <tag>` gathers a
list: talos's Criterion Collection is the `great-films` list's tag. catalog seeds a MetadataProvider for every
type that needs no key (`metadataprovider.SeedDefaults`: MusicBrainz,
Cover Art Archive, Open Library, Audnexus, MangaDex, AniList, Kitsu,
Anime-Lists) in its own namespace at start, create-only, skipping a type
already configured under any name; `spec.enabled: false` turns one off,
since a deleted seed returns at the next start. caption likewise seeds
the default SubtitleProfile `english` (`subtitleprofile.SeedDefault`)
when the cluster has no SubtitleProfile at all, with `audioExclude` so
only media whose audio is not English is searched; a file with untagged or
unprobed audio counts as its item's original language (the Movie's or the
Series' `status.metadata.originalLanguage`, `subtitleplan.audioLanguages`).
Books take English titles as movies and TV do (2026-09-30): the Open
Library client (`WithLanguage`, from MetadataProvider `spec.language`)
lists an author's works through its search API with `lang`, titles each
by its edition in that language (and `Book` by its most common edition
title, so a refresh keeps it), names the author by Wikidata's label, and
gives each work `Languages` so `metadataProfile.allowedLanguages` can drop
translations catalogued as works of their own; Add New sets it to `en`.
One book is one Book: Open Library holds many works per book (19 for The
Brothers Karamazov in Dostoevsky's top 100), so `Books` collapses works
whose titles are the same book (`bookKey`: case, punctuation, articles,
subtitle, brackets, byline, volume and edition words) into the
most-published one, and with a language lists no work it cannot title in
it (only-Cyrillic volumes).
Each library item's page has **Delete**
(`docs/superpowers/specs/2026-09-30-library-delete-design.md`, 2026-09-30):
the ui only patches `catalog.clustarr.io/delete` (`files` or `records`)
and `delete-add-exclusion`; import's `librarydelete` controller removes
the folder permanently for `files` (never the RootFolder, refusing a path
outside it or a folder holding another item's MediaFile), deletes the
MediaFiles and then the item, or writes `delete-error`. The library scan
skips a folder whose id an ImportExclusion names (unmatched `excluded`).
Search posters are provider URLs, so they go through `/art/search` --
signed by the ui, fetched only from the four providers' image hosts,
capped and cached -- never straight to the browser (ADR-0011).
The Plex provider answers with the full Metadata Response (2026-09-30,
`docs/superpowers/specs/2026-09-30-plex-full-metadata-response-design.md`):
tagline, studios, countries, per-country certifications (`contentRating`
by `X-Plex-Country`, `cc/rating` outside the US), `isAdult`, season posters,
episode stills, `SeasonType` for the stored order, `theme` for shows, and
the original-language title, genres and images when Plex asks in another
language; people and similar titles come from the
`clustarr-metadata-extended` KV document the metadata gateway writes per
item (`pkg/metadata/extended`; file-backed even on single-node NATS,
`BucketSpec.Durable`, since a document is rebuilt only at the item's next
refresh), which the ui reads through a closure under a 2 s deadline, never
a KV handle. PMS 1.43 (11029) reaches the ui's cluster address only
through its agent, which downloads a refreshed item's images; any image
URL it keeps it fetches later through `images.plex.tv` or itself, refusing
a private address ("not a permitted destination for a caller-supplied
URL"; through `images.plex.tv` the failure reads "URL requested for photo
transcode is over maximum size of 104857600 bytes", though nothing was
that big). So, since 2026-10-26:
- a movie's or show's poster, background and logo, and a season's
  fallback to them, stay the ui's `/art` URLs, the poster overlay-first,
  which PMS downloads, so the rating-badge overlay reaches Plex;
- **the compact rendition is served beside it and never instead of it**
  (the compact-posters design, 2026-11-27): `/art/<kind>/<uid>/poster?size=compact`
  answers a movie's or a series' compact overlay, else its compact poster,
  else **404** -- never the regular poster, and 404 for any other `size`,
  type or kind (`chooseArt`, `artVariants`), because the proxy in the Plex
  pod takes a 404 to mean "let Plex serve what it holds". `/art` scales
  nothing. `GET /plex/compact-posters` (`ui/plex/compact.go`) is the
  pairing list that proxy reads: for each movie and series with a compact
  rendition, the poster URL the provider hands Plex **exactly as handed**
  (built by `artworkFor.url`, the `:443` and `&nowebp=1` included: Plex
  names its stored poster by that URL's SHA-1) and the twin's path with
  its digest in `?v`; sorted, no title, ETag and 304. A ui whose artwork
  index is not synced lists nothing. The list pairs like with like
  (`Server.compactArt`): an overlaid poster with its compact overlay only,
  an original with the compact poster, so a phone never gets a poster
  without the badge the large one carries (`/art?size=compact` itself
  still falls back from an unreadable compact overlay to the compact poster);
- a match result (`POST .../matches`) carries them by the artwork entry's
  `sourceURL` instead (`urls.sourced`, `artworkFor.url`), because PMS makes
  a match result's thumb the item's and selects a refresh's download only
  when it is a new file -- never a season's, whose poster is its show's.
  Every season of 86 and A Model Family and 14 movies lost
  their posters that way, as did 214 seasons in all;
- person photos, season posters, episode stills and image-set overrides
  go through `Options.PhotoURL` (`plexPhotoURL`): the source itself, only
  on a host `/art/search` would fetch from. PMS keeps a person's photo as
  a URL, which had broken 17,086 cast photos.

ADR-0011's rule against hot-linking concerns the browser, and Plex's
servers fetch these URLs. `/art/search` still signs with
`$CLUSTARR_ART_SIGNING_KEY` from the chart's kept
`<release>-ui-art-signing-key` Secret for the ui's own pages. A Movie or Series document
older than `pkg/metadata.SchemaVersion` (`status.metadata.schemaVersion`)
is refreshed once, past the L2 cache (whose key carries the version), and
stays ready meanwhile -- raise the version when the gateway learns a field.
An image set from MediUX or ThePosterDB (2026-10-21,
`docs/superpowers/specs/2026-10-21-image-sets-design.md`) replaces a
movie's or series' poster and background, a series' season posters and
backgrounds and its episodes' thumbnails: the item page's **Image Set**
dialog takes Kometa's image-files YAML (`pkg/imageset`: `url_poster`,
`url_background`, `tpdb_*` ids, `seasons{n}`, `episodes{n}`), and
`ui/actions.SetArtwork` patches `spec.artwork` and
`Series.spec.seasonArtwork` (`{season, episode?, type, url}`, 1000 at
most), the item's overrides of other types kept. The Plex provider hands
PMS the season and episode overrides' own URLs on a host the `/art/search`
allowlist names (it gained `api.mediux.pro` and `theposterdb.com`); an
override on another host is not shown. PMS shows them at its next refresh of the
show.
Sets are also chosen automatically (2026-10-22,
`docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md`;
owner: automatic for every item, opt-out per item): a `mediux`
MetadataProvider (secretRef key `token`, a MediUX bearer token; `spec.mediux`
`preferredCreators`, `languages`, `quality`, `refresh`) makes the remediation
loop's image-set stage (`app/remediation/imageset`, after metadata, before
artwork) ask the metadata agent's `catalog-imageset` worker for every Movie
and Series with a TMDB id (a series' TheTVDB id is mapped) whose
`spec.imageSet` is not `none`, through a `clustarr-image-sets` record and
the dispatch ledger; the agent (`app/catalog/worker/imagesets`) reads MediUX
through `pkg/metadata/mediuxsets` over the `clustarr-mediux` cache
(`pkg/metadata/mediuxcache`: id mappings for good, set lists a week, "no
sets" a day, the daily `content_ids` list first, so an item MediUX has
nothing for costs no request) and composes the item's images from all of
its sets, slot by slot (owner, 2026-10-27: "combining various sets";
`mediux.ComposeShow`/`ComposeMovie`, the design's "Composed sets"): the
anchor -- a pinned set, else the best by preferred creator, poster
language, completeness, a film's own set over a collection's,
popularity, recency -- gives what it has, its creator's other sets and
then the rest fill each poster, backdrop and season poster it lacks, and
each season's titlecards come from one set (the anchor creator's with the
most for it) with only its gaps filled elsewhere. MediUX's `popularity` is
a rank, 1 the most popular, which the first cut read upside down. A film's
candidates include the collection sets' images of it (a second query in
AURA's shape, never recorded live: a GraphQL error from it alone,
`mediux.ErrQuery`, leaves the film its movie sets). Every image carries
its scope (a specials poster is season 0's, never the item's) and the set
it came from; the stage writes the result into `status.imageSet` under
`catalog-imageset` (its one field; `setID`, `title`, `creator` the
anchor's, `sets` every set that supplied an image, at most 32),
reconsidered every `refresh` (7 days; an hour after a failure), never the
spec. MediUX has no logos, banners or season backgrounds, and any slot no
set fills keeps the providers' images. Every reader takes the owner's
`spec.artwork`/`spec.seasonArtwork` over it: the Plex provider's
`seasonOverride`, the artwork fetch (`catalogartwork.WithImageSet`, so the
set's poster and background are stored as custom originals) and the item
page's "Images" fact (pasted set, else "MediUX set <title> by <creator>"
and how many more sets filled it, linked to the anchor's
`mediux.pro/sets/<id>` page, built from the set id, never from `status.imageSet.url`, which held AURA's `mediux.io/<kind>-set/<id>` until 2026-11-04 and which mediux.io no longer serves; the pin form accepts both shapes). A set cached as JSON must round-trip: `ShowSet` and `File` have
`MarshalJSON` writing the wire's nested shape their decoders read, without
which every cached set came back poster-only and the second choice was never
the first's. The proxy allowlist has `images.mediux.io` too, the host the
chosen sets link (`api.mediux.pro` refuses `key=jpg`). The dialog's second
form sets the policy per item (`ui/actions.SetImageSetPolicy`): untick
"choose automatically" for `spec.imageSet: none`, or pin a set by id or
page URL into `spec.imageSetID`, which anchors the composition and keeps
every image it has (fetched by id when the item's list lacks it, taken
alone when the item has no id, passed over and named in the reason when
MediUX does not find it, never a NotFound that clears the item's images)
and a changed pin asks for at once. Only a series' own slots are filled
when its shape is known (its seasons and the specials, each season's
episode count). A MediUX limit longer than five minutes defers the record and acks,
and the stage republishes the task at the reset (`Decision.Resume`,
`MsgIDForImageSetAt`), as the markers worker does.
A season named by TheTVDB ("Book 1: Water", "Steel Ball Run";
2026-10-21) is titled by that name in Plex instead of "Season N", and on
the series page beside its label: `/series/{id}/extended` carries each
season's `name` in the show's original language, the client asks
`/seasons/{id}/translations/<lang>` for the titles' language where
`nameTranslations` lists it (at most 40 per series, a failed one keeping
the original), and the gateway writes `status.metadata.seasonTitles`
(per order, `SchemaVersion` 6); the provider takes the series' own
order's title, else the aired order's, as it does the season poster.
TheTVDB's Sub-Genre tags (Dark Comedy, Psychological Thriller;
2026-10-27, `SchemaVersion` 7) are a series' `status.metadata.subGenres`,
kept apart from `genres` (which classification and `originalGenres` read),
and the Plex provider lists them after the genres as further `Genre`
entries (`withSubGenres`), since Plex builds a TV library's categories from
Genre and keeps no Label, Style or Mood from a provider (spike
2026-10-08). Only the Sub-Genre group is taken: Setting, Time Period and
Geographic Location are no genres, and coverage is crowd-sourced (Breaking
Bad has three, Stranger Things none). TMDB's keywords ("time travel",
"heist"; 2026-10-27, the same `SchemaVersion` 7) are a Movie's and a
Series' `status.metadata.keywords` (50 at most; a movie's appended to its
one TMDB request, a series' to the tv request that already fetches its
tagline, `SeriesExtrasProvider`), and they never go through the Plex
provider: PMS keeps no Label from one, so cluster-plex writes them into
Plex's database as the item's Labels (tag type 11, verified live) beside
the markers and streams it already seeds.
Images are chosen per (type, language) before the CRD's 50
(`selectImages`), so the original language's and fanart.tv's survive;
TheTVDB's artworks, a series' own image and a season's list poster
among them, rank by their score, ties going to the titles' language, no
language, then the original (`tvdb.rankArtworks`, `SchemaVersion` 8,
owner 2026-10-28: TheTVDB's own image for an anime is its best English
poster, often never scored -- Cautious Hero's was a title card on black);
`contentRating` prefixes the country the gateway chose the certification
from (`status.metadata.certificationCountry`), and a request naming no
language is answered in `status.metadata.language`. Response customization (`includeFields`/`excludeFields`/
`includeElements`/`excludeElements`, never dropping ratingKey, key, guid or
type) and `episodeOrder` (no seasons for an order clustarr does not store)
are honoured. An episode answers its own `tvdb://` Guid and, since
2026-10-07, its guest cast and crew: after each Series refresh the gateway
lists the series' Episodes from the apiserver by Episode's selectable field
`spec.seriesRef` (no cache, no index -- what the first cut lacked), and for
each with a file whose document is missing or over 30 days old asks
TheTVDB's `/episodes/{id}/extended` (`pkgmetadata.EpisodePeopleProvider`),
at most 300 a pass, heartbeating the delivery
(`app/catalog/metadata/episodepeople.go`); `SchemaVersion` 4 refreshes
every series once to backfill.
PMS also asks every provider for an item's extras on each refresh
(`GET {root}/library/metadata/{ratingKey}/extras`, not in Plex's provider
docs) and reads a 404 or an empty list as "none", deleting the trailers it
holds -- which cost the library its Internet Video Archive trailers on
2026-10-06. The route (`ui/plex/extras.go`) answers Plex's own extras for the
item's (or season's) Plex id, which it asks the metadata gateway for over
`rpc.catalog.metadata.extras` (2026-10-06): the gateway answers from the
file-backed `clustarr-plex-extras` bucket (`pkg/metadata/plexextras`, keyed
by Plex id) and on a miss, or an entry older than 7 days, fetches from
`metadata.provider.plex.tv` with the `plex` MetadataProvider's token and
stores it (`app/catalog/metadata/extras.go`); "none" is stored too, and a
failed refetch serves the stale entry. The ui holds no Plex token. An item
without a Plex id answers none, and a gateway that cannot answer is 502/503,
never an empty list. A show answers its own extras and then each season's
in season order, specials left out, each clip once (2026-11-17,
`extrasPlexIDs`): Plex's service keeps most TV trailers on a season
(Succession: none on the show, five on its fourth season) and PMS stores
none for a season, so 390 of talos's 541 shows had no trailer; one season
that cannot be answered fails the whole list. Extras reach Plex only at an
item's refresh. A movie or a show whose Plex extras hold no trailer answers
its own after them, when the trailers agent has fetched one (ADR-0023,
`ui/plex/trailer.go`): a clip whose part is the file under the root folder
at `.clustarr/trailers/`, named where Plex mounts it (`--plex-path-map`,
`ui.plex.pathMappings`; no mapping, no clip). PMS takes a part's `file`
from a provider, reads the file itself and plays it as a local part
(checked on PMS 1.43.5.11079); a part naming an outside address was stored
and did not play, and a provider path answered 404. The clip's guid is the
video's site and id, so a refresh that finds the same video changes
nothing. The record is a read of the `clustarr-trailers` bucket; one that
cannot be read fails the list.
Skip segments come from TheIntroDB (2026-09-30,
`docs/superpowers/specs/2026-09-30-plex-analyze-bypass-design.md`): for a
probed movie or episode MediaFile whose `status.markers` are due
(`app/catalog/markers.Due`: never fetched, a new probe hash, Found older
than 30 days, Error than 1, NotFound than 7 the first time, then 30, then
90 once TheIntroDB has had nothing for 90 days, from
`status.markers.notFoundSince`) the remediation loop's markers planner
writes a request into the `clustarr-markers` record and publishes a
`catalog-markers` task; the metadata agent's marker worker asks the keyless
`theintrodb` MetadataProvider (a movie by TMDB id, an episode by its series'
TVDB id, season and episode -- only in the aired order TheIntroDB numbers)
with the probe's duration and answers in the record (`records.Answerer`), and
the loop writes `status.markers` under `catalog` (ADR-0016; the
`catalog-markers` field manager is gone, and a deferral to a key's reset is
republished by the loop, ADR-0019). A series TheIntroDB
lacks outright (`metadata.ErrNoTitle`, its `"media not found"` as against
`"... for provided season/episode"`) answers its other episodes without a
request for 24 hours; a file holding several episodes is not asked. Each
key's allowance is its own: Secret `apiKey`, plus `apiKeys` one per line,
spent in order, a spent key resting until its reset; the provider is
probed every 6 hours, since its probe is a counted lookup. cluster-plex seeds them
into Plex, with the probe's streams, straight into Plex's database: Plex's
API can write neither (the spec's §2 is the proof).
clustarr also detects segments itself (2026-10-01,
`docs/superpowers/specs/2026-10-01-segment-detection-design.md`,
`docs/research/segment-detection.md`), so Plex's pods run no detection
(cluster-plex sets `MarkerSource=cloud`): `markers` (`cmd/markers`), a
credential-less binary on the native image, decoding in-process on ffgo
(`pkg/segments/decode`, ADR-0017), takes a season's (or a movie's)
`AnalyzeTask` from the manager's segment planner (`app/catalog/segmentplan`,
through its Episode and MediaFile indexes), and analyzes in Go -- a
Chromaprint port bit-exact against ffmpeg's muxer for season-wide intros
and ending themes, black/entropy/edge/scroll statistics of end frames, chapter
names (Intro Skipper's patterns), and PaddleOCR text density on ONNX
Runtime through purego only when the cheaper signals are weak. Fingerprints
are cached in the `clustarr-fingerprints` object store (versioned keys and
metadata, `segments.FingerprintVersion` 2) and raw results in the
`clustarr-segments` bucket, written by compare-and-swap; its work and
TheIntroDB's ride the durable `CLUSTARR_WORK_MARKERS` stream (gotcha below);
the loop's markers planner merges them into `status.markers.segments` (each
tagged `source` and `confidence`; `pkg/segments.Merge`) under
TheIntroDB per kind, then chapters, then analysis at 60 or more -- except
that a chapter wholly titled as credits ("Credits", "End Credits",
"Ending", "Outro", and since 2026-11-09 anime's bare "ED") outranks
TheIntroDB's credits (2026-10-01: of 107 files with both, 36 disagreed by
more than 5 s, the chapter right in the worst cases), and TheIntroDB credits
that start before the intro ends or cover more than half the runtime are
dropped, leaving credits to the next source (owner, 2026-11-09: TheIntroDB
answers To LOVE-Ru's first season with credits `{start_ms: 0, end_ms:
null}`, the whole episode, so Plex offered to skip the credits as the intro
ended on 21 of 26 episodes; `segments.implausibleCredits`, and a stored
block holding such credits is merged again, `segments.ImplausibleCredits`;
chapter confidence is set in the segments record, so the "ED" rule raised
`AnalyzerVersion` to 4 and every file is analysed once more, its cached
fingerprints kept), and a chapter intro that is the cold open beside an
"OP" or "Opening" chapter is left out (owner, 2026-11-10: To LOVE-Ru
S01E01's Blu-ray chapters "Intro" 0:00-1:29 then "OP" 1:29-2:59 both read as
intros, so Plex offered "Skip intro" over the cold open; `segments.ColdOpens`,
judged at every merge from the probe's `chapterList` titles rather than at
analysis, so it raised nothing, and a stored block holding one is merged
again, `segments.HasColdOpen`) -- in the loop's markers planner, which reads both records
(TheIntroDB's in `clustarr-markers`, the analysis in `clustarr-segments`) and
writes the merge in the loop's one apply (`app/catalog/segmenting.Applier`,
the compare-and-swap path the two used to share, went with ADR-0016's fold).
Raise `pkg/segments.AnalyzerVersion` when detection changes.
The Plex provider (`ui/plex`, ADR-0012) matches a request by the file Plex
names first (2026-09-30, rule 0 in `ui/plex/matchfile.go`): `filename`,
relative to Plex's library folder, is matched as a path suffix of
`MediaFile.spec.path` (`projection.Index.FilesEndingWith`) and answers only
when it resolves to exactly one item; a path two items share, an unknown
file or an unsafe name falls through to the guid and title rules, so the
provider never guesses between two items.
The provider answers items Plex knows with Plex's own GUIDs (2026-10-06,
`docs/superpowers/specs/2026-10-06-plex-native-guids-design.md`): Plex Web
offers Watchlist and Discover only for an item whose own `guid` is
`plex://`, and PMS 1.43.4 accepts one from a custom provider while still
reading the item from it, fetching by the GUID's id
(`/library/metadata/<24 hex>`). A keyed `plex` MetadataProvider (secretRef
key `token`) resolves the ids from `metadata.provider.plex.tv`
(`pkg/metadata/clients/plex`): a Movie's or Series' into
`status.metadata.externalIDs["plex"]` through the gateway's resolvers,
seasons into `status.metadata.plexSeasons`, and episodes into Episode
`status.plexID`, joined by TVDB episode id in the gateway's episode list,
written by the Series reconciler and kept when a later list carries none.
`ui/plex` builds `guid`, `parentGuid` and `grandparentGuid` from them
(`--plex-guids`, chart `ui.plex.plexGuids`, on by default); `ratingKey` and
`key` stay clustarr's, and every ratingKey route also resolves a Plex id
(`projection.Index.ByPlexID`, which refuses an id two items claim), with the
flag off too. This rests on PMS not enforcing its documented rule that a
provider's GUID starts with its identifier; `--plex-guids=false` is the way
back.
Series type `anime` keeps the episode order (2026-10-06,
`docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md`): it reads
absolute numbers for search, import and naming from
`status.absoluteNumber`, and only `spec.episodeOrder: absolute` changes
the order (design §4.2's "absolute forced when anime" collapsed a
multi-season anime into one season). A RootFolder's `defaults.anime` moves
a series whose TVDB genres name it Anime to that profile and type once
(`series.Classify`, recorded in `status.classification`, spec patched
under field manager `catalog-classify`), so the owner's later change
stays; `catalog.clustarr.io/classify: off` skips one, and an import list's
re-apply keeps a classified series' values. Movies have the same once-only
move since 2026-10-20 (`movie.Classify`, Movie `status.classification`): TMDB
has no anime genre, so a film is anime when a genre names Animation (or
Anime) and its original language is Japanese, and the movie folder's
`defaults.anime` (`anime-remux-1080p`, the Radarr anime built-in) gives it
English plus the original in one release. That profile does not graft
(owner, 2026-10-20: a dual-audio release, not a grafted dub) and scores
Anime Dual Audio 1000. A file missing a language a non-grafting policy
wants (`decision.IncompleteAudio`) reads CutoffUnmet, reason
`MissingAudio`, transcoded or not, until a release carrying every wanted
language replaces it. `videoItemState` lets such a release past
TranscodedFinal, the baseline rules and the upgrade table. Import's
`audioFill` takes it only when the file's own probe carries them all, and
refuses it as a releaseFault when it does not. Akira had played
Portuguese: its scanned file carried only Portuguese and Japanese audio,
which `hd-bluray-web`'s `language: original` accepted. A file whose probed audio
lacks the profile's language (`decision.LacksLanguage`; unknown or
untagged audio never lacks) reads `WrongLanguage` and CutoffUnmet, unless
it is transcoded. Without a quality upgrade it is replaced only by a
release whose title names its language (an untagged one merely assumes the
original, the assumption that imported the wrong file) and only when the
new file's own probe carries the language (`importplan.replacesWrongLanguage`),
so it never loops grab, reject and blocklist. The Movie and Episode
item keys wake on a probe's audio languages
(`rollup.AudioLanguagesObject`), and a RootFolder's spec edit wakes its
unclassified series. A profile's `audio` (`languages`, `graft`, `default`;
the anime built-ins want `[en, original]`, grafted) replaces its
`language` in the decision (`decision.audioRejection`): a release carrying
every wanted language is complete, a partial one is a video candidate only
when the profile grafts and the release carries the original language (the
anchor a graft aligns on), and a complete one wins a tie. The built-in
`anime-web-1080p` scores Anime Dual Audio 1000 (2026-10-20; TRaSH 0, or
101 to prefer), past the release-group tiers' spread (WEB 100-600, BD
700-1400), so a dual-audio release outranks every Japanese-only one of its
quality -- the quality tier and a REPACK's revision still rank first, and
a -10000 group stays below the minimum. **Releases are ordered by one
rank score** (owner, 2026-11-03: "all of our current rules impact scoring
- which determines the order. For instance, instead of bumping dual audio
to the top, we should add to the score so that we end up with the same
order"; `pkg/rankscore`): the format score, clamped to +-9,999, plus a
bonus per rule, each larger than everything below it can add -- the
preferred protocol 20,000, a whole season 40,000 (owner, 2026-10-27:
within a quality both rank ahead of the format score, where Sonarr has
them after it), each version past the first 100,000 and each REAL
1,000,000, each tier above the profile's worst 10,000,000, and complete
audio under `completeFirst` 1,000,000,000 -- so the order is the one the
rules made as a chain of tie-breaks (`decision.Rank` through
`RankKey.Score`, `grabplan.better`), with size, episode count, indexer,
flags and seeders breaking a tie after it. Search results carry it as
`ReleaseDecision.score`, which the ui's results table shows, its title
naming the parts (`rankscore.Parts`); the minimum, cutoff and upgrade rules
still read the format score alone. Every built-in prefers usenet (`preferredProtocol: usenet`). A profile's `preferSmallest`
(owner, 2026-10-28: every file is transcoded, so a bigger source is only a
longer download and transcode) ranks the smallest release first right after
the format score and languages, ahead of indexer, flags and seeders
(`decision.less`, `grabplan.better`); the owner's transcode-first profiles
pair it with one 1080p tier, `x265-hd` +300 at a minimum score of 0 (until
2026-11-02 also `10bit` -150, which held x265 10-bit back with H.264 10-bit;
now `h264-10bit` refuses H.264 10-bit, which no GPU here decodes, and
`av1` 0 takes AV1, decoded on the Intel GPU since ffgo
`v0.0.0-clustarr.16`) and a 5 MB-a-minute floor in `sizeLimits`,
since TRaSH's x264 minimums reject a small HEVC encode. Their anime twins
(`hevc-small-1080p-anime`, `-anime-tv`) also zero TRaSH's anime group tiers,
which score 100-1,400 under every score set and would carry a tier-1 10-bit
H.264 past the minimum (`TestAnAnimeTranscodeFirstProfileIgnoresGroupTiers`).
An audio policy's `completeFirst` (owner, 2026-10-28: "We *must* always take
dual audio if it exists") ranks a release carrying every wanted language ahead
of every other key -- tier, revision, season pack, protocol, format score and
size -- as the rank score's largest bonus
(`decision.ReleaseCompletesAudio`); it ranks and never approves, so a
dual-audio 10-bit H.264 is still refused, and it matters only to a grafting
policy, since one that does not graft refuses an incomplete release outright.
Under it the audio also decides before the upgrade table (owner, 2026-11-02:
"Ensure we use dual audio as the highest priority"): a complete release passes
a queued grab whose release languages (`decision.Queued.Languages`) lack one,
a pack included, and an incomplete one is refused by a complete grab
(`queueRejection`, `decision.AudioOutranks`); a file known to lack a wanted
language is `decision.ReplaceableAudio` -- CutoffUnmet, reason
`MissingAudio`, transcoded or not, replaced by a complete release (import's
`audioFill`), its graft only the fallback -- and an incomplete release never
replaces a file carrying them all (`ExistingHigherPreference`). Only a
dual-audio single downloading holds a pack that is not dual audio back, and
its season against the automatic season search (`grabplan.Holds`,
`holdsSeason`; every other single a pack replaces, above). Remake our Life!'s
EMBER dual-audio pack had been refused for SENPAI's Japanese-only singles, one
1080p tier and a format cutoff of 0 making each "equal or higher preference";
2,068 episodes of `hevc-small-1080p-anime-tv` held Japanese-only files, their
grafts still searching. Only a non-grafting policy's import blocklists a file
lacking a language as mislabelled (`decision.IncompleteAudio`).
**A tier can be a last resort (owner, 2026-11-26, for `hevc-small-1080p-anime-tv`:
720p "*should* be blocked and should also not meet the quality cutoff *even*
when transcoded. The only time a 720p release should be allowed is if there
are literally no other releases that exist. But in this scenario, season
packs, episode packs, etc should take precedence and block an episode release
if one is found that is 1080p").** `Tier.lastResort` marks it; such a tier is
below the cutoff and every tier under it is one too (`quality.FromCRD` reports
anything else; `Profile.LastResortFrom`, `LastResort`, `Outranks`). Four rules
follow, each a person's pick is exempt from:
- its audio earns it nothing: a release of it is never "complete" in the
  rank score, the queue rules or the audio shortcut (`videoItemState`,
  `queuedComplete`, `ReleaseCompletesAudio`), so a dual-audio 720p release
  ranks below every 1080p one and replaces or passes none. Among the tiers
  that are not a last resort dual audio still decides first (2026-10-28
  stands: a dual-audio remux is taken ahead of a Japanese-only encode);
- it is grabbed only from a search's answer: the grab planner refuses an RSS
  match or a pushed release of it (`reasonLastResortAlone`), since one
  release says nothing of what else exists. The release stays in the release
  index, so the item's next search, or its season's, judges it beside
  everything known, at the wanted sweep's backoff;
- it is refused while a release of a better tier approved in the same pass
  would be taken for an item it brings, a pack included, whether that one is
  grabbed, delayed or waiting on its indexer's grab limit
  (`decider.outranked`). A better release the rules refuse for good (blocked,
  under the minimum score, over the size limit) does not count as existing;
- a file of it is never final: the Episode and Movie reconcilers read it
  CutoffUnmet, a transcode included, and a release of a better tier passes
  the file's audio and `TranscodedFinal` to the upgrade table at the decision
  (`overLastResort`), in `grabplan.StillWants` and at import (`gate`), where
  a file of such a tier also fills nothing by its audio or by coming in a
  pack. A release of the same or a worse tier still meets a final transcode.
The same request put Remux-1080p in that profile as its own tier under the
1080p encodes, at the cutoff: taken last of the 1080p releases, and a remux
file is not replaced by an encode (the transcode encodes a remux like any
H.264 file). Tests: `pkg/decision/lastresort_test.go`, `pkg/quality`,
`grabplan` and `importplan`'s `lastresort_test.go`, the Episode envtest's
"a transcode of a last-resort tier" subtest; the Movie reconciler's identical
block has none.

**A custom format can be a last resort (owner, 2026-11-27, for the anime
low-quality groups: "low-quality-group encodes should be allowed as a last
resort when nothing else exists").** `QualityProfile.spec.lastResortFormats`
names catalogue slugs (`anime-lq-groups`). A release those formats alone put
below `minFormatScore` is not refused for its score
(`Profile.LastResortScore`, `qualityRejections`); one below the minimum for
another format as well (a raw, a dub alone) is refused as before. Its score
is unchanged, so it ranks under everything else. The grab planner holds it to
the last-resort rule of a tier (`decider.lastResort`): an RSS match or a
pushed release of it is refused, and from a search it is refused while any
approved release that is no last resort would be taken for an item it
brings, whatever that one's tier, a pack included. A person's pick is
exempt. Not carried over from the tier's rule, my default: its audio counts
as any release's, and a file of it is final once transcoded like any other,
so a better release is taken over it only by the usual upgrade rules.
Seasons this was for (talos, 2026-11-27): Altair, Yuuki Yuuna's third,
Sacrificial Princess, every candidate an ASW or Sokudo encode. No settings
form field yet: the list is set in the profile's manifest.
**An anime season search asks a text indexer for the season's own names
(owner, 2026-11-27: "The sixth season should also show [SubsPlease] Lupin
III - Part 6 (00-24) (1080p) [Batch]").** An anime season's text is the
series title alone, with no season (its packs are named by absolute range
or "Batch"), and a text indexer answers one page of that, newest first.
For a franchise the page holds none of the season's packs: nyaa has 809
"Lupin III" torrents in its English-translated category and returned 75,
neither Part 6 batch among them, while the search of one of the season's
episodes ("Lupin III 2", 455 results) happened to return both. That was
luck, not a rule. The season search now sends further requests
(`search.seasonNamed`, merged as `otherSeasons`' are), each with one of
`TargetIDs.SeasonTexts` as its text: TheXEM's scene titles of the season,
then the series' alternate titles that end in the season's number, in
digits or as a Roman numeral ("Lupin III Part 6", "Lupin III Part II"),
three names at most, and "<title> Snn" for a season past the first. They
carry `SearchRequest.TextOnly`, so the indexer agent skips every indexer
that takes the series' id (`resolveQuery`, `skipTextOnly`): those answered
the first request by id, and their queries are not spent twice. What comes
back is judged by the search's identity like any release. Cost: up to four
more queries of each text indexer per anime season search, at its
`requestDelay`. A standard series is unchanged: its season goes on the
wire as a parameter.
**An indexer's id does not carry a release whose title names something
else (owner, 2026-11-27: "This obviously needs fixing and warrants a deep
investigation into how we search and filter. I've been seeing many random
and incorrect responses, some of which are even getting approved as
releases").** Two things skip the title check for a series' release: the
release carries the series' tvdb id, or its indexer answered a query for it
(`Identity.IDQueryIndexers`). Both are the indexer's catalogue speaking,
and it is sometimes wrong: the 22 best-ranked answers for Lupin III's
third season were the live-action "Lupin" (Netflix, 2021), which amenzb
files under the anime's tvdb, tmdb and imdb ids, and 17 of Unlimited
Psychic Squad's were Takopi's Original Sin. The sample behind the rule: 20
look-only season searches of random monitored anime series (the tv half
of the sample never folded: see the manager's CPU below), 3,482
candidates, 3,016 approved; 318 approvals had a title matching none
of the series' titles or TheXEM names, 276 of them the series under a name
the titles do not list (a year, a season marker or a group left in the
title; "Romaji (English)"; an unlisted romanisation) and 42 another show,
every one from amenzb with the id carried. `decision.vouchContradiction`
now holds an id to the title, word by word against every name the series
has (titles and scene season names): a title that is a name, holds one
whole, is one less a year or a country, or shares a word with one without
being its start, is believed; one that is only the start of a name
("Lupin" of "Lupin III": what follows a name's start is what tells a
sequel or a namesake from it) or shares no word with any is `WrongItem`.
Replayed over the sample it refuses the 42 and nothing else. A title in
another script has nothing to compare and keeps the id; a movie is left to
its ids and year. The cost: a release named only by the first words of a
long title, or by a romanisation unlike every listed name, was carried by
the id and is refused now; the name belongs in the series' titles.
**An episode's title carries its second name**
(`release.withSecondNames`): "[Group] Romaji (English) - 25", or either
side of a bar, as a pack's already did (`animeTitles`). Of the 53 releases
the sample refused for their title, all from the one text-query indexer
(nyaasi), 40 were the series under such a name; 33 match now, and the 20
left are 17 right refusals (Detective Conan, Magia Record, Lupin's films)
and 3 forms not read ("A - B", "[v2] A", a trailing "1"). A release with
several names is numbered under the one that is a season's scene name
(`sceneIndex.namingTitle`), the same one that places it: "[KaiDubs] JoJo's
Bizarre Adventure: Golden Wind (Part 5) - 02" is also "... Golden Wind",
so it is season 4's second episode, where the day before it was placed
nowhere and, before that, approved for S01E02. Other findings
of that pass, not changed: the manager spent 63% of its CPU decoding rows
(`rowcache.(*codec).decodeInto`, half of it under the item reconciler's
search stage) at its 750m limit, so 30 interactive searches answered by
the agents in seconds were still unfolded 30 minutes later and titles
timed out being read; and "DUAL" on a series whose original language is
Japanese is read as Japanese and English, which ranked the live-action
releases first. Tests: `TestAnIndexersIDDoesNotCarryATitleThatNamesSomethingElse`,
`TestAnIndexersIDStillCarriesANameTheTitlesDoNotList`,
`TestAnEpisodesTitleCarriesItsSecondName` (the first falsified).

**A bare number under a title the series does not know is placed nowhere
(owner, 2026-11-27: "that's season 5 --- definitely should not be an
approved S01E02 release!", "Ensure that only the correct season is
selected. Either Season 1 or 'Phantom Blood & Battle Tendency'").** JoJo's
Bizarre Adventure (TVDB 262954; six seasons, each counted from 1 by the
scene as by TVDB): `[Erai-raws] JoJo no Kimyou na Bouken - Stone Ocean
Part 2 - 01 ~ 12` and `[KaiDubs] JoJo's Bizarre Adventure: Golden Wind
(Part 5) - 02` were approved for S01E02, and the first was taken for
S01E02 and S01E03 twice in two minutes (23:15Z and 23:16Z, two postings on
amenzb), each a pack that displaced the live download of the right
episode; both failed as missing articles, so nothing was imported. Two
standing rules met. An anime episode is asked for by the series' id and
its number with no season, so an indexer that takes ids (amenzb,
animetosho, added that day) answers with that number of every season; and
a release from an indexer asked by id passes the title half unread
(`Identity.IDQueryIndexers`). Its number then counts in the season its
title names (`sceneIndex.sceneSeason`): one of TheXEM's names is that
season, and ANY other title was the series' own, the first season. Neither
title is a TheXEM name (it lists "JoJo no Kimyou na Bouken: Stone Ocean"
and "JoJo's Bizarre Adventure - Golden Wind"), so both were season 1's.
Now `sceneIndex.places`: on a series whose seasons have scene names, a
bare number is read only under a title that is one of those names or one
of the series' own (`Identity.Titles`, keyed with the year as the title
half keys them, and TheXEM's names for every season at once, which
`SceneReadings` now passes as `SceneSeasonName{Season: 0}`); under any
other it is left out of the coverage (`Coverage.Unplaced`), neither mapped
nor read literally, and `numberingRejection` answers UnknownItem naming the
title. Not placed in the later season either, my call: "Stone Ocean Part
2 - 01" is the season's episode 13 and "Golden Wind (Part 5) - 02" its
episode 2, and nothing tells a cour's count from a season's. Replayed over
the 3,842 JoJo titles in the release index: 1,661 carry a bare number,
1,501 are placed (203 in season 1, 143, 463, 515, 80, 97) and about 130
are not, every one a later season's under another spelling ("Stone Ocean
Part 3", "Stardust Crusaders Egypt Hen", "Golden Wind (Part 5)"); all of
those were season 1's before. Unchanged: a series with no scene season
names, an identity that carries no titles (the import agent's: a file is
read by its download's episodes, and files inside a rightly taken pack
carry titles of their own), SxxEyy and "Show S2 - 05" releases. Tests:
`TestABareNumberUnderATitleTheSeriesDoesNotKnowIsPlacedNowhere`,
`TestAnEpisodeRefusesAReleaseOfAnotherPartOfItsSeries`,
`TestABareNumberIsReadAsBeforeWithoutSeasonNames`.
**An indexer kept to anime is asked for anime alone (owner, 2026-11-27:
"ensure the amenzb and anime tosho indexers are only combined with the
other usenet indexers for anime series. otherwise, it will be a collosal
waste of resources, rate limits, etc").** A search went to every enabled
indexer whose advertised categories overlap the request's, and a parent
category survives when the indexer serves any child of it
(`queryCategories`): amenzb advertises the whole Movies and TV trees and
animetosho Movies (2020) and Anime (5070), so every film and every
standard series search on talos spent a query of each. `spec.categories`
never narrowed that: only the RSS poll reads it. `Indexer.spec.animeOnly`
(default false) now leaves the indexer out of a request that is not anime
(`selectCandidates`): not a candidate, with no outcome, so a search whose
other indexers were all paced or in backoff still reads as no attempt
(`allPaced`, `noIndexer`). A request that names the indexer
(`IndexerRefs`: an interactive Search of chosen indexers, the Torznab
facade) asks it whatever it is for; its RSS poll is unchanged, one query
an interval. Anime is `SearchRequest.Anime` or the older absolute-number
shape (`isAnimeRequest`): a Series of `seriesType: anime`, and, new with
this, a Movie whose `status.classification.anime` is set
(`snapshot` to `TargetIDs.Anime` to `BuildSearchRequest`), so an anime
film is still searched there and each indexer's `animeCategories` are
added to its query; `Target.Anime` stays a series'. Not set on nyaasi (my
call: the owner named the two usenet indexers, and nyaa carries live
action too). Tests: `TestAnAnimeOnlyIndexerIsAskedForAnimeAlone`,
`TestAnAnimeFilmIsSearchedAsAnime`.
**Raws, dubs alone and releases naming no quality are last resorts too
(owner, 2026-11-27: "Allow dubs-only (or vice versa - *with* the graft),
tentatively allow "unknown" quality releases - but we should find out the
quality after download to determine if we'll keep it (or from metadata
etc), allow raws").** About 190 missing anime episodes had nothing else.
Three different mechanisms:
- *A raw* is refused by its format's score alone, so the profile names
  `anime-raws` in `lastResortFormats` and no code changes.
- *A dub alone* (a wanted language without the original) was refused twice:
  by two -10000 formats, `dubs-only` (its title) and `language-not-original`
  (its languages), which the profile names as last resorts, and by
  `audioRejection`, which took a partial release only when it carried the
  anchor a graft aligns on. A grafting policy now takes it
  (`decision.dubAlone`), and the planner holds it to the last-resort rule
  whatever its formats (`decision.DubAlone`, `decider.lastResortFormat`),
  so one whose indexer names English and whose title names nothing is held
  too. A policy that does not graft refuses it as before, and so does an
  anime MAL-Dubs says has no dub, whose English is not wanted (`ForDub`).
  **Its graft runs the other way**: the file's missing language is the
  original, and the anchor is the dub it carries (`rollup.GraftAnchor`,
  read by the donor search, the donor's import and the graft planner, so
  the three name one track). Its donor is a release carrying that dub and
  the original, an H.264 10-bit dual-audio one included, since a donor is
  judged on its languages alone. A file carrying no wanted language has no
  anchor and no donor search. Not changed: such a file is `MissingAudio`,
  below the cutoff, and a complete release replaces it; a release of the
  original alone replaces it only while it is not transcoded
  (`upgradeRejection`'s `LacksLanguage`), since `TranscodedFinal` still
  holds a transcoded one.
- *A release naming no quality* is `Unknown`, which a profile takes by
  listing it in a tier: `{name: Unknown, lastResort: true, qualities:
  [Unknown]}`, last. The import then reads the file: after
  `AugmentFromMediaInfo`, `quality.ResolveUnknown` makes an unknown source
  HDTV at the probed resolution (SDTV below 720p), the parser's own rule
  for a title naming a resolution alone, and the profile judges that. A
  1080p file is kept, a 720p one is kept as the last-resort tier it is, an
  SD one is refused as the release's fault and blocklisted. Only the
  import of a download does this; the scanner and the renamer keep ruling
  R13 (an unknown source stays `Unknown`). A file whose probe gives no
  frame size stays `Unknown`, in the last-resort tier, never final.
  **The tier as first rolled (sha-6f8a8fb) held nothing**: a tier matches a
  video quality by source, resolution and modifier, the table's `Unknown`
  names no source and a parsed one names `unknown`, so Yozakura Quartet's
  batch was still "Unknown is not wanted" minutes after the profile took
  the tier. `Definition.holds` now takes an unknown source at any
  resolution. I had tested the import half through the planner's harness
  and the grab half not at all; its test builds the profile from its
  manifest (`TestAReleaseNamingNoQualityIsALastResortUnderAProfileThatListsUnknown`).

**A season is satisfied by as many packs as it takes (owner, 2026-11-27, of
How a Realist Hero Rebuilt the Kingdom 1x14-26: "we should download and
extract *both* packs to satisfy the season. Each pack will account for the
files it provides for things like downloading statuses etc. This should be a
rule when searching/selecting releases for a full season").** TVDB lists the
show as one season of 26 and its releases come as two parts of 13. Three
usenet packs named "S01" were grabbed in turn for 14-26, each held 1-13,
was held at import over the files those episodes already had, and went on
covering 14-26 for the 24 hours of its hold; EMBER's "(Season 1 | Part 02
[EP: 14-26])", the release that held them, read as the whole season too and
ranked fourth, under the usenet packs. Three rules, each in its own place:
- **a pack that states its episodes is a pack of those**
  (`release.stateEpisodes`, both series types): "[EP: 14-26]", "(Episodes
  01-13)", "Eps 1~12" beside one season marker make `Episodes` the range,
  `Partial` true and `FullSeason` false, so `decision.CoverageOf` names the
  pairs. Two parts overlap nowhere and one grab pass takes both, each entry
  for its own episodes; a part is still refused beside a pack of the whole
  season (`reasonOverlapsPass`, one release per episode). Only a worded
  range counts: "Part 02" alone, a year range or "S01-S02" change nothing;
- **an entry accounts for the files it holds** (`importplan`'s `provide`,
  `lifecycle.ImportDecision.Episodes`): the first inspection is the first to
  know which episodes a pack's files are, and from it `status.downloads[]
  .episodes` are the ones the entry was grabbed for and holds, or, when it
  holds none of them, the ones its files are. Only that first inspection is
  read (once an execute has answered, a later one lists what is left of the
  payload), and only when every file is attributed: a file nobody could
  attribute may be any episode the entry names, so such a pack keeps its
  episodes until a person says or its hold expires (iAHD's "S01-S02", whose
  second half is named S02E01-13, is that case and is not helped). A person's
  target, a donor and a set are left alone;
- **the episodes it did not hold are searched again at once**
  (`lifecycle.Plan.Freed`, the downloads stage's `followUps`,
  `remediation.GrantSearch`): each still wanted one is granted a live search
  in the BacklogSearch grant book for `remediation.FollowUpLapse` (3 h), and
  two or more make their Series hold the season's grant, so its automatic
  season search asks again (`SeasonFacts.BacklogDue`; within the hour's
  `AutomaticCooldown` after an automatic one, at once after a requested
  one). The held pack is "already a grab of the owner", and since the same
  day holds none of its episodes against another release
  (`docs/claude/gotchas.md`, "A download held at import holds nothing"), so
  the answer's
  next release is taken. The book is the leader's: a grant lost with it
  leaves the episodes to their own backoff.
Not built, my choices: nothing prefers a release that states its episodes
over one that only claims the season, so several mislabelled packs are
still tried one after another, a download each; and a pack is never taken
beside another for episodes both claim. Tests: `pkg/release`'s
`statedrange_test.go`, the coverage corpus in `pkg/decision`'s
`seasonsearch_test.go`, `grabplan`'s `parts_season_test.go`, `importplan`'s
`provide_test.go` (through the real inspect worker), `lifecycle`'s
`provide_test.go`, `followups_internal_test.go` and
`grantsearch_internal_test.go`. Not proven end to end: no engine ran.

**An anime wants English only when the MAL-Dubs list names it** (owner,
2026-11-03: English dubs and dual audio "*only* for series who's
status.externalIDs.mal exists within" github.com/MAL-Dubs/MAL-Dubs'
`dubInfo.json`; `pkg/dubs`). The manager's leader fetches the list once a
day by its ETag (`dubs.Refresher`) into the durable `clustarr-mal-dubs`
bucket, and the manager and every agent watch it into an index on their root
context (`dubs.WithIndex`, as `pkg/grouplang`'s). An anime -- a Series of
type `anime`, a Movie its classification calls anime -- is `Dubbed`,
`Incomplete` (the list's `incomplete`: a simuldub), or `NotDubbed` (no MAL
id, or one the list does not name); anything else, and every item before a
list was read, is `Ungoverned`. `decision.ForDub` makes the state the item's
audio policy wherever its profile is resolved (`itemread.ItemProfile`, the
Episode and Movie reconcilers, the downloads stage, the search worker's
snapshot, the RSS matcher), and the remediation loop's `dubs` source
(`dubSource`) re-plans at low priority the anime a list change moved -- every
one on the first list a process reads, then those whose MAL id changed
state -- since nothing else wakes an item when the list changes (rev 177
loaded the list and moved none of 864 not-dubbed episodes): NotDubbed drops English from the languages the
policy wants, so no dub, donor or dual-audio release is searched for or
ranked first; Incomplete moves it to `quality.Profile.AudioOptional`, so a
file without it meets the cutoff and a release without it is not refused,
while a release with it still ranks first (`LanguagesComplete`) and replaces
a file lacking it (`decision.AudioUpgradeable`), and `status.audio` keeps it
missing, so the donor search looks for it on the wanted backoff (weekly
from the sixth try; IndexOnly after the first). An English original is
still wanted through `original`, and the K-dramas' dual-audio profile, whose
series carry no MAL id, is untouched. On talos then: 383 dubbed, 2
incomplete, 97 not dubbed and 26 without a MAL id of the 508 anime series;
27 of 28 anime films have one.
**A dubbed anime's poster says so** (owner, 2026-11-17: "a simple overlay
for all dubbed series denoting that the anime is dubbed"): an anime the list
names `Dubbed` -- a Series or a Movie, the same state the audio policy wants
English for -- carries the dub mark, a box reading `DUB` in the score's face
(`overlay.DubBadge`; Plex's episode-count box is itself a box of text), drawn
after the rating badges of the OverlayProfile that selects it and standing
alone when no badge has a rating (`overlayplan.Plan`), which is how nearly
every anime carries it: talos' one profile draws Metacritic, and no anime
there has a Metacritic score, so none had an overlay before. It is no
profile field and no `RatingSource`: a profile says which items carry an
overlay and where, and an item no profile selects carries no mark.
`Incomplete` is not marked (its English is optional, so its files may have
none), and the mark is the list's word, not a probe of the files. It is an
input of the render (`overlayplan.InputsDigest`, a `dub=` line only when
drawn, so no unmarked poster's digest moved and none was re-rendered), and a
list change re-plans the item through the `dubs` source as it does the audio
policy. **An anime's overlay is not judged before the process has read the
list** (`dubs.Index.Unread`, `overlayplan.Item.DubUnread`): read then, the
anime is `Ungoverned`, which plans as not dubbed, and a manager or agent
that started ahead of the bucket would have every marked poster removed and
drawn again -- a poster change Plex refreshes for. The overlay stage keeps
status.overlay as the object says and publishes nothing, the OverlayProfile
controller skips the item, and the renderer retries the task
(`ErrDubUnread`, 30 s); the first list read wakes every anime.
**An anime's score is MyAnimeList's, not Metacritic's** (owner, 2026-11-25:
"pivot from metascores for anime overlays to mal scores"). The score is
rating source `myanimelist`, from the response MDBList already gives
(`pkg/metadata/clients/mdblist`: out of 10 to one decimal for an anime, null
for anything else; 86 read 8.3 with 533,936 votes and no Metacritic), so no
provider and no request was added; it draws as the other 0-10 scores do,
"8.3", under Kometa's MAL mark. A profile takes it for anime alone through
`OverlayProfile.spec.anime` (`only`, `exclude`, unset for both;
`overlayplan.Selects`, by `itemread.Anime`: a Series by its type, a Movie by
its classification), because nothing on a Series or Movie says anime to a
label selector: one profile `anime: only` drawing `myanimelist`, the other
`anime: exclude` drawing `metacritic`
(`config/samples/catalog_v1alpha1_overlayprofile_anime.yaml`). An anime film
with both scores, as 31 of talos' 34 had, shows MyAnimeList's. The selection
is no render field and moves no profile hash. A Movie's and a Series'
`status.metadata.ratings` went from seven entries to eight with it: the list
is cut by source name, so at seven an anime film with every score would have
lost trakt. The CRDs go on before the images, as ever: an eighth rating is
refused by the old schema.
**A series is rated from TheTVDB and TMDB together, and by translation when
no rating it can show was published** (owner, 2026-11-27: "Why do many anime
series not have a content rating? Can we retrieve this from tmdb? e.g.
Maria the Virgin Witch", then "Build it and also translate foreign only
ratings like MA 15+ as TV-MA"). A series' ratings were TheTVDB's alone and
its rating the region's, else the origin country's, else the US one: 210 of
752 series on talos had none, 168 of them anime (TheTVDB lists Germany's
16+ alone for Maria). Now `tmdb.SeriesExtras` asks for `content_ratings` in
the request the tagline, keywords and images already come in, the gateway
adds those countries to the series' list, TheTVDB's kept where both have
one (`metadata.MergeCertifications`, `enrich.withExtraCertifications`), and
the rating is chosen again in the same order with one more step
(`metadata.PickSeriesCertification`): another country's rating read on the
US television scale (`TranslateCertification`, table `tvTranslations`:
Australia, New Zealand, the UK, Canada, Brazil, South Korea, Singapore,
Germany, Austria, the first that has a rating it knows). A translated
rating is stored as `certification` with `certificationCountry` "US" and
**no US entry in `certifications`**, which stays the published list: that
is how a reader tells it, and the Plex provider shows it unprefixed while
a client in a country with a published rating still gets that one.
The table is each rating's own meaning (G and PG keep their word, an age is
its age), and data only where a country's 15 or 16 could be TV-14 or TV-MA:
South Korea's 15 is TV-14 (198 series against 64), Australia's MA 15+
TV-MA (170 against 33). One entry is the owner's ruling against its word
("Australia's PG -- map to TV-14", 2026-11-27: the series carrying it are
TV-14 in the US 42 times against 25 TV-PG). Measured on the 687 series with
a US rating and a rating the table reads: the same rating for 82 in 100, a
neighbouring one for 99 in 100. Taiwan is left out (its 6+ and 0+ sit on TV-14 series). Of
the 210, TMDB's US rating answers 158, a published origin-country rating
5, a translation 45 (41 through Australia: 34 TV-14, 10 TV-MA, 1 TV-Y) and
2 have no rating anywhere; 9 series rated from their own
country (eight Korean, one German: "kr/15" in Plex) take TMDB's US rating. A film is never translated: its
ratings are cinema scales (`PickCertification`). It is Series'
`SchemaVersion` 10, and **each kind has had a version of its own since**
(owner, 2026-11-27: "Can we split this for each type?" -- one number for
all eight kinds meant this refreshed the roughly 6,900 Albums too):
`metadata.SchemaVersion(kind)`, every other kind at the 9 they shared, the
L2 cache key and a refresh's inputs carrying the kind's. Raise the one
kind that learned the field. The region and origin the gateway chooses by are the TMDB
provider's and the tv record's; a TMDB MetadataProvider with another
`spec.region` than TheTVDB's would choose by its own.
**Anime is becoming a library of its own, by root folder** (owner,
2026-11-25; design of record
`docs/superpowers/specs/2026-11-25-anime-library-design.md`, its §10 says
what is built). The rule: an anime root is a series RootFolder whose
`defaults.seriesType` is `anime` (`RootFolderSpec.AnimeRoot`), and a Series
is anime when it is stored under one; anime films stay in Movies. Built so
far (step 1, not deployed): `defaults.anime.rootFolderRef` on a series root
moves a newly classified anime to the anime root in the classification's one
spec patch, before the episode fan-out, so nothing is searched or imported
under the TV root first (`series.AnimeRoute`); a Series under an anime root
is recorded anime with no detection (`series.Classify`), which is what keeps
`status.classification` derivable between the classification's two writes;
an anime whose destination is missing or is no anime root is held
(Ready=False `AnimeRootFolderNotFound`, no Episodes) rather than classified
without the move. **A list places an item once and never moves it**
(`importlist.applySeries`, `applyMovie` send the root folder the item has):
until this, every sync forced the list's `defaults.rootFolderRef` onto
everything it had added, so re-pointing a list would have re-rooted all of
it with nothing moving the files. **The ui has an Anime tab** (step 3 in
part, not deployed): `projection.TabAnime` after TV; a Series is carded on
it when its root folder is an anime root, whatever its series type
(`buildLibraryItems` with `AnimeRoots`), so until the existing anime are
moved they show on TV; `RootFolderOnTab` puts every RootFolder on exactly
one tab (a series folder on TV or Anime), which is what Rescan and Add New
read; Add New on TV offers `standard` and `daily`, on Anime no type, and the
handler holds a posted type to the folder. The projection lists and watches
RootFolders for it (one more List a round).
**A file's history follows it across a rename or a move** (owner,
2026-11-26: "Carry the history across a path-only move"; the anime library
design's §8a and §10). The probe hash is `sha1(path|size|mtime)`, so a
change of path alone moves it, and until this every rename read as new
bytes to the stages keyed by it: subtitles reset every language's attempts
and searched again, markers were fetched and the segments analysed again,
a Failed or Skipped transcode verdict was made again. Now
`status.formerProbeHashes` (four at most) keeps the hashes the same bytes
had; the probe planner appends to it at a path-only change
(`mediafile.PlanProbe`) and clears it when the bytes change; and the file
loop, right after the probe planner, re-points every block at rest to the
new hash (`MediaFileStatus.CarryBytes`, in `v.Prev` and the draft) so the
later planners of that same pass see their block as current and one status
write stores hash and blocks together -- no stored status ever names the
new hash beside a block under the old. A block with work in flight is left
to its stage. **A planner compares its block with the draft's probe hash,
never the stored one**: one that read `v.Prev.ProbeHash` would see the
carried block as changed. The segments record and the fingerprint cache are
keyed by hash outside the status, so the markers planner
(`markers.Adapter.input`), the analyze task (`AnalyzeFile.formerProbeHashes`)
and the worker (`worker.record`, `worker.window`) take a former hash for the
file's own. The mediafiles CRD goes on before the manager that writes the
field.
**A re-rooted series' files follow it, one at a time, through the rename
actuator** (anime library design §3 and §10; not deployed). Not a folder
rename by an agent, which the design first described: the file loop's
rename actuator already moves a file and records its path in one guarded
step, so `rename.Actuator.relocation` adds a second kind of move ahead of
its rename gate -- an episode's file under another series root folder than
its Series', to the same place under the Series' new folder, name kept --
and `specwrite.RelocateFile` shares `RenameFile`'s move-and-record. No root
folder's rename switch decides it; it waits for the file to be Ready and
Probed, for no transcode or graft in flight, and for no LibraryScan over
the folder it leaves or the one it goes to; a target that exists is never
replaced. The Series reconciler still renders `status.path` under the
current root at once, so new imports land in the new folder while the old
files drain; a scan of the old root refuses them (`other_root_folder`).
The Series is read only for a file whose path and proposed name are not
under one root folder, so a library at rest pays the cached RootFolder list.
**Move is how a series changes library** (not deployed): a bulk action on
TV and Anime (`Server.moveSeries`, `moveTargets`, the bulk bar's Move
dialog) that patches `rootFolderRef`, the series type the folder makes it
and the folder's default profile in one `EditItem`, after which the files
follow by the relocation above. The Edit dialog no longer offers `anime`
(`views.seriesTypeChoices`): none on Anime, `standard` and `daily` on TV,
and an anime not moved yet keeps its type as a third choice; the handler
drops a type posted for a series on Anime and refuses `anime` elsewhere.
Still not built: the Plex library.
An Episode carries two conditions, Ready and DeadLettered (owner,
2026-11-03, cutting the manager's memory): the Aired, HasFile, CutoffMet and
WrongLanguage conditions every episode carried restated `status.phase`,
`hasFile` and `cutoffMet`, and their five structs per episode were 26 MB of
the manager's cache for 27,558 episodes. What their readers needed is now
`status.cutoffReason` (Pending, Transcoded, ProfileUnresolved, WrongLanguage,
MissingAudio, BitrateOutOfRange, UndecodableVideo, CutoffMet, CutoffUnmet;
the season search's `replacesAsPack` reads it) and `status.wrongLanguage`;
the Episode reconciler drops the retired four from an episode still carrying
them on its first pass (`retiredEpisodeConditions`), since an apply that
copied them from the cache would keep them for good. Movies keep theirs
(1,050 objects). The tuner keeps no guide in memory between renders (same
day): a render drops the parsed programmes once the guides are built and
reads them back from the guide cache object when needed, and a device serves
`xmltv.xml` from the store (`GuideObject`, or `GuideBlockObject` for a block
that does not hold the whole lineup), its ETag answered from the stored hash
without a read.
anime-web-1080p ranks Bluray-1080p, then WEB 1080p, then Remux-1080p,
below the cutoff, so an encode replaces a remux, which is never transcoded
(`demotedTiers` exempts that tier from the best-first guard). A bare scene WEB is a WEB-DL
(`release.sourceRegex`, 2026-10-26: `1080p.WEB.h264-GROUP`, and the
anime `[WEB]`, `(WEB 1080p`, `(BD 1080p` and `BD1080p` fallbacks); the
first port read every one as quality Unknown, which every profile
rejects, 20 of A Model Family's 26 episode results among them. A title
naming a resolution and no source is HDTV at that resolution, SDTV below
720p (*arr's last fallback, 2026-11-02): fansubs name only the resolution
("[SubsPlease] ... (1080p)", "[Cleo]...Dual.Audio.10bit.1080p.x265"), and
Somali and the Forest Spirit's dual-audio Cleo episodes read Unknown. A title
naming neither stays Unknown: nzbgeek's and nzb-life's APIs report a
release's audio and subtitle languages from its MediaInfo but no video
(`t=details&extended=1`: no `video`, `resolution` or `framerate`), and the
details page carrying it needs a web login. A file
named for its episode alone parses, as Sonarr's "episodes without a title"
do (`release.titlelessEpisodeRegex`, 2026-10-27): "S01E01 - Title.mkv",
"1x05", leading bracket tags passed over. It parses with no title, so the
import attributes it by its numbering, and when its name names no source it
takes the release title's quality, revision, group and languages
(`fileimport.fillFromRelease`, Sonarr's aggregation). A download's one
video file whose own name attributes it to no episode of the series takes
the release title's numbering when that names one (`plan.sceneParsed`,
`fileimport.takeNumbering`), and a person's named episode takes such a lone
file whatever its name says; a pack's numbered files keep their own
verdicts. A resolution is never an episode number (`release.notResolution`):
GUACAMOLE's "gua-drifters.2016.e08-1080p.mkv" read as absolute episode
1080 (talos, 2026-11-02), and all eleven files were held for a person. The usenet engine
names a file whose subject quotes an empty name (`- "" yEnc`) by the
file-name-shaped token in it, as SABnzbd's name_extractor does
(`usenet.fileName`), never by the subject's wrapper. A Sign of Affection's
EMBER pack had all twelve files held as unparseable until then. A movie's
edition (MediaFile `spec.edition`, which cluster-plex's projection writes
as the Plex edition since 2026-10-27) is read after its title, never from
it: "Uncut Gems" had been frozen as the Uncut edition. The parser
reads a release's audio markers past Sonarr (2026-10-19,
`release.namedLanguages`): a dual-audio title -- "Dual Audio"/"Dual-Audio",
`[DUAL]`, or a bare DUAL after the resolution or right after the episode or
year -- is English beside what it names, or beside the item's original
language when it names nothing else (`ParsedRelease.DualAudio`,
`LanguagesFor`); a bare DUBBED is English; a JA+EN-style code pair names
both. Every reader takes that one reading -- the decision, the Search
results the ui lists, import's frozen `spec.languages` -- under every
profile; until then only the decision knew, under an anime score set, so a
dual-audio release showed Japanese alone. Outside anime, DUAL means the
original language plus the group's own language (2026-10-20: PD's "DUAL" NF
WEB-DL of Akira carries Portuguese, as PD's file in the library did). So a
DUAL title that names nothing else reads as the original plus the group's
language (`ParsedRelease.LanguagesWith`). That language comes from
`pkg/grouplang`'s index, learned from imports, or else
`release.builtinGroupLanguages`, seeded from the library's evidence. The
manager's downloads stage records every inspected file's pre-transcode
audio against its release group (`grouplang.Observe`), refused imports
included, in the durable `clustarr-release-groups` bucket. Only a file with
exactly one language beside English and its original votes, and a group's
verdict needs 2 votes, two thirds of the votes and a third of its files.
Each binary puts the index on its root context (`release.WithGroupLanguages`),
so every reader resolves through `release.GroupLanguagesFrom(ctx)`. The
parser also reads Sonarr's original-language scene tags
(`ParsedRelease.NamesOriginal`, resolved by `LanguagesFor`): German beside
DL (never WEB-DL's) or ML, and French (VF, VFF, VFI, VFQ, VF2, TRUEFRENCH)
beside VO or MULTi. It reads explicit runs of three-letter codes in title
order (`ENG.ITA`, `JPN.ENG`); a code alone is no language. Where the
indexer reports a release's audio languages from its MediaInfo, they beat
all of these: nzbgeek sends one `language` attribute per track, and
nzb-life sends `audio`/`language` lists mixing codes and names.
`rss.ProjectRelease` stores them as `ReleaseInfo.IndexerLanguages`
(`catalogue.ResolveLanguageName`), the grab entry keeps them
(`DownloadRelease.IndexerLanguages`), and `decision.indexerLanguages` makes
them the release's languages at search and at the grab check. Example:
hallowed's untagged Akira Blu-ray is English and Japanese. One exception
(owner, 2026-10-21): a title that says dual audio keeps the title parser's
reading when the indexer names fewer than two languages. nzb-life names only
Japanese for many dual-audio TV posts (Kitsune's DUAL Monster, 86 and BLUE
LOCK), and such a release is Japanese and English for an episode, a season
pack and a film alike. NFOs are not
fetched, since nzbgeek ignores `t=getnfo`. Items report `status.audio` (wanted, present, missing,
graft); a missing dub never blocks the video. Grafting (phase 4): the
search planner queues a donor search (`SearchTask.Purpose: audioDonor`) for
a monitored item whose graft reads `searching` or `failed`, on its own
backoff (`status.donorSearchAttempts`), at most 20 first-time live ones per
namespace per sweep (`searchplan.DonorBudget`); a donor is judged on its
languages, never its quality, and is grabbed as a grab entry with
`purpose: audioDonor` (a donor and a video grab of one item coexist, and a
donor moves neither `activeDownloadRef` nor the phase). The import agent's
execute task places it under `<RootFolder>/.clustarr/donors/<item-uid>/` (no
walk enters `.clustarr`) and reduces it to its `.mka` at once
(`pkg/transcode/donoraudio`), and the item records it (`status.audio.donor`,
derived by the item key from the entry's execute record). The remediation
loop's graft planner (`app/transcode/graftplan`) runs one graft Job per file
-- `transcode --graft-task` on the cpu pool's pod template, owned by its
MediaFile, at most `--graft-concurrency` (2) ledger slots, never beside an
open transcode of the file, answering in `clustarr-grafts` -- which aligns
the anchors (`pkg/audioalign`), muxes the dub in beside every copied stream
(`engine.Options.Graft`), verifies the muxed track and swaps it in, tagged
`CLUSTARR_GRAFT`. catalog re-probes it without making a transcode:
`spec.original` stays, `status.graftTag` is recorded, and the size, mtime and
path are catalog's from then on. A failed graft rejects that donor for the
item (`status.audio.rejectedReleases` on the item). A graft rides along with
its file's transcode when one is due: the transcode dispatch carries the
file's graft (`task.Task.Graft`, recorded as `status.transcode.joinedGraft`),
the pool worker aligns before it encodes and muxes the dub in the same pass
(`worker.GraftEngine`), and a graft waits up to 6 h for such a transcode
(`graftplan.JoinWait`) before it grafts alone; only a standalone graft
(`status.graft` Running) holds a transcode back. A multi-episode file is
never grafted (`MultiItemFile` on the item).
The movies provider has Plex's collection feature (2026-10-06; type 18,
Feature `collection` at `/library/collections`, movie libraries only): a
collection is a TMDB collection (`status.metadata.collection`), its items
the library's movies in it, answered at `/library/collections/<key>` and its
`/children` (`ui/plex/collection.go`), by Plex id or `tmdb-collection-<id>`,
and matched as type 18 by GUID or name. The gateway learns the collection's
Plex id from the movie's own Plex metadata (Plex cannot look a collection up
by TMDB id) into `collection.plexID`, and its summary, poster and background
from TMDB into the movie's extended document. Both providers declare `match`,
which Plex's provider docs require.

**Plex collections are resources** (owner, 2026-11-10: "Custom Resource the
clustarr UI can edit", then "also include normal collections, the ability to
customize the image, the ability to change its settings";
`docs/superpowers/specs/2026-11-10-plex-collections-design.md`). A
`PlexCollection` (catalog.clustarr.io) is smart -- `spec.filter`, Plex's own
filter keys (`show.genre`, `episode.audioLanguage`) with word operators and
tag values by name, one level of groups, which cluster-plex compiles to Plex
Web's encoding -- or normal -- `spec.members.items`, clustarr Movies or Series
by name, found in Plex by the GUID cluster-plex's projection gives them -- or
a TMDB collection's (owner, 2026-11-17; the design's amendment) --
`spec.tmdbCollection.id`, a normal collection in Plex whose members are the
library's Movies TMDB lists in it, which cluster-plex asks clustarr's own Plex
provider for (`/plex/movies/library/collections/tmdb-collection-<id>` and its
children) and whose title, summary, poster and background left out are TMDB's
-- exactly one of the three, and the choice, `library` and `itemType` never
change. **A smart collection never hides its items** (2026-11-17, the same on
a stock server): Plex hides an item by a flag on its row
(`metadata_items.absolute_index`) recomputed from the members it stores
(`taggings`), and a smart collection stores none, so a collection whose items
should hide behind it ("Hide items in this collection", or the library's
setting) is a normal or a TMDB one. Its
presentation is optional: `summary`, `sortTitle`, `poster`/`background` URLs
(https on a public host, or under the UI's own `/art/`), `settings.mode` and
`settings.order`, and `visibility` (library Recommended, Home, friends' Home).
Every field left out keeps what Plex has, so adopting an existing collection
by title changes only what the spec states; a collection Plex made from
metadata (GUID not `collection://`) is never adopted. cluster-plex
reconciles them on its lease holder and writes their status and finalizer
(below); the UI's Collections page (Library sub-menu) edits their spec.

**A library's hubs are a resource too** (owner, 2026-11-16: "Add a mechanism
for managing \"Hubs\" within the libraries - including collections, sorting,
and everything available from the provided Plex Web UI image", Plex Web's
"Manage Recommendations"; `docs/superpowers/specs/2026-11-16-plex-hub-layouts-design.md`).
A `PlexHubLayout` (catalog.clustarr.io, `phub`, one per Plex library,
`spec.library` its title, immutable) lists the library's hubs in the order
wanted: each `spec.hubs` entry (100 at most) is exactly one of `identifier`
(a built-in hub: `movie.recentlyreleased`), `collection` (a PlexCollection
of the namespace) and `collectionTitle` (a Plex collection no resource
manages), with `libraryRecommended`, `home` and `friendsHome` always
stated. The listed hubs come first in that order; a hub not listed keeps
Plex's relative order after them, and its visibility; a listed collection
without a hub gets one, and takes its visibility from the layout, not from
PlexCollection `spec.visibility`. The annotation
`catalog.clustarr.io/hubs-reset` (an RFC 3339 time, a nonce;
`catalogv1alpha1.AnnotationPlexHubsReset`) asks for Plex's Reset
Customization once, recorded in `status.resetHandled`. cluster-plex applies
it and is the only writer of its status (`status.hubs`: Plex's whole list in
Plex's order, 200 at most; condition `Ready`); it has no finalizer, and
clustarr's manager never touches it. The UI's **Hubs** page (`/library/hubs`,
Library sub-menu; `ui/hubs.go`, `ui/views/hubs.templ`) is a section a
library, each row a hub with three switches, up and down and, for a listed
entry, a remove, with "Add collection" and "Reset customization" (an alert
dialog) above; the Collections list carries the same three switches per
collection, which write the library's layout. Every write is
`ui/actions/plexhublayout.go`: `HubRows` is the order shown (the spec's
entries, then the hubs Plex reports that it does not list), and `EditHubs`
first materialises a hub the spec does not list, with every row ahead of
it and Plex's flags, so an edit never reorders the rest or drops their
visibility; `EditPlexHubLayout` reads the layout through the apiserver and
patches `spec.hubs` under the generation it read, creating the layout
(`PlexHubLayoutName`) at a library's first write, and the reset sets the
annotation and clears `spec.hubs` in one merge patch. An edit's answer
draws the rows from the layout the write returned, never from the cache.
The chart's Plex libraries are `Movies` and `TV Shows` since then, with
`formerNames` the old `Clustarr Movies` and `Clustarr TV`, which needs a
cluster-plex chart that reads the key. Built 2026-11-16, not deployed, not
seen in a browser, and not yet run against cluster-plex's half.


**The 2026-11-17 polish** (owner: "polish Clustarr's UI as much as possible
... a fully polished, production ready, UI design system"; fifteen review
areas at phone, Galaxy Note, tablet and desktop widths, then the fixes;
commits `ui: ...` of 2026-11-17 on main). What it left as rules:

- **Tokens and glass.** One `:root, .dark` token block in
  `ui/static/input.css`; `<html class="dark">`, so `dark:` utilities apply.
  Three glass levels: resting (cards, panels), bars (`.glass-bar`, and
  `.glass-pill` for a button group), floating (dialogs, menus, toasts). An
  unlayered `box-shadow` replaces a component's `ring-*`/`shadow-*`, so each
  rule states its whole stack. Every dialog is bounded by the theme's guard
  (`max-height: calc(100dvh - 2rem)`, scrolls); never add a per-dialog one.
- **Widths follow the container, not the viewport.** Page content keys on
  `@container/hero`, `/table`, `/bar`, `/modal`: the sidebar (a sheet below
  768px, an icon rail to 1023px, expanded from 1024px, remembered in the
  `sidebar_state` and `nav_closed` cookies) changes the room a page has.
- **One feedback path.** Every write answers through `finish` in
  `ui/respond.go`: an htmx request is always 200, a refusal a toast in
  `#action-status`, a success a toast or a redraw (`HX-Trigger:
  page-changed`); a plain post that fails gets a styled page in the shell
  (`views.ErrorPage`), never Go's bare text. Forms post with `hx-post` into
  `#action-status`. Responses are gzipped (`ui/compress.go`).
- **A question goes through the layout's confirm**: `confirmAttrs(title,
  text, action)` on the asking element (`hx-confirm` plus
  `data-confirm-title`/`-action`/`-variant`). Never a dialog inside a polled
  or streamed region: the next frame tears it down.
- **Polled and streamed regions morph** (`hx-swap-oob="morph"`,
  `morph:outerHTML`, `morph:innerHTML`), so focus, hover and motion survive,
  and poll with `pollTrigger` (paused while the tab is hidden). **A filter
  form never redraws itself**: it targets its results (`#guide-body`,
  `#playlist-results`, `#channels-table`); a swap that replaces the search
  box throws away what was typed while the request was out.
- **Inner tables select** (seasons' episodes, an artist's albums and
  tracks, an author's books, a comic's issues): `selectHead`/`selectCell`
  marks, `ui/static/rowselect.js`, the gold-rimmed `selectionBar` across the
  top, `POST .../rows/bulk` (`ui/rows_bulk.go`). Live TV's channels use the
  same head mark over their own bulk form (`ui/static/livetv.js`).
- **Settings add and edit in a dialog**: an opener carries
  `views.ModalOpen`; `#modal` in the shell, `ui/static/modal.js` (the hash
  `#modal=<url>`, a guarded close), `ui/modal.go`; a plain GET of a form URL
  redirects to its page with the hash.
- **An item's page is the Plex apps'** (owner's iPad and Plex Web
  screenshots): the fanart sharp across the top right fading into the
  blurred page, the title logo (a tablet, and over the banner on a phone)
  or the title in type beside the poster (from 1024px of hero), the meta
  line, a marks row (the monitor bookmark and the provider logos, all 20px
  tall), the actions as icon-only frosted circles with their word as a
  tooltip, the overview, then the facts as a two-column list.
- **The library** is Plex's grid: a card is the art alone with its status
  stripe, its title and a line under it on the page; the toolbar is two
  shadcn-templ button groups, the docs' pill (`[--radius:9999rem]`), ghost
  buttons with a `buttongroup.Separator` between each pair -- actions left,
  view menus right; under it the filter's Edit/Save/Clear group, the count
  as a bare number, then the chips; selecting swaps the toolbar for the
  gold-rimmed selection bar of the same height; the A-Z strip is 44px, the
  thumb at its left and the letters in its right 24px.

Still open from that review (majors, none blocking): the Settings list as
click-to-open rows with a switch, read-only built-in profiles with Copy,
and Save disabled on an untouched form; the collection editor, Add New and
manual assign as dialogs; the guide's own scroller and "jump to now"; the
dashboard's charts replaying their entrance at every poll and its empty
states (Activity's rows became one component with toned badges on
2026-11-27); the Files
tables' narrow layouts; the nine hero copies folded into one component.

**The ui counts its requests, and its autoscaler waits five minutes**
(owner, 2026-11-26: "Add request metrics", "Keep the autoscaler but make it
harder to trigger", after asking why the ui pods "scale up (HPA) but not
down"). What was found on talos that night: the HPA went 1 -> 2 -> 3 in five
minutes on a minute over its target and held three for the 30-minute
scale-down window it has had since 2026-11-17, twice; one pod held a steady
210-265m while the other two idled at 2-6m, because the ui's load arrives on
long-lived connections (Plex's to the provider, a browser's streams) that
stay on the pod they opened on. So a new pod takes none of the load that
started it, one busy pod at X asks for ceil(X / target) pods however many
there are, and every new pod costs about 900 MiB, a whole-library LIST
through a kube-apiserver that sat at its 4 Gi limit, and a read of the
artwork store. What the busy pod was serving could not be read: the ui
logged no request and served no metrics.

- **Request series** (`ui/httpmetrics.go`): `clustarr_ui_http_requests_total`
  (`route`, `method`, `code`), `_request_duration_seconds` (`route`,
  `method`), `_response_bytes_total` (`route`) and `_requests_in_flight`
  (`route`), on the ui's default registry with `go_*` and `process_*`,
  served on `--metrics-bind-address` (`:8081`, container port `metrics` in
  both installers, a ServiceMonitor each) and never on the pages' address.
  `route` is the mux pattern the request matched, read from `r.Pattern`
  after the handler returns: the wrapper sits directly around
  `compress(mux)`, and nothing between it and the mux may copy the request
  (`r.WithContext`), or every request reads `unmatched`
  (`TestARequestIsCountedByTheRouteItMatched` fails that way). The Plex
  provider's mux is mounted on the request path unchanged and names its own
  routes; the in-flight gauge, set before the handler runs, reads `/plex/`
  for all of them. The counting writer passes `Flush` through, which the
  streams assert (`TestAnOpenStreamIsInFlightAndStillFlushes`).
  `TestUINeverWrites` refuses any `.Serve(` call in `ui/`, so the listener
  is `ListenAndServe`.
- **The autoscaler on talos**: target 600 (% of the 100m request), scale-up
  after five straight minutes above it, one pod per five minutes, scale-down
  window 1800 s as before; set on the live HPA by kubectl and in
  `talos-values.yaml` (`ui.autoscaling`). The chart's values comment carries
  the reasoning. Raise or lower the target from the pods' own series, not
  from a guess.
