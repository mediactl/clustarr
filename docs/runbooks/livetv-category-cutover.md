# Live TV cutover: the DIRECTV cloud block to category DVRs with built guides

**Status:** written 2026-11-10 and revised the same day after the branch's final review (the
holds, the collapse guard, the pre-flight); not run. Nothing here is deployed by an agent: the owner applies
CRDs by hand and everything else through cluster-bootstrap Terraform (`tfaa`). Design:
`docs/superpowers/specs/2026-11-10-livetv-custom-guides-design.md` (§5.9 what is measured, §5.10
the steps below). Measured, as built and rulings: its "As built" section.

**What changes.** Today argon-tv's 248 paired channels sit on one cloud DVR (DIRECTV Mobile, DVR
32, `spec.plexGuide.enabled: true`) and the other channels are on no DVR. After the flip,
`spec.lineup` (categories, trim and exclude rules) decides which playlist entries are channels,
their names and numbers (the lineup controller owns `spec.number`, `spec.active` and each
channel's `status.lineup`, never `spec.name`: a channel's shown name is its `spec.name`, else
`status.lineup.name`, the trimmed playlist name), and the tuner publishes one device per category. Each
category is one XMLTV DVR whose guide cluster-plex builds in a Job (iptv-epg.org, else the panel's
guide; Gracenote grids from Plex's guide service where a station pairs only when
`spec.guideSources.plex` is set, which the owner has not asked for on talos) and writes into PMS's
guide schema.

**Accepted loss** (owner): every recording rule on the DIRECTV DVR, which is deleted with it.
Re-create them in Plex afterwards. Recordings on disk stay.

**Names.** Kube context `admin@talos`; namespace `clustarr-system`; provider `argon-tv`;
Secret `plex-token` (key `token`, already there); the release `clustarr` (livetv Service
`clustarr-livetv`). Every `kubectl` below reads one object, or filters on the server with
`-l` / `--field-selector` and `--chunk-size=0`. Never list every Episode or MediaFile.

Rules in force (from the build's rulings, so the screens make sense): a `spec.name` equal to the
playlist's raw name is no override; under an enabled lineup the ui's Active switch and bulk
enable/disable mean the category override (Off or Automatic), because the lineup owns
`spec.active`; `Automatic` and `Off` are reserved category names; a category base is at most
99000, and two categories' bases at least 400 (`dvr.maxChannels`) apart or the lineup reads
`InvalidLineup`; a category over 400 channels or over the 30,000-byte map budget is refused (reason
`TooMany`) and **held**: its channels are left as they were and the block it has published (if
any) is kept; a plan that would deactivate more than half of the active channels (and at least
ten) is refused as `InvalidLineup` ("the plan would deactivate N of M active channels; refusing",
Event `LineupCollapseRefused`, nothing written) until the provider carries
`clustarr.io/lineup-allow-collapse: "true"`, which lets one such plan through (remove it and set
it again for another; the manager never removes it, so take it off once the plan is applied); a
second channel of one playlist key (a hand-made or timeshift copy) is deactivated, its
`status.lineup.duplicateOf` naming the channel that carries the key; a cloud block takes only
pairings of the cloud lineup; Sports Events (one category per game feed) is deferred, so event
feeds stay in Sports. **The MLB and NBA PACKAGE groups** are admitted by talos's filters (for the
2026-11-04 DIRECTV pairings) but no category rule names them: their entries read unmatched and
their channels go off every DVR at the flip. Add them to a rule's `groups` first if they should
stay.

---

## 0. The CRDs, by hand, first

Helm does not upgrade CRDs, and `spec.lineup`, `spec.guideSources`, `status.categories`,
`status.guideSources` and the channels' `spec.category` / `status.lineup` need the new schema.
Both IPTV CRDs, from a checkout at the commit being deployed:

```bash
kubectl --context admin@talos apply --server-side --field-manager=clustarr-deploy \
  -f config/crd/bases/clustarr.io_iptvproviders.yaml \
  -f config/crd/bases/clustarr.io_iptvchannels.yaml
```

Both are additive; the running manager keeps working on the old object shape until the deploy.

**Stop if** the apply is refused (a schema error): do not deploy; report the message.

## 1. Deploy clustarr and run the dry run

1. **Publish.** Push main, tag a release so CI builds the multi-arch `clustarr` and `native`
   images and the chart (the ui runs on the arm64 control plane: an amd64-only image leaves it in
   ImagePullBackOff). In cluster-bootstrap raise `chart_version` and the image tags (double-quoted).
   A `chart_version` bump restarts every pod; announce it first and wait for any in-flight deploy
   and for `/activities` to list no `provider.epg.load` on PMS.
2. **Stage the provider's new fields.** The source of truth is
   `docs/examples/iptvprovider-argon-lineup.yaml` (committed; its comments carry the rules'
   reasons). Copy its `spec.lineup` block (`enabled: false`), comments included, under the
   `argon-tv` entry of `iptvproviders` in cluster-bootstrap's `clustarr/resources.yaml`, where
   the spec's keys sit at four spaces (the example's sit at two), so:

   ```bash
   E=docs/examples/iptvprovider-argon-lineup.yaml
   sed -n "$(grep -n '^  # The argon-tv lineup' $E | cut -d: -f1),$(( $(grep -n '^  guideSources:' $E | cut -d: -f1) - 1 ))p" $E | sed 's/^/  /'
   ```

   Leave `spec.plexGuide.enabled: true` as it is. **`spec.guideSources.plex`** (the example's
   last block: `credentialsRef: {name: plex-token, key: token}`, `refresh: 24h`, `rate: 2`,
   eleven lineup ids) is **not** staged: the owner's call for talos (2026-11-10, "stick with the
   original iptv-epg.org setup") is no Gracenote fetched from Plex's guide service, so the
   category guides come from iptv-epg.org and the panel's guide alone, and every line below that
   reads `status.guideSources.plex` applies only if the owner later asks for it. Plan, read it,
   apply.
3. **Nothing moves yet.** A dry run renames and renumbers no channel, publishes no block and
   touches no DVR. The manager compiles the lineup, writes a plan object, and starts the Gracenote
   fetch.

### What to read

```bash
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv \
  -o jsonpath='{range .status.categories[*]}{.name}{"\t"}{.base}{"\t"}{.channels}{"\t"}{.deviceID}{"\t"}{.reason}{"\n"}{end}'
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv \
  -o jsonpath='{.status.guideSources.plex}{"\n"}'
```

- **`LineupApplied`** reads `False`, reason `DryRun`. Any other reason is a finding:
  `InvalidLineup` (a rule does not compile or a category is malformed; the provider still
  streams), `LineupPending` (only after the flip), `TooMany` (see below).
- **`status.categories`**: a name, base, channel count (and device id and reason once enabled).
  Expected, from Task 2's planner sample over `test/data/iptv/lineup_names.txt` (the 2026-11-10
  playlist's names, which **lacks the affiliate and streaming groups**, so talos's numbers will be
  larger for those): Sports about 114, Premium 46, `Entertainment / Drama & Crime` 42, News 41,
  Movies 34, `Entertainment / Reality & Lifestyle` 31, Kids 28, `Entertainment / Music` 22,
  `Entertainment / Comedy & Game Shows` 16, `Entertainment / Science, Nature & History` 15,
  `Entertainment / Faith & Family` 11, `Entertainment / Classic TV` 7, Local 1 (the sample has no affiliate groups, so it counts only the `^ABC$` match).
  `Entertainment / On Demand` (base 9500) is 0 in the sample, which had none of its groups, and
  **reads 0 on talos too unless `spec.filters` admits them**: talos's filters admit `USA: TV`, the
  affiliate network groups and the MLB/NBA packages, not the streaming or religious groups, and an
  entry no filter admits is no candidate for the lineup. Add, beside the existing filters (the
  `exclude` keeps the playlist's ✶ header entries out, as the others do):

  ```yaml
  filters:
    - {name: hulu, type: groupTitle, match: 'USA: HULU NETWORK [1080p]', exclude: ['✶']}
    - {name: hbo-max, type: groupTitle, match: 'USA: HBO MAX NETWORK [1080p]', exclude: ['✶']}
    - {name: disney, type: groupTitle, match: 'USA: DISNEY+ NETWORK [1080p]', exclude: ['✶']}
    - {name: peacock, type: groupTitle, match: 'USA: PEACOCK NETWORK [1080p]', exclude: ['✶']}
    - {name: paramount, type: groupTitle, match: 'USA: PARAMOUNT+ [1080p]', exclude: ['✶']}
    - {name: biblical, type: groupTitle, match: 'USA: BIBLICAL/RELIGIOUS [1080p]', exclude: ['✶']}
  ```

  The last one is `Entertainment / Faith & Family`'s first rule's group; without it that category
  holds only the names its second rule takes from `USA: TV` and `USA: ENTERTAINMENT`. Expect the
  ORIGINALS loops in On Demand once the five are in. **On talos Local must show the 11 affiliates plus the national ABC
  entry, 12** (WEAR, WALA, WKRG, WPMI and the New York flagships WABC, WNYW, WCBS, WPIX, WWOR, WNBC,
  WNET; the `^ABC$` rule is the extra): fewer or more means the `names` regexes or the
  ENTERTAINMENT group rule is wrong. Every category must be under 400.
- **`status.guideSources.plex`** (only with `spec.guideSources.plex` set; absent otherwise):
  `stations` (paired to a Gracenote station), `grids` fetched,
  `fetchedAt`, and `holdUntil` / `error` if Plex answered 429 (the pass holds for max(Retry-After,
  1 min) and resumes). The first pass is about 1,000 calls at 2 per second, around 10 minutes;
  `GuideSourcesReady` reads Pending until it ends. A long `holdUntil` is a finding: lower `rate`.
- **The Channels page** (`/livetv/channels`, the ui at clustarr.k8s.appkins.io; the layout below is
  described from the design and **untested on talos**, so trust the screen over this text): the dry-run banner
  with its counts (channels kept, dropped, duplicates), the per-row planned name, number and
  category, the **dropped** rows with their reason (the exclude rule or "no category"), and each
  duplicate beside the winner that took the channel. Read it as the owner reads a channel guide:
  are the wanted channels in, are the dropped ones meant to be, did each duplicate keep the right
  feed. A wrong category is fixed in the values (edit `lineup.categories`, apply, the plan
  recompiles in a pass) or per channel with the category picker, which writes `spec.category`.
- A category over the map budget (30,000 bytes: an XMLTV category's channel map costs 52 bytes
  plus 5 a digit per channel, 72 for a four-digit number and 77 for five, so 390 to 416 channels;
  the 116 bytes and 255 channels measured on 2026-11-04 are a cloud block's, whose keys are
  stations) or over 400 channels reads `TooMany`: split it by adding a rule that joins a second
  category.

**Stop if** `LineupApplied` reads `InvalidLineup`, any category reads `TooMany`, a category is
missing from `status.categories`, Local is not the 12 (11 affiliates plus ABC), or `status.guideSources.plex`
holds for long: fix the values and re-read; do not proceed to step 2 or 3. The action is to edit
the values (or remove `spec.lineup`), never a rollback of the deploy.

Iterate here for as long as needed. Nothing in step 1 needs rolling back: set nothing, or remove
`spec.lineup`.

## 2. Deploy cluster-plex with built XMLTV guides

In cluster-bootstrap's cluster-plex values set `plex.liveTV.xmltvGuide: manager` (default `pms`).
The same chart render adds the Jobs RBAC the build needs (the Role rule applies when `cloudGuide`
or `xmltvGuide` is `manager` and `cloudGuideBuild` is not `inProcess`). It is **inert** while
every block is the cloud one: only an XMLTV block asks for a build. Apply, then confirm the
Plex pods restarted cleanly and `kubectl -n clustarr-system get iptvprovider argon-tv` still
reads Ready. This can ride with step 1's apply when the owner prefers one pass.

`plex.liveTV.cloudGuide` is a **separate key** and stays as it is (`manager` on talos today); the
cutover changes `xmltvGuide` only.

**Stop if** a Plex pod does not come back ready or the provider leaves Ready: revert `xmltvGuide`
to `pms` (inert either way) and report.

## Pre-flight, before step 3

All three must pass before the flip; each is a measurement on the dry run, not a deploy.

1. **The manager's memory.** Without `spec.guideSources.plex` the render's cost is the
   whole-provider XMLTV it already carries today (about 800 channels where it serves 257): read
   the live heap during a dry-run render as below and record it; the rest of this item is the
   grid source's cost, for the day the owner asks for it. With it: raise talos's
   `manager.resources` (cluster-bootstrap values) to at
   least `2Gi`, the request equal to the limit (the chart's default is 640Mi requested, 1664Mi
   limit). After the flip the tuner fetches and renders a Gracenote grid for about 800 stations
   where it serves 248 today: each costs about 0.3 MB live and 4 MB of short-lived churn a render
   (the build's measure, 6 ms and 4 MB a grid), so about 240 MB live for the grids beside the
   whole-provider XMLTV, and 3 GB of churn a render. Once the first full grid pass has ended
   (`status.guideSources.plex.grids` near `stations`), read the live heap while the tuner renders
   (its render line in the manager log):

   ```bash
   kubectl --context admin@talos -n clustarr-system port-forward deploy/clustarr-manager 16060:6060 &
   curl -s 'http://127.0.0.1:16060/debug/pprof/heap?debug=1' | grep -E '^# (HeapAlloc|HeapInuse|Sys) '
   ```

   Record it. **Stop if** the manager is OOMKilled or restarts during the dry run's renders
   (`kubectl --context admin@talos -n clustarr-system get pods --chunk-size=0 -o
   custom-columns=NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount,LAST:.status.containerStatuses[0].lastState.terminated.reason
   | grep manager`): remove `spec.guideSources` (the grids stop and the guides come from
   iptv-epg.org) or roll back, and report the heap.
2. **The built guide parses.** Serve the dry run's whole-provider XMLTV (the tuner's range
   endpoint `/livetv/clustarr-system/argon-tv/xmltv.xml`, with its `lineup.json`, through a
   port-forward to `svc/clustarr-livetv`) on the kind rig's `xmltvprobe` as cluster-plex's
   `docs/guide-rig.md` ("The XMLTV oracle") serves its probe guide, reload the rig's DVR 8, and run
   cluster-plex's `hack/guidebuild -xmltv … -additions` against PMS's load of it. It must exit 0.
   The rig proof used 51 synthetic programmes; real titles with `'` and `/`, multi-episode shows
   and the HD rule are unprobed. A ConfigMap holds at most 1 MiB, so the whole guide goes into the
   probe pod another way (a volume it serves from) or a range (`?from=&to=`) of a few hundred
   numbers is served instead; the rig's `rig-x1` block needs new `lineupHash` and `guideHash`
   values so the converger saves the new map. **Stop if** it exits non-zero: report the diff.
3. **The guide's size.** Divide the whole-provider `xmltv.xml`'s bytes by its `<channel>`
   count, and multiply by the largest category's planned count (`status.categories[].channels`,
   or the Channels page's dry-run banner):

   ```bash
   curl -s http://127.0.0.1:18080/livetv/clustarr-system/argon-tv/xmltv.xml -o /tmp/argon.xml   # after port-forward svc/clustarr-livetv 18080:80
   wc -c < /tmp/argon.xml; grep -c '<channel ' /tmp/argon.xml
   ```

   That is the largest category guide cluster-plex will read. **Stop if** it is within a factor
   of two of cluster-plex's XMLTV cap (256 MiB once its fix wave lands; read the deployed value):
   over 128 MiB.

## 3. The flip, in one edit

**Before the edit, record the cloud block's device id** (the rollback compares against it):

```bash
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv \
  -o jsonpath='{.status.blocks[*].deviceID}{"\n"}'
```

Go/no-go, all must hold: the pre-flight passed, `status.guideSources.plex` complete (no
`holdUntil`, `GuideSourcesReady` True; skip this when `spec.guideSources.plex` is not set), no
`provider.epg.load` in PMS's `/activities`, step 1's
stop-ifs clear, and the owner has accepted the loss of the recording rules. "Complete" covers the
stations of the channels that exist now: the channels the flip creates are paired and fetched
after it (about 6 minutes at `rate: 2`), their guide comes from iptv-epg.org until then, and each
category guide's hash moves again as their grids land (one more build per DVR). Announce it. In cluster-bootstrap's argon-tv
object make exactly this edit and apply it:

```yaml
spec:
  plexGuide:
    enabled: false      # was true
  lineup:
    enabled: true       # was false
```

### What happens, in order

1. **The channels are renumbered** to their category's base plus place, renamed and
   (de)activated by the plan. That is about `2 x channels` apiserver writes at about 20 a second,
   so a minute or two for roughly 1,000 channels. The provider's `Ready` reads False, reason
   `LineupPending`, from the edit until every channel carries its plan (before the lineup
   controller's first enabled plan lands as well), and the cloud block is held meanwhile: its
   device and DVR 32 stay and it keeps streaming, but its channel map follows the channels as they
   are renumbered, so cluster-plex saves DVR 32's map again a few times before it deletes it. This
   is expected, not a fault; a message counts the channels still pending, and a channel that can
   never be applied holds the provider there (find it by the count and `kubectl describe` the
   channel). A `LineupApplied` of `InvalidLineup` "the plan would deactivate N of M active
   channels; refusing" is the collapse guard: stop and read the plan; set the
   `clustarr.io/lineup-allow-collapse: "true"` annotation only if that many should go.
2. **The category blocks are published**, each a device id under `cat/...` and a path
   `/hdhr/<id>`; the cloud block is gone from `status.blocks`. `status.categories[].deviceID`
   fills in and `LineupApplied` reads True.
3. **cluster-plex's converger** removes the DIRECTV device and DVR (rule: devices no block names
   go once every add succeeds), then per category: registers the device at its `uri`, creates
   its DVR, **waits for PMS's first load**, saves the channel map, and asks the manager's
   builder for the guide. **Plex Live TV is empty from the removal until the first DVR is
   loaded**, and each PMS first load takes 2 to 10 minutes on the Pi. The DVRs go **serially**,
   and cluster-plex cancels nothing while any map is pending, so each DVR costs two serial PMS
   loads: thirteen categories is 13 x 2 x 2-10 minutes, about two hours and up to four, before
   the last has a guide, during which cluster-plex defers other DVR changes. This is the cost; do
   the flip in an evening.
4. **The builds**: one Job per DVR (about a minute each) fetches the provider's guide and the
   Gracenote grids from the stores, builds the guide schema (episode stills, backgrounds,
   `plex://` GUIDs) and the holder swaps it in.

### What to watch

The manager is `deploy/clustarr-manager` and the build Jobs run beside it, both in `clustarr-system`
(cluster-plex's manager namespace on talos; the kind rig uses `media`).

```bash
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv -o jsonpath='{range .status.categories[*]}{.name}{"\t"}{.channels}{"\t"}{.deviceID}{"\t"}{.reason}{"\n"}{end}'
kubectl --context admin@talos -n clustarr-system get iptvprovider argon-tv -o jsonpath='{range .status.blocks[*]}{.deviceID}{"\t"}{.path}{"\t"}{.owner}{"\n"}{end}'
kubectl --context admin@talos -n clustarr-system get jobs -l clusterplex.io/guide-dvr --chunk-size=0 --no-headers -o wide
kubectl --context admin@talos -n clustarr-system logs deploy/clustarr-manager --since=15m | grep -i 'lineup\|livetv\|guide'
```

- Manager log: the lineup controller's pass (channels applied, plan stored), the tuner's
  renders (`withBlocks` publishes a device only once the tuner serves it), the grid fetch.
- cluster-plex's log (the Plex pod holding the lease): `DVRDeviceReplaced`/`DVRProvisioned`/
  `DVRChannelMapSaved` events per category, a PMS `provider.epg.load` activity per DVR, and the
  builder's completion. PMS's DVR list, read-only, through a port-forward to the PMS pod (cluster-plex's recipe in
  `docs/guide-rig.md`: forward to the `plex` pod in `clustarr-system`, authenticate with PMS's `.LocalAdminToken`):
  `GET /livetv/dvrs` should show one DVR per category and none for the cloud lineup. A device PMS
  marks dead is discovered again by the converger within a minute (`DVRDeviceRevived`).
- **Stop and roll back if** any of: `LineupPending` for longer than 10 minutes (read the message's
  pending count and the manager log for apply errors first); any category reads `TooMany` (it is
  held where it was, which at the flip is on no DVR); a DVR's
  first PMS load has not finished in 20 minutes; a build Job has failed. The action for each is the
  rollback below, after reading the cause.
- Never `reloadGuide` or POST a DVR by hand while a load is in progress (it deletes the DVR).

## 4. Verify

- **Plex:** Live TV shows one guide per category, in the order of their bases; a few channels per
  category tune; the guide rows carry titles, posters, stills and backgrounds; Local holds 12 (the 11
  affiliates plus the national ABC entry); a sports and a movie programme open with the right metadata. A Watchlist action works
  on a `plex://` programme.
- **Stop and roll back if** a category DVR is missing in Plex, a DVR has no guide after its first load, or the ui's TV Guide disagrees with Plex.
- **ui:** `/livetv/channels` reads the lineup (planned names and numbers, no dry-run banner), and
  the TV Guide page shows the same channels.
- **Cluster:** `LineupApplied=True`, `PlexGuideReady` not required (it is disabled), no category
  with a `reason`, every block with an `owner`.
- **Overnight:** read each DVR's `refreshedAt` the next morning (PMS's `GET /livetv/dvrs`, as
  above). If PMS's nightly `RefreshEpgGuides` reloaded the 13 XMLTV DVRs only to have the loads
  refused (the built guide stands), that is the Pi parsing 13 guides for nothing every night: tell
  the owner.
- Record in the memory note and the spec's As built: the Gracenote pass time and call count, the
  measured PMS first load per DVR, the build Job time per DVR, the render time and heap of the
  manager on talos (§5.9's list).

## Rollback

Reverse the flip in one edit:

```yaml
spec:
  lineup:
    enabled: false
  plexGuide:
    enabled: true
```

- The cloud block's device id derives from `plex/<lineupID>/<start>`. After the flip the cloud
  block has left `status.blocks`, so a rollback **repacks from the category numbers**, and its first
  block's `start` is `1001` only by the coincidence that News's base is 1000. Compare the new
  `status.blocks[*].deviceID` with the one recorded before the flip. **Same id:** cluster-plex
  re-registers that device and creates the DIRECTV DVR as it was, **minus its recording rules**.
  **Different id:** it is a new device and a new DVR, cluster-plex deletes the old one, and the
  recording rules are lost a second time. Either way the category devices and DVRs are removed,
  and Plex Live TV is empty for the cloud DVR's first load (the 2026-11-03 measurement,
  **measured elsewhere**, was about 21 minutes for 167 channels; expect similar, not that).
- The channels keep their planned numbers, names and activation until the next plan: with
  `lineup.enabled: false` the lineup controller stops owning `spec.number`, so the cloud block
  packs by the numbers they have now. Renumber by hand (the Channels page) or re-enable the
  lineup later; the DIRECTV numbering (the station's channel number) returns at the next cloud
  pairing pass, as in the 2026-11-04 cutover.
- If the built XMLTV guides misbehave but the categories are right, set cluster-plex's
  `plex.liveTV.xmltvGuide: pms` instead: PMS loads each DVR's XMLTV itself (poster only, no
  stills or backgrounds, title-keyed GUIDs, no Gracenote data) and the category DVRs stay.
- A Plex pod restart is not needed for any of this; the converger re-registers by itself.
