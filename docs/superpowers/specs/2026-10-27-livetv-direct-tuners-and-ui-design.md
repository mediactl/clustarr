# Live TV: path-addressed tuners, and a Live TV section in the ui

Date: 2026-10-27. Status: approved by the owner on 2026-10-27 (§10 carries the answers). Amends
`2026-10-07-iptv-live-tv-design.md` (§5.0, §6.1, §7) and
`2026-10-20-iptv-xtream-ranges-design.md`. Where they disagree, this
design wins.

## 1. What the owner asked, and what the research found

The owner asked for three things (2026-10-27):

1. Redesign the Live TV stream and proxy relay so cluster-plex does no
   link-local port proxying. Give Plex the paths it expects, with the
   right endpoints, as the device's custom URL.
2. Investigate Dispatcharr (its multiple channel groups as multiple tuner
   endpoints for Plex) and build a similar UI in clustarr's ui package:
   a "Live TV" sidebar entry with Channels, M3U/EPG Manager, TV Guide and
   DVR.
3. Investigate Dispatcharr's lineuparr plugin for program images and
   alternate channel images, from a source like tv-logo/tv-logos.

What the research established (the exploration of both repos, Dispatcharr
through DeepWiki and its source, the lineuparr plugin's source and the
tv-logos README):

- **Plex reaches cluster Services from its network namespace.** ADR-0003's
  veth, default route and masquerade exist for exactly that, the recording
  in cluster-plex registered a device at a ClusterIP and PMS reported it
  alive, and PMS already fetches a path-prefixed guide
  (`http://10.96.55.197/livetv/probe/probe/xmltv.xml`) and clustarr's
  metadata providers at Service FQDNs. The link-local proxies were never
  about reachability. The 2026-10-07 design assumed Plex reads
  `/discover.json` at the root of an address and tells devices apart by
  host and port; the 2026-10-20 amendment made each proxy an HTTP reverse
  proxy that maps one port onto one device path and sets
  `X-Forwarded-Host` and `X-Forwarded-Prefix`. Mapping a port to a path
  is the proxies' whole remaining job.
- **clustarr's tuner already serves Dispatcharr-style devices.** Since the
  amendment, `/livetv/<ns>/<name>/devices/<deviceID>/discover.json`
  answers a `BaseURL` and `LineupURL` that carry the path, and every
  lineup entry's `URL` is `BaseURL/stream/<number>`. With no proxy headers
  a direct request already returns the prefixed form
  (`app/livetv/tuner/http.go`, `tuner_test.go:337-341`).
- **Dispatcharr does the same on one port.** Its HDHomeRun emulation lives
  under `/hdhr/<channel_profile>/[output_profile/<id>/]{discover.json,
  lineup.json,lineup_status.json}`, one device per channel profile, each
  with its own `DeviceID` (`dispatcharr-hdhr-<profile>`; not even hex) and
  `FriendlyName`, `BaseURL` built from `Host` or trusted `X-Forwarded-*`
  headers, stream URLs absolute at `/proxy/ts/stream/<uuid>`. Plex,
  Jellyfin, Emby and Channels DVR all add it by that URL.
- **What is unproven** is only PMS's handling of a device URI that carries
  a path: that `POST /media/grabbers/devices?uri=<uri-with-path>` fetches
  `<uri>/discover.json`, keeps the path in the stored `uri`, and uses
  `BaseURL`/`LineupURL` from the answer afterwards. Every recording so far
  registered a bare host. The gemini note in the owner's request says
  this is how Dispatcharr is added, and that only the *web form* strips a
  typed path on some PMS builds. cluster-plex registers devices through
  the API, not the form. This design still makes the proof its first
  task (§9, task 0).
- **lineuparr fetches no EPG and no program images.** It matches a
  provider's lineup JSON (shipped with the plugin) to the user's streams
  by name, assigns guide data Dispatcharr already holds, and assigns
  logos in three tiers: the EPG's `icon`, Dispatcharr's Logo manager, and
  tv-logos, by a GitHub contents listing of one country directory and a
  fuzzy name match (threshold 85) on the file name. tv-logos is a
  repository of 512 px PNGs, `countries/<country>/<name>-<cc>.png`
  (`-hz` for a horizontal variant), fetched by raw URL, CC BY-SA /
  BY-NC-SA, free for personal use. Program images in Dispatcharr come from
  the XMLTV `<icon>` elements of the EPG source (Schedules Direct or an
  XMLTV that carries them); it has no other source.
- **clustarr's ui has no Live TV page**, cannot read the `clustarr.io`
  group (no reader scheme, no role rule, no projection kind), and holds
  only the artwork object store. The 2026-10-07 design's §7 (a Settings
  section and a `/livetv` page) was never built.
- **clustarr has no DVR.** Plex records through its own DVR against the
  tuner. Dispatcharr records itself: an ffmpeg capture of its proxy
  stream into `/data/recordings`, remuxed to MKV, with series rules
  evaluated against its guide.

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. Plex adds each block of channels by one URL on the livetv Service,
  `http://<release>-livetv.<ns>.svc.cluster.local/hdhr/<deviceID>`, and
  nothing in a Plex pod proxies, listens or holds a link-local address
  for Live TV. Ports and `--livetv-port-range` go.
- G2. The tuner's device surface is what Plex, Jellyfin, Emby and Channels
  DVR expect from an HDHomeRun, as Dispatcharr serves it, plus the M3U
  and XMLTV outputs per device.
- G3. A "Live TV" section in the ui with three of Dispatcharr's pages,
  in clustarr's Plex look: Channels, M3U/EPG Manager, TV Guide. The DVR
  page is deferred (owner, 2026-10-27; §5.8).
- G4. Channel logos from tv-logos where the provider's are missing or
  poor, and program images where a guide carries none, chosen by the
  manager and visible in Plex and the ui.
- G5. Every invariant holds: the manager is the only writer of CRs and
  Secrets; the ui writes spec through `ui/actions` and never status;
  the ui's bus connection stays read-only.

**Non-goals**

- Transcoding or remuxing live streams (Dispatcharr's output profiles).
  A stream is relayed as the provider sends it; HLS upstreams stay
  unsupported, as today.
- Dispatcharr's VOD, Xtream Codes *output* (serving `player_api.php` to
  other clients), users and plugins.
- Serving the lineup over UDP discovery (SSDP). Plex is given the URL.
- Any migration of the link-local devices in PMS. The cutover deletes and
  re-creates them (§6.4), the repo's cold-turkey rule.

**Anti-goals**

- A second mechanism for the same thing: no port routing left beside path
  routing, no proxy kept "for compatibility".
- A ui that reaches PMS. The ui holds no Plex token and asks nothing of
  PMS; what it shows of Plex's DVR state comes from clustarr's resources.
- A guide page that parses 400 channels of XMLTV per request.

## 3. The tuner: path-addressed devices

### 3.1 URLs

The tuner keeps serving on the manager's `--livetv-bind-address` behind
the selector-less `<release>-livetv` Service whose EndpointSlice names
the leader. Its routes become:

| Path | Serves |
| --- | --- |
| `/hdhr/<deviceID>/discover.json` | the block's device description |
| `/hdhr/<deviceID>/lineup.json` | the block's lineup |
| `/hdhr/<deviceID>/lineup_status.json` | static, as today |
| `/hdhr/<deviceID>/lineup.post` | 200, empty |
| `/hdhr/<deviceID>/device.xml` | UPnP description |
| `/hdhr/<deviceID>/xmltv.xml` | the block's XEPG guide (404 under `epgSource: PMS`) |
| `/hdhr/<deviceID>/playlist.m3u` | the block's M3U |
| `/hdhr/<deviceID>/stream/<number>` | the provider's relay |
| `/livetv/<ns>/<name>/<file>?from=&to=` | any range of the provider's lineup, as today |

`DeviceID` is unique across the cluster (the controller's
`DuplicateDeviceID` check), so `/hdhr/<deviceID>` names one block of one
provider without the namespace or name. The `/livetv/<ns>/<name>/devices/
<deviceID>/…` form goes (cold turkey; it was the proxies' target). A
range's `DeviceID` stays `BlockDeviceID(ns, name, from)`, so a range
starting at a block's start shares its id; a range is still reached only
by its `/livetv/…?from=&to=` URL, never under `/hdhr`, so the two never
meet.

`BaseURL` is `scheme://<Host>/hdhr/<deviceID>` from the request's `Host`,
with `X-Forwarded-Host` and `X-Forwarded-Proto` honoured as today (a
Gateway in front of the tuner is an operator's choice; nothing in this
design needs one). `X-Forwarded-Prefix` is no longer read: the path is in
the request. `LineupURL` is `BaseURL/lineup.json`; every lineup entry's
`URL` is `BaseURL/stream/<number>`; the M3U's `url-tvg` is
`BaseURL/xmltv.xml`.

`discover.json` keeps clustarr's fields (`Manufacturer`, `ModelNumber`
`HDTC-2US`, `FirmwareName`, `FirmwareVersion`, `DeviceAuth`,
`TunerCount` with the three spares). `FriendlyName` stays
`<device.friendlyName or "Clustarr <name>"> <start>+`.

### 3.2 What a block records

`status.blocks[]` loses `port` and gains `uri`: the device's absolute URL
on the Service, `http://<address>/hdhr/<deviceID>`, where `status.address`
becomes the Service's FQDN, `<release>-livetv.<ns>.svc.cluster.local:80`
(the `.svc` short form could not be resolved by a PMS on another
cluster's DNS search path; the FQDN is what clustarr already hands PMS for
its metadata providers). `endpoint` is renamed `path`: `/hdhr/<deviceID>`.
`guidePath` stays. The CRD's printer column `PORTS` goes.

Blocks, their packing, sticky boundaries, DeviceIDs, map budget,
`dvr.maxChannels`, Lease owners, `lineupHash` and `guideHash` are
unchanged: a block is still one HDHomeRun device and one Plex DVR, because
PMS's channel-map PUT is still bounded.

### 3.3 Removed

- `--livetv-port-range`, `status.blocks[].port`, the port allocator and
  reservations in `app/livetv/controller/iptvprovider/blocks.go`, the
  port-held-by-another-provider rules.
- `X-Forwarded-Prefix` handling in `tuner.baseURL`.
- The `/livetv/<ns>/<name>/devices/<deviceID>/…` routes.

The chart and kustomize render the manager with `--livetv-bind-address`
and `--livetv-service` only; `test/guards/livetv`'s four guards that name
the port range or count three `--livetv-` args change with them.

### 3.4 The relay

Unchanged. It is per provider, shared by every viewer of a channel, with
the tuner limit, the reclaim rule, the keyframe join and the metrics the
2026-10-21 work gave it. The stream URL a client follows,
`BaseURL/stream/<number>`, lands on it as today.

## 4. cluster-plex: register the URL, nothing else

### 4.1 Removed

`pkg/plex/livetv/proxies.go` (the `ProxySet`), `linklocal_linux.go`
(`EnsureLinkLocal`), the `plex.liveTV.localAddress` value and its
validation, and the start-before-PMS wiring in `cmd/manager/livetv.go`.
No Plex pod runs anything for Live TV but the lease holder's converger.
The Plex container no longer needs to add an address to `lo`; whether
that lets it drop privilege is a question for cluster-plex's own ADRs,
not this design.

### 4.2 The converger

Per block, the lease holder:

1. registers the device with `POST /media/grabbers/devices?uri=<uri>`,
   where `uri` is `status.blocks[].uri`;
2. creates the DVR with the lineup
   `lineup://tv.plex.providers.epg.xmltv/<uri>/xmltv.xml#<FriendlyName>`;
3. PUTs the channel map and reloads the guide on the hashes, as today.

Three places keyed on host and port change:

- `sameEndpoint` compares the whole URL (scheme, host, port with 80
  implied, and path with a trailing slash trimmed).
- Rule M and the ghost sweep decide what is clustarr's by DeviceID, not
  by host: a PMS device whose DeviceID is a current block's is clustarr's
  wherever its URI points. One at the block's `uri` is converged as
  today; one at any other URI is clustarr's own at a stale address (the
  link-local cutover, a renamed release, a changed Service name) and is
  replaced: its DVRs and the device are deleted, then the block is
  registered at its `uri`. A device whose URI starts with
  `http://<status.address>/hdhr/` but whose DeviceID matches no current
  block, or a DVR whose lineup URL does with no device, is a ghost and
  is deleted. `DVRDeviceIDTaken` goes: it guarded a second clustarr
  installation registering the same ids with one PMS, which the ids'
  derivation (`ns/name/start`) makes a same-name install in the same
  PMS, and this stack has one. `localAddress` is gone, so the old rule's
  host test goes with it.
- `blockURI` reads `status.blocks[].uri`.

Everything else holds: the Lease per device, no writes while PMS loads a
guide, the one PUT under 32 KiB, the restart round after a DVR change,
stale grabs.

### 4.3 The one-time cutover

The link-local devices in PMS (`http://169.254.47.1:<port>`) carry the
same DeviceIDs as the blocks (the ids are sticky), at another URI, so the
first converger that runs this design replaces each under rule M: it
deletes the DVR and the device, then registers the block at its `uri`
and creates the DVR. No code names the link-local address: the stale-
address rule is general, which is what the cold-turkey rule (owner,
2026-10-07) asks for. PMS keeps no recording rules across that; the
owner re-creates them in Plex. The runbook (§9) says so. Task 0 also
records what PMS does when a device with a known DeviceID is added at a
second URI (a conflict, or a second device); the converger deletes
before it adds, so either answer works, and the recording pins it.

## 5. The ui: a Live TV section

### 5.1 Reading

The ui gains:

- the `clustarr.io/v1alpha1` scheme in `ui/reader.go` and `get`, `list`,
  `watch` on `iptvproviders` and `iptvchannels` in `ui_role.yaml`;
- `IPTVProvider` and `IPTVChannel` in the projection round
  (`ui/projection`), indexed by provider, with rows for the three pages
  and an SSE fan-out for the live parts (tuners in use, fetch state,
  channel readiness);
- a read-only watch on the `clustarr-livetv` object store, as `/art` has
  on `clustarr-artwork` (`pkg/events/objindex`), and `Get` closures over
  its objects. `TestUINeverWrites` already admits `Get`.

For the guide, the tuner writes one more object per provider,
`guide-index/<ns>/<name>`: a compact JSON of the provider's XEPG guide
(channels: number, name, logo, guide id; programmes: channel, start,
stop, title, sub-title, description, episode numbers, categories, image
URL), gzip, rewritten with the guide. The ui keeps the last parsed index
per provider in memory, keyed by the object's digest, and serves a time
window from it. The ui never parses XMLTV.

### 5.2 Writing

Through `ui/actions`, under `clustarr-ui`, with `Grants()` and
`ui_role.yaml` extended to match:

- `IPTVProvider`: create, patch (spec), delete; the eleventh Settings
  kind, so the form system (`ui/forms`, `ui/schema`) renders it. The
  credentials go into the Secret the provider names, created or patched
  and never read, as the DownloadClient form does.
- `IPTVChannel`: create, patch (spec), delete.
- `catalog.clustarr.io`-style annotations on the provider for "refresh
  now" (`livetv.clustarr.io/refresh: <time>`), which the controller
  honours and clears.

### 5.3 The sidebar

A "Live TV" `sidebar.MenuItem` under Library's, with a `MenuSub` of three
entries (a fourth, DVR, once §5.8 is wanted), swapped through htmx as the Library sub-menu is (`hx-get`,
`preload="mouseover"`, `hx-select="#page-body"`, the out-of-band crumbs
and search), each page answering `preloadable`:

| Entry | Path |
| --- | --- |
| Channels | `/livetv/channels` |
| M3U/EPG Manager | `/livetv/sources` |
| TV Guide | `/livetv/guide` |

`Chrome` gains the active Live TV entry, and `activeFor` learns the
titles.

### 5.4 Channels

Dispatcharr's split pane, in clustarr's glass: the channel table on the
left, the provider's playlist on the right.

**Channel table** (one row per `IPTVChannel`, every provider, sorted by
number): active switch, number (editable in place: a patch of
`spec.number`), logo (the resolved one, §7), name (editable; the override
`spec.name`), group, provider, EPG (the guide channel it maps to, with
a picker over the provider's guide channels, or "dummy"), Plex (which
block and DVR it is in, from `status.blocks`; the Lease owner), and row
actions: edit (a dialog over every spec field), copy stream URL,
delete. There is no in-browser player (§5.9). Filters: name, group, provider, active, "no guide", "no logo".
Bulk: enable, disable, delete, assign group, renumber from a start.
Header actions: copy the M3U, EPG and HDHR URLs of each block (the
"Links" popover).

**Playlist pane** (the provider's cached playlist object, paged and
searched server-side; the owner's provider lists 28,527 live entries):
group, name, tvg-id, logo, whether a channel exists for it, and "Add as
channel", which creates an `IPTVChannel` with the next free number. The
provider's filters are shown as chips; editing them is the Manager's job.

A table edit is one merge patch of the channel's spec under the read
`resourceVersion`, answered in place (`finishActionWith`), as the
library's card actions are.

### 5.5 M3U/EPG Manager

Dispatcharr's two stacked tables.

**Playlists** (one row per `IPTVProvider`): name, type (Xtream, or M3U
once §5.7 lands), server, status (Ready and its reason, coloured), status
message (the latest condition message; a fetch in progress shows its
phase), tuners (`status.tuners.total`, in use), account expiry (bold
within 7 days), entries, candidates, active, updated (`playlist.fetchedAt`),
enabled switch, actions: edit (the Settings form), delete, refresh now.
"Add playlist" opens the form.

**Guides** (one row per guide the provider fetches): name, type (XMLTV),
source URL (the panel's `xmltv.php`), status, channels, programmes,
updated, actions: refresh now. A provider with `epgSource: PMS` shows its
guide row as "Plex's", disabled.

Below both, a **Blocks** panel per provider: each block's numbers,
channel count, map bytes, DeviceID, URL (copyable), Plex owner and
hashes, and the Lease state cluster-plex writes.

### 5.6 TV Guide

Dispatcharr's grid, from the guide index (§5.1): a 120 px channel
column (logo, number, name), a time header of hours with "Today",
"Tomorrow" and weekday labels, 450 px per hour (7.5 px per minute), 90 px
rows, programme blocks placed by start and duration with title, an
`S01E02` badge, sub-title or description, times, `LIVE` and `NEW` badges,
a progress bar on the live one, and a teal "now" line moved once a
second by a small script. Past programmes are dimmed. Colours are the
Plex tokens, not Dispatcharr's.

The grid is server-rendered a window at a time: the ui answers
`/livetv/guide?provider=&from=&hours=` with the rows for that window,
and a scroll near either edge asks for the next window through htmx
(`hx-trigger="revealed"` on the edge sentinels), appended. Virtualising
hundreds of rows client-side is not needed for 400 channels at 90 px.

Filters: provider, group, search. A programme click opens a details
dialog (title, times, description, image, episode) with "Copy stream
URL" (§5.9); "Record" arrives with §5.8.

### 5.7 Playlist types beyond Xtream (my call)

Dispatcharr's manager takes plain M3U URLs and separate XMLTV sources.
clustarr's `IPTVProvider` is an Xtream account. To make the Manager an
M3U/EPG manager rather than an Xtream manager, the provider gains
`spec.type: Xtream | M3U` (default `Xtream`, so every existing provider
is unchanged): an `M3U` provider names `spec.playlistURL` and optional
`spec.guideURL` (both templates may name `{username}`/`{password}` from
the Secret, as the cached playlist does), fetches no account, and reads
its tuner limit from `spec.tuners` (default 1). Everything after the
fetch (filters, channels, blocks, guide, tuner, relay) is the same. This
is the one CRD addition; extra XMLTV sources per provider are a
non-goal until someone needs one.

### 5.8 DVR (deferred; owner, 2026-10-27)

Dispatcharr records itself. Plex is the DVR in this stack, and the ui
can neither read nor drive PMS, so a DVR page needs a recorder of
clustarr's own. The owner chose to skip it for now; this section records
the design it would take, so a later request starts here rather than
from nothing:

- Two kinds in `clustarr.io/v1alpha1`: `IPTVRecording` (channel ref,
  start, stop, padding, title and programme fields frozen from the guide,
  `status.phase` Scheduled, Recording, Completed, Failed, `status.path`,
  bytes) and `IPTVRecordingRule` (title match, channel or any, new
  episodes or all, days of week and time for a manual rule), their
  controllers in the manager.
- The recorder is a manager runnable beside the tuner, leader-only: at
  start minus padding it joins the provider's relay as a viewer, writes
  the MPEG-TS to `/data/recordings/<provider>/<title>/<title> - <start>.ts`
  through `pkg/fsops` (O_DIRECT on NFS, as every large write), takes a
  tuner like any viewer, and at stop closes the file. A recording is
  then a MediaFile under a RootFolder whose `kind` is `recordings`, so
  the transcode standard remuxes it to the MP4 layout and Plex sees it
  through the library as any file, with the guide's episode numbers.
- Series rules are evaluated against the guide index at each guide
  render; a rule schedules recordings and the page shows them under
  "Upcoming".
- The page is Dispatcharr's: a toolbar (New recording, search, channel,
  status), then "Currently recording", "Upcoming", "Previously recorded"
  as card grids, with the rules in a dialog.

Without the recorder, the DVR entry is not built: a page that only
restates Plex's DVR from `status.blocks[].owner` would be a dashboard
tile, not a DVR.

### 5.9 No playback in the browser (my call)

The tuner is a ClusterIP Service behind a NetworkPolicy that admits only
Plex's pods, every URL it hands out names the Service's cluster FQDN,
and raw MPEG-TS plays in no browser's `<video>` without an MSE demuxer.
Playing a channel in the ui would mean the ui proxying the tuner (a new
ui-to-manager HTTP coupling beside the bus, a policy change and a
demuxer in the bundle), and on the owner's one-connection account a
preview would take the tuner from Plex's viewer under the reclaim rule.
Dispatcharr's preview and the guide's "Watch" are therefore not built.
The Channels page and the guide dialog copy the stream URL, which plays
in VLC or mpv from inside the cluster network, or through a route an
operator adds to the tuner (the chart renders none; the NetworkPolicy's
`allowFrom` is where such a client is admitted). Watching is Plex's.

## 6. Logos and program images

**Built the same day, by another session (clustarr f2852be6, 2026-10-27),
before this design was written:** the tuner already looks programme
titles up as movies and series through the metadata gateway's search RPC
(`app/livetv/tuner/artwork.go`: `Options.Search`, bound in the manager;
background passes of 300 at 400 ms, never holding a guide build; the
answers cached in the Live TV store under `programme-art/<ns>/<name>`, a
hit 7 days, a miss shorter) and writes a poster found into the XEPG
guide's `<icon>`; `xmltv.Enricher` classifies programmes for Plex's
parser; and the tuner serves every channel's logo itself at
`logos/<number>.png` under each device and range (`logo.go`), recoloured
white when it is dark text on transparency, cached by source URL. The
design keeps all of that as built. The manager holding a requester for
that RPC is the pattern that commit chose; it stands.

What remains for Phase C:

### 6.1 Channel logos from tv-logos

The source of a channel's logo, in order: `spec.logo`; the provider's
`tvg-logo` when it answers an image (the fetch `logo.go` already makes);
a tv-logos match. The match is lineuparr's: the provider's
`spec.logos.countries` (ISO codes; default `us`) name tv-logos country
directories; the manager lists each directory through the GitHub
contents API once a day, cached in `clustarr-livetv/tv-logos/<country>`
(a 1000-entry listing is reported as truncated; a 403 keeps the cached
listing); a channel name is normalised as lineuparr does (`&` to "and",
punctuation out, a trailing HD/SD/UHD/4K/FHD/Network/Channel/TV word
out, the file's `-<cc>` and `-hz` suffixes out, hyphens to spaces) and
fuzzy-matched at 85 or better. The winner is written to
`IPTVChannel.status.logo` (`url`, `source`: spec, provider, tvlogos) by
the channel controller, and the tuner's logo pipeline reads it as the
source URL it fetches, recolours and serves, so Plex and the ui see one
logo through the same `logos/<number>.png`. The ui's logo picker lists
the country listing with a search box, and a pick patches `spec.logo`;
its thumbnails go through `/art/search`, whose allowlist gains
`raw.githubusercontent.com` restricted to the `/tv-logo/tv-logos/` path.

### 6.2 Program images

Built (above). The guide index of §5.1 carries each programme's image
URL from the same cache, so the ui's TV Guide shows what Plex shows.

## 7. Invariants and guards

- The manager writes every CR and Secret; the ui writes spec and Secrets
  through `ui/actions` only; `Grants()` and `ui_role.yaml` match
  (`TestUIRoleGrantsOnlyReadsAndActionWrites`).
- The ui's bus stays read-only: object-store `Get` and watches only
  (`TestUINeverWrites`).
- One writer per status: `k8s.ManagerLiveTV` for the provider,
  `ManagerLiveTVChannel` for the channel; `status.logo` is the channel
  controller's.
- Status lists stay capped (`blocks` 32, recordings' lists likewise).
- `test/guards/livetv` holds: no `--livetv-port-range` anywhere; the
  Service and NetworkPolicy unchanged otherwise; the tuner's `/hdhr/<id>`
  routes and absolute URLs; cluster-plex's registered URI equals
  `status.blocks[].uri`.
- The e2e scenario 19 is rewritten for the direct URL and stays written,
  not run, until Phase H.

## 8. Risks

- **PMS and a path in the device URI** (§1). If PMS strips the path or
  ignores `BaseURL`, the fallback is a hostname per block: an HTTPRoute
  per block on the cilium Gateway (`hdhr-<deviceID>.<domain>`) rewriting
  `/` to `/hdhr/<deviceID>/`, which the chart can render. Task 0 decides
  before anything else is built.
- **The cutover loses Plex's recording rules** (§4.3). Stated in the
  runbook; the owner schedules it.
- **GitHub's unauthenticated rate limit** for the tv-logos listing: one
  request per country per day; a 403 keeps the cached listing.
- **TMDB budget**: bounded by the artwork passes' pacing (300 titles at
  400 ms) and the gateway's limiter, as built.
- **The guide index size**: 400 channels times 7 days of programmes is a
  few MiB of JSON gzip; the ui parses it once per change.

## 9. Phases and the runbook

- **Task 0, the proof.** Extend cluster-plex's `hack/record-livetv.sh`
  to register the probe at a URI with a path and record whether PMS
  stores the path, fetches `<uri>/discover.json`, and follows
  `BaseURL`/`LineupURL`; then add a device with the same DeviceID at a
  second URI and record the answer (§4.3). The recordings land in
  `pkg/plex/api/testdata/livetv/`.
- **Phase A, direct tuners** (§3, §4): the tuner's routes and `uri`, the
  CRD change, the port removal, cluster-plex's converger and the
  removal of the proxies, the guards, the chart values. Runbook
  `docs/runbooks/livetv-direct-tuners-cutover.md`: deploy clustarr first
  (new `status.blocks[].uri`), then cluster-plex; the converger deletes
  the link-local devices and DVRs and creates the new ones; re-create
  the recording rules in Plex; check each DVR's channel map and guide.
- **Phase B, the ui** (§5.1-5.6, with §5.7 as its one CRD change).
- **Phase C, logos and images** (§6).
- **Phase D, the DVR** (§5.8): deferred by the owner; not planned.

Each phase is its own plan (`superpowers:writing-plans`), built
implementation-first with its tests named for the batch, as the repo
does since 2026-10-07.

## 10. The owner's answers (2026-10-27)

1. **The DVR:** skipped for now. §5.8 stays as the recorded design; the
   sidebar has three entries.
2. **The cutover:** approved, once the other changes are confirmed
   working: task 0's recording proves the path, Phase A is built and its
   guards pass, and the owner picks the deploy. The link-local devices
   are replaced at that deploy and the recording rules re-created in
   Plex afterwards.
3. **tv-logos' licence:** acceptable for this install.
4. **TMDB program images:** wanted (§6.2).

## 11. Calls I made

- `/hdhr/<deviceID>` as the device path, the DeviceID alone (it is
  cluster-unique) rather than `<ns>/<name>/<start>`.
- `status.address` becomes the Service FQDN.
- `X-Forwarded-Prefix` dropped; `X-Forwarded-Host` and `-Proto` kept.
- The guide index object, rather than the ui parsing XMLTV.
- `spec.type: Xtream | M3U` as the one CRD addition for the Manager.
- Server-rendered guide windows through htmx rather than a client grid.
- Logos resolved by the channel controller into `status.logo`, tv-logos
  listings cached a day, lineuparr's normalisation and threshold.
- Program images: as f2852be6 built them (the gateway's search RPC from
  the tuner, cached in the Live TV store); the guide index reuses the
  cache.
- A device with a block's DeviceID at another URI is replaced, so the
  cutover and a renamed Service need no code that names an old address;
  `DVRDeviceIDTaken` goes.
- No stream plays in the browser (§5.9).

## 12. As built, Phase A (2026-10-27)

Built on clustarr main and cluster-plex main; not deployed, and task 0's
PMS recording has not run (it waits for the owner's OK).

- clustarr, "feat(livetv): the tuner serves each block under
  /hdhr/<deviceID>, the path Plex is given": §3.1 as written.
  `tuner.DeviceEndpoint` takes the id alone.
- clustarr, "feat(livetv): a block publishes its path and URI on the livetv
  Service and takes no port": §3.2 and §3.3. `status.address` is
  `manager.ServiceAddress` (the FQDN on port 80). Blocks kept by a rule (an
  overlap, too many channels) get their path and URI too, so blocks an
  older manager wrote carry them at the next reconcile.
- clustarr, "chore(livetv): the installers render no port range and the
  guards hold it": §3.3's installers and §7's guard.
- cluster-plex, "livetv: Plex is given each block's URI on clustarr's livetv
  Service": §4.1 and §4.2. One difference from §4.2's wording: a device or a
  guide counts as under a livetv Service when its URL is under the `/hdhr/`
  of any Service address a live provider has named since the converger
  started, not only a current provider's, so a deleted provider's devices
  and DVRs still go (as the link-local rule removed them). After a
  cluster-plex restart with no provider left, such rows stay until a
  provider names the address again.
- cluster-plex, "chart: 0.6.0 -- no plex.liveTV.localAddress".
- The cutover is `docs/runbooks/livetv-direct-tuners-cutover.md`. The
  umbrella chart still pins cluster-plex 0.4.1; raising it waits for the
  0.6.0 chart's publication (the runbook's step 0.2).
