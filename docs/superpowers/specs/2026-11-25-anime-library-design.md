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

## 3. Relocation: a series follows its root folder

`spec.rootFolderRef` is desired state. When a Series' files are in a folder
under another root, the manager moves the folder. This is what a "Move to
Anime" in the ui, a classification and the bulk migration all are: a spec
patch, and the same convergence.

A series with no folder on disk has nothing to move: `status.path`
re-renders under the new root and that is all.

Otherwise, for one series:

1. **Wait until it is quiet.** While any of its files has an import, a
   transcode, a graft, a subtitle or a marker task in flight, the
   relocation waits. While it is pending, the loop admits no new file work
   for the series, and a scan of either root leaves its folders alone.
2. **Move the folder.** The manager publishes one task to the `import`
   agent, which renames the series folder to its folder under the new root
   (the destination root's naming renders the name) through `pkg/fsops`,
   and answers in a record. One rename carries everything in it: episodes,
   sidecars, NFO, extras, unmatched files. Refused, never worked around:
   a destination that exists (`Collision`: nothing is merged or
   overwritten) and a destination on another filesystem (`CrossDevice`:
   nothing is copied). Source gone and destination present reads as done.
3. **Re-point the files.** The manager rewrites the folder prefix of every
   MediaFile `spec.path` of the series, under import's field manager, each
   a complete apply. Size and mtime are unchanged, so it is a path-only
   change: no probe, no transcode verdict lost.
4. **Finish.** `status.path` becomes the new folder, the condition clears,
   a Normal `Relocated` event is recorded, and a scan of the new folder
   re-derives whatever status names a path.

The Series carries condition `Relocating` while 1 to 3 run, its reason the
step or the refusal (`Waiting`, `Moving`, `Collision`, `CrossDevice`), and
`status.path` keeps naming where the files are until step 4.

The rename is the only change on disk and is atomic. Everything after it is
a prefix rewrite keyed on the agent's record, so a manager that dies at any
point resumes without a second move.

Held to the invariants: the agent writes no Kubernetes object and answers
in a record; the manager ensures the new consumer; the manager makes every
apply. One relocation runs at a time: the rename is instant, but each one
has every file of the series leave one Plex library and enter another.

Series only. The mechanism does not care about the kind, but nothing asks
for it elsewhere yet.

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
7. One relocation at a time.
8. Activity, calendar, wanted and dashboard are not split yet.
9. A list places an item once, at creation, and never moves it after; its
   root folder default is not re-asserted on what exists.

## 8. Not verified

Before section 3 is built:

- every record that holds a path besides `MediaFile.spec.path` (the
  trailers bucket, NFO state, the recycle folder, the ASS backups,
  download entries, graft donors) and whether each follows a prefix
  rewrite or a scan;
- how a root's scan treats a folder that is mid-move, and how the loop
  tells that a series has file work in flight;
- whether the 2026-11-04 chain, in which import's facts were re-applied
  over a renamed file's `spec.path`, is fixed; a relocation would trip it.

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

