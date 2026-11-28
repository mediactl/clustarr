# Clustarr record: Invariants

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Invariants — do not break these

A guard name marked † is the one the plan gives a guard the test batch has yet
to write: the unify branch's waves were built implementation-first (owner,
2026-10-07), and `.superpowers/unify/progress.md` lists every deferred test.
Until the guard lands, review holds the invariant; drop the mark when it does.

- **A library kind is a row in Postgres, held to what the apiserver held it to**
  (ADR-0024; design `docs/superpowers/specs/2026-11-26-library-store-design.md`,
  §9 for what was built). MediaFile, Episode, Album, Book, Audiobook and Issue
  are no custom resources: the apiserver serves none of them. Everything below
  that says "CR", "status" or "resource" holds for them in the library store.
  - **Read and write one through the seams a custom resource uses**: the
    manager's cache (`mgr.GetCache()`, fed by `library.ListerWatcher`), its
    client (`pkg/library/readwrite`) and `pkg/k8s.PatchStatus`. A client-go
    or dynamic client, a `ctrl.NewManager` or `client.New` that was not wired
    (`pkg/library/ctrlwire`, `test/librarytest` in a test), and a statement of
    the caller's own cannot see one, or write one without its rules.
    `TestAManagerServesAKindThatHasNoCRD`.
  - **A write follows the apiserver's rules by running its code**: defaults,
    pruning and value checks from the kind's schema, field ownership by
    `k8s.io/apimachinery/pkg/util/managedfields`, the status subresource's
    split, generation, finalizers, and a Conflict on a stale
    `resourceVersion`. The differential tests hold every kind to a real
    apiserver, write for write (`TestIssueWritesMatchTheAPIServer`,
    `TestEveryKindsWritesMatchTheAPIServer`); a change to the write path that
    they do not cover is not proven.
  - **A kind's schema is its CRD file under `pkg/library/schemas/`**, moved
    there by `make manifests` (`LIBRARY_RESOURCES`) and installed nowhere. A
    validation rule (`XValidation`) on a library kind is also written in Go in
    `pkg/library/validate.go`: the manager links no CEL, and a schema naming a
    rule that table lacks does not load.
  - **The change feed is in commit order by construction**: a write takes its
    version from its kind's row in `library.head`, in the table's trigger, and
    holds that row until it commits. Never give a write a version any other
    way (a sequence, the clock). `TestTheFeedIsInCommitOrder` fails with the
    lock gone.
  - **The manager is the only writer of status, in the database too**: the
    ui's role is granted an object's row, body and ownership records and not
    its status column (`TestTheDatabaseHoldsEachRoleToItsShare`); agents and
    cluster-plex read with a role that writes nothing, and no agent links
    `pkg/library/readwrite` or calls a `library.Store` writer
    (`TestAgentLinksNoManagerRegistration`, `TestAgentsCallNoKubernetesWriter`).
    The chart grants neither role unless `library.ui.role` and
    `library.readOnly.role` name one; by default every process shares the
    Cluster's app role and only the code holds the rule.
  - **An owner in Kubernetes cannot collect a row.** `readwrite.Collector`
    (the leader's) deletes the objects all of whose owners are gone; a kind
    that needs its children gone sooner than its sweep deletes them itself.
    `TestTheCollectorDeletesWhatItsOwnerLeftBehind`.
  - **The tables are a contract with cluster-plex**, which reads
    `library.mediafile`, `library.episode`, `library.head` and
    `library.change` with its own statements
    (`TestTheTablesAreTheContractClusterPlexReads` runs them). Change both
    repositories together.
- **The manager is the only writer of every CR and Secret** (ADR-0019). Agents,
  markers and transcode pods write no Kubernetes object: an agent's client is
  read-only (`k8s.ReadOnly`, which counts a refused write in
  `clustarr_agent_write_refused_total`; `TestAgentsCallNoKubernetesWriter`†), its
  generated role grants get, list and watch only, Secrets get only
  (`TestNoAgentRoleGrantsAWriteVerb`†), and markers and the transcode pods run
  with no ServiceAccount token at all. A worker answers its task in a record
  (`pkg/records`: Durable KV, compare-and-swap) or on the intake streams, and the
  manager incorporates the answer under its own field manager. Every lifecycle --
  an item, a MediaFile, a grab and its transfer, a scan, a list sync, a search,
  an indexer's health -- is a state machine in the manager's reconcile loop, and
  every task an effect it admits and routes (`app/dispatch`).
  One exception, outside clustarr's own processes: a `PlexCollection`'s status
  and its finalizer (`clusterplex.io/plex-collection`) are cluster-plex's,
  written under field manager `cluster-plex` (owner, 2026-11-10, the
  collections design's option A); its spec is the UI's, kubectl's or
  Terraform's, and clustarr's manager never writes it.
  A `PlexHubLayout`'s status is cluster-plex's the same way (2026-11-16,
  the hub layouts design); it has no finalizer.
- **One controller-writer per resource.** The sole exception is `MediaFile`, and
  the split is **spec versus status**, not status versus status, with both
  writers in the manager: `import-worker` creates the resource and owns
  `MediaFileSpec` (the observed path, size and fingerprint, plus the quality,
  revision, formatScore, matchedFormats and releaseType frozen at import),
  through the scan applier (`app/import/manager/scanapply`) and the downloads
  stage's import materialisation; `catalog` is the sole writer of all of
  `MediaFileStatus`, through the remediation loop, and additionally takes over `spec.sizeBytes`,
  `spec.modTime`, `spec.original` and `spec.sourceSizeBytes` once it incorporates a transcode swap —
  and `spec.path` too when the transcode landed under a new name and the
  source is gone (a container change or an explicit `spec.outputPath`, ruling
  R-11). A `replaceSource=false` result is not a swap and claims nothing.
  Both write with their own field manager, so the apiserver enforces the split
  rather than convention. (Until Phase C this file claimed `import` owned
  `status.file`/`status.probe` and `catalog` `status.quality`/
  `status.formatScore`. No such fields exist — `MediaFileStatus` is flat and
  the decided fields are spec, frozen at import per spec §8.4. Nothing had
  reconciled yet, so no code ever contradicted the claim.) Since ADR-0016 the
  per-file work is MediaFile status too: the manager's one remediation loop
  (`app/remediation`, controller `mediafile`) plans and writes
  `status.subtitles`, `status.transcode`, `status.graft` and `status.markers`,
  so `catalog` owns every `MediaFileStatus` leaf
  (`TestMediaFileStatusIsOwnedByCatalogAlone`†). Workers answer through records
  in Durable KV buckets (`clustarr-probes`, `clustarr-transcodes`,
  `clustarr-grafts`, `clustarr-subtitles`, `clustarr-markers`), written by
  compare-and-swap, and hold no MediaFile RBAC (`TestFoldedAgentsWriteNoPerFileWork`†).
  Only the loop watches MediaFile (`TestOnlyTheLoopWatchesMediaFile`†); the slim
  TranscodeProfile and SubtitleProfile reconcilers and `transcode-admission`
  are woken by the loop's and the ledger's signals. SubtitleRequest,
  TranscodeJob, AudioGraft and Download are deleted (cold turkey, 2026-10-07):
  no code names them (`TestNoCodeNamesTheFoldedKinds`†, `TestNothingNamesDownload`†).
- **All status writes go through `pkg/k8s.PatchStatus`** (server-side apply with a
  named field manager). `.Status().Update()` and `.Status().Patch()` are banned
  outside `pkg/k8s` and golangci-lint's forbidigo rule enforces it.
- **No `float32`/`float64` in `api/`.** controller-gen rejects them without
  `allowDangerousTypes`, and floats do not round-trip across API clients. Use
  `resource.Quantity` for decimals a human types (e.g. seed ratio `"2.0"`) and
  scaled integers for machine telemetry: `Milli` (thousandths, 23.976 fps →
  `23976`), `Centis` (hundredths), `Percent` (whole 0-100).
- **The manager couples through Kubernetes watches; agents couple through NATS
  only** (ADR-0019). An agent reads Kubernetes through a read-only cache, takes
  its tasks from JetStream and answers in records, on `CLUSTARR_INTAKE` (grab
  candidates, scan observations) and in RPC replies; JetStream's nak and term
  advisories reach the manager on `CLUSTARR_TASK_EVENTS`. NATS otherwise carries
  rate-limited or long-running work, RPC, the release firehose, scheduled
  delivery and KV caches.
- **Only the manager ensures the NATS topology** (`busconn.EnsureTopology`, and
  `busconn.KeepTopology` after a NATS restart). `Subscribe` binds its durable and
  its dead-letter watcher and never creates either, so an older agent can never
  revert a newer topology (`TestEveryConsumerHasExactlyOneHome`†).
  The leader also spreads the JetStream leaders, which nats-server never
  moves once elected (`natsbus.Bus.BalanceLeaders`, `--nats-balance-leaders`;
  owner, 2026-11-03).
  - **Weights are measured, not declared:** a third traffic (appends or
    deliveries per second, an hourly EWMA), a third stored bytes, a third
    being one more RAFT group.
  - **When it runs:** a pass runs at the manager's start and once leader
    elections it did not ask for settle (the account's `LEADER_ELECTED`
    advisories: a NATS restart).
  - **How it moves:** account-level stepdowns to a named, current follower.
  - **What it can't move:** the meta leader needs the system account, which
    clustarr holds none of.
  - **Clients:** a durable whose leader moves ends a waiting pull with
    `ErrConsumerLeadershipChanged`, which the subscription loop treats as
    routine.
- **One domain per agent process.** `--domain` takes exactly one name; there is
  no `all` (`TestEveryAgentDeploymentRunsOneDomain`†).
- **Every container names its binary** (`pkg/binpath`); no image has an
  ENTRYPOINT (`TestEveryContainerNamesItsBinary`†).
- **No production code starts a process** but the usenet engine's `par2`
  (`pkg/download/usenet/repair.go`, `usenet.Par2Runner`) until par2go lands as a
  library; then only `pkg/par2child/exec.go`, whose only target is
  `os.Executable()` (ADR-0017; `TestNoProductionCodeStartsAProcess`†). The
  one other start is the trailers agent's yt-dlp (`pkg/ytdlp/exec.go`,
  ADR-0023, owner 2026-11-17): only `app/trailers` links the package
  (`TestOnlyTheTrailersAgentLinksYtDlp`), and only the optional `trailers`
  image holds the program. **No test, test helper, script or `hack/` program
  runs the ffmpeg or ffprobe CLI either, and the test environment installs
  neither** (2026-11-26, owner: migrate *from* the command line):
  a test makes its media with `internal/testmedia` (a Spec is an ffmpeg
  command line on ffgo; `Remux`/`Mux` are `-c copy`, with a codec tag, an
  input format, a packet's bytes or its timestamp changed) and reads it back
  with `Inspect`, `Frames`, `Packets` and `ChannelRMS`, and an oracle is a
  recorded golden under `test/data/media-goldens` (`TestNoGoFileRunsFFmpeg`,
  `TestNoScriptRunsFFmpeg`, `TestTheTestEnvironmentHasNoFFmpegProgram`). The
  par2 CLI stays for `test/par2test.CreateSet`. What ffmpeg(1) did silently
  and a Spec must say: the layout an encoder takes (TrueHD and DTS want
  `5.1(side)`, DTS a bit rate), the `ENCODER` stream tag it wrote on what it
  encoded (which is what reads as another tool's transcode), and a muxer
  whose name the file's extension does not give ffgo (`Format: "flac"`).
- **Cap every status list** with `+kubebuilder:validation:MaxItems`. Unbounded
  lists in status are how operators melt etcd.
- **The scanner never guesses.** Unattributable files go to
  `LibraryScan.status.unmatched` with the reason, never a speculative item.
- **The UI never writes status** and owns no CRD. User actions patch spec,
  create short-lived resources, or -- since the settings CRUD design
  (`docs/superpowers/specs/2026-09-24-settings-crud-design.md`) -- create,
  patch and delete the ten Settings kinds (RootFolder, QualityProfile,
  MetadataProvider, Indexer, IndexerProxy (2026-09-30), DownloadClient,
  SubtitleProvider, SubtitleProfile, TranscodeProfile, and ImportList from
  the Import Lists page) and create or patch the Secrets their
  credentials live in, never reading one (the role grants no get, list or
  watch on secrets) -- and, since Add New
  (`docs/superpowers/specs/2026-09-29-add-new-design.md`), create a Movie,
  Series, Artist or Author with spec only, named by `pkg/names` as
  import names it (the role's `create` on those four) -- and, since the
  Collections page (`docs/superpowers/specs/2026-11-10-plex-collections-design.md`),
  create, replace the spec of (a JSON patch testing `metadata.generation`)
  and delete a PlexCollection, whose status and finalizer are
  cluster-plex's -- and, since the Hubs page
  (`docs/superpowers/specs/2026-11-16-plex-hub-layouts-design.md`), create,
  patch `spec.hubs` and the `catalog.clustarr.io/hubs-reset` annotation of,
  and delete a PlexHubLayout, whose status is cluster-plex's. So anything
  the UI does, `kubectl` can do.
- **The UI may hold a bus connection for reads plus three requests.** `cmd/ui`
  (`internal/cli/ui/bus.go`) calls `busconn.Connect` and passes
  `Bus.ObjectStore(...)` into `ui.Options.Artwork` to serve `/art` (indexed by a
  read-only watch, `pkg/events/objindex`, which also feeds the SSE `art`
  event); closures over KV `Get` alone for the Plex provider's extended
  documents (`clustarr-metadata-extended`), a transfer's and a transcode's
  progress (`clustarr-progress`) and an import's detail (`clustarr-imports`);
  and three requests, `ui.Options.MetadataSearch`, bound to
  `clustarr.rpc.catalog.metadata.search` (Add New, 2026-09-29),
  `ui.Options.MetadataLookup`, bound to `clustarr.rpc.catalog.metadata.lookup`
  (the audiobook add's author by ASIN, 2026-11-10, the non-video design's
  D11; `requestIdempotent` as the search), and `ui.Options.PlexExtras`,
  bound to `clustarr.rpc.catalog.metadata.extras` (2026-10-06), so the ui
  never holds a requester and can ask nothing else --
  `TestUINeverWrites` bans `Request`, `Publish` and `Serve` anywhere in `ui/`.
  `ui/` still never imports `pkg/k8s`.
  `TestUINeverWrites`' AST guard extends its banned-selector list with every
  object-store write method (`Put`, `PutBytes`, `UpdateMeta`, `Seal`,
  `AddLink`, `Purge`) so a NATS write is caught the same way a Kubernetes
  write already is — the connection is read-only by construction, not by
  omission.
- **Artwork objects have two writers split by variant, the same discipline
  as spec versus status on MediaFile.** The metadata gateway (agent
  `metadata`) is the sole writer of `original` objects and their metadata; the
  renderer (agent `catalog`) is the sole writer of `overlay` objects and theirs
  (Movie and Series only). Neither writes status since ADR-0019: the manager's
  item keys render `status.artwork` (under `k8s.ManagerCatalogMetadata`) and
  `status.overlay` (under `k8s.ManagerCatalogArtwork`) from the objects'
  metadata, read through the manager's own `objindex`. Neither writer reads,
  writes or deletes the other's variant. Each `Put` sends the variant's
  complete header and metadata set (`artwork.ObjectMeta`), and `SetMeta` is the
  owner's alone, under its per-item lock. No code creates object links
  (`TestNoObjectLinks`†). The ui watches the bucket read-only
  (`pkg/events/objindex`). The reaper (the manager's, leader-only) is the only
  code that deletes both; the manager's object-store maintenance loop
  (`app/maintenance`, ADR-0020 §5) purges orphan chunks in every store.
  **Each writer has a compact variant beside its regular one** (the
  compact-posters design, 2026-11-27; owner, 2026-11-28: "Approve the
  design"): `compact` is the metadata gateway's, the twin of a titled
  `original` with the logo in the larger box, drawn in the fetch that draws
  the original and stored before it, and deleted with a poster that is no
  longer titled or is dropped; `overlay-compact` is the renderer's, the same
  badges in the profile's compact geometry (`spec.compactGeometry`,
  `overlay.CompactTemplateSpec`), drawn on `compact` when the item has one
  and on `original` when not, in the render task that settles `overlay`, and
  cleared with it. `events.KnownArtworkVariant` is the one enumeration.
  What must hold: **a regular `original` or `overlay` keeps its bytes for
  the same inputs** -- the compact draw is on a copy of the canvas
  (`TestFetchTitled_StoresCompactAndLeavesOriginalAlone` pins the digest,
  `TestDrawTitled_RegularUnchanged` the pixels), `TitleVersion`,
  `RenderVersion`, `InputsDigest`, `ProfileHash` and `status.overlay` do not
  see the compact rendition (its own versions are `CompactTitleVersion` and
  `overlayplan.CompactRenderVersion`, its own digest
  `overlayplan.Compact.InputsDigest`), since a new regular digest is a new
  poster URL and Plex refreshes the item. No status field records a compact
  object: the plan fetches a titled poster whose twin's mark is not its
  source's (`Source.CompactMark`), and the overlay stage asks for a render
  when the compact overlay's `Clustarr-Rendered-From` is not the plan's.
  The ui's pages never show one and raise no art event for one
  (`TestACompactObjectRaisesNoArtEvent`).
  A profile edit that moves only the compact template
  (`spec.compactGeometry`) wakes its namespace's items through the overlay
  stage's source S41 (`app/remediation/artwork/overlaysource.go`): the
  OverlayProfile controller compares `status.overlay` and cannot see it.
  The compact digest carries `RenderVersion` as well as its own version.

