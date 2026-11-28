# Clustarr record: Live TV

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Live TV

IPTV for Plex, as xTeVe does it (2026-10-07,
`docs/superpowers/specs/2026-10-07-iptv-live-tv-design.md`, phase 1;
amended 2026-10-20 by `2026-10-20-iptv-xtream-ranges-design.md`).

**The provider.** An `IPTVProvider` (`clustarr.io`) is an Xtream Codes
account (`spec.type: Xtream`, the default), or a plain M3U playlist
(`spec.type: M3U`, 2026-10-27: `spec.playlistURL`, whose `{username}` and
`{password}` take a Secret's when `credentialsRef` names one; no account
and no panel, so its guide is `spec.guideURL` alone, else every channel's
dummy guide, `GuidesFetched` reading `NoGuide`; its tuners `spec.tuners`,
else 1). An Xtream provider requires two fields:
- `server`: a host, host:port or URL, https unless it says http.
- `credentialsRef`: a Secret with `username` and `password`, trimmed of
  stray whitespace. Every URL that is logged, put in status or an error
  passes `iptv.Redact`.

The tuner fetches three things from the panel:
- the playlist, `<server>/get.php?username&password&output=ts&type=m3u_plus`;
- the guide, `xmltv.php`, unless `spec.guideURL` names another XMLTV
  (owner, 2026-10-27: argon-tv reads iptv-epg.org's US guide, 500 MB,
  13,025 channels, programmes with images and categories). `{username}`
  and `{password}` in it take the account's. Each IPTVChannel's
  `spec.epg.channelID` names a channel of whichever guide is in use, so a
  new guide needs the lineup's ids remapped to it. The guide cache records
  its source, and a changed URL is fetched at once, cache or not. The
  panel's own xmltv.php is still fetched beside it, as a fallback for the
  channels that guide lacks or has no programme for (owner, 2026-10-27:
  Adult Swim and eight more), so such a channel keeps its panel guide id;
  the fallback's failure keeps its last good copy and is no guide error. A
  programme the guide already gives an image and a Movie or Series
  category is not looked up for artwork;
- the stream list, `player_api.php?action=get_live_streams`, after each
  playlist (owner, 2026-11-16): argon-tv's playlist carries no `tvg-id` on
  any entry, and the panel names a stream's guide channel only here
  (`epg_channel_id`). The tuner sets `m3u.Entry.GuideID` by the stream id
  an entry's URL ends in (`tuner/streams.go`), never `TvgID`, so keys and
  Plex pairing are untouched; an unmapped channel that maps by neither
  tvg-id nor name takes its own stream's id, then the id of an admitted
  entry of the same trimmed name (`tuner/twins.go`, `lineup.NameKey`: the
  lineup keeps the HD entry while only the 720p one may carry the id). A
  failed list keeps the last ids; the playlist cache carries them;
- the account, `player_api.php`. Its `max_connections` is the tuner limit
  unless `tuners` overrides it; `status.account` shows its status, expiry
  and connections.

The playlist and guide are fetched every `refresh` (default 24h). Both are
cached in the `clustarr-livetv` object store, and a restart serves the
caches and fetches only a stale one:
- the playlist cache (`PlaylistObject`) keeps every live entry, its stream
  URL templated with `{username}`/`{password}`, so it holds no credential;
- the guide cache (`GuideCacheObject`) keeps every channel, with programmes
  only for the mapped ones.

Optional fields are `filters` and `plexGuide` (below). An `IPTVChannel` is one channel turned on,
`key` (immutable, `t:<tvg-id>` or a name key) and `number`. The provider
controller (`clustarr-livetv`) and the channel controller
(`clustarr-livetv-channel`) are their status's sole writers. `pkg/iptv/m3u`
streams the playlist under a 1 GiB cap and skips Xtream `m3u_plus`'s movie
and series entries (`/movie/`, `/series/`): the owner's provider lists 1.24
million entries in 328 MiB, 28,527 of them live. PMS takes at most ~454
channels per device (one channel-map PUT under 32 KiB of request line), so
a provider's active channels are split into **blocks** (`pkg/iptv/blocks`):
contiguous numbers, sticky boundaries, a first packing filled to 75%, an
overflowing block split at its median, `dvr.maxChannels` (default 400, at
most 450) and a 30,000-byte map budget each, at most 32. Each block is one
HDHomeRun device and one Plex DVR. Each block gets:
- its own device ID (`sha256(ns/name/start)[:4]`, or `spec.device.id`,
  checked unique).
- its device's path, `/hdhr/<id>` (`status.blocks[].path`), and the URI
  Plex is given, `http://<status.address><path>` (`status.blocks[].uri`).
- an owner, read from cluster-plex's Lease `livetv-<device id>` into
  `status.blocks[].owner`.

Spans are disjoint across providers.

The tuner (`app/livetv/tuner`, the manager's `--livetv-bind-address`,
`:5004`) serves each device at its path, on the one `livetv` Service
(chart `<release>-livetv`; `status.address` is its cluster FQDN,
`<release>-livetv.<ns>.svc.cluster.local:80`):
- **Direct.** Plex is given each block's `status.blocks[].uri`,
  `http://<release>-livetv.<ns>.svc.cluster.local:80/hdhr/<deviceID>`;
  the tuner builds every URL it hands out from the request's Host and that
  path (`X-Forwarded-Host`/`-Proto` honoured; no prefix header), so nothing
  in a Plex pod proxies or listens for Live TV, and there are no ports per
  block (2026-10-27,
  `docs/superpowers/specs/2026-10-27-livetv-direct-tuners-and-ui-design.md`;
  until then cluster-plex proxied a link-local port per block).
- **Ranges.** `/livetv/<ns>/<name>/<file>?from=&to=` serves any range of
  the lineup, `playlist.m3u` included.
- **Change detection.** Lineups and guides carry an ETag, and cluster-plex
  still reads `lineupHash`/`guideHash`.

It also relays streams per provider
(`app/livetv/relay`): viewers of one channel share one upstream. Past
`tuners`, the newest tune takes a tuner: an unwatched stream's first, else
the stream whose viewer joined longest ago, whose viewers are cut off. A
channel taken that way may not take a watched stream's tuner back for 10 s
(`relay.Options.Reclaim`), which is the only time a stream answers 503. The
reason: PMS keeps reading the channel its client left for 30 s to 3 min. On
2026-10-21 the argon-tv account, which allows one stream whatever its
`max_connections` says (`spec.tuners: 1`), refused every channel change that
way, and Plex showed "failed to fetch play queue response". Before the cap,
two upstreams knocked each other off.

**`tuners` above what the provider really serves breaks every channel
change** (talos, 2026-11-16 to 2026-11-25; owner: "tv channels besides ABC
News and CNN seem to be throwing errors when tuning"). The account reports
`maxConnections: 3` and `spec.tuners` was raised from 1 to 3 on that reading
(2026-11-16), but tvload.win still serves one stream at a time: a second
connection ends the first, the relay reopens it (reason `eof`), which ends
the second, every 2.5 to 4.5 s for as long as both have a reader, and PMS
reads the channel its client left for 30 s to 3 min. So a tune from cold
worked (ABC NEWS, the first channel) and a tune made while another channel
was open failed. Nothing in the relay is at fault and no channel was: no
`eof` on the days at `tuners: 1`, one on every multi-channel session after,
and two curls through the tuner port reproduce it with nobody watching.
Back at `tuners: 1` (owner, 2026-11-25) three channels tuned in a row each
took the tuner cleanly ("a new tune took a watched channel's tuner").
`status.account.maxConnections` is what the panel says, not what its CDN
does: raise `tuners` only after two streams have been held side by side.

A viewer starts where a decoder can (2026-10-21, `relay.ring.Join`):

- within a connection's first 1 MiB (`relay.connectionStart`), at the
  connection's first byte;
- else at the latest keyframe, after copies of the latest PAT and PMT
  (`relay.tsIndex`: H.264 and HEVC parameter sets with an IDR picture or
  the random-access flag, or an MPEG-2 sequence header);
- else at the newest packet.

Before this, every viewer started at the newest packet. tvload.win's CDN opens a
stream with its tables, a keyframe and about 5 s of video in one burst, and
the relay dropped that start. PMS's decoder then began mid-GOP ("non-existing
PPS 0 referenced") and waited for the next keyframe. Each connection's time
to its first byte is `clustarr_livetv_upstream_first_byte_seconds`, plus an
Info log line; a refusal logs its HTTP status. For tvload.win that time is
about 1.8 s: 0.5 s to its 302, then 1.3 s at the CDN node.

Each channel's ring is the provider's `spec.stream.buffer`, 64 MiB by
default (2026-11-03; it was 8 MiB). tvload.win now opens a stream with about
a minute of backlog, 34 MB in 2-3 s, which a first viewer reads from the
connection's first byte. PMS's grab on the Pi fell a whole 8 MiB ring
behind, the relay cut it off (`relay.errSlow`), its response ended cleanly,
and PMS failed every fresh tune as "Recording failed. Please check your
tuner or antenna", while a tune that joined a warm stream (at the latest
keyframe) held. A viewer cut off that way is logged ("fell a whole buffer
behind") and counted (`clustarr_livetv_viewers_dropped_total`).

**A reopened upstream is spliced in forward, and a stalled one is reopened**
(2026-11-05, after the owner's "Plex Live TV session streams periodically
disconnect with an error": six live sessions in seven days ended on a
conversion failure, five of them an hour or two in, when the provider's
connection ended or stalled). The provider (tvload.win's CDN) closes a
stream's connection after about 65 minutes and opens every connection with
its own burst of backlog; the relay reopened it (`pump`, `Retries` 3,
`Backoff` 1 s) and appended the new connection's bytes to the viewers'
stream as they came, so PMS's recorder and cluster-plex's GPU conversion
saw time step back and the conversion's TS muxer refused it ("non
monotonically increasing dts", `av_interleaved_write_frame: Invalid
argument`), which PMS reports as "Recording failed. Please check your tuner
or antenna". Now `ring.Realign` starts a catch-up (`relay.catchUp`) when a
video presentation time was delivered before: the reopened connection's
packets are indexed but dropped until a keyframe whose PTS (`tsIndex.pts`,
`pesPTS`, wrap-aware `ptsAhead`) is past the last delivered, then its
tables and that keyframe on are stored, so an attached viewer sees a short
forward gap and never a step back; a connection without TS tables within
`catchUpProbe` (1 MiB) passes through as it came, and one that never
passes the time within the ring's capacity splices at its next keyframe
("livetv: upstream reopened and spliced", `droppedBytes`, `aligned`).
And a connection that sends nothing for `relay.Options.StallTimeout`
(15 s) is dropped and reopened as one that closed is ("livetv: upstream
stalled; reopening", reason `timeout`): the stream transport has only a
header timeout, so a stalled connection was never reopened and its viewers
starved until PMS's recorder (`-rw_timeout` 30 s) or the conversion's own
stall watch (30 s, "input stalled") gave the session up. cluster-plex's
cutter drops a packet whose decode time is not past its stream's last
instead of failing the job (its `pkg/video/pipeline/cut.go`), the belt
under this.

**Streams are paced: held 30 s behind the upstream and released in real
time** (2026-11-27, owner: "the server sends a backlog at connect and then
chunks every ~10 s. This behavior is very noticeable on the Plex server.
Can we mitigate the sputtering and create a more reliable, stable streaming
connection for Plex to consume?"). tvload.win's CDN (CDN77, a 302 from the
panel) sends a stream as whole HLS segments of about 10 s, each in under a
second, with up to 21 s of no media between them; bytes still trickle in the
silences, so the longest byte gap was 6.2 s while the media fell 23 s behind
the wall clock at worst (three minutes of CNN, two captures, PCR continuous
throughout). The relay wrote every byte to its viewers as it came, so PMS
sat at the live edge of what it had and stalled at every silence; a viewer
joining a warm channel started at the newest keyframe with nothing in hand.
Now each ring has a release point (`relay/pace.go`, `pacer`): viewers read
only up to it, and it moves at real time by the stream's PCR (the PMT's PCR
PID, `tsIndex.pcrs`, thinned to one mark per 100 ms), never more than
`relay.Options.Hold` (`DefaultHold`, 30 s) behind the newest PCR and never
past it. So the opening backlog fills the cushion at once (its part beyond
the hold goes out at once, as before), the clock drains the cushion while
the upstream is silent, and each segment refills it; a clock that reaches
the newest PCR waits there and paces the next burst from its arrival. A
replay of the captured timeline ran dry five times with a 20 s hold, never
with 25 s (1.9 s left at worst), and kept 6.9 s with 30 s, which is also the
owner's figure ("Something like 30sec or however long is needed to correct
for upstream stuttering, reconnections"). The cost is 30 s of latency behind
the provider's own live edge. A PCR step back or of more than 2 s is a
discontinuity and paces as no time; a stream without PCRs, or `Hold < 0`,
passes through as it comes. A viewer joins at the latest keyframe at or
before the release point, so it shares the cushion; right after a reopen,
while the release point is still in the earlier connection, it may start at
that connection's keyframes (`ring.keys` spans connections) rather than wait
out the hold. A closed ring releases everything at once. A drought with a
viewer attached logs "livetv: stream ran dry; its viewers wait for the
upstream" and counts `clustarr_livetv_streams_dry_total`. Proven by
`TestABurstyUpstreamReachesItsViewerSteadily` (600 ms silences reach the
viewer unpaced, under 350 ms gaps paced), not yet on talos.

**Only the correction window stays resident** (owner, 2026-11-27: "can we
minimize the buffer retained as much as possible making it ephemeral
outside a failure correction timeframe"). A ring's buffer was resident in
full once the writer had wrapped it (64 MiB after about 100 s of a 5 Mbit/s
stream). Now `ring.trimLocked` gives its pages back (`offheap.Discard`,
`MADV_DONTNEED`, at least 1 MiB at a time) up to the oldest byte a viewer may
still read: the slowest viewer's next byte (viewers are `*reader` handles
from `Join` to `Leave`), or where a viewer joining now would start, whichever
is earlier. A paced stream so holds its hold and a GOP (about 17 MB at
4.5 Mbit/s); the capacity, `spec.stream.buffer`, is the cap a slow first
viewer reading the opening backlog may fill, and a viewer that falls a whole
buffer behind is cut off as before. Only a mapped buffer of whole pages is
trimmed.

**A connection that pads without media is reopened** (2026-11-27). The
CDN fills the silences between segments with null packets (PID 0x1fff: 162
of them in one minute's silences, with a few segment-tail packets), so
`StallTimeout`, which counts bytes, never sees a connection that keeps
padding with no media behind it, and the paced viewers would drain the
cushion and then starve until PMS's own 30 s timeout. `ring.mediaAt` is when
the current connection last delivered a PCR (stored, or replayed during a
catch-up), and `Relay.watchMedia` drops a connection silent for
`Options.MediaStallTimeout` (25 s, past the 21 s silences seen; with a
reopen's 3 s it fits the 30 s hold) and reopens it as a stalled one
("livetv: upstream sends no media; reopening", reason `timeout`). A stream
that never showed a PCR is never judged. `TestAnUpstreamPaddingWithoutMediaIsReopened`.

discover.json advertises three tuners more than `tuners`
(`tuner.SpareTuners`). The reason: on 2026-10-20 a grab PMS never ended held
the one tuner for hours while the relay streamed nothing, and every tune
waited 300 s and failed. A stream's upstream must answer within 10 s.
Playlists and guides go through their own client (`tuner.Options.Fetch`),
which waits up to 2 min, because a panel builds its whole m3u_plus before it
answers. On 2026-10-20 tvload.win took 13 s for 588 MB, so every fetch
failed. At a restart only the guide was restored, never the playlist, so
the next manager restart left the lineup answering 503 and every tune 404,
which Plex shows as "Could not tune channel". The playlist cache replaces
the Secret kept for a day to bridge that. cluster-plex (its `livetv` branch) registers
the devices and DVRs, saves each map in one PUT, never deletes, saves,
reloads or creates while PMS loads a guide (a delete then deadlocked PMS),
and restarts PMS on the other pods after a DVR change, since each PMS reads
its DVRs once. The installers render the Service and a NetworkPolicy
(`livetv.networkPolicy`) admitting the tuner port only from Plex's pods
while every other manager port stays open (`clustarr.managerPorts`; held by
`test/guards/livetv`, its own package while `test/guards` does not build);
kustomize's policy is `config/livetv`, outside `config/manager`, whose
`includeSelectors` labels would add clustarr's labels to the peer and admit
no Plex pod. The manager's memory default carries 128Mi for the stream
buffers (the cap: since 2026-11-27 only the correction window is resident,
above), which live outside the Go heap (`pkg/offheap`, 2026-11-03: one
64 MiB ring was a fifth of the manager's live heap and of its GC
headroom) and go back to the kernel once a ring is closed and its last
viewer has left.

**Plex's own guide** (owner, 2026-11-03: "Add support for mapping TV
channels to Plex Native Guide (Gracenote)";
`docs/superpowers/specs/2026-11-03-livetv-plex-native-guide-design.md`):
an IPTVProvider's `spec.plexGuide` names a Plex cloud lineup (`lineup`, its
24-hex id; on talos DIRECTV Mobile, `657c8183afd0f02cd353633f`, the owner's
"DIRECTV - Mobile-Pensacola (Ft. Walton Beach)") and a Secret key holding a
Plex account token (`credentialsRef`). The tuner fetches the lineup's title
and stations from `epg.provider.plex.tv` (`/lineups/<id>`,
`/lineups/<id>/channels`, `X-Plex-Token`; at most 1,000 stations listed,
the 1,009-station lineup cut, no paging by query), caches it as
`plex-lineup/<ns>/<name>`, and each render pairs the active channels with
stations (`pkg/iptv/plexguide.Pair`): `spec.epg.plex.off`, a timeshifted
channel (Plex cannot shift its guide), a hand-picked station
(`spec.epg.plex.station`, a gridKey, the same in every lineup), the last
pairing while its station is listed or absent under 7 days
(`MissingGrace`), then the call sign a guide id names (`ABCEast_WABC.us`
is WABC; the New York flagships 1001-1004 sit on DIRECTV's distant network
feeds 390-398), the name or guide id normalized, and a short alias table; a
station's feeds under one call sign are one (WEAR, WEARDT), a cable
network's feeds under unrelated call signs are one when titled alike (FX,
FXHD), never a local station's, and the HD feed is served; several stations
named is `Ambiguous`, never a guess; one channel per station (a hand pick,
else the lower number; the other `Taken`). The channel controller writes it
to `status.plex` (`match`: Manual, Automatic, Ambiguous, Taken,
Timeshifted, NotInLineup, Off, NoLineup); the provider's
`status.plexGuide` counts them, condition `PlexGuideReady`. Only with
`plexGuide.enabled` do the blocks move: they hold the `Manual` and
`Automatic` channels alone, under device ids of their own
(`hdhr.BlockDeviceID(ns, name, "plex/"+lineupID+"/"+start)`, so cluster-plex replaces
the XMLTV devices and DVRs, recording rules included, rather than changing
a lineup in place), `status.blocks[].lineup` names the cloud lineup, each
serves `/hdhr/<id>/plexmap.json` (its numbers' lineup channel ids and
numbers) and no guide, and cluster-plex creates the DVR on the lineup,
saves that map and never reloads its guide; every other channel is on no
DVR (owner: "Drop out of Plex"), and Plex shows DIRECTV's numbers. Until a
lineup is held the published blocks stay (`LineupPending`) and the tuner
serves each channel's recorded pairing. Without `enabled` the pairing is a
dry run the Channels page shows (station, number, or why off Plex, with a
station picker and an "Off Plex" filter). On the owner's 257 channels 167
pair. **Live on talos since 2026-11-04 09:20Z** (DVR 32, device 1AED97B8,
167 channels): the first cutover that morning (03:16Z) was undone because
every cloud-guide load failed "Error 25 when binding to pos 1 on bulk
operations" then "EPG[cloud]: Failed to load EPG", after which PMS replaced
the DVR's map with its own pairing (NBC on CNBC 355). The cause was the
plex-postgresql shim, not PMS: since upstream `ff63333` (2026-01-09, when
the library was the only database it served) `prepare_real_sqlite_stmt`
simplified and then blocked (`SELECT 1 WHERE 0`) any statement naming an
`fts4_` table or a `MATCH` on *every* handle, and PMS's cloud loader
prepares `select ... join fts4_metadata_titles_icu ... match ?` on its
genuine-SQLite `-loading.db` (where PMS registers its own `collating`
tokenizer through the passed-through `fts3_tokenizer`), so the first bind
hit SQLITE_RANGE. Shim `v1.3.17-clusterplex.44` (`handle_is_pg_backed`,
`fts_for_sqlite`) rewrites or refuses FTS only on a handle the shim serves
from PostgreSQL, and its dummy shadow carries every parameter past the C
shim's 4 KiB cap (about 225 placeholders). Measured then: a cloud load is
13 s for one channel and 21 min for 167 (two 7-day steps, 60,398
airings), against 10-13 min for the XMLTV guide's 257 channels, so the
saving is in reloads, not loads (cluster-plex never reloads a cloud guide;
XMLTV reloaded four times a day). The load is still PMS's SQLite
`-loading.db` (the 2026-10-21 rename-hook design: an in-process load, a
copy at the rename), and cluster-plex's copy of it into `epg_<uuid>` is
354,116 rows in 35 s, held in the shim's `rename()` hook; PMS serves the
guide from PostgreSQL from its reopen. Each DVR creation logs one "Failed
to load EPG" on PMS's auto map, before our map is saved. **A block's device
is published only once the tuner serves it** (`withBlocks`, `allServed`,
`tuner.BlockState.DeviceID`; clustarr 3a646c10): the controller applies a
plan and writes status in one reconcile, while the render serves a new
`/hdhr/<id>` only when it ends (85 s for 257 channels), so cluster-plex
had registered a device PMS could not fetch ("POST /media/grabbers/devices:
plex: not found") and deleted the XMLTV DVR first, leaving Plex without
Live TV for 90 s; cluster-plex 40dd555 also holds the removal of devices no
block names while a block's add fails. **A device PMS marks dead is
discovered again by cluster-plex** (87bdd72, `DiscoverDevice`, once a
minute while it stays dead, Event `DVRDeviceRevived`): PMS refreshes its
devices at its start and on a schedule, marks one dead when the refresh
fails and never retries it ("Device not found. Ensure your device is
powered on and connected to your network"), and a manager restart leaves
every `/hdhr/<id>` unserved for the length of the tuner's first render
(85 s), which is how rev 213's restart lost Live TV at 09:52Z until a
discover by hand. **The copy into PostgreSQL happens before PMS's rename**
since shim `v1.3.17-clusterplex.45` (its `rename()` hook, the shim half of
cluster-plex's 2026-10-21 design, built the same day; the manager's
`guideSwapDeadline` 230 s, the hook's wait 240 s): the shim decides SQLite
or PostgreSQL when PMS opens a guide, so until then a new DVR's first guide
was served from its SQLite file until PMS next reopened it (DVR 32,
09:43-09:52Z) and each later load was served stale for the minute the poll
took; a forced reload at 10:30Z proved the order (the manager's "the shim
asks for a guide load's copy before its rename", the copy, then PMS reading
the new load from PostgreSQL). **PMS never refreshed the cloud guide incrementally, for two reasons of
ours** (owner, 2026-11-04: "why we never seem to get any incremental guide
refreshes"): PMS has an incremental refresh, run by its nightly
`RefreshEpgGuides` butler task and by itself at boot when the DVR's
provider row (`plex.media_provider_resources`, `pv:lastChunkEndedAt`,
`pv:timeOfLastRefresh`) reads stale, but cluster-plex turns every butler
task off, so the nightly one never ran; and when a refresh does run it
first streams the whole guide it holds through one SELECT (98,180 rows
for 248 channels, 657 ms on the server) and works each row as it arrives,
longer than the plex-postgresql shim's five-minute statement timeout, so
PostgreSQL cancelled it ("canceling statement due to statement timeout"),
PMS's loader died with an uncaught exception, its "Refreshing guide data,
Initializing 0%" activity sat as a ghost until the next restart, and it
retried every five minutes. Shim `v1.3.17-clusterplex.46` runs a guide's
connection with no statement timeout, and .47-.49 (talos rev 222,
cluster-plex 8459523) fix the three translator misses the refresh's own
statements then hit: an ICU `COLLATE` after an `IN (...)` list, an escaped
`''` inside a literal, and backticks under a collation (`preprocess.rs`,
`quotes.rs`). **A refresh is not atomic** (2026-11-04 19:34Z, the owner's
cancel from Plex Web, `DELETE /livetv/dvrs/32/reloadGuide`, 83 of 248
channels into the first 2-day chunk): PMS empties the chunk's window for
every channel before it refills per channel, so the cancel left 165
channels with no airings for the rest of the day and the next, and PMS
wrote the provider row as refreshed through 11-18 anyway (a complete
refresh writes it too, and the DVR's `refreshedAt` with it, so neither
says the guide is whole); the repair is to stale the row and restart PMS
(the re-run completed in 22 min under .49), and the finding is the case for a
manager-written guide (cluster-plex's
`docs/superpowers/specs/2026-11-04-cloud-guide-loader-design.md`, parked).
A load from scratch (a map save,
`reloadGuide`) reads nothing back and was never affected: it is two 7-day
steps of one `GET /grid/bulk?channelGridKey=&startDate=&endDate=` per
channel and one `GET /library/metadata/<id>` per programme, serial, with
PMS's summary `Step 2/2 (network: 512.6 sec, database: 1651.1 sec)` for
248 channels. **Plex's limit for a cloud block is the map
budget** (`blocks.DefaultMaxBytes`, 30,000 bytes; about 116 bytes per
cloud channel key, so about 255 channels per DVR), not `dvr.maxChannels`.
The DIRECTV mapping was extended by hand on 2026-11-04 (owner: "map out the
remaining direct TV channels"): 65 IPTVChannels paired through
`spec.epg.plex.station` (the Pensacola/Mobile and Los Angeles affiliates,
CSPAN 1-3, CNNI, premium movie variants, shopping, Music Choice, F1,
MotoGP, team feeds), then 12 more at 12:55Z whose stations the lineup lists under other names (ION Television HD; BBC News (North America), the former BBC World News; MSG National and YES Network's national feed; the outer-market feeds of NBC Sports Boston and Bay Area, Monumental and Space City; the blackout feeds of SNY, SportsNet Pittsburgh, SportsNet LA and Altitude, the only ones listed, whose blacked-out games differ), and at 13:05Z PBS, CW and MyNetworkTV (owner: "Why is PBS not in the lineup?"): the lineup carries them as stations named by call sign alone -- WEIQ 42 (Alabama Public Television), WDCW 394 (DIRECTV's distant CW feed from Washington) and WFGX 35 (Pensacola's MyNetworkTV) -- while argon's streams are WNET, WPIX and WWOR New York, so the matcher found no call sign and the hand picks give each the network's own hours right (the Eastern feed on a Central guide lands on the same local clock) and local programmes wrong; PBS Kids has no station in any DIRECTV lineup (it is a broadcast subchannel, WSRE-DT3 in the OTA listings), so it stays off; 248 on the one block; `status.plexGuide.notInLineup` counts the channels no station matched, a station listed under another name included, so read those channels against the station list before trusting the count, and the automatic matcher could learn the renames and the regional feed suffixes (not built); a channel is served only when
some provider filter admits its playlist entry (51 read Missing, "not a
candidate ... filtered out", until argon-tv admitted their groups), and the
remaining 740-odd unpaired stations have no argon stream (DIRECTV
placeholders, PPV, regional sports, out-of-market locals); of the lineups
Plex offers the owner's market, DIRECTV Mobile has the most
argon-compatible networks (230, against U-verse 209, DISH 199, Cox 186,
Mediacom 166, one network counted once across its SD and HD feeds), and
DIRECTV is the provider with the most overlap nationally (213-218 networks
in each of its market lineups, the markets differing only in locals;
U-verse 203, DISH 194, Spectrum 189, Fios 188, Cox 180, Xfinity 175 over
108 lineups of 20 major markets, 2026-11-04). **A
paired channel's number is its station's DIRECTV number** (owner,
2026-11-04: "channel numbers should match the direct tv mobile provided by
plex instead of the 1000+ we have now"): `spec.number` is
`status.plex.vcn` (3 WEAR ... 9572), channels off Plex park at 10000 plus
their old number, and the block's sticky start (1001) keeps its device id
and DVR through a renumbering. `spec.epgSource`
(XEPG or PMS, the latter Plex's setup wizard by hand) is gone.

**Streams declare their language** (owner, 2026-10-27: Plex showed ESPN's
audio as Unknown; its PMT said `und` where the Weather Channel's said
`eng`): the relay rewrites every connection's PMT on its way into the ring
(`relay.tsRewriter`, `app/livetv/relay/pmt.go`) so each audio and subtitle
track carries an ISO 639 descriptor -- an undetermined one (`und`, `mis`,
`zxx`) becomes the provider's `spec.stream.language` (default `eng`;
`pkg/iptv.ISO6392` turns a BCP-47 tag into the three letters), a missing
one is added when the packet has room (stuffing after the section, else
taken from the adaptation field), a DVB subtitling or teletext descriptor's
own codes are rewritten too, and an IPTVChannel's `spec.language` is forced
over whatever the stream says; the section's CRC is recomputed. **The
guide is enriched for Plex's parser** (`xmltv.Enricher`,
`pkg/iptv/xmltv/enrich.go`; the provider's programmes carry a title and a
description and nothing else, 0 icons, categories or languages in 323,339):
a sports channel's programmes (IPTVChannel `spec.group`) are category
Sports; a live broadcast -- a "Live:" title (the provider's own marker,
5,743 of its programmes, "Live: SportsCenter" among them) or a matchup in
the title or description -- carries `<live/>` and `<new/>`, and one that
also names a matchup or a sport is a sports event: "Sports event" and its
sport, the matchup as its sub-title ("Dallas Cowboys at Philadelphia Eagles"
from "the Eagles host the Cowboys"), so a live studio desk is flagged live
without reaching Plex's Upcoming Sports hub; every programme without an
episode number gets a Gracenote-shaped `dd_progid` (SP from the channel and
start for a sports event, so ten channels' "NFL Football" are ten games; EP from the
title, sub-title and description otherwise, so a repeat is the same
episode -- unless the built guide airs that listing more than 3 times or in
its channel's slot on another day (`xmltv.repeats`, counted by
`BuildGuideWith` before it enriches), when each airing is its own episode
of the one show: the provider gives most programmes a series' description
and no episode title, and on 2026-10-28 one "Paid Programming" id carried
1,077 airings on 70 channels, so every What's On hub that showed it listed
them all and took 20 to 46 s; MV for a known movie, unless the title airs
more than 14 times), which keeps a one-off out of Plex's Movies
while a movie channel's unknown film keeps the movie fallback; a news
channel's programmes are News, a kids channel's Children; a children's
programme, the source's or a kids channel's, is also Kids (owner,
2026-11-16: PMS lists Kids On Now and Kids On Later for an XMLTV guide only
when a genre named Kids exists, and offers the sports, news and movies
on-now hubs to a cloud guide alone, whatever the guide's tags say); a source's
placeholder listing is left out of the built guide (owner, 2026-11-16,
`xmltv.Filler`: "Paid Programming", "SIGN OFF", "Channel No Longer
Available", "No EVENT Today", "No Data", "To Be Announced" and their like,
by title; 3,786 of talos's 85,487 programmes, 27 of the 464 on at the
moment, and 19 channels left with no listing): nothing but the airing
itself puts a programme in Plex's What's On hubs, so there is no tag to
take from it instead; every programme
says one of `<live/>`, `<new/>` and `<previously-shown/>` (owner,
2026-11-16: the iOS app badged every What's On listing Live): live is the
source's or a live event's, new the source's (its `<premiere>` gets the
`<new/>` it implies), anything else previously shown, as every dummy block
is: PMS's reader
and cluster-plex's builder take an episode without it for a premiere,
which Plex's apps badge (Plex Web "New", iOS "Live") and its What's On hubs
list first, and since the dd_progid makes nearly every programme an
episode, 57% of talos's airings had been premieres; PMS keeps no `<live/>`
at all, so premiere is the only badge a guide can set; the output keeps
XMLTV's element order, and nothing the source said is doubled or dropped,
except that an episode iptv-epg.org numbers its own way -- "S26 E32" in an
`<episode-num>` naming no system, "S26 E32 — Justice for Nikki" as the
description's first line -- is said in XMLTV's own fields: the title as
`<sub-title>`, the numbers as `xmltv_ns` and `onscreen`, the description
without that line (owner, 2026-11-10: 3,000 of the 9,950 programmes on
talos's off-Plex channels carried their episode only so).
**Programme artwork** (`app/livetv/tuner/artwork.go`): a title the enricher
wants art for is looked up as a series and a movie through the metadata
gateway's search RPC (the Add New search, bound in
`app/livetv/manager/register.go`), a movie first on a movie channel or for a
film-length programme; an exact title match (year within one when the title
carries one) becomes the programme's `<icon>` and its Movie or Series
category. A build never waits on a lookup: unknown titles queue, a
background pass resolves up to 300 at 400 ms each and the guide is rebuilt
when it found any, and the cache is the `programme-art/<ns>/<name>` object
(hits 30 days, misses 7). **A channel's logo in the guide and playlist is a URL PMS fetches
itself** (owner, 2026-10-27): the IPTVChannel's own `spec.logo`, else its
guide channel's `<icon>` (iptv-epg.org's colour logos made for a dark
guide, or the panel's picon for a channel only the panel's guide holds),
else the playlist's `tvg-logo`. PMS 1.43.5 refuses private and link-local
addresses as an image's destination, so the tuner's own processed copy is
in no guide until a loopback base exists; **the tuner still serves it**
(`app/livetv/tuner/logo.go`, `logos/<number>.png` under every device and
range): the providers' logos (the playlist's `tvg-logo`, the guide's
picons alike) are often black wordmarks on transparency (Bloomberg,
C-SPAN, ESPN), invisible on Plex's dark guide, so the tuner fetches each
once, recolours one at least a fifth transparent with 85% of its opaque
pixels dark (dim by luminance or in every channel; a saturated red stays)
to white keeping every pixel's alpha, and caches the PNG in the Live TV
store by source URL (`livetvstore.LogoObject`); a logo that cannot be had
redirects to its source. After each render a background pass stores every
active channel's logo that way (four at a time, a failure passed over for
an hour), so the ui shows logos from the store, never from their hosts
(ADR-0011). Each render that stores the whole-provider guide
(`guide/<ns>/<name>`, the logos Plex is given) stores its **guide index**
beside it (`guide-index/<ns>/<name>`, `livetvstore.IndexGuide`: channels
and programmes with their titles, episode labels, images, live and new),
which the ui's TV Guide reads instead of parsing XMLTV; the store's names
and shapes are `pkg/iptv/livetvstore`. E2e scenario 19
(`test/e2e/livetv_test.go`, `iptv-stub`) is written and has never run.

**The automatic lineup and the custom guides** (owner, 2026-11-10: "let's go
custom the whole way"; `docs/superpowers/specs/2026-11-10-livetv-custom-guides-design.md`,
whose "As built" section lists the rulings). Every channel argon-tv lists
within reason goes to Plex, split across DVRs by category, alphabetical
within each, with a guide built outside Plex.
**The lineup.** An IPTVProvider's `spec.lineup` (`enabled`, `trim`,
`exclude`, `categories[]` each with a `name`, a `base` of at most 99000,
`groups`, `names` and `exclude`) is planned by the pure `pkg/iptv/lineup`
and applied by `app/livetv/controller/lineup`, a leader-only runnable in
the manager. The planner trims each admitted entry's name, leaves out what
`exclude` names, drops duplicates by trimmed name (HD over SD, then group
order, then playlist order), classifies by the first category whose
`groups` or `names` match and whose `exclude` does not (a later rule of the
same name joins that category; the first rule of the name that carries a
`base` sets it, and two categories whose bases are less than
`dvr.maxChannels` apart are refused as `InvalidLineup`, since their numbers
could meet), and numbers each category alphabetically from `base`+1 with no
gaps. A category over `dvr.maxChannels` (400) or whose channel map would
pass the 30,000-byte budget (`blocks.MapBytes`) is `TooMany` and **held**
(the design's §5.2 step 5, restored by the final review, 2026-11-10): the
lineup controller writes nothing to its channels, which keep their spec and
`status.lineup`; the provider keeps the block it has published for the
category at its published start and device (none published, none made)
and does not wait on its channels; `LineupApplied=False` reason `TooMany`
names it. Until then a rule edit that overfilled one category deleted that
category's DVR in Plex. An entry no rule matches is unclassified and off
every DVR: an IPTVChannel it already has is deactivated (its
`status.lineup` naming no rule), and none is made for it, since the lineup
creates channels only for its assignments, so a new unmatched entry shows
only in the Channels page's playlist pane. **The collapse guard:** a plan
that would take more than half of the active channels off every DVR (and
at least ten) is refused as an invalid one is ("the plan would deactivate N
of M active channels; refusing" stored beside the last good plan,
`LineupApplied=False` `InvalidLineup`, nothing written to a channel, Event
`LineupCollapseRefused`): a playlist come back nearly empty, or a rule edit
that unmatches most of the lineup. The provider's
`clustarr.io/lineup-allow-collapse: "true"`
(`clustarrv1.AnnotationLineupAllowCollapse`) lets one such plan through;
the plan object's `CollapseAllowed` remembers it while the annotation
stays, so no second one passes until a person removes it and sets it again
(the manager holds no patch on IPTVProviders and never removes it).
**One channel a key:** two IPTVChannels of one playlist key (a copy made by
hand, a timeshift copy) are one too many; `index.ChannelsByKey` keeps the
key's `names.ChannelObjectName` channel (the one name the lineup and the
ui's add give a key), else the first by name, for both controllers alike,
and the lineup deactivates every other channel of the key, its number kept,
`status.lineup.duplicateOf` naming the chosen channel (never deleted); the
provider holds its blocks until they are inactive. So a timeshift copy is
off every DVR under an automatic lineup: keeping one there is a follow-up
for the owner. The
dry run is `enabled: false` with categories set: nothing is written to a
channel's spec; the plan is stored whole as the `lineup-plan/<ns>/<name>`
object (assignments, duplicates and their winners, drops, `TooMany`), the
provider's `status.categories` and each existing channel's `status.lineup`
are written, and the Channels page reads it as a banner and per-row
columns. The controller writes under field manager `clustarr-livetv-lineup`
(`k8s.ManagerLiveTVLineup`), which owns exactly `spec.number`, `spec.active`,
the `clustarr.io/origin: lineup` label on the channels it created and
`status.lineup` (`rule`, `name`, `category`, `duplicateOf`, each clamped to
its MaxLength so an oversized string cannot refuse every apply); it never
writes `spec.name` or `spec.group`: the shown name is `spec.name`, else
`status.lineup.name`, and the guide class group is `spec.group`, else the
category. A `spec.name` equal to the playlist's raw name is no override
(the ui posts the raw name on every add, and on talos every channel would
keep it). `spec.category` is a person's: nil follows the rules, "" is off
every DVR, a name moves the channel (an undefined name drops it as
unmatched; an override skips the spec's `exclude`); a category may not be
named `Automatic` or `Off`, the ui picker's two words (CEL on `LineupSpec`).
An entry gone from the playlist is deactivated and deleted after 7 days
only if the lineup created it; one that returns inside the week keeps its
channel and its edits. An invalid rule (a `names` regexp that does not
compile) reads `Valid=True`, `LineupApplied=False` reason `InvalidLineup`
naming the rule, writes nothing and keeps the last applied lineup. The
provider controller then publishes **a block per category**, with no
packer: `status.blocks[].category`, `start` = `base`+1, device id
`hdhr.BlockDeviceID(ns, name, "cat/"+category)` (kept across a new base),
friendly name the category; a renumbering moves that device's `lineupHash`
and costs one channel-map save, never a guide reload on a built guide. Until
every channel carries its plan the published blocks stand (`LineupPending`),
as they do on an invalid plan, and so does any published block (the cloud
block at the one-edit flip included) while an enabled lineup has no plan
stored or only the dry run's: only a provider with nothing published packs
by number meanwhile, or the flip would publish an XMLTV block of every
channel at its DIRECTV number in the cloud DVR's place. `spec.channelNumberStart` is ignored while
the lineup is enabled. The tuner's `splitBlocks` puts a channel no category
lists on no category's device.
**The guide sources.** `spec.guideSources.plex` (`credentialsRef`,
`lineups`, `refresh`, `rate` calls per second, default 2) pairs each
channel with a Gracenote station through `plexguide.Pair` over the lineups
in order, the cloud lineup first when `plexGuide` is enabled, a hand pick
before any (`spec.epg.plex.station` stays a station hint, not a DVR
pairing); the first Manual or Automatic match wins and is recorded in
`status.plex` (`Pairing.Lineup`/`status.plex.lineup`; a cloud block takes
only pairings of the cloud lineup). The tuner fetches one `GET /grid/bulk`
per paired station for 14 days (`pkg/iptv/plexgrid`; the fetch key
`"grids"`), paced at `rate`; a 429 holds the pass for max(Retry-After,
1 minute) **timed from the answer**, not the pass's start (a 200-station
pass outlived the hold otherwise), recorded in `status.guideSources.plex`,
and a held fetch never holds a render, which takes what the store has.
Each grid is stored as `grid/<ns>/<name>/<gridKey>` (about 100 KB); a
station whose grid is empty, or fails, leaves the channel on its next
source (the grid, else iptv-epg.org, else the panel's, else the dummy; never
mixed within a channel), recorded as `status.guide` `plex/<gridKey>`,
`xmltv/<id>`, `panel/<id>` or `dummy/<minutes>`. **No decoded grid is kept
between renders** (the 2026-11-03 rule; a cache was built and dropped): the
measured cost is about 6 ms and 4 MB of short-lived heap per station grid,
about 1.2 s and 800 MB of churn per render at 200 stations, accepted until
measured on talos. A grid programme with a Plex guid counts as described, so
no art lookup runs for it. Metrics: `clustarr_livetv_grid_calls_total` and
`clustarr_livetv_grid_hold_seconds`. Grids of unpaired stations or removed
providers are never swept (a maintenance sweep is a follow-up).
**The XMLTV profile** (`pkg/iptv/xmltv.Listing`, `Programme.Listing()` its
inverse; the one format for PMS's loader, the ui's TV Guide index and
cluster-plex's builder): every programme in XMLTV's DTD order
(`dtdOrder` in `enrich.go`, which now reorders every programme, not only the
ones it changes), each channel with a second `<display-name>`, "<number>
<name>" (PMS's channelTitle); `image` elements for the poster, backdrop and
still (`size="3"`, `orient`), which PMS ignores and the builder reads; the
`plex`, `plex.show` and `plex.season` `episode-num` systems; credits, rating,
`previously-shown`. A programme with no episode numbers gets the enricher's
`dd_progid`, so PMS files it alike whatever the source. `original-air-date`
is written only beside an episode's numbers: PMS keys by the numbers and
stores the date, but alone it keys the episode by date and suppresses the
dd_progid. `Series` and `Movie` stay in `<category>` as genres PMS keeps (an
oracle against the rig's DVR 8 dump decided it). The first render moves every
XMLTV block's `guideHash` (the reorder and the second display-name): one
reload per XMLTV DVR, none on talos today.
**The ui.** The Channels page shows each channel's category, number, rule,
the duplicate it lost to and its guide source; the category override is an
on-demand picker dialog (`GET /livetv/channels/{ns}/{name}/category`, like
the EPG picker; an in-row select cost 22.6 KB a row), listing Automatic, Off,
then `status.categories` (the stored plan's in a dry run); under an enabled
lineup the Active switch and the bulk enable and disable post the override
(Automatic or Off), since the lineup owns `spec.active`. The dry-run banner
carries the plan's summary. `docs/examples/iptvprovider-argon-lineup.yaml` is
the example. **Sports Events is deferred** (design D3): the packages' feeds
rename themselves per game and a channel's identity is its name key, so the
category would churn IPTVChannels and renumber daily; it needs stream-id keys
and event-name programmes, a follow-up design. `spec.lineup.prefer` and
`languages` are not built: the duplicate rule is fixed and a language is left
out with `exclude`. **The cluster-plex half** (builder from this XMLTV, the
first PMS load imported and later ones refused) is `plex.liveTV.xmltvGuide:
pms | manager` there; clustarr's contract is the profile above.

