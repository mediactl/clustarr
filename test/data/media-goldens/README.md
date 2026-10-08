# Media goldens (W6.23)

Recorded 2026-10-07, once, from the exec implementations before their deletion
(upgrade guide U2.3). **Do not re-record**: the implementations are gone, and the
in-process replacements are what these files check.

| Kind | Recorded from | At | Files |
| --- | --- | --- | --- |
| `decode` | `pkg/segments/decode`'s exec decoder (`ffmpeg(1)`) over W7.6's windows | `08cf7fe3^` (`e7e05be6`) | `decode/<clip>.json` |
| `probe` | `pkg/mediainfo/ffprobeexec.Prober` (`ffprobe(1)`): `Probe` and `ProbeAudio` | `5b8d3b00^` (`e26177a3`) | `probe/<clip>.json` |
| `extract` | `pkg/subtitles/providers/embedded/execextract` (`ffmpeg -c:s srt`) | `55c18417^` (`cb781329`) | `extract/<clip>.s<stream>.srt` |

Each recorder ran from a throwaway worktree at that commit with the pinned
`make native-assets` CLIs on `PATH`: FFmpeg `n9.0.2-17-g2a571b6068-20260930`,
libavcodec 63.1.102 -- the build ffgo (`v0.0.0-clustarr.13`) loads in the parity
tests. The recorders were not committed: the last of the implementations they
call is deleted.

- `MANIFEST.json` lists every clip: its name, module-relative path, kinds, sha256
  and the `testmedia.Spec` it was made from. `test/goldens` reads it.
- `clips/` are made by `internal/testmedia` on ffgo from `test/goldens.ClipSpecs`
  (W7.6's seven decode clips, W8.4's probe parity fixtures and the in-process
  probe's, W8.8's extraction fixtures); the three `test/data/mediainfo` files are
  referenced in place.
- In `probe/*.json` the clip's absolute path (ffprobe's `format.filename`, and the
  error strings) reads `<path>`. `raw` is the whole `mediainfo.Raw` but
  `FrameErr`, which is `frameError`.
- `decode/*.json`: audio windows at 0, 12.05 and 12.5 s for 10 s (sha256 of the
  little-endian int16 samples, and their chromaprint), the gray frames from 15 s,
  and an RGB frame at 1, 7.3, 12.05, 20.5 and 29 s (sha256 each).

Not recorded: W8.4's "flac 24-bit" clip (ffgo opens the FLAC encoder in s16 only;
`audio_flac_24bit.flac` stands in) and W8.8's WebVTT and `mov_text` streams
(ffgo's muxer writes SubRip and ASS text streams only).
