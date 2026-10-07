# Live TV: an IPTV proxy for Plex, after xTeVe

Status: design, for the owner's approval (2026-10-07).

**Owner rulings (2026-10-07):**
- **No new service:** the tuner and its controller both run inside the
  unified manager (`cmd/manager`, branch `unify-manager-agent`), with no
  Deployment or service of their own.
- **The group:** `IPTVProvider` belongs to the `clustarr.io` group.
- **The stream format:** the provider serves MPEG-TS, so HLS is deferred.
- **No channel cap:** 480 is the cap of Plex's setup wizard, not of PMS.
  cluster-plex registers through PMS's API, so a provider carries any
  number of active channels (§3.1). This follows iptvtunerr's patterns.
  - **What it changes:** the mappings move out of `IPTVProvider` into a
    kind of their own, `IPTVChannel`, one object per mapped channel. One
    object could not hold thousands.
- **cluster-plex adds each provider to Plex:** it registers the device and
  creates the DVR through PMS's API (§6.1). No address is entered by hand.
- **The NetworkPolicy ships on.** clustarr and cluster-plex are being
  combined into one namespace. So the policy selects Plex's pods by label,
  not by namespace (§5.3).

The sections below are written to these rulings.

## 1. What the owner asked, and what this design assumes

**Asked:**

- A simple, efficient IPTV proxy that routes channels to Plex, modelled on
  xTeVe.
- Configured as a CRD shaped like Indexer, DownloadClient and
  MetadataProvider, holding:
  - the number of tuners;
  - the provider's M3U URL;
  - the EPG source, defaulting to XEPG;
  - XMLTV guides ("Add EPG URL");
  - filters;
  - mappings;
  - active channels.
- Validation that Plex sees no more than 480 live channels, Plex's limit.
  **Superseded:** the owner removed the cap (§3.1).
- Every setting manageable from the UI, on the Settings page.
- A new **Live TV** page that does what xTeVe's tables do: filtering,
  mapping and channel selection.

**Settled:**

- **One provider, one device:** each provider is one emulated HDHomeRun
  device and one Plex DVR.
- **No channel cap:** the owner's ruling (2026-10-07). §3.1 says why it
  holds and what remains to be measured.
- **Plex's job:** Plex records and transcodes. Clustarr only relays streams
  and serves the guide.
- **Network:** Plex reaches the proxy inside the cluster. Plex and clustarr
  share one namespace.
- **Where it runs:** the manager is one replica, and its runnables run on
  the leader, so the leader holds every provider's upstream connections. No
  two pods ever hold a provider's tuners at once, even during a rollout.

## 2. How xTeVe works, and what we keep

xTeVe pretends to be an HDHomeRun network tuner. Plex's DVR setup talks to
it over HTTP:

| Endpoint | Answer |
|---|---|
| `GET /discover.json` | The device: `FriendlyName`, `ModelNumber`, `FirmwareName`, `TunerCount`, `DeviceID`, `DeviceAuth`, `BaseURL`, `LineupURL` |
| `GET /lineup_status.json` | `{"ScanInProgress":0,"ScanPossible":1,"Source":"Cable","SourceList":["Cable"]}` |
| `GET /lineup.json` | `[{"GuideNumber","GuideName","URL"}]`, one entry per active channel |
| `GET /device.xml` | The UPnP description (Plex reads it in its discovery path) |
| `POST /lineup.post` | A scan request, answered with an empty 200 |
| `GET <URL from lineup>` | The channel's MPEG-TS stream |

Its model has four parts:

- **Playlists:** M3U sources, each with a tuner count, which is the
  provider's connection limit.
- **XMLTV files:** EPG sources.
- **Filters:** group-title filters or custom name filters, with include and
  exclude words. Only filtered channels reach the mapping table.
- **Mapping (XEPG):** each filtered channel has an active flag, a channel
  number, a name, a logo, a group, an XMLTV source and channel (or the
  "xTeVe Dummy" guide), and a timeshift.

**The EPG source:**
- `XEPG`: xTeVe serves its own XMLTV guide.
- `PMS`: Plex uses its own lineup guide, and xTeVe passes the numbers
  through.

**Buffering:** xTeVe offers none, its own HTTP buffer, ffmpeg or VLC.

**What we keep:**
- the HDHomeRun surface;
- the four-part model;
- XEPG and PMS;
- a pure-Go HTTP buffer that shares one upstream between viewers.

**What we drop:**
- the ffmpeg and VLC buffers, since clustarr execs no media tools;
- xTeVe's users and authentication, since Plex sends none;
- SSDP discovery, since multicast does not cross the pod network and the
  device is added by address;
- the web-socket configuration API. The CRD is the configuration.

## 3. The resources: `IPTVProvider` and `IPTVChannel`

Both are the first kinds of the `clustarr.io/v1alpha1` group, in
`api/clustarr/v1alpha1`:

- **`IPTVProvider`** (short name `iptv`) holds the provider's settings. Its
  controller gives it one Service, its address in Plex. Its tuner is a
  runnable of the manager (§5), not a pod of its own.
- **`IPTVChannel`** (short name `iptvch`) holds one mapped channel: a
  candidate the owner activated or customised. Its full model is in §3.0.
  - **Why a kind of its own:** with no cap, the mappings cannot be a list in
    the provider. etcd caps an object at about 1.5 MiB, which a few
    thousand mappings with logo URLs would exceed.
  - **What else it buys:** one object per channel also means a table edit
    writes one small object, never a whole list that two browser tabs could
    overwrite. And `kubectl get iptvch` is xTeVe's mapping table.

```go
type IPTVProviderSpec struct {
	// Enabled false stops this provider's tuner; Plex then sees the device gone.
	Enabled *bool `json:"enabled,omitempty"`

	// Playlist is the provider's M3U.
	Playlist PlaylistSource `json:"playlist"`

	// Tuners is how many distinct channels may stream at once: the
	// provider's connection limit, and discover.json's TunerCount.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=64
	Tuners int32 `json:"tuners"`

	// EPGSource is where Plex reads the guide: XEPG (this provider's
	// /xmltv.xml, built from Guides and the channel mappings) or PMS
	// (Plex's own lineup guide; mappings' epg is ignored). Default XEPG.
	// +kubebuilder:validation:Enum=XEPG;PMS
	EPGSource EPGSource `json:"epgSource,omitempty"`

	// Guides are the XMLTV sources ("Add EPG URL").
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Guides []XMLTVSource `json:"guides,omitempty"`

	// Filters choose which playlist entries reach the mapping table. An
	// entry is a candidate when any enabled filter matches it; with no
	// filter, nothing is a candidate (xTeVe's rule).
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Filters []ChannelFilter `json:"filters,omitempty"`

	// ChannelNumberStart is where the UI's auto-numbering begins. Default 1000.
	ChannelNumberStart *int32 `json:"channelNumberStart,omitempty"`

	// Device names the emulated HDHomeRun.
	Device DeviceSpec `json:"device,omitempty"`

	// Stream tunes the relay.
	Stream StreamSpec `json:"stream,omitempty"`
}

type PlaylistSource struct {
	// URL, or URLFrom when the URL carries credentials (an Xtream Codes
	// get.php?username=…&password=…). Exactly one (CEL).
	URL     string                    `json:"url,omitempty"`
	URLFrom *corev1.SecretKeySelector `json:"urlFrom,omitempty"`
	// UserAgent for playlist, guide and stream requests. Default "clustarr".
	UserAgent string `json:"userAgent,omitempty"`
	// Refresh is how often the playlist is fetched again. Default 24h.
	Refresh *metav1.Duration `json:"refresh,omitempty"`
}

type XMLTVSource struct {
	Name    string                    `json:"name"`
	URL     string                    `json:"url,omitempty"`
	URLFrom *corev1.SecretKeySelector `json:"urlFrom,omitempty"`
	Refresh *metav1.Duration          `json:"refresh,omitempty"` // default 12h
}

type ChannelFilter struct {
	Name string `json:"name"`
	// Type groupTitle matches an entry's group-title exactly; custom
	// matches Match as a substring of the channel name.
	// +kubebuilder:validation:Enum=groupTitle;custom
	Type  FilterType `json:"type"`
	Match string     `json:"match"`
	// Include: the name must contain at least one; Exclude: none.
	// +kubebuilder:validation:MaxItems=32
	Include []string `json:"include,omitempty"`
	// +kubebuilder:validation:MaxItems=32
	Exclude       []string `json:"exclude,omitempty"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
	Enabled       *bool    `json:"enabled,omitempty"`
}

type IPTVChannelSpec struct {
	// ProviderRef names the IPTVProvider in the same namespace. Immutable (CEL).
	ProviderRef string `json:"providerRef"`
	// Key is the playlist entry's identity (pkg/iptv.EntryKey, 4.1). Immutable (CEL).
	// +kubebuilder:validation:MaxLength=64
	Key string `json:"key"`
	// Active puts the channel in the lineup. An inactive IPTVChannel keeps
	// its mapping for later.
	Active bool `json:"active,omitempty"`
	// Number is the GuideNumber Plex shows: "1001" or "5.1".
	// +kubebuilder:validation:Pattern=`^[0-9]{1,5}(\.[0-9]{1,3})?$`
	Number string `json:"number"`
	// Name, Group and Logo override the playlist's.
	Name  string `json:"name,omitempty"`
	Group string `json:"group,omitempty"`
	Logo  string `json:"logo,omitempty"`
	// EPG is the guide mapping; unset means by tvg-id, else dummy (4.3).
	EPG *ChannelEPG `json:"epg,omitempty"`
	// TimeshiftMinutes moves this channel's programmes.
	// +kubebuilder:validation:Minimum=-720
	// +kubebuilder:validation:Maximum=720
	TimeshiftMinutes int32 `json:"timeshiftMinutes,omitempty"`
}

type ChannelEPG struct {
	// Guide names one of spec.guides and ChannelID an XMLTV channel id in
	// it; or Dummy draws DummyMinutes blocks titled by the channel's name.
	// Exactly one of Guide and Dummy (CEL).
	Guide        string `json:"guide,omitempty"`
	ChannelID    string `json:"channelID,omitempty"`
	Dummy        bool   `json:"dummy,omitempty"`
	DummyMinutes *int32 `json:"dummyMinutes,omitempty"` // 30, 60, 120, 180; default 60
}

type DeviceSpec struct {
	FriendlyName string `json:"friendlyName,omitempty"` // default "Clustarr <name>"
}

type StreamSpec struct {
	// Buffer is each channel's ring buffer. Default 8Mi.
	Buffer *resource.Quantity `json:"buffer,omitempty"`
	// Linger keeps an upstream open after its last viewer leaves, so a
	// channel flip back costs no reconnect. Default 10s.
	Linger *metav1.Duration `json:"linger,omitempty"`
}
```

Every optional scalar with a default is a pointer with an `…OrDefault`
accessor, as the typed-client gotcha requires. `EPGSource` is an
`omitempty` string whose zero value means nothing, so its CRD default can
be reached.

### 3.0 IPTVChannel's name, owner and status

- **Its name:** `<provider>-<base32 SHA-256 of the key, 10 characters>`,
  built by `pkg/names`. So the UI and kubectl name a channel the same way,
  and a second IPTVChannel for one key cannot exist.
- **Its owner:** an owner reference to its IPTVProvider, so deleting the
  provider deletes its channels.
- **Its status** comes from the IPTVChannel reconciler (§6), its sole
  writer, under `k8s.ManagerLiveTVChannel`:
  - `conditions`: `Ready`, with reason `Active`, `Inactive`, `Missing`
    (the key has left the playlist), `DuplicateNumber`, or `GuideNotFound`;
  - `guide`: the guide and channel actually used, including one resolved by
    tvg-id or a dummy;
  - `observedGeneration`.
- **Its printer columns:** Provider, Number, Name, Active, Guide, Ready.

### 3.1 No channel cap

**Why the cap goes:**
- **The 480 is the setup wizard's.** Plex Web's DVR wizard shows at most
  480 channels.
- **PMS takes more through its API.** It accepts a larger channel map when
  a DVR is registered through the API rather than the wizard. That is
  iptvtunerr's documented production path (its README: "Plex's wizard caps
  the visible lineup at 480 channels … use programmatic registration
  instead", `run -register-plex=api`).
- **So nothing in clustarr limits the active count.** cluster-plex
  registers every DVR through the API (§6.1). Under PMS the owner finishes
  the setup in the wizard, and cluster-plex then saves the full channel map
  through the API, so there is no cap there either.
- **What is gone:** the CEL rule, the UI's `/ 480` counter and refusal, and
  the tuner's lineup cap. The UI shows the active count only.

**What remains to measure, in the plan's PMS recording task (§6.1):**
1. **The largest channel map PMS 1.43.4 accepts in one request.** The map
   is a full replacement, so it cannot be split. iptvtunerr found that
   batching truncates the map to its last batch. The request carries every
   pair in its query string, so the URL length is the likely limit.
2. **How PMS behaves with thousands of channels,** including its guide
   grid. Clustarr does not limit this; the owner judges it.

**If PMS refuses a large map,** the fallback is iptvtunerr's "category DVR
fleet": several devices from one provider, each with its own device ID and
a non-overlapping range of guide numbers. It is designed here but not
built. It would be a `spec.devices` list splitting the lineup by group,
and each entry would get its own Service.

**What is still validated:**
- **CEL, for the cheap rules:** exactly one of `url` and `urlFrom`; exactly
  one of `epg.guide` and `epg.dummy`; `channelID` required with `guide`;
  `providerRef` and `key` immutable.
- **The IPTVChannel reconciler, for the rules that span objects:** a
  duplicate number among a provider's active channels, and an `epg.guide`
  naming no guide. Each is reported on the channel's `Ready` condition. The
  tuner's `lineup.json` keeps the first channel by name and drops the
  others.

### 3.2 Status: one writer

The reconciler is the sole writer of status, under `k8s.ManagerLiveTV`.

- **Why one writer works:** the tuner runs in the same process and exposes
  `tuner.Snapshot(ref)`: playlist, guides, lineup and tuners in use.
- **How status is written:** after each fetch, and on a change in tuners in
  use (debounced to 5 s), the tuner enqueues the provider through a
  `source.Channel`. The reconciler then renders one complete status from
  the CR and the snapshot.
- **What that avoids:**
  - a second field manager;
  - compare-and-swap between writers;
  - slow work between a read and its apply, the lost-update gotcha. A fetch
    happens in the tuner, never in the reconcile.

**Fields:**
- `conditions`:
  - `Ready`;
  - `Valid`: the Secrets and keys the provider names exist;
  - `PlaylistFetched`;
  - `GuidesFetched`;
- `observedGeneration`;
- `deviceID`: eight hex digits from a hash of namespace/name, so a
  recreated CR is the same device to Plex;
- `address`: the provider's Service ClusterIP, which cluster-plex registers
  with PMS (§6.1);
- `guideURL`: XEPG only;
- `playlist`: entries, groups, candidates, fetchedAt, hash, error;
- `guides[]`: name, channels, programmes, fetchedAt, error; `MaxItems=16`;
- `lineup`: active, unmapped (on dummy), missing (active keys absent from
  the playlist), and `hash`, a hash of the rendered `lineup.json`. A change
  in it tells cluster-plex to save the DVR's channel map again;
- `guideHash`: a hash of the rendered XEPG guide. A change in it tells
  cluster-plex to reload Plex's guide;
- `tuners`: total, inUse.

**Printer columns:** Tuners, Active, Candidates, EPG, Ready, Address. So
`kubectl get iptv` shows what Plex will get.

## 4. Data: `pkg/iptv` (pure Go, no cluster)

The tuner and the UI share one implementation of every rule, so the
counts, filters and previews the UI shows are the ones Plex gets.

### 4.1 M3U: `pkg/iptv/m3u`

- **What it parses:**
  - `#EXTM3U` with its `url-tvg`/`x-tvg-url` header (offered to the UI as a
    guide to add);
  - `#EXTINF:-1 tvg-id="" tvg-name="" tvg-logo="" group-title="",Name`;
  - `#EXTGRP`;
  - `#EXTVLCOPT:http-user-agent=` (per-entry user agent).
- **What it tolerates:** a BOM, CRLF line ends, unquoted attributes and
  commas inside quotes.
- **Caps:** 64 MiB read through `io.LimitReader` with an
  `ErrResponseTooLarge`, and 200,000 entries.
- **`EntryKey`:**
  - `t:` plus tvg-id, when the tvg-id is unique within the playlist;
  - otherwise `n:` plus a base32 SHA-256 of (group, name), truncated to 26
    characters.
  - It is stable across the token rotations providers apply to stream URLs,
    which a URL-based key would not survive.
  - When an active key leaves the playlist, it counts as `lineup.missing`.
    The UI lists it as "Missing", and it is never silently deleted.

### 4.2 Filters: `pkg/iptv/filter`

- **`Candidates(entries, filters) []Entry`,** with xTeVe's semantics:
  - **`groupTitle`:** the group equals `match`.
  - **`custom`:** the name contains `match`.
  - **Both:** at least one of `include` must appear and none of `exclude`.
  - Case-insensitive unless `caseSensitive`.
  - The result is the union over enabled filters.
- **`Preview(entries, filter) int`** serves the UI's live count.

### 4.3 XMLTV: `pkg/iptv/xmltv`

- **Parsing:** a streaming `encoding/xml` token reader, never a DOM, with
  gzip detected by magic bytes. Guides run to hundreds of MiB. The cap is 1
  GiB uncompressed.
- **One pass yields:**
  - the channel list `{id, displayNames, icon}` for the UI's mapping
    dropdown;
  - with a set of wanted channel ids, those channels' programmes from now
    to now plus `guideDays` (7).
- **`BuildGuide(lineup, sources) []byte`** writes the XEPG guide:
  - one `<channel id="<number>">` per active channel, since Plex maps an
    XMLTV channel to a lineup entry by the GuideNumber, as xTeVe does;
  - its programmes, timeshifted;
  - dummy blocks for unmapped channels;
  - sorted, so the same input gives the same bytes.
- **Auto-mapping:**
  - **`AutoMap(entry, sources)`:** tvg-id equals an XMLTV channel id first,
    then a match of `release.TitleNorm(name)` against a normalised
    display-name;
  - **at render time:** the tuner applies it to an active channel with no
    `epg`;
  - **in the UI:** the "Auto-map guide" bulk action writes the result into
    each IPTVChannel's spec, so the objects say what Plex gets.

### 4.4 The HDHomeRun surface: `pkg/iptv/hdhr`

- Pure renderers for `discover.json`, `lineup_status.json`, `lineup.json`
  and `device.xml`, taken from a `Device` and a `[]LineupEntry`.
- **Golden files:** each renderer is tested against a golden file built
  from a real HDHomeRun's answers, which xTeVe's source reproduces.
- **`ModelNumber`:** `HDTC-2US`, and `FirmwareName`: `hdhomeruntc_atsc`,
  the values Plex accepts from xTeVe.

## 5. The tuner: a manager runnable, `app/livetv/tuner`

There is no tuner pod and no new service. `cmd/manager` registers the
tuner beside the IPTVProvider controller as a leader-only runnable
(`NeedLeaderElection` true).

- **One listener for every provider:** `--livetv-bind-address`, default
  `:5004` (HDHomeRun's stream port), opened when the pod wins the election.
  A non-leader pod refuses connections on it.
- **The manager's image:** pure Go and distroless. The tuner links no ffgo
  or purego, so the manager's own dynamic-loader guard still holds.

### 5.0 One address per provider

Plex adds a device by an address and reads `/discover.json` at its root.
So each provider needs an address of its own:

- **A Service per provider:** the controller (§6) gives each one its own
  Service, `livetv-<hash of ns/name>`.
  - It lives in the manager's namespace: a selector reaches only its own
    namespace's pods.
  - It selects the manager pod, port 80 to `5004`.
  - Its ClusterIP is `status.address`.
- **Routing:**
  - **At the root:** Plex sends `Host: <the address it was given>`. The
    tuner routes `/discover.json`, `/lineup_status.json`, `/device.xml`,
    `/lineup.json` and `/lineup.post` to the provider whose
    `status.address` that host is. An unknown host at the root gets 404.
  - **Everything else:** each URL the tuner hands out is absolute under
    `http://<address>/livetv/<ns>/<name>/`. That covers `BaseURL`,
    `LineupURL`, the stream URLs and the guide URL. So every later request
    names its provider in the path, which also serves curl and the e2e
    tests.

### 5.0.1 What sharing the manager's process costs

The relay runs in the process that runs every controller, so it is
bounded:

- **Memory:** it is at most `Σ tuners × stream.buffer`, 64 MiB at 8 tuners
  of 8 MiB. The chart adds `livetv.memory` (default 128Mi) to the manager's
  request and limit, and `GOMEMLIMIT` follows.
- **Failures:** each stream's goroutines recover their own panic and close
  only that stream's viewers. No stream code holds a lock the controllers
  share.
- **Readiness:** a provider that cannot fetch its playlist never fails the
  manager's `/readyz`. It reads `PlaylistFetched=False`, and its
  `lineup.json` answers 503.
- **Bandwidth:** bytes pass straight through. Eight HD channels are about
  80 Mbit/s.

### 5.1 Startup and refresh

1. **Read the CRs** from the manager's cache: every enabled provider, and
   its IPTVChannels through a field index on `spec.providerRef`. A
   namespace List per event would scale as watched × listed, the gotcha the
   startup map functions hit.
2. **Fetch the playlist**, with the URL from the Secret. A Secret is read
   by name with `get` only, as the usenet engine reads provider Secrets.
   - The playlist, stream URLs included, is held in memory only.
   - Credentials ride in URLs, so every logged or reported URL passes
     `iptv.Redact`, which drops the userinfo and query and keeps the path.
3. **Fetch each guide** whose copy in the object store is older than its
   `refresh`, or that has none.
   - It is parsed in one pass for the active set.
   - The channel lists and the rendered guide are stored (5.4).
   - Until the fetch finishes, the stored guide is served, so a restart
     never blanks Plex's guide.
4. **Re-render on a spec change:** `lineup.json` and the guide are rendered
   again when an IPTVChannel or the provider's filters or guides change.
   The render is debounced to 2 s, so a bulk edit of thousands of channels
   renders once. A playlist URL
   change fetches the playlist again. Nothing restarts.
5. **Refresh on schedule or by request:** fetches repeat on each source's
   `refresh`. The `clustarr.io/livetv-refresh` annotation, which the UI's
   "Refresh now" sets, forces one.

**Before the first fetch:** until a provider's playlist has been fetched
once, its `lineup.json` answers 503 and Plex retries.

### 5.2 The stream relay

- **`GET /stream/<number>`** (the lineup's `URL`):
  - **The channel already streams:** the viewer joins its fan-out and takes
    no tuner.
  - **Otherwise:** it takes a tuner from a semaphore of `spec.tuners`. With
    every tuner taken it answers `503 Service Unavailable`, which Plex reads
    as "all tuners in use", as from a real HDHomeRun.
- **The upstream:**
  - one HTTP GET with the provider's user agent;
  - a 10 s header timeout and no body timeout.
- **The fan-out:**
  - **Buffer:** one ring buffer per channel of `stream.buffer` bytes,
    written by the upstream reader, with `ReadAt` cursors per viewer.
  - **Joining:** a new viewer starts at the newest 188-byte TS packet
    boundary, and Plex resyncs at the next PAT.
  - **A slow viewer:** one that falls a full buffer behind is cut off,
    never waited for, so one stalled client cannot stall the others.
- **The upstream failing mid-stream:** it is reopened up to 3 times with a
  1 s backoff, keeping viewers attached. After that the viewers' responses
  end.
- **The last viewer leaving:** the upstream stays open for `stream.linger`,
  then closes and gives back its tuner.
- **The stream format:** the relay expects MPEG-TS, which is what the
  provider serves.
  - An upstream answering an HLS playlist
    (`application/vnd.apple.mpegurl`) is refused with 502, as
    `unsupportedStream` on the error counter.
  - HLS is deferred (§9).

This is xTeVe's own buffer, minus the ffmpeg and VLC options:
- one upstream connection per channel, however many Plex clients watch;
- memory bounded at `tuners × buffer` (§5.0.1);
- no disk.

### 5.3 The HTTP surface

| Path | Serves |
|---|---|
| `/discover.json`, `/lineup_status.json`, `/device.xml`, `/lineup.json`, `/lineup.post` | §2, routed by `Host` (§5.0), from `pkg/iptv/hdhr` |
| `/livetv/<ns>/<name>/{discover.json,lineup.json,…}` | the same, by path |
| `/livetv/<ns>/<name>/xmltv.xml` | the XEPG guide (404 under PMS) |
| `/livetv/<ns>/<name>/stream/<number>` | §5.2 |
| `/livetv/<ns>/<name>/logo/<sha>` (phase 3) | cached channel logos, so Plex never hotlinks a provider host |

Metrics ride the manager's own `/metrics`.

**Authentication:** the surface has none, because Plex sends none. The
Services are ClusterIP only, and nothing in the chart routes an Ingress to
them: whoever reaches a stream watches on the owner's subscription.

**The NetworkPolicy (on by default):**
- **What it selects:** the manager pod.
- **Port 5004:** allowed only from pods matching
  `livetv.allowFrom.podSelector`, by default cluster-plex's PMS pods. The
  two share one namespace, so a namespace selector could not tell them
  apart.
- **The manager's other ports (metrics, health, webhooks):** allowed from
  anywhere, as today. A policy that selects a pod denies every port it does
  not list, so leaving a port out would cut it off.
- **Guard test:** `TestLiveTVPolicyListsEveryManagerPort` holds the
  policy's port list to the manager container's ports. A new manager port
  can then never be blocked silently.
- **Turning it off:** `livetv.networkPolicy.enabled: false`, for a cluster
  whose CNI enforces no policies.

### 5.4 What the UI reads: the `clustarr-livetv` object store

The bucket is file-backed: `ForSingleNode` keeps object stores on file
storage, so it does not touch the memory budget. It is 1 GiB. The tuner is
its sole writer, and the UI only `Get`s from it, as it does `/art`.

| Object | Content |
|---|---|
| `playlist/<ns>/<name>` | gzip JSON: hash, fetchedAt, entries `{key, name, group, tvgID, tvgName, logo}`. **No stream URLs:** the UI never holds a credential, and the role grants it no Secrets. |
| `guide-channels/<ns>/<name>/<guide>` | gzip JSON: `{id, displayName, icon}` per XMLTV channel |
| `guide/<ns>/<name>` | the rendered XEPG guide, gzip, served across restarts |

The UI caches each object by digest, so its 5 s projection tick re-reads
nothing that has not changed.

### 5.5 Metrics

| Metric | Labels |
|---|---|
| `clustarr_livetv_tuners_in_use` | `provider` |
| `clustarr_livetv_viewers` | `provider` |
| `clustarr_livetv_stream_bytes_total` | `provider` |
| `clustarr_livetv_upstream_errors_total` | `provider`, `reason` (`refused`, `timeout`, `eof`, `unsupportedStream`) |
| `clustarr_livetv_fetch_duration_seconds` | `provider`, `source` (`playlist` or the guide name) |

There is no channel label: a channel name is a title.

## 6. The controller: `app/livetv/controller/iptvprovider`

`cmd/manager` registers the controller beside the tuner.

**Each provider's Service (§5.0):**
- **Created:** by the controller, labelled with the provider.
- **Cleaned up:** an owner reference cannot cross namespaces, so finalizer
  `clustarr.io/livetv-service` deletes the Service when the CR goes.
- **Kept:** it is never recreated on a spec edit, so the ClusterIP Plex was
  given stays.

**Its other work:**
- `Valid` (§3.2);
- `deviceID`, `address` and `guideURL`;
- status from the tuner's snapshot (§3.2).

It does no fetching. Fetching is the tuner's.

**The IPTVChannel reconciler** (`app/livetv/controller/iptvchannel`) is
the sole writer of IPTVChannel status (§3.0).
- **What wakes it:** a channel's own change, and its provider's snapshot
  changing (a new playlist or guide). The provider's events reach its
  channels through the `spec.providerRef` index.
- **What it writes:** only a status that differs from the current one. So
  a playlist refresh that changes nothing writes nothing, even across
  thousands of channels.

**Guard test:** `TestLiveTVServicesSelectTheManager` holds each Service's
selector and target port to the manager pod's labels and
`--livetv-bind-address`, as the chart and kustomize render them. A
controller-built Service is the same class as the engines' controller-built
pods: nothing else would notice it drifting from the installer.

**Placement:** the work lands on the `unify-manager-agent` branch, where
`cmd/manager` exists, coordinated with that branch's session. Nothing lands
on main first.

**RBAC:** the manager role needs:
- `create`, `get`, `list`, `watch`, `update` and `delete` on Services;
- `get` on Secrets, for named reads, unless it already has it.

### 6.1 cluster-plex adds each provider to Plex

cluster-plex already has the pieces:
- a watcher on clustarr's namespace (`pkg/clustarrwatch`);
- a PMS provisioner (`pkg/plex/provision`) that converges metadata
  providers and libraries through PMS's API, with the server's own token.

**What changes:**
- **The watch:** the watcher adds `iptvproviders.clustarr.io`.
- **The provisioner:** it gains DVRs, run by cluster-plex's leader. That
  makes it the one writer of Plex's Live TV state.

**What it converges, for each IPTVProvider that is `Ready` and enabled.**
The calls are the ones iptvtunerr makes in production
(`internal/plex/dvr.go`), each with the server's `X-Plex-Token`:

1. **The device.**
   - **Find it:** `GET /media/grabbers/devices`, looking for the identifier
     `device://tv.plex.grabbers.hdhomerun/<status.deviceID>`.
   - **When none exists:**
     - probe with `POST /media/grabbers/devices/discover?uri=http://<status.address>`;
     - then register with `POST /media/grabbers/devices?uri=http://<status.address>`.
   - **When one exists, it is never registered again.** Registering again
     is how duplicate and empty DVR rows arise (iptvtunerr's recovery
     notes).
   - **When an existing device names another address:** it records
     `DVRDeviceAddressChanged` and changes nothing. The Service is never
     recreated, so this means a person did something by hand.
2. **The DVR.**
   - **Under XEPG:** when `GET /livetv/dvrs` has no DVR on the device, it
     creates one with `POST /livetv/dvrs?language=<lang>&device=<device uuid>&lineup=lineup://tv.plex.providers.epg.xmltv/<url-encoded status.guideURL>#<friendlyName>`.
   - **Under PMS:** a Plex lineup depends on the owner's location, which
     the CR does not know. So it registers the device only and records
     `DVRNeedsLineup`. Once the owner has finished Plex's wizard, it adopts
     that DVR for steps 3 and 4.
3. **The channel map,** when `status.lineup.hash` differs from the hash it
   last saved:
   - `GET /livetv/epg/channelmap?device=<uuid>&lineup=<lineup id>` gives
     PMS's pairing of the device's channels with the lineup's;
   - then **one** `PUT /media/grabbers/devices/<key>/channelmap` enables
     every channel. Its query is `channelsEnabled=<every number>`, plus
     `channelMappingByKey[<number>]=<channel key>` and
     `channelMapping[<number>]=<lineup identifier>` for each channel.
   - The PUT replaces the whole map, so it is never batched. A batch would
     leave only the last batch's channels (iptvtunerr).
   - This is the step that carries more than 480 channels, and it is how an
     activation on the Live TV page reaches Plex without the wizard.
4. **The guide:** when `status.guideHash` changes, it calls
   `POST /livetv/dvrs/<key>/reloadGuide`.

**Which DVRs it manages:** cluster-plex manages exactly the devices whose
identifier carries an IPTVProvider's `deviceID`. Every other tuner and DVR
in Plex is left alone.

**Ghost cleanup (iptvtunerr's "ghost-hunter" pattern):** on a managed
device, DVR rows beyond the first are deleted with
`DELETE /livetv/dvrs/<key>`. So are DVRs left on a managed device that no
longer exists. Being the one writer is what keeps such rows from coming
back.

**Deleting a provider** removes its DVR and its device, through the
watcher's tombstone handling. That also removes the DVR's recording rules:
the tuner they would record from is gone. The calls are
`DELETE /livetv/dvrs/<key>` and `DELETE /media/grabbers/devices/<key>`.
`enabled: false` removes
nothing, and Plex shows the device offline until it is enabled again.

**Reporting:** cluster-plex never writes clustarr's status (one writer). It
records Events on the IPTVProvider instead:
- `DVRProvisioned`;
- `DVRChannelMapSaved`;
- `DVRNeedsLineup`;
- `DVRDeviceAddressChanged`;
- `DVRProvisionFailed`, with PMS's answer through cluster-plex's existing
  `snippet`.

`kubectl describe iptv` and the Settings panel (§7.1) show them.

**Recording the calls.** The calls come from iptvtunerr's code, not from
Plex's documentation. So the plan's first cluster-plex task records them
once against the owner's PMS (1.43.4), by hand, and replays them as
`fakepms` fixtures. This is how `ui/plex`'s extras route was pinned down.
The same task settles three points:
- the largest channel map one PUT carries (§3.1);
- whether PMS re-reads a changed `lineup.json` on its own before step 3,
  or needs a device rescan first;
- what step 2 needs under PMS.

## 7. The UI

The UI writes only spec, Secrets and annotations, through `ui/actions`,
under `clustarr-ui`:
- **IPTVProvider** is the eleventh Settings kind (create, patch, delete).
- **IPTVChannel** joins the kinds the UI creates, as Add New's four do
  (create, patch, delete; spec only).

CLAUDE.md's invariant and `actions.Grants()` change to match.
`TestUINeverWrites` needs no change: the object-store reads are `Get`.

### 7.1 Settings: "Live TV providers"

A section in the same form system as Download Clients (`ui/forms`).

- **Create and edit fields:**
  - name;
  - **M3U URL:** written into Secret `<name>-livetv`, key `playlist-url`,
    and shown afterwards only as "set". The UI never reads a Secret;
  - tuners;
  - user agent;
  - playlist refresh;
  - EPG source (XEPG or PMS);
  - channel number start;
  - **Guides:** a repeatable "Add EPG URL" row (name, URL, refresh), each
    URL in the same Secret under `guide-<name>-url`. The playlist's own
    `url-tvg` header is offered as a one-click guide.
- **A status panel:**
  - **Plex:** the DVR's state, from the latest of the Events cluster-plex
    records on the provider (§6.1); the ui role gains `list` and `watch` on
    events;
  - the device address and the XMLTV URL, for a Plex that cluster-plex does
    not manage;
  - entries, candidates and active channels;
  - each guide's last fetch and error;
  - tuners in use.
- **Actions:** "Refresh now" (the annotation) and Delete.

### 7.2 The Live TV page: `/livetv`

A sidebar entry, with one sub-entry per provider, as Library's media types
are (`sidebar.MenuSub`). Each provider has three tabs that swap through
htmx, so the sidebar stays put:

- **Mapping** (the default): xTeVe's mapping table.
  - **Rows:** every candidate, plus every active mapping now missing from
    the playlist.
  - **Columns:**
    - a select box;
    - an **Active** toggle;
    - **#** (number);
    - logo;
    - name;
    - group;
    - **Guide:** the source and channel, or "Dummy 60m", or "by tvg-id";
    - timeshift;
    - badges: *No guide*, *Missing*, *Duplicate #*.
  - **The toolbar,** the library's menubar:
    - search over name and tvg-id;
    - Group (a select);
    - State: All, Active, Inactive, No guide, Missing;
    - Sort: number, name, group.
  - **The counter:** a badge reads **Active 312**.
  - **Paging:** server-side through `ui/paging`, 100 rows a page, because a
    candidate set can run to thousands.
  - **Editing a row** opens a dialog:
    - number, name, group and logo override;
    - timeshift;
    - the guide: a source select, plus a typeahead over that source's
      `guide-channels`. This is `find.js`'s keyboard model, ranked as
      `projection.Find` ranks titles.
  - **Bulk, the library's mass editor:**
    - **Select** marks rows; a bottom bar acts on them, or on every row the
      current filter matches;
    - **Activate;**
    - **Deactivate;**
    - **Auto-number:** from `channelNumberStart`, filling gaps, in the
      current sort;
    - **Auto-map guide** (§4.3);
    - **Use dummy guide;**
    - **Clear mapping.**
- **Filters:**
  - **Left:** the filter table: name, type, match, include, exclude, case,
    enabled, and **matched** (a live count from `filter.Preview`). Add,
    edit and delete happen in a dialog. Typing in it updates the matched
    count through an htmx `GET …/filters/preview`.
  - **Right:** the playlist's groups, with entry counts and a search box.
    Each group has **Add as filter**, which is xTeVe's group list.
  - **Below:** a "candidates" total, and "would also remove N active
    channels" when an edit drops active ones. Those are kept as Missing,
    never deactivated by a filter edit.
- **Guides:** each XMLTV source's channel count, programme count, last
  fetch and error. Here also: **Add EPG URL** (the same form as Settings)
  and **Refresh now**.

### 7.3 How a table edit is written

- **A single row:** `actions.EditIPTVChannel` reads the channel and sends a
  JSON merge patch of its spec with `metadata.resourceVersion`, the
  precondition `ui/actions/season.go` uses.
  - It creates the IPTVChannel, named and owned per §3.0, when the row has
    none.
  - On a 409 Conflict it reads again and applies the edit once more; a
    second conflict is reported as "changed elsewhere, reload".
- **Deactivating:**
  - a channel with no customisation is deleted;
  - a customised one keeps its mapping with `active: false`.
- **A bulk edit:** `actions.EditIPTVChannels` does the same per row.
  - Eight writers run at once, under a dedicated client limited to 50
    requests a second. That is about a minute for 3,000 rows.
  - The bottom bar shows progress over the page's SSE stream.
  - A failure names the rows it did not write, and every other row stays
    written.
  - The tuner's 2 s debounce renders the lineup once, after the burst.

Filter, guide and settings edits are merge patches of the provider's spec,
under the same precondition.

## 8. Testing

- **`pkg/iptv`, table-driven:**
  - **Fixtures under `test/data/iptv/`:**
    - a real-shaped `m3u_plus` with every attribute form;
    - a 50,000-entry playlist as a benchmark under 1 s;
    - an XMLTV guide, plain and gzip.
  - **Filter cases:** taken from xTeVe's own documentation.
  - **`EntryKey`:** stable across stream-URL token changes; distinct keys
    for duplicate tvg-ids.
  - **The guide:** goldens for timeshift and dummy blocks, and a
    deterministic output.
  - **The HDHomeRun renderers:** goldens.
  - **Inputs from the real producer:** the parser runs on the fixture, never
    on a `[]Entry` built by hand. That is the "fixture shaped like the
    answer" gotcha.
- **The relay, against an `httptest` upstream:**
  - two viewers share one upstream and one tuner;
  - tuner exhaustion answers 503;
  - a slow viewer is cut off and the other is unharmed;
  - an upstream reset is reopened;
  - linger works, and so does the tuner coming back after linger;
  - an HLS answer is refused as `unsupportedStream`;
  - `Host` routing picks the provider, an unknown host gets 404, and path
    routing works on any host.
  - The tests run under `-race`.
- **`pkg/crdcheck`:**
  - `url` and `urlFrom` are exclusive;
  - `guide` and `dummy` are exclusive;
  - an IPTVChannel's `providerRef` and `key` are immutable.
  - These run under `KUBEBUILDER_ASSETS`.
- **envtest:**
  - the controller creates the Service, keeps it across a spec edit, and
    deletes it through the finalizer;
  - a duplicate number marks both channels `DuplicateNumber`, and a key
    gone from the playlist marks its channel `Missing`;
  - a playlist refresh that changes nothing writes no IPTVChannel status,
    shown across 3,000 channels;
  - deleting the provider removes its channels through the owner
    reference;
  - status rendered from a snapshot is complete: it is run against an
    object that already has status, and `managedFields` show one manager
    (the release gotchas);
  - the Service selector guard.
- **The UI:**
  - a bulk activation of 3,000 rows reports progress, and a failure part
    way names its unwritten rows;
  - a 409 on a row is retried once;
  - the filter preview count;
  - the Missing rows;
  - `TestBothUICommandsWireEveryUIOption` covers the new
    `ui.Options.LiveTV`.
- **cluster-plex, against its `fakepms`, with fixtures recorded from the
  owner's PMS:**
  - a Ready provider gets one device and one DVR;
  - a second run changes nothing;
  - a new `lineup.hash` saves the channel map once, in one PUT, with 2,000
    channels;
  - a new `guideHash` reloads the guide once;
  - a provider under PMS gets a device and `DVRNeedsLineup`, and no DVR;
  - a deleted provider removes its DVR and device;
  - a DVR whose device no IPTVProvider names is never touched;
  - a duplicate DVR row on a managed device is deleted;
  - an existing device is never registered again.
- **e2e scenario 19** (`test/e2e/livetv_test.go`, with an `iptvstub`
  fixture serving an M3U, an MPEG-TS loop and an XMLTV guide):
  - create an IPTVProvider;
  - read `lineup.json` and `xmltv.xml` through the Service;
  - read 1 MiB of `/stream/<n>` from two clients over one upstream
    connection;
  - a third channel past `tuners: 1` gets 503.
  - It runs on kind, under the rule that nothing is finished until it does.

## 9. Phases

1. **Plex sees channels.** This phase is enough to configure from kubectl.
   It covers:
   - the two kinds and their CEL;
   - `pkg/iptv` (M3U, filter, XMLTV, hdhr);
   - the controller;
   - the tuner runnable in the manager, with the TS relay and fan-out;
   - the object store;
   - the chart, kustomize and RBAC, with the NetworkPolicy;
   - e2e 19;
   - in cluster-plex: the IPTVProvider watch and the DVR provisioner
     (§6.1), after the recorded PMS fixtures.
2. **The UI:** the Settings section and the Live TV page (Mapping, Filters,
   Guides), with bulk edits.
3. **Breadth:**
   - the logo cache (`/logo`, used in the lineup, the guide and the UI);
   - a `/playlist.m3u` output for players other than Plex (cheap, since the
     lineup is already rendered).

**Not planned:**
- HLS upstreams: the provider serves MPEG-TS. They would add polling a
  media playlist into the same ring, since TS segments concatenate;
- recording (Plex's DVR does it);
- transcoding or an ffmpeg buffer;
- catch-up TV;
- Xtream Codes API logins (the `get.php` M3U URL covers them);
- SSDP discovery;
- multi-user authentication.

## 10. Open questions for the owner

None. Two consequences to confirm while reviewing:

1. **Deleting a provider** removes its Plex DVR and that DVR's recording
   rules (§6.1).
2. **Mappings are now objects:** they live in `IPTVChannel`, one per mapped
   channel, rather than in the provider (§3). This follows from removing
   the cap.
