# Plex hub layouts

Design of record, 2026-11-16. The clustarr half; cluster-plex's controller is
built from the same contract in its own repository.

## The request

Owner, 2026-11-16:

> Rename the Plex libraries from "Clustarr Movies" and "Clustarr TV" to
> "Movies" and "TV Shows". Add a mechanism for managing "Hubs" within the
> libraries - including collections, sorting, and everything available from
> the provided Plex Web UI image. We should also be able to control the
> "Library Recommended", "Home", and "Friends' Home" options directly from
> the collection UI.

The image was Plex Web's Settings > Manage > Libraries > "Manage
Recommendations": per library an ordered, drag-sortable list of hubs --
Plex's own ("Recently Released Movies", "Top Movies in (Genre)", "Seasonal
Movies") and collection hubs ("A24", "Pixar") -- each with three checkboxes
(Library Recommended, Home, Friends' Home), and "Reset Customization" per
library.

## 1. The contract: `PlexHubLayout`

`catalog.clustarr.io/v1alpha1`, namespaced, short name `phub`, one per Plex
library. As with a PlexCollection
(`2026-11-10-plex-collections-design.md`, option A): its spec is the UI's,
kubectl's or Terraform's; its status is cluster-plex's alone, under field
manager `cluster-plex`; clustarr's manager never reconciles or writes it.
It has **no finalizer**: deleting a layout leaves Plex's hubs as they are.

```yaml
apiVersion: catalog.clustarr.io/v1alpha1
kind: PlexHubLayout
metadata:
  name: movies
  namespace: clustarr-system
  annotations:
    catalog.clustarr.io/hubs-reset: "2026-11-16T21:00:00Z"   # optional, a nonce
spec:
  library: Movies                 # required, MinLength 1, immutable
  hubs:                           # optional, atomic list, MaxItems 100
    - identifier: movie.recentlyreleased   # exactly one of identifier,
      libraryRecommended: true             # collection, collectionTitle
      home: true                           # the three booleans are required
      friendsHome: false
    - collection: the-up-collection        # a PlexCollection of the namespace
      libraryRecommended: true
      home: false
      friendsHome: false
    - collectionTitle: A24                 # a Plex collection no resource manages
      libraryRecommended: true
      home: true
      friendsHome: false
status:                           # cluster-plex's
  observedGeneration: 3
  sectionKey: "1"
  resetHandled: "2026-11-16T21:00:00Z"
  hubs:                           # Plex's whole list, in Plex's order; MaxItems 200
    - identifier: movie.recentlyreleased   # required
      title: Recently Released Movies      # MaxLength 256
      kind: builtin                        # builtin | collection, required
      libraryRecommended: true             # the three booleans are always sent
      home: true
      friendsHome: false
    - identifier: custom.collection.1.77
      title: The Up Collection
      kind: collection
      collection: the-up-collection        # when a PlexCollection holds it
      ratingKey: "77"
      libraryRecommended: true
      home: false
      friendsHome: false
  conditions:                     # MaxItems 8; cluster-plex sets Ready
    - type: Ready
      status: "True"
      reason: Synced
```

Rules the CRD holds (CEL, checked offline against apiextensions-apiserver
v0.37.0's validator, which compiles them and enforces the cost budget; no
envtest ran):

- `spec.library` is immutable. Nothing validates the title: cluster-plex
  also takes a library's former title (below).
- A hub entry is exactly one of `identifier`, `collection`,
  `collectionTitle` (`identifier` and `collection` at most 253 characters,
  `collectionTitle` 256).
- An identifier, a collection and a collection title are each listed once.

Semantics, which are cluster-plex's to carry out:

- The listed hubs come first, in the spec's order. A hub of the library
  the spec does not list keeps Plex's relative order after them, and its
  visibility.
- A `collection` entry whose collection has no hub yet gets one created.
- A collection a layout lists takes its visibility from the layout, not
  from PlexCollection `spec.visibility`, which still holds for a collection
  no layout lists.
- **Reset.** The annotation `catalog.clustarr.io/hubs-reset`
  (`catalogv1alpha1.AnnotationPlexHubsReset`), an RFC 3339 time read as a
  nonce, asks for Plex Web's "Reset Customization" once: cluster-plex resets
  when the value differs from `status.resetHandled`, and records it there.
- An empty `spec.hubs` changes nothing in Plex; the layout then only has
  cluster-plex report the library's hubs.

## 2. The library names

The chart's cluster-plex libraries are `Movies` and `TV Shows`, each with
`formerNames` (`[Clustarr Movies]`, `[Clustarr TV]`), which cluster-plex
reads to rename the section it made under the old title and to resolve a
PlexCollection or PlexHubLayout that still names it. The metadata
providers' own titles (`ui/plex/provider.go`) are not the libraries' and
stay. **The pinned cluster-plex dependency must be one that reads
`formerNames`**: an older chart ignores the key and would make two new,
empty sections.

## 3. The ui

Every write is in `ui/actions/plexhublayout.go`, under field manager
`clustarr-ui`; the ui role gains create, patch and delete on
`plexhublayouts` and reads it with the rest of the group.

- `HubRows(layout)` is the order both the page and the writes use: the
  spec's entries, then the hubs `status.hubs` reports that the spec does
  not list, in Plex's order. A listed row shows the spec's flags, an
  unlisted one Plex's.
- `EditHubs(layout, edit)` is the `spec.hubs` an edit leaves: a flag set, a
  move to a position, a collection added, an entry removed. **A hub the
  spec does not list yet is materialised first**, with every row ahead of it
  (and up to the position a move reaches), each carrying Plex's flags, so
  the order shown and the other rows' visibility survive the write. The
  rows after it stay unlisted, which keeps them in Plex's order behind the
  listed ones.
- `EditPlexHubLayout` reads the layout through the apiserver (not the
  page's cache, so two quick edits build on one another) and sends one JSON
  patch: a test of `metadata.generation`, then `spec.hubs` whole. A layout
  that does not exist is created, named `PlexHubLayoutName(library)`
  ("tv-shows"; `hubs-<hash>` for a title with no Latin letter or digit).
- `ResetPlexHubLayout` is one merge patch: the annotation set to now and
  `spec.hubs` cleared, or the layout would put back what the reset undid.

**The Hubs page** (`/library/hubs`, Library sub-menu after Collections;
`ui/hubs.go`, `ui/views/hubs.templ`) has a section a library: the distinct
libraries of the layouts and of the PlexCollections. Each row has its kind
badge, title, three switches, up and down, and a remove for a listed entry;
the header has "Add collection" (the library's PlexCollections no row
shows) and "Reset customization" behind an alert dialog. A switch, a move
and a remove answer into `#action-status` with a toast and, out of band,
the library's rows drawn again from the layout the write returned. Adding,
resetting and "Manage hubs" (which creates the empty layout of a library
none holds, so cluster-plex reports its hubs) are form posts that redirect
to the page. A section waiting on cluster-plex's first report, or on a
reset, asks for itself every 5 s. A Ready condition that is not True shows
its message.

**The Collections list** carries three switches per collection
(`views.CollectionHubFlags`), showing the collection's entry in its
library's layout, else what Plex reports for its hub, else its own
`spec.visibility`. A switch writes the library's layout (creating it, and
the entry, as above). The editor keeps `spec.visibility` and says the hub
switches take precedence once a layout lists the collection.

## 4. Rulings where the request was silent

1. A status hub is listed under the name the spec would use for it: a
   built-in hub by `identifier`, a collection's by the PlexCollection that
   holds it, else by `collectionTitle`.
2. A collection added from the page that Plex shows no hub for starts on
   Library Recommended alone; one added by a switch starts with that flag
   alone.
3. Adding a collection lists every hub ahead of it, since it goes last.
4. More than 100 hubs to list is refused, naming the count.
5. "Remove" drops the spec entry only; the hub stays Plex's.
6. The reset is not generation-tested: it discards the spec's hubs anyway.
7. Two layouts of one library in a namespace: the first by name stands in
   the ui. Nothing refuses the second.
8. The ui role has `delete` on the kind and `ui/actions` has
   `DeletePlexHubLayout`, but no page offers it yet.
9. Reordering is by up and down buttons; drag and drop was not built.
10. The samples and `pkg/plexfilter`'s reference specs name the new
    library titles; the ui's own test fixtures keep the old ones, which are
    only strings there.

## 5. As built (2026-11-16)

Built as above in clustarr, not deployed, not seen in a browser, and never
run against cluster-plex's controller, which was written at the same time.
Unit tests cover the order and materialise logic, the writes against a fake
client, and the handlers; the CRD's CEL rules were validated offline only.
Open: the chart's cluster-plex dependency version; a browser pass of the
page (the out-of-band row swap, the select and the alert dialog); a
`pkg/crdcheck` envtest of the CEL rules in the final envtest run.
