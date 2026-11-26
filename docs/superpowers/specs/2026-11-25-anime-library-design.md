# The Anime library

Design of record, 2026-11-25. **Proposed, not built.** It spans clustarr
(API, manager, import agent, ui), cluster-plex (one more library) and a
talos runbook. Sections 7 and 8 list what is the author's ruling rather than
the owner's, and what has not been verified; both are to be settled before
the part that depends on them is built.

## The request

Owner, 2026-11-25, in three steps:

> Postulate whether it would be better to separate anime series into
> collections with the items hidden in library or within its own separate
> library to avoid unnecessary confusion for parents when browsing the plex
> server

> The separate library method looks promising - and like the desired
> behavior. Currently, however, we have import lists pointing at the tv
> shows root folder which will need to move to the anime root when we move.
> We should consider anime to be tv shows only. Anime movies can be combined
> with the movies root

> Should this *just* be a root folder change or should it also reflect the
> design in the UI such that tv series are *non* anime while anime series
> are *anime only*?

The answer given to the third, which this design carries out: it is
reflected in the ui. Anime is a library of its own in Plex and in clustarr's
ui, defined the same way in both.

## 0. What is there today

Measured on talos, 2026-11-25.

- One series RootFolder, `tv` (`/data/media/tv`), holding 752 Series: 509 of
  type `anime`, 243 `standard`. One Plex show library, "TV Shows", on
  `/media/tv`. 34 of 1,292 Movies are classified anime, under `movies`.
- Six series ImportLists, every one with `defaults.rootFolderRef: tv`; two
  (`mal-anime-series`, `the-700-club-deep-cut-anime`) default to type
  `anime`, the other four to `standard`.
- Anime also arrives outside those two lists. A series added from a
  watchlist or by hand is created `standard`; when its metadata first
  arrives the Series reconciler classifies it once (`series.Classify`, TVDB
  genre "Anime") and applies `RootFolder.spec.defaults.anime`: a quality
  profile and a series type. Not a root folder.
- A list re-applies its complete declaration, under force, to every item
  it added, on every sync (`importlist.applySeries`). For a classified
  Series it keeps the profile and the type it finds; the root folder it
  sends is always its own `defaults.rootFolderRef`.
- Changing a Series' `spec.rootFolderRef` moves nothing. A video file's
  rename is the whole-path rename, which holds a move to another folder
  (`rename.Actuator.renameMode`); nothing relocates a series.
- The ui's library has six tabs, one per parent kind; "every RootFolderKind
  belongs to exactly one tab" (`ui/projection/library.go`).
- cluster-plex declares libraries in its config, each with `locations`, and
  puts a file in the library whose location is the longest prefix of its
  path (`clustarrwatch.Watcher.sectionFor`).
- `/data` is one NFS export (`10.1.50.1:/volume2/data`), so a folder moves
  between `/data/media/tv` and `/data/media/anime` by rename.

Why not a hidden collection: only a plain collection hides its members, a
smart one never does; cluster-plex's plain-collection sync stops near 500
members and there are 509; and it would put two thirds of the library
behind one tile while leaving it in the library's hubs.

## 1. The rule

**The root folder decides the library.**

- An **anime root** is a RootFolder of kind `series` whose
  `spec.defaults.seriesType` is `anime`. No new kind and no marker field:
  the existing default already says "what is added here is anime".
- A Series **is anime** when its `spec.rootFolderRef` names an anime root,
  and is a TV series otherwise.
- Under an anime root a Series' `spec.seriesType` is `anime`; under any
  other series root it is `standard` or `daily`.
- Movies take no part. A movie root's `defaults.anime` keeps moving a
  classified anime film to its profile and nothing else; anime films stay
  in Movies, in clustarr and in Plex.

Plex already defines a library by folder, so this one rule keeps the ui,
Plex and the disk agreeing on what "TV Shows" and "Anime" hold. Keying the
ui on the series type would let them part: an edited type would change the
tab while the files, and so Plex, stayed where they were.

It stays one resource. There is no Anime kind; an anime is a Series.

## 2. Routing a new anime

```yaml
apiVersion: catalog.clustarr.io/v1alpha1
kind: RootFolder
metadata: {name: tv}
spec:
  kind: series
  defaults:
    seriesType: standard
    anime:
      qualityProfileRef: hevc-small-1080p-anime-tv
      seriesType: anime
      rootFolderRef: anime        # new, optional
```

- `AnimeDefaults.rootFolderRef` is the RootFolder an anime **series** is
  moved to on classification. Refused by CEL on a root that is not of kind
  `series`, and when it names the root itself.
- The Series reconciler applies it with the profile and the type, in the
  one spec patch the classification already makes (`series.AnimeRoute`).
- **Under an anime root nothing is detected: a Series there is recorded
  anime, with no patch** (`series.Classify`). It is the rule of section 1,
  and it is what keeps the record derivable: the spec patch and the status
  record are two writes, and a pass that reads the series between them
  finds it under the anime root with nothing recorded. So a routed series'
  `status.classification` is `{anime: true}` and carries no profile, type
  or root folder; the event says what was applied.
- A target that does not exist, or is not an anime root, records no
  classification and holds the series: Ready=False
  `AnimeRootFolderNotFound`, no Episode fanned out, tried again when
  either root changes. Recorded without the move it would stay in TV for
  good, classification being once only; let through unclassified it would
  be searched under the TV root and the wrong profile.
- Classification runs once and has run for every series there is. It
  routes what is added from now on; the 509 move by section 3.
- The two anime ImportLists take `defaults.rootFolderRef: anime`. The other
  four stay on `tv` and rely on classification.
- **A list never moves an item.** `applySeries` and `applyMovie` send the
  root folder an existing item already has, as `applySeries` already keeps
  a classified Series' profile and type; `defaults.rootFolderRef` decides
  only where a list's new item is created. As built, each sync would force
  a routed or hand-moved series back to the list's root, and with section 3
  that would move its files back; and re-pointing a list would force its
  new root onto everything it ever added. With the change, re-pointing a
  list moves nothing.

A Plex watchlist under this, start to finish: the list creates the Series
under `tv` as `standard`, exactly as today. Its metadata arrives, and the
same reconcile that reads it classifies it, before the episode fan-out, so
no Episode exists and nothing has been searched or imported yet. An anime
takes the anime profile, the type and the anime root in that one patch, and
its first file is imported under `/data/media/anime`; anything else stays
in `tv`. The list's later syncs find the item, keep its root, and go on
owning its monitoring and tags. A watchlisted anime film goes to `movies`
and stays there.

## 3. Relocation: a series' files follow its root folder

`spec.rootFolderRef` is desired state. This is what a "Move to Anime" in
the ui and the bulk migration are: a spec patch, after which the files
converge. (As first written this section had the import agent rename the
whole folder under a new task, record and status block; what was built is
below, and section 10 says why.)

The Series reconciler renders `status.path` under the root folder the
Series now names, as it always has, so what is imported from then on lands
in the new folder. A series with no file has nothing more to do.

Each of its files then follows, one at a time, through the file loop's
rename actuator (`rename.Actuator.relocation`, `specwrite.RelocateFile`):

- **What moves.** An episode's file that is under a series root folder
  other than the one its Series is stored under, once the Series' folder
  under its own root is rendered. It goes to the place under the Series'
  new folder that it had under the old one, name included; what it is
  called stays the naming pass's, under the rules of the root it arrives
  in. No root folder's rename switch decides this move.
- **One guarded step**, a rename's: the file's size and mtime must still be
  what spec records; nothing may exist at the target, and something that
  does is never replaced, so two series' folders are never merged over each
  other (`Collision`, said once and tried again a minute later); the move
  and the complete spec apply go together, moved back when the apply
  fails, rolled forward when a move was made and never recorded. Nothing is
  copied: a move across filesystems fails.
- **With it** go the file's sidecars. The folders the move empties are
  pruned, the Series' old folder too, the root folder never. A file that is
  not media and that nothing records stays, and keeps the old folder.
- **It waits** for the file to be present and probed, for no transcode or
  graft of it to be in flight (their tasks name the path), and for no
  LibraryScan to be walking the folder it is in or the one it goes to,
  which could re-attribute the vacated path or find the file before the
  move is recorded.
- **Afterwards** the file's probe pass takes the change of path alone and
  carries its history to the new hash (section 8a): nothing is searched,
  fetched, analysed or encoded again.

A scan of the old root meanwhile finds the files still there recorded and
their Series under another root, and leaves them (`other_root_folder`).
There is no series-level phase: a series is moved when none of its files
is under another root, which the Events on its MediaFiles and its old
folder's disappearance show.

Episodes only. The mechanism does not care about the kind, but nothing
asks for it elsewhere yet.

## 4. Plex

One more entry in cluster-plex's config:

```yaml
libraries:
  - name: Anime
    type: show
    provider: tv.plex.agents.custom.clustarr.tv   # the TV library's
    language: en-US
    locations: [/media/anime]
```

and a `PlexHubLayout` for it. The folder must exist first: cluster-plex
leaves out a section with no location.

No change to cluster-plex's model is expected: a relocated file's path now
falls under the Anime library's location, so its rows are written there and
taken off TV Shows. Watch state is keyed by guid and survived the
libraries' re-creation on 2026-11-17. No PlexCollection is affected: all
five are in Movies. The dub mark is unaffected.

What the split buys in Plex is the owner's to use: the Anime library can be
left unshared with an account, or unpinned by it.

## 5. The ui

- **A seventh tab, Anime, after TV.** A Series' card goes on Anime when its
  root is an anime root. "Every RootFolderKind belongs to one tab" becomes
  "every RootFolder belongs to one tab": series roots split by the rule in
  section 1. Anime takes everything the TV tab has, entry for entry: sorts,
  quick filters, filter fields, find, bulk actions, rescan.
- **Add New.** Anime's add page is TV's (series, TVDB) offering the anime
  roots; TV's offers the others. An anime added from TV is routed on
  classification, and the result says where it went.
- **Series type.** Under an anime root there is no type control. Under any
  other root it offers `standard` and `daily`.
- **Move to Anime / Move to TV**, on the item and as a bulk action: one
  spec patch of `rootFolderRef`, `seriesType` and `qualityProfileRef` (the
  destination root's default profile), with the destination chosen when
  there is more than one. The card shows the relocation's state and, when
  refused, why. This is how a misclassified series is put right.
- **Root folder settings** gain "Anime root folder" on a series root.
- **Collections and Hubs** gain the third library.
- Activity, the calendar, wanted and the dashboard stay combined in the
  first cut.

The ui writes spec only, as ever; every write above is in `ui/actions`.

## 6. Migration on talos

A runbook, not migration code.

1. CRDs by hand, then images.
2. `/data/media/anime`; RootFolder `anime` (kind series, type `anime`, the
   anime profile, `tv`'s naming and schedule); `tv` gains
   `defaults.anime.rootFolderRef: anime`; the two lists re-pointed. From
   here every new anime lands in the anime root. **The lists are re-pointed
   only on an image with "a list never moves an item"** (section 2): on
   today's, a re-pointed list forces `rootFolderRef: anime` onto every
   series it added at its next sync, and nothing moves the files.
3. cluster-plex's config gains the Anime library and its hub layout.
4. **Pilot: three series**, moved from the ui: one transcoded, one still
   airing, one with sidecar subtitles and a trailer. Checked for each:
   files and sidecars in place, MediaFiles Ready with no probe or encode
   run again, rows in Anime and gone from TV Shows, watch state kept, the
   poster and its dub mark.
5. The rest, about 506: TV tab filtered to type `anime`, bulk Move to
   Anime, paced by the one-at-a-time relocation.
6. Done when no Series of type `anime` is under `tv` and the TV tab shows
   243.

Between 2 and 5 the existing anime sit under `tv` with type `anime`, the
one state the rule forbids. They show on TV, where the bulk action finds
them.

## 7. Rulings where the request was silent

The author's, for the owner to overturn.

1. The root folder decides, not the series type (section 1).
2. An anime root is told by `defaults.seriesType: anime`; no new
   RootFolderKind, which would touch every switch on the kind.
3. Relocation is declarative on `spec.rootFolderRef`, not an imperative
   operation; the ui's Move is a spec patch.
4. A relocation never merges into an existing folder and never copies
   across filesystems.
5. A Series whose type and root disagree is reported (a Warning event, a
   hint on its page), not corrected: which of the two was meant is not the
   controller's to guess.
6. A classification that cannot route waits rather than recording.
7. Files are relocated one at a time, each in its own pass of the file
   loop; nothing moves a folder.
8. Activity, calendar, wanted and dashboard are not split yet.
9. A list places an item once, at creation, and never moves it after; its
   root folder default is not re-asserted on what exists.

## 8. Not verified

Before section 3 is built: the three checks first listed here were made on
2026-11-25, and section 8a is what they found. One of them is a decision
for the owner.

## 8a. What the checks found (2026-11-25)

**A file's probe hash names its path** (`sha1(path|size|mtime)`,
`pkg/mediainfo/hash.go`), so a relocation gives every file of the series a
new hash. The MediaFile controller takes the new hash without a probe
(`pathOnly`), but two stages read a changed hash as new bytes:

- subtitles reset every language's attempts and next search and withdraw
  what is in flight (`subtitleplan`, "New bytes"), so the file is searched
  for again;
- markers are due again (`markers.Due`, `segmentplan.due`): TheIntroDB is
  fetched and the segments analysed again, and the old fingerprints stay in
  the object store until their 90 days are up.

Both happen today at every single rename. The bulk move is 7,784 files in
368 series (141 of the 509 anime have no file and only change root).
**Settled, owner, 2026-11-26: "Carry the history across a path-only move."**
The stages keep their history under the new hash when the bytes did not
change, which also ends the same waste at every ordinary rename. Built
first, as section 10 records; section 3 follows it.

What section 3, as first written, would also have had to do -- hold
`status.path`, guard the scans against a folder moved before its paths were
rewritten, and read "quiet" off every file of the series -- is not needed
by what was built: a file's move and its path are one step, guarded per
file by the holds a rename already has.

Settled, and no obstacle:

- The 2026-11-04 chain is bounded. An import's destination is applied to a
  MediaFile's path only while its download entry is importable, which is
  the window the relocation already waits out.
- No other record holds a path that goes stale harmfully. A file's
  `status.naming.expectedPath` re-renders from `Series.status.path`; donor
  files and trailers live under `<root>/.clustarr`, outside the series
  folder, and stay where they are and valid; sidecar names are bare;
  artwork, metadata and leases are keyed by UID. In-flight task payloads
  carry paths, which is why the relocation waits for none.
- A MediaFile's name hashes the path it was created at and never changes;
  scans find it by `spec.path`, as they do after a rename.

Before section 4 is deployed, on the cluster-plex rig:

- a file whose path leaves one library for another: old rows removed
  rather than left as trash, new ones made, watch state following;
- two show libraries on one agent, and provisioning's rename-in-place
  with a second library of the same type present.

Before the bulk move: that a rename between the two folders on the NAS is
a rename (one export, expected) and not a copy.

## 9. Build order

Each step is deployable on its own.

1. clustarr: `AnimeDefaults.rootFolderRef`, the routing and "a list never
   moves an item" (section 2). With the `anime` root and the lists
   re-pointed, new anime stops landing in `tv`.
2. clustarr: relocation (section 3), after the first three checks of
   section 8.
3. clustarr ui: the Anime tab, the type control and Move (section 5).
4. cluster-plex: the rig checks, then the library (section 4).
5. talos: the runbook (section 6).

## 10. As built

**Step 1, 2026-11-25** (clustarr; not deployed). `AnimeDefaults.rootFolderRef`
with its two CEL rules, `RootFolderSpec.AnimeRoot`, `series.AnimeRoute`,
the hold, the RootFolder watch waking the series a root routes elsewhere,
and "a list never moves an item" in `importlist.applySeries` and
`applyMovie`. Differences from the text as first written, folded in above:
`status.classification` gained no field, and a Series under an anime root is
recorded anime without detection. The first cut recorded the route in
status and lost the whole record whenever a pass ran between the
classification's two writes; `TestAnAnimeIsRoutedToTheAnimeRoot` found it.
The root folder form's "Anime root folder" waits for step 3.

**Step 3, in part, 2026-11-25** (clustarr ui; not deployed). The Anime
tab: `projection.TabAnime` after TV, a Series carded on it by its root
folder (`AnimeRoots`, `RootFolderOnTab`; the projection lists and watches
RootFolders for it), TV's sorts, filters and find, Rescan over the anime
roots, and Add New on each tab offering its own folders -- TV the types
`standard` and `daily`, Anime none, the handler setting `anime` under an
anime root and refusing it under any other. Checked in a browser against
the preview harness seeded from talos. Not built, because they move files:
Move to Anime / Move to TV, and the series type control on the edit form;
nor the root folder form's "Anime root folder", Collections and Hubs.

**The carry, 2026-11-26** (clustarr; not deployed; section 8a's decision).
`MediaFile.status.formerProbeHashes` (at most four, oldest first): the
hashes the same bytes had at the paths the file was renamed or moved from.
The probe planner appends the hash it leaves when it takes a change of path
alone (`mediafile.PlanProbe`'s path-only row) and clears the list when the
bytes change. In the same pass, right after the probe planner, the file
loop re-points every block at rest to the new hash
(`MediaFileStatus.CarryBytes`: `transcode`, `graft`, `subtitles`, `markers`
and `markers.analysis`), in the stored view the planners copy from and in
the draft, so the later planners -- which plan in that pass and compare
their block with the draft's hash -- find it about the bytes they have, and
the one status write stores the new hash and the carried blocks together.
A block with work in flight (a transcode or graft Queued, Running or
Swapping, a subtitle fetch outstanding) is not carried: its task names the
old path and is its stage's to withdraw or see fail, as before. A stored
status that lags is carried at the next pass's start and written even when
no planner changes anything (the pass's compare-and-swap reads the stored
object, not the carried view). Outside the status: the markers planner
reads a segments record under a former hash as the file's, the analyze task
carries the former hashes, and the segment worker finds an analysis record
and cached fingerprints under them, storing the fingerprints again under
the new hash so nothing is decoded. What still happens at a move: the
subtitle stage lists the file's sidecars again, its last listing being kept
in memory by path. `TestAMovedFilesHistoryFollowsIt` and its two
neighbours, `TestAPathOnlyChangeKeepsTheHashTheBytesHad`,
`TestAMovedSeasonsFingerprintsAreFoundUnderTheHashTheyHad`; each fails
with its part of the carry removed.

**Relocation, 2026-11-26** (clustarr; not deployed; section 3 as it now
reads). Not the folder rename by the import agent that section 3 first
described: the file loop's rename actuator already moved a file and
recorded its path in one guarded step -- fingerprint check, no-replace
move, apply with roll back and roll forward, sidecars, pruning, a hold
while a transcode is live or a scan covers either folder -- so the
relocation is a second kind of move through it. `specwrite.RelocateFile`
shares `RenameFile`'s move-and-record (`moveRecorded`) with a target that
keeps the file's path below its item's folder; `rename.Actuator` decides
it before its rename gate, for an episode's file whose own path and
proposed name are not under one root folder, reading the Series only then.
That dropped the task, the record, the agent handler, the Series status
block and the scan guard, and with them the window in which a folder had
moved and its files' paths had not. What it costs: files move one pass
each rather than a folder at a time, and a file nothing records keeps the
old folder alive. `TestRelocateFileMovesAFileAfterItsItemToAnotherRootFolder`,
`TestAFileFollowsItsSeriesToAnotherRootFolder` and its neighbours in
`app/remediation/rename`.

**Move and the type control, 2026-11-26** (clustarr ui; not deployed; the
rest of section 5's step 3). Move is a bulk action on TV and Anime, in a
dialog of its own form as Delete's is, offering the other library's root
folders (`moveTargets`); for each selected series `Server.moveSeries` makes
one `EditItem` patch of `rootFolderRef`, the type the folder makes it
(anime under an anime root; from anime, the folder's default type; a daily
or standard series otherwise keeps its own) and the folder's default
profile, the series' own where the folder names none. `EditRequest` takes a
root folder for a Series as it did for a standalone Book. The Edit dialog
offers `standard` and `daily` on TV, no type on Anime, and an anime not
moved yet keeps its own type as a third choice; the handler drops a type
posted for a series on Anime and refuses `anime` for one that is not. Seen
in a browser against the preview harness, where a Move of two series took
TV from four cards to two and Anime from three to five. Not built: Move on
the item's own page (one series is moved by selecting it), the root folder
form's "Anime root folder", and Collections and Hubs for the third
library.

