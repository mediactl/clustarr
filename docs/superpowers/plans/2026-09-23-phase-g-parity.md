# Phase G — M6 parity, lists and non-video inventory

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Cardigann-defined indexers search, clustarr serves Torznab to other tools, import lists add items, domain events and dead letters surface on the CRs they concern, the seven non-video kinds reconcile end to end, and the UI gains its remaining four pages plus the actions that make it more than a viewer.

**Shape:** three slices, like D1/D2/D3 — **G1 backend** (indexarr, importarr lists, catalogarr history/DLQ), **G2 non-video catalog**, **G3 UI**. As in E and F, nearly every library exists; the work is controllers, servers and wiring.

**Spec:** design §5, §6.1, §6.2, §13, §16 M6; amendment-1 §A1.5, §A3 (UI) wins on conflict. Scope: `docs/superpowers/plans/2026-09-18-remaining-work.md` Phase G, plus every carried-defect item tagged Phase G.

**Prior art, verified 2026-09-23 against source.**

---

## Global Constraints

- Go 1.27, controller-runtime v0.25.1, k8s.io/* v0.37.0. GPL-3.0 header on every file.
- **All status writes via `pkg/k8s.PatchStatus`**, each a **complete declaration**; every CLAUDE.md "Gotchas" variant applies. **An over-claim is silent** — assert splits on `managedFields`. **Re-`Get` before applying** on read-slow-work-apply paths.
- **NATS KV keys via `events.KVKeyToken`**, new shapes contract-tested against a **real embedded server**.
- **The caller owns rate limiting**; libraries never default a limiter on. **Every HTTP body is read through a cap.**
- `+kubebuilder:rbac` markers are **package-level**; after `make manifests`, **sync the chart's RBAC between its BEGIN/END sentinels**.
- **No new Go dependencies.** Tailwind is a standalone binary, not a Go module (R4).
- **No `-race`, no e2e runs, no kind clusters** (user instruction). e2e is **written, not run**.
- Commit with a pathspec. Never `git add -A`/`commit -a`/`git stash`. **Do not push.**
- **Registration is where inert code hides.** Every slice has a wiring task that proves each new runnable is reachable from its subcommand and from `clustarr all`, and adds it to `cmd/clustarr/runnable_registration_test.go`.

## Rulings

**R1 — the DLQ projector annotates and emits Events; it does not write status.** Design §5 has it set a `DeadLettered` condition on the CR named by `Clustarr-Key` — on *any* kind, which would make one projector a second status writer on every resource in the system, against CLAUDE.md's first invariant. It instead applies a metadata annotation `clustarr.io/dead-lettered: <subject>@<RFC3339>` under its own field manager `clustarr-dlq-projector` (metadata is not status, and an SSA apply of one annotation key owns exactly that leaf) and emits an `events.k8s.io` Event. Owning controllers folding that annotation into a condition is carried, not built here. Cost if wrong: the dead letter is visible in `kubectl describe` and on the object's annotations rather than in its conditions.

**R2 — the UI gains writes, through one package, and the D3-4 guards narrow rather than disappear.** Amendment §A3: "search now" creates a `Search`, "rescan" creates a `LibraryScan`, "monitor this" patches `spec.monitored`. D3-4's AST guard bans every write in `ui/` and its role guard allows only `get,list,watch` — correct for D3, wrong for G. So: **every UI write lives in `ui/actions/` and nowhere else**; the AST guard allows write calls only inside that package and **still bans `Status()` everywhere**; the role guard allows exactly `create` on `searches` and `libraryscans` and `patch` on the catalog kinds' main resource, and **still refuses any `*/status` resource and any `delete`**. Spec patches use field manager `clustarr-ui` so an audit of `managedFields` can tell a UI edit from a controller's. D3-5's e2e assertion — no UI manager on any **status** — must still hold, and a guard test asserts it.

**R3 — non-video kinds get inventory in G; automated search and grab for them are in scope only where `pkg/quality` already supports the kind.** Phase B's 13 built-in quality profiles are video. Building music/book quality catalogues is its own project. G2 delivers controllers, metadata, rescan attribution and manual import for all seven kinds; if the grab path cannot decide non-video releases with what exists, say so in the report and file it — do not invent quality data.

**R4 — the Tailwind pipeline is the standalone `tailwindcss` CLI, pinned, with the generated CSS committed and embedded.** No Node. A `make css` target downloads a pinned release binary into `$(go env GOPATH)/bin` and builds `ui/static/app.css`, which is committed and served via `go:embed` — exactly the model `make templ` already uses for generated templ code. The CDN `<script>` tags for Tailwind are removed; htmx may stay on its CDN or be vendored, implementer's choice, stated in the report.

**R5 — Cardigann search runs through `indexarr/search`'s existing fan-out, not beside it.** The fan-out already owns dedupe, the query-limit window, health and backoff. A Cardigann indexer must be one more client behind the same interface, so it inherits all of that rather than getting a parallel path that skips it.

**R6 — a Cardigann `SearchBlock.Error` must become an error, not "no results".** The carried defect (remaining-work, Phase D1 block) is that a failing tracker currently reads as zero releases, so health and backoff never see it. Fix it as part of wiring, since wiring is what makes it live.

---

## G1 — backend

### G1-0 — field managers and shared declarations (SERIAL, blocks G)
Add to `pkg/k8s/fieldmanager.go`: `clustarr-dlq-projector` (R1), `clustarr-ui` (R2), and one manager for non-video parent→child fan-out writes (the role `catalogarr-series` plays for Series→Episode — read `catalogarr/controller/series/doc.go` for why it must be distinct from the child's own manager). Update the design spec's field-manager table to match, and `FieldManagers()`'s list. Nothing else — this is the serial point so parallel tasks never edit that file.

### G1-1 — Cardigann indexers searchable
`indexarr/controller/indexer`: resolve `spec.definition`/`spec.definitionRef` to a `cardigann.Definition` instead of short-circuiting to `DefinitionNotImplemented` (`controller.go:238-273`), set `status.protocol`/`privacy`, and map `DefinitionType`'s `semi-private` to the CRD's spelling (`source.go:83-87`). Login sessions into `clustarr-indexer-sessions` KV plus the owned Secret (`sessionSecretSuffix`, `source.go:114-129`, unused today). Give `cardigann.Engine` an injectable limiter (it has none). `indexarr/search` fan-out and `rpc.indexarr.download` dispatch to the engine per **R5**, fixing **R6**. Apply `IndexerProxy` to requests. Tests against the existing Cardigann fixtures under `testdata/`.

### G1-2 — Torznab facade, and free-text Search
Serve `/{indexer}/api`, `/{indexer}/download` and `/search/api` on `FacadeBindAddress` (`indexarr/run.go:73-81`, bound to nothing today), backed by `rpc.indexarr.query` and live search with `SearchRequest.Text` set. Remove catalogarr's stale `failQueryMode` (`catalogarr/controller/search/reconciler.go:356-363`), which still rejects free-text Search as "not yet available before Phase D" although D1 shipped the RPC — wire it to `rpc.indexarr.query`.

### G1-3 — ImportList controller and sync worker in importarr
Every provider in `pkg/importlist` is built and none is reachable. An ImportList controller and scheduled sync: `Config` → provider constructor (`pkg/importlist/config.go:112-115` names this task as its owner), fetch, `Dedupe`, `ApplySyncLevel`, create or update catalog items. The `importlist.ExternalIDs` ↔ `metadata.ExternalIDs` conversion carried at `pkg/importlist/types.go:27-32`. Trakt's device-code flow with a Secret-backed `TokenStore`, status surfacing the user code for the UI. Respect `ImportExclusion`. Status under `ManagerImportarr`.

### G1-4 — history sink and DLQ projector
Fill `catalogarr/run.go`'s `RoleHistory` branch (`run.go:405-406`, a valid role that starts nothing today). Subscribe `ConsumerCatalogHistory` and project domain events to `events.k8s.io` Events on the owning CR. Subscribe `ConsumerDLQProjector` and apply **R1**. Both consumers exist server-side with nothing acking them, so messages accumulate until this lands. A `clustarr.io/replay` handler is in scope only if small; otherwise carry it.

### G1-6 — title fallback for automatic search (parity with Radarr/Sonarr)
Automatic search is **ids-only**: `catalogarr/worker/search.BuildSearchRequest` never sets `SearchRequest.Text`, and the frozen payload has no title field. So an indexer that advertises none of the request's id parameters — which describes many Cardigann-defined private trackers, now searchable after G1-1 — is **skipped for every automatic search** with "no supported id parameter", reachable only interactively. Radarr and Sonarr fall back to a title query. Add a new version of the search payload carrying the item's resolved title and year (find how `pkg/events/schema` versions payloads — `Clustarr-Schema` — and follow it exactly; a consumer on the old version must keep working), populate it from `status.metadata`, and in `indexarr/search`'s per-indexer query building use `t=search&q=<title> <year>` **only** when the indexer supports none of the request's id parameters. Ids stay preferred wherever they work — a title query is strictly less precise, and results still pass through `pkg/decision`, which rejects a wrong title. Test the fallback is taken exactly when it should be and never when an id works. Carried item it closes: remaining-work, Phase D1 block, "The federated search is ids-only".

### G1-5 — G1 wiring (SERIAL, after G1-1..G1-4)
Reachability of every new runnable and server, RBAC markers + `make manifests` + chart sync, `runnable_registration_test.go`, `start_envtest_test.go` per role. **The facade must actually bind** — test it by dialling it, not by checking the flag parses.

## G2 — non-video catalog

### G2-1 — metadata gateway for the seven kinds
`catalogarr/metadata/patch.go` has builders only for Movie and Series (`:53`, `:123`); add Artist, Album, Author, Book, Audiobook, Comic, Issue. `pkg/metadata/registry.go:50-77`'s `Lookup` covers `artist/author/audiobook/comic` — add `album`, `book`, `issue` via the existing clients (MusicBrainz release groups, Open Library works/editions, ComicVine issues). `pkg/pipeline/project.go:471-520` already reads each kind's `ConditionMetadataReady`; this task is what finally sets them.

### G2-2 — Artist→Album, Author→Book, Comic→Issue controllers
Three parent/child pairs, each following **Series→Episode exactly** (`catalogarr/controller/series/`): the parent fans out children from metadata, sets `spec.monitored` only at creation, writes children's provider-sourced fields under G1-0's fan-out manager, and uses `reassertKnownStatus` — except where a status list's generated `With*` appends, per CLAUDE.md's `reassertKnownStatus` exception. Children own their phase/conditions under `ManagerCatalogarr`. Deterministic child names per design §4.2 (`<artist>-<releasegroup-uid8>`, `<comic>-<calculatedNumber padded 5.1>`).

### G2-3 — Audiobook controller, and standalone Book
Audiobook (ASIN + region via Audnexus, optional `bookRef`) and a Book with no `authorRef` (standalone, `book_types.go:159-161`).

### G2-4 — rescan attribution and manual import for non-video roots
`importarr/worker/rescan/mediafile.go:86-88` refuses every RootFolder kind but movie with `unsupported_root_kind`. Extend attribution to the non-video kinds using `pkg/release`'s per-kind parsers and `pkg/naming`'s per-kind layouts (both already cover them), **under the never-guess rule** — unattributable files go to `LibraryScan.status.unmatched` with a reason. Manual import: the `catalog.clustarr.io/import-target` and `import-override` annotations from design §779, named there and implemented nowhere. Per **R3**, report whether non-video grab works with existing quality data.

### G2-5 — G2 wiring (SERIAL)
As G1-5, for every new catalogarr controller.

## G3 — UI

### G3-1 — `ui/actions` and the narrowed guards (SERIAL within G3)
Per **R2**. The package, the three actions (create Search, create LibraryScan, patch `spec.monitored` via `clustarr-ui`), a `ui` Role granting exactly those verbs, and D3-4's two guards narrowed — then **falsified again**: a write outside `ui/actions` must fail the AST guard, a `*/status` or `delete` grant must fail the role guard. Plus a guard that no `clustarr-ui` manager ever appears on a status path.

### G3-2 — Tailwind pipeline
Per **R4**. `make css`, committed `ui/static/app.css`, `go:embed`, CDN Tailwind removed from `ui/views/layout.templ:35-37`.

### G3-3 — library and unmatched pages
Library (amendment §A3: poster grid, detail modal, monitor/unmonitor/search actions, SSE on status change) and Unmatched (`LibraryScan.status.unmatched`, candidates, manual-assign using G2-4's annotations). Extend `ui/projection` rather than adding tickers — D3 R4. Stable `data-*` attributes on every row.

### G3-4 — import lists and settings pages
**Plus the unmatched page's manual-assign action**, moved here from G3-3 because its mechanism is defined by G2-4 (rescan-unmatched files have no Download to annotate). Build it from whatever G2-4 documents; if G2-4 concluded it needs an API addition, stop and report rather than inventing one.
**Note from G3-1:** the role test deliberately refuses any write grant not declared in `actions.Grants()`. Settings forms that patch spec on new kinds (root folders, quality profiles, indexers, download clients, providers) must add each grant to **both** `actions.Grants()` and `config/rbac/ui_role.yaml` (and its chart copy); the test cross-checks them. Use merge patch as G3-1 did — an SSA apply by `clustarr-ui` would release fields a previous UI edit owned.
Import lists (schedule, last sync, counts, Trakt device code from G1-3's status). Settings (root folders, quality profiles, indexers, download clients, providers, profiles) as edit forms that **patch spec only**, per §A3, via `ui/actions`.

### G3-5 — G3 wiring, and the projection-stream guard
Every new route and stream wired in both `clustarr ui` and `clustarr all`; `cmd/clustarr/ui_projection_wiring_test.go` covers any new `Subscribe*` automatically.

**Carried into G3-5 from G3-1 (`4b146af`):**
- **`Options.Actions` is set nowhere, so every UI action returns `ErrNoWriter`.** Build `actions.New(client)` from a real client with ui's scheme in **both** `clustarr ui` and `clustarr all`. Add a guard in the style of `ui_projection_wiring_test.go` so an unset `Actions` is a red test, not a UI whose buttons silently do nothing.
- Stale text to correct: `CLAUDE.md` names `TestUIRoleGrantsOnlyReadVerbs` (renamed `TestUIRoleGrantsOnlyReadsAndActionWrites`) and says the D3 AST guard checks only calls on the `client` package (it matches the method name on anything); `config/manager/ui.yaml:13` still says the ServiceAccount is "bound, read-only"; amendment-1 §A3.2 says the UI "holds no field manager" — ruling R2 gives it `clustarr-ui` for spec, never status. Amend §A3.2 with a dated note rather than rewriting it.

## G4 — fixtures, e2e (written, not run), gate

### G4-0 — sweep `api/` for CRD defaults a Go client can never reach (SERIAL, after the wiring queue)
The same defect has now turned up **five times** in one phase, each found by a different agent in a different API group: `TranscodeProfile.maxOutputToSourcePercent` (an int32 defaulted to `1.0`, so every real transcode exited 4), `policy.replaceSource`/`recycleBin`, `activeDeadline`/`resources`/`scratch`, D2's usenet `PostProcess`, and `SubtitleProviderSpec.Enabled`. The mechanism is always one of two:
- a **`bool` with `omitempty` and `+kubebuilder:default=true`** — a typed client drops `false`, the apiserver re-applies `true`, so the field **cannot be set false** from Go (kubectl YAML works, which is why it survives review);
- a **value-typed field with a default** (`int32`, `metav1.Duration`, `resource.Quantity`, a struct) — a typed client always marshals it, present-but-zero, so the default **never** applies to anything created from Go.

Find them all mechanically rather than waiting for the sixth: walk every `api/**/*_types.go` with `go/ast` for fields carrying `+kubebuilder:default` whose Go type is not a pointer, and classify each. Then per field, one of: make it a pointer (`*bool` for defaulted booleans — the only honest fix when `false` is meaningful); floor it in code where zero has no coherent meaning (the D1 `Indexer.spec.timeout` precedent), saying so in the doc comment; or record why neither applies. Update every consumer, `make generate manifests` in a clean worktree, and **turn the walker into a permanent test** that fails when a new defaulted non-pointer `bool` is added — the only way this stops recurring.



### G4-1 — fixtures and scenarios 9, 10, 11 and the rest of 14
**Fix first (from G3-1):** `test/e2e/ui_test.go`'s `requireNoUIManager` rejects **any** manager whose name contains `ui` on the whole object. After R2 the UI legitimately owns `spec.monitored` under `clustarr-ui`, so the first e2e that performs a UI action would fail spuriously. Narrow it to the actual invariant: no `clustarr-ui` entry on the **status** subresource — the same assertion `TestUIManagerNeverOwnsStatus` makes in envtest.
The Cardigann tracker page, import-list stubs and non-video metadata stubs in `test/fixtures/`, deployed from `config/e2e`. **Do not run.**

### G4-2 — gate, CLAUDE.md Status, carried list
Mirror D1-10. Phase G paragraph, identifiers grepped, scenarios stated as **never executed**.

## Waves
0: G1-0 · 1: G1-1, G1-2, G1-3, G1-4, G2-1, G3-2 · G1-6 after G1-1/G1-2 · 2: G2-2, G2-3, G3-1 · 3: G2-4, G3-3, G3-4 · 4: G1-5, G2-5, G3-5 (serial, one at a time) · 5: G4-0 · 6: G4-1 · 7: G4-2
