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
    `client.New` / `ctrl.NewManager`**
    (`internal/cli/manager/probes_internal_test.go`): `librarytest`'s would
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
  - **A list by selector read the whole table until 2026-11-26.** The store
    answered `client.MatchingFields` and a label selector by reading every
    row with its ownership records, decoding all of them and filtering in
    Go, where the apiserver filtered a selectable field itself. The
    transcode ledger's rebuild is seven such lists of the MediaFiles
    (`app/transcode/admission/rebuild.go`), every five minutes: each read
    all 33,822 rows of talos, 205 MB, sorted on disk by Postgres (159 MB of
    temp files, 1.03 s), to keep one or two, and the manager's live heap
    rose from about 400 to 645 MiB each time (owner: "Can we design a fix
    for this?", then "Build it"). Now `Schema.narrow` (`selector.go`) puts
    the selection in the WHERE: a selectable field that is a string, a
    boolean or an integer as `COALESCE(<column> #>> '{path}', '')` (the
    apiserver's absent field is the empty string), `metadata.name` and
    `.namespace` as the key columns, a label's `=`, `in` and exists as a
    read of `body->'metadata'->'labels'`. It only narrows: every row that
    comes back still passes `selection.matches`, so a requirement with no
    SQL (a negated label) costs decodes and is never wrong. The migration
    makes one expression index per selectable field from the schema file
    (`<table>_sel_<path>`), and the list's condition must be that
    expression to the letter or the planner reads every row's status
    instead (458 ms for those MediaFiles):
    `TestEverySelectableFieldHasTheIndexItsListUses` explains each. Rows
    are decoded as they arrive and never held raw (`Store.scan`), and no
    list asks Postgres to order: the reader sorts what it kept by
    `<namespace>/<name>`, bytes, which is the apiserver's order where the
    database's collation is not. `clustarr_library_list_rows_total{kind}`
    counts what the database returned; a kind's rate at a multiple of its
    row count is the next caller that lists a whole kind on a timer.
    Found while writing the test: the apiserver refuses a name that is not
    a lowercase RFC 1123 subdomain and the store takes it (nothing here
    makes one; not fixed).

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
  **A season with every file was never asked for, and its backlog grant
  came back every cycle** (owner, 2026-11-27: Magi: Adventure of Sinbad S1,
  cutoff-unmet, granted "every 15 minutes for hours, which starves every
  other cutoff-unmet search"). A Series' search pass listed its Episodes
  only when a season passed `PackFirst` (two or more files missing), so a
  series holding every file counted no `BacklogDue` and no `ReplaceDue` and
  asked for no season, while its episodes were told to wait for exactly
  that query (`seasonBacklog`, `seasonReplaces`): nothing was searched, no
  `lastLiveSearchAt` was written, the grant lapsed, and the season was the
  oldest due again. On talos 40 seasons (480 episodes) could loop this way;
  of the replaceable episodes in them 235 of 349 had not been searched
  since 11-12. Now the pass also lists them for a Series the book names
  (`backlog.Book.GrantSeason`, set by the cycle before it wakes the Series)
  and for one with two or more replaceable Episodes
  (`itemread.ReplaceableIndex`; `search.couldAsk`), and a cycle leaves out
  a candidate still holding an earlier cycle's grant (`Candidate.Granted`),
  so a unit whose search does not start costs its queue one slot per lapse
  and not every slot. A count the planner reads is only as good as the
  pass that fills it: the episode-side tests of both rules passed all
  along. Guards: `TestASeasonOfFilesBelowTheCutoffIsAskedForAndNotGrantedAgain`
  (the cycle's grants through the stage's passes),
  `TestASeriesWithEveryFileAsksForASeasonOfReplaceableFiles`,
  `TestAnItemStillHoldingItsGrantIsNotDueAgain`.
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
  - **A full list keeps a blocked release's tombstone 40 minutes, not the
    hour** (`lifecycle.BlockYield`, 2026-11-27, owner: "correct this now",
    of "all 24 slots of the series' list are held by failed entries"). The
    pending grab kept the releases but not the pace: JoJo's Bizarre
    Adventure on talos held 21 tombstones and 24 pending grabs with nothing
    downloading, because every release ranked first for its episode was dead
    on every provider (one uploader's posts, taken down) and each failure
    held a slot for its hour while the releases that worked waited behind
    it. The tombstone's only job is refusing an answer computed before the
    block (`grabplan.CandidateWindow`, 30 min from the search's end); past
    that and ten minutes for a search in flight it refuses nothing, so an
    owner whose stored entries fill its cap (`decider.full`) drops it then.
    With room in the list it stays the hour as before. Only a confirmed
    Blocklisted entry yields: a `RefusedHold` or `LocalFaultHold` tombstone
    is the only thing keeping its release from the next answer, and stays.
    Guards: `TestABlocklistedEntryIsDroppedOnlyAfterConfirmationAndQuarantine`
    ("a full list keeps it only for the answer window", falsified) and
    `TestATombstoneYieldsOnlyAfterTheCandidateWindow`.
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
  (5.75 MiB then; a reply crosses in parts since 2026-11-27, see "A reply
  was one NATS message") compressed, and both engines read it through
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
- **A backlog item was due a search only when its cooldown ran out, however
  the search had changed since (2026-11-27, owner: "Adding additional
  indexers or changing the way indexers are searched should trigger
  re-search similar to what we do with schemas and metadata").** Two anime
  indexers were added that day and a dozen search and parse fixes rolled;
  of 270 missing anime episodes most had an approved release in a look-only
  search (all 51 of Sket Dance on amenzb, 181 approvals for D.Gray-man
  Hallow's 12), and none was due until 12-04, the missing queue's 7 days
  after searches made hours before the indexers existed. Now a cycle
  records the indexers automatic searches ask
  (`BacklogSearch.status.indexers[]`: name and a fingerprint of the
  indexer's uid, definition, base URL, settings, secret name, categories,
  anime switches, minimum seeders and tags -- not its priority, feed, pace
  or grab settings) and the build's `backlog.SearchVersion`
  (`status.searchVersion`); an indexer new to the list, a fingerprint that
  differs or a higher version sets `status.searchChangedAt` to now, and an
  item whose last live search is older than that is due whatever its
  cooldown (`backlog.Config.SearchChanged`). `perCycle` and `hourlyLimit`
  still pace it, so 730 missing items drain as season units over hours,
  not at once. An indexer removed or disabled changes nothing. A backlog's
  first reading takes the newest indexer's creation time, not now.
  **Raise `backlog.SearchVersion` with any change to how indexers are
  asked or how their answers are judged that can turn up a release an
  earlier search missed**; a fix that only refuses more needs none.
- **A 7-Zip archive posted in parts was renamed out from under its par2
  set: the cause of "the transfer's scratch files are gone or altered"
  (found 2026-11-28; the failure had been open since October).** A post of
  `GiDY8v0wuYowRRaQ.7z.001` and on (a film, obfuscated) was content to
  `classify`, so the magic-bytes step read the first part's 7z signature
  and named it `...7z.001.7z`; par2, whose set records `...7z.001`,
  repaired and put the name back; `adoptRepairedSet` added the set's name
  beside the list's stale one; and `verifyContent` then stat'ed
  `...7z.001.7z`: `writeError`, a local fault, the download thrown away.
  Now `x.7z.NNN` is an archive (`split7zRE`): it keeps its names, its
  first part is the set's entry point, sevenzip opens every part after a
  name ending `.001`, and the set is cleaned up as one. And no file whose
  name the par2 set records is renamed by its magic bytes, whatever it
  ends in. Tests: `TestA7zArchiveInPartsKeepsItsNamesAndIsUnpacked` (a
  real archive, `test/data/usenet/show.7z`, cut in three),
  `TestAFileThePar2SetNamesIsGivenNoExtension` (falsified).
- **One folder the recycle sweep could not remove stopped it for five
  weeks (found 2026-11-28).** `fsops.SweepRecycleBin` returned at the first
  day's folder that failed; on talos a file under
  `/data/.recycle/2026-10-22/cyberpunk-edgerunners-s01-dolby-vision-p5/`
  answered "permission denied" (a download recycled whole keeps its
  folders' modes, and a folder without its owner's write bit cannot be
  emptied), so every sweep since ended there, no later day was ever
  removed, and `import.RecycleSweepTask` was dead-lettered twice an hour.
  Now a folder that cannot be removed is passed (the error at the end
  names the first and counts the rest), and a permission error has every
  directory of the tree given its owner's rwx and the removal tried again
  (`removeAllOwned`). A tree another uid owns still fails, by name.
- **A torrent that trickles is never stalled, and a large publish held up
  every small one (2026-11-28, owner: "Design and implement fixes for all
  identified blockers/bugs as well as the smaller items").** The stall
  verdict (`lifecycle.decider.stalled`) reads the record's
  `lastProgressAt`, and a byte is progress: fifteen torrents moved 0.4 MB/s
  between them through one to four peers each, a 200 GB remux at 31 GB
  after 23.5 hours, and their 120 episodes read "downloading" with nothing
  else searched. Now `lifecycle.tooSlow` fails a torrent as `stalled`
  (blocked, redownload searched) once it has transferred for `SlowAfter`
  (12 h) and what is left would take more than `SlowHorizon` (48 h) at the
  rate it has averaged since it started; it reads only a record in its
  transferring stage, since an engine re-verifying after a restart counts
  what it has verified so far. A person's pause is not known to it, so a
  torrent resumed after a long pause may be judged at once. On the usenet
  side `Config.MaxPublish` (2) tokens were both held by 80 GB season packs
  for a quarter of an hour each, two more behind them, while a 500 MB
  episode and a 6 GB season waited half an hour for copies of seconds: a
  download over `largePublishBytes` (8 GiB) now takes a large token too,
  of which there is one fewer (`Client.publishLarge`), so one token is
  always a small download's and the large copies go one at a time, which
  the NAS's write rate made of them anyway.
- **A reply was one NATS message, and a season pack's .nzb does not fit
  one however it is compressed (2026-11-27, owner: "This one seems fixable
  - find a solution", "I'm guessing we can raise this?").** Three One
  Piece season packs from nzb-life (S13 153 GB, S17 182 GB, S21 302 GB:
  375 episodes) sat `Assigned` for a day. An .nzb is mostly message-ids,
  which are random: S13's gzipped to 6,142,018 bytes against
  `schema.MaxDownloadWireBytes` (6,029,312: the broker's 8Mi
  `max_payload` less base64's third and the envelope), and S21's is
  38.2 MB raw, over index's and the engine's 32 MiB caps. The engine
  retried "payload too large" on 30 s, 1 min, 2 min, each retry a new
  fetch, and nzb-life answered the fourth with its per-release 24-hour
  429, so the entry then read "the engine holds no transfer for this
  entry; adding it again" and held its episodes as downloading. Now a
  reply over one message's room crosses in parts
  (`pkg/events/natsbus/parts.go`: the first part carries
  `Clustarr-Reply-Parts`/`-Bytes`/`-More`, and the requester asks the
  responder's own subject for each further part by number, so nothing is
  pushed and `Request`/`Serve` callers see one reply), up to
  `events.MaxReplyBytes` (64 MiB); the three .nzb caps are 64 MiB
  (`app/indexer/download.MaxPayloadBytes`, the usenet resolver's,
  `pkg/download/usenet`'s), a release of about 500 GB; and a payload that
  is still too large is `payloadUnavailable` (`DownloadResponse.TooLarge`,
  the resolver's and the client's own cap errors), blocked and searched
  past, never fetched again. Both ends of an RPC must run the build: an
  older requester reads the first part as the whole reply (only for
  replies that did not cross at all before). Raising `max_payload` was not
  the fix: NATS takes 64 MB at most, every connection's buffers grow with
  it, and the servers here already stall on large messages.
- **A download a handful of articles short, with nothing to repair it, was
  thrown away whole (2026-11-27, owner: "For a few missing articles, we
  can likely force assembly and unpack anyway with 99.9% success").**
  `One.Piece.S16...Kitsune-xpost` (76 GB, 49 episodes, no par2) failed
  and was blocked for 1 article of 103,312, and a 79 GB Fate/kaleid
  remux for 4 of 107,066, every primary and the backup asked
  (`unverifiableGate`: bare content with a hole had nothing to judge it).
  Now `job.fewMissing` -- some, no more than `maxBadArticles` (5), and no
  more than one article in a thousand of the download -- lets such a
  download through: bare files are published with the hole where the
  article was, par2 that cannot repair is passed, and an archive is opened
  without its checksums (`withHoles`: rardecode's `SkipCheck`, a zip's or
  7z's checksum error at an entry's end kept as written). The importer's
  probe is the judge of each file (a few seconds missing from a long file
  are tolerated, 67d7baf0; a file that does not play is refused and its
  episode searched again), so 48 whole episodes are no longer lost for one
  article. One of four articles is still a failure (the share), and so are
  six of a hundred thousand (the count). The owner's other two points
  were already so since 158194b5 that afternoon: a chase asks the backup
  (`frugal-bonus`, `providers[].backup`) last, inside the one download,
  for the articles every primary refused, and `overBackupShare` keeps it
  from carrying a download; a backup never holds a transfer's primary
  grant.
- **An audio donor stayed under the root folder its series had when it
  was imported, and the graft refused it (2026-11-27, owner: "Fix these
  items - moving them into the new root").** An item records its donor at
  `status.audio.donor.path`, `<root>/.clustarr/donors/<item uid>/...`; a
  Series moved to another root folder takes its files along
  (`app/remediation/rename`'s relocation) and nothing took the donors. On
  talos 31 grafts of eight series moved from `tv` to `anime` (Log Horizon
  S2 18, The World God Only Knows S2 5) failed with `InvalidTask: donor:
  /data/media/tv/.clustarr/donors/... is outside root folder
  /data/media/anime` on every backoff, 35 Jobs an hour. Now the planner
  names the donor where it lies under the file's root folder
  (`grafttask.DonorUnder`, `Task.PlaceDonor`; the recorded path is left as
  the import wrote it and rides in `Task.DonorFrom`), and the graft worker
  moves every file of the item's donor folder there before it reads one
  (`settleDonor`: only where nothing stands yet, so a move cut short is
  finished by the next graft). The record's inputs keep the recorded path,
  so the attempt's identity does not change. Not done: the import's
  removal of a superseded donor still reads the recorded path, so after a
  move the older donor stays under the new root until the item is deleted
  (`librarydelete` removes the item's donor folder under the current
  root).
- **A post can carry a par2 set per file, and the engine read every job
  as one set (2026-11-27, owner: "investigate it", then "Yes to both").**
  `[pcela] Gargantia on the Verdurous Planet (2013) S01` (98.6 GB): 13
  episodes of about 7 GB, each with its own `<episode>.mkv.par2` and
  volumes (1 MiB slices, about 348 recovery blocks), and five more sets
  for the .md5/.nfo/.sfv/.sha1/.tth beside them; the files on disk carried
  the NZB subjects' names, the sets `[pcela] ... - S01Enn ...mkv`. One
  article of 94,229 was missing, in `[B8846714]`. Three places took one
  set for the job: `renameObfuscated` stopped at the first par2 file that
  completed a set (the smallest index, the .md5's: one rename);
  `par2IndexFile` picked the set with the most recovery blocks
  (`[BF067D90]`, another episode); and `Repair` handed par2 every other
  file as an extra. par2 found its one target missing by name and searched
  the other twelve episodes for it, and there met par2cmdline's own slow
  path: every episode opens with 2.66 MiB of zeros (a reserved gap, offset
  78,601), the set holds a whole slice of zeros, and once that slice is
  found each further byte offset inside a run of zeros matches it again by
  CRC, is hashed whole (MD5 of 1 MiB) and dropped as a duplicate
  (`ScanDataFile`: `if (duplicate && false) // ignore duplicates`, so one
  byte forward, not one block). 1.7 million hashes a file: eight files
  held eight cores with no read for 15-35 minutes each, then slid through
  their 7 GB at about 10 MB/s; the run was killed by a deploy at 77
  minutes of its 121-minute `par2Deadline`, started over, and would have
  ended by renaming one intact episode and publishing the damaged one as
  it was. Read it on the node: `talosctl processes` for the `agent
  par2-repair` child, `/proc/<pid>/io` (`rchar` flat) and
  `/proc/<pid>/fdinfo/<fd>` (`pos` stuck at 2-3 MiB). Reproduced with the
  library and with stock par2cmdline 2.0.0 alike: a 16 MiB target holding
  a zero slice and damaged elsewhere, beside an unrelated 8 MiB file with
  a zero run 20,000 bytes over a slice, verifies in 23 s against 0.16 s
  without the run; a set that is whole never scans its extras. Now
  `par2Sets` reads every set by the id its packets carry (the rename walk
  still stops at the first file that completes a set, per set, the files
  of one taken to be those sharing its name), each file takes its own
  set's name by its first 16 KiB, and a damaged start is matched by a
  slice only one file of one set owns (the sets share slices: the zeros).
  `job.par2Runs` groups the par2 files by set id at repair time, one
  header read a file, and for a job of two sets or more `planPar2Runs`
  runs par2 (`Repairer.RepairSet`) on each set that holds a file with a
  failed article, whose files are unknown, every set when a file's start
  is damaged, or one missing a file while a file no set names lost an
  article; its candidates are its own volumes and the files no set names,
  never another set's. A set whose files are all on disk under its names
  with every article verified is left alone, the quick check a set at a
  time; a failed run fails the job as one set's did. A job of one set
  (or none readable) takes the old path unchanged, extras and all, so the
  zero-run slow path could still be met by a one-set job with a foreign
  file: par2go v0.1.2 closes it in par2 itself
  (`shim/patches/0003-duplicate-run.patch`: the scan keeps the length of
  the run of one byte its window ends with, and a block of one byte
  repeated that has no entry to give is not looked up again a byte on;
  22.6 s to 0.13 s on the reproduction, the verdict unchanged; the first
  cut keyed on FindMatch's `duplicate`, which several identical blocks
  never set, and par2go's `TestALongRunOfZerosInAnExtraFileIsNotRehashedAtEveryOffset`
  caught it). Not offered upstream yet: the owner's to open. Tests:
  `TestAJobOfSeveralPar2SetsRepairsTheSetOfTheDamagedFile` (real par2),
  `TestPlanPar2Runs`, `TestPar2FileSetID`.
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
- **A file deleted from disk is found, and its record removed, without a
  person asking (2026-11-27; owner: "Fix the gap").** A record is removed on
  two observations (P46): the MediaFile reconciler's stat held for
  `mediafilespec.MissingGrace` (10 min of Ready False, `FileMissing`), and a
  library scan's lstat of the same path. Neither began for a settled,
  untranscoded file: nothing wakes its pass (no timer; the day's recheck was
  for `spec.original=false` only), so it was never marked, and a scan
  reported a record only once it was marked. Gosick S01E22's file was
  deleted by hand on talos and its record said "file present and probed" a
  quarter of an hour later; no root folder there has a `scanSchedule`, so no
  scan was coming either, and the episode kept counting a file it did not
  have. Four parts now close it:
  - the rescan's prune pass reports every record under the scanned path
    whose file is ENOENT and which the reconciler has either had missing
    past the grace or not marked at all (one lstat a record), and the scan
    applier rings the reconciler's doorbell for the second kind: the
    observed-fingerprint annotation with `mediafilespec.ObservedMissing`
    under `k8s.ManagerImport`. The pass's own stat is still the authority;
    a record inside the grace is left alone;
  - a scan of a folder that is itself gone (`scanMissingFolder`) runs the
    prune pass instead of ending at "nothing to walk": a season folder
    removed with its files kept every record otherwise;
  - `mediafile.RecheckInterval` (was `TranscodedRecheckInterval`): every
    settled file is stat'd again a day on, so a library that is never
    scanned still finds a file gone;
  - `app/import/controller/missingfile`: once a file has been FileMissing
    for the grace plus `Margin` (1 min), one incremental LibraryScan of its
    own folder is created (named by root folder, folder and the minute it
    was marked), whose prune pass is the second observation. It asks for
    nothing while more than `MassLimit` (100, my default) MediaFiles are
    marked at once -- a share that came back empty would otherwise have
    every record removed and every item downloaded again -- and those wait
    for a scan a person asks for.
  So a file deleted on the NAS reads Wanted within a day and eleven
  minutes, or eleven minutes after any scan of its folder. **An item still
  counts a FileMissing file as its file** (`episode.FileState` does not
  read the Ready condition): only the record's removal makes it wanted.
- **A RAR volume the NZB lists outside its set takes the name its articles
  carry (2026-11-27; owner: "Pursue the unrar fix").** 225 unpacks failed
  in a week on talos with every article in hand, "open
  .../<release>.part01.r00: no such file or directory", 216 of them Lupin
  the 3rd S04 (`...1080p.WEB.x264-ANiURL`), each grabbed again at the next
  search since a `writeError` blocks nothing. It was first read as
  rardecode lacking unrar's fallback between naming schemes; it is not.
  The release is an old-style scene set (`aniurl-lt3.s04e09.1080p.web.rar`,
  `.r00` ... `.r09`), and nzbgeek's NZB lists the first volume as
  `<release>.part01.rar` (and the par2 files as `<release>.vol-0N.par2`)
  while leaving every other file as posted. The archive header says old
  numbering (main header flags 0x0101: volume, first volume, no new
  numbering), so the next volume is looked for beside the listed name;
  `unrar t` fails the same way on those names ("Cannot find volume
  ...part01.r00"), so no change to the library would help. The par2 set
  cannot rename it either: it describes the sample alone
  (`...web-sample.par2`). The poster's name is on the wire, in the first
  article's yEnc `name=`, which the engine already fetches first for the
  file's size and threw away. It is kept now (`job.wireNames`, in the
  manifest), and `renameStrayVolumes`, after the par2 names, gives a file
  its wire name only when all of these hold: its bytes are a RAR volume's;
  its listed name is in no set with another of the job's files; the wire
  name is a RAR volume's, in a set another of the job's files is listed in,
  and unheld. SABnzbd takes the yEnc name for any file that does not look
  obfuscated (`verify_nzf_filename`); this is narrower on purpose, because
  an obfuscated post names every article differently (the entry above) and
  the importer reads a media file's name. **How it was found, for the next
  one:** the transfer record's `files[]` are the listed names; the NZB
  comes from the indexer's `t=get`; one `BODY <message-id>` per file shows
  `=ybegin ... name=` and the first bytes. The tests' archives are real:
  `storedRarVolumes` writes a stored RAR 3 multi-volume set that `unrar t`
  accepts, since the tree had no RAR fixture at all. Not covered: the
  week's other three failures (`...CtrlHD.part10.rar` missing, a volume
  that was never posted or never listed).
- **A file the import ran out of time to judge is retried, never imported
  by name (2026-11-27).** `fileimport.probeVideo` treated every probe
  failure but "not media" as harmless -- "could not probe the file;
  importing it under its name-derived quality" -- and returned before
  `videoHealth`, so a probe that hit its 15 s (`videoProbeTimeout`) skipped
  the end-to-end read too. On talos eleven single-episode downloads of
  `Gosick.S01Exx.1080p.CR.WEB-DL.AAC2.0.H.264-BiOMA` arrived with every
  article fetched (`failed: 0` of about 2,150) and 14 to 16 of their 24
  minutes zeroed, in stretches on a 32 MiB grid; ten were refused and
  blocklisted, and S01E22's probe timed out while both import pods read
  the others end to end (16 to 106 s a file), so it was placed in the
  library unread, 49% zeros, and planned for a transcode. Now a probe or an
  end-to-end read out of its bound is `errUnjudged`, which
  `notMediaRejection` turns into a *transient* rejection: the import is
  inspected again on `importplan.RetryLadder` (1, 10, 45 min) and held for
  a person after the last, never blocklisted; and a header probe that
  failed any other way is still followed by the read. Only a read that
  fails for a reason of its own (no decoder) still imports unjudged.
  **The damage was in the post, not the engine:** S01E23 was fetched twice,
  from nzb-life's listing and then nzbgeek's, an hour apart, and both
  copies read "16m25s of 24m29s is missing ... in 11 places (the longest
  4m8.1s)"; other releases through the same engine in the same minutes
  were whole. So `failed: 0` says the articles arrived, not that the file
  is whole: the Fena gates below catch what an NZB does not list, and only
  the end-to-end read catches zeros the poster uploaded. Checking a library
  file for holes by hand: a one-off busybox pod with the `clustarr-data-nas`
  claim mounted read-only, `dd ... | tr -d '\000' | wc -c` over 16 MiB
  blocks (a whole video is about 1% zeros). Each FFmpeg line now names its
  file (`ffruntime.DoOn`, `docs/observability.md`): the 116 "invalid as
  first byte of an EBML number" warnings of that night named none.
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
  than `mediainfo.Health.Tolerated` gone (2 s, or five thousandths of a
  longer file's length up to 10 s, since 2026-11-27 -- owner: "losing 4sec
  of a 30min show seems acceptable - and like we should take the media
  file anyway", of Kemono Friends S2E06, 4 s of 22m55s) from *every* audio and video
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
  **A row read again is copied, not decoded (2026-11-28, owner: "Do the
  kubernetes libraries included in the project ... provide more efficient
  methods of handling this?", then "Design and implement fixes for all
  identified blockers").** The libraries' method is the one the rows
  replaced: controller-runtime's informer cache keeps every object decoded,
  so a Get is a map lookup and a DeepCopy (none under
  `UnsafeDisableDeepCopy`), and beyond `ByObject` selectors, cache
  transforms and metadata-only informers they offer nothing between that
  and no cache. The rows' price showed once a long series was worked: a
  30 s profile of the manager, held at its 750m limit, had 63% of its CPU
  in `rowcache.(*codec).decodeInto` -- every item pass reads its Series for
  its monitoring, owner, settings and profile, stage by stage, and One
  Piece's is 168 KB of JSON, 1.9 ms a decode, with a thousand episodes in
  flight -- and another 10% in `transcodeprofile.counts`, which listed
  17,000 MediaFiles to count them. Thirty interactive searches the agents
  answered in seconds were unfolded 30 minutes later. Now each row kind
  keeps the objects read most recently decoded (`rowcache/memo.go`:
  `memoBudget`, 4 MiB of rows' JSON a kind, about 40 MB decoded at the
  most across the five kinds; an entry is the row it came from, so a
  changed object is never served stale): a repeat Get or a short List is a
  DeepCopy (33 µs for One Piece's Series, 4 µs for an average one), and
  under `UnsafeDisableDeepCopy` it is the memo's own object, which
  therefore must not be written (it bought nothing on these kinds before).
  A List of more than `memoListLimit` rows reads the memo and does not fill
  it. `transcodeprofile.counts` counts keys (`rowcache.Keys` on the cache).
  The CPU limit itself is the owner's and unchanged.
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
  **The manager talked to the apiserver in CBOR from 2026-11-04 to
  2026-11-26, and no longer does** (owner, 2026-11-26: "We should also
  consider ditching the cbor encoding/decoding for the api server and use
  the upstream controller-runtime go dep"). It cost a controller-runtime
  fork (`github.com/mediactl/controller-runtime v0.25.1-clustarr.3`, three
  commits: its client's and cache's codec factories through
  `rest.CodecFactoryForGeneratedClient`, and an apply's answer decoded by
  content type), client-go's `ClientsAllowCBOR` and `ClientsPreferCBOR`
  gates turned on at init (`pkg/k8s/rowcache/cbor.go`), the apiserver's
  alpha `CBORServingAndStorage` gate on every cluster, and two environment
  variables to turn the client gates off under every envtest. The kind rig
  never got the apiserver gate, so its manager crash-looped on "415
  Unsupported Media Type" and the end-to-end suite did not run from
  2026-11-04 until 2026-11-26. What it bought (caches synced in 30 s
  against 56 s, a lower heap while they did) was the decode of the large
  kinds, and those are rows in Postgres now (ADR-0024): about 3,500 objects
  are left to list from the apiserver. `go.mod` has no replace for
  controller-runtime, `cbor.go` and `ENVTEST_ENV` are gone, and the tree
  built against upstream v0.25.1 with no other change. **talos's apiserver
  still has `CBORServingAndStorage` on, and it stays on**: the gate is also
  what makes CBOR the storage encoding for custom resources, so objects
  written since 2026-11-04 may sit in etcd as CBOR; nobody has checked what
  an apiserver with the gate off reads of them. A JSON client is served by
  either.
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
- **At most `spec.torrent.maxSeeding` torrents seed at once (2026-11-27;
  owner: "We should only allow a max of 3 seeding torrents at a time.
  Torrents that have seeding turned off immediately move their files off
  the node's storage and into the NAS storage (unless the files were
  already moved - in which case, we delete them)").** The field is new on
  DownloadClient (0 or unset: no cap; read at start, so a change rolls the
  engine), and the cap is the engine's, in the one place a seed goal is
  judged (`pkg/download/torrent`, `Client.capSeeding`, on every `List`):
  while more than the cap are seeding, the ones with the most cumulative
  seed time have their goal counted as met, sticky and persisted like any
  met goal. Seed time is the key because it is the only order that
  survives a restart; a newly completed torrent has none, so it takes its
  place from the one that has had the most of its turn (my choice of
  which, told to the owner). Nothing else was built, because the second
  sentence is what a met goal already does: a torrent is moved from
  scratch to `publishDir` when it completes, whatever its seeding; with the
  goal met it uploads nothing; one already imported goes Seeding ->
  Removing with its data (`lifecycle` transitions, `removeCompleted`), and
  one not yet imported keeps its files for the importer and is removed
  after it. A paused or failed torrent holds no place. On talos the
  torrent scratch is a path on the NAS, so "the node's storage" is not in
  play there yet.
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
- **A release whose title cannot be read is still held to the item's state:
  the grab planner failed open on it (2026-11-27, owner: "Fix the
  runaway-grab defect").** `decision.ItemStateRejections` answered a failed
  `ParseForItemState` with no rejection, so the release was held to no
  item-state rule at all: not TranscodedFinal, not ExistingHigherPreference,
  not the queue. A read fails most often by timing out (the record above),
  and the manager's CPU limit is 750m: while it sat at that limit judging
  season answers, seven weekly singles were taken over transcoded files of
  As a Reincarnated Aristocrat's third season (17:55-17:57Z), and some
  twenty-five German singles of Date A Live's first beside two packs still
  downloading and then over the files those packs brought (18:06-18:27Z);
  each logged `complete=false`, which is `ReleaseCompletesAudio` of a nil
  parse. A replay of the same records, profile, entries and files through
  `grabplan.Decide` refuses every one, and a grabbing search at 18:51Z,
  the manager idle, took nothing: the stored state was never the cause.
  Now `release.Parse` reads a title again when its read timed out
  (`timeoutTries`, three); `ParseForItemState` judges a release whose title
  still fails by what its search read it as (the quality, languages and
  type on the ReleaseInfo), with a Warn for a timeout; and a release read
  as nothing is `UnableToParse`. Tests:
  `TestParseReadsATitleAgainWhenItsReadTimesOut`,
  `TestAReleaseWhoseTitleCannotBeReadIsStillHeldToTheItemsState` (five
  cases, all failing with the old return). Not changed: the 50 ms limit,
  the manager's CPU limit, and a timeout inside `catalogue` matching.
- **A pack taken for one episode holds it as a pack, and a hold over a
  final file ends the tries (2026-11-27, the same ask).** An entry names
  the episodes its release was taken for (P23), `DownloadEntry.Pack()` is
  `len(Episodes) > 1`, and a candidate pack passes every queued single (it
  replaces them): so a season pack taken for the one episode that wanted
  it let the answer's next pack through at the following pass. On talos
  eight packs of Aharen-san wa Hakarenai's second season, 3 to 62 GB, were
  taken for its eighth episode in under three minutes (clustarr-8a found
  the cause). `grabplan`'s `entryPack` and `itemread.Queue` now read a
  one-episode entry's release title (`decision.PackTitle`). And where the
  planner takes a release over a transcoded file by a rule import does not
  share (a title read as dual audio whose file is not), import holds it
  "the file it has is final" and the planner took the next: now a download
  held `itemState` over a file that is final refuses every further
  automatic release for that item (`itemCheck`), until the hold is removed
  or lapses. A person's pick passes. The grab line also names what was
  beside the grab: `entries` (how many the planner saw) and `beside` (the
  live or held entries covering an item it is taken for). Tests:
  `TestAPackTakenForOneEpisodeHoldsItAsAPack`,
  `TestAHoldOverAFinalFileStopsTheNextRelease`.
- **A torrent pack is passed over for usenet episodes that cover what it
  would be taken for (2026-11-27, owner: "I *would* prefer usenet based
  episode downloads for a season over torrent season downloads ... But
  *only* if **all** episodes are found and approved", then "if usenet
  accounts for *most* downloads and torrent episodes fill the rest").** A
  torrent here runs under 1 MB/s most of the time and usenet at 20 to 80,
  several episodes at once. `grabplan.usenetCovers`: on a Series, an
  automatic torrent pack is refused when every episode it would be taken
  for has another release (a usenet release of any shape, live or approved
  in the pass, or a torrent single approved in the pass) and usenet has
  more than half of them ("most" is my reading); a pack that would be
  taken for one episode is passed over for any single of it. The other
  release must be of no worse a tier, no last resort unless the pack is
  one, and dual audio when the pack is (under `completeFirst`). Live
  usenet singles count, so the pack does not replace them at the next
  pass (the 2026-11-03 "a pack replaces the singles" stands for a usenet
  pack and for torrent singles); when they fail they stop counting and
  the pack is taken. Test: `TestATorrentPackIsPassedOverForUsenetEpisodes`.
- **A torrent pack is the last of three ways to a dual-audio episode
  (2026-11-27, owner, of JoJo's Bizarre Adventure: "For jojo episodes where
  we can't find a working dual audio, download the jpn release and an eng
  release and perform the audio graft. If no eng release exists whatsoever
  that we can download (unlikely) - we can then finally fallback to a full
  season re-download from torrent for dual", and "a dual lang or eng lang
  release of the single episode will **also** be higher priority than the
  season torrent at that point").** The pack rule above counted only
  releases that carry every wanted language, so once one uploader's
  dual-audio usenet posts had failed (taken down on every provider) the
  planner took a 162 GB five-season torrent for 26 episodes. Under a
  grafting audio policy `grabplan.grafts` now counts an episode as had
  without the pack when (a) releases approved in the pass, none a donor, a
  person's pick or a torrent pack, and none of a worse tier than the pack,
  together carry every wanted language (`decision.ReleaseMissingAudio`):
  the best is grabbed as any release is and the donor search finds the
  other; or (b) its file lacks a language and the graft can still supply
  it: a donor is live or approved in the pass, or `Covered.GraftOpen`
  (`rollup.GraftOpen`: a donor downloading, the graft under way, or fewer
  than `GraftDonorSearches`, 3, donor searches run with none found; never
  a file that backs several items). `usenetCovers` counts such an episode
  as usenet's, and `outranked` does not let that pack outrank a release
  lacking a language whose every episode is so covered (a raw or a dub
  alone is a last resort by its format, which any other release
  outranked). A dual-audio single still outranks both and is taken first.
  The rule is the profile's, not JoJo's: every series under a grafting
  policy (the anime profiles) follows it. Not built: the two releases are
  not grabbed together, so the English one waits for the donor search
  after the Japanese file is in; and an episode with no English release
  sends its whole season to the pack, as "every episode it would be taken
  for" did before. Tests: `TestATorrentPackIsTheLastWayToDualAudio`
  (falsified), `TestAGraftIsOpenUntilItsDonorSearchesRunOut`.
- **A torrent pack whose grafts would be most of what it brings is taken
  rather than them (2026-11-27, owner, of Sacrificial Princess and the King
  of Beasts S1: "we should take a season release over episodes when
  episodes are already present for a season ... We should have a limit for
  which it's preferable to download individual episodes - where usenet is
  the primary heavy weight - torrent follows the fallback to torrent season
  if all else fails model we already have in place"; the owner's limit:
  half of the episodes the pack brings).** The season is 24 episodes: 17
  Japanese-only files from 11-02/03 singles (taken the evening the
  no-pack-while-singles-download rule was live) and 7 with none. At 14:09Z
  the planner took AV1ARY's dual-audio torrent pack of exactly that season
  (nyaasi), which died at 16:54Z on the NAS `input/output error` under
  `/data/torrents/incomplete`; the owner took the same pack from the
  `animetosho` usenet indexer by hand at 23:40Z. Under the rule above
  (a96abf59, not yet live then) the pack would have been refused instead:
  every episode counted as grafted (17 open grafts, 7 by a Japanese single
  and an English donor), so 24 donor downloads and 24 grafts in its place.
  `grabplan.cover` now counts, of the episodes the pack would be taken
  for, those only a graft completes, and `graftsOutweigh` takes the pack
  when they are more than half of its payload (`payload`, every episode
  its coverage names): `usenetCovers` does not pass it over, and
  `outranked` lets it outrank the last-resort releases it spares
  (`packOutweighed`), so none is grabbed while the pack waits on its
  indexer. A usenet release that carries every wanted language by itself
  still counts for usenet with no limit, and a usenet pack is untouched
  (it outranks singles and replaces Japanese-only files already). JoJo's
  pack, five seasons taken for 26 episodes, stays the last of the three.
  An episode whose donor is already downloading still counts as grafted.
  Tests: `TestATorrentSeasonPackOutweighsItsGrafts` (the limit, its
  payload measure and the `outranked` half each falsified), and
  `TestATorrentPackIsTheLastWayToDualAudio` now gives its pack seven more
  episodes with dual-audio files, as JoJo's had.
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
- **A transfer leaves a primary that does not hold its post, and a backup
  fills a few articles, never the bulk of a download (2026-11-27, owner:
  "ensure frugal bonus is reserved for downloads that have a *few* articles
  missing ... For downloads that will depend on frugal-bonus, blocklist and
  grab the next", then "frugal gets blacklisted when it's missing too many
  articles and then the download has to use easynews - reconciling the
  connections used").** On talos thirteen episodes of one poster (nzb-life,
  `...DUAL-FRNcR`) were on none of frugal, easynews and newsdemon. Each held
  a transfer slot and its 13-15 connection grant while its workers asked the
  granted server for every article, then its chase asked each other server
  for every article in turn on a chase share (frugal and easynews take about
  half a second a connection to refuse one: 8-14 minutes an episode), then
  took what frugal-bonus had on two connections (20 / maxActive); the
  ledger read "frugal 50/50 in use", the engine moved 1.4 MiB/s, and ten
  downloads waited, a 92 GB one a single article short. frugal-bonus never
  held a whole post (E10: 1,474 of 4,466). Now, in `pkg/download/usenet`:
  `leave.go` -- a primary that refuses more than half of at least
  `lackSample` (50) articles the transfer's workers asked it for is left:
  its grant goes back at once, no grant is taken on it again
  (`holdingPrimaries`), what it refused is asked of the next primary as
  transfer work (the requeue, in rounds after the batches), and when the
  last primary is left with more refused than fetched the download fails as
  missing ("no primary server holds the post"); with more fetched than
  refused the judgement is forgotten and the failures are the chase's.
  And `overBackupShare` (segment.go): a chase does not ask a backup when,
  with what it is about to ask, the backups would have supplied more than
  `maxBadArticles` and more than 100 less `abortHealthPercent` percent of
  the download (a tenth), or more than half of what the transfer has asked
  for once that is 50; the articles count as missing, and while a primary
  could not be asked the chase waits (`ErrProvidersUnavailable`) and judges
  nothing. Neither count is kept in the manifest: a restart starts both
  again. Tests: `TestATransferLeavesAPrimaryThatDoesNotHoldThePost` (its
  first case fails without the rule: 601 articles asked, three connections
  held), `TestABackupIsNotAskedForTheBulkOfADownload`. Four older tests fed
  a backup a quarter to two thirds of a post and now refuse one article in
  twelve. Not changed: a chase still holds the transfer's grant while it
  waits on another server for a post the granted one holds with holes, and
  the pre-check's `chaseExists` asks every server. The package's tests
  report data races in `client_test.go`'s reads of a job under
  `go test -race`; they are there without this change.
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
- **An object store at its byte limit refuses every write, and nothing said
  so (2026-11-28; owner: "Keep the jetstream object store design for now
  and design your fixes around that. Design and implement iron clad fixes
  for all issues you identified").** A bucket's stream discards new. talos's
  `clustarr-fingerprints` stood at 1,073,741,677 of 1,073,741,824 bytes:
  every fingerprint write refused, and ignored, since the worker treats the
  store as a cache. Three quarters of it was garbage: of 42,580 probe hashes
  stored, 6,675 were a file's, 4,449 a hash a file had had, and 31,456 no
  file's at all, while 28,134 files had none. A probe hash is
  sha1(path|size|mtime), so every rename, move and transcode strands the
  fingerprints under the old one, and only the 90-day `MaxAge` ever removed
  them. Four things hold it now.
  - **The reap** (`fingerprintmeta.ReapStore`, a `maintenance.Reap` on every
    sweep): a fingerprint whose probe hash no MediaFile has or had
    (`mfindex.ProbeHash`, asked as keys through `rowcache.Keys`, never a list
    of every file: that decodes every row), one of an old
    `FingerprintVersion`, or a name that is no key, is deleted once older
    than the sweep's grace. A lookup that fails deletes nothing, and a store
    of 20 hashes or more none of which is referenced is refused
    (`ErrNoRefs`): that is a lookup answering nothing, not a library.
  - **Eviction** (`ObjectStoreSpec.Cache`, `maintenance.CheckFill`): every
    10 minutes each store's size is read against the limit the broker holds
    (`ObjectStoreStatus.MaxBytes`); a cache store at 85%
    (`events.ObjectStoreNearlyFull`) gives up its oldest-written objects down
    to 70%. Fingerprints and payloads are caches; artwork and Live TV are
    not, and are only logged ("nearly full", again every hour), since an
    eviction there takes an image or a guide a page is showing. Not
    JetStream's own discard-old: it drops messages, not objects, and an
    object's chunks are older than a record `SetMeta` wrote again, so it
    leaves records with no content.
  - **It is seen**: `clustarr_object_store_bytes` and `_max_bytes`,
    `clustarr_objects_evicted_total{reason}`, and
    `clustarr_object_put_errors_total{bucket,reason}` from every writer; a
    refused write is `events.ErrObjectStoreFull` (the broker's "maximum
    bytes exceeded", which has no error code of its own).
  - **Room**: fingerprints 2 GiB (some 80,000 files at 12 KB a window),
    artwork 8 GiB (it was 3.2 of 5 and had doubled in ten days; 1.2 GB of
    it is disc art and 0.6 GB clearart). The servers' `max_file_store` is
    30Gi and the streams reserved 20.4 GB of it before this; it is about
    25.4 GB with the payload store below. A new bucket or a higher limit
    has to fit under that, or Ensure fails for every stream.
- **A fetched .nzb is kept, so a release is fetched from its indexer once
  (2026-11-28, same ask).** Every payload request reached the indexer:
  nothing held what the last fetch brought, and a fetch is a grab whatever
  becomes of its reply (lost in transit, too large, an engine that
  restarted or lost the transfer), so the next one met nzb-life's 24-hour
  per-NZB 429. The index agent now stores each fetched .nzb, gzipped, in
  the object store `clustarr-payloads` (1 GiB, three days, a cache) under
  `nzb/<indexer uid>/<sha256(guid)[:32]>` and answers a release it holds
  before anything else (`app/indexer/download/kept.go`; metric result
  `kept`): no pause, limit or cooldown applies, since the indexer is not
  asked. Only an .nzb: a .torrent carries its tracker's passkey and the
  bucket is readable by every bus client. The name never holds the GUID,
  which is often the download URL with its key. A kept object that does
  not read back as an .nzb is removed and the indexer asked.
- **The ui serves `net/http/pprof` on `127.0.0.1:6060`
  (`--pprof-bind-address`), as the manager does** (2026-11-28: a ui at
  750 MB of heap, 953 MB resident, could not be asked what held it; its
  artwork byte cache is capped at 64 MiB and `/art` had served four
  requests in 2.5 hours, so that is not it). `kubectl port-forward
  pod/<ui> 6060`, then `/debug/pprof/heap`.
- **`%w (%v)` in natsbus's object reader is on purpose.** A formatting
  commit (73a418fd) turned it into `%w (%w)`, which put nats.go's private
  `ErrTimeout` back into the chain a caller matches and failed
  `TestObjectReaderReportsTheCallersDeadline` on main; the lines carry
  `//nolint:errorlint` now. The same commit did it to
  `pkg/par2child/exec.go` and two lines of `ui/livetv_channels.go`, which
  were left as they are.
- **A failed object `Put` can leave a record with no content, and a record
  alone is not the object** (2026-11-27; owner, of the series Beef:
  "has no image", then "Repair the 31 now ... Afterwards, stop it
  recurring"). nats.go's `ObjectStore.Put` (v1.56.0, unchanged on its main
  that day) publishes the chunks and the record without waiting, then waits
  for every acknowledgement, and on any error purges the chunks it wrote --
  though the record may be stored by then. Two real ways in: an
  acknowledgement slower than the 5 s a publish is given ("nats: timeout
  waiting for ack"; the handler's context has no deadline, and with one
  nats.go returns `ErrTimeout` and purges nothing), and a bucket with no
  room for the chunk while the small record still fits
  (`clustarr-artwork` discards new at 5 GiB). What is left answers `Info`
  and stalls every `Get` to its deadline. On talos: 31 poster overlays in
  ten days (19 movies, 12 series), each written within seconds of nats
  logging `Internal subscription on "$JS.API.STREAM.INFO|PURGE.
  OBJ_clustarr-artwork" took too long` (6 to 10 s; 2 to 35 such lines a
  day), 14 of them with the ack timeout logged 5 to 9 s after the record's
  time. The renderer's retry then read the record, found its own inputs
  digest in it and answered "unchanged"; the overlay stage reads the index
  and saw an overlay; `/art` picked the overlay, stalled twice and answered
  500. Four things hold it now. natsbus's `Put` looks at what a failed Put
  left and deletes a record whose chunks the stream's leader does not count
  (`dropHollowAfterPut`; a subject-filtered stream info, never a direct
  get, which any replica answers). `PurgeOrphanChunks` does the same for
  every record older than the grace that the listing found no chunks for,
  each read again from the bucket first (`dropHollowRecords`): the net
  under the first, whose look comes while the stream is slowest, and it
  covers originals and fingerprints, whose writers also ask `Info` and skip.
  The renderer reads and hashes a recorded overlay before it answers
  "unchanged" (`held`). And `/art` serves the original for an overlay it
  could not read, under `no-cache`, passing that digest over for five
  minutes (`unreadableArt`). Counted in
  `clustarr_object_hollow_records_removed_total{bucket}`. A hand repair is
  `nats object rm clustarr-artwork <name> -f` from the nats-box, after
  checking that the record's `nuid` has no `$O.<bucket>.C.<nuid>` message:
  the stage draws it again within seconds. Finding them: the records from
  `OBJMETA_<bucket>` (`nats sub --stream ... --all --raw`) against `nats
  stream subjects OBJ_<bucket> '$O.<bucket>.C.>'`, neither of which reads
  a chunk. The non-atomic `Put` is nats.go's gap; no issue was filed.
- **Writing an object again makes nats-server read every block written to
  the bucket since that object was last written, under the stream's lock**
  (2026-11-27; owner: "Determine the cause of the 6-10 second stalls on the
  artwork stream. Check if it's an upstream issue"). This is what stalls
  `OBJ_clustarr-artwork` for 6 to 10 s several times a day, and so what
  times out a `Put`'s acknowledgement (the entry above). nats-server
  v2.15.3, and its main at 52f36cf2 the same day. An object's record is
  published with `Nats-Rollup: sub`; the stream stores it and then runs
  `purgeLocked({Subject, Keep: 1})` inside `processJetStreamMsg`, holding
  the stream's lock. `PurgeEx` removes the previous record, and
  `purgeLocked` then asks `FilteredState(firstSeq, subject)` for the
  consumers' sake. The file store keeps, per subject, the first and last
  block it is in (`psi.fblk`, `lblk`), and `fblk` is lazy: a removal never
  moves it ("We do not update sense of fblk here", `removePerSubject`), and
  only a stream with `MaxMsgsPerSubject: 1` has it corrected on write, which
  an object store's stream is not (-1; the rollup does that job). So
  `numFilteredPendingWithLast` looks in the block the record was just
  removed from, misses, and walks forward block by block to the one the new
  record is in, loading each block's per-subject state, which is the whole
  block read from disk once it has been idle two minutes
  (`defaultFssExpiration`). Then it corrects `fblk`. The cost of a rewrite is
  therefore the bytes written to the bucket since that object's previous
  write, at most the bucket; a first write costs nothing. Reproduced on an
  embedded v2.15.3 with nats.go's own object store (150 KiB objects, 2,000
  per span, the server idle 150 s, bytes counted from `/proc/self/io`): a
  new object read 1.8 MiB, an object last written one span ago 293.7 MiB,
  and the stack through it was `internalLoop < processJetStreamMsg <
  processJetStreamMsgWithRollup < purgeLocked < FilteredState <
  numFilteredPendingWithLast < filteredPending < ensurePerSubjectInfoLoaded
  < loadMsgsWithLock < loadBlock`, with a direct get of another object
  waiting on the stream's lock for as long. It fits what talos showed: only
  overlays, the one variant written again whenever a rating moves; every
  replica at once, since each applies the record; 0.3 to 1.3 GiB read from
  the NVMe and as much less memory available on the leader's node in the
  half minute of a stall; and nats's memory spikes. The server's own logs
  name the victims (`STREAM.INFO`, `STREAM.PURGE` and `DIRECT.GET` "took
  too long"), never the scan. A single-subject purge is not the cause:
  `PurgeEx` is bounded to the subject's blocks since upstream PR 8004. No
  upstream issue describes this (searched: fblk, numFilteredPending,
  rollup, purge); PR 5545 and 5577 (2024) added the lazy correction, PR
  8234, which gave a subject its full set of blocks, was closed unmerged.
  What holds it here, the server unchanged: every object store is bound
  through a second JetStream context whose default timeout is 30 s
  (`objectStoreTimeout`; owner, 2026-11-27: "Start with this for now"), so a
  `Put` caught in a stall waits for its acknowledgements and succeeds --
  the stall itself is as long as before for every reader and writer.
  Still open: overlays in a bucket of their own, so a rewrite scans other
  overlays only (148 MiB of the bucket's 2.99 GiB that day; the rest is
  originals, 1.15 GiB of it disc art and 538 MiB clearart, and nothing in
  the bucket is other than an item's images). More CPU for the nats pods
  (limited to one) the owner would rather not. `/stacksz` on the monitoring port (8222,
  through the apiserver's pod proxy) dumps a live server's goroutines,
  which is how to see a stall's holder.
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

- **A series whose broadcasts each hold several of TVDB's episodes is
  released under three numberings, and a download is attributed at import
  as it was judged at the grab (2026-11-26).** TVDB lists Panty & Stocking
  with Garterbelt's short episodes one by one (30 in season 2); TheXEM maps
  the thirteen broadcasts onto them (scene S02E07 is TVDB S02E14 and
  S02E15). On talos a release numbered by TVDB under its episode's title
  (`S02E07.Daten.City.President`, six minutes) was grabbed for S02E14 and
  S02E15; `New ... S01E07.14.15` (TheXEM's scene title for season 2) for the
  2010 season's S01E14; and import, which read every file literally, held
  each against the episode its number names. Before that a season pack of
  the thirteen broadcasts was imported as S02E01 to S02E13, so the library
  showed 23 episodes missing that were on disk. Now `release.Parse` keeps
  the numbers a title lists after its one SxxEyy (`ListedEpisodes`) and the
  episode's title (`EpisodeTitle`), and `decision.releaseCoverage` reads,
  beyond Sonarr: the scene season a release's title names
  (`Identity.SceneSeasonNames`, TheXEM's names), a listed broadcast as its
  scene row when the row maps onto exactly the list and as every number
  written otherwise, and a release titled as the TVDB episode its number
  names as that episode (`SceneMapping.Title`, set by
  `search.SceneReadings`). The import agent reads TheXEM itself
  (`fileimport.Worker.SceneMaps`, its own client and cache as the catalog
  agent's) and attributes a download's files through the same function
  (`MatchDownloaded`); a download's one file is narrowed to the episodes it
  was grabbed for (`narrowToGranted`), so the others keep their files. Not
  done: the RSS matcher still matches by its own copy of the scene readings
  (a release it matches the old way is refused by the identity check, never
  grabbed wrong); a size check to tell the numberings apart where a name
  carries no episode title; `Episode.status.sceneNumbering`, still written
  by nothing; the library scan, which reads names literally as clustarr
  wrote them; and the records already filed wrong. Tests: the parser's
  table, the decision's readings over TheXEM's rows for the series, the
  search worker's `SceneReadings`, and the import's matcher and narrowing,
  each failing with its rule off; the import's walk itself and a grab on a
  cluster are not exercised.

- **A person's search for an episode whose scene numbering differs from
  TVDB's asks for both (2026-11-26).** The search asked the indexers for
  the scene number alone (`searchNumbering`): Panty & Stocking with
  Garterbelt S02E14 went out as scene S02E07, the broadcast that holds it,
  and nzb-life's "S02E14 Independence Dick", a release of TVDB's episode
  itself, was never returned. The snapshot keeps the episode's own
  numbering beside the scene's (`TargetIDs.TVDBSeason`, `TVDBEpisode`), a
  user-invoked live search sends a second request under it in the same
  slot (`tvdbNumbered`), and the two replies are folded by indexer and GUID
  (`mergeResponses`); the identity check reads each release by its own
  numbering. An automatic search still asks once, for the broadcast: asking
  twice would spend two of an indexer's queries for every such episode. A
  broadcast's release in a search for one of its episodes is right -- scene
  S02E07 holds TVDB's S02E14 and S02E15 (TheXEM, the aired order, and the
  file's 24 minutes against TVDB's 12 and 12) -- and the owner first read
  it as wrong; only a release titled as another TVDB episode is. Not
  tested: the snapshot's setting of the two fields, and a search on a
  cluster.

- **A scan creates an item only when no item carries its provider id,
  whatever the item is named (2026-11-26).** `scanapply.ensureItem` looked
  for the proposal's name alone (`names.Series(title, tvdbID)` and its
  kin), and a name is the title the item had when it was added: Panty &
  Stocking with Garterbelt, added as "New Panty & Stocking with Garterbelt"
  and moved to the anime root, was proposed by a scan of the folder it left
  under today's title, and a second Series was created, classified into the
  first one's folder, searched, and re-downloaded 24 episodes; Plex held two
  shows it could not refresh. Now a proposal whose id another Movie, Series,
  Artist, Author or Audiobook carries (`sameItem`, the manager's cache, once
  per proposed create) creates nothing and lists the file against that
  item: `other_root_folder` when it is stored under another root folder,
  `item_exists` when under the scan's own. The walk's own match
  (`rescan.MatchSeries`) still reads only the scanned root's Series, so the
  folder is proposed and refused at every scan until its files are gone. An
  Album, its Artist's, is not looked for.

- **A whole bucket is read with `KV.Entries` (`records.Reader.All`), never
  `Keys` and a `Get` per key, when anything waits on it (2026-11-26).** The
  downloads stage seeds its unclaimed index from clustarr-transfers once per
  leader, and every item pass waits for that seed (`seedOnce`). It asked for
  the keys and then for each record in turn: 2,866 requests from a manager at
  its 750m CPU limit took 165 s after the 13:36Z start on talos (24-55 s
  before the library store, when the start did less beside it), the loop's
  sixteen workers finished no pass meanwhile, and every candidate parked in
  the inbox lapsed its 60 s budget ("intake: no decision on the candidate
  within the handler budget", 26 times; all were settled once the seed
  ended). Read it off `clustarr_remediation_planner_seconds_sum{planner=
  "item.downloads"}` against the other stages, and
  `workqueue_longest_running_processor_seconds{name="mediafile"}` at a
  start. `Entries` is one watch stopped at JetStream's end-of-initial-values
  marker; the seed now starts with the loop's sources (`seedSource`), before
  a worker runs. The trailers and segments sweepers still read key by key:
  nothing waits on them.

- **A feed item's download is its enclosure of the indexer's own protocol,
  and a usenet release carries nothing of a torrent (2026-11-27, owner:
  "Add an additional usenet indexer for animetosho").** Anime Tosho itself
  is gone: updates stopped 2026-05-09 and feed.animetosho.org stopped
  resolving in October. Its successor at animetosho.net (a third party,
  named on the original's shutdown page; `https://feed.animetosho.net/api`,
  no API key) answers Newznab and Torznab from one endpoint, and each item
  holds both: the release's page as `<link>`, a torrent `<enclosure>`, an
  NZB one where the release was mirrored to Usenet, and the torrent's
  `magneturl`, `infohash` and swarm as attrs. The client read `<link>` as
  the download and one enclosure only as its fallback, and
  `grabsource.ResolveSource` takes a magnet before anything else, so as a
  usenet indexer its releases would have been fetched by their magnets, or
  by the HTML page. Now `torznab.Release.Enclosures` keeps every enclosure
  and `PayloadLink` answers the one of a media type, as Sonarr's
  `GetDownloadUrl` does over `PreferredEnclosureMimeTypes`;
  `rss.ProjectRelease` takes it for the indexer's protocol and, for
  usenet, drops the magnet, info hash and swarm; `rss.NoPayload`, beside
  `BelowMinimumSeeders` in the RSS poll and the search fan-out, drops an
  item whose enclosures name types and none is the protocol's (a torrent
  not mirrored). A feed with one enclosure, or with none that names a
  type, is read as before. Fixture: `test/data/torznab/
  mirror_both_protocols.xml`, two items of the live answer; test:
  `app/indexer/worker/rss/payload_test.go`. The other live replacements
  probed that day: aninzb.moe (keyless, plain NZB links, works unchanged;
  its `tvdbid` search answered nothing for one sample) and amenzb.moe
  (needs an API key, registration closed); AniFix has closed and SceneNZBs
  rebranded.

- **An indexer's title is HTML-unescaped where the feed is parsed
  (`torznab.newRelease`), because some escape it twice (2026-11-26).**
  nzbgeek wrote `New Panty &amp;amp; Stocking with Garterbelt`, the XML
  decoder left the text `&amp;` in the title, and `titleKey` read
  "newpantyampstocking...": not the series' scene name for its second
  season, so `sceneSeason` kept the release in season 1 and four `[DKB] New
  Panty & Stocking with Garterbelt-S01E07/09/12/13` releases were grabbed
  for the 2010 season's S01E14-E26. The import agent reads the file's own
  name, which has a real "&", mapped each to season 2, found that episode's
  file transcoded and held the download ("a transcoded file is final"): the
  owner saw season 2 import and season 1 not. The title stored in
  `status.downloads[].release.title` showed the `&amp;` all along. Cardigann
  results were already unescaped (`pkg/cardigann/filters.go`). The title is
  unescaped until that changes nothing (`unescapeTitle`, at most four
  passes): the same day the release index held nzbgeek's `Abyss S01 E02
  T&amp;amp;M E16 (2019)`, a level deeper, which one pass leaves `T&amp;M`.
  Rows stored before the fix are rewritten when the release is seen again
  and pruned 72 h after their last fetch (108 of 65,682 held an entity).
  That title's "T&M" is the Dutch "t/m" (tot en met, "through"), a range
  E02-E16 that `release.Parse` reads as the whole season in every spelling:
  not built.

- **A scene absolute number is every TVDB episode of its broadcast, and on
  a scene that counts each season from 1 the release's title says which
  season (2026-11-26).** `releaseCoverage` mapped an absolute number only
  when the table had exactly one row for it (Sonarr's "Don't allow multiple
  results without a scene name mapping"). TheXEM gives Panty & Stocking
  with Garterbelt's eighth broadcast of each season the absolute 8, and two
  or three TVDB rows each, so "Panty.And.Stocking.With.Garterbelt.E08.720p.
  BluRay.x264-GOTEi" (and Moozzi2's "-08") was read as TVDB's absolute 8,
  S01E08: `WrongItem` in a search for S01E15, and, in a season search,
  refused against S01E08's file. `sceneIndex.absolute` now maps the rows of
  one scene episode together, and picks the scene season from the title
  (`sceneSeason`: the series' own title is season 1, a later season's scene
  name that season) when several scene episodes share the number; with no
  scene names it still refuses. The same day's `S01E08...BluRay.REMUX`
  releases were read right all along and refused only by the profile
  (`QualityNotWanted: Remux-1080p`). `rssmatcher` keeps its own copy of the
  single-row rule (`match.go`), unchanged.

- **The number after a title that ends in a season marker is that
  season's episode, however the separators are written (2026-11-27).**
  Owner: "Investigate the anime series with missing episodes. Determine why
  monitored seasons/episodes are missing and fix the issue." Fansubs name
  an episode "Show S2 - 05"; a pack's files write it "Seirei.Gensouki.S2.-
  .01" or "_Re-Dive_S2_-_01_V2", and a usenet indexer flattens it to
  "[SubsPlease].Kingdom.S5-06.1080p". Only the spaced, bracketed form was an
  episode, and that one an absolute number whatever its title said. The
  others were season packs: "S5-04" all of season 5, "S5-06" seasons 5 to
  6, "S6-13" seasons 6 to 13 (the standard parser for a name with no group
  in front, `animeSeasonPackRegex`'s range for one with). Of the 8,343
  candidate titles of talos's 71 anime season searches that night, 556 one-
  episode releases were read as packs (380 as a range of seasons) and 74
  of them approved as packs, a 1.6 GB episode grabbed for a whole season;
  at import a pack's "Pop Team Epic S2 - 01" was filed as absolute 1,
  S01E01, and "Kemono Friends S2 - 01" held as "a whole season's, not an
  episode's". Now: `animeTitleSeasonEpisodeRegex` reads every spelling as
  one episode (title "Show S2", `Absolute` [5]), `parseSeries` sends a
  group-less name of that shape to the anime parser, and
  `ParsedRelease.TitleSeason` carries the season the title's ending names
  ("S2", "Season 2", "2nd Season"). `releaseCoverage` (`addTitleSeason`)
  takes the number as that season's episode: through the scene table's row
  for it, the season being the one the series' scene names give the title
  where they know it; as the absolute number when the table has no such
  episode and does hold the number as an absolute (a group counting on
  through the series, "Shingeki no Kyojin S3 - 49"); and literally
  otherwise, never both ways ("Show S2 - 05" read as an absolute too would
  be season 1's fifth episode in a season 1 search). `MatchDownloaded`
  reads a file's name the same way and falls back to the absolute number
  when the series lacks that episode. What stays a range of seasons: a
  zero-padded season, a bare dash and no E ("Tsugumomo S01-02 + OVA"); "S05-
  E01" is a season and episode outright. Not read: an unpadded season
  before a two-digit count of seasons ("S1-10" is now season 1's episode
  10), and a group counting through the series on a series with no scene
  table (refused, where it was matched by absolute number).

- **One separator may stand between the season and an episode token: "S01
  E01", "S01.E01" (2026-11-27, owner: "Build the smaller fixes").** Until
  then the name read as a season pack, so each file of Sakamoto Days'
  RUBaDUB pack ("... - S01 E01 [BD x265 ...]") was "a whole season's name,
  not an episode's" and the download was held for a person. Both
  `multiEpisodeRegex` and `dashRangeEpisodeRegex` take `[.\s_]?` before
  the E; an E with no digits after it ("S01 Extras", "S01.EAC3") is still a
  pack (`TestASeparatorBeforeTheEpisodeTokenIsStillAnEpisode`).

- **A held import that nobody acts on is deleted after 24 hours and its
  release grabbed again (found 2026-11-27; the unattributable case fixed
  the same day, the owner's "Break the loop").** The fix: an automatic
  grab's hold for files whose names do not say which item they are (the
  import agent's `InspectKindUnattributed`: a name that does not parse, or
  names an episode the series lacks) expires as
  `lifecycle.ImportExpiredBlocked`: the entry is Blocklisted under
  `importExpired`, for the item alone, and R25 searches the item again, so
  the planner takes the next release. Every other hold expires as before,
  blocking nothing: a suspected sample, the item's own state, a local
  fault (blocking those would have the search take every release of the
  item in turn, one a day), a person's own import, and a hold whose
  inspection record is gone. The same verdict ends the hold of a release
  that brought nothing it was grabbed for: its files were attributed, none
  to an item of the grab (Heaven's Design Team's "S01" held ten episodes,
  was grabbed for the two still missing and held because the ten have
  transcoded files). And an anime's absolute number is read at import as
  the grab read it when the series' scene seasons have names
  (`MatchDownloaded`, `sceneIndex.absolute`): "Shingeki no Bahamut -
  Virgin Soul - 01" was filed as season 1's first episode, its release
  grabbed for season 2's. A third loop, found in the transfer records the
  same night: a Failed entry of a local fault (`writeError`, `diskFull`)
  was dropped at once, and the search answer it came from handed the
  planner the same release again every pass for its whole candidate
  window. One usenet release whose finished files `verifyContent` could
  not find under the NZB's name ("[Cleo]Shuumatsu.Nani.Shitemasu.ka-03.
  Dual.Audio.10bit.720p.x265", `ErrScratchLost`) was grabbed 65 times in
  30 minutes, 23 releases 262 times in a week. Such an entry now stands
  `lifecycle.LocalFaultHold` (2 h) as a tombstone the planner refuses the
  release for (`tombstoned`), blocking and searching nothing. Not fixed:
  why those releases fail `verifyContent` every time (a content file
  missing under the NZB's name, or a size other than the yEnc header's),
  which is a fact about the release classed as a local fault. With it: a pack's file named for its episode
  alone is read (`bareEpisode`: "S02EP01.mkv" by the season it writes,
  "[Prof] Episode 01 - The Wind Blows.mkv" by the one season its release
  is a pack of), and a name that is nothing but hexadecimal digits is no
  name (`hashedNameRegex`): the release parser read "5e21e3e9...mkv" as
  S05E21 and "00e74c75...mkv" as S00E74, and a hash spelling an episode
  the series has would have been imported as it. Two more from the same
  list, built the same day: a season the scene splits is searched under
  every scene season that still holds a wanted episode
  (`search.SceneSeasons`, `otherSeasons`: further requests of the one
  search, merged as `tvdbNumbered`'s is, at most `MaxOtherSeasons`; the
  first is the scene season with the most wanted episodes, where it was the
  one with the most episodes and the rest were "left to the sweep"); and
  an anime the provider numbers on from an earlier series counts from 1
  (`series.countFromOne`: every regular episode's absolute number is its
  place plus one same offset, the smallest above the number of regular
  episodes, the series' own numbers starting at 1; D.Gray-man Hallow's 104
  to 116 become 1 to 13 at the Series' next reconcile). A series offset by
  less (Steins;Gate 0, Nanbaka: TVDB counts a special as 1) is not
  renumbered: a release's 05 could be either episode. What was found: 56 anime series had 938 monitored episodes missing. Re-running
  their 74 season searches grabbed for 44 of them, and 49 of the 105 grabs
  were releases already downloaded whole once, held at import as
  `needsPerson`, removed when the hold lapsed (`lifecycle.
  ImportHoldRetention`) and never blocklisted, so every search takes the
  same best-ranked release again; BacklogSearch's cooldown makes that once
  in 7 days. What held them, by file name: hashed names with hashed par2
  and no container title (Kitsune's and others' Crunchyroll packs on
  usenet: nothing to read, see the 2026-11-25 Higurashi record); the
  season-marker names above; names with no title ("S02EP01.mkv", "[Prof]
  Episode 01 - The Wind Blows.mkv"); scene-numbered
  names on a build before `MatchDownloaded`. No Episode in the library
  store carries `status.sceneNumbering` (0 of 27,707), so `MatchEpisodes`'
  scene fallback never matches: `decision.CoverageFor` is the only scene
  reading at import. Also seen in the same sample: three torrents removed
  on `input/output error` writing under `/data/torrents/incomplete`, three
  usenet transfers on "scratch files are gone or altered"; D.Gray-man
  Hallow refused whole because TVDB numbers its 13 episodes absolute 104
  to 116 and every release 01 to 13; a TVDB season the scene splits in four
  (Ascendance of a Bookworm) searched as one query, its 200 kept results
  all scene season 4; and seasons whose only releases the profile refuses
  (raws, `anime-lq-groups`, dubs alone). How it was read: each season
  search's record from `clustarr-searches` inside its 30 minutes
  (`nats kv get clustarr-searches '<uid with - doubled>.season--<n>'
  --raw`), and the transfer and import records by `nats sub --stream
  KV_clustarr-transfers --last-per-subject --raw '$KV.clustarr-
  transfers.>'`.

- **A person's pick is imported at the quality it was picked at
  (2026-11-26).** A pick (`Search.spec.grab`, the entry's `manual`) of a
  release whose quality the profile does not allow needs `spec.override` to
  be grabbed at all, and the import then refused the same file a second
  time ("quality Remux-1080p is not allowed by the quality profile", held
  as `needsPerson`) until the person sent `download.clustarr.io/import ...
  override` as well: six Blu-ray remuxes for Panty & Stocking with
  Garterbelt's first season each needed both. `importplan`'s planner now
  takes a file whose quality is the one the picked release advertised
  (`pickedAt`: the entry is manual and `release.quality.name` is the
  file's). A pick whose file is another quality than its release's, and
  any automatic grab, are refused as before.

- **A kind's own pass starts from the object its stages planned on: one
  read, never two (2026-11-27, owner: "Fix this", of imports that ran
  twice).** A staged item pass read its item twice: `ItemReconciler.staged`
  for the stages, then the kind's `ReconcileItem` again from the manager's
  cache, and the catalog apply was preconditioned on the second. The cache
  can catch up with the pass before's apply between the two (the stages
  read records and lists for every entry: seconds, on a Series with 23), so
  the stages planned on status from before that apply and the plan landed
  at the resourceVersion after it. On talos five imports ran twice in a
  night (Ascendance of a Bookworm 1x05, 1x26 and 1x27 in one pass, How a
  Realist Hero 1x25, Yuuki Yuuna 3x01): the pass before had sent an execute
  and recorded its seq; this one saw the answered inspection alone, sent a
  second execute at a new seq, and its status replaced the first's. The
  first placed the file; the second found the source gone, and since
  `executeAnswer` keeps only an answer at the same seq, its "no such file"
  replaced the placed answer. No MediaFile was made, the entry was held
  "the download holds no file, and an import of it was already carried
  out", and the episode read Wanted with its file in the library. Nothing
  was logged: no apply failed. Now `itempass.Read` gives every kind's
  `ReconcileItem` a copy of the pass's view (`Pass.View`), so a pass on a
  stale object is a Conflict at the apiserver and sends nothing
  (`TestAPassAppliesAtTheResourceVersionItsStagesPlannedOn`, and
  `TestEveryItemKindStartsFromThePassesView` holds each kind's source to
  it, since no staged-pass test runs a real kind's reconciler). The agent
  is held to the same end from its side: a second execute of a plan an
  earlier one placed, every file still at its destination with the size
  and modification time recorded, answers what the first placed
  (`fileimport.placedBefore`), whatever sent it twice. The 2026-10-21 case
  (a status write lost to an etcd timeout) ends the same way now.
  **Finding one:** `status.downloads[].import.message` matching "holds no
  file, and an import"; the agent's "fileimport: executed an import ...
  placed 1" for the entry is in VictoriaLogs (field `entry`). **Repairing
  one:** a `LibraryScan` of the item's folder (`rootFolderRef`, `subpath`),
  first with `dryRun: true`, whose `itemsUpdated` counts the unrecorded
  files.

- **A refused pick says why, and over which grab (2026-11-27, owner: "The
  refusal reason should reach the UI").** The grab planner's reason for
  refusing a candidate was one Debug line, and the Search reported "the
  item's owner did not take the release (see its Events)" over an owner
  with no such Event. The reason now goes back with the settlement
  (`grabplan.Plan.Refusals`, `remediation.SettleCandidate.Reason`,
  `intake.Inbox.SettleBecause` and `Refusal`), and `status.grabbed[].error`
  reads "the item's owner did not take the release: <reason>", which the
  interactive search's download button shows. A release refused over a
  live grab names it and where it stands: `"<release>" (<entry id>),
  downloading`, or `downloaded, and its import is held for a person`. That
  last one is the case that raised it: **a Completed entry whose import is
  held is still live in the planner's queue**, so a person's pick of
  another release for the same episode is refused (`overlapsLive`,
  QueueHigherPreference) unless `spec.override`, which the UI sends only
  for a release the search itself rejected. The inbox is leader-local: a
  refusal settled before a leader change reads without its reason.
  **Changed the same day, below: a held import is no longer live, and a
  person's pick is never refused over one.**

- **A download held at import holds nothing against another release
  (2026-11-27, owner: "stop a download held at import from blocking other
  releases").** "A grab still in flight" is written three times
  (`rollup.EntryNonTerminal`, lifecycle's `nonTerminal`, grabplan's `live`),
  and the planner's copy was the one that left the import out: a held,
  expired or finished import was live there, so a download held at import
  went on holding its episodes for its 24 hours, a held *pack* against any
  release at all (`PackDownloading`, "judged again once the pack has
  imported or failed": it never would), and a torrent seeding after its
  import against every release of its episodes. The search agent and the
  Search controller read the queue through rollup's copy
  (`itemread.Queue`), so they approved what the planner then refused. On
  talos three packs of How a Realist Hero Rebuilt the Kingdom, held in
  turn, kept EMBER's whole season from being taken; it was grabbed five
  seconds after the last was removed by hand. `live` now says what the
  other two say. One thing a hold still decides, the planner's alone
  (`refusedByItem`): a download whose import is held with class
  `itemState` (the item's file is final, or no worse) is the release an
  automatic one must beat for an episode it would otherwise be taken for,
  as a release and never as a pack. Import would refuse one no better for
  the same reason, and without it the planner takes the answer's next
  release at every pass, a whole download each (one cause of the planner approving a
  release over a final file was found the same day, the next bullet; the
  other, `Covered.Current` nil at grab time, was not). It holds nothing for an episode the release is
  not taken for, nothing against a person's pick, and a hold of any other
  class (`needsPerson`, an import that could not be carried out) holds
  nothing at all. Tests: grabplan's `heldimport_test.go`.
  **A finished import is the one that is not simply "not live"
  (`landing`, the same day).** The first roll of this made it so, and within
  minutes a twelve-episode batch of Rozen Maiden: Zurückspulen imported at
  11:38:12Z and the rank-2 batch was grabbed at 11:38:13Z for the two
  episodes whose files had landed last: an item's current file is read from
  its own status (`itemread.Current`: `hasFile`, `fileRef`), which its
  reconciler writes a pass after the MediaFile exists, so between the
  import and that pass the episode had neither a live grab nor a file.
  (Before, the entry stayed live until `filesLanded`, a narrower window of
  the same kind.) An entry whose import is finished now holds an item that
  shows no file, as a grab in flight does, for `grabplan.LandingGrace`
  (10 minutes) from `import.importedAt`; an item that shows its file is
  judged by that file, so a torrent seeding after its import still holds
  nothing. Any rule that reads "no current file" as "has no file" has this
  window; the planner is the one that acts on it at once.

- **A release the scene table places at an episode is that episode, by its
  season and number alone (2026-11-27, owner: "Fix in order", of "Planner
  approving releases over final files").** A coverage's absolute numbers
  are compared with the series' stored ones (`Episode.status.absoluteNumber`,
  from the metadata provider), and `addMapped` also put each scene row's
  TVDB absolute number into the coverage, which is TheXEM's count. The two
  disagree on some series, and a provider's own count can repeat a number
  across seasons. Link Click on talos: TheXEM's row for the scene's season
  3 episode 5 is S04E05 with absolute 34; the series stores S04E05 as 29,
  and both S03E10 and S04E10 as 34. A search of season 3 took
  "[Erai-raws] Shiguang Dailiren Season 3 - 05" as one of its episodes, a
  search for S03E10 took it as that episode, and the planner
  (`coveredBy`: place *or* absolute) grabbed it for S03E10, which was
  missing. The importer reads places only (`fileimport.MatchDownloaded`:
  `CoverageFor(...).Episodes`), filed it as S04E05 and held it against the
  transcoded file there; the entry then named S04E05 (importplan's
  `provide`), S03E10 was free and searched again at once, and the next
  release of the same episode was taken: four in four minutes, the only
  grabs over a final file after that day's 12:02Z roll. A mapped row now
  adds its place and nothing else, so the search, the planner and the
  importer read one release as one episode. What it gives up: a row whose
  TVDB place the series does not have no longer reaches an episode through
  its absolute number; such a release covers nothing and is refused, where
  before it was taken for whatever shared the number. A number the table
  does not place is still an absolute number, compared as before. Tests:
  `pkg/decision`'s `TestASceneRowNamesItsEpisodeByPlaceAlone`, grabplan's
  `mappedabsolute_test.go`.
  **The other form of "a grab over a final file" is not explained, and it
  is not "the planner read no file".** On 2026-11-26 and 27 single-episode
  releases were taken for episodes holding transcodes, the release's
  coverage being the episode it was then filed as (Monogatari Series: Off
  & Monster Season, 720p over 1080p; Bahamut; Realist Hero), and the guess
  was `Covered.Current` nil. The first build that logged each grab
  (sha-6f8a8fb, sha-d04cc65) settled that: at 14:22-14:35Z four URANiME
  singles of Cardcaptor Sakura season 3 were taken with
  `over: [...-s03e19=Bluray-720p/transcoded]` and the like, each the very
  release its episode's file had been imported from twelve hours before,
  each then held "transcoded file is final". Replayed offline through
  `grabplan.Decide` with the live season record, the live profile and the
  episodes and MediaFiles from SQL, every one is refused
  (`AlreadyImportedSameName`), on the first pass and on a second with the
  first pass's seventeen grabs queued; a grab held for the indexer's limit
  is re-checked when it comes due and refused too; the titles parse; no
  removal intent ran on the series; the importer's and the planner's
  "final" are one predicate (`MediaFile.Final`). So an input the replay
  does not have differs: where the candidate came from, or a flag that
  exempts it. The line now says so (`logGrab`, grabplan, since the commit
  after 2d8bae27): `grab: taking a release` with `release`, `source`
  (`answer`, `parked`, `pending`), `by`, `manual`, `override`, `complete`,
  `covered` and `over`, each item as `<name>=<quality>[/transcoded]
  [/same-release]` or `=none`. Read that line for the next hold before
  theorising, and replay the record as above (save it from
  `clustarr-searches` inside its 30 minutes).

- **A pack's file named "<the series' title> <number>" is that episode
  (2026-11-27, owner: "Ensure no remaining series has any missing monitored
  episodes").** No release pattern reads a number that only follows a
  title, since a title may end in one; at import the title is known, so
  `fileimport.titledEpisode` reads the name when what stands before its
  last number is one of the series' titles (its own and its alternates,
  compared as `release.CleanTitle`) and the download is one season's
  (`downloadSeason`, as for `bareEpisode`). On talos "[anime4life.] Wandering
  Witch The Journey of Elaina Season 1 BD 1080p ..." held twelve files
  named "Wandering Witch The Journey of Elaina 01.mkv" to 12 for a person,
  eleven episodes missing meanwhile. The name may go on with the episode's
  own title ("Gosick 01 The Dark Reaper Finds the Golden Fairy.mkv", a
  usenet pack of 24 held the same day): the number is the first one a title
  ends at, whole, so "Mob Psycho 100 05 ..." is episode 5, and a name that
  goes on with another number ("Gosick 2 01") is a sequel's and is not
  read. Still held, and by rule: a pack named
  in scene seasons the series does not have and TheXEM does not map (How a
  Realist Hero Rebuilt the Kingdom "S01-S02", TVDB one season of 26: "the
  series has no S02E01"), and files with no number at all.

- **A season pack's files no pattern reads are read from what is known of
  the download (owner, 2026-11-27: "Since we have the episode names and
  numbers, we should be able to build each filename ... We know the season
  when downloading a season pack. The import/rename should build the target
  name intelligently and not just verify that it can be perfectly parsed as
  is").** `fileimport.packEpisodes` runs once over the media files that
  neither a release pattern nor `bareEpisode` nor `titledEpisode` read, when
  the download is of one season (`plan.season`) and holds more than one
  video. Two readings: the number that follows the names' common prefix
  (after any leading group), taken only when every file has one, no two
  share one and each is an episode of the season by its number in it, or
  each by its absolute number; and an episode's own title found in the name
  (cleaned, six characters or more, the longest of those found). Where both
  read a file they must agree and no two files may be one episode; on
  either contradiction nothing is read for any file and the pack stays a
  person's. So "Gsk - 01 [BD]" needs no title, a number inside an episode's
  title is never the one read, and a hashed name is never read. The quality
  and the group are still the release title's (`fillFromRelease`,
  `release.TitleTags`), and the file is renamed by the naming format as any
  import is. Gosick's metadata titles are another translation's than its
  pack's (one of twenty-four agrees), so the number is what carries a pack
  and the title what names a file with no number. Not done: the title does
  not yet overrule or confirm what `bareEpisode` or `titledEpisode` read.
- **A file named by a number and no season is an episode of the season its
  download is of (2026-11-27, owner: "we provide season, release, etc
  metadata so that it can *just* infer the episode number this way").** A
  name's bare number ("[GRP] Show - 07") was counted through the series
  whatever the download was, so a season 2 pack whose files restart at 01
  was filed as season 1. Found among the failed transcodes: on talos all
  twelve files of Ranma ½ (2024)'s season 1 are its season 2 pack's
  (imported 2026-10-28; the same running times to the millisecond as the
  season 2 files imported from that pack on 11-27), Date A Live's season 1
  is twelve files whose releases were titled "Date A Live II" and "IV"
  (11-05 and 11-09), and New Game!'s S01E01 and S01E05 came from "New
  Game!!" releases (11-03); those two by their release titles, not by a
  check of the content. All three predate
  the season-marker and scene-name readings above, which now read those
  titles when TheXEM answers; nothing read the download.
  `fileimport.downloadSeason` is the one season a download is of: the
  season its release title names, read as the grab read it
  (`decision.CoverageFor` for a pack or an SxxEyy, `decision.NamedSeason`
  for a marker or a later season's scene name), and under a title that
  names none the season of the episodes it was grabbed for (the task's
  `targets`, never a person's own target). `inDownloadSeason` then reads a
  bare number in that season when the count through the series did not
  land there and the season has the number. Three things stand as they
  were, each with a test: a count that lands in the season ("Show - 14" in
  a season 2 pack), a count the season has no episode for (a pack titled
  for one season holding the cour TVDB lists as the next is filed by its
  count), and a name that says its season ("S01E05", "Show S2 - 04"),
  since the name, not the download, says what such a file is. A download
  of several seasons says none, and the episode a person names is that
  person's. `bareEpisode` and `titledEpisode` take the same season, so a
  lone "Episode 01.mkv" is read too. **Two seasons' scene names that
  differ in punctuation alone were one name**: `sceneIndex` keyed names by
  `titleKey`, which drops punctuation, so TheXEM's "New Game!" (season 1)
  and "New Game!!" (season 2) shared a key, the later won, and every
  season 1 release of that series covered season 2 ("[DB]New Game! - 09"
  as S02E09, at the grab and at import, live from the scene-name reading
  until this). Names two seasons share are now kept under their spelling
  (`addSeasonNames`, `spelledKey`), and a title that is neither spelling
  names no season. Not done: the twenty-six misfiled files above are still
  in the library under the wrong episodes; and with TheXEM unreachable at
  the grab a title it alone knows as a later season ("Date A Live II",
  which is also one of the series' own alternate titles) is still counted
  through the series there.

- **A file takes the quality its download's release title names even when
  no release pattern reads that title (2026-11-27, owner: "We know the
  quality from the release title").** `fileimport.fillFromRelease` gave a
  file that names no source the release's quality, revision and group by
  parsing the title as a release, and a batch named for its series alone
  ("[GERMini] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual
  Audio]": no episode for a pattern to hold on to) parses as none, so its
  thirteen files ("Rozen Maiden - Zurückspulen - 01.mkv") read Unknown and
  a person's pick was held "quality Unknown is not allowed by the quality
  profile". Now a title that does not parse has its tags read on their own
  (`release.TitleTags`: quality, revision, group). Such a release is still
  refused at search as UnableToParse; a person takes it by an **episode**
  Search's pick with `spec.override` (a season Search's pick of an unparsed
  release covers nothing and is refused).

- **A release that names a one-season series and nothing else is that
  season's pack (2026-11-27, owner: "Yes - do it").** A fansub batch is
  often named for its series alone, "[GERMini] Rozen Maiden 2013
  Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]": no season, no episode,
  so no release pattern takes it and every search refused it as
  UnableToParse while thirteen episodes stayed missing. A season search
  now reads such a title when the season is the series' only one
  (`decision.Identity.OnlySeason`, from `status.seasons`, specials aside)
  and what stands between the title's leading groups and its trailing
  tags, less a year, is exactly one of the series' titles
  (`release.SeriesBatchTitle`: `CleanTitle`, and once more with the letters
  outside ASCII dropped, as a usenet post writes "Zurckspulen"). It is
  parsed as "<groups> <title> S01 <tags>", so its quality, group and
  languages are read as any pack's, and the Decision keeps the indexer's
  title. A title with anything else in that place -- "Movie", "OVA", a
  quality word outside brackets -- and a series of more seasons than one
  are refused as before: the name cannot say which season it holds.
  **The grab planner reads a title again without the search's identity**
  (`decision.ParseForItemState`), so a release that does not parse there
  but was read as a season pack is judged by what its ReleaseInfo carries;
  returning no parse, as that function did for every unparsed title,
  skipped the queue and current-file rules altogether. A torrent grabbed
  for some of a pack's episodes downloads only their files, so a pick of
  such a batch from one episode's Search brings that one episode.
  "Yozakura Quartet - Hana no Uta" (no tags at all) is read too and then
  refused for its quality, Unknown.
