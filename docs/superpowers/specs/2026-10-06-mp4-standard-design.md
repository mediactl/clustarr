# One MP4 for every player: the transcode standard's new layout

Status: approved (2026-10-06); E-AC-3 kept as preferred, by the owner's
correction the same day.

## 1. Goal

Every transcoded file is a single `.mp4` that every Plex player, browsers
included, plays natively, with no server-side transcode:

- the video is HEVC tagged `hvc1`;
- each language has one or two audio tracks: a Dolby surround track
  (E-AC-3 or AC-3) plus an AAC 2.0 companion for a surround source, or AAC
  alone for mono or stereo;
- every subtitle is text that Plex shows everywhere.

The video pipeline stays the in-process ffgo engine (`pkg/transcode/engine`),
streaming packets into `ffgo.MuxerStream`s, one output file per run.

## 2. The owner's decisions (2026-10-06)

| Question | Decision |
|---|---|
| Container | MP4 always, `hvc1`, faststart. |
| Surround audio (more than 2 channels) | **E-AC-3 is kept, and preferred when the language has it** (owner's correction, 2026-10-06: MP4 carries it as `ec-3`, Atmos included). AC-3 is copied next. Any other surround codec (TrueHD, DTS, FLAC, PCM, multichannel AAC) becomes AC-3 5.1 at 640 kbps, a 7.1 source downmixed to 5.1. This replaces the first answer, "everything to AC-3", which would have re-encoded the library's 7,273 E-AC-3 tracks. |
| Companion | Every surround track gets an AAC 2.0 companion in the same language. |
| Mono or stereo audio | AAC only: copied when already AAC, else encoded. |
| Subtitles MP4 cannot hold | Sidecars. ASS/SSA are written as `.ass` beside the video, and their attached fonts are lost. |
| Image subtitles (PGS, DVD) | OCR'd to text and muxed as `mov_text`. Plex ignores external `.sup` files ([Plex support](https://support.plex.tv/articles/200471133-adding-local-subtitles-to-your-media/)). |
| Files already transcoded (HEVC in MKV) | Remuxed once to the new layout, without re-encoding the video. |

The library on 2026-10-06 (11,958 probed files):

- **Surround audio:** 7,273 E-AC-3 tracks and 937 AC-3, with 363 DTS, 30
  FLAC and 9 TrueHD.
- **Subtitles:** 8,450 files with SRT, 1,425 with ASS, 492 with PGS, 9 with
  DVD subtitles. 612 files carry attached fonts.
- **Image subtitle tracks by language:** 832 English, 137 Spanish, 143
  Chinese, 106 German, 91 French, 63 Japanese, then a long tail.
- **Video:** 4,111 files (6.0 TB) are already HEVC or AV1 in MKV, and 7,800
  (13.9 TB) are not yet HEVC.

## 3. Audio (`standard.planAudio`)

These rules apply per language, after the profile's `audio.languages`
filter (unchanged):

1. **The primary track** is never a commentary track. Among the
   language's surround tracks (more than 2 channels) it is the first
   E-AC-3 one, else the first AC-3 one, else the one with the most
   channels; with no surround track, the one with the most channels. The
   first such track wins a tie. A copyable Dolby track is chosen over one
   with more channels that would need encoding (TrueHD 7.1 beside an AC-3
   5.1 core: the AC-3 is copied).
2. **A primary with more than 2 channels** becomes two tracks, the AAC
   companion listed first and the Dolby track after it (the owner's
   correction, 2026-10-07; the default flag goes with the first):
   - **The Dolby surround track:** E-AC-3 copied as is, at any channel
     count (7.1 and Atmos included); AC-3 copied as is; anything else
     decoded and encoded with FFmpeg's `ac3` encoder to AC-3 5.1 at
     640 kbps, downmixed by channel position.
   - **AAC 2.0 at 160 kbps**, downmixed from the decoded primary. Chrome
     and Firefox decode neither AC-3 nor E-AC-3, so Plex Web needs it.
3. **A primary with 1 or 2 channels** becomes one AAC track at its own
   channel count: copied when AAC, otherwise encoded.
4. **Commentary tracks** become AAC 2.0, one each, flagged as comment.
5. **Other tracks in the same language are dropped.** "1 or 2 audio streams
   per language" leaves no room for alternative mixes.
6. **Default flag:** the default language's first track carries it, which
   for a surround language is its AAC companion (ruling R4 picks the
   language).

One decoded source feeding two encoders is new to the engine: a stage that
fans the decoded frames out to two resamplers and encoders (§6).

A grafted dub follows the same rules, except that it is always encoded:
its rate and segments change, so nothing of it can be copied. A surround
donor dub becomes AC-3 5.1 plus AAC 2.0; a stereo one becomes AAC 2.0.

## 4. Subtitles

| Source | In the MP4 | Beside it |
|---|---|---|
| SRT, WebVTT, `mov_text` | `mov_text`, converted (styling tags dropped) | none |
| ASS, SSA | none | `<stem>.<lang>[.forced].ass`, the stream copied out |
| PGS, DVD (VobSub) | `mov_text` from OCR (§5) | none |

- **Sidecar names** follow `pkg/subtitles.SidecarName`, so captionarr's
  sidecar scan and catalogarr's `status.sidecars` see them as subtitle
  sidecars.
- **The ASS styling stays, but its fonts are lost.** Plex renders such a
  sidecar with its own fonts, and some clients burn it in.
- **Attachments are dropped**, since MP4 cannot carry them. **Chapters are
  kept** as MP4 chapters.
- **Forced and SDH flags** are kept as `mov_text` dispositions and in the
  sidecar names.

### 4.1 The owner's correction: every subtitle beside the file (2026-10-06)

The owner asked for subtitle extraction inside the transcode's own pass,
into Plex's local-subtitle layout:

```
/Movies/Avatar (2009)/Avatar (2009).mkv
                      Avatar (2009).eng.srt
                      Avatar (2009).en.forced.ass
                      Avatar (2009).en.sdh.srt
                      Avatar (2009).de.srt
TV Shows/Show_Name/Season XX/Show_Name SxxEyy.[Language_Code].ext
```

`[Language_Code]` is ISO 639-1, or ISO 639-2/B where a language has no
two-letter code.

This replaces the table above:

- **Text subtitles become `.srt` sidecars:** SubRip, WebVTT, `mov_text` and
  plain text. Nothing is embedded as `mov_text`, and the MP4 carries no
  subtitle stream.
- **ASS and SSA become `.ass` sidecars,** as before.
- **The name** is `<stem>.<lang>[.forced|.sdh].<ext>`:
  - the language is `pkg/lang.Normalize`'s base, the 639-1 code where one
    exists, its region dropped;
  - a forced track, or one titled "Signs" or "Songs", takes `.forced`;
  - a hearing-impaired track takes `.sdh`;
  - a second track that would take a name already planned is dropped and
    recorded.
- **The ffgo equivalent of `ffmpeg -i input.mkv -map 0:s:0 output.srt`:**
  ffmpeg picks the `srt` muxer from the extension, and a SubRip track's
  packets reach it unchanged. In ffgo that is `NewMuxer(path, "srt")`,
  `AddCopyStream` with the stream's codec parameters, one `WritePacket`
  per packet as the transcode's demuxer reads them, then `WriteTrailer`.
  For WebVTT or `mov_text`, ffmpeg's CLI would decode and re-encode, and
  ffgo binds no subtitle encoder, so the engine writes SubRip itself from
  each packet's text, without markup.
- **Sidecars are written as parts beside the output** and placed at their
  final names before the video swap. An existing file at a final name is
  kept.

## 5. OCR of image subtitles (`pkg/subtitles/ocr`)

- **Models:** PaddleOCR's recognition models (PP-OCR rec, Apache 2.0) for
  Latin, Chinese, Japanese and Korean scripts, run on ONNX Runtime through
  purego, as `pkg/segments/textdet` runs PaddleOCR's detection model. The
  four scripts cover 1,200 of the library's ~1,300 image subtitle tracks.
  Tracks in other scripts (Greek, Thai, Arabic, Hindi, Cyrillic, ~40 tracks)
  are dropped, and the file's transcode records which were.
- **Pipeline:**
  1. Decode the subtitle stream's bitmaps with timings (ffgo's subtitle
     decoder).
  2. Binarise each bitmap from its palette's alpha.
  3. Split it into lines by horizontal projection.
  4. Recognise each line with the track language's model, with greedy CTC
     decoding over the model's dictionary.
  5. Keep lines above a confidence floor and join them into one cue per
     bitmap.
  6. Encode the cues to `mov_text`.
- **Where it runs:** in the transcode run itself, in `squasharr-worker`. The
  transcoder image gains `libonnxruntime.so` and the four models, about
  40 MB.
- **Its cost** is a few seconds per subtitle track on CPU.
- **A track whose OCR fails or reads mostly nothing** is dropped and
  recorded, never mangled into the file.

## 6. Engine (`pkg/transcode/engine`)

These need ffgo capabilities to be confirmed in the plan's first task (a
spike against ffgo `v0.0.0-clustarr.11`):

- **Fan-out audio:** one decoder feeding an AC-3 encoder and an AAC encoder
  through their own resamplers. ffgo's `AudioEncoder` takes an encoder name;
  FFmpeg's native `ac3` encoder takes FLTP.
- **Text subtitles to `mov_text`:** decode, then encode with the `mov_text`
  encoder. Subtitle encoding is not used by the engine today.
- **ASS out to a sidecar:** a copy mux into FFmpeg's `ass` muxer, one small
  file per track.
- **PGS and DVD bitmaps out of the decoder,** for §5.
- **MP4 with `hvc1`, faststart and `use_metadata_tags`** already works.

If ffgo lacks one of these, the gap is closed in the fork (a new
`v0.0.0-clustarr.N` tag), as earlier engine work did.

## 7. Planning (`pkg/transcode/standard`)

- **`standard.Plan`** always outputs MP4, whatever the profile's
  `container`, which the plan ignores and the CRD documents as ignored.
- **`standard.Version` is raised,** so untranscoded files are planned under
  the new layout.
- **The plan's audio list, subtitle actions, sidecar list and OCR list**
  are part of the plan and its hash.
- **The output name** is `<stem>.mp4`. The swap is already a container
  change (gap fix R-11): the source is retired and the MediaFile follows the
  new path.
- **Sidecars are placed beside the output** before the swap. The verify step
  checks their count and that they are non-empty.

## 8. The remux of already-transcoded files

- **What's in scope:** a file that is `Transcoded()` (by squasharr, or
  elsewhere: Tdarr's HEVC) but not in the new layout. Its probe reads a
  container other than MP4, or audio that does not follow §3.
- **The plan** is the existing remux decision (`DecisionCopyVideo`): the
  video is copied, and the audio and subtitles follow §3 and §4.
- **"A transcoded file is final" still holds for the video.** It is never
  re-encoded and never upgraded automatically. The layout is converted once.
- **The remux is idempotent:** an MP4 in the new layout is never remuxed
  again.
- **The TranscodeProfile controller selects these files.** They queue
  through the same job window (`--job-window`) and pools; a remux takes a
  CPU slot.
- **The volume** is 4,111 files and 6.0 TB rewritten at copy speed, bounded
  by the window.

## 9. What changes downstream

- **catalogarr** follows the new path (R-11) and re-probes. The probe hash
  changes, so captionarr, segment detection and TheIntroDB markers see a new
  file. Markers are re-fetched and segments re-analysed once per file.
- **Plex** sees the file at `<stem>.mp4` and rescans. Watched state belongs
  to the metadata item, not the file. Existing `<stem>.<lang>.srt` sidecars
  keep matching, since the stem does not change.
- **`naming.renameTranscoded`** is unaffected: the stem stays.
- **Audio grafts** on an MP4 target work as they do on an MKV one.
  `engine.CopyPlan` already writes MP4.
- **CLAUDE.md's transcoding section** is rewritten for the new standard. Its
  "every subtitle, attachment and chapter kept" no longer holds.

## 10. Phases

Each phase gets its own plan, gate and deploy, as the anime phases did.

1. **MP4 layout:**
   - the container;
   - the audio rules, with the fan-out stage;
   - text subtitles to `mov_text`;
   - ASS sidecars, and dropping attachments;
   - `standard.Version` raised.

   Files with image subtitles are held back until phase 2: their job is
   Skipped with `standard.HoldImageSubtitles`, not Planned, since a Planned
   job counts toward the 32-job window (ruling R1). Phase 2 raises
   `standard.Version`, so they are planned again.
2. **OCR** of PGS and DVD subtitles (§5), releasing the held files.
3. **The remux** of already-transcoded files (§8).

## 11. Risks

- **ASS without its fonts.** Anime typesetting (signs, karaoke) degrades in
  Plex. This is the owner's accepted cost.
- **OCR errors** in recognised subtitles: a wrong word, italic markers lost.
  A track that reads poorly is dropped rather than kept.
- **AC-3 from TrueHD or DTS** loses their lossless audio and any Atmos or
  DTS:X objects. E-AC-3 keeps its Atmos, since it is copied.
- **AC-3 from multichannel AAC, or AAC 2.0 from a lossy source,** is a
  lossy-to-lossy re-encode.
- **The I/O:** 6 TB of remux and 14 TB of transcodes rewrite the library
  over weeks, bounded by the job window.
- **ffgo gaps** (§6) may need fork releases before phase 1 can finish.

## As built (phase 1, 2026-10-07)

Plan `docs/superpowers/plans/2026-10-06-mp4-standard-phase1.md`. Its
rulings:

- **R1.** A file with an image subtitle is Skipped, not Planned, with
  `HoldImageSubtitles`.
- **R2.** A forced track, or one titled "Signs" or "Songs", takes `.forced`
  in its sidecar name.
- **R3.** One sidecar per name: a second track that would take a name
  already planned is dropped and recorded in `Result.Dropped`. An existing
  file at a final name is kept.
- **R4.** The source's default language keeps the default.
- **R5.** Encoded tracks are titled "Dolby Digital 5.1", "Stereo" or
  "Mono". On MP4 a track's title is written as its handler name, since
  FFmpeg's MP4 muxer writes no track title.
- **R6** (`mov_text` written by the engine) was withdrawn with Task 6, when
  the owner moved every subtitle beside the file (§4.1).

What was built:

- **ffgo** `v0.0.0-clustarr.12`: packets from bytes, plus codec-parameter
  setters (unused since §4.1).
- **`standard.Plan`** (Version 2):
  - always MP4;
  - the audio rules (`planAudio`);
  - every subtitle as a `SidecarPlan`, named by `pkg/lang.Normalize`'s base;
  - the image-subtitle hold.
- **The engine:**
  - one audio decode feeds several encoders, and a stream can be copied and
    encoded at once;
  - every track's flags come from the plan, and `CopyPlan` carries them;
  - sidecar sinks: the `ass` muxer, the `srt` muxer (ffgo's
    `-map 0:s:N out.srt`) and a Go SubRip writer;
  - a surround dub grafted as AC-3 5.1 plus AAC 2.0.
- **The worker:**
  - `OutputContainer`;
  - sidecar parts verified, then placed before the swap and removed with a
    failed attempt;
  - the profile hash at Version 2 ignores the container.
- **The CRD:** `container` defaults to `mp4` and is documented as ignored.
