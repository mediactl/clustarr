# Debrid download client: Real-Debrid, `.strm` import and the link resolver

**Status:** Proposed, 2026-09-24. Not approved. Once approved it amends the
design of record (`2026-09-18-clustarr-design.md`) and amendment 1 for the parts
it covers.

**Why:** the owner's decision to take plex_debrid's behaviour into clustarr, in
place of Zurg.

**Companion:** cluster-plex's
`docs/superpowers/specs/2026-09-24-plex-strm-port-design.md`. That design takes
the `.strm` files this one writes, puts their URLs into Plex's library, and
streams from the resolver described in §6.

**Research:** a behavioural reading of
[debridmediamanager/plex_debrid](https://github.com/debridmediamanager/plex_debrid)
at `1a07b57`. Its last code change was 2023-11. It is summarised in §1, not
ported.

## 1. What plex_debrid does, and what clustarr already has

plex_debrid is a watchlist-driven loop. It polls Plex and Trakt watchlists and
Overseerr requests, scrapes torrentio, Jackett or Prowlarr, ranks the results
with ordered "version" rules, adds the winner to a debrid account, and then
relies on an **rclone or Zurg mount** of that account for Plex to see files.
clustarr already has almost all of that loop:

| plex_debrid | clustarr today | This design |
| --- | --- | --- |
| Plex and Trakt watchlists; auto-remove on success | ImportList `plex` and `trakt`, with sync levels | Unchanged |
| Overseerr requests | Not present | Non-goal (§10) |
| Scrapers: torrentio, Jackett, Prowlarr, … | indexarr: Torznab plus 749 Cardigann definitions | Unchanged. A torrentio `Indexer` client is a possible later addition |
| "Versions": triggers plus ordered requirement/preference rules | QualityProfile, custom formats, `pkg/decision` `Evaluate`/`Rank` | Unchanged, plus a cache-status signal (§4) |
| `retries <= N` fallback versions; ignore after N | Search backoff, wanted cron, blocklist | Unchanged |
| Airtime offset | DelayProfile, Series airings | Unchanged |
| Season pack versus episodes; files mapped by `SxxEyy` regex | Search packs (R-3), `spec.target.keys`, torrent file selection | File selection is reused by the debrid engine (§4) |
| Cache check through RD `instantAvailability` | — | **Replaced** by a trial add. RD removed that endpoint in 2024-11, and plex_debrid still calls it (`realdebrid.py:214`), so with its default rules it can no longer download anything |
| `addMagnet` → `selectFiles` → `info` → `unrestrict` | — | **New**: the debrid engine (§4) |
| Plex sees files through an rclone or Zurg mount | — | **New**: `.strm` files (§5), served by the link resolver (§6) |
| No repair, blacklist or cleanup | — | **New**: the resolver re-adds by hash; losses are blocklisted and searched again; an account-safe reaper (§7) |

Three plex_debrid defects are not carried over, because the design has no
equivalent code:

- The cache-status rule's weight is stored as a bool, so it never sorts.
- The `downloading()` guard is always false.
- A still-downloading torrent is reported as a cached success, with `unrestrict`
  called on the magnet.

## 2. Decisions

- **DD1. A debrid client is a `DownloadClient` with `protocol: torrent` and a
  `debrid` sub-spec.** Releases are torrents, and `pickClient`,
  `ResolveSource`, the delay and quality profiles and the ranking all key on
  `protocol`. A new Protocol value would touch every one of them for no gain.
  The CEL rule at `downloadclient_types.go:378` becomes: `torrent` requires
  exactly one of `torrent` or `debrid`; `usenet` requires `usenet`. When both a
  local torrent client and a debrid client are enabled, `priority` decides, as
  it does today (open question 2).
- **DD2. The cache check is a trial add** (§4). A release that is not cached
  fails with `notCached`, which is a release fault with a short blocklist, and
  the existing redownload path (Y3) picks the next release.
- **DD3. The debrid torrent lives exactly as long as the library file.** The
  Download of a debrid client is **not** removed on import. The reaper (§7)
  removes a torrent only once no MediaFile references it.
- **DD4. The URL in a `.strm` identifies content:
  `/strm/v1/<btih>/<fileID>`.** A repair re-adds the same hash, so no URL ever
  changes, and neither does anything downstream (Plex's database, MediaFile).
- **DD5. Only the debrid pods talk to Real-Debrid, and always over IPv4 through
  one egress IP** (§9). The resolver streams bytes itself, so no other pod,
  cluster-plex's included, ever reaches the CDN.
- **DD6. A `.strm` is a first-class *remote* media file.** It is recognised by
  the classifier, carries `MediaFileSpec.remote`, is probed over its URL, and is
  excluded from transcoding explicitly rather than by the accident of a failed
  probe.

## 3. API changes

All of these land in wave 0, serially (per the gap-fix discipline).

```go
// api/download/v1alpha1 — DownloadClientSpec gains:
Debrid *DebridSpec `json:"debrid,omitempty"`

type DebridSpec struct {
	// +kubebuilder:validation:Enum=realdebrid
	Provider  DebridProvider              `json:"provider"`
	SecretRef corev1.LocalObjectReference `json:"secretRef"` // key "token"
	// CachedOnly refuses a release the service does not already hold.
	// Pointer plus CachedOnlyOrDefault(): default true (typed-client gotcha).
	CachedOnly *bool `json:"cachedOnly,omitempty"`
	// CacheCheckTimeout is how long after selectFiles the torrent may take
	// to reach "downloaded" and still count as cached. Default 30s.
	CacheCheckTimeout *metav1.Duration `json:"cacheCheckTimeout,omitempty"`
	// DownloadTimeout bounds an uncached download when CachedOnly is false.
	// Default 24h; the failure is `timeout`.
	DownloadTimeout *metav1.Duration `json:"downloadTimeout,omitempty"`
	// StrmDir is where the engine writes .strm files for import.
	// Default /data/debrid; must be under /data (validateEngineDirs).
	StrmDir string `json:"strmDir,omitempty"`
	// RequestsPerMinute is shared by every engine and resolver replica of
	// this client (KV token bucket). Default 200; Real-Debrid's cap is 250.
	RequestsPerMinute *int32 `json:"requestsPerMinute,omitempty"`
	Resolver DebridResolverSpec `json:"resolver,omitempty"`
}

type DebridResolverSpec struct {
	Replicas     *int32           `json:"replicas,omitempty"`     // default 2
	Port         *int32           `json:"port,omitempty"`         // default 8090
	LinkCacheTTL *metav1.Duration `json:"linkCacheTTL,omitempty"` // default 1h
}
```

- **`spec.replicas`** must be 1 for a debrid client. The engine is a single
  writer; the resolver scales separately.
- **`EngineFailureReason`** gains four values:
  - `notCached`: a release fault, blocklisted for `NotCachedBlocklistTTL`
    (7 d), because a service's cache changes.
  - `remoteLost`: a release fault, blocklisted for the default 90 d.
  - `remoteError`: a release fault. Real-Debrid reported `error`, `virus`,
    `magnet_error` or `dead`.
  - `accountLimit`: a *local* fault, not blocklisted. The account's active or
    quota limit was hit; retry.

  `IsReleaseFault` and `LocalFailure` (redownload) are updated to match, and
  `blocklistedUntil` becomes `now + BlocklistTTLFor(reason)`.
- **`Item.Files[]` / `status.files[]`** gains `remote {infoHash, fileID,
  sizeBytes}`. This is in `EngineFields`.
- **`MediaFileSpec.remote *RemoteSource`** is `{provider, infoHash, fileID,
  sizeBytes, url}`. importarr owns it and it is frozen at import, like the rest
  of the spec.
- **`status.import.retiredAt`** is owned by importarr (§7).
- **A phase-derivation exception.** "Imported is sticky and first"
  (`phase.go:49`) gains one exception: a debrid Download whose
  `engineFailureReason` is `remoteLost` derives Blocklisted.

## 4. The debrid engine: grab, cache check and completion

The engine is the grabarr role `debrid-engine`. It is a one-replica Deployment
per debrid DownloadClient and follows the usenet engine's reconcile shape. For
each labelled Download:

1. **Attach or add.** It looks the hash up in the registry, the KV bucket
   `clustarr-debrid-torrents`, keyed `events.KVKeyToken(btih)` →
   `{rdTorrentID, fileIDs, downloadUID}`. If it is absent, the engine sends
   `POST /torrents/addMagnet`. A source that is only a `.torrent` goes through
   `PUT /torrents/addTorrent`, with `indexerDownload` resolved through indexarr
   exactly as today.
2. **Checks the hash.** If `info.hash` does not equal
   `spec.source.expectedInfoHash`, the result is `payloadMismatch`.
3. **Selects files.** It waits for `waiting_files_selection` (bounded), then
   chooses from `info.files[]` using the torrent engine's `spec.target.keys`
   selection, extracted to a shared `pkg/download/selection`. That means video
   files matching the target episodes, with samples and extras excluded; the
   sample floor applies here, to the real sizes. Then `POST
   /torrents/selectFiles/{id}`.
4. **Judges the cache.** It polls `GET /torrents/info/{id}` for up to
   `cacheCheckTimeout`:
   - `downloaded` means cached.
   - `error`, `virus`, `magnet_error` or `dead` give `remoteError`.
   - Otherwise, with `cachedOnly`, it sends `DELETE /torrents/delete/{id}` and
     reports **`notCached`**.
   - Without `cachedOnly`, it reports progress (bytes, speed, seeders) until
     the torrent is downloaded or `downloadTimeout` passes.
5. **Completes.** For each selected file it writes the file
   `<strmDir>/<client>/<download>/<relative path>.strm` with `fsops.AtomicWrite`,
   containing:

   ```text
   http://<client>-resolver.<ns>.svc:<port>/strm/v1/<btih>/<fileID>/<escaped basename>
   ```

   It sets `contentRoot`, `outputPath`, `files[].remote` (remote sizes) and
   `canMoveFiles=true`, and moves the stage to `done`. The phase goes to
   Completed, and the ordinary import handoff runs.

**Protocol methods with no Real-Debrid equivalent:**
- `SetSeedCriteria`, `Pause`/`Resume` of a cached item and `SetPriority` are
  no-ops, as for usenet.
- `MarkImported` is a no-op, and `removeOnImport` is ignored for a debrid
  client (DD3), reported in a condition message.

**Cost of a trial add.** Roughly 4 to 8 API calls per candidate, within the
shared 200/min bucket. A `notCached` grab costs a redownload search. That
round trip is accepted for v1. A pre-grab cache probe of the top-K approved
releases is the later optimisation: an RPC `rpc.grabarr.debrid.cached`, called
by the grab worker when the chosen client is debrid. It is what would make
`cachedOnly` behave like plex_debrid's "cached requirement" without the
blocklist round trip (open question 1).

## 5. Importing `.strm` files

- **`pkg/fsops`.** `.strm` is added as a *remote* extension of `KindVideo`. It
  classifies as `ClassMedia` and is **never** `ClassSuspectedSample`. If it were
  merely added to the video extensions, every debrid import would trip the
  50 MiB floor. `importRejected` would follow, which is a release fault, so every
  grab would be blocklisted.
- **fileimport.** A candidate whose Download `files[]` entry is `remote` takes
  its size from `remote.sizeBytes`, not from stat, and sets
  `MediaFileSpec.remote`. The destination keeps the `.strm` extension, and
  `pkg/naming` renders `Movie (Year).strm`. `release.ParsePath` already strips
  `.strm` (`pkg/release/group.go:106`). The mode is move (`canMoveFiles`).
  `EnsureFreeSpace` becomes trivial.
- **Upgrades.** Recycling the old `.strm` works unchanged. The old Download is
  then unreferenced, and the reaper retires its torrent (§7).
- **Rescan.** A `.strm` whose URL parses as this contract is attributed by path,
  like any file. Any other `.strm` goes to `status.unmatched` as
  `unknown_strm`, following the never-guess rule.

## 6. The link resolver

The resolver is the grabarr role `debrid-resolver`. Each debrid client gets a
Deployment of `resolver.replicas` and a Service `<client>-resolver`. It holds
the token and is the sole implementer of the contract. That contract text is
identical in both companion documents:

```text
GET|HEAD /strm/v1/<btih>/<fileID>[/<display-name>]     Range honoured
  200/206 bytes (default) | 302 CDN URL (?delivery=redirect only)
  404 unknown | 410 lost (X-Clustarr-Reason) | 503 transient (Retry-After)
```

**Serving a request:**

1. **Resolve.** The resolver finds the RD torrent id through the registry, then
   calls `GET /torrents/info/{id}`, cached for 5 min. It maps the fileID to its
   link: links follow the selected files in id order, which the recorded
   responses must confirm. It then calls `POST /unrestrict/link`, cached per
   link for `linkCacheTTL`. Finally it streams from the CDN with the client's
   `Range`. A CDN 403, 404 or 5xx invalidates the cached link and triggers one
   re-unrestrict.
2. **Repair.** Repair runs when the torrent is gone, reports `dead`, `error` or
   `virus`, or the unrestrict names the file unavailable or infringing:
   - It takes a KV lease on the hash (single-flight across replicas), re-adds
     the magnet by hash, selects the same fileIDs and waits
     `cacheCheckTimeout`.
   - If the torrent reaches `downloaded`, the resolver updates the registry and
     serves.
   - Otherwise it marks the registry entry lost, answers **410**, and publishes
     `work.grabarr.debrid.lost.<btih>`.
   - The resolver never writes a Download. The engine consumes that subject and
     sets `remoteLost` on the owning Downloads, which keeps the one-writer rule.
3. **Error mapping.** Real-Debrid's error codes are mapped to 410 or 503 from
   responses recorded against the live API, the way MDBList's were. They are not
   restated from documentation.
4. **Observability.** Metrics cover bytes served, unrestrict calls, repairs and
   410s. None are labelled by title or hash.

## 7. Lifecycle, loss and the reaper

**Loss.** A lost file goes through these steps:

1. The engine sets `remoteLost`, and the phase derives Blocklisted (the §3
   exception). The label goes on first, then `ActionFailed` is published, as
   today.
2. A new importarr consumer, `importarr-remote-lost`, retires the library copy.
   It recycles every path in `status.import.imported`, deletes the MediaFiles it
   created for them, and sets `status.import.retiredAt`.
3. `catalogarr-redownload` treats `remoteLost` as a release fault. For this
   reason it waits for `retiredAt` as well as the blocklist label, using the
   `awaitBlocklist` pattern, and only then publishes the search. The item is
   therefore missing, not "has a file", when the search runs.
4. The replacement imports as a new `.strm`. cluster-plex's next scan and sync
   pick it up.

**Reaper (account safety).** The existing reaper model lists **every** transfer
on the client and removes the ones it does not know. On a shared Real-Debrid
account that would delete other tools' torrents. The debrid reaper considers
only hashes in its own registry. It removes a registered torrent when either:

- its Download is gone or terminal-unimported; or
- no MediaFile has `spec.remote.infoHash` equal to the hash, for longer than
  the RootFolder's `recycleBin.cleanupDays`. That grace lets a recycled `.strm`
  be restored while its torrent still exists.

It never touches a torrent outside its registry.

**Deletion.** The Download's engine finalizer (R-6) removes the Real-Debrid
torrent only once the reaper would. A Download deleted while its MediaFile
still exists leaves the torrent, with a Warning event.

## 8. What changes downstream

- **catalogarr MediaFile controller.**
  - For a remote file, `os.Stat` checks only that the `.strm` exists
    (`FileMissing` if it is gone).
  - `ProbeHash` becomes `sha1(btih|fileID|sizeBytes)`.
  - `mediainfo.Probe` runs ffprobe over the resolver URL.
  - The container comes from the probe, not the extension. Today it would read
    `"strm"`.
  - A 503 backs off rather than requeueing every 30 s. A 410 sets
    `ProbeFailed` with reason `RemoteLost`.
- **squasharr.** Remote MediaFiles are excluded from selection explicitly
  (`transcodeprofile/profile.go:65`), since they cannot be transcoded in place.
  The `Transcoded` predicate is unaffected.
- **captionarr.**
  - Sidecars are written beside the `.strm`. `datapath.Local` accepts it, since
    it is under `/data`.
  - `mediainfo.MovieHashURL` reads the first and last 64 KiB with two range
    reads.
  - **Embedded extraction is disabled for remote files**, because it would
    demux, and so download, the whole file. Embedded tracks already known from
    the probe count as present.
  - The fetch worker compares the remote `ProbeHash`.
- **UI.** The DownloadClient form gains the debrid kind
  (`ui/forms/kinds.go:35`). The downloads page shows cache state and loss.

## 9. Wiring, RBAC and egress

- **grabarr** gains two roles, `debrid-engine` and `debrid-resolver`, with a
  `setupDebrid*` for each. Both go into the registration guard and the RBAC
  roles (`RBAC_ROLES`, the start envtest under the real role).
- **DownloadClient controller.** It renders:
  - the engine Deployment: 1 replica, Recreate;
  - the resolver Deployment and Service;
  - Secret digests that roll both when the token changes.

  Both pods are labelled `clustarr.io/egress: debrid`. The rendered pods are
  tested against what the installer creates, the same guard class as the
  existing engines.
- **Real-Debrid client:** `pkg/download/debrid/realdebrid`.
  - It **dials `tcp4` only**, so IPv6 cannot bypass the egress policy.
  - The caller injects the limiter, and every body is capped.
  - Sentinels: `ErrNotCached`, `ErrTorrentGone`, `ErrUnavailable`,
    `ErrAccountLimit`.
  - It is tested against responses recorded from the live API.
- **NATS.** The KV buckets are `clustarr-debrid-torrents` (registry) and
  `clustarr-debrid-leases`, plus the `debrid.lost` work subject. Every key goes
  through `events.KVKeyToken`. They must fit the `ForSingleNode` topology test.
- **Egress** (owner decision): one static IPv4 address from the AT&T block,
  through a Cilium egress gateway.
  - The chart renders a `CiliumEgressGatewayPolicy` only when
    `egressGateway.enabled` is set **and** the API group exists
    (`.Capabilities.APIVersions.Has`).
  - It selects `clustarr.io/egress: debrid` in the release namespace:
    destination `0.0.0.0/0`, excluding the cluster and RFC 1918 ranges, with the
    gateway `nodeSelector` and `egressIP` taken from values.
  - The kustomize overlay carries the same resource, to keep
    `TestChartAndKustomizeAgreePerComponent` green.
  - The companion's §4 lists the cluster prerequisites: Cilium settings, the IP
    bound on the gateway node's public-block VLAN, failover, and the IPv6 note.

## 10. Non-goals

- Other debrid services. The `Provider` enum leaves room for AllDebrid,
  Premiumize, Debrid-Link and Put.io.
- An Overseerr import list, and a torrentio indexer.
- A WebDAV or rclone mount.
- Downloading locally when a release is not cached. `cachedOnly: false` waits
  on Real-Debrid instead; a local torrent client at a lower priority is the way
  to have both.

## 11. Phases

| Wave | Content |
| --- | --- |
| W0 | API changes (§3), serially; `make generate manifests`; CEL tests in `pkg/crdcheck` |
| W1 | `pkg/download/debrid/realdebrid` (recorded responses), `pkg/download/selection` extraction |
| W2 | The debrid engine; import of `.strm` (`fsops`, fileimport, rescan) |
| W3 | The resolver, registry, repair and the lost path (engine, importarr consumer, redownload wait) |
| W4 | The reaper; downstream changes (MediaFile probe, squasharr exclusion, captionarr); UI |
| W5 | Wiring, RBAC, the chart and kustomize (egress policy included), the start envtest |
| W6 | e2e on kind: a Real-Debrid stub (add, select, info, unrestrict, delete, with switches for not-cached, lost and revive) plus the cluster-plex companion's resolver-contract scenario |

## 12. Testing notes that follow from CLAUDE.md's gotchas

- **Every status test acts on a Download that already has status.** This
  applies to the engine's `EngineFields` writes and to the new phase exception
  on an *Imported* Download. The release check asserts on `managedFields`.
- **Resolver and engine state writes** go through their own field managers.
  Their tests interleave a second writer (a lost update is not an SSA release).
- **The classifier fix is falsified.** The test is reverted to plain
  `MediaExtensions` membership and is expected to fail by name with
  `suspected_sample`.
- **KV keys** are contract-tested against a real embedded NATS server.
- **The reaper** is tested with foreign torrents on the stub account. It must
  leave them alone.

## 13. Open questions

1. **Cache checking.** v1 checks after the grab: `notCached`, blocklist, search
   again. Is that acceptable, or should the pre-grab probe (§4) be in v1?
2. **Mixed clients.** With a local torrent client and a debrid client both
   enabled, is `priority` enough, or should a QualityProfile be able to prefer
   one?
3. **Link lifetime.** How long a Real-Debrid unrestricted link lasts, which
   sets `linkCacheTTL`, is to be confirmed from recorded responses.
4. **Gateway failover.** The gateway node is a single point of failure for all
   debrid playback unless the static IP floats (companion §4).
