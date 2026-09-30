# Library delete -- design

Status: approved in conversation 2026-09-30, pending review of this document.

## Goal

A **Delete** button on every library item -- Movie, Series, Artist, Author,
Audiobook, Comic -- that removes the item and every MediaFile record of it
and of its children. Its dialog has two checkboxes, both off by default:

- **Delete files and folders** (Sonarr's "Delete Series Folder"): also
  remove **everything of it on disk** -- its folder, recursively (files
  Clustarr never recorded included), and every directory the removal leaves
  empty, up to but never including the RootFolder. Removal is **permanent**
  (the owner's choice: no recycle bin, even when the RootFolder configures
  one). Off by default because it cannot be undone.
- **Add import list exclusion** (Sonarr/Radarr's): keep import lists -- and,
  new with this design, the library scan -- from adding it back.

## What exists (2026-09-30)

- Deleting an item removes only records. Children (Episode, Album, Book an
  Author created, Issue) and Downloads are garbage-collected through
  ownerReferences; MediaFiles have no owner and are orphaned; nothing on disk
  is touched.
- `app/import/worker/importlist/delete.go` (import lists' `removeAndDelete`)
  recycles the MediaFiles an item has, deletes their records and prunes
  emptied directories -- files only, never an untracked file, Movie and
  Series only.
- The ui has no `/data` mount, may not delete catalog kinds (role and
  `TestUINeverWrites`) and may not import `pkg/fsops`. `/data` is mounted
  read-write in importarr, which owns MediaFile spec and the RootFolders'
  recycle bins.

## Design

### The request: two annotations

The ui never deletes. The button's one write is a merge patch of the item's
metadata (the ui already holds `patch` on every catalog kind):

| Annotation | Value | Meaning |
| --- | --- | --- |
| `catalog.clustarr.io/delete` | `files` or `records` | Delete the item and its records; with `files`, everything of it on disk too. Any other value is refused. |
| `catalog.clustarr.io/delete-add-exclusion` | `true` | Also create an ImportExclusion for it first. |

So `kubectl annotate movie heat catalog.clustarr.io/delete=files` does what
the button does with "Delete files and folders" ticked, and `=records`
what it does without.

### The executor: importarr's library-delete controller

A new controller, `app/import/controller/librarydelete`, watches the six
kinds with a predicate on the `catalog.clustarr.io/delete` annotation, and
for an annotated item, in order:

1. **Resolve the targets.**
   - The RootFolder (`spec.rootFolderRef`; a standalone Book's own ref) and
     its `spec.path`.
   - The item folder: `status.path`.
   - Every MediaFile of the item and its children, by `spec.mediaRef`: the
     item itself, a Series' Episodes (and pack keys), an Artist's Albums, an
     Author's Books, a Comic's Issues -- listed through the existing
     `.spec.mediaRef.target` index, never a namespace scan per child.
   - Each MediaFile's path and `status.sidecars`.
2. **Refuse what is unsafe** (`files` only; `records` touches no disk and
   skips to step 3) -- nothing is removed if any check fails:
   - every target must be strictly under the RootFolder's path (`strictlyUnder`,
     shared with importlist), never the RootFolder itself;
   - the item folder must not hold a MediaFile of any other item (one List
     of the namespace's MediaFiles, filtered by path prefix -- a one-off per
     delete, not a watch);
   - no symlink is followed (`fsops.SafeRemove`'s guard).
3. **Add the exclusion** when `delete-add-exclusion` is `true` and the kind has
   one (Movie by tmdb, Series by tvdb, Audiobook by asin, Comic by its
   ComicVine id; Artist and Author have no ExclusionKind, so the ui offers no
   checkbox for them). Created by name from the item, so a retry finds it.
4. **Remove from disk** (`files` only), permanently: `fsops.SafeRemove` the item folder
   (recursive), then each MediaFile path and sidecar outside that folder;
   a path already gone is success (a retry after a partial run).
5. **Delete the records**: every MediaFile listed in 1 (their SubtitleRequests
   and TranscodeJobs follow by ownerReference).
6. **Prune** (`files` only) each removed path's parent directories while
   empty, up to and excluding the RootFolder (`pruneEmptyDirs`, shared).
7. **Delete the item.** Its children and Downloads follow by ownerReference;
   catalogarr's finalizer publishes the `deleted` ItemEvent as today.

A refusal or an error writes `catalog.clustarr.io/delete-error: <reason>` on
the item and a Warning Event, and keeps `delete`. An I/O error is retried
with backoff; a refusal is not retried until the request changes (the ui's
"Retry" re-patches `delete`, which clears `delete-error`). Steps 3-6 are
idempotent, so a crash anywhere re-runs cleanly.

importarr's role gains `delete` on the six kinds (it already deletes Movies
and Series for import lists), `create` on importexclusions, and `delete`
on mediafiles (already held for upgrades).

### The library scan honours exclusions

With `records`, the files stay, and the next scan would re-create a Movie
or Series from the id in its folder name (the scan checks no exclusion
today). The rescan worker's auto-add (`createSeries`, and the movie
equivalent) now looks the candidate's id up in the
`clustarr-import-exclusions` bucket, as the import-list sync does, and
records an excluded folder in `LibraryScan.status.unmatched` with reason
`excluded` instead of creating the item. So "keep files" plus the exclusion
checkbox removes the item for good; "keep files" alone behaves as Sonarr's
does, and a scan may add it back.

### The ui

- A **Delete** button in each item page's header (movie, series, artist,
  author, audiobook and comic pages), destructive style.
- It opens the existing `dialog` component: the item's title, the folder
  that will be removed, its file count and size where the page lists its
  files (a movie's),
  a **Delete files and folders** checkbox (off) that, when ticked, shows
  "This permanently deletes the folder and cannot be undone.", the
  exclusion checkbox (kinds with an ExclusionKind only, off), and
  Cancel / Delete.
- Delete posts `POST /library/{namespace}/{kind}/{name}/delete`, which calls
  `ui/actions.RequestDelete` -- a merge patch of the two annotations (and a
  null `delete-error`) under `clustarr-ui` -- and redirects to the library
  tab.
- While the annotation stands, the item's card and page read **Deleting**;
  with `delete-error` set they read **Delete failed: <reason>** with a Retry.
- No new ui grant: `patch` on the catalog kinds is already held.

## Out of scope

Deleting a single Episode, Album, Book or Issue (or only its file); bulk
delete from the library toolbar (amendment §A3); the recycle bin.

## Testing

- Unit, against a temp directory and the fake client: a Movie folder with
  a tracked file, an untracked `.nfo` and a subtitle is removed entirely and
  its empty parent pruned, the RootFolder kept; a Series removes its Episodes'
  MediaFiles; a folder holding another item's MediaFile is refused and
  nothing is removed; a path outside the RootFolder is refused; a symlinked
  folder is not followed; a retry after a partial run completes; the
  exclusion is created once; the RootFolder itself is never removed; a
  `records` delete removes the item and its MediaFiles and leaves every
  file and folder in place; the rescan records an excluded folder as
  unmatched `excluded` and creates nothing.
- ui: the action's patch body for each checkbox combination, the button
  and dialog on each page, the Deleting and Delete failed states, and the
  exclusion checkbox only where the kind has one.
- The existing guards: `TestUINeverWrites` (patch only), the ui role test,
  and the RBAC markers regenerating importarr's role.
