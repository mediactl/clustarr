# Phase D1 — `indexarr` (M2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `indexarr`, the indexer aggregation service, so that `catalogarr`'s search path — already shipped in Phase C and currently calling into silence — works end to end against a real indexer.

**Architecture:** One replica, `strategy: Recreate`, one RWO PVC, `--role all`, leader election forbidden. Three CRDs (`Indexer`, `IndexerDefinition`, `IndexerProxy`) reconcile indexer configuration and health; a SQLite FTS5 store on the PVC indexes every release seen; three NATS RPC verbs (`search`, `download`, `query`) serve `catalogarr` and, later, the M6 Torznab facade; an RSS worker polls each indexer on a schedule and publishes to the release firehose that `catalogarr`'s RSS matcher already consumes.

**Tech Stack:** Go 1.27, controller-runtime v0.25.1, k8s.io/* v0.37.0, NATS JetStream (`pkg/events`), `modernc.org/sqlite` (pure Go, no cgo — the image is distroless-static), `pkg/torznab` (the wire client for **both** Torznab and Newznab), `pkg/newznab` (category table only), `pkg/ratelimit`, `pkg/release`.

**Spec:** `docs/superpowers/specs/2026-09-18-clustarr-design.md` §6.2, §5, §16 M2, §12, §13; `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` (wins on conflict); `docs/adr/0003-release-index-sqlite-fts5.md`.

**Source research (read these, they carry exact values):**
- `docs/research/phase-d1-indexarr-spec.md` — spec sections verbatim, all three CRDs field by field, the RPC payload types, the shipped `pkg/events` topology, and §12's eight contradictions.
- `docs/research/phase-d1-libraries-api.md` — exact public APIs of `pkg/torznab`, `pkg/newznab`, `pkg/cardigann`, `pkg/ratelimit`, `pkg/release`, plus the caller-side contract `catalogarr/worker/search` already pins.

---

## Global Constraints

Copied from the spec and `CLAUDE.md`. Every task's requirements implicitly include this section.

- Module `github.com/mediactl/clustarr`. Go 1.27. Licence **GPL-3.0**; every Go file starts with the header in `hack/boilerplate.go.txt`.
- **One controller-writer per resource**, enforced by field manager. See Ruling R6 below — `indexarr` has two writer paths and this phase splits them *before* any controller is written.
- **Every status write goes through `pkg/k8s.PatchStatus`** (server-side apply, named field manager). `.Status().Update()` and `.Status().Patch()` are banned outside `pkg/k8s` and golangci-lint's forbidigo rule fails the build on them.
- **Server-side apply replaces a field manager's ownership set on every apply — it does not merge.** `CLAUDE.md` names **eight** distinct forms this took in Phase C. Read that entry before writing any apply. Treat every `PatchStatus` call as a complete declaration of everything that manager owns, and remember that **a test against a blank object cannot observe a release** — drive the object to its real steady state first, then trigger the path.
- **No `float32`/`float64` under `api/`.** `resource.Quantity` for decimals a human types; scaled integers (`Milli`, `Centis`, `Percent`) for telemetry.
- **Cap every status list** with `+kubebuilder:validation:MaxItems`.
- **The caller owns rate limiting.** A library package accepts an injected limiter and never defaults one on; the controller holds one limiter per host. (See Ruling R9 — `torznab.WithRateLimit` currently violates this and D1 fixes it.)
- **Every HTTP response body is read through a cap** — a package-level max, an `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel. `pkg/torznab` and `pkg/cardigann` do this at 8 MiB; follow them, **not** `pkg/metadata/clients/*`, which does not (recorded as a carried defect).
- **A NATS KV key must match `^[-/_=\.a-zA-Z0-9]+$`.** Build every key through `events.KVKeyToken`. Two separate illegal-key defects escaped in Phase C; the second left an object undeletable.
- Controllers: `RequeueAfter` only, `reconcile.TerminalError` for an invalid spec, `RecoverPanic`, conditions carrying `observedGeneration`, Events through the manager's recorder, a 5-minute reconciliation timeout. Workers: `events.Retry(after)` → nak with delay, a generic error → backoff nak, `events.Discard` → DLQ, **heartbeats on long tasks**.
- Logging is `slog` through `context` (`pkg/obs/logging.FromContext`); no package-level logger, no logger struct field. Spans wrap every `Reconcile`, work handler and outbound provider call. Metrics use the `clustarr_` prefix and base units and are **never labelled by title, path, release name or indexer-supplied string**.
- Tests are table-driven with testify; fixtures under `testdata/`. **No network in tests.** envtest suites need `KUBEBUILDER_ASSETS`; a suite finishing in milliseconds **skipped**, which is not a pass.
- Generated code stays clean: `make generate && make manifests` must leave no diff.

### Rules for parallel agents

1. **Never run `go get` or `go mod tidy` from a worker.** Task D1-0 adds every dependency serially, up front.
2. **Each task owns disjoint paths**, listed per task. `go.mod`, `go.sum`, `api/`, `pkg/events/`, `pkg/k8s/`, `config/`, `charts/` and `indexarr/run.go` belong to the controller unless a task says otherwise.
3. **Commit path-scoped:** `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "<msg>" -- <your paths>`. Never `git add -A`, never `git commit -a`.
4. **Never `go build` without `-o`** — a bare `go build` drops a binary in the repo root. Use `go build ./<pkg>/...`.
5. **Never `git stash`** — it is process-global and will sweep up another agent's uncommitted work. Use a throwaway `git worktree` if you need a clean baseline.
6. **Verify, do not trust.** The controller re-runs every gate itself after workers report.

---

## Pre-flight rulings

Research found eight contradictions between the spec, the shipped code and the manifests **before implementation began**. Phase C's evidence is that each such conflict costs a fix round when an implementer meets it mid-task. All eight are ruled on here. **Do not relitigate these**; if you believe one is wrong, say so with reasoning rather than implementing something you think is incorrect.

**R1 — `DefaultIndexPath` becomes `/var/lib/clustarr/index/releases.db`.**
`indexarr/run.go:52` compiles in `/index/releases.db`, which `readOnlyRootFilesystem: true` makes unwritable, and which contradicts the env var (`config/manager/indexarr.yaml:84-85`), the PVC mount (`:124`) and an existing assertion (`cmd/clustarr/deploy_args_test.go:290`). Only the compiled-in fallback is wrong, and only for someone running the binary with no env var — `clustarr all` on a dev box. One answer, not two: change the constant. Cost if wrong: a dev-box default moves into a directory that may not exist locally, which is a clear error rather than a silent read-only failure.

**R2 — `DefaultFacadeBindAddress` becomes `:8080`.**
Code binds `:9696` (Prowlarr's port); the manifest, the Service's named port and `charts/clustarr/values.yaml:229` all use 8080, and **no flag exists to override it**. As shipped the facade would bind a port nothing routes. The facade itself is M6 and is **not built in D1** — but the constant is a landmine sitting in a file D1 edits, and fixing it is one line. Pin it with a test asserting the constant matches the manifest's containerPort, so the two cannot drift again.

**R3 — `indexarr` counts grabs inside `rpc.indexarr.download`; no new `CLUSTARR_EVENTS` consumer is added.**
The spec says three different things about grab accounting (§5's subject table, §5's RPC table, §8.3), and **no durable consumer for indexarr exists on `StreamEvents`** — `defaultConsumers()` has exactly one, `catalogarr-history`. The RPC-table reading is the only one that needs no new shared infrastructure: indexarr increments `GrabLimit` in the `download` verb, where it already holds the indexer's session and passkey.
**Known limitation, document it in code:** a grab whose `DownloadSource` is `torrentURL`, `magnetURL` or `nzbURL` never calls `rpc.indexarr.download` (`api/download/v1alpha1/download_types.go:235`), so those grabs go uncounted against `GrabLimit`. That is acceptable for M2 — those grabs use no indexer credentials — but it means `grabsInWindow` undercounts for public indexers. Record it as a carried item for whoever implements grab-limit enforcement.

**R4 — serve `rpc.indexarr.query`; do NOT add a "query mode" field to `SearchSpec`.**
§16's M2 line names `rpc.indexarr.search|download|query` explicitly, so serving it is in scope. But it has **zero callers** today: `SearchSpec` has no mode switch that would route to the local index instead of the live fan-out, and the Torznab facade is M6. Serve it against the local index, exercise it directly from the e2e, and **do not** invent a CRD field for it — an API change with no consumer is how a field ships wrong. The missing "query mode" switch is a carried item for M6.

**R5 — `Caps.Modes` uses `torznab.SearchMode`'s real wire values.**
Two doc comments (`indexer_types.go:226-227`, `indexerdefinition_types.go:61-62`) say the keys are `search, tv-search, movie-search, …`. The real values, which `pkg/torznab` implements and `torznab.Caps.Supports` compares against, are `search`, `tvsearch`, `movie`, `music`, `audio`, `book` (`pkg/torznab/caps.go:41-47`). There is no enum marker, so nothing catches the divergence and the caps-gating predicate would **silently never match**. Fix both doc comments in the same change as the code, and add a test that pins the vocabulary against `torznab.SearchMode` so they cannot drift.

**R6 — add `indexarr-worker`; split the field managers before writing any controller. This is the most important ruling here.**
§2 lists `indexarr` and no worker, but indexarr has **two status-writing paths onto the same `Indexer` object**: the reconciler (conditions, protocol, privacy, caps, observedGeneration, sessionSecretRef) and the RSS worker plus search fan-out (lastRssAt, lastRssNewCount, indexedReleases, queriesInWindow, grabsInWindow, and the escalation fields via `RecordSuccess`/`RecordFailure`).

This is **exactly** the trap Phase C hit: one manager name shared by two writers means each apply releases the other's fields. Phase C tried the alternatives twice and rejected both — re-assertion couples unrelated writers and breaks the moment a third appears, and it was reproduced against a real apiserver deleting a live field. The remedy that worked, twice (`catalogarr-series`, then `catalogarr-metadata`/`catalogarr-grab`), is distinct managers so the ownership sets are disjoint natively and a genuine double-claim surfaces as a loud apiserver conflict instead of silent data loss.

So: add `ManagerIndexarrWorker = "indexarr-worker"` to `pkg/k8s/fieldmanager.go`, **to `FieldManagers()`** (forgetting this allow-list broke an entire task in Phase C wave 1 — every apply is rejected), to `pkg/k8s/fieldmanager_test.go`'s `want` list, and to the spec's §2 table with its rationale. Assign every `IndexerStatus` field to exactly one manager and write that assignment into both packages' doc comments.
Rejected alternative, on the record: "funnel every write through one reconciler" is tempting because indexarr is a single replica — but it couples the worker to the controller, and the single-replica premise is a deployment detail that a future change can quietly invalidate.

**R7 — `ConsumerIndexRSS` drops to `AckWait: 60s` with `Heartbeat: 30s`; spec §5's table is edited to match.**
`topology.go:535` sets `AckWait: 120s`, but `config/manager/indexarr.yaml:56` sets `terminationGracePeriodSeconds: 60`. The importarr consumers document the rule this violates, verbatim: a worker that is SIGTERMed must finish or give up inside the grace period, and *"any unit of work that can outlast 60s must send in-progress acks rather than have its AckWait raised past the grace period, and the manifest and this value must be changed together."* An RSS poll of a slow indexer can plausibly exceed 60s, and `ConsumerIndexRSS` has **no heartbeat today**.
Raising the grace period to 120s instead would slow every indexarr rollout for the sake of one consumer. Take the shape `ConsumerImportScan` already uses: 60s AckWait plus a 30s heartbeat, with the RSS worker sending in-progress acks. This changes a value spec §5's table pins, so **edit the spec table in the same commit** — leaving the spec and the topology disagreeing is how the next reader loses an afternoon.

**R8 — SQLite FTS5 via `modernc.org/sqlite`; `docs/research/indexers.md` §10 is superseded.**
That note marks Postgres FTS "best fit for a distributed K8s service" and sketches a full schema — but it **predates the decision**. ADR-0003 and spec §6.2/§12/§16 all pin SQLite FTS5 on an RWO volume, with Postgres named as the rejected alternative. The driver must be `modernc.org/sqlite` (pure Go): the image is distroless-static and a cgo driver will not link. Add a dated superseded-by note at the top of the research section so the next reader does not rediscover the wrong answer.

**R9 — `torznab.WithRateLimit` is reshaped to accept the injected limiter.**
It currently builds a private `*rate.Limiter` internally, so it **cannot** accept a `ratelimit.Limiter` — which contradicts the project convention stated in `CLAUDE.md` ("the caller owns rate limiting… never defaults one on") and prevents indexarr from holding one limiter per host across calls, which is the entire point. Reshape the option to take the injected limiter. Its package doc also cites `Indexer.spec.rateLimit`, a field that does not exist — the CRD has `spec.requestDelay` and `spec.limits`; fix that in the same change. `pkg/torznab` is a Phase B package with its own tests; changing an exported option is a real edit, so it gets its own task and its own review.

### Rulings added while writing the task sections

Two independent section writers converged on the same gap in the interface contract, and one found a multi-tenancy hole in a frozen payload. Both are ruled here.

**R10 — `SearchRequest` gains an optional `Namespace`, populated caller-side, in Task D1-0.**
`schema.SearchRequest` carries no namespace. `IndexerRefs []Ref` does carry one — `schema.Ref` has `Namespace`, `Name`, `UID` — but `catalogarr/worker/search` only populates `IndexerRefs` for **interactive** searches (`worker.go:471-482`). So an automatic search arrives at indexarr with no namespace anywhere, and indexarr would have to list `Indexer` objects **cluster-wide**, letting namespace A's Movie be served by namespace B's Indexer, with B's credentials, counted against B's grab limit.

Shipping that silently is not acceptable, and "indexarr lists cluster-wide" is not a decision a task should make by accident because a field was missing. Adding an **optional** field to a versioned payload is backward compatible — an old producer simply omits it — so:
- add `Namespace string \`json:"namespace,omitempty"\`` to `SearchRequest` with a doc comment saying why it exists;
- populate it in `catalogarr/worker/search`'s `buildRequest` from the namespace the worker already recovers from the envelope key;
- indexarr scopes its `Indexer` list to it, and when it is **empty** falls back to cluster-wide **with a logged warning naming the request**, so the old behaviour is observable rather than silent.

This is controller work in D1-0 because it spans `pkg/events/schema` and `catalogarr/` — neither is an indexarr task's path. Cost if wrong: one optional field on a payload that is not yet consumed by anything outside this repo.

**R11 — a caps-gated indexer that supports only `search` is skipped, with a named reason.**
Spec §6.2 describes a `t=search&q=` fallback for indexers that do not support id-based search. It is **unreachable as shipped**: the caller never sets `Text` (searches are ids-only), so there is nothing to fall back *with*. Do not build a fallback that cannot execute. Skip the indexer and emit a `SearchOutcome` whose reason names the real cause — "indexer supports only free-text search; request carries ids only" — so an operator sees why an indexer never contributes. Record the missing free-text path as a carried item; it becomes reachable when something populates `Text`.

**R12 — `alsoOn` provenance is logged, not modelled.**
Spec §6.2 mentions recording which other indexers also carried a release. Neither `schema.Release` nor `commonv1.ReleaseInfo` has such a field — a repo-wide grep finds zero hits. **Do not invent one.** An API field with no consumer is how a field ships wrong; Phase C had exactly that argument about a "query mode" switch. Log the provenance at debug and carry the modelling decision.

**R13 — the fan-out uses `sync.WaitGroup`, not `errgroup`.**
Spec §6.2's sketch uses `errgroup`, but `golang.org/x/sync` is an **indirect** dependency, and no worker may touch `go.mod` (rule 1). More importantly `errgroup`'s first-error-cancels semantics are wrong here: a fan-out wants **every** indexer's outcome, including the failures, because each one becomes a named `SearchOutcome` the operator reads. Use `sync.WaitGroup` and collect all results.

**R14 — the `indexarr-worker` owned-field set is pinned in exactly one place.**
D1-5 and D1-7 both apply under `ManagerIndexarrWorker`, so if their two applies declare different field sets, each release the other's — the eighth SSA form, which in Phase C was one sibling never getting the fix its siblings had. The set is defined **once**, in `indexarr/status.WorkerFields` (interface contract C5), and both tasks call it. Neither task hand-builds an apply configuration for that manager. Any test for either task's failure path must drive the Indexer to a **real steady state first**.

**R15 — R6's split stands; spec §6.2's controller sentence is amended to match.**
§6.2 has two clauses that disagree with each other. Its *controller* clause says the `indexer` controller "mirrors KV health/limits into status with Prowlarr's escalation table"; its *search-service* clause names `RecordSuccess`/`RecordFailure` in the fan-out. R6 follows the second, because the escalation is computed where the failure is observed — in the fan-out and the RSS poll, not in a reconcile that has no idea a query just failed. The reconciler **reads** the escalation fields and writes none of them.
Amend §6.2's controller sentence in Task D1-0 so the spec states one answer. Leaving both sentences standing guarantees this gets relitigated by whoever reads §6.2 first, which is the failure mode this whole rulings section exists to prevent.

**R16 — `Escalation` gains `InitialFailureAt` as a field, not a method.**
Three section writers independently hit this, which is as strong a signal as this process produces. One proposed a helper method on `Escalation`; correction C2 in the interface contract adds it as a struct field instead. Take the field: the complete-declaration rule means every caller must send `initialFailureAt` on every escalation apply, and a field makes that unmissable where a method is something a caller can simply not call. `RecordSuccess` clears it with the rest.

**R17 — `RecordFailure` takes process start as an explicit parameter.**
`StartupGrace` is defined against process start, but the contract's signature has no way to learn it. A package-level `processStart` variable that tests assign would make the function look pure while carrying a hidden global — and three callers share these functions, so the hidden dependency would be invisible at every call site. Pass it:

```go
func RecordFailure(cur indexv1alpha1.IndexerStatus, now, processStart time.Time, reason string) Escalation
func RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) Escalation
func EscalationTable() []time.Duration // returns a copy; the table is Prowlarr's, 10 entries, level capped at 9
```

**R18 — the owned set may vary with the spec's *shape*, never with a transient *outcome*; and `IndexerEvent` is published by the apply helper.**
Two findings, one ruling, because they share a mechanism.

`status.protocol` carries `enum: [torrent, usenet]` in the generated CRD, so an Indexer whose protocol is not yet resolvable (a definition-backed indexer, M6) must **omit** the field rather than send `""`, which the apiserver would reject. That is a legitimate variation in the owned set — and the formulation one section writer proposed is the right one to adopt project-wide: **the owned set may vary with the spec's shape, never with a transient outcome.** Omitting `protocol` because the spec cannot resolve it is shape. Omitting `caps` because this reconcile's probe failed is outcome, and that is the release bug.

Separately, §8.2 requires `schema.IndexerEvent` on `clustarr.evt.index.indexer.disabled|recovered|limited.<uid>`, and **no D1 task was going to publish it** — the reconciler has no bus handle and both worker paths assumed the other would. Put the publish inside `indexarr/status.Patch` (interface contract C5), where the escalation transition is already detected. One detection site, one event, and neither caller can forget it.

**R19 — `Query.Since` filters `fetched_at`, not `published_at`.**
The interface contract left `Since` uncommented, which is my omission. Filtering on `published_at` would **silently drop every dateless release**, because a nil publish date cannot satisfy a `>=` comparison — and dateless releases are common and legitimate. `fetched_at` is always set by the store and means what a caller actually wants: "what has this index seen since". D1-5 and D1-6 both build `Query` values and must assume the same answer; it is settled here so neither has to guess.

**R20 — the package stays `pkg/relindex`; spec §6.2 is amended.**
§6.2 names `indexarr/releaseindex`. The store has no controller-runtime dependency and no cluster dependency at all, which is exactly the property the project's `pkg/` convention exists for — it keeps the store testable with `go test ./pkg/relindex/...` and no envtest, and Phase B put every such package in `pkg/`. Three task sections are already written against `pkg/relindex`. Amend the spec in D1-0 rather than churn four sections for a placement the convention already decides.

**R21 — the four-method `Store` stands; the filters the spec and ADR sketch beyond it are carried, and `rpc.indexarr.query` is correspondingly limited.**
ADR-0003 fixes `Store` at `Upsert, Search, Prune, Stats`, and the contract's `Query` can express text, indexers, categories, protocol, since and limit — nothing more. But §6.2 lists twelve `pkg/release` columns that the fixed `Release` struct does not carry, and ADR-0003's own context promises size and seeder filters, ranking and paging that `Query` cannot express.
Do not widen the interface to chase them. **Do** record the consequence plainly: `rpc.indexarr.query` can answer "what have we seen matching this text, from these indexers, in these categories, since then" and nothing richer. That is enough for its only planned consumer (the M6 facade) and for the e2e. Carry the richer query surface, with the note that it needs both a wider `Release` and a wider `Query`, so whoever picks it up knows it is two changes and not one.
Also settled: §6.2's `expires_at` column is **not** built — it is redundant against `Prune(olderThan)`, which already expresses retention as a function of time, and a second source of truth for expiry is how the two drift.

**R22 — the startup grace is injectable, and D1-3 exposes it.**
`StartupGrace` is 15 minutes from process start, and `make e2e`'s entire budget is 30 minutes — so whether escalation fires during a scenario depends on how long indexarr's pod happened to have been up, which is nondeterministic and unrelated to the code under test. An untestable invariant is an invariant that rots.
Add `CLUSTARR_INDEXER_STARTUP_GRACE` (default `15m`), wired in D1-8 alongside the other env-var options and set to `0s` by `config/e2e`. R17 already makes `processStart` an explicit parameter, so this is a configuration value reaching the same call, not a second mechanism. The e2e asserts the plumbing as a **named precondition**, so a scenario that silently lost the override fails loudly rather than passing for the wrong reason.

**R23 — three e2e design constraints, accepted as specified. Recorded because each one is a trap that produces "flaky" rather than "failing".**

*Proof of contact.* The test process reaches only the Kubernetes API and `/data`, so it cannot ask the fixture whether it was called. The fixture writes a JSONL request log onto the shared PVC and the scenario reads it. This is the direct answer to Phase C's most instructive e2e defect, where an assertion passed from a warm cache with the stub scaled to zero — "the gateway reached a verdict" is not "the fixture was contacted on this run", and only the log distinguishes them.

*Cross-scenario contamination.* An RSS release carrying tmdb 27205 would also match Phase C's `TestLibraryRescan` Movie, whose `e2e-any` profile is single-tier — so every release is top-tier, `BypassIfHighestQuality` (default true) fires, and a real `Download` is created against another scenario's object. The scenario would "pass" while corrupting its neighbour, and the neighbour would fail intermittently depending on ordering. Avoided with fixture-owned TMDB ids (900100/900101) and scenario-owned two-tier quality profiles. **Generalise the rule: a new e2e scenario introduces its own fixture identifiers and its own profiles, and never reuses another scenario's.**

*Dedup windows constrain ordering, not just correctness.* `CLUSTARR_RELEASES` dedups on `MsgIDForRelease(indexerName, guid)` for two hours. The single fixture item publishes once with no second chance, so the Movie must be fully settled **before** the RSS-enabled Indexer is created, and the Indexer name must be per-run unique or a rerun inside two hours hangs on a message the stream has already seen. Both are ordering requirements that look like nondeterminism when violated.

**R24 — `indexarr/status` is created by D1-0, and no other task builds an apply for `ManagerIndexarrWorker`. This plan violated its own rule; here is the correction.**
R14 put the worker's owned field set in exactly one place. Three sections then claimed it: D1-0's text (written before correction C5) never creates the package, D1-5 hand-builds a `recordOutcome`, and D1-7 exports `rss.WorkerStatus` describing *itself* as "the one constructor" the others must call. That is precisely the duplication R14 exists to prevent, reproduced inside the document that states R14 — which is worth recording plainly, because it is the same failure mode as Phase C's two tasks independently building one mapping, and it survived right up until a fourth section tried to consume it.

**The package is `indexarr/status`, shipped in wave 0 by D1-0** (added to that task below). D1-5 deletes its `recordOutcome`; D1-7's `WorkerStatus` becomes a thin call into it, not a second definition. No task under `ManagerIndexarrWorker` constructs an apply configuration by hand.

**R25 — D1-0 also carries R10's `SearchRequest.Namespace`.**
Same cause: D1-0 was written before R10 existed. The optional field and its caller-side population are controller work in wave 0, because they span `pkg/events/schema` and `catalogarr/`, neither of which is an indexarr task's path.

**R26 — export `cardigann`'s redaction helpers rather than writing a third copy.**
`redactURL` and `redactErr` are unexported in `pkg/cardigann`, so D1-6 would hand-roll its own — a third implementation of "strip the passkey before this string reaches a log, an error, or a status condition". Secret redaction is the worst possible thing to have three of, because the copies drift silently and the failure is a credential on someone's screen. Export them from `pkg/cardigann` (it is the package that already got this right) and have D1-6 call them. This is a small additive change to a Phase B package; fold it into D1-1, which is already the "adjust a Phase B library" task.
Correction to my own brief while here: it told D1-6 to follow "`pkg/torznab`'s download call". **There is no such call** — `torznab.Client` exposes only `Caps` and `Search`. Only `cardigann.Engine.Download` fetches a torrent or NZB. D1-6 must not go looking for a method that does not exist.

**R27 — `rpc.indexarr.query` strips `DownloadURL` from its results.**
`query` has no namespace on its request and `relindex.Release` has no namespace column, so it is inherently cluster-wide — and its results carry `Info.DownloadURL`, which for a private indexer **embeds a passkey**. A cluster-wide read that returns other namespaces' credentials is a credential-disclosure surface, not a convenience.
Strip it: `query` answers "what releases have we seen", and a caller that wants the bytes calls `download`, which is namespace-scoped and refuses an empty `IndexerRef.Namespace`. That keeps the credential on exactly one path, the one that already authenticates. Carry the namespace-scoping of the index itself to M6, when the facade gives `query` a real consumer.

**R28 — the download reply is capped at 4 MiB, not 8.**
`DownloadResponse.Bytes` is base64 inside a JSON reply sent as a single NATS message, and `config/nats/configmap.yaml:20` sets `max_payload: 8Mi`. Base64 inflates by 4/3 before the JSON envelope is added, so an 8 MiB body cannot fit an 8 MiB payload. Pin `MaxPayloadBytes = 4 << 20` **with the arithmetic in a test**, so the next person to raise it sees why it is not simply the HTTP cap. The 8 MiB `io.LimitReader` convention still applies to the outbound fetch; the two limits are different things and the code should say so.

---

**R29 — no task writes its own FTS5 MATCH escaping, and the reason is a live DoS found in D1-2.**

The brief for `pkg/relindex` built its MATCH expression with `strings.Fields`, which does **not** split on NUL. A `Query.Text` of `"dune\x00matrix"` therefore bound as one token, the driver handed SQLite a C string truncated at the NUL, and the query failed with `SQL logic error: unterminated string`. `Query.Text` is attacker-controlled — it originates in release titles from third-party indexers — so that is a **one-byte denial of service on every search**, and it was in the plan, not in the implementation. D1-2 found it with the brief's own hostile-input corpus and fixed it by treating control runes as term separators, which is what SQLite's `unicode61` tokenizer does anyway.

The rule this establishes: **`pkg/relindex` owns MATCH escaping, and no caller performs its own.** D1-5 and D1-6 pass raw text into `relindex.Query` and rely on the store, exactly as the interface contract already says. If either is tempted to pre-sanitise, normalise or tokenise the text before handing it over, it must not — a second escaper is a second place for this bug to live, and the two will drift. Any task that believes it needs its own must raise it rather than write one.

**R30 — the same-normaliser contract needs a test outside `pkg/relindex`.**
D1-2 reports, correctly, that nothing inside the package can catch a mismatch between how `TitleNorm` is produced on the write side and how `Query.Text` is normalised on the read side: the store sees both as opaque strings and would happily index one form and search for another, returning nothing and looking like "no results". The test belongs where both sides are visible — D1-7 writes `TitleNorm`, D1-5 and D1-6 read it — so **D1-5 owns a test that indexes a release through the real write path and finds it through the real query path**, with a title that actually exercises normalisation (mixed case, dots, a release group). Without it the two halves can disagree silently and forever.

---

## Cross-task interface contract

Phase C's most expensive defects were not inside tasks; they were **between** them — two tasks building the same subject token two ways, a producer and a consumer disagreeing about an envelope key, an interface exported for a neighbour that the neighbour then reimplemented. Every symbol two D1 tasks share is fixed here, verbatim. A task that needs a shape not listed here must ask the controller rather than invent one.

### Shared symbols, by owning task

```go
// ---- D1-2 owns: pkg/relindex ----
// ADR-0003 fixes this interface at exactly four methods. Do not widen it.
package relindex

type Store interface {
    Upsert(ctx context.Context, rels []Release) (inserted int, err error)
    Search(ctx context.Context, q Query) ([]Release, error)
    Prune(ctx context.Context, olderThan time.Time) (deleted int, err error)
    Stats(ctx context.Context) (Stats, error)
}

// Open returns a Store backed by SQLite FTS5 at path, creating the schema if
// absent. It is the ONLY constructor; callers never touch database/sql.
func Open(ctx context.Context, path string) (Store, io.Closer, error)

type Release struct {
    Indexer     string    // the Indexer CR's name -- half of UNIQUE(indexer, guid)
    GUID        string    // the indexer's own id -- the other half
    Title       string    // raw title, exactly as the indexer returned it
    TitleNorm   string    // lowercased/normalised, the FTS5 column
    Group       string    // release group, the second FTS5 column
    Protocol    string    // "torrent" | "usenet"
    Categories  []int     // newznab category ids
    SizeBytes   int64
    PublishedAt *time.Time // nil when the indexer reported none -- NEVER backfill
    FetchedAt   time.Time
    InfoJSON    []byte     // the full schema.Release, for replay without re-query
}

type Query struct {
    Text       string // FTS5 MATCH against title_norm and grp; empty means "no text filter"
    Indexers   []string
    Categories []int
    Protocol   string
    Since      *time.Time
    Limit      int // caller-supplied; the store never invents one
}

type Stats struct {
    Releases   int64
    Indexers   int64
    OldestSeen time.Time
    NewestSeen time.Time
    SizeBytes  int64 // on-disk size of the database file
}
```

```go
// ---- D1-3 owns: indexarr/controller/indexer ----
// Health and backoff, ported from Prowlarr's verified algorithm. Exported
// because D1-5's search fan-out records every outcome through them, and D1-7's
// RSS worker records its own. They are PURE -- they compute the next status
// from the current one and never write to the apiserver, so the caller decides
// which field manager applies the result.
package indexer

// EscalationTable is Prowlarr's 10-entry backoff ladder; level is capped at 9.
// A failure inside StartupGrace of process start does not escalate.
const StartupGrace = 15 * time.Minute

// RecordFailure returns the escalation fields the caller must apply.
func RecordFailure(cur indexv1alpha1.IndexerStatus, now time.Time, reason string) Escalation

// RecordSuccess clears the escalation. It returns the zero Escalation when the
// indexer was already healthy, so a caller can skip a no-op apply.
func RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) Escalation

type Escalation struct {
    FailureLevel   int32
    DisabledUntil  *metav1.Time
    LastFailureAt  *metav1.Time
    LastFailureMsg string
    Changed        bool // false when applying this would be a no-op
}

// Healthy reports whether the indexer may be queried right now.
func Healthy(st indexv1alpha1.IndexerStatus, now time.Time) bool

// SupportsMode gates the caps check. mode MUST be a torznab.SearchMode value
// ("search", "tvsearch", "movie", "music", "audio", "book") -- see Ruling R5;
// the CRD doc comments naming "tv-search"/"movie-search" are wrong and are
// corrected in D1-0.
func SupportsMode(caps indexv1alpha1.Caps, mode string) bool
```

```go
// ---- D1-7 owns: indexarr/worker/rss ----
// The firehose publisher. D1-5 does NOT publish releases; only the RSS worker
// does. catalogarr/worker/rssmatcher already consumes this and its handler
// pins two requirements, both load-bearing:
//
//   1. Subject: events.ReleaseSubject(protocol, indexerName, newznabTop)
//   2. Envelope.Key MUST be "<namespace>/<indexerName>". The matcher splits on
//      "/" for its namespace and events.Discard()s -- straight to the DLQ,
//      bypassing MaxDeliver -- on anything without one. Guessing a namespace
//      instead would fan one indexer's releases across the whole cluster.
//
// Msg-Id is events.MsgIDForRelease(indexerName, guid) = sha1(indexer:guid),
// which the stream dedups on for 2h.
package rss

// PublishReleases publishes each release to the firehose. It is exported so
// D1-9's e2e can drive it directly without a full reconcile.
func PublishReleases(ctx context.Context, bus events.Bus, ns, indexerName string, rels []schema.Release) (published int, err error)
```

```go
// ---- D1-5 owns: indexarr/search ----
// The RPC server half. The request and response types ALREADY EXIST in
// pkg/events/schema/index.go and are ALREADY CALLED by
// catalogarr/worker/search/rpc.go:38-45. Do not redefine them; do not change
// their shape. The caller pins: Limit is always 500 (schema.MaxSearchReleases),
// Kind is only ever movie or episode, Text is never set (ids-only), and anime
// arrives as an absolute number in Episode with Season nil.
package search

// Serve registers all three RPC verbs on the bus under queue group "indexarr".
// D1-6 supplies downloadFn and queryFn; D1-8 calls this once from run.go.
func Serve(ctx context.Context, bus events.Bus, s *Service) (stop func(), err error)
```

### Things two tasks must NOT each build

- **The release→`schema.Release` projection.** D1-5 and D1-7 both turn a `torznab.Release` into a `schema.Release`. It is built **once**, by D1-7, and exported as `rss.ProjectRelease(torznab.Release, indexerName, protocol string) schema.Release`. D1-5 imports it. In Phase C, two tasks each built the download-source mapping and the results disagreed on an immutable field, which made the two grab paths mutually exclusive at the apiserver — do not repeat it.
- **The per-host rate limiter.** One `ratelimit.Limiter` per indexer host, owned by D1-3's reconciler and handed to callers. D1-5 and D1-7 never construct one.
- **`events.KVKeyToken`.** Every KV key goes through it. No task writes its own escaping; two illegal-key defects escaped in Phase C and the second made an object undeletable.

### Corrections to this contract (2026-09-19)

Writing the D1-7 section surfaced five defects in the contract above. They are corrected here rather than in place, so the record shows the contract was wrong and how.

**C1 — the second subject/key segment is the Indexer's OBJECT name, not its display name.**
`ReleaseInfo` carries both `IndexerRef` (the CR's `metadata.name`) and `IndexerName` (a human display name), and the text above said only "indexerName", which is ambiguous in the worst possible way. The shipped matcher keys indexer priority off `rel.Info.IndexerRef` (`catalogarr/worker/rssmatcher/resolve.go:216`) and logs that field (`handler.go:175`). So **all three must be the object name**: the subject's second token, the envelope key's second segment, and `ReleaseInfo.IndexerRef`. Using the display name would break priority resolution silently — a release would arrive, match, and rank against a priority that resolves to nothing.

**C2 — `Escalation` must carry `InitialFailureAt`.**
`IndexerStatus.InitialFailureAt` records when the current failure streak began, and the complete-declaration rule (R6) means whoever applies the escalation must send it — otherwise it is released on the first failure apply, which resets the streak and defeats the backoff ladder. The struct in this contract omitted it. Corrected:

```go
type Escalation struct {
    FailureLevel     int32
    InitialFailureAt *metav1.Time // start of the current streak; nil once healthy
    DisabledUntil    *metav1.Time
    LastFailureAt    *metav1.Time
    LastFailureMsg   string
    Changed          bool
}
```

`RecordSuccess` clears `InitialFailureAt` along with the rest.

**C3 — `schema.Release.Kind` is derived from the newznab category, and that is the only authoritative source.**
`pkg/release.ParsedRelease` does not carry the kind that `Parse` dispatched on, and the RSS path has no `SearchRequest` to take it from. So the kind comes from the release's newznab category ids via `pkg/newznab`'s table: 2000-range → movie, 5000-range → episode, and so on. D1-7 owns the mapping and exports it:

```go
// KindForCategories maps newznab category ids to a media kind. It returns
// ("", false) when no category resolves -- the caller then omits Kind rather
// than guessing, because the matcher uses it to decide what to match against
// and a wrong kind is worse than an absent one.
func KindForCategories(cats []int) (commonv1.MediaKind, bool)
```

**C4 — the dedup windows are different, and the contract stated the wrong one.**
`CLUSTARR_RELEASES` (the firehose) dedups for **2 hours**; `CLUSTARR_WORK_INDEXARR` (where the scheduled `RssTask` lives) dedups for **1 hour**. So a duplicate *schedule* is suppressed by the 1h window, while the 2h window is what makes a duplicate *poll* harmless downstream. Task D1-7's schedule msg-id must be quantised to a slot shorter than 1 hour for the dedup to mean anything.

**C5 — the shared status-apply helper belongs to the controller, not to whichever task writes it first.**
Three tasks (D1-3, D1-5, D1-7) all apply `IndexerStatus` and all schedule bus work. Left unowned, each would build its own — which is precisely how Phase C ended up with two tasks building the same projection and disagreeing on an immutable field. Task **D1-0** ships a small shared package `indexarr/status` before wave 1:

```go
package status // indexarr/status

// Patch applies a complete IndexerStatus declaration under mgr. It exists so
// that the three writer paths cannot each invent their own apply, and so the
// complete-declaration rule (R6) has exactly one place to be enforced.
func Patch(ctx context.Context, c client.Client, mgr k8s.FieldManager,
    idx *indexv1alpha1.Indexer, mutate func(*indexac.IndexerStatusApplyConfiguration)) error

// WorkerFields is the complete set ManagerIndexarrWorker owns. Every apply
// under that manager declares all of them; anything omitted is released.
func WorkerFields(st indexv1alpha1.IndexerStatus) *indexac.IndexerStatusApplyConfiguration

// ScheduleNext publishes the next RssTask onto the holding subject.
// TaskMsgID quantises to a slot so a duplicate schedule dedups (see C4).
func ScheduleNext(ctx context.Context, bus events.Bus, ns, indexerRef string, at time.Time) error
func TaskMsgID(ns, indexerRef string, at time.Time) string
```

---

## Execution order

Eleven tasks in six waves. Tasks inside a wave own disjoint paths and run in parallel; each wave lands before the next begins. The dependency that sets the shape is that three tasks write `IndexerStatus` and one package they all use must exist first.

| Wave | Tasks | Why together |
| --- | --- | --- |
| **0** | D1-0 | Serial, controller-run. Every path is shared infrastructure — `go.mod`, `pkg/k8s`, `pkg/events`, `api/`, the spec. Nothing else may run while it does. |
| **1** | D1-1 · D1-2 · D1-4 | Disjoint and independent: `pkg/torznab`, `pkg/relindex`, the two config controllers. None of the three reads another's output. |
| **2** | D1-3 · D1-7 | Both need wave 1 (`torznab`'s reshaped option; `relindex`). They write **different** `IndexerStatus` field sets under **different** field managers, which is the whole point of Ruling R6 — this wave is the first real test of that split. |
| **3** | D1-5 · D1-6 | Both need D1-3's pure health functions and D1-7's `ProjectRelease`. D1-5 registers all three RPC verbs and takes D1-6's two handlers, so D1-6 produces handlers rather than a second server. |
| **4** | D1-8 | Serial. Registration points are shared files; this is also where Phase C found two Criticals no component review could see. |
| **5** | D1-9 | The e2e scenario, against a cluster that now actually reconciles. |
| **6** | D1-10 | Serial. The phase gate, the Status rewrite, the carries. |

### The three status writers, and why the ordering matters

`Indexer.status` has three writer paths, which is one more than the spec anticipated:

| Path | Task | Manager | Fields |
| --- | --- | --- | --- |
| reconciler | D1-3 | `indexarr` | conditions, protocol, privacy, caps, observedGeneration, sessionSecretRef |
| RSS poll | D1-7 | `indexarr-worker` | lastRssAt, lastRssNewCount, indexedReleases, + escalation |
| search fan-out | D1-5 | `indexarr-worker` | queriesInWindow, grabsInWindow, + escalation |

D1-5 and D1-7 **share** a manager, so their applies must declare the identical field set or each releases the other's — which is the eighth form of the hazard `CLAUDE.md` catalogues, and in Phase C it took the shape of one sibling never receiving the fix its siblings got. Ruling R14 puts that set in exactly one function, `indexarr/status.WorkerFields`, shipped by D1-0 in wave 0 so both tasks import it rather than each writing an apply. Neither task hand-builds an apply configuration for that manager.

### Review discipline

Follow `superpowers:subagent-driven-development`: a fresh implementer per task, a task review after each (spec compliance **and** code quality, both required), fix rounds with scoped re-reviews, then one whole-branch review at the end and a single fix wave.

Two things Phase C proved worth insisting on, because they are what turned reviews from opinion into evidence:

- **Falsify, don't read.** A reviewer that reverts the fix in a throwaway `git worktree` and watches the new test fail by name has established something; one that reads the diff and agrees has not. Phase C's reviewers did this routinely by the end and it found defects that reading had missed twice.
- **A steady-state test is necessary but not sufficient.** The sharpest lesson of the last phase came from a test that correctly drove an object to steady state and *still* passed while watching the object get gutted — because it only asserted that its own field had arrived. Assert that the rest of the object survived.

### What "done" means for D1

The gate is not green tests. It is: `catalogarr`'s search path — built in Phase C and calling into silence ever since — returns ranked results from a real indexer, on a real cluster, with the release firehose reaching the RSS matcher and an unhealthy indexer visibly backing off.

---

## Wave 0 — serial, controller-run, before any worker is dispatched

### Task D1-0: dependencies, the field-manager split, and the six recorded conflicts

**Files:**
- Modify: `go.mod`, `go.sum` (one `go get`, run by the controller only)
- Create: `hack/deps/deps.go` entry for `modernc.org/sqlite` if no real importer exists yet
- Modify: `pkg/k8s/fieldmanager.go` (add `ManagerIndexarrWorker`, **and add it to `FieldManagers()`**)
- Modify: `pkg/k8s/fieldmanager_test.go` (`want` list)
- Modify: `docs/superpowers/specs/2026-09-18-clustarr-design.md` (§2 field-manager table; §5's `indexarr-rss` AckWait row)
- Modify: `pkg/events/topology.go` (`ConsumerIndexRSS`: AckWait 120s → 60s, add `Heartbeat: 30s`)
- Modify: `indexarr/run.go` (two constants only — `DefaultIndexPath`, `DefaultFacadeBindAddress`)
- Modify: `api/index/v1alpha1/indexer_types.go`, `indexerdefinition_types.go` (the two wrong `Caps.Modes` doc comments)
- Create: `cmd/clustarr/indexarr_defaults_test.go` (pins the two constants against the manifest)
- Modify: `docs/research/indexers.md` (dated superseded-by note on §10)

**Path ownership:** controller only. No worker is running. Every path here is shared infrastructure that three or more later tasks read, which is exactly why it is serial and first.

**Interfaces — Produces:** `k8s.ManagerIndexarrWorker` (`"indexarr-worker"`), a `ConsumerIndexRSS` whose AckWait fits the pod's grace period, and two constants that match the manifests.

- [ ] **Step 1: Add the SQLite driver, serially**

`modernc.org/sqlite` is pure Go. This matters: `images/Dockerfile.controller` is two-stage distroless-static, and a cgo driver will not link.

```bash
cd /home/appkins/src/mediactl/clustarr
go get modernc.org/sqlite@latest
go mod tidy
go build ./... && go vet ./...
```

Record the resolved version in the commit message. **This is the only `go get` in the entire phase** — every later task is told the dependency is already present, because concurrent module edits corrupt `go.mod` and a `tidy` in one agent strips what another just added.

- [ ] **Step 2: Keep the dependency alive until a real importer exists**

`go mod tidy` strips a module nothing imports, and `pkg/relindex` (Task D1-2) does not exist yet. If `hack/deps/deps.go` exists, add a blank import; if it does not, create it following the Phase B pattern. Delete the entry in Task D1-10 once `pkg/relindex` imports the driver for real.

- [ ] **Step 3: Add the `indexarr-worker` field manager**

This implements Ruling R6 and it is the highest-value step in the task. Add to `pkg/k8s/fieldmanager.go`:

```go
	// ManagerIndexarrWorker is the indexarr RSS worker and search fan-out.
	// It is deliberately distinct from ManagerIndexarr, which the Indexer
	// reconciler uses for that same object's configuration status.
	//
	// Server-side apply replaces a manager's whole ownership set on every
	// apply, so two writers sharing one manager name on one object silently
	// release each other's fields. This project hit that eight times in one
	// phase. Distinct managers make the split native -- and if the two ever
	// both claim a field, the apiserver reports a loud conflict instead of
	// losing data quietly.
	//
	// The split on IndexerStatus:
	//   indexarr        -- conditions, protocol, privacy, caps,
	//                      observedGeneration, sessionSecretRef
	//   indexarr-worker -- lastRssAt, lastRssNewCount, indexedReleases,
	//                      queriesInWindow, grabsInWindow, failureLevel,
	//                      disabledUntil, lastFailureAt, lastFailureMsg
	ManagerIndexarrWorker FieldManager = "indexarr-worker"
```

- [ ] **Step 4: Add it to the allow-list — do not skip this**

`FieldManager.Validate()` rejects any name not in `FieldManagers()`, so a manager that is declared but not allow-listed makes **every apply using it fail**. In Phase C this exact omission blocked an entire task until the controller noticed. Add `ManagerIndexarrWorker` to the slice in `FieldManagers()`, in spec order.

- [ ] **Step 5: Run the guard test and watch it fail, then fix the list**

```bash
go test -count=1 -run TestFieldManagersAreTheOnesTheSpecLists ./pkg/k8s/
```

Expected: FAIL — the test pins the manager list against the spec's §2 table. That is the test doing its job. Add `"indexarr-worker"` to `want` in `pkg/k8s/fieldmanager_test.go`, add the row to §2's table in the design spec with the rationale from Step 3, and re-run to green. **Change the test and the spec together**; a guard silenced to match the code protects nothing.

- [ ] **Step 6: Fix `ConsumerIndexRSS`'s AckWait and add a heartbeat**

Ruling R7. In `pkg/events/topology.go`, change `ConsumerIndexRSS` from `AckWait: 120 * s` to `AckWait: 60 * s` and add `Heartbeat: 30 * s`, matching the shape `ConsumerImportScan` already uses. The importarr block above it states the rule verbatim — a worker that is SIGTERMed must finish or give up inside `terminationGracePeriodSeconds: 60`, and work that can outlast that sends in-progress acks rather than raising AckWait past the grace period.

Then edit spec §5's consumer table so it says 60s too. The spec pins this value; leaving the two disagreeing is how the next reader loses an afternoon.

- [ ] **Step 7: Fix the two constants and pin them**

Rulings R1 and R2, in `indexarr/run.go`:

```go
	// DefaultIndexPath is the SQLite release index. It must match the PVC
	// mount in config/manager/indexarr.yaml: readOnlyRootFilesystem makes
	// anything outside the mount unwritable.
	DefaultIndexPath = "/var/lib/clustarr/index/releases.db"

	// DefaultFacadeBindAddress must match the container port the Service
	// routes to. The facade itself is M6; this constant is fixed now because
	// it is a landmine, not because the facade is being built.
	DefaultFacadeBindAddress = ":8080"
```

Then write `cmd/clustarr/indexarr_defaults_test.go` asserting both against the manifest, by parsing `config/manager/indexarr.yaml` rather than restating its values — a test that restates the value it is guarding cannot catch drift, which is how `ValidKVKey` shipped wrong in Phase C.

- [ ] **Step 8: Fix the `Caps.Modes` doc comments**

Ruling R5. Both `api/index/v1alpha1/indexer_types.go:226-227` and `indexerdefinition_types.go:61-62` claim the keys are `search, tv-search, movie-search, …`. The real values are `torznab.SearchMode`'s: `search`, `tvsearch`, `movie`, `music`, `audio`, `book`. There is no enum marker, so nothing catches the divergence — and a wrong vocabulary means the caps gate silently never matches and no indexer is ever queried. Fix both comments to name the real values and to point at `pkg/torznab/caps.go` as the source of truth.

- [ ] **Step 9: Mark the superseded research**

Ruling R8. Add a dated note at the top of `docs/research/indexers.md` §10 recording that its Postgres recommendation predates ADR-0003, which chose SQLite FTS5 on an RWO volume, and that Postgres is the documented rejected alternative. The note is the point: the next reader should not have to rediscover which document won.

- [ ] **Step 10: Ship `indexarr/status`, the one place the worker's owned set is declared**

Ruling R24. Three tasks write `IndexerStatus`; two of them share `ManagerIndexarrWorker`, so their applies must declare an identical field set or each releases the other's. Create `indexarr/status` now, in wave 0, so wave 2 and wave 3 import it instead of each inventing one:

```go
// Package status is the single place indexarr declares what each field
// manager owns. It exists because IndexerStatus has three writer paths --
// the reconciler, the RSS poll and the search fan-out -- and the last two
// share a field manager. Server-side apply replaces a manager's ownership
// set on every apply, so two callers declaring different sets would release
// each other's fields.
package status

func Patch(ctx context.Context, c client.Client, mgr k8s.FieldManager,
    idx *indexv1alpha1.Indexer, mutate func(*indexac.IndexerStatusApplyConfiguration)) error

// WorkerFields returns the complete set ManagerIndexarrWorker owns, seeded
// from the live status. Every apply under that manager starts here.
func WorkerFields(st indexv1alpha1.IndexerStatus) *indexac.IndexerStatusApplyConfiguration

func ScheduleNext(ctx context.Context, bus events.Bus, ns, indexerRef string, at time.Time) error
func TaskMsgID(ns, indexerRef string, at time.Time) string
```

`Patch` also publishes `schema.IndexerEvent` on `clustarr.evt.index.indexer.disabled|recovered|limited.<uid>` when it observes an escalation transition (Ruling R18) — one detection site, one event, and neither caller can forget it.

Write a test that drives an Indexer to a populated steady state, applies through `Patch` under each manager in turn, and asserts **neither release the other's fields**. That test is the whole reason this package exists.

- [ ] **Step 10b: Add `SearchRequest.Namespace` and populate it**

Ruling R10. Add the optional field to `pkg/events/schema/index.go` with a doc comment explaining that without it an automatic search carries no namespace at all, then populate it in `catalogarr/worker/search`'s `buildRequest` from the namespace the worker already recovers from the envelope key. Optional, so an old producer simply omits it.

- [ ] **Step 11: Regenerate and gate**

```bash
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
make generate && make manifests && git status --short   # must be clean
make lint
go test -count=1 -race -p 4 ./pkg/k8s/... ./pkg/events/... ./cmd/...
go mod tidy -diff
```

- [ ] **Step 12: Commit**

```bash
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "chore(d1): add the SQLite driver, split the indexarr field managers, and fix six recorded conflicts" -- go.mod go.sum hack/deps pkg/k8s pkg/events docs indexarr/run.go api/index cmd/clustarr
```

**Done when:** `modernc.org/sqlite` is in `go.mod`; `indexarr-worker` exists in the code, the allow-list, the guard test and spec §2; `ConsumerIndexRSS` is 60s/30s and spec §5 agrees; both constants match the manifest and a test parses the manifest to prove it; both `Caps.Modes` doc comments name the real vocabulary; the research note is marked superseded; the gate is green and `make generate && make manifests` leaves no diff.

---

## Wave 1 — three parallel tasks on disjoint paths

### Task D1-1: reshape `torznab.WithRateLimit` to accept the injected limiter

**Files:**
- Modify: `pkg/torznab/client.go` (the `WithRateLimit` option and the field it sets)
- Modify: `pkg/torznab/doc.go` (the stale `Indexer.spec.rateLimit` reference)
- Modify: `pkg/torznab/client_test.go` (call sites and a new test)

**Path ownership:** `pkg/torznab/` and nothing else.

**Interfaces — Consumes:** `ratelimit.Limiter` (`pkg/ratelimit`, already shipped).
**Interfaces — Produces:**
```go
// WithRateLimit makes the client wait on lim before every outbound request.
// The caller owns the limiter: indexarr holds one per indexer host and shares
// it across the caps probe, the search fan-out and the RSS poll, so that all
// three respect one budget.
func WithRateLimit(lim ratelimit.Limiter) ClientOption
```

**Why this is its own task.** `pkg/torznab` is a reviewed Phase B package and this changes an exported option's signature. It gets its own review rather than riding along inside a controller task.

- [ ] **Step 1: Write the failing test**

The behaviour that matters is that **the caller's** limiter is the one consulted — today it cannot be, because the option builds a private one. Add to `pkg/torznab/client_test.go`:

```go
// The caller owns rate limiting: indexarr shares one limiter per host across
// the caps probe, the search fan-out and the RSS poll, so all three draw on a
// single budget. An option that constructs its own limiter internally makes
// that impossible -- each client would get its own allowance and the host
// would see three times the intended rate.
func TestWithRateLimitUsesTheCallersLimiter(t *testing.T) {
	var calls int
	lim := ratelimit.NewLimiter(rate.Every(time.Hour), 1) // one token, then block
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, capsXML)
	}))
	defer srv.Close()

	c, err := torznab.New(srv.URL, "apikey", torznab.WithRateLimit(lim))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = c.Caps(ctx)          // consumes the single token
	require.NoError(t, err)

	_, err = c.Caps(ctx)          // must block on the CALLER's limiter, then time out
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the second call did not wait on the caller's limiter")
	assert.Equal(t, 1, calls, "the second request reached the server despite an exhausted limiter")
}
```

- [ ] **Step 2: Run it and watch it fail to compile**

```bash
go test -count=1 -run TestWithRateLimitUsesTheCallersLimiter ./pkg/torznab/
```

Expected: a **compile error**, not an assertion failure — `WithRateLimit` currently takes `(rate.Limit, int)`, so passing a `ratelimit.Limiter` does not type-check. That is the defect: the signature makes the correct usage inexpressible.

- [ ] **Step 3: Reshape the option**

Change the option to take `ratelimit.Limiter` and store it directly. Delete the internal `rate.NewLimiter` construction. Keep the client's behaviour when **no** option is passed exactly as it is — no limiter, no waiting — because `CLAUDE.md` is explicit that a library "never defaults one on".

- [ ] **Step 4: Run the test and the whole package**

```bash
go test -count=1 -race ./pkg/torznab/
```

Expected: PASS, including every pre-existing test. Any call site inside the package's own tests that passed `(rate.Limit, int)` is updated in this step.

- [ ] **Step 5: Fix the stale doc reference**

`pkg/torznab`'s package doc cites `Indexer.spec.rateLimit`. **That field does not exist** — the CRD has `spec.requestDelay` and `spec.limits` (`api/index/v1alpha1/indexer_types.go`). Correct it to name the real fields, and say which one the caller is expected to derive the limiter from. A doc comment that names a non-existent field sends the next reader looking for it.

- [ ] **Step 6: Commit**

```bash
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "fix(torznab): let WithRateLimit take the caller's limiter

The option built a private *rate.Limiter internally, so a caller could not
share one budget across several clients -- which is the whole point of the
project's rule that the caller owns rate limiting. indexarr needs one
limiter per indexer host, shared by the caps probe, the search fan-out and
the RSS poll; with the old signature each of the three would have got its
own allowance and the host would have seen three times the intended rate.

Also fixes the package doc, which cited Indexer.spec.rateLimit -- a field
that does not exist. The CRD has spec.requestDelay and spec.limits." -- pkg/torznab
```

**Done when:** `WithRateLimit` takes `ratelimit.Limiter`, the new test passes and genuinely fails against the old signature, no default limiter is introduced, the package doc names real CRD fields, and `go test -race ./pkg/torznab/` is green.
### Task D1-2: `pkg/relindex` — the SQLite FTS5 release index

**Files:**
- Create: `pkg/relindex/doc.go`
- Create: `pkg/relindex/store.go`
- Create: `pkg/relindex/schema.go`
- Create: `pkg/relindex/upsert.go`
- Create: `pkg/relindex/fts.go`
- Create: `pkg/relindex/search.go`
- Create: `pkg/relindex/maint.go`
- Test: `pkg/relindex/helpers_test.go`
- Test: `pkg/relindex/open_test.go`
- Test: `pkg/relindex/schema_test.go`
- Test: `pkg/relindex/upsert_test.go`
- Test: `pkg/relindex/fts_test.go`
- Test: `pkg/relindex/search_test.go`
- Test: `pkg/relindex/maint_test.go`
- Test: `pkg/relindex/concurrency_test.go`

**Path ownership:** `pkg/relindex/` and nothing else. Do **not** touch `go.mod`,
`go.sum`, `indexarr/`, `api/` or `config/`. Task D1-0 has already added
`modernc.org/sqlite` in a single serial `go get`; if it is missing, stop and tell
the controller rather than running `go get` yourself (`CLAUDE.md`: parallel
`go get` corrupts `go.mod`).

---

**Interfaces — Consumes:**

- `modernc.org/sqlite` — pure-Go driver, registered under the `database/sql`
  driver name `"sqlite"`. Added to `go.mod` by Task D1-0. Ruling **R8**: a cgo
  driver (`mattn/go-sqlite3`) will not link into the distroless-static image,
  so this driver is not negotiable.
- The standard library only otherwise: `database/sql`, `encoding/json`, `os`,
  `sync`, `time`, `unicode`. **`pkg/relindex` imports nothing from
  `github.com/mediactl/clustarr`.** It is a leaf package. In particular it does
  **not** import `pkg/events/schema` — `Release.InfoJSON` is opaque bytes the
  caller marshals, which is what keeps the store swappable per ADR-0003.
- The file path is supplied by the caller. `pkg/relindex` never hardcodes
  `/var/lib/clustarr/index/releases.db`; that constant is
  `indexarr.DefaultIndexPath`, corrected by Ruling **R1** in Task D1-0, and
  overridden in-cluster by `CLUSTARR_INDEX_PATH`
  (`config/manager/indexarr.yaml:84-85`). Every test in this task uses
  `t.TempDir()`.

**Interfaces — Produces:** reproduced verbatim from `02-interfaces.md`.
**ADR-0003 fixes `Store` at exactly four methods. Do not widen it.**

```go
// ---- D1-2 owns: pkg/relindex ----
// ADR-0003 fixes this interface at exactly four methods. Do not widen it.
package relindex

type Store interface {
    Upsert(ctx context.Context, rels []Release) (inserted int, err error)
    Search(ctx context.Context, q Query) ([]Release, error)
    Prune(ctx context.Context, olderThan time.Time) (deleted int, err error)
    Stats(ctx context.Context) (Stats, error)
}

// Open returns a Store backed by SQLite FTS5 at path, creating the schema if
// absent. It is the ONLY constructor; callers never touch database/sql.
func Open(ctx context.Context, path string) (Store, io.Closer, error)

type Release struct {
    Indexer     string    // the Indexer CR's name -- half of UNIQUE(indexer, guid)
    GUID        string    // the indexer's own id -- the other half
    Title       string    // raw title, exactly as the indexer returned it
    TitleNorm   string    // lowercased/normalised, the FTS5 column
    Group       string    // release group, the second FTS5 column
    Protocol    string    // "torrent" | "usenet"
    Categories  []int     // newznab category ids
    SizeBytes   int64
    PublishedAt *time.Time // nil when the indexer reported none -- NEVER backfill
    FetchedAt   time.Time
    InfoJSON    []byte     // the full schema.Release, for replay without re-query
}

type Query struct {
    Text       string // FTS5 MATCH against title_norm and grp; empty means "no text filter"
    Indexers   []string
    Categories []int
    Protocol   string
    Since      *time.Time
    Limit      int // caller-supplied; the store never invents one
}

type Stats struct {
    Releases   int64
    Indexers   int64
    OldestSeen time.Time
    NewestSeen time.Time
    SizeBytes  int64 // on-disk size of the database file
}
```

Additionally exported (sentinels, so `errors.Is` works through `Unwrap` — the
`pkg/` convention in `CLAUDE.md`):

```go
var (
    ErrInvalidPath    = errors.New("relindex: invalid database path")
    ErrNoFTS5         = errors.New("relindex: sqlite build has no FTS5")
    ErrSchemaTooNew   = errors.New("relindex: database schema is newer than this build")
    ErrInvalidRelease = errors.New("relindex: invalid release")
    ErrInvalidArg     = errors.New("relindex: invalid argument")
)
```

**Contract notes the rest of D1 must honour** (say these out loud in code
comments; they are the seams where a sibling task will otherwise guess):

1. **Normalisation is the caller's job, on both sides.** ADR-0003: "FTS5
   tokenisation is not release-title-aware out of the box, so title
   normalisation happens before insert." `relindex` stores `TitleNorm` exactly as
   given and escapes `Query.Text` without normalising it. D1-5 and D1-7 must
   therefore run `Query.Text` and `Release.TitleNorm` through the **same**
   function (`release.CleanTitle`), or a query will never match an indexed row.
2. **`Query.Since` filters on `fetched_at`, not `published_at`.** A release with
   no publish date must never vanish from a `Since` window — that is the same
   defect class as the Phase C `PublishedAt` backfill. Pinned by a test.
3. **`Query.Limit <= 0` emits no `LIMIT` clause at all.** The contract says the
   store never invents one. D1-5 always passes `schema.MaxSearchReleases` (500).
4. **Readiness (spec §6.2: "readiness tied to the DB open") is a `Stats` call.**
   `Stats` is the cheapest of the four methods and touches the file, so D1-8
   wires `/readyz` to it. This is why the interface does not need a fifth
   `Ping` method.
5. **`Prune` starts no goroutine.** The 10-minute sweep (spec §6.2) is scheduled
   by D1-8 in `run.go`; `Open` must not spawn anything, or every test and every
   e2e gets a background writer it did not ask for.

---

- [ ] **Step 1: Create the package doc, `pkg/relindex/doc.go`.**

  Every Go file in this repo starts with the GPL-3.0 header from
  `hack/boilerplate.go.txt`. This is its full text; later steps abbreviate it to
  `// <GPL-3.0 header from hack/boilerplate.go.txt>` — paste the real thing.

  ```go
  /*
  Copyright 2026 The Clustarr Authors.

  This program is free software: you can redistribute it and/or modify
  it under the terms of the GNU General Public License as published by
  the Free Software Foundation, either version 3 of the License, or
  (at your option) any later version.

  This program is distributed in the hope that it will be useful,
  but WITHOUT ANY WARRANTY; without even the implied warranty of
  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
  GNU General Public License for more details.

  You should have received a copy of the GNU General Public License
  along with this program.  If not, see <https://www.gnu.org/licenses/>.
  */

  // Package relindex is the local release index: every release indexarr sees,
  // from an RSS poll or a federated search, in a SQLite database with an FTS5
  // virtual table over the normalised title and the release group.
  //
  // It is a cache, not a system of record (ADR-0003). Losing the file costs a
  // re-sync and nothing else, which is what makes a single-writer, single-volume
  // design acceptable and why there are no backups.
  //
  // # Shape
  //
  // One content table, `releases`, keyed UNIQUE(indexer, guid); one external-
  // content FTS5 table, `releases_fts`, over `title_norm` and `grp`, kept in
  // sync by three triggers. The engine is SQLite through the pure-Go
  // modernc.org/sqlite driver: the indexarr image is distroless-static and a cgo
  // driver will not link (ADR-0003, Ruling R8).
  //
  // # Deployment
  //
  // The file lives on the RWO PVC `clustarr-index`, mounted at
  // /var/lib/clustarr/index, at /var/lib/clustarr/index/releases.db. indexarr is
  // pinned to exactly one replica with a Recreate strategy precisely so there is
  // exactly one writer process; SQLite plus WAL is safe for one writer and many
  // readers, and is not safe for two pods. This package takes the path as an
  // argument and hardcodes nothing.
  //
  // # What this package does not do
  //
  // It does not normalise titles -- the caller supplies Release.TitleNorm and
  // must normalise Query.Text with the same function, or nothing will match. It
  // does not schedule the retention sweep -- Prune is a pure function of its
  // olderThan argument and Open starts no goroutines. It does not know what is
  // inside Release.InfoJSON. It does not invent a query limit.
  package relindex
  ```

  Run `gofmt -l pkg/relindex` and expect no output.

- [ ] **Step 2: Declare the types and the interface in `pkg/relindex/store.go`, with no implementation yet.**

  This step must compile and do nothing else. `Open` is a stub that returns an
  error, so Step 3's test fails for a reason about behaviour rather than about a
  missing symbol.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"context"
  	"errors"
  	"io"
  	"time"
  )

  // Sentinel errors. Callers use errors.Is; every returned error wraps one of
  // these where the cause is one of these.
  var (
  	// ErrInvalidPath means the database path is empty or contains a
  	// character that cannot survive the SQLite DSN.
  	ErrInvalidPath = errors.New("relindex: invalid database path")

  	// ErrNoFTS5 means the SQLite build behind the driver has no FTS5
  	// module, so the index cannot be created.
  	ErrNoFTS5 = errors.New("relindex: sqlite build has no FTS5")

  	// ErrSchemaTooNew means the database on disk was written by a newer
  	// build. Opening it read-write would corrupt it, so Open refuses.
  	ErrSchemaTooNew = errors.New("relindex: database schema is newer than this build")

  	// ErrInvalidRelease means a Release in an Upsert batch cannot be
  	// stored. The whole batch is rejected; nothing is written.
  	ErrInvalidRelease = errors.New("relindex: invalid release")

  	// ErrInvalidArg means a method argument is unusable.
  	ErrInvalidArg = errors.New("relindex: invalid argument")
  )

  // Store is the release index. ADR-0003 fixes it at exactly four methods so the
  // engine stays swappable -- the documented scale-out path is a Postgres FTS
  // implementation of this same interface, with no caller changes. Do not widen
  // it.
  type Store interface {
  	// Upsert writes rels in one transaction, keyed UNIQUE(indexer, guid).
  	// inserted counts only rows that did not already exist. On any error the
  	// transaction rolls back and inserted is 0.
  	Upsert(ctx context.Context, rels []Release) (inserted int, err error)

  	// Search returns releases matching q, newest first, or best-ranked first
  	// when q.Text is set.
  	Search(ctx context.Context, q Query) ([]Release, error)

  	// Prune deletes every release fetched before olderThan. It is a pure
  	// function of its argument: it does not read a clock and it schedules
  	// nothing.
  	Prune(ctx context.Context, olderThan time.Time) (deleted int, err error)

  	// Stats reports corpus size and on-disk footprint. It is also the
  	// readiness probe: it fails if the handle is no longer usable.
  	Stats(ctx context.Context) (Stats, error)
  }

  // Release is one indexed release.
  type Release struct {
  	// Indexer is the Indexer CR's name -- half of UNIQUE(indexer, guid).
  	Indexer string

  	// GUID is the indexer's own id -- the other half.
  	GUID string

  	// Title is the raw title, exactly as the indexer returned it.
  	Title string

  	// TitleNorm is the lowercased/normalised title, the FTS5 column. The
  	// CALLER normalises: this package stores what it is given. Query.Text
  	// must be normalised with the same function or nothing will match.
  	TitleNorm string

  	// Group is the release group, the second FTS5 column. Its SQL column is
  	// `grp`, because `group` is a reserved word.
  	Group string

  	// Protocol is "torrent" or "usenet".
  	Protocol string

  	// Categories are newznab category ids.
  	Categories []int

  	// SizeBytes is the release size.
  	SizeBytes int64

  	// PublishedAt is nil when the indexer reported none. NEVER backfill it.
  	//
  	// api/common/v1alpha1.ReleaseInfo.PublishedAt carries the Phase C
  	// post-mortem at length: substituting "now" makes a dateless release
  	// sort as brand new and substituting the zero time makes it sort as
  	// ancient, and neither is true. This package stores NULL and reads it
  	// back as nil, and Upsert rejects a non-nil pointer to the zero time so
  	// a caller cannot smuggle the lie past it.
  	PublishedAt *time.Time

  	// FetchedAt is when indexarr read the release. Always set; it is what
  	// Prune and Query.Since work on.
  	FetchedAt time.Time

  	// InfoJSON is the full schema.Release, for replay without re-query. This
  	// package treats it as opaque bytes and never unmarshals it.
  	InfoJSON []byte
  }

  // Query selects releases. Every field is optional; the zero Query returns the
  // whole corpus, newest first, unlimited.
  type Query struct {
  	// Text is an FTS5 MATCH against title_norm and grp; empty means "no text
  	// filter". It is attacker-controlled -- it reaches here from third-party
  	// indexer titles and from user input -- and is escaped into a safe MATCH
  	// expression before use. See fts.go.
  	Text string

  	// Indexers restricts the search to these Indexer names.
  	Indexers []string

  	// Categories matches a release carrying ANY of these newznab ids.
  	Categories []int

  	// Protocol restricts to "torrent" or "usenet".
  	Protocol string

  	// Since restricts to releases FETCHED at or after this instant -- not
  	// published. A release whose indexer reported no publish date must never
  	// disappear from a window, and fetched_at is the only column that is
  	// always present.
  	Since *time.Time

  	// Limit is caller-supplied; the store never invents one. Zero or
  	// negative emits no LIMIT clause at all.
  	Limit int
  }

  // Stats is the corpus summary.
  type Stats struct {
  	// Releases is the row count.
  	Releases int64

  	// Indexers is the number of distinct indexers with at least one row.
  	Indexers int64

  	// OldestSeen is the earliest fetched_at, or the zero time when empty.
  	OldestSeen time.Time

  	// NewestSeen is the latest fetched_at, or the zero time when empty.
  	NewestSeen time.Time

  	// SizeBytes is the on-disk size of the database file plus its
  	// write-ahead log.
  	SizeBytes int64
  }

  // Open returns a Store backed by SQLite FTS5 at path, creating the schema if
  // absent. It is the ONLY constructor; callers never touch database/sql.
  //
  // It starts no goroutines. The 10-minute retention sweep spec §6.2 requires is
  // scheduled by the caller around Prune.
  func Open(ctx context.Context, path string) (Store, io.Closer, error) {
  	return nil, nil, errors.New("relindex: Open not implemented")
  }
  ```

  Run `go build ./pkg/relindex/...` and expect it to succeed with no output.
  (`go build` with a package argument writes nothing to the repo root —
  never run a bare `go build`.)

- [ ] **Step 3: Write the shared test fixtures, `pkg/relindex/helpers_test.go`.**

  Every test uses `t.TempDir()`, never the PVC path. Note the fixed timestamps:
  `time.Now()` carries a monotonic reading that `require.Equal` compares, so a
  round-trip through int64 nanoseconds would spuriously fail against it.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"context"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  // Fixed instants. These are deliberately not time.Now(): a time.Time from
  // time.Now() carries a monotonic reading, require.Equal compares it, and a
  // value that has round-tripped through an int64 of Unix nanoseconds has lost
  // it. Nanosecond components are non-zero so a truncating storage bug shows up.
  var (
  	fetchedAt   = time.Date(2026, 9, 19, 12, 0, 0, 123456789, time.UTC)
  	publishedAt = time.Date(2026, 9, 18, 3, 30, 0, 987654321, time.UTC)
  )

  // newStore opens a store in a fresh temp directory and closes it on cleanup.
  func newStore(t *testing.T) relindex.Store {
  	t.Helper()
  	s, closer, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "releases.db"))
  	require.NoError(t, err)
  	t.Cleanup(func() { require.NoError(t, closer.Close()) })
  	return s
  }

  // newStoreAt opens a store at an explicit path and closes it on cleanup.
  func newStoreAt(t *testing.T, path string) relindex.Store {
  	t.Helper()
  	s, closer, err := relindex.Open(t.Context(), path)
  	require.NoError(t, err)
  	t.Cleanup(func() { require.NoError(t, closer.Close()) })
  	return s
  }

  // rel builds a minimally valid Release.
  func rel(indexer, guid, title string) relindex.Release {
  	return relindex.Release{
  		Indexer:     indexer,
  		GUID:        guid,
  		Title:       title,
  		TitleNorm:   title,
  		Group:       "NTb",
  		Protocol:    "torrent",
  		Categories:  []int{2000, 2040},
  		SizeBytes:   1 << 30,
  		PublishedAt: &publishedAt,
  		FetchedAt:   fetchedAt,
  		InfoJSON:    []byte(`{"info":{"guid":"` + guid + `"}}`),
  	}
  }

  // mustUpsert upserts and asserts the inserted count.
  func mustUpsert(t *testing.T, ctx context.Context, s relindex.Store, want int, rels ...relindex.Release) {
  	t.Helper()
  	got, err := s.Upsert(ctx, rels)
  	require.NoError(t, err)
  	require.Equal(t, want, got)
  }
  ```

- [ ] **Step 4: Write the failing open test, `pkg/relindex/open_test.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"path/filepath"
  	"testing"

  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  func TestOpenCreatesTheDatabaseFileAndItsParentDirectory(t *testing.T) {
  	// The PVC mounts at /var/lib/clustarr/index and the database sits
  	// directly in it, but a dev box running `clustarr all` has neither, and
  	// a first boot must not need a shell.
  	path := filepath.Join(t.TempDir(), "index", "releases.db")
  	s, closer, err := relindex.Open(t.Context(), path)
  	require.NoError(t, err)
  	t.Cleanup(func() { require.NoError(t, closer.Close()) })
  	require.NotNil(t, s)
  	require.FileExists(t, path)
  }

  func TestOpenIsUsableImmediately(t *testing.T) {
  	// sql.Open is lazy: it validates nothing and connects to nothing. If
  	// Open does not ping, a broken path surfaces on the first Upsert
  	// instead, long after readiness said the service was up (spec §6.2 ties
  	// readiness to the DB being open).
  	s := newStore(t)
  	st, err := s.Stats(t.Context())
  	require.NoError(t, err)
  	require.Zero(t, st.Releases)
  }

  func TestOpenRejectsAnEmptyPath(t *testing.T) {
  	_, _, err := relindex.Open(t.Context(), "")
  	require.ErrorIs(t, err, relindex.ErrInvalidPath)
  }

  func TestOpenRejectsAPathThatWouldCorruptTheDSN(t *testing.T) {
  	// The DSN is "file:<path>?<params>". A '?' or '#' in the path would be
  	// parsed as the start of the query or fragment and silently point the
  	// driver at a different file.
  	_, _, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "rel?eases.db"))
  	require.ErrorIs(t, err, relindex.ErrInvalidPath)
  }

  func TestOpenReopensAnExistingDatabaseWithItsRows(t *testing.T) {
  	path := filepath.Join(t.TempDir(), "releases.db")

  	first, closer, err := relindex.Open(t.Context(), path)
  	require.NoError(t, err)
  	n, err := first.Upsert(t.Context(), []relindex.Release{rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264")})
  	require.NoError(t, err)
  	require.Equal(t, 1, n)
  	require.NoError(t, closer.Close())

  	second := newStoreAt(t, path)
  	st, err := second.Stats(t.Context())
  	require.NoError(t, err)
  	require.EqualValues(t, 1, st.Releases)
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestOpen` and expect every case to
  fail with `relindex: Open not implemented`.

- [ ] **Step 5: Write the schema DDL and the version ladder, `pkg/relindex/schema.go`.**

  Read the comments — the migration story and the external-content trigger
  contract are the two things a later change gets wrong.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"context"
  	"database/sql"
  	"fmt"
  )

  // schemaVersion is the DDL revision recorded in SQLite's `user_version`
  // header field. A database carrying this value is up to date.
  //
  // To change the schema: append a new []string to migrations, bump this
  // constant, and NEVER edit ddlV1 in place. Every PVC in the field already has
  // ddlV1 applied; editing it means new databases and old databases diverge with
  // the same recorded version, which is the corruption this ladder exists to
  // prevent.
  const schemaVersion = 1

  // migrations[v] upgrades a database from user_version v to v+1.
  // migrations[0] therefore creates the whole schema from an empty file.
  //
  // user_version is 0 on a brand-new SQLite file, and this package has never
  // shipped an unversioned schema, so 0 unambiguously means "empty".
  var migrations = [][]string{
  	0: ddlV1,
  }

  // ddlV1 is the initial schema. Statements run in order, in one transaction.
  //
  // Each statement is executed separately rather than as one multi-statement
  // Exec: the driver's behaviour with multiple statements and its error
  // reporting are both better one at a time, and a failure names the statement.
  var ddlV1 = []string{
  	// The content table. `grp` rather than `group` because `group` is a SQL
  	// reserved word; spec §6.2 names the FTS columns `title_norm, grp` for
  	// exactly that reason.
  	//
  	// INTEGER PRIMARY KEY AUTOINCREMENT, not a bare INTEGER PRIMARY KEY: the
  	// FTS5 table is external-content keyed on this rowid, and AUTOINCREMENT
  	// guarantees a rowid freed by Prune is never handed to a later insert. A
  	// reused rowid plus one missed trigger is a silently wrong search result.
  	//
  	// published_at is nullable and fetched_at is not. That asymmetry is the
  	// whole PublishedAt contract expressed in DDL.
  	`CREATE TABLE IF NOT EXISTS releases (
  		id           INTEGER PRIMARY KEY AUTOINCREMENT,
  		indexer      TEXT    NOT NULL,
  		guid         TEXT    NOT NULL,
  		title        TEXT    NOT NULL,
  		title_norm   TEXT    NOT NULL,
  		grp          TEXT    NOT NULL DEFAULT '',
  		protocol     TEXT    NOT NULL DEFAULT '',
  		categories   TEXT    NOT NULL DEFAULT '[]',
  		size_bytes   INTEGER NOT NULL DEFAULT 0,
  		published_at INTEGER,
  		fetched_at   INTEGER NOT NULL,
  		info_json    BLOB,
  		UNIQUE(indexer, guid)
  	)`,

  	// Prune scans fetched_at; a no-text Search orders by it.
  	`CREATE INDEX IF NOT EXISTS releases_fetched_at ON releases(fetched_at)`,

  	// Query.Indexers combined with the default ordering.
  	`CREATE INDEX IF NOT EXISTS releases_indexer_fetched ON releases(indexer, fetched_at)`,

  	// External-content FTS5: the index stores only the inverted lists and
  	// reads column values back from `releases` through content_rowid. A
  	// contentless or self-contained table would duplicate every title.
  	//
  	// remove_diacritics 2 is the corrected fold; 1 is retained only for
  	// backward compatibility with databases built before SQLite 3.27 and
  	// mishandles codepoints that matter for European release titles.
  	`CREATE VIRTUAL TABLE IF NOT EXISTS releases_fts USING fts5(
  		title_norm,
  		grp,
  		content='releases',
  		content_rowid='id',
  		tokenize='unicode61 remove_diacritics 2'
  	)`,

  	// The three sync triggers. External content means FTS5 does NOT see
  	// writes to the content table; these are the only thing keeping the
  	// index true.
  	//
  	// TRAP: the 'delete' command must be given the OLD column values exactly
  	// as they were indexed. FTS5 subtracts the terms it is handed; hand it
  	// the new values, or omit a column, and it subtracts the wrong postings
  	// and the index rots silently -- no error, just wrong results, forever.
  	`CREATE TRIGGER IF NOT EXISTS releases_ai AFTER INSERT ON releases BEGIN
  		INSERT INTO releases_fts(rowid, title_norm, grp)
  		VALUES (new.id, new.title_norm, new.grp);
  	END`,

  	`CREATE TRIGGER IF NOT EXISTS releases_ad AFTER DELETE ON releases BEGIN
  		INSERT INTO releases_fts(releases_fts, rowid, title_norm, grp)
  		VALUES ('delete', old.id, old.title_norm, old.grp);
  	END`,

  	`CREATE TRIGGER IF NOT EXISTS releases_au AFTER UPDATE ON releases BEGIN
  		INSERT INTO releases_fts(releases_fts, rowid, title_norm, grp)
  		VALUES ('delete', old.id, old.title_norm, old.grp);
  		INSERT INTO releases_fts(rowid, title_norm, grp)
  		VALUES (new.id, new.title_norm, new.grp);
  	END`,
  }

  // checkFTS5 verifies the driver's SQLite build carries the FTS5 module, so the
  // failure is one clear sentence at Open rather than a confusing error from the
  // middle of the DDL.
  //
  // It runs on a pinned *sql.Conn, not on the *sql.DB. A `temp.` table belongs to
  // one connection; issued against the pool, the CREATE and the DROP can land on
  // different connections and the DROP fails for a reason that has nothing to do
  // with FTS5.
  func checkFTS5(ctx context.Context, db *sql.DB) error {
  	conn, err := db.Conn(ctx)
  	if err != nil {
  		return fmt.Errorf("relindex: acquire connection: %w", err)
  	}
  	defer func() { _ = conn.Close() }()

  	if _, err := conn.ExecContext(ctx, `CREATE VIRTUAL TABLE temp.relindex_fts5_probe USING fts5(x)`); err != nil {
  		return fmt.Errorf("%w: %v", ErrNoFTS5, err)
  	}
  	if _, err := conn.ExecContext(ctx, `DROP TABLE temp.relindex_fts5_probe`); err != nil {
  		return fmt.Errorf("relindex: drop fts5 probe: %w", err)
  	}
  	return nil
  }

  // migrate brings the database at db up to schemaVersion, or refuses to touch
  // it. The whole ladder plus the version stamp runs in one transaction, so a
  // crash mid-migration leaves the recorded version and the actual schema in
  // agreement.
  func migrate(ctx context.Context, db *sql.DB) error {
  	var have int
  	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&have); err != nil {
  		return fmt.Errorf("relindex: read user_version: %w", err)
  	}
  	switch {
  	case have > schemaVersion:
  		// Refuse rather than downgrade. A newer build may have added a
  		// column this build does not write; opening read-write and
  		// upserting would leave rows this build cannot read back.
  		return fmt.Errorf("%w: database is version %d, this build understands %d",
  			ErrSchemaTooNew, have, schemaVersion)
  	case have == schemaVersion:
  		return nil
  	}

  	tx, err := db.BeginTx(ctx, nil)
  	if err != nil {
  		return fmt.Errorf("relindex: begin migration: %w", err)
  	}
  	defer func() { _ = tx.Rollback() }()

  	for v := have; v < schemaVersion; v++ {
  		for _, stmt := range migrations[v] {
  			if _, err := tx.ExecContext(ctx, stmt); err != nil {
  				return fmt.Errorf("relindex: migration %d->%d failed on %.60q: %w", v, v+1, stmt, err)
  			}
  		}
  	}

  	// A PRAGMA value cannot be a bound parameter -- SQLite parses pragma
  	// arguments before statement parameters are bound -- so this one is
  	// formatted. schemaVersion is an untyped integer constant known at
  	// compile time, so there is no injection surface.
  	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
  		return fmt.Errorf("relindex: stamp user_version: %w", err)
  	}
  	return tx.Commit()
  }
  ```

  Run `go build ./pkg/relindex/...`; it compiles but nothing calls it yet.

- [ ] **Step 6: Implement `Open`, `sqliteStore` and `Close` in `pkg/relindex/store.go`.**

  Replace the stub `Open` with this, and add the imports and the struct. **Do
  not add the `_pragma` DSN parameters yet** — Step 8 adds them against a test
  that proves they were missing.

  ```go
  // add to the import block:
  //	"fmt"
  //	"os"
  //	"path/filepath"
  //	"strings"
  //	"sync"
  //	"database/sql"
  //
  //	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite" (ADR-0003, R8)

  // sqliteStore is the only Store implementation.
  //
  // Concurrency: indexarr is one process with two callers into this store -- the
  // RSS worker and the search service -- so "single writer" has to be enforced
  // inside the process, not just by the deployment.
  //
  //   - WAL gives one writer concurrent with many readers. Search and Stats take
  //     no lock and never block.
  //   - wmu serialises Upsert and Prune in Go. Without it, two goroutines racing
  //     to BEGIN would have SQLite arbitrate and return SQLITE_BUSY to the
  //     loser; with it, the loser waits on a mutex and the busy_timeout pragma
  //     is only ever a backstop for the WAL checkpointer.
  //   - Because wmu guarantees one writer, a deferred BEGIN is safe and there is
  //     no read-to-write upgrade deadlock to avoid. This deliberately does NOT
  //     rely on a driver-specific `_txlock=immediate` DSN parameter: correctness
  //     must survive the Store swap ADR-0003 anticipates.
  type sqliteStore struct {
  	db   *sql.DB
  	path string
  	wmu  sync.Mutex
  }

  // Open returns a Store backed by SQLite FTS5 at path, creating the schema if
  // absent. It is the ONLY constructor; callers never touch database/sql.
  //
  // It starts no goroutines. The 10-minute retention sweep spec §6.2 requires is
  // scheduled by the caller around Prune.
  func Open(ctx context.Context, path string) (Store, io.Closer, error) {
  	if strings.TrimSpace(path) == "" {
  		return nil, nil, fmt.Errorf("%w: path is empty", ErrInvalidPath)
  	}
  	// The DSN is "file:<path>?<params>". A '?' would start the query string
  	// and a '#' a fragment, so either silently redirects the driver.
  	if strings.ContainsAny(path, "?#") {
  		return nil, nil, fmt.Errorf("%w: %q contains '?' or '#'", ErrInvalidPath, path)
  	}
  	// The PVC mounts at the parent directory in-cluster, but `clustarr all`
  	// on a dev box has neither (Ruling R1), and a first boot must not need a
  	// shell.
  	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
  		return nil, nil, fmt.Errorf("relindex: create %s: %w", filepath.Dir(path), err)
  	}

  	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
  	if err != nil {
  		return nil, nil, fmt.Errorf("relindex: open %s: %w", path, err)
  	}

  	// sql.Open is lazy: it parses the DSN and connects to nothing. Ping now,
  	// so a broken path or volume fails here instead of on the first write --
  	// spec §6.2 ties readiness to the DB being open, and that is only true
  	// if Open actually opened it.
  	if err := db.PingContext(ctx); err != nil {
  		_ = db.Close()
  		return nil, nil, fmt.Errorf("relindex: ping %s: %w", path, err)
  	}
  	if err := checkFTS5(ctx, db); err != nil {
  		_ = db.Close()
  		return nil, nil, err
  	}
  	if err := migrate(ctx, db); err != nil {
  		_ = db.Close()
  		return nil, nil, err
  	}

  	s := &sqliteStore{db: db, path: path}
  	return s, s, nil
  }

  // Close checkpoints the write-ahead log into the main database and closes the
  // handle. Checkpointing on the way out keeps the -wal file off the PVC across
  // the Recreate rollouts §3 pins indexarr to. Skipping it would still be safe --
  // SQLite replays the WAL on the next open -- so the checkpoint error is joined
  // rather than swallowed or returned alone.
  func (s *sqliteStore) Close() error {
  	s.wmu.Lock()
  	defer s.wmu.Unlock()

  	var errs []error
  	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
  		errs = append(errs, fmt.Errorf("relindex: checkpoint wal: %w", err))
  	}
  	if err := s.db.Close(); err != nil {
  		errs = append(errs, fmt.Errorf("relindex: close %s: %w", s.path, err))
  	}
  	return errors.Join(errs...)
  }
  ```

  Add temporary stub methods so `*sqliteStore` satisfies `Store` and the package
  compiles; Steps 12, 22, 27 and 29 replace them.

  ```go
  func (s *sqliteStore) Upsert(ctx context.Context, rels []Release) (int, error) {
  	return 0, errors.New("relindex: Upsert not implemented")
  }

  func (s *sqliteStore) Search(ctx context.Context, q Query) ([]Release, error) {
  	return nil, errors.New("relindex: Search not implemented")
  }

  func (s *sqliteStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
  	return 0, errors.New("relindex: Prune not implemented")
  }

  func (s *sqliteStore) Stats(ctx context.Context) (Stats, error) {
  	return Stats{}, errors.New("relindex: Stats not implemented")
  }
  ```

- [ ] **Step 7: Implement `Stats` far enough to make Step 4's tests pass, in `pkg/relindex/maint.go`.**

  `TestOpenIsUsableImmediately` and `TestOpenReopensAnExistingDatabaseWithItsRows`
  both go through `Stats`. Delete the `Stats` stub from `store.go` and write the
  real one now; `Prune` stays a stub until Step 27.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"context"
  	"database/sql"
  	"fmt"
  	"os"
  	"time"
  )

  // Stats reports corpus size and on-disk footprint.
  //
  // It takes no write lock: WAL readers never block, and a count that is one
  // transaction stale is not worth serialising a writer for.
  func (s *sqliteStore) Stats(ctx context.Context) (Stats, error) {
  	var (
  		st             Stats
  		oldest, newest sql.NullInt64
  	)
  	const q = `SELECT COUNT(*), COUNT(DISTINCT indexer), MIN(fetched_at), MAX(fetched_at) FROM releases`
  	if err := s.db.QueryRowContext(ctx, q).Scan(&st.Releases, &st.Indexers, &oldest, &newest); err != nil {
  		return Stats{}, fmt.Errorf("relindex: stats: %w", err)
  	}
  	// MIN/MAX over zero rows are NULL, which is the zero time -- the Stats
  	// doc says so, and an empty index is the normal state at first boot.
  	if oldest.Valid {
  		st.OldestSeen = time.Unix(0, oldest.Int64).UTC()
  	}
  	if newest.Valid {
  		st.NewestSeen = time.Unix(0, newest.Int64).UTC()
  	}
  	st.SizeBytes = s.onDiskBytes()
  	return st, nil
  }

  // onDiskBytes sums the main database file and its write-ahead log, which is
  // what actually consumes the 5Gi PVC. The -shm file is excluded: it is a
  // fixed-size shared-memory mapping, not stored data.
  //
  // A missing file counts as zero rather than failing: Stats is the readiness
  // probe and feeds a gauge, and a stat() race must not flap readiness.
  func (s *sqliteStore) onDiskBytes() int64 {
  	var total int64
  	for _, p := range []string{s.path, s.path + "-wal"} {
  		if fi, err := os.Stat(p); err == nil {
  			total += fi.Size()
  		}
  	}
  	return total
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestOpen`. All five cases pass
  except `TestOpenReopensAnExistingDatabaseWithItsRows`, which still fails with
  `relindex: Upsert not implemented`. Comment that one case out with a
  `// TODO(D1-2): un-skip after Step 12` marker — or better, add `t.Skip("Upsert
  lands in Step 12")` as its first line, and delete the skip in Step 12.

- [ ] **Step 8: Write the failing PRAGMA test in `pkg/relindex/open_test.go`.**

  This is the step that proves the connection settings are real. It reaches into
  the store through a second, independent handle so it tests the *file*, and it
  asserts the pragma on a pooled connection the store itself opened.

  Append to `open_test.go`:

  ```go
  func TestOpenPutsTheDatabaseInWALMode(t *testing.T) {
  	// Spec §6.2 requires WAL. WAL is what lets Search read while the RSS
  	// worker writes; in the default rollback-journal mode a writer takes an
  	// exclusive lock and every concurrent search blocks.
  	//
  	// journal_mode is persisted in the file header, so a second handle sees
  	// it -- which is exactly why this asserts through a second handle rather
  	// than through the one that set it.
  	path := filepath.Join(t.TempDir(), "releases.db")
  	newStoreAt(t, path)

  	db, err := sql.Open("sqlite", "file:"+path)
  	require.NoError(t, err)
  	t.Cleanup(func() { require.NoError(t, db.Close()) })

  	var mode string
  	require.NoError(t, db.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
  	require.Equal(t, "wal", strings.ToLower(mode))
  }

  func TestOpenAppliesItsPragmasToEveryPooledConnection(t *testing.T) {
  	// TRAP: a PRAGMA executed once via db.Exec lands on ONE arbitrary
  	// connection from the pool. database/sql opens more on demand, and those
  	// get the defaults -- so busy_timeout is set on connection 1 and the
  	// search running on connection 3 still returns SQLITE_BUSY instantly.
  	// The modernc driver's `_pragma=` DSN parameters run on every new
  	// connection, which is the only correct place for them.
  	//
  	// Forcing four concurrent queries forces four connections open.
  	s := newStore(t)

  	var wg sync.WaitGroup
  	for range 8 {
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			_, err := s.Stats(t.Context())
  			assert.NoError(t, err)
  		}()
  	}
  	wg.Wait()

  	inner, ok := relindex.ExportedForTestDB(s)
  	require.True(t, ok)

  	// Every connection the pool holds must report the same settings.
  	for range 8 {
  		var (
  			busy int
  			sync string
  			temp int
  		)
  		require.NoError(t, inner.QueryRow(`PRAGMA busy_timeout`).Scan(&busy))
  		require.NoError(t, inner.QueryRow(`PRAGMA synchronous`).Scan(&sync))
  		require.NoError(t, inner.QueryRow(`PRAGMA temp_store`).Scan(&temp))
  		require.Equal(t, 5000, busy, "busy_timeout")
  		require.Equal(t, "1", sync, "synchronous should be NORMAL(1)")
  		require.Equal(t, 2, temp, "temp_store should be MEMORY(2)")
  	}
  }
  ```

  Add the imports `database/sql`, `strings`, `sync` and
  `github.com/stretchr/testify/assert`, plus the blank import
  `_ "modernc.org/sqlite"`.

  `ExportedForTestDB` does not exist yet. Add it to a new in-package file so it
  is not part of the public API surface a caller sees — put it at the bottom of
  `pkg/relindex/store.go` guarded by a comment:

  ```go
  // ExportedForTestDB exposes the underlying handle to this package's tests.
  // It is deliberately not part of Store: ADR-0003 fixes that at four methods.
  // Nothing outside pkg/relindex may call it.
  func ExportedForTestDB(s Store) (*sql.DB, bool) {
  	impl, ok := s.(*sqliteStore)
  	if !ok {
  		return nil, false
  	}
  	return impl.db, true
  }
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestOpenPutsTheDatabase|TestOpenApplies'`.
  Expect `TestOpenPutsTheDatabaseInWALMode` to fail with
  `Not equal: expected: "wal" actual: "delete"`, and
  `TestOpenAppliesItsPragmasToEveryPooledConnection` to fail on `busy_timeout`
  with `expected: 5000 actual: 0`.

- [ ] **Step 9: Add the DSN pragmas and the pool limits in `pkg/relindex/store.go`.**

  Replace the `sql.Open` call in `Open` with this block.

  ```go
  	// modernc.org/sqlite applies each `_pragma=` parameter to EVERY new
  	// connection in the pool. Executing these as statements after opening
  	// would set them on one connection only -- see the test.
  	//
  	//	journal_mode(WAL)   spec §6.2. One writer concurrent with many
  	//	                    readers; the alternative blocks every search for
  	//	                    the duration of an RSS batch.
  	//	busy_timeout(5000)  SQLite returns SQLITE_BUSY immediately by
  	//	                    default. The Go-side write mutex removes
  	//	                    in-process contention, so this is the backstop
  	//	                    for the WAL checkpointer, not the main path.
  	//	synchronous(NORMAL) With WAL this is the documented safe pairing: a
  	//	                    power loss can lose the last transactions but
  	//	                    cannot corrupt the file. ADR-0003 calls the index
  	//	                    a cache and says backups are unnecessary, so
  	//	                    FULL's fsync per commit buys nothing.
  	//	temp_store(MEMORY)  The container runs readOnlyRootFilesystem: true
  	//	                    with only an emptyDir at /tmp. Keeping FTS5 merges
  	//	                    and ORDER BY sorts in memory removes any
  	//	                    dependence on a writable temp directory at all.
  	const dsnParams = "_pragma=journal_mode(WAL)" +
  		"&_pragma=busy_timeout(5000)" +
  		"&_pragma=synchronous(NORMAL)" +
  		"&_pragma=temp_store(MEMORY)"

  	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+dsnParams)
  	if err != nil {
  		return nil, nil, fmt.Errorf("relindex: open %s: %w", path, err)
  	}

  	// Readers are cheap and never block under WAL; writers are serialised by
  	// wmu, so a large pool costs nothing and a small one throttles search
  	// behind an RSS batch. SetConnMaxLifetime stays 0: recycling a
  	// connection only re-runs the pragmas for no benefit on a local file.
  	db.SetMaxOpenConns(8)
  	db.SetMaxIdleConns(8)
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestOpen'` and expect all of them
  to pass except the skipped reopen case.

- [ ] **Step 10: Commit the open path.**

  ```
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit \
    -m "feat(relindex): open a WAL SQLite FTS5 index with a versioned schema" \
    -- pkg/relindex
  ```

- [ ] **Step 11: Write the failing schema-version tests, `pkg/relindex/schema_test.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"database/sql"
  	"path/filepath"
  	"testing"

  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  func TestOpenStampsTheSchemaVersion(t *testing.T) {
  	// A database with no recorded version cannot be migrated later; it can
  	// only be guessed at. Pinning the value here means a schema change that
  	// forgets to bump it fails loudly in this test instead of quietly in the
  	// field.
  	path := filepath.Join(t.TempDir(), "releases.db")
  	newStoreAt(t, path)

  	db, err := sql.Open("sqlite", "file:"+path)
  	require.NoError(t, err)
  	t.Cleanup(func() { require.NoError(t, db.Close()) })

  	var v int
  	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&v))
  	require.Equal(t, 1, v)
  }

  func TestOpenRefusesADatabaseWrittenByANewerBuild(t *testing.T) {
  	// Opening it read-write and upserting would write rows a newer build
  	// cannot read back. Refusing turns a data-loss bug into a crash-loop
  	// with one clear message.
  	path := filepath.Join(t.TempDir(), "releases.db")
  	newStoreAt(t, path)

  	db, err := sql.Open("sqlite", "file:"+path)
  	require.NoError(t, err)
  	_, err = db.Exec(`PRAGMA user_version = 999`)
  	require.NoError(t, err)
  	require.NoError(t, db.Close())

  	_, _, err = relindex.Open(t.Context(), path)
  	require.ErrorIs(t, err, relindex.ErrSchemaTooNew)
  	require.Contains(t, err.Error(), "version 999")
  }

  func TestOpenOnAnExistingDatabaseIsIdempotent(t *testing.T) {
  	// Every restart of the single indexarr replica re-runs Open against a
  	// populated PVC. It must be a no-op, not a re-migration.
  	path := filepath.Join(t.TempDir(), "releases.db")
  	for range 3 {
  		s, closer, err := relindex.Open(t.Context(), path)
  		require.NoError(t, err)
  		_, err = s.Stats(t.Context())
  		require.NoError(t, err)
  		require.NoError(t, closer.Close())
  	}
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestOpenStamps -run TestOpenRefuses -run TestOpenOnAnExisting`.
  Expect all three to **pass immediately** — Step 5 already wrote the ladder.
  That is fine: these are regression pins for a mechanism that has no observable
  behaviour until someone changes it. Note it in the commit message so a reviewer
  does not look for a red-to-green transition that never happened.

- [ ] **Step 12: Write the failing upsert tests, `pkg/relindex/upsert_test.go`.**

  Delete the `t.Skip` added in Step 7 from
  `TestOpenReopensAnExistingDatabaseWithItsRows` in the same edit.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"testing"

  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  func TestUpsertReportsOnlyGenuineInserts(t *testing.T) {
  	s := newStore(t)
  	ctx := t.Context()

  	n, err := s.Upsert(ctx, []relindex.Release{
  		rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb"),
  		rel("nzbgeek", "g2", "The Matrix Reloaded 2003 1080p BluRay x264-NTb"),
  	})
  	require.NoError(t, err)
  	require.Equal(t, 2, n)

  	st, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.EqualValues(t, 2, st.Releases)
  }

  func TestUpsertOnAnEmptyBatchIsANoOp(t *testing.T) {
  	s := newStore(t)
  	n, err := s.Upsert(t.Context(), nil)
  	require.NoError(t, err)
  	require.Zero(t, n)
  }

  func TestUpsertScopesUniquenessToTheIndexer(t *testing.T) {
  	// The key is (indexer, guid). Two indexers reusing the same guid string
  	// -- which they do; "12345" is a popular guid -- are two distinct rows.
  	s := newStore(t)
  	mustUpsert(t, t.Context(), s, 2,
  		rel("nzbgeek", "12345", "The Matrix 1999"),
  		rel("drunkenslug", "12345", "Dune 2021"),
  	)
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestUpsert` and expect every case
  to fail with `relindex: Upsert not implemented`.

- [ ] **Step 13: Write the value-mapping helpers in `pkg/relindex/upsert.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"context"
  	"database/sql"
  	"encoding/json"
  	"fmt"
  	"time"
  )

  // Times are stored as INTEGER Unix nanoseconds, UTC. Nanoseconds because the
  // column has to order exactly and a truncation to seconds would collapse the
  // ordering of a burst of RSS rows fetched in the same second.
  //
  // nullNanos is the one place the PublishedAt contract is enforced on the way
  // in: nil becomes SQL NULL, and nothing else.
  func nullNanos(t *time.Time) sql.NullInt64 {
  	if t == nil {
  		return sql.NullInt64{}
  	}
  	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
  }

  // timeFromNull is the inverse, and the one place the contract is enforced on
  // the way out: SQL NULL becomes nil, never the zero time and never now.
  func timeFromNull(n sql.NullInt64) *time.Time {
  	if !n.Valid {
  		return nil
  	}
  	t := time.Unix(0, n.Int64).UTC()
  	return &t
  }

  // marshalCategories stores newznab ids as a JSON array so Search can filter
  // with json_each without a join table. An absent or empty list is stored as
  // "[]", never as JSON null, so json_each always has something to iterate.
  func marshalCategories(cats []int) (string, error) {
  	if len(cats) == 0 {
  		return "[]", nil
  	}
  	b, err := json.Marshal(cats)
  	if err != nil {
  		return "", fmt.Errorf("relindex: marshal categories: %w", err)
  	}
  	return string(b), nil
  }

  // unmarshalCategories is the inverse. An empty array comes back as nil, not as
  // an empty slice, so a Release round-trips equal to one built with a nil
  // Categories field.
  func unmarshalCategories(s string) ([]int, error) {
  	var cats []int
  	if err := json.Unmarshal([]byte(s), &cats); err != nil {
  		return nil, fmt.Errorf("relindex: unmarshal categories %q: %w", s, err)
  	}
  	if len(cats) == 0 {
  		return nil, nil
  	}
  	return cats, nil
  }

  // validate rejects a Release that cannot be stored truthfully.
  func validate(r Release) error {
  	switch {
  	case r.Indexer == "":
  		return fmt.Errorf("%w: Indexer is empty", ErrInvalidRelease)
  	case r.GUID == "":
  		return fmt.Errorf("%w: GUID is empty", ErrInvalidRelease)
  	case r.TitleNorm == "":
  		// An empty normalised title indexes nothing, so the row would be
  		// invisible to every text search while still counting in Stats.
  		return fmt.Errorf("%w: TitleNorm is empty", ErrInvalidRelease)
  	case r.FetchedAt.IsZero():
  		// Prune and Query.Since both work on fetched_at, and the zero
  		// time's UnixNano overflows int64 into a large negative number --
  		// so a zero FetchedAt is both meaningless and actively wrong.
  		return fmt.Errorf("%w: FetchedAt is zero", ErrInvalidRelease)
  	case r.PublishedAt != nil && r.PublishedAt.IsZero():
  		// A caller with no publish date MUST send nil. A pointer to the
  		// zero time is the Phase C defect in a new costume: it persists as
  		// year 1 and sorts as ancient. See
  		// api/common/v1alpha1.ReleaseInfo.PublishedAt.
  		return fmt.Errorf("%w: PublishedAt points at the zero time; send nil instead", ErrInvalidRelease)
  	}
  	return nil
  }
  ```

- [ ] **Step 14: Implement `Upsert` the naive way, in `pkg/relindex/upsert.go`.**

  This is deliberately the obvious-looking wrong implementation. Step 16's test
  kills it. Delete the `Upsert` stub from `store.go`.

  ```go
  // Upsert writes rels in one transaction.
  func (s *sqliteStore) Upsert(ctx context.Context, rels []Release) (int, error) {
  	if len(rels) == 0 {
  		return 0, nil
  	}
  	s.wmu.Lock()
  	defer s.wmu.Unlock()

  	tx, err := s.db.BeginTx(ctx, nil)
  	if err != nil {
  		return 0, fmt.Errorf("relindex: begin upsert: %w", err)
  	}
  	defer func() { _ = tx.Rollback() }()

  	const stmt = `INSERT OR REPLACE INTO releases
  		(indexer, guid, title, title_norm, grp, protocol, categories, size_bytes, published_at, fetched_at, info_json)
  		VALUES (?,?,?,?,?,?,?,?,?,?,?)`
  	for _, r := range rels {
  		cats, err := marshalCategories(r.Categories)
  		if err != nil {
  			return 0, err
  		}
  		if _, err := tx.ExecContext(ctx, stmt,
  			r.Indexer, r.GUID, r.Title, r.TitleNorm, r.Group, r.Protocol,
  			cats, r.SizeBytes, nullNanos(r.PublishedAt), r.FetchedAt.UTC().UnixNano(), r.InfoJSON,
  		); err != nil {
  			return 0, fmt.Errorf("relindex: upsert %s/%s: %w", r.Indexer, r.GUID, err)
  		}
  	}
  	if err := tx.Commit(); err != nil {
  		return 0, fmt.Errorf("relindex: commit upsert: %w", err)
  	}
  	return len(rels), nil
  }
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestUpsert|TestOpenReopens'` and
  expect all four to pass.

- [ ] **Step 15: Commit the naive upsert, then write the failing idempotency tests.**

  Append to `upsert_test.go`:

  ```go
  func TestUpsertIsIdempotentAndCountsOnlyNewRows(t *testing.T) {
  	// The RSS worker re-reads the same feed every 15 minutes and the search
  	// service upserts every hit of every search. Almost every row it writes
  	// already exists, and `inserted` is what becomes
  	// Indexer.status.lastRssNewCount and what the RSS worker publishes to the
  	// firehose. An inflated count fans stale releases at catalogarr forever.
  	s := newStore(t)
  	ctx := t.Context()

  	r := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
  	mustUpsert(t, ctx, s, 1, r)
  	mustUpsert(t, ctx, s, 0, r)
  	mustUpsert(t, ctx, s, 0, r)

  	st, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.EqualValues(t, 1, st.Releases)
  }

  func TestUpsertUpdatesTheExistingRowInPlace(t *testing.T) {
  	s := newStore(t)
  	ctx := t.Context()

  	first := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
  	mustUpsert(t, ctx, s, 1, first)

  	second := first
  	second.SizeBytes = 42
  	second.TitleNorm = "the matrix 1999 2160p remux"
  	second.FetchedAt = fetchedAt.Add(time.Hour)
  	mustUpsert(t, ctx, s, 0, second)

  	got, err := s.Search(ctx, relindex.Query{Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.EqualValues(t, 42, got[0].SizeBytes)
  	require.Equal(t, "the matrix 1999 2160p remux", got[0].TitleNorm)
  }

  func TestUpsertCountsARepeatedKeyInTheSameBatchOnce(t *testing.T) {
  	// A paged RSS response can repeat a guid across page boundaries.
  	s := newStore(t)
  	r := rel("nzbgeek", "g1", "The Matrix 1999")
  	mustUpsert(t, t.Context(), s, 1, r, r, r)
  }

  func TestUpsertKeepsTheFTSIndexInSyncAcrossAnUpdate(t *testing.T) {
  	// The external-content FTS5 table only learns about writes through the
  	// triggers. An INSERT OR REPLACE deletes and re-inserts the row with a
  	// NEW rowid, which fires the delete trigger with values the index may no
  	// longer hold -- the classic way to rot an external-content index in
  	// silence.
  	s := newStore(t)
  	ctx := t.Context()

  	first := rel("nzbgeek", "g1", "matrix")
  	first.TitleNorm = "matrix"
  	mustUpsert(t, ctx, s, 1, first)

  	second := first
  	second.TitleNorm = "dune"
  	mustUpsert(t, ctx, s, 0, second)

  	gone, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 10})
  	require.NoError(t, err)
  	require.Empty(t, gone, "the old term must have been removed from the FTS index")

  	found, err := s.Search(ctx, relindex.Query{Text: "dune", Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, found, 1)
  }
  ```

  Add `"time"` to the imports. Run
  `go test -race ./pkg/relindex/... -run TestUpsert`. Expect
  `TestUpsertIsIdempotentAndCountsOnlyNewRows` to fail with
  `expected: 0 actual: 1`, `TestUpsertCountsARepeatedKeyInTheSameBatchOnce` to
  fail with `expected: 1 actual: 3`, and the two `Search` cases to fail with
  `relindex: Search not implemented` — leave those failing; Step 22 fixes them.

- [ ] **Step 16: Replace the naive upsert with the insert-then-update pair, in `pkg/relindex/upsert.go`.**

  Two statements, not one. `INSERT ... ON CONFLICT DO NOTHING` reports
  `RowsAffected() == 1` for a genuine insert and `0` for a conflict, which is the
  only unambiguous source of a truthful `inserted` count.

  ```go
  // Upsert writes rels in one transaction, keyed UNIQUE(indexer, guid).
  //
  // The count is exact, and getting it exact is why this is two statements:
  //
  //   - `INSERT OR REPLACE` deletes the conflicting row and inserts a new one
  //     with a NEW rowid. That breaks the external-content FTS5 pairing, churns
  //     the index, and -- since every statement "succeeds" -- gives no way to
  //     tell an insert from a replace.
  //   - `INSERT ... ON CONFLICT DO UPDATE` keeps the rowid, but RowsAffected
  //     counts the update too, so it also cannot distinguish them.
  //   - `INSERT ... ON CONFLICT DO NOTHING` affects exactly 1 row on a genuine
  //     insert and exactly 0 on a conflict. The UPDATE runs only in the 0 case.
  //
  // `inserted` feeds Indexer.status.lastRssNewCount and decides which releases
  // the RSS worker publishes to CLUSTARR_RELEASES, so an inflated count fans
  // stale releases at catalogarr on every poll, forever.
  //
  // Every release is validated before the transaction opens, so a bad batch
  // costs no writes at all. On any error the transaction rolls back and the
  // returned count is 0: nothing was written, so nothing may be claimed.
  func (s *sqliteStore) Upsert(ctx context.Context, rels []Release) (int, error) {
  	if len(rels) == 0 {
  		return 0, nil
  	}
  	for i := range rels {
  		if err := validate(rels[i]); err != nil {
  			return 0, fmt.Errorf("relindex: release %d: %w", i, err)
  		}
  	}

  	s.wmu.Lock()
  	defer s.wmu.Unlock()

  	tx, err := s.db.BeginTx(ctx, nil)
  	if err != nil {
  		return 0, fmt.Errorf("relindex: begin upsert: %w", err)
  	}
  	defer func() { _ = tx.Rollback() }()

  	const insertSQL = `INSERT INTO releases
  		(indexer, guid, title, title_norm, grp, protocol, categories, size_bytes, published_at, fetched_at, info_json)
  		VALUES (?,?,?,?,?,?,?,?,?,?,?)
  		ON CONFLICT(indexer, guid) DO NOTHING`
  	const updateSQL = `UPDATE releases SET
  		title = ?, title_norm = ?, grp = ?, protocol = ?, categories = ?,
  		size_bytes = ?, published_at = ?, fetched_at = ?, info_json = ?
  		WHERE indexer = ? AND guid = ?`

  	ins, err := tx.PrepareContext(ctx, insertSQL)
  	if err != nil {
  		return 0, fmt.Errorf("relindex: prepare insert: %w", err)
  	}
  	defer func() { _ = ins.Close() }()

  	upd, err := tx.PrepareContext(ctx, updateSQL)
  	if err != nil {
  		return 0, fmt.Errorf("relindex: prepare update: %w", err)
  	}
  	defer func() { _ = upd.Close() }()

  	inserted := 0
  	for _, r := range rels {
  		cats, err := marshalCategories(r.Categories)
  		if err != nil {
  			return 0, err
  		}
  		pub := nullNanos(r.PublishedAt)
  		fetched := r.FetchedAt.UTC().UnixNano()

  		res, err := ins.ExecContext(ctx,
  			r.Indexer, r.GUID, r.Title, r.TitleNorm, r.Group, r.Protocol,
  			cats, r.SizeBytes, pub, fetched, r.InfoJSON,
  		)
  		if err != nil {
  			return 0, fmt.Errorf("relindex: insert %s/%s: %w", r.Indexer, r.GUID, err)
  		}
  		n, err := res.RowsAffected()
  		if err != nil {
  			return 0, fmt.Errorf("relindex: insert %s/%s rows: %w", r.Indexer, r.GUID, err)
  		}
  		if n == 1 {
  			inserted++
  			continue
  		}
  		if _, err := upd.ExecContext(ctx,
  			r.Title, r.TitleNorm, r.Group, r.Protocol, cats,
  			r.SizeBytes, pub, fetched, r.InfoJSON,
  			r.Indexer, r.GUID,
  		); err != nil {
  			return 0, fmt.Errorf("relindex: update %s/%s: %w", r.Indexer, r.GUID, err)
  		}
  	}
  	if err := tx.Commit(); err != nil {
  		return 0, fmt.Errorf("relindex: commit upsert: %w", err)
  	}
  	return inserted, nil
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestUpsert`. The two counting cases
  now pass; the two `Search` cases still fail with `Search not implemented`.

- [ ] **Step 17: Write the failing `PublishedAt` round-trip tests.**

  Append to `upsert_test.go`. This is the named defect; it gets four cases.

  ```go
  func TestPublishedAtRoundTripsAsNil(t *testing.T) {
  	// Phase C: a non-pointer time could not be persisted at all, and the
  	// workaround -- substituting "now" -- made every dateless release sort as
  	// brand new. Ranking uses publish age as the usenet tiebreaker, so this
  	// silently promoted the worst releases available. Absence is a fact, and
  	// it is stored as one: SQL NULL in, nil out.
  	s := newStore(t)
  	ctx := t.Context()

  	r := rel("nzbgeek", "g1", "The Matrix 1999")
  	r.PublishedAt = nil
  	mustUpsert(t, ctx, s, 1, r)

  	got, err := s.Search(ctx, relindex.Query{Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Nil(t, got[0].PublishedAt, "a dateless release must read back dateless")
  }

  func TestPublishedAtRoundTripsExactlyWhenPresent(t *testing.T) {
  	s := newStore(t)
  	ctx := t.Context()
  	mustUpsert(t, ctx, s, 1, rel("nzbgeek", "g1", "The Matrix 1999"))

  	got, err := s.Search(ctx, relindex.Query{Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.NotNil(t, got[0].PublishedAt)
  	// Nanosecond precision, UTC, and no monotonic reading.
  	require.Equal(t, publishedAt, *got[0].PublishedAt)
  }

  func TestUpsertRejectsAPointerToTheZeroPublishedAt(t *testing.T) {
  	// The store cannot tell "no date" from "year 1" once it is stored, so it
  	// refuses to be handed the ambiguity in the first place.
  	s := newStore(t)
  	zero := time.Time{}
  	r := rel("nzbgeek", "g1", "The Matrix 1999")
  	r.PublishedAt = &zero

  	n, err := s.Upsert(t.Context(), []relindex.Release{r})
  	require.ErrorIs(t, err, relindex.ErrInvalidRelease)
  	require.Contains(t, err.Error(), "send nil instead")
  	require.Zero(t, n)
  }

  func TestPruneAndSinceDoNotDropDatelessReleases(t *testing.T) {
  	// The other half of the contract: a nil PublishedAt must not make a row
  	// invisible to a window. Both Prune and Query.Since work on fetched_at,
  	// which is never NULL.
  	s := newStore(t)
  	ctx := t.Context()

  	r := rel("nzbgeek", "g1", "The Matrix 1999")
  	r.PublishedAt = nil
  	mustUpsert(t, ctx, s, 1, r)

  	since := fetchedAt.Add(-time.Hour)
  	got, err := s.Search(ctx, relindex.Query{Since: &since, Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  }
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestPublishedAt|TestUpsertRejects|TestPruneAndSince'`.
  `TestUpsertRejectsAPointerToTheZeroPublishedAt` passes (Step 13 wrote
  `validate`); the other three fail with `relindex: Search not implemented`.

- [ ] **Step 18: Write the remaining failing validation and rollback tests.**

  Append to `upsert_test.go`:

  ```go
  func TestUpsertRejectsAnIncompleteRelease(t *testing.T) {
  	s := newStore(t)
  	good := rel("nzbgeek", "g1", "The Matrix 1999")

  	tests := []struct {
  		name    string
  		mutate  func(r *relindex.Release)
  		wantMsg string
  	}{
  		{"empty indexer", func(r *relindex.Release) { r.Indexer = "" }, "Indexer is empty"},
  		{"empty guid", func(r *relindex.Release) { r.GUID = "" }, "GUID is empty"},
  		{"empty title_norm", func(r *relindex.Release) { r.TitleNorm = "" }, "TitleNorm is empty"},
  		{"zero fetchedAt", func(r *relindex.Release) { r.FetchedAt = time.Time{} }, "FetchedAt is zero"},
  	}
  	for _, tc := range tests {
  		t.Run(tc.name, func(t *testing.T) {
  			r := good
  			tc.mutate(&r)
  			n, err := s.Upsert(t.Context(), []relindex.Release{r})
  			require.ErrorIs(t, err, relindex.ErrInvalidRelease)
  			require.Contains(t, err.Error(), tc.wantMsg)
  			require.Zero(t, n)
  		})
  	}
  }

  func TestUpsertWritesNothingWhenAnyReleaseInTheBatchIsInvalid(t *testing.T) {
  	// One transaction means one outcome. A partially-applied RSS batch would
  	// leave the index claiming rows the worker never published.
  	s := newStore(t)
  	ctx := t.Context()

  	bad := rel("nzbgeek", "g2", "Dune 2021")
  	bad.GUID = ""

  	n, err := s.Upsert(ctx, []relindex.Release{
  		rel("nzbgeek", "g1", "The Matrix 1999"),
  		bad,
  		rel("nzbgeek", "g3", "Arrival 2016"),
  	})
  	require.ErrorIs(t, err, relindex.ErrInvalidRelease)
  	require.Zero(t, n)

  	st, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.Zero(t, st.Releases, "the whole batch must roll back")
  }

  func TestUpsertRoundTripsEveryField(t *testing.T) {
  	s := newStore(t)
  	ctx := t.Context()

  	want := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
  	want.Protocol = "usenet"
  	want.Categories = []int{2000, 2040, 5040}
  	want.SizeBytes = 8_589_934_592
  	want.InfoJSON = []byte(`{"info":{"guid":"g1"},"parsedTitle":"The Matrix"}`)
  	mustUpsert(t, ctx, s, 1, want)

  	got, err := s.Search(ctx, relindex.Query{Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Equal(t, want, got[0])
  }

  func TestUpsertRoundTripsAnEmptyCategoryListAsNil(t *testing.T) {
  	s := newStore(t)
  	ctx := t.Context()

  	r := rel("nzbgeek", "g1", "The Matrix 1999")
  	r.Categories = []int{}
  	mustUpsert(t, ctx, s, 1, r)

  	got, err := s.Search(ctx, relindex.Query{Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Nil(t, got[0].Categories)
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestUpsert`. The two validation
  cases pass; the two round-trip cases fail with `Search not implemented`.

- [ ] **Step 19: Commit the upsert path.**

  ```
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit \
    -m "feat(relindex): idempotent batched upsert with a truthful inserted count" \
    -- pkg/relindex
  ```

- [ ] **Step 20: Write the failing FTS5 escaping tests, `pkg/relindex/fts_test.go`.**

  This file is `package relindex` (internal), unlike every other test file here,
  because `matchExpr` is unexported and is the single most security-relevant
  function in the package. Both packages can live in the same directory.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"testing"

  	"github.com/stretchr/testify/require"
  )

  // Query.Text is attacker-controlled. It arrives from third-party indexer
  // titles (via the RSS matcher's re-query path) and from user input in the UI.
  // There are TWO distinct injection surfaces and they need two distinct
  // defences:
  //
  //  1. SQL. Solved by binding: the MATCH argument is always a `?` parameter,
  //     never concatenated into the statement.
  //  2. The FTS5 query mini-language, which lives INSIDE that bound string and
  //     has its own grammar: " for phrases, * for prefixes, : for column
  //     filters, ^ for anchors, - and NOT and AND and OR and NEAR for
  //     operators, and parentheses. A bound parameter does nothing about this
  //     one. Unescaped, the best case is `fts5: syntax error near "..."` on
  //     every search with an apostrophe in it; the worse case is a query that
  //     silently means something else.
  //
  // The defence is to stop parsing user text as a query at all: split on
  // whitespace, wrap each token in double quotes (doubling any interior quote),
  // and join with AND. Inside a double-quoted FTS5 string every character is a
  // literal to be tokenised -- operators, punctuation and all.
  func TestMatchExprQuotesEveryTermAndNeutralisesTheGrammar(t *testing.T) {
  	tests := []struct {
  		name string
  		in   string
  		want string
  	}{
  		{"plain words", "the matrix", `"the" AND "matrix"`},
  		{"collapses runs of whitespace", "  the \t matrix \n ", `"the" AND "matrix"`},
  		{"empty", "", ``},
  		{"whitespace only", "   \t\n ", ``},
  		{"a bare quote is dropped", `"`, ``},
  		{"an embedded quote is doubled", `he said "hi"`, `"he" AND "said" AND """hi"""`},
  		{"a lone star is dropped", "*", ``},
  		{"a trailing star becomes a literal", "matrix*", `"matrix*"`},
  		{"OR is a term, not an operator", "matrix OR dune", `"matrix" AND "OR" AND "dune"`},
  		{"NEAR is a term, not an operator", "matrix NEAR dune", `"matrix" AND "NEAR" AND "dune"`},
  		{"NOT is a term, not an operator", "matrix NOT dune", `"matrix" AND "NOT" AND "dune"`},
  		{"a leading dash is a literal", "-dune", `"-dune"`},
  		{"a column filter becomes a phrase", "title_norm:foo", `"title_norm:foo"`},
  		{"parentheses alone are dropped", "( )", ``},
  		{"an anchor is a literal", "^matrix", `"^matrix"`},
  		{"punctuation-only tokens are dropped", `matrix -- ( ) * " dune`, `"matrix" AND "dune"`},
  		{"a hyphenated title survives as a phrase", "spider-man", `"spider-man"`},
  		{"unicode is preserved", "amélie", `"amélie"`},
  		{"digits are searchable terms", "1999", `"1999"`},
  		{"a quote-injection attempt is inert", `a" OR title_norm:"b`, `"a"" AND "OR" AND "title_norm:""b"`},
  	}
  	for _, tc := range tests {
  		t.Run(tc.name, func(t *testing.T) {
  			require.Equal(t, tc.want, matchExpr(tc.in))
  		})
  	}
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestMatchExpr` and expect a compile
  failure: `undefined: matchExpr`.

- [ ] **Step 21: Implement `matchExpr`, `pkg/relindex/fts.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"strings"
  	"unicode"
  )

  // matchExpr turns arbitrary, attacker-controlled text into an FTS5 MATCH
  // expression that can only ever mean "every one of these terms appears".
  //
  // It does not parse the input as a query. Each whitespace-separated token is
  // wrapped in double quotes, which makes it an FTS5 *string*: the tokenizer
  // splits it into terms and every operator character inside it -- * : ^ - ( )
  // and the bare keywords AND, OR, NOT and NEAR -- is a literal. An interior
  // double quote is escaped by doubling it, which is FTS5's only escape.
  //
  // Tokens carrying no letter or digit are dropped. `"*"` or `"("` would quote
  // cleanly but tokenise to nothing, and FTS5 rejects a phrase with no terms
  // with a syntax error -- so a user typing a lone asterisk would get a 500
  // rather than a result set.
  //
  // The return value is always passed as a BOUND parameter. This function
  // defends the FTS5 grammar; parameter binding defends SQL. Neither substitutes
  // for the other.
  //
  // It returns "" when text carries no searchable term at all, and Search then
  // omits the MATCH clause entirely rather than passing an empty expression,
  // which FTS5 also rejects.
  //
  // It deliberately does NOT normalise. ADR-0003 puts title normalisation in
  // front of the store, so the caller must run Query.Text through the same
  // function it ran Release.TitleNorm through, or nothing will match.
  func matchExpr(text string) string {
  	fields := strings.Fields(text)
  	terms := make([]string, 0, len(fields))
  	for _, f := range fields {
  		if !hasAlnum(f) {
  			continue
  		}
  		terms = append(terms, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
  	}
  	return strings.Join(terms, " AND ")
  }

  // hasAlnum reports whether s contains at least one letter or digit, i.e.
  // whether the unicode61 tokenizer will get at least one term out of it.
  func hasAlnum(s string) bool {
  	for _, r := range s {
  		if unicode.IsLetter(r) || unicode.IsDigit(r) {
  			return true
  		}
  	}
  	return false
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestMatchExpr` and expect all 20
  subtests green.

- [ ] **Step 22: Implement `Search`, `pkg/relindex/search.go`.**

  Delete the `Search` stub from `store.go`. Note the ordering discipline: the
  WHERE fragments are written and their arguments appended in the same pass, so
  the placeholder order and the `args` order cannot drift.

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex

  import (
  	"context"
  	"database/sql"
  	"fmt"
  	"strings"
  	"time"
  )

  // Search returns releases matching q.
  //
  // Ordering: best-ranked first when q.Text is set (FTS5's bm25 `rank` is
  // negative and smaller is better, so plain ascending order is best-first),
  // newest-fetched first otherwise. `r.id DESC` is always the final tiebreaker,
  // so two rows with identical rank or identical fetched_at come back in a
  // stable order -- without it, a paged caller can see the same row twice.
  //
  // It takes no write lock: under WAL a reader never blocks and is never blocked.
  func (s *sqliteStore) Search(ctx context.Context, q Query) ([]Release, error) {
  	var (
  		sb   strings.Builder
  		args []any
  	)
  	text := matchExpr(q.Text)

  	sb.WriteString(`SELECT r.indexer, r.guid, r.title, r.title_norm, r.grp, r.protocol, ` +
  		`r.categories, r.size_bytes, r.published_at, r.fetched_at, r.info_json FROM releases r`)
  	if text != "" {
  		// The FTS5 table is NOT aliased: `releases_fts MATCH ?` has to name
  		// the table for SQLite to route the MATCH to the module.
  		sb.WriteString(` JOIN releases_fts ON releases_fts.rowid = r.id`)
  	}
  	sb.WriteString(` WHERE 1 = 1`)

  	if text != "" {
  		// Bound, never interpolated. matchExpr has already made the string
  		// harmless as an FTS5 expression; this keeps it harmless as SQL.
  		sb.WriteString(` AND releases_fts MATCH ?`)
  		args = append(args, text)
  	}
  	if len(q.Indexers) > 0 {
  		sb.WriteString(` AND r.indexer IN (` + placeholders(len(q.Indexers)) + `)`)
  		for _, ix := range q.Indexers {
  			args = append(args, ix)
  		}
  	}
  	if q.Protocol != "" {
  		sb.WriteString(` AND r.protocol = ?`)
  		args = append(args, q.Protocol)
  	}
  	if q.Since != nil {
  		// fetched_at, not published_at. published_at is nullable and a NULL
  		// compares false against every bound, so filtering on it would
  		// silently delete every dateless release from every window -- the
  		// same defect as backfilling it, arriving from the query side.
  		sb.WriteString(` AND r.fetched_at >= ?`)
  		args = append(args, q.Since.UTC().UnixNano())
  	}
  	if len(q.Categories) > 0 {
  		// "carries ANY of these". json_each expands the stored JSON array
  		// into rows; the column is always a valid array, never NULL, so
  		// this never errors on a row.
  		sb.WriteString(` AND EXISTS (SELECT 1 FROM json_each(r.categories) je WHERE je.value IN (` +
  			placeholders(len(q.Categories)) + `))`)
  		for _, c := range q.Categories {
  			args = append(args, c)
  		}
  	}

  	if text != "" {
  		sb.WriteString(` ORDER BY releases_fts.rank, r.id DESC`)
  	} else {
  		sb.WriteString(` ORDER BY r.fetched_at DESC, r.id DESC`)
  	}
  	// Zero or negative means no LIMIT clause at all: the contract says the
  	// store never invents one. D1-5 always passes schema.MaxSearchReleases.
  	if q.Limit > 0 {
  		sb.WriteString(` LIMIT ?`)
  		args = append(args, q.Limit)
  	}

  	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
  	if err != nil {
  		return nil, fmt.Errorf("relindex: search: %w", err)
  	}
  	defer func() { _ = rows.Close() }()

  	capHint := 0
  	if q.Limit > 0 {
  		capHint = q.Limit
  	}
  	out := make([]Release, 0, capHint)
  	for rows.Next() {
  		var (
  			r        Release
  			cats     string
  			pub      sql.NullInt64
  			fetched  int64
  		)
  		if err := rows.Scan(&r.Indexer, &r.GUID, &r.Title, &r.TitleNorm, &r.Group,
  			&r.Protocol, &cats, &r.SizeBytes, &pub, &fetched, &r.InfoJSON); err != nil {
  			return nil, fmt.Errorf("relindex: scan release: %w", err)
  		}
  		if r.Categories, err = unmarshalCategories(cats); err != nil {
  			return nil, err
  		}
  		r.PublishedAt = timeFromNull(pub)
  		r.FetchedAt = time.Unix(0, fetched).UTC()
  		out = append(out, r)
  	}
  	// rows.Err reports an error that ended iteration early. Without this
  	// check a truncated result set reads as a successful empty one.
  	if err := rows.Err(); err != nil {
  		return nil, fmt.Errorf("relindex: search rows: %w", err)
  	}
  	return out, nil
  }

  // placeholders returns "?,?,?" for n > 0.
  func placeholders(n int) string {
  	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
  }
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestUpsert|TestPublishedAt|TestPruneAndSince'`
  and expect every case green.

- [ ] **Step 23: Write the failing search filter tests, `pkg/relindex/search_test.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"testing"
  	"time"

  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  // seed writes a small, deliberately heterogeneous corpus.
  func seed(t *testing.T, s relindex.Store) {
  	t.Helper()

  	a := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
  	a.TitleNorm = "matrix 1999 1080p bluray x264"
  	a.Protocol = "usenet"
  	a.Categories = []int{2000, 2040}
  	a.FetchedAt = fetchedAt

  	b := rel("nzbgeek", "g2", "Dune Part Two 2024 2160p WEB-DL DV HDR10-FLUX")
  	b.TitleNorm = "dune part two 2024 2160p web dl"
  	b.Group = "FLUX"
  	b.Protocol = "usenet"
  	b.Categories = []int{2000, 2045}
  	b.FetchedAt = fetchedAt.Add(time.Minute)

  	c := rel("torrentleech", "t1", "Severance S02E01 1080p ATVP WEB-DL-NTb")
  	c.TitleNorm = "severance s02e01 1080p atvp web dl"
  	c.Protocol = "torrent"
  	c.Categories = []int{5000, 5040}
  	c.FetchedAt = fetchedAt.Add(2 * time.Minute)
  	c.PublishedAt = nil

  	mustUpsert(t, t.Context(), s, 3, a, b, c)
  }

  func TestSearchWithNoFiltersReturnsEverythingNewestFirst(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	got, err := s.Search(t.Context(), relindex.Query{})
  	require.NoError(t, err)
  	require.Len(t, got, 3)
  	require.Equal(t, "t1", got[0].GUID, "newest fetched_at first")
  	require.Equal(t, "g1", got[2].GUID)
  }

  func TestSearchMatchesTheNormalisedTitle(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	got, err := s.Search(t.Context(), relindex.Query{Text: "dune", Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Equal(t, "g2", got[0].GUID)
  }

  func TestSearchMatchesTheReleaseGroupColumn(t *testing.T) {
  	// Spec §6.2 pins the FTS5 columns as `title_norm, grp`. A caller
  	// searching for a group name must hit the second column.
  	s := newStore(t)
  	seed(t, s)

  	got, err := s.Search(t.Context(), relindex.Query{Text: "FLUX", Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Equal(t, "g2", got[0].GUID)
  }

  func TestSearchRequiresEveryTerm(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	both, err := s.Search(t.Context(), relindex.Query{Text: "dune 2024", Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, both, 1)

  	none, err := s.Search(t.Context(), relindex.Query{Text: "dune matrix", Limit: 10})
  	require.NoError(t, err)
  	require.Empty(t, none)
  }

  func TestSearchFiltersByIndexer(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	got, err := s.Search(t.Context(), relindex.Query{Indexers: []string{"torrentleech"}, Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Equal(t, "t1", got[0].GUID)
  }

  func TestSearchFiltersByProtocol(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	got, err := s.Search(t.Context(), relindex.Query{Protocol: "usenet", Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 2)
  }

  func TestSearchFiltersByAnyCategory(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	top, err := s.Search(t.Context(), relindex.Query{Categories: []int{2000}, Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, top, 2)

  	sub, err := s.Search(t.Context(), relindex.Query{Categories: []int{2045, 5040}, Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, sub, 2, "ANY of the requested ids, not all")

  	none, err := s.Search(t.Context(), relindex.Query{Categories: []int{7000}, Limit: 10})
  	require.NoError(t, err)
  	require.Empty(t, none)
  }

  func TestSearchFiltersBySinceOnFetchedAt(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	since := fetchedAt.Add(time.Minute)
  	got, err := s.Search(t.Context(), relindex.Query{Since: &since, Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 2, "Since is inclusive")
  	// t1 has a nil PublishedAt and must still be inside the window.
  	require.Equal(t, "t1", got[0].GUID)
  }

  func TestSearchCombinesEveryFilter(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	since := fetchedAt.Add(-time.Hour)
  	got, err := s.Search(t.Context(), relindex.Query{
  		Text:       "dune",
  		Indexers:   []string{"nzbgeek", "torrentleech"},
  		Categories: []int{2000},
  		Protocol:   "usenet",
  		Since:      &since,
  		Limit:      10,
  	})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Equal(t, "g2", got[0].GUID)
  }

  func TestSearchHonoursTheCallerLimitAndInventsNone(t *testing.T) {
  	s := newStore(t)
  	seed(t, s)

  	two, err := s.Search(t.Context(), relindex.Query{Limit: 2})
  	require.NoError(t, err)
  	require.Len(t, two, 2)

  	all, err := s.Search(t.Context(), relindex.Query{Limit: 0})
  	require.NoError(t, err)
  	require.Len(t, all, 3, "zero means no LIMIT clause, not a default")

  	neg, err := s.Search(t.Context(), relindex.Query{Limit: -1})
  	require.NoError(t, err)
  	require.Len(t, neg, 3)
  }

  func TestSearchOnAnEmptyIndexReturnsNoRowsAndNoError(t *testing.T) {
  	s := newStore(t)
  	got, err := s.Search(t.Context(), relindex.Query{Text: "matrix", Limit: 10})
  	require.NoError(t, err)
  	require.Empty(t, got)
  }
  ```

  Run `go test -race ./pkg/relindex/... -run TestSearch`. Expect green — Step 22
  implemented all of it. If anything is red, the bug is in Step 22; fix it there
  rather than loosening the test.

- [ ] **Step 24: Write the hostile-input test that drives the real database.**

  Step 20 proved `matchExpr` produces the right string. This proves SQLite
  accepts it — which is the assertion that actually matters, because a MATCH
  syntax error is a runtime 500 on a live search and nothing in the unit test
  would have caught it. Append to `search_test.go`:

  ```go
  func TestSearchSurvivesHostileQueryText(t *testing.T) {
  	// Every one of these is a real FTS5 syntax error, a real column filter,
  	// or a real operator if it reaches MATCH unescaped. None may error, and
  	// none may return a row it should not.
  	s := newStore(t)
  	seed(t, s)

  	hostile := []string{
  		``,
  		`   `,
  		`"`,
  		`""`,
  		`"""`,
  		`*`,
  		`**`,
  		`^`,
  		`(`,
  		`)`,
  		`()`,
  		`-`,
  		`:`,
  		`title_norm:dune`,
  		`matrix OR dune`,
  		`matrix AND dune`,
  		`matrix NOT dune`,
  		`matrix NEAR dune`,
  		`NEAR(matrix dune, 5)`,
  		`dune*`,
  		`"dune" OR "matrix"`,
  		`dune" OR title_norm:"matrix`,
  		`'; DROP TABLE releases; --`,
  		`dune'); DELETE FROM releases; --`,
  		`{dune matrix}`,
  		`amélie`,
  		`日本語`,
  		"dune\x00matrix",
  	}
  	for _, q := range hostile {
  		t.Run(q, func(t *testing.T) {
  			got, err := s.Search(t.Context(), relindex.Query{Text: q, Limit: 10})
  			require.NoError(t, err, "hostile text must not produce an error")
  			require.LessOrEqual(t, len(got), 3)
  		})
  	}

  	// And the corpus is intact: no statement smuggled a DELETE through.
  	st, err := s.Stats(t.Context())
  	require.NoError(t, err)
  	require.EqualValues(t, 3, st.Releases)
  }

  func TestSearchTreatsOperatorKeywordsAsLiterals(t *testing.T) {
  	// `matrix OR dune` must mean "a title containing matrix AND or AND dune",
  	// which nothing does -- NOT "matrix or dune", which two rows do. If this
  	// returns rows, the grammar leaked.
  	s := newStore(t)
  	seed(t, s)

  	got, err := s.Search(t.Context(), relindex.Query{Text: "matrix OR dune", Limit: 10})
  	require.NoError(t, err)
  	require.Empty(t, got)
  }
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestSearchSurvives|TestSearchTreats'`.
  Expect green. If a case errors with `fts5: syntax error near ...`, add its
  shape to `matchExpr`'s drop rule in Step 21 and re-run both this and Step 20's
  table.

- [ ] **Step 25: Commit the search path.**

  ```
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit \
    -m "feat(relindex): FTS5 search with escaped MATCH text and bound filters" \
    -- pkg/relindex
  ```

- [ ] **Step 26: Write the failing prune and stats tests, `pkg/relindex/maint_test.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  func TestPruneDeletesOnlyRowsOlderThanTheCutoff(t *testing.T) {
  	// Spec §6.2: a sweep every 10 minutes over a 72h window. The window is
  	// the CALLER's arithmetic -- Prune is a pure function of the instant it
  	// is handed, reads no clock, and can therefore be tested without one.
  	s := newStore(t)
  	ctx := t.Context()

  	old := rel("nzbgeek", "old", "The Matrix 1999")
  	old.FetchedAt = fetchedAt.Add(-96 * time.Hour)
  	fresh := rel("nzbgeek", "fresh", "Dune Part Two 2024")
  	fresh.FetchedAt = fetchedAt
  	mustUpsert(t, ctx, s, 2, old, fresh)

  	n, err := s.Prune(ctx, fetchedAt.Add(-72*time.Hour))
  	require.NoError(t, err)
  	require.Equal(t, 1, n)

  	got, err := s.Search(ctx, relindex.Query{Limit: 10})
  	require.NoError(t, err)
  	require.Len(t, got, 1)
  	require.Equal(t, "fresh", got[0].GUID)
  }

  func TestPruneIsExclusiveAtTheBoundary(t *testing.T) {
  	// `fetched_at < olderThan`. A row fetched exactly at the cutoff stays,
  	// so a sweep cannot delete a row the previous sweep just decided to keep.
  	s := newStore(t)
  	ctx := t.Context()

  	r := rel("nzbgeek", "g1", "The Matrix 1999")
  	r.FetchedAt = fetchedAt
  	mustUpsert(t, ctx, s, 1, r)

  	n, err := s.Prune(ctx, fetchedAt)
  	require.NoError(t, err)
  	require.Zero(t, n)
  }

  func TestPruneOnAnEmptyIndexIsANoOp(t *testing.T) {
  	s := newStore(t)
  	n, err := s.Prune(t.Context(), fetchedAt)
  	require.NoError(t, err)
  	require.Zero(t, n)
  }

  func TestPruneRejectsTheZeroTime(t *testing.T) {
  	// time.Time{}.UnixNano() overflows int64 into a large negative number,
  	// so a zero cutoff silently deletes nothing while looking like it worked.
  	s := newStore(t)
  	_, err := s.Prune(t.Context(), time.Time{})
  	require.ErrorIs(t, err, relindex.ErrInvalidArg)
  }

  func TestPruneRemovesTheRowsFromTheFTSIndexToo(t *testing.T) {
  	// External-content FTS5 learns about the DELETE only through the
  	// releases_ad trigger. If the trigger is wrong, the row vanishes from
  	// Stats but keeps answering text searches -- with a rowid that no longer
  	// resolves.
  	s := newStore(t)
  	ctx := t.Context()

  	r := rel("nzbgeek", "g1", "matrix")
  	r.TitleNorm = "matrix"
  	r.FetchedAt = fetchedAt.Add(-96 * time.Hour)
  	mustUpsert(t, ctx, s, 1, r)

  	n, err := s.Prune(ctx, fetchedAt.Add(-72*time.Hour))
  	require.NoError(t, err)
  	require.Equal(t, 1, n)

  	got, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 10})
  	require.NoError(t, err)
  	require.Empty(t, got)
  }

  func TestStatsReportsTheCorpus(t *testing.T) {
  	s := newStore(t)
  	ctx := t.Context()

  	a := rel("nzbgeek", "g1", "The Matrix 1999")
  	a.FetchedAt = fetchedAt
  	b := rel("nzbgeek", "g2", "Dune Part Two 2024")
  	b.FetchedAt = fetchedAt.Add(time.Hour)
  	c := rel("torrentleech", "t1", "Severance S02E01")
  	c.FetchedAt = fetchedAt.Add(-time.Hour)
  	mustUpsert(t, ctx, s, 3, a, b, c)

  	st, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.EqualValues(t, 3, st.Releases)
  	require.EqualValues(t, 2, st.Indexers)
  	require.Equal(t, fetchedAt.Add(-time.Hour), st.OldestSeen)
  	require.Equal(t, fetchedAt.Add(time.Hour), st.NewestSeen)
  	require.Positive(t, st.SizeBytes)
  }

  func TestStatsOnAnEmptyIndexReportsZeroTimes(t *testing.T) {
  	// First boot on a fresh PVC. MIN/MAX over no rows are NULL, and NULL is
  	// the zero time -- not "now", and not an error that would fail readiness.
  	s := newStore(t)
  	st, err := s.Stats(t.Context())
  	require.NoError(t, err)
  	require.Zero(t, st.Releases)
  	require.Zero(t, st.Indexers)
  	require.True(t, st.OldestSeen.IsZero())
  	require.True(t, st.NewestSeen.IsZero())
  	require.Positive(t, st.SizeBytes, "an empty database is still a file with a header")
  }

  func TestStatsCountsTheWriteAheadLog(t *testing.T) {
  	// The -wal file shares the 5Gi PVC, so a SizeBytes that ignored it would
  	// under-report the thing that actually fills the volume.
  	path := filepath.Join(t.TempDir(), "releases.db")
  	s := newStoreAt(t, path)
  	ctx := t.Context()

  	before, err := s.Stats(ctx)
  	require.NoError(t, err)

  	rels := make([]relindex.Release, 0, 200)
  	for i := range 200 {
  		rels = append(rels, rel("nzbgeek", "g"+strconv.Itoa(i), "The Matrix 1999 "+strconv.Itoa(i)))
  	}
  	mustUpsert(t, ctx, s, 200, rels...)

  	after, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.Greater(t, after.SizeBytes, before.SizeBytes)
  	require.FileExists(t, path+"-wal")
  }
  ```

  Add `"strconv"` to the imports. Run
  `go test -race ./pkg/relindex/... -run 'TestPrune|TestStats'`. Expect the five
  `TestPrune` cases to fail with `relindex: Prune not implemented` and the three
  `TestStats` cases to pass (Step 7 wrote `Stats`).

- [ ] **Step 27: Implement `Prune` in `pkg/relindex/maint.go`.**

  Delete the `Prune` stub from `store.go`.

  ```go
  // Prune deletes every release fetched before olderThan and returns how many
  // rows went.
  //
  // It is a pure function of its argument: it reads no clock, holds no timer and
  // starts no goroutine. Spec §6.2's "sweep every 10 min (72h)" is a SCHEDULE,
  // and the schedule belongs to indexarr's run loop, not to the store. A store
  // that spawned its own sweeper would give every test and every e2e a
  // background writer it did not ask for, and would keep writing after the
  // caller had stopped using it.
  //
  // The releases_ad trigger removes each deleted row's terms from the FTS5
  // index. Nothing here touches releases_fts directly.
  //
  // The file does NOT shrink: SQLite frees the pages for reuse rather than
  // returning them to the filesystem. That is intended. With a 72h window the
  // corpus reaches a high-water mark and stays there, and the freed pages absorb
  // the next three days of inserts. Do not "fix" this with VACUUM -- it rewrites
  // the whole database while holding an exclusive lock, which would stall every
  // search for the duration, every ten minutes.
  func (s *sqliteStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
  	if olderThan.IsZero() {
  		// The zero time's UnixNano overflows int64 into a large negative
  		// number, so this would delete nothing while reporting success.
  		return 0, fmt.Errorf("%w: olderThan is the zero time", ErrInvalidArg)
  	}

  	s.wmu.Lock()
  	defer s.wmu.Unlock()

  	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE fetched_at < ?`, olderThan.UTC().UnixNano())
  	if err != nil {
  		return 0, fmt.Errorf("relindex: prune: %w", err)
  	}
  	n, err := res.RowsAffected()
  	if err != nil {
  		return 0, fmt.Errorf("relindex: prune rows: %w", err)
  	}
  	return int(n), nil
  }
  ```

  Run `go test -race ./pkg/relindex/... -run 'TestPrune|TestStats'` and expect
  all eight green.

- [ ] **Step 28: Write the concurrency test, `pkg/relindex/concurrency_test.go`.**

  ```go
  // <GPL-3.0 header from hack/boilerplate.go.txt>

  package relindex_test

  import (
  	"strconv"
  	"sync"
  	"testing"
  	"time"

  	"github.com/stretchr/testify/assert"
  	"github.com/stretchr/testify/require"

  	"github.com/mediactl/clustarr/pkg/relindex"
  )

  func TestStoreIsSafeUnderConcurrentWritersAndReaders(t *testing.T) {
  	// indexarr is one replica, so there is one writer PROCESS -- but inside
  	// it the RSS worker and the search service both write, concurrently, on
  	// different goroutines. SQLite allows exactly one writer at a time; the
  	// store serialises them with a mutex so the loser waits instead of
  	// getting SQLITE_BUSY. Readers under WAL never block and take no lock.
  	//
  	// Must be run with -race. It is the only test here that would notice a
  	// data race on the store's own fields.
  	const (
  		writers      = 4
  		readers      = 4
  		perWriter    = 25
  		pruners      = 1
  	)

  	s := newStore(t)
  	ctx := t.Context()
  	var wg sync.WaitGroup

  	for w := range writers {
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			indexer := "indexer-" + strconv.Itoa(w)
  			for i := range perWriter {
  				r := rel(indexer, "g"+strconv.Itoa(i), "The Matrix 1999 "+strconv.Itoa(i))
  				r.TitleNorm = "matrix 1999 " + strconv.Itoa(i)
  				r.FetchedAt = fetchedAt.Add(time.Duration(i) * time.Second)
  				n, err := s.Upsert(ctx, []relindex.Release{r})
  				if !assert.NoError(t, err) {
  					return
  				}
  				assert.Equal(t, 1, n)
  			}
  		}()
  	}

  	for range readers {
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			for range perWriter {
  				_, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 50})
  				if !assert.NoError(t, err) {
  					return
  				}
  				if _, err := s.Stats(ctx); !assert.NoError(t, err) {
  					return
  				}
  			}
  		}()
  	}

  	for range pruners {
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			for range perWriter {
  				// Older than anything written, so it competes for the
  				// write lock without changing the expected row count.
  				if _, err := s.Prune(ctx, fetchedAt.Add(-24*time.Hour)); !assert.NoError(t, err) {
  					return
  				}
  			}
  		}()
  	}

  	wg.Wait()

  	st, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.EqualValues(t, writers*perWriter, st.Releases)
  	require.EqualValues(t, writers, st.Indexers)
  }

  func TestConcurrentUpsertsOfTheSameKeyInsertItExactlyOnce(t *testing.T) {
  	// Two goroutines racing on the same (indexer, guid) must produce one row
  	// and exactly one reported insert across all of them. If UNIQUE were
  	// missing or the count were derived from len(rels), this reports more.
  	s := newStore(t)
  	ctx := t.Context()

  	const goroutines = 8
  	var (
  		wg    sync.WaitGroup
  		mu    sync.Mutex
  		total int
  	)
  	for range goroutines {
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			n, err := s.Upsert(ctx, []relindex.Release{rel("nzbgeek", "g1", "The Matrix 1999")})
  			if !assert.NoError(t, err) {
  				return
  			}
  			mu.Lock()
  			total += n
  			mu.Unlock()
  		}()
  	}
  	wg.Wait()

  	require.Equal(t, 1, total, "exactly one goroutine may claim the insert")
  	st, err := s.Stats(ctx)
  	require.NoError(t, err)
  	require.EqualValues(t, 1, st.Releases)
  }
  ```

  Run `go test -race -count=4 ./pkg/relindex/... -run 'TestStoreIsSafe|TestConcurrentUpserts'`.
  Expect green on all four runs. A `SQLITE_BUSY` here means the write mutex is
  not held for the whole transaction; a race detector report means `sqliteStore`
  has a field being written outside it.

- [ ] **Step 29: Run the full gate and commit.**

  ```
  gofmt -l pkg/relindex
  go vet ./pkg/relindex/...
  go test -race -count=1 ./pkg/relindex/...
  golangci-lint-v2 run ./pkg/relindex/...
  ```

  `gofmt -l` must print nothing. `go vet` must print nothing. `golangci-lint-v2`
  must report no issues — note that `gocritic` will flag the `sb.WriteString`
  chain in `Search` if any fragment is built with `+` where a second
  `WriteString` would do; keep them as written.

  ```
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit \
    -m "feat(relindex): retention prune and corpus stats" \
    -- pkg/relindex
  ```

---

**Verification (the gate for this task):**

```
go test -race -count=1 ./pkg/relindex/...
```

- **Must show `ok  github.com/mediactl/clustarr/pkg/relindex`** with no `[no test files]`, no `SKIP` and no `FAIL`.
- **Expect roughly 1.5 s to 6 s of wall time** under `-race`. Every test in this
  package creates a real SQLite file in a real `t.TempDir()`, runs the DDL,
  builds an FTS5 index and fsyncs a WAL. **A result line reading `ok ... 0.0Xs`
  means the suite did not touch SQLite** — the likely causes are a `t.Skip` left
  behind from Step 7, or a build constraint. Investigate before believing it.
  This is the same failure mode `CLAUDE.md` records for `pkg/crdcheck`: a suite
  that finishes in milliseconds skipped.
- Run `go test -race -count=1 -v ./pkg/relindex/... | grep -c '^=== RUN'` and
  expect **at least 70** run lines (the top-level functions plus the subtests in
  `TestMatchExpr...`, `TestUpsertRejectsAnIncompleteRelease` and
  `TestSearchSurvivesHostileQueryText`).
- `go test -race -count=4 ./pkg/relindex/... -run 'TestStoreIsSafe|TestConcurrentUpserts'`
  must be green four times in a row. Once is not evidence for a concurrency test.
- `go build ./pkg/relindex/...` — never a bare `go build`; it drops a binary in
  the repo root.
- `git status --porcelain go.mod go.sum` must print nothing. If it does not, a
  `go get` ran in this task and the controller must be told.

**Carried items for the controller** (do not fix them here; they are other
tasks' paths):

- D1-8 wires `/readyz` to a `Stats` call and schedules `Prune(now.Add(-72h))` on
  a 10-minute ticker, per spec §6.2. Neither belongs in `pkg/relindex`.
- D1-5 and D1-7 must normalise `Query.Text` and `Release.TitleNorm` with the
  **same** function or the index answers nothing. Neither `02-interfaces.md` nor
  the spec says which function; `release.CleanTitle` is the only candidate in
  the tree.
- A `clustarr_relindex_*` metrics set (rows, on-disk bytes, prune duration) is
  worth having, but `pkg/relindex` imports nothing from the module and should
  stay that way — the gauge belongs in `indexarr`, fed from `Stats`.

**Where the sources disagree — flagged, not papered over:**

1. **Package name.** Spec §6.2 says `indexarr/releaseindex.Store`;
   `02-interfaces.md` says `pkg/relindex` and this task is titled for it. The
   interface contract is the one two tasks share, so `pkg/relindex` wins here —
   but a `pkg/` location also makes the store importable by the M6 facade and by
   a future Postgres implementation without pulling in `indexarr`'s deps, which
   is the ADR's stated reason for the interface existing. **The spec's §6.2
   sentence should be amended to `pkg/relindex.Store` in D1-0's spec-edit
   commit**, alongside the R7 table edit it is already making.
2. **Stored columns.** Spec §6.2 requires "columns from `pkg/release` (source,
   resolution, modifier, codec, hdr, audio, languages, group, year, season,
   episode, ids)". The fixed `Release` struct has none of them: only `Group`
   survives as a column, and everything else lives inside the opaque
   `InfoJSON`. That is a real narrowing. It is also the right one — those twelve
   fields are not in the `Query` struct either, so no caller can filter on them,
   and promoting them to columns now would ship twelve unqueryable columns. But
   it means **`rpc.indexarr.query` (Ruling R4) cannot filter by resolution or
   source**, and the spec sentence promises it can. Flagged for the controller:
   either amend §6.2 or open it as an M6 carried item.
3. **`expires_at`.** Spec §6.2 names an `expires_at` column; the fixed `Prune`
   signature takes `olderThan time.Time`, which makes the retention window the
   caller's arithmetic and a stored expiry redundant. This task stores no
   `expires_at` and prunes on `fetched_at`. Same amendment.
4. **`Query` cannot express what ADR-0003's context describes.** The ADR says
   releases "must then be queryable: full-text over release titles, filtered by
   category, indexer, age, size and seeders, ranked, and paged." The fixed
   `Query` has no size filter, no seeders filter, no offset and no ranking
   control. Not resolved here — the interface is fixed and ADR context is not
   normative — but the ADR's paragraph overstates what D1 ships.
5. **`Query.Since` is undefined in the contract.** `02-interfaces.md` gives it
   no comment, and `published_at` and `fetched_at` are both defensible readings.
   This task picks `fetched_at` and pins it with
   `TestPruneAndSinceDoNotDropDatelessReleases`, because the `published_at`
   reading silently drops every dateless release. **If D1-5 or D1-7 assumed
   `published_at`, that is a cross-task defect and the controller should settle
   it before either lands.**

---

### Task D1-4: the `IndexerDefinition` and `IndexerProxy` controllers

**Files:**
- Create: `indexarr/controller/indexerdefinition/{doc,controller}.go`, `controller_envtest_test.go`
- Create: `indexarr/controller/indexerproxy/{doc,controller}.go`, `controller_envtest_test.go`

**Path ownership:** `indexarr/controller/indexerdefinition/` and `indexarr/controller/indexerproxy/`. Nothing else — `indexarr/run.go` is Task D1-8's.

**Interfaces — Consumes:** `pkg/k8s.PatchStatus`, `k8s.ManagerIndexarr` (these two objects have **one** writer each, so the D1-0 split does not apply to them), `pkg/obs/logging`, `pkg/obs/tracing`.
**Interfaces — Produces:** `indexerdefinition.NewReconciler(...).SetupWithManager(mgr)` and `indexerproxy.NewReconciler(...).SetupWithManager(mgr)`, documented in each package's `doc.go` as the exact call Task D1-8 must make.

**Scope boundary, narrowed 2026-09-22 after D1-4 read it literally and had to argue back.** `pkg/cardigann` is not *wired up* in this phase, which means **no `Engine`, no login, no search, no indexer registration**. It does NOT mean "no import": `Validate`, `Load` and `Capabilities` are pure functions, and `Capabilities`' own doc comment says it is the shape `IndexerDefinitionStatus.Caps` is populated from. Taken literally the old wording left `status.id`/`name`/`caps` permanently empty, contradicted this task's own Steps 2 and 6, and would have made the release-regression test near-vacuous by leaving only two owned fields. `IndexerDefinition` here validates and reports; it does not load a Cardigann definition, log into a tracker or run a search. Wiring Cardigann into `IndexerDefinition`/`IndexerProxy` is M6 (Phase G), and Phase G inherits a recorded list of eleven unimplemented Cardigann features — three documented, eight found during D1's research and not documented anywhere before. Building against those now would build against sand.

- [ ] **Step 1: Read the two CRDs and write down the owned field set**

Read `api/index/v1alpha1/indexerdefinition_types.go` and `indexerproxy_types.go` in full — `docs/research/phase-d1-indexarr-spec.md` §4.2 and §4.3 reproduce them field by field. Before writing any code, write the complete list of status fields each controller owns into its `doc.go`. Every apply must declare all of them: server-side apply **replaces** a manager's ownership set per apply, so a field omitted on one path is released, and this project has hit that eight times.

- [ ] **Step 2: Write the failing envtest for `IndexerDefinition`**

Assert the reconcile sets `observedGeneration`, a `Ready` condition carrying that generation, and the `status.caps` summary derived from the spec (the field is `Caps`; `CapsSummary` is its *type*). Use `newTestClient(t)` from the established pattern — the suite skips cleanly when `KUBEBUILDER_ASSETS` is unset, and **a suite that finishes in milliseconds skipped rather than passed**.

```go
func TestIndexerDefinitionReportsReadyAndSummary(t *testing.T) {
	c, stop := newTestClient(t)
	defer stop()
	// ... create a definition with a known spec, then:
	require.Eventually(t, func() bool {
		var got indexv1alpha1.IndexerDefinition
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		return got.Status.ObservedGeneration == got.Generation &&
			k8s.FindCondition(got.Status.Conditions, indexv1alpha1.IndexerDefinitionConditionValid) != nil
	}, 10*time.Second, 50*time.Millisecond)
}
```

- [ ] **Step 3: Run it and watch it fail**

```bash
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
go test -count=1 -run TestIndexerDefinitionReportsReadyAndSummary ./indexarr/controller/indexerdefinition/
```

Expected: FAIL on the `Eventually` — no reconciler exists, so nothing ever sets the status.

- [ ] **Step 4: Write the reconciler**

Package-level `+kubebuilder:rbac` markers — **package level, not on the function.** controller-gen silently ignores a marker attached to a declaration, which in Phase C meant four controllers' rules were never generated at all and would have been `Forbidden` on a real cluster while every envtest passed, because envtest does not enforce RBAC.

```go
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
```

Note the Events group is `""` (core/v1), because the recorder from `mgr.GetEventRecorderFor` writes core Events. Four catalogarr controllers declared `events.k8s.io` and were wrong.

Reconcile: `RecoverPanic`, a 5-minute `ReconciliationTimeout`, `RequeueAfter` only, `reconcile.TerminalError` for an invalid spec, a span wrapping the whole reconcile, and one `k8s.PatchStatus` that declares the complete owned set.

- [ ] **Step 5: Run the test and see it pass**

- [ ] **Step 6: Add the release-on-early-return regression test**

This is the test the last phase kept missing. Drive the definition to a **real steady state** — status populated by a successful reconcile — then trigger the early-return path, and assert the previously-set fields **survive**:

```go
// A blank object cannot observe a release: there is nothing to release.
// Drive it to steady state first, then fail it.
require.Eventually(t, /* status fully populated */)
// ... now make the spec invalid, or the dependency unavailable ...
var after indexv1alpha1.IndexerDefinition
require.NoError(t, c.Get(ctx, key, &after))
assert.NotEmpty(t, after.Status.Caps.Modes,
	"a transient failure released status.caps")
```

Confirm it fails if you make the early return build a conditions-only apply.

- [ ] **Step 7: Repeat Steps 2–6 for `IndexerProxy`**

Same shape, its own owned set (`observedGeneration`, conditions, and the reachability fields the CRD defines). Write the code out rather than referring back — plan steps get read out of order.

- [ ] **Step 8: Regenerate RBAC and commit**

```bash
make manifests && git status --short   # controller-gen UNIONS into existing rules;
                                       # expect a few merged lines, not new blocks
go test -count=1 -race ./indexarr/controller/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(indexarr): IndexerDefinition and IndexerProxy controllers" -- indexarr/controller/indexerdefinition indexarr/controller/indexerproxy config/rbac charts
```

**Done when:** both controllers reconcile, both declare a complete owned set on every path including early returns, both have a regression test that drives real steady state first and provably fails against a partial apply, RBAC markers are package-level and generated, and the envtest genuinely ran (seconds, not milliseconds).
### Task D1-3: the `Indexer` reconciler

Reconciles `index.clustarr.io/v1alpha1 Indexer`: validates the spec, probes
Torznab caps, resolves protocol and privacy, resolves the session-Secret
reference, derives the four conditions, and exports the **pure** health and
backoff functions that D1-5's search fan-out and D1-7's RSS worker call.

Spec obligations covered (§6.2's controller clause): *validate
definition/generic*, *probe caps*, *owned session Secret* (the reference half —
see the boundary note below), and Prowlarr's escalation table + 15-minute
startup grace. **Not** covered here: *login test* and *schedule RSS via
`WithScheduleAt`* — the Cardigann login is M6 (`phase-d1-libraries-api.md`
§3.12) and the RSS schedule is D1-7.

**Depends on:** D1-0 (the `indexarr-worker` field manager, the R5 doc-comment
fix), D1-1 (the reshaped `torznab.WithRateLimit`).
**Depended on by:** D1-5 (`Healthy`, `SupportsMode`, `RecordSuccess`,
`RecordFailure`, the shared `*ratelimit.Limiter`), D1-7 (the same),
D1-8 (`NewReconciler` + `SetupWithManager` from `setupControllers`).

---

#### Files

**Create**

| Path | Holds |
| --- | --- |
| `indexarr/controller/indexer/doc.go` | package doc; the R6 field-manager ownership table; the SSA rule this package obeys |
| `indexarr/controller/indexer/health.go` | `StartupGrace`, `EscalationTable`, `Escalation`, `RecordFailure`, `RecordSuccess`, `Healthy` |
| `indexarr/controller/indexer/health_test.go` | table tests for the ladder, the grace, the cap at 9, `Changed`, `Healthy` |
| `indexarr/controller/indexer/caps.go` | `SupportsMode`, `projectCaps` (`torznab.Caps` → `indexv1alpha1.Caps`), `capsAC` |
| `indexarr/controller/indexer/caps_test.go` | the R5 vocabulary pin, truncation and projection tests |
| `indexarr/controller/indexer/source.go` | `resolveSource`, `protocolFor`, `privacyFor`, `sessionSecretName`, `readSecret`, `rpsFor`, `buildClient`, `classify` |
| `indexarr/controller/indexer/source_test.go` | table tests for all of the above (no network, no cluster) |
| `indexarr/controller/indexer/controller.go` | `Reconciler`, `NewReconciler`, `Reconcile`, `patch`, RBAC markers, `SetupWithManager` |
| `indexarr/controller/indexer/controller_envtest_test.go` | envtest suite, incl. the two SSA-release regressions |

**Modify (generated — never hand-edited)**

| Path | Why |
| --- | --- |
| `config/rbac/role.yaml` | `make manifests` picks up this package's new markers |
| `charts/clustarr/templates/rbac.yaml` | the chart copy between the `BEGIN`/`END` sentinels; `cmd/clustarr`'s `TestChartRBACMatchesTheGeneratedRole` compares it byte-for-byte |

**Do not touch:** `indexarr/run.go` (D1-8 registers this reconciler),
`api/index/**` (D1-0 owns the doc-comment fix), `pkg/torznab/**` (D1-1),
`pkg/k8s/fieldmanager.go` (D1-0), `go.mod` / `go.sum` (D1-0, serially).

#### Path ownership

`indexarr/controller/indexer/**` is exclusively this task's. The two generated
files above are shared with every other D1 task that adds RBAC markers, so
regenerate them **last** (step 37) and commit them path-scoped in the same
commit as the markers that produced them.

#### Interfaces — Consumes

```go
// pkg/k8s
func PatchStatus[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
const ManagerIndexarr FieldManager = "indexarr"            // this task applies as this, and ONLY this
const ManagerIndexarrWorker FieldManager = "indexarr-worker" // D1-0 adds it; this task never applies as it
func MarkTrue(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkFalse(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkUnknown(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkReady(obj client.Object, conditions *[]metav1.Condition, ready bool, reason, message string, args ...any) bool
func SetCondition(obj client.Object, conditions *[]metav1.Condition, c metav1.Condition) bool
func NewCondition(condType string, status metav1.ConditionStatus, reason, message string, args ...any) metav1.Condition
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration
func GenerationChanged() predicate.Predicate
const ReasonReconciled, ReasonInvalidSpec, ReasonDisabled, ReasonDependencyNotReady, ReasonThrottled, ReasonReconcileError = ...

// pkg/torznab  (post-D1-1; see the note under step 20)
func NewClient(baseURL, apikey string, opts ...ClientOption) (*Client, error)
func WithTimeout(d time.Duration) ClientOption
func WithRateLimit(l *ratelimit.Limiter, key string) ClientOption // RESHAPED BY D1-1 -- see step 20
func (c *Client) Caps(ctx context.Context) (Caps, error)
type Caps struct { ServerTitle string; LimitsDefault, LimitsMax int; Modes map[SearchMode]Searching; Categories []newznab.Category; Tags map[string]string }
type Searching struct { Available bool; SupportedParams []string; SearchEngine string }
type SearchMode string // ModeSearch "search", ModeTVSearch "tvsearch", ModeMovieSearch "movie", ModeMusicSearch "music", ModeAudioSearch "audio", ModeBookSearch "book"
type Error struct { Code ErrorCode; Description string; HTTPStatus int; RetryAfter time.Duration }
var ErrResponseTooLarge = errors.New("torznab: response body exceeds size limit")

// pkg/ratelimit
func New(defaults Config) *Limiter
func (l *Limiter) SetConfig(key string, cfg Config)
type Config struct { RPS float64; Burst int } // RPS <= 0 means unlimited; Burst <= 0 is treated as 1

// pkg/obs/{logging,tracing,metrics}
func logging.FromContext(ctx context.Context) *slog.Logger
func tracing.Start(ctx context.Context, name string) (context.Context, trace.Span)
var metrics.IndexerQueryDuration, metrics.IndexerQueriesTotal // labels: indexer, function / indexer, outcome
```

#### Interfaces — Produces

Verbatim from `02-interfaces.md`. Two other tasks import these; the signatures
are fixed.

```go
package indexer

// EscalationTable is Prowlarr's 10-entry backoff ladder; level is capped at 9.
// A failure inside StartupGrace of process start does not escalate.
const StartupGrace = 15 * time.Minute

// RecordFailure returns the escalation fields the caller must apply.
func RecordFailure(cur indexv1alpha1.IndexerStatus, now time.Time, reason string) Escalation

// RecordSuccess clears the escalation. It returns the zero Escalation when the
// indexer was already healthy, so a caller can skip a no-op apply.
func RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) Escalation

type Escalation struct {
    FailureLevel   int32
    DisabledUntil  *metav1.Time
    LastFailureAt  *metav1.Time
    LastFailureMsg string
    Changed        bool // false when applying this would be a no-op
}

// Healthy reports whether the indexer may be queried right now.
func Healthy(st indexv1alpha1.IndexerStatus, now time.Time) bool

// SupportsMode gates the caps check. mode MUST be a torznab.SearchMode value
// ("search", "tvsearch", "movie", "music", "audio", "book") -- see Ruling R5.
func SupportsMode(caps indexv1alpha1.Caps, mode string) bool
```

Plus two additions this task must make, both flagged for the plan controller to
ratify because they go beyond the verbatim block:

```go
// EscalationTable returns a copy of Prowlarr's ladder. The contract names it in
// prose without a signature; a function returning a fresh slice is used so no
// caller can mutate the ladder.
func EscalationTable() []time.Duration

// InitialFailure derives status.initialFailureAt, which Escalation has no field
// for but IndexerStatus does. Without it the caller cannot assemble a COMPLETE
// five-field declaration of the escalation set, and SSA would release
// status.initialFailureAt on every apply.
func (e Escalation) InitialFailure(cur indexv1alpha1.IndexerStatus) *metav1.Time
```

---

#### The two rules this task exists to get right

**Rule 1 — the owned set is complete on every apply, on every path.**
Server-side apply *replaces* a field manager's ownership set. Anything
`indexarr` sent last time and omits this time is **released**, which reads on
the object as "reset to zero". `CLAUDE.md` records eight forms this took in
Phase C; the worst is *an early-return path built a partial status and wiped
everything the happy path had set* — and the early return is always a transient
failure, so a healthy object gets gutted by a blip.

The mechanical defence here is two-part and both parts are mandatory:

1. `Reconcile` has **exactly one** `k8s.PatchStatus` call site, inside the
   `patch` helper. Every `return` in `Reconcile` is `return r.patch(...)`.
2. The struct `patch` renders is **seeded from the observed status** at the top
   of `Reconcile` (`ownedFrom(&idx)`), so a path that never reaches the caps
   probe still re-sends the caps that are already there.

The owned set may vary with the **spec shape** (a definition-backed Indexer has
no resolvable `status.protocol`, and the CRD's `enum: [torrent, usenet]` makes
sending `""` an apiserver rejection, so it must be omitted). It must never vary
with a **transient outcome**.

**Rule 2 — this reconciler never writes an escalation field.**
Per R6, `escalationLevel`, `disabledUntil`, `initialFailureAt`, `lastFailureAt`,
`lastFailure`, `queriesInWindow`, `grabsInWindow`, `lastRssAt`,
`lastRssNewCount` and `indexedReleases` belong to `indexarr-worker`. This
reconciler **reads** them (to derive the `Healthy` and `RateLimited` conditions
and to choose a requeue delay) and **exports** `RecordFailure`/`RecordSuccess`
for the workers to apply. A caps-probe failure therefore moves conditions and
the requeue delay, and does *not* move `escalationLevel`. Writing the escalation
set here under `indexarr-worker` would re-create exactly the trap R6 forbids,
because this reconciler does not know the counters and its apply would release
them.

---

#### Steps

**Scaffolding**

- [ ] 1. Create `indexarr/controller/indexer/doc.go` with the GPL header from
  `hack/boilerplate.go.txt` and the package doc. It carries the ownership table,
  because R6 requires the split to be written into both packages' doc comments:

  ```go
  // Package indexer reconciles index.clustarr.io Indexer objects: it validates
  // the spec, probes Torznab caps, resolves the protocol, the privacy class and
  // the session-Secret reference, and derives the Ready, Authenticated, Healthy
  // and RateLimited conditions. It also exports the health and backoff
  // functions -- RecordFailure, RecordSuccess, Healthy -- that indexarr's RSS
  // worker and search fan-out call. Those are PURE: they compute the next
  // status from the current one and never touch the apiserver, so the caller
  // decides which field manager applies the result.
  //
  // # Field-manager split (design spec §2, Phase D1 ruling R6)
  //
  // Two writers reach IndexerStatus and each owns a disjoint set, because
  // server-side apply REPLACES a manager's ownership set on every apply rather
  // than merging it -- one shared name means each writer silently releases the
  // other's fields.
  //
  //	k8s.ManagerIndexarr ("indexarr", this package):
  //	    observedGeneration, conditions, protocol, privacy, caps,
  //	    sessionSecretRef
  //	k8s.ManagerIndexarrWorker ("indexarr-worker", indexarr/worker/rss and
  //	indexarr/search):
  //	    escalationLevel, disabledUntil, initialFailureAt, lastFailureAt,
  //	    lastFailure, queriesInWindow, grabsInWindow, lastRssAt,
  //	    lastRssNewCount, indexedReleases
  //
  // This package never applies as ManagerIndexarrWorker. It reads the worker's
  // fields to derive conditions and a requeue delay, nothing more.
  package indexer
  ```

  Run `go build ./indexarr/...` and see it succeed.

**Health and backoff — pure, no client, no cluster (TDD)**

- [ ] 2. Write `health_test.go` (package `indexer`, in-package so it can set the
  process-start seam) with the ladder test only. It will not compile yet:

  ```go
  func TestEscalationTableIsProwlarrsTenEntryLadder(t *testing.T) {
      want := []time.Duration{0, time.Minute, 5 * time.Minute, 15 * time.Minute,
          30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour,
          12 * time.Hour, 24 * time.Hour}
      require.Equal(t, want, EscalationTable())
      require.Len(t, EscalationTable(), 10, "level is capped at len-1 = 9")

      got := EscalationTable()
      got[0] = time.Hour
      require.Equal(t, time.Duration(0), EscalationTable()[0], "EscalationTable must return a copy")
  }
  ```

- [ ] 3. Run `go test ./indexarr/controller/indexer/...` and see it fail to build
  with `undefined: EscalationTable`. That failure is the point: confirm it before
  writing any implementation.

- [ ] 4. Create `health.go` (GPL header) with the ladder and the process-start
  seam:

  ```go
  // StartupGrace is Prowlarr's MinimumTimeSinceStartup: a failure inside this
  // window of process start does not escalate, so a restart does not disable
  // every indexer at once.
  const StartupGrace = 15 * time.Minute

  // maxFailureMsg caps status.lastFailure. The CRD carries no MaxLength and an
  // indexer's error body can be arbitrarily long; an unbounded status string is
  // how operators melt etcd.
  const maxFailureMsg = 512

  // escalationTable is Prowlarr's EscalationBackOff.Periods
  // [0,60,300,900,1800,3600,10800,21600,43200,86400]s (design spec §6.2).
  var escalationTable = [...]time.Duration{
      0, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
      time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
  }

  // maxEscalationLevel is len(escalationTable)-1.
  const maxEscalationLevel = int32(len(escalationTable)) - 1

  // EscalationTable returns a copy of the ladder, in order.
  func EscalationTable() []time.Duration {
      out := make([]time.Duration, len(escalationTable))
      copy(out, escalationTable[:])
      return out
  }

  // processStart is when this process started. RecordFailure's signature is
  // fixed by the Phase D1 interface contract and takes no start parameter, so
  // the grace window is measured against this package variable. In-package
  // tests assign it directly; nothing outside the package can.
  var processStart = time.Now()
  ```

  Run the test and see it pass.

- [ ] 5. Add the `Escalation` struct and `RecordFailure` to `health.go`, verbatim
  from the contract plus the derivation method:

  ```go
  // Escalation is the complete escalation field set a caller must apply. Every
  // field is always populated: the caller applies all five under
  // k8s.ManagerIndexarrWorker in one apply, because a partial apply releases
  // the fields it omits.
  type Escalation struct {
      FailureLevel   int32
      DisabledUntil  *metav1.Time
      LastFailureAt  *metav1.Time
      LastFailureMsg string
      Changed        bool
  }

  // InitialFailure derives status.initialFailureAt, the sixth field of the
  // escalation set. Escalation has no field for it because the interface
  // contract fixes the struct, but IndexerStatus does, so the caller must send
  // it or SSA releases it.
  func (e Escalation) InitialFailure(cur indexv1alpha1.IndexerStatus) *metav1.Time {
      if e.LastFailureAt == nil {
          return nil // the run is over
      }
      if cur.InitialFailureAt != nil {
          return cur.InitialFailureAt
      }
      return e.LastFailureAt
  }

  // RecordFailure returns the escalation fields the caller must apply.
  func RecordFailure(cur indexv1alpha1.IndexerStatus, now time.Time, reason string) Escalation {
      level := cur.EscalationLevel
      if now.Sub(processStart) >= StartupGrace {
          level = min(level+1, maxEscalationLevel)
      }
      if level < 0 {
          level = 0
      }
      at := metav1.NewTime(now)
      e := Escalation{
          FailureLevel:   level,
          LastFailureAt:  &at,
          LastFailureMsg: truncate(reason, maxFailureMsg),
          Changed:        true,
      }
      // A zero period is not a disable. Prowlarr stores DisabledTill = now,
      // which makes "is it disabled?" an equality edge case; nil is
      // unambiguous and is what Healthy reads.
      if d := escalationTable[level]; d > 0 {
          until := metav1.NewTime(now.Add(d))
          e.DisabledUntil = &until
      }
      return e
  }

  func truncate(s string, n int) string {
      if len(s) <= n {
          return s
      }
      return s[:n]
  }
  ```

  Run `go test ./indexarr/controller/indexer/...`; it still only exercises the
  ladder. Commit nothing yet.

- [ ] 6. Add the escalation tests to `health_test.go` and see them pass:

  ```go
  func TestRecordFailureEscalatesAndCapsAtNine(t *testing.T) {
      processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
      t.Cleanup(func() { processStart = time.Now() })
      now := processStart.Add(time.Hour) // well outside the grace

      cases := []struct {
          from int32
          want int32
          dur  time.Duration
      }{
          {from: 0, want: 1, dur: time.Minute},
          {from: 4, want: 5, dur: time.Hour},
          {from: 8, want: 9, dur: 24 * time.Hour},
          {from: 9, want: 9, dur: 24 * time.Hour},
          {from: 40, want: 9, dur: 24 * time.Hour}, // a corrupted status cannot index out of range
      }
      for _, tc := range cases {
          got := RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: tc.from}, now, "boom")
          require.Equal(t, tc.want, got.FailureLevel)
          require.True(t, got.Changed)
          require.NotNil(t, got.DisabledUntil)
          require.Equal(t, now.Add(tc.dur), got.DisabledUntil.Time)
          require.Equal(t, "boom", got.LastFailureMsg)
      }
  }

  func TestRecordFailureInsideStartupGraceDoesNotEscalate(t *testing.T) {
      processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
      t.Cleanup(func() { processStart = time.Now() })

      inside := processStart.Add(StartupGrace - time.Second)
      got := RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: 3}, inside, "boom")
      require.Equal(t, int32(3), got.FailureLevel, "grace must not escalate")
      require.True(t, got.Changed, "the failure is still recorded")

      atBoundary := processStart.Add(StartupGrace)
      require.Equal(t, int32(4), RecordFailure(indexv1alpha1.IndexerStatus{EscalationLevel: 3}, atBoundary, "boom").FailureLevel)
  }

  func TestRecordFailureAtLevelZeroDoesNotDisable(t *testing.T) {
      processStart = time.Now()
      t.Cleanup(func() { processStart = time.Now() })
      got := RecordFailure(indexv1alpha1.IndexerStatus{}, processStart, "boom")
      require.Equal(t, int32(0), got.FailureLevel)
      require.Nil(t, got.DisabledUntil, "escalationTable[0] is 0s, which is not a disable")
  }

  func TestRecordFailureTruncatesTheMessage(t *testing.T) {
      processStart = time.Now()
      t.Cleanup(func() { processStart = time.Now() })
      got := RecordFailure(indexv1alpha1.IndexerStatus{}, processStart, strings.Repeat("x", 4096))
      require.Len(t, got.LastFailureMsg, maxFailureMsg)
  }
  ```

- [ ] 7. Write the `RecordSuccess` tests first, and see them fail with
  `undefined: RecordSuccess`:

  ```go
  func TestRecordSuccessOnAHealthyIndexerIsANoOp(t *testing.T) {
      got := RecordSuccess(indexv1alpha1.IndexerStatus{}, time.Now())
      require.Equal(t, Escalation{}, got)
      require.False(t, got.Changed, "a caller must be able to skip the apply entirely")
  }

  func TestRecordSuccessDeEscalatesOneStepAndClearsTheDisable(t *testing.T) {
      now := time.Now()
      until := metav1.NewTime(now.Add(time.Hour))
      at := metav1.NewTime(now.Add(-time.Minute))
      cur := indexv1alpha1.IndexerStatus{
          EscalationLevel: 5, DisabledUntil: &until,
          InitialFailureAt: &at, LastFailureAt: &at, LastFailure: "boom",
      }
      got := RecordSuccess(cur, now)
      require.True(t, got.Changed)
      require.Equal(t, int32(4), got.FailureLevel)
      require.Nil(t, got.DisabledUntil)
      require.Equal(t, &at, got.LastFailureAt, "history is carried, not dropped, while the level is > 0")
      require.Equal(t, "boom", got.LastFailureMsg)
      require.Equal(t, &at, got.InitialFailure(cur))
  }

  func TestRecordSuccessAtLevelOneClearsTheWholeRun(t *testing.T) {
      now := time.Now()
      at := metav1.NewTime(now.Add(-time.Minute))
      cur := indexv1alpha1.IndexerStatus{EscalationLevel: 1, InitialFailureAt: &at, LastFailureAt: &at, LastFailure: "boom"}
      got := RecordSuccess(cur, now)
      require.True(t, got.Changed)
      require.Equal(t, int32(0), got.FailureLevel)
      require.Nil(t, got.DisabledUntil)
      require.Nil(t, got.LastFailureAt)
      require.Empty(t, got.LastFailureMsg)
      require.Nil(t, got.InitialFailure(cur), "the run is over")
  }
  ```

- [ ] 8. Implement `RecordSuccess` in `health.go` and see the tests pass:

  ```go
  // RecordSuccess clears the escalation. It returns the zero Escalation when
  // the indexer was already healthy, so a caller can skip a no-op apply.
  func RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) Escalation {
      _ = now // the ladder de-escalates by step, not by elapsed time
      if cur.EscalationLevel <= 0 && cur.DisabledUntil == nil &&
          cur.LastFailureAt == nil && cur.InitialFailureAt == nil && cur.LastFailure == "" {
          return Escalation{}
      }
      level := cur.EscalationLevel - 1
      if level < 0 {
          level = 0
      }
      e := Escalation{FailureLevel: level, Changed: true}
      if level > 0 {
          // Still climbing down; keep the run's history so the applied set
          // stays a complete declaration.
          e.LastFailureAt = cur.LastFailureAt
          e.LastFailureMsg = cur.LastFailure
      }
      return e
  }
  ```

- [ ] 9. Write the `Healthy` test, see it fail, then implement it and see it pass:

  ```go
  func TestHealthy(t *testing.T) {
      now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
      past := metav1.NewTime(now.Add(-time.Second))
      future := metav1.NewTime(now.Add(time.Hour))
      require.True(t, Healthy(indexv1alpha1.IndexerStatus{}, now), "never-failed is healthy")
      require.True(t, Healthy(indexv1alpha1.IndexerStatus{EscalationLevel: 7}, now), "a level with no disable is still queryable")
      require.True(t, Healthy(indexv1alpha1.IndexerStatus{DisabledUntil: &past}, now))
      require.False(t, Healthy(indexv1alpha1.IndexerStatus{DisabledUntil: &future}, now))
      require.True(t, Healthy(indexv1alpha1.IndexerStatus{DisabledUntil: &metav1.Time{Time: now}}, now), "the boundary re-enables")
  }
  ```

  ```go
  // Healthy reports whether the indexer may be queried right now. It looks only
  // at status.disabledUntil: spec.enabled is the operator's switch and the
  // limit windows are the RateLimited condition's business, and a caller that
  // conflates the three cannot tell an operator why an indexer went quiet.
  func Healthy(st indexv1alpha1.IndexerStatus, now time.Time) bool {
      return st.DisabledUntil == nil || !now.Before(st.DisabledUntil.Time)
  }
  ```

- [ ] 10. Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com
  commit -m "feat(indexarr): Prowlarr's escalation ladder as pure functions" --
  indexarr/controller/indexer/doc.go indexarr/controller/indexer/health.go
  indexarr/controller/indexer/health_test.go`

**Caps — the R5 vocabulary and the projection (TDD)**

- [ ] 11. Write `caps_test.go` with the vocabulary pin **first**. This is the
  test R5 asks for: a wrong mode vocabulary makes `SupportsMode` silently never
  match, and an indexer that is never queried looks exactly like an indexer with
  nothing to offer.

  ```go
  // TestModeVocabularyIsTorznabsWireValues pins status.caps.modes' keys to
  // torznab.SearchMode. Ruling R5: the CRD doc comments used to name
  // "tv-search"/"movie-search", which are the caps-XML ELEMENT names, not the
  // t= wire values. Nothing enforces the map's keys (no enum marker is possible
  // on map keys), so this test is the enforcement.
  func TestModeVocabularyIsTorznabsWireValues(t *testing.T) {
      all := []torznab.SearchMode{torznab.ModeSearch, torznab.ModeTVSearch,
          torznab.ModeMovieSearch, torznab.ModeMusicSearch,
          torznab.ModeAudioSearch, torznab.ModeBookSearch}
      require.Equal(t, []string{"search", "tvsearch", "movie", "music", "audio", "book"},
          func() (out []string) {
              for _, m := range all {
                  out = append(out, string(m))
              }
              return
          }())

      for _, m := range all {
          got := projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
              m: {Available: true, SupportedParams: []string{"q"}},
          }})
          require.True(t, SupportsMode(got, string(m)), "mode %q must project to a key SupportsMode matches", m)
      }

      wide := projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
          torznab.ModeTVSearch:    {Available: true, SupportedParams: []string{"q", "tvdbid"}},
          torznab.ModeMovieSearch: {Available: true, SupportedParams: []string{"q", "imdbid"}},
      }})
      for _, wrong := range []string{"tv-search", "movie-search", "music-search", "book-search", "tvSearch", ""} {
          require.False(t, SupportsMode(wide, wrong), "the caps-XML element vocabulary must NOT match: %q", wrong)
      }
  }
  ```

- [ ] 12. Run the test, see it fail to build (`undefined: projectCaps`,
  `undefined: SupportsMode`).

- [ ] 13. Create `caps.go` (GPL header) with `SupportsMode` and `projectCaps`:

  ```go
  // SupportsMode gates the caps check. mode MUST be a torznab.SearchMode value
  // ("search", "tvsearch", "movie", "music", "audio", "book") -- ruling R5. A
  // zero Caps (an Indexer whose caps have not been probed) supports nothing,
  // so a caller must treat status.caps == nil as "not yet probed", not as
  // "supports everything".
  func SupportsMode(caps indexv1alpha1.Caps, mode string) bool {
      if len(caps.Modes) == 0 || mode == "" {
          return false
      }
      _, ok := caps.Modes[mode]
      return ok
  }

  // maxCapsItems mirrors the CRD's +kubebuilder:validation:MaxItems=200 on
  // status.caps.categories and on each Category.Sub. Exceeding it is an
  // apiserver rejection of the whole apply, so the projection truncates rather
  // than letting one chatty indexer make its own status unwritable.
  const maxCapsItems = 200

  // projectCaps maps the wire caps onto the CRD's Caps.
  //
  // Only AVAILABLE modes are projected: SupportsMode treats key presence as
  // availability, so projecting an unavailable mode would advertise a search
  // the indexer answers with error 203.
  func projectCaps(c torznab.Caps) indexv1alpha1.Caps {
      out := indexv1alpha1.Caps{
          LimitsMax:     clampInt32(c.LimitsMax),
          LimitsDefault: clampInt32(c.LimitsDefault),
          // SupportsRawSearch is the plain t=search mode's searchEngine
          // attribute: the free-text q= fallback the search fan-out uses when
          // caps gate out every id parameter.
          SupportsRawSearch: c.Modes[torznab.ModeSearch].SearchEngine == "raw",
      }
      if len(c.Modes) > 0 {
          out.Modes = make(map[string][]string, len(c.Modes))
          for mode, s := range c.Modes {
              if !s.Available {
                  continue
              }
              params := append([]string(nil), s.SupportedParams...)
              sort.Strings(params) // a stable map value keeps the apply idempotent
              out.Modes[string(mode)] = params
          }
      }
      cats := append([]newznab.Category(nil), c.Categories...)
      sort.Slice(cats, func(i, j int) bool { return cats[i].ID < cats[j].ID })
      if len(cats) > maxCapsItems {
          cats = cats[:maxCapsItems]
      }
      for _, cat := range cats {
          sub := append([]newznab.SubCategory(nil), cat.Sub...)
          sort.Slice(sub, func(i, j int) bool { return sub[i].ID < sub[j].ID })
          if len(sub) > maxCapsItems {
              sub = sub[:maxCapsItems]
          }
          projected := indexv1alpha1.Category{ID: int32(cat.ID), Name: cat.Name}
          for _, s := range sub {
              projected.Sub = append(projected.Sub, indexv1alpha1.SubCategory{ID: int32(s.ID), Name: s.Name})
          }
          out.Categories = append(out.Categories, projected)
      }
      return out
  }

  func clampInt32(n int) int32 {
      switch {
      case n < 0:
          return 0
      case n > math.MaxInt32:
          return math.MaxInt32
      default:
          return int32(n)
      }
  }
  ```

  Run the test and see it pass.

- [ ] 14. Add the projection tests to `caps_test.go` and see them pass:

  ```go
  func TestProjectCapsDropsUnavailableModes(t *testing.T) {
      got := projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
          torznab.ModeSearch:   {Available: true, SupportedParams: []string{"q"}},
          torznab.ModeBookSearch: {Available: false, SupportedParams: []string{"q", "author"}},
      }})
      require.True(t, SupportsMode(got, "search"))
      require.False(t, SupportsMode(got, "book"), "an unavailable mode must not be advertised")
  }

  func TestProjectCapsTruncatesToTheCRDsMaxItems(t *testing.T) {
      var cats []newznab.Category
      for i := range 300 {
          sub := make([]newznab.SubCategory, 0, 250)
          for j := range 250 {
              sub = append(sub, newznab.SubCategory{ID: newznab.CategoryID(j), Name: "s"})
          }
          cats = append(cats, newznab.Category{ID: newznab.CategoryID(i), Name: "c", Sub: sub})
      }
      got := projectCaps(torznab.Caps{Categories: cats})
      require.Len(t, got.Categories, maxCapsItems)
      require.Len(t, got.Categories[0].Sub, maxCapsItems)
      require.Equal(t, int32(0), got.Categories[0].ID, "truncation is by sorted id, so it is deterministic")
  }

  func TestProjectCapsIsIdempotent(t *testing.T) {
      in := torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
          torznab.ModeTVSearch: {Available: true, SupportedParams: []string{"tvdbid", "q", "season"}},
      }}
      require.Equal(t, projectCaps(in), projectCaps(in), "unstable ordering would apply a diff every reconcile")
  }

  func TestProjectCapsRawSearch(t *testing.T) {
      require.True(t, projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
          torznab.ModeSearch: {Available: true, SearchEngine: "raw"},
      }}).SupportsRawSearch)
      require.False(t, projectCaps(torznab.Caps{}).SupportsRawSearch)
  }
  ```

- [ ] 15. Add `capsAC` to `caps.go` — the inverse, building the apply
  configuration `patch` sends. Keep it next to `projectCaps` so the two cannot
  drift:

  ```go
  // capsAC renders Caps as an apply configuration. It sends every field,
  // including the zero values: server-side apply releases what a manager omits,
  // and LimitsMax dropping to 0 because an indexer reported 0 this round is a
  // different thing from LimitsMax being released.
  func capsAC(c indexv1alpha1.Caps) *indexac.CapsApplyConfiguration {
      ac := indexac.Caps().
          WithLimitsMax(c.LimitsMax).
          WithLimitsDefault(c.LimitsDefault).
          WithSupportsRawSearch(c.SupportsRawSearch)
      if len(c.Modes) > 0 {
          ac = ac.WithModes(c.Modes)
      }
      for _, cat := range c.Categories {
          catAC := indexac.Category().WithID(cat.ID).WithName(cat.Name)
          for _, s := range cat.Sub {
              catAC = catAC.WithSub(indexac.SubCategory().WithID(s.ID).WithName(s.Name))
          }
          ac = ac.WithCategories(catAC)
      }
      return ac
  }
  ```

  Run `go test ./indexarr/controller/indexer/...` and see everything still pass.

- [ ] 16. Commit path-scoped: `... commit -m "feat(indexarr): project Torznab
  caps onto the CRD with the R5 mode vocabulary" --
  indexarr/controller/indexer/caps.go indexarr/controller/indexer/caps_test.go`

**Spec resolution, the session reference and the per-host limiter (TDD)**

- [ ] 17. Write `source_test.go` covering `resolveSource`, then implement it.
  The CEL rule on `IndexerSpec` already enforces exactly-one-of, but a
  reconciler that trusts a CEL rule it did not write is one apiserver upgrade
  from a nil dereference.

  ```go
  func TestResolveSource(t *testing.T) {
      def := "1337x"
      cases := []struct {
          name    string
          spec    indexv1alpha1.IndexerSpec
          want    sourceKind
          wantErr bool
      }{
          {name: "generic", spec: indexv1alpha1.IndexerSpec{Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent}}, want: sourceGeneric},
          {name: "bundled definition", spec: indexv1alpha1.IndexerSpec{Definition: &def}, want: sourceDefinition},
          {name: "definitionRef", spec: indexv1alpha1.IndexerSpec{DefinitionRef: &def}, want: sourceDefinitionRef},
          {name: "none", spec: indexv1alpha1.IndexerSpec{}, wantErr: true},
          {name: "two", spec: indexv1alpha1.IndexerSpec{Definition: &def, Generic: &indexv1alpha1.GenericNewznab{}}, wantErr: true},
      }
      for _, tc := range cases {
          t.Run(tc.name, func(t *testing.T) {
              got, err := resolveSource(tc.spec)
              if tc.wantErr {
                  require.Error(t, err)
                  return
              }
              require.NoError(t, err)
              require.Equal(t, tc.want, got)
          })
      }
  }
  ```

  ```go
  type sourceKind int

  const (
      sourceGeneric sourceKind = iota
      sourceDefinition
      sourceDefinitionRef
  )

  // resolveSource re-checks the spec's type-level CEL rule in Go. An invalid
  // spec is a reconcile.TerminalError, not a requeue: no amount of retrying
  // fixes it and a hot loop on a typo is how a controller burns an apiserver.
  func resolveSource(spec indexv1alpha1.IndexerSpec) (sourceKind, error) {
      var set int
      kind := sourceGeneric
      if spec.Generic != nil {
          set, kind = set+1, sourceGeneric
      }
      if spec.Definition != nil {
          set, kind = set+1, sourceDefinition
      }
      if spec.DefinitionRef != nil {
          set, kind = set+1, sourceDefinitionRef
      }
      if set != 1 {
          return kind, fmt.Errorf("indexer: exactly one of spec.definition, spec.definitionRef or spec.generic must be set, found %d", set)
      }
      return kind, nil
  }
  ```

- [ ] 18. Add the protocol, privacy and session-name helpers to `source.go`, with
  their tests. `status.protocol` carries `enum: [torrent, usenet]` in the
  generated CRD, so the empty string is an apiserver rejection, not a no-op:

  ```go
  // Privacy classes. They match IndexerDefinition's DefinitionType enum
  // (public|semiPrivate|private) so the two status fields read the same, even
  // though status.privacy itself carries no enum marker. Note that
  // pkg/cardigann.DefinitionType spells the middle one "semi-private"; M6 maps
  // between them.
  const (
      PrivacyPublic      = "public"
      PrivacySemiPrivate = "semiPrivate"
      PrivacyPrivate     = "private"
  )

  // protocolFor resolves status.protocol. It returns "" for a definition-backed
  // Indexer, whose protocol comes from the Cardigann definition (M6). The
  // caller MUST omit status.protocol when this returns "": the CRD schema is
  // enum: [torrent, usenet] and an explicit "" is rejected.
  func protocolFor(kind sourceKind, spec indexv1alpha1.IndexerSpec) commonv1alpha1.Protocol {
      if kind == sourceGeneric && spec.Generic != nil {
          return spec.Generic.Protocol
      }
      return ""
  }

  // privacyFor resolves status.privacy. §6.2 resolves it "from the definition",
  // and a generic upstream has none -- so it is derived from whether the
  // upstream is credentialled. Prowlarr's own generic Newznab and Torznab
  // indexers are both IndexerPrivacy.Private, because a generic upstream is
  // nearly always an API-keyed private tracker or a Prowlarr/Jackett proxy in
  // front of one; an uncredentialled one is a public index.
  func privacyFor(kind sourceKind, secret map[string][]byte) string {
      if kind != sourceGeneric {
          return "" // M6 reads it off the definition
      }
      if len(secret["apikey"]) > 0 || len(secret["passkey"]) > 0 || len(secret["cookie"]) > 0 {
          return PrivacyPrivate
      }
      return PrivacyPublic
  }
  ```

  ```go
  func TestProtocolForIsEmptyForADefinitionBackedIndexer(t *testing.T) {
      def := "1337x"
      require.Equal(t, commonv1alpha1.ProtocolUsenet,
          protocolFor(sourceGeneric, indexv1alpha1.IndexerSpec{Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet}}))
      require.Equal(t, commonv1alpha1.Protocol(""),
          protocolFor(sourceDefinition, indexv1alpha1.IndexerSpec{Definition: &def}),
          "status.protocol is enum:[torrent,usenet]; the caller must OMIT it, not send an empty string")
  }

  func TestPrivacyFor(t *testing.T) {
      require.Equal(t, PrivacyPrivate, privacyFor(sourceGeneric, map[string][]byte{"apikey": []byte("k")}))
      require.Equal(t, PrivacyPublic, privacyFor(sourceGeneric, nil))
      require.Equal(t, "", privacyFor(sourceDefinition, map[string][]byte{"apikey": []byte("k")}))
  }
  ```

- [ ] 19. Add `sessionSecretName` and `readSecret` to `source.go`, with tests.
  **The session-Secret boundary, stated plainly:** this reconciler *resolves and
  publishes the reference* — it does not create the Secret, because nothing
  produces a session until the Cardigann login lands in M6
  (`phase-d1-libraries-api.md` §3.12: the login → owned Secret →
  `clustarr-indexer-sessions` KV flow "does not exist"). The name is
  deterministic and is therefore **sent on every apply regardless of whether the
  Secret exists**: a reference that is sometimes sent and sometimes omitted is
  the "a boolean stopped being sent once it was true" trap in another costume.

  ```go
  // sessionSecretSuffix names the Secret that holds an indexer's login session
  // (Cardigann cookies and JWTs, mirrored from the clustarr-indexer-sessions KV
  // bucket). The reconciler publishes the name in status.sessionSecretRef; M6's
  // login flow creates and fills it.
  const sessionSecretSuffix = "-session"

  // maxObjectName is a DNS subdomain's limit (RFC 1123).
  const maxObjectName = 253

  func sessionSecretName(indexerName string) string {
      if len(indexerName)+len(sessionSecretSuffix) <= maxObjectName {
          return indexerName + sessionSecretSuffix
      }
      return indexerName[:maxObjectName-len(sessionSecretSuffix)] + sessionSecretSuffix
  }

  // readSecret reads spec.secretRef. The recognised keys are apikey, username,
  // password, cookie, passkey and rss_key (indexer_types.go:116). A missing
  // Secret is a dependency error, not a terminal one: the operator may well be
  // creating it in the next kubectl apply.
  func readSecret(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
      if ref == nil {
          return nil, nil
      }
      var s corev1.Secret
      if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
          if apierrors.IsNotFound(err) {
              return nil, fmt.Errorf("indexer: secret %s/%s not found", ns, ref.Name)
          }
          return nil, err
      }
      return s.Data, nil
  }
  ```

  ```go
  func TestSessionSecretNameIsDeterministicAndFitsADNSSubdomain(t *testing.T) {
      require.Equal(t, "nzbgeek-session", sessionSecretName("nzbgeek"))
      long := strings.Repeat("a", 253)
      got := sessionSecretName(long)
      require.LessOrEqual(t, len(got), maxObjectName)
      require.True(t, strings.HasSuffix(got, sessionSecretSuffix))
      require.Equal(t, got, sessionSecretName(long), "the name must not drift between reconciles")
  }
  ```

- [ ] 20. Add `rpsFor` and `buildClient` to `source.go`, with the per-host
  limiter. **One limiter per indexer host, owned here.** `pkg/torznab` never
  defaults one on (`CLAUDE.md`: "the caller owns rate limiting"), and the
  limiter instance is created once in D1-8's `run.go` and handed to this
  reconciler *and* to D1-5 and D1-7, so all three pace against the same buckets.
  This reconciler is the only one that *configures* a key, because it is the
  only one that reads `spec.requestDelay`.

  > **Ruling R9 — written against the reshaped signature.** D1-1 reshapes
  > `torznab.WithRateLimit` to take the injected limiter instead of building a
  > private `*rate.Limiter`. These steps assume
  > `func WithRateLimit(l *ratelimit.Limiter, key string) ClientOption`. If D1-1
  > lands a narrow interface instead (e.g.
  > `type RateLimiter interface { Wait(context.Context, string) error }` with
  > `WithRateLimit(l RateLimiter, key string)`), `*ratelimit.Limiter` satisfies
  > it and nothing below changes. If D1-1 lands a *different parameter order or
  > arity*, `buildClient` is the single call site to adjust — do not spread the
  > option across the package.

  ```go
  // rpsFor converts spec.requestDelay (default 2s) into a token-bucket rate.
  // ratelimit.Config treats RPS <= 0 as unlimited and Burst <= 0 as 1.
  func rpsFor(delay metav1.Duration) float64 {
      if delay.Duration <= 0 {
          return 0
      }
      return 1 / delay.Duration.Seconds()
  }

  // limiterKey is the indexer HOST, not the object name: two Indexers pointing
  // at one tracker must share one bucket, which is the whole reason the limiter
  // is injected rather than built per client.
  func limiterKey(u *url.URL) string { return u.Host }

  // buildClient assembles the Torznab client for one Indexer. Option order is
  // load-bearing: WithHTTPClient (which this does not use) would discard
  // WithTimeout if it came after it.
  func buildClient(spec indexv1alpha1.IndexerSpec, secret map[string][]byte, lim *ratelimit.Limiter) (*torznab.Client, *url.URL, error) {
      u, err := url.Parse(spec.BaseURL)
      if err != nil {
          return nil, nil, fmt.Errorf("indexer: parse spec.baseURL %q: %w", spec.BaseURL, err)
      }
      if u.Scheme == "" || u.Host == "" {
          return nil, nil, fmt.Errorf("indexer: spec.baseURL %q must be absolute", spec.BaseURL)
      }
      apiPath := "/api"
      if spec.Generic != nil && spec.Generic.APIPath != "" {
          apiPath = spec.Generic.APIPath
      }
      endpoint := u.JoinPath(apiPath)

      key := limiterKey(u)
      lim.SetConfig(key, ratelimit.Config{RPS: rpsFor(spec.RequestDelay), Burst: 1})

      c, err := torznab.NewClient(endpoint.String(), string(secret["apikey"]),
          torznab.WithTimeout(spec.Timeout.Duration),
          torznab.WithRateLimit(lim, key),
      )
      if err != nil {
          return nil, nil, err
      }
      return c, u, nil
  }
  ```

  Tests: `rpsFor(2s) == 0.5`, `rpsFor(0) == 0`, and a `buildClient` table for a
  relative baseURL (error), a baseURL with a trailing slash plus `apiPath:
  "/api"` (no doubled slash), and a default empty `apiPath` resolving to
  `/api`. Run and see them pass.

- [ ] 21. Add `classify` to `source.go` with its test. It is the one place
  `*torznab.Error` is decoded, so the conditions and the requeue delay cannot
  disagree about what a 429 means:

  ```go
  // probeOutcome is what one caps probe told us.
  type probeOutcome struct {
      Reason     string        // a condition reason; "" on success
      Message    string
      AuthFailed bool
      Limited    bool
      RetryAfter time.Duration // from the Retry-After header on a 429
  }

  // Condition reasons local to Indexer; see pkg/k8s.Reason* for the shared set.
  const (
      ReasonDefinitionNotImplemented = "DefinitionNotImplemented"
      ReasonCredentialsRejected      = "CredentialsRejected"
      ReasonIndexerDisabled          = "IndexerDisabled"
      ReasonResponseTooLarge         = "ResponseTooLarge"
      ReasonProbeFailed              = "ProbeFailed"
      ReasonBackingOff               = "BackingOff"
      ReasonLimitReached             = "LimitReached"
  )

  func classify(err error) probeOutcome {
      if err == nil {
          return probeOutcome{}
      }
      out := probeOutcome{Reason: ReasonProbeFailed, Message: err.Error()}
      if errors.Is(err, torznab.ErrResponseTooLarge) {
          out.Reason = ReasonResponseTooLarge
          return out
      }
      var te *torznab.Error
      if !errors.As(err, &te) {
          return out
      }
      switch {
      case te.Code == torznab.ErrIncorrectCredentials ||
          te.Code == torznab.ErrAccountSuspended ||
          te.Code == torznab.ErrInsufficientPrivileges ||
          te.HTTPStatus == http.StatusUnauthorized || te.HTTPStatus == http.StatusForbidden:
          out.Reason, out.AuthFailed = ReasonCredentialsRejected, true
      case te.Code == torznab.ErrRequestLimitReached ||
          te.Code == torznab.ErrDownloadLimitReached ||
          te.HTTPStatus == http.StatusTooManyRequests:
          out.Reason, out.Limited, out.RetryAfter = ReasonLimitReached, true, te.RetryAfter
      case te.HTTPStatus == http.StatusGone: // Prowlarr's "indexer disabled"
          out.Reason = ReasonIndexerDisabled
      }
      return out
  }
  ```

  Test it as a table over: nil, a bare error, `fmt.Errorf("%w: ...",
  torznab.ErrResponseTooLarge)`, `&torznab.Error{Code: 100}`,
  `&torznab.Error{HTTPStatus: 429, RetryAfter: 90 * time.Second}`,
  `&torznab.Error{Code: 501}`, `&torznab.Error{HTTPStatus: 410}`.

- [ ] 22. Commit: `... commit -m "feat(indexarr): resolve an Indexer's source,
  protocol, privacy and per-host limiter" -- indexarr/controller/indexer/source.go
  indexarr/controller/indexer/source_test.go`

**The reconciler**

- [ ] 23. Create `controller.go` (GPL header) with the `Reconciler` type, the
  caps memo and `NewReconciler`. The caps TTL memo lives in memory because
  `IndexerStatus` has no `capsFetchedAt` field and inventing one is an API
  change with one consumer — a process restart simply re-probes once, which is
  harmless and arguably desirable:

  ```go
  const (
      // reprobeInterval is the steady-state tick. It is short because the
      // RateLimited and Healthy conditions are derived from worker-owned
      // status fields that k8s.GenerationChanged() deliberately filters out of
      // the watch, so this tick is the only thing that refreshes them.
      reprobeInterval = 15 * time.Minute

      // capsTTL is how stale status.caps may get before the tick spends an
      // actual HTTP request on t=caps. An indexer's capability set changes
      // roughly never; its rate limit is a real budget.
      capsTTL = 12 * time.Hour
  )

  // Reconciler reconciles an Indexer: it validates the spec, probes caps and
  // derives the conditions. It is the ONLY writer of status.conditions,
  // .protocol, .privacy, .caps, .observedGeneration and .sessionSecretRef, and
  // it writes NONE of the escalation or counter fields -- see this package's
  // doc comment.
  type Reconciler struct {
      Client   client.Client
      Recorder events.EventRecorder

      // Limiters paces every outbound indexer request in this process, keyed
      // by indexer host. D1-8 constructs exactly one and hands the same
      // instance to this reconciler, to the search fan-out and to the RSS
      // worker, so all three share one bucket per host. Remove() is
      // deliberately never called: the key is a host, not an object, and the
      // reconciler cannot know when the last Indexer using a host went away.
      // The map is bounded by the number of distinct indexer hosts.
      Limiters *ratelimit.Limiter

      mu       sync.Mutex
      capsSeen map[types.UID]capsMemo
  }

  type capsMemo struct {
      at         time.Time
      generation int64
  }

  func NewReconciler(c client.Client, recorder events.EventRecorder, limiters *ratelimit.Limiter) *Reconciler {
      return &Reconciler{Client: c, Recorder: recorder, Limiters: limiters, capsSeen: map[types.UID]capsMemo{}}
  }

  func (r *Reconciler) shouldProbe(uid types.UID, generation int64, hasCaps bool, now time.Time) bool {
      r.mu.Lock()
      defer r.mu.Unlock()
      m, ok := r.capsSeen[uid]
      return !ok || !hasCaps || m.generation != generation || now.Sub(m.at) >= capsTTL
  }

  func (r *Reconciler) markProbed(uid types.UID, generation int64, now time.Time) {
      r.mu.Lock()
      defer r.mu.Unlock()
      r.capsSeen[uid] = capsMemo{at: now, generation: generation}
  }

  func (r *Reconciler) forget(uid types.UID) {
      r.mu.Lock()
      defer r.mu.Unlock()
      delete(r.capsSeen, uid)
  }
  ```

- [ ] 24. Add `ownedStatus`, `ownedFrom` and `patch` to `controller.go`. **This
  is the SSA defence**; write the comment, it is the reason the code looks the
  way it does:

  ```go
  // ownedStatus is the COMPLETE set of IndexerStatus fields
  // k8s.ManagerIndexarr owns. Server-side apply replaces a manager's ownership
  // set on every apply rather than merging it, so anything this struct does not
  // carry into patch() is RELEASED -- which reads on the object as "reset to
  // zero". Adding a field to this reconciler's ownership means adding it here,
  // not adding a second apply.
  type ownedStatus struct {
      // Protocol is omitted from the apply when empty: status.protocol is
      // enum:[torrent,usenet] and the apiserver rejects an explicit "".
      Protocol         commonv1alpha1.Protocol
      Privacy          string
      Caps             *indexv1alpha1.Caps
      SessionSecretRef string
  }

  // ownedFrom seeds the owned set from what is already on the object. Every
  // early return in Reconcile therefore RE-SENDS the caps, protocol and privacy
  // that are already there instead of releasing them. Phase C's most expensive
  // SSA defect was an early-return path that built a partial status and wiped
  // the happy path's work -- and because the early return is always a transient
  // failure (a full queue, a missing RootFolder, here a timed-out indexer), a
  // healthy object got gutted by a blip.
  func ownedFrom(idx *indexv1alpha1.Indexer) ownedStatus {
      return ownedStatus{
          Protocol:         idx.Status.Protocol,
          Privacy:          idx.Status.Privacy,
          Caps:             idx.Status.Caps,
          SessionSecretRef: idx.Status.SessionSecretRef,
      }
  }

  // patch is the ONLY k8s.PatchStatus call site in this package. Every return
  // path in Reconcile goes through it. Do not add a second one.
  func (r *Reconciler) patch(ctx context.Context, idx *indexv1alpha1.Indexer,
      conditions []metav1.Condition, owned ownedStatus, result ctrl.Result) (ctrl.Result, error) {

      st := indexac.IndexerStatus().
          WithObservedGeneration(idx.Generation).
          WithConditions(k8s.ConditionACs(conditions)...).
          WithPrivacy(owned.Privacy).
          WithSessionSecretRef(owned.SessionSecretRef)
      if owned.Protocol != "" {
          st = st.WithProtocol(owned.Protocol)
      }
      if owned.Caps != nil {
          st = st.WithCaps(capsAC(*owned.Caps))
      }
      ac := indexac.Indexer(idx.Name, idx.Namespace).WithStatus(st)
      if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerIndexarr, ac); err != nil {
          logging.FromContext(ctx).Error("patch status", "error", err)
          return ctrl.Result{}, err
      }
      return result, nil
  }
  ```

- [ ] 25. Add the condition derivation helpers to `controller.go`. `RateLimited`
  reads two worker-owned counters against `spec.limits`; note that it does *not*
  clear `Ready`, deliberately:

  ```go
  // rateLimited derives the RateLimited condition from spec.limits and the
  // worker-owned counters status.queriesInWindow / status.grabsInWindow. Note
  // Prowlarr counts IndexerQuery + IndexerRss together against QueryLimit; both
  // land in queriesInWindow, which the RSS worker and the search fan-out
  // increment.
  func rateLimited(spec indexv1alpha1.IndexerSpec, st indexv1alpha1.IndexerStatus) (bool, string) {
      if spec.Limits == nil {
          return false, ""
      }
      unit := spec.Limits.Unit
      if unit == "" {
          unit = indexv1alpha1.LimitUnitDay
      }
      if l := spec.Limits.QueryLimit; l != nil && st.QueriesInWindow >= *l {
          return true, fmt.Sprintf("queries %d/%d per %s", st.QueriesInWindow, *l, unit)
      }
      if l := spec.Limits.GrabLimit; l != nil && st.GrabsInWindow >= *l {
          return true, fmt.Sprintf("grabs %d/%d per %s", st.GrabsInWindow, *l, unit)
      }
      return false, ""
  }
  ```

- [ ] 26. Write `Reconcile`'s spine in `controller.go`. Every branch returns
  through `r.patch`:

  ```go
  func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
      ctx, span := tracing.Start(ctx, "indexer.Reconcile")
      defer span.End()
      log := logging.FromContext(ctx).With("indexer", req.NamespacedName)
      now := time.Now()

      var idx indexv1alpha1.Indexer
      if err := r.Client.Get(ctx, req.NamespacedName, &idx); err != nil {
          if apierrors.IsNotFound(err) {
              // The object is already gone, so neither its UID nor its host is
              // recoverable and nothing can be pruned here. The
              // DeletionTimestamp branch below is where pruning actually
              // happens; this path leaves at most one stale map key.
              return ctrl.Result{}, nil
          }
          return ctrl.Result{}, err
      }
      if !idx.DeletionTimestamp.IsZero() {
          r.forget(idx.UID)
          r.Limiters.Remove(limiterKeyFor(idx.Spec.BaseURL))
          return ctrl.Result{}, nil
      }

      conditions := append([]metav1.Condition(nil), idx.Status.Conditions...)
      owned := ownedFrom(&idx)
      owned.SessionSecretRef = sessionSecretName(idx.Name)

      kind, err := resolveSource(idx.Spec)
      if err != nil {
          k8s.MarkReady(&idx, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
          if _, perr := r.patch(ctx, &idx, conditions, owned, ctrl.Result{}); perr != nil {
              return ctrl.Result{}, perr
          }
          return ctrl.Result{}, reconcile.TerminalError(err)
      }
      // The remaining branches are added one per step below, in this order,
      // each with its own failing test first:
      //   step 27  disabled, and definition-backed (deferred to M6)
      //   step 28  caps probe, through the per-host limiter
      //   step 29  protocol and privacy resolution
      //   step 30  the success apply -- one complete owned-set declaration
      //   step 31  the early-return regression test, driven from real
      //            steady state, proving a failed probe releases nothing
  }
  ```

  Note the deletion branch: `r.Limiters.Remove` is called only here, where the
  spec is still readable. Add `limiterKeyFor(baseURL string) string` to
  `source.go` (parse-and-Host, empty on error) so this path cannot panic on a
  malformed URL.

- [ ] 27. Add the disabled and definition-deferred branches. Both return with
  **no requeue** — a spec edit re-triggers them through
  `k8s.GenerationChanged()`:

  ```go
      if idx.Spec.Enabled != nil && !*idx.Spec.Enabled {
          k8s.MarkReady(&idx, &conditions, false, k8s.ReasonDisabled, "spec.enabled is false")
          return r.patch(ctx, &idx, conditions, owned, ctrl.Result{})
      }

      if kind != sourceGeneric {
          // The Cardigann engine, IndexerDefinition ingestion and the login
          // flow are M6 (§16). Unknown, not False: nothing is wrong with this
          // Indexer, there is simply no code to drive it yet.
          k8s.SetCondition(&idx, &conditions, k8s.NewCondition(
              indexv1alpha1.IndexerConditionReady, metav1.ConditionUnknown,
              ReasonDefinitionNotImplemented,
              "Cardigann definitions are M6; only spec.generic is reconciled in M2"))
          return r.patch(ctx, &idx, conditions, owned, ctrl.Result{})
      }
  ```

- [ ] 28. Add the credential read, the client build and the TTL-gated caps
  probe. The probe result is folded into `owned.Caps` **only on success**, so a
  failure leaves the previously probed caps in place:

  ```go
      secret, err := readSecret(ctx, r.Client, idx.Namespace, idx.Spec.SecretRef)
      if err != nil {
          k8s.MarkUnknown(&idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, k8s.ReasonDependencyNotReady, "%s", err.Error())
          k8s.MarkReady(&idx, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
          return r.patch(ctx, &idx, conditions, owned, ctrl.Result{RequeueAfter: reprobeInterval})
      }
      owned.Protocol = protocolFor(kind, idx.Spec)
      owned.Privacy = privacyFor(kind, secret)

      client, base, err := buildClient(idx.Spec, secret, r.Limiters)
      if err != nil {
          k8s.MarkReady(&idx, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
          if _, perr := r.patch(ctx, &idx, conditions, owned, ctrl.Result{}); perr != nil {
              return ctrl.Result{}, perr
          }
          return ctrl.Result{}, reconcile.TerminalError(err)
      }

      outcome := probeOutcome{}
      probed := false
      if r.shouldProbe(idx.UID, idx.Generation, idx.Status.Caps != nil, now) {
          probed = true
          start := time.Now()
          wire, perr := client.Caps(ctx)
          // The `function` label is the Torznab t= value, and t=caps is not a
          // SearchMode -- hence the literal. `indexer` is the OBJECT NAME:
          // never a title, a host or an indexer-supplied string.
          metrics.IndexerQueryDuration.WithLabelValues(idx.Name, "caps").Observe(time.Since(start).Seconds())
          outcome = classify(perr)
          if perr == nil {
              projected := projectCaps(wire)
              owned.Caps = &projected
              r.markProbed(idx.UID, idx.Generation, now)
              metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, "ok").Inc()
          } else {
              metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, outcomeLabel(outcome)).Inc()
              log.Warn("caps probe failed", "host", base.Host, "reason", outcome.Reason, "error", perr)
          }
      }
  ```

  Add `outcomeLabel(probeOutcome) string` returning a small bounded set
  (`ok`, `error`, `rate_limited`, `unauthorized`, `banned`) — the metric's
  `outcome` label must never carry an indexer-supplied string.
  **Note the `function` label is the Torznab `t=` value, and `t=caps` is not a
  `SearchMode`; use the literal `"caps"` rather than `string(torznab.ModeSearch)`
  above — fix that line before moving on.**

- [ ] 29. Add the condition derivation and the single terminal `r.patch`:

  ```go
      switch {
      case !probed || outcome.Reason == "":
          k8s.MarkTrue(&idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, k8s.ReasonReconciled, "credentials accepted")
      case outcome.AuthFailed:
          k8s.MarkFalse(&idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, ReasonCredentialsRejected, "%s", outcome.Message)
          if r.Recorder != nil {
              r.Recorder.Eventf(&idx, nil, "Warning", ReasonCredentialsRejected, "Reconcile", outcome.Message)
          }
      default:
          // Always set, never left absent: a condition this manager sometimes
          // sends and sometimes omits is released on the apply that omits it.
          k8s.MarkUnknown(&idx, &conditions, indexv1alpha1.IndexerConditionAuthenticated, outcome.Reason, "%s", outcome.Message)
      }

      limited, limitMsg := rateLimited(idx.Spec, idx.Status)
      if outcome.Limited {
          limited, limitMsg = true, outcome.Message
      }
      if limited {
          k8s.MarkTrue(&idx, &conditions, indexv1alpha1.IndexerConditionRateLimited, ReasonLimitReached, "%s", limitMsg)
      } else {
          k8s.MarkFalse(&idx, &conditions, indexv1alpha1.IndexerConditionRateLimited, k8s.ReasonReconciled, "under the configured limits")
      }

      healthy := Healthy(idx.Status, now) && outcome.Reason == ""
      switch {
      case !Healthy(idx.Status, now):
          k8s.MarkFalse(&idx, &conditions, indexv1alpha1.IndexerConditionHealthy, ReasonBackingOff,
              "backing off until %s (escalation level %d)", idx.Status.DisabledUntil.Time.UTC().Format(time.RFC3339), idx.Status.EscalationLevel)
      case outcome.Reason != "":
          k8s.MarkFalse(&idx, &conditions, indexv1alpha1.IndexerConditionHealthy, outcome.Reason, "%s", outcome.Message)
      default:
          k8s.MarkTrue(&idx, &conditions, indexv1alpha1.IndexerConditionHealthy, k8s.ReasonReconciled, "last request succeeded")
      }

      // RateLimited deliberately does NOT clear Ready. A daily query budget is
      // an Indexer's expected steady state, and flapping Ready once a day is
      // noise an operator learns to ignore. (This differs from
      // MetadataProvider, where Throttled does clear Ready; that provider's
      // throttle is an exception, not a budget.)
      if healthy && owned.Caps != nil {
          k8s.MarkReady(&idx, &conditions, true, k8s.ReasonReconciled, "reachable, caps probed")
      } else if owned.Caps == nil {
          k8s.MarkReady(&idx, &conditions, false, ReasonProbeFailed, "caps have never been probed successfully")
      } else {
          k8s.MarkReady(&idx, &conditions, false, firstNonEmpty(outcome.Reason, ReasonBackingOff), "%s", firstNonEmpty(outcome.Message, "backing off"))
      }

      return r.patch(ctx, &idx, conditions, owned, ctrl.Result{RequeueAfter: r.requeueAfter(idx.Status, outcome, now)})
  ```

- [ ] 30. Add `requeueAfter` to `controller.go`. **`RequeueAfter` only — never a
  bare `Requeue: true`, never an error returned purely to force a retry:**

  ```go
  // requeueAfter picks the next tick. It never returns 0, because a zero
  // RequeueAfter means "do not requeue" and this reconciler's RateLimited and
  // Healthy conditions are derived from worker-owned fields the watch
  // predicate filters out -- only the tick refreshes them.
  func (r *Reconciler) requeueAfter(st indexv1alpha1.IndexerStatus, outcome probeOutcome, now time.Time) time.Duration {
      // A server's Retry-After is authoritative; pkg/ratelimit.Backoff
      // documents the same rule for the same reason.
      if outcome.RetryAfter > 0 {
          return outcome.RetryAfter
      }
      if st.DisabledUntil != nil && now.Before(st.DisabledUntil.Time) {
          return st.DisabledUntil.Time.Sub(now) + time.Second
      }
      return reprobeInterval
  }
  ```

- [ ] 31. Add the RBAC markers and `SetupWithManager` to the bottom of
  `controller.go`. **The marker block must be package level — separated from
  `func SetupWithManager` by a blank line.** controller-gen only collects
  `+kubebuilder:rbac` from a comment group that is *not* a declaration's doc
  comment; attach it and controller-gen exits 0 having collected nothing, and
  the reconciler ships with no permissions at all. In Phase C that silently hit
  four controllers (`rootfolder`, `qualityprofile`, `delayprofile`,
  `metadataprovider`) and envtest cannot see it, because envtest does not
  enforce RBAC. `cmd/clustarr`'s `TestRBACMarkersArePackageLevel` is the guard;
  `indexarr` is already in the Makefile's `RBAC_DIRS`.

  ```go
  // +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
  // +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers/status,verbs=get;update;patch
  // Secrets: spec.secretRef carries the apikey the caps probe authenticates
  // with, and status.sessionSecretRef names the login-session Secret M6's
  // Cardigann flow will read. Both are reads through the manager's cached
  // client, which lists and watches -- hence get;list;watch and not get alone.
  // No write verb is granted: nothing in M2 creates or fills a session Secret.
  // Carried item for M6 -- the login flow needs create;update;patch here.
  // +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
  // The Recorder is a k8s.io/client-go/tools/events.EventRecorder, so
  // events.k8s.io is the correct group. mgr.GetEventRecorder supplies it.
  // +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

  // SetupWithManager registers the Indexer controller.
  //
  // The marker block above is deliberately separated from this declaration by a
  // blank line; see cmd/clustarr's TestRBACMarkersArePackageLevel.
  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
      return ctrl.NewControllerManagedBy(mgr).
          Named("indexer").
          For(&indexv1alpha1.Indexer{}, builder.WithPredicates(k8s.GenerationChanged())).
          WithOptions(controller.Options{
              ReconciliationTimeout: 5 * time.Minute,
              RecoverPanic:          ptr.To(true),
          }).
          Complete(r)
  }
  ```

  Run `go build ./indexarr/...` and `go vet ./indexarr/...` and see both clean.

**envtest — including the two SSA regressions**

- [ ] 32. Create `controller_envtest_test.go` (package `indexer_test`) with the
  standard `newTestClient` helper, copied from
  `catalogarr/controller/rootfolder/controller_envtest_test.go:37`. The
  `KUBEBUILDER_ASSETS` skip is mandatory and so is noticing it:

  ```go
  func newTestClient(t *testing.T) client.Client {
      t.Helper()
      if os.Getenv("KUBEBUILDER_ASSETS") == "" {
          t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
      }
      env := &envtest.Environment{
          CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
          ErrorIfCRDPathMissing: true,
      }
      cfg, err := env.Start()
      require.NoError(t, err)
      t.Cleanup(func() { require.NoError(t, env.Stop()) })
      c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
      require.NoError(t, err)
      return c
  }
  ```

  Add a `capsServer(t *testing.T, body string, status int) *httptest.Server`
  helper serving a fixed `<caps>` document, and put the XML fixture in
  `indexarr/controller/indexer/testdata/caps.xml` — one `<search available="yes"
  supportedParams="q" searchEngine="raw"/>`, one `<tv-search available="yes"
  supportedParams="q,season,ep,tvdbid"/>`, one `<movie-search available="yes"
  supportedParams="q,imdbid"/>`, a `<limits max="100" default="50"/>` and a
  three-category tree. **No network in tests.**

- [ ] 33. Write the happy-path test and see it pass:

  ```go
  func TestReconcileProbesCapsAndBecomesReady(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      ns := mustNamespace(t, ctx, c, "idx-ready")
      srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)

      require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
          ObjectMeta: metav1.ObjectMeta{Name: "nzbgeek", Namespace: ns},
          Spec: indexv1alpha1.IndexerSpec{
              BaseURL: srv.URL,
              Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet, APIPath: "/api"},
          },
      }))

      r := indexer.NewReconciler(c, events.NewFakeRecorder(10), ratelimit.New(ratelimit.Config{}))
      res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "nzbgeek"}})
      require.NoError(t, err)
      require.Equal(t, 15*time.Minute, res.RequeueAfter)
      require.False(t, res.Requeue, "RequeueAfter only")

      var got indexv1alpha1.Indexer
      require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nzbgeek"}, &got))
      require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionReady))
      require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionHealthy))
      require.Equal(t, commonv1alpha1.ProtocolUsenet, got.Status.Protocol)
      require.Equal(t, indexer.PrivacyPublic, got.Status.Privacy)
      require.Equal(t, "nzbgeek-session", got.Status.SessionSecretRef)
      require.Equal(t, got.Generation, got.Status.ObservedGeneration)
      require.NotNil(t, got.Status.Caps)
      require.True(t, indexer.SupportsMode(*got.Status.Caps, "tvsearch"))
      require.False(t, indexer.SupportsMode(*got.Status.Caps, "tv-search"), "R5: the CRD stores the t= vocabulary")
      require.True(t, got.Status.Caps.SupportsRawSearch)
      for _, cond := range got.Status.Conditions {
          require.Equal(t, got.Generation, cond.ObservedGeneration, "%s carries no observedGeneration", cond.Type)
      }
  }
  ```

- [ ] 34. **Write the SSA release regression.** This is the test the codebase has
  earned eight times over, and it only works because it drives the object to a
  real steady state *first*: a test that creates a blank Indexer, triggers the
  failure and asserts cannot observe a release, because there was nothing to
  release.

  ```go
  // TestTransientProbeFailureDoesNotReleaseCaps drives the Indexer to its real
  // steady state, then breaks the upstream. Server-side apply replaces a field
  // manager's ownership set per apply: an early-return path that builds a
  // partial status RELEASES everything the happy path set, and the early return
  // is always a transient failure -- so a healthy object gets gutted by a blip.
  func TestTransientProbeFailureDoesNotReleaseCaps(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      ns := mustNamespace(t, ctx, c, "idx-ssa")

      var fail atomic.Bool
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
          if fail.Load() {
              http.Error(w, "upstream on fire", http.StatusBadGateway)
              return
          }
          w.Header().Set("Content-Type", "application/xml")
          _, _ = io.WriteString(w, readFixture(t, "testdata/caps.xml"))
      }))
      t.Cleanup(srv.Close)

      name := types.NamespacedName{Namespace: ns, Name: "flaky"}
      require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
          ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
          Spec: indexv1alpha1.IndexerSpec{
              BaseURL: srv.URL,
              Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
          },
      }))

      r := indexer.NewReconciler(c, events.NewFakeRecorder(10), ratelimit.New(ratelimit.Config{}))

      // 1. Steady state.
      _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: name})
      require.NoError(t, err)
      var steady indexv1alpha1.Indexer
      require.NoError(t, c.Get(ctx, name, &steady))
      require.NotNil(t, steady.Status.Caps)
      require.NotEmpty(t, steady.Status.Caps.Categories)
      wantCaps := steady.Status.Caps.DeepCopy()

      // 2. The OTHER manager's fields, applied exactly as D1-5/D1-7 will.
      until := metav1.NewTime(time.Now().Add(-time.Minute))
      _, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
          indexac.Indexer(name.Name, ns).WithStatus(indexac.IndexerStatus().
              WithEscalationLevel(3).
              WithDisabledUntil(until).
              WithInitialFailureAt(until).
              WithLastFailureAt(until).
              WithLastFailure("earlier trouble").
              WithQueriesInWindow(7).
              WithGrabsInWindow(2).
              WithLastRssNewCount(11).
              WithIndexedReleases(4242)))
      require.NoError(t, err)

      // 3. The blip. Force a re-probe, since the caps TTL would otherwise skip it.
      fail.Store(true)
      require.NoError(t, c.Get(ctx, name, &steady))
      steady.Spec.Priority = 30 // a generation bump invalidates the caps memo
      require.NoError(t, c.Update(ctx, &steady))
      _, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: name})
      require.NoError(t, err, "a transient upstream failure is not a reconcile error")

      // 4. Nothing was released.
      var after indexv1alpha1.Indexer
      require.NoError(t, c.Get(ctx, name, &after))
      require.False(t, k8s.IsConditionTrue(after.Status.Conditions, indexv1alpha1.IndexerConditionReady))

      require.NotNil(t, after.Status.Caps, "caps were RELEASED by the failure path")
      require.Equal(t, wantCaps, after.Status.Caps)
      require.Equal(t, commonv1alpha1.ProtocolTorrent, after.Status.Protocol)
      require.Equal(t, indexer.PrivacyPublic, after.Status.Privacy)
      require.Equal(t, "flaky-session", after.Status.SessionSecretRef)

      require.Equal(t, int32(3), after.Status.EscalationLevel, "the reconciler released indexarr-worker's fields")
      require.Equal(t, int32(7), after.Status.QueriesInWindow)
      require.Equal(t, int32(2), after.Status.GrabsInWindow)
      require.Equal(t, int64(4242), after.Status.IndexedReleases)
      require.Equal(t, "earlier trouble", after.Status.LastFailure)
  }
  ```

  Run it. If it fails on the `after.Status.Caps` assertion, the bug is a second
  `PatchStatus` call site or a path that does not go through `ownedFrom` — fix
  the reconciler, not the test.

- [ ] 35. Add the remaining envtest cases and see them pass:
  - `spec.enabled: false` → `Ready=False/Disabled`, `RequeueAfter == 0`, and
    `status.protocol` **still** carried forward from a prior steady state.
  - `spec.definition: "1337x"` → `Ready=Unknown/DefinitionNotImplemented`, and
    **no** `status.protocol` on the object (assert
    `require.Empty(t, got.Status.Protocol)`); the apply must not have been
    rejected — `require.NoError` on the Reconcile is what proves the enum trap
    is avoided.
  - a spec with neither `generic` nor `definition` created through a client that
    bypasses CEL is not reachable in envtest (the CRD rule fires), so assert the
    terminal path directly instead: `resolveSource` returns an error and
    `Reconcile` wraps it — use `errors.Is(err, reconcile.TerminalError(nil))` on
    a unit-level call with a fake client, or assert
    `reconcile.TerminalError` via `apierrors`-free `errors.As`. Keep this one
    small; the CEL rule is the real guard.
  - `spec.secretRef` naming a missing Secret → `Ready=False/DependencyNotReady`,
    `RequeueAfter == 15m`, `Authenticated=Unknown`.
  - a `spec.secretRef` Secret carrying `apikey` → `status.privacy == "private"`.
  - a server answering `429` with `Retry-After: 90` →
    `RateLimited=True`, `RequeueAfter == 90s`, `Ready=False`, caps preserved.
  - a server answering the Newznab XML `<error code="100"/>` at HTTP 200 →
    `Authenticated=False/CredentialsRejected` and a Warning Event on the fake
    recorder.
  - `status.disabledUntil` in the future (applied as `indexarr-worker`) →
    `Healthy=False/BackingOff` and `RequeueAfter ≈ disabledUntil - now`.

- [ ] 36. Add the caps-TTL test: reconcile twice against a server that counts
  requests, assert the second reconcile issued **no** HTTP request (the memo is
  warm and the generation is unchanged), then bump `spec.priority` and assert the
  third reconcile did. This is what keeps a 15-minute tick from spending an
  indexer's query budget.

- [ ] 37. Commit: `... commit -m "feat(indexarr): reconcile Indexer caps, health
  and conditions" -- indexarr/controller/indexer/controller.go
  indexarr/controller/indexer/controller_envtest_test.go
  indexarr/controller/indexer/testdata/`

**Manifests and verification**

- [ ] 38. Run `make manifests`. Confirm `config/rbac/role.yaml` gained
  `index.clustarr.io/indexers` with `update;patch`, `index.clustarr.io/indexers/status`
  and that `""/secrets` is still `get;list;watch`. If the diff is empty, the
  markers were **not** collected — re-check the blank line above
  `SetupWithManager` before doing anything else.

- [ ] 39. Sync the chart: copy the generated rules into
  `charts/clustarr/templates/rbac.yaml` between the BEGIN/END sentinels, then
  run `go test ./cmd/clustarr/ -run 'TestChartRBACMatchesTheGeneratedRole|TestRBACMarkersArePackageLevel|TestGeneratedRoleCoversEveryStatusWriter|TestEveryDirectoryWithRBACMarkersIsGenerated|TestEveryCRDKindAControllerTouchesHasAnRBACMarker'`
  and see all five pass.

- [ ] 40. **Run the envtest suite properly: `make test`.** `go test ./...` is not
  a substitute — `newTestClient` calls `t.Skip` when `KUBEBUILDER_ASSETS` is
  unset, and `pkg/crdcheck` skips the same way, so a green `go test` proves
  nothing about the CEL rules or the `status.protocol` enum. Check the reported
  time for `indexarr/controller/indexer`: envtest starts a real apiserver, so a
  passing run takes **seconds**. A result in **milliseconds means the suite
  skipped**, not that it passed. If it did, export the assets path
  (`KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path)`) and run again.

- [ ] 41. Run `make lint` and see it clean. Confirm specifically that forbidigo
  reports nothing: `.Status().Update()` and `.Status().Patch()` are banned
  outside `pkg/k8s`, and `grep -rn 'Status()\.\(Update\|Patch\)'
  indexarr/controller/indexer/` must return nothing.

- [ ] 42. Run `make generate && make manifests` once more and confirm
  `git status --porcelain` is empty. Generated code that drifts is a broken
  build for the next task.

- [ ] 43. Final commit of the generated files: `... commit -m "chore(indexarr):
  regenerate RBAC for the Indexer reconciler" -- config/rbac/role.yaml
  charts/clustarr/templates/rbac.yaml`

---

#### Boundaries this task deliberately does not cross

- **No escalation writes.** See Rule 2. `RecordFailure`/`RecordSuccess` are
  exported and tested here; D1-5 and D1-7 apply their results under
  `k8s.ManagerIndexarrWorker`, sending all six fields
  (`escalationLevel`, `disabledUntil`, `initialFailureAt` via
  `Escalation.InitialFailure`, `lastFailureAt`, `lastFailure`) plus the
  counters in **one** apply.
- **No `schema.IndexerEvent` publishing.** §8.2 requires
  `clustarr.evt.index.indexer.disabled|recovered|limited.<uid>` on an escalation
  transition. The transition happens where the escalation is applied, i.e. in
  D1-5/D1-7, not here — this task has no bus handle. Flagged below.
- **No RSS scheduling.** §6.2's "schedule RSS via `WithScheduleAt` on
  `work.indexarr.rss`" is D1-7's.
- **No session Secret creation, no Cardigann login, no proxy.** M6.
- **No registration in `run.go`.** D1-8 wires
  `indexer.NewReconciler(mgr.GetClient(), mgr.GetEventRecorderFor("indexarr"), limiters)`
  into `setupControllers` and constructs the one shared `*ratelimit.Limiter`.
---

### Task D1-7: the RSS worker and the release firehose

This is the **producer for a consumer that already ships**. `catalogarr/worker/rssmatcher`
was written in Phase C, is registered, is subscribed to `clustarr.rel.>`, and today
receives nothing because nothing publishes. Every shape below is pinned by that
handler's source, not by a preference. Read
`catalogarr/worker/rssmatcher/handler.go:155-200` before Step 1; two of its comments
are requirements written down verbatim, and both are load-bearing.

**Files:**
- Create: `indexarr/worker/rss/doc.go` (package doc: ownership, the two pinned requirements, the registration D1-8 performs)
- Create: `indexarr/worker/rss/project.go` (`ProjectRelease`, the flag mapping, the index-row projection)
- Create: `indexarr/worker/rss/project_test.go` (pure, table-driven, no bus, no cluster)
- Create: `indexarr/worker/rss/publish.go` (`PublishReleases`)
- Create: `indexarr/worker/rss/publish_test.go` (embedded NATS JetStream; publish → consume through the **shipped** matcher subscription)
- Create: `indexarr/worker/rss/worker.go` (`Worker`, `Deps`, `Searcher`, `Subscription`, `SetupWithManager`, `Handle`, the poll loop, the heartbeat, the reschedule)
- Create: `indexarr/worker/rss/status.go` (`WorkerStatus` — the complete `indexarr-worker` declaration)
- Create: `indexarr/worker/rss/worker_test.go` (fake `Searcher`, fake `events.Message` counting `InProgress`)
- Create: `indexarr/worker/rss/suite_envtest_test.go` (envtest bootstrap, copied from `catalogarr/worker/rssmatcher/suite_envtest_test.go`)
- Create: `indexarr/worker/rss/worker_envtest_test.go` (status applies against a real apiserver, steady-state first)
- Create: `indexarr/worker/rss/testdata/torznab-feed.xml`, `testdata/torznab-feed-nodate.xml`, `testdata/newznab-feed.xml`

**Path ownership:** `indexarr/worker/rss/**` and nothing else. This task does **not**
touch `indexarr/run.go` (D1-8 wires it), `pkg/events/`, `pkg/k8s/`, `api/`, `config/`
or `go.mod`. `ConsumerIndexRSS`'s AckWait and Heartbeat were already changed by
Task D1-0; do not change them again.

**Interfaces — Consumes:**

```go
// D1-0 (already committed when this task starts)
k8s.ManagerIndexarrWorker                 // "indexarr-worker" -- and it is in FieldManagers()
events.Default().Consumer(events.ConsumerIndexRSS)  // AckWait 60s, Heartbeat 30s, MaxDeliver 4, MaxAckPending 4

// D1-2
relindex.Store       // Upsert(ctx, []relindex.Release) (inserted int, err error)
relindex.Release     // Indexer, GUID, Title, TitleNorm, Group, Protocol, Categories,
                     // SizeBytes, PublishedAt *time.Time, FetchedAt, InfoJSON

// D1-3
indexer.RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) indexer.Escalation
indexer.RecordFailure(cur indexv1alpha1.IndexerStatus, now time.Time, reason string) indexer.Escalation
indexer.Healthy(st indexv1alpha1.IndexerStatus, now time.Time) bool

// Phase B, unchanged
torznab.Query, torznab.Release, torznab.ModeSearch
release.Parse, release.ClassifyKind, release.Normalize, (*ParsedRelease).ApplyTo
newznab.CategoryID.Parent()
```

**Interfaces — Produces (verbatim — D1-5 imports these; do not redeclare them there):**

```go
// ---- indexarr/worker/rss ----
package rss

// PublishReleases publishes each release to the firehose. It is exported so
// D1-9's e2e can drive it directly without a full reconcile.
func PublishReleases(ctx context.Context, bus events.Bus, ns, indexerName string, rels []schema.Release) (published int, err error)

// ProjectRelease turns one wire release into the firehose payload. It is
// built ONCE, here, and imported by D1-5's search fan-out. indexerName is the
// Indexer object's metadata.name (NOT a display name): the matcher looks up
// indexer priority by Info.IndexerRef, and the envelope key's second segment
// must match it.
func ProjectRelease(r torznab.Release, indexerName, protocol string) schema.Release
```

Three further symbols this task creates that neighbouring tasks need. They are **not**
in `02-interfaces.md` and the controller must add them there before D1-3/D1-5/D1-6 run
— see **Flagged for the controller** at the end of this task.

```go
// WorkerStatus builds the COMPLETE set of IndexerStatus fields the
// "indexarr-worker" field manager owns. Every writer under that manager
// (the RSS worker, D1-5's search fan-out, D1-6's download verb) must build
// its apply with this, because server-side apply releases whatever a manager
// omits.
func WorkerStatus(ns, name string, cur indexv1alpha1.IndexerStatus, p Patch) *indexac.IndexerApplyConfiguration

// ScheduleNext publishes the next RssTask for idx, held on the schedule
// subject until at. D1-3's reconciler seeds the first one with this same
// function so the two cannot build the subject or the msg-id two ways.
func ScheduleNext(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, at time.Time) error

// TaskMsgID is the deduplication id for one scheduled poll slot.
func TaskMsgID(uid string, generation int64, slot time.Time) string
```

---

- [ ] **Step 1: Create the package and write down the two pinned requirements**

Create `indexarr/worker/rss/doc.go` with the GPL-3.0 header from
`hack/boilerplate.go.txt` and a package doc that states, in the package where a future
reader will look, what the consumer requires. Quote the consumer, do not paraphrase it:

```go
// Package rss polls each Indexer's feed and publishes what it finds to the
// release firehose, clustarr.rel.<protocol>.<indexerName>.<newznabTop>.
//
// The consumer already ships. catalogarr/worker/rssmatcher has been
// subscribed to clustarr.rel.> since Phase C and has received nothing,
// because nothing published. Its handler pins two requirements:
//
//  1. The subject is events.ReleaseSubject(protocol, indexerName, newznabTop),
//     where newznabTop is the 1000-aligned parent category
//     (newznab.CategoryID.Parent()).
//
//  2. Envelope.Key MUST be "<namespace>/<indexerName>", literally, with a
//     slash. handler.go:167-174, verbatim:
//
//         ns, _, ok := strings.Cut(env.Key, "/")
//         if !ok || ns == "" {
//             return events.Discard("rssmatcher: envelope key is not <namespace>/<indexerName>", ...)
//         }
//
//     events.Discard goes straight to the DLQ and bypasses MaxDeliver. A key
//     with no slash is not retried, it is lost, silently, for every release
//     from that indexer. events.MediaKey's own doc says the same thing from
//     the other side: "A media key is NOT an Envelope.Key ... has no slash
//     left to cut on, so publishing one as the envelope key dead-letters the
//     task on first delivery. Build both, and keep them in separate
//     variables." In Phase C exactly that conflation dead-lettered every
//     metadata refresh in the system, and nothing alerted.
//
// indexerName is the Indexer object's metadata.name. The matcher resolves
// indexer priority by Info.IndexerRef (resolve.go:216), which is documented
// as "the name of the Indexer object", so the envelope key's second segment,
// the subject's second token and Info.IndexerRef are all the same string.
package rss
```

```bash
cd /home/appkins/src/mediactl/clustarr && go vet ./indexarr/...
```

- [ ] **Step 2: Failing test — the wire fields `ApplyTo` does not fill**

`(*ParsedRelease).ApplyTo` fills exactly six fields plus IDs (`pkg/release/convert.go:24`:
Quality, Revision, ReleaseGroup, Edition, Languages, ReleaseType). Everything else on
`commonv1.ReleaseInfo` is this package's job. Write the test first, in
`indexarr/worker/rss/project_test.go`:

```go
func TestProjectReleaseFillsTheIndexerSourcedFields(t *testing.T) {
	seeders, leechers := int32(42), int32(7)
	in := torznab.Release{
		Title:      "The Matrix 1999 1080p BluRay x264-GROUP",
		GUID:       "https://idx.example/details/9001",
		Link:       "https://idx.example/download/9001.torrent",
		CommentURL: "https://idx.example/details/9001",
		PubDate:    time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Size:       8_589_934_592,
		Categories: []newznab.CategoryID{2040, 2000},
		Seeders:    &seeders,
		Leechers:   &leechers,
		InfoHash:   "0123456789abcdef0123456789abcdef01234567",
		MagnetURL:  "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		IDs:        map[string]string{"imdb": "tt0133093"},
	}

	got := rss.ProjectRelease(in, "my-indexer", string(commonv1.ProtocolTorrent))

	require.Equal(t, "https://idx.example/details/9001", got.Info.GUID)
	require.Equal(t, "my-indexer", got.Info.IndexerRef)
	require.Equal(t, "The Matrix 1999 1080p BluRay x264-GROUP", got.Info.Title,
		"Info.Title is the RAW title as published, never a cleaned one")
	require.Equal(t, commonv1.ProtocolTorrent, got.Info.Protocol)
	require.Equal(t, int64(8_589_934_592), got.Info.SizeBytes)
	require.Equal(t, "https://idx.example/download/9001.torrent", got.Info.DownloadURL)
	require.Equal(t, "https://idx.example/details/9001", got.Info.InfoURL)
	require.Equal(t, []int32{2040, 2000}, got.Info.Categories)
	require.Equal(t, &seeders, got.Info.Seeders)
	require.Equal(t, &leechers, got.Info.Leechers)
	require.Equal(t, "tt0133093", got.Info.IDs["imdb"], "the tt prefix is canonical on the way in")
	require.Equal(t, "GROUP", got.Info.ReleaseGroup, "filled by ApplyTo, not by hand")
}
```

```bash
go test -count=1 -run TestProjectRelease ./indexarr/worker/rss/
```

Expected: **FAIL to build** — `rss.ProjectRelease` is undefined. That is the failure you
want; it proves the test is reaching the symbol under test.

- [ ] **Step 3: Implement the wire half of `ProjectRelease`**

In `indexarr/worker/rss/project.go`. Only the hand-filled fields for now; the parsed
half arrives in Step 7.

```go
// ProjectRelease turns one wire release into the firehose payload.
//
// pkg/release fills six fields plus ids through ApplyTo; every other field on
// ReleaseInfo is indexer-sourced and hand-filled here. FormatScore and
// MatchedFormats are deliberately NOT set: pkg/decision.Evaluate writes both
// on the consumer side before scoring (rssmatcher/handler.go:270-272), and a
// value invented here would be overwritten or, worse, believed.
func ProjectRelease(r torznab.Release, indexerName, protocol string) schema.Release {
	info := commonv1.ReleaseInfo{
		GUID:        r.GUID,
		IndexerRef:  indexerName,
		IndexerName: indexerName,
		Title:       r.Title,
		Protocol:    commonv1.Protocol(protocol),
		SizeBytes:   r.Size,
		DownloadURL: r.Link,
		MagnetURL:   r.MagnetURL,
		InfoHash:    r.InfoHash,
		InfoURL:     r.CommentURL,
		Seeders:     r.Seeders,
		Leechers:    r.Leechers,
		Categories:  categoryIDs(r.Categories),
		IDs:         maps.Clone(r.IDs),
	}
	return schema.Release{Info: info}
}

// categoryIDs widens newznab ids to the []int32 the CRD carries. A nil slice
// stays nil so the omitempty tag drops the field rather than encoding [].
func categoryIDs(cats []newznab.CategoryID) []int32 {
	if len(cats) == 0 {
		return nil
	}
	out := make([]int32, len(cats))
	for i, c := range cats {
		out[i] = int32(c)
	}
	return out
}
```

`IndexerName` is set to the object name too. The CRD has no display-name field; if one
is ever wanted, the **caller** overwrites `Info.IndexerName` after projection and never
`Info.IndexerRef`, which the matcher keys on.

```bash
go test -count=1 -run TestProjectRelease ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 4: Failing test — a missing `PublishedAt` passes through as nil**

This is a Phase-C scar with a 12-line doc comment on the field
(`api/common/v1alpha1/release_types.go:95-108`). `PublishedAt` is a `*metav1.Time`
precisely so absence is representable, and ranking uses publish age as the **usenet
tiebreaker**: substituting `now` makes a dateless release sort as brand new,
substituting the zero time makes it sort as ancient, and neither is true.

```go
func TestProjectReleasePublishedAtIsNeverBackfilled(t *testing.T) {
	pub := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	usenet := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   torznab.Release
		want *metav1.Time
	}{
		{"pubdate wins", torznab.Release{PubDate: pub}, ptr.To(metav1.NewTime(pub))},
		{"usenetdate when pubdate is absent", torznab.Release{UsenetDate: &usenet}, ptr.To(metav1.NewTime(usenet))},
		{"neither reported stays nil", torznab.Release{}, nil},
		{"an explicitly zero usenetdate stays nil", torznab.Release{UsenetDate: &time.Time{}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rss.ProjectRelease(tt.in, "idx", "usenet")
			require.Equal(t, tt.want, got.Info.PublishedAt)
		})
	}
}

func TestProjectReleaseNilPublishedAtSurvivesJSONRoundTrip(t *testing.T) {
	// A nil that becomes a zero metav1.Time somewhere in encode/decode is the
	// same corruption by another route, so assert on the far side of the wire.
	in := rss.ProjectRelease(torznab.Release{Title: "Some.Release.2026", GUID: "g"}, "idx", "usenet")
	require.Nil(t, in.Info.PublishedAt)

	name, data, err := schema.Encode(in)
	require.NoError(t, err)
	var out schema.Release
	require.NoError(t, schema.Decode(name, data, &out))
	require.Nil(t, out.Info.PublishedAt, "nil publishedAt must round-trip as nil, not as the zero time")
	require.NotContains(t, string(data), "publishedAt", "omitempty must drop the key entirely")
}
```

```bash
go test -count=1 -run TestProjectReleasePublishedAt ./indexarr/worker/rss/
```

Expected: FAIL — `PublishedAt` is nil in every case, so the first two subtests fail.

- [ ] **Step 5: Implement the date passthrough**

Add to `ProjectRelease`, before the `schema.Release` is returned:

```go
	// PublishedAt is a *metav1.Time on purpose: nil means the indexer
	// reported no date, which is genuinely different from a date. Ranking
	// uses publish age as the usenet tiebreaker, so backfilling now() makes
	// a dateless release sort as brand new and backfilling the zero time
	// makes it sort as ancient. Both are lies. Pass absence through.
	//
	// Both candidates are indexer-reported, so preferring one over the other
	// is a choice between two truths, not a backfill: Newznab-native feeds
	// carry <newznab:attr name="usenetdate"> and often a later <pubDate> for
	// when the row was indexed.
	switch {
	case !r.PubDate.IsZero():
		info.PublishedAt = ptr.To(metav1.NewTime(r.PubDate))
	case r.UsenetDate != nil && !r.UsenetDate.IsZero():
		info.PublishedAt = ptr.To(metav1.NewTime(*r.UsenetDate))
	}
```

```bash
go test -count=1 -run TestProjectRelease ./indexarr/worker/rss/
```

Expected: PASS, all four subtests plus the round trip.

- [ ] **Step 6: Failing test — `ParsedTitle` is the raw parser title, and `Kind` agrees with it**

Two traps in one test. First: `rssmatcher.TitleYearKey` (`index.go:103-109`) is
`release.CleanTitle(title) + "|" + itoa(year)` and its doc is explicit that the matcher
applies `CleanTitle` itself, so `ParsedTitle` must be the parser's raw
`ParsedRelease.Title`. Second: `rel.Kind` is the matcher's **first** dispatch
(`match.go:45`) and anything that is not movie or series matches nothing at all, so a
`Kind` that disagrees with the parse silently drops every match.

```go
func TestProjectReleaseParsedTitleIsRawAndKindAgrees(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{
		Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP",
		GUID:  "g",
	}, "idx", "torrent")

	parsed, err := release.Parse("The.Matrix.1999.1080p.BluRay.x264-GROUP", release.Options{})
	require.NoError(t, err)
	require.Equal(t, parsed.Title, got.ParsedTitle,
		"ParsedTitle is the parser's raw Title; the matcher applies CleanTitle itself")
	require.Equal(t, int32(1999), got.Year)
	require.Equal(t, commonv1.MediaKindMovie, got.Kind)

	// The matcher's own key must be derivable from what we sent.
	require.Equal(t,
		release.CleanTitle(parsed.Title)+"|1999",
		release.CleanTitle(got.ParsedTitle)+"|"+strconv.Itoa(int(got.Year)))
}

func TestProjectReleaseSeriesFields(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{
		Title: "Some.Show.S02E05.1080p.WEB-DL.x265-GRP", GUID: "g",
	}, "idx", "torrent")
	require.Equal(t, commonv1.MediaKindEpisode, got.Kind)
	require.Equal(t, []int32{2}, got.Seasons)
	require.Equal(t, []int32{5}, got.Episodes)
	require.False(t, got.FullSeason)
	require.False(t, got.MultiSeason)
}
```

```bash
go test -count=1 -run TestProjectRelease ./indexarr/worker/rss/
```

Expected: FAIL — `ParsedTitle`, `Year`, `Kind`, `Seasons`, `Episodes` are all zero.

- [ ] **Step 7: Implement the parsed half, classifying exactly once**

`ParsedRelease` does **not** carry the kind `Parse` dispatched on, and `Parse` classifies
the *id-stripped* title while `ClassifyKind` classifies whatever you hand it. Classify
once, and pass the result into `Parse` so the two cannot diverge:

```go
	// Classify once and pin it. Parse would classify internally, but it does
	// so on the id-stripped title and does not return the kind it chose, so
	// calling ClassifyKind separately afterwards can disagree with the parse
	// that actually ran. rel.Kind is the matcher's first dispatch
	// (rssmatcher/match.go:45) and a wrong kind matches nothing, silently --
	// so the classification that steers the parse and the one on the wire
	// are the same value by construction.
	kind := release.ClassifyKind(r.Title)
	parsed, err := release.Parse(r.Title, release.Options{Kind: kind})
	if err != nil {
		// An unparsable title is still a real release: it can match on
		// tmdb/tvdb ids, and dropping it here would hide it from the
		// matcher entirely. Publish the wire half with no parsed fields.
		return schema.Release{Info: info, FetchedAt: r.fetchedAt()}
	}
	parsed.ApplyTo(&info)

	out := schema.Release{
		Info: info,
		// The RAW parser title. TitleYearKey applies CleanTitle itself
		// (rssmatcher/index.go:96-109); pre-cleaning here is only harmless
		// because CleanTitle is idempotent, and relying on that is how the
		// next change breaks it.
		ParsedTitle: parsed.Title,
		Year:        int32(parsed.Year),
		Seasons:     widen(parsed.Seasons),
		Episodes:    widen(parsed.Episodes),
		Absolute:    widen(parsed.Absolute),
		AirDate:     parsed.AirDate,
		FullSeason:  parsed.FullSeason,
		MultiSeason: parsed.MultiSeason,
		Special:     parsed.Special,
		Kind:        kind,
		Hints:       hints(parsed.Hints),
	}
	return out
```

`widen([]int) []int32` and `hints(release.Hints) map[string][]string` are two small
unexported helpers in the same file; `hints` keys on `"codec"`, `"hdr"`, `"audio"`,
`"channels"`, `"streaming"`, `"container"` and omits empty entries. Add `FetchedAt` as a
parameter-free field set by the caller in Step 10 — for now leave it zero and let the
publisher stamp it.

```bash
go test -count=1 -run TestProjectRelease ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 8: Failing test — `IndexerFlags` is a closed enum the apiserver enforces**

`ReleaseInfo.IndexerFlags` carries
`+kubebuilder:validation:items:Enum=freeleech;halfleech;neutralleech;doubleupload;internal;exclusive;scene`.
A flag outside those seven is rejected when a `Download.spec.release` is later persisted
— i.e. at grab time, on the far side of the system, long after the release looked fine.

```go
func TestProjectReleaseIndexerFlagsStayInsideTheEnum(t *testing.T) {
	zero, half := 0.0, 0.5
	tests := []struct {
		name string
		in   torznab.Release
		want []string
	}{
		{"dvf 0 is freeleech", torznab.Release{DownloadVolumeFactor: &zero}, []string{"freeleech"}},
		{"dvf 0.5 is halfleech", torznab.Release{DownloadVolumeFactor: &half}, []string{"halfleech"}},
		{"dvf 1 is neither", torznab.Release{DownloadVolumeFactor: ptr.To(1.0)}, nil},
		{"tag attrs pass through when known", torznab.Release{
			Attrs: map[string][]string{"tag": {"internal", "scene"}}}, []string{"internal", "scene"}},
		{"unknown tags are dropped, not forwarded", torznab.Release{
			Attrs: map[string][]string{"tag": {"internal", "PersonalRelease", "trumpable"}}}, []string{"internal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rss.ProjectRelease(tt.in, "idx", "torrent")
			require.Equal(t, tt.want, got.Info.IndexerFlags)
		})
	}
}
```

```bash
go test -count=1 -run TestProjectReleaseIndexerFlags ./indexarr/worker/rss/
```

Expected: FAIL — `IndexerFlags` is nil everywhere.

- [ ] **Step 9: Implement the flag mapping as an allow-list**

An allow-list, never a passthrough. Indexer-supplied strings are untrusted input and this
field is a closed enum:

```go
// knownFlags is the enum ReleaseInfo.IndexerFlags declares. A tag outside it
// is dropped: the apiserver rejects the whole object otherwise, and it would
// do so at grab time, in grabarr, not here.
var knownFlags = map[string]string{
	"freeleech": commonv1.IndexerFlagFreeleech, "halfleech": commonv1.IndexerFlagHalfleech,
	"neutralleech": commonv1.IndexerFlagNeutralleech, "doubleupload": commonv1.IndexerFlagDoubleUpload,
	"internal": commonv1.IndexerFlagInternal, "exclusive": commonv1.IndexerFlagExclusive,
	"scene": commonv1.IndexerFlagScene,
}

func indexerFlags(r torznab.Release) []string {
	var out []string
	if f := r.DownloadVolumeFactor; f != nil {
		switch {
		case *f == 0:
			out = append(out, commonv1.IndexerFlagFreeleech)
		case *f > 0 && *f < 1:
			out = append(out, commonv1.IndexerFlagHalfleech)
		}
	}
	for _, tag := range r.Attrs["tag"] {
		if v, ok := knownFlags[strings.ToLower(strings.TrimSpace(tag))]; ok && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
```

Set `info.IndexerFlags = indexerFlags(r)` in `ProjectRelease`.

```bash
go test -count=1 -run TestProjectRelease ./indexarr/worker/rss/ && go test -count=1 ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 10: Failing test then fix — the scoring fields stay empty, and `FetchedAt` is set**

```go
func TestProjectReleaseLeavesScoringToTheConsumer(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{Title: "X.2026.1080p-G", GUID: "g"}, "idx", "torrent")
	require.Zero(t, got.Info.FormatScore, "pkg/decision.Evaluate writes this on the consumer side")
	require.Empty(t, got.Info.MatchedFormats)
}
```

`schema.Release.FetchedAt` has **no** `omitempty` — it is always encoded, so a zero value
ships as `"0001-01-01T00:00:00Z"` and is visibly wrong on the wire. Give `Worker` the job
of stamping it (Step 20) and assert here only that `ProjectRelease` leaves it zero, so
one place owns it. Add a one-line doc note on `ProjectRelease` saying so.

```bash
go test -count=1 ./indexarr/worker/rss/ && go vet ./indexarr/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(indexarr): project a torznab release onto the firehose payload" -- indexarr/worker/rss
```

- [ ] **Step 11: Failing test — the envelope key is cuttable and resolves to the right namespace**

This is the single most important test in the task. Assert it **the way the consumer
reads it**, with the same `strings.Cut`, not by string equality alone — a test that only
compares to `"ns/name"` passes for a key the consumer would still discard if the
convention ever moved.

In `indexarr/worker/rss/publish_test.go`:

```go
func TestPublishReleasesEnvelopeKeyIsNamespaceSlashIndexerName(t *testing.T) {
	bus := membus.New()
	t.Cleanup(func() { _ = bus.Close() })

	var got []*events.Envelope
	stop, err := bus.Subscribe(t.Context(), events.Subscription{
		Stream: events.StreamReleases, Durable: "test", Filters: []string{events.FilterAllReleases},
	}, func(_ context.Context, m events.Message) error {
		got = append(got, m.Envelope().Clone())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	rels := []schema.Release{rss.ProjectRelease(torznab.Release{
		Title: "The.Matrix.1999.1080p-G", GUID: "g1", Categories: []newznab.CategoryID{2040},
	}, "my-indexer", "torrent")}

	n, err := rss.PublishReleases(t.Context(), bus, "media", "my-indexer", rels)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Eventually(t, func() bool { return len(got) == 1 }, 5*time.Second, 10*time.Millisecond)

	// Exactly what catalogarr/worker/rssmatcher/handler.go:171 does.
	ns, indexer, ok := strings.Cut(got[0].Key, "/")
	require.True(t, ok, "key %q has no slash: the matcher Discards it STRAIGHT TO THE DLQ, bypassing MaxDeliver", got[0].Key)
	require.Equal(t, "media", ns)
	require.Equal(t, "my-indexer", indexer)
	require.Equal(t, got[0].Key, "media/my-indexer")

	// And the negative: a media key is not an envelope key. If someone ever
	// swaps one in, this is the assertion that catches it.
	require.NotEqual(t, events.MediaKey("movie", "media", "my-indexer"), got[0].Key)
	require.NotContains(t, events.MediaKey("movie", "media", "my-indexer"), "/")
}
```

```bash
go test -count=1 -run TestPublishReleases ./indexarr/worker/rss/
```

Expected: FAIL to build — `rss.PublishReleases` is undefined.

- [ ] **Step 12: Implement `PublishReleases`**

In `indexarr/worker/rss/publish.go`:

```go
// PublishReleases publishes each release to the firehose. It is exported so
// D1-9's e2e can drive it directly without a full reconcile.
//
// published counts the messages the broker STORED: a receipt with
// Duplicate set means the stream's 2h dedup window already held this
// indexer+guid, so nothing new reached the matcher. This is a bus-level
// figure and is NOT Indexer.status.lastRssNewCount -- see the Worker, which
// takes that count from relindex.Upsert instead.
func PublishReleases(ctx context.Context, bus events.Bus, ns, indexerName string, rels []schema.Release) (published int, err error) {
	if ns == "" || indexerName == "" {
		// Refuse rather than publish an uncuttable key. The matcher would
		// Discard every one of these to the DLQ without a retry, and
		// nothing downstream would report it.
		return 0, fmt.Errorf("rss: publish releases: namespace=%q indexer=%q: both are required to build the envelope key", ns, indexerName)
	}
	// Built explicitly, once, in its own variable. A media-key-style token is
	// NOT an envelope key: it has been through tok() and has no slash left to
	// cut on. Conflating the two dead-lettered every metadata refresh in
	// Phase C, silently, because the consumer discards on a failed split.
	key := ns + "/" + indexerName

	for _, rel := range rels {
		schemaName, data, encErr := schema.Encode(rel)
		if encErr != nil {
			return published, encErr
		}
		env := &events.Envelope{
			ID:     events.MsgIDForRelease(indexerName, rel.Info.GUID),
			Type:   "index.Release",
			Schema: schemaName,
			Source: "indexarr@" + version.String(),
			Key:    key,
			Time:   rel.FetchedAt,
			Data:   data,
		}
		// The matcher calls tracing.Extract before Start so its span
		// continues indexarr's poll. Without this Inject the RSS leg of
		// every trace is orphaned (rssmatcher/handler.go:157-159).
		tracing.Inject(ctx, env)

		rcpt, pubErr := bus.Publish(ctx, subjectFor(rel, indexerName), env, events.WithMsgID(env.ID))
		if pubErr != nil {
			return published, fmt.Errorf("rss: publish %s/%s: %w", indexerName, rel.Info.GUID, pubErr)
		}
		if !rcpt.Duplicate {
			published++
		}
	}
	return published, nil
}
```

```bash
go test -count=1 -run TestPublishReleasesEnvelopeKey ./indexarr/worker/rss/
```

Expected: FAIL — `subjectFor` is undefined. Next step.

- [ ] **Step 13: Failing test then implement — the subject's category token is the 1000-aligned parent**

```go
func TestSubjectUsesTheAlignedParentCategory(t *testing.T) {
	tests := []struct {
		name string
		cats []newznab.CategoryID
		want string
	}{
		{"2040 aligns to 2000", []newznab.CategoryID{2040}, events.ReleaseSubject("torrent", "idx", 2000)},
		{"first category wins", []newznab.CategoryID{5030, 2040}, events.ReleaseSubject("torrent", "idx", 5000)},
		{"a custom id is its own parent", []newznab.CategoryID{100042}, events.ReleaseSubject("torrent", "idx", 100042)},
		{"no category at all", nil, events.ReleaseSubject("torrent", "idx", 0)},
	}
	...
}
```

Implement:

```go
// subjectFor builds clustarr.rel.<protocol>.<indexerName>.<newznabTop>.
// newznabTop is the 1000-aligned PARENT category (newznab.CategoryID.Parent(),
// which returns a custom id >= 100000 unchanged), because the subject exists
// so a future consumer can filter on "movies" without knowing that 2040 is
// Movies/HD. A release with no category publishes under 0, which
// clustarr.rel.> still matches.
func subjectFor(rel schema.Release, indexerName string) string {
	var top newznab.CategoryID
	if len(rel.Info.Categories) > 0 {
		top = newznab.CategoryID(rel.Info.Categories[0]).Parent()
	}
	return events.ReleaseSubject(string(rel.Info.Protocol), indexerName, int(top))
}
```

```bash
go test -count=1 -run 'TestPublishReleases|TestSubject' ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 14: Failing test — dedup on `sha1(indexer:guid)`, against a real broker**

`membus` will not prove the dedup window; JetStream will. Copy `startServer`/`connect`
from `pkg/events/natsbus/natsbus_test.go:37-70` verbatim into `publish_test.go` — an
embedded `natsserver.NewServer` with `JetStream: true`, `Port: -1`, `StoreDir` under
`t.TempDir()`, `NoLog`, `NoSigs`, `srv.ReadyForConnections(20*time.Second)` and
`t.Cleanup(srv.Shutdown)`. **No network, no external broker, no cluster.**

```go
func TestPublishReleasesDedupsOnIndexerAndGUID(t *testing.T) {
	bus := newJetStreamBus(t) // embedded server + k8s.EnsureTopology(events.Default())
	rels := []schema.Release{rss.ProjectRelease(
		torznab.Release{Title: "A.2026.1080p-G", GUID: "same-guid", Categories: []newznab.CategoryID{2040}},
		"idx", "torrent")}

	first, err := rss.PublishReleases(t.Context(), bus, "media", "idx", rels)
	require.NoError(t, err)
	require.Equal(t, 1, first)

	// Re-reading the same RSS page is the normal case at a 15m interval on a
	// slow-moving feed. CLUSTARR_RELEASES has Duplicates: 2h, so the second
	// publish stores nothing and the matcher never sees it twice.
	second, err := rss.PublishReleases(t.Context(), bus, "media", "idx", rels)
	require.NoError(t, err)
	require.Equal(t, 0, second, "a duplicate is a stored-nothing, not an error")

	// The same guid from a DIFFERENT indexer is a different release.
	other, err := rss.PublishReleases(t.Context(), bus, "media", "idx2",
		[]schema.Release{rss.ProjectRelease(torznab.Release{Title: "A.2026.1080p-G", GUID: "same-guid",
			Categories: []newznab.CategoryID{2040}}, "idx2", "torrent")})
	require.NoError(t, err)
	require.Equal(t, 1, other)
}
```

```bash
go test -count=1 -run TestPublishReleasesDedups ./indexarr/worker/rss/
```

Expected: PASS if Step 12 is right; if `published` counts attempts rather than stored
messages, the second assertion fails. Fix the accounting, not the test.

- [ ] **Step 15: Failing test — publish → consume through the shipped matcher subscription**

The end of the contract: prove a published message is actually *deliverable* to the real
consumer definition, without a cluster.

```go
func TestPublishedReleaseReachesTheShippedMatcherSubscription(t *testing.T) {
	bus := newJetStreamBus(t)

	// Not a subscription written for this test: the one the shipped handler
	// asks for. If the tuning or the filter ever moves, this moves with it.
	sub := (&rssmatcher.Handler{}).Subscription()
	require.Equal(t, events.StreamReleases, sub.Stream)

	var seen []*events.Envelope
	var mu sync.Mutex
	stop, err := bus.Subscribe(t.Context(), sub, func(_ context.Context, m events.Message) error {
		mu.Lock(); defer mu.Unlock()
		seen = append(seen, m.Envelope().Clone())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	rel := rss.ProjectRelease(torznab.Release{
		Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP", GUID: "g1",
		Categories: []newznab.CategoryID{2040}, IDs: map[string]string{"tmdb": "603"},
	}, "my-indexer", "torrent")
	_, err = rss.PublishReleases(t.Context(), bus, "media", "my-indexer", []schema.Release{rel})
	require.NoError(t, err)

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 1 },
		10*time.Second, 20*time.Millisecond)

	env := seen[0]
	ns, _, ok := strings.Cut(env.Key, "/")
	require.True(t, ok); require.Equal(t, "media", ns)

	var out schema.Release
	require.NoError(t, schema.Decode(env.Schema, env.Data, &out))
	require.Equal(t, commonv1.MediaKindMovie, out.Kind, "match.go:45 dispatches on this first")
	require.Equal(t, "603", out.Info.IDs["tmdb"], "match.go:59 keys movies on this")
	require.Equal(t, "my-indexer", out.Info.IndexerRef, "resolve.go:216 looks up priority by this")
	require.NotEmpty(t, out.ParsedTitle)
	require.NotZero(t, out.Info.Title)
	require.NotEmpty(t, env.Trace, "tracing.Inject must run or the matcher's span starts a new trace")
}
```

The ten fields the matcher actually reads are `Kind`, `Info.IDs["tmdb"]`,
`Info.IDs["tvdb"]`, `ParsedTitle`+`Year`, `MultiSeason`+`Seasons`, `Episodes`,
`FullSeason`, `AirDate`, `Info` (whole) and `Info.IndexerRef`. Assert the ones this
fixture exercises; the episode/season ones get their own subtest with a series fixture.

```bash
go test -count=1 -race ./indexarr/worker/rss/
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(indexarr): publish parsed releases to the firehose with a cuttable envelope key" -- indexarr/worker/rss
```

- [ ] **Step 16: The worker skeleton and its subscription**

In `indexarr/worker/rss/worker.go`. The `Searcher` seam is a local interface so D1-5 and
D1-7 do not share a symbol neither owns — the concrete client comes from D1-3's
reconciler at wiring time (D1-8), already carrying that host's single
`ratelimit.Limiter`. **This package never constructs a limiter.**

```go
// Searcher is one indexer's search call. D1-3's reconciler supplies the
// concrete *torznab.Client, already built with this host's single injected
// rate limiter; this package never constructs one.
type Searcher interface {
	Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error)
}

type Deps struct {
	Client   client.Client
	Bus      events.Bus
	Index    relindex.Store
	SearcherFor func(ctx context.Context, idx *indexv1alpha1.Indexer) (Searcher, error)
	Clock    func() time.Time
}

type Worker struct{ Deps Deps }

// Subscription is the indexarr-rss durable consumer. It reads the tuning from
// the topology rather than restating it, exactly as
// rssmatcher.Handler.Subscription does, so AckWait and Heartbeat live in one
// place.
func (w *Worker) Subscription() events.Subscription {
	spec, ok := events.Default().Consumer(events.ConsumerIndexRSS)
	if !ok {
		return events.Subscription{} // fails Validate loudly at Subscribe time
	}
	return spec.Subscription()
}

func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, w.Subscription(), w.Handle)
		if err != nil { return fmt.Errorf("indexarr: subscribe rss: %w", err) }
		defer stop()
		<-ctx.Done()
		return nil
	}))
}
```

Test, which pins Ruling R7 from the consumer's side:

```go
func TestSubscriptionFitsThePodsGracePeriod(t *testing.T) {
	sub := (&rss.Worker{}).Subscription()
	require.Equal(t, events.StreamWorkIndexarr, sub.Stream)
	require.Equal(t, events.ConsumerIndexRSS, sub.Durable)
	require.LessOrEqual(t, sub.AckWait, 60*time.Second,
		"terminationGracePeriodSeconds is 60; work that can outlast AckWait heartbeats instead")
	require.Equal(t, 30*time.Second, sub.Heartbeat)
	require.NoError(t, sub.Validate())
}
```

```bash
go test -count=1 -run TestSubscription ./indexarr/worker/rss/
```

Expected: PASS once D1-0's topology change is in; FAIL with `AckWait 120s` if it is not,
which tells you D1-0 did not land.

- [ ] **Step 17: Failing test — `Handle` refuses a task it cannot act on**

```go
func TestHandleDiscardsUnusableTasks(t *testing.T) {
	tests := []struct{ name string; env *events.Envelope }{
		{"no envelope", nil},
		{"undecodable", &events.Envelope{Schema: "index.RssTask.v1", Data: []byte("{[")}},
		{"wrong schema", &events.Envelope{Schema: "index.SearchRequest.v1", Data: []byte("{}")}},
		{"no indexer name", mustEnv(t, schema.RssTask{IndexerRef: schema.Ref{Namespace: "media"}})},
		{"no namespace", mustEnv(t, schema.RssTask{IndexerRef: schema.Ref{Name: "idx"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&rss.Worker{}).Handle(t.Context(), &fakeMessage{env: tt.env})
			var d *events.DiscardError
			require.ErrorAs(t, err, &d, "an unusable task must be terminated, not retried four times")
		})
	}
}
```

"no namespace" is the one that matters: `schema.Ref.String()` returns the bare name when
`Namespace` is empty, so a task built from such a Ref would produce an envelope key with
no slash and dead-letter every release it publishes. Refuse it at the door.

```bash
go test -count=1 -run TestHandleDiscards ./indexarr/worker/rss/
```

Expected: FAIL to build — `Handle` is undefined.

- [ ] **Step 18: Implement `Handle`'s decode-and-guard prologue**

```go
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	ctx, span := tracing.Start(ctx, "rss.Worker.Handle")
	defer span.End()

	env := m.Envelope()
	if env == nil {
		return events.Discard("rss task has no envelope", errors.New("rss: nil envelope"))
	}
	var task schema.RssTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable rss task", err)
	}
	if task.IndexerRef.Name == "" || task.IndexerRef.Namespace == "" {
		// Both halves are required: the envelope key this poll will publish
		// under is namespace + "/" + name, and the matcher Discards a key
		// it cannot Cut. Refuse here, where it is one message, rather than
		// there, where it is every release from this indexer.
		return events.Discard("rss task is missing a namespaced indexer reference",
			fmt.Errorf("rss: indexerRef=%q", task.IndexerRef.String()))
	}
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With(
		"indexer", task.IndexerRef.Name, "namespace", task.IndexerRef.Namespace))
	...
}
```

```bash
go test -count=1 -run TestHandleDiscards ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 19: Failing test — the poll heartbeats rather than outliving AckWait**

Ruling R7: `AckWait` is 60s because `terminationGracePeriodSeconds` is 60, and an RSS poll
of a slow indexer can plausibly exceed it. `fakeMessage.InProgress` counts beats
(`importarr/worker/rescan/helpers_envtest_test.go:211` has the pattern —
`m.heartbeats.Add(1)`).

```go
func TestPollHeartbeatsWhileItPages(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC))
	msg := &fakeMessage{env: mustEnv(t, schema.RssTask{IndexerRef: schema.Ref{Namespace: "media", Name: "idx"}})}

	// Four pages, each "taking" 25 seconds of fake time.
	searcher := &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
		clock.Advance(25 * time.Second)
		return pageOf(100), nil
	}}
	w := newTestWorker(t, clock, searcher) // 4 pages then empty
	_ = w.Handle(t.Context(), msg)

	require.GreaterOrEqual(t, msg.heartbeats.Load(), int32(4),
		"a 100s poll with a 60s AckWait must extend its deadline or the broker redelivers it and two workers poll one indexer")
}

func TestPollAtSigtermRetriesWithoutEscalatingTheIndexer(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	searcher := &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
		cancel()
		return nil, ctx.Err()
	}}
	w := newTestWorker(t, clock, searcher)
	err := w.Handle(ctx, msg)

	var r *events.RetryError
	require.ErrorAs(t, err, &r, "a cancelled poll is unfinished work, not a failed indexer")
	require.Zero(t, appliedEscalationCount(t), "a rollout must not escalate every indexer at once")
}
```

```bash
go test -count=1 -run 'TestPoll' ./indexarr/worker/rss/
```

Expected: FAIL — no poll loop yet.

- [ ] **Step 20: Implement the poll loop, the heartbeat, and the SIGTERM path**

The heartbeat goes **inside the paging loop, at the top of each iteration**, not around
the whole call: one `Search` is one HTTP request bounded by `spec.timeout` (default 30s),
so the only way a poll outlives AckWait is by making several of them.

```go
const (
	// heartbeatInterval is how often the poll extends its ack deadline.
	// ConsumerIndexRSS's AckWait is 60s, pinned to the pod's
	// terminationGracePeriodSeconds by Ruling R7; topology.go's own rule is
	// that work which can outlast the grace period heartbeats rather than
	// raising AckWait past it. 20s leaves two missed beats of headroom.
	// This is NOT Subscription.Heartbeat, which is the broker's idle
	// heartbeat for connection liveness and extends nothing.
	heartbeatInterval = 20 * time.Second

	// maxPages bounds one poll. An indexer that keeps returning full pages
	// would otherwise hold the delivery open indefinitely; the next poll
	// picks up where this one stopped, and RSS only needs the newest rows.
	maxPages = 4

	// pageSize is per Torznab request. Prowlarr's RSS default.
	pageSize = 100
)

func (w *Worker) poll(ctx context.Context, m events.Message, s Searcher, idx *indexv1alpha1.Indexer, task schema.RssTask) ([]torznab.Release, error) {
	var (
		all  []torznab.Release
		last time.Time
	)
	for page := 0; page < maxPages; page++ {
		if ctx.Err() != nil {
			return all, ctx.Err()
		}
		if now := w.now(); last.IsZero() || now.Sub(last) >= heartbeatInterval {
			last = now
			if err := m.InProgress(ctx); err != nil {
				return all, fmt.Errorf("rss: heartbeat: %w", err)
			}
		}
		// t=search with an empty q is the RSS call: the indexer's newest
		// rows, unfiltered (design.md:621).
		q := torznab.Query{
			Type:       torznab.ModeSearch,
			Categories: categoryIDsFor(idx, task),
			Limit:      pageSize,
			Offset:     page * pageSize,
		}
		batch, err := s.Search(ctx, q)
		if err != nil {
			return all, err
		}
		all = append(all, batch...)
		if len(batch) < pageSize || reachedSince(batch, task.Since) {
			break // RssTask.Since exists so the worker can stop paging early.
		}
	}
	return all, nil
}
```

At SIGTERM the manager cancels the context, `bus.Subscribe`'s `stop` drains in-flight
handlers, the in-flight HTTP request returns `ctx.Err()`, and `poll` returns it. `Handle`
then does:

```go
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// The pod is going away mid-poll. This is our shutdown, not the
		// indexer's fault: recording a failure here would escalate every
		// indexer in the namespace on every rollout, and Prowlarr's ladder
		// would then disable them for minutes. Nak and let the next pod
		// take it; nothing has been indexed or published, so redelivery is
		// a clean retry.
		return events.Retry(retryAfterShutdown, err)
	}
```

`retryAfterShutdown` is 30s. Note the ordering guarantee this relies on: the worker
**indexes and publishes only after the poll has completed**, so a cancelled poll leaves
no partial state to reconcile.

```bash
go test -count=1 -run TestPoll ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 21: Failing test — `lastRssNewCount` is what the index accepted, not what the feed returned**

`lastRssNewCount` is documented as "how many new releases the last RSS poll returned",
and the spec's RSS clause is "insert **new rows** (`UNIQUE(indexer, guid)`)". New means
*new to the index*. There are three different counts in flight and they routinely differ:

| count | source | means |
|---|---|---|
| `len(fetched)` | `torznab.Search` | how many rows the feed had — on a quiet feed this is the same 100 rows every 15 minutes |
| `inserted` | `relindex.Upsert` | **how many were new to the index** ← this one |
| `published` | `PublishReleases` | how many the bus stored, suppressed by a **2h** dedup window against a **72h** index |

```go
func TestLastRssNewCountComesFromTheIndexNotTheFeed(t *testing.T) {
	// 5 rows on the wire; the store reports 1 was new.
	store := &fakeStore{inserted: 1}
	w := newTestWorker(t, clock, &fakeSearcher{releases: pageOf(5)}, withStore(store))
	require.NoError(t, w.Handle(t.Context(), msg))

	st := getIndexerStatus(t)
	require.Equal(t, int32(1), st.LastRssNewCount, "new means new to the index, not fetched")
	require.Equal(t, int64(1), st.IndexedReleases, "running total advances by the insert count")
	require.Len(t, store.upserted, 5, "every fetched row is offered to the index; the store decides")
}
```

```bash
go test -count=1 -run TestLastRssNewCount ./indexarr/worker/rss/
```

Expected: FAIL.

- [ ] **Step 22: Implement index-then-publish, and document the two windows**

```go
	rows := make([]relindex.Release, 0, len(fetched))
	projected := make([]schema.Release, 0, len(fetched))
	for _, r := range fetched {
		rel := ProjectRelease(r, idx.Name, string(protocol))
		rel.FetchedAt = now
		projected = append(projected, rel)
		rows = append(rows, indexRow(rel, idx.Name, now))
	}

	// Upsert first, and take lastRssNewCount from its truthful `inserted`.
	// Every fetched row is offered; UNIQUE(indexer, guid) decides what is
	// new. Counting len(fetched) instead would report 100 new releases every
	// 15 minutes on a feed that has not moved.
	inserted, err := w.Deps.Index.Upsert(ctx, rows)
	if err != nil { ... }

	// Then publish ALL of them, not just the new ones. Upsert returns a
	// count, not a set (ADR-0003 fixes Store at four methods), and the
	// firehose is idempotent by design: Nats-Msg-Id is
	// sha1(indexer:guid) and CLUSTARR_RELEASES dedups for 2h, so
	// re-reading the same RSS page republishes nothing.
	//
	// The two windows are deliberately different and the asymmetry is
	// correct: the index keeps 72h, the dedup window is 2h. A release last
	// seen three hours ago is still in the index (inserted == 0) but its
	// dedup entry has expired, so it is republished and the matcher
	// re-evaluates it once. That costs one evaluation and can only help --
	// the item's monitored state or quality profile may have changed since.
	// What it must never do is inflate lastRssNewCount, which is why that
	// number comes from `inserted` and not from `published`.
	published, err := PublishReleases(ctx, w.Deps.Bus, idx.Namespace, idx.Name, projected)
```

`indexRow` fills `relindex.Release`: `Indexer`, `GUID`, `Title` (raw), `TitleNorm` =
`release.Normalize(rel.Info.Title)`, `Group` = `rel.Info.ReleaseGroup`, `Protocol`,
`Categories`, `SizeBytes`, `PublishedAt` (the same nil-preserving `*time.Time`),
`FetchedAt`, and `InfoJSON` = `json.Marshal(rel)` so a replay needs no re-query.

```bash
go test -count=1 -run TestLastRssNewCount ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 23: Implement `WorkerStatus` — the complete `indexarr-worker` declaration**

In `indexarr/worker/rss/status.go`. Read this before writing it: server-side apply
**replaces** a field manager's ownership set on every apply. Anything the manager sent
before and omits now is *released*, which reads as "reset to zero" on the object. Three
writers share `indexarr-worker` — this RSS worker, D1-5's search fan-out and D1-6's
download verb — so each of them must declare **all nine** of that manager's fields on
**every** apply, including the ones it did not change.

```go
// Patch is the subset of the indexarr-worker fields one writer changed.
// Everything it leaves nil is carried forward from cur.
type Patch struct {
	LastRssAt       *metav1.Time
	LastRssNewCount *int32
	IndexedReleases *int64
	QueriesInWindow *int32
	GrabsInWindow   *int32
	Escalation      *indexer.Escalation
}

// WorkerStatus builds the COMPLETE set of IndexerStatus fields the
// "indexarr-worker" field manager owns:
//
//	lastRssAt, lastRssNewCount, indexedReleases,
//	queriesInWindow, grabsInWindow,
//	escalationLevel, disabledUntil, initialFailureAt, lastFailureAt, lastFailure
//
// It is a COMPLETE declaration on purpose. Server-side apply replaces the
// manager's ownership set rather than merging it, so a field this manager
// owned and now omits is released and reads as zero. CLAUDE.md names eight
// distinct forms this took in Phase C. The worst one is the early return: a
// transient failure -- a slow indexer, a full queue -- builds a partial
// status, returns, and silently guts a healthy object. There is therefore
// exactly ONE constructor for this manager's apply, and every caller goes
// through it, including on the failure path.
//
// The other two writers under this manager (D1-5's search fan-out, D1-6's
// download verb) MUST build their applies with this function too. If one of
// them hand-rolls an apply that omits lastRssAt, the next search zeroes the
// RSS timestamps.
func WorkerStatus(ns, name string, cur indexv1alpha1.IndexerStatus, p Patch) *indexac.IndexerApplyConfiguration {
	st := indexac.IndexerStatus().
		WithLastRssAt(derefOr(p.LastRssAt, cur.LastRssAt)).
		WithLastRssNewCount(derefOr(p.LastRssNewCount, cur.LastRssNewCount)).
		WithIndexedReleases(derefOr(p.IndexedReleases, cur.IndexedReleases)).
		WithQueriesInWindow(derefOr(p.QueriesInWindow, cur.QueriesInWindow)).
		WithGrabsInWindow(derefOr(p.GrabsInWindow, cur.GrabsInWindow))
	// Escalation is sent EVERY time, not only when it changes. A field that
	// stops being sent once it reaches a value flips back to zero; keep
	// sending it. RecordSuccess returns Changed=false for an
	// already-healthy indexer, and that is a signal to skip the whole
	// apply -- never a signal to send a shorter one.
	esc := currentEscalation(cur)
	if p.Escalation != nil { esc = *p.Escalation }
	st = applyEscalation(st, esc)

	return indexac.Indexer(name, ns).WithStatus(st)
}
```

Note the gap this exposes: `indexer.Escalation` (D1-3) carries `FailureLevel`,
`DisabledUntil`, `LastFailureAt` and `LastFailureMsg`, but `IndexerStatus` also has
`InitialFailureAt`, which no `Escalation` field feeds. `applyEscalation` therefore carries
`cur.InitialFailureAt` forward and sets it from `LastFailureAt` on the transition from
level 0 to level 1. Flagged below.

- [ ] **Step 24: Failing envtest — the apply against an object already in steady state**

This project has hit the release hazard **eight times**, and every time the test that
missed it acted on a blank object. A blank object has nothing to release, so it cannot
observe a release. Drive the `Indexer` to a real steady state **first**.

Create `suite_envtest_test.go` by copying `catalogarr/worker/rssmatcher/suite_envtest_test.go`
— `TestMain` starting envtest, `requireEnvtest(t)` skipping on an unset
`KUBEBUILDER_ASSETS`, `newTestManager`, `newNamespace`.

```go
func TestStatusApplyDoesNotReleaseWhatItDidNotChange(t *testing.T) {
	requireEnvtest(t)
	ctx, c, ns := setup(t)

	idx := newIndexer(t, ctx, c, ns, "idx")

	// STEADY STATE FIRST. Two writers, both real:
	//   - the reconciler's manager, "indexarr"
	//   - this worker's manager, "indexarr-worker", with all nine fields set
	// A test that skips this and applies to a blank object proves nothing.
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerIndexarr,
		indexac.Indexer("idx", ns).WithStatus(indexac.IndexerStatus().
			WithObservedGeneration(1).WithProtocol(commonv1.ProtocolTorrent).
			WithPrivacy("private").WithSessionSecretRef("idx-session")))
	require.NoError(t, err)

	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
		rss.WorkerStatus(ns, "idx", indexv1alpha1.IndexerStatus{}, rss.Patch{
			LastRssAt:       ptr.To(metav1.NewTime(t0)),
			LastRssNewCount: ptr.To(int32(7)),
			IndexedReleases: ptr.To(int64(4242)),
			QueriesInWindow: ptr.To(int32(11)),
			GrabsInWindow:   ptr.To(int32(3)),
		}))
	require.NoError(t, err)

	// Now the path under test: a poll that finds nothing new. It changes
	// lastRssAt and lastRssNewCount and touches nothing else.
	before := getStatus(t, ctx, c, ns, "idx")
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
		rss.WorkerStatus(ns, "idx", before, rss.Patch{
			LastRssAt: ptr.To(metav1.NewTime(t1)), LastRssNewCount: ptr.To(int32(0)),
		}))
	require.NoError(t, err)

	after := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(11), after.QueriesInWindow, "released by an apply that omitted it")
	require.Equal(t, int32(3), after.GrabsInWindow)
	require.Equal(t, int64(4242), after.IndexedReleases)
	// And the OTHER manager's fields must be untouched: this is the whole
	// point of Ruling R6's split.
	require.Equal(t, int64(1), after.ObservedGeneration)
	require.Equal(t, commonv1.ProtocolTorrent, after.Protocol)
	require.Equal(t, "idx-session", after.SessionSecretRef)
	require.Equal(t, "private", after.Privacy)
}
```

```bash
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
go test -count=1 -v -run TestStatusApplyDoesNotRelease ./indexarr/worker/rss/
```

**Check the elapsed time.** A suite that finishes in milliseconds **skipped**;
`KUBEBUILDER_ASSETS` was not exported and nothing was compiled against a real apiserver.
An envtest run here takes seconds. `go test ./...` passing proves nothing about this test.

- [ ] **Step 25: Failing envtest — the failure path declares the complete set too**

The dangerous one. A full queue, a 429, an indexer timeout: all transient, all routine,
and an early return that applies only an escalation wipes `lastRssAt`, `lastRssNewCount`
and `indexedReleases` off a healthy object.

```go
func TestFailedPollKeepsTheRssFieldsItDidNotChange(t *testing.T) {
	requireEnvtest(t)
	ctx, c, ns := setup(t)
	idx := newIndexer(t, ctx, c, ns, "idx")
	driveToSteadyState(t, ctx, c, ns, "idx") // lastRssAt=t0, newCount=7, indexed=4242, queries=11

	w := newWorker(t, c, &fakeSearcher{err: errors.New("indexer returned 503")})
	err := w.Handle(ctx, rssTaskMessage(t, ns, "idx"))
	require.Error(t, err, "a failed poll is retried")

	st := getStatus(t, ctx, c, ns, "idx")
	require.Equal(t, int32(1), st.EscalationLevel, "the failure was recorded")
	require.Equal(t, "indexer returned 503", st.LastFailure)
	require.NotNil(t, st.InitialFailureAt)
	// The three fields the failure path did not touch must survive it.
	require.Equal(t, int64(4242), st.IndexedReleases)
	require.Equal(t, int32(7), st.LastRssNewCount)
	require.Equal(t, t0, st.LastRssAt.Time.UTC(), "a 503 must not erase when we last polled successfully")
	require.Equal(t, int32(11), st.QueriesInWindow)
}
```

```bash
go test -count=1 -run TestFailedPollKeeps ./indexarr/worker/rss/
```

Expected: FAIL until the failure path routes through `WorkerStatus` like every other path.

- [ ] **Step 26: Implement the single status write, on both paths**

There is exactly one `PatchStatus` call in the worker and every path reaches it. Structure
`Handle` so the apply is unconditional:

```go
	fetched, pollErr := w.poll(ctx, m, s, idx, task)

	patch := Patch{}
	switch {
	case pollErr != nil && (errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded)):
		// Shutdown. Record nothing, release nothing, retry.
		return events.Retry(retryAfterShutdown, pollErr)
	case pollErr != nil:
		esc := indexer.RecordFailure(idx.Status, now, pollErr.Error())
		patch.Escalation = &esc
	default:
		inserted, published := ... // Step 22
		esc := indexer.RecordSuccess(idx.Status, now)
		patch = Patch{
			LastRssAt:       ptr.To(metav1.NewTime(now)),
			LastRssNewCount: ptr.To(int32(inserted)),
			IndexedReleases: ptr.To(idx.Status.IndexedReleases + int64(inserted)),
			Escalation:      &esc,
		}
	}

	// ONE apply, whatever happened. Never an early return with a partial
	// status: the early return is usually the transient case, which is
	// exactly when a healthy object would be gutted by a blip.
	if _, err := k8s.PatchStatus(ctx, w.Deps.Client, k8s.ManagerIndexarrWorker,
		WorkerStatus(idx.Namespace, idx.Name, idx.Status, patch)); err != nil {
		return events.Retry(statusRetry, err)
	}
	// Reschedule BEFORE returning the poll error, so a failing indexer keeps
	// its cadence and recovers on its own (Step 28).
	if err := ScheduleNext(ctx, w.Deps.Bus, idx, now.Add(interval(idx))); err != nil { ... }
	if pollErr != nil {
		return events.Retry(indexer.BackoffFor(idx.Status), pollErr)
	}
	return nil
```

`idx.Status.IndexedReleases + inserted` is a read-modify-write. `relindex.Store` is fixed
at four methods and `Stats` has no per-indexer breakdown, so a running total is the only
source. Document the bound: `MaxAckPending` is 4, so two concurrent polls of the *same*
indexer are possible only when a duplicate schedule fires, and the lost update is at most
one poll's `inserted` and self-corrects at the next poll. Do not add a CAS loop for it.

```bash
go test -count=1 -run 'TestFailedPollKeeps|TestStatusApply|TestLastRss' ./indexarr/worker/rss/
```

Expected: PASS.

- [ ] **Step 27: Failing test — one broken indexer does not stop the others**

Isolation is structural here — one message is one indexer, and the handler must never fan
out across indexers — but it has to be *asserted*, because the cheap implementation
(list every Indexer and loop) looks reasonable and destroys it.

```go
func TestOneFailingIndexerDoesNotStopTheOthers(t *testing.T) {
	requireEnvtest(t)
	ctx, c, ns := setup(t)
	newIndexer(t, ctx, c, ns, "broken"); newIndexer(t, ctx, c, ns, "healthy")

	w := newWorker(t, c, searcherFunc(func(idx string) (Searcher, error) {
		if idx == "broken" { return &fakeSearcher{err: errors.New("503")}, nil }
		return &fakeSearcher{releases: pageOf(3)}, nil
	}))

	require.Error(t, w.Handle(ctx, rssTaskMessage(t, ns, "broken")))
	require.NoError(t, w.Handle(ctx, rssTaskMessage(t, ns, "healthy")),
		"one indexer's failure must not reach another's delivery")

	require.Equal(t, int32(1), getStatus(t, ctx, c, ns, "broken").EscalationLevel)
	require.Zero(t, getStatus(t, ctx, c, ns, "healthy").EscalationLevel)
	require.Equal(t, int32(3), getStatus(t, ctx, c, ns, "healthy").LastRssNewCount)
}

func TestHandleNeverListsIndexers(t *testing.T) {
	// A Get of exactly one object, by the name in the task. If this ever
	// becomes a List, the failure of one indexer can abort the loop and
	// starve every indexer after it in the slice.
	rec := &countingClient{}
	_ = newWorker(t, rec, ...).Handle(ctx, rssTaskMessage(t, ns, "idx"))
	require.Equal(t, 1, rec.gets); require.Zero(t, rec.lists)
}
```

Also add the two gates that keep a poll from happening at all, each with its own subtest:
`spec.enableRss` false (default true) and `spec.enabled` false → acknowledge without
polling and without a status write; `indexer.Healthy(status, now)` false → the indexer is
inside its backoff window, so acknowledge, reschedule for `disabledUntil`, and do not
query.

```bash
go test -count=1 -run 'TestOneFailing|TestHandleNeverLists' ./indexarr/worker/rss/
```

- [ ] **Step 28: Failing test then implement — `ScheduleNext` and the slot msg-id**

The controller seeds the first schedule; the worker schedules the next one at the end of
**every** poll, success or failure. If only the controller scheduled, polling would stop
until the next reconcile.

The msg-id is the trap. `events.MsgIDForObject(uid, generation, "rss")` is **constant**
across polls for an unchanged generation, and `CLUSTARR_WORK_INDEXARR` has
`Duplicates: 1h`, so at the default 15-minute interval the *second* poll's schedule would
be swallowed as a duplicate of the first and the indexer would stop polling. Quantise the
id to the slot:

```go
// TaskMsgID is the deduplication id for one scheduled poll slot. It carries
// the slot, not just the object, on purpose: CLUSTARR_WORK_INDEXARR dedups
// for 1h, so a constant per-object id would make every schedule after the
// first within that hour a no-op and the indexer would silently stop
// polling. Two schedulers converging on the same slot -- the reconciler
// seeding one while a poll schedules the next -- collapse to one delivery,
// which is exactly what we want.
func TaskMsgID(uid string, generation int64, slot time.Time) string {
	return events.MsgIDForObject(uid, generation, "rss:"+slot.UTC().Truncate(time.Second).Format(time.RFC3339))
}

func ScheduleNext(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, at time.Time) error {
	name, data, err := schema.Encode(schema.RssTask{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
		Categories: idx.Spec.Categories,
		Since:      newestSeen(idx),
	})
	if err != nil { return err }
	id := TaskMsgID(string(idx.UID), idx.Generation, at)
	env := &events.Envelope{
		ID: id, Type: "index.RssTask", Schema: name,
		Source: "indexarr@" + version.String(),
		// The work-task envelope key is the same <namespace>/<name> shape
		// as the firehose's, for the same reason: whoever handles it cuts
		// it to recover the namespace.
		Key: idx.Namespace + "/" + idx.Name, Time: at, Data: data,
	}
	tracing.Inject(ctx, env)
	// Publish to the TARGET subject; the bus rewrites it onto
	// clustarr.work.indexarr.sched.rss.normal.<uid> and asks the broker to
	// republish it to the target when the schedule fires
	// (natsbus.go:210-218). Publishing to the holding subject directly
	// would make the message re-trigger itself.
	_, err = bus.Publish(ctx, events.WorkRSSSubject(string(idx.UID)), env,
		events.WithMsgID(id), events.WithScheduleAt(at))
	return err
}
```

Even if a duplicate slot *does* slip through — the interval exceeds the 1h dedup window,
or the generation changed between the two schedulers — the duplicate poll is harmless at
the release layer: every release carries `Nats-Msg-Id = sha1(indexer:guid)` and
`CLUSTARR_RELEASES` dedups for **2h**, so the second poll of the same feed republishes
nothing and the matcher sees each release once. `relindex.Upsert` is likewise idempotent
on `UNIQUE(indexer, guid)`, so the second poll reports `inserted == 0`.

Test:

```go
func TestScheduleNextDedupsPerSlotAndAdvancesBetweenSlots(t *testing.T) {
	bus := newJetStreamBus(t)
	idx := indexerWithUID("u1", 3)

	require.NoError(t, rss.ScheduleNext(t.Context(), bus, idx, t0.Add(15*time.Minute)))
	require.NoError(t, rss.ScheduleNext(t.Context(), bus, idx, t0.Add(15*time.Minute)))
	require.Equal(t, uint64(1), storedOn(t, bus, events.WorkRSSSubject("u1")),
		"the reconciler and the worker racing on one slot must collapse to one poll")

	require.NoError(t, rss.ScheduleNext(t.Context(), bus, idx, t0.Add(30*time.Minute)))
	require.Equal(t, uint64(2), storedOn(t, bus, events.WorkRSSSubject("u1")),
		"a constant per-object msg-id would swallow this and polling would stop dead")

	require.NotEqual(t,
		rss.TaskMsgID("u1", 3, t0.Add(15*time.Minute)),
		rss.TaskMsgID("u1", 3, t0.Add(30*time.Minute)))
}
```

```bash
go test -count=1 -run TestScheduleNext ./indexarr/worker/rss/
```

- [ ] **Step 29: Metrics, span attributes and RBAC**

Three shipped metrics, all with bounded labels (`indexer` is an object name, bounded by
the number of `Indexer` objects; **never** label by release title or feed URL):

```go
	metrics.IndexerQueryDuration.WithLabelValues(idx.Name, "rss").Observe(time.Since(start).Seconds())
	metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, outcome).Inc()   // ok | error | rate_limited | banned
	metrics.IndexerReleasesReturned.WithLabelValues(idx.Name).Observe(float64(len(fetched)))
```

Derive `rate_limited` and `banned` from `*torznab.Error` (`HTTPStatus == 429` and
`== 410`) with `errors.As`, so a 429 is visible as pacing rather than as a generic error.

Add the RBAC markers above `Worker`, and nothing more: the worker reads `Indexer` and
writes only its `/status` subresource.

```go
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers/status,verbs=get;update;patch
```

```bash
make manifests && git status --short   # config/rbac must show the two new rules and nothing else
```

- [ ] **Step 30: Run the full gate**

```bash
cd /home/appkins/src/mediactl/clustarr
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
go test -count=1 -race -v ./indexarr/worker/rss/ 2>&1 | tail -40
make lint
make generate && make manifests && git status --short   # clean
go build ./indexarr/...                                 # never a bare `go build`
```

Confirm in the `-v` output that **no envtest reported SKIP** and that the envtest tests
took seconds, not milliseconds. `pkg/crdcheck` and every envtest suite skip silently
without `KUBEBUILDER_ASSETS`, and a suite that finishes instantly did not run.

- [ ] **Step 31: Commit**

```bash
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(indexarr): poll indexer feeds and publish new releases to the firehose" -- indexarr/worker/rss
```

**Done when:** `ProjectRelease` fills every indexer-sourced field, leaves `FormatScore`
and `MatchedFormats` to the consumer, passes a missing `PublishedAt` through as nil, and
sends the raw parser title as `ParsedTitle`; `PublishReleases` builds
`"<namespace>/<indexerName>"` explicitly and a test cuts it the way the shipped matcher
does; a published release is proved deliverable to `rssmatcher.Handler.Subscription()`
against an embedded JetStream server with no cluster and no network; the poll heartbeats
at 20s inside a 60s AckWait and naks without escalating on shutdown; `lastRssNewCount`
comes from `relindex.Upsert`'s `inserted`; every path writes one complete
`indexarr-worker` declaration through `WorkerStatus`, proved against an object already in
steady state on both the success and failure paths; one indexer's failure leaves the
others untouched; the next poll is scheduled with a slot-quantised msg-id; `make lint`,
`make generate` and `make manifests` are clean.

---

#### Flagged for the controller — not decided here

1. **Three shared symbols are missing from `02-interfaces.md`.** `WorkerStatus`/`Patch`,
   `ScheduleNext` and `TaskMsgID` are all used by neighbouring tasks (D1-3 seeds the
   first schedule; D1-5 and D1-6 write under `indexarr-worker`). The contract file pins
   only `PublishReleases` and `ProjectRelease` for this package, and says a task needing
   an unlisted shape must ask rather than invent one. This task implements them in
   `indexarr/worker/rss` because it is the field manager's first writer — but if three
   writers sharing one manager is the real shape, `WorkerStatus` arguably belongs in
   D1-3's pure `indexarr/controller/indexer` package next to `Escalation`. **Rule before
   D1-3, D1-5 and D1-6 start.** If it stays here, add all three to `02-interfaces.md`
   verbatim; if it moves, D1-7 imports it instead.

2. **`indexer.Escalation` cannot express `IndexerStatus.InitialFailureAt`.** The status
   has `escalationLevel`, `disabledUntil`, `initialFailureAt`, `lastFailureAt` and
   `lastFailure`; `Escalation` (D1-3, pinned in `02-interfaces.md`) has only the first,
   second, fourth and fifth. Under `indexarr-worker`'s complete-declaration rule,
   `initialFailureAt` must be sent on every apply by someone, and nothing produces it.
   This task carries `cur.InitialFailureAt` forward and sets it on the 0→1 transition,
   which is a guess about Prowlarr's semantics. Either add `InitialFailureAt` to
   `Escalation` in D1-3, or confirm the carry-forward.

3. **`schema.Release.Kind` has no authoritative source.** The matcher's *first* dispatch
   is `rel.Kind` and anything that is not movie or series matches nothing, silently. But
   `release.ParsedRelease` does not carry the kind `Parse` dispatched on, and `Parse`
   classifies the **id-stripped** title while `release.ClassifyKind` classifies whatever
   it is given — so for a title carrying `[tmdbid-603]` the two can disagree. This task
   works around it by classifying once and passing the result into `Options{Kind:}`.
   The real fix is a `Kind` field on `ParsedRelease`, which is a `pkg/release` change no
   D1 task owns.

#### Disagreements found between the shipped matcher and the spec

- **Which name is the second segment.** Spec §5's subject row and
  `events.ReleaseSubject`'s parameter are both called `indexerName`, and
  `commonv1.ReleaseInfo` carries *both* `IndexerRef` ("the name of the Indexer object")
  and `IndexerName` ("display name at search time"). The shipped matcher settles it: it
  logs `rel.Info.IndexerRef` (`handler.go:175`) and looks up indexer priority by
  `rel.Info.IndexerRef` (`resolve.go:216`). **The object name wins** for the envelope
  key's second segment, the subject token and `Info.IndexerRef`; the display name, if one
  ever exists, goes only in `Info.IndexerName`. Recorded here because the spec text reads
  the other way at first glance.
- **The dedup window quoted for schedules.** The 2-hour `Duplicates` window is
  `CLUSTARR_RELEASES` (`topology.go:400-411`), which is what makes a **duplicate poll**
  harmless. `CLUSTARR_WORK_INDEXARR`, where the scheduled `RssTask` lives, dedups for
  **1 hour** (`topology.go:372-384`). A duplicate *schedule* is therefore suppressed by
  the 1h window, and only if that window is exceeded does the 2h release window do the
  work. The two are often quoted as one; they are not, and the worker relies on both.
### Task D1-5: the search fan-out and `rpc.indexarr.search`

**Why this task is different from every other task in D1.** The wire contract is
not being designed here — it shipped in Phase C and is *already being called*.
`catalogarr/worker/search` builds a `schema.SearchRequest`, sends it to
`clustarr.rpc.indexarr.search`, gets `events.ErrNoResponders` and retries in 15 s,
forever. This task is the server that answers. Every payload type is frozen in
`pkg/events/schema/index.go`; **do not add a field, do not rename a field, do not
"fix" a tag**. The client half (`catalogarr/worker/search/rpc.go:38-45`) states it
in the source: *"indexarr (Phase D) MUST serve that subject with exactly
schema.SearchRequest -> schema.SearchResponse"*.

In Phase C two tasks each built the download-source mapping and the results
disagreed on an immutable field, which made the two grab paths mutually exclusive
at the apiserver. The two mappings this task must **not** build are named in
Interfaces — Consumes: the release projection belongs to D1-7, the per-host rate
limiter belongs to D1-3.

---

#### Files

**Create**

| Path | Contents |
| --- | --- |
| `indexarr/search/doc.go` | Package doc: the frozen contract, the field-manager ownership set, the two things this package must not build. |
| `indexarr/search/service.go` | `Service`, `IndexerClient`, `ClientFor`, `DownloadFn`, `QueryFn`, `Serve`, `Search`, the three bus handlers. |
| `indexarr/search/select.go` | `candidate`, `selectCandidates`, the closed set of skip reasons. Pure. |
| `indexarr/search/query.go` | `modeFor`, `paramSupported`, `queryCategories`, `buildQuery`. Pure. |
| `indexarr/search/merge.go` | `indexerResult`, `mergeReleases` — dedupe, rank, cap. Pure. |
| `indexarr/search/status.go` | `recordOutcome` — the complete ten-field `indexarr-worker` declaration. |
| `indexarr/search/fanout.go` | `fanOut` — budgets, parallelism, pre-named outcome slots, metrics, relindex upsert. |

**Create (tests)**

| Path | Contents |
| --- | --- |
| `indexarr/search/query_test.go` | R5 vocabulary pin, id-param gating, anime inference, category intersection. |
| `indexarr/search/select_test.go` | Every gate and every skip reason, table-driven. |
| `indexarr/search/merge_test.go` | Infohash collapse, `(indexer, guid)` collapse, priority/seeders tiebreak, cap + `Truncated`. |
| `indexarr/search/fanout_test.go` | Deadline division, slow-indexer timeout outcome, partial failure, relindex failure is non-fatal. |
| `indexarr/search/serve_bus_test.go` | **The contract test.** Drives `Serve` over `membus` using the *shipped caller's own* `NewBusSearchRPC` and `BuildSearchRequest`. |
| `indexarr/search/suite_envtest_test.go` | envtest bootstrap, `requireEnvtest`, `newTestManager`, `eventually`. |
| `indexarr/search/status_envtest_test.go` | The SSA release test: steady state first, then the failure path. |

**Modify:** none. `indexarr/run.go` is wired by **D1-8**; do not touch it.

#### Path ownership

- **Owns:** `indexarr/search/**` — nothing else.
- **Must not touch:** `indexarr/run.go` (D1-8), `indexarr/controller/indexer/**` (D1-3),
  `indexarr/worker/rss/**` (D1-7), `pkg/relindex/**` (D1-2), `go.mod`/`go.sum`,
  `api/**`, `pkg/events/**`, `pkg/k8s/**`, `config/**`, `charts/**` (all D1-0).
- **Commit path-scoped:** `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "<msg>" -- indexarr/search`

#### Interfaces — Consumes

Verbatim signatures. Every one of these already exists, or is pinned by another
D1 task's section. Nothing here is to be reimplemented.

```go
// ---- pkg/events (SHIPPED) ----
const RPCIndexSearch   = "clustarr.rpc.indexarr.search"   // subjects.go:156
const RPCIndexDownload = "clustarr.rpc.indexarr.download" // subjects.go:157
const RPCIndexQuery    = "clustarr.rpc.indexarr.query"    // subjects.go:158
const QueueGroupIndexarr = "indexarr"                     // subjects.go:162

type Requester interface {
    Request(ctx context.Context, subject string, in, out any) error
    Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) error
}
type Bus interface { Publisher; Subscriber; Requester; KV(bucket string) KV; Ensure(ctx context.Context, t Topology) error; Close() error }

// ---- pkg/events/schema (SHIPPED AND FROZEN) ----
const MaxSearchReleases = 500

type SearchRequest struct {
    Kind           commonv1.MediaKind  `json:"kind"`
    Text           string              `json:"text,omitempty"`
    IDs            map[string]string   `json:"ids,omitempty"`
    Season         *int32              `json:"season,omitempty"`
    Episode        *int32              `json:"episode,omitempty"`
    Year           int32               `json:"year,omitempty"`
    Categories     []int32             `json:"categories,omitempty"`
    IndexerRefs    []Ref               `json:"indexerRefs,omitempty"`
    Protocols      []commonv1.Protocol `json:"protocols,omitempty"`
    Limit          int32               `json:"limit,omitempty"`
    DeadlineMillis int64               `json:"deadlineMillis,omitempty"`
    UserInvoked    bool                `json:"userInvoked,omitempty"`
}
type SearchResponse struct {
    Releases  []Release       `json:"releases,omitempty"`
    Outcomes  []SearchOutcome `json:"outcomes,omitempty"`
    Truncated bool            `json:"truncated,omitempty"`
}
type SearchOutcome struct {
    IndexerRef    Ref                 `json:"indexerRef"`
    IndexerName   string              `json:"indexerName,omitempty"`
    Status        SearchOutcomeStatus `json:"status"`
    Releases      int32               `json:"releases"`
    ElapsedMillis int64               `json:"elapsedMillis,omitempty"`
    Error         string              `json:"error,omitempty"`
}
type SearchOutcomeStatus string
const (
    SearchOutcomeOK      SearchOutcomeStatus = "ok"
    SearchOutcomeTimeout SearchOutcomeStatus = "timeout"
    SearchOutcomeError   SearchOutcomeStatus = "error"
    SearchOutcomeSkipped SearchOutcomeStatus = "skipped"
)
type Ref struct { Namespace string `json:"namespace,omitempty"`; Name string `json:"name"`; UID string `json:"uid,omitempty"` }

// ---- indexarr/controller/indexer (D1-3) -- PURE, never writes to the apiserver ----
func Healthy(st indexv1alpha1.IndexerStatus, now time.Time) bool
func SupportsMode(caps indexv1alpha1.Caps, mode string) bool
func RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) Escalation
func RecordFailure(cur indexv1alpha1.IndexerStatus, now time.Time, reason string) Escalation
type Escalation struct {
    FailureLevel   int32
    DisabledUntil  *metav1.Time
    LastFailureAt  *metav1.Time
    LastFailureMsg string
    Changed        bool
}

// ---- indexarr/worker/rss (D1-7) -- the ONE release projection ----
func ProjectRelease(r torznab.Release, indexerName, protocol string) schema.Release

// ---- pkg/relindex (D1-2) ----
type Store interface {
    Upsert(ctx context.Context, rels []Release) (inserted int, err error)
    Search(ctx context.Context, q Query) ([]Release, error)
    Prune(ctx context.Context, olderThan time.Time) (deleted int, err error)
    Stats(ctx context.Context) (Stats, error)
}

// ---- pkg/torznab (SHIPPED) ----
type SearchMode string
const (
    ModeSearch      SearchMode = "search"
    ModeTVSearch    SearchMode = "tvsearch"
    ModeMovieSearch SearchMode = "movie"
    ModeMusicSearch SearchMode = "music"
    ModeAudioSearch SearchMode = "audio"
    ModeBookSearch  SearchMode = "book"
)
type Query struct {
    Type                             SearchMode
    Q                                string
    Categories                       []newznab.CategoryID
    IMDBID, TMDBID, TVDBID, TVMazeID string
    Season                           *int
    Episode                          string
    Artist, Album                    string
    Author, Title                    string
    Limit, Offset                    int
}
func (c *Client) Search(ctx context.Context, q Query) ([]Release, error)
type Error struct { Code ErrorCode; Description string; HTTPStatus int; RetryAfter time.Duration }
const ( ErrRequestLimitReached ErrorCode = 500; ErrDownloadLimitReached ErrorCode = 501 ) // errors.go:32-47

// ---- pkg/newznab (SHIPPED) ----
type CategoryID int32
func Expand(ids []CategoryID) []CategoryID
func (c CategoryID) Parent() CategoryID

// ---- pkg/k8s (SHIPPED; D1-0 adds ManagerIndexarrWorker) ----
func PatchStatus[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
const ManagerIndexarrWorker FieldManager = "indexarr-worker"

// ---- pkg/obs (SHIPPED) ----
func tracing.Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span)
func tracing.RecordError(span trace.Span, err error)
func logging.FromContext(ctx context.Context) *slog.Logger
var metrics.IndexerQueryDuration   // clustarr_indexer_query_duration_seconds{indexer,function}
var metrics.IndexerQueriesTotal    // clustarr_indexer_queries_total{indexer,outcome}
var metrics.IndexerReleasesReturned // clustarr_indexer_releases_returned{indexer}
```

#### Interfaces — Produces

```go
package search // indexarr/search

// IndexerClient is the one call the fan-out makes against a live indexer. It is
// an interface, not *torznab.Client, so the fan-out is testable without a
// network and so a Cardigann-backed client (M6) drops in unchanged.
type IndexerClient interface {
    Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error)
}

// ClientFor returns the wire client for one Indexer, ALREADY carrying that
// host's injected ratelimit.Limiter, its timeout and its proxy. D1-3's
// reconciler owns the limiter cache and supplies this function; this package
// never constructs a ratelimit.Limiter and never calls torznab.NewClient.
type ClientFor func(ctx context.Context, idx *indexv1alpha1.Indexer) (IndexerClient, error)

// DownloadFn and QueryFn are the other two verbs' bodies, supplied by D1-6. A
// nil one answers with a populated Error field rather than a handler error.
type DownloadFn func(ctx context.Context, req schema.DownloadRequest) schema.DownloadResponse
type QueryFn    func(ctx context.Context, req schema.QueryRequest) schema.QueryResponse

type Service struct {
    Client    client.Client // reads Indexer objects; the manager's cached client
    ClientFor ClientFor
    Store     relindex.Store // nil disables the local-index side effect
    Download  DownloadFn
    Query     QueryFn
    Now       func() time.Time // nil means time.Now
    // unexported: srvCtx, stopOnce, inflight
}

// Serve registers all three RPC verbs on the bus under queue group "indexarr".
// stop() drains in-flight fan-outs; it cannot deregister the responder, because
// events.Requester.Serve has no unsubscribe -- the responder lives until
// bus.Close(). stop is idempotent.
func Serve(ctx context.Context, bus events.Bus, s *Service) (stop func(), err error)

// Search is the search verb's body, exported so D1-9's e2e and the bus contract
// test can drive it without a bus. It NEVER returns an error: every failure is
// reported as a named SearchOutcome, because an error reply discards the
// outcomes (see step 52).
func (s *Service) Search(ctx context.Context, req schema.SearchRequest) schema.SearchResponse

// Budget constants.
const (
    MaxRPCBudget          = 45 * time.Second // spec §5: "single reply at min(deadline, 45s)"
    DefaultIndexerTimeout = 30 * time.Second // Indexer.spec.timeout's own default
)
```

---

#### Design points, decided here so no step has to decide them

**D1. The deadline is divided, not shared.** `fanoutBudget =
min(req.DeadlineMillis, 45s) - 2s reply margin`, floored at 1 s. Each indexer runs
under `context.WithTimeout(fanCtx, min(idx.spec.timeout, fanoutBudget))`. The whole
fan-out runs under `context.WithTimeout(fanCtx, fanoutBudget)`. **The reply is sent
when all goroutines finish or `fanoutBudget` expires, whichever comes first** — one
slow indexer cannot consume the budget, because every other indexer's result is
already in its slot. Stragglers keep running (bounded by their own timeout) so their
status apply and relindex upsert still land; that is why `fanCtx` is rooted at
`context.WithoutCancel(reqCtx)` and tied to the service lifetime with
`context.AfterFunc(s.srvCtx, cancelFan)`, not at the handler's ctx, which the bus
cancels the moment the reply is written.

**D2. Every candidate gets exactly one outcome, and it is named before anything can
fail.** The caller *silently drops* a nameless outcome (`worker.go:658-661`: it is
`listType=map` keyed by name, so a keyless entry would make the apiserver reject the
whole status apply). An indexer that fails while building its client, or that times
out, must still appear. So: allocate `outcomes[i]` **first**, with
`IndexerRef{Namespace, Name}` *and* `IndexerName` both set from the object, and
`Status: SearchOutcomeTimeout` — the honest default, because "still running when the
reply was sent" is exactly what is true before a worker writes its slot. Workers
overwrite the slot; they never append.

**D3. Selection vs skipping are different things.** An indexer excluded by
`req.IndexerRefs` is **not a candidate** and gets no outcome — the caller asked for a
subset and 200 unwanted outcomes would blow its 100-entry cap. An indexer that *is*
in scope but cannot serve this query gets a named `skipped` outcome with a reason
from a **closed set** of strings (never indexer-supplied text).

**D4. R5: the mode vocabulary is `torznab.SearchMode`'s wire values.** `movie` and
`tvsearch`, **not** `movie-search` and `tv-search`. The CRD doc comments say the
latter and D1-0 corrects them. There is no enum marker on `Caps.Modes`, so a wrong
string here makes `indexer.SupportsMode` return false for every indexer and the
service silently searches nothing while reporting a tidy list of `skipped` outcomes.
Step 3 pins the vocabulary with a test that fails if it drifts.

**D5. Record through D1-3's pure functions; apply under `indexarr-worker`.** One
apply per indexer, and every apply declares the **complete** set that manager owns.
Ruling R6 fixes that set at ten fields:

```
escalationLevel  disabledUntil  initialFailureAt  lastFailureAt  lastFailure
queriesInWindow  grabsInWindow  lastRssAt  lastRssNewCount  indexedReleases
```

D1-7's RSS worker writes the *same* ten under the *same* manager. If either side
declares nine, the tenth is released — SSA replaces a manager's ownership set, it
does not merge. `recordOutcome` therefore reads the live object and re-declares all
ten, carrying through the fields it did not change (`lastRssAt`, `lastRssNewCount`,
`grabsInWindow`). `CLAUDE.md` records eight distinct forms this failure has already
taken in this repo.

**D6. Dedupe is infohash first, then `(indexer, guid)`.** Exactly the two keys spec
§6.2 names, and no third. Cross-indexer collapse therefore happens for torrents only;
`(indexer, guid)` is per-indexer by construction and only catches an indexer
returning the same guid twice in one page. **Usenet releases offered by two indexers
are not collapsed** — record it as a carried item rather than inventing a
title+size key.

**D7. The cap is `schema.MaxSearchReleases` and truncation is reported, weakly.**
`Truncated` is set when the merged set exceeded the cap. The shipped caller only
*logs* it (`worker.go:316-318`) — no condition, no retry — so do not expect it to
surface anywhere; set it truthfully anyway, because the Torznab facade (M6) will
need it.

**D8. The local index is a side effect that can never fail the search.** Each
worker upserts its projected releases into `relindex.Store`; on error it logs at
Warn, counts nothing, and leaves the outcome alone — the indexer answered correctly,
and a full disk must not turn a good search into a nak storm.

**D9. Error mapping — what the caller does with each shape.**

| Situation | Reply | Why |
| --- | --- | --- |
| Malformed JSON, or `kind` empty | **handler error** | Nothing else is possible; the caller naks and its `30s, 2m, 10m` ladder with `MaxDeliver: 5` ends in the DLQ, which is right for a bug-shaped input. |
| No `Indexer` objects exist at all | `SearchResponse{}` — success, empty | An error would nak forever against a cluster nobody has configured yet. |
| Every candidate skipped | success, one named `skipped` outcome each | The reason list is the operator's whole diagnosis. |
| Every indexer failed | success, one named `error` outcome each | **An error reply throws the outcomes away** — the caller returns at `worker.go:284-287` before it ever reaches `writeResults`. The operator would see "search failed" and no reason. |
| Partial failure | success, mixed outcomes, whatever releases arrived | |
| `events.ErrNoResponders` | never produced here | It is what the caller sees when `Serve` was never called. D1-8 must call `Serve` **before** the readiness probe passes. |

`SearchResponse` has **no `Error` field** (unlike `DownloadResponse` and
`QueryResponse`) — that asymmetry is why the outcomes carry everything.

**D10. Metrics are never labelled by an indexer-supplied string.** `indexer` is the
Kubernetes object name; `function` is one of the six `torznab.SearchMode` values;
`outcome` is one of our five closed strings (`ok`, `error`, `timeout`, `skipped`,
`rate_limited`). A `torznab.Error.Description` comes off the wire and must never
reach a label.

---

#### Steps

**Package skeleton and the R5 vocabulary**

- [ ] 1. Create `indexarr/search/doc.go`: the GPL-3.0 header from
  `hack/boilerplate.go.txt`, then `package search` with a doc comment stating (a) the
  payload types are frozen in `pkg/events/schema/index.go` and already called by
  `catalogarr/worker/search`, (b) the ten `Indexer.status` fields owned by
  `k8s.ManagerIndexarrWorker` listed verbatim from D5 above, and (c) the two things
  this package must not build: *"the torznab.Release -> schema.Release projection is
  rss.ProjectRelease (D1-7); the per-host rate limiter is built by D1-3's reconciler
  and arrives through ClientFor."* Run `go build ./indexarr/...` — it compiles.

- [ ] 2. Write `indexarr/search/query_test.go` with the vocabulary pin, and only it:

  ```go
  func TestModeForUsesTorznabWireValues(t *testing.T) {
      require.Equal(t, torznab.ModeMovieSearch, modeFor(commonv1.MediaKindMovie))
      require.Equal(t, torznab.ModeTVSearch, modeFor(commonv1.MediaKindEpisode))
      require.Equal(t, torznab.ModeSearch, modeFor(commonv1.MediaKindSeries))
      // R5: the CRD doc comments say "tv-search"/"movie-search". They are wrong.
      // A wrong string here makes indexer.SupportsMode never match and the
      // service silently searches nothing.
      require.Equal(t, "movie", string(modeFor(commonv1.MediaKindMovie)))
      require.Equal(t, "tvsearch", string(modeFor(commonv1.MediaKindEpisode)))
  }
  ```

- [ ] 3. Run `go test ./indexarr/search/...`. Expect `undefined: modeFor`.

- [ ] 4. Create `indexarr/search/query.go` (GPL header) with `modeFor` only:

  ```go
  // modeFor maps the request's media kind onto the Torznab t= mode. Ruling R5:
  // these are torznab.SearchMode's real wire values, which is what
  // indexer.SupportsMode compares Caps.Modes' keys against; the CRD's
  // "tv-search"/"movie-search" doc comments are wrong and D1-0 fixes them.
  func modeFor(kind commonv1.MediaKind) torznab.SearchMode {
      switch kind {
      case commonv1.MediaKindMovie:
          return torznab.ModeMovieSearch
      case commonv1.MediaKindEpisode:
          return torznab.ModeTVSearch
      default:
          return torznab.ModeSearch
      }
  }
  ```

- [ ] 5. Run `go test ./indexarr/search/...`. It passes. Commit:
  `feat(indexarr): map media kind to the torznab search mode (R5)`.

**Query building: id params, anime, categories**

- [ ] 6. Add to `query_test.go` a table for `paramSupported`, covering: the mode
  present with the param listed → true; the mode present without the param → false;
  the mode absent → false; a nil `Caps` → false. Run it; expect
  `undefined: paramSupported`.

- [ ] 7. Add `paramSupported` to `query.go`:

  ```go
  // paramSupported reports whether the indexer advertises param for mode, e.g.
  // "imdbid" under "movie". Caps.Modes is map[string][]string on the CRD --
  // mode -> supported query parameters -- and a nil Caps means "not probed
  // yet", which is not the same as "not supported"; the caller skips those
  // indexers rather than guessing (see selectCandidates).
  func paramSupported(caps *indexv1alpha1.Caps, mode torznab.SearchMode, param string) bool {
      if caps == nil {
          return false
      }
      for _, p := range caps.Modes[string(mode)] {
          if p == param {
              return true
          }
      }
      return false
  }
  ```

  Run the test; it passes.

- [ ] 8. Add a `queryCategories` table to `query_test.go`: requested `[2000]` against
  an indexer advertising parent `2000` with subs `2040,2050` keeps `[2000]` (the
  parent, not the leaves — the query stays short and the server expands); requested
  `[2000]` against an indexer advertising only `5000` yields empty; requested
  `[5000]` against an indexer advertising sub `5070` under parent `5000` keeps
  `[5000]`; empty `caps.Categories` (unprobed tree) passes the request through
  unchanged. Run it; expect `undefined: queryCategories`.

- [ ] 9. Add `queryCategories` to `query.go`:

  ```go
  // queryCategories intersects the requested Newznab ids with what the indexer
  // advertises. A requested id survives when the indexer serves it or any of its
  // children, so a parent-only request ([2000], which is exactly what
  // BuildSearchRequest sends by default) stays [2000] instead of fanning out to
  // fifty leaves. An indexer with no advertised tree gets the request verbatim:
  // absence of caps is not evidence of absence of support.
  func queryCategories(requested []int32, caps *indexv1alpha1.Caps) []newznab.CategoryID {
      out := make([]newznab.CategoryID, 0, len(requested))
      if caps == nil || len(caps.Categories) == 0 {
          for _, id := range requested {
              out = append(out, newznab.CategoryID(id))
          }
          return out
      }
      served := make(map[int32]struct{}, len(caps.Categories)*4)
      for _, c := range caps.Categories {
          served[c.ID] = struct{}{}
          for _, s := range c.Sub {
              served[s.ID] = struct{}{}
          }
      }
      for _, id := range requested {
          if _, ok := served[id]; ok {
              out = append(out, newznab.CategoryID(id))
              continue
          }
          for sub := range served {
              if int32(newznab.CategoryID(sub).Parent()) == id {
                  out = append(out, newznab.CategoryID(id))
                  break
              }
          }
      }
      return out
  }
  ```

  Run it; it passes.

- [ ] 10. Add a `buildQuery` table to `query_test.go` with four rows, each asserting
  the whole `torznab.Query`: **movie with tmdb+imdb** where caps advertise
  `movie: [imdbid, tmdbid]` → `Type: "movie"`, `IMDBID`, `TMDBID`, `Limit: 500`;
  **episode with tvdb, season 2 episode 5** where caps advertise
  `tvsearch: [tvdbid, season, ep]` → `Season: ptr(2)`, `Episode: "5"`; **anime**
  (`Season == nil`, `Episode == ptr(137)`) → `Season: nil`, `Episode: "137"` and the
  indexer's `spec.animeCategories` appended to `Categories`; **caps advertise
  `tvsearch` but not `tvdbid`** → the returned `ok` is false. Run it; expect
  `undefined: buildQuery`.

- [ ] 11. Add `buildQuery` to `query.go`:

  ```go
  // buildQuery renders one SearchRequest against one Indexer's caps. ok is false
  // when the indexer advertises the mode but none of the request's id parameters,
  // because the request is ids-only: catalogarr NEVER sets Text
  // (catalogarr/worker/search/request.go:65-102), so spec §6.2's
  // "fallback t=search&q=" has no title to fall back to. Carried item: text
  // fallback needs a resolved title that the payload has no field for.
  //
  // Anime arrives as an absolute number in Episode with Season nil and no flag
  // (request.go:59-64). That shape is the only signal, so it is the inference:
  // an episode request with Episode set and Season nil also searches the
  // indexer's animeCategories, which newznab.ByKind cannot reach.
  func buildQuery(req schema.SearchRequest, idx *indexv1alpha1.Indexer, mode torznab.SearchMode, limit int) (torznab.Query, bool) {
      q := torznab.Query{Type: mode, Limit: limit}
      cats := queryCategories(req.Categories, idx.Status.Caps)
      if req.Kind == commonv1.MediaKindEpisode && req.Episode != nil && req.Season == nil {
          for _, c := range idx.Spec.AnimeCategories {
              cats = append(cats, newznab.CategoryID(c))
          }
      }
      q.Categories = cats

      var anyID bool
      if v := req.IDs[commonv1.IDKeyIMDB]; v != "" && paramSupported(idx.Status.Caps, mode, "imdbid") {
          q.IMDBID, anyID = v, true
      }
      if v := req.IDs[commonv1.IDKeyTMDB]; v != "" && paramSupported(idx.Status.Caps, mode, "tmdbid") {
          q.TMDBID, anyID = v, true
      }
      if v := req.IDs[commonv1.IDKeyTVDB]; v != "" && paramSupported(idx.Status.Caps, mode, "tvdbid") {
          q.TVDBID, anyID = v, true
      }
      if req.Season != nil && paramSupported(idx.Status.Caps, mode, "season") {
          s := int(*req.Season)
          q.Season = &s
      }
      if req.Episode != nil && paramSupported(idx.Status.Caps, mode, "ep") {
          q.Episode = strconv.Itoa(int(*req.Episode))
      }
      if req.Text != "" {
          q.Q, anyID = req.Text, true
      }
      return q, anyID
  }
  ```

  Run it; it passes.

- [ ] 12. Run `go vet ./indexarr/search/...` and `golangci-lint-v2 run ./indexarr/search/...`.
  Fix anything reported. Commit:
  `feat(indexarr): build a torznab query from a frozen SearchRequest`.

**Candidate selection**

- [ ] 13. Write `indexarr/search/select_test.go` as one table over
  `selectCandidates(idxs []indexv1alpha1.Indexer, req schema.SearchRequest, mode torznab.SearchMode, now time.Time) []candidate`,
  with one row per gate and the expected `Skip` string:
  `spec.enabled=false` → `"disabled"`; `enableAutomaticSearch=false` with
  `UserInvoked=false` → `"automatic search disabled"`; `enableInteractiveSearch=false`
  with `UserInvoked=true` → `"interactive search disabled"`;
  `req.Protocols=[usenet]` against `status.protocol=torrent` → `"protocol not requested"`;
  `status.caps=nil` → `"caps not probed"`; caps without the mode →
  `"does not support mode tvsearch"`; no category intersection →
  `"no requested category is served by this indexer"`; `indexer.Healthy` false →
  `"unhealthy or in backoff"`; `status.queriesInWindow >= *spec.limits.queryLimit` →
  `"query limit reached"`; and a healthy indexer → `Skip == ""`. Add two rows for
  scoping: with `req.IndexerRefs = [{ns, a}]`, indexer `b` **is absent from the
  result entirely** (not skipped), and a ref whose namespace differs is also absent.
  Run it; expect `undefined: selectCandidates`.

- [ ] 14. Create `indexarr/search/select.go` (GPL header) with the type and the
  closed reason set:

  ```go
  // candidate is one Indexer the request is in scope for. A non-empty Skip means
  // it will not be queried and gets a named "skipped" outcome; an Indexer the
  // request did not ask for is not a candidate at all and produces no outcome,
  // because the caller caps outcomes at 100 and first-wins
  // (catalogarr/worker/search/worker.go:682-708).
  type candidate struct {
      Indexer *indexv1alpha1.Indexer
      Skip    string
  }

  // The skip reasons are a closed set. They reach Prometheus indirectly (as the
  // "skipped" outcome) and the Search CR's status, so none of them may ever
  // interpolate an indexer-supplied string.
  const (
      skipDisabled      = "disabled"
      skipNoAuto        = "automatic search disabled"
      skipNoInteractive = "interactive search disabled"
      skipProtocol      = "protocol not requested"
      skipNoCaps        = "caps not probed"
      skipNoCategory    = "no requested category is served by this indexer"
      skipNoIDParam     = "no supported id parameter for this request"
      skipUnhealthy     = "unhealthy or in backoff"
      skipQueryLimit    = "query limit reached"
  )
  ```

- [ ] 15. Add `selectCandidates` to `select.go`, in this order — cheapest gate first,
  `indexer.Healthy` before anything that would build a client:

  ```go
  func selectCandidates(idxs []indexv1alpha1.Indexer, req schema.SearchRequest, mode torznab.SearchMode, now time.Time) []candidate {
      scope := map[string]struct{}{}
      for _, r := range req.IndexerRefs {
          scope[r.Namespace+"/"+r.Name] = struct{}{}
      }
      protocols := map[commonv1.Protocol]struct{}{}
      for _, p := range req.Protocols {
          protocols[p] = struct{}{}
      }

      out := make([]candidate, 0, len(idxs))
      for i := range idxs {
          idx := &idxs[i]
          if len(scope) > 0 {
              if _, ok := scope[idx.Namespace+"/"+idx.Name]; !ok {
                  continue // not asked for: not a candidate, no outcome
              }
          }
          c := candidate{Indexer: idx}
          switch {
          case idx.Spec.Enabled != nil && !*idx.Spec.Enabled:
              c.Skip = skipDisabled
          case !req.UserInvoked && idx.Spec.EnableAutomaticSearch != nil && !*idx.Spec.EnableAutomaticSearch:
              c.Skip = skipNoAuto
          case req.UserInvoked && idx.Spec.EnableInteractiveSearch != nil && !*idx.Spec.EnableInteractiveSearch:
              c.Skip = skipNoInteractive
          case len(protocols) > 0 && !inProtocols(protocols, idx.Status.Protocol):
              c.Skip = skipProtocol
          case !indexer.Healthy(idx.Status, now):
              c.Skip = skipUnhealthy
          case atQueryLimit(idx):
              c.Skip = skipQueryLimit
          case idx.Status.Caps == nil:
              c.Skip = skipNoCaps
          case !indexer.SupportsMode(*idx.Status.Caps, string(mode)):
              c.Skip = "does not support mode " + string(mode)
          case len(queryCategories(req.Categories, idx.Status.Caps)) == 0 && len(req.Categories) > 0:
              c.Skip = skipNoCategory
          }
          out = append(out, c)
      }
      return out
  }

  func inProtocols(want map[commonv1.Protocol]struct{}, got commonv1.Protocol) bool {
      _, ok := want[got]
      return ok
  }

  // atQueryLimit mirrors Prowlarr's IndexerLimitService: RSS polls and searches
  // both count toward QueryLimit, which is why the counter this reads
  // (status.queriesInWindow) is written by both this package and the RSS worker,
  // under the same indexarr-worker manager.
  func atQueryLimit(idx *indexv1alpha1.Indexer) bool {
      if idx.Spec.Limits == nil || idx.Spec.Limits.QueryLimit == nil {
          return false
      }
      return idx.Status.QueriesInWindow >= *idx.Spec.Limits.QueryLimit
  }
  ```

  Run `go test ./indexarr/search/...`. Every row passes.

- [ ] 16. Add the `skipNoIDParam` gate: it cannot live in `selectCandidates` because
  it needs `buildQuery`'s verdict, so add a second pure helper to `select.go` and a
  test row for it in `select_test.go` (caps advertise `movie` but not `imdbid`/`tmdbid`
  and the request carries only those two ids → `skipNoIDParam`):

  ```go
  // resolveQuery finalises a candidate: it builds the query and converts a
  // "no usable id parameter" verdict into a skip. Kept separate from
  // selectCandidates so both stay pure and individually testable.
  func resolveQuery(c candidate, req schema.SearchRequest, mode torznab.SearchMode, limit int) (torznab.Query, string) {
      q, ok := buildQuery(req, c.Indexer, mode, limit)
      if !ok {
          return torznab.Query{}, skipNoIDParam
      }
      return q, ""
  }
  ```

  Run the tests; they pass.

- [ ] 17. Run `golangci-lint-v2 run ./indexarr/search/...`. Commit:
  `feat(indexarr): gate search candidates on health, caps and limits`.

**Merge: dedupe, rank, cap**

- [ ] 18. Write `indexarr/search/merge_test.go` with four cases:
  (a) the same `InfoHash` from indexers `a` (priority 10, seeders 5) and `b`
  (priority 25, seeders 900) collapses to one release and the survivor is `a`'s —
  **priority beats seeders**; (b) the same `InfoHash` from two indexers at equal
  priority collapses to the higher-seeder one; (c) one indexer returning the same
  `GUID` twice with no `InfoHash` collapses to one; (d) 501 distinct releases cap to
  500 with `truncated == true`, 500 give `truncated == false`. Run it; expect
  `undefined: mergeReleases`.

- [ ] 19. Create `indexarr/search/merge.go` (GPL header) with the input type and the
  key function:

  ```go
  // indexerResult is one indexer's contribution to the merge. Priority is
  // Indexer.spec.priority (1..50, lower wins) and is the FIRST tiebreak, ahead of
  // seeders: spec §6.2 says "keeping best (priority, seeders)".
  type indexerResult struct {
      Name     string
      Priority int32
      Releases []schema.Release
  }

  // dedupeKey is exactly the two keys spec §6.2 names and no third. Infohash
  // first, so the same torrent from three indexers collapses to one; then
  // (indexer, guid), which is per-indexer by construction and therefore only
  // catches an indexer that repeated itself within one page.
  //
  // CARRIED ITEM: a usenet release offered by two indexers has no infohash and
  // distinct guids, so it is NOT collapsed. Inventing a title+size key was
  // considered and rejected -- a wrong dedupe silently loses releases, and
  // pkg/decision downstream already ranks duplicates sanely.
  func dedupeKey(r schema.Release) string {
      if h := strings.ToLower(strings.TrimSpace(r.Info.InfoHash)); h != "" {
          return "hash:" + h
      }
      return "guid:" + r.Info.IndexerRef + "\x00" + r.Info.GUID
  }
  ```

- [ ] 20. Add `mergeReleases` to `merge.go`:

  ```go
  // mergeReleases collapses duplicates, orders the survivors and caps the set.
  // The order decides WHICH releases survive the cap, not how they are ranked:
  // catalogarr re-scores everything through pkg/decision
  // (catalogarr/worker/search/worker.go:311-320). It is fully deterministic so
  // the same inputs always truncate the same way.
  func mergeReleases(results []indexerResult, limit int) ([]schema.Release, bool) {
      type entry struct {
          rel      schema.Release
          priority int32
          seeders  int32
          seq      int
      }
      best := make(map[string]*entry, limit)
      order := make([]string, 0, limit)
      seq := 0
      for _, res := range results {
          for _, r := range res.Releases {
              seq++
              k := dedupeKey(r)
              cur := &entry{rel: r, priority: res.Priority, seeders: seedersOf(r), seq: seq}
              prev, ok := best[k]
              if !ok {
                  best[k] = cur
                  order = append(order, k)
                  continue
              }
              if cur.priority < prev.priority ||
                  (cur.priority == prev.priority && cur.seeders > prev.seeders) {
                  best[k] = cur
              }
          }
      }
      merged := make([]*entry, 0, len(order))
      for _, k := range order {
          merged = append(merged, best[k])
      }
      sort.SliceStable(merged, func(i, j int) bool {
          a, b := merged[i], merged[j]
          if a.priority != b.priority {
              return a.priority < b.priority
          }
          if a.seeders != b.seeders {
              return a.seeders > b.seeders
          }
          return a.seq < b.seq
      })
      truncated := false
      if limit > 0 && len(merged) > limit {
          merged, truncated = merged[:limit], true
      }
      out := make([]schema.Release, 0, len(merged))
      for _, e := range merged {
          out = append(out, e.rel)
      }
      return out, truncated
  }

  func seedersOf(r schema.Release) int32 {
      if r.Info.Seeders == nil {
          return 0
      }
      return *r.Info.Seeders
  }
  ```

  Run `go test ./indexarr/search/...`. All four cases pass.

- [ ] 21. Add a fifth case to `merge_test.go` pinning the cap constant against the
  schema, so a future change to either is caught:

  ```go
  func TestMergeCapIsTheSchemaCap(t *testing.T) {
      rels := make([]schema.Release, 0, schema.MaxSearchReleases+1)
      for i := 0; i <= schema.MaxSearchReleases; i++ {
          rels = append(rels, schema.Release{Info: commonv1.ReleaseInfo{
              IndexerRef: "a", GUID: strconv.Itoa(i),
          }})
      }
      got, truncated := mergeReleases([]indexerResult{{Name: "a", Releases: rels}}, schema.MaxSearchReleases)
      require.Len(t, got, schema.MaxSearchReleases)
      require.True(t, truncated)
  }
  ```

  Run it; it passes. Commit: `feat(indexarr): dedupe and cap merged search results`.

**Status: the ten-field declaration under `indexarr-worker`**

- [ ] 22. Create `indexarr/search/status.go` (GPL header) with **only** the doc
  comment enumerating the owned set, and no code yet:

  ```go
  // ownedStatusFields documents, for humans, the COMPLETE set of Indexer.status
  // fields owned by the k8s.ManagerIndexarrWorker field manager (Ruling R6):
  //
  //   escalationLevel  disabledUntil  initialFailureAt  lastFailureAt  lastFailure
  //   queriesInWindow  grabsInWindow  lastRssAt  lastRssNewCount  indexedReleases
  //
  // Server-side apply REPLACES a manager's ownership set on every apply; it does
  // not merge. A field this manager sent last time and omits this time is
  // released, which reads as "reset to zero" on the object. indexarr/worker/rss
  // writes the same ten under the same manager, so BOTH packages must declare all
  // ten on every apply, carrying through the ones they did not change. CLAUDE.md
  // records eight distinct forms this failure has already taken in this repo.
  //
  // The reconciler's own fields (conditions, protocol, privacy, caps,
  // observedGeneration, sessionSecretRef) belong to k8s.ManagerIndexarr and must
  // never appear in an apply from here.
  ```

- [ ] 23. Write the unit half of `status_envtest_test.go`'s logic as a pure test in
  `fanout_test.go` first — `initialFailureAt` bookkeeping is not in D1-3's
  `Escalation` struct and is this package's job:

  ```go
  func TestInitialFailureAt(t *testing.T) {
      now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
      earlier := metav1.NewTime(now.Add(-time.Hour))
      // first failure of a run starts the clock
      require.Equal(t, metav1.NewTime(now), *initialFailureAt(indexv1alpha1.IndexerStatus{}, 1, now))
      // a continuing run keeps the original
      require.Equal(t, earlier, *initialFailureAt(indexv1alpha1.IndexerStatus{InitialFailureAt: &earlier}, 3, now))
      // success clears it
      require.Nil(t, initialFailureAt(indexv1alpha1.IndexerStatus{InitialFailureAt: &earlier}, 0, now))
  }
  ```

  Run it; expect `undefined: initialFailureAt`.

- [ ] 24. Add `initialFailureAt` to `status.go`:

  ```go
  // initialFailureAt maintains status.initialFailureAt, which indexer.Escalation
  // deliberately does not carry: D1-3's RecordFailure/RecordSuccess compute the
  // backoff, and "when did this run of failures begin" is bookkeeping the
  // applying caller owns.
  func initialFailureAt(cur indexv1alpha1.IndexerStatus, level int32, now time.Time) *metav1.Time {
      if level == 0 {
          return nil
      }
      if cur.InitialFailureAt != nil {
          return cur.InitialFailureAt
      }
      t := metav1.NewTime(now)
      return &t
  }
  ```

  Run it; it passes.

- [ ] 25. Add `recordOutcome` to `status.go` — one apply, all ten fields:

  ```go
  // recordOutcome runs D1-3's pure escalation functions and applies the result
  // under k8s.ManagerIndexarrWorker. It re-reads the live object first, because
  // every apply must re-declare the fields it is not changing (lastRssAt,
  // lastRssNewCount, grabsInWindow all belong to this manager and are written by
  // the RSS worker) -- omitting one releases it.
  //
  // newlyIndexed is what relindex.Upsert actually inserted, so indexedReleases
  // counts distinct releases seen rather than rows returned.
  func (s *Service) recordOutcome(ctx context.Context, ref schema.Ref, ok bool, reason string, queried bool, newlyIndexed int64) error {
      var live indexv1alpha1.Indexer
      if err := s.Client.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &live); err != nil {
          return fmt.Errorf("indexarr/search: get indexer %s: %w", ref, err)
      }
      cur := live.Status
      now := s.now()

      var esc indexer.Escalation
      if ok {
          esc = indexer.RecordSuccess(cur, now)
      } else {
          esc = indexer.RecordFailure(cur, now, reason)
      }

      queries := cur.QueriesInWindow
      if queried {
          queries++
      }

      st := indexac.IndexerStatus().
          WithEscalationLevel(esc.FailureLevel).
          WithQueriesInWindow(queries).
          WithGrabsInWindow(cur.GrabsInWindow).
          WithLastRssNewCount(cur.LastRssNewCount).
          WithIndexedReleases(cur.IndexedReleases + newlyIndexed)
      if t := initialFailureAt(cur, esc.FailureLevel, now); t != nil {
          st = st.WithInitialFailureAt(*t)
      }
      if esc.DisabledUntil != nil {
          st = st.WithDisabledUntil(*esc.DisabledUntil)
      }
      if esc.LastFailureAt != nil {
          st = st.WithLastFailureAt(*esc.LastFailureAt)
      }
      if esc.LastFailureMsg != "" {
          st = st.WithLastFailure(esc.LastFailureMsg)
      }
      if cur.LastRssAt != nil {
          st = st.WithLastRssAt(*cur.LastRssAt)
      }

      ac := indexac.Indexer(ref.Name, ref.Namespace).WithStatus(st)
      _, err := k8s.PatchStatus(ctx, s.Client, k8s.ManagerIndexarrWorker, ac)
      return err
  }
  ```

  Imports: `indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"`.
  Run `go build ./indexarr/...`; it compiles once `Service` exists — if it does not
  yet, park this step's build check until step 40 and move on.

- [ ] 26. Run `golangci-lint-v2 run ./indexarr/search/...` and confirm **forbidigo
  reports nothing** — the only status write in the package goes through
  `k8s.PatchStatus`. Commit:
  `feat(indexarr): record search outcomes under the indexarr-worker manager`.

**The fan-out**

- [ ] 27. Add budget tests to `fanout_test.go`:

  ```go
  func TestBudget(t *testing.T) {
      // The shipped caller always sends 45000 and sets NO context deadline, so the
      // real outer bound is the consumer's 120s AckWait: indexarr owns this budget.
      require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 45000}))
      require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{}))           // absent -> 45s
      require.Equal(t, 8*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 10000}))
      require.Equal(t, time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 500})) // floored
      require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: -1}))
  }

  func TestIndexerDeadlineIsCappedByTheFanoutBudget(t *testing.T) {
      idx := &indexv1alpha1.Indexer{Spec: indexv1alpha1.IndexerSpec{Timeout: metav1.Duration{Duration: 30 * time.Second}}}
      require.Equal(t, 8*time.Second, indexerDeadline(idx, 8*time.Second))
      require.Equal(t, 30*time.Second, indexerDeadline(idx, 43*time.Second))
      require.Equal(t, 30*time.Second, indexerDeadline(&indexv1alpha1.Indexer{}, 43*time.Second)) // unset -> default
  }
  ```

  Run it; expect `undefined: fanoutBudget`.

- [ ] 28. Create `indexarr/search/fanout.go` (GPL header) with the constants and both
  budget functions:

  ```go
  const (
      // MaxRPCBudget is spec §5's "single reply at min(deadline, 45s)". It is also
      // natsbus.DefaultRequestTimeout, which bounds the handler's own ctx.
      MaxRPCBudget = 45 * time.Second
      // DefaultIndexerTimeout mirrors Indexer.spec.timeout's CRD default.
      DefaultIndexerTimeout = 30 * time.Second
      // replyMargin is reserved out of the budget for merging, marshalling and
      // the per-indexer status applies, so the reply is written before the
      // caller's own clock runs out.
      replyMargin = 2 * time.Second
      // minFanoutBudget keeps a pathological deadline from producing a zero
      // budget, which would time out every indexer before it started.
      minFanoutBudget = time.Second
  )

  func fanoutBudget(req schema.SearchRequest) time.Duration {
      b := MaxRPCBudget
      if req.DeadlineMillis > 0 {
          if d := time.Duration(req.DeadlineMillis) * time.Millisecond; d < b {
              b = d
          }
      }
      if b -= replyMargin; b < minFanoutBudget {
          b = minFanoutBudget
      }
      return b
  }

  // indexerDeadline divides the budget: each indexer gets its own spec.timeout,
  // never more than what is left of the whole fan-out. One slow indexer therefore
  // cannot consume the budget -- every other indexer's slot is already filled and
  // the reply goes out on time with the straggler reported as a timeout.
  func indexerDeadline(idx *indexv1alpha1.Indexer, budget time.Duration) time.Duration {
      t := idx.Spec.Timeout.Duration
      if t <= 0 {
          t = DefaultIndexerTimeout
      }
      if t > budget {
          t = budget
      }
      return t
  }
  ```

  Run the tests; they pass.

- [ ] 29. Add the outcome-naming test to `fanout_test.go` — this is the one that
  stops an indexer becoming invisible:

  ```go
  func TestNewOutcomeIsNamedBeforeAnythingCanFail(t *testing.T) {
      idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nzbgeek"}}
      got := newOutcome(idx)
      require.Equal(t, schema.Ref{Namespace: "media", Name: "nzbgeek"}, got.IndexerRef)
      require.Equal(t, "nzbgeek", got.IndexerName)
      // The default is "timeout" because that is what is TRUE before a worker
      // writes its slot: still running when the reply had to be sent.
      require.Equal(t, schema.SearchOutcomeTimeout, got.Status)
  }
  ```

  Run it; expect `undefined: newOutcome`.

- [ ] 30. Add `newOutcome` and the closed metric-outcome vocabulary to `fanout.go`:

  ```go
  // newOutcome names the outcome BEFORE the indexer is touched. The caller drops
  // a nameless outcome silently (catalogarr/worker/search/worker.go:658-661:
  // status.indexerOutcomes is listType=map keyed by name), so an indexer that
  // fails while its client is being built would otherwise vanish from the
  // operator's view entirely. Both IndexerRef.Name and IndexerName are set: the
  // caller prefers the former and falls back to the latter.
  func newOutcome(idx *indexv1alpha1.Indexer) schema.SearchOutcome {
      return schema.SearchOutcome{
          IndexerRef:  schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
          IndexerName: idx.Name,
          Status:      schema.SearchOutcomeTimeout,
      }
  }

  // The metric outcome label vocabulary. Closed, five values, never derived from
  // a torznab.Error.Description or any other indexer-supplied string.
  const (
      metricOK          = "ok"
      metricError       = "error"
      metricTimeout     = "timeout"
      metricSkipped     = "skipped"
      metricRateLimited = "rate_limited"
  )
  ```

  Run it; it passes.

- [ ] 31. Add the failure-classification test to `fanout_test.go`: a
  `*torznab.Error{Code: 500}` and a `*torznab.Error{HTTPStatus: 429}` both classify
  as `metricRateLimited`; `context.DeadlineExceeded` classifies as `metricTimeout`;
  anything else as `metricError`. Also assert the **message** carried into the
  outcome is bounded: `classify` returns a reason truncated to 512 bytes on a rune
  boundary (the caller truncates at the same number; doing it here keeps the RPC
  payload small). Run it; expect `undefined: classifyFailure`.

- [ ] 32. Add `classifyFailure` to `fanout.go`:

  ```go
  // maxOutcomeError bounds one failure message. catalogarr truncates at the same
  // 512 bytes (worker.go:655); doing it here too keeps the reply small when fifty
  // indexers fail with verbose XML errors.
  const maxOutcomeError = 512

  func classifyFailure(err error) (schema.SearchOutcomeStatus, string, string) {
      var te *torznab.Error
      switch {
      case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
          return schema.SearchOutcomeTimeout, metricTimeout, truncateUTF8(err.Error(), maxOutcomeError)
      case errors.As(err, &te) && (te.Code == torznab.ErrRequestLimitReached ||
          te.Code == torznab.ErrDownloadLimitReached || te.HTTPStatus == http.StatusTooManyRequests):
          return schema.SearchOutcomeError, metricRateLimited, truncateUTF8(err.Error(), maxOutcomeError)
      default:
          return schema.SearchOutcomeError, metricError, truncateUTF8(err.Error(), maxOutcomeError)
      }
  }

  func truncateUTF8(s string, max int) string {
      if len(s) <= max {
          return s
      }
      for max > 0 && !utf8.RuneStart(s[max]) {
          max--
      }
      return s[:max]
  }
  ```

  Run the tests; they pass.

- [ ] 33. Write the fan-out's main test in `fanout_test.go` using a stub
  `IndexerClient`: three indexers where `fast` returns two releases immediately,
  `broken` returns an error, and `slow` blocks on `<-ctx.Done()`. Assert: exactly
  three outcomes; `fast` is `ok` with `Releases: 2` and a non-zero `ElapsedMillis`;
  `broken` is `error` with a non-empty `Error`; `slow` is `timeout`; **all three are
  named**; and the call returns in well under the budget. Use a 200 ms
  `DeadlineMillis` equivalent by calling `fanOut` with an explicit small budget.
  Run it; expect `undefined: fanOut`.

- [ ] 34. Add `fanOut` to `fanout.go` — slots, parallelism, and the deadline race:

  ```go
  // fanOut queries every non-skipped candidate in parallel and returns one
  // outcome per candidate, in candidate order, plus each indexer's releases.
  //
  // It returns as soon as every worker has finished OR the budget expires,
  // whichever comes first. Stragglers are NOT abandoned: their context is rooted
  // at context.WithoutCancel(ctx) so that returning from the RPC handler (which
  // the bus cancels the moment the reply is written) does not kill a status apply
  // or a relindex upsert halfway through. They are bounded by their own
  // per-indexer deadline and by the service lifetime via context.AfterFunc.
  func (s *Service) fanOut(ctx context.Context, cands []candidate, req schema.SearchRequest, mode torznab.SearchMode, budget time.Duration) ([]schema.SearchOutcome, []indexerResult) {
      outcomes := make([]schema.SearchOutcome, len(cands))
      results := make([]indexerResult, len(cands))
      for i, c := range cands {
          outcomes[i] = newOutcome(c.Indexer)
          results[i] = indexerResult{Name: c.Indexer.Name, Priority: c.Indexer.Spec.Priority}
      }

      var mu sync.Mutex
      var wg sync.WaitGroup
      work := context.WithoutCancel(ctx)
      work, cancelWork := context.WithCancel(work)
      defer context.AfterFunc(s.srvCtx, cancelWork)()

      for i, c := range cands {
          if c.Skip != "" {
              outcomes[i].Status = schema.SearchOutcomeSkipped
              outcomes[i].Error = c.Skip
              metrics.IndexerQueriesTotal.WithLabelValues(c.Indexer.Name, metricSkipped).Inc()
              continue
          }
          q, skip := resolveQuery(c, req, mode, queryLimit(req, c.Indexer))
          if skip != "" {
              outcomes[i].Status = schema.SearchOutcomeSkipped
              outcomes[i].Error = skip
              metrics.IndexerQueriesTotal.WithLabelValues(c.Indexer.Name, metricSkipped).Inc()
              continue
          }
          wg.Add(1)
          s.inflight.Add(1)
          go func(i int, c candidate, q torznab.Query) {
              defer wg.Done()
              defer s.inflight.Done()
              out, rels := s.queryOne(work, c.Indexer, q, indexerDeadline(c.Indexer, budget))
              mu.Lock()
              outcomes[i] = out
              results[i].Releases = rels
              mu.Unlock()
          }(i, c, q)
      }

      done := make(chan struct{})
      go func() { wg.Wait(); close(done) }()
      timer := time.NewTimer(budget)
      defer timer.Stop()
      select {
      case <-done:
      case <-timer.C:
          logging.FromContext(ctx).Warn("search: replying before every indexer finished", "budget", budget)
      }

      mu.Lock()
      defer mu.Unlock()
      return append([]schema.SearchOutcome(nil), outcomes...), append([]indexerResult(nil), results...)
  }

  // queryLimit clamps the requested page size to what the indexer will accept.
  func queryLimit(req schema.SearchRequest, idx *indexv1alpha1.Indexer) int {
      n := int(req.Limit)
      if n <= 0 || n > schema.MaxSearchReleases {
          n = schema.MaxSearchReleases
      }
      if idx.Status.Caps != nil && idx.Status.Caps.LimitsMax > 0 && int(idx.Status.Caps.LimitsMax) < n {
          n = int(idx.Status.Caps.LimitsMax)
      }
      return n
  }
  ```

  Note the copy under `mu` before returning: a straggler keeps writing its slot
  after the reply, and the copy is what makes that safe.

- [ ] 35. Add `queryOne` to `fanout.go` — one span, one metric triple, one status
  apply, one index upsert:

  ```go
  // queryOne runs one indexer end to end. It never returns an error: the outcome
  // IS the error channel, because an error reply would discard every outcome (see
  // Service.Search).
  func (s *Service) queryOne(ctx context.Context, idx *indexv1alpha1.Indexer, q torznab.Query, timeout time.Duration) (schema.SearchOutcome, []schema.Release) {
      out := newOutcome(idx)
      ctx, cancel := context.WithTimeout(ctx, timeout)
      defer cancel()
      ctx, span := tracing.Start(ctx, "indexarr.search.indexer",
          trace.WithAttributes(attribute.String("indexer", idx.Name), attribute.String("mode", string(q.Type))))
      defer span.End()

      started := s.now()
      cli, err := s.ClientFor(ctx, idx)
      if err != nil {
          return s.failOutcome(ctx, out, started, err), nil
      }
      raw, err := cli.Search(ctx, q)
      elapsed := s.now().Sub(started)
      metrics.IndexerQueryDuration.WithLabelValues(idx.Name, string(q.Type)).Observe(elapsed.Seconds())
      if err != nil {
          return s.failOutcome(ctx, out, started, err), nil
      }

      protocol := string(idx.Status.Protocol)
      rels := make([]schema.Release, 0, len(raw))
      for _, r := range raw {
          // rss.ProjectRelease is the ONE torznab.Release -> schema.Release
          // projection in this repo (D1-7 owns it). Building a second one here is
          // the Phase C download-source defect repeated.
          rels = append(rels, rss.ProjectRelease(r, idx.Name, protocol))
      }

      inserted := s.index(ctx, idx, rels)
      metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, metricOK).Inc()
      metrics.IndexerReleasesReturned.WithLabelValues(idx.Name).Observe(float64(len(rels)))

      if err := s.recordOutcome(ctx, out.IndexerRef, true, "", true, inserted); err != nil {
          logging.FromContext(ctx).Warn("search: record success", "indexer", idx.Name, "error", err)
      }
      out.Status = schema.SearchOutcomeOK
      out.Releases = int32(len(rels))
      out.ElapsedMillis = elapsed.Milliseconds()
      return out, rels
  }

  func (s *Service) failOutcome(ctx context.Context, out schema.SearchOutcome, started time.Time, err error) schema.SearchOutcome {
      status, metric, msg := classifyFailure(err)
      metrics.IndexerQueriesTotal.WithLabelValues(out.IndexerRef.Name, metric).Inc()
      if rerr := s.recordOutcome(ctx, out.IndexerRef, false, msg, true, 0); rerr != nil {
          logging.FromContext(ctx).Warn("search: record failure", "indexer", out.IndexerRef.Name, "error", rerr)
      }
      out.Status = status
      out.Error = msg
      out.ElapsedMillis = s.now().Sub(started).Milliseconds()
      return out
  }
  ```

- [ ] 36. Add `index` to `fanout.go` — the non-fatal local-index side effect:

  ```go
  // index upserts everything the indexer returned into the local release index,
  // so the SQLite store reflects what was seen and rpc.indexarr.query can replay
  // it. A store failure is logged and swallowed: the indexer answered correctly,
  // and turning a full disk into a failed search would nak the caller's task and
  // re-run the whole fan-out.
  func (s *Service) index(ctx context.Context, idx *indexv1alpha1.Indexer, rels []schema.Release) int64 {
      if s.Store == nil || len(rels) == 0 {
          return 0
      }
      rows := make([]relindex.Release, 0, len(rels))
      for _, r := range rels {
          info, err := json.Marshal(r)
          if err != nil {
              continue
          }
          rows = append(rows, relindex.Release{
              Indexer:     idx.Name,
              GUID:        r.Info.GUID,
              Title:       r.Info.Title,
              TitleNorm:   release.CleanTitle(r.Info.Title),
              Group:       r.Info.ReleaseGroup,
              Protocol:    string(r.Info.Protocol),
              Categories:  intsOf(r.Info.Categories),
              SizeBytes:   r.Info.SizeBytes,
              PublishedAt: timePtr(r.Info.PublishedAt), // nil stays nil: NEVER backfill
              FetchedAt:   r.FetchedAt,
              InfoJSON:    info,
          })
      }
      n, err := s.Store.Upsert(ctx, rows)
      if err != nil {
          logging.FromContext(ctx).Warn("search: upsert release index", "indexer", idx.Name, "error", err)
          return 0
      }
      return int64(n)
  }
  ```

  Add the two small helpers `intsOf([]int32) []int` and
  `timePtr(*metav1.Time) *time.Time`, the latter returning nil for nil.

- [ ] 37. Add a test to `fanout_test.go` proving D8: a `Store` stub whose `Upsert`
  always returns an error still yields an `ok` outcome with the full release count.
  Run `go test ./indexarr/search/...`; everything passes.

- [ ] 38. Run `golangci-lint-v2 run ./indexarr/search/...`, then
  `go test -race ./indexarr/search/...` — the straggler path is exactly what `-race`
  exists for. Commit:
  `feat(indexarr): fan out searches across healthy indexers with a divided deadline`.

**`Service` and `Serve`**

- [ ] 39. Create `indexarr/search/service.go` (GPL header) with the types from
  Interfaces — Produces: `IndexerClient`, `ClientFor`, `DownloadFn`, `QueryFn` and
  `Service` (add unexported `srvCtx context.Context`, `stopOnce sync.Once`,
  `inflight sync.WaitGroup`), plus:

  ```go
  func (s *Service) now() time.Time {
      if s.Now == nil {
          return time.Now()
      }
      return s.Now()
  }
  ```

  Run `go build ./indexarr/...`; the package now compiles as a whole.

- [ ] 40. Add a `Search` test to `fanout_test.go` covering D9's four success-shaped
  rows against a `fake.NewClientBuilder().WithScheme(k8s.MustNewScheme())` client:
  **no Indexer objects** → zero releases, zero outcomes, no panic; **one indexer, all
  gates passed** → one `ok` outcome; **one disabled indexer** → one `skipped` outcome
  named after it; **two indexers, both erroring** → two `error` outcomes and
  `Releases` empty. Assert in every row that `Search` returns a value and never
  panics. Run it; expect `undefined: Search` (method).

- [ ] 41. Add `Search` to `service.go`:

  ```go
  // Search is the body of clustarr.rpc.indexarr.search. It NEVER returns an
  // error, by design: the caller returns immediately on an RPC error
  // (catalogarr/worker/search/worker.go:284-287) and therefore never writes
  // status.indexerOutcomes, so an error reply would throw away the only record of
  // WHY nothing was found. Every failure is a named outcome instead.
  func (s *Service) Search(ctx context.Context, req schema.SearchRequest) schema.SearchResponse {
      ctx, span := tracing.Start(ctx, "indexarr.search.fanout")
      defer span.End()

      mode := modeFor(req.Kind)
      budget := fanoutBudget(req)

      var list indexv1alpha1.IndexerList
      opts := []client.ListOption{}
      if ns := requestNamespace(req); ns != "" {
          opts = append(opts, client.InNamespace(ns))
      }
      if err := s.Client.List(ctx, &list, opts...); err != nil {
          // The only failure with no indexer to name. Report it as one outcome
          // under a reserved name rather than an error, so the operator sees it.
          tracing.RecordError(span, err)
          return schema.SearchResponse{Outcomes: []schema.SearchOutcome{{
              IndexerRef:  schema.Ref{Name: ListOutcomeName},
              IndexerName: ListOutcomeName,
              Status:      schema.SearchOutcomeError,
              Error:       truncateUTF8(err.Error(), maxOutcomeError),
          }}}
      }

      cands := selectCandidates(list.Items, req, mode, s.now())
      outcomes, results := s.fanOut(ctx, cands, req, mode, budget)
      rels, truncated := mergeReleases(results, int(min(int32(schema.MaxSearchReleases), limitOf(req))))

      logging.FromContext(ctx).Info("search: replied",
          "kind", req.Kind, "candidates", len(cands), "releases", len(rels), "truncated", truncated)
      return schema.SearchResponse{Releases: rels, Outcomes: capOutcomes(outcomes), Truncated: truncated}
  }

  // ListOutcomeName is the reserved outcome name used when the failure is
  // indexarr's own and no indexer can be named. It must be non-empty or the
  // caller drops the entry silently.
  const ListOutcomeName = "indexarr"
  ```

  Add `limitOf(req) int32` (clamp to `[1, MaxSearchReleases]`).

- [ ] 42. Add `requestNamespace` to `service.go` with the gap documented in the code,
  not just in the plan:

  ```go
  // requestNamespace scopes the Indexer list. It returns the namespace shared by
  // every IndexerRefs entry, and "" when there are none or they disagree.
  //
  // GAP, carried: schema.SearchRequest has no Namespace field, and
  // catalogarr's buildRequest only populates IndexerRefs for an INTERACTIVE
  // search (worker.go:471-482). An automatic search therefore arrives with no
  // namespace at all, and this service must list Indexers cluster-wide to answer
  // it -- so a Movie in namespace A can be served by an Indexer in namespace B.
  // Fixing it needs either a Namespace field on the frozen payload or catalogarr
  // always populating IndexerRefs; both are out of scope for D1 and neither may
  // be done unilaterally.
  func requestNamespace(req schema.SearchRequest) string {
      ns := ""
      for _, r := range req.IndexerRefs {
          if r.Namespace == "" {
              return ""
          }
          if ns == "" {
              ns = r.Namespace
          } else if ns != r.Namespace {
              return ""
          }
      }
      return ns
  }
  ```

- [ ] 43. Add `capOutcomes` to `service.go`, and a test in `fanout_test.go` asserting
  that with 150 candidates — 50 queried, 100 skipped — the reply carries 100 outcomes
  and every one of the 50 queried ones survives:

  ```go
  // capOutcomes bounds the reply at what the caller can actually persist.
  // catalogarr drops everything past its own MaxIndexerOutcomes = 100, first-wins
  // (worker.go:682-708), so THIS side chooses which hundred matter: the indexers
  // that were actually queried come first, skipped ones fill what is left.
  const maxOutcomes = 100

  func capOutcomes(in []schema.SearchOutcome) []schema.SearchOutcome {
      if len(in) <= maxOutcomes {
          return in
      }
      out := make([]schema.SearchOutcome, 0, maxOutcomes)
      for _, o := range in {
          if o.Status != schema.SearchOutcomeSkipped && len(out) < maxOutcomes {
              out = append(out, o)
          }
      }
      for _, o := range in {
          if o.Status == schema.SearchOutcomeSkipped && len(out) < maxOutcomes {
              out = append(out, o)
          }
      }
      return out
  }
  ```

  Run the tests; they pass.

- [ ] 44. Pin `maxOutcomes` against the caller with a **test-only** import, so the two
  cannot drift without a red build — add to `fanout_test.go`:

  ```go
  // Test-only import of the caller. Production code in indexarr must never import
  // catalogarr; this assertion exists because the two constants describe the same
  // limit from opposite ends of one RPC.
  func TestOutcomeCapMatchesTheCaller(t *testing.T) {
      require.Equal(t, searchworker.MaxIndexerOutcomes, maxOutcomes)
  }
  ```

  with `searchworker "github.com/mediactl/clustarr/catalogarr/worker/search"`. Run it;
  it passes.

- [ ] 45. Add `Serve` to `service.go`:

  ```go
  // Serve registers all three RPC verbs under queue group "indexarr". D1-6
  // supplies Download and Query; D1-8 calls this once from run.go, BEFORE the
  // readiness probe passes -- until it does, every caller gets
  // events.ErrNoResponders and retries in 15s (catalogarr/worker/search/rpc.go:36).
  //
  // stop drains in-flight fan-outs. It cannot deregister the responder:
  // events.Requester.Serve has no unsubscribe and its handlers "run until the bus
  // is closed" (pkg/events/bus.go:159-161). It is idempotent.
  func Serve(ctx context.Context, bus events.Bus, s *Service) (func(), error) {
      switch {
      case s == nil:
          return nil, errors.New("indexarr/search: nil Service")
      case s.Client == nil:
          return nil, errors.New("indexarr/search: nil Service.Client")
      case s.ClientFor == nil:
          return nil, errors.New("indexarr/search: nil Service.ClientFor")
      }
      srvCtx, cancel := context.WithCancel(ctx)
      s.srvCtx = srvCtx

      verbs := []struct {
          subject string
          span    string
          handler func(context.Context, []byte) ([]byte, error)
      }{
          {events.RPCIndexSearch, "indexarr.rpc.serve.search", s.handleSearch},
          {events.RPCIndexDownload, "indexarr.rpc.serve.download", s.handleDownload},
          {events.RPCIndexQuery, "indexarr.rpc.serve.query", s.handleQuery},
      }
      for _, v := range verbs {
          v := v
          if err := bus.Serve(v.subject, events.QueueGroupIndexarr, func(ctx context.Context, data []byte) ([]byte, error) {
              ctx, span := tracing.Start(ctx, v.span)
              defer span.End()
              out, err := v.handler(ctx, data)
              if err != nil {
                  tracing.RecordError(span, err)
              }
              return out, err
          }); err != nil {
              cancel()
              return nil, fmt.Errorf("indexarr/search: serve %s: %w", v.subject, err)
          }
      }
      return func() { s.stopOnce.Do(func() { cancel(); s.inflight.Wait() }) }, nil
  }
  ```

- [ ] 46. Add the three handlers to `service.go`:

  ```go
  func (s *Service) handleSearch(ctx context.Context, data []byte) ([]byte, error) {
      var req schema.SearchRequest
      if err := json.Unmarshal(data, &req); err != nil {
          // A malformed request is the one case with no better answer than an
          // error: the caller naks, its 30s/2m/10m ladder runs out at
          // MaxDeliver 5 and the message lands in the DLQ, which is correct for
          // an input that cannot be parsed.
          return nil, fmt.Errorf("indexarr/search: decode SearchRequest: %w", err)
      }
      if req.Kind == "" {
          return nil, errors.New("indexarr/search: SearchRequest.kind is required")
      }
      return json.Marshal(s.Search(ctx, req))
  }

  func (s *Service) handleDownload(ctx context.Context, data []byte) ([]byte, error) {
      var req schema.DownloadRequest
      if err := json.Unmarshal(data, &req); err != nil {
          return nil, fmt.Errorf("indexarr/search: decode DownloadRequest: %w", err)
      }
      if s.Download == nil {
          return json.Marshal(schema.DownloadResponse{Error: "indexarr: download verb is not configured"})
      }
      return json.Marshal(s.Download(ctx, req))
  }

  func (s *Service) handleQuery(ctx context.Context, data []byte) ([]byte, error) {
      var req schema.QueryRequest
      if err := json.Unmarshal(data, &req); err != nil {
          return nil, fmt.Errorf("indexarr/search: decode QueryRequest: %w", err)
      }
      if s.Query == nil {
          return json.Marshal(schema.QueryResponse{Error: "indexarr: query verb is not configured"})
      }
      return json.Marshal(s.Query(ctx, req))
  }
  ```

- [ ] 47. Run `go test -race ./indexarr/search/...` and
  `golangci-lint-v2 run ./indexarr/search/...`. Commit:
  `feat(indexarr): serve rpc.indexarr.search, .download and .query`.

**The bus contract test — the one that catches a cross-task mismatch**

- [ ] 48. Create `indexarr/search/serve_bus_test.go` (GPL header, package
  `search_test`) that wires the **shipped caller's own client** to this server over
  an in-memory bus. Nothing in this test may construct a `schema.SearchRequest`
  literal — it must come from `BuildSearchRequest`, or the test cannot catch a
  divergence:

  ```go
  func TestServeAnswersTheShippedCaller(t *testing.T) {
      bus := membus.New(nil)
      t.Cleanup(func() { _ = bus.Close() })

      idx := &indexv1alpha1.Indexer{
          ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nzbgeek"},
          Spec:       indexv1alpha1.IndexerSpec{BaseURL: "http://example.invalid", Priority: 25},
          Status: indexv1alpha1.IndexerStatus{
              Protocol: commonv1.ProtocolUsenet,
              Caps: &indexv1alpha1.Caps{Modes: map[string][]string{
                  "movie": {"q", "imdbid", "tmdbid"},
              }, Categories: []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}}},
          },
      }
      c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
          WithObjects(idx).WithStatusSubresource(idx).Build()

      svc := &search.Service{Client: c, ClientFor: stubClientFor(twoReleases())}
      stop, err := search.Serve(context.Background(), bus, svc)
      require.NoError(t, err)
      t.Cleanup(stop)

      // The caller's OWN client and request builder. If the wire shape ever
      // diverges, this line fails rather than production.
      rpc := searchworker.NewBusSearchRPC(bus)
      req := searchworker.BuildSearchRequest(
          commonv1.MediaKindMovie,
          searchworker.TargetIDs{TmdbID: 27205, ImdbID: "tt1375666", Year: 2010},
          schema.MaxSearchReleases, false, nil, nil)
      require.Equal(t, int64(45000), req.DeadlineMillis)
      require.Empty(t, req.Text, "the caller is ids-only; the t=search fallback is unreachable")

      resp, err := rpc.Search(context.Background(), req)
      require.NoError(t, err)
      require.Len(t, resp.Releases, 2)
      require.Len(t, resp.Outcomes, 1)
      require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status)
      require.Equal(t, "nzbgeek", resp.Outcomes[0].IndexerRef.Name)
      require.NotEmpty(t, resp.Outcomes[0].IndexerName, "a nameless outcome is dropped silently by the caller")
      require.False(t, resp.Truncated)
  }
  ```

- [ ] 49. Run `go test ./indexarr/search/... -run TestServeAnswersTheShippedCaller -v`.
  Fix whatever it reports — **this failing is the point of the task**; a mismatch here
  is a mismatch in production.

- [ ] 50. Add a second case to `serve_bus_test.go`: with **no** `Serve` registered,
  `NewBusSearchRPC(bus).Search(...)` returns an error that `errors.Is`-matches
  `events.ErrNoResponders`, and the caller has wrapped it in an `events.RetryError`.
  This documents, executably, what the cluster does before D1-8 wires `Serve`. Run it.

- [ ] 51. Add a third case: an indexer whose stub client blocks until its context is
  cancelled, with `DeadlineMillis: 3000`. Assert the response arrives in under 3 s,
  carries one outcome with `Status == schema.SearchOutcomeTimeout`, and that the
  outcome is named. Run it. Commit:
  `test(indexarr): drive the shipped catalogarr client against the search server`.

**envtest: the SSA release hazard**

- [ ] 52. Create `indexarr/search/suite_envtest_test.go` (GPL header, package
  `search_test`) copying the bootstrap from
  `catalogarr/worker/search/suite_envtest_test.go`: `TestMain` that returns early
  when `KUBEBUILDER_ASSETS` is unset, an `envtest.Environment{CRDDirectoryPaths:
  []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}`, `requireEnvtest`
  that skips with *"KUBEBUILDER_ASSETS is unset; run via `make test`"*, a
  `newTestManager` and an `eventually` poller. Run
  `KUBEBUILDER_ASSETS=$(setup-envtest use -p path) go test ./indexarr/search/...` and
  confirm it does **not** skip.

- [ ] 53. Add to `status_envtest_test.go` the steady-state helper — this is what makes
  the release observable:

  ```go
  // steadyState creates an Indexer and drives its status to a REAL steady state:
  // a completed RSS poll (lastRssAt, lastRssNewCount), a release count and a grab
  // count, all applied under the SAME indexarr-worker manager the search path
  // uses, exactly as indexarr/worker/rss would. A test that skips this and acts on
  // a blank object CANNOT observe a field-manager release, because there is
  // nothing to release -- that is how this class of bug reached main three times
  // in Phase C.
  func steadyState(t *testing.T, ctx context.Context, c client.Client, ns, name string) *indexv1alpha1.Indexer
  ```

  Implement it with one `k8s.PatchStatus` under `k8s.ManagerIndexarrWorker` declaring
  all ten owned fields, with `LastRssAt` an hour ago, `LastRssNewCount: 7`,
  `IndexedReleases: 1234`, `GrabsInWindow: 3`, `QueriesInWindow: 5`.

- [ ] 54. Write the release test in `status_envtest_test.go`:

  ```go
  func TestFailingSearchDoesNotReleaseTheRSSFields(t *testing.T) {
      requireEnvtest(t)
      // ... newTestManager, namespace, steadyState(...)
      svc := &search.Service{Client: mgr.GetClient(), ClientFor: failingClientFor(errors.New("boom"))}
      resp := svc.Search(ctx, req)
      require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)

      var got indexv1alpha1.Indexer
      require.NoError(t, c.Get(ctx, key, &got))
      // The escalation landed ...
      require.Equal(t, int32(1), got.Status.EscalationLevel)
      require.NotNil(t, got.Status.DisabledUntil)
      require.NotNil(t, got.Status.InitialFailureAt)
      require.Equal(t, int32(6), got.Status.QueriesInWindow)
      // ... and NOTHING the RSS worker owns under the same manager was released.
      require.NotNil(t, got.Status.LastRssAt, "lastRssAt was released by a partial apply")
      require.Equal(t, int32(7), got.Status.LastRssNewCount)
      require.Equal(t, int64(1234), got.Status.IndexedReleases)
      require.Equal(t, int32(3), got.Status.GrabsInWindow)
  }
  ```

  Run it with `KUBEBUILDER_ASSETS` set. If `recordOutcome` omits any of the ten, this
  is where it fails.

- [ ] 55. Add the mirror test: a **successful** search from the same steady state
  clears `escalationLevel`, `disabledUntil`, `initialFailureAt`, `lastFailureAt` and
  `lastFailure` (absence in the apply is the declaration "not set", which is the
  intended clear), while `lastRssAt`, `lastRssNewCount` and `grabsInWindow` survive
  and `indexedReleases` has grown by exactly what the store inserted. Run it.

- [ ] 56. Add a third envtest asserting the reconciler's fields are untouched: before
  the search, apply `conditions`, `protocol`, `privacy`, `caps` and
  `sessionSecretRef` under `k8s.ManagerIndexarr`; run a failing search; assert all
  five survive. This proves the R6 split does what it was added for — two managers,
  disjoint sets, no re-assertion. Run it.

- [ ] 57. Run the whole suite with assets and `-race`:
  `KUBEBUILDER_ASSETS=$(setup-envtest use -p path) go test -race ./indexarr/search/...`.
  Confirm the output shows the envtests **running**, not skipping. Commit:
  `test(indexarr): prove the indexarr-worker apply releases nothing`.

**Gates**

- [ ] 58. Run `make lint`. Fix every finding in `indexarr/search`. In particular
  confirm forbidigo flags nothing — `.Status().Update()` and `.Status().Patch()` must
  not appear anywhere in this package.

- [ ] 59. Run `make test` (it sets `KUBEBUILDER_ASSETS` itself) and confirm the
  `indexarr/search` line does not finish in milliseconds — a millisecond suite
  skipped, which is not a pass.

- [ ] 60. Run `make generate && make manifests && git status --porcelain` and confirm
  an empty diff — this task adds no API types, so anything appearing here is a
  mistake.

- [ ] 61. Run `make build` and confirm `bin/clustarr` is produced and no stray binary
  landed in the repo root.

- [ ] 62. Append the four carried items to the phase's carried-items list, each one
  sentence: (a) the ids-only request makes spec §6.2's `t=search&q=` fallback
  unreachable; (b) usenet duplicates across indexers are not collapsed, because only
  infohash and `(indexer, guid)` are keys; (c) `schema.SearchRequest` carries no
  namespace, so an automatic search lists Indexers cluster-wide; (d) spec §6.2's
  `alsoOn` provenance has no field on `schema.Release` or `commonv1.ReleaseInfo` and
  is recorded only in the log. Commit:
  `docs(indexarr): record the four carried items from the search fan-out`.

---

#### Verification (what "done" means for this task)

1. `KUBEBUILDER_ASSETS=$(setup-envtest use -p path) go test -race ./indexarr/search/...`
   runs — not skips — and is green.
2. `TestServeAnswersTheShippedCaller` passes **using
   `searchworker.NewBusSearchRPC` and `searchworker.BuildSearchRequest`**, not a
   hand-written request. That single test is the whole anti-mismatch guarantee: if
   catalogarr's request shape and this server's expectations ever diverge, it goes red.
3. `TestFailingSearchDoesNotReleaseTheRSSFields` passes against a real apiserver with
   an object **already in steady state**.
4. `make lint`, `make test`, `make build` are green and
   `make generate && make manifests` leaves no diff.
5. D1-9's e2e adds the live-indexer scenario against the in-cluster fixture indexer;
   this task is not "done end to end" until that scenario is green on kind.
### Task D1-6: `rpc.indexarr.download` and `rpc.indexarr.query`

**Why this task is different from D1-5.** D1-5 serves a verb whose response has no
`Error` field, so every failure has to be smuggled out as a named `SearchOutcome`.
These two verbs are the opposite: `DownloadResponse` and `QueryResponse` each carry
an `Error string` (`pkg/events/schema/index.go:272`, `:307`). That single field
changes the whole error contract — after a successful decode, **a failure is a
populated reply, never a transport error** — and it is the only asymmetry in the
three verbs. Get it wrong in the lenient direction and grabarr naks a dead tracker
link five times and DLQs it; get it wrong in the strict direction and the operator
sees "search failed" with no reason.

Two further things make `download` the most dangerous handler in D1:

1. **It proxies third-party bytes.** The reply body is a `.torrent` or `.nzb`
   produced by a host we do not control, arriving over a connection that carries
   our session cookie and, very often, a passkey in the query string.
2. **It is the only place indexarr counts grabs** (Ruling R3), which makes it the
   third writer under `k8s.ManagerIndexarrWorker` — a field manager whose ownership
   set server-side apply *replaces* rather than merges on every write.

`query` is small by comparison, and deliberately so: Ruling R4 says serve it against
the local index, exercise it from the e2e, and add no CRD field for it. It has zero
callers today. The temptation to make it clever — a fan-out fallback when the index
is cold, a `quality` filter the store cannot serve, an offset the store has no
concept of — is the temptation to ship an API nothing will ever exercise. Resist it.

---

#### Files

**Create**

| Path | Contents |
| --- | --- |
| `indexarr/download/doc.go` | Package doc: the `Error`-field contract, the worker-owned field set, the R3 limitation, the "never mutate the URL" rule. |
| `indexarr/download/redact.go` | `redactRawURL`, `redactURL`, `redactErr`, `truncate`, `scrub`. |
| `indexarr/download/payload.go` | `MaxPayloadBytes`, `ErrResponseTooLarge`, `readPayload`, `sniffKind`, `contentTypeFor`. Pure. |
| `indexarr/download/fetch.go` | `Fetcher`, `FetchResult`, `FetcherFor`, `NewFetcherFor`, `sameOrigin`, the `CheckRedirect` policy. |
| `indexarr/download/grabs.go` | `grabEntry`, `GrabRingKey`, `pruneRing`, `CountGrab`, `grabWindow`. |
| `indexarr/download/service.go` | `Service`, `Handle`, `resolveIndexer`, the single status apply, spans and metrics. |
| `indexarr/query/doc.go` | Package doc: local index only, the closed filter vocabulary, the cluster-wide carried item. |
| `indexarr/query/filters.go` | `filterKeys`, `buildQuery`, `DefaultLimit`, `MaxScanRows`, `MaxReplyBytes`. Pure. |
| `indexarr/query/service.go` | `Service`, `Handle`, `decodeRows`, the reply byte budget. |

**Create (tests)**

| Path | Contents |
| --- | --- |
| `indexarr/download/redact_test.go` | Query strings, userinfo, unparseable URLs, `*url.Error` unwrapping, secret scrubbing. |
| `indexarr/download/payload_test.go` | The broker-payload arithmetic, the cap, the three sniffs, the HTML interstitial. |
| `indexarr/download/fetch_test.go` | `httptest` servers: same-origin chain, cross-origin stop, magnet redirect, hop limit, cookie scoping, `Content-Length` short circuit. |
| `indexarr/download/grabs_test.go` | `membus` KV: first count, redelivery, distinct GUIDs, window prune, ring cap, CAS retry. |
| `indexarr/download/service_test.go` | The error-mapping table, the magnet short circuit, the missing-namespace refusal, metric label bounding. |
| `indexarr/download/suite_envtest_test.go` | envtest bootstrap, `newTestClient`, `newNamespace`. |
| `indexarr/download/status_envtest_test.go` | **The SSA release test**: steady state first, then a grab, then assert nothing else was released. |
| `indexarr/query/filters_test.go` | The closed vocabulary, every filter, hostile FTS5 text, clamping, negative inputs. |
| `indexarr/query/service_test.go` | Fake store: paging, `Total` semantics, undecodable row, byte budget, store error, no outbound calls. |

**Modify:** none. `indexarr/run.go` is wired by **D1-8**; `pkg/obs/metrics` is not
touched (see D11); `pkg/events/schema` is frozen.

#### Path ownership

- **Owns:** `indexarr/download/**` and `indexarr/query/**` — nothing else.
- **Must not touch:** `indexarr/search/**` (D1-5), `indexarr/status/**` (D1-0),
  `indexarr/controller/indexer/**` (D1-3), `indexarr/worker/rss/**` (D1-7),
  `indexarr/run.go` (D1-8), `pkg/relindex/**` (D1-2), `pkg/torznab/**` (D1-1),
  `go.mod`/`go.sum`, `api/**`, `pkg/events/**`, `pkg/obs/**`, `pkg/k8s/**`,
  `config/**`, `charts/**`.
- **Commit path-scoped:**
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "<msg>" -- indexarr/download indexarr/query`

> **Hard dependency, do not work around it.** Both handlers need
> `indexarr/status` (interface contract C5, Ruling R14). D1-0's written section
> does **not** list that package among its files — that is a recorded gap, flagged
> to the plan controller. If `indexarr/status` does not exist when this task
> starts, **stop and ask**; do not build a local copy of `WorkerFields`. Three
> copies of a "complete declaration" is the exact defect R14 exists to prevent.

#### Interfaces — Consumes

Verbatim. Everything here already exists or is pinned by another D1 section.
Nothing below is to be reimplemented.

```go
// ---- pkg/events/schema (SHIPPED AND FROZEN -- do not add, rename or retag) ----
type Ref struct {
    Namespace string `json:"namespace,omitempty"`
    Name      string `json:"name"`
    UID       string `json:"uid,omitempty"`
}

type DownloadRequest struct {
    IndexerRef Ref    `json:"indexerRef"`
    GUID       string `json:"guid"`
    URL        string `json:"url,omitempty"`
}
// "DownloadResponse carries exactly one of Bytes, MagnetURL or RedirectURL."
type DownloadResponse struct {
    Bytes       []byte `json:"bytes,omitempty"`
    MagnetURL   string `json:"magnetURL,omitempty"`
    RedirectURL string `json:"redirectURL,omitempty"`
    ContentType string `json:"contentType,omitempty"`
    Error       string `json:"error,omitempty"`
}

type QueryRequest struct {
    Text    string            `json:"text,omitempty"`
    Filters map[string]string `json:"filters,omitempty"`
    Limit   int32             `json:"limit,omitempty"`
    Offset  int32             `json:"offset,omitempty"`
}
type QueryResponse struct {
    Releases []Release `json:"releases,omitempty"`
    Total    int64     `json:"total,omitempty"`
    Error    string    `json:"error,omitempty"`
}

const MaxSearchReleases = 500
type Release struct { /* Info commonv1.ReleaseInfo, ParsedTitle, ... FetchedAt */ }

// ---- pkg/events (SHIPPED) ----
const RPCIndexDownload = "clustarr.rpc.indexarr.download" // subjects.go:157
const RPCIndexQuery    = "clustarr.rpc.indexarr.query"    // subjects.go:158
const BucketIndexerLimits = "clustarr-indexer-limits"     // subjects.go:98, TTL 2d

type KV interface {
    Get(ctx context.Context, key string) (Entry, error)
    Create(ctx context.Context, key string, val []byte, opts ...KVOption) (uint64, error)
    Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error)
    Put(ctx context.Context, key string, val []byte) (uint64, error)
    Delete(ctx context.Context, key string) error
    Watch(ctx context.Context, pattern string) (<-chan Entry, error)
}
type Entry struct { Bucket, Key string; Value []byte; Revision uint64; Created time.Time; Delta uint64; Operation KVOp }
var ErrKeyNotFound, ErrKeyExists, ErrRevisionMismatch error // errors.go:45-54

// KVKeyToken escapes s into [0-9A-Za-z-]. EVERY KV key goes through it.
func KVKeyToken(s string) string // kvkey.go:47

// ---- indexarr/status (D1-0, interface contract C5; Ruling R14) ----
func Patch(ctx context.Context, c client.Client, mgr k8s.FieldManager,
    idx *indexv1alpha1.Indexer, mutate func(*indexac.IndexerStatusApplyConfiguration)) error
func WorkerFields(st indexv1alpha1.IndexerStatus) *indexac.IndexerStatusApplyConfiguration

// ---- pkg/k8s (SHIPPED; D1-0 adds the constant) ----
const ManagerIndexarrWorker k8s.FieldManager = "indexarr-worker"

// ---- pkg/relindex (D1-2). ADR-0003 fixes this at FOUR methods. Do not widen it. ----
type Store interface {
    Upsert(ctx context.Context, rels []Release) (inserted int, err error)
    Search(ctx context.Context, q Query) ([]Release, error)
    Prune(ctx context.Context, olderThan time.Time) (deleted int, err error)
    Stats(ctx context.Context) (Stats, error)
}
type Query struct {
    Text       string // FTS5 MATCH; the STORE escapes it, the caller passes raw
    Indexers   []string
    Categories []int
    Protocol   string
    Since      *time.Time
    Limit      int // caller-supplied; the store never invents one
}
type Release struct {
    Indexer, GUID, Title, TitleNorm, Group, Protocol string
    Categories  []int
    SizeBytes   int64
    PublishedAt *time.Time
    FetchedAt   time.Time
    InfoJSON    []byte // the full schema.Release, for replay without re-query
}

// ---- pkg/ratelimit (SHIPPED) ----
func (l *Limiter) Wait(ctx context.Context, key string) error
func (l *Limiter) SetConfig(key string, cfg Config)

// ---- pkg/obs (SHIPPED) ----
func logging.FromContext(ctx context.Context) *slog.Logger
func tracing.Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span)
func tracing.RecordError(span trace.Span, err error)
var metrics.IndexerQueriesTotal   *prometheus.CounterVec   // labels: indexer, outcome
var metrics.IndexerQueryDuration  *prometheus.HistogramVec // labels: indexer, function

// ---- api/index/v1alpha1 (SHIPPED) ----
type Limits struct { QueryLimit, GrabLimit *int32; Unit LimitUnit } // Unit: "day"|"hour", default day
type IndexerStatus struct { /* ... */ GrabsInWindow int32; SessionSecretRef string /* ... */ }
```

#### Interfaces — Produces

```go
package download // indexarr/download

// MaxPayloadBytes bounds a proxied .torrent/.nzb body. It is NOT pkg/torznab's
// 8 MiB: this body is base64-encoded into a JSON reply and sent as one NATS
// message, and the broker's max_payload is 8Mi (config/nats/configmap.yaml:20).
// See D3 for the arithmetic.
const MaxPayloadBytes = 4 << 20 // 4 MiB

// ErrResponseTooLarge follows the pkg/torznab convention: a package-level max,
// an io.LimitReader(body, max+1) and a sentinel, so errors.Is works.
var ErrResponseTooLarge = errors.New("indexarr/download: response body exceeds size limit")

// FetchResult is one authenticated GET, with the redirect chain ALREADY
// classified by the fetcher. Exactly one of Body, MagnetURL and OffHostURL is
// set on a non-error result.
type FetchResult struct {
    Status      int
    Header      http.Header
    Body        io.ReadCloser // nil unless Status is 2xx; the caller closes it
    ContentLen  int64         // -1 when the server sent no Content-Length
    FinalURL    *url.URL      // the URL that produced this response
    MagnetURL   string        // a redirect pointed at a magnet: URI
    OffHostURL  string        // a redirect left the indexer's origin
}

// Fetcher performs one authenticated GET against one indexer.
type Fetcher interface {
    Fetch(ctx context.Context, rawURL string) (*FetchResult, error)
    // Scrub removes this indexer's secret VALUES from a diagnostic string. It
    // is the last line of defence before a message reaches DownloadResponse.Error
    // and, through grabarr, a Download's status condition.
    Scrub(s string) string
}

// FetcherFor builds the Fetcher for one Indexer. D1-8 supplies it; this package
// ships NewFetcherFor as the production implementation.
type FetcherFor func(ctx context.Context, idx *indexv1alpha1.Indexer) (Fetcher, error)

// NewFetcherFor reads spec.secretRef and status.sessionSecretRef, paces through
// the injected limiter keyed by the indexer HOST (one bucket per host, never per
// object -- D1-3 owns the limiter, this never constructs one), and applies
// spec.timeout.
func NewFetcherFor(c client.Client, lim *ratelimit.Limiter) FetcherFor

type Service struct {
    Client  client.Client // the manager's cached client; reads Indexer and Secret
    Bus     events.Bus    // for the grab ring in clustarr-indexer-limits
    Fetch   FetcherFor
    Now     func() time.Time // nil means time.Now
}

// Handle is the rpc.indexarr.download body. Its type is assignable to
// search.DownloadFn. It NEVER returns an error: after a successful decode every
// failure is a populated DownloadResponse.Error (see D9).
func (s *Service) Handle(ctx context.Context, req schema.DownloadRequest) schema.DownloadResponse

// GrabRingKey is "<KVKeyToken(uid)>.grab", the key spec §5's KV table names.
// Exported so D1-9's e2e can assert the ring without reimplementing the escape.
func GrabRingKey(uid string) string

// CountGrab records one grab of guid against idx, idempotently, and returns the
// number of grabs in the current window. A guid already in the ring is a
// redelivery and changes nothing.
func CountGrab(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, guid string, now time.Time) (count int32, counted bool, err error)
```

```go
package query // indexarr/query

const (
    DefaultLimit  = 100      // QueryRequest.Limit == 0
    MaxScanRows   = 2000     // the ceiling on limit+offset handed to the store
    MaxReplyBytes = 4 << 20  // the same broker-payload budget as download
)

type Service struct {
    Store relindex.Store   // nil answers with a populated Error
    Now   func() time.Time
}

// Handle is the rpc.indexarr.query body. Its type is assignable to
// search.QueryFn. It reads the LOCAL INDEX ONLY: no HTTP client, no bus, no
// fan-out, no Indexer lookup.
func (s *Service) Handle(ctx context.Context, req schema.QueryRequest) schema.QueryResponse
```

---

#### Design points, decided here so no step has to decide them

**D1. `download` never mutates the URL it was given.** The link came out of the
indexer's own feed and already embeds whatever credential that indexer signs with —
`apikey`, `passkey`, `rsskey`, a one-time token. Appending our own `apikey` can
duplicate a parameter, break an HMAC or produce a 403 that reads like an auth
failure. Credentials are applied **only** as cookies on a per-origin jar, plus
`spec.timeout`, the per-host limiter and the proxy dialler. One consequence worth
stating: `req.URL` empty is a hard `Error`. indexarr cannot resolve a GUID to a URL,
because `relindex.Query` has no GUID field and ADR-0003 fixes the store at four
methods — widening it is out of scope and is recorded as a carried item.

**D2. The redirect policy is part of the fetcher, and it is a closed classification.**
Go's default client follows ten redirects and **errors** on a `magnet:` target
("unsupported protocol scheme"), which is exactly the shape a torrent site uses.
`CheckRedirect` therefore returns `http.ErrUseLastResponse` — stop, hand back the
3xx — in three cases, and allows the hop otherwise:

| Next hop | Decision | Becomes |
| --- | --- | --- |
| `magnet:` | stop | `MagnetURL` |
| http(s), different origin | stop | `RedirectURL` (D4) |
| http(s), same origin, hop ≤ 5 | follow | eventually `Bytes` |
| http(s), same origin, hop > 5 | stop | `Error` "too many redirects" |
| any other scheme (`file:`, `data:`, `javascript:`) | stop | `Error`, scheme named, URL never echoed |

`sameOrigin` compares lowercased hostnames only (ports ignored) and accepts
`a == b`, `a` a dot-suffix of `b`, or `b` a dot-suffix of `a`, so `tracker.example`
→ `dl.tracker.example` is followed and `tracker.example` → `cdn.example.net` is not.

**D3. The size cap is 4 MiB, not pkg/torznab's 8 MiB, and the arithmetic is the
reason.** `Bytes []byte` marshals to base64 — `ceil(n/3)*4`, a 1.34× expansion —
and the whole `DownloadResponse` is one NATS message. The broker's
`max_payload: 8Mi` (`config/nats/configmap.yaml:20`) is the hard ceiling, so an
8 MiB body is unsendable and would fail *after* the fetch, having already consumed a
possibly one-shot link. 4 MiB encodes to ~5.34 MiB and leaves room for the envelope.
A step pins this with a test so the two numbers cannot drift apart.

The read follows the `pkg/torznab` convention **exactly** — a package-level max, an
`io.ReadAll(io.LimitReader(body, MaxPayloadBytes+1))`, a length check against the
max and an `ErrResponseTooLarge` sentinel wrapped with the byte count. It does **not**
follow `pkg/metadata/clients`, which has no cap at all; that is a recorded defect in
this repo, not a pattern.

**D4. `RedirectURL` is "indexarr will not proxy this", and it has exactly two
causes.** Both are *successes*, not errors, and both still count the grab:
1. the chain left the indexer's origin, where our session cookie would not be sent
   anyway and proxying buys nothing but memory;
2. `Content-Length` exceeds `MaxPayloadBytes` — checked **before** the body is read,
   so a one-shot link is not burned; when the server sends no `Content-Length` the
   overflow is discovered mid-read and becomes an `Error` instead, because by then
   the link may already be spent.

The URL handed back may carry a passkey. It is returned **intact** — grabarr needs
it — and logged **redacted**, always.

**D5. Grab counting is a CAS ring in KV, and the ring is what makes it idempotent.**
Spec §5's KV table pins `<indexer-uid>.grab` as a timestamp ring under CAS. Store
the GUIDs in the ring, not just timestamps, and idempotency falls out: a redelivered
`download` for a GUID already in the ring changes nothing and re-reports the same
count. The key is built from the **live object's** UID, never `req.IndexerRef.UID` —
the request's UID is caller-supplied and may be stale, and a stale UID would open a
second ring for the same indexer.

- Window: `spec.limits.Unit` (`hour` → 1 h, anything else → 24 h, matching the CRD
  default). A nil `spec.limits` still counts; the counter is observability whether or
  not a limit is configured.
- Prune on every read; cap at `maxRingEntries = 512` (newest kept) so one busy
  indexer cannot grow an unbounded KV value.
- CAS: `Get` → prune → append → `Update(key, val, rev)`; `ErrKeyNotFound` → `Create`;
  `ErrRevisionMismatch` or `ErrKeyExists` → retry, **3 attempts**, then give up.
- Counting happens **after** a successful fetch — bytes, magnet or redirect — never
  on a failure, so a broken indexer does not burn the operator's grab budget.
- A KV failure is **logged and dropped**, never fatal. An accounting outage must not
  strand a grab that already has its bytes.

**Known limitation, and it goes in the package doc verbatim (Ruling R3):** a grab
whose `DownloadSource` is `torrentURL`, `magnetURL` or `nzbURL` never calls this verb
(`api/download/v1alpha1/download_types.go:235`), so those grabs go uncounted and
`grabsInWindow` **undercounts for public indexers**. Acceptable for M2 — those grabs
use no indexer credentials — but it means grab-limit *enforcement* cannot be built on
this counter alone. Carried item.

**D6. The status write declares the complete `indexarr-worker` set, through
`indexarr/status` and nothing else.** This verb changes exactly one field,
`grabsInWindow`, and must still send all ten the manager owns:

```
escalationLevel  disabledUntil  initialFailureAt  lastFailureAt  lastFailure
queriesInWindow  grabsInWindow  lastRssAt  lastRssNewCount  indexedReleases
```

`status.WorkerFields(idx.Status)` builds that set (R14); this package calls it and
never hand-rolls one. The apply is **skipped entirely** when the count did not change
— an apply that does not happen releases nothing, which is the one safe shortcut.

`status.grabsInWindow` is a *projection* of the KV ring; the ring is the source of
truth. That matters because D1-5's fan-out and D1-7's RSS worker also apply under
this manager from their own possibly-stale cached read, so a lost update is possible.
It self-heals on the next grab. Do not add a CAS loop for a status field.

**D7. Sessions and passkeys must not leak, and there are four separate places they
can.** `pkg/cardigann`'s `redactURL`/`redactRawURL`/`redactErr` (`engine.go:145`,
`:161`, `:176`) are the shape to copy — they are **unexported**, so this package
writes its own; a shared `pkg/redact` is a carried item, not this task's job.

1. **Logs.** Never log a raw URL. `redactRawURL` drops everything from the first `?`
   and strips userinfo, and also handles a URL `url.Parse` rejects.
2. **Errors.** `redactErr` unwraps `*url.Error`, which carries the full URL including
   the query in its own `Error()` string — and unwrapping keeps
   `errors.Is(err, context.Canceled)` working.
3. **`DownloadResponse.Error`.** grabarr puts this on a `Download`'s status, where a
   human reads it. Every message goes through `Fetcher.Scrub` (replaces the
   indexer's known secret values, for values of 8+ characters, with `***`) and is
   truncated to `maxErrorChars = 512`.
4. **`req.GUID` and spans.** A GUID is very often the release's details URL, passkey
   included. It is redacted like a URL and truncated before it reaches a log line or
   a span attribute, and it never reaches a metric label at all.

**D8. `query` reads the local index only, and the FTS5 text is attacker-controlled.**
No HTTP client is constructed, no `Indexer` is read, no bus call is made. `Text` is
passed to `relindex.Query.Text` **raw and unmodified**: D1-2 owns FTS5 escaping, and
two escapers compose into a query that matches nothing. A hostile string is therefore
a normal, probably empty, result — not an error, not a panic. Should the store still
return a syntax error, it becomes a populated `Error`.

`Filters` is a **closed vocabulary**: `protocol`, `indexer`, `category`, `since`.
An unknown key is an **error**, not a silent no-op — silently dropping a filter
returns *more* data than the caller asked for and the caller cannot tell. The key is
truncated to 64 characters before it is quoted into the message, and the keys are
walked in sorted order so two bad filters produce a deterministic message.
`schema.QueryRequest`'s own doc names `quality` as an example filter;
`relindex.Query` has no quality column, so `quality` is rejected **by name** with a
message saying so, and the gap is a carried item.

`Offset` has no counterpart in `relindex.Query`, and ADR-0003 forbids widening the
Store. It is applied in process: ask for `limit+offset` rows (capped at
`MaxScanRows`), slice off the offset. Paging is only meaningful if the store orders
deterministically — that is D1-2's property, not something this package can fix.
`Total` is set to the row count the store returned **before** slicing, and is
documented in code as a **lower bound**: a four-method Store has no `COUNT`.

**D9. The `Error` field, precisely.** One rule: **a successful decode always produces
a reply.** A transport error means exactly one thing to the caller — the request never
reached a handler, or could not be parsed.

| Situation | `download` | What the caller sees |
| --- | --- | --- |
| Malformed JSON | **transport error** | `Request` returns an error. Handled in D1-5's `handleDownload`, not here. |
| `indexerRef.name` or `indexerRef.namespace` empty | `Error` | "indexerRef.namespace and indexerRef.name are required" |
| `guid` empty | `Error` | "guid is required" |
| `url` empty | `Error` | "release has no download URL; indexarr cannot resolve a guid without one" |
| Indexer not found | `Error` | names namespace/name (both are bounded CR identifiers) |
| `spec.enabled == false` | `Error` | "indexer is disabled" |
| `status.disabledUntil` in the future | **proceeds** | escalation is *health*; the release was already selected and refusing strands an approved grab. Download failures do **not** escalate (carried item). |
| Transport failure, timeout, TLS | `Error` | redacted, scrubbed, truncated |
| HTTP 4xx/5xx | `Error` | "indexer returned HTTP 403" — status only, never the body |
| Body is an HTML page | `Error` | "indexer returned an HTML page, not a download payload (session expired?)" |
| Body over the cap, no `Content-Length` | `Error` wrapping `ErrResponseTooLarge` | byte count, no URL |
| Empty 200 body | `Error` | "indexer returned an empty body" |
| Grab counting failed | **success** | bytes still returned; the failure is a log line and a metric |
| `Fetch` is nil (misconfiguration) | `Error` | "download verb is not configured" |

`query` follows the same rule: decode failure → transport error (D1-5's handler);
negative `limit`/`offset`, unknown filter, unparseable `since`, non-integer category,
unknown protocol, nil `Store`, store failure → populated `Error`. An empty result set
is a **success with no releases**, never an error.

**D10. Spans.** D1-5's `Serve` already opens `indexarr.rpc.serve.download` and
`.query`; these are children. `indexarr.download` wraps `Handle`, with
`indexarr.download.fetch` and `indexarr.download.count_grab` inside it;
`indexarr.query` wraps its `Handle` with `indexarr.query.store` inside. Attributes
are `indexer.namespace`, `indexer.name`, `download.result`, `download.bytes`,
`query.limit`, `query.offset`, `query.rows`, `query.text_len` — **the query text
itself is never an attribute**: it is attacker-controlled and unbounded.

**D11. Metrics reuse `pkg/obs/metrics`, and never carry an indexer-supplied string.**
`newCounterVec` is unexported and `pkg/obs/**` belongs to another task's path, so
this task adds no metric. It uses:

- `IndexerQueriesTotal{indexer, outcome}` — `indexer` is the Kubernetes object name,
  bounded by the number of `Indexer` CRs. When the object could **not** be resolved
  the label is the constant `"_unknown"`, because the request's name is
  caller-supplied and would be unbounded cardinality.
- `IndexerQueryDuration{indexer, function}` — `function` is `"download"` or
  `"query"`; the query verb's `indexer` label is the constant `"_local-index"`.
- `outcome` is a **closed set**: `ok`, `magnet`, `redirect`, `no_url`, `bad_request`,
  `not_found`, `disabled`, `unauthorized`, `http_error`, `transport_error`,
  `too_large`, `invalid_payload`, `not_configured`, `grab_counted`,
  `grab_duplicate`, `grab_count_failed`. A `Content-Type`, a tracker error string or
  a GUID must never reach a label.

Carried item: a dedicated `clustarr_indexer_grabs_total` belongs in
`pkg/obs/metrics`; adding it is an edit to a path this task does not own.

**D12. Multi-tenancy.** `DownloadRequest.IndexerRef` *has* a `Namespace`, and an
empty one is **refused** rather than resolved cluster-wide. Unlike `SearchRequest`
(Ruling R10) this verb has **zero callers today** — grabarr's resolution is Task D2 —
so refusing closes the hole by construction at no compatibility cost, and the
`IndexerDownload` CRD type already says the indexer is "in the same namespace as the
Download".

`query` is the opposite and cannot be fixed here: `QueryRequest` carries no
namespace, `relindex.Release` has no namespace column, and the index is one SQLite
file for the whole process. The verb is therefore **inherently cluster-wide**, and
its results include `Info.DownloadURL`, which may embed a passkey. It is not stripped
— the download verb cannot resolve a GUID without a URL (D1), so stripping would
break the future path it exists to serve. Instead: the package doc states the
exposure, the URLs are never logged, and **"`query` must take a namespace and the
index must gain a namespace column before this verb gets a real caller"** is recorded
as a carried item for M6. Do not invent the field now (R4/R12).

---

#### Steps

**Package skeleton and redaction — before anything can produce a message**

- [ ] 1. Create `indexarr/download/doc.go`: the GPL-3.0 header from
  `hack/boilerplate.go.txt`, then `package download` with a doc comment stating
  (a) the payload types are frozen in `pkg/events/schema/index.go`; (b) the
  `Error`-field rule from D9 in one sentence — *"after a successful decode every
  failure is a populated DownloadResponse.Error; a transport error means the request
  never reached a handler"*; (c) the ten `Indexer.status` fields owned by
  `k8s.ManagerIndexarrWorker`, listed verbatim from D6; (d) the R3 limitation
  verbatim:

  ```go
  // Grab accounting, and what it misses. Ruling R3 puts grab counting here --
  // indexarr already holds the indexer's session and passkey at this point, and
  // no new CLUSTARR_EVENTS consumer is needed. But a grab whose DownloadSource
  // is torrentURL, magnetURL or nzbURL never calls this verb at all
  // (api/download/v1alpha1/download_types.go:235), so those grabs go uncounted
  // and status.grabsInWindow UNDERCOUNTS for public indexers. That is acceptable
  // for M2 -- those grabs use no indexer credentials -- but grab-limit
  // ENFORCEMENT cannot be built on this counter alone.
  ```

  Run `go build ./indexarr/...`. It compiles.

- [ ] 2. Create `indexarr/download/redact_test.go` (GPL header, package `download`)
  with the URL cases only:

  ```go
  func TestRedactRawURLDropsEverythingSecret(t *testing.T) {
      for _, tc := range []struct{ name, in, want string }{
          {"passkey in query", "https://tr.example/dl?id=7&passkey=deadbeef", "https://tr.example/dl"},
          {"apikey in query", "https://nzb.example/api?t=get&apikey=abc123", "https://nzb.example/api"},
          {"userinfo", "https://user:hunter2@tr.example/dl", "https://tr.example/dl"},
          {"fragment", "https://tr.example/dl#frag", "https://tr.example/dl"},
          {"unparseable keeps the prefix", "ht tp://x/y?passkey=s3cret", "ht tp://x/y"},
          {"no query is unchanged", "https://tr.example/dl", "https://tr.example/dl"},
          {"empty", "", ""},
      } {
          t.Run(tc.name, func(t *testing.T) {
              require.Equal(t, tc.want, redactRawURL(tc.in))
              require.NotContains(t, redactRawURL(tc.in), "passkey")
              require.NotContains(t, redactRawURL(tc.in), "hunter2")
          })
      }
  }
  ```

- [ ] 3. Run `go test ./indexarr/download/...`. Expect `undefined: redactRawURL`.

- [ ] 4. Create `indexarr/download/redact.go` (GPL header) with `redactURL` and
  `redactRawURL`, following `pkg/cardigann/engine.go:145-170`. The doc comment must
  say **why**, not just what:

  ```go
  // redactURL renders u for a diagnostic -- a log line, an error, a span
  // attribute -- with everything secret stripped: query string, fragment and
  // userinfo. Scheme, host and path survive, which is what keeps the message
  // diagnosable.
  //
  // Indexer download links put passkey, apikey and rsskey in the query string.
  // These messages reach DownloadResponse.Error, which grabarr writes onto a
  // Download's status condition, which a human then reads off a terminal.
  // pkg/cardigann makes the same choice for the same reason (engine.go:145);
  // its helpers are unexported, so this is a deliberate copy, not an oversight.
  func redactURL(u *url.URL) string {
      if u == nil {
          return ""
      }
      safe := *u
      safe.RawQuery, safe.ForceQuery = "", false
      safe.Fragment, safe.RawFragment = "", ""
      safe.User = nil
      return safe.String()
  }

  // redactRawURL is redactURL for a URL still in string form, including one
  // url.Parse rejects: everything from the first "?" on is dropped, so an
  // unparseable URL cannot leak its query either.
  func redactRawURL(raw string) string {
      if raw == "" {
          return ""
      }
      if u, err := url.Parse(raw); err == nil {
          return redactURL(u)
      }
      if i := strings.IndexByte(raw, '?'); i >= 0 {
          return raw[:i]
      }
      return raw
  }
  ```

- [ ] 5. Run `go test ./indexarr/download/...`. Green.

- [ ] 6. Append to `redact_test.go` the error and scrubbing cases:

  ```go
  func TestRedactErrKeepsTheCauseAndDropsTheURL(t *testing.T) {
      ue := &url.Error{Op: "Get", URL: "https://tr.example/dl?passkey=s3cret", Err: context.Canceled}
      got := redactErr(ue)
      require.NotContains(t, got.Error(), "s3cret")
      require.ErrorIs(t, got, context.Canceled, "errors.Is must still see through it")
      plain := errors.New("boom")
      require.Equal(t, plain, redactErr(plain))
      require.Nil(t, redactErr(nil))
  }

  func TestScrubReplacesSecretValuesButNotShortOnes(t *testing.T) {
      s := scrubber([]string{"deadbeefcafe", "abc", ""})
      require.Equal(t, "auth failed for ***", s("auth failed for deadbeefcafe"))
      // "abc" is too short to scrub: it would redact ordinary prose.
      require.Equal(t, "abc problem", s("abc problem"))
      require.Equal(t, "", s(""))
  }

  func TestTruncateBoundsAnAttackerControlledString(t *testing.T) {
      require.Equal(t, "abc", truncate("abc", 8))
      require.Equal(t, "abcde...", truncate("abcdefghij", 8))
      require.Len(t, truncate(strings.Repeat("x", 10_000), maxErrorChars), maxErrorChars)
  }
  ```

- [ ] 7. Run it; expect three `undefined` failures. Add to `redact.go`:

  ```go
  // maxErrorChars bounds DownloadResponse.Error and QueryResponse.Error. Both
  // land in another controller's status, and an unbounded status string is how
  // operators melt etcd.
  const maxErrorChars = 512

  // minScrubLen is the shortest secret value worth replacing. A two-character
  // "password" would redact ordinary English out of every message.
  const minScrubLen = 8

  // redactErr strips the URL net/url puts in *url.Error's own message while
  // keeping the underlying cause, so errors.Is still finds context.Canceled,
  // syscall errors and the rest through it.
  func redactErr(err error) error {
      var ue *url.Error
      if errors.As(err, &ue) && ue.Err != nil {
          return ue.Err
      }
      return err
  }

  // scrubber returns a function that replaces each secret value in a
  // diagnostic with "***". It is the last line of defence: the layers above
  // already avoid putting a secret in a message, and this catches the case
  // where a third party echoed one back at us.
  func scrubber(secrets []string) func(string) string {
      var pairs []string
      for _, s := range secrets {
          if len(s) >= minScrubLen {
              pairs = append(pairs, s, "***")
          }
      }
      if len(pairs) == 0 {
          return func(s string) string { return s }
      }
      r := strings.NewReplacer(pairs...)
      return r.Replace
  }

  func truncate(s string, max int) string {
      if len(s) <= max {
          return s
      }
      return s[:max-3] + "..."
  }
  ```

- [ ] 8. Run `go test ./indexarr/download/...`; green. Commit:
  `feat(indexarr): redaction helpers for the download verb`.

**The payload cap — the number that must match the broker**

- [ ] 9. Create `indexarr/download/payload_test.go` with the arithmetic pin first.
  This is the test that stops someone "harmonising" the cap with `pkg/torznab`:

  ```go
  // TestMaxPayloadFitsOneNATSMessage is the reason this cap is 4 MiB and not
  // pkg/torznab's 8 MiB. DownloadResponse.Bytes is base64 in JSON and the whole
  // reply is ONE NATS message; the broker's max_payload is 8Mi
  // (config/nats/configmap.yaml:20). An 8 MiB body would fail AFTER the fetch,
  // having already spent a possibly one-shot download link.
  func TestMaxPayloadFitsOneNATSMessage(t *testing.T) {
      const brokerMaxPayload = 8 << 20
      encoded := base64.StdEncoding.EncodedLen(MaxPayloadBytes)
      const envelopeOverhead = 4 << 10
      require.Less(t, encoded+envelopeOverhead, brokerMaxPayload,
          "a full-size body must marshal into one NATS message")
      require.Greater(t, MaxPayloadBytes, 1<<20, "1 MiB would reject real .nzb files")
  }
  ```

- [ ] 10. Run `go test ./indexarr/download/...`. Expect `undefined: MaxPayloadBytes`.

- [ ] 11. Create `indexarr/download/payload.go` (GPL header) with the constants and
  the sentinel, doc comments carrying the arithmetic:

  ```go
  // MaxPayloadBytes bounds a proxied .torrent/.nzb body. See
  // TestMaxPayloadFitsOneNATSMessage for the arithmetic: base64 expands a body
  // by 4/3, the whole DownloadResponse is one NATS message, and the broker's
  // max_payload is 8Mi. 4 MiB encodes to about 5.34 MiB and leaves room for the
  // envelope, while sitting far above any real .torrent and above all but
  // pathological .nzb files.
  //
  // This is deliberately NOT pkg/torznab.maxResponseBodyBytes (8 MiB): that cap
  // bounds an XML document that is parsed and discarded, this one bounds bytes
  // that must survive a round trip through the broker.
  const MaxPayloadBytes = 4 << 20

  // ErrResponseTooLarge follows the pkg/torznab convention (client.go:164): a
  // package-level max, an io.LimitReader(body, max+1) and a sentinel, so
  // errors.Is works through the wrapping. pkg/metadata/clients reads bodies with
  // no cap at all; that is a recorded defect, not a second convention.
  var ErrResponseTooLarge = errors.New("indexarr/download: response body exceeds size limit")
  ```

- [ ] 12. Run `go test ./indexarr/download/...`. Green.

- [ ] 13. Append the cap test to `payload_test.go`:

  ```go
  func TestReadPayloadCapsTheBody(t *testing.T) {
      body := bytes.Repeat([]byte("d"), MaxPayloadBytes+1)
      _, err := readPayload(io.NopCloser(bytes.NewReader(body)))
      require.ErrorIs(t, err, ErrResponseTooLarge)
      require.NotContains(t, err.Error(), "http", "the cap error must never carry a URL")
  }

  func TestReadPayloadAcceptsExactlyTheLimit(t *testing.T) {
      body := bytes.Repeat([]byte("d"), MaxPayloadBytes)
      got, err := readPayload(io.NopCloser(bytes.NewReader(body)))
      require.NoError(t, err)
      require.Len(t, got, MaxPayloadBytes)
  }

  func TestReadPayloadRejectsAnEmptyBody(t *testing.T) {
      _, err := readPayload(io.NopCloser(bytes.NewReader(nil)))
      require.ErrorIs(t, err, errEmptyBody)
  }
  ```

- [ ] 14. Run it; expect `undefined: readPayload`. Add to `payload.go`:

  ```go
  var errEmptyBody = errors.New("indexarr/download: indexer returned an empty body")

  // readPayload reads r through the cap. One byte past the limit is read so a
  // body exactly at the limit is accepted while anything larger is detected
  // without ever buffering more than MaxPayloadBytes+1.
  func readPayload(r io.Reader) ([]byte, error) {
      b, err := io.ReadAll(io.LimitReader(r, MaxPayloadBytes+1))
      if err != nil {
          return nil, err
      }
      if len(b) > MaxPayloadBytes {
          return nil, fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(b))
      }
      if len(b) == 0 {
          return nil, errEmptyBody
      }
      return b, nil
  }
  ```

- [ ] 15. Run `go test ./indexarr/download/...`. Green.

- [ ] 16. Append the sniff test. The HTML case is the valuable one: a tracker whose
  session expired answers a download link with a login page and HTTP 200, and without
  this check grabarr would add a login page to a torrent client.

  ```go
  func TestSniffKind(t *testing.T) {
      for _, tc := range []struct {
          name string
          body []byte
          want payloadKind
      }{
          {"bencode dict", []byte("d8:announce30:http://tr.example/announceee"), kindTorrent},
          {"nzb with prolog", []byte("<?xml version=\"1.0\"?>\n<nzb xmlns=\"...\">"), kindNZB},
          {"nzb without prolog", []byte("<nzb><file/></nzb>"), kindNZB},
          {"leading whitespace is skipped", []byte("\n\n  <?xml version=\"1.0\"?><nzb/>"), kindNZB},
          {"html doctype", []byte("<!DOCTYPE html><html><body>login"), kindHTML},
          {"html tag", []byte("<HTML><head>"), kindHTML},
          {"garbage", []byte{0x00, 0x01, 0x02}, kindUnknown},
      } {
          t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, sniffKind(tc.body)) })
      }
  }

  func TestContentTypeForPrefersTheSniffOverAWrongHeader(t *testing.T) {
      // Real trackers serve .torrent as text/html all the time.
      require.Equal(t, "application/x-bittorrent", contentTypeFor("text/html; charset=utf-8", kindTorrent))
      require.Equal(t, "application/x-nzb", contentTypeFor("", kindNZB))
      // An unrecognised body falls back to the header, parsed and stripped of
      // parameters -- never echoed raw.
      require.Equal(t, "application/octet-stream", contentTypeFor("application/octet-stream; x=1", kindUnknown))
      require.Equal(t, "application/octet-stream", contentTypeFor("!! not a media type", kindUnknown))
      require.LessOrEqual(t, len(contentTypeFor(strings.Repeat("a/b;", 500), kindUnknown)), maxContentTypeChars)
  }
  ```

- [ ] 17. Run it; expect failures. Add to `payload.go`:

  ```go
  type payloadKind int

  const (
      kindUnknown payloadKind = iota
      kindTorrent
      kindNZB
      kindHTML
  )

  // maxContentTypeChars bounds a header value from a third party before it is
  // copied into DownloadResponse.ContentType.
  const maxContentTypeChars = 128

  // sniffKind classifies a body by its first bytes. The HTML case is the one
  // that earns this function: a tracker whose session has expired answers a
  // download link with a login page and HTTP 200, and without this check the
  // login page reaches a torrent client as a "torrent".
  func sniffKind(b []byte) payloadKind {
      t := bytes.TrimLeft(b, " \t\r\n")
      switch {
      case len(t) == 0:
          return kindUnknown
      case t[0] == 'd': // bencode dictionary
          return kindTorrent
      case hasPrefixFold(t, []byte("<!doctype html")), hasPrefixFold(t, []byte("<html")):
          return kindHTML
      case hasPrefixFold(t, []byte("<?xml")), hasPrefixFold(t, []byte("<nzb")):
          return kindNZB
      default:
          return kindUnknown
      }
  }

  func hasPrefixFold(b, prefix []byte) bool {
      return len(b) >= len(prefix) && bytes.EqualFold(b[:len(prefix)], prefix)
  }

  // contentTypeFor prefers what the bytes ARE over what the indexer claimed.
  // An unrecognised body falls back to the header, parsed by mime.ParseMediaType
  // so parameters are dropped, and bounded -- the header is a third-party
  // string on its way into another controller's object.
  func contentTypeFor(header string, k payloadKind) string {
      switch k {
      case kindTorrent:
          return "application/x-bittorrent"
      case kindNZB:
          return "application/x-nzb"
      }
      if mt, _, err := mime.ParseMediaType(header); err == nil && mt != "" {
          return truncate(mt, maxContentTypeChars)
      }
      return "application/octet-stream"
  }
  ```

- [ ] 18. Run `go test ./indexarr/download/...`; green. Run
  `golangci-lint-v2 run ./indexarr/download/...`. Commit:
  `feat(indexarr): capped, sniffed download payload handling`.

**The fetcher and the redirect policy**

- [ ] 19. Create `indexarr/download/fetch_test.go` (GPL header, package `download`)
  with the origin table:

  ```go
  func TestSameOrigin(t *testing.T) {
      for _, tc := range []struct{ base, target string; want bool }{
          {"tr.example", "tr.example", true},
          {"tr.example", "TR.Example", true},
          {"tr.example", "dl.tr.example", true},   // a download subdomain
          {"dl.tr.example", "tr.example", true},   // and back
          {"tr.example", "tr.example:8443", true}, // ports are ignored
          {"tr.example", "cdn.example.net", false},
          {"tr.example", "eviltr.example", false}, // NOT a dot-suffix
          {"tr.example", "", false},
      } {
          t.Run(tc.base+"->"+tc.target, func(t *testing.T) {
              require.Equal(t, tc.want, sameOrigin(tc.base, tc.target))
          })
      }
  }
  ```

  Note `eviltr.example`: the suffix check must be on `"."+host`, never a bare
  `strings.HasSuffix`, or a look-alike domain harvests the session cookie.

- [ ] 20. Run it; expect `undefined: sameOrigin`. Create
  `indexarr/download/fetch.go` (GPL header) with:

  ```go
  // sameOrigin reports whether target may be reached with this indexer's
  // session. Hostnames only, case-insensitive, ports ignored; a dot-suffix in
  // either direction counts, so tr.example -> dl.tr.example is followed. The
  // "."+host is load-bearing: a bare suffix test would accept eviltr.example.
  func sameOrigin(base, target string) bool {
      b, t := strings.ToLower(hostOnly(base)), strings.ToLower(hostOnly(target))
      if b == "" || t == "" {
          return false
      }
      return b == t || strings.HasSuffix(t, "."+b) || strings.HasSuffix(b, "."+t)
  }

  func hostOnly(h string) string {
      if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h[i:], "]") {
          return h[:i]
      }
      return h
  }
  ```

- [ ] 21. Run `go test ./indexarr/download/...`. Green.

- [ ] 22. Append the redirect-classification test, driven through real `httptest`
  servers so the policy is proved against Go's actual client:

  ```go
  func TestFetchFollowsSameOriginAndStopsElsewhere(t *testing.T) {
      var srv *httptest.Server
      srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          switch r.URL.Path {
          case "/hop":
              http.Redirect(w, r, srv.URL+"/file", http.StatusFound)
          case "/file":
              w.Header().Set("Content-Type", "application/x-bittorrent")
              _, _ = w.Write([]byte("d8:announce5:hello e"))
          case "/magnet":
              http.Redirect(w, r, "magnet:?xt=urn:btih:abc", http.StatusFound)
          case "/away":
              http.Redirect(w, r, "https://cdn.elsewhere.invalid/x?passkey=s3cret", http.StatusFound)
          case "/evil":
              http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
          case "/loop":
              http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
          }
      }))
      t.Cleanup(srv.Close)
      f := testFetcher(t, srv.URL)

      t.Run("same origin is followed", func(t *testing.T) {
          res, err := f.Fetch(context.Background(), srv.URL+"/hop")
          require.NoError(t, err)
          t.Cleanup(func() { _ = res.Body.Close() })
          require.Equal(t, http.StatusOK, res.Status)
          b, _ := readPayload(res.Body)
          require.Equal(t, "d8:announce5:hello e", string(b))
      })
      t.Run("magnet redirect becomes MagnetURL", func(t *testing.T) {
          res, err := f.Fetch(context.Background(), srv.URL+"/magnet")
          require.NoError(t, err)
          require.Equal(t, "magnet:?xt=urn:btih:abc", res.MagnetURL)
          require.Nil(t, res.Body)
      })
      t.Run("cross origin becomes OffHostURL, intact", func(t *testing.T) {
          res, err := f.Fetch(context.Background(), srv.URL+"/away")
          require.NoError(t, err)
          require.Equal(t, "https://cdn.elsewhere.invalid/x?passkey=s3cret", res.OffHostURL,
              "grabarr needs the URL intact; only the LOG is redacted")
          require.Nil(t, res.Body)
      })
      t.Run("a non-http scheme is refused by name", func(t *testing.T) {
          _, err := f.Fetch(context.Background(), srv.URL+"/evil")
          require.ErrorContains(t, err, "file")
          require.NotContains(t, err.Error(), "/etc/passwd")
      })
      t.Run("a redirect loop stops at the hop limit", func(t *testing.T) {
          _, err := f.Fetch(context.Background(), srv.URL+"/loop")
          require.ErrorIs(t, err, errTooManyRedirects)
      })
  }
  ```

- [ ] 23. Run it; expect `undefined`. Add the fetcher core to `fetch.go`:

  ```go
  const maxRedirects = 5

  var errTooManyRedirects = errors.New("indexarr/download: too many redirects")

  type FetchResult struct {
      Status     int
      Header     http.Header
      Body       io.ReadCloser
      ContentLen int64
      FinalURL   *url.URL
      MagnetURL  string
      OffHostURL string
  }

  type Fetcher interface {
      Fetch(ctx context.Context, rawURL string) (*FetchResult, error)
      Scrub(s string) string
  }

  type fetcher struct {
      hc      *http.Client
      base    string // the indexer host, for the same-origin test
      limiter *ratelimit.Limiter
      key     string
      scrub   func(string) string
  }

  func (f *fetcher) Scrub(s string) string { return f.scrub(s) }

  // checkRedirect classifies the next hop instead of blindly following it. Go's
  // default policy follows ten hops and ERRORS on a magnet: target with
  // "unsupported protocol scheme" -- which is exactly the shape a torrent site
  // uses, so the default would turn the most common success into a failure.
  //
  // Returning http.ErrUseLastResponse hands the 3xx back to Fetch, which then
  // reads Location. Cross-origin hops stop because our session cookie would not
  // be sent there anyway (the jar is per-origin), so proxying buys nothing but
  // memory -- and because a look-alike host must never see the cookie.
  func (f *fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
      if len(via) > maxRedirects {
          return errTooManyRedirects
      }
      switch req.URL.Scheme {
      case "http", "https":
          if !sameOrigin(f.base, req.URL.Host) {
              return http.ErrUseLastResponse
          }
          return nil
      case "magnet":
          return http.ErrUseLastResponse
      default:
          // The scheme is named; the URL is not, because a data: URL can carry
          // anything and a file: URL names a path.
          return fmt.Errorf("indexarr/download: refusing to follow a %q redirect", req.URL.Scheme)
      }
  }
  ```

- [ ] 24. Add `Fetch` itself to `fetch.go`. The magnet short circuit comes first, so
  a magnet link never touches the network:

  ```go
  func (f *fetcher) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
      ctx, span := tracing.Start(ctx, "indexarr.download.fetch")
      defer span.End()

      u, err := url.Parse(rawURL)
      if err != nil {
          return nil, fmt.Errorf("indexarr/download: parse download URL: %w", redactErr(err))
      }
      // A magnet link is already the payload. Never fetch it.
      if u.Scheme == "magnet" {
          return &FetchResult{MagnetURL: rawURL, FinalURL: u}, nil
      }
      if u.Scheme != "http" && u.Scheme != "https" {
          return nil, fmt.Errorf("indexarr/download: refusing a %q download URL", u.Scheme)
      }

      // The caller owns rate limiting (CLAUDE.md); this waits on the limiter
      // D1-3 built and keyed by HOST, so two Indexers pointing at one tracker
      // share one bucket.
      if f.limiter != nil {
          if err := f.limiter.Wait(ctx, f.key); err != nil {
              return nil, err
          }
      }

      logging.FromContext(ctx).Debug("indexarr/download: fetching", "url", redactURL(u))
      req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
      if err != nil {
          return nil, redactErr(err)
      }
      resp, err := f.hc.Do(req)
      if err != nil {
          tracing.RecordError(span, err)
          return nil, fmt.Errorf("indexarr/download: get %s: %w", redactURL(u), redactErr(err))
      }

      res := &FetchResult{
          Status: resp.StatusCode, Header: resp.Header,
          ContentLen: resp.ContentLength, FinalURL: resp.Request.URL,
      }
      if resp.StatusCode >= 300 && resp.StatusCode < 400 {
          defer func() { _ = resp.Body.Close() }()
          loc, lerr := resp.Location()
          if lerr != nil {
              return nil, fmt.Errorf("indexarr/download: %d with no usable Location: %w", resp.StatusCode, redactErr(lerr))
          }
          if loc.Scheme == "magnet" {
              res.MagnetURL = loc.String()
          } else {
              res.OffHostURL = loc.String()
          }
          return res, nil
      }
      res.Body = resp.Body
      return res, nil
  }
  ```

- [ ] 25. Add the test helper `testFetcher` to `fetch_test.go` and run the suite:

  ```go
  func testFetcher(t *testing.T, base string) *fetcher {
      t.Helper()
      u, err := url.Parse(base)
      require.NoError(t, err)
      f := &fetcher{base: u.Host, scrub: func(s string) string { return s }}
      f.hc = &http.Client{Timeout: 5 * time.Second, CheckRedirect: f.checkRedirect}
      return f
  }
  ```

  `go test ./indexarr/download/...` — the five subtests pass.

- [ ] 26. Append the `Content-Length` short-circuit test. The point is that a link
  which can only be spent once must not be spent on a body we will refuse:

  ```go
  func TestFetchReportsAnOversizeContentLengthWithoutReadingTheBody(t *testing.T) {
      var reads atomic.Int32
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          w.Header().Set("Content-Length", strconv.Itoa(MaxPayloadBytes+1))
          w.WriteHeader(http.StatusOK)
          reads.Add(1)
          _, _ = w.Write(bytes.Repeat([]byte("x"), MaxPayloadBytes+1))
      }))
      t.Cleanup(srv.Close)
      res, err := testFetcher(t, srv.URL).Fetch(context.Background(), srv.URL+"/big")
      require.NoError(t, err)
      t.Cleanup(func() { _ = res.Body.Close() })
      require.Equal(t, int64(MaxPayloadBytes+1), res.ContentLen,
          "the handler decides on ContentLen alone; the body is never read")
  }
  ```

  Run it; it passes with the code from step 24 (`ContentLen` is already carried).
  The *decision* lives in `service.go`, step 44.

- [ ] 27. Append the cookie-scoping test — the one that proves a session cannot walk
  off-host:

  ```go
  func TestSessionCookieIsScopedToTheIndexerOrigin(t *testing.T) {
      var gotCookie string
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          gotCookie = r.Header.Get("Cookie")
          _, _ = w.Write([]byte("d1:xe"))
      }))
      t.Cleanup(srv.Close)
      u, _ := url.Parse(srv.URL)

      jar, err := cookiejar.New(nil)
      require.NoError(t, err)
      seedCookies(jar, u, "sess=abc123; other=zz")
      f := testFetcher(t, srv.URL)
      f.hc.Jar = jar

      res, err := f.Fetch(context.Background(), srv.URL+"/dl")
      require.NoError(t, err)
      _ = res.Body.Close()
      require.Contains(t, gotCookie, "sess=abc123")

      // A different origin must see nothing.
      other, _ := url.Parse("https://cdn.elsewhere.invalid/")
      require.Empty(t, jar.Cookies(other))
  }
  ```

- [ ] 28. Run it; expect `undefined: seedCookies`. Add it to `fetch.go`:

  ```go
  // seedCookies installs a raw Cookie header value into the jar, scoped to
  // origin. A jar is used rather than a request header on purpose: net/http
  // will not send jar cookies to a different origin, so a redirect that leaves
  // the indexer cannot carry the session away with it.
  func seedCookies(jar http.CookieJar, origin *url.URL, raw string) {
      if raw == "" {
          return
      }
      header := http.Header{}
      header.Add("Cookie", raw)
      cookies := (&http.Request{Header: header}).Cookies()
      if len(cookies) > 0 {
          jar.SetCookies(origin, cookies)
      }
  }
  ```

- [ ] 29. Run `go test ./indexarr/download/...`. Green.

- [ ] 30. Add `NewFetcherFor` to `fetch.go`. It reads the two Secrets and builds the
  client; it does **not** touch the URL (D1):

  ```go
  // NewFetcherFor is the production FetcherFor. It reads spec.secretRef and
  // status.sessionSecretRef, seeds a per-origin cookie jar, applies
  // spec.timeout, and paces through the injected limiter keyed by HOST.
  //
  // It does NOT rewrite the download URL. The link came out of the indexer's own
  // feed and already embeds whatever credential that indexer signs with;
  // appending our apikey can duplicate a parameter or break an HMAC, and the
  // failure reads like an auth error.
  func NewFetcherFor(c client.Client, lim *ratelimit.Limiter) FetcherFor {
      return func(ctx context.Context, idx *indexv1alpha1.Indexer) (Fetcher, error) {
          base, err := url.Parse(idx.Spec.BaseURL)
          if err != nil || base.Host == "" {
              return nil, fmt.Errorf("indexarr/download: indexer %s/%s has an unusable spec.baseURL", idx.Namespace, idx.Name)
          }
          secret, err := readSecretData(ctx, c, idx.Namespace, idx.Spec.SecretRef)
          if err != nil {
              return nil, err
          }
          session, err := readSessionData(ctx, c, idx.Namespace, idx.Status.SessionSecretRef)
          if err != nil {
              return nil, err
          }
          jar, err := cookiejar.New(nil)
          if err != nil {
              return nil, err
          }
          seedCookies(jar, base, string(secret["cookie"]))
          seedCookies(jar, base, string(session["cookie"]))

          timeout := idx.Spec.Timeout.Duration
          if timeout <= 0 {
              timeout = 30 * time.Second
          }
          f := &fetcher{
              base:    base.Host,
              limiter: lim,
              key:     base.Host,
              scrub: scrubber([]string{
                  string(secret["apikey"]), string(secret["passkey"]),
                  string(secret["rss_key"]), string(secret["password"]),
                  string(secret["cookie"]), string(session["cookie"]),
              }),
          }
          f.hc = &http.Client{Timeout: timeout, Jar: jar, CheckRedirect: f.checkRedirect}
          return f, nil
      }
  }

  // readSecretData reads spec.secretRef. Recognised keys are apikey, username,
  // password, cookie, passkey and rss_key (indexer_types.go:116). A missing
  // Secret is an error naming the Secret, never its contents.
  func readSecretData(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
      if ref == nil || ref.Name == "" {
          return nil, nil
      }
      var s corev1.Secret
      if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
          return nil, fmt.Errorf("indexarr/download: read secret %s/%s: %w", ns, ref.Name, err)
      }
      return s.Data, nil
  }

  // readSessionData reads status.sessionSecretRef. A missing session Secret is
  // NOT an error: the indexer may be public, or the login may not have run yet,
  // and the fetch should be attempted either way.
  func readSessionData(ctx context.Context, c client.Client, ns, name string) (map[string][]byte, error) {
      if name == "" {
          return nil, nil
      }
      var s corev1.Secret
      if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
          if apierrors.IsNotFound(err) {
              return nil, nil
          }
          return nil, fmt.Errorf("indexarr/download: read session secret %s/%s: %w", ns, name, err)
      }
      return s.Data, nil
  }
  ```

- [ ] 31. Run `go build ./indexarr/...` and `go test ./indexarr/download/...`; green.
  Commit: `feat(indexarr): authenticated fetcher with a classified redirect policy`.

**The grab ring — idempotent under redelivery**

- [ ] 32. Create `indexarr/download/grabs_test.go` (GPL header, package `download`)
  with the idempotency test first. This is the test the whole mechanism exists for:

  ```go
  func TestCountGrabIsIdempotentUnderRedelivery(t *testing.T) {
      ctx := context.Background()
      kv := newTestKV(t)
      idx := testIndexer("media", "nzbgeek", "uid-1", indexv1alpha1.LimitUnitDay)
      now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

      n, counted, err := CountGrab(ctx, kv, idx, "guid-a", now)
      require.NoError(t, err)
      require.True(t, counted)
      require.Equal(t, int32(1), n)

      // The SAME grab again -- an RPC retry, a duplicate delivery, a grabarr
      // requeue. It must not double count.
      n, counted, err = CountGrab(ctx, kv, idx, "guid-a", now.Add(time.Second))
      require.NoError(t, err)
      require.False(t, counted, "a redelivered grab is not a new grab")
      require.Equal(t, int32(1), n)

      n, counted, err = CountGrab(ctx, kv, idx, "guid-b", now.Add(2*time.Second))
      require.NoError(t, err)
      require.True(t, counted)
      require.Equal(t, int32(2), n)
  }
  ```

- [ ] 33. Run it; expect `undefined: CountGrab`, `newTestKV`, `testIndexer`. Add the
  helpers to `grabs_test.go`:

  ```go
  func newTestKV(t *testing.T) events.KV {
      t.Helper()
      bus := membus.New(nil)
      t.Cleanup(func() { _ = bus.Close() })
      require.NoError(t, bus.Ensure(context.Background(), events.Default()))
      return bus.KV(events.BucketIndexerLimits)
  }

  func testIndexer(ns, name, uid string, unit indexv1alpha1.LimitUnit) *indexv1alpha1.Indexer {
      return &indexv1alpha1.Indexer{
          ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: k8stypes.UID(uid)},
          Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://tr.example", Limits: &indexv1alpha1.Limits{Unit: unit}},
      }
  }
  ```

- [ ] 34. Create `indexarr/download/grabs.go` (GPL header) with the ring type, the
  key and the window. The doc comment records why the UID comes from the object:

  ```go
  // maxRingEntries caps the ring. Spec §5 pins <indexer-uid>.grab as a timestamp
  // ring under CAS in clustarr-indexer-limits (TTL 2d); an uncapped ring is how
  // a busy indexer turns a KV value into a megabyte.
  const maxRingEntries = 512

  // grabEntry is one grab. The GUID is stored, not just the timestamp, and that
  // is what makes counting idempotent: a redelivered download for a GUID already
  // in the ring changes nothing.
  type grabEntry struct {
      GUID string `json:"g"`
      At   int64  `json:"t"` // unix seconds
  }

  // GrabRingKey is the key spec §5's KV table names. The UID must come from the
  // LIVE object, never from DownloadRequest.IndexerRef.UID: the request's UID is
  // caller-supplied and a stale one would open a second ring for one indexer.
  func GrabRingKey(uid string) string { return events.KVKeyToken(uid) + ".grab" }

  // grabWindow is the limit window. Prowlarr counts ReleaseGrabbed events over
  // 1h or 24h (docs/research/indexers.md §6); the CRD's default unit is day.
  func grabWindow(idx *indexv1alpha1.Indexer) time.Duration {
      if idx.Spec.Limits != nil && idx.Spec.Limits.Unit == indexv1alpha1.LimitUnitHour {
          return time.Hour
      }
      return 24 * time.Hour
  }

  // pruneRing drops entries older than the window and keeps the newest
  // maxRingEntries. It is pure so the window arithmetic is testable without a bus.
  func pruneRing(ring []grabEntry, cutoff time.Time, max int) []grabEntry {
      out := ring[:0:0]
      for _, e := range ring {
          if e.At >= cutoff.Unix() {
              out = append(out, e)
          }
      }
      if len(out) > max {
          out = out[len(out)-max:]
      }
      return out
  }
  ```

- [ ] 35. Add `CountGrab` to `grabs.go`, with the CAS loop:

  ```go
  // casAttempts bounds the optimistic loop. Contention here is one process
  // grabbing twice at once; three attempts is generous and a failure is
  // non-fatal to the caller.
  const casAttempts = 3

  // CountGrab records one grab of guid against idx and returns the number of
  // grabs in the current window. counted is false when guid was already in the
  // ring -- a redelivery, which must not double count.
  //
  // The caller treats an error as NON-FATAL: an accounting outage must never
  // strand a grab that already has its bytes.
  func CountGrab(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, guid string, now time.Time) (int32, bool, error) {
      key := GrabRingKey(string(idx.UID))
      cutoff := now.Add(-grabWindow(idx))

      var lastErr error
      for attempt := 0; attempt < casAttempts; attempt++ {
          var (
              ring []grabEntry
              rev  uint64
              have bool
          )
          ent, err := kv.Get(ctx, key)
          switch {
          case err == nil:
              have, rev = true, ent.Revision
              // A value we cannot decode is a value we replace. Failing shut
              // here would wedge counting for this indexer for the bucket's
              // whole 2d TTL.
              if uerr := json.Unmarshal(ent.Value, &ring); uerr != nil {
                  logging.FromContext(ctx).Warn("indexarr/download: replacing an undecodable grab ring",
                      "indexer", idx.Name, "err", uerr)
                  ring = nil
              }
          case errors.Is(err, events.ErrKeyNotFound):
          default:
              return 0, false, err
          }

          ring = pruneRing(ring, cutoff, maxRingEntries)
          for _, e := range ring {
              if e.GUID == guid {
                  return int32(len(ring)), false, nil
              }
          }
          ring = pruneRing(append(ring, grabEntry{GUID: guid, At: now.Unix()}), cutoff, maxRingEntries)

          val, err := json.Marshal(ring)
          if err != nil {
              return 0, false, err
          }
          if have {
              _, err = kv.Update(ctx, key, val, rev)
          } else {
              _, err = kv.Create(ctx, key, val)
          }
          switch {
          case err == nil:
              return int32(len(ring)), true, nil
          case errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyExists):
              lastErr = err
              continue
          default:
              return 0, false, err
          }
      }
      return 0, false, fmt.Errorf("indexarr/download: grab ring CAS gave up after %d attempts: %w", casAttempts, lastErr)
  }
  ```

- [ ] 36. Run `go test -run TestCountGrab ./indexarr/download/`. Green.

- [ ] 37. Append the window and cap tests to `grabs_test.go`:

  ```go
  func TestGrabRingPrunesOutsideTheWindow(t *testing.T) {
      ctx := context.Background()
      kv := newTestKV(t)
      idx := testIndexer("media", "tr", "uid-2", indexv1alpha1.LimitUnitHour)
      t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

      _, _, err := CountGrab(ctx, kv, idx, "old", t0)
      require.NoError(t, err)
      n, counted, err := CountGrab(ctx, kv, idx, "new", t0.Add(90*time.Minute))
      require.NoError(t, err)
      require.True(t, counted)
      require.Equal(t, int32(1), n, "the hour-old grab aged out of a 1h window")
  }

  func TestGrabRingIsCapped(t *testing.T) {
      ring := make([]grabEntry, 0, maxRingEntries+10)
      now := time.Now()
      for i := 0; i < maxRingEntries+10; i++ {
          ring = append(ring, grabEntry{GUID: fmt.Sprintf("g%d", i), At: now.Unix()})
      }
      got := pruneRing(ring, now.Add(-time.Hour), maxRingEntries)
      require.Len(t, got, maxRingEntries)
      require.Equal(t, "g10", got[0].GUID, "the OLDEST entries are the ones dropped")
  }

  func TestGrabRingKeyGoesThroughKVKeyToken(t *testing.T) {
      // A UID is a UUID today, but the key builder must not assume it: every KV
      // key in this repo goes through KVKeyToken, and an illegal key makes both
      // Put and Delete fail.
      require.Equal(t, events.KVKeyToken("a/b:c")+".grab", GrabRingKey("a/b:c"))
      require.True(t, events.ValidKVKey(GrabRingKey("a/b:c")))
      require.True(t, events.ValidKVKey(GrabRingKey("")))
  }
  ```

- [ ] 38. Run `go test ./indexarr/download/...`; green. Run
  `golangci-lint-v2 run ./indexarr/download/...`. Commit:
  `feat(indexarr): idempotent grab accounting in the indexer-limits ring`.

**The download handler**

- [ ] 39. Create `indexarr/download/service.go` (GPL header) with the struct, the
  closed result vocabulary and the reply constructors:

  ```go
  // The closed set of metric outcome values. An indexer-supplied string -- a
  // Content-Type, a tracker error, a GUID -- must NEVER reach a label.
  const (
      resultOK             = "ok"
      resultMagnet         = "magnet"
      resultRedirect       = "redirect"
      resultNoURL          = "no_url"
      resultBadRequest     = "bad_request"
      resultNotFound       = "not_found"
      resultDisabled       = "disabled"
      resultUnauthorized   = "unauthorized"
      resultHTTPError      = "http_error"
      resultTransport      = "transport_error"
      resultTooLarge       = "too_large"
      resultInvalidPayload = "invalid_payload"
      resultNotConfigured  = "not_configured"
      resultGrabCounted    = "grab_counted"
      resultGrabDuplicate  = "grab_duplicate"
      resultGrabFailed     = "grab_count_failed"
  )

  // unknownIndexerLabel keeps an unresolved request off the metric's label
  // space: req.IndexerRef.Name is caller-supplied and unbounded.
  const unknownIndexerLabel = "_unknown"

  type Service struct {
      Client client.Client
      Bus    events.Bus
      Fetch  FetcherFor
      Now    func() time.Time
  }

  func (s *Service) now() time.Time {
      if s.Now != nil {
          return s.Now()
      }
      return time.Now()
  }

  // fail builds the ONE shape a failure takes: a populated Error, scrubbed of
  // this indexer's secret values and truncated, because grabarr writes it onto
  // a Download's status condition where a human reads it.
  func fail(scrub func(string) string, format string, args ...any) schema.DownloadResponse {
      msg := fmt.Sprintf(format, args...)
      if scrub != nil {
          msg = scrub(msg)
      }
      return schema.DownloadResponse{Error: truncate(msg, maxErrorChars)}
  }
  ```

- [ ] 40. Create `indexarr/download/service_test.go` (GPL header, package `download`)
  with the validation table. Every row asserts a **populated reply**, not an error —
  that is the contract D9 pins:

  ```go
  func TestHandleRejectsBadRequestsWithAPopulatedError(t *testing.T) {
      for _, tc := range []struct{ name string; req schema.DownloadRequest; want string }{
          {"no namespace", schema.DownloadRequest{IndexerRef: schema.Ref{Name: "tr"}, GUID: "g", URL: "https://x/y"}, "namespace"},
          {"no name", schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media"}, GUID: "g", URL: "https://x/y"}, "name"},
          {"no guid", schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, URL: "https://x/y"}, "guid"},
          {"no url", schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: "g"}, "download URL"},
      } {
          t.Run(tc.name, func(t *testing.T) {
              got := (&Service{}).Handle(context.Background(), tc.req)
              require.Contains(t, got.Error, tc.want)
              require.Nil(t, got.Bytes)
              require.Empty(t, got.MagnetURL)
              require.Empty(t, got.RedirectURL)
          })
      }
  }
  ```

  The missing-namespace row is Ruling R10's hole closed at the source: unlike
  `SearchRequest`, this verb has zero callers today, so refusing costs nothing and
  makes a cluster-wide `Indexer` lookup impossible by construction.

- [ ] 41. Run it; expect `undefined: Handle`. Add the entry point and validation to
  `service.go`:

  ```go
  // Handle is the rpc.indexarr.download body. It NEVER returns an error: after a
  // successful decode every failure is a populated DownloadResponse.Error, so
  // the caller can distinguish "this release could not be fetched" (a reply)
  // from "the request never reached indexarr" (a transport error). D1-5's
  // handleDownload owns the decode and is the only place a transport error is
  // produced for this verb.
  func (s *Service) Handle(ctx context.Context, req schema.DownloadRequest) schema.DownloadResponse {
      ctx, span := tracing.Start(ctx, "indexarr.download")
      defer span.End()
      start := s.now()

      resp, result, label := s.handle(ctx, req)

      metrics.IndexerQueriesTotal.WithLabelValues(label, result).Inc()
      metrics.IndexerQueryDuration.WithLabelValues(label, "download").
          Observe(s.now().Sub(start).Seconds())
      span.SetAttributes(
          attribute.String("indexer.namespace", req.IndexerRef.Namespace),
          attribute.String("indexer.name", req.IndexerRef.Name),
          attribute.String("download.result", result),
          attribute.Int("download.bytes", len(resp.Bytes)),
      )
      if resp.Error != "" {
          tracing.RecordError(span, errors.New(resp.Error))
      }
      return resp
  }

  func (s *Service) handle(ctx context.Context, req schema.DownloadRequest) (schema.DownloadResponse, string, string) {
      switch {
      case req.IndexerRef.Namespace == "" || req.IndexerRef.Name == "":
          // Ruling R10's hole, closed by construction. Resolving a nameless
          // namespace cluster-wide would let namespace A's release be fetched
          // with namespace B's passkey and counted against B's grab limit.
          return fail(nil, "indexarr: indexerRef.namespace and indexerRef.name are required"),
              resultBadRequest, unknownIndexerLabel
      case req.GUID == "":
          return fail(nil, "indexarr: guid is required"), resultBadRequest, unknownIndexerLabel
      case req.URL == "":
          // relindex.Query has no GUID field and ADR-0003 fixes the Store at
          // four methods, so indexarr cannot resolve a guid to a URL. Carried item.
          return fail(nil, "indexarr: release has no download URL; indexarr cannot resolve a guid without one"),
              resultNoURL, unknownIndexerLabel
      case s.Client == nil || s.Fetch == nil:
          return fail(nil, "indexarr: download verb is not configured"), resultNotConfigured, unknownIndexerLabel
      }
      return s.fetchAndCount(ctx, req)
  }
  ```

- [ ] 42. Run `go test -run TestHandleRejects ./indexarr/download/`. Green.

- [ ] 43. Append the indexer-resolution test to `service_test.go`:

  ```go
  func TestHandleResolvesTheIndexerAndRefusesADisabledOne(t *testing.T) {
      ctx := context.Background()
      ref := schema.Ref{Namespace: "media", Name: "tr"}
      req := schema.DownloadRequest{IndexerRef: ref, GUID: "g", URL: "https://tr.example/dl"}

      t.Run("missing", func(t *testing.T) {
          s := &Service{Client: fakeClient(t), Fetch: nilFetcherFor}
          got := s.Handle(ctx, req)
          require.Contains(t, got.Error, "media/tr")
      })
      t.Run("disabled", func(t *testing.T) {
          idx := testIndexer("media", "tr", "uid-3", indexv1alpha1.LimitUnitDay)
          idx.Spec.Enabled = ptr.To(false)
          s := &Service{Client: fakeClient(t, idx), Fetch: nilFetcherFor}
          got := s.Handle(ctx, req)
          require.Contains(t, got.Error, "disabled")
      })
      t.Run("in backoff is still served", func(t *testing.T) {
          idx := testIndexer("media", "tr", "uid-4", indexv1alpha1.LimitUnitDay)
          idx.Status.DisabledUntil = ptr.To(metav1.NewTime(time.Now().Add(time.Hour)))
          s := &Service{Client: fakeClient(t, idx), Fetch: stubFetcherFor(&FetchResult{MagnetURL: "magnet:?xt=urn:btih:z"})}
          got := s.Handle(ctx, req)
          require.Empty(t, got.Error, "escalation is health, not authorisation: an approved grab must not be stranded")
          require.Equal(t, "magnet:?xt=urn:btih:z", got.MagnetURL)
      })
  }
  ```

  `fakeClient` is `fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()`;
  `stubFetcherFor` returns a `Fetcher` whose `Fetch` yields the given result and whose
  `Scrub` is the identity; `nilFetcherFor` returns an error.

- [ ] 44. Run it; expect `undefined: fetchAndCount`. Add it, plus the classification,
  to `service.go`. Read the `Content-Length` branch carefully — it is the branch that
  protects a one-shot link:

  ```go
  func (s *Service) fetchAndCount(ctx context.Context, req schema.DownloadRequest) (schema.DownloadResponse, string, string) {
      var idx indexv1alpha1.Indexer
      key := types.NamespacedName{Namespace: req.IndexerRef.Namespace, Name: req.IndexerRef.Name}
      if err := s.Client.Get(ctx, key, &idx); err != nil {
          if apierrors.IsNotFound(err) {
              return fail(nil, "indexarr: indexer %s not found", key), resultNotFound, unknownIndexerLabel
          }
          return fail(nil, "indexarr: read indexer %s: %v", key, err), resultTransport, unknownIndexerLabel
      }
      // From here the label is a Kubernetes object name: bounded cardinality.
      label := idx.Name
      if idx.Spec.Enabled != nil && !*idx.Spec.Enabled {
          return fail(nil, "indexarr: indexer %s is disabled", key), resultDisabled, label
      }
      // status.disabledUntil is deliberately NOT checked: escalation is health,
      // and this release was already selected by a search that succeeded.
      // Refusing here strands an approved grab. Download failures also do not
      // feed RecordFailure -- a dead link is a release-level fact, not an
      // indexer-level one. Carried item.

      f, err := s.Fetch(ctx, &idx)
      if err != nil {
          return fail(nil, "indexarr: build client for %s: %v", key, redactErr(err)), resultTransport, label
      }
      log := logging.FromContext(ctx).With("indexer", idx.Name, "namespace", idx.Namespace,
          "guid", truncate(redactRawURL(req.GUID), 128))

      res, err := f.Fetch(ctx, req.URL)
      if err != nil {
          return fail(f.Scrub, "indexarr: fetch from %s: %v", key, redactErr(err)), resultTransport, label
      }

      resp, result := s.classify(ctx, f, res, log)
      if resp.Error == "" {
          s.countGrab(ctx, &idx, req.GUID, log)
      }
      return resp, result, label
  }

  // classify turns one FetchResult into the reply. DownloadResponse carries
  // "exactly one of Bytes, MagnetURL or RedirectURL", so every branch sets
  // exactly one -- or an Error and none.
  func (s *Service) classify(ctx context.Context, f Fetcher, res *FetchResult, log *slog.Logger) (schema.DownloadResponse, string) {
      switch {
      case res.MagnetURL != "":
          log.Debug("indexarr/download: magnet link")
          return schema.DownloadResponse{MagnetURL: res.MagnetURL}, resultMagnet

      case res.OffHostURL != "":
          // Cause 1 of RedirectURL: the chain left the indexer's origin, where
          // our session cookie would not be sent anyway. Returned INTACT --
          // grabarr needs it -- and logged redacted, because it may carry a passkey.
          log.Debug("indexarr/download: handing back an off-host link", "url", redactRawURL(res.OffHostURL))
          return schema.DownloadResponse{RedirectURL: res.OffHostURL}, resultRedirect
      }

      defer func() { _ = res.Body.Close() }()

      if res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden {
          return fail(f.Scrub, "indexarr: indexer returned HTTP %d; the session or passkey may have expired", res.Status), resultUnauthorized
      }
      if res.Status < 200 || res.Status >= 300 {
          // The STATUS only. An error page's body is attacker-controlled text
          // on its way to a status condition.
          return fail(f.Scrub, "indexarr: indexer returned HTTP %d", res.Status), resultHTTPError
      }

      // Cause 2 of RedirectURL: too big to send. Decided on Content-Length
      // ALONE, before the body is touched, so a one-shot link is not spent on
      // bytes we would refuse.
      if res.ContentLen > MaxPayloadBytes {
          log.Info("indexarr/download: body exceeds the broker payload budget; handing back the link",
              "contentLength", res.ContentLen, "max", MaxPayloadBytes)
          return schema.DownloadResponse{RedirectURL: res.FinalURL.String()}, resultRedirect
      }

      body, err := readPayload(res.Body)
      if err != nil {
          if errors.Is(err, ErrResponseTooLarge) {
              // No Content-Length, so this was only discovered mid-read. The
              // link may already be spent, which is why this is an Error and
              // not a RedirectURL.
              return fail(f.Scrub, "indexarr: %v", err), resultTooLarge
          }
          return fail(f.Scrub, "indexarr: read payload: %v", redactErr(err)), resultInvalidPayload
      }

      kind := sniffKind(body)
      if kind == kindHTML {
          return fail(f.Scrub, "indexarr: indexer returned an HTML page, not a download payload (session expired?)"), resultInvalidPayload
      }
      return schema.DownloadResponse{Bytes: body, ContentType: contentTypeFor(res.Header.Get("Content-Type"), kind)}, resultOK
  }
  ```

- [ ] 45. Run `go test ./indexarr/download/...`. Green for the resolution subtests.

- [ ] 46. Append the payload-classification table to `service_test.go`, driven through
  `stubFetcherFor` so each branch is exercised without a network:

  ```go
  func TestClassify(t *testing.T) {
      body := func(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
      hdr := func(ct string) http.Header { return http.Header{"Content-Type": []string{ct}} }
      final, _ := url.Parse("https://tr.example/dl?passkey=s3cret")

      for _, tc := range []struct {
          name       string
          res        *FetchResult
          wantResult string
          check      func(*testing.T, schema.DownloadResponse)
      }{
          {"torrent", &FetchResult{Status: 200, Header: hdr("text/html"), Body: body("d8:announce1:xe"), ContentLen: 15, FinalURL: final}, resultOK,
              func(t *testing.T, r schema.DownloadResponse) {
                  require.Equal(t, "application/x-bittorrent", r.ContentType, "the bytes win over a wrong header")
              }},
          {"nzb", &FetchResult{Status: 200, Header: hdr(""), Body: body("<?xml version=\"1.0\"?><nzb/>"), ContentLen: 27, FinalURL: final}, resultOK,
              func(t *testing.T, r schema.DownloadResponse) { require.Equal(t, "application/x-nzb", r.ContentType) }},
          {"login page", &FetchResult{Status: 200, Header: hdr("text/html"), Body: body("<!DOCTYPE html><html>login"), ContentLen: 26, FinalURL: final}, resultInvalidPayload,
              func(t *testing.T, r schema.DownloadResponse) { require.Contains(t, r.Error, "HTML page") }},
          {"403", &FetchResult{Status: 403, Header: hdr(""), Body: body("nope"), FinalURL: final}, resultUnauthorized,
              func(t *testing.T, r schema.DownloadResponse) {
                  require.Contains(t, r.Error, "403")
                  require.NotContains(t, r.Error, "nope", "an error page's body never reaches the reply")
              }},
          {"oversize by Content-Length", &FetchResult{Status: 200, Header: hdr(""), Body: body("ignored"), ContentLen: MaxPayloadBytes + 1, FinalURL: final}, resultRedirect,
              func(t *testing.T, r schema.DownloadResponse) {
                  require.Equal(t, final.String(), r.RedirectURL, "grabarr needs the link intact")
                  require.Empty(t, r.Error)
              }},
          {"oversize with no Content-Length", &FetchResult{Status: 200, Header: hdr(""), Body: body(strings.Repeat("d", MaxPayloadBytes+1)), ContentLen: -1, FinalURL: final}, resultTooLarge,
              func(t *testing.T, r schema.DownloadResponse) { require.Contains(t, r.Error, "size limit") }},
          {"empty", &FetchResult{Status: 200, Header: hdr(""), Body: body(""), ContentLen: 0, FinalURL: final}, resultInvalidPayload,
              func(t *testing.T, r schema.DownloadResponse) { require.Contains(t, r.Error, "empty body") }},
      } {
          t.Run(tc.name, func(t *testing.T) {
              got, result := (&Service{}).classify(context.Background(), identityFetcher{}, tc.res, slog.Default())
              require.Equal(t, tc.wantResult, result)
              tc.check(t, got)
              require.LessOrEqual(t, len(got.Error), maxErrorChars)
          })
      }
  }
  ```

- [ ] 47. Run `go test ./indexarr/download/...`; green.

- [ ] 48. Append the leak test. It is worth its own name because it is the failure the
  operator sees, and it must fail loudly if someone drops `Scrub` from an error path:

  ```go
  func TestErrorsNeverCarryAPasskey(t *testing.T) {
      const passkey = "deadbeefcafe1234"
      f := scrubbingFetcher{secret: passkey}
      res := &FetchResult{Status: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("x"))}
      got, _ := (&Service{}).classify(context.Background(), f, res, slog.Default())
      require.NotEmpty(t, got.Error)

      // And the belt: any message built by fail() with this scrubber is clean.
      msg := fail(f.Scrub, "indexarr: get https://tr.example/dl?passkey=%s: refused", passkey)
      require.NotContains(t, msg.Error, passkey)
      require.Contains(t, msg.Error, "***")
  }
  ```

- [ ] 49. Run it; green (the code in step 44 already routes every failure through
  `f.Scrub`). If it fails, the fix is in `classify`, not in the test. Commit:
  `feat(indexarr): serve rpc.indexarr.download`.

**The grab count and its status projection**

- [ ] 50. Add `countGrab` to `service.go`. It is deliberately the only place a status
  apply happens in this package:

  ```go
  // countGrab records the grab and projects the ring into status.grabsInWindow.
  //
  // Both halves are NON-FATAL: the bytes are already in the reply, and an
  // accounting outage must not strand a grab. The ring is the source of truth;
  // status.grabsInWindow is a projection of it, which matters because D1-5's
  // fan-out and D1-7's RSS worker also apply under this manager from their own
  // cached read. A lost update there self-heals at the next grab.
  func (s *Service) countGrab(ctx context.Context, idx *indexv1alpha1.Indexer, guid string, log *slog.Logger) {
      ctx, span := tracing.Start(ctx, "indexarr.download.count_grab")
      defer span.End()
      if s.Bus == nil {
          return
      }
      n, counted, err := CountGrab(ctx, s.Bus.KV(events.BucketIndexerLimits), idx, guid, s.now())
      if err != nil {
          log.Warn("indexarr/download: grab accounting failed", "err", err)
          metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, resultGrabFailed).Inc()
          tracing.RecordError(span, err)
          return
      }
      if !counted {
          // A redelivery. The count did not change, so there is nothing to
          // apply -- and an apply that does not happen releases nothing, which
          // is the one safe shortcut under this field manager.
          metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, resultGrabDuplicate).Inc()
          return
      }
      metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, resultGrabCounted).Inc()
      if n == idx.Status.GrabsInWindow {
          return
      }
      if err := idxstatus.Patch(ctx, s.Client, k8s.ManagerIndexarrWorker, idx,
          func(ac *indexac.IndexerStatusApplyConfiguration) {
              // COMPLETE declaration, every time. Server-side apply REPLACES a
              // manager's ownership set rather than merging it, so a field this
              // manager owned and now omits is released and reads as zero.
              // WorkerFields is the single definition of that set (Ruling R14);
              // this assignment makes the completeness visible at the call site
              // and is correct whether or not Patch pre-seeds ac.
              *ac = *idxstatus.WorkerFields(idx.Status)
              ac.WithGrabsInWindow(n)
          }); err != nil {
          log.Warn("indexarr/download: grabsInWindow apply failed", "err", err)
      }
  }
  ```

- [ ] 51. Create `indexarr/download/suite_envtest_test.go` (GPL header, package
  `download_test`) with the shared control plane. A skip is **not** a pass — these
  tests only run under `make test`:

  ```go
  var testCfg *rest.Config

  func TestMain(m *testing.M) {
      if os.Getenv("KUBEBUILDER_ASSETS") == "" {
          os.Exit(m.Run())
      }
      env := &envtest.Environment{
          CRDDirectoryPaths:     []string{"../../config/crd/bases"},
          ErrorIfCRDPathMissing: true,
      }
      cfg, err := env.Start()
      if err != nil {
          panic("start envtest: " + err.Error())
      }
      testCfg = cfg
      code := m.Run()
      if err := env.Stop(); err != nil {
          panic("stop envtest: " + err.Error())
      }
      os.Exit(code)
  }

  func newTestClient(t *testing.T) client.Client {
      t.Helper()
      if testCfg == nil {
          t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
      }
      c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
      require.NoError(t, err)
      return c
  }

  func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
      t.Helper()
      err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
      if err != nil && !apierrors.IsAlreadyExists(err) {
          t.Fatalf("create namespace %s: %v", ns, err)
      }
  }
  ```

- [ ] 52. Create `indexarr/download/status_envtest_test.go` with **the** test this
  whole design point exists for. It drives the object to a real steady state first —
  a blank object cannot observe a release, because there was nothing to release:

  ```go
  func TestGrabCountDoesNotReleaseTheOtherWorkerFields(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "dl-ssa", "tr"
      newNamespace(t, ctx, c, ns)

      idx := &indexv1alpha1.Indexer{
          ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
          Spec: indexv1alpha1.IndexerSpec{
              BaseURL: "https://tr.example",
              Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
          },
      }
      require.NoError(t, c.Create(ctx, idx))

      // STEADY STATE FIRST. The RSS worker has run, the fan-out has run, and
      // both wrote under indexarr-worker. Without this the test cannot observe
      // a release at all.
      rssAt := metav1.NewTime(time.Now().Truncate(time.Second))
      require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, idx,
          func(ac *indexac.IndexerStatusApplyConfiguration) {
              *ac = *idxstatus.WorkerFields(indexv1alpha1.IndexerStatus{
                  LastRssAt: &rssAt, LastRssNewCount: 7, IndexedReleases: 4211,
                  QueriesInWindow: 11, EscalationLevel: 2,
              })
          }))
      require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
      require.Equal(t, int32(11), idx.Status.QueriesInWindow)

      // Now one grab.
      bus := membus.New(nil)
      t.Cleanup(func() { _ = bus.Close() })
      require.NoError(t, bus.Ensure(ctx, events.Default()))
      s := &download.Service{Client: c, Bus: bus}
      s.CountGrabForTest(ctx, idx, "guid-a")

      var after indexv1alpha1.Indexer
      require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
      require.Equal(t, int32(1), after.Status.GrabsInWindow)
      // Every one of these would be zero if the apply had declared less than
      // the complete indexarr-worker set.
      require.Equal(t, int32(11), after.Status.QueriesInWindow, "released by a partial apply")
      require.Equal(t, int64(4211), after.Status.IndexedReleases, "released by a partial apply")
      require.Equal(t, int32(7), after.Status.LastRssNewCount, "released by a partial apply")
      require.NotNil(t, after.Status.LastRssAt, "released by a partial apply")
      require.Equal(t, int32(2), after.Status.EscalationLevel, "released by a partial apply")
  }
  ```

- [ ] 53. Run it; expect `undefined: CountGrabForTest`. Add the exported shim to
  `service.go` — a one-line wrapper, so the envtest can drive the accounting path
  without a fetcher, a bus subject or an HTTP server:

  ```go
  // CountGrabForTest drives the accounting path directly. It exists so the SSA
  // release test and D1-9's e2e can exercise the status projection without a
  // fetcher or an HTTP server.
  func (s *Service) CountGrabForTest(ctx context.Context, idx *indexv1alpha1.Indexer, guid string) {
      s.countGrab(ctx, idx, guid, logging.FromContext(ctx))
  }
  ```

- [ ] 54. Run `make test 2>&1 | tail -30` and confirm the envtest ran rather than
  skipped — a suite that finishes in milliseconds skipped. Then append the redelivery
  half to `status_envtest_test.go` and run again:

  ```go
  func TestARedeliveredGrabDoesNotDoubleCountInStatus(t *testing.T) {
      // ... same setup ...
      s.CountGrabForTest(ctx, idx, "guid-a")
      require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
      s.CountGrabForTest(ctx, idx, "guid-a") // the RPC was retried
      var after indexv1alpha1.Indexer
      require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
      require.Equal(t, int32(1), after.Status.GrabsInWindow)
  }
  ```

- [ ] 55. Run `golangci-lint-v2 run ./indexarr/download/...`. Commit:
  `feat(indexarr): count grabs in the download verb, idempotently`.

**The query verb**

- [ ] 56. Create `indexarr/query/doc.go` (GPL header, `package query`) with a doc
  comment stating: (a) this verb reads the **local index only** — no HTTP, no bus, no
  `Indexer` lookup, no fan-out; (b) `Text` is attacker-controlled and passed **raw**
  because `pkg/relindex` owns FTS5 escaping and two escapers compose into a query
  matching nothing; (c) the closed filter vocabulary; (d) the carried item, verbatim:

  ```go
  // Multi-tenancy, and what this verb cannot do about it. QueryRequest carries
  // no namespace, relindex.Release has no namespace column, and the index is one
  // SQLite file per process, so this verb is inherently CLUSTER-WIDE. Its
  // results include Info.DownloadURL, which for a private tracker embeds a
  // passkey. Those URLs are never logged -- but they ARE returned, because the
  // download verb cannot resolve a guid without a URL and stripping them would
  // break the path this verb exists to serve.
  //
  // Ruling R4 keeps this theoretical: the verb has zero callers today. Before it
  // gets a real one (the M6 Torznab facade, or a query-mode Search), QueryRequest
  // must take a namespace and the index must gain a namespace column. Do NOT add
  // either now -- an API field with no consumer is how a field ships wrong.
  ```

- [ ] 57. Create `indexarr/query/filters_test.go` (GPL header, package `query`) with
  the vocabulary test. The unknown-key row is the load-bearing one:

  ```go
  func TestBuildQueryRejectsAnUnknownFilterByName(t *testing.T) {
      _, err := buildQuery(schema.QueryRequest{Filters: map[string]string{"seeders": "5"}})
      require.ErrorContains(t, err, `"seeders"`)
      require.ErrorContains(t, err, "category, indexer, protocol, since")

      // schema.QueryRequest's own doc names "quality" as an example filter.
      // relindex.Query has no quality column, so it is rejected BY NAME.
      _, err = buildQuery(schema.QueryRequest{Filters: map[string]string{"quality": "1080p"}})
      require.ErrorContains(t, err, `"quality"`)

      // A caller-supplied key is unbounded; it must be truncated before it is
      // quoted into a message that reaches a status condition.
      _, err = buildQuery(schema.QueryRequest{Filters: map[string]string{strings.Repeat("k", 10_000): "v"}})
      require.Less(t, len(err.Error()), 256)
  }

  func TestBuildQueryMapsEveryKnownFilter(t *testing.T) {
      since := "2026-09-01T00:00:00Z"
      q, err := buildQuery(schema.QueryRequest{
          Text:  "the matrix",
          Limit: 25,
          Filters: map[string]string{
              "protocol": "torrent",
              "indexer":  "nzbgeek, tr ",
              "category": "2000,2040",
              "since":    since,
          },
      })
      require.NoError(t, err)
      require.Equal(t, "the matrix", q.Text)
      require.Equal(t, "torrent", q.Protocol)
      require.Equal(t, []string{"nzbgeek", "tr"}, q.Indexers)
      require.Equal(t, []int{2000, 2040}, q.Categories)
      require.NotNil(t, q.Since)
      require.Equal(t, since, q.Since.Format(time.RFC3339))
      require.Equal(t, 25, q.Limit)
  }

  func TestBuildQueryRejectsMalformedFilterValues(t *testing.T) {
      for _, tc := range []struct{ name, key, val, want string }{
          {"protocol", "protocol", "carrier-pigeon", "torrent"},
          {"category", "category", "2000,abc", "category"},
          {"since", "since", "yesterday", "RFC3339"},
      } {
          t.Run(tc.name, func(t *testing.T) {
              _, err := buildQuery(schema.QueryRequest{Filters: map[string]string{tc.key: tc.val}})
              require.ErrorContains(t, err, tc.want)
          })
      }
  }
  ```

- [ ] 58. Run `go test ./indexarr/query/...`. Expect `undefined: buildQuery`.

- [ ] 59. Create `indexarr/query/filters.go` (GPL header) with the constants and the
  vocabulary:

  ```go
  const (
      // DefaultLimit applies when QueryRequest.Limit is zero. relindex's Store
      // "never invents one", so this verb must always send a positive limit.
      DefaultLimit = 100

      // MaxScanRows ceilings limit+offset. QueryRequest.Offset has no
      // counterpart in relindex.Query and ADR-0003 fixes the Store at four
      // methods, so paging is over-fetch plus slice, and the over-fetch is bounded.
      MaxScanRows = 2000

      // MaxReplyBytes is the same broker-payload budget the download verb uses:
      // a QueryResponse is one NATS message and max_payload is 8Mi.
      MaxReplyBytes = 4 << 20

      maxFilterKeyChars = 64
      maxIndexerFilters = 32
      maxErrorChars     = 512
  )

  // filterKeys is the CLOSED set this verb understands. An unknown key is an
  // ERROR, not a silent no-op: dropping a filter silently returns MORE data than
  // the caller asked for, and the caller cannot tell.
  var filterKeys = []string{"category", "indexer", "protocol", "since"}

  func knownFilter(k string) bool { return slices.Contains(filterKeys, k) }
  ```

- [ ] 60. Add `buildQuery` to `filters.go`. Note the sorted iteration — without it two
  bad filters produce a message that changes between runs:

  ```go
  // buildQuery maps a QueryRequest onto relindex.Query. It is pure: no I/O, no
  // clock, no store.
  func buildQuery(req schema.QueryRequest) (relindex.Query, error) {
      if req.Limit < 0 || req.Offset < 0 {
          return relindex.Query{}, errors.New("query: limit and offset must not be negative")
      }
      limit := int(req.Limit)
      if limit == 0 {
          limit = DefaultLimit
      }
      if limit > schema.MaxSearchReleases {
          limit = schema.MaxSearchReleases
      }
      scan := min(limit+int(req.Offset), MaxScanRows)

      q := relindex.Query{
          // RAW. pkg/relindex owns FTS5 escaping (Task D1-2); pre-escaping here
          // would compose two escapers into a query that matches nothing, and
          // is how an injection "fix" becomes an outage.
          Text:  req.Text,
          Limit: scan,
      }

      // Sorted, so two bad filters give a deterministic message.
      keys := slices.Sorted(maps.Keys(req.Filters))
      for _, k := range keys {
          v := strings.TrimSpace(req.Filters[k])
          if !knownFilter(k) {
              return relindex.Query{}, fmt.Errorf("query: unknown filter %q; known filters are %s",
                  truncate(k, maxFilterKeyChars), strings.Join(filterKeys, ", "))
          }
          switch k {
          case "protocol":
              if v != string(commonv1.ProtocolTorrent) && v != string(commonv1.ProtocolUsenet) {
                  return relindex.Query{}, fmt.Errorf("query: filter protocol must be torrent or usenet")
              }
              q.Protocol = v
          case "indexer":
              for _, name := range strings.Split(v, ",") {
                  if name = strings.TrimSpace(name); name != "" {
                      q.Indexers = append(q.Indexers, name)
                  }
              }
              if len(q.Indexers) > maxIndexerFilters {
                  return relindex.Query{}, fmt.Errorf("query: filter indexer lists more than %d indexers", maxIndexerFilters)
              }
          case "category":
              for _, c := range strings.Split(v, ",") {
                  c = strings.TrimSpace(c)
                  if c == "" {
                      continue
                  }
                  n, err := strconv.Atoi(c)
                  if err != nil {
                      return relindex.Query{}, fmt.Errorf("query: filter category must be comma-separated newznab ids")
                  }
                  q.Categories = append(q.Categories, n)
              }
          case "since":
              ts, err := time.Parse(time.RFC3339, v)
              if err != nil {
                  return relindex.Query{}, fmt.Errorf("query: filter since must be RFC3339")
              }
              q.Since = &ts
          }
      }
      return q, nil
  }

  func truncate(s string, max int) string {
      if len(s) <= max {
          return s
      }
      return s[:max-3] + "..."
  }
  ```

- [ ] 61. Run `go test ./indexarr/query/...`. Green.

- [ ] 62. Append the clamping and hostile-text tests to `filters_test.go`:

  ```go
  func TestBuildQueryClampsTheLimitAndOverFetchesForTheOffset(t *testing.T) {
      q, err := buildQuery(schema.QueryRequest{})
      require.NoError(t, err)
      require.Equal(t, DefaultLimit, q.Limit, "the store never invents a limit")

      q, err = buildQuery(schema.QueryRequest{Limit: 10_000})
      require.NoError(t, err)
      require.Equal(t, schema.MaxSearchReleases, q.Limit)

      q, err = buildQuery(schema.QueryRequest{Limit: 50, Offset: 100})
      require.NoError(t, err)
      require.Equal(t, 150, q.Limit, "offset is over-fetch plus slice; relindex.Query has no Offset")

      q, err = buildQuery(schema.QueryRequest{Limit: 500, Offset: 100_000})
      require.NoError(t, err)
      require.Equal(t, MaxScanRows, q.Limit)

      _, err = buildQuery(schema.QueryRequest{Limit: -1})
      require.Error(t, err)
  }

  func TestHostileFTS5TextReachesTheStoreUNCHANGED(t *testing.T) {
      // FTS5 syntax is attacker-controlled here. The STORE escapes it (D1-2);
      // this verb must not, and must not reject it either -- a hostile string
      // is a normal, probably empty, result.
      for _, in := range []string{
          `"unbalanced`, `foo OR 1=1 --`, `NEAR/`, `a*`, `^`, `"" OR ""`,
          `'; DROP TABLE releases; --`, strings.Repeat("x", 4096),
      } {
          q, err := buildQuery(schema.QueryRequest{Text: in})
          require.NoError(t, err, "hostile text is data, not an error")
          require.Equal(t, in, q.Text, "the text must reach the store byte-for-byte")
      }
  }
  ```

- [ ] 63. Run `go test ./indexarr/query/...`; green. Commit:
  `feat(indexarr): the query verb's filter vocabulary`.

- [ ] 64. Create `indexarr/query/service_test.go` (GPL header, package `query`) with
  the handler's contract:

  ```go
  type fakeStore struct {
      got  relindex.Query
      rows []relindex.Release
      err  error
  }

  func (f *fakeStore) Search(_ context.Context, q relindex.Query) ([]relindex.Release, error) {
      f.got = q
      return f.rows, f.err
  }
  func (f *fakeStore) Upsert(context.Context, []relindex.Release) (int, error) { return 0, nil }
  func (f *fakeStore) Prune(context.Context, time.Time) (int, error)           { return 0, nil }
  func (f *fakeStore) Stats(context.Context) (relindex.Stats, error)           { return relindex.Stats{}, nil }

  func row(t *testing.T, guid string) relindex.Release {
      t.Helper()
      b, err := json.Marshal(schema.Release{Info: commonv1.ReleaseInfo{GUID: guid, Title: guid}})
      require.NoError(t, err)
      return relindex.Release{Indexer: "tr", GUID: guid, InfoJSON: b}
  }

  func TestHandleReturnsAnEmptyResultAsASuccess(t *testing.T) {
      got := (&Service{Store: &fakeStore{}}).Handle(context.Background(), schema.QueryRequest{Text: "nothing"})
      require.Empty(t, got.Error, "an empty result set is a success, not a failure")
      require.Empty(t, got.Releases)
      require.Zero(t, got.Total)
  }

  func TestHandleWithNoStoreAnswersWithAPopulatedError(t *testing.T) {
      got := (&Service{}).Handle(context.Background(), schema.QueryRequest{})
      require.Contains(t, got.Error, "not configured")
  }

  func TestHandleSurfacesAStoreFailureAsAnError(t *testing.T) {
      got := (&Service{Store: &fakeStore{err: errors.New("fts5: syntax error near \"OR\"")}}).
          Handle(context.Background(), schema.QueryRequest{Text: `foo OR`})
      require.Contains(t, got.Error, "fts5")
      require.Empty(t, got.Releases)
  }

  func TestHandlePagesWithOffsetAndReportsTotal(t *testing.T) {
      st := &fakeStore{rows: []relindex.Release{row(t, "a"), row(t, "b"), row(t, "c"), row(t, "d")}}
      got := (&Service{Store: st}).Handle(context.Background(), schema.QueryRequest{Limit: 2, Offset: 1})
      require.Empty(t, got.Error)
      require.Len(t, got.Releases, 2)
      require.Equal(t, "b", got.Releases[0].Info.GUID)
      require.Equal(t, "c", got.Releases[1].Info.GUID)
      require.Equal(t, int64(4), got.Total, "Total is the matched count before paging -- a LOWER BOUND")
      require.Equal(t, 3, st.got.Limit, "limit+offset was over-fetched")
  }

  func TestHandleSkipsAnUndecodableRowRatherThanFailingTheQuery(t *testing.T) {
      bad := relindex.Release{Indexer: "tr", GUID: "bad", InfoJSON: []byte("{not json")}
      st := &fakeStore{rows: []relindex.Release{bad, row(t, "good")}}
      got := (&Service{Store: st}).Handle(context.Background(), schema.QueryRequest{})
      require.Empty(t, got.Error)
      require.Len(t, got.Releases, 1)
      require.Equal(t, "good", got.Releases[0].Info.GUID)
  }
  ```

- [ ] 65. Run it; expect `undefined: Service`. Create `indexarr/query/service.go`
  (GPL header):

  ```go
  // localIndexLabel keeps this verb on the same dashboards as the indexer
  // metrics without adding an unbounded label value. It is a CONSTANT: no
  // caller-supplied string is ever a metric label.
  const localIndexLabel = "_local-index"

  type Service struct {
      Store relindex.Store
      Now   func() time.Time
  }

  func (s *Service) now() time.Time {
      if s.Now != nil {
          return s.Now()
      }
      return time.Now()
  }

  // Handle is the rpc.indexarr.query body. It reads the LOCAL INDEX ONLY. It
  // never returns an error: after a successful decode every failure is a
  // populated QueryResponse.Error.
  func (s *Service) Handle(ctx context.Context, req schema.QueryRequest) schema.QueryResponse {
      ctx, span := tracing.Start(ctx, "indexarr.query")
      defer span.End()
      start := s.now()
      defer func() {
          metrics.IndexerQueryDuration.WithLabelValues(localIndexLabel, "query").
              Observe(s.now().Sub(start).Seconds())
      }()

      resp, outcome := s.handle(ctx, req)
      metrics.IndexerQueriesTotal.WithLabelValues(localIndexLabel, outcome).Inc()
      span.SetAttributes(
          attribute.Int("query.limit", int(req.Limit)),
          attribute.Int("query.offset", int(req.Offset)),
          // The TEXT is never an attribute: attacker-controlled and unbounded.
          attribute.Int("query.text_len", len(req.Text)),
          attribute.Int("query.rows", len(resp.Releases)),
      )
      if resp.Error != "" {
          tracing.RecordError(span, errors.New(resp.Error))
      }
      return resp
  }

  func (s *Service) handle(ctx context.Context, req schema.QueryRequest) (schema.QueryResponse, string) {
      if s.Store == nil {
          return schema.QueryResponse{Error: "indexarr: release index is not configured"}, "not_configured"
      }
      q, err := buildQuery(req)
      if err != nil {
          return schema.QueryResponse{Error: truncate(err.Error(), maxErrorChars)}, "invalid_filter"
      }

      ctx, span := tracing.Start(ctx, "indexarr.query.store")
      rows, err := s.Store.Search(ctx, q)
      span.End()
      if err != nil {
          tracing.RecordError(span, err)
          return schema.QueryResponse{Error: truncate("indexarr: query the release index: "+err.Error(), maxErrorChars)}, "store_error"
      }

      limit := q.Limit - int(req.Offset)
      if limit < 0 {
          limit = 0
      }
      return schema.QueryResponse{
          Releases: decodeRows(ctx, rows, int(req.Offset), limit),
          // Total is the matched count BEFORE paging -- and a LOWER BOUND: a
          // four-method Store has no COUNT, so when the over-fetch hit
          // MaxScanRows this is exactly that ceiling and not the true total.
          Total: int64(len(rows)),
      }, "ok"
  }

  // decodeRows replays each row's InfoJSON -- the full schema.Release, stored
  // exactly so a query needs no re-query. A row that will not decode is skipped
  // and logged: one corrupt row must not fail a whole page.
  //
  // The reply is bounded by MaxReplyBytes because a QueryResponse is one NATS
  // message and the broker's max_payload is 8Mi.
  func decodeRows(ctx context.Context, rows []relindex.Release, offset, limit int) []schema.Release {
      if offset >= len(rows) || limit == 0 {
          return nil
      }
      log := logging.FromContext(ctx)
      out := make([]schema.Release, 0, min(limit, len(rows)-offset))
      budget := MaxReplyBytes
      for _, r := range rows[offset:] {
          var rel schema.Release
          if err := json.Unmarshal(r.InfoJSON, &rel); err != nil {
              log.Warn("indexarr/query: skipping a row whose infoJSON will not decode",
                  "indexer", r.Indexer, "err", err)
              continue
          }
          if budget -= len(r.InfoJSON); budget < 0 {
              break
          }
          out = append(out, rel)
          if len(out) == limit {
              break
          }
      }
      return out
  }
  ```

- [ ] 66. Run `go test ./indexarr/query/...`. All six tests pass.

- [ ] 67. Append the two guard tests that pin the properties R4 asks for:

  ```go
  func TestHandleMakesNoOutboundCall(t *testing.T) {
      // The Service has exactly one dependency and it is the local store. This
      // test is a compile-time-ish assertion of that: if someone adds an HTTP
      // client or a bus to Service, this stops constructing.
      var s Service
      require.Equal(t, 2, reflect.TypeOf(s).NumField(),
          "the query verb reads the local index only: no bus, no HTTP client, no Indexer lookup")
  }

  func TestReplyIsBoundedByTheBrokerPayload(t *testing.T) {
      big, err := json.Marshal(schema.Release{Info: commonv1.ReleaseInfo{Title: strings.Repeat("x", 64<<10)}})
      require.NoError(t, err)
      rows := make([]relindex.Release, 0, 200)
      for i := 0; i < 200; i++ {
          rows = append(rows, relindex.Release{Indexer: "tr", InfoJSON: big})
      }
      got := (&Service{Store: &fakeStore{rows: rows}}).Handle(context.Background(), schema.QueryRequest{Limit: 200})
      enc, err := json.Marshal(got)
      require.NoError(t, err)
      require.Less(t, len(enc), 8<<20, "a QueryResponse is ONE NATS message")
      require.Equal(t, int64(200), got.Total, "Total still reports what matched")
  }
  ```

- [ ] 68. Run `go test -race ./indexarr/query/... ./indexarr/download/...` and
  `golangci-lint-v2 run ./indexarr/query/... ./indexarr/download/...`. Commit:
  `feat(indexarr): serve rpc.indexarr.query against the local index`.

**Handing off**

- [ ] 69. Confirm the two handlers are assignable to D1-5's function types without
  either package importing the other. Add a compile-time assertion in each package's
  `doc.go` — it costs one line and catches a signature drift at build time rather
  than in D1-8:

  ```go
  // In indexarr/download/doc.go:
  //   var _ func(context.Context, schema.DownloadRequest) schema.DownloadResponse = (&Service{}).Handle
  // In indexarr/query/doc.go:
  //   var _ func(context.Context, schema.QueryRequest) schema.QueryResponse = (&Service{}).Handle
  ```

  Do **not** import `indexarr/search` to reference `search.DownloadFn` directly: a
  named func type accepts a plain func of the same signature, and the import would
  couple two packages that have no other reason to know about each other.

- [ ] 70. Write the wiring note for **D1-8** into `indexarr/download/doc.go`, so the
  next task does not have to infer it:

  ```go
  // Wiring (Task D1-8, in indexarr/run.go):
  //
  //	dl := &download.Service{Client: mgr.GetClient(), Bus: bus,
  //	    Fetch: download.NewFetcherFor(mgr.GetClient(), limiters)}
  //	q  := &query.Service{Store: store}
  //	svc := &search.Service{..., Download: dl.Handle, Query: q.Handle}
  //
  // `limiters` is the ONE *ratelimit.Limiter D1-3 constructs and shares with the
  // fan-out and the RSS worker, so all three pace against the same per-host
  // buckets. This package never constructs one.
  ```

- [ ] 71. Run the full gate: `make build`, `make test`, `make lint`. Confirm the
  envtest suites **ran** (`make test` sets `KUBEBUILDER_ASSETS`; a suite that finishes
  in milliseconds skipped, and a skip is not a pass). Commit:
  `docs(indexarr): record the D1-8 wiring for the download and query verbs`.

- [ ] 72. Record the carried items in the task report (not in a new doc file):
  1. **Grab-limit enforcement** is not built, and cannot be built on this counter
     alone — R3's undercount for `torrentURL`/`magnetURL`/`nzbURL` grabs.
  2. **Download failures do not escalate** the indexer. A dead link is a
     release-level fact; whether a run of them should feed `RecordFailure` is open.
  3. **`relindex` has no GUID lookup**, so `url`-less download requests cannot be
     served. Widening the four-method Store needs an ADR-0003 amendment.
  4. **`query` is cluster-wide** and returns passkey-bearing URLs. It needs a
     namespace on `QueryRequest` and a namespace column in the index before it gets
     a real caller (M6).
  5. **`QueryResponse.Total` is a lower bound**, and `QueryRequest.Offset` has no
     store-level counterpart; both are papered over in process.
  6. **The `quality` filter** `schema.QueryRequest` advertises cannot be served.
  7. **A dedicated `clustarr_indexer_grabs_total`** belongs in `pkg/obs/metrics`.
  8. **The redaction helpers are a third copy** (`pkg/cardigann`, `pkg/torznab`'s
     logging discipline, now here). A shared `pkg/redact` would be one small change.

---

#### What D1-9's e2e must exercise (Ruling R4)

Not this task's code to write, but this task's contract to state, so the e2e author
does not have to reverse-engineer it:

1. `rpc.indexarr.download` against an in-cluster fixture indexer that serves a real
   `.torrent`: the reply carries `Bytes` and `application/x-bittorrent`, and
   `kubectl get indexer -o jsonpath='{.status.grabsInWindow}'` reads `1`.
2. The same request again: `grabsInWindow` still reads `1`.
3. A fixture that 302s to a `magnet:` URI: the reply carries `MagnetURL` and no
   `Bytes`.
4. `rpc.indexarr.query` against the index the RSS worker filled in the same scenario:
   a text query returns the release, and a query with an unknown filter returns a
   populated `Error` rather than a transport failure.

---

## Wave 4 — serial, after every component task is approved

### Task D1-8: wiring, RBAC, readiness

**Files:**
- Modify: `indexarr/run.go` (`setupControllers`, `setupWorkers`, the RPC server, readiness)
- Create: `indexarr/wiring_envtest_test.go`
- Modify: `config/rbac/role.yaml`, `charts/clustarr/templates/rbac.yaml` (generated + the drift test)

**Path ownership:** controller only. No worker is running.

**Interfaces — Consumes:** every `SetupWithManager` and `Serve` from D1-2 through D1-7, each documented in its own package's `doc.go` as the exact call to make. Read those rather than inferring from the types.

**Why this is serial and last.** Registration points are shared files that every component task would otherwise edit. In Phase C this exact task found two Criticals that no component review could see: one controller was **never registered at all** — leaving the whole release-decision path inert on a fresh cluster, with a tree-wide grep showing zero production call sites — and a readiness check was added as a plain runnable, which controller-runtime treats as leader-election-gated, so every rollout deadlocked. Both were one-line fixes that no test caught.

- [ ] **Step 1: Register everything, and prove nothing is missing**

Wire each component behind the role flags. `indexarr` is **one replica, `--role all`, leader election forbidden** (spec §12), so unlike catalogarr there is no controller/worker role split to get wrong — but there is still a registration to forget.

Write the guard **first**, as a test that discovers rather than lists:

```go
// A hand-maintained list is the anti-pattern: the next component added to
// indexarr would simply not be added to it. Walk the packages instead.
func TestEveryIndexarrRunnableIsRegistered(t *testing.T) {
	// AST-walk indexarr/**, collect every exported type with Start and
	// NeedLeaderElection, and every SetupWithManager; assert run.go names
	// each one. require.Positive on the count so it cannot pass vacuously.
}
```

Phase C's equivalent guard shipped with a hand-maintained service list and two known blind spots; write this one to discover, and say in a comment what it still cannot see.

- [ ] **Step 2: Readiness must not be leader-election-gated**

A bare `manager.RunnableFunc` does **not** implement `LeaderElectionRunnable`, so controller-runtime's `runnables.Add` falls through to the leader-election group. `indexarr` forbids leader election, so this is latent here rather than fatal — but use `k8s.EveryReplica` anyway, because the constant that makes it safe is a deployment detail a future change can quietly invalidate, and because the next reader should not have to work out why this one service is exempt.

Readiness per spec §13: the informer caches synced, the SQLite index open and writable, and the bus connected. **Test it with leader election explicitly on**, even though production runs without it — the Phase C test that missed this set `LeaderElect = false` and passed vacuously.

- [ ] **Step 3: Pass the bus hooks at the construction site**

`pkg/k8s.ConnectBus` takes `k8s.WithBusHooks(obs.BusHooks())`. Without it, trace propagation exists and never runs — which was true in production for an entire phase. An AST guard already asserts every service's `run.go` passes it; make sure `indexarr` satisfies it rather than being the exception that makes the guard fail.

- [ ] **Step 4: Regenerate RBAC and check the whole surface**

```bash
make manifests && git status --short
```

Then sweep for the class the generated-role guard **cannot** see: a client call to a kind with **no marker at all**. The guard checks that markers match the generated role; it cannot know about a `Get` on a type nobody declared. In Phase C exactly that shipped — a controller read a `TranscodeProfile` with no marker, and the test exercising that path passed because envtest does not enforce RBAC. List every kind `indexarr` touches and confirm each has a marker.

- [ ] **Step 5: The chart copy must not drift**

`charts/clustarr/templates/rbac.yaml` carries a copy of the generated rules between sentinel comments, and `TestChartRBACMatchesTheGeneratedRole` compares them byte-for-byte. Confirm it still passes, and confirm it fails if you add a rule to only one side — in **both** directions. A drift test that only catches removals is half a test.

- [ ] **Step 6: Gate and commit**

```bash
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
make generate && make manifests && git status --short   # must be clean
make lint
go test -count=1 -race -p 4 ./...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(indexarr): register every controller, worker and RPC verb; readiness and RBAC" -- indexarr config charts
```

**Done when:** every component is registered and a discovering guard proves it; readiness is `EveryReplica` and tested with leader election on; the bus hooks are passed; every kind indexarr touches has a package-level RBAC marker; the chart cannot drift in either direction; the full gate is green.
### Task D1-9: the fixture indexer and end-to-end scenario 17

The standing rule is that nothing is finished until it is proven end to end on a
kind cluster. Phase C built that harness — `test/e2e/`, `test/fixtures/`,
`config/e2e/`, `hack/e2e.sh`, `images/Dockerfile.e2e-fixtures` — and it works:
five scenarios, green, rerunnable. **This task extends it; it does not redesign
it.** Read `test/e2e/main_test.go`, `test/e2e/helpers_test.go` and
`test/fixtures/tvdbstub/server.go` in full before writing a line.

The sixteen planned scenarios contain no pure-indexer scenario, because the
original Phase D bundled indexarr with downloads and import. Scenarios 2, 3 and
4 were assigned to D1 and then moved to D2 — 2 ends "grabbed and imported", 3 is
a failed *download*, 4 asserts "no duplicate **Download**" — so all three need
grabarr. **This task writes the new one and numbers it 17**, appended to the
roster rather than inserted, so no existing cross-reference shifts and Phase H's
audit counts seventeen.

Scenario 17 proves four things with real controllers on a real cluster:

1. an `Indexer` pointed at the in-cluster fixture reconciles to **healthy**,
   with `status.caps` from a live capabilities fetch and `protocol` resolved;
2. a `Search` CR returns **ranked results** — the whole Phase C path
   (snapshot → `rpc.indexarr.search` → decision engine → `RankAndCap` →
   `status.results`) that until now called into silence. This is the centre of
   gravity: it is the first time that path has had a server on the other end;
3. the **release firehose** reaches `catalogarr`'s RSS matcher with a legal
   envelope key — asserted by what the matcher *did*, not by what indexarr
   published;
4. **health and backoff are observable**: an Indexer pointed at a failing
   fixture endpoint escalates, and stops being queried.

Three `Test*` functions, not one: `scenarioTimeout` is 15 minutes and each of
these has its own multi-minute wait, and Phase C's split of `TestLibraryRescan`
from `TestLibraryRescanUnmatchedAndSchedule` exists so one failure does not bury
the other's planted state in the same cleanup stack.

#### Files

Created:

- `test/fixtures/torznabstub/server.go`
- `test/fixtures/torznabstub/reqlog.go`
- `test/fixtures/torznabstub/server_test.go`
- `test/fixtures/torznabstub/testdata/caps.xml`
- `test/fixtures/torznabstub/testdata/movie_900100.xml`
- `test/fixtures/torznabstub/testdata/rss.xml`
- `test/fixtures/torznabstub/testdata/empty.xml`
- `test/fixtures/torznabstub/testdata/error_100.xml`
- `test/fixtures/torznabstub_cmd.go`
- `test/fixtures/tmdbstub/testdata/movie_900100.json`
- `test/fixtures/tmdbstub/testdata/movie_900101.json`
- `config/e2e/torznab-stub.yaml`
- `config/e2e/indexarr-e2e-patch.yaml`
- `test/e2e/indexer_test.go`

Modified:

- `test/fixtures/root.go` (one `AddCommand` line)
- `test/fixtures/tmdbstub/server.go` (two embeds, two routes)
- `test/fixtures/seed/seed.go` (create the request-log directory)
- `config/e2e/kustomization.yaml` (two resources, one patch)
- `test/e2e/main_test.go` (one refusal gate)
- `test/e2e/helpers_test.go` (timeouts and shared helpers)
- `hack/e2e.sh` (one `WORKLOADS` entry, one dump block)
- `docs/superpowers/plans/2026-09-18-remaining-work.md` (roster line 17, two
  cross-references)

#### Path ownership

**Exclusive to this task:** `test/e2e/`, `test/fixtures/`, `config/e2e/`.
No other D1 task writes under them.

**Shared, append-only, coordinate before editing:**

- `hack/e2e.sh` — this task adds exactly two things: `deployment/torznab-stub`
  to the `WORKLOADS` array (after `deployment/tvdb-stub`, so a broken fixture
  image is still the cheapest failure to see) and one diagnostics block. It
  changes nothing else in the script.
- `docs/superpowers/plans/2026-09-18-remaining-work.md` — this task owns
  **only** the new roster entry 17 and the two sentences that reference it
  (the D1 bullet's "Number it when the D1 plan is written", and the
  "Rule for Phases C–G" line). Every other line in that file belongs to
  whoever is editing it for their own task.

**Explicitly NOT this task's:** `images/Dockerfile.e2e-fixtures`. Every byte
this task adds to the fixture image goes in through `go:embed`, exactly as
`test/fixtures/tvdbstub` already does for its fixture-owned series, so the
Dockerfile needs no new `COPY` and no other task's image change can collide
with this one.

#### Interfaces — Consumes

From **D1-3** (`indexarr/controller/indexer`), through the apiserver only:

- `IndexerStatus.Caps` populated from a real `torznab.Caps` fetch, with
  `Modes` keyed by `torznab.SearchMode` **wire** values (R5: `search`,
  `tvsearch`, `movie`, …), not the CRD doc comments' `tv-search`/`movie-search`.
- `IndexerStatus.Protocol`, `IndexerConditionReady`,
  `IndexerConditionAuthenticated`, `IndexerConditionHealthy`.
- `spec.generic.apiPath` joined onto `spec.baseURL` for every request.
- `spec.secretRef` → the `apikey` key, forwarded as the `apikey` query
  parameter.

From **D1-5** (`indexarr/search`), through `rpc.indexarr.search`:

- `schema.SearchResponse.Outcomes[].IndexerRef.Name` and
  `Releases[].Info.IndexerRef` are the Indexer's **object name** (correction
  C1), never `IndexerName`.
- `SearchOutcomeSkipped` for an indexer inside its backoff window
  (`indexer.Healthy` false).

From **D1-7** (`indexarr/worker/rss`), through NATS and then through
catalogarr:

- subject `events.ReleaseSubject(protocol, indexerObjectName, newznabTop)`;
- envelope key `"<namespace>/<indexerObjectName>"` — the matcher
  `events.Discard()`s (straight to the DLQ, bypassing MaxDeliver) on a key it
  cannot `strings.Cut` on `/`, so a wrong key fails silently and invisibly;
- `Msg-Id = events.MsgIDForRelease(indexerObjectName, guid)`, deduped for 2h.

From **D1-8** (`indexarr/run.go`) — **a new interface this task requires, and
the only thing here that is not already in the contract:**

- environment variable `CLUSTARR_INDEXER_STARTUP_GRACE`, a `time.Duration`
  string, defaulting to `indexer.StartupGrace` (15m), threaded to every
  `RecordFailure` call site as the grace against which `processStart` is
  compared (R17 already passes `processStart` explicitly; this makes the
  *window* configurable).

  Without it scenario 17's assertion 4 is untestable, and not marginally:
  `StartupGrace` is 15 minutes measured from **process start**, and the whole
  `make e2e` run has a 30-minute budget. Whether indexarr's process is 2 or 20
  minutes old when this scenario runs depends on how long the scenarios before
  it took — so the escalation either happens or does not, run to run, for
  reasons that have nothing to do with the code under test. `config/e2e` sets
  it to `0s`; production keeps 15m. Step 13 asserts the plumbing exists as a
  named precondition, so a missing env var reads as "indexarr was not built
  with CLUSTARR_INDEXER_STARTUP_GRACE" rather than as "escalation never moved".

From **Phase C**, unchanged and only read: `catalogarr/controller/search`
(`SearchRunningTimeout` = 5m), `catalogarr/worker/search` (`RankAndCap`,
1-based `Rank`, approved-before-rejected), `catalogarr/worker/rssmatcher`,
`catalogarr/worker/grab.Decide`.

#### The two channels, and why every assertion uses one of them

`test/e2e/main_test.go`'s package doc pins the constraint: the test binary runs
on the **host**, against the kind kubeconfig. It can reach the API server and
the host directory kind mounts at `/data`. It cannot reach a ClusterIP Service
and it cannot reach NATS — there are no `extraPortMappings` for either.

So every assertion below goes through the apiserver or through a file under
`$CLUSTARR_DATA_DIR`. That is why the fixture writes a **request log** onto the
shared `/data` volume: it is the only way for the test process to learn what the
fixture was actually asked, and it is what makes the assertions live rather than
merely consistent.

---

#### Steps

##### Part A — the fixture's data

- [ ] **1. Write `test/fixtures/torznabstub/testdata/caps.xml`.** A real
  `t=caps` document, shaped to `pkg/torznab`'s `wireCaps` (note `<limits>`
  carries `default` and `max` as attributes, and the *element* names are
  `tv-search`/`movie-search` while the *mode keys* they parse into are
  `tvsearch`/`movie` — that asymmetry is R5's whole subject):

  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <caps>
    <server title="Clustarr E2E Fixture Indexer"/>
    <limits default="50" max="100"/>
    <searching>
      <search available="yes" supportedParams="q" searchEngine="raw"/>
      <tv-search available="yes" supportedParams="q,season,ep,tvdbid"/>
      <movie-search available="yes" supportedParams="q,imdbid,tmdbid"/>
      <music-search available="no" supportedParams=""/>
      <audio-search available="no" supportedParams=""/>
      <book-search available="no" supportedParams=""/>
    </searching>
    <categories>
      <category id="2000" name="Movies">
        <subcat id="2030" name="Movies/SD"/>
        <subcat id="2040" name="Movies/HD"/>
        <subcat id="2050" name="Movies/BluRay"/>
      </category>
      <category id="5000" name="TV">
        <subcat id="5030" name="TV/SD"/>
        <subcat id="5040" name="TV/HD"/>
      </category>
    </categories>
    <tags>
      <tag name="freeleech" description="Free download"/>
    </tags>
  </caps>
  ```

  `music-search available="no"` is not filler: it is what lets step 18 assert
  that `status.caps.modes` records unavailable modes as unavailable rather than
  omitting them, which is the difference between "the indexer cannot do music"
  and "we never asked".

- [ ] **2. Write `test/fixtures/torznabstub/testdata/movie_900100.xml`** — the
  `t=movie` result feed, three items. **The order is load-bearing: worst
  first.** `RankAndCap` must reorder them, and if it ever degraded to a
  pass-through the scenario's `results[0]` assertion would fail instead of
  accidentally passing on arrival order.

  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
    <channel>
      <title>Clustarr E2E Fixture Indexer</title>
      <item>
        <title>Fixture.Search.Film.2019.480p.DVDRip.XviD-CLUSTARR</title>
        <guid>fixture-search-480p</guid>
        <link>http://torznab-stub.clustarr-system.svc/dl/fixture-search-480p.torrent</link>
        <comments>http://torznab-stub.clustarr-system.svc/details/fixture-search-480p</comments>
        <pubDate>Mon, 08 Sep 2025 09:00:00 +0000</pubDate>
        <size>1073741824</size>
        <category>2000</category>
        <category>2030</category>
        <enclosure url="http://torznab-stub.clustarr-system.svc/dl/fixture-search-480p.torrent"
                   length="1073741824" type="application/x-bittorrent"/>
        <torznab:attr name="tmdbid" value="900100"/>
        <torznab:attr name="imdbid" value="tt9000100"/>
        <torznab:attr name="seeders" value="3"/>
        <torznab:attr name="peers" value="4"/>
        <torznab:attr name="infohash" value="1111111111111111111111111111111111111111"/>
        <torznab:attr name="downloadvolumefactor" value="1"/>
        <torznab:attr name="uploadvolumefactor" value="1"/>
      </item>
      <item>
        <title>Fixture.Search.Film.2019.720p.WEBRip.x264-CLUSTARR</title>
        <guid>fixture-search-720p</guid>
        <link>http://torznab-stub.clustarr-system.svc/dl/fixture-search-720p.torrent</link>
        <comments>http://torznab-stub.clustarr-system.svc/details/fixture-search-720p</comments>
        <pubDate>Tue, 09 Sep 2025 09:00:00 +0000</pubDate>
        <size>4294967296</size>
        <category>2000</category>
        <category>2040</category>
        <enclosure url="http://torznab-stub.clustarr-system.svc/dl/fixture-search-720p.torrent"
                   length="4294967296" type="application/x-bittorrent"/>
        <torznab:attr name="tmdbid" value="900100"/>
        <torznab:attr name="imdbid" value="tt9000100"/>
        <torznab:attr name="seeders" value="40"/>
        <torznab:attr name="peers" value="45"/>
        <torznab:attr name="infohash" value="2222222222222222222222222222222222222222"/>
        <torznab:attr name="downloadvolumefactor" value="1"/>
        <torznab:attr name="uploadvolumefactor" value="1"/>
      </item>
      <item>
        <title>Fixture.Search.Film.2019.1080p.BluRay.x264-CLUSTARR</title>
        <guid>fixture-search-1080p</guid>
        <link>http://torznab-stub.clustarr-system.svc/dl/fixture-search-1080p.torrent</link>
        <comments>http://torznab-stub.clustarr-system.svc/details/fixture-search-1080p</comments>
        <pubDate>Wed, 10 Sep 2025 09:00:00 +0000</pubDate>
        <size>8589934592</size>
        <category>2000</category>
        <category>2050</category>
        <enclosure url="http://torznab-stub.clustarr-system.svc/dl/fixture-search-1080p.torrent"
                   length="8589934592" type="application/x-bittorrent"/>
        <torznab:attr name="tmdbid" value="900100"/>
        <torznab:attr name="imdbid" value="tt9000100"/>
        <torznab:attr name="seeders" value="120"/>
        <torznab:attr name="peers" value="130"/>
        <torznab:attr name="infohash" value="3333333333333333333333333333333333333333"/>
        <torznab:attr name="downloadvolumefactor" value="1"/>
        <torznab:attr name="uploadvolumefactor" value="1"/>
      </item>
    </channel>
  </rss>
  ```

  The titles are fixture-owned strings that exist nowhere else in the repo, on
  disk, or in any CR — which is what makes asserting them in `status.results`
  prove the data came through the wire and not from somewhere convenient.
  `pubDate` must be RFC1123Z or `torznab.ParseResults` returns a hard error for
  the whole feed (`release.go`'s `newRelease`), so do not reformat these.

- [ ] **3. Write `test/fixtures/torznabstub/testdata/rss.xml`** — the `t=search`
  feed the RSS poll reads. Exactly one item, and a **different** fixture movie
  from step 2, so the firehose scenario's release can match only the Movie that
  scenario creates:

  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
    <channel>
      <title>Clustarr E2E Fixture Indexer</title>
      <item>
        <title>Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR</title>
        <guid>fixture-firehose-1080p</guid>
        <link>http://torznab-stub.clustarr-system.svc/dl/fixture-firehose-1080p.torrent</link>
        <comments>http://torznab-stub.clustarr-system.svc/details/fixture-firehose-1080p</comments>
        <pubDate>Wed, 10 Sep 2025 12:00:00 +0000</pubDate>
        <size>6442450944</size>
        <category>2000</category>
        <category>2040</category>
        <enclosure url="http://torznab-stub.clustarr-system.svc/dl/fixture-firehose-1080p.torrent"
                   length="6442450944" type="application/x-bittorrent"/>
        <torznab:attr name="tmdbid" value="900101"/>
        <torznab:attr name="imdbid" value="tt9000101"/>
        <torznab:attr name="seeders" value="88"/>
        <torznab:attr name="peers" value="95"/>
        <torznab:attr name="infohash" value="4444444444444444444444444444444444444444"/>
        <torznab:attr name="downloadvolumefactor" value="1"/>
        <torznab:attr name="uploadvolumefactor" value="1"/>
      </item>
    </channel>
  </rss>
  ```

  **Why a separate movie and not Inception (27205).** `TestLibraryRescan`
  already creates a monitored Movie for tmdbID 27205 against the permissive
  single-tier `e2e-any` profile. A firehose release carrying `tmdbid=27205`
  would match *that* Movie too, and because `e2e-any` has one tier every
  release is top-tier, so `grab.Bypasses` (`BypassIfHighestQuality` defaults
  **true**) fires and `performGrab` creates a real `Download` against a Phase C
  scenario's object. Scenario 17 would then be silently mutating scenario 7.
  A fixture-owned id that no other scenario uses removes the interaction
  entirely.

  The `2040` category is what `rss.KindForCategories` (correction C3) must map
  to `movie`; `2000` is the newznab top the subject's last token comes from.

- [ ] **4. Write the two degenerate documents.**
  `test/fixtures/torznabstub/testdata/empty.xml`:

  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
    <channel><title>Clustarr E2E Fixture Indexer</title></channel>
  </rss>
  ```

  `test/fixtures/torznabstub/testdata/error_100.xml` — Newznab's convention is
  an `<error>` element under **HTTP 200** (`pkg/torznab/client.go`'s
  `readOrError`), so serving a bare 401 would exercise a path real indexers do
  not use:

  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <error code="100" description="Incorrect user credentials"/>
  ```

- [ ] **5. Add two fixture-owned TMDB movies.** Copy
  `testdata/metadata/tmdb/movie_27205.json` to
  `test/fixtures/tmdbstub/testdata/movie_900100.json` and `movie_900101.json`,
  editing **only** `id`, `title`, `original_title`, `imdb_id`, `release_date`
  and `overview` — every other field stays exactly as recorded, so
  `golang-tmdb` parses the same shape it parses for a real movie:

  | file | `id` | `title` | `imdb_id` | `release_date` |
  | --- | --- | --- | --- | --- |
  | `movie_900100.json` | `900100` | `Fixture Search Film` | `tt9000100` | `2019-03-01` |
  | `movie_900101.json` | `900101` | `Fixture Firehose Film` | `tt9000101` | `2019-05-01` |

  This is the precedent `test/fixtures/tvdbstub` already set with series
  900001/900002 — fixture-owned ids, embedded in the binary, beside the
  recorded ones.

- [ ] **6. Serve them from `test/fixtures/tmdbstub/server.go`.** Two embeds and
  two routes, following the file's existing `serveBytes`/`serveFile` split (add
  `serveBytes` to this package; `tvdbstub` already has the identical helper):

  ```go
  //go:embed testdata/movie_900100.json
  var movie900100 []byte

  //go:embed testdata/movie_900101.json
  var movie900101 []byte
  ```

  ```go
  // Fixture-owned movies: ids no recorded fixture uses, so a scenario can
  // own a Movie outright instead of sharing Inception with every other
  // scenario that plants tmdb-27205.
  mux.HandleFunc("GET /movie/900100", serveBytes(movie900100))
  mux.HandleFunc("GET /movie/900101", serveBytes(movie900101))
  ```

  A movie whose metadata never resolves is not merely cosmetic here: the RSS
  matcher only proceeds for a monitored, **available** item, and
  `movie.Availability` returns "never available" when no release date is known
  (`known == false`). The firehose scenario also sets
  `minimumAvailability: announced` as a second, independent guarantee — see
  step 22 — but a Movie stuck at `MetadataReady=False` is a bad object to hang
  three assertions on regardless.

##### Part B — the fixture server

- [ ] **7. Write `test/fixtures/torznabstub/reqlog.go`.** The request log is the
  only channel through which the host-side test can learn what the fixture was
  asked, and it is what forces the live path (see step 19's rationale).

  ```go
  // Package torznabstub ... (GPL-3.0 header from hack/boilerplate.go.txt)
  package torznabstub

  import (
      "encoding/json"
      "net/http"
      "os"
      "path/filepath"
      "strings"
      "sync"
      "time"
  )

  // maxLogBytes caps the request log. The readiness probe alone writes a line
  // every few seconds for the life of the cluster, and an e2e run that is
  // re-executed with E2E_SKIP_BUILD=1 keeps appending to the same file, so the
  // log is truncated rather than allowed to grow without bound. Scenarios only
  // ever read entries newer than their own start time, so losing old ones
  // costs nothing.
  const maxLogBytes = 4 << 20

  // Entry is one line of the JSONL request log. The e2e suite decodes it from
  // the host side of the /data mount: it cannot reach this Service, so this
  // file is the only evidence that the fixture was contacted on THIS run.
  type Entry struct {
      At     time.Time `json:"at"`
      Path   string    `json:"path"`
      Query  string    `json:"query"`
      T      string    `json:"t"`
      Status int       `json:"status"`
  }

  // reqLog appends Entries to a file on the shared /data volume.
  type reqLog struct {
      mu   sync.Mutex
      path string
  }

  func newReqLog(path string) *reqLog { return &reqLog{path: path} }

  // record appends one entry. A probe request is skipped: kubelet hits the
  // caps route every few seconds, and those lines would drown the handful a
  // scenario actually cares about.
  func (l *reqLog) record(r *http.Request, status int) {
      if strings.HasPrefix(r.UserAgent(), "kube-probe/") {
          return
      }
      if l.path == "" {
          return
      }
      e := Entry{
          At:     time.Now().UTC(),
          Path:   r.URL.Path,
          Query:  r.URL.RawQuery,
          T:      r.URL.Query().Get("t"),
          Status: status,
      }
      b, err := json.Marshal(e)
      if err != nil {
          return
      }
      l.mu.Lock()
      defer l.mu.Unlock()
      if err := os.MkdirAll(filepath.Dir(l.path), 0o777); err != nil {
          return
      }
      if fi, err := os.Stat(l.path); err == nil && fi.Size() > maxLogBytes {
          _ = os.Truncate(l.path, 0)
      }
      f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
      if err != nil {
          return
      }
      defer func() { _ = f.Close() }()
      _, _ = f.Write(append(b, '\n'))
  }
  ```

  Every failure path here is swallowed on purpose. A stub that crashes because
  it could not write a diagnostic line turns a missing directory into a
  CrashLoopBackOff and a twenty-minute mystery; the *scenario* is what fails
  loudly when the log is absent, and it says why.

- [ ] **8. Write `test/fixtures/torznabstub/server.go`.** One mux, three
  personalities selected by path, everything embedded:

  ```go
  // Package torznabstub serves a real Torznab upstream -- a real t=caps
  // document and real <rss><channel><item> result feeds, byte for byte the
  // XML in testdata/, parsed by pkg/torznab's own ParseCaps/ParseResults.
  // It never reaches the Internet; the e2e cluster has no egress.
  //
  // Three personalities, selected by path, so one Deployment covers every
  // shape scenario 17 needs:
  //
  //   /api       the healthy indexer. Requires apikey; dispatches on t.
  //   /api/down  always HTTP 500, for the escalation leg.
  //
  // /api is the DEFAULT Indexer.spec.generic.apiPath, so the happy path
  // exercises the default; /api/down is reachable only if indexarr actually
  // joins apiPath onto baseURL, which is what makes step 25's assertion on
  // the logged path meaningful.
  package torznabstub
  ```

  ```go
  import (
      _ "embed"
      "log/slog"
      "net/http"
  )

  //go:embed testdata/caps.xml
  var capsXML []byte

  //go:embed testdata/movie_900100.xml
  var movie900100XML []byte

  //go:embed testdata/rss.xml
  var rssXML []byte

  //go:embed testdata/empty.xml
  var emptyXML []byte

  //go:embed testdata/error_100.xml
  var error100XML []byte

  // APIKey is the key the healthy personality demands. It matches the
  // apikey entry in config/e2e/torznab-stub.yaml's Secret; an Indexer whose
  // secretRef never reached the client gets Newznab error 100, not results,
  // so credential plumbing cannot pass by being ignored.
  const APIKey = "e2e-fixture-key"

  // NewHandler builds the stub. logPath is a file on the shared /data volume
  // that every non-probe request is appended to.
  func NewHandler(logPath string, logger *slog.Logger) http.Handler {
      lg := newReqLog(logPath)
      mux := http.NewServeMux()
      mux.HandleFunc("GET /api", api(lg, logger))
      mux.HandleFunc("GET /api/down", down(lg, logger))
      mux.HandleFunc("/", notFound(lg, logger))
      return mux
  }

  // api is the healthy personality.
  func api(lg *reqLog, logger *slog.Logger) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          q := r.URL.Query()
          if q.Get("apikey") != APIKey {
              logger.Warn("torznabstub: bad or missing apikey", "query", r.URL.RawQuery)
              // Newznab answers a credential failure with HTTP 200 and an
              // <error> element (docs/research/indexers.md §4.5), which is
              // what pkg/torznab's readOrError looks for.
              write(w, lg, r, http.StatusOK, error100XML)
              return
          }
          switch q.Get("t") {
          case "caps":
              write(w, lg, r, http.StatusOK, capsXML)
          case "movie":
              if q.Get("tmdbid") == "900100" || q.Get("imdbid") == "9000100" {
                  write(w, lg, r, http.StatusOK, movie900100XML)
                  return
              }
              // A real indexer answers an unknown id with an empty feed, not
              // an error, and pkg/torznab must parse that as zero releases.
              write(w, lg, r, http.StatusOK, emptyXML)
          case "search":
              // t=search with no q is the RSS poll.
              write(w, lg, r, http.StatusOK, rssXML)
          case "tvsearch":
              write(w, lg, r, http.StatusOK, emptyXML)
          default:
              logger.Warn("torznabstub: unsupported function", "t", q.Get("t"), "query", r.URL.RawQuery)
              write(w, lg, r, http.StatusOK, error100XML)
          }
      }
  }

  // down always fails. It is the escalation leg's driver: every RSS poll and
  // every search fan-out against it errors, so indexarr's RecordFailure path
  // runs against a real HTTP failure rather than a simulated one.
  func down(lg *reqLog, logger *slog.Logger) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          logger.Info("torznabstub: failing on purpose", "query", r.URL.RawQuery)
          lg.record(r, http.StatusInternalServerError)
          http.Error(w, "fixture indexer is down on purpose", http.StatusInternalServerError)
      }
  }

  func write(w http.ResponseWriter, lg *reqLog, r *http.Request, status int, body []byte) {
      lg.record(r, status)
      w.Header().Set("Content-Type", "application/xml; charset=utf-8")
      w.WriteHeader(status)
      _, _ = w.Write(body)
  }

  // notFound logs every unrecognised route, for the same reason tmdbstub and
  // tvdbstub do: "nobody serves that endpoint" must show up as a line in the
  // stub's log, not as a twenty-minute timeout in a scenario.
  func notFound(lg *reqLog, logger *slog.Logger) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          logger.Warn("torznabstub: unrecognised route", "method", r.Method,
              "path", r.URL.Path, "query", r.URL.RawQuery)
          lg.record(r, http.StatusNotFound)
          http.Error(w, "no fixture for this route", http.StatusNotFound)
      }
  }
  ```

  Note `http.Error` writes `text/plain`; `readOrError` sees a non-2xx with no
  `<error>` body and returns `torznab: unexpected status 500`, which is exactly
  what a real dead indexer produces.

- [ ] **9. Write `test/fixtures/torznabstub_cmd.go`**, copying
  `tvdbstub_cmd.go`'s shape exactly — same cobra construction, same
  `ReadHeaderTimeout`, same JSON slog handler:

  ```go
  func newTorznabStubCommand() *cobra.Command {
      var (
          addr    string
          logPath string
      )
      cmd := &cobra.Command{
          Use:   "torznab-stub",
          Short: "Serve the fixture Torznab indexer on --addr",
          RunE: func(cmd *cobra.Command, args []string) error {
              logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
              logger.Info("torznab-stub: listening", "addr", addr, "request_log", logPath)
              srv := &http.Server{
                  Addr:              addr,
                  Handler:           torznabstub.NewHandler(logPath, logger),
                  ReadHeaderTimeout: 10 * time.Second,
              }
              if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
                  return err
              }
              return nil
          },
      }
      cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
      cmd.Flags().StringVar(&logPath, "request-log", "/data/.e2e-fixtures/torznab/requests.jsonl",
          "JSONL file on the shared /data volume that every non-probe request is appended to")
      return cmd
  }
  ```

  Then add `root.AddCommand(newTorznabStubCommand())` to
  `test/fixtures/root.go` and strike `torznab-stub` from that file's
  "Phase D adds …" comment.

- [ ] **10. Write `test/fixtures/torznabstub/server_test.go`** — a plain unit
  test, no build tag, that runs in `make test`:

  ```go
  // TestEmbeddedDocumentsParse is the fixture's own guard rail: every
  // document this stub serves is parsed here by the SAME pkg/torznab
  // functions indexarr will use, so a typo in the XML fails in `make test`
  // instead of twenty minutes into `hack/e2e.sh` as an Indexer that never
  // goes Ready.
  func TestEmbeddedDocumentsParse(t *testing.T) {
      caps, err := torznab.ParseCaps(bytes.NewReader(capsXML))
      require.NoError(t, err)
      require.True(t, caps.Supports(torznab.ModeMovieSearch, "tmdbid"))
      require.True(t, caps.Supports(torznab.ModeTVSearch, "tvdbid"))
      require.False(t, caps.Modes[torznab.ModeMusicSearch].Available)
      require.Equal(t, 100, caps.LimitsMax)
      require.Equal(t, "raw", caps.Modes[torznab.ModeSearch].SearchEngine)

      movies, err := torznab.ParseResults(bytes.NewReader(movie900100XML))
      require.NoError(t, err)
      require.Len(t, movies, 3)
      require.Equal(t, "Fixture.Search.Film.2019.480p.DVDRip.XviD-CLUSTARR", movies[0].Title,
          "the feed is deliberately worst-first; RankAndCap is what must reorder it")
      for _, rel := range movies {
          require.Equal(t, "900100", rel.IDs[commonv1.IDKeyTMDB])
          require.NotZero(t, rel.Size)
          require.NotNil(t, rel.Seeders)
          require.False(t, rel.PubDate.IsZero(), "a malformed pubDate fails the WHOLE feed")
      }

      rss, err := torznab.ParseResults(bytes.NewReader(rssXML))
      require.NoError(t, err)
      require.Len(t, rss, 1)
      require.Equal(t, "900101", rss[0].IDs[commonv1.IDKeyTMDB])

      empty, err := torznab.ParseResults(bytes.NewReader(emptyXML))
      require.NoError(t, err)
      require.Empty(t, empty)

      e, err := torznab.ParseError(bytes.NewReader(error100XML))
      require.NoError(t, err)
      require.NotNil(t, e)
      require.Equal(t, 100, e.Code)
  }
  ```

  Also add a `httptest`-based `TestHandlerRoutes` covering: no apikey → error
  100; `t=caps` → the caps document; `t=movie&tmdbid=900100` → three items;
  `t=movie&tmdbid=999` → empty; `t=search` → one item; `/api/down` → 500; and
  that a `kube-probe/1.33` User-Agent writes **no** log line while a normal one
  writes exactly one.

- [ ] **11. Create the request-log directory in `test/fixtures/seed/seed.go`.**
  The stub pod runs as uid/gid 1000 against a hostPath directory owned by
  whoever ran `hack/kind.sh`, and hostPath ignores `fsGroup`. `hack/e2e.sh`
  already runs `seed` as the *host* user, so this is the one place that can
  create a directory the pod can write to, and `0o777` removes the dependency
  on the host's gid being 1000 (which `ensure_data_dir`'s `chmod g+rwX` quietly
  assumes today):

  ```go
  // TorznabDirName is the subdirectory of the seed directory that the
  // torznab-stub appends its request log to. It is created world-writable
  // here, by the HOST user, because the stub pod runs as uid/gid 1000 and a
  // hostPath mount ignores fsGroup -- there is no other moment at which a
  // process with the right identity touches this path.
  const TorznabDirName = "torznab"

  // RequestLogName is the JSONL file inside it.
  const RequestLogName = "requests.jsonl"
  ```

  and, at the end of `Run`, after the clip is written:

  ```go
  reqDir := filepath.Join(dir, TorznabDirName)
  if err := os.MkdirAll(reqDir, 0o777); err != nil {
      return fmt.Errorf("seed: mkdir %s: %w", reqDir, err)
  }
  // MkdirAll applies the process umask, which under §11's UMASK 002 clears
  // the other-write bit that the stub pod depends on. Chmod does not.
  if err := os.Chmod(reqDir, 0o777); err != nil {
      return fmt.Errorf("seed: chmod %s: %w", reqDir, err)
  }
  ```

##### Part C — deploying it

- [ ] **12. Write `config/e2e/torznab-stub.yaml`** — Secret, Deployment,
  Service, following `tmdb-stub.yaml` exactly except for the `/data` mount and
  the uid:

  ```yaml
  # The in-cluster fixture indexer: a real Torznab upstream serving the XML
  # embedded in test/fixtures/torznabstub/testdata, and nothing else. The
  # readiness probe is a real caps fetch WITH the apikey, so a fixture image
  # built without the embedded XML -- or with a key that no longer matches the
  # Secret below -- never goes Ready instead of failing scenario 17 ten
  # minutes later.
  apiVersion: v1
  kind: Secret
  metadata:
    name: torznab-fixture-credentials
    namespace: clustarr-system
  stringData:
    # Must equal torznabstub.APIKey. The stub answers Newznab error 100 for
    # anything else, so a mismatch surfaces as an Indexer that authenticates
    # and never becomes Ready -- which is the correct diagnosis.
    apikey: e2e-fixture-key
  ---
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: torznab-stub
    namespace: clustarr-system
    labels:
      app.kubernetes.io/name: clustarr
      app.kubernetes.io/part-of: clustarr
      app.kubernetes.io/component: torznab-stub
  spec:
    replicas: 1
    selector:
      matchLabels:
        app.kubernetes.io/component: torznab-stub
    template:
      metadata:
        labels:
          app.kubernetes.io/name: clustarr
          app.kubernetes.io/part-of: clustarr
          app.kubernetes.io/component: torznab-stub
      spec:
        securityContext:
          runAsNonRoot: true
          # 1000/1000, not the image's default 65532: this pod WRITES its
          # request log onto the shared hostPath volume, and it must do so as
          # the same identity every other Clustarr pod uses there
          # (config/manager/*.yaml, hack/kind.sh's ensure_data_dir).
          runAsUser: 1000
          runAsGroup: 1000
          seccompProfile:
            type: RuntimeDefault
        containers:
        - name: torznab-stub
          image: ghcr.io/mediactl/clustarr-e2e-fixtures:dev
          imagePullPolicy: IfNotPresent
          args: ["torznab-stub", "--addr=:8080", "--request-log=/data/.e2e-fixtures/torznab/requests.jsonl"]
          ports:
          - name: http
            containerPort: 8080
            protocol: TCP
          readinessProbe:
            httpGet:
              path: /api?t=caps&apikey=e2e-fixture-key
              port: http
            initialDelaySeconds: 1
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /api?t=caps&apikey=e2e-fixture-key
              port: http
            initialDelaySeconds: 5
            periodSeconds: 20
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 64Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          volumeMounts:
          - name: data
            mountPath: /data
        volumes:
        - name: data
          persistentVolumeClaim:
            # The same RWX hostPath claim importarr-worker mounts; it is the
            # host directory the e2e test binary reads as $CLUSTARR_DATA_DIR.
            claimName: clustarr-data
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: torznab-stub
    namespace: clustarr-system
    labels:
      app.kubernetes.io/name: clustarr
      app.kubernetes.io/part-of: clustarr
      app.kubernetes.io/component: torznab-stub
  spec:
    type: ClusterIP
    selector:
      app.kubernetes.io/component: torznab-stub
    ports:
    - name: http
      port: 80
      targetPort: http
      protocol: TCP
  ```

  `resources.requests` must be present or `config/e2e/resources-patch.yaml`'s
  `replace` operations fail on this Deployment — that patch targets
  `kind: Deployment` with no name selector, so it lands here too.

- [ ] **13. Write `config/e2e/indexarr-e2e-patch.yaml`.** One env var, with the
  reason in the file so nobody deletes it as noise:

  ```yaml
  # indexer.StartupGrace is 15 minutes from PROCESS START, and it suppresses
  # escalation for failures inside that window (ruling R17). `make e2e` has a
  # 30-minute budget, and how old indexarr's process is when scenario 17 runs
  # depends entirely on how long the scenarios before it took -- so with the
  # production grace the backoff assertions pass or fail for reasons unrelated
  # to the code under test. Zero here; 15m everywhere else.
  - op: add
    path: /spec/template/spec/containers/0/env/-
    value:
      name: CLUSTARR_INDEXER_STARTUP_GRACE
      value: "0s"
  ```

- [ ] **14. Wire both into `config/e2e/kustomization.yaml`** — one resource
  line and one targeted patch, which is exactly the "add one line without
  editing anything else" the file's own comment promised:

  ```yaml
  resources:
  - ../default
  - tmdb-stub.yaml
  - tvdb-stub.yaml
  - torznab-stub.yaml
  - quality-profile.yaml
  - metadata-providers.yaml

  patches:
  - target:
      kind: Deployment
    path: resources-patch.yaml
  - target:
      kind: Deployment
      name: indexarr
    path: indexarr-e2e-patch.yaml
  ```

- [ ] **15. Extend `hack/e2e.sh`** — two edits, nothing else. Add
  `deployment/torznab-stub` to `WORKLOADS` directly after
  `deployment/tvdb-stub` (stubs first, so a broken fixture image is still the
  cheapest failure to read), and add one block to the diagnostics section,
  after the `resources.yaml` loop:

  ```bash
  # The fixture indexer's request log is the only record of what indexarr
  # actually ASKED it. Without it, "the Search returned nothing" cannot be
  # told apart from "indexarr never issued a query", and by the time anyone
  # looks the cluster is usually gone.
  if [[ -f "${CLUSTARR_DATA_DIR}/.e2e-fixtures/torznab/requests.jsonl" ]]; then
    cp "${CLUSTARR_DATA_DIR}/.e2e-fixtures/torznab/requests.jsonl" \
       "${ARTIFACTS_DIR}/torznab-requests.jsonl" 2>/dev/null || true
  fi
  ```

##### Part D — the suite's shared pieces

- [ ] **16. Add the refusal gate to `test/e2e/main_test.go`.** Extend
  `checkDataDir` — the same function that already refuses a missing or
  under-threshold clip — rather than adding a second gate:

  ```go
  // The torznab-stub appends its request log here, and scenario 17 reads it
  // from the host side to prove the fixture was contacted on THIS run. The
  // directory is created by `clustarr-e2e-fixtures seed`, as the host user,
  // because the stub pod cannot create it: it runs as uid/gid 1000 against a
  // hostPath directory it does not own. A fixture image predating that
  // change leaves this absent, which would otherwise surface as an empty
  // request log and an assertion nobody can explain.
  reqDir := filepath.Join(dir, fixtureDirName, seed.TorznabDirName)
  if _, err := os.Stat(reqDir); err != nil {
      return fmt.Errorf("the torznab request-log directory %q is missing "+
          "(rebuild the fixture image: `clustarr-e2e-fixtures seed` creates it): %w", reqDir, err)
  }
  ```

- [ ] **17. Add the derived timeouts to `test/e2e/helpers_test.go`.** Every one
  of these is derived from the ladder it races, in the style
  `scanCompletedTimeout` established. Do not round them down, and do not copy
  a number from a neighbouring constant because it looks similar.

  ```go
  // indexerReadyTimeout covers an Indexer's first reconcile: one caps fetch
  // against a Service on the same node. It races no redelivery ladder at all
  // -- the reconcile is a watch-driven controller-runtime reconcile, not a
  // queue task -- so the only things to cover are the fixture pod's own
  // readiness (probe period 5s, up to a few periods on a loaded node),
  // indexarr's per-request Timeout (Indexer.spec.timeout, default 30s) and a
  // controller-runtime rate-limited requeue after a first failure (the
  // default workqueue starts at 5ms and doubles, so two failures cost
  // milliseconds, not minutes). Two minutes is roughly four times the worst
  // realistic case and short enough that a genuinely broken fixture is
  // reported before the suite has burned its budget.
  const indexerReadyTimeout = 2 * time.Minute

  // searchCompletedTimeout is bounded by the CONTROLLER, not by the queue:
  // catalogarr/controller/search's SearchRunningTimeout (5 minutes) fails a
  // Search that has sat in Running that long, so no wait past it can ever
  // observe a Completed that was not already going to arrive.
  //
  // Within that window the task rides ConsumerCatalogSearchHigh (AckWait
  // 120s, MaxDeliver 5, BackOff 30s/2m/10m):
  //
  //   attempt 1 delivered at   0s, AckWait expires at 120s, backoff 30s
  //   attempt 2 delivered at 150s, AckWait expires at 270s, backoff  2m
  //   attempt 3 delivered at 390s  -- already past SearchRunningTimeout
  //
  // so one lost ack is survivable and two are not, by construction. Six
  // minutes clears SearchRunningTimeout plus the reconciler's own requeue
  // and this suite's 2s poll; it deliberately does NOT reach for attempt 3,
  // which the controller has already given a verdict on.
  const searchCompletedTimeout = 6 * time.Minute

  // firehoseTimeout covers a release travelling indexarr's RSS poll ->
  // CLUSTARR_RELEASES -> catalogarr's rss-matcher -> a status write. It
  // races TWO ladders in series.
  //
  // ConsumerIndexRSS (after ruling R7: AckWait 60s, Heartbeat 30s,
  // MaxDeliver 4, BackOff 1m/5m/15m):
  //   attempt 1 at   0s, expires  60s, backoff 1m -> attempt 2 at 120s
  //
  // ConsumerCatalogRSSMatcher (AckWait 30s, MaxDeliver 6, BackOff
  // 1s/5s/30s/2m/10m), measured from the release's publish:
  //   attempt 1 at   0s   attempt 2 at  31s   attempt 3 at  66s
  //   attempt 4 at 126s   attempt 5 at 276s   attempt 6 at 906s
  //
  // Covering one lost RSS delivery (120s) plus the matcher's FIFTH delivery
  // (276s) plus the decide-and-apply round trip and this suite's 2s poll is
  // 120 + 276 + ~20 = 416s. Seven minutes sits past that and far short of
  // the matcher's sixth attempt at 120 + 906 = 1026s, which would exceed
  // every per-scenario context here.
  //
  // It equals scanCompletedTimeout by coincidence, not by copying: that one
  // is derived from ConsumerImportScan. Changing either ladder changes only
  // its own constant.
  const firehoseTimeout = 7 * time.Minute

  // escalationTimeout covers driving a failing indexer far enough up
  // Prowlarr's backoff ladder to be disabled. The driver is the RSS poll, at
  // the scenario's rssInterval of 1 minute, and the ladder's first step may
  // be a zero-length disable, so budget three failing polls (180s). One of
  // those polls can lose its delivery and come back on ConsumerIndexRSS's
  // second attempt, 120s after its schedule; with the apply and this suite's
  // 2s poll that is ~320s. Six minutes sits past it and short of that
  // consumer's third delivery at 480s.
  const escalationTimeout = 6 * time.Minute

  // quietWindow is how long scenario 17 watches a disabled indexer's request
  // log for a query that must not arrive. It is two rssInterval periods: a
  // worker that ignored the backoff would poll within one, and a second
  // covers a poll whose delivery slipped. A longer window proves nothing
  // extra and only eats the scenario budget.
  const quietWindow = 2 * time.Minute
  ```

- [ ] **18. Add the request-log reader to `test/e2e/helpers_test.go`.** This is
  the second channel, and the whole answer to "could this assertion pass from
  stale state":

  ```go
  // torznabRequestsSince returns every request the fixture indexer logged at
  // or after t0. It is the ONLY way this suite can observe what indexarr
  // asked the indexer: the test process cannot reach a ClusterIP Service, so
  // the fixture writes onto the shared /data volume instead.
  //
  // The since filter is what makes an assertion live rather than merely
  // consistent. Phase C's lesson was that scaling a stub to zero was NOT
  // enough to prove a metadata assertion was hitting it -- a warm cache
  // served the same answer -- and only forcing cold caches distinguished the
  // two. Here there is no equivalent doubt to resolve by deletion: a request
  // logged after the scenario started can only have been issued during this
  // run. Both clocks are the host kernel's (kind's node shares it), so the
  // comparison is sound without any clock-skew allowance.
  func torznabRequestsSince(t *testing.T, t0 time.Time) []torznabstub.Entry {
      t.Helper()
      path := filepath.Join(dataDir(), fixtureDirName, seed.TorznabDirName, seed.RequestLogName)
      f, err := os.Open(path)
      if err != nil {
          if errors.Is(err, os.ErrNotExist) {
              return nil // the stub has not been asked anything yet
          }
          t.Fatalf("torznabRequestsSince: open %s: %v", path, err)
      }
      defer func() { _ = f.Close() }()

      var out []torznabstub.Entry
      sc := bufio.NewScanner(f)
      sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
      for sc.Scan() {
          line := bytes.TrimSpace(sc.Bytes())
          if len(line) == 0 {
              continue
          }
          var e torznabstub.Entry
          if err := json.Unmarshal(line, &e); err != nil {
              // A torn final line is expected: the stub appends while this
              // reads. Skipping it is right; failing on it would be flaky.
              continue
          }
          if !e.At.Before(t0) {
              out = append(out, e)
          }
      }
      return out
  }

  // describeTorznabRequests renders the fixture's request log for a failure
  // message. hack/e2e.sh copies the whole file into test/e2e/artifacts, but
  // that happens after `go test` returns; a wait that fails needs the
  // evidence inline, in the message, on the machine where it failed.
  func describeTorznabRequests(t0 time.Time) func() string {
      return func() string {
          es := torznabRequestsSince(nil, t0) //nolint:staticcheck // read-only, never fails the test
          if len(es) == 0 {
              return "the fixture indexer logged NO request since this scenario started " +
                  "-- indexarr never contacted it (check the indexarr logs and the Indexer's conditions)"
          }
          out := fmt.Sprintf("fixture indexer saw %d request(s) since this scenario started:", len(es))
          for _, e := range es {
              out += fmt.Sprintf("\n    %s %s?%s -> %d", e.At.Format(time.RFC3339), e.Path, e.Query, e.Status)
          }
          return out
      }
  }
  ```

  (`torznabRequestsSince` takes `*testing.T` and must tolerate `nil` for the
  describe path, or split the body into an error-returning helper the two share
  — do the latter; it is cleaner than a nil check on `t`.)

- [ ] **19. Add the resource constructors to `test/e2e/helpers_test.go`** —
  `newIndexer`, `newRankedQualityProfile`, `newDelayProfile`, each registering
  `cleanupUnlessFailed`, in the style of `newRootFolder`:

  ```go
  // newIndexer creates a generic Torznab Indexer pointed at the in-cluster
  // fixture and registers its cleanup. apiPath selects the fixture's
  // personality: "" takes Indexer.spec.generic.apiPath's own default of
  // "/api" (the healthy one), "/api/down" the failing one.
  //
  // The object NAME matters beyond uniqueness here, and uniqueName supplies
  // what is needed: it is the second token of the release subject, the second
  // segment of the firehose envelope key, and the first half of
  // events.MsgIDForRelease -- which CLUSTARR_RELEASES dedups on for two
  // hours. A fixed name would mean a rerun inside that window silently
  // publishing nothing, and the firehose scenario waiting out its whole
  // timeout for a release the broker had already suppressed.
  func newIndexer(ctx context.Context, t *testing.T, prefix, apiPath string, rssInterval time.Duration, enableRss bool) *indexv1alpha1.Indexer {
      t.Helper()
      idx := &indexv1alpha1.Indexer{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
          Spec: indexv1alpha1.IndexerSpec{
              BaseURL: "http://torznab-stub.clustarr-system.svc",
              Generic: &indexv1alpha1.GenericNewznab{
                  Protocol: commonv1.ProtocolTorrent,
                  APIPath:  apiPath,
              },
              SecretRef:   &corev1.LocalObjectReference{Name: "torznab-fixture-credentials"},
              EnableRss:   ptr.To(enableRss),
              RssInterval: metav1.Duration{Duration: rssInterval},
              // 2s is the CRD default and would pace three fan-out requests
              // across six seconds for no reason against a local fixture.
              RequestDelay: metav1.Duration{Duration: 100 * time.Millisecond},
              Priority:     25,
          },
      }
      require.NoError(t, k8sClient.Create(ctx, idx))
      cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), idx) })
      return idx
  }

  // describeIndexer renders one Indexer's resolved fields, escalation and
  // conditions for a failure message. An Indexer that never goes healthy has
  // almost always stalled on one condition, and naming it is the difference
  // between a diagnosis and a shrug.
  func describeIndexer(key client.ObjectKey) func() string {
      return func() string {
          var live indexv1alpha1.Indexer
          if err := k8sClient.Get(context.Background(), key, &live); err != nil {
              return fmt.Sprintf("Indexer %s could not be read back: %v", key.Name, err)
          }
          modes := "<no status.caps>"
          if live.Status.Caps != nil {
              keys := make([]string, 0, len(live.Status.Caps.Modes))
              for k := range live.Status.Caps.Modes {
                  keys = append(keys, k)
              }
              sort.Strings(keys)
              modes = strings.Join(keys, ",")
          }
          out := fmt.Sprintf("Indexer %s protocol=%q privacy=%q capsModes=[%s] escalationLevel=%d disabledUntil=%v lastRssAt=%v lastRssNewCount=%d indexedReleases=%d lastFailure=%q",
              key.Name, live.Status.Protocol, live.Status.Privacy, modes,
              live.Status.EscalationLevel, live.Status.DisabledUntil,
              live.Status.LastRssAt, live.Status.LastRssNewCount,
              live.Status.IndexedReleases, live.Status.LastFailure)
          for _, c := range live.Status.Conditions {
              out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
          }
          return out
      }
  }
  ```

  Add `describeSearch(key)` in the same shape, printing `phase`,
  `startedAt`/`finishedAt`, every `indexerOutcome` (name, state, count,
  durationMs, error) and `len(results)`. **`indexerOutcomes` is the field that
  diagnoses a failed search**, and it is the first thing a reader needs.

  `newRankedQualityProfile(ctx, t, prefix, tiers)` creates a **cluster-scoped**
  `QualityProfile` (no namespace) with `builtIn: false`, and
  `newDelayProfile(ctx, t, prefix, torrentDelayMinutes)` creates a namespaced
  `DelayProfile` with `bypassIfHighestQuality: false` — both registering
  `cleanupUnlessFailed`.

##### Part E — the scenario

Everything below goes in `test/e2e/indexer_test.go`, with the `//go:build e2e`
tag, the GPL-3.0 header, and a package comment naming scenario 17.

- [ ] **20. `TestIndexerHealthAndCaps`** — assertion 1.

  ```go
  // TestIndexerHealthAndCaps is scenario 17's first leg: an Indexer pointed
  // at the in-cluster fixture reconciles to healthy off a REAL capabilities
  // fetch.
  func TestIndexerHealthAndCaps(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
      defer cancel()
      t0 := time.Now()

      // apiPath "" takes the CRD default "/api", which is the fixture's
      // healthy personality; enableRss false keeps this leg from publishing
      // onto the firehose and racing TestIndexerReleaseFirehose.
      idx := newIndexer(ctx, t, "e2e-idx-caps", "", 15*time.Minute, false)

      var live indexv1alpha1.Indexer
      waitFor(t, ctx, indexerReadyTimeout, "Indexer "+idx.Name+" Ready with caps",
          func(ctx context.Context) (bool, error) {
              if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(idx), &live); err != nil {
                  //nolint:nilerr // keep polling
                  return false, nil
              }
              return live.Status.Caps != nil &&
                  isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerConditionReady) &&
                  isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerConditionHealthy), nil
          }, describeIndexer(client.ObjectKeyFromObject(idx)), describeTorznabRequests(t0))

      // Authenticated, separately from Ready: the fixture answers Newznab
      // error 100 without the apikey, so this condition being True is the
      // only proof that spec.secretRef was read and forwarded. If the Secret
      // plumbing regressed, caps would be nil and the wait above would time
      // out with "unrecorded route"/error-100 lines in the request log.
      require.True(t, isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerConditionAuthenticated))

      // Resolved from spec.generic, not guessed. A regression here reads as
      // an empty column in `kubectl get indexers` and an indexer that the
      // protocol-gated parts of the decision engine silently never match.
      require.Equal(t, commonv1.ProtocolTorrent, live.Status.Protocol)
      require.NotEmpty(t, live.Status.Privacy,
          "status.privacy must be resolved for a generic indexer, not left blank")

      // The caps document, field by field, against testdata/caps.xml. These
      // numbers exist in exactly one place in the repo, so they cannot have
      // come from a default.
      caps := live.Status.Caps
      require.EqualValues(t, 100, caps.LimitsMax)
      require.EqualValues(t, 50, caps.LimitsDefault)
      require.True(t, caps.SupportsRawSearch,
          `testdata/caps.xml sets searchEngine="raw" on <search>`)

      // RULING R5, and the single most regression-prone assertion here.
      // Caps.Modes is keyed by torznab.SearchMode's WIRE values, which are
      // what torznab.Caps.Supports and the caps-gating predicate compare
      // against. The CRD's own doc comment used to say "search, tv-search,
      // movie-search", and there is no enum marker, so a caps fetch keyed
      // the wrong way would populate status, look entirely plausible in
      // kubectl, and cause SupportsMode to NEVER match -- every indexer
      // silently skipped, every search empty, nothing logged.
      require.Contains(t, caps.Modes, string(torznab.ModeMovieSearch)) // "movie"
      require.Contains(t, caps.Modes, string(torznab.ModeTVSearch))    // "tvsearch"
      require.NotContains(t, caps.Modes, "movie-search",
          "Caps.Modes is keyed by the wire value, not by the caps ELEMENT name (R5)")
      require.NotContains(t, caps.Modes, "tv-search")
      require.Contains(t, caps.Modes[string(torznab.ModeMovieSearch)], "tmdbid")
      require.Contains(t, caps.Modes[string(torznab.ModeTVSearch)], "tvdbid")

      // The category tree survived the flattening into the CRD's two-level
      // Category/SubCategory shape.
      byID := map[int32]indexv1alpha1.Category{}
      for _, c := range caps.Categories {
          byID[c.ID] = c
      }
      require.Contains(t, byID, int32(2000))
      require.Contains(t, byID, int32(5000))
      var subIDs []int32
      for _, s := range byID[2000].Sub {
          subIDs = append(subIDs, s.ID)
      }
      require.ElementsMatch(t, []int32{2030, 2040, 2050}, subIDs)

      // Live, not cached or stale: a caps request logged by the fixture
      // after this scenario began. The Indexer object is created fresh with
      // a unique name each run, so its status cannot be left over -- but
      // asserting the request closes the door on a future caps cache too,
      // and it is the one assertion that would still hold if someone added
      // one.
      reqs := torznabRequestsSince(t, t0)
      var sawCaps bool
      for _, r := range reqs {
          if r.Path == "/api" && r.T == "caps" && r.Status == http.StatusOK {
              sawCaps = true
          }
      }
      require.True(t, sawCaps,
          "no successful t=caps request reached the fixture during this scenario; requests=%+v", reqs)
  }
  ```

- [ ] **21. `TestIndexerSearchReturnsRankedResults`** — assertion 2, the centre
  of gravity. Set-up half:

  ```go
  // TestIndexerSearchReturnsRankedResults is scenario 17's centre of
  // gravity: the first time catalogarr's search path has had a server on the
  // other end of rpc.indexarr.search. Everything from the Search CR through
  // the snapshot, the RPC, pkg/decision and RankAndCap to status.results runs
  // for real, against real XML off a real HTTP server.
  func TestIndexerSearchReturnsRankedResults(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
      defer cancel()
      t0 := time.Now()

      // A two-tier profile, NOT config/e2e's e2e-any. e2e-any has a single
      // tier, so every quality in it compares EQUAL and the ordering of the
      // two approved releases would fall through to size and seeders -- an
      // assertion that passes for the wrong reason. Two tiers make the
      // ranking a statement about quality, which is what is under test.
      profile := newRankedQualityProfile(ctx, t, "e2e-idx-qp", []tier{
          {name: "Bluray1080", qualities: []string{"Bluray-1080p"}},
          {name: "Web720", qualities: []string{"WEBRip-720p", "WEBDL-720p"}},
      })

      rf := newRootFolder(ctx, t, "e2e-idx-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
      idx := newIndexer(ctx, t, "e2e-idx-search", "", 15*time.Minute, false)
      waitForIndexerReady(ctx, t, idx)

      movie := newMovie(ctx, t, "e2e-idx-movie", 900100, profile.Name, rf.Name,
          catalogv1alpha1.MinimumAvailabilityAnnounced)
      waitForMovieMetadata(ctx, t, movie, "Fixture Search Film")

      // spec.indexerRefs pins the fan-out to this scenario's indexer, so
      // status.indexerOutcomes is deterministic however many Indexers other
      // scenarios have left in the namespace.
      srch := &catalogv1alpha1.Search{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-search"), Namespace: Namespace},
          Spec: catalogv1alpha1.SearchSpec{
              MediaRef:    &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
              IndexerRefs: []string{idx.Name},
              Limit:       50,
              TTL:         metav1.Duration{Duration: time.Hour},
          },
      }
      require.NoError(t, k8sClient.Create(ctx, srch))
      cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), srch) })
  ```

- [ ] **22. `TestIndexerSearchReturnsRankedResults`, assertion half.**

  ```go
      var done catalogv1alpha1.Search
      waitFor(t, ctx, searchCompletedTimeout, "Search "+srch.Name+" Completed",
          func(ctx context.Context) (bool, error) {
              var live catalogv1alpha1.Search
              if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(srch), &live); err != nil {
                  //nolint:nilerr // keep polling
                  return false, nil
              }
              if live.Status.Phase == catalogv1alpha1.SearchPhaseFailed {
                  // Fail immediately rather than waiting out the window: the
                  // controller has already reached a verdict, and its
                  // indexerOutcomes carry the reason.
                  return false, fmt.Errorf("Search %s reached Failed; outcomes=%+v conditions=%+v",
                      live.Name, live.Status.IndexerOutcomes, live.Status.Conditions)
              }
              done = live
              return live.Status.Phase == catalogv1alpha1.SearchPhaseCompleted, nil
          }, describeSearch(client.ObjectKeyFromObject(srch)), describeTorznabRequests(t0))

      // One outcome, for THIS indexer, keyed by the Indexer's OBJECT name.
      //
      // Correction C1: the subject token, the envelope key's second segment
      // and ReleaseInfo.IndexerRef must all be the object name, never the
      // display name. If indexarr used the display name here, this outcome's
      // Name would not match any Indexer and rssmatcher's indexer-priority
      // lookup would silently fall back to the default -- a release that
      // arrives, matches and ranks against a priority resolving to nothing.
      require.Len(t, done.Status.IndexerOutcomes, 1)
      out := done.Status.IndexerOutcomes[0]
      require.Equal(t, idx.Name, out.Name,
          "the outcome must be keyed by the Indexer's object name, not its display name (C1)")
      require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, out.State, "error=%q", out.Error)
      require.EqualValues(t, 3, out.Count, "testdata/movie_900100.xml holds three items")

      // Three results, ranked 1..3, approved before rejected.
      require.Len(t, done.Status.Results, 3)
      for i, r := range done.Status.Results {
          require.EqualValues(t, i+1, r.Rank, "Rank is the 1-based position in the final list")
          require.Equal(t, idx.Name, r.IndexerRef, "every result carries the object name (C1)")
          require.Equal(t, commonv1.ProtocolTorrent, r.Protocol)
      }

      // The ordering assertion, and the reason testdata/movie_900100.xml is
      // deliberately WORST-FIRST. The feed's arrival order is 480p, 720p,
      // 1080p; the ranked order must be the reverse. A decision engine that
      // regressed to a pass-through -- or a fan-out that forwarded the
      // indexer's own ordering -- would put the 480p release at rank 1 and
      // fail here, rather than quietly agreeing with the wire.
      require.Equal(t, "Fixture.Search.Film.2019.1080p.BluRay.x264-CLUSTARR", done.Status.Results[0].Title)
      require.True(t, done.Status.Results[0].Approved)
      require.Equal(t, "Bluray-1080p", done.Status.Results[0].Quality.Name)

      require.Equal(t, "Fixture.Search.Film.2019.720p.WEBRip.x264-CLUSTARR", done.Status.Results[1].Title)
      require.True(t, done.Status.Results[1].Approved)

      // The 480p release is below the profile's lowest tier: present, with a
      // reason, and last. "Present with a reason" is the never-guess
      // invariant in its search form -- a release the engine turned down is
      // reported, not dropped.
      last := done.Status.Results[2]
      require.Equal(t, "Fixture.Search.Film.2019.480p.DVDRip.XviD-CLUSTARR", last.Title)
      require.False(t, last.Approved)
      require.NotEmpty(t, last.Rejections, "a rejected release must say why")

      // Wire fields survived the whole projection: torznab.Release ->
      // rss.ProjectRelease -> schema.Release -> ReleaseInfo -> the CRD.
      top := done.Status.Results[0]
      require.EqualValues(t, 8589934592, top.SizeBytes)
      require.NotNil(t, top.Seeders)
      require.EqualValues(t, 120, *top.Seeders)
      require.Equal(t, "3333333333333333333333333333333333333333", top.InfoHash)
      require.Equal(t, "900100", top.IDs[commonv1.IDKeyTMDB])
      require.NotNil(t, top.PublishedAt, "pubDate must survive as PublishedAt")

      // Live, and id-based. Two separate claims, both from the request log:
      //  - a t=movie request reached the fixture during THIS scenario, so
      //    the results cannot have come from indexarr's local release index
      //    (rpc.indexarr.query's store) or any future cache;
      //  - it carried the provider id, not free text, which is what ruling
      //    R11 says the ids-only caller produces. A regression to q= would
      //    show up here as a query with no tmdbid/imdbid, and the fixture
      //    would have answered the empty feed.
      var movieReqs []torznabstub.Entry
      for _, r := range torznabRequestsSince(t, t0) {
          if r.Path == "/api" && r.T == "movie" {
              movieReqs = append(movieReqs, r)
          }
      }
      require.NotEmpty(t, movieReqs, "indexarr never issued a t=movie query to the fixture")
      var idBased bool
      for _, r := range movieReqs {
          v, err := url.ParseQuery(r.Query)
          require.NoError(t, err)
          if v.Get("tmdbid") == "900100" || v.Get("imdbid") == "9000100" {
              idBased = true
          }
      }
      require.True(t, idBased,
          "the fan-out must query by provider id, not free text; requests=%+v", movieReqs)
  }
  ```

- [ ] **23. `TestIndexerReleaseFirehose`, set-up** — assertion 3. The **order of
  creation is load-bearing** and the comment must say so:

  ```go
  // TestIndexerReleaseFirehose is scenario 17's third leg: a release
  // published by indexarr's RSS worker reaches catalogarr's rss-matcher with
  // a legal envelope key, and the matcher acts on it.
  //
  // It asserts what the MATCHER DID, not what indexarr published. The
  // matcher events.Discard()s -- straight to the DLQ, bypassing MaxDeliver
  // -- on any envelope key it cannot strings.Cut on "/", so a wrong key is
  // silent and invisible: indexarr's own status would still show
  // lastRssNewCount > 0, the stream would still show the message, and
  // nothing anywhere would report a failure. Only a downstream effect
  // distinguishes "published" from "arrived".
  //
  // The effect chosen is Movie.status.pendingGrab, not a Download. A
  // DelayProfile with a 12-hour torrent delay stops grab.Decide before
  // performGrab, so this leg proves the whole path -- decode, key-split into
  // the right namespace, match, resolve, evaluate, delay, apply -- without
  // creating a Download, which is D2's subject and D2's scenario.
  func TestIndexerReleaseFirehose(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
      defer cancel()
      t0 := time.Now()

      // Tiers chosen so the RSS release (WEBDL-1080p) is approved but NOT
      // top-tier. grab.Bypasses skips the delay when BypassIfHighestQuality
      // is set and the release is at index 0, and that flag DEFAULTS TO
      // TRUE. The DelayProfile below turns it off explicitly; this profile
      // makes the assertion hold even if it were left on. Two independent
      // reasons, because a delayed grab is the entire observable.
      profile := newRankedQualityProfile(ctx, t, "e2e-fh-qp", []tier{
          {name: "Bluray1080", qualities: []string{"Bluray-1080p"}},
          {name: "Web1080", qualities: []string{"WEBDL-1080p", "WEBRip-1080p"}},
      })
      delay := newDelayProfile(ctx, t, "e2e-fh-dp", 720 /* minutes */)
      rf := newRootFolder(ctx, t, "e2e-fh-rf", catalogv1alpha1.RootFolderKindMovie, "movies")

      // ORDER MATTERS, and this is the trap. CLUSTARR_RELEASES dedups on
      // Msg-Id = MsgIDForRelease(indexerName, guid) for TWO HOURS. The
      // fixture's RSS feed holds exactly one item, so the release is
      // published once per indexer and never again inside that window. If
      // the Movie is not already monitored, available and resolvable when
      // that single publish lands, the matcher correctly declines it -- and
      // there is no second chance. So: create the Movie, wait for it to be
      // fully settled, and only THEN create the RSS-enabled Indexer whose
      // first poll publishes the release.
      movie := newMovie(ctx, t, "e2e-fh-movie", 900101, profile.Name, rf.Name,
          catalogv1alpha1.MinimumAvailabilityAnnounced)
      patchMovieDelayProfile(ctx, t, movie, delay.Name)
      waitForMovieMetadata(ctx, t, movie, "Fixture Firehose Film")

      // A unique object name per run is what keeps that 2h dedup window from
      // suppressing this run's publish -- the indexer name is half the
      // Msg-Id. uniqueName already guarantees it; this comment records that
      // it is load-bearing here and not merely hygienic.
      idx := newIndexer(ctx, t, "e2e-fh-idx", "", time.Minute, true /* enableRss */)
      waitForIndexerReady(ctx, t, idx)
  ```

  `minimumAvailability: announced` is the second guarantee behind the fixture
  TMDB entry: `movie.Availability` returns "always available" for `announced`
  without consulting metadata at all, so even a metadata refresh that failed
  could not leave this Movie unavailable and the release temporarily rejected.

- [ ] **24. `TestIndexerReleaseFirehose`, assertions.**

  ```go
      // indexarr's own side first, so a failure is attributed correctly: if
      // the poll itself never happened, that is D1-7's bug, not the
      // matcher's.
      waitFor(t, ctx, firehoseTimeout, "Indexer "+idx.Name+" published an RSS poll",
          func(ctx context.Context) (bool, error) {
              var live indexv1alpha1.Indexer
              if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(idx), &live); err != nil {
                  //nolint:nilerr // keep polling
                  return false, nil
              }
              return live.Status.LastRssAt != nil && live.Status.IndexedReleases >= 1, nil
          }, describeIndexer(client.ObjectKeyFromObject(idx)), describeTorznabRequests(t0))

      // The arrival assertion. status.pendingGrab is written by
      // catalogarr's grab path under ManagerCatalogarrGrab, and it can only
      // exist if the release was decoded, its envelope key split into THIS
      // namespace, matched to this Movie by tmdbid, resolved, evaluated and
      // delayed. releaseTitle is the fixture's string, which appears nowhere
      // on disk, in any CR, or in any other fixture.
      //
      // If the envelope key regressed to something without a "/", this wait
      // times out while every other signal stays green -- which is precisely
      // the failure this assertion exists to make visible.
      var delayed catalogv1alpha1.Movie
      waitFor(t, ctx, firehoseTimeout, "Movie "+movie.Name+" status.pendingGrab from the firehose",
          func(ctx context.Context) (bool, error) {
              if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(movie), &delayed); err != nil {
                  //nolint:nilerr // keep polling
                  return false, nil
              }
              return delayed.Status.PendingGrab != nil, nil
          }, describeMovie(client.ObjectKeyFromObject(movie)),
             describeIndexer(client.ObjectKeyFromObject(idx)),
             describeTorznabRequests(t0))

      pg := delayed.Status.PendingGrab
      require.Equal(t, "Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR", pg.ReleaseTitle,
          "the pending release's title can only have come from the fixture's rss.xml")
      require.Equal(t, commonv1.ProtocolTorrent, pg.Protocol)
      // ~12h out, from the scenario's own DelayProfile. A grabAt only
      // minutes away would mean the profile was not resolved and the
      // zero-value spec was used -- which is also the state in which
      // performGrab would have run and created a Download.
      require.True(t, pg.GrabAt.Time.After(time.Now().Add(11*time.Hour)),
          "pendingGrab.grabAt %s does not reflect the scenario's 720-minute torrent delay", pg.GrabAt)

      // The Movie reconciler recomputed Phase from pendingGrab under its own
      // field manager -- the two-writer split round-tripping through the
      // apiserver, not just one manager's apply landing.
      waitFor(t, ctx, 3*time.Minute, "Movie "+movie.Name+" phase Delayed",
          func(ctx context.Context) (bool, error) {
              var live catalogv1alpha1.Movie
              if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(movie), &live); err != nil {
                  //nolint:nilerr // keep polling
                  return false, nil
              }
              return live.Status.Phase == catalogv1alpha1.MoviePhaseDelayed, nil
          }, describeMovie(client.ObjectKeyFromObject(movie)))

      // Nothing was grabbed: this leg belongs to D1, and a Download here
      // would mean the delay was bypassed and D2's scenarios are now racing
      // an object they did not create.
      var dls downloadv1alpha1.DownloadList
      require.NoError(t, k8sClient.List(ctx, &dls, client.InNamespace(Namespace)))
      for _, d := range dls.Items {
          require.NotEqual(t, "Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR", d.Spec.Release.Title,
              "the delayed release must not have been grabbed")
      }
  }
  ```

- [ ] **25. `TestIndexerFailureBackoff`, driving the failure** — assertion 4.

  ```go
  // TestIndexerFailureBackoff is scenario 17's fourth leg: health and
  // backoff are observable, and a disabled indexer is actually left alone.
  func TestIndexerFailureBackoff(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
      defer cancel()
      t0 := time.Now()

      // Precondition, checked first and named explicitly. Escalation is
      // suppressed for failures inside indexer.StartupGrace of indexarr's
      // PROCESS START, which is 15 minutes in production -- half of `make
      // e2e`'s entire budget, and indexarr's age when this scenario runs
      // depends on how long the scenarios before it took. config/e2e sets
      // the grace to 0s; without that this test would pass or fail for
      // reasons unrelated to the code under test. Assert the plumbing rather
      // than discovering its absence as "escalation never moved".
      requireIndexarrStartupGraceZero(ctx, t)

      // apiPath selects the fixture's always-500 personality. It is also the
      // only assertion that spec.generic.apiPath is JOINED onto baseURL: if
      // indexarr ignored it, every request would land on "/api" instead, the
      // indexer would be perfectly healthy, and this test would fail on the
      // escalation wait with a request log full of successful /api calls --
      // a diagnosis, not a mystery.
      broken := newIndexer(ctx, t, "e2e-idx-down", "/api/down", time.Minute, true)
      healthy := newIndexer(ctx, t, "e2e-idx-up", "", 15*time.Minute, false)
      waitForIndexerReady(ctx, t, healthy)

      // The RSS poll is the driver: the reconciler records no escalation
      // (rulings R6/R15 -- escalation is computed where the failure is
      // observed, in the fan-out and the poll), so a caps failure alone
      // would only move conditions. rssInterval is 1m, and the ladder's
      // first step may be a zero-length disable, so escalationTimeout
      // budgets three failing polls.
      var down indexv1alpha1.Indexer
      waitFor(t, ctx, escalationTimeout, "Indexer "+broken.Name+" disabled by backoff",
          func(ctx context.Context) (bool, error) {
              if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(broken), &down); err != nil {
                  //nolint:nilerr // keep polling
                  return false, nil
              }
              return down.Status.EscalationLevel >= 1 &&
                  down.Status.DisabledUntil != nil &&
                  down.Status.DisabledUntil.After(time.Now()), nil
          }, describeIndexer(client.ObjectKeyFromObject(broken)), describeTorznabRequests(t0))

      require.NotNil(t, down.Status.InitialFailureAt,
          "the failure streak's anchor must be recorded and kept (correction C2): "+
              "an apply that omits it releases it, resetting the streak on every failure "+
              "and defeating the ladder entirely")
      require.NotNil(t, down.Status.LastFailureAt)
      require.NotEmpty(t, down.Status.LastFailure, "a failure must say what failed")
      require.False(t, isConditionTrue(down.Status.Conditions, indexv1alpha1.IndexerConditionHealthy))
      require.True(t, down.Status.DisabledUntil.After(down.Status.InitialFailureAt.Time))

      // It really was contacted, and really at the failing path.
      var sawDown bool
      for _, r := range torznabRequestsSince(t, t0) {
          if r.Path == "/api/down" {
              sawDown = true
          }
      }
      require.True(t, sawDown,
          "no request reached /api/down: spec.generic.apiPath was not joined onto spec.baseURL")
  ```

- [ ] **26. `TestIndexerFailureBackoff`, "stops being queried."** Two
  independent proofs, through the two channels:

  ```go
      // Proof one, through the API: a fan-out that includes the disabled
      // indexer must SKIP it, not query it and fail. The Search is created
      // only now, after disabledUntil is set -- issued earlier it would
      // legitimately report "error" instead, and the assertion would be
      // about timing rather than about the gate.
      profile := newRankedQualityProfile(ctx, t, "e2e-bo-qp", []tier{
          {name: "Bluray1080", qualities: []string{"Bluray-1080p"}},
          {name: "Web720", qualities: []string{"WEBRip-720p", "WEBDL-720p"}},
      })
      rf := newRootFolder(ctx, t, "e2e-bo-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
      movie := newMovie(ctx, t, "e2e-bo-movie", 900100, profile.Name, rf.Name,
          catalogv1alpha1.MinimumAvailabilityAnnounced)
      waitForMovieMetadata(ctx, t, movie, "Fixture Search Film")

      done := runSearch(ctx, t, movie, []string{broken.Name, healthy.Name})

      byName := map[string]catalogv1alpha1.IndexerOutcome{}
      for _, o := range done.Status.IndexerOutcomes {
          byName[o.Name] = o
      }
      require.Contains(t, byName, broken.Name)
      require.Contains(t, byName, healthy.Name)
      require.Equal(t, catalogv1alpha1.IndexerOutcomeSkipped, byName[broken.Name].State,
          "a disabled indexer must be SKIPPED, not queried and failed: "+
              "an 'error' outcome here means indexer.Healthy was never consulted")
      require.Zero(t, byName[broken.Name].Count)
      require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, byName[healthy.Name].State,
          "one indexer's backoff must not take the healthy one down with it; error=%q",
          byName[healthy.Name].Error)
      require.NotEmpty(t, done.Status.Results, "the healthy indexer still contributed results")

      // Proof two, through the file channel: the fixture sees nothing
      // further on /api/down. A worker ignoring the backoff would poll
      // within one rssInterval; quietWindow is two, so a delivery that
      // slipped cannot manufacture a false pass by being late.
      mark := time.Now()
      time.Sleep(quietWindow)
      var after []torznabstub.Entry
      for _, r := range torznabRequestsSince(t, mark) {
          if r.Path == "/api/down" {
              after = append(after, r)
          }
      }
      require.Empty(t, after,
          "the disabled indexer was queried %d more time(s) during its backoff window: %+v",
          len(after), after)

      // ...and the window really was still open for the whole of it, so the
      // quiet above was the gate and not an expired disable.
      var stillDown indexv1alpha1.Indexer
      require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(broken), &stillDown))
      require.NotNil(t, stillDown.Status.DisabledUntil)
      require.True(t, stillDown.Status.DisabledUntil.After(time.Now()),
          "the backoff window expired during the quiet check; the assertion proved nothing")
  }
  ```

  **Not asserted, deliberately:** recovery. `RecordSuccess` clearing the
  escalation is real behaviour, but reaching it means waiting out the ladder's
  first step, which is minutes at best and outside this scenario's budget at
  worst. D1-3's unit tests own the transition; add an e2e recovery assertion
  only if the ladder ever gains a sub-minute first step. Record it as a carried
  item rather than leaving a reader to wonder whether it was forgotten.

##### Part F — the roster and verification

- [ ] **27. Add scenario 17 to
  `docs/superpowers/plans/2026-09-18-remaining-work.md`.** Append after
  scenario 16 — appended, never inserted, so no existing number shifts and no
  cross-reference in this file or in `test/e2e` goes stale:

  ```markdown
  17. **Indexer, federated search and the release firehose (M2).** An `Indexer`
      against the in-cluster Torznab fixture reconciles to healthy with
      `status.caps` from a live capabilities fetch and `status.protocol`
      resolved; a `Search` CR returns ranked `status.results` end to end
      through `rpc.indexarr.search`, worst-first on the wire and best-first in
      status, with every result keyed by the Indexer's object name; the RSS
      worker's release reaches `catalogarr`'s rss-matcher with a legal
      `<namespace>/<indexer>` envelope key, proven by the matched Movie's
      `status.pendingGrab`; and a failing indexer escalates, is disabled, and
      is then neither queried by the fan-out (`skipped`) nor polled again
      (the fixture's request log stays quiet).
  ```

  Then two cross-references, in the same commit:
  - the D1 bullet's "Number it when the D1 plan is written and add it to the
    roster below, so Phase H's audit sees seventeen" becomes "It is **scenario
    17** in the roster below, so Phase H's audit sees seventeen."
  - the "Rule for Phases C–G" line's `D:` clause gains 17: "D: 1 through the
    import, 2, 3, 4, 6, **17** and the pipeline/downloaders pages of 14".

- [ ] **28. Verify the parts that do not need a cluster.** `make generate`
  (nothing should change — this task adds no API types), `make lint`, and
  `make test` for `test/fixtures/torznabstub`'s unit tests. Then
  `"$(go env GOPATH)/bin/kustomize" build config/e2e | head -200` to confirm
  the overlay renders the new Deployment, Service and Secret and that the
  indexarr patch lands (`grep -A2 CLUSTARR_INDEXER_STARTUP_GRACE`). Confirm
  `go vet -tags e2e ./test/e2e/...` compiles the new file — the `e2e` build tag
  means a plain `go build` never sees it, which is exactly how a scenario
  reaches `main` uncompiled.

- [ ] **29. Run the scenario on kind.** `hack/e2e.sh` end to end once, then
  iterate with `E2E_SKIP_BUILD=1 E2E_ARGS="-run 'TestIndexer' -v" hack/e2e.sh`.
  Before declaring it green, confirm the **rerun** property the harness
  depends on: run the three tests twice in a row without recreating the
  cluster and confirm both runs pass. A second run inside two hours is exactly
  the window in which `CLUSTARR_RELEASES`'s Msg-Id dedup would suppress a
  firehose release if the Indexer name were not unique per run — so a green
  first run and a hanging second one is the specific symptom to look for, and
  it means `uniqueName` stopped being used for the Indexer.

#### Failure diagnostics

What already exists, unchanged: on any non-zero exit `hack/e2e.sh` writes
per-workload logs (plus `--previous` for crash loops), `events.txt`,
`pods.txt`, `pods-describe.txt`, every Clustarr CR as `resources.yaml`, and
NATS's `/jsz` through a port-forward, all into `test/e2e/artifacts/`.
`cleanupUnlessFailed` leaves a failing scenario's objects in place so the dump
can see them.

What this task adds, and why each one is needed to diagnose a failure on
someone else's machine without a rerun:

- **`deployment/torznab-stub` in `WORKLOADS`** — its rollout is waited on (so a
  fixture image built without the embedded XML fails at `rollout status`, not
  in an assertion) and its logs are dumped. Those logs carry the stub's own
  `unrecognised route` and `unsupported function` warnings, which is how "a
  query nobody serves" is told from "a query nobody made".
- **`torznab-requests.jsonl` copied into artifacts** — the full request log.
  It answers, offline, the three questions a failed indexer scenario always
  raises: was the fixture contacted at all; on which path; with which query.
- **`describeIndexer` and `describeSearch` on every wait** — the last observed
  state is captured in the failure message itself, because `hack/e2e.sh`'s dump
  runs after `go test` returns, by which time a passing sibling's cleanup may
  already have deleted neighbouring objects. `describeSearch` prints
  `indexerOutcomes` in full: a search that returned nothing has its reason
  there and nowhere else.
- **`describeTorznabRequests` on every wait that depends on the fixture** —
  the same log, filtered to this scenario's window, inline in the failure
  message. "Timed out waiting for Search Completed" plus "the fixture indexer
  logged NO request since this scenario started" is a complete diagnosis;
  either line alone is not.

---

## Wave 6 — serial, the phase gate

### Task D1-10: gate, status and carries

**Files:**
- Modify: `hack/deps/deps.go` (delete the `modernc.org/sqlite` keeper — `pkg/relindex` imports it for real now)
- Modify: `go.mod`, `go.sum` (one `go mod tidy`)
- Modify: `CLAUDE.md` Status
- Modify: `docs/superpowers/plans/2026-09-18-remaining-work.md` (carried items, and the new e2e scenario in the roster)

**Path ownership:** controller only.

- [ ] **Step 1: Drop the dependency keeper**

`pkg/relindex` now imports `modernc.org/sqlite`, so the blank import added in D1-0 is dead. Delete the entry (and the file, if it is empty), then `go mod tidy` and confirm the module survives on its real importer.

- [ ] **Step 2: The full gate**

```bash
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
go build ./... && go vet ./...
make generate && make manifests && git status --short   # must be clean
make lint
go test -count=1 -race -p 4 ./...
go mod tidy -diff
```

`-p 4` is deliberate: roughly fifteen packages each stand up an envtest control plane, and unbounded contention surfaces as a failure in a **different** package on each run — the worst shape for a gate to fail in. Re-run any one-off in isolation before reporting it as a defect. Every envtest must genuinely run; a suite finishing in single-digit milliseconds **skipped**.

- [ ] **Step 3: The end-to-end gate**

```bash
hack/e2e.sh
```

from a clean cluster. The new indexer scenario and Phase C's five must all pass, and `test/e2e/artifacts/` must be empty of failures. **Check the machine first** — the user runs their own kind clusters, `hack/kind.sh` is name-scoped to `clustarr`, and adding a cluster to a loaded box makes any failure unattributable to contention. If it is not safe to run, say so plainly rather than reporting an untested pass. Delete only your own cluster afterwards.

- [ ] **Step 4: Rewrite `CLAUDE.md`'s Status**

Phase C's lesson: two "read me first" documents were **materially false** about the project's own state — the Status section still said "nothing reconciles yet" long after it did. Write what D1 actually delivered, in the compressed style the existing paragraphs use, and point "Next" at D2. Keep the section near 40 lines. Check the observability doc's banner in the same pass; nothing parses it, so nothing catches it going stale.

- [ ] **Step 5: Sweep the carries**

Every deferred item goes into the remaining-work plan's carried section with the phase that owns it, harvested from the ledger. D1 will have accumulated at least: the unreachable free-text search path (R11); `alsoOn` provenance (R12); the richer query surface `Store`'s four methods cannot express (R21); the uncounted grabs for `torrentURL`/`magnetURL`/`nzbURL` sources (R3); and the "query mode" switch `SearchSpec` lacks (R4). Add the new e2e scenario to the roster so Phase H audits seventeen, not sixteen.

- [ ] **Step 6: Commit**

**Done when:** the keeper is gone and tidy is clean; the full gate is green with envtest genuinely running; `hack/e2e.sh` passes or its non-run is honestly reported; `CLAUDE.md` describes the real state; every carry has a home and an owning phase.
