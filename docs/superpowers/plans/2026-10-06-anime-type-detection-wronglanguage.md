# Anime Type, Anime Detection and WrongLanguage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Phase 1 of the anime dual-audio design. Series type `anime` stops
forcing absolute episode order. A RootFolder's `defaults.anime` moves a
TVDB-"Anime" series to the anime profile and type once. A file whose audio
lacks the profile's language reads `WrongLanguage` and is searched for a
replacement.

**Architecture:**
- `series.EffectiveEpisodeOrder`, and its `ui/plex` mirror, drop the anime
  override.
- The Series reconciler gains a once-only classifier. It merge-patches two
  spec fields under a new field manager and records
  `status.classification`.
- Import lists send a classified series' current profile and type instead
  of their own defaults.
- The language verdict is one pure function in `pkg/decision`, fed by the
  probe's normalised audio languages. The Episode and Movie reconcilers use
  it for a condition and the cutoff; search uses it so a same-quality,
  right-language release counts as an upgrade.

**Tech Stack:** Go, controller-runtime, controller-gen v0.22.0, envtest (`make test`), testify.

**Spec:** `docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md` (§3, §4, §5.3).

## Global Constraints

- GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go file.
- All status writes go through `pkg/k8s.PatchStatus`. A manager's apply is
  a complete declaration of everything it owns (CLAUDE.md, gotchas).
- No `float32`/`float64` in `api/`. Cap every status list with `MaxItems`.
- One field manager per writer. The classifier writes spec under
  `k8s.ManagerCatalogarrClassify` = `"catalogarr-classify"`, and nothing
  else uses that name.
- After any `api/` change run `make generate manifests`, and commit the
  generated files with the change.
- Commit by pathspec (`git commit -m ... -- <paths>`). Never `git stash`,
  `go get` or `go mod tidy`. Other sessions share this checkout.
- `ui/plex/` belongs to session clustarr-d2. Tell them before editing
  `ui/plex/extended.go` (Task 1).
- The gate is `make test` in a clean worktree, with `helm dependency build`
  run or the vendored chart `.tgz` files copied in.

## Review Focus

1. **An import list re-applies a series it added after the classifier moved
   it.** The profile and type must stay. Test in Task 4.
2. **The owner changes a classified series' profile.** The change must stay
   through later reconciles. Test in Task 3.
3. **A file with an untagged (`und`) or empty-language audio track.** It
   must not read `WrongLanguage`: unknown is not wrong. Test in Task 5.
4. **A series' metadata arrives before its RootFolder has
   `defaults.anime`.** No classification is recorded, so setting the
   defaults later still classifies it. Test in Task 3.
5. **Early returns (QueueFull, RootFolderNotFound) after classification.**
   They must re-send `status.classification`, not release it. Test in
   Task 3.

---

### Task 1: `anime` keeps the episode order

**Files:**
- Modify: `app/catalog/controller/series/order.go:27-42`
- Modify: `app/catalog/controller/series/order_test.go:30-48`
- Modify: `ui/plex/extended.go:105-118` (clustarr-d2's directory: message them first)
- Test: `ui/plex/extended_test.go` (add a case)
- Modify: `docs/superpowers/specs/2026-09-18-clustarr-design.md:226`

**Interfaces:**
- Produces: `series.EffectiveEpisodeOrder(seriesType, order) EpisodeOrder`
  returns `order`, or official when it is empty, for every series type.

- [ ] **Step 1: Write the failing test.** Replace the two anime rows in
  `TestEffectiveEpisodeOrder`:

```go
		{"anime keeps the official order", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderOfficial},
		{"anime keeps an explicit dvd order", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderDVD},
		{"anime keeps an explicit absolute order", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderAbsolute, catalogv1alpha1.EpisodeOrderAbsolute},
		{"anime falls back to official on the Go zero value", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrder(""), catalogv1alpha1.EpisodeOrderOfficial},
```

- [ ] **Step 2: Run it.** `go test ./app/catalog/controller/series/ -run TestEffectiveEpisodeOrder`.
  Expected: FAIL on "anime keeps the official order" (got absolute).

- [ ] **Step 3: Implement.** In `order.go`, delete the
  `if seriesType == catalogv1alpha1.SeriesTypeAnime { return ... }` block
  and rewrite the doc comment:

```go
// EffectiveEpisodeOrder returns the numbering scheme actually used: the
// requested order, falling back to official when order is the Go zero
// value. Series type anime no longer forces absolute order (2026-10-06,
// docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md §3): with
// TVDB, absolute order puts every episode in season 1, which collapsed a
// multi-season show's seasons. An anime reads absolute numbers for search,
// import and naming from status.absoluteNumber instead, and a series that
// wants absolute order sets spec.episodeOrder: absolute. seriesType stays
// a parameter so the callers read as before.
func EffectiveEpisodeOrder(_ catalogv1alpha1.SeriesType, order catalogv1alpha1.EpisodeOrder) catalogv1alpha1.EpisodeOrder {
	if order == "" {
		return catalogv1alpha1.EpisodeOrderOfficial
	}
	return order
}
```

- [ ] **Step 4: Run it.** Same command. Expected: PASS.

- [ ] **Step 5: The `ui/plex` mirror, test first.** After clustarr-d2
  acknowledges, add to `ui/plex/extended_test.go`:

```go
func TestEffectiveOrderAnimeKeepsTheOfficialOrder(t *testing.T) {
	s := &catalogv1.Series{Spec: catalogv1.SeriesSpec{SeriesType: catalogv1.SeriesTypeAnime}}
	require.Equal(t, catalogv1.EpisodeOrderOfficial, effectiveOrder(s))
	s.Spec.EpisodeOrder = catalogv1.EpisodeOrderAbsolute
	require.Equal(t, catalogv1.EpisodeOrderAbsolute, effectiveOrder(s))
}
```

  Run `go test ./ui/plex/ -run TestEffectiveOrderAnime`. Expected: FAIL. Then
  delete the anime branch in `effectiveOrder`, update its comment ("mirrors
  series.EffectiveEpisodeOrder: the requested order, official when unset"),
  and rerun. Expected: PASS. Then run `go test ./ui/plex/ ./app/catalog/...`.
  Expected: PASS. A golden or a markers test that asserted the old anime
  behaviour is updated to the new rule; list each one in the commit message.

- [ ] **Step 6: Amend the design of record.** At
  `2026-09-18-clustarr-design.md:226`, change the comment
  `// absolute forced when anime` to
  `// anime no longer forces absolute (2026-10-06 anime dual-audio spec §3)`.

- [ ] **Step 7: Commit.**

```bash
git commit -m "fix(catalog): series type anime keeps the episode order -- absolute order put every episode of a multi-season anime in season 1; anime reads absolute numbers for search, import and naming instead" -- app/catalog/controller/series/order.go app/catalog/controller/series/order_test.go ui/plex/extended.go ui/plex/extended_test.go docs/superpowers/specs/2026-09-18-clustarr-design.md
```

### Task 2: API: anime defaults, classification, the classify manager

**Files:**
- Modify: `api/catalog/v1alpha1/rootfolder_types.go` (in `RootDefaults`, after `SeriesType`, about line 151)
- Modify: `api/catalog/v1alpha1/series_types.go` (`SeriesStatus` at line 420; constants block)
- Modify: `api/catalog/v1alpha1/episode_types.go:32` and `movie_types.go:36` (condition constants)
- Modify: `pkg/k8s/fieldmanager.go` (after `ManagerCatalogarrMarkers`, line 164)
- Modify: `pkg/k8s/fieldmanager_test.go` (whatever list it holds)
- Generated: `make generate manifests` output

**Interfaces:**
- Produces:
  - `RootDefaults.Anime *AnimeDefaults` and `AnimeDefaults{QualityProfileRef string; SeriesType SeriesType}`.
  - `SeriesStatus.Classification *SeriesClassification` and
    `SeriesClassification{Anime bool; AppliedAt metav1.Time; QualityProfileRef string; SeriesType SeriesType}`.
  - Constants `catalogv1alpha1.AnnotationClassify = "catalog.clustarr.io/classify"`,
    `EpisodeConditionWrongLanguage = "WrongLanguage"`,
    `MovieConditionWrongLanguage = "WrongLanguage"` and
    `k8s.ManagerCatalogarrClassify FieldManager = "catalogarr-classify"`.

- [ ] **Step 1: Add the types.** In `rootfolder_types.go`, inside `RootDefaults`:

```go
	// Anime are the defaults a series under this folder takes once its
	// metadata shows it is anime (TVDB genre "Anime"); the Series
	// reconciler applies them once and records status.classification.
	// Unset: no detection.
	// +optional
	Anime *AnimeDefaults `json:"anime,omitempty"`
```

  and below `RootDefaults`:

```go
// AnimeDefaults are what an anime series is moved to on classification.
type AnimeDefaults struct {
	// QualityProfileRef is the profile an anime series is moved to.
	// +kubebuilder:validation:MinLength=1
	QualityProfileRef string `json:"qualityProfileRef"`

	// SeriesType is the series type an anime series is moved to.
	// +optional
	// +kubebuilder:default=anime
	SeriesType SeriesType `json:"seriesType,omitempty"`
}
```

  In `series_types.go`, inside `SeriesStatus` after `Conditions`:

```go
	// Classification records the one-time anime detection: once set, the
	// Series reconciler never classifies the series again, so a profile or
	// type the owner changes afterwards stays.
	// +optional
	Classification *SeriesClassification `json:"classification,omitempty"`
```

  and the type plus the annotation constant:

```go
// AnnotationClassify set to "off" skips anime classification for a Series.
const AnnotationClassify = "catalog.clustarr.io/classify"

// SeriesClassification is the result of the one-time anime detection.
type SeriesClassification struct {
	// Anime is true when the series' metadata named it anime.
	Anime bool `json:"anime"`
	// AppliedAt is when it was classified.
	AppliedAt metav1.Time `json:"appliedAt"`
	// QualityProfileRef is the profile applied, empty when none was.
	// +optional
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`
	// SeriesType is the series type applied, empty when none was.
	// +optional
	SeriesType SeriesType `json:"seriesType,omitempty"`
}
```

  Condition constants beside the CutoffMet ones:

```go
	// EpisodeConditionWrongLanguage is True when the file's audio lacks the
	// language the profile wants (decision.LacksLanguage).
	EpisodeConditionWrongLanguage = "WrongLanguage"
```

  and the same as `MovieConditionWrongLanguage`. In `fieldmanager.go`:

```go
	// ManagerCatalogarrClassify is the Series reconciler's one-time anime
	// classification: it merge-patches spec.seriesType and
	// spec.qualityProfileRef once, never again for that Series. Distinct
	// from every creator's manager (importarr, importarr-worker, clustarr-ui),
	// so the patch reads in managedFields as the classifier's.
	ManagerCatalogarrClassify FieldManager = "catalogarr-classify"
```

- [ ] **Step 2: Generate.** `make generate manifests`. Expected: deepcopy,
  applyconfigurations (`RootDefaults.WithAnime`, `AnimeDefaults`,
  `SeriesStatus.WithClassification`, `SeriesClassification`) and the CRDs
  for rootfolders and series change. `git status --short` lists only those
  files plus yours.

- [ ] **Step 3: Run the guards.** `go test ./pkg/k8s/ ./pkg/crdcheck/ ./api/...`
  with `KUBEBUILDER_ASSETS` exported. Expected: PASS. If
  `TestNoCRDDefaultIsUnreachableFromGo` flags `AnimeDefaults.SeriesType`
  (an `omitempty` string defaulted to anime is reachable: "" is absent),
  read its message and follow it. If `fieldmanager_test.go` enumerates the
  managers, add the new one there first and watch it fail.

- [ ] **Step 4: Commit.**

```bash
git commit -m "feat(api): RootFolder defaults.anime, Series status.classification, the catalogarr-classify manager and the WrongLanguage conditions" -- api/ config/ charts/clustarr/crds pkg/k8s/fieldmanager.go pkg/k8s/fieldmanager_test.go
```

  (Check where the chart keeps CRDs with `git status`, and list exactly
  those paths.)

### Task 3: The Series reconciler classifies anime once

**Files:**
- Create: `app/catalog/controller/series/classify.go`
- Create: `app/catalog/controller/series/classify_test.go` (pure function)
- Modify: `app/catalog/controller/series/reconciler.go:350-389` (after the
  RootFolder Get), `:435` (the happy-path apply) and `:492`
  (`reassertKnownStatus`)
- Test: `app/catalog/controller/series/classify_envtest_test.go`

**Interfaces:**
- Consumes: Task 2's types and `k8s.ManagerCatalogarrClassify`.
- Produces: `series.Classify(s *Series, rf *RootFolder) (c *SeriesClassification, patch bool)`.

- [ ] **Step 1: Write the pure tests** in `classify_test.go`:

```go
func TestClassify(t *testing.T) {
	anime := &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "anime-web-1080p", SeriesType: catalogv1alpha1.SeriesTypeAnime}
	rf := func(a *catalogv1alpha1.AnimeDefaults) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Defaults: catalogv1alpha1.RootDefaults{Anime: a}}}
	}
	ser := func(genres ...string) *catalogv1alpha1.Series {
		return &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{Genres: genres}}}
	}
	t.Run("an anime series is moved", func(t *testing.T) {
		c, patch := series.Classify(ser("Animation", "Anime"), rf(anime))
		require.True(t, patch)
		require.Equal(t, &catalogv1alpha1.SeriesClassification{Anime: true, QualityProfileRef: "anime-web-1080p", SeriesType: catalogv1alpha1.SeriesTypeAnime}, c)
	})
	t.Run("a non-anime series is recorded, not moved", func(t *testing.T) {
		c, patch := series.Classify(ser("Drama"), rf(anime))
		require.False(t, patch)
		require.Equal(t, &catalogv1alpha1.SeriesClassification{Anime: false}, c)
	})
	t.Run("the genre matches case-insensitively", func(t *testing.T) {
		_, patch := series.Classify(ser("anime"), rf(anime))
		require.True(t, patch)
	})
	t.Run("no anime defaults: nothing is recorded", func(t *testing.T) {
		c, patch := series.Classify(ser("Anime"), rf(nil))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("already classified: never again", func(t *testing.T) {
		s := ser("Anime")
		s.Status.Classification = &catalogv1alpha1.SeriesClassification{Anime: false}
		c, patch := series.Classify(s, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("classify off: skipped", func(t *testing.T) {
		s := ser("Anime")
		s.Annotations = map[string]string{catalogv1alpha1.AnnotationClassify: "off"}
		c, patch := series.Classify(s, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("no metadata yet: nothing is recorded", func(t *testing.T) {
		c, patch := series.Classify(&catalogv1alpha1.Series{}, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("an empty seriesType defaults to anime", func(t *testing.T) {
		c, _ := series.Classify(ser("Anime"), rf(&catalogv1alpha1.AnimeDefaults{QualityProfileRef: "p"}))
		require.Equal(t, catalogv1alpha1.SeriesTypeAnime, c.SeriesType)
	})
}
```

- [ ] **Step 2: Run it.** `go test ./app/catalog/controller/series/ -run TestClassify`.
  Expected: FAIL (`undefined: series.Classify`).

- [ ] **Step 3: Implement `classify.go`.** Use the GPL header.

```go
package series

import (
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// animeGenre is TheTVDB's genre for anime (19 of 150 series on the owner's
// library, 2026-10-06).
const animeGenre = "Anime"

// Classify is the one-time anime detection (anime dual-audio spec §4). It
// returns nil when there is nothing to record yet -- classification is
// off, already done, the metadata has not arrived, or the RootFolder sets
// no anime defaults (so setting them later still classifies) -- and
// otherwise the classification to record, with patch true when s's spec
// must take the anime profile and type. AppliedAt is the caller's to set.
func Classify(s *catalogv1alpha1.Series, rf *catalogv1alpha1.RootFolder) (*catalogv1alpha1.SeriesClassification, bool) {
	if s.Annotations[catalogv1alpha1.AnnotationClassify] == "off" || s.Status.Classification != nil ||
		s.Status.Metadata == nil || rf == nil || rf.Spec.Defaults.Anime == nil {
		return nil, false
	}
	anime := false
	for _, g := range s.Status.Metadata.Genres {
		if strings.EqualFold(g, animeGenre) {
			anime = true
			break
		}
	}
	if !anime {
		return &catalogv1alpha1.SeriesClassification{Anime: false}, false
	}
	d := rf.Spec.Defaults.Anime
	st := d.SeriesType
	if st == "" {
		st = catalogv1alpha1.SeriesTypeAnime
	}
	return &catalogv1alpha1.SeriesClassification{Anime: true, QualityProfileRef: d.QualityProfileRef, SeriesType: st}, true
}
```

- [ ] **Step 4: Run it.** Expected: PASS.

- [ ] **Step 5: Write the envtest** `classify_envtest_test.go`. Mirror
  `TestSeriesEnsureEpisodeKeepsItsPlexID`'s setup (`startCacheOnly`,
  `testNamespace`, `fakeEpisodeRPC`, `combinedBus`). Create a RootFolder
  whose `Spec.Defaults.Anime = &AnimeDefaults{QualityProfileRef: "anime-web-1080p"}`
  and a Series `{TvdbID: 79824, QualityProfileRef: "web-1080p", RootFolderRef: ...}`.
  Give it metadata with `k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Series(...).WithStatus(catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle("Naruto").WithGenres("Animation", "Anime").WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion))))`,
  so it is fresh and current. Then assert, each step with
  `require.Eventually`, reading through a direct client where the
  assertion is on managedFields:
  1. After `r.Reconcile`, the spec reads `QualityProfileRef == "anime-web-1080p"`
     and `SeriesType == anime`, and `status.classification.anime` is true
     with a non-zero `appliedAt`.
  2. In managedFields, `catalogarr-classify` owns `f:spec` /
     `f:qualityProfileRef` (an `Update` entry), and `catalogarr` owns
     `f:status` / `f:classification`.
  3. **The owner's change sticks:** merge-patch `spec.qualityProfileRef`
     to `"web-1080p"` under `client.FieldOwner("clustarr-ui")`, reconcile
     twice, and the profile stays `"web-1080p"`.
  4. **Early return keeps it** (Review Focus 5): delete the RootFolder,
     reconcile (the RootFolderNotFound path), and
     `status.classification.anime` is still true.
  5. **Defaults set later still classify** (Review Focus 4): a second
     Series under a RootFolder without `defaults.anime` reconciles to no
     classification. Patch the folder's `defaults.anime` in, reconcile
     again, and it is classified.

- [ ] **Step 6: Run it.** `make test` scoped:
  `KUBEBUILDER_ASSETS=$(setup-envtest use -p path 1.37.0) go test ./app/catalog/controller/series/ -run TestSeriesClassif -v`.
  Expected: FAIL at assertion 1 (spec unchanged).

- [ ] **Step 7: Wire the reconciler.** In `reconcileNormal`, inside
  `if s.Status.Metadata != nil {` right after the RootFolder `Get`
  succeeds:

```go
		if c, patch := Classify(s, &rf); c != nil {
			if patch {
				body, err := json.Marshal(map[string]any{"spec": map[string]any{
					"qualityProfileRef": c.QualityProfileRef, "seriesType": c.SeriesType,
				}})
				if err != nil {
					return ctrl.Result{}, err
				}
				if err := r.Patch(ctx, s.DeepCopy(), client.RawPatch(types.MergePatchType, body),
					client.FieldOwner(k8s.ManagerCatalogarrClassify)); err != nil {
					return ctrl.Result{}, err
				}
				r.normal(s, "Classified", "anime: quality profile %s, series type %s", c.QualityProfileRef, c.SeriesType)
			}
			c.AppliedAt = metav1.NewTime(now)
			classification = c
		}
```

  Declare `classification := s.Status.Classification` at the top of
  `reconcileNormal`. Before the happy-path `PatchStatus` (line 435) add:

```go
	if classification != nil {
		statusAC = statusAC.WithClassification(classificationAC(classification))
	}
```

  Add a helper next to `seasonACs`. Being the one renderer, it keeps the
  happy path and `reassertKnownStatus` declaring the same leaves:

```go
// classificationAC renders status.classification, shared by the happy path
// and reassertKnownStatus so both declare the same leaves.
func classificationAC(c *catalogv1alpha1.SeriesClassification) *catalogac.SeriesClassificationApplyConfiguration {
	ac := catalogac.SeriesClassification().WithAnime(c.Anime).WithAppliedAt(c.AppliedAt)
	if c.QualityProfileRef != "" {
		ac = ac.WithQualityProfileRef(c.QualityProfileRef)
	}
	if c.SeriesType != "" {
		ac = ac.WithSeriesType(c.SeriesType)
	}
	return ac
}
```

  In `reassertKnownStatus`, add
  `if s.Status.Classification != nil { statusAC = statusAC.WithClassification(classificationAC(s.Status.Classification)) }`.

  Do not let the merge patch's new generation re-run this pass. The next
  reconcile reads `status.classification` and skips classifying.

- [ ] **Step 8: Run it.** Same command as Step 6, then
  `go test ./app/catalog/controller/series/` with the assets. Expected:
  PASS, including `TestSeriesEpisodeFieldManagersStayDisjoint` and
  `TestSeriesReconcilerTransientFailuresPreserveSteadyState`.

- [ ] **Step 9: Falsify.** Delete the `reassertKnownStatus` line and run
  assertion 4. It must fail by name. Restore it.

- [ ] **Step 10: Commit.**

```bash
git add app/catalog/controller/series/classify.go app/catalog/controller/series/classify_test.go app/catalog/controller/series/classify_envtest_test.go
git commit -m "feat(catalog): a series whose TVDB genres name it Anime takes its RootFolder's anime profile and type once, recorded in status.classification so the owner's later change stays" -- app/catalog/controller/series/
```

### Task 4: Import lists keep a classified series' profile and type

**Files:**
- Modify: `app/import/worker/importlist/catalogitem.go:193-222` (`applySeries`)
- Create: `app/import/worker/importlist/classified_envtest_test.go`, built on the
  package's `helpers_envtest_test.go` harness (no existing test calls
  `applySeries` directly)

**Interfaces:**
- Consumes: `SeriesStatus.Classification` (Task 2).
- Produces: no new API. `applySeries` keeps its signature.

- [ ] **Step 1: Write the failing test** with the envtest client from
  `helpers_envtest_test.go`. Create the Series as the list would, then
  simulate the classifier with a merge patch to `anime-web-1080p` and type
  `anime`, and set `status.classification{Anime: true}` through the status
  client. Call `applySeries(ctx, c, ns, "trakt", name, item, defaults{QualityProfileRef: "web-1080p"}, id)`
  again and assert:

```go
	require.Equal(t, "anime-web-1080p", got.Spec.QualityProfileRef, "a list re-apply must not undo the anime classification")
	require.Equal(t, catalogv1alpha1.SeriesTypeAnime, got.Spec.SeriesType)
```

- [ ] **Step 2: Run it.** `go test ./app/import/worker/importlist/ -run Classif`.
  Expected: FAIL (profile back to web-1080p).

- [ ] **Step 3: Implement.** At the top of `applySeries`, after `spec` is
  built and before the `k8s.Apply`:

```go
	// A series the catalogarr classifier moved to its RootFolder's anime
	// defaults keeps them: this apply forces ownership, so re-sending the
	// list's defaults would undo the classification on every sync.
	var existing catalogv1alpha1.Series
	switch err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &existing); {
	case err == nil:
		if cl := existing.Status.Classification; cl != nil && cl.Anime {
			spec = spec.WithQualityProfileRef(existing.Spec.QualityProfileRef).WithSeriesType(existing.Spec.SeriesType)
		}
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("importlist: get series %s: %w", name, err)
	}
```

  This sends the series' current values, not the classification's, so a
  later change by the owner stays too.

- [ ] **Step 4: Run it.** Expected: PASS. Then
  `go test ./app/import/worker/importlist/`. Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add app/import/worker/importlist/classified_envtest_test.go
git commit -m "fix(import): an import list's re-apply keeps a classified anime series' profile and type rather than forcing its own defaults back" -- app/import/worker/importlist/catalogitem.go app/import/worker/importlist/classified_envtest_test.go
```

### Task 5: The language verdict, and an upgrade over a wrong-language file

**Files:**
- Create: `pkg/decision/audiolanguage.go`
- Create: `pkg/decision/audiolanguage_test.go`
- Modify: `pkg/decision/types.go:41-49` (`Current`)
- Modify: `pkg/decision/checks.go:188-205` (`upgradeRejection`)
- Create: `app/catalog/controller/rollup/audio.go` and `audio_test.go`
- Modify: `app/catalog/worker/search/snapshot.go:250-259` (`CurrentFile`)

**Interfaces:**
- Produces:
  - `decision.LacksLanguage(p quality.Profile, originalTag string, audio []string) bool`
  - `decision.Current.AudioLanguages []string` (normalised BCP-47 tags;
    nil means unknown)
  - `rollup.ProbedAudioLanguages(mf *catalogv1alpha1.MediaFile) []string`

- [ ] **Step 1: Write the failing tests.** `audiolanguage_test.go`:

```go
func TestLacksLanguage(t *testing.T) {
	orig := quality.Profile{Language: "original"}
	cases := []struct {
		name  string
		p     quality.Profile
		orig  string
		audio []string
		want  bool
	}{
		{"korean-only file of a japanese show", orig, "ja", []string{"ko"}, true},
		{"dual audio carries the original", orig, "ja", []string{"en", "ja"}, false},
		{"region subtags compare by base", quality.Profile{Language: "pt"}, "", []string{"pt-BR"}, false},
		{"unknown audio is not wrong", orig, "ja", nil, false},
		{"unknown original constrains nothing", orig, "", []string{"ko"}, false},
		{"any wants nothing", quality.Profile{Language: "any"}, "ja", []string{"ko"}, false},
		{"empty wants nothing", quality.Profile{}, "ja", []string{"ko"}, false},
		{"an explicit tag", quality.Profile{Language: "en"}, "ja", []string{"ja"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, decision.LacksLanguage(c.p, c.orig, c.audio)) })
	}
}

```

  and a subtest appended to `TestUpgradeRejection` in `pkg/decision/checks_test.go`
  (internal package, so `p`, `bluray1080` and `upgradeRejection` are in scope):

```go
	t.Run("a current file lacking the profile's language is replaced at the same quality", func(t *testing.T) {
		lp := p
		lp.Language = "original"
		same := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		tg := Target{OriginalLanguageTag: "ja", Current: &Current{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}, AudioLanguages: []string{"ko"}}}
		require.Nil(t, upgradeRejection(lp, tg, same))
		tg.Current.AudioLanguages = []string{"ja"}
		require.NotNil(t, upgradeRejection(lp, tg, same), "a right-language file is not replaced by the same quality")
	})
```

  `rollup/audio_test.go`:

```go
func TestProbedAudioLanguages(t *testing.T) {
	mf := func(langs ...string) *catalogv1alpha1.MediaFile {
		var a []commonv1.AudioStream
		for _, l := range langs {
			a = append(a, commonv1.AudioStream{Language: l})
		}
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{Audio: a}}}
	}
	require.Equal(t, []string{"en", "ja"}, rollup.ProbedAudioLanguages(mf("eng", "jpn")))
	require.Equal(t, []string{"ko"}, rollup.ProbedAudioLanguages(mf("kor")))
	require.Nil(t, rollup.ProbedAudioLanguages(mf("jpn", "und")), "one untagged track makes the set unknown")
	require.Nil(t, rollup.ProbedAudioLanguages(mf("jpn", "")), "an empty tag too")
	require.Nil(t, rollup.ProbedAudioLanguages(mf()), "no audio streams: unknown")
	require.Nil(t, rollup.ProbedAudioLanguages(&catalogv1alpha1.MediaFile{}), "not probed")
	require.Nil(t, rollup.ProbedAudioLanguages(nil))
}
```

- [ ] **Step 2: Run them.** `go test ./pkg/decision/ ./app/catalog/controller/rollup/ -run 'LacksLanguage|WrongLanguage|ProbedAudio'`.
  Expected: FAIL (undefined).

- [ ] **Step 3: Implement.** `pkg/decision/audiolanguage.go`:

```go
// LacksLanguage reports whether a file whose probed audio languages are
// audio (canonical BCP-47 tags; nil when unknown) lacks the language
// profile p wants: the item's original language for "original", the tag
// itself for a tag, nothing for "any" or "". Unknown audio or an unknown
// original language is never "lacking" -- the verdict only fires on a
// fact. Tags compare by their base language ("pt-BR" carries "pt").
func LacksLanguage(p quality.Profile, originalTag string, audio []string) bool {
	if audio == nil {
		return false
	}
	want := p.Language
	switch want {
	case "", "any":
		return false
	case "original":
		want = originalTag
	}
	w, ok := lang.Normalize(want)
	if !ok {
		return false
	}
	base := func(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }
	for _, a := range audio {
		if base(a) == base(string(w)) {
			return false
		}
	}
	return true
}
```

  Add to `Current`:

```go
	// AudioLanguages are the file's probed audio languages, canonical
	// BCP-47 (rollup.ProbedAudioLanguages); nil when unknown. A file that
	// lacks the profile's language (LacksLanguage) is replaced by any
	// accepted release, whatever its quality.
	AudioLanguages []string
```

  In `upgradeRejection` (it already receives the whole Target), after the
  `t.Current == nil` check:

```go
	if LacksLanguage(p, t.OriginalLanguageTag, t.Current.AudioLanguages) {
		return nil // the current file lacks the profile's language: anything accepted replaces it
	}
```

  `rollup/audio.go`:

```go
// ProbedAudioLanguages returns mf's audio track languages as canonical
// BCP-47 tags in stream order, deduplicated, or nil when they are not all
// known: not probed, no audio stream, or any track untagged ("und", "" or
// unparseable). One unknown track makes the whole set unknown, since that
// track may be the wanted language.
func ProbedAudioLanguages(mf *catalogv1alpha1.MediaFile) []string {
	if mf == nil || mf.Status.MediaInfo == nil || len(mf.Status.MediaInfo.Audio) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range mf.Status.MediaInfo.Audio {
		t, ok := lang.Normalize(a.Language)
		if !ok {
			return nil
		}
		if !seen[string(t)] {
			seen[string(t)] = true
			out = append(out, string(t))
		}
	}
	return out
}
```

  In `search.CurrentFile`, set `AudioLanguages: rollup.ProbedAudioLanguages(&mf)`
  in the `cur` literal. The RSS matcher reads `search.CurrentFile`, so it
  is covered.

- [ ] **Step 4: Run them.** Same command, then `go test ./pkg/decision/ ./app/catalog/...`.
  Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add pkg/decision/audiolanguage.go pkg/decision/audiolanguage_test.go app/catalog/controller/rollup/audio.go app/catalog/controller/rollup/audio_test.go
git commit -m "feat(decision): a file whose probed audio lacks the profile's language is replaced by any accepted release -- decision.LacksLanguage over rollup.ProbedAudioLanguages, unknown audio never lacking" -- pkg/decision/ app/catalog/controller/rollup/audio.go app/catalog/controller/rollup/audio_test.go app/catalog/worker/search/snapshot.go
```

### Task 6: Episode and Movie read `WrongLanguage`

**Files:**
- Modify: `app/catalog/controller/episode/reconciler.go:455-512`
- Modify: `app/catalog/controller/movie/reconciler.go:532-622`
- Test: `app/catalog/controller/episode/wronglanguage_envtest_test.go`
  (mirror the package's existing envtest setup; find it with
  `grep -ln 'func Test.*Cutoff' app/catalog/controller/episode/*_test.go`)
- Test: the same for movie

**Interfaces:**
- Consumes: `decision.LacksLanguage`, `rollup.ProbedAudioLanguages`, and
  `EpisodeConditionWrongLanguage` / `MovieConditionWrongLanguage`.

- [ ] **Step 1: Write the failing envtest (Episode).** It needs:
  - a Series with `status.metadata.originalLanguage: ja`;
  - a QualityProfile with `language: original`. Use a built-in seed name
    the package's tests already create, or create one;
  - an Episode with a MediaFile whose `status.mediaInfo.audio` is
    `[{language: kor}]` and whose quality meets the cutoff.

  After reconcile, assert all of:
  - condition `WrongLanguage` is True;
  - `CutoffMet` is False with reason `WrongLanguage`;
  - `status.cutoffMet` is false.

  Then patch the MediaFile's audio to `[{language: jpn}]`, reconcile, and
  assert `WrongLanguage` is False and `CutoffMet` is True. Last, make the
  file transcoded (`spec.original: false`) with Korean audio: `CutoffMet`
  stays True with reason `Transcoded`, and `WrongLanguage` reads True. A
  transcoded file stays final; the condition only reports.

- [ ] **Step 2: Run it.** Expected: FAIL (no WrongLanguage condition).

- [ ] **Step 3: Implement (Episode).** After `FileState(...)`:

```go
	originalTag := ""
	if series != nil && series.Status.Metadata != nil {
		originalTag = series.Status.Metadata.OriginalLanguage
	}
	wrongLanguage := profile != nil && hasFile && decision.LacksLanguage(*profile, originalTag, rollup.ProbedAudioLanguages(mf))
	if wrongLanguage && !transcoded {
		cutoffMet = false
	}
```

  This goes before `Phase(...)` is computed, so the phase reads
  CutoffUnmet and the wanted sweep searches it. In the CutoffMet `switch`,
  add an arm before `case cutoffMet:`:

```go
	case wrongLanguage:
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "WrongLanguage", "the file's audio lacks the profile's language")
```

  After the switch:

```go
	if wrongLanguage {
		k8s.MarkTrue(ep, &conditions, catalogv1alpha1.EpisodeConditionWrongLanguage, "WrongLanguage",
			"the file's audio %v lacks the profile's language", rollup.ProbedAudioLanguages(mf))
	} else {
		k8s.MarkFalse(ep, &conditions, catalogv1alpha1.EpisodeConditionWrongLanguage, "LanguageOK", "no evidence the file's audio is wrong")
	}
```

  Check the Episode conditions `MaxItems`. A new condition type must fit
  under it; raise the cap in Task 2's API commit if it is reached.

- [ ] **Step 4: Run it.** Expected: PASS, and the whole package passes.

- [ ] **Step 5: Movie.** Repeat Steps 1-4 for the Movie reconciler, with
  `originalTag` from `m.Status.Metadata.OriginalLanguage` and the
  `MovieCondition*` constants.

- [ ] **Step 6: Commit.**

```bash
git add <the two new test files>
git commit -m "feat(catalog): an episode or movie whose file's audio lacks the profile's language reads WrongLanguage and CutoffUnmet, so the wanted sweep looks for a replacement; a transcoded file stays final" -- app/catalog/controller/episode/ app/catalog/controller/movie/
```

### Task 7: Docs, gate, deploy, rollout

**Files:**
- Modify: `CLAUDE.md` (UI section: one paragraph after the Plex-native
  GUIDs paragraph)
- Modify: `docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md`
  (add "As built: phase 1")

- [ ] **Step 1: CLAUDE.md paragraph.**

```markdown
Series type `anime` keeps the episode order (2026-10-06,
`docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md`): it reads
absolute numbers for search, import and naming from
`status.absoluteNumber`, and only `spec.episodeOrder: absolute` changes
the order. A RootFolder's `defaults.anime` moves a series whose TVDB
genres name it Anime to that profile and type once
(`series.Classify`, `status.classification`, field manager
`catalogarr-classify`); `catalog.clustarr.io/classify: off` skips one, and
an import list's re-apply keeps a classified series' values. A file whose
probed audio lacks the profile's language (`decision.LacksLanguage`;
unknown audio never lacks) reads `WrongLanguage` and CutoffUnmet, and any
accepted release replaces it.
```

- [ ] **Step 2: Commit the docs** by pathspec.

- [ ] **Step 3: Gate.** In a clean worktree at HEAD, copy in the chart
  tarballs and run `make test`. Expected: every package `ok`. A failure
  is fixed through superpowers:systematic-debugging, never skipped.

- [ ] **Step 4: Measure before deploying.** On the cluster, count the
  MediaFiles that will read `WrongLanguage`. For each item, take its
  original language and its profile's `language`, apply the same rule with
  `jq` over `kubectl get mediafiles,episodes,movies,series -o json`, and
  report the count and the top series in the final message. If it is over
  a few hundred, stop and show the owner before deploying.

- [ ] **Step 5: Coordinate.** Ask clustarr-d2 whether My Happy Marriage,
  Solo Leveling and The Dangers in My Heart are finished. If not, annotate
  them with `catalog.clustarr.io/classify=off` before deploying.

- [ ] **Step 6: Push and deploy.**
  - Push the commits (`git push origin <sha>:main`, fast-forward only).
  - Build the controller and media images and load them into kind.
  - Upgrade helm from `helm get values` with both tags changed.
  - Apply the CRDs first if the chart does not; check how the last deploy
    did it.

- [ ] **Step 7: Turn detection on.**

```bash
kubectl --context kind-cluster-plex -n clustarr-system patch rootfolder tv --type merge -p '{"spec":{"defaults":{"anime":{"qualityProfileRef":"anime-web-1080p"}}}}'
```

  Then watch until all 19 anime series read `status.classification.anime: true`
  with `spec.qualityProfileRef: anime-web-1080p` and `spec.seriesType: anime`.
  Check that no Episode was created or deleted for the seven multi-season
  ones (count per series before and after), and that every non-anime series
  reads `classification.anime: false` with its profile unchanged.

- [ ] **Step 8: Verify WrongLanguage live.** Monster S01E01 (the Korean
  file) reads `WrongLanguage` True and CutoffUnmet. Report the library-wide
  count against Step 4's estimate.
