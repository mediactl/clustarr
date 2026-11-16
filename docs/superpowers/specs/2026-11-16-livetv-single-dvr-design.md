# Live TV: one Plex DVR over every block's device

Status: proposed, 2026-11-16. Not built. Awaits the owner's approval.

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
