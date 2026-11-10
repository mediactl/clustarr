# ADR-0022: Plex reads clustarr's music and audiobook metadata from NFO files the manager renders and the import agent writes; the custom-provider roots for music stay flagged off

**Status:** Accepted, 2026-11-10. Built the same day (`pkg/nfo`, the `import-nfo` consumer,
the item key's `nfo` stage under `catalog-nfo`); the Plex roots of §7 of the design are built
behind `--plex-music-provider`, off everywhere.

## Context

Movies and TV reach Plex through the Custom Metadata Provider protocol (ADR-0012): `ui/plex`
answers Plex's match and metadata requests from the projection, so a library's metadata is
clustarr's own. The owner asked (2026-11-10) for the same for music and audiobooks, "with an
additional caveat that music custom metadata is in its infancy", and pointed at Plex's new
support for reading artist and album metadata from NFO files in a music library.

Two facts settled the shape, both verified that day
(`docs/superpowers/specs/2026-11-10-non-video-libraries-design.md` §0 and §7.6):

1. **No PMS build accepts a custom provider for music.** Plex's announcement of the protocol
   lists movie and TV libraries; its own type table numbers artist 8, album 9 and track 10. A
   spike against `plexinc/pms-docker:beta` (PMS 1.43.5.11079, unclaimed, a stub provider on
   the loopback) registered a root declaring type 1 (HTTP 200) and refused every root declaring
   8, 9 or 10, alone or beside 1, with `HTTP 400 The provider supports unsupported metadata
   types`, after fetching the root. The refusal is a whitelist in PMS, not a shape clustarr
   could get right.
2. **Plex reads Kodi-shape `artist.nfo` and `album.nfo` since PMS 1.43.5.11029** (PM-6153,
   the beta forum thread of 2026-10-06: `<biography>`, `<review>`, `<releasedate>`, the
   title; Jellyfin's `<artist>` fallback promised). The talos PMS runs 1.43.5.11029.

So the only route that puts clustarr's music metadata in front of Plex today is a file beside
the music, and the invariants bound who may write it: the manager writes no file under `/data`
(ADR-0019: agents execute, the manager decides), and an agent decides nothing and writes no
Kubernetes object.

Options weighed:

| Option | Verdict |
|---|---|
| Serve `/plex/music` and `/plex/audiobooks` as custom providers now | Refused by every PMS build through 1.43.5.11079. Built to the inferred shapes behind a flag so the day a build accepts the registration is a values change (D12), never served meanwhile. |
| cluster-plex writes Plex's database rows for artists and albums, as it does markers, streams and labels | Plex's music agent and scanner own those tables and rewrite them on every refresh and analysis; a row war with the agent is exactly what the NFO route avoids. Kept for the fields NFO cannot carry (§10 of the design). |
| The manager writes the NFO files itself | Breaks ADR-0019: the manager holds no `/data` mount and must not. |
| The import agent renders and writes the NFO from the CRs it reads | The agent would decide what Plex sees from a cache read of its own, racing the manager's reconcile and duplicating the rendering in a second process. The rule since ADR-0019 is "the manager decides, the agent executes". |
| **The manager renders every byte; the import agent writes them as a dispatched task and answers in a record** | Chosen. The same split as a probe, an import execute or an artwork fetch. |

## Decision

1. **Plex's music and audiobook libraries use Plex's own agent with NFO reading** (`Prefer
   local metadata`), not a clustarr provider. The provider roots of design §7 exist behind
   `--plex-music-provider` (chart `ui.plex.musicProvider`, default false) and the chart's
   `plex.metadataProviders` carries their uris commented out with the reason; the spike of
   §7.6 is re-run against each new PMS beta, never against the talos PMS.
2. **`pkg/nfo` renders the documents from the CRs alone** -- pure functions with golden tests:
   `nfo.Artist` and `nfo.Album` (Kodi's `<artist>` and `<album>` element sets, plus the
   `<artist>`/`<albumartist>` fallbacks and a `<track>` list), `nfo.AuthorAsArtist` and
   `nfo.AudiobookAsAlbum` for an audiobook root, where the author is the artist and the
   audiobook the album. `nfo.Hash` is the sha256 of the rendered bytes and the image objects'
   digests, so the plan compares one string.
3. **The item key's `nfo` stage plans the write** (`app/remediation/nfo`, after artwork in
   `remediation.StageOrder`): when the folder is resolved, the root is a music or audiobook
   root with `naming.writeNFO` not false, and `status.nfo.hash` differs from the rendered
   hash, it publishes `schema.NFOWriteTask` (the folder, each file's name and bytes, each
   image's artwork object and digest, the file names the item owns, the hash, the root's
   file mode) on `CLUSTARR_WORK_IMPORT` through the dispatch ledger, and waits
   `nfo.RequestTimeout` (15 min) before asking again. It writes `status.nfo` (hash, path,
   files, writtenAt) under its own field manager, `catalog-nfo`, as the image-set stage owns
   `status.imageSet` under `catalog-imageset`: a stage's release of its field must touch no
   other manager's.
4. **The import agent's `import-nfo` consumer writes the bytes it was handed** and nothing
   else (`app/import/worker/nfo`; `Slots` 2, `BackOff` 1m/10m, heartbeats through a long
   write): each file through `fsops.AtomicWrite` at the root's mode (else 0664 under the
   pod's UMASK), each image copied from the `clustarr-artwork` object store it already reads,
   and **never over a file it did not write before** -- the task's `Owned` list
   (`status.nfo.files`) and the record's last answer are the only names it may replace, so an
   operator's own `folder.jpg` survives. It answers in the `clustarr-nfo` record
   (`records.Answerer`, compare-and-swap, `Superseded` checked first); a failure is a
   transient answer the stage retries after `RequestTimeout`.
5. **`RootFolder.naming.writeNFO: false` plans nothing and removes nothing.** A folder rename
   (design D7) re-plans the write at the new path, and the old files go with the recycled
   folder. Nothing deletes an NFO on purpose.
6. **The Plex side is cluster-plex's** (design §10): `type: artist` sections on the Plex Music
   Scanner with the NFO-reading agent and `Prefer local metadata`; whatever Plex's agent does
   not read from the NFO (MusicBrainz ids, narrators) is a candidate for cluster-plex's
   database seeding, as labels are for movies and TV.

## Consequences

- Plex's music metadata is Plex's agent's reading of clustarr's NFO, not clustarr's answer to
  a request: there is no match step, so a mis-scanned album in Plex is fixed by the file
  layout and the NFO, not by a provider rule, and a field the NFO cannot carry does not reach
  Plex.
- Every NFO write is a task the ledger admits and a record the loop incorporates, so the
  per-file discipline of ADR-0016 and ADR-0019 holds: the manager is the one writer of
  `status.nfo`, the agent holds no MediaFile or item RBAC for it, and a raced or duplicated
  delivery is fenced by the record's seq.
- A second write of the same bytes is a no-op by hash; a metadata refresh that changes a
  field rewrites the two small files and, only when its digest moved, the image.
- When a PMS build accepts a music provider, the roots are already there: the switch is a
  flag and the chart's uris, and this ADR's §1 is amended, not reversed -- the NFO files stay
  useful for Plex's own agent on any library not bound to the provider.
- `TestNoAgentRoleGrantsAWriteVerb`† and `TestAgentsCallNoKubernetesWriter`† (ADR-0019's
  guards, still deferred to the test batch) cover the new consumer as they cover every other.
