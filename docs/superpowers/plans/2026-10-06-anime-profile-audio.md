# Profile Audio Languages Implementation Plan (anime dual-audio phase 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A quality profile can name the audio languages a file must carry
(`audio.languages`, `audio.graft`). The release decision accepts a partial
release as a video candidate when grafting is on, and prefers a complete
one at a tie. Episodes and Movies report `status.audio`. The anime
built-ins want `[en, original]` with grafting on.

**Architecture:**
- `QualityProfileSpec.Audio` resolves into `quality.Profile`, and the
  profile hash covers it.
- `pkg/decision` gains one audio-language check, which replaces the
  single-language check when a profile sets `audio`. The check reads
  TRaSH's "Dual Audio" title pattern as `{original, English}` under an
  anime score set.
- `RankKey` gains `LanguagesComplete`.
- `decision.LacksLanguage` uses the anchor (the original language) when
  `audio` is set.
- The Episode and Movie reconcilers write `status.audio` from the probe.

**Spec:** `docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md` §5 (phase 1 as built at its end).

**Execution:** Native, approved by the owner on 2026-10-06 ("approve all recommended, for all phases"). Plan review is pre-approved.

## Global Constraints

- Phase 1's constraints hold: GPL headers, `PatchStatus`, complete SSA
  declarations, capped lists, pathspec commits, no `git stash`, `go get`
  or `go mod tidy`, and `make generate manifests` after API changes.
- `ui/plex/` and `app/catalog/metadata/plexepisodes.go` are clustarr-d2's.
  This phase touches neither.
- Graft states in `status.audio.graft` are phase 4's. This phase writes
  `none` when the profile grafts, and leaves the field out otherwise.

## Review Focus

1. **An untagged release under the anime profile.** `LanguagesFor` gives
   the original language only, so it is partial (missing English). With
   graft on it must pass as a video candidate; with graft off it must be
   rejected.
2. **`WEB-DL` in a title** must not read as a dual-audio token (`DL`).
   Use TRaSH's pattern, which has no bare `DL`.
3. **A profile with `audio` and an unknown original language** (metadata
   not in yet): the anchor is unknown, so the check fails open, as the
   single-language rule does.
4. **The built-in anime profiles change hash**, so Bootstrap re-creates
   them (delete, then create). Series that reference them must keep
   resolving: the name is unchanged.
5. **A file whose audio is unknown** gets `status.audio` with `wanted` and
   no `present` or `missing`. Never invent "missing English".

---

### Task 1: API: `audio` policy, `status.audio`, phase-1 minors

**Files:** `api/catalog/v1alpha1/qualityprofile_types.go`, `shared_types.go`
(or wherever `Image` lives), `episode_types.go`, `movie_types.go`,
`series_types.go` (move the misplaced comment), generated files.

- [ ] Add to `QualityProfileSpec`:

```go
	// Audio is the audio a file must carry. When set it replaces Language
	// in the release decision (anime dual-audio spec §5).
	// +optional
	Audio *AudioPolicy `json:"audio,omitempty"`
```

```go
// AudioPolicy is a profile's wanted audio languages.
type AudioPolicy struct {
	// Languages are BCP-47 tags, or "original" for the item's own
	// original language. A file is complete when it carries every one.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	Languages []string `json:"languages"`
	// Graft lets a release missing some of Languages be completed with
	// another release's audio (spec §6, §7).
	// +optional
	Graft bool `json:"graft,omitempty"`
	// Default is the language marked default in a grafted file; unset,
	// the first of Languages.
	// +optional
	Default string `json:"default,omitempty"`
}

// AudioState is what a file's audio carries against its profile's
// AudioPolicy (spec §5.3).
type AudioState struct {
	// Wanted are the profile's languages, resolved to tags.
	// +optional
	// +kubebuilder:validation:MaxItems=4
	Wanted []string `json:"wanted,omitempty"`
	// Present are the file's probed audio languages; empty when unknown.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Present []string `json:"present,omitempty"`
	// Missing are Wanted less Present; empty when Present is unknown.
	// +optional
	// +kubebuilder:validation:MaxItems=4
	Missing []string `json:"missing,omitempty"`
	// Graft is the graft's state: none, searching, grabbed, pending,
	// aligned, failed or done.
	// +optional
	// +kubebuilder:validation:Enum=none;searching;grabbed;pending;aligned;failed;done
	Graft string `json:"graft,omitempty"`
	// Reason says why, when Graft is failed.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason,omitempty"`
}
```

  Add `Audio *AudioState json:"audio,omitempty"` to `EpisodeStatus` and
  `MovieStatus`. Raise the Movie and Episode `Conditions` `MaxItems` from
  8 to 12 (phase-1 minor). In `series_types.go`, move the
  `// SeriesStatus describes the observed state of Series.` comment back
  above `type SeriesStatus` (phase-1 minor: the CRD lost its
  description).
- [ ] `make generate manifests`, then `go build ./...`, then
  `go test ./pkg/crdcheck/ ./api/...` with assets. Expected: PASS.
- [ ] Commit by pathspec.

### Task 2: `quality.Profile` resolves `audio`, seeds want dual audio

**Files:** `pkg/quality/profile.go`, `pkg/quality/builtins.go`,
`pkg/quality/catalogue/data/profiles/anime-web-1080p.json`,
`anime-remux-1080p.json`, and tests `pkg/quality/profile_test.go` and
`builtins_test.go`.

**Produces:** `Profile.AudioLanguages []string` (raw tags or "original",
nil when unset), `Profile.AudioGraft bool`, `Profile.AudioDefault string`.
`hashProfile` covers all three.

- [ ] Test first: `FromCRD` of a profile with
  `audio: {languages: [en, original], graft: true}` yields
  `AudioLanguages == [en original]` and `AudioGraft`. A profile without
  `audio` yields nil. Two profiles differing only in `audio.graft` differ
  in `Hash`. `quality.Builtins()` (or the seed loader the tests use) gives
  both anime seeds that audio. Run them and watch them fail.
- [ ] Implement:
  - copy the fields in `FromCRD` and validate each tag with
    `lang.Normalize` (or "original"), reporting an unresolvable one as an
    error the way `Language` is;
  - add `audio=<langs>,<graft>,<default>` to the `hashProfile` line;
  - add `Audio *catalogv1alpha1.AudioPolicy json:"audio"` to the seed
    struct and pass it into the spec;
  - add `"audio": {"languages": ["en", "original"], "graft": true}` to
    both anime seeds.
- [ ] Run `go test ./pkg/quality/...`. Expected: PASS, after updating any
  seed-hash golden the tests pin (say which in the commit).
- [ ] Commit by pathspec.

### Task 3: The audio-language release check and the tie-break

**Files:** create `pkg/decision/audiopolicy.go` and `audiopolicy_test.go`;
modify `pkg/decision/evaluate.go` (`languageRejection` call),
`pkg/decision/types.go` (`RankKey.LanguagesComplete`),
`pkg/decision/rank.go` (`less`) and `pkg/decision/audiolanguage.go`
(`LacksLanguage` anchor).

**Produces:**
- `audioRejection(originalLanguage string, p quality.Profile, parsed *release.ParsedRelease, title string) (rej *common.Rejection, complete bool)`.
- `dualAudio` is a regexp2 built from TRaSH's "Dual Audio" `ReleaseTitle`
  pattern, which this repo vendors in
  `pkg/quality/catalogue/data/formats/anime.json` slug `anime-dual-audio`.
  Copy the pattern string verbatim into a constant, with a comment naming
  its source.

- [ ] Tests first (table), with `originalLanguage = "Japanese"`. Mark each
  release complete or not:
  1. `[Group] Show - 01 [1080p] [Dual Audio]` under scoreSet `anime-sonarr`
     with audio `[en, original]`: accepted, complete.
  2. An untagged release, so languages are `[Japanese]` through
     `LanguagesFor`, with graft on: accepted, not complete.
  3. The same untagged release with graft off: rejected `WantedLanguage`.
  4. `Show.S01E01.1080p.WEB-DL.ENGLISH-GRP`, with languages `[English]`
     and graft on: rejected, because it lacks the anchor.
  5. `Show.S01E01.1080p.WEB-DL-GRP` under the anime score set: `DL` is not
     a dual-audio token, so it is partial.
  6. An unknown original (`""`) with graft on and languages `[English]`:
     accepted, failing open.

  Add a `Rank` test: two decisions equal in tier and score, one complete,
  and the complete one ranks first. Add a `LacksLanguage` case: a profile
  with `Language: "any"` and `AudioLanguages: [en original]` against a
  Korean-only file with original `ja` is lacking. The anchor overrides
  `any`.
- [ ] Run them. Expected: FAIL (undefined).
- [ ] Implement:
  - `audioRejection`: resolve wanted display names, mapping `original` to
    `originalLanguage` and a tag through `catalogue.LanguageName`, and skip
    any that resolve to nothing. `have` is `parsed.Languages`, plus
    `{originalLanguage, "English"}` when the profile's score set starts
    with `anime-` and the title matches `dualAudio`. `missing` is wanted
    less have, case-insensitive. Complete when missing is empty. Otherwise,
    with graft on, accept when `originalLanguage == ""` or have contains
    it; else reject `ReasonWantedLanguage` with "audio %v wanted, found %v".
  - In `evaluateOne`: when `len(p.AudioLanguages) > 0` call
    `audioRejection` instead of `languageRejection`, and set
    `RankKey.LanguagesComplete`; otherwise `LanguagesComplete = true`.
    Check where `RankKey` is built and fill it there.
  - `less`: after the format-score comparison,
    `if a.Rank.LanguagesComplete != b.Rank.LanguagesComplete { return a.Rank.LanguagesComplete }`.
  - `LacksLanguage`: `want := p.Language; if len(p.AudioLanguages) > 0 { want = "original" }`.
- [ ] Run `go test ./pkg/decision/ ./app/catalog/worker/...`. Expected: PASS.
- [ ] Commit by pathspec.

### Task 4: `status.audio` on Episode and Movie

**Files:** create `app/catalog/controller/rollup/audiostate.go` and its
test; modify the episode and movie reconcilers where Phase 1 computed
`wrongLanguage`, and their envtests.

**Produces:**
`rollup.AudioStateFor(p *quality.Profile, originalTag string, mf *catalogv1alpha1.MediaFile) *catalogv1alpha1.AudioState`,
nil when `p == nil`, `len(p.AudioLanguages) == 0` or `mf == nil`.

- [ ] Unit test first. Profile `[en, original]` with graft on, original
  `ja`:
  - probe `[jpn]` gives Wanted `[en ja]`, Present `[ja]`, Missing `[en]`,
    Graft `none`;
  - probe `[eng, jpn]` gives Missing empty;
  - probe `[und]` gives Present and Missing empty (Review Focus 5);
  - an unknown original gives Wanted `[en]` (the original dropped);
  - no audio policy gives nil.
- [ ] Implement. Resolve wanted tags with `lang.Normalize`, mapping
  `original` to `originalTag`. Present comes from
  `ProbedAudioLanguages(mf)`. Missing is computed only when Present is
  non-nil, comparing base subtags. Graft is `none` when `p.AudioGraft`,
  else empty.
- [ ] Envtest, test first: in each reconciler's real-controller test, add
  a subtest with an anime-style profile (`audio` set) and a Japanese-only
  file. `status.audio.missing == [en]` and `graft == none`, and the item
  is **not** CutoffUnmet for it: a missing graft never blocks the video.
  Then switch the probe to `[eng, jpn]` and `missing` empties.
- [ ] Wire both reconcilers:
  `if a := rollup.AudioStateFor(profile, originalTag, mf); a != nil { statusAC = statusAC.WithAudio(audioAC(a)) }`.
  Build the AC in one helper per package, and re-send it in any
  reassert path the reconciler has. Check `reassertKnownStatus` exists in
  movie, and declare `audio` there too.
- [ ] Run the packages with assets. Expected: PASS.
- [ ] Commit by pathspec.

### Task 5: Docs, review, gate, deploy

- [ ] CLAUDE.md: extend the phase-1 paragraph with one sentence on
  `audio`, the partial and complete rules, and `status.audio`. Add an
  "As built: phase 2" section to the spec.
- [ ] Final whole-branch review (fresh reviewer, opus), then the fix pass.
- [ ] Gate `make test` in a clean worktree, with no image build alongside.
- [ ] Push. Build controller and media, load them into kind, apply the
  CRDs server-side, and helm upgrade from `helm get values` with both
  tags changed.
- [ ] Verify:
  - the two anime built-ins carry `audio`, re-created by Bootstrap;
  - the 19 anime series' episodes read `status.audio`. Report the count
    with `missing: [en]`;
  - Monster S01E02 reads `missing [en]` and graft `none`;
  - no unexpected CutoffUnmet: count before and after.
