# Clustarr record: Transcoding

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Transcoding

A transcoded media file should be the final destination. If we detect a transcoded profile, we should not mark the media "CutoffUnmet" - instead it should be marked `Transcoded`.

The predicate is `app/catalog/controller/rollup.Transcoded` -- `spec.original` false, or the probe's `status.mediaInfo.transcodeProfile` (the `CLUSTARR_PROFILE` tag), or a transcode the transcode controller did not make: `status.mediaInfo.videoEncoder` naming an ffmpeg HEVC or AV1 encoder (`commonv1.MediaInfo.TranscodedElsewhere`; Tdarr's `Lavc… hevc_qsv` on the owner's library, 2026-09-29) -- and such a Movie or Episode reads phase `Transcoded`, with `CutoffMet=True` reason `Transcoded` (the one exception: a file missing an audio language a non-grafting profile wants, `MissingAudio` under UI below); `pkg/decision` rejects every release over it as `TranscodedFinal` (spec §4.2) -- a user-invoked search's too, since "Search now" grabs its best automatically, so only a person's own pick (Search `spec.grab`, exempt in the Search controller's `resolveGrab`) replaces it; an import refused for it reads `importplan.MessageExistingFileFinal`, which never blocklists (2026-10-07: a refused 21 GB grab was blocklisted and its file deleted) -- and transcode never encodes it again: a file another tool transcoded is
still remuxed into the MP4 layout when its video is compliant, and skipped
rather than encoded a second time otherwise (2026-10-15;
`transcodeplan.AlreadyTranscoded` counts only transcode's own output as
done), and so is transcode's own from before the MP4 layout -- a
`CLUSTARR_PROFILE`-tagged Matroska file, `standard.Version` 4 -- whose tag
keeps the standard from encoding it again. A good transcode baseline is kept as well (2026-10-16, after an afternoon's upgrades swapped compact HEVC and HDR files for 14-22 GB x264 Blu-rays): `pkg/decision`'s `baselineRejections` refuses an HDR file's replacement by a release that names no HDR (`LosesHDR`) and an HEVC or AV1 file's by an AVC release of no greater resolution (`AVCOverHEVC`), both from the probe (`decision.Current.VideoCodec`/`HDR`), a person's pick exempt as from `TranscodedFinal`; a Dolby Vision release is never taken (`DolbyVision`, `spec.override` only); the video built-ins score TRaSH's x265 (HD) 0, not -10000 (`quality.clustarrFormatScores`); and an upgrade must beat the current file's frozen formats as the profile scores them today (`quality.Profile.ScoreFormats`), not the score frozen at import. Import holds the same line from both files' probes (`decision.BaselineLoss`, in `importplan`'s gate), and refuses a movie file whose own streams say it is not the movie (`importplan.movieContentRejection`, a releaseFault, calibrated on the owner's 839 movie files): a Dolby Vision probe, a runtime under 65% of the movie's (one broadcast part), or one more than 15% and 10 minutes off when the release title does not name the movie (`decision.TitleNamesMovie`; an id query's vouch took No.Escape.1994 as The NeverEnding Story III on its year alone). An episode file's Dolby Vision probe is refused the same way (`importplan.episodeContentRejection`, 2026-10-22): a Netflix profile 5 season pack whose title named no Dolby Vision had replaced ten BluRay episodes of Cyberpunk: Edgerunners, which transcode then skipped (no HDR10 base layer) and a client without Dolby Vision showed in the wrong colours. A Dolby Vision file already in the library is no baseline for either rule (`baselineRejections` and `BaselineLoss` skip it, 2026-10-22): the library never wanted it, so an SDR AVC Blu-ray replaces it, where the HDR rule had held the ten Edgerunners files against the BluRay they came from. `catalog.clustarr.io/upgradeable: "true"` on a MediaFile has catalog and import read a transcoded file as the release it was imported from (`MediaFile.Final`, which `rollup.Transcoded` returns; owner, 2026-10-27: The Sopranos' WEB-DL transcodes, re-searched for Blu-rays): not final, so its item reads CutoffUnmet below the cutoff and an automatic grab replaces it, and no codec or HDR baseline, since those are the transcode's own (`itemread.CurrentOf`, `importplan.baselineLoss`). transcode still reads `Transcoded` and never encodes it again, and the file that replaces it is a new MediaFile without the annotation. A QualityProfile's `bitrateLimits` (owner, 2026-10-27, after a 167 MiB BiOMA WEB-DL of a 43-minute Sopranos episode played badly; the built-in `web-1080p` seeds 1080p 100k to 1M, since built-ins are immutable on the cluster) bound a file's overall bitrate per resolution in bytes per second (`quality.Profile.BitrateOutOfRange`, by the quality's resolution): a release outside the range is refused at search over the size check's runtime (`BelowMinimumBitrate`, `AboveMaximumBitrate`) and at import from the file's size and probed runtime (a releaseFault, a person's pick exempt); a file outside it does not fit the profile, so its item reads CutoffUnmet, reason `BitrateOutOfRange`, transcoded or not, and a release inside the range at any quality the profile takes (`quality.Profile.Lists`; its best tier still ranks first, so a WEB-first profile falls back to a Blu-ray once every WEB post fails, owner 2026-10-27) replaces it past the transcoded, baseline and upgrade rules (`videoItemState`, `grabplan.StillWants`, `importplan.bitrateRefit`). A file is judged by `MediaFile.BytesPerSecond`: its size before its first transcode swap when recorded (`spec.sourceSizeBytes`, which catalog's swap takeover writes, `app/remediation/main.go`), else its size now, over the probed runtime, so clustarr's own encodes, smaller than their releases, are never re-searched for that; a file transcoded before 2026-10-27 has no record and is judged as it is. A movie released in parts within one download -- two or more files numbered Part 1 to n (`importplan.PartNumber`: Part, pt, CD, Disc) whose probes agree on codec, size and HDR -- imports as one file, the parts joined losslessly through FFmpeg's concat demuxer (`importplan.foldParts`, `schema.ImportMove.Join`, `engine.Join`; 2026-10-16: 28, 42 and 49 Up each imported as half the film). A movie posted only as separate parts (56 Up's Part1-3, No Direction Home's Part.1-2; `docs/superpowers/specs/2026-10-16-movie-part-sets-design.md`) is one search result per indexer: the search agent adds each set of one indexer's posts of parts 1 to N (`release.PartOf`, `decision.PartSets`: same parsed title, year, quality, group and edition; GUID `parts:...`, `ReleaseInfo.Parts`), judged as the film with its parts' rejections; its grab is an entry per part (`status.downloads[].set`), never delayed or held; part 1 imports them all once every part is done (`lifecycle.SetReady`, `ImportInspectTask.Parts`, each file tagged with its part, `InspectedFile.Part`, so an obfuscated name joins), and the others follow it; a set import must join every part, any joined movie must run `importplan.JoinedShortPercent` (85%) of the film, and a part that fails, or a failed import, removes the rest. An execute's long steps (a join, a copy, a donor's reduction) ack their delivery in progress while they run (`fileimport` `keepAlive`), each join writes its own temporary file, and `engine.Join` fails a join that ran short of its parts (FFmpeg's concat demuxer ends quietly at a part it cannot open): on 2026-10-17 four of five joins outlived the consumer's 1-minute deadline, ran a second time beside the first, and 49 Up was placed as its first part. A new file under its current MediaFile's name replaces the file a transcode renamed away (recycled; the MediaFile re-pointed, never deleted), which 28, 42 and 49 Up left beside the film, and another MediaFile at the path the new file takes is deleted, so a path never has two. A probe that learns a new field raises `mediainfo.ProbeVersion`, which re-probes every file once through `status.probeVersion` -- never by changing `probeHash`, which caption and transcode read as the file's identity. Probing is `pkg/mediainfo/native` in the import agent (ADR-0017; it prints what ffprobe printed, so `ProbeVersion` stays 3): the remediation loop's probe planner (`app/remediation/probe`) asks for a probe on `CLUSTARR_WORK_PROBE` and incorporates the `clustarr-probes` record, and a `ProbeVersion` raise re-probes through the low lane.

Backlogs are not an object per file (2026-09-30,
`docs/superpowers/specs/2026-09-30-backlog-windowing-and-encoder-limits-design.md`):
a TranscodeProfile admits at most `--job-window` (32) of its files at once
into a window phase of `status.transcode` (Pending, Planned, Queued, Running,
Swapping), the next matching files by name, and counts the rest in
`status.waitingFiles` (`clustarr_transcode_backlog_files`); a window file
something holds (`Eligibility.Pending`: unprobed, a probe pending, a
standalone graft, an upgrade downloading) is Pending, never Planned, since
admission offers every Planned file a slot and a grant given back unused
backs its file off as an expired one does (2026-11-03: five Planned
Deca-Dence episodes whose season pack found no peers were granted and
refused both intel slots about 25 times a second for four hours, and
nothing transcoded); Succeeded retention
went with TranscodeJob (ADR-0016); a MediaFile carries `status.subtitles` only
when a subtitle may be wanted for it (the subtitles planner,
`app/caption/subtitleplan`, on the tagged audio). **Every transcode is the fixed standard, run in-process**
(2026-10-01, `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md`
and its "As built" section): `pkg/transcode/standard.Plan` decides (HEVC
Main 10, or Main 8-bit for SDR at 1080p or less; Dolby Vision 7/8.1 as
HDR10; since `standard.Version` 3 (2026-10-15) a profile whose
`policy.keepCodecs` lists `h264` copies 8-bit SDR H.264 into the layout
as compliant HEVC is copied -- remuxed, tagged, final while the profile
keeps H.264; once it no longer does, such a remux is the one tagged file
encoded again (`transcodeplan.KeptH264Dropped`, owner 2026-10-19: 5,956
remuxed files) -- while Hi10P, HDR and Dolby Vision H.264 still encode; since `standard.Version` 2 the MP4 layout,
`docs/superpowers/specs/2026-10-06-mp4-standard-design.md`: one `.mp4`,
HEVC tagged `hvc1`, whatever the profile's `container`
(`jobspec.OutputContainer`); per language an AAC 2.0 160k track listed
first (it carries the default) and then a Dolby surround track -- E-AC-3
or AC-3 copied, anything else AC-3 5.1 640k -- or AAC alone for mono or
stereo, commentary AAC, other mixes dropped; every subtitle beside the file in Plex's layout
`<stem>.<lang>[.forced|.sdh].srt` (spec §4.1: SubRip copied through
the `srt` muxer, WebVTT/`mov_text`/text converted; since `standard.Version`
5 ASS and SSA too, as plain text through `pkg/subtitles/asssrt` -- Plex's
web player shows an ASS subtitle only by burning it into the video, a video
transcode cluster-plex forbids, so an `.ass` sidecar made it refuse to
play; `agent convert-ass-sidecars` replaced the earlier `.ass` ones once,
originals under `/data/.clustarr/ass-backup`), written in the transcode's own pass and placed before the swap,
never over an existing file; since 2026-10-15 an attempt encodes, writes
its sidecars and verifies in the pool pod's scratch emptyDir
(`--scratch-dir /scratch`, sized by `spec.scratch`, `jobspec.Scratch`)
when 1.5x the source plus 1 GiB fits, and only the verified result is
staged beside its output on `/data` before the swap (`worker/scratch.go`),
so the NAS never carries the encode's writes or the MP4 faststart rewrite;
a larger source encodes beside its output as before; attachments dropped, chapters kept; a PGS
subtitle no text subtitle covers goes beside the MP4 as a `.sup` sidecar
(`standard.SidecarSUP`, `<stem>.<lang>[.forced|.sdh].sup`, copied through
FFmpeg's `sup` muxer; `standard.Version` 8, owner 2026-10-27: every output
is MP4, which carries no image subtitle), which Plex ignores and the `ocr`
subtitle provider reads -- `pkg/subtitles.ImageSidecar`, listed in
`SidecarExtensions` so it is renamed, recycled and deleted with its file,
but never an "existing" subtitle to the caption planner, which still wants
a text one for the language; a DVD subtitle has no such format and is
dropped, named. **The `.sup` is read into `<stem>.<lang>.srt` by the
caption pipeline, not the transcode** (owner, 2026-10-27: "convert .sup to
.srt when .sup is the only subtitle format provided in the blu-ray"): a
SubtitleProvider of type `ocr` is a local provider like `embedded`
(`pkg/subtitles/providers/pgsocr`, no credentials, the local tier, never
paced or throttled) serving the file's own image sidecars, and the
subtitles planner's `imageWants` makes a profile language wanted when an
image sidecar of its language and forced flag lies beside the file, no text
subtitle does, and an `ocr` provider serves it -- whatever the profile's
audio rules say, since the owner's `english` profile excludes English-audio
files and the disc's own English PGS is still worth reading; such a task
is `FetchTask.LocalOnly` (no remote provider is asked) and carries the
sidecars (`ImageSidecars`). A bare `<stem>.sup`, an untagged track's, is
the item's original language, as an untagged audio track is, and is listed
in `status.sidecars` with no language. The reading is `pkg/subtitles/pgs`
(the PGS segments decoded in Go, each display set a grey bitmap) and
`pkg/subtitles/ocr` (PaddleOCR's PP-OCRv5 recognition model, embedded, on
ONNX Runtime through purego from `$ORT_LIB_PATH`, lines split by their
rows' ink, greedy CTC; a track fewer than 60% of whose bitmaps read as text
is `ErrUnreadable` and left alone), wired only in the caption agent
(`app/caption/agent/ocr.go`, built at the first read, one track at a time)
so `cmd/manager` links no purego; `agent --self-check` proves the runtime
loads. The fetch worker heartbeats the delivery through a download
(`Worker.download`), since a film's track takes minutes. The Sopranos'
pilot (18 MB, 889 bitmaps) reads in 28 s with its speaker labels intact.
Until Version 7 such a file was Skipped whole (`HoldImageSubtitles`,
ruling R1): 129 files with DTS or TrueHD audio sat that way, their audio
playable natively on no Apple device or browser. Version 7's first cut kept
the file in Matroska with the image subtitles inside, and was withdrawn the
same day: the owner wants every output MP4, and the standard planned its
own interim output again (its AAC companion read as work), so the pilot of
The Sopranos was remuxed twice before the loop was caught -- a tagged
file's own output must never read as work. Since `standard.Version` 6
(owner, 2026-10-20), a text subtitle of the same language covers an image
one, provided it is forced only if the image one is (`standard.textCovers`),
and the covered image subtitle is dropped. Akira had held for its English
DVD track beside three English ASS ones; about 125 of the 458 held files
(106 movies, 19 episodes) had such cover. An untagged image track is kept
as uncovered.) and
`pkg/transcode/engine` runs it on FFmpeg 9's libraries through the ffgo
fork (`github.com/mediactl/ffgo`, tags `v0.0.0-clustarr.N`). There is no
argv engine, no `--worker-engine`, and no `ffmpeg`, `ffprobe` or `par2` in
any image (ADR-0017; `images/distroless/stage.sh` refuses all three;
`TestTheNativeImageCarriesNoMediaExecutable`†). A TranscodeProfile
names no encoder setting: `quality`, `container` (ignored since
`standard.Version` 2), `audio.languages`, `subtitles.languages` (hashed
only when set, so a profile keeping every subtitle kept its hash),
`policy.keepCodecs` (hashed as a set
without `hevc`, which is always kept), `policy.maxVideoBitrateKbps`
(hashed only when set), `policy.minDuration` and
`policy.maxOutputToSourcePercent` are the standard's inputs, and with
`standard.Version` the only things `status.hash` covers.
**`policy.maxVideoBitrateKbps`** (owner, 2026-10-27, 6000 on `hevc-mkv`:
"why remux Blu-ray 1080p? we should transcode it HEVC 8-bit") encodes an
HEVC source whose video runs over it, normalised to 1920x1080 by pixel
count (`standard.NormalisedKbps`: 20 Mbps of 2160p is 5 Mbps of 1080p),
where a compliant one is copied -- a release group's Blu-ray x265 at 8-20
Mbps (273 untranscoded, 739 GiB; 95 over 6 Mbps) becomes the standard's
HEVC, 8-bit for SDR at 1080p or less, where clustarr's own encodes land
near 1.1 Mbps. Every output now carries `CLUSTARR_VIDEO=copy|encode`
(`standard.VideoTagKey`) beside `CLUSTARR_PROFILE`: a copied video is the
release's and is the one part of a transcode that is not final -- a remux
over the limit is encoded once (`transcodeplan.RemuxOverLimit`, as
`KeptH264Dropped`; 110 remuxed Blu-ray HEVC files, 462 GiB), a transcode
clustarr encoded never is (61 of its 4,060 encodes run over 6 Mbps on
grainy sources). The planner reads the recorded plan's `videoAction` for
outputs written before the tag, and a tagged file without the tag reads
as copied to the standard; one with no recorded transcode at all reads as
copied to the planner too (owner, 2026-11-02: My Happy Marriage's 14
remuxes at 31 Mbps, tagged before plans were recorded, were never planned
again), and only a recorded encode is final, the worker's probe of the
file's own tag still skipping an encode it finds; a source another tool
transcoded (`TranscodedElsewhere`) is still remuxed, never encoded.
**A release's 10-bit HEVC is never copied** (owner, 2026-11-02,
`standard.Version` 9: "When media does not match the correct audio, but it
*does* have HEVC x265 10 bit, we should perform the same encode/decode
process as any other format - *not* a remux", and for a file whose audio
already fits too): it is encoded as any source is, 8-bit for SDR at 1080p
or less, else Main 10, HDR included, decoded on the Intel pods' VA-API
(`hevc:10` in their trial), so the one HEVC a release keeps is 8-bit at the
8-bit target under the bitrate limit. An encode's 10-bit HEVC, clustarr's
own (tagged) or another tool's, is still only remuxed. clustarr's earlier
remuxes of 10-bit releases are encoded once (owner, same day;
`transcodeplan.TenBitRemux`, `standard.copiedTenBit`), as
`RemuxOverLimit` does, but only on evidence the video was copied: a
recorded copy or `CLUSTARR_VIDEO=copy`, or with neither, SDR at 1080p or
less, where clustarr's own encodes have been 8-bit since the first. An
unrecorded HDR or above-1080p one may be clustarr's own Main 10 encode and
stays.
The codecs off limits are those no GPU here decodes, H.264 10-bit and
MPEG-4 Part 2 (XviD, DivX; MS-MPEG-4 with it): clustarr's own catalogue
formats `h264-10bit` (`pkg/quality/catalogue/data/formats/clustarr.json`,
a 10bit or Hi10P title naming no x265, HEVC, AV1 or VP9) and `mpeg4-part2`
(an XviD or DivX title) score -10000 by default, so every profile refuses
them, dual audio or not (owner), where x265 10-bit is taken. A file of
either does not meet the cutoff (owner, 2026-11-03: "These files shouldn't
meet the cutoff - or should be upgraded", then "The 12 XviD files and 18
H.264 10-bit files should be under the cutoff - forced to search for a new
release"; `MediaFile.UndecodableVideo`, CutoffMet reason
`UndecodableVideo`): the transcode skips it, since a GPU transcode never
decodes on the CPU (below), and a small one could not transcode under the
size limit anyway (Star Trek: Discovery's 480p mSD episodes came to
100-109%), so a release of any quality the profile takes replaces it past
the transcoded, baseline and upgrade rules, as for a file off the bitrate
range (`decision.Current.Undecodable`, `grabplan.StillWants`,
`importplan.undecodableRefit`), and import refuses an incoming file of
either as a releaseFault (`importplan.undecodableRejection`, from the
inspect's `ProbeSummary.VideoCodec` and `VideoBitDepth`), a person's pick
exempt. A season of such files, or of files missing a wanted audio
language (`MissingAudio`), is searched as a pack (owner, 2026-11-03: the
same for "anime episodes/series/seasons that are missing dual audio when a
dub (configured language) is available"): an Episode below the cutoff for
either reason whose own search is due replaces as a pack
(`search.replacesAsPack`), and a season with two or more such, no live
grab covering them and none holding the season, is asked for by the
automatic season search though every episode holds a file
(`SeasonFacts.ReplaceDue`), those episodes waiting for it rather than
searching the release index alone (`View.SeasonReplaces`), so the season's
answer is each one's search and their backoffs move together.
AV1 decodes on the Intel GPU too (2026-11-03, ffgo `v0.0.0-clustarr.16`:
a GPU decode opens FFmpeg's own `av1` decoder, where
`avcodec_find_decoder` answered `libdav1d`, a CPU decoder that ignores the
device, so the pods' trial had measured AV1 as undecodable on an iHD
driver that decodes it at 48x), and the transcode-first profiles take it
(`av1` 0); TRaSH's built-ins still score AV1 -10000.
`policy.neverTranscodeModifiers` is gone (owner, 2026-10-27, "we can
ignore the remux modifier"): a remux's or BR-disk's modifier skipped the
whole file, 40 of them with DTS or TrueHD audio, and the standard now
decides on the file's streams alone, so under `keepCodecs: [hevc]` an
H.264 remux is encoded like any H.264 file; `MediaInfo.Modifier` and
`task.SourceModifier` went with it, and the field must leave a profile's
Terraform values with the CRD. The language lists
(2026-10-15, the owner's English-plus-original audio and English
subtitles) compare codes as languages (`pkg/lang`: "en", "eng", TMDB's
"en"), and `original` is the movie's or series' original language, which
the transcode adapter's Gather reads (`readItem`, an episode's through its
series) and the task carries to the worker (`task.OriginalLanguage`,
`transcode.MediaInfo.OriginalLanguage`) so both plan alike; an unknown
original keeps every audio track. Untagged tracks stay, commentary only in
a kept language, and an image subtitle in a dropped language holds no
file for OCR. A dropped track is gone once the output replaces the
source. The size limit
holds an encode only: a remux adds the AAC 2.0 companion, so it is a little
larger by design (`worker.outputTooLarge`), and an encode of an AV1
source may run to 150% (`worker.AV1LimitPercent`, owner, 2026-11-03): the
standard's HEVC of a lean AV1 encode lands at 100-105% of it (Attack on
Titan's fourth season failed every attempt at 100), and is made for
playability, since Plex here transcodes no video. **An encode made only
to save space that saves none keeps its video** (owner, 2026-11-15:
`standard.KeepVideo`): when the encode of 8-bit SDR H.264 -- what
`policy.keepCodecs: [h264]` would copy -- comes out over the limit, the
worker removes it and, in the same attempt, remuxes the file into the MP4
layout with the source's video copied and tagged `CLUSTARR_VIDEO=keep`
(`standard.VideoKept`), reporting `task.Result.KeptVideo`, which the loop
records as `status.transcode.plan` (`videoAction: keep`, mode remuxOnly,
the message naming the encode's percent). A kept video is final like an
encode: `standard.Plan` and `transcodeplan.KeptH264Dropped` never encode
it again. The probe reads the tag too (`MediaInfo.TranscodeVideo`, only
copy, encode or keep kept), and the planner takes it over the record
(`transcodeplan.VideoActionOf`, for every copied-video rule), and a swap's
incorporation rewrites a record it contradicts: the first file kept on
talos (2026-11-16, Spare Me S01E11) was swapped by the loop's FileMissing
recovery before the worker's answer landed, recorded as the dispatched
encode, and dispatched again at once. `ProbeVersion` was not raised: only
outputs written since carry `keep`, each probed at its swap. On talos 82 such encodes had failed, lean 1 Mbps streaming
WEB-DLs and grainy sources whose fixed-quality hevc_qsv encode ran 100-174%,
each left Failed in its original Matroska. Every other encode over the
limit -- a release's 10-bit HEVC, AV1, Hi10P or HDR H.264 -- is made for
playability and still fails. The files that failed before it stay blocked
for their hashes until retried. A file is planned
again at dispatch whenever its plan would differ (a profile edit, another
class, new limits). **A new hash
re-transcodes nothing**: the transcode planner (`app/transcode/transcodeplan`)
skips a file that is `MediaFile.Transcoded()` or whose `status.transcode` is
already open for the profile, a verdict (Skipped or Failed) holds only for its
profile hash and probe hash, and the standard skips any
`CLUSTARR_PROFILE`-tagged MP4 and never encodes a tagged file. `transcode.clustarr.io/exclude: "true"` on a MediaFile, its Movie or Episode, or an Episode's Series keeps the file out of every transcode whatever the hashes (owner, 2026-10-22, Mister Rogers' Neighborhood): it is never eligible, so it takes no window place, and a running attempt is withdrawn as Skipped, reason `Excluded`. On an item it takes effect as each file is next planned. Suspend only holds a file in its window place, and a cancel lasts only while the profile and probe hashes do. A plan
reads as `TranscodePlan.Describe` ("remux to MP4, video copied", "encode
with hevc_qsv") in the dispatch and running messages and on the pipeline
page, never as FFmpeg's `copy`. Raise
`standard.Version` when the standard's output changes, so untranscoded
files plan under it. NVENC holds an encode under the source: with the
probe summary's `videoBitrateKbps` known it runs VBR at `cq` = quality - 1
capped at `standard.NVENCMaxBitratePercent` (70) of it, else constant QP
(2026-10-05; constant QP alone made lean sources larger than
`policy.maxOutputToSourcePercent` allows). The NVENC tier decodes on NVDEC, frames staying on
the GPU, for a source each pool worker's trial decodes (published as
`clustarr-progress` `encoder-limits.<class>` `nvdec`, shown in
TranscodeProfile `status.encoderLimits[].nvdec`) or else a static Turing
list say NVDEC decodes. The
QSV tier likewise decodes on the GPU, through VA-API on the pod's render
node, its frames mapped to QSV (`hwmap=derive_device=qsv`) for `vpp_qsv` and
`hevc_qsv` (`engine.Options.DecodeDevice`), but only a format its pod's
start-up trial measured (`transcode.VAAPIDecodes`, published as
`encoder-limits.<class>` `vaapi`; no static list), and encodes at preset
`medium` (2026-10-16, `hack/presetbench` on the NUC: decoding on the GPU made
`veryslow` 4-6x faster, and `medium` halved that again at 1.8-5.4% more bytes
and the same VMAF); the VAAPI tier decodes on its encoder's own device
(`scale_vaapi` into `hevc_vaapi`). **A GPU transcode decodes on the GPU or
not at all** (owner, 2026-11-03: "Ensure that the transcode pods are
*actually* using hardware decoding an encoding - never falling back to
cpu"): a source the tier's GPU does not decode is Skipped ("the qsv tier's
GPU does not decode mpeg4 8-bit, and a GPU transcode never decodes on the
CPU"; `standard.gpuUndecodable`) where it was decoded in software and
uploaded -- on talos the XviD episodes of Seinfeld and The Simpsons, the
H.264 10-bit of Star Trek: Discovery S05 (both of which the
`UndecodableVideo` cutoff replaces) and any 4:2:2 stream, which no key covers -- neither QSV nor VA-API
decodes the first two on any Intel GPU (no `mpeg4_qsv` exists; VA-API: "No
support for codec mpeg4 profile 15"; `h264_qsv` on High 10: "Decode error
rate 1", talos-88, 2026-11-03); 4:4:4 has its own key (`hevc:10:444`), which
the trial measures, since the Arrow Lake decodes it at 9x; a
GPU tier whose decode device does not open, or whose trial decodes no
sample format, fails its measurement (`measureTier`), so the pod reports
unhealthy rather than decode on the CPU; the worker refuses a GPU plan
that decodes on the CPU (`ffgoJob`), and the engine fails a GPU decode at
its first frame in system memory, FFmpeg's silent fallback when the GPU's
decoder refuses the stream (`engine/video.go`). Measured on talos-88 the
same day through the processes' DRM fdinfo (`talosctl read
/proc/<pid>/fdinfo/<fd>`): each Ted Lasso encode held the Arrow Lake's
video engine 97% busy and video-enhance 35%, at about one CPU core. A
pool pod measures its own device at start through ffgo
(`inprocess.Engine.Measure`), takes no work while it is unusable, and
publishes `healthy` beside its limits; no work goes to a GPU class whose
every fresh report is unhealthy. A class plans with the tightest of its
pods' fresh reports (`task.EncoderLimitsFresh`, 10 minutes), and a pod
withdraws its own when Serve returns (`task.WithdrawEncoderLimits`, owner,
2026-11-03): after a pool's image change the old pods' reports had
narrowed the new pods' class for those ten minutes, so three of Attack on
Titan's AV1 episodes decoded on the CPU beside pods that decode AV1, and
under the rule above such a gap would skip the file for its hashes. A pod
killed outright still counts until its report goes stale. That engine lives in
`app/transcode/worker/inprocess`, imported only by `cmd/transcode`; `cmd/manager`
and `cmd/ui` must never link ffgo or purego, which make a dynamic binary the
distroless `clustarr` image cannot start (`TestManagerNeverLinksADynamicLoader`†,
`TestUINeverLinksADynamicLoader`†). Every binary that links ffgo
passes `pkg/ffruntime` (FFmpeg 9, the shim's API, one log trampoline, calls
bounded by their context and a grace, a panic in one recovered as its error,
`ffruntime.ErrPanicked`, counted in `clustarr_ffgo_panics_total`: `Do` runs
the call on a goroutine of its own, beyond the bus's handler recover, so on
2026-11-11 ffgo's 4 KiB tag bug killed the import agent at every delivery of
the music scan).
`hardware: gpu` (2026-10-01) is `auto` without the CPU overflow: a job
takes NVIDIA, then Intel, and waits Planned ("waiting for a free GPU slot")
when both are busy, rather than encode with libx265; it goes to cpu only
when no GPU class is usable at all or a GPU already refused it
(`fallbackReason`), a remux still takes a cpu slot, and a reroute off an
unschedulable or unusable GPU pool leaves it no `fallbackReason`
(`admission.ChooseClass`'s `gpuOnly`, `app/transcode/admission`, the
`transcode-admission` controller's since ADR-0016).
How this cluster's GPU nodes are set up -- the NVIDIA device plugin and
runtime class, NFD with Intel's rules and Intel's GPU plugin -- is
`docs/gpu-nodes.md`.
**Plex's Live TV conversions take the Intel GPU's video engines first**
(owner, 2026-10-26, cluster-plex's gpu-video-transcode-pool design §6):
cluster-plex counts the conversions its GPU pool runs in the Lease
`cluster-plex-video-transcodes` (`clusterplex.io/active-video-transcodes`,
renewed every 10 s, stale after 30 s), and while the count is above zero
admission (`app/transcode/admission/videolease.go`) admits new files to
the intel class only on the video engines the streams leave whole
(`VideoAllowance`, owner 2026-11-05: "maximum transcode streams without
throttling the streams"; and since 2026-11-10 the same pass publishes the
intel class's work in hand and waiting -- running, queued and Planned intel
files and the yield's held gpu files, `PassResult.IntelWork` -- into the
Lease `--intel-work-lease` names in the manager's own namespace (chart
`manager.intelWorkLease`, annotation `clustarr.io/intel-work`, renewed
every 10 s while above zero by the leader-only `admission.IntelWorkLease`),
which cluster-plex's manager reads to send Plex's conversions to its
libx264 pool while clustarr holds intel work, so clustarr keeps both
engines: cluster-plex's `2026-11-10-cpu-video-route-design.md`): each pool pod measures its device's engine count
from the kernel's DRM fdinfo (`pkg/transcode/drminfo`, `Limits.VideoEngines`,
shown in TranscodeProfile `status.encoderLimits[].videoEngines`), a
stream is reserved a quarter of an engine (measured 20% of one on
talos-88's Arc 140T for a 1080p H.264 conversion, in bursts), the
streams' engines are reserved whole, and every engine left takes one
encode (one held an engine 97% busy on 2026-11-03), so on the 140T's two
engines one encode runs beside one to four streams and none beside five
or more; an unmeasured count is one engine, which yields the whole device
as the rule did until then (all evening on 2026-11-04 while one Apple TV
watched Election Night: 32 Planned files read "waiting: Plex is converting
video on the GPU" with 90% of the GPU idle). Running encodes always
finish. The wait reason names the stream and encode counts when an
allowance exists. **The 140T's throughput saturates at two encodes**
(owner, 2026-11-05, "which concurrency is fastest": a benchmark pod on
the NUC ran the standard's QSV pipeline as N parallel ffmpeg jobs on one
1.5 GB 1080p H.264 episode with the pool held at zero slots): 22.3, 29.4,
30.1, 30.2 and 29.9 MB/s of source at 1, 2, 3, 4 and 6 jobs (21x, 14x,
9.5x, 7x, 4.7x real time each; 1.4 CPU cores in all), because the i915
driver already spreads one hevc_qsv encode across both video engines
(149% of their 200%) and two fill them (195%). So `--slots intel=2` is
the throughput ceiling (about 28 such episodes an hour), three adds
noise, and the "one engine per encode" reservation above is a model of
what to leave a stream, not how the hardware schedules: the engines are
time-sliced among contexts, and two encodes beside a stream would give it
about a third of an engine against the 20% it needs, which is unproven
for a real-time stream and is the test to run before relaxing the
allowance.
**There is no CUDA image** (2026-10-01, `docs/adr/0015-no-cuda-image.md`):
every pool, nvidia's included, runs the one image `--native-image` names
(chart `image.native`, built `FROM scratch` by `images/Dockerfile.native`, which
also carries the agent and markers).
FFmpeg's libraries `dlopen` `libcuda`, `libnvidia-encode`
and `libnvcuvid`, which the NVIDIA container runtime injects from the host
into a pod with `runtimeClassName: nvidia`, an `nvidia.com/gpu` request and
`NVIDIA_DRIVER_CAPABILITIES=video,compute,utility` -- all three set by
`pool.applyHardware` -- and its CUDA filters are clang-built PTX, so no CUDA
toolkit or runtime is linked. The `transcoder-cuda` images (an
`nvidia/cuda` base, later two `ENV` lines on `transcoder`),
`--worker-image-cuda` and `image.transcoderCuda` were removed: nothing in
them was loaded, and their `NVIDIA_VISIBLE_DEVICES=all` handed every GPU to
a pod that ran the image without requesting one. The schema now rejects
`image.transcoderCuda`; `--set image.transcoderCuda=null` does not remove
it from a release's values, so upgrade such a release once from
`helm get values -o yaml` with the key deleted, passed with `-f`, not
`--reuse-values`. Do not add a CUDA image, or set
`NVIDIA_VISIBLE_DEVICES` in any image, without reopening the ADR;
`TestNativeImageIsWhatTheManagerStampsOntoPoolsAndEngines`† is to fail on either.
A RootFolder's `naming.renameTranscoded` renames a file
the transcode controller transcoded (`status.transcode.profileTag`) to its canonical file
name in the folder it is already in (the remediation loop's rename actuator,
`app/remediation/rename`, through `specwrite.RenameFile`'s keepFolder), so
a season is never split between "Season 3" and "Season 03"; `renameFiles`
renames every file its own way and wins when both are on.

**A series is kept in one shape of picture (owner, 2026-11-17: "we should
ensure a season has a consistent aspect ratio ... attempt to always
download widescreen unless the original aspect ratio was 4:3").**
`Series.spec.aspectRatio` is `widescreen` (the default), `fullscreen` (4:3)
or `any`, and `spec.seasonAspectRatios` gives a season its own
(`Series.WantedAspect`); neither TMDB nor TheTVDB says what a series was
made in, so it is a setting (the Edit dialog's Aspect Ratio; seasons by the
spec). A picture is fullscreen under 3:2 by its display aspect ratio --
frame times sample aspect, `MediaInfo.displayAspectMilli`, which the probe
records since `ProbeVersion` 5 (a DVD's 720x480 is 4:3 or 16:9 by its
pixels alone; 775 files on talos are that frame). It reads the frame: bars
drawn into a wider frame are the picture, so a 4:3 scene pillarboxed into
16:9 is widescreen. A release's shape is in no name, so nothing is refused
before it is downloaded:

- an Episode whose file is the wrong shape does not meet the cutoff, a
  transcode included (`cutoffReason` `WrongAspectRatio`, phase
  `CutoffUnmet`), so the wanted sweep searches it; the item wakes when a
  probe first reads the shape (`FileSignature.Aspect`) and when the
  series' setting moves;
- search and grab let any release the profile takes replace it
  (`decision.Current.WrongAspect`, `grabplan.StillWants`), for at most
  `grabplan.MaxAspectTries` (3) releases: each try is a download, and once
  that many of the releases covering the episode carry an import block the
  file is judged as any other until the blocks lapse (90 days);
- import (`importplan/aspect.go`) takes whatever came for an episode with
  no file, refuses a file of the wrong shape that would replace one as a
  releaseFault (blocklisted, the next searched), and lets a file of the
  wanted shape replace one that is not past the transcoded rule and the
  upgrade table (`aspectRefit`); a person's pick is exempt, and a file of
  unknown shape is judged by none of it.

Movies have no such setting. On 2026-11-17 talos held 1,054 4:3 episode
files across 16 series, 20 seasons mixing the two shapes (Seinfeld S9: 20
of the cropped 16:9 remaster, 4 in 4:3).

**A series can crop instead of replacing (owner, 2026-11-17: "for some
shows, we should prefer the high quality and crop ourselves during
transcode"; asked: fill the wanted shape, and download a wrong-shaped
transcode again rather than encode it twice).** `Series.spec.aspectRatioFix`
is `replace` (the default, the rule above) or `crop` (the Edit dialog's "A
File of Another Shape"). Under `crop` the best release is kept whatever its
shape and its one transcode cuts it to the wanted shape, centred, as a
remaster does: a 4:3 picture loses its top and bottom for 16:9
(1440x1080 becomes 1440x810), a 16:9 one its sides for 4:3
(`standard.cropRect`, from the display aspect ratio, so a DVD frame is cut
by its picture; sides and offsets even; the two ratios exact, not by
thousandths). The pieces:

- the target is no fact of the file: `Series.CropTarget` (0 unless the
  series crops and the file is the other shape), read by the adapter
  (`itemread.CropTarget`) into `transcodeplan.Inputs.CropAspectMilli`, and
  carried in the task (`Task.CropAspectMilli`) so the worker's plan from its
  live probe is the manager's;
- a crop makes an encode of what would be skipped or copied (compliant
  HEVC, kept H.264); the plan's `Video.Crop` is in its hash, the output is
  held to the rectangle's size (`Expectation.Width/Height`, checked by
  `engine.Verify`), and a cropped encode has no kept-video form
  (`KeepVideo`): one that comes out no smaller fails as any other;
- the crop is cut on **the CPU tier (`crop=` before `format=`) and QSV
  (`vpp_qsv`'s `cw/ch/cx/cy`, both decode paths)** only. On NVENC and VAAPI
  the file is skipped, never encoded whole: an uncropped transcode would be
  a final file of the wrong shape, searched for again and encoded the same
  way for ever;
- a transcode is still never encoded twice. A wrong-shaped file that is
  already a transcode reads `WrongAspectRatio` under `crop` too and is
  searched for anew; one that is not a transcode yet is not wrong (the
  Episode reconciler), so nothing is searched while it waits for its
  encode;
- import takes a release of either shape in place of a wrong-shaped
  transcode, refuses only a wrong-shaped file that is a transcode itself,
  and does not count a held, not-yet-transcoded file as wrong
  (`aspectRefit`, `Input.CropsAspect`).

Gaps, known: a file that was Skipped or Failed before the series was set
to crop keeps that verdict (it holds for its profile and probe hash) until
the retry annotation; a file with no transcode profile is never cropped
and never searched for; and the QSV crop was run on no hardware before its
first use on talos (the development machine opens no QSV device; the CPU
crop is run by the engine's tests).

