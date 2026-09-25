# Probe-Driven Naming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** name library files from what a probe proves (codec, dynamic range, resolution), identify a file by its folder when its name fails, and rename existing files to their canonical names through a pass split between catalogarr's proposal and importarr's move.

**Architecture:** pure additions to `pkg/naming`, `pkg/quality`, `pkg/release` and `pkg/mediainfo` first (N1); then import and rescan run the probe and correct quality (N2); catalogarr's MediaFile reconciler renders `status.naming` from the owner item, the RootFolder and the probe (N2); an importarr controller watching MediaFiles performs the move and applies `spec.path` and `spec.quality` (N3); a LibraryScan `rename` mode gives dry runs and on-demand passes; the UI creates them (N3); e2e is written, not run (N4).

**Tech Stack:** Go, controller-runtime, kubebuilder markers + `make generate manifests`, envtest, ffprobe fixtures under `test/data/mediainfo`, testify.

**Spec:** `docs/superpowers/specs/2026-09-24-probe-driven-naming-design.md`

## Global Constraints

- Every status write goes through `pkg/k8s.PatchStatus` as a complete declaration of the manager's owned set; `status.naming` is sent on every apply of `k8s.ManagerCatalogarr`, early returns included.
- importarr owns `MediaFileSpec`; the rename applies `spec.path`, `spec.sizeBytes`, `spec.modTime` and `spec.quality` under `k8s.ManagerImportarr` through one function that renders the manager's complete set (`rescan.applyObserved` with `reassertFrozen`).
- No `float32`/`float64` in `api/`; every new status list carries `+kubebuilder:validation:MaxItems`; every new bool default is a pointer with an `OrDefault` accessor.
- The UI never writes status; new UI writes are `create` of LibraryScan only (`ui/actions.Grants()`).
- Never `go get`/`go mod tidy`; no new dependencies are needed.
- Commits are pathspec-scoped (`git commit -m '...' -- <paths>`); never `git add -A`, `git stash`, `reset --hard`; never push.
- Tests run with `KUBEBUILDER_ASSETS` exported (`export KUBEBUILDER_ASSETS=$($(go env GOPATH)/bin/setup-envtest use 1.37.0 -p path)`); `ffprobe` is at `/usr/bin/ffprobe` on the development box and tests that need it skip without it.
- Source (Bluray, WEB, …) and revision are never changed by a probe; resolution, codec, bit depth, dynamic range and audio are.
- **Ruling (deviation from spec §5):** the rename trigger is a controller-runtime controller in importarr watching MediaFiles, not a NATS consumer. CLAUDE.md: "Kubernetes watches are the default coupling; NATS is the exception". The LibraryScan path is unchanged.

## Review Focus

1. A file whose name says `2160p` over a 1920×1080 probe must render `1080p` (Task 4 test, falsified by reverting to name-only).
2. A basename that does not parse inside `A Scanner Darkly (2006) {tmdb-3509}/` must attribute to that movie and never to a speculative one (Task 5 and Task 8 tests on the real parser).
3. A rename must never overwrite: a file already at `expectedPath` is `Collision`, and a fingerprint that changed between the read and the move is `Changed` (Task 10 tests with a real second writer).
4. `status.naming` must survive the reconciler's early returns (`FileMissing`, `ProbeFailed`): the release test acts on a MediaFile that already has it and asserts `managedFields` (Task 9).
5. A file never probed must render exactly the old name: every optional MediaInfo block renders to nothing (Task 3 emptytoken test).

---

### Task 1: API — `status.naming`, `renameFiles`, LibraryScan `rename`

**Files:**
- Modify: `api/catalog/v1alpha1/mediafile_types.go`
- Modify: `api/catalog/v1alpha1/rootfolder_types.go`
- Modify: `api/catalog/v1alpha1/defaults.go`
- Modify: `api/catalog/v1alpha1/libraryscan_types.go`
- Generated: `api/applyconfiguration/...`, `config/crd/...`, `charts/clustarr/crds/...` via `make generate manifests`
- Test: `pkg/crdcheck/naming_status_cel_test.go`

**Interfaces:**
- Produces: `catalogv1alpha1.NamingStatus{ExpectedPath string; Current bool; Reason NamingReason; Quality *commonv1.Quality}`, `MediaFileStatus.Naming *NamingStatus`, condition type `ConditionNamingCurrent = "NamingCurrent"`, reasons `NamingReasonMetadataPending|ProbePending|TranscodePending|Recycling|Unrenderable`; `NamingSpec.RenameFiles *bool` + `func (n NamingSpec) RenameFilesOrDefault() bool`; `LibraryScanSpec.Rename ScanRename` (`""|off|dryRun|apply`), `LibraryScanStatus.Renamed []RenamedFile{From, To, Reason string}` (MaxItems=200), `LibraryScanStatus.FilesRenamed int64`.

- [ ] **Step 1: Add the types**

```go
// mediafile_types.go, in MediaFileStatus after Transcode:
	// Naming is the file's canonical path under its RootFolder's naming
	// preset, rendered by catalogarr from the item's metadata, the
	// release-time spec and the probe; importarr performs the rename.
	// +optional
	Naming *NamingStatus `json:"naming,omitempty"`

// NamingStatus is catalogarr's proposal for a MediaFile's canonical path.
type NamingStatus struct {
	// ExpectedPath is the absolute path the preset renders; empty until
	// the item's metadata and the probe are both present.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	ExpectedPath string `json:"expectedPath,omitempty"`
	// Current is true when spec.path equals expectedPath.
	Current bool `json:"current"`
	// Reason says why expectedPath is empty or the file is not renameable.
	// +optional
	// +kubebuilder:validation:Enum=MetadataPending;ProbePending;TranscodePending;Recycling;Unrenderable
	Reason NamingReason `json:"reason,omitempty"`
	// Quality is the probe-corrected quality importarr re-applies into
	// spec.quality when it renames.
	// +optional
	Quality *commonv1.Quality `json:"quality,omitempty"`
}

type NamingReason string

const (
	NamingReasonMetadataPending  NamingReason = "MetadataPending"
	NamingReasonProbePending     NamingReason = "ProbePending"
	NamingReasonTranscodePending NamingReason = "TranscodePending"
	NamingReasonRecycling        NamingReason = "Recycling"
	NamingReasonUnrenderable     NamingReason = "Unrenderable"
	// ConditionNamingCurrent mirrors status.naming.current.
	ConditionNamingCurrent = "NamingCurrent"
)
```

```go
// rootfolder_types.go, NamingSpec:
	// RenameFiles lets importarr rename a library file to its canonical
	// name whenever catalogarr reports it is not (status.naming). Off by
	// default: a library imported before codec tokens existed differs on
	// every file. A LibraryScan with spec.rename runs a pass regardless.
	// +optional
	// +kubebuilder:default=false
	RenameFiles *bool `json:"renameFiles,omitempty"`

// defaults.go:
// RenameFilesOrDefault applies the CRD default (false) to a nil pointer,
// since a typed client cannot send a value the CRD default overrides.
func (n NamingSpec) RenameFilesOrDefault() bool {
	return n.RenameFiles != nil && *n.RenameFiles
}
```

```go
// libraryscan_types.go:
// ScanRename selects the rename pass of a LibraryScan.
// +kubebuilder:validation:Enum=off;dryRun;apply
type ScanRename string

const (
	ScanRenameOff    ScanRename = "off"
	ScanRenameDryRun ScanRename = "dryRun"
	ScanRenameApply  ScanRename = "apply"
)

// in LibraryScanSpec:
	// Rename runs a rename pass over the scanned subtree: dryRun records
	// what would move in status.renamed, apply moves it. Off by default.
	// +optional
	// +kubebuilder:default=off
	Rename ScanRename `json:"rename,omitempty"`

// RenamedFile is one entry of a rename pass.
type RenamedFile struct {
	// +kubebuilder:validation:MaxLength=4096
	From string `json:"from"`
	// +kubebuilder:validation:MaxLength=4096
	To string `json:"to"`
	// Reason is empty for a move, else why it was refused (Collision,
	// Changed, DryRun, NotCurrent, Held).
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Reason string `json:"reason,omitempty"`
}

// in LibraryScanStatus:
	FilesRenamed int64 `json:"filesRenamed,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Renamed []RenamedFile `json:"renamed,omitempty"`
```

- [ ] **Step 2: Regenerate**

Run: `make generate manifests`
Expected: apply configurations gain `WithNaming`, `WithRenameFiles`, `WithRename`, `WithRenamed`, `WithFilesRenamed`; CRDs updated under `config/crd` and the chart's copy.

- [ ] **Step 3: Write the CRD guard test**

```go
// pkg/crdcheck/naming_status_cel_test.go
func TestNamingStatusRoundTripsAndScanRenameIsAnEnum(t *testing.T) {
	c := envClient(t) // the package's existing dynamic-client helper
	mf := unstructuredMediaFile("media", "inception-abc1234567")
	setStatus(t, c, mf, map[string]any{"naming": map[string]any{
		"expectedPath": "/data/media/movies/Inception (2010)/Inception (2010) - [Bluray-1080p][x265].mkv",
		"current": false, "reason": "ProbePending",
	}})
	got := getStatus(t, c, mf)
	require.Equal(t, "ProbePending", got["naming"].(map[string]any)["reason"])

	scan := unstructuredLibraryScan("media", "movies-abc")
	unstructured.SetNestedField(scan.Object, "sideways", "spec", "rename")
	_, err := c.Create(ctx, scan, metav1.CreateOptions{})
	require.Error(t, err, "rename accepts only off, dryRun and apply")
}
```

- [ ] **Step 4: Run the guards**

Run: `export KUBEBUILDER_ASSETS=$($(go env GOPATH)/bin/setup-envtest use 1.37.0 -p path) && go test ./pkg/crdcheck/ -run 'Naming|EveryStatusListIsCapped|NoCRDDefaultIsUnreachableFromGo' -count=1`
Expected: PASS (the capped-list guard sees `renamed`; the default guard sees `renameFiles` as a pointer).

- [ ] **Step 5: Commit**

```bash
git commit -m 'feat(api): MediaFile status.naming, RootFolder naming.renameFiles, LibraryScan rename mode and status.renamed' -- api/ config/crd/ charts/clustarr/crds/ pkg/crdcheck/naming_status_cel_test.go
```

---

### Task 2: `pkg/mediainfo.FormatVideoCodec`

**Files:**
- Create: `pkg/mediainfo/codec.go`
- Test: `pkg/mediainfo/codec_test.go`

**Interfaces:**
- Produces: `func FormatVideoCodec(codecName, videoProfile, releaseTitle string) string`

- [ ] **Step 1: Write the failing test**

```go
func TestFormatVideoCodecFollowsRadarr(t *testing.T) {
	for _, tc := range []struct{ codec, profile, title, want string }{
		{"h264", "High", "Movie.2010.1080p.BluRay.x264-GRP", "x264"},
		{"h264", "High", "Movie.2010.1080p.WEB-DL.H.264-GRP", "h264"},
		{"h264", "High", "", "h264"},
		{"hevc", "Main 10", "Movie.2010.2160p.x265-GRP", "x265"},
		{"hevc", "Main 10", "2ef6f194995e4a11b055d0f2354ef0ba", "h265"},
		{"av1", "", "", "AV1"},
		{"vp9", "", "", "VP9"},
		{"mpeg4", "Advanced Simple Profile", "Movie.XviD-GRP", "XviD"},
		{"mpeg4", "Advanced Simple Profile", "Movie.DivX-GRP", "DivX"},
		{"vc1", "", "", "VC1"},
		{"mpeg2video", "", "", "MPEG2"},
		{"prores", "", "", "PRORES"},
		{"", "", "", ""},
	} {
		require.Equal(t, tc.want, FormatVideoCodec(tc.codec, tc.profile, tc.title), "%s/%s/%s", tc.codec, tc.profile, tc.title)
	}
}
```

- [ ] **Step 2: Run it** — `go test ./pkg/mediainfo/ -run TestFormatVideoCodecFollowsRadarr` — FAIL: undefined `FormatVideoCodec`.

- [ ] **Step 3: Implement**

```go
// FormatVideoCodec is Radarr's MediaInfoFormatter.FormatVideoCodec: the
// probe names the codec, and the release title says whether an AVC or HEVC
// stream was an x264/x265 encode, which the file itself cannot.
func FormatVideoCodec(codecName, videoProfile, releaseTitle string) string {
	title := strings.ToLower(releaseTitle)
	switch strings.ToLower(codecName) {
	case "h264", "avc":
		if strings.Contains(title, "x264") { return "x264" }
		return "h264"
	case "hevc", "h265":
		if strings.Contains(title, "x265") { return "x265" }
		return "h265"
	case "av1": return "AV1"
	case "vp9": return "VP9"
	case "vc1": return "VC1"
	case "mpeg2video": return "MPEG2"
	case "mpeg4", "msmpeg4v3":
		if strings.Contains(title, "divx") { return "DivX" }
		return "XviD"
	case "": return ""
	}
	return strings.ToUpper(codecName)
}
```

- [ ] **Step 4: Run** — PASS. Then `gofumpt -w`, `golangci-lint-v2 run ./pkg/mediainfo/`.

- [ ] **Step 5: Commit** — `git commit -m 'feat(mediainfo): FormatVideoCodec, Radarr codec labels from the probe and the release title' -- pkg/mediainfo/codec.go pkg/mediainfo/codec_test.go`

---

### Task 3: naming tokens and presets

**Files:**
- Modify: `pkg/naming/render.go` (token table + helpers)
- Modify: `pkg/naming/preset.go` (`movieFileTemplate`), `pkg/naming/series.go` (three episode templates)
- Modify: `pkg/naming/context.go` (add `ReleaseTitle string` beside `OriginalFilename`)
- Test: `pkg/naming/mediainfo_tokens_test.go`, extend `pkg/naming/emptytoken_test.go`

**Interfaces:**
- Consumes: `mediainfo.FormatVideoCodec` (Task 2) — **no**: `pkg/naming` must not import `pkg/mediainfo` (mediainfo imports commonv1 only, so it may; check `go list -deps` shows no cycle; if there is one, copy the switch into naming as `videoCodecLabel` and hold both to one table with a test in `pkg/mediainfo` that calls `naming.VideoCodecLabel`).
- Produces: tokens `mediainfo videocodec`, `mediainfo videobitdepth`, `mediainfo audiolanguages`, `mediainfo subtitlelanguages`, `mediainfo simple`, `mediainfo full`; `Context.ReleaseTitle`.

- [ ] **Step 1: Failing tests**

```go
func TestMediaInfoTokensRenderFromTheProbe(t *testing.T) {
	mi := commonv1.MediaInfo{VideoCodec: "hevc", VideoBitDepth: 10, Hdr: commonv1.HdrHDR10,
		Audio: []commonv1.AudioStream{{Codec: "aac", Channels: 2, Language: "ja"}, {Codec: "eac3", Channels: 6, Language: "en", Default: true}},
		Subtitles: []commonv1.SubtitleStream{{Language: "en"}, {Language: "es"}}}
	c := Context{Kind: commonv1.MediaKindMovie, Title: "Akira", Year: 1988, MediaInfo: mi, ReleaseTitle: "Akira.1988.2160p.x265-GRP"}
	e := NewEngine(Config{})
	for tmpl, want := range map[string]string{
		"{MediaInfo VideoCodec}":        "x265",
		"{MediaInfo VideoBitDepth}":     "10",
		"{MediaInfo AudioCodec}":        "EAC3",   // the default stream, not the first
		"{MediaInfo AudioChannels}":     "5.1",
		"{MediaInfo AudioLanguages}":    "[JA+EN]",
		"{MediaInfo SubtitleLanguages}": "[EN+ES]",
		"{MediaInfo Simple}":            "x265 EAC3",
		"{MediaInfo Full}":              "x265 EAC3 [JA+EN] [EN+ES]",
	} {
		got, err := e.Render(tmpl, c)
		require.NoError(t, err, tmpl)
		require.Equal(t, want, got, tmpl)
	}
	one := c
	one.MediaInfo.Audio = one.MediaInfo.Audio[1:]
	got, _ := e.Render("{MediaInfo AudioLanguages}", one)
	require.Equal(t, "", got, "a single language is not stated, Radarr's rule")
}

func TestVideoPresetsStateTheCodec(t *testing.T) {
	c := Context{Kind: commonv1.MediaKindMovie, Title: "Akira", Year: 1988,
		Quality: commonv1.Quality{Name: "Bluray-1080p"}, MediaInfo: commonv1.MediaInfo{VideoCodec: "hevc", Hdr: commonv1.HdrHDR10}}
	got, err := NewEngine(Config{}).MovieFile(c)
	require.NoError(t, err)
	require.Equal(t, "Akira (1988) - [Bluray-1080p] [HDR10] [h265]", got)
	c.MediaInfo = commonv1.MediaInfo{}
	got, _ = NewEngine(Config{}).MovieFile(c)
	require.Equal(t, "Akira (1988) - [Bluray-1080p]", got, "a file never probed renders as before")
}
```

Check the existing `{MediaInfo AudioCodec}` casing convention in `render_test.go` and match it (the table above assumes upper-case labels; if the existing token renders `eac3`, keep that and adjust the expectation, never change the existing token's output).

- [ ] **Step 2: Run** — FAIL with `ErrUnknownToken` for the new tokens and the old preset string.

- [ ] **Step 3: Implement** — in `render.go` add to `tokenFuncs`:

```go
	"mediainfo videocodec":        {fn: func(c Context, _, _ int) string { return videoCodecLabel(c.MediaInfo.VideoCodec, c.MediaInfo.VideoProfile, c.ReleaseTitle) }},
	"mediainfo videobitdepth":     {fn: func(c Context, _, _ int) string { return nonZero(c.MediaInfo.VideoBitDepth) }},
	"mediainfo audiolanguages":    {fn: func(c Context, _, _ int) string { return languageList(audioLanguages(c.MediaInfo)) }},
	"mediainfo subtitlelanguages": {fn: func(c Context, _, _ int) string { return languageList(subtitleLanguages(c.MediaInfo)) }},
	"mediainfo simple":            {fn: func(c Context, _, _ int) string { return joinNonEmpty(" ", videoCodecLabel(...), defaultAudioCodec(c.MediaInfo)) }},
	"mediainfo full":              {fn: func(c Context, _, _ int) string { return joinNonEmpty(" ", videoCodecLabel(...), defaultAudioCodec(c.MediaInfo), languageList(audioLanguages(c.MediaInfo)), languageList(subtitleLanguages(c.MediaInfo))) }},
```

with `defaultAudioStream(mi)` returning the stream with `Default`, else the first; change `firstAudioCodec`/`firstAudioChannels` to use it. `languageList` upper-cases BCP-47 tags, keeps first occurrence order, returns `""` for fewer than two distinct known languages, else `"[A+B]"`. Presets:

```go
const movieFileTemplate = "{Movie CleanTitle}{ (Release Year)}{ - [Quality Full]}{ [MediaInfo VideoDynamicRangeType]}{ [MediaInfo VideoCodec]}{-Release Group}"
episodeFileStandardTemplate = "{Series TitleWithoutYear}{ (Series Year)} - S{season:00}E{episode:00}{ - Episode CleanTitle:90}{ [Quality Full]}{ [MediaInfo VideoDynamicRangeType]}{ [MediaInfo VideoCodec]}{-Release Group}"
// anime and daily: the same two blocks after { [Quality Full]}
```

- [ ] **Step 4: Run** — `go test ./pkg/naming/ -count=1` — PASS, including `emptytoken_test.go` (every preset's optional blocks empty → dialect-stable names). Update any golden in `pkg/naming` that hard-codes the old movie name only where the context now carries MediaInfo; a golden without MediaInfo must be unchanged.

- [ ] **Step 5: Commit** — `git commit -m 'feat(naming): MediaInfo VideoCodec, VideoBitDepth, AudioLanguages, SubtitleLanguages, Simple and Full tokens; every video preset states dynamic range and codec' -- pkg/naming/`

---

### Task 4: `pkg/quality.AugmentFromMediaInfo`

**Files:**
- Modify: `pkg/release/quality.go` — export `func QualityFor(src commonv1.Source, res int32, mod commonv1.Modifier) (commonv1.Quality, bool)` over `qualityTable`
- Create: `pkg/quality/augment.go`
- Test: `pkg/quality/augment_test.go`

**Interfaces:**
- Produces: `func AugmentFromMediaInfo(q commonv1.Quality, mi *commonv1.MediaInfo) (commonv1.Quality, bool)` (changed reports whether anything moved).

- [ ] **Step 1: Failing test**

```go
func TestAugmentFromMediaInfoCorrectsResolutionAndKeepsSource(t *testing.T) {
	mi := &commonv1.MediaInfo{Width: 1920, Height: 1080, VideoCodec: "hevc", VideoBitrateKbps: 4000}
	q, changed := AugmentFromMediaInfo(commonv1.Quality{Name: "Bluray-2160p", Source: commonv1.SourceBluray, Resolution: 2160}, mi)
	require.True(t, changed)
	require.Equal(t, commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}, q)

	q, changed = AugmentFromMediaInfo(commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}, mi)
	require.True(t, changed)
	require.Equal(t, int32(1080), q.Resolution)
	require.Equal(t, commonv1.SourceUnknown, q.Source, "the probe never supplies a source")

	q, changed = AugmentFromMediaInfo(commonv1.Quality{Name: "Remux-1080p", Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierRemux}, mi)
	require.True(t, changed)
	require.Equal(t, commonv1.ModifierNone, q.Modifier, "4 Mbit/s HEVC is not a remux")
	require.Equal(t, "Bluray-1080p", q.Name)

	same, changed := AugmentFromMediaInfo(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}, mi)
	require.False(t, changed)
	require.Equal(t, "Bluray-1080p", same.Name)

	_, changed = AugmentFromMediaInfo(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}, nil)
	require.False(t, changed, "no probe, no change")
}
```

- [ ] **Step 2: Run** — FAIL.

- [ ] **Step 3: Implement**

```go
// remuxFloorKbps: below this an AVC/HEVC stream is an encode, not a disc remux.
const remuxFloorKbps = 20000

func AugmentFromMediaInfo(q commonv1.Quality, mi *commonv1.MediaInfo) (commonv1.Quality, bool) {
	if mi == nil || mi.Width == 0 || mi.Height == 0 {
		return q, false
	}
	out := q
	if res := mediainfo.ResolutionFromDimensions(mi.Width, mi.Height); res != 0 && res != q.Resolution {
		out.Resolution = res
	}
	if q.Modifier == commonv1.ModifierRemux && mi.VideoBitrateKbps > 0 && mi.VideoBitrateKbps < remuxFloorKbps {
		switch strings.ToLower(mi.VideoCodec) {
		case "h264", "hevc", "vc1":
			out.Modifier = commonv1.ModifierNone
		}
	}
	if out == q {
		return q, false
	}
	if named, ok := release.QualityFor(out.Source, out.Resolution, out.Modifier); ok {
		out.Name = named.Name
	} else {
		out.Name = "Unknown"
	}
	return out, true
}
```

`pkg/quality` already reads CRD types; confirm `pkg/quality` → `pkg/release` and `pkg/mediainfo` introduce no import cycle (`go list -deps ./pkg/quality | grep -c pkg/quality` must stay 1). If `pkg/release` imports `pkg/quality`, put `QualityFor` in `pkg/release` and call it from a new tiny package `pkg/quality/augment` instead; the plan's later tasks import whichever package holds `AugmentFromMediaInfo`.

- [ ] **Step 4: Run** — PASS. Falsify: replace the resolution line with `_ = res` and confirm the first assertion fails by name; restore.

- [ ] **Step 5: Commit** — `git commit -m 'feat(quality): AugmentFromMediaInfo corrects resolution and a false remux from the probe, keeps the source' -- pkg/quality/augment.go pkg/quality/augment_test.go pkg/release/quality.go`

---

### Task 5: `release.ParsePath` folder fallback

**Files:**
- Modify: `pkg/release/parse.go`, `pkg/release/types.go` (`ParsedRelease.FromFolder bool`)
- Test: `pkg/release/parsepath_folder_test.go`

**Interfaces:**
- Produces: `ParsedRelease.FromFolder` true when title, year and ids came from an ancestor folder because the basename failed.

- [ ] **Step 1: Failing test** (real folder names from the owner's library)

```go
func TestParsePathFallsBackToTheItemFolder(t *testing.T) {
	p, err := ParsePath("/data/media/movies/A Scanner Darkly (2006) {tmdb-3509}/2ef6f194995e4a11b055d0f2354ef0ba.mp4", Options{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	require.True(t, p.FromFolder)
	require.Equal(t, "A Scanner Darkly", p.Title)
	require.Equal(t, 2006, p.Year)
	require.Equal(t, "3509", p.IDs["tmdb"])
	require.Equal(t, commonv1.SourceUnknown, p.Quality.Source)
	require.Empty(t, p.Group)

	p, err = ParsePath("/data/media/movies/A Scanner Darkly (2006) {tmdb-3509}/A.Scanner.Darkly.2006.1080p.BluRay.x264-GRP.mkv", Options{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	require.False(t, p.FromFolder, "a basename that parses is never overridden by its folder")
	require.Equal(t, "GRP", p.Group)

	_, err = ParsePath("/data/media/movies/junk/2ef6f194995e4a11b055d0f2354ef0ba.mp4", Options{Kind: commonv1.MediaKindMovie})
	require.Error(t, err, "neither the name nor a folder names an item")

	e, err := ParsePath("/data/media/tv/Breaking Bad (2008) [tvdbid-81189]/Season 01/2ef6f194995e4a11b055d0f2354ef0ba.mkv", Options{Kind: commonv1.MediaKindEpisode})
	require.Error(t, err, "an episode file needs its numbering from its own name; the folder cannot say which episode it is")
	_ = e
}
```

- [ ] **Step 2: Run** — FAIL (the first case errors today).

- [ ] **Step 3: Implement** in `ParsePath`: after `Parse(base, o)` fails and `o.Kind` is `MediaKindMovie` (or empty and `ClassifyKind` says movie), walk `segments[len-2]` down to `segments[1]`, strip a comic/season shape is not needed for movies, `Parse(dir, o)`; the first success becomes `p` with `FromFolder = true`, `Quality = commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}`, `Revision = revisionOrDefault("")`, `Group = ""`, `Hash = ""`, then merge `dirIDs` as today. Episodes keep the error (the spec's D4 covers movies only; the episode case is Sonarr's own behaviour).

- [ ] **Step 4: Run** — PASS; run the whole package (`go test ./pkg/release/ -count=1`, the 130-title corpus).

- [ ] **Step 5: Commit** — `git commit -m 'feat(release): ParsePath attributes a movie file by its item folder when the basename does not parse' -- pkg/release/`

---

### Task 6: one renderer for import and rename — `pkg/naming/catalogctx`

**Files:**
- Create: `pkg/naming/catalogctx/context.go`, `pkg/naming/catalogctx/context_test.go`
- Modify: `app/import/worker/fileimport/worker.go` (`base := ...` → `catalogctx.Movie(&movie)`), `naming.go` (`destinationPath` → `catalogctx.MovieFilePath`), `episode_import.go` (`episodeDestination` → `catalogctx.EpisodeFilePath`), `nonvideo.go` (`engineFor` → `catalogctx.EngineFor`)

**Interfaces:**
- Produces:

```go
package catalogctx
func EngineFor(root *catalogv1alpha1.RootFolder) naming.Engine
func Movie(m *catalogv1alpha1.Movie) (naming.Context, bool)              // false until status.metadata.title is set
func Episode(s *catalogv1alpha1.Series, eps []catalogv1alpha1.Episode) (naming.Context, bool)
// File merges the release-time spec and the probe into c.
func File(c naming.Context, spec *catalogv1alpha1.MediaFileSpec, mi *commonv1.MediaInfo) naming.Context
// MovieFilePath renders <root>/<folder>/<file><ext>; ext is the container the
// probe reports, else the source path's extension.
func MovieFilePath(root *catalogv1alpha1.RootFolder, m *catalogv1alpha1.Movie, c naming.Context, ext string) (string, error)
func EpisodeFilePath(root *catalogv1alpha1.RootFolder, s *catalogv1alpha1.Series, c naming.Context, ext string) (string, error)
func ContainerExt(mi *commonv1.MediaInfo, fallbackPath string) string  // ".mkv" for matroska, ".mp4" for mov,mp4,m4a...
```

- [ ] **Step 1: Failing test** `TestImportAndRenameRenderTheSamePath`: build a Movie with metadata, a RootFolder (Plex dialect), a MediaFileSpec (quality Bluray-1080p, group GRP, importedFrom.releaseTitle with x265) and a MediaInfo (hevc, HDR10); assert `MovieFilePath` equals `/data/media/movies/Akira (1988) {tmdb-149}/Akira (1988) - [Bluray-1080p] [HDR10] [x265]-GRP.mkv`; assert `ContainerExt(&commonv1.MediaInfo{Container: "matroska"}, "x.mp4") == ".mkv"` and `ContainerExt(nil, "x.mp4") == ".mp4"`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** by moving the code from `worker.go:286-296`, `naming.go:destinationPath` and `episode_import.go:episodeDestination` into the package; `File` sets `Quality, Revision, ReleaseGroup, Edition, CustomFormats (spec.matchedFormats), MediaInfo (*mi when non-nil), ReleaseTitle (spec.importedFrom.releaseTitle)`. Keep the per-call `folderOverride` (`movie.Spec.Folder`) behaviour. Container mapping: `matroska,webm`→`.mkv`, `mov,mp4,m4a,3gp,3g2,mj2`→`.mp4`, `avi`→`.avi`, else fallback.
- [ ] **Step 4: Run** — `go test ./pkg/naming/... ./app/import/worker/fileimport/ -count=1` (envtest) — PASS with no behaviour change for existing tests.
- [ ] **Step 5: Commit** — `git commit -m 'refactor(naming): one renderer for import and rename -- pkg/naming/catalogctx' -- pkg/naming/catalogctx/ app/import/worker/fileimport/`

---

### Task 7: import probes the file and names it from the probe

**Files:**
- Modify: `app/import/worker/fileimport/process.go` (movie), `episode_import.go` (episode)
- Test: `app/import/worker/fileimport/probe_envtest_test.go`

**Interfaces:**
- Consumes: `mediainfo.Probe`, `quality.AugmentFromMediaInfo`, `catalogctx.File`, `catalogctx.ContainerExt`.
- Produces: the imported file's name carries the codec; `spec.quality` is the corrected quality.

- [ ] **Step 1: Failing test**: with `ffprobe` present (skip otherwise), copy `test/data/mediainfo/sample_hevc_10bit.mkv` into a Download's content root as `The.Matrix.1999.2160p.BluRay.x264-SPARKS.mkv` (the fixture's real dimensions decide the expected resolution; read them with `mediainfo.Probe` in the test and compute the expected name through `catalogctx`), import it through the fixture's `importOne`, and assert: `mf.Spec.Quality.Resolution` equals the probe's, `mf.Spec.Quality.Source == bluray`, `filepath.Base(mf.Spec.Path)` contains `[h265]` and the probe's `[Quality Full]`, not `2160p`.
- [ ] **Step 2: Run** — FAIL (name has no codec; resolution 2160).
- [ ] **Step 3: Implement**: in `processFile` after `parseMediaFile` succeeds and before `pc.profile.Allowed`: `mi, _, perr := mediainfo.Probe(ctx, srcPath)`; on error log at Warn and continue with `mi = nil` (an unprobeable file still imports under its name, as today); `parsed.Quality, _ = quality.AugmentFromMediaInfo(parsed.Quality, mi)`; `nctx = catalogctx.File(nctx, specFromParsed, mi)`; the destination uses `catalogctx.ContainerExt(mi, srcPath)`. Same in `importEpisodeFile`. The ranking walk (`c.ranked`) keeps the name-only parse: it only orders candidates.
- [ ] **Step 4: Run** — PASS; whole package green.
- [ ] **Step 5: Commit** — `git commit -m 'feat(importarr): an import probes the file, corrects its quality from the probe and names it for its codec' -- app/import/worker/fileimport/`

---

### Task 8: rescan probes, augments and attributes by folder

**Files:**
- Modify: `app/import/worker/rescan/mediafile.go` (`attributeMediaFile`, `freshVideoSpec`), `keptoutput.go` (reuse one probe per file)
- Test: `app/import/worker/rescan/folder_envtest_test.go`

- [ ] **Step 1: Failing tests**: (a) a junk-named HEVC fixture copy inside `A Scanner Darkly (2006) {tmdb-3509}/` under a movie RootFolder whose scan state knows that movie → attributed to it, `spec.quality.resolution` from the probe, `spec.releaseGroup == ""`; (b) the same file in a folder naming a movie the catalogue lacks → `unmatched` with `CodeNoMatch` (never created); (c) a `2160p`-named 1080p fixture → `spec.quality.name == "Bluray-1080p"`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: `attributeMediaFile` keeps `release.ParsePath` (now folder-aware); after `MatchMovie`, probe once (`mediainfo.Probe`; the kept-output tag check reads the same `*Raw`/`MediaInfo` rather than probing again — refactor `probeTranscodeProfile` to accept an already-probed `MediaInfo` when given), then `parsed.Quality, _ = quality.AugmentFromMediaInfo(parsed.Quality, mi)` before `freshVideoSpec`. A `FromFolder` parse with `result.Unmatched` stays unmatched with the existing code; `created` from a `FromFolder` parse is allowed only when `result.TmdbID != 0` came from the folder's id (a title-and-year match alone from a folder does not create an item — the folder is evidence of identity, not a request to add).
- [ ] **Step 4: Run** — PASS; package green.
- [ ] **Step 5: Commit** — `git commit -m 'feat(importarr): rescan probes each file, corrects its quality and attributes a junk-named file by its item folder' -- app/import/worker/rescan/`

---

### Task 9: catalogarr renders `status.naming`

**Files:**
- Modify: `app/catalog/controller/mediafile/mediafile_controller.go` (`knownStatus`, `statusOf`, `statusAC`, reconcile), `watch.go` (owner and RootFolder mappings)
- Create: `app/catalog/controller/mediafile/naming.go`
- Test: `app/catalog/controller/mediafile/naming_envtest_test.go`

**Interfaces:**
- Consumes: `catalogctx.*`, `quality.AugmentFromMediaInfo`.
- Produces: `status.naming` and the `NamingCurrent` condition; a watch on Movie, Series, Episode and RootFolder that enqueues their MediaFiles.

- [ ] **Step 1: Failing tests** (envtest): (a) a MediaFile of a Movie with metadata, under a RootFolder, with `status.mediaInfo` seeded → `status.naming.expectedPath` is `catalogctx.MovieFilePath(...)` and `current` is false when `spec.path` differs, the condition `NamingCurrent=False`; (b) no `mediaInfo` yet → `reason: ProbePending`, `expectedPath` empty; (c) the Movie's title changes → the MediaFile is re-reconciled and `expectedPath` follows (the watch); (d) **release test**: seed `status.naming` on a MediaFile whose file is then removed, reconcile → `FileMissing` path, assert through a direct client that `managedFields` for `k8s.ManagerCatalogarr` still owns `f:status.f:naming` and the value is intact.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: `knownStatus` gains `Naming *catalogv1alpha1.NamingStatus`; `statusOf` copies it; `statusAC` emits it with `WithNaming(catalogac.NamingStatus().WithExpectedPath(...).WithCurrent(...).WithReason(...).WithQuality(...))`; `naming.go`'s `renderNaming(ctx, mf, known) *NamingStatus` loads the owner (Movie or Series+Episodes by `spec.mediaRef`, the RootFolder by the owner's `spec.rootFolderRef`) and returns `MetadataPending`/`ProbePending`/`TranscodePending` (an unincorporated TranscodeJob, the existing `latestUnincorporatedTranscode`)/`Unrenderable` (a render error) or the path; `Current = filepath.Clean(mf.Spec.Path) == expectedPath`; `Quality` is `AugmentFromMediaInfo(spec.quality, mediaInfo)`. Called on every reconcile path that applies status, including the early returns (pass `known` through). Condition `NamingCurrent` with reason `Current`/`Stale`/`<Reason>`. `watch.go`: `handler.EnqueueRequestsFromMapFunc` for Movie/Series/Episode using the existing `IndexMediaFileByTarget` index, and for RootFolder by listing MediaFiles whose owner's `rootFolderRef` matches (through the owner list; cached, cheap).
- [ ] **Step 4: Run** — PASS; whole package (`go test ./app/catalog/controller/mediafile/ -count=1`, envtest).
- [ ] **Step 5: Commit** — `git commit -m 'feat(catalogarr): the MediaFile reconciler proposes each file'"'"'s canonical path in status.naming and mirrors it as NamingCurrent' -- app/catalog/controller/mediafile/`

---

### Task 10: importarr's rename controller

**Files:**
- Create: `app/import/controller/rename/controller.go`, `apply.go`, `apply_test.go`, `controller_envtest_test.go`
- Modify: `app/import/run.go` (register under the controller role), `config/rbac/` via `make manifests` (mediafiles get/list/watch/patch; rootfolders, movies, series, episodes get/list/watch; events create), `cmd/clustarr` registration guard list if it enumerates controllers by name

**Interfaces:**
- Produces: `rename.Apply(ctx, c client.Client, mf *catalogv1alpha1.MediaFile, dryRun bool) (Outcome, error)` with `Outcome{From, To, Reason string; Moved bool}` and reasons `NotCurrent` (nothing to do), `Held` (`status.naming.reason` set), `Collision`, `Changed`, `DryRun`; the controller `Reconciler` watching MediaFiles with a predicate on `NamingCurrent=False`.

- [ ] **Step 1: Failing tests** (envtest + a temp dir under the fixture's data root): (a) `renameFiles: true`, a MediaFile with `status.naming{expectedPath, current:false}` and a real file at `spec.path` → after reconcile the file is at `expectedPath`, `spec.path`/`sizeBytes`/`modTime`/`quality` updated under `k8s.ManagerImportarr`, every other frozen field intact (assert the full spec and `managedFields`), an Event `Renamed` recorded; sidecar `.en.srt` moved alongside; (b) a file already at `expectedPath` → `Collision`, nothing moved, an Event; (c) **changed in flight**: a second writer truncates the file between the controller's read and its move (inject through `Apply`'s `beforeMove func()` test hook) → `Changed`, nothing moved; (d) `renameFiles` unset → the controller does nothing; (e) `dryRun` → `Outcome{Moved:false, Reason:"DryRun"}` and the file untouched.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**:

```go
func Apply(ctx context.Context, c client.Client, api client.Reader, mf *catalogv1alpha1.MediaFile, dryRun bool) (Outcome, error) {
	var fresh catalogv1alpha1.MediaFile
	if err := api.Get(ctx, client.ObjectKeyFromObject(mf), &fresh); err != nil { return Outcome{}, err } // the lost-update rule
	n := fresh.Status.Naming
	out := Outcome{From: fresh.Spec.Path}
	switch {
	case n == nil || n.ExpectedPath == "" || n.Current: out.Reason = "NotCurrent"; return out, nil
	case n.Reason != "": out.Reason = "Held"; return out, nil
	}
	out.To = n.ExpectedPath
	if filepath.Dir(out.To) != filepath.Dir(out.From) { out.Reason = "Held"; return out, nil } // files only (D5)
	if _, err := os.Lstat(out.To); err == nil { out.Reason = "Collision"; return out, nil }
	st, err := os.Stat(out.From)
	if err != nil { return out, err }
	if st.Size() != fresh.Spec.SizeBytes || !st.ModTime().Equal(fresh.Spec.ModTime.Time) { out.Reason = "Changed"; return out, nil }
	if dryRun { out.Reason = "DryRun"; return out, nil }
	if err := fsops.MoveAtomic(out.From, out.To); err != nil { return out, err }
	for _, s := range fresh.Status.Sidecars { moveSidecar(s.Path, out.From, out.To) } // same stem swap, best effort, logged
	// importarr's complete spec, with path, size, mtime and quality overridden.
	if err := rescan.ApplyRenamed(ctx, c, &fresh, out.To, st, n.Quality); err != nil { return out, err }
	out.Moved = true
	return out, nil
}
```

`rescan.ApplyRenamed` is a thin exported wrapper over `applyObserved` with `reassertFrozen` plus the quality override, so the apply is the manager's whole set. The controller: `For(&MediaFile{})` with a predicate `NamingCurrent == False`, loads the owner's RootFolder, returns early unless `RenameFilesOrDefault()`, calls `Apply`, records an Event, and requeues after 1 minute on `Changed` (the rescan re-observes).

- [ ] **Step 4: Run** — PASS; `make manifests` and the RBAC byte-equality test in `cmd/clustarr`; the start envtest that runs importarr under its real role.
- [ ] **Step 5: Commit** — `git commit -m 'feat(importarr): a rename controller moves a MediaFile to the path catalogarr proposes when its RootFolder allows it' -- app/import/controller/rename/ app/import/run.go config/rbac/ charts/clustarr/templates/`

---

### Task 11: LibraryScan rename pass (dry run and apply)

**Files:**
- Modify: `app/import/worker/rescan/worker.go` (after the walk, when `spec.rename != off`), `mediafile.go`
- Test: `app/import/worker/rescan/rename_envtest_test.go`

- [ ] **Step 1: Failing test**: a RootFolder with `renameFiles` unset, two MediaFiles under it whose `status.naming` is stale (seeded), a LibraryScan `rename: dryRun` over the root → `status.renamed` lists both with `Reason: DryRun`, files untouched, `filesRenamed == 0`; then `rename: apply` → both moved, `filesRenamed == 2`, `status.renamed` entries with empty reason; a third MediaFile outside `spec.subpath` untouched.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**: after the existing walk completes (so the scan's own observations land first), list MediaFiles whose `spec.path` is under the scanned subtree (the existing `IndexMediaFileByPath`), call `rename.Apply(ctx, c, api, &mf, spec.Rename == dryRun)` for each with `NamingCurrent=False`, append `RenamedFile{From, To, Reason}` to the scan's status (cap 200 with the existing `capUnmatched` pattern), count `FilesRenamed`. Progress heartbeats as the walk does.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `git commit -m 'feat(importarr): a LibraryScan rename pass, dry run or apply, over the scanned subtree' -- app/import/worker/rescan/`

---

### Task 12: UI — the rename action and the RootFolder switch

**Files:**
- Modify: `ui/actions/actions.go` (`RenameFiles`), `ui/forms/kinds.go` (RootFolder `naming.renameFiles` field), the library page's per-item "Rename" button and the settings page's switch (`ui/views/*.templ`, regenerate with `make templ` or the repo's target)
- Test: `ui/actions/rename_envtest_test.go`; `cmd/clustarr/ui_rbac_test.go` stays green (no new grant: LibraryScan create already exists)

- [ ] **Step 1: Failing test**: `RenameFiles(ctx, creator, ns, rootFolder, subpath, dryRun)` creates a LibraryScan with `spec.rename` `dryRun`/`apply`, `subpath` validated as `RescanPath` does, label `LabelOrigin: OriginUI`; an absolute subpath is `ErrInvalid`.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** beside `RescanPath`; the templ button posts to the existing actions handler with `rename=dryRun|apply`; the settings form field is a boolean under the Naming group.
- [ ] **Step 4: Run** — `go test ./ui/... ./cmd/clustarr/ -run 'Rename|UIRole|UINeverWrites' -count=1` — PASS.
- [ ] **Step 5: Commit** — `git commit -m 'feat(ui): rename files action (dry run or apply) and the RootFolder renameFiles switch' -- ui/`

---

### Task 13: e2e scenario written, docs

**Files:**
- Create: `test/e2e/rename_test.go` (build tag `e2e`, never run per the project's standing rule)
- Modify: `docs/superpowers/plans/2026-09-18-remaining-work.md` (the naming items closed; folder renames carried), `CLAUDE.md` (Status: a paragraph under the gap-fix/M7 entries naming probe-driven naming; Gotchas: "`naming.Context.MediaInfo` was never filled before 2026-09-24, so no MediaInfo token ever rendered; every video preset now states range and codec, and a file never probed renders exactly as before")

- [ ] **Step 1: Write the scenario**: import an `x264`-named HEVC clip, expect `[h265]` in the imported name; place a junk-named clip in a `{tmdb-N}` folder and rescan, expect attribution; rescan with `rename: dryRun` then `apply` over a stale-named file, expect `status.renamed` then the move.
- [ ] **Step 2: `go vet -tags e2e ./test/e2e/`** compiles.
- [ ] **Step 3: Commit** — `git commit -m 'test(e2e): scenario 19, probe-driven naming and the rename pass; docs' -- test/e2e/rename_test.go docs/superpowers/plans/2026-09-18-remaining-work.md CLAUDE.md`

Only commit `CLAUDE.md` if `git status` shows it unmodified by another session at that moment; otherwise record the gotcha text in the task report for the controller to apply later.
