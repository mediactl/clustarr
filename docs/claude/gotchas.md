# Clustarr record: Gotchas found the hard way

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Gotchas found the hard way

- **The library store (ADR-0024, 2026-11-26): six kinds are rows in Postgres
  and the apiserver serves none of them.** MediaFile, Episode, Album, Book,
  Audiobook and Issue. What goes wrong, and why:
  - **"no matches for kind Episode" / "the server could not find the
    requested resource"** means something read a library kind from the
    apiserver: a `ctrl.NewManager` or `client.New` that was not wired
    (`pkg/library/ctrlwire`; `test/librarytest.NewManager`/`NewClient` in a
    test), `mgr.GetAPIReader()` taken before `k8s.LibraryManager` wrapped the
    manager, a client-go or dynamic client, or `kubectl`. A cache constructor
    that builds its own informers needs the wiring's `Source`
    (`rowcache.Options.Source`): `internal/cli/manager.libraryOptions` is the
    one place that knows.
  - **The differential tests are the authority on what a write does**
    (`pkg/library/differential*_test.go`: the same writes to a real apiserver
    and to the store). They found five things no reading of the code had:
    the status endpoint ignores `metadata` as well as `spec` (a status update
    that drops a label releases no ownership); a null where the schema takes
    none is dropped from an update or merge patch (a typed object's zero
    `metav1.Time` encodes as `null`: `spec.modTime`) and is refused in an
    applied configuration; metadata passes through ObjectMeta, so the last
    annotation removed by a JSON patch leaves no `annotations` at all; an
    applied configuration is read as YAML, which refuses a raw C1 control
    character that a patch (JSON) takes -- the reason
    `mediafilestatus.Render` scrubs them; and a read of the store itself
    must answer `managedFields`, which tests of field ownership read.
  - **A JSON patch (RFC 6902) is served** beside a merge patch and an apply:
    the metadata refresher and the history replay consume an annotation with
    a `test` and a `remove`, on a `PartialObjectMetadata`. Without it each
    pass failed and retried (41 passes in three seconds in
    `TestOwnStatusWritesNeverEnqueueTheFile`). A strategic merge patch is not.
  - **An envtest of a library kind needs `CLUSTARR_PG_ASSETS`** beside
    `KUBEBUILDER_ASSETS`. One embedded Postgres per test binary, lazily
    (`test/librarytest`), stopped by a watchdog process when the binary
    exits; a `postgres` left running after a crashed run is that watchdog
    not yet woken, not a leak to chase.
  - **A test on a fake HTTP server or an unreachable config keeps
    `client.New` / `ctrl.NewManager`** (`pkg/k8s/rowcache/cbor_test.go`,
    `internal/cli/manager/probes_internal_test.go`): `librarytest`'s would
    start a database for a test that has no library.
  - **A deleted parent's children go at the collector's next sweep** (30 s),
    not at once, and only once every owner is gone; envtest never collected
    at all (it runs no controller manager), so no test had leaned on it.
  - **`make manifests` moves the six CRD files to `pkg/library/schemas/`.** A
    session that regenerates and sees them "deleted" from `config/crd/bases`
    must not restore them. The ui's ClusterRole and the chart's `rbac.yaml`
    are hand-written and carry no rule for them either.
  - **The store's default grants nothing to anyone but the app role**: the
    column grant that keeps the ui off status exists only when
    `library.ui.role` names a role the operator made.

- **`make test` cannot pass in a fresh clone or a new git worktree until
  `helm dependency build` has run.** `charts/clustarr/charts/` holds two
  vendored dependency tarballs (`nats`, `cloudnative-pg`) and is gitignored by
  `charts/clustarr/.gitignore`, so a clean checkout does not have them.
  `TestChartAndKustomizeAgreePerComponent` shells out to real `helm template`,
  which refuses with "found in Chart.yaml, but missing in charts/ directory"
  — and it reads as a chart/kustomize *disagreement*, which is a code defect,
  rather than as a missing local artifact. This cost a false red on a merge
  gate run in a throwaway worktree; the branch was fine. `make test` runs
  `make chart-deps` first; by hand, `helm dependency build charts/clustarr`.
- **`go test ./...` passing does not mean the CRDs are valid.** `pkg/crdcheck`
  and the envtest suites skip silently when `KUBEBUILDER_ASSETS` is unset. Only
  `make test` (or exporting the assets path) compiles the CEL rules against a real
  apiserver. A suite that finishes in milliseconds skipped.
- **controller-gen v0.22.0's `applyconfiguration` generator ignores output rules**
  — it writes next to the types regardless of `output:...:dir`, at each group's
  `+kubebuilder:ac:output:package` plus `<group>/<version>`, so it nests every
  group under its own name. `make generate` runs `hack/flatten-applyconfiguration.sh`
  afterwards, which moves each version up to `api/applyconfiguration/<group>/<version>`
  and rewrites the imports. Do not point the groups at a shared output root: they
  would share one `internal/internal.go`, the last group's schema would overwrite
  the others', and `Extract*` would fail at runtime for every other group.
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
    `catalog-worker`, so the grab released `status.metadata` — the movie
    dropped to `Phase=Pending` and the gateway refetched, on every grab. Give
    each writer its own manager (as `catalog-series` does) rather than
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
    write (the Download kind is gone since ADR-0019; the lesson stands).
    G1-3's unmonitor path sent only `spec.monitored` under the manager
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
  immediately before the apply. The RSS poll did this ("the poll closes the
  window") until ADR-0019 moved the Indexer's observed fields into the manager;
  `refreshWorkerFields` (`app/indexer/controller/indexer/controller_definition.go`)
  re-reads the Indexer after a slow network operation, and the health ladder
  applies as a compare-and-swap from a fresh read.
- **Server-side apply tracks ownership per leaf inside a struct**, not for the
  sub-object as a whole. A renderer that sent only `caps.modes` released
  `caps.limitsMax`, `caps.limitsDefault`, `caps.supportsRawSearch` and
  `caps.categories` on every apply that did not re-probe — and the reconciler
  re-applies every 15 minutes while re-probing every 12 hours, so four of five
  fields zeroed within one tick of the probe that set them. The envtest that
  should have caught it asserted only that `status.caps` was non-nil, which a
  modes-only renderer satisfies. Assert every leaf, not the parent.
- **A server-side apply that changes nothing still costs the apiserver a
  whole apply, and a pass that only waits still sends one** (talos,
  2026-11-26). An Album refused a `catalog-metadata` slot asked again every
  `dispatch.TickInterval` (10 s) with a whole item pass, each ending in its
  status apply: ~580 Albums, ~40 `patch albums/status` a second for four
  hours, 80% of the requests on one apiserver and 3.8 of its node's 4 cores,
  while the object's managedFields time stood still. The wave itself came
  from the Albums' refresh: skipped as idle at a manager start they had no
  wake when their document came due, so all 6,884 read stale at the first
  start after it. Three fixes, each with its test:
  - `itemstatus.Apply` keeps an apply back when the pass remembers
    (`itempass.Applies`, `remediation.ApplyMemo`) that the same declaration
    left the item at the resourceVersion it is read at. That is the one case
    an item apply is known a no-op without managedFields; a comparison with
    the cached item is still never one. Kept applies count as `unchanged` in
    `clustarr_remediation_passes_total`.
    `TestAnItemApplyThatWouldChangeNothingIsNotSent` (envtest),
    `TestApplyMemoKnowsAnItemOnlyAsItsOwnApplyLeftIt`.
  - A Budget refusal on a durable that never wakes its waiters (no Source)
    answers `dispatch.UnwokenRetry`, about a minute spread by key
    (`TestABudgetRefusalOnASearchLaneWaitsForAWake`).
  - An Album is stale only `metadata.RefreshSpread` past its ttl, its pass
    requeues for then (`album.RefreshIn`), and one its watch leaves out of a
    start is enqueued for then too (`rollup.Watch.IdleWake`):
    `TestAlbumsRefreshedTogetherComeDueApart`,
    `TestAnItemLeftOutOfTheInitialListIsWokenWhenItsWatchSaysSo`. The spread
    itself had never been more than 4.3 s: a 32-bit hash taken as
    nanoseconds (`TestRefreshSpreadFillsASixteenthOfTheTTL`), so Movie's and
    Series' wakes since 2026-11-17 were not spread either.
  Not done: Book and Audiobook are skipped at a start the same way and have
  no refresh wake (hundreds of items, no wave; their documents wait for a
  start after they are due); Artist, Author and Comic read stale on the bare
  ttl.

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
  default to nil (`app/transcode/jobspec.ReplaceSource`,
  `Revision.EffectiveVersion`), or a floor in code, documented on the field,
  where zero means nothing (`Indexer.spec.timeout`). `pkg/crdcheck`'s
  `TestNoCRDDefaultIsUnreachableFromGo` and `TestEveryStatusListIsCapped`
  guard the sweep.
- **A pod a controller builds at runtime escapes every guard the installers
  have.** The chart and `config/manager` are held to each other, to the
  generated roles and to a securityContext by tests; the pod specs grab and
  transcode build in Go were held to nothing. Transcode Job pods had no
  securityContext, UMASK or tracing flags; engine pods had no ServiceAccount
  (so they ran as `default`, bound to nothing), no `POD_NAMESPACE`, no NATS
  address, no UMASK, no `GOMEMLIMIT` and no securityContext — they could not
  have started on any cluster, and every envtest passed, because envtest
  enforces no RBAC and runs no pods. The KEDA ScaledJob example ran the wrong
  ServiceAccount without `--job` (KEDA left with the split, 2026-10-06). Fix the
  class, not the instance: test the rendered pod spec against what the installer
  creates and binds (`TestGrabEnginesRunAsAnAccountTheInstallerBinds`,
  `TestEnginePodSecurityMatchesTheDeployment`, and for the native image
  `TestNativeImageIsWhatTheManagerStampsOntoPoolsAndEngines`†), and run each
  identity under its real role (the RBAC-enforced start envtests of
  `cmd/manager`, `cmd/agent` and `cmd/ui`).
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
  next delivery attempt, and that needs a waiting pull request.** nats-server's
  `deliveryCount` returns redeliveries, so the ack-timer path (`checkPending`)
  catches a lapsed final delivery only for `MaxDeliver` 1; for 2 or more,
  `getNextMsg` raises it on the next waiting pull from any replica
  (`consumer.go:4841-4849, 2427-2457, 6187-6192`). A pull a stopping pod abandons
  is pruned by the next `CONSUMER.INFO` (the manager's queue gauge, every 30 s)
  and is never relied on. A subscription fetches only for free slots and never
  parks (Wave 4c's parking spent an attempt per redelivery and dead-lettered
  healthy tasks it never ran, E11), so nothing keeps a pull open at saturation;
  the dead-letter watcher (`busconn.WatchDeadLetters`, the manager's) is the
  backstop, and membus's sweep dead-letters a lapsed final delivery in place.
  Before Wave 4f a `Consume` callback that blocked issued no pull, and a test of
  the hung case once needed `MaxInFlight = MaxDeliver+1` to see the advisory at
  all.
- **On a consumer with `BackOff`, nats-server adds `BackOff[n-1] -
  BackOff[0]` to every delayed nak.** `processNak` stamps the entry
  `now - AckWait + d`, `checkPending` redelivers once it is `BackOff[n-1]`
  old, and `BackOff` overrides AckWait with `BackOff[0]`. A retry that asked
  for `BackOff[n-1]` therefore waited nearly twice that — about 2h, not 1h,
  on `catalog-search-normal`'s fourth attempt — while membus, which has no
  such arithmetic, waited exactly `d`, so every test on the in-memory bus
  passed. natsbus now naks for `d - (BackOff[n-1] - BackOff[0])`
  (`natsbus.nakDelay`), and the contract test measures the real gap between
  deliveries on both buses.
- **`BackOff` also times a first delivery at `BackOff[0]`, so a handler
  slower than that is redelivered mid-run unless something sends
  InProgress.** The engine durable (`BackOff[0]` 10 s, no
  `HandlerTimeout`) ran payload RPCs that took about 15 s while nzb.life
  answered 522 (2026-10-15): the broker handed a second copy out mid-run,
  which fetched the payload again, and lapsed copies, served before new
  work, kept 1,153 commands undelivered, the removals of 30 GB among them.
  A durable whose handler can outlast `BackOff[0]` takes a
  `HandlerTimeout`, which makes the bus heartbeat it, or heartbeats its own
  work as the transcode pool worker does; `TestOnlyExplicitBudgetsAreEnforced`
  holds the list.
- **On single-node NATS a work stream is a few MiB of memory with
  discard-oldest, and a feature that queues by the thousand silently drops
  its neighbours' work.** `ForSingleNode` scales every stream into a 64 MiB
  memory budget (catalog's 1 GiB became 7.2 MiB), and `AllowMsgSchedules`
  forces `DiscardOld`. On 2026-10-01 TheIntroDB's tasks rescheduled to the
  allowance reset (5,028) plus segment detection's analysis tasks filled
  `CLUSTARR_WORK_CATALOG`, and 12,161 messages -- catalog's own
  searches, grabs and metadata work among them -- were discarded without an
  error; the analysis queue's ack floor read as 842 tasks done for 7
  results. Such work gets its own stream marked `StreamSpec.Durable`
  (file-backed at full size, outside the memory budget):
  `CLUSTARR_WORK_MARKERS`, 256 MiB, `clustarr.work.markers.>`.
- **A query's wait for its own rate limiter counted against the indexer's
  timeout, and the wanted sweep searched every item live at once.** On
  2026-10-06 the sweep published 104 searches in one second; index
  (answering RPCs concurrently since 8638117b) queued all of them on
  nzbgeek's 2 s `requestDelay`, and the fan-out's per-query deadline --
  started before the wait -- expired for every query past the fifteenth or
  so, which were recorded as nzbgeek timing out and disabled it (an earlier
  sweep had grabbed 73 releases in one hour). Two fixes:
  `torznab.Client.WaitTurn` takes the request slot inside the search's
  budget and `spec.timeout` starts after it, a query that gets no slot is
  the `paced` skip, never a failure (`app/indexer/search/pacing_test.go`);
  and an item searched before is searched `SearchTask.IndexOnly`, which the
  search worker answers from the index agent's local release index
  (`clustarr.rpc.index.query`, every release the RSS feeds delivered, no
  indexer query) -- an item never searched gets one live search, and Add New,
  "Search now" and redownloads stay live. Since ADR-0019 the item key's search
  planner decides this (`app/catalog/searchplan`), and every live search takes
  its namespace's pace slot first (`--search-live-per-minute`, 20 by default)
  and is admitted through the dispatch ledger. A pool of searches must never be
  fired at an indexer in one burst. A
  season search (Wave S) is one paced query per season per indexer,
  never one per episode: it counts as each wanted episode's search (the
  Episode folds its season's answer into `lastSearchedAt` once, so its
  next search is IndexOnly) and an Episode waits while its season is
  queued or searching; nothing publishes per-episode searches for it.
  **The catalog agent runs at most `--live-searches` (4) live searches at
  once** (owner, 2026-11-10, `search.liveGate`; a person's search first):
  every live search queues on each indexer's `requestDelay`, and the two
  search lanes' 16 slots had queued 16 deep, 6-20 s a query on talos for
  answers of 0.2-0.3 s; the agent never scales below one (it had
  cold-started 20 times in 30 hours, and the searches queued meanwhile
  reached the indexers at once); and a live search heartbeats its delivery
  from the moment it waits (`searchLive`), since the search durables' first
  `BackOff` step, 30 s, is the delivery's deadline and a search may take
  45 s (`SearchDeadline`), so a long one would be delivered again. The
  usenet API indexers run at `requestDelay: 500ms` on talos.
  `clustarr_indexer_query_duration_seconds` is the request alone for a
  search or an RSS page, and `clustarr_indexer_query_wait_seconds` the wait
  before it (a search outcome's `elapsedMillis` too): as one number the
  queue read as a slow indexer.
  **The backlog is searched live again only through a BacklogSearch**
  (owner, 2026-11-10: "our own version" of a scheduled backlog searcher;
  `docs/superpowers/specs/2026-11-10-backlog-search-design.md`): an
  IndexOnly item never finds a release posted before the RSS capture, so a
  namespaced `BacklogSearch` (catalog.clustarr.io, one per namespace, the
  oldest wins) picks every `interval` (15m) the wanted items searched
  before whose `status.lastLiveSearchAt` (new on the six searchable kinds:
  stamped at every live video dispatch and at a season answer's fold) is
  past the queue's cooldown (missing 7 d, cutoff-unmet 14 d), oldest first,
  up to each queue's `perCycle` (5, 1) and `hourlyLimit` (20, counted from
  `status.recent` after a leader change). It lists them through
  `itemread.BacklogIndex`, grants each a live search in the leader's
  `backlog.Book` and wakes its key; the search planner then dispatches it
  live past its backoff (`View.Backlog`), behind the pace and the ledger.
  Two or more due episodes of a season are one unit: the Series' automatic
  season search asks for the season (`SeasonFacts.BacklogDue`) and they wait
  for it (`View.SeasonBacklog`). A grant is spent at dispatch (an all-paced
  backlog search loses its turn) and lapses after `max(2 x interval, 3h)`.
  **A release date is a day, and three things followed from it** (owner,
  2026-11-17: "Do we check for new episodes or follow release dates ...
  Upon release, we should search", then "Implement the fixes"):
  - **A Series' and a Movie's pass is owed again when its document is due**
    (`metadata.RefreshWake`: the ttl plus up to a sixteenth of it, by UID,
    so items refreshed together at a manager start drift apart; while a
    refresh is asked for and unanswered, `metadata.RefreshRetry`, 65 min,
    past the work stream's duplicate window). Staleness was only ever read
    when something else woke the item -- a manager start or the series'
    next airing -- so in an 11-hour stretch without a restart talos ran 1-2
    refreshes a half hour and the restart after it 1,170, and a new season
    of a continuing series with no airing listed (121 of 149) was found at
    the next restart. A stale item still reads Pending until answered. The
    other kinds' passes are not owed that way yet.
  - **A wanted Episode or Movie gets one more live search
    `searchplan.PostReleaseDelay` (36 h) after its date**
    (`View.ReleasedAt`: the air date, a Movie's `status.availableAt`).
    TheTVDB's and TMDB's dates are days, read as 00:00 UTC, so the one live
    search the date makes runs up to a day before the episode airs; after
    it only the RSS feeds and the release index were asked, and a release
    they missed waited for a BacklogSearch's cooldown. Only within
    `PostReleaseWindow` (7 days) of the date, never for an item added with
    searchForMissing false, behind the pace, the ledger and its season; the
    plan's `Due` is the only thing that wakes the item for it.
  - **A film whose chosen country lists no theatrical, digital or physical
    release takes each type's earliest date in any country, else TMDB's own
    release date as its cinema date** (`metadata.DeriveRegionalReleases`,
    `tmdb.mapMovie`). Cure (1997) has a US festival premiere and 21 dated
    releases elsewhere: it read `tba`, never available, and was never
    searched, with fourteen more monitored films on talos.
- **A failed grab's retry is the whole pipeline again -- a search, a pick,
  a download, an import and the file's work -- and two things had ended it
  with approved releases left** (owner, 2026-11-17: "Shouldn't we exhaust
  all possible release sources before giving up?", then "The retry should
  enqueue a release search, select, download release, enqueue download,
  transfer, enqueue transcode, transcode and the other steps in between").
  The loop was already R25: a failure blocklists its release and owes each
  covered item one live search, whose best release the grab stage takes.
  - **An owner at its entry cap holds the release, it does not drop it**
    (`grabplan` `holdAtCapacity`, `CapacityRetry` 5 min): a Series keeps at
    most 24 entries (`MaxContainerEntries`), a failed one stays an hour as a
    tombstone (`BlockQuarantine`) and counts, and a release refused at the
    cap was gone while its search had counted as the failure's. Succession
    on talos held exactly 24 at every retry that never came -- thirteen
    live and eleven failed tombstones -- and nine episodes with 77
    and 89 approved releases a season sat Wanted with nothing queued; Naked
    and Afraid and Sex and the City hit the cap the same day. The release is
    now a pending grab, tried again every five minutes until there is room;
    a set and a donor are still refused.
  - **A dispatch that asked no indexer is not the failure's search**
    (`itemread.SearchCounted`: closed, and `lastSearchedAt` not before it).
    One every indexer was paced for, one with none to ask and one nobody
    answered close without moving `lastSearchedAt`; the redownload read
    only the dispatch's time, so it was spent. It is dispatched again after
    `searchplan.RedownloadRetry` (5 min, growing with the time since the
    failure to an hour), and lifecycle keeps the failed entry until a
    counted search (`View.Searched`).
  Still open: an answer the owner's grab stage never reads inside
  `grabplan.CandidateWindow` (a manager down for half an hour) is lost, and
  telling that from "nothing was left" needs a status field; a local fault
  (`writeError`: 20 of 22 on 2026-11-17 were followed by no grab, the
  engine's scratch files gone or altered) and an expired import hold start
  no retry, by design. After a chain breaks only the release index is
  asked, which found nothing in 54 episode searches while 5 of 5 live ones
  found approved releases, until a BacklogSearch's cooldown.
- **A payload fetch an indexer answers 429 pauses its grabs locally, and a
  429 whose body names one release's cooldown pauses that release alone
  (2026-10-16).** `spec.limits.grabLimit`'s ring is clustarr's count, not
  the indexer's allowance: three usenet engines spent nzb-life's as fast as
  it came back for seven hours, each re-add of a lost transfer fetching its
  .nzb again. `app/indexer/limits.PauseGrabs` records the refusal in
  `clustarr-indexer-limits` (Retry-After, else 15 min doubling to 6 h, never
  shortening a pause in force), the download verb refuses with
  `DownloadResponse.RetryAt`, the engines defer the command until then
  (`engine.PayloadDeferred`), and fetches to one indexer are single-flight so
  a burst asks it once. But nzb-life's 429 was never an account limit: it is
  "Already Downloaded", 24 h per NZB, and the indexer-wide pause held every
  nzb-life grab for the longest cooldown among the entries retrying. The verb
  sniffs the 429's body (`releaseCooldown`) and records such a refusal under
  `ReleasePauseKey` -- the indexer's other releases are fetched meanwhile.
  A release refused again after its cooldown is given up (2026-10-21). The
  refusals are counted (`DownloadResponse.ReleaseRefusals`), and at
  `engine.MaxReleaseRefusals` (two) the engine fails the entry
  `releaseRefused`. That blocklists nothing, a redownload is searched, and
  the Failed entry stays `lifecycle.RefusedHold` (24 h) as a tombstone, so
  the search takes another release. nzb-life refused three Naked and Afraid
  grabs every day from 10-15 to 10-20.
- **An import that imports nothing is classed, never blocklisted at once
  (2026-10-07).** Refusing every file used to read as a bad release, so a
  good 21 GB download the item merely would not take (its file transcoded)
  was blocklisted and deleted. Since ADR-0019 the import plan
  (`app/import/importplan`, in the manager's downloads stage) classes every
  refusal (`importplan.Classify`) and settles the outcome by the entry's import
  class: `transient` retries at 1m, 10m and 45m then holds; `releaseFault` is
  inspected once more before the release index blocklists it; `itemState` and
  `needsPerson` hold at once; a hold keeps the files for
  `lifecycle.ImportHoldRetention` (24h) and then fails as `importExpired`,
  neither blocklisted nor searched again. A new rejection site must say its
  class -- a rejection built by its class's constructor, never a bare string --
  and the grab lifecycle reads the class, never a message. Two deliveries
  of one execute ran side by side on 2026-10-27 (an import agent pod
  stopping while another took the redelivery): the first placed the file,
  the second found the source gone and its failure overwrote the placed
  answer, so the manager never materialised the MediaFile, re-inspected an
  empty folder and blocklisted a good release. Since then the execute's
  answer is written through `executeAnswer` (never over a terminal record
  at the same seq), an inspection that finds no file at all once an
  execute record exists is `needsPerson` ("may be in the library
  unrecorded"), never a releaseFault, a terminal entry keeps the message
  its failure set (a live one takes the transfer record's), and R25's
  redownload and the refused hold run from the entry's `failedAt`, never
  its latest dispatch (a Remove re-issues the command, and had restarted
  the window), with no search owed for an unmonitored item
  (`View.Unmonitored`), which had held that tombstone for good.
  An entry re-created from its transfer's claim covers no item (a claim
  carries none), so the downloads stage gives its import the owner itself
  as the one target; the agent keys a lone target only when it is an
  Episode or Issue (`fileimport.targetOf`, 2026-11-02), so such an import
  is attributed by its files' names, as a pack's is. Read as a key, the
  Series named an "episode" after itself ("episode X is not an episode of
  series X"), and the import was held and its files removed a day later.
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
  reply on the floor.** index answers `rpc.index.download` with the
  .nzb inline (base64 in JSON, so 4/3 of its size); a 1080p movie's
  ~1.3 MB .nzb is over 1 MiB, nats.go refused the reply client-side with
  `ErrMaxPayload`, `Serve` discarded that error, and the usenet engine
  waited out its 45 s deadline on every attempt -- the first real NZB
  grab on the owner's cluster sat in `Assigned` with "context deadline
  exceeded" while index's metrics counted a grab (2026-09-24). Two
  fixes: `nats.config.merge.max_payload: "<< 8Mi >>"` in the chart, held
  to kustomize by `TestBothInstallersRaiseTheNATSMaxPayload`; and `Serve`
  answers a reply it cannot send with a header-only service error, so the
  requester fails at once naming the cause
  (`TestServeReportsAReplyTheServerRefuses`). Operational trap on top: a
  `max_payload` reload updates the server, but a connected nats.go client
  keeps the `INFO` it read at connect, so the publisher (index) had to
  be restarted before the raised limit took effect. The inline cap itself
  (4 MiB raw) then refused The Godfather's 1080p .nzb (2026-09-30): an
  .nzb grows with the release, about 3.8 MB for 28 GB, so any large encode
  hit it. The bus reply now gzips a body over 1 MiB
  (`schema.DownloadResponse.ForWire`, `encoding: gzip`; NZB XML compresses
  about four times), refuses one still over `schema.MaxDownloadWireBytes`
  (5.75 MiB) compressed, and both engines read it through
  `DownloadResponse.Payload`; index reads up to 32 MiB raw, the usenet
  engine's own cap. A real-server test holds the budget to the broker's
  8Mi, envelope included.
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
  carries the main packet. Real subjects from that post were the fixture.
  Since then the floor follows SABnzbd's byte rule, in which block counts play
  no part (`TestCriticalHealthPercentFollowsSABnzbdsByteRule`; the
  missing-article gotcha below).
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
  good; `download.ClampMessage` cuts it on a rune boundary, the same class as
  `clampPercent`. Since ADR-0019 the engine writes it into its transfer record
  (2048), and the manager cuts an entry's message to
  `lifecycle.MaxEntryMessage` (1024) when it renders the grab entry: any string
  an engine reports must be clamped at the `pkg/download` boundary and again
  where the manager writes it into a bounded CRD field, never trusted.
- **An obfuscated season pack unpacks to hashed file names
  (`02fb98288e35eea0f5b10ea420980959.mkv`), and no job name can restore
  them (2026-10-27).** The uploader hashes the `.mkv` names before
  archiving; the NZB's title is the season's, so nothing says which hash
  is which episode. The engine runs SABnzbd's three recoveries, in its
  order of trust: before repair, the outer par2 set names the files on the
  wire by their first 16 KiB or any intact slice (`renameObfuscated`);
  before publish (`job.finalNames`, `pkg/download/usenet/deobfuscate.go`),
  the par2 set an archive unpacked beside its files names them the same
  way and is then removed (`process_unpacked_par2`), a file with no usable
  extension takes one from its magic bytes, and one big file (10 MiB or
  more, outside disc and archive structures) whose name is probably
  obfuscated (`isProbablyObfuscated`, SABnzbd's rules) takes the release's
  title ("Deobfuscate final filenames"; `download.AddRequest.Title`, from the
  engine command's release, else an NZB title that is no hash itself, else
  nothing) -- never the job's own name, which is the grab entry's id: until
  2026-10-27 it was, and "86-eighty-six-cb83c58857-50e5c90a59.mkv" parsed as
  absolute episode 50 and was held for a person. A pack's hashed files are left as they
  are on purpose: `job.1.mkv`-style names would parse as nothing, and the
  importer reads each file's container `title` tag instead
  (`fileimport.titleNames`), which uploaders who hash the names usually
  leave as the episode's release name, probing the file once for both the
  title and its quality (`probed`); a title that names no episode is said
  in the `needsPerson` rejection. srrDB (scene releases' exact sizes and
  CRCs) is not consulted. The unpack also opens archives nested inside the
  NZB's archives (`unpackNested`, `maxNestedUnpack` 3 levels), each into
  the directory it sits in, removing the set it opened
  (`archiveSetFiles`): a rar posted inside a rar was published as an
  archive, which the import refused as the release's fault. An inner par2
  set is read for names only, never for a repair: the rar extraction
  checks every file's CRC (`rardecode.ErrBadFileChecksum` fails the
  unpack), so a repair pass would re-read the whole release to learn what
  the extraction proved.
- **A usenet transfer whose scratch directory is removed from under it
  must fail, never publish (2026-10-27).** `/data/usenet/incomplete/tv`
  was removed from the NAS side while the engine transferred a 26 GB
  season pack. NFSv4 keeps a file another client unlinked writable for
  whoever holds it open, so every article "landed" in orphaned inodes and
  the job reported 100% while its checkpoint failed ENOENT every two
  seconds for nine minutes; the end-of-transfer chase then reopened the
  targets with `O_CREATE`, which recreated the job directory with twelve
  empty files (one got the chase's 343 articles), and the publish moved
  them into the data directory as a complete pack, which the import held
  as "not a playable media file". Three guards, all `ErrScratchLost`
  (classed `writeError`, a local fault): `openTargets(create)` creates
  only on the transfer's first open, so a retry or chase whose target is
  gone fails instead of starting it over empty; `startCheckpoint` fails
  the transfer on a checkpoint's `ErrNotExist`; and `job.verifyContent`
  holds every fully-arrived file to the decoded size its first article's
  yEnc header declared (`job.sizes`, kept in the manifest as `sizes`)
  after repair and before unpack or publish. `Client.Remove` treats a
  scratch directory already gone as removed, since the manager had
  retried the absent command until another job recreated `tv`. The
  engine never probes media (ADR-0017): a file's bytes are the articles'
  CRCs' and par2's to judge, and the importer's probe refuses a file that
  is not media.
- **A file's frozen `spec.languages` are its probe's tagged audio tracks
  (2026-10-27), else the names' reading.** A title's MULTI names no
  language, so Solo Leveling's OldT pack froze every file as Japanese (the
  series' original language) while its probe carried fourteen tracks and
  the file was named for all of them; Sonarr's AggregateLanguage lets
  MediaInfo outrank the file's and the release's names, and
  `fileimport.probedLanguageNames` does the same in the catalogue's
  vocabulary (`catalogue.LanguageName`). An untagged track leaves the
  names' reading (`decision.AudioLanguages` names none then). Only the ui's
  file table reads the field; the decision reads the probe.
- **The usenet engine's emptyDir scratch shares the node disk with etcd
  on kind, and vanishes with the pod.** A 10 GB transfer plus its par2
  verify pushed etcd's fsyncs past a second: the grab controllers lost their
  leader lease and exited, then the kube-apiserver failed its liveness probe and was
  killed (2026-09-24); and a deploy that replaced the engine pod discarded
  a finished 9.4 GB transfer, so the new pod downloaded it again. Place
  the working area on the shared volume instead -- `spec.usenet.scratch.
  path: /data/usenet/incomplete` and `spec.usenet.publishDir:
  /data/usenet/complete` (ADR-0014; `existingClaim`, `volumeName` and
  `accessModes` cover a claim of the operator's own). The manager's
  DownloadClient controller refuses a path off the data mount with
  `Ready=False, InvalidSpec`. That is kind's rule. talos, whose etcd is
  not on the NUC's disk, has used a node-local volume since 2026-10-28:
  `scratch.volumeName: usenet-scratch-nvme` (hostPath on talos-88's NVMe,
  declared in cluster-bootstrap), with only the publish crossing to the
  NAS. On NFS scratch, post-processing waited on the NAS at every step
  (ADR-0014's amendment and its as-built note).
  Torrent clients have the same placements since 2026-09-30
  (`spec.torrent.scratch`, `spec.torrent.publishDir`, default
  `/data/torrents`): a transfer downloads in scratch and reads Completed
  only once `pkg/download/torrent`'s publish has moved it to publishDir,
  where it seeds; a storage class is a claim per StatefulSet replica
  (volumeClaimTemplates, whose change replaces the StatefulSet keeping its
  pods), and no scratch means download in place as before.
- **A newznab .nzb is not byte-stable across fetches, so a payload hash
  cannot tell a re-add from a new transfer.** nzbgeek varies the obfuscated
  `title` and `password` metas on every download of the same release; the
  usenet engine re-resolved a Download whose status did not yet carry its
  id (a status write had failed) and added the release a second time under
  a second hash -- two 10 GB transfers of one Download, the first an orphan
  until the reaper's 10-minute grace (2026-09-24). `usenet.Client.Add` now
  dedupes on `AddRequest.Name` as well as on the payload. Since ADR-0019 the
  name is the grab entry's id: the engine's add command asks the client
  (`download.ByName`) for a job under that id before fetching a payload, and
  again after a download RPC that timed out, before asking twice -- every fetch
  is a grab the indexer counts -- and the engine journal claims a transfer by
  the same id, so a re-grab of the same release reuses it. Torrents are
  unaffected: an info hash is the content.
- **An engine reports a transfer under one entry uid, and an add adopts a
  transfer by the entry's id (2026-11-02).** On 2026-10-30 the manager sent
  four entries a second add under a new uid a second or two after the first
  (the second admission's status was lost; cause unproven). The usenet
  client adopted each job by name and re-journaled it to the new uid, so the
  records the owners read -- the first uid's -- stood at `fetchingMetadata`
  for three days while the jobs had failed or finished. An add that takes over
  another grab's transfer of the entry now writes that grab's record removed
  at its own seq (`engine.Handler`'s `writeDisplaced`, found through
  `NamedFinder` without listing), and lifecycle re-adds a live entry whose
  record reads removed at or past its dispatch seq, which adopts the transfer
  back under the uid the owner holds; commands lock on the entry's id. An
  unclaimed failed transfer is removed (`converge`), where it stayed for good.
- **A rebuildable durable's ledger slot never lapses, so a lost Answered
  wedged automatic search (2026-11-02).** From 2026-10-30 10:12Z sixteen
  slots -- catalog-search-normal's whole budget -- that no status showed in
  flight admitted no automatic search for three and a half days, and the
  catalog agent's HPA (min 0) took it to zero, since nothing was queued; a
  manager restart rebuilt the book (rev 165). Two ways a slot leaks are
  known: on a Movie or a Series an item pass ends before its effects when a
  set after catalog-grab's (catalog-metadata, -artwork, -imageset) conflicts,
  losing a `DispatchAnswered` whose closed `searchDispatch` landed; and a
  deleted item's search in flight was never forgotten. An Episode writes
  catalog-grab's set alone, so the Episodes' leak itself is unexplained. A
  closed dispatch the ledger still `Holds` is answered again
  (`search.Stage.unanswered`, the season search's too), an item pass that
  finds its item gone forgets its slots (`Ledger.ForgetGone`), and every
  `AuditInterval` (5 min) the leader releases a published slot older than
  `AuditAge` (20 min; a dispatch in flight is republished, and restamped,
  every 2 min) that its source's keyed read shows no longer in flight
  (`Ledger.audit`, `KeyedSource`, Event `DispatchSlotsReleased`, which names
  the first keys). The search stage's R25 window reads `failedAt`, as
  lifecycle's does. On the search lanes (the durables with a `Source`) a
  redownload is admitted through `Ledger.AdmitFirst`, which claims the next
  slot ahead of routine searches (a routine `Admit` is refused one slot
  sooner per such waiter, for `FirstTTL`); a Budget refusal asks again
  unwoken only after about `BudgetRetry` (2 min, spread by key), and every
  slot that frees wakes two waiters, AdmitFirst's first (`Ledger.Wakes`, the
  item loop's "ledger" source at `PriorityUser`): ~2,263 refused searches
  had asked every 10 s, ~226 passes a second, and the ~307 redownloads among
  them got about one admission in eight. Other durables keep `TickInterval`
  and never wake. A redownload is owed only while the item does not hold a
  file that wants nothing (`searchplan.RedownloadDue`, lifecycle's
  `View.Satisfied`), so an item that got a file after its grab failed owes no
  live search, and its tombstone is not held for one.
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
  **An article is missing only once every server has refused it**
  (2026-10-22, `job.exhausted`): the verdict after a chase counted the
  articles the primary's workers failed *during* the chase, which no second
  server had been asked for, so every Cyberpunk: Edgerunners post on talos
  failed "84 of 4294 articles missing" right after easynews had served
  every article frugal lacked. The gate still triggers on any failure, a
  chase asks the other servers for the chaseable ones (failed, not yet
  refused everywhere), `markExhausted` records what every server refused,
  and `healthVerdict` judges those alone; the message says "missing on
  every server". **A chase never stops the transfer** (2026-10-26): one
  worker retries or chases while the others fetch on (`healthGate`'s
  `TryLock`), and a chase asks each server only for the articles it began
  with (`stillChaseable`), so one the primary fails mid-walk starts the
  next chase at the first server. Before, every worker queued behind each
  chase, and articles failed during it were asked only of the servers still
  ahead. On talos, A Sign of Affection's posts lacked about a quarter of
  their articles on frugal, and easynews held all of them. Each job spent
  nine tenths of its time chasing, three quarters asking frugal-bonus,
  which refused every one, and the two transfer slots took turns moving
  bytes.
- **A provider can answer a message-ID with another post's article, whole
  and CRC-clean.** frugal served AnoZu's S01E01 (posted 2026-02-16, part 1
  of 2100) with part 237 of 503 of "b5f29fcb1fe75f9e", posted 2026-08-12
  to alt.binaries.bloaf under the same message-IDs; easynews held the
  original. Every such article's own pcrc32 held, so the engine wrote it at
  its own post's offsets, counted nothing missing and skipped par2 ("every
  article verified on receipt"): 22 of 435 transfers in five days came out
  as foreign bytes and holes, refused at import as "not a playable media
  file" -- a releaseFault, so the automatic ones were blocklisted -- and
  the first article's foreign `size=` cut the file short. SABnzbd and
  NZBGet check only the CRC (and NZBGet the size). The pool now checks a
  body against the segment it was asked for (`segmentMismatch`: yEnc
  `part=` is the NZB segment number, `total=` is not short of the file's
  highest), and an article that fails is no copy on that server
  (`ErrArticleMismatch`), so the next one's is taken, as for a corrupt one.
  The yEnc `name=` is no test: obfuscated posts name every article
  differently.
- **An NZB can list only part of a post, and what it does not list is
  missing (2026-11-17).** nzbgeek's NZBs for Fena: Pirate Princess S01E01
  and E11 listed 1,735 of the file's 2,087 articles and 1,248 of 2,079, the
  par2 volumes short the same way (the indexer showed 1,337 and 942 MiB
  where a whole post is about 1,470). The transfer preallocates a file to
  the size its first article declares and writes what it fetches inside it,
  so the unlisted parts stayed zeros; every article *asked for* arrived, so
  nothing was missing, par2 was skipped ("every article verified on
  receipt") and `verifyContent` saw the declared size. The files imported
  (a header probes whole), transcoded with 28% and 59% of their frames gone
  and read `Transcoded`, cutoff met; the owner found it by eye. Three gates
  since, each on the bytes it has:
  `unlistedSegments` (`pkg/download/usenet/nzb.go`) counts the numbers an
  NZB skips below its highest, and below the part count its yEnc subject
  ends with (`yEnc (1/2087)`; a subject's `[3/74]` numbers files and is not
  read), as missing on every server from the parse -- in `failedArticles`,
  `exhaustedArticles` and the bytes beside them -- so the health gate, par2
  and the bare-content rule judge it, and `unlistedGate` does so before a
  byte moves; a retry or a chase still asks only for listed articles
  (`failedListed`). The import reads every probed video end to end
  (`fileimport.videoHealth`, `mediainfo.HealthProber`, heartbeating; owner:
  "We should verify the video files are healthy before considering a
  release ready for transfer and next steps") and refuses one with more
  than `mediainfo.MaxMissing` (2 s) gone from *every* audio and video
  stream at once as the release's fault (`mediainfo.ErrDamaged`, "not a
  whole media file"): a hole in the bytes takes every stream's packets, and
  a still in a variable-rate video or a late audio track does not. Only
  Matroska and MP4 are judged, and only with both sound and picture; a read
  that cannot finish judges nothing. The transcode engine feeds the same
  `mediainfo.Tracker` from its own demux and fails a damaged source
  (`InvalidSource`, "the source is damaged and was not encoded") rather
  than encode what is left, which covers files imported before the gate.
  The cost is one sequential read of each imported video. **Not built:** a
  transcode's verdict does not blocklist the release or search again -- a
  MediaFile records its release's title and indexer, not the GUID the
  blocklist matches -- and nothing re-judges the library's existing
  transcodes, whose damage is in their own timestamps (found by reading
  each MP4's frame count through Plex, 2026-11-17).
- **A post can be a cut-off copy of its release, whole and repairable,
  and a file cut short that way is replaced (2026-11-25).** nzb-life's
  `The.Drama.2026.1080p.AMZN.WEB-DL...-BYNDR.gz` was 2.3 GB where the BYNDR
  release is 9.3 GB: the NZB listed all 2,854 of its articles and its par2
  was made from the cut-off MKV, whose header still said 106 minutes and
  carried every chapter. Imported and transcoded on 2026-11-17 before
  a013ac4b's gates were live, it was a finished transcode of 28 minutes,
  final under every rule. The import's health read refuses such a file now
  (its streams end before the container says). For one already in the
  library, `MediaFile.CutShort` reads two signs, since a transcode writes
  what it was given under its source's chapters: the video ends more than a
  minute, and more than a fiftieth, before its last chapter does
  (`TruncatedBy`), and it runs more than a twentieth under the runtime its
  item is listed with. **Neither sign alone is the rule.** Of talos' 1,279
  movie files the chapters outran five: The Drama, Finding Nemo (67 of 101
  minutes) and Eighth Grade (76 of 94), and Toy Story 2 and Pastoral, whole
  files at their listed runtime under chapter lists six and ten minutes too
  long. And of the eight files well under their listed runtime, four ended
  with their chapters: another cut. Both signs together name the three. The
  item reads cutoff unmet, reason `Truncated`, a transcode included (movie
  and episode reconcilers); a probe that moves the length of a file short
  of its chapters wakes the item (`FileSignature.ShortOfChapters`); search
  and grab let a release of any quality the profile takes replace it
  (`decision.Current.Truncated`, set in `itemread.Current` from the item's
  runtime); import does too, measuring the existing file against the
  incoming file's own length (`importplan` `truncatedRefit`); and a season
  of such episodes is asked for as a pack. **Not covered:** a file with no
  chapters (400 of the 1,279); an item with no listed runtime; a file with
  holes whose length is whole (the Fena kind; the import and the transcode
  refuse new ones); and the wait for the replacement's search is the wanted
  sweep's, whose scheduled searches ask the release index alone until a
  BacklogSearch grants a live one.
- **A usenet engine ran every job at once, and OOM-looped seventy times
  in six days (2026-10-14).** `Add` and re-attach started each job
  immediately, each with a worker per connection of every provider, and
  a restart resumed all of them -- two archives unpacking and a dozen
  transfers -- past its 8 GiB, so every start ended the same way. Now
  `usenet.Config.MaxActive` (2; DownloadClient `spec.usenet.maxActive`
  since 2026-10-28) jobs hold a transfer slot and `MaxPostProcess` (1;
  `spec.usenet.maxPostProcess`) post-process; the rest wait Queued
  (`priority.go`: the best class, then the oldest; a paused or outranked
  job gives its slot back) -- a waiting job parks on its own ticket and the queue's
  one loop fills free slots on a change, never a poll (2026-10-28: about
  720 queued jobs each walking every job under the client lock at every
  wake held the engine's commands, removes among them, for minutes). Such a wait is `Item.WaitingTurn`
  (`TransferRecord.waitingTurn`), which stops the manager's stall clock
  and reads as the entry's phase Queued, not Downloading (2026-10-28: 590
  waiting entries read Downloading beside the five transferring), and
  `lastProgressAt` restarts when the turn comes -- a queue is not a
  stall. The engine serves pprof on `127.0.0.1:6060`
  (`downloadclient.EnginePprofAddress`), reached through
  `kubectl port-forward`.
- **`exec.CommandContext` alone does not end a subprocess that has
  children.** Cancelling the context kills only the leader; a wrapper
  script's `sleep` (or par2's helpers) kept the stdout pipe open and
  `Wait` blocked until the child exited on its own, which made the par2
  deadline test hang for the child's whole lifetime. `Par2Runner.Repair`
  sets `Setpgid`, a `Cancel` that kills the process group, and
  `WaitDelay`; do the same for any subprocess given a deadline.
- **Every cache strips `managedFields`, and the cache-sync timeout is ten
  minutes.** On the owner's library (15,630 Episodes, a 57 MB list that
  `kubectl` alone takes 40 s to fetch) caption crash-looped on
  controller-runtime's two-minute default while every service listed at
  once after an upgrade, so `pkg/k8s.ManagerOptions` sets
  `Controller.CacheSyncTimeout = k8s.CacheSyncTimeout` and a
  `DefaultTransform` of `cache.TransformStripManagedFields()`, and the ui's
  reader does the same. The trap: a reconciler that reads `managedFields`
  off an object from the cached client sees none, silently. Read them
  through `mgr.GetAPIReader()`, as the grab worker's `appliedByGrabPath`
  did (it retired with ADR-0019); the artwork envtests read them through a
  direct client. One process now caches every kind the manager reconciles, so
  the ten minutes is the manager's start budget for the whole library.
  **The manager holds its four large kinds as compressed rows** (owner,
  2026-11-03, `pkg/k8s/rowcache`,
  `docs/superpowers/specs/2026-11-03-compressed-row-cache-design.md`):
  Episode, MediaFile, Movie and Series were about 230 MB of its live heap as
  decoded objects, keys and index sets, and their compact JSON is the same
  size, so `rowCacheKinds` in `internal/cli/manager` go through
  `rowcache.New`, whose informer turns each object into a `Row` in the
  DeltaFIFO's transform (its JSON as one zstd frame, 4.9-8.3x with a
  dictionary the informer builds from the kind's own initial list and
  applies in a sweep; the labels and every registered index's values kept
  beside it) and whose cache decodes a fresh object for every Get, List
  entry and handler notification. The rules that follow: the store's items
  are `*Row`, never the kind, so nothing reads `GetIndexer().List()` for
  objects; a List narrows by its field index, namespace and labels before
  it decodes, so a whole-collection List of a large kind now costs a decode
  per object (about 1.5 s for the MediaFiles) and belongs behind an index
  or a memo -- a watch map function that needs only keys or labels reads
  them through `rowcache.Keys` or `rowcache.Metadata` on `mgr.GetCache()`,
  which the row cache answers without a decode (the first deploy
  OOM-looped at 768Mi: three such map functions decoded every MediaFile at
  start, 233 MB held at once); an event is decoded once and every handler
  gets the same object, as a shared informer's handlers do, so a handler
  never mutates it; the watch stream turns each object into a row before
  the reflector's own store sees it, so the initial list is never held
  decoded whole; and `UnsafeDisableDeepCopy` buys nothing on these kinds.
  A reader that wants the few items in some state lists them through a
  marker index (`itemread.SearchInFlightIndex`, `SeasonSearchInFlightIndex`,
  `AnimeIndex`, `MALIndex`, `mfindex.MediaKind`), never the kind: the
  ledger rebuilds, the MAL-Dubs source and the profile overlap checks did,
  and were the manager's start-up peak after the first deploy (rev 197).
  Metadata-only and unstructured reads of them keep their own ordinary
  informers. Two more rules since the third round (2026-11-03): the
  handlers that register together on a started informer share one replay
  of the store (`pkg/k8s/rowcache/replay.go`: one decode per row for all
  of them, each member at most `replayLead` rows ahead of the slowest, so
  a shared object lives until the slowest has dispatched it; a handler
  registering after the store changed replays on its own), and a
  remediation pass reads through a `passReader` that answers a repeat Get
  of one object from the first decode, by the same object (the planners
  read with `UnsafeDisableDeepCopy` already and never mutate what they
  read), counted in `clustarr_remediation_pass_reads_total{kind,result}`.
  **Album is the fifth row kind, and the music library is why** (owner,
  2026-11-17, "What's causing clustarr-manager's memory to spike now?",
  then "Approve all"): the scan of 2026-11-16 took talos from 1,055 to
  6,876 Albums and from 16,864 to 33,304 MediaFiles, and the manager's live
  heap from about 290 to 510 MiB. An Album's status carries its tracks and
  releases (37 MB of compact JSON for the 6,876; 7.7 MB as rows, 81 us a
  decode), so it joins `rowCacheKinds`, and the Album watches' map
  functions (`album.albumsOf`) read an Artist's Albums through
  `artist.AlbumByArtistRefIndex`, where they had listed every Album in the
  namespace and filtered in Go, per Artist and per profile. The transcode
  ledger's resync, every 5 minutes, finds the video files by
  `mfindex.MediaKind` on `mgr.GetCache()` and reads them one at a time
  (`admission.Ledger.scanBacklog`): one List had decoded every MediaFile,
  the music tracks too, and held them all for the walk, about 160 MiB.
  And the Album's track-listing cache (`album.ReleaseCache`) keeps each
  listing as its JSON in one zstd frame, decoded at each Get, bounded by
  Albums (16,384) and bytes (48 MiB): decoded, a release group's every
  release was a map of ids per track, 113 MB for the 4,096 Albums it held.
  **The manager talks to the apiserver in CBOR** (owner, 2026-11-04;
  `pkg/k8s/rowcache/cbor.go`): talos's apiserver serves and accepts it
  (`CBORServingAndStorage`), and client-go's `ClientsAllowCBOR` and
  `ClientsPreferCBOR` gates are turned on at process start unless
  `KUBE_FEATURE_<gate>` names one in the environment. controller-runtime
  0.25 itself built the codec factories of its client and its cache without
  the CBOR serializer, so under PreferCBOR every list it made asked for CBOR
  and could not decode the answer ("serializer for application/cbor doesn't
  exist"; the pods started with the two variables on 2026-11-04 never became
  ready): `go.mod` replaces it with the clustarr fork
  (`github.com/mediactl/controller-runtime v0.25.1-clustarr.N`, branch
  `clustarr`, three commits on the upstream tag) whose two factories go
  through `rest.CodecFactoryForGeneratedClient`, identical to upstream with
  the gate off, and whose typed client decodes an apply's answer by its
  content type, a CBOR answer transcoded through a generic map and the JSON
  path so that fields the apply configurations do not declare
  (`metadata.managedFields`) are dropped as upstream's JSON decode dropped
  them (upstream read the answer as JSON only, so the manager exited at its
  first server-side apply at 04:5xZ; apimachinery's direct CBOR decoder is
  strict, so every apply failed "found unknown field" at 05:18Z). Bump the
  fork by rebasing those commits onto the next upstream tag and tagging
  `v<upstream>-clustarr.1`, as ffgo's fork is bumped.
- **A watch's map function runs for every object at startup, and the
  source is not synced until it has.** controller-runtime's `Kind` source
  waits on `handlerRegistration.HasSynced`, and a cache's initial sync
  delivers every object as a create, so an `EnqueueRequestsFromMapFunc`
  that lists a namespace per event is O(watched × listed) before the
  controller may start. caption's two Movie/Episode map functions each
  listed every MediaFile: ~450 million deep copies on the owner's library
  (16,450 items, 13,754 files), the Episode source never synced inside
  `CacheSyncTimeout`, and caption crash-looped (2026-09-29; the "load
  after a deploy" explanation recorded before was wrong). Look a
  watched object's related objects up through a field index (the remediation
  loop's `remediation.mediafile.item`, `app/remediation/mfindex`, which
  replaced `app/caption/itemindex` and eight other MediaFile indexes in the
  fold), never a filtered namespace List. The initial list also queues every
  key the loop has at low priority: on 2026-10-28 that was 39,296 keys
  (28,945 Episodes, 16,699 of them unmonitored), which 16 workers drained at
  about 15 a second while the day's work arrived as fast, so the backlog never
  shrank. An item watch's `rollup.Watch.SkipInitial` leaves out of the
  initial list what it calls idle -- an Episode unmonitored or transcoded with
  nothing in flight (`episode.idleAtStart`) -- and a file's or
  QualityProfile's initial-list create wakes no item, since every item's own
  watch lists it at the same start. The manager serves pprof on
  `127.0.0.1:6060` by default (`ctrlflags.ManagerPprofAddress`), and
  `clustarr_remediation_pass_seconds{kind,phase}` and
  `clustarr_remediation_effect_seconds{planner,effect}` say where a slow
  pass spends its time. The first profile found 11 of 16 workers waiting on
  the Series pass's episode fan-out, which asked the metadata gateway for
  the whole list and patched every Episode on every pass (1,457 PATCHes a
  pass for Pokemon, about 110 a second on the control plane): a Series now
  fetches its list again only when its spec, metadata refresh or add options
  moved since its last complete fan-out in the process (the containers' one
  memo, `app/remediation/childsync`, through `series.syncKey` since
  2026-11-10; the fan-out also waits for `status.metadata`, reading
  `EpisodesSynced` False/MetadataPending meanwhile, as the Artist's, Author's
  and Comic's do), and an Episode already holding its provider fields is not
  patched (`providerStatusMatches`). **A container's `status.addOptionsApplied`
  flips only with the fan-out that applied them** (the Comic's rule: the
  stored value on every early return, `stored || fan.synced` after; the
  Series, Artist and Author since 308f974c, 2026-11-10): a pass that flipped
  it while waiting for the metadata document made the first real fan-out
  create every child under `monitorNewItems` instead of
  `addOptions.monitor`, the 2026-09-29 search-storm class, on all three.
  Those three key every childsync fan-out under the flag true, since a
  child's own Create wakes the container before the flip's apply reaches
  the cache and a key read as false would refetch the list to create
  nothing; the Comic still keys the flag as read (a latent double fetch,
  open), and the "added" Event reads `observedGeneration == 0`, never the
  flag.
- **Never list every Episode or MediaFile with kubectl: talos's apiservers
  read every page after the first from etcd (2026-11-03).** Each
  kube-apiserver sat at 5.0-5.7 GiB resident on the 16 GB control-plane
  nodes, its Go heap having reserved 7-8 GiB against about 2.3 GB live (the
  watch cache's ~42,000 clustarr objects): in the four days since it
  started, each had read about 870 whole Episode collections (24 million
  objects) and 815 whole MediaFile collections out of etcd, and the audit
  logs named `kubectl` from the sessions' own machine (user `admin`,
  `?continue=…&limit=500`). kubectl pages a LIST 500 at a time; the watch
  cache answers the first page and etcd every continuation, decoded into
  unstructured maps (measured on IPTVChannels: `--chunk-size=100` read 157
  of 257 from etcd, `--chunk-size=0` none), so one `kubectl get mediafiles`
  costs a control-plane node ~210 MB of JSON decoded and `get episodes`
  ~280 MB, every time a loop polls it. Read one object by name, or filter on
  the server (`--field-selector` on a selectable field, `-l`) with
  `--chunk-size=0`, which the watch cache answers with no etcd read
  (`--field-selector status.transcode.phase=Running`: 0 from etcd); never
  poll a whole collection. `apiserver_storage_list_fetched_objects_total`
  `{storage="etcd"}` (`kubectl get --raw /metrics`, per apiserver with
  `--server https://<node>:6443`) and the audit log
  (`talosctl -n <node> read /var/log/audit/kube/kube-apiserver.log`) show
  who reads what. Talos runs the apiserver without profiling, so no heap
  profile is to be had.
- **On kind, loading an image into the node stalls etcd, and every lease
  with it.** `kind load docker-image` unpacks gigabytes into the node's
  containerd store, which shares a disk with etcd; etcd's WAL fsync then
  took 3-15 s (2026-09-30), kube-controller-manager and kube-scheduler
  restarted ~50 times, and every leader-elected service exited "leader
  election lost" (controller-runtime times a lease request out at
  RenewDeadline/2, 5 s by default). `pkg/k8s.ManagerOptions` sets the lease
  to 60 s / 45 s / 5 s (`k8s.LeaseDuration`, `RenewDeadline`,
  `RetryPeriod`), and a deploy loads only images whose inputs changed.
- **A torrent engine behind a SOCKS5 proxy (`spec.torrent.proxy`,
  2026-09-30) must not leak by UDP or by DNS, and Go's library covers
  neither.** `golang.org/x/net/proxy` speaks CONNECT only, so `pkg/socks5`
  implements UDP ASSOCIATE itself; the proxied engine opens no local socket
  (anacrolix `DisableTCP`/`DisableUTP`/`NoDHT`, no incoming, no WebTorrent)
  and adds a CONNECT dialer, a DHT server and a pure-Go uTP dialer on UDP
  associations, and a listener that only names `listenPort` for announces.
  anacrolix resolves a UDP tracker's host (`net.ResolveUDPAddr`) and the DHT
  its bootstrap routers before any packet reaches a PacketConn, and the
  pod's `ndots:5` plus the host's search domain sent `tracker.org.appkins.io`
  to the node's resolver, so the engine process swaps `net.DefaultResolver`
  for `socks5.Proxy.Resolver`, which routes each query by its question name:
  cluster names to cluster DNS, everything else over TCP through the proxy
  to `dnsServer` (Mullvad's SOCKS server refuses its own resolver
  `10.64.0.1:53`, hence `1.1.1.1:53`). A proxy that refuses UDP ASSOCIATE
  leaves the DHT, uTP and UDP trackers off with a `ProxyUDPUnavailable`
  Event, never direct. anacrolix/utp stalls ~1.5 s on a first burst with or
  without a proxy; its proxy test skips under `-race` for that reason.
- **No Movie had a certification until 2026-09-30.**
  `tmdb.mapReleaseDates` parsed each release's certification into the
  client model and nothing promoted one to `Movie.Certification`, and the
  gateway passed an empty region to every lookup, so Plex showed no content
  rating for any film. `metadata.PickCertification` now chooses the
  region's, else the origin country's, else the US one, from
  `CertificationsFromReleases`. The recorded fixture shows why the origin
  step exists and why it does not always decide: Weekend (2011) carries GB
  18 and US NR, so a US-region install shows NR. TVDB episodes likewise
  never carried their own id, so `Episode.status.tvdbID` was 0 everywhere.
- **Use `github.com/dlclark/regexp2`, not stdlib `regexp`, for TRaSH patterns.**
  Go's RE2 rejects 157 of the 2791 custom-format regexes (backtracking,
  lookaround). Set `IgnoreCase` and a `MatchTimeout`.
- **A regexp2 `MatchTimeout` is wall-clock time, so a timeout is "unknown",
  not "no", and must never be remembered.** A process held off its CPU for
  50 ms (a throttled container, a collection pause) runs out on a title that
  matches in microseconds: agents logged four on plain titles in nine days
  (talos, 2026-11). `release.Parse` returns the timeout as an error and
  `catalogue` reads it as "no match"; `catalogctx.formatNames` took either
  as its answer and its memo kept it for the life of the process. A file
  with no recorded release title is named from its own name, so on talos
  One Piece S18E28 read `[v2]` from the name `[v2 v3]` and `[v2 v3]` from
  `[v2]`, and was renamed between them 4.86 million times in eight days
  (2026-11-18 04:28Z to 2026-11-25: five API requests a pass, 83% of the
  requests on the kube-apiserver the manager was connected to, the
  apiservers at their 4 Gi limit; five more files on 2026-11-03, which
  ended when the memo filled and was emptied). The warning went nowhere:
  the naming render passes `context.Background()`, whose logger discards.
  Now (owner, 2026-11-25: "Fix this in whatever way seems suitable"):
  `release.IsTimeout` and `catalogue.WithMatchFailures` say an answer is
  short; `catalogctx.renameSlugs` works a short answer out again, three
  times, and `formatNames` never remembers one; and
  `mediafile.settledName` proposes a name only if the file would keep it
  once it had it (rendered again at the proposed name, three names on): a
  name that proposes itself is the answer, names that go round leave the
  file where it is, and names that neither settle nor return are
  `Unrenderable`, which holds the rename. Tests:
  `TestAnAnswerARegexRanOutOfTimeOnIsNotRemembered`,
  `TestAnAnswerAConditionRanOutOfTimeOnIsNotRemembered`,
  `TestAFailedMatchIsCountedForACallerThatAsks`,
  `TestANameIsProposedOnlyIfTheFileWouldKeepIt` (each fails with its fix
  removed). Not changed: a timeout in `Match` or `Score` still scores a
  release short for that one evaluation, and nothing caps how often one
  file may be renamed for any other cause. To stop such a loop on a live
  manager without a restart, send the MediaFile's `spec.matchedFormats`
  again in another order: the memo's key includes them.
- **Quality profiles are TRaSH-only and opinionated.** Built-in profiles ship as
  embedded data; custom-format editing is deliberately not exposed.
- **Every per-file answer is a record written by compare-and-swap, and only the
  loop writes MediaFile status.** A worker answers its dispatch with
  `records.Answerer.Answer`, a compare-and-swap on the record's revision
  (`pkg/records`); a raced answer is
  `records.ErrRaced`, redone from a fresh read, and the loop incorporates the
  record in its one status apply under `catalog`. A fact (a swap, a graft) that
  lands after its dispatch was withdrawn is still incorporated
  (`records.FactWindow`, `ErrUnincorporatedFact`). Any new per-file worker
  answers through a record, never a status write. The lesson underneath is
  older: two write paths under one manager need compare-and-swap. Before the
  fold, transcode wrote `TranscodeJob.status` from its reconciler and its results
  consumer through `writeStatus`/`patchCAS`, carrying the read `resourceVersion`,
  so a race was a Conflict redone from a fresh read, not a silent rollback; the
  Indexer's writers still do (`app/indexer/status/statuspatch.PatchCAS`).
- **A usenet transfer runs only under a connection grant from the shared
  ledger (2026-10-16, `docs/superpowers/specs/2026-10-16-usenet-connection-ledger-design.md`).**
  `clustarr-usenet-connections` holds one compare-and-swap entry per
  provider account (host, port, username: every pod and every
  DownloadClient on that account share the plan's limit), each grant
  expiring 90 s after its last renewal. A transfer takes a share on a
  primary and fetches only there, bounded by the grant: MaxActive's slots
  are divided among the primaries in proportion to their connections, at
  least one each, a share is a server's connections over its slots
  (`usenet.primaryShares`), a transfer asks every primary for a full
  share before it takes a leftover on any, and with several primaries
  transfers leave a tenth of each to chases (`usenet.transferBudgets`: a
  mid-transfer chase parks on a full server while its job keeps its
  grant, so two primaries full of transfers could each wait on the
  other's chase for good) (owner, 2026-10-28: every
  primary carries transfers at once; until then each took `limit /
  MaxActive` of the first primary with room, so only frugal's 50
  connections ever carried transfers); no primary with room means it parks
  Queued, `WaitingTurn` (the manager's stall clock and the engine's stall
  watch both stop), naming the wait. Articles the granted server lacks
  are chased through the other servers in order, backups last, each under
  a grant of its own, and the job fails as missing only after every
  server was tried; the pre-check STATs its shortfall the same way. A
  paused or outranked job gives its grant back; a grant the renewal finds
  gone is taken again, never assumed. One pod with `Ledger` nil runs the
  in-process `LocalLedger`, which is the same rules. Usenet is still one
  pod (`replicas == 1`): lifting that needs per-engine scratch and a
  StatefulSet, spec §6.
- **A running Job's template is re-sent, not re-rendered.** An SSA manager that stops sending
  a template field releases it, and a released field on a running Job is a rejected write. So
  `app/transcode/controller/pool.Render` re-sends the template recorded in
  `transcode.clustarr.io/applied-template`, and judges drift against that annotation rather
  than the stored template, which carries apiserver defaults.
- **A fired schedule has no `Nats-Msg-Id`.** nats-server strips it and adds
  `Nats-Scheduler` and `Nats-Schedule-Next: purge` (`scheduler.go:214-231`); a
  copy published with those onto a stream without schedules is refused with
  10188, which lost every scheduled task's dead letter until 2026-10-07. The
  envelope's ID rides in `Clustarr-Id` too, and `EnvelopeFromHeaders` drops every
  transport header (`Nats-Schedule*`, `Nats-Expected-*`, `Nats-TTL`,
  `Nats-Rollup`).
- **net/http's Shutdown counts a silent (StateNew) connection as active
  until it is 5 s old, so a browser preconnect open at SIGTERM made `ui.Run`
  burn its whole `shutdownGrace` and exit "graceful shutdown: context
  deadline exceeded" (Go issue 22682; 2026-11-10).** `Run` tracks silent
  connections through the ConnState hook and closes them at the cancel, a
  late accept closed as it arrives; and `/events/art` subscribes before it
  writes its 200, since a change landing between the headers and the
  subscribe was relayed to no one. An embedded NATS test server declares
  `JetStreamMaxStore` (64 GiB): without it nats-server caps the store at
  the host's free /tmp and refuses `ForSingleNode`'s 12 GiB of object-store
  reservations on a full machine.
- **The broker keys `InProgress`, `Ack` and `Nak` by stream sequence, not by
  delivery.** A lapsed handler could keep its redelivered copy from timing out,
  or move its deadline, so the bus mutes a lapsed delivery's `InProgress` and
  `Nak` (`Ack` and `Term` still go; `clustarr_bus_muted_total`). Double ack plus
  Msg-Id dedupe is not exactly-once after a lapse; the record fence is.
- **Object stores.** `Put` replaces an object's whole metadata (every `Put` is a
  complete declaration, like an SSA apply). Two concurrent `Put`s of one name
  leak a full copy for good, which only `PurgeOrphanChunks` reclaims, since the
  meta-level `List` cannot see it. A reader whose object is overwritten
  mid-`Get` stalls until its deadline, never a digest mismatch, so `/art`
  reads whole objects under a deadline before it answers -- and natsbus's
  `Get` hands out a reader that reports a read outliving the caller's
  context as `ctx.Err()` (2026-11-10, `objectReader`): nats.go v1.53.1's
  `objResult.Read` copies the deadline onto its pipe and shadows `err` in
  its timeout branch, so the same stall came back as `os.ErrDeadlineExceeded`
  or `nats.ErrTimeout` depending on which timer fired first, and `/art`'s
  retry fired only on the former (TestArtRetriesAnObjectOverwrittenMidRead,
  6 of 15 runs). A store's read ends with the context's error; the contract
  test holds natsbus to it. A link fires no watch event when its target changes, and nats.go
  refuses a link over any name a regular object ever held -- hence no links
  (ADR-0021). `ObjectStore.Watch` ignores `ResumeFromRevision` and `MetaOnly`
  (the bus refuses `WatchFromRevision`), and a bucket re-created under a
  running watch is skipped silently, so `objindex` compares `Status().Created`.
- **A whole-bucket watch or list of an object store costs the NATS server
  the bucket's size in memory, so the artwork bucket is watched through a
  metadata index stream** (owner, 2026-11-17: nats-0 and nats-1 at 1.5 and
  1.4 GiB of a 2Gi limit). A bucket's metadata records lie between its
  chunks, and nats-server starts a last-per-subject consumer by loading
  every block (`fileStore.MultiLastSeqs`), so each replay of
  `clustarr-artwork`'s 15 MB of records read all 1.46 GiB into the heap of
  whichever server hosted the consumer, held about two minutes until Go's
  forced collection. `objindex` replays at every ui and manager start; the
  ui's HPA flapped between 2 and 3 pods (85 scale-ups in 11 days, a 100m
  CPU request at 70%), and each start's watch timed out at nats.go's 5 s
  consumer-creation deadline and was asked for again after 1, 2 and 4 s.
  `ObjectStoreSpec.MetaIndex` (every bucket) has Ensure keep
  `OBJMETA_<bucket>`, a stream that sources `$O.<bucket>.M.>` from the
  bucket's stream with one message per subject, and natsbus's `Watch`
  replays that (`pkg/events/natsbus/objectindex.go`: 19 MiB against at
  least 185 MiB of heap for a 400 MiB bucket on the embedded server). The
  watch first waits until the index holds the bucket's latest record, by
  the `Nats-Stream-Source` sequence of the index's last message -- a
  source's reported lag is 0 before its first message, and a replay
  without the wait is partial -- and reads the bucket itself, as before,
  when the index is missing (an older manager), older than its bucket (a
  bucket deleted and created again; the next Ensure rebuilds the index) or
  still behind after a minute. An index event's `ModTime` is when the
  record reached the index. `List` and `PurgeOrphanChunks` read the index
  the same way, behind the same wait (owner, 2026-11-17: "Go with the
  recommended solution"), and all three buckets carry one: the maintenance
  sweep's purges, the fingerprint audit and the artwork reaper were five
  whole-bucket reads a sweep, about 5 GB across the servers, at every
  manager start and every 6 h. A purge deletes on the list's word, so one
  the index would have orphan at least 100 chunk subjects and more than a
  quarter of the bucket's is decided again from the bucket itself
  (`orphanGuardTrips`): a damaged index that still named the bucket's
  latest record had a test's purge take 159 of 160 live objects' chunks
  without it. A listed object's `ModTime` is its record's arrival, never
  earlier than its write, so both graces only spare more; and in the
  fingerprint bucket, which expires objects after 90 days, a record
  outlives its object by as long as it was copied after it, so for 90 days
  after the first fill a list may name expired fingerprints (reading one is
  `ErrObjectNotFound`, which the audit passes over). Both sweeps also
  record their last whole run in `clustarr-maintenance` (`pkg/lastrun`) and
  a manager that starts inside the interval waits out the rest: fourteen
  restarts on 2026-11-16 had been fourteen sweeps. nats.go's `ObjectStore.Watch`
  also unsubscribes when the context it was created under ends, so a
  longer creation deadline is `openContext`, whose deadline holds only
  until the watch is open (2 minutes), and `objindex` waits at least 10 s
  after a watch that ended before its replay. The first Ensure fills each
  index with one read of its bucket.
- **ffgo maps FFmpeg at package init, and a stale shim is silently wrong.** Any
  binary or test that links ffgo has dlopened FFmpeg before `main`, through
  `LD_LIBRARY_PATH` and a fixed list of system paths, and ffgo's loader
  remembers a failure, so `t.Setenv` is too late: native tests run through
  `make test`, which sets the variables, and a native test that skips under
  them is a failure, not a pass. A shim older than `ffruntime.MinShimAPI` reads
  FFmpeg 9's structs at FFmpeg 4-7 offsets; `ffruntime.Load` refuses it. A call
  that outlives its context is abandoned, not killed (`clustarr_ffgo_abandoned_calls`);
  at `ffruntime.MaxAbandoned` (4) the process reports itself wedged and its
  `ffgo` liveness check restarts it.
- **FFmpeg's memory is glibc's, which the Go collector never sees and glibc
  never gives back on its own.** Each OS thread that mallocs gets an arena
  of its own, and an arena keeps what its thread freed. On talos-88
  (2026-11-03) a transcode pool worker ratcheted from 319 to 757 MiB RSS
  over four hevc_qsv jobs while 2 MiB of its C heap was in use;
  `debug.FreeOSMemory` freed 4 MiB, and one `malloc_trim(0)` took it to 301
  MiB in 50 ms. So the native image caps the arenas at two
  (`MALLOC_ARENA_MAX`, `images/Dockerfile.native`), which bounds the ratchet
  at no measured encode cost, and `ffruntime` trims the C heap
  (`TrimHeap`) once no FFmpeg work has been in flight for `IdleTrim` (2 s).
  `Do` marks its calls in flight; an entry point that runs FFmpeg outside
  `Do` -- `engine.Run`, `Join`, `Verify`, the graft's, the pool's `Measure`
  -- marks itself with `defer ffruntime.Busy()()`, or the trim may land
  mid-call (safe, glibc trims each arena under its lock, but a stall).
  par2go needs neither: it repairs in a child process (`pkg/par2child`).
- **`agent --self-check` is the image's proof.** It checks the cgo SQLite piece
  completion (R13) and FFmpeg 9 with what the import and caption domains need;
  `markers --self-check` and `transcode --self-check=<class>` (`cpu`, `cuda`,
  `intel`; `--trial` encodes for real) cover the rest. `make docker-selfcheck`
  and CI run them all through `hack/image-checks.sh`, against an image whose
  binaries are UPX-packed: a packed executable hides the `DT_NEEDED` entries
  `lddtree` reads, so `images/distroless/stage.sh` traces the binaries before
  they are packed.
- **Every envtest fails under the CBOR client gates unless the environment
  turns them off.** `pkg/k8s/rowcache/cbor.go` sets client-go's
  `ClientsAllowCBOR` and `ClientsPreferCBOR` at init in every process that
  links it, every test binary included, and envtest's kube-apiserver (1.37.0)
  serves no CBOR, so each suite died at its CRD install with "the body of
  the request was in an unknown format" (2026-11-10). `make test` exports
  `KUBE_FEATURE_ClientsAllowCBOR=false KUBE_FEATURE_ClientsPreferCBOR=false`
  (`ENVTEST_ENV`); a hand-run envtest needs both beside `KUBEBUILDER_ASSETS`.
- **`controller-gen ... > config/rbac/<role>.yaml` truncated a role to zero
  bytes when a listed package did not compile** (2026-11-10, mid-wave,
  another agent's broken package): the shell opens the output before the
  generator fails. `make manifests` writes each role to a temp file and
  moves it; keep that shape for any generator whose output lands in git.
- **The unify branch was built implementation-first.** From Wave 2 on its tasks
  wrote no tests (owner, 2026-10-07): each recorded the tests and guards it
  skipped in `.superpowers/unify/progress.md`, test files that no longer compile
  were left for the batch, and `go vet ./...` fails on those packages until it
  lands. `go build ./...`, `make build`, `make generate manifests` (clean),
  `make brand-gate` and `helm lint --strict charts/clustarr` are the gates that
  hold meanwhile; a guard marked † above is one the batch writes.

- **An aggregated API that answers 404 for `/openapi/v2` is logged by
  every kube-apiserver, four times every five minutes (2026-11-26).** The
  External Metrics API (`app/autoscale/extmetrics`) was built to answer 404
  there, since the aggregator's downloader skips one (split design §9.4).
  The cache above the downloader does not: kube-aggregator v0.36's
  `cacheableDownloader.get` makes the 404 "resource not found" and `Get`
  wraps it, so the controller's test for that error misses it, logs
  `loading OpenAPI spec for "v1beta1.external.metrics.k8s.io" failed with:
  failed to download ...: resource not found` and retries on a backoff that
  starts over every five minutes (talos, v1.37.0: 60 lines in 75 minutes on
  one apiserver; the APIService stayed Available throughout). The handler
  now serves a Swagger 2.0 document with no paths and an entity tag, which
  the aggregator caches and re-checks each minute for a 304; `/openapi/v3`
  stays a 404, which the v3 aggregator reads as a legacy APIService and
  converts the v2 document. The test reads the document into kube-openapi's
  `spec.Swagger`, as the downloader does.
