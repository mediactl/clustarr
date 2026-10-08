# The test batch: T1 compile, T2 deferred tests, T3 green, by domain (unify branch)

> **For agentic workers:** this plan is executed by the controlling session with about six
> parallel agents, one per domain, each in its own git worktree, phase by phase (T1, then T2, then
> T3), with a gate and a merge between phases. The machine-readable list of every test is
> `docs/superpowers/plans/2026-10-07-test-batch-catalogue.tsv`; this file is its key, its
> partition and its order. Hard rules for every agent are in "Execution" at the end.

**Goal.** Every wave since Wave 0 deferred its tests (owner, 2026-10-07: "Skip tests for now --
quickly implement all planned changes and then add tests for the whole batch"). The batch makes
every test package compile (T1), writes every deferred test (T2), and turns `make test`,
`make test-race` and `make lint` green, fixing the bugs the tests find in the code (T3).

**Branch** `unify-manager-agent`. **Inventory taken at** `ac521b88` (`go vet ./...`, the
test-name index of the tree and of history) and re-checked at `66e44603`: W9.3-W9.7 (par2go)
landed during the inventory and made `pkg/download/usenet` fail to compile; nothing else moved.
**Re-run `go vet ./...` at the batch start**: W9.8 and anything later can move T1.

**Inputs.** The ledger `.superpowers/unify/progress.md` (every "Skipped -> batch" line); the side
lists `f66-deleted-tests.txt`, `f7-tests.txt`, `removal-deleted-tests.txt`,
`removal-skipped-tests.txt`; the plans `2026-10-06-manager-agent-split.md` (every `### Task`),
`2026-10-07-agents-report-over-nats.md`, `2026-10-07-unbranded-names.md`,
`2026-10-07-series-search.md`, each with its "Cold-turkey revision" (dropped tests excluded) and
the A plan's "Amendments to the main plan"; ADR-0020 and ADR-0021 Consequences (Wave N); the
guards CLAUDE.md marks with †.

---

## The catalogue

`2026-10-07-test-batch-catalogue.tsv`, tab-separated, one header line, one line per test:

| Column | Meaning |
| --- | --- |
| `domain` | `D1`..`D6` below; `-` for a dropped row whose package no longer exists |
| `target_package` | the package directory the test lives in (new ones included: `internal/testmedia`, `test/nativetest`, `test/goldens`, `test/par2test`, `test/parity/segments`, `test/fixtures/mkclips`, `test/guards/depguard`) |
| `test_name` | the Go name to write. `TestBusContract/<Case>` is a contract case: a `t.Run("<Case>", …)` in `pkg/events/contracttest`, run by `natsbus` and `membus` |
| `source` | the task(s) that name it (plan task id, or the ledger / ADR / side list), then a bracketed note where the row needs one: a rename (`plan name …`, `was …`), a re-home, `delete`, the file a rebuild starts from, or why a row is dropped |
| `status` | `deferred`, `rebuild-from-<sha>^`, `rewrite` or `dropped-excluded` |

What each status asks:

- **`deferred`** -- no test of that name exists. Write it as its task says: the plan's code body
  where the plan gives one, else the one-line assertion of its "Tests (deferred to the batch)"
  list. The plans predate later waves: adapt names and signatures to the API as built, keep the
  assertion. A row whose subject no longer exists is not resurrected: drop it with a one-line
  reason in the domain's T2 report.
- **`rebuild-from-<sha>^`** -- the test existed and an implementer deleted it where the plan said
  move, port or copy it (F3.2/F3.3 copies, F5.1/F5.2 ports, F6.3 moves), or with code that
  survives in another shape (F9.2's MediaFile-keyed worker). `git show <sha>^:<path>` (the path is
  in the note) is the starting point; re-home it as the plan's task says (`git show` only -- never
  check out, stash or reset).
- **`rewrite`** -- a test of that name exists at `target_package` and a task edits, moves or
  renames it (`edit per plan`, `rename of …`), or deletes it (`delete …` with no `edit`). Most
  deletes are T1 work.
- **`dropped-excluded`** -- listed for traceability only: the cold-turkey revisions' 66 dropped
  tests, the side lists' deletions whose subject is gone, and the tests later waves superseded.

How it was built, so a reviewer can redo it: every `### Task` of the four plans was parsed for
`func Test…`/`func Fuzz…` in its Go blocks (the file taken from the nearest path the task names),
for the names under "Tests (deferred to the batch)", and for contract cases (`t.Run("…")` in
`contracttest` blocks, `TestBusContract/<Case>` in prose); ledger skip lines, the side lists, the
ADRs and CLAUDE.md † were added and cross-checked (every ledger, side-list and † name is in the
catalogue, live or dropped with a reason). Excluded by construction: W0.1-W0.23 (the ffgo
repository), W1, W2a and W2b (written test-first; their names exist), and names that occur only
inside `-run` patterns. Each name was then classified against the tree (`git grep '^func Test'`)
and against every `func Test` line `git log -p` ever removed. File attribution is heuristic for
about one row in ten; the T2 check reports a test found in another package as `ELSEWHERE`, not
as missing.

---

## a. The domain partition

Six domains with disjoint package ownership: a domain's agent edits only files under its own
package directories, so two worktrees never touch one file. Ownership is by longest matching
prefix. The rule for the loop's adapters and item stages under `app/remediation`: each belongs to
the domain of the planner it adapts.

| Domain | Owns (package prefixes) | Packages (with tests) | T1 failing | T2 rows (deferred / rebuild / rewrite) |
| --- | --- | --- | --- | --- |
| **D1** Bus, records and platform libraries | `pkg/events`, `pkg/records`, `pkg/probestore`, `pkg/subtitlestore`, `pkg/k8s`, `pkg/busconn`, `pkg/presence`, `pkg/obs`, `pkg/agentdomain`, `pkg/fsops`, `pkg/binpath`, `pkg/names`, `pkg/lang`, `pkg/textcase`, `pkg/redact`, `pkg/ratelimit`, `pkg/version`, `app/dispatch`, `app/maintenance`; helpers: `contracttest`, `schematest`, `pkg/obs/metrics` `wantSeries` | 33 (19) | 5 | 217 (201 / 0 / 16) |
| **D2** Catalog, items, markers and segments | `app/catalog`, `app/remediation/itemtest`, `app/remediation/artwork`, `app/remediation/metadata`, `app/remediation/search`, `app/remediation/grab`, `app/remediation/markers`, `app/intake`, `app/segments`, `pkg/segments`, `cmd/markers`, `test/parity/segments`, `pkg/decision`, `pkg/quality`, `pkg/metadata`, `pkg/release`, `pkg/overlay`; helpers: `app/remediation/itemtest` | 96 (74) | 26 | 221 (206 / 0 / 15) |
| **D3** The MediaFile loop, transcode and test media (W6b) | `app/catalog/controller/mediafile`, `app/remediation`, `app/transcode`, `cmd/transcode`, `pkg/transcode`, `pkg/mediainfo`, `pkg/ffruntime`, `pkg/naming`, `pkg/audioalign`, `internal/testmedia`, `test/nativetest`, `test/parity`, `test/goldens`; helpers: `internal/testmedia`, `test/goldens`, `test/nativetest`, `app/remediation/remediationtest` | 38 (20) | 14 | 336 (254 / 63 / 19) |
| **D4** Acquisition: downloads, import, index, subtitles | `app/grab`, `app/import`, `app/indexer`, `app/caption`, `app/remediation/downloads`, `app/remediation/dlindex`, `app/remediation/subtitles`, `pkg/download`, `pkg/par2child`, `pkg/relindex`, `pkg/torznab`, `pkg/newznab`, `pkg/cardigann`, `pkg/importlist`, `pkg/socks5`, `pkg/subtitles`, `test/parity/subtitles`, `test/par2test`; helpers: `probetest`, `storetest`, `socks5test`, `test/par2test` | 94 (69) | 23 | 297 (237 / 27 / 33) |
| **D5** Processes, CLI, UI and autoscaling | `ui`, `cmd/manager`, `cmd/agent`, `cmd/ui`, `internal/cli`, `app/autoscale`, `pkg/pipeline`; helpers: -- | 53 (13) | 6 | 144 (135 / 0 / 9) |
| **D6** Guards, installers, API schemas, system and e2e | `test`, `pkg/crdcheck`, `api`, `config`, `hack`, `images`, `charts`; helpers: `test/starttest`, `test/installtest` | 46 (23) | 4 | 195 (171 / 0 / 24) |
| **Total** | | 360 (218) | 78 | 1410 (1204 / 90 / 116) |

Non-package files: `Makefile`, `hack/**`, `images/**`, `.github/**`, `config/**`, `charts/**` and
`api/**` belong to D6; `go.mod`/`go.sum` to nobody (no `go get`, no `go mod tidy`: a needed
dependency goes to the controller). Generated files are regenerated (`make generate manifests
templ`), never hand-edited; a T3 fix that needs regeneration touches D6's `api/` and `config/`,
so it goes through the controller.

Why these boundaries, from the import graph:

- `app/remediation` imports `app/catalog` 61 times and every domain package; its root (the loop
  core: F3.1, most of F4 and A3.3) is MediaFile-centred, so it goes with the per-file planners to
  D3; its item stages (`search`, `grab`, `metadata`, `artwork`) go to D2, `downloads`/`dlindex` to
  D4, `subtitles` to D4 with `app/caption`, `markers` to D2 with the catalog's markers planner,
  `transcode`/`graft` to D3 with `app/transcode`.
- `app/catalog/controller/mediafile` still holds the probe and naming planners
  (`plan_probe.go`, `probe.go`, `naming.go`) the loop calls: D3.
- `app/caption` sits with acquisition (D4): its fetch worker is an agent domain over external
  providers, like the indexer agent; this also balances D3.
- `app/segments`, `pkg/segments`, `cmd/markers` and `test/parity/segments` go with the catalog's
  markers planner (`app/catalog/markers`, `segmentplan`, `worker/markers`) in D2.
- `app/dispatch` and `app/maintenance` are bus-centred (the dispatch ledger, object-store purges):
  D1. `app/autoscale` (the HPA and the External Metrics server) goes with the processes: D5.
- `cmd/transcode` exercises `app/transcode/worker`: D3. `cmd/markers`: D2. `cmd/manager`,
  `cmd/agent`, `cmd/ui`: D5.

### Shared test helpers, one owner each

| Helper | Owner | Consumers | What the batch adds | Phase |
| --- | --- | --- | --- | --- |
| `pkg/events/contracttest` | D1 | `pkg/events/natsbus`, `membus`, `pkg/busconn` | W4.42 `Bind` (bind-only `Subscribe` makes a test that subscribes an undeclared durable hang until it lands), every contract case in the catalogue | T2a |
| `pkg/events/schema/schematest` | D1 | `pkg/events/schema` (D1), `app/catalog/history` (D2) | `Payloads()` gains the A1.4, F3.4, F5.1 and F6.4 payloads (`TestEveryPayloadHasAResolver` reads it) | T1 |
| `app/remediation/itemtest` | D2 | catalog item envtests (D2), `app/remediation` (D3) | F4.2's `itemtest.Start` as built; harness edits F4.1-F4.3 name | T2a |
| `app/remediation/remediationtest` (new) | D3 | `app/remediation/{probe,naming,rename}` (D3), `markers` (D2), `subtitles` (D4) | F3.1's package; F3.3's `probeharness.go`; `ImportarrCreatesMediaFile*` | T2a |
| `app/import/worker/probe/probetest` | D4 | `app/remediation/probe` (D3), `app/import` (D4) | check it against the native prober (W8.6) | T2a |
| `pkg/relindex/storetest`, `pkg/socks5/socks5test` | D4 | D4 only | the A3.1 blocklist methods on the store fakes | T1 |
| `internal/testmedia` (new, W6b) | D3 | D2, D3, D4, D6 | W6.18; then W6.19-W6.22 conversions by each package's owner | T2a |
| `test/goldens` (new, W6.23) | D3 | `pkg/mediainfo/native` (D3), `test/parity/segments` (D2) | W6.23's reader and the recorded goldens | T2a |
| `test/nativetest` (new, W7.3) | D3 | `pkg/segments/decode` (D2), `pkg/mediainfo/native`, `app/transcode/worker` (D3), `pkg/subtitles/providers/embedded/native` (D4) | W7.3 | T2a |
| `test/par2test` (new, W9.2) | D4 | `pkg/par2child`, `pkg/download/usenet` | W9.2, unless W9's own agent wrote it | T2a |
| `test/starttest` | D6 | `cmd/manager`, `cmd/agent` (D5), `test/system` (D6) | W3.2/W5.4's exported helpers (today it holds only test files), A7.2's `AssertWroteOnlyAs` | T1 (compile), T2a |
| `test/installtest` | D6 | `test/guards` (D6), `cmd/{manager,agent}` manifest tests (D5), `cmd/markers` (D2) | W5.4's helpers, W6.11's `RolesFor` | T2a |
| `pkg/obs/metrics` `wantSeries` | D1 | every domain's metrics | one table edit for every metric the waves registered (F1.1, A1.7, A2.2, F6.3, S7, N3) | T2b |

---

## b. Per domain

Each domain section has its T1 list (every test package `go vet` fails on, with the first error),
its T2 list (every live catalogue row, grouped by package: `Name (source)`, with `R` for a
rebuild and `E` for a rewrite of an existing test), and its T3 notes. Prerequisites common to all
of T3 are under "d".

### D1 -- Bus, records and platform libraries

The bus and its contract suite, the records protocol and its stores, the Kubernetes helpers, the bus connector, presence, observability, the agent-domain table, the dispatch ledger and the maintenance loop. Every other domain's tests run on this one's fakes and contract: it goes first inside each phase.

**T1 -- 5 failing test packages** (first error at the inventory):

| Package | First error |
| --- | --- |
| `pkg/events` | pkg/events/segments_topology_test.go:53:59: undefined: WorkSegmentsResultSubject |
| `pkg/events/natsbus` | pkg/events/natsbus/objectstore_contract_test.go:78:3: cannot use map[string]string{…} (value of type map[string]string) as events.ObjectMeta value in argument to store.Put |
| `pkg/events/schema` | pkg/events/schema/schema_test.go:127:17: undefined: schema.GrabTask |
| `pkg/fsops` | pkg/fsops/parts_test.go:34:72: unknown field JobUID8 in struct literal of type fsops.TranscodePart |
| `pkg/k8s` | pkg/k8s/predicates_test.go:94:32: undefined: downloadv1alpha1.Download |

`schematest.Payloads()` is fixed here in T1 (D2's `app/catalog/history` reads it). `pkg/k8s`'s Download fixtures move to a kind that still exists (A9 ledger); `pkg/fsops`'s part tests take `FileUID8`/`Seq` in place of `JobUID8`/`Attempt` (F6.4's part name).

**T2 -- 217 tests** (201 deferred, 0 rebuilt, 16 rewritten; 21 more dropped, in the catalogue only). Order inside the domain: W4.42 `contracttest.Bind` and the contract cases (T2a); the records core (F1.1-F1.4, A1.5); the topology and Validate tests (W4.40-W4.46, W4.90-W4.103, F1.2, A1.6, Wave U U4, N1-N6); `pkg/k8s` (W3.1, W4.62, W4.80, A1.7, F3.4); presence, dispatch and maintenance (A2.1, A2.2, N4, N5, N7); the metrics `wantSeries` last (T2b).

- `app/dispatch` (7): `TestAdmitHoldsTheBudgetAndReportsTheReason` (A2.2), `TestAdmitRefusesUntilRebuilt` (A2.2), `TestAnUnconfirmedReservationLapses` (A2.2), `TestNoRespondersReadsAsTheDomainsNoAgent` (Wave N N4), `TestRenderFollowsTheDeliveryTable` (A2.2), `TestTheLedgerIsRebuiltFromStatusNeverTrusted` (A2.2), `TestUnattendedNeedsTwoQuietMinutesOrNoPresence` (A2.2)
- `app/maintenance` (2): `TestBackfillFingerprintMetaSetsTheCompleteMap` (Wave N N7), `TestEveryObjectStoreIsPurgedOfOrphans` (ADR-0020)
- `pkg/agentdomain` (6): `TestAgentDomainIsALeaf` (W4.63), `TestAutoscaledConsumersSizeMaxAckPendingForTheCeiling` (W4.63), `TestDomainsCoverEveryTopologyConsumer` (W4.63), `TestFixedConsumersKeepMaxAckPendingEqualSlots` (W4.63), `TestMinZeroDomainsConsumeOnlyWorkQueueStreams` (W4.63), `TestTheDomainTable` (W4.63)
- `pkg/busconn` (9): `TestKeepTopologyEnsuresAtTheLeaseAndReplacesWhatGoesMissing` (W4.46), `TestKeepTopologyNeedsABusThatCanReportWhatIsMissing` (W4.46), `TestKeepTopologyReEnsuresWhenNATSReconnects` (W4.46), `TestReconnectDelayBacksOffWithJitter` (ADR-0020), `TestShutdownDrainsPullsThenRespondersThenTheConnection` (ADR-0020), `TestValidateURLAcceptsAPool` (Wave N N6), `TestWatchDeadLettersNeedsABusThatCanWatch` (W4.46), `TestWatchDeadLettersWatchesEveryStaticConsumerUntilCancelled` (W4.46), `TestWedgeCheckerReportsTheBus` (W4.97)
- `pkg/events` (52): `TestAPoolDurableKeepsItsCapThroughItsSubscription` (W4.40), `TestAckDeadline` (W4.40), `TestAgentRecordBucketsAreDurableHistoryOneAndBounded` (A1.6), `TestAutoscaledDurablesNeverExpire` (W4.100), `TestClassifyRequestError` (ADR-0020), `TestConsumerStateLagCountsDeliveredAndUndelivered` (W4.43), `TestDeadLetterWatcherSpecKeepsTheLiveConfig` (W4.41), `TestDefaultConsumersCarryTheirSlots` (W4.40), `TestEngineConsumerNamesAreInjectiveAndKVSafe` (A1.6), `TestEnvelopeFromHeadersDropsTransportHeaders` (W4.91), `TestEnvelopeFromHeadersKeepsTheIDOfAFiredSchedule` (W4.91), `TestEveryStaticConsumerHasOneDeadLetterWatcher` (W4.41), `TestHandlerBudgetFallsBackToAckWait` (W4.97), `TestIntakeAndCommandStreamsAreDurableAndDiscardNew` (A1.6), `TestMergeRetiredKeepsTheReportThatSawTheDurable` (F1.2), `TestMsgIDForProbeNamesTheRequest` (W4.2), `TestMsgIDForSubtitleFetchCarriesTheSequence` (F5.1), `TestNoTopologyNameIsBranded` (U4), `TestObjectStoresCarryTheirKeyScheme` (W4.101), `TestOnlyExplicitBudgetsAreEnforced` (W4.97), `TestParseKVKeyTokenInvertsKVKeyToken` (F1.1), `TestParseSlotOverrides` (W4.40), `TestRecordKeysAreUIDTokens` (F1.1), `TestRecordsBucketsAreDurableHistoryOneAndBounded` (F1.2), `TestRecycleSweepConsumer` (W4.60), `TestSlotsFor` (W4.40), `TestSubscriptionSpecInvertsSubscription` (W4.40), `TestTaskEventSubjectsListOnlyDispatchedConsumersAndNeverGt` (A1.6), `TestTheFoldsRetiredBusNamesAreGone` (F9.4), `TestTheProbeBucketKeepsARecordForAWeek` (W4.2), `TestTheProbeConsumersAreOnTheProbeStream` (W4.2), `TestTheProbeStreamIsDurableAndRefusesWhenFull` (W4.2), `TestTheResultsDurableIsRetired` (F6.6), `TestTheTranscodeAndSubtitleBuildersAreKeyedByTheFile` (F9.4), `TestTimingEqualComparesDeadlines` (W4.93), `TestTranscodePoolDurableNamesAreKVSafe` (U4), `TestValidateAdmitsUnlimitedDeliveryOnlyForWatchers` (W4.41), `TestValidateChecksRetiredConsumers` (F1.2, A9.2), `TestValidateExemptsDiscardNewWorkQueuesFromSchedules` (W4.40), `TestValidateHoldsEveryRecordsBucketToTheRule` (F1.2), `TestValidateHoldsSlotsUnderTheCap` (W4.40), `TestValidateRefusesAChunkOverOneMiB` (ADR-0020), `TestWorkFetchSubjectAnyPriorityMatchesEveryLane` (F5.1), `TestConsumerSpecSubscription` (W4.40, W4.40, W4.63 E), `TestDeadLetterSubjectNamesTheFailedTask` (W4.2 E), `TestEveryConsumerHasDeliveryHeadroom` (W4.41 E), `TestEveryWorkStreamHasAConsumer` (W4.41 E), `TestForSingleNodeKeepsDurableBucketsOnFile` (F1.2 E), `TestKVKeyBuildersAreInjective` (W4.2, F1.1 E), `TestSegmentsTopology` (W4.63, F9.4 E), `TestTranscodeTopology` (F6.6 E), `TestWorkStreamsAllowSchedules` (W3.15, W4.40 E)
- `pkg/events/contracttest` (39): `TestBusContract/AHandlerPastItsBudgetIsCancelled` (W4.97), `TestBusContract/ALapsedDeliveryCannotExtendItsRedelivery` (W4.92), `TestBusContract/ALapsedFinalDeliveryIsDeadLetteredWithABindOnlySubscribe` (W4.45), `TestBusContract/AScheduledTaskKeepsItsIDAndItsDeadLetter` (W4.91), `TestBusContract/AScheduledTasksLapsedFinalDeliveryIsDeadLettered` (W4.91), `TestBusContract/BindCreatesTheDurableAndItsWatcher` (W4.42), `TestBusContract/ConsumerStateCountsPendingAndAckPending` (W4.43), `TestBusContract/DrainLetsRunningHandlersFinish` (W4.44), `TestBusContract/EnsureConsumerIsIdempotentAndBindsTheWatcher` (A1.6), `TestBusContract/KVKeys` (F1.3), `TestBusContract/KVStatus` (F1.3), `TestBusContract/KVWatchDeliversPastTheBuffer` (F1.3), `TestBusContract/KVWatchFromRevision` (F1.3), `TestBusContract/KVWatchUpdatesOnly` (F1.3), `TestBusContract/LapsedHandlerFreesItsSlot` (W4.44), `TestBusContract/LapsedHandlersDoNotMultiplyConcurrency` (W4.44), `TestBusContract/MaxAckPendingCapsEveryReplicaTogether` (W4.43), `TestBusContract/ObjectStoreMetadataRoundTrip` (W4.101), `TestBusContract/ObjectStorePutReplacesMetadata` (W4.101), `TestBusContract/ObjectStoreSetMetaKeepsContent` (W4.101), `TestBusContract/ObjectStoreSetMetaOnDeletedIsNotFound` (W4.101), `TestBusContract/ObjectStoreStatusReportsCreation` (W4.102), `TestBusContract/ObjectStoreWatchDeliversPastTheBuffer` (W4.102), `TestBusContract/ObjectStoreWatchRefusesFromRevision` (W4.102), `TestBusContract/ObjectStoreWatchReplaysThenSynced` (W4.102), `TestBusContract/ObjectStoreWatchSeesDeleteAsTombstone` (W4.102), `TestBusContract/ObjectStoreWatchSeesSetMeta` (W4.102), `TestBusContract/ObjectStoreWatchUpdatesOnlyHasNoSynced` (W4.102), `TestBusContract/ReplicasShareMaxAckPendingBeyondOneSlotCount` (W4.44), `TestBusContract/RetiredConsumersAreDeletedAndPurged` (F1.2), `TestBusContract/SaturatedSubscriptionNeverDeadLettersAnUnrunTask` (W4.90), `TestBusContract/SetMetaSendsTheCompleteMap` (N8; ADR-0021), `TestBusContract/StreamFillReportsBytesAndTheLimit` (W4.96), `TestBusContract/SubscribeBindsAndNeverWritesConsumerConfig` (W4.45), `TestBusContract/SubscribeRebindsAVanishedDurable` (W4.44), `TestBusContract/SubscribeWaitsForAMissingDurable` (W4.45), `TestBusContract/SubscriptionHoldsNoMoreThanItsSlots` (W4.44), `TestBusContract/TheBusHeartbeatsARunningHandler` (W4.97), `TestBusContract/TimingComesFromTheBoundDurable` (W4.93)
- `pkg/events/membus` (2): `TestAWatcherThatNeverReadsBlocksNoWriter` (F1.3), `TestMembusDrainWaitsForCalls` (Wave N N1)
- `pkg/events/natsbus` (34): `TestASlowResponderLeavesItsQueueGroupOnlyWithAPeer` (ADR-0020), `TestArtworkPutsUseTheStoresChunkSize` (ADR-0020), `TestAsyncErrorKinds` (W4.94), `TestAsyncErrorsAreCounted` (W4.94), `TestCloseStillHaltsAtOnce` (ADR-0020), `TestConsumerStateExcludesExhaustedMessages` (W4.100), `TestConsumerStateReadsTheConsumerLeader` (W4.100), `TestConsumerStateRefusesAnAnswerWithoutPlacement` (W4.100), `TestDrainAnswersEveryBufferedRequest` (ADR-0020), `TestEveryScheduledSubjectKeepsItsID` (W4.91), `TestIndexAppliesPutsSetMetaAndDeletes` (W4.104), `TestIndexEmitsChanges` (W4.104), `TestIndexKeepsServingWhileItReopens` (W4.104), `TestIndexNeverBlocksOnASlowSubscriber` (W4.104), `TestIndexReplaysThenSyncs` (W4.104), `TestKVMaxValueSize` (F1.2), `TestLapsedDeliveriesNeverExceedTheSlots` (W4.44), `TestMessageReadsANakedMessageByStreamSeq` (A2.4), `TestNextFetchesOnlyFreeSlots` (W4.90), `TestObjectIndexRebuildsWhenTheBucketIsRecreated` (W4.104), `TestObjectWatchSurvivesAServerRestart` (W4.102), `TestOnlyPullWritesConsumerConfig` (W4.45), `TestPublishReconnectBufExceededIsTransient` (Wave N N6), `TestPullWritesTheSubscriptionsCapNotItsSlots` (W4.40), `TestPurgeOrphanChunksReclaimsALostRace` (W4.103), `TestPurgeOrphanChunksSparesAPutInProgress` (W4.103), `TestSampleFrequencyReachesTheConsumerConfig` (A1.6), `TestSubscribeRebindsAfterItsStreamIsDeleted` (W4.44), `TestTermWithReasonCarriesTheClustarrID` (A2.4), `TestTranscodeTaskMsgIDDedupesOnTheRealServer` (F6.4), `TestWatcherAckWaitCoversItsBatch` (W4.41), `TestEnsureDefaultTopology` (W4.40 E), `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` (A1.6 E), `TestKVKeyBuildersProduceKeysARealServerAccepts` (W4.2, F1.2 E)
- `pkg/events/schema` (10): `TestBlockScopeOfIsInjective` (A1.4), `TestEveryPayloadRoundTrips` (A1.4), `TestEveryRecordHeaderKeyIsOptionalButTheProbes` (F1.1), `TestMarkersTaskRoundTrip` (F3.4), `TestProbeRecordWireFormatIsUnchanged` (F1.1, A1.4), `TestProbeTaskAndRecordRoundTrip` (W4.3), `TestRecycleSweepTaskEncodeDecodeRoundTrip` (W4.60), `TestSubtitleEventIsV2AndNamesTheMediaFile` (F5.2), `TestSubtitleRecordKeepsTheHeaderFlat` (F5.1), `TestSegmentsResultRoundTrip` (F3.4 E)
- `pkg/k8s` (22): `TestANilChecksIsEmpty` (W3.1), `TestAddProbesRegistersPingThenEachSetInOrder` (W3.1), `TestAddProbesRejectsDuplicates` (W3.1), `TestAgentManagerOptionsNeverElect` (W5.3), `TestAnUntaggedWriteRecordsTheDefaultFieldOwner` (W4.80), `TestCaptionWorkerIsRetired` (F5.3), `TestChecksKeepTheirOrder` (W3.1), `TestChecksMergeKeepsOrderAndRejectsACollision` (W3.1), `TestChecksRejectWhatControllerRuntimeWouldSilentlyOverwrite` (W3.1), `TestClampTextNeverSplitsARune` (F2.2), `TestFieldManagerValuesAreUnbranded` (U3), `TestGraftJobsCarryTheTranscodeLabels` (U3), `TestLeaderOnlyWaitsForTheLease` (W3.1), `TestManagerOptionsPinTheDefaultFieldOwner` (W4.80), `TestPoolJobsCarryTheTranscodeLabels` (U3), `TestReadOnlyRefusesEveryWriterAndCounts` (A1.7), `TestRetiredFieldManagersStayValid` (F3.4, F5.3), `TestSanitizeTextReplacesWhatTheApplyDecoderRefuses` (F2.2), `TestScaleToWritesTheScaleSubresource` (W4.62), `TestTheDefaultFieldOwnerIsNoFieldManager` (W4.80), `TestFieldManagersAreTheOnesTheSpecLists` (F3.4, W4.62, F9.4, U3 E), `TestOptionsValidate` (W4.74 E)
- `pkg/obs/metrics` (1): `TestCatalogueMatchesTheAmendmentTable` (A1.7, S7, S9, F1.1, A1.7, A2.2 E)
- `pkg/presence` (3): `TestPresenceLinksNoKubernetes` (A2.1), `TestPresenceWritesAtStartAndEveryInterval` (A2.1), `TestThePresenceReaderCachesForFiveSeconds` (A2.1)
- `pkg/probestore` (9): `TestAFailureIsClippedOnARuneBoundary` (W4.4), `TestASeedAnswersAnOutstandingRequest` (W4.4), `TestAnOversizedAnswerIsRecordedAsAFailure` (F1.1), `TestDecodeSkipsDeletesAndGarbage` (W4.4), `TestSeedNeverOverwritesAMatchingResult` (W4.4), `TestTheProbeStoreLinksNoProberAndNoClient` (W4.4), `TestTheProbeStoreRefusesWhatTheBucketWould` (F1.2), `TestTheRecordProtocolHoldsOnARealServer` (W4.4), `TestTheStoreCountsUnreadableRecords` (F1.1)
- `pkg/records` (14): `TestAnswerReReadsBeforeItsCAS` (F1.1), `TestAnswerRules` (F1.1), `TestKeyOfGovernsTheKeyCheck` (A1.5), `TestNextSeqIsMonotoneAcrossExpiry` (F1.1), `TestOversizedRecordIsRefused` (F1.1), `TestRecordsLinksNoKubernetes` (F1.1, A1.5), `TestRequestIsACompareAndSwapAtTheReadRevision` (F1.1), `TestRequestNeverOverwritesAnUnincorporatedFact` (F1.1), `TestThePacerReservesOneSlotPerKey` (F1.1), `TestTheSearchClassPacesPerNamespace` (A1.5), `TestUndecodableRecordReadsAbsent` (F1.1), `TestWithdrawRules` (F1.1), `TestWriterDropsALowerSeqAndAllowsARepeatedOne` (A1.5), `TestWriterRereadsAfterACASMiss` (A1.5)
- `pkg/records/agentrecords` (2): `TestSearchRecordKeyOfCarriesTheSub` (S1), `TestSeasonSearchKeyIsAValidKVKeyAndInjective` (S1)
- `pkg/records/recordsource` (5): `TestNewItemsEnqueuesEveryKeyAndSkipsDeletes` (A1.5), `TestSourceResumesFromRevisionAfterAClose` (F1.4), `TestSourceSkipsTheLoopsOwnWrites` (F1.4), `TestSourceSurvivesABucketRecreatedUnderIt` (F1.4), `TestStartFailsWhenTheBucketNeverAppears` (F1.4)

**T3 notes.**

- Known red before the batch (catalogued as rewrites): `TestConsumerSpecSubscription`, `TestEveryConsumerHasDeliveryHeadroom`, `TestSegmentsTopology`, `TestFieldManagersAreTheOnesTheSpecLists`, `TestKVKeyBuildersAreInjective`, the `pkg/agentdomain` tests, `TestCatalogueMatchesTheAmendmentTable`; the membus `Pull`/`Hooks` contract cases were red at A1.
- `natsbus` tests run an embedded nats-server; rerun `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` against A1.6's budget (file streams 640 MiB, file buckets 1,760 MiB, object stores 6,144 MiB, 8,544 MiB of 20 GiB).
- W10's finding: registered but never recorded -- five `clustarr_download_*`, `clustarr_reconcile_errors_total`, `clustarr_artwork_audit_tasks_total` (A5 retired the audit). `wantSeries` surfaces them: record or unregister, with the owner's metric doc.
- Bind-only `Subscribe` (W4.45): a test anywhere that subscribes a durable the topology does not declare hangs; T2a's `Bind` is the cure.

### D2 -- Catalog, items, markers and segments

The catalog: item kinds and their reconcilers, the item key stages for search, grab, metadata and artwork, the grab and search planners, metadata and artwork, history and replay, intake, and the markers and segments pipeline (planner, agent worker, `pkg/segments`, `app/segments`, `cmd/markers`, `test/parity/segments`).

**T1 -- 26 failing test packages** (first error at the inventory):

| Package | First error |
| --- | --- |
| `app/catalog/agent/catalog` | app/catalog/agent/catalog/wiring_envtest_test.go:36:2: no required module provides package app/catalog/worker/grab; to add it: |
| `app/catalog/artwork` | app/catalog/artwork/reaper_test.go:67:65: cannot use map[string]string{…} (value of type map[string]string) as "pkg/events".ObjectMeta value in argument to fx.store.Put |
| `app/catalog/controller/album` | app/catalog/controller/album/filestate_envtest_test.go:104:15: r.Reconcile undefined (type *album.Reconciler has no field or method Reconcile) |
| `app/catalog/controller/audiobook` | app/catalog/controller/audiobook/reconciler_test.go:231:82: undefined: downloadac.DownloadApplyConfiguration |
| `app/catalog/controller/book` | app/catalog/controller/book/itemevent_envtest_test.go:97:15: r.Reconcile undefined (type *book.Reconciler has no field or method Reconcile) |
| `app/catalog/controller/comic` | app/catalog/controller/comic/itemevent_envtest_test.go:97:15: r.Reconcile undefined (type *comic.Reconciler has no field or method Reconcile) |
| `app/catalog/controller/episode` | app/catalog/controller/episode/seriesmonitored_internal_test.go:91:15: r.Reconcile undefined (type *Reconciler has no field or method Reconcile) |
| `app/catalog/controller/issue` | app/catalog/controller/issue/reconciler_test.go:108:82: undefined: downloadac.DownloadApplyConfiguration |
| `app/catalog/controller/movie` | app/catalog/controller/movie/reconciler_test.go:229:82: undefined: downloadac.DownloadApplyConfiguration |
| `app/catalog/controller/overlayprofile` | app/catalog/controller/overlayprofile/controller_test.go:298:37: undefined: catalogstatus.PatchOverlay |
| `app/catalog/controller/search` | app/catalog/controller/search/grab_test.go:52:27: undefined: BuildDownloadSource |
| `app/catalog/controller/series` | app/catalog/controller/series/cascade_test.go:98:14: r.Reconcile undefined (type *series.Reconciler has no field or method Reconcile) |
| `app/catalog/controller/wantedcron` | app/catalog/controller/wantedcron/runnable_envtest_test.go:120:28: unknown field Bus in struct literal of type Runnable |
| `app/catalog/grabsource` | app/catalog/grabsource/grabsource_test.go:159:8: undefined: SourceApplyConfiguration |
| `app/catalog/history` | app/catalog/history/target_test.go:117:51: undefined: schema.GrabTask |
| `app/catalog/markers` | app/catalog/markers/due_test.go:98:54: undefined: markers.MsgID |
| `app/catalog/metadata` | app/catalog/metadata/extras_test.go:94:21: multiple-value ServeExtras(bus, bus.KV(events.BucketPlexExtras), reg, clock) (value of type (stop func(), err error)) in single-value context |
| `app/catalog/metadata/artwork` | app/catalog/metadata/artwork/export_test.go:23:27: (*Fetcher).fetchOne undefined (type *Fetcher has no field or method fetchOne, but does have method FetchOne) |
| `app/catalog/segmentplan` | app/catalog/segmentplan/plan_test.go:121:51: undefined: episode.MediaFileByEpisodeIndex |
| `app/catalog/worker/artwork` | app/catalog/worker/artwork/handler_test.go:108:47: cannot use h (variable of type map[string]string) as "pkg/events".ObjectMeta value in argument to s.ObjectStore.Put |
| `app/catalog/worker/markers` | app/catalog/worker/markers/handler_test.go:68:51: unknown field MediaFile in struct literal of type "pkg/events/schema".MarkersTask |
| `app/catalog/worker/rssmatcher` | app/catalog/worker/rssmatcher/match_test.go:203:21: undefined: downloadv1alpha1.Download |
| `app/catalog/worker/search` | app/catalog/worker/search/nonvideo_envtest_test.go:36:2: no required module provides package app/catalog/worker/grab; to add it: |
| `app/segments/worker` | app/segments/worker/worker_test.go:152:19: undefined: schema.SegmentsResult |
| `pkg/segments` | pkg/segments/segments_test.go:44:32: cannot use k (variable of string type "api/catalog/v1alpha1".MarkerKind) as segments.Kind value in struct literal |
| `pkg/segments/decode` | pkg/segments/decode/decode_test.go:56:29: unknown field FFmpeg in struct literal of type decode.Decoder |

Most failures are one shape: `r.Reconcile` is gone (the item kinds run through `remediation.ItemReconciler`, F4.2; use `itemtest`), `downloadac`/`downloadv1alpha1` are gone (A9: owners' `status.downloads` entries), `ObjectStore.Put` takes `events.ObjectMeta` (W4.101), `ServeRPC`/`ServeExtras` return `(stop, err)` (N1). `app/catalog/agent/catalog` and `app/catalog/worker/search` import the deleted `app/catalog/worker/grab` (A4.4): delete those tests or re-home their intent onto `grabplan`.

**T2 -- 221 tests** (206 deferred, 0 rebuilt, 15 rewritten; 39 more dropped, in the catalogue only). Order inside the domain: `itemtest` (T2a); pure planners (`grabplan`, `searchplan`, `pkg/decision`, `rollup`); item keys and stages (F4.x, A3.3, A4.x, A5.x, S2-S9); history and intake (A2.3-A2.5, F8.6); markers and segments (W7.x, F3.4, N7) after `test/nativetest` and `internal/testmedia` land; agent and manager registration last.

- `app/catalog/agent` (4): `TestAssertIndexesFailsWhenTheIndexesAreMissing` (W3.3), `TestAssertIndexesPassesOnceRegistered` (W3.3), `TestNewSceneMapsBuildsASource` (W3.6), `TestRegisterIndexes` (W3.3)
- `app/catalog/agent/catalog` (7): `TestQueueWorkersShareTheDomainsWiring` (W3.6), `TestRegisterRequiresTheBus` (W3.6), `TestRegisterWiresTheCatalogDomain` (W3.6), `TestTheDomainLeavesTheBlocklistPathLive` (W5.17), `TestRegisterWorkerIndexesIsTheOnlyRegistrar` (W3.6 E), `TestSetupControllersRegistersEveryCatalogController` (W3.8 E), `TestSetupWorkersSkipsTheQueueWorkersForRoleMetadata` (W3.6, W3.7 E)
- `app/catalog/agent/events` (2): `TestRegisterWiresTheEventsDomain` (W3.6), `TestTheEventsDomainRunsOnlyTheRSSMatcher` (A2.5, A4.4)
- `app/catalog/agent/metadata` (1): `TestTheMetadataDomainSubscribesNoQueueWorker` (W3.7)
- `app/catalog/artwork` (6): `TestKeyedLockSerialisesOneKeyAndNotTwo` (W4.106), `TestMetaCurrent` (W4.105), `TestObjectMetaIsTheCompleteSet` (W4.105), `TestObjectMetaOfAnOverlay` (W4.105), `TestResolveSourcesCarriesTheImageLanguage` (W4.105), `TestReaperAgainstARealAPIServer` (W10.8 E)
- `app/catalog/controller/album` (4): `TestAlbumWatchesEverythingButItsFiles` (F4.3), `TestAnAlbumReconcileWithoutAMetadataChangeMakesNoRPC` (F4.3), `TestReleaseCacheIsBounded` (F4.3), `TestReleaseCacheKeysOnTheAlbumItsGroupAndItsRefresh` (F4.3)
- `app/catalog/controller/audiobook` (1): `TestAudiobookWatchesEverythingButItsFiles` (F4.3)
- `app/catalog/controller/book` (1): `TestBookWatchesEverythingButItsFiles` (F4.3)
- `app/catalog/controller/episode` (3): `TestAnEpisodeOfAMultiEpisodeFileIsNeverGrafted` (F7.1), `TestEpisodeWatchesEverythingButItsFiles` (F4.2), `TestMapSeriesEnqueuesItsEpisodes` (F4.1 E)
- `app/catalog/controller/issue` (1): `TestIssueWatchesEverythingButItsFiles` (F4.3)
- `app/catalog/controller/itemstatus` (2): `TestApplyRefusesAStaleViewAndLandsAFreshOne` (F4.2), `TestApplySetReleasesAFieldItStopsSending` (A3.3)
- `app/catalog/controller/movie` (2): `TestMovieWatchesEverythingButItsFiles` (F4.2), `TestTheMovieRecordsItsDonorAndRejectsAFaultedRelease` (F7.1)
- `app/catalog/controller/rollup` (11): `TestAppendRejectedAddsADonorFaultOnceAndKeepsTheNewest16` (F7.1), `TestAudioStateACBoundsWhatItSends` (F2.3), `TestAudioStateACIsLossless` (F2.3), `TestAudioStateCarriesTheDonorWithoutAFile` (F7.1), `TestAudioStateForReadsTheFilesGraftBlock` (F7.1), `TestDonorIgnoresAVideoGrabAnUnfinishedImportAndAnotherOwner` (F7.1), `TestDonorIsCarriedForwardWhenItsDownloadIsGone` (F7.1), `TestDonorIsTheNewestImportedDonorDownload` (F7.1), `TestDownloadPredicateSeesAnImportHeldForAPerson` (F4.2), `TestEntryRollupsMatchTheDownloadRollups` (A3.5), `TestSelfMapsAnItemToItself` (F4.2)
- `app/catalog/controller/search` (13): `TestAPickBecomesAManualCandidateForTheOwner` (A4.3), `TestAPlannedSearchFailsWhenStalled` (S9), `TestAPlannedSearchMirrorsTheSeries` (S9), `TestAPlannedSearchNeverPicks` (S9), `TestAQueuedPlannedSearchWaitsAndThenRuns` (S9), `TestASeasonPickReachesTheSeriesWithItsSeason` (S9), `TestASeasonResultIsRejectedAsThePlannerWould` (S9), `TestBandedPutsOtherTitlesLastAndCountsThem` (S9), `TestCoversSeasonMarksPacksAndBatches` (S9), `TestIncorporateWritesOtherTitlesWithResults` (S9), `TestInteractiveResultsStillShowItemStateRejections` (A4.3), `TestTheSearchControllerIncorporatesTheRecordByCAS` (A4.3), `TestTranscodedFinalBindsGrabBestButNotAPick` (A4.3)
- `app/catalog/controller/wantedcron` (1): `TestWantedcronOnlyEnqueues` (A4.2)
- `app/catalog/grabplan` (15): `TestADelayedGrabIsMadeFromStatusAlone` (A4.1), `TestAFullSeasonPackWithAnUnairedEpisodeIsRefused` (S8), `TestALiveGrabRefusesAnOverlappingReleaseUnlessItUpgrades` (S8), `TestAManualPickIsExemptFromTranscodedFinalAndOverrideFromBlocklisted` (A4.1), `TestAMultiSeasonPackIsJudgedAgainstEveryEpisode` (S8), `TestAPackIsNarrowedToTheWantedEpisodes` (A4.1, S8), `TestAPackOverTwoHundredEpisodesIsRefused` (S8), `TestASeasonAnswerGrabsAPackOverSinglesOnATie` (S8), `TestASeasonPickWithoutTargetsCoversByCoverage` (S8), `TestATombstoneRejectsACandidateFromBeforeTheBlock` (A4.1), `TestAnOwnerAtItsCapPendsNeverTruncates` (A4.1), `TestBetterSinglesWinAndThePackIsThenRefused` (S8), `TestCoverageReadsSceneMappedNumbers` (S8), `TestGrabplanReChecksAgainstFreshState` (A4.1), `TestStillWantsKeepsAnUpgradeAndAnUnmetSeedObligation` (A4.1)
- `app/catalog/grabsource` (1): `TestSourceApplyConfigurationRoundTripsEveryMember` (A1.1 E)
- `app/catalog/history` (7): `TestEveryDispatchedSubjectHasAResolver` (A2.4), `TestHistoryResolvesADownloadEventToItsOwner` (A3.5), `TestParseDispatchIDReadsASeasonSearch` (S1), `TestRecycleSweepTaskResolvesToNoObject` (W4.60), `TestResolve_ProbeTask` (W4.3), `TestEveryPayloadHasAResolver` (W10.8, A1 E), `TestResolve_SelfContainedRefs` (F5.1 E)
- `app/catalog/history/replay` (1): `TestReplayKindsAreTheAnnotatableItemsOnly` (F8.6)
- `app/catalog/manager` (3): `TestRegisterAddsEveryCatalogController` (W3.8), `TestRegisterRequiresTheBus` (W3.8), `TestRegisterRunsTheR9Runnables` (W3.8)
- `app/catalog/markers` (1): `TestMarkersPlan` (F3.4)
- `app/catalog/metadata` (2): `TestMetadataJSONRoundTripsEveryKindsBlock` (A5.1), `TestTheGatewayAnswersOnlyInARecord` (A5.1)
- `app/catalog/metadata/artwork` (3): `TestBackfillUsesSetMetaNotPut` (W4.105), `TestEveryOriginalPutCarriesTheFullMetadata` (W4.105), `TestTheFetcherWritesObjectsOnlyAndRecordsFailures` (A5.2)
- `app/catalog/searchplan` (13): `TestAPacedSeasonAnswerCountsForNoEpisode` (S6), `TestAnEpisodeWaitsForItsPendingSeason` (S6), `TestAnIndexOnlySearchIsNotPacedAndALiveOneIs` (A4.2), `TestDecideSeasonsCancelsWhenTheSearchIsDeleted` (S6), `TestDecideSeasonsDispatchesOneSeasonAtATime` (S6), `TestDecideSeasonsRequeuesAnAllPacedSeasonOnce` (S6), `TestDecideSeasonsServesRequestsInOrderAfterTheHandshake` (S6), `TestDecideSeasonsSkipsWhatCannotGrab` (S6), `TestDecideSeasonsTimesOutAndGoesOn` (S6), `TestDecideSeasonsWaitsForAdmission` (S6), `TestDecideSeasonsWaitsForItsPaceSlot` (S6), `TestTheEpisodeFoldsASeasonAnswerOnce` (S6), `TestTheSearchPlannerFollowsTheBackoffLadder` (A4.2)
- `app/catalog/segmentplan` (1): `TestTheSweeperDeletesRecordsOfDeadFiles` (F3.4)
- `app/catalog/worker/artwork` (5): `TestATimedOutOriginalReadIsInputsMoved` (W4.106), `TestAnOverlayIsBackfilledWithSetMeta` (W4.106), `TestEveryOverlayPutCarriesTheFullMetadata` (W4.106), `TestRendererSerialisesOneItem` (W4.106), `TestTheRendererWritesNoStatus` (A5.3)
- `app/catalog/worker/history` (5): `TestADeadLetteredTranscodeTaskMarksItsMediaFile` (F8.6), `TestDLQProjectorAnnotatesAMediaFile` (F3.5, A2.5), `TestTheDLQProjectorMayAnnotateAMediaFile` (F5.4), `TestTheProjectorMayPatchEveryKindAResolverNames` (W4.3), `TestTheProjectorMayPatchExactlyTheKindsItAnnotates` (F8.6)
- `app/catalog/worker/markers` (7): `TestALongLimitDefersAndRepublishesAtTheReset` (F3.4), `TestASupersededTaskIsAckedUnasked` (F3.4), `TestTheHandlerAnswersByRecord` (F3.4), `TestTheLastDeliveryAnswersFailed` (F3.4), `TestTheLoopsNotAskedMessagesMatchTheWorkers` (F3.4), `TestTheMarkerWorkerLinksNoKubernetesClient` (F3.4), `TestALongLimitDefersTheTaskToItsReset` (A7.1 E)
- `app/catalog/worker/rssmatcher` (3): `TestFieldIndexesDeclareTheThirteenMatchingIndexes` (W3.4), `TestTheRSSMatcherKeepsOnlyItsItemsAndGlobalBlocks` (A3.1), `TestTheRSSMatcherSendsCandidatesAndWritesNothing` (A4.4)
- `app/catalog/worker/search` (14): `TestADonorSearchSkipsTheReleasesItsGraftsRejected` (F7.1), `TestASeasonRequestIsTitleAloneWithTheSceneSeason` (S5), `TestASeasonTaskAnswersAtItsSeasonKey` (S5), `TestASupersededSearchTaskIsDropped` (A4.3), `TestAnAllPacedSearchIsNotRecordedAsAnAttempt` (W4.87), `TestCurrentFileReadsTheImportedInfoHash` (A3.9), `TestFieldIndexesDeclareTheQueueIndex` (W3.4), `TestNamesakesFindsAnotherLibraryItemByTitle` (S5), `TestRankAndCapBandsBeforeTheCap` (S5), `TestSceneSeasonIsTheMajorityMapping` (S5), `TestTheRecordCarriesCoverage` (S5), `TestTheSearchAgentAnswersOnlyInARecord` (A4.3), `TestTheSearchWorkerRejectsABlockedReleaseFromTheAnswer` (A3.1), `TestTheSearchWorkersQueueReadsTheOwnersEntries` (A3.9)
- `app/intake` (4): `TestACandidateIsAckedOnlyAfterItsOwnerSettlesIt` (A2.3), `TestALeaderCrashRedeliversTheParkedCandidate` (A2.3), `TestAnUnsettledCandidateIsNakedAfterTheBudget` (A2.3), `TestTheScanConsumerWritesBeforeItAcks` (A2.3)
- `app/intake/advisory` (5): `TestANakAdvisoryBecomesRetryingOnTheFirstAndLastNakOnly` (A2.4), `TestAStaleAdvisoryIsAckedAndDropped` (A2.4), `TestATermAdvisoryResolvesFromTheReasonsClustarrID` (A2.4), `TestATermWithNoDLQCopyIsAnnotatedAfterSixtySeconds` (A2.4), `TestAckSamplingIsMetricsOnly` (A2.4)
- `app/remediation/artwork` (6): `TestALostBucketReadsAsNoArtworkAndRefetches` (A5.2), `TestARendererReplicaNeverRollsBackAnotherReplicasOverlay` (A5.3), `TestAnUnchangedDigestPublishesNoRender` (A5.3), `TestArtworkFetchFailedIsOneEventPerNewFailure` (A5.2), `TestArtworkStatusIsRenderedFromObjectMetadata` (A5.2), `TestOverlayStatusIsRenderedFromTheOverlayObject` (A5.3)
- `app/remediation/grab` (3): `TestAnRSSCandidateBecomesAnEntryThroughTheOwner` (A4.4), `TestEveryIntakeCandidateIsSettled` (A4.1), `TestTheGrabStageReadsAnsweredSeasonsInsideTheWindow` (S8)
- `app/remediation/markers` (5): `TestADeferredMarkersRecordIsRepublishedAtItsReset` (A7.1), `TestAMarkersRequestLeavesStatusMarkersUntouched` (F3.4), `TestASeasonAmendmentIncrementsAmend` (F3.4), `TestTheFirstLoopPassKeepsEverySeedKey` (F8.4), `TestTheSegmentPlanIsPublishedWhenAnalysisIsDue` (F3.4)
- `app/remediation/metadata` (4): `TestAMetadataRecordIsIncorporatedOnlyForTheCurrentInputs` (A5.1), `TestAnOlderRefreshNeverRollsBackANewerOne` (A5.1), `TestArtistAndAuthorIncorporateTheirRecords` (A5.1), `TestTheMetadataSetDeclaresMetadataAndArtworkTogether` (A5.1)
- `app/remediation/search` (11): `TestABlocklistedEntryMakesItsItemsDueAtOnce` (A4.2), `TestAPacedAnswerDoesNotCountAnAttempt` (A4.2), `TestASeriesSearchPlansOneSeasonAtATime` (S7), `TestAnEpisodeSearchRecordWakesTheEpisodeAndItsSeries` (A4.2), `TestNoBurstOfLiveSearchesReachesAnIndexer` (A4.2), `TestNoEpisodeSearchIsPublishedForASeason` (S7), `TestS24WakesTheChangedSeasonsEpisodes` (S7), `TestS31WakesTheSeriesOnAPlannedSearch` (S7), `TestTheDonorBudgetCapsFirstTimeLiveDonorSearchesPerSweep` (A4.2), `TestTheSeasonLedgerRebuildsAfterALeaderChange` (S7), `TestTheSeriesSetIsCompleteOnEveryPath` (S7)
- `app/segments/worker` (3): `TestAnAbandonedDecodeDeadLettersTheTask` (W7.8), `TestAnUnversionedFingerprintIsNotRead` (W7.5), `TestFingerprintCacheRecomputesOnMismatch` (Wave N N7)
- `cmd/markers` (10): `TestABadSlotOverrideExitsMisconfigured` (W5.12), `TestABadUMASKExitsMisconfigured` (W5.12), `TestASelfCheckWithoutONNXRuntimeFails` (W7.9), `TestConcurrencyIsNoLongerAFlag` (W5.12), `TestDecodeThreadsFollowTheSlots` (W7.9), `TestHealthzFailsOnAWedgedBus` (W4.98), `TestInstallerArgsParseAsMarkers` (W6.11), `TestMarkersSelfCheckNeedsNoClusterEnvironment` (W7.9), `TestReadyOnceSubscribedAndDrainedOnSIGTERM` (W5.12), `TestTheFFmpegFlagsAreGone` (W7.9)
- `pkg/decision` (12): `TestASeasonSearchSizesBySumOfCoveredRuntimes` (S2), `TestAYearlessNamesakeIsUnknownItem` (S2), `TestBandOfPutsIdentityRejectionsLast` (S3), `TestCompareEpisodeCountIsSonarrs` (S3), `TestCoverageOfMatchesTheNumberingHalf` (S2), `TestDecisionSplitIsBehaviourPreserving` (A4.1), `TestRankKeepsQualityAheadOfPacks` (S3), `TestRankPutsAPackFirstOnATie` (S3), `TestRejectedDecisionsCarryARankKey` (S3), `TestSeasonSearchAcceptsAnySingleOfTheSeason` (S2), `TestSeasonSearchReadsSceneNumbers` (S2), `TestSingleEpisodeSearchIsUnchanged` (S2)
- `pkg/segments` (8): `TestFingerprintKeyKeepsTheLegacyNameAtVersionOne` (W4.111), `TestFingerprintKeysCarryTheDecoderVersion` (W7.5), `TestLocalEnumsAreTheAPIs` (W7.4), `TestSegmentsLinksNoKubernetesAPI` (W7.4), `TestStoreReadsV1RecordsAsV2` (F3.4), `TestTheDecoderSwapDoesNotRaiseTheAnalyzerVersion` (W7.5), `TestUpdateIsACompareAndSwap` (F3.4), `TestDue` (W7.4 E)
- `pkg/segments/decode` (9): `TestACancelledDecodeReturnsWithinTheGrace` (W7.7), `TestAMissingStreamIsNamed` (W7.7), `TestAudioTSFollowsFFmpeg` (W7.7), `TestConcurrentDecodesAreRaceFree` (W7.7), `TestAFailureCarriesFFmpegsMessage` (W7.7 E), `TestAudioDecodesTheWindow` (W7.7 E), `TestFrameIsOneRGBFrame` (W7.7 E), `TestFramesDecodeKeyframesOnly` (W7.7 E), `TestFramesOnePerSecondToTheEnd` (W7.7 E)
- `test/parity/segments` (1): `TestTheDecoderMatchesTheGoldens` (W7.6)

**T3 notes.**

- Envtest suites for all eight item kinds; `worker/artwork` and `controller/overlayprofile` envtests left orphaned etcd and kube-apiserver processes (W2a ledger note): stop each environment in `TestMain`/`t.Cleanup`.
- `pkg/segments/decode`, `app/segments/worker` and `cmd/markers --self-check` need `NATIVE_ENV` (FFmpeg 9 through ffgo; ONNX Runtime for the OCR path). W7.6's parity test is `parity`-tagged and owner-run.
- `TestTheFirstLoopPassKeepsEverySeedKey` needs the owner's read-only `clustarr-segments` dump: until it exists the test skips, naming the variable that points at it (owner input, not a batch blocker).
- `cmd/markers`' deps guard: no Kubernetes client, no controller-runtime (Wave N: `pkg/busconn` leaves its deny list).

### D3 -- The MediaFile loop, transcode and test media (W6b)

The MediaFile remediation loop (its core, manager, indexes, status renderer, replay, and the probe, naming, rename, transcode and graft planners and adapters), the transcode planners, admission, pools and workers, `cmd/transcode`, the media libraries (`pkg/transcode`, `pkg/mediainfo`, `pkg/ffruntime`, `pkg/naming`, `pkg/audioalign`), and the test media infrastructure: W6b (`internal/testmedia`, the goldens), W7.3 (`test/nativetest`), `test/parity`.

**T1 -- 14 failing test packages** (first error at the inventory):

| Package | First error |
| --- | --- |
| `app/catalog/controller/mediafile` | app/catalog/controller/mediafile/watch_test.go:42:35: undefined: Reconciler |
| `app/transcode/controller/transcodeprofile` | app/transcode/controller/transcodeprofile/profile_test.go:101:14: undefined: profileHash |
| `app/transcode/jobspec` | app/transcode/jobspec/jobspec_test.go:187:61: undefined: transcodev1alpha1.TranscodeJobSpec |
| `app/transcode/manager` | app/transcode/manager/cache_test.go:35:7: undefined: DefaultOptions |
| `app/transcode/task` | app/transcode/task/task_test.go:38:3: unknown field Job in struct literal of type task.Task |
| `app/transcode/worker` | app/transcode/worker/graft_envtest_test.go imports pkg/mediainfo/ffprobeexec (no non-test files since W8) |
| `app/transcode/worker/graft` | app/transcode/worker/graft/graft_test.go:168:3: unknown field Graft in struct literal of type grafttask.Task |
| `app/transcode/worker/inprocess` | app/transcode/worker/inprocess/probe_test.go imports pkg/mediainfo/ffprobeexec (no non-test files since W8) |
| `pkg/mediainfo` | pkg/mediainfo/audio_test.go:67:16: undefined: AudioProbeFrom |
| `pkg/mediainfo/ffprobeexec` | pkg/mediainfo/ffprobeexec/incomplete_test.go:46:48: undefined: mediainfo.FrameProbeData |
| `pkg/naming/catalogctx` | pkg/naming/catalogctx/context_test.go imports pkg/mediainfo/ffprobeexec (no non-test files since W8) |
| `pkg/transcode/engine` | pkg/transcode/engine/helpers_test.go, video_test.go import pkg/mediainfo/ffprobeexec (no non-test files since W8) |
| `pkg/transcode/standard` | pkg/transcode/standard/parity_test.go imports pkg/mediainfo/ffprobeexec (no non-test files since W8) |
| `test/parity` | (go vet -tags parity) test/parity/parity_test.go imports pkg/mediainfo/ffprobeexec (no non-test files since W8) |

Six D3 packages (five, and `test/parity` under its tag) fail only because their tests import `pkg/mediainfo/ffprobeexec`, which has had no non-test file since W8: delete `ffprobeexec`'s test files first, then switch each importer to `pkg/mediainfo/native` (`testmedia.Inspect` replaces it in T2). `app/transcode/{task,jobspec,worker/graft,controller/transcodeprofile,manager}` still name TranscodeJob-era fields (`Task.Job`, `TranscodeJobSpec`, `grafttask.Task.Graft`, `profileHash`, `DefaultOptions`).

**T2 -- 336 tests** (254 deferred, 63 rebuilt, 19 rewritten; 145 more dropped, in the catalogue only). Order inside the domain: T2a first: W6.18 `internal/testmedia` -> W6.23 goldens -> W7.3 `test/nativetest` -> `app/remediation/remediationtest` (F3.1/F3.3), merged before anyone converts a clip. Then the loop core (F2.2's renderer, F3.1, F3.5), the planners and adapters (F3.2-F3.4, W4.13 re-homed, F6.2-F6.5, F7.2), the rebuilt envtests (F3.2/F3.3 copies, F6.3 moves, F9.2's worker run/swap suite on a MediaFile fixture), the media libraries and W6.19-W6.21 conversions, `cmd/transcode` last.

- `app/catalog/controller/mediafile` (8): `TestAProbeRecordOutlivesItsRequest` (W4.11), `TestJudgeProbe` (W4.11), `TestPlanProbe` (F3.2), `TestProbeLane` (W4.11), `TestRenderNaming` (F3.3), `TestSoonerAndRequeueAt` (W4.11), `TestTheProbeShortcuts` (W4.11), `TestTheRepublishWindowIsInsideTheDedupWindow` (W4.11)
- `app/remediation` (50): `TestAClaimArrivingBeforeTheCacheSeesQueuedIsIncorporated` (F2.4), `TestADeadLetteredAnnotationFoldsIntoTheLoopsCondition` (F3.5), `TestADispatchCarriesTheJoinableGraft` (F7.2), `TestADispatchWaitsForTheJoinedGraftToBeRecorded` (F7.2), `TestAGraftJobWakesTheFileThatOwnsIt` (F7.2), `TestAHungMountNeverStallsItemsOrGrants` (F2.4), `TestAHungMountNeverStallsOtherFiles` (F3.1), `TestAHungStatSaturatesThePoolNotTheCaller` (F3.1), `TestAMultiEpisodeFileWakesEveryEpisode` (F2.4), `TestAPanickingEffectDoesNotStopOtherPlannersEffects` (F3.1), `TestAPanickingPlannerKeepsItsBlock` (F3.1), `TestAProbeThatChangesTheCodecPicksTheWinnerInTheSamePass` (F2.4), `TestASetEqualAtItsPathsIsSkipped` (A3.3), `TestAStageThatPanicsKeepsItsStoredValues` (A3.3), `TestAStaleViewRequeuesInsteadOfEndingAsANoOp` (F3.1), `TestATransientErrorWritesNothing` (F3.1), `TestAWriterBetweenGatherAndApplyIsNotRolledBack` (F3.1), `TestAZeroRateDisablesTheLimiter` (F3.1), `TestAnEpisodeHasFileFlipWakesItsSeriesKey` (A3.2), `TestAnImportHeldForAPersonWakesTheItem` (F4.2), `TestAnUnchangedPassAppliesNothing` (F3.1), `TestBulkWritesArePaced` (F3.1), `TestC1ControlsNeverReachTheApply` (F3.1), `TestEachIsolatedPhaseIsAChildSpan` (F3.1), `TestEveryCatalogApplySendsTheDownloads` (A3.3), `TestFileKeyOfARef` (F3.1), `TestFileSignatureMovesWithTheGraftBlock` (F7.1), `TestGraftJobNameIsTheDispatchIdentity` (F7.2), `TestIsolateClassifiesOutcomes` (F3.1), `TestItemKeysAreTheItemsAFileCovers` (F4.1), `TestItemKindCoversTheSixItemKinds` (F4.1), `TestItemReconcilerDispatchesByKind` (F4.2), `TestItemWakeKeys` (F4.2), `TestLoopLabelKeysAreWhatMirrorLabelsWrites` (F3.1), `TestNewReconcilerSortsPlannersAndRefusesStrangers` (F3.1), `TestNoPlannerPlansFromAPendingProbe` (F3.2), `TestNormalizeTreatsNilAndEmptyAlikeAndSortsLists` (F3.1), `TestOneApplyPerManagerCASChained` (A3.3), `TestOwnStatusWritesNeverEnqueueTheFile` (F3.1), `TestPlannerOrder` (F3.1), `TestS1WakesOnTheWatchedChanges` (F3.1), `TestS24WakesOnlyTheCoveredEpisodes` (A3.3), `TestSeriesAndComicAreItemKeys` (A3.2), `TestStatusApplyIsLossless` (F3.1), `TestSubtitleProfileSignature` (F5.3), `TestTheBreakerOpensAfterFiveTimeoutsAndHalfOpens` (F3.1), `TestTheCaptionBacklogGate` (F5.1), `TestTheItemArmWakesTheItemsAFileCovers` (F4.2), `TestTheLoopLinksNoWorkerCode` (F3.1), `TestTheWriteLimiterReservesOneSlotPerKey` (F3.1)
- `app/remediation/graft` (1): `TestTheGraftJobRunsTheTranscodeBinary` (F7.2)
- `app/remediation/manager` (6): `TestALoopPassDoesNotReleaseOwnerStatus` (F3.4), `TestItemsRunEveryItemKind` (F4.3), `TestItemsRunMovieAndEpisode` (F4.2), `TestMediaFileStatusIsOwnedByCatalogAlone` (F3.4), `TestPlannerCopyPartitionsTheStatus` (F3.1), `TestRegisterAddsTheMediaFileLoop` (F3.1)
- `app/remediation/mediafilestatus` (5): `TestAC1ControlCharacterNeverBlocksTheStatusApply` (F2.2), `TestRenderCapsTheDroppedSubtitles` (F2.2), `TestRenderCarriesStoredValuesForward` (F2.2), `TestRenderClampsEveryBoundedString` (F2.2), `TestRenderSortsAndDedupes` (F2.2)
- `app/remediation/mfindex` (4): `TestExtractNamesTheItemAndEveryKey` (F4.1), `TestFilesOfListsThroughTheIndex` (F4.1), `TestIndexDeclaresTheOneName` (F4.1), `TestItemKey` (F4.1)
- `app/remediation/naming` (11): `TestTheLoopRenamesOnThePassThatMakesTheFileRenameable` (F3.3), `TestTheTemporaryListingFindsEverySidecar` (F3.3), `TestNamingEpisodeRendersThroughItsSeries` (F3.3 R), `TestNamingFollowsTheMoviesTitle` (F3.3 R), `TestNamingHoldsAStaleFileWhoseProbeFailed` (F3.3 R), `TestNamingHoldsWhileATranscodeRuns` (F3.3 R), `TestNamingKeepsItsProposalOverALookupFailure` (F3.3 R), `TestNamingProposesTheCanonicalPath` (F3.3 R), `TestNamingReportsWhyItCannotRender` (F3.3 R), `TestNamingSurvivesAMissingFile` (F3.3 R), `TestNamingUnrenderableClearsThePath` (F3.3 R)
- `app/remediation/probe` (26): `TestACrashBetweenTheApplyAndTheRequestHeals` (F3.2), `TestAFailedProbeRetriesOnItsBackoff` (W4.13), `TestAGraftIsIncorporatedOnlyFromARecordOfItsNewBytes` (W4.13), `TestAGraftedOriginalKeepsItsSpecOnEveryMainApply` (F3.2), `TestAPendingProbeKeepsEveryOtherStatusField` (W4.13), `TestAProbeAbandonedThreeTimesIsGivenUp` (W4.13), `TestAProbeRequestIsRepublishedOnlyInsideTheDedupWindow` (W4.13), `TestAProbeVersionRaiseReprobesAFreshlySeededFile` (W4.13), `TestARecreatedProbeBucketWakesEveryPendingProbe` (F1.4), `TestARenamedFileStaysReadyWithoutAProbe` (W4.13), `TestAStaleProbeRecordIsNeverIncorporated` (W4.13), `TestASwapIsIncorporatedOnlyFromARecordForItsTarget` (W4.13), `TestAnOlderAgentsResultIsNotRequestedAgainAtOnce` (W4.13), `TestNewRequestIsWhatRequestWrites` (F3.2), `TestTheProbeAdaptersSources` (F3.2), `TestTheProbeRequestFollowsTheProbePendingApply` (F3.2), `TestAFileBackFromMissingIsReadyAgainWithoutAProbe` (F3.2 R), `TestContainerChangeMovesSpecPath` (F3.2 R), `TestLookupsSelectThroughTheManagerCachesIndex` (F3.2 R), `TestObservedFingerprintIsImportsKey` (F3.2, F3.4 R), `TestObservedFingerprintWakesTheController` (F3.2 R), `TestReconcileRecordsAnEarlierInstallsTranscode` (F3.2 R), `TestReplaceSourceFalseIsNotASwap` (F3.2 R), `TestTranscodeJobWatchTriggersReconcile` (F3.2 R), `TestTranscodedFileBytesChangedOnDisk` (F3.2 R), `TestTransientFileFailuresPreserveProbedStatus` (F3.2 R)
- `app/remediation/rename` (8): `TestTheActuatorRemembersARefusedProposal` (F3.3), `TestATranscodedFileIsRenamedInItsOwnFolder` (F3.3 R), `TestReconcileHoldsOnlyForAScanWhoseSubpathCoversTheFolder` (F3.3 R), `TestReconcileHoldsWhileAScanWalksTheFolder` (F3.3 R), `TestReconcileLeavesAFileAlone` (F3.3 R), `TestReconcileRenamesAFileItsRootFolderAllows` (F3.3 R), `TestReconcileReportsACollision` (F3.3 R), `TestReconcileRequeuesAChangedFile` (F3.3 R)
- `app/remediation/replay` (2): `TestAReplayAnnotationOnAMediaFileIsActedOnByTheLoop` (F3.5), `TestAnUnparsableReplayOnAMediaFileIsRefusedAndConsumed` (F3.5)
- `app/remediation/transcode` (22): `TestAClearedBlockFreesItsSlot` (F6.5), `TestACrashBetweenEffectsNeverHidesADispatch` (F6.4), `TestAFactAfterTheClosingWithdrawalPassIsIncorporated` (F6.5), `TestAFinalDeliveryFailureShowsItsCause` (F6.4), `TestAStaleAnswerIsNotIncorporated` (F6.4), `TestASwapIsIncorporatedInThreeApplies` (F6.2), `TestCancelRecordsAVerdictAndRetryClearsIt` (F6.5), `TestCopyTakesTheTranscodeBlockAndItsNoncesOnly` (F6.2), `TestDeletedFileTaskIsWithdrawnAtOnce` (F6.5), `TestDispatchPublishesOneTaskPerSeq` (F6.4), `TestHardwareIntentAppliesAtTheNextDispatch` (F6.5), `TestHeldFilesAreWrittenOnlyOnChange` (F6.4), `TestLostWorkerIsRedispatched` (F6.4), `TestOrphanPartKeysOnTheFilesInFlightTranscode` (F6.4), `TestPriorityIntentOrdersWindowAndAdmission` (F6.5), `TestQueuedWithoutARecordIsDispatched` (F6.4), `TestRepublishStopsOnceClaimed` (F6.4), `TestRequestedWithoutATaskIsRepublished` (F6.4), `TestSuspendKeepsAWindowPlace` (F6.5), `TestTwoFilesNeverShareTheLastSlot` (F6.4), `TestUnclaimedDispatchIsWithdrawnAfterTheWindow` (F6.5), `TestWithdrawalPrecedesThePlannedWrite` (F6.5)
- `app/transcode/admission` (19): `FuzzLedgerNeverOverAdmits` (F6.3), `TestAJoinedGraftIsNeverAGraftSlotCandidate` (F7.2), `TestAnAbsentBlockFreesItsSlot` (F6.3), `TestExpiredGrantBacksOff` (F6.3), `TestFillWindowTakesPriorityThenName` (F6.3), `TestGraftGrantsFillFreeSlotsByDonorImportedAtThenKey` (F7.2), `TestGraftSlotsCountJobsStatusAndGrants` (F7.2), `TestGrantInUseNeverExpires` (F6.3), `TestObserveIgnoresAStaleObservation` (F6.3), `TestRebuildCountsEveryInFlightFileFromTheAPIServer` (F6.3), `TestRebuildSeesALastMomentDispatch` (F6.3), `TestSweepWithdrawsADeletedFilesTaskAfterRestart` (F6.5), `TestTheLedgerGrantsAGraftSlotAndTheLoopTakesIt` (F7.2), `TestWaitReasonEnqueuesOnlyOnChange` (F6.3), `TestWindowCountsWindowPhasesAndGrants` (F6.3), `TestAClassIsUnhealthyOnlyWhenEveryReportSaysSo` (F6.3 R), `TestAdmitDoesNotMutateItsInput` (F6.3 R), `TestAdmitNeverExceedsBudget` (F6.3 R), `TestChooseClassGPUOnlyWaitsWhileAGPUIsUsable` (F6.3 R)
- `app/transcode/controller/pool` (1): `TestPoolJobPredicate` (F6.3 R)
- `app/transcode/controller/transcodeprofile` (6): `TestARestartedManagerNeverSuspendsARunningPool` (F6.3), `TestTheProfileStatusCountsFilesAndTheBacklog` (F6.3), `TestAProfileShowsEachNodesDeviceHealth` (F6.3 R), `TestAProfileShowsThePublishedEncoderLimits` (F6.3 R), `TestReconcileMarksTheNewerOfTwoDefaultsInvalid` (F6.3 R), `TestReconcileSurfacesSelectorOverlapWithoutDoubleCreating` (F6.3 R)
- `app/transcode/graftplan` (23): `TestAClaimedRecordIsRunning` (F7.2), `TestADonorFaultFailsForGoodAndOtherFailuresBackOff` (F7.2), `TestAFactAfterTheWithdrawalIsIncorporated` (F7.2), `TestAFactUnderTheNextRequestClosesItAndIsIncorporated` (F7.2), `TestAFileMissingTheDonorsLanguageWaitsForASlot` (F7.2), `TestAFileUnderNoRootFolderFails` (F7.2), `TestAGraftBlockExistsOnlyWhereAGraftCan` (F7.2), `TestAGraftJobWithoutARecordFailsJobLost` (F7.2), `TestAGraftWaitsForTheFilesTranscodeAtMostSixHours` (F7.2), `TestAGrantedGraftIsQueuedThenRecordedThenItsJobCreated` (F7.2), `TestAJoinedGraftRunsWithItsTranscodeAndTakesItsResult` (F7.2), `TestALostJobIsWithdrawnBeforeItFails` (F7.2), `TestAPanickingGraftPlannerNeverLeavesAJoinedTaskBesideAWaitingBlock` (F7.2), `TestAnAnsweredGraftSwapsThenSucceedsOnItsProbe` (F7.2), `TestAnInFlightBlockOutlivesItsConditions` (F7.2), `TestEveryPlannerKeepsItsBlockWhileTheProbeIsPending` (F7.2), `TestGraftplanIsPure` (F7.2), `TestJobNameIsLabelSafeAndTheDispatchIdentity` (F7.2), `TestJoinCarriesTheGraftOfAJoinableFile` (F7.2), `TestJoinPendingHoldsTheNextDispatchUntilTheGraftRecordsItsResult` (F7.2), `TestJoinRefusesWhatCannotRideAlong` (F7.2), `TestOwedEffectsAreEmittedAgain` (F7.2), `TestTheFileCarryingTheLanguageIsPresent` (F7.2)
- `app/transcode/grafttask` (7): `TestAGraftRecordFitsItsBucket` (F7.2), `TestATaskV2RoundTripsAndAJoinedTaskCarriesNoRecordFields` (F7.2), `TestServeClaimsRunsThenAnswers` (F7.2), `TestServeDoesNothingForASupersededTask` (F7.2), `TestServeRetriesItsAnswer` (F7.2), `TestServeWaitsForItsRecord` (F7.2), `TestTheGraftFactIsAGraftedSuccess` (F7.2)
- `app/transcode/jobspec` (3): `TestEveryTranscodeWritesMP4` (F6.1), `TestNoManagerSideCodeNamesTheWorkersOutputContainer` (F6.1), `TestBuildTask` (F9.2 R)
- `app/transcode/manager` (9): `TestCacheSelectsOnlyThePoolJobs` (W5.17), `TestRegisterAddsEveryTranscodeController` (W3.12, F6.6), `TestRegisterRefusesNoLedger` (F6.6), `TestRegisterRequiresTheBus` (W3.12), `TestFormatSlotsRoundTrips` (W5.17 E), `TestParseSlots` (W5.17 E), `TestParseSlotsDefaultsToTheSpecBudget` (W5.17 E), `TestParseSlotsRejectsBadInput` (W5.17 E), `TestRolesAndValidate` (W5.17 E)
- `app/transcode/task` (3): `TestASucceededAnswerIsAFact` (F6.4), `TestResultKeepsTheWireFormat` (F9.2), `TestTaskV2NamesTheFileAndItsSeq` (F6.4)
- `app/transcode/transcodeplan` (25): `TestADispatchGrantOnAStaleViewChangesNothing` (F6.4), `TestADispatchRendersQueuedThenOwesTheRecordAndThePublish` (F6.4), `TestADispatchRewritesTheDroppedSubtitles` (F6.4), `TestAFactAfterTheClosingWithdrawalIsIncorporated` (F6.5), `TestAFinalDeliveryFailureIsRetriableWithItsCause` (F6.4), `TestALostWorkerIsRetriedAfterTheGrace` (F6.4), `TestAPlannedFileLeavesTheWindowWhenItStopsBeingEligible` (F6.2), `TestASwapIsIncorporatedWithTheDispatchedTag` (F6.2), `TestAVerdictHoldsOnlyForItsTagAndProbe` (F6.2), `TestAWindowGrantOnAHeldFileIsSkipped` (F6.2), `TestAWindowGrantPlansFromTheStoredProbe` (F6.2), `TestEligibleFollowsTheWinningProfile` (F6.2), `TestFileMissingRecoversTheSwapFromTheRecord` (F6.5), `TestIntentsAreRecordedOnce` (F6.5), `TestNewBytesOnASucceededFileDropItsVerdict` (F6.2), `TestOrphanAndDeadlineRows` (F6.5), `TestPlanNormalisesALegacyBlock` (F6.2), `TestRepublishStopsAtTheWindow` (F6.4), `TestRetryClearsAVerdictForTheCurrentTag` (F6.5), `TestStandingIntentsAreRecordedInTheBlock` (F6.5), `TestThePlanNamesTheSubtitlesItDrops` (F6.2), `TestTheRecordMovesTheBlock` (F6.4), `TestTranscodePlanIsPure` (F6.2), `TestWithdrawalTakesTwoPasses` (F6.5), `TestStatusPlanIsTheArgvTheWorkerRenders` (F6.6 R)
- `app/transcode/worker` (33): `TestAFactAnswerIsRetried` (F6.5), `TestAFactIsAnsweredBeforeSuperseded` (F6.5), `TestServeAcksASupersededTask` (F6.4), `TestServeAnswersTheRecord` (F6.4), `TestServeWaitsForTheOwedRecord` (F6.4), `TestAFailedVerifyLeavesNoSidecar` (F9.2 R), `TestAMissingSidecarFailsVerification` (F9.2 R), `TestARetryAfterPlacingTheSidecarsDoesNotDuplicateThem` (F9.2 R), `TestARunThatAbortsNeverTouchesAnotherAttemptsPartFile` (F9.2 R), `TestATaskForAnotherContainerIsRetried` (F9.2 R), `TestAnEmptySidecarIsDroppedNotFailed` (F9.2 R), `TestAnFFgoTaskOnAWorkerWithoutTheEngineIsRetriable` (F9.2 R), `TestProcessReportsSourceChangedWhenTheSourceIsEditedAfterPlanning` (F9.2 R), `TestRunAbortsTheSwapAndRemovesThePartWhenBeforeSwapFails` (F9.2 R), `TestRunAfterACrashFinishesUnderAnEarlierHashOfTheProfile` (F9.2 R), `TestRunAfterACrashPostPlaceRetiresTheSourceWithoutTranscoding` (F9.2 R), `TestRunAfterACrashPostSwapRecordsTheResultWithoutTranscodingAgain` (F9.2 R), `TestRunChangesTheContainerAndRetiresTheSource` (F9.2 R), `TestRunClassifiesInputFailures` (F9.2 R), `TestRunExitsThreeBeforeRunningFFmpegWhenTheSourceChanged` (F9.2 R), `TestRunPlacesTheSidecarsAndKeepsAnExistingOne` (F9.2 R), `TestRunRefusesToOverwriteAnUnrelatedFileAtTheOutputPath` (F9.2 R), `TestRunTranscodesVerifiesAndSwapsOverTheSource` (F9.2 R), `TestRunUnderTheCRDDefaultOutputLimitSwapsANormalTranscode` (F9.2 R), `TestRunWithRecycleBinOffSwapsWithoutRecycling` (F9.2 R), `TestRunWithReplaceSourceFalseKeepsTheSource` (F9.2 R), `TestRunWithTheInProcessEngineTranscodesVerifiesAndSwaps` (F9.2 R), `TestRunWritesAnExplicitOutputPath` (F9.2 R), `TestTheAPIAdmitsReplaceSourceFalse` (F9.2 R), `TestTheEnginesFailuresAreClassified` (F9.2 R), `TestTheFFgoJobUsesTheMeasuredTier` (F9.2 R), `TestAJoinedGraftThatWillNotAlignLeavesTheTranscodeAlone` (W8.5 E), `TestATranscodeCarriesAJoinedGraftInOnePass` (W8.5 E)
- `app/transcode/worker/graft` (1): `TestTheReducedDonorKeepsEveryWantedLanguage` (W6.21 E)
- `app/transcode/worker/inprocess` (3): `TestNewRequiresItsClassesEncoders` (W8.5), `TestDoviRecordReadsEveryField` (W8.3 E), `TestTheInProcessProbeAgreesWithFFprobe` (W8.3, W8.4 E)
- `cmd/transcode` (11): `TestAGraftLoadsFFmpegThroughTheGate` (W8.5), `TestAGraftPodNeedsNATSAndItsName` (F7.2), `TestAGraftTaskAnswersInTheRecordsBucket` (F7.2), `TestASupersededGraftTaskExitsWithoutRunning` (F7.2), `TestPoolAndGraftArgsParse` (W5.12), `TestTheTerminationLogFlagIsGone` (F7.2), `TestUMASKIsAppliedBeforeAGraft` (W5.12), `TestVersionFlag` (W5.12), `TestBinaryImportsNoKubernetesClient` (W5.12, W5.14 E), `TestSIGTERMIsDrained` (W5.12 E), `TestUnreachableNATSIsRetriable` (W5.12 E)
- `internal/testmedia` (5): `TestAClipCarriesWhatItsSpecSays` (W6.18), `TestHDR10AndTenBitHEVC` (W6.18), `TestNoBinaryLinksTestmedia` (W6.18), `TestTheInspectorsReadWhatTheConvertedTestsAssert` (W6.18), `TestTruncateAndTheSameSpecWritesTheSameBytes` (W6.18)
- `pkg/ffruntime` (13): `TestABodyThatStopsInsideTheGraceIsNotAbandoned` (W7.2), `TestACancelledCallReturnsWithinTheGraceAndIsCountedUntilItReturns` (W7.2), `TestCaptureSeesEveryLineUntilReleased` (W7.2), `TestDoNeverStartsAfterItsContextEnded` (W7.2), `TestDoReturnsTheBodysResult` (W7.2), `TestErrorsReachSlogRateLimited` (W7.2), `TestFFRuntimeImportsOnlyFFgo` (W7.2), `TestLoadReportsFFmpeg9AndTheShim` (W7.3), `TestMappedFindsTheLoadedLibraries` (W7.2), `TestMaxAbandonedWedgesTheProcess` (W7.2), `TestMuteSilencesSlogButNotCaptures` (W7.2), `TestRequireNamesWhatIsMissing` (W7.3), `TestShimAPIMatchesTheFork` (W7.3)
- `pkg/ffruntime/ffmetrics` (1): `TestRegisterExportsTheAbandonedCallsGauge` (W7.3)
- `pkg/mediainfo` (4): `TestAtPathRederivesOnlyTheContainer` (W4.1), `TestErrProbeAbandonedSurvivesWrapping` (W4.1), `TestMediainfoKeepsOnlyTheModel` (W8.7), `TestToMediaInfoClampsEveryStreamString` (F2.2)
- `pkg/mediainfo/ffprobeexec` (1): `TestProbeMissingFileReturnsWrappedError` (W8.3 E)
- `pkg/mediainfo/native` (8): `TestAbandonedProbesFailHealth` (W8.3), `TestCodecNameDropsWhatFFprobeOmits` (W8.4), `TestFFprobeTime` (W8.4), `TestNativeProbeMatchesTheCorpusGoldens` (W8.4), `TestNativeProbeMatchesTheGoldens` (W8.4), `TestNativeProbesInParallel` (W8.3), `TestProbeHonoursItsContext` (W8.3), `TestValidStringMatchesFFprobe` (W8.4)
- `pkg/transcode/donoraudio` (5): `TestKeepTakesTheAnchorThenTheLanguagesOnce` (F7.1), `TestPickComparesTheBaseLanguage` (F7.1), `TestReduceReturnsAMatroskaAudioDonorUnopened` (F7.1), `TestReduceReusesAnEarlierReduction` (F7.1), `TestReducedPathIsTheStemAsMatroskaAudio` (F7.1)
- `pkg/transcode/engine` (6): `TestTheLogTailIsACaptureOfTheProcessLog` (W8.5), `TestAGraftedTrackIsInterleavedWithTheVideo` (W6.20 E), `TestASourceWhoseTimestampsGoBackwardsStillEncodes` (W6.20 E), `TestEAC3IsCopiedIntoMP4` (W6.20 E), `TestExtractAudioKeepsTheNamedTracks` (W6.20 E), `TestTheAC3EncoderOpensAtFivePointOne` (W6.20 E)
- `pkg/transcode/selfcheck` (1): `TestCheckNamesTheFirstMissingMuxer` (W8.5)
- `test/goldens` (1): `TestTheGoldensAreCompleteAndIntact` (W6.23)
- `test/nativetest` (4): `TestPinnedRefusesAHostFFmpeg` (W7.3), `TestRequiredFollowsTheEnvironment` (W7.3), `TestTheFFgoRequirementCoversFFmpeg` (W7.3), `TestVersionLineReadsLibavcodec` (W7.3)

**T3 notes.**

- W6.23 records goldens against the exec implementations, which are already deleted: the segments decoder (`pkg/segments/decode/proc.go`, gone in `08cf7fe3`), the ffprobe prober (`5b8d3b00`) and `execextract` (`55c18417`). Record each kind from a throwaway worktree at that commit's parent with the native-assets `ffmpeg`/`ffprobe`, commit only the goldens; or the owner records them. It must precede W10.10, which removes the CLIs.
- `NATIVE_ENV` for `pkg/transcode/engine`, `app/transcode/worker/inprocess`, `pkg/mediainfo/native`, `pkg/ffruntime`; GPU tiers (NVENC, QSV) skip without a device -- check whether `CLUSTARR_REQUIRE_TOOLS=1` turns those into failures on a GPU-less host, and keep a device skip a skip.
- The transcode envtests take some 460 s: `-timeout 20m` (`60m` under `-race`). `FuzzLedgerNeverOverAdmits` runs once with `-fuzztime 60s` (F7.5's gate), outside `make test`.
- `app/transcode/controller/pool/template_test.go` reads an installer file W6 deleted (ledger W6.9-W6.17): point it at the rendered native image.

### D4 -- Acquisition: downloads, import, index, subtitles

Everything that brings media in: the grab lifecycle, engines and DownloadClient controller, the downloads item stage and entry index, imports (two-phase inspect/execute, scans, lists, the recycle bin), the indexer agent, controller, clients, sessions, limits and release index, captions (`app/caption`, its loop adapter, `pkg/subtitles`), `pkg/download` and par2 (`pkg/par2child`, `test/par2test`).

**T1 -- 23 failing test packages** (first error at the inventory):

| Package | First error |
| --- | --- |
| `app/caption/controller/subtitleprofile` | app/caption/controller/subtitleprofile/profile_test.go:42:36: undefined: itemSet |
| `app/caption/status` | app/caption/status/split_test.go:149:65: undefined: k8s.ManagerCaptionWorker |
| `app/caption/worker/fetch` | app/caption/worker/fetch/pool_test.go:170:29: undefined: sidecarMode |
| `app/grab/controller/downloadclient` | app/grab/controller/downloadclient/workload_test.go:333:3: unknown field BusSingleNode in struct literal of type EngineRuntime |
| `app/grab/engine` | app/grab/engine/engine_test.go:38:12: undefined: OrphanClock |
| `app/grab/engine/torrent` | app/grab/engine/torrent/finalizer_test.go:53:7: undefined: newEnvtestClient |
| `app/grab/engine/usenet` | app/grab/engine/usenet/payload_test.go:109:35: cannot use rpc (variable of type *fakeRequester) as "pkg/events".Requester value in struct literal: *fakeRequester does not implement "pkg/events".Requester (wrong type for method ... |
| `app/grab/manager` | app/grab/manager/run_internal_test.go:40:7: undefined: DefaultOptions |
| `app/import/agent` | app/import/agent/run_test.go:31:2: no required module provides package app/import; to add it: |
| `app/import/controller/importlist` | app/import/controller/importlist/status_envtest_test.go:55:102: undefined: importliststate.Result |
| `app/import/controller/libraryscan` | app/import/controller/libraryscan/helpers_envtest_test.go:92:20: undefined: rescan.IndexMediaFileByPath |
| `app/import/mediafilespec` | app/import/mediafilespec/export_test.go:23:10: undefined: renameBeforeMove |
| `app/import/worker/fileimport` | app/import/worker/fileimport/ackdeadline_test.go:60:11: undefined: heartbeatInterval |
| `app/import/worker/importlist` | app/import/worker/importlist/sync_envtest_test.go:54:99: undefined: importliststate.Result |
| `app/import/worker/rescan` | app/import/worker/rescan/folder_envtest_test.go imports pkg/mediainfo/ffprobeexec (no non-test files since W8); then export_test.go:27 undefined clampRunes |
| `app/indexer/agent` | app/indexer/agent/rssdeps_test.go:45:21: cannot use reader (variable of interface type client.WithWatch) as *health.Writer value in argument to rssDeps |
| `app/indexer/controller/indexer` | app/indexer/controller/indexer/controller_test.go:228:4: r.ForgetClient undefined (type *Reconciler has no field or method ForgetClient) |
| `app/indexer/limits` | app/indexer/limits/ring_test.go:317:46: cannot use kv (variable of type *conflictKV) as events.KV value in argument to ReserveGrab: *conflictKV does not implement events.KV (missing method Keys) |
| `app/indexer/query` | app/indexer/query/service_test.go:63:26: cannot use &fakeStore{} (value of type *fakeStore) as relindex.Store value in struct literal: *fakeStore does not implement relindex.Store (missing method Block) |
| `app/indexer/search` | app/indexer/search/fanout_test.go:298:32: undefined: searchworker.MaxIndexerOutcomes |
| `app/indexer/status` | app/indexer/status/status_envtest_test.go:90:28: undefined: status.Patch |
| `app/indexer/worker/rss` | app/indexer/worker/rss/worker_envtest_test.go:341:21: cannot use (*fakeStore)(nil) (value of type *fakeStore) as relindex.Store value in variable declaration: *fakeStore does not implement relindex.Store (missing method Block) |
| `pkg/download/usenet` | pkg/download/usenet/client_test.go:326:88: unknown field Par2Path in struct literal of type Config (newly failing at 66e44603, W9.5) |

Download fixtures go (A3/A9: entries and engine records); `fakeStore`s gain `relindex.Store.Block` (A3.1); `conflictKV`/`countingKV` gain `Keys`/`Status` (F1.3); `heartbeatInterval` is `HeartbeatInterval`; `importliststate.Result`, `rescan.IndexMediaFileByPath`, `renameBeforeMove`, `ForgetClient`, `status.Patch` are gone (A3.8, A6.x, W4.25). `app/import/agent/run_test.go` imports the deleted `app/import` root: its tests are W5.17's deletes and moves. `pkg/download/usenet`, `pkg/par2child`, `app/grab/engine/usenet`: start after W9.8 lands.

**T2 -- 297 tests** (237 deferred, 27 rebuilt, 33 rewritten; 31 more dropped, in the catalogue only). Order inside the domain: the store and client fakes (T1); `probetest`, `test/par2test` (T2a); the pure lifecycle and importplan (A3.4, A3.8); engines on records (A3.6, A3.7), the downloads stage (A3.5); imports (A3.8, A6.1, A6.2, W4.x survivors), the F9.2 rescan rebuilds; indexer (W4.20-W4.28 survivors, A3.1, A6.3, A6.4, S4); captions (F5.1-F5.4 incl. the rebuilt ports); par2 (W9.2-W9.6) last.

- `app/caption/agent` (3): `TestRegisterSubscribesBothFetchConsumers` (W3.13), `TestTheCaptionDomainExtractsInProcess` (W8.9), `TestTheCaptionDomainRefusesToStartWithoutTheSRTEncoder` (W8.9)
- `app/caption/controller/subtitleprofile` (7): `TestAnInvalidFilterMarksTheProfileInvalid` (F5.3), `TestAnUnchangedStatusIsNotApplied` (F5.3), `TestMatchingFilesCountsTheFilesPlannedWithTheProfile` (F5.3), `TestOverlapIsTrueWhenAnotherProfileWinsAFileThisOneSelects` (F5.3), `TestSignalsWithinTheDebounceCollapseIntoOneEvent` (F5.3), `TestTheNewerOfTwoDefaultsIsInvalid` (F5.3), `TestValidateProfileFlagsAnInvalidFilter` (F5.3)
- `app/caption/controller/subtitleprovider` (1): `TestTheQuotaGaugeFollowsTheThrottleState` (F5.2)
- `app/caption/manager` (3): `TestRegisterAddsEveryCaptionController` (W3.13), `TestRegisterRequiresTheBus` (W3.13), `TestRegisterRequiresTheWake` (F5.3)
- `app/caption/subtitleplan` (47): `TestAFailedRecordWriteAfterTheApplyIsRedispatched` (F5.1), `TestAFirstPlanWantsGermanAndDispatchesIt` (F5.1), `TestAForcedNonceWaitsOutABlock` (F5.4), `TestAForcedSearchRacingAnInFlightAnswerKeepsBoth` (F5.4), `TestAForcedSearchSupersedesADispatchInFlight` (F5.4), `TestALateAnswerForAnOlderSeqIsIgnored` (F5.1), `TestANewProbeHashResetsAttemptsAndWithdraws` (F5.1), `TestAPendingProbeHoldsTheBlock` (F5.1), `TestARecordAheadOfStatusIsAdoptedNotRedispatched` (F5.1), `TestARemovedSidecarIsWantedAgain` (F5.1), `TestARenamedSidecarIsRebound` (F5.1), `TestATimedOutRequestIsWithdrawnThenClosed` (F5.1), `TestAnAnsweredRecordIsIncorporated` (F5.1), `TestAnInvalidNonceIsRecordedWithAWarning` (F5.4), `TestAnInvalidWinnerStillWins` (F5.1), `TestBlockedRowsKeepTheItemsVerbatim` (F5.1), `TestForcedSearchIsHandledOnce` (F5.4), `TestItemFromMovieCarriesIDsAndTheOriginalLanguage` (F5.1), `TestNeedsListsOnlyWhenAGateOpens` (F5.1), `TestNotWantedNeedsNoItemAndNoProbeTag` (F5.1), `TestNothingToSearchIsRecorded` (F5.4), `TestSubtitleLanguageStateMirrorsTheFetchVerdicts` (F1.5 -> F5.1), `TestTheBacklogGateDefersScheduledDispatches` (F5.1), `TestTheBacklogGateDefersScheduledNotForced` (F5.4), `TestThePlannerImportsNoIO` (F5.1), `TestUntaggedAudioUsesTheItemsOriginalLanguage` (F5.1), `TestUpgradableFollowsTheProfilesMinDeltaPoints` (F5.1), `TestAdaptiveGate` (F5.1 R), `TestBuildExistingFiltersToTheProfileAndCaps` (F5.1 R), `TestCadenceDefaultsAZeroDuration` (F5.1 R), `TestEmbeddedEntriesAreTheProvidersTheWorkerWouldTask` (F5.1 R), `TestEmbeddedPolicy` (F5.1 R), `TestExtractableStreams` (F5.1 R), `TestISO6392AudioTripsAudioExclude` (F5.1 R), `TestISO6392StreamLanguagesCountAsExisting` (F5.1 R), `TestItemFromEpisodeTakesTheSeriesIDsAsParents` (F5.1 R), `TestMinScoreFor` (F5.1 R), `TestNormalizeKeyKeepsSuffixes` (F5.1 R), `TestPlanItemsWithdrawsAnUnwantedLanguageThatNeverDownloaded` (F5.1 R), `TestPlannerProfileOverrideAndNormalisation` (F5.1 R), `TestSidecarExisting` (F5.1 R), `TestStamp` (F5.1 R), `TestUpgradeCandidate` (F5.1 R), `TestUpgradeCheckAt` (F5.1 R), `TestUpgradeMinScore` (F5.1 R), `TestUpgradeScheduled` (F5.1 R), `TestWantedDueAt` (F5.1 R)
- `app/caption/worker/fetch` (25): `TestAFileChangedSincePlannedAnswersDropped` (F5.2), `TestAForeignOrUndecodableRecordReadsAbsent` (F5.1), `TestASupersededTaskIsAckedWithoutSearching` (F5.2), `TestAVideoReplacedDuringTheSearchAnswersDropped` (F5.2), `TestAWithdrawalDuringTheSearchDropsANonDownloadAnswer` (F5.2), `TestAnInvalidFilterAnswersFailed` (F5.2), `TestAnUpgradeReplacesTheSubtitleOnDiskAndRecordsIt` (F5.2), `TestAnswerRefusesASupersededSeq` (F5.1), `TestEachLanguageHasItsOwnKey` (F5.1), `TestFetchWritesTheBestAcceptableSubtitleAndAnswers` (F5.2), `TestNothingBetterThanTheSubtitleOnDiskAnswersKept` (F5.2), `TestRequestNeverOverwritesAnUnincorporatedDownload` (F5.1), `TestTheLastDeliveryAnswersFailed` (F5.2), `TestTheProfileFencesTheTask` (F5.2), `TestThrottledProvidersAnswerThrottledWithTheirWindow` (F5.2), `TestWithdrawBeatsALateAnswer` (F5.1), `TestASecondAccountOfOneTypeBacksTheFirstsDownloadQuota` (F5.2 R), `TestAnExtractableEmbeddedTrackIsWrittenWithoutAskingARemoteProvider` (F5.2 R), `TestPoolingDownloadsTheBestCandidateOfEveryProvider` (F5.2 R), `TestProviderErrorsGoToTheSharedThrottleNeverToSubtitleProviderStatus` (F5.2 R), `TestTheSidecarTakesTheTasksFileMode` (F5.2 R), `TestChooseProfile` (F5.2 E), `TestIMDbNumber` (F5.2 E), `TestSidecarModeComesFromTheContainingRootFolder` (F5.2 E), `TestWantForResolvesTheEntryAndItsAliases` (F5.2 E)
- `app/grab/agent/torrent` (4): `TestEngineIdentity` (W5.5), `TestPodNamePrefersTheDownwardAPI` (W5.5), `TestRegisterReattachesAndAddsNoLeaderOnlyRunnable` (W3.11), `TestRegisterRequiresTheBus` (W3.11)
- `app/grab/agent/usenet` (2): `TestBuildConfigMapsTheConnectionBudget` (W9.6), `TestTheUsenetDirectClientIsReadOnly` (A7.1)
- `app/grab/controller/downloadclient` (11): `TestARecreatedTransfersBucketStartsAResync` (A3.7), `TestAScaledDownOrdinalsDurableGoesOnlyWhenNothingIsPinned` (A3.7), `TestAnUnidentifiedTransferIsRemovedAfterTheGraceWithItsBytesKept` (A3.7), `TestDiskSpaceOKReadsTheEnginesFreeBytes` (A3.7), `TestEngineCommandRunsTheAgentWithOneDomain` (W5.5), `TestEngineReadyNeedsAFreshEngineRecord` (A3.7), `TestEveryRenderedOrdinalHasItsDurable` (A3.7), `TestProxyUDPUnavailableIsAConditionAndOneEvent` (A3.7), `TestBuildDeploymentShape` (W5.5 E), `TestBuildStatefulSetShape` (W5.5 E), `TestEnginePodsGetTheRuntimeTheyNeed` (W5.5 E)
- `app/grab/engine` (7): `TestACommandAtOrBelowTheJournalSeqIsDropped` (A3.6), `TestAPreJournalTransferIsMatchedByNameAndClaimed` (A3.6), `TestAnUnclaimedTransferReadsClaimedFalseAfterTheGrace` (A3.6), `TestDownloadTimeoutReReadsBeforeAskingAgain` (Wave N N4), `TestResyncRewritesEveryRecordThenReportsTheSeq` (A3.6), `TestTheEngineAcksOnlyAfterTheRecordShowsTheSeq` (A3.6), `TestTransferRecordFilesAreClampedToTheBudget` (A3.6)
- `app/grab/engine/torrent` (2): `TestPayloadUnavailableIsReportedNotRetriedForever` (A3.6), `TestTheEngineRemovesNothingWithoutAnAbsentCommand` (A3.6)
- `app/grab/engine/usenet` (2): `TestTheEngineRepairsInAChildOfItself` (W9.6), `TestTheUnreapedChildrenGaugeRegistersOnce` (W9.6)
- `app/grab/lifecycle` (12): `TestABlocklistedEntryIsDroppedOnlyAfterConfirmationAndQuarantine` (A3.4), `TestAFailedEntryWaitsForItsRedownloadSearch` (A3.4), `TestAnEntryIsNotJudgedMissingBeforeResync` (A3.4), `TestAnOwedCommandIsRepublishedWithTheSameMsgIDInsideTheWindow` (A3.4), `TestChooseClientPrefersTheIndexersClient` (A3.4), `TestChooseOrdinalBalancesThenHashes` (A3.4), `TestEngineGoneDropsAfterTenMinutes` (A3.4), `TestEveryRowOfTheStateMachine` (A3.4), `TestIntentsParseAndRecordEveryNonce` (A3.4), `TestNothingRemovesOnAbsence` (A3.4), `TestScopeByReason` (A3.4), `TestTheClaimEvidenceOrder` (A3.4)
- `app/grab/manager` (2): `TestEngineRuntimeCarriesTheControllerOptions` (W3.11 E), `TestTorrentEngineDirs` (W3.11 E)
- `app/import/agent` (15): `TestRegisterWiresTheImportDomain` (W3.9), `TestTheImportAgentAnswersBothProbeLanes` (W4.7), `TestTheImportDomainProbesInProcess` (W8.6), `TestTheImportDomainRefusesToStartWithoutAProber` (W8.6), `TestTheProbeWorkerIsNamedForItsPod` (W4.7), `TestAllRoleLeaderElectsWhenAsked` (W5.17 E), `TestImportListsGetTheirBaseURLs` (W3.9 E), `TestManagerOptionsNeverCacheSecretsOrConfigMaps` (W5.17 E), `TestRunRejectsAnUnknownRole` (W5.17 E), `TestScanWorkerGetsTheAPIReader` (W3.9 E), `TestTheControllerRoleCachesMediaFilesWithoutMediaInfo` (W5.17 E), `TestTheListWorkerGetsItsBaseURLs` (W3.9 E), `TestValidateRejectsAnEmptyDataPath` (W5.17 E), `TestValidateRequiresTheBus` (W5.17 E), `TestWorkerRoleDoesNotLeaderElect` (W5.17 E)
- `app/import/controller/importlist` (8): `TestADeviceCodeSaveNeverRollsBackARefreshedToken` (W4.83), `TestAListSyncRacingTheClassifierKeepsTheAnimeProfile` (W4.85), `TestAPartialSnapshotIsNotDiffed` (A6.2), `TestARefusedRefreshedTokenStartsADeviceFlow` (A6.2), `TestATranscodePartWithoutABinIsRemovedOutright` (A6.2), `TestOneRefresherRotatesTheTraktToken` (A6.2), `TestRemoveAndDeleteRecyclesThenDeletesWithPreconditions` (A6.2), `TestTheDiffWritesOnlyTheItemsTheListAddedItself` (A6.2)
- `app/import/controller/recyclesweep` (4): `TestPublishOnceQueuesOneSweepPerSlot` (W4.61), `TestSlotsAreSixHourUTCWindows` (W4.61), `TestStartPublishesAtOnceAndRetriesAFailure` (W4.61), `TestTheTaskNamesItsSlot` (W4.61)
- `app/import/importplan` (3): `TestAHeldImportExpiresAfterTwentyFourHoursAndIsNeverBlocklisted` (A3.8), `TestATransientImportRetriesOnStatusNotStreamState` (A3.8), `TestImportplanCarriesEveryRuleOfFileimport` (A3.8)
- `app/import/manager` (3): `TestRegisterRequiresTheBus` (W3.9), `TestRegisterSubscribesNothing` (W3.9), `TestTheImportListControllerGetsTheTraktBaseURL` (W3.9)
- `app/import/manager/scanapply` (5): `TestAMissingFileIsDeletedAfterTenMinutesWithPreconditions` (A6.1), `TestAStaleBasisIsReobservedNotApplied` (A6.1), `TestAnExistingItemWithAnotherProviderIDIsNeverOverwritten` (A6.1), `TestCreatesAndDeletesArePaced` (A6.1), `TestTheScannerNeverGuesses` (A6.1)
- `app/import/mediafilespec` (1): `TestReassertFrozenKeepsTheInfoHash` (A1.3)
- `app/import/worker/fileimport` (13): `TestADonorIsReducedBesideTheLibraryAtImport` (F7.1), `TestADonorLackingALanguageAtReductionIsTheReleasesFault` (F7.1), `TestADonorThatCannotBeReducedIsRetriedThenBlocked` (F7.1), `TestANewDonorRemovesTheItemsRecordedOne` (F7.1), `TestAnAudioProbeHeartbeatsAndIsBounded` (W4.86), `TestAnAudioProbeIsBounded` (W4.9), `TestFieldIndexesDeclareTheMediaFileTargetIndex` (W3.4), `TestFrozenReleaseFitsTheMediaFileBounds` (F2.2), `TestInspectAndExecuteWriteNoKubernetesObject` (A3.8), `TestRecycleFilesRefusesAPathOutsideTheRootFolder` (A6.2), `TestRecycleSweeperHandlesTheSweepTask` (W4.61), `TestTheAudioProbeFitsTheFileConsumersAckDeadline` (W4.9), `TestTheExecuteTaskRefusesAStaleBasis` (A3.8)
- `app/import/worker/importlist` (1): `TestTheListAgentWritesOnlyASnapshot` (A6.2)
- `app/import/worker/probe` (11): `TestADrainingWorkerLeavesTheTaskForAnotherReplica` (W4.6), `TestAFileThatChangesWhileProbedIsTransient` (W4.6), `TestAPathOutsideTheDataMountIsRefused` (W4.6), `TestASupersededTaskIsAckedWithoutProbing` (W4.6), `TestATimedOutOrIncompleteProbeIsTransient` (W4.6), `TestAnAbandonedProbeIsNotTransient` (W4.6), `TestAnUndecodableTaskIsDeadLettered` (W4.6), `TestTheProbeFitsTheProbeConsumersAckDeadline` (W4.6), `TestTheProbeWorkerAnswersARealFile` (W4.6), `TestTheProbeWorkerAnswersOnlyTheCurrentRequest` (W4.6), `TestTheProbeWorkerLinksNoProberAndNoClient` (W4.6)
- `app/import/worker/rescan` (5): `TestFieldIndexesDeclareTheMediaFilePathIndex` (W3.4), `TestTheAudioProbeFitsTheScanConsumersAckDeadline` (W4.9), `TestTheRescanPublishesObservationsAndWritesNothing` (A6.1), `TestARescanRemovesAnAbandonedTranscodeAttemptsPartFile` (F9.2 R), `TestHandleLeavesTranscodeOutputsToCatalog` (F9.2 R)
- `app/indexer/agent` (17): `TestAgentClientCacheAppliesRateLimit` (W4.25), `TestAgentForgetsClientOnSessionChange` (W4.25), `TestAgentGrabAppliesTheIndexersRequestDelay` (W4.25), `TestEveryComponentIsRegistered` (W5.17), `TestIndexAgentLinksNoManagerCode` (W4.26), `TestIndexSweeperRunsOnEveryReplica` (W4.28), `TestRegisterRequiresTheBus` (W3.10), `TestRegisterServesTheIndexDomain` (W3.10), `TestTheFacadeFailsClosedUntilTheManagerCreatesItsKey` (A6.4), `TestTheReleaseIndexIsSwept` (W3.10), `TestAnExplicitZeroRequestDelayStaysUnpaced` (W4.25 E), `TestIndexSweeperIsLeaderElected` (W3.10, W4.28, W5.17 E), `TestIndexSweeperPrunesOnceOnStartBeforeTicking` (W3.10, W5.17 E), `TestIndexerSpecDefaultsMatchTheCRD` (W3.10, W4.22, W4.25 E), `TestLeaderElectionIsGatedOnIndexDSN` (W5.17 E), `TestReadinessPassesOnANonLeaderReplica` (W3.10 E), `TestTheIndexReadinessCheckFailsOnAClosedStore` (W3.10 E)
- `app/indexer/blocklist` (3): `TestAQueryAnswerMarksABlockedRelease` (A3.1), `TestASearchAnswerMarksABlockedRelease` (A3.1), `TestTheBlocklistVerbPersistsWhatItIsAsked` (A3.1)
- `app/indexer/clientcache` (6): `TestANewerGenerationAppliesAfterAnOlderOne` (W4.22), `TestAStaleIndexerDoesNotRevertANewerRequestDelay` (W4.22), `TestClientCacheAppliesTheIndexersRequestDelay` (W4.22), `TestForgetKeepsTheAppliedGeneration` (W4.23), `TestPruneDropsExpiredEntriesAndDeletedIndexers` (W4.23), `TestWatchSessionsForgetsTheIndexerWhoseSessionChanged` (W4.23)
- `app/indexer/clients` (5): `TestAFailedReLoginKeepsASessionTheManagerSavedMeanwhile` (W4.21), `TestDefaultLimiterConfigPacesAtTheCRDDefault` (W4.22), `TestSessionDropDoesNotWipeANewerSave` (W4.21, A6.4), `TestSessionDropInKV` (W4.21), `TestTheAgentWritesNoSecret` (A6.4)
- `app/indexer/controller/indexer` (10): `TestADroppedSessionLogsInAgainAtOnce` (W4.24), `TestIndexersForSession` (W4.24), `TestSessionsSourceEnqueuesOnlyADroppedSession` (W4.24), `TestTheControllerMirrorsTheKVSessionIntoTheSecret` (A6.4), `TestTheControllerSchedulesTheNextPollAfterAnOutcome` (A6.3), `TestTheIndexerReconcilerHoldsNoClientCacheHook` (W4.25), `TestTheLadderRunsInTheController` (A6.3), `TestAFailedReLoginFailsTheSearchAndDropsTheSession` (W4.21 E), `TestDeletionCallsForgetClient` (W10.8 E), `TestDeletionPrunesTheCapsMemoButNotTheSharedBucket` (W4.25 E)
- `app/indexer/health` (1): `TestLocalSuppressionLastsAtMostThirtySeconds` (A6.3)
- `app/indexer/manager` (4): `TestBundleLoaderIsLeaderOnly` (W4.27), `TestEveryComponentIsRegistered` (W5.17), `TestIndexerManagerLinksNoAgentState` (W4.26), `TestRegisterAddsNoConsumerAndOneLeaderOnlyLoader` (W3.10)
- `app/indexer/search` (3): `TestAnEpisodeQueryIsUnchanged` (S4), `TestBuildQueryForASeason` (S4), `TestTheIndexAgentWritesOutcomesNotStatus` (A6.3)
- `app/indexer/worker/rss` (2): `TestNoPollChainsItself` (A6.3), `TestTheFirehoseCarriesEveryScopeThatBlocksARelease` (A3.1)
- `app/remediation/dlindex` (1): `TestDlindexFindsTheOwnerByEntryIDOrUID` (A3.3)
- `app/remediation/downloads` (9): `TestARefusedImportOverATranscodedFileStopsReadingDownloading` (A3.5), `TestAnEpisodeReadsItsSeriesEntries` (A3.5), `TestAnUnclaimedTransferIsResumedUnderItsOwner` (A3.5), `TestMaterialiseDeletesReplacedFilesWithAUIDPrecondition` (A3.8), `TestOwnerGoneNeedsTheAPIReadersNotFound` (A3.5), `TestTheDownloadsStageRunsTheTableOnAnOwnerAlreadyInItsSteadyState` (A3.5), `TestTheEntryReadsImportedOnlyWhenEveryPlacedFileHasItsMediaFile` (A3.8), `TestTheFinalizerHoldsDeletionUntilEveryTransferIsRemoved` (A3.5), `TestTheImportIntentReissuesTheInspect` (A3.8)
- `app/remediation/subtitles` (7): `TestAProfileEditThatWantsNothingNewWritesNoMediaFile` (F5.1), `TestARepublishOfAnEvictedTaskTakesAFreshMsgID` (F5.1), `TestAnnotatingTheMediaFileForcesASearch` (F5.4), `TestCopyTakesOnlyTheSubtitlesFields` (F5.1), `TestEffectsWithdrawBeforeTheyRequest` (F5.1), `TestSubtitlesSurviveABlockedPass` (F5.1), `TestTheLoopIncorporatesAnAnsweredRecord` (F5.1)
- `pkg/download/torrent` (1): `TestImportedSurvivesARestart` (A3.6)
- `pkg/download/usenet` (12): `TestAnEngineStoppedDuringRepairResumesTheJob` (W9.5), `TestLibraryRepairerCountsTheBlocksItNeeds` (W9.4), `TestLibraryRepairerRepairsASetWhoseFilesCarryOtherNames` (W10.7), `TestLibraryRepairerReportsAMissingLibrary` (W9.4), `TestLibraryRepairerResolvesTargetsAgainstTheJobDir` (W9.4), `TestPar2ExtrasAreAbsoluteAndSkipEmptyFiles` (W9.4), `TestPar2VerdictMapsEveryOutcome` (W9.4), `TestTheUsenetClientExecsNothing` (W9.5), `TestClientRepairsAReleaseWithAMissingArticle` (W9.5 E), `TestPar2RunnerRepairsASetWhoseFilesCarryOtherNames` (W10.7 E), `TestPar2RunnerReportsAMissingBinary` (W9.5 E), `TestTailWriterBoundsASubprocessThatTalksTooMuch` (W9.5 E)
- `pkg/par2child` (14): `TestChildRunsTheLibrary` (W9.3), `TestExecCancelsTheChildCooperatively` (W9.3), `TestExecKillsAChildThatIgnoresCancel` (W9.3), `TestExecPassesTheChildOnlyItsEnvironment` (W9.3), `TestExecReportsAChildThatDiedWithoutAResult` (W9.3), `TestExecReportsAMissingBinary` (W9.3), `TestExecReturnsTheChildsResult` (W9.3), `TestExecSendsNamesByteForByte` (W9.3), `TestInProcessCancelReturnsPromptly` (W9.3), `TestInProcessResolvesAgainstDirNotCwd` (W9.3), `TestInProcessVerdicts` (W9.3), `TestResultErrorWrapsTheLibrarySentinel` (W9.3), `TestTailBufferBoundsAChildThatTalksTooMuch` (W9.3), `TestTheOnlyExecIsTheRepairChild` (W9.3)
- `pkg/relindex` (1): `TestTheMigrationFromVersionOneAddsTheBlocklist` (A3.1)
- `pkg/subtitles/providers/embedded/native` (3): `TestExtractCapsItsOutput` (W8.8), `TestExtractConvertsEveryTextCodecToSRT` (W8.8), `TestExtractRefusesAStreamThatIsNotText` (W8.8)
- `test/par2test` (1): `TestCreateSetMakesAnIndexAndVolumes` (W9.2)

**T3 notes.**

- `CLUSTARR_PG_ASSETS` for `pkg/relindex`'s Postgres store contract; `PAR2GO_REQUIRE=1` with `libpar2shim` for `pkg/par2child` and the usenet repair tests (real par2go: `TestLibraryRepairerRepairsASetWhoseFilesCarryOtherNames`).
- `CLUSTARR_TEST_MEDIA_ROOT` writable for the import scan suites; the anacrolix/utp proxy test skips under `-race` (known).
- `app/grab/controller/downloadclient/workload_security_test.go` reads an installer file W6 deleted (ledger W6.9-W6.17).
- W9 wrote no test through `66e44603`; re-run the T2 check over D4 once W9.8 lands -- W9's own agent may write some.

### D5 -- Processes, CLI, UI and autoscaling

The processes: `internal/cli` (manager, agent, ui, ctrlflags), `cmd/manager`, `cmd/agent`, `cmd/ui`, the ui (pages, projection, actions, Plex provider, art), `pkg/pipeline`, and autoscaling (the HPA reconciler and the External Metrics server).

**T1 -- 6 failing test packages** (first error at the inventory):

| Package | First error |
| --- | --- |
| `cmd/agent` | cmd/agent/index_flags_test.go:28:2: no required module provides package app/caption; to add it: |
| `cmd/manager` | cmd/manager/cli_test.go:34:2: no required module provides package app/caption; to add it: |
| `cmd/ui` | cmd/ui/bus_test.go:35:2: no required module provides package app/caption; to add it: |
| `pkg/pipeline` | pkg/pipeline/project_test.go:130:29: undefined: downloadv1.Download |
| `ui` | ui/art_test.go:54:3: cannot use map[string]string{…} (value of type map[string]string) as events.ObjectMeta value in argument to store.Put |
| `ui/projection` | ui/projection/projection_test.go:123:68: undefined: downloadv1.Download |

`cmd/manager`, `cmd/agent`, `cmd/ui` import the deleted `app/caption` root (W5.17): the manager's caption half is `app/caption/manager`, the agent's `app/caption/agent`. `ui` and `ui/projection` still build Downloads (A3.9: entries) and `map[string]string` object metadata (W4.101). `cmd/agent` waits for W9.8.

**T2 -- 144 tests** (135 deferred, 0 rebuilt, 9 rewritten; 6 more dropped, in the catalogue only). Order inside the domain: `internal/cli` option and flag tests (W5.2, W5.7, W5.9, W5.11); `cmd/*` wiring and start envtests (W5.8, W5.10) after D6's `starttest` lands in T2a; ui and projection (W4.108-W4.110, A3.9, F8.7, S10); autoscale (W4.64-W4.74, W4.99, A2.2).

- `app/autoscale` (2): `TestAutoscaleLinksNoAPIServerLibraries` (W4.74), `TestRegisterWithAutoscaleOffDeletesTheManagedHPAs` (W4.74)
- `app/autoscale/controller` (6): `TestDeleteManagedHPAs` (W4.66), `TestMatchingNodes` (W4.64), `TestMatchingNodesReportsAnUnparseableAffinity` (W4.64), `TestReconcilerBuildsTheHPA` (W4.66), `TestRenderHPA` (W4.65), `TestRenderHPAGolden` (W4.65)
- `app/autoscale/extmetrics` (29): `TestCAInjectorOwnsOnlyCABundle` (W4.71), `TestCANeedsRenewal` (W4.67), `TestCertManagerCreatesTheSecretOnceAndRenews` (W4.71), `TestCertManagerReplacesAnUnusableCA` (W4.71), `TestDiscovery` (W4.69), `TestErrors` (W4.69), `TestFrontProxyAuth` (W4.68), `TestIssueServingNamesTheServiceFourWaysForOneYear` (W4.67), `TestListGolden` (W4.69), `TestNamespaceGuardDeletesTheAPIServiceOnlyWhileItsNamespaceTerminates` (W4.73), `TestNewCAIsAP256CAForTenYears` (W4.67), `TestNewSecretDataHoldsFourKeys` (W4.67), `TestNewServerRefusesAnIncompleteConfig` (W4.70), `TestParseAuthConfig` (W4.68), `TestQueueGaugeAndTheAPIShareOneRead` (W4.99), `TestQueueGaugeExportsDispatchWaitingAndUnattended` (A2.2), `TestQueueGaugeExportsEachCounter` (W4.99), `TestQueueGaugeSetsStreamFill` (W4.96), `TestQueueGaugeSetsTheLagOfEveryWorkConsumer` (W4.72), `TestRenewalReason` (W4.67), `TestSelection` (W4.69), `TestServableSeriesRefusesAnUndeclaredConsumer` (W4.69), `TestStateCacheExpires` (W4.99), `TestStateCacheMakesOneReadPerSeriesUnderABurst` (W4.99), `TestStatesAreCachedForFiveSeconds` (W4.69), `TestTheServerAnswersBeforeTheCachesSync` (W4.70), `TestTheServerAuthenticatesTheFrontProxy` (W4.70), `TestTheServerRefusesHandshakesUntilItsMaterialLoads` (W4.70), `TestTheServerReloadsTheFrontProxyCA` (W4.70)
- `cmd/agent` (23): `TestASelfCheckReportsAMissingDecoder` (W8.10), `TestAgentManagersNeverElect` (W5.9), `TestDomainFlagsNeedTheirDomain` (W5.9), `TestDomainsMatchAgentdomain` (W5.9), `TestEngineArgvParses` (W5.9), `TestEngineDomainsNeedAnIdentity` (W5.9), `TestEveryDomainIsAccepted` (W5.9), `TestEveryDomainStartsServesProbesAndStopsOnSignal` (W5.10), `TestFacadeSettings` (W5.9), `TestImportDomainOptions` (W5.9), `TestImportDomainRejectsAnEmptyDataDir` (W5.9), `TestIndexDSNEnvDefault` (W5.9), `TestIndexDSNFlagReachesTheIndexDomain` (W5.9), `TestIndexDomainOptions` (W5.9), `TestInstallerArgsParseAsTheirDomain` (W6.11), `TestRetiredFlagsAreGone` (W5.9), `TestSelfCheckNeedsNoDomain` (W5.9), `TestThePar2ChildIsDispatchedBeforeCobra` (W9.6), `TestTheRendererRunsInTheCatalogDomain` (W5.9), `TestUnknownDomainIsRejected` (W5.9), `TestDefaultFacadeBindAddressMatchesTheServicePort` (W5.9 E), `TestDefaultIndexPathMatchesTheManifest` (W5.9 E), `TestIndexDSNEmptyKeepsSQLite` (W5.9 E)
- `cmd/manager` (20): `TestAutoscaleNeedsTheExternalMetricsAPI` (W5.7), `TestCardigannSettingsComeFromTheEnvironment` (W5.7), `TestDataClaimAndEngineServiceAccountComeFromTheEnvironment` (W5.7), `TestGPUNodeLabelsMustBeQualifiedNames` (W5.7), `TestGraftConcurrency` (W5.7), `TestInstallerArgsParseAsTheManager` (F9.1, W6.11), `TestIntelRenderGroups` (W5.7), `TestManagerCarriesTheFoldFlags` (W6.11), `TestManagerFailsFastOnBadOptions` (W5.8), `TestManagerOptions` (W5.7), `TestManagerRejectsAnEmptyDataDir` (W5.7), `TestManagerRequiresTheNativeImage` (W5.7), `TestManagerStartsServesProbesAndStopsOnSignal` (W5.8), `TestManagerValidatesTheRemediationFlags` (F3.4), `TestRejectsABadSlotFlag` (W5.7), `TestRetiredFlagsAreGone` (W5.7), `TestTraktBaseURLReachesTheImportListDeviceFlow` (W5.7), `TestVersionFlag` (W5.7), `TestServiceFailsFastOnBadOptions` (W5.8 E), `TestVersionCommandTakesNoArguments` (W5.2 E)
- `cmd/ui` (10): `TestEveryProjectionStreamIsWiredIntoTheUICommand` (W5.11), `TestExternalURLReachesTheUI` (W5.11), `TestUICommandClosesItsBusOnShutdown` (W5.11), `TestUIHandsTheChosenAuthModeToRunUI` (W5.11), `TestUIPassesPlexGUIDs` (W5.11), `TestUIReadsTheArtSigningKey` (W5.11), `TestUIStartsAndServes` (W5.11), `TestUIWiresEveryUIOption` (A3.9), `TestUIWiresTheArtCacheFlag` (W4.110), `TestBothUICommandsWireEveryUIOption` (A3.9 E)
- `internal/cli` (6): `TestCLIStaysLight` (W5.2), `TestEnvBoolOr` (W5.2), `TestEnvOr` (W5.2), `TestRootBindsTheObservabilityFlagsOnce` (W5.2), `TestVersionCommandPrintsTheBinaryAndToolchain` (W5.2), `TestVersionFlagHasNoShorthand` (W5.2 E)
- `internal/cli/agent` (10): `TestAgentCacheNeverHoldsSecretsOrConfigMaps` (W5.9), `TestAgentClientIsReadOnly` (A7.1), `TestAgentsRegisterNoLeaderOnlyRunnable` (W5.9), `TestDomainBusAppliesDrainAndSlotOverrides` (W5.9), `TestDrainDefaultsToTheLongestAckWait` (W5.9), `TestEngineClientSplitsAtTheLastHyphen` (W5.9), `TestEveryAgentDomainWritesPresence` (A2.1), `TestPieceCompletionMatchesTheBuild` (W5.9), `TestTheAgentIsTheOnlyIndexRegistrar` (W5.17), `TestTheBusLivenessCheckIsRegistered` (W4.98)
- `internal/cli/ctrlflags` (5): `TestBindAgentHasNoElectionOrTopologyFlags` (W5.2), `TestBindManagerBindsElevenFlagsAndElectsByDefault` (W5.2), `TestNATSURLAndNamespaceComeFromTheEnvironment` (W5.2), `TestParseGIDs` (W5.2), `TestRegisterMetricsIsIdempotent` (W5.2)
- `internal/cli/manager` (8): `TestAGraftedOriginalKeepsItsSpecWhenTheGraftBlockGoes` (F7.2), `TestALostGraftJobIsWithdrawnAndFails` (F7.2), `TestAStandaloneGraftIsDispatchedAndItsAnswerIncorporated` (F7.2), `TestIntakeConsumersRunOnlyOnTheLeader` (A2.3), `TestManagerCacheOptions` (W5.7), `TestManagerFieldIndexes` (W4.115, W5.7, F8.4, F9.1, A3.2, A3.3), `TestRegisterWiresAutoscaleByTheFlag` (W5.7), `TestTheBusLivenessCheckIsRegistered` (W4.98)
- `internal/cli/ui` (1): `TestBuildBusLogsAnUnconnectedBus` (W5.11)
- `pkg/pipeline` (3): `TestCompleteNeedsATerminalTranscodeNotAJob` (F8.7, F8.6-F9.4), `TestPipelineProjectsEntries` (A3.9), `TestProjectMapsEveryDownloadPhase` (A9.1 E)
- `ui` (11): `TestArt304MakesNoStoreCalls` (W4.108), `TestArtCacheEvictsByBytes` (W4.108), `TestArtEventOnlyForServedDigestChanges` (W4.109), `TestArtEventsStreamOnlyTheAskedKeys` (W4.109), `TestArtMissWithUnsyncedIndexAsksTheStore` (W4.108), `TestArtRetriesAnObjectOverwrittenMidRead` (W4.108), `TestArtServesFromTheDigestCache` (W4.108), `TestArtSkipsOverlayForKindsWithoutOverlays` (W4.108), `TestLibraryStreamCarriesArtEventsForItsPage` (W4.109), `TestMoviePageListsTheFilesSubtitlesAndItsTranscode` (F8.7), `TestUINeverWrites` (A3.9, S10, W4.108, F0.3 E)
- `ui/actions` (7): `TestDownloadActionsPatchOnlyTheOwnersAnnotations` (A3.9), `TestSearchSeasonAndInteractiveSeasonSearchCreateTheRightSpec` (S10), `TestThePanelCollapsesOtherTitles` (S10), `TestTheSeasonBadgeReadsTheSeriesState` (S10), `TestTheSeasonButtonsPostToTheirRoutes` (S10), `TestTheSeasonPanelDefaultsToPacks` (S10), `TestTheSeasonPanelOffersAllWhenNoPackExists` (S10)
- `ui/projection` (3): `TestRelatedFindsTheItemsFileBySpecMediaRef` (F8.7), `TestTheDownloadsPageRendersEntriesAndProgress` (A3.9), `TestTheProjectionBuildsOneRowPerEntry` (A3.9)

**T3 notes.**

- `cmd/manager` and `cmd/agent` start envtests run each identity under its own generated role (`test/starttest`, D6).
- `ui`: `make templ` and `make css` output is committed and must not drift; the ui RBAC guards live in `test/guards` (D6) -- the hunk the main merge moved into `test/guards/ui_rbac_test.go` goes back to the ui's own guard (ledger).
- `cmd/agent/index_defaults_test.go` reads an installer file W6 deleted (ledger W6.9-W6.17).

### D6 -- Guards, installers, API schemas, system and e2e

Every cross-domain guard (`test/guards`), the system and start tests (`test/system`, `test/starttest`, `test/installtest`), e2e (`test/e2e`, written and vetted only), `test/deadcode`, `test/fixtures` (W10.11's `mkclips`), the CRD and CEL checks (`pkg/crdcheck`), the API types' own tests (`api/**`), `config/crd`, `hack/**`, and the non-package files (Makefile, images, CI, charts, CLAUDE.md's † marks).

**T1 -- 4 failing test packages** (first error at the inventory):

| Package | First error |
| --- | --- |
| `test/e2e` | (go vet -tags e2e) test/e2e/transcode_test.go:133:2 imports the removed app/transcode/controller/transcodejob |
| `test/guards` | test/guards/autoscale_test.go:32:2: no required module provides package app/caption; to add it: |
| `test/starttest` | test/starttest/identity_test.go:102:20: undefined: rbacRoles |
| `test/system` | test/system/nonvideo_envtest_test.go:314:2: undefined: waitForLong |

`test/guards` imports the deleted `app/caption` root; `test/starttest`'s own tests miss `rbacRoles`/`readRole` (W5.4's helpers, not built); `test/system` misses `waitForLong`; `test/e2e` (tag `e2e`) imports the deleted `transcodejob`. Make `test/starttest` and `test/installtest` compile first: D5 depends on them.

**T2 -- 195 tests** (171 deferred, 0 rebuilt, 24 rewritten; 18 more dropped, in the catalogue only). Order inside the domain: `test/starttest` and `test/installtest` (T2a); `pkg/crdcheck` and `api/**` (F2.1, F2.2, A1.2, F9.2, S1) in T2b; `test/system`, `test/e2e` (F8.9, W6.15) in T2b; `test/guards` in T2c, after every other domain merged: deps and exec guards (W5.14, W10.1, W10.9), records and fold guards (F7.4, F9.2), RBAC and installers (W6.9-W6.16, A7.2), brand gate (U7), object stores (N8), consumer homes (W6.2); W10.10 last of the batch.

- `api/catalog/v1alpha1` (9): `TestAPhaselessTranscodeBlockSendsNoPhase` (F2.1), `TestDispatchInFlight` (F2.1), `TestDownloadEntryBoundsMatchTheCRD` (A1.2), `TestHandledNonce` (F2.1), `TestMediaFileFoldNames` (F2.1), `TestMediaFileProbeCurrent` (W4.10), `TestPathBesideJoinsTheNameToTheMediaFilesFolder` (F9.2), `TestPathBesideRefusesANameThatLeavesTheFolder` (F9.2), `TestValidNonce` (F5.4)
- `api/common/v1alpha1` (2): `TestPayloadUnavailableIsAReleaseFault` (A1.1), `TestReleaseAndLocalFaultsArePartitioned` (A1.1)
- `config/crd/bases` (1): `TestKustomizationListsEveryGeneratedCRD` (F8.9)
- `pkg/crdcheck` (20): `TestAnItemsAudioDonorRoundTripsAndItsRejectedReleasesAreCapped` (F2.3), `TestAudioStateBoundsMatchTheCRD` (F2.3), `TestDownloadSourceAllowsExactlyOne` (A9.1), `TestEveryItemKindSelectsByDownloadPhase` (A1.2), `TestGraftSegmentsAreCappedAtSixteen` (F9.2), `TestItemAtEveryCapFitsTheBudget` (A1.2), `TestMediaFilePrintColumnsAndSelectableFields` (F2.1), `TestMediaFileStatusIsReleaseN1Shaped` (F9.2), `TestMediaFileStatusPhasesAreSelectable` (F2.1), `TestMediaFileStringsAreBounded` (F2.2), `TestProbeAndImportBoundsMatchTheCRD` (F2.2), `TestSearchSeasonRules` (S1), `TestTheReleaseN1MediaFileCRDTakesOverReleaseNsStatus` (F9.2), `TestTheSchemaAcceptsThePreviousReleasesMediaFileApply` (F2.1, A1.3), `TestTranscodeClassIsConcreteHardware` (F2.1), `TestTranscodeHardwareMirrorsTheProfileEnum` (F2.1), `TestEveryStatusListIsCapped` (A1.2 E), `TestMaxTranscodeProfileLengthMatchesTheCRD` (F9.2 E), `TestMediaFileAtEveryCapFitsTheBudget` (F2.2, A1.3 E), `TestNoCRDDefaultIsUnreachableFromGo` (A1.2, S1 E)
- `test/e2e` (12): `TestABlockedReleaseShowsBlockedInASearch` (A3.1, A8.4), `TestAGraftRidesAFilesTranscode` (F8.9), `TestAnUnclaimedTransferIsResumedOrRemoved` (A8.4), `TestCaptionAgentScalesFromZero` (W6.15), `TestEngineRestartKeepsSeeding` (A3.6, A8.4), `TestForcedSubtitleSearchAnnotation` (F8.9), `TestSubtitlesSidecarPipelineAndLanguageRemoval` (F8.9), `TestTranscodeMediaFileThroughItsStatus` (F8.9), `TestClustarrAllSinglePodPassesScenarioOne` (W5.17 E), `TestObservabilityMetricsNonZero` (W10.8 E), `TestSubtitleRequestSidecarPipelineAndLanguageRemoval` (F8.9, W6.15 E), `TestTranscodeDolbyVisionSkipped` (F8.9 E)
- `test/fixtures/mkclips` (1): `TestTheE2EClipsAreTheOldRecipes` (W10.11)
- `test/guards` (134): `TestAdmissionWritesNoMediaFileStatus` (F6.3), `TestAgentCaptionRoleHasNoWriteVerbs` (F5.2), `TestAgentKeepsTheCgoPieceCompletion` (W5.14), `TestAgentLinksNoManagerRegistration` (W5.14, A7.1), `TestAgentLinksNoPlanner` (A7.1), `TestAgentProbesAndExtractsInProcess` (W8.10), `TestAgentRepairsWithPar2go` (W9.6), `TestAgentsCallNoKubernetesWriter` (A7.1), `TestAgentsPublishNoTask` (A7.1), `TestArtworkWritersAreTheTwoVariantOwners` (W4.112), `TestAutoscaledDeploymentsAreDeclared` (W6.11), `TestCardigannValuesReachTheManager` (W6.11), `TestConfigRBACHoldsOnlyKnownRoleFiles` (W6.9), `TestConsumerSlotsNameOnlyTheDomainsConsumers` (W6.13), `TestContextsExportsPinnedCleanTrees` (W6.3), `TestCredentialLessWorkloadsHoldNoToken` (W6.11), `TestDataMountsMatchTheTopology` (W6.11), `TestDevRunStartsEveryProcess` (W6.8), `TestEachRoleCoversTheMarkedPackagesItsHomeLinks` (W6.9), `TestEnginesRunAsAnAccountTheInstallerBinds` (W5.16), `TestEveryAgentDeploymentRunsOneDomain` (W4.115, W6.11), `TestEveryBusConnectionCarriesTraceHooks` (W5.15), `TestEveryChartClaimOutlivesTheRelease` (W6.14), `TestEveryClustarrContainerCarriesNATSURL` (W6.11), `TestEveryClustarrDeploymentCarriesNATSURL` (W5.16), `TestEveryConsumerHasExactlyOneHome` (W6.2, A2.5, A4.4), `TestEveryContainerNamesItsBinary` (W6.11), `TestEveryCoreSubscriptionHasPendingLimits` (ADR-0020), `TestEveryFFgoGateHonoursTheRequirement` (W10.9), `TestEveryFieldManagerHasItsHome` (W4.115, W5.19, A7.2), `TestEveryGoBuildingDockerfileCarriesTheReplaceStages` (W6.4), `TestEveryManagerGetsTheBaseContext` (W5.15), `TestEveryNativeImageCheckRunsTheMarkersSelfCheck` (W7.10), `TestEveryNativeImageCheckRunsThePar2Child` (W9.7), `TestFoldedAgentsWriteNoPerFileWork` (W6.9), `TestGOMEMLIMITIsItsShareOfTheLimit` (W6.11), `TestGrabEngineRoleIsReadOnly` (A3.6), `TestGracePeriodsCoverAckWait` (W6.11), `TestHeartbeatsFitTheirDeadline` (W4.95), `TestInstallersRenderTheTopology` (W4.115, W6.11), `TestIntelRenderGroupsReachTheManager` (W5.16, W6.11), `TestLeaderOnlyRunnablesLiveInTheManager` (W3.14, W5.15), `TestLifecycleIsPure` (A3.4), `TestMakeBuildBuildsEachBinaryAsItsImageDoes` (W6.8), `TestManagerLinksMetadataClientsOnlyToProbeThem` (W5.14), `TestManagerLinksNoAgentCode` (W5.14, F8.4, A7.1), `TestManagerNeverLinksADynamicLoader` (W5.14), `TestManagerStampsAClaimTheInstallerCreates` (W5.16), `TestMarkersAndTranscodeLinkNoControllerRuntime` (Wave N N1), `TestMarkersHasNoCredentials` (W6.11), `TestMarkersImportsNoKubernetesClient` (W5.14, W7.9), `TestNativeAssetsReadsItsPinsFromTheNativeDockerfile` (W6.7), `TestNativeImageIsWhatTheManagerStampsOntoPoolsAndEngines` (W6.11), `TestNoAgentRoleGrantsAWriteVerb` (A3.6, A7.2), `TestNoAgentRoleGrantsMediaFileStatus` (W6.9), `TestNoBrandNameOutsideTheRecords` (U7), `TestNoCodeNamesTheFoldedKinds` (F9.1 -> F9.2), `TestNoFoldedKindIsServed` (F9.2), `TestNoGoFileRunsFFmpeg` (W10.9), `TestNoIdentityWritesAsARetiredManager` (F9.1 -> F9.2), `TestNoImageShipsAPar2Executable` (W9.7), `TestNoInstallerRendersAnHPAOrKEDA` (W6.2), `TestNoObjectLinks` (W4.112, A5.2, W4.112, N8; ADR-0021), `TestNoProductionCodeStartsAProcess` (W5.14), `TestNoRenderedInstallerGrantsTheFoldedKinds` (F9.3), `TestNoRoleGrantsTheFoldedKinds` (F9.1 -> F9.2), `TestNoScriptRunsFFmpeg` (W10.9), `TestNoShutdownPathClosesTheConnectionOutsideShutdown` (ADR-0020), `TestNoV1SchemaOfTheFoldedKindsRemains` (F9.4), `TestNoV1TaskProducers` (F7.4), `TestNothingAppliesAsARetiredManager` (F9.1 -> F9.2), `TestNothingButHistoryNamesTheFoldedKinds` (F9.2), `TestNothingNamesDownload` (A3.9, A9.1), `TestNothingReconcilesAudioGrafts` (F7.3), `TestObjectMetadataComesFromItsStoresBuilder` (N8; ADR-0021), `TestOnlyNatsbusTouchesJetStreamObjectStores` (W4.112), `TestOnlyPkgK8sRegistersProbes` (W3.1, W5.15), `TestOnlyTheLoopWatchesMediaFile` (F0.3, F8.4, A3.5), `TestOnlyTheLoopWritesMediaFileStatus` (F3.4, F8.4, F9.1), `TestOnlyTheManagerEnsuresTopology` (W5.17, A3.7), `TestOnlyTheManagerRegistersControllers` (W5.15), `TestPar2LibraryComesOnlyFromPar2go` (W9.7), `TestPar2childLinksNoClustarrCode` (W9.3), `TestPlannerTagIsTheProfileStatusHash` (F6.3), `TestRecordsBucketsOpenOnlyThroughTheirStores` (F7.4, A7.2), `TestRecordsRequestersAreTheLoop` (F7.4), `TestRecordsWritersAreTheirAgents` (A7.2), `TestReleaseNsManagerValuesAreChecked` (W6.13), `TestRenamesAreTheLoopsAlone` (A6.1), `TestRetiredValuesKeysAreRejected` (W6.13), `TestTheBrandGateAllowListIsExact` (U7), `TestTheBrandGateDoesNotMatchItself` (U7), `TestTheCRDReadmeCountsTheEmbeddedCRDs` (F9.5), `TestTheChartREADMENamesOnlyLiveValuesKeys` (W6.13), `TestTheClustarrImageIsStatic` (W6.4), `TestTheDefaultValuesPassTheSchema` (W6.13), `TestTheDesignOfRecordDefinesNoFoldedKind` (F9.5), `TestTheE2EFixturesImageRunsNoFFmpeg` (W10.11), `TestTheExecGuardAllowsOnlyTheRepairChild` (W10.1), `TestTheFFmpegGuardCatchesEachShape` (W10.9), `TestTheFFmpegGuardCatchesIndirectShapes` (W10.9), `TestTheFetchWorkerWritesNoKubernetesObject` (F5.2), `TestTheFoldsRetiredManagersAreGone` (F9.1 -> F9.2), `TestTheMakefileChecksImagesThroughOneScript` (W6.6), `TestTheMakefileDerivesItsContextsFromGoMod` (W6.3), `TestTheMediaFileDomainLinksNoProbe` (F3.4), `TestTheNativeImageCarriesNoMediaExecutable` (W6.5), `TestTheNativeImageCarriesTheIntelStack` (W6.5), `TestTheNativeImageStagesEveryBinaryItBuilds` (W6.5), `TestTheNativeImageStagesOnlyFFgosFFmpegLibraries` (W6.5), `TestTheRetiredMediaFileIndexesAreGone` (F4.1), `TestTheTestEnvironmentHasNoFFmpegProgram` (W10.10), `TestTheTestTargetsLoadTheNativeAssets` (W6.7), `TestTheWorkflowsCheckImagesThroughOneScript` (W6.16), `TestTranscodeImportsNoKubernetesClient` (W5.14), `TestTranscodePhaseIsSelectable` (F6.3), `TestUILinksNoAppOrManagerCode` (W5.14), `TestUINeverLinksADynamicLoader` (W5.14), `TestChartAndKustomizeAgreePerComponent` (W4.75, W5.16 E), `TestChartImagesMatchConfig` (W5.16 E), `TestChartRBACMatchesTheGeneratedRoles` (W6.11 E), `TestChartRefusesTwoCardigannDefinitionSources` (W6.11 E), `TestControllerNamesAreUniqueAcrossTheBinary` (W5.15, A3.2 E), `TestEachServiceAccountHoldsExactlyItsOwnRole` (W6.11 E), `TestEnvironmentSuppliesDefaults` (W5.7, W5.9 E), `TestEveryManagerRunnableIsRegistered` (W3.5, W4.47 E), `TestEveryServiceComponentIsRegistered` (W3.5, W4.47 E), `TestForbidigoRejectsStatusWrites` (W5.17 E), `TestGrabEnginesRunAsAnAccountTheInstallerBinds` (U3 E), `TestTheOneTranscoderImageCarriesTheIntelStack` (W5.16, W6.5 E), `TestTheTranscoderImageCarriesNoFFmpegExecutable` (W5.16, W6.5 E), `TestUIRoleChartMatchesConfig` (A3.9 E), `TestUIRoleGrantsOnlyReadsAndActionWrites` (A3.9, S10 E), `TestWorkerGracePeriodsCoverAckWait` (W4.7, W6.11 E)
- `test/guards/depguard` (2): `TestRuleMatching` (W5.14), `TestTheHelperFindsAKnownChain` (W5.14)
- `test/installtest` (3): `TestDecodeRenderedSortsEveryKind` (W5.4), `TestPatternCovers` (W5.4), `TestRBACRolesReadsTheMakefile` (W5.4)
- `test/starttest` (5): `TestManagersOfCoverEveryFieldManager` (W5.4), `TestRecordingBusRecordsEachDurable` (W3.2), `TestRecordingManagerSortsOutWhatWaitsForTheLease` (W3.2), `TestRolesForBridgesToTheGeneratedRoles` (W5.4), `TestRolesForIsTheIdentityItself` (W6.11)
- `test/system` (6): `TestAMediaFilesStatusCarriesOnlyCatalogAcrossTheManagerAndImport` (F8.12), `TestHistoryAcrossManagerAndEvents` (W5.13), `TestImportListAcrossManagerAndImport` (W5.13), `TestMediaFileProbeRoundTrip` (W5.13), `TestNonVideoCatalogAcrossManagerAndMetadata` (W5.13), `TestRendererThroughTheOverlayProfileController` (W5.13)

**T3 notes.**

- `make chart-deps` (helm) and kustomize for the installer guards; `TestChartAndKustomizeAgreePerComponent` reads as a disagreement when the chart's dependencies are missing (CLAUDE.md gotcha).
- `pkg/crdcheck` skips silently without `KUBEBUILDER_ASSETS`; `CLUSTARR_REQUIRE_TOOLS=1` makes that a failure. `TestTheReleaseN1MediaFileCRDTakesOverReleaseNsStatus` is the one proof of F9.2's ratcheting ruling (`phase` and `name` required in the schema, `omitempty` in Go).
- `test/e2e` is vetted with `-tags e2e`; its `expectedCRDCount` still reads 26 while both installers ship 27 since W6.12 (F8.9's edit). `test/guards/images_test.go` reads `Dockerfile.transcoder`, deleted in W6 (ledger).
- When a † guard of CLAUDE.md is written, drop its † in the same commit (18 at the inventory).


---

## c. The order inside the batch

**Before T1 (controller, once, in the main worktree).** `make envtest pg-assets native-assets
chart-deps` -- the assets live under `$(go env GOPATH)/bin` and are shared by every worktree, and
six concurrent `make native-assets` race on one directory. Wait for W9.8's gate to land. Create
the six worktrees from one commit: `git worktree add ../clustarr-unify-tb-dN -b unify-tb-dN
<sha>`; copy `charts/clustarr/charts/*.tgz` (gitignored) into each.

**T1 -- compile.** All six domains in parallel, each over its own failing packages only.

1. Shared helpers first, by their owner: D6 makes `test/starttest` and `test/installtest`
   compile; D1 brings `schematest.Payloads()` up to the built payloads; D3 deletes the test files
   left in `pkg/mediainfo/ffprobeexec` (it has had no non-test files since W8; the ffprobe oracle
   W8.7 planned was not built -- move a model-only test to `pkg/mediainfo` or
   `pkg/mediainfo/native` where W8.3 says so). Each owner then switches its own tests that import
   `ffprobeexec` to `pkg/mediainfo/native` (five packages in D3, `app/import/worker/rescan` in D4,
   and the build-tagged `test/parity` (D3) and `test/e2e` (D6) files).
2. Then every other failing package. A test is rewritten against the new API, or deleted when its
   subject was removed; a deletion names the test and the reason in its commit message. No new
   test is written in T1, and no production code changes; a test that cannot compile without a
   production change stops the agent and goes to the controller.
3. D4 starts `pkg/download/usenet`, `pkg/par2child` and `app/grab/engine/usenet`, and D5 starts
   `cmd/agent`, only once W9.8 is on the batch's base commit (W9's agent edits them).

The controller merges the six branches (rebase and fast-forward, one at a time), runs the T1
gate on the merged tip, and cuts T2's worktrees from it.

**T2 -- the deferred tests.** Three steps, each merged before the next starts:

- **T2a, infrastructure.** D3: W6.18 `internal/testmedia`, then W6.23 (`test/goldens` and the
  recording -- see the D3 notes: the exec implementations it records are already deleted), then
  W7.3 `test/nativetest`, then `app/remediation/remediationtest`. D1: W4.42 `contracttest.Bind`
  and the contract cases' scaffolding. D6: `test/starttest` and `test/installtest` helpers. D4:
  `probetest`, `test/par2test`. D2: `itemtest`. Merged and gated (`go vet ./...`) before T2b.
- **T2b, every domain's own tests**, plus the clip conversions each owner does on top of
  `internal/testmedia`: W6.19 (`pkg/mediainfo`, `pkg/naming` in D3; `pkg/segments` in D2;
  `pkg/subtitles` in D4), W6.20 (`pkg/transcode`, D3), W6.21 (`app/transcode`, `cmd/transcode`,
  `test/parity`, D3), W6.22 (`app/import` in D4, `app/catalog` in D2, the rest by owner), and
  W10.11's `test/fixtures/mkclips` (D6). Inside a domain: helpers and pure-function tests first,
  then envtests, then the cross-package wiring tests (`register`, `manager`, `agent`).
- **T2c, the cross-domain guards (D6, last).** `test/guards` reads every domain's final code:
  the deps, exec, fold, records, RBAC, installer, brand and object-store guards, W10.9's
  `TestNoGoFileRunsFFmpeg` family, and then W10.10 (no `ffmpeg`/`ffprobe` program in the test
  environment, `CLUSTARR_FFGO_REQUIRE=1`, the native-assets, Makefile and CI changes) -- last of
  all, because W6.23's recording and every clip conversion must be done first.

**T3 -- green.** Each domain runs its packages under the `make test` environment, then with
`-race`, then `golangci-lint` over its packages, and fixes what fails in its own code; a bug in
another domain's code goes to that owner through the controller. The controller then runs the
full gates on the merged tip.

---

## d. Gates per phase

**T1.** On the merged tip, all clean:

```bash
go vet ./...
go vet -tags e2e ./test/e2e/...
go vet -tags parity ./test/parity/...
```

A domain checks itself first with `go vet` over its own prefixes. `go vet` stops at ten errors
per package and skips a package whose test files cannot load (a deleted import), so a package can
fail again after its first fix: loop until clean. Nine packages failed only that way at the
inventory (the `ffprobeexec` and deleted-package imports).

**T2.** (1) Every named test exists: the check below, per domain and then on the merged tip,
reports no missing test (`ELSEWHERE` lines are reviewed, not failures). (2) Each test was
seen failing before the code it guards, where that is feasible: the code is already written, so
write the test, see it pass, then break the guarded behaviour with a temporary edit (revert the
line, flip the condition; restore it with `git checkout -- <file>`, never `git stash`), see the
test fail by name, and restore. A test that cannot be made to fail that way -- a guard over an
absence, a golden, a test of a deleted path -- is listed in the domain's T2 report as "cannot
fail: <name> -- <why>"; the controller copies the falsification list into the ledger. (3) The T1
gate still holds.

Extract the check into your scratch directory and run it from the repo root:
`sed -n '/^# BEGIN t2check/,/^# END t2check/p' docs/superpowers/plans/2026-10-07-test-batch.md >
"$SCRATCH/t2check.sh" && bash "$SCRATCH/t2check.sh" '' D3` (an empty first argument takes the
committed catalogue). It passes when it prints no `MISSING` or `STILL-THERE` line.

```bash
# BEGIN t2check -- every live catalogue row exists in its package; a "delete" rewrite row is gone.
set -u
cat=${1:-}; [ -n "$cat" ] || cat=docs/superpowers/plans/2026-10-07-test-batch-catalogue.tsv
dom=${2:-all}
miss=0; warn=0; ok=0
while IFS=$'\t' read -r d pkg name src status; do
  [ "$d" = domain ] && continue
  [ "$status" = dropped-excluded ] && continue
  [ "$dom" != all ] && [ "$d" != "$dom" ] && continue
  sub=${name#*/}; [ "$sub" = "$name" ] && sub=
  if [ -n "$sub" ]; then pat="\"${sub}\""; glob="$pkg/*.go"; else pat="^func ${name}\("; glob="$pkg/*_test.go"; fi
  found=no
  # shellcheck disable=SC2086
  grep -qsE "$pat" $glob && found=here
  if [ $found = no ] && [ -z "$sub" ] && git grep -qE "^func ${name}\(" -- '*_test.go'; then found=elsewhere; fi
  if [ "$status" = rewrite ] && [[ "$src" == *delete* ]] && [[ "$src" != *edit* ]]; then
    if [ $found = here ]; then echo "STILL-THERE $d $pkg $name"; miss=$((miss+1)); else ok=$((ok+1)); fi
    continue
  fi
  case $found in
    here) ok=$((ok+1)) ;;
    elsewhere) warn=$((warn+1)); echo "ELSEWHERE $d $pkg $name -> $(git grep -lE "^func ${name}\(" -- '*_test.go' | xargs -n1 dirname | sort -u | tr '\n' ' ')" ;;
    no) miss=$((miss+1)); echo "MISSING $d $pkg $name ($src)" ;;
  esac
done < "$cat"
echo "ok=$ok missing=$miss elsewhere=$warn"
[ "$miss" -eq 0 ]
# END t2check
```

At the inventory it reported `ok=96 missing=1314 elsewhere=0` (the 96 are rewrite rows that
already exist, and deletes not yet due).

**T3.** `make test`, `make test-race` and `make lint` green on the merged tip, plus `make vet`,
`make brand-gate`, and `make generate manifests templ` leaving no diff. Prerequisites:

- `make chart-deps` (`helm dependency build charts/clustarr`; per worktree, since
  `charts/clustarr/charts/` is gitignored; or copy the `.tgz` files);
- `KUBEBUILDER_ASSETS` from `make envtest` (setup-envtest, Kubernetes 1.37.0; `make test` sets it);
- `CLUSTARR_PG_ASSETS` from `make pg-assets` (`pkg/relindex`'s Postgres store contract);
- `NATIVE_ENV` from `make native-assets` (FFmpeg 9's libraries and `libffshim`, ONNX Runtime,
  `libpar2shim`, the test-only CLIs; `CLUSTARR_REQUIRE_TOOLS=1` turns a missing tool into a
  failure, not a skip), and `make fork-status` (the ffgo fork) -- both prerequisites of `make test`;
- `CLUSTARR_TEST_MEDIA_ROOT` (default `/data/media`) writable, or the import scan suites skip;
- `-timeout 20m` (`test`), `60m` (`test-race`): the transcode envtests take some 460 s alone.

A domain runs the same environment over its own packages, e.g. for D4:
`KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" CLUSTARR_PG_ASSETS=… <NATIVE_ENV> go
test ./app/grab/... ./app/import/... ./app/indexer/... ./app/caption/... ./pkg/download/... …`.
e2e scenarios (`test/e2e`, tag `e2e`) are written in T2 and vetted in T3, never run: running them
needs a kind cluster, which the batch does not touch. The `parity`-tagged tests are owner-run
(`CLUSTARR_PARITY_DIR`) and vetted only.

---

## e. Counts

| Domain | T1 failing packages | T2 deferred | T2 rebuild | T2 rewrite | T2 total | Dropped (catalogue only) |
| --- | --- | --- | --- | --- | --- | --- |
| D1 | 5 | 201 | 0 | 16 | 217 | 21 |
| D2 | 26 | 206 | 0 | 15 | 221 | 39 |
| D3 | 14 | 254 | 63 | 19 | 336 | 145 |
| D4 | 23 | 237 | 27 | 33 | 297 | 31 |
| D5 | 6 | 135 | 0 | 9 | 144 | 6 |
| D6 | 4 | 171 | 0 | 24 | 195 | 18 |
| `-` (no package) | -- | -- | -- | -- | -- | 31 |
| **Total** | **78** | **1204** | **90** | **116** | **1410** | **291** |

The catalogue has 1701 rows. T1's 78 packages are the 66 `go vet ./...` reported at `ac521b88`, the 9 it could not load (found by compiling every test binary with `go test -exec /bin/true ./...`), `pkg/download/usenet` (newly failing at `66e44603`) and the two build-tagged packages (`test/e2e`, `test/parity`). Of the 1410 T2 tests, 39 are contract cases; 116 rewrites include 18 deletions of tests the plans retire (the T2 check expects them gone).

---

## Ambiguities, and how this plan rules them

1. **The batch is larger than the ledger's estimate.** The ledger guessed about 500 deferred
   tests; many of its lines say "every test the plan names for A4.1-A4.4" or "F6.4 25 (names in
   the plan)". Counting the plans' own bodies gives 1,204 deferred, 90 rebuilt and 116 rewritten.
   Ruling: the plans' names are the catalogue; a domain that finds a row whose subject is gone
   drops it with a reason rather than writing against a deleted API.
2. **Tests superseded by later waves are dropped, not written.** The split plan's Wave 3-5 tasks
   were written before ADR-0016/0019 and the cold turkey reshaped their code. Dropped, each with
   its superseding task in the catalogue: W4.8's probe seeding (A3.8, A6.1 dropped seeding), W4.20's
   session field managers (A6.4: KV only), W4.84's `applyMediaFile` CAS (A3.8), W4.107's reaper
   audit and purge (A5 R7; N5 moved purges to `app/maintenance`), W4.4's `Request`/`Publish` (A7.1
   deleted them), W4.12's waker (F1.4), W4.13's reconciler deps guard (F3.4), W4.82 (the A plan's
   own amendment), six W3 tests of the deleted `app/<svc>/run.go` shims (W5.17), W5.6's AudioGraft
   template test (F7.3), F6.2's graftstate test (F7.3), W4.1's exec-prober test (W8), the FFmpeg
   comparisons W6b's goldens replace (W7.6, W8.4), and the v1 stale-ack tests (F9.4's cold
   turkey). W4.13's other envtests are re-homed onto the loop's probe adapter, since the
   behaviour survives in `app/remediation/probe`.
3. **Ports the implementers deleted are rebuilt from history.** F3.2/F3.3 said "copy" the old
   MediaFile, naming and rename envtests into the loop's packages; F3.4 deleted the originals before
   any copy existed. Likewise F5.1/F5.2's ports, F6.3's `git mv`s (admission, class, pool predicate,
   profile status) and F6.6's parity envtest. Each is `rebuild-from-<sha>^`, the sha being the
   commit that deleted it.
4. **`f66-deleted-tests.txt`:** 94 of its tests are dropped (their subject, the TranscodeJob
   controller, is gone) and 10 rebuilt (the four moves and status checks named in 3, plus
   `TestStatusPlanIsTheArgvTheWorkerRenders`).
5. **`removal-deleted-tests.txt`:** the transcode worker's run/swap and sidecar envtests (26),
   `TestBuildTask` and the two rescan envtests are rebuilt from `0beb88d6^` on a MediaFile-keyed
   fixture, as the list asks; the progress-reporter tests are dropped (`status.progress` is gone;
   the `clustarr-progress` telemetry stays); rows and legs inside surviving tests are T1 work.
6. **Wave U.** The ledger's kept list stands; `TestUIHelpTextNamesNoBrand` folds into the
   brand-gate guard (U5). A plan name carrying a retired brand is renamed (the brand gate would
   refuse it), the plan name kept in the note.
7. **`TestTheFirstLoopPassKeepsEverySeedKey`** is kept, as the cold-turkey revision recommends,
   re-homed to F3.4's markers planner; it needs the owner's read-only `clustarr-segments` dump.
8. **W6.23's goldens** need the exec implementations W7.7, W8.7 and W8.9 already deleted. Ruling:
   record from throwaway worktrees at `08cf7fe3^`, `5b8d3b00^` and `55c18417^` with the
   native-assets CLIs, before W10.10 removes them -- or the owner records them. Building old code
   for this needs the controller's OK.
9. **W9 is in flight.** Its rows are deferred; W9 wrote no test through `66e44603`. D4 and D5 take
   W9's packages only after W9.8, and re-run the T2 check then.
10. **Boundaries that are judgement calls:** captions with acquisition (D4), markers and segments
    with the catalog (D2), `cmd/transcode` with D3, `app/autoscale` with D5, `app/dispatch` and
    `app/maintenance` with D1. They follow the import graph and balance the load; the largest
    domain (D3) also carries W6b and most of the rebuilds.
11. **Contract cases** are catalogued as `TestBusContract/<Case>`, including ADR-0021's
    `TestSetMetaSendsTheCompleteMap` (a case on both buses). The W4.114/W4.115 cases are dropped:
    those could-defer tasks were never built (consumers still carry `BackOff`).
12. **Repeated names** are separate rows when the plans put them in different packages
    (`TestRegisterRequiresTheBus` x7, `TestEveryComponentIsRegistered` and
    `TestTheBusLivenessCheckIsRegistered` x2, `TestRetiredFlagsAreGone` and `TestVersionFlag` x2,
    `TestTheLastDeliveryAnswersFailed` x2).
13. **"Rewritten, not dropped" under the cold turkey** is `deferred` with the rewrite named
    (`TestRetiredFieldManagersStayValid`, `TestCaptionWorkerIsRetired`, `TestTheResultsDurableIsRetired`,
    `TestManagerCarriesTheFoldFlags`, `TestReleaseNsManagerValuesAreChecked`, the two release-N+1
    CRD tests, `TestAMediaFilesStatusCarriesOnlyCatalogAcrossTheManagerAndImport`,
    `TestAPreJournalTransferIsMatchedByNameAndClaimed`, `TestNothingNamesDownload`); F9.1's five kept
    guards are deferred under F9.2.
14. **Tests with no package in their source** (ADR-0020's eleven, Wave N's eight extras, a few
    prose-only names) are placed by the code they test; the T2 check reports `ELSEWHERE` if an
    agent puts one in a better package.

---

## Execution

- Work only in your domain's worktree (`../clustarr-unify-tb-dN`, branch `unify-tb-dN`); edit
  only files under your domain's package prefixes (the partition table). Commit path-scoped:
  `git add <paths> && git commit -m '…' -- <paths>`; never a bare `git commit`.
- Never `git push`, never `git stash` (the stash is shared by every worktree), never `git reset
  --hard`, never touch a cluster, never `go get` or `go mod tidy`.
- Scratch goes under `$TMPDIR/…/scratchpad/batch-dN/` only; envtest and embedded NATS pick free
  ports, but name any fixed-port helper per domain.
- Do not edit `.superpowers/unify/progress.md`; report to the controller, which keeps the ledger
  (T1 deletions, T2 drops with reasons, the falsification list, T3 bug fixes).
- The plans' code predates later waves: the catalogue says what to write; the task text says how;
  the code as built decides the names. CLAUDE.md's gotchas bind the tests too (assert every leaf,
  act on an object that already has status, look in `managedFields` for an over-claim, build
  inputs through the real producer, falsify).
- When a guard named with † in CLAUDE.md is written, its † goes in the same commit (D6 owns
  CLAUDE.md for that edit; the other domains name the guard in their report).
