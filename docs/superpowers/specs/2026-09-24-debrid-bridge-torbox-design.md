# TorBox: indexer, downloader and instant-play bridge, for torrents and usenet

**Status:** Proposed, 2026-09-24 (revision 2). Not approved. It amends
`2026-09-24-debrid-download-client-design.md` (the debrid design). Where the two
disagree, this document wins.

**Why:** the owner wants three things from TorBox:

- a watchlisted title playable in Plex within minutes, from a debrid copy (the
  **bridge**), until the real file has downloaded;
- TorBox as a **downloader**, for both torrent and usenet;
- TorBox as an **indexer**, for both torrent and usenet.

Transcoding is clustarr's own (squasharr). Tdarr is **not** used; revision 1's
external-transcoder section is withdrawn.

**Live-library pause.** `kind-cluster-plex` is attached to the owner's live Plex
library. Since 2026-09-24, at the owner's request, the `frugal` DownloadClient
is disabled and every active Download is paused while this work is developed
(§8).

## 1. Three roles, one account

| Role | clustarr object | What TorBox does |
| --- | --- | --- |
| **Indexer** | `Indexer` with `spec.torbox`, one per protocol | Searches TorBox's search API, which reports whether each result is cached (§2) |
| **Downloader** | `DownloadClient` with `debrid.provider: torbox, mode: download`, one per protocol | TorBox fetches the release (instantly when cached); clustarr copies the files from TorBox's CDN to `/data` and imports them as ordinary local media (§4) |
| **Bridge** | `DownloadClient` with `debrid.provider: torbox, mode: bridge` (torrent) | clustarr writes `.strm` files served by the resolver (§5); Plex plays them at once; they retire when the real file is final (§6) |

- **One client per protocol.** A DownloadClient and an Indexer each carry
  exactly one `protocol`, and client selection is by protocol equality
  (`pickClient`). So TorBox appears as one object **per protocol**, not one
  object for both. That keeps the existing enums and the CEL rules intact.
- **Shared account.** Every TorBox object names the same Secret (key `apiKey`).
  They share one rate budget, a KV token bucket keyed by a hash of the key,
  because TorBox counts limits per API key across its servers.
- **The debrid design's modes, renamed.** Its "primary" mode (a permanent
  `.strm`) becomes `mode: stream`. The modes are `download | bridge | stream`.

## 2. TorBox as an indexer (`app/indexer/clients/torbox`)

TorBox's search API lives at `https://search-api.torbox.app`, with
`Authorization: Bearer <key>`. Every request carries **`check_cache=true`**.

| Search | Path |
| --- | --- |
| Movie by id | `/torrents/imdb:<tt>`, `/usenet/imdb:<tt>` |
| Episode by id | `…/imdb:<tt>?season=<s>&episode=<e>` |
| Text fallback (`SearchRequest.Text`) | `/torrents/search/<query>`, `/usenet/search/<query>` |

- **Where it plugs in.** A `torbox` Indexer is one more `Client` behind
  `ClientCache.For`, as Cardigann was (ruling R5). It inherits the fan-out's
  dedupe, the query-limit window and the health/backoff ladder.
- **API.** `IndexerSpec` gains `torbox *TorBoxIndexer{protocol}`, as a
  third mutually exclusive source beside `definition`/`definitionRef` and
  `generic`. It also gains `cachedOnly *bool` (default false), which maps to the
  API's `cached_only` parameter.
- **Id-first searching.** The client advertises IMDb-id search for movies and
  series in its caps. `BuildSearchRequest` already prefers ids and uses text
  only for an indexer with no id support.
- **Result mapping.** Results map to `ReleaseInfo`:
  - torrents: `raw_title`, `size`, `magnet`/hash, `last_known_seeders`, `age`;
  - usenet: the NZB link and size.

  Each release carries `ReleaseInfo.cachedOn: ["torbox"]` when `cached` is true.
  This is a new list field on the shared type, so both the ranking (§3) and the
  bridge grab can use it.
- **Unverified shapes.** Some field names come from a third-party Cardigann
  definition and TorBox's changelog. The usenet result fields in particular are
  unverified. The client is built against **responses recorded from the live
  API** (the MDBList rule), including the "alternative hashes" TorBox's
  changelog says it returns.

## 3. Cached as a ranking input

`pkg/decision` gains one signal, `Cached(release, service)`:

- it is true when `cachedOn` names the service; or
- after the grab worker's **batch `checkcached`**, for releases from other
  indexers (§4).

How the signal is used:

- **Bridge and `cachedOnly` clients** use it as a *requirement*: nothing
  uncached is grabbed.
- **Download-mode clients** use it as a *preference*, after quality and custom
  format score but before seeders and size. A cached copy of the chosen quality
  arrives in minutes; an uncached one arrives in hours, subject to the 60/hour
  cap on uncached adds.

The item's QualityProfile is unchanged. The bridge ranks with its own profile
(§5).

## 4. TorBox as a downloader (`mode: download`)

This is the grabarr role `debrid-engine`, from the debrid design. A TorBox
client in download mode fetches real files to `/data`, so everything downstream
(import, probe, squasharr, captionarr) sees ordinary local media. Nothing in
that path is new.

**The flow, per Download:**

1. **Cache check before the grab.** With a TorBox client in play, the grab
   worker calls `checkcached` over every approved hash in **one** call:
   - `GET /v1/api/torrents/checkcached?hash=…&format=object&list_files=true`;
   - `GET /v1/api/usenet/checkcached`, which hashes the **MD5 of the NZB or its
     link**.

   It records the result against `Cached` (§3).
2. **Add.**
   - **Torrent:** `POST /v1/api/torrents/createtorrent`, with `magnet`, or
     `file` for a `.torrent`, and `name`, `allow_zip=false`, and `seed=3`
     meaning do not seed; TorBox does the seeding.
   - **Usenet:** `POST /v1/api/usenet/createusenetdownload`, with `link` when the
     release came from the TorBox indexer, or otherwise `file` (the NZB fetched
     through `rpc.indexarr.download`, as today). It sends `post_processing=-1`,
     so TorBox repairs and unpacks. That makes par2 and unrar TorBox's problem,
     not the engine's.
3. **Wait.** It polls `mylist?id=` (with `bypass_cache=true`) and maps TorBox's
   state onto `download.Item`: queued, downloading with bytes and speed, then
   finished. The state strings go into the Go code from recorded responses.
   Failures map onto the existing reasons `stalled`, `timeout` and
   `missingArticles`, plus the debrid design's `remoteError` and
   `accountLimit`.
4. **Fetch.**
   - For each selected file (by `spec.target.keys`, with the shared selection)
     it calls `requestdl?…&redirect=false` for the CDN URL and GETs it with
     `Range` into `/data/torbox/incomplete/<download>/<path>.part`.
   - A restart resumes from the part's current length (ADR-0014's working area
     on the shared volume).
   - Each file's length is checked against TorBox's reported size, then the file
     is renamed into `/data/torbox/complete/<download>/`, and the stage goes to
     `done`.
   - `status.files`, `contentRoot` and `canMoveFiles=true` are set as for the
     usenet engine.
5. **Import.** This is the ordinary fileimport path, with real files.
6. **Release.** After import, `removeOnImport` (default true) deletes the TorBox
   item (`controltorrent` / `controlusenetdownload`, `operation: delete`). The
   exception is when a bridge still references the same hash (§6); that is a
   registry reference count.

**Throughput.** One download is one or more HTTPS streams from TorBox's CDN
through the egress gateway, with no local peers or NNTP connections. Segmented
parallel ranges (`DebridSpec.download.segments`, default 4) are a knob, not
required for v1.

**Rate budget.** Every TorBox call from every role draws on one shared bucket
(250/min default under TorBox's 300/min per key). Uncached `createtorrent` calls
draw on a separate 50/hour bucket under TorBox's 60/hour. The engine does not
retry against either bucket; it requeues after the bucket's reset time.

## 5. The bridge (`mode: bridge`)

The flow and the budget, about 2-3 minutes worst case:

| Step | Owner | Time |
| --- | --- | --- |
| 1. A watchlist add is noticed by the delta poll (below) | importarr ImportList `plex` | ≤ 60 s |
| 2. The item is created; `searchOnAdd` publishes a search | importarr, catalogarr | seconds |
| 3. Search, including the TorBox indexer; releases evaluated under the bridge profile | indexarr, catalogarr | 10-30 s |
| 4. The best **cached** release is grabbed onto the bridge client | catalogarr grab worker | < 2 s |
| 5. `createtorrent` → `mylist` → a `.strm` per selected file | debrid engine | ~5 s |
| 6. The `.strm` is imported (`MediaFile.spec.remote`, `bridge: true`) | importarr | seconds |
| 7. The folder is refreshed; fast strm sync (inject, probe, flush) | cluster-plex (§7) | 10-40 s |

The primary grab (step 4's twin, onto a download-mode client) runs alongside it.

**The delta poll.**

- **Today:** the `plex` provider's `refreshInterval` is clamped to at least 6 h
  (`importlist_types.go:283`), and the cluster has no ImportList.
- **New:** `ImportList.spec.fastPoll` (default off, floor 30 s). While it is
  set, importarr fetches only the first page of the Discover watchlist, which
  is already sorted `watchlistedAt:desc` (`pkg/importlist/plex/watchlist.go:137`),
  and stops at the first item it already knows.
- **The full sync** stays on its 6 h interval and still owns removals.
- **Alternative:** Plex's watchlist RSS feed, if the poll turns out to be
  throttled.

**The bridge profile.** `DebridSpec.bridge.qualityProfileRef` defaults to a new
built-in, `bridge-directplay`. It prefers 1080p WEB, H.264 or HEVC, and AAC,
AC-3 or E-AC-3, and it rejects TrueHD, DTS-HD and Dolby Vision without a
fallback. The stand-in is chosen to **direct-play**, not to be the best copy. On
top of that, `Cached` is a requirement.

**Two Downloads per item.**

- **Role.** `Download.spec.role: primary | bridge` is immutable, and the grab
  worker sets it from the client's mode.
- **Guards and leases.** The double-grab guard (`downloads.ResolveSource`) and
  the grab leases count **per role**.
- **Rollups.** `status.activeDownloadRef` stays the primary's. A new
  `status.bridge {downloadRef, mediaFileRef, since}` has one writer, the item's
  reconciler.
- **Wanted and cutoff.** A bridge MediaFile makes an item playable but does not
  give it a file. Wanted, cutoff and `Transcoded` are judged on non-bridge
  MediaFiles only.

**One TorBox item for both roles.** When the bridge and the primary pick the
same release, both Downloads resolve to one TorBox item through the registry
(keyed by hash). The download-mode engine copies the files while the resolver
streams them. There is one `createtorrent`, and the TorBox item is deleted only
when the reference count reaches zero.

## 6. Bridge retirement

`DebridSpec.bridge.retireWhen` is one of:

- **`final`** (default). The primary MediaFile satisfies `rollup.Transcoded`,
  meaning squasharr has swapped in its output (`CLUSTARR_PROFILE` tag or
  `spec.original=false`). If **no enabled TranscodeProfile selects** that
  MediaFile, `final` holds as soon as it is imported and probed, since there is
  nothing to wait for. Selection is judged by the same
  `transcodeprofile.Selects` the profile controller uses, called from the item
  reconciler.
- **`imported`.** As soon as the primary is imported.
- **`never`.**

`bridge.maxAge` (default 30 d) retires a bridge that has had a primary for that
long.

Retirement belongs to importarr, which placed the `.strm`:

1. It recycles the `.strm`.
2. It deletes the bridge MediaFile.
3. It calls library refresh for the folder, so Plex drops the version.
4. The reaper deletes the TorBox item once nothing references its hash.

Until then Plex lists two versions of the title, which is intended.

## 7. Telling Plex, through cluster-plex

importarr gains `--library-refresh-url`. After an import or a retirement it
sends `POST {"paths": [...]}`. The call is best-effort, retried with backoff,
and never fatal.

cluster-plex serves it as `POST /api/v1/library/refresh`. It:

1. maps each path to a section through `section_locations`;
2. sends `refresh?path=` to the pod that owns the section;
3. for debrid libraries, runs a fast strm sync of just the new `.strm` parts:
   it waits up to 60 s for the parts to appear, then injects, probes and
   flushes.

The endpoint is an addition to cluster-plex's strm port design, §9 item 4.
clustarr holds no Plex token.

## 8. The live-library pause, and what re-enabling means

State as of 2026-09-24 on `kind-cluster-plex`:

- the `frugal` usenet DownloadClient has `spec.enabled=false`;
- all 63 active Downloads have `spec.paused=true`;
- no TranscodeProfile or TranscodeJob exists.

Most of those Downloads are **upgrades of movies already in the library**: 257
Movies read `CutoffUnmet`, and every profile has `upgradeAllowed: true`.
Re-enabling downloads resumes them, and each import recycles the existing
library file. That is ordinary *arr behaviour, but it writes to the owner's live
library, so it is the owner's call.

One known obstacle remains. The library is owned by uid 1024, gid 100, with
`755` folders. clustarr runs as 1000:1000, hard-coded in the engine and Job
builders (`workload.go:72-73`), so imports into existing item folders fail. A
configurable pod identity (1024:100) fixes it.

Development of this design needs none of that. The TorBox indexer, the bridge
and a TorBox download client can be exercised against a **disposable
RootFolder** outside the live library (the transcode test tree's pattern), with
the live client left disabled.

## 9. Phases

| Wave | Content |
| --- | --- |
| T0 | API, serially. Fields: `DebridSpec.provider` gains `torbox`, `mode: download\|bridge\|stream`, `bridge`, `download`; `Download.spec.role`; item `status.bridge`; `ImportList.spec.fastPoll`; `IndexerSpec.torbox` and `cachedOnly`; `ReleaseInfo.cachedOn`. Also CEL (`debrid` on either protocol; `mode: bridge` or `stream` only with `protocol: torrent`) and the typed-client default sweep |
| T1 | `pkg/download/debrid/torbox` (torrents and usenet) and `app/indexer/clients/torbox`, both built on recorded responses |
| T2 | Download mode: the engine, fetch/resume/verify, the shared rate buckets, `removeOnImport`, reference counting |
| T3 | The `Cached` signal, batch `checkcached` in the grab worker, the per-role guard and leases |
| T4 | Bridge: `.strm` import, the resolver for TorBox (`requestdl` behind the resolver contract), the retirement rule, the library-refresh call and cluster-plex's endpoint |
| T5 | Fast watchlist poll, `bridge-directplay`, e2e on kind: a TorBox stub (search, checkcached, create, mylist, requestdl, delete), then watchlist → `.strm` playable → primary download → squasharr → retirement |

## 10. Open questions

1. **Series.** Should the bridge grab a season pack, or only the aired episodes
   of a watchlisted show? A pack is one `createtorrent`.
2. **Usenet bridge.** TorBox caches usenet too, so `mode: bridge` could accept
   `protocol: usenet`. It is left out of v1 because the resolver contract keys
   on a torrent hash. Adding it means a `/strm/v1/nzb/<md5>/<fileID>` form.
3. **Client IPs.** Do TorBox's terms tolerate several CDN client IPs? If so, the
   single-IP egress gateway could be scoped to Real-Debrid only.
4. **Real-Debrid.** Is Real-Debrid still wanted at all, now that TorBox covers
   every role? If not, it drops to a later provider behind the same `Provider`
   enum.
