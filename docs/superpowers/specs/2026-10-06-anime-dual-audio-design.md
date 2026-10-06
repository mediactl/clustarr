# Anime series type, anime detection and dual-audio grafting

Date: 2026-10-06. Status: proposed, for the owner's review.

## 1. Goal

An anime series in the library should end up with the best video release
available, carrying both the English dub and the original-language audio,
even when no single release has both. Monster (2004) is the motivating case.
Its 74 episodes are mostly a 480p Netflix WEB release with Japanese audio
only. The English dub exists only on the 480p Viz DVDs, which BoB released
as dual-audio rips. The only 1080p file in the library (S01E01, Netflix via
DBTV) carries Korean audio alone.

Decisions the owner made on 2026-10-06:

- Grafting is configured **per quality profile**.
- Anime series are **detected automatically** and given the anime profile
  and series type `anime`.
- The anime profile wants **dual audio: English plus the original
  language**.
- Series type `anime` behaves **as in Sonarr**. It changes searching,
  file parsing and naming to use absolute numbers, and no longer forces
  absolute episode order. This amends the design of record (§3).

## 2. Evidence (spike, 2026-10-06)

Throwaway scripts; nothing committed. Both tests decoded audio to 8 kHz
mono with the method §7.1 describes.

| Test | Result |
|---|---|
| A. Netflix S01E02's Japanese track against a doctored copy of itself (sped up 25/23.976, delayed 3.217 s, 10 s cut at 12:00) | The PAL ratio won clearly (median peak 0.634; next 0.574 for 25/24; 1.0 scored 0.115). Offset −3.36 s on the stretched timeline (3.217 × 25/23.976). The cut appeared as a +10.42 s jump at 12:00. 45 of 46 windows agreed within 20 ms. |
| B. English dub against Japanese in one BoB DVD file (S01E10), true offset 0 | 18 of 46 windows matched confidently, all at offset 0, none wrong. The correct rate won narrowly (0.146 against 0.131). |

Conclusion: aligning the original-language track of two releases is
reliable, including speed changes and cuts. Aligning a dub against the
original works on music and effects, but too sparsely to trust. **A donor
must carry the original-language track** (§6.1).

**Not yet tested:** two real, different releases of one episode (DVD
against Netflix WEB). Separate masters can differ in openings, eyecatches
and length. This is gate G1 (§10). The library holds no such pair, and the
cluster's downloads are paused, so the test needs the owner's OK for one
download.

## 3. Series type `anime` (amends design §4.2)

§4.2 says `episodeOrder` is "absolute forced when anime". With TVDB,
absolute order puts every episode in season 1. Setting `anime` on the
library's 19 anime series would collapse seven multi-season shows into one
season: One Piece (23 seasons, 665 files), JoJo's Bizarre Adventure (6),
Overlord (4), Mushoku Tensei (3), The Dangers in My Heart (3), Reincarnated
as a Sword (2) and My Happy Marriage (2). Their Episodes would be re-created,
about 900 files moved, and their seasons would lose their `plex://` GUIDs.

New rule: **`anime` never changes the episode order.**
`series.EffectiveEpisodeOrder` returns `spec.episodeOrder` (official when
unset) for every series type. The `ui/plex` copy of the same function
(`extended.go` `effectiveOrder`) changes the same way; that directory is
clustarr-d2's, so the change is coordinated with them. A series that wants
absolute order sets `episodeOrder: absolute` explicitly, as Solo Leveling
does today.

`anime` keeps every other effect it has today, each of which already reads
`status.absoluteNumber`. In official order every anime episode in the
library carries one (Overlord 52 of 52, One Piece 1,179 of 1,179):

- Search sends absolute numbers (`snapshot.go`, `IDs.Anime`).
- File import matches a release's absolute number first
  (`fileimport.MatchEpisodes`).
- Naming renders `{absolute}` (`catalogctx`, `Anime`).
- Segment detection plans anime seasons together (`segmenting/plan.go`).

The TV root folder renames only files squasharr transcoded
(`renameTranscoded: true`, `renameFiles: false`). Switching a series to
`anime` renames nothing until its next transcode, which then names the file
with the anime preset.

## 4. Anime detection and profile assignment

**Signal.** A Series is anime when its TVDB genres
(`status.metadata.genres`) contain `Anime`. 19 of the library's 150 series
match. All are `standard` on `web-1080p` today.

**Where it is configured.** `RootDefaults` (RootFolder `spec.defaults`)
gains:

```go
// Anime are the defaults a series added under this folder takes once its
// metadata shows it is anime (TVDB genre "Anime"). Unset: no detection.
// +optional
Anime *AnimeDefaults `json:"anime,omitempty"`

type AnimeDefaults struct {
	// QualityProfileRef is the profile an anime series is moved to.
	QualityProfileRef string `json:"qualityProfileRef"`
	// SeriesType defaults to anime.
	// +kubebuilder:default=anime
	SeriesType SeriesType `json:"seriesType,omitempty"`
}
```

The rollout sets the `tv` RootFolder's `defaults.anime` to
`{qualityProfileRef: anime-web-1080p}`.

**Who applies it, and once only.** The Series reconciler (catalogarr) checks
each Series when its metadata is ready:

- It acts only when the Series has no `status.classification` and its
  RootFolder has `defaults.anime` set.
- An anime Series has `spec.seriesType` and `spec.qualityProfileRef` applied
  under a field manager of their own, `k8s.ManagerCatalogarrClassify`.
- The reconciler then records
  `status.classification: {anime: true|false, appliedAt, qualityProfileRef}`.
  This is part of the reconciler's own complete status declaration, so it is
  re-sent on every apply.

A Series that already has a classification is never re-classified, so a
profile or type the owner changes afterwards stays. The annotation
`catalog.clustarr.io/classify: "off"` skips a Series entirely. The library's
19 existing anime series are classified the first time the new reconciler
sees them; that is the one-time migration. clustarr-d2's three series still
being renumbered (My Happy Marriage, Solo Leveling, The Dangers in My Heart)
carry `classify: off` until they finish.

This follows the precedent of the Series controller writing Episode
`spec.monitored` once per season toggle (`SeasonCascade`, recorded in
`status.seasons[].appliedMonitored`).

## 5. Profile audio languages

### 5.1 API

`QualityProfileSpec` gains:

```go
// Audio is the audio a file must carry. Unset: the single-language rule in
// Language applies as today.
// +optional
Audio *AudioPolicy `json:"audio,omitempty"`

type AudioPolicy struct {
	// Languages are BCP-47 tags, or "original" for the item's own original
	// language. A file is complete when it carries every one.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	Languages []string `json:"languages"`
	// Graft lets a release missing some of Languages be completed with
	// another release's audio (§6, §7).
	// +optional
	Graft bool `json:"graft,omitempty"`
	// Default is the language marked default in a grafted file. Unset: the
	// first entry of Languages.
	// +optional
	Default string `json:"default,omitempty"`
}
```

When `audio` is set, it replaces `language` in the release decision.
`anime-web-1080p` and `anime-remux-1080p` seed
`audio: {languages: [en, original], graft: true}`. That changes their
seed hash, so `qualityprofile.Bootstrap` re-creates them once.

### 5.2 Release decision

- A release's languages are the parser's. One new rule: under the anime
  score set, the `Dual Audio` / `DUAL` / `DL` token reads as `{original,
  English}`. The parser already treats an untagged release as the item's
  original language (`LanguagesFor`).
- **Complete:** the release carries every wanted language. It passes as
  today.
- **Partial, graft on:** the release passes as a video candidate only if
  it carries the **anchor**, the original language. The donor is aligned
  against that track (§7). It then ranks exactly as it would on quality and
  score.
- **Ties:** between two releases of the same tier and score, a complete one
  wins.
- **Partial, graft off:** rejected with `WantedLanguage`, as today.

### 5.3 What the file actually carries

`Episode.status.audio` (and the same on Movie), written by the item's own
reconciler from the current MediaFile's probe (`pkg/lang.Normalize`):

```go
type AudioState struct {
	Wanted  []string `json:"wanted,omitempty"`  // resolved, e.g. [en, ja]
	Present []string `json:"present,omitempty"` // from the probe
	Missing []string `json:"missing,omitempty"`
	// Graft: none | searching | grabbed | pending | aligned | failed | done
	Graft  string `json:"graft,omitempty"`
	Reason string `json:"reason,omitempty"`
}
```

Separately, and shippable on its own: a file whose probe lacks the
**anchor** language, like Monster S01E01's Korean-only audio, gets condition
`WrongLanguage`. The item keeps searching for a replacement under its usual
upgrade rules, and the file is never deleted.

## 6. Audio donors

### 6.1 Search and grab

When an item's file has `missing` languages, graft is on and no donor is
held, the item's reconciler queues a donor search
(`SearchTask.Purpose: audioDonor`). The decision for that search:

- **Required:** the release carries every missing language plus the anchor.
  Dual-audio releases qualify.
- **Ignored:** the quality ladder and cutoff. A 480p DVD is a good donor.
  The size table still applies, against fakes.
- **Ranking:** lineage close to the video, then smaller size. Lineage means
  the same source class (a DVD-sourced dub for a DVD-era show) and an
  edition that is not "uncut" or "extended" when the video is neither.

A grabbed donor is a Download with `spec.purpose: audioDonor` and the same
target keys as any grab.

### 6.2 Import and retention

importarr imports a donor without making it a library file:

- It remuxes only the donor's missing-language tracks and its anchor track
  into `<RootFolder>/.clustarr/donors/<item-uid>/<target-key>.mka`. A dot
  folder is skipped by Plex's scanner. No video is kept.
- It creates an `AudioGraft` (group `transcode.clustarr.io`; spec owned by
  importarr, status by squasharr):

```go
type AudioGraftSpec struct {
	ItemRef   commonv1.ItemRef `json:"itemRef"`   // the Episode or Movie
	DonorPath string           `json:"donorPath"` // the .mka above
	Languages []string         `json:"languages"` // the tracks to graft
	Anchor    string           `json:"anchor"`    // e.g. ja
	Release   string           `json:"release"`   // donor release title
}
```

The donor file is kept as long as the item exists, and is removed with it.
When a later video upgrade arrives without the wanted languages, the graft
runs again from the kept donor, with no new download.

## 7. Alignment and the graft job

### 7.1 Alignment (`pkg/audioalign`, pure Go)

A port of the spike's method, with the spike's numbers as starting
thresholds:

1. Decode both anchor tracks to 8 kHz mono.
2. Features: 512-point STFT, 20 ms hop, 24 log-spaced bands from 150 to
   3,800 Hz. Take the positive log-energy difference per band (onsets),
   z-normalised.
3. **Rate:** stretch the donor's features by each candidate in {1,
   25/23.976, 23.976/25, 25/24, 24/25, 1.001, 1/1.001} and keep the one
   with the highest median window peak. Then refine it by a line fit of the
   window lags within the longest constant run. 25/24 and 25/23.976 are
   only 0.1% apart, and the refinement tells them apart.
4. **Offsets:** 20 s windows every 30 s, FFT cross-correlation summed over
   bands. A window is confident when its peak is ≥ 1.5× the best peak
   outside ±1 s.
5. **Segments:** runs of confident windows with offsets within 40 ms. A
   jump between runs is a cut. Where the target has extra material, the
   graft inserts silence. Where the donor has extra, its audio is dropped.

**Accept** only if:

- confident windows cover ≥ 80% of the target;
- there are at most 4 segments, each at least 3 windows long;
- the rate margin over the runner-up candidate is clear (thresholds set at
  G1).

Anything else ends the graft with a reason; nothing is muxed. The FFT is
written in the package (radix-2); no new module dependency.

### 7.2 The graft job (squasharr)

squasharr reconciles `AudioGraft`. When the item's current MediaFile is
probed and still missing languages, it gives the job to a CPU pool worker
(`cmd/squasharr-worker`, which already decodes and muxes in-process through
ffgo). The worker:

1. Aligns the anchors (§7.1) and writes the result to the job's status
   (rate, segments, confidence).
2. Muxes: every stream of the target is copied; the grafted track or tracks
   are added, resampled by the rate, padded or trimmed per segment, encoded
   AAC stereo, language-tagged and titled `English (dub, grafted)`. The
   profile's default language gets the default flag. The container is
   tagged `CLUSTARR_GRAFT=<donor sha256 prefix>`.
3. **Verifies:** applies the same transform to the donor's anchor and
   aligns it against the target's anchor again. Accept if the median
   absolute residual is ≤ 40 ms and at least 90% of windows are within
   80 ms.
4. Swaps the file into place through the transcode swap path
   (`fsops.RecycleLink`, then rename).

A graft is **not a transcode**:

- catalogarr's swap incorporation must not set `spec.original: false` for
  it. The file stays upgradable; `rollup.Transcoded` must not read it as
  final.
- A transcode copies audio already in Apple TV direct-play codecs (AAC,
  AC-3, E-AC-3) and keeps the `CLUSTARR_GRAFT` tag.
- squasharr never runs a graft and a transcode on one MediaFile at once.

## 8. Ownership summary

| Field | Writer (field manager) |
|---|---|
| Series `spec.seriesType`, `spec.qualityProfileRef`, once | catalogarr Series reconciler (`catalogarr-classify`) |
| Series `status.classification` | catalogarr Series reconciler (its existing status manager) |
| Episode or Movie `status.audio`, condition `WrongLanguage` | the item's reconciler (catalogarr) |
| Download `spec.purpose` | catalogarr grab worker, at creation; immutable |
| AudioGraft spec | importarr |
| AudioGraft status | squasharr (`ManagerSquasharr`) |
| MediaFile after a graft swap | as after a transcode, except `spec.original` |

All status lists are capped (`segments` ≤ 16, `languages` ≤ 4).

## 9. Failure handling

- Any alignment, mux or verify failure: the target file is untouched,
  AudioGraft reads `Failed` with the reason, and Episode `status.audio.graft`
  reads `failed`. The donor is blocklisted for this item only, and the next
  donor search runs on the normal backoff.
- A donor whose anchor track is missing or unaligned is rejected the same
  way.
- **No English donor exists at all** (common for niche anime): the item
  stays `missing: [en]`. This is visible on its page and never blocks the
  video.

## 10. Phases and gates

1. **Anime type and detection** (§3, §4) plus `WrongLanguage` (§5.3).
   Independent and useful on its own. Coordinate the `ui/plex`
   `effectiveOrder` change and the three `classify: off` series with
   clustarr-d2.
2. **Profile audio** (§5): API, release decision, `status.audio`, anime
   seeds.
3. **G1, real-source alignment.** Run `pkg/audioalign` (written TDD from
   the spike's recordings) on a real DVD/WEB pair of one Monster episode.
   The cheapest pair is BoB's dual-audio DVD S01E02 against the library's
   Netflix S01E02, one release of about 200 MB, **needing the owner's OK to
   download**. Set the acceptance thresholds from what it shows. If G1
   fails, phase 4 is redesigned before it is built.
4. **Donors and grafting** (§6, §7). The first live run is on Monster.

Each phase gets its own plan, gate and deploy.

## 11. Out of scope

- English-only donors (test B: too sparse to trust). Revisit with real data
  after G1.
- Grafting subtitles. captionarr already fetches them.
- Music, books and other non-video kinds.
- Changing which release Plex plays. Plex picks the audio track from each
  user's language settings, and the default flag only matters without one.

## As built: phase 1 (2026-10-06)

Plan `docs/superpowers/plans/2026-10-06-anime-type-detection-wronglanguage.md`.

- **§3:** `series.EffectiveEpisodeOrder` and the `ui/plex` mirror return
  `spec.episodeOrder` for every series type. Design §4.2 is amended.
- **§4:** `series.Classify` and `status.classification`, with the spec
  merge-patched under `catalogarr-classify`.
  - Built beyond the spec: import lists send a classified series' current
    profile and type, since their re-apply forces ownership on every sync.
  - Built beyond the spec: a RootFolder's spec edit enqueues its
    unclassified series, so turning detection on after deploy is
    immediate.
- **§5.3:** `WrongLanguage` reads the profile's `language`, which is the
  anchor for every built-in profile, through `decision.LacksLanguage` over
  `decision.AudioLanguages`. A file with any untagged track is unknown,
  never wrong. The final review found that the import gate still demanded
  an upgrade, which would have looped grab, reject and blocklist. A
  wrong-language file is now replaced without an upgrade only when both
  hold:
  - search: the release title names its languages;
  - import: the new file's probe carries the wanted language.

  The Movie and Episode watches also wake on a probe's audio languages.

## As built: phase 2 (2026-10-06)

Plan `docs/superpowers/plans/2026-10-06-anime-profile-audio.md`.
`QualityProfileSpec.Audio` resolves into `quality.Profile` (`AudioLanguages`,
`AudioGraft`, `AudioDefault`, hashed; `ScoreSet` added, unhashed). The
dual-audio token is TRaSH's own "Dual Audio" pattern, which has no bare
`DL`, so `WEB-DL` never reads as dual audio. A dual-audio title also counts
as naming its languages for phase 1's wrong-language replacement.
`status.audio` is written by the Episode and Movie reconcilers
(`rollup.AudioStateFor`, one renderer for every path). Phase 1's minors
fixed here: conditions capped at 12, SeriesStatus' description restored.

## G1 result (2026-10-06)

`pkg/audioalign` (phase 3), `TestRealPair`. The pair is BoB's dual-audio
DVD rip of Monster S01E02 (NTSC 29.97 fps, 1,435.58 s) against the
library's Netflix WEB S01E02 (23.976 fps, 1,435.68 s), both decoded to
8 kHz mono:

| Donor track against Netflix Japanese | Rate (margin) | Confident windows | Coverage | Segments | Verify |
|---|---|---|---|---|---|
| DVD Japanese (anchor against anchor) | 1 (7.31) | 46 of 48 | 0.96 | 1, offset +1.00 s | median 0 ms, 100% within 80 ms |
| DVD English (dub against original, information only) | 1 (2.03) | 33 of 48 | 0.69 | 1, offset +1.00 s | median 0 ms, 100% within 80 ms |

G1 passes: phase 4 is built as designed. The default thresholds stand
(coverage 0.6, at most 4 segments, each at least 3 windows long, a rate
margin of 1.02 over another family of ratios). Real sources aligned
better than spike test B suggested: even the dub reached 69% coverage on
music and effects. English-only donors stay out of scope for phase 4, but
they look feasible. Alignment takes about 14 s for 24 minutes on 12
cores.
