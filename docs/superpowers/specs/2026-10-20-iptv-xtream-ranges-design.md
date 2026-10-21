# Live TV: Xtream providers, cached sources, channel ranges

Date: 2026-10-20. Status: approved by the owner (choices below).
Amends `2026-10-07-iptv-live-tv-design.md`; where they disagree, this wins.

## Why

An IPTVProvider named its playlist and guides as Secret-held URLs, and the
tuner held the playlist only in memory. A slow panel (tvload.win, 13 s to
answer, 2026-10-20) and a manager restart together took every channel
off Plex. The controller also planned blocks, ports and device IDs, which
cluster-plex then had to mirror.

## Decisions (owner, 2026-10-20)

- **The provider is an Xtream Codes account.** `spec.server` (a host,
  host:port or URL; `https://` unless it says `http://`) and
  `spec.credentialsRef`, a Secret with `username` and `password`. Those are
  the only required fields. The playlist is
  `<server>/get.php?username=…&password=…&output=ts&type=m3u_plus`, the
  guide `<server>/xmltv.php?username=…&password=…`, and the account
  `<server>/player_api.php?username=…&password=…`.
- **Tuners come from the account.** `user_info.max_connections`, read with
  the playlist; `spec.tuners` is an optional override. `status.account`
  reports the account's status, expiry and connections.
- **Both sources are fetched daily and cached** in the `clustarr-livetv`
  object store (`spec.refresh`, default 24 h). The playlist cache is every
  live entry, with the account's username and password in stream URLs
  replaced by `{username}` and `{password}`, so no credential is stored.
  The guide cache is the parsed guide for the selected channels only. A
  restart serves both from cache and fetches a source only when its cache
  is older than the refresh, or the guide's wanted channels grew. The kept
  playlist Secret (`<provider>-livetv-playlist`) goes.
- **clustarr plans the blocks and devices; cluster-plex maps a port to
  each device's endpoint** (owner, revised the same day). The controller
  still packs the active channels into blocks (`pkg/iptv/blocks`), and gives
  each a device ID, a sticky port from `--livetv-port-range` and a Lease
  owner. Each block also gets an **endpoint**,
  `/livetv/<ns>/<name>/devices/<deviceID>`, published in
  `status.blocks[].endpoint` beside its port.
  - **The tuner** serves each device at its endpoint: `discover.json`,
    `lineup_status.json`, `lineup.json`, `lineup.post`, `device.xml`,
    `xmltv.xml`, `playlist.m3u` and `stream/<number>`.
  - **cluster-plex** runs one HTTP proxy per block, from
    `169.254.47.1:<port>` to `<status.address><endpoint><path>`. It sets
    `X-Forwarded-Host` to the port Plex dialled and `X-Forwarded-Prefix: /`.
    The tuner builds every URL it hands out from those headers, so Plex's
    later requests come back through the same port.
  - **Change detection** is unchanged: the converger still reads
    `lineupHash` and `guideHash`, and lineups and guides also carry an ETag
    of their body.
  - **The tuner no longer routes by the port in `Host`.**
- **Ranges, for any other client.** `/livetv/<ns>/<name>/<file>?from=<n>&to=<n>`
  serves any range of the lineup, either end optional. Its device ID is
  `hdhr.BlockDeviceID(ns, name, from)`, and its `LineupURL` carries the
  range.

## Removed (cold turkey)

`spec.playlist` and `spec.guides`, which `spec.server` and
`spec.credentialsRef` replace. Also removed:
- `status.guides`, now `status.guide`, and IPTVChannel `spec.epg.guide`;
- the tuner's port routes (`/<file>` by the port in Host) and its old
  `/livetv/<ns>/<name>/<start>/` routes;
- the per-block stored guides, which the guide cache replaces;
- the kept playlist Secret (`<provider>-livetv-playlist`), which the
  playlist cache replaces.

`spec.device`, `spec.dvr`, `spec.tuners` and `status.blocks` stay
(`spec.tuners` now optional).
