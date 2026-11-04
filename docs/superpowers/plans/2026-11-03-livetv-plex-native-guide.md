# Live TV on Plex's own guide: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A provider with `spec.plexGuide` pairs its active channels with a Plex cloud (Gracenote) lineup's stations, serves only the paired channels to Plex on blocks whose DVR uses that lineup, and drops the rest out of Plex, saying why.

**Architecture:** The tuner (manager, leader) fetches and caches the lineup from `epg.provider.plex.tv` and pairs channels in its render (pure package `pkg/iptv/plexguide`); the channel controller writes each channel's pairing into `status.plex`; the provider controller packs the paired numbers into Plex blocks (new device ids, `status.blocks[].lineup`) once `plexGuide.enabled`; each Plex block serves `plexmap.json`, which cluster-plex's converger saves on a DVR it creates with the cloud lineup.

**Tech Stack:** Go, controller-runtime, controller-gen v0.22.0, templ + htmx (ui), cluster-plex's `plexapi`.

**Spec:** `docs/superpowers/specs/2026-11-03-livetv-plex-native-guide-design.md`

## Global Constraints

- Lineup for talos: `657c8183afd0f02cd353633f`, title `DIRECTV Mobile (1009 channels)`.
- Cloud API: `https://epg.provider.plex.tv/lineups/<id>` (title) and `/lineups/<id>/channels` (stations), header `X-Plex-Token`, `Accept: application/json`.
- A lineup URI is `lineup://tv.plex.providers.epg.cloud/<id>#<title>`, the title escaped as PMS lists it (`DIRECTV%20Mobile%20%281009%20channels%29`).
- `match` values, verbatim: `Manual`, `Automatic`, `Ambiguous`, `Taken`, `Timeshifted`, `NotInLineup`, `Off`, `NoLineup`. Only `Manual` and `Automatic` reach Plex.
- A station absent from fetches keeps its channel for 7 days (`plexguide.MissingGrace`).
- Plex block device id: `hdhr.BlockDeviceID(ns, name, "plex/"+start)`.
- **Owner's rule for Live TV (2026-10-07): implementation first with compile checks only; every test is written and run in one pass at the end (Task 7).** No envtests, no `make test` (owner, 2026-10-08): the gate is `go build ./...`, `go vet` and `go test` of touched packages without `KUBEBUILDER_ASSETS`.
- GPL-3.0 header on every new Go file; slog from context; status writes through `k8s.PatchStatus`; every status list has `+kubebuilder:validation:MaxItems`; no floats in `api/`; metrics never labelled by title.
- Commit with a pathspec (`git add <new files>` first, then `git commit -m ... -- <paths>`); other sessions share both checkouts. Never push.
- `epgSource` is deleted cold turkey: no reader of it may remain in either repo.

## Review Focus

1. **The lineup is not loaded yet** (first start, missing token, a failed fetch): Plex blocks must keep their last published blocks, never empty out and take the DVR down. Test: `TestPlexBlocksWaitForTheLineup` (Task 7, owned by Task 4).
2. **Turning `enabled` on or off** switches block kinds: the previous kind's starts must not seed the new packing. Test: `TestSwitchingKindsPacksAfresh` (Task 7, owned by Task 4).
3. **A ui EPG edit must not wipe a channel's Plex choice** (today `EPG ""` writes `{"epg":null}`). Test: `TestEditChannelsKeepsThePlexChoice` (Task 7, owned by Task 5).
4. **A lineup title with `&`, `#` or `+`** must round-trip into the URI PMS stores. Test: `TestLineupURIEscapesAsPMSDoes` (Task 7, owned by Task 2).
5. **A paired channel renumbered** must move the block's `lineupHash`, so cluster-plex saves the new map. Test: `TestTheLineupHashCoversThePlexMap` (Task 7, owned by Task 3).

---

### Task 1: API and the removal of `epgSource` (clustarr)

**Files:**
- Modify: `api/clustarr/v1alpha1/iptvprovider_types.go`, `api/clustarr/v1alpha1/iptvchannel_types.go`, `api/clustarr/v1alpha1/defaults.go`, `api/clustarr/v1alpha1/conditions.go` (or wherever `ConditionGuidesFetched` lives)
- Modify (compile only): `app/livetv/controller/iptvprovider/{config,blocks,status}.go`, `app/livetv/tuner/{config,render,http,tuner}.go`
- Regenerate: `api/applyconfiguration/...`, `config/crd/bases/`, the chart's CRD copies

**Interfaces — Produces:**
- `IPTVProviderSpec.PlexGuide *PlexGuide` (`json:"plexGuide,omitempty"`):
  `type PlexGuide struct { Lineup string; CredentialsRef corev1.SecretKeySelector; Enabled bool }` (json `lineup`, `credentialsRef`, `enabled,omitempty`); `Lineup` MaxLength 512, pattern `^(lineup://tv\.plex\.providers\.epg\.cloud/)?[0-9a-f]{24}(#.*)?$`.
- `func (g *PlexGuide) LineupID() string`: the 24-hex id, prefix and `#title` cut.
- `IPTVProviderStatus.PlexGuide *PlexGuideStatus` (`json:"plexGuide,omitempty"`): `LineupID, Title string; Stations int32; FetchedAt *metav1.Time; Error string` (MaxLength 1024) and counts `Paired, Ambiguous, Taken, Timeshifted, NotInLineup, Off int32`.
- `const ConditionPlexGuideReady = "PlexGuideReady"`; reasons `ReasonLineupFetched = "LineupFetched"`, `ReasonLineupFailed = "LineupFailed"`, `ReasonLineupPending = "LineupPending"`.
- `BlockStatus.Lineup string` (`json:"lineup,omitempty"`, MaxLength 1024).
- `ChannelEPG.Plex *ChannelPlex` (`json:"plex,omitempty"`); `type ChannelPlex struct { Station string; Off bool }` (pattern `^[0-9a-f]{24}$` on `station`; CEL: exactly one of a non-empty `station` and `off: true`). `ChannelEPG`'s rule becomes "not both `channelID` and `dummy`", so `epg: {plex: ...}` alone is valid and maps by tvg-id as an absent `epg` does.
- `type PlexMatch string` (enum of the eight values) and `IPTVChannelStatus.Plex *ChannelPlexStatus`: `Match PlexMatch; Station, ID, CallSign, Title, VCN, TakenBy string; Candidates []string` (MaxItems 5, each `"<callSign> <vcn> <title>"`); `MissingSince *metav1.Time`.
- Deleted: `EPGSource`, its consts, `IPTVProviderSpec.EPGSource`, `EPGSourceOrDefault`; `tuner.Config.EPGSource`.

- [ ] **Step 1: Edit the types above, with doc comments naming the design's sections.**
- [ ] **Step 2: Delete every `EPGSource` reader:** `status.go`'s `NotUsed` guides condition (the guide condition is computed as for XEPG); `blocks.go` sets `GuidePath` for every non-Plex block; `config.go` stops setting it; the tuner's `pms` branches in `render.go`, `http.go` (`xmltv.xml` and the range guide) and `tuner.go`'s `dispatch` go.
- [ ] **Step 3: Run `make generate manifests`.** Expected: clean exit; `git status` shows the applyconfiguration, CRD and chart CRD changes only.
- [ ] **Step 4: Run `go build ./... && go vet ./api/... ./app/livetv/...`.** Expected: no output.
- [ ] **Step 5: Commit** (`feat(livetv): API for Plex's own guide; epgSource removed`), the generated files included.

### Task 2: `pkg/iptv/plexguide` — the lineup and the pairing (clustarr)

**Files:**
- Create: `pkg/iptv/plexguide/doc.go`, `lineup.go`, `pair.go`, `normalize.go`
- Create: `test/data/plexguide/directv-mobile-channels.json` (copy of the recorded `/lineups/657c8183afd0f02cd353633f/channels` answer, 1,000 stations; the session scratchpad's `gn/cloud-ch.json`) and `test/data/plexguide/argon-channels.json` (the 257 active channels' `number`, `name`, `epg.channelID`, from `kubectl get iptvchannels -n clustarr-system --chunk-size=0 -o json`, nothing else)

**Interfaces — Produces:**
- `const BaseURL = "https://epg.provider.plex.tv"`; `const MissingGrace = 7 * 24 * time.Hour`
- `func ChannelsURL(id string) string`, `func LineupURL(id string) string`
- `type Station struct { ID, GridKey, CallSign, AffiliateCallSign, Title, ShortTitle, VCN, Thumb string; HD bool }` (JSON tags as the cloud's: `id`, `gridKey`, `callSign`, `affiliateCallSign`, `title`, `shortTitle`, `vcn`, `thumb`, `isHd`)
- `type Lineup struct { ID, Title string; Stations []Station; FetchedAt time.Time }` (stored as JSON in the Live TV store)
- `func DecodeChannels(r io.Reader) ([]Station, error)`: `MediaContainer.Channel[]`, hidden ones skipped, body capped by the caller.
- `func DecodeTitle(r io.Reader) (string, error)`: `MediaContainer.Lineup[0].title`.
- `func LineupURI(id, title string) string`: `"lineup://tv.plex.providers.epg.cloud/" + id + "#" + strings.ReplaceAll(url.QueryEscape(title), "+", "%20")`.
- `type Match string` with the eight constants (same strings as the API's `PlexMatch`).
- `type Prev struct { Station string; MissingSince time.Time }`
- `type Channel struct { Object, Number, Name, ChannelID, Station string; Off, Timeshifted bool; Prev Prev }`
- `type Pairing struct { Match Match; Station Station; TakenBy string; Candidates []Station; MissingSince time.Time }`
- `func Pair(l Lineup, chans []Channel, now time.Time) map[string]Pairing` keyed by `Channel.Object`; deterministic (channels taken in `xmltv.CompareNumbers` order).
- `func Normalize(s string) string`

- [ ] **Step 1: `lineup.go`** — the URLs, the two decoders, `LineupURI`.
- [ ] **Step 2: `normalize.go`** — `Normalize`: lower case, `&`→`and`, `+`→`plus`, a trailing `.us`/`.ca`/`.uk` cut, non-alphanumerics dropped, then one trailing `hdtv|hd|east|usa|us|network|channel|tv` cut when something is left; and `baseCallSign(cs string) string` (a trailing `DT` or `HD` cut). An alias table `var aliases = map[string]string{}` of normalized channel names to normalized lineup keys, filled from `argon-channels.json`'s names that pair with nothing against the fixture lineup (FOX SPORTS 1→FS1 and the like); only names whose station is certain go in.
- [ ] **Step 3: `pair.go`** — `Pair`, in this order per channel (design §5.3):
  1. `Off` → `Off`. `Timeshifted` → `Timeshifted` (a manual station included).
  2. `Station` set → `Manual`, or `NotInLineup` if no station has that `GridKey`.
  3. `Prev.Station` listed → `Automatic` on it (sticky). `Prev.Station` not listed: within `MissingGrace` of `Prev.MissingSince` (or `now` when zero) → kept, `MissingSince` carried; past it → the rules below.
  4. Rules, stopping at the first that names exactly one station group (stations grouped by `baseCallSign`; a group's station is its HD one, else the lowest `VCN`): a call sign in `ChannelID` (each token of `ChannelID` split on non-alphanumerics, the token or its last four letters matching `^[KW][A-Z]{3}$` and naming a lineup group); then `Normalize(Name)` or `Normalize(ChannelID)` (through `aliases`) equal to a group's normalized base call sign, title or short title. A rule naming several groups → `Ambiguous`, `Candidates` the first 5 by `VCN`. No rule → `NotInLineup`.
  5. One channel per station: among channels on one station, `Manual` beats `Automatic`, then the lower number; the others become `Taken` with `TakenBy` = the holder's `Object`.
  An empty `Lineup` (no stations) answers `NoLineup` for every channel.
- [ ] **Step 4: Run `go build ./pkg/iptv/plexguide/ && go vet ./pkg/iptv/plexguide/`.** Expected: no output.
- [ ] **Step 5: Commit** (`feat(livetv): plexguide pairs channels with a Plex lineup's stations`), fixtures included.

### Task 3: The tuner — lineup fetch, pairing, Plex blocks (clustarr)

**Files:**
- Create: `app/livetv/tuner/plexlineup.go`
- Modify: `app/livetv/tuner/{config,tuner,render,http,cache,fetch}.go`, `pkg/iptv/livetvstore/livetvstore.go`

**Interfaces — Consumes:** Task 2's `plexguide` API.
**Interfaces — Produces:**
- `livetvstore.PlexLineupObject(key types.NamespacedName) string` = `"plex-lineup/<ns>/<name>"`.
- `tuner.PlexConfig struct { LineupID, Token string; Enabled bool }`; `Config.Plex *PlexConfig`.
- `ChannelConfig` gains `PlexStation string; PlexOff bool; PlexPrev plexguide.Prev`.
- `BlockConfig` gains `Lineup string` (the block's lineup URI; empty for an XMLTV block).
- `ChannelState` gains `Plex *plexguide.Pairing` (nil when the provider has no `Plex`).
- `Snapshot` gains `Plex PlexState`; `type PlexState struct { LineupID, Title string; Stations int; FetchedAt time.Time; Error string; Loaded bool }`.
- `GET /hdhr/<id>/plexmap.json`: a JSON array of `{"deviceIdentifier":<number>,"channelKey":<station ID>,"lineupIdentifier":<station VCN>}` in number order; 404 for a block without `Lineup`. `xmltv.xml` is 404 for a block with one.

- [ ] **Step 1: `plexlineup.go`** — `func (t *Tuner) fetchPlexLineup(ctx context.Context, p *provider, cfg Config)`: GET `LineupURL` then `ChannelsURL` with the token header through the tuner's fetch client (extend `get` with an optional `http.Header`), bodies capped at 8 MiB, errors redacted of the token (`cfg.scrub` learns it); on success store `plexguide.Lineup` with `putJSON` under `PlexLineupObject`, set `p.plex` and schedule a render; on failure keep the last lineup, record the error, back off with `nextBackoff`. Due at start when the cache is older than `cfg.Every` or its `ID` differs from `cfg.Plex.LineupID`, then every `cfg.Every`, and at once when the id changes (in `Apply`).
- [ ] **Step 2: `cache.go`** — `loadCache` reads `PlexLineupObject` when `cfg.Plex` is set and the cached `ID` matches.
- [ ] **Step 3: `tuner.go`** — `provider.plex` state (lineup, loaded, due, next, error); `dispatch` starts the fetch when due; `Snapshot` fills `Plex`.
- [ ] **Step 4: `render.go`** — with `cfg.Plex` set, build `[]plexguide.Channel` from the active, listed channels (`Timeshifted` = `TimeshiftMinutes != 0`), call `plexguide.Pair` with the loaded lineup (an unloaded one pairs nothing: `ChannelState.Plex` stays nil), and put each pairing in its `ChannelState`. For a block with `Lineup`: its entries as today, `sf.plexMap` from its numbers' pairings, `sf.lineupHash = sha(lineup JSON + plexmap JSON)`, no per-block guide (`guideObject` empty, `GuideHash` empty). The whole-provider guide and its index are still built for every channel (the ui's TV Guide).
- [ ] **Step 5: `http.go`** — `plexmap.json` and the `xmltv.xml` rule above in `serveDevice`.
- [ ] **Step 6: Run `go build ./... && go vet ./app/livetv/tuner/ ./pkg/iptv/livetvstore/`.** Expected: no output.
- [ ] **Step 7: Commit** (`feat(livetv): the tuner fetches the Plex lineup, pairs channels and serves plexmap.json`).

### Task 4: The controllers — config, Plex blocks, status (clustarr)

**Files:**
- Modify: `app/livetv/controller/iptvprovider/{config,blocks,status}.go`, `app/livetv/controller/iptvchannel/reconciler.go`, `app/livetv/tuner/tuner.go` (`ChannelView` carries `Plex`), `pkg/iptv/blocks/blocks.go`

**Interfaces — Consumes:** Task 1's API, Task 3's `PlexConfig`, `BlockConfig.Lineup`, `ChannelState.Plex`, `Snapshot.Plex`.
**Interfaces — Produces:**
- `blocks.Limits` gains `Cost func(number string) int` (nil = today's per-number cost); `blocks.MapBytesWith(numbers []string, cost func(string) int) int`; `func PlexCost(number, key, vcn string) int { return perChannelBytes + 3*len(number) + len(key) + len(vcn) }`.

- [ ] **Step 1: `config.go`** — `resolve` reads `plexGuide.credentialsRef` through `SecretReader` (a missing Secret or key: `Valid=False`, as the account credentials do); `buildConfig` sets `Config.Plex` and each channel's `PlexStation`, `PlexOff` and `PlexPrev` (from its `status.plex.station` and `missingSince`).
- [ ] **Step 2: `blocks.go`** — with `plexGuide.enabled`: the numbers are the active channels whose snapshot pairing is `Manual` or `Automatic`; before the snapshot has `Plex.Loaded`, keep the published blocks (`keep(ReasonLineupPending, ...)`); previous starts count only from blocks of the same kind (a Plex block has `Lineup`), so a switch packs afresh; device ids per the Global Constraints (`spec.device.id` applies to XMLTV blocks only); `Lineup` = `plexguide.LineupURI(id, snapshot title)`; no `GuidePath`; the byte limit uses `PlexCost` with each number's paired station. `tunerBlocks` passes `Lineup`.
- [ ] **Step 3: `status.go`** — `status.plexGuide` from `Snapshot.Plex` and the pairings' counts; condition `PlexGuideReady` (`LineupFetched`, `LineupFailed` with the error, `LineupPending`); `status.blocks[].lineup`; `reassertKnownStatus` carries `plexGuide`.
- [ ] **Step 4: `iptvchannel/reconciler.go`** — `status.plex` from the view's pairing (nil pairing: omitted), compared like `guide` so an unchanged pairing writes nothing; `Candidates` rendered `"<callSign> <vcn> <title>"`.
- [ ] **Step 5: Run `go build ./... && go vet ./app/livetv/... ./pkg/iptv/...`.** Expected: no output.
- [ ] **Step 6: Commit** (`feat(livetv): Plex blocks, plexGuide status and channel pairing status`).

### Task 5: The ui (clustarr)

**Files:**
- Modify: `ui/actions/livetv.go`, the Live TV channels projection in `ui/projection/` (the file holding `LiveTVChannels`), `ui/livetvstore.go`, `ui/views/` (the channels and sources templates), `ui/livetv*.go` handlers

**Interfaces — Consumes:** Task 1's status fields, Task 3's `PlexLineupObject`.
**Interfaces — Produces:** `actions.ChannelEdit.Plex *string`: `""` automatic (`spec.epg.plex` null), `"off"` (`{"off":true,"station":null}`), a 24-hex grid key (`{"station":<k>,"off":null}`), anything else refused.

- [ ] **Step 1: `actions/livetv.go`** — the `Plex` edit; `EPG ""` writes `{"epg":{"channelID":null,"dummy":null,"dummyMinutes":null}}` instead of `{"epg":null}`, so it keeps `plex`.
- [ ] **Step 2: Projection and Channels view** — each row shows the pairing (call sign, title, DIRECTV number, the station's `thumb`) with Manual or Automatic, or the reason it is off Plex (Ambiguous with candidates, Taken by which channel, Timeshifted, Not in lineup, Off); a filter "Off Plex"; the edit form's Plex field is a text input with a `datalist` of the cached lineup's stations (`"<callSign> — <title> (<vcn>)"`, value the grid key) plus "Automatic" and "Keep off Plex". The lineup is read through `ui/livetvstore.go` as the guide index is (decoded again only when its digest moves).
- [ ] **Step 3: Sources view** — `status.plexGuide` (lineup title, stations, fetched, error, counts) per provider.
- [ ] **Step 4: Run `make css` if classes changed, `templ generate`, `go build ./... && go vet ./ui/...`.** Expected: no output.
- [ ] **Step 5: Commit** (`feat(ui): Live TV channels show and edit the Plex pairing`).

### Task 6: cluster-plex — cloud-lineup DVRs

**Files (in `/home/appkins/src/mediactl/cluster-plex`):**
- Modify: `pkg/plex/livetv/watch.go`, `pkg/plex/livetv/converge.go`, `docs/configuration.md`

**Interfaces — Consumes:** `status.blocks[].lineup` and `/hdhr/<id>/plexmap.json` (Task 3).
**Interfaces — Produces:** `Block.Lineup string`; `Converger.HTTP *http.Client` (nil: a client with a 10 s timeout); `func (c *Converger) plexMap(ctx context.Context, uri string) ([]plexapi.ChannelMapping, error)` (GET `uri + "/plexmap.json"`, body capped at 1 MiB).

- [ ] **Step 1: `watch.go`** — read `lineup`; delete `Provider.EPGSource` and its default.
- [ ] **Step 2: `converge.go`** — a block with `Lineup`: `CreateDVR` with it; on a `lineupHash` change save `plexMap(uri)` (an empty map waits as today's `ErrEmptyChannelMap` does); never `ReloadGuide` and skip `emptyGuide`. Delete the `epgSource: PMS` wizard branch and `ReasonNeedsLineup`.
- [ ] **Step 3: `docs/configuration.md`** — a paragraph on Plex-guide blocks.
- [ ] **Step 4: Run `go build ./... && go vet ./pkg/plex/livetv/`.** Expected: no output.
- [ ] **Step 5: Commit** in cluster-plex (`feat(livetv): blocks on a Plex cloud lineup get a cloud DVR and the tuner's channel map`).

### Task 7: Tests, in one pass (both repos)

**Files:**
- Create: `pkg/iptv/plexguide/{lineup,pair}_test.go`, `app/livetv/tuner/plexlineup_test.go`
- Modify: `pkg/iptv/blocks/blocks_test.go`, `app/livetv/controller/iptvprovider/blocks_internal_test.go`, `ui/actions/livetv_test.go`, cluster-plex `pkg/plex/livetv/converge_test.go` (and `fakepms_test.go` as needed)

- [ ] **Step 1: `plexguide` tests** (fixtures from Task 2):
  - `TestDecodeChannelsReadsTheCloudShape`: 1,000 stations; `WKRGDT` has `GridKey "5fc70603336b43002e137ec6"`, `VCN "005"`, `HD true`, `AffiliateCallSign "CBS"`.
  - `TestLineupURIEscapesAsPMSDoes`: `LineupURI("657c8183afd0f02cd353633f", "DIRECTV Mobile (1009 channels)") == "lineup://tv.plex.providers.epg.cloud/657c8183afd0f02cd353633f#DIRECTV%20Mobile%20%281009%20channels%29"`, and a title `"A&B #1 + C"` escapes `&`, `#` and `+`.
  - `TestACallSignInTheGuideIDDecides`: `{Name:"ABC", ChannelID:"ABCEast_WABC.us"}` → `Automatic`, `CallSign "WABCDT"`, `VCN "396"`.
  - `TestANetworkNameAloneIsAmbiguous`: `{Name:"ABC"}` → `Ambiguous`; candidates include WEAR, WABC and KABC; at most 5.
  - `TestNamesMatchAfterNormalising`: `"A&E"` → `AETVHD`; `"ANIMAL PLANET"` → `APLHD`.
  - `TestAManualStationTakesItFromAnAutomaticOne` and `TestTwoAutomaticClaimsGoToTheLowerNumber`: the loser is `Taken`, `TakenBy` the winner's object.
  - `TestATimeshiftedChannelIsNeverPaired` (a manual station included), `TestOffIsOff`, `TestAManualStationNotInTheLineup` (`NotInLineup`), `TestAnEmptyLineupIsNoLineup`.
  - `TestAPairSticksWhileItsStationIsListed`: `Prev.Station` X listed, the name now matching Y → X, `Automatic`.
  - `TestAMissingStationKeepsItsChannelForSevenDays`: missing since `now-6d` → kept with that `MissingSince`; since `now-8d` → the rules decide.
  - `TestTheOwnersChannelsPair`: `argon-channels.json` against the fixture: at least 170 `Manual`/`Automatic`; 1001-1004 on `WABCDT`, `WCBSDT`, `WNBCDT`, `WNYWDT`; no station held twice.
- [ ] **Step 2: `blocks` tests** — `TestPlexCostCountsKeyAndVCN`: `PlexCost("1070", "657c8183afd0f02cd353633f-5fc705e2088e7d002d4c6de2", "265") == 116`; `TestNextUsesTheLimitsCost`: a cost of 1,000 bytes a number with `MaxBytes` 30,000 splits 40 numbers into two blocks.
- [ ] **Step 3: tuner tests** (`httptest` over `Tuner.Handler`, a stub lineup server for the fetch):
  - `TestPlexMapIsServedForAPlexBlockOnly`: a block with `Lineup` answers `plexmap.json` with its paired numbers in order and 404 for `xmltv.xml`; a block without answers 404 for `plexmap.json`.
  - `TestTheLineupHashCoversThePlexMap`: renumbering a paired channel moves the block's `LineupHash`.
  - `TestTheLineupFetchSendsTheTokenAndRedactsIt`: the stub sees `X-Plex-Token`; a failing fetch's `Snapshot.Plex.Error` does not contain the token.
- [ ] **Step 4: provider controller tests** (internal, fake client, no envtest):
  - `TestPlexBlocksHoldOnlyPairedChannelsUnderNewDeviceIDs`: enabled, a snapshot pairing 3 of 5 channels → one block, `DeviceID == hdhr.BlockDeviceID(ns, name, "plex/<start>")`, `Lineup` set, `GuidePath` empty, 3 numbers.
  - `TestPlexBlocksWaitForTheLineup`: enabled, `Snapshot.Plex.Loaded` false → the published blocks are kept, reason `LineupPending`.
  - `TestSwitchingKindsPacksAfresh`: published XMLTV blocks starting 1001 and 1200, enabled → the Plex blocks' starts come from a first packing of the paired numbers alone; disabling again → XMLTV blocks packed afresh.
  - `TestADryRunPairsButKeepsXMLTVBlocks`: `plexGuide` set, `enabled` false → XMLTV blocks, channel pairings present in the snapshot.
- [ ] **Step 5: ui test** — `TestEditChannelsWritesThePlexChoice` (`""` → `{"epg":{"plex":null}}`, `"off"` → `{"epg":{"plex":{"off":true,"station":null}}}`, a grid key → `{"epg":{"plex":{"station":"<k>","off":null}}}`, `"WABC"` refused) and `TestEditChannelsKeepsThePlexChoice` (`EPG ""` → `{"epg":{"channelID":null,"dummy":null,"dummyMinutes":null}}`; update the old `{"epg":null}` case).
- [ ] **Step 6: cluster-plex tests** (`fakepms`, an `httptest` tuner serving `plexmap.json`):
  - `TestAPlexBlockGetsACloudDVRAndTheTunersMap`: `CreateDVR`'s lineup is the block's `Lineup`; the saved map's query carries `channelMappingByKey[1070]=<id>` and `channelMapping[1070]=265`; `/livetv/epg/channelmap` is never asked.
  - `TestAPlexBlockIsNeverReloaded`: a `guideHash` change and an empty guide send no `reloadGuide`.
- [ ] **Step 7: Run** `go test ./pkg/iptv/... ./app/livetv/... ./ui/actions/` in clustarr and `go test ./pkg/plex/livetv/ ./pkg/plex/api/` in cluster-plex, without `KUBEBUILDER_ASSETS`. Expected: PASS (envtest suites skip). Falsify `TestACallSignInTheGuideIDDecides` and `TestPlexBlocksWaitForTheLineup` by reverting their rule once and watching each fail by name.
- [ ] **Step 8: Commit** each repo's tests (`test(livetv): ...`).

### Task 8: Docs and gates (clustarr)

**Files:** `CLAUDE.md` (Live TV section), `charts/clustarr/README.md` if it lists IPTVProvider fields, the design doc's status line.

- [ ] **Step 1: CLAUDE.md** — one paragraph after the provider bullets: `spec.plexGuide`, the pairing and its `match` values, Plex blocks and `plexmap.json`, uncovered channels off Plex, the dry run, the 1,000-station cap; delete every `epgSource` mention.
- [ ] **Step 2: Gates** — `go build ./...`; `go vet` and `golangci-lint run` on the touched packages; `make brand-gate`; `make generate manifests` leaves `git status` clean; `helm lint --strict charts/clustarr`. Expected: all clean (pre-existing failures named, not fixed).
- [ ] **Step 3: Commit** (`docs(livetv): Plex's own guide`). Report the commits of both repos; nothing is pushed or deployed (the §5.8 check and the cutover are the owner's next calls).
