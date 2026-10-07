# Manager, agents and ui: one reconciler process, domain workers, no external media programs

**Status:** Accepted for implementation, 2026-10-06. Supersedes
`docs/superpowers/specs/2026-09-24-unified-manager-design.md` and adopts the
topology ADR-0013 deferred (the superseding ADR is ADR-0018, §12; ADR-0016 is
"per-file work is MediaFile status", accepted on this branch the same day). Nothing in
this document is implemented or deployed. Building it is in scope; deploying
it to kind-cluster-plex needs a later, explicit OK from the owner (§1).

**Reconciled with main `80175fdc`, 2026-10-07.** The 28 commits
`0d3ae234..80175fdc` are folded in below (summary under "Drift since
`0d3ae234`"): the fork tag is now `.13`, on main's published `.12`; the MP4
standard's phase 1 changes what `cmd/transcode` writes and what it must
`Require`; the usenet connection budget is coming to `pkg/download/usenet`;
and the ui gained a mass editor and per-card actions, but no new grant.

**Adoption.** The owner asked for this to be designed, planned and
implemented in one request, so every owner decision in §13 is taken at its
recommendation, except the public actions, which wait for the owner:
OD18 (push the ffgo `clustarr/unify-media` branch and the
`v0.0.0-clustarr.13` tag), OD19 (publish par2go) and OD23 (report the
dropped `basepath` upstream). Until OD18 and OD19 are granted, go.mod
replaces ffgo and par2go with local directories (§7.6; the plan's W0.25
uses the worktree `../ffgo-unify`), and the images build from named
contexts.

**Drift since the review.** Main gained anime dual-audio phase 4 after the
review (`4bd5d7b9`..`cdbdd870`): the `AudioGraft` kind (transcode group),
an AudioGraft controller in squasharr that runs one graft Job on the cpu
pool pod with `squasharr-worker --graft-task`, `app/squash/worker`'s graft
run (`grafttask`), `pkg/engine` audio-graft support, and the
`--graft-concurrency` flag. Under this design the AudioGraft controller is a
manager reconciler like the TranscodeJob controller, the graft Job runs
`/usr/bin/transcode --graft-task` on the native image (§3.8's explicit
`Command`), and squasharr's graft flags move to `cmd/manager` with the other
squasharr flags. The plan carries this; the sections below predate it.

**Drift since `0d3ae234` (main `80175fdc`, 28 commits; the branch is rebased
onto it).** Each item names the sections it changed:

- **ffgo `.12` is published by main** (`ebbb2322`; fork commit `a184557`:
  `NewPacketFromData`, `(*Packet).Data`, `avcodec.NewPacket`, and the
  `avcodec` codec-id and extradata setters; no shim change). So this design's
  fork tag is **`v0.0.0-clustarr.13`**, cut from `.12` (R3, R12, §6.4, §7.4,
  §7.5, §7.6, §10.1.6, §11.1, OD18).
- **MP4 standard, phase 1** (`35db28ff`..`adf9372c`, gate commit `dea6d010`,
  with the fixes `f8eb8d90`, `a9848d35`, `c0fb39b7` and `adf9372c` after it, and
  the test fixes `64e46a8c`, `adbb865a`):
  `standard.Version` 2; every transcode writes `<stem>.mp4`
  (`worker.OutputContainer`); audio is E-AC-3 or AC-3 copied, or AC-3 5.1
  encoded, with an AAC 2.0 companion listed first; every text subtitle is
  written beside the output in the transcode's own pass
  (`pkg/transcode/engine/sidecar.go`), verified, placed before the swap and
  never over an existing name; image subtitles hold the file
  (`standard.HoldImageSubtitles`). This touches §3.8, §4.3 S1, §6.3, §6.8,
  §7.3 and §7.4 (the transcode class must `Require` the `ac3` encoder and the
  `mp4`, `srt` and `ass` muxers). Its new tests run the ffmpeg and ffprobe
  CLIs; the plan's U2 conversions (W6.20, W6.21) take them.
- **Sidecars follow the file** (`fb57194d`, `fa12e1b5`): `fsops.SidecarPath`
  names a sidecar or its part; `subtitles.SidecarsOf` lists every subtitle
  beside a video, so the rename (manager, `mediafilespec` after W2.21), the
  library delete (manager) and the import-list delete (agent `import`) move or
  recycle the transcode's own sidecars too. librarydelete and the rename now
  import `pkg/subtitles` as well; the manager already links it (§4.1), and
  W1.7 keeps its root free of astisub (§4.5.1).
- **Transcoded files are final against every search** (`22230298`,
  `991b7ced`, `e21885cc`): `pkg/decision` raises `TranscodedFinal` for a
  user-invoked search too, a Search `spec.grab` pick is exempt in
  `resolveGrab`, and an import refused over a transcoded file writes
  `ImportMessageExistingFileFinal`, which grabarr never blocklists. No process
  boundary moves (search controller in the manager, the decision in agent
  `catalog`, the importer in agent `import`, grabarr's phase derivation in the
  manager), so §5 is unchanged.
- **Usenet post-processing reports progress** (`38db94e6`): the Repairing,
  Extracting and Publishing stages write `status.message` from the stage's
  start time, and `job.repair` logs its start and finish. §8.1, §8.5.4, §8.6
  and OD24.
- **Usenet connection budget** (spec `e4b59a5c`, phase-A plan `db53ebd1`),
  to be built on main by another session (clustarr-c9), in
  `pkg/download/usenet`, the CRD and the engine's `BuildConfig`. Not landed.
  §3.5.3 says how the usenet-engine domain takes it, and what its phase B
  (several pods) would need from this design; §8.4.5 and §14 cover its
  interplay with the repair child.
- **ui** (`b77c30d2`, `0a39889a`): Sonarr's layout on shadcn-templ's registry
  components, a mass editor (`POST /library/{tab}/bulk`), a library-wide find
  (`GET /library/find`) and per-card hover actions. Every write goes through
  the existing `ui/actions` calls, so no grant, ui option or `cmd/ui` link
  changes (§3.6, §4.7).

**NATS research, 2026-10-07.** Three read-only research notes, written after
Waves 0-4e were built, amend this spec in place. Every amended passage is marked
**Amended 2026-10-07 (NATS research)** and names the note it rests on; the body
before each mark stays as the record of what was decided first:

- `.superpowers/unify/research/nats-hpa-metrics.md` (where the HPA's number
  comes from): §9.0, §9.2, §9.3 (`ConsumerState`), §9.4, §9.8, §9.9, §12.
- `.superpowers/unify/research/nats-worker-pools.md` (the pull loop and slow
  consumers; its defects D1-D7 and recommendations M1, M2, S1-S11): §3.3,
  §3.5.6, §9.2, §9.3, §9.6, §9.8, §9.9, §12. D1 (a saturated subscription
  dead-letters healthy tasks it never ran) is Wave 4c's; D2 (a fired schedule
  loses its ID, and its dead-letter copy is refused) is on main and predates this
  branch.
- `.superpowers/unify/research/nats-object-store.md` (artwork metadata, links
  and watches): §3.6, §4.5.2, §5.13, §5.15, §7.2.7, §10.3.2, §12. The artwork
  design (`2026-09-24-index-artwork-ratings-plex-design.md` §B) and the loop
  spec (`2026-10-06-mediafile-remediation-loop-design.md` §4.9, §4.15) carry
  the rest, amended the same day.

The owner's answers of 2026-10-07 bind: **(a)** fast lookups use deterministic
object names plus a full, versioned metadata map on every object, and there are
no object-store links, held by a guard that nothing creates one; **(b)** the ui
pushes cover changes to open pages with an SSE `art` event fed by its read-only
object-store watch; **(c)** the HPA metric stays JetStream `CONSUMER.INFO`
`NumPending + NumAckPending` per durable, not `/jsz` and not a stream's message
count (§9.0 records why, with the live numbers). The plan carries the work as
Wave 4f (W4.90-W4.115), between Waves 4e and 5.

**Basis.**

- Line references are to the worktree `/home/appkins/src/mediactl/clustarr-unify`,
  branch `unify-manager-agent`, at `7791f1f5`, unless a section says otherwise.
- Local `main` is 7 commits ahead (`599b288d`). Five of those commits change
  this design's inputs, and the spec already accounts for them:
  - `98d852ce` and `599b288d` add the **par2go** design and plan
    (`docs/superpowers/specs/2026-10-06-par2go-design.md`,
    `docs/superpowers/plans/2026-10-06-par2go.md`). par2go is being built in
    `/home/appkins/src/mediactl/par2go`. §8 is the "clustarr integration spec"
    its §8 defers to. **par2go basis: commit `1bd94eb`** (clean tree, untagged).
    Since the section drafts (`49b35a1`) it renamed its library to
    **`libpar2shim.so`** (`ebed9f6`; the exported ABI is still `p2_*`), moved to
    ABI 2 with each job on the shim's own thread polled from Go (`9907015`;
    `jobMu` still serialises jobs per process, `par2.go:30`, and a cancel still
    waits until C lets go of the job, `par2.go:105-123`), keeps a CMake build
    directory per host (`00f0fd2`), and releases `libpar2shim-linux-<arch>.so`
    with `SHA256SUMS`. Every library name in this spec is the new one.
  - **ffgo basis: master `9f7a3a4`** (`v0.0.0-clustarr.11`), clean tree.
  - `b3b077a2` raises `github.com/ebitengine/purego` to v0.11.1 (`go.mod:17` on
    main) and moves the ffgo replace to `github.com/mediactl/ffgo v0.0.0-clustarr.11`,
    a tag another session published today at ffgo `9f7a3a4` (`.10` plus the purego
    bump). So the purego bump §7.6 needs is done once this branch rebases (R14),
    and this design's fork tag was **`v0.0.0-clustarr.12`**, not `.11` as the
    section drafts assumed. **Superseded 2026-10-07:** main then published `.12`
    itself (`ebbb2322`, fork commit `a184557`), so the tag is **`.13`**, cut from
    `.12` (R12).
  - Main is now 20 commits ahead and moving; the others (anime audio policy,
    `pkg/audioalign`, ui forms) do not touch this design's packages beyond line
    numbers.
  - `eb766af0` and `ce0bd695` add the wrong-language rule. Its gate
    (`app/import/worker/fileimport/wronglanguage.go`) reads the import probe's
    `audio[].language` and keeps working unchanged under §6.6.
- Live-cluster facts come from a read-only inventory of kind-cluster-plex
  (`clustarr-system`, Helm revision 130, about 19:37Z on 2026-10-06). They will
  be stale by the time §11 runs; §11 re-reads them.

**Reading order.** §1 and §2 are the request and the binding rulings, with
every amendment marked. §3 to §10 are the design, in the order binaries,
packages, writers, probe, decode, par2, autoscale, installers. §11 is the build
order and the live cutover runbook, neither executed. §12 lists the docs and
ADRs to write, §13 every decision left to the owner, §14 the risks. Appendix A
records how the section drafts' contradictions were resolved, and Appendix B
the first review's findings and what was done with each.

---

## 1. Owner's request and decisions

The owner asked for:

- One manager: every reconciler (manager) function merged into one process,
  `cmd/manager`.
- Every worker registration, and its tests, moved out of `cmd/clustarr` into
  `cmd/agent` (whatever supports `--role worker` today).
- `cmd/segmentarr-worker` renamed `cmd/markers`, and `cmd/squasharr-worker`
  renamed `cmd/transcode`.
- `cmd/markers` and `cmd/transcode` use the in-process ffgo pattern and never
  run an ffmpeg executable.
- Every `cmd/*` imports only what its function needs, for binary size.

The owner then answered:

- The binaries are manager, agent and ui (plus markers and transcode).
  `cmd/clustarr` and `clustarr all` are removed.
- **Build, don't deploy.** No kind-cluster-plex rollout without a later explicit OK.
- markers shares the transcoder's FROM-scratch image. ffprobe is converted to
  ffgo. par2 is converted to `github.com/nzbgetcom/par2cmdline-turbo` used as a
  library.
- One Deployment per agent domain and no KEDA. Instead an HPA that scales to
  zero: at least 1 replica while any queue item exists, at most the number of
  nodes matching the pod's selectors, scaled on a per-agent-type concurrency
  limit.

---

## 2. Rulings

R1 to R14 are carried forward from `.superpowers/unify/rulings.md`. Where a
section draft proved a ruling unworkable as worded, the ruling is **amended**
here with the evidence; the amendment is binding for the rest of this spec.

**R1. Binaries and images.** `cmd/manager` (static, distroless), `cmd/ui`
(static, distroless), `cmd/agent` (native image), `cmd/markers` (native),
`cmd/transcode` (native). One static image `clustarr` holds manager and ui
(`images/Dockerfile.clustarr`); one FROM-scratch `native` image holds agent,
markers and transcode (`images/Dockerfile.native`), plus `native-debug`.
`images/Dockerfile.media` and `cmd/clustarr` are retired.

**R2. No external programs.** ffprobe (`pkg/mediainfo`), segment decode
(ffmpeg), embedded subtitle extraction (ffmpeg, captionarr fetch) and par2 all
become library calls (ffgo; par2cmdline-turbo through a purego shim). The
embedded-subtitle conversion is implied, not named by the owner (§13 OD8).
**Amended (par2, §8):**

- The library is **par2go** (`github.com/mediactl/par2go`), the owner-approved
  design on main, not an in-tree shim.
- The usenet engine calls it in a **short-lived child of its own binary**
  (`agent par2-repair`), not in the engine process. Evidence: a worker-thread
  exception or `abort()` in par2-turbo would crash-loop the whole engine
  through reattach; repair memory sits outside `GOMEMLIMIT`; par2go serialises
  jobs per process; and some phases cannot be cancelled, which a child bounds
  for every phase that is not stuck in uninterruptible kernel I/O (a child in
  D state on a hung NFS mount is reaped only when the I/O returns, as today's
  `par2` process was; §8.4.3). Still true: no image ships an external program,
  and the child is always `os.Executable()`. §13 OD21 lets the owner insist on
  in-process.

**R3. Probe placement.** The manager never links ffgo. The MediaFile reconciler
stays the only writer of `MediaFileStatus`. It requests probes as queued work
on a new Durable stream, answered by the agent's import domain, which links
`pkg/mediainfo/native`. The import domain's own probes (fileimport, rescan,
`ProbeAudio`) call `pkg/mediainfo/native` in-process. The ffgo probe must
produce a summary identical to ffprobe's for every stored field, and
`mediainfo.ProbeVersion` is not raised unless outputs differ. **Amended (§6):**

- "The squasharr ResultsConsumer pattern" cannot be copied literally.
  squasharr's consumer writes through a CAS path shared with its reconciler
  (`app/squash/controller/transcodejob/results.go:93`, `write.go:55,76`), while
  MediaFile's status apply carries no resourceVersion
  (`mediafile_controller.go:665-670`) and one incorporation writes labels and
  the spec takeover (`:346`) as well as status (`:441`). So results go only to a
  Durable KV bucket (`clustarr-probes`), and the manager's leader-only piece is a
  KV watch that only **enqueues** the MediaFile. The reconciler stays the single
  write path.
- **"The only writer of `MediaFileStatus`" is stated per field manager.** At
  the process level it was never literal: `status.markers` is written today by
  `catalogarr-markers` through `segmenting.Applier`'s compare-and-swap
  (`app/catalog/segmenting/apply.go:80-109`), and after the split that runs in
  agent `metadata`. The invariant this spec keeps, and §12 writes into
  CLAUDE.md, is: `catalogarr` (the MediaFile reconciler, in the manager) owns
  every `MediaFileStatus` leaf **except** `status.markers`; `status.markers` is
  owned by `catalogarr-markers` (agent `metadata`) through the Applier's CAS;
  the reconciler's `statusOf`/`statusAC` never declares `markers`
  (`TestMediaFileFieldManagersStayDisjoint` asserts the split, §6.8).
- "Not raised unless outputs differ" is reachable only after the ffgo fork
  additions (§7.5). With ffgo `.9` to `.12`, outputs differ on live files today
  (codec profiles on every file, channel layout on 249 streams, chapter times on
  44 files; main's `.12` added only packet and codec-parameter setters). The
  native probe cannot ship before `v0.0.0-clustarr.13`: a hard ordering, not an
  option.

**R4. Agent domains.** One Deployment per domain, `agent --domain <d>`, each a
non-electing controller-runtime manager used for cache, indexes, probes and
metrics. **Amended:**

- The events domain is named **`events`** (§13 OD2).
- **`catalogarr-redownload` moves from `catalog` to `events`.** Its durable is
  on `CLUSTARR_EVENTS`, a Limits stream (`pkg/events/topology.go:566-579,771-777`),
  and R4's own criterion puts consumers of non-work-queue streams in `events`.
- **The recycle sweep stays in `import` but stops being a timer.** Today it is
  an hourly `EveryReplica` timer (`app/import/run.go:558`,
  `app/import/worker/fileimport/recyclebin.go:40,100-110`), which never fires in
  a domain at zero replicas. The manager publishes a sweep task on a new
  `importarr-recycle` durable (§3.5.3).
- **The index domain is fixed at 1 replica with `strategy: Recreate` under both
  release-index engines**, not only SQLite. Under Postgres the chart pins no
  strategy (`charts/clustarr/templates/deployments.yaml:114-116`), so a rollout
  runs two per-process limiters against each indexer host: the 2026-10-06
  nzbgeek class. This holds until a shared KV limiter exists (§13 OD15).
- Engine argv is `agent --domain torrent-engine|usenet-engine` with no
  `/bin/sh`. The torrent ordinal comes from `POD_NAME` (then the hostname),
  parsed in Go (§3.5.5).
- **One domain per agent process, always.** `--domain` takes exactly one
  value and there is no `all`: the owner removed `clustarr all`, and a
  combined-domain agent would bring it back under another name. Development
  runs one agent process per domain (§3.10; §13 OD1).

Domain table as ruled (consumers in §3.5.3, scaling in §9):

| Domain | What | Replicas |
|---|---|---|
| `catalog` | search-high, search-normal, grab, artwork render | HPA 0..1 (max 1 while search and grab are throttled, R5 amendment, §9.1.1) |
| `events` | rss-matcher, history sink, DLQ projector, redownload | HPA 1..N |
| `metadata` | gateway, markers (TheIntroDB), segments-result, artwork fetch | fixed 1, Recreate (ADR-0007, RPC) |
| `import` | rescan, fileimport, import-list sync, recycle sweep, probe tasks | HPA 0..N |
| `index` | relindex, search/download/query RPC, RSS poll, facade, sweeper | fixed 1, Recreate |
| `caption` | subtitle fetch | HPA 0..1 (throttled, §9.1.1) |
| `torrent-engine`, `usenet-engine` | rendered by the manager's DownloadClient controller | per DownloadClient, no HPA |
| `markers` (binary `cmd/markers`) | `segmentarr-analyze` | HPA 0..N |

**R5. Autoscaling without KEDA.** The manager serves `external.metrics.k8s.io/v1beta1`
itself, hand-rolled (no `k8s.io/apiserver`), with a self-generated CA kept in a
Secret and injected as `caBundle`, and front-proxy client-cert authentication.
Metric `clustarr_consumer_lag{stream,consumer}` = `NumPending + NumAckPending`
through a new `events.StreamAdmin` method. A manager reconciler with its own
field manager owns one `autoscaling/v2` HPA per autoscaled Deployment (min 0,
max = matching nodes, one External metric per consumer at AverageValue =
per-pod concurrency, scale-up window 0, scale-down window ≥ longest AckWait).
HPAs are never templated by the chart. Topology owns `MaxAckPending`.
**Amended (§9):**

- **The APIService is rendered by the installers; the manager owns only
  `spec.caBundle`.** An APIService is cluster-scoped and cannot carry an
  ownerReference to the namespaced manager, so a manager-created one would
  outlive `helm uninstall` as an unavailable aggregated API, stall every
  namespace deletion in the cluster (`NamespacedResourcesDeleter.Delete` returns
  the discovery error before `finalizeNamespace`) and break `kubectl
  api-resources` (§13 OD28).
- **Stopping natsbus from writing per-pod `MaxInFlight` into `MaxAckPending` is
  unsafe on its own.** `Subscribe` uses `Consume`, which pulls again as soon as
  the non-blocking dispatch returns (nats.go@v1.53.1 `jetstream/pull.go:287-291`);
  parking is bounded only because `MaxAckPending == MaxInFlight`
  (`pkg/events/natsbus/subscription.go:42-49`). The ruling works only together
  with the slot-gated pull, lapse reclaim and membus parity of §9.3.
- **`Subscribe` binds and never creates**, for its work durable and for that
  durable's MAX_DELIVERIES dead-letter watcher (`clustarr-dlq-watch-<stream>-<durable>`
  on `CLUSTARR_ADVISORIES`), which `Subscribe` creates today
  (`pkg/events/natsbus/natsbus.go:331`, `deadletter.go:113-127`). The watchers
  become topology objects `events.Default()` derives, so only the manager's
  `EnsureTopology` writes consumer config (§5.9) and an older agent can never
  revert a newer topology. Because the manager is then the only process that
  recreates topology, it re-ensures after a NATS restart wipes memory-backed
  streams (`busconn.KeepTopology`, §5.9), and it also consumes every static
  watcher as a backstop, so a lapse a draining min-0 domain leaves behind is
  dead-lettered without waiting for the next wake.
- **401 versus 403.** No verified client cert, or no identity header, is 401; a
  front-proxy-CA cert whose CN is not allowed is 403, matching kube-apiserver.
  Both are tested, and neither returns data.
- **Throttled consumers keep today's `MaxAckPending`.** For
  `catalogarr-search-high`, `catalogarr-search-normal`, `catalogarr-grab` and
  the two `captionarr-fetch-*` durables the consumer-wide cap is the only
  cluster-wide bound on load a downstream per-process limiter already sets
  (indexarr's per-host limiter, the shared subtitle-provider token bucket), so
  raising it to `Slots × 8` would add replicas that cannot add throughput and,
  for search, turn queued work into `paced` skips that cost an item its live
  search (§9.1.1, §13 OD46). Their HPAs still scale 0..1.
- **The APIService outlives namespace deletion** even when installer-rendered:
  `kubectl delete ns` removes the manager and its Service but not the
  cluster-scoped APIService, which then blocks every namespace deletion in the
  cluster. The manager deletes it on its way out when its own namespace is
  terminating (`extmetrics.NamespaceGuard`, §9.4).
- **Amended 2026-10-07 (NATS research; owner decision (c)).** The metric and its
  source are confirmed against the alternatives the owner raised (the
  monitoring endpoint `/jsz`, a stream's `messages` count) with live numbers
  (§9.0). The slot-gated pull this ruling depends on no longer parks what it
  cannot run (§9.3, M1), and a fired schedule keeps its ID (§9.3, M2).

**R6. Package layout.** Per service an exported controller-side registration
package `app/<svc>/manager` and agent-side `app/<svc>/agent`, with leaf
extractions so that `cmd/manager` links no worker code, no sqlite/pgx, no
anacrolix/nntp, no ffgo/purego/onnx, no metadata clients it does not reconcile
with, and `cmd/ui` links no `app/*`. Each binary has a `go list -deps` guard
(§4.5).

**R7. Field managers and names.** Every `k8s.Manager*` string and every NATS
durable, consumer and stream name stays. A new field manager or CAS path is
introduced only where the split puts two processes on one manager that were one
process before. **Applied (§5.3):** the index agent's session-Secret and
facade-key writes move to the existing `indexarr-worker`, `indexarr` becomes
manager-only, and session `Drop` becomes a compare-and-swap. New names:
`clustarr-autoscale` (`k8s.ManagerAutoscale`, §9.5) and the pinned default
field owner `clustarr` (`k8s.DefaultFieldOwner`, §5.3.5).

**R8. Indexarr split.** Indexer, IndexerDefinition, IndexerProxy, DirectGrab
and the bundle loader (made leader-only) go to the manager, with the caps-probe
and login limiter local to the manager. RPC, RSS, facade, relindex and sweeper
go to the index domain, which builds its own ClientCache and limiter and
invalidates sessions across processes through the session KV. **Amended
(§5.12):** "re-apply spec rate limits on every client rebuild" must be
**generation-gated**. The cache misses on any resourceVersion mismatch
(`clientcache.go:315-330`), so a search holding a pre-edit `*Indexer` would
rebuild and revert an operator's `requestDelay` edit (the hazard
`source.go:220-230` documents).

**R9. Leader-only runnables move to the manager.** The 16 history replay
controllers, the artwork Reaper and the indexarr bundle loader (§5.13).

**R10. Tests.** Installer and source guards move to `test/guards`; shared test
helpers to `test/installtest`, `test/starttest`, `test/nativetest`; cross-identity
envtests to `test/system`. Per-binary CLI and wiring tests live with their cmd.
Start envtests split per identity. Every guard's intent is kept or retired with
a stated reason (the KEDA guard becomes the consumer-home guard) (§10.3).

**R11. Docs.** ADR-0018 adopts the topology and supersedes ADR-0013; ADR-0017
records "no external media programs" and supersedes the par2-is-an-exec
decision; `docs/autoscaling.md` replaces the KEDA docs; CLAUDE.md and the design
of record are rewritten; the 2026-09-24 design is marked superseded (§12).

**R12. ffgo fork.** New surface goes into `/home/appkins/src/mediactl/ffgo` on a
branch, tagged locally as the next `v0.0.0-clustarr.N`. That is **`.13`**: `.11`
was published at ffgo `9f7a3a4` (the purego bump), and main published `.12` at
`a184557` on 2026-10-06 for the MP4 standard (`ebbb2322`) and pins it. The branch
`clustarr/unify-media` is cut from `.12` in the worktree
`/home/appkins/src/mediactl/ffgo-unify` (other sessions edit `../ffgo`), and
clustarr's `go.mod` replaces ffgo with that worktree until the owner OKs pushing
the tag. If `.13` is taken when the branch tags, it takes the next free N (the
remediation-loop design's §8.4 collision rule).
**Amended (§7.6, §10.1.5):**

- A directory replace cannot build any image as written:
  `images/Dockerfile.transcoder:64` resolves the shim source with `go list -m`,
  and `../ffgo-unify` lies outside the build context. Every Dockerfile gains an
  empty `FROM scratch AS ffgo` stage that a BuildKit named context
  (`--build-context ffgo=<export>`) replaces.
- No GitHub CI job can build or test the branch while the replace is local.
  That is acceptable only because R14 forbids pushing anyway; `ci.yml` gains a
  "no local replace" check that names the cause.
- par2go is in the same position (an unpublished sibling module) and gets the
  same treatment: `replace github.com/mediactl/par2go => ../par2go` and a
  `par2go` named context, until the owner OKs its push.
- **The named contexts are clean exports, not the sibling working trees.**
  `../ffgo-unify` and `../par2go` are working trees with uncommitted state (and
  `../ffgo` beside them is main's, edited by other sessions), and par2go's holds a
  host CMake cache that breaks the image build, so the Makefile passes
  `git archive` exports of `v0.0.0-clustarr.13` and par2go `1bd94eb` and refuses
  a mismatched or dirty checkout (§7.6 item 3). go.mod's replaces still read the
  working trees for local builds and tests, as R12 says.

**R13. Torrent piece completion.** The agent builds with `CGO_ENABLED=1`, the
mode the engines run today, so seeding torrents keep the SQLite piece-completion
store and never rehash. Held by a `go list -deps` guard on
`github.com/go-llsqlite/adapter` (present only under cgo, checked) and by the
agent's `--self-check` (§10.1.1).

**R14. Branch hygiene.** Work only in `../clustarr-unify` on
`unify-manager-agent`; path-scoped commits; never `git stash`; never push;
rebase onto local main before handing back. Other sessions keep committing to
main.

---
## 3. Binaries, domains and process wiring

Five binaries replace `cmd/clustarr`, `cmd/segmentarr-worker` and
`cmd/squasharr-worker`. For each: command line, what starts and in what order,
probes, metrics, tracing, umask, signals and exit codes. Registration packages
are `app/<svc>/manager` and `app/<svc>/agent` (§4). Images and Deployment shapes
are §10.

### 3.1 The five binaries

| Binary | Replaces | CLI | Build | Path in image | Runs as | Lease | tracing `service.name` | NATS client name |
|---|---|---|---|---|---|---|---|---|
| `cmd/manager` | Every `--role controller` (catalogarr, importarr, indexarr's controller half, grabarr, squasharr, captionarr), plus the history replay controllers, `artwork.Reaper` and `bundle.Loader` (R9) | cobra | `CGO_ENABLED=0`, static | `/usr/bin/manager` in `clustarr` | Deployment, 1 replica, RollingUpdate | `manager.clustarr.io` | `manager` | `clustarr-manager@<version>` |
| `cmd/agent` | Every worker, metadata, history and artwork role; indexarr's workers; the grabarr engines | cobra | `CGO_ENABLED=1` (R13) | `/usr/bin/agent` in `native` | One Deployment per domain; engines rendered by the manager | none | `agent-<domain>` | `clustarr-agent-<domain>@<version>` |
| `cmd/ui` | `clustarr ui` | cobra | `CGO_ENABLED=0`, static | `/usr/bin/ui` in `clustarr` | Deployment | none | `ui` | `clustarr-ui@<version>` |
| `cmd/markers` | `cmd/segmentarr-worker` | pflag | `CGO_ENABLED=0` (purego, as `Makefile:167` builds it) | `/usr/bin/markers` in `native` | Deployment, HPA | none | `markers` | `markers/<POD_NAME>` |
| `cmd/transcode` | `cmd/squasharr-worker` | pflag | `CGO_ENABLED=0` (`Makefile:166`) | `/usr/bin/transcode` in `native` | pool Jobs rendered by the manager | none | `transcode` | `transcode/<POD_NAME>` |

- **Names.** `service.name` equals the `busconn.Connect` service argument, the
  convention today's services already follow (`importarr`, `catalogarr`,
  `squasharr-worker`; `cmd/clustarr/services.go:73`). The `clustarr-` prefix of
  NATS client names comes from `pkg/k8s/bus.go:95` and stays. Span names stay
  package-qualified (for example `indexer.reconcileDefinition`), so no
  per-service attribute is added. Each component keeps its logger name
  (`WithName("catalogarr")`), so filtering logs by component still works.
- **Binary paths** are constants in the new leaf package `pkg/binpath`
  (`Manager`, `UI`, `Agent`, `Markers`, `Transcode`), which every installer,
  renderer and Dockerfile guard reads (§10.1.1).
- **Every container sets `command:` explicitly**, in every manifest and every
  pod the manager renders (engine StatefulSets and Deployments, transcode pool
  Jobs). No image has an ENTRYPOINT, because one image holds two or three
  binaries. Today the usenet engine and the pool Jobs rely on the ENTRYPOINT
  (`app/grab/controller/downloadclient/workload.go:471-500`,
  `app/squash/controller/pool/template.go:370-373`) and the torrent engine uses
  `/bin/sh -c` (`workload.go:428-455`). All three change.
- **cobra for manager, agent and ui; pflag for markers and transcode.** The cobra
  binaries keep a `version` subcommand and a `--version` flag without the `-v`
  shorthand (`cmd/clustarr/root.go:109-112`), and the `PersistentPreRunE`
  process setup. Their command trees live in importable packages
  (`internal/cli/<bin>`), because the installer guards (R10) parse rendered argv
  through them. markers and transcode keep
  `func main() { os.Exit(run(os.Args[1:], os.Getenv)) }`: their exit codes are a
  contract with their pods (§3.7, §3.8), and they must not link `pkg/obs`
  (root) or `pkg/k8s`.

### 3.2 Shared CLI plumbing

**`internal/cli`** (light; imports only cobra, pflag, `pkg/version`,
`pkg/obs/obsflags` and `pkg/fsops`, so `cmd/ui` can use it; never `pkg/k8s`):

- every env-var constant from `cmd/clustarr/flags.go:38-155`, plus the new ones
  in §3.4.1;
- `EnvOr` and `EnvBoolOr` (`flags.go:177-191`);
- `NewVersionCommand(binary string)`, printing `<binary> <version.String()>` and
  `go <ver> <os>/<arch>`;
- `NewRoot(binary, short, long string)`: the root with `--version` (no
  shorthand) and `obsflags.Bind` on `PersistentFlags()` (`root.go:153`);
- `Main(newCmd func() *cobra.Command)`: `signals.SetupSignalHandler()` from
  `sigs.k8s.io/controller-runtime/pkg/manager/signals` (stdlib-only; the
  controller-runtime root re-export used today at `main.go:39` would pull the
  manager into `cmd/ui`), then `ExecuteContext`; on error print `error: <err>`
  to stderr and `os.Exit(1)`.

**`internal/cli/ctrlflags`** (manager and agent only; imports `pkg/k8s` and
`pkg/obs/metrics`):

- `BindManager(fs) *k8s.Options` binds all 11 flags of today's `bindCommonFlags`
  (`flags.go:198-233`), with `--leader-elect` defaulting to **true**.
- `BindAgent(fs) *k8s.Options` binds 8 of them, dropping `--leader-elect`,
  `--leader-election-namespace` and `--nats-single-node`.
- `RegisterMetrics()` is today's `registerMetrics` `sync.Once` around
  `metrics.Register(ctrlmetrics.Registry)` (`root.go:42-52`); the Once keeps
  repeated `Execute` calls in one test binary safe.
- `ParseGIDs` (`flags.go:283-297`).

**`internal/cli/manager`, `internal/cli/agent`, `internal/cli/ui`.** Each exports
`NewCommand() *cobra.Command` and has a package-level run seam
(`var runManager = Run`, `var runAgent = Run`, `var runUI = ui.Run`) replacing
`services.go:55-63`, which tests stub. `internal/cli/manager` also holds
`options.go` (`managerOptions`, the merged cache, §5.6);
`internal/cli/agent` holds `domains.go`, the table mapping each domain name to
its registration package's `Register` (§4.2.1).
 `cmd/<bin>/main.go` is one line, for example
`func main() { cli.Main(manager.NewCommand) }`.

**`PersistentPreRunE`.** manager and agent: `fsops.ApplyUmaskFromEnv()`, then
`ctrlflags.RegisterMetrics()`. ui: umask only; it registers no Prometheus
collectors and serves no `/metrics`, as today (`ui/routes.go:47-48`). Logging is
never set up here (`root.go:120-129`); each `RunE` calls `obs.Bootstrap` exactly
once.

**Exit codes of the cobra binaries.** 0 on clean shutdown after SIGTERM or
SIGINT; 1 on any error returned from `RunE`, or a second signal (the signals
package calls `os.Exit(1)`). A Go panic exits 2, which means nothing special
here: 2 is "retriable" only for markers and transcode.

### 3.3 Probes: one registration per process, namespaced names

**The problem.** controller-runtime v0.25.1 stores healthz and readyz checks in a
map (`cm.readyzHandler.Checks[name] = check`, `pkg/manager/internal.go:223-237`)
and never reports a duplicate. Today `pkg/k8s.AddProbes`
(`pkg/k8s/manager.go:314-326`) is called from six `run.go` files, each with
`ping`, `jetstream` and `cache`; in one process the last caller's checks
silently replace the earlier ones.

**Amended 2026-10-07 (NATS research; `nats-worker-pools.md` §3.5, §7 S1).** One
more local-fault liveness check, `bus`, beside `ffgo`, in the manager, every
agent domain and markers (on its `/healthz`): it fails while any subscription of
the process has sat at its lapsed cap (§9.3) for longer than its handler budget
(`HandlerBudget()`, §3.5.6 as amended) plus its first-delivery deadline, which
only handlers that ignore their context can cause, and which only a restart
cures. It reads the bus's own state (`events.WedgeReporter`, §9.3
"Handler budget"), never the broker, so a NATS outage never fails it. The table
below gains `bus` in the healthz column of the manager, agent (every domain) and
markers rows; transcode has none (its Job pod is fenced by its lease).

**The design.**

- **`pkg/k8s.Checks`**, an ordered set. `Add(name string, c healthz.Checker) error`
  rejects an empty name, a nil checker, a duplicate, and a name not matching
  `^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)?$`.
- **`k8s.AddProbes(mgr ctrl.Manager, ready, live *k8s.Checks) error`** registers
  healthz `ping` and readyz `ping`, plus every check in each set.
  `internal/cli/manager` and `internal/cli/agent` call it exactly once, after
  every component is registered. It is the only registrar; guard
  `test/guards.TestOnlyPkgK8sRegistersProbes` fails on an `AddReadyzCheck` or
  `AddHealthzCheck` selector outside `pkg/k8s`, and `pkg/k8s.TestAddProbesRejectsDuplicates`
  holds the set.
- **Registration functions return their checks** and never call the manager's
  check methods.
- **Naming.** Process-level checks are unprefixed and occur once per process:
  `ping`, `jetstream`, `cache`, `ffgo`. Domain checks are `<domain>.<check>`,
  served also at `/readyz/<name>` (for example `/readyz/index.releaseindex`),
  so a check's name says which domain's registration owns it. Nothing reads
  per-check names today: e2e (`test/e2e/zzz_observability_test.go:268-308`) and
  `docs/observability.md` read the aggregate `/readyz` only.
- **Liveness is `ping`**, the existing rule (`manager.go:306-313`): an external
  outage must never restart a pod. The one addition is **`ffgo`**
  (`ffruntime.Healthz`, §7.4) in processes that link ffgo: a wedged in-process
  FFmpeg is a local fault a restart cures, the in-process analogue of a hung
  child the old code SIGKILLed.

| Process | readyz checks | healthz checks |
|---|---|---|
| manager | `ping`, `jetstream`, `cache`. No `external-metrics` check: the External Metrics API is reached through its own Service, which publishes not-ready addresses (§9.4), so the manager's readiness neither gates nor is gated by it | `ping` |
| agent (every domain) | `ping`, `jetstream`, `cache` | `ping` |
| agent `import` | adds `import.data`: `DataReadyChecker(--data-dir)`, moved from `app/import/dataready.go:57` to `pkg/k8s.DataReadyChecker` | adds `ffgo` |
| agent `caption` | adds `caption.data`: the same checker. New: the fetch worker writes sidecars into `/data` but had no gate | adds `ffgo` |
| agent `index` | adds `index.releaseindex`: `IndexReadyChecker(store)` (`app/indexer/run.go:475-490`) | `ping` |
| agent `torrent-engine` | adds `torrent-engine.reattach`: `proxyReadiness(e.HealthzCheck, pcfg)` (`app/grab/run.go:555`) | `ping` |
| agent `catalog`, `events`, `metadata`, `usenet-engine` | nothing extra; the index asserts stay fatal at start, as `assertWorkerIndexes` is today (`app/catalog/wiring.go:165-192`) | `ping` |
| ui | `/healthz` and `/readyz` on `--bind-address`, from `WaitForSync` and `Projected` (unchanged) | — |
| markers | `/readyz` (NATS connected and subscription bound) on `--metrics-bind-address`. New: segmentarr-worker had no probes | `/healthz` (always 200; a wedged FFmpeg exits the process instead, §3.7) |
| transcode | none (a Job pod) | — |

**The manager registers no `data` check**, unlike today's importarr controller:

- Readiness never gated a leader's reconcilers (an unready pod can hold the
  lease), so importarr's claim that the controllers "must not accept work"
  without `/data` (`app/import/run.go:350-352`) was never enforced.
- The manager's readiness takes its pod out of the `manager` Service's
  endpoints; a one-minute NFS stall would make the metrics endpoint flap for no
  gain. (The External Metrics API has its own Service and does not depend on
  this readiness, §9.4.)
- `/data` failures surface per reconcile and requeue (`FileMissing` at
  `mediafile_controller.go:252-262`, `StatfsFailed`/`DiskSpaceOK` at
  `downloadclient/controller.go:204-209`), as the grab, squash and caption
  controllers have always worked (§13 OD7).

### 3.4 cmd/manager

#### 3.4.1 Command line

`manager [flags]` and `manager version`. The log and tracing flags
(`--log-level`, `--log-format`, `--log-add-source`, `--tracing-enabled`,
`--tracing-endpoint`, `--tracing-insecure`, `--tracing-sample-ratio` default 1)
come from `obsflags.Bind`.

| Flag | Default | Env | Comes from | Used by |
|---|---|---|---|---|
| `--metrics-bind-address` | `:8443` | — | every service | controller-runtime metrics server (`"0"` disables) |
| `--metrics-secure` | `true` | — | every service | |
| `--health-probe-bind-address` | `:8081` | — | every service | `/healthz`, `/readyz` |
| `--pprof-bind-address` | `""` | — | every service | |
| `--leader-elect` | **`true`** (was `false`; every controller manifest passed `--leader-elect`) | — | every service | lease `manager.clustarr.io` |
| `--leader-election-namespace` | `""` (falls back to `--namespace`) | — | every service | |
| `--namespace` | `$POD_NAMESPACE` | `POD_NAMESPACE` | every service | the lease; `metadataprovider.Bootstrap`; `subtitleprofile.Bootstrap`; pool Jobs; the external-metrics Secret |
| `--watch-namespace` | `[]` | — | every service | cache; `wantedcron.Runnable.Namespaces` |
| `--nats-url` | `$NATS_URL`, else `nats://clustarr-nats:4222` | `NATS_URL` | every service | bus; handed to engine pods (`EngineRuntime.NATSURL`) and pools (`pool.Config.NATSURL`) |
| `--nats-single-node` | `false` | — | every service | `EnsureTopology` (only the manager ensures) |
| `--graceful-shutdown-timeout` | `60s` | — | every service | |
| `--data-dir` | `/data` | — | grabarr, squasharr and captionarr `--data-dir`; importarr `--data-path` | rename, librarydelete, Download data removal, DownloadClient statfs, RootFolder probe, MediaFile stat, SubtitleRequest ReadDir; the mount path stamped onto engines and pools |
| `--engine-scratch-dir` | `/scratch` | — | grabarr controller `--scratch-dir` | scratch mount path the DownloadClient controller stamps onto engines |
| `--native-image` | `""`, required | `CLUSTARR_NATIVE_IMAGE` | grabarr `--engine-image` (`CLUSTARR_ENGINE_IMAGE`) and squasharr `--worker-image` (`CLUSTARR_WORKER_IMAGE`), merged | engine pods and pool Jobs; R1 makes them one image (§13 OD3) |
| `--data-claim` | `clustarr-data` | `CLUSTARR_DATA_CLAIM` | grabarr and squasharr `--data-claim` | engine and pool `/data` claim |
| `--engine-service-account` | `grabarr-engine` | `CLUSTARR_ENGINE_SERVICE_ACCOUNT` | grabarr | engine pods |
| `--slots` | `cpu=2,intel=1,nvidia=1` | — | squasharr | transcode admission |
| `--intel-render-groups` | `""` | `CLUSTARR_INTEL_RENDER_GROUPS` | squasharr | Intel pool `supplementalGroups` |
| `--gpu-node-label-nvidia` | `nvidia.com/gpu.present` | `CLUSTARR_GPU_NODE_LABEL_NVIDIA` | squasharr | |
| `--gpu-node-label-intel` | `intel.feature.node.kubernetes.io/gpu` | `CLUSTARR_GPU_NODE_LABEL_INTEL` | squasharr | |
| `--job-window` | `32` | — | squasharr | |
| `--job-retention` | `24h` | — | squasharr | |
| `--cardigann-definitions-dir` | `""` | `CLUSTARR_CARDIGANN_DEFINITIONS_DIR` | indexarr | `bundle.Loader`, now the manager's (R9); the chart's cardigann mount moves to the manager |
| `--cardigann-bundled` | `true` | `CLUSTARR_CARDIGANN_BUNDLED` | indexarr | `bundle.Loader` |
| `--trakt-base-url` | `""` | `CLUSTARR_TRAKT_BASE_URL` | importarr | ImportList controller's device-code flow |
| `--autoscale` | `true` | — | new (R5) | the HPA reconciler, Server and CertManager (§9.4); requires the External Metrics API enabled |
| `--external-metrics-bind-address` | `:6443` | — | new (R5) | External Metrics API server (`"0"` disables it, and `--autoscale` must then be false); container port `extmetrics` |
| `--external-metrics-service` | `external-metrics` | `CLUSTARR_EXTERNAL_METRICS_SERVICE` | new (R5) | serving-cert SANs; the APIService's `spec.service.name` the CA injector checks; the Service `NamespaceGuard` matches (§9.4) |
| `--external-metrics-secret` | `external-metrics-tls` | `CLUSTARR_EXTERNAL_METRICS_SECRET` | new (R5) | Secret holding the self-generated CA and serving cert |
| `--legacy-lease-check` | `true` | — | new, cutover release only | wraps the lease lock in `k8s.GatedLeaseLock` (§5.8) |

**Retired from the manager:** `--role`; `--engine` and `--publish-dir`
(engine-only); `--sample-max-bytes`, `--plex-base-url`, `--index-path`,
`--index-dsn` and `--facade-*` (agent-only); `--engine-image` and
`--worker-image`, merged into `--native-image`.

**Validation, before anything touches the cluster:** `--nats-url`,
`--native-image`, `--data-claim`, `--engine-service-account` and `--data-dir`
non-empty; `--slots` parses through `squasharr.ParseSlots` and
`--intel-render-groups` through `ParseGIDs`; both GPU label keys pass
`validation.IsQualifiedName`; `--autoscale` implies an enabled
`--external-metrics-bind-address`; then `k8s.Options.Validate()` (a lease needs
a namespace).

#### 3.4.2 Start order (`internal/cli/manager.Run`)

1. **Validate** the options (§3.4.1).
2. **Bootstrap observability.** `obs.Bootstrap(ctx, lo, to)` with
   `to.ServiceName = "manager"`. Tracing's first caller wins
   (`pkg/obs/tracing/tracing.go` `Setup` installs only when `setupRefs == 0`), so
   this is the process's one name.
3. **`k8s.RegisterRESTClientMetrics()`** (already `sync.Once`, `manager.go:289-304`).
4. **Build the manager.** `ctrl.GetConfig()`, then
   `ctrl.NewManager(cfg, k8s.WithBaseContext(managerOptions(o), ctx))`.
   `managerOptions` is §5.6: lease `manager.clustarr.io`, lease timings
   60/45/5 s (`pkg/k8s/manager.go:192-196`), the 10-minute `CacheSyncTimeout`,
   managedFields stripped, `Client.FieldOwner = "clustarr"`, `DisableFor`
   Secret and ConfigMap, the pool-Job `ByObject`, no MediaFile `status.mediaInfo`
   strip, and (with `--legacy-lease-check`) `LeaderElectionResourceLockInterface`
   set to a `k8s.GatedLeaseLock` that holds election shut while any of the six
   legacy `<svc>.clustarr.io` leases is live (§5.8). The gate never exits the
   process, so it never trips liveness and never crash-loops.
5. **Connect the bus.** `busconn.Connect(o.NATSURL, "manager",
   busconn.WithHooks(obs.BusHooks()))`, deferring the close of both `nc` and
   `bus`.
6. **`busconn.EnsureTopology(ctx, bus, o.BusTopology())`.** The manager is the
   only process that creates or updates streams, consumers (the static
   dead-letter watchers included), buckets and object stores (§5.9). A failure
   stops startup. Then `mgr.Add(k8s.LeaderOnly(...))` of
   `busconn.KeepTopology(ctx, nc, bus, o.BusTopology())` and of
   `busconn.WatchDeadLetters(ctx, bus, o.BusTopology())` (§5.9).
7. **Process-level checks:** `jetstream` (`busconn.ReadyChecker(nc, bus)`) and
   `cache` (`k8s.CacheSyncChecker(mgr)`).
8. **Register components** in this fixed order, each
   `Register(mgr ctrl.Manager, bus events.Bus, o Options) error` from
   `app/<svc>/manager`, a failure aborting start: catalog, import, index, grab,
   squash, caption; then `app/autoscale.Register` (R5), which adds no
   readiness check (§3.3).
9. **`k8s.AddProbes(mgr, ready, live)`.**
10. **Log** `starting` with the component list, the lease ID and `leaderElection`.
11. **`mgr.Start(ctx)`.** Return its error; a clean stop exits 0.

#### 3.4.3 What the manager runs

**L** is leader-only (a controller, or a runnable whose `NeedLeaderElection()`
is true); **E** is every replica (`k8s.EveryReplica`). A new
`pkg/k8s.LeaderOnly func(ctx) error` with `NeedLeaderElection() bool { return true }`
mirrors `EveryReplica` (`manager.go:348-355`), so every runnable declares its
election explicitly; `TestNoBareRunnableFunc` already bans bare `RunnableFunc`.

| Component | Reconcilers (L) | Other runnables | Today |
|---|---|---|---|
| catalog (`app/catalog/manager`) | movie, series, episode, artist, album, author, book, audiobook, comic, issue, mediafile (with the probe-record KV watch `probeRecordsSource` as a raw source, §6.5.3), rootfolder, qualityprofile, delayprofile, metadataprovider, search, overlayprofile, the metadata refresher (`app/catalog/controller/metadatarefresh`), and the 16 `replay-<kind>` controllers (`app/catalog/history/replay`) | `qualityprofile.Bootstrap` (L); `metadataprovider.Bootstrap` (L); `wantedcron.Runnable` (L); the segment planner on durable `catalogarr-segments-plan` (E); `artwork.Reaper` (L, `app/catalog/artwork`) | `app/catalog/run.go:344-470`; replay moves from the history role (`:632-639`), the reaper from the metadata role (`:848-853`) |
| import (`app/import/manager`) | libraryscan, rename, rootfolderschedule, importexclusion, librarydelete, importlist, retrigger (`app/import/controller/retrigger`) | `recyclesweep.Scheduler` (L, new, §3.5.3) | `app/import/run.go:379-453` |
| index (`app/indexer/manager`) | indexer (its own limiter and no ClientCache, R8), indexerdefinition, directgrab (`app/indexer/controller/directgrab`), indexerproxy | `bundle.Loader` as `k8s.LeaderOnly`; today `EveryReplica` (`indexer/run.go:663`), repeating about 1,500 API calls on every pod start | `app/indexer/run.go:595-667` |
| grab (`app/grab/manager`) | downloadclient, the blocklist sweeper, download | — | `app/grab/run.go:353-383` |
| squash (`app/squash/manager`) | transcodeprofile, transcodejob | `ResultsConsumer` on durable `squasharr-transcode-results` (L, `results.go:66`) | `app/squash/run.go:383-415` |
| caption (`app/caption/manager`) | subtitleprofile, subtitleprovider, subtitlerequest | `subtitleprofile.Bootstrap` (L) | `app/caption/run.go:244-271` |
| autoscale (`app/autoscale`, R5) | the HPA reconciler (`app/autoscale/controller`) | `extmetrics.Server` (E, a `*manager.Server`, started before the caches, §9.4); `extmetrics.CertManager` (L); `extmetrics.QueueGauge` (L); `extmetrics.NamespaceGuard` (E) | new |
| process | — | the `CacheSyncChecker` runnable (E); `busconn.KeepTopology` (L) and `busconn.WatchDeadLetters` (L) (§5.9) | `pkg/k8s/manager.go:372-403`; the other two new |

`TestControllerNamesAreUniqueAcrossTheBinary` becomes load-bearing: it must
cover the replay controllers beside the typed reconcilers (`replay-movie`
versus `movie`). controller-runtime also rejects a duplicate name at
registration (`pkg/controller/name.go:37`).

**`/data`** is mounted read-write at `--data-dir`, with UMASK applied, for the
seven manager-side filesystem users listed in §5.14.

**Metrics.** The `clustarr_` collectors (registered once) and the REST client
metrics, on `--metrics-bind-address`. The manager also becomes the producer of
`clustarr_work_queue_pending{stream,consumer}`, declared at
`pkg/obs/metrics/domain.go:247-255` and set by nothing today, from the same
ConsumerInfo poll that serves the External Metrics API (`extmetrics.QueueGauge`,
§9.4).

### 3.5 cmd/agent

#### 3.5.1 Command line

`agent --domain <d> [flags]`, `agent version`, `agent --self-check`
(§10.1.1) and the par2 child `agent par2-repair` (dispatched before cobra,
§8.4.4). `--domain` is validated in `RunE`, not by `MarkFlagRequired`, so
`agent version` and `--self-check` work without it.

**Domains:** `catalog`, `events`, `metadata`, `import`, `index`, `caption`,
`torrent-engine`, `usenet-engine`.

**One domain per process** (R4 amendment, §13 OD1). `--domain` takes exactly
one name; a list, `all`, an unknown name or an empty value is rejected with
`--domain takes exactly one of catalog, events, metadata, import, index,
caption, torrent-engine, usenet-engine`. So a process never holds two domains'
field indexes, checks, caches or engine state (the proxied torrent engine
replaces `net.DefaultResolver` for the whole process, `app/grab/run.go:496-498`).
Guard `test/guards.TestEveryAgentDeploymentRunsOneDomain` additionally holds
that each domain appears in exactly one Deployment per installer.

**Common flags** (every domain):

| Flag | Default | Env | Notes |
|---|---|---|---|
| `--domain` | — (required) | — | above |
| `--metrics-bind-address` | `:8443` | — | |
| `--metrics-secure` | `true` | — | |
| `--health-probe-bind-address` | `:8081` | — | |
| `--pprof-bind-address` | `""` | — | |
| `--namespace` | `$POD_NAMESPACE` | `POD_NAMESPACE` | an engine finds its DownloadClient here; the facade's key Secret lives here |
| `--watch-namespace` | `[]` | — | |
| `--nats-url` | `$NATS_URL`, else `nats://clustarr-nats:4222` | `NATS_URL` | |
| `--drain-timeout` | derived (§3.5.6) | — | how long running handlers keep their context after shutdown begins (`events.Subscription.Drain`, §9.3) |
| `--graceful-shutdown-timeout` | derived (§3.5.6) | — | an explicit value wins |
| `--topology-wait` | `45s` | — | §3.5.2 step 7; below the liveness budget (15 s initial delay plus 3 × 20 s) |
| log and tracing flags | as for the manager | | |

Per-pod handler slots are not a flag. They come from each durable's
`events.ConsumerSpec.Slots`, overridable by the `CLUSTARR_CONSUMER_SLOTS` env
(`segmentarr-analyze=3,catalogarr-grab=8`), which the autoscale reconciler reads
from the same pod template (§9.2, §9.5).

**Domain flags.** Setting a flag (`Flags().Changed`) for a domain the process
does not run is an error, for example
`--index-path is only meaningful with --domain index`. Env-derived defaults do
not count as set.

| Flag | Default | Env | Domains | Comes from |
|---|---|---|---|---|
| `--data-dir` | `/data` | — | import, caption, torrent-engine, usenet-engine | importarr `--data-path` (renamed); captionarr and grabarr `--data-dir` |
| `--sample-max-bytes` | `52428800` (`fsops.DefaultSampleMaxBytes`) | — | import | importarr |
| `--trakt-base-url` | `""` | `CLUSTARR_TRAKT_BASE_URL` | import (list worker) | importarr |
| `--plex-base-url` | `""` | `CLUSTARR_PLEX_BASE_URL` | import | importarr |
| `--index-path` | `/var/lib/clustarr/index/releases.db` | `CLUSTARR_INDEX_PATH` | index | indexarr |
| `--index-dsn` | `""` | `CLUSTARR_INDEX_DSN` | index | indexarr |
| `--facade-bind-address` | `:8080` (`"0"` disables) | `CLUSTARR_FACADE_BIND_ADDRESS` | index | indexarr |
| `--facade-api-key-secret` | `indexarr-facade` | `CLUSTARR_FACADE_API_KEY_SECRET` | index | indexarr |
| `--download-client` | `""` | — | torrent-engine | new (§3.5.5) |
| `--engine` | `""` | — | torrent-engine, usenet-engine | grabarr `--engine` |
| `--scratch-dir` | `/scratch` | — | usenet-engine (required); torrent-engine when the controller passes it | grabarr |
| `--publish-dir` | `""` | — | usenet-engine | grabarr |

**Retired from the agent:** `--role`, `--leader-elect`,
`--leader-election-namespace` and `--nats-single-node` (agents never elect and
never ensure topology); `--cardigann-*` (now the manager's); `--engine-image`,
`--data-claim` and `--engine-service-account` (manager-only).

**Validation:** the torrent engine needs exactly one of `--engine` or
`--download-client`; the usenet engine needs `--engine` and `--scratch-dir`; the
index domain with the facade enabled needs `--facade-api-key-secret` and
`--namespace` (`indexer/run.go:231-245`); `--index-path` must be non-empty
unless `--index-dsn` is set; `--nats-url` must be non-empty.

#### 3.5.2 Start order (`internal/cli/agent.Run`)

1. **Parse `--domain`**: exactly one known name (§3.5.1).
2. **Validate the options.** Derive the drain and graceful-shutdown timeouts (§3.5.6).
3. **Bootstrap observability** with `ServiceName` `agent-<domain>`.
4. **`k8s.RegisterRESTClientMetrics()`.**
5. **Build the manager** from `k8s.Options.AgentManagerOptions()`
   (`ManagerOptions("", false)` with
   `Controller.NeedLeaderElection = ptr.To(false)`, controller-runtime
   `pkg/config/controller.go:66-68`), through `ctrl.NewManager(cfg,
   k8s.WithBaseContext(opts, ctx))`:
   - No election and no `LeaderElectionID`. Engine reconcilers run as declared
     non-leader controllers, no longer depending on the "not electing counts as
     elected" fallthrough (`internal.go:479-484`), which also runs every
     `NeedLeaderElection()==true` runnable on every replica. That is why every
     leader-only runnable moved to the manager (R9).
   - `Client.Cache.DisableFor` is Secret and ConfigMap for every domain: no
     domain lists or watches either, every read is a by-name `Get`, and an
     informer would need `list`/`watch` grants no agent role holds. The
     metadata gateway's Secret reads (`registry.go:362,373`) become live GETs,
     so its role narrows to `secrets get`.
   - managedFields stripped, as in every cache; `Client.FieldOwner = "clustarr"`
     (§5.3.5).
6. **Connect the bus.** `busconn.Connect(o.NATSURL, "agent-<domain>",
   busconn.WithHooks(obs.BusHooks()))`.
7. **`busconn.AwaitTopology(ctx, bus, events.Default(), --topology-wait)`** (new).
   Every 2 s it polls until every stream, KV bucket, object store and consumer
   named in the default topology (the dead-letter watchers included, §5.9)
   exists, checked by name through a new
   `events.StreamAdmin.Missing(ctx, events.Topology) ([]string, error)` on
   natsbus and membus, held by a contract test. On timeout it returns
   `waiting for the manager to create <names>` and the process exits 1: the
   kubelet's liveness probe cannot be answered before `mgr.Start`, so a longer
   wait would end in a kill anyway. The wait is bounded in practice because
   the manager re-creates anything missing within 60 s (`busconn.KeepTopology`,
   §5.9), so a restart racing a NATS restart costs at most a few crash-loop
   backoffs. Agents never call `EnsureTopology`: today every replica of every
   service does (`pkg/k8s/bus.go:123-139`), so an old pod during a rollout can
   revert a newer topology. A durable or stream that disappears after start is
   re-bound by `Subscribe` itself (§5.9), so a running agent never exits for it.
8. **Process-level checks:** `jetstream`, `cache`; `ffgo` (healthz) when the
   domain is `import` or `caption`.
9. **Register the domain.** `Register(ctx, mgr, bus, Options) (Registration, error)`
   from the domain's registration package (§4.2.1), where `Registration`
   carries the domain's ready and live `*k8s.Checks`, its `[]k8s.FieldIndex`
   and a closer.
   - **Field indexes are declared, not registered, by the domain.** It returns
     `[]k8s.FieldIndex{Object, Name, Extract}`; the agent registers each once
     and asserts each reached the cache (today's `assertWorkerIndexes`,
     `app/catalog/wiring.go:165-192`, generalised). `catalog` and `events` each
     declare `search.IndexDownloadTarget` in their own process.
10. **`k8s.AddProbes(mgr, ready, live)`.**
11. **`mgr.Start(ctx)`.** Every consumer is an `EveryReplica` runnable, started
    after the caches sync (`manager.go:369-381`). When Start returns, the
    closer runs: the relindex store, or the torrent or usenet client.

#### 3.5.3 What each domain registers

Durables are named with their stream and retention (WQ = WorkQueue).

| Domain | Durables | RPC and servers | Runnables, indexes, process-local state | Extra checks | Scaling |
|---|---|---|---|---|---|
| `catalog` (`app/catalog/agent/catalog`: search, grab, renderer) | `catalogarr-search-high`, `catalogarr-search-normal` (`search.Worker`, `search/worker.go:936-970`); `catalogarr-grab` (`grab.Handler`, `grab/handler.go:122`); `catalogarr-artwork-render` (`worker/artwork/handler.go:198-217`). All on `CLUSTARR_WORK_CATALOGARR`, WQ | — | index `search.IndexDownloadTarget` and its assert; TheXEM scene maps (`wiring.go:223`) | — | HPA 0..1 (throttled, §9.1.1) |
| `events` (`app/catalog/agent/events`: RSS matcher, history, redownload) | `catalogarr-rss-matcher` (`CLUSTARR_RELEASES`, Limits); `catalogarr-redownload` (`CLUSTARR_EVENTS`, Limits); `catalogarr-history` (`CLUSTARR_EVENTS`, Limits); `clustarr-dlq-projector` (`CLUSTARR_DLQ`, Limits) | — | the 13 RSS-matcher indexes plus `search.IndexDownloadTarget` (`wiring.go:95-109`) and their assert; its own TheXEM scene maps | — | HPA **1**..N (§3.5.4) |
| `metadata` (`app/catalog/agent/metadata`: gateway) | `catalogarr-metadata` and `catalogarr-artwork-fetch` (`CLUSTARR_WORK_CATALOGARR`, WQ); `catalogarr-markers` and `catalogarr-segments-result` (`CLUSTARR_WORK_SEGMENTARR`, WQ, Durable) | `clustarr.rpc.catalogarr.metadata.lookup`, `.search`, `.resolve`, `.extras`, queue group `catalogarr` (`metadata/gateway.go:138-146`) | `catalogmetadata.Setup` wrapping `markers.Setup` and `segmenting.Setup{Results: true}` (`catalog/run.go:788-823`), now `app/catalog/worker/markers` and `app/catalog/worker/segmentresults`; provider limiters; L1 cache; the artwork `Fetcher` lock and per-host limiters. No `artwork.Reaper` (the manager's) | — | fixed 1, Recreate (ADR-0007, RPC) |
| `import` (`app/import/agent`: scan, file import, list sync, recycle sweeper, probe) | `importarr-scan`, `importarr-fileimport`, `importarr-list` and **new** `importarr-recycle` (all `CLUSTARR_WORK_IMPORTARR`, WQ); **new** `importarr-probe-high` and `importarr-probe-low` (`CLUSTARR_WORK_PROBE`, WQ, Durable, §6.5.1) | — | indexes `rescan.IndexMediaFileByPath` and `fileimport.IndexMediaFileByTarget` (`import/run.go:465-560`); one `native.Prober` (§6.6). `RecycleSweeper` is no longer an `EveryReplica` timer (`import/run.go:558`); it handles `importarr-recycle` | `import.data`; healthz `ffgo` | HPA 0..N |
| `index` (`app/indexer/agent`) | `indexarr-rss` (`CLUSTARR_WORK_INDEXARR`, WQ; `rss.Worker`, `rss/worker.go:195`) | `clustarr.rpc.indexarr.search`, `.download`, `.query`, queue group `indexarr` (`search.Serve`, E); Torznab facade on `--facade-bind-address` (E), with `ensureFacadeAPIKeys` at registration | relindex store opened before `mgr.Start` (SQLite at `--index-path`, or Postgres with `--index-dsn`) and closed after; its own `clientcache.ClientCache`, `clients.SessionStore` and limiter (R8, §5.12); `indexSweeper` as `EveryReplica` (it was `NeedLeaderElection` true, `indexer/run.go:891`, and the agent never elects) | `index.releaseindex` | fixed 1, Recreate (both engines) |
| `caption` (`app/caption/agent`: fetch) | `captionarr-fetch-high`, `captionarr-fetch-normal` (`CLUSTARR_WORK_CAPTIONARR`, WQ; `fetch/worker.go:155-170`) | — | providerset built by `providerset/build` with the KV throttle `clustarr-provider-throttle`; `providers.Extract = native.Extract` (§7.3) | `caption.data`; healthz `ffgo` | HPA 0..1 (throttled, §9.1.1) |
| `torrent-engine` (`app/grab/agent/torrent.Register`) | none | — | DownloadClient and proxy Secret read through the APIReader; `net.DefaultResolver` swapped when proxied; `dltorrent` client and `ReAttach`; `torrent.Reconciler` (controller `torrent-engine`); `torrent.Reaper`; `engine.ProgressPublisher` writing KV `clustarr-progress` (`grab/run.go:456-556`) | `torrent-engine.reattach` | StatefulSet replicas from the DownloadClient spec (manager-rendered) |
| `usenet-engine` (`app/grab/agent/usenet.Register`) | none | — | direct client and `usenet.BuildClient` with `usenet.LibraryRepairer{Runner: par2child.Exec{}}` (§8.6); `usenet.Reconciler`; `usenet.Reaper`; `ProgressPublisher` (`grab/run.go:561-605`); once main's connection budget lands, its per-server collector (below) | — | Deployment, 1 replica (CEL) |

**The usenet connection budget** (main's design `e4b59a5c`, phase-A plan
`db53ebd1`; built on main by another session, not landed at `80175fdc`). Phase
A keeps the engine one pod and puts everything in `pkg/download/usenet`, the
`UsenetSpec` CRD fields `connectionsPerDownload` and `maxBytesPerSecond`, and
the engine's `BuildConfig`, which maps them to `Config.ConnectionsPerDownload`
and an injected byte-rate `Config.Limiter` (`app/grab/engine/usenet/config.go`).
`Client.Add` then admits a job's transfer stage by slots (the primaries'
connections divided by `connectionsPerDownload`), and a job waiting for a slot
reads `Queued`. For this design that means:

- **Nothing moves that the split moves.** `pkg/download/usenet` and
  `app/grab/engine/usenet` stay where they are (agent-only, §4.4). Every
  `spec.usenet` field is already in `engineConfigHash`, so an edit rolls the
  engine, rendered by the manager's DownloadClient controller as today.
- **The per-server collector** (`clustarr_usenet_server_bytes_total{client,server}`
  and `clustarr_usenet_server_penalties_total{client,server,reason}`, read from
  `Client.Info`'s `ServerStats` on scrape, phase-A Task 4) is registered by
  `app/grab/agent/usenet.Register` on the agent's registry, wherever main's
  `setupUsenetEngine` registers it, since that function becomes `Register`
  (§4.3). Both families need rows in amendment §A2.3 and `wantSeries`, which
  the catalogue test reads.
- **`Download.status.usenet.servers[]`**, if phase A adds it (design §3.5; the
  phase-A plan does not), is an `EngineFields` leaf under `grabarr-engine`,
  which `app/grab/status.Patch` must list. It adds no field manager.
- **Phase B (several pods, deferred by the owner)** would touch what this
  design moves. The CEL rule that pins usenet to one replica relaxes; a
  usenet engine then needs a per-pod identity like the torrent engine's
  (`EngineIdentity` from `POD_NAME`, §3.5.5), instead of the fixed
  `--engine=<dc>-0`, and pod anti-affinity that the DownloadClient controller
  renders. The KV bucket `clustarr-usenet-connections` goes into
  `events.Default()`, so only the manager creates it (§5.9) and engines wait
  for it in `AwaitTopology`. Assigning a Download by free capacity instead of
  `HashOrdinal` is the manager's download controller's job
  (`app/grab/manager`), and `status.engine` is still pinned once.
- **Slots gate only the transfer.** Repair, unpack and publish hold no slot
  (phase-A Global Constraints), so the budget does not bound how many par2
  children run at once (§8.4.5).

**The recycle sweep becomes queued work** (R4 amendment):

- **Constants** in `pkg/events`: `ConsumerImportRecycle = "importarr-recycle"`,
  `FilterImportRecycle = "clustarr.work.importarr.recycle.>"`, subject
  `clustarr.work.importarr.recycle.sweep`. The filter is inside the existing
  `CLUSTARR_WORK_IMPORTARR` subjects, so no stream changes.
- **Spec:** AckWait 60s, MaxDeliver 3, BackOff `[5m, 30m]`, Heartbeat 30s,
  `Slots: 1`, MaxAckPending 8 (`Slots × events.AutoscaleReplicaCeiling`, §9.2).
- **Task:** `schema.RecycleSweepTask` (`pkg/events/schema`), empty but for the
  period it names.
- **Publisher:** `recyclesweep.Scheduler` in `app/import/manager`, a
  `k8s.LeaderOnly`. It publishes once at start and then every 6 h (§13 OD36),
  with Msg-Id `recycle-sweep/<now.UTC().Truncate(6h) RFC3339>`. The stream's
  dedupe window is 1 h (`Duplicates: time.Hour`, `pkg/events/topology.go:560`),
  so a manager restart in the first hour of a 6-hour slot publishes nothing
  new, and a later restart publishes one extra sweep for that slot. That is
  accepted: `SweepOnce` is idempotent (it removes only files already past
  retention), and the cost is one extra wake of `import`.
- **Handler:** `fileimport.RecycleSweeper.SweepOnce` (`recyclebin.go:130`). One
  global task, not one per RootFolder: `SweepOnce` sweeps each distinct bin
  once, with the **longest** retention any sharing RootFolder asks for
  (`recyclebin.go` doc); a per-RootFolder task would sweep a shared bin with one
  folder's shorter retention. Two sweeps racing on one RWX bin stays harmless.

#### 3.5.4 Which consumers can live on an HPA that reaches zero

Ruled per consumer from `pkg/events/topology.go`:

| Durable | Stream | Retention (`topology.go`) | Domain |
|---|---|---|---|
| `catalogarr-search-high`, `catalogarr-search-normal`, `catalogarr-grab`, `catalogarr-artwork-render` | `CLUSTARR_WORK_CATALOGARR` | WorkQueue (`:545-556`, `:664-717`) | catalog, HPA 0..N |
| `catalogarr-rss-matcher` | `CLUSTARR_RELEASES` | **Limits**, 72 h / 4 GiB (`:580-590`, `:657`) | events |
| `catalogarr-history` | `CLUSTARR_EVENTS` | **Limits**, 168 h / 2 GiB (`:566-579`, `:758`) | events |
| `catalogarr-redownload` | `CLUSTARR_EVENTS` | **Limits** (`:771-777`) | events (moved from catalog, R4 amendment) |
| `clustarr-dlq-projector` | `CLUSTARR_DLQ` | **Limits**, 720 h (`:618-629`, `:864`) | events |
| import, caption, index, metadata, probe and segmentarr durables | their `CLUSTARR_WORK_*` | WorkQueue | as in §3.5.3 |

**The three Limits streams do not buffer safely while no `events` replica runs.**
Every live Deployment passes `--nats-single-node`, and these streams are not
`Durable`, so `ForSingleNode` (`topology.go:249-283`) makes them memory-backed,
scales them into the 64 MiB budget (of 9,344 MiB of non-Durable reservations,
`CLUSTARR_RELEASES` gets about 27.6 MiB, `CLUSTARR_EVENTS` about 13.8 MiB,
`CLUSTARR_DLQ` about 6.9 MiB) and applies `DiscardOld`. Releases and events
published while the matcher is down are silently dropped once the share fills.
A cold `events` start must also sync 14 indexes (about 11 s measured live for
catalogarr's worker indexes). So `events` has **minReplicas 1**: its HPA ranges
1..N on these four durables' lag. Everything else on an HPA is a WorkQueue
consumer, whose messages wait for the next replica.

#### 3.5.5 Engine argv (rendered by the manager's DownloadClient controller)

`downloadclient.EngineCommand(dc, EngineRuntime) (command, args []string)` is
exported (the `cmd/agent` argv test parses its output, §10.3), and uses
`binpath.Agent` in place of `clustarrBinary = "/usr/local/bin/clustarr"`
(`workload.go:79-84`). Neither engine uses a shell.

**Torrent** (StatefulSet `<dc>-engine`), replacing
`/bin/sh -c 'ordinal=${HOSTNAME##*-}; exec ...'` (`workload.go:428-455`):

```
command: ["/usr/bin/agent"]
args:    ["--domain=torrent-engine", "--download-client=<dc>", "--data-dir=<--data-dir>"]
         + ["--scratch-dir=<dir>"] when spec.torrent.scratch is set
env:     POD_NAMESPACE (fieldRef metadata.namespace), POD_NAME (fieldRef metadata.name, new),
         NATS_URL, UMASK, GOMEMLIMIT
```

`agent.EngineIdentity(client, podName string) (string, error)` reads `podName`
from `$POD_NAME`, else `os.Hostname()`, requires
`podName == client + "-engine-" + N` with N decimal, and returns
`client + "-" + N`, the `<client>-<ordinal>` the Downloads' engine label
carries. Anything else is an error, so a pod that is not a StatefulSet replica
fails at start instead of claiming ordinal `abc12`.

**Usenet** (Deployment `<dc>-engine`, ordinal fixed at 0 by CEL):

```
command: ["/usr/bin/agent"]
args:    ["--domain=usenet-engine", "--engine=<dc>-0", "--data-dir=<--data-dir>", "--scratch-dir=<dir>"]
         + ["--publish-dir=<dir>"] when set
```

**What follows:** `--nats-single-node` is dropped from both and
`EngineRuntime.BusSingleNode` is removed; engines `AwaitTopology` instead,
because they publish to `CLUSTARR_EVENTS` and KV `clustarr-progress`. Every
engine pod template changes once (image, command, args, `POD_NAME`), so each
engine rolls once at cutover. The engine selectors and names, the
`app.kubernetes.io/component: grabarr-engine` label and the
`grabarr-engine` ServiceAccount are unchanged (selectors are immutable). R13
keeps the cgo build, so the SQLite piece-completion store survives and seeding
torrents do not rehash.

#### 3.5.6 Drain and graceful shutdown

One rule sizes every agent's shutdown from its durables, so a scale-down or
rollout lets running handlers finish rather than naking them:

- `--drain-timeout` defaults to the **longest `ConsumerSpec.AckWait`** among
  the domain's durables (catalog 120 s from search; caption 90 s; metadata,
  import and index 60 s; events 30 s). It becomes `events.Subscription.Drain`
  on every subscription (§9.3).
- `--graceful-shutdown-timeout` defaults to drain + 15 s.
- Installers set `terminationGracePeriodSeconds` to graceful + 10 s: catalog
  145, caption 115, metadata/import/index 85, events 55 (§10.2.1). markers
  drains for its 1800 s AckWait and gets 1825 (§3.7). The manager keeps a 60 s
  timeout and a 70 s grace. Engines keep today's grace.

**What `AckWait` means here.** It is not the broker's deadline: every consumer
these agents drain sets a `BackOff`, and nats-server replaces `AckWait` with
`BackOff[0]` (`server/consumer.go:678-682`; the repo says so at
`pkg/events/topology.go:786-797`). The broker's deadline is
`events.AckDeadline(sub, attempt)`, which §9.3's reclaim uses. `AckWait` is this
repo's declared per-message handler budget, the value its authors already tie
to the grace period ("the floor set by terminationGracePeriodSeconds",
`topology.go:776-781`); long work heartbeats with `InProgress` within
`BackOff[0]` regardless. Drain sizes from that declared budget, because what it
must cover is a running handler, not a delivery deadline.

`TestGracePeriodsCoverAckWait` (successor of `TestWorkerGracePeriodsCoverAckWait`,
`deploy_args_test.go:337-349`) holds every Deployment's grace at or above its
longest declared AckWait plus 25 s. This also closes a gap against the declared
budget: catalogarr runs a 60 s grace beneath search's 120 s AckWait. (The
autoscale draft's fixed 45 s drain and the installers draft's grace = AckWait
are both replaced by this rule; §13 OD35.)

**Amended 2026-10-07 (NATS research; `nats-worker-pools.md` D7, §7 S1).** The
handler budget gets its own field, so `AckWait` keeps one meaning, the
broker's. `events.ConsumerSpec.HandlerTimeout` is an explicit per-message
handler budget, which the bus enforces (§9.3 as amended, "Handler budget");
`ConsumerSpec.HandlerBudget()` returns it, else `AckWait`, and is what sizing
reads: `--drain-timeout` defaults to the longest `HandlerBudget()` of the
domain's durables, `TestGracePeriodsCoverAckWait` reads `HandlerBudget()`, and
the `bus` liveness check (§3.3 as amended) uses it as its threshold. Every value
above is unchanged. Enforcement is **opt-in**, unlike the research's S1, which
made every consumer's budget its `AckWait`: fileimport (60 s `AckWait`,
heartbeat 10 s) copies files across filesystems and the rescan (60 s, heartbeat
3 s) walks whole libraries, both heartbeating past `AckWait` by design, so a
budget at `AckWait` would cancel them. W4.97 sets `HandlerTimeout` only where a
bound already exists by hand (`segmentarr-analyze`, 30 min, its `TaskTimeout`);
each other consumer gets one when its `clustarr_work_duration_seconds` tail on
the live cluster says what it should be. S3 (§9.6) needs it set on every
consumer whose `AckWait` it changes.

### 3.6 cmd/ui

`ui [flags]` and `ui version`. Flags unchanged from `services.go:625-673`, plus
log and tracing flags:

| Flag | Default | Env |
|---|---|---|
| `--bind-address` | `:8080` (`ui.DefaultBindAddress`) | — |
| `--auth-mode` | `""`, required (`ui.Run` refuses to serve without it) | — |
| `--nats-url` | `$NATS_URL`, else `nats://clustarr-nats:4222` | `NATS_URL` |
| `--plex-provider` | `true` | — |
| `--plex-guids` | `true` | — |
| `--namespace` | `$POD_NAMESPACE` | `POD_NAMESPACE` |
| `--external-url` | `""` | `CLUSTARR_EXTERNAL_URL` |
| `--pipeline-history` | `100` (`projection.DefaultPipelineHistory`) | — |
| (env only) signing key | — | `CLUSTARR_ART_SIGNING_KEY`, at least 32 bytes (`flags.go:160-174`) |

**Start order**, moved unchanged into `internal/cli/ui`:

1. `buildUICluster` (`services.go:446-474`): a reader plus an uncached writer
   into `actions.New`; failures logged and swallowed.
2. `buildUIProjection` (`:489-497`).
3. `artSigningKey()`; an error exits 1.
4. `buildUIBus` (`:536-587`): `busconn.Connect(natsURL, "ui",
   busconn.WithHooks(obs.BusHooks()))`, the artwork ObjectStore, the
   `RPCMetadataSearch` closure, the extended-metadata KV Get closure and
   `RPCMetadataExtras` with a 30 s timeout; `defer close`.
5. `ui.Run`, which calls `obs.Bootstrap` with `ui`.

The ui never ensures topology. Signals, umask and exit codes are §3.2.

**Amended 2026-10-07 (NATS research; `nats-object-store.md` §6.5, §8;
owner decision (b)).** One new flag and one new start step:

| Flag | Default | Env |
|---|---|---|
| `--art-cache-bytes` | `67108864` (64 MiB, `ui.DefaultArtCacheBytes`): the digest-keyed artwork byte cache; `0` disables it | — |

`ui.Run`, step 5, starts the artwork index (`pkg/events/objindex`) on
`Options.Artwork` when it is non-nil, after step 4 (`buildBus` in
`internal/cli/ui`) has bound the store: one
read-only `ObjectStore.Watch` per ui process, feeding `/art`'s lookups, its
byte cache and the SSE `art` event (artwork design §B.8 as amended). A watch is
a read: the ui still writes nothing and ensures no topology, and `TestUIWiresEveryUIOption`
holds `ArtCacheBytes` like every other option. The ui's 256 MiB memory limit
(chart `ui.resources`) holds the 64 MiB cache and the index (about 250 bytes per
object, 2.5 MB at 10,000 objects).

**Links.** `cmd/ui` imports `internal/cli/ui`, `ui`, `ui/actions`,
`ui/projection`, `pkg/events`, `pkg/events/schema`, `pkg/metadata/extended`,
`pkg/obs` and `pkg/busconn`, and no `app/*`. The ui guard (§4.5.2) needs the
three cuts of §4.3 Wave 1 together: `pkg/obs` calls `SetLogger` through
`sigs.k8s.io/controller-runtime/pkg/log`; `pkg/pipeline` reads conditions from
`pkg/k8s/conditions`; the bus connector moves to `pkg/busconn`.

**Main's ui since `0d3ae234`** (`b77c30d2`, `0a39889a`) changes none of this.
The mass editor (`POST /library/{tab}/bulk`, `ui/library_bulk.go`) and the
per-card hover actions (Refresh & Scan, Search, Monitor/Unmonitor, Delete,
answered in place by `finishActionWith`) call only the item page's existing
actions: `SetMonitored`, `SearchNow`, `RefreshMetadata` with `RescanPath`, and
`RequestDelete`. The library-wide find (`GET /library/find`) reads the
projection. So `actions.Grants()`, `ui_role.yaml`,
`TestUIRoleGrantsOnlyReadsAndActionWrites`, `TestUINeverWrites` and
`TestBothUICommandsWireEveryUIOption` are unchanged, and the `clustarr-ui` write
sites in §5.2 are the same lines. Replacing the vendored components with
shadcn-templ's registry ones deleted `ui/components/{navigationmenu,scrollarea}`'s
own tests. No moved test names them.

### 3.7 cmd/markers

`markers [flags]`, pflag, flag set `markers`.

| Flag | Default | Notes |
|---|---|---|
| `--decode-threads` | `0`: `max(1, runtime.GOMAXPROCS(0) / slots)`, where slots is `segmentarr-analyze`'s per-pod slot count | replaces `--ffmpeg-threads`; follows the cgroup quota (§7.2.8) |
| `--metrics-bind-address` | `:8080` (`""` serves nothing) | also serves `/healthz` and `/readyz` |
| `--self-check` | `false` | runs steps 3-6 below with no NATS or env, prints a JSON report, exits 0 or 3 |
| `--version` | `false` | new: prints `markers <version.String()>`, exits 0 right after the flag parse, before the env checks (the image check `version:markers`, §10.1.4) |
| log and tracing flags | `obsflags.Bind` | |

**Removed:** `--ffmpeg` (no executable, R2) and `--concurrency`. Per-pod
in-flight is `segmentarr-analyze`'s `ConsumerSpec.Slots`, overridable through
`CLUSTARR_CONSUMER_SLOTS`, the same number the autoscale reconciler uses as the
HPA target, so the two cannot disagree. Today's manifest pins `--concurrency=1`
(`config/manager/segmentarr-worker.yaml:42`) and the live release 3; the live
value carries into `markers.consumerSlots` (§13 OD34).

**Environment:** `NATS_URL` and `POD_NAME` required; `ORT_LIB_PATH` optional
(without a loadable library the text detector is off); `FFGO_SHIM_DIR` from the
image; `CLUSTARR_CONSUMER_SLOTS`, `UMASK`, `GOMEMLIMIT`.

**Start order:**

1. Parse flags; on error `ExitMisconfigured` (3).
2. Check the required env; missing exits 3.
3. `fsops.ApplyUmaskFromEnv()` (new, for uniformity); error exits 3.
4. `signal.NotifyContext(SIGTERM, os.Interrupt)`.
5. Logging, then `tracing.Setup` with `markers`.
6. `textdet.NewONNX($ORT_LIB_PATH)`, optional and off on error, as at
   `segmentarr-worker/main.go:88-94`.
7. `ffruntime.Load()` then `ffruntime.Require(decode.Needs)` (§7.4). Either
   failure exits `ExitMisconfigured` (3): the native image always carries
   FFmpeg, so a failure means a broken image.
8. `ffruntime.RouteLog(log)`.
9. The HTTP listener on `--metrics-bind-address`: `/metrics` (`promhttp` over the
   default registry `app/segments/worker/metrics.go` uses, plus a GaugeFunc
   `clustarr_ffgo_abandoned_calls` over `ffruntime.Abandoned()`), `/healthz`,
   `/readyz`.
10. `nats.Connect(NATS_URL, nats.Name("markers/"+POD_NAME))`, then `natsbus.New`
    with the inline tracing hooks.
11. `bus.Subscribe(ctx, events.Default().Consumer(segmentarr-analyze).Subscription(), h.Handle)`
    with `Drain` = the AckWait (1800 s). A failure exits 2; `Subscribe` waits
    for a durable the manager has not created yet (§5.9), and `/readyz` stays
    false meanwhile.
12. Wait for `ctx.Done()` (then `unsub()`, which waits for in-flight handlers up
    to the drain, then `ExitDrained` 10) or for `ffruntime.Wedged()` (then
    `ExitRetriable` 2, so the Deployment restarts the container).

**Exit codes:** 2, 3 and 10, from `app/segments/worker/exit.go:23-25`.
markers links no controller-runtime, no client-go and (after §7.2.9) no
`k8s.io/api`.

### 3.8 cmd/transcode

`transcode [flags]`, pflag, flag set `transcode`. Flags unchanged: `--data-dir`
(`/data`, `worker.LogicalDataRoot`), `--self-check=<cpu|cuda|intel>`, `--trial`,
`--scratch-dir` (`os.TempDir()`), log and tracing flags; plus one new flag,
`--version`, which prints `transcode <version.String()>` and exits 0 right after
the flag parse, before the env checks (the image check `version:transcode`,
§10.1.4). Neither `cmd/segmentarr-worker` nor `cmd/squasharr-worker` has it
today, and without it a pflag parse error would exit 3.

**Environment:** `NATS_URL`, `CLUSTARR_POOL_PROFILE_UID`, `CLUSTARR_POOL_CLASS`
and `POD_NAME` required; also `NODE_NAME`, `CLUSTARR_CPU_LIMIT`, `UMASK`,
`FFGO_SHIM_DIR`, `LIBVA_DRIVERS_PATH` and `NVIDIA_DRIVER_CAPABILITIES` (set by
`pool.applyHardware`).

**Start order** is today's (`squasharr-worker/main.go:49-131`) with three
renames: `to.ServiceName = "transcode"`, NATS name `transcode/<POD_NAME>`,
message prefix `transcode:`. `inprocess.New` becomes `ffruntime.Load()` plus
`Require` of its class (§7.4).

**Exit codes are unchanged**, because the pool Job's `podFailurePolicy` depends
on them (`pool/render.go:259-275`): `WorkerExitRetriable` 2,
`WorkerExitMisconfigured` 3, `WorkerExitDrained` 10
(`app/squash/worker/exit.go:24-35`). `--self-check` returns 0 or 3.

**No metrics endpoint**, as today; worker-side observations
(`app/squash/worker/run.go:624-668`) stay unexported, and the manager's
transcodejob controller exports transcode speed.

**What a run writes, since the MP4 standard** (main `b33e4417`, `71dc301d`,
`a9848d35`; nothing in the CLI changes). Every output is `<stem>.mp4`
(`worker.OutputContainer`, whatever the profile's `container`). Beside its part
the run writes one subtitle sidecar part per `standard.Result.Sidecars`
(`fsops.SidecarPath`: `<stem>.<lang>[.forced|.sdh].part-<uid8>-<n>.<srt|ass>`):
SubRip through FFmpeg's `srt` muxer, ASS through `ass`, and WebVTT,
`mov_text` and plain text through a Go SubRip writer
(`pkg/transcode/engine/sidecar.go`). Verify fails a missing sidecar; an empty
one (a track with no cues) is dropped at placement. `placeSidecars` moves each
to its final name before the video swap, never over an existing file, and
`removePart` and `sweepEarlierAttempts` remove sidecar parts with their attempt.
A task rendered for another container (a pre-MP4 `.mkv` output) is refused as
retriable. So a pool pod writes more files under `/data`, with the same mounts,
UMASK and exit codes. The FFmpeg it loads must carry the `ac3` encoder and the
`mp4`, `srt` and `ass` muxers, which §7.4's `Require` checks. A surround dub is
grafted as AC-3 5.1 plus AAC 2.0 (`1f9e8c90`), inside the same graft task.

**Pool Jobs.** `pool.Template` sets `Command: []string{binpath.Transcode}`. Args
are unchanged (`--data-dir` plus `workerObservabilityArgs`).
`app.kubernetes.io/component` becomes `transcode` (`template.go:392`,
`render.go:220`); no selector reads it (`PoolJobCache` selects on
`LabelManagedBy`, `transcodejob/pools.go:598-604`). Both are immutable template
fields, so `pool.Classify` returns `DriftRecreate` and each pool drains and is
recreated once at cutover; the image change alone already forces that. The Job
name prefix `squasharr-pool-` and the `app.kubernetes.io/managed-by=squasharr`
label stay.

### 3.9 Build modes

| Binary | Build | Image |
|---|---|---|
| `bin/manager` | `CGO_ENABLED=0` | `clustarr` (cross-compiled from `$BUILDPLATFORM`) |
| `bin/ui` | `CGO_ENABLED=0` | `clustarr` |
| `bin/agent` | `CGO_ENABLED=1` (R13) | `native` (built on the target platform) |
| `bin/markers` | `CGO_ENABLED=0` | `native` |
| `bin/transcode` | `CGO_ENABLED=0` | `native` |

The images build each binary the same way the Makefile does (§10.1). Today
`Dockerfile.media` builds segmentarr-worker with cgo while the Makefile does
not (`Dockerfile.media:63-79`, `Makefile:167`). The per-binary `go list -deps`
guards are §4.5.

### 3.10 Development without `clustarr all`

Manager and agent code never share a process: they are different binaries (R6),
and each agent process runs one domain (R4 amendment). The development shape is
eight processes against the kind cluster's API server and a port-forwarded
NATS. `make run-dev` depends on `build` and `native-assets` and runs
`hack/dev-run.sh`, which starts:

```
bin/manager --leader-elect=false --nats-single-node --native-image=$NATIVE_IMG \
            --autoscale=false --external-metrics-bind-address=0 \
            --metrics-bind-address=0 --health-probe-bind-address=:8081 --data-dir=$DATA_DIR
bin/agent --domain=catalog  --metrics-bind-address=0 --health-probe-bind-address=:8082
bin/agent --domain=events   --metrics-bind-address=0 --health-probe-bind-address=:8083
bin/agent --domain=metadata --metrics-bind-address=0 --health-probe-bind-address=:8084
bin/agent --domain=import   --metrics-bind-address=0 --health-probe-bind-address=:8085 --data-dir=$DATA_DIR
bin/agent --domain=index    --metrics-bind-address=0 --health-probe-bind-address=:8086 \
          --index-path=${XDG_CACHE_HOME:-$HOME/.cache}/clustarr/releases.db --facade-bind-address=:9696
bin/agent --domain=caption  --metrics-bind-address=0 --health-probe-bind-address=:8087 --data-dir=$DATA_DIR
bin/ui      --auth-mode=anonymous --bind-address=:8080
```

Every process takes `NATS_URL` and `POD_NAMESPACE` from the environment. The
script also exports, from `$(NATIVE_ASSETS)/lib` (§10.1.6): `LD_LIBRARY_PATH`
(ffgo finds libavutil, libavcodec and libavformat only through it and fixed
system directories, ffgo `internal/bindings/bindings.go:117-128,207-224`),
`FFGO_SHIM_DIR` (`make build` uses `-trimpath`, which defeats ffgo's
source-relative shim lookup, `internal/shim/shim.go:947`), `PAR2GO_LIB` and
`ORT_LIB_PATH`; without them the import and caption agents exit 1 at
`ffruntime.Load` (§7.4). It runs every process in one process group and stops
them together on Ctrl-C. The index path and facade port are what `all.go`'s
`devIndexPath` and `devFacadeBindAddress` did. `--legacy-lease-check` is
harmless here (no legacy lease exists).

**Deleted with `all.go`:** `TestOffsetAddress`, `TestAllGivesEachServiceItsOwnPorts`,
every `all` row in the cli, ui, index_dsn, importarr_sample and start-envtest
tests, and e2e scenario 16's `clustarr all` leg (`test/e2e/parity_test.go:76-86`,
which skips today).

### 3.11 Names: what changes and what does not

**Changes:**

- **Leases:** the six `<svc>.clustarr.io` leases become one, `manager.clustarr.io`.
  The `indexarr.clustarr.io` lease (elected only under `--index-dsn`,
  `indexer/run.go:333-339`) disappears.
- **Observability names:** tracing `service.name` and NATS client names (§3.1).
- **`--domain`** is one value per process (R4 amendment): there is no `all`.
- **Binary paths:** `/usr/bin/<bin>` (`pkg/binpath`).
- **Engine argv** (§3.5.5) and **pool Jobs** (command; component label `transcode`).
- **Flags:** importarr's `--data-path` becomes `--data-dir`; the grabarr
  controller's `--scratch-dir` becomes `--engine-scratch-dir`; `--engine-image`
  and `--worker-image` merge into `--native-image`; markers' `--concurrency`,
  `--ffmpeg` and `--ffmpeg-threads` go.
- **Env:** `CLUSTARR_ENGINE_IMAGE` and `CLUSTARR_WORKER_IMAGE` become
  `CLUSTARR_NATIVE_IMAGE`; new `CLUSTARR_CONSUMER_SLOTS`,
  `CLUSTARR_EXTERNAL_METRICS_SERVICE`, `CLUSTARR_EXTERNAL_METRICS_SECRET`,
  `PAR2GO_LIB`.
- **New `pkg/events` names:** stream `CLUSTARR_WORK_PROBE`; durables
  `importarr-probe-high`, `importarr-probe-low`, `importarr-recycle`; subjects
  `clustarr.work.probe.file.<high|low>.<mediaKey>` and
  `clustarr.work.importarr.recycle.sweep`; KV bucket `clustarr-probes`.
- **New field manager:** `clustarr-autoscale`.

**Unchanged:**

- Every `k8s.Manager*` field manager (R7).
- Every existing durable, stream, bucket, object store, subject, RPC subject and
  queue group (`catalogarr`, `indexarr`), and `TranscodeTaskConsumerName`.
- Every controller name.
- Every envelope `Source` string (`catalogarr@`, `importarr@`,
  `squasharr-controller@`, `squasharr-worker@`, `segmentarr-worker@`, …): they
  name the producing component, and history and DLQ records already carry them.
- The engines' ServiceAccount `grabarr-engine`, the engine workload names and
  selectors, the ui Service `<fullname>-ui` on port 8080, the Secrets
  `<fullname>-ui-art-signing-key` and `<fullname>-indexarr-facade`, and the PVCs
  `<fullname>-data` and `<fullname>-index` (cluster-plex and live data depend on
  them, §10.0).

---
## 4. Package layout and dependency guards

Where every package lives after the split; which leaf extractions make that
possible, in an order where every step compiles; the `go list -deps` guard each
binary carries; and what that does to binary size.

**Basis.** The import graph was dumped once with `go list -e -deps -json ./...`
under both `CGO_ENABLED=0` and `CGO_ENABLED=1` (Go 1.27, linux/amd64). Every
chain quoted is the shortest path over `.Imports`. The target layout was
simulated on that graph by moving the actual per-file imports of every file
listed in §4.3; every projected closure in §4.4 to §4.7 comes from that
simulation. Re-run the §4.1 chains after the R14 rebase.

### 4.1 What links what today (verified edges)

**Manager-side packages that pull agent or heavy code:**

| Package (manager side) | Unwanted dependency | Shortest chain (`go list`) | What it actually uses |
|---|---|---|---|
| `app/catalog/controller/{movie,series,album,artist,audiobook,author,book,comic}` | `golang.org/x/image/webp` | `golang.org/x/image/webp <- app/catalog/metadata/artwork <- app/catalog/controller/movie` | Only `artwork.PublishFetch`: movie/reconciler.go:345, series:238, album:395, artist:188, audiobook:324, author:196, book:390, comic:192. x/image comes through one blank import, `metadata/artwork/fetcher.go:37`. |
| `app/catalog/controller/mediafile` | go-ffprobe + ffprobe exec | `gopkg.in/vansante/go-ffprobe.v2 <- pkg/mediainfo <- app/catalog/controller/mediafile` | Default `Probe: mediainfo.Probe` (mediafile_controller.go:165), which calls `ffprobe.ProbeURL` (pkg/mediainfo/mediainfo.go:119) and `exec.CommandContext(ctx, "ffprobe", …)` (pkg/mediainfo/ffprobe.go:146). |
| 〃 | go-astisub | `github.com/asticode/go-astisub <- pkg/subtitles <- app/catalog/controller/mediafile` | `subtitles.LangKey/ParseLangKey` (sidecars.go:45). astisub is imported only by pkg/subtitles/postprocess.go and uppercase.go. |
| 〃 | x/image | `… <- app/catalog/metadata/artwork <- app/catalog/controller/series <- app/catalog/markers <- app/catalog/controller/mediafile` | `markers.Due/Publish`; markers/handler.go:242 imports controller/series for `EffectiveEpisodeOrder`. |
| `app/catalog/controller/metadataprovider` | 18 metadata clients, golang-tmdb, resty, musicbrainzws2 | `github.com/cyruzin/golang-tmdb <- pkg/metadata/clients/tmdb <- app/catalog/controller/metadataprovider` | `NewProber` (prober.go:104) builds and probes a real client per type. `BuildRegistry/addToRegistry` (registry.go:75,147) are dead in production: the gateway uses app/catalog/metadata/registry.go:69. |
| `app/catalog/controller/overlayprofile` | worker code, `x/image/draw` | `golang.org/x/image/draw <- app/catalog/worker/artwork <- app/catalog/controller/overlayprofile` | plan.go only (controller.go:41): Item, ItemOf, Plan, ProfileHash, Publish, ReasonProfile, Selects, Want, Winner. |
| `app/catalog/controller/search` | worker code | `app/catalog/worker/grab/downloads <- app/catalog/controller/search` | `downloads.ResolveSource` and `SourceApplyConfiguration` (grab.go:44,55). |
| `app/catalog/metadata` (refresh controllers) | 19 metadata clients, x/image | refresher.go shares a package with the gateway | refresher.go is self-contained (api, events, obs, version); marker at :90. |
| `app/catalog/segmenting`, `app/catalog/markers` | x/image; agent consumers in the same package | via controller/series → metadata/artwork | The manager uses the planner, `PublishPlan`, `Due/Publish`; Applier/Results and the TheIntroDB handler are agent code. |
| `app/catalog/history` | agent consumers | sink.go and dlq.go (consumers) share a package with replay.go (controllers) | — |
| `app/import/controller/{libraryscan,rename}` | worker/rescan, go-ffprobe, ffprobe exec | `pkg/mediainfo <- app/import/worker/rescan <- app/import/controller/libraryscan` | libraryscan uses progress.go types (libraryscan_controller.go:318–555). rename uses `rescan.RenameFile` (controller.go:129), which needs rescan's spec renderer `applySpec/reassertFrozen` (rename.go:254,265). |
| `app/import/controller/importlist` | worker/importlist + 7 list providers | `pkg/importlist/arr <- app/import/worker/importlist <- app/import/controller/importlist` | result.go, tokenstore.go, kinds.go and `AnnotationSyncedAt` (worker.go:211); providers come through provider.go only. |
| `fileimport.Retrigger` (a controller) | lives in a worker package | registered at app/import/run.go:448 | retrigger.go is self-contained apart from two annotation constants in annotation.go. |
| `app/indexer/controller/indexer` | modernc sqlite, pgx | `modernc.org/sqlite <- pkg/relindex <- app/indexer/worker/rss <- app/indexer/controller/indexer`; `github.com/jackc/pgx/v5 <- github.com/jackc/pgx/v5/stdlib <- pkg/relindex <- …` | `rss.NextPollAt`, `rss.ScheduleNext`, `rss.TaskMsgID` (controller.go:443,444,478,482). |
| 〃 | agent verb code | `app/indexer/download <- app/indexer/controller/indexer` | clientcache.go:198,207 (`download.Fetcher`, `download.EngineFetcher`); ClientCache is agent state living in the controller package. |
| `download.DirectGrabReconciler` | lives in the agent package `app/indexer/download` | run.go:621 | directgrab.go:116 builds a `download.Service` only to call `countGrabAt` (service.go:391–417: `limits.CountGrabAt` plus metrics plus `idxstatus.PublishTransitions`). |
| `app/grab/controller/download` | the engine runtime package | `app/grab/engine <- app/grab/controller/download` | Only `engine.Finalizer` (engine.go:87; controller.go:607,628). |
| `app/squash/controller/{pool,transcodejob,transcodeprofile}` | `app/squash/worker`, ffprobe exec | `pkg/mediainfo <- app/squash/worker <- app/squash/controller/pool` | BuildTask, OutputPath, ErrNoRootFolder, ErrInvalidOutput, ProfileHardware, ProfileHash(At), ReplaceSource, the Default\* constants, StandardProfile/StandardTier (ffgo.go:67,80), CPULimitEnv (run.go:93), WorkerExit\* (exit.go:31,35). |
| `app/caption/controller/{subtitleprovider,subtitlerequest,subtitleprofile}` | 4 subtitle clients, rardecode, embedded ffmpeg exec, astisub | `github.com/nwaples/rardecode/v2 <- pkg/subtitles/providers/internal/subarchive <- pkg/subtitles/providers/subdl <- app/caption/providerset <- app/caption/controller/subtitleprovider`; `pkg/subtitles/providers/embedded <- app/caption/controller/subtitlerequest` | `providerset.Validate/NeedsSecrets/HIVerifiable/Entry/Order`; NeedsSecrets and HIVerifiable call `prototype()`, which builds every client (providerset.go:320–361). `embedded.New(…).Search` (existing.go:145) is pure; only `Download` execs ffmpeg (embedded/provider.go:118). |
| `app/<svc>` root packages | everything | cmd/clustarr/services.go:30–36 imports all six roots, each run.go imports both sides | — |

**ui and the native workers:**

| Binary | Edge | Chain | Cause |
|---|---|---|---|
| ui | controller-runtime root and `pkg/manager` | `sigs.k8s.io/controller-runtime/pkg/manager <- sigs.k8s.io/controller-runtime <- pkg/obs <- ui` | Only `ctrl.SetLogger` (pkg/obs/bootstrap.go:91), an alias of `pkg/log.SetLogger` (controller-runtime@v0.25.1/alias.go:191). |
| 〃 | `pkg/k8s` | `pkg/k8s <- pkg/pipeline <- ui` | `k8s.IsConditionTrue`, `k8s.StatusUpToDate` (pipeline/project.go:591–592). |
| 〃 | `pkg/k8s` | services.go `k8s.ConnectBus` | pkg/k8s/bus.go |
| segmentarr-worker | no client, but k8s API types | `k8s.io/api/core/v1 <- api/catalog/v1alpha1 <- app/segments/worker` | Execs ffmpeg at pkg/segments/decode/proc.go:49. |
| squasharr-worker | no client, but k8s API types | `k8s.io/api/core/v1 <- api/catalog/v1alpha1 <- app/squash/worker` | — |

Four non-test files import `os/exec` today: `pkg/mediainfo/ffprobe.go`,
`pkg/subtitles/providers/embedded/provider.go`, `pkg/segments/decode/proc.go`,
`pkg/download/usenet/repair.go`. `os/exec` is useless as a `go list` gate (every
client-go user reaches it through `k8s.io/client-go/plugin/pkg/client/auth/exec`),
so exec is guarded by AST (§4.5.6).

### 4.2 Decisions

**4.2.1 Registration packages.** Each service gets `app/<svc>/manager` (the
controller side, imported only by `internal/cli/manager`) and `app/<svc>/agent`
(the worker side, imported only by `internal/cli/agent`). Import them aliased
(`catalogmanager`, `catalogagent`) where controller-runtime's `pkg/manager` is
also imported.

- **Manager side:** `func Register(mgr ctrl.Manager, bus events.Bus, o Options) error`,
  `Options` plain data with no flag binding. A service needing cache entries
  also exports `Cache(o Options)` returning its `ByObject`/`DisableFor`
  contributions (today squash's `transcodejob.PoolJobCache`, pools.go:598, and
  grab/import/indexer's `DisableFor` Secret, grab/run.go:251, import/run.go:263,
  indexer/run.go:335). `internal/cli/manager/options.go` merges them (§5.6).
- **Agent side: one registration package per domain**, each exporting
  `Register(ctx, mgr, bus, Options) (Registration, error)`. A package is the
  unit `go list -deps` sees, so one package per domain is what lets the
  field-manager guard (§5.15) and the RBAC linkage guard (§10.3.2) compute what
  each domain's process can write from its package root. catalog's three
  domains therefore get three packages and the two engines two;
  `internal/cli/agent/domains.go` maps each domain name to its package's
  `Register`. Its static data (domain name, consumer durables, autoscaled or
  fixed, minimum replicas, throttled consumers) lives in the leaf package
  **`pkg/agentdomain`**, which `app/autoscale/controller`, the guards and the
  agent read; a test holds the key sets of the two tables equal.
- **`--domain`** takes exactly one domain (§3.5.1). Each domain declares its
  field indexes and the agent registers and asserts them once, so
  `registerWorkerIndexes` never runs twice in one cache
  (`app/catalog/run.go:690–695`).

| Package | Exports | Source today |
|---|---|---|
| `app/catalog/manager` | `Register`: movie, series, episode, the 7 non-video reconcilers, mediafile, rootfolder, qualityprofile + `Bootstrap`, delayprofile, metadataprovider + `Bootstrap`, search, overlayprofile, `metadatarefresh.Refresher`, `wantedcron`, the segment planner, the replay controllers (R9) and `artwork.Reaper` (R9) | catalog/run.go:344–537; replay from :632; Reaper from :848 |
| `app/catalog/agent` | leaf: `Deps{Manager, Bus, Topology}`, `Registration`, and the index-assert helper generalised from `assertWorkerIndexes`; imports no worker package | `setupWorkers` (catalog/run.go:539-569); wiring.go:165-192 |
| `app/catalog/agent/catalog` | `Register`: catalogarr-search-high/-normal and catalogarr-grab (the search and grab halves of `buildQueueWorkers`/`setupQueueWorkers`), catalogarr-artwork-render (`setupArtworkWorker`); declares `search.IndexDownloadTarget`; TheXEM scene maps (`newSceneMaps`) | catalog/run.go:570-614, 669-787; wiring.go:50-105, 223 |
| `app/catalog/agent/events` | `Register`: catalogarr-rss-matcher and catalogarr-redownload (their halves of `buildQueueWorkers`), catalogarr-history and clustarr-dlq-projector (`setupHistory` without replay); declares `search.IndexDownloadTarget` and the 13 `rssmatcher.Index*`; its own scene maps | catalog/run.go:615-668, 669-787; wiring.go:50-192, 223 |
| `app/catalog/agent/metadata` | `Register`: catalogarr-metadata, the 4 RPCs, catalogarr-markers, catalogarr-segments-result, catalogarr-artwork-fetch (`setupMetadataGateway` without the Reaper) | catalog/run.go:788-853 |
| `app/import/manager` | `Register`: libraryscan, rename, rootfolderschedule, importexclusion, librarydelete, importlist, retrigger, `recyclesweep.Scheduler` | import/run.go:379–463 |
| `app/import/agent` | `Register`: importarr-scan (+ `IndexMediaFileByPath`), importarr-fileimport (+ `IndexMediaFileByTarget`), importarr-list, importarr-recycle, importarr-probe-high/-low (R3) | import/run.go:465–603 |
| `app/indexer/manager` | `Register`: Indexer (own limiter, R8), IndexerDefinition, IndexerProxy, DirectGrab, `bundle.Loader` leader-only (R9) | indexer/run.go:595–667 |
| `app/indexer/agent` | `Register(ctx, Deps, Options) (io.Closer, error)`: relindex, ClientCache and limiter, the search/download/query RPCs, `indexarr-rss`, the facade and its key Secret (`facadekey.go` moves here), the sweeper | indexer/run.go:380–430, 697–909, facadekey.go |
| `app/grab/manager` | `Register`: downloadclient (+ blocklist sweeper), download; `engineRuntime` | grab/run.go:353–403 |
| `app/grab/agent/torrent` | `Register` (torrent engine), `EngineIdentity` (replacing `splitEngineIdentity`), `torrentEngineDirs`, and proxy.go's `torrentProxy`/`proxyReadiness` | `setupTorrentEngine` (grab/run.go:456-575), `splitEngineIdentity` :445-455, :634-643, proxy.go |
| `app/grab/agent/usenet` | `Register` (usenet engine) | `setupUsenetEngine` (grab/run.go:576-633), `directClient` :437-444 |
| `app/squash/manager` | `Register` (pool/profile/job reconcilers + `ResultsConsumer`), `Cache`, `ParseSlots/FormatSlots/DefaultSlots` | squash/run.go:103–160, 383–480 |
| `app/caption/manager` | `Register`: subtitleprofile + `Bootstrap`, subtitleprovider, subtitlerequest | caption/run.go:244–271 |
| `app/caption/agent` | `Register`: captionarr-fetch-high/-normal | caption/run.go:285–293 |
| `app/autoscale` | `Register(mgr, admin events.StreamAdmin, Options)`: `controller.Reconciler`, `extmetrics.Server`, `CertManager`, `QueueGauge`, `NamespaceGuard` (§9) | new |

**`internal/cli/agent/domains.go` (R4)** maps `catalog` to
`app/catalog/agent/catalog.Register`, `events` to `app/catalog/agent/events.Register`
(the matcher reads search's Download target index, catalog/run.go:690, so
`events` declares it too), `metadata` to `app/catalog/agent/metadata.Register`,
`import` to `app/import/agent.Register`, `index` to `app/indexer/agent.Register`,
`caption` to `app/caption/agent.Register`, and `torrent-engine` / `usenet-engine`
to `app/grab/agent/torrent.Register` / `app/grab/agent/usenet.Register`.

**4.2.2 "No worker code" is an allow-list** of `app/` packages (§4.5.1). A new
`app/` package fails the manager guard until someone classifies it. Path
patterns alone would not do: the gateway lives at `app/catalog/metadata`, not
under `worker/`.

**4.2.3 Extraction rule.** The half the manager needs moves outside
`app/*/worker/` and `app/catalog/metadata/`. Where the manager's importers need
only an import-path change, the package name stays (the 8 item controllers keep
calling `artwork.PublishFetch`). Unexported identifiers that cross the new
boundary are exported; each step lists them, from a symbol-level coupling check.

**4.2.4 Flags** live in `internal/cli/<bin>`. In the one manager flag set,
grabarr's, squasharr's and captionarr's `--data-dir` and importarr's
`--data-path` collapse into `--data-dir`; grabarr's and squasharr's
`--data-claim` into `--data-claim`; `--role` disappears.

**4.2.5 Transitional packages** let the layout land before the R2 conversions,
each step compiling with unchanged behaviour: `pkg/mediainfo/ffprobeexec`
(Probe, ProbeAudio, runFrameProbe) and `pkg/subtitles/providers/embedded/execextract`
(the ffmpeg extractor). The R2 steps remove both from production: `ffprobeexec`
moves to `test/ffprobeoracle` as the probe-parity oracle (§6.2), and
`execextract` is deleted.

**4.2.6 Shared cmd plumbing** is `internal/cli` and `internal/cli/ctrlflags` (§3.2).

**4.2.7 Where R3 and R5 land.** The probe request publisher (`probe.go`) and the
leader-only KV-watch source (`results.go`) go in
`app/catalog/controller/mediafile`, the package of the sole MediaFileStatus
writer. The record protocol is `pkg/probestore`; the answering handler is
`app/import/worker/probe`; the ffgo probe is `pkg/mediainfo/native`, moved from
`app/squash/worker/inprocess/probe.go`; task and record types go in
`pkg/events/schema`. R5 lands as `app/autoscale/controller` (HPA reconciler)
and `app/autoscale/extmetrics` (the hand-rolled external.metrics.k8s.io server).

### 4.3 Ordered move list

**Rules for every step:**

- `_test.go` files whose subject moved go with it; `export_test.go` shims split
  to match.
- RBAC markers move with the code that needs the grant; `make generate manifests`
  regenerates.
- `go build ./... && go vet ./... && make test` stays green.
- No step adds or removes a module. The go.mod edits (ffgo replace, par2go
  replace, `k8s.io/component-helpers v0.37.0`) are serial and belong to their
  steps (§11.1).
- Within a wave, steps touch disjoint files, except: 1.2, 1.3 and 1.6 all touch
  `pkg/k8s` (one owner); each service's Wave 2 and 3 steps share its `run.go`
  (one owner per service).

**Wave 1: pkg leaves**

- **1.1 pkg/obs.** bootstrap.go:36,91: `ctrl.SetLogger` becomes
  `sigs.k8s.io/controller-runtime/pkg/log.SetLogger`; the same function.
- **1.2 `pkg/k8s/conditions`.** Moves out of pkg/k8s/conditions.go (:33–195):
  `ConditionReady`, `FindCondition`, `IsConditionTrue`, `IsConditionFalse`,
  `IsReady`, `ObservedGeneration`, `StatusUpToDate`. pkg/k8s keeps one-line
  wrappers, so no caller changes. `pkg/pipeline/project.go:591–592` imports
  `pkg/k8s/conditions`.
- **1.3 `pkg/busconn`**, out of pkg/k8s/bus.go. Renames `ConnectBus`→`Connect`,
  `BusOption`→`Option`, `WithBusHooks`→`WithHooks`, `BusReadyChecker`→`ReadyChecker`;
  `EnsureTopology` and `DefaultNATSURL` keep their names; new `AwaitTopology`
  (§3.5.2). `(k8s.Options).BusTopology` stays in pkg/k8s (bus.go:115). pkg/k8s
  keeps thin wrappers until Wave 5 deletes cmd/clustarr.
- **1.4 `pkg/overlay/render`.** Carries pkg/overlay/overlay.go, `fonts/` and
  render_test.go; nothing crosses (template.go names `drawBadge/layoutBoxes`
  only in a comment). Caller: app/catalog/worker/artwork/handler.go:442.
- **1.5 `pkg/subtitles/postprocess`.** Carries postprocess.go and uppercase.go;
  copy the one-line `replaceAll` from himods.go:66, and move or export the two
  hearing-impaired patterns uppercase.go:125,128 use, `hiSpeakerLabel` and
  `hiBrackets` (himods.go:51,58), which the symbol-level check first missed.
  Caller: app/caption/worker/fetch/search.go:360.
- **1.6 pkg/k8s/dataready.go** takes `DataReadyChecker` from
  app/import/dataready.go:57; the import and caption agents use it.
- **1.7 transitional `pkg/mediainfo/ffprobeexec`.** Carries `Probe`
  (mediainfo.go:115), `ProbeAudio` (audio.go:62), `runFrameProbe` (ffprobe.go:145).
  pkg/mediainfo exports `BuildRaw`, `MergeFrame`, `AudioProbeFrom`. Callers:
  mediafile_controller.go:165, rescan/probe.go:51, rescan/worker.go:167,
  fileimport/process.go:441, fileimport/worker.go:136. pkg/mediainfo keeps
  go-ffprobe as types only (`Raw`).
- **1.8 pkg/subtitles/providers/embedded.** `Config` gains the `Extract` func of
  §7.3.1; the exec body moves to transitional `embedded/execextract`, wired by
  app/caption/run.go into the fetch worker.
- **1.9 `pkg/binpath`**, the five path constants (§3.1).

**Wave 2: app leaves**

*Catalog*

- **C1. `app/catalog/artwork` (light).** Carries metadata/artwork's drift.go,
  item.go and reaper.go, plus sources.go cut from fetcher.go:100–160
  (`imageTypes`, `KnownType` (was knownType), `Source`, `ResolveSources`,
  `Fetchable` (was fetchable), `index`). Exports `Item`, `ItemOf`, `NewObject`,
  `PublishRender` for the gateway half's `Pass`. The 8 controllers change import
  path only; run.go:848 (`Reaper`) changes. reaper.go gets
  `+kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;albums;authors;books;audiobooks;comics,verbs=list`.
  The status-write markers at doc.go:54–56 stay with the gateway half,
  `app/catalog/metadata/artwork` (Fetcher, Handler, Pass, KeepAlive, the
  `x/image/webp` import).
- **C2. `app/catalog/overlayplan`**, from worker/artwork/plan.go.
  overlayprofile/controller.go:41 switches; handler.go qualifies
  `Item/ItemOf/NewObject/Overlaid/Plan/Want/Winner`.
- **C3. `app/catalog/controller/metadatarefresh`**, from metadata/refresher.go
  with its marker (:90). run.go:437 switches; no symbol crosses.
- **C4. Delete `BuildRegistry/addToRegistry`** (metadataprovider/registry.go:53–210;
  only tests call them). Keep `buildSupplementary` and `newSupplementaryProber`.
- **C5. `app/catalog/grabsource`**, from worker/grab/downloads, renamed.
  controller/search/grab.go:44,55 and worker/grab switch.
- **C6. `app/catalog/worker/segmentresults`**, from segmenting's apply.go,
  results.go and the Results half of setup.go (`NewApplier`), with marker
  setup.go:32 (`mediafiles/status patch`). segmenting keeps plan.go, the
  planner `Setup` and marker :31. run.go:809 switches.
- **C7. `app/catalog/worker/markers`**, from markers' handler.go and setup.go
  with markers handler.go:60–61. `app/catalog/markers` keeps due.go and
  publish.go and gets the `now` clock var. run.go:803 switches; the handler uses
  `segmentresults.Applier`.
- **C8. History splits in three.**
  - `app/catalog/history` (shared, no RBAC markers): target.go, dlqstore.go,
    doc.go, and the constants `AnnotationDeadLettered` and
    `AnnotationDeadLetterSeq` (from dlq.go), `AnnotationReplay` (from
    replay.go:69; dlq.go:222 names it in the DLQ Event, and importing
    `history/replay` instead would link the replay controllers into
    `cmd/agent`) and `EventType` (was sink.go's `eventType`).
  - `app/catalog/history/replay` (manager): replay.go with its markers
    (replay.go:58-62).
  - `app/catalog/worker/history` (agent `events`): sink.go and dlq.go with their
    markers (sink.go:45, dlq.go:88–103).

- **C9. Three leaves so the agent links no controller registration.** The
  agent's workers reach three controller packages that call
  `ctrl.NewControllerManagedBy` (delayprofile/controller.go:111,
  search/reconciler.go:175, series/reconciler.go:164) for one helper each:
  - `app/catalog/delay` (package `delay`): `Resolve`, moved from
    controller/delayprofile/resolve.go:49 with its test; used by
    worker/rssmatcher/resolve.go:293 and by the delayprofile controller.
  - `app/catalog/searchoutcome`: `WorkerOutcomeName`, `TruncatedOutcomeName`
    and `UnnamedOutcomeName`, from controller/search/reconciler.go:463-485;
    used by worker/search/worker.go:500,806,836 and by the search controller.
  - `app/catalog/episodeorder`: `EffectiveEpisodeOrder`, moved from
    controller/series/order.go:39; used by markers/handler.go:242 (C7's
    `app/catalog/worker/markers`) and by the series controller.
  The agent still links `app/catalog/controller/rollup` (`DownloadNonTerminal`)
  and `app/catalog/controller/wantedcron` (`ListCandidates`); neither calls
  `NewControllerManagedBy`, which `TestOnlyTheManagerRegistersControllers`
  checks (§10.3.2).

*Import*

- **I1. `app/import/scanprogress`**
, from rescan/progress.go; libraryscan switches.
- **I2. `app/import/mediafilespec`.** From rescan: the `RenameFile` path
  (rename.go:41–305, `clampRunes` :409) and the spec renderer from mediafile.go
  (`ObservedFingerprint` :94, `staleReadError` :111, `sameFingerprint` :425,
  `frozenFields` :459, `applySpec` :613, `reassertFrozen` :675), exported as
  `Apply`, `ReassertFrozen`, `Frozen`, `StaleReadError`, `SameFingerprint`, with
  `FieldManager = k8s.ManagerImportarrWorker`. rescan keeps
  `renameMode/renamePass/renameCandidates` (rename.go:311–408) in rescan/renamepass.go.
  rename/controller.go:129 switches.
- **I3. `app/import/importliststate`**, from worker/importlist's result.go,
  tokenstore.go, kinds.go and `AnnotationSyncedAt` (worker.go:211);
  `HasCatalogWriter` exported (kinds.go:138; sync.go:113 uses it).
  importlist/controller.go:40 and auth.go:28 switch.
- **I4. `app/import/importtarget`**, from fileimport/annotation.go.
- **I5. `app/import/controller/retrigger`**, from fileimport/retrigger.go, with its
  own marker `downloads get;list;watch` (today fileimport/worker.go:84).
  run.go:448 switches.

*Indexer*

- **X1. `app/indexer/rssschedule`**, from rss/worker.go: `defaultRssInterval`
  :101, `rssInterval` :832, `TaskMsgID` :847, `NextPollAt` :875, `ScheduleNext`
  :895, `newestSeen` :941. This alone removes sqlite and pgx from the controller.
- **X2. `app/indexer/clients`** (shared), from controller/indexer's cardigann.go,
  session.go, source.go and proxy.go. Exported: `Client`, `BuildClient`,
  `BuildCardigann`, `ApplyRateLimit`, `ResolveSource`, `ReadSecret`,
  `ResolveProxy`, `ResolveDefinition`, `SessionStore`, `NewSessionStore`,
  `SessionKey`, `SessionSecretName`, `Classify`, `ClassifyLogin`, `ProbeOutcome`,
  `OutcomeLabel`, `PrivacyFor`, `ProtocolFor`, `DefinitionCaps`,
  `DefinitionDelay`, the `Reason*` constants, `ErrDefinitionNotFound`,
  `MaxCapsItems` (caps.go), and six identifiers the first coupling check missed:
  `CardigannClient` (was `cardigannClient`, cardigann.go:300; clientcache.go:203,289),
  `SourceGeneric` (was `sourceGeneric`; controller.go:268, clientcache.go:251),
  `ErrDefinitionInvalid` (cardigann.go:73), `DefinitionProtocol` (:266),
  `DefinitionPrivacy` (:191) and `SessionRenewMargin` (:469), all four read by
  controller_definition.go:75,85,86,178. Markers doc.go:189–191 and :209 (indexers,
  definitions and proxies read; `secrets get;create;patch`) are duplicated into
  clients/doc.go; :210 (events) stays with the reconciler.
- **X3. `app/indexer/clientcache`** (agent), from clientcache.go. The
  reconciler's `Clients *ClientCache` becomes a `ForgetClient func(types.UID)`
  hook (controller.go:115,227; controller_definition.go:149), wired by run.go to
  `clients.Forget` so behaviour is unchanged until R8 replaces it with the
  session-KV watch (§5.12).
- **X4. `app/indexer/controller/directgrab`**, from download/directgrab.go plus
  `countGrabAt` (service.go:391–417). The `resultGrab*` labels are exported in
  `app/indexer/limits`; `Service.CountGrabForTest` moves to a directgrab test
  helper. run.go:621 switches.

*Grab, squash, caption*

- **G1.** `app/grab/status.EngineFinalizer = "download.clustarr.io/engine"`;
  engine.go:87 becomes `const Finalizer = status.EngineFinalizer`;
  controller/download switches.
- **S1. `app/squash/jobspec`.** Carries worker's buildtask.go, exit.go, paths.go
  and profile.go, plus `StandardProfile/StandardTier` (ffgo.go:63–90; at main
  `80175fdc` ffgo.go:84–108) and `CPULimitEnv` (run.go:87–93; now :94);
  `localPath` and `within` exported as `LocalPath` and `Within` (run.go:271,275,295
  use them). pool, transcodejob and transcodeprofile switch from worker to
  jobspec. Since the MP4 standard (main `b33e4417`) profile.go also holds
  `OutputContainer` (`ContainerMP4`, which `ProfileHashAt` uses from Version 2
  on), so it moves too: transcodejob's `recordPlan`, `markPlanned` and `planFor`
  call sites (controller.go:541,577; dispatch.go:266,275,323) read
  `jobspec.OutputContainer`. The worker's sidecar code (`placeSidecars`,
  `sidecarParts`, the container refusal) stays in the worker.
- **P1. Split `app/caption/providerset`.** The light half stays: Entry,
  FileSource, Order, Serves, errors, Validate, resolve, readSecret, requireKeys;
  `NeedsSecrets/HIVerifiable` read a static `capabilities` table (§13 OD9);
  markers doc.go:71–72 stay (both sides need them). New
  `app/caption/providerset/build`: Builder, NewBuilder, `Builder.Entry`,
  prototype, TokenCache, prune, and `TestCapabilityTableMatchesTheClients`
  comparing the table with every `prototype(t)`.

**Wave 3: registration packages and R9.** Create the registration packages of
§4.2.1 (six manager-side, eight per-domain agent-side and the `app/catalog/agent`
leaf; `app/autoscale` comes with §11.1 step 7) from the run.go ranges given there. Each `app/<svc>/run.go` becomes a shim
calling them per `--role`, so cmd/clustarr keeps working. In the same step apply
R9 and update cmd/clustarr's start envtests: the replay controllers move from
RoleHistory to RoleController; `artwork.Reaper` from RoleMetadata to
RoleController; `bundle.Loader` from `k8s.EveryReplica(loader.Run)`
(indexer/run.go:663) to `k8s.LeaderOnly`.

**Wave 4: behaviour steps owned by other sections** (listed for their package effect):

- **R3** (§6): the MediaFile probe queue removes the manager's only route to
  `ffprobeexec`.
- **R8** (§5.12): `app/indexer/agent` builds its own ClientCache and limiter;
  the `ForgetClient` hook goes.
- **R2** (§6, §7, §8): `pkg/mediainfo/native` replaces `ffprobeexec` (which
  becomes `test/ffprobeoracle`); `embedded/native` replaces `execextract`;
  `pkg/segments/decode` decodes on ffgo; the usenet engine repairs through
  par2go in `pkg/par2child`.
- **R5** (§9): `app/autoscale/...` and `pkg/agentdomain`.

**Wave 5: binaries**

- **5.1 `cmd/manager`** (main.go and tests) plus `internal/cli/manager`
  (flags.go, options.go). If this lands before R3, the deps guard carries one
  temporary exception: `pkg/mediainfo/ffprobeexec`, imported only by
  `app/catalog/controller/mediafile`; R3's step deletes it.
- **5.2 `cmd/agent`** (main.go with the par2 child dispatch, §8.4.4; tests) plus
  `internal/cli/agent` (flags.go, domains.go). `downloadclient/workload.go:79–84`
  `clustarrBinary` becomes `binpath.Agent`, argv
  `--domain torrent-engine|usenet-engine`, no `/bin/sh` (R4).
- **5.3 `cmd/ui`** plus `internal/cli/ui`, from cmd/clustarr/services.go:446–716
  and flags.go:160–196, using `pkg/busconn.Connect`, controller-runtime's
  `pkg/client/config`, `pkg/manager/signals` and `pkg/log`, never the
  controller-runtime root.
- **5.4 Renames.** `git mv cmd/segmentarr-worker cmd/markers`;
  `git mv cmd/squasharr-worker cmd/transcode`.
- **5.5** The `test/guards/depguard` helper and the §4.5 guards.
- **5.6 Deletions.** cmd/clustarr and the six `app/<svc>` root packages;
  app/indexer/facadekey.go goes to `app/indexer/agent`, app/grab/proxy.go to
  `app/grab/agent`. Recut the Makefile build targets and RBAC paths (§4.6).

### 4.4 Final package tree

Legend: **[M]** cmd/manager only; **[A:d]** agent domain d; **[S]** shared by
manager and agent, light; **[U]** cmd/ui; **[K]** cmd/markers; **[T]** cmd/transcode.

```
cmd/manager [M]   cmd/agent   cmd/ui [U]   cmd/markers [K]   cmd/transcode [T]
internal/cli [M,A,U]   internal/cli/ctrlflags [M,A]
internal/cli/manager [M]   internal/cli/agent [A]   internal/cli/ui [U]
test/guards (+ test/guards/depguard)   test/installtest   test/starttest   test/system
test/nativetest   test/ffprobeoracle   test/par2test   test/parity

app/catalog/
  manager [M]   agent [A] (leaf: Deps, Registration)
  agent/catalog [A:catalog]   agent/events [A:events]   agent/metadata [A:metadata]
  delay [S]   searchoutcome [S]   episodeorder [S]
  controller/{album,artist,audiobook,author,book,comic,delayprofile,episode,issue,
              mediafile,metadataprovider,metadatarefresh,movie,overlayprofile,
              qualityprofile,rootfolder,search,series,wantedcron} [M]
  controller/rollup [S]   status [S]
  artwork [S] (Drift, PublishFetch, RenderToken, PublishRender, ResolveSources, Item, Reaper)
  overlayplan [S]   grabsource [S]   markers [S] (Due, MsgID, Publish, PublishAt)
  segmenting [M] (PublishPlan, Planner)
  history [S] (target, dlqstore, constants)   history/replay [M]
  metadata [A:metadata] (gateway, 19 clients)   metadata/artwork [A:metadata] (Fetcher, Handler, Pass)
  worker/{search,grab,artwork} [A:catalog]
  worker/{rssmatcher,redownload,history} [A:events] (+ worker/grab, shared with catalog)
  worker/{markers,segmentresults} [A:metadata]
app/import/
  manager [M]   agent [A:import]
  controller/{importexclusion,importlist,librarydelete,libraryscan,rename,
              rootfolderschedule,retrigger} [M]
  scanprogress [S]   mediafilespec [S]   importliststate [S]   importtarget [S]
  worker/{rescan,fileimport,importlist,probe} [A:import]
app/indexer/
  manager [M]   agent [A:index] (+ facade key)
  controller/{indexer,indexerdefinition,indexerproxy,directgrab} [M]   bundle, bundle/embedded [M]
  clients [S]   proxy [S]   status [S]   limits [S]   rssschedule [S]
  clientcache, search, query, facade, download, worker/rss [A:index]
app/grab/
  manager [M]   agent/torrent [A:torrent-engine]   agent/usenet [A:usenet-engine]
  controller/{download,downloadclient} [M]   status [S] (+EngineFinalizer)
  engine, engine/torrent, engine/usenet [A:engines]
app/squash/
  manager [M]   controller/{pool,transcodejob,transcodeprofile} [M]   status [M]
  task [M,T]   jobspec [M,T]   worker, worker/inprocess [T]
app/caption/
  manager [M]   agent [A:caption]
  controller/{subtitleprofile,subtitleprovider,subtitlerequest} [M]
  providerset, status, throttle, itemindex, datapath [S]
  providerset/build, worker/fetch [A:caption]
app/segments/worker [K]
app/autoscale, app/autoscale/{controller,extmetrics} [M]

pkg/agentdomain [M,A]   pkg/binpath [M,A,U,K,T]   pkg/busconn [M,A,U]
pkg/k8s [M,A]   pkg/k8s/conditions [M,A,U]   pkg/probestore [M,A:import]
pkg/mediainfo [S]   pkg/mediainfo/native [A:import,T]   pkg/ffruntime [A:import,A:caption,K,T]
pkg/overlay [S]   pkg/overlay/render [A:catalog]
pkg/subtitles [S]   pkg/subtitles/postprocess [A:caption]
pkg/subtitles/providers/embedded [S]   .../embedded/native, opensubtitlescom, gestdown, subdl, subsource [A:caption]
pkg/segments [S]   pkg/segments/{decode,textdet,chromaprint,frames,align} [K]
pkg/par2child, pkg/download/usenet, github.com/mediactl/par2go [A:usenet-engine]
pkg/download/torrent, pkg/socks5 [A:torrent-engine]
pkg/relindex [A:index]
pkg/metadata/clients/* [A:metadata; M only via controller/metadataprovider]
pkg/importlist, pkg/importlist/trakt [S]   pkg/importlist/{arr,custom,imdbcsv,mdblist,plex,stevenlu,tmdb} [A:import]
```

**Simulated closures** (total / non-stdlib / own packages):

| Binary | Closure | Notes |
|---|---|---|
| cmd/manager | 1201 / 954 / 142 | 71 `app` packages. Without the Wave 2 cuts: 1259 / 1008 / 150. |
| cmd/ui | 762 / 526 / 57 | today's ui closure is 1063 |
| cmd/agent | 1520 / 1262 / 146 | |
| cmd/markers | 537 | segmentarr-worker today: 520 (before §7.2.9 drops the API types) |
| cmd/transcode | 540 | squasharr-worker today: 535 |

The agent still links two controller-directory packages through its workers,
`app/catalog/controller/rollup` (`DownloadNonTerminal`) and
`app/catalog/controller/wantedcron` (`ListCandidates`); neither calls
`ctrl.NewControllerManagedBy`. The three that did (`delayprofile`, `search`,
`series`) are reached no longer, after C9. `TestOnlyTheManagerRegistersControllers`
(§10.3.2) holds both facts.

### 4.5 Dependency guards

**Helper `test/guards/depguard`** (imported only from `_test.go` files):

```go
func Load(t testing.TB, pkg string, env ...string) Graph   // go list -deps -json=ImportPath,Imports,Standard <pkg>, with env
func (g Graph) Deny(t testing.TB, rules []Rule)            // one failure per hit: rule.Why + shortest import chain
func (g Graph) Require(t testing.TB, pkg, why string)      // must-link anchors, so a renamed package cannot make a guard vacuous
func (g Graph) AllowApp(t testing.TB, allowed []*regexp.Regexp)        // every module app/ package must match one
func (g Graph) OnlyImportedBy(t testing.TB, target func(string) bool, importers func(string) bool, why string)
type Rule struct {
    Path   string
    Exact  bool     // false: Path or Path/...
    Except []string // exact import paths under Path that the rule allows
    Why    string
}
```

All five binaries' deps guards live in `test/guards/deps_test.go` (R6 per
binary, R10 location), and only there: `cmd/transcode/main_test.go` and
`cmd/markers/main_test.go` carry none (§10.3.4). Each pins the build
environment its image uses; today's static-link guard does not.

#### 4.5.1 cmd/manager

`TestManagerNeverLinksADynamicLoader` (carries `TestClustarrNeverLinksADynamicLoader`,
cmd/clustarr/static_link_guard_test.go:33), `TestManagerLinksNoAgentCode`,
`TestManagerLinksMetadataClientsOnlyToProbeThem`. Environment
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64` (images/Dockerfile.controller:21).

| Group | Denied (prefix unless exact) | Why |
|---|---|---|
| Dynamic loading and native media | `github.com/ebitengine/purego`, `github.com/obinnaokechukwu/ffgo`, `github.com/shota3506/onnxruntime-purego`, `github.com/mediactl/par2go`, `…/clustarr/pkg/par2child`, `…/pkg/ffruntime`, `…/pkg/mediainfo/native`, `…/pkg/mediainfo/ffprobeexec`, `…/pkg/subtitles/providers/embedded/native`, `…/embedded/execextract`, `…/pkg/segments/{decode,textdet,chromaprint,frames,align}` | static distroless image (R1); the manager never probes (R3) |
| Storage and index | `modernc.org/sqlite`, `modernc.org/libc`, `github.com/jackc/pgx`, `github.com/go-llsqlite`, `zombiezen.com/go/sqlite`, `…/pkg/relindex` | R6, R8 |
| Download engines | `github.com/anacrolix`, `github.com/pion`, `github.com/Tensai75/nntp`, `github.com/javi11/`, `github.com/nwaples/rardecode`, `github.com/bodgit/sevenzip`, `…/pkg/download/torrent`, `…/pkg/download/usenet`, `…/pkg/socks5` | R6 |
| Agent-only media | `golang.org/x/image`, `github.com/asticode`, `…/pkg/overlay/render`, `…/pkg/subtitles/postprocess` | rendering and subtitle post-processing are agent work |
| Clients the manager never calls | `…/pkg/subtitles/providers/{opensubtitlescom,gestdown,subdl,subsource,internal}`, `…/pkg/importlist/{arr,custom,imdbcsv,mdblist,plex,stevenlu,tmdb}` | R6 |
| Agent and ui code | regex `^github.com/mediactl/clustarr/app/[a-z]+/(worker\|agent)(/\|$)`, `…/app/catalog/metadata`, `…/app/indexer/{search,query,facade,download,clientcache}`, `…/app/grab/engine`, `…/app/caption/providerset/build`, `…/app/segments`, `…/ui`, `…/test/`, `github.com/a-h/templ`, `github.com/Oudwins/tailwind-merge-go` | R6 |
| Servers | `github.com/nats-io/nats-server`, `github.com/fergusstrange/embedded-postgres`, `k8s.io/apiserver`, `k8s.io/kube-aggregator`, `k8s.io/metrics`, `sigs.k8s.io/custom-metrics-apiserver` | test-only servers; R5's metrics API is hand-rolled |

**Require:** `sigs.k8s.io/controller-runtime/pkg/manager`,
`app/catalog/controller/movie`, `app/squash/controller/transcodejob`,
`app/grab/controller/downloadclient`.

**AllowApp:** `^app/[a-z]+/manager$`, `^app/[a-z]+/controller(/.+)?$`,
`^app/[a-z]+/status$`, `^app/autoscale(/.+)?$`;
`app/catalog/{artwork,overlayplan,grabsource,markers,segmenting,history,history/replay,delay,searchoutcome,episodeorder}`;
`app/import/{scanprogress,mediafilespec,importliststate,importtarget}`;
`app/indexer/{clients,proxy,limits,rssschedule,bundle,bundle/embedded}`;
`app/squash/{jobspec,task}`; `app/caption/{providerset,throttle,itemindex,datapath}`.

**OnlyImportedBy:** `pkg/metadata/clients/<x>` with x ≠ `extid` may be imported
only by `app/catalog/controller/metadataprovider` and other
`pkg/metadata/clients/*` packages. `extid` is a constants leaf the comic
controller imports. With the cuts applied the simulation shows no other route.

**What the manager may link:** controller-runtime, client-go and apiextensions
types; nats.go and natsbus (through busconn), otel and grpc, prometheus, cobra
and pflag; `pkg/cardigann` with goquery and jsonschema (the Indexer reconciler
logs in; the bundle loader validates 752 definitions); the 18 metadata clients,
through metadataprovider only; `pkg/importlist` and `pkg/importlist/trakt` (the
device flow, importlist/auth.go:75,136); go-ffprobe as types only
(`mediainfo.Raw`); `pkg/transcode` and `pkg/transcode/standard` (squash
planning); `pkg/overlay` (templates, badges, 64 KiB of logo PNGs);
`pkg/probestore`; the quality catalogue, regexp2 and the 1.6 MB Cardigann bundle;
`k8s.io/component-helpers` (§9.5).

#### 4.5.2 cmd/ui

`TestUILinksNoAppOrManagerCode`, `TestUINeverLinksADynamicLoader`. Environment
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`.

- **Deny:** `…/clustarr/app` (prefix; R6); `…/clustarr/pkg/k8s` (exact; where
  PatchStatus and Apply live; `pkg/k8s/conditions` is allowed);
  `sigs.k8s.io/controller-runtime` (exact), `…/pkg/manager` (exact),
  `…/pkg/{builder,leaderelection,webhook,cluster}` (prefix); every
  Dynamic-loading, Storage and Download-engine entry of §4.5.1;
  `…/pkg/metadata/clients` (the ui asks the gateway over RPC); `nats-server`,
  `embedded-postgres`, `k8s.io/apiserver`.
- **Require:** `ui`, `ui/plex`.
- **Allowed:** controller-runtime `pkg/{cache,client,client/config,log,manager/signals,healthz}`;
  client-go informers, k8s and apiextensions API types (ui/schema); nats.go and
  natsbus through `pkg/busconn`; templ and tailwind-merge, x/text/language,
  golang-lru; `pkg/pipeline` and `pkg/k8s/conditions`.

`ui/guard_test.go:159` (`TestUINeverWrites`) is unchanged: it bans direct
`pkg/k8s` imports under `ui/`, and this guard adds the transitive check it never
had.

**Amended 2026-10-07 (NATS research; `nats-object-store.md` §1.3 G7, §6.10).**
`TestUINeverWrites` is extended, since the ui now holds a watch on the artwork
bucket and its read-only status rests on the guard alone (neither installer
configures NATS authorization):

- `neverWriteSelectors` gains `PutString`, `PutFile`, `AddBucketLink`, `SetMeta`,
  `CreateObjectStore`, `UpdateObjectStore`, `CreateOrUpdateObjectStore`,
  `DeleteObjectStore` and `PurgeOrphanChunks`, beside today's `Put`, `PutBytes`,
  `UpdateMeta`, `Seal`, `AddLink` and `Purge`.
- No non-test file under `ui/` imports `github.com/nats-io/nats.go` or
  `github.com/nats-io/nats.go/jetstream`: the ui reaches NATS only through
  `pkg/events` and the bus `internal/cli/ui` hands it. This deps guard still
  allows nats.go transitively, through `pkg/busconn`.

`Watch`, `Status`, `Info`, `Get` and `List` stay allowed: they are reads.

#### 4.5.3 cmd/agent

`TestAgentLinksNoManagerRegistration`, `TestAgentKeepsTheCgoPieceCompletion`.
Environment `CGO_ENABLED=1 GOOS=linux GOARCH=amd64` (R13).

- **Deny:** `^github.com/mediactl/clustarr/app/[a-z]+/manager$`, `…/app/autoscale`,
  `…/app/squash/controller`, `…/app/squash/worker`, `…/app/segments`,
  `…/pkg/segments/{decode,textdet,chromaprint,frames,align}`,
  `onnxruntime-purego` (the 4.8 MB ONNX model stays in markers), `…/ui`,
  `…/test/`, `templ`, `tailwind-merge-go`, `nats-server`, `embedded-postgres`,
  `k8s.io/apiserver`, `custom-metrics-apiserver`; after R2,
  `…/pkg/mediainfo/ffprobeexec` and `…/embedded/execextract`.
- **Require:** `app/grab/engine/torrent` and `github.com/go-llsqlite/adapter`
  from the guard's first commit (§11.1 step 8); `github.com/obinnaokechukwu/ffgo`
  and `…/pkg/mediainfo/native` added by step 11 (the native probe; until then the
  agent probes through `ffprobeexec` and links no ffgo); `pkg/par2child` and
  `github.com/mediactl/par2go` added by step 12 (par2). The chain
  `github.com/go-llsqlite/adapter <- github.com/anacrolix/torrent/storage <- pkg/download/torrent`
  exists only under cgo (verified: `CGO_ENABLED=0 go list -deps ./pkg/download/torrent`
  lists `go.etcd.io/bbolt` and no `go-llsqlite`), so a cgo-free agent fails here
  before every seeding torrent would rehash.

#### 4.5.4 cmd/markers

`TestMarkersImportsNoKubernetesClient`. Environment `CGO_ENABLED=0`.

- **Deny:** `k8s.io/client-go` (prefix); `k8s.io/api` (prefix, after §7.2.9);
  `sigs.k8s.io/controller-runtime` (prefix); `…/pkg/k8s` (prefix), `…/pkg/obs`
  (exact), `…/pkg/busconn`; `…/app/squash`, `…/ui`, `nats-server`,
  `modernc.org/sqlite`, `github.com/jackc/pgx`, `github.com/anacrolix`,
  `github.com/mediactl/par2go`.
- **AllowApp:** `^app/segments/worker$` only.
- **Require:** `app/segments/worker`, `github.com/obinnaokechukwu/ffgo`,
  `…/pkg/ffruntime`, `github.com/shota3506/onnxruntime-purego/onnxruntime`.
- **Allowed:** `k8s.io/apimachinery` (through `pkg/events/schema`), ffgo/purego,
  onnxruntime-purego, nats.go/natsbus, promhttp,
  `pkg/obs/{logging,tracing,metrics,obsflags}`.

#### 4.5.5 cmd/transcode

`TestTranscodeImportsNoKubernetesClient` (was `TestBinaryImportsNoKubernetesClient`,
cmd/squasharr-worker/main_test.go:140), moved into `test/guards/deps_test.go`
with its list widened. Environment `CGO_ENABLED=0` (images/Dockerfile.transcoder:54).

- **Deny:** `k8s.io/client-go` (prefix); `sigs.k8s.io/controller-runtime`
  (prefix) with `Except: ["sigs.k8s.io/controller-runtime/pkg/scheme"]`, because
  `api/catalog/v1alpha1/groupversion_info.go:34` and
  `api/transcode/v1alpha1/groupversion_info.go:29` import it and transcode keeps
  those API types (§13 OD11; today `go list -deps ./cmd/squasharr-worker` lists
  it through `api/catalog/v1alpha1 <- app/squash/worker`, which is why today's
  guard names only `pkg/client` and `pkg/manager`); `…/pkg/k8s` (prefix);
  `…/pkg/obs` (exact); `…/pkg/busconn`; `…/app/squash/controller`, `…/ui`,
  `nats-server`, `modernc.org/sqlite`, `github.com/jackc/pgx`,
  `github.com/anacrolix`, `github.com/mediactl/par2go`;
  `onnxruntime-purego`, `…/app/segments`, `…/pkg/segments/{decode,textdet}`.
  `k8s.io/api` and `k8s.io/apimachinery` are allowed.
- **AllowApp:** `^app/squash/(worker(/inprocess)?|jobspec|task)$`.
- **Require:** `app/squash/worker/inprocess`, ffgo, `…/pkg/ffruntime`.

`app/squash/worker/imports_test.go:28` stays as it is.

#### 4.5.6 Repo-wide exec guard

`test/guards/exec_test.go` `TestNoProductionCodeStartsAProcess` replaces
`app/squash/exec_guard_test.go:40` (`TestTheWorkerNeverExecsFFmpeg`, rooted at
cmd/squasharr-worker) and the pkg/mediainfo half of it.

- **Scope:** the AST of every non-test `.go` file under `app/`, `pkg/`, `cmd/`,
  `ui/` and `api/`. `hack/` and `test/` are out of scope, which is why the
  parity oracles live under `test/`.
- **Fails on:** an `os/exec` import; calls to `syscall.Exec`, `syscall.ForkExec`
  or `os.StartProcess`; calls to go-ffprobe `Probe*` or `SetFFProbeBinPath`.
- **Transitional allow-list:** `pkg/mediainfo/ffprobeexec`,
  `pkg/subtitles/providers/embedded/execextract`, `pkg/segments/decode`,
  `pkg/download/usenet`. Each R2 step removes its entry.
- **Permanent allow-list, exactly one file:** `pkg/par2child/exec.go`, whose
  only exec target is `os.Executable()` (the par2 child, §8.4). Its own guard
  `TestTheOnlyExecIsTheRepairChild` holds that.

### 4.6 RBAC paths that follow from the tree

Role names and files are §10.2.4. Each `RBAC_PATHS_*` stays on one line,
because `rbacRoles` parses the Makefile line by line
(`cmd/clustarr/rbac_markers_test.go:62-68`):

```make
RBAC_ROLES := manager agent-catalog agent-events agent-metadata agent-import agent-index agent-caption grabarr-engine
RBAC_PATHS_manager := ./app/catalog/controller/... ./app/catalog/history/replay ./app/catalog/segmenting ./app/catalog/artwork ./app/import/controller/... ./app/indexer/controller/... ./app/indexer/bundle/... ./app/indexer/clients ./app/indexer/proxy ./app/indexer/status ./app/grab/controller/... ./app/grab/status ./app/squash/controller/... ./app/squash/status ./app/caption/controller/... ./app/caption/providerset ./app/autoscale/...
RBAC_PATHS_agent-catalog := ./app/catalog/worker/search ./app/catalog/worker/grab ./app/catalog/worker/artwork
RBAC_PATHS_agent-events := ./app/catalog/worker/rssmatcher ./app/catalog/worker/grab ./app/catalog/worker/redownload ./app/catalog/worker/history
RBAC_PATHS_agent-metadata := ./app/catalog/metadata/... ./app/catalog/worker/markers ./app/catalog/worker/segmentresults
RBAC_PATHS_agent-import := ./app/import/worker/...
RBAC_PATHS_agent-index := ./app/indexer/agent ./app/indexer/clients ./app/indexer/clientcache ./app/indexer/proxy ./app/indexer/status ./app/indexer/search ./app/indexer/query ./app/indexer/download ./app/indexer/facade ./app/indexer/worker/...
RBAC_PATHS_agent-caption := ./app/caption/worker/... ./app/caption/providerset/...
RBAC_PATHS_grabarr-engine := ./app/grab/engine/... ./app/grab/status
```

- `app/catalog/worker/grab` is in both `agent-catalog` and `agent-events`,
  because the RSS matcher grabs through `grab.Sink`.
- Other packages deliberately in two roles: `app/caption/providerset`,
  `app/indexer/{clients,proxy,status}`, `app/grab/status` (manager and engines).
- A shared leaf carries only markers both sides need; a marker only one side
  needs lives in that side's package (hence the three-way history split, C8).
  `app/catalog/history` (shared) carries no markers.

### 4.7 Binary sizes

**Method.** Measured: `go build -trimpath -ldflags "-s -w"`. Projected: take
today's unstripped `cmd/clustarr`, attribute its non-BSS symbol bytes
(`go tool nm -size`) to packages (89% attributable), sum over each simulated
closure, scale to the stripped size. Projections are upper bounds.

| Binary | Today (measured) | Projected | Direction |
|---|---|---|---|
| cmd/clustarr | 1569 pkgs; 93.9 MB static (CGO=0, Makefile:165, controller image); 95.6 MB dynamic (CGO=1, media image) | removed | — |
| cmd/manager | — | ≈ 60 MB static (1201 pkgs) | Without the cuts ≈ 70 MB, linking sqlite, pgx, x/image, astisub, rardecode and the list providers. ≈ 3.0 MB (+34 pkgs) is metadata clients via metadataprovider (§13 OD10). |
| cmd/ui | (its closure alone: 1063 pkgs ≈ 51 MB) | ≈ 48 MB static (762 pkgs) | −301 pkgs but only ≈ 3 MB: k8s API (core/v1 is 1.9 MB of symbols), templ views (1 MB) and ui/static (4.9 MB) dominate. Main's `b77c30d2` grew both after the measurement (generated templ Go 1.53 → 1.71 MB of source, `ui/static` 553 → 825 KB, the shadcn-templ bundle 6,038 → 11,665 lines): add about 0.5 MB. |
| cmd/agent | — | ≈ 74 MB (1520 pkgs), plus cgo (crawshaw sqlite, C++ go-libutp) | about today's media-image clustarr; `libffshim.so`, `libpar2shim.so` and ORT are separate `.so` files |
| cmd/markers | segmentarr-worker: 30.6 MB, 520 pkgs (4.8 MB is textdet's embedded ONNX model) | ≈ 31 MB (537 pkgs); about 5 MB less once §7.2.9 drops the API types | ffgo adds ≈ 0.15 MB of symbols |
| cmd/transcode | squasharr-worker: 25.7 MB, 535 pkgs | ≈ 26 MB (540 pkgs) | |

**Image level (R1).** The `clustarr` image carries manager plus ui, ≈ 108 MB of
Go, against one 94 MB binary today: each binary carries its own client-go, k8s
API, otel and grpc (k8s API plus apimachinery alone are 11.8 MB of symbols in
today's binary). The native image carries agent + markers + transcode, ≈ 131 MB
of Go; transcode pool pods pull about 105 MB more Go code than today's 25.7 MB
worker. The media image (95.6 MB + ≈ 31 MB) is retired. R6 makes each binary
smaller and the images bigger (§14).

---
## 5. Writers, caches, leases and the indexarr split

"Process" means `manager`; one `agent --domain <d>` per domain (`catalog`,
`events`, `metadata`, `import`, `index`, `caption`, `torrent-engine`,
`usenet-engine`); `markers`; `transcode`; or `ui`. No process co-hosts two
domains (§3.5.1), so registrar double-registration cannot happen; `k8s.Checks`
still rejects a duplicate check name within one process (§3.3).

### 5.1 Process map today

| Deployment today (config/manager) | Roles | Field managers written in that process |
| --- | --- | --- |
| catalogarr | `controller,worker,history,artwork --leader-elect` (catalogarr.yaml:72-79) | catalogarr, catalogarr-series, catalogarr-fanout, catalogarr-classify, catalogarr-worker, catalogarr-grab, catalogarr-artwork, clustarr-dlq-projector |
| catalogarr-metadata | `metadata`, Recreate (catalogarr-metadata.yaml:40-42,66) | catalogarr-metadata, catalogarr-markers |
| importarr | `controller --leader-elect` (importarr.yaml:70-77) | importarr, importarr-worker (rename controller) |
| importarr-worker | `worker`, 2 replicas (importarr-worker.yaml:34,60) | importarr, importarr-worker |
| indexarr | `all`, Recreate (indexarr.yaml:44-46,72) | indexarr, indexarr-worker |
| grabarr / engine pods | `controller` / `torrent-engine`, `usenet-engine` | grabarr / grabarr-engine |
| squasharr | `controller` | squasharr, squasharr-pool |
| captionarr / captionarr-worker | `controller` / `worker` (2 replicas) | captionarr / captionarr-worker |
| ui | n/a | clustarr-ui |

Every process also records untagged writes under the user-agent-derived manager
`clustarr`, the basename of `os.Args[0]` (client-go rest/config.go:533-540;
controller-runtime pkg/client/client.go:170-171).

### 5.2 Every field manager, every write site, and where it runs after the split

21 constants (pkg/k8s/fieldmanager.go:38-309, listed by `FieldManagers()` at
:313-336). `PatchStatus` and `Apply` refuse any other name (`fm.Validate()`,
pkg/k8s/patch.go:77,110) and always force ownership (patch.go:88,121).

| Manager | Write sites (path:line) | After the split |
| --- | --- | --- |
| `catalogarr` | Item status: movie/reconciler.go:452,482,649; series :335,363,463; episode :578; artist :273,293,334; album :472,491,506,625; author :283,303,341; book :474,495,590; audiobook :440,461,562; comic :281,301,351; issue :439 (all app/catalog/controller). MediaFile main-resource Apply mediafile/mediafile_controller.go:346, status :667. Search status search/reconciler.go:351, child Search Apply search/container.go:99, Download Apply search/reconciler.go:834. Status of RootFolder rootfolder/controller.go:133, QualityProfile qualityprofile/controller.go:103, DelayProfile delayprofile/controller.go:79, MetadataProvider metadataprovider/controller.go:125, OverlayProfile overlayprofile/controller.go:173 | manager |
| `catalogarr-series` | Episode spec.monitored merge patch series/reconciler.go:583; Episode status :784 | manager |
| `catalogarr-fanout` | Issue status comic/reconciler.go:499 | manager |
| `catalogarr-classify` | Series merge patch series/reconciler.go:383-384 | manager |
| `catalogarr-worker` | Search status app/catalog/worker/search/worker.go:774 | agent `catalog` |
| `catalogarr-grab` | Item status app/catalog/worker/grab/kindops.go:180,241 and nonvideo.go:105,167,219,275; Download Apply perform.go:296. Called from the grab handler and search worker (catalog) and from the RSS matcher through `grab.Decide` (rssmatcher/handler.go:315, decide.go:98) | agent `catalog` **and** agent `events` |
| `catalogarr-metadata` | `artwork.Pass` app/catalog/metadata/artwork/handler.go:205, from the gateway (metadata/worker.go:333) and the artwork-fetch consumer (catalog/run.go:837) | agent `metadata` |
| `catalogarr-artwork` | `PatchOverlay` app/catalog/status/artwork.go:174, from worker/artwork/handler.go:512 | agent `catalog` |
| `catalogarr-markers` | markers/handler.go:289 and segmenting/setup.go:54, both through `segmenting.Applier.ApplyMerged` (segmenting/apply.go:80-109) | agent `metadata` |
| `importarr` | **Manager:** rootfolderschedule_controller.go:259,265; importlist/controller.go:427 plus the Trakt token Secret (controller.go:279 → worker/importlist/tokenstore.go:136); libraryscan_controller.go:477; importexclusion_controller.go:246,269; librarydelete/reconciler.go:314. **Agent import:** Download.status.import fileimport/worker.go:499; observed-fingerprint annotation rescan/mediafile.go:393; Trakt token refresh importlist/sync.go:89 → tokenstore.go:136 | manager + agent `import` (two processes today too) |
| `importarr-worker` | **Agent import:** rescan/mediafile.go:449,661; rescan/series.go:279; fileimport/worker.go:406; importlist/catalogitem.go:187,231; importlist/worker.go:219. **Manager:** rename/controller.go:129 → rescan/rename.go:265 → `applySpec` mediafile.go:661 (now `mediafilespec.Apply`) | manager + agent `import` (two processes today too) |
| `indexarr` | **Manager:** Indexer status controller/indexer/controller.go:613; IndexerDefinition status indexerdefinition/controller.go:138; IndexerProxy status indexerproxy/controller.go:227; bundle Apply bundle/loader.go:127; session Secret Save session.go:164 (from controller_definition.go:139). **Agent today:** relogin Save/Drop session.go:164,201 via clientcache.go:296,303; facade key Create facadekey.go:108 | manager only, after §5.3.2 |
| `indexarr-worker` | search/fanout.go:658; worker/rss/worker.go:332 (both `idxstatus.PatchCAS`); after §5.3.2, session Save/Drop and the facade key from the agent | agent `index` |
| `grabarr` | downloadclient/controller.go:243,275-276,302,310,325,340; download/controller.go:299,316,445 | manager |
| `grabarr-engine` | engine/torrent/reconciler.go:483; engine/usenet/engine.go:338 | agent `torrent-engine` / `usenet-engine` |
| `squasharr` | transcodeprofile/controller.go:277,551; transcodejob/write.go:60, shared by the reconciler and `ResultsConsumer` (results.go:42-66) | manager |
| `squasharr-pool` | transcodejob/pools.go:481 | manager |
| `captionarr` | subtitlerequest/reconciler.go:646,706; subtitleprovider/controller.go:218; subtitleprofile/controller.go:248,338 | manager |
| `captionarr-worker` | worker/fetch/apply.go:301 (`PatchStatusCAS`) | agent `caption` |
| `clustarr-dlq-projector` | history/dlq.go:280-281 (now `app/catalog/worker/history`) | agent `events` |
| `clustarr-ui` | ui/actions: actions.go:199,265,308,360; add.go:80; config.go:204,263,342,354; settings.go:85; refresh.go:90; delete.go:77,124; season.go:98; manualassign.go:173 | ui |
| `clustarr` (no FieldOwner; user agent) | **Manager:** Creates at series/reconciler.go:716, artist :437, author :441, comic :472, librarydelete/reconciler.go:295, qualityprofile/bootstrap.go:89,96,99, metadataprovider/defaults.go:83, subtitleprofile/defaults.go:66; JSON patches at metadata/refresher.go:228 and history/replay.go:302; finalizer Updates pkg/k8s/finalizers.go:100,128. **Engines:** finalizer Updates | manager + engines; name pinned by §5.3.5 |
| **New:** `clustarr-autoscale` (`k8s.ManagerAutoscale`, R5) | HPAs, the `v1beta1.external.metrics.k8s.io` APIService `spec.caBundle` (§9.4) | manager |

`markers` and `transcode` write no Kubernetes objects, only KV and object-store
entries (cmd/segmentarr-worker/main.go:112, cmd/squasharr-worker/main.go:107,
app/squash/worker/lease.go:58,84,159,229). The redownload handler (now in
`events`) writes no Kubernetes object: it frees grab leases in KV and publishes
searches. The CertManager's Secret writes are create-only and CAS `Update`s,
not SSA, so they carry no field manager beyond the default owner.

### 5.3 Rulings for managers whose process set changes

**5.3.1 `catalogarr-grab`: one process becomes two (`catalog`, `events`). Safe;
no change.** Item writes go only through `updateWorkerStatus`, which reads, then
applies with `WithResourceVersion` under `RetryOnConflict` (kindops.go:288-325,
180-181; its doc already says "safe across replicas", :298-305). Downloads use a
deterministic name, a `clustarr-leases` KV lease (lease.go:126,153,223) and a
live-read duplicate guard (perform.go:178,400). The field-manager guard (§5.15)
allowlists this manager for exactly those two domains.

**5.3.2 `indexarr`: one process (`indexarr --role all`) becomes manager plus
agent `index`, the R7 case. `indexarr` becomes manager-only; the agent's writes
move to the existing `indexarr-worker`.** Three agent-side write paths change:

- **Relogin Save.** `clients.SessionStore` gains a `Manager k8s.FieldManager`
  field in place of the hard-coded name (session.go:164,201); the agent
  constructs it with `k8s.ManagerIndexarrWorker`. Both managers apply the same
  complete declaration from the one `sessionSecretAC` (session.go:212): owner
  reference, type, `data.session` and `data.cookie`, so neither can release a
  field. This is the only new deliberate co-ownership.
- **Drop becomes a compare-and-swap**, `Drop(ctx, idx, failed *cardigann.Session)`:
  1. Live-read the Secret (Secrets are never cached, §5.6).
  2. Return without writing unless `data.session` equals `MarshalSession(failed)`.
  3. Apply the emptied declaration `WithResourceVersion(read.ResourceVersion)`;
     on Conflict, redo from step 1.
  4. In KV, `Get(SessionKey(uid))`, compare the value, then
     `DeleteRevision(key, entry.Revision)` (pkg/events/bus.go:299);
     `ErrRevisionMismatch` means the manager saved a newer session, so skip.

  Today Drop is unconditional (session.go:186-205), so a failed agent relogin
  can wipe the session the manager just saved.
- **Facade key.** The Create (facadekey.go:108, moving to `app/indexer/agent`)
  uses `k8s.ManagerIndexarrWorker`. It is create-only and AlreadyExists counts
  as success (facadekey.go:98-121), so the owner rename touches no live field.

No new constant, no `FieldManagers()` change, no spec §2 name change: §2 and
fieldmanager.go:222-239 only list the session and facade-key Secrets under
`indexarr-worker` (§13 OD14).

**5.3.3 Managers already split across two processes: unchanged (R7).**
`importarr` is written by the importarr and importarr-worker Deployments
today (tokenstore.go:72-80 documents the two-process Trakt Secret), and
`importarr-worker` by the rename controller and the workers. The split moves
the controller half into the manager and the worker half into agent `import`,
so the two-process shape is identical. Pre-existing lost-update risks (§5.5)
stay; fixing them is §13 OD13.

**5.3.4 Same domain, more replicas (HPA):**

- **`catalogarr-artwork`.** `status.overlay` is one value, and `record` re-reads
  and re-checks every input before applying (worker/artwork/handler.go:493-513).
  `PatchOverlay` (status/artwork.go:151-176) gains `WithResourceVersion(fresh)`
  and a retry on Conflict, closing the window between recheck and apply now that
  more than one replica is real.
- **`catalogarr-worker`, `clustarr-dlq-projector`.** Each message goes to one
  consumer; the projector's patch is idempotent.
- **`catalogarr-metadata`.** `artwork.Pass` relies on an in-process lock
  (fetcher.go:216-241) and applies with no resourceVersion (handler.go:205). So
  `metadata` stays at exactly 1 replica with `strategy: Recreate`
  (catalogarr-metadata.yaml:40-42) and may never scale until `Pass` becomes
  `k8s.PatchStatusCAS`.

**5.3.5 The default user-agent manager.** In binaries named `manager` and
`agent`, untagged writes would record `manager` and `agent`, leaving stale
entries and an unreadable audit. `pkg/k8s.Options.ManagerOptions` sets
`opts.Client.FieldOwner = k8s.DefaultFieldOwner`, a new untyped const
`"clustarr"`; controller-runtime then wraps the client with
`client.WithFieldOwner` (pkg/client/client.go:154-156). An explicit option still
wins (fieldowner.go:27-31), so every `PatchStatus`, `Apply` and
`client.FieldOwner(...)` site keeps its own name. `DefaultFieldOwner` is not a
`FieldManager`, so `Validate` never accepts it for SSA (§13 OD4).

**5.3.6 New name.** `ManagerAutoscale FieldManager = "clustarr-autoscale"` joins
`FieldManagers()`, its doc and spec §2, written only from the manager.

### 5.4 Status-write invariant and forced ownership

- **All status writes still go through `pkg/k8s.PatchStatus`/`PatchStatusCAS`.**
  The forbidigo rule (.golangci.yml:19-34) covers every path except `pkg/k8s`, so
  the new packages are covered without a config change.
  `TestForbidigoRejectsStatusWrites` (cmd/clustarr/lint_guard_test.go:61) moves
  to `test/guards`.
- **No new partial applies.** Each moved writer keeps its one renderer:
  `idxstatus.ControllerFields`/`WorkerFields`, `grabstatus.ControllerFields`/`EngineFields`,
  `squasharrstatus.PatchCAS`, `captionstatus.*Fields`, `sessionSecretAC`.
- **Forced ownership** (patch.go:88,121) keeps double claims silent. That is why
  §5.15 asserts writer homes statically and the session-Secret test asserts
  `metadata.managedFields` by manager name, not values.

**R3 probe results: one MediaFileStatus write path, not two.** A results
consumer that applied `status.mediaInfo` under `catalogarr` would be a second
write path without compare-and-swap (the status apply carries no
resourceVersion, mediafile_controller.go:664-669), and the reconciler's next
apply from a lagging cache would revert it (CLAUDE.md, "Two write paths under
one manager need compare-and-swap"). So:

- The import domain's probe worker writes only the Durable KV bucket
  `clustarr-probes`, keyed by MediaFile UID (§6.5).
- The manager's leader-only piece is `probeRecordsSource`, a KV watch registered
  on the MediaFile controller with `WatchesRawSource`. It writes nothing to
  Kubernetes; it only enqueues.
- `Reconcile` replaces the `r.Probe(ctx, path)` seam (mediafile_controller.go:165,278)
  with a record read and, on a miss, a published probe task and
  `Probed=False/ProbePending`. `applyStatus` stays the only status write.

The probe leaves `Reconcile`, so the stale-read window before the spec-takeover
Apply (:346) shrinks to ordinary cache lag.

### 5.5 Lost updates (read, slow work, apply) across the new boundary

| Path | Crosses a process boundary? | Verdict |
| --- | --- | --- |
| Indexer reconciler login, then status apply | manager writer vs agent `indexarr-worker` writes | Safe: re-reads worker fields after the login (controller_definition.go:157) |
| Session Save/Drop | yes (new) | fixed by the Drop CAS (§5.3.2) |
| MediaFile probe | yes (new, R3) | fixed by the single write path (§5.4) |
| `segmenting.Applier` (TheIntroDB and segment results) | no (both in `metadata`) | CAS (apply.go:96-107) |
| Grab path | catalog + events | CAS (§5.3.1) |
| Caption fetch, RSS, fan-out, squasharr | no | `PatchStatusCAS` / `idxstatus.PatchCAS` / `writeStatus` already |
| Trakt token `applyAll` | same two processes as today | pre-existing: live Get, then apply with no resourceVersion (tokenstore.go:114-139); §13 OD13 |
| `fileimport.applyMediaFile` | same as today | pre-existing: no resourceVersion (fileimport/worker.go:406); §13 OD13 |
| Import list `applySeries` vs `catalogarr-classify` | same as today | pre-existing (catalogitem.go:221-231 vs series/reconciler.go:383); §13 OD13 |

### 5.6 The one merged manager cache

`internal/cli/manager/options.go` renders the manager's options from
`k8s.Options` plus each `app/<svc>/manager.Cache(o)`:

```go
opts := o.Options.ManagerOptions("manager.clustarr.io", o.LeaderElect) // --leader-elect defaults true here
opts.Client.FieldOwner = k8s.DefaultFieldOwner                          // "clustarr", §5.3.5
opts.Client.Cache = &client.CacheOptions{
    DisableFor: []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}},
}
opts.Cache.ByObject = map[client.Object]cache.ByObject{
    &batchv1.Job{}: transcodejob.PoolJobCache(o.Namespace), // label managed-by=squasharr, own namespace
}
if o.LegacyLeaseCheck {
    opts.LeaderElectionResourceLockInterface = k8s.NewGatedLeaseLock(...) // §5.8
}
// DefaultTransform stays cache.TransformStripManagedFields() (pkg/k8s/manager.go:227-233).
// No MediaFile ByObject: importarr's withoutMediaInfo (app/import/run.go:261-288) is deleted.
```

- **The MediaFile strip goes.** `withoutMediaInfo` is deleted, because the
  manager's reconcilers read `status.mediaInfo`: squasharr
  (transcodejob/plan.go:52,70, controller.go:507, metrics.go:101-106,
  transcodeprofile/profile.go:149), captionarr (subtitlerequest/reconciler.go:218,395-397),
  the movie and episode rollups and the segment planner (segmenting/plan.go:177-183).
  With the strip, squasharr would plan nothing and captionarr would skip every
  file, without an error.
- **`DisableFor` Secret and ConfigMap** is the union of importarr's (both,
  import/run.go:263) and indexarr's and grabarr's (Secret, indexer/run.go:333-337,
  grab/run.go:249-252). No manager code Lists or watches Secrets or ConfigMaps;
  every read is a `Get`, which goes live. `metadataprovider/registry.go:130`
  becomes a live GET, so the marker at metadataprovider/controller.go:145 narrows
  to `secrets get`. R5's read of `kube-system/extension-apiserver-authentication`
  stays a live `get`, with no cluster-wide ConfigMap informer. The gateway's
  `secrets get;list;watch` (gateway.go:50) is agent-side and leaves the manager role.
- **The Job `ByObject` stays.** Only app/squash reads Jobs; the namespace pin
  (`PoolJobCache(o.Namespace)`, pools.go:598-604) keeps pool Jobs in the
  manager's namespace, as today.
- **No `ByObject` on apps/v1.** DownloadClient owns StatefulSets and Deployments
  (downloadclient/controller.go:526-527) and the autoscale reconciler reads the
  agent Deployments; a grabarr-only selector would answer NotFound for them, the
  trap pools.go:590-597 documents.
- **Nodes.** One cluster-scoped informer, shared by `capacity.go:44` and the
  autoscale reconciler.
- **Metadata-only informers become typed.** The 8 `metadata-refresh-<kind>`
  controllers (refresher.go:102-114) and the 16 `replay-<kind>` controllers
  (replay.go:141-156) switch from `PartialObjectMetadata` to the typed object.
  The manager already holds a typed informer for all 16 kinds, and the cache
  keeps metadata and typed informers separately, so today they cost a second
  Episode watch of 15k objects. `replayKind.Reconcile` (replay.go:184-192) Gets
  the typed object and passes it as `client.Object`; the JSON patches are unchanged.
- **`ReaderFailOnMissingInformer` is not set:** the reconcilers lazily read many
  kinds (Nodes, QualityProfiles, RootFolders, …).
- **Namespaces.** `DefaultNamespaces` comes from `--watch-namespace` (manager.go:239-245).
- **Size estimate**, from live list sizes with managedFields stripped (Episode
  32.2 MB, MediaFile 46.0 MB including 13 MB of mediaInfo, Movie 7.6,
  IndexerDefinition 5.7, SubtitleRequest 3.7, others under 3): about 100 MB of
  JSON, roughly 130-200 MiB of heap. Today's six controller pods total about
  600 MiB.

### 5.7 Field indexes: the complete list, and why nothing collides

`IndexField` on an existing (GVK, name) is an "indexer conflict"
(itemindex.go:79-81), so names must be unique per GVK within one cache. Every
manager-side registration:

| GVK | Name → registrar |
| --- | --- |
| MediaFile | `.spec.mediaRef.movie` movie/reconciler.go:137 · `.spec.mediaRef.episode` episode/reconciler.go:140 · `.spec.mediaRef.album` album :160 · `.spec.mediaRef.book` book :141 · `.spec.mediaRef.audiobook` audiobook :132 · `.spec.mediaRef.issue` issue :123 · `mediafile.clustarr.io/owner` mediafile/watch.go:152 · `librarydelete.spec.mediaRef.target` librarydelete/kinds.go:40 · `captionarr.spec.mediaRef` caption/itemindex/itemindex.go:89 (deduplicated per FieldIndexer, :82-94; called from subtitlerequest/reconciler.go:788 and subtitleprofile/controller.go:451) |
| Download | `.spec.target.{movie,episode,album,book,audiobook,issue}` movie :147, episode :150, album :170, book :151, audiobook :142, issue :133 · `.spec.clientRef` grab/controller/download/controller.go:778 |
| Episode | `.spec.seriesRef` series/reconciler.go:153 · `episode.spec.seriesRef` episode :160 · `mediafile.clustarr.io/episode-series` watch.go:152 · `librarydelete.parent` kinds.go:71 |
| Movie | `.spec.qualityProfileRef` movie :157 · `mediafile.clustarr.io/movie-rootfolder` watch.go:152 |
| Series | `.spec.qualityProfileRef` episode :170 · `mediafile.clustarr.io/series-rootfolder` watch.go:152 |
| Album | `.spec.artistRef` artist :119 · `.spec.qualityProfileRef` album :180 · `librarydelete.parent` |
| Book | `.spec.authorRef` author :130 · `.spec.qualityProfileRef` book :161 · `librarydelete.parent` |
| Audiobook | `.spec.qualityProfileRef` audiobook :152 · `.spec.bookRef` :162 |
| Issue | `.spec.comicRef` comic :126 · `librarydelete.parent` |
| TranscodeJob | `spec.mediaFileRef` mediafile_controller.go:720 · `squasharr.transcodejob.spec.mediaFileRef` transcodejob/controller.go:912 · `squasharr.transcodejob.spec.profileRef` :921 |
| SubtitleRequest | `spec.mediaFileRef` mediafile_controller.go:723 |

The importarr names were chosen not to collide with catalog's (kinds.go:30-35);
squasharr and captionarr names are prefixed. Names that repeat
(`.spec.qualityProfileRef`, `librarydelete.parent`) do so on different GVKs.

- **Manager consumers of these indexes** stay with their registrars: the segment
  planner (catalog/run.go:457-467) on `series.EpisodeBySeriesRefIndex` and
  `episode.MediaFileByEpisodeIndex` (segmenting/plan.go:150,162); the search
  controller on `.spec.artistRef`, `.spec.authorRef` and `.spec.comicRef`.
- **Agent caches** (per process): `catalog` declares Download
  `search.clustarr.io/download-target` (search/blocklist.go:58, read at
  snapshot.go:287); `events` declares the same index (read at
  rssmatcher/resolve.go:249) plus the 13 `rssmatcher.clustarr.io/*` indexes
  (rssmatcher/index.go:94-149); `import` declares MediaFile `spec.path`
  (rescan/index.go:40) and `.spec.mediaRef.target` (fileimport/index.go:45);
  `caption`, `metadata`, `index` and the engines declare none.
- **Registrar changes.** `registerWorkerIndexes`/`assertWorkerIndexes`
  (catalog/wiring.go:95-192) split into a catalog set (1 index) and an events set
  (14 indexes), each declared as `[]k8s.FieldIndex` by its domain package and
  registered and asserted once by the agent (§3.5.2). The ui's own cache keeps `spec.seriesRef`
  (ui/reader.go:154,173).

### 5.8 The lease

- **One lease**, `manager.clustarr.io`, in `--leader-election-namespace` or
  `POD_NAMESPACE`, with the existing 60 s / 45 s / 5 s timings
  (pkg/k8s/manager.go:188-202) and `LeaderElectionReleaseOnCancel`. It replaces
  `<svc>.clustarr.io` (catalog/run.go:85, import/run.go:71, indexer/run.go:67,
  grab/run.go:65, squash/run.go:57, caption/run.go:54).
- **Manager Deployment:** `replicas: 1`, RollingUpdate with `maxSurge 1` and
  `maxUnavailable 0`. The new pod waits for the lease, which the old pod
  releases on cancel. Its `EveryReplica` runnables tolerate two pods during a
  rollout: the segment planner is a work-queue consumer, and the metrics server
  and health endpoints are stateless.
- **Legacy-lease gate.** `pkg/k8s.GatedLeaseLock`, passed as
  `ctrl.Options.LeaderElectionResourceLockInterface` (controller-runtime
  pkg/manager/manager.go:214-219,407-408), wraps the `manager.clustarr.io`
  `LeaseLock`:
  - `Create` and `Update` return an error while any of `catalogarr.clustarr.io`,
    `importarr.clustarr.io`, `indexarr.clustarr.io`, `grabarr.clustarr.io`,
    `squasharr.clustarr.io` or `captionarr.clustarr.io` has a non-empty
    `holderIdentity` and `renewTime + leaseDurationSeconds` in the future;
  - leader election retries every `RetryPeriod`;
  - once open, the gate stays open (`atomic.Bool`), so renewals cost no extra GET.

  This stops a new manager reconciling beside a still-terminating per-service
  controller under the same field managers. It needs only `get` on leases,
  which the leader-election Role grants
  (config/rbac/leader_election_role.yaml:13-24). `--legacy-lease-check` (default
  true) turns it on; the flag and the gate are deleted one release after the
  cutover (§13 OD6).
- **Agents never elect** (`AgentManagerOptions`, §3.5.2). No agent, not even
  `index`, holds a lease; `indexarr.clustarr.io` disappears.
- **RBAC.** Only the manager ServiceAccount is bound to the leader-election
  Role; the nine bindings in config/rbac/leader_election_role_binding.yaml
  collapse to one. `grabarr-engine` stays unbound (grabarr_engine_role.yaml has
  no coordination rule).

### 5.9 EnsureTopology ownership

- **Only `cmd/manager` calls `busconn.EnsureTopology`**: once before
  `mgr.Start`, where a failure stops startup, and afterwards only through
  `busconn.KeepTopology` (below). Today all six services call it (e.g.
  indexer/run.go:371), and bus.go:124-127's doc says "every replica of every
  service"; that doc is rewritten.
- **Agents, ui, markers and transcode never ensure.** That ends an older agent
  reverting a newer topology during a mixed rollout (topology_nats.go:30-57 is
  CreateOrUpdate). Agents wait for it at start (`AwaitTopology`, §3.5.2).
- **`natsbus.Subscribe` binds and never creates.** It replaces
  `js.CreateOrUpdateConsumer` (natsbus.go:306-325) with
  `js.Consumer(ctx, stream, durable)`, in every process, the manager included.
  A missing durable is waited for: it polls every 5 s and logs WARN once a
  minute until ctx ends, so a subscriber that starts before the manager idles
  instead of crash-looping. The same wait re-binds a running subscription
  whose durable or stream vanished (a `Fetch` returning
  `jetstream.ErrConsumerNotFound` or `ErrStreamNotFound`, as after a NATS
  restart wipes a memory-backed stream): the pull loop stops fetching, keeps
  running handlers, and resumes once `js.Consumer` succeeds again.
  `sub.MaxInFlight` is only the per-pod slot count; the topology owns
  `MaxAckPending` (R5). Every `Subscribe` call site uses a static topology
  durable (catalog/run.go:837, import/run.go:488,515,539,
  worker/rss/worker.go:197, segmenting/setup.go:72, gateway.go:125,
  markers/setup.go:46, sink.go:87, dlq.go:140, worker/artwork/handler.go:215,
  search/worker.go:957, grab/handler.go:124, rssmatcher/handler.go:168,
  redownload/handler.go:195, fetch/worker.go:165, transcodejob/results.go:55,
  cmd/segmentarr-worker/main.go:134).
- **The dead-letter watchers become topology objects.** Today `Subscribe` and
  `Pull` each create their durable's MAX_DELIVERIES watcher with
  `CreateOrUpdateConsumer` on `CLUSTARR_ADVISORIES` (natsbus.go:331 calls
  `watchMaxDeliveries`, deadletter.go:113-127; pull.go:57), and none is in
  `events.Default()`. So:
  - `events.Default()` derives, for every static consumer, one
    `ConsumerSpec{Name: "clustarr-dlq-watch-" + stream + "-" + durable, Stream:
    StreamAdvisories, Filters: [MaxDeliveriesAdvisorySubject(stream, durable)],
    AckWait: 80s, MaxDeliver: -1 (unlimited), MaxAckPending: 4, Slots: 4}`,
    today's deadletter.go:44-57 settings, through a new
    `events.DeadLetterWatcherSpec(c ConsumerSpec) ConsumerSpec` (natsbus's
    unexported `dlqWatchName` moves to `pkg/events` as
    `events.DeadLetterWatcherName(stream, durable)`). `Topology.Validate` and
    `ConsumerConfig` accept `MaxDeliver: -1` for these specs only (a spec whose
    stream is `StreamAdvisories`).
  - The manager's `EnsureTopology` creates them; `StreamAdmin.Missing` and
    `AwaitTopology` check them.
  - `Subscribe` binds its durable's watcher exactly as it binds the work durable
    (`js.Consumer`, waiting while missing) and runs it as today
    (`deadLetterLapsed`, the `watchRetry` schedule). `Pull` keeps creating its
    own watcher, because pool durables are dynamic, and the withdrawal path
    keeps deleting both (`DeleteSubscription`, natsbus/admin.go:51).
  - **The manager is a backstop watcher.** A new optional interface
    `events.DeadLetterWatcher{ WatchDeadLetters(ctx, sub Subscription) (stop func(), err error) }`,
    implemented by natsbus (bind the watcher, run `deadLetterLapsed`) and by
    membus (a no-op: membus dead-letters a lapsed delivery in the subscribing
    bus's sweep). `busconn.WatchDeadLetters(ctx, bus, t)` calls it for every
    static consumer of `t` and blocks until ctx ends; `internal/cli/manager`
    adds it as `k8s.LeaderOnly`. The watcher durable is shared, so whichever of
    the manager and the domain's pods pulls an advisory first copies it, and
    the copy's Msg-Id deduplicates a double handling (deadletter.go:40-43).
    Why it is needed: a lapsed final delivery stays in the consumer's pending
    set, so `NumAckPending` (and `clustarr_consumer_lag`) holds the domain at
    one or more replicas until the server raises the advisory on its next pull
    (nats-server `getNextMsg`, consumer.go:4976-4993); but a pull request a
    draining pod left behind can raise it after the pod is gone
    (`WorkQueueLapseWhileUnwatchedToDLQ`, contracttest.go), and in a domain at
    zero nothing else would consume it until the next wake.
- **`natsbus.Pull` keeps `CreateOrUpdateConsumer`** (pull.go:45-58), because
  transcode pool durables are dynamic per profile and class and deleted by the
  manager's withdrawal path (transcodejob/withdraw.go:238-293). It writes
  `MaxAckPending: cmp.Or(sub.MaxAckPending, max(sub.MaxInFlight, 1))` (§9.3).
- **The manager keeps the topology, not only creates it.** On single-node
  NATS every non-`Durable` stream and KV bucket is memory-backed
  (`ForSingleNode`, `pkg/events/topology.go:249-283`), so a NATS pod restart
  deletes `CLUSTARR_EVENTS`, `CLUSTARR_RELEASES`, every `CLUSTARR_WORK_*` but
  segmentarr and probe, `CLUSTARR_DLQ`, `CLUSTARR_ADVISORIES` and their
  consumers. Today any service restart re-ensures; after the split only the
  manager may. `busconn.KeepTopology(ctx, nc, bus, t)`, added by
  `internal/cli/manager` as `k8s.LeaderOnly`, calls `EnsureTopology` once when
  the lease is acquired (so a new manager that ensured before winning the lease
  converges any change an older leader made meanwhile), then on every NATS
  reconnect (a `nats.ReconnectHandler` it chains onto the connection) and every
  60 s while `StreamAdmin.Missing(ctx, t)` is non-empty. Subscriptions re-bind
  on their own (above), and extmetrics answers 200 again once the durables
  exist (§9.4).
- **membus** mirrors the binding: `Ensure` binds every topology consumer with
  its filters and cap, and `Subscribe` on an unbound durable waits (§9.3).
- **KV and object stores** already resolve lookup-only (natsbus/kv.go:53,
  objectstore.go:60).

### 5.10 Readiness check names

§3.3. `pkg/k8s.AddProbes` is the only registrar and rejects duplicates, because
controller-runtime overwrites silently (internal.go:235).

### 5.11 Tracing, NATS names, process-once setup

- **One `obs.Bootstrap` per process**, called by the command's `Run`, never by a
  registration function. Today every `app/<svc>.Run` calls it and the first
  caller wins (tracing.go:141-159). Names are §3.1.
- **Envelope `Source` strings are unchanged** (they name the publishing
  component; for example `squasharr-controller@` at transcodejob/dispatch.go:212,
  `importarr@` at importlist/controller.go:316).
- **`metrics.Register` and `fsops.ApplyUmaskFromEnv` run once** in each of
  manager and agent (today cmd/clustarr/root.go:43-52,90);
  `k8s.RegisterRESTClientMetrics` is already `sync.Once` (manager.go:289-304).
- **e2e scenario 1** (test/e2e/transcode_test.go:945-946) compares the manager
  Deployment (the Download controller publishes the ImportTask) with the
  import-agent Deployment. It must read logs by label across all pods, because
  helpers_test.go:1598-1599 reads one pod and the import agent can run more than
  one replica.

### 5.12 The indexarr split (R8)

| Piece | Today (one process) | After |
| --- | --- | --- |
| Indexer reconciler: caps probe, login, session Save, RSS chain seed, limits-ring watch | indexer/run.go:595-610 | manager, own limiter `ratelimit.New(defaultLimiterConfig())`, no ClientCache |
| IndexerDefinition and IndexerProxy reconcilers | run.go:613-639 | manager |
| `DirectGrabReconciler` (KV grab-ring CAS) | run.go:617-619; download/directgrab.go:147 | manager, as `app/indexer/controller/directgrab`, so the manager does not link the download verb |
| Cardigann bundle loader | `EveryReplica` (run.go:663) | manager, **leader-only** |
| relindex store (SQLite/Postgres), readiness `index.releaseindex` | run.go:393-413 | agent `index` only |
| `search.Serve` (rpc.indexarr.search/download/query), RSS consumer, facade | run.go:697-849 | agent `index`, one process: the facade reuses the `search.Service` whose `srvCtx` and `inflight` `Serve` sets (search/service.go:118-149) |
| `indexSweeper` | `NeedLeaderElection` true (run.go:891) | agent `index`, as `k8s.EveryReplica`; a racing prune is "wasted work, not corruption" (run.go:864-868), and the domain is fixed at 1 |
| `ClientCache`, `SessionStore`, wire-client builders | app/indexer/controller/indexer | `app/indexer/clientcache` (cache) and `app/indexer/clients` (store, builders), §4.3 X2/X3 |

**`app/indexer/rssschedule`** receives `NextPollAt`, `ScheduleNext`,
`TaskMsgID` and `rssInterval` (worker/rss/worker.go:832-930). The reconciler's
only use of `rss` is controller.go:478-482, and that import pulls in
`pkg/relindex`, `modernc.org/sqlite` and `jackc/pgx` today. `bundle` stays at
`app/indexer/bundle`, which links only `pkg/cardigann`.

**Limiter (generation-gated, R8 amendment).**

- The agent builds `ratelimit.New(defaultLimiterConfig())` (2 s default,
  indexer/run.go:534-536) and its own `clientcache.ClientCache`.
- `buildWireClientFor` calls `clients.ApplyRateLimit(idx.Spec, def, lim,
  clients.DefinitionDelay(def.RequestDelay))`, keeping the definition floor and
  the explicit `0s` = unpaced semantics (source.go:247-258), **only when
  `idx.Generation >= cc.appliedGeneration[uid]`**, and `store`
  (clientcache.go:332) replaces an entry only from an equal-or-newer generation.
  Without the gate, an in-flight search holding a pre-edit `*Indexer` misses the
  RV-equality lookup (clientcache.go:315-330), rebuilds and reverts the edit.
  `SetConfig` adjusts a live bucket in place (pkg/ratelimit/limiter.go:121-135).
- **Manager-side traffic** paces on its own bucket: the caps probe (at most once
  per `capsTTL` of 12 h or per generation change, controller.go:65-68), logins
  (only when the session is missing or near expiry, `needsLogin` at
  controller_definition.go:173), and IndexerProxy probes (`http.DefaultClient`,
  run.go:625-631). Worst case one extra request inside the agent's
  `requestDelay` gap per 12 h per host, which R8 accepts. The manager pod needs
  egress to indexer hosts, proxies and FlareSolverr.

**ClientCache invalidation across processes (through the session KV).**

- The agent runs `clientcache.ClientCache.WatchSessions(ctx,
  bus.KV(events.BucketIndexerSessions))` as an `EveryReplica`: on any Put or
  Delete of `SessionKey(uid)` it `Forget`s the matching entry, replacing the
  reconciler's in-process `Forget` after login (controller_definition.go:148-151).
- The same runnable calls a new `ClientCache.Prune(now)` every
  `DefaultClientCacheTTL`, replacing the reconciler's delete-time `Forget`
  (controller.go:228), so a deleted Indexer's client and idle connections do not
  linger.
- The manager's reconciler gains `sessionsSource`, mirroring `limitsSource`
  (controller.go:669,685-690): it enqueues the Indexer when its session key is
  **deleted**, so an agent Drop gets a manager login at once rather than at the
  next 15-minute reprobe (controller.go:63).

**relindex.** The manager must not link it (§4.5.1). `--index-path` and
`--index-dsn` (`CLUSTARR_INDEX_DSN`) are index-agent flags. The index
Deployment is fixed at `replicas: 1` with **`strategy: Recreate` under both
engines**, until a shared KV limiter exists (R4 amendment, §13 OD15).

**Facade key.** Read or created once at agent start, before `mgr.Start`, under
`indexarr-worker` (§5.3.2). `--facade-bind-address` and `--facade-api-key-secret`
are index-agent flags.

**Bundle loader (leader-only).** `mgr.Add(k8s.LeaderOnly(loader.Run))`; a standby
manager no longer re-applies all 752 definitions (`SyncOnce`,
bundle/loader.go:74-131). `--cardigann-bundled` and `--cardigann-definitions-dir`
are manager flags; the embedded 1.5 MB `definitions.zip` is linked only by
cmd/manager; the index agent reads IndexerDefinitions from its cache
(cardigann.go:101,149).

**Tests that move or are rewritten.** `TestLeaderElectionIsGatedOnIndexDSN`
(app/indexer/run_test.go) is retired (no index lease). The two limiter tests
(app/indexer/wiring_envtest_test.go:509-546) become agent-only tests (§5.15).
`IndexarrWiringRegistersEveryComponent` splits into a manager half and an agent half.

### 5.13 Leader-only runnables in the manager (R9 plus existing)

| Runnable | Today | After | Needs |
| --- | --- | --- | --- |
| 16 `replay-<kind>` controllers | `setupHistory` on the worker side (catalog/run.go:632-639); unleased under `--role history` | manager, `app/catalog/history/replay`; shared `DLQReader`/`DLQReaderFor`/`Target`/`Resolve` stay in `app/catalog/history` (dlqstore.go, target.go); typed `For` (§5.6) | cached client; `events.Publisher`; JetStream handle via `DLQReaderFor` (dlqstore.go:66-72); RBAC replay.go:58-62 |
| `artwork.Reaper` | in `setupMetadataGateway`, non-electing (catalog/run.go:848-851) | manager, from `app/catalog/artwork`, so the manager does not link the gateway's `x/image/webp` decoders | `bus.ObjectStore(events.BucketArtwork)`; `mgr.GetAPIReader()` for metadata-only paged lists (reaper.go:58-80,199); list on the 8 kinds; the 30-minute grace (reaper.go:43-49) covers the gateway's Puts from another process |
| `bundle.Loader` | `EveryReplica` (indexer/run.go:663) | manager, `k8s.LeaderOnly` | cached client; embedded FS or the `/etc/clustarr/cardigann` mount |
| `qualityprofile.Bootstrap`, `metadataprovider.Bootstrap`, `wantedcron`, `subtitleprofile.Bootstrap` | controller roles | manager (unchanged) | `o.Namespace`, `o.WatchNamespaces`, bus publish |
| squasharr `ResultsConsumer` | leader-only (results.go:64-66) | manager (unchanged; shares `writeStatus` with the reconciler) | `squasharr-transcode-results` |
| `probeRecordsSource` (R3) | new | manager, as a raw source on the MediaFile controller (leader-only by being a controller source) | KV `clustarr-probes` |
| `recyclesweep.Scheduler` | new (replaces the agent timer) | manager, `k8s.LeaderOnly` | bus publish |
| `extmetrics.CertManager`, `extmetrics.QueueGauge` | new (R5) | manager, `k8s.LeaderOnly` | APIReader; `events.StreamAdmin` |
| `busconn.KeepTopology`, `busconn.WatchDeadLetters` | new (§5.9) | manager, `k8s.LeaderOnly` | the NATS connection; `events.StreamAdmin`; `events.DeadLetterWatcher` |
| Segment planner | `EveryReplica` in the controller role | manager (unchanged), because it needs the manager's Episode and MediaFile indexes | bus, cached client |

`indexSweeper` is the one leader-only runnable not moved: it needs the store
the manager must not link (§5.12). After these moves no agent holds a
`NeedLeaderElection()==true` runnable.

**Amended 2026-10-07 (NATS research; `nats-object-store.md` §6.6, §6.11).** The
`artwork.Reaper` row's "Needs" column gains, for its two new duties (the
orphan-chunk purge and the artwork audit, artwork design §B.5 as amended): the
manager's **cache** (the audit reads `status.artwork` of the eight kinds and
`status.overlay` of Movie and Series, synced before leader-only runnables
start); an `events.Publisher` (paced `ArtworkFetchTask` and `RenderOverlay`
repair tasks); `events.StreamAdmin.ConsumerState` (pacing against the lag of
`catalogarr-artwork-fetch` and `catalogarr-artwork-render`); and
`events.ObjectStoreAdmin` (`PurgeOrphanChunks` on `clustarr-artwork` and
`clustarr-fingerprints`). It still never `Put`s or calls `SetMeta`, decodes no
image (the audit compares digests), and keeps its metadata-only uncached
liveness lists. The manager links no new decoder.

### 5.14 What the manager mounts, and why

**`/data`, the RWX claim `clustarr-data`, read-write**, with the same identity as
today's controllers: uid/gid 1000, `fsGroup` 1000 with `OnRootMismatch`,
`UMASK=002`, `readOnlyRootFilesystem` with a `/tmp` emptyDir
(catalogarr.yaml:50-54,93,131,136). Every manager-side filesystem access:

1. MediaFile: `os.Stat` at mediafile_controller.go:252 and :552 (`swapTarget`);
   `ProbeHash` from the stat (probe.go:42-43). R3 moves the probe at :278 out;
   the stats stay.
2. RootFolder: `os.Stat` probe.go:33, `os.CreateTemp` :41, `os.Remove` :47,
   `fsops.DiskUsage` :51; `fsops.EnsureFreeSpace` controller.go:97.
3. Rename: rename/controller.go:129 → `mediafilespec.RenameFile` (was
   `rescan.RenameFile`): Stat/Lstat at rescan/rename.go:161,165,195;
   `fsops.MoveNoReplace` :208; Stat :259; move-back :271; sidecar moves :294.
   The field manager stays `importarr-worker`.
4. LibraryDelete: `os.Lstat` target.go:137 and reconciler.go:209;
   `fsops.SafeRemove` target.go:141; `fsops.PruneEmptyDirs` :161.
5. Download finalizer: `os.Lstat` and `fsops.SafeRemove` at
   grab/controller/download/controller.go:635-638. Without the mount it logs
   "already gone" and drops the finalizer, leaking the data silently.
6. DownloadClient `DiskSpaceOK`: `r.DiskUsage(r.DataDir)` at
   downloadclient/controller.go:204.
7. SubtitleRequest: `os.ReadDir` at caption/controller/subtitlerequest/reconciler.go:251.

`--data-dir` feeds grab's `DataDir`, caption's `DataDir`, the squasharr pool's
logical root (squash/run.go:427) and the engine render. squasharr's own
controllers touch no file.

**Read-only, optional:** `/etc/clustarr/cardigann`, from a ConfigMap or claim,
only when `--cardigann-definitions-dir` is set (bundle/loader.go:82; chart
deployments.yaml:95-110).

**Not mounted:** the index PVC (agent `index`), scratch volumes, and any ffgo,
ONNX or par2 library (R3).

### 5.15 Guards owned by this section (in `test/guards` unless noted)

- **`TestEveryFieldManagerHasItsHome`** (R7). For each `k8s.Manager*` constant
  (plus the aliases `rescan.FieldManager`, `fileimport.FieldManager`,
  `importlist.FieldManager`, `mediafilespec.FieldManager`, `fetch.FieldManager`,
  `catalogstatus.GatewayManager`, `RendererManager`, `ui/actions.FieldManager`):
  an AST scan finds every **write** argument (`PatchStatus`, `PatchStatusCAS`,
  `Apply`, `client.FieldOwner`, and the `status`-package `Patch*` wrappers),
  ignoring the declaration-only packages `pkg/k8s` and `app/*/status`. A write
  whose manager argument is not a constant (the session store's
  `s.Manager`, §5.3.2) is attributed to the package of each constructor call
  that supplies the constant (`clients.NewSessionStore(..., k8s.ManagerIndexarr)`
  in the manager, `k8s.ManagerIndexarrWorker` in `app/indexer/agent`).
  `go list -deps` from each **home root** decides which homes link that
  package. The roots are package-granular on purpose, which is why every agent
  domain has its own registration package (§4.2.1): `cmd/manager`, `cmd/ui`,
  `cmd/markers`, `cmd/transcode`, and the eight domain packages
  `app/catalog/agent/{catalog,events,metadata}`, `app/import/agent`,
  `app/indexer/agent`, `app/caption/agent`, `app/grab/agent/{torrent,usenet}`.
  The result must equal the §5.2 "After" column. Allowlisted multi-home names:
  `catalogarr-grab` (catalog, events), `importarr` and `importarr-worker`
  (manager, import), `grabarr-engine` (torrent-engine, usenet-engine). Any
  other name reachable from two roots fails. A root that links a writer it
  never calls (the over-approximation of a package-level graph) needs an
  explicit `linkedNotWritten` entry with a reason, the shape the RBAC linkage
  guard uses (§10.3.2). The runtime half is `starttest.AssertWroteOnlyAs`
  (§10.3.1).
- **`TestAgentsRegisterNoLeaderOnlyRunnable`** (`internal/cli/agent`, a
  package-internal test). Each agent domain's registration runs against a
  recording `ctrl.Manager`; every added runnable reports
  `NeedLeaderElection()==false`, and `AgentManagerOptions` sets
  `Controller.NeedLeaderElection=false`.
- **`TestManagerCacheOptions`** (`internal/cli/manager`, package-internal,
  because its subject `managerOptions` is unexported). `DisableFor` is exactly
  Secret and ConfigMap; `ByObject` holds only Job with the
  `managed-by=squasharr` selector; no MediaFile transform; `DefaultTransform`
  strips managedFields; `Client.FieldOwner == "clustarr"`. `cmd/manager`'s
  `TestManagerOptions` asserts only the flag-to-`Options` parse, not these.
- **`TestManagerFieldIndexes`** (`internal/cli/manager`, package-internal). A
  recording `FieldIndexer` over the full manager registration yields exactly the
  §5.7 (GVK, name) set.
- **`TestOnlyTheManagerEnsuresTopology`.** AST check that `EnsureTopology` is
  called only under `internal/cli/manager` and inside `pkg/busconn`'s own
  `KeepTopology`, and that `KeepTopology` and `WatchDeadLetters` are called only
  under `internal/cli/manager`; a natsbus/membus contract test proves
  `Subscribe` binds its durable and its dead-letter watcher and waits for
  either while missing, never creating one (§9.3).
- **`TestAddProbesRejectsDuplicates`** (pkg/k8s) and
  `TestOnlyPkgK8sRegistersProbes` (§3.3).
- **`TestSessionDropDoesNotWipeANewerSave`** (`app/indexer/clients`, envtest),
  two cases, each starting from a Secret the manager saved (S1) and asserting
  `metadata.managedFields` by manager name, since forced ownership
  (patch.go:88,121) makes values alone blind to an over-claim:
  - *Interleaved.* A real second writer runs the manager's Save of S2 between
    the agent's read and its Drop of S1. The Drop's CAS conflicts, it re-reads,
    sees S2 and writes nothing. Then `data.session` is S2; `indexarr` owns
    `f:data.f:session` and `f:data.f:cookie`; `indexarr-worker` owns nothing.
  - *Drop applies.* With no interleaving the Drop of S1 applies: `data.session`
    and `data.cookie` are empty, `indexarr-worker` owns `f:data.f:session` and
    `f:data.f:cookie` (and `f:metadata.f:ownerReferences` and `f:type`, sent in
    the same complete declaration); the next manager Save takes both data keys
    back, and `indexarr` owns them again.
- **`TestAgentClientCacheAppliesRateLimit`** (replaces
  wiring_envtest_test.go:509-546): an agent-only process leaves an explicit `0s`
  Indexer unpaced, honours a 5 s `requestDelay`, honours the definition floor,
  and does not revert a newer generation from a stale object.
- **`TestAgentForgetsClientOnSessionChange`** (KV Put, then rebuild),
  **`TestBundleLoaderIsLeaderOnly`**.
- **`TestGatedLeaseLockWaitsForLegacyLeases`** (pkg/k8s, envtest).
- **`TestControllerNamesAreUniqueAcrossTheBinary`**
  (cmd/clustarr/runnable_registration_test.go:702) moves and runs over the
  manager registration.
- **Amended 2026-10-07 (NATS research; `nats-object-store.md` §6.10, owner
  decision (a)).** Three object-store guards, each an AST scan of non-test files:
  - **`TestNoObjectLinks`**: no `AddLink` or `AddBucketLink` selector anywhere in
    the module. Links fire no watch event when their target changes, carry no
    size, digest or headers, dangle silently when the target goes, and nats.go
    refuses one over any name a regular object ever held, so the deterministic
    name stays the only lookup key (artwork design §B.2 as amended).
  - **`TestOnlyNatsbusTouchesJetStreamObjectStores`**: outside
    `pkg/events/natsbus` and `pkg/events/topology_nats.go`, no file that imports
    `github.com/nats-io/nats.go/jetstream` names a selector containing
    `ObjectStore` or `ObjectMeta`. (`app/catalog/history/dlqstore.go` imports
    jetstream for the DLQ stream reader and stays allowed: it names no object
    store. The research's version of this guard banned every jetstream import
    outside the bus packages and would fail on that file.)
  - **`TestArtworkWritersAreTheTwoVariantOwners`**: `events.BucketArtwork` is
    named only by `pkg/events`, the registration packages that bind the store
    (`app/catalog/manager`, `app/catalog/agent/catalog`,
    `app/catalog/agent/metadata`, `internal/cli/ui`); an object-store write
    (`Put` with four arguments, `SetMeta`) appears only in
    `app/catalog/metadata/artwork`, `app/catalog/worker/artwork`,
    `app/segments/worker` (its own fingerprint bucket) and the two buses; an
    object `Delete` in a file that names `events.ArtworkVariantOriginal` or
    `ArtworkVariantOverlay` appears only in those two writers and
    `app/catalog/artwork` (the reaper).
- **`TestHeartbeatsFitTheirDeadline`** (amended 2026-10-07, `nats-worker-pools.md`
  §2.4, S9): every handler heartbeat interval (each worker package's exported
  `HeartbeatInterval`) is at most a third of its durable's
  `events.AckDeadline(sub, 1)`, so two heartbeats can be lost before a lapse.

---
## 6. Probing without ffprobe

Every ffprobe run (pkg/mediainfo's go-ffprobe call, its exec'd first-frame
probe, and `ProbeAudio`) is replaced by one in-process ffgo probe,
`pkg/mediainfo/native`. The MediaFile reconciler's probe moves out of
`Reconcile` onto a queue the import domain answers (R3). Three things must not
change: `mediainfo.ProbeVersion` stays `3`; no stored `status.mediaInfo`
changes; the MediaFile reconciler stays the only writer of `MediaFileStatus`,
as `k8s.ManagerCatalogarr` (`catalogarr`).

### 6.1 What runs today (verified)

**`mediainfo.Probe`** (pkg/mediainfo/mediainfo.go:115-149) runs `ffprobe.ProbeURL`
(go-ffprobe v2.3.1, `-show_format -show_streams -show_chapters`), then
`runFrameProbe`, which execs `ffprobe -select_streams v:0 -show_frames -read_intervals %+#1`
(pkg/mediainfo/ffprobe.go:145-161). `toMediaInfo` (map.go:58-98) maps the result.
`ProbeAudio` (audio.go:62-76) is one more `ProbeURL`.

**Callers:**

- The MediaFile reconciler, inside `Reconcile` (mediafile_controller.go:277-278;
  `NewReconciler` sets `Probe: mediainfo.Probe` at :165), with
  `MaxConcurrentReconciles = 8` (:735). A failed probe requeues after 30 s,
  forever (:297).
- fileimport's `probeVideo` (fileimport/process.go:435-448, 15 s timeout), called
  at process.go:271 and episode_import.go:301.
- rescan's `probeVideo` (rescan/probe.go:48-53, 15 s timeout).
- `ProbeAudio` through `fileimport.FrozenFileQuality` (kinds.go:179-191; called at
  nonvideo.go:182,286 and rescan/nonvideo_scan.go:272), with no timeout.

**The transcode worker already probes through ffgo.** `inprocess.Engine.Probe`
(app/squash/worker/inprocess/probe.go:46-116) fills a `mediainfo.Raw` and maps it
with `mediainfo.FromRaw` (map.go:51). Its parity test,
`TestTheInProcessProbeAgreesWithFFprobe` (inprocess/probe_test.go:88-125), blanks
`VideoProfile` and every `Audio[].Profile` before comparing.

**ffgo versions.** The worktree pins ffgo `v0.0.0-clustarr.9` (go.mod:218, commit
02bfd1d); main pins `.11` (9f7a3a4). `.10` (6e3b3ca) adds only `NewMuxerToWriter`,
`Muxer.Flush` and `OutputTimeBase`, and `.11` only the purego bump, so the probe
surface of `.9` to `.11` is identical.

**How the divergences were measured:** on this host (FFmpeg n9.0.1 with the
shim), a scratch program ran `mediainfo.Probe` and `inprocess.Engine{}.Probe` on
crafted files; and on the live library, read-only (`kubectl get mediafiles`,
11,958 objects).

| ffgo .9-.11 vs ffprobe | Reproduced | Live impact |
|---|---|---|
| Codec `profile` is absent | every file | `videoProfile` set on 11,942 files. Audio profile strings: "Dolby Digital Plus + Dolby Atmos" ×582, "DTS-HD MA" ×207, "Dolby TrueHD + Dolby Atmos" ×4, "DTS-ES" ×1. Naming's `AudioCodecLabel` reads them: 573 names change, and 146 transcoded files would be renamed on disk under `renameTranscoded`. |
| An unspecified channel order is described as "2 channels", where ffprobe omits it | pcm in mkv: ffprobe `channelLayout:""`, ffgo `"2 channels"` | 249 audio streams on 128 mkv files (mostly eac3 2ch); 45 of them the lead track. Read by `ui/detail.go:261` and cluster-plex's `ma:audioChannelLayout`. Naming unaffected (`render.go:385-394` uses `channels`). |
| Chapter times computed as `pts*num*1000000/den` in int64 (ffgo `chapters.go:57-58`), which wraps for a Matroska 1/1e9 time past 9,223.372 s | chapters at 9,300,000 ms read 0/0 | 44 files' `chapterList`. Segment detection reads chapter times, and a "Credits" chapter outranks TheIntroDB. |
| `r_frame_rate` absent; avg used instead (`decoder.go:330`) | equal on the constant-frame-rate clips tested | fpsMilli is 50000 on 142 files, 59940 on 97, 60000 on 81; some may be field rates. |
| `bits_per_raw_sample` and `bits_per_sample` absent | – | a 24-bit FLAC or ALAC freezes at the 16-bit tier; no album files live |
| Stream colour tags, `level`, `field_order` and stream `duration` absent | – | read only by `IncompleteHDR` and `transcode.FromProbe` |

### 6.2 Packages after the change

**`pkg/mediainfo`** (pure: no os/exec, no ffgo, no pkg/obs) keeps `Raw`,
`DoviRecord`, `MasteringDisplay`, `ContentLight`; `FromRaw`, `FormatTag`,
`ProbeVersion`, `ClassifyHDR`, `IncompleteHDR`, `ErrIncompleteProbe`;
`AudioProbe`, `ErrNoAudioStream`, `ProbeHash`, `MovieHash`; all of classify.go
and codec.go. It loses `Probe`, `ProbeAudio`, `buildRaw`, `parseDoviRecord`,
`mergeFrame`, `runFrameProbe`, `frameProbe`, `frameProbeData`,
`toMasteringDisplay` and `toContentLight` (to `test/ffprobeoracle`), and
ffprobe.go is deleted. New:

```go
// prober.go
type Prober interface {
    Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *Raw, error)
    ProbeAudio(ctx context.Context, path string) (AudioProbe, error)
}
func AudioProbeFromRaw(raw *Raw) (AudioProbe, error)              // today's audioProbeFrom (audio.go:79-94), taking a Raw
func AtPath(mi *commonv1.MediaInfo, path string) *commonv1.MediaInfo // a copy with Container re-derived from path (map.go:60); the import seed uses it (§6.6)
```

`Raw` keeps go-ffprobe's types only (`*ffprobe.Format`, `[]*ffprobe.Stream`,
`[]*ffprobe.Chapter`): `transcode.FromProbe` and every Raw test read them,
go-ffprobe is one stdlib-only package, and the exec guard bans its entry points
(§4.5.6). Re-typing `Raw` is not needed (§13 OD12).

**`pkg/mediainfo/native`** (the ffgo probe). Only `app/import/agent` and
`app/squash/worker/inprocess` import it, so only cmd/agent and cmd/transcode
link it.

- **API.** `func New() (*Prober, error)` calls `ffruntime.Load()` (§7.4: ffgo
  `Init`, libavcodec major 63, the shim loaded, the shim API version) and
  `ffruntime.Require(native.Needs)`, where `native.Needs` names the demuxers
  the library uses (`matroska`, `mov`, `mpegts`, `avi`, `flac`, `mp3`, `ogg`,
  `wav`; checked with `avformat.FindInputFormat`, §7.4). Disposition and ChannelLayout are
  zero without the shim (ffgo `streaminfo.go:34-36`), so a `Prober` never exists
  without one. `(*Prober).Probe` and `(*Prober).ProbeAudio` implement
  `mediainfo.Prober`.
- **Opening the file:** `ffgo.NewDecoder(path, ffgo.WithAVOptions(map[string]string{"scan_all_pmts": "1"}),
  ffgo.WithInterrupt(ctx.Done()))` (F16). ffprobe sets `scan_all_pmts=1` for
  MPEG-TS; ffgo leaves options a demuxer does not consume in the dictionary with
  no error (ffgo `probe.go:133-157`).
- **Building `Raw`:** field by field as ffprobe's JSON would decode (§6.3).
- **First frame:** the first frame of **v:0**, the first video stream by index,
  as `-select_streams v:0` does, through inprocess's `firstFrame` loop (at most
  `firstFramePackets = 2000` packets, `StreamDecoderConfig{Threads: 1}`). DOVI
  is read from the same stream (go-ffprobe `FirstVideoStream`, probedata.go:162).
  `FrameErr` and `IncompleteHDR` behave as today.
- **Strings as ffprobe prints them.** ffprobe validates every string it
  prints (its text writer defaults to `string_validation=replace` with U+FFFD,
  `fftools/textformat/avtextformat.c`), one U+FFFD per `av_utf8_decode`
  failure (bad lead or continuation bytes, overlong forms, surrogates,
  U+FFFE/U+FFFF). ffgo hands back raw `AVDictionary` bytes, and Go's
  `json.Marshal` would later replace per byte and keep noncharacters. So
  `native` ports `validate_string` (av_utf8_decode rules, flags 0, U+FFFD
  replacement) as `validString(s string) string` and applies it to every
  format, stream and chapter tag and title before mapping; `clipRunes`
  (map.go `chapterList`) then cuts the same characters ffprobe's output would.
- **Result:** `mediainfo.FromRaw(raw), raw`.
- **Cancellation.** Each `Probe` runs under `ffruntime.Do(ctx, …)`: it returns
  `ctx.Err()` once ctx ends and the grace passes, while `WithInterrupt` makes
  FFmpeg abort at its next I/O check. A goroutine still blocked in the kernel
  (an NFS hard mount) counts in `ffruntime.Abandoned()`; past
  `ffruntime.MaxAbandoned` the process-level healthz check `ffgo` fails and the
  kubelet restarts the pod. This plays the role `WaitDelay` plays for a
  subprocess.
- Code moved here from inprocess/probe.go: `staticHDRSideData`,
  `firstFrame`/`readFirstFrame`, `masteringDisplay`, `doviRecord`, `codecType`,
  `disposition`, `tags`, `positive`, `colourNames`. `inprocess.New` builds a
  `*native.Prober`, and `Engine.Probe` delegates to it.
- `native` never touches ffgo's process-wide log callback; the binaries route it
  through `ffruntime.RouteLog` / `Capture` (§7.4).

**`test/ffprobeoracle`** (test support only). Today's go-ffprobe plus exec'd
frame-probe code, verbatim: `Probe`, `ProbeAudio`, `buildRaw`, `mergeFrame`,
`parseDoviRecord`, `runFrameProbe`. It is the transitional
`pkg/mediainfo/ffprobeexec` (§4.3 step 1.7), moved under `test/` when the
native probe ships, so the repo-wide exec guard's allow-list ends empty (§4.5.6)
and no production binary can import it (the deps guards deny `…/test/`). It is
the parity oracle on hosts that have ffprobe.

**`pkg/probestore`.** The record protocol of §6.5.2; imports `pkg/events` and
`pkg/events/schema`, no ffgo. The manager, the probe worker and the importers use it.

**`app/import/worker/probe`.** The probe-task handler (§6.6). It takes a
`mediainfo.Prober`, does not import ffgo, and has no Kubernetes client.

### 6.3 Every ffprobe field clustarr consumes, and where native gets it

"inprocess" means inprocess/probe.go already reads the field the same way; F*
refers to the unified fork additions of §7.5.

| ffprobe field | Read by | ffgo .9-.11 | Native source |
|---|---|---|---|
| format.filename | container (`containerFromPath`, classify.go:74), `AtPath` | – | the path argument |
| format.format_name | `FromProbe` Format.Name | `Decoder.FormatName()` | inprocess |
| format.duration | runtimeMillis (map.go:61), `FromProbe`, standard minDuration | `DurationMicroseconds()` | `ffprobeTime`: replicates ffprobe's `print_time` (`float64(ts)*(float64(num)/float64(den))`, formatted `%.6f`, then `ParseFloat`, as go-ffprobe's `,string` decodes it) and the N/A rule (a duration of 0, or a non-duration at AV_NOPTS_VALUE, becomes 0) |
| format.bit_rate | videoBitrateKbps fallback (map.go:206-223), ProbeAudio fallback | `Decoder.BitRate()` | inprocess (`positive`) |
| format.size | `FromProbe` SizeBytes | – | `os.Stat` |
| format.tags | CLUSTARR_PROFILE (`FormatTag`, map.go:121); worker run.go:545,592 | `GetMetadata()` | inprocess |
| stream index, codec_type | everything | `StreamInfo.Index`, `Type` | inprocess |
| codec_name | videoCodec, audio and subtitle codec, bitmap flag (the MP4 standard's `HoldImageSubtitles` and its sidecar format, main `fbfd754c`), ProbeAudio | `StreamInfo.Codec` (`avcodec_get_name`) | "none" and "unknown_codec" become "", because ffprobe omits a codec without a descriptor |
| profile | videoProfile; audio profile (naming's Atmos and DTS-HD tokens, Plex, `FromProbe`'s Lossless and Atmos) | **missing** | F11a, ffprobe's rule: `ProfileName(id,p)`, else `strconv.Itoa(p)` when `p != ProfileUnknown`, else "" |
| level (video) | `FromProbe` | **missing** | F11a |
| pix_fmt | pixelFormat, videoBitDepth (classify.go:33) | `PixelFormatName` | inprocess |
| width, height | summary, `quality.AugmentFromMediaInfo` | ✓ | inprocess |
| r_frame_rate | fpsMilli (map.go:71); `FromProbe` FrameRate (sets the x265 keyint) | **missing** (avg only) | F11e: `"%d/%d"` of `RealFrameRate`, always printed, 0/0 included, as ffprobe does |
| field_order | `FromProbe` | **missing** | F11c |
| stream color_range, color_space, color_transfer, color_primaries | `IncompleteHDR` evidence (incomplete.go:67-84); `FromProbe` fallback | **missing** | F11d + `colourNames` (unspecified becomes "", which ffprobe omits) |
| stream duration | `FromProbe` | **declared but never filled** (ffgo.go:440) | F11e + `ffprobeTime` |
| stream bit_rate | audio bitrateKbps, video bitrate, ProbeAudio | `StreamInfo.BitRate` (since .8) | inprocess |
| bits_per_raw_sample | ProbeAudio SampleBits | **missing** | F11b, read from codecpar after `find_stream_info` (note below) |
| bits_per_sample | ProbeAudio fallback | **missing** | F11b `BitsPerSample(id)` (`av_get_bits_per_sample`) |
| channels, sample_rate | summary, `FromProbe` | ✓ | inprocess |
| channel_layout | audio channelLayout (UI, Plex, `FromProbe`) | describes an unspecified order as "N channels" | F11f: "" when `ChannelOrder == ChannelOrderUnspec` |
| disposition (default, comment, forced, hearing_impaired, attached_pic) | summary, `primaryVideoStream`; since standard Version 2 the audio default and commentary rules (`planAudio`) and each subtitle sidecar's `.forced`/`.sdh` (`planSubtitles`), so the plan hash | from the shim | inprocess |
| tags: language, title, encoder, BPS, BPS-eng, filename, mimetype | summary, videoEncoder (map.go:247), videoBitrateKbps (map.go:227), `FromProbe`; since main `c0fb39b7` a subtitle's title also decides `.forced` (`forced`, `sign(s)`, `song(s)`, unless it also says `full` or `dialogue`) and `.sdh` (`sdh`, `hi`, `cc`, `hearing impaired`), and names a dropped track in `Result.Dropped`, both in the plan hash; the language's `lang.Normalize` base names the sidecar | `Metadata` (raw bytes) | inprocess, through `validString` (§6.2); `StreamTags` filled as go-ffprobe's `setFrom` fills them |
| side data "DOVI configuration record" | doviProfile, doviBLCompatID, `Raw.Dovi` | `StreamSideData(…, PacketSideDOVIConf())` | inprocess `doviRecord` |
| stream side data mastering display and CLL (presence) | `IncompleteHDR` | `StreamSideData` | inprocess `staticHDRSideData` |
| chapter id, tags, start_time, end_time | chapters, chapterList (map.go:164-187), `FromProbe` | count and title ✓; times overflow | F15 + `ffprobeTime(StartTS/EndTS, TimeBase)`; TagList is all chapter metadata |
| first-frame colour, mastering display, CLL, HDR10+ | `ClassifyHDR` (classify.go:82), x265 params | `Frame.ColorSpec`, `FrameSide*` | inprocess |
| attachment count | attachments | stream type | inprocess |

**bits_per_raw_sample.** ffprobe prints the value from the decoder it opened
(`ffprobe.c:2046`); `avformat_find_stream_info` copies that decoder's context back
into codecpar, so the two agree. The 24-bit FLAC fixture
(`test/data/mediainfo/audio_flac_24bit.flac`) holds this.

**The first-frame read differs in one way.** ffprobe decodes one packet of v:0
and flushes; native decodes up to 2000 packets. When ffprobe's first packet
decodes, both read the same frame. When it does not, ffprobe either fails the
probe (an HDR-tagged stream gives `ErrIncompleteProbe`, retried forever) or reads
SDR, while native reads a later frame; on an SDR stream the summary is identical
either way. So they can differ only on files ffprobe cannot probe, and there are
none live: all 11,958 MediaFiles are `Probed=True`.

### 6.4 ffgo fork additions the probe needs

F11a-F11f, F15 and F16 of the unified table (§7.5), all in
`v0.0.0-clustarr.13`. Every new struct read is a shim layout entry in
`shim/ffshim.c` `ffshim_fields[]` (:646-724) with a `layout.Offset` Go fallback
for FFmpeg 7.1, as the existing entries have; FFmpeg 9 requires the shim anyway
(`ffgo.go:35`). The native probe cannot ship before `.13` (R3 amendment). ffgo
tests owned here: `TestStreamInfoReadsWhatFFprobePrints` (FFmpeg 9 with ffprobe
present; every new field against `ffprobe -show_streams` JSON),
`TestChapterTimesPastTheInt64MicrosecondRange`,
`TestWithInterruptAbortsAnOpenOnAStalledReader` (opens a FIFO nobody writes).

### 6.5 MediaFile probing through the agent (R3)

#### 6.5.1 Names

| Kind | Name and settings |
|---|---|
| Stream | `events.StreamWorkProbe = "CLUSTARR_WORK_PROBE"`, subjects `events.FilterWorkProbe = "clustarr.work.probe.>"`. WorkQueue retention, File storage, **DiscardNew** with no message schedules: a full queue refuses the publish and the reconcile retries, rather than dropping a queued task. MaxBytes 64 MiB (a task is about 0.8 KB, so about 80k tasks; a full 11,958-file re-probe is about 10 MiB). Duplicates 1h, Replicas 3, `Durable: true`, which keeps it outside ForSingleNode's 64 MiB memory budget; inside it, the bump would discard-oldest the import stream's work (the 2026-10-01 gotcha). |
| Subject | `events.WorkProbeSubject(p events.Priority, mediaKey string)` gives `clustarr.work.probe.file.<high\|low>.<mediaKey>`, mediaKey = `events.MediaKey("mediafile", ns, name)`. |
| Consumers | `events.ConsumerImportProbeHigh = "importarr-probe-high"`, filter `events.FilterProbeHigh = "clustarr.work.probe.file.high.>"`; `events.ConsumerImportProbeLow = "importarr-probe-low"`, filter `events.FilterProbeLow = "clustarr.work.probe.file.low.>"`. Both: AckWait 60 s, BackOff [60 s, 5 m, 30 m], MaxDeliver 4, `Slots: 4`, MaxAckPending 32 (`Slots × events.AutoscaleReplicaCeiling`, §9.2). Two lanes of 4 slots give one pod today's 8 concurrent probes (`MaxConcurrentReconciles = 8`), and a ProbeVersion re-probe never queues ahead of a new import's probe. No Heartbeat, because `probe.TaskTimeout` (45 s) is below BackOff[0]. |
| Bucket | `events.BucketProbes = "clustarr-probes"`. TTL 7 d, History 1, `Durable: true`. |
| KV key | `events.ProbeKey(uid string) string` = `KVKeyToken(uid)`. |
| Msg-Id | `events.MsgIDForProbe(uid, probeHash string, version int32, seq int64)` gives `probe/<uid>/<probeHash>/v<version>/<seq>`. |
| Task | `schema.ProbeTask` in `pkg/events/schema/probe.go`, schema "importarr.ProbeTask.v1": `MediaFile Ref` (ns, name, uid), `Path`, `ProbeHash`, `ProbeVersion` (the minimum the manager accepts), `Seq int64`, `Lane`. Envelope Type "importarr.ProbeTask", Source "catalogarr@<version>", Key "<ns>/<name>". |
| Record | `schema.ProbeRecord` in the same file; the KV value is its JSON: `MediaFile Ref`, `Seq`, `State` (`ProbeRequested`="requested", `ProbeProbed`="probed", `ProbeFailed`="failed"), `Path`, `ProbeHash`, `ProbeVersion` (the version of the probe that produced the answer), `RequestedVersion` (the `ProbeTask.ProbeVersion` the answer was for; 0 for an importer's seed, which answers no request), `Lane`, `RequestedAt`, `ProbedAt`, `MediaInfo *commonv1.MediaInfo`, `Failure` (at most 1024 bytes), `Transient`, `Abandoned` (the probe outlived `ffruntime.Grace` after its deadline, §6.6), `AbandonedCount` (consecutive abandoned probes of this path and hash, carried forward by `Store.Request` and `Store.Answer`), `Source` ("probe" or "import"), `Prober` (pod name). An undecodable value is treated as absent. |

None of this adds a field manager, a CRD field or an RBAC rule. The agent writes
only to NATS.

#### 6.5.2 Record protocol (`pkg/probestore`)

One record per MediaFile UID. Every write is a compare-and-swap (`Create` when
absent, else `Update(rev)`); on a CAS miss the writer re-reads and decides again.

- **The reconciler** (`Store.Request(ctx, want, prev)`) writes a `requested`
  record with `Seq = prev.Seq + 1` (1 when absent) and `RequestedVersion =
  want.ProbeVersion`, as an `Update` at the revision `judgeProbe` read, then
  publishes the task. It writes only when `judgeProbe` returned **request**
  for that revision, so it never overwrites a record the judge would
  incorporate or wait on; a CAS miss means another write landed, and the
  reconcile re-reads and judges again.
- **The probe worker** (`Store.Answer(task, rec)`) writes `probed` or `failed`
  with `Seq = task.Seq` and `RequestedVersion = task.ProbeVersion` when the
  current record is absent, `requested` with
  `Seq <= task.Seq`, or `probed`/`failed` with `Seq < task.Seq`. When the
  current record has `Seq > task.Seq` (superseded) or already answers
  `Seq == task.Seq`, it drops its result. `Store.Superseded` runs the same test
  before probing, so the worker acks a superseded or answered task unprobed.
- **The importer seed** (`Store.Seed`) writes `probed` with `Seq = current.Seq`
  (0 when absent), `RequestedVersion = 0` and `Source "import"`, unless the
  current record is already `probed` for the same hash and version.
- **Nobody deletes a record**; the TTL retires them. A reconcile from a lagging
  cache then finds the same matching record and re-applies identical values,
  rather than requesting a second probe.

#### 6.5.3 The reconciler

`mediafile.Reconciler` drops `Probe ProbeFunc` and the `ProbeFunc` type and gains
`Probes *probestore.Store`; the constructor becomes
`NewReconciler(c, scheme, recorder, probes)`; `app/catalog/manager` passes
`probestore.New(bus)` (today app/catalog/run.go:379). Everything up to
`evaluateProbe` (mediafile_controller.go:241-274) is unchanged. After it:

**Path-only staleness is incorporated without a probe.** The stat hash names
the path (`pkg/mediainfo/hash.go:35-36`), so any rename makes `ps.Stale` true
although the bytes are those `status.mediaInfo` describes (probe.go:64-65;
mediafile_controller.go:363). Every rename (the rename controller, a
LibraryScan rename pass, `renameTranscoded` after each transcode) would
otherwise read Probed=False and ProbePending for the queue's latency, a bulk
rename after a naming-preset change would put thousands of probes on the high
lane ahead of new imports, and captionarr, squasharr and the segment planner
would skip the files meanwhile. So, when `ps.Stale && !bytesChanged(&mf, ps)`,
`swap == nil`, `kept == nil` and `status.mediaInfo != nil`, the reconcile
applies, with no record read and no task: `ProbeHash = ps.Hash`,
`MediaInfo = mediainfo.AtPath(status.mediaInfo, path)` (the container is the
only path-derived field, map.go:60), `ProbedAt = now`, `ProbeVersion`
unchanged, conditions unchanged. That is exactly what today's synchronous
re-probe of the same bytes recorded. If the stored version is older than
`mediainfo.ProbeVersion`, the next reconcile requests a version-only re-probe
on the low lane as usual.

Otherwise:

```
due  := swap != nil || kept != nil || probeDue(status.probeHash, status.probeVersion, ps)
lane := "high" if status.probeHash == "" || bytesChanged(&mf, ps) || swap != nil || kept != nil, else "low" (a version-only re-probe)
want := {UID, Path: path /* the swap target when swapping */, ProbeHash: ps.Hash, ProbeVersion: mediainfo.ProbeVersion}
```

A `kept` job needs no probe when `ps.Hash == status.probeHash` and the version is
current: the same apply records the kept TranscodeState and `probedAt = now`
from the stored mediaInfo, because the bytes are untouched.

The pure function `judgeProbe(rec, ok, want, status, now)` in `probe.go`
decides; the first matching row wins. "Matching" means the record's UID, Path
and ProbeHash equal `want`'s.

| Record | Verdict | What is written |
|---|---|---|
| `probed`, matching, `ProbeVersion >= want` | **incorporate** | today's success block (:300-415), unchanged: one `k8s.Apply` with the labels, plus spec path, size, modTime and `original=false` after a swap; `known.ProbeHash`, `ProbedAt = now`, `ProbeVersion = rec.ProbeVersion`, `MediaInfo = rec.MediaInfo`; a swap gives a Compliant `TranscodeState`, a kept job its profileTag, changed bytes `staleTranscodeState`; Probed and Ready True; the `Probed` Event |
| `probed` or `failed`, matching, `ProbeVersion < want` and `RequestedVersion < want`: an importer's seed, or an answer to a request made before a `mediainfo.ProbeVersion` raise | **request** | at once (`Seq + 1`), on the lane the lane rule picks: low when `status.probeHash` already equals the hash (a version-only re-probe, which changes no condition), else high |
| `probed`, matching, `ProbeVersion < want` and `RequestedVersion >= want`: an agent older than the manager answered a current request (skew during a rollout, or an import pod draining fetched tasks) | **incorporate as is**, when `status.probeHash != rec.ProbeHash` (the stored summary does not describe these bytes, for example a new file); otherwise **wait** | incorporation as in row 1 but with `ProbeVersion = rec.ProbeVersion`, so the file is named, Ready and usable now and stays due by version; either way requeue at `rec.ProbedAt + probeSkewRetry` (30 m), when the next row applies. Never re-request sooner: while the old agent still answers, that would loop. A `failed` record in skew falls through to the `failed` rows below |
| `probed` or `failed`, matching, `RequestedVersion >= want > ProbeVersion`, and `now >= ProbedAt + probeSkewRetry` | **request** | low lane when `status.probeHash == rec.ProbeHash`, else high |
| `failed`, matching, `AbandonedCount >= probeAbandonLimit` (3) | **give up** | ProbeFailed, message `probe abandoned 3 times: <rec.Failure>`; no requeue. Only new bytes (a new hash) or a `ProbeVersion` raise (row 2) request again (§6.6) |
| `failed`, matching, retry not yet due | wait | today's ProbeFailed branch (:280-297) with `rec.Failure`; requeue at `ProbedAt + probeTransientRetry` (5 m) when `Transient`, else `+ probeFailedRetry` (1 h). Today this is 30 s forever (§13 OD47). |
| `requested`, matching, `now - RequestedAt` under the lane's timeout | pending | ProbePending. While `now - RequestedAt < probeRepublishWindow` (50 m), republish the same Msg-Id (JetStream absorbs the duplicate; it covers a failed publish). Requeue at the timeout. |
| absent; a record describing other bytes, path or UID; `failed` with its retry due; `requested` past `probeRequestTimeoutHigh` (6 h) or `probeRequestTimeoutLow` (72 h) | request | `Store.Request`, then ProbePending; a CAS miss returns `Requeue: true`. `Store.Request` carries `AbandonedCount` forward from a previous record for the same path and hash |

**Why `RequestedVersion`.** Without it a matching record below the wanted
version could only wait: after any `mediainfo.ProbeVersion` raise, every file
with a record younger than the bucket's 7-day TTL (each import seeds one, and
every swap and re-probe writes one) would sit in "wait" until the record
expired, breaking "a ProbeVersion raise re-probes every file once" for exactly
the newest files; and during skew a new file would read Probed=False for days.
The two cases differ only in which version was asked for, which the record now
carries.

**ProbePending status.** A version-only request changes no condition and no
naming input (the stored mediaInfo still describes the bytes). Any other pending
request writes `Probed=False` and `Ready=False`, reason `ProbePending`
(`catalogv1alpha1.MediaFileReasonProbePending`), message
`probe requested <RFC3339> (<lane>)`; naming through
`renderNaming(probeStale: ps.Stale || status.probeHash == "", transcodePending: swap != nil || transcodeRunning)`
(the existing NamingReason ProbePending or TranscodePending, naming.go:122-141);
and every other known field re-sent from `statusOf`, the complete declaration.
Labels and the spec takeover are applied only on incorporation.

**Swap incorporation.** `latestUnincorporatedTranscode` (:759-776) keys on
`status.probedAt`, which moves only on incorporation. So a swap stays pending,
and its rename held, until a record arrives for **the swap target's path with its
new hash**; a record from before the swap differs in path or hash and is never
incorporated. `probedAt` is the manager's `now`, after the job's `finishedAt`,
both set in the manager, so squasharr's retention check
(transcodeprofile/controller.go:368,421) works as today.

**The leader-only waker** (`app/catalog/controller/mediafile/results.go`):
`func (r *Reconciler) probeRecordsSource() source.Source`, registered with
`WatchesRawSource` (precedent: `limitsSource`,
app/indexer/controller/indexer/controller.go:669,685). It runs
`KV(BucketProbes).Watch(ctx, ">")`, enqueues `{rec.MediaFile.Namespace,
rec.MediaFile.Name}` for each `probed` or `failed` value, skips `requested`
values and delete markers, and re-opens the watch after 5 s if it closes while
ctx is live. Controller sources start only on the leader, and the watch replays
every record at start, so a result that landed while no leader ran is still
incorporated. It writes nothing (R3 amendment).

**Lost updates.** Reconcile does no slow work between reading the MediaFile and
applying status (the probe was the slow work); it reads the KV record inside
Reconcile, never from the watch event; the probe worker re-reads the record
after probing, immediately before its CAS write.

**What the manager still does on /data:** two `os.Stat` calls, no media file
opened: `path` (:252; FileMissing; size and mtime feed the probeHash,
`bytesChanged` and the spec-takeover values) and `mf.Spec.Path` inside
`swapTarget` (:552; whether the source is still present, which decides a kept job).

**Downstream gates.** A Go-only method (no schema change) in
`api/catalog/v1alpha1/mediafile_transcoded.go`:
`func (mf *MediaFile) ProbeCurrent() bool`, true when `probeHash != ""`,
`mediaInfo != nil`, and Probed is not False with reason `ProbePending`. During
a stale-bytes ProbePending, `status.probeHash` still names the old bytes (it
moves only on incorporation), so checks that compare hashes pass while the
summary describes replaced bytes; the window used to be one reconcile and is
now the probe queue's latency. The common trigger is an upgrade import to the
same canonical path, which reuses the MediaFile (`k8s.ChildName(..., dest)`,
fileimport/process.go:363). Its callers:

- `transcodeprofile.probed` (profile.go:148-150): no new job is planned.
- The transcodejob reconcile (transcodejob/controller.go:507-516): after the
  `MediaInfo == nil` check, `!mf.ProbeCurrent()` is
  `r.wait(tj, st, "waiting for MediaFile %s to be re-probed", mf.Name)` and a
  requeue after `requeueWaiting`: the job stays Planned, is not dispatched and
  takes no slot. Without it a job would take a slot (a GPU on nvidia or intel),
  the worker would re-probe and exit `ExitInvalidSource`
  (app/squash/worker/run.go:211-214), and the job would be replaced only after
  the probe landed (transcodeprofile/controller.go:192-213).
- captionarr `subtitleprofile/controller.go:187` and
  `subtitlerequest/reconciler.go:218`.
- `segmenting.Applier.ApplyMerged` (apply.go:92, after C6 in
  `app/catalog/worker/segmentresults`): an update for a file whose probe is
  pending is dropped like one for another probe hash ("re-probing: its new
  probe asks again"), so a TheIntroDB or analysis result for the old bytes is
  never applied to the new ones.

**Constants** in `probe.go`: `probeRequestTimeoutHigh=6h`,
`probeRequestTimeoutLow=72h`, `probeRepublishWindow=50m`,
`probeTransientRetry=5m`, `probeFailedRetry=1h`, `probeSkewRetry=30m`,
`probeAbandonLimit=3`.
`MaxConcurrentReconciles` stays 8; its comment loses the ffprobe rationale. New
metric: `clustarr_probe_requests_total{lane}`.

```
manager (static, /data stat only)                  NATS                          agent --domain import (native, /data)
MediaFile.Reconcile -- judgeProbe(clustarr-probes[uid]) -- Request(CAS) --> clustarr-probes <-- Answer(CAS) -- probe.Worker
          '-- publish ProbeTask --> CLUSTARR_WORK_PROBE --> importarr-probe-high|low -----------'   native.Prober.Probe
probeRecordsSource (leader) <-- Watch -- clustarr-probes <-- Seed(CAS) -- fileimport / rescan, after creating a MediaFile
incorporate --> one k8s.Apply + one PatchStatus as catalogarr
```

### 6.6 The import domain (R3, R4)

`app/import/agent` builds one `native.New()` at start. If that fails the process
exits non-zero: a native image without FFmpeg 9 or the shim is broken, and
importing without probes would silently freeze name-derived qualities. The
process-level healthz check `ffgo` (§3.3) covers a wedged probe. The Prober goes
to:

**The probe worker**, `probe.NewWorker(bus, prober, probe.Options{Pod, DataRoot: "/data"})`:

- two `EveryReplica` subscriptions, on `importarr-probe-high` and
  `importarr-probe-low`, each with its durable's `Slots` (4) as per-pod
  `MaxInFlight`, overridable by `CLUSTARR_CONSUMER_SLOTS` (§9.2);
- the handler, in order: decode the task (an undecodable one is
  `events.Discard`); ack at once if `Store.Superseded`; refuse a path that is not
  absolute or not under DataRoot (non-transient failure); stat the file; probe it
  with `Prober.Probe` under `probe.TaskTimeout = 45*time.Second`; stat again (a
  changed file is a transient failure); take `ProbeHash` from the first stat;
  `Store.Answer`, then ack;
- a KV error returns the error (nak, then BackOff); a timeout that returned
  within the grace, or `mediainfo.ErrIncompleteProbe`, is recorded as
  `Transient`;
- a probe that `ffruntime.Do` **abandoned** (its goroutine still in FFmpeg
  after the deadline plus `ffruntime.Grace`, typically a CPU-bound demuxer or
  first-frame loop on a malformed file, which `WithInterrupt` cannot stop
  because it is checked only at I/O) is recorded as **non-transient** with
  `Abandoned: true` and `AbandonedCount = previous + 1` for the same path and
  hash, and `Failure` naming it. Retrying such a file every 5 minutes would
  pile abandoned goroutines up to `ffruntime.MaxAbandoned` and restart the
  import agent about every 20 minutes, cutting in-flight imports each time;
  instead it retries on `probeFailedRetry` and the reconciler gives up after
  `probeAbandonLimit` (§6.5.3). The same rule holds for markers' decode (an
  abandoned decode returns `events.Discard`, so the task goes to the DLQ at
  once instead of being redelivered into another stuck call) and caption
  extraction (the fetch item fails non-transiently for that file and probe
  hash, like any other extraction error);
- metric `clustarr_probe_duration_seconds{lane,outcome}`; no Kubernetes client.

**fileimport**, `fileimport.NewWorker(c, bus, prober)`: new field
`Prober mediainfo.Prober`; `probeVideo` becomes a method calling
`w.Prober.Probe`, keeping `videoProbeTimeout` (15 s) and its heartbeat;
`ProbeAudio` defaults to `prober.ProbeAudio`, now under
`audioProbeTimeout = 15*time.Second` with a heartbeat first (unbounded today).

**rescan**, `rescan.NewWorker(c, bus, prober)`: `ProbeVideo` and `ProbeAudio`
become closures over the prober, with 15 s timeouts.

**Seeding the probe record.** Each import currently probes the file twice (the
importer, then the reconciler). Seeding removes the second probe and R3's
first-naming latency for imports:

- After `applyMediaFile` (fileimport/worker.go:405), the import reads the applied
  object's UID (controller-runtime writes the response back into the apply
  configuration, typed_client.go:150-185).
- The movie, episode and series-root import paths call `Store.Seed` with
  `mediainfo.AtPath(mi, dest)` and `ProbeHash(dest, destInfo.Size(), destInfo.ModTime())`,
  reusing the stat at process.go:354.
- The rescan seeds after `mediafilespec.Apply` (was `applySpec`,
  rescan/mediafile.go:613), which then returns the UID, when it creates a new
  MediaFile whose probe ran.
- A seed failure is logged and never fails the import. The first reconcile finds
  a matching `probed` record and incorporates it with no task.

fileimport, rescan and probe never import `pkg/mediainfo/native`; only
`app/import/agent` does.

### 6.7 Parity (R3)

**Rule.** `ProbeVersion` stays 3. The native probe ships only when, for every
file ffprobe probes successfully, it produces a `commonv1.MediaInfo` deep-equal
to the oracle's, with no field blanked. Once F11 and F15 land and `validString`
(§6.2) applies ffprobe's string validation, the text fields match by
construction; `ffprobeTime`'s print-and-parse makes the floats identical.

1. **`pkg/mediainfo/native/parity_test.go` `TestNativeProbeMatchesFFprobe`**,
   skipping unless ffprobe is on PATH and `native.New` succeeds (failing instead
   under `CLUSTARR_REQUIRE_TOOLS=1`, through `test/nativetest.Require`). Fixtures
   are inprocess's `probeFixtures` plus ffmpeg-made: PCM in Matroska; Matroska
   with chapters at 9,300,000 ms; field-coded interlaced H.264 in MPEG-TS; DTS
   5.1, E-AC-3 5.1 in MP4, TrueHD; VFR Matroska, MPEG-2 interlaced, AVI with MP3;
   MOV with PCM 24-bit, FLAC 24-bit, ALAC 24-bit; an MP4 tagged
   `clustarr_profile`, a Matroska with `BPS` tags, an MP4 with cover art; and
   Matroska files whose titles and tags carry invalid UTF-8 (a long Latin-1
   chapter title past `clipRunes`' 128 bytes, an overlong sequence, an encoded
   surrogate, U+FFFE), so `validString` is held to ffprobe's output. It
   asserts equal `MediaInfo`; equal Raw leaves that `FromProbe` and
   `IncompleteHDR` read (Profile, Level, RFrameRate, FieldOrder, stream colour,
   Duration, SampleRate, BitRate, BitsPerRawSample, BitsPerSample, Disposition,
   Dovi, MasteringDisplay, ContentLight, HasHDR10Plus, ColorTransfer); equal
   `ProbeAudio`; equal `standard.Plan(transcode.FromProbe(...)).Hash()`.
2. **`TestNativeProbeMatchesFFprobeOnACorpus`** (same file) walks
   `CLUSTARR_PROBE_PARITY_DIR` recursively and reports every diff. With
   `CLUSTARR_PROBE_PARITY_MEDIAFILES=<kubectl get mediafiles -o json>` it also
   compares native output with the stored `status.mediaInfo` of every file whose
   stat hash still equals its `probeHash`; that oracle needs no ffprobe. The
   owner runs it as the cutover gate (§11.2, §13 OD16).
3. **`test/parity` `TestTheInProcessProbeAgreesOnTheLibrary`** (parity build tag)
   compares native with `test/ffprobeoracle` and stops blanking profiles.

**Accepted behaviour changes, raising neither ProbeVersion nor `standard.Version`:**

- **Frame rate in the transcode worker.** The worker plans its keyint from
  `r_frame_rate`, as `status.plan` already does (`FromSummary` uses `fpsMilli`).
  Today it uses the average rate and logs "the plan differs"
  (app/squash/worker/ffgo.go:104-114); field-rate sources will now be encoded
  with the plan status.plan records (§13 OD17).
- **First video stream.** The worker reads v:0 as ffprobe does, rather than the
  first non-cover-art video stream. No live file has cover art first (no live
  videoCodec is mjpeg or png).

### 6.8 Tests that change

**pkg/mediainfo**

| File | Change |
|---|---|
| `probe_test.go` | `TestProbeH264MP4`, `TestProbeHEVC10bitMKV`, `TestProbeMissingFileReturnsWrappedError` move to `native/probe_test.go`, assertions unchanged; "Constrained Baseline" and "Main 10" now prove F11a. |
| `audio_test.go` | `TestAudioProbeFrom` uses `AudioProbeFromRaw`; `TestProbeAudioReadsTheFixtures` moves to native. |
| `ffprobe_test.go` | all five tests (`TestBuildRaw*`, `TestMergeFrame*`) move to `test/ffprobeoracle`. |
| `incomplete_test.go` | `TestIncompleteHDRWhenTheStreamSaysTheVideoMayBeHDR` stays; the three `TestProbeOf*` move to native and fail `readFirstFrame` instead. |
| `hdr10_fixture_test.go` | `TestHDR10FixtureClassifiesAsHDR10` moves to native. |
| `profiletag_ffmpeg_test.go` | `TestProbeReadsTheTagSquasharrWrites` moves to native and drops the ffprobe skip. |

**app/squash**

| File | Change |
|---|---|
| `worker/inprocess/probe_test.go` | `TestTheInProcessProbeAgreesWithFFprobe` deleted (native's parity test replaces it); `TestDoviRecordReadsEveryField` moves to native. |
| `worker/inprocess/incomplete_test.go` | both tests move to native with `hdr10WithStreamSideData`. |
| `worker/inprocess/measure_test.go` | `Engine{}` literals become `New()`. |
| `worker/worker_envtest_test.go` | `fakeEngine.Probe` and `recordingEngine.Probe` (:1076, :1099; :1085, :1108 at main `80175fdc`) and `videoCodec` (:389; :397) use `native.Prober`, skipping without FFmpeg 9. Main's MP4 sidecar tests in this file (`TestRunPlacesTheSidecarsAndKeepsAnExistingOne` and five more, `b33e4417`, `a9848d35`) probe through the same two engines; their source clip `withASSAndForcedSRT` runs the ffmpeg CLI (`adbb865a`) and converts with the plan's U2 task W6.21. |
| `controller/transcodejob/parity_envtest_test.go` | `TestStatusPlanIsTheArgvTheWorkerRenders` uses native. |

**pkg/transcode, pkg/naming, test/parity**

| File | Change |
|---|---|
| `pkg/transcode/standard/parity_test.go` | `TestTheSummaryAndTheProbePlanTheSameStandard`, `TestContainerNamesFromTheSummaryAndTheDemuxer` and main's `TestTheSummaryAndTheProbePlanSubtitlesAlike` (`fbfd754c`: the stored summary and a live probe give the same sidecars and drops, so subtitle titles and dispositions must survive the native probe byte for byte) use native. |
| `pkg/transcode/engine/video_test.go` | `TestTheStandardsNVENCPlanHoldsALeanSourceUnderItsBitRate` uses native. |
| `pkg/naming/catalogctx/context_test.go` | `TestContainerExtReadsWhatTheProbeRecords` uses native. |
| `test/parity/parity_test.go` | `checkStandard` and the library test use native or the oracle. |

**app/catalog/controller/mediafile**

| File | Change |
|---|---|
| new `probe_helper_test.go` | `settle(t, r, key)`: Reconcile; run `app/import/worker/probe/probetest.Agent` (the real `probe.Worker` over membus with a fake Prober); Reconcile again. `fakeProbe` becomes `fakeProber`. |
| `mediafile_envtest_test.go` | `TestMediaFileFieldManagersStayDisjoint` also asserts no new manager appears, that `catalogarr` never owns `f:status.f:markers`, and that `catalogarr-markers` owns nothing under `f:status` but `f:markers` (R3 amendment). `TestReconcileRealFFprobe` is deleted (the real end-to-end check moves to the probe package). `TestReconcileRecordsAnEarlierInstallsTranscode`, `TestTranscodeJobWatchTriggersReconcile`, `TestSubtitleRequestWatchTriggersReconcile` use `settle`. `TestTransientFileFailuresPreserveProbedStatus` gets its ProbeFailed case from a `failed` record. |
| `transcoded_envtest_test.go` | `TestTranscodedFileBytesChangedOnDisk`, `TestObservedFingerprintWakesTheController`, `TestContainerChangeMovesSpecPath` use `settle`; `TestReplaceSourceFalseIsNotASwap` also asserts no task is published. |
| `naming_envtest_test.go` | all nine tests use `settle` or `fakeProber`; in `TestNamingReportsWhyItCannotRender` the failing-probe case reads ProbePending until a `failed` record arrives, then ProbeFailed. |
| `ownerstatus_envtest_test.go` | `TestMediaFileReconcileDoesNotReleaseOwnerStatus` uses `settle`. |
| `ready_test.go` | `TestAFileBackFromMissingIsReadyAgainWithoutAProbe` asserts no task is published. |
| `probe_test.go` | keeps its three tests; adds `TestProbeLane` and `TestJudgeProbe` (one case per row of §6.5.3). |

**app/import/worker**

| File | Change |
|---|---|
| `fileimport/probe_envtest_test.go` | `TestAnImportIsQualifiedAndNamedFromItsProbe`, `TestAProbedDVDRipIsStillDVD` use `NewWorker(c, bus, native)` with native as the oracle; `TestAnImportHeartbeatsImmediatelyBeforeItsProbe` uses a fake Prober. |
| `fileimport/ackdeadline_test.go` | `TestTheImportFitsTheFileConsumersAckDeadline` also holds `audioProbeTimeout`. |
| `rescan/folder_envtest_test.go` | `TestHandleCorrectsANamedQualityFromTheProbe`, `TestHandleHeartbeatsImmediatelyBeforeAProbe` and the oracle at :89 use `NewWorker(c, bus, prober)` or native. |

**pkg/events**

| File | Change |
|---|---|
| `events_test.go` | `TestWorkStreamsAllowSchedules` exempts every WorkQueue stream with `Discard == DiscardNew` (squasharr and `StreamWorkProbe`), matching `Topology.Validate`'s new rule (§9.2), instead of naming squasharr; `TestEveryWorkStreamHasAConsumer` drops its advisory-stream exception, since the dead-letter watchers are now topology consumers (§5.9); `TestDefaultTopologyIsValid` covers the new entries, and a new `TestEveryStaticConsumerHasOneDeadLetterWatcher` holds `DeadLetterWatcherSpec` for each. |
| natsbus `singlenode_limits_test.go` | re-run with the additions. |

**New tests:**

- `app/import/worker/probe`: `TestTheProbeWorkerAnswersOnlyTheCurrentRequest`,
  `TestASupersededTaskIsAckedWithoutProbing`, `TestAPathOutsideTheDataMountIsRefused`,
  `TestAFileThatChangesWhileProbedIsTransient`,
  `TestTheProbeFitsTheProbeConsumersAckDeadline` (TaskTimeout < BackOff[0] on both),
  `TestTheProbeWorkerAnswersARealFile` (native; `nativetest.Require`).
- `pkg/probestore`: `TestAnswerRules` (table), `TestSeedNeverOverwritesAMatchingResult`,
  CAS checks on membus and on an embedded NATS server.
- `app/catalog/controller/mediafile` envtests:
  `TestAStaleProbeRecordIsNeverIncorporated` (another UID, path or hash, or a
  lower version); `TestAPendingProbeKeepsEveryOtherStatusField` (on an object
  that already has status, for both the version-only and stale cases; every leaf,
  then `managedFields` per manager); `TestASwapIsIncorporatedOnlyFromARecordForItsTarget`;
  `TestAProbeRequestIsRepublishedOnlyInsideTheDedupWindow`;
  `TestAFailedProbeRetriesOnItsBackoff`; `TestAnOlderAgentsResultIsNotRequestedAgainAtOnce`;
  `TestTheProbeRecordsSourceEnqueuesResultsOnly` (including the replay after a
  restart); `TestTheRepublishWindowIsInsideTheDedupWindow`;
  `TestAProbeRecordOutlivesItsRequest` (bucket TTL > `probeRequestTimeoutLow`).
- fileimport and rescan: `TestAnImportSeedsTheProbeRecord` (the real reconciler
  incorporates it and no task is published), `TestAnAudioProbeHeartbeatsAndIsBounded`,
  `TestARescanSeedsTheProbeRecordForANewFile`.
- `pkg/mediainfo/native`: `TestProbeHonoursItsContext` (a FIFO),
  `TestAbandonedProbesFailHealth`, `TestNativeProbesInParallel` (`-race`; this
  process is the first to run concurrent ffgo decoders),
  `TestValidStringMatchesFFprobe` (table of the invalid-UTF-8 cases above,
  against ffprobe's JSON when it is on PATH).
- `app/catalog/controller/mediafile`: `TestJudgeProbe` gains a case per row,
  including a pre-raise `probed` record (`RequestedVersion < want`: request at
  once, low lane), a seeded record from an older import agent (request), skew
  on a new file (incorporate with the old version), skew on an incorporated
  file (wait), skew past `probeSkewRetry` (request), and three abandoned
  failures (give up); envtests `TestAProbeVersionRaiseReprobesAFreshlySeededFile`
  (raise `ProbeVersion` in the test through a package seam, with a fresh seeded
  record: a low-lane task is published and no condition changes),
  `TestARenamedFileStaysReadyWithoutAProbe` (rename: Ready stays True, the hash
  and container follow the path, no task is published) and
  `TestAJobIsNotDispatchedWhileItsFilesProbeIsPending` (squasharr; the job stays
  Planned and takes no slot); `segmentresults`
  `TestAnUpdateForAFileWhoseProbeIsPendingIsDropped`.
- `app/import/worker/probe`: `TestAnAbandonedProbeIsNotTransient`.
- `pkg/events` `probe_topology_test.go`: `TestTheProbeStreamIsDurableAndRefusesWhenFull`,
  `TestTheProbeConsumersAreOnTheProbeStream`.
- squasharr and captionarr: `TestAFileWhoseProbeIsPendingIsNotQueued` (and the
  captionarr equivalent).

**Guards:** the go-ffprobe `Probe*`/`SetFFProbeBinPath` ban and the "no exec of
ffprobe" rule are part of `TestNoProductionCodeStartsAProcess` (§4.5.6), which
replaces the pkg/mediainfo half of `TestTheWorkerNeverExecsFFmpeg`. The deps
guards hold `pkg/mediainfo/native` out of manager and ui and require it in agent
and transcode (§4.5). `TestEveryConsumerHasExactlyOneHome` (§10.3.2) places
`importarr-probe-high`/`-low` in the import domain.

### 6.9 Order of work

1. The ffgo additions F11, F15, F16 and their tests (with §7.5's others); tag
   `.13` locally; `replace => ../ffgo-unify`.
2. Split pkg/mediainfo; add native; move the oracle to `test/ffprobeoracle`;
   parity test 1 green on this host.
3. Make inprocess delegate to native.
4. Topology, schema, probestore, the probe worker, the import wiring and the seed.
5. The reconciler, `probeRecordsSource` and the downstream gates.
6. The guards, then the corpus gate (§11.2).
7. Docs: CLAUDE.md's Transcoding paragraph (probing is `pkg/mediainfo/native` in
   the import domain; a ProbeVersion raise re-probes through the low lane) and
   ADR-0017.

---
## 7. Markers decode, embedded subtitles, the ffgo runtime gate and the fork

This section moves the last two FFmpeg-executable users other than the probe
onto in-process ffgo (`pkg/segments/decode` for markers; the embedded-subtitle
provider for captionarr's fetch worker), defines the one runtime gate every
binary that links ffgo passes through (`pkg/ffruntime`), lists every ffgo fork
addition for probe, decode and subtitles in one table, and says how clustarr
builds against the fork before its tag is pushed.

**Sources checked:** clustarr at 7791f1f5; ffgo `/home/appkins/src/mediactl/ffgo`
master `9f7a3a4` (`v0.0.0-clustarr.11`, published today: `.10` = `6e3b3ca` plus
"upgrade: purego to v0.11.1"); re-checked against `.12` (`a184557`, published by
main's `ebbb2322`), which adds `NewPacketFromData`, `(*Packet).Data`,
`avcodec.NewPacket` and the `avcodec` codec-id and extradata setters, changes no
shim file, and closes none of the gaps below; FFmpeg n9.0.2 sources (fftools, libavcodec, libavformat, libavfilter)
for what `ffmpeg(1)` does; the host's FFmpeg n9.0.1 (libavformat 63.1.101) for
demuxer flags and exported symbols.

### 7.1 Starting point (verified)

**What is replaced:**

- `pkg/segments/decode/proc.go:43-69` runs `ffmpeg -nostdin -v error [-threads N] …`
  in its own process group, SIGKILLed on ctx, with `WaitDelay` 5 s, stdout capped
  at 64 MiB and a 2048-byte stderr tail. `decode.go` issues three commands:
  - **Audio** (`:50-62`): `-ss S -t D -i F -map 0:a:N -vn -sn -dn -ac 1 -ar 11025 -f s16le`.
  - **Frames** (`:71-85`): `-skip_frame nokey -ss S -i F -an -sn -dn -vf fps=fps=1:start_time=0,scale=128:72:flags=area,format=gray -f rawvideo`.
  - **Frame** (`:88-98`): `-ss T -i F -an -sn -dn -frames:v 1 -vf scale=320:180:flags=area -f rawvideo -pix_fmt rgb24`.
  - `secs()` (`:100`) rounds to milliseconds.
- `pkg/subtitles/providers/embedded/provider.go:118` runs
  `ffmpeg -y -i P -map 0:<abs index> -c:s srt -f srt pipe:1`, output uncapped, no
  process group.
- Both ran the media image's unpinned BtbN "latest" static ffmpeg
  (`images/Dockerfile.media:107`). In the markers closure only
  `pkg/segments/decode` imports `os/exec` (checked with `go list -deps`); only
  `cmd/segmentarr-worker` imports `app/segments/worker`, `pkg/segments/decode`
  and `textdet`.

**ffgo facts that shape the design:**

1. **FFmpeg is dlopen'ed at package init, not at `ffgo.Init`.**
   `avutil/avutil.go:91`, `avcodec/avcodec.go:63-74` and `avformat/avformat.go:88`
   each call `bindings.Load()` from `init()`; `bindings.Load` is a `sync.Once`
   that remembers a failure (`internal/bindings/bindings.go:53-61`). So any
   binary linking ffgo maps FFmpeg before `main`, and "load ONNX Runtime first"
   cannot work. The RTLD_GLOBAL symbol-clash hazard is closed differently:
   FFmpeg's version scripts export only `av*`/`avcodec_*`/`avpriv_*`/`avfilter_*`/`sws_*`/`swr_*`
   and make everything else local (`libavcodec/libavcodec.v` and siblings,
   n9.0.2); on the host `nm -D --defined-only libavcodec.so.63 libavfilter.so.12 | grep -c ' _Z'`
   returns 0.
2. **`StreamDecoder` passes no option dictionary**: `avcodec.Open2(ctx, codec, nil)`
   (`streamdecoder.go:90`), `threads` set only when greater than 0 (`:77-81`).
   libavcodec's `threads` default is 1 (`options_table.h:216`) but `ffmpeg(1)`
   forces `threads=auto` (`ffmpeg_dec.c:1571-1572`), so a `Threads: 0` decode
   in-process is single-threaded. There is no way to set `skip_frame`.
3. **Seeking.** `Decoder.SeekTimestamp` is `av_seek_frame(-1, ts, AVSEEK_FLAG_BACKWARD)`
   (`decoder.go:840-861`); `Decoder.StartTime` is exposed (`:401-411`); nothing
   exposes the input format's flags or `codecpar->video_delay`.
4. **Filter graphs.** The audio `FilterGraph` derives `channel_layout` from the
   channel count (6 becomes "5.1", anything unlisted "stereo") and passes no
   `time_base` (`filter_graph.go:369-385`), so a 5.1(side) frame is refused. The
   video buffer args carry no `colorspace` or `range` (`:159-162`).
5. **Subtitles.** `NewSubtitleDecoder` opens without `pkt_timebase`
   (`subtitles.go:114`), so `AVSubtitle.pts` and `end_display_time` are never
   filled; it frees each `AVSubtitle` after parsing (`:197-209`); there is no
   subtitle encoder binding.
6. **Cancellation.** No `AVIOInterruptCB`.
7. **Logging.** The shim's log callback formats and forwards every level,
   ignoring `av_log_get_level()` (`shim/ffshim.c:33-47`).
8. **A stale shim is silently wrong.** A shim lacking a field makes
   `layout.Offset` fall back to ffgo's Go offset (`internal/layout/layout.go:61-73`),
   written for FFmpeg 4-7 and wrong on 9; shim functions are optional symbols
   (`internal/shim/shim.go:285-307`). A pinned shim API version is needed.

**What `ffmpeg(1)` does for these commands (n9.0.2):**

- **`-ss`** (`fftools/ffmpeg_demux.c:2243-2277`): `timestamp = ss + ic->start_time`;
  if `!(iformat->flags & AVFMT_SEEK_TO_PTS)` and any stream has
  `codecpar->video_delay`, the seek point drops by `3*AV_TIME_BASE/23` (130434 µs);
  then `avformat_seek_file(ic, -1, INT64_MIN, ts, ts, 0)`, a failure only a
  warning. For a demuxer without `read_seek2` this is exactly
  `av_seek_frame(-1, ts, BACKWARD)` (`libavformat/seek.c:703-711`); matroska,
  mov, mpegts and avi have none. Matroska lacks `AVFMT_SEEK_TO_PTS` (flags 0x0);
  mov/mp4 has it (0x4008008), both read through `av_find_input_format` on
  libavformat 63.1.101.
- **Timestamp offset.** `ts_offset = -timestamp` is added to every packet's pts
  and dts (`:418-421`).
- **Discard comes after the seek.** Unused streams get `discard=AVDISCARD_ALL`
  only in `ist_add`, after the seek (`:1444-1445`, `:2325`), so the seek's default
  stream is chosen with nothing discarded.
- **Accurate seek.** `trim`/`atrim` with `starti=0` (and `durationi` = input `-t`)
  goes right after the buffer source (`:1136-1142`; `ffmpeg_filter.c:1532-1577, 1944-1946`).
- **Open.** `scan_all_pmts=1` (`ffmpeg_demux.c:2163-2165`).
- **Decoder.** `threads=auto`. Decoder cropping is off and applied as
  `av_frame_apply_cropping(AV_FRAME_CROP_UNALIGNED)` (`ffmpeg_dec.c:1597-1598, 434-436`);
  libavcodec's own cropping is aligned unless `AV_CODEC_FLAG_UNALIGNED`
  (`libavcodec/decode.c:786`). Video pts is `best_effort_timestamp`
  (`ffmpeg_dec.c:396`). Audio pts goes through `audio_ts_process` (`:205-283`),
  `av_rescale_delta` into 1/sample_rate.
- **Buffer sources.** Video takes colorspace and range from the first frame
  (`ffmpeg_filter.c:1873-1874, 2247-2248`). Audio is
  `time_base=1/sr:sample_rate=…:sample_fmt=…:channel_layout=<describe>`, or
  `channels=N` for an unspecified order (`:1971-1980`, `:2238`).
- **Outputs.** Audio is `aformat=sample_fmts=s16:sample_rates=11025:channel_layouts=mono`
  (`:1777-1795`) with an auto-inserted `aresample` on default swr options
  (`fftools/cmdutils.c` fills `sws_dict`/`swr_opts` only from user flags).
  `-pix_fmt rgb24` adds `format=pix_fmts=rgb24` (`:1703`).
- **Not replicated in-process:** autorotate and container-crop filters
  (`:1894-1941`); MPEG-TS pts wrap and discontinuity correction
  (`ffmpeg_demux.c:240-300, 400-412`).

### 7.2 `pkg/segments/decode` on ffgo

**Target:** output byte-identical to the `ffmpeg(1)` commands above on the same
FFmpeg 9 libraries, by rebuilding the same demux, seek, decode and filter graph
`ffmpeg(1)` builds, not by reimplementing trimming or resampling in Go.

#### 7.2.1 API

The `Decoder` methods keep their signatures, so `app/segments/worker.Decoder`
(`worker.go:42-47`) and the worker are untouched; `GrayW`, `GrayH`, `RGBW`,
`RGBH` and `SampleRate` are unchanged.

```go
// Decoder decodes in-process through ffgo; Threads is each decoder's thread
// count, 0 meaning FFmpeg's "auto" (ffmpeg(1)'s default).
type Decoder struct{ Threads int }
func (d Decoder) Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error)
func (d Decoder) Frames(ctx context.Context, path string, fromS float64) ([][]byte, error)
func (d Decoder) Frame(ctx context.Context, path string, atS float64) ([]byte, error)
```

The `FFmpeg` field goes (its only setter, `cmd/segmentarr-worker/main.go:124`,
becomes `cmd/markers`). `proc.go` is deleted. New files: `open.go` (open, stream
choice, seek), `audio.go`, `video.go`, `ts.go` (a port of `audio_ts_process` on
the F8 rescale helpers). Each method runs its body inside `ffruntime.Do(ctx, …)`
(§7.4) and opens its own `ffgo.Decoder`, as each ffmpeg run did.

#### 7.2.2 Steps common to all three methods

1. **Open.** `in, err := ffgo.NewDecoderWithOptions(path, &ffgo.DecoderOptions{AVOptions: map[string]string{"scan_all_pmts": "1"}})`,
   `defer in.Close()`. Unused dictionary entries are freed (`probe.go:133-157`),
   so non-TS inputs ignore the option, as in ffmpeg.
2. **Choose the stream.** Audio: the `stream`-th `si` in `in.Streams()` with
   `si.Type == ffgo.MediaTypeAudio`, in index order (`-map 0:a:N`). Video: a port
   of `map_auto_video` (`ffmpeg_mux_init.c:1603-1625`): score =
   `Width*Height + 5_000_000*(Disposition&DispositionDefault != 0)`, an attached
   picture scores 1, the first stream with a strictly greater score wins. When
   none matches: `decode: <path> has no audio stream N` or `… no video stream`.
3. **Seek like `-ss`.** `origin := ms(x)*1000 + in.StartTime().Microseconds()`,
   where `ms(x)` is the integer parsed back from
   `strconv.FormatFloat(max(x,0),'f',3,64)` (today's `secs()` precision);
   `seekTS := origin`; if `!in.SeekToPTS()` (F2) and any stream has
   `si.VideoDelay > 0` (F3), `seekTS -= 3*1_000_000/23`; `_ = in.SeekTimestamp(seekTS)`,
   a failure logged at Debug and decoding continuing.
4. **Discard after the seek.** `in.Discard(si.Index)` (F4), preserving ffmpeg's
   default-stream choice for the seek.
5. **Open the stream decoder.** `sd, err := in.NewStreamDecoder(si.Index, &ffgo.StreamDecoderConfig{Options: opts})`
   (F1): `opts["threads"] = "auto"` when `d.Threads == 0`, else the number; for
   video also `opts["flags"] = "+unaligned"`; for Frames also
   `opts["skip_frame"] = "nokey"`.
6. **Shift timestamps.** `off := ffgo.RescaleQ(-origin, ffgo.NewRational(1, 1000000), si.TimeBase)`
   (F8). For each packet of `si.Index` from `in.ReadPacket()`, add `off` to pts
   and dts when not `avutil.AV_NOPTS_VALUE`, through
   `avcodec.SetPacketPTS/SetPacketDTS(p.Raw(), …)` (existing exports). Other
   streams' packets are skipped. `ctx.Err()` is checked before every `ReadPacket`.
7. **Decode loop.** `sd.Send(p)`; on `ffgo.ErrAgain` drain `sd.Receive()` until
   `ErrAgain` and resend. At end of file `sd.Send(nil)` (repeat while `ErrAgain`,
   draining), then `Receive` until `io.EOF`. Received frames are borrowed until
   the next `Receive`.
8. **Filter graph.** Built lazily from the first frame, as ffmpeg does, with
   that frame's side data handed to the buffer source (F17): ffmpeg clones
   every `AV_SIDE_DATA_PROP_GLOBAL` entry plus `AV_FRAME_DATA_DOWNMIX_INFO`
   into the input filter (`fftools/ffmpeg_filter.c:2255-2289`) and sets them
   on the buffer source through `av_buffersrc_parameters_set`
   (`:1877-1878`, `:1990-1991`), because `aresample` reads the downmix levels
   at init, not per frame (the comment at `:2278-2280`). On a change of frame
   parameters (size, pixel/sample format, layout, rate, colorspace, range, or
   the downmix info) the old graph is drained with `Flush()` and a new one with
   the same chain is built (`-reinit_filter 1`). Every output `*ffgo.Frame` is
   copied out and `Free()`d; every ffgo object is `Close()`d with `defer`,
   because only `FilterGraph` has a finalizer.

#### 7.2.3 Audio (11025 Hz mono PCM s16)

- **Timestamps.** `f.SetPTS(ts.next(f.PTS(), si.TimeBase, rate, f.NumSamples()))`,
  `rate` = `ffgo.WrapFrame(f, ffgo.MediaTypeAudio).SampleRate()`. `ts` is the
  faithful port of `audio_samplerate_update` and `audio_ts_process`, from
  `last_frame_tb = 1/1`, `last_frame_pts` and `last_filter_in_rescale_delta` =
  `AV_NOPTS_VALUE`, `last_frame_sample_rate = 0`, using `ffgo.RescaleQRnd`
  (`RoundUp`) and `ffgo.RescaleDelta` (F8). A frame without pts takes the
  predicted pts.
- **Graph** (F6):
  ```go
  ffgo.NewFilterGraph(ffgo.FilterGraphConfig{
      SampleRate: rate,
      Layout:     f.ChannelLayout(),
      SampleFmt:  ffgo.SampleFormat(f.Format()),
      TimeBase:   ffgo.NewRational(1, int32(rate)),
      InputSideData: f, // F17: global side data plus DOWNMIX_INFO, as ffmpeg forwards it
      Filters:    "atrim=starti=0:durationi=" + secs6(ms(durS)*1000) +
                  ",aformat=sample_fmts=s16:sample_rates=11025:channel_layouts=mono",
  })
  ```
  Without the downmix info, `aresample` folds 5.1 to mono with swr's defaults
  (centre and surround at -3 dB, no LFE) instead of the levels the AC-3/E-AC-3
  decoder attaches to every surround frame, so the PCM, and the fingerprint,
  would differ for every surround AC-3/E-AC-3 lead track (by the reviewer's
  read-only count about half the live library's video files).
  `secs6` prints `S.ffffff`, since `durationi` is an `AV_OPT_TYPE_DURATION`
  (`libavfilter/trim.c:325`) and a bare integer would read as seconds.
  `avfilter_graph_config` inserts the same default-option `aresample` ffmpeg
  gets; trimming happens at the input rate inside FFmpeg, so Go needs no
  partial-frame handling.
- **Output.** `g.Filter(&f)`; from each output frame `w := ffgo.WrapFrame(*o, ffgo.MediaTypeAudio)`
  append `w.Data(0)[:2*w.NumSamples()]` as little-endian int16.
- **Stop reading** only when the graph says so: push frames until
  `abuffersink` returns EOF for `atrim`'s end (`libavfilter/trim.c:183-184,205-207`
  ends output at `first_pts + duration`, where `first_pts` is the pts of the
  first sample passed, positive when the audio starts after the window origin),
  then `g.Flush()` drains `aresample`'s delay. A fixed stop at
  `round(durS*rate)` would truncate the window by `first_pts` samples whenever
  the track starts late.
- **Cap.** Over 64 MiB of output is `errTooLarge` (`decode: output over the cap`).

#### 7.2.4 Frames (1 fps, keyframes only, 128x72 gray)

- **Decoder options** `skip_frame=nokey` plus `flags=+unaligned` (F1): ffmpeg's
  `AVDISCARD_NONKEY`, not a packet key-flag filter, which keeps a different frame
  set on open-GOP H.264 and HEVC.
- **Timestamps.** `f.SetPTS(f.BestEffortTimestamp())` (F5), as `ffmpeg_dec.c:396`.
- **Graph.** `ffgo.NewFilterGraph(ffgo.FilterGraphConfig{…})` with `Width`,
  `Height` from `ffgo.WrapFrame(f, ffgo.MediaTypeVideo)`; `PixelFmt`
  `ffgo.PixelFormat(f.Format())`; `TimeBase` `sd.TimeBase()`; `SAR`
  `si.SampleAspectRatio`; `ColorSpace` `f.ColorSpec().Space` and `ColorRange`
  `f.ColorSpec().Range` (F7); `InputSideData` the first frame (F17, as for
  audio); `Filters`
  `"trim=starti=0,fps=fps=1:start_time=0,scale=128:72:flags=area,format=gray"`.
  The video path splits the chain at commas and each filter at its first `=`
  (`linkFilterChain`), so `fps` gets `fps=1:start_time=0` as written.
- **Output.** 72 rows of 128 bytes from each output frame's `Data(0)`, stepping
  by `Linesize(0)`.
- **End of file.** Read to EOF, drain the decoder, `g.Flush()`; buffersrc closes
  at the last frame's `pts+duration` (`libavfilter/buffersrc.c:219-223`), the end
  timestamp ffmpeg sends for containers with packet durations (`ffmpeg_dec.c`
  `video_duration_estimate`). The worker's `padToEnd` is unchanged.

#### 7.2.5 Frame (one 320x180 RGB24 frame)

`flags=+unaligned` only (every frame from the seek keyframe is decoded);
`BestEffortTimestamp`; the same graph with
`Filters: "trim=starti=0,scale=320:180:flags=area,format=pix_fmts=rgb24"`, whose
colorspace and range (F7) make `vf_scale`'s YUV-to-RGB matrix and range
ffmpeg's. The first output frame is the answer (`-frames:v 1`): 180 rows of 960
bytes. With no output frame at EOF, return
`decode: frame at %.1fs of %s: 0 bytes`, keeping the length check
(`decode.go:94-96`). `textdet.FindStart` calls `Frame` repeatedly, each call
opening a new decoder, so no `StreamDecoder.Flush` is needed.

#### 7.2.6 Equivalence and its limits

**Byte-identical by construction:** demuxer options and seek point; the same
packets offset by the same integer; decoder options (`threads` changes only
speed); timestamps (`best_effort`, `audio_ts_process`); buffer parameters and
the side data ffmpeg forwards to them (F17); filter chains and auto-inserted
converters with default swr/sws options.

**Not replicated, accepted:** autorotate on a display matrix (phone footage);
container cropping (Matroska PixelCrop / `AV_PKT_DATA_FRAME_CROPPING`); MPEG-TS
pts wrap and discontinuity correction (`ts_fixup`, `fftools/ffmpeg_demux.c:240-300`)
and `DECODER_FLAG_TS_UNRELIABLE`, which changes `video_duration_estimate`
(`ffmpeg_dec.c:285-342`) and so the end pts the Frames graph closes at. These
are **not** absent from the library: by the reviewer's read-only count it holds
9 `.ts` files (and 73 `.avi`) beside 10,703 `.mkv` and 1,172 `.mp4`. The
divergence on MPEG-TS is accepted rather than ported, because only the Frames
end-of-file pts and wrap-crossing windows can differ and TS sources are a
fraction of a percent; the library parity run (§7.2.10) must include every
`.ts` file, so OD20's threshold counts them.

#### 7.2.7 Fingerprint cache and `AnalyzerVersion`

- **Version the cache key now.** `const FingerprintVersion int32 = 2` in
  `pkg/segments` (1 standing for the unversioned keys the CLI decoder wrote);
  `app/segments/worker/cache.go:45` becomes
  `name := segments.FingerprintKey(f.ProbeHash, which)`, i.e.
  `<probeHash>.<which>.v2`. The old keys came from an unpinned BtbN build that
  cannot be reproduced, and a season task fingerprints every sibling, so mixed
  decoders inside one season's alignment would otherwise be silent. The cost is
  one re-decode of each season's windows at its next task. Old objects age out
  under the store's `MaxAge` of 90 days (`pkg/events/topology.go:926`).
- **Raise `FingerprintVersion`** whenever decode output can change: this
  package's code, the fork's decode path, or the native image's libavcodec minor.
- **`AnalyzerVersion` (3, `pkg/segments/segments.go:34`) is not raised by this
  work.** Raising it re-analyzes every movie and episode file (`merge.go` `Due`),
  invalidates `clustarr-segments` records and changes result Msg-Ids. It is
  raised only if the library parity run crosses the owner's threshold (§13 OD20).
- **Names unchanged:** consumer `segmentarr-analyze`, stream `CLUSTARR_WORK_SEGMENTARR`.

**Amended 2026-10-07 (NATS research; `nats-object-store.md` §1.3 G6, §6.8).** The
research found the live cache key unversioned (G6) and proposed a second scheme,
`events.FingerprintKey` naming `<probeHash>.fp<N>.<start|end>`. This section
already versions the key, so that scheme is not adopted: there is one key
builder, `segments.FingerprintKey`, and one constant, `segments.FingerprintVersion`.
What changes is when they land and what raises the constant:

- **Land the builder now, at version 1** (plan W4.111, Wave 4f). Version 1 is the
  CLI decoder's output and keeps its legacy name, `<probeHash>.<which>`, so
  nothing is recomputed before the decoder changes; `FingerprintKey` appends
  `.v<N>` only from version 2 on. W7.5 then raises the constant to 2 with the
  ffgo decoder, exactly as above, and the names become `<probeHash>.<which>.v2`.
- **Raise `FingerprintVersion` also** when the window lengths (`startWindow`,
  `endWindow`) or the Chromaprint configuration change, not only decode output:
  a cached fingerprint of another window is as wrong as one of another decoder.
  A detection-only change raises `AnalyzerVersion` alone and reuses the cache.
- **The bucket says so.** `clustarr-fingerprints` carries bucket metadata
  (`ObjectStoreSpec.Metadata`, artwork design §B.1 as amended):
  `clustarr.io/key-scheme: "<probeHash>.<start|end>[.v<FingerprintVersion>]"`.
  Fingerprint objects carry no per-object metadata and no links; the reaper's
  orphan-chunk purge covers this bucket too (§5.13 as amended), since two
  markers pods racing one `Put` leak a copy until the 90-day `MaxAge`.

#### 7.2.8 Cancellation, threads, memory, concurrency

- **Cancellation.** Every loop checks `ctx` between packets, sends, receives and
  filter calls. The whole body runs under `ffruntime.Do`: if `ctx` ends and the
  body has not returned within 5 s (the old `WaitDelay`), `Do` returns
  `decode: <ctx.Err()>` and leaves the goroutine to free its FFmpeg objects when
  the blocked read returns. Decode does not use `WithInterrupt` (F16): a read
  blocked in the kernel on an NFS hard mount ignores it, as it ignored SIGKILL of
  the old child, and path-based open (not custom IO) keeps a cancelled read from
  surfacing as a clean EOF. Error text comes from the returned `avutil.Error`
  (`av_strerror`); `TestAFailureCarriesFFmpegsMessage`'s "No such file" holds.
- **Threads.** `--decode-threads` 0 resolves in `main` to
  `max(1, runtime.GOMAXPROCS(0)/slots)`, following the cgroup quota. `"auto"`
  follows `av_cpu_count()` (the node's cores) and is used only when a library
  caller passes 0. Decoder threading never changes output.
- **Memory.** Decoder pools, filter frames and ORT arenas live in the C heap,
  outside `GOMEMLIMIT`; an OOM now kills the whole worker and every in-flight task
  is redelivered. Mitigations: capped decode threads; 1 slot by default; and
  markers' `GOMEMLIMIT` at 50% of `limits.memory` (§10.2.1), since the Go heap is
  about 50 MB per task (a 600 s window of PCM is 13 MB; 900 gray frames 8 MB).
- **Concurrency.** Each call owns its objects; ffgo's `Decoder` and
  `StreamDecoder` hold their own mutexes; there are no per-call purego callbacks
  (path open, no IO callbacks). The log callback is process-wide (§7.4), and
  markers never captures it, because with more than one slot lines cannot be
  attributed.

#### 7.2.9 Decode-related parts of `cmd/markers`

Startup (§3.7) runs `textdet.NewONNX` (soft), then `ffruntime.Load()` and
`ffruntime.Require(decode.Needs)` (failure exits `ExitMisconfigured` 3), then
`ffruntime.RouteLog(log)` and the GaugeFunc `clustarr_ffgo_abandoned_calls`;
`run` exits `ExitDrained` on `ctx.Done()` or `ExitRetriable` on
`ffruntime.Wedged()`.

- **`decode.Needs`:** demuxers `matroska`, `mov`, `mpegts`, `avi`; filters
  `buffer`, `buffersink`, `abuffer`, `abuffersink`, `trim`, `atrim`, `fps`,
  `scale`, `format`, `aformat`, `aresample`; decoders `h264`, `hevc`,
  `libdav1d` (FFmpeg's native `av1` decoder is hardware-only, so a build
  without libdav1d would pass a check for `av1` and fail every AV1 decode),
  `mpeg2video`, `mpeg4`, `aac`, `ac3`, `eac3`, `dca`, `truehd`, `flac`, `opus`,
  `mp3`.
- **Self-check.** `markers --self-check` runs those steps with no NATS or env,
  prints a JSON report, exits 0 or 3; CI runs it on the built native image, as
  `squasharr-worker --self-check` does today.
- **Closure.** `pkg/segments` and `app/segments/worker` stop importing
  `api/catalog/v1alpha1`: local `segments.Kind`, `segments.Source` and result
  string constants replace the API ones; `Due` moves to `app/catalog/segmenting`;
  conversion happens at `segmenting/apply.go`, `segmenting/results.go` and
  `markers/handler.go` (now `app/catalog/worker/segmentresults` and
  `app/catalog/worker/markers`); a test holds the local constants equal to the
  API enums. `cmd/markers` then links no `k8s.io/api` or controller-runtime;
  `k8s.io/apimachinery` stays through `pkg/events/schema`.

#### 7.2.10 Tests

`pkg/segments/decode` (always run; `test/nativetest.Require` skips, or fails
under `CLUSTARR_REQUIRE_TOOLS=1`, when `ffruntime.Load` fails), on the committed
`test/data/mediainfo/sample_hevc_10bit.mkv` and `sample_h264_8bit.mp4`:

- kept: `TestAudioDecodesTheWindow`, `TestFramesOnePerSecondToTheEnd`,
  `TestFrameIsOneRGBFrame`, `TestAFailureCarriesFFmpegsMessage`;
- `TestAHungFFmpegIsKilledWithItsGroup` becomes `TestACancelledDecodeReturnsWithinTheGrace`
  (`ffruntime.Do` with a blocking body);
- `TestFramesDecodeKeyframesOnly` becomes a count (a test hook counts decoded
  frames, at most the fixture's keyframe count);
- `TestConcurrentDecodesAreRaceFree` under `-race`: 4 goroutines × 10 decodes of
  both fixtures.

`test/parity/segments` (build tag `parity`):

- **Reference.** Today's exec decoder (`decode.go` + `proc.go`) moves here
  verbatim as `execDecoder`; the production package never imports `os/exec`.
- **Gate.** Runs only when `ffmpeg` is on PATH and its `libavcodec` major.minor
  equals `ffgo.Version()` (`make native-assets` supplies such an ffmpeg, §10.1.6).
- **Generated fixtures (byte equality):** MKV, H.264 `-bf 3 -g 25` with AAC; MKV,
  HEVC 10-bit BT.709 and one BT.2020/PQ, with 5.1(side) AC-3; the same 5.1 AC-3
  encoded with non-default `-center_mixlev 0.5 -surround_mixlev 0.5` (so the
  decoder's `DOWNMIX_INFO` differs from swr's defaults and F17 is exercised);
  an MKV whose audio starts late (`-itsoffset 0.7` on the audio input, so
  `atrim`'s `first_pts` is positive); MP4; MPEG-TS with `-output_ts_offset 1.4`
  (no wrap or discontinuity).
- **Windows:** Audio at 0, mid-GOP and 50 ms after a keyframe (the 3/23 s edge);
  Frames from mid-file to end; Frame at 5 points.
- **On a mismatch** it prints, for Audio, the differing sample count, max |diff|
  and chromaprint Hamming distance; for Frames, the first differing frame and the
  counts.
- **Library mode.** `CLUSTARR_SEGMENTS_PARITY_FILES` names one media path per
  line, read-only, and must include every `.ts` file in the library (§7.2.6);
  both decoders run each file's start, end and credits work through
  `worker.Handler.analyze`, and every differing segment is printed, grouped by
  container. Its result decides §13 OD20.
- **Soak** (build tag `soak`): RSS from `/proc/self/statm` stays within 64 MiB
  over 300 decodes.

### 7.3 Embedded subtitle extraction on ffgo

#### 7.3.1 Package split

`pkg/subtitles/providers/embedded` stays pure: the manager side imports it
(`subtitlerequest/existing.go:145`, `providerset.prototype`,
`providerset.go:353-354`). `Config.FFmpeg` and `os/exec` go; it gains:

```go
type ExtractFunc func(ctx context.Context, path string, stream int) ([]byte, error)
// Config.Extract ExtractFunc   // nil: Download returns ErrNoExtractor
var ErrNoExtractor = errors.New("subtitles: embedded: no extractor in this process")
func IsTextCodec(codec string) bool // subrip, ass, ssa, webvtt, mov_text -- Search uses it too
```

`Download` validates `FetchID` as before, then returns
`cfg.Extract(ctx, cfg.Path, idx)` and the name `stream-<id>.srt`, both
unchanged. `Search`, `Capabilities` and `HIVerifiable` are unchanged.

New `pkg/subtitles/providers/embedded/native` (imports ffgo and `pkg/ffruntime`):

```go
const MaxOutputBytes = 8 << 20 // the remote providers' maxSubtitleBytes
var ErrTooLarge = errors.New("subtitles: embedded: extracted subtitle exceeds 8 MiB")
var ErrNotText  = errors.New("subtitles: embedded: not a text subtitle stream")
var Needs = ffruntime.Needs{Encoders: []string{"srt"}, Decoders: []string{"subrip", "ass", "ssa", "webvtt", "mov_text"}}
func Extract(ctx context.Context, path string, stream int) ([]byte, error)
```

`app/caption/providerset/build.Builder.FFmpeg` (`providerset.go:217-219, 405-409`)
becomes `Builder.Extract embedded.ExtractFunc`, passed into each
`embedded.Config`. Only `app/caption/agent` sets
`providers.Extract = native.Extract`; the manager never does (`Validate`,
`prototype` and `extractableStreams` never call `Download`).

#### 7.3.2 `native.Extract`, step by step (`ffmpeg -i P -map 0:N -c:s srt -f srt -`)

The body runs under `ffruntime.Do`.

1. **Open** with §7.2.2's `scan_all_pmts` options.
2. **Check the stream:** `si.Index == stream`, `MediaTypeSubtitle`, and
   `embedded.IsTextCodec(si.Codec)`, else `ErrNotText`.
3. **Discard and open.** `in.Discard(stream)` (F4);
   `tr, err := in.NewSubtitleTranscoder(stream, "srt")` (F10), `defer tr.Close()`.
   No seek.
4. **Offset.** `off := ffgo.RescaleQ(-in.StartTime().Microseconds(), µs, si.TimeBase)`;
   without `-ss`, ffmpeg's `ts_offset` is `-ic->start_time`.
5. **Read loop**, for each packet of `stream`, ctx checked each time: shift
   pts/dts by `off`; `ev, ok, err := tr.Transcode(p)`;
   `errors.Is(err, ffgo.ErrSubtitleDecode)` skips the packet (ffmpeg logs and
   continues, `ffmpeg_dec.c:660-668`); `ffgo.ErrSubtitleEncode` fails (ffmpeg's
   "Subtitle encoding failed" is fatal, `ffmpeg_enc.c:431-434`); `!ok` is no
   output (no event, pts unset, or empty encode; empty cues would be dropped by
   `subtitles.PostProcess` anyway).
6. **End of file.** `tr.Transcode(nil)` until `!ok` (decoder flush).
7. **Framing** in Go as `libavformat/srtenc.c:54-90` writes it:
   `s := ffgo.RescaleQ(ev.PTS, µs, ms)`, `d := ev.Duration/1000`; events with
   `d < 0` skipped ("Insufficient timestamps"); each event writes
   `"%d\n%02d:%02d:%02d,%03d --> %02d:%02d:%02d,%03d\n"` (index from 1, C
   truncating division), the payload, `"\n\n"`. Past `MaxOutputBytes` the result
   is `ErrTooLarge`.

**Why decode + encode, not stream copy or a Go port:** ffmpeg's `-c:s srt`
always re-encodes; every FFmpeg text decoder emits ASS events, and the `srt`
encoder converts them with `ff_ass_split_override_codes`, applying the stream's
ASS styles from `subtitle_header` (`libavcodec/srtenc.c:227-262`,
`ffmpeg_enc.c:298-316`). Copying subrip packets into an srt muxer would not
normalise tags and cannot convert ass, webvtt or mov_text; a Go port of srtenc
and ass_split would be a second implementation to keep in step. FFmpeg's encoder
keeps today's output exactly, for one shim function and one binding. Framing in
Go saves adding a subtitle stream to ffgo's `Muxer`; `PostProcess` re-parses with
astisub anyway.

**Caps:** 8 MiB of output; 1 MiB per encoded event (ffmpeg's encoder buffer,
`ffmpeg_enc.c:378`; an overflow is an encode error); the fetch task's ctx
deadline. Reading the whole file costs the same I/O as ffmpeg.

**Main's transcode sidecars are a different writer, on purpose.** Since the MP4
standard (main `71dc301d`) the transcode engine writes a file's text subtitles
beside its output in-process (`pkg/transcode/engine/sidecar.go`): SubRip copied
packet for packet into FFmpeg's `srt` muxer (ffgo's `ffmpeg -map 0:s:N out.srt`),
ASS copied into the `ass` muxer, and WebVTT, `mov_text` and plain text written as
SubRip by a Go writer that drops markup. It needs no subtitle encoder, so it
does not need F10. `native.Extract` keeps F10's decode and encode anyway: it
must reproduce `ffmpeg -c:s srt` byte for byte (the `parity` gate, §11.2), it
must turn ASS into SubRip for `subtitles.PostProcess`, and the srt encoder
normalises tags, which neither the copy nor the Go writer does. So one file's
`<stem>.en.srt` can differ in markup depending on which wrote it. Since an MP4
output carries no subtitle stream, only untranscoded files and image-subtitle
holds reach `native.Extract`. The transcode never replaces an existing name
(MP4 ruling R3). The fetch worker writes `subtitles.SidecarName` with a
replacing write (`fetch/search.go:445-458`), but only for a language the file
lacked when the search was planned, so it replaces a transcode's sidecar only
when a search was already in flight as the transcode placed the same name. The
downloaded subtitle then wins, which is what the search was for.

**Tests.** `native` tests use generated MKVs with subrip carrying `<i>`, ass with
an `Italic=-1` style, webvtt, and an MP4 with mov_text, asserting cue text and
italics and that `ErrTooLarge` fires when the test lowers the cap.
`TestDownloadExtractsTheStreamViaFFmpeg` becomes `TestDownloadUsesTheExtractor`
with a fake. Under the `parity` tag each output must equal
`ffmpeg -i x -map 0:N -c:s srt -f srt -` byte for byte.

### 7.4 `pkg/ffruntime`: the shared runtime gate

`pkg/ffruntime` imports only the standard library and ffgo;
`TestFFRuntimeImportsOnlyFFgo` holds its `go list -deps` to std,
`github.com/obinnaokechukwu/ffgo/...` and `github.com/ebitengine/purego/...`.

```go
const (
    AVCodecMajor = 63 // FFmpeg 9
    MinShimAPI   = 1  // FFSHIM_API_VERSION of ffgo v0.0.0-clustarr.13
    Grace        = 5 * time.Second
    MaxAbandoned = 4
)
type Report struct{ FFmpegMajor, ShimAPI int; AVUtil, AVCodec, AVFormat, ShimPath string } // paths as loaded
type Needs struct{ Demuxers, Muxers, Decoders, Encoders, Filters []string }
var ErrUnavailable = errors.New("ffruntime: FFmpeg is unavailable")
var ErrWedged = errors.New("ffruntime: FFmpeg calls are stuck; restart the process")

func Load() (Report, error)        // once per process
func Require(n Needs) error        // avformat.FindInputFormat, avformat.AllocOutputContext2 (freed at once), avcodec.FindDecoderByName/FindEncoderByName, avfilter.GetByName
func RouteLog(l *slog.Logger)      // one process-wide FFmpeg log callback
func Capture(sink func(ffgo.LogLevel, string)) (release func())
func Mute() (release func())
func Do(ctx context.Context, f func(context.Context) error) error
func Abandoned() int               // calls abandoned and still running; decremented when one returns
func Wedged() <-chan struct{}
func Healthz(*http.Request) error  // healthz.Checker: error once wedged
```

- **`Load`** does what `app/squash/worker/inprocess/inprocess.go:45-56` and
  `pkg/transcode/selfcheck/selfcheck.go:112-124` each do today, plus the shim API
  check, each failure wrapped in `ErrUnavailable` naming what is missing:
  `ffgo.Init()`; libavcodec major 63; `ffgo.Diagnose().ShimLoaded`;
  `ffgo.ShimAPIVersion() >= MinShimAPI` (F12; an older shim would read FFmpeg 9
  structs at FFmpeg 4-7 offsets). On success it calls
  `ffgo.SetLogLevel(ffgo.LogWarning)`, which F13 makes effective under a
  callback. The first result is kept (a failed `bindings.Load` cannot be retried).
- **Logging.** One trampoline, installed on the first use of `RouteLog`,
  `Capture` or `Mute`: lines at `LogError` or worse go to slog at Warn as
  `ffmpeg`, rate-limited to 10/s with a burst of 20 unless a `Mute` is held;
  every line goes to each `Capture` sink. `pkg/transcode/engine/logtail.go:57-64`
  `install` becomes `ffruntime.Capture(t.add)`;
  `app/squash/worker/inprocess/measure.go:165-166` becomes `ffruntime.Mute()`.
  This ends the pattern where a run restored `SetLogCallback(nil)` and FFmpeg
  fell back to stderr at INFO.
- **`Do`** runs `f` on its own goroutine; when `ctx` ends it waits up to `Grace`
  and returns an error wrapping `ErrAbandoned` and `ctx.Err()`. `Abandoned()`
  counts calls **still outstanding**: an abandoned goroutine that later returns
  (a slow NFS read that completed) decrements it, so four brief stalls over a
  pod's life never add up to a restart. When the outstanding count reaches
  `MaxAbandoned` it closes `Wedged()` and refuses new calls with `ErrWedged`.
  Callers record an `ErrAbandoned` result as non-transient (§6.6). It replaces
  the process-group kill, which could not reap a child in D state either.
- **`Report`'s paths** are the files ffgo actually mapped (read from
  `/proc/self/maps` after `Init`). `test/nativetest.Require` fails under
  `CLUSTARR_REQUIRE_TOOLS=1` when `AVCodec` is not under
  `$CLUSTARR_NATIVE_ASSETS/lib` (§10.1.6), so a host's system FFmpeg can never
  stand in for the pinned build the parity oracles run.

**Consumers, and behaviour when `Load` or `Require` fails** (every one hard-gates;
the native image always carries FFmpeg, so a failure means a broken image):

| Consumer | On failure | Notes |
| --- | --- | --- |
| `cmd/transcode` | `WorkerExitRetriable` | as `inprocess.New` today; `inprocess.New` becomes `ffruntime.Load()` + `Require` of its class; `selfcheck.Check` takes its first fields from `Load`. Every class's `Needs` covers what standard Version 2 writes (main `b0bd01ee`, `71dc301d`): encoders `libx265`, `aac` and **`ac3`** (every surround track that is not E-AC-3 or AC-3), muxers **`mp4`**, **`srt`** and **`ass`** (the sidecars). Main's `selfcheck.Needs` (selfcheck.go:56-60) still lists only `libx265` and `aac` plus the GPU encoders and filters, so on main today an FFmpeg build without `ac3` passes the self-check and fails the first surround encode; `ebbb2322`'s `TestTheAC3EncoderOpensAtFivePointOne` proves the encoder only in a test |
| `cmd/markers` | `ExitMisconfigured` | §3.7 |
| `cmd/agent --domain import` | exit 1 before the manager starts | `native.New()` (§6.6) |
| `cmd/agent --domain caption` | exit 1 before the manager starts | requires `native.Needs` |

Both agent domains register the process-level healthz check `ffgo` =
`ffruntime.Healthz` (§3.3). Agent domains that never call ffgo still map FFmpeg
at start through the package-init dlopen (§7.1): accepted, it costs mapped
pages, not binary size.

### 7.5 ffgo fork additions: `v0.0.0-clustarr.13`

- go.mod pins `.12` (go.mod:218, since main's `ebbb2322`; the worktree pinned `.9`
  and main `.11` when this section was drafted). `.10`, `.11` and `.12` are taken
  (published; `.10` is used by cluster-plex, `.12` by main's MP4 standard), so the
  next tag is `.13`.
- One branch, **`clustarr/unify-media`**, in the worktree
  `/home/appkins/src/mediactl/ffgo-unify`, cut from `.12` = `a184557` (which carries
  `.10`'s muxer-to-writer API, purego v0.11.1 and `.12`'s packet and
  codec-parameter setters). One commit per addition, each with tests against
  `testdata/` and generated clips.
- Rebuild the shim with `./shim/build.sh prebuilt` against FFmpeg 9 headers and
  commit `shim/prebuilt/linux-amd64/libffshim.so`, as `9c292f0` did.
- Run `go test ./...`, then `git tag -a v0.0.0-clustarr.13` locally. Nothing is
  pushed (§13 OD18).
- Every new struct read is a shim layout entry (`FFSHIM_FIELD`) with a Go
  fallback; `FFSHIM_API_VERSION` (F12) is raised whenever the shim gains a symbol
  or a layout field.

| ID | Addition | C/shim change | Used by |
| --- | --- | --- | --- |
| F1 | `StreamDecoderConfig.Options map[string]string`: each entry through `avutil.OptSet` on the codec context before `avcodec_open2`; an unknown name is an error | none | decode: `skip_frame`, `flags`, `threads` |
| F2 | `(*Decoder).SeekToPTS() bool` (`iformat->flags & AVFMT_SEEK_TO_PTS`) | layout `AVInputFormat.flags` | decode |
| F3 | `StreamInfo.VideoDelay int` | layout `AVCodecParameters.video_delay` | decode |
| F4 | `(*Decoder).Discard(keep ...int) error`: `AVStream.discard = AVDISCARD_ALL` (48) for the others | layout `AVStream.discard` | decode, embedded/native |
| F5 | `Frame.BestEffortTimestamp() int64` | layout `AVFrame.best_effort_timestamp` | decode |
| F6 | `FilterGraphConfig.Layout string`: abuffer gets `channel_layout=<Layout>` (`channels=N` for an "N channels" layout) and `time_base=` when `TimeBase` is set; `Layout` alone satisfies the audio check | none | decode |
| F7 | `FilterGraphConfig.ColorSpace ColorSpace`, `ColorRange ColorRange`: buffer args `colorspace=%d:range=%d` | none | decode |
| F8 | `type Rounding` with `RoundNearInf`/`RoundUp`/`RoundDown`/`RoundPassMinMax`; `RescaleQRnd` over a binding of `av_rescale_rnd` (scalar arguments); `RescaleQ`; `RescaleDelta` (port of `av_rescale_delta`) | binding only | decode, embedded/native |
| F9 | `NewSubtitleDecoder` sets `pkt_timebase`, as `StreamDecoder` does, so `AVSubtitle.pts` and `end_display_time` are filled (`libavcodec/decode.c:962-979`) | none | fixes the existing API |
| F10 | `(*Decoder).NewSubtitleTranscoder(index int, encoder string) (*SubtitleTranscoder, error)`, `Transcode(*Packet) (SubtitleEvent, bool, error)`, `Close() error`; `SubtitleEvent{PTS, Duration int64 /*µs*/; Data []byte}`; `ErrSubtitleDecode`, `ErrSubtitleEncode` (internals below) | shim function `int ffshim_codecctx_copy_subtitle_header(void *dst, const void *src)`; binding `avcodec_encode_subtitle` | embedded/native |
| F11a | profile and level: `avformat.GetCodecParProfile`, `GetCodecParLevel`; `avcodec.ProfileName(id CodecID, p int32) string` (NULL → ""); `StreamInfo.Profile`, `StreamInfo.Level` (int); `ffgo.ProfileName(id CodecID, p int) string`; `ffgo.ProfileUnknown = -99` | `FFSHIM_FIELD(AVCodecParameters, profile)`, `(…, level)`; binding `avcodec_profile_name` | probe |
| F11b | sample size: `StreamInfo.BitsPerRawSample`, `StreamInfo.BitsPerCodedSample` (int); `ffgo.BitsPerSample(id CodecID) int` (`av_get_bits_per_sample`, as ffprobe prints it) | `FFSHIM_FIELD(AVCodecParameters, bits_per_raw_sample)`, `(…, bits_per_coded_sample)`; binding `av_get_bits_per_sample` | probe |
| F11c | `StreamInfo.FieldOrder FieldOrder`; `FieldOrder.String()` gives ffprobe's names "progressive", "tt", "bb", "tb", "bt", and "" when unknown | `FFSHIM_FIELD(AVCodecParameters, field_order)` | probe |
| F11d | `StreamInfo.Color ColorSpec`, reusing the existing type and its `Names()` | `FFSHIM_FIELD(AVCodecParameters, color_range)`, `…color_primaries`, `…color_trc`, `…color_space` | probe |
| F11e | `avformat.GetStreamRFrameRate`, `GetStreamDuration`, `GetStreamStartTime`; `StreamInfo.RealFrameRate Rational`, `StreamInfo.StartTime int64`; `StreamInfo.Duration` filled, AV_NOPTS_VALUE kept as is | `FFSHIM_FIELD(AVStream, r_frame_rate)`, `(…, duration)`, `(…, start_time)` | probe |
| F11f | `StreamInfo.ChannelOrder ChannelOrder` with `ChannelOrderUnspec` (0), `ChannelOrderNative`, `ChannelOrderCustom`, `ChannelOrderAmbisonic` | `FFSHIM_FIELD(AVCodecParameters, ch_layout.order)` | probe |
| F12 | `ffgo.ShimAPIVersion() int` (0 when the symbol is missing) | shim function `int ffshim_api_version(void)`; `#define FFSHIM_API_VERSION 1` in `ffshim.h` | ffruntime |
| F13 | `internal_log_callback` returns early when `level > av_log_get_level()`, before `vsnprintf` | C change in `ffshim.c:33-47` | ffruntime; stops a purego callback per debug line |
| F14 | rebuilt `shim/prebuilt/linux-amd64/libffshim.so` | build | local tests through the module's prebuilt search path |
| F15 | chapter timestamps: `Chapter.TimeBase Rational`, `Chapter.StartTS`, `Chapter.EndTS int64`; `Start`/`End` computed with `math/big` so they cannot overflow | none (fields exist) | probe |
| F16 | `ffgo.WithInterrupt(done <-chan struct{}) DecoderOption`: `openInputOnce` allocates the context with `avformat.AllocContext()` and installs the callback before `avformat_open_input`; one process-wide purego callback (one `NewCallback`, never per call) looks up `opaque` in a registry | shim function `void ffshim_formatctx_set_interrupt(void *ctx, void *cb, void *opaque)` | probe (not decode, §7.2.8) |
| F17 | `FilterGraphConfig.InputSideData *Frame`: before `avfilter_graph_config`, the buffer source (audio and video) gets that frame's side data the way `ffmpeg(1)` forwards it, every entry whose descriptor has `AV_SIDE_DATA_PROP_GLOBAL` plus `AV_FRAME_DATA_DOWNMIX_INFO` (`fftools/ffmpeg_filter.c:2255-2289`), through `AVBufferSrcParameters.side_data`; ffgo builds its graphs from an args string only (`filter_graph.go:361-399`) and has no such path today | shim function `int ffshim_buffersrc_set_side_data(void *src_ctx, const void *frame)`: allocates `AVBufferSrcParameters`, clones the selected entries with `av_frame_side_data_clone`, calls `av_buffersrc_parameters_set`, frees; the same pattern as `ffshim_buffersrc_set_hw_frames` (`shim/ffshim.c:772-780`) | decode (audio downmix levels, §7.2.3) |

**F10 internals.** The decoder is opened from codecpar with `pkt_timebase` set.
The encoder comes from `FindEncoderByName` with `time_base` 1/1000000, the
decoder's `subtitle_header` copied (`ffmpeg_enc.c:298-316`), and `avcodec_open2`.
One 32-byte `AVSubtitle` and one 1 MiB output buffer are allocated through
`av_malloc`. Per packet `Transcode` decodes (a `nil` packet sends an allocated
empty packet as the flush), reads the event's original `pts` and
`end_display_time`, folds `start_display_time` into `pts` for the encode only
(`ffmpeg_enc.c:416-420`), calls `avcodec_encode_subtitle` then
`avsubtitle_free`, and returns `ok == false` when there is no output, the pts is
unset, or the encoder returned 0 bytes.

The shim functions of F10, F12, F16 and F17 all ship in the first versioned
shim, so `FFSHIM_API_VERSION 1` covers them and `MinShimAPI` stays 1.

**Decided against** (from the prior mapping's list): resampler partial frames
and a looping `Flush` (audio goes through ffmpeg's own graph);
`StreamDecoder.Flush` (every call opens a fresh decoder); passing `avcl` to the
log callback (errors come from return values); making ffgo's package-init dlopen
lazy (it changes semantics for every ffgo user, for no gain once the version
scripts are checked). An `AVIOInterruptCB` is added (F16) for the probe's 45 s
task bound, not for decode.

### 7.6 Consuming the fork (and par2go) before their tags are pushed

1. **go.mod** (the controlling session only, serially, once; never
   `go mod tidy`, never from a parallel agent):
   - `go mod edit -replace=github.com/obinnaokechukwu/ffgo=../ffgo-unify`.
   - purego v0.11.1, which the fork's `go.mod` requires, is already on main
     (`b3b077a2`, which also replaces ffgo with the published `.11`, and
     `ebbb2322` with `.12`); after the R14 rebase this edit only changes that
     replace to `../ffgo-unify`. If the replace
     lands before the rebase, add `-require=github.com/ebitengine/purego@v0.11.1`
     and record `go.sum` with `GOPROXY=off go mod download github.com/ebitengine/purego`
     (v0.11.1 is in `~/go/pkg/mod`). `onnxruntime-purego` uses only `Dlopen`,
     `RegisterFunc` and `RegisterLibFunc`, all present in v0.11.1; its `textdet`
     tests prove the bump.
   - `go mod edit -require=github.com/mediactl/par2go@v0.0.0-00010101000000-000000000000 -replace=github.com/mediactl/par2go=../par2go`
     for §8 (par2go's own module requires purego v0.11.1 too).
   - `../ffgo-unify` and `../par2go` resolve to
     `/home/appkins/src/mediactl/{ffgo-unify,par2go}` from both `clustarr` and
     `clustarr-unify`. Only this branch carries the replaces.
2. **Dockerfiles.** Every image whose build stage resolves this module needs
   `/ffgo-unify` (the replace `../ffgo-unify` seen from `WORKDIR /src`) and
   `/par2go`, because a directory replace needs the replacement's
   `go.mod` even for binaries that never link it: `images/Dockerfile.clustarr`,
   `images/Dockerfile.native` and `images/Dockerfile.e2e-fixtures`. Each gains
   stages `FROM scratch AS ffgo` and `FROM scratch AS par2go` before its build
   stage, and `COPY --from=ffgo / /ffgo-unify/` and `COPY --from=par2go / /par2go/`
   before `go mod download`. A BuildKit `--build-context ffgo=…` overrides a
   stage of the same name; without the flag the stage is empty and harmless. In
   the native build, `go list -m -f '{{.Dir}}' github.com/obinnaokechukwu/ffgo`
   (today `Dockerfile.transcoder:64`) resolves to `/ffgo-unify`, so `libffshim.so` is
   compiled from the fork's own `shim/ffshim.c` (`:106-108`) and always matches
   `MinShimAPI`. An image-build check in the `staging` stage, after
   `stage.sh` has run, fails the build if BtbN's libraries ever export C++
   symbols next to ONNX Runtime:
   `if nm -D --defined-only /staging/usr/lib/libav*.so.* /staging/usr/lib/libsw*.so.* | grep -q ' _Z'; then echo 'FFmpeg exports C++ symbols' >&2; exit 1; fi`
   (the `grep -q … && exit 1` form would exit 1 on success, as the last
   command of a `RUN`).
3. **Makefile: clean, pinned contexts.** A `--build-context` directory ships
   its uncommitted files and build output, and `../ffgo-unify` and `../par2go` are
   working trees (during review ffgo carried an uncommitted `shim/build.sh`
   change and par2go's HEAD moved twice; main's MP4 work then committed and
   tagged `.12` in `../ffgo` itself, which is why the fork moved to its own
   worktree, `../ffgo-unify`; par2go's
   tree holds a 251 MB `shim/build/` with a CMake cache recorded at host paths,
   which makes the image's CMake refuse to configure). So the Makefile never
   passes the working trees. `FFGO_DIR ?= ../ffgo-unify`, `PAR2GO_DIR ?= ../par2go`,
   `FFGO_REF ?= v0.0.0-clustarr.13`, `PAR2GO_REF ?= 1bd94eb` (the par2go basis,
   until it is tagged); target `contexts` refuses unless
   `git -C $(FFGO_DIR) describe --exact-match --dirty` prints exactly
   `$(FFGO_REF)` and `git -C $(PAR2GO_DIR) rev-parse HEAD` resolves to
   `$(PAR2GO_REF)` with `git -C $(PAR2GO_DIR) status --porcelain
   --untracked-files=no` empty, then exports each with
   `git -C <dir> archive <ref> | tar -x -C $(CONTEXTS)/<name>` into
   `$(CONTEXTS) ?= $(TMPDIR)/clustarr-contexts-$(USER)` (a scratch path per
   user, replaced atomically). Every `docker build` gets
   `--build-context ffgo=$(CONTEXTS)/ffgo` and
   `--build-context par2go=$(CONTEXTS)/par2go` while go.mod carries the local
   replace (§10.1.6), and passes `--build-arg FFGO_COMMIT=…` and
   `PAR2GO_COMMIT=…`, which the images write into their `SOURCE` notices
   (§8.7) and `hack/image-checks.sh notices` prints. The exports carry no
   `shim/build/`, so the image builds par2cmdline-turbo from a fresh clone and
   the Go stages copy kilobytes, not 251 MB. ffgo is 4.6 MB.
4. **CI** cannot resolve `../ffgo-unify` or `../par2go`, so this branch stays local, as
   R14 requires. `ci.yml` fails with a named cause on a local replace (§10.1.7);
   that red CI is the guard against merging a directory replace.
5. **After the owner's OK** (§13 OD18, OD19): push `clustarr/unify-media` and
   `v0.0.0-clustarr.13` to `github.com/mediactl/ffgo`; push par2go per its plan's
   Task 9; `go mod edit -replace=github.com/obinnaokechukwu/ffgo=github.com/mediactl/ffgo@v0.0.0-clustarr.13`
   and replace par2go's directory with its tag; `go mod download` both; commit
   `go.mod` and `go.sum` path-scoped. The empty stages stay; the Makefile stops
   passing the contexts.

### 7.7 Guards owned by this section

- **`cmd/markers` closure** (§4.5.4): no `os/exec`, `k8s.io/client-go`,
  `sigs.k8s.io/controller-runtime`, `k8s.io/api`, `pkg/k8s` or `pkg/obs`;
  requires ffgo, `pkg/ffruntime` and `github.com/shota3506/onnxruntime-purego/onnxruntime`.
- **Exec** is the repo-wide AST guard (§4.5.6), whose scope covers
  `pkg/segments`, `app/segments`, `cmd/markers`,
  `pkg/subtitles/providers/embedded` and `pkg/ffruntime`.
- **`cmd/manager` and `cmd/ui`** never list `pkg/ffruntime`,
  `pkg/subtitles/providers/embedded/native`, `pkg/segments/decode`, ffgo or purego
  (§4.5.1, §4.5.2).
- **Shim pin.** `TestShimAPIMatchesTheFork` (`nativetest.Require`) asserts
  `ffgo.ShimAPIVersion() == ffruntime.MinShimAPI` against the module's prebuilt shim.
- **Self-check.** `TestMarkersSelfCheckNeedsNoClusterEnvironment` mirrors
  `cmd/squasharr-worker/main_test.go`'s self-check test.

---
## 8. PAR2 as a library

**Decision.** PAR2 verify and repair no longer exec the `par2` program.

- **Library: par2go.** `github.com/mediactl/par2go` (package `par2`) is the
  owner-approved design on main (`docs/superpowers/specs/2026-10-06-par2go-design.md`,
  plan `docs/superpowers/plans/2026-10-06-par2go.md`), being built in
  `/home/appkins/src/mediactl/par2go`. It wraps par2cmdline-turbo's C++ library
  (nzbgetcom fork) behind an `extern "C"` shim, `libpar2shim.so` (`p2_*` ABI,
  exporting nothing else), loaded with purego v0.11.1, no cgo, C never calling
  Go. This section is the "clustarr integration spec" par2go's §8 defers to, and
  answers its three open questions: the usenet engine's binary (the agent, §3),
  crash isolation (a child process, §8.4), and the mapping onto `Par2Result` and
  `status.engineFailureReason` (§8.5).
- **Child process.** The usenet engine runs every repair in a short-lived child
  of its own binary, `agent par2-repair`, through the new clustarr package
  `pkg/par2child`. A deadline cancels the run through par2go's cancel, and SIGKILL
  follows after a 30 s grace. A crash or OOM inside par2's C++ kills one repair,
  never the engine (R2 amendment; §13 OD21).
- **Go interface.** `pkg/download/usenet.Par2Runner` becomes the
  `usenet.Repairer` interface, with today's verdicts: AllCorrect, Repaired,
  BlocksNeeded, failed.
- **Image and tests.** The native image carries `/usr/lib/libpar2shim.so` and no
  `par2` executable. `make native-assets` builds the same library for tests, the
  way `make pg-assets` supplies Postgres.

The section draft designed and prototyped an in-tree equivalent (`native/par2shim`
with a `par2shim_*` ABI, a `pkg/par2` loader, `PAR2SHIM_DIR`; its library file
happened to share par2go's current name, `libpar2shim.so`, but not its ABI).
par2go landed on main the same day with the same upstream pin, the same
polled-status, no-callback design and the same `basepath` workaround, so
clustarr adopts par2go and keeps only the integration. The prototype's results
(§8.3.2) stand as evidence that the approach works on this host.

### 8.1 What exists today (verified at `7791f1f5`)

**`Par2Runner`.** `Par2Runner.Repair(ctx, dir, indexFile)`
(`pkg/download/usenet/repair.go:117-189`) runs `par2 r -q -- <index> <extras…>`
with `cmd.Dir = dir` (`:138-140`). On cancel it kills the process group
(`Setpgid`, SIGKILL of `-pid`, `WaitDelay` 5 s, `:145-147`). Output goes into a
1 MiB `tailWriter` ring (`:154-156`, `:326-348`). `Available()` is
`exec.LookPath` (`:92-108`); a missing binary gives `ErrPar2Unavailable` (`:52`).

**Verdicts are parsed from text** (`:77-82`, `:165-188`): `Repair complete` gives
`Repaired`; `All files are correct|Repair is not required` gives `AllCorrect`;
`You need (\d+) more recovery blocks` gives `BlocksNeeded`; `Repair is not
possible`, or `BlocksNeeded>0`, gives `ErrRepairFailed`; any other non-zero exit
gives `ErrRepairFailed` wrapping the exit error; exit 0 with no recognised line
gives `AllCorrect`.

**Extra files.** `extraFiles(dir, index)` (`:254-267`) passes every other regular
file, sorted: the 2026-09-24 fix for obfuscated names.

**The caller, `job.repair`** (`client.go:1094-1136`): skips repair when there is
no index or `failedArticles()==0`; returns `ErrPar2Unavailable` when
`!Available()`; runs under `context.WithTimeout(par2Deadline(total))`, 30 min +
1 min/GB (`repair.go:205-207`); turns `repairCtx.Err()!=nil && ctx.Err()==nil`
into `ErrPar2Timeout`; after a repair calls `removePar2Backups`
(`repair.go:222-241`) and `adoptRepairedSet` (`rename.go:480+`). The
`Par2Result` is discarded, so `BlocksNeeded` is unused.

**After the repair.** `postProcess` tolerates `ErrRepairFailed` when no failed
article lies inside the archive set (`client.go:1025-1040`). `failureReason`
(`client.go:903-918`) maps `ErrRepairFailed` to `missingArticles` (a release
fault that blocklists the release); `ErrPar2Unavailable` and `ErrPar2Timeout`
fall to the default, `writeError` (local).

**Where the binary comes from.** `Config.Par2Path` (`client.go:99-100`) is never
set (`app/grab/engine/usenet.BuildClient`, `config.go:159-175`), so production
finds `par2` on PATH as `/usr/local/bin/par2` from
`images/Dockerfile.media:116-136,168` (the animetosho static zip, `v1.5.0`).

**Tests and CI that depend on the binary:** `par2 c`/`par2 r` in
`repair_test.go:35-137`, `rename_test.go:39,157,212,276`, `client_test.go:723-790`;
shell-script fakes in `client_test.go:323-327` (a fake that must not run) and
`client_test.go:367-376` (a fake that sleeps, so the deadline gives writeError);
CI downloads the release zip (`.github/workflows/ci.yml:36,121-136`).

**"PAR2 is an EXEC" is a recorded decision** at `hack/deps/deps.go:43-49`,
`repair.go:39-47` and `docs/research/download.md:272,318`.

**Line numbers moved on main.** `38db94e6` (below) added lines to `client.go`;
at main `80175fdc` the places this section cites are `Config.Par2Path` :99-100
and `Client.par2` :205 (unchanged), `New` :228 with the `Par2Runner` at :293,
`job.stop` :438, `job.fail` :467, the reattach test :551, `abort` :878,
`failureReason` :913-929, `postProcess` :1013 with the `ErrRepairFailed`
tolerance at :1042, `job.repair` :1113-1160 with the timeout branch at :1150,
and `Client.Close` :1594. The plan's par2 tasks (Wave 9) should cite these.

**Post-processing reports its progress** (main `38db94e6`, 2026-10-07). par2
and the unpack move no downloaded bytes, so `progressPercent`,
`lastProgressAt` and the rate used to stand still for as long as they ran (42
minutes on a 20 GB set over NFS) and read as a stall. Now:

- `setStage` stamps `job.stageStarted` when the stage changes (in memory only:
  a job re-attached from its manifest reports no duration).
- `job.item` fills an empty `status.message` from `postProcessMessageLocked`:
  `verifying and repairing with par2 for <d>` while `DownloadStageRepairing`,
  `extracting: <written> of about <archives> for <d>, <rate>/s` while
  `Extracting` (an `unpackProgress` counter in `writeArchiveEntry`), and
  `publishing for <d>`.
- The download rate and ETA are the transfer's alone (zero outside
  `Transferring`).
- `job.repair` logs `usenet: par2 verifying and repairing` (with the failed
  article count and the set's size) before the call and `usenet: par2
  repaired` (with its duration) after; the unpack logs its start and finish.

None of it reads par2's output, so the repair child (§8.4) needs no progress
channel to keep it: the stage change, its timestamp and both logs stay in the
engine process, around `Repairer.Repair`.

**Latent defect, fixed by §8.5.4: an engine shutdown during a repair blocklists a
good release.**

1. `Client.Close` stops every job (`client.go:1561-1576`; `job.stop` `:431-442`).
2. The cancelled `par2` dies by SIGKILL; `exec.Cmd.Wait` then returns the
   `*ExitError` ("signal: killed"), not the context's error.
3. `Par2Runner.Repair` turns that into `ErrRepairFailed` (`repair.go:181-183`).
4. `job.repair` returns it unchanged; `ctx.Err()` is non-nil, so the timeout
   branch at `:1127` does not apply.
5. `abort` ignores only `context.Canceled` (`:869`); `failureReason` sees
   `ErrRepairFailed` under a cancelled, not expired, context and returns
   `missingArticles`.
6. `job.fail` checkpoints that into the manifest (`:457-468`).
7. After the restart, reattach keeps the job Failed (`:541-543`), and the
   controller blocklists the release.

Every engine-image bump rolls both engines, so any roll during a 40-minute
repair costs a good release.

### 8.2 Upstream: the pin and the traps in it

**Pin** (par2go's `shim/build.sh`, which refuses unless `HEAD` is the pinned
commit): tag `v1.5.0-20261005`, commit `4aa390514d0236c00810f2a74f4435b6fff5df37`,
`https://github.com/nzbgetcom/par2cmdline-turbo`. The tag's tarball
(`…/archive/refs/tags/v1.5.0-20261005.tar.gz`, 3.8 MB, top directory
`par2cmdline-turbo-1.5.0-20261005/`) has SHA-256
`889afd443a9d07388ec67bb551ad538a6a464171c9a02a3eb64c3d28ceb6b864` (the same on
two downloads on 2026-10-06; tag and commit checked with `git ls-remote`),
recorded here for a mirror (§14). This tag is chosen over NZBGet's pin,
`v1.5.0-20260914` (`06a29c35`), because it carries 4aa3905's AArch64 CMake fix,
and release builds arm64 natively on `ubuntu-24.04-arm`
(`.github/workflows/release.yml:63-72`).

**API the shim uses** (`include/par2/par2repairer.h`, `libpar2.h` at the tag):
`Par2::Par2Repairer::Process(memorylimit, basepath, nthreads, filethreads,
parfilename, extrafiles, dorepair, purgefiles, renameonly, skipdata, skipleaway)`;
the virtuals `SigProgress(int)` and `SigDone(name, available, total)`; protected
`cancelled`, `basepath`, `mainpacket`, `recoverypacketmap`, `completefilecount`,
`renamedfilecount`, `damagedfilecount`, `missingfilecount`, `sourceblockcount`,
`availableblockcount`, `missingblockcount`; the `Result` enum, 0-8
(`libpar2.h:128-156`).

**Traps** (each checked in the tag's source; "proto" marks those the prototype
hit). "Handled by" says where the fix lives now that par2go is the library.

| # | Trap | Evidence | Handled by |
|---|---|---|---|
| T1 | `Process` ignores its `basepath` argument: the nzbget branch dropped `basepath = _basepath`, so only `PreProcess(CommandLine)` sets it, and every target resolves against the process's cwd | `src/par2repairer.cpp:124-197` has no assignment; `:374` | par2go's shim sets the protected `basepath` (absolute, trailing `/`) before `Process` (its plan's upstream fact 1); clustarr's regression test `TestLibraryRepairerResolvesTargetsAgainstTheJobDir` (proto passed from another cwd). Report upstream: §13 OD23 |
| T2 | `libpar2.h` includes `config.h`, `<inttypes.h>`, `<string.h>` **inside** `namespace Par2`; a later `<cstring>` fails ("'memchr' has not been declared in '::'") | `include/par2/libpar2.h:28,51,54,85`; proto, g++ 16 | par2go shim: system headers first |
| T3 | Scanning checks `cancelled` only inside `if (noiselevel > nlQuiet)` | `par2repairer.cpp:1758-1766` | par2go runs at `nlNormal` (its spec §4.2) |
| T4 | A Release build adds `-fno-rtti`, so `Par2Repairer`'s typeinfo is never emitted and an RTTI subclass cannot link | `cmake/common.cmake:53` | par2go shim build |
| T5 | `HAVE_CONFIG_H`, `PARPAR_ENABLE_HASHER_MD5CRC`, `PARPAR_INVERT_SUPPORT`, `PARPAR_SLIM_GF16` are directory-scoped, and `par2repairer.h` embeds `PAR2Proc`/`PAR2ProcCPU` by value, so a consumer without them sees another class layout | `cmake/common.cmake:33`; `par2repairer.h:24-38,264-266` | par2go shim build (all four, plus `_FILE_OFFSET_BITS=64`) |
| T6 | `CMAKE_BUILD_TYPE` defaults to Debug (asserts, RTTI) | `CMakeLists.txt:35-37` | par2go `build.sh` builds Release |
| T7 | `configure_file` writes `include/par2/version.h` into the **source** tree | `CMakeLists.txt:106-109` | par2go clones a fresh tree |
| T8 | `par2create` appends `.par2` itself (`x.par2` becomes `x.par2.par2`) | proto | moot: par2go has no creator (`ENABLE_CREATOR` off); clustarr's tests create sets with the CLI (§8.8) |
| T9 | `CommandLine` writes about 90 messages to `std::cout`/`std::cerr`; its defaults (memory = host RAM/8, threads = host CPUs, the extra-file filter) exist only there | `src/commandline.cpp:1018-1040,1091-1127` | not used: par2go supplies cgroup-aware limits; clustarr filters extras (`par2Extras`, §8.5.2) |
| T10 | `setup_hasher()` is idempotent but racy on its first call, and every `Par2Repairer` constructor calls it | `parpar/hasher/hasher.cpp:79-81`; `par2repairer.cpp:65` | par2go runs one job at a time per process (`jobMu`, `par2.go:32`); each clustarr child runs one job |
| T11 | `Par2Repairer::filethreads` is static, shared by every repairer in the process | `par2repairer.cpp:39` | same as T10 |
| T12 | `cancelled` is a plain `bool` that worker threads read | `par2repairer.h:210` | par2go sets its own `std::atomic<bool>` and the base flag; formally racy (§14) |
| T13 | Worker threads are not exception-safe: `foreach_parallel`'s `std::thread` branch has no try/catch, so an exception calls `std::terminate`; `gf16mul.cpp:524` calls `abort()` | `include/par2/foreach_parallel.h:34-45` | the entry point's try/catch covers only the calling thread: the clustarr child (§8.4) |
| T14 | Some phases never check cancel: `ComputeRSmatrix`, `AllocateBuffers`, `CreateTargetFiles`, `RenameTargetFiles`, any blocked read | cancel sites only `par2repairer.cpp:477,656,937,1766,1919,2665,2714,2727` | the child is SIGKILLed after the grace |
| T15 | `AllocateBuffers` runs `ceil(blocksize×(missing+overhead)/memorylimit)` passes, each re-reading the whole set | `par2repairer.cpp:2543-2556` | par2go's memory default from the cgroup (§8.4.6) |

### 8.3 The library

#### 8.3.1 What clustarr uses from par2go

- `par2.Available() error` (loads once; `ErrUnavailable` names every path tried).
  The library is `$PAR2GO_LIB` (a file path), else `libpar2shim.so` on the dynamic
  loader's paths.
- `par2.Repair(ctx, index string, opts par2.Options) (par2.Result, error)`, with
  `Options{Dir, ExtraFiles, MemoryLimit, Threads, FileThreads}`. clustarr passes
  `Purge: false` (the `name.N` backups are still removed in Go by
  `removePar2Backups`, and deleting par2 volumes stays the operator's
  `deleteArchives` choice, `repair.go:214-221`), no `Progress` (§13 OD24), and
  zero for the three limits (par2go's defaults, §8.4.6). Every extra file must be
  under `Dir`, which `par2Extras` guarantees.
- `par2.Result{Status, BlocksNeeded, Headers, Files, Log}`; `Status` is
  `AllCorrect`, `Repaired`, `RepairPossible` (Verify only) or `RepairNotPossible`;
  `BlocksNeeded` is `missing - recovery` (`par2repairer.cpp:2242-2245`), the
  number par2 prints; `Log` is the last 4 KiB of par2's own output.
- Sentinels `ErrUnavailable`, `ErrInvalidOptions`, `ErrInsufficientCriticalData`,
  `ErrIO`, `ErrMemory`, `ErrRepairFailed`, `ErrLogic`; a cancelled ctx returns
  `ctx.Err()`, after par2go waits for C to let go of the job.
- One job at a time per process (`jobMu`, `par2.go:30`); `PAR2GO_REQUIRE=1`
  turns its test skips into failures.
- Checked against par2go `1bd94eb` (ABI 2, `internal/bindings/abi.go:21`): each
  job runs on the shim's own thread and Go polls it (`par2.go:98-123`); a
  cancelled ctx calls `p2_cancel` and keeps polling until the job has stopped,
  so "returns after C lets go" and the crash model of §8.4.2 hold unchanged.
  New since the draft and unused here: `Verify`, `Options.Progress` with the
  Loading, Verifying and Repairing phases (§13 OD24).

#### 8.3.2 Prototype evidence (the section draft's in-tree shim, run on this host)

| Check | Result |
|---|---|
| Tools | `/usr/bin/cmake` 4.4.3, `/usr/bin/g++` 16.2.1 (clang++, ninja and make present); CMake 4 accepts par2's `cmake_minimum_required(VERSION 3.13)` |
| Configure + build | 33 s wall on 12 CPUs; the `.so` is 2.8 MB, 2.3 MB stripped (2.2 MB without the creator) |
| `readelf -d` | NEEDED only `libm.so.6`, `libc.so.6`, `ld-linux-x86-64.so.2`; BIND_NOW (static libstdc++/libgcc) |
| `nm -D --defined-only` | only the shim's own symbols |
| purego harness | passes under `CGO_ENABLED=0`, `=1` and `-race` |
| intact set, cwd elsewhere | verify 0; output ends "All files are correct, repair is not required." |
| one damaged slice | verify 1, repair 0; the file byte-identical to the original; backup `movie.mkv.1` left behind |
| data under an obfuscated name, passed as an extra | verify 1, repair 0 (rename only); restored under the set's name |
| 8 of 16 blocks lost, 2 recovery blocks | verify 2, missing 8, recovery 2; output ends "You need 6 more recovery blocks to be able to repair." |
| cancel during a 1 GiB slide-scan | cancel 300 ms in; verify returned "cancelled" after **176 µs** |
| child protocol (§8.4.3) | a cooperative cancel returned at the deadline with the child's result; a child ignoring cancel was SIGKILLed at deadline + grace (200 + 300 ms, returned at 501 ms); a child that aborted left no result (exit 2) |

par2go's own suite (its spec §7) holds the shipped library to the same facts:
fixtures from the real CLI, a differential test against the CLI's verdicts,
cancellation of a generated 128 MiB set, serialisation, `DT_NEEDED` ⊆ libc, libm,
ld-linux, only `p2_*` exported, and no cgo.

### 8.4 `pkg/par2child`: the repair child

`pkg/par2child` imports par2go, the standard library and nothing from clustarr,
so any binary can take it or refuse it.

```go
package par2child

const ChildCommand = "par2-repair" // `agent par2-repair`
var ErrChildDied = errors.New("par2child: repair child exited without a result")

type Op uint8 // OpRepair = 1, OpProbe = 2
type ErrKind uint8 // None, Unavailable, InvalidOptions, InsufficientCriticalData, IO, Memory,
                   // RepairFailed, Logic, Cancelled, Other; maps to and from par2go's sentinels
type Request struct {
    Op          Op
    Index       string   // absolute path of the index volume
    Dir         string   // absolute base directory; targets are written here
    Extras      []string // absolute paths scanned as candidate targets
    Threads, FileThreads int
    MemoryLimit int64
}
type Result struct {
    Status       par2.Status
    BlocksNeeded int
    Headers      par2.Headers
    Log          string  // par2go's Log
    ErrKind      ErrKind // the sentinel, which cannot cross gob as an error
    Err          string  // its text
    Library      string  // the loaded library's path, for logs
}
func (r Result) Error() error // nil, or an error wrapping the matching par2go sentinel, so errors.Is works in the parent

type Runner interface {
    Run(ctx context.Context, req Request) (Result, error)
    Probe(ctx context.Context) error
}
type InProcess struct{}            // par2go in this process (the child itself, and tests)
type Exec struct {                 // par2go in a child of this binary
    Path  string        // "" = os.Executable()
    Args  []string      // nil = []string{ChildCommand}
    Env   []string      // nil = childEnv()
    Grace time.Duration // 0 = 30 s
}
func ChildMain(args []string) int
```

Files: `doc.go`; `types.go`; `inprocess.go`; `exec.go` (`Exec`, `childEnv`, the
framing: the one production exec in the module, §4.5.6); `child.go`
(`ChildMain`); `tail.go` (`tailBuffer`, moved from usenet's `tailWriter`).

#### 8.4.1 `InProcess`

`Run` calls `par2.Repair(ctx, req.Index, par2.Options{Dir: req.Dir, ExtraFiles:
req.Extras, MemoryLimit: req.MemoryLimit, Threads: req.Threads, FileThreads:
req.FileThreads})` and maps the error onto `ErrKind`. If ctx is done it returns
the result and `fmt.Errorf("par2child: run interrupted: %w", ctx.Err())`.
`Probe` is `par2.Available()`.

#### 8.4.2 Why the engine does not call the library in-process

1. **A crash would become a crash loop.** An `abort()` or a terminating exception
   on a par2 worker thread (T13) would kill the whole usenet engine; reattach then
   restarts every job not Completed or Failed (`client.go:541-543`), so the same
   set crashes the engine again on every start. Today a crashing `par2` fails one
   job (`repair.go:181-183`). par2go's own spec says the same (its §5: "a crash
   inside the C++ library … takes the process down. The only remedy is process
   isolation").
2. **Some phases cannot be cancelled (T14).** A thread blocked on the NFS working
   area (ADR-0014) cannot be interrupted; an abandoned in-process run keeps
   writing the job's content directory.
3. **Memory sits outside GOMEMLIMIT.** Repair buffers and ParPar's staging are
   outside the engine's `GOMEMLIMIT` of 0.8 × limit
   (`app/grab/controller/downloadclient/workload.go:133-157`).
4. **par2go serialises jobs per process** (`jobMu`). In-process, every repair of
   the engine's concurrent downloads would queue behind one; children run
   concurrently, as today's `par2` processes do.

A child keeps today's guarantees (a SIGKILL deadline, one failed job per crash,
an OOM victim that is not the engine) while using the library as a library, and
the image still ships no external program.

#### 8.4.3 The child protocol

*Parent, `Exec.Run`:*

- `cmd := exec.CommandContext(ctx, path, args...)` with `cmd.Env = env`;
  `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` (a terminal SIGINT to
  the engine's group cannot kill the child before the parent cancels it);
  `stdin := cmd.StdinPipe()`; a result pipe `r, w := os.Pipe()` passed as
  `cmd.ExtraFiles = []*os.File{w}` (fd 3 in the child);
  `cmd.Stdout = cmd.Stderr = &tailBuffer{max: 64 << 10}`;
  `cmd.Cancel = func() error { return stdin.Close() }` (the cancellation style the
  os/exec docs describe as "closing a stdin … pipe"); `cmd.WaitDelay = Grace`, so
  os/exec `Process.Kill`s a child still running Grace after ctx ends.
- Start the child (ENOENT or EACCES gives an error wrapping `par2.ErrUnavailable`),
  then close `w`.
- Write a big-endian `uint32` length plus `gob(Request)`, keeping stdin open.
- A goroutine reads `io.LimitReader(r, 1<<20)`. `cmd.Wait` runs in its own
  goroutine too, because Go's `Cmd.Wait` calls `Process.Wait` first
  (`$GOROOT/src/os/exec/exec.go:944`) and `WaitDelay` only sends
  `Process.Kill`, which cannot reap a child in uninterruptible I/O. `Run`
  returns when `Wait` returns, or, once ctx has ended, `Grace` has passed and
  the kill was sent, after a further `reapSlack` (5 s), with an error wrapping
  `ErrChildDied` and `ctx.Err()`; the goroutine stays behind to reap the
  child. A gauge `clustarr_par2_unreaped_children` counts such goroutines, and
  each logs one ERROR naming the job; neither gates readiness (a stuck NFS read
  is not a fault a restart cures). Then decode `gob(Result)`.
- Outcomes: ctx done gives the decoded result, if any, and an error wrapping
  `ctx.Err()`; nothing to decode gives
  `fmt.Errorf("%w: %v: %s", ErrChildDied, waitErr, stderrTail)`; `ErrKind`
  Unavailable gives `par2.ErrUnavailable`; otherwise the result and
  `result.Error()`.
- gob, not JSON: NZB and par2 names are arbitrary bytes, `encoding/json` rewrites
  invalid UTF-8 to U+FFFD, and gob carries strings byte for byte.

*Child, `ChildMain(args)`*, with no flags, logging, tracing, NATS or Kubernetes:

1. With `--self-check`: `par2.Available()`, print `par2go <library path>`, exit 0;
   on failure print the error (naming `PAR2GO_LIB`) and exit 1.
2. Write `1000` to `/proc/self/oom_score_adj`, best effort (raising it needs no
   privilege, and a cgroup OOM then picks the child).
3. Read the framed request; a malformed request exits 3.
4. A goroutine runs `io.Copy(io.Discard, os.Stdin)`; when it returns (the parent
   closed stdin, or died), cancel the run context.
5. `OpProbe` calls `par2.Available()`; `OpRepair` calls `InProcess{}.Run`. A load
   failure writes `Result{ErrKind: Unavailable}` and exits 2.
6. gob-encode the result to `os.NewFile(3, "par2-result")`, exit 0.

*`childEnv()`* passes only `PAR2GO_LIB`, `LD_LIBRARY_PATH`, `TMPDIR`, `GODEBUG`
and `GOMAXPROCS`. `Exec.Probe` runs the child with `OpProbe`, so the engine
process itself never dlopens par2go.

*Bound:* a repair returns within `par2Deadline + Grace + reapSlack`, whatever
par2 does in user space. A child blocked in uninterruptible kernel I/O (D state
on a hung NFS mount) is not reaped until the I/O returns, exactly like today's
`par2` process: `job.repair` returns and the job fails as `ErrPar2Timeout`, but
the child and its open files remain until the kernel releases them.

#### 8.4.4 Dispatch

`cmd/agent/main.go`'s `main` begins, before cobra, flags or obs exist:

```go
if len(os.Args) > 1 && os.Args[1] == par2child.ChildCommand {
    os.Exit(par2child.ChildMain(os.Args[2:]))
}
```

The child is the file the engine runs from, `/usr/bin/agent` in the native
image; `os.Executable()` reads `/proc/self/exe`, which exists in a FROM-scratch
image, so no new executable ships. This lands only once the usenet engine runs
from `cmd/agent` and `cmd/clustarr` is gone: `cmd/clustarr` must never link purego
(`TestClustarrNeverLinksADynamicLoader`), the boundary par2go's §1 itself names
(§8.11, §11.1).

#### 8.4.5 Concurrency

Repairs run concurrently, as today: one child per repairing job, each with
par2go's one-job mutex to itself.

Main's usenet connection budget (phase A, `db53ebd1`) admits only the transfer
stage by slots; repair, unpack and publish hold none. So it does not bound the
children either: a pod with 10 transfer slots can still have every job that
finished its transfer repairing at once, each child taking par2go's default
memory share (§8.4.6, §14). Today every job transfers at once, so their
repairs can overlap too; slots space the transfers out, which makes overlapping
repairs rarer, not impossible. A cap, if one is needed, is a per-pod repair
semaphore in `job.repair` around
`Repairer.Repair`; it belongs to whoever builds the budget's phase A, since it
lives in `pkg/download/usenet`.

#### 8.4.6 Threads and memory

- `Threads` 0: par2go uses `runtime.GOMAXPROCS(0)`, cgroup-aware (the CLI used
  host CPUs). `FileThreads` 0: 2.
- `MemoryLimit` 0: par2go's default, 1/8 of `/sys/fs/cgroup/memory.max`, or of
  `MemTotal` from `/proc/meminfo` when the limit is `max` or unreadable, at least
  256 MiB. A fixed 256 MiB would cost passes (T15): with 1000 missing 2 MB blocks
  a 10 GB set on NFS would be read 8 times. The section draft's 2 GiB ceiling is
  dropped because par2go owns the rule; `LibraryRepairer.MemoryLimit` can still
  set one (§14 notes concurrent children each taking 1/8).

### 8.5 `pkg/download/usenet`: `Repairer` replaces `Par2Runner`

#### 8.5.1 The interface (`repair.go`), signatures identical to `Par2Runner`'s

```go
// Repairer verifies a par2 set and repairs it in place.
type Repairer interface {
    // Available reports whether repair can run at all; false makes a set that
    // needs repair fail with ErrPar2Unavailable, a local fault (writeError).
    Available() bool
    // Repair verifies dir against indexFile (a name inside dir), scanning every
    // other regular file in dir as a candidate target, and repairs when it can.
    // A cancelled ctx returns an error wrapping ctx.Err().
    Repair(ctx context.Context, dir, indexFile string) (Par2Result, error)
}
```

#### 8.5.2 `LibraryRepairer` (`pkg/download/usenet/par2lib.go`)

```go
type LibraryRepairer struct {
    Runner      par2child.Runner // par2child.Exec{} in the engine; par2child.InProcess{} in tests
    Threads     int              // 0 = par2go's default
    MemoryLimit int64            // 0 = par2go's default
    mu    sync.Mutex
    ready bool
}
```

- **`Available()`** calls `Runner.Probe` with a 10 s timeout; a success is cached
  for the process's life, a failure re-probes next call (calls happen only when a
  job needs repair).
- **`Repair`** keeps the span `usenet.par2.repair`, with attributes
  `par2.status`, `par2.blocks_needed`, `par2.recovery_blocks` and
  `par2.missing_files`, and the debug log `"par2 finished"` (`repair.go:161-162`).
  It builds `Request{Op: OpRepair, Index: abs(dir)/indexFile, Dir: abs(dir),
  Extras: par2Extras(dir, indexFile), …}`. `par2.ErrUnavailable` becomes
  `fmt.Errorf("%w: %w", ErrPar2Unavailable, err)`; a context error is returned
  as is; `par2child.ErrChildDied` becomes `ErrRepairFailed` (today's behaviour,
  §13 OD22); everything else goes through `par2Verdict`.
- **`par2Extras(dir, index)`** takes `extraFiles` (kept), joins each name onto
  `dir`, and drops zero-byte files, matching the CLI's filter
  (`commandline.cpp:1091-1127`: exists, inside basepath, non-empty, no
  duplicates; the other three hold by construction).

#### 8.5.3 Verdicts

`par2Verdict(par2child.Result, error) (Par2Result, error)` is pure and
table-tested without the library.

| Library outcome | Today's equivalent | Result |
|---|---|---|
| `Status AllCorrect` | "All files are correct…", or exit 0 with no recognised line (a missing *non-recoverable* file: `Process` returns eSuccess without "Repair complete") | `AllCorrect` |
| `Status Repaired` (a rename-only repair included) | "Repair complete." | `Repaired` |
| `Status RepairNotPossible` | "Repair is not possible / You need N more recovery blocks" | `ErrRepairFailed`, `BlocksNeeded = Result.BlocksNeeded` |
| `ErrInsufficientCriticalData`, `ErrRepairFailed`, `ErrIO`, `ErrLogic`, `ErrMemory` | a non-zero exit | `ErrRepairFailed` wrapping the sentinel (§13 OD22 would make `ErrMemory` local) |
| `ErrInvalidOptions` | none (a caller bug) | a plain error, so `writeError` |
| `ErrUnavailable` | missing binary | `ErrPar2Unavailable` |
| ctx done | §8.1's defect | an error wrapping `ctx.Err()` |

`Output` is `tail(res.Log, 2048)`, as today. At `nlNormal` the log also holds
per-file lines, but the verdict lines come last.

#### 8.5.4 Changes in `client.go`

- `Config.Par2Path string` (`:99-100`) becomes `Config.Repairer Repairer`; nil
  means no repair (a set that needs one fails with `ErrPar2Unavailable`).
- `Client.par2` (`:205`) becomes `Client.repairer`; `New` (`:293`) installs
  `cfg.Repairer`, or `noRepairer{}` when nil.
- `job.repair` (`:1126-1131` at `7791f1f5`; the call is at :1149-1154 at main
  `80175fdc`, between `38db94e6`'s two log lines) gains one branch, which fixes
  §8.1's defect:

```go
if _, err := j.client.repairer.Repair(repairCtx, j.contentDir(), safeName(index)); err != nil {
    if ctx.Err() != nil {
        // The job was stopped (engine shutdown, Remove) or hit DownloadTimeout:
        // never a verdict on the release. abort ignores context.Canceled;
        // failureReason reads DeadlineExceeded as timeout.
        return fmt.Errorf("usenet: par2 interrupted: %w", ctx.Err())
    }
    if repairCtx.Err() != nil {
        return fmt.Errorf("%w: par2 did not finish within %s", ErrPar2Timeout, par2Deadline(j.nzb.TotalBytes))
    }
    return err
}
```

- `failureReason` (`:903-918`; :913-929 at `80175fdc`) is unchanged; its doc's
  "no par2 binary" becomes "no par2 library".
- `38db94e6`'s logs stay where they are: `usenet: par2 verifying and repairing`
  before the call, and `usenet: par2 repaired` only after a success, so an
  interrupted repair (the new branch) logs neither a finish nor a verdict.
  `postProcessMessageLocked`'s repair message names "par2", and stays true.

#### 8.5.5 `repair.go` cleanup

- **Delete:** the imports `os/exec`, `syscall`, `strconv`, `bytes`;
  `par2OutputMax`; the four verdict regexes; `Par2Runner` and `resolve`;
  `tailWriter` (moves to `pkg/par2child`).
- **Rewrite** the comment at `:39-47`.
- **Keep:** `ErrPar2Unavailable` (text "usenet: par2 library unavailable"),
  `ErrRepairFailed`, `Par2Result`, `ErrPar2Timeout`, `par2Deadline`,
  `par2BackupRE`, `removePar2Backups`, `tail`, `extraFiles`, `par2IndexFile`,
  `smallestPar2Volume`. `rename.go`'s pure-Go packet parser is untouched.

### 8.6 Wiring

`app/grab/engine/usenet.BuildClient` (`config.go:159-175`) sets, after
`BuildConfig`:

```go
rep := &usenetclient.LibraryRepairer{Runner: par2child.Exec{}}
cfg.Repairer = rep
// log Info "par2 library ready" with the probed library, or Error "par2 library
// unavailable: a release that needs repair fails as writeError"; never gates readiness.
```

par2 changes nothing in `BuildConfig` or its table tests (`config_test.go`).
Main's connection budget (phase-A Task 1, not landed at `80175fdc`) adds
`ConnectionsPerDownload` and `Limiter` there; the repairer line goes after them
either way. par2 owns no field manager, KV bucket, stream or consumer (R7). par2go and `pkg/par2child` are
reached only through `pkg/download/usenet` → the usenet-engine domain →
`cmd/agent`; manager, ui, markers and transcode never link them (§4.5). purego
works under both cgo modes, so the agent's cgo build (R13) does not matter.

### 8.7 The native image

`images/Dockerfile.native` (R1) gains a `par2` stage:

```dockerfile
# par2go: libpar2shim.so -- par2cmdline-turbo (nzbgetcom fork) as a library.
# Built from the par2go named context while par2go is unpublished; once it is
# released, fetch libpar2shim-linux-${TARGETARCH}.so and check SHA256SUMS instead.
FROM scratch AS par2go
FROM debian:bookworm-slim AS par2
ARG PAR2GO_COMMIT=unknown
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates cmake g++ git make binutils \
 && rm -rf /var/lib/apt/lists/*
COPY --from=par2go / /src/par2go/
RUN set -eux; \
    test -x /src/par2go/shim/build.sh || { echo "pass --build-context par2go=<a git archive export of par2go>" >&2; exit 1; }; \
    rm -rf /src/par2go/shim/build /src/par2go/shim/out; \
    lib=$(CMAKE_BUILD_DIR=/tmp/par2-cmake OUT_DIR=/tmp/out /src/par2go/shim/build.sh | tail -n1); \
    test "$(basename "$lib")" = libpar2shim.so; \
    mkdir -p /out/usr/lib /out/usr/share/licenses/par2cmdline-turbo /out/usr/share/licenses/par2go; \
    strip --strip-unneeded -o /out/usr/lib/libpar2shim.so "$lib"; \
    src=/src/par2go/shim/build/par2cmdline-turbo; \
    cp "$src/COPYING" "$src/AUTHORS" /out/usr/share/licenses/par2cmdline-turbo/; \
    cp /src/par2go/LICENSE /out/usr/share/licenses/par2go/; \
    printf '%s\n' \
      "par2cmdline-turbo v1.5.0-20261005 (nzbgetcom fork, commit 4aa390514d0236c00810f2a74f4435b6fff5df37), GPL-2.0-or-later; ParPar backend public domain/CC0" \
      "Source: https://github.com/nzbgetcom/par2cmdline-turbo/archive/refs/tags/v1.5.0-20261005.tar.gz, patched with github.com/mediactl/par2go shim/patches/*.patch at par2go commit ${PAR2GO_COMMIT} ($(ls /src/par2go/shim/patches | tr '\n' ' '))" \
      "Shim: github.com/mediactl/par2go commit ${PAR2GO_COMMIT} (GPL-3.0-or-later); libstdc++/libgcc linked statically under the GCC Runtime Library Exception 3.1" \
      > /out/usr/share/licenses/par2cmdline-turbo/SOURCE
```

- **The context is a clean export.** The Makefile passes `git archive` of the
  pinned par2go commit (§7.6 item 3), never the working tree: the checkout's
  git-ignored `shim/build/` (251 MB) holds a CMake cache recorded at
  `/home/appkins/src/mediactl/par2go/shim/...`, and CMake refuses a cache made
  in another directory, so building from the working tree fails, as par2go
  found for its own Docker build (`00f0fd2`, `shim/build-in-docker.sh`). The
  `rm -rf` and `CMAKE_BUILD_DIR` keep the stage correct even if someone passes
  a working tree by hand. The library name is read from `build.sh`'s last line
  (`shim/build.sh:50` echoes the installed path) and checked, so a future
  rename fails here, not in `stage.sh`.
- Built natively per architecture, like the `ffmpeg` stage (no
  `--platform=$BUILDPLATFORM`), on bookworm (glibc 2.36), par2go's build base, so
  the library runs on the trixie staging base too.
- `staging-base` adds `COPY --from=par2 /out/ /`. `images/distroless/stage.sh`
  adds `/usr/lib/libpar2shim.so` to the `trace` line (`:38`) and refuses
  `for f in ffmpeg ffprobe par2; do` (`:76`). The final image adds
  `ENV PAR2GO_LIB=/usr/lib/libpar2shim.so` beside `FFGO_SHIM_DIR`.
- **Self-checks** (`hack/image-checks.sh`, §10.1.4): `self-check:agent` (which
  covers par2go) and `self-check:par2-child`
  (`docker run --rm --entrypoint /usr/bin/agent $(NATIVE_IMG) par2-repair --self-check`);
  the debug image's `notices` check asserts
  `/usr/share/licenses/par2cmdline-turbo/{COPYING,SOURCE}` and
  `/usr/share/licenses/par2go/LICENSE`, and `no-media-executables` asserts no
  `par2` anywhere.
- **Removed with the media image:** the par2 lines of `images/Dockerfile.media`
  (`:36-41,50,89,93-94,116-136,168`).

### 8.8 Local builds and tests

**Requirements:** CMake ≥ 3.13, a C++20 compiler (GCC ≥ 10), git, curl and
sha256sum; this host has all of them (§8.3.2).

**`make native-assets`** (`hack/native-assets.sh`, §10.1.6) builds
`$(NATIVE_ASSETS)/lib/libpar2shim.so` from par2go's `shim/`, and installs the
par2 CLI as `$(NATIVE_ASSETS)/bin/par2` (the animetosho static zip at
`PAR2_CLI_VERSION=v1.5.0`, today's `ci.yml:36` pin) **for creating test sets
only**. par2go's `build.sh` always clones, resets and patches par2cmdline-turbo
inside its own checkout (`work=${here}/build`, `shim/build.sh:21-22,41-44`), so
running it in `$(PAR2GO_DIR)` would race a concurrent build and write into a
sibling repository. The script therefore exports `$(PAR2GO_REF)` with
`git -C $(PAR2GO_DIR) archive` into its `mktemp -d` directory and runs that
copy's `shim/build.sh` with `CMAKE_BUILD_DIR` and `OUT_DIR` inside the same
directory (once par2go is released it downloads the pinned asset and checks
`SHA256SUMS` instead). Every output is stamped by its inputs (the FFmpeg and
ORT pins, the par2go commit, `PAR2_CLI_VERSION`) and installed with an atomic
`mv`, so concurrent `make test`s in `clustarr` and `clustarr-unify` are safe.

`make test` and `test-race` export `PAR2GO_LIB=$(NATIVE_ASSETS)/lib/libpar2shim.so`,
`PAR2GO_REQUIRE=1` and `PATH=$(NATIVE_ASSETS)/bin:$PATH` beside
`CLUSTARR_PG_ASSETS`. By hand:
`make native-assets && PAR2GO_LIB=$(go env GOPATH)/bin/native-assets/lib/libpar2shim.so go test ./pkg/par2child/... ./pkg/download/usenet/...`.

**Skip rule** (the cure for the silent-skip gotcha):
`test/nativetest.Require(t, "par2go", par2.Available())` skips, naming
`PAR2GO_LIB` and `make native-assets`, only when neither
`CLUSTARR_REQUIRE_TOOLS=1` nor `PAR2GO_REQUIRE=1` is set; otherwise it fails.

**`test/par2test.CreateSet(t, dir, index string, files []string, blocks, redundancyPercent, recoveryFiles uint32)`**
runs `par2 c -q -b<blocks> -r<pct> [-n<files>] -- <index> <files>` from PATH.
`test/` is outside the exec guard's scope, and nothing outside `_test.go` files
imports it. (par2go creates no sets: `ENABLE_CREATOR` is off by its design, §13 OD26.)

**CI** (`ci.yml`): the par2 half of "Install ffmpeg and par2" (`:129-133,136`) is
replaced by `make native-assets`, cached with `actions/cache` (pinned by SHA like
the workflow's other actions; key over `images/Dockerfile.native`,
`hack/native-assets.sh` and the par2go pin).

**Test conversions**

| Today | After |
|---|---|
| `repair_test.go:35` `par2Binary` | removed; real tests call `nativetest.Require` and use `&LibraryRepairer{Runner: par2child.InProcess{}}` |
| `TestPar2RunnerReportsAMissingBinary` | `TestLibraryRepairerReportsAMissingLibrary`: `par2child.Exec{Path:"/nonexistent/agent"}`, expecting `Available()==false` and `ErrPar2Unavailable`; no library needed |
| `TestPar2RunnerVerifiesAnIntactSet` / `…RepairsADamagedFile` / `…RepairsASetWhoseFilesCarryOtherNames` | `TestLibraryRepairer…`, same names after the prefix, with `par2test.CreateSet(…,16,20,0)` / `(16,50,0)` / `(16,20,0)` |
| `TestTailWriterBoundsASubprocessThatTalksTooMuch` | `pkg/par2child` `TestTailBufferBoundsAChildThatTalksTooMuch` |
| `rename_test.go:39,276` (`par2 c`) | `CreateSet`: `-b16 -r10` is `(16,10,0)`, `-b8 -r50 -n1` is `(8,50,1)` |
| `rename_test.go:157,212` (`Par2Path: bin`) | `CreateSet` plus `Repairer: &LibraryRepairer{Runner: par2child.InProcess{}}` |
| `client_test.go:315` skip test | `fakeRepairer{available: true}`, whose `Repair` calls `t.Error` |
| `client_test.go:356` deadline test | a `fakeRepairer` that blocks until ctx is done; still expects `writeError` and "par2 did not finish" |
| `client_test.go:723` full pipeline | `CreateSet` plus an InProcess `LibraryRepairer` |

**New tests**

- usenet: `TestPar2VerdictMapsEveryOutcome` (table, no library);
  `TestPar2ExtrasAreAbsoluteAndSkipEmptyFiles`;
  `TestLibraryRepairerResolvesTargetsAgainstTheJobDir` (`t.Chdir` elsewhere; the
  T1 regression); `TestLibraryRepairerCountsTheBlocksItNeeds`;
  `TestAnEngineStoppedDuringRepairResumesTheJob` (its fake returns
  `ErrRepairFailed` on cancel, as the exec'd binary did; `Close()` lands
  mid-repair; the manifest must not be Failed and a fresh `New` completes the
  job; falsify by reverting §8.5.4's branch and watching it see `missingArticles`).
- `pkg/par2child`, fake children (no library; `TestMain` dispatches on
  `PAR2_TEST_CHILD`): `TestExecReturnsTheChildsResult`,
  `TestExecCancelsTheChildCooperatively`, `TestExecKillsAChildThatIgnoresCancel`
  (Grace 300 ms), `TestExecReportsAChildThatDiedWithoutAResult` (`ErrChildDied`
  and the stderr tail), `TestExecReportsAMissingBinary`,
  `TestExecPassesTheChildOnlyItsEnvironment`, `TestExecSendsNamesByteForByte` (an
  extra named `"\xff.rar"`), `TestResultErrorWrapsTheLibrarySentinel`.
- `pkg/par2child`, with the library: `TestInProcessVerdicts` (intact, damaged,
  renamed, impossible), `TestInProcessResolvesAgainstDirNotCwd`,
  `TestInProcessCancelReturnsPromptly` (a 64 MiB set cancelled at 50 ms returns
  within 1 s with `ctx.Err()`), `TestChildRunsTheLibrary`.

The draft's `TestCreateStripsThePar2Suffix` (no creator) and
`TestDefaultMemoryLimitFollowsTheCgroup` and `TestStatusAndOptionsMirrorTheHeader`
(par2go's `options_test.go` and ABI tests own them) are not clustarr tests.

### 8.9 Guards

1. **`test/guards` `TestNoImageShipsAPar2Executable`.** No Dockerfile installs a
   `par2` into a bin directory; `stage.sh` matches
   `for f in ffmpeg ffprobe par2; do.*exit 1` (updating the regex at
   `cmd/clustarr/chart_images_test.go:183`, which moves under R10).
2. **`test/guards` `TestPar2LibraryComesOnlyFromPar2go`.** The native image builds
   (or fetches) `libpar2shim.so` only in its `par2` stage, from par2go; no
   Dockerfile, workflow or `hack/` script fetches a par2 release except
   `hack/native-assets.sh`'s test-only CLI pin.
3. **`pkg/download/usenet` `TestTheUsenetClientExecsNothing`.** No non-test file
   imports `os/exec` (also covered repo-wide, §4.5.6).
4. **`pkg/par2child` `TestTheOnlyExecIsTheRepairChild`.** `exec.Command`/`exec.CommandContext`
   appear only in `Exec.Run`, and `exec.LookPath` nowhere.
5. **The engine package `TestTheEngineRepairsInAChildOfItself`.** The installed
   Repairer is a `*LibraryRepairer` whose `Runner` is the zero `par2child.Exec{}`.
6. **`go list -deps`** (§4.5): manager, ui, markers and transcode never reach
   `github.com/mediactl/par2go` or `pkg/par2child`; agent must; `pkg/par2child`
   reaches no other `github.com/mediactl/clustarr/...` package.
7. **`cmd/agent` `TestThePar2ChildIsDispatchedBeforeCobra`.** Runs
   `par2-repair --self-check` with `PAR2GO_LIB` pointing at a missing file and
   expects exit 1 and a message naming `PAR2GO_LIB`, with no flag parsing.

### 8.10 Licence

par2cmdline-turbo is GPL-2.0-or-later (`include/par2/libpar2.h:6-9`: "either
version 2 of the License, or (at your option) any later version"; `COPYING` is the
GPL v2 text); with clustarr's and par2go's GPL-3.0-or-later code, the image is
distributed under GPL-3.0 (ADR-0002). ParPar (`parpar/`) is public domain or CC0.
Static libstdc++ and libgcc are permitted by the GCC Runtime Library Exception
3.1. The image carries `/usr/share/licenses/par2cmdline-turbo/{COPYING,AUTHORS,SOURCE}`
(the corresponding-source pattern `images/Dockerfile.transcoder:93-100` uses for
FFmpeg) and `/usr/share/licenses/par2go/LICENSE`. par2's sources are fetched at
build time and never copied into either repository. `go.mod` gains
`github.com/mediactl/par2go`; `hack/deps` gains no entry beyond it.

### 8.11 Order of work

1. par2go per its plan (Tasks 1-8, local; Task 9's push waits for the owner).
2. `go.mod`: the par2go replace (serial, §7.6). `pkg/par2child`, its tests, guards
   4 and 6.
3. `pkg/download/usenet`: the conversion, §8.5.4's fix, the test conversions,
   guard 3. `Par2Runner` is deleted here.
4. The dispatch in `cmd/agent`, the `BuildClient` wiring, guards 5 and 7.
5. The image stage, the stage.sh edits, the ENV line, the self-checks, guards 1
   and 2.
6. CI, then the docs (§12).

Steps 2-4 put purego into packages `cmd/clustarr` links today (`pkg/download/usenet`
via `app/grab`), which `TestClustarrNeverLinksADynamicLoader` forbids, so they
land after Wave 5.6 deletes `cmd/clustarr` (§11.1). Step 4 changes runtime
behaviour and needs step 5's image before any deploy, because an engine on the
media image would find no library and fail repairs as writeError.

---
## 9. Autoscaling without KEDA

The opt-in KEDA path is replaced by three things: the manager serves the External
Metrics API itself; a manager reconciler owns one `autoscaling/v2` HPA per
queue-driven agent Deployment; and natsbus separates per-pod handler slots from
the consumer-wide `MaxAckPending`. Live-cluster facts come from kind-cluster-plex
(HPAScaleToZero Beta and on in 1.37: `kubernetes_feature_enabled{name="HPAScaleToZero",stage="BETA"} 1`).

### 9.0 Decisions in brief

- **Metric.** One external metric, `clustarr_consumer_lag{stream,consumer}` =
  `NumPending + NumAckPending`, from JetStream ConsumerInfo through the new
  `events.StreamAdmin.ConsumerState`.
- **Metrics server.** In `cmd/manager`, package `app/autoscale/extmetrics`,
  hand-rolled on `net/http` + `crypto/tls` (no `k8s.io/apiserver`,
  `k8s.io/metrics` or `k8s.io/kube-aggregator`), on every manager replica, on
  `:6443`, started before the caches sync, behind its own Service
  `external-metrics` (port 443, `publishNotReadyAddresses: true`), so the API
  does not wait out a 10-minute cache sync after every manager restart.
- **Authentication.** Front-proxy mTLS checked against
  `kube-system/extension-apiserver-authentication`. No SubjectAccessReview, so
  `system:auth-delegator` is not bound.
- **TLS.** The manager generates a CA, keeps it in a Secret, and injects it as
  `spec.caBundle` on `v1beta1.external.metrics.k8s.io`. The installers render that
  APIService; the manager owns only `spec.caBundle` (R5 amendment).
- **Autoscale reconciler.** `app/autoscale/controller`, field manager
  `clustarr-autoscale` (`k8s.ManagerAutoscale`), leader-only. It finds Deployments
  by the label `autoscale.clustarr.io/domain`. Which consumers each domain holds is
  the Go table `pkg/agentdomain.Domains()`, held to `events.Default()` by a guard.
  Per-pod slots come from `events.ConsumerSpec.Slots`, overridable per Deployment by
  `CLUSTARR_CONSUMER_SLOTS`.
- **HPA shape.** minReplicas 0 (1 for `events`; raised by the annotation
  `autoscale.clustarr.io/min-replicas`); maxReplicas
  `max(minReplicas, 1, min(matching nodes, floor(MaxAckPending/slots) over the domain's consumers))`;
  one External metric per consumer at AverageValue = its slots; scale-up window 0
  with a Pods policy of maxReplicas per 15 s; scale-down window
  `max(300s, longest AckWait of the domain's consumers)`.
- **natsbus.** `Subscribe` binds (never writes consumer config, §5.9) and stops
  using `Consume`: a pull loop never fetches more than its free slots; a slot whose
  delivery lapsed on the server is reclaimed; a per-subscription drain lets running
  handlers finish on scale-down. `Pull` writes the topology's `MaxAckPending`.
  membus mirrors all of it.
- **Which Deployments autoscale.** HPAs for `catalog`, `events` (min 1), `import`,
  `caption` and `markers`; fixed replicas for `metadata`, `index`, the manager,
  `ui`, engines and transcode pools. `catalog` and `caption` scale 0..1 because
  their throttled consumers keep today's cap (R5 amendment, §9.1.1).
- **KEDA is deleted** (§10.2.7). `TestCaptionarrScaledObjectCountsTheRealFetchConsumers`
  retires in favour of the domain and topology guards (§9.9, §10.3.2).

**Amended 2026-10-07 (NATS research; `nats-hpa-metrics.md` §1, §2, §5; owner
decision (c)).** The metric stays as decided; this states it precisely and
records why the two NATS-native alternatives the owner raised are not used.

**The metric, per durable.** `clustarr_consumer_lag{stream,consumer}` =
`ConsumerInfo.NumPending + ConsumerInfo.NumAckPending`, where:

- `NumPending` is the messages matching the durable's filters after its
  delivered sequence. A `WithScheduleAt` hold sits on a `.sched.` subject no
  filter matches and counts only once it fires, so a delay-profile grab wakes
  its domain when it is due, not when it is queued.
- `NumAckPending` is delivered and unsettled: running in a handler, waiting out
  a delayed nak, or lapsed and waiting to be redelivered (a crashed pod's
  delivery). That is what wakes a domain at zero for a retry, and what keeps
  the last pod while it holds work.
- Neither counts a message past `MaxDeliver` that waits for its dead-letter copy
  (nats-server v2.15.0 `consumer.go:2427-2453`), nor `NumRedelivered` (a subset,
  not additive) or `NumWaiting` (idle pull requests, the inverse of demand).

**The source is `$JS.API.CONSUMER.INFO.<stream>.<durable>`**, which only the
consumer's leader answers (`jetstream_api.go:5556`, "we need to be the consumer
leader to proceed", and `:5649`), so it is right on the chart's 3-node R3
cluster by construction. It rides the manager's NATS connection, needs no
monitoring port, and costs 897 bytes and 0.23-0.83 ms per durable (measured
live). `CONSUMER.LIST` is not used: in a cluster it reports a durable whose
leader does not answer within 4 s only as `missing`, which nats.go's
`ListConsumers` drops, so a slow leader would read as lag 0.

**Why not `/jsz`.** Live, kind-cluster-plex, 2026-10-07 08:21 UTC
(`nats:2.14.6-alpine`, one node, account `$G`):

- `num_pending` is authoritative only on the consumer leader: `setLeader` resets
  it on a step-down ("these are only authoritative on the leader",
  `consumer.go:1728-1729`) and only the leader counts new messages
  (`consumer.go:7082-7084`), so a follower reads 0. On the chart's 3-node R3
  default a `/jsz` read that lands on a follower, with no pods running and so no
  ack-pending, would leave a domain at zero asleep while its leader holds
  thousands of tasks; kind runs one server, the leader of everything, so every
  e2e would pass. KEDA's scaler reads `/jsz` and needs a `/varz` walk of every
  node to find the leader (2 + 2N calls per leader change).
- Port 8222 is unauthenticated, and the chart exposes it only per pod, through
  the headless Service.
- An `acc` that names no account answers HTTP 200 with no `account_details` at
  all (`monitor.go:3391-3408`; reproduced live with `acc=NOPE`): the silent
  failure behind KEDA issue #8165.
- It is a whole-account dump: 45,913 bytes and about 6 ms for 26 streams and 45
  consumers (110,325 bytes with `config=true`), against 897 bytes per
  `CONSUMER.INFO`.

**Why not a stream's `messages` count.** Same reading:

| Stream (retention) | `state.messages` | Lag of its durable(s) | The gap |
| --- | ---: | --- | --- |
| `CLUSTARR_WORK_SEGMENTARR` (WorkQueue) | 18,897 | `segmentarr-analyze` 16,559 + 4 = 16,563; the other three 0 | 2,334 TheIntroDB holds on `clustarr.work.segmentarr.sched.markers.*`, exactly |
| `CLUSTARR_RELEASES` (limits, 72 h) | 10,137 | `catalogarr-rss-matcher` 0 | the retention window, not work |
| `CLUSTARR_EVENTS` (limits, 168 h) | 346 | `catalogarr-history` 0, `catalogarr-redownload` 0 | the retention window |
| `CLUSTARR_WORK_INDEXARR` (WorkQueue) | 3 | `indexarr-rss` 0 | scheduled RSS polls on `.sched.` |

It counts future and dead work, measures retention on limits streams, and cannot
say which domain a shared stream's work is for (`CLUSTARR_WORK_CATALOGARR` feeds
the autoscaled `catalog` and the fixed `metadata`). `num_pending` alone is wrong
the other way: it never wakes a domain for a retry and scales down mid-handler
(the defect KEDA fixed in PR #3809, 2022).

**No NATS-native Kubernetes metrics adapter exists**: nothing in nats-io serves
`external.metrics.k8s.io` or `custom.metrics.k8s.io`, nats-server has no
`/metrics` route, and every published route to an HPA goes through Prometheus or
KEDA. So the manager's own External Metrics API (§9.4) stays, and nothing in
this section reads port 8222 or adds Prometheus. The deleted ScaledObject's
comment that KEDA "cannot yet read the two counters together… until
kedacore/keda#8166 ships" was wrong (KEDA has summed both since 2022; #8166 is
the `accounts=true` fix), and its PromQL summed every server's series without
`is_consumer_leader="true"`, which would have tripled ack-pending on R3:
`docs/autoscaling.md` (§12) must not carry the claim.

### 9.1 What the code does today (verified)

**Consumers** (`pkg/events/topology.go:651-870`). "MAP" is `MaxAckPending`, today
also every pod's handler count, through `ConsumerSpec.Subscription()`
`MaxInFlight: c.MaxAckPending` (topology.go:119). "Mem" is the stream's
single-node memory share after `ForSingleNode` scales it into 64 MiB
(`scaleToBudget`, topology.go:290-339); CATALOGARR's 6.89 MiB matches live.

| Consumer (line) | Stream / retention | AckWait / MaxDeliver / BackOff | MAP | Runs today |
| --- | --- | --- | --- | --- |
| catalogarr-rss-matcher (657) | CLUSTARR_RELEASES, limits 72h, Mem 27.6 MiB | 30s/6/1s..10m | 256 | catalogarr worker |
| catalogarr-search-high (664) | CLUSTARR_WORK_CATALOGARR, WQ, Mem 6.9 MiB | 120s/5/30s,2m,10m | 8 | worker |
| catalogarr-search-normal (671) | same | 120s/5/30s,2m,10m,1h | 8 | worker |
| catalogarr-grab (682) | same | 60s/5/10s,1m,5m | 16 | worker |
| catalogarr-metadata (689) | same | 60s/8/..6h | 32 | metadata gateway |
| catalogarr-artwork-fetch (700) | same | 60s/8/..6h | 32 | metadata gateway |
| catalogarr-artwork-render (711) | same | 60s/8/..6h | 32 (2 draws/process, `artwork.DefaultMaxConcurrentRenders`, handler.go:157) | artwork role |
| catalogarr-markers (724) | CLUSTARR_WORK_SEGMENTARR, WQ, Durable file | 60s/8/..6h | 32 | metadata gateway |
| catalogarr-segments-plan (733) | same | 60s/5/30s,2m | 8 | controller role (run.go:457) |
| segmentarr-analyze (742) | same | 30m/3/1m,10m | 4; the pod runs `--concurrency` (main.go:133) | segmentarr-worker |
| catalogarr-segments-result (751) | same | 60s/8/..10m | 32 | metadata gateway |
| catalogarr-history (758) | CLUSTARR_EVENTS, limits 168h, Mem 13.8 MiB | 30s/3/5s,30s | 512 | history role |
| catalogarr-redownload (772) | CLUSTARR_EVENTS, limits | 30s/6/..10m | 16 | worker (run.go:690) |
| importarr-scan/list/fileimport (800/807/814) | CLUSTARR_WORK_IMPORTARR, WQ, Mem 3.4 MiB | 60s; list BackOff 5m,30m,2h | 4/2/4 | importarr-worker |
| indexarr-rss (831) | CLUSTARR_WORK_INDEXARR, WQ | 60s/4 | 4 | indexarr |
| captionarr-fetch-high/normal (839/846) | CLUSTARR_WORK_CAPTIONARR, WQ, Mem 1.7 MiB | 90s/8/..6h | 16 each | captionarr-worker |
| squasharr-transcode-results (856) | CLUSTARR_WORK_SQUASHARR, WQ DiscardNew | 30s/10 | 1 | squasharr controller |
| clustarr-dlq-projector (864) | CLUSTARR_DLQ, limits 720h | 30s/3 | 64 | history role |
| pool durables (`TranscodeTaskConsumer`, 140-155) | CLUSTARR_WORK_SQUASHARR | 60s/16 | 64 | transcode pools via `Pull` |

**`MaxAckPending` is per-pod and consumer-wide at once.** `natsbus.Subscribe`
writes `MaxAckPending: max(sub.MaxInFlight,1)` through `CreateOrUpdateConsumer`
(natsbus.go:308-319), `Pull` the same (pull.go:51), `EnsureTopology` the topology's
value (topology_nats.go:44-48, 123). The last writer wins: live,
segmentarr-analyze reads 4 (topology) against `--concurrency=3`. JetStream caps
unacknowledged deliveries across every replica, so a second replica adds no
concurrency.

**Consume refills after a non-blocking callback.** nats.go `pullConsumer.Consume`
calls the handler and then immediately `decrementPendingMsgs` and `checkPending`
(nats.go@v1.53.1 `jetstream/pull.go:287-291`). natsbus's `dispatch` never blocks;
it parks overflow (subscription.go:104-131), bounded only because
`MaxAckPending == MaxInFlight` (subscription.go:42-49).

**Delayed naks stay ack-pending.** nats-server v2.15.0 `processNak` keeps the
message in `o.pending` (consumer.go:3249-3300), and `NumAckPending = len(o.pending)`
(consumer.go:3599). ConsumerInfo has no count of delayed messages (consumer.go:56-74).

**membus** claims one message and only then waits for a slot (membus.go:301-321),
has no consumer-wide cap, and its `Ensure` registers no consumers (membus.go:124-164).

**KEDA today** is one opt-in ScaledObject (`charts/clustarr/templates/scaledobject.yaml`,
`config/keda/captionarr-worker-scaledobject.yaml`) summing
`num_pending + num_ack_pending` for the two caption fetch consumers through the
prometheus scaler. `pkg/obs/metrics.WorkQueuePending` (domain.go:247-255) is
declared as "the KEDA scaling input", but nothing sets it.

#### 9.1.1 Which Deployments autoscale, per consumer

The reconciler finds Deployments by label, so installer names do not matter to it.

| Domain (label `autoscale.clustarr.io/domain`) | HPA | min | Consumers: Slots / new MaxAckPending | Scale-down window |
| --- | --- | --- | --- | --- |
| `catalog` | yes | 0 (max 1: throttled) | search-high 8/**8**, search-normal 8/**8**, grab 16/**16** (throttled, unchanged), artwork-render 4/32 | 300s |
| `events` | yes | 1 | rss-matcher 256/2048, history 512/4096, redownload 16/128, dlq-projector 64/512 | 300s |
| `import` | yes | 0 | scan 4/32, list 2/16, fileimport 4/32, `importarr-recycle` 1/8, `importarr-probe-high` 4/32, `importarr-probe-low` 4/32 (both on `CLUSTARR_WORK_PROBE`, §6.5.1) | 300s |
| `caption` | yes | 0 (max 1: throttled) | fetch-high 16/**16**, fetch-normal 16/**16** (throttled, unchanged) | 300s |
| `markers` (cmd/markers) | yes | 0 | segmentarr-analyze 1/8 | 1800s (AckWait 30m) |
| `metadata` | no, fixed 1 (ADR-0007, RPC) | – | metadata, artwork-fetch, markers, segments-result: Slots = MAP = 32 | – |
| `index` | no, fixed 1 (RWO SQLite or per-process limiter, RPC, facade) | – | indexarr-rss 4/4 | – |
| manager | no | – | segments-plan 8/8, squasharr-transcode-results 1/1 | – |
| engines, transcode pools | no (controller-rendered) | – | pool durables Slots 1, MAP 64 | – |

- Slots equal today's per-pod value, so a single replica behaves exactly as today,
  except: artwork-render gets 4 (the process draws only 2 at a time, and 32 slots
  would make the HPA under-count load 16×); segmentarr-analyze gets 1 (the
  shipped `--concurrency` default; the live 3 is carried by
  `CLUSTARR_CONSUMER_SLOTS`, §13 OD34); the two probe lanes get 4 each (today's 8
  concurrent probes).
- Autoscaled consumers get `MaxAckPending = Slots × events.AutoscaleReplicaCeiling`
  (8). Fixed consumers keep `MaxAckPending == Slots`, so their broker config does
  not change.
- **Throttled consumers keep `MaxAckPending == Slots` too**, though their
  domains autoscale: `catalogarr-search-high`, `catalogarr-search-normal`,
  `catalogarr-grab`, `captionarr-fetch-high`, `captionarr-fetch-normal`, listed
  with their reason in `pkg/agentdomain.Throttled()`. Their throughput is set
  by a limiter downstream of them, not by pod count, and today's
  consumer-wide cap (8/8/16, `pkg/events/topology.go:664-685`; 16 each for
  caption) is the only cluster-wide bound on what reaches it. Searches and
  grabs end in indexarr's one process with one limiter per host (R8,
  `app/indexer/search/fanout.go:351-366`), which queues every query on the
  indexer's `requestDelay` inside a 45 s budget (`MaxRPCBudget`,
  `fanout.go:55`); a query that gets no slot is a `paced` skip, a nil-error
  outcome (`pacedOutcome`, `fanout.go:405-410`). At `Slots × 8` a multi-node
  wanted sweep (104 searches in one second on 2026-10-06) would run 64
  federated searches into nzbgeek's 2 s delay and most would come back paced,
  and the search worker records an attempt after any answer
  (`app/catalog/worker/search/worker.go:362`), which turns the item's next
  sweep into `IndexOnly` (`wantedscan.go:155-156`): the item loses its one
  live search, the 2026-10-06 failure. Caption fetches block on the shared
  provider token bucket (`app/caption/worker/fetch/search.go:260`), so extra
  replicas add pods, not throughput, and hold lag (and the HPA) high. With the
  `floor(MaxAckPending/slots)` clamp the `catalog` and `caption` HPAs scale
  0..1: they still wake from zero, which is the owner's rule.
  `TestAutoscaledConsumersSizeMaxAckPendingForTheCeiling` exempts exactly
  `Throttled()` and asserts their `MaxAckPending == Slots` (§13 OD46).
- **A paced search is not an attempt.** Independently of the cap,
  `pkg/events/schema` gains `SkipReasonPaced` (the text of indexarr's
  unexported `skipPaced`, `app/indexer/search/select.go:62`, which
  `pacedOutcome` then uses), and the search worker does not call
  `recordAttempt` when every outcome is `SearchOutcomeSkipped` with
  `Error == schema.SkipReasonPaced`: no indexer was asked, so the item keeps
  its live search for the next sweep (the worker's own rule, "a search that
  never reached an indexer is not an attempt", worker.go:358-360).
  `TestAnAllPacedSearchIsNotRecordedAsAnAttempt` holds it.

**Limits-stream consumers** (R4 asked for a ruling per consumer):
`catalogarr-rss-matcher`, `catalogarr-history` and `clustarr-dlq-projector` stay in
`events` with minReplicas 1 (§3.5.4: on single node their streams are memory
streams with discard-old; history sees a constant event flow and the matcher a
burst at every RSS poll, and scale-to-zero would cold-start a pod caching every
Movie/Series/Episode, about 45 MB of JSON, every few minutes).
`catalogarr-redownload` moves from `catalog` to `events` (R4 amendment): in a
scale-to-zero domain a failed-download event would have to outlive the wake inside
a 13.8 MiB discard-old firehose; `events` is always up and holds the same caches.
**Every consumer in a min-0 domain is on a WorkQueue stream**, held by a guard (§9.9).

**Rule for min-0 domains:** queue consumers only, no `Serve` responder, no HTTP
listener other than metrics and probes, no timer. The one timer that broke this,
`fileimport.RecycleSweeper` (recyclebin.go:100-105, hourly, `EveryReplica`), becomes
the manager-published `importarr-recycle` task (§3.5.3). Scheduled publishes
(`WithScheduleAt`) are held on `.sched.` subjects no consumer filter matches
(natsbus.go:245-254), so a delay-profile grab wakes `catalog` only when it fires.

### 9.2 `pkg/events` and `pkg/agentdomain` changes

```go
// topology.go
type ConsumerSpec struct {
	// ...existing fields...
	// Slots is how many handlers one process runs for this durable: the
	// per-pod concurrency and the HPA AverageValue target. MaxAckPending is
	// the durable's cap across every process.
	Slots int
}
// AutoscaleReplicaCeiling sizes an autoscaled consumer's MaxAckPending:
// Slots × 8, so up to 8 replicas each fill their slots.
const AutoscaleReplicaCeiling = 8

func (c ConsumerSpec) Subscription() Subscription // MaxInFlight: c.Slots, MaxAckPending: c.MaxAckPending

// bus.go
type Subscription struct {
	// ...existing...
	MaxInFlight   int           // handlers this subscription runs at once (per process)
	MaxAckPending int           // the durable's cross-process cap (Pull writes it; Subscribe only binds)
	Drain         time.Duration // after ctx ends, how long running handlers keep their context; 0 = cancel at once
}

type ConsumerState struct {
	Pending       uint64    // JetStream NumPending: matching, not yet delivered
	AckPending    uint64    // NumAckPending: in a handler, waiting out a delayed nak, or lapsed
	MaxAckPending int
	ObservedAt    time.Time // ConsumerInfo.TimeStamp
}
func (s ConsumerState) Lag() uint64 { return s.Pending + s.AckPending }

type StreamAdmin interface {
	// ...existing four methods...
	// ConsumerState reads one durable's backlog. A missing stream is
	// ErrStreamNotFound, a missing durable ErrConsumerNotFound.
	ConsumerState(ctx context.Context, stream, durable string) (ConsumerState, error)
	// Missing names every stream, bucket, object store and consumer of t that does not exist (§3.5.2).
	Missing(ctx context.Context, t Topology) ([]string, error)
}

// errors.go
var ErrConsumerNotFound = errors.New("events: consumer not found")

// AckDeadline is how long a delivery attempt has before the broker redelivers
// it: Backoff[attempt-1] (the last entry past the end), else AckWait. It is
// membus's ackWaitFor (membus.go:357-369), promoted so both buses share it.
func AckDeadline(s Subscription, attempt uint64) time.Duration

// slots.go
const SlotsEnv = "CLUSTARR_CONSUMER_SLOTS" // "segmentarr-analyze=3,catalogarr-grab=8"
func ParseSlotOverrides(v string) (map[string]int, error)  // rejects n<1, duplicates, malformed
func SlotsFor(c ConsumerSpec, overrides map[string]int) int  // override, else c.Slots
```

`Topology.Validate` changes in three ways:

- every consumer has `Slots >= 1` and `MaxAckPending >= Slots`;
- the work-stream rule (`pkg/events/topology.go:384-389`), which today rejects
  a WorkQueue stream without `AllowMsgSchedules` unless it is named
  `StreamAdvisories` or `StreamWorkSquasharr`, instead exempts
  `StreamAdvisories` and **any WorkQueue stream with `Discard == DiscardNew`**
  (nats-server refuses schedules with DiscardNew, which the next rule already
  says). That covers squasharr and the new `CLUSTARR_WORK_PROBE` (§6.5.1);
  without it `EnsureTopology` returns `invalid topology`
  (`topology_nats.go:37-39`) and the manager cannot start.
  `TestDefaultTopologyValidates` covers the probe stream;
- a consumer on `StreamAdvisories` (a dead-letter watcher, §5.9) may set
  `MaxDeliver: -1`; every other consumer keeps `MaxDeliver > len(BackOff)`.

`TranscodeTaskConsumer` gets `Slots: 1`. The `PullSubscriber` doc
(bus.go:170-173) names `s.MaxAckPending` as the cross-puller cap.

**`pkg/agentdomain`** (a leaf importing only `pkg/events`) holds
`Domain{Name string; Autoscaled bool; MinReplicas int32; Consumers []string}`,
`Domains()` (every agent domain and `markers`, the §9.1.1 table), `Fixed()` (every
other topology consumer with the reason it is homed elsewhere: manager, pools),
`Throttled()` (consumer name → the downstream limiter that bounds it, §9.1.1),
and the constants `LabelDomain = "autoscale.clustarr.io/domain"`,
`AnnotationPaused = "autoscale.clustarr.io/paused"`,
`AnnotationMinReplicas = "autoscale.clustarr.io/min-replicas"`,
`MetricConsumerLag = "clustarr_consumer_lag"`. `internal/cli/agent/domains.go`
composes units per domain from it (§4.2.1).

**Amended 2026-10-07 (NATS research; `nats-hpa-metrics.md` §6, `nats-worker-pools.md`
§7).** `pkg/events` gains, in the order Wave 4f lands them:

```go
// envelope.go (M2): the envelope ID also rides in a clustarr header, because a
// fired schedule loses Nats-Msg-Id (nats-server scheduler.go:214-231).
const HeaderID = "Clustarr-Id"
// EnvelopeFromHeaders reads Nats-Msg-Id, else Clustarr-Id, and drops every
// transport header (case-insensitively): Nats-Schedule*, Nats-Scheduler,
// Nats-Expected-*, Nats-TTL, Nats-Rollup. ToHeaders writes both ID headers.

// bus.go (S5): the broker's timing of a bound durable.
type Timing struct {
	AckWait    time.Duration
	Backoff    []time.Duration
	MaxDeliver int
}
func (s Subscription) Timing() Timing
func (s Subscription) WithTiming(t Timing) Subscription // AckDeadline, nakDelay and Settle read it
func (t Timing) Equal(u Timing) bool                    // compares the deadlines AckDeadline derives

// topology.go and bus.go (S1): the handler budget, §3.5.6 as amended.
type ConsumerSpec struct{ /* ... */ HandlerTimeout time.Duration }
func (c ConsumerSpec) HandlerBudget() time.Duration // HandlerTimeout, else AckWait
type Subscription struct{ /* ... */ HandlerTimeout time.Duration } // the explicit budget only; 0: no bus heartbeat, no deadline
var ErrLapsed = errors.New("events: delivery lapsed; the broker has redelivered it")
var ErrHandlerBudget = errors.New("events: handler budget spent")
// WedgeReporter is implemented by both buses; the `bus` liveness check (§3.3) reads it.
type WedgeReporter interface{ Wedged() error }

// consumerstate.go (HPA): the counters QueueGauge exports separately.
type ConsumerState struct {
	Pending, AckPending uint64
	Waiting             int // NumWaiting: open pull requests; never part of Lag
	MaxAckPending       int
	ObservedAt          time.Time
}
// ErrConsumerUnavailable: the broker answered without the durable's cluster
// placement while the connection is to a cluster -- the "assigned, no Raft node
// yet" answer, whose zero state is not the durable's (nats-server
// jetstream_api.go:5675-5688). extmetrics answers 503 for it, as for any error.
var ErrConsumerUnavailable = errors.New("events: consumer state unavailable")

// streamfill.go (S10): an optional interface both buses implement.
type StreamFill struct{ Bytes, MaxBytes, Messages uint64 }
type StreamStater interface {
	StreamFill(ctx context.Context, stream string) (StreamFill, error)
}

// could-defer (S3, S4): an application retry schedule apart from the broker's
// BackOff, and the attempt a scheduled retry carries.
type ConsumerSpec struct{ /* ... */ Retry []time.Duration }
type Subscription struct{ /* ... */ Retry []time.Duration } // Settle's schedule; Backoff when empty
const HeaderAttempt = "Clustarr-Attempt"
```

`ConsumerState`'s doc comment states the metric as §9.0 amended does. A guard,
`TestAutoscaledDurablesNeverExpire` (§9.9), holds that no `ConsumerSpec` and no
rendered `jetstream.ConsumerConfig` sets `InactiveThreshold`: an autoscaled
durable must outlive zero replicas, or its metric becomes `ErrConsumerNotFound`
for good and its domain can never wake. Nothing sets it today
(`docs/research/queue.md:235`); the guard keeps it so. `ConsumerSpec.Heartbeat`
and `Subscription.Heartbeat` stay dead fields (D6; `ConsumerConfig` never renders
them): deleting them is the research's C6, not planned.

### 9.3 natsbus and membus: per-pod slots under a cluster-wide cap

R5's "stop writing per-pod MaxInFlight into MaxAckPending" is unsafe alone (R5
amendment). What changes:

**`Subscribe` binds; `Pull` writes the topology's cap.** `Subscribe` looks the
durable up and never writes its config (§5.9), so the manager's `EnsureTopology`
is the only writer of a static durable's `MaxAckPending`. `Pull` (dynamic pool
durables) writes `MaxAckPending: cmp.Or(sub.MaxAckPending, max(sub.MaxInFlight, 1))`.
That ends the live 4-versus-3 fight.

**Slot-gated pull** (`pkg/events/natsbus/subscription.go`, replacing `cons.Consume`
at natsbus.go:340):

- **State.** `subscription` keeps `running map[uint64]*delivery` keyed by stream
  sequence (each with `deadline` and `lapsed`), `live int` (running deliveries
  not lapsed), `lapsed int` (running deliveries past their deadline),
  `parked map[uint64]jetstream.Msg`, and a `wake` channel.
- **pullLoop.** If a parked delivery's sequence is no longer running and
  `slots-live > 0` and `lapsed < slots`, start it. Otherwise, while
  `lapsed < slots`, call
  `cons.Fetch(slots-live, jetstream.FetchContext(ctx with 30s deadline), jetstream.FetchHeartbeat(5s))`
  and dispatch each message as it arrives. When `live == slots`, wait on `wake`.
  Once `lapsed == slots` (the **lapsed cap**), the loop starts no handler: it
  keeps exactly one `Fetch(1)` open and parks whatever arrives, as today's
  `subscription.go` parks overflow, so a pull request is always waiting for
  JetStream to raise MAX_DELIVERIES, but no new work starts behind handlers
  that are still running.
- **Reclaim.** A reaper ticks every 250 ms. A running delivery past
  `deadline + 1s` is marked lapsed (`live--`, `lapsed++`, `wake`), where
  `deadline = dispatchedAt + events.AckDeadline(sub, attempt)` (BackOff[0] on a
  first delivery: 1 s for `catalogarr-rss-matcher`, 5 s for history and the DLQ
  projector, 10 s for grab, because nats-server replaces `AckWait` with
  `BackOff[0]`, `server/consumer.go:678-682`) and each `message.InProgress`
  moves it forward (`message` gains an `onProgress` hook). The handler keeps
  running with its context; its eventual settle behaves as today's late
  settle, and `lapsed--` when it returns. So a hung handler never pins a slot
  past its deadline, and a free slot always means an outstanding Fetch, which
  is what lets JetStream raise MAX_DELIVERIES (CLAUDE.md gotcha).
- **The bound.** A handler that is only slow (an apiserver stall during
  `grab.Decide`'s live Download list, rssmatcher/handler.go:309-321) lapses at
  the broker's deadline like a hung one. Without the lapsed cap every lapse
  would fetch a replacement, and running handlers per pod would be bounded only
  by the consumer-wide `MaxAckPending`, which the autoscaled consumers raise
  8× (rss-matcher 256 to 2048), all in one `events` pod on a single node, and
  the load would feed itself. With the cap, **handlers running in one pod never
  exceed `2 × slots`** (`slots` live plus at most `slots` lapsed). Parked
  messages run nothing; they lapse at the broker's deadline and are redelivered,
  and what one pod holds in total stays bounded by the consumer's
  `MaxAckPending`, as today's parking is. Today's handler bound is
  `running <= slots` with overflow parked (`subscription.go:42-49`); the new
  one doubles it only while handlers are past their deadline.
- **Drain.** When the Subscribe ctx ends the loop stops fetching; running handlers
  keep their context for up to `sub.Drain`, then it is cancelled, then the
  subscription waits. Agents set `Drain` from `--drain-timeout`, whose default is
  the longest AckWait of the process's durables (§3.5.6), so HPA scale-downs
  finish in-flight work instead of naking it.
- **`ConsumerState`** (natsbus/admin.go) calls `b.js.Consumer(ctx, stream, durable)`,
  one `CONSUMER.INFO` request (nats.go `jetstream/consumer.go:368-372`), and reads
  `CachedInfo()`, mapping `jetstream.ErrStreamNotFound` and
  `jetstream.ErrConsumerNotFound` to the events sentinels.

**Amended 2026-10-07 (NATS research; `nats-worker-pools.md` §0, §3, §6, §7).**
The research ran the built Wave 4c loop against an embedded nats-server v2.15.0
(the chart's 2.14.6 has byte-identical `deliveryCount`, `hasMaxDeliveries`,
`progressUpdate`, `processNak` and `checkPending`). What it changes here, in
priority order:

- **M1. Fetch only for free slots; never park** (D1, must). The pull-loop
  clause "Once `lapsed == slots` … it keeps exactly one `Fetch(1)` open and
  parks whatever arrives", the State's `parked` map, and The bound's sentence
  "Parked messages run nothing; they lapse at the broker's deadline and are
  redelivered, and what one pod holds in total stays bounded by the consumer's
  `MaxAckPending`" are **withdrawn**. Each redelivery of a parked message spends
  an attempt, so at the lapsed cap a pod drew message after message into one
  `Fetch(1)` and held each, never `InProgress`ed, until the attempt past
  `MaxDeliver`; the watcher then dead-lettered it as "acknowledgement timed out"
  without it ever running. In E11 (one deaf handler on a one-slot durable), 0 of
  8 healthy tasks ran and all 8 were dead-lettered within 17 s; a pod could hold
  `MaxAckPending - 2 x slots` such messages (1,536 for `catalogarr-rss-matcher`).
  Parking also dated a parked message's deadline from its start, not its
  delivery. Now `next()` returns `slots - live` when positive and **0**
  otherwise: at saturation the loop has no pull outstanding, and a message the
  pod cannot run stays `NumPending`, spends no attempt and stays available to
  other replicas. The bound is simply `live <= slots` and `lapsed <= slots`.
  `dispatch` never parks: a delivery that finds no free slot (it cannot, since a
  fetch asks only for free slots and slots only free up meanwhile), or one that
  arrives while the subscription stops, is handed back with a plain `Nak` so
  another replica gets it at once (C8). membus never parked and is unchanged.
- **The MAX_DELIVERIES account, corrected** (D4, S6). The Reclaim bullet's "a
  free slot always means an outstanding Fetch, which is what lets JetStream
  raise MAX_DELIVERIES" is withdrawn as a reason. The cause is an off-by-one:
  `deliveryCount` returns redeliveries (deliveries - 1, `consumer.go:4841-4849`),
  so the ack-timer path in `checkPending` (`:6187-6192`) catches a lapsed final
  delivery only for `MaxDeliver` 1, and for 2 or more only `getNextMsg`
  (`:4970-4990`), which runs only for a waiting pull request (`:5468`), raises it
  (E1). Until then the message holds a `MaxAckPending` place and counts as lag.
  A pull a stopping pod abandons does not stand in for a live one: nats.go
  unsubscribes its inbox when its context ends, and any `CONSUMER.INFO` prunes it
  (`consumer.go:3650-3653`), which the manager's QueueGauge sends every 30 s
  (E5: the advisory fired without an INFO and never after one). What rescues the
  message is the next pull from any replica, and, for a domain at zero, the lag
  metric's ack-pending, which wakes a pod; a final-attempt handler that does
  return is dead-lettered in process by `Settle`. If a prompt dead letter while
  saturated is ever wanted, the research's C1 (an overflow-group sentinel pull
  that never takes work) is the tool, not a pull that does. The comments at
  `natsbus/subscription.go`'s type doc and `fetch`, and `deadletter.go:77-80`,
  say this instead.
- **S2. A lapsed delivery is muted** (D3). The server keys `InProgress`, `Ack`
  and `Nak` by stream sequence, not by delivery (`consumer.go:2838-2893`,
  `:3249-3326`): a lapsed handler that heartbeats keeps its redelivered copy from
  ever timing out, even when that copy's handler hangs (E3), and its `Nak` moves
  the live copy's deadline. Once the reaper marks a delivery lapsed, its
  `InProgress` and `Nak` become local no-ops, logged at debug and counted
  (`clustarr_bus_muted_total{durable,op}`). `Ack` still goes (the work is done,
  and settling the live copy is right), and so does `Term` from `Discard`.
  membus mutes the same two calls on a delivery its sweep released.
- **S5. Timing comes from the bound durable** (D5). With `Subscribe` bind-only,
  the broker's `AckWait`, `BackOff` and `MaxDeliver` are the topology's, yet
  `AckDeadline`, `nakDelay` and `Settle` read the caller's compiled
  `Subscription`, so an older agent bound to a newer topology lapsed early or
  late and terminated on the wrong attempt, silently. `bindConsumer` returns the
  durable's `events.Timing` from `CachedInfo().Config` (on every bind and
  re-bind), and the subscription uses `sub.WithTiming(bound)` for all three; a
  caller whose timing differs gets one warning per bind, naming the skew. membus
  binds each topology durable with its timing and does the same.
- **Handler budget (S1).** For a subscription with `HandlerTimeout > 0`
  (`ConsumerSpec.Subscription()` copies the consumer's explicit `HandlerTimeout`,
  never the `AckWait` fallback, so a consumer without one, and every hand-built
  `Subscription`, keeps today's behaviour; §3.5.6 as amended says why), the bus,
  for each running delivery:
  (a) sends `InProgress` every `AckDeadline(timing, attempt)/3` while the handler
  runs and its budget is not spent, so long work needs no per-handler keep-alive
  (the six that exist stay harmless, and S2 mutes them after a lapse); (b) runs
  the handler under a context whose deadline is the budget, with cause
  `events.ErrHandlerBudget`; (c) once the reaper marks the delivery lapsed (now
  only a crashed process, a wedged runtime or a spent budget can cause one),
  cancels its context with cause `events.ErrLapsed` one `AckDeadline` later.
  Gauges `clustarr_bus_lapsed_handlers{durable}` and
  `clustarr_bus_saturated{durable}` (1 while `lapsed == slots`), for every
  subscription. Both buses implement `events.WedgeReporter`, also for every
  subscription: `Wedged()` names one that has sat at its lapsed cap for longer
  than `HandlerBudget()` plus its first-delivery deadline (a heartbeating handler
  never lapses, so only deaf, silent handlers fill the cap), and the `bus`
  liveness check (§3.3 as amended) fails on it, the only remedy for a goroutine
  that ignores its context. The transcode pools' lease renewal and segments' `TaskTimeout` already
  follow this pattern by hand; S1 makes it the bus's rule.
- **Heartbeats (S9).** Until S1 lands, and for every handler that keeps its own
  keep-alive, a heartbeat goes at most every third of the delivery's deadline, so
  two can be lost before a lapse: segments 30 s becomes 20 s (deadline 1 min,
  `BackOff[0]`), caption fetch 20 s becomes 10 s (`BackOff[0]` 30 s); fileimport
  10 s, rescan 3 s, artwork 10 s, RSS 20 s and transcode 20 s already fit.
  `TestHeartbeatsFitTheirDeadline` holds it (§5.15 as amended).
- **M2. A scheduled task keeps its ID** (D2, must; on main before this branch).
  When a schedule fires, the server copies the hold's headers, strips
  `Nats-Schedule*`, `Nats-Expected-*`, `Nats-Msg-Id`, `Nats-TTL` and
  `Nats-Rollup`, and adds `Nats-Scheduler` and `Nats-Schedule-Next: purge`
  (`scheduler.go:214-231`). So the handler saw `ID == ""`, the two broker
  headers fell into `Envelope.Headers`, and any copy published with them onto a
  stream without schedules was read as a request to purge a schedule and refused
  with 10188 "message schedules is disabled" (`jetstream_batching.go:889-904`):
  the in-process dead-letter path terminated the message without a copy, and the
  watcher's `copyLapsed` failed on every retry and, at `MaxDeliver -1`, naked for
  ever without deleting the WorkQueue original (E7, E12: the handler saw
  `["" ""]`; `CLUSTARR_DLQ` held 0). Had only the headers been fixed, every such
  dead letter on one durable would have shared the Msg-Id
  `dlq:<durable>:<attempts>` and all but the first been dropped as duplicates.
  Fix (§9.2 as amended): `ToHeaders` also writes the ID to `Clustarr-Id`,
  `EnvelopeFromHeaders` falls back to it and drops every transport header, so
  `DeadLetterEnvelope` and `replayEnvelope` clone clean headers; natsbus keeps
  `WithMsgID` for dedupe (which still applies at schedule time, E7). It covers
  every `WithScheduleAt` caller: delay-profile grabs (`grab/decide.go`),
  TheIntroDB re-asks (`markers/publish.go`), segment plans
  (`segmentplan/plan.go`) and RSS schedules (`rssschedule/schedule.go`). It is
  S4's prerequisite. Transcode tasks were never affected:
  `CLUSTARR_WORK_SQUASHARR` is DiscardNew and allows no schedules.
- **Slow consumers are reported (S7).** No `nats.ErrorHandler` was installed
  (`pkg/busconn/busconn.go:104-110`, both worker mains), so a core-NATS
  slow-consumer drop on a `Serve` responder, a KV watcher or a Fetch inbox was
  silent. `natsbus.New` installs `nc.SetErrorHandler` and
  `nc.SetDisconnectErrHandler` when the connection has none, counts
  `clustarr_nats_async_errors_total{kind}` (`slow_consumer`, `permission`,
  `disconnect`, `other`; never labelled by subject) and logs once per kind and
  subject per minute through `natsbus.WithLogger`. Every process gets it, since
  `busconn.Connect`, cmd/markers and cmd/transcode all build their bus with
  `natsbus.New`.
- **`ConsumerState` reads the consumer leader** (`nats-hpa-metrics.md` §2.1,
  §6.2). It is answered by the consumer leader only (`jetstream_api.go:5556,5649`),
  so it is accurate on an R3 cluster, where `/jsz` on a follower reads
  `num_pending` 0 (§9.0 as amended). One hardening: when the connection is to a
  cluster (`nc.ConnectedClusterName() != ""`) and the answer carries no
  `Cluster` placement, it returns `events.ErrConsumerUnavailable` rather than
  zero state; only the "assigned, no Raft node yet" answer looks like that
  (`jetstream_api.go:5675-5688`). It also fills `Waiting` from `NumWaiting`.
  membus never reports unavailable; its `Waiting` counts open `Subscribe` slots
  with no claim.

Not adopted from the research: C1 (sentinel pull), C2 (an application attempt
check at receipt), C3 (consumer pause: `EnsureTopology` would undo a pause
unless the rendered config carried `PauseUntil`, and extmetrics would have to
report 0 for a paused durable), C4 (`Nats-Expected-Last-Subject-Sequence` as a
one-queued-task-per-file guard; a candidate for the fold's transcode dispatch),
C5 (per-message TTL), C6, C7 and C9; overflow priority groups beside the HPA,
`pinned_client` for the single-replica domains, counters, and a return to
`Consume`. S3 and S4 are §9.6's, and S11 (one nats-server version: the chart
runs 2.14.6, kustomize and the tests 2.15.0) is left to the installers wave.

**membus parity:** Subscribe acquires a slot before `claimNext` (today it claims
first, membus.go:301-321); a delivery whose `cs.ackDeadline` passes while its
handler runs releases its slot once (the sweep that calls `deadLetterLapsed`
detects it), counting against the same lapsed cap, past which no slot is
released; `claim` refuses a durable whose unsettled delivered count has reached
its cap, stored per durable by `bindDurable(durable, filters, maxAckPending)`;
`Ensure` binds every topology consumer with its filters and cap (JetStream's
Ensure creates them); Subscribe on an unbound durable waits, as natsbus does;
`ConsumerState` counts as Pending the non-removed messages that match the filters,
are due (`deliverAt` reached), unclaimed or claimed by this durable, with no
attempts, and as AckPending those with `attempts>0 && !settled`; an unbound
durable is `ErrConsumerNotFound`.

**Contract cases** (`pkg/events/contracttest`, run by both buses):

- `ConsumerStateCountsPendingAndAckPending`: Ensure a topology with one test
  consumer, without subscribing; Pending must be N. Subscribe with a holding
  handler; AckPending equals the in-flight count. A `Retry(time.Hour)` nak stays in
  AckPending. A future `WithScheduleAt` counts in neither. A missing durable or
  stream returns its sentinel.
- `SubscriptionHoldsNoMoreThanItsSlots`: MaxInFlight 1, MaxAckPending 8, 5 tasks,
  handler blocked; after 2 s AckPending is 1 and Pending 4 (today's `Consume`
  gives 5).
- `ReplicasShareMaxAckPendingBeyondOneSlotCount`: two subscriptions on one durable,
  each MaxInFlight 2, MaxAckPending 4; four handlers run at once (today natsbus
  caps this at 2).
- `MaxAckPendingCapsEveryReplicaTogether`: the same with MaxAckPending 2; exactly 2 run.
- `LapsedHandlerFreesItsSlot`: MaxInFlight 1; handler 1 hangs past its deadline; a
  second task is handled within 3× the deadline (fails on today's natsbus and membus).
- `LapsedHandlersDoNotMultiplyConcurrency`: MaxInFlight 2, MaxAckPending 16,
  Backoff `[1s]`, a handler that sleeps 5 s, 20 tasks; at most 4 handlers ever
  run at once in the process, and all 20 are eventually acked.
- `ALapsedFinalDeliveryIsDeadLetteredWithABindOnlySubscribe`: after
  `Ensure`, a bind-only Subscribe (MaxInFlight 1, MaxDeliver 2) whose handler
  hangs through its final delivery; the message reaches `CLUSTARR_DLQ` and, on
  a WorkQueue stream, is removed. A second variant stops the subscription
  before the advisory fires and runs only `WatchDeadLetters` (the manager's
  backstop): the message is still dead-lettered.
- `SubscribeRebindsAVanishedDurable`: delete the durable and its stream under a
  running subscription, re-`Ensure`; the subscription resumes and a new task is
  handled (the NATS-restart case, §5.9).
- `DrainLetsRunningHandlersFinish`: cancel ctx mid-handler with Drain 1s; the
  handler's ctx is not cancelled and the message is acked.
- `SubscribeBindsAndNeverWritesConsumerConfig` and
  `SubscribeWaitsForAMissingDurable` (§5.9).
- `MissingNamesEveryAbsentTopologyObject` (§3.5.2).
- The existing hung-handler cases (contracttest.go:66-82) are unchanged and still pass.

**Amended 2026-10-07 (NATS research; `nats-worker-pools.md` §7 S8,
`nats-hpa-metrics.md` §6.6).** None of the cases above is in the tree yet (W4.42's
`contracttest.Bind` and W4.43-W4.45's cases wait for the test batch); D1, D2 and
D3 would each have failed one of the new ones. Added, on both buses:

- `SaturatedSubscriptionNeverDeadLettersAnUnrunTask` (M1; E11 as a test): one
  slot, a handler deaf to its context on every delivery of one message; eight
  healthy tasks published once the slot is wedged stay `Pending`, none reaches
  `CLUSTARR_DLQ`, and all eight run once the deaf handlers return.
- `AScheduledTaskKeepsItsIDAndItsDeadLetter` (M2; E12): two `WithScheduleAt`
  tasks whose handler discards them; the handler saw both IDs, and the DLQ holds
  two copies on two subjects. Its watcher variant,
  `AScheduledTasksLapsedFinalDeliveryIsDeadLettered`: a hung handler on a
  scheduled task with `MaxDeliver` 2; the copy lands and the WorkQueue original
  is deleted.
- `ALapsedDeliveryCannotExtendItsRedelivery` (S2; E3): delivery 1 lapses and
  keeps heartbeating, delivery 2 hangs silent, delivery 3 still arrives on the
  broker's schedule.
- `TimingComesFromTheBoundDurable` (S5): a durable bound with a 1 s deadline and
  `MaxDeliver` 2, subscribed with a caller's 30 s and 5; the hung handler's slot
  is reclaimed after about 2 s, and a failing task is dead-lettered at attempt 2.
- `TheBusHeartbeatsARunningHandler` and `AHandlerPastItsBudgetIsCancelled` (S1):
  with `HandlerTimeout` set, a handler running three deadlines is never
  redelivered, and one past its budget sees `context.Cause == events.ErrHandlerBudget`;
  a deaf one is cancelled with `events.ErrLapsed` one deadline after it lapses,
  and `Wedged()` names its subscription once it has sat at the lapsed cap past
  the budget.
- `ConsumerStateExcludesExhaustedMessages` (HPA): a WorkQueue message past
  `MaxDeliver` is in neither count before the dead-letter path removes it (the
  existing case already covers a `WithScheduleAt` hold). This one runs on the
  real server only (`pkg/events/natsbus`), on a raw consumer with no
  dead-letter watcher, since a bus subscription's own watcher closes the window
  at once; membus has no such window (its sweep dead-letters a lapsed final
  delivery in place).

`ALapsedFinalDeliveryIsDeadLetteredWithABindOnlySubscribe`'s second variant
changes (D4): it no longer relies on the pull the stopped subscription
abandoned. After the stop it reads `ConsumerState` (the `CONSUMER.INFO` that
prunes that pull), then brings a replica back (a second `Subscribe` whose
handler succeeds): the backstop copies the message once that replica's pull
lets JetStream raise the advisory, as `testLapseWhileUnwatchedToDLQ` already
does. Named `ByTheBackstopOnceAReplicaPullsAgain`.

### 9.4 The External Metrics API inside `cmd/manager`

**Runnables** (`app/autoscale/extmetrics`, registered by `app/autoscale.Register`):

| Runnable | Replicas | Does |
| --- | --- | --- |
| `Server` | every replica, as a `*manager.Server`, which controller-runtime starts in its HTTP-servers group **before the caches** (`pkg/manager/internal.go:446-451` starts caches before other runnables, so an `EveryReplica` would wait out a cache sync of up to `CacheSyncTimeout`, 10 min, on every restart) | TLS listener on `--external-metrics-bind-address` (`:6443`; `"0"` disables). It needs only the APIReader and `events.StreamAdmin`. Until the TLS Secret and the front-proxy CA are loaded it refuses the handshake, which the aggregator reads as unavailable; there is **no readiness check**: the API is reached through its own Service, which publishes not-ready addresses (below), so neither gates the other |
| `CertManager` | leader-only | creates the TLS Secret create-only, re-issues the serving cert under 30 days left, injects caBundle; every 60 s and at start |
| `QueueGauge` | leader-only | every 30 s sets `metrics.WorkQueuePending{stream,consumer}` to `Lag()` for every consumer in `events.Default()`; the gauge finally gets a producer, and its help text becomes "the autoscaling input" |
| `NamespaceGuard` | every replica | waits for ctx to end; then, with its own 10 s context, reads its Namespace through the APIReader and, if `deletionTimestamp` is set and `v1beta1.external.metrics.k8s.io`'s `spec.service.namespace` is this namespace, deletes the APIService. Namespace deletion removes pods first (graceful, SIGTERM), so the manager is still running when its namespace is terminating. Without this, `kubectl delete ns <release ns>` (or a harness recreating its namespace) leaves a cluster-scoped APIService pointing at a deleted Service; discovery for the group then fails and `NamespacedResourcesDeleter.Delete` returns that error before `finalizeNamespace` (k8s.io/kubernetes@v1.36.3 `pkg/controller/namespace/deletion/namespaced_resources_deleter.go:512-517`), so the release namespace, and every namespace deleted meanwhile, stays Terminating for good. `helm uninstall` deletes the APIService itself; the guard is a no-op then |

**Routes** (GET only; any other method is 405 with a `metav1.Status` JSON):

- `/apis/external.metrics.k8s.io/v1beta1` returns `metav1.APIResourceList`:
  `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"external.metrics.k8s.io/v1beta1","resources":[{"name":"clustarr_consumer_lag","singularName":"","namespaced":true,"kind":"ExternalMetricValueList","verbs":["get"]}]}`
  (the aggregator's availability probe and legacy discovery target).
- `/apis/external.metrics.k8s.io/v1beta1/namespaces/{ns}/{metric}?labelSelector=...`
  returns the list below.
- `/apis`, `/apis/external.metrics.k8s.io`, `/openapi/v2`, `/openapi/v3` and
  everything else return a 404 Status; the aggregator falls back to legacy
  discovery and skips OpenAPI on 404 (`handler_discovery.go`, `downloader.go`).
- The list body is hand-defined structs with the exact JSON tags of
  `k8s.io/metrics/pkg/apis/external_metrics/v1beta1` (`metricName`, `metricLabels`,
  `timestamp`, `window,omitempty`, `value`), `Content-Type: application/json`
  (the metrics client decodes by content type through `scheme.Codecs`). Example:
  `{"kind":"ExternalMetricValueList","apiVersion":"external.metrics.k8s.io/v1beta1","metadata":{},"items":[{"metricName":"clustarr_consumer_lag","metricLabels":{"stream":"CLUSTARR_WORK_CAPTIONARR","consumer":"captionarr-fetch-high"},"timestamp":"2026-10-06T20:00:00Z","value":"37"}]}`.

**Selection and errors.** The servable series are exactly `(stream, consumer)` for
every consumer of every autoscaled `agentdomain.Domains()` entry. `labelSelector`
is parsed with `k8s.io/apimachinery/pkg/labels.Parse` (empty means everything); a
series matches when `selector.Matches(labels.Set{"stream":…, "consumer":…})`. Each
matched series gets one `ConsumerState` call with a 5 s timeout, cached for 5 s.

| Case | Response |
| --- | --- |
| metric other than `clustarr_consumer_lag` | 404 |
| namespace other than the manager's (`k8s.Options.Namespace`) | 404 |
| unparseable selector | 400 |
| no match | 200, empty items (the HPA reports FailedGetExternalMetric, which makes a misconfiguration visible) |
| any matched `ConsumerState` error | 503, so the HPA holds its scale instead of acting on partial data |

**Authentication (`FrontProxyAuth`).**

- **Source.** ConfigMap `kube-system/extension-apiserver-authentication`, read
  through `mgr.GetAPIReader()` at start (retrying) and every 60 s: keys
  `requestheader-client-ca-file` (PEM); `requestheader-allowed-names`,
  `requestheader-username-headers`, `requestheader-group-headers` (JSON arrays);
  `requestheader-extra-headers-prefix`.
- **TLS config.** `MinVersion: tls.VersionTLS12`, `ClientAuth: tls.VerifyClientCertIfGiven`;
  `GetConfigForClient` returns the current `ClientCAs` and `GetCertificate`.

| Request | Outcome |
| --- | --- |
| no verified chain | 401 |
| a cert from another CA | fails the TLS handshake |
| leaf CN not in allowed names (an empty list allows any CN the CA signed) | 403 |
| no username header | 401 |
| otherwise | served |

- **No authorization.** The kube-apiserver has already authenticated and
  RBAC-authorized the caller before proxying; only it holds the front-proxy client
  key; SubjectAccessReview is a "should" for extension servers. The HPA
  controller's bootstrap ClusterRole already grants get/list/watch on
  `external.metrics.k8s.io` (verified live).

**TLS material and the APIService.**

- **The Secret.** `--external-metrics-secret` (default `external-metrics-tls`;
  the chart sets `<fullname>-external-metrics-tls`), type `kubernetes.io/tls`, keys
  `tls.crt`, `tls.key`, `ca.crt`, `ca.key`. CA: ECDSA P-256, 10 years. Serving
  cert: 1 year, SANs `<svc>`, `<svc>.<ns>`, `<svc>.<ns>.svc`,
  `<svc>.<ns>.svc.cluster.local`, `<svc>` from `--external-metrics-service`
  (default `external-metrics`; the chart sets `<fullname>-external-metrics`).
  On a first install the one manager pod is the leader and creates it; a later
  non-leader replica (a rollout's new pod) serves the Secret its predecessor
  created.
- **Rotation.** CertManager creates the Secret create-only and renews with an
  `Update` carrying the read `resourceVersion` (CAS); servers reload it through
  the APIReader every 60 s.
- **caBundle injection.** CertManager reads `v1beta1.external.metrics.k8s.io` as
  unstructured through the APIReader and injects only when `spec.service` names its
  own namespace and Service. Otherwise (KEDA or prometheus-adapter owns the name)
  it sets nothing and emits a Warning Event `ExternalMetricsAPIServiceForeign` on
  the manager Deployment. The apply is
  `{apiVersion: apiregistration.k8s.io/v1, kind: APIService, metadata.name, spec.caBundle}`
  through `k8s.Apply` under `clustarr-autoscale`, with a small hand-written apply
  configuration implementing `k8s.ApplyConfiguration` (controller-runtime's typed
  Apply needs only the getters plus REST mapping, client/typed_client.go:151-186,
  client_rest_resources.go:104-113), so `k8s.io/kube-aggregator` is not linked.
- **The APIService the installers render:** `group: external.metrics.k8s.io`,
  `version: v1beta1`, `service {name: <external-metrics svc>, namespace, port: 443}`,
  `groupPriorityMinimum: 100`, `versionPriority: 100`,
  `insecureSkipTLSVerify: false`, no `caBundle` (§10.2.2).
- **Service.** A dedicated Service `<fullname>-external-metrics` (kustomize
  `external-metrics`), selecting the manager pods, port `https` 443 →
  targetPort `extmetrics` (containerPort 6443), with
  `publishNotReadyAddresses: true`, so the aggregator reaches a manager whose
  caches are still syncing; the `manager` Service keeps only `metrics`. The
  APIService names this Service.

**Flags on `cmd/manager`** (§3.4.1): `--autoscale` (default true; false skips the
reconciler, Server, CertManager and NamespaceGuard, keeps QueueGauge, and
deletes every HPA in its namespace labelled `autoscale.clustarr.io/domain` once
at start),
`--external-metrics-bind-address`, `--external-metrics-service`,
`--external-metrics-secret`.

**Amended 2026-10-07 (NATS research; `nats-hpa-metrics.md` §5.4, §6.3, §6.4;
`nats-worker-pools.md` S10).**

- **One read path with a singleflight.** The per-series cache moves out of the
  `Handler` into `extmetrics.StateCache` (5 s TTL, 5 s timeout per read), which
  puts a `golang.org/x/sync/singleflight` group in front of each
  `(stream, consumer)`: a burst of aggregator retries, or several HPAs asking at
  once, makes one `CONSUMER.INFO`. The `Handler` and `QueueGauge` share the one
  cache in a process, so the leader never double-polls. Cadence stays
  negligible: the HPA controller syncs every 15 s, §9.1.1's five HPAs carry 17
  External series, so at most 17 requests per 15 s reach the broker whatever the
  number of manager replicas, plus the gauge's static consumers (about 30) every
  30 s on the leader: about 2 requests and 2 KB per second, each under a
  millisecond, on an info queue whose limit is 10,000. No other background
  poller is added.
- **`QueueGauge` exports each counter.** Beside `clustarr_work_queue_pending`
  (the lag, unchanged), it sets `clustarr_consumer_pending`,
  `clustarr_consumer_ack_pending`, `clustarr_consumer_waiting` and
  `clustarr_consumer_max_ack_pending`, each `{stream,consumer}` (cardinality
  fixed by the topology), and deletes all five series of a consumer it cannot
  read. A durable stalled at its cap by long delayed naks
  (`ack_pending == max_ack_pending` with `waiting > 0`) is then visible without
  an exporter; it is a diagnostic, not a scaling input.
- **`QueueGauge` sets `clustarr_stream_fill_ratio{stream}`** (S10) for every
  stream of the topology, from `events.StreamStater.StreamFill` (STREAM.INFO:
  bytes over `MaxBytes`). Lag cannot show a single-node memory stream silently
  discarding its oldest messages: the 2026-10-01 incident dropped 12,161 unseen.
  `docs/observability.md` documents an alert at 0.8.

### 9.5 The autoscale reconciler

**Wiring.** `app/autoscale/controller.Reconciler`, built as
`ctrl.NewControllerManagedBy(mgr).Named("autoscale")`:
`.For(&appsv1.Deployment{}, predicate: has label autoscale.clustarr.io/domain)`,
`.Owns(&autoscalingv2.HorizontalPodAutoscaler{})`,
`.Watches(&corev1.Node{}, EnqueueRequestsFromMapFunc(every labelled Deployment from the cache))`.
`pkg/k8s.AddToSchemeFuncs` (scheme.go:48-62) gains `autoscalingv1.AddToScheme`
(the Scale subresource) and `autoscalingv2.AddToScheme`.

**Declaration: a Go table, not annotations.** The domain table is
`pkg/agentdomain` (§9.2). The reconciler's pure functions `MatchingNodes` and
`RenderHPA` live in `app/autoscale/controller` (which imports
`k8s.io/component-helpers`). Per-pod slots resolve through
`events.SlotsFor(spec, overrides)`, the overrides read from the literal `value` of
env `CLUSTARR_CONSUMER_SLOTS` on the Deployment's pod template; the agent and
markers read the same env, so each install has one source of truth. That env
replaces markers' `--concurrency` (live 3; the chart's `segmentarrWorker.concurrency`
becomes `markers.consumerSlots`). A `valueFrom` env, two containers that disagree,
or an override naming a consumer outside the domain gets a Warning Event
`InvalidSlots`, and the topology default is used. The annotation
`autoscale.clustarr.io/min-replicas: "<n>"` raises minReplicas above the domain's
default (config/e2e uses it, §10.4).

**maxReplicas.** Count Nodes from the cache that are all of: `NodeReady=True` and
`!spec.unschedulable`; matched by
`nodeaffinity.GetRequiredNodeAffinity(&corev1.Pod{Spec: tmpl.Spec}).Match(node)`
(nodeSelector and required affinity); free of any untolerated taint with effect
NoSchedule or NoExecute (`corev1helpers.FindMatchingUntoleratedTaint`;
`k8s.io/component-helpers/scheduling/corev1`, signatures at v0.36.3 helpers.go:79
and nodeaffinity.go:306-323); `Allocatable[r] > 0` for every requested resource
other than cpu, memory, ephemeral-storage and `hugepages-*`. This generalises
squasharr's `gpuNodes` (capacity.go:43-75). `k8s.io/component-helpers v0.37.0`
(matching `k8s.io/api v0.37.0`) is added to go.mod once, serially, by the
controlling session; it needs the network once (only v0.36.3 is in the module cache).

`computed = max(1, min(n, min over consumers floor(MaxAckPending/slots)))`, so no
replica is added that a consumer could not feed, and
`maxReplicas = max(minReplicas, computed)`, because the apiserver rejects an
HPA whose `maxReplicas` is below its `minReplicas` with an error on
`spec.maxReplicas` (k8s.io/kubernetes@v1.36.3
`pkg/apis/autoscaling/validation/validation.go:67-68`), which the
`ScaleToZeroUnavailable` fallback below would never fix. `n == 0` gets a
Warning Event `NoMatchingNodes`; a floor of 0 (an override larger than
MaxAckPending) gets `SlotsExceedMaxAckPending`; a `min-replicas` annotation
above `computed` (for example `"2"` on one node) gets
`MinReplicasAboveCapacity`.

**The HPA**, applied with `k8s.Apply(ctx, c, k8s.ManagerAutoscale, ac)`, built with
`k8s.io/client-go/applyconfigurations/autoscaling/v2`, a complete declaration on
every apply:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: <deployment name>
  labels: {autoscale.clustarr.io/domain: caption, app.kubernetes.io/part-of: clustarr, app.kubernetes.io/component: <deployment's component>}
  ownerReferences: [{apiVersion: apps/v1, kind: Deployment, name: <deployment>, uid: <uid>, controller: true}]  # no blockOwnerDeletion: it would need deployments/finalizers
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: <deployment>}
  minReplicas: 0
  maxReplicas: 1            # kind-cluster-plex: one node
  metrics:
  - type: External
    external:
      metric: {name: clustarr_consumer_lag, selector: {matchLabels: {stream: CLUSTARR_WORK_CAPTIONARR, consumer: captionarr-fetch-high}}}
      target: {type: AverageValue, averageValue: "16"}
  - type: External
    external:
      metric: {name: clustarr_consumer_lag, selector: {matchLabels: {stream: CLUSTARR_WORK_CAPTIONARR, consumer: captionarr-fetch-normal}}}
      target: {type: AverageValue, averageValue: "16"}
  behavior:
    scaleUp: {stabilizationWindowSeconds: 0, selectPolicy: Max, policies: [{type: Pods, value: 1, periodSeconds: 15}]}   # value = maxReplicas
    scaleDown: {stabilizationWindowSeconds: 300, selectPolicy: Max, policies: [{type: Percent, value: 100, periodSeconds: 15}]}
```

With AverageValue the HPA computes `ceil(sum/target)` per metric and takes the
maximum; from 0 it wakes on any value of at least 1; the first step up from 0 is
the Pods policy, so value = maxReplicas lets one step reach the needed count
(replica_calculator.go, horizontal.go).

**Wake guard.** An HPA wakes only a workload it scaled to zero itself
(horizontal.go:814-822,1609-1625). So before creating an HPA for a Deployment at
`spec.replicas == 0`, the reconciler sets the Scale subresource to 1
(`c.SubResource("scale").Update`, RBAC `deployments/scale` update), and does the
same whenever its HPA reports `ScalingActive=False` with reason
`ScalingDisabled`, no `ScaledToZero=True`, and the Deployment at 0, so a hand
`kubectl scale --replicas=0` does not strand a domain. The annotation
`autoscale.clustarr.io/paused: "true"` makes the reconciler delete its HPA and
leave replicas alone: the supported way to pause an agent.

**Other cases.** If the apply fails `Invalid` on `spec.minReplicas` (HPAScaleToZero
off, or a cluster older than 1.37), re-apply with minReplicas 1 and emit Warning
`ScaleToZeroUnavailable`. An HPA of that name without our controller ownerRef gets
Warning `AutoscaleConflict` and no apply. A Deployment that loses its domain label
loses its HPA.

**RBAC** (kubebuilder markers on the reconciler and extmetrics, generated into the
manager role): `autoscaling` horizontalpodautoscalers get, list, watch, create,
patch, delete; `apps` deployments get, list, watch; `apps` deployments/scale
update; core nodes get, list, watch (already granted to squasharr);
`apiregistration.k8s.io` apiservices, resourceNames
`v1beta1.external.metrics.k8s.io`, get, patch, delete (delete for
`NamespaceGuard`); core namespaces get (`NamespaceGuard` reads its own; a
ClusterRole cannot name a namespace it does not know at generation time, and
the grant is a read); core secrets get, create, update;
`events.k8s.io` events create, patch. Installer-rendered: a RoleBinding in
`kube-system` from Role `extension-apiserver-authentication-reader` to the manager
ServiceAccount (§10.2.2).

### 9.6 The MaxAckPending ownership change: consequences

- **In-flight limits.** Per pod: `slots` live handlers plus at most `slots`
  more that are past the broker's deadline (the lapsed cap, §9.3), so at most
  `2 × slots`, against today's `slots`; the extra half exists only while
  handlers are slow or hung. Per consumer: `MaxAckPending = slots × 8`
  (`slots` for throttled consumers). On one node (maxReplicas 1) each
  consumer's concurrency equals today's while no handler lapses. Delayed naks now consume an 8× larger
  budget before they stall the queue, so the stall the `catalogarr-markers`
  comment describes (topology.go:719-722) becomes 8× harder to reach on autoscaled
  consumers.
- **Scale-to-zero wake.** A lag of at least 1 keeps a domain at 1 or more while any
  item exists: the owner's rule. A pending-only metric would never wake a domain
  at zero for a matured redelivery or a crashed pod's lapsed delivery, because both
  stay in `o.pending`.
- **NakWithDelay backoff.** Staying at 1 or more during a backoff is the owner's
  rule. The cost is over-provisioning beyond 1: `ceil((P+F+D)/slots)` counts
  delayed messages (D) as work, bounded by `D <= MaxAckPending` and by
  maxReplicas. ConsumerInfo cannot separate D, so this is accepted; on one node it
  costs nothing (§13 OD27).
- **Scale-down** removes pods while in-flight work remains; the drain (§9.3) lets
  it finish, and the window of at least 300 s limits how often it happens.
- **Cold start.** HPA sync (15 s) plus pod start plus cache sync (catalog indexes
  were live 11 s after start): expect about 30-60 s. A manager restart no longer
  adds to it: the External Metrics API serves before the manager's caches sync
  (§9.4).

**Amended 2026-10-07 (NATS research; `nats-worker-pools.md` §3.2, §4.4, §7 S3,
S4). Could-defer: these change consumer config on durables that are live, so
they land last (plan W4.114, W4.115, after the wave gate W4.113) and may wait past release N with the
owner's say.**

- **S3. Work durables drop the broker `BackOff`.** `BackOff` replaces `AckWait`
  and doubles as the first delivery's processing deadline (1 s for
  `catalogarr-rss-matcher`, 5 s for history), it makes every delayed nak wait
  `BackOff[n-1] - BackOff[0]` longer than asked (E6: asked 1 s, got 3 s; the
  compensation `natsbus.nakDelay` exists only for it), and it makes the broker
  discard declared `AckWait`s (D7: `segmentarr-analyze` declares 30 m and the
  broker stores 1 m). Instead the retry spacing moves to `ConsumerSpec.Retry`,
  which `Settle` reads for `NakWithDelay` (it reads `Backoff` today), and
  `AckWait` becomes the one broker deadline. Each consumer keeps its present
  `AckWait`, which is then both its declared budget and its crash-detection
  deadline (a non-heartbeating handler is redelivered after its full budget
  rather than after `BackOff[0]`; a heartbeating one, fileimport or the rescan,
  is unaffected), except where a heartbeating handler declared a budget far
  above its crash-detection need: `segmentarr-analyze` gets `AckWait` 1 m, its
  30 m budget already being its explicit `HandlerTimeout` (§3.5.6 as amended,
  W4.97), so `HandlerBudget()` and everything sized from it are unchanged. The
  transcode durables already work this way (`topology.go:157-177`). Both fields
  update in place on a live durable; no recreation. Cost: a crash loop is
  redelivered every `AckWait` rather than on a growing schedule, still capped by
  `MaxDeliver`, and a crashed search or grab is noticed after its `AckWait`
  (120 s, 60 s) rather than after `BackOff[0]` (30 s, 10 s).
- **S4. Long retries become scheduled republishes** (needs M2 and S3). For a
  retry delay of 5 minutes or more (the 1 h and 6 h steps of metadata, artwork,
  markers and caption; import-list's 30 min and 2 h), `Settle`'s nak becomes:
  republish the envelope with `WithScheduleAt(now+d)`, the attempt in
  `Clustarr-Attempt` (a republish restarts JetStream's delivery count), under the
  Msg-Id `<id>/retry/<attempt>`; then `Ack`. The attempt `Settle` judges is the
  larger of the broker's and the header's. A waiting retry then holds no
  `MaxAckPending` place and is not lag, so `caption` and `catalog` scale to zero
  during a 6 h backoff instead of idling one pod (the "NakWithDelay backoff"
  bullet's accepted over-provisioning, OD27, goes), and the `catalogarr-markers`
  stall the topology's comment describes ends. Only streams that allow schedules
  take it: the DiscardNew `CLUSTARR_WORK_SQUASHARR` and `CLUSTARR_WORK_PROBE`
  keep `NakWithDelay`. A memory stream loses a scheduled retry on a NATS restart
  exactly as it loses a naked one, so this is no weaker.

### 9.7 Installers

The chart and kustomize side (the `autoscaling.enabled` value, the domain label
and no `spec.replicas` on autoscaled Deployments, `consumerSlots` values, the soft
hostname `topologySpreadConstraints`, the APIService, the Service port, the
kube-system RoleBinding, and never a templated HPA, because Helm SSA would conflict
with the reconciler's maxReplicas) is §10.2.2; the KEDA removal list is §10.2.7;
`docs/autoscaling.md` replaces the KEDA docs (§12).

### 9.8 Failure modes

| Failure | Effect | Handling |
| --- | --- | --- |
| Manager down or crash-looping | HPAs read FailedGetExternalMetric and hold scale; agents at 0 stay at 0. The APIService is unavailable, so namespace deletion anywhere stalls until it returns: `NamespacedResourcesDeleter.Delete` returns the aggregated discovery error before `finalizeNamespace` (pkg/controller/namespace/deletion/namespaced_resources_deleter.go@release-1.37) | documented; the reconcilers are down too |
| The release namespace is deleted (`kubectl delete ns`, a kustomize user, a test harness) | the manager and its Service go, the cluster-scoped APIService stays, and without handling the deletion is a **deadlock**, not a stall: the manager never comes back | `NamespaceGuard` deletes the APIService during the manager's shutdown (§9.4); if the manager was already down, `docs/autoscaling.md` gives the manual recovery `kubectl delete apiservice v1beta1.external.metrics.k8s.io` |
| Manager restart (OOMKill, eviction, node reboot) | before this design, a 10-minute-bounded cache sync would have kept the API away; now the Server answers as soon as it listens (§9.4) | – |
| NATS down | 503, HPAs hold | agents could not work anyway |
| NATS restarted (single node: memory-backed streams, consumers and buckets are gone) | `ConsumerState` returns not-found, extmetrics answers 503 and every HPA holds; running subscriptions stop fetching; an agent that restarts meanwhile waits in `AwaitTopology` | `busconn.KeepTopology` re-ensures on reconnect and every 60 s while anything is missing; subscriptions re-bind on their own (§5.9); queued work in memory streams is lost, as today |
| Another external-metrics provider (KEDA, prometheus-adapter) | one APIService per group per cluster | Helm refuses to adopt the existing object (loud); set `autoscaling.enabled=false`. A kustomize overwrite is caught by CertManager's `spec.service` check |
| HPAScaleToZero off | minReplicas 0 rejected | re-applied with min 1 and Event `ScaleToZeroUnavailable` |
| No node matches | maxReplicas 1, pod Pending | Event `NoMatchingNodes` |

**Amended 2026-10-07 (NATS research; `nats-hpa-metrics.md` §5.5, §6.5;
`nats-worker-pools.md` §6, §7).** Rows added:

| Failure | Effect | Handling |
| --- | --- | --- |
| Consumer leader election (R3) | `CONSUMER.INFO` times out, or members answer 10008 (`JSClusterNotAvail`), for the seconds the election takes; the new leader recomputes `NumPending` at takeover (`consumer.go:1811-1812`) | that series is a 503: the HPA scales up on the others and holds any scale-down (`horizontal.go:354`) |
| Consumer assigned, Raft node not yet up | a member answers with the config and zero state (`jetstream_api.go:5675-5688`) | `ConsumerState` returns `ErrConsumerUnavailable` when the connection is to a cluster and the answer has no placement (§9.3 as amended); the ≥300 s scale-down window would absorb it anyway |
| NATS monitoring port closed, or `http_port` unset | none | the design never reads it (§9.0 as amended) |
| A durable expires while its domain is at zero | its metric would be `ErrConsumerNotFound` for good, and the domain could never wake | no `ConsumerSpec` sets `InactiveThreshold`, held by `TestAutoscaledDurablesNeverExpire` (§9.9) |
| A durable stalls at `MaxAckPending` behind long delayed naks | lag at or above the cap, `waiting > 0`, nothing delivered; the HPA keeps pods that receive nothing | a diagnostic, not a scaling concern: `QueueGauge`'s separate gauges show `ack_pending == max_ack_pending` (§9.4 as amended); S4 (§9.6) removes the cause for long retries |
| Every slot of a pod held by handlers deaf to their context | before M1: the pod drew and parked messages it could not run until each was dead-lettered unrun (E11) | M1: it fetches nothing; queued work stays `Pending` for other replicas; S1's `bus` liveness check restarts the pod once it has sat at the lapsed cap past the handler budget (§3.3, §9.3 as amended) |
| A lapsed handler still heartbeats or naks | before S2: it kept the live copy from ever timing out (E3) | S2: muted after the lapse |
| A scheduled task fires | before M2: no ID, and its dead-letter copy refused with 10188; the watcher naked for ever | M2: `Clustarr-Id`, transport headers dropped |
| A core-NATS slow consumer (a `Serve` responder, a KV watch, a Fetch inbox) | the client drops messages | S7: counted in `clustarr_nats_async_errors_total{kind}` and logged |
| A single-node memory stream fills and discards its oldest | silent; lag cannot show it | S10: `clustarr_stream_fill_ratio{stream}`, alert at 0.8 |

### 9.9 Tests

**Unit, `pkg/agentdomain`:** `TestDomainsCoverEveryTopologyConsumer` (every
`events.Default()` work consumer, the dead-letter watchers on
`StreamAdvisories` aside, is in exactly one Domain or in `Fixed()`, and every
name exists: the KEDA guard's intent, that the autoscaler counts the consumers
the agent drains; every static consumer has exactly one watcher);
`TestMinZeroDomainsConsumeOnlyWorkQueueStreams`;
`TestAutoscaledConsumersSizeMaxAckPendingForTheCeiling`
(`MaxAckPending == Slots*AutoscaleReplicaCeiling`, except the consumers of
`Throttled()`, which must have `MaxAckPending == Slots` and a reason);
`TestFixedConsumersKeepMaxAckPendingEqualSlots`.

**Unit, `app/autoscale/controller`:** `TestMatchingNodes` (table: NotReady,
unschedulable, nodeSelector miss, `In`/`NotIn`/`Exists` affinity, NoSchedule taint
tolerated and not, a PreferNoSchedule taint ignored, an extended resource with
zero allocatable); `TestRenderHPA` (windows, per-consumer AverageValue, slot
overrides, the floor(MAP/slots) clamp, min 1 for `events`, the min-replicas
annotation, a min-replicas annotation above the computed maximum raising
`maxReplicas` with Event `MinReplicasAboveCapacity`, and `catalog` and
`caption` rendering maxReplicas 1 on six matching nodes).

**Unit, `app/autoscale/extmetrics`** (httptest TLS server with a generated
front-proxy CA): the authn table (no client cert 401; foreign CA handshake error;
disallowed CN 403; allowed CN without `X-Remote-User` 401; allowed and identified
200; no error response carries metric data); a discovery golden; an
ExternalMetricValueList golden, field names checked against k8s.io/metrics
`external_metrics/v1beta1` types.go; selector cases (`consumer=captionarr-fetch-high`
gives one item, `stream=CLUSTARR_WORK_CATALOGARR` the four catalog consumers,
`consumer in (a,b)`, empty means all); 404 for metric and namespace, 400, 405, 503
on a ConsumerState error (fake StreamAdmin), 404 for `/apis` and `/openapi/v2`; CA
reload after the ConfigMap changes. `TestNamespaceGuardDeletesTheAPIServiceOnlyWhileItsNamespaceTerminates`
(envtest: a terminating namespace with a matching `spec.service` deletes it; a
live namespace, or a foreign `spec.service.namespace`, does not).
`TestTheServerAnswersBeforeTheCachesSync` (a manager whose cache cannot sync
still serves the API).

**envtest** (Kubernetes 1.37.0, where minReplicas 0 is accepted):

- `TestReconcilerBuildsTheHPA`: 6 Nodes (2 matching, 1 cordoned, 1 NotReady, 1
  untolerated taint, 1 missing the selector label) and a labelled `caption`
  Deployment with nodeSelector give an HPA with maxReplicas 2, minReplicas 0, two
  External metrics at averageValue 16, both windows, a controller ownerReference,
  and `metadata.managedFields` only under `clustarr-autoscale`. Cordoning a node
  makes maxReplicas 1; an env override changes averageValue; a Deployment at 0
  with no HPA is scaled to 1 before the HPA appears; the paused annotation or
  removing the label deletes the HPA.
- `TestCAInjectorOwnsOnlyCABundle`: an installer-shaped APIService gets
  `spec.caBundle == ca.crt`; managedFields show `clustarr-autoscale` owning only
  `f:spec.f:caBundle`; a foreign `spec.service` gets no injection.

**Contract:** the §9.3 cases on membus and on a real embedded nats-server.

**Per-domain start envtest (R10):** for each min-0 domain a recording bus wrapper
asserts the domain calls `Subscribe` exactly for its table consumers, and never
`Serve`.

**Installer guards** are §10.3.2 (no HPA or ScaledObject rendered; the domain label
on exactly the autoscaled Deployments with no `spec.replicas`; the APIService on
443 with no caBundle and `insecureSkipTLSVerify` false; the kube-system
RoleBinding; `CLUSTARR_CONSUMER_SLOTS` parsing and naming only that domain's
consumers; the manager's ban on `k8s.io/apiserver`, `k8s.io/kube-aggregator`,
`k8s.io/metrics` and `sigs.k8s.io/custom-metrics-apiserver`).

**Phase H e2e:** `test/e2e/zzz_scale_from_zero_test.go`
`TestCaptionAgentScalesFromZero`, specified in §10.4.

**Amended 2026-10-07 (NATS research; `nats-hpa-metrics.md` §6.6,
`nats-worker-pools.md` §7 S8).** Added (the contract cases are listed in §9.3 as
amended; each new test is falsified, reverting its fix and watching it fail by
name):

- **Real NATS, a 3-node embedded cluster** (`pkg/events/natsbus`):
  `TestConsumerStateReadsTheConsumerLeader`: publish N to an R3 WorkQueue
  stream, step the consumer leader down
  (`$JS.API.CONSUMER.LEADER.STEPDOWN.<stream>.<durable>`), and `ConsumerState`
  still reads N once a leader answers. This is KEDA #3564's failure, and the
  only test that sees the gap between kind (one server) and the chart's R3
  default. `TestConsumerStateRefusesAnAnswerWithoutPlacement` drives
  `stateOf(info, clustered)` with a placement-less info.
- **Guard, `pkg/events`:** `TestAutoscaledDurablesNeverExpire`: no field of
  `ConsumerSpec` names an inactivity threshold, and `ConsumerConfig(c)` renders
  `InactiveThreshold == 0` for every consumer of `events.Default()`.
- **Unit, `app/autoscale/extmetrics`:** `TestStateCacheMakesOneReadPerSeriesUnderABurst`
  (100 concurrent reads of one series through a blocking fake make one call);
  `TestQueueGaugeAndTheAPIShareOneRead`; `TestQueueGaugeExportsEachCounter`
  (all five series per consumer, and all five deleted on an error);
  `TestQueueGaugeSetsStreamFill`.
- **natsbus:** `TestAsyncErrorsAreCounted` (a core subscription with a pending
  limit of 1 overflows; `slow_consumer` counts).
- **Guard, `test/guards`:** `TestHeartbeatsFitTheirDeadline` (§5.15 as amended).

---
## 10. Images, installers, RBAC and tests

The two images and how they are built; the Helm chart (`charts/clustarr`) and the
kustomize tree (`config/`); generated and hand-written RBAC; where every test now
in `cmd/clustarr`, `cmd/segmentarr-worker` and `cmd/squasharr-worker` goes, plus
the new guards; and the renames in `test/e2e` and `hack/e2e.sh`.

### 10.0 Names this section fixes

| Workload | kustomize name (= `app.kubernetes.io/component`, container, ServiceAccount) | chart name | image | binary |
| --- | --- | --- | --- | --- |
| manager | `manager` | `<fullname>-manager` | `clustarr` | `/usr/bin/manager` |
| catalog agent | `agent-catalog` | `<fullname>-agent-catalog` | `native` | `/usr/bin/agent` |
| event-stream agent | `agent-events` | `<fullname>-agent-events` | `native` | `/usr/bin/agent` |
| metadata agent | `agent-metadata` | `<fullname>-agent-metadata` | `native` | `/usr/bin/agent` |
| import agent | `agent-import` | `<fullname>-agent-import` | `native` | `/usr/bin/agent` |
| index agent | `agent-index` | `<fullname>-agent-index` | `native` | `/usr/bin/agent` |
| caption agent | `agent-caption` | `<fullname>-agent-caption` | `native` | `/usr/bin/agent` |
| markers | `markers` | `<fullname>-markers` | `native` | `/usr/bin/markers` |
| ui | `ui` | `<fullname>-ui` (unchanged) | `clustarr` | `/usr/bin/ui` |
| torrent/usenet engines (manager-rendered) | `<dc>-engine`, component `grabarr-engine`, SA `grabarr-engine` (unchanged) | same | `native` | `/usr/bin/agent` |
| transcode pools (manager-rendered) | Job `squasharr-pool-<profile>-<class>` (unchanged), component `squasharr-worker` becomes `transcode` | same | `native` | `/usr/bin/transcode` |

Unchanged because live data or cluster-plex depends on them: the `<fullname>-ui`
Service on port 8080 named `http` and its selector (cluster-plex's provider URIs
and every image URL Plex stored use it); the Secrets `<fullname>-ui-art-signing-key`
and `<fullname>-indexarr-facade` (kustomize `ui-art-signing-key`,
`indexarr-facade`); the PVCs `<fullname>-data` and `<fullname>-index`; the engine SA
`<fullname>-grabarr-engine` and every engine workload name and selector; the pool
Job prefix `squasharr-pool-` and the label `app.kubernetes.io/managed-by=squasharr`
(Jobs are listed by that label, `transcodejob/pools.go:88`, so the component rename
is safe).

### 10.1 Images

#### 10.1.1 Binary paths and self-checks

`pkg/binpath` (§3.1) holds `Manager = "/usr/bin/manager"`, `UI = "/usr/bin/ui"`
(clustarr image), `Agent = "/usr/bin/agent"`, `Markers = "/usr/bin/markers"`,
`Transcode = "/usr/bin/transcode"` (native image). `downloadclient` replaces
`clustarrBinary = "/usr/local/bin/clustarr"` (`workload.go:84`) and the `/bin/sh -c`
ordinal script (`workload.go:428-430,454`) with `Command: []string{binpath.Agent}`
(§3.5.5). `pool.Template` sets `Command: []string{binpath.Transcode}`; today it
sets none (`template.go:370-374`) and relies on
`ENTRYPOINT ["/usr/bin/squasharr-worker"]` (`images/Dockerfile.transcoder:169`).
Neither image sets an ENTRYPOINT: one image holds several binaries, so any
ENTRYPOINT would be wrong for most of them, and a container with no command fails
at once ("no command specified") rather than starting the wrong binary.

Two behaviour requirements on the binaries:

- **`--version`.** All five answer it: print `pkg/version.Version`, read no
  environment, exit 0. Image smoke tests use it.
- **`--self-check`.** `agent --self-check` and `markers --self-check` print a JSON
  report and exit 0 only when every check passes; `transcode --self-check=<class> [--trial]`
  exists today (`cmd/squasharr-worker/main.go:52-53`).
  - **agent:** `ffruntime.Load()` (FFmpeg 9 through ffgo, avcodec major 63, shim
    API) and `Require` of the import and caption needs; `par2.Available()` (par2go
    loads); the anacrolix piece completion compiled in is `sqlite`, not `bolt`
    (two build-tagged files set it: `//go:build cgo && !nosqlite` gives sqlite, the
    opposite tag bolt), which turns R13 into a check on the built artifact.
  - **markers:** `ffruntime.Load()` and `Require(decode.Needs)`; ONNX Runtime loads
    from `$ORT_LIB_PATH`.
  - The par2 child has its own `agent par2-repair --self-check` (§8.4.3).

#### 10.1.2 `images/Dockerfile.clustarr`: manager and ui

Created with `git mv images/Dockerfile.controller images/Dockerfile.clustarr`;
target `clustarr`, image `ghcr.io/mediactl/clustarr`.

```dockerfile
# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.27
# Empty unless `--build-context ffgo=<dir>` / `par2go=<dir>` replaces them (§10.1.5).
FROM scratch AS ffgo
FROM scratch AS par2go
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY --from=ffgo / /ffgo-unify/
COPY --from=par2go / /par2go/
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w -X github.com/mediactl/clustarr/pkg/version.Version=${VERSION}" -o /out/manager ./cmd/manager \
 && GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w -X github.com/mediactl/clustarr/pkg/version.Version=${VERSION}" -o /out/ui ./cmd/ui
FROM gcr.io/distroless/static:nonroot AS clustarr
LABEL org.opencontainers.image.source="https://github.com/mediactl/clustarr" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.title="clustarr"
COPY --from=build /out/manager /out/ui /usr/bin/
USER 65532:65532
```

Cross-compiling from `$BUILDPLATFORM` stays possible because both binaries are
CGO=0 and loader-free, which the deps guards enforce (§4.5).

#### 10.1.3 `images/Dockerfile.native`: agent, markers, transcode

Created with `git mv images/Dockerfile.transcoder images/Dockerfile.native`. Targets
`native` (`ghcr.io/mediactl/clustarr/native`) and `native-debug`
(`ghcr.io/mediactl/clustarr/native-debug`, `native` plus static busybox, exactly
as `transcoder-debug` was). `images/Dockerfile.media` is deleted: its ORT stage
(`Dockerfile.media:51-55,138-149`) moves here; its ffmpeg/ffprobe fetch (`:100-114`)
and par2 fetch (`:116-136`) go.

| Stage | Base | Does |
| --- | --- | --- |
| `ffgo`, `par2go` | `scratch` | empty defaults, replaced by named contexts (§10.1.5) |
| `build` | `golang:${GO_VERSION}-bookworm`, **target platform** (no `--platform=$BUILDPLATFORM`: the agent needs cgo, and release builds already run natively per arch) | copies `/ffgo` and `/par2go`, `go mod download`, then `CGO_ENABLED=1 go build -o /out/agent ./cmd/agent` (R13: sqlite piece completion and go-libutp stay), `CGO_ENABLED=0 … -o /out/markers ./cmd/markers`, `CGO_ENABLED=0 … -o /out/transcode ./cmd/transcode`; copies the ffgo `shim/` and licence exactly as today (`Dockerfile.transcoder:63-67`) |
| `ffmpeg` | `debian:bookworm-slim` | unchanged: the pinned BtbN 9.0 shared build (`FFMPEG_RELEASE`, `FFMPEG_VERSION`, `FFMPEG_SHA256_AMD64/ARM64`, `:77-80`) and `libffshim.so` |
| `ort` | `debian:bookworm-slim` | moved from `Dockerfile.media`: `ARG ORT_VERSION=1.23.2` and the two per-arch SHA-256s; installs `lib/libonnxruntime.so.${ORT_VERSION}` as `/out/usr/lib/libonnxruntime.so`, and `LICENSE` plus `ThirdPartyNotices.txt` into `/out/usr/share/licenses/onnxruntime/` |
| `par2` | `debian:bookworm-slim` with `cmake g++ git make binutils ca-certificates` | builds `libpar2shim.so` with par2go's `shim/build.sh` (§8.7), installs it at `/out/usr/lib/libpar2shim.so` with `/out/usr/share/licenses/par2cmdline-turbo/{COPYING,AUTHORS,SOURCE}` and `/out/usr/share/licenses/par2go/LICENSE` |
| `staging-base` | `debian:trixie-slim` (unchanged) | `COPY --from=ffmpeg /out/ /`, `COPY --from=ort /out/ /`, `COPY --from=par2 /out/ /`, `COPY --from=build /out/agent /out/markers /out/transcode /usr/bin/`, then licences |
| `staging` | as today | `stage.sh` and the Intel iHD/QSV stack on amd64 (`:135-149`), unchanged; then, since `/staging/usr/lib` exists only after `stage.sh`, the C++-symbol `nm` check of §7.6 item 2 |
| `native` | `scratch` | `COPY --from=staging /staging/ /`; `ENV FFGO_SHIM_DIR=/usr/lib ORT_LIB_PATH=/usr/lib/libonnxruntime.so PAR2GO_LIB=/usr/lib/libpar2shim.so LIBVA_MESSAGING_LEVEL=1 LIBVA_DRIVERS_PATH=/usr/lib/x86_64-linux-gnu/dri LIBVA_DRIVER_NAME=iHD`; `VOLUME ["/data"]`; `USER 1000:1000`; no ENTRYPOINT |
| `native-debug` | `native` | `COPY --from=busybox:1.37-musl /bin/ /bin/` plus `busybox-SOURCE`, as today |

The cgo agent is built against bookworm's glibc 2.36 and libstdc++ (GCC 12) and
staged with trixie's newer, backward-compatible glibc and libstdc++, the
arrangement the purego transcoder uses today (`:114-117`). go-libutp is C++
(`utp_api.cpp`; go-libutp@v1.3.2/utp.go:7), so the agent is dynamically linked
against libc, libm, libresolv, libstdc++ and libgcc_s (`ldd` of today's CGO=1
`cmd/clustarr` shows all five); `stage.sh`'s `trace` stages them automatically.

`images/distroless/stage.sh`:

- `:38` becomes `trace /usr/bin/agent /usr/bin/markers /usr/bin/transcode /usr/lib/libffshim.so /usr/lib/libpar2shim.so /usr/lib/libonnxruntime.so`;
  `trace` already fails when a file is missing (`:14`), so a binary dropped from
  the build fails the image.
- The refusal loop (`:76-81`) becomes `for f in ffmpeg ffprobe par2`, with the
  message "the native image carries no media executable".

Its header comment and `images/distroless/README.md` are rewritten for the native
image (targets, contents, size, the make targets of §10.1.6).

#### 10.1.4 One definition of the image checks: `hack/image-checks.sh`

`hack/image-checks.sh <image> <check>...` is the only place the image checks are
written down; the Makefile, `ci.yml` and `release.yml` all call it (the "one
definition of the e2e `go test` line" rule, `hack/e2e.sh:116-118`).

| check | runs |
| --- | --- |
| `version:<bin>` | `docker run --rm --read-only --cap-drop=ALL --user 1000:1000 --entrypoint /usr/bin/<bin> IMG --version` |
| `self-check:transcode:<class>` | the same flags, `--entrypoint /usr/bin/transcode IMG --self-check=<class>` |
| `self-check:agent`, `self-check:markers` | the same flags, `--entrypoint /usr/bin/<bin> IMG --self-check` |
| `self-check:par2-child` | the same flags, `--entrypoint /usr/bin/agent IMG par2-repair --self-check` |
| `no-shell` | `docker run --rm --entrypoint /bin/sh IMG -c true` must fail (`Makefile:194`, `ci.yml:200-203`) |
| `notices` (debug image) | each exists and is non-empty: `/usr/share/licenses/{ffmpeg/LICENSE.txt,ffmpeg/SOURCE,clustarr/LICENSE,ffgo/LICENSE,onnxruntime/LICENSE,par2cmdline-turbo/COPYING,par2cmdline-turbo/SOURCE,par2go/LICENSE}` and `/usr/share/doc/libc6/copyright` |
| `no-media-executables` (debug image) | `find / -xdev \( -name ffmpeg -o -name ffprobe -o -name par2 \) -type f` prints nothing |

#### 10.1.5 Building while go.mod replaces ffgo and par2go with local directories (R12)

Under R12, `go.mod` says `replace github.com/obinnaokechukwu/ffgo => ../ffgo-unify`
(`=> github.com/mediactl/ffgo v0.0.0-clustarr.9` at `go.mod:218` in the worktree
when this was drafted, `.12` on main since `ebbb2322`) and §8 adds
`replace github.com/mediactl/par2go => ../par2go`. Inside a build `/src/../ffgo-unify` is
`/ffgo-unify`, outside the build context, so `go mod download` fails in every
Dockerfile; ffgo is a direct requirement, so even `./cmd/manager` needs its
`go.mod`. Hence every Dockerfile's empty `ffgo` and `par2go` stages and
`COPY --from=… / /…/` before `go mod download` (§7.6); with the published
replaces they stay empty and unused.

GitHub CI cannot build or test the branch while a replace is local (the checkout
has no `../ffgo-unify`); `ci.yml` fails with that message (§10.1.7) rather than
obscurely in `go mod download`. The first push of this work must push the ffgo
tag and par2go and switch the replaces to tagged versions (R12 amendment).

#### 10.1.6 Makefile

Remove `MEDIA_IMG`, `TRANSCODER_IMG`, `TRANSCODER_DEBUG_IMG` (`:16-17,178`),
`TRANSCODER_CLASSES` (`:181`), `docker-build-transcoder` and
`docker-selfcheck-transcoder` (`:183-196`). Then:

```make
IMG ?= ghcr.io/mediactl/clustarr:dev
NATIVE_IMG ?= ghcr.io/mediactl/clustarr/native:dev
NATIVE_DEBUG_IMG ?= ghcr.io/mediactl/clustarr/native-debug:dev
NATIVE_CLASSES ?= cpu cuda intel
FFGO_DIR ?= ../ffgo-unify
PAR2GO_DIR ?= ../par2go
FFGO_REF ?= v0.0.0-clustarr.13
PAR2GO_REF ?= 1bd94eb
CONTEXTS ?= $(or $(TMPDIR),/tmp)/clustarr-contexts-$(USER)
LOCAL_CONTEXTS := $(if $(shell grep -qE '^replace github.com/obinnaokechukwu/ffgo => \.\./ffgo-unify$$' go.mod && echo y),--build-context ffgo=$(CONTEXTS)/ffgo --build-arg FFGO_COMMIT=$(shell git -C $(FFGO_DIR) rev-parse $(FFGO_REF)^{commit})) \
                  $(if $(shell grep -qE '^replace github.com/mediactl/par2go => \.\./par2go$$' go.mod && echo y),--build-context par2go=$(CONTEXTS)/par2go --build-arg PAR2GO_COMMIT=$(shell git -C $(PAR2GO_DIR) rev-parse $(PAR2GO_REF)^{commit}))
NATIVE_ASSETS ?= $(GOBIN)/native-assets
LDFLAGS := -s -w -X github.com/mediactl/clustarr/pkg/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build: ## Build bin/manager, bin/ui, bin/agent, bin/markers and bin/transcode.
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/manager ./cmd/manager
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ui ./cmd/ui
	CGO_ENABLED=1 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agent ./cmd/agent
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/markers ./cmd/markers
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/transcode ./cmd/transcode

contexts: ## Export clean trees of $(FFGO_REF) and $(PAR2GO_REF) for the image builds (§7.6); refuses a dirty or mismatched checkout.
	hack/contexts.sh $(CONTEXTS) $(FFGO_DIR) $(FFGO_REF) $(PAR2GO_DIR) $(PAR2GO_REF)

docker-build: contexts ## Build the clustarr and native images.
	docker build $(LOCAL_CONTEXTS) -f images/Dockerfile.clustarr --target clustarr -t $(IMG) .
	docker build $(LOCAL_CONTEXTS) -f images/Dockerfile.native --target native -t $(NATIVE_IMG) .

docker-build-native: contexts ## Build the native image and its -debug twin.
	docker build $(LOCAL_CONTEXTS) -f images/Dockerfile.native --target native -t $(NATIVE_IMG) .
	docker build $(LOCAL_CONTEXTS) -f images/Dockerfile.native --target native-debug -t $(NATIVE_DEBUG_IMG) .

docker-selfcheck: ## Check the built images as CI and release do.
	hack/image-checks.sh $(IMG) version:manager version:ui no-shell
	hack/image-checks.sh $(NATIVE_IMG) $(foreach c,$(NATIVE_CLASSES),self-check:transcode:$(c)) self-check:agent self-check:markers self-check:par2-child version:agent version:markers version:transcode no-shell
	hack/image-checks.sh $(NATIVE_DEBUG_IMG) notices no-media-executables

kind-load: ## Load the clustarr and native images into kind, skipping any the node already holds.
	IMG=$(IMG) NATIVE_IMG=$(NATIVE_IMG) hack/kind.sh load

native-assets: ## FFmpeg 9 shared libs + libffshim, ONNX Runtime, libpar2shim and the test-only CLIs.
	hack/native-assets.sh $(NATIVE_ASSETS)

chart-deps: ## Vendor the chart's nats and cloudnative-pg tarballs when charts/clustarr/charts is empty.
	@ls charts/clustarr/charts/nats-*.tgz charts/clustarr/charts/cloudnative-pg-*.tgz >/dev/null 2>&1 || { \
	  helm repo add nats https://nats-io.github.io/k8s/helm/charts/ --force-update && \
	  helm repo add cloudnative-pg https://cloudnative-pg.github.io/charts --force-update && \
	  helm dependency build charts/clustarr; }
```

**`hack/native-assets.sh <dir>`**, the analogue of `make pg-assets`, idempotent and
stamped by its inputs. It reads its pins from the `ARG` defaults in
`images/Dockerfile.native` (`FFMPEG_RELEASE`, `FFMPEG_VERSION`, `FFMPEG_BRANCH`,
`FFMPEG_SHA256_*`, `ORT_VERSION`, `ORT_SHA256_*`), from par2go's `shim/build.sh`
(the par2cmdline-turbo tag and commit), and its own `PAR2_CLI_VERSION=v1.5.0`. It
works under `mktemp -d` in `$TMPDIR` (scratch areas are shared) and installs with
an atomic `mv`. It writes:

- `<dir>/lib`: the FFmpeg libraries, `libffshim.so` built with
  `-Wl,-rpath,<dir>/lib`, `libonnxruntime.so`, `libpar2shim.so` (built with
  `$(PAR2GO_DIR)/shim/build.sh`, or the released asset once par2go publishes);
- `<dir>/bin`: BtbN's shared `ffmpeg` and `ffprobe` (RUNPATH `$ORIGIN/../lib`), which
  the probe, decode and subtitle parity tests compare against, and the `par2` CLI,
  which `test/par2test.CreateSet` uses. These are test inputs only; no image ships
  them.

`test` and `test-race` gain `native-assets chart-deps` as prerequisites and export,
beside `KUBEBUILDER_ASSETS` and `CLUSTARR_PG_ASSETS`:
`LD_LIBRARY_PATH=$(NATIVE_ASSETS)/lib`,
`CLUSTARR_NATIVE_ASSETS=$(NATIVE_ASSETS)`,
`FFGO_SHIM_DIR=$(NATIVE_ASSETS)/lib`,
`ORT_LIB_PATH=$(NATIVE_ASSETS)/lib/libonnxruntime.so`,
`PAR2GO_LIB=$(NATIVE_ASSETS)/lib/libpar2shim.so`, `PAR2GO_REQUIRE=1`,
`PATH="$(NATIVE_ASSETS)/bin:$$PATH"`, `CLUSTARR_REQUIRE_TOOLS=1` (§10.3.1).

`LD_LIBRARY_PATH` is not optional. ffgo finds libavutil, libavcodec and
libavformat only through `LD_LIBRARY_PATH`, a fixed list of system directories
and the system loader (ffgo `internal/bindings/bindings.go:117-128,207-224`);
`FFGO_SHIM_DIR` locates only the shim (`internal/shim/shim.go:875-887`). The
libraries load at package init (`avutil/avutil.go:91`) behind a `sync.Once`
that keeps a failure, so the variable must be in the test process's
environment at start; `t.Setenv` is too late. Without it, on the owner's host
the native tests run against Arch's system FFmpeg 9.0.1 (`/usr/lib/libavcodec.so.63`)
while the parity oracles in `$(NATIVE_ASSETS)/bin` run BtbN's 9.0.2, which a
libavcodec major.minor comparison cannot tell apart, and on CI (no system
FFmpeg 9) every native test fails under `CLUSTARR_REQUIRE_TOOLS=1`.
`nativetest.Require` checks `ffruntime.Report.AVCodec` against
`$CLUSTARR_NATIVE_ASSETS/lib` (§7.4).

`test-unit` builds nothing. `chart-deps` closes the CLAUDE.md gotcha that
`make test` cannot pass in a fresh clone or worktree without `helm dependency build`.
`make run-dev` (§3.10) is added beside them, depending on `build` and
`native-assets`; `hack/dev-run.sh` exports the same library variables.

**Contexts.** Target `contexts` (§7.6 item 3) exports clean trees of
`$(FFGO_REF)` and `$(PAR2GO_REF)` and refuses a mismatched or dirty checkout;
every `docker-build*` target depends on it, and `LOCAL_CONTEXTS` becomes
`--build-context ffgo=$(CONTEXTS)/ffgo --build-context par2go=$(CONTEXTS)/par2go
--build-arg FFGO_COMMIT=$(shell git -C $(FFGO_DIR) rev-parse $(FFGO_REF))
--build-arg PAR2GO_COMMIT=$(shell git -C $(PAR2GO_DIR) rev-parse $(PAR2GO_REF))`
while go.mod carries the local replaces. `make build` and `make test`, which
read `../ffgo-unify` and `../par2go` through go.mod's replaces (R12), only print a
WARNING when either tree is dirty or off its ref, because developing the fork
additions needs a dirty tree.

#### 10.1.7 GitHub workflows

**`.github/workflows/ci.yml`**

- **Header (`:1-12`):** native test assets come from `make native-assets`, not
  static ffmpeg and par2 executables.
- **`env`:** drop `FFMPEG_BRANCH` and `PAR2_VERSION` (`:35-36`).
- **`generated` job** gains "go.mod carries no local replace":
  ```sh
  if grep -nE '^replace .* => \.{1,2}/' go.mod; then
    echo "::error::go.mod replaces a module with a local directory; push that module's tag and replace with the tagged version"
    exit 1
  fi
  ```
- **`test` job:** "Install ffmpeg and par2" (`:121-136`) becomes `make native-assets`
  with an `actions/cache` step (pinned by SHA; path `~/go/bin/native-assets`; key
  over `images/Dockerfile.native`, `hack/native-assets.sh` and the par2go pin);
  ubuntu-24.04 runners carry cmake and g++. "Chart dependencies" (`:138-143`)
  becomes `make chart-deps`, dropping the `kedacore` repo (`:141`). The `make test`
  step sets `CLUSTARR_REQUIRE_TOOLS: "1"` and runs through the Makefile, so it
  inherits `LD_LIBRARY_PATH` and `CLUSTARR_NATIVE_ASSETS` (§10.1.6); no step
  runs `go test` on native packages directly. ubuntu-24.04 runners carry no
  FFmpeg 9, so without `LD_LIBRARY_PATH` every native test would fail there.
- **`transcoder` job (`:155-203`)** becomes job `images`: build with `load: true`,
  tag `selfcheck/${{ matrix.image }}:ci`, then
  `hack/image-checks.sh selfcheck/${{ matrix.image }}:ci ${{ matrix.checks }}`:

```yaml
matrix:
  include:
    - { image: clustarr, file: images/Dockerfile.clustarr, target: clustarr, arch: amd64, runner: ubuntu-24.04,
        checks: "version:manager version:ui no-shell" }
    - { image: native, file: images/Dockerfile.native, target: native, arch: amd64, runner: ubuntu-24.04,
        checks: "self-check:transcode:cpu self-check:transcode:cuda self-check:transcode:intel self-check:agent self-check:markers self-check:par2-child version:agent version:markers version:transcode no-shell" }
    - { image: native, file: images/Dockerfile.native, target: native, arch: arm64, runner: ubuntu-24.04-arm,
        checks: "self-check:transcode:cpu self-check:agent self-check:markers self-check:par2-child version:agent version:markers version:transcode no-shell" }
```

**`.github/workflows/release.yml`**

- **Header (`:4-8`)** lists `clustarr`, `native`, `native-debug` and
  `e2e-fixtures`; the "media images compile with cgo" note (`:25-26`) now refers to
  the native image's agent.
- **`images` matrix (`:62-72`):** `class` becomes `checks`, and the self-check step
  runs `hack/image-checks.sh selfcheck/<image>:ci $CHECKS`. Rows: `clustarr`
  (`""`, `images/Dockerfile.clustarr`, target `clustarr`, amd64 and arm64, checks
  `version:manager version:ui no-shell`); `native` (`/native`,
  `images/Dockerfile.native`, target `native`, amd64 and arm64, the `ci.yml`
  checks); `native-debug` (`/native-debug`, target `native-debug`, amd64 and arm64,
  `notices no-media-executables`); `e2e-fixtures` unchanged.
- **`merge` matrix (`:188-192`):** `clustarr ""`, `native /native`,
  `native-debug /native-debug`, `e2e-fixtures /e2e-fixtures`.
- **`chart-lint` and `chart-release`:** drop `helm repo add kedacore` (`:263`, `:299`).

`/media`, `/transcoder` and `/transcoder-debug` are no longer published; their tags
remain in GHCR (§13 OD38).

#### 10.1.8 Scripts and ignore files

- **`hack/kind.sh`:** `IMG` and `NATIVE_IMG` replace `MEDIA_IMG` and
  `TRANSCODER_IMG` (`:25`, `:38-40`, `:205`). `cmd_load` (`:213-226`) loops over
  `"${IMG}" "${NATIVE_IMG}"` and skips an image the node already holds, so a deploy
  loads only images whose inputs changed (CLAUDE.md, "loading an image into the
  node stalls etcd"):
  ```bash
  node_id=$(docker exec "${CLUSTER_NAME}-control-plane" crictl inspecti -o go-template --template '{{.status.id}}' "${img}" 2>/dev/null || true)
  [[ "${node_id}" == "$(docker image inspect -f '{{.Id}}' "${img}")" ]] && { log "${img} unchanged on the node, skipping"; continue; }
  ```
  `create_cluster` needs no feature gate (HPAScaleToZero is Beta and on in 1.37) and
  no metrics-server (the manager serves `external.metrics.k8s.io` itself).
- **`hack/e2e.sh`:** §10.4.
- **`.dockerignore` and `.gitignore`:** the bare-build guards `/clustarr` (and
  `.gitignore`'s `/clustarr/clustarr`) become `/manager /ui /agent /markers /transcode`.
- **`hack/deps/deps.go:43-49`** is rewritten (§12).
- **New scripts:** `hack/contexts.sh` (§7.6 item 3, §10.1.6) and
  `hack/dev-run.sh` (§3.10).

### 10.2 Installers

#### 10.2.1 Workloads, held identically by the chart and kustomize

| component | command | args (kustomize; the chart derives `--nats-single-node`) | replicas | strategy | grace (s) | SA token / ClusterRole | leader-election binding | `/data` | other volumes | ports |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `manager` | `/usr/bin/manager` | `--leader-elect --slots=cpu=2,nvidia=1,intel=1 --nats-single-node` | 1, no knob (R5) | RollingUpdate, maxSurge 1, maxUnavailable 0 | 70 | yes / manager | yes | rw | cardigann definitions, read-only (when configured) | `metrics` 8443, `health` 8081, `extmetrics` 6443 |
| `agent-catalog` | `/usr/bin/agent` | `--domain=catalog` | omitted (HPA) | RollingUpdate | 145 | yes / agent-catalog | no | none | – | `metrics` 8443, `health` 8081 |
| `agent-events` | `/usr/bin/agent` | `--domain=events` | omitted (HPA) | RollingUpdate | 55 | yes / agent-events | no | none | – | as above |
| `agent-metadata` | `/usr/bin/agent` | `--domain=metadata` | 1, no knob (ADR-0007) | Recreate | 85 | yes / agent-metadata | no | none | – | as above |
| `agent-import` | `/usr/bin/agent` | `--domain=import` | omitted (HPA) | RollingUpdate | 85 | yes / agent-import | no | rw | – | as above |
| `agent-index` | `/usr/bin/agent` | `--domain=index` | 1, no knob (R4 amendment) | Recreate (both engines) | 85 | yes / agent-index | no | none | `<fullname>-index` at `/var/lib/clustarr/index` (SQLite only) | `http` 8080 (facade), `metrics` 8443, `health` 8081 |
| `agent-caption` | `/usr/bin/agent` | `--domain=caption` | omitted (HPA) | RollingUpdate | 115 | yes / agent-caption | no | rw | – | `metrics` 8443, `health` 8081 |
| `markers` | `/usr/bin/markers` | none | omitted (HPA) | RollingUpdate | 1825 | `automountServiceAccountToken: false`, nothing bound | no | ro | – | `metrics` 8080 (`/metrics`, `/healthz`, `/readyz`) |
| `ui` | `/usr/bin/ui` | `--auth-mode=anonymous --plex-provider=true --plex-guids=true --pipeline-history=100` (unchanged) | `ui.replicas` | RollingUpdate | 30 | yes / ui (hand-written) | no | none | – | `http` 8080 |

- **Grace** follows §3.5.6 (longest AckWait + 25 s; `TestGracePeriodsCoverAckWait`).
- **Only `manager` carries `--leader-elect`** and the leader-election binding;
  agents have no such flag (§3.5.1, §5.8).
- **Environment**, the same in both installers:
  - every row: `NATS_URL`, `POD_NAME`, `POD_NAMESPACE`, `GOMEMLIMIT`; `UMASK=002`
    on every rw `/data` row;
  - `GOMEMLIMIT` is 80% of the memory limit, except `agent-import` and
    `agent-caption` at 60% and `markers` at 50%, because FFmpeg, par2 and ONNX
    allocate outside the Go heap (§7.2.8). `clustarr.gomemlimit` takes the
    percentage as its second argument; kustomize literals are computed the same
    way and held by `TestGOMEMLIMITIsItsShareOfTheLimit`;
  - autoscaled rows: `CLUSTARR_CONSUMER_SLOTS` when `consumerSlots` is set;
  - `manager`: `CLUSTARR_NATIVE_IMAGE` (the native image); 
    `CLUSTARR_ENGINE_SERVICE_ACCOUNT` = `<fullname>-grabarr-engine` (kustomize
    `grabarr-engine`); `CLUSTARR_DATA_CLAIM`; with autoscaling,
    `CLUSTARR_EXTERNAL_METRICS_SECRET` = `<fullname>-external-metrics-tls`
    (kustomize `external-metrics-tls`) and `CLUSTARR_EXTERNAL_METRICS_SERVICE` =
    `<fullname>-external-metrics` (kustomize `external-metrics`); only when set in values:
    `CLUSTARR_INTEL_RENDER_GROUPS`, `CLUSTARR_GPU_NODE_LABEL_NVIDIA` / `_INTEL`,
    `CLUSTARR_CARDIGANN_BUNDLED=false`,
    `CLUSTARR_CARDIGANN_DEFINITIONS_DIR=/etc/clustarr/cardigann` (moved from
    indexarr: R8 and R9 put the bundle loader in the manager);
  - `agent-index`: `CLUSTARR_INDEX_PATH=/var/lib/clustarr/index/releases.db`,
    `CLUSTARR_FACADE_API_KEY_SECRET` (the kept Secret name), `CLUSTARR_INDEX_DSN`
    under `postgres.enabled`;
  - `ui`: `CLUSTARR_ART_SIGNING_KEY`, unchanged;
  - `markers`: nothing extra (`ORT_LIB_PATH`, `FFGO_SHIM_DIR` from the image).
- **Placement:** a soft `topologySpreadConstraints` (`kubernetes.io/hostname`,
  maxSkew 1, `ScheduleAnyway`) on autoscaled agents; a required anti-affinity
  would stall RollingUpdate surges on a single node (§13 OD33).
- **Resource defaults** (requests → limits); the manager's single cache keeps
  `status.mediaInfo` for 13.7k MediaFiles:

| Workload | Requests | Limits |
| --- | --- | --- |
| manager | 200m / 512Mi | 2 / 1536Mi |
| agent-catalog | 100m / 256Mi | 1 / 1Gi |
| agent-events | 100m / 256Mi | 1 / 1Gi |
| agent-metadata | 50m / 96Mi | 500m / 512Mi |
| agent-import | 100m / 256Mi | 2 / 1536Mi |
| agent-index | 100m / 256Mi | 1 / 1Gi |
| agent-caption | 100m / 192Mi | 1 / 1Gi |
| markers | 250m / 512Mi | 2 / 2Gi |
| ui | unchanged | unchanged |

#### 10.2.2 Autoscaling contract between the installers and the manager

- An autoscaled Deployment (`agent-catalog`, `agent-events`, `agent-import`,
  `agent-caption`, `markers`) carries
  `metadata.labels["autoscale.clustarr.io/domain"]` =
  `catalog|events|import|caption|markers`, which the reconciler selects in
  `POD_NAMESPACE`, and has **no `spec.replicas`**: created without it, it starts at
  1, while an explicit 0 would leave it paused and never woken. The
  `_helpers.tpl:237-241` omission pattern, which applies today only to
  `captionarr-worker` under KEDA, now applies to these rows.
- The optional annotation `autoscale.clustarr.io/min-replicas: "<n>"` raises that
  Deployment's HPA minReplicas (§9.5); `config/e2e` sets `"1"` (§10.4).
- **`autoscaling.enabled: false`** (values; kustomize has no switch) drops the
  label, renders `replicas` from `agents.<d>.replicas` / `markers.replicas`,
  passes `--autoscale=false` to the manager, and renders neither the APIService nor
  the auth-reader binding. Use it where KEDA or prometheus-adapter already holds
  `v1beta1.external.metrics.k8s.io`: only one APIService may hold the group, and
  Helm refuses to adopt one it does not own, so the conflict is a loud install error.
- **No installer renders a HorizontalPodAutoscaler** (R5);
  `TestNoInstallerRendersAnHPAOrKEDA` holds it.
- **APIService `v1beta1.external.metrics.k8s.io`**, rendered by the installers:
  `group: external.metrics.k8s.io`, `version: v1beta1`,
  `service: {namespace: <release ns>, name: <fullname>-external-metrics (kustomize: external-metrics), port: 443}`,
  `groupPriorityMinimum: 100`, `versionPriority: 100`, `insecureSkipTLSVerify: false`,
  **no `caBundle`**. The manager writes only `spec.caBundle`, under
  `clustarr-autoscale`, from the CA in `CLUSTARR_EXTERNAL_METRICS_SECRET`. Neither
  Helm SSA nor `kubectl apply --server-side` sends `caBundle`, so nothing fights.
  Why the installers render it: R5 amendment.
- **RoleBinding in `kube-system`:** `<fullname>-manager-auth-reader` (kustomize
  `clustarr-manager-auth-reader`) grants Role `extension-apiserver-authentication-reader`
  to SA manager in the release namespace, so the manager can read the front-proxy
  client CA from ConfigMap `kube-system/extension-apiserver-authentication`.
- **No `system:auth-delegator` binding:** the kube-apiserver's authentication and
  authorization run before the aggregator proxies the request, and the HPA
  controller's bootstrap role already grants `external.metrics.k8s.io`; the server
  accepts front-proxy client certs only and reviews no tokens.
- **`consumerSlots`** per agent and for markers render `CLUSTARR_CONSUMER_SLOTS`
  (§9.5); a guard holds that it parses and names only that domain's consumers.

#### 10.2.3 Services and ServiceMonitors

| Service | Ports |
| --- | --- |
| `manager` | `metrics` 8443→`metrics` |
| `external-metrics` (chart `<fullname>-external-metrics`; only with autoscaling) | `https` 443→`extmetrics` on the manager pods, `publishNotReadyAddresses: true` (§9.4) |
| `agent-catalog`, `agent-events`, `agent-metadata`, `agent-import`, `agent-caption` | `metrics` 8443 |
| `agent-index` | `http` (`agents.index.facade.service.port`, default 8080, type `agents.index.facade.service.type`) and `metrics` 8443 |
| `markers` | `metrics` 8080 (new; `segmentarr-worker` had no Service) |
| `ui` | `http` 8080, unchanged |

ServiceMonitors (`templates/servicemonitor.yaml`, `config/prometheus/servicemonitors.yaml`):
`manager` and the six agents with https, bearer token and `insecureSkipVerify`, as
today; `markers` with `scheme: http`, no bearer, no TLS; `ui` none, as today.

#### 10.2.4 RBAC

**`Makefile` `RBAC_ROLES` and `RBAC_PATHS`** replace `:51-58` with the list in
§4.6, and the comment block above them (`Makefile:24-50`, which describes the
seven per-service roles, "a service's role reads every package under its
directory", the engine sub-identity, `cmd/squasharr-worker` and "cmd/clustarr's
guards") is rewritten for the eight identities, the explicit per-domain paths
and `test/guards`. Globs like `./app/catalog/...` cannot separate controllers from workers, so
the paths are explicit and assume the package splits of §4.3 (C3, C6-C8, X2-X3,
the facade key in `app/indexer/agent`, `app/autoscale`); until those land,
`TestEveryPackageWithRBACMarkersIsInARole` and the linkage guard fail.

**Generated files** in `config/rbac/`: `manager_role.yaml`,
`agent_catalog_role.yaml`, `agent_events_role.yaml`, `agent_metadata_role.yaml`,
`agent_import_role.yaml`, `agent_index_role.yaml`, `agent_caption_role.yaml`,
`grabarr_engine_role.yaml`, each ClusterRole `clustarr-<role>-role`.
`make manifests` never deletes, so `git rm` removes `catalogarr_role.yaml`,
`importarr_role.yaml`, `indexarr_role.yaml`, `grabarr_role.yaml`,
`squasharr_role.yaml` and `captionarr_role.yaml`;
`TestConfigRBACHoldsOnlyKnownRoleFiles` catches a leftover. The manager role
carries the autoscale markers (§9.5). `config/rbac/kustomization.yaml`
(`:15-24` lists the six deleted files) lists `manager_role.yaml`, the six
`agent_*_role.yaml`, `grabarr_engine_role.yaml`, `role_binding.yaml`,
`leader_election_role.yaml`, `leader_election_role_binding.yaml`,
`ui_role.yaml` and `ui_role_binding.yaml`; without that edit
`kustomize build config/default` fails on the missing files.

**Hand-written files:** `role_binding.yaml`, eight ClusterRoleBindings
`clustarr-<identity>-rolebinding` → `clustarr-<identity>-role`, one per SA;
`leader_election_role.yaml` unchanged, its comment naming `manager.clustarr.io`;
`leader_election_role_binding.yaml` for `manager` only; `ui_role.yaml` and
`ui_role_binding.yaml` unchanged; `config/autoscaling/auth_reader_binding.yaml`
(new, §10.2.6).

**`charts/clustarr/templates/rbac.yaml`:** `$components` becomes `manager` plus
each enabled agent; `$roleOf` is deleted (component equals identity); one
ClusterRole `<fullname>-<identity>` per generated role, with its BEGIN/END
sentinels; the grabarr-engine SA, ClusterRole and binding always rendered (the
manager is always on); the leader-election RoleBinding for `manager` only; the
markers SA with no binding; ui unchanged.

#### 10.2.5 Chart files

- **`Chart.yaml`:** `version: 0.5.0` (the values break, §10.2.8; §13 OD40); the
  `keda` dependency (`:62-65`) and its comment (`:49-61`) deleted; `Chart.lock`
  regenerated with `helm dependency update charts/clustarr` (needs the network
  once; its digest cannot be hand-edited).
- **`templates/deployments.yaml`:** `manager` plus the six agents through
  `clustarr.workload`; `ui` stays hand-written, gains
  `image: clustarr.image "clustarr"` and `command: ["/usr/bin/ui"]`, and its args
  lose the leading `"ui"`.
- **`templates/markers.yaml`:** `git mv templates/segmentarr-worker.yaml templates/markers.yaml`;
  command `/usr/bin/markers`, no args, image native; grace 1825, the autoscaling
  label, no replicas; `/healthz` and `/readyz` probes on `metrics`;
  `automountServiceAccountToken: false` on the SA and pod, as today; `/data`
  read-only.
- **`templates/autoscaling.yaml`** (new): the APIService, the
  `<fullname>-external-metrics` Service (§9.4) and the kube-system RoleBinding,
  under `{{- if .Values.autoscaling.enabled }}`.
- **`templates/scaledobject.yaml`:** deleted.
- **`templates/servicemonitor.yaml`:** §10.2.3.
- **`templates/pvc.yaml`:** the index PVC condition becomes
  `and .Values.agents.index.enabled (not .Values.postgres.enabled) (not .Values.storage.index.existingClaim)`,
  and it gains `helm.sh/resource-policy: keep` (today none, `:29-46`; the live PV's
  reclaim policy is Delete; §13 OD39).
- **`templates/NOTES.txt`:** the service list names the manager, each agent ("HPA
  by the manager, 0..N" or its replica count), markers and ui; the Torznab URL
  becomes `http://<fullname>-agent-index.<ns>.svc:<port>/<indexer>/api` (the Secret
  is unchanged); new lines `kubectl get hpa -n <ns>` and
  `kubectl get apiservice v1beta1.external.metrics.k8s.io`; the KEDA line (`:29`)
  deleted.
- **`templates/_helpers.tpl`:** `clustarr.workload` gains `command` (required),
  `autoscaled` and `domain` (when autoscaled and `.Values.autoscaling.enabled`, omit
  `replicas` and add the label), `gomemPercent`, `extraPorts`, `extraServicePorts`;
  the KEDA condition (`:237-241`) goes; `clustarr.image` takes `which` =
  `clustarr|native`, and its doc comment (`:177`) names the two images;
  `clustarr.facadeAPIKeySecret` (`:32-33`) reads
  `.Values.agents.index.facade.apiKeySecret` (it reads
  `.Values.indexarr.facade.apiKeySecret` today), defaulting to the unchanged
  `<fullname>-indexarr-facade`. `clustarr.validate`: delete the `catalogarrMetadata.replicas`
  rule (`:149-151`) and the `keda.prometheusAddress` rule (`:161-163`); the indexarr
  replicas rule goes (the index agent has no replicas knob); the cardigann
  both-sources rule becomes `manager.cardigann.definitions`; the RWX and
  CNPG-first-install rules stay.
- **`values.yaml`** skeleton:

```yaml
image:
  registry: ghcr.io
  pullPolicy: IfNotPresent
  clustarr: { repository: mediactl/clustarr, tag: "" }          # manager, ui
  native:   { repository: mediactl/clustarr/native, tag: "" }   # agents, markers, engines, transcode pools
autoscaling:
  enabled: true      # manager-owned HPAs + the external metrics APIService
manager:
  slots: cpu=2,nvidia=1,intel=1
  intelRenderGroups: []
  gpuNodeLabelNvidia: ""
  gpuNodeLabelIntel: ""
  cardigann:
    bundled: true
    definitions: { configMap: "", existingClaim: "", subPath: "" }
  resources: {...}
  nodeSelector: {}
  tolerations: []
  affinity: {}
agents:
  catalog:  { enabled: true, replicas: 1, consumerSlots: {}, resources: {...}, nodeSelector: {}, tolerations: [], affinity: {} }  # replicas used only when autoscaling.enabled=false
  events:   { enabled: true, replicas: 1, consumerSlots: {}, ... }
  metadata: { enabled: true, resources: {...}, ... }   # always 1 replica, Recreate
  import:   { enabled: true, replicas: 1, consumerSlots: {}, ... }
  index:
    enabled: true    # always 1 replica, Recreate
    facade: { service: { type: ClusterIP, port: 8080 }, apiKeySecret: "" }
    resources: {...}
  caption:  { enabled: true, replicas: 1, consumerSlots: {}, ... }
markers:   { enabled: true, replicas: 1, consumerSlots: {}, resources: {...}, nodeSelector: {}, tolerations: [], affinity: {} }
ui: {...}            # unchanged
# nameOverride, fullnameOverride, imagePullSecrets, natsUrl, natsSingleNode, nats, postgres,
# cloudnative-pg, metrics, storage, podSecurityContext, securityContext: unchanged
```

- **`values.schema.json`:** top level `additionalProperties: false`, listing
  `global` (Helm injects it for charts with dependencies) and every key above;
  `image.required` becomes `[registry, pullPolicy, clustarr, native]`, still
  `additionalProperties: false`; `$defs/agentBlock` is
  `{enabled, replicas, consumerSlots, resources, nodeSelector, tolerations, affinity}`
  with `additionalProperties: false` (`consumerSlots` an object of consumer name →
  integer ≥ 1); `agents.metadata` and `agents.index` take no `replicas`;
  `agents.index` adds `facade`; `markers` uses `agentBlock`. There is no
  `concurrency` and no `ackWaitSeconds`: grace comes from code (§3.5.6) and slots
  from `consumerSlots`, which the reconciler reads from the same pod template.
  The top-level properties keep `nameOverride`, `fullnameOverride` and
  `imagePullSecrets` exactly as today's schema declares them (`values.yaml:5-6,36`;
  `_helpers.tpl:5,9-12,201,257` read them). Guard
  `test/guards/values_test.go` `TestTheDefaultValuesPassTheSchema` renders the
  chart with its own `values.yaml`, and again with `imagePullSecrets` set and
  both name overrides set, through `helm lint --strict` and `helm template`
  (both apply `values.schema.json`), so a key the skeleton drops fails here,
  not at a user's upgrade.

#### 10.2.6 Kustomize files

- **`config/manager/`:** adds `manager.yaml` (also holding the `grabarr-engine`
  ServiceAccount, moved from `grabarr.yaml:25`), `agent-catalog.yaml`,
  `agent-events.yaml`, `agent-metadata.yaml`, `agent-import.yaml`,
  `agent-index.yaml`, `agent-caption.yaml` and `markers.yaml` (from
  `segmentarr-worker.yaml`); edits `ui.yaml` (image `ghcr.io/mediactl/clustarr:dev`,
  `command: ["/usr/bin/ui"]`, args without `"ui"`); lists the nine files in
  `kustomization.yaml`; deletes the ten old files.
- **`config/rbac/`:** §10.2.4.
- **`config/autoscaling/`** (new): `kustomization.yaml`; `apiservice.yaml`
  (`spec.service.namespace` written literally as `clustarr-system`,
  `spec.service.name: external-metrics`); `service.yaml` (Service
  `external-metrics`, selector the manager's labels, `publishNotReadyAddresses:
  true`, port `https` 443 → `extmetrics`); `auth_reader_binding.yaml`
  (`metadata.namespace: kube-system`).
- **`config/default/kustomization.yaml`:** adds `- ../autoscaling`; replaces
  `namespace: clustarr-system` with a NamespaceTransformer setting
  `unsetOnly: true`, so the kube-system RoleBinding keeps its namespace (the plain
  `namespace:` field overwrites every namespaced resource); updates the header
  comment ("nine Deployments", no `../keda`):
  ```yaml
  transformers:
  - |-
    apiVersion: builtin
    kind: NamespaceTransformer
    metadata: { name: clustarr-namespace, namespace: clustarr-system }
    unsetOnly: true
  ```
  `TestChartAndKustomizeAgreePerComponent` asserts the RoleBinding's namespace is
  `kube-system` in both installers.
- **`config/prometheus/servicemonitors.yaml`:** eight entries (§10.2.3).
- **`config/postgres/`:** `indexarr-patch.yaml` becomes `agent-index-patch.yaml`
  (Deployment and container `agent-index`); the `kustomization.yaml` patch path
  follows; `delete-index-pvc.yaml` unchanged.
- **`config/keda/`:** deleted.
- **`config/README.md`:** directory table updated.

#### 10.2.7 KEDA removal: the complete list

**Delete:** `charts/clustarr/Chart.yaml:49-65` (the keda dependency and comment),
and regenerate `Chart.lock`; `charts/clustarr/charts/keda-2.20.2.tgz` (local,
gitignored); `values.yaml:131-148`; the `keda` block in `values.schema.json`;
`templates/scaledobject.yaml`; `_helpers.tpl:161-163` and `:237-241`;
`NOTES.txt:29`; the README "Autoscaling (KEDA)" section (`:214`, rewritten as
"Autoscaling"), its table rows 388-390 and line 35; `config/keda/` (3 files);
`config/default/kustomization.yaml:11`; `config/README.md:21,29-30`;
`config/prometheus/README.md:51-52`; `cmd/clustarr/keda_consumer_test.go`;
`helm repo add kedacore` at `ci.yml:141` and `release.yml:263,299`.

**Edit these comments:** `config/nats/service.yaml:3`; `charts/clustarr/.gitignore:1`
and `.helmignore:2` ("nats / cloudnative-pg"); `README.md:109-111`;
`pkg/obs/metrics/domain.go:248` ("the autoscaling input", §9.4);
`test/e2e/parity_test.go:23-31`; the CLAUDE.md gotcha "three vendored dependency
tarballs", which becomes two. `docs/observability.md:81` is rewritten and
`docs/autoscaling.md` replaces the KEDA docs (§12).

`TestNoInstallerRendersAnHPAOrKEDA` asserts: no `keda.sh` kind in either render;
no `keda` key in `values.yaml` or the schema; no `keda` dependency in `Chart.yaml`;
no `config/keda` directory.

#### 10.2.8 Retired values keys

The schema rejects every retired key. An existing release is upgraded with a fresh
values file passed by `-f`, never `--reuse-values`: the ADR-0015 precedent for
`image.transcoderCuda` (§13 OD37).

| old key | new key |
| --- | --- |
| `image.controller` | `image.clustarr` |
| `image.transcoder` | `image.native` |
| `image.media` | (gone: everything that used it runs `native`) |
| `catalogarr`, `importarr`, `grabarr`, `squasharr`, `captionarr` | `manager` (resources merged) |
| `squasharr.slots`, `.intelRenderGroups`, `.gpuNodeLabelNvidia`, `.gpuNodeLabelIntel` | `manager.*` |
| `squasharr.workerEngine` | (gone: there is no `--worker-engine`, and no template read it) |
| `catalogarrMetadata` | `agents.metadata` (no `replicas`) |
| `importarrWorker` | `agents.import` |
| `indexarr.replicas` | (gone: the index agent is fixed at 1) |
| `indexarr.facade`, `.resources`, … | `agents.index.*` |
| `indexarr.cardigann` | `manager.cardigann` |
| `captionarrWorker` (incl. `ackWaitSeconds`) | `agents.caption` |
| `segmentarrWorker` (incl. `concurrency`) | `markers` (`concurrency: N` becomes `consumerSlots: {segmentarr-analyze: N}`) |
| `keda` | `autoscaling.enabled` |

The chart README gains "Upgrading from 0.4.x" with this table, and says to
annotate `<fullname>-index` with `helm.sh/resource-policy=keep` before the first
upgrade (the chart renders it itself from 0.5.0). The scale-down, lease and
quiesce ordering belong to the cutover runbook (§11.3).

### 10.3 Tests

#### 10.3.1 New test packages

| package | kind | holds |
| --- | --- | --- |
| `test/guards` (package `guards`, `doc.go` plus `_test.go` files; `test/guards/depguard` helper) | tests | every installer, image, RBAC and source guard that reads files, Makefile variables or `go list` output and links no cmd |
| `test/installtest` | helpers imported only by tests | today's helpers, renamed and exported: `RepoRoot`, `FindTool`, `Run`, `RenderChart(t, args...)`, `BuildKustomize(t, dir)`, `DecodeRendered` (`Rendered{Deployments, ServiceAccounts, Roles, Bindings, Services, APIServices}`), `DeploymentsIn`, `GrantsOf`, `SortedGrants`, `RBACRoles` (from `rbac_markers_test.go:56-80`), `ReadRole`; sources `helpers_test.go`, `deploy_parity_test.go:256-329`, `deploy_args_test.go:188-216` |
| `test/starttest` | envtest helpers | `StartEnv`, `EmbeddedNATS`, `FreeAddress`, `WaitForProbe`, `HoldLease`, `IdentityKubeconfigs` (from `start_rbac_envtest_test.go:85-168`, reading roles through `installtest.RBACRoles`; for the `manager` identity it also binds Role `extension-apiserver-authentication-reader` in `kube-system`, as the installers do, §10.2.2), `StatusManagers`, `AssertWroteOnlyAs` |
| `test/system` | envtests | cross-identity flows no single identity can prove (§10.3.3), composing the manager and agent domains in one process, each under its own identity kubeconfig |
| `test/nativetest` | helper | `Require(t, what string, err error)`: skips, or fails when `CLUSTARR_REQUIRE_TOOLS=1` (or `PAR2GO_REQUIRE=1` for par2go), if FFmpeg, ffgo's shim, par2go or ONNX Runtime cannot load; the probe, decode, caption and par2 sections use it |
| `test/ffprobeoracle`, `test/par2test`, `test/parity/segments` | test support | §6.2, §8.8, §7.2.10 |

`CLUSTARR_REQUIRE_TOOLS=1` (set by `make test`, `make test-race` and CI) turns
`installtest.FindTool`'s skip (today `deploy_parity_test.go:268`),
`nativetest.Require` and `starttest.StartEnv`'s `KUBEBUILDER_ASSETS` skip into
failures, closing the CLAUDE.md "a suite that finishes in milliseconds skipped"
class for these tests (§13 OD44).

**Per-binary tests stay with their cmd (R10):** `cmd/manager`: `cli_test.go`,
`manifest_test.go`, `umask_test.go`, `version_test.go`, `start_envtest_test.go`;
`cmd/agent`: the same five plus `domains_test.go`, `index_flags_test.go`,
`import_flags_test.go`, `engine_argv_test.go`; `cmd/ui`: `cli_test.go`,
`manifest_test.go`, `wiring_test.go`, `bus_test.go`, `umask_test.go`,
`start_envtest_test.go` (the ui applies `UMASK` too, §3.2, which today's
`TestEveryCommandAppliesUMASK` covers through the shared root);
`cmd/markers` and `cmd/transcode`: `main_test.go`, `manifest_test.go`,
`umask_test.go`. They exercise `internal/cli/<bin>.NewCommand()`. Tests whose
subject is unexported in `internal/cli/<bin>` (`TestManagerCacheOptions`,
`TestManagerFieldIndexes`, `TestAgentsRegisterNoLeaderOnlyRunnable`, §5.15)
live in that package, not in `test/guards`, which links no cmd.

- `manifest_test.go` renders both installers through `installtest`, takes its
  Deployment's `args` and env, runs them through the binary's own command, and
  asserts the result parses, `Validate()`s and carries the expected leader election.
- `cmd/agent/engine_argv_test.go` does the same for the engine argv from
  `downloadclient.EngineCommand(dc, EngineRuntime)`, closing the hole that envtest
  runs no pods.
- `cmd/transcode/manifest_test.go` parses `pool.Template(...)`'s container args
  with its own flag set.

#### 10.3.2 New guards (in `test/guards` unless named)

Guards owned by other sections are listed there: deps (§4.5), exec (§4.5.6),
writers (§5.15), probe (§6.8), decode (§7.7), par2 (§8.9), autoscale (§9.9).

| guard | holds |
| --- | --- |
| `registration_test.go` `TestOnlyTheManagerRegistersControllers` | among module packages `cmd/agent` links, only `app/grab/engine/torrent` and `app/grab/engine/usenet` call `ctrl.NewControllerManagedBy`, `builder.ControllerManagedBy` or `controller.New*` (true only after §4.3 C9 moves `delay.Resolve`, the search outcome names and `EffectiveEpisodeOrder` out of the delayprofile, search and series controller packages); `cmd/markers`, `cmd/transcode` and `cmd/ui` link none; in both installers only the `manager` Deployment passes `--leader-elect`. Its runtime counterpart is each agent start envtest asserting the registered controller names are exactly `torrent-engine` or `usenet-engine` for the engine domains and none for the others |
| `registration_test.go` `TestLeaderOnlyRunnablesLiveInTheManager` (R9) | every runnable type whose `NeedLeaderElection` is true is wired only from manager registration (the replay controllers, `artwork.Reaper`, `bundle.Loader`, `recyclesweep.Scheduler`, `extmetrics.CertManager`, `extmetrics.QueueGauge`, and the `k8s.LeaderOnly` wrappers of `busconn.KeepTopology` and `busconn.WatchDeadLetters` in `internal/cli/manager`); `runnableTypes` already records `lease`.
 The runtime counterpart is §5.15's `TestAgentsRegisterNoLeaderOnlyRunnable` |
| `topology_test.go` `TestEveryAgentDeploymentRunsOneDomain` | every shipped agent argv parses through `internal/cli/agent.NewCommand()` to exactly one domain, and each `--domain=<d>` appears exactly once per installer |
| `autoscale_test.go` `TestEveryConsumerHasExactlyOneHome` (successor to `keda_consumer_test.go`) | every work consumer in `events.Default()` is drained by exactly one home: the manager (`squasharr-transcode-results`, `catalogarr-segments-plan`), one agent domain of `pkg/agentdomain`, `markers` (`segmentarr-analyze`), or the transcode pools (`TranscodeTaskConsumerName`); an orphaned or doubly-drained consumer fails. Each dead-letter watcher is homed with its durable's home plus the manager's `WatchDeadLetters` backstop (§5.9), and only there |
| `autoscale_test.go` `TestAutoscaledDeploymentsAreDeclared` | in both default renders a Deployment carries `autoscale.clustarr.io/domain` iff `pkg/agentdomain` marks its domain autoscaled; such Deployments omit `spec.replicas` and every other Deployment sets it; `CLUSTARR_CONSUMER_SLOTS` parses and names only that domain's consumers |
| `autoscale_test.go` `TestGracePeriodsCoverAckWait` (generalizes `TestWorkerGracePeriodsCoverAckWait`) | every Deployment's `terminationGracePeriodSeconds` ≥ the largest declared `ConsumerSpec.AckWait` (the handler budget, not the broker's `BackOff[0]` deadline) among the consumers its home drains + 25 s (§3.5.6) |
| `autoscale_test.go` `TestNoInstallerRendersAnHPAOrKEDA` | §10.2.7; plus the APIService targets the `external-metrics` Service on 443 with no caBundle and `insecureSkipTLSVerify` false, that Service selects the manager pods with `publishNotReadyAddresses: true`, and the kube-system RoleBinding exists |
| `fieldmanager_test.go` `TestEveryFieldManagerHasItsHome` (R7) | §5.15 |
| `starttest.AssertWroteOnlyAs` (runtime half of R7), called by every per-identity start envtest | snapshots every clustarr CR's managedFields through an uncached client before the case; afterwards the newly appearing managers must be a subset of that identity's §5.2 row. A write with no `FieldOwner` would show up under `clustarr` (pinned default owner) and fail unless the row allows it |
| `rbac_markers_test.go` `TestEachRoleCoversTheMarkedPackagesItsHomeLinks` | the marker packages in `RBAC_PATHS_<role>` must be linked by that identity's home (no role holds grants for code its process does not run); the marker packages that home links must be covered by its paths or by an explicit `linkedNotRun` entry with a reason |
| `rbac_markers_test.go` `TestConfigRBACHoldsOnlyKnownRoleFiles` | `config/rbac/*_role.yaml` is exactly the `RBAC_ROLES` files plus `ui_role.yaml` and `leader_election_role.yaml` |
| `images_test.go` `TestEveryContainerNamesItsBinary` | every installer container, engine container (`downloadclient.EngineCommand`) and pool container (`pool.Template`) runs a `binpath` constant its image ships; `clustarr` ships manager and ui, `native` agent, markers and transcode, read from each Dockerfile's `COPY --from=build /out/<bin> /usr/bin/` and `stage.sh`'s `trace` line |
| `images_test.go` `TestGOMEMLIMITIsItsShareOfTheLimit` | each kustomize `GOMEMLIMIT` literal is `round(limit × pct)`, matching the chart's computed value |

**Amended 2026-10-07 (NATS research).** Four more, owned by §5.15 as amended:
`objectstore_test.go` `TestNoObjectLinks`, `TestOnlyNatsbusTouchesJetStreamObjectStores`
and `TestArtworkWritersAreTheTwoVariantOwners` (`nats-object-store.md` §6.10,
owner decision (a)); `heartbeat_test.go` `TestHeartbeatsFitTheirDeadline`
(`nats-worker-pools.md` S9). `ui/guard_test.go`'s `TestUINeverWrites` gains the
selectors and the nats.go import ban of §4.5.2 as amended.

#### 10.3.3 Where every test goes: `cmd/clustarr` (40 test files)

| file | tests | new home | change |
| --- | --- | --- | --- |
| `artwork_role_deploy_test.go` | `TestBothInstallersRunTheRendererOnTheCatalogarrDeployment` | `test/guards/topology_test.go` `TestEveryAgentDeploymentRunsOneDomain`; `cmd/agent/domains_test.go` `TestTheRendererRunsInTheCatalogDomain` | the agent half asserts `ConsumerCatalogArtworkRender` belongs to `catalog` and `ConsumerCatalogArtworkFetch` to `metadata` (R4) |
| `base_context_guard_test.go` | `TestEveryServiceGivesTheManagerItsBaseContext` | `test/guards/base_context_test.go` `TestEveryManagerGetsTheBaseContext` | discovers every `ctrl.NewManager` call in production code instead of reading `busServiceSources` (`bus_hooks_guard_test.go:41-49`); each wraps its options with `k8s.WithBaseContext` |
| `bus_hooks_guard_test.go` | `TestEveryServicePassesBusHooks` | `test/guards/bus_hooks_test.go` `TestEveryBusConnectionCarriesTraceHooks` | discovers every `busconn.Connect` and `natsbus.New` call; now covers markers and transcode, which build their hooks by hand |
| `chart_cardigann_test.go` | `TestChartIndexarrCardigannReachesIndexarr`, `TestChartRefusesTwoCardigannDefinitionSources` | `test/guards/manager_values_test.go` (`TestCardigannValuesReachTheManager`, `TestChartRefusesTwoCardigannDefinitionSources`); `cmd/manager/cli_test.go` (env → options) | values move to `manager.cardigann`; env and volume to the manager |
| `chart_images_test.go` | `TestChartImagesMatchConfig`, `TestTranscoderImagesAreWhatSquasharrStampsOntoPools`, `TestTheTranscoderImageCarriesNoFFmpegExecutable`, `TestTheOneTranscoderImageCarriesTheIntelStack` | `test/guards/images_test.go` | the first kept; the others become `TestNativeImageIsWhatTheManagerStampsOntoPoolsAndEngines` (`image.native` == `CLUSTARR_NATIVE_IMAGE` in both installers, and the engine and pool renders use it; no CUDA target, no `NVIDIA_VISIBLE_DEVICES`, ADR-0015 kept), `TestTheNativeImageCarriesNoMediaExecutable` (`ffmpeg`, `ffprobe` and `par2` refused by `stage.sh`; `FROM scratch AS native`), `TestTheNativeImageCarriesTheIntelStack`; plus `TestEveryContainerNamesItsBinary` and `TestTheClustarrImageIsStatic` (distroless static base, no cgo stage) |
| `chart_rbac_test.go` | `TestChartRBACMatchesTheGeneratedRoles` | `test/guards/chart_rbac_test.go` | logic unchanged; eight roles from `installtest.RBACRoles` |
| `cli_test.go` | `TestVersionCommand`, `TestVersionCommandTakesNoArguments` | each cmd `version_test.go` `TestVersionFlag` | `--version` plus the `version` subcommand |
| ″ | `TestRootListsEveryService` | retired | no service subcommands; covered by `TestEveryAgentDeploymentRunsOneDomain` and `cmd/agent` `TestEveryDomainIsAccepted` |
| ″ | `TestCatalogarrManagerOptions`, `TestGrabarrManagerOptions`, `TestSquasharrManagerOptionsAndSlots`, `TestCaptionarrManagerOptions`, `TestEverySubcommandBuildsManagerOptions` | `cmd/manager/cli_test.go` `TestManagerOptions`; the cache assertions in `internal/cli/manager` `TestManagerCacheOptions` (§5.15) | `TestManagerOptions`: flags parse into one lease `manager.clustarr.io`, leader-elected, the gated lease lock, metrics and probe addresses; the cache shape (Secret and ConfigMap uncached, the Job `ByObject`) is asserted once, in `TestManagerCacheOptions` |
| ″ | `TestIndexarrManagerOptions`, `TestIndexarrFacadeSettings`, `TestIndexarrRejectsLeaderElection` | `cmd/agent/index_flags_test.go` (`TestIndexDomainOptions`, `TestFacadeSettings`); `cmd/agent/cli_test.go` `TestAgentManagersNeverElect` | the agent has no `--leader-elect` flag |
| ″ | `TestIndexarrCardigannDefinitionsDir`, `TestIndexarrCardigannBundled` | `cmd/manager/cli_test.go` | the bundle loader is the manager's |
| ″ | `TestGrabarrDataClaimComesFromTheEnvironment`, `TestGrabarrEngineServiceAccountComesFromTheEnvironment`, `TestSquasharrWorkerSettingsComeFromTheEnvironment`, `TestSquasharrIntelRenderGroups`, `TestSquasharrRejectsABadSlotFlag` | `cmd/manager/cli_test.go` | renamed without the service prefix; the image setting is `CLUSTARR_NATIVE_IMAGE` |
| ″ | `TestGrabarrEngineRoleNeedsAnIdentity` | `cmd/agent/cli_test.go` `TestEngineDomainsNeedAnIdentity` | identity from `POD_NAME` (R4) |
| ″ | `TestUnknownRoleIsRejected` | `cmd/agent/cli_test.go` `TestUnknownDomainIsRejected` | |
| ″ | `TestOffsetAddress`, `TestAllGivesEachServiceItsOwnPorts` | retired | `clustarr all` is removed (owner ruling) |
| `deploy_args_test.go` | `TestManagerManifestsMatchTheCLI` | `test/guards/topology_test.go` `TestInstallersRenderTheTopology`; each cmd `manifest_test.go` | the topology map (`:50-77`) becomes §10.2.1's table, checked against both renders (names, replicas or none, strategy, image, command, `--leader-elect`, `/data` mode, grace); the argv parse moves to each binary |
| ″ | `TestCatalogarrRoleCombinations` | retired | roles become domains |
| ″ | `TestEnvironmentSuppliesDefaults` | `cmd/manager/cli_test.go`, `cmd/agent/cli_test.go` | `NATS_URL` for both; `CLUSTARR_INDEX_PATH` for agent |
| ″ | `TestWorkerGracePeriodsCoverAckWait` | `test/guards/autoscale_test.go` `TestGracePeriodsCoverAckWait` | every Deployment |
| `deploy_parity_test.go` | `TestChartAndKustomizeAgreePerComponent` | `test/guards/parity_test.go` | `parityKinds` gains `Deployment`, `Service`, `APIService`; the RoleBinding filter also accepts roleRef `extension-apiserver-authentication-reader` and asserts namespace `kube-system` |
| `external_url_chart_test.go` | `TestExternalURLFlagReachesTheChart` | `test/guards/ui_values_test.go` | renders `ui.plex.externalURL`; the argv parse is `cmd/ui/manifest_test.go` |
| `grabarr_data_claim_test.go` | `TestGrabarrEnginesMountAClaimTheInstallerCreates`, `TestGrabarrEnginesRunAsAnAccountTheInstallerBinds` | `test/guards/engine_identity_test.go` (`TestManagerStampsAClaimTheInstallerCreates`, `TestEnginesRunAsAnAccountTheInstallerBinds`); `cmd/manager/cli_test.go` | the manager's `CLUSTARR_DATA_CLAIM` and `CLUSTARR_ENGINE_SERVICE_ACCOUNT` against the rendered PVC and the SA bound to `grabarr-engine` |
| `helpers_test.go` | (helpers) | `test/installtest` | exported |
| `importarr_data_mount_test.go` | `TestEveryImportarrDeploymentMountsTheDataClaim` | `test/guards/data_mount_test.go` `TestDataMountsMatchTheTopology` | manager, agent-import and agent-caption rw; markers ro; no other Deployment mounts the claim |
| `importarr_sample_test.go` | `TestImportarrSampleMaxBytesReachesTheOptions`, `TestImportarrListBaseURLsReachTheOptions` | `cmd/agent/import_flags_test.go`; `cmd/manager/cli_test.go` (base URLs for the ImportList device flow) | the `allServiceRun` legs (`:79`, `:164`) are deleted |
| `index_dsn_wiring_test.go` | `TestIndexDSNFlagReachesIndexerOptions`, `TestIndexDSNEnvDefaultsBothCommands`, `TestIndexDSNEmptyKeepsSQLite` | `cmd/agent/index_flags_test.go` | "both commands" becomes the one index domain |
| `indexarr_defaults_test.go` | `TestDefaultIndexPathMatchesTheManifest`, `TestDefaultFacadeBindAddressMatchesTheServicePort` | `cmd/agent/index_flags_test.go` | reads `config/manager/agent-index.yaml` |
| `keda_consumer_test.go` | `TestCaptionarrScaledObjectCountsTheRealFetchConsumers` | retired; successors `TestEveryConsumerHasExactlyOneHome`, `TestAutoscaledDeploymentsAreDeclared` | KEDA is removed; the intent ("the scaler counts the consumers that really exist") is kept |
| `lint_guard_test.go` | `TestForbidigoRejectsStatusWrites` | `test/guards/lint_test.go` | unchanged |
| `nats_payload_test.go` | `TestBothInstallersRaiseTheNATSMaxPayload` | `test/guards/nats_test.go` | unchanged |
| `nats_url_deploy_test.go` | `TestEveryNATSDialingDeploymentCarriesNATSURL` | `test/guards/nats_test.go` `TestEveryClustarrContainerCarriesNATSURL` | every container whose command is a `binpath` constant carries `NATS_URL` (all five dial NATS), removing the "skips argv the root cannot Find" vacuity; the ui value equals the manager's |
| ″ | `TestUICommandsCloseTheirBusOnShutdown`, `TestBuildUIArtworkLogsAnUnconnectedBus` | `cmd/ui/bus_test.go` | one command |
| `postgres_parity_test.go` | `TestChartAndKustomizePostgresAgree`, `TestChartHasNoNackReference` | `test/guards/postgres_test.go` | Deployment `agent-index`; `config/postgres/agent-index-patch.yaml` |
| `rbac_markers_test.go` | `TestEveryPackageWithRBACMarkersIsInARole`, `TestEveryGeneratedRoleMatchesItsMarkers`, `TestRBACMarkersArePackageLevel`, `TestGeneratedRolesCoverEveryStatusWriter`, `TestEveryCRDKindAControllerTouchesHasAnRBACMarker`, `TestEveryBuiltinKindAControllerTouchesHasAnRBACMarker` | `test/guards/rbac_markers_test.go` | unchanged logic, plus §10.3.2's two |
| `rbac_split_test.go` | `TestEachServiceAccountHoldsExactlyItsOwnRole` | `test/guards/rbac_split_test.go` | `identityOf`'s suffix fallback (`:56-66`) goes (component equals identity); the `>= 11` floor (`:93`) becomes exact set equality with `{manager, agent-catalog, agent-events, agent-metadata, agent-import, agent-index, agent-caption, grabarr-engine, ui}` |
| ″ | `TestNoInstallerShipsASquasharrWorkerIdentity` | `test/guards/rbac_split_test.go` `TestCredentialLessWorkloadsHoldNoToken` | the markers SA and pod set `automountServiceAccountToken: false` and nothing binds the SA; no SA, Role or binding is named `squasharr-worker`, `segmentarr-worker` or `transcode` |
| `root_test.go` | `TestEveryServiceHasASubcommand` | retired | as `TestRootListsEveryService` |
| ″ | `TestVersionHasNoShorthand` | each cmd `version_test.go` | |
| `runnable_registration_test.go` | `TestEveryManagerRunnableIsRegistered`, `TestEveryServiceComponentIsRegistered`, `TestNoBareRunnableFunc`, `TestEveryEveryReplicaIsAdded`, `TestControllerNamesAreUniqueAcrossTheBinary` | `test/guards/registration_test.go` | below |
| `segmentarr_worker_test.go` | `TestSegmentarrWorkerHasNoCredentials` | `test/guards/markers_test.go` `TestMarkersHasNoCredentials` | name suffix `markers`, command `/usr/bin/markers`, `/data` read-only, no token, no binding |
| `squasharr_worker_test.go` | `TestChartIntelRenderGroupsReachTheController` | `test/guards/manager_values_test.go` `TestIntelRenderGroupsReachTheManager`; `cmd/manager/cli_test.go` | |
| `start_artwork_envtest_test.go` | (`verifyRenderer`) | `cmd/agent/start_envtest_test.go` (task published directly); `test/system` (through the OverlayProfile controller) | |
| `start_envtest_test.go` | `TestServiceStartsServesProbesAndStopsOnSignal`, `TestServiceFailsFastOnBadOptions`; helpers | `cmd/manager`, `cmd/agent`, `cmd/ui` `start_envtest_test.go`; `test/system`; helpers in `test/starttest` | below |
| `start_nonvideo_envtest_test.go` | (`verifyNonVideoCatalog`, `verifyRetrigger`, `startFakeMetadataProviders`, `statusManagers`) | `test/system` (non-video: manager plus agent-metadata); `cmd/manager` (`verifyRetrigger`); `test/starttest` (`StatusManagers`) | |
| `start_rbac_envtest_test.go` | (`identityKubeconfigs`, `caseIdentity`) | `test/starttest.IdentityKubeconfigs` | `caseIdentity` and `identityDirs` (`:35-55`) go: every case names its identity |
| `start_ui_envtest_test.go` | (`verifyUI` and helpers) | `cmd/ui/start_envtest_test.go` | |
| `static_link_guard_test.go` | `TestClustarrNeverLinksADynamicLoader` | `test/guards/deps_test.go` | all five binaries (§4.5) |
| `storage_security_test.go` | `TestEveryPVCMountingWorkloadGetsFsGroup` | `test/guards/storage_test.go` | unchanged intent; reads `_helpers.tpl`'s `if or .data .index` and every `config/manager` file |
| `ui_art_key_deploy_test.go` | `TestBothInstallersGiveTheUIAStableArtSigningKey` | `test/guards/ui_values_test.go` | unchanged |
| `ui_auth_test.go` | `TestUICommandsHandTheChosenAuthModeToRunUI`, `TestChartUIAuthModeReachesUI` | `cmd/ui/cli_test.go`, `cmd/ui/manifest_test.go` | one command |
| `ui_options_wiring_test.go` | `TestBothUICommandsWireEveryUIOption`, `TestBothUICommandsReadTheArtSigningKey`, `TestArtSigningKeyRefusesAShortKey`, `TestBothUICommandsPassPlexGUIDs` | `cmd/ui/wiring_test.go` | "both" becomes cmd/ui alone (`TestUIWiresEveryUIOption`, …); the `run<Svc>` stubs (`:110-115`) go |
| `ui_projection_wiring_test.go` | `TestEveryProjectionStreamIsWiredIntoBothUICommands` | `cmd/ui/wiring_test.go` | reads `internal/cli/ui/*.go` instead of `services.go` and `all.go` (`:106`) |
| `ui_rbac_test.go` | `TestUIRoleGrantsOnlyReadsAndActionWrites`, `TestUIRoleChartMatchesConfig`, `TestUIRoleIsBoundToTheUIServiceAccount` | `test/guards/ui_rbac_test.go` | unchanged |
| `umask_test.go` | `TestEveryCommandAppliesUMASK` | `cmd/manager`, `cmd/agent`, `cmd/ui`, `cmd/markers`, `cmd/transcode` `umask_test.go` | each binary applies `UMASK` itself |

**`registration_test.go` rewrite.** `runnableServices` stops parsing `services.go`'s
`run<Svc> = <pkg>.Run` block (`:46-129`) and discovers the registration packages
imported by `internal/cli/manager` and `internal/cli/agent`, plus
`app/segments/worker` for markers and `app/squash/worker` for transcode. Floors: at
least 6 manager registrations, at least 6 agent domain registrations, and
`app/segments` (never covered before). `wiringFiles`' `"."` (`:439`) becomes the
binary that registers the package. `TestControllerNamesAreUniqueAcrossTheBinary`
runs over the manager registration packages only.

**Start envtests, per identity (R10):**

- **`cmd/manager`, two cases under identity `manager`**, both with the default
  `--autoscale=true`:
  - *Non-leader.* The readiness proof (`starttest.HoldLease(manager.clustarr.io)`,
    then `/readyz` goes green while not leader, the C12a class; then release),
    keeping the proof `app/import/all (non-leader)` carried at
    `start_envtest_test.go:539-567`. It passes with autoscaling on because no
    readiness check depends on leader-only work (§3.3, §9.4); the case also
    asserts the External Metrics server is listening and refuses the TLS
    handshake (no Secret yet), and that no HPA, caBundle or Secret was written.
  - *Leader.* `verifyBundle`, `verifyRetrigger`, `verifyCaptionarrController`;
    the grab-controller and squash-controller bodies (`:232-309`, `:415-466`),
    now asserting the rendered engine command `/usr/bin/agent` and pool command
    `/usr/bin/transcode`; the CertManager creates `external-metrics-tls` and
    injects `spec.caBundle` into an installer-shaped APIService pointing at
    `external-metrics`, after which an authenticated request is answered (the
    test writes its own CA into `kube-system/extension-apiserver-authentication`'s
    `requestheader-client-ca-file` and `requestheader-allowed-names` when
    envtest's apiserver left them unset, and presents a client cert from it);
    `AssertWroteOnlyAs`.
- **`cmd/agent`, one case per domain under its own identity:** catalog runs
  `verifyRenderer` (direct task); events runs `verifyRedownload`; metadata, import
  also run; index runs `verifyFacade`; caption runs `verifyCaptionarrWorker`;
  torrent-engine and usenet-engine use today's bodies (`:310-414`) under
  `grabarr-engine`. Each min-0 domain also runs §9.9's recording-bus assertion.
- **`cmd/ui`, one case:** `verifyUI`.
- **`test/system`**, flows needing two identities at once: `verifyNonVideoCatalog`
  (manager plus agent-metadata); `verifyHistory` (replay in the manager, sink and DLQ
  projector in agent-events); `verifyImportList` (the ImportList controller in the
  manager, sync in agent-import); `verifyRenderer` through the OverlayProfile
  controller (manager plus agent-catalog); a MediaFile probe round trip (the
  manager's reconciler plus agent-import's probe worker). Each process registers
  each controller name once, so the manager's controllers and the agents'
  consumers can share it.

#### 10.3.4 The other two cmds, and tests elsewhere that name old binaries, images or Deployments

| file:line | today | change |
| --- | --- | --- |
| `cmd/segmentarr-worker/main_test.go` | `TestMissingNATSURLExitsMisconfigured`, `TestABadFlagExitsMisconfigured` | `git mv` to `cmd/markers/main_test.go`, both kept; add `TestVersionFlag` and `TestSelfCheckNeedsNoClusterEnvironment` (ffgo + ORT, `nativetest.Require`) |
| `cmd/squasharr-worker/main_test.go` | `TestMain`, `env`, `fakeTools`, `ensureTopology`, `TestMissingEnvironmentIsMisconfigured`, `TestUnreachableNATSIsRetriable`, `TestSIGTERMIsDrained`, `TestRunNeverReturnsZero`, `TestBinaryImportsNoKubernetesClient`, `TestSelfCheckNeedsNoClusterEnvironment` | `git mv` to `cmd/transcode/main_test.go`; `fakeTools` (`:55-63`) and its fake `ffmpeg`/`ffprobe` on PATH go (R2); `TestBinaryImportsNoKubernetesClient` leaves this file and becomes `test/guards/deps_test.go` `TestTranscodeImportsNoKubernetesClient` (§4.5.5), the one home of every deps guard; the rest kept; add `TestVersionFlag` |
| `app/squash/exec_guard_test.go:46` | `TestTheWorkerNeverExecsFFmpeg`, rooted at `../../cmd/squasharr-worker` | deleted; successor `TestNoProductionCodeStartsAProcess` (§4.5.6) |
| `app/squash/controller/pool/template_test.go:79-88` | reads `config/manager/squasharr.yaml` | reads `config/manager/manager.yaml`; asserts `Command == []string{binpath.Transcode}` and component `transcode` |
| `app/squash/slots_test.go:102` | message "pools run cmd/squasharr-worker" | "cmd/transcode" |
| `app/squash/status/status_envtest_test.go:445` | `k8s.FieldManager("squasharr-worker")` must be refused | unchanged (R7) |
| `app/grab/controller/downloadclient/workload_security_test.go:115-124` | reads `config/manager/grabarr.yaml` | reads `config/manager/manager.yaml` |
| `app/grab/controller/downloadclient/workload_test.go:116,152,199` | asserts `/bin/sh -c … clustarr grabarr --role …` and `"grabarr", "--role", "usenet-engine"` | asserts `Command == []string{binpath.Agent}` and args starting `--domain=torrent-engine` or `--domain=usenet-engine`; `/bin/sh` appears nowhere |
| `app/grab/run_test.go:36-41`, `app/import/run_test.go:40-43` | assert `grabarr.clustarr.io` and `importarr.clustarr.io` | retired with their `run.go`; the lease is asserted once, in `cmd/manager` `TestManagerOptions` |
| `app/indexer/wiring_envtest_test.go:652` | parses `run.go` | follows the indexer registration split (§5.12) |
| `ui/guard_test.go:195` | message "cmd/clustarr binds …" | "cmd/ui binds" |
| `pkg/k8s/manager_test.go:56`, `pkg/k8s/fieldmanager_test.go:25-30`, and `app/*` tests asserting manager names | field-manager and lease strings as data | unchanged (R7); `fieldmanager_test.go` adds `clustarr-autoscale` |
| `test/fixtures/{gestdownstub,opensubtitlesstub}/server_test.go` | User-Agent `clustarr/captionarr-worker` | unchanged (the provider clients' HTTP User-Agent) |

#### 10.3.5 Where every test goes: the six `app/<svc>` root packages

Wave 5.6 deletes `app/{catalog,import,indexer,grab,squash,caption}` (the
`run.go` packages). Every `Test` in their `*_test.go` files (60 functions in 16
files) goes here or is retired with its reason. A `TestMain` goes with each new
envtest package.

| file | tests | new home | change or reason |
| --- | --- | --- | --- |
| `app/catalog/artwork_wiring_envtest_test.go` | `TestTheRenderConsumerRunsUnderArtworkAndAllOnly` | retired | roles are gone; "only the catalog domain subscribes `catalogarr-artwork-render`" is the catalog domain's recording-bus start envtest (§9.9) and `TestEveryConsumerHasExactlyOneHome` |
| `app/catalog/wiring_envtest_test.go` | `TestSetupWorkersLeavesTheBlocklistPathLive` | `app/catalog/agent/catalog` and `app/catalog/agent/events` `TestTheDomainLeavesTheBlocklistPathLive` | each domain declares `search.IndexDownloadTarget` and the index is live before its consumers start (the C12a proof) |
| ″ | `TestQueueWorkersShareRunsWiring` | `app/catalog/agent/catalog`, `app/catalog/agent/events` `TestQueueWorkersShareTheDomainsWiring` | the nil-seam fallbacks per domain |
| ″ | `TestAssertWorkerIndexesFailsWhenTheIndexesAreMissing` | `app/catalog/agent` `TestAssertIndexesFailsWhenTheIndexesAreMissing` | the generalised assert helper (§3.5.2 step 9) |
| ″ | `TestRegisterWorkerIndexesIsTheOnlyRegistrar` | `internal/cli/agent` `TestTheAgentIsTheOnlyIndexRegistrar` | AST: declared indexes are registered only by `internal/cli/agent`, never by a worker's `SetupWithManager` |
| ″ | `TestSetupWorkersSkipsTheQueueWorkersForRoleMetadata` | `app/catalog/agent/metadata` `TestTheMetadataDomainSubscribesNoQueueWorker` | recording bus: only the gateway's durables and RPCs |
| ″ | `TestSetupControllersRegistersEveryCatalogController` | `app/catalog/manager` `TestRegisterAddsEveryCatalogController` | envtest; QualityProfiles still seeded |
| `app/import/dataready_internal_test.go` | `TestDataCheckAnswersFromTheLastProbeWhileASlowOneRuns`, `TestDataCheckRunsOneProbeAtATime`, `TestDataCheckFailsAProbeStalledPastTheLimit`, `TestDataCheckIsNotReadyUntilAProbeFinishes`, `TestDataCheckReportsAFailureAndRetriesAtOnce`, `TestDataCheckReusesAFreshResult` | `pkg/k8s/dataready_internal_test.go` | unchanged, with `DataReadyChecker` (§4.3 step 1.6) |
| `app/import/run_internal_test.go` | `TestWorkersGetTheSampleSizeFloor`, `TestImportWorkerGetsTheAPIReader`, `TestScanWorkerGetsTheAPIReader`, `TestDefaultOptionsTurnTheSampleSizeFloorOn` | `app/import/agent` (package-internal) | unchanged |
| ″ | `TestImportListsGetTheirBaseURLs` | `app/import/agent` (list worker) and `app/import/manager` (ImportList controller's device flow) | split along the two sides; one Trakt host |
| ″ | `TestWithoutMediaInfoChainsTheDefaultTransform` | retired | `withoutMediaInfo` is deleted (§5.6); `TestManagerCacheOptions` asserts there is no MediaFile transform |
| `app/import/run_test.go` | `TestRunRejectsAnUnknownRole` | retired | no roles; `cmd/agent` `TestUnknownDomainIsRejected` |
| ″ | `TestManagerOptionsUseTheImportarrLeaderElectionID`, `TestAllRoleLeaderElectsWhenAsked` | retired | one lease, asserted in `cmd/manager` `TestManagerOptions`; no `all` |
| ″ | `TestWorkerRoleDoesNotLeaderElect` | `cmd/agent/cli_test.go` `TestAgentManagersNeverElect` | |
| ″ | `TestValidateRejectsAnEmptyDataPath` | `cmd/agent/import_flags_test.go` `TestImportDomainRejectsAnEmptyDataDir`; `cmd/manager/cli_test.go` `TestManagerRejectsAnEmptyDataDir` | `--data-path` became `--data-dir` |
| ″ | `TestValidateRequiresTheBus` | `app/import/manager`, `app/import/agent` `TestRegisterRequiresTheBus` | |
| ″ | `TestDataReadyCheckerRejectsAMissingDirectory`, `TestDataReadyCheckerAcceptsAWritableDirectory` | `pkg/k8s/dataready_test.go` | unchanged |
| ″ | `TestTheControllerRoleCachesMediaFilesWithoutMediaInfo` | retired, inverted | the manager must keep `status.mediaInfo` (§5.6); `TestManagerCacheOptions` holds the opposite |
| ″ | `TestManagerOptionsNeverCacheSecretsOrConfigMaps` | `internal/cli/manager` `TestManagerCacheOptions`; `internal/cli/agent` `TestAgentCacheNeverHoldsSecretsOrConfigMaps` | |
| `app/indexer/rssdeps_test.go` | `TestTheRSSPollGetsTheBusAndAnUncachedReader` | `app/indexer/agent` | unchanged |
| `app/indexer/run_test.go` | `TestIndexSweeperIsLeaderElected` | retired, inverted: `app/indexer/agent` `TestIndexSweeperRunsOnEveryReplica` | ruling R1 ("the sweep is leader-only") is superseded: agents never elect and the index domain is fixed at 1 under both engines, so the sweeper is `EveryReplica` (§5.12) |
| ″ | `TestIndexSweeperPrunesOnceOnStartBeforeTicking` | `app/indexer/agent` | unchanged |
| ″ | `TestLeaderElectionIsGatedOnIndexDSN` | retired | no index lease (§5.12) |
| `app/indexer/wiring_envtest_test.go` | `TestEveryIndexarrRunnableIsRegistered` | `app/indexer/manager` and `app/indexer/agent` `TestEveryComponentIsRegistered`, each walking its side | |
| ″ | `TestIndexerSpecDefaultsMatchTheCRD` | `app/indexer/clients` | `defaultLimiterConfig` moves there; the manager and the agent both build their limiter from it |
| ″ | `TestTheDefaultLimiterPacesAnUnknownHost`, `TestAnExplicitZeroRequestDelayStaysUnpaced` | `app/indexer/agent` `TestAgentClientCacheAppliesRateLimit` (§5.15) | |
| ″ | `TestReadinessPassesOnANonLeaderReplica` | retired for the index | the index agent never elects; the non-leader readiness proof is `cmd/manager`'s non-leader start case (§10.3.3) |
| ″ | `TestTheIndexReadinessCheckFailsOnAClosedStore` | `app/indexer/agent` | unchanged, as `index.releaseindex` |
| ″ | `TestRunPassesTheBusHooks` | retired | registration packages connect no bus; `test/guards` `TestEveryBusConnectionCarriesTraceHooks` covers every `busconn.Connect` |
| ″ | `TestIndexarrWiringRegistersEveryComponent` | manager half and agent half (§5.12) | |
| `app/grab/proxy_internal_test.go` | `TestTorrentProxyIsNilWithoutAProxy`, `TestTorrentProxyReadsItsSecretAndSwitches`, `TestTorrentProxyWithAMissingSecretFails`, `TestProxyReadinessNeedsTheProxy` | `app/grab/agent/torrent` (package-internal) | unchanged |
| `app/grab/run_internal_test.go` | `TestEngineRuntimeCarriesTheControllerOptions` | `app/grab/manager` | `BusSingleNode` is gone (§3.5.5) |
| ″ | `TestTorrentEngineDirs` | `app/grab/agent/torrent` | unchanged |
| `app/grab/run_test.go` | `TestRunRejectsAnUnknownRole`, `TestManagerOptionsUseTheGrabarrLeaderElectionID` | retired | as for import |
| ″ | `TestEngineRoleDoesNotLeaderElect` | `cmd/agent/cli_test.go` `TestAgentManagersNeverElect` | |
| ″ | `TestValidateRequiresEngineForAnEngineRole` | `cmd/agent/cli_test.go` `TestEngineDomainsNeedAnIdentity` | |
| ″ | `TestValidateRejectsEngineOnAControllerRole` | `cmd/agent/cli_test.go` `TestDomainFlagsNeedTheirDomain` | `--engine` set for a non-engine domain is the §3.5.1 domain-flag error |
| ″ | `TestValidateRequiresEngineImageForTheControllerRole` | `cmd/manager/cli_test.go` `TestManagerRequiresTheNativeImage` | |
| ″ | `TestValidateDoesNotRequireEngineImageForAnEngineRole` | retired | the agent has no image flag |
| ″ | `TestValidateRequiresTheBus` | `cmd/manager`, `cmd/agent` `TestEnvironmentSuppliesDefaults` | an empty `--nats-url` is rejected |
| ″ | `TestManagerOptionsNeverCacheSecrets` | `internal/cli/manager` `TestManagerCacheOptions`; `internal/cli/agent` `TestAgentCacheNeverHoldsSecretsOrConfigMaps` | |
| `app/grab/split_test.go` | `TestSplitEngineIdentity` | `app/grab/agent/torrent` `TestEngineIdentity` | from `POD_NAME` or the hostname; keeps the hyphenated-DownloadClient case and adds the refusals of §3.5.5 |
| `app/squash/cache_test.go` | `TestTheManagerCachesOnlyThePoolJobs` | `app/squash/manager` `TestCacheSelectsOnlyThePoolJobs`; the merge in `TestManagerCacheOptions` | |
| `app/squash/observability_test.go` | `TestWorkerObservabilityArgsParse`, `TestPoolConfigCarriesTheControllerOptions` | `app/squash/manager` | parsed back by `cmd/transcode`'s flag set (`cmd/transcode/manifest_test.go`) |
| `app/squash/slots_test.go` | `TestParseSlotsDefaultsToTheSpecBudget`, `TestParseSlots`, `TestParseSlotsRejectsBadInput`, `TestFormatSlotsRoundTrips` | `app/squash/manager` | with `ParseSlots`/`FormatSlots` (§4.2.1) |
| ″ | `TestRolesAndValidate` | retired | roles; `--slots` validation is `cmd/manager` `TestRejectsABadSlotFlag` |
| `app/squash/exec_guard_test.go` | `TestTheWorkerNeverExecsFFmpeg` | §10.3.4 | |
| `app/caption/run_test.go` | `TestRolesSelectTheirHalves` | retired | the halves are separate packages; RoleAll's hazard (a controller whose fetch consumer nothing runs) is `TestEveryConsumerHasExactlyOneHome` |

### 10.4 `test/e2e` and `hack/e2e.sh`

The scenarios are written but have never run past Phase C. These edits keep them
compiling under `go vet -tags e2e ./test/...` (`ci.yml:94`) and naming what will exist.

| file:line | change |
| --- | --- |
| `test/e2e/main_test.go:228-245` | `deploymentRoster` becomes `"manager", "agent-catalog", "agent-events", "agent-metadata", "agent-import", "agent-index", "agent-caption", "markers", "ui"` plus the twelve stubs, unchanged; `markers` is new to the roster (`segmentarr-worker` was deployed but never checked) |
| `hack/e2e.sh:41-59` | `WORKLOADS` gets the same nine as `deployment/<name>` (stubs and `statefulset/nats` unchanged); after the rollouts, `kubectl wait --for=condition=Available apiservice/v1beta1.external.metrics.k8s.io --timeout=300s`; the build step text (`:76`) becomes "building the clustarr, native and fixture images"; the failure dump also writes `kubectl get hpa,apiservice -o yaml` (`hpa-apiservice.yaml`) |
| `test/e2e/transcode_test.go:945-953` | the trace check reads `"manager"` (the Download controller publishes the import task) and `"agent-import"` (fileimport consumes it), by label across pods (§5.11); comment `:31` names `deployment/manager` |
| `test/e2e/zzz_observability_test.go:194,208,220,230,265-276` | `fetchMetrics`: `grabarr`, `squasharr` and `importarr-worker` become `manager` (queue depth comes from the manager's `QueueGauge`); `indexarr` becomes `agent-index`; the `readyz` checks on `catalogarr` become `manager` |
| `test/e2e/indexer_test.go:676,698` | Deployment and component label `indexarr` become `agent-index` |
| `test/e2e/cardigann_test.go:131,227` | `requireFixtureService` and `portForwardService("indexarr", 8080)` become `agent-index`; `facadeAPIKeySecretName = "indexarr-facade"` (`:100`) unchanged; comment `:97` names `config/manager/agent-index.yaml` |
| `test/e2e/download_test.go:303` | `kubectlRolloutRestart(ctx, t, "grabarr")` becomes `"manager"` |
| `test/e2e/parity_test.go` | delete `TestClustarrAllSinglePodPassesScenarioOne` and its doc paragraph; `TestHelmChartE2EValuesPassesScenarioOne` stays skipped |
| `test/e2e/ui_test.go:150,161`, `helpers_test.go:1468,1631` | message text: "cmd/ui", "agent-import" |
| `config/e2e/resources-patch.yaml` | delete the `replace /spec/replicas` op (a JSON6902 `replace` fails on rows with no `spec.replicas`); every fixed row and stub already declares 1 or defaults to it; the cpu and memory request ops stay |
| `config/e2e/kustomization.yaml` | `indexarr-e2e-patch.yaml` targets `agent-index` and `manager` (indexer health is written by both under R8); `importarr-e2e-patch.yaml` targets `manager` (device flow) and `agent-import` (syncs); new `autoscale-e2e-patch.yaml` adds `autoscale.clustarr.io/min-replicas: "1"` with target `kind: Deployment, labelSelector: autoscale.clustarr.io/domain`, so logs, metrics and the trace check always find a pod (§13 OD41); `ui-e2e-patch.yaml` unchanged |
| new `test/e2e/zzz_scale_from_zero_test.go` | `TestCaptionAgentScalesFromZero`, the §11.2 gate-6 scenario. It runs after every other scenario (file order: `zzz_observability` < `zzz_scale…`) because it removes the always-a-pod guarantee the others rely on. Steps: `kubectl annotate deployment agent-caption autoscale.clustarr.io/min-replicas-`, with `t.Cleanup` restoring `"1"` and waiting for the Deployment to be Available; wait up to 7 min for `status.replicas == 0` (the 300 s scale-down window plus HPA syncs); assert `kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/<ns>/clustarr_consumer_lag?labelSelector=consumer%3Dcaptionarr-fetch-high` answers an item with value `0`; create the subtitle fixture `TestSubtitleRequestSidecarPipelineAndLanguageRemoval` uses (its setup moves to a shared helper), so the manager publishes fetch tasks; assert the HPA scales `agent-caption` to 1 within 2 min and the sidecar appears; then wait up to 7 min for 0 again. The test takes up to about 17 min, so the `make e2e` target's whole-suite `-timeout 30m` (`Makefile:247`, the one definition `hack/e2e.sh` calls) becomes `60m` |

---
## 11. Migration and cutover (not executed)

Nothing in this section has been run. §11.1 and §11.2 are for the sessions that
build the branch; §11.3 is for a later session, with the owner's explicit OK, on
kind-cluster-plex.

### 11.1 Build order

Every step keeps `go build ./... && go vet ./... && make test` green, commits
path-scoped (`git commit -m … -- <paths>`), and never stashes or pushes (R14). The
three go.mod edits are made serially by the controlling session, never by a
parallel agent and never with `go mod tidy`.

| # | Step | Depends on | Section |
|---|---|---|---|
| 0a | ffgo fork F1-F17 on `clustarr/unify-media` (worktree `../ffgo-unify`, cut from main's `.12`), `go test ./...`, tag `v0.0.0-clustarr.13` locally | — | §7.5 |
| 0b | par2go Tasks 1-8, locally | — | §8.11 |
| 0c | go.mod: ffgo replace `=> ../ffgo-unify`; par2go require + replace `=> ../par2go`; `k8s.io/component-helpers v0.37.0` (network once) | 0a, 0b; the R14 rebase (purego v0.11.1) | §7.6, §9.5 |
| 1 | Wave 1: pkg leaves (1.1-1.9) | — | §4.3 |
| 2 | Wave 2: app leaves (C1-C9, I1-I5, X1-X4, G1, S1, P1) | 1 | §4.3 |
| 3 | Wave 3: registration packages, R9 moves, cmd/clustarr shims | 2 | §4.3 |
| 4 | R3 queue: topology, schema, `pkg/probestore`, the reconciler, `probeRecordsSource`, the downstream gates; the probe worker answers through transitional `ffprobeexec` | 3 | §6.5 |
| 5 | R8: index agent ClientCache, generation-gated limiter, session CAS, `WatchSessions`, `sessionsSource` | 3 | §5.12 |
| 6 | R5 bus: `Slots`, slot-gated pull, lapse reclaim with the lapsed cap, drain, bind-only `Subscribe` (durable and dead-letter watcher) with re-binding, the watchers as topology objects, `ConsumerState`, `Missing`, `events.DeadLetterWatcher`, `busconn.KeepTopology` and `WatchDeadLetters`, the `Validate` changes, membus parity, contract tests | 1 | §9.2, §9.3, §5.9 |
| 7 | R5 manager: `pkg/agentdomain`, `app/autoscale/{controller,extmetrics}` | 0c, 3, 6 | §9.4, §9.5 |
| 8 | Wave 5: `internal/cli/*`, `cmd/manager`, `cmd/agent`, `cmd/ui`, the `cmd/markers`/`cmd/transcode` renames, deps guards (the agent's `Require` list holds only `app/grab/engine/torrent` and `go-llsqlite/adapter` here, §4.5.3), `pkg/binpath`, engine argv and pool command; then delete `cmd/clustarr` and the `app/<svc>` roots, moving their tests per §10.3.5 | 3-7 | §3, §4.3 |
| 9 | Images, installers, RBAC regeneration, test moves, e2e renames, KEDA removal. The native image lands **without** the par2 stage, the `libpar2shim.so` trace entry, `ENV PAR2GO_LIB`, `self-check:par2-child` and the par2go and ffgo parts of `agent --self-check` (until step 11 the agent links no ffgo, until step 12 no par2go): `stage.sh`'s `trace` fails on a missing file (`:14`), so each lands with the step that makes it exist, the way §4.3 step 5.1 handles `ffprobeexec` | 8 | §10 |
| 10 | Markers decode on ffgo (markers already links purego through ONNX, and `cmd/clustarr` never linked segments, so this may land any time after 0c) | 0c | §7.2 |
| 11 | Native probe (`pkg/mediainfo/native`; `ffprobeexec` becomes `test/ffprobeoracle`); embedded extraction on ffgo; `pkg/ffruntime` in transcode; the agent deps guard gains `Require` ffgo and `pkg/mediainfo/native`; `agent --self-check` gains `ffruntime.Load` and `Require` | 0c, 8 (these put purego into packages `cmd/clustarr` links, which `TestClustarrNeverLinksADynamicLoader` forbids until it is deleted) | §6, §7.3, §7.4 |
| 12 | par2 integration (`pkg/par2child`, usenet `Repairer`, dispatch); the agent deps guard gains `Require` `pkg/par2child` and par2go; the image's par2 stage, `trace` entry, `ENV PAR2GO_LIB`, `self-check:par2-child` and the par2go part of `agent --self-check` | 0b, 0c, 8, 9 (same reason) | §8 |
| 13 | Exec guard allow-list down to `pkg/par2child/exec.go` | 10-12 | §4.5.6 |
| 14 | Docs and ADRs | all | §12 |

**Amended 2026-10-07 (NATS research).** A step **7b**, the NATS follow-ups
(plan Wave 4f): M1 and M2 first, then S2, S5, S7, S9, S10 and S1, the HPA
additions of §9.4 and §9.3, the object-store work (artwork design §B as amended,
loop spec §4.15 as amended), the guards, and last the could-defer S3 and S4. It
depends on 6 and 7 (and on 8 only where it wires a flag, a liveness check or a
guard into `internal/cli`, `cmd/markers` or `test/guards`), runs beside 8, and is
green before the fold's first wave, whose records waker and `KV.Watch` options
build on its `events.WatchOption` (loop spec §4.15 as amended).

### 11.2 Gates before any deploy

1. `make test` and `make test-race` with `CLUSTARR_REQUIRE_TOOLS=1` and
   `PAR2GO_REQUIRE=1`, and `make lint`, green on the rebased branch.
2. **Probe parity:** `TestNativeProbeMatchesFFprobeOnACorpus` read-only against the
   live library mount (`CLUSTARR_PROBE_PARITY_DIR`) and a MediaFile dump
   (`CLUSTARR_PROBE_PARITY_MEDIAFILES`) shows zero diffs (§13 OD16).
3. **Segments parity:** the `CLUSTARR_SEGMENTS_PARITY_FILES` library run (every
   `.ts` file included, §7.2.6), judged against §13 OD20's threshold.
4. **Embedded subtitle parity:** the `parity`-tagged byte comparison.
5. **Images:** `make docker-build-native docker-build docker-selfcheck`.
6. **Phase H e2e on a throwaway kind cluster** (`make kind-up`, `make install deploy`,
   `make e2e`), never kind-cluster-plex, including the scale-from-zero scenario
   (`TestCaptionAgentScalesFromZero`, §10.4).
7. **Owner OKs:** pushing the ffgo tag (OD18) and par2go (OD19), switching go.mod to
   tagged versions, then CI green; and, separately, the kind-cluster-plex rollout.

Gates 2 to 4 run through `make` (so `LD_LIBRARY_PATH` and
`CLUSTARR_NATIVE_ASSETS` are set, §10.1.6) or inside the `native-debug` image
with the library mount bound read-only (and `CLUSTARR_NATIVE_ASSETS=/usr`, the
image's library prefix), never against whatever FFmpeg the host
has; each report's first line prints `ffruntime.Report` (the FFmpeg version and
the paths of the libraries loaded), and `nativetest.Require` fails if those are
not the pinned build.

### 11.3 Live cutover runbook (kind-cluster-plex)

**State as inventoried** (2026-10-06 ~19:37Z; re-read before running): release
`clustarr` revision 130, chart 0.4.0, Helm v4.2.2 with server-side apply, 10
revisions of history; controller and media images at `ce0bd69`, transcoder pinned
to `44461d0`. 11 Deployments (catalogarr, catalogarr-metadata, importarr(+worker),
indexarr, grabarr, squasharr, captionarr(+worker), segmentarr-worker, ui, nats-box)
plus three CR-owned workloads (the `frugal-engine` usenet Deployment and
`torrent-engine` StatefulSet, owned by DownloadClients; one transcode pool Job owned
by TranscodeProfile `hevc-mkv`). Five `<svc>.clustarr.io` leases, none for indexarr
(SQLite, no election). No HPA, no external-metrics APIService, no KEDA, no
Prometheus; one node (12 CPU, ~62.6Gi, 1 NVIDIA and 1 Intel GPU), so every HPA's
maxReplicas will be 1. Two memory notes are stale: **downloads are not paused**
(frugal enabled, 63 Downloads Completed with `Imported=False`), and **transcoding is
stalled, not running**: the pool Job runs `transcoder:471aa61` while squasharr wants
`44461d0`, and one Running TranscodeJob
(`avatar---the-last-airbender-403da1849a-s02e09-debec48de0-c5a05678`) whose worker
pod has been gone since 2026-10-02 holds the drain, so all 31 Planned jobs wait.

**What a plain `helm upgrade` would break** (why the runbook exists): it creates the
manager before deleting the old controllers, so both write at once (the wanted cron
fires twice, the 2026-10-06 nzbgeek class; slot admission over-admits; two engine
builders flap the engine templates; indexarr's lease-less controllers and SQLite run
twice; two metadata gateways double the provider rate); it deletes the helm-owned
`clustarr-index` PVC (PV reclaim Delete); it fails the values schema under
`--reuse-values` or defaults image tags to 0.1.0; it invalidates the engines' SA
tokens if the SA is renamed (it is not, §10.0); it breaks the torrent engine's
`/bin/sh` wrapper on a FROM-scratch image unless the new manager re-renders the
engines (it does, §3.5.5); and it orphans the GPU-holding pool Job if pool names or
the managed-by label change (they do not, §3.8). `k8s.GatedLeaseLock` (§5.8) is a
safety net for the first case, not a substitute for step 5.

**Runbook** (the kubectl and helm writes are for that operator):

0. **Freeze.** Ask the other sessions (`ListAgents`; at inventory clustarr-c9 busy,
   clustarr-2b waiting, clustarr-d2 idle) to stop helm upgrades. Save
   `helm get values -o yaml` and `helm get manifest` for revision N, the rollback
   target.
1. **Images and CRDs.** Build all images and `kind load` them now, while the old
   system is healthy (a load stalls etcd and can cost every lease); wait until lease
   transitions stop moving. If a CRD changed:
   `kubectl apply --server-side --force-conflicts -f config/crd/bases/`.
2. **Protect data.** `kubectl -n clustarr-system annotate pvc clustarr-index helm.sh/resource-policy=keep`;
   confirm the art key's keep annotation; record the facade Secret name
   (`clustarr-indexarr-facade`).
3. **Quiesce transcode** (the owner approves the pause and its lift). Set
   `spec.suspend=true` on the orphaned Running TranscodeJob and all 31 Planned jobs
   (withdraws their tasks, `controller.go:388-401`); wait until pool Job
   `squasharr-pool-hevc-mkv-nvidia-b8e379254f` reads suspend=true, Active=0 and no
   startTime; delete it (background propagation) to free `nvidia.com/gpu`.
4. **Quiesce downloads and imports.** Confirm no Download is Queued, Downloading or
   Paused and `/data/torrents/.state` is absent; let any import in flight finish
   (importarr-worker logs).
5. **Stop every old writer.** Scale to 0: `clustarr-catalogarr`, `clustarr-importarr`,
   `clustarr-grabarr`, `clustarr-squasharr`, `clustarr-captionarr`,
   `clustarr-indexarr`, `clustarr-catalogarr-metadata`, `clustarr-importarr-worker`,
   `clustarr-captionarr-worker`, `clustarr-segmentarr-worker`. Wait until their pods
   are gone (longest grace 120 s) and all five leases show an empty `holderIdentity`
   or a `renewTime` more than 60 s old. The engines and `clustarr-ui` keep running (no
   controller re-renders the engines meanwhile; the ui is read-only). Queues buffer in
   JetStream; do not restart NATS (single-node work streams are memory-backed).
6. **Upgrade.** `helm upgrade clustarr <chart 0.5.0> -n clustarr-system -f <fresh values> --timeout 15m --wait`,
   never `--reuse-values` (add `--force-conflicts` if a kept object was edited with
   kubectl). The fresh values set `image.clustarr.tag` and `image.native.tag`
   explicitly, carry `ui.plex` (`externalURL http://clustarr-ui.clustarr-system.svc.cluster.local:8080`),
   `storage.data.existingClaim: clustarr-data`, the nats config, the live resources
   mapped by §10.2.8 (captionarr's 1536Mi to the manager; captionarrWorker's to
   `agents.caption`; segmentarrWorker's 6 CPU / 3Gi to `markers`), and
   `markers.consumerSlots: {segmentarr-analyze: 3}` (§13 OD34). Helm creates the
   manager, agents and markers, keeps the ui and its Service, and deletes the old,
   already-empty Deployments, Services, SAs and RBAC. The manager's `EnsureTopology`
   creates `CLUSTARR_WORK_PROBE`, `clustarr-probes`, `importarr-probe-high`,
   `importarr-probe-low` and `importarr-recycle`, and raises autoscaled durables'
   `MaxAckPending`; no existing durable is renamed, so no `nats consumer rm` is needed.
7. **Verify.** `manager.clustarr.io` is held by the manager pod and no old lease
   renews. `kubectl get apiservice v1beta1.external.metrics.k8s.io` is Available;
   `kubectl get hpa` shows five HPAs at maxReplicas 1;
   `kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/clustarr-system/clustarr_consumer_lag`
   answers. The engines are re-rendered (new engine-config-hash, argv `/usr/bin/agent
   --domain=…`, native image, pods Ready, DownloadClient `EngineReady=True`), and
   torrents keep `.torrent.db` (R13). Exactly one `agent-index` pod mounts
   `clustarr-index`, and the facade Secret is unchanged. The ui is ready and the Plex
   provider answers through `media/clusterplex-config`. MediaFiles stay `Probed=True`
   with no mass re-probe (ProbeVersion unchanged); markers re-decodes a season's
   windows only at its next task (FingerprintVersion 2). No pod runs an ffmpeg,
   ffprobe or par2 executable.
8. **Resume transcode** (owner's OK): unsuspend the TranscodeJobs; confirm one new
   pool Job appears (`/usr/bin/transcode`, component `transcode`, native image),
   measures its device and takes a task.
9. **Clean up.** Delete the stale `captionarr`, `catalogarr`, `grabarr`, `importarr`
   and `squasharr` `.clustarr.io` leases. One release later remove
   `--legacy-lease-check` and `k8s.GatedLeaseLock` (§13 OD6).

**Rollback.** Scale the manager, agents and markers to 0 and wait until
`manager.clustarr.io` lapses; delete the new pool Job; `helm rollback clustarr N`
(the old images are still loaded); remove the new streams and consumers only if they
interfere (they do not: nothing old consumes them); unsuspend the TranscodeJobs with
the owner's OK. The old binaries ensure their own topology on start and rewrite
`MaxAckPending` back to their per-pod values.

**cluster-plex** needs no change: it depends on the `clustarr-ui` Service and port
8080, the art signing key, Secret `plex-token` and the catalog CRDs, all unchanged.
Its `kind-clustarr` fixture stands in for the ui Service under the same name.

---

## 12. Docs and ADRs to write

ADR-0016 went to "per-file work is MediaFile status" (accepted on this branch,
2026-10-06), so the topology ADR, first drafted as 0016, is ADR-0018; ADR-0017 keeps
its number, since the plan's code cites it from Wave 0 on.

- **ADR-0018** `docs/adr/0018-manager-agents-and-ui.md` (Accepted when the
  owner accepts this spec): one manager with every reconciler and leader-only
  runnable under `manager.clustarr.io`; one agent Deployment per domain; ui alone;
  markers and transcode as native binaries; HPA autoscaling without KEDA. Its Context
  names ADR-0013 and the premise that changed (the owner chose to adopt now). In the
  same commit ADR-0013's Status becomes `Superseded by ADR-0018, <date>`, and both
  rows of `docs/adr/README.md` change.
- **ADR-0017** `docs/adr/0017-no-external-media-programs.md`: no image ships or runs
  ffmpeg, ffprobe or par2; probing, decode and subtitle extraction run on ffgo;
  par2 runs par2go in a re-exec'd child of the agent. Alternatives: keep the CLI
  execs; par2 in-process; `akalin/gopar` (too slow; reads everything into memory);
  cgo bindings; a separate `cmd/par2` binary. Consequences: purego in the agent;
  `.so` files in the native image; `make native-assets` needs cmake, git and a C++20
  compiler. It supersedes the par2-is-an-exec decision (`hack/deps/deps.go:43-49`,
  `repair.go:39-47`); `docs/research/download.md:272,318` gets a superseded note.
- **Refinements under the ADR index** (no supersession): ADR-0015, "the transcoder
  image is now the native image, which also carries the agent and markers"; ADR-0009,
  "pool Jobs run `/usr/bin/transcode` from the native image, component `transcode`";
  ADR-0010, "the index agent stays at one replica with Recreate under Postgres too,
  until a shared KV limiter exists"; ADR-0007 unchanged (the metadata domain).
- **`docs/superpowers/specs/2026-09-24-unified-manager-design.md`:** Status becomes
  "Superseded by `2026-10-06-manager-agent-split-design.md`".
- **par2go design** (`docs/superpowers/specs/2026-10-06-par2go-design.md` §8): each
  open question points at §8 here.
- **Design of record** (`2026-09-18-clustarr-design.md`) §3, §6 and §19 and
  amendment-1's services table: rewritten for the topology; §2's field-manager list
  gains `clustarr-autoscale` and lists the session and facade-key Secrets under
  `indexarr-worker`.
- **`docs/autoscaling.md`** (new) replaces the KEDA docs: the metric, the
  APIService ownership split, the reconciler, `CLUSTARR_CONSUMER_SLOTS`,
  `autoscale.clustarr.io/{domain,paused,min-replicas}`, `autoscaling.enabled`,
  coexistence with KEDA or prometheus-adapter (unsupported), failure modes (§9.8),
  the throttled consumers and why `catalog` and `caption` scale 0..1 (§9.1.1),
  and the manual recovery for a namespace stuck Terminating behind the APIService:
  `kubectl delete apiservice v1beta1.external.metrics.k8s.io`.
- **`docs/observability.md`:** `:81` rewritten; `:160` ("under `clustarr all`")
  rewritten for one process per domain; the readiness table (`:227-240`) for
  §3.3's per-process checks; `clustarr_work_queue_pending` now produced;
  `clustarr_probe_*`, `clustarr_ffgo_abandoned_calls` and
  `clustarr_par2_unreaped_children`.
- **Top-level `README.md`:** `:44` ("Seven services, one `clustarr` binary,
  invoked as `clustarr <service> --role <role>`"), the services table (`:48`
  on), `:104` (`cmd/clustarr/` as the single cobra binary) and the images line
  are rewritten for the five binaries, the eight domains and the two images,
  beside the KEDA lines `:109-111` of §10.2.7.
- **`docs/gpu-nodes.md`:** `:17` and `:87` ("the one `transcoder` image")
  name the `native` image.
- **`hack/deps/deps.go:43-49`:** "PAR2 adds no module but par2go: par2cmdline-turbo is
  C++, built into libpar2shim.so and loaded through purego by github.com/mediactl/par2go
  (ADR-0017)."
- **CLAUDE.md:**
  - Services table: manager, the agent domains, ui, markers, transcode, with the
    `app/<svc>/{manager,agent}` packages; `clustarr all` is gone; `make run-dev`.
  - Commands: `make build` (five binaries), `make native-assets`, `make chart-deps`,
    `make docker-selfcheck`, `make run-dev`.
  - Invariants: "the UI may hold a bus connection" names `pkg/busconn`; "kubernetes
    watches are the default coupling" unchanged; new: only the manager ensures
    topology (and keeps it after a NATS restart), `Subscribe` binds its durable
    and its dead-letter watcher; every container names its binary; one domain
    per agent process. The MediaFile split is restated per field manager (R3
    amendment): `importarr` owns `MediaFileSpec`; `catalogarr` (the manager's
    reconciler) owns every `MediaFileStatus` leaf except `status.markers`,
    which `catalogarr-markers` (agent `metadata`) owns through
    `segmenting.Applier`'s CAS; the reconciler never declares `markers`.
  - Guard names CLAUDE.md cites today and their successors:
    `TestClustarrNeverLinksADynamicLoader` → `TestManagerNeverLinksADynamicLoader`
    and the other deps guards (§4.5); `TestTheWorkerNeverExecsFFmpeg` and
    `TestTheTranscoderImageCarriesNoFFmpegExecutable` →
    `TestNoProductionCodeStartsAProcess` and `TestTheNativeImageCarriesNoMediaExecutable`;
    `TestTranscoderImagesAreWhatSquasharrStampsOntoPools` →
    `TestNativeImageIsWhatTheManagerStampsOntoPoolsAndEngines`;
    `TestGrabarrEnginesRunAsAnAccountTheInstallerBinds` →
    `TestEnginesRunAsAnAccountTheInstallerBinds`;
    `TestCaptionarrScaledObjectCountsTheRealFetchConsumers` →
    `TestEveryConsumerHasExactlyOneHome`; `TestWorkerGracePeriodsCoverAckWait`
    → `TestGracePeriodsCoverAckWait`; `TestEveryNATSDialingDeploymentCarriesNATSURL`
    → `TestEveryClustarrContainerCarriesNATSURL`; and `--worker-image` /
    `image.transcoder` → `--native-image` / `image.native` in the Transcoding
    paragraph.
  - Transcoding paragraph: probing is `pkg/mediainfo/native` in the import domain; a
    ProbeVersion raise re-probes through the low lane; there is no transcoder image,
    only `native`. Edit main's text as it stands at the rebase, not this design's
    draft of it: at `80175fdc` it already carries the MP4 standard (`dea6d010`,
    `adf9372c`), TranscodedFinal for every search with the `spec.grab` exemption
    and `ImportMessageExistingFileFinal` (`e21885cc`), and the ui's top bar, mass
    editor and card actions (`b77c30d2`, `0a39889a`).
  - Gotchas: the par2 gotchas (`:794-807` names `usenet.LibraryRepairer`,
    `par2Extras`, `TestLibraryRepairerRepairsASetWhoseFilesCarryOtherNames`; `:845-852`
    points at `par2child.Exec`: cooperative stdin cancel, WaitDelay kill, Setpgid);
    a new gotcha on par2's traps T1, T2, T3, T13 and the `PAR2GO_LIB` skip/fail rule;
    "three vendored dependency tarballs" becomes two; a new gotcha on the ffgo
    package-init dlopen and the shim API pin; the D2 status paragraph (`:1040`) says
    par2 is used as a library.
  - The status section: this restructure, as built, when it lands.
- **Chart README** (`charts/clustarr/README.md`): "Autoscaling" replaces
  "Autoscaling (KEDA)"; "Upgrading from 0.4.x" (§10.2.8); the Values table
  (`:358-412`, 38 rows naming retired keys such as `image.controller`,
  `image.media`, `indexarr.cardigann.*`, `squasharr.slots`,
  `captionarrWorker.ackWaitSeconds`) and the GOMEMLIMIT, Cardigann and Postgres
  sections are rewritten to §10.2.5's keys. `images/distroless/README.md` and
  `config/README.md` (§10.1.3, §10.2.6).

**Amended 2026-10-07 (NATS research).** The docs wave (plan W10.5, W10.7) also
writes:

- **`docs/autoscaling.md`**, "The metric": §9.0 as amended, with its numbers:
  why `CONSUMER.INFO` (answered by the consumer leader; 897 bytes, under a
  millisecond) and not `/jsz` (followers read `num_pending` 0; an unauthenticated
  port; HTTP 200 with no data on a wrong `acc`; 46-110 KB per server) or a
  stream's `messages` (18,897 against a lag of 16,563 on
  `CLUSTARR_WORK_SEGMENTARR`, 10,137 against 0 on `CLUSTARR_RELEASES`), and that
  no NATS-native metrics adapter exists. It does **not** repeat the deleted
  ScaledObject's claim about KEDA and kedacore/keda#8166. Failure modes take §9.8
  as amended.
- **`docs/observability.md`**, the metric catalogue gains:
  `clustarr_consumer_pending`, `clustarr_consumer_ack_pending`,
  `clustarr_consumer_waiting`, `clustarr_consumer_max_ack_pending` (§9.4 as
  amended); `clustarr_stream_fill_ratio{stream}` with an alert at 0.8;
  `clustarr_nats_async_errors_total{kind}`; `clustarr_bus_lapsed_handlers`,
  `clustarr_bus_saturated` and `clustarr_bus_muted_total{durable,op}` (§9.3 as
  amended); `clustarr_object_orphan_chunks_purged_total{bucket}`,
  `clustarr_object_orphan_bytes_purged_total{bucket}`,
  `clustarr_artwork_objects{variant,meta_version}` and
  `clustarr_artwork_audit_tasks_total{variant,reason}` (artwork design §B.5 as
  amended); and the `bus` liveness check in the readiness table (§3.3 as amended).
- **CLAUDE.md:**
  - The invariant "Artwork objects have two writers split by variant" ends:
    "…and of its metadata. Each `Put` sends the variant's complete header and
    metadata set (`artwork.ObjectMeta`), and `SetMeta` is the owner's alone,
    under its per-item lock. No code creates object links
    (`TestNoObjectLinks`). The ui watches the bucket read-only. The reaper is the
    only code that deletes both, and it also purges orphan chunks and audits
    status against the bucket."
  - The MAX_DELIVERIES gotcha gains its cause and loses its remedy: nats-server's
    `deliveryCount` returns redeliveries, so the ack-timer path catches a lapsed
    final delivery only for `MaxDeliver` 1 and for 2 or more the advisory waits
    for the next waiting pull from any replica (`consumer.go:4841-4849,
    2427-2457, 6187-6192`); a pull a stopping pod abandons is pruned by the next
    `CONSUMER.INFO` and is not relied on; and a subscription never fetches more
    than its free slots and never parks (M1), because each redelivery of a
    parked message spends an attempt and Wave 4c's parking dead-lettered healthy
    tasks unrun.
  - New gotcha, **a fired schedule has no `Nats-Msg-Id`** and carries
    `Nats-Scheduler` and `Nats-Schedule-Next: purge`; a copy published with them
    onto a stream without schedules is refused with 10188. The envelope ID rides
    in `Clustarr-Id` too, and `EnvelopeFromHeaders` drops every transport header
    (M2).
  - New gotcha, **the broker keys `InProgress`, `Ack` and `Nak` by stream
    sequence**, so a lapsed handler can extend or settle the copy another worker
    runs; the bus mutes `InProgress` and `Nak` after a lapse (S2), and double
    ack plus Msg-Id dedupe is not exactly-once: the record fence is.
  - New gotcha, **object stores:** `Put` replaces an object's whole metadata;
    two concurrent `Put`s of one name leak a full copy for good, which only an
    orphan-chunk purge reclaims, since the meta-level `List` cannot see it; a
    reader whose object is overwritten mid-`Get` stalls until its deadline and
    fails with `i/o timeout`, never a digest mismatch; a link fires no watch
    event when its target changes, and nats.go refuses a link over any name a
    regular object ever held; `ObjectStore.Watch` ignores `ResumeFromRevision`
    and `MetaOnly`; a bucket re-created under a running watcher is skipped
    silently, so `objindex` compares the bucket's creation time.
  - The autoscaling text says the HPA reads `CONSUMER.INFO`, never `/jsz` or a
    stream's message count, and why in one line.
- **ADR index** (`docs/adr/README.md`): a one-line refinement under ADR-0011,
  written with this amendment: "2026-10-07: no object links; objects carry
  versioned metadata; the ui indexes the bucket by watch; the reaper audits and
  purges orphan chunks." A partial change, not a supersession.
- **Not on this branch: cluster-plex.** Its watcher hashes `status.metadata`,
  `status.overlay`, `status.title`, `status.overview` and `status.airDate` to
  decide whether to refresh Plex (`cluster-plex/pkg/clustarrwatch/fields.go:46-53`,
  compared at `watcher.go:302`), but not `status.artwork`, so a custom override or
  a backfilled original changes the `?v=` URL the Plex provider answers with and
  Plex never asks again. The fix (`{"status","artwork"}` in `shown`,
  `TestMetadataHashCoversArtwork`) is a cross-repo item for the owner to schedule
  in cluster-plex (artwork design §B.8 as amended).

---
## 13. Owner decisions

Each has a recommendation; the spec is written to the recommendation. Duplicates
across the section drafts are merged.

### Topology, binaries and names

- **OD1. A combined-domain agent at all.** The owner removed `clustarr all`; the
  section drafts had brought it back as `agent --domain all` for development.
  **This spec drops it: one domain per agent process, everywhere** (R4
  amendment, §3.5.1), and `make run-dev` starts one agent per domain (§3.10).
  Keeping it would cost namespaced-check rules for combination, a field-index
  union with a collision guard, combination rules for engines, and the
  identical-`DisableFor` constraint, for a convenience a script gives as well.
  If the owner wants it back for development only: `--domain` accepts a list of
  non-engine domains and `all`, the agent registers the union of declared
  indexes once per `(GVK, name)` with a guard that two extractors never share a
  name, and `TestEveryAgentDeploymentRunsOneDomain` keeps it out of the
  installers. **Recommend dropping it.**
- **OD2. `events` as the event-stream domain's name** (Deployment and SA
  `agent-events`, ClusterRole `clustarr-agent-events-role`). **Recommend `events`.**
- **OD3. Merge `--engine-image` and `--worker-image` into `--native-image`**
  (`CLUSTARR_NATIVE_IMAGE`). **Recommend merge:** R1 makes them one image, and two
  flags carrying one value invite a chart that updates only one. The cost: engines and
  pools can no longer run different tags (for example native-debug for engines only).
- **OD4. Default user-agent field manager** pinned to `clustarr`
  (`k8s.DefaultFieldOwner` through `client.Options.FieldOwner`), rather than
  `manager`/`agent` from the binary names. **Recommend pin:** live managedFields stay
  continuous.
- **OD5. Tracing `service.name` and NATS client-name scheme** `manager`,
  `agent-<domain>`, `markers`, `transcode`, `ui`, rather than per-service names like
  `catalogarr`. **Recommend the new scheme:** no dashboard in the repo reads
  `service.name`, and e2e reads logs.
- **OD6. Legacy-lease gate lifetime** (`k8s.GatedLeaseLock` behind
  `--legacy-lease-check`). **Recommend** shipping it with the cutover release and
  deleting it, with its test, one release later.
- **OD7. Manager readiness without a `/data` check** (the import and caption agents
  keep `import.data` and `caption.data`). **Recommend exclude:** readiness never gated
  a leader's reconcilers, `/data` failures already surface per reconcile, and an
  NFS stall would only make the manager's metrics endpoint flap (the External
  Metrics API has its own Service, §9.4).
- **OD8. Scope: convert embedded-subtitle extraction too** (R2 says it is implied, not
  named). **Recommend convert:** the shared native image ships no ffmpeg, and keeping
  it would keep the media image for captionarr alone.

### Packages

- **OD9. SubtitleProvider capability table.** Read `NeedsSecrets/HIVerifiable` from a
  static table in `app/caption/providerset`, held to the clients by
  `TestCapabilityTableMatchesTheClients` in `providerset/build`. This reverses
  providerset.go:316-340's deliberate read-off-the-client design; the test keeps its
  guarantee. **Recommend the table.** If refused, the manager links the four remote
  subtitle clients plus rardecode (+6 packages, ≈ 0.4 MB).
- **OD10. MetadataProvider probing stays in the manager.** R6 permits clients the
  manager reconciles with, and `OnlyImportedBy` pins the route. Cost +34 packages,
  ≈ 3.0 MB. The alternative, an RPC probe through the gateway, makes `Ready` depend on
  the single-replica metadata domain. **Recommend keep.**
- **OD11. API types in `cmd/transcode`.** markers drops them (§7.2.9); transcode keeps
  `api/catalog` and `api/transcode` (`k8s.io/api/core/v1`, ≈ 5 MB stripped) for this
  restructure. Neither links a Kubernetes client, which is what the request asks.
  **Recommend keep for transcode;** decoupling needs schema-only copies of the types.
- **OD12. go-ffprobe as a types-only dependency** of `mediainfo.Raw`. After the move no
  code calls it, and the exec guard bans `Probe*`. **Recommend keep;** re-typing `Raw`
  is a separate cleanup.

### Writers

- **OD13. Fix the pre-existing two-process lost updates now:** Trakt
  `SecretTokenStore.applyAll` (`tokenstore.go:114-139`), `fileimport.applyMediaFile`
  (`fileimport/worker.go:406`), import-list `applySeries` vs `catalogarr-classify`
  (`catalogitem.go:221-231`). Each is a live re-read plus `WithResourceVersion` and
  `RetryOnConflict`, with an interleaved-second-writer test. **Recommend yes,** in the
  import-agent wave.
- **OD14. Field manager for the index agent's session-Secret Save/Drop and facade-key
  Create.** **Recommend reusing `indexarr-worker`** (no spec §2 name change;
  `indexarr` becomes manager-only) over a new `indexarr-session`.
- **OD15. The index agent at 1 replica with Recreate under Postgres too.** This
  restricts ADR-0010's multi-replica indexarr until a shared KV limiter exists,
  because two per-process limiters pace each indexer host independently (the
  2026-10-06 nzbgeek class). **Recommend yes.**

### Probe, decode and the forks

- **OD16. Probe cutover gate.** Run `TestNativeProbeMatchesFFprobeOnACorpus` read-only
  against the live library and a MediaFile dump before switching. If a diff remains
  that the fork cannot remove, choose between raising `mediainfo.ProbeVersion` to 4
  (re-probing all 11,958 files, and `renameTranscoded` renaming transcoded files whose
  names change; cluster-plex then re-seeds every file's streams into Plex) and accepting
  the diff. **Recommend no switch until zero diffs, and no ProbeVersion raise.**
- **OD17. The transcode worker plans its x265 keyint from `r_frame_rate`**, matching
  `status.plan`, instead of `avg_frame_rate` (today the plans differ and the worker
  logs it, `app/squash/worker/ffgo.go:104-114`). This changes the GOP length of future
  encodes of field-coded sources. **Recommend accept, with no `standard.Version` raise.**
- **OD18. Push the ffgo fork:** branch `clustarr/unify-media` and tag
  `v0.0.0-clustarr.13` to `github.com/mediactl/ffgo` (`.12` is main's, published
  2026-10-06), then switch go.mod from `../ffgo-unify` to the tag. **Recommend** once ffgo's `go test ./...`, clustarr's
  `parity`-tagged decode, probe and subtitle tests, and the library parity runs pass.
  Until then CI cannot build the branch.
- **OD19. Publish par2go** (its plan's Task 9: create `github.com/mediactl/par2go`,
  tag, release assets) and switch go.mod from `../par2go`. **Recommend** once
  par2go's suite and clustarr's par2 tests pass.
- **OD20. Raise `segments.AnalyzerVersion` from 3 to 4** after the ffgo decoder lands?
  A raise re-analyzes about 14k movie and episode files, invalidates `clustarr-segments`
  records and changes result Msg-Ids. **Recommend raising only if the library parity
  run shows more than 1% of compared files changing a result or moving a segment
  boundary by more than 1 s;** otherwise keep 3, since `FingerprintVersion` 2 already
  keeps CLI- and ffgo-decoded fingerprints apart within a season.

### par2

- **OD21. Run the par2 library in a child of the agent (recommended) or in the usenet
  engine process** (R2's wording). Evidence in R2's amendment and §8.4.2. If the owner
  insists on in-process: drop `par2child.Exec` and `ChildMain`; write a per-job
  "repairing" marker before the call so reattach fails a set that crashed the engine as
  writeError instead of re-running it; on grace expiry mark the engine unready so the
  kubelet restarts it; accept that a wedge or crash costs every transfer in the pod and
  that par2go's per-process mutex serialises all repairs.
- **OD22. Failure classes.** Today every non-success par2 exit is `missingArticles`
  (blocklists the release); this design keeps that, including a child that died
  without a result. **Recommend** making `ErrMemory` and a child death with no result
  (OOM, crash) local faults (`writeError`, no blocklist), since they carry no evidence
  against the release (`failureReason`'s own principle), and keeping
  `RepairNotPossible`, `ErrInsufficientCriticalData`, `ErrRepairFailed`, `ErrIO`,
  `ErrLogic` as release faults. One `case` in `par2Verdict` and one in
  `LibraryRepairer.Repair`.
- **OD23. Report the dropped `basepath = _basepath` (T1) upstream** to
  nzbgetcom/par2cmdline-turbo, a public action. **Recommend yes;** the workaround stays
  either way.
- **OD24. Surface repair progress** (phase, per mille, file) in the Download stage
  message or `clustarr-progress`. **Recommend not in this change;** par2go's
  `Options.Progress` already carries it. Main's `38db94e6` already writes the
  stage and its elapsed time into `status.message` (§8.1), which answers the
  stall it was meant to explain. Adding par2's per mille now needs §8.4.3's
  protocol to carry progress frames from the child, not only a final result.
- **OD25. NZBGet-style ParQuick** (skip re-reading files whose articles all verified).
  **Recommend a later, separate change** (it needs slice-CRC equivalence and a par2go
  hook).
- **OD26. How clustarr's tests create PAR2 sets.** par2go has no creator by design.
  **Recommend** the par2 CLI installed by `make native-assets` for tests only
  (`test/par2test.CreateSet`), as par2go's own fixtures are made, over asking par2go
  to enable `ENABLE_CREATOR`.

### Autoscaling

- **OD27. `clustarr_consumer_lag` counts delayed naks** (NumAckPending), so a domain
  stays at 1 or more during a backoff and may over-provision beyond 1 on multi-node
  clusters; ConsumerInfo has no delayed count (nats-server consumer.go:56-74).
  **Recommend accept now.** Later option: natsbus turns a nak longer than AckWait on a
  WorkQueue stream into an ack plus a `WithScheduleAt` republish carrying the attempt
  in a header.
- **OD28. Who creates `v1beta1.external.metrics.k8s.io`.** **Recommend** the installers
  render it without `caBundle` and the manager SSA-injects only `spec.caBundle` under
  `clustarr-autoscale` (R5 amendment). The alternative: the manager creates it with an
  ownerReference to its cluster-scoped ClusterRole (an extra `get` on that ClusterRole).
- **OD29. Move `catalogarr-redownload` to `events`.** **Recommend yes** (R4 amendment).
- **OD30. `events`: an HPA with minReplicas 1** (max = matching nodes) **or a fixed
  single replica.** **Recommend the HPA;** it costs nothing at rest.
- **OD31. `events.AutoscaleReplicaCeiling = 8`** (KEDA's former max), so autoscaled
  consumers get `MaxAckPending = Slots × 8` and maxReplicas is clamped to
  `floor(MaxAckPending/slots)`. **Recommend 8;** raising it later is a topology change.
- **OD32. `autoscaling.enabled` defaults to true**, and coexistence with KEDA or a
  prometheus-adapter serving external metrics is documented as unsupported (one
  APIService per group). **Recommend yes.**
- **OD33. Agent placement:** a soft `topologySpreadConstraint` on hostname
  (`ScheduleAnyway`) rather than a required podAntiAffinity, which would stall
  RollingUpdate surges on a single node. **Recommend the soft spread.**
- **OD34. Per-pod slot tuning through `CLUSTARR_CONSUMER_SLOTS`**, read by the agent,
  markers and the reconciler from the same pod template, replacing markers'
  `--concurrency`; the live value 3 is carried into
  `markers.consumerSlots: {segmentarr-analyze: 3}` at cutover. **Recommend yes.**
- **OD35. Drain and grace sized to AckWait.** `--drain-timeout` defaults to the longest
  AckWait of the process's durables, graceful shutdown to drain + 15 s, and the grace
  period to graceful + 10 s (catalog 145, caption 115, events 55, others 85,
  markers 1825). AckWait here is the declared handler budget, not the broker's
  deadline, which is `BackOff[0]` for every one of these consumers (§3.5.6). A
  markers scale-down or rollout can then wait up to about 30 minutes for an old pod
  (the new pod starts without waiting). This replaces the autoscale draft's fixed 45 s
  drain, under which markers' longer analyses would be naked and redelivered, and the
  installers draft's grace = AckWait. **Recommend the AckWait-sized rule.**
- **OD36. The recycle sweep becomes a manager-published task** on `importarr-recycle`,
  so `import` can reach zero (the alternative is `import` at minReplicas 1), **and its
  cadence:** today's hourly (`DefaultRecycleSweepInterval`) or every 6 hours.
  **Recommend the task, every 6 hours:** retention is in whole days, so a file outlives
  it by at most 6 hours, and `import` wakes 4 times a day instead of 24.

- **OD46. Throttled consumers keep today's `MaxAckPending`**
  (`catalogarr-search-high` 8, `catalogarr-search-normal` 8, `catalogarr-grab`
  16, `captionarr-fetch-high` 16, `captionarr-fetch-normal` 16), so the
  `catalog` and `caption` HPAs scale 0..1 rather than to the node count
  (§9.1.1). Raising them to `Slots × 8` would add replicas that indexarr's
  per-host limiter and the shared provider token bucket cannot feed, and for
  search would turn a wanted sweep's queue into `paced` skips. **Recommend
  yes,** together with not recording an all-paced search as an attempt. Revisit
  when indexarr gains a shared KV limiter (OD15), which would also let the index
  domain scale.
- **OD47. Probe retry cadence.** Today a failed probe is retried every 30 s
  forever (`mediafile_controller.go:297`) and `ProbeAudio` has no timeout. This
  spec retries a transient failure after 5 m and any other after 1 h, gives up
  after three abandoned probes of the same bytes (§6.5.3, §6.6), and bounds
  `ProbeAudio` at 15 s. A file that cannot be probed then reads ProbeFailed for
  up to an hour after it is fixed, unless its bytes (and so its hash) change.
  **Recommend yes:** 30 s forever was cheap only while ffprobe was a killable
  process.
- **OD48. Invalidate the fingerprint cache now** (`segments.FingerprintVersion = 2`,
  §7.2.7). Every season re-decodes its windows once, at its next segment task,
  and the old objects linger up to the store's 90-day `MaxAge`. The alternative
  keeps the unversioned keys and mixes CLI- and ffgo-decoded fingerprints inside
  one season's alignment, a difference nothing would report. **Recommend
  versioning now.**

### Installers

- **OD37. Retired values keys are rejected** by a top-level `additionalProperties: false`
  schema, so upgrading needs a fresh `-f` values file and `--reuse-values` stops
  working. **Recommend yes** (the ADR-0015 precedent; accepting old keys would keep
  stale blocks alive silently).
- **OD38. Published images:** `ghcr.io/mediactl/clustarr` (manager and ui) and new
  `ghcr.io/mediactl/clustarr/native` and `/native-debug`; `/media`, `/transcoder` and
  `/transcoder-debug` stop. **Recommend yes:** reusing "transcoder" for an image that
  runs the agent and markers would mislead.
- **OD39. The `<fullname>-index` PVC gains `helm.sh/resource-policy: keep`.**
  **Recommend yes:** the live PV reclaims with Delete, and losing it loses every
  RSS-collected release.
- **OD40. Chart 0.5.0**, a breaking minor bump while pre-1.0. **Recommend yes.**
- **OD41. `config/e2e` pins autoscaled agents at a minimum of 1** through
  `autoscale.clustarr.io/min-replicas: "1"`, so the trace, log and metrics scenarios
  always find a pod. **Recommend yes;** the scale-from-zero scenario is separate (§9.9).
- **OD42. `GOMEMLIMIT` shares:** 60% for `agent-import` and `agent-caption`, 50% for
  `markers`, 80% elsewhere, because FFmpeg, par2 and ONNX allocate outside the Go heap.
  **Recommend yes.**
- **OD43. One native image carries the Intel VAAPI/QSV runtime for every consumer**
  (agent and markers included), not a transcode-only variant. **Recommend one image**
  (ADR-0015's rationale); the cost is tens of MB in every amd64 agent pull.
- **OD44. `CLUSTARR_REQUIRE_TOOLS=1`** (and `PAR2GO_REQUIRE=1`) in `make test`,
  `make test-race` and CI, with `make native-assets` and `make chart-deps` as
  prerequisites, so missing FFmpeg libraries, libffshim, libpar2shim, ONNX Runtime, helm,
  kustomize or `KUBEBUILDER_ASSETS` fail instead of skipping. **Recommend yes;** it
  costs a one-time download and build per machine.
- **OD45. Kustomize keeps full parity with the chart, autoscaling included** (the
  APIService and the kube-system RoleBinding, through a NamespaceTransformer with
  `unsetOnly`). **Recommend yes:** the parity guard enforces it, and e2e deploys
  kustomize.

---

## 14. Risks

**Topology and cutover**

- **Two writers during `helm upgrade`.** Helm creates the new objects before deleting
  the old, under a different lease. Without §11.3 step 5 the wanted cron fires twice,
  slot admission over-admits, the engines' templates flap, indexarr's lease-less
  controllers and SQLite run twice, and two metadata gateways double the outbound
  rate. `k8s.GatedLeaseLock` covers the leased controllers only.
- **Index data loss.** `clustarr-index` is helm-managed with no keep annotation and a
  Delete reclaim policy; any chart change that stops rendering it deletes the release
  index (§11.3 step 2, §13 OD39).
- **Transcoding is already stalled** by an orphaned Running TranscodeJob; any new pool
  template waits on that drain. A renamed pool name or label would instead leave the
  old Job running, holding the only NVIDIA GPU with the old image (names are kept, §3.8).
- **Values drift from other sessions.** A `helm upgrade --reuse-values` or a stale
  `-f` file from another session can roll back tags, fail the schema, or resurrect the
  old topology mid-cutover (§11.3 step 0).
- **Image tags.** A new image key left unset defaults to 0.1.0, which no locally built
  image carries (ErrImageNeverPull). `kind load` during the window stalls etcd and can
  cost every lease.
- **Engine roll.** Every engine template changes once (image, command, args,
  `POD_NAME`), so both engines restart at cutover; any later native-image bump rolls
  them again. Keeping R13's cgo build avoids a torrent rehash; a cgo-free agent would
  switch to `.torrent.bolt.db` and rehash every resumed torrent (none tracked today).
- **Manager blast radius.** One crash-looping reconciler restarts every reconciler;
  workers keep draining. The manager also serves the External Metrics API, so while
  it is down HPAs hold their scale, agents at zero stay at zero, and namespace deletion
  anywhere in the cluster stalls (§9.8).
- **Manager memory.** One cache holds 15.6k Episodes and 13.7k MediaFiles with
  `status.mediaInfo` (≈ 100 MB of JSON, 130-200 MiB of heap estimated); the 1536Mi
  limit is sized from that estimate, not a measurement.
- **Scale-to-zero latency.** Waking a domain takes about 30-60 s (HPA sync, pod start,
  cache sync); `events` is pinned at 1 because its Limits streams would drop data
  meanwhile.
- **One topology keeper.** After the split only the manager creates streams and
  consumers. A NATS restart on a single node wipes every memory-backed stream;
  `busconn.KeepTopology` re-creates them within about 60 s, but while the
  manager is itself down nothing does, agents that restart then crash-loop in
  `AwaitTopology`, and work queued in memory streams is lost, as today.
- **More handlers while handlers lapse.** The slot-gated pull lets a pod run up
  to `2 × slots` handlers while some are past the broker's deadline (§9.3);
  today it is `slots`. A handler that is slow on purpose should heartbeat with
  `InProgress`.
- **One APIService per group.** Any cluster already running KEDA or prometheus-adapter
  for external metrics must set `autoscaling.enabled=false`.

**Native code**

- **C heap outside `GOMEMLIMIT`.** FFmpeg decoder pools, filter frames, ORT arenas and
  par2 repair buffers count against the pod but not against Go's limit; an OOM in
  markers or the import/caption agents kills every in-flight task (they redeliver).
  The par2 child takes the OOM itself (`oom_score_adj` 1000), but concurrent children
  in one pod each size their buffers at 1/8 of the pod's limit, and main's coming
  usenet connection budget bounds transfers, not repairs (§8.4.5).
- **Package-init dlopen.** Every binary that links ffgo maps FFmpeg before `main`;
  agent domains that never decode still pay the mapped pages.
- **A stale shim reads wrong offsets.** Mitigated by `FFSHIM_API_VERSION` and
  `ffruntime.MinShimAPI` (F12); a shim built from the wrong ffgo commit fails `Load`.
- **Unreplicated ffmpeg behaviour** in decode: autorotate, container cropping, MPEG-TS
  pts wrap, discontinuity correction and the unreliable-timestamp duration rule.
  The library's MKV and MP4 files carry none; its 9 `.ts` files may decode
  differently at a wrap or at end of file, which the parity run counts (§7.2.6).
- **par2:** par2go's cancel flag is formally racy (T12; benign, TSan would flag it); a
  repaired set prints its verdict lines twice (verify and repair passes; `Log` only);
  the child pays the agent binary's package `init`s on every start (unmeasured); if the
  par2cmdline-turbo tag disappeared, image and test builds stop (the tarball SHA-256 is
  recorded for a mirror; no mirror is designed here); no e2e scenario repairs a set
  (`test/e2e/download_test.go:135-137`; Phase H should add an nntpstub post with
  recovery volumes and one refused article, built with the par2 CLI in the harness).
- **par2go is new code** (19 commits at `1bd94eb`); its suite and the differential test against the
  CLI are the evidence that its counters mean what par2's text says.

**Build and CI**

- **CI is red while go.mod carries `../ffgo-unify` and `../par2go`.** Nothing can merge until
  the owner OKs both pushes (§13 OD18, OD19).
- **Image totals grow.** Each binary carries its own client-go, k8s API, otel and grpc:
  the clustarr image ≈ 108 MB of Go against 94 MB today; transcode pool pods pull
  ≈ 105 MB more Go code than today's worker (§4.7).
- **Test prerequisites.** `make test` now needs cmake, git, a C++20 compiler and a
  one-time download of FFmpeg, ORT and the par2 CLI (§13 OD44).
- **The rebase.** Main moved during design (par2go, purego, wrong-language); other
  sessions keep committing. Re-run §4.1's chains and the line references after the R14
  rebase. The 2026-10-07 reconciliation with `80175fdc` found main had taken this
  design's fork tag (`.12`), added CLI-running tests the plan's U2 tasks must convert,
  and added encoders and muxers the transcode class must `Require`; the usenet
  connection budget will change `pkg/download/usenet` and `BuildConfig` under Wave 9.

---

## Appendix A. Contradictions between the section drafts, and how they were resolved

| # | Topic | Drafts disagreed | Resolution | Why |
|---|---|---|---|---|
| A1 | Probe queue names | binaries: durable `importarr-probe` and a leader-only consumer `catalogarr-probe-results`; autoscale: `importarr-probe` 8/64; writers: bucket `clustarr-probe-results`, waker via `source.Channel`; probe: stream `CLUSTARR_WORK_PROBE`, durables `importarr-probe-high`/`-low`, bucket `clustarr-probes`, KV-watch `probeRecordsSource` via `WatchesRawSource` | the probe section's names everywhere; no `catalogarr-probe-results` durable | the probe section owns R3 and designed the record protocol; a KV watch needs no durable |
| A2 | Probe slots | probe: MaxAckPending 32 as "16 pods × 2", flags `--probe-slots-high/low`; autoscale: one consumer at 8/64 | two lanes at `Slots: 4`, MaxAckPending 32 each; no `--probe-slots-*` flags | autoscale's `Slots × 8` rule and env override; 4 + 4 keeps today's 8 concurrent probes |
| A3 | Recycle sweep | binaries: one manager-published task every 6 h on `clustarr.work.importarr.recycle.sweep`, MaxDeliver 3, BackOff [5m,30m], MAP 1; autoscale: hourly per-RootFolder tasks from the RootFolder schedule controller, MaxDeliver 4, BackOff 30s/2m/10m, Slots 1/MAP 8 | binaries' single task (`recyclesweep.Scheduler`) and retry spec, with autoscale's Slots 1 / MAP 8; cadence left to OD36 | `SweepOnce` dedupes shared bins with the longest retention, which per-RootFolder tasks would break; MAP must follow the ceiling rule |
| A4 | `catalogarr-redownload` | packages and installers RBAC put it in `catalog`; binaries and autoscale moved it to `events` | `events` (R4 amended) | it consumes a Limits stream |
| A5 | One or many domains per process | packages and writers: exactly one; binaries: several non-engine domains for development | exactly one, everywhere (review: a combined-domain agent would bring back the `clustarr all` the owner removed; §13 OD1) | removes the field-index union, the combination rules and their guard; development runs one process per domain |
| A6 | Readiness check names | writers: keep `data`, `releaseindex`, `reattach`; binaries: `import.data`, `index.releaseindex`, `torrent-engine.reattach` | namespaced (binaries) | nothing reads per-check names, and a domain-prefixed name says which registration owns a check |
| A7 | Tracing `service.name` | binaries: `clustarr-manager`, `clustarr-agent-<domain>`; writers: `manager`, `agent-<domain>` | writers' unprefixed names | today's services are unprefixed (`importarr`, `squasharr-worker`); NATS names already carry `clustarr-` |
| A8 | Legacy lease protection | binaries: check once before `mgr.Start` and exit 1; writers: `k8s.GatedLeaseLock` | `GatedLeaseLock`, switched by binaries' `--legacy-lease-check` | no crash-loop backoff; the gate opens the moment the last legacy lease lapses |
| A9 | Manager options home | packages: `cmd/manager/cache.go`; writers: new `app/manager.ManagerOptions`; binaries: inline in `internal/cli/manager.Run` | `internal/cli/manager/options.go` `managerOptions` | one place, importable by `TestManagerCacheOptions` |
| A10 | CLI plumbing | packages: `internal/cli/k8sflags`, flags in `cmd/<bin>/flags.go`, `cmd/agent/domains.go`; binaries: `internal/cli/ctrlflags` and `internal/cli/<bin>` trees | binaries' layout; tests stay in `cmd/<bin>` | guards need importable command trees; R10 keeps tests with their cmd |
| A11 | Bus connector package | packages: `pkg/busconn`; binaries: `pkg/k8s/bus` | `pkg/busconn` | the packages section's simulated closures and guards use it |
| A12 | `Subscribe` and consumer config | autoscale: Subscribe writes the topology's `MaxAckPending`; writers: Subscribe binds and never creates; binaries: agents `AwaitTopology` and exit after 45 s | Subscribe binds (writers) and waits for a durable that disappears later; `AwaitTopology` (binaries) bounds start-up; `Pull` writes the cap (autoscale) | only the manager writes static consumer config, so an old agent cannot revert it |
| A13 | Autoscale label and annotation keys | autoscale: `autoscale.clustarr.io/{domain,paused}`; installers: `autoscaling.clustarr.io/{domain,min-replicas}` | `autoscale.clustarr.io/{domain,paused,min-replicas}` | the reconciler owner's prefix; installers' min-replicas annotation added to the reconciler |
| A14 | Autoscaler field manager | autoscale: `k8s.ManagerAutoscale` = `clustarr-autoscale`; writers: `ManagerAutoscaler` = `clustarr-autoscaler` | `clustarr-autoscale` | the R5 owner's name |
| A15 | Autoscale packages | autoscale: `pkg/autoscale` (table + pure functions), `app/autoscale/extmetrics`; packages: `pkg/agentdomain`, `app/autoscale/metricsapi` | `pkg/agentdomain` (the table, also read by the agent), `app/autoscale/controller` (pure functions, component-helpers), `app/autoscale/extmetrics` | the table is broader than autoscaling; component-helpers stays manager-only |
| A16 | External-metrics port and Secret defaults | binaries and installers `:6443`; autoscale `:9443`; binaries Secret default `manager-external-metrics`; autoscale/installers `external-metrics-tls` | `:6443`, container port `extmetrics`, Service port `external-metrics` 443; Secret `external-metrics-tls` (chart `<fullname>-external-metrics-tls`) | two of three drafts; the kustomize name |
| A17 | Index agent replicas, strategy, election | installers: `agents.index.replicas` (1 unless Postgres), RollingUpdate under Postgres, a leader-election binding for its sweeper lease; writers and binaries: agents never elect, sweeper `EveryReplica`, Recreate under both engines | fixed 1, Recreate, no election, no binding, no replicas key | writers' objection (two limiters per host); agents never elect |
| A18 | Agent `--nats-single-node` | installers' args passed it to every agent; binaries retired it from agents | manager only | agents never ensure topology |
| A19 | Image env for engines and pools | installers: `CLUSTARR_ENGINE_IMAGE` and `CLUSTARR_WORKER_IMAGE`; binaries: one `CLUSTARR_NATIVE_IMAGE` | `CLUSTARR_NATIVE_IMAGE` (OD3) | R1 makes them one image |
| A20 | Grace and drain | binaries: graceful = max(60 s, AckWait), grace + 10 s; autoscale: 45 s drain, grace ≥ 60; installers: grace = AckWait (markers 1800) | drain = longest AckWait, graceful = drain + 15 s, grace = graceful + 10 s (OD35) | lets running handlers finish everywhere, markers included |
| A21 | `GOMEMLIMIT` for markers | decode: 50%; installers: 60% | markers 50%, import and caption agents 60% | decode's measured C-heap reasoning (ORT arenas) |
| A22 | Slot values knob | installers: no `concurrency` key, "a knob could only disagree"; autoscale: `consumerSlots` rendering `CLUSTARR_CONSUMER_SLOTS` | `consumerSlots` in `agentBlock` and `markers` | the reconciler reads the same pod-template env, so it cannot disagree; the live markers value 3 needs a home |
| A23 | ffgo fork branch and API shapes | probe: branch `clustarr/probe-fields`, F-P1..F-P8 (typed `FieldOrder`, `RealFrameRate`, `ChannelOrder`, chapter TS, `WithInterrupt`); decode: branch `clustarr/unify-media`, F1..F14 with F11 as int fields and `RFrameRate`, "decided against `AVIOInterruptCB`" | one branch `clustarr/unify-media`; one table F1..F16; probe fields as F11a-F11f in the probe section's shapes plus decode's `VideoDelay`, `StartTime`, `BitsPerCodedSample`; `WithInterrupt` added as F16 for the probe only | one tag (`.12`, since `.11` is published); the probe's parity test pins its shapes; decode's objection holds for decode, not for the probe's 45 s bound |
| A24 | Probe abandonment and health | probe: `native.Options{MaxAbandoned}`, gauge `clustarr_probe_abandoned`, healthz `native-probe`; decode: `ffruntime.Do`, `clustarr_ffgo_abandoned_calls`, healthz `ffgo` | `ffruntime` for both; the native probe calls `ffruntime.Load` and `Do`; one process-level liveness check `ffgo` | one gate per process, one accounting of stuck FFmpeg calls |
| A25 | ffprobe oracle location | packages: transitional production `pkg/mediainfo/ffprobeexec`, then deleted; probe: `pkg/mediainfo/ffprobeoracle` (test-only, non-test files); exec guard: empty final allow-list over `pkg/` | `ffprobeexec` in Wave 1, moved to `test/ffprobeoracle` when native ships | non-test files under `pkg/` importing `os/exec` would fail the repo-wide exec guard; `test/` is outside its scope |
| A26 | Exec guard name and final allow-list | packages: `TestNoProductionCodeExecsAProgram`, `app/squash/exec_guard_test.go` kept; installers: `TestNoProductionCodeStartsAProcess`, the old guard deleted, empty allow-list (a par2 child "if chosen" allowlisted); probe: `TestNothingRunsFFprobe`; decode: widen `TestTheWorkerNeverExecsFFmpeg` | one guard `TestNoProductionCodeStartsAProcess`, transitional allow-list, final allow-list exactly `pkg/par2child/exec.go` | one repo-wide guard subsumes the others |
| A27 | par2 library | par2 draft: in-tree `native/par2shim` (`par2shim_*` ABI), `pkg/par2`, `PAR2SHIM_DIR`, creator on, `make par2-assets`, trixie build; installers: `pkg/par2/shim/` built from a git clone in a bookworm stage | par2go (`github.com/mediactl/par2go`, `libpar2shim.so`, `PAR2GO_LIB`, no creator), clustarr's `pkg/par2child` for the child protocol, `make native-assets`, bookworm build | par2go is the owner-approved design on main (98d852ce, 599b288d) and already implemented locally; two libraries for one job would diverge |
| A28 | par2 step ordering | par2 draft: dispatch heads `cmd/clustarr/main.go` while the engine lives there | par2, the native probe and embedded extraction land after `cmd/clustarr` is deleted | `cmd/clustarr` must never link purego (`TestClustarrNeverLinksADynamicLoader`), as par2go's own §1 notes |
| A29 | History package split | packages: replay stays in `app/catalog/history`, sink and DLQ to `app/catalog/worker/history`; writers and installers: replay to `app/catalog/history/replay` | three ways: shared `app/catalog/history` (no markers), `app/catalog/history/replay` (manager), `app/catalog/worker/history` (events) | each RBAC marker lives in the one side that needs it |
| A30 | Other package names | metadata refresher: packages `app/catalog/controller/metadatarefresh` vs installers `app/catalog/metadata/refresh`; facade key: packages and writers `app/indexer/agent` vs installers `app/indexer/facade`; indexer clients: packages `clients` + `clientcache` vs writers `wireclient`; artwork reaper: packages `app/catalog/artwork` vs writers `app/catalog/artworkreaper`; binary paths: binaries `downloadclient.AgentBinary`/`pool.TranscodeCommand` vs installers `pkg/binpath` | `metadatarefresh`, `app/indexer/agent`, `clients` + `clientcache`, `app/catalog/artwork`, `pkg/binpath` | the majority or the section owning the layout; `binpath` is the one constant set guards can read |
| A31 | markers and API types | packages: keep `api/*` types in markers (OD); decode: drop them via local constants | markers drops them; transcode keeps them (OD11) | decode owns markers and did the work |
| A32 | RBAC verbs for the autoscaler | installers: hpa update, apiservices list/watch; autoscale: hpa get/list/watch/create/patch/delete, apiservices get/patch by name | autoscale's set | the owner of the reconciler; least privilege |
| A33 | `clustarr_work_queue_pending` labels | binaries: `{consumer}`; autoscale: `{stream,consumer}` | `{stream,consumer}` | the declaration at `pkg/obs/metrics/domain.go:247-255` |
| A34 | Leader-election bindings | installers: manager and agent-index; writers: manager only | manager only | agents never elect |
| A35 | ffgo fork tag | every draft: `v0.0.0-clustarr.11`; go.mod at `.9` | `v0.0.0-clustarr.12`, from master `9f7a3a4`; **since 2026-10-07 `.13`, from `.12` (`a184557`)** | another session published `.11` at `9f7a3a4` (the purego bump) today, and main's go.mod pins it (`b3b077a2`); main then published `.12` for the MP4 standard (`ebbb2322`) |
| A36 | purego bump | decode: this branch must raise purego from v0.9.1 to v0.11.1 | already done on main; only needed if the replace lands before the rebase | `b3b077a2` on main |

---

## Appendix B. Review notes (first review, 2026-10-06)

Every critical and important finding was checked against the code and fixed in
the body; the table records where. Minor findings are listed with what was done.
"Verified" names the evidence re-read for this revision.

**Important findings**

| # | Finding | Verified | Disposition |
|---|---|---|---|
| B1 | `Subscribe` creates the per-durable dead-letter watchers, which a bind-only `Subscribe` and `AwaitTopology` ignored (invariants and completeness lenses) | natsbus.go:319-331, deadletter.go:44-127, pull.go:53-57, admin.go:51, topology.go:630-649 | Fixed: watchers are topology objects (`events.DeadLetterWatcherSpec`), created by the manager, bound by `Subscribe`, checked by `Missing`; `Pull` keeps creating its own; the manager runs every static watcher as a backstop; new contract cases (§5.9, §9.3). **Partly disputed:** the completeness lens said a lapsed final delivery in a domain at zero is never dead-lettered. A lapsed delivery stays in the consumer's pending set until the server raises the advisory on its next pull (nats-server consumer.go:4976-4993), so `NumAckPending` holds the domain at 1 or more and its own watcher handles it; only an advisory raised by a pull a draining pod left behind waits. The manager backstop covers that case |
| B2 | `judgeProbe` never re-requests a matching record below the wanted version, so a ProbeVersion raise skips every file seeded or probed in the last 7 days, and skew leaves new files unready for days | probe.go:56-58; spec §6.5.2-6.5.3 | Fixed: `ProbeRecord.RequestedVersion`; rows split into pre-raise (request at once), skew (incorporate an unincorporated file with the old version, else wait `probeSkewRetry`), skew expired (request); new tests (§6.5) |
| B3 | `TestEveryFieldManagerHasItsHome` used package-granular `go list -deps` over one package holding three domains | §4.2.1 as drafted | Fixed: one registration package per domain (`app/catalog/agent/{catalog,events,metadata}`, `app/grab/agent/{torrent,usenet}`); `grabarr-engine` allowlisted; non-constant manager arguments attributed through their constructor sites; `linkedNotWritten` for over-approximation (§4.2.1, §5.15) |
| B4 | The transcode deps guard denied all of controller-runtime, which `api/catalog` and `api/transcode` link through `pkg/scheme` | groupversion_info.go:34 / :29; `go list -deps ./cmd/squasharr-worker` lists `controller-runtime/pkg/scheme`; main_test.go:140-157 | Fixed: `Rule.Except`; the deny list spelled out; the old test name recorded; one home in `test/guards` (§4.5) |
| B5 | `TestOnlyTheManagerRegistersControllers` would fail on the agent's own links to `delayprofile`, `search` and `series` | delayprofile/controller.go:111, search/reconciler.go:175, series/reconciler.go:164; rssmatcher/resolve.go:293, search/worker.go:500,806,836, markers/handler.go:242 | Fixed: Wave 2 step C9 moves `Resolve`, the outcome names and `EffectiveEpisodeOrder` to `app/catalog/{delay,searchoutcome,episodeorder}`; the guard and §4.4 restated |
| B6 | The par2 image stage reused par2go's host CMake cache and copied 251 MB of build output | par2go `shim/build.sh:21-26`, `shim/build/` 251 MB, no `.dockerignore`; `00f0fd2` | Fixed: contexts are `git archive` exports of pinned refs, and the stage deletes `shim/build` and sets `CMAKE_BUILD_DIR` (§7.6, §8.7) |
| B7 | par2go renamed its library to `libpar2shim.so` | par2go `ebed9f6`; `internal/bindings/bindings.go:87`; release.yml:36 | Fixed throughout; par2go basis recorded as `1bd94eb`, ABI 2 re-checked against §8.3.1 and §8.4.2 (Basis, §8) |
| B8 | `make test` never pointed ffgo at the pinned FFmpeg (two lenses) | ffgo `internal/bindings/bindings.go:117-128,207-224`, `internal/shim/shim.go:875-887`, `avutil/avutil.go:91` | Fixed: `LD_LIBRARY_PATH` and `CLUSTARR_NATIVE_ASSETS` in test, test-race, CI and dev-run; `ffruntime.Report` paths checked by `nativetest.Require`; parity gates pinned (§7.4, §10.1.6, §11.2) |
| B9 | Audio decode dropped the decoder's `DOWNMIX_INFO`, which `ffmpeg(1)` forwards to `aresample` | FFmpeg n9.0.2 `fftools/ffmpeg_filter.c:2255-2289,1877-1878,1990-1991`; ffgo `filter_graph.go:361-399`, `shim/ffshim.c:772-780` | Fixed: fork addition F17 (`FilterGraphConfig.InputSideData`, `ffshim_buffersrc_set_side_data`), reinit on a downmix change, mixlev parity fixture (§7.2, §7.5). The reviewer's live count of surround AC-3/E-AC-3 lead tracks (6,239 of 11,958) was not re-read: this revision touched no cluster |
| B10 | Lapse reclaim at `BackOff[0]` let slow handlers multiply per-pod concurrency up to `MaxAckPending` | nats-server consumer.go:678-682; topology.go:651-700 | Fixed: the lapsed cap (`2 × slots` handlers per pod), contract case `LapsedHandlersDoNotMultiplyConcurrency`, corrected claims (§9.3, §9.6, §14) |
| B11 | Raising search and grab `MaxAckPending` 8× would scale catalog past what indexarr can feed and cost items their live search through `paced` skips | fanout.go:55,351-366,405-410; worker.go:340-362; wantedscan.go:155-156; caption fetch/search.go:260 | Fixed: `pkg/agentdomain.Throttled()` keeps today's cap for search, grab and caption fetch (their HPAs scale 0..1); an all-paced search is not recorded as an attempt; OD46 (§9.1.1) |
| B12 | After a NATS restart nothing re-creates memory-backed topology, because only the manager ensures, once | topology.go:249-283; pkg/k8s/bus.go:123-161 | Fixed: `busconn.KeepTopology` (leader-only: at lease, on reconnect, every 60 s while anything is missing); subscriptions re-bind; failure-mode row (§5.9, §9.8) |
| B13 | `CLUSTARR_WORK_PROBE` (WorkQueue, DiscardNew, no schedules) fails `Topology.Validate`, so the manager cannot start | topology.go:384-389; topology_nats.go:37-39 | Fixed: Validate exempts any WorkQueue stream with `DiscardNew` (§9.2), and the test row follows (§6.8) |
| B14 | Deleting the release namespace leaves the APIService and deadlocks namespace deletion | reasoning as in the R5 amendment; the k8s line references are the reviewer's | Fixed: `extmetrics.NamespaceGuard`, RBAC, the manual recovery in `docs/autoscaling.md`, §9.8's wording (§9.4) |
| B15 | 60 tests in the six `app/<svc>` root packages had no destination | listed with `grep '^func Test'` over the 16 files | Fixed: §10.3.5 maps every one, retiring or inverting the role, lease and MediaInfo-strip tests with reasons |
| B16 | The manager's non-leader start envtest could not go Ready under `--autoscale=true` | §9.4 as drafted; `start_rbac_envtest_test.go:85-168` | Fixed: no `external-metrics` readiness check; the API has its own Service publishing not-ready addresses and a `*manager.Server` that starts before the caches; non-leader and leader start cases; `IdentityKubeconfigs` binds the auth-reader Role (§3.3, §9.4, §10.3.3) |
| B17 | The new values schema dropped `nameOverride`, `fullnameOverride` and `imagePullSecrets` | values.yaml:5-6,36; values.schema.json; _helpers.tpl | Fixed, with `TestTheDefaultValuesPassTheSchema` (§10.2.5) |
| B18 | `agent --domain all` brought back the `clustarr all` the owner removed | rulings.md (owner answers) | Fixed: one domain per process everywhere; OD1 rewritten so the owner can ask for it back (R4 amendment, §3.5, §3.10) |
| B19 | The scale-from-zero e2e scenario had no file, test or mechanism, and contradicted the e2e min-replicas patch | §9.9, §10.4, §11.2 as drafted; Makefile:247 (`-timeout 30m`) | Fixed: `test/e2e/zzz_scale_from_zero_test.go` `TestCaptionAgentScalesFromZero`, which removes and restores the annotation; suite timeout 60m (§10.4) |

**Minor findings**

| Finding | Disposition |
|---|---|
| A rename is path-only staleness, yet read as a high-lane request with Probed=False | Applied: incorporated in the reconcile with `mediainfo.AtPath`, no task (§6.5.3) |
| `ProbeCurrent()` gate list incomplete (transcodejob dispatch, `segmenting.Applier`) | Applied (§6.5.3) |
| `TestSessionDropDoesNotWipeANewerSave` asserted an impossible managedFields shape | Applied: two cases with achievable assertions (§5.15) |
| "Only writer of `MediaFileStatus`" is false per process (`status.markers`) | Applied: stated per field manager in R3 and for CLAUDE.md (§2, §12) |
| `make run-dev` exits 1 under `-trimpath` without `FFGO_SHIM_DIR` | Applied (§3.10) |
| The C++-symbol `nm` check failed on success and ran before `stage.sh` | Applied (§7.6, §10.1.3) |
| Build steps 8 and 9 required par2go and ffgo before steps 11 and 12 | Applied: requirements staged (§4.5.3, §11.1) |
| markers and transcode lacked `--version` | Applied (§3.7, §3.8) |
| `native-assets` races on par2go's in-tree build directory | Applied: builds from an export in its own temp directory (§8.8) |
| Unexported identifiers crossing steps 1.5, C8 and X2 | Applied (§4.3) |
| `ffruntime.Needs` could not check demuxers | Applied (§7.4, §6.2, §7.2.9) |
| Builds took whatever the sibling working trees held | Applied: pinned, clean `git archive` contexts and commits in `SOURCE`; `make build`/`make test` only warn, since fork work needs a dirty tree (§7.6, §10.1.6) |
| The par2 child bound ignores D-state | Applied: `Wait` in a goroutine with `reapSlack`, gauge, restated bound; R2's reason qualified (§2, §8.4.3) |
| Native text fields skip ffprobe's string validation | Applied: `validString` port and fixtures (§6.2, §6.7) |
| `av1` is hardware-only; `mpeg4` missing from `decode.Needs` | Applied: `libdav1d`, `mpeg4`, `mp3` (§7.2.9) |
| The audio early stop ignored `atrim`'s `first_pts` | Applied: stop on the sink's EOF; late-audio fixture (§7.2.3, §7.2.10) |
| Abandoned calls plus endless transient retries could restart the import agent every ~20 min | Applied: `Abandoned()` counts outstanding calls; an abandoned probe is non-transient; give up after 3 (§6.6, §7.4) |
| "The library has no MPEG-TS" is false (9 `.ts` files) | Applied: divergence accepted explicitly; every `.ts` in the parity run (§7.2.6) |
| Drain, grace and window are sized from `AckWait`, which nats-server replaces with `BackOff[0]` | Partly applied: the events figure is corrected (30 s drain, 55 s grace), and §3.5.6 now says `AckWait` is used as the repo's declared handler budget (topology.go:776-781), not the broker deadline. **Not applied:** a new `ConsumerSpec.HandlerBudget` (AckWait already plays that role in this repo), and a fixed 300 s scale-down window (R5 rules the window at least the longest AckWait; the rule is conservative, since lag counts `NumAckPending`, but not unworkable) |
| The External Metrics API waited for the manager's cache sync | Applied with B16 (§9.4) |
| A `min-replicas` annotation above `maxReplicas` makes every HPA apply fail | Applied: `maxReplicas = max(minReplicas, computed)` and Event `MinReplicasAboveCapacity` (§9.5) |
| The recycle Msg-Id outlives the 1 h dedupe window | Applied: the guarantee reworded, the extra sweep accepted as idempotent (§3.5.3) |
| §12 missed docs naming the retired topology | Applied (§12) |
| Tests placed where they could not compile or in two places | Applied (§5.15, §10.3.1, §10.3.4) |
| Behaviour changes decided inline (FingerprintVersion, probe retry cadence) | Applied: OD47 and OD48 (§13) |
| Installer edit lists missed `config/rbac/kustomization.yaml`, `clustarr.facadeAPIKeySecret` and the Makefile RBAC comment | Applied (§10.2.4, §10.2.5) |
