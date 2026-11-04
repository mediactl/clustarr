# Live TV on Plex's own guide (Gracenote lineups)

Date: 2026-11-03. Status: built 2026-11-03 (clustarr 1e4244ae..f85335a7, cluster-plex 3bd6d98, 6daf7d3); not enabled on talos, the §5.8 check pending. Repos: clustarr (API, the
provider and channel controllers, blocks, the tuner, the ui) and
cluster-plex (the Live TV converger).

## 1. Intent

The owner wants argon-tv's channels to use Plex's native guide, the
Gracenote listings Plex serves for a cable or satellite lineup, instead of
the XMLTV guide clustarr builds. Two reasons, in the owner's words: it
"should provide a large performance improvement and be natively supported
in Plex".

The performance problem is measured. On 2026-11-03 PMS loaded DVR 12's
XMLTV guide (257 channels, a 33.9 MB `xmltv.xml`, about 220,000 rows) four
times, and each load took 9.8 to 12.7 minutes from start to end:

| Load started (UTC) | Copied into PostgreSQL | Rows    | cluster-plex's copy |
| ------------------ | ---------------------- | ------- | ------------------- |
| 16:21:06           | 16:31:08               | 229,298 | 19.6 s              |
| 16:54:44           | 17:06:49               | 228,594 | 30.2 s              |
| 19:00:20           | 19:13:04               | 225,682 | 45.7 s              |
| 23:17:51           | 23:27:42               | 220,962 | 23.2 s              |

(plex-0's PMS log "Running migrations. (EPG 1)" against cluster-plex's
"copied a Live TV guide PMS loaded into its schema".) cluster-plex's part is
the last 20-46 s; the rest is PMS on the Pi 5 parsing the XMLTV and writing
its `-loading.db`. While a load runs the converger defers every DVR change
and What's On renders slowly.

"Natively supported" means Plex's own data for those channels: Gracenote's
titles, episode numbers, sports metadata and channel logos, the way a Plex
user with a real DIRECTV box sees them, with no clustarr-built XMLTV in
between.

## 2. What the live PMS showed (read-only, 2026-11-03)

- **Lineups by postal code.** `GET /livetv/epg/countries` lists the US as
  `usa/tv.plex.providers.epg.cloud/lineups` (flavor 1, a postal code).
  `GET /livetv/epg/countries/usa/tv.plex.providers.epg.cloud/lineups?postalCode=32548`
  answers lineups whose `uuid` is
  `lineup://tv.plex.providers.epg.cloud/<24-hex id>#<url-encoded title>`.
- **There is no national DIRECTV lineup.** DIRECTV lineups are per market
  (DIRECTV Los Angeles, Chicago, Denver, Honolulu, ...), each the national
  channels plus that market's stations.
- **The owner's lineup.** "DIRECTV - Mobile-Pensacola (Ft. Walton Beach)" is
  Plex's **DIRECTV Mobile (1009 channels)**, id `657c8183afd0f02cd353633f`
  (the same for 32548 Fort Walton Beach, 32501 Pensacola and 36602 Mobile).
- **A lineup's channels.** `GET /livetv/epg/lineupchannels?lineup=<uuid>` on
  PMS, and `GET https://epg.provider.plex.tv/lineups/<id>/channels` on Plex's
  cloud with the owner's Plex account token (the `plex-token` Secret in
  `clustarr-system`, key `token`), answer the same channels. The cloud's
  entry:
  `{"affiliateCallSign":"CBS","callSign":"WKRGDT","gridKey":"5fc70603336b43002e137ec6","id":"657c8183afd0f02cd353633f-5fc70603336b43002e137ec6","isHd":true,"shortTitle":"WKRG-DT","thumb":"https://provider-static.plex.tv/...png","title":"WKRG-DT","vcn":"005"}`.
  PMS's spells `id` as `key` and `vcn` as `identifier`/`channelVcn`.
- **A station's id is the same in every lineup.** `gridKey` (the part of
  `id` after the dash) was equal for all 766 call signs DIRECTV New York and
  DIRECTV Mobile share. A pairing kept by station survives a change of
  lineup.
- **Neither list goes past 1,000 channels** for the 1,009-channel lineup,
  and `X-Plex-Container-Start` as a query parameter is ignored (the second
  page repeated the first). Nine channels are unlisted.
- **`vcn` is not unique** (WEAR and WEARDT are both 003); `id` is.
- **The New York stations are in DIRECTV Mobile** as DIRECTV's distant
  network feeds: WCBS 390, WNBC 392, WABC 396, WNYW 398 (and KCBS, KNBC,
  KABC, KTTV, WDCW, KTLA). The playlist's 1001-1004 (WABC, WCBS, WNBC,
  WNYW, the owner's 2026-10-28 picks) therefore have their exact stations.
  WNET (PBS), WPIX (CW) and WWOR (MyNetworkTV) are not in the lineup; its
  own PBS, CW and MyNetworkTV are WEIQ, WFNA and WFGX.
- **Coverage.** A rough name and call-sign match (no use of the guide ids,
  affiliates or aliases) paired 161 of the 257 active channels with DIRECTV
  Mobile. Unpaired, among others: FOX SPORTS 1/2, SHOWTIME, TRAVEL CHANNEL,
  SMITHSONIAN (likely present under other names); UFC, WWE, STADIUM, GINX
  ESPORTS, FUBO SPORTS (streaming-only); COMET, COZI, BUZZR, LAFF, GRIT
  (broadcast subchannels); out-of-market regional sports networks.
- **Channel map save.** cluster-plex saves a device's map in one
  `PUT /media/grabbers/devices/<key>/channelmap` with
  `channelMappingByKey[<device channel>]=<lineup channel id>` and
  `channelMapping[<device channel>]=<lineup vcn>` (`plexapi.SaveChannelMap`);
  today the pairs come from PMS's own `GET /livetv/epg/channelmap`, which
  for an XMLTV lineup pairs each number with itself.

## 3. Goals, non-goals, anti-goals

Goals:

1. Every active channel that has a station in the provider's chosen Plex
   lineup is on a Plex DVR whose guide is that lineup, paired with that
   station.
2. Pairing is automatic, and the owner can override any channel by hand,
   or keep it off Plex.
3. The rest of the channels are on no Plex DVR (owner: "Drop out of
   Plex"); the ui and status say why for each.
4. cluster-plex creates and maintains the DVRs; nobody runs Plex's setup
   wizard.
5. The owner can see which channel is paired with what, and why not.

Non-goals:

- A guide in clustarr's own ui from Gracenote. The ui's TV Guide keeps
  reading clustarr's guide index (§5.7).
- Several Plex lineups per provider. One lineup per provider; a second
  lineup is a later change if the coverage asks for it.
- Choosing the lineup in the ui. It is a spec field (Terraform or kubectl).
- Plex's wizard path (today's `epgSource: PMS`), which goes (§5.1).

Anti-goals, which would make this a failure even if it works:

- A channel silently missing from Plex. Every active channel is on the
  Plex DVR, or its status and the ui say why not.
- A wrong pairing presented as right: the ABC stream showing WEAR's local
  schedule because the names matched. An ambiguous match is left for the
  owner, not guessed (§5.3).
- More guide reloads.
- A channel moving on or off Plex because of one lineup fetch (§5.3).

## 4. Decisions

The owner's:

- Use Plex's native (Gracenote) guide for the channels it covers.
- The lineup is **DIRECTV Mobile** (the owner named "DIRECTV -
  Mobile-Pensacola (Ft. Walton Beach)").
- Pair the argon-tv playlist's channels to it, and the playback the owner
  approved ("Looks good"): automatic pairs by name and call sign with a
  per-channel override; cluster-plex sets the DVR up itself.
- Channels the lineup does not cover **drop out of Plex** (no second,
  XMLTV DVR).
- Paired channels may show DIRECTV's channel numbers in Plex ("Yes").
- The §5.8 check may write to talos's PMS ("Yes"); then "hold off on the
  Plex test then and start the implementation/design".

Mine, which the owner has not ruled on:

- The pairing is kept by station (`gridKey`), not by the lineup channel's
  `id`, so a later lineup change keeps it.
- clustarr fetches the lineup from Plex's cloud itself, so pairing and
  block membership are decided in clustarr (§5.2), and cluster-plex only
  saves what the tuner serves.
- `epgSource` is removed (cold turkey; it was never used: it needed the
  wizard).
- One channel per station, timeshifted channels are not Plex-guided, and
  pairings are sticky across fetches (§5.3).
- A provider with `plexGuide` has Plex blocks only, under new device ids
  (§5.4).

## 5. Design

### 5.1 API (`clustarr.io/v1alpha1`)

IPTVProvider `spec.plexGuide` (optional; absent is today's behaviour, every
channel on XMLTV):

- `lineup` (required): a Plex cloud lineup id (`657c8183afd0f02cd353633f`)
  or its `lineup://tv.plex.providers.epg.cloud/...` uuid; the controller
  stores the id.
- `credentialsRef`: a Secret and key holding a Plex account token
  (talos: `plex-token`, key `token`, which cluster-plex manages).
- `enabled` (default false): the blocks switch to Plex only when true;
  until then the pairing is a dry run (§5.9).

`spec.epgSource` and `EPGSource` are deleted, with every reader
(`status.go`'s `NotUsed` guides condition, `render.go`'s and `http.go`'s
PMS branches, the converger's `ReasonNeedsLineup` wizard message).

IPTVChannel `spec.epg` gains `plex` beside `channelID`/`dummy` (which keep
their exactly-one rule; `plex` is independent of them):

- `plex.station`: a `gridKey` to pair with, overriding the automatic match;
  or
- `plex.off: true`: keep this channel off Plex.

(Exactly one of the two when `plex` is set.) A channel keeps its XMLTV
`channelID`, which still feeds clustarr's guide index (the ui's TV Guide)
and is its guide again should the provider drop `plexGuide`.

IPTVChannel status gains `plex` (the channel controller writes it):
`station`, `callSign`, `title`, `vcn`, `id` (the lineup channel),
`missingSince` (§5.3), and `match`: `Manual`, `Automatic`, `Ambiguous`
(candidates listed, at most 5), `Taken` (another channel holds the
station), `Timeshifted`, `NotInLineup`, `Off`, or `NoLineup`.

IPTVProvider status: `status.plexGuide` with the lineup's title, channel
count, `fetchedAt`, and counts of channels by `match`; a condition
`PlexGuideReady` (lineup fetched, or why not).

`status.blocks[]` gains `lineup`: for a provider with `plexGuide`, the full
`lineup://tv.plex.providers.epg.cloud/<id>#<title>` cluster-plex creates
the DVR with; empty for an XMLTV block.

### 5.2 The lineup

The tuner (manager, leader) fetches the lineup's channels from
`epg.provider.plex.tv/lineups/<id>/channels` with the token, under the
same fetch client and body cap as the playlist, at start when the cache is
stale, daily, and at once when `spec.plexGuide.lineup` changes. It caches
the list in the `clustarr-livetv` store (`plex-lineup/<ns>/<name>`,
`livetvstore` names and shapes) so a restart serves the cache, as the
playlist cache does. A failed fetch keeps the last good list and says so
in `PlexGuideReady`.

### 5.3 Pairing

For each active channel, in order:

1. `spec.epg.plex.off`: `Off`.
2. `spec.epg.plex.station`: `Manual`; `NotInLineup` if the lineup has no
   such station.
3. Automatic, in order of evidence, stopping at the first rule that names
   exactly one station:
   - a call sign named in the channel's XMLTV `channelID`
     (`ABCEast_WABC.us` names WABC, `CBSKEYE.us` KEYE), compared with the
     lineup's `callSign` with a `DT`/`HD` suffix removed;
   - the channel's name or `channelID` equal to a lineup `callSign`,
     `title` or `shortTitle` after normalisation (case, punctuation, `&` as
     "and", a trailing HD/East/US/Network/Channel/TV);
   - a maintained alias table for names Gracenote spells otherwise
     (FOX SPORTS 1 = FS1, ...), built from the owner's channels, in code.
   Several stations of one call sign (WEAR, WEARDT) are one station: take
   the HD one (`isHd`), else the lowest `vcn`. A rule that names several
   stations is `Ambiguous` (the name "ABC" alone names WEAR, WABC and KABC:
   never guessed). No rule: `NotInLineup`.

Two more rules (my calls):

- **One channel per station.** A station paired by two channels goes to
  one: a Manual pair over an Automatic one, else the lower channel number.
  The other reads `Taken` (naming the holder) and is off Plex. Whether PMS
  accepts two device channels on one lineup channel is not known, so it is
  never sent.
- **A timeshifted channel is never Plex-guided.** Plex's guide cannot
  shift a channel's times, so a channel with `timeshiftMinutes` set reads
  `Timeshifted` and is off Plex, a Manual station included.

Only `Manual` and `Automatic` channels reach Plex; every other `match` is
off Plex, and says why.

The pairing is recomputed when the channel, its provider's lineup, or the
cached list changes, and it is **sticky** (my call): an Automatic pair is
never moved to another station while its station is listed, and a station
absent from a fetch keeps its channel on Plex, with
`status.plex.missingSince`, until it has been absent for 7 days. Neither
list goes past 1,000 channels and which nine are left out may change from
fetch to fetch; a channel leaving the map costs its recording rules and a
map save, so one fetch must never move it.

### 5.4 Blocks

With `plexGuide` set, a provider's blocks hold its `Manual` and
`Automatic` channels only; the others are in no block, so no device lists
them and PMS never sees them. Without it, blocks are today's. A provider's
blocks are of one kind, so `status.blocks` keeps its `start` key and
`blocks.Next` its rules.

A Plex block's device id is `hdhr.BlockDeviceID(ns, name, "plex/"+lineupID+"/"+start)`
(my call): turning `plexGuide` on gives every block a new device, so
cluster-plex removes the old devices and their XMLTV DVRs as gone blocks
and registers the new ones, through paths it already has, rather than
changing a DVR's lineup in place (PMS has no call for that). Turning it off
does the same the other way.

A Plex block's map budget counts what its save actually sends (the lineup
channel `id` and `vcn` per number, not the number twice): `blocks.MapBytes`
takes the per-channel values. At about 116 bytes a channel, a block holds
about 258 channels at 100% (about 193 at the first packing's 75%), so 161
to 200 paired channels are one block.

### 5.5 The tuner

Each block keeps its `/hdhr/<id>` device (discover, lineup, streams) as
today. A Plex block serves no `xmltv.xml` and has no `guideHash`. Each Plex
block serves its channel map at `/hdhr/<id>/plexmap.json`: one entry per
number, `{"deviceIdentifier":"1070","channelKey":"<lineup channel id>","lineupIdentifier":"<vcn>"}`.
The block's `lineupHash` covers the map, so a pairing change reaches PMS
through the converger's existing lineup-hash path.

### 5.6 cluster-plex

`watch.go` reads `lineup` from each block. For a block with one the
converger:

- creates the DVR with `lineup` (not `XMLTVLineup`);
- when `lineupHash` moves, reads `plexmap.json` from the block's URI and
  saves it with `SaveChannelMap`, instead of asking PMS's
  `/livetv/epg/channelmap`;
- never reloads its guide (no `guideHash`, no empty-guide reload), and does
  not treat it as an XMLTV ghost; a cloud-lineup DVR on a managed device the
  provider no longer names is removed with its device, as today.

A block without `lineup` is converged as today. cluster-plex's guide copy
(`bootstrap/guides.go`, `copyGuideLoads`) already matches any
`tv.plex.providers.epg.<provider>-<uuid>` file, a cloud one included; what
§5.8 finds about PMS's local writes for a cloud guide is folded in before
the cutover.

### 5.7 The ui

Channels (`/livetv/channels`) shows each channel's guide: the paired
station (call sign, title, DIRECTV number, logo from the lineup's `thumb`)
with Manual or Automatic, or why it is off Plex (Ambiguous with its
candidates, Taken by which channel, Timeshifted, Not in lineup, Off). Its
EPG edit adds a Plex station picker over the cached lineup (searched by
call sign and title) and "Keep off Plex", written through
`actions.EditChannels` as `spec.epg.plex`; a filter lists the channels off
Plex. M3U/EPG Manager shows `status.plexGuide`. The TV Guide keeps reading
clustarr's guide index, which the tuner still builds for every channel with
an XMLTV mapping, on Plex or not.

### 5.8 What is measured before the cutover

Nothing has shown that PMS's guide work for a cloud lineup is cheaper than
for an XMLTV one; Plex may still load the lineup's listings into a local
`tv.plex.providers.epg.cloud-<uuid>.db`. The owner approved a check and
then held it (a Live TV session was running) and asked for the build to
start: the build goes ahead, and the check runs before `plexGuide.enabled`
is set on talos. Its result may still change §5.6 (the copy into
PostgreSQL, reloads). The script and stub are in the session's scratchpad
(`probe-create.sh`, `gn/stub.yaml`). The check, on talos's PMS: a stub HDHomeRun device outside the livetv Service
(so the converger ignores it), a DVR on it with DIRECTV Mobile and a few
dozen of argon-tv's channel numbers paired by `channelMappingByKey`, then:
the time from DVR creation to guide airings for the paired channels; what
PMS writes locally (files, `/activities`), and whether it goes through the
`-loading.db` rename that cluster-plex copies into PostgreSQL; whether a
map save or `reloadGuide` starts a load; which channel numbers Plex shows
for paired channels (the lineup's `vcn` or the device's number); whether
the Plex Web guide shows them; then the DVR and device are deleted. The
result goes into this document. If a cloud guide costs PMS about as much as
an XMLTV one, the owner decides whether to build for the native metadata
alone.

The check writes to talos's PMS, which the owner's standing rule since
2026-10-27 forbids without approval (two hand-made `reloadGuide` calls
deleted DVR 5 that night). Fenced: only with no Live TV session and no
`provider.epg.load` in `/activities`; the stub device's URI is outside the
livetv Service, so the converger neither adopts nor deletes it; DVR 12 is
never touched; the DVR is deleted only once `/activities` shows its load
ended.

### 5.9 Rollout

- Turning `plexGuide` on deletes DVR 12 and device 8246E54A, and with them
  every recording rule (`GET /media/subscriptions`); list them before the
  cutover.
- The cutover is one deploy of clustarr and cluster-plex together (the
  block field and the converger change), then `spec.plexGuide` in
  cluster-bootstrap's `clustarr/resources.yaml`. Removing `plexGuide` puts
  every channel back on an XMLTV DVR the same way.
- The CRD change is applied by hand before the Terraform apply (talos).
- A dry run comes first (my call): with `plexGuide.lineup` set the pairing
  is computed and shown (channel status, the ui) while the blocks stay
  XMLTV; the blocks switch only when `plexGuide.enabled` is true, so the
  owner settles the Ambiguous and off-Plex channels before anything in
  Plex changes.

## 6. Left to the builder

- How the lineup list is paged past 1,000 channels, if at all (nine of
  DIRECTV Mobile's are unlisted; neither API pages by query parameter).
- The alias table's first contents, from the unpaired list in §2 against
  the lineup.
- Names of the store object, metrics and Events.
- The ui's picker widget.
