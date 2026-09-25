# Probe-driven naming: codec tokens, quality from the file, and a rename pass

**Status:** Proposed, 2026-09-24. Approved in conversation the same day; the
written spec is for review. Once approved it amends the design of record
(`2026-09-18-clustarr-design.md` §4.2, §7, §8.4, §8.5) and amendment 1 (§A1.5,
§A3.4) for the parts it covers.

**Why:** a library file's name lies. Files transcoded outside clustarr (Tdarr)
keep their `x264` tokens after becoming HEVC; squasharr's own output keeps the
source's stem; rescan freezes `spec.quality` from the basename alone and reports
a file whose basename does not parse as unmatched even inside a folder that
names the item; and no naming preset can state a codec, because `pkg/naming` has
no codec token and nothing ever fills `Context.MediaInfo`. Nothing renames an
existing library file. The owner asked (2026-09-24) that clustarr detect the
codec and every other naming fact from the file itself, name files from what it
finds, and rename what is already in the movies root, with codec tokens added
to the presets by default.

## 1. Decisions

- **D1. The probe is the authority for what a probe can prove.** Resolution,
  video codec, bit depth, dynamic range, audio codec, channels and languages
  come from `mediainfo.Probe`, at import and at rescan, and override the name.
  **Source** (Bluray, WEB, Remux, …) and **revision** cannot be probed and stay
  as parsed from the release name, Radarr's rule (`AugmentQualityFromMediaInfo`
  supplies resolution only). `formatScore`, `matchedFormats` and the rest of
  the release-time spec never change (design §8.5).
- **D2. Presets state the codec by default.** Every video preset gains
  `{ [MediaInfo VideoDynamicRangeType]}{ [MediaInfo VideoCodec]}` after the
  quality block (owner's call, 2026-09-24). A library imported before this
  therefore differs from its canonical names on the first rename pass; the pass
  is off until `renameFiles` is set, and it reports before it moves (§5).
- **D3. Two writers, the existing split.** catalogarr's MediaFile reconciler
  owns the probe and computes the canonical path into `status.naming`;
  importarr, which owns `spec.path`, performs the move and applies the new
  path. Neither writes the other's field.
- **D4. Identification uses the folder when the basename fails.** A file whose
  basename does not parse is attributed by its item folder's ids or title and
  year. A file is unmatched only when neither its name nor its folder names an
  item. Nothing is inferred from probe data alone: the probe describes a file,
  it does not say which film it is.
- **D5. Files only, not folders, in v1.** The pass renames a file within its
  item folder. Folder renames follow metadata changes and are a separate pass.
- **D6. A rename never crosses a container boundary and never touches a file
  another job holds.** The extension is the file's real container (from the
  probe, not the old name); a file with an unincorporated TranscodeJob, a
  `Recycling` state or an open `LibraryScan` over its folder is skipped and
  reported.

## 2. Naming tokens

`pkg/naming`'s token table gains, all read from `Context.MediaInfo`
(`commonv1.MediaInfo`):

| Token | Value | Rule |
| --- | --- | --- |
| `{MediaInfo VideoCodec}` | `x264`, `h264`, `x265`, `h265`, `AV1`, `VP9`, `XviD`, `DivX`, `VC1`, `MPEG2`, else ffprobe's `codec_name` upper-cased | Radarr's `FormatVideoCodec`: AVC is `x264` when the release title (`spec.importedFrom.releaseTitle`, else the original file name) says `x264`, else `h264`; HEVC likewise `x265`/`h265` |
| `{MediaInfo VideoBitDepth}` | `8`, `10`, `12` | `videoBitDepth` |
| `{MediaInfo AudioLanguages}` | `[EN+JA]` style, BCP-47 upper-cased, distinct, in stream order; empty when one language only or unknown | Radarr's `MediaInfo AudioLanguages` with its "skip a single language" rule |
| `{MediaInfo SubtitleLanguages}` | same shape | subtitle streams, embedded only |
| `{MediaInfo Simple}` | `{VideoCodec} {AudioCodec}` | Radarr |
| `{MediaInfo Full}` | `{VideoCodec} {AudioCodec}{ AudioLanguages}{ SubtitleLanguages}` | Radarr |

The existing `{MediaInfo AudioCodec}`, `{MediaInfo AudioChannels}` and
`{MediaInfo VideoDynamicRangeType}` are unchanged. `{MediaInfo AudioCodec}`
picks the **default** audio stream, else the first, as Radarr does (today the
first). An unknown token is still `ErrUnknownToken`.

**Presets.** The video file templates become:

```text
movieFile:   {Movie CleanTitle}{ (Release Year)}{ - [Quality Full]}{ [MediaInfo VideoDynamicRangeType]}{ [MediaInfo VideoCodec]}{-Release Group}
episodeFile: {Series TitleWithoutYear}{ (Series Year)} - S{season:00}E{episode:00}{ - Episode CleanTitle:90}{ [Quality Full]}{ [MediaInfo VideoDynamicRangeType]}{ [MediaInfo VideoCodec]}{-Release Group}
```

and the anime and daily templates the same way. Bracketed optional blocks
already render to nothing when their token is empty, so a file never probed
(no `MediaInfo`) renders exactly as before, and the dangling-separator rule
holds. `Config.Overrides[TokenMovieFile]` and friends still replace a template
whole. The Plex and Jellyfin dialect rules are unaffected: Plex reads only what
precedes ` - `.

## 3. Quality from the file

`pkg/quality` gains `AugmentFromMediaInfo(q commonv1.Quality, mi
*commonv1.MediaInfo) (commonv1.Quality, bool)`:

- `resolution` becomes `mediainfo.ResolutionFromDimensions(width, height)`
  when the probe has dimensions and they disagree with the name, or when the
  name gave none. The name's `2160p` on a 1920×1080 file becomes `1080p`.
- `source` is kept. A name with no source keeps `unknown`; the probe cannot
  supply one.
- `name` is re-derived from the corrected pair through the existing quality
  table (`Bluray-1080p`), so `{Quality Full}` renders the corrected quality.
- `modifier` is kept, except that `remux` is dropped when the codec is not a
  Blu-ray codec at Blu-ray bitrate (AVC or HEVC above 20 Mbit/s, or VC-1); a
  10-bit HEVC file at 4 Mbit/s named `Remux` is not one.

**Where it applies.**
- **Import** (`app/import/worker/fileimport`): after `release.ParsePath` and
  before the profile check, the worker runs `mediainfo.Probe` once (the design
  of record §8.4 already lists this step; the code never did it) and augments
  the parsed quality. The probe result fills `naming.Context.MediaInfo`, so the
  destination name carries the codec from the first import. The profile's
  `Allowed` and the upgrade comparison see the corrected quality. The probe is
  importarr's for naming only; `status.mediaInfo` stays catalogarr's.
- **Rescan** (`app/import/worker/rescan`): `freshVideoSpec` augments the same
  way. The worker already probes kept outputs for the `CLUSTARR_PROFILE` tag;
  the same probe now serves the quality and the name.
- **The MediaFile reconciler** (catalogarr): a re-probe that changes the
  corrected quality (a Tdarr transcode observed through the fingerprint
  annotation or the 24 h recheck) updates `status.naming` (§4). `spec.quality`
  is importarr's and is not rewritten by catalogarr; the rename pass (§5)
  re-applies it alongside `spec.path`, from the same augmentation, under
  importarr's manager.

## 4. `status.naming`, catalogarr's proposal

`MediaFileStatus` gains:

```go
// Naming is the file's canonical path under its RootFolder's preset,
// rendered from the item's metadata, the release-time spec and the probe.
Naming *NamingStatus `json:"naming,omitempty"`

type NamingStatus struct {
	// ExpectedPath is the absolute path the preset renders; empty until
	// the item's metadata and the probe are both present.
	ExpectedPath string `json:"expectedPath,omitempty"`
	// Current is true when spec.path equals expectedPath.
	Current bool `json:"current"`
	// Reason says why expectedPath is empty or the file is not renameable:
	// MetadataPending, ProbePending, TranscodePending, Recycling, Unrenderable.
	Reason string `json:"reason,omitempty"`
	// Quality is the probe-corrected quality (§3), which the rename pass
	// re-applies into spec.quality.
	Quality *commonv1.Quality `json:"quality,omitempty"`
}
```

The MediaFile reconciler renders it on every reconcile from the owning item's
`status.metadata`, `spec` (quality, revision, group, edition, custom formats,
`importedFrom.releaseTitle`) and `status.mediaInfo`, through the RootFolder's
`naming` config and the same `destinationPath` rendering fileimport uses,
extracted into `pkg/naming`'s `Engine` so import and rename cannot drift
(`TestImportAndRenameRenderTheSamePath`). The extension is the probe's
container. `Naming` is one of `ControllerFields` under `k8s.ManagerCatalogarr`
and is sent on every apply, including the early returns (the release trap).

A `NamingCurrent` condition mirrors `Current` for `kubectl` and the UI.

## 5. The rename pass, importarr's move

**Configuration.** `RootFolder.spec.naming.renameFiles *bool` (default false;
pointer plus accessor, the typed-client gotcha). `LibraryScan.spec.rename`
(`off|dryRun|apply`, default `off`) runs one pass over the scan's subtree
regardless of the RootFolder switch. `ui/actions.RenameFiles(rootFolder,
subpath, dryRun)` creates such a LibraryScan; a per-item "Rename" on the
library page creates one with `subpath` set to the item folder. The UI never
writes status.

**Trigger.** A new importarr consumer, `importarr-rename`, on
`clustarr.evt.catalog.mediafile.naming.<uid>` (published by the MediaFile
reconciler when `Current` flips false and the RootFolder has `renameFiles`),
plus the LibraryScan path for on-demand passes. Both call one function,
`rename.Apply(ctx, mediaFile, dryRun)`.

**Apply**, per MediaFile:

1. Re-`Get` the MediaFile (the lost-update rule) and refuse unless
   `status.naming.expectedPath` is set, `Current` is false and `Reason` is
   empty.
2. Refuse when `expectedPath` is not under the same item folder (D5), when a
   file already exists there (reported `Collision`; never overwritten), or when
   the file's fingerprint (size, mtime) differs from `spec` (a change in
   flight: reported `Changed`, and the rescan path re-observes it).
3. `fsops.MoveAtomic(spec.path, expectedPath)`, same filesystem by
   construction; sidecars in `status.sidecars` (subtitles, `.nfo`) move with
   the file to their renamed names.
4. Apply `spec.path`, `spec.sizeBytes`, `spec.modTime` and `spec.quality`
   (from `status.naming.quality`) under `k8s.ManagerImportarr` through the one
   function that renders that manager's complete spec (the narrower-apply
   trap).
5. Record the move in `LibraryScan.status.renamed[]` (capped) for a scan pass,
   or an Event for the streaming pass, with `from` and `to`.

**Dry run** does steps 1-2 and records `{from, to, reason}` only.

**After the move.** The MediaFile reconciler sees the new path, recomputes
`ProbeHash` (it includes the path) and re-probes once; `Current` becomes true.
The 24 h `TranscodedRecheckInterval` and the fingerprint annotation are what
notice a Tdarr transcode in place; a transcode that changed the extension goes
through §6.

**squasharr.** `app/squash/worker.OutputPath` is unchanged (the stem is kept
so the swap is in place); the reconciler's swap sets `spec.path` as today, the
re-probe flips `Current`, and the pass renames. A Movie therefore reads
`[Bluray-1080p][x265]` after clustarr's own transcode without squasharr
knowing about naming.

## 6. Identification by folder

`release.ParsePath` today takes ids from ancestor folders but title and quality
from the basename only. It gains a fallback: when the basename does not parse
as the requested kind, parse the nearest ancestor folder whose name parses as
that kind, take its title, year and ids, and mark the result
`FromFolder: true`. Quality then comes from the probe (§3) with source
`unknown`, and the release group is empty.

- Rescan attributes such a file through the same `MatchMovie` path (ids first,
  then clean title and year). Only a single hit counts, as today.
- A file whose basename and folder both fail stays `CodeParseError`.
- A file that parses from its folder but whose folder names an item the
  catalogue does not have stays `CodeNoMatch`, never a speculative item (the
  never-guess rule).
- The `FromFolder` case never sets `spec.importedFrom.releaseTitle`, so
  `{MediaInfo VideoCodec}` renders `h264`/`h265` rather than `x264`/`x265`
  for it.

The **transcode-successor** rule from the external-transcoder draft (a new file
alone in an item folder whose MediaFile's path is gone) is subsumed: the new
file attributes through its folder, the old MediaFile reads `FileMissing`, and
the existing single-file-per-item replacement in rescan resolves the pair. That
draft's marker (which files are final) is still a separate decision.

## 7. Phases

| Wave | Content |
| --- | --- |
| N0 | API: `MediaFileStatus.naming`, `RootFolder.spec.naming.renameFiles`, `LibraryScan.spec.rename` and `status.renamed`; `make generate manifests`; `pkg/crdcheck` guards (the naming list capped, the pointer default reachable from Go) |
| N1 | `pkg/naming`: the six tokens, the preset change, `Engine.FilePath` extraction; `pkg/quality.AugmentFromMediaInfo`; `pkg/release.ParsePath` folder fallback; `pkg/mediainfo` codec normalisation. Pure, table-driven, fixtures from real ffprobe output under `test/data/mediainfo` |
| N2 | Import and rescan probe and augment; `status.naming` in the MediaFile reconciler; the `NamingCurrent` condition |
| N3 | The rename pass: `importarr-rename` consumer, the LibraryScan pass, dry run, sidecars, `ui/actions.RenameFiles`, RBAC, registration guard |
| N4 | e2e on kind: import an `x264`-named HEVC file, see it renamed `[x265]`; transcode in place, see the rename; a junk-named file in a `{tmdb-N}` folder attributed and renamed; dry run over the fixture root reports without moving |

## 8. Testing notes that follow from CLAUDE.md's gotchas

- `status.naming` is sent on every apply, including the early-return paths; the
  release test acts on a MediaFile that already has status and asserts
  `managedFields`.
- The rename's spec apply renders importarr's complete set; a test seeds a
  MediaFile with every frozen field and asserts none is released.
- The augmentation is falsified with a fixture whose name says `2160p` over a
  1080p probe; the test reverted to name-only must fail by name.
- `ParsePath`'s folder fallback is built through the real parser on real
  folder names from the owner's library (`A Scanner Darkly (2006) {tmdb-3509}`),
  not from a fixture shaped like the answer.
- A rename over NFS keeps `MoveAtomic` on one filesystem; the collision and
  changed-in-flight refusals are tested with a real second writer between the
  read and the move.
- No metric is labelled by title or path; the pass counts renames, refusals by
  reason, and dry runs.

## 9. Open questions

1. Should the first `renameFiles` pass over the existing movies root be run as
   a dry run first and reviewed, or applied directly? (Recommended: dry run,
   then apply per RootFolder.)
2. Folder renames (D5) as the next pass, or never?
