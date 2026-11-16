# Live TV: one Plex DVR over every block's device

Status: built 2026-11-16 (clustarr eed4eeb8, b274f833; cluster-plex 5ac8f70,
49166dd), approved by the owner the same day. Cut over on talos at 07:01Z
and rolled back at 07:43Z; see "As built, and the talos cutover" at the end.
Off on talos.

## Why

Since the category cutover (2026-11-11, talos rev 251) argon-tv is 14 blocks,
each one HDHomeRun device and one Plex DVR: 796 channels across DVRs 35-74.
Plex advertises every DVR as its own source, all titled "Live TV & DVR", and
no client merges their guides. The owner saw only News and asked (2026-11-15)
for one place listing every channel.

The split exists because PMS takes about 450 channels in one **device's**
channel map (one PUT under 32 KiB). That limit is per device, not per DVR.

## What the rig proved (2026-11-15/16, kind, PMS 1.43.5)

- `POST /livetv/dvrs` with two `device=` parameters makes one DVR over both.
- Each device keeps its own channel map (`PUT
  /media/grabbers/devices/<key>/channelmap`); 350 channels a device saved at
  22.6 KB each.
- The DVR is one source whose guide lists every device's channels (20, then
  700).
- A tune goes to the device whose map carries the channel. With a device's
  slots full, a further tune of its channel is refused ("The recording
  conflicts with other recordings") and never sent to the other device.
- PMS's own load of a 1,350-channel XMLTV took 15 minutes; a 3-hour grid
  query over 700 channels took 21.6 s and 9.7 MB on the rig's database.

Not proven: attaching a device to a DVR that already exists; a create naming
14 devices; a client's guide over 796 channels; the cutover itself.

Measured on talos the same day, read-only: a 3-hour grid query over each of
the 14 DVRs sums to 15.6 s (Sports' 281 channels 4.6 s); 0 recording rules.

## Goals

- One Plex source and one guide for a provider's every channel.
- Devices, device ids, channel numbers and channel maps unchanged, so the
  move is a DVR change only and is reversible.
- Off by default; a provider opts in.

## Non-goals

- Merging several providers into one DVR.
- Changing the per-device limit, the categories or the numbering.
- Naming the per-DVR sources (moot once there is one).

## Anti-goals

- A cutover that can leave Plex without Live TV until a person steps in.
- A second copy of the guide rendered or held in the manager's memory.

## Design

### clustarr

`IPTVProvider.spec.dvr.single` (`*bool`, default false). When true:

- Blocks are planned and published exactly as today (`status.blocks[]`,
  one per category or packed block, same device ids).
- `status.dvr` is published once every block is served:
  - `title`: the provider's friendly name ("Clustarr Argon TV").
  - `guidePath`: `/livetv/<ns>/<name>/xmltv.xml`, the whole-provider guide
    the tuner already builds and stores each render (`livetvstore.GuideObject`)
    and already serves on the range path.
  - `guideURI`: `http://<status.address><guidePath>`.
  - `guideHash`: the whole guide's hash, which the render already computes
    (`storedGuideHash`) and publishes nowhere today.
- A cloud block (`status.blocks[].lineup` set) is never part of a single
  DVR: `single` with `plexGuide.enabled` is refused by CEL.
- The whole guide must hold exactly the channels on the blocks. Today it is
  built from the lineup's channels; the build asserts the two sets agree
  when `single` is set and reads `Ready=False` reason `GuideMismatch`
  otherwise.

No new render, object or memory: the whole guide and the per-block guides
are built in the one pass as today. The per-block guides stay served, so the
flag can be turned off again.

### cluster-plex

`pkg/plex/livetv`: a provider whose status carries `dvr` is converged as one
group.

1. Register every block's device as today (leases per device, the
   stale-address rule, the dead-device discover, all unchanged).
2. The wanted DVR is the one whose lineup is `XMLTVLineup(dvr.guideURI,
   dvr.title)`.
3. A DVR on any of the group's devices with another lineup is stale and is
   deleted, one write a pass, never while PMS loads a guide (the existing
   `idle` gate).
4. With no wanted DVR, create it naming every registered device not on a
   DVR. A device registered later is attached to it (the call the rig run
   must settle: `PUT /livetv/dvrs/<key>/devices/<deviceKey>`, else a
   re-create).
5. Each device's map is saved as today, from PMS's own pairing of that
   device against the DVR's lineup, when its block's `lineupHash` moves.
6. The guide is asked of the builder once per DVR: `XMLTVGuides.Want(dvr
   uuid, dvr.guideURI, dvr.guideHash)`. PMS's loads after a map save are
   cancelled per device as today.
7. A provider without `status.dvr` converges per block as today, and a
   group DVR on its devices is then the stale one: the way back.

`cmd/manager/livetv.go` reads `status.dvr` into `livetv.Provider`.

### Cutover on talos

1. CRD by hand, then both images with `single` unset: no change.
2. `spec.dvr.single: true` on argon-tv.
3. cluster-plex deletes DVRs 35-74 (one a pass, between guide loads),
   creates the one DVR, saves 14 maps, builds one guide.
4. Live TV is off from the first delete until the build installs: expect
   minutes. Run it in a quiet hour.
5. Rollback: `single: false`, which recreates the 14 DVRs the same way.

## Rig proof required before talos

- Attach a device to an existing DVR, or settle on re-create.
- One create naming 14 devices.
- The full cutover and its rollback at 796 channels with the managers'
  built guide (`xmltvGuide: manager`), including a failure injected between
  the deletes and the create.
- The guide in a real client against the built 796-channel guide: first
  paint and scroll. If it is slow, the same grouping takes two or three
  DVRs (a `dvr.group` on categories), a follow-up, not this design.

## Risks

- **Guide query cost in one request** (above). Clients page the grid.
- **Tuner slots.** The account allows 3 streams (`status.account`
  `maxConnections` 3; owner, 2026-11-16), but `spec.tuners` is still 1 on
  talos, set 2026-10-21 when the account refused a second stream. Each
  device advertises `tuners` plus 3 spare, so one DVR reads as 56 slots
  today and 84 at `tuners: 3`. Plex counts conflicts per device, so it
  will let more than the account's streams start at once across devices;
  the relay's cap decides, as it does across the 14 DVRs today. Raising
  `spec.tuners` to 3 is a separate, one-line change the owner may want
  first.
- **Mid-cutover failure.** Deletes done and the create failing leaves Live
  TV off until a later pass succeeds; the pass retries on its own.
- **Several Plex pods.** Unchanged: one-pod Live TV guard, and the other
  pods' PMS restarted after a DVR change.

## Owner's calls

1. Approve the approach (one DVR over the existing devices, opt-in).
2. The DVR's title.
3. Whether to cut talos over once the rig proof passes, or review first.

## As built, and the talos cutover (2026-11-16)

**Built as designed**, with these differences:

- No `GuideMismatch` check: PMS maps only the channels a device lists, so a
  guide channel on no block is inert (rig: 1,350 guide channels, 700 mapped).
- The DVR is created over one device and every other is attached
  (`PUT /livetv/dvrs/<key>/devices/<deviceKey>`, proven on the rig; it
  starts no load). No create names several devices.
- The unranged `/livetv/<ns>/<name>/xmltv.xml` is served from the store
  under the whole guide's hash; it was rebuilt per request and cached.
- Under the managers' builder the loads the create and the saves start are
  cancelled; a whole lineup's load is half an hour and every write waits.

**Rig proof** (two devices, `xmltvGuide: manager`): per block to one DVR
in 61 s, and back in 61 s; tunes went to the device carrying the channel.

**talos**, by kubectl (the owner's call): CRD, manager `sha-eed4eeb`, Plex
`cluster-plex:25d7e75` (7f641ac plus the converger change), then
`spec.dvr.single: true` at 07:01:48Z. DVR 76 held all 14 devices and 796
channels 691 s later, one Live TV source, 132,893 airings built. Then:

1. **The guide was empty in Plex** until an in-place restart: PMS opens a
   new DVR's guide before the build exists, and the load that would have
   it open the guide again is cancelled.
2. **Tunes hung and Plex stopped answering** (about 07:30-07:40Z, and again
   at 07:44-07:47Z when the rollback's delete of DVR 76 hung PMS and the
   supervisor restarted it). PMS held about 3 cores on the Pi.
3. Rolled back at 07:43Z (`single: false`); 14 DVRs and 796 channels were
   back at 08:47Z.
4. At 08:05Z the Helm addon reconciled the release (rev 257) and undid the
   kubectl images and, through the settings sync, `spec.dvr`. **A change
   made by kubectl lasts under an hour on talos: this feature goes in
   through cluster-bootstrap or not at all.**

**The cause of 2**, reproduced on the rig with talos's guide (82 MB, 796
channels) and a live MPEG-TS stream, and read from PostgreSQL's log: during
a Live TV session PMS asks for what is on now on every channel of the DVR
(one statement, `metadata_items.id IN (768 ids)`) joined to **every airing
of each of those items**, and works the rows one at a time with further
queries per row. A channel without listings had hourly dummy blocks under a
bare title, which PMS files as one movie a channel with an airing a block:
160 airings each on 478 channels, 76,480 rows a tune. With a DVR a category
the same statement names that category's channels only (Sports, 281
channels, about 34,000 rows today). The first reading, "PMS walks the whole
guide on every tune", was wrong: the statements seen in `ClientWrite` for
20 minutes were this one, not a scan.

**Fixes** (built, rig-proven for the first; not deployed):

- clustarr b274f833: each dummy block is its own episode (a `dd_progid`
  per block, the channel's name as sub-title). Rig, same guide: the tune
  answered in 5.4 s where it had not in 120 s; a one-hour grid 0.34 s
  where it took 2.7 s. This also cuts the 14 DVRs' tunes.
- cluster-plex 49166dd: the converger asks PMS what it serves of a built
  one-DVR guide once the maps are saved and restarts this pod's PMS once
  when that is nothing; and PMS's own loads of such a guide are cancelled
  whenever they run when every DVR in PMS is such a one.

**Before another cutover:** deploy the dummy-block fix first and watch a
real client's tune time on the 14 DVRs; then cut over through
cluster-bootstrap (images and `dvr.single` both), and test a real client
before calling it done. A synthetic tune leaves a "Recording" session in
Plex for minutes: cancel it (`DELETE /activities/<uuid>`).
