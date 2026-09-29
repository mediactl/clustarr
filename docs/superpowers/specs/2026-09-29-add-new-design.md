# Add New: search for and add media from the UI

**Date:** 2026-09-29 · **Status:** design approved section by section by the
owner; this written spec awaits their review · **Amends:** CLAUDE.md's
"The UI never writes status" and "The UI may hold a read-only bus
connection" invariants, ADR-0011's image rule (by extension, not reversal).

## The ask

> We need the ability to search and add media from the UI like Radarr's
> "Add new".

Today a Movie, Series, Artist or Author enters the catalog only through an
import list or a library rescan (both importarr), or through `kubectl
apply`. Radarr, Sonarr, Lidarr and Readarr each have an "Add New" page:
type a title, see the provider's matches with posters, pick one, set a
few options, add it. This design gives the Library the same thing for all
four of its media types.

## Decisions (owner, 2026-09-29)

- **Scope:** Movies, TV, Music and Books, all four in the first version.
- **Books add an author**, as Readarr does, never a single book. Music adds
  an artist, as Lidarr does.
- **Placement:** an "Add New" button on each library page opens a search
  for that page's type. There is no type picker and no sidebar entry.
- **Search runs through catalogarr's existing RPC**, not through a CR.
  Search results are ephemeral and do not belong in etcd.

## What stays true

- **The UI never writes status and owns no CRD.** An add is a create of a
  catalog item with only `spec` set, under field manager `clustarr-ui`.
  `kubectl apply` of the same object is equivalent.
- **The apiserver validates.** A bad add is rejected by the CRD schema and
  CEL rules, and the form is re-rendered with the owner's input and the
  apiserver's message. No rule is restated in Go.
- **A provider never sees a Clustarr viewer** (ADR-0011). Every image the
  browser loads comes from the ui.
- **The UI never reads a Secret**, and it holds no provider API key. Every
  provider call is catalogarr's.

## Design

### Flow

1. Each library page (Movies, TV, Music, Books) gets an **Add New** button
   that opens `GET /library/{kind}/add`.
2. The search box calls `GET /library/{kind}/add/search?q=` through htmx,
   debounced to 400 ms. A query under two characters is not sent.
3. The ui requests `rpc.catalogarr.metadata.search` (`schema.MetadataRequest`
   with the kind and text) over its bus connection, with a 10-second
   deadline, and renders the hits as cards: poster, title, year. A hit
   whose provider id is already in the library (read from the projection
   the Library page uses) carries an "In library" badge that links to the
   item instead of offering an add.
4. Choosing a hit shows its add form (below). Submitting it posts to
   `POST /library/{kind}/add`, which calls `ui/actions.AddItem` and
   redirects to the new item's detail page.

### Search, by kind

catalogarr's `app/catalog/metadata/rpc.go` already serves the search verb
for movie, artist, book and comic. The UI asks for the four kinds below;
two need a new provider search.

| Kind | Provider | Keyed by | Work |
|---|---|---|---|
| Movie | TMDB `SearchMovies` | `spec.tmdbID` | none |
| Series | TVDB v4 `GET /search?type=series` | `spec.tvdbID` | new `SeriesProvider.SearchSeries`, TVDB client, RPC case |
| Artist | MusicBrainz `SearchArtists` | `spec.musicBrainzID` | none |
| Author | Open Library `GET /search/authors.json` | `spec.openLibraryID` | new `SearchAuthors` on the Open Library client, RPC case for `MediaKindAuthor` |

Series are keyed by TVDB id, so the series search must be TVDB's rather
than TMDB's. The new searches follow `pkg/`'s conventions: an injected
limiter, bodies read through `metadata.ReadBody`, sentinel errors, and
tests against responses recorded from the live APIs, as MDBList's were.

A kind with no configured MetadataProvider gets the RPC's "no provider"
answer. The Add page shows it as a notice linking to Settings rather than
as an empty result. (On the owner's cluster, MusicBrainz and Open Library
are not configured yet. Both work without a key.)

### The add form

Shared fields, on every form:

- **Root folder:** the RootFolders of the page's kind, preselected when
  there is exactly one.
- **Quality profile:** the QualityProfiles whose `mediaKind` fits the kind
  (video, music, book).
- **Monitored:** a checkbox, checked by default.

Per kind, each field an existing spec field:

| Kind | Monitor (`spec.addOptions.monitor`) | Also | Search on add |
|---|---|---|---|
| Movie | `movieOnly`, `movieAndCollection`, `none` | `minimumAvailability` | `addOptions.searchForMovie` |
| Series | `all`, `future`, `missing`, `existing`, `firstSeason`, `lastSeason`, `pilot`, `recent`, `none` | `seriesType`, `seasonFolder` | `addOptions.searchForMissing`, `addOptions.searchForCutoffUnmet` |
| Artist | `all`, `future`, `missing`, `existing`, `latest`, `first`, `none` | `monitorNewItems` (`all`, `none`, `new`) | `addOptions.searchForMissing` |
| Author | `all`, `future`, `missing`, `existing`, `none` | `monitorNewItems` (`all`, `none`) | `addOptions.searchForMissing` |

Left at their defaults and not on the form: `folder`, `tags`, the delay,
transcode and subtitle profile refs, and the music and book metadata
profiles. Each stays editable on the item afterwards or with `kubectl`.

The form is built from the shadcn-templ components on the theme's tokens,
held to them by the same guard as the settings pages
(`requireThemedComponents`).

### Writes (`ui/actions.AddItem`)

- One create per add, with `spec` only. `spec.source` is set with an empty
  `importListRef`, which the API already defines as "added by hand".
- **The object's name is the one an import list would give it** for the
  same provider id. The naming functions now in
  `app/import/worker/importlist` (`movieName` and its siblings) move to a
  package both importarr and the ui can import. An add of an item that
  already exists, or one an import list adds later, is therefore the same
  object. `AlreadyExists` redirects to the existing item and never creates
  a duplicate.
- **RBAC:** `ui/actions.Grants()` gains `create` on `movies`, `series`,
  `artists` and `authors`, and nothing else. The chart's copy of the ui
  role follows, held by `TestUIRoleGrantsOnlyReadsAndActionWrites` and
  `TestUIRoleChartMatchesConfig`.

### The bus: read plus request

The ui's bus connection gains exactly one capability: a request to
`rpc.catalogarr.metadata.search`. (The approved design also named
`.lookup`; nothing in the flow uses it, so the allowlist is the one
subject.) The connection it is handed in `ui.Options` exposes only that
request, never a general publish. `TestUINeverWrites` gains a subject allowlist: a request to
any other subject, any publish, and every object-store write already on its
banned list still fail it. CLAUDE.md's invariant is amended to "read plus
metadata requests".

### Search-result posters (ADR-0011)

A search hit's poster is a provider URL, which ADR-0011 forbids the browser
to load. The ui serves it instead:

- `GET /art/search?src=<url>&sig=<hmac>`: the ui signs each poster URL it
  renders with a per-process key, and the route refuses any URL whose
  signature does not verify. It is not an open proxy.
- The fetch is made only to an allowlist of provider image hosts
  (`image.tmdb.org`, `artworks.thetvdb.com`, `coverartarchive.org`,
  `covers.openlibrary.org`). Redirects are refused unless the destination
  is also on the allowlist.
- The body is read through a cap (`io.LimitReader`, 5 MiB) and must be an
  image content type. Anything refused, too large or failing renders as a
  placeholder tile, never a broken image.
- Fetched posters are cached in memory with a bound (LRU, 256 entries) for
  an hour, with `Cache-Control: private, max-age=3600`.

This is the ui pod's first outbound HTTP. Both installers must allow it
wherever egress is restricted.

## Errors

| Case | What the page shows |
|---|---|
| catalogarr does not answer within 10 s | "Metadata search is not responding" with Retry |
| No provider for the kind | A notice linking to Settings |
| Provider error (bad key, rate limit) | The message, clamped |
| No hits | An empty state |
| Apiserver rejects the add | The destructive alert, form re-rendered with the owner's input |
| Item already exists | Redirect to it |
| Poster refused or failed | Placeholder tile |

## Testing

- **Provider searches:** `SearchSeries` (TVDB) and `SearchAuthors` (Open
  Library), tested against recorded live responses. No network in tests.
- **RPC:** catalogarr answers a series and an author search. The contract
  runs on the in-memory bus and on a real embedded NATS server.
- **UI:** the search renders hits with the in-library badge. Each of the
  four forms creates exactly the expected spec, proven against a real
  apiserver (envtest), including `AlreadyExists` redirecting and a CEL
  rejection re-rendering the form.
- **Guards:** `TestUINeverWrites` fails on a request to a subject off the
  allowlist, on any publish and on any object-store write. The RBAC guard
  admits exactly the four new `create` grants.
- **Poster proxy:** a bad signature, a host off the allowlist, an
  off-allowlist redirect, a non-image body and an oversized body are each
  refused.
- **Theme guard:** the add pages pass `requireThemedComponents`.
- **E2E:** a scenario in `test/e2e` against the in-cluster metadata
  fixtures, written now and run with the rest in Phase H.

## Out of scope

- Deleting an item from the UI.
- Bulk add, list import from the Add page, and "add by id" (a TMDB or TVDB
  id typed directly).
- Editing folder, tags and profile refs at add time (defaults apply).
- Comics and audiobooks.
