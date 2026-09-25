# TorBox: usenet through the bridge, and TorBox as an indexer

**Status:** Proposed, 2026-09-24. Not approved. It amends
`2026-09-24-debrid-bridge-torbox-design.md` (the bridge design), which amends
`2026-09-24-debrid-download-client-design.md` (the debrid design). Where this
document disagrees with either, this document wins. The owner chose the
resolver path over a WebDAV mount on 2026-09-24; §7 records why.

**Why:** the owner wants a watchlisted title playable in Plex within minutes
through TorBox, the placeholder gone once the real file has downloaded, and
TorBox usable as a downloader and an indexer for both torrents and usenet. The
two drafts above cover the torrent bridge, playback through the resolver,
retirement and Tdarr safety. This document adds what they do not: TorBox's
usenet side and TorBox's search API.

**Research:** TorBox's OpenAPI document (`https://api.torbox.app/openapi.json`,
read 2026-09-24), its Go SDK (`TorBox-App/torbox-sdk-go`), its Prowlarr
definition (`TorBox-App/torbox-prowlarr-indexers`, torrents only since
2024-11), and its help centre. Every endpoint below is to be pinned with
recorded responses, as MDBList's were; nothing is restated from documentation
into a test.

## 1. What changes from the bridge design

| Bridge design | This amendment |
| --- | --- |
| Bridge grabs cached **torrents** | Bridge grabs the best cached release across TorBox's torrent **and usenet** caches (§4) |
| Primary usenet downloads need a separate provider | TorBox subscribers get NNTP credentials from the API; the DownloadClient controller provisions them into the existing usenet engine (§3) |
| Indexers unchanged | A native `torbox` Indexer kind serves TorBox's search API for torrents and usenet (§5) |
| The resolver URL's `<btih>` is a torrent hash | The registry records a protocol per entry; the URL is content-addressed by TorBox's hash for either protocol (§6) |
| `retireWhen` default `final` | Default **`imported`** (§8, D3) |
| Every TorBox call through the egress gateway | The resolver still proxies every byte; the gateway is scoped to Real-Debrid unless TorBox's terms require one address (§8, D4) |

## 2. Decisions

- **D1. Two usenet paths, one client kind.** A `DownloadClient` with
  `protocol: usenet` and `spec.usenet.torbox` uses TorBox as its NNTP provider
  through the existing usenet engine: par2, unpack and publish stay as built,
  and files land locally. A `DownloadClient` with `protocol: torrent` and
  `spec.debrid` (the debrid design's DD1) gains **`debrid.usenet: true`**,
  under which the bridge may also add NZBs to TorBox. The debrid engine and
  resolver then speak both TorBox protocols. No third client kind.
- **D2. The indexer is native Go, not a Cardigann definition.** The Cardigann
  engine hard-codes `ProtocolTorrent` (`cardigann.go:266`) and its schema
  requires `seeders`; TorBox itself withdrew its usenet definition because
  Prowlarr cannot express one. `pkg/indexers/torbox` is one client for both
  protocols, selected by `Indexer.spec.torbox.protocol`, behind the same
  `Client` interface `ClientCache.For` already returns (`Search(ctx,
  torznab.Query) ([]torznab.Release, error)`).
- **D3. Retirement defaults to `imported`.** The bridge exists to cover the
  gap before the local file is playable, and it is gone as soon as that file
  is imported. `final` remains for an owner whose Tdarr output must be the
  version Plex prefers.
- **D4. The resolver proxies every byte for TorBox, gateway or not.** That is
  what keeps API keys, CDN links and loss handling inside the cluster. Whether
  TorBox also needs one static address is a terms question (open question 1);
  until answered, the debrid pods keep the `clustarr.io/egress: debrid` label
  and the gateway applies to them when it is enabled.
- **D5. No cached-flag in `ReleaseInfo` for v1.** TorBox's search results carry
  a cached marker, but `ReleaseInfo` has no attribute map and `IndexerFlags` is
  an enum. The bridge's `checkcached` batch already answers the question for
  every release in one call, so the flag would only save that call. Deferred.

## 3. TorBox as an NNTP provider (primary usenet)

`UsenetSpec` gains:

```go
// Torbox provisions this client's NNTP provider from a TorBox account.
// Exactly one of providers or torbox is set (CEL).
Torbox *TorboxProviderSpec `json:"torbox,omitempty"`

type TorboxProviderSpec struct {
	SecretRef   corev1.LocalObjectReference `json:"secretRef"`          // key "apikey"
	Connections int32                       `json:"connections,omitempty"` // default 8
}
```

The DownloadClient controller, on reconcile, calls
`GET /v1/api/usenet/provider/connection` and `GET /v1/api/usenet/provider/account`
with the key, and writes an owned Secret `<client>-torbox-nntp` with the keys
`username` and `password` the usenet engine already reads
(`app/grab/engine/usenet/config.go:125`), plus host and port into a rendered
`NNTPProvider` named `torbox`. The engine sees an ordinary provider. The
controller re-reads the account every 24 h and on a Secret change (the digest
that already rolls the engine), and sets `Ready=False, ProviderUnavailable` with
the API's message when the account has no usenet entitlement. The controller
is the only caller of these two endpoints.

## 4. Usenet through the bridge

The debrid engine (debrid design §4, bridge design §2) gains the usenet calls,
mirroring the torrent ones:

| Step | Torrent | Usenet |
| --- | --- | --- |
| Cache check | `GET /v1/api/torrents/checkcached?hash=&format=object&list_files=true` | `GET /v1/api/usenet/checkcached?hash=&format=object&list_files=true` |
| Add | `POST /v1/api/torrents/createtorrent` (`magnet` or `file`, `name`, `allow_zip=false`, `add_only_if_cached`) | `POST /v1/api/usenet/createusenetdownload` (`link` or `file`, `name`, `password`, `post_processing`, `add_only_if_cached`) |
| Files | `GET /v1/api/torrents/mylist?id=&bypass_cache=true` | `GET /v1/api/usenet/mylist?id=&bypass_cache=true` |
| Link | `GET /v1/api/torrents/requestdl?token=&torrent_id=&file_id=` | `GET /v1/api/usenet/requestdl?token=&usenet_id=&file_id=` |
| Delete | `POST /v1/api/torrents/controltorrent` `{torrent_id, operation: delete}` | `POST /v1/api/usenet/controlusenetdownload` `{usenet_id, operation: delete}` |

- **The hash.** TorBox keys its usenet cache by a hash of the NZB it computes
  itself; the search API returns it with each usenet result, and `mylist`
  returns it for an added item. A Download grabbed from a TorBox usenet result
  carries that hash in `spec.release.infoHash`, which is allowed to be empty
  today for usenet and is the field the bridge's `checkcached` batches on. A
  usenet release from any other indexer has no TorBox hash before it is added;
  the bridge does not consider it (a `.nzb` from nzbgeek is added by the
  primary client, not the bridge), which keeps the bridge to one cache call
  and no uncached adds.
- **`add_only_if_cached`** is sent on every bridge add, torrent and usenet, so
  a cache that changed between the check and the add fails as `notCached`
  rather than starting a 60-per-hour uncached download.
- **`post_processing`** is TorBox's own unpack. The bridge sends the value that
  yields playable files without a zip (to be read from the recorded
  responses); the resolver serves the resulting file ids.
- **Selection** of which files get a `.strm` is the same episode and feature
  matching as for torrents (`pkg/download/selection`).
- **Grab-time choice.** The bridge design's §3 step 4 becomes: take the
  approved releases under the bridge profile in rank order, split by protocol,
  one `checkcached` per protocol, then grab the highest-ranked cached release
  of either protocol. Rank order across protocols is `pkg/decision`'s, which
  already carries `PreferredProtocol`.
- **Rate limits.** One shared 250/min KV bucket per API key across the
  DownloadClient controller, the engine and the resolver replicas; the
  50-per-hour bucket for uncached adds applies only to `mode: primary` with
  `cachedOnly: false`, for either protocol.

## 5. TorBox as an indexer

```go
// api/index/v1alpha1 — IndexerSpec gains a fourth, mutually exclusive kind:
Torbox *TorboxSearch `json:"torbox,omitempty"`

type TorboxSearch struct {
	// +kubebuilder:validation:Enum=torrent;usenet
	Protocol commonv1.Protocol `json:"protocol"`
	// SearchUserEngines asks TorBox to run the account's own engines too.
	SearchUserEngines bool `json:"searchUserEngines,omitempty"`
}
```

- `spec.secretRef` carries the key `apikey`, as for other kinds; `spec.baseURL`
  is the search API's base and defaults to TorBox's. Its host must be confirmed
  live under the owner's key before W1 (open question 2).
- **`pkg/indexers/torbox`** implements `Search` for `imdbid` (movies and
  series, with `season` and `episode`), and text `q`; it reports caps of exactly
  those modes, so `BuildSearchRequest`'s text fallback applies as for any
  indexer. It maps a result to `torznab.Release`: `raw_title` → Title, `size`,
  `age` → PubDate, `last_known_seeders` → Seeders, `magnet` → MagnetURL or
  `nzb` → Link, `hash` → InfoHash for both protocols, categories from the
  season/episode shape as TorBox's own definition does. Every body is read
  through a cap; the caller injects the limiter; errors expose sentinels
  (`ErrUnauthorized`, `ErrRateLimited`). Recorded responses are the fixtures.
- **Download.** A usenet result's `nzb` link is fetched by indexarr's
  `download` verb through the same client with the bearer header, and the
  bytes go out as `application/x-nzb` in `schema.DownloadResponse`, exactly as
  for a Newznab indexer. A torrent result is a magnet.
- **Two Indexer objects** in practice, `torbox-torrents` and `torbox-usenet`,
  each with one protocol, so the fan-out's per-indexer dedupe, query window and
  backoff apply per protocol. The UI's Indexer form gains the kind.
- **Cardigann is not touched.** TorBox's bundled torrent definition is left
  out of the bundle, so an operator has one TorBox indexer kind, not two.

## 6. Registry and resolver

The registry bucket `clustarr-debrid-torrents` becomes `clustarr-debrid-items`,
keyed `events.KVKeyToken(hash)`, with the value
`{provider, protocol, remoteID, fileIDs, downloadUID}`. The resolver contract
`GET /strm/v1/<hash>/<fileID>` is unchanged; `<hash>` is "the debrid service's
content hash for the item: the info hash of a torrent, TorBox's NZB hash for a
usenet item". The resolver picks `torrents/requestdl` or `usenet/requestdl`
from the entry's protocol. Repair re-adds by hash for a torrent (a magnet from
the hash) and by the stored NZB for a usenet item, which the engine keeps in the
job directory for that purpose, so a usenet repair needs no indexer. The
reaper's rule is unchanged: registered items only, never a foreign one.

## 7. Why not WebDAV

TorBox's WebDAV (`https://webdav.torbox.app`, Basic auth, read-only, rebuilt
every 15 minutes) would replace the resolver with an rclone FUSE mount and
symlinks. It was declined on 2026-09-24 because it needs a privileged mount pod
per node whose death takes playback with it; because every probe, scan and
seek becomes API traffic under the 300/min limit and, per warpbox's authors,
reads as access that resets TorBox's retention timer; and because loss shows up
as a read error in Plex rather than as a `410` clustarr can act on. The bridge
Download, symlink-or-strm placement and retirement would have been the same
code either way, so nothing is lost by choosing the resolver first.

## 8. Phases

| Wave | Content |
| --- | --- |
| T0 | API: `UsenetSpec.torbox`, `DebridSpec.usenet`, `IndexerSpec.torbox`, registry value shape; `make generate manifests`; CEL tests in `pkg/crdcheck` |
| T1 | `pkg/download/debrid/torbox` (both protocols, recorded responses); `pkg/indexers/torbox` (recorded responses); the NNTP provisioning in the DownloadClient controller |
| T2 | The debrid engine's usenet path and the grab worker's two-cache choice (bridge design B2-B3 with usenet) |
| T3 | Resolver and registry protocol switch, usenet repair from the stored NZB, reaper |
| T4 | Indexer controller kind, download verb, UI forms for both kinds |
| T5 | e2e on kind: a TorBox stub with torrent and usenet caches, `provider/account`, and the search API; scenario: watchlist stub → cached usenet bridge → playable `.strm` → primary import through the TorBox NNTP provider stub → retirement |

The bridge design's B0 (the Tdarr marker) stays the gate for re-enabling
downloads and is unchanged by this document.

## 9. Testing notes that follow from CLAUDE.md's gotchas

- The provisioned NNTP Secret is a second writer's data on the DownloadClient's
  workload: its digest must roll the engine like a provider Secret does, and the
  test seeds a client that already has a rendered provider.
- Every status write here is a complete declaration under its manager; the
  Indexer's `status.caps` test asserts every leaf.
- The registry key is contract-tested against a real embedded NATS server,
  since a TorBox usenet hash is not guaranteed to be hex.
- API keys never appear in error strings, `.strm` files, status or logs; the
  `requestdl` URL that carries the key exists only inside the resolver.
- Recorded responses, never restated documentation, are the fixtures for both
  new clients.

## 10. Open questions

1. **TorBox's terms on client addresses.** If several CDN client addresses are
   tolerated, the egress gateway is scoped to Real-Debrid only.
2. **The search host.** `search-api.torbox.app` did not resolve from the
   development machine on 2026-09-24 while `api.torbox.app` did. Confirm the
   host under the owner's key before T1.
3. **`post_processing` value** for playable usenet output, from a recorded
   response.
4. **Series scope for the bridge** (bridge design open question 3) stands.
