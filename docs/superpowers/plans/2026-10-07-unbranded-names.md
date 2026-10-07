# Unbranded names (Wave U): the *arr brands leave code and wire, migrated in release N -- Implementation Plan, U1-U7 and the amendments

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or
> superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. **Execution mode: implement
> only**, as for every wave on this branch: no task writes a test; each lists the tests it needs
> under **Tests (deferred to the batch)** by Go test name with a one-line assertion. A task never
> weakens or deletes a test or guard; a test file a rename touches is renamed mechanically with the
> rest (that is not writing a test), and one whose expectations change is listed "rewrite".

**Goal:** No tracked file outside `pkg/legacynames` and the historical records names `catalogarr`,
`importarr`, `indexarr`, `grabarr`, `squasharr`, `captionarr`, `segmentarr` or the derived `squash`;
every stored and wire name takes the domain name (`catalog`, `import`, `index`, `grab`,
`transcode`, `caption`, `markers`), and release N carries the live cluster across: the Migrator
renames `managedFields` entries in place, the topology displaces the six work streams and hands the
two event-stream durables over at their ack floor, and the rollback gains `restore-names`.

**Architecture:** one data-only package, `pkg/legacynames`, spells every old name; U1 builds it and
re-points every retired or legacy-read use at it, so U2-U6's mechanical passes cannot damage a name
that must keep its spelling. U2 renames directories and identifiers (no behaviour change), U3 and U4
switch the stored Kubernetes and NATS values and add the mechanics that carry them through release
N, U5-U6 rename the remaining text, U7 commits the gate. The Migrator's rename part lands later,
inside F8.3-F8.5 and A8, as amendments.

**Spec:** `docs/superpowers/specs/2026-10-07-unbranded-names-design.md` ("spec", cited `§n`):
read §1-§4 before any task; §6 is the mapping table every later wave reads. **Also binding:** the
split plan's Global Constraints, `docs/superpowers/plans/2026-10-07-agents-report-over-nats.md`'s
Global Constraints, the loop spec §7 (the Migrator, the runbook, the rollback),
`.superpowers/unify/rulings.md` (the owner's 2026-10-07 entry supersedes split R7 for the brand),
and CLAUDE.md.

**Basis.** Written at `cae73ac6`-`9f788545` (A3.5-A3.7 committed, A3 in flight). Wave U starts after
A3.10's gate; U1 step 1 re-inventories, because A3.6-A3.10 add names (the engine durables, the
engine recorder, the DownloadClient controller's).

---

## Global Constraints

**Inherited, verbatim in force:** branch hygiene (R14); path-scoped commits (`git add` every new
file first, `git rm`/`git mv` for deletions and moves, never `git add -A`, never `git stash`, never
`git reset --hard`, never `git push`); never `go get` or `go mod tidy` (no task here adds a module:
`sigs.k8s.io/structured-merge-diff/v6` is already required at v6.4.2); GPL header on every new `.go`
file; `$(go env GOPATH)/bin/gofumpt -w` and `$(go env GOPATH)/bin/goimports -local
github.com/mediactl/clustarr -w` on every touched `.go` file; RBAC markers move with their code and
`make generate manifests` follows any marker or API change; build, don't deploy.

**Specific to Wave U:**

- **Alone.** No other wave runs while U1-U7 run: a rename touches nearly every package, and a
  pathspec scopes files, not authors. Before each commit, `git status --short` must show only this
  task's files.
- **Gates per task:** `go build ./...`, `make build` (five binaries), `make generate manifests`
  leaving `git status --porcelain -- api config charts` empty after the commit, and from U5 on
  `make templ` leaving `ui/` clean. `go vet` runs in the test batch.
- **Never rename a legacy value.** After U1, a string listed in `pkg/legacynames` is spelled only
  there; every mechanical command below excludes `pkg/legacynames` explicitly.
- **The legacy installer set is W6's** (spec §5): `charts/clustarr/`, `config/{manager,rbac,prometheus,postgres,e2e}/`,
  `config/README.md`, the `Makefile`'s RBAC identities, `hack/e2e.sh`. Wave U edits them only where
  a code rename forces it (the `Makefile`'s `./app/squash/...` path, U2). The gate's temporary
  allow-list covers them until W6.17.
- **Prose rule** (spec R-U5): a sentence the map makes read badly is reworded, never left branded.

### The token map (copy; spec §1)

```bash
# Re-runnable. Apply to the files named by each task, never to pkg/legacynames or docs/{superpowers,research,adr/[0-9]*}.
perl -pi -e '
  s/squasharr-transcode-/transcode-pool-/g;
  s/Catalogarr/Catalog/g;   s/catalogarr/catalog/g;   s/CATALOGARR/CATALOG/g;
  s/Importarr/Import/g;     s/importarr/import/g;     s/IMPORTARR/IMPORT/g;
  s/Indexarr/Index/g;       s/indexarr/index/g;       s/INDEXARR/INDEX/g;
  s/Grabarr/Grab/g;         s/grabarr/grab/g;         s/GRABARR/GRAB/g;
  s/Squasharr/Transcode/g;  s/squasharr/transcode/g;  s/SQUASHARR/TRANSCODE/g;
  s/Captionarr/Caption/g;   s/captionarr/caption/g;   s/CAPTIONARR/CAPTION/g;
  s/Segmentarr/Markers/g;   s/segmentarr/markers/g;   s/SEGMENTARR/MARKERS/g;
  s{app/squash\b}{app/transcode}g;
' "$@"
```

`squash` outside `app/squash` (identifiers, `$squashEnv`, prose) is renamed by hand (U2, U5),
because a blind `squash` → `transcode` would also hit words the gate would then show anyway.

---

## Order and dependencies

**Overall order:** F3 → F4 → A1 → A2 → A3 → **U1 → U2 → U3 → U4 → U5 → U6 → U7** → A4 / A5 / A6 →
F5 → F6 → F7 → F8 with A8 (with the F8.3-F8.5 and A8 amendments below) → A7 → W6 → W6b → W9 → W10 →
F9 with A9. Waves 0-5, 4a-4f, 7 and 8 are built; Wave N (ADR-0020, ADR-0021) follows the
implementation waves.

| Task | Depends on | Parallel with |
| --- | --- | --- |
| U1 legacy table | A3.10 green | nothing |
| U2 identifiers, directories | U1 | nothing |
| U3 Kubernetes names | U2 | nothing (shares `app/` with U4) |
| U4 NATS names | U3 | nothing |
| U5 Go text, generated | U4 | nothing |
| U6 non-Go files, live docs | U5 | nothing |
| U7 gate | U6 | nothing |

**Between U2 and U7 the tree builds but names are mixed** (identifiers new, some values old); nothing
deploys before F8.12, so nothing observes it.

---

## Rulings

Each names its cost if wrong. Spec rulings R-U1 to R-U7 (§1) are in force.

- **U-R1. `pkg/legacynames` is built from `main`, not the branch.** The names it must hold are the
  deployed ones: `git grep` on `main` for every value in spec §2.5-§2.10. A name only the branch
  ever had is not legacy (R-U7). -- Cost: none; a name missed fails `TestLegacyNamesAreWhatMainDeploys`.
- **U-R2. Identifiers first, values second.** U2 renames identifiers keeping values, U3/U4 change
  values; the reviewer of U3/U4 sees only stored-name changes. -- Cost: two passes over
  `pkg/k8s` and `pkg/events`.
- **U-R3. Dual-use strings are split by use in U1** (spec §9 risk 3): `catalogarr-markers` (retired
  manager / renamed durable), `catalogarr-grab` (renamed manager / retired durable),
  `grabarr-engine` (retired manager / renamed SA, recorder, label value, RBAC identity, durable
  prefix). Every retired use becomes a `legacynames` constant before any mechanical pass. -- Cost:
  a wrong classification is a wrong name in N, caught by the U5 review grep or the Migrator's
  envtests.
- **U-R4. Displacement lives in the topology, the census in the Migrator.** U4 builds
  `Topology.Displaced` and the leader-only retire in `busconn.KeepTopology` with a report hook; F8.3
  wires the hook to `clustarr_legacy_fold_preparations_total{action="retire_stream"}`. -- Cost:
  between U4 and F8.3 the census is a log line only (nothing deploys).
- **U-R5. The facade copy is A6.4's.** A6.4 moves `EnsureFacadeKey` into the manager; the copy from
  the legacy Secret is written there (amendment), not in U3's agent code that A6.4 replaces. --
  Cost: none.
- **U-R6. `k8s.LegacyLeaseIDs` stays as an alias of `legacynames.LeaseIDs`** until OD6 deletes the
  gate. -- Cost: none.

---

## Wave U: unbranded names (U1-U7)

### Task U1: `pkg/legacynames`, the one place an old name is spelled

**Spec:** §1 (R-U3), §3, §9 risk 3.

**Files:**

- Create: `pkg/legacynames/legacynames.go`
- Modify: `pkg/k8s/fieldmanager.go` (`ManagerCatalogarrMarkers`, `ManagerGrabarrEngine` take their
  values from `legacynames`; `RetiredFieldManagers()` unchanged in content), `pkg/k8s/leaselock.go`
  (`LegacyLeaseIDs = legacynames.LeaseIDs`), `pkg/events/subjects.go` (`ConsumerCatalogGrab`,
  `ConsumerSquasharrResults`, `ConsumerCatalogSegmentsResult` values), `pkg/events/topology.go`
  (`ConsumerCatalogRedownload` value; the `Retired` entry's purge filter),
  `app/squash/controller/transcodejob/withdraw.go` (`FinalizerTaskWithdrawal` value), and every Go
  file (test files included) whose literal spells a retired use of a dual-use name (step 3)

**Interfaces:**

- Produces package `legacynames` (standard library only), exactly spec §3's surface:
  `Token{Old, New string}`, `Tokens []Token`; `FieldManagers map[string]string` (17 entries);
  `MarkersStatusManager`, `EngineTelemetryManager`; `Streams map[string]string` (6);
  `SubjectTokens map[string]string` (5); `Durables map[string]string` (14);
  `Handover map[string]string` (`catalogarr-history`, `catalogarr-rss-matcher`); `GrabDurable`,
  `RedownloadDurable`, `TranscodeResultsDurable`, `SegmentsResultDurable`, `PoolDurablePrefix`;
  `Schemas map[string]string` (the `importarr.*` values `main` publishes); `LeaseIDs []string` (6);
  `TaskWithdrawalFinalizer`, `PoolManagedBy`, `GraftLabel`, `GraftComponent`, `EngineComponent`,
  `EngineServiceAccount`, `FacadeSecret`, `FacadeSecretSuffix`; `SearchOutcomeNames
  map[string]string`, `SearchOutcomePrefix`; `ValuesKeys []string` (10);
  `func Subject(s string) string`, `func Manager(old string) (string, bool)`,
  `func LegacyManager(new string) (string, bool)`.
- Consumed by U3, U4, F8.3-F8.5, A4.4, A6.4, A9.2, W6.13.

- [ ] **Step 1: Inventory.** At the current tip, run spec §2's `git grep` (and the same on `main`).
  Record in the ledger the per-area counts and every value of spec §2.5-§2.10 that A3.6-A3.10 added
  or changed. Run `git grep -n -h -o -E '"[a-z.]*arr[a-z.-]*@"|"importarr\.[A-Za-z]+\.v[0-9]+"' main -- '*.go' | sort -u`
  for the Source prefixes and schema values `main` publishes.
- [ ] **Step 2: Write `pkg/legacynames/legacynames.go`.** Package doc: "Package legacynames is the
  table of names chart 0.4.x (the per-service release) used and release N replaces; the only Go
  code allowed to spell one (docs/superpowers/specs/2026-10-07-unbranded-names-design.md §3, §7)."
  Values exactly as spec §2.5 (17 renamed managers), §2.6 (streams, subject tokens, the 14 renamed
  durables, the 2 handovers, the kept durables and the pool prefix, the schema values from step 1),
  §2.7, §2.9, §2.10, §2.11. `Tokens` in the order of the token map above (`squasharr-transcode-`
  handled by `Durables`/`PoolDurablePrefix`, not `Tokens`). `Subject` maps the third dot-token of a
  subject starting `clustarr.work.`, `clustarr.dlq.` or `clustarr.rpc.` through `SubjectTokens`
  (plus `catalogarr` → `catalog` for the DLQ's durable-derived service token) and returns any other
  subject unchanged. No init, no package-level mutation helpers.
- [ ] **Step 3: Split the dual-use strings (U-R3).** List every Go occurrence:

  ```bash
  git grep -n -E '"(catalogarr-markers|catalogarr-grab|grabarr-engine|catalogarr-redownload|squasharr-transcode-results|catalogarr-segments-result|squasharr\.clustarr\.io/task-withdrawal|(catalogarr|importarr|indexarr|grabarr|squasharr|captionarr)\.clustarr\.io)"' -- '*.go' ':(exclude)pkg/legacynames'
  ```

  For each, decide by use: a field-manager use of `catalogarr-markers` or `grabarr-engine`, a
  durable use of `catalogarr-grab`, `catalogarr-redownload`, `squasharr-transcode-results` or
  `catalogarr-segments-result`, the finalizer, a lease id → replace the literal with the
  `legacynames` constant (or the `k8s`/`events` constant that now reads it). Every other use is left
  for U3/U4. Record the per-file decisions in the commit message body.
- [ ] **Step 4: Re-point the constants** listed under Files.
- [ ] **Step 5: Gates.** `go build ./...`; `make build`;
  `go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./pkg/legacynames` prints only
  `github.com/mediactl/clustarr/pkg/legacynames` (standard library only).
- [ ] **Step 6: Commit.**

  ```bash
  git add pkg/legacynames/legacynames.go
  git commit -m 'feat(legacynames): the one table of chart 0.4.x names -- managers, streams, durables, schemas, leases, labels, values keys; retired and dual-use names point at it (Wave U U1)' -- pkg/legacynames pkg/k8s pkg/events app/squash <every file step 3 touched>
  ```

**Tests (deferred to the batch):** `TestLegacyNamesImportsOnlyTheStandardLibrary` (go list -deps);
`TestLegacyNamesAreWhatMainDeploys` (a golden list of spec §2's old values equals the package's);
`TestEveryLegacyFieldManagerMapsToALiveOne` (each `FieldManagers` value is in
`k8s.FieldManagers()`, each retired name in `k8s.RetiredFieldManagers()`, after U3);
`TestSubjectMapsOnlyTheThirdToken` (work, dlq, rpc; `clustarr.evt.…` and `$JS…` unchanged; an
already-new subject unchanged); `TestLegacyAndNewNamesNeverCollide` (no new value equals any old
value of another class).

---

### Task U2: Directories, files and Go identifiers (`app/squash` → `app/transcode`)

**Spec:** §1 (R-U4), §2.1, §6.

**Files:**

- Move: `git mv app/squash app/transcode`; `git mv pkg/events/schema/importarr.go pkg/events/schema/import.go`;
  `git mv pkg/events/schema/importarr_test.go pkg/events/schema/import_test.go`
- Modify: every `.go` file importing `github.com/mediactl/clustarr/app/squash/...` (75 at `cae73ac6`)
  or naming a branded identifier; `Makefile` (`RBAC_PATHS_squasharr := ./app/transcode/... ./app/autoscale/...`
  and the comment at `:82` -- the identity names stay for W6.9), `images/Dockerfile.native:24`
  (comment path)

**Interfaces:**

- Produces the identifiers of spec §6's table; values unchanged (U-R2). Aliases: `squashmanager` →
  `transcodemanager`, `grabarrstatus` → `grabstatus`, `squasharrstatus` → `transcodestatus`,
  `captionarrstatus` → `captionstatus`; an alias the map would make `import` becomes
  `<domain><last element>` (`importagent`), and so for any alias that would shadow an imported
  package (R-U4).

- [ ] **Step 1: Move and rewrite paths.**

  ```bash
  git mv app/squash app/transcode
  git mv pkg/events/schema/importarr.go pkg/events/schema/import.go
  git mv pkg/events/schema/importarr_test.go pkg/events/schema/import_test.go
  git grep -l -E 'app/squash\b' -- '*.go' | xargs perl -pi -e 's{app/squash\b}{app/transcode}g'
  perl -pi -e 's{\./app/squash/}{./app/transcode/}g; s{app/squash/worker}{app/transcode/worker}g' Makefile
  perl -pi -e 's{app/squash/}{app/transcode/}g' images/Dockerfile.native
  ```

  (The Go rewrite also fixes path strings such as the dependency guards' `./app/squash/...`.)
- [ ] **Step 2: List the identifiers.** In a scratch directory (never committed), a throwaway program
  over `go/scanner` prints every `IDENT` token containing a brand or `squash`/`Squash`, with its
  files, from `git ls-files '*.go' ':(exclude)pkg/legacynames'`. Map each through the token map
  (`QueueGroupCatalogar` → `QueueGroupCatalog`, `squash` locals → `transcodeRun` where the file
  imports `pkg/transcode`, `origSquash` → `origTranscode`, `TestManagerSideSquashLinksNoWorker` →
  `TestManagerSideTranscodeLinksNoWorker`, `squashmanager` → `transcodemanager`; R-U4 for keywords).
- [ ] **Step 3: Rename.** For each pair, `gofmt -r 'Old -> New' -w <its files>` (multi-letter
  identifiers are literal in `gofmt -r` patterns; it rewrites identifiers only, never strings or
  comments). Identifiers retired by U1 (`ManagerCatalogarrMarkers` → `ManagerRetiredMarkers`,
  `ManagerGrabarrEngine` → `ManagerRetiredEngine`) follow spec §6.
- [ ] **Step 4: Format and build.** gofumpt and goimports on every touched file; `go build ./...`;
  `make build`; `make generate manifests` (the `Makefile` path keeps the generated
  `config/rbac/squasharr_role.yaml` byte-identical); the rescan of step 2 prints no branded IDENT.
- [ ] **Step 5: Commit.**

  ```bash
  git add app/transcode pkg/events/schema/import.go pkg/events/schema/import_test.go
  git commit -m 'refactor!: app/squash is app/transcode, and no Go identifier carries an *arr brand -- values unchanged (Wave U U2)' -- app cmd internal pkg test ui hack api Makefile images
  ```

**Tests (deferred to the batch):** none new; the batch's `go vet ./...` and full suite run over the
renamed tree; `TestManagerSideTranscodeLinksNoWorker` (renamed) keeps its assertion.

---

### Task U3: Kubernetes names -- field managers, labels, annotations, object names, read-both, engine workload replacement

**Spec:** §2.5, §2.7, §2.9, §2.10, §4.1.5, §4.3, §4.4, §4.6.

**Files:**

- Create: `pkg/k8s/ownedby.go`
- Modify: `pkg/k8s/fieldmanager.go` (every value and its comments, spec §2.5), `app/transcode/controller/pool/render.go`
  (`LabelTemplateHash`, `AnnotationAppliedTemplate`, Job name prefix `transcode-pool-`),
  `app/transcode/controller/pool/template.go` (`ManagedByValue = "transcode"`),
  `app/transcode/controller/audiograft/controller.go` (`LabelGraft`, component `transcode-graft`),
  `app/grab/controller/downloadclient/workload.go` (`componentEngine`, `DefaultEngineServiceAccount`),
  `app/grab/controller/downloadclient/controller.go` (selector replacement),
  `app/grab/lifecycle/constants.go` (`RecorderGrabEngine = "grab-engine"`),
  `app/catalog/manager/register.go` (recorder `catalog-history`), `app/indexer/agent/facadekey.go`
  (`facadeComponent = "index"`), `app/indexer/agent/register.go` and `internal/cli/agent/options.go`
  (`DefaultFacadeAPIKeySecret = "index-facade"`), `app/catalog/searchoutcome/names.go`,
  `app/catalog/controller/search/reconciler.go` (`IndexUnavailable`; outcome matching through the
  helpers), `app/indexer/search/service.go` (`ListOutcomeName = "index"`),
  `app/import/worker/importlist/catalogitem.go`, `app/catalog/status/artwork.go`,
  `app/catalog/worker/grab/perform.go` (the three `managedFields` readers)

**Interfaces:**

- Produces: `func k8s.OwnedBy(e metav1.ManagedFieldsEntry, m FieldManager) bool` -- `e.Manager` is `m`
  or `legacynames.LegacyManager(m)` (release N only; F9 removes the legacy half);
  `searchoutcome.IsWorkerOutcome(name)`, `IsTruncatedOutcome(name)`, `Reserved(name)` (each accepts
  the `legacynames` spelling); the DownloadClient controller's `replaceForSelector`.
- Consumes: U1's `legacynames`.

- [ ] **Step 1: Field-manager values.** In `pkg/k8s/fieldmanager.go` set each value per spec §2.5;
  rewrite each comment's names by the prose rule; the two retired constants keep their
  `legacynames` values.
- [ ] **Step 2: `k8s.OwnedBy`** and the three readers. The import list's classifier check must keep
  an unrenamed Series' classification (spec §4.1.5).
- [ ] **Step 3: Labels, annotations, object names** per spec §2.7 and §2.10 in the files listed. The
  pool's cache filter and list (`transcodejob/pools.go:88,599`) follow `ManagedByValue`; the legacy
  value is used by nothing in N but the Migrator (F8.3).
- [ ] **Step 4: Search outcomes.** New values `catalog/search-worker`, `catalog/truncated`, prefix
  `catalog/`; the reconciler's comparisons (`reconciler.go:461-492`) go through the helpers, which
  also accept `legacynames.SearchOutcomeNames` keys and `SearchOutcomePrefix`.
- [ ] **Step 5: Engine workload replacement** (spec §4.4). Generalise `replaceForClaimTemplates`:
  before applying a StatefulSet or Deployment, read the live object; if its `spec.selector` differs
  from the desired one, refuse (log, `Ready=False`, reason `InvalidSpec`, message naming the policy)
  when a StatefulSet's `persistentVolumeClaimRetentionPolicy.whenDeleted` is set to anything but
  `Retain`; otherwise delete it with `PropagationPolicy(Background)` and requeue, so the next pass
  applies the new workload. Record a Normal Event `EngineWorkloadReplaced` on the DownloadClient.
- [ ] **Step 6: Gates and commit.** `go build ./...`; `make build`; `make generate manifests` (no
  RBAC change expected; a new marker, if any, synced into the chart's `rbac.yaml`).

  ```bash
  git add pkg/k8s/ownedby.go
  git commit -m 'feat!: Kubernetes names lose the *arr brands -- field managers, pool and graft labels, the engine component and SA, the facade Secret default, Event recorders, Search outcomes read both ways; engines are replaced on a selector change (Wave U U3)' -- pkg/k8s app internal
  ```

**Tests (deferred to the batch):** `TestFieldManagerValuesAreUnbranded` (every `FieldManagers()`
value matches no brand; `want` in `pkg/k8s/fieldmanager_test.go` rewritten); `TestOwnedByReadsTheLegacyName`;
`TestClassifiedSeriesKeepsItsValuesBeforeTheRename` (envtest: Series spec owned by a legacy
`catalogarr-classify` entry survives an import list re-apply); `TestManagedFieldsAreReadThroughOwnedBy`
(guard: no non-test comparison of `ManagedFieldsEntry.Manager` outside `pkg/k8s`);
`TestSearchOutcomesReadBothNames`; `TestPoolJobsCarryTheTranscodeLabels` and
`TestGraftJobsCarryTheTranscodeLabels` (rewrite of the pool and audiograft template tests);
`TestASelectorChangeReplacesTheEngineWorkloadKeepingItsClaims` (envtest: a StatefulSet with the
legacy selector and a claim template is deleted and re-created; the PVC with the same name is
untouched); `TestAReplacementRefusesADeletingClaimPolicy`; rewrite
`TestGrabarrEnginesRunAsAnAccountTheInstallerBinds` (as `TestGrabEnginesRunAsAnAccountTheInstallerBinds`,
W6.11 makes it pass).

---

### Task U4: NATS names -- streams, subjects, durables, RPC, schemas; displacement, handover, legacy subjects

**Spec:** §2.6, §4.2.

**Files:**

- Modify: `pkg/events/subjects.go` (stream, filter, subject, consumer, RPC and queue-group values;
  `TranscodeTaskConsumerName` → `transcode-pool-…`), `pkg/events/agents.go`
  (`ConsumerIntakeCandidate`, `ConsumerIntakeScan`, `EngineConsumerPrefix`, `RPCIndexBlocklist`, the
  subject builders), `pkg/events/probe.go` (`ConsumerImportProbeHigh`/`Low`, descriptions),
  `pkg/events/topology.go` (descriptions; `Topology.Displaced`; `ConsumerSpec.HandoverFrom`;
  `Retired` may name an undeclared stream; `Validate`), `pkg/events/consumerstate.go` (`AckFloorStreamSeq`), `pkg/events/bus.go`
  (`StreamAdmin.RetireStream`), `pkg/events/schema/{import.go,importlist.go,imports.go,probe.go}`
  (`Schema()` values; `LegacySchemas()` on `ScanTask` and `ListTask`), `pkg/events/natsbus/{natsbus.go,admin.go,ensure_consumer.go,bind.go}`,
  `pkg/events/membus/{membus.go,admin.go}`, `pkg/busconn/{busconn.go,keep.go}`,
  `app/catalog/history/target.go`, `app/catalog/history/replay/replay.go`,
  `app/catalog/worker/history/dlq.go`, `app/transcode/controller/transcodejob/withdraw.go`
  (`poolDurablePrefix` from `events`)

**Interfaces:**

- Produces: `events.DisplacedStream{Old, New string}`, `Topology.Displaced []DisplacedStream` (the
  six pairs, `Old` from `legacynames.Streams`); `ConsumerSpec.HandoverFrom string` (set on
  `catalog-history` and `catalog-rss-matcher`); `events.ConsumerState.AckFloorStreamSeq uint64`;
  `events.StreamAdmin.RetireStream(ctx, name string) (events.StreamCensus, error)` (state, then
  delete; NotFound is success) with `StreamCensus{Stream string; Messages uint64; BySubjectToken
  map[string]uint64; Pending map[string]uint64}`; `busconn.KeepTopology(ctx, nc, bus, t,
  busconn.OnRetired(func(events.StreamCensus)))` (option; F8.3 passes the metrics hook); the
  `Blocked` list in Ensure's report.
- Consumes: U1.

- [ ] **Step 1: Values.** Every value per spec §2.6 (streams; `FilterWork*`; the `clustarr.work.<token>`
  builders and constants; durables incl. `transcode-pool-` and the doubled
  `clustarr.work.markers.markers.`; `clustarr.rpc.index.*`, `clustarr.rpc.catalog.metadata.*`;
  queue groups `index`, `catalog`). Retired durables keep their `legacynames` values (U1).
- [ ] **Step 2: Schemas.** `import.*` values; `ScanTask.LegacySchemas()` and
  `ListTask.LegacySchemas()` return their `legacynames.Schemas` keys (the `main` values, U1 step 1).
- [ ] **Step 3: Displacement** (spec §4.2.1). `Ensure` (both buses): for a `Displaced` pair whose
  `Old` stream exists, create neither `New` nor any consumer on it; add both to the report's
  `Blocked`; never delete. `Missing` reports them. `RetireStream` (both buses). `KeepTopology`: on
  "lease acquired", before its first ensure, `RetireStream` each present `Old` (log the census at
  Warn; call the `OnRetired` hook), delete each legacy durable's dead-letter watcher on
  `CLUSTARR_ADVISORIES` (`events.DeadLetterWatcherName(old stream, old durable)` for every
  `legacynames.Durables` key and kept durable on that stream) and purge its advisory subject, then
  ensure. A failure is logged and retried at the next tick. `Retired` entries whose stream is a
  displaced `Old` are dropped from `Default()` (the stream's deletion covers them).
- [ ] **Step 4: Handover** (spec §4.2.3). In `EnsureConsumer` and `Ensure` (both buses): when the
  durable is absent and `HandoverFrom` names a durable present on the same stream, create it with
  `DeliverByStartSequence`, `OptStartSeq = AckFloorStreamSeq + 1` of the legacy one; when both exist
  and the legacy floor is ahead of the new durable's, delete and recreate the new one at the legacy
  floor + 1; on every update, carry the live consumer's `DeliverPolicy` and `OptStartSeq` into the
  config sent. Never delete the legacy durable (N+1 does).
- [ ] **Step 5: Legacy subjects** (spec §4.2.6). The replayer republishes to
  `legacynames.Subject(original)`; `history.Target` resolution and the DLQ projector resolve
  `legacynames.Subject(subject)` before matching.
- [ ] **Step 6: Gates and commit.** `go build ./...`; `make build`; `go list -deps ./cmd/manager`
  linkage unchanged.

  ```bash
  git commit -m 'feat(events)!: NATS names lose the *arr brands -- six work streams displaced (created once the leader retires the old), event-stream durables handed over at the legacy ack floor, index and catalog RPC subjects, import.* schemas reading their predecessors, DLQ replays mapped (Wave U U4)' -- pkg/events pkg/busconn app/catalog/history app/catalog/worker/history app/transcode/controller/transcodejob
  ```

**Tests (deferred to the batch):** `TestNoTopologyNameIsBranded` (every stream, consumer, filter,
subject builder output, RPC subject and queue group of `Default()`); `TestADisplacedStreamBlocksItsReplacementUntilRetired`
(contract, both buses: with `Old` present, `Ensure` creates neither `New` nor its consumers and
reports them; after `RetireStream`, `Ensure` creates them); `TestKeepTopologyRetiresDisplacedStreamsOnlyOnTheLease`
(the start-up `EnsureTopology` deletes nothing); `TestAHandoverDurableStartsAfterTheLegacyAckFloor`
(contract, real server: legacy durable acked to seq 41 of 50; the new one's first delivery is 42);
`TestAHandoverKeepsItsDeliverPolicyOnUpdate`; `TestAHandoverBehindTheLegacyFloorIsRecreated`;
`TestRetireStreamReportsTheCensusThenDeletes` and `NotFound` is success;
`TestEnsureSingleNodeTopologyFitsTheKindServersLimits` (rerun with N-1's six streams pre-created:
start-up `Ensure` succeeds, and after retirement the full topology fits);
`TestLegacySchemasDecodeScanAndListTasks`; `TestReplayRepublishesALegacySubjectUnderItsNewToken`;
`TestResolversResolveLegacyDeadLetters`; `TestTranscodePoolDurableNamesAreKVSafe`; rewrite every
contract and topology test expecting old values.

---

### Task U5: Go text -- remaining literals and comments, `api/` comments, generated CRDs and templ

**Spec:** §2.2-§2.4, §4.6, R-U5.

**Files:** every `.go` and `.templ` file still matching the gate pattern outside `pkg/legacynames`;
generated: `config/crd/bases/*.yaml` (`make manifests`), `api/**/zz_generated*.go` and
`applyconfiguration/**` if their comments change (`make generate`), `ui/**/_templ.go` (`make templ`).

- [ ] **Step 1: Mechanical pass** (re-runnable at every rebase onto `main`):

  ```bash
  git grep -l -I -i -E '(catalog|import|index|grab|caption|segment)arr|squas[h]' -- '*.go' '*.templ' ':(exclude)pkg/legacynames' \
    | xargs perl -pi -e '<the token map from Global Constraints>'
  ```

- [ ] **Step 2: `squash` in prose and names.** `git grep -n -i 'squas[h]' -- '*.go' '*.templ'` and
  rename each by hand (`"squash"` registration name in `internal/cli/manager/run.go:257` →
  `"transcode"`; comments).
- [ ] **Step 3: Prose review.** Read every changed comment and message hunk; reword what reads badly
  (R-U5), e.g. "transcode worker: …" is fine, "the transcode transcodes" is not; ui help text in
  `ui/forms/kinds.go` names the component as a user knows it ("the transcode controller").
- [ ] **Step 4: Retired-name review.** `git grep -n -E 'catalog-markers|grab-engine|catalog-grab|catalog-redownload|transcode-results|catalog-segments-result' -- '*.go'`:
  each hit must be a live use (the `catalog-markers` durable, the `grab-engine` SA, recorder, label
  value or durable prefix, the `catalog-grab` field manager); a hit naming the retired manager,
  durable or finalizer is reworded to cite the `legacynames` constant (spec §9 risk 3).
- [ ] **Step 5: Generate and build.** `make generate manifests`; `make templ`; gofumpt/goimports;
  `go build ./...`; `make build`.
- [ ] **Step 6: Commit.**

  ```bash
  git commit -m 'refactor: no Go string, comment, CRD description or ui text names an *arr brand (Wave U U5)' -- api app cmd config/crd internal pkg test ui hack
  ```

**Tests (deferred to the batch):** rewrite every test whose expected message, span or text changed
(the batch finds them by `go test`); `TestUIHelpTextNamesNoBrand` folds into the gate guard.

---

### Task U6: Non-Go files Wave U owns, and the live docs

**Spec:** §2.3, §2.12, §5, Q1.

**Files:** `hack/kind.sh`, `hack/parity-clips.sh` (comments), `config/default/pvc.yaml`,
`config/samples/catalog_v1alpha1_overlayprofile.yaml`, `CLAUDE.md`, `README.md`,
`docs/observability.md`, `docs/gpu-nodes.md`, `docs/adr/README.md`; top notes in
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`,
`docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md`,
`docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`. **Not** the legacy installer
set (Global Constraints) and not the historical docs.

- [ ] **Step 1: Mechanical pass** on the files above (the token map), then the prose rule: CLAUDE.md's
  historical narratives name the component by its new name ("the caption service crash-looped",
  spec R-U5); its services table, invariants and gotchas use the new identifiers and values
  (`k8s.ManagerCatalog`, `catalog-metadata`, `clustarr.rpc.catalog.metadata.search`,
  `CLUSTARR_WORK_MARKERS`, `app/transcode/...`).
- [ ] **Step 2: Add the names rule to CLAUDE.md**, under "Services": three sentences -- the domain
  names are the only internal names; `pkg/legacynames` alone spells the chart 0.4.x names, and
  release N's Migrator carries them over; `make brand-gate` holds it.
- [ ] **Step 3: Notes.** `docs/adr/README.md`: one line under the index, "Wave U (2026-10-07)
  renamed the internal *arr names to domain names; ADRs keep the names they were written with --
  read them through docs/superpowers/specs/2026-10-07-unbranded-names-design.md §6." Each in-flight
  spec: one line under its title, "> **Names (Wave U, 2026-10-07):** this spec uses the pre-Wave U
  names; read them through docs/superpowers/specs/2026-10-07-unbranded-names-design.md §6."
- [ ] **Step 4: Commit.**

  ```bash
  git commit -m 'docs: live docs and CLAUDE.md use the domain names; the ADR index and the in-flight specs point at the Wave U mapping (Wave U U6)' -- CLAUDE.md README.md docs/observability.md docs/gpu-nodes.md docs/adr/README.md docs/superpowers/specs/2026-10-06-manager-agent-split-design.md docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md hack/kind.sh hack/parity-clips.sh config/default/pvc.yaml config/samples
  ```

**Tests (deferred to the batch):** none beyond U7's guard.

---

### Task U7: The gate

**Spec:** §7.

**Files:**

- Create: `hack/brand-gate.sh` (below, verbatim)
- Modify: `Makefile` (`.PHONY: brand-gate` and `brand-gate: ## Fail if a retired *arr brand name appears outside pkg/legacynames and the records.` running `hack/brand-gate.sh`)

```bash
#!/usr/bin/env bash
# hack/brand-gate.sh -- the Wave U gate (docs/superpowers/specs/2026-10-07-unbranded-names-design.md
# §7): prints every line that names a retired *arr brand (or the derived app directory) outside
# the legacy-name table and the historical records, and exits 1 if there is one. The pattern below
# spells no brand, so this file does not match itself.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

re='(catalog|import|index|grab|caption|segment)arr|squas[h]'
readme=charts/clustarr/README.md

# Temporary: the per-service installer set W6.9-W6.15 delete or rewrite. W6.17 deletes this list.
temp=(
  ':(exclude)charts/clustarr'
  ':(exclude)config/manager'
  ':(exclude)config/rbac'
  ':(exclude)config/prometheus'
  ':(exclude)config/postgres'
  ':(exclude)config/e2e'
  ':(exclude)config/README.md'
  ':(exclude)Makefile'
  ':(exclude)hack/e2e.sh'
)

# The chart README's "Upgrading from 0.4.x" section names the retired values keys on purpose.
lo=0 hi=0
if [[ -f $readme ]]; then
  read -r lo hi < <(awk '/^## Upgrading from 0\.4\.x/ { s = NR; next }
    s && /^## / { print s, NR - 1; found = 1; exit }
    END { if (s && !found) print s, NR; else if (!s) print 0, 0 }' "$readme")
fi

{ git grep -n -I -i -E "$re" -- . \
    ':(exclude)pkg/legacynames' \
    ':(exclude)docs/superpowers' \
    ':(exclude)docs/research' \
    ':(glob,exclude)docs/adr/[0-9]*.md' \
    ${temp[@]+"${temp[@]}"} || true; } |
awk -v readme="$readme" -v lo="$lo" -v hi="$hi" -v re="$re" '
  {
    path = $0; sub(/:.*/, "", path)
    rest = substr($0, length(path) + 2)
    line = rest; sub(/:.*/, "", line)
    text = tolower(substr(rest, length(line) + 2))
    if (path == readme && line + 0 >= lo + 0 && line + 0 <= hi + 0 && lo + 0 > 0) next
    # Citations of historical files whose own names carry a brand.
    gsub(/docs\/research\/phase-d1-index[a]rr-spec\.md/, "", text)
    gsub(/docs\/superpowers\/plans\/2026-09-19-phase-d1-index[a]rr\.md/, "", text)
    gsub(/docs\/superpowers\/plans\/2026-09-22-phase-d2-gra[b]arr\.md/, "", text)
    gsub(/\.superpowers\/sdd\/2026-09-22-phase-d2-gra[b]arr\//, "", text)
    if (text ~ re) { print; n++ }
  }
  END {
    if (n) { printf "brand-gate: %d line(s) name a retired brand outside pkg/legacynames and the records\n", n > "/dev/stderr"; exit 1 }
  }'
```

(Dry run at `9f788545`, before any U task: 4,832 lines reported, 0 from the script itself; the
citation at `test/e2e/download_test.go:21` filtered as intended.)

- [ ] **Step 1:** Write the script (mode 755) and the Makefile target.
- [ ] **Step 2: The wave gate.** `make brand-gate` prints nothing and exits 0; `go build ./...`;
  `make build`; `make generate manifests` and `make templ` leave the tree clean;
  `git grep -n -E '"(catalogarr|importarr|indexarr|grabarr|squasharr|captionarr|segmentarr)' -- '*.go' ':(exclude)pkg/legacynames'`
  prints nothing; `go list -deps ./cmd/manager | grep -E 'ffgo|purego|onnx|anacrolix|pkg/download/usenet|pkg/relindex'`
  prints nothing; the ledger records the deferred tests of U1-U7.
- [ ] **Step 3: Commit.**

  ```bash
  git add hack/brand-gate.sh
  git commit -m 'build: make brand-gate -- no *arr brand outside pkg/legacynames and the records; the installer set W6 rewrites is allowed until W6.17 (Wave U U7)' -- hack/brand-gate.sh Makefile
  ```

**Tests (deferred to the batch):** `TestNoBrandNameOutsideTheLegacyTable` (`test/guards`: runs
`hack/brand-gate.sh`, fails with its output); `TestTheBrandGateAllowListIsExact` (the script's
permanent excludes are exactly spec §7's; the temporary list is empty from W6.17 on);
`TestTheBrandGateDoesNotMatchItself`.

---

## Amendments to the main plans

Each task below carries one line under its heading pointing here
(`> **Amended by Wave U:** see docs/superpowers/plans/2026-10-07-unbranded-names.md, "Amendments", <task>.`);
its own text is not edited, and every name in it is read through spec §6. Tasks not listed take
the mapping only.

| Task | Plan | Change |
| --- | --- | --- |
| **F8.3** (Migrator) | split | Add the names part. Files: `app/catalog/legacyfold/{renamefields.go,names.go,legacyjobs.go}`. `RenameManagedFields(entries []metav1.ManagedFieldsEntry, rename func(string) (string, bool)) ([]metav1.ManagedFieldsEntry, bool, error)` (pure, spec §4.1.2, `fieldpath.Set.Union`); `(*Migrator).namesPass` over spec §4.1.3's kinds (metadata-only APIReader pages of 500) and owner-derived Secrets and PVCs, sending spec §4.1.1's JSON patch with field owner `clustarr-legacy-fold`; runs in both modes; shares the Migrator's 20/s limiter. `legacyJobsPass`: delete Jobs labelled `app.kubernetes.io/managed-by=` `legacynames.PoolManagedBy` (background) after the fold's withdrawal. `FinalizerTaskWithdrawal` = `legacynames.TaskWithdrawalFinalizer`. Metrics: `clustarr_legacy_fold_objects{state="legacy_managers"}`; `clustarr_legacy_fold_preparations_total{action}` gains `rename_managers`, `delete_legacy_job`, `retire_stream` (results `ok`, `conflict`, `unmergeable`, `error`), the last wired through U4's `busconn.OnRetired` hook in `internal/cli/manager`. RBAC (`legacyfold/rbac.go`): `patch` on every kind of §4.1.3 and their status-free main resource, `secrets get;patch`, `persistentvolumeclaims get;patch`, `jobs list;delete`. Tests (batch): spec §4.1.6's seven. |
| **F8.4** (adoption in the loop) | split | The markers release apply runs under `k8s.ManagerRetiredMarkers` (value `legacynames.MarkersStatusManager`); the claim side applies under `catalog`. `legacyfold.HasMarkersEntry` reads the retired name; nothing else in the loop reads a legacy name. |
| **F8.5** (report and flags) | split | The report gains a "Names" section: per kind, objects with legacy manager entries and which names; `unmergeable` (must be 0); legacy Jobs; the legacy and new facade Secrets (present, same keys); with `--nats-url`, the six legacy streams' message counts by subject token and the handover durables' floors; legacy-subject dead letters. New `manager legacy-fold restore-names [--namespace] [--nats-url] [--writes-per-second 25]` (spec §4.8 step 3a): refuses while `manager.clustarr.io` is held; inverse rename by the same merge rule; deletes N's six renamed work streams; exits non-zero while any new name remains. No new flag on the manager. |
| **F8.6** (dead letters, replays) | split | U4 already maps legacy subjects in the replayer, resolvers and projector; F8.6's additions resolve through `legacynames.Subject` too. |
| **F8.9** (e2e) | split | The upgrade-and-rollback scenario asserts after the upgrade: no legacy manager on any object once the census reads 0, `CLUSTARR_WORK_TRANSCODE` present and `CLUSTARR_WORK_SQUASHARR` gone, `catalog-history` starting after the legacy floor, engines re-created with component `grab-engine`; and on rollback runs `restore-names` and the engine deletion (spec §4.8) before `helm rollback`, then asserts N-1's `catalogarr` applies clear a field N's rename had re-claimed. |
| **F8.11** (split spec amendments) | split | Add spec §8's supersessions: split R7 (brand half), §3.11 "Unchanged", §10.0's kept names, loop §7.3.9's two retirements, A-plan R22's N+1 deletion of `catalogarr-grab`. |
| **F8.12** (fold gate) | split | Runs `make brand-gate` (temporary list still in force). |
| **F9.1** (remove the legacy fold) | split | `namesPass`, `legacyJobsPass`, `RenameManagedFields`, `restore-names` and the report's Names section go with the fold. |
| **F9.4** (bus names, retired managers) | split | `RetiredFieldManagers()` empties (both retired names); `k8s.OwnedBy` loses its legacy half; `Topology.Displaced` empties; `Topology.Retired` gains `catalogarr-history`, `catalogarr-rss-matcher` (with A9.2's `catalogarr-redownload`) and their watchers from `legacynames`; `ScanTask`/`ListTask` `LegacySchemas()` lose the `importarr.*` values; the replay mapping goes; `legacynames` keeps `ValuesKeys` and the three durables. Gate: the report shows 0 legacy managers and 0 legacy-subject dead letters. |
| **F9.5** (docs, N+1 runbook) | split | The N+1 runbook deletes `<fullname>-indexarr-facade` after comparing its keys with `<fullname>-index-facade`. |
| **A4.4** (grab worker retires) | A-plan | `agentdomain.Draining()` names `legacynames.GrabDurable` and `legacynames.RedownloadDurable`; the grab durable is deleted in N with its displaced stream (R22's N+1 deletion moves to N), the redownload durable stays idle until A9.2. |
| **A6.4** (facade key in the manager) | A-plan | `EnsureFacadeKey`: when the configured Secret is absent and the legacy one (`legacynames.FacadeSecret`, or the configured name with `FacadeSecretSuffix` replaced) exists, create the new Secret with its keys unchanged (component `index`, manager `index`); never write or delete the legacy one. Test (batch): `TestTheFacadeKeyIsCopiedFromTheLegacySecret`. |
| **A7.2** (roles, field-manager homes) | A-plan | Role and manager names per spec §6 (`clustarr-grab-engine-role`; `catalog`, `import-worker`, …); the retired names are homeless. |
| **A8.1** | A-plan | Names per spec §6 only. |
| **A8.2** (Migrator's Download part) | A-plan | Downloads are excluded from the names pass (their `grab`, retired `grabarr-engine` and `import` entries go with them). |
| **A8.3** (report) | A-plan | The Downloads section sits beside F8.5's Names section; `downloads export` unchanged. |
| **A8.4** (runbook) | A-plan | Loop §7.5: step 1's pre-flight report must show `unmergeable` 0; step 6 also records each legacy work stream's message count (they are displaced, not drained); step 10 checks the names census trends to 0, `CLUSTARR_WORK_SQUASHARR` gone and `CLUSTARR_WORK_TRANSCODE` present, `catalog-history` and `catalog-rss-matcher` with `deliver_policy: by_start_sequence`, the engines' component `grab-engine` and SA `<fullname>-grab-engine`, and the two facade Secrets holding the same keys. Loop §7.6 gains spec §4.8's steps 3a, 3b and the amended step 5. |
| **A8.5** (gate additions) | A-plan | Add `make brand-gate`. |
| **A9.2** (old grab path NATS state) | A-plan | `catalogarr-grab` is already gone (deleted with `CLUSTARR_WORK_CATALOGARR` in N); retire `catalogarr-redownload` with F9.4's two handover legacies. |
| **A9.5** (N+1 gate) | A-plan | Adds F9.4's names checks. |
| **W6.9** (one ClusterRole per identity) | split | The engine identity is `grab-engine`: `RBAC_ROLES` entry `grab-engine`, `RBAC_PATHS_grab-engine`, generated `config/rbac/grab_engine_role.yaml`, ClusterRole `clustarr-grab-engine-role`; `git rm config/rbac/grabarr_engine_role.yaml`. |
| **W6.11** (topology switch) | split | Every name it writes per spec §6: engine SA `<fullname>-grab-engine` (kustomize `grab-engine`) bound to `clustarr-grab-engine-role`; `CLUSTARR_ENGINE_SERVICE_ACCOUNT` and `CLUSTARR_FACADE_API_KEY_SECRET` (`<fullname>-index-facade`, kustomize `index-facade`) values; `config/e2e/{importarr,indexarr}-e2e-patch.yaml` rewritten for the new topology under unbranded names (`agent-import-e2e-patch.yaml`, `agent-index-e2e-patch.yaml`) or folded into `manager-e2e-patch.yaml`; `$squashEnv` → `$transcodeEnv`; comments by the prose rule. |
| **W6.13** (closed schema, chart 0.5.0) | split | `consumerSlots` keys are the new durable names (`markers.consumerSlots.markers-analyze`, `agents.import.consumerSlots.import-fileimport`, …) and the per-domain enums list them; the facade default is `<fullname>-index-facade`; the README's "Upgrading from 0.4.x" table keeps the 0.4.x keys (the gate's README section) and adds rows for the engine SA and facade Secret names; `TestRetiredValuesKeysAreRejected` reads `legacynames.ValuesKeys`; `TestTheChartREADMENamesOnlyLiveValuesKeys` builds its retired-key pattern from `legacynames.ValuesKeys`. |
| **W6.14** (index PVC kept, config READMEs) | split | `config/README.md` by the prose rule. |
| **W6.15** (e2e renames) | split | The legacy Deployment names in `hack/e2e.sh` and `test/e2e` become spec §10.0's; Wave U already renamed `test/e2e`'s Go identifiers and strings mechanically (a `deployment/catalog`-style name W6.15 replaces). |
| **W6.17** (wave gate) | split | Delete the `temp` array from `hack/brand-gate.sh`; `make brand-gate` must pass over the whole tree. |
| **W10.2** (ADR-0018) | split | Uses the domain names; cites spec §6 for the old ones. |
| **W10.4** (design of record) | split | The design of record and amendment 1 are records (Q1): a top note pointing at spec §6, no rename. |
| **W10.5** (docs) | split | `docs/autoscaling.md` and `docs/observability.md` print the new stream and durable label values. |
| **W10.6** (READMEs) | split | Chart README outside the upgrade section, `images/distroless/README.md`, `config/README.md`: the domain names. |
| **W10.7** (CLAUDE.md) | split | Keep U6's names paragraph; add `TestNoBrandNameOutsideTheLegacyTable` and `make brand-gate` to the guard list. |
| **W10.8** (final gate) | split | Runs `make brand-gate`. |

---

## Under-specified points, and how this plan decides them

- **Where the old names live** (spec §3): one data-only package, read by the Migrator, topology, read-both
  helpers, report and tests; not per-package `legacy.go` files, which the gate could not allow-list
  cleanly.
- **Which tool renames identifiers** (U2): `gofmt -r` per identifier, driven by a scratch
  `go/scanner` listing -- precise (identifiers only) without committing a tool.
- **Dual-use strings** (U-R3) are classified by use before any mechanical pass.
- **The legacy installer set** is W6's; the gate's temporary allow-list holds it to W6.17 (spec §5).
- **Why every work stream is displaced, not only the overlapping one** (spec §4.2.1): single-node
  memory, and one mechanism.
- **Durables on kept streams** hand over at the ack floor and keep the legacy durable idle for a
  rollback (spec §4.2.3).
- **No RPC dual-serving** (spec §4.2.4).
- **The facade copy is A6.4's** (U-R5); **the census metric is F8.3's** (U-R4).
