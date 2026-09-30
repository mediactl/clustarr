# Container search: an author's, artist's or comic's monitored items

Date: 2026-09-30. Approved in conversation.

A Search on an Author failed "kind author is not searchable": the search
worker searches only items with an identity of their own (movie, episode,
album, book, audiobook, issue), and the UI offered Search on every page.
Readarr's author search and Lidarr's artist search mean "search the
monitored books/albums and grab the best", so a Search on a container
now fans out into one child Search per monitored child and each grabs its
best approved release.

## Scope

| Container | Children | Link |
| --- | --- | --- |
| Author | monitored Books | `spec.authorRef` (index `.spec.authorRef`) |
| Artist | monitored Albums | `spec.artistRef` (index `.spec.artistRef`) |
| Comic | monitored Issues | `spec.comicRef` (index `.spec.comicRef`) |

Audiobooks carry no author link and are left out; Series keeps per-episode
search (TV wants season packs, a design of its own). The worker's
`Searchable` refusal stays as the backstop for anything else.

## API (`catalog.clustarr.io/v1alpha1`, Search)

- `spec.grabBest` (bool, optional, default false): once results are in,
  grab the top-ranked **approved** release through the same path as
  `spec.grab`. A release with any rejection, temporary included, is never
  auto-grabbed -- so an item already queued, downloading or at cutoff is not
  grabbed again. The grab is automatic, not a human pick: the Download
  carries `grabbedBy: search` and `manual: false`, so the importer applies
  its upgrade rules.
- `status.children` (`SearchChildren`, optional, counts only -- no list to
  cap): `total`, `running`, `completed`, `failed`, `grabbed`.

## Behaviour (catalogarr's Search controller, `ManagerCatalogarr`)

A Search whose `spec.mediaRef.kind` is author, artist or comic publishes no
search task. On its first reconcile the controller:

1. lists the container's children through the index and keeps those that
   are monitored and **missing or cutoff unmet** (the *arrs' Missing and
   Cutoff Unmet lists; owner's call, 2026-09-30): a book or issue with no
   file or below cutoff, an album with any track lacking a file or below
   cutoff. One at cutoff is not searched at all, nor one not released yet
   (release date after now; an unknown date counts as released), as the
   *arrs' Missing lists hold only what has come out;
2. above `MaxContainerChildren` (200), fails the Search at once
   (`TooManyChildren`) and creates nothing;
3. with none, completes at once ("no monitored books are missing or below cutoff");
4. otherwise applies one child Search per child: named
   `k8s.ChildName(parent, kind+"/"+name)`, controller-owned by the parent,
   labelled `catalog.clustarr.io/parent-search=<parent>`, spec
   `mediaRef` = the child, `grabBest`, `indexerRefs`, `categories`, `limit`
   and `override` from the parent; then marks the parent Running.

The controller owns Search (`Owns`), so each child's change reconciles the
parent, which counts its children by label: Running until every child is
Completed or Failed, then Completed with `finishedAt` (this manager's, as in
query mode, since no worker writes a container Search) and a condition
"N searched, G grabbed, F failed". A child's grab counts when its
`status.grabbed` holds an entry with a `downloadRef`; a Completed child
whose grabBest pick has no `status.grabbed` entry yet still counts as
running, since its grab lands a reconcile later. Before a grabBest pick is
applied, the controller lists the item's Downloads uncached and records an
error instead when another is in flight: the worker's "already queued"
check predates the search. The child tasks stay user-invoked, as a
Search the person pressed is in Sonarr and Readarr, so delay profiles and
the availability check do not hold them.

A child never expires on its own TTL; it is deleted with its parent through
the owner reference, so the parent's counts cannot lose one. A container
Search is never failed as stuck: its children time out on their own.

## UI

On Author, Artist and Comic pages the button reads "Search monitored
books / albums / issues" and creates the parent Search with
`grabBest: true`. An item's own page (movie, episode, book, album, issue)
sets `grabBest` too, as Radarr's and Sonarr's Search does: this design first
said those pages kept an interactive results list, but the ui renders no
Search results, so a movie search found 35 approved releases and grabbed
none (corrected 2026-09-30). While Downloads are paused on
kind-cluster-plex, auto-grabbed Downloads wait on the disabled client.

## Tests

Unit tests on the fake client: fan-out picks only monitored children and a
repeat reconcile creates no duplicate; counts and phase follow the children;
the cap creates nothing; zero children completes; only missing or cutoff-unmet children are searched; `grabBest` grabs only an
approved result, once, as `search`/non-manual; a child never self-expires;
the UI's container button label and the Search it creates.
