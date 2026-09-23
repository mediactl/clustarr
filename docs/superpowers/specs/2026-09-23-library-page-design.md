# Library page: tabs, parents, series and season pages

**Status:** approved in conversation on 2026-09-23; amends design amendment 1
§A3.4's Library row. Where this document and §A3.4 disagree, this document
wins for the library page.

## Goal

The library page shows one tab per media type -- Movies, TV, Music, Books --
and each tab shows only that type's *parents*: the items a person looks for,
never their children. Every card carries cover art, the monitored flag and the
quality profile. A series has a page of its own listing its seasons; a season
expands into its episodes; seasons and episodes are monitorable from there.
Artists and authors get the same treatment with albums and books.

Out of scope (follow-ups, in order of value): the on-disk cover-art cache
§A3.4 describes; bulk operations and filtering toolbars; child pages for
albums, books and issues beyond the monitor toggle.

## Decisions

1. **Cover art is hotlinked, not cached.** Cards and headers render the
   provider URL already in `status.metadata.images` (the first image whose
   `type` is `poster`). §A3.4 says the metadata gateway caches art on `/data`
   and the UI serves it from disk; that cache does not exist yet and is the
   first follow-up. Until then the browser fetches from the provider's image
   CDN with `loading="lazy"` and `referrerpolicy="no-referrer"`, so the CDN
   never learns the UI's address. The UI itself still never calls a provider.
2. **Tabs show parents only.** Movie → Movies; Series → TV; Artist → Music;
   Author, Comic and Audiobook → Books (Comic and Audiobook have no parent
   kind of their own), and so does a Book with no `authorRef`, which stands
   alone. Episode, Album, Issue and an author's Book produce no card; they
   still feed the pipeline page.
3. **Children load lazily, per season.** A series page renders one season
   component per season with a collapsed episodes area that `hx-get`s its
   episodes on first expand. The same route serves a full page without the
   `HX-Request` header, so a season is linkable on its own.
4. **Seasons and episodes are monitorable from the series page.** Toggles
   `hx-post` and receive their re-rendered component, so the expanded state
   survives and the new value shows at once.

## Pages and routes

| Route | Renders |
|---|---|
| `GET /library` | redirect to `/library/movies` |
| `GET /library/{tab}` | the tab's poster grid; `tab` ∈ movies, tv, music, books, else 404 |
| `GET /events/library/{tab}` | SSE stream of that tab's rows only |
| `GET /library/{ns}/{kind}/{name}` | the item page; series-, artist- and author-specific below, the existing detail page otherwise |
| `GET /library/{ns}/series/{name}/seasons/{n}` | the season's episodes component with `HX-Request`, a full page without |
| `POST /library/{ns}/series/{name}/seasons/{n}/monitor` | season toggle; replies with the season component to htmx, redirects otherwise |
| `POST /library/{ns}/{kind}/{name}/monitor` | existing; an htmx request from an episode row gets the row back |

**Card:** poster (placeholder until metadata arrives), title, year, monitored
badge, quality profile name, phase where the kind has one; links to the item
page.

**Series page:** header as the existing detail page (poster, title, monitored
toggle, profile, search now) followed by one season component per entry of
`status.seasons`: number, episode and file counts, next airing, monitor toggle,
collapsed episodes area. **Episode row:** number, title, air date, monitored
toggle, file and quality, phase.

**Artist and author pages** reuse the season and episode component shapes
with albums or books as the children, loaded lazily, monitored through the
existing item action.

## Projection and reads

`projection.LibraryItem` gains `Year`, `Poster`, `QualityProfileRef` and
`Tab`. The projection keeps its one tick over every kind and one library
stream; `/events/library/{tab}` filters every frame with `ForTab` before
writing it, so a subscriber receives only its tab's rows. Parent-only
filtering happens in the projection, so the grid, the stream and the tests
share one rule.

The series, season, artist and author pages read at request time through the
UI's cache reader: `Get` the parent, `List` its children from the cache and
filter by parent ref in memory. A field index is added only if listing proves
slow. After a toggle the component is rendered from the object the patch
returns, not from the cache.

## Actions

- **Episode toggle:** the existing `SetMonitored` for kind `episode`.
- **Season toggle:** `SetSeasonMonitored(ctx, reader, patcher, namespace,
  series, number, monitored)` reads the Series, copies `spec.seasons`, sets or
  adds the entry for `number`, and sends `{"spec":{"seasons":[…]}}` as a merge
  patch under the `clustarr-ui` field manager, carrying the `resourceVersion`
  it read so a raced write is a conflict, not a silent overwrite; one re-read
  and retry, then the conflict is reported. The `series` patch grant already
  exists, so `Grants()` and `ui_role.yaml` are unchanged.
- **Failures:** an htmx request gets its component back with an inline error;
  a plain form post keeps the `ActionError` page.

Every write stays in `ui/actions`; nothing writes status.

## Metadata refresh

A refresh only ever ran when metadata was stale by age (`RefreshTTL`: seven
days for a released movie, thirty for an ended series), so a field a provider
now publishes -- the artwork above -- reached no existing item. Spec §5 already
gives `MetadataTask` a `refreshEpoch` that "increments whenever an operator
forces a refresh"; this fills in the trigger.

- **Trigger:** the annotation `clustarr.io/refresh-metadata: <epoch>`
  (`catalogv1alpha1.AnnotationRefreshMetadata`) on a kind with metadata of its
  own -- Movie, Series, Artist, Album, Author, Book, Audiobook, Comic. The value
  is a positive integer, by convention the requester's Unix time.
  `ui/actions.RefreshMetadata` writes it as a merge patch under `clustarr-ui`
  (the item's existing patch grant; `Grants()` unchanged) from a "Refresh
  metadata" button on every item page; `kubectl annotate` does the same.
- **Consumer:** `catalogarr/metadata.Refresher`, one metadata-only controller
  per kind, modelled on `history.Replayer`, under catalogarr's controller
  role. It publishes `MetadataTask{mediaRef, refreshEpoch}` on the
  high-priority metadata subject with id
  `<uid>:<generation>:metadata:refresh:<epoch>`, which no scheduled task
  shares, records a `MetadataRefreshRequested` Event, and removes the
  annotation with a JSON patch that first tests the value it handled, so a
  newer request written meanwhile is handled on the requeue rather than lost.
  A value that is not a positive integer is refused (`MetadataRefreshRefused`)
  and removed.
- **Gateway:** `Handler.Handle` skips its cache lookup when `refreshEpoch > 0`
  and fetches from the provider, then stores the fresh document as usual.
- **Item reconcilers:** unchanged; the gateway's status write moves
  `refreshedAt`, which already wakes them.

## Tests

- **Projection:** table tests over real API objects: parents only; tab per
  kind; `Poster` from the poster image, empty without metadata; profile and
  year; per-tab stream rows.
- **Handlers and templates (httptest):** the redirect; a tab renders only its
  kind with poster, badge and profile; the series page renders a season
  component per season with its `hx-get`; the season route's partial versus
  page; episode rows; toggle targets; the inline error.
- **Actions (envtest):** one season flipped and the rest preserved; an entry
  added for a season with no override; `clustarr-ui` in `managedFields`; a
  real second writer between the read and the patch, retried and both
  changes kept.
- **Guards:** `TestUINeverWrites`, the role guard and
  `TestBothUICommandsWireEveryUIOption` keep holding.
- **End to end:** scenario 14 gains the tabs, a series page and a season
  toggle; written now, executed in Phase H with the rest.

Each piece is falsified: revert it and watch its test fail by name.
