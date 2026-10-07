# Live TV: an IPTV proxy for Plex, after xTeVe

Status: design, for the owner's approval (2026-10-07).

**Owner rulings (2026-10-07):**
- **No new service:** the tuner and its controller both run inside the
  unified manager (`cmd/manager`, branch `unify-manager-agent`), with no
  Deployment or service of their own.
- **The group:** `IPTVProvider` belongs to the `clustarr.io` group.
- **The stream format:** the provider serves MPEG-TS, so HLS is deferred.
- **No channel cap for the owner:** a provider carries any number of
  active channels (§3.1). This follows iptvtunerr's patterns.
  - **What it changes:** the mappings move out of `IPTVProvider` into a
    kind of their own, `IPTVChannel`, one object per mapped channel. One
    object could not hold thousands.
  - **How Plex takes them (amended 2026-10-07, after the recording):**
    PMS takes at most ~450 channels per device. So a provider is split
    into blocks of consecutive channel numbers, each its own device and
    DVR, numbered contiguously across them. Block boundaries are sticky
    (§3.1).
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

- **One block, one device:** each block of a provider's channels (§3.1) is
  one emulated HDHomeRun device and one Plex DVR. A provider of up to 400
  channels is one block.
- **No channel cap for the owner:** the owner's ruling (2026-10-07). §3.1
  says how the blocks carry it past PMS's per-device limit.
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
  controller packs its channels into blocks, each one Plex device on a port
  of its own (§3.1, §5.0). Its tuner is a runnable of the manager (§5), not
  a pod of its own.
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

	// Device names the emulated HDHomeRuns, one per block (§3.1).
	Device DeviceSpec `json:"device,omitempty"`

	// DVR sizes the blocks the lineup is split into, one Plex DVR each (§3.1).
	DVR DVRSpec `json:"dvr,omitempty"`

	// Stream tunes the relay.
	Stream StreamSpec `json:"stream,omitempty"`
}

type DVRSpec struct {
	// MaxChannels is a block's most channels. Default 400; PMS takes at most
	// ~450 per device (§3.1). A block is also held under 30,000 bytes of
	// channel map, whichever comes first.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=450
	MaxChannels *int32 `json:"maxChannels,omitempty"`
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
	// ID pins the first block's DeviceID: eight hex digits, unique among
	// every provider's blocks (§3.1). Later blocks derive theirs from it.
	// Unset, every DeviceID is a hash (§3.2).
	// +kubebuilder:validation:Pattern=`^[0-9A-F]{8}$`
	ID string `json:"id,omitempty"`
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

### 3.1 One DVR per block of channels

**What PMS takes (recorded 2026-10-07, PMS 1.43.4;
cluster-plex `docs/livetv-pms-calls.md`):**
- **One device's channel map is one request.** A save replaces the whole
  map, so it cannot be batched.
- **That request stops at 32 KiB of request line.** PMS answers 400 past
  it. With Go's `url.Values` that is **454 channels** for 4-digit numbers.
  PMS reads no body, so nothing gets past it.
- **The wizard's "480" is the same wall.** No API path removes it.

**So a provider's active channels are split into blocks, one Plex DVR
each.** Channel numbers read as one contiguous lineup across the DVRs.

**How blocks are made.** The IPTVProvider controller does it, in
`pkg/iptv/blocks`, a pure function:
- **Order:** active channels sorted by number, major then minor.
- **A block's limits:**
  - `spec.dvr.maxChannels` channels (default 400, at most 450);
  - **30,000 bytes** of channel-map query, measured with the exact encoding
    cluster-plex sends. For our guides PMS's three identifiers are each the
    GuideNumber, so the size is known before Plex sees it. A long number
    (`12345.123`) costs about 97 bytes, a 4-digit one 72.
- **Contiguity:** a block holds a run of consecutive numbers, and every
  number in block k+1 is above every number in block k.

**Blocks are sticky, so a channel rarely changes DVR.** A channel that
changes DVR loses the Plex recording rules made on it.
- **Where boundaries live:** `status.blocks[].start`. A channel belongs to
  the last block whose `start` is at or below its number.
- **First packing:** each block is filled to 75% of either limit, leaving
  room to grow.
- **When a block outgrows a limit:** it splits at its median channel. The
  new boundary is that channel's number. Only the upper half moves, to a
  new block and DVR; every other block stays as it is.
- **When a block has no channel left:** it is removed. Blocks are never
  merged, because merging moves channels.
- **When status is lost** (the CR recreated): the first packing runs again.
  It is deterministic, so the same channels give the same blocks.
- **Bound:** `MaxItems=32` blocks, 12,800 channels at 400 each. A provider
  past that reads `Ready=False`, reason `TooManyChannels`.

**What each block is in Plex:**
- an HDHomeRun device of its own:
  - DeviceID from a hash of namespace, name and the block's `start`, or,
    for the first block, `spec.device.id` when set;
  - FriendlyName `<device.friendlyName> <start>+`, e.g. "Sling 1000+";
- its own `lineup.json`, and an XMLTV guide that is a subset of the feeds:
  only its channels, and their programmes for `guideDays` (§4.3);
- its own port, reached on a link-local address inside each Plex pod
  (§5.0);
- its own DVR, whose lineup title is the FriendlyName.

A title names only the block's start, which never changes, because Plex
fixes a DVR's title at creation.

**Tuners:** every block's device advertises `spec.tuners`. The relay
enforces the provider-wide limit (§5.2). When every tuner is busy, a stream
on another block's DVR gets 503, which Plex shows as "tuner busy".

**What no two registrations share.** A registration is one block: one
device, one port, one DVR. Across every provider in the cluster:
- **Device IDs are unique.**
  - Derived IDs are hashes, so they differ by construction.
  - An explicit `spec.device.id` is checked against every other block's
    ID. The provider created later reads `Ready=False`, reason
    `DuplicateDeviceID`.
  - cluster-plex also refuses a DeviceID that Plex already has at another
    address, a real HDHomeRun or another proxy (§6.1).
- **Number ranges do not overlap.**
  - Within a provider, blocks are disjoint by construction.
  - Across providers, the controller checks each provider's span, from its
    lowest active number to its highest, against every other's. The
    provider created later reads `Ready=False`, reason `NumberRangeOverlap`,
    naming the other provider and the shared span.
  - Disjoint spans also make every GuideNumber, and so every XMLTV channel
    id, unique cluster-wide.
- **Each block has one registering owner.** Only the cluster-plex lease
  holder registers, and a Lease per block keeps a second cluster-plex
  installation from registering it in another Plex (§6.1).

**A conflict never removes a DVR.** A provider that fails one of these keeps
its last good `status.blocks`, so Plex keeps what it had. Only the new
state is withheld until the conflict is fixed. The UI's auto-numbering
starts a new provider past every existing span.

**What is gone:** the 480 CEL rule and the UI's `/ 480` refusal. The UI
shows the active count and the number of blocks.

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
- `address`: the tuner Service, where the Plex pods' proxies send each
  block's port (§5.0);
- `blocks[]` (§3.1), `MaxItems=32`, ordered by `start`, one per Plex DVR:
  - `start`: the block's boundary, the number its channels begin at;
  - `channels`, `first` and `last`: its active channels and the numbers
    they span;
  - `mapBytes`: the size of its channel-map query;
  - `deviceID`: eight hex digits, from `spec.device.id` or a hash of
    namespace, name and `start` (§3.1), so a recreated CR gives the same
    devices;
  - `port`: the block's port (§5.0). The Plex pods' proxies listen on it,
    and the tuner routes by it;
  - `owner`: the cluster-plex installation registered for the block, read
    from its Lease (§6.1). Empty while none holds it;
  - `guideURL`: XEPG only;
  - `lineupHash`: a hash of the block's `lineup.json`. A change tells
    cluster-plex to save that DVR's channel map again;
  - `guideHash`: a hash of the block's guide. A change tells cluster-plex
    to reload that DVR's guide;
- `playlist`: entries, groups, candidates, fetchedAt, hash, error;
- `guides[]`: name, channels, programmes, fetchedAt, error; `MaxItems=16`;
- `lineup`: active, unmapped (on dummy), and missing (active keys absent
  from the playlist), across all blocks;
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
- **Caps:** 1 GiB read through `io.LimitReader` with an
  `ErrResponseTooLarge`, and 200,000 live entries.
- **Xtream VOD is skipped** (amended 2026-10-07): an entry whose URL path
  starts `/movie/` or `/series/` is not an entry. An Xtream Codes
  `m3u_plus` lists the panel's whole catalogue: the owner's provider sent
  328 MiB and 1.24 million entries, of which 28,527 are live, and the first
  caps (64 MiB, 200,000 entries) refused it. The read is line by line and
  only live entries are kept: 1.2 s and 17 MiB of heap for that playlist.
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
- **`BuildGuide(block, sources) []byte`** writes one block's XEPG guide, a
  subset of the feeds: only the block's channels, and only their
  programmes inside `guideDays`. So a guide grows with its block, never
  with the provider's feed:
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

### 4.5 Blocks: `pkg/iptv/blocks`

The split of §3.1, as one pure function:

```go
// Next renders a provider's blocks from the previous ones (status.blocks)
// and its active channels, under limits.
func Next(prev []Block, channels []Channel, limits Limits) ([]Block, error)

type Limits struct {
	MaxChannels int // spec.dvr.maxChannels, default 400
	MaxBytes    int // 30,000
	Fill        int // percent filled by a first packing, 75
}
```

- **What a block's size is:** `MapBytes(numbers)`, the channel-map query's
  length under the encoding cluster-plex sends. It is the same function
  cluster-plex checks before its PUT, so the two cannot disagree.
- **What it never does:** move a channel between existing blocks, other
  than the upper half of a block that splits.
- **Its error:** more than 32 blocks, which the controller reports as
  `TooManyChannels`.

## 5. The tuner: a manager runnable, `app/livetv/tuner`

There is no tuner pod and no new service. `cmd/manager` registers the
tuner beside the IPTVProvider controller as a leader-only runnable
(`NeedLeaderElection` true).

- **One listener for every provider:** `--livetv-bind-address`, default
  `:5004` (HDHomeRun's stream port), opened when the pod wins the election.
  A non-leader pod refuses connections on it.
- **The manager's image:** pure Go and distroless. The tuner links no ffgo
  or purego, so the manager's own dynamic-loader guard still holds.

### 5.0 One port per block, on a link-local address in each Plex pod

Plex adds a device by an address and reads `/discover.json` at its root.
Plex treats one IP on different ports as different devices. So every block
shares one tuner address, and each block gets a port of its own.

**clustarr's side:**
- **One Service for every provider:** `livetv`, rendered by the chart and
  kustomize, selecting the manager pod, port 80 to `5004`. Its address is
  `status.address`.
- **A port per block:** the IPTVProvider controller allocates
  `status.blocks[].port` from `--livetv-port-range`, default `47000-47999`.
  - Ports are unique across every provider. The controller is the one
    writer, and it sees every provider through its cache.
  - A block keeps its port for life, and a port is never reused while a
    block holds it. If two providers claim one port (after a status was
    lost), the older provider keeps it and the other gets a new one.

**cluster-plex's side (§6.1):**
- **A link-local address:** every Plex pod adds the same address,
  `plex.liveTV.localAddress` (default `169.254.47.1`), to its loopback
  interface. The supervisor does this at start; the Plex container is
  privileged.
  - Link-local, not loopback: it is never routed beyond the pod, and it is
    the same in every pod.
  - It avoids the link-local addresses clusters already use: cloud
    metadata at `169.254.169.254`, and NodeLocal DNSCache at
    `169.254.20.10` or `169.254.25.10`.
- **A proxy per block:** each pod runs one of its supervisor's byte-level
  TCP proxies (`pkg/proxy.TCP`) per block, listening on
  `<localAddress>:<port>` and forwarding to `status.address`.
  - Every pod runs the full set, not only the lease holder, from a
    read-only watch of the IPTVProviders. The listeners are up before PMS
    starts.
  - So the device address Plex stores, `http://169.254.47.1:<port>`, is the
    same on every pod and never moves.
  - It also works for a remote transcode, which runs on one of the same
    Plex pods.

**Routing in the tuner:**
- **By port:** the proxy passes bytes untouched, so the tuner sees PMS's
  own `Host: 169.254.47.1:<port>`. It routes `/discover.json`,
  `/lineup_status.json`, `/device.xml`, `/lineup.json` and `/lineup.post`
  to the block holding that port. An unknown port gets 404.
- **Its URLs:** `BaseURL`, `LineupURL`, the stream URLs and the guide URL
  are built from that `Host`. So every later request, streams included,
  returns through the same proxy port.
- **By path, for curl and the e2e tests:** `/livetv/<ns>/<name>/<start>/…`
  on any host.
- **A stream URL keeps working when its channel changes block.** The relay
  is per provider, so `stream/<number>` answers under any of the
  provider's blocks.

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
| `/discover.json`, `/lineup_status.json`, `/device.xml`, `/lineup.json`, `/lineup.post` | §2 for one block, routed by the port in `Host` (§5.0), from `pkg/iptv/hdhr` |
| `/livetv/<ns>/<name>/<start>/{discover.json,lineup.json,…}` | the same, by path |
| `/livetv/<ns>/<name>/<start>/xmltv.xml` | the block's XEPG guide, only its channels (404 under PMS) |
| `/livetv/<ns>/<name>/<start>/stream/<number>` | §5.2; any of the provider's numbers, under any of its blocks |
| `/livetv/<ns>/<name>/xmltv.xml` | the whole provider's guide, for curl and the UI's preview |
| `/livetv/<ns>/<name>/logo/<sha>` (phase 3) | cached channel logos, so Plex never hotlinks a provider host |

Metrics ride the manager's own `/metrics`.

**Authentication:** the surface has none, because Plex sends none. The
`livetv` Service is ClusterIP only, and nothing in the chart routes an
Ingress to it: whoever reaches a stream watches on the owner's
subscription. The block ports exist only inside the Plex pods, on a
link-local address.

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

**Its work:**
- `Valid` (§3.2);
- **the blocks** (§3.1, `pkg/iptv/blocks`): it reads the previous
  `status.blocks` and the active channels, and renders the next blocks,
  each with its `deviceID`, `port` and `guideURL`. A split or a removal
  happens here and nowhere else;
- **the ports** (§5.0), allocated across every provider;
- **the checks across providers** (§3.1): `DuplicateDeviceID` and
  `NumberRangeOverlap`. A conflict keeps the last good blocks;
- **each block's `owner`,** read from its Lease (§6.1). The controller
  only reads Leases;
- status from the tuner's snapshot (§3.2). The tuner renders each block's
  `lineup.json` and guide from the blocks in status.

It does no fetching. Fetching is the tuner's.

**The IPTVChannel reconciler** (`app/livetv/controller/iptvchannel`) is
the sole writer of IPTVChannel status (§3.0).
- **What wakes it:** a channel's own change, and its provider's snapshot
  changing (a new playlist or guide). The provider's events reach its
  channels through the `spec.providerRef` index.
- **What it writes:** only a status that differs from the current one. So
  a playlist refresh that changes nothing writes nothing, even across
  thousands of channels.

**Guard test:** `TestLiveTVServiceSelectsTheManager` holds the `livetv`
Service's selector and target port to the manager pod's labels and
`--livetv-bind-address`, in both the chart and kustomize.

**Placement:** the work lands on the `unify-manager-agent` branch, where
`cmd/manager` exists, coordinated with that branch's session. Nothing lands
on main first.

**RBAC:** the manager role needs:
- `get`, `list` and `watch` on `coordination.k8s.io` Leases, to read each
  block's owner;
- `get` on Secrets, for named reads, unless it already has it.

It creates no Service: the one `livetv` Service is the installers'.

### 6.1 cluster-plex adds each provider to Plex

cluster-plex already has the pieces:
- a watcher on clustarr's namespace (`pkg/clustarrwatch`);
- a PMS provisioner (`pkg/plex/provision`) that converges metadata
  providers and libraries through PMS's API, with the server's own token.

**What changes:**
- **The watch:** the watcher adds `iptvproviders.clustarr.io`.
- **The provisioner:** it gains DVRs, run by cluster-plex's leader. That
  makes it the one writer of Plex's Live TV state.

**What it converges, for each block of each IPTVProvider that is `Ready`
and enabled.** The calls were recorded against the owner's PMS 1.43.4
(cluster-plex `docs/livetv-pms-calls.md`, fixtures in
`pkg/plex/api/testdata/livetv`). Each carries the server's `X-Plex-Token`.

1. **The device.**
   - **Find it:** `GET /media/grabbers/devices`, looking for the identifier
     `device://tv.plex.grabbers.hdhomerun/<block.deviceID>`.
   - **When none exists:** take the block's Lease (below), then register
     with `POST /media/grabbers/devices?uri=http://<localAddress>:<block.port>`.
     The discover call iptvtunerr makes first answers `size: 0` and is
     not needed.
   - **When one exists, it is never registered again.** Registering again
     is how duplicate and empty DVR rows arise (iptvtunerr's recovery
     notes).
   - **When Plex has the DeviceID at another address:** a real
     HDHomeRun, another proxy, or a person's edit. cluster-plex records
     `DVRDeviceIDTaken` and changes nothing.
2. **The DVR.**
   - **Under XEPG:** when `GET /livetv/dvrs` has no DVR on the device, it
     creates one with `POST /livetv/dvrs?language=<lang>&device=<device uuid>&lineup=<lineup>`.
     The lineup is `lineup://tv.plex.providers.epg.xmltv/<block.guideURL>#<block FriendlyName>`,
     escaped as one query value.
   - **Under PMS:** a Plex lineup depends on the owner's location, which
     the CR does not know. So it registers the device only and records
     `DVRNeedsLineup`. Once the owner has finished Plex's wizard, it adopts
     that DVR for steps 3 and 4.
3. **The channel map,** when the block's `lineupHash` differs from the
   hash it last saved:
   - `GET /livetv/epg/channelmap?device=<uuid>&lineup=<lineup id>` gives
     PMS's pairing of the device's channels with the lineup's;
   - then **one** `PUT /media/grabbers/devices/<key>/channelmap` enables
     every channel. Its query is `channelsEnabled=<every number>`, plus
     `channelMappingByKey[<number>]=<channel key>` and
     `channelMapping[<number>]=<lineup identifier>` for each channel.
   - The PUT replaces the whole map, so it is never batched. A batch would
     leave only the last batch's channels (iptvtunerr).
   - **Never empty or partial.** A save without the map cleared the
     device's map in the recording. A query over the 30,000-byte budget is
     refused before it is sent, with `DVRChannelMapTooLarge`; clustarr's
     blocks keep it from happening (§3.1).
   - This is how an activation on the Live TV page reaches Plex without the
     wizard.
4. **The guide:** when the block's `guideHash` changes, it calls
   `POST /livetv/dvrs/<key>/reloadGuide`.

**Never while a guide loads.** Every save and reload rebuilds the DVR's
guide database (activity `provider.epg.load` in `GET /activities`). In the
recording, a DVR deleted during such a rebuild deadlocked PMS until the pod
restarted. So cluster-plex deletes a DVR, and saves or reloads a guide
again, only when `/activities` lists no `provider.epg.load`. Otherwise it
waits for its next pass.

**Which DVRs it manages:** cluster-plex manages exactly the devices whose
identifier carries one of an IPTVProvider's block `deviceID`s, at its own
link-local address. Every other tuner and DVR in Plex is left alone.

**One owner per block.** Before it registers a block, the lease holder
takes that block's Lease, `livetv-<deviceID>` in clustarr's namespace,
holding it as its own installation (its PMS's machine identifier).
- **It renews the Lease every pass** while it manages the device.
- **A live Lease held by another installation** means another Plex owns
  the block. cluster-plex then registers nothing for it, and records
  `DVRBlockOwned`.
- **An expired Lease** can be taken over.
- **Why it matters:** two Plex servers on one block would each count the
  provider's tuners as their own.
- **RBAC:** cluster-plex needs `create`, `get` and `update` on Leases in
  clustarr's namespace. clustarr only reads them, to report
  `blocks[].owner`.

**The proxy in every Plex pod (§5.0).** On every pod, not only the lease
holder:
- the supervisor adds `plex.liveTV.localAddress` to `lo`;
- it watches the IPTVProviders read-only;
- it keeps one `proxy.TCP` per block, `<localAddress>:<port>` to
  `status.address`.

It starts them before PMS, and adds and removes them as blocks come and
go. A pod whose proxies are not up yet shows those devices offline. It
never shows another block's.

**A block that is gone** (removed by clustarr, §3.1) leaves a managed
device that no block names. cluster-plex deletes its DVR, then the device,
under the same guide-load rule. The channels it held already live in
another block's DVR.

**The other PMS pods.** Each PMS process caches its DVRs from its start,
and no call refreshes them (recorded). A DVR created or deleted on the
lease holder is invisible to the other pods until they restart, and
`plex-main` sends clients to any pod. So after a pass that created or
deleted a DVR, cluster-plex restarts PMS on each other pod, one at a time,
each answering again before the next.
- **Rate:** at most one round per 10 minutes. Changes made during the wait
  are picked up by the next round, so editing a hundred channels is one
  restart per pod.
- **Event:** `DVRPodsRestarted`.

Channel-map saves and guide reloads restart nothing. Whether the other pods
then show a changed map without a restart is still to be recorded.

**Ghost cleanup (iptvtunerr's "ghost-hunter" pattern):** on a managed
device, DVR rows beyond the first are deleted with
`DELETE /livetv/dvrs/<key>`. So are DVRs left on a managed device that no
longer exists. Being the one writer is what keeps such rows from coming
back.
- **Ghosts that reappear.** A DVR whose guide failed to load can drop out
  of the lease holder's list while its rows stay. A failed creation can
  leave such rows too. A PMS that starts later lists them again.
- **So the sweep runs every pass,** not only after cluster-plex's own
  changes. It deletes any DVR whose lineup is one of an IPTVProvider's
  guide URLs but which is not on a managed device.

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

**The recording (done 2026-10-07)** settled these points:
- **The largest channel map one PUT carries:** 454 channels, 32 KiB of
  request line (§3.1).
- **Rescans:** none needed. PMS re-reads a changed `lineup.json` and guide
  on `reloadGuide`, and step 3's map lists the new channels.
- **The shim:** XMLTV DVR creation needs plex-postgresql
  `v1.3.17-clusterplex.25`. Before it, the guide database's migration
  failed and PMS answered 500.
- **The JSON shapes:**
  - a DVR carries `lineupTitle` and `epgIdentifier`, and no `title`;
  - a device's `parentID` is a number, and it has no `name` (`model` holds
    the FriendlyName).

**Still to record** (the plan's probe task):
- that PMS registers a device at a link-local address, and two at once on
  one address with different ports;
- a stream played through a proxy port;
- whether the other pods show a changed channel map without a restart;
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
  - each block: its numbers, port, DeviceID and owner (§3.2). A Plex that
    cluster-plex does not manage has no link-local proxy, so phase 1
    serves managed Plex only;
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
  - **Blocks** (`pkg/iptv/blocks`), on numbers built by the real parser:
    - a first packing fills each block to 75%, and is deterministic;
    - every block is contiguous, and every number in block k+1 is above
      every number in block k;
    - a channel added inside a block stays in it, and later blocks keep
      their `start`;
    - an overflowing block splits at its median, and no other block's
      channels move;
    - an emptied block is removed, and nothing is merged;
    - long numbers (`12345.123`) close a block on the byte budget before
      the channel limit;
    - `MapBytes` equals the length of the query cluster-plex's
      `SaveChannelMap` builds, for the same channels;
    - more than 32 blocks is an error.
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
  - ports are allocated without collision across providers, and kept
    across spec edits and splits; a port claimed twice stays with the
    older provider;
  - an explicit `spec.device.id` that another block holds reads
    `DuplicateDeviceID`, and overlapping spans read `NumberRangeOverlap`,
    each keeping the last good blocks;
  - a duplicate number marks both channels `DuplicateNumber`, and a key
    gone from the playlist marks its channel `Missing`;
  - a playlist refresh that changes nothing writes no IPTVChannel status,
    shown across 3,000 channels;
  - deleting the provider removes its channels through the owner
    reference;
  - status rendered from a snapshot is complete: it is run against an
    object that already has status, and `managedFields` show one manager
    (the release gotchas);
  - the `livetv` Service guard (§6).
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
  - a Ready provider gets one device and one DVR per block;
  - a second run changes nothing;
  - a block's new `lineupHash` saves that DVR's channel map once, in one
    PUT, with 400 channels; a map over the byte budget is refused before
    it is sent;
  - a block's new `guideHash` reloads that DVR's guide once;
  - a new block gets a device and a DVR, and a removed block's DVR and
    device are deleted, never while `/activities` lists a guide load;
  - a pass that created or deleted a DVR restarts PMS on the other pods
    once, one at a time; a second pass within 10 minutes restarts nothing;
  - a provider under PMS gets a device and `DVRNeedsLineup`, and no DVR;
  - a deleted provider removes its DVR and device;
  - a DVR whose device no IPTVProvider names is never touched;
  - a duplicate DVR row on a managed device is deleted;
  - an existing device is never registered again;
  - a block whose Lease another installation holds is not registered, and
    records `DVRBlockOwned`;
  - a DeviceID Plex holds at another address records `DVRDeviceIDTaken`,
    and is left alone;
  - each pod's proxies follow the blocks: one listener per port on the
    link-local address, added and removed with the blocks.
- **e2e scenario 19** (`test/e2e/livetv_test.go`, with an `iptvstub`
  fixture serving an M3U, an MPEG-TS loop and an XMLTV guide):
  - create an IPTVProvider;
  - read `lineup.json` and `xmltv.xml` through the `livetv` Service, by
    path and by a block's port in `Host`;
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

## 11. As built (phase 1, clustarr)

Built on branch `livetv`, rebased onto `unify-manager-agent` after Wave 6
(plan `docs/superpowers/plans/2026-10-07-livetv-phase1.md`; its ledger holds
every ruling with its cost). Where this section and the sections above
disagree, this one describes the code.

**Behaviour the sections above do not state:**
- **The playlist (§4.1):** Xtream `m3u_plus` movie and series entries
  (`/movie/`, `/series/`) are skipped as they stream, `MaxEntries` counts
  the live entries kept, and the cap is 1 GiB. The owner's provider: 1.24
  million entries in 328 MiB, 28,527 live.
- **Guides (§5.1):** fetched only after the first playlist, whose channels
  choose what is parsed (`xmltv.ParseFunc`, auto-mapping in the same pass);
  a channel wanting an id a loaded guide was not parsed for refetches that
  guide. The stored guide is served until every guide has been fetched in
  this process, and a failed fetch never replaces it.
- **Dummy guides (§3):** an explicit `epg.dummy` is never auto-mapped.
- **A provider made invalid** (its Secret or key gone) keeps serving its
  last configuration, so deleting a Secret never takes a device off Plex;
  it reads Ready=False with the reason.
- **Finalizers** are added and removed by optimistic-lock merge patches. A
  channel the tuner has no snapshot entry for reads Ready=Unknown, reason
  `Pending`.
- **Blocks (§3.1, §6):** a split never moves a surviving block's port (the
  test pass found it moving the next block's).
- **The relay (§5.2)** has a fifth close reason, `panic`, and cuts off a
  viewer that falls a buffer behind.
- **Installers (§5.3):**
  - the manager's memory default rises by 128Mi (request and limit) instead
    of a `livetv.memory` added by Helm arithmetic;
  - `livetv.enabled: false` renders no Service, tuner port or policy, and
    `--livetv-bind-address=0`;
  - kustomize's NetworkPolicy is `config/livetv`, outside `config/manager`,
    whose `includeSelectors` labels would add clustarr's own labels to the
    policy's peer, and no Plex pod carries them;
  - the guards are `test/guards/livetv`, their own package, because
    `test/guards` does not build on `unify-manager-agent`.
- **E2e scenario 19** (`test/e2e/livetv_test.go`, the `iptv-stub` fixture)
  is written and type-checks, but has never run: `unify-manager-agent`'s
  `test/e2e` does not build.

**cluster-plex (§6.1), as built on its `livetv` branch** (its own ledger):
- A channel-map save starts `provider.epg.load`, like creating a DVR and
  reloading a guide. A pass starts at most one load, and reads
  `/activities` again before every delete, save, reload or DVR creation. No
  reload follows a save.
- What was saved is noted on the block's Lease
  (`livetv.clusterplex.io/saved`: the DVR and both hashes), so a new lease
  holder saves nothing again. A Lease that changes installation drops the
  note.
- A device whose DVR this PMS does not list, though the database ties it to
  one (`parentID`), gets no second DVR. The lease holder records
  `DVRListStale` and restarts its own PMS once.
- A Lease is renewed while this Plex has the block's device, whatever the
  provider's state, and while PMS cannot be read. A block another
  installation took while this Plex has it is a Warning; its DVR is left for
  a person to delete.
- A pod whose in-place restart fails is handed back to its health watch.
