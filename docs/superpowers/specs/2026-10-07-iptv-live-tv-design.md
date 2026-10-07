# Live TV: an IPTV proxy for Plex, after xTeVe

Status: design, for the owner's approval (2026-10-07).

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
- Every setting manageable from the UI, on the Settings page.
- A new **Live TV** page that does what xTeVe's tables do: filtering,
  mapping and channel selection.

**Assumed (correct any of these):**

- **One provider, one device:** each provider is one emulated HDHomeRun
  device, so Plex adds it as one DVR.
- **The 480 limit:** it applies to each device's lineup. Two providers make
  two DVRs of up to 480 channels each (open question 2).
- **Plex's job:** Plex records and transcodes. Clustarr only relays streams
  and serves the guide.
- **Network:** Plex reaches the proxy inside the cluster, as cluster-plex's
  PMS already reaches clustarr.

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

## 3. The resource: `IPTVProvider`

A new group, `livetv.clustarr.io/v1alpha1`, holds one kind: `IPTVProvider`
(short name `iptv`). Like a DownloadClient, it is reconciled into a
workload of its own: one tuner pod and a Service.

```go
type IPTVProviderSpec struct {
	// Enabled false scales the tuner to zero; Plex then sees the device gone.
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

	// Channels are the mapped channels: every candidate the owner activated
	// or customised. An inactive entry keeps its mapping for later.
	// +listType=map
	// +listMapKey=key
	// +kubebuilder:validation:MaxItems=1024
	// +kubebuilder:validation:XValidation:rule="self.filter(c, c.active).size() <= 480",message="Plex accepts at most 480 channels per tuner device"
	Channels []Channel `json:"channels,omitempty"`

	// Device names the emulated HDHomeRun.
	Device DeviceSpec `json:"device,omitempty"`

	// Stream tunes the relay.
	Stream StreamSpec `json:"stream,omitempty"`

	// Resources, NodeSelector, Tolerations: as DownloadClient's engine.
	Resources    corev1.ResourceRequirements `json:"resources,omitempty"`
	NodeSelector map[string]string           `json:"nodeSelector,omitempty"`
	Tolerations  []corev1.Toleration         `json:"tolerations,omitempty"`
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

type Channel struct {
	// Key is the playlist entry's identity (pkg/iptv.EntryKey, 4.1).
	// +kubebuilder:validation:MaxLength=64
	Key    string `json:"key"`
	Active bool   `json:"active,omitempty"`
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

### 3.1 The 480 limit, in three places

1. **Admission:** the CEL rule on `spec.channels` rejects a 481st active
   channel, from kubectl or the UI alike. `MaxItems=1024` bounds the CEL
   cost. Inactive mappings count toward 1024, not 480.
2. **The UI:** it shows `active / 480`. It refuses an activation that would
   pass 480, saying how many of the selection fit, before it writes
   anything (§6.2).
3. **The tuner:** it renders at most `livetv.PlexChannelLimit` (480)
   entries into `lineup.json`, whatever it reads. This is a defence, never
   the rule.

Rules too costly for CEL go to the controller, which reports them in
`Valid=False` with a reason. Its `lineup.json` drops an offending entry and
names it:
- **Duplicate numbers among active channels:** checking them pairwise over
  1024 entries is beyond CEL's cost budget.
- **`epg.guide` naming no guide.**

CEL covers the cheap rules: exactly one of `url` and `urlFrom`, exactly one
of `epg.guide` and `epg.dummy`, and `channelID` required with `guide`.

### 3.2 Status: two writers, split by field

The split is DownloadClient's controller/engine split (`app/grab/status`
`ControllerFields`/`EngineFields`). `app/tune/status` declares both sets,
and its `Patch` refuses any other manager.

- **The controller (`k8s.ManagerTunearr`):**
  - `conditions`: `Ready`, `Valid`, `TunerReady`;
  - `observedGeneration`;
  - `deviceID`: eight hex digits from a hash of namespace/name, so a
    recreated CR is the same device to Plex;
  - `address`: the Service's ClusterIP, which is what Plex's "enter address
    manually" takes;
  - `guideURL`: `http://<address>/xmltv.xml`, XEPG only.
- **The tuner (`k8s.ManagerTunearrTuner`):**
  - `playlist`: entries, groups, candidates, fetchedAt, hash, error;
  - `guides[]`: name, channels, programmes, fetchedAt, error; `MaxItems=16`;
  - `lineup`: active, unmapped (on dummy), missing (active keys absent from
    the playlist);
  - `tuners`: inUse, written on change and debounced to 5 s.

Each status write by the tuner follows a slow fetch, so it re-`Get`s
immediately before the apply. That is the lost-update gotcha, and the RSS
worker's "the poll closes the window" is the pattern to copy.

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
    spec, so the CR says what Plex gets.

### 4.4 The HDHomeRun surface: `pkg/iptv/hdhr`

- Pure renderers for `discover.json`, `lineup_status.json`, `lineup.json`
  and `device.xml`, taken from a `Device` and a `[]LineupEntry`.
- **Golden files:** each renderer is tested against a golden file built
  from a real HDHomeRun's answers, which xTeVe's source reproduces.
- **`ModelNumber`:** `HDTC-2US`, and `FirmwareName`: `hdhomeruntc_atsc`,
  the values Plex accepts from xTeVe.

## 5. The tuner pod: `app/tune/tuner`

`clustarr tunearr --role tuner --provider <ns>/<name>` runs one per
provider:
- **Deployment:** replicas 1, `strategy: Recreate`. Two pods would open two
  sets of upstream connections, and providers ban accounts for exceeding
  their connection limits.
- **Image:** the controller image, which is pure Go and distroless. It
  links no ffgo or purego.
- **Service:** port 80. The pod listens on 5004, HDHomeRun's stream port.

### 5.1 Startup and refresh

1. **Read the CR** from a cache scoped to one object (a field selector on
   its name).
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
   again on a spec change (channels, filters or guides). The pod rolls only
   when a pod-template field changes (resources, placement, image), through
   a config hash as the engines use.
5. **Refresh on schedule or by request:** fetches repeat on each source's
   `refresh`. The `livetv.clustarr.io/refresh` annotation, which the UI's
   "Refresh now" sets, forces one.

**Readiness:** the pod reads ready once the playlist has been fetched once.
Until then, `lineup.json` answers 503 and Plex retries.

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
- **HLS** (phase 3): an upstream answering `application/vnd.apple.mpegurl`
  is followed by polling its media playlist and feeding its MPEG-TS
  segments into the same ring.
  - TS segments concatenate into a valid stream, so no remux is needed.
  - fMP4 segments are refused as `unsupportedStream` on the stream's error
    counter.
  - Before phase 3, an HLS channel answers 502 with that reason.

This is xTeVe's own buffer, minus the ffmpeg and VLC options:
- one upstream connection per channel, however many Plex clients watch;
- memory bounded at `tuners × buffer` (64 MiB at 8 tuners);
- no disk.

### 5.3 The HTTP surface

| Path | Serves |
|---|---|
| `/discover.json`, `/lineup_status.json`, `/lineup.json`, `/device.xml`, `/lineup.post` | §2, from `pkg/iptv/hdhr` |
| `/xmltv.xml` | the XEPG guide (404 under PMS) |
| `/stream/<number>` | §5.2 |
| `/logo/<sha>` (phase 3) | cached channel logos, so Plex never hotlinks a provider host |
| `/healthz`, `/readyz`, `/metrics` | as every service |

**Authentication:** the surface has none, because Plex sends none. The
Service is ClusterIP only, and nothing in the chart routes an Ingress to
it: whoever reaches `/stream` watches on the owner's subscription. The
chart gains a NetworkPolicy, `tunearr.allowFrom` (namespace selectors),
off by default (open question 5).

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

## 6. The controller: `app/tune/controller/iptvprovider`

- Reconciles each IPTVProvider into:
  - a ServiceAccount-bound Deployment;
  - a Service, owned by the CR and never recreated on a spec edit, so the
    ClusterIP Plex was given stays.
- The pod spec is built in Go, so the engines' gotcha applies. It carries:
  - the installer-bound ServiceAccount;
  - `POD_NAMESPACE`, the NATS URL and UMASK;
  - `GOMEMLIMIT`, from the limit;
  - the Deployments' securityContext.
- **Guard test:** `TestTunearrTunersRunAsAnAccountTheInstallerBinds` holds
  the pod spec to what the chart creates, as
  `TestGrabarrEnginesRunAsAnAccountTheInstallerBinds` does.
- **The controller's own checks:**
  - it runs the §3.1 checks and writes `Valid`;
  - it reads `TunerReady` from the Deployment;
  - it writes `deviceID`, `address` and `guideURL`.
- It does no fetching. Fetching is the tuner's.

**Placement:**
- **On main:** a new service, `tunearr`, in `app/tune/`, with roles
  `controller` and `tuner`. `clustarr all` runs only the controller, just
  as it never runs grab engines.
- **On the unify-manager-agent branch:** the controller registers in
  `cmd/manager`, and the tuner is an agent role, like the grab engines. The
  package boundary is the same either way, so nothing here needs that
  branch first.

## 7. The UI

The UI writes only spec, Secrets and annotations, through `ui/actions`,
under `clustarr-ui`. That makes IPTVProvider the eleventh Settings kind, and
CLAUDE.md's invariant and `actions.Grants()` change from ten to eleven.
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
  - the address to enter in Plex (*Live TV & DVR → Set up → enter its
    network address manually*);
  - the XMLTV URL to give it under XEPG;
  - entries, candidates, active out of 480;
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
  - **The counter:** a badge reads **Active 312 / 480** and turns
    destructive at 480.
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
    - **Activate:** refused past 480, before any write, with "37 of 50
      fit";
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

- **The edit:** `actions.EditIPTVChannels(ctx, ref, mutate)` reads the CR,
  applies a pure `mutate([]Channel) []Channel`, and sends a JSON merge
  patch of `spec.channels` with `metadata.resourceVersion`. That is the
  precondition `ui/actions/season.go` already uses.
- **Why the whole list:** a merge patch replaces a list whole. So the
  precondition is what stops two tabs from overwriting each other.
- **On a 409 Conflict:**
  - it reads again and re-applies the same `mutate` once. A mutation is
    stated in keys, such as "activate these", so it applies to the new list
    as meant;
  - a second conflict is reported as "changed elsewhere, reload".
- **What a mutation keeps:**
  - The list stays sorted by key, for stable diffs.
  - Deactivating an entry that has no customisation removes it, so 1024 is
    rarely approached.
  - Deactivating a customised entry keeps it with `active: false`.
- **Above 480:** the apiserver's CEL message is shown as is, if the UI's
  own check was raced.

Filter, guide and settings edits are merge patches of their own lists,
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
  - an HLS-of-TS upstream concatenates (phase 3).
  - The tests run under `-race`.
- **`pkg/crdcheck`:**
  - 480 active channels are admitted, 481 are refused with the message;
  - 1,000 entries with 400 active are admitted;
  - `url` and `urlFrom` are exclusive;
  - `guide` and `dummy` are exclusive.
  - These run under `KUBEBUILDER_ASSETS`.
- **envtest:**
  - the controller renders the Deployment and Service;
  - `Valid=False` on a duplicate number;
  - status writes by both managers, asserted on `managedFields`, with each
    path run against an object that already has status (the release
    gotchas);
  - the pod-spec guard.
- **The UI:**
  - activation past 480 is refused with the fit count, and nothing is
    written;
  - a 409 is retried once with the same mutation;
  - the filter preview count;
  - the Missing rows;
  - `TestBothUICommandsWireEveryUIOption` covers the new
    `ui.Options.LiveTV`.
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
   - the API and CEL;
   - `pkg/iptv` (M3U, filter, XMLTV, hdhr);
   - the controller;
   - the tuner with the TS relay and fan-out;
   - the object store;
   - the chart, kustomize and RBAC;
   - e2e 19.
2. **The UI:** the Settings section and the Live TV page (Mapping, Filters,
   Guides), with bulk edits and the 480 checks.
3. **Breadth:**
   - HLS upstreams;
   - the logo cache (`/logo`, used in the lineup, the guide and the UI);
   - a `/playlist.m3u` output for players other than Plex (cheap, since the
     lineup is already rendered).

**Not planned:**
- recording (Plex's DVR does it);
- transcoding or an ffmpeg buffer;
- catch-up TV;
- Xtream Codes API logins (the `get.php` M3U URL covers them);
- SSDP discovery;
- multi-user authentication.

## 10. Open questions for the owner

1. **Names:** the service is `tunearr` (`app/tune/`), the group
   `livetv.clustarr.io`, the kind `IPTVProvider`. Tunarr is an unrelated
   project with a similar name. Is `tunearr` acceptable, or is another name
   better?
2. **The 480 limit per DVR:** this design assumes Plex's 480 applies to
   each tuner device's lineup, so two providers give two DVRs of 480. Is
   that what your PMS shows, or should 480 bound every provider together?
   A total limit cannot be CEL; it would be a controller condition, plus
   the UI's check.
3. **Adding the device to Plex:** this design shows the address to enter by
   hand. Should cluster-plex instead create the DVR through PMS's API
   (`POST /livetv/dvrs`, then the lineup and the XMLTV guide), as it seeds
   markers? That would be a separate change in cluster-plex.
4. **HLS:** does your provider serve MPEG-TS (`output=ts` on an Xtream
   `get.php`) or HLS? If HLS, the relay's HLS support moves from phase 3 to
   phase 1.
5. **Exposure:** should the chart ship the NetworkPolicy on, allowing only
   Plex's namespace, rather than off?
