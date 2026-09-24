# Clustarr

A distributed, event-driven media stack for Kubernetes: controllers and CRDs that
manage a media collection end to end, from wanting something through finding,
downloading, importing, transcoding and subtitling it.

Custom resources are the interface. `kubectl get movies,downloads,transcodejobs`
is the UI, and the web UI is a view over the same resources.

## Read before designing anything

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` — the design of record.
  Field-level CRD definitions, event subjects, data flows, milestones.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` — adds
  `importarr`, observability and the UI. **Where the two disagree, the amendment
  wins.**
- `docs/adr/0001..0008` — why NATS, why GPL-3.0, why Jobs for transcode, and so on.
  `docs/adr/README.md` is the index and the supersede lifecycle.
- `docs/research/*.md` — nine verified research notes (TRaSH quality model,
  Prowlarr/Cardigann, queue comparison, torrent/usenet, controller-runtime,
  ffmpeg, Bazarr, metadata providers, naming). Long: `grep -n '^#'` first, then
  read the section. They contain verified API shapes — prefer them over guessing.

The spec is authoritative for names, fields and behaviour. Do not invent fields;
do not drop them.

## Services

One Go module, one cobra binary: `clustarr <service> --role <role>`.

| Dir | Owns |
| --- | --- |
| `app/catalog/` | Media domain (movies, series, music, books, comics, audiobooks), metadata gateway, release decisions, artwork render role (`--role artwork`) |
| `app/import/` | Everything entering the library: root-folder rescan, import lists, completed-download import |
| `app/indexer/` | Indexer aggregation, Cardigann engine, local release index (SQLite FTS5) |
| `app/grab/` | Download clients: torrent (anacrolix) and usenet engines |
| `app/squash/` | Transcode to HEVC 10-bit + AAC, as batch Jobs |
| `app/caption/` | Subtitles, Bazarr-equivalent, distributed |
| `ui/` | Server-rendered web UI (templ + htmx + SSE); serves artwork at `/art` and the Plex Metadata Provider roots over a read-only bus; never writes status |

API groups: `{catalog,index,download,transcode,subtitle}.clustarr.io/v1alpha1`.
Module `github.com/mediactl/clustarr`. Licence GPL-3.0 (lets us port *arr and
Bazarr logic verbatim — keep the header on every file).

## Transcoding

A transcoded media file should be the final destination. If we detect a transcoded profile, we should not mark the media "CutoffUnmet" - instead it should be marked `Transcoded`.

The predicate is `app/catalog/controller/rollup.Transcoded` -- `spec.original` false, or the probe's `status.mediaInfo.transcodeProfile` (the `CLUSTARR_PROFILE` tag) -- and such a Movie or Episode reads phase `Transcoded`, with `CutoffMet=True` reason `Transcoded`; `pkg/decision` rejects every automatic upgrade of it as `TranscodedFinal` (spec §4.2).

## UI

The library UI page should contain tabs for each media type:
(Movies) (TV) (Music) (Books)
In the TV pane, only series should be shown. Clicking a series should present a page with the seasons and episodes.

Each item should show the cover art, monitored status and selected quality profile

## Invariants — do not break these

- **One controller-writer per resource.** The sole exception is `MediaFile`, and
  the split is **spec versus status**, not status versus status: `importarr`
  creates the resource and owns `MediaFileSpec` (the observed path, size and
  fingerprint, plus the quality, revision, formatScore, matchedFormats and
  releaseType frozen at import); `catalogarr` is the sole writer of all of
  `MediaFileStatus`, and additionally takes over `spec.sizeBytes`,
  `spec.modTime` and `spec.original` once it incorporates a transcode swap —
  and `spec.path` too when the transcode landed under a new name and the
  source is gone (a container change or an explicit `spec.outputPath`, ruling
  R-11). A `replaceSource=false` result is not a swap and claims nothing.
  Both write with their own field manager, so the apiserver enforces the split
  rather than convention. (Until Phase C this file claimed `importarr` owned
  `status.file`/`status.probe` and `catalogarr` `status.quality`/
  `status.formatScore`. No such fields exist — `MediaFileStatus` is flat and
  the decided fields are spec, frozen at import per spec §8.4. Nothing had
  reconciled yet, so no code ever contradicted the claim.) `TranscodeJob.status`
  is squasharr's alone, under one field manager, `k8s.ManagerSquasharr`: the
  reconciler and the `squasharr-transcode-results` consumer both write it, but
  through one compare-and-swap path, not as two managers (Gotchas, below).
- **All status writes go through `pkg/k8s.PatchStatus`** (server-side apply with a
  named field manager). `.Status().Update()` and `.Status().Patch()` are banned
  outside `pkg/k8s` and golangci-lint's forbidigo rule enforces it.
- **No `float32`/`float64` in `api/`.** controller-gen rejects them without
  `allowDangerousTypes`, and floats do not round-trip across API clients. Use
  `resource.Quantity` for decimals a human types (e.g. seed ratio `"2.0"`) and
  scaled integers for machine telemetry: `Milli` (thousandths, 23.976 fps →
  `23976`), `Centis` (hundredths), `Percent` (whole 0-100).
- **Kubernetes watches are the default coupling**; NATS is the exception, for
  rate-limited or long-running work, RPC, the release firehose and KV leases.
- **Cap every status list** with `+kubebuilder:validation:MaxItems`. Unbounded
  lists in status are how operators melt etcd.
- **The scanner never guesses.** Unattributable files go to
  `LibraryScan.status.unmatched` with the reason, never a speculative item.
- **The UI never writes status** and owns no CRD. User actions patch spec,
  create short-lived resources, or -- since the settings CRUD design
  (`docs/superpowers/specs/2026-09-24-settings-crud-design.md`) -- create,
  patch and delete the eight Settings kinds (RootFolder, QualityProfile,
  MetadataProvider, Indexer, DownloadClient, SubtitleProvider,
  SubtitleProfile, TranscodeProfile) and create or patch the Secrets their
  credentials live in, never reading one (the role grants no get, list or
  watch on secrets). So anything the UI does, `kubectl` can do.
- **The UI may hold a read-only bus connection.** Since M7, `cmd/clustarr`'s
  ui command calls `k8s.ConnectBus` and passes `Bus.ObjectStore(...)` into
  `ui.Options.Artwork` to serve `/art`; `ui/` still never imports `pkg/k8s`.
  `TestUINeverWrites`' AST guard extends its banned-selector list with every
  object-store write method (`Put`, `PutBytes`, `UpdateMeta`, `Seal`,
  `AddLink`, `Purge`) so a NATS write is caught the same way a Kubernetes
  write already is — the connection is read-only by construction, not by
  omission.
- **Artwork objects have two writers split by variant, the same discipline
  as spec versus status on MediaFile.** The metadata gateway
  (`k8s.ManagerCatalogarrMetadata`) is the sole writer of `original` objects
  and of `status.artwork`; the renderer (`catalogarr --role artwork`,
  `k8s.ManagerCatalogarrArtwork`) is the sole writer of `overlay` objects and
  of `status.overlay` (Movie and Series only). Neither reads, writes or
  deletes the other's variant; the reaper is the only code that deletes
  both.

## Commands

```bash
make generate      # deepcopy + apply configurations
make manifests     # CRDs + RBAC into config/
make build         # binary into bin/  (never bare `go build` — it drops a binary in the repo root)
make test          # unit + envtest, sets KUBEBUILDER_ASSETS
make lint          # golangci-lint v2
make cardigann-bundle  # re-pack .data/Definitions into app/indexer/bundle/embedded/definitions.zip
make kind-up       # local cluster with NATS, then: make install deploy
make e2e           # end-to-end suite against that kind cluster (test/e2e, tag e2e)
```

Tools live in `$(go env GOPATH)/bin`: `controller-gen` v0.22.0, `setup-envtest`,
`kustomize`, `golangci-lint-v2`. envtest assets are Kubernetes 1.37.0.

## Gotchas found the hard way

- **`make test` cannot pass in a fresh clone or a new git worktree until
  `helm dependency build` has run.** `charts/clustarr/charts/` holds three
  vendored dependency tarballs (`nats`, `cloudnative-pg`, `keda`) and is gitignored by
  `charts/clustarr/.gitignore`, so a clean checkout does not have them.
  `TestChartAndKustomizeAgreePerComponent` shells out to real `helm template`,
  which refuses with "found in Chart.yaml, but missing in charts/ directory"
  — and it reads as a chart/kustomize *disagreement*, which is a code defect,
  rather than as a missing local artifact. This cost a false red on a merge
  gate run in a throwaway worktree; the branch was fine. Either run
  `helm dependency build charts/clustarr` first or copy the `.tgz` files in.
- **`go test ./...` passing does not mean the CRDs are valid.** `pkg/crdcheck`
  and the envtest suites skip silently when `KUBEBUILDER_ASSETS` is unset. Only
  `make test` (or exporting the assets path) compiles the CEL rules against a real
  apiserver. A suite that finishes in milliseconds skipped.
- **controller-gen v0.22.0's `applyconfiguration` generator ignores output rules**
  — it writes next to the types regardless of `output:...:dir`. The Makefile
  documents the workaround.
- **Server-side apply replaces a field manager's ownership set on every apply —
  it does not merge.** Any field that manager previously sent and now omits is
  *released*, which reads as "reset to zero" on the object. This bit three
  different ways in Phase C alone, so treat every `PatchStatus` call as a
  complete declaration of everything that manager owns:
  - Two applies in one reconcile (spec, then labels): the second erased the
    first. Build one apply configuration instead.
  - A boolean stopped being sent once it was true: it flipped back to false.
    Keep sending it.
  - **An early-return path built a partial status** (conditions only) and
    returned: it wiped `Phase`, `Path`, and every rollup field the happy path
    had set. This is the dangerous one, because the early return is usually a
    transient failure — a full queue, a missing RootFolder — so a healthy
    object gets silently gutted by a blip.
  - **Two different components shared one field manager on one object.** The
    metadata gateway and the grab worker both write `Movie.status` as
    `catalogarr-worker`, so the grab released `status.metadata` — the movie
    dropped to `Phase=Pending` and the gateway refetched, on every grab. Give
    each writer its own manager (as `catalogarr-series` does) rather than
    re-asserting the other's fields.

  **A double-claim is silent, not loud — this file and a code comment both
  said otherwise and both were wrong.** `pkg/k8s.PatchStatus` and
  `pkg/k8s.Apply` append `client.ForceOwnership` unconditionally
  (`patch.go:88,121`), and there is no non-forcing path in the package.
  Forcing is what makes deliberate co-ownership work, so it is not a bug — but
  it means the apiserver never raises the conflict that distinct field managers
  were supposed to surface. The later applier simply takes the field. Proven by
  making one manager claim another's field and watching **every object-value
  assertion keep passing**. So a test that asserts on the object's values
  catches *under*-declaration only; an over-claim is visible in exactly one
  place, and that is where a test meaning to catch one has to look —
  `metadata.managedFields`, by manager name against the field paths it owns.

  And the reason this class keeps surviving review: **two managers can
  *co-own* a field.** SSA only needs force when their values differ, so while
  a second manager keeps applying the same value, a release by the first
  leaves the field standing and the bug is invisible. A "manager X released
  field Y" test that does not deliberately drop the co-owner reports a false
  pass — which is how the rollup release above survived three reviews.

  - **A sibling controller never got the fix its siblings did.** Movie and
    Series gained `reassertKnownStatus`; `mediafile`, built in the same wave,
    kept both partial early returns *and* a second unconditional status apply
    at the end of every reconcile. Once a rule goes in here, sweep every
    controller for it — the rule existing is not the same as the code obeying
    it. The gap fixes hit the same class outside SSA three more times: a CI fix
    to how the `media` image fetches par2 was not carried to `media-cuda`,
    which kept failing; deleting the rescan's copy of the release-group guard
    once the parser was fixed left `fileimport`'s second copy dropping real
    groups; and `rollup.DownloadOverlay` learned the just-created `""` phase
    while `pkg/pipeline`'s own mapping of the same enum did not. Before closing
    a fix, grep for the other copies of the thing you fixed.

  - **The same rule binds the main resource, not only status — and there the
    failure is a rejected write, not a silent reset.** A manager that applies
    `spec` must send every spec field it owns on every apply. D2-4's
    labels-only apply omitted `spec.clientRef`; the same manager released it,
    and the Download's "clientRef is immutable" CEL rule then rejected the
    write. G1-3's unmonitor path sent only `spec.monitored` under the manager
    that also creates Movies, releasing the required `tmdbID`,
    `qualityProfileRef` and `rootFolderRef`, so the apiserver refused it.
    Route every write from a manager through the one function that renders
    that manager's complete set, and override a field there, rather than
    building a second, narrower apply.

  **`reassertKnownStatus` has an exception.** `MediaFileStatus`' `Conditions`
  and `Sidecars` are lists whose generated `With*` methods **append**, so a
  reassert-then-overwrite helper doubles their entries. Seed a struct and
  render it once instead of copying the sibling pattern verbatim.

  A related trap, same apply, different mechanism: **`WithConditions` appends**
  rather than replacing, so setting conditions both in a shared `baseStatus`
  helper and again at the call site is rejected outright with `duplicate
  entries for key [type="Ready"]`. Set conditions in exactly one place per
  apply.

  Tests miss all three unless they act on an object that **already has status**.
  A test that creates a blank object, triggers the path and asserts cannot
  observe a release, because there was nothing to release. Exercise the failure
  path against an object already in its steady state.
- **A lost update is not an SSA release, and no release test can see one.**
  Phase D1 hit this three times in one wave. A handler reads the object, does
  slow work (an HTTP fetch, a four-page RSS poll), then applies a status seeded
  from that stale snapshot — silently rolling back whatever another writer
  under the same manager did meanwhile. The complete-declaration rule does not
  help: every field *is* declared, just with values from a minute ago. Worse
  than a rollback, a field that was nil when read and non-nil now is
  **cleared**, because a seed that emits a pointer field only when non-nil omits
  it — so a download verb re-enabled an indexer the search fan-out had just
  disabled, undoing the backoff protecting a failing tracker. Every
  release-regression test in the tree is blind to this by construction: the
  object it seeds from is the one it wrote, so there is no window. The
  distinguishing test interleaves a *real* second writer mid-operation; the
  rule is that any path which reads, works slowly, then applies must re-`Get`
  immediately before the apply. `app/indexer/worker/rss/worker.go` does this with
  the comment "the poll closes the window".
- **Server-side apply tracks ownership per leaf inside a struct**, not for the
  sub-object as a whole. A renderer that sent only `caps.modes` released
  `caps.limitsMax`, `caps.limitsDefault`, `caps.supportsRawSearch` and
  `caps.categories` on every apply that did not re-probe — and the reconciler
  re-applies every 15 minutes while re-probing every 12 hours, so four of five
  fields zeroed within one tick of the probe that set them. The envtest that
  should have caught it asserted only that `status.caps` was non-nil, which a
  modes-only renderer satisfies. Assert every leaf, not the parent.
- **`metav1.Time` comes back from the apiserver in the replica's local
  timezone, so `.Year()` on it is wrong by a year near New Year.**
  `metav1.Time.UnmarshalJSON` converts to `time.Local` on every round-trip, so
  a release dated 1 January 00:30 UTC reads as the previous year on any host
  west of UTC — and a year feeds folder names and year-tolerance matching, so
  files land in the wrong folder. G2-3 found it in the Audiobook naming
  context; the same `.Year()` call was already in the Album controller and in
  four places in the non-video rescan and import paths. Always
  `.UTC().Year()` (and `.UTC()` before any other calendar field), and test it
  with a Jan 1 date on a west-of-UTC location.
- **A typed Go client cannot send a value the CRD default overrides.** The
  apiserver defaults only an *absent* field, so what counts is what
  `encoding/json` sends for the Go zero. A `bool,omitempty` with
  `default=true` drops `false` and gets `true` back, so it can never be set
  false; a struct-valued field (`metav1.Duration`, `resource.Quantity`, a
  nested struct) or a scalar without `omitempty` is always sent, so its
  default never reaches an object created from Go; and an `omitempty` scalar
  whose zero means something ("0 disables") loses that zero to the default.
  kubectl YAML sends every value, which is why it survives review: it hit
  five times across Phases D-F before G4-0 swept it, the worst an `int32`
  `maxOutputToSourcePercent` defaulted to `1.0` that would have failed every
  real transcode. Fix with a pointer plus an accessor that applies the
  default to nil (`app/squash/worker.ReplaceSource`,
  `Revision.EffectiveVersion`), or a floor in code, documented on the field,
  where zero means nothing (`Indexer.spec.timeout`). `pkg/crdcheck`'s
  `TestNoCRDDefaultIsUnreachableFromGo` and `TestEveryStatusListIsCapped`
  guard the sweep.
- **A pod a controller builds at runtime escapes every guard the installers
  have.** The chart and `config/manager` are held to each other, to the
  generated roles and to a securityContext by tests; the pod specs grabarr and
  squasharr build in Go were held to nothing. Transcode Job pods had no
  securityContext, UMASK or tracing flags; engine pods had no ServiceAccount
  (so they ran as `default`, bound to nothing), no `POD_NAMESPACE`, no NATS
  address, no UMASK, no `GOMEMLIMIT` and no securityContext — they could not
  have started on any cluster, and every envtest passed, because envtest
  enforces no RBAC and runs no pods. The KEDA ScaledJob example ran the wrong
  ServiceAccount without `--job`. Fix the class, not the instance: test the
  rendered pod spec against what the installer creates and binds
  (`TestGrabarrEnginesRunAsAnAccountTheInstallerBinds`,
  `TestEnginePodSecurityMatchesTheDeployment`), and run services under their
  real roles (the RBAC-enforced start envtest).
- **A test built from a fixture shaped like the answer cannot fail.** Custom
  formats' `ReleaseTitle` conditions were matched against
  `ParsedRelease.Title`, which the real parser sets to the item's title
  ("Heat"), so no repack, HDR or streaming format ever scored — and every test
  passed, because each built a `ParsedRelease` whose `Title` was the whole
  release name. The RSS matcher's yearless series fallback never matched a
  single release, and its test's fixture carried a year. A `TitleNorm` test
  used ASCII titles, on which it and `CleanTitle` are identical. Build inputs
  through the real producer (run the real parser, the real client against a
  recorded response), and falsify: revert the fix and watch the test fail by
  name. Falsification also found tests that never existed — a guard a Phase C
  ruling required ("fails if controller-gen ever generates one") and a
  RoundTripper fix, both untested.
- **Other processes share this checkout, its git state and the scratchpad.**
  In one wave an agent's `git stash` swept five other agents' uncommitted
  files (each recovered its own with `git show stash@{0}:<path>`, never
  `pop`); a process ran `git pull --rebase origin main` and pushed mid-gate,
  giving every commit of the wave a new SHA, so find commits by subject
  (`git log --grep`), never by a SHA from a report; and two agents' scripts of
  the same name in the shared scratchpad meant one repeatedly reset the
  other's throwaway worktree, invalidating its falsification runs. Keep
  scratch files under your own subdirectory, name worktrees uniquely, and
  never push from a task.
- **Never run `go get` or `go mod tidy` from parallel agents.** They corrupt
  `go.mod`. Add every dependency serially up front, then tell workers not to touch
  it.
- **The git index is shared by every agent in the worktree, so `git add <path>`
  followed by a bare `git commit` is not a path-scoped commit.** It commits
  whatever else was already staged. This swept fourteen files of another agent's
  half-finished, non-compiling work into a docs commit, under a docs message.
  Staging your own path is not the scoping step -- the commit is. The only safe
  form while anyone else is working is a pathspec on the commit itself:
  `git commit -m '...' -- path/to/file`, which builds the commit from HEAD plus
  those paths and ignores the index for everything else. To undo a sweep, do
  **not** `git reset --hard` or `git stash` (both destroy the other agent's
  uncommitted work, and `stash` is process-global): move the branch ref with a
  compare-and-swap, `git update-ref refs/heads/<branch> <sha>^ <sha>`, which
  leaves the index and working tree exactly as they were.
  **A pathspec scopes files, not authors.** Two agents editing the *same*
  file cannot be separated by it: whichever commits first takes the other's
  uncommitted hunks in that file too. That is how one agent's `lookupBooks`
  landed inside another's `lookupAlbums` commit. Harmless there, but it means
  a shared file should have one owner per wave, or be committed immediately
  after each edit.
- **A NATS KV key must match `^[-/_=\.a-zA-Z0-9]+$`, and nothing in the Go
  types enforces it.** Build every key through `events.KVKeyToken`; its
  escaping is injective on purpose, because a sanitiser that maps every
  illegal byte to one replacement collapses distinct ids onto one key and
  serves one item's state for another. This escaped twice: an illegal cache
  key made **every** metadata refresh fail forever, and an illegal exclusion
  key left the object **undeletable**, because the finalizer's `Delete`
  validates the key exactly as the `Put` did. Both times every suite that
  could have caught it ran against the in-memory bus, which has no key
  grammar — so the guard is a contract test against a real embedded server
  (`pkg/events/natsbus/kvkey_contract_test.go`), not a regex restated in a
  test file. Restating it is also how `ValidKVKey` shipped permissive:
  nats.go's gate is the character set **plus** no leading `.`, no trailing
  `.` and no `..`, and the first version implemented only the regex.
- **JetStream raises a pull consumer's MAX_DELIVERIES advisory only on its
  next delivery attempt, and that needs a waiting pull request.** A `Consume`
  callback that blocks issues none, so a handler hung inside the callback
  never gets its message dead-lettered, and its consumer stalls with it
  (nats-server 2.15 `getNextMsg`). That is why natsbus's callback never blocks
  and parks a delivery when every `MaxInFlight` slot is busy — and why a test
  of the hung case once needed `MaxInFlight = MaxDeliver+1` to see the
  advisory at all.
- **On a consumer with `BackOff`, nats-server adds `BackOff[n-1] -
  BackOff[0]` to every delayed nak.** `processNak` stamps the entry
  `now - AckWait + d`, `checkPending` redelivers once it is `BackOff[n-1]`
  old, and `BackOff` overrides AckWait with `BackOff[0]`. A retry that asked
  for `BackOff[n-1]` therefore waited nearly twice that — about 2h, not 1h,
  on `catalogarr-search-normal`'s fourth attempt — while membus, which has no
  such arithmetic, waited exactly `d`, so every test on the in-memory bus
  passed. natsbus now naks for `d - (BackOff[n-1] - BackOff[0])`
  (`natsbus.nakDelay`), and the contract test measures the real gap between
  deliveries on both buses.
- **A Helm hook with no delete policy defaults to `before-hook-creation`,
  which destroys anything holding data.** A `post-install,post-upgrade`
  hook carrying no `helm.sh/hook-delete-policy` annotation is deleted and
  recreated on every `helm upgrade` — fine for a Job, fatal for a database.
  The CNPG `Cluster` for the Postgres-backed release index
  (`charts/clustarr/templates/postgres-cluster.yaml`) shipped as exactly
  that hook first, caught in review before it reached `main`: a stateful
  resource that must survive `helm upgrade` is a normal templated resource
  carrying `helm.sh/resource-policy: keep` (as `pvc.yaml`'s `/data` claim
  already does), never a hook. Ordering against an external operator
  (CNPG's admission webhook fails closed until its own Deployment is Ready)
  is a render-time `clustarr.validate` fail plus documentation instead —
  and `--wait` does **not** substitute for that ordering, since it only
  waits on the release's own resources, never a resource Helm did not
  install.
- **A JetStream object digest is `SHA-256=<base64url>`, not the hex
  `events.ObjectInfo.Digest` documents.** `natsbus.objectDigestHex`
  (`pkg/events/natsbus/objectstore.go`) converts through
  `jetstream.DecodeObjectDigest` plus `hex.EncodeToString`; an empty digest
  (a freshly created, still-uploading object) stays empty rather than
  erroring, and anything else that fails to decode is reported as an error,
  never silently turned into `""`.
- **`make pg-assets` (`CLUSTARR_PG_ASSETS`) populates `embedded-postgres`'s
  cache directory once, the way `setup-envtest` populates
  `KUBEBUILDER_ASSETS`.** `make test`/`test-race` depend on it and export
  the variable; `pkg/relindex`'s Postgres store contract test skips
  silently, naming the variable, when it is unset — the same silent-skip
  trap `KUBEBUILDER_ASSETS` already has above, now with a second variable
  to remember.
- **`git stash` swept another task's uncommitted work again, despite the
  rule above.** An M7 task ran a path-scoped `git stash` mid-task; the
  stash list was empty afterwards and no damage was observed this time, but
  the rule stands exactly as written above: never `git stash` while another
  agent may be working in the same checkout.
- **`ForSingleNode` keeps object stores on file storage.** It maps streams
  and KV buckets to memory (scaled into a 64 MiB budget under config/nats'
  256Mi `max_memory_store`), but the artwork object store reserves 5 GiB,
  and mapping it to memory made every controller crash-loop on kind at
  start (`ensure object store clustarr-artwork: insufficient memory
  resources available`, 2026-09-24). `TestEnsureDefaultTopology`'s embedded
  server has no memory ceiling, so it never saw it;
  `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` runs the same
  Ensure against a server capped like the cluster's. Anything new in the
  topology must fit that server.
- **The Helm chart's NATS ran the server's 1 MiB `max_payload` default
  while `config/nats` has always set 8Mi, and natsbus dropped the refused
  reply on the floor.** indexarr answers `rpc.indexarr.download` with the
  .nzb inline (base64 in JSON, so 4/3 of its size); a 1080p movie's
  ~1.3 MB .nzb is over 1 MiB, nats.go refused the reply client-side with
  `ErrMaxPayload`, `Serve` discarded that error, and the usenet engine
  waited out its 45 s deadline on every attempt -- the first real NZB
  grab on the owner's cluster sat in `Assigned` with "context deadline
  exceeded" while indexarr's metrics counted a grab (2026-09-24). Two
  fixes: `nats.config.merge.max_payload: "<< 8Mi >>"` in the chart, held
  to kustomize by `TestBothInstallersRaiseTheNATSMaxPayload`; and `Serve`
  answers a reply it cannot send with a header-only service error, so the
  requester fails at once naming the cause
  (`TestServeReportsAReplyTheServerRefuses`). Operational trap on top: a
  `max_payload` reload updates the server, but a connected nats.go client
  keeps the `INFO` it read at connect, so the publisher (indexarr) had to
  be restarted before the raised limit took effect.
- **A par2 set named `name.vol-01.par2`..`vol-07.par2` carries no block
  counts, and the usenet engine read it as unrepairable.** `par2VolumeRE`
  only knew `vol000+01`, so those volumes classified as *index* files with
  zero blocks, `criticalHealthPercent` came out at 100, and the first failed
  article of 14,169 paused a 10 GB transfer at 1% under `healthAction:
  pause` ("health 99% is below the floor (abort 90%, critical 100%)") -- the
  same grab as above, once the payload reached the engine. The regex now
  takes the block-less form, `criticalHealthPercent` falls back to NZBGet's
  size-based estimate (`(size - 2*par) / (size - par)`, capped at 99) when
  no volume names its blocks, and `par2IndexFile` hands par2cmdline the
  smallest volume when the set has no separate index, since every volume
  carries the main packet. Real subjects from that post are the fixture
  (`TestCriticalHealthPercentEstimatesFromVolumeSizesWithoutBlockCounts`).
- **`par2 r index.par2` alone reports every target missing when the set
  records other names than the files on disk carry, and the engine's
  failure text then overflowed `status.message`.** The same grab's set
  described obfuscated names (`z75QO...part070.rar`) while the files were
  written under the subjects' names; SABnzbd and NZBGet pass the
  directory's files as extras so par2 matches them by content, and
  `Par2Runner.Repair` now does too (`extraFiles`;
  `TestPar2RunnerRepairsASetWhoseFilesCarryOtherNames` runs real par2).
  The failure message -- par2's 2048-byte output tail behind a prefix --
  was over the CRD's `MaxLength=2048`, so the apiserver rejected every
  status apply and a finished, failed transfer read `Downloading` for
  good; `pkg/download.clampMessage` cuts it on a rune boundary, the same
  class as `clampPercent`. Any string an engine writes into a bounded CRD
  field must be clamped at the `pkg/download` boundary, not trusted.
- **The usenet engine's emptyDir scratch shares the node disk with etcd
  on kind, and vanishes with the pod.** A 10 GB transfer plus its par2
  verify pushed etcd's fsyncs past a second: grabarr lost its leader lease
  and exited, then the kube-apiserver failed its liveness probe and was
  killed (2026-09-24); and a deploy that replaced the engine pod discarded
  a finished 9.4 GB transfer, so the new pod downloaded it again. Place
  the working area on the shared volume instead -- `spec.usenet.scratch.
  path: /data/usenet/incomplete` and `spec.usenet.publishDir:
  /data/usenet/complete` (ADR-0014; `existingClaim`, `volumeName` and
  `accessModes` cover a claim of the operator's own). The controller
  refuses a path off the data mount with `Ready=False, InvalidSpec`.
- **A newznab .nzb is not byte-stable across fetches, so a payload hash
  cannot tell a re-add from a new transfer.** nzbgeek varies the obfuscated
  `title` and `password` metas on every download of the same release; the
  usenet engine re-resolved a Download whose status did not yet carry its
  id (a status write had failed) and added the release a second time under
  a second hash -- two 10 GB transfers of one Download, the first an orphan
  until the reaper's 10-minute grace (2026-09-24). `usenet.Client.Add` now
  dedupes on `AddRequest.Name` as well as on the payload, and the engine
  asks the client (`download.ByName`) before fetching a payload again --
  every fetch is a grab the indexer counts. Torrents are unaffected: an
  info hash is the content.
- **The usenet engine's missing-article rules are SABnzbd's, by
  number.** `maxArticleTries` 3 same-server retries for a connection that
  dies mid-batch (`max_art_tries`); `maxBadArticles` 5 tolerated before
  any judgement (`MAX_BAD_ARTICLES`); `hopeless` is
  `check_availability_ratio` with `req_completion_rate` 100.2%, in bytes
  and integer arithmetic; plus one retry pass over every missing article
  (`retryFailed`, `ArticleRetryDelay`) before a breach or at the end of the
  transfer. Whole-percent health against a whole-percent floor decided a
  10 GB job at the margin once ("96%" that was 96.98% against 97), so the
  gate compares bytes, and the percentages in status are for reading only.
- **Every cache strips `managedFields`, and the cache-sync timeout is ten
  minutes.** On the owner's library (15,630 Episodes, a 57 MB list that
  `kubectl` alone takes 40 s to fetch) captionarr crash-looped on
  controller-runtime's two-minute default while every service listed at
  once after an upgrade, so `pkg/k8s.ManagerOptions` sets
  `Controller.CacheSyncTimeout = k8s.CacheSyncTimeout` and a
  `DefaultTransform` of `cache.TransformStripManagedFields()`, and the ui's
  reader does the same. The trap: a reconciler that reads `managedFields`
  off an object from the cached client sees none, silently. Read them
  through `mgr.GetAPIReader()`, as the grab worker's `appliedByGrabPath`
  does; the artwork envtests read them through a direct client.
- **Use `github.com/dlclark/regexp2`, not stdlib `regexp`, for TRaSH patterns.**
  Go's RE2 rejects 157 of the 2791 custom-format regexes (backtracking,
  lookaround). Set `IgnoreCase` and a `MatchTimeout`.
- **Quality profiles are TRaSH-only and opinionated.** Built-in profiles ship as
  embedded data; custom-format editing is deliberately not exposed.
- **Two write paths under one manager need compare-and-swap.** squasharr writes
  `TranscodeJob.status` from its reconciler and from its results consumer. Both go through
  `writeStatus`/`patchCAS`, which carry the read `resourceVersion`, so a race is a Conflict and
  is redone from a fresh read, not a silent rollback. Any new status writer must use the same
  path.
- **A running Job's template is re-sent, not re-rendered.** An SSA manager that stops sending
  a template field releases it, and a released field on a running Job is a rejected write. So
  `app/squash/controller/pool.Render` re-sends the template recorded in
  `squasharr.clustarr.io/applied-template`, and judges drift against that annotation rather
  than the stored template, which carries apiserver defaults.

## Code conventions

- GPL-3.0 header from `hack/boilerplate.go.txt` on every Go file.
- Logging is `slog` carried through `context` (`pkg/obs/logging.FromContext`). No
  package-level logger, no logger struct fields. controller-runtime gets a
  `logr.FromSlogHandler` bridge at startup.
- Spans wrap every `Reconcile`, work handler, outbound provider call and ffmpeg
  run; propagation rides the `Clustarr-Trace` header already in `pkg/events`.
- Prometheus metrics use the `clustarr_` prefix and base units. **Never label by
  title, path or release name** — unbounded cardinality.
- Tests: table-driven, testify, fixtures under `test/data/`. Pure functions where
  the logic is tricky (`pkg/pipeline.Project`, `pkg/transcode.Plan`) so it is
  testable without a cluster.

### Conventions across `pkg/`

- **The caller owns rate limiting.** A library package accepts an injected
  limiter (`torznab.WithRateLimit`; the metadata, OpenSubtitles.com and
  Gestdown clients' `Config.Limiter`) and never defaults one on; the
  controller holds one limiter per host. `pkg/subtitles/providers/
  opensubtitlescom` shipped in Phase F with exactly the violation this rule
  warns against -- `Config.Limiter == nil` silently got `rate.NewLimiter(5,
  5)` -- so a caller with no Limiter configured got a private 5 req/s
  allowance per `Provider` instance instead of the shared budget captionarr's
  KV token bucket (`app/caption/throttle`) provides; fixed under ruling R3,
  alongside giving `providers/gestdown` the injection point it had never had.
- **Every HTTP response body is read through a cap** — a package-level max, an
  `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel.
- **Provider errors expose sentinels** (`metadata.ErrNotFound`,
  `subtitles.ErrNotFound`, …) so `errors.Is` works through `Unwrap`.
- **Filesystem writes that must survive a crash go through `pkg/fsops`**
  (`AtomicWrite` fsyncs the file and its parent and takes an explicit mode).

## Status

Pre-alpha, and it reconciles: every service — `catalogarr`, `importarr`,
`indexarr`, `grabarr`, `squasharr`, `captionarr` and `ui` — registers every
controller, worker and server behind its role flags, and `clustarr all` still
stands every service up in one process (controller roles only for `grabarr`
and `squasharr`, so it never downloads or transcodes; `captionarr` runs its
fetch worker too since the gap fixes, and `--ui-bind-address` sets the ui's
address).

M0 (done): the 29 CRDs across five groups, `pkg/events` (NATS and in-memory
behind one contract suite), `pkg/k8s`, the binary, manifests, chart and images.

Phase A (done): `pkg/obs` (`logging`, `tracing`, `metrics`, `obs.Bootstrap`),
`docs/observability.md`, the `LibraryScan` kind and `importarr` skeleton,
`pkg/pipeline` and the `ui` skeleton; all services wired into the binary.

Phase B (done): the library layer, thirteen pure-Go packages (only
`api/common/v1alpha1` shared types; `pkg/quality` alone reads catalog CRD
types). `release` (rls-based parser for every kind, batch ranges, id tokens,
130-title corpus); `quality` (vendored TRaSH corpus, all 2,791 regexes compile
under regexp2, 66 embedded formats proven byte-identical, 13 built-in profiles,
Match/Score, FromCRD, upgrade decision); `naming` (four dialects); `mediainfo`
(two-call ffprobe, HDR/DV, ProbeHash, MovieHash); `transcode` (argv goldens,
runner, cleanup, verify); `torznab`/`newznab`; `cardigann` (v11, 25 filters,
five logins, HTML/JSON/XML); `subtitles` (Bazarr scoring, cue-aware
post-processing, OpenSubtitles, Gestdown, embedded); `metadata` (six clients);
`importlist` (five lists, Dedupe, ApplySyncLevel); `fsops`/`ratelimit`.
Fixtures under `test/data/<pkg>/`, no network in tests, ffmpeg tests skip
without it.

Phase C (done): M1 catalog core and library rescan — the first phase that
reconciles. `catalogarr` controllers for Movie, Series, Episode, MediaFile,
RootFolder, QualityProfile (a `Bootstrap` runnable seeds the 13 TRaSH
built-ins), DelayProfile, MetadataProvider and Search; the metadata gateway
(registry over Phase B's clients, RPC plus work queue, L1 memory + L2 NATS KV);
`pkg/decision` (`Evaluate`/`Rank`) over `quality.Profile`; the search, grab (KV
lease, delay profiles) and RSS-matcher workers and the wanted cron. `importarr`
controllers for LibraryScan, the RootFolder schedule and ImportExclusion, plus
the rescan worker under the never-guess rule. Traces now cross the bus in
production (`pkg/events` hooks; every `run.go` passes
`k8s.WithBusHooks(obs.BusHooks())` to `k8s.ConnectBus`, AST-guarded); RBAC
generates from controller markers, with a byte-for-byte test holding the
chart's copy to it; readiness is per-service and runs on every replica, not
only the leader; `test/e2e`, `config/e2e`, the fixture image and `hack/e2e.sh`
land scenarios 5, 7 and 8 on kind.

Phase D1 (done): M2 indexers — `indexarr` controllers for `Indexer`,
`IndexerDefinition` and `IndexerProxy`; the caps probe with its 12-hour
refresh (`capsTTL`, `app/indexer/controller/indexer/controller.go`); the
health/backoff escalation ladder in `app/indexer/status/health.go` (Prowlarr's
ten-rung `EscalationBackOff` periods, 0s to 24h, with a 15-minute startup
grace); the SQLite FTS5 release index (`pkg/relindex`, external-content FTS5,
pure-Go `modernc.org/sqlite`, no cgo); the federated search
(`app/indexer/search`) with its dedupe (infohash, then per-indexer
`(indexer, guid)`) and the KV-backed query-limit window
(`clustarr-indexer-limits`); the RSS poll worker and release firehose
(`app/indexer/worker/rss`); the three RPC verbs `rpc.indexarr.search|download|query`
that make `app/catalog/worker/search`'s calls reach a real server for the first
time; `app/indexer/status` as the single declaration of each field manager's
owned set (`ControllerFields`/`WorkerFields`). E2E scenario 17
(`test/e2e/indexer_test.go`, against the Torznab fixture under
`test/fixtures/torznabstub/`) is written — Indexer health and caps, ranked
federated search, the release firehose, failure backoff — but has **never
been executed against a kind cluster**; that and `make e2e` generally are
deferred by explicit user instruction — first until D1–D3 were in, then
through E–G to Phase H — not by oversight. Reconciles against envtest; not
proven end to end.

Phase D2 (done): M3 downloads and import — `grabarr` and `importarr`'s
file-import worker. `pkg/download/torrent` over `anacrolix/torrent`
(idempotent `Add` on the infohash, `Resume`/`SetSeedCriteria` no-ops on an
already-satisfied state, anacrolix state mapped onto `download.Status`);
`pkg/download/usenet` over `Tensai75/nntp` plus pure-Go `javi11/rapidyenc` —
`javi11/nntppool` was ruled out as cgo-only (R9), so this package writes its
own bounded connection pool, 430-failover across configured servers,
pipelining and quota accounting, then PAR2-verifies/repairs (shelling out to
`par2cmdline-turbo`, no Go module) and unpacks with
`github.com/nwaples/rardecode/v2`. `app/grab/status` declares the two disjoint
field-manager sets on `Download.status` — `ControllerFields`
(`k8s.ManagerGrabarr`, nine fields) and `EngineFields`
(`k8s.ManagerGrabarrEngine`, twenty-three then, twenty-five since gap fix
Y2 added `engineFailureReason` and `seedGoalReached`, twenty-six since Z1
added `healthPaused`) — and `Patch` refuses any other
manager. `app/grab/controller/downloadclient` reconciles `DownloadClient` into
its engine workload (a `StatefulSet` per torrent client, a `Deployment` for
usenet), `DiskSpaceOK` and the blocklist sweep.
`app/grab/controller/download` picks a `ClientRef`, waits for `EngineReady`,
pins the choice into `status.engine` once and never recomputes it, and runs
the `removeDataOnDelete` finalizer. `app/grab/engine/torrent` and
`app/grab/engine/usenet` gate readiness on re-attach completing first (R4) and
write only `EngineFields` telemetry; each carries a level-driven `Reaper`
(`torrent.Reaper`, `usenet.Reaper`) that lists client transfers against
owned Downloads and removes orphans past a grace period, proved by deleting a
`Download` with the engine's watch deliberately not firing.
`app/import/worker/fileimport` consumes `ConsumerImportFile`
(`"importarr-fileimport"`) and creates `MediaFile`, writing only
`MediaFileSpec` per the spec/status split; it also settled the
`Download.status.import` ownership question three comments disagreed on,
onto `k8s.ManagerImportarr`. D2-4, D2-5 and D2-6 could not advance
`status.phase` past `Assigned` — it is `ControllerFields`, not something an
engine can write — so `grep -rn WorkFileImportSubject` found the subject
builder and the consumer but no publisher, and the phase→import handoff that
is D2's own gate could not complete until D2-8a
(`app/grab/controller/download`) added phase derivation from engine telemetry
(`Assigned`→`Queued`→`Downloading`→`Completed`→`Seeding`→`Imported`, plus
`Paused`/`Failed`/`Blocklisted`/`Removing`) and published exactly one
`schema.ImportTask` per completion. Separately, D2-8 found the grabarr
reconcilers, both engines, both reapers and the file-import worker
registered nowhere and wired them into `app/grab/run.go` and
`cmd/clustarr`, added `grabarr` to the registration guard's
`runnableServices`, and regenerated RBAC. A CEL defect found along the way
was Critical: the `Download` release-identity rule
(`api/download/v1alpha1/download_types.go`) compared five `+optional` fields
unguarded, so an absent one raised "no such key" and the apiserver rejected
the write outright — fatal for usenet, which never carries `infoHash` — for
every write after creation, including the status-only applies that are
grabarr's only way to report progress; fixed in `c5e86d5` by guarding every
comparison with `has()`, with `pkg/crdcheck/download_cel_test.go` as the
regression guard using a dynamic client so an absent `infoHash` stays absent
on the wire. E2E scenarios (`test/e2e/download_test.go`,
`test/e2e/import_test.go`: 1 through import, 2, 3, 4, 6) are **written and
never executed**, deferred by user instruction to Phase H.
Reconciles against envtest; not proven end to end.

Phase D3 (done): the first UI slice — the pipeline and downloads pages over
real cluster state, streamed. `ui/reader.go`'s `NewClusterReader` builds a
standalone controller-runtime `cache.Cache` (no manager, no metrics or
leader-election port); `Options.Reader` staying nil is still legal and renders
empty pages. `/readyz` now gates on cache sync while `/healthz` stays
unconditional, and `config/rbac/ui_role.yaml` is a hand-written
`get,list,watch`-only role (no `*/status`) bound to the previously-unbound
`ServiceAccount ui` (at D3; Phase G adds exactly `ui/actions`' grants).
`ui/projection`'s `Projection` replaces the old per-connection 5s poll with
one process-wide tick that lists `Download`, `TranscodeJob`,
`SubtitleRequest`, `Search` and `MediaFile`, indexes them by owner through
`metav1.OwnerReference` UID, projects `pipeline.Entry` and
`downloadv1.Download` rows, and fans both out through `Subscribe`/
`SubscribeDownloads` to every open SSE connection. `ui/views/downloads.templ`
renders all eleven `DownloadPhase` values plus a `DownloadClient` section
without panicking on a zero-value status; `GET /downloads` and
`GET /events/downloads` join the existing pipeline page and stream. The
never-writes invariant (CLAUDE.md) is now two enforced guards rather than an
accident of no RBAC grant existing (D3-4, `6597877`): `ui/guard_test.go`'s
`TestUINeverWrites` is an AST guard over every non-test file in `ui/`
rejecting a `client.Writer` selector (`Create`, `Update`, `Patch`, `Delete`,
`DeleteAllOf`, `Status`) by method name on any receiver -- it is syntactic,
not type-checked, so it over-matches rather than tracing types back to
controller-runtime's `client` package -- plus any import of `pkg/k8s`;
`cmd/clustarr/ui_rbac_test.go`'s `TestUIRoleGrantsOnlyReadsAndActionWrites`
(named `TestUIRoleGrantsOnlyReadVerbs` until Phase G ruling R2 narrowed both
guards to admit `ui/actions`' writes) and `TestUIRoleChartMatchesConfig`
asserted `ui_role.yaml`'s verbs were a subset of `{get,list,watch}` with no
`/status` resource, and hold the chart's copy byte-identical to it (Phase G
below narrows both). E2E scenario 14 (`test/e2e/ui_test.go`: the pipeline and
downloads pages) is **written and never executed**, deferred the same way.
Reconciles against envtest; not proven end to end.

Phase E (done): M4 transcode — `squasharr`. `app/squash/status` declares the
disjoint sets on `TranscodeJob.status` — `ControllerFields`
(`k8s.ManagerSquasharr`) and a worker-owned field set for `progress`,
`result` and `stderrTail` — plus `ProfileFields`/`PatchProfile`, and `Patch`
refuses any other manager (the transcode-worker-pools plan later retired the
worker manager; see below). `app/squash/controller/transcodeprofile`
computes `status.hash` through `app/squash/worker.ProfileSpec`, the same
conversion the worker renders from
(`TestStatusHashChangesWithEveryRenderField` fails if a render field stops
moving it), marks the loser of two overlapping selectors with an `Overlap`
condition, and creates one TranscodeJob per selected MediaFile not already
tagged `CLUSTARR_PROFILE=<profile>@<hash>`, named `<mediafile>-<hash[:8]>` so
a re-run creates nothing. `app/squash/controller/transcodejob` plans from the
MediaFile's stored probe (a Dolby Vision `reject` or a container change lands
as `Skipped` with a reason, rulings R1 and R8), creates a suspended `batch/v1`
Job whose `podFailurePolicy` fails on exit codes 3 and 4 and ignores
`DisruptionTarget`, unsuspends through the pure `Admit` slot scheduler, and
owns the transcode metrics, observed once per terminal transition (R9).
`app/squash/worker` runs in the Job pod: re-probe and refuse a changed source
(exit 3), encode, verify (exit 4), then hard-link the original into the
recycle bin (`fsops.RecycleLink`) and rename the output over the source, so
the library path is never empty; a retry after a crash past the swap finds the
`CLUSTARR_PROFILE` tag and exits 0.
`TestSquasharrWorkerExitCodeReachesTheProcess` runs the real `main` to prove
the exit codes survive; at Phase E the Job ran as its own `squasharr-worker`
ServiceAccount with a generated role
(`config/rbac/squasharr_worker_role.yaml`); `--worker-image`,
`--worker-image-cuda` and `--data-claim` are threaded through config and
chart. (The gap fixes reversed two of Phase E's rulings: a container change
and `replaceSource: false` are transcoded now, ruling R-11, below. The
transcode-worker-pools plan, 2026-09-23/24, replaced this per-TranscodeJob
Job with one long-lived pool Job per (TranscodeProfile, hardware class): see
`docs/superpowers/plans/2026-09-23-transcode-worker-pools.md` and design spec
§6.4/ADR-0009. The `squasharr-worker` ServiceAccount and role above are gone;
the worker is now the separate, credential-less `cmd/squasharr-worker`
binary.) Defects a
future reader must know: `maxOutputToSourcePercent` was an
`int32` defaulted to `1.0`, so **every real transcode would have exited 4**;
`policy.replaceSource`/`recycleBin` could not be set false from Go and
`activeDeadline`/`resources`/`scratch` never got their defaults from a Go
client (the typed-client gotcha above); `replaceSource: false` is now rejected
by CEL rather than silently ignored. E2E scenario 12
(`test/e2e/transcode_test.go`, against an HDR10 clip generated in the fixture
image; Dolby Vision skips with a named reason, since ffmpeg alone cannot
synthesise an RPU) and scenario 1's transcode leg (skipped on the `.bin`
fixture gap) are **written and never executed**, deferred by user instruction.
Reconciles against envtest, real libx265 encodes included; not proven end to
end.

Phase F (done): M5 subtitles — `captionarr`. `app/caption/status` declares each
manager's set: `RequestControllerFields`/`RequestWorkerFields` behind
`PatchRequest`, `ProfileFields`, and `ProviderFields` behind a `PatchProvider`
that refuses the worker's manager — the SubtitleProvider controller is the
only writer of provider status (ruling R2), and workers record throttle, quota
and auth into the `clustarr-provider-throttle` KV bucket instead
(`app/caption/throttle`: a per-provider token bucket shared by every replica,
keys through `events.KVKeyToken`, contract-tested against a real embedded NATS
server). `SubtitleRequest.status.items` is split per leaf between the two
managers (R4), and the split needed a liveness protocol to work at all: an
item is live exactly when the controller has given it a `nextSearchAt`
(`status.IsLive`, `LiveItemKeys`); the controller creates and withdraws items,
the worker re-sends leaves only for live items and never creates one, and
`items[].state` became optional. Before that the worker re-sent every item, so
a withdrawn language could never be removed, and only `managedFields` caught
the controller claiming the worker's `state`. `pkg/subtitles` gained the
missing-subtitle `Plan` and right-to-left `SidecarName`/`ParseSidecar`;
`pkg/lang.Normalize` maps ffprobe's ISO 639-2 onto the profiles' BCP-47 — the
third instance of the language-vocabulary class, and it also fixed TVDB's
original language. The three controllers
(`app/caption/controller/subtitleprofile`, `subtitleprovider`,
`subtitlerequest`) and the fetch worker (`app/caption/worker/fetch` over
`app/caption/providerset`, never leader-elected) are registered behind
`captionarr`'s role flags; one `providerset.Validate` now serves controller
and worker (the two copies had drifted — a Gestdown provider with a missing
Secret reported Ready), and `app/caption/datapath` gives both one `/data`
mapping. Fixed along the way: the embedded provider claimed
hash-verifiability, so embedded tracks scored 0-1 and were never taken;
OpenSubtitles.com defaulted a private 5 req/s limiter and read JSON uncapped
(R3); a forced search was absorbed by the JetStream dedup window for up to an
hour until `events.MsgIDForForcedSubtitle` gave it an ID of its own — so fetch
MsgIDs now extend spec §6.5's `<uid>/<langKey>/<probeHash>` as a prefix, a
recorded deviation; and the `captionarr-worker` KEDA `ScaledObject` counted a
consumer that does not exist, so it never scaled
(`TestCaptionarrScaledObjectCountsTheRealFetchConsumers`). E2E scenario 13
(`test/e2e/subtitle_test.go`, against `test/fixtures/opensubtitlesstub` and
`gestdownstub`, both round-tripped through the real clients) and scenario 1's
subtitle leg (skipped on the `.bin` gap) are **written and never executed**,
deferred by user instruction. Reconciles against envtest; not proven end to
end.

Phase G (done): M6 parity, import lists, non-video inventory and the rest of
the UI. **Indexers.** A Cardigann definition is one more `Client` behind
`app/indexer/controller/indexer`'s `ClientCache.For` (ruling R5), so it inherits
the fan-out's dedupe, query-limit window and backoff; a `SearchBlock.Error`
match is now a `*cardigann.SearchError` (`ErrSearchFailed`) that escalates the
indexer instead of reading as "no results" (R6); sessions live in the
`clustarr-indexer-sessions` KV bucket (`SessionKey`, real-NATS contract test)
plus an owned `<indexer>-session` Secret; `IndexerProxy` http/socks5 is
applied; `cardigann.Engine` takes an injected limiter. `app/indexer/facade`
serves the Torznab facade (`/{indexer}/api`, `/{indexer}/download`,
`/search/api`) on `--facade-bind-address` (`:9696` under `clustarr all`) and
**fails closed** without a key from `--facade-api-key-secret` (created with
one random key if absent); a Search with `spec.query` runs through
`rpc.indexarr.query`. Automatic search falls back to a title query:
`BuildSearchRequest` fills `SearchRequest.Text` from the resolved title and
`app/indexer/search` uses it only for an indexer that supports none of the
request's ids (`SearchOutcome.QueryMode` records which). That made live the
absence of any release-identity check — a text fallback could approve the
wrong film — so `pkg/decision/identity.go` now rejects `WrongItem` (ids decide
when both sides carry one; otherwise titles plus year ±1, or
season/episode/absolute/air-date) and `UnknownItem` when unevaluable, fed by
`search.MovieIdentity`/`EpisodeIdentity` at both construction sites. **Lists
and history.** `importarr` gains the ImportList controller and sync worker
(Trakt device flow with a `SecretTokenStore`, `status.auth` for the UI,
ImportExclusion respected); `catalogarr`'s `RoleHistory` starts the history
`Sink` and the `DLQProjector`, which per R1 applies a
`clustarr.io/dead-lettered` annotation under `clustarr-dlq-projector` and
emits an Event rather than writing anyone's status. **Non-video catalog.** The
metadata gateway is the sole writer of `status.metadata` on Artist, Album,
Author, Book, Audiobook and Comic; the Artist→Album, Author→Book (standalone
Book too), Comic→Issue and Audiobook controllers follow Series→Episode, and
only Comic→Issue uses `ManagerCatalogarrFanout`, since Issue, like Episode,
has no `status.metadata`. Rescan attributes non-video files to existing items
only; manual import is the `catalog.clustarr.io/import-target` and
`import-override` annotations on a Download (re-queued by
`fileimport.Retrigger`) or on a LibraryScan whose `spec.subpath` names the
file (manual assignment). **UI.** Every write lives in `ui/actions` — search
now, rescan, monitor, manual assign and eight Settings patches — as merge
patches of spec under `clustarr-ui`; `TestUINeverWrites` allows Create/Patch
there and nowhere else and bans every status write everywhere, and the role
guard holds `ui_role.yaml` to reads plus exactly `actions.Grants()`. The
library, unmatched, import-lists and settings pages ride the one projection
with no new ticker; Tailwind is a pinned standalone binary behind `make css`,
with `ui/static/app.css` committed and htmx vendored.
`TestBothUICommandsWireEveryUIOption` runs both ui commands and fails on any
unset option — the text guard it replaced missed that `/library`, `/unmatched`
and `/import-lists` rendered no rows in production. **G4-0** swept `api/` for
the typed-client defaulting trap and uncapped status lists and left two guards
behind (gotcha above). Defects found and fixed along the way: the non-video
built-in profiles listed tiers worst-first, so PDF was the best ebook and
every comic met cutoff; `pkg/fsops` knew only `.mkv` as video, so **every
`.mp4` movie was skipped**, and applied the 50 MiB sample floor to every kind
— classification is now per kind (`fsops.Classifier`), the floor is video-only
and set by `--sample-max-bytes`, and a size-suspected sample goes to
`status.unmatched` as `suspected_sample` instead of vanishing; every naming
preset left dangling separators for an empty optional token
(`"The Matrix (1999) - -RlsGrp"`, shipped in Phase C); ComicVine wants the
`4050-N` guid for a volume but a bare `N` in the issues filter, which every
fake accepted either way; Audiobook's region was hard-coded `us`; and a
re-scan erased a MediaFile's frozen import fields by re-applying
`MediaFileSpec` under the same manager. E2E scenarios 9, 10 and 11
(`test/e2e/importlist_test.go`, `cardigann_test.go`, `nonvideo_test.go`,
against `cardigannstub`, `httpproxystub`, `importliststub` and `nonvideostub`)
and the rest of 14 (`TestUILibraryImportListsSettingsAndUnmatchedPages`) are
**written and never executed**, deferred by user instruction; scenario 9 only
checked that the Trakt and Plex CRs are accepted, since neither client took a
base-URL override (the gap fixes added `--trakt-base-url`/`--plex-base-url`
and un-skipped both). Reconciles against envtest; not proven end to end.

Gap fixes (done, 2026-09-23): plan `docs/superpowers/plans/2026-09-23-gap-fixes.md`,
tasks X1-X16 in four waves — W0 every API change serially, W1a/W1b code under
strict file ownership, W2 wiring and RBAC, W3 docs — closing the carried list
except what the spec defers. **API** (X1, X15): `CutoffUnevaluated` on Movie
and Episode (an unresolved profile no longer reads `CutoffUnmet`);
`IndexerProxy.spec.port` required; `ReleaseDecision` moved to
`api/common/v1alpha1` so controller-gen generates Search's apply configuration
and the hand-written one is gone; `fieldRecording`; all nine image types;
`IssueStatus.cutoffMet`, `pendingGrab` and state `delayed`; `MediaRef.track`;
`TranscodeProfileSpec.maxConcurrent`; `hdrOffset`/`minimumSeeders`/
`cleanupDays` as pointers with `*OrDefault` accessors; `ReleaseInfo.alsoOn`;
`MovieMetadata.secondaryYear`; `replaceSource=false` admitted;
`k8s.MarkDeadLettered`/`DeadLetteredAnnotationChanged`, folded into the
`DeadLettered` condition of all sixteen annotatable kinds; ImportList CEL
admitting only the kinds its provider can yield;
`IndexerDefinition.status.replaces`. **Catalog and search:**
`status.activeDownloadRef` has one writer, the item's reconciler, derived from
its owned non-terminal Downloads (`rollup.ActiveDownload`,
`rollup.DownloadNonTerminal`) on all six grabbable kinds, and
`rollup.DownloadOverlay` reads every Download phase before Imported as
Downloading; the grab path has one source resolver shared with Search-CR grabs
(`app/catalog/worker/grab/downloads.ResolveSource`), a double-grab guard that
lists Downloads through the uncached APIReader, reclaimable and re-enterable
leases, CAS status writes, and grabs albums, books, audiobooks and issues,
which automatic search now covers too (text queries after Lidarr, Readarr and
Mylar); `decision.Identity` gains `SecondaryYear`, `FullSeason` on a
single-episode search, TheXEM scene mappings (`pkg/metadata/scenemap`) and
non-video rules; ReleaseTitle custom formats read the release or file name;
the RSS matcher keys series as Sonarr does; Series airings get a writer;
`history.Replayer` serves `clustarr.io/replay`; item, media-file, indexer,
download and transcode events all have producers. **Metadata:** eight new
clients (coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
animelists) that reach Ready, every client read through `metadata.ReadBody`/
`CappedTransport`, the real `tmdb.SearchMovies`/`musicbrainz.SearchArtists`,
`Album.Releases`, Open Library editions, ComicVine status, artwork and
resolver enrichment of `status.metadata`, Lidarr's album release selection
(`selectedReleaseID` is the Album reconciler's leaf), Kapowarr issue numbers
and injective Issue names. **Release and quality:** `release.TitleNorm` on
both sides of the release index (non-Latin search works), Radarr's
ReleaseGroupParser, MULTi is not a language, an untagged release takes the
item's original language, non-video qualities on Lidarr's music ladder,
multi-episode names per Sonarr. **Indexers:** every field the Cardigann schema
decodes, XML queried with CSS, cookie and form logins that work,
session-expiry re-login, `app/indexer/proxy` (selector, FlareSolverr, SOCKS4;
an empty selector matches nothing and ambiguity fails closed),
`minimumSeeders`, RSS pages and direct grabs counted into their windows,
`alsoOn`. **Downloads:** torrent file selection by `spec.target.keys`, usenet
priority classes, `Item.AddedAt`, and an engine finalizer so the controller
removes files only after the engine lets go. **Import:** best file first per
single-file item, episode and series-root imports, honest rescan counters,
redelivery resume, `fileimport.RecycleSweeper`, `removeAndDelete` through the
recycle bin, `automaticAdd=false`. **Transcode:** one output rule
(`app/squash/worker.OutputPath`), `status.plan` equal to the worker's argv,
per-profile `maxConcurrent`, Job pods with the Deployments' securityContext,
`--intel-render-groups`, the Intel QSV/VAAPI runtime in the media image.
**Subtitles:** SubDL and SubSource clients, Bazarr pooling, an effective
`embedded.extract`, one shared OpenSubtitles login, sidecar mode from the
RootFolder. **Wiring** (X14, X16): one generated ClusterRole per identity
(`RBAC_ROLES`), proven by a start envtest that runs every service as its own
ServiceAccount under only its role; a registration guard that derives the
services and catches bare-`Start` runnables, unadded `EveryReplica`s and
duplicate controller names; UMASK applied by the binary; engine pods with a
ServiceAccount (`--engine-service-account`), namespace, NATS URL, UMASK,
`GOMEMLIMIT` and the Deployments' securityContext; `--trakt-base-url`,
`--plex-base-url`, `--ui-bind-address`; ui `/readyz` after the first
projection round. **The Cardigann corpus** (`7b6fc4a`): the project owner
added Prowlarr's Cardigann definitions on 2026-09-23 — 752 files packed by
`hack/pack-cardigann` into `app/indexer/bundle/embedded/definitions.zip`
(`go:embed`, read through `archive/zip` as an `fs.FS`), 749 of which load;
indexarr applies them at startup (`--cardigann-bundled`, default true), and
`--cardigann-definitions-dir` (chart: `indexarr.cardigann.*`) replaces them.
**Found and fixed beyond the list:** engine pods had no ServiceAccount,
namespace or NATS address and could not have started on any cluster; grab
leases were never released, so every item could be grabbed exactly once; the
RSS title fallback had never matched a series without an id; the generic
`.torrent` fetch bypassed the Indexer's proxy, passkey included; TMDB and
ComicVine API keys leaked into error strings; every HDR10 remux with non-AAC
audio failed (`-vf` beside stream copy, exit 234) and every 8-bit QSV encode
failed (exit 218); `fsops.IsPart` missed a transcode's `<stem>.part.<ext>`;
a multi-file download gave one item several MediaFiles; `removeDataOnDelete`
was a no-op for every torrent; HTML `case` keys, XML definitions (a panic)
and cookie logins were all broken for the whole corpus; MusicBrainz release
statuses never matched; an album listing one recording twice made a status the
apiserver would have rejected; ImportList `Synced` reset once its checkpoint expired.
**Rulings:** R-1 subtitle sync, Whisper and chunked transcoding stay
spec-deferred and CEL-forced off; R-2 usenet dedupe stays per indexer; R-3 an
automatic single-episode search rejects a full-season pack (Sonarr); R-4
subtitles pool every provider, Bazarr-style; R-5 one writer of
`activeDownloadRef`, the reconciler; R-6 each engine's finalizer gates the
controller's file removal, with a 10-minute timeout for a gone engine; R-7
`SecondaryYear` exact or ±1 of `Year`; R-8 only three `omitempty` scalars
become pointers; R-9 `IndexerProxy.spec.port` required; R-10 an unyieldable
import-list kind is refused at admission; R-11 `replaceSource=false` and the
container change are implemented; R-12 confirmation-only items closed with
evidence, or fixed where the evidence said so; R-13 vendor the Cardigann corpus only under a compatible licence —
superseded by the owner's addition of the corpus above. E2E: X12c made the
download fixtures serve real media, deployed the seeder and NNTP stubs, and
wrote scenarios 15 and 16 (16's Helm and `clustarr all` legs skip); every
scenario is still **written and never executed**. Reconciles against envtest
(W2 gate green at `732c4a1`); not proven end to end.

Gap fixes Y1-Y3 (done, 2026-09-23): the four failure-handling behaviours the
spec named and nothing built (spec §4.4, §5, §8.3 now say "as built").
**Hung-handler DLQ** (Y1, `bd6227e`): natsbus watches JetStream's
`MAX_DELIVERIES` advisory per subscription, in the queue group
`clustarr-dlq-watch-<durable>`, and copies a message whose final delivery
lapsed on AckWait under the in-process path's Msg-Id, deleting a work-queue
original afterwards; membus sweeps the same case. A non-work consumer's dead
letter is named by its durable (`clustarr.dlq.catalogarr.redownload.<id>`);
before, two event consumers shared one DLQ subject. `events.KV.DeleteRevision`
(`1ab8ce2`) is a revision-checked delete (`ErrRevisionMismatch`; refuses
revision 0, which JetStream reads as unconditional). **Failure reasons, seed
goal, blocklisting** (Y2, `813145b`, `56c10c1`, `0b607c0`): engines report
through `status.engineFailureReason` (torrent `stalled` after
`spec.torrent.stallTimeout`, default 24h; `diskFull`; `writeError`; usenet
`missingArticles`, `encrypted`, `diskFull`, `writeError`, and `timeout` after
the opt-in `downloadTimeout`) and `status.seedGoalReached`; the controller
records a terminal `failureReason`, labels a release fault blocklisted before
publishing `failed`, leaves a local fault (`diskFull`, `writeError`) Failed
and unlabelled, and fires `seedGoalMet`; engines remove a Failed or
Blocklisted transfer. A server that could not be asked (bad credentials,
refused connection) is `ErrProvidersUnavailable` and retries, not
`missingArticles`. `importRejected` is read off `status.import` through
`downloadv1alpha1.ImportMessageEveryFileRejected`, one constant both services
name. **Redownload** (Y3, `dd1ea96`): the `catalogarr-redownload` durable on
`failed` and `blocklisted` frees the grab lease (`grab.FreeLeases`,
revision-checked) and publishes one `redownload` search per monitored target
under a per-Download Msg-Id, none for a local fault (Radarr raises no
DownloadFailedEvent for one) or a failure older than 24h; the grab records
`grabbedBy=redownload`. The carried items, including the owner's policy call
on `importRejected` blocklisting at once, are under *Downloads and events* in
the remaining-work plan.

Z-wave (done, 2026-09-23; Z1-Z6 plus a final pass, reports in
`.superpowers/sdd/2026-09-23-gap-fixes/`): every fixable item left open after
the gap fixes. Downloads read `healthAction` (`pause` holds a job with
`status.healthPaused` until `spec.paused` is toggled) and `removeCompleted`,
persist seed counters, blocklist `payloadMismatch`, reach a live transfer
through `download.Client.SetPriority`, and roll the engine when a start-time
setting or a usenet provider Secret's data changes (Secrets read by name with
`get` alone, no watch, so within 5 minutes). natsbus runs `MaxInFlight`
handlers, `CLUSTARR_ADVISORIES` captures MAX_DELIVERIES advisories durably,
retries wait exactly their backoff, and the never-used consumers, subjects and
search-cache bucket are pruned. Engines and the transcode worker write 1 Hz
telemetry into `clustarr-progress`. RSS matches non-video releases and
alternate titles; searches send scene numbering. The open list is the
unchecked items under "Still open after the gap fixes" in the remaining-work
plan.

M7 (done, 2026-09-24): index/artwork/ratings/Plex
(`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md`).
**A** `pkg/relindex` gains `OpenPostgres` behind the same `Store` contract
(`storetest.Run` runs both engines), selected by `--index-dsn`
(`CLUSTARR_INDEX_DSN`, ADR-0010); a DSN turns on leader election for every
indexarr controller and the retention sweep, since more than one replica may
now run; the chart and kustomize gained an optional CloudNativePG `Cluster`
(`postgres.enabled`/`config/postgres`) as a kept normal resource, never a
hook (a ruling reversing this design's own hook sentence and the plan's R2,
gotchas above), and `nack` left the chart. **B** a JetStream object store
bucket, `clustarr-artwork` (5Gi, file storage — `ForSingleNode` had to learn
every object store belongs there, not only KV and streams, gotchas above),
holds provider-fetched `original` artwork and `spec.artwork` overrides, one
writer per variant (the metadata gateway; the renderer, `catalogarr --role
artwork`); `ui` gained a read-only NATS connection and serves it at `/art`,
dropping every provider hotlink (ADR-0011); both installers give the ui
`NATS_URL` (the first cut did not, so every image request hung — the
`TestEveryNATSDialingDeploymentCarriesNATSURL` guard). **C**
`status.metadata.ratings` from TMDB for **movies only** — series carry no
ratings in M7: TMDB declares its source for movies only, TVDB (the series
provider) supplies none, and MDBList and OMDb, declared as fallthrough, were
not built (ruling R5: no API keys at hand; both `MetadataProviderType`s
report `Ready=False, InvalidSpec` until they are), so series stay unrated
until TMDB TV ratings (a recorded `/tv/{id}` fixture) or MDBList/OMDb land;
`OverlayProfile` and `pkg/overlay` composite Kometa-style rating badges
onto posters, rendered by the same `--role artwork` worker for Movie and
Series only, bounded to 2 concurrent renders by default and drawn at most
2000px wide (`MaxRenderWidth`). **D** `ui/plex` serves Plex's Custom
Metadata Provider protocol at `/plex/movies` and `/plex/tv` (`--plex-provider`,
`--external-url`/`CLUSTARR_EXTERNAL_URL`; ADR-0012), read-only over the
existing projection cache (one index build per 5 s, `projection.IndexTTL`),
gated 503 with no external URL configured.
Reconciles against envtest, with real Postgres (`make pg-assets`) and real
NATS object-store round trips included; e2e scenario 18
(`test/e2e/plex_test.go`) is written and, like every scenario since Phase C,
has never run on kind — its thumb-fetch leg skips by name rather than
asserting, since `config/e2e` has no egress and no in-cluster fixture serves
image bytes yet. Every deferred item the build surfaced is under "M7 carried
items" in `docs/superpowers/plans/2026-09-18-remaining-work.md`.

Next: Phases D through G, the gap fixes and M7 are done (M2-M7) → **Phase H:
end-to-end proof on kind** is next. Only Phase C's scenarios (5, 7 and 8)
have ever run on kind; every scenario written since — 1-4, 6, 9-18 — never
has. Phase H runs them, builds the second deploy path scenario 16's Helm and
`clustarr all` legs need, extends scenario 1's trace check to all four
services, and owns the open items left under "Carried defects" in
`docs/superpowers/plans/2026-09-18-remaining-work.md`. Milestone detail is in
the spec's §16 and amendment §A4. The unified-manager topology (one
manager Deployment for every reconciler, domain workers with their own
ServiceAccounts) is designed in
`docs/superpowers/specs/2026-09-24-unified-manager-design.md` and deferred
by ADR-0013 until the system is production ready; keep landing controllers
in their service's Deployment until then.

**Nothing is finished until it is proven end to end on a kind cluster.** Every
phase from C onward lands its milestone's scenarios in `test/e2e` (real CRs,
real controllers, real NATS, real files under `/data`, in-cluster fixture
services, no Internet) and keeps `hack/e2e.sh` green; Phase H audits that every
scenario in the plan exists and passes. Unit, envtest and build-tagged
integration suites do not substitute for it.
