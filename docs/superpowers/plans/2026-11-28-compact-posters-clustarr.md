# Compact Posters (clustarr half) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** clustarr draws, stores and serves a second, "compact" rendition of a movie's or series' poster (larger title image, larger badges), and lists which regular poster each one is the twin of.

**Architecture:** Two more artwork variants beside `original` and `overlay`, written by the same two writers in the passes they already run: the metadata agent draws `compact` while it draws a titled poster, the renderer draws `overlay-compact` in the render task. Nothing the regular poster is identified by changes, so Plex refreshes nothing. The UI serves the rendition at `/art/...?size=compact` and a pairing list under `/plex/`.

**Tech Stack:** Go, `golang.org/x/image/draw` (already used), JetStream object store through `pkg/events`, kubebuilder CRD markers, testify.

**Spec:** `docs/superpowers/specs/2026-11-27-compact-posters-design.md` (§5.1 to §5.3, §5.5 steps 1 and 2). The proxy in the Plex pod (§5.4) is a separate plan in the cluster-plex repository; this plan produces everything it consumes.

## Global Constraints

- Both new variants are JPEG, 500 x 750 at most, as their regular twins.
- `TitleVersion`, `overlayplan.RenderVersion`, `overlayplan.InputsDigest`, `overlayplan.ProfileHash` and `status.overlay` keep their values and meaning. No status field is added. A regular `original` or `overlay` keeps its bytes for the same inputs.
- A writer writes, clears and deletes only its own variants: the metadata agent `original` and `compact`, the renderer `overlay` and `overlay-compact`.
- Starting numbers, replaced by what the owner settles in Task 3: compact title at most 90% of the width and 32% of the height; compact badge width 28, score 31.
- `/art` gains no resizing. The pairing list is on a path outside `/art` and carries no title and no name.
- No `float32`/`float64` in `api/`; GPL header on every Go file; no *arr brand; metrics never labelled by title, path or release name.
- Commits are by pathspec (`git add` new files first, then `git commit -m '...' -- <paths>`), on `main`, never from a subagent push. Generated files are committed with their sources.
- Envtest and `make test` run once, in Task 9. Tasks run their own packages with `go test ./<pkg>/...`.
- Tests build inputs through the real producers and are falsified once (revert the change, watch the test fail).

## Review Focus

1. **A regular titled poster's bytes after this change.** The compact pass shares a canvas with the regular one; a draw on the shared canvas changes every titled poster's digest and Plex refreshes the library. Expected: byte-identical. Test in Task 5.
2. **An item with badges whose poster is not titled** (most of the library). Expected: `overlay-compact` drawn on `original`, no `compact` object, no refetch loop. Tests in Tasks 5 and 6.
3. **`size=compact` asked of an item that has none, of a non-poster type, or with another `size` value.** Expected: 404, never the regular poster (the proxy relies on it to decline). Test in Task 7.
4. **An item that loses its profile or its last badge, or is deleted.** Expected: `overlay-compact` cleared with `overlay`; all four objects reaped. Tests in Tasks 4 and 6.
5. **A compact object changing in the bucket while a UI page is open.** Expected: no artwork event to the browser, no page poster swap. Test in Task 4.

---

### Task 1: Title geometry as a value

**Files:**
- Modify: `app/catalog/metadata/artwork/title.go` (constants at 40-70, `titled` at 129, `drawTitle` at 167)
- Test: `app/catalog/metadata/artwork/title_test.go`

**Interfaces:**
- Produces:
  - `type TitleGeometry struct{ MaxWidthPct, MaxHeightPct, BottomPct int }`
  - `var RegularTitle = TitleGeometry{MaxWidthPct: 78, MaxHeightPct: 24, BottomPct: 5}`
  - `var CompactTitle = TitleGeometry{MaxWidthPct: 90, MaxHeightPct: 32, BottomPct: 5}`
  - `func DrawTitled(canvas *image.RGBA, mark image.Image, g TitleGeometry) error` (shade, then the mark; draws on canvas)
  - `func (f *Fetcher) titled(canvas *image.RGBA, mark image.Image, g TitleGeometry) (fitted, error)`

- [ ] **Step 1: Write the failing tests**

`TestDrawTitled_Geometry`: a 500 x 750 grey canvas and a 1000 x 100 opaque white mark. With `RegularTitle` the white pixels' bounding box is 390 wide (78%); with `CompactTitle` 450 wide (90%); both end 37 px above the bottom edge (750 x 5 / 100). With a 100 x 1000 mark, the box is 180 tall under `RegularTitle` (24%) and 240 under `CompactTitle` (32%).

`TestDrawTitled_RegularUnchanged`: the output of `DrawTitled(canvas, mark, RegularTitle)` equals, pixel for pixel, the canvas the code at HEAD produces for the same inputs. Capture the HEAD output first as a golden PNG under `test/data/artwork/title-regular-golden.png` with the unmodified `shade` + `drawTitle`.

- [ ] **Step 2: Run** `go test ./app/catalog/metadata/artwork/ -run 'TestDrawTitled' -count=1`. Expected: does not compile (`DrawTitled` undefined).
- [ ] **Step 3: Implement.** `titleMaxWidthPct`, `titleMaxHeightPct` and `titleBottomPct` become `RegularTitle`'s fields; `drawTitle` takes the geometry; the shade keeps its constants for both.
- [ ] **Step 4: Run the package's tests.** Expected: PASS, the existing title tests untouched.
- [ ] **Step 5: Commit** `title.go`, the test and the golden.

### Task 2: Compact badge geometry on the profile

**Files:**
- Modify: `api/catalog/v1alpha1/overlayprofile_types.go` (beside `Geometry` at 169), `api/catalog/v1alpha1/defaults.go`
- Modify: `pkg/overlay/template.go` (beside `TemplateSpec` at 114)
- Generated: `make generate && make manifests` (deepcopy, apply configurations, `config/crd/bases/catalog.clustarr.io_overlayprofiles.yaml`)
- Test: `pkg/overlay/template_test.go`, `app/catalog/overlayplan/plan_test.go`

**Interfaces:**
- Produces:
  - `OverlayProfileSpec.CompactGeometry *OverlayGeometry` (`json:"compactGeometry,omitempty"`), documented as the geometry of the rendition served to small posters
  - `const DefaultOverlayCompactWidthPercent = 28`, `const DefaultOverlayCompactScorePercent = 31`
  - `func CompactTemplateSpec(spec catalogv1alpha1.OverlayProfileSpec) Template`: each field is `compactGeometry`'s when set; else, for width and score, the compact default; else, for the others, what `TemplateSpec(spec)` gives (so a profile's own radius, padding, logo and opacity carry over). Corner is the profile's.

- [ ] **Step 1: Write the failing tests**

```go
func TestCompactTemplateSpec(t *testing.T) {
	spec := catalogv1alpha1.OverlayProfileSpec{Corner: catalogv1alpha1.OverlayCornerTopLeft,
		Geometry: &catalogv1alpha1.OverlayGeometry{OpacityPercent: ptr.To[int32](60), WidthPercent: ptr.To[int32](22)}}
	got := overlay.CompactTemplateSpec(spec)
	assert.Equal(t, 28, got.WidthPct)   // not the regular 22
	assert.Equal(t, 31, got.ScorePct)
	assert.Equal(t, 60, got.OpacityPct) // carried from geometry
	assert.Equal(t, overlay.CornerTopLeft, got.Corner)

	spec.CompactGeometry = &catalogv1alpha1.OverlayGeometry{WidthPercent: ptr.To[int32](33)}
	assert.Equal(t, 33, overlay.CompactTemplateSpec(spec).WidthPct)
}
```

In `plan_test.go`, `TestProfileHash_IgnoresCompactGeometry`: `ProfileHash` of a spec equals `ProfileHash` of the same spec with `CompactGeometry` set.

- [ ] **Step 2: Run** `go test ./pkg/overlay/ ./app/catalog/overlayplan/ -run 'CompactTemplateSpec|IgnoresCompactGeometry' -count=1`. Expected: does not compile.
- [ ] **Step 3: Implement, then `make generate && make manifests`.** The field's validation markers are `OverlayGeometry`'s own.
- [ ] **Step 4: Run both packages' tests.** Expected: PASS.
- [ ] **Step 5: Commit** the two API files, `template.go`, tests, and every generated file `git status` shows under `api/` and `config/` for this kind only (other sessions have generated files in flight: name the paths, never `git add -A`).

### Task 3: Mock renderings, and the owner settles the numbers

**Files:**
- Create: `hack/compactmock/main.go`

**Interfaces:**
- Consumes: `DrawTitled`, `RegularTitle`, `CompactTitle` (Task 1); `overlay.TemplateSpec`, `overlay.CompactTemplateSpec` (Task 2); `overlayrender.Render`; `catalogartwork.FitBox`.
- Produces: `go run ./hack/compactmock --out <png> --item '<poster>;<logo>;<source>=<centis>,...;dub' ...` where `<poster>` and `<logo>` are files or http(s) URLs, the logo and everything after it optional. Geometry flags override the starting numbers: `--title-width`, `--title-height`, `--badge-width`, `--badge-score`.

- [ ] **Step 1: Write the program.** For each item it fits the poster to 500 x 750, draws the regular rendition (regular title if a logo is given, then badges with `TemplateSpec`) and the compact one (compact title, `CompactTemplateSpec`), and lays out one row per item on a sheet: regular and compact at 500 px, at 250 px (a phone's grid at 2x) and at 125 px, CatmullRom-scaled. On each 250 px tile it outlines a reference box 28% wide and 27% tall at the top right: the size of Plex's episode-count box in the owner's screenshot (spec §1).
- [ ] **Step 2: Run it** on the three posters of the owner's screenshot (textless poster and logo URLs from each item's metadata record; ratings from its status) and on one untitled poster with a `DUB` badge. Expected: a PNG sheet; view it.
- [ ] **Step 3: Show the sheet to the owner. STOP.** Later tasks do not start until the owner has said the numbers are right. Rounds are flag changes and a re-run.
- [ ] **Step 4: Write the settled numbers** into `CompactTitle` (Task 1), the two compact defaults (Task 2) and their tests; `make generate && make manifests` if a default's comment changed.
- [ ] **Step 5: Commit** `hack/compactmock` and the changed numbers.

### Task 4: The two variant names, everywhere variants are enumerated

**Files:**
- Modify: `pkg/events/subjects.go` (293-298, metadata keys near 331)
- Modify: `app/catalog/artwork/reaper.go:302` (`countObjects`), `ui/artevents.go` (`changed` at 92, `parseArtName` at 137)
- Modify: `docs/observability.md` (the `variant` label's values)
- Test: `pkg/events/artworkkey_test.go` (create), `app/catalog/artwork/reaper_test.go`, `ui/artevents_test.go`

**Interfaces:**
- Produces:
  - `events.ArtworkVariantCompact = "compact"`, `events.ArtworkVariantOverlayCompact = "overlay-compact"`
  - `func events.KnownArtworkVariant(v string) bool` (the four)
  - `events.ArtworkMetaKeyCompact = "clustarr.io/compact"` (the compact title mark)

- [ ] **Step 1: Write the failing tests**
  - `countObjects` over a listing with one object of each of the four variants sets `ArtworkObjects` for all four labels.
  - `parseArtName("movie/u1/poster/overlay-compact")` and `.../compact` return ok with that variant; `.../thumbnail` does not.
  - `artEvents.changed` for a change of `.../poster/compact` and of `.../poster/overlay-compact` (with and without `OldDigest`, the item having an overlay and an original) broadcasts nothing and leaves `last` as it was.
  - The reaper's `Sweep` over an item that no longer exists deletes its `compact` and `overlay-compact` objects too (extend the existing sweep test's fixture; `parseKey` should already cover it, the test pins it).
- [ ] **Step 2: Run** `go test ./pkg/events/ ./app/catalog/artwork/ ./ui/ -run 'Variant|countObjects|parseArtName|artEvents|Sweep' -count=1`. Expected: FAIL / does not compile.
- [ ] **Step 3: Implement.** `countObjects` and `parseArtName` use `KnownArtworkVariant`; `changed` returns at once for the two compact variants.
- [ ] **Step 4: Run the three packages' tests.** Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 5: The metadata agent draws and keeps `compact`

**Files:**
- Modify: `app/catalog/artwork/sources.go` (beside `TitleVersion` at 103 and `TitleMark` at 120)
- Modify: `app/catalog/artwork/meta.go` (`Facts`, `ObjectMeta`), `app/catalog/artwork/index.go` (`Object`, `ObjectsFromIndex`), `app/catalog/artwork/fetchplan.go` (`Hash`, `PlanFetch`)
- Modify: `app/catalog/metadata/artwork/fetcher.go` (`fetchPlain` 321, `put` 345, `fetchTitled` 367, `Drop` 270)
- Test: `app/catalog/artwork/fetchplan_test.go`, `app/catalog/metadata/artwork/fetcher_test.go`

**Interfaces:**
- Consumes: Task 1's `titled(canvas, mark, g)`, Task 4's names.
- Produces:
  - `const CompactTitleVersion = "1"`; `func (s Source) CompactMark() string`: `""` when `TitleMark()` is `""`, else `CompactTitleVersion + ":" + TitleMark()`
  - `Facts.Compact string`, written as `events.ArtworkMetaKeyCompact` when not empty
  - `Object.Titled bool` (the original's `ArtworkMetaKeyTitled`), `Object.Compact string` (the `compact` object's mark; `""` when there is none), both filled by `ObjectsFromIndex` for the poster
  - The `compact` object: key `events.ArtworkKey(kind, uid, "poster", events.ArtworkVariantCompact)`, same `Facts` as the titled original it was drawn with plus `Compact: src.CompactMark()`

Behaviour to implement:
- `fetchTitled`, at the candidate it stores: copy the canvas before the regular draw, draw the copy with `CompactTitle`, Put it as `compact`, then draw and Put the original exactly as today. A failed compact Put fails the fetch.
- `fetchPlain` of a poster (the source is not titled, or every candidate carried text) and `Drop` of a poster delete the `compact` object; absent is success.
- `PlanFetch`: a poster is also fetched when `obj.Titled && obj.Compact != src.CompactMark()`. The failure-rest rule applies as to any fetch.
- `FetchPlan.Hash`: inside the `Logo != ""` block, also write `"compact\x00" + CompactTitleVersion + "\x00"`, so the plan that asks for a missing compact is published at once.

- [ ] **Step 1: Write the failing tests**
  - `TestFetchTitled_StoresCompactAndLeavesOriginalAlone` (through `FetchOne`, the test HTTP server and fake text detector the existing titled tests use): after one fetch the store holds `original` and `compact`; the original's bytes equal those the same fixture produced before this task (compute them in the test with `titled(canvas, mark, RegularTitle)` on a fresh canvas); the compact's bytes differ; its metadata has `clustarr.io/compact` = `src.CompactMark()`, the `titled` key, and variant `compact`. (Review Focus 1.)
  - `TestFetch_UntitledPosterDeletesCompact`: a store holding a `compact`; a fetch whose candidates all carry text stores the plain poster and the `compact` is gone; so does `Drop`.
  - `TestPlanFetch_Compact` (table): titled original with no compact object → fetched; with a compact whose mark matches → not fetched; with an older `CompactTitleVersion` in the mark → fetched; an original stored plain under a titled source (`Titled` false) with no compact → not fetched (Review Focus 2); a non-poster type is never fetched for it.
  - `TestFetchPlanHash_Compact`: a plan with a `Logo` hashes differently from HEAD's value for the same plan (pin the new value), and a plan with no `Logo` hashes as at HEAD (pin the old value from the existing test).
- [ ] **Step 2: Run** `go test ./app/catalog/artwork/ ./app/catalog/metadata/artwork/ -count=1`. Expected: FAIL / does not compile.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run both packages.** Expected: PASS. Falsify the first test once by drawing compact on the shared canvas.
- [ ] **Step 5: Commit.**

### Task 6: The renderer draws `overlay-compact`; the stage asks for it

**Files:**
- Modify: `app/catalog/overlayplan/plan.go` (beside `InputsDigest` 282, `Want` 304)
- Modify: `app/catalog/artwork/index.go` (beside `OverlayOf` 146)
- Modify: `app/catalog/worker/artwork/handler.go` (`Render` 338, `plan` 460, `draw` 482, outcomes at 108)
- Modify: `app/remediation/artwork/overlay.go` (`Plan` 65, `decide` 129)
- Test: `app/catalog/overlayplan/plan_test.go`, `app/catalog/worker/artwork/handler_test.go`, `app/remediation/artwork/overlay_test.go`, `test/starttest/renderer.go` if its assertions enumerate objects

**Interfaces:**
- Consumes: `overlay.CompactTemplateSpec` (Task 2), Task 4's names, Task 5's `compact` object.
- Produces:
  - `const overlayplan.CompactRenderVersion = 1`
  - `type overlayplan.Compact struct{ BaseVariant, BaseDigest, InputsDigest string }` and `Want.Compact Compact`
  - `func (w Want) WithCompact(it Item, compactDigest string) Want`: a `Want` with no profile is returned as is; else the base is `compact` with `compactDigest` when that is not empty, else `original` with `w.OriginalDigest`; `InputsDigest` is the hex SHA-256 of `"compact=%d\nbase=%s:%s\ntemplate=%s\n"` (version, variant, digest, `overlay.TemplateHash(overlay.CompactTemplateSpec(spec))`) followed by the same `rating=` and `dub=` lines `inputsDigest` writes
  - `func catalogartwork.CompactOverlayOf(idx *objindex.Index, kind commonv1.MediaKind, uid types.UID) (renderedFrom string, present bool)`: the `overlay-compact` object's `ArtworkMetaKeyRenderedFrom`
  - `OutcomeCompact Outcome = "compact"`: only the compact rendition was drawn
  - `decide(it, want, recorded, present, current, compactStale bool)`: as today, and `render` is also true when `compactStale`; with a profile the token is `want.InputsDigest + "+" + want.Compact.InputsDigest`

Behaviour to implement:
- `Handler.plan` reads the `compact` object's digest (`Store.Info`; absent is `""`) and returns `Plan(...).WithCompact(it, digest)`.
- `Handler.Render`: where it clears `overlay` it clears `overlay-compact` too. With a profile, after the regular rendition is settled as today, the compact one is: unchanged when the stored object's `Clustarr-Rendered-From` is `want.Compact.InputsDigest`, else drawn. `draw` becomes one function over a target (base key and digest, output variant, template, rendered-from) used for both; the compact object's `OverlayFacts.OriginalDigest` is its base's digest. The regular outcome is reported when the regular rendition changed, `OutcomeCompact` when only the compact one did.
- `OverlayStage.Plan`: `want` gets `WithCompact` with the index's `compact` digest; `compactStale` is: with a profile, `CompactOverlayOf` absent or not `want.Compact.InputsDigest`; with none, present. `status.overlay` is computed exactly as today.

- [ ] **Step 1: Write the failing tests**
  - `TestWithCompact`: no profile → unchanged; base is `compact` when given, else `original`; the digest changes with the base digest, with `CompactGeometry`, with a rating and with `Dubbed`, and does not change `w.InputsDigest`.
  - Handler (the existing fake store and fixtures): an item with a profile, an untitled `original` and no objects → both `overlay` and `overlay-compact` stored, the compact drawn from `original`, wider badge (decode both, compare the badge box's width against 19% and 28% of the poster within 2 px), outcome `rendered` (Review Focus 2). A second `Render` → `unchanged`, no Put. With a `compact` object present → the compact overlay's `original-digest` metadata is the compact's digest. Regular current, compact missing → only `overlay-compact` is Put, `overlay`'s digest and mod time untouched, outcome `compact`. Profile removed → both cleared (Review Focus 4). The compact base changing during the draw → `ErrInputsMoved`.
  - `decide` (table, added rows): everything current and `compactStale` → `render` true, status unchanged; no profile and `compactStale` (a leftover object) → `render` true; `DubUnread` → never renders; the token differs when only `Compact.InputsDigest` differs.
- [ ] **Step 2: Run** `go test ./app/catalog/overlayplan/ ./app/catalog/worker/artwork/ ./app/remediation/artwork/ ./app/catalog/artwork/ -count=1`. Expected: FAIL / does not compile.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the four packages and `go build ./test/...`.** Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 7: `/art/{kind}/{uid}/poster?size=compact`

**Files:**
- Modify: `ui/art.go` (`handleArt` 98, `isOverlay` 165), `ui/artindex.go` (`chooseArt` 47)
- Test: `ui/art_test.go`

**Interfaces:**
- Produces:
  - `const artSizeCompact = "compact"`
  - `func (s *Server) chooseArt(ctx, kind, uid, t, size string) (name string, e objindex.Entry, err error)`: `size == ""` is today's choice; `artSizeCompact` on an overlaid kind and type is `overlay-compact` (unless marked unreadable), else `compact`, else `events.ErrObjectNotFound`; anything else is `events.ErrObjectNotFound`
  - `func (s *Server) compactArt(kind commonv1.MediaKind, uid types.UID) (digest string, ok bool)`: the digest `chooseArt(..., artSizeCompact)` would serve, from the index only (false when the index is not synced)

- [ ] **Step 1: Write the failing tests** (table, through the mux, with the fake store and index the existing `/art` tests use)
  - `overlay-compact` and `compact` both stored → 200 with the `overlay-compact` bytes, `Content-Type: image/jpeg`; only `compact` → its bytes; `?size=compact&v=<digest>` carries the immutable `Cache-Control` the regular `?v=` does; `If-None-Match` with the returned ETag → 304.
  - Only `overlay` and `original` stored → 404 (Review Focus 3). `?size=large` → 404. `/art/movie/<uid>/fanart?size=compact` → 404. `/art/episode/<uid>/poster?size=compact` → 404.
  - No `size` → the regular choice, byte for byte what the test asserted before.
  - An `overlay-compact` whose read fails with `errArtMoved` → the `compact` bytes, as the regular overlay falls back to the original.
- [ ] **Step 2: Run** `go test ./ui/ -run 'Art' -count=1`. Expected: FAIL / does not compile.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./ui/ -count=1`. Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 8: The pairing list

**Files:**
- Create: `ui/plex/compact.go`, `ui/plex/compact_test.go`
- Modify: `ui/plex/provider.go` (`Options`, the handler's routes), `ui/routes.go:165` (pass `Compact`)

**Interfaces:**
- Consumes: `Server.compactArt` (Task 7); `artworkFor.url` (`ui/plex/images.go:106`); `projection.ArtURL`.
- Produces:
  - `Options.Compact func(kind commonv1.MediaKind, uid types.UID) (digest string, ok bool)`
  - `GET /plex/compact-posters` → `200 application/json`:

```json
{"posters":[{"url":"https://plex.example:443/art/movie/<uid>/poster?v=<digest>","compact":"/art/movie/<uid>/poster?size=compact&v=<compact digest>"}]}
```

  One entry per movie and per series of the projection index for which `artworkFor{kind, uid, artwork, overlay, contentType: Options.ArtContentType}.url(Options.ExternalURL, poster)` gives a URL and `Options.Compact` gives a digest. `url` is that URL unchanged (it is what Plex takes the SHA-1 of). Sorted by `url`. `ETag` is the quoted hex SHA-256 of the body; a matching `If-None-Match` answers 304. `Cache-Control: no-cache`. With `Options.Compact` nil or `ExternalURL` empty the list is empty, not an error.

- [ ] **Step 1: Write the failing tests**
  - A movie with `status.overlay` and a compact digest → one entry whose `url` equals what `handleMetadata`'s movie response carries as its `coverPoster` image URL for the same fixture (assert against the real response, not a rebuilt string).
  - A series with no `status.overlay` but a titled `compact` → its `url` is built on the original's digest.
  - A movie whose stored poster is WebP → `url` ends `&nowebp=1`.
  - A movie with no compact digest, an episode, an artist → absent.
  - Two calls → equal bodies and ETags; `If-None-Match` → 304 with no body.
- [ ] **Step 2: Run** `go test ./ui/plex/ -run 'Compact' -count=1`. Expected: FAIL / does not compile.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./ui/plex/ ./ui/ -count=1`. Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 9: Records, end to end, the gate

**Files:**
- Modify: `docs/claude/invariants.md` (216-229: "two writers, each with a regular and a compact variant"), `docs/claude/ui.md` (`/art`'s `size=compact`, `/plex/compact-posters`), `CLAUDE.md` (the one-line artwork invariant), `docs/claude/status.md`
- Modify: `test/e2e/main_test.go` (beside its overlay assertions)

- [ ] **Step 1: Extend the e2e case**: once the movie's `status.overlay` is set, `GET /art/movie/<uid>/poster?size=compact` answers 200 `image/jpeg` with bytes that differ from the regular poster's, and `GET /plex/compact-posters` lists that movie's regular URL.
- [ ] **Step 2: Write the records.** In `status.md`, the rollout cost: every titled poster is fetched once more (the plan's hash changed) and every item with an overlay gets one render task; no regular poster changes digest; the OverlayProfile CRD is applied by hand on talos before the roll.
- [ ] **Step 3: Run the gates once**: `make test`, `make lint`, `make brand-gate`, `make e2e`. Expected: all pass.
- [ ] **Step 4: Commit.** Push and deploy are the owner's word and the shared roll's.
