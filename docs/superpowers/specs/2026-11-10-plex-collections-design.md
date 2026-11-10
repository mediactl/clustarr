# Plex collections as resources: design

Status: approved by the owner, 2026-11-10 ("That looks perfect - take
option A and implement"). cluster-plex writes status (option A, below).
Extended the same day (owner: "we should also include normal
collections, the ability to customize the image, the ability to change
its settings (like hide in library, show on home, etc)"). The extension
is in "Normal collections" and "Presentation" below; the API calls they
use were proven on a stock PMS 1.43.5 and, for hub promotion, read on
talos.

## Intent

The owner wants Plex's smart collections declared as Kubernetes resources
that clustarr's web UI can edit (owner, 2026-11-10: "Custom Resource the
clustarr UI can edit"; "For now, start with the existing smart collections
in Plex"). Today they exist only inside Plex, made by hand in Plex Web's
filter editor, and survive only as long as Plex's database does. As
resources they are declared, versioned with the rest of the stack, editable
without Plex Web, and put back if Plex loses them.

The first version covers smart collections, where Plex evaluates a
filter, and normal collections, where the resource names the members.
Both carry their presentation: poster, background, summary, sort title,
collection mode and where Plex promotes the collection (library
Recommended, Home, friends' Home). Collections whose members clustarr
computes from its own data (the "anime with an English dub" kind of rule
that Plex cannot express) are a later phase, and this design leaves room
for them without building them.

The three collections the owner named are the reference cases. Each must
be expressible, and each must round-trip to the filter Plex Web stores
today.

| Collection | Library | Plex Web's rule | Stored filter (talos, 2026-11-10) |
| --- | --- | --- | --- |
| Anime Dubbed | Clustarr TV (shows) | all of: Audio Language is English; Genre is Anime | `type=2&sort=titleSort&episode.audioLanguage=en&and=1&show.genre=6437` |
| Netflix | Clustarr TV (shows) | all of: Network is Netflix | `type=2&show.network=6787` |
| UP | Clustarr Movies | any of: Title is "Seven Up!"; all of (Genre is Documentary; Title contains "Up") | `type=1&push=1&push=1&title==Seven Up!&pop=1&or=1&push=1&genre=5836&and=1&title=Up&pop=1&pop=1` |

Each stored filter is the query part of the collection's `content` URI,
`server://<machine id>/com.plexapp.plugins.library/library/sections/<n>/all?...`.
The numbers 6437, 6787 and 5836 are this server's tag ids for Anime,
Netflix and Documentary. They differ between servers and after a rebuild.

## Goals

- A namespaced kind, `PlexCollection`, whose spec is a Plex collection:
  library, title, item type, and either a smart filter (with sort and
  limit) or a list of members, plus its presentation.
- cluster-plex reconciles each one into Plex. It creates the collection
  or adopts one of the same title. It keeps the filter, or the members,
  equal to the spec. It applies whatever presentation the spec states.
- The clustarr UI lists and edits them in an editor modelled on Plex
  Web's. A smart collection gets match all/any, rows of field, operator
  and value, nested groups and limit. A normal collection gets a member
  picker over the library. Both get image, settings and visibility
  sections.
- The three reference collections become resources, adopting the
  existing Plex collections so their ratingKeys, posters and hub
  placement are kept.

## Non-goals (this version)

- Collections computed from clustarr's data (phase 2; see "Later").
- A live preview of the matching items in the editor: the UI holds no
  Plex token. The item count arrives in status after the sync.
- Uploading an image file from the browser. An image is a URL that
  cluster-plex fetches; the UI has nowhere to store an upload (it writes
  no object store, ADR-0011, and `TestUINeverWrites`).
- A custom item order in a normal collection (Plex's drag-to-reorder).
  Release date and alphabetical are supported.
- Normal collections of seasons or episodes. Members are movies or
  shows.
- Importing every existing smart collection automatically. The four not
  named (A24, Documentaries, Pixar, Studio Ghibli) stay as they are until
  someone declares them.

## Anti-goals

- Losing what the owner set in Plex Web on a collection it adopts: its
  poster, sort, hub promotion or ratingKey. Adoption changes only what
  the spec states. Every optional field left out means "keep Plex's",
  so a resource that states only a filter changes only the filter.
- Adopting a collection Plex's metadata made: a movie's TMDB
  collection, which clustarr's provider hands Plex as a Collection tag.
  Plex rewrites its membership on every refresh of its items, so a
  resource managing it would fight Plex forever.
- Deleting a Plex collection the resources never created or adopted.
- A filter that compiles to something Plex Web cannot open and edit.
  The compiled form follows Plex Web's own encoding exactly, so a
  collection stays editable in Plex Web.

## The resource

`catalog.clustarr.io/v1alpha1`, kind `PlexCollection`, namespaced (the
release namespace, `clustarr-system` on talos). The kind lives in
clustarr's API, so clustarr's chart installs the CRD and the UI edits it
through its typed client. The name carries "Plex" because clustarr's
catalog already has a different "collection": a movie's TMDB collection
(`status.metadata.collection`), which the Plex provider serves as type 18.

```yaml
apiVersion: catalog.clustarr.io/v1alpha1
kind: PlexCollection
metadata:
  name: up
  namespace: clustarr-system
spec:
  library: Clustarr Movies     # a Plex library section, by title
  title: UP                    # the collection's title in Plex
  itemType: movie              # movie | show | season | episode
  filter:
    match: any                 # all | any
    rules:
      - field: title
        operator: is
        value: "Seven Up!"
      - match: all             # a group
        rules:
          - field: genre
            operator: is
            value: Documentary
          - field: title
            operator: contains
            value: Up
  # sort and limit are optional; absent means "keep Plex's" (see below).
  # UP has neither today.
```

The other two:

```yaml
spec:
  library: Clustarr TV
  title: Anime Dubbed
  itemType: show
  filter:
    match: all
    rules:
      - {field: episode.audioLanguage, operator: is, value: en}
      - {field: show.genre, operator: is, value: Anime}
  sort: {field: titleSort}
---
spec:
  library: Clustarr TV
  title: Netflix
  itemType: show
  filter:
    match: all
    rules:
      - {field: show.network, operator: is, value: Netflix}
```

### Fields

`field` is Plex's own filter key, as Plex lists it in a library's
`/library/sections/<n>/all?includeMeta=1` under `Meta.Type[].Field[]`:
`title`, `genre`, `studio`, `audioLanguage` for a movie library;
`show.genre`, `show.network`, `episode.audioLanguage` for a TV library.
Using Plex's keys keeps the mapping exact and lets new Plex fields work
without a schema change. The schema checks the shape only (a pattern of
`[a-zA-Z]+(\.[a-zA-Z]+)?`); the reconciler checks the field against the
library's live catalogue and reports an unknown one in status.

As read from talos's PMS 1.43.5: a movie library has 39 fields, a TV
library 20 show fields and 26 episode fields. Each field has a type:
`tag`, `string`, `integer`, `date`, `boolean`, `audioLanguage`,
`subtitleLanguage` or `resolution`.

### Operators

`operator` is a word, translated per field type to Plex's symbol, which
the same Meta lists under `FieldType[].Operator[]`:

| Operator | tag | string | integer | date | boolean | language | resolution |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `is` | `=` | `==` | `=` | | | `=` | `=` |
| `isNot` | `!=` | `!==` | `!=` | | | `!=` | |
| `contains` | | `=` | | | | | |
| `doesNotContain` | | `!=` | | | | | |
| `beginsWith` | | `<=` | | | | | |
| `endsWith` | | `>=` | | | | | |
| `greaterThan` | | | `>>=` | | | | |
| `lessThan` | | | `<<=` | | | | |
| `before` | | | | `<<=` | | | |
| `after` | | | | `>>=` | | | |
| `isTrue` | | | | | `=` (value `1`) | | |
| `isFalse` | | | | | `!=` (value `1`) | | |

The schema holds `operator` to that enum. A field/operator pair the
table leaves blank is reported as invalid in status, never sent.

### Values

`value` is a string, written the way a person reads it:

- A tag field (`genre`, `show.network`, `label`, `actor` and so on)
  takes the tag's name ("Anime", "Netflix"). The reconciler resolves the
  name to this server's tag id through the field's value list, whose
  endpoint the same Meta names (`Meta.Type[].Filter[].key`, for example
  `/library/sections/2/genre?type=2`). Matching is case-insensitive. A
  name the library does not hold is reported, and the collection keeps
  its last good filter.
- A language field takes an ISO 639-1 code ("en"), as Plex stores it.
- A date field takes an absolute date (`2024-01-01`) or Plex's relative
  form (`-6w`, `-1y`).
- A boolean field takes no value; the operator decides.
- `string` and `integer` values pass through.

Names, not ids, make a resource portable: it applies unchanged to the
kind rig's Plex and to talos's.

### Nesting

`filter.rules[]` holds conditions (`field`, `operator`, `value`) and
groups (`match`, `rules`). A group's `rules` hold conditions only, so the
nesting is two levels deep: the top level and one level of groups. A CEL
rule requires each entry to be exactly one of a condition or a group.
OpenAPI v3 structural schemas cannot recurse, and every reference
collection fits in two levels (UP uses both). A third level would be one
more Go type with the same shape, if a collection ever needs it.

The lists are capped (`rules` at 32 per level) like every list in
clustarr's API.

### Sort and limit

`sort` (`{field, descending}`) is optional. `sort.field` is Plex's sort
key (`titleSort`, `addedAt`, `rating`, `originallyAvailableAt`,
`random`, and the rest a library's Meta lists under `Type[].Sort[]`),
compiled to `sort=<key>` or `sort=<key>:desc`. `limit` (optional, at
least 1) compiles to Plex Web's LIMIT TO parameter. The builder confirms
its exact name against Plex Web's encoding (it is believed to be
`limit`).

**Absent means Plex's own** (my call): with no `sort` in the spec, the
reconciler carries the `sort=` the collection's current filter holds into
every filter it sends. A sort set in Plex Web therefore survives
adoption and every later sync. A new collection gets none, which is
Plex's default. `limit` works the same way. A `sort` or `limit` in the
spec is the resource's, and wins like the rest of the filter.

### Normal collections

A resource holds either `filter` (smart) or `members` (normal), never
both. A CEL rule requires exactly one. Another requires the choice never
to change (`has(self.filter) == has(oldSelf.filter)`), because Plex
cannot turn one kind into the other. `library` and `itemType` never
change either (CEL transition rules, my call); a collection moves by
deleting and recreating its resource.

```yaml
spec:
  library: Clustarr Movies
  title: Studio Ghibli Essentials
  itemType: movie              # movie or show for a normal collection
  members:
    items:                     # clustarr items, by name, in this namespace
      - {kind: Movie, name: spirited-away-2001}
      - {kind: Movie, name: my-neighbor-totoro-1988}
  settings:
    order: releaseDate         # releaseDate | alphabetical
```

`members` is an object so that computed collections can add
`members.selector` beside `items` later (my call). A member is a clustarr
Movie (for `itemType: movie`) or Series (for `show`) in the resource's
namespace. `items` is capped at 1000. cluster-plex finds each member in
Plex by the GUID its projection already gives that item
(`pkg/plexproject.GUID`, keyed by the object's UID):

- `plex://movie/<id>` when clustarr knows the item's Plex id;
- else the clustarr provider's own GUID.

Every edition of a movie is a member (`plexproject.BaseGUID`). A member
Plex doesn't hold yet, because it isn't scanned or matched, is reported
in status and added at a later sync once it appears.

**Removals** (my calls):
- The resource wins over members too. An item added to the collection in
  Plex Web that is not among the members is removed.
- Removal never empties a collection. Plex deletes a normal collection
  when its last item goes, and the collection's ratingKey, poster and hub
  would go with it. So when no member resolves to a Plex item, nothing is
  removed.
- When clustarr's items can't be read at all (the resolver fails), no
  item is added or removed in that pass.

### Presentation

Every field here is optional, and absent means Plex's own is kept.

```yaml
spec:
  summary: "Every Up film, 1964 to 2019."
  sortTitle: "Up 00"
  poster:
    url: https://image.tmdb.org/t/p/original/abc.jpg
  background:
    url: https://image.tmdb.org/t/p/original/def.jpg
  settings:
    mode: hideItems            # default | hideCollection | hideItems | showCollectionAndItems
    order: alphabetical        # normal collections only
  visibility:                  # all three are set together when present
    libraryRecommended: true
    home: true
    friendsHome: false
```

- **`summary`, `sortTitle`.** Set through the collection's edit
  (`PUT /library/sections/<n>/all?type=18&id=<rk>&summary.value=...&summary.locked=1`,
  and the same with `titleSort`).
- **`poster`, `background`.** Each is an `http` or `https` URL.
  cluster-plex fetches it itself, then uploads the bytes. That lets an
  in-cluster URL work, such as clustarr's own `/art`, which PMS refuses
  to fetch from a private address. The fetch is capped at 10 MiB and
  must be an image content type.
  - Which URLs (my call), so that editing a resource can't make
    cluster-plex probe internal services:
    - `https` to any host whose every resolved address is public;
    - anything under clustarr UI's own `/art/`, at its external URL
      (on talos `https://clustarr.k8s.appkins.io/art/`, a LAN address),
      which cluster-plex is told through `plex.clustarr.uiURL`;
    - nothing else, including private, loopback and link-local
      addresses, other in-cluster services, and redirects to any of
      them.
  - The UI checks the same rule before a write.
  - Upload: `POST /library/metadata/<rk>/posters` or `/arts`, with the
    image as the body. Plex selects the upload at once and names it
    `upload://posters/<sha1 of the bytes>`.
  - So the poster is in sync while the selected entry of
    `GET /library/metadata/<rk>/posters` is the upload whose SHA-1 is
    the fetched image's. Otherwise cluster-plex uploads it again.
  - The URL is fetched again only when it changes, or every 24 hours.
  - Proven on stock PMS 1.43.5.
- **`settings.mode`** is Plex's collection mode:

  | Value | Plex value | Meaning |
  | --- | --- | --- |
  | `default` | -1 | library default |
  | `hideCollection` | 0 | hide this collection but show its items ("hide in library") |
  | `hideItems` | 1 | hide the items in this collection |
  | `showCollectionAndItems` | 2 | show both |
- **`settings.order`** is Plex's collection sort: `releaseDate` (0) or
  `alphabetical` (1).
  - Both settings go through `PUT /library/metadata/<rk>/prefs?collectionMode=&collectionSort=`
    and are read back with `GET /library/metadata/<rk>?includePreferences=1`.
    Proven on stock PMS.
  - A smart collection's order comes from its filter's sort, so `order`
    is refused there (CEL).
- **`visibility`** is where Plex promotes the collection as a hub of its
  own (Plex Web's "Visible on"): the library's Recommended tab, the
  owner's Home, and friends' Home.
  - Read: `GET /hubs/sections/<n>/manage`, which works on talos, a
    claimed server, and answers 404 on an unclaimed one. A collection's
    hub is the entry whose identifier is
    `custom.collection.<section>.<ratingKey>`.
  - Create: `POST /hubs/sections/<n>/manage?metadataItemId=<rk>&promotedToRecommended=&promotedToOwnHome=&promotedToSharedHome=`.
  - Update: `PUT /hubs/sections/<n>/manage/<identifier>?...`.
  - The writes are confirmed on the claimed rig before they're relied
    on.
  - All three false means promoted nowhere, and a missing hub entry
    reads the same (my call). With no entry and all false, nothing is
    written.

### Status

```yaml
status:
  observedGeneration: 3
  ratingKey: "26252"          # the Plex collection this resource holds
  adopted: true               # it existed before the resource did
  content: "library/sections/1/all?type=1&push=1&..."   # smart: Plex's filter after the sync
  itemCount: 8                # Plex's childCount after the last sync
  missingMembers: 1           # normal: members Plex doesn't hold yet
  missing: [{kind: Movie, name: ...}]   # the first 20 of them
  poster: {url: "...", sha1: "e9dfa006..."}       # what was uploaded
  background: {url: "...", sha1: "..."}
  lastSyncedAt: "2026-11-10T14:00:00Z"
  conditions:
    - type: Synced            # True once Plex matches everything the spec states
      reason: Synced | Created | Adopted | MembersPending | Waiting | InvalidRule | UnknownValue | LibraryNotFound | NotSmart | NotAdoptable | Conflict | ImageUnavailable | PlexUnavailable
      message: "..."
```

`Synced` is `True` for `Synced`, `Created`, `Adopted` and
`MembersPending`, and `False` for the rest (my calls).

- `MembersPending`: everything is synced, but some members aren't in
  Plex yet; the message counts them.
- `Waiting`: a new normal collection none of whose members is in Plex
  yet, so nothing has been created.
- `ImageUnavailable`: the URL failed, was refused, was too large or
  wasn't an image. It leaves the image as it is and doesn't hold back
  the rest of the sync, but the condition says what failed.

## Reconciling (cluster-plex)

cluster-plex already reads clustarr's resources through dynamic
informers (`pkg/clustarrwatch`, `pkg/clustarrobj`) and already turns
declared state into PMS API calls (the Live TV converger,
`pkg/plex/livetv`). A collection controller joins its manager beside the
converger. It is level-driven: on a resource change, on Plex's start,
and every 10 minutes.

For each resource:

1. **Library.** Resolve `spec.library` to a section key by title from
   `/library/sections`. If no section or several match, report
   `LibraryNotFound` and stop.
2. **Compile (smart).** Read the section's Meta (cached per section and Plex
   start), check every field and operator, and resolve every tag name to
   its id. Build the query exactly as Plex Web does, so the reference
   collections compile byte-for-byte to the filters above:
   - `type=<1|2|3|4>` for the item type;
   - then `sort=...` (the spec's, else the one the collection holds
     now, else none), then the filter, then the limit when there is one;
   - a top level of `match: all` holding only conditions joins them with
     `&and=1&`; one condition stands alone;
   - a group, and every entry of a top level holding a group or using
     `match: any`, is wrapped in `push=1&...&pop=1`. Entries are joined
     by `&and=1&` or `&or=1&`, and the whole filter is wrapped once more
     when the top level is `any` (UP's form);
   - values are URL-encoded the way Plex Web encodes them.

   The golden tests are the three reference filters, compiled from the
   three resources above with the talos tag ids. Anime Dubbed's
   resource states `sort: {field: titleSort}`; UP's and Netflix's state
   none. All three compile to exactly the filters in the table.
   A normal collection skips this step. Instead it resolves its members
   to Plex ratingKeys by GUID (see "Normal collections"), reading the
   clustarr Movies and Series through its dynamic client. cluster-plex
   already holds `get, list, watch` on them.
3. **Find the collection.** Look it up by `status.ratingKey`. If that
   fails, adopt one of the same title and item type in the section, and
   record `adopted: true`. The candidate must:
   - be of the same kind (a smart resource adopts only a smart
     collection, a normal resource only a normal one; otherwise
     `NotSmart`);
   - have been made by a person, which Plex marks with a
     `collection://<uuid>` GUID. A metadata-made collection is
     `NotAdoptable`.

   If nothing can be adopted, create one:
   - smart: `POST /library/collections?type=<t>&title=<title>&smart=1&sectionId=<n>&uri=server://<machine id>/com.plexapp.plugins.library/library/sections/<n>/all?<query>`;
   - normal: the same with `smart=0&uri=server://<machine id>/com.plexapp.plugins.library/library/metadata/<rk>,<rk>,...`.

   A normal resource with no member in Plex yet waits rather than create
   an empty collection.
4. **Sync the content.**
   - Smart: when the collection's `content`, normalised (URL decoding,
     the `server://` prefix dropped), differs from the compiled query,
     send `PUT /library/collections/<ratingKey>/items?uri=server://...`.
     Always send `server://` URIs: plex-postgresql .57 exists because a
     rewritten `library://` form broke every smart collection's start-up
     fixup.
   - Normal: compare the collection's children
     (`GET /library/collections/<rk>/children`) with the resolved
     members.
     - Add the missing ones in one `PUT /library/collections/<rk>/items?uri=server://.../library/metadata/<rk>,...`.
     - Remove each extra with `DELETE /library/collections/<rk>/items/<item rk>`. The builder confirms this call on stock PMS before relying on it.
5. **Title, summary, sort title.** Each one the spec states and Plex
   differs from is set with
   `PUT /library/sections/<n>/all?type=18&id=<ratingKey>&<field>.value=...&<field>.locked=1`.
6. **Presentation.** Images, `settings` and `visibility`, as
   "Presentation" describes, each only when stated.
7. **Status.** Record the ratingKey, Plex's `content` or the missing
   members, `childCount`, the uploaded images and the condition.

**The resource wins.** Plex Web edits to anything the spec states are put
back at the next sync, and the condition's message says so: the filter
or members, the title, summary or sort title, the images, the mode,
order or visibility. Anything the spec leaves out is never touched.

**Deletion.** A finalizer (`clusterplex.io/plex-collection`) deletes the
Plex collection (`DELETE /library/collections/<ratingKey>`) when the
resource is deleted, created or adopted alike. Without a recorded
ratingKey it deletes nothing.

**Where it runs** (my call): in cluster-plex's manager, on the lease
holder, whenever the clustarr integration (`plex.clustarr` in its chart)
is on. It does not depend on Live TV or its one-pod rule. talos runs one
Plex pod. With several pods, all PMS processes share one database. The
builder checks on the rig whether a collection created or changed
through one pod's PMS shows on another pod without a restart. If it
does not, the controller restarts the other pods the way the DVR path
already does.

## Who writes status

clustarr's invariant (ADR-0019) is that its manager writes every
clustarr resource. cluster-plex has only ever read clustarr's resources.
It reports through Leases (`livetv-<device>`) and Events, and clustarr's
manager renders those into status. Two ways to keep a collection's sync
state visible:

- **A: cluster-plex writes `status`** under its own field manager
  (`cluster-plex`), with `get, list, watch, patch` on `plexcollections`
  (the finalizer, by server-side apply) and `patch` on
  `plexcollections/status`. This is the spec/status split
  MediaFile already uses: spec by the UI, kubectl or Terraform, status by
  cluster-plex alone. cluster-plex also holds the deletion finalizer: it
  adds the finalizer, deletes the Plex collection, then removes it. That
  is the `patch` on the main object, and the only write to it. It is an
  exception ADR-0019 would name.
- **B: cluster-plex reports through a Lease or Event per collection**,
  and clustarr's manager renders status from it. This keeps the
  invariant, at the cost of a second controller and a hop. It also needs
  a handshake for deletion, because cluster-plex could not hold the
  finalizer:
  1. clustarr's manager adds the finalizer.
  2. On deletion, cluster-plex deletes the Plex collection and reports
     it.
  3. The manager then releases the finalizer.

  Without that handshake, deleting a resource would leave its collection
  in Plex.

**Decided: A** (owner, 2026-11-10). It is the simplest, the controller
that knows the outcome writes it, and B's only gain is the letter of an
invariant that was written about clustarr's own agents. clustarr's
CLAUDE.md names the exception beside ADR-0019's invariant:
`PlexCollection` status and its finalizer are cluster-plex's.

## The UI

A **Collections** entry in the Library sub-menu, beside Movies, TV, Music
and Books, swapped through htmx like its siblings. It lists every
PlexCollection by library, each row showing:

- the poster, through `/art/search` when its host is one the proxy
  allows, else a placeholder;
- the title;
- a one-line reading of the rule ("Genre is Anime and Audio language is
  English") or "N items";
- the item count and the Synced condition.

The editor dialog follows Plex Web's layout (the owner's screenshots), in
sections:

- **General:** title, library, item type, smart or normal (the last
  three fixed after creation), summary, sort title.
- **Filter (smart):**
  - a "Match all/any of the following" select;
  - a "+" that adds a condition and a "⋮" that adds a group;
  - condition rows of field, operator and value selects, each with "−";
  - LIMIT TO and sort.
- **Members (normal):** a search over the library's items of the
  collection's type, from the projection, as the header search finds
  them. Each pick joins a list of chips with remove buttons, poster and
  year shown.
- **Images:** poster and background URL fields, each with a preview
  through `/art/search` when the host is allowed, and a clear button that
  drops the field (Plex keeps its own).
- **Settings:** collection mode; for a normal collection, the order.
  Each is a select whose first option is "Keep Plex's", which leaves the
  field out.
- **Visibility:** a "Manage visibility" switch. Off leaves `visibility`
  out. On shows three switches: Library Recommended, Home, Friends'
  Home.

The components follow the UI's rules: shadcn-templ `select`, `switch`,
`textarea` and input groups, an `alertdialog` for delete, a toast for
the result.

The field list and labels, and the operators per field type, come from a
table in clustarr (`pkg/plexfilter`) generated from Plex's Meta and
checked into the repo. The UI holds no Plex token. The value pickers for
tag fields offer the names the projection already holds: genres,
networks, studios and keywords come from clustarr's own metadata, which
is where Plex got them, since clustarr is Plex's metadata provider.
Every value field still accepts free text.

Writes go through `ui/actions` (create, merge patch of spec under the
read resourceVersion, delete), with the role grants `TestUINeverWrites`
and the role guard allow: `create, patch, delete` on `plexcollections`,
never status.

## Seeding

The three reference collections are applied as resources once (kubectl
or the UI), not through Terraform: Terraform would revert every UI edit.
Each adopts its existing Plex collection by title.

UP's current Plex filter is the one repaired through the API on
2026-11-10. Its resource states the rule Plex Web shows, so adoption
changes nothing.

## Testing and proof

- cluster-plex:
  - compile tests with the three golden filters;
  - a fake PMS (`fakepms`) for:
    - find, adopt, create, sync, rename and delete, and the drift that
      is put back;
    - membership adds and removes, and members Plex lacks;
    - an image uploaded once and again only when its SHA-1 is no longer
      selected;
    - mode, order and visibility set only when stated;
  - a test that adoption never changes anything the spec leaves out.
- clustarr: `pkg/plexfilter` validates a spec in Go (condition xor
  group, a field/operator pair its type allows, the caps). The UI uses it
  before a write, and plain `go test` covers it, along with the table
  against a recorded Meta and the UI's rendering and actions. The CRD's
  CEL rules repeat the structural checks as a guard at the API server.
  Their tests are `pkg/crdcheck` envtests, which run only in the owner's
  single final envtest run (owner, 2026-10-08), so they are written in
  that test batch, not now (my call).
- Proof: the kind rig's Plex first (adopt, edit, delete), then talos,
  where the three must adopt with no change to their items.

## Later

- **Computed collections:** `spec.members.selector` beside, or instead
  of, `members.items`. Its members are chosen by a selector over
  clustarr's items (labels, MAL-Dubs state, audio languages, profiles)
  and synced through the same normal-collection path. That serves rules Plex
  cannot express, such as "anime with an English dub per MAL-Dubs".
- Uploading an image from the browser, once something can store it.
- A custom member order, and normal collections of seasons or episodes.
- Importing an existing Plex collection into a resource from the UI.

## Left to the builder

- The exact Plex Web encoding of LIMIT TO and of a relative date, read
  from a collection saved in Plex Web.
- The CRD's printer columns and short name.
- How the reconciler caches section Meta and tag lists, and invalidates
  them.
- The editor's markup within the UI's component rules.
