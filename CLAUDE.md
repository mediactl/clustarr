# Clustarr

A distributed, event-driven media stack for Kubernetes: controllers and CRDs that
manage a media collection end to end, from wanting something through finding,
downloading, importing, transcoding and subtitling it.

Custom resources are the interface. `kubectl get movies,series,mediafiles` is
the UI -- an item's grabs are its `status.downloads[]` (`--field-selector
status.downloadPhase!=` is the download queue), a file's transcode and subtitles
its MediaFile's status -- and the web UI is a view over the same resources.

## Read before designing anything

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` — the design of record.
  Field-level CRD definitions, event subjects, data flows, milestones.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` — adds
  `import`, observability and the UI. **Where the two disagree, the amendment
  wins.**
- The unify branch's designs (2026-10-06/07), each with an "as built" record of
  where the code moved: `2026-10-06-manager-agent-split-design.md` (ADR-0018,
  ADR-0017), `2026-10-06-mediafile-remediation-loop-design.md` (ADR-0016),
  `2026-10-07-agents-report-over-nats-design.md` (ADR-0019) and
  `2026-10-07-unbranded-names-design.md` (Wave U; its §6 maps every old name to
  its domain name). The design of record and the amendment carry dated "as built
  on the unify branch" notes where these made them false.
- `docs/adr/` — why NATS, why GPL-3.0, why worker pools for transcode, why one
  manager, and so on. `docs/adr/README.md` is the index and the supersede
  lifecycle.
- `docs/research/*.md` — nine verified research notes (TRaSH quality model,
  Prowlarr/Cardigann, queue comparison, torrent/usenet, controller-runtime,
  ffmpeg, Bazarr, metadata providers, naming). Long: `grep -n '^#'` first, then
  read the section. They contain verified API shapes — prefer them over guessing.

The spec is authoritative for names, fields and behaviour. Do not invent fields;
do not drop them.

## Services

One Go module, five binaries (ADR-0018,
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`):

| Binary | Image | Runs |
| --- | --- | --- |
| `cmd/manager` (`internal/cli/manager`) | `clustarr` (static, distroless) | the control plane (ADR-0019): every reconciler and leader-only runnable of the six services, one lease (`manager.clustarr.io`) and one cache, registered by `app/<svc>/manager`; the MediaFile remediation loop with its file and item keys (`app/remediation`, ADR-0016) and transcode admission; task admission and routing (`app/dispatch`) and the intake of the agents' candidates, scan observations and advisories (`app/intake`); ensures the NATS topology; serves `external.metrics.k8s.io` and owns the agents' HPAs (`app/autoscale`); the object-store maintenance loop (`app/maintenance`) |
| `cmd/agent --domain <d>` (`internal/cli/agent`) | `native` (cgo) | one domain per agent process, never leader-elected, registered by `app/<svc>/agent`, read-only to Kubernetes (`k8s.ReadOnly`): `catalog` (search answers, overlay renders), `events` (RSS matcher), `metadata` (gateway, TheIntroDB markers, artwork fetch), `import` (scan observations, inspect and execute imports, list snapshots, recycle sweep, probe tasks), `index` (release index and its blocklist, search/download/query/blocklist RPC, RSS poll, Torznab facade), `caption` (subtitle fetch, embedded extraction), `torrent-engine` and `usenet-engine` (rendered by the manager's DownloadClient controller: engine commands in, transfer and engine records out) |
| `cmd/ui` (`internal/cli/ui`) | `clustarr` | the web UI and the Plex provider |
| `cmd/markers` | `native` | segment detection (`app/segments/worker`) on ffgo and ONNX Runtime |
| `cmd/transcode` | `native` | transcode pool and graft Jobs (`app/transcode/worker`) on ffgo |

Both images UPX-pack every Go binary; `native` is FROM scratch with FFmpeg 9's
shared libraries, the ffgo shim and ONNX Runtime, and neither ships `ffmpeg`,
`ffprobe` or `par2` (ADR-0017). `make run-dev` runs the manager, one agent per
domain and the ui as local processes; there is no `clustarr all`.

| Dir | Owns |
| --- | --- |
| `app/catalog/` | Media domain (movies, series, music, books, comics, audiobooks), metadata gateway, release decisions, artwork render (agent `catalog`) |
| `app/import/` | Everything entering the library: root-folder rescan, import lists, completed-download import |
| `app/indexer/` | Indexer aggregation, Cardigann engine, local release index (SQLite FTS5) |
| `app/grab/` | Download clients: torrent (anacrolix) and usenet engines |
| `app/transcode/` | Transcode to HEVC (the MP4 standard) on worker-pool and graft Jobs; admission (`app/transcode/admission`) |
| `app/caption/` | Subtitles, Bazarr-equivalent, distributed |
| `app/remediation/` | The MediaFile remediation loop (ADR-0016): file and item keys, the planners' adapters, records incorporation, the item stages (metadata, artwork, search, grab, downloads) |
| `app/dispatch/`, `app/intake/` | Task admission and routing; the intake of grab candidates, scan observations and task advisories (ADR-0019) |
| `app/autoscale/`, `app/maintenance/` | The agents' HPAs and the External Metrics API (ADR-0018); the object-store maintenance loop (ADR-0020) |
| `app/segments/` | markers' segment analysis |
| `app/livetv/` | Live TV: the IPTVProvider and IPTVChannel controllers and the HDHomeRun tuner, in the manager |
| `ui/` | Server-rendered web UI (templ + htmx + SSE); serves artwork at `/art` and the Plex Metadata Provider roots over a read-only bus; never writes status |

API groups: `{catalog,index,download,transcode,subtitle}.clustarr.io/v1alpha1`,
and `clustarr.io/v1alpha1` for Live TV.
Module `github.com/mediactl/clustarr`. Licence GPL-3.0 (lets us port *arr and
Bazarr logic verbatim — keep the header on every file).

**Names.** The domain names -- `catalog`, `import`, `index`, `grab`,
`transcode`, `caption`, `markers` -- are the only internal names: Go
identifiers, field managers, streams, subjects, durables, RPC subjects,
labels, Secrets and leases. The per-service *arr brands were swapped out
cold turkey on 2026-10-07 (Wave U), with no migration of stored names and
no code that reads an old one; `make brand-gate` (`hack/brand-gate.sh`;
`TestNoBrandNameOutsideTheRecords`†) fails on any brand outside the dated records
(`docs/superpowers`, `docs/research`, the ADR bodies, the cold-cutover runbook and
the chart README's "Upgrading from 0.4.x"). `clustarr` itself is the product
name and stays.

## Transcoding

A transcoded media file should be the final destination. If we detect a transcoded profile, we should not mark the media "CutoffUnmet" - instead it should be marked `Transcoded`.

The predicate is `app/catalog/controller/rollup.Transcoded` -- `spec.original` false, or the probe's `status.mediaInfo.transcodeProfile` (the `CLUSTARR_PROFILE` tag), or a transcode the transcode controller did not make: `status.mediaInfo.videoEncoder` naming an ffmpeg HEVC or AV1 encoder (`commonv1.MediaInfo.TranscodedElsewhere`; Tdarr's `Lavc… hevc_qsv` on the owner's library, 2026-09-29) -- and such a Movie or Episode reads phase `Transcoded`, with `CutoffMet=True` reason `Transcoded` (the one exception: a file missing an audio language a non-grafting profile wants, `MissingAudio` under UI below); `pkg/decision` rejects every release over it as `TranscodedFinal` (spec §4.2) -- a user-invoked search's too, since "Search now" grabs its best automatically, so only a person's own pick (Search `spec.grab`, exempt in the Search controller's `resolveGrab`) replaces it; an import refused for it reads `importplan.MessageExistingFileFinal`, which never blocklists (2026-10-07: a refused 21 GB grab was blocklisted and its file deleted) -- and transcode never encodes it again: a file another tool transcoded is
still remuxed into the MP4 layout when its video is compliant, and skipped
rather than encoded a second time otherwise (2026-10-15;
`transcodeplan.AlreadyTranscoded` counts only transcode's own output as
done), and so is transcode's own from before the MP4 layout -- a
`CLUSTARR_PROFILE`-tagged Matroska file, `standard.Version` 4 -- whose tag
keeps the standard from encoding it again. A good transcode baseline is kept as well (2026-10-16, after an afternoon's upgrades swapped compact HEVC and HDR files for 14-22 GB x264 Blu-rays): `pkg/decision`'s `baselineRejections` refuses an HDR file's replacement by a release that names no HDR (`LosesHDR`) and an HEVC or AV1 file's by an AVC release of no greater resolution (`AVCOverHEVC`), both from the probe (`decision.Current.VideoCodec`/`HDR`), a person's pick exempt as from `TranscodedFinal`; a Dolby Vision release is never taken (`DolbyVision`, `spec.override` only); the video built-ins score TRaSH's x265 (HD) 0, not -10000 (`quality.clustarrFormatScores`); and an upgrade must beat the current file's frozen formats as the profile scores them today (`quality.Profile.ScoreFormats`), not the score frozen at import. Import holds the same line from both files' probes (`decision.BaselineLoss`, in `importplan`'s gate), and refuses a movie file whose own streams say it is not the movie (`importplan.movieContentRejection`, a releaseFault, calibrated on the owner's 839 movie files): a Dolby Vision probe, a runtime under 65% of the movie's (one broadcast part), or one more than 15% and 10 minutes off when the release title does not name the movie (`decision.TitleNamesMovie`; an id query's vouch took No.Escape.1994 as The NeverEnding Story III on its year alone). A movie released in parts within one download -- two or more files numbered Part 1 to n (`importplan.PartNumber`: Part, pt, CD, Disc) whose probes agree on codec, size and HDR -- imports as one file, the parts joined losslessly through FFmpeg's concat demuxer (`importplan.foldParts`, `schema.ImportMove.Join`, `engine.Join`; 2026-10-16: 28, 42 and 49 Up each imported as half the film). A movie posted only as separate parts (56 Up's Part1-3, No Direction Home's Part.1-2; `docs/superpowers/specs/2026-10-16-movie-part-sets-design.md`) is one search result per indexer: the search agent adds each set of one indexer's posts of parts 1 to N (`release.PartOf`, `decision.PartSets`: same parsed title, year, quality, group and edition; GUID `parts:...`, `ReleaseInfo.Parts`), judged as the film with its parts' rejections; its grab is an entry per part (`status.downloads[].set`), never delayed or held; part 1 imports them all once every part is done (`lifecycle.SetReady`, `ImportInspectTask.Parts`, each file tagged with its part, `InspectedFile.Part`, so an obfuscated name joins), and the others follow it; a set import must join every part, any joined movie must run `importplan.JoinedShortPercent` (85%) of the film, and a part that fails, or a failed import, removes the rest. An execute's long steps (a join, a copy, a donor's reduction) ack their delivery in progress while they run (`fileimport` `keepAlive`), each join writes its own temporary file, and `engine.Join` fails a join that ran short of its parts (FFmpeg's concat demuxer ends quietly at a part it cannot open): on 2026-10-17 four of five joins outlived the consumer's 1-minute deadline, ran a second time beside the first, and 49 Up was placed as its first part. A new file under its current MediaFile's name replaces the file a transcode renamed away (recycled; the MediaFile re-pointed, never deleted), which 28, 42 and 49 Up left beside the film, and another MediaFile at the path the new file takes is deleted, so a path never has two. A probe that learns a new field raises `mediainfo.ProbeVersion`, which re-probes every file once through `status.probeVersion` -- never by changing `probeHash`, which caption and transcode read as the file's identity. Probing is `pkg/mediainfo/native` in the import agent (ADR-0017; it prints what ffprobe printed, so `ProbeVersion` stays 3): the remediation loop's probe planner (`app/remediation/probe`) asks for a probe on `CLUSTARR_WORK_PROBE` and incorporates the `clustarr-probes` record, and a `ProbeVersion` raise re-probes through the low lane.

Backlogs are not an object per file (2026-09-30,
`docs/superpowers/specs/2026-09-30-backlog-windowing-and-encoder-limits-design.md`):
a TranscodeProfile admits at most `--job-window` (32) of its files at once
into a window phase of `status.transcode` (Pending, Planned, Queued, Running,
Swapping), the next matching files by name, and counts the rest in
`status.waitingFiles` (`clustarr_transcode_backlog_files`); Succeeded retention
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
a larger source encodes beside its output as before; attachments dropped, chapters kept; a file
with an image subtitle no text subtitle covers is `status.transcode` Skipped,
`standard.HoldImageSubtitles`, until phase 2's OCR. Since `standard.Version`
6 (owner, 2026-10-20), a text subtitle of the same language covers an image
one, provided it is forced only if the image one is
(`standard.textCovers`), and the covered image subtitle is dropped. Akira
had held for its English DVD track beside three English ASS ones; about
125 of the 458 held files (106 movies, 19 episodes) had such cover. An
untagged image track still holds.) and
`pkg/transcode/engine` runs it on FFmpeg 9's libraries through the ffgo
fork (`github.com/mediactl/ffgo`, tags `v0.0.0-clustarr.N`). There is no
argv engine, no `--worker-engine`, and no `ffmpeg`, `ffprobe` or `par2` in
any image (ADR-0017; `images/distroless/stage.sh` refuses all three;
`TestTheNativeImageCarriesNoMediaExecutable`†). A TranscodeProfile
names no encoder setting: `quality`, `container` (ignored since
`standard.Version` 2), `audio.languages`, `subtitles.languages` (hashed
only when set, so a profile keeping every subtitle kept its hash),
`policy.neverTranscodeModifiers`, `policy.keepCodecs` (hashed as a set
without `hevc`, which is always kept), `policy.minDuration` and
`policy.maxOutputToSourcePercent` are the standard's inputs, and with
`standard.Version` the only things `status.hash` covers. The language lists
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
larger by design (`worker.outputTooLarge`). A file is planned
again at dispatch whenever its plan would differ (a profile edit, another
class, new limits). **A new hash
re-transcodes nothing**: the transcode planner (`app/transcode/transcodeplan`)
skips a file that is `MediaFile.Transcoded()` or whose `status.transcode` is
already open for the profile, a verdict (Skipped or Failed) holds only for its
profile hash and probe hash, and the standard skips any
`CLUSTARR_PROFILE`-tagged MP4 and never encodes a tagged file. A plan
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
list say NVDEC decodes; anything else decodes on the CPU and uploads. The
QSV tier likewise decodes on the GPU, through VA-API on the pod's render
node, its frames mapped to QSV (`hwmap=derive_device=qsv`) for `vpp_qsv` and
`hevc_qsv` (`engine.Options.DecodeDevice`), but only a format its pod's
start-up trial measured (`transcode.VAAPIDecodes`, published as
`encoder-limits.<class>` `vaapi`; no static list), and encodes at preset
`medium` (2026-10-16, `hack/presetbench` on the NUC: decoding on the GPU made
`veryslow` 4-6x faster, and `medium` halved that again at 1.8-5.4% more bytes
and the same VMAF). A
pool pod measures its own device at start through ffgo
(`inprocess.Engine.Measure`), takes no work while it is unusable, and
publishes `healthy` beside its limits; no work goes to a GPU class whose
every fresh report is unhealthy. That engine lives in
`app/transcode/worker/inprocess`, imported only by `cmd/transcode`; `cmd/manager`
and `cmd/ui` must never link ffgo or purego, which make a dynamic binary the
distroless `clustarr` image cannot start (`TestManagerNeverLinksADynamicLoader`†,
`TestUINeverLinksADynamicLoader`†). Every binary that links ffgo
passes `pkg/ffruntime` (FFmpeg 9, the shim's API, one log trampoline, calls
bounded by their context and a grace).
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

## Live TV

IPTV for Plex, as xTeVe does it (2026-10-07,
`docs/superpowers/specs/2026-10-07-iptv-live-tv-design.md`, phase 1). An
`IPTVProvider` (`clustarr.io`) names a playlist (`url`, or `urlFrom` a
Secret: Xtream URLs carry the account's credentials, and every URL that is
logged, put in status or an error passes `iptv.Redact`), XMLTV `guides`,
`filters`, `tuners` (the account's connections) and `epgSource` (`XEPG`,
clustarr's guide, or `PMS`); an `IPTVChannel` is one channel turned on,
`key` (immutable, `t:<tvg-id>` or a name key) and `number`. The provider
controller (`clustarr-livetv`) and the channel controller
(`clustarr-livetv-channel`) are their status's sole writers. `pkg/iptv/m3u`
streams the playlist under a 1 GiB cap and skips Xtream `m3u_plus`'s movie
and series entries (`/movie/`, `/series/`): the owner's provider lists 1.24
million entries in 328 MiB, 28,527 of them live. PMS takes at most ~454
channels per device (one channel-map PUT under 32 KiB of request line), so
a provider's active channels are split into **blocks** (`pkg/iptv/blocks`):
contiguous numbers, sticky boundaries, a first packing filled to 75%, an
overflowing block split at its median, `dvr.maxChannels` (default 400, at
most 450) and a 30,000-byte map budget each, at most 32. Each block is one
HDHomeRun device and one Plex DVR: its own port from `--livetv-port-range`
(default `47000-47999`; unique across providers, kept for life -- a split
never moves a surviving block's port), its own device ID
(`sha256(ns/name/start)[:4]`, or `spec.device.id`, checked unique), spans
disjoint across providers, and its owner read from cluster-plex's Lease
`livetv-<device id>` into `status.blocks[].owner`. The tuner
(`app/livetv/tuner`, the manager's `--livetv-bind-address`, `:5004`) routes
by the port in `Host` -- cluster-plex proxies each block's port from the
link-local `169.254.47.1` in every Plex pod to the one `livetv` Service
(chart `<release>-livetv`, `status.address`), so PMS's own `Host` reaches
the tuner -- or by path (`/livetv/<ns>/<name>/<start>/...`), serves each
block's lineup and XEPG guide, and relays streams per provider
(`app/livetv/relay`): viewers of one channel share one upstream, and past
`tuners` a stream answers 503. A stream's upstream must answer within 10 s.
Playlists and guides go through their own client (`tuner.Options.Fetch`),
which waits up to 2 min, because a panel builds its whole m3u_plus before it
answers. On 2026-10-20 tvload.win took 13 s for 588 MB, so every fetch
failed. At a restart only the guide is restored from the object store, never
the playlist, so a manager restart left the lineup answering 503 and every
tune 404, which Plex shows as "Could not tune channel". cluster-plex (its `livetv` branch) registers
the devices and DVRs, saves each map in one PUT, never deletes, saves,
reloads or creates while PMS loads a guide (a delete then deadlocked PMS),
and restarts PMS on the other pods after a DVR change, since each PMS reads
its DVRs once. The installers render the Service and a NetworkPolicy
(`livetv.networkPolicy`) admitting the tuner port only from Plex's pods
while every other manager port stays open (`clustarr.managerPorts`; held by
`test/guards/livetv`, its own package while `test/guards` does not build);
kustomize's policy is `config/livetv`, outside `config/manager`, whose
`includeSelectors` labels would add clustarr's labels to the peer and admit
no Plex pod. The manager's memory default carries 128Mi for the stream
buffers. E2e scenario 19 (`test/e2e/livetv_test.go`, `iptv-stub`) is written
and has never run.

## UI

The library's media types -- Movies, TV, Music, Books -- are the Library
entry's sub-menu in the sidebar (shadcn-templ `sidebar.MenuSub` under the
Library `sidebar.MenuItem`, since 2026-09-24; before that a tab strip in
the top bar). Each sub-entry swaps the page body through htmx so the
sidebar stays put.
In the TV pane, only series should be shown. Clicking a series should present a page with the seasons and episodes.
A season's episodes are Sonarr's table (2026-10-07, `ui/series.go`,
`ui/episode.go`, `ui/views/episode.templ`): the monitor bookmark, number,
title (opening the episode's details modal, Details and Search tabs), air
date, Sonarr's status cell (`episodeStatus`: downloading, pending, the
file's quality -- amber below the cutoff -- TBA, unaired, unmonitored,
missing; read from the Episode's status, which the catalog derives from
its Series' grab entries -- `activeDownloadRef`, `pendingGrab` -- and its
MediaFile's status) and two searches: the magnifier is "search now" (a
Search with `grabBest`), the person an interactive search -- a Search
without `grabBest` (`actions.InteractiveSearch`) whose ranked
`status.results` the modal polls (`GET /searches/{ns}/{name}`) and whose
download button adds the release to `spec.grab` (`actions.GrabRelease`: a
merge patch of the whole list under the read resourceVersion, so a
concurrent grab conflicts; `spec.override` for a permanent rejection,
after a confirm). The Search controller sends each pick to the item's
owner as a manual Candidate, and the panel polls until `status.grabbed`
names the grab entry it became (`downloadRef`) or the owner's refusal.
The ui role patches `searches` for it. A season is searched as one
query, planned by the Series (Wave S, 2026-10-07,
`docs/superpowers/specs/2026-10-07-series-search-design.md`): each season
header has Sonarr's two buttons -- the magnifier (`actions.SearchSeason`, a
Search with `grabBest` and `spec.season`) and the person
(`actions.InteractiveSeasonSearch`, `spec.season` without `grabBest`), whose
results open in the season's modal listing season packs first
(`?filter=packs|all`, `ReleaseDecision.coversSeason`) -- plus a badge from
`Series.status.seasonSearch`. The series header's "search now" is the
whole-series search. A planned Search (`SearchSpec.PlannedBySeries`) is
served by the Series key's season planner (`searchplan.DecideSeasons`,
the search stage's Series mode): one request at a time, one season task in
flight, each behind the namespace's live-search pace slot and admitted on
`catalog-search-high`; the Search mirrors it in `status.seasons`. A Series
is still not a Search fan-out kind (`ContainerKind`). Every interactive
search's results are banded -- approved, rejected, then the identity
mismatches (`WrongItem`, `UnknownItem`, `UnableToParse`) last, counted by
`status.otherTitles` and collapsed behind "N releases for other titles",
never dropped -- in the agent before its cap and again after the
item-state rules; and a yearless, title-only match on a title another
library item carries is `UnknownItem` (`decision.Identity.Namesakes`).
An author's books are the same table (2026-10-07, `ui/book.go`,
`ui/views/book.templ`): bookmark, title opening the book's modal
(`/library/{ns}/book/{name}/details`, `#book-modal`), release date,
`bookStatus` (an episode's cases with `unreleased` for TBA and unaired --
an undated book reads missing) and both searches, the interactive one
through the same `startInteractiveSearch` and `/searches` routes. The
episode and book templates share `searchButtons`, `searchTab`,
`fileTable` and `statusCell` (`views.RowStatus`); an artist's albums are
still `ChildRows`. A movie's interactive search is Radarr's (2026-10-20,
`ui/movie_search.go`): "Interactive Search" beside "Search Movie" in the
movie page's toolbar opens `views.MovieSearchModal` ("Interactive Search -"
and the title) in `#movie-modal`, the same results panel and `/searches` routes.
Those modals (and the season search's) are shadcn-templ dialogs open by
default, whose portal moves them to `<body>` a microtask after htmx swaps
them in and before htmx settles the swap, so htmx processed only the empty
portal owner and wired nothing inside: an interactive search's panel never
polled past "Searching indexers…" and the modal's forms fell back to full
posts (2026-10-19). `ui/static/portals.js` has htmx process every portaled
node a loaded swap owns. A test of the rendered markup cannot see this --
the poll attributes were always there -- so check such a change in a
browser.
Series monitoring is Sonarr's (2026-09-29): an episode is searched only
when its Series and the episode itself are monitored (the Episode
reconciler reads its Series, a Series' `spec.monitored` change wakes its
episodes, and the search worker drops an automatic task for an item no
longer monitored, as the *arrs' Monitored*Specification does); a season
toggle writes `spec.seasons[n].monitored`, which the
Series controller applies once to every episode of the season
(`series.SeasonCascade`, recorded in `status.seasons[].appliedMonitored`, so
an episode toggled afterwards keeps its flag) and to the season's new
episodes; and a series is added with `addOptions.monitor: none` -- the CRD
default, the rescan's and Add New's first choice -- so nothing is searched
until a season or an episode is turned on. The rescan's old `all` default
queued 5,718 episode searches for a 147-show library. Specials never count
toward the series: `status.episodeCount`, `episodeFileCount`, `nextAiring`
and `previousAiring` leave season 0 out (`series.Rollup`, as Sonarr's series
statistics do), and only the Specials row of `status.seasons` counts them.
A series' library status is read from its episodes
(`projection.seriesStatus`): Downloading while an episode's grab is in
flight, Missing while `status.missingEpisodeCount` (monitored, aired, no
file -- phase Wanted) is above zero, Downloaded once files are on disk and
nothing is missing, else Unmonitored. A Series has no file of its own and
its phase is only Ready, Pending or Unmonitored, so the movie rule read
every monitored series as Missing until 2026-09-30.

Each item should show the cover art, monitored status and selected quality profile

Each library page has an **Add New** (`/library/{tab}/add`, since
2026-09-29): it searches its kind through catalog's metadata search RPC
(TMDB movies, TVDB series, MusicBrainz artists, Open Library authors) and
adds the chosen item through `ui/actions.AddItem`; an item the library
already holds is found by `LibraryItem.ProviderID` and opened instead.
The top bar (2026-10-06, after Sonarr's) is shadcn's navigation menu: the
sidebar trigger and the breadcrumbs (`#page-crumbs`) on the left, the
search centred as an input group (`#header-find`), an Add New menu on the
right; a tab's htmx swap carries the crumbs and search out of band beside
`#library-subnav`. The search is a typeahead (2026-10-01, Sonarr's and
Radarr's header search): on a tab `GET /library/{tab}/find?q=`, elsewhere
`GET /library/find?q=` over every tab, answers a dropdown of up to 10
**monitored** items (`projection.Find`: titles through
`release.TitleNorm`, ranked exact, prefix, word prefix, anywhere; a
`tmdb:603`-style or bare provider id first), from the projection alone,
with the count (`#library-find-count`) out of band;
`ui/static/find.js` gives it arrow keys, Enter and Escape and places the
dropdown `position: fixed`. No match links to Add New with `?q=`, which
fills and runs its search. The page toolbar is shadcn's menubar
(`views.toolbar`; Options, Sort and Filter are its menus). The library has
Sonarr's mass editor: Select marks cards (`ui/static/library.js`, client
side, re-applied over every stream frame) and a bottom bar posts the
selection as `item=namespace/kind/name` to `POST /library/{tab}/bulk`
(monitor, unmonitor, search, refresh, delete), which accepts only items on
that tab in the projection, validates all before writing, and runs each
item page's own `ui/actions` call; Options (poster size, details) are the
reader's own, in localStorage. Every card has its own actions on hover
(Sonarr's poster buttons): the card is a div with a stretched link
(`data-card-link`), and Refresh & Scan, Search, Monitor/Unmonitor post
through htmx to the item's own routes with `HX-Target: action-status`,
which `finishActionWith` answers in place (200 with `ActionStatus` or
`ActionError`, never a redirect; the monitor toggle skips its row reply);
Delete opens the page's delete dialog for that item alone. Tailwind's
`group-hover` applies only under `(hover: hover)` -- headless Chromium
reports `none`, so a screenshot shows no hover bar unless the rule is
injected. The vendored shadcn-templ components are
the registry's own (`shadcn-templ add … --overwrite`, 2026-10-06: hooks are
`data-templ-*` and `window.templ`, the select's hidden input follows its
trigger and a read-only select is `ReadOnly` with a disabled trigger, so
its value still posts); Clustarr no longer keeps its own navigationmenu or
scrollarea.
An import list likewise finds an item by provider id
(`importlist.libraryByID`) and writes only the items it added itself:
one added by hand, by a rescan or by another list is recorded as listed
and never changed or removed by it (Radarr), since item names hash the
title as well as the id. catalog seeds a MetadataProvider for every
type that needs no key (`metadataprovider.SeedDefaults`: MusicBrainz,
Cover Art Archive, Open Library, Audnexus, MangaDex, AniList, Kitsu,
Anime-Lists) in its own namespace at start, create-only, skipping a type
already configured under any name; `spec.enabled: false` turns one off,
since a deleted seed returns at the next start. caption likewise seeds
the default SubtitleProfile `english` (`subtitleprofile.SeedDefault`)
when the cluster has no SubtitleProfile at all, with `audioExclude` so
only media whose audio is not English is searched; a file with untagged or
unprobed audio counts as its item's original language (the Movie's or the
Series' `status.metadata.originalLanguage`, `subtitleplan.audioLanguages`).
Books take English titles as movies and TV do (2026-09-30): the Open
Library client (`WithLanguage`, from MetadataProvider `spec.language`)
lists an author's works through its search API with `lang`, titles each
by its edition in that language (and `Book` by its most common edition
title, so a refresh keeps it), names the author by Wikidata's label, and
gives each work `Languages` so `metadataProfile.allowedLanguages` can drop
translations catalogued as works of their own; Add New sets it to `en`.
One book is one Book: Open Library holds many works per book (19 for The
Brothers Karamazov in Dostoevsky's top 100), so `Books` collapses works
whose titles are the same book (`bookKey`: case, punctuation, articles,
subtitle, brackets, byline, volume and edition words) into the
most-published one, and with a language lists no work it cannot title in
it (only-Cyrillic volumes).
Each library item's page has **Delete**
(`docs/superpowers/specs/2026-09-30-library-delete-design.md`, 2026-09-30):
the ui only patches `catalog.clustarr.io/delete` (`files` or `records`)
and `delete-add-exclusion`; import's `librarydelete` controller removes
the folder permanently for `files` (never the RootFolder, refusing a path
outside it or a folder holding another item's MediaFile), deletes the
MediaFiles and then the item, or writes `delete-error`. The library scan
skips a folder whose id an ImportExclusion names (unmatched `excluded`).
Search posters are provider URLs, so they go through `/art/search` --
signed by the ui, fetched only from the four providers' image hosts,
capped and cached -- never straight to the browser (ADR-0011).
The Plex provider answers with the full Metadata Response (2026-09-30,
`docs/superpowers/specs/2026-09-30-plex-full-metadata-response-design.md`):
tagline, studios, countries, per-country certifications (`contentRating`
by `X-Plex-Country`, `cc/rating` outside the US), `isAdult`, season posters,
episode stills, `SeasonType` for the stored order, `theme` for shows, and
the original-language title, genres and images when Plex asks in another
language; people and similar titles come from the
`clustarr-metadata-extended` KV document the metadata gateway writes per
item (`pkg/metadata/extended`; file-backed even on single-node NATS,
`BucketSpec.Durable`, since a document is rebuilt only at the item's next
refresh), which the ui reads through a closure under a 2 s deadline, never
a KV handle; provider-hosted images go through the signed `/art/search`
proxy, signed with `$CLUSTARR_ART_SIGNING_KEY` from the chart's kept
`<release>-ui-art-signing-key` Secret, because Plex stores those URLs and a
per-process key broke them at every restart. A Movie or Series document
older than `pkg/metadata.SchemaVersion` (`status.metadata.schemaVersion`)
is refreshed once, past the L2 cache (whose key carries the version), and
stays ready meanwhile -- raise the version when the gateway learns a field.
Images are chosen per (type, language) before the CRD's 50
(`selectImages`), so the original language's and fanart.tv's survive;
`contentRating` prefixes the country the gateway chose the certification
from (`status.metadata.certificationCountry`), and a request naming no
language is answered in `status.metadata.language`. Response customization (`includeFields`/`excludeFields`/
`includeElements`/`excludeElements`, never dropping ratingKey, key, guid or
type) and `episodeOrder` (no seasons for an order clustarr does not store)
are honoured. An episode answers its own `tvdb://` Guid and, since
2026-10-07, its guest cast and crew: after each Series refresh the gateway
lists the series' Episodes from the apiserver by Episode's selectable field
`spec.seriesRef` (no cache, no index -- what the first cut lacked), and for
each with a file whose document is missing or over 30 days old asks
TheTVDB's `/episodes/{id}/extended` (`pkgmetadata.EpisodePeopleProvider`),
at most 300 a pass, heartbeating the delivery
(`app/catalog/metadata/episodepeople.go`); `SchemaVersion` 4 refreshes
every series once to backfill.
PMS also asks every provider for an item's extras on each refresh
(`GET {root}/library/metadata/{ratingKey}/extras`, not in Plex's provider
docs) and reads a 404 or an empty list as "none", deleting the trailers it
holds -- which cost the library its Internet Video Archive trailers on
2026-10-06. The route (`ui/plex/extras.go`) answers Plex's own extras for the
item's (or season's) Plex id, which it asks the metadata gateway for over
`rpc.catalog.metadata.extras` (2026-10-06): the gateway answers from the
file-backed `clustarr-plex-extras` bucket (`pkg/metadata/plexextras`, keyed
by Plex id) and on a miss, or an entry older than 7 days, fetches from
`metadata.provider.plex.tv` with the `plex` MetadataProvider's token and
stores it (`app/catalog/metadata/extras.go`); "none" is stored too, and a
failed refetch serves the stale entry. The ui holds no Plex token. An item
without a Plex id answers none, and a gateway that cannot answer is 502/503,
never an empty list.
Skip segments come from TheIntroDB (2026-09-30,
`docs/superpowers/specs/2026-09-30-plex-analyze-bypass-design.md`): for a
probed movie or episode MediaFile whose `status.markers` are due
(`app/catalog/markers.Due`: never fetched, a new probe hash, Found older
than 30 days, Error than 1, NotFound than 7 the first time, then 30, then
90 once TheIntroDB has had nothing for 90 days, from
`status.markers.notFoundSince`) the remediation loop's markers planner
writes a request into the `clustarr-markers` record and publishes a
`catalog-markers` task; the metadata agent's marker worker asks the keyless
`theintrodb` MetadataProvider (a movie by TMDB id, an episode by its series'
TVDB id, season and episode -- only in the aired order TheIntroDB numbers)
with the probe's duration and answers in the record (`records.Answerer`), and
the loop writes `status.markers` under `catalog` (ADR-0016; the
`catalog-markers` field manager is gone, and a deferral to a key's reset is
republished by the loop, ADR-0019). A series TheIntroDB
lacks outright (`metadata.ErrNoTitle`, its `"media not found"` as against
`"... for provided season/episode"`) answers its other episodes without a
request for 24 hours; a file holding several episodes is not asked. Each
key's allowance is its own: Secret `apiKey`, plus `apiKeys` one per line,
spent in order, a spent key resting until its reset; the provider is
probed every 6 hours, since its probe is a counted lookup. cluster-plex seeds them
into Plex, with the probe's streams, straight into Plex's database: Plex's
API can write neither (the spec's §2 is the proof).
clustarr also detects segments itself (2026-10-01,
`docs/superpowers/specs/2026-10-01-segment-detection-design.md`,
`docs/research/segment-detection.md`), so Plex's pods run no detection
(cluster-plex sets `MarkerSource=cloud`): `markers` (`cmd/markers`), a
credential-less binary on the native image, decoding in-process on ffgo
(`pkg/segments/decode`, ADR-0017), takes a season's (or a movie's)
`AnalyzeTask` from the manager's segment planner (`app/catalog/segmentplan`,
through its Episode and MediaFile indexes), and analyzes in Go -- a
Chromaprint port bit-exact against ffmpeg's muxer for season-wide intros
and ending themes, black/entropy/edge/scroll statistics of end frames, chapter
names (Intro Skipper's patterns), and PaddleOCR text density on ONNX
Runtime through purego only when the cheaper signals are weak. Fingerprints
are cached in the `clustarr-fingerprints` object store (versioned keys and
metadata, `segments.FingerprintVersion` 2) and raw results in the
`clustarr-segments` bucket, written by compare-and-swap; its work and
TheIntroDB's ride the durable `CLUSTARR_WORK_MARKERS` stream (gotcha below);
the loop's markers planner merges them into `status.markers.segments` (each
tagged `source` and `confidence`; `pkg/segments.Merge`) under
TheIntroDB per kind, then chapters, then analysis at 60 or more -- except
that a chapter wholly titled as credits ("Credits", "End Credits",
"Ending", "Outro") outranks TheIntroDB's credits (2026-10-01: of 107 files
with both, 36 disagreed by more than 5 s, the chapter right in the worst
cases) -- in the loop's markers planner, which reads both records
(TheIntroDB's in `clustarr-markers`, the analysis in `clustarr-segments`) and
writes the merge in the loop's one apply (`app/catalog/segmenting.Applier`,
the compare-and-swap path the two used to share, went with ADR-0016's fold).
Raise `pkg/segments.AnalyzerVersion` when detection changes.
The Plex provider (`ui/plex`, ADR-0012) matches a request by the file Plex
names first (2026-09-30, rule 0 in `ui/plex/matchfile.go`): `filename`,
relative to Plex's library folder, is matched as a path suffix of
`MediaFile.spec.path` (`projection.Index.FilesEndingWith`) and answers only
when it resolves to exactly one item; a path two items share, an unknown
file or an unsafe name falls through to the guid and title rules, so the
provider never guesses between two items.
The provider answers items Plex knows with Plex's own GUIDs (2026-10-06,
`docs/superpowers/specs/2026-10-06-plex-native-guids-design.md`): Plex Web
offers Watchlist and Discover only for an item whose own `guid` is
`plex://`, and PMS 1.43.4 accepts one from a custom provider while still
reading the item from it, fetching by the GUID's id
(`/library/metadata/<24 hex>`). A keyed `plex` MetadataProvider (secretRef
key `token`) resolves the ids from `metadata.provider.plex.tv`
(`pkg/metadata/clients/plex`): a Movie's or Series' into
`status.metadata.externalIDs["plex"]` through the gateway's resolvers,
seasons into `status.metadata.plexSeasons`, and episodes into Episode
`status.plexID`, joined by TVDB episode id in the gateway's episode list,
written by the Series reconciler and kept when a later list carries none.
`ui/plex` builds `guid`, `parentGuid` and `grandparentGuid` from them
(`--plex-guids`, chart `ui.plex.plexGuids`, on by default); `ratingKey` and
`key` stay clustarr's, and every ratingKey route also resolves a Plex id
(`projection.Index.ByPlexID`, which refuses an id two items claim), with the
flag off too. This rests on PMS not enforcing its documented rule that a
provider's GUID starts with its identifier; `--plex-guids=false` is the way
back.
Series type `anime` keeps the episode order (2026-10-06,
`docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md`): it reads
absolute numbers for search, import and naming from
`status.absoluteNumber`, and only `spec.episodeOrder: absolute` changes
the order (design §4.2's "absolute forced when anime" collapsed a
multi-season anime into one season). A RootFolder's `defaults.anime` moves
a series whose TVDB genres name it Anime to that profile and type once
(`series.Classify`, recorded in `status.classification`, spec patched
under field manager `catalog-classify`), so the owner's later change
stays; `catalog.clustarr.io/classify: off` skips one, and an import list's
re-apply keeps a classified series' values. Movies have the same once-only
move since 2026-10-20 (`movie.Classify`, Movie `status.classification`): TMDB
has no anime genre, so a film is anime when a genre names Animation (or
Anime) and its original language is Japanese, and the movie folder's
`defaults.anime` (`anime-remux-1080p`, the Radarr anime built-in) gives it
English plus the original in one release. That profile does not graft
(owner, 2026-10-20: a dual-audio release, not a grafted dub) and scores
Anime Dual Audio 1000. A file missing a language a non-grafting policy
wants (`decision.IncompleteAudio`) reads CutoffUnmet, reason
`MissingAudio`, transcoded or not, until a release carrying every wanted
language replaces it. `videoItemState` lets such a release past
TranscodedFinal, the baseline rules and the upgrade table. Import's
`audioFill` takes it only when the file's own probe carries them all, and
refuses it as a releaseFault when it does not. Akira had played
Portuguese: its scanned file carried only Portuguese and Japanese audio,
which `hd-bluray-web`'s `language: original` accepted. A file whose probed audio
lacks the profile's language (`decision.LacksLanguage`; unknown or
untagged audio never lacks) reads `WrongLanguage` and CutoffUnmet, unless
it is transcoded. Without a quality upgrade it is replaced only by a
release whose title names its language (an untagged one merely assumes the
original, the assumption that imported the wrong file) and only when the
new file's own probe carries the language (`importplan.replacesWrongLanguage`),
so it never loops grab, reject and blocklist. The Movie and Episode
item keys wake on a probe's audio languages
(`rollup.AudioLanguagesObject`), and a RootFolder's spec edit wakes its
unclassified series. A profile's `audio` (`languages`, `graft`, `default`;
the anime built-ins want `[en, original]`, grafted) replaces its
`language` in the decision (`decision.audioRejection`): a release carrying
every wanted language is complete, a partial one is a video candidate only
when the profile grafts and the release carries the original language (the
anchor a graft aligns on), and a complete one wins a tie. The built-in
`anime-web-1080p` scores Anime Dual Audio 1000 (2026-10-20; TRaSH 0, or
101 to prefer), past the release-group tiers' spread (WEB 100-600, BD
700-1400), so a dual-audio release outranks every Japanese-only one of its
quality -- the quality tier and a REPACK's revision still rank first, and
a -10000 group stays below the minimum. The parser
reads a release's audio markers past Sonarr (2026-10-19,
`release.namedLanguages`): a dual-audio title -- "Dual Audio"/"Dual-Audio",
`[DUAL]`, or a bare DUAL after the resolution or right after the episode or
year -- is English beside what it names, or beside the item's original
language when it names nothing else (`ParsedRelease.DualAudio`,
`LanguagesFor`); a bare DUBBED is English; a JA+EN-style code pair names
both. Every reader takes that one reading -- the decision, the Search
results the ui lists, import's frozen `spec.languages` -- under every
profile; until then only the decision knew, under an anime score set, so a
dual-audio release showed Japanese alone. Outside anime, DUAL means the
original language plus the group's own language (2026-10-20: PD's "DUAL" NF
WEB-DL of Akira carries Portuguese, as PD's file in the library did). So a
DUAL title that names nothing else reads as the original plus the group's
language (`ParsedRelease.LanguagesWith`). That language comes from
`pkg/grouplang`'s index, learned from imports, or else
`release.builtinGroupLanguages`, seeded from the library's evidence. The
manager's downloads stage records every inspected file's pre-transcode
audio against its release group (`grouplang.Observe`), refused imports
included, in the durable `clustarr-release-groups` bucket. Only a file with
exactly one language beside English and its original votes, and a group's
verdict needs 2 votes, two thirds of the votes and a third of its files.
Each binary puts the index on its root context (`release.WithGroupLanguages`),
so every reader resolves through `release.GroupLanguagesFrom(ctx)`. The
parser also reads Sonarr's original-language scene tags
(`ParsedRelease.NamesOriginal`, resolved by `LanguagesFor`): German beside
DL (never WEB-DL's) or ML, and French (VF, VFF, VFI, VFQ, VF2, TRUEFRENCH)
beside VO or MULTi. It reads explicit runs of three-letter codes in title
order (`ENG.ITA`, `JPN.ENG`); a code alone is no language. Where the
indexer reports a release's audio languages from its MediaInfo, they beat
all of these: nzbgeek sends one `language` attribute per track, and
nzb-life sends `audio`/`language` lists mixing codes and names.
`rss.ProjectRelease` stores them as `ReleaseInfo.IndexerLanguages`
(`catalogue.ResolveLanguageName`), the grab entry keeps them
(`DownloadRelease.IndexerLanguages`), and `decision.indexerLanguages` makes
them the release's languages at search and at the grab check. Example:
hallowed's untagged Akira Blu-ray is English and Japanese. NFOs are not
fetched, since nzbgeek ignores `t=getnfo`. Items report `status.audio` (wanted, present, missing,
graft); a missing dub never blocks the video. Grafting (phase 4): the
search planner queues a donor search (`SearchTask.Purpose: audioDonor`) for
a monitored item whose graft reads `searching` or `failed`, on its own
backoff (`status.donorSearchAttempts`), at most 20 first-time live ones per
namespace per sweep (`searchplan.DonorBudget`); a donor is judged on its
languages, never its quality, and is grabbed as a grab entry with
`purpose: audioDonor` (a donor and a video grab of one item coexist, and a
donor moves neither `activeDownloadRef` nor the phase). The import agent's
execute task places it under `<RootFolder>/.clustarr/donors/<item-uid>/` (no
walk enters `.clustarr`) and reduces it to its `.mka` at once
(`pkg/transcode/donoraudio`), and the item records it (`status.audio.donor`,
derived by the item key from the entry's execute record). The remediation
loop's graft planner (`app/transcode/graftplan`) runs one graft Job per file
-- `transcode --graft-task` on the cpu pool's pod template, owned by its
MediaFile, at most `--graft-concurrency` (2) ledger slots, never beside an
open transcode of the file, answering in `clustarr-grafts` -- which aligns
the anchors (`pkg/audioalign`), muxes the dub in beside every copied stream
(`engine.Options.Graft`), verifies the muxed track and swaps it in, tagged
`CLUSTARR_GRAFT`. catalog re-probes it without making a transcode:
`spec.original` stays, `status.graftTag` is recorded, and the size, mtime and
path are catalog's from then on. A failed graft rejects that donor for the
item (`status.audio.rejectedReleases` on the item). A graft rides along with
its file's transcode when one is due: the transcode dispatch carries the
file's graft (`task.Task.Graft`, recorded as `status.transcode.joinedGraft`),
the pool worker aligns before it encodes and muxes the dub in the same pass
(`worker.GraftEngine`), and a graft waits up to 6 h for such a transcode
(`graftplan.JoinWait`) before it grafts alone; only a standalone graft
(`status.graft` Running) holds a transcode back. A multi-episode file is
never grafted (`MultiItemFile` on the item).
The movies provider has Plex's collection feature (2026-10-06; type 18,
Feature `collection` at `/library/collections`, movie libraries only): a
collection is a TMDB collection (`status.metadata.collection`), its items
the library's movies in it, answered at `/library/collections/<key>` and its
`/children` (`ui/plex/collection.go`), by Plex id or `tmdb-collection-<id>`,
and matched as type 18 by GUID or name. The gateway learns the collection's
Plex id from the movie's own Plex metadata (Plex cannot look a collection up
by TMDB id) into `collection.plexID`, and its summary, poster and background
from TMDB into the movie's extended document. Both providers declare `match`,
which Plex's provider docs require.

## Invariants — do not break these

A guard name marked † is the one the plan gives a guard the test batch has yet
to write: the unify branch's waves were built implementation-first (owner,
2026-10-07), and `.superpowers/unify/progress.md` lists every deferred test.
Until the guard lands, review holds the invariant; drop the mark when it does.

- **The manager is the only writer of every CR and Secret** (ADR-0019). Agents,
  markers and transcode pods write no Kubernetes object: an agent's client is
  read-only (`k8s.ReadOnly`, which counts a refused write in
  `clustarr_agent_write_refused_total`; `TestAgentsCallNoKubernetesWriter`†), its
  generated role grants get, list and watch only, Secrets get only
  (`TestNoAgentRoleGrantsAWriteVerb`†), and markers and the transcode pods run
  with no ServiceAccount token at all. A worker answers its task in a record
  (`pkg/records`: Durable KV, compare-and-swap) or on the intake streams, and the
  manager incorporates the answer under its own field manager. Every lifecycle --
  an item, a MediaFile, a grab and its transfer, a scan, a list sync, a search,
  an indexer's health -- is a state machine in the manager's reconcile loop, and
  every task an effect it admits and routes (`app/dispatch`).
- **One controller-writer per resource.** The sole exception is `MediaFile`, and
  the split is **spec versus status**, not status versus status, with both
  writers in the manager: `import-worker` creates the resource and owns
  `MediaFileSpec` (the observed path, size and fingerprint, plus the quality,
  revision, formatScore, matchedFormats and releaseType frozen at import),
  through the scan applier (`app/import/manager/scanapply`) and the downloads
  stage's import materialisation; `catalog` is the sole writer of all of
  `MediaFileStatus`, through the remediation loop, and additionally takes over `spec.sizeBytes`,
  `spec.modTime` and `spec.original` once it incorporates a transcode swap —
  and `spec.path` too when the transcode landed under a new name and the
  source is gone (a container change or an explicit `spec.outputPath`, ruling
  R-11). A `replaceSource=false` result is not a swap and claims nothing.
  Both write with their own field manager, so the apiserver enforces the split
  rather than convention. (Until Phase C this file claimed `import` owned
  `status.file`/`status.probe` and `catalog` `status.quality`/
  `status.formatScore`. No such fields exist — `MediaFileStatus` is flat and
  the decided fields are spec, frozen at import per spec §8.4. Nothing had
  reconciled yet, so no code ever contradicted the claim.) Since ADR-0016 the
  per-file work is MediaFile status too: the manager's one remediation loop
  (`app/remediation`, controller `mediafile`) plans and writes
  `status.subtitles`, `status.transcode`, `status.graft` and `status.markers`,
  so `catalog` owns every `MediaFileStatus` leaf
  (`TestMediaFileStatusIsOwnedByCatalogAlone`†). Workers answer through records
  in Durable KV buckets (`clustarr-probes`, `clustarr-transcodes`,
  `clustarr-grafts`, `clustarr-subtitles`, `clustarr-markers`), written by
  compare-and-swap, and hold no MediaFile RBAC (`TestFoldedAgentsWriteNoPerFileWork`†).
  Only the loop watches MediaFile (`TestOnlyTheLoopWatchesMediaFile`†); the slim
  TranscodeProfile and SubtitleProfile reconcilers and `transcode-admission`
  are woken by the loop's and the ledger's signals. SubtitleRequest,
  TranscodeJob, AudioGraft and Download are deleted (cold turkey, 2026-10-07):
  no code names them (`TestNoCodeNamesTheFoldedKinds`†, `TestNothingNamesDownload`†).
- **All status writes go through `pkg/k8s.PatchStatus`** (server-side apply with a
  named field manager). `.Status().Update()` and `.Status().Patch()` are banned
  outside `pkg/k8s` and golangci-lint's forbidigo rule enforces it.
- **No `float32`/`float64` in `api/`.** controller-gen rejects them without
  `allowDangerousTypes`, and floats do not round-trip across API clients. Use
  `resource.Quantity` for decimals a human types (e.g. seed ratio `"2.0"`) and
  scaled integers for machine telemetry: `Milli` (thousandths, 23.976 fps →
  `23976`), `Centis` (hundredths), `Percent` (whole 0-100).
- **The manager couples through Kubernetes watches; agents couple through NATS
  only** (ADR-0019). An agent reads Kubernetes through a read-only cache, takes
  its tasks from JetStream and answers in records, on `CLUSTARR_INTAKE` (grab
  candidates, scan observations) and in RPC replies; JetStream's nak and term
  advisories reach the manager on `CLUSTARR_TASK_EVENTS`. NATS otherwise carries
  rate-limited or long-running work, RPC, the release firehose, scheduled
  delivery and KV caches.
- **Only the manager ensures the NATS topology** (`busconn.EnsureTopology`, and
  `busconn.KeepTopology` after a NATS restart). `Subscribe` binds its durable and
  its dead-letter watcher and never creates either, so an older agent can never
  revert a newer topology (`TestEveryConsumerHasExactlyOneHome`†).
- **One domain per agent process.** `--domain` takes exactly one name; there is
  no `all` (`TestEveryAgentDeploymentRunsOneDomain`†).
- **Every container names its binary** (`pkg/binpath`); no image has an
  ENTRYPOINT (`TestEveryContainerNamesItsBinary`†).
- **No production code starts a process** but the usenet engine's `par2`
  (`pkg/download/usenet/repair.go`, `usenet.Par2Runner`) until par2go lands as a
  library; then only `pkg/par2child/exec.go`, whose only target is
  `os.Executable()` (ADR-0017; `TestNoProductionCodeStartsAProcess`†). Oracles
  that run ffprobe, ffmpeg or par2 belong under `test/`; several package tests
  still run the ffmpeg CLI until the test batch moves them onto
  `internal/testmedia` and recorded goldens (`TestNoGoFileRunsFFmpeg`†).
- **Cap every status list** with `+kubebuilder:validation:MaxItems`. Unbounded
  lists in status are how operators melt etcd.
- **The scanner never guesses.** Unattributable files go to
  `LibraryScan.status.unmatched` with the reason, never a speculative item.
- **The UI never writes status** and owns no CRD. User actions patch spec,
  create short-lived resources, or -- since the settings CRUD design
  (`docs/superpowers/specs/2026-09-24-settings-crud-design.md`) -- create,
  patch and delete the ten Settings kinds (RootFolder, QualityProfile,
  MetadataProvider, Indexer, IndexerProxy (2026-09-30), DownloadClient,
  SubtitleProvider, SubtitleProfile, TranscodeProfile, and ImportList from
  the Import Lists page) and create or patch the Secrets their
  credentials live in, never reading one (the role grants no get, list or
  watch on secrets) -- and, since Add New
  (`docs/superpowers/specs/2026-09-29-add-new-design.md`), create a Movie,
  Series, Artist or Author with spec only, named by `pkg/names` as
  import names it (the role's `create` on those four). So anything the
  UI does, `kubectl` can do.
- **The UI may hold a bus connection for reads plus two requests.** `cmd/ui`
  (`internal/cli/ui/bus.go`) calls `busconn.Connect` and passes
  `Bus.ObjectStore(...)` into `ui.Options.Artwork` to serve `/art` (indexed by a
  read-only watch, `pkg/events/objindex`, which also feeds the SSE `art`
  event); closures over KV `Get` alone for the Plex provider's extended
  documents (`clustarr-metadata-extended`), a transfer's and a transcode's
  progress (`clustarr-progress`) and an import's detail (`clustarr-imports`);
  and two requests, `ui.Options.MetadataSearch`, bound to
  `clustarr.rpc.catalog.metadata.search` (Add New, 2026-09-29), and
  `ui.Options.PlexExtras`, bound to `clustarr.rpc.catalog.metadata.extras`
  (2026-10-06), so the ui never holds a requester and can ask nothing else --
  `TestUINeverWrites` bans `Request`, `Publish` and `Serve` anywhere in `ui/`.
  `ui/` still never imports `pkg/k8s`.
  `TestUINeverWrites`' AST guard extends its banned-selector list with every
  object-store write method (`Put`, `PutBytes`, `UpdateMeta`, `Seal`,
  `AddLink`, `Purge`) so a NATS write is caught the same way a Kubernetes
  write already is — the connection is read-only by construction, not by
  omission.
- **Artwork objects have two writers split by variant, the same discipline
  as spec versus status on MediaFile.** The metadata gateway (agent
  `metadata`) is the sole writer of `original` objects and their metadata; the
  renderer (agent `catalog`) is the sole writer of `overlay` objects and theirs
  (Movie and Series only). Neither writes status since ADR-0019: the manager's
  item keys render `status.artwork` (under `k8s.ManagerCatalogMetadata`) and
  `status.overlay` (under `k8s.ManagerCatalogArtwork`) from the objects'
  metadata, read through the manager's own `objindex`. Neither writer reads,
  writes or deletes the other's variant. Each `Put` sends the variant's
  complete header and metadata set (`artwork.ObjectMeta`), and `SetMeta` is the
  owner's alone, under its per-item lock. No code creates object links
  (`TestNoObjectLinks`†). The ui watches the bucket read-only
  (`pkg/events/objindex`). The reaper (the manager's, leader-only) is the only
  code that deletes both; the manager's object-store maintenance loop
  (`app/maintenance`, ADR-0020 §5) purges orphan chunks in every store.

## Commands

```bash
make generate      # deepcopy + apply configurations (flattened to api/applyconfiguration/<group>/<version>)
make manifests     # CRDs + one RBAC ClusterRole per identity (RBAC_ROLES) into config/
make build         # bin/manager, bin/ui, bin/agent (cgo), bin/markers, bin/transcode (never bare `go build`)
make native-assets # FFmpeg 9 libs + libffshim, ONNX Runtime, and the test-only ffmpeg, ffprobe and par2 CLIs
make chart-deps    # vendor the chart's nats and cloudnative-pg tarballs into charts/clustarr/charts
make test          # unit + envtest + native suites; sets KUBEBUILDER_ASSETS, CLUSTARR_PG_ASSETS, LD_LIBRARY_PATH, FFGO_SHIM_DIR and CLUSTARR_REQUIRE_TOOLS=1
make lint          # golangci-lint v2
make brand-gate    # no *arr brand outside the dated records
make docker-build docker-build-native docker-selfcheck  # the clustarr and native images, and their checks (hack/image-checks.sh)
make run-dev       # manager, ui and one agent per domain against your kubeconfig and a NATS at $NATS_URL
make cardigann-bundle  # re-pack .data/Definitions into app/indexer/bundle/embedded/definitions.zip
make kind-up       # local cluster with NATS, then: make install deploy (make kind-load for the images)
make e2e           # end-to-end suite against that kind cluster (test/e2e, tag e2e)
```

Tools live in `$(go env GOPATH)/bin`: `controller-gen` v0.22.0, `setup-envtest`,
`kustomize`, `golangci-lint-v2`; `make native-assets` also needs curl, xz, unzip
and a C compiler (it compiles `libffshim.so` against the pinned FFmpeg). envtest
assets are Kubernetes 1.37.0. `go.mod` replaces the ffgo fork with
`../ffgo-unify` until the owner OKs pushing its tag (OD18), and `make contexts`
exports the pinned ref every image build takes as a named context.

## Gotchas found the hard way

- **`make test` cannot pass in a fresh clone or a new git worktree until
  `helm dependency build` has run.** `charts/clustarr/charts/` holds two
  vendored dependency tarballs (`nats`, `cloudnative-pg`) and is gitignored by
  `charts/clustarr/.gitignore`, so a clean checkout does not have them.
  `TestChartAndKustomizeAgreePerComponent` shells out to real `helm template`,
  which refuses with "found in Chart.yaml, but missing in charts/ directory"
  — and it reads as a chart/kustomize *disagreement*, which is a code defect,
  rather than as a missing local artifact. This cost a false red on a merge
  gate run in a throwaway worktree; the branch was fine. `make test` runs
  `make chart-deps` first; by hand, `helm dependency build charts/clustarr`.
- **`go test ./...` passing does not mean the CRDs are valid.** `pkg/crdcheck`
  and the envtest suites skip silently when `KUBEBUILDER_ASSETS` is unset. Only
  `make test` (or exporting the assets path) compiles the CEL rules against a real
  apiserver. A suite that finishes in milliseconds skipped.
- **controller-gen v0.22.0's `applyconfiguration` generator ignores output rules**
  — it writes next to the types regardless of `output:...:dir`, at each group's
  `+kubebuilder:ac:output:package` plus `<group>/<version>`, so it nests every
  group under its own name. `make generate` runs `hack/flatten-applyconfiguration.sh`
  afterwards, which moves each version up to `api/applyconfiguration/<group>/<version>`
  and rewrites the imports. Do not point the groups at a shared output root: they
  would share one `internal/internal.go`, the last group's schema would overwrite
  the others', and `Extract*` would fail at runtime for every other group.
- **Server-side apply replaces a field manager's ownership set on every apply —
  it does not merge.** Any field that manager previously sent and now omits is
  *released*, which reads as "reset to zero" on the object. This bit three
  different ways in Phase C alone, so treat every `PatchStatus` call as a
  complete declaration of everything that manager owns:
  - Two applies in one reconcile (spec, then labels): the second erased the
    first. Build one apply configuration instead.
  - A boolean stopped being sent once it was true: it flipped back to false.
    Keep sending it.
  - **An early-return path built a partial status** (conditions only) and
    returned: it wiped `Phase`, `Path`, and every rollup field the happy path
    had set. This is the dangerous one, because the early return is usually a
    transient failure — a full queue, a missing RootFolder — so a healthy
    object gets silently gutted by a blip.
  - **Two different components shared one field manager on one object.** The
    metadata gateway and the grab worker both write `Movie.status` as
    `catalog-worker`, so the grab released `status.metadata` — the movie
    dropped to `Phase=Pending` and the gateway refetched, on every grab. Give
    each writer its own manager (as `catalog-series` does) rather than
    re-asserting the other's fields.

  **A double-claim is silent, not loud — this file and a code comment both
  said otherwise and both were wrong.** `pkg/k8s.PatchStatus` and
  `pkg/k8s.Apply` append `client.ForceOwnership` unconditionally
  (`patch.go:88,121`), and there is no non-forcing path in the package.
  Forcing is what makes deliberate co-ownership work, so it is not a bug — but
  it means the apiserver never raises the conflict that distinct field managers
  were supposed to surface. The later applier simply takes the field. Proven by
  making one manager claim another's field and watching **every object-value
  assertion keep passing**. So a test that asserts on the object's values
  catches *under*-declaration only; an over-claim is visible in exactly one
  place, and that is where a test meaning to catch one has to look —
  `metadata.managedFields`, by manager name against the field paths it owns.

  And the reason this class keeps surviving review: **two managers can
  *co-own* a field.** SSA only needs force when their values differ, so while
  a second manager keeps applying the same value, a release by the first
  leaves the field standing and the bug is invisible. A "manager X released
  field Y" test that does not deliberately drop the co-owner reports a false
  pass — which is how the rollup release above survived three reviews.

  - **A sibling controller never got the fix its siblings did.** Movie and
    Series gained `reassertKnownStatus`; `mediafile`, built in the same wave,
    kept both partial early returns *and* a second unconditional status apply
    at the end of every reconcile. Once a rule goes in here, sweep every
    controller for it — the rule existing is not the same as the code obeying
    it. The gap fixes hit the same class outside SSA three more times: a CI fix
    to how the `media` image fetches par2 was not carried to `media-cuda`,
    which kept failing; deleting the rescan's copy of the release-group guard
    once the parser was fixed left `fileimport`'s second copy dropping real
    groups; and `rollup.DownloadOverlay` learned the just-created `""` phase
    while `pkg/pipeline`'s own mapping of the same enum did not. Before closing
    a fix, grep for the other copies of the thing you fixed.

  - **The same rule binds the main resource, not only status — and there the
    failure is a rejected write, not a silent reset.** A manager that applies
    `spec` must send every spec field it owns on every apply. D2-4's
    labels-only apply omitted `spec.clientRef`; the same manager released it,
    and the Download's "clientRef is immutable" CEL rule then rejected the
    write (the Download kind is gone since ADR-0019; the lesson stands).
    G1-3's unmonitor path sent only `spec.monitored` under the manager
    that also creates Movies, releasing the required `tmdbID`,
    `qualityProfileRef` and `rootFolderRef`, so the apiserver refused it.
    Route every write from a manager through the one function that renders
    that manager's complete set, and override a field there, rather than
    building a second, narrower apply.

  **`reassertKnownStatus` has an exception.** `MediaFileStatus`' `Conditions`
  and `Sidecars` are lists whose generated `With*` methods **append**, so a
  reassert-then-overwrite helper doubles their entries. Seed a struct and
  render it once instead of copying the sibling pattern verbatim.

  A related trap, same apply, different mechanism: **`WithConditions` appends**
  rather than replacing, so setting conditions both in a shared `baseStatus`
  helper and again at the call site is rejected outright with `duplicate
  entries for key [type="Ready"]`. Set conditions in exactly one place per
  apply.

  Tests miss all three unless they act on an object that **already has status**.
  A test that creates a blank object, triggers the path and asserts cannot
  observe a release, because there was nothing to release. Exercise the failure
  path against an object already in its steady state.
- **A lost update is not an SSA release, and no release test can see one.**
  Phase D1 hit this three times in one wave. A handler reads the object, does
  slow work (an HTTP fetch, a four-page RSS poll), then applies a status seeded
  from that stale snapshot — silently rolling back whatever another writer
  under the same manager did meanwhile. The complete-declaration rule does not
  help: every field *is* declared, just with values from a minute ago. Worse
  than a rollback, a field that was nil when read and non-nil now is
  **cleared**, because a seed that emits a pointer field only when non-nil omits
  it — so a download verb re-enabled an indexer the search fan-out had just
  disabled, undoing the backoff protecting a failing tracker. Every
  release-regression test in the tree is blind to this by construction: the
  object it seeds from is the one it wrote, so there is no window. The
  distinguishing test interleaves a *real* second writer mid-operation; the
  rule is that any path which reads, works slowly, then applies must re-`Get`
  immediately before the apply. The RSS poll did this ("the poll closes the
  window") until ADR-0019 moved the Indexer's observed fields into the manager;
  `refreshWorkerFields` (`app/indexer/controller/indexer/controller_definition.go`)
  re-reads the Indexer after a slow network operation, and the health ladder
  applies as a compare-and-swap from a fresh read.
- **Server-side apply tracks ownership per leaf inside a struct**, not for the
  sub-object as a whole. A renderer that sent only `caps.modes` released
  `caps.limitsMax`, `caps.limitsDefault`, `caps.supportsRawSearch` and
  `caps.categories` on every apply that did not re-probe — and the reconciler
  re-applies every 15 minutes while re-probing every 12 hours, so four of five
  fields zeroed within one tick of the probe that set them. The envtest that
  should have caught it asserted only that `status.caps` was non-nil, which a
  modes-only renderer satisfies. Assert every leaf, not the parent.
- **`metav1.Time` comes back from the apiserver in the replica's local
  timezone, so `.Year()` on it is wrong by a year near New Year.**
  `metav1.Time.UnmarshalJSON` converts to `time.Local` on every round-trip, so
  a release dated 1 January 00:30 UTC reads as the previous year on any host
  west of UTC — and a year feeds folder names and year-tolerance matching, so
  files land in the wrong folder. G2-3 found it in the Audiobook naming
  context; the same `.Year()` call was already in the Album controller and in
  four places in the non-video rescan and import paths. Always
  `.UTC().Year()` (and `.UTC()` before any other calendar field), and test it
  with a Jan 1 date on a west-of-UTC location.
- **A typed Go client cannot send a value the CRD default overrides.** The
  apiserver defaults only an *absent* field, so what counts is what
  `encoding/json` sends for the Go zero. A `bool,omitempty` with
  `default=true` drops `false` and gets `true` back, so it can never be set
  false; a struct-valued field (`metav1.Duration`, `resource.Quantity`, a
  nested struct) or a scalar without `omitempty` is always sent, so its
  default never reaches an object created from Go; and an `omitempty` scalar
  whose zero means something ("0 disables") loses that zero to the default.
  kubectl YAML sends every value, which is why it survives review: it hit
  five times across Phases D-F before G4-0 swept it, the worst an `int32`
  `maxOutputToSourcePercent` defaulted to `1.0` that would have failed every
  real transcode. Fix with a pointer plus an accessor that applies the
  default to nil (`app/transcode/jobspec.ReplaceSource`,
  `Revision.EffectiveVersion`), or a floor in code, documented on the field,
  where zero means nothing (`Indexer.spec.timeout`). `pkg/crdcheck`'s
  `TestNoCRDDefaultIsUnreachableFromGo` and `TestEveryStatusListIsCapped`
  guard the sweep.
- **A pod a controller builds at runtime escapes every guard the installers
  have.** The chart and `config/manager` are held to each other, to the
  generated roles and to a securityContext by tests; the pod specs grab and
  transcode build in Go were held to nothing. Transcode Job pods had no
  securityContext, UMASK or tracing flags; engine pods had no ServiceAccount
  (so they ran as `default`, bound to nothing), no `POD_NAMESPACE`, no NATS
  address, no UMASK, no `GOMEMLIMIT` and no securityContext — they could not
  have started on any cluster, and every envtest passed, because envtest
  enforces no RBAC and runs no pods. The KEDA ScaledJob example ran the wrong
  ServiceAccount without `--job` (KEDA left with the split, 2026-10-06). Fix the
  class, not the instance: test the rendered pod spec against what the installer
  creates and binds (`TestGrabEnginesRunAsAnAccountTheInstallerBinds`,
  `TestEnginePodSecurityMatchesTheDeployment`, and for the native image
  `TestNativeImageIsWhatTheManagerStampsOntoPoolsAndEngines`†), and run each
  identity under its real role (the RBAC-enforced start envtests of
  `cmd/manager`, `cmd/agent` and `cmd/ui`).
- **A test built from a fixture shaped like the answer cannot fail.** Custom
  formats' `ReleaseTitle` conditions were matched against
  `ParsedRelease.Title`, which the real parser sets to the item's title
  ("Heat"), so no repack, HDR or streaming format ever scored — and every test
  passed, because each built a `ParsedRelease` whose `Title` was the whole
  release name. The RSS matcher's yearless series fallback never matched a
  single release, and its test's fixture carried a year. A `TitleNorm` test
  used ASCII titles, on which it and `CleanTitle` are identical. Build inputs
  through the real producer (run the real parser, the real client against a
  recorded response), and falsify: revert the fix and watch the test fail by
  name. Falsification also found tests that never existed — a guard a Phase C
  ruling required ("fails if controller-gen ever generates one") and a
  RoundTripper fix, both untested.
- **Other processes share this checkout, its git state and the scratchpad.**
  In one wave an agent's `git stash` swept five other agents' uncommitted
  files (each recovered its own with `git show stash@{0}:<path>`, never
  `pop`); a process ran `git pull --rebase origin main` and pushed mid-gate,
  giving every commit of the wave a new SHA, so find commits by subject
  (`git log --grep`), never by a SHA from a report; and two agents' scripts of
  the same name in the shared scratchpad meant one repeatedly reset the
  other's throwaway worktree, invalidating its falsification runs. Keep
  scratch files under your own subdirectory, name worktrees uniquely, and
  never push from a task.
- **Never run `go get` or `go mod tidy` from parallel agents.** They corrupt
  `go.mod`. Add every dependency serially up front, then tell workers not to touch
  it.
- **The git index is shared by every agent in the worktree, so `git add <path>`
  followed by a bare `git commit` is not a path-scoped commit.** It commits
  whatever else was already staged. This swept fourteen files of another agent's
  half-finished, non-compiling work into a docs commit, under a docs message.
  Staging your own path is not the scoping step -- the commit is. The only safe
  form while anyone else is working is a pathspec on the commit itself:
  `git commit -m '...' -- path/to/file`, which builds the commit from HEAD plus
  those paths and ignores the index for everything else. To undo a sweep, do
  **not** `git reset --hard` or `git stash` (both destroy the other agent's
  uncommitted work, and `stash` is process-global): move the branch ref with a
  compare-and-swap, `git update-ref refs/heads/<branch> <sha>^ <sha>`, which
  leaves the index and working tree exactly as they were.
  **A pathspec scopes files, not authors.** Two agents editing the *same*
  file cannot be separated by it: whichever commits first takes the other's
  uncommitted hunks in that file too. That is how one agent's `lookupBooks`
  landed inside another's `lookupAlbums` commit. Harmless there, but it means
  a shared file should have one owner per wave, or be committed immediately
  after each edit.
- **A NATS KV key must match `^[-/_=\.a-zA-Z0-9]+$`, and nothing in the Go
  types enforces it.** Build every key through `events.KVKeyToken`; its
  escaping is injective on purpose, because a sanitiser that maps every
  illegal byte to one replacement collapses distinct ids onto one key and
  serves one item's state for another. This escaped twice: an illegal cache
  key made **every** metadata refresh fail forever, and an illegal exclusion
  key left the object **undeletable**, because the finalizer's `Delete`
  validates the key exactly as the `Put` did. Both times every suite that
  could have caught it ran against the in-memory bus, which has no key
  grammar — so the guard is a contract test against a real embedded server
  (`pkg/events/natsbus/kvkey_contract_test.go`), not a regex restated in a
  test file. Restating it is also how `ValidKVKey` shipped permissive:
  nats.go's gate is the character set **plus** no leading `.`, no trailing
  `.` and no `..`, and the first version implemented only the regex.
- **JetStream raises a pull consumer's MAX_DELIVERIES advisory only on its
  next delivery attempt, and that needs a waiting pull request.** nats-server's
  `deliveryCount` returns redeliveries, so the ack-timer path (`checkPending`)
  catches a lapsed final delivery only for `MaxDeliver` 1; for 2 or more,
  `getNextMsg` raises it on the next waiting pull from any replica
  (`consumer.go:4841-4849, 2427-2457, 6187-6192`). A pull a stopping pod abandons
  is pruned by the next `CONSUMER.INFO` (the manager's queue gauge, every 30 s)
  and is never relied on. A subscription fetches only for free slots and never
  parks (Wave 4c's parking spent an attempt per redelivery and dead-lettered
  healthy tasks it never ran, E11), so nothing keeps a pull open at saturation;
  the dead-letter watcher (`busconn.WatchDeadLetters`, the manager's) is the
  backstop, and membus's sweep dead-letters a lapsed final delivery in place.
  Before Wave 4f a `Consume` callback that blocked issued no pull, and a test of
  the hung case once needed `MaxInFlight = MaxDeliver+1` to see the advisory at
  all.
- **On a consumer with `BackOff`, nats-server adds `BackOff[n-1] -
  BackOff[0]` to every delayed nak.** `processNak` stamps the entry
  `now - AckWait + d`, `checkPending` redelivers once it is `BackOff[n-1]`
  old, and `BackOff` overrides AckWait with `BackOff[0]`. A retry that asked
  for `BackOff[n-1]` therefore waited nearly twice that — about 2h, not 1h,
  on `catalog-search-normal`'s fourth attempt — while membus, which has no
  such arithmetic, waited exactly `d`, so every test on the in-memory bus
  passed. natsbus now naks for `d - (BackOff[n-1] - BackOff[0])`
  (`natsbus.nakDelay`), and the contract test measures the real gap between
  deliveries on both buses.
- **`BackOff` also times a first delivery at `BackOff[0]`, so a handler
  slower than that is redelivered mid-run unless something sends
  InProgress.** The engine durable (`BackOff[0]` 10 s, no
  `HandlerTimeout`) ran payload RPCs that took about 15 s while nzb.life
  answered 522 (2026-10-15): the broker handed a second copy out mid-run,
  which fetched the payload again, and lapsed copies, served before new
  work, kept 1,153 commands undelivered, the removals of 30 GB among them.
  A durable whose handler can outlast `BackOff[0]` takes a
  `HandlerTimeout`, which makes the bus heartbeat it, or heartbeats its own
  work as the transcode pool worker does; `TestOnlyExplicitBudgetsAreEnforced`
  holds the list.
- **On single-node NATS a work stream is a few MiB of memory with
  discard-oldest, and a feature that queues by the thousand silently drops
  its neighbours' work.** `ForSingleNode` scales every stream into a 64 MiB
  memory budget (catalog's 1 GiB became 7.2 MiB), and `AllowMsgSchedules`
  forces `DiscardOld`. On 2026-10-01 TheIntroDB's tasks rescheduled to the
  allowance reset (5,028) plus segment detection's analysis tasks filled
  `CLUSTARR_WORK_CATALOG`, and 12,161 messages -- catalog's own
  searches, grabs and metadata work among them -- were discarded without an
  error; the analysis queue's ack floor read as 842 tasks done for 7
  results. Such work gets its own stream marked `StreamSpec.Durable`
  (file-backed at full size, outside the memory budget):
  `CLUSTARR_WORK_MARKERS`, 256 MiB, `clustarr.work.markers.>`.
- **A query's wait for its own rate limiter counted against the indexer's
  timeout, and the wanted sweep searched every item live at once.** On
  2026-10-06 the sweep published 104 searches in one second; index
  (answering RPCs concurrently since 8638117b) queued all of them on
  nzbgeek's 2 s `requestDelay`, and the fan-out's per-query deadline --
  started before the wait -- expired for every query past the fifteenth or
  so, which were recorded as nzbgeek timing out and disabled it (an earlier
  sweep had grabbed 73 releases in one hour). Two fixes:
  `torznab.Client.WaitTurn` takes the request slot inside the search's
  budget and `spec.timeout` starts after it, a query that gets no slot is
  the `paced` skip, never a failure (`app/indexer/search/pacing_test.go`);
  and an item searched before is searched `SearchTask.IndexOnly`, which the
  search worker answers from the index agent's local release index
  (`clustarr.rpc.index.query`, every release the RSS feeds delivered, no
  indexer query) -- an item never searched gets one live search, and Add New,
  "Search now" and redownloads stay live. Since ADR-0019 the item key's search
  planner decides this (`app/catalog/searchplan`), and every live search takes
  its namespace's pace slot first (`--search-live-per-minute`, 20 by default)
  and is admitted through the dispatch ledger. A pool of searches must never be
  fired at an indexer in one burst. A
  season search (Wave S) is one paced query per season per indexer,
  never one per episode: it counts as each wanted episode's search (the
  Episode folds its season's answer into `lastSearchedAt` once, so its
  next search is IndexOnly) and an Episode waits while its season is
  queued or searching; nothing publishes per-episode searches for it.
- **A payload fetch an indexer answers 429 pauses its grabs locally, and a
  429 whose body names one release's cooldown pauses that release alone
  (2026-10-16).** `spec.limits.grabLimit`'s ring is clustarr's count, not
  the indexer's allowance: three usenet engines spent nzb-life's as fast as
  it came back for seven hours, each re-add of a lost transfer fetching its
  .nzb again. `app/indexer/limits.PauseGrabs` records the refusal in
  `clustarr-indexer-limits` (Retry-After, else 15 min doubling to 6 h, never
  shortening a pause in force), the download verb refuses with
  `DownloadResponse.RetryAt`, the engines defer the command until then
  (`engine.PayloadDeferred`), and fetches to one indexer are single-flight so
  a burst asks it once. But nzb-life's 429 was never an account limit: it is
  "Already Downloaded", 24 h per NZB, and the indexer-wide pause held every
  nzb-life grab for the longest cooldown among the entries retrying. The verb
  sniffs the 429's body (`releaseCooldown`) and records such a refusal under
  `ReleasePauseKey` -- the indexer's other releases are fetched meanwhile.
- **An import that imports nothing is classed, never blocklisted at once
  (2026-10-07).** Refusing every file used to read as a bad release, so a
  good 21 GB download the item merely would not take (its file transcoded)
  was blocklisted and deleted. Since ADR-0019 the import plan
  (`app/import/importplan`, in the manager's downloads stage) classes every
  refusal (`importplan.Classify`) and settles the outcome by the entry's import
  class: `transient` retries at 1m, 10m and 45m then holds; `releaseFault` is
  inspected once more before the release index blocklists it; `itemState` and
  `needsPerson` hold at once; a hold keeps the files for
  `lifecycle.ImportHoldRetention` (24h) and then fails as `importExpired`,
  neither blocklisted nor searched again. A new rejection site must say its
  class -- a rejection built by its class's constructor, never a bare string --
  and the grab lifecycle reads the class, never a message.
- **A Helm hook with no delete policy defaults to `before-hook-creation`,
  which destroys anything holding data.** A `post-install,post-upgrade`
  hook carrying no `helm.sh/hook-delete-policy` annotation is deleted and
  recreated on every `helm upgrade` — fine for a Job, fatal for a database.
  The CNPG `Cluster` for the Postgres-backed release index
  (`charts/clustarr/templates/postgres-cluster.yaml`) shipped as exactly
  that hook first, caught in review before it reached `main`: a stateful
  resource that must survive `helm upgrade` is a normal templated resource
  carrying `helm.sh/resource-policy: keep` (as `pvc.yaml`'s `/data` claim
  already does), never a hook. Ordering against an external operator
  (CNPG's admission webhook fails closed until its own Deployment is Ready)
  is a render-time `clustarr.validate` fail plus documentation instead —
  and `--wait` does **not** substitute for that ordering, since it only
  waits on the release's own resources, never a resource Helm did not
  install.
- **A JetStream object digest is `SHA-256=<base64url>`, not the hex
  `events.ObjectInfo.Digest` documents.** `natsbus.objectDigestHex`
  (`pkg/events/natsbus/objectstore.go`) converts through
  `jetstream.DecodeObjectDigest` plus `hex.EncodeToString`; an empty digest
  (a freshly created, still-uploading object) stays empty rather than
  erroring, and anything else that fails to decode is reported as an error,
  never silently turned into `""`.
- **`make pg-assets` (`CLUSTARR_PG_ASSETS`) populates `embedded-postgres`'s
  cache directory once, the way `setup-envtest` populates
  `KUBEBUILDER_ASSETS`.** `make test`/`test-race` depend on it and export
  the variable; `pkg/relindex`'s Postgres store contract test skips
  silently, naming the variable, when it is unset — the same silent-skip
  trap `KUBEBUILDER_ASSETS` already has above, now with a second variable
  to remember.
- **`git stash` swept another task's uncommitted work again, despite the
  rule above.** An M7 task ran a path-scoped `git stash` mid-task; the
  stash list was empty afterwards and no damage was observed this time, but
  the rule stands exactly as written above: never `git stash` while another
  agent may be working in the same checkout.
- **`ForSingleNode` keeps object stores on file storage.** It maps streams
  and KV buckets to memory (scaled into a 64 MiB budget under config/nats'
  256Mi `max_memory_store`), but the artwork object store reserves 5 GiB,
  and mapping it to memory made every controller crash-loop on kind at
  start (`ensure object store clustarr-artwork: insufficient memory
  resources available`, 2026-09-24). `TestEnsureDefaultTopology`'s embedded
  server has no memory ceiling, so it never saw it;
  `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` runs the same
  Ensure against a server capped like the cluster's. Anything new in the
  topology must fit that server.
- **The Helm chart's NATS ran the server's 1 MiB `max_payload` default
  while `config/nats` has always set 8Mi, and natsbus dropped the refused
  reply on the floor.** index answers `rpc.index.download` with the
  .nzb inline (base64 in JSON, so 4/3 of its size); a 1080p movie's
  ~1.3 MB .nzb is over 1 MiB, nats.go refused the reply client-side with
  `ErrMaxPayload`, `Serve` discarded that error, and the usenet engine
  waited out its 45 s deadline on every attempt -- the first real NZB
  grab on the owner's cluster sat in `Assigned` with "context deadline
  exceeded" while index's metrics counted a grab (2026-09-24). Two
  fixes: `nats.config.merge.max_payload: "<< 8Mi >>"` in the chart, held
  to kustomize by `TestBothInstallersRaiseTheNATSMaxPayload`; and `Serve`
  answers a reply it cannot send with a header-only service error, so the
  requester fails at once naming the cause
  (`TestServeReportsAReplyTheServerRefuses`). Operational trap on top: a
  `max_payload` reload updates the server, but a connected nats.go client
  keeps the `INFO` it read at connect, so the publisher (index) had to
  be restarted before the raised limit took effect. The inline cap itself
  (4 MiB raw) then refused The Godfather's 1080p .nzb (2026-09-30): an
  .nzb grows with the release, about 3.8 MB for 28 GB, so any large encode
  hit it. The bus reply now gzips a body over 1 MiB
  (`schema.DownloadResponse.ForWire`, `encoding: gzip`; NZB XML compresses
  about four times), refuses one still over `schema.MaxDownloadWireBytes`
  (5.75 MiB) compressed, and both engines read it through
  `DownloadResponse.Payload`; index reads up to 32 MiB raw, the usenet
  engine's own cap. A real-server test holds the budget to the broker's
  8Mi, envelope included.
- **A par2 set named `name.vol-01.par2`..`vol-07.par2` carries no block
  counts, and the usenet engine read it as unrepairable.** `par2VolumeRE`
  only knew `vol000+01`, so those volumes classified as *index* files with
  zero blocks, `criticalHealthPercent` came out at 100, and the first failed
  article of 14,169 paused a 10 GB transfer at 1% under `healthAction:
  pause` ("health 99% is below the floor (abort 90%, critical 100%)") -- the
  same grab as above, once the payload reached the engine. The regex now
  takes the block-less form, `criticalHealthPercent` falls back to NZBGet's
  size-based estimate (`(size - 2*par) / (size - par)`, capped at 99) when
  no volume names its blocks, and `par2IndexFile` hands par2cmdline the
  smallest volume when the set has no separate index, since every volume
  carries the main packet. Real subjects from that post were the fixture.
  Since then the floor follows SABnzbd's byte rule, in which block counts play
  no part (`TestCriticalHealthPercentFollowsSABnzbdsByteRule`; the
  missing-article gotcha below).
- **`par2 r index.par2` alone reports every target missing when the set
  records other names than the files on disk carry, and the engine's
  failure text then overflowed `status.message`.** The same grab's set
  described obfuscated names (`z75QO...part070.rar`) while the files were
  written under the subjects' names; SABnzbd and NZBGet pass the
  directory's files as extras so par2 matches them by content, and
  `Par2Runner.Repair` now does too (`extraFiles`;
  `TestPar2RunnerRepairsASetWhoseFilesCarryOtherNames` runs real par2).
  The failure message -- par2's 2048-byte output tail behind a prefix --
  was over the CRD's `MaxLength=2048`, so the apiserver rejected every
  status apply and a finished, failed transfer read `Downloading` for
  good; `download.ClampMessage` cuts it on a rune boundary, the same class as
  `clampPercent`. Since ADR-0019 the engine writes it into its transfer record
  (2048), and the manager cuts an entry's message to
  `lifecycle.MaxEntryMessage` (1024) when it renders the grab entry: any string
  an engine reports must be clamped at the `pkg/download` boundary and again
  where the manager writes it into a bounded CRD field, never trusted.
- **The usenet engine's emptyDir scratch shares the node disk with etcd
  on kind, and vanishes with the pod.** A 10 GB transfer plus its par2
  verify pushed etcd's fsyncs past a second: the grab controllers lost their
  leader lease and exited, then the kube-apiserver failed its liveness probe and was
  killed (2026-09-24); and a deploy that replaced the engine pod discarded
  a finished 9.4 GB transfer, so the new pod downloaded it again. Place
  the working area on the shared volume instead -- `spec.usenet.scratch.
  path: /data/usenet/incomplete` and `spec.usenet.publishDir:
  /data/usenet/complete` (ADR-0014; `existingClaim`, `volumeName` and
  `accessModes` cover a claim of the operator's own). The manager's
  DownloadClient controller refuses a path off the data mount with
  `Ready=False, InvalidSpec`.
  Torrent clients have the same placements since 2026-09-30
  (`spec.torrent.scratch`, `spec.torrent.publishDir`, default
  `/data/torrents`): a transfer downloads in scratch and reads Completed
  only once `pkg/download/torrent`'s publish has moved it to publishDir,
  where it seeds; a storage class is a claim per StatefulSet replica
  (volumeClaimTemplates, whose change replaces the StatefulSet keeping its
  pods), and no scratch means download in place as before.
- **A newznab .nzb is not byte-stable across fetches, so a payload hash
  cannot tell a re-add from a new transfer.** nzbgeek varies the obfuscated
  `title` and `password` metas on every download of the same release; the
  usenet engine re-resolved a Download whose status did not yet carry its
  id (a status write had failed) and added the release a second time under
  a second hash -- two 10 GB transfers of one Download, the first an orphan
  until the reaper's 10-minute grace (2026-09-24). `usenet.Client.Add` now
  dedupes on `AddRequest.Name` as well as on the payload. Since ADR-0019 the
  name is the grab entry's id: the engine's add command asks the client
  (`download.ByName`) for a job under that id before fetching a payload, and
  again after a download RPC that timed out, before asking twice -- every fetch
  is a grab the indexer counts -- and the engine journal claims a transfer by
  the same id, so a re-grab of the same release reuses it. Torrents are
  unaffected: an info hash is the content.
- **The usenet engine's missing-article rules are SABnzbd's, by
  number.** `maxArticleTries` 3 same-server retries for a connection that
  dies mid-batch (`max_art_tries`); `maxBadArticles` 5 tolerated before
  any judgement (`MAX_BAD_ARTICLES`); `hopeless` is
  `check_availability_ratio` with `req_completion_rate` 100.2%, in bytes
  and integer arithmetic; plus one retry pass over every missing article
  (`retryFailed`, `ArticleRetryDelay`) before a breach or at the end of the
  transfer. Whole-percent health against a whole-percent floor decided a
  10 GB job at the margin once ("96%" that was 96.98% against 97), so the
  gate compares bytes, and the percentages in status are for reading only.
- **A provider can answer a message-ID with another post's article, whole
  and CRC-clean.** frugal served AnoZu's S01E01 (posted 2026-02-16, part 1
  of 2100) with part 237 of 503 of "b5f29fcb1fe75f9e", posted 2026-08-12
  to alt.binaries.bloaf under the same message-IDs; easynews held the
  original. Every such article's own pcrc32 held, so the engine wrote it at
  its own post's offsets, counted nothing missing and skipped par2 ("every
  article verified on receipt"): 22 of 435 transfers in five days came out
  as foreign bytes and holes, refused at import as "not a playable media
  file" -- a releaseFault, so the automatic ones were blocklisted -- and
  the first article's foreign `size=` cut the file short. SABnzbd and
  NZBGet check only the CRC (and NZBGet the size). The pool now checks a
  body against the segment it was asked for (`segmentMismatch`: yEnc
  `part=` is the NZB segment number, `total=` is not short of the file's
  highest), and an article that fails is no copy on that server
  (`ErrArticleMismatch`), so the next one's is taken, as for a corrupt one.
  The yEnc `name=` is no test: obfuscated posts name every article
  differently.
- **A usenet engine ran every job at once, and OOM-looped seventy times
  in six days (2026-10-14).** `Add` and re-attach started each job
  immediately, each with a worker per connection of every provider, and
  a restart resumed all of them -- two archives unpacking and a dozen
  transfers -- past its 8 GiB, so every start ended the same way. Now
  `usenet.Config.MaxActive` (2) jobs hold a transfer slot and
  `MaxPostProcess` (1) post-process; the rest wait Queued
  (`priority.go`: the best class, then the oldest; a paused or outranked
  job gives its slot back). Such a wait is `Item.WaitingTurn`
  (`TransferRecord.waitingTurn`), which stops the manager's stall clock,
  and `lastProgressAt` restarts when the turn comes -- a queue is not a
  stall. The engine serves pprof on `127.0.0.1:6060`
  (`downloadclient.EnginePprofAddress`), reached through
  `kubectl port-forward`.
- **`exec.CommandContext` alone does not end a subprocess that has
  children.** Cancelling the context kills only the leader; a wrapper
  script's `sleep` (or par2's helpers) kept the stdout pipe open and
  `Wait` blocked until the child exited on its own, which made the par2
  deadline test hang for the child's whole lifetime. `Par2Runner.Repair`
  sets `Setpgid`, a `Cancel` that kills the process group, and
  `WaitDelay`; do the same for any subprocess given a deadline.
- **Every cache strips `managedFields`, and the cache-sync timeout is ten
  minutes.** On the owner's library (15,630 Episodes, a 57 MB list that
  `kubectl` alone takes 40 s to fetch) caption crash-looped on
  controller-runtime's two-minute default while every service listed at
  once after an upgrade, so `pkg/k8s.ManagerOptions` sets
  `Controller.CacheSyncTimeout = k8s.CacheSyncTimeout` and a
  `DefaultTransform` of `cache.TransformStripManagedFields()`, and the ui's
  reader does the same. The trap: a reconciler that reads `managedFields`
  off an object from the cached client sees none, silently. Read them
  through `mgr.GetAPIReader()`, as the grab worker's `appliedByGrabPath`
  did (it retired with ADR-0019); the artwork envtests read them through a
  direct client. One process now caches every kind the manager reconciles, so
  the ten minutes is the manager's start budget for the whole library.
- **A watch's map function runs for every object at startup, and the
  source is not synced until it has.** controller-runtime's `Kind` source
  waits on `handlerRegistration.HasSynced`, and a cache's initial sync
  delivers every object as a create, so an `EnqueueRequestsFromMapFunc`
  that lists a namespace per event is O(watched × listed) before the
  controller may start. caption's two Movie/Episode map functions each
  listed every MediaFile: ~450 million deep copies on the owner's library
  (16,450 items, 13,754 files), the Episode source never synced inside
  `CacheSyncTimeout`, and caption crash-looped (2026-09-29; the "load
  after a deploy" explanation recorded before was wrong). Look a
  watched object's related objects up through a field index (the remediation
  loop's `remediation.mediafile.item`, `app/remediation/mfindex`, which
  replaced `app/caption/itemindex` and eight other MediaFile indexes in the
  fold), never a filtered namespace List.
- **On kind, loading an image into the node stalls etcd, and every lease
  with it.** `kind load docker-image` unpacks gigabytes into the node's
  containerd store, which shares a disk with etcd; etcd's WAL fsync then
  took 3-15 s (2026-09-30), kube-controller-manager and kube-scheduler
  restarted ~50 times, and every leader-elected service exited "leader
  election lost" (controller-runtime times a lease request out at
  RenewDeadline/2, 5 s by default). `pkg/k8s.ManagerOptions` sets the lease
  to 60 s / 45 s / 5 s (`k8s.LeaseDuration`, `RenewDeadline`,
  `RetryPeriod`), and a deploy loads only images whose inputs changed.
- **A torrent engine behind a SOCKS5 proxy (`spec.torrent.proxy`,
  2026-09-30) must not leak by UDP or by DNS, and Go's library covers
  neither.** `golang.org/x/net/proxy` speaks CONNECT only, so `pkg/socks5`
  implements UDP ASSOCIATE itself; the proxied engine opens no local socket
  (anacrolix `DisableTCP`/`DisableUTP`/`NoDHT`, no incoming, no WebTorrent)
  and adds a CONNECT dialer, a DHT server and a pure-Go uTP dialer on UDP
  associations, and a listener that only names `listenPort` for announces.
  anacrolix resolves a UDP tracker's host (`net.ResolveUDPAddr`) and the DHT
  its bootstrap routers before any packet reaches a PacketConn, and the
  pod's `ndots:5` plus the host's search domain sent `tracker.org.appkins.io`
  to the node's resolver, so the engine process swaps `net.DefaultResolver`
  for `socks5.Proxy.Resolver`, which routes each query by its question name:
  cluster names to cluster DNS, everything else over TCP through the proxy
  to `dnsServer` (Mullvad's SOCKS server refuses its own resolver
  `10.64.0.1:53`, hence `1.1.1.1:53`). A proxy that refuses UDP ASSOCIATE
  leaves the DHT, uTP and UDP trackers off with a `ProxyUDPUnavailable`
  Event, never direct. anacrolix/utp stalls ~1.5 s on a first burst with or
  without a proxy; its proxy test skips under `-race` for that reason.
- **No Movie had a certification until 2026-09-30.**
  `tmdb.mapReleaseDates` parsed each release's certification into the
  client model and nothing promoted one to `Movie.Certification`, and the
  gateway passed an empty region to every lookup, so Plex showed no content
  rating for any film. `metadata.PickCertification` now chooses the
  region's, else the origin country's, else the US one, from
  `CertificationsFromReleases`. The recorded fixture shows why the origin
  step exists and why it does not always decide: Weekend (2011) carries GB
  18 and US NR, so a US-region install shows NR. TVDB episodes likewise
  never carried their own id, so `Episode.status.tvdbID` was 0 everywhere.
- **Use `github.com/dlclark/regexp2`, not stdlib `regexp`, for TRaSH patterns.**
  Go's RE2 rejects 157 of the 2791 custom-format regexes (backtracking,
  lookaround). Set `IgnoreCase` and a `MatchTimeout`.
- **Quality profiles are TRaSH-only and opinionated.** Built-in profiles ship as
  embedded data; custom-format editing is deliberately not exposed.
- **Every per-file answer is a record written by compare-and-swap, and only the
  loop writes MediaFile status.** A worker answers its dispatch with
  `records.Answerer.Answer`, a compare-and-swap on the record's revision
  (`pkg/records`); a raced answer is
  `records.ErrRaced`, redone from a fresh read, and the loop incorporates the
  record in its one status apply under `catalog`. A fact (a swap, a graft) that
  lands after its dispatch was withdrawn is still incorporated
  (`records.FactWindow`, `ErrUnincorporatedFact`). Any new per-file worker
  answers through a record, never a status write. The lesson underneath is
  older: two write paths under one manager need compare-and-swap. Before the
  fold, transcode wrote `TranscodeJob.status` from its reconciler and its results
  consumer through `writeStatus`/`patchCAS`, carrying the read `resourceVersion`,
  so a race was a Conflict redone from a fresh read, not a silent rollback; the
  Indexer's writers still do (`app/indexer/status/statuspatch.PatchCAS`).
- **A usenet transfer runs only under a connection grant from the shared
  ledger (2026-10-16, `docs/superpowers/specs/2026-10-16-usenet-connection-ledger-design.md`).**
  `clustarr-usenet-connections` holds one compare-and-swap entry per
  provider account (host, port, username: every pod and every
  DownloadClient on that account share the plan's limit), each grant
  expiring 90 s after its last renewal. A transfer takes a share
  (`limit / MaxActive`) on the best primary with room and fetches only
  there, bounded by the grant; no primary with room means it parks
  Queued, `WaitingTurn` (the manager's stall clock and the engine's stall
  watch both stop), naming the wait. Articles the granted server lacks
  are chased through the other servers in order, backups last, each under
  a grant of its own, and the job fails as missing only after every
  server was tried; the pre-check STATs its shortfall the same way. A
  paused or outranked job gives its grant back; a grant the renewal finds
  gone is taken again, never assumed. One pod with `Ledger` nil runs the
  in-process `LocalLedger`, which is the same rules. Usenet is still one
  pod (`replicas == 1`): lifting that needs per-engine scratch and a
  StatefulSet, spec §6.
- **A running Job's template is re-sent, not re-rendered.** An SSA manager that stops sending
  a template field releases it, and a released field on a running Job is a rejected write. So
  `app/transcode/controller/pool.Render` re-sends the template recorded in
  `transcode.clustarr.io/applied-template`, and judges drift against that annotation rather
  than the stored template, which carries apiserver defaults.
- **A fired schedule has no `Nats-Msg-Id`.** nats-server strips it and adds
  `Nats-Scheduler` and `Nats-Schedule-Next: purge` (`scheduler.go:214-231`); a
  copy published with those onto a stream without schedules is refused with
  10188, which lost every scheduled task's dead letter until 2026-10-07. The
  envelope's ID rides in `Clustarr-Id` too, and `EnvelopeFromHeaders` drops every
  transport header (`Nats-Schedule*`, `Nats-Expected-*`, `Nats-TTL`,
  `Nats-Rollup`).
- **The broker keys `InProgress`, `Ack` and `Nak` by stream sequence, not by
  delivery.** A lapsed handler could keep its redelivered copy from timing out,
  or move its deadline, so the bus mutes a lapsed delivery's `InProgress` and
  `Nak` (`Ack` and `Term` still go; `clustarr_bus_muted_total`). Double ack plus
  Msg-Id dedupe is not exactly-once after a lapse; the record fence is.
- **Object stores.** `Put` replaces an object's whole metadata (every `Put` is a
  complete declaration, like an SSA apply). Two concurrent `Put`s of one name
  leak a full copy for good, which only `PurgeOrphanChunks` reclaims, since the
  meta-level `List` cannot see it. A reader whose object is overwritten
  mid-`Get` stalls until its deadline and fails with `i/o timeout`, never a
  digest mismatch, so `/art` reads whole objects under a deadline before it
  answers. A link fires no watch event when its target changes, and nats.go
  refuses a link over any name a regular object ever held -- hence no links
  (ADR-0021). `ObjectStore.Watch` ignores `ResumeFromRevision` and `MetaOnly`
  (the bus refuses `WatchFromRevision`), and a bucket re-created under a
  running watch is skipped silently, so `objindex` compares `Status().Created`.
- **ffgo maps FFmpeg at package init, and a stale shim is silently wrong.** Any
  binary or test that links ffgo has dlopened FFmpeg before `main`, through
  `LD_LIBRARY_PATH` and a fixed list of system paths, and ffgo's loader
  remembers a failure, so `t.Setenv` is too late: native tests run through
  `make test`, which sets the variables, and a native test that skips under
  them is a failure, not a pass. A shim older than `ffruntime.MinShimAPI` reads
  FFmpeg 9's structs at FFmpeg 4-7 offsets; `ffruntime.Load` refuses it. A call
  that outlives its context is abandoned, not killed (`clustarr_ffgo_abandoned_calls`);
  at `ffruntime.MaxAbandoned` (4) the process reports itself wedged and its
  `ffgo` liveness check restarts it.
- **`agent --self-check` is the image's proof.** It checks the cgo SQLite piece
  completion (R13) and FFmpeg 9 with what the import and caption domains need;
  `markers --self-check` and `transcode --self-check=<class>` (`cpu`, `cuda`,
  `intel`; `--trial` encodes for real) cover the rest. `make docker-selfcheck`
  and CI run them all through `hack/image-checks.sh`, against an image whose
  binaries are UPX-packed: a packed executable hides the `DT_NEEDED` entries
  `lddtree` reads, so `images/distroless/stage.sh` traces the binaries before
  they are packed.
- **The unify branch was built implementation-first.** From Wave 2 on its tasks
  wrote no tests (owner, 2026-10-07): each recorded the tests and guards it
  skipped in `.superpowers/unify/progress.md`, test files that no longer compile
  were left for the batch, and `go vet ./...` fails on those packages until it
  lands. `go build ./...`, `make build`, `make generate manifests` (clean),
  `make brand-gate` and `helm lint --strict charts/clustarr` are the gates that
  hold meanwhile; a guard marked † above is one the batch writes.

## Code conventions

- GPL-3.0 header from `hack/boilerplate.go.txt` on every Go file.
- Logging is `slog` carried through `context` (`pkg/obs/logging.FromContext`). No
  package-level logger, no logger struct fields. controller-runtime gets a
  `logr.FromSlogHandler` bridge at startup.
- Spans wrap every `Reconcile`, work handler, outbound provider call and ffmpeg
  run; propagation rides the `Clustarr-Trace` header already in `pkg/events`.
- Prometheus metrics use the `clustarr_` prefix and base units. **Never label by
  title, path or release name** — unbounded cardinality.
- Tests: table-driven, testify, fixtures under `test/data/`. Pure functions where
  the logic is tricky (`pkg/pipeline.Project`, `pkg/transcode/standard.Plan`) so it is
  testable without a cluster.

### Conventions across `pkg/`

- **The caller owns rate limiting.** A library package accepts an injected
  limiter (`torznab.WithRateLimit`; the metadata, OpenSubtitles.com and
  Gestdown clients' `Config.Limiter`) and never defaults one on; the
  controller holds one limiter per host. `pkg/subtitles/providers/
  opensubtitlescom` shipped in Phase F with exactly the violation this rule
  warns against -- `Config.Limiter == nil` silently got `rate.NewLimiter(5,
  5)` -- so a caller with no Limiter configured got a private 5 req/s
  allowance per `Provider` instance instead of the shared budget caption's
  KV token bucket (`app/caption/throttle`) provides; fixed under ruling R3,
  alongside giving `providers/gestdown` the injection point it had never had.
- **Every HTTP response body is read through a cap** — a package-level max, an
  `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel.
- **Provider errors expose sentinels** (`metadata.ErrNotFound`,
  `subtitles.ErrNotFound`, …) so `errors.Is` works through `Unwrap`.
- **Filesystem writes that must survive a crash go through `pkg/fsops`**
  (`AtomicWrite` fsyncs the file and its parent and takes an explicit mode).
- **A copy across filesystems writes its destination with `O_DIRECT`**
  (`fsops.copyFile`: `MoveAtomic`'s EXDEV fallback, `CopyDir`,
  `HardlinkOrCopy`), 64 MiB per write, the source's pages dropped behind
  the read. /data is NFS, and a buffered copy onto it leaves the data in
  the page cache under writeback, charged to the pod with no per-cgroup
  throttling: on 2026-10-15 a buffered 3 GiB copy held 1.9 GiB in
  writeback and crawled at 8.3 MB/s, and transcode pool and usenet engine
  pods were OOM-killed moving outputs and releases from local scratch.
  Never move a large file onto /data with `io.Copy` or `os.WriteFile`.

## Status

Pre-alpha, and it reconciles. Since the manager/agent split (2026-10-06/07,
below) the services run as one `cmd/manager`, one `cmd/agent` per domain,
`cmd/ui`, `cmd/markers` and `cmd/transcode`. Until then every service --
`catalog`, `import`, `index`, `grab`, `transcode`, `caption` and `ui` --
registered every controller, worker and server behind its role flags of one
`clustarr` binary, and `clustarr all` stood every service up in one process
(controller roles only for `grab` and `transcode`, so it never downloaded or
transcoded). The paragraphs below are history in the order it happened, and
keep the names of their time.

M0 (done): the 29 CRDs across five groups, `pkg/events` (NATS and in-memory
behind one contract suite), `pkg/k8s`, the binary, manifests, chart and images.

Phase A (done): `pkg/obs` (`logging`, `tracing`, `metrics`, `obs.Bootstrap`),
`docs/observability.md`, the `LibraryScan` kind and `import` skeleton,
`pkg/pipeline` and the `ui` skeleton; all services wired into the binary.

Phase B (done): the library layer, thirteen pure-Go packages (only
`api/common/v1alpha1` shared types; `pkg/quality` alone reads catalog CRD
types). `release` (rls-based parser for every kind, batch ranges, id tokens,
130-title corpus); `quality` (vendored TRaSH corpus, all 2,791 regexes compile
under regexp2, 66 embedded formats proven byte-identical, 13 built-in profiles,
Match/Score, FromCRD, upgrade decision); `naming` (four dialects); `mediainfo`
(two-call ffprobe, HDR/DV, ProbeHash, MovieHash); `transcode` (argv goldens,
runner, cleanup, verify); `torznab`/`newznab`; `cardigann` (v11, 25 filters,
five logins, HTML/JSON/XML); `subtitles` (Bazarr scoring, cue-aware
post-processing, OpenSubtitles, Gestdown, embedded); `metadata` (six clients);
`importlist` (five lists, Dedupe, ApplySyncLevel); `fsops`/`ratelimit`.
Fixtures under `test/data/<pkg>/`, no network in tests, ffmpeg tests skip
without it.

Phase C (done): M1 catalog core and library rescan — the first phase that
reconciles. `catalog` controllers for Movie, Series, Episode, MediaFile,
RootFolder, QualityProfile (a `Bootstrap` runnable seeds the 13 TRaSH
built-ins), DelayProfile, MetadataProvider and Search; the metadata gateway
(registry over Phase B's clients, RPC plus work queue, L1 memory + L2 NATS KV);
`pkg/decision` (`Evaluate`/`Rank`) over `quality.Profile`; the search, grab (KV
lease, delay profiles) and RSS-matcher workers and the wanted cron. `import`
controllers for LibraryScan, the RootFolder schedule and ImportExclusion, plus
the rescan worker under the never-guess rule. Traces now cross the bus in
production (`pkg/events` hooks; every `run.go` passes
`k8s.WithBusHooks(obs.BusHooks())` to `k8s.ConnectBus`, AST-guarded); RBAC
generates from controller markers, with a byte-for-byte test holding the
chart's copy to it; readiness is per-service and runs on every replica, not
only the leader; `test/e2e`, `config/e2e`, the fixture image and `hack/e2e.sh`
land scenarios 5, 7 and 8 on kind.

Phase D1 (done): M2 indexers — `index` controllers for `Indexer`,
`IndexerDefinition` and `IndexerProxy`; the caps probe with its 12-hour
refresh (`capsTTL`, `app/indexer/controller/indexer/controller.go`); the
health/backoff escalation ladder in `app/indexer/status/health.go` (Prowlarr's
ten-rung `EscalationBackOff` periods, 0s to 24h, with a 15-minute startup
grace); the SQLite FTS5 release index (`pkg/relindex`, external-content FTS5,
pure-Go `modernc.org/sqlite`, no cgo); the federated search
(`app/indexer/search`) with its dedupe (infohash, then per-indexer
`(indexer, guid)`) and the KV-backed query-limit window
(`clustarr-indexer-limits`); the RSS poll worker and release firehose
(`app/indexer/worker/rss`); the three RPC verbs `rpc.index.search|download|query`
that make `app/catalog/worker/search`'s calls reach a real server for the first
time; `app/indexer/status` as the single declaration of each field manager's
owned set (`ControllerFields`/`WorkerFields`). E2E scenario 17
(`test/e2e/indexer_test.go`, against the Torznab fixture under
`test/fixtures/torznabstub/`) is written — Indexer health and caps, ranked
federated search, the release firehose, failure backoff — but has **never
been executed against a kind cluster**; that and `make e2e` generally are
deferred by explicit user instruction — first until D1–D3 were in, then
through E–G to Phase H — not by oversight. Reconciles against envtest; not
proven end to end.

Phase D2 (done): M3 downloads and import — `grab` and `import`'s
file-import worker. `pkg/download/torrent` over `anacrolix/torrent`
(idempotent `Add` on the infohash, `Resume`/`SetSeedCriteria` no-ops on an
already-satisfied state, anacrolix state mapped onto `download.Status`);
`pkg/download/usenet` over `Tensai75/nntp` plus pure-Go `javi11/rapidyenc` —
`javi11/nntppool` was ruled out as cgo-only (R9), so this package writes its
own bounded connection pool, 430-failover across configured servers,
pipelining and quota accounting, then PAR2-verifies/repairs (shelling out to
`par2cmdline-turbo`, no Go module -- since 2026-10-06 ADR-0017 makes it par2go
as a library, built once a par2go release can be required) and unpacks with
`github.com/nwaples/rardecode/v2`. `app/grab/status` declares the two disjoint
field-manager sets on `Download.status` — `ControllerFields`
(`k8s.ManagerGrab`, nine fields) and `EngineFields`
(`k8s.ManagerGrabEngine`, twenty-three then, twenty-five since gap fix
Y2 added `engineFailureReason` and `seedGoalReached`, twenty-six since Z1
added `healthPaused`) — and `Patch` refuses any other
manager. `app/grab/controller/downloadclient` reconciles `DownloadClient` into
its engine workload (a `StatefulSet` per torrent client, a `Deployment` for
usenet), `DiskSpaceOK` and the blocklist sweep.
`app/grab/controller/download` picks a `ClientRef`, waits for `EngineReady`,
pins the choice into `status.engine` once and never recomputes it, and runs
the `removeDataOnDelete` finalizer. `app/grab/engine/torrent` and
`app/grab/engine/usenet` gate readiness on re-attach completing first (R4) and
write only `EngineFields` telemetry; each carries a level-driven `Reaper`
(`torrent.Reaper`, `usenet.Reaper`) that lists client transfers against
owned Downloads and removes orphans past a grace period, proved by deleting a
`Download` with the engine's watch deliberately not firing.
`app/import/worker/fileimport` consumes `ConsumerImportFile`
(`"import-fileimport"`) and creates `MediaFile`, writing only
`MediaFileSpec` per the spec/status split; it also settled the
`Download.status.import` ownership question three comments disagreed on,
onto `k8s.ManagerImport`. D2-4, D2-5 and D2-6 could not advance
`status.phase` past `Assigned` — it is `ControllerFields`, not something an
engine can write — so `grep -rn WorkFileImportSubject` found the subject
builder and the consumer but no publisher, and the phase→import handoff that
is D2's own gate could not complete until D2-8a
(`app/grab/controller/download`) added phase derivation from engine telemetry
(`Assigned`→`Queued`→`Downloading`→`Completed`→`Seeding`→`Imported`, plus
`Paused`/`Failed`/`Blocklisted`/`Removing`) and published exactly one
`schema.ImportTask` per completion. Separately, D2-8 found the grab
reconcilers, both engines, both reapers and the file-import worker
registered nowhere and wired them into `app/grab/run.go` and
`cmd/clustarr`, added `grab` to the registration guard's
`runnableServices`, and regenerated RBAC. A CEL defect found along the way
was Critical: the `Download` release-identity rule
(`api/download/v1alpha1/download_types.go`) compared five `+optional` fields
unguarded, so an absent one raised "no such key" and the apiserver rejected
the write outright — fatal for usenet, which never carries `infoHash` — for
every write after creation, including the status-only applies that are
grab's only way to report progress; fixed in `c5e86d5` by guarding every
comparison with `has()`, with `pkg/crdcheck/download_cel_test.go` as the
regression guard using a dynamic client so an absent `infoHash` stays absent
on the wire. E2E scenarios (`test/e2e/download_test.go`,
`test/e2e/import_test.go`: 1 through import, 2, 3, 4, 6) are **written and
never executed**, deferred by user instruction to Phase H.
Reconciles against envtest; not proven end to end.

Phase D3 (done): the first UI slice — the pipeline and downloads pages over
real cluster state, streamed. `ui/reader.go`'s `NewClusterReader` builds a
standalone controller-runtime `cache.Cache` (no manager, no metrics or
leader-election port); `Options.Reader` staying nil is still legal and renders
empty pages. `/readyz` now gates on cache sync while `/healthz` stays
unconditional, and `config/rbac/ui_role.yaml` is a hand-written
`get,list,watch`-only role (no `*/status`) bound to the previously-unbound
`ServiceAccount ui` (at D3; Phase G adds exactly `ui/actions`' grants).
`ui/projection`'s `Projection` replaces the old per-connection 5s poll with
one process-wide tick that lists `Download`, `TranscodeJob`,
`SubtitleRequest`, `Search` and `MediaFile`, indexes them by owner through
`metav1.OwnerReference` UID, projects `pipeline.Entry` and
`downloadv1.Download` rows, and fans both out through `Subscribe`/
`SubscribeDownloads` to every open SSE connection. `ui/views/downloads.templ`
renders all eleven `DownloadPhase` values plus a `DownloadClient` section
without panicking on a zero-value status; `GET /downloads` and
`GET /events/downloads` join the existing pipeline page and stream. The
never-writes invariant (CLAUDE.md) is now two enforced guards rather than an
accident of no RBAC grant existing (D3-4, `6597877`): `ui/guard_test.go`'s
`TestUINeverWrites` is an AST guard over every non-test file in `ui/`
rejecting a `client.Writer` selector (`Create`, `Update`, `Patch`, `Delete`,
`DeleteAllOf`, `Status`) by method name on any receiver -- it is syntactic,
not type-checked, so it over-matches rather than tracing types back to
controller-runtime's `client` package -- plus any import of `pkg/k8s`;
`cmd/clustarr/ui_rbac_test.go`'s (now `test/guards/ui_rbac_test.go`'s) `TestUIRoleGrantsOnlyReadsAndActionWrites`
(named `TestUIRoleGrantsOnlyReadVerbs` until Phase G ruling R2 narrowed both
guards to admit `ui/actions`' writes) and `TestUIRoleChartMatchesConfig`
asserted `ui_role.yaml`'s verbs were a subset of `{get,list,watch}` with no
`/status` resource, and hold the chart's copy byte-identical to it (Phase G
below narrows both). E2E scenario 14 (`test/e2e/ui_test.go`: the pipeline and
downloads pages) is **written and never executed**, deferred the same way.
Reconciles against envtest; not proven end to end.

Phase E (done): M4 transcode — `transcode`. `app/transcode/status` declares the
disjoint sets on `TranscodeJob.status` — `ControllerFields`
(`k8s.ManagerTranscode`) and a worker-owned field set for `progress`,
`result` and `stderrTail` — plus `ProfileFields`/`PatchProfile`, and `Patch`
refuses any other manager (the transcode-worker-pools plan later retired the
worker manager; see below). `app/transcode/controller/transcodeprofile`
computes `status.hash` through `app/transcode/worker.ProfileSpec`, the same
conversion the worker renders from
(`TestStatusHashChangesWithEveryRenderField` fails if a render field stops
moving it), marks the loser of two overlapping selectors with an `Overlap`
condition, and creates one TranscodeJob per selected MediaFile not already
tagged `CLUSTARR_PROFILE=<profile>@<hash>`, named `<mediafile>-<hash[:8]>` so
a re-run creates nothing. `app/transcode/controller/transcodejob` plans from the
MediaFile's stored probe (a Dolby Vision `reject` or a container change lands
as `Skipped` with a reason, rulings R1 and R8), creates a suspended `batch/v1`
Job whose `podFailurePolicy` fails on exit codes 3 and 4 and ignores
`DisruptionTarget`, unsuspends through the pure `Admit` slot scheduler, and
owns the transcode metrics, observed once per terminal transition (R9).
`app/transcode/worker` runs in the Job pod: re-probe and refuse a changed source
(exit 3), encode, verify (exit 4), then hard-link the original into the
recycle bin (`fsops.RecycleLink`) and rename the output over the source, so
the library path is never empty; a retry after a crash past the swap finds the
`CLUSTARR_PROFILE` tag and exits 0.
`TestTranscodeWorkerExitCodeReachesTheProcess` runs the real `main` to prove
the exit codes survive; at Phase E the Job ran as its own `transcode-worker`
ServiceAccount with a generated role
(`config/rbac/transcode_worker_role.yaml`); `--worker-image`,
`--worker-image-cuda` (removed 2026-10-01, ADR-0015) and `--data-claim` are
threaded through config and chart. (The gap fixes reversed two of Phase E's rulings: a container change
and `replaceSource: false` are transcoded now, ruling R-11, below. The
transcode-worker-pools plan, 2026-09-23/24, replaced this per-TranscodeJob
Job with one long-lived pool Job per (TranscodeProfile, hardware class): see
`docs/superpowers/plans/2026-09-23-transcode-worker-pools.md` and design spec
§6.4/ADR-0009. The `transcode-worker` ServiceAccount and role above are gone;
the worker became a separate, credential-less binary, `cmd/transcode` since the
split.) Defects a
future reader must know: `maxOutputToSourcePercent` was an
`int32` defaulted to `1.0`, so **every real transcode would have exited 4**;
`policy.replaceSource`/`recycleBin` could not be set false from Go and
`activeDeadline`/`resources`/`scratch` never got their defaults from a Go
client (the typed-client gotcha above); `replaceSource: false` is now rejected
by CEL rather than silently ignored. E2E scenario 12
(`test/e2e/transcode_test.go`, against an HDR10 clip generated in the fixture
image; Dolby Vision skips with a named reason, since ffmpeg alone cannot
synthesise an RPU) and scenario 1's transcode leg (skipped on the `.bin`
fixture gap) are **written and never executed**, deferred by user instruction.
Reconciles against envtest, real libx265 encodes included; not proven end to
end.

Phase F (done): M5 subtitles — `caption`. `app/caption/status` declares each
manager's set: `RequestControllerFields`/`RequestWorkerFields` behind
`PatchRequest`, `ProfileFields`, and `ProviderFields` behind a `PatchProvider`
that refuses the worker's manager — the SubtitleProvider controller is the
only writer of provider status (ruling R2), and workers record throttle, quota
and auth into the `clustarr-provider-throttle` KV bucket instead
(`app/caption/throttle`: a per-provider token bucket shared by every replica,
keys through `events.KVKeyToken`, contract-tested against a real embedded NATS
server). `SubtitleRequest.status.items` is split per leaf between the two
managers (R4), and the split needed a liveness protocol to work at all: an
item is live exactly when the controller has given it a `nextSearchAt`
(`status.IsLive`, `LiveItemKeys`); the controller creates and withdraws items,
the worker re-sends leaves only for live items and never creates one, and
`items[].state` became optional. Before that the worker re-sent every item, so
a withdrawn language could never be removed, and only `managedFields` caught
the controller claiming the worker's `state`. `pkg/subtitles` gained the
missing-subtitle `Plan` and right-to-left `SidecarName`/`ParseSidecar`;
`pkg/lang.Normalize` maps ffprobe's ISO 639-2 onto the profiles' BCP-47 — the
third instance of the language-vocabulary class, and it also fixed TVDB's
original language. The three controllers
(`app/caption/controller/subtitleprofile`, `subtitleprovider`,
`subtitlerequest`) and the fetch worker (`app/caption/worker/fetch` over
`app/caption/providerset`, never leader-elected) are registered behind
`caption`'s role flags; one `providerset.Validate` now serves controller
and worker (the two copies had drifted — a Gestdown provider with a missing
Secret reported Ready), and `app/caption/datapath` gives both one `/data`
mapping. Fixed along the way: the embedded provider claimed
hash-verifiability, so embedded tracks scored 0-1 and were never taken;
OpenSubtitles.com defaulted a private 5 req/s limiter and read JSON uncapped
(R3); a forced search was absorbed by the JetStream dedup window for up to an
hour until `events.MsgIDForForcedSubtitle` gave it an ID of its own — so fetch
MsgIDs now extend spec §6.5's `<uid>/<langKey>/<probeHash>` as a prefix, a
recorded deviation; and the `caption-worker` KEDA `ScaledObject` counted a
consumer that does not exist, so it never scaled
(`TestCaptionScaledObjectCountsTheRealFetchConsumers`; KEDA and the guard left
with the split, 2026-10-06, for the HPAs the manager owns). E2E scenario 13
(`test/e2e/subtitle_test.go`, against `test/fixtures/opensubtitlesstub` and
`gestdownstub`, both round-tripped through the real clients) and scenario 1's
subtitle leg (skipped on the `.bin` gap) are **written and never executed**,
deferred by user instruction. Reconciles against envtest; not proven end to
end.

Phase G (done): M6 parity, import lists, non-video inventory and the rest of
the UI. **Indexers.** A Cardigann definition is one more `Client` behind
`app/indexer/controller/indexer`'s `ClientCache.For` (ruling R5), so it inherits
the fan-out's dedupe, query-limit window and backoff; a `SearchBlock.Error`
match is now a `*cardigann.SearchError` (`ErrSearchFailed`) that escalates the
indexer instead of reading as "no results" (R6); sessions live in the
`clustarr-indexer-sessions` KV bucket (`SessionKey`, real-NATS contract test)
plus an owned `<indexer>-session` Secret; `IndexerProxy` http/socks5 is
applied; `cardigann.Engine` takes an injected limiter. `app/indexer/facade`
serves the Torznab facade (`/{indexer}/api`, `/{indexer}/download`,
`/search/api`) on `--facade-bind-address` (`:9696` under `clustarr all`) and
**fails closed** without a key from `--facade-api-key-secret` (created with
one random key if absent); a Search with `spec.query` runs through
`rpc.index.query`. Automatic search falls back to a title query:
`BuildSearchRequest` fills `SearchRequest.Text` from the resolved title and
`app/indexer/search` uses it only for an indexer that supports none of the
request's ids (`SearchOutcome.QueryMode` records which). That made live the
absence of any release-identity check — a text fallback could approve the
wrong film — so `pkg/decision/identity.go` now rejects `WrongItem` (ids decide
when both sides carry one; otherwise titles plus year ±1, or
season/episode/absolute/air-date) and `UnknownItem` when unevaluable, fed by
`search.MovieIdentity`/`EpisodeIdentity` at both construction sites. **Lists
and history.** `import` gains the ImportList controller and sync worker
(Trakt device flow with a `SecretTokenStore`, `status.auth` for the UI,
ImportExclusion respected); `catalog`'s `RoleHistory` starts the history
`Sink` and the `DLQProjector`, which per R1 applies a
`clustarr.io/dead-lettered` annotation under `clustarr-dlq-projector` and
emits an Event rather than writing anyone's status. **Non-video catalog.** The
metadata gateway is the sole writer of `status.metadata` on Artist, Album,
Author, Book, Audiobook and Comic; the Artist→Album, Author→Book (standalone
Book too), Comic→Issue and Audiobook controllers follow Series→Episode, and
only Comic→Issue uses `ManagerCatalogFanout`, since Issue, like Episode,
has no `status.metadata`. Rescan attributes non-video files to existing items
only; manual import is the `catalog.clustarr.io/import-target` and
`import-override` annotations on a Download (re-queued by
`fileimport.Retrigger`) or on a LibraryScan whose `spec.subpath` names the
file (manual assignment). **UI.** Every write lives in `ui/actions` — search
now, rescan, monitor, manual assign and eight Settings patches — as merge
patches of spec under `clustarr-ui`; `TestUINeverWrites` allows Create/Patch
there and nowhere else and bans every status write everywhere, and the role
guard holds `ui_role.yaml` to reads plus exactly `actions.Grants()`. The
library, unmatched, import-lists and settings pages ride the one projection
with no new ticker; Tailwind is a pinned standalone binary behind `make css`,
with `ui/static/app.css` committed and htmx vendored.
`TestBothUICommandsWireEveryUIOption` runs both ui commands and fails on any
unset option — the text guard it replaced missed that `/library`, `/unmatched`
and `/import-lists` rendered no rows in production. **G4-0** swept `api/` for
the typed-client defaulting trap and uncapped status lists and left two guards
behind (gotcha above). Defects found and fixed along the way: the non-video
built-in profiles listed tiers worst-first, so PDF was the best ebook and
every comic met cutoff; `pkg/fsops` knew only `.mkv` as video, so **every
`.mp4` movie was skipped**, and applied the 50 MiB sample floor to every kind
— classification is now per kind (`fsops.Classifier`), the floor is video-only
and set by `--sample-max-bytes`, and a size-suspected sample goes to
`status.unmatched` as `suspected_sample` instead of vanishing; every naming
preset left dangling separators for an empty optional token
(`"The Matrix (1999) - -RlsGrp"`, shipped in Phase C); ComicVine wants the
`4050-N` guid for a volume but a bare `N` in the issues filter, which every
fake accepted either way; Audiobook's region was hard-coded `us`; and a
re-scan erased a MediaFile's frozen import fields by re-applying
`MediaFileSpec` under the same manager. E2E scenarios 9, 10 and 11
(`test/e2e/importlist_test.go`, `cardigann_test.go`, `nonvideo_test.go`,
against `cardigannstub`, `httpproxystub`, `importliststub` and `nonvideostub`)
and the rest of 14 (`TestUILibraryImportListsSettingsAndUnmatchedPages`) are
**written and never executed**, deferred by user instruction; scenario 9 only
checked that the Trakt and Plex CRs are accepted, since neither client took a
base-URL override (the gap fixes added `--trakt-base-url`/`--plex-base-url`
and un-skipped both). Reconciles against envtest; not proven end to end.

Gap fixes (done, 2026-09-23): plan `docs/superpowers/plans/2026-09-23-gap-fixes.md`,
tasks X1-X16 in four waves — W0 every API change serially, W1a/W1b code under
strict file ownership, W2 wiring and RBAC, W3 docs — closing the carried list
except what the spec defers. **API** (X1, X15): `CutoffUnevaluated` on Movie
and Episode (an unresolved profile no longer reads `CutoffUnmet`);
`IndexerProxy.spec.port` required; `ReleaseDecision` moved to
`api/common/v1alpha1` so controller-gen generates Search's apply configuration
and the hand-written one is gone; `fieldRecording`; all nine image types;
`IssueStatus.cutoffMet`, `pendingGrab` and state `delayed`; `MediaRef.track`;
`TranscodeProfileSpec.maxConcurrent`; `hdrOffset`/`minimumSeeders`/
`cleanupDays` as pointers with `*OrDefault` accessors; `ReleaseInfo.alsoOn`;
`MovieMetadata.secondaryYear`; `replaceSource=false` admitted;
`k8s.MarkDeadLettered`/`DeadLetteredAnnotationChanged`, folded into the
`DeadLettered` condition of all sixteen annotatable kinds; ImportList CEL
admitting only the kinds its provider can yield;
`IndexerDefinition.status.replaces`. **Catalog and search:**
`status.activeDownloadRef` has one writer, the item's reconciler, derived from
its owned non-terminal Downloads (`rollup.ActiveDownload`,
`rollup.DownloadNonTerminal`) on all six grabbable kinds, and
`rollup.DownloadOverlay` reads every Download phase before Imported as
Downloading; the grab path has one source resolver shared with Search-CR grabs
(`app/catalog/worker/grab/downloads.ResolveSource`), a double-grab guard that
lists Downloads through the uncached APIReader, reclaimable and re-enterable
leases, CAS status writes, and grabs albums, books, audiobooks and issues,
which automatic search now covers too (text queries after Lidarr, Readarr and
Mylar); `decision.Identity` gains `SecondaryYear`, `FullSeason` on a
single-episode search, TheXEM scene mappings (`pkg/metadata/scenemap`) and
non-video rules; ReleaseTitle custom formats read the release or file name;
the RSS matcher keys series as Sonarr does; Series airings get a writer;
`history.Replayer` serves `clustarr.io/replay`; item, media-file, indexer,
download and transcode events all have producers. **Metadata:** eight new
clients (coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
animelists) that reach Ready, every client read through `metadata.ReadBody`/
`CappedTransport`, the real `tmdb.SearchMovies`/`musicbrainz.SearchArtists`,
`Album.Releases`, Open Library editions, ComicVine status, artwork and
resolver enrichment of `status.metadata`, Lidarr's album release selection
(`selectedReleaseID` is the Album reconciler's leaf), Kapowarr issue numbers
and injective Issue names. **Release and quality:** `release.TitleNorm` on
both sides of the release index (non-Latin search works), Radarr's
ReleaseGroupParser, MULTi is not a language, an untagged release takes the
item's original language, non-video qualities on Lidarr's music ladder,
multi-episode names per Sonarr. **Indexers:** every field the Cardigann schema
decodes, XML queried with CSS, cookie and form logins that work,
session-expiry re-login, `app/indexer/proxy` (selector, FlareSolverr, SOCKS4;
an empty selector matches nothing and ambiguity fails closed),
`minimumSeeders`, RSS pages and direct grabs counted into their windows,
`alsoOn`. **Downloads:** torrent file selection by `spec.target.keys`, usenet
priority classes, `Item.AddedAt`, and an engine finalizer so the controller
removes files only after the engine lets go. **Import:** best file first per
single-file item, episode and series-root imports, honest rescan counters,
redelivery resume, `fileimport.RecycleSweeper`, `removeAndDelete` through the
recycle bin, `automaticAdd=false`. **Transcode:** one output rule
(`app/transcode/worker.OutputPath`), `status.plan` equal to the worker's argv,
per-profile `maxConcurrent`, Job pods with the Deployments' securityContext,
`--intel-render-groups`, the Intel QSV/VAAPI runtime in the media image.
**Subtitles:** SubDL and SubSource clients, Bazarr pooling, an effective
`embedded.extract`, one shared OpenSubtitles login, sidecar mode from the
RootFolder. **Wiring** (X14, X16): one generated ClusterRole per identity
(`RBAC_ROLES`), proven by a start envtest that runs every service as its own
ServiceAccount under only its role; a registration guard that derives the
services and catches bare-`Start` runnables, unadded `EveryReplica`s and
duplicate controller names; UMASK applied by the binary; engine pods with a
ServiceAccount (`--engine-service-account`), namespace, NATS URL, UMASK,
`GOMEMLIMIT` and the Deployments' securityContext; `--trakt-base-url`,
`--plex-base-url`, `--ui-bind-address`; ui `/readyz` after the first
projection round. **The Cardigann corpus** (`7b6fc4a`): the project owner
added Prowlarr's Cardigann definitions on 2026-09-23 — 752 files packed by
`hack/pack-cardigann` into `app/indexer/bundle/embedded/definitions.zip`
(`go:embed`, read through `archive/zip` as an `fs.FS`), 749 of which load;
index applies them at startup (`--cardigann-bundled`, default true), and
`--cardigann-definitions-dir` (chart: the index service's `cardigann.*` values) replaces them.
**Found and fixed beyond the list:** engine pods had no ServiceAccount,
namespace or NATS address and could not have started on any cluster; grab
leases were never released, so every item could be grabbed exactly once; the
RSS title fallback had never matched a series without an id; the generic
`.torrent` fetch bypassed the Indexer's proxy, passkey included; TMDB and
ComicVine API keys leaked into error strings; every HDR10 remux with non-AAC
audio failed (`-vf` beside stream copy, exit 234) and every 8-bit QSV encode
failed (exit 218); `fsops.IsPart` missed a transcode's `<stem>.part.<ext>`;
a multi-file download gave one item several MediaFiles; `removeDataOnDelete`
was a no-op for every torrent; HTML `case` keys, XML definitions (a panic)
and cookie logins were all broken for the whole corpus; MusicBrainz release
statuses never matched; an album listing one recording twice made a status the
apiserver would have rejected; ImportList `Synced` reset once its checkpoint expired.
**Rulings:** R-1 subtitle sync, Whisper and chunked transcoding stay
spec-deferred and CEL-forced off; R-2 usenet dedupe stays per indexer; R-3 an
automatic single-episode search rejects a full-season pack (Sonarr); R-4
subtitles pool every provider, Bazarr-style; R-5 one writer of
`activeDownloadRef`, the reconciler; R-6 each engine's finalizer gates the
controller's file removal, with a 10-minute timeout for a gone engine; R-7
`SecondaryYear` exact or ±1 of `Year`; R-8 only three `omitempty` scalars
become pointers; R-9 `IndexerProxy.spec.port` required; R-10 an unyieldable
import-list kind is refused at admission; R-11 `replaceSource=false` and the
container change are implemented; R-12 confirmation-only items closed with
evidence, or fixed where the evidence said so; R-13 vendor the Cardigann corpus only under a compatible licence —
superseded by the owner's addition of the corpus above. E2E: X12c made the
download fixtures serve real media, deployed the seeder and NNTP stubs, and
wrote scenarios 15 and 16 (16's Helm and `clustarr all` legs skip); every
scenario is still **written and never executed**. Reconciles against envtest
(W2 gate green at `732c4a1`); not proven end to end.

Gap fixes Y1-Y3 (done, 2026-09-23): the four failure-handling behaviours the
spec named and nothing built (spec §4.4, §5, §8.3 now say "as built").
**Hung-handler DLQ** (Y1, `bd6227e`): natsbus watches JetStream's
`MAX_DELIVERIES` advisory per subscription, in the queue group
`clustarr-dlq-watch-<durable>`, and copies a message whose final delivery
lapsed on AckWait under the in-process path's Msg-Id, deleting a work-queue
original afterwards; membus sweeps the same case. A non-work consumer's dead
letter is named by its durable (`clustarr.dlq.catalog.redownload.<id>`);
before, two event consumers shared one DLQ subject. `events.KV.DeleteRevision`
(`1ab8ce2`) is a revision-checked delete (`ErrRevisionMismatch`; refuses
revision 0, which JetStream reads as unconditional). **Failure reasons, seed
goal, blocklisting** (Y2, `813145b`, `56c10c1`, `0b607c0`): engines report
through `status.engineFailureReason` (torrent `stalled` after
`spec.torrent.stallTimeout`, default 24h; `diskFull`; `writeError`; usenet
`missingArticles`, `encrypted`, `diskFull`, `writeError`, and `timeout` after
the opt-in `downloadTimeout`) and `status.seedGoalReached`; the controller
records a terminal `failureReason`, labels a release fault blocklisted before
publishing `failed`, leaves a local fault (`diskFull`, `writeError`) Failed
and unlabelled, and fires `seedGoalMet`; engines remove a Failed or
Blocklisted transfer. A server that could not be asked (bad credentials,
refused connection) is `ErrProvidersUnavailable` and retries, not
`missingArticles`. `importRejected` is read off `status.import` through
`downloadv1alpha1.ImportMessageEveryFileRejected`, one constant both services
name. **Redownload** (Y3, `dd1ea96`): the `catalog-redownload` durable on
`failed` and `blocklisted` frees the grab lease (`grab.FreeLeases`,
revision-checked) and publishes one `redownload` search per monitored target
under a per-Download Msg-Id, none for a local fault (Radarr raises no
DownloadFailedEvent for one) or a failure older than 24h; the grab records
`grabbedBy=redownload`. The carried items, including the owner's policy call
on `importRejected` blocklisting at once, are under *Downloads and events* in
the remaining-work plan.

Z-wave (done, 2026-09-23; Z1-Z6 plus a final pass, reports in
`.superpowers/sdd/2026-09-23-gap-fixes/`): every fixable item left open after
the gap fixes. Downloads read `healthAction` (`pause` holds a job with
`status.healthPaused` until `spec.paused` is toggled) and `removeCompleted`,
persist seed counters, blocklist `payloadMismatch`, reach a live transfer
through `download.Client.SetPriority`, and roll the engine when a start-time
setting or a usenet provider Secret's data changes (Secrets read by name with
`get` alone, no watch, so within 5 minutes). natsbus runs `MaxInFlight`
handlers, `CLUSTARR_ADVISORIES` captures MAX_DELIVERIES advisories durably,
retries wait exactly their backoff, and the never-used consumers, subjects and
search-cache bucket are pruned. Engines and the transcode worker write 1 Hz
telemetry into `clustarr-progress`. RSS matches non-video releases and
alternate titles; searches send scene numbering. The open list is the
unchecked items under "Still open after the gap fixes" in the remaining-work
plan.

M7 (done, 2026-09-24): index/artwork/ratings/Plex
(`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md`).
**A** `pkg/relindex` gains `OpenPostgres` behind the same `Store` contract
(`storetest.Run` runs both engines), selected by `--index-dsn`
(`CLUSTARR_INDEX_DSN`, ADR-0010); a DSN turns on leader election for every
index controller and the retention sweep, since more than one replica may
now run; the chart and kustomize gained an optional CloudNativePG `Cluster`
(`postgres.enabled`/`config/postgres`) as a kept normal resource, never a
hook (a ruling reversing this design's own hook sentence and the plan's R2,
gotchas above), and `nack` left the chart. **B** a JetStream object store
bucket, `clustarr-artwork` (5Gi, file storage — `ForSingleNode` had to learn
every object store belongs there, not only KV and streams, gotchas above),
holds provider-fetched `original` artwork and `spec.artwork` overrides, one
writer per variant (the metadata gateway; the renderer, `catalog --role
artwork`); `ui` gained a read-only NATS connection and serves it at `/art`,
dropping every provider hotlink (ADR-0011); both installers give the ui
`NATS_URL` (the first cut did not, so every image request hung — the
`TestEveryNATSDialingDeploymentCarriesNATSURL` guard). **C**
`status.metadata.ratings` from TMDB for **movies only** — series carry no
ratings in M7: TMDB declares its source for movies only, TVDB (the series
provider) supplies none, and MDBList and OMDb, declared as fallthrough, were
not built (ruling R5: no API keys at hand). MDBList has since landed
(2026-09-24, `pkg/metadata/clients/mdblist`, built against responses
recorded from the live API): every source for movies and series, with an
optional second key spent once the first's daily quota is out; OMDb still
reports `Ready=False, InvalidSpec`;
`OverlayProfile` and `pkg/overlay` composite Kometa-style rating badges
onto posters, rendered by the same `--role artwork` worker for Movie and
Series only, bounded to 2 concurrent renders by default and drawn at most
2000px wide (`MaxRenderWidth`). **D** `ui/plex` serves Plex's Custom
Metadata Provider protocol at `/plex/movies` and `/plex/tv` (`--plex-provider`,
`--external-url`/`CLUSTARR_EXTERNAL_URL`; ADR-0012), read-only over the
existing projection cache (one index build per 5 s, `projection.IndexTTL`),
gated 503 with no external URL configured.
Reconciles against envtest, with real Postgres (`make pg-assets`) and real
NATS object-store round trips included; e2e scenario 18
(`test/e2e/plex_test.go`) is written and, like every scenario since Phase C,
has never run on kind — its thumb-fetch leg skips by name rather than
asserting, since `config/e2e` has no egress and no in-cluster fixture serves
image bytes yet. Every deferred item the build surfaced is under "M7 carried
items" in `docs/superpowers/plans/2026-09-18-remaining-work.md`.

Live TV phase 1 (done on branch `livetv`, 2026-10-07): the two CRDs,
`pkg/iptv`, the controllers, the tuner and relay in the manager, blocks and
ports, the installers and scenario 19 (written, not run); see **Live TV**
above. Reconciles against envtest; not proven end to end.

Manager/agent split (2026-10-06/07, as built;
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`, ADR-0018,
ADR-0017): `cmd/clustarr` and `clustarr all` are gone. `cmd/manager` runs every
reconciler under one lease (`manager.clustarr.io`) and one cache, registered by
`app/<svc>/manager`; `cmd/agent --domain <d>` runs one worker domain per process,
registered by `app/<svc>/agent`; `cmd/ui`, `cmd/markers` and `cmd/transcode`
stand alone (`internal/cli` holds the command trees, `make run-dev` the local
processes). Two images replace the controller, media and transcoder images:
`clustarr` (distroless static: manager, ui) and `native` (FROM scratch: agent,
markers, transcode), every Go binary UPX-packed (owner, 2026-10-07). Agents
scale on HPAs the manager owns, driven by `clustarr_consumer_lag` from its own
External Metrics API (`app/autoscale`, `docs/autoscaling.md`), and KEDA is
gone. The manager alone ensures the NATS topology; subscriptions bind, never
create, and fetch only for free slots (Waves 4c, 4f). Probing, segment
decoding and embedded subtitle extraction run in-process on ffgo
(`pkg/mediainfo/native`, `pkg/segments/decode`, `embedded/native`, behind
`pkg/ffruntime`); no image ships ffmpeg, ffprobe or par2. Not yet: par2 as a
library (Wave 9 waits on a par2go release Go's module tooling accepts, so the
usenet engine still execs `par2` and a set that needs repair fails as a local
fault in the native image); tests that run no ffmpeg (Wave 6b's
`internal/testmedia` and goldens and W10.9's guard are in the test batch); and
`go.mod` replaces ffgo with `../ffgo-unify` until the owner OKs pushing tag
`v0.0.0-clustarr.13` (OD18).

MediaFile remediation loop (ADR-0016, 2026-10-06/07;
`docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md`):
SubtitleRequest, TranscodeJob and AudioGraft fold into MediaFile
`status.subtitles`, `status.transcode` and `status.graft`, planned and written
by one remediation loop in the manager (`app/remediation`, controller
`mediafile`, file and item keys) that replaces the MediaFile, Movie, Episode,
Album, Book, Audiobook, Issue, Series, Comic, rename, TranscodeJob, AudioGraft
and SubtitleRequest controllers; workers answer through records (`pkg/records`,
Durable KV); transcode and graft admission is one ledger
(`transcode-admission`); the `transcode-results` and `catalog-segments-result`
durables and the `catalog-markers` and caption-worker field managers are gone.

Agents never write Kubernetes (ADR-0019, waves A1-A7, 2026-10-07;
`docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`): the
manager is the control plane -- admission and routing (`app/dispatch`, agent
presence in `pkg/presence`), the intake of candidates, scan observations and
task advisories (`app/intake`) -- and the only writer of every CR and Secret.
The Download kind is deleted: a grab is an entry in its owner's
`status.downloads[]`, run by the item key's grab and downloads stages
(`app/catalog/grabplan`, `app/grab/lifecycle`), carried out by seq-fenced
engine commands (`CLUSTARR_WORK_ENGINE`) and reported in transfer and engine
records; imports are two-phase (inspect, execute) with the manager
materialising MediaFiles (`app/import/importplan`); the blocklist is a table in
the release index (`clustarr.rpc.index.blocklist`); the grab leases, the pending
KV and the redownload consumer are gone. Scans report observations the
manager's scan applier writes; import lists report snapshots the ImportList
controller applies; the index agent reports health outcomes and KV sessions the
Indexer controller turns into status and Secrets; the metadata gateway answers
in `clustarr-item-metadata`, and `status.artwork` and `status.overlay` are
rendered from the artwork objects' metadata.

Wave U (2026-10-07; `docs/superpowers/specs/2026-10-07-unbranded-names-design.md`):
every internal *arr name became its domain name, cold turkey (the transcode
service's packages moved to `app/transcode`), and `make brand-gate` holds it; the apply
configurations moved to `api/applyconfiguration/<group>/<version>`
(`hack/flatten-applyconfiguration.sh`, run by `make generate`). Wave N
(ADR-0020, ADR-0021): `busconn.Shutdown` drains every binary's bus last,
pending limits on every core subscription, `clustarr_nats_dropped_messages`
and the `ClustarrNATSSlowConsumer` PrometheusRule, `ClassifyRequestError` at
every RPC caller (no responders reads as the domain's `NoAgent`), a chunk size
per object store and the manager's orphan purge over every store, reconnect
backoff with jitter, a `NATS_URL` pool and a named reconnect buffer, and
fingerprint metadata. Wave S (2026-10-07): series and season search, banded
interactive results and pack preference (the UI section above).

Cold turkey (owner, 2026-10-07): one release and no migration code -- no
adoption of the four removed kinds, no legacy names, no rollback tooling; 27
CRDs remain. A running 0.4.x install moves over once, by hand
(`docs/runbooks/unify-cold-cutover.md`), with the owner's OK. Everything since
the split is **built, not deployed**, and was built implementation-first: the
tests and guards each wave named are written by the test batch
(`.superpowers/unify/progress.md` lists them), and until then `go vet ./...`
fails on the test packages the waves broke.

Next: the unified topology ADR-0013 deferred is built (ADR-0018 supersedes
ADR-0013; the 2026-09-24 unified-manager design is superseded by the split
design): land reconcilers in their service's `app/<svc>/manager` package and
workers in its `app/<svc>/agent` domain, never in a per-service Deployment.
The **test batch** comes first: it writes every deferred test and guard, then
runs `make test` and `make lint` green. Then Wave 9 (par2go in a re-exec'd child
of the agent) once a par2go release can be required, and the cold cutover of
kind-cluster-plex with the owner's OK. Then **Phase H: end-to-end proof on
kind**. Only Phase C's scenarios (5, 7 and 8) have ever run on kind; every
scenario written since — 1-4, 6, 9-18 — never has. Phase H runs them, builds the
Helm deploy path scenario 16 needs, extends scenario 1's trace check across the
manager and its agents, and owns the open items left under "Carried defects" in
`docs/superpowers/plans/2026-09-18-remaining-work.md`. Milestone detail is in
the spec's §16 and amendment §A4.

**Nothing is finished until it is proven end to end on a kind cluster.** Every
phase from C onward lands its milestone's scenarios in `test/e2e` (real CRs,
real controllers, real NATS, real files under `/data`, in-cluster fixture
services, no Internet) and keeps `hack/e2e.sh` green; Phase H audits that every
scenario in the plan exists and passes. Unit, envtest and build-tagged
integration suites do not substitute for it.
