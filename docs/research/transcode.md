# Clustarr research note: ffmpeg HEVC 10-bit pipeline + distributed transcoding

Date: 2026-09-18. Author: research subagent (pre-design). Everything marked **[verified]** was run or read from source on the dev box (ffmpeg n9.0.1, x265 4.3, NVENC API 13.1 on an RTX 2070 Max-Q, 12 CPUs, 62 GB RAM). Everything marked **[source]** was read from the FFmpeg 9.0 / x265 source tree or vendor docs. Opinions are marked **[opinion]**.

Scratch artifacts referenced here live under
`/tmp/claude-1000/-home-appkins-src-mediactl-clustarr/55489e0d-8512-4ab3-bc91-253f47ae031c/scratchpad/` (`chunk/`, `bench/`, `src/`, `*.txt` option dumps, `probe.json`).

---

## 0. TL;DR for the architects

1. **Target format**: MKV, HEVC Main 10 (`yuv420p10le`), CRF-driven `libx265 -preset slow` for archival quality/size; hardware encoders (NVENC/QSV/VAAPI) as an *opt-in throughput tier*, never for Dolby Vision sources. AAC-LC via FFmpeg's native `aac` encoder (libfdk_aac is `nonfree` and not shippable in container images).
2. **Detection**: one `ffprobe -print_format json -show_format -show_streams -show_chapters` call plus one `-show_frames -read_intervals "%+#1"` call (HDR static metadata lives in frame side data, DV config lives in stream side data). Skip when video is already `hevc/Main 10` and all audio is `aac` (or an allowed passthrough codec), or when the file carries our own `CLUSTARR_PROFILE` tag.
3. **HDR**: HDR10 static metadata passes through automatically to libx265 (FFmpeg >= 7.0) and hevc_nvenc (>= 7.1) **[verified on 9.0.1]**, but Clustarr should still pass explicit `master-display`/`max-cll`/color params from the probe (belt and braces). HDR10+ dynamic metadata is **dropped** by the libx265 wrapper **[source]** unless you extract it with `hdr10plus_tool` and pass `dhdr10-info=`. Dolby Vision: profiles 5/8.1/8.2/8.4 pass through with `libx265` but **require VBV (`-maxrate/-bufsize`)**; profile 7 (UHD Blu-ray, dual layer) is silently downgraded to HDR10 by FFmpeg's auto mode **[source]**.
4. **Chunked (segment-parallel) encoding works with stock ffmpeg** and is verified end to end here: stream-copy split at source keyframes with the `segment` muxer, encode chunks with closed GOP, concat with the `concat` demuxer, encode audio once, mux at the end. Frame count and DTS monotonicity verified exact (720/720 frames, VMAF 92 vs source at CRF 28 ultrafast). Constraints: CRF only (no cross-chunk 2-pass), chunk starts must be keyframes, audio never chunked, HDR10+ not chunkable without offsetting the JSON.
5. **K8s model**: one `TranscodeJob` CR -> one `batch/v1 Job` (whole file) or one **Indexed Job** (`completionMode: Indexed`, one index per chunk, `backoffLimitPerIndex`) plus a concat/verify Job. GPU tier via `nvidia.com/gpu` / `gpu.intel.com/i915|xe` limits and GFD/NFD node labels; consumer NVENC is capped at 8 sessions per GPU. Scale on the CR queue from the controller (or Kueue quotas); KEDA `nats-jetstream` ScaledJob is the alternative if workers are plain queue consumers.
6. **Threads in containers**: x265 sizes its pool from `sysconf(_SC_NPROCESSORS_ONLN)` (host CPU count, not the cgroup quota) **[source]** — always pass `pools=<cpu limit>` via the Downward API or the pod will be CFS-throttled.

---

## 1. Dev-box environment (verified)

```
ffmpeg n9.0.1 (libavcodec 63.1.101), built with --enable-gpl --enable-version3, libx265 (x265 4.3, 8bit+10bit+12bit),
nvenc/nvdec, libvpl (QSV), vaapi, vulkan, amf, libvmaf, libopus, libsvtav1, libplacebo. NO libfdk_aac (nonfree).
HEVC encoders: libx265 hevc_amf hevc_nvenc hevc_qsv hevc_v4l2m2m hevc_vaapi hevc_vulkan
Audio encoders: aac (native), libopus, opus (native)
hwaccels: vdpau cuda vaapi qsv drm opencl vulkan amf
Bitstream filters of interest: dovi_rpu, dovi_split, hevc_metadata, hevc_mp4toannexb, extract_extradata
Tools NOT installed: mkvmerge, mkvpropedit, mediainfo, dovi_tool, hdr10plus_tool, HandBrakeCLI, vainfo
GPU: NVIDIA GeForce RTX 2070 with Max-Q (Turing SM 7.5), NVENC API 13.1 loads. /dev/dri/renderD128+129 exist but VAAPI init fails (NVIDIA-only box).
```

FFmpeg changelog items that matter **[source: Changelog]**:
- 7.0: "HDR10 metadata passthrough when encoding with libx264, libx265, and libsvtav1"; ffmpeg CLI now fully parallel (demux/decode/filter/encode/mux threads); `-bsf` usable on inputs.
- 7.1: "Mastering Display and Content Light Level metadata support in hevc_nvenc and av1_nvenc"; Vulkan H.265 encoder; MV-HEVC decoding.
- 8.0: libx265 alpha layer encoding; HDR10+ passthrough for libaom-av1 (not x265).
- 9.0: nothing HEVC/HDR-relevant beyond AMF HDR conversions.

---

## 2. Probe: detecting the current state

### 2.1 Commands

```bash
# Container + streams + chapters (one call, no decode)
ffprobe -v error -print_format json -show_format -show_streams -show_chapters "$IN"

# HDR static metadata + real colour tags come from the first decoded frame
ffprobe -v error -print_format json -select_streams v:0 -show_frames -read_intervals "%+#1" \
  -show_entries frame=pix_fmt,color_primaries,color_transfer,color_space,color_range,side_data_list "$IN"

# Exact video packet count without decoding (MKV has no nb_frames)
ffprobe -v error -select_streams v:0 -count_packets -show_entries stream=nb_read_packets,r_frame_rate -of json "$IN"

# Keyframe timestamps (for chunk planning) — decode-free
ffprobe -v error -select_streams v:0 -skip_frame nokey -show_entries frame=pts_time -of csv=p=0 "$IN"
```

### 2.2 JSON shape **[verified, ffprobe 9.0.1]**

Stream-level fields Clustarr needs (video): `index, codec_name ("hevc"), profile ("Main 10"), codec_type, width, height, coded_width/height, has_b_frames, sample_aspect_ratio, display_aspect_ratio, pix_fmt ("yuv420p10le"), level (63 = 2.1 ... 153 = 5.1), color_range ("tv"), color_space ("bt2020nc"), color_transfer ("smpte2084" | "arib-std-b67" | "bt709"), color_primaries ("bt2020"), chroma_location, field_order ("progressive"|"tt"|"bb"...), r_frame_rate ("24/1"), avg_frame_rate, time_base ("1/1000" in MKV), start_pts, start_time, extradata_size, disposition{default,dub,original,comment,forced,hearing_impaired,visual_impaired,attached_pic,captions,...}, tags{language, title, ENCODER, DURATION ("00:00:03.000000000")}`.

Audio: `codec_name ("aac"|"eac3"|"truehd"|"dts"|"flac"|"opus"), profile ("LC"|"DTS-HD MA"...), sample_fmt, sample_rate ("48000"), channels (6), channel_layout ("5.1(side)"), bits_per_sample, bits_per_raw_sample, initial_padding, mime_codec_string ("mp4a.40.2"), disposition, tags{language,title}`.

Subtitles: `codec_name ("subrip"|"ass"|"hdmv_pgs_subtitle"|"dvd_subtitle"|"dvb_subtitle"|"webvtt"|"mov_text"), disposition{forced,hearing_impaired}, tags{language,title}`. Attachments: `codec_type: "attachment"`, `tags{filename,mimetype}`.

Format: `format_name ("matroska,webm"|"mov,mp4,m4a,3gp,3g2,mj2"), duration ("30.023000"), size, bit_rate, tags{title,ENCODER,...}`. Chapters: `id, time_base, start_time, end_time, tags{title}`.

Side data names printed as `side_data_type` **[source: libavutil/side_data.c, libavcodec/packet.c, fftools/ffprobe.c]**:

| Where | `side_data_type` string | Fields |
|---|---|---|
| frame (and MKV stream) | `Mastering display metadata` | `red_x,red_y,green_x,green_y,blue_x,blue_y,white_point_x,white_point_y` as rationals over 50000; `min_luminance,max_luminance` over 10000 |
| frame (and stream) | `Content light level metadata` | `max_content` (MaxCLL), `max_average` (MaxFALL) |
| stream | `DOVI configuration record` | `dv_version_major, dv_version_minor, dv_profile, dv_level, rpu_present_flag, el_present_flag, bl_present_flag, dv_bl_signal_compatibility_id, dv_md_compression` |
| frame | `Dolby Vision RPU Data` / `Dolby Vision Metadata` | raw RPU / parsed metadata (presence is what matters) |
| frame | `HDR Dynamic Metadata SMPTE2094-40 (HDR10+)` | `application version, num_windows, ...` |
| packet/stream | `HDR10+ Dynamic Metadata (SMPTE 2094-40)` | container-level HDR10+ |

Verified first-frame output for an x265-encoded HDR10 clip:

```json
{"side_data_type":"Mastering display metadata","red_x":"34000/50000","red_y":"16000/50000","green_x":"13250/50000","green_y":"34500/50000","blue_x":"7500/50000","blue_y":"3000/50000","white_point_x":"15635/50000","white_point_y":"16450/50000","min_luminance":"1/10000","max_luminance":"10000000/10000"},
{"side_data_type":"Content light level metadata","max_content":1000,"max_average":400}
```

Converting back to x265 syntax (this is exactly what `libavcodec/libx265.c:handle_mdcv` does **[source]**): `master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)` and `max-cll=1000,400`. Note x265 wants G,B,R order and luminance as (max,min) in 0.0001 cd/m2.

### 2.3 Deriving `HDRFormat`

```
if DOVI configuration record present            -> DolbyVision (profile = dv_profile, compat = dv_bl_signal_compatibility_id)
elif frame has "HDR Dynamic Metadata SMPTE2094-40" -> HDR10Plus (static metadata also present)
elif color_transfer == smpte2084                 -> HDR10 (PQ; master display may be absent -> still HDR10)
elif color_transfer == arib-std-b67              -> HLG
else                                             -> SDR
```

### 2.4 Skip / remux decision (Plan output kinds)

```
AlreadyCompliant : v0.codec=="hevc" && v0.profile=="Main 10" && v0.pix_fmt in {yuv420p10le} && all kept audio codecs allowed by profile
                   && container == profile.container  (or tags.CLUSTARR_PROFILE == profile.hash)   -> no-op
RemuxOnly        : video compliant, audio/subs/container not                                       -> -c:v copy, transcode audio, remux
FullTranscode    : otherwise
Reject           : DV profile 5 when policy=passthrough and HW encoder selected; interlaced+DV; unsupported pix_fmt (yuv422p10 from ProRes is fine, it is converted)
```

`ENCODER` tag is overwritten by lavf on every write; use a custom tag `-metadata CLUSTARR_PROFILE=<name>@<hash>` (MKV keeps arbitrary tags) and check it on the next scan.

---

## 3. Video: libx265 (the archival path)

### 3.1 Wrapper options **[verified: `ffmpeg -h encoder=libx265`]**

`-crf <float>`, `-qp`, `-forced-idr`, `-preset`, `-tune`, `-profile`, `-x265-stats`, `-udu_sei`, `-a53cc`, `-x265-params k=v:k=v`, `-dolbyvision auto|0|1` (default auto). Supported pix fmts include `yuv420p10le` (use this), `yuv422p10le`, `yuv444p10le`, `yuv420p12le`, `gray10le`, `yuva420p10le` (alpha, x265 >= 4.x).

Generic options mapped by the wrapper **[source: libx265.c]**: `-g` -> `keyint`, `-keyint_min` -> `min-keyint`, `-bf` -> `bframes`, `-flags +cgop` -> `no-open-gop`, `-maxrate/-bufsize` -> `vbv-maxrate/vbv-bufsize` (kbit), `-force_key_frames` honoured (IDR if `-forced-idr 1`).

Static HDR metadata: `handle_side_data()` copies `AV_FRAME_DATA_CONTENT_LIGHT_LEVEL` -> `maxCLL/maxFALL` and `AV_FRAME_DATA_MASTERING_DISPLAY_METADATA` -> `master-display` from `avctx->decoded_side_data` (stream-global side data the CLI hands to the encoder) **[source]**. HDR10+ (`AV_FRAME_DATA_DYNAMIC_HDR_PLUS`) is **not referenced anywhere in libx265.c** -> dropped **[source]**.

### 3.2 x265 4.3 defaults that matter **[source: x265 docs + `x265 --fullhelp`]**

`crf 28`, `preset medium`, `aq-mode 2` (auto-variance), `aq-strength 1.0`, `psy-rd 2.0`, `psy-rdoq 0` (1.0 at slow/slower/veryslow), `bframes 4`, `b-adapt 2`, `ref 3`, `rd 3`, `rc-lookahead 20`, `cutree on`, `sao on`, `open-gop on`, `keyint 250`, `min-keyint auto`, `scenecut 40`, `qcomp 0.6`, `high-tier on`, `repeat-headers off`, `hdr10 off` ("if max-cll or master-display has non-zero values, this is enabled"), `hdr10-opt off`, `cll on`. `--tune` values: `psnr, ssim, grain, zerolatency, fastdecode, animation`. `--dolby-vision-profile`: help text says 5/8.1/8.2 but `param.cpp` accepts `50, 81, 82, 84` **[source]**.

### 3.3 Opinionated Clustarr defaults **[opinion, synthesised from x265 docs, codecalamity, silentaperture, ffmpeg.party, xcodecpack guides]**

Space-optimised but "transparent enough" for a media library. CRF is per resolution class of the *output*; x265 10-bit CRF numbers run ~1-2 lower than 8-bit for equal size.

| Output class | CRF | preset | extra x265-params |
|---|---|---|---|
| <= 720p (SD/HD-ready) | 21 | slow | `aq-mode=3` |
| 1080p SDR | 22 | slow | `aq-mode=3` |
| 2160p SDR | 23 | slow | `aq-mode=3` |
| 2160p / 1080p HDR10, HDR10+, HLG, DV | 22 | slow | `hdr10=1:hdr10-opt=1:repeat-headers=1` (no aq-mode 3: it biases dark SDR scenes) |
| tune overrides | animation: `-tune animation`, CRF -1 | grain: `-tune grain`, CRF +1 | |

Common to all: `-pix_fmt yuv420p10le -profile:v main10 -flags +cgop` (`no-open-gop`, required for clean chunk concat and preferred by most players/BD specs), `keyint = 10 x fps` (`-g 240` for 24 fps; x265 default 250), `min-keyint = fps`, `bframes=8`, `ref=4`, `rc-lookahead=40`, keep `psy-rd 2.0`, `sao` default on (turn off only for grain tune), `pools=<cpu limit>`, `frame-threads=0` (auto), `log-level=none` (keep ffmpeg stderr clean for error capture).

Guide ranges for reference: general 1080p CRF 25-29 / 4K 26-32 (consumer); HDR/10-bit 22-26; "archival" 18-22 (codecalamity: 16-20 for 4K HDR with slow/medium; 22-24 for heavy grain; 16-18 animation). Enthusiast guide (silentaperture): `veryslow/slower, aq-mode 3|4, aq-strength 0.8-1.4, psy-rd 0.8-2.0, psy-rdoq 0-2, bframes 16, ref 4-6, rd 3-4, rc-lookahead 60, no-cutree, deblock -4:-4..0:0, no-sao, no-open-gop` — too slow for a fleet; keep as an optional "quality" profile.

### 3.4 Reference command (HDR10 source, MKV, keep everything sensible)

```bash
ffmpeg -hide_banner -nostdin -y -nostats -loglevel error -progress pipe:1 -stats_period 1 \
  -i "$IN" \
  -map 0:v:0 -map 0:a:0 -map 0:a:2 -map 0:s? -map 0:t? -map_metadata 0 -map_chapters 0 \
  -c:v libx265 -preset slow -crf 22 -pix_fmt yuv420p10le -profile:v main10 -flags +cgop -g 240 -keyint_min 24 -bf 8 \
  -x265-params "pools=8:frame-threads=0:ref=4:rc-lookahead=40:hdr10=1:hdr10-opt=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:range=limited:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400:log-level=none" \
  -c:a:0 aac -b:a:0 384k -metadata:s:a:0 language=eng \
  -c:a:1 copy \
  -c:s copy -c:t copy \
  -metadata CLUSTARR_PROFILE="hevc10-aac@$HASH" \
  -f matroska "$OUT.part.mkv"
```

Pitfall **[verified]**: passing `-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc` on the CLI did *not* tag the output (only `color_space` came through) and x265 printed "Recommended Settings for HDR10-opt ... Disabling hdr10-opt" when the frames lacked tags (synthetic source). Tagging the frames with `-vf setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv` **and** passing `colorprim/transfer/colormatrix` in `-x265-params` produced a correctly tagged Main 10 stream with MDCV+CLL side data. Real HDR sources already carry the tags, but the Plan should always emit both to be immune to untagged sources.

Interlaced sources (`field_order != progressive`): add `-vf bwdif=mode=send_frame` before encoding (x265 field coding is not worth it). Never use `-r`; keep `-fps_mode passthrough` (default for MKV) so VFR sources keep timestamps.

### 3.5 Dolby Vision through libx265 **[source: libavcodec/dovi_rpuenc.c, libx265.c, x265 param.cpp]**

Flow: hevc decoder exports RPU as frame side data -> `ff_dovi_configure()` decides profile/compat -> `x265 params.dolbyProfile = dv_profile*10 + dv_bl_signal_compatibility_id` -> per frame `ff_dovi_rpu_generate()` wraps the RPU NAL into `x265pic.rpu`.

`dovi_configure_ext()` rules:
- `-dolbyvision auto` (default) with no RPU header in the input -> DV disabled (plain encode).
- Source profile **4 or 7** (dual-layer, enhancement layer): auto -> **silently skip** (output is HDR10 base layer, RPU dropped); `-dolbyvision 1` -> error `Coding of Dolby Vision enhancement layers is currently unsupported` (AVERROR_PATCHWELCOME). The hevc decoder ignores the EL (`UNSPEC63` NALs) anyway.
- Profile 5 -> compat 0 (IPTPQc2; base layer is NOT watchable without RPU). Never strip DV from a P5 source; either keep it (x265 profile 50) or refuse.
- Profile 8 (and AV1 10) -> compat derived from colour tags: BT.2020+PQ -> **8.1**, BT.2020+HLG -> **8.4**, BT.709 -> **8.2**. Pixel format must be `yuv420p10` unless `-strict unofficial`.
- Writes `AV_PKT_DATA_DOVI_CONF` with `rpu_present_flag=1, el_present_flag=0, bl_present_flag=1, dv_level` from a pixels-per-second/width table; `dv_md_compression` from the encoder's `compression` setting (profile 8 compression "known to be unsupported by many devices" -> keep `none`; verify `dv_md_compression` in the output probe and, if needed, post-process with `-bsf:v dovi_rpu=compression=none`).

x265 side constraints (`x265_check_params`) **[source]**: `dolbyProfile` in {50,81,82,84}; **"Dolby Vision requires VBV settings to enable HRD"** -> you must pass `-maxrate`/`-bufsize` (e.g. `-maxrate 40M -bufsize 60M` for 4K, level 5.1 high tier) or x265 refuses to open; Main10 4:2:0 only; profile 8.1 additionally requires `master-display`.

Other DV pitfalls:
- Any scale/crop makes the RPU's L5 (active area) metadata wrong; DoviCrop exists for that. Policy: if the plan scales or crops and the source is DV -> `-dolbyvision 0` and treat as HDR10 (P8) or refuse (P5).
- No hardware encoder (nvenc/qsv/vaapi/vulkan/videotoolbox) writes RPUs -> DV becomes HDR10 (P8) or garbage (P5). Force libx265 for DV sources.
- To keep DV from a profile 7 disc rip you need `dovi_tool -m 2 convert --discard` (RPU -> 8.1, drop EL; MEL loses nothing, FEL residual is lost) before encoding; `dovi_tool` is Rust, easy to add to the worker image.
- `dovi_split` bsf (`mode=bl|bl_rpu|el|el_rpu`) can strip the EL from P7 streams without re-encoding; `dovi_rpu` bsf (`strip=1`) removes DV completely from a stream copy.
- MP4 output needs `-tag:v hvc1`; stick to MKV.

### 3.6 HDR10+ and HLG

- HDR10+ (SMPTE 2094-40) is dropped by libx265/nvenc/qsv/vaapi wrappers. To preserve: `ffmpeg -i in -c:v copy -bsf:v hevc_mp4toannexb -f hevc - | hdr10plus_tool -o meta.json -` then `-x265-params dhdr10-info=meta.json` (frame-indexed JSON; only valid for a whole-file encode, so chunking + HDR10+ = fall back to single job, or drop HDR10+ by policy).
- HLG needs nothing beyond correct colour tags (`transfer=arib-std-b67`); no static metadata required.

### 3.7 Threads, memory, speed **[verified on synthetic 1080p testsrc2, 120 frames, 12 CPUs]**

x265 `ThreadPool::getCpuCount()` returns `sysconf(_SC_NPROCESSORS_ONLN)` -> host CPUs, not cgroup quota. Set `pools=<limit>` (Downward API `resourceFieldRef: limits.cpu`).

| Encoder | fps | wall | peak RSS |
|---|---|---|---|
| libx265 slow CRF22 10-bit, pools=12 | 4.9 | 24.8 s | ~1.16 GB |
| libx265 slow, pools=4 | 7.3 (run order/cache effects; treat as noise) | 16.7 s | |
| libx265 medium, pools=12 | 19.2 | 6.4 s | |
| hevc_nvenc p6 fullres, RTX 2070 Max-Q (CPU decode) | 64 | 2.1 s | |

Synthetic content is not representative of real film (expect roughly 8-15 fps at 1080p slow on 12 real cores, 2-4 fps at 2160p). A 2 h 4K movie at 2 fps is ~24 h on one node -> this is why chunking (section 8) or the GPU tier matters. Memory: budget 2 GiB for 1080p, 6 GiB for 2160p x265 slow (4K uses ~4x the frame buffers; measure before pinning limits).

---

## 4. Hardware encoders **[verified option tables: scratchpad `nvenc.txt`, `qsv.txt`, `vaapi.txt`, `vulkan.txt`]**

When to prefer **[opinion]**: (a) backlog throughput (one RTX card does 4-8 simultaneous 1080p encodes at several hundred fps aggregate), (b) energy/cost per file, (c) 2160p where x265 slow is hours per file, (d) interactive "watch soon" priority. Prefer x265 for the final archival copy when size matters (HW HEVC at equal quality is typically 20-40 % larger than x265 slow; Intel's own positioning is "competitive with x264/x265 medium" per Jellyfin docs). Never for DV sources.

### 4.1 hevc_nvenc (NVIDIA)

Options: `-preset p1..p7` (default p4; legacy `slow|medium|fast` map to hq 2-pass/1-pass), `-tune hq|uhq|ll|ull|lossless` (default hq; `uhq` is Blackwell/SDK 13 and has an open bug report with `highbitdepth` in ffmpeg), `-profile main|main10|rext|mv`, `-tier main|high`, `-rc constqp|vbr|cbr`, `-cq 0-51`, `-multipass disabled|qres|fullres`, `-b_ref_mode disabled|each|middle`, `-rc-lookahead N`, `-spatial-aq 0|1`, `-temporal-aq 0|1`, `-aq-strength 1-15` (default 8), `-nonref_p`, `-weighted_pred`, `-highbitdepth` (10-bit from 8-bit input), `-tf_level`, `-lookahead_level`, `-unidir_b`, `-split_encode_mode`. Pixel formats: `nv12 p010le yuv444p ... cuda` (no `yuv420p10le`: use `p010le`).

Verified archival-style command (Turing):
```bash
ffmpeg -i in.mkv -map 0:v:0 -c:v hevc_nvenc -preset p6 -tune hq -rc vbr -cq 24 -b:v 0 -multipass fullres \
  -bf 3 -b_ref_mode middle -spatial-aq 1 -temporal-aq 1 -rc-lookahead 32 -profile:v main10 -tier high -pix_fmt p010le out.mkv
# -> hevc / Main 10 / yuv420p10le / bt2020+smpte2084, Mastering display + CLL side data preserved (FFmpeg >= 7.1)
```
Full-GPU chain (decode on NVDEC, scale on GPU, no CPU copies) **[verified]**:
```bash
ffmpeg -hwaccel cuda -hwaccel_output_format cuda -i in.mkv -vf "scale_cuda=w=1920:h=-2:format=p010le" -c:v hevc_nvenc ... out.mkv
```
Session limits: consumer GeForce = **8 concurrent NVENC sessions** per system (Linux driver >= 550.54.14; GTX 1630 = 3); professional cards unlimited. CQ 22-26 for HEVC 10-bit is the usual quality band; `-cq` + `-b:v 0` is the CRF analogue.

### 4.2 hevc_qsv (Intel, libvpl)

Options: `-preset veryslow(1)..veryfast(7)`, `-profile main|main10|mainsp|rext|scc`, `-scenario archive|livestreaming|...`, `-extbrc 0|1`, `-look_ahead_depth 0-100` (only with extbrc), `-low_power auto|0|1`, `-p_strategy`, `-b_strategy`, `-adaptive_i/-adaptive_b`, `-tier` (default high). Pixel formats `nv12 p010le p012le ... qsv`.

Rate control selection **[source: qsvenc.c]**: `-global_quality N` with no `-maxrate` -> **ICQ** (CRF analogue, 1-51); `-extbrc 1 -look_ahead_depth 40` adds lookahead. 
```bash
ffmpeg -init_hw_device qsv=hw -filter_hw_device hw -hwaccel qsv -hwaccel_output_format qsv -i in.mkv \
  -c:v hevc_qsv -preset veryslow -global_quality 22 -extbrc 1 -look_ahead_depth 40 -scenario archive -profile:v main10 out.mkv
# software-decode variant: drop the -hwaccel flags and add -pix_fmt p010le
```
Platform notes (Jellyfin docs): use the `iHD` VA driver; HEVC 10-bit encode from Gen 9.5 (Kaby Lake) onwards; Arc A-series supports LP and non-LP encode, **Arc B-series (Battlemage) only low-power (VDENC) mode** and uses the `xe` kernel driver (kernel >= 6.12). Container needs `/dev/dri/renderD*` and membership of the `render` group (supplementalGroups).

### 4.3 hevc_vaapi (Intel/AMD, Mesa/iHD)

Options: `-rc_mode auto|CQP|CBR|VBR|ICQ|QVBR|AVBR`, `-qp 0-52`, `-profile main|main10|rext`, `-tier`, `-low_power`, `-idr_interval`, `-b_depth`, `-sei hdr+a53_cc` (default -> MDCV/CLL SEI written). Input must be `vaapi` frames:
```bash
ffmpeg -init_hw_device vaapi=va:/dev/dri/renderD128 -hwaccel vaapi -hwaccel_output_format vaapi -i in.mkv \
  -vf 'scale_vaapi=format=p010' -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -sei hdr out.mkv
```
AMD (RDNA2+ via Mesa) generally has CQP/VBR only; Intel has ICQ/QVBR. VAAPI is the vendor-neutral Linux path but exposes fewer quality knobs than QSV/NVENC.

### 4.4 hevc_vulkan (FFmpeg >= 7.1, vendor-neutral, experimental) — `-qp`, `-rc_mode auto|driver|cqp|cbr|vbr`, `-tune default|hq|ll|ull|lossless`, `-usage transcode|record`, `-profile main|main10`, `-tier`. Not recommended for production yet (no HDR SEI verified, driver maturity).

### 4.5 hevc_videotoolbox (macOS only; dev laptops, not K8s) — options from `videotoolboxenc.c`: `-profile`, `-alpha_quality`, `-constant_bit_rate`, `-prio_speed`, `-realtime`, `-allow_sw`, `-require_sw`, `-power_efficient`, `-max_ref_frames`, `-frames_before/-frames_after`, `-a53cc`; constant quality via `-q:v 1-100` on Apple Silicon; `-profile:v main10 -pix_fmt p010le`. hevc_amf (AMD AMF) exists in the build; on Linux it needs the AMD proprietary Vulkan/AMF stack — prefer VAAPI for AMD.

---

## 5. Audio

### 5.1 Native `aac` vs `libfdk_aac`

- Native `aac` (LC only): options `-aac_coder twoloop|fast` (default twoloop), `-aac_ms auto`, `-aac_is 1`, `-aac_pns 1`, `-aac_tns 1`, `-profile:a aac_low` (default), `-b:a`. Default bitrate when unset **[source: aacenc.c]**: 128 kbps per channel pair (CPE) + 69 kbps per single channel (SCE) + 16 kbps LFE -> stereo 128k, 5.1 = 341k, 7.1 = 469k. Hard cap 6144 bits/channel/frame. Quality since FFmpeg 4 is good at >= 96 kbps/pair; VBR (`-q:a`) exists but is less tuned — use CBR-ish `-b:a`.
- `libfdk_aac`: better at low bitrates and supports HE-AAC/HE-AACv2 (`-profile:a aac_he|aac_he_v2|aac_ld|aac_eld`), `-vbr 1-5` (~32/40/48-56/64/80-96 kbps per channel), `-afterburner 1` (default), `-cutoff`. It is **nonfree**: a GPL ffmpeg linked with it is not redistributable, so Clustarr images cannot ship it. Not installed here either.
- Opus (`libopus`) is technically superior and free, but AAC is the brief's requirement and the most direct-play-compatible.

### 5.2 Clustarr audio policy (per-channel-count bitrate) **[opinion]**

| Layout | AAC-LC bitrate | note |
|---|---|---|
| mono | 96k | |
| stereo | 160k (min 128k) | |
| 5.1 (6 ch) | 384k (64k/ch) | native aac default would be 341k |
| 7.1 (8 ch) | 512k | |

Stream handling (all driven by probe data, explicit `-map 0:<index>` per stream; never rely on `-map 0:a:m:language:eng` in production — it works **[verified]** but fails the whole run with "Stream map matches no streams" when no stream matches):
1. Choose "main" audio per wanted language (profile `Languages`, e.g. `[eng, jpn, und]`): highest channel count, prefer lossless/object formats as the source of the transcode.
2. Emit one AAC track per kept language (channels = min(source, `MaxChannels`)).
3. `KeepOriginal: never | lossless | atmos | always` — `truehd` (Atmos = TrueHD + JOC/Atmos extension, cannot be re-created by ffmpeg), `dts` profile `DTS-HD MA`, `flac`, `eac3` with JOC -> copied after the AAC track when policy allows; AAC track is `-disposition:a:0 default`.
4. Optional stereo compatibility track (`StereoCompatTrack: true`): `-ac 2` (swresample defaults **[verified]**: `center_mix_level 0.707, surround_mix_level 0.707, lfe_mix_level 0` -> LFE dropped) or a "night mode" pan: `-af "pan=stereo|FL=FC+0.30*FL+0.30*BL|FR=FC+0.30*FR+0.30*BR"`; `-matrix_encoding dplii` for Pro Logic II.
5. Drop commentary (`DropCommentary: true`): title matches `(?i)commentar` or `disposition.comment == 1`.
6. Preserve `language`, `title`, disposition flags (`default`, `forced`, `hearing_impaired`, `visual_impaired`) via `-metadata:s:a:N` / `-disposition:a:N`.
7. AAC adds 1024-sample priming; expect audio `DURATION` ~20 ms longer than video (verified 30.023 vs 29.999 s) — verification must tolerate this.

---

## 6. Subtitles, attachments, container, metadata

- Text subtitles (`subrip`, `ass`, `ssa`, `webvtt`, `mov_text`): `-c:s copy` into MKV. MP4 accepts only `mov_text` (`-c:s mov_text`, loses ASS styling).
- Bitmap subtitles (`hdmv_pgs_subtitle`, `dvd_subtitle`, `dvb_subtitle`): copy into MKV; MP4 cannot carry them -> drop (the Subtitle service can re-fetch text subs) or OCR out of band.
- `-map 0:s?` / `-map 0:t?` (`?` = optional, verified no error when absent); attachments (`-c:t copy`) are needed for ASS fonts.
- Forced/SDH flags are preserved on copy; profile can filter by language and `KeepForced`.
- Container: **MKV** default (HEVC10, DV config, PGS/ASS, attachments, chapters, TrueHD/DTS, arbitrary tags). MP4 only as an "apple-compat" profile (`-tag:v hvc1 -movflags +faststart`, no PGS/ASS).
- Metadata/chapters: `-map_metadata 0 -map_chapters 0` (default for input 0 anyway, be explicit). Write to `<out>.part.mkv` then rename (Unmanic pattern) and tag `CLUSTARR_PROFILE`.

---

## 7. Progress reporting **[verified]**

Run with `-nostdin -nostats -loglevel error -progress pipe:1 -stats_period 1` (stderr then contains only real errors; capture and attach the tail to the job status). Progress blocks on stdout, one key per line, terminated by `progress=continue` or `progress=end`:

```
frame=72
fps=0.00
stream_0_0_q=36.0
bitrate= 623.4kbits/s
total_size=233781
out_time_us=3000000
out_time_ms=3000000      <- QUIRK: also microseconds (identical to out_time_us), do not divide by 1000
out_time=00:00:03.000000
dup_frames=0
drop_frames=0
speed=9.24x
progress=end
```
Percent = `out_time_us / source_duration_us` (or `frame / nb_read_packets`); ETA = `(duration - out_time) / speed`. `fps` and `speed` are windowed averages; `bitrate/total_size` may be `N/A` for `-f null`. `-progress` accepts any avio URL (`pipe:1`, a file, `tcp://`, `unix:`); a pipe read by the Go parent is the simplest. Multiple outputs put `stream_N_M_q` keys per stream. For chunked jobs sum `frame` across chunk workers.

---

## 8. Verification

1. Process exit code 0 **and** a final `progress=end` block.
2. `ffprobe` the output: expected stream count/types, `codec_name=hevc`, `profile=Main 10`, `pix_fmt=yuv420p10le`, colour tags equal to the source (for HDR), side data present (MDCV/CLL, DOVI record when DV was requested).
3. Video packet count (`-count_packets`) == source packet count (exact match verified in the chunk experiment: 720/720); duration within +-1 frame (MKV `DURATION` tags differ by 1 ms due to 1 ms timebase: 29.999 vs 30.000 in the test); audio duration within +-100 ms.
4. Optional full decode: `ffmpeg -v error -i out.mkv -f null -` (Tdarr's "health check"; ~decode speed).
5. Optional VMAF (build has libvmaf): 
   ```bash
   ffmpeg -i out.mkv -i src.mkv -lavfi "[0:v]format=yuv420p10le[d];[1:v]format=yuv420p10le[r];[d][r]libvmaf=model=version=vmaf_v0.6.1:n_threads=8:n_subsample=4:log_fmt=json:log_path=vmaf.json" -f null -
   # 4K: model=version=vmaf_4k_v0.6.1 ; JSON keys: version, fps, frames[], pooled_metrics.vmaf{min,max,mean,harmonic_mean}, aggregate_metrics
   ```
   Verified run: mean 92.08 / min 90.7 for CRF 28 ultrafast synthetic. Suggested gate: `mean >= 93 && min >= 80` (per-profile). VMAF costs a full decode of both files; run it with `n_subsample` and only for a sample of jobs or when a profile demands it. If the output was scaled, scale the reference to the output size first.

---

## 9. Distributed transcoding

### 9.1 (a) One Job per file
Simple, exact, no seams, 2-pass possible, HDR10+/DV trivial. Parallelism only across files. Fine for 1080p and for GPU tiers; painful for 4K x265 slow (hours to a day per file).

### 9.2 (b) Chunked / segment-parallel — verified pipeline

```bash
# 1. Split by stream copy at source keyframes (no decode; segments start on keyframes by construction)
ffmpeg -i src.mkv -map 0:v:0 -c copy -f segment -segment_time 60 -reset_timestamps 1 \
       -segment_list segs.txt -segment_list_type ffconcat seg_%03d.mkv
#    (segment boundaries snap to the next keyframe >= segment_time; use -segment_times t1,t2,... to cut at chosen keyframes)
# 2. Encode each segment independently (any node), closed GOP, identical HDR params; CRF only
ffmpeg -i seg_001.mkv -c:v libx265 -preset slow -crf 22 -pix_fmt yuv420p10le -flags +cgop -g 240 -keyint_min 24 \
       -x265-params "pools=8:repeat-headers=1:hdr10=1:master-display=...:max-cll=..." -an enc_001.mkv
# 3. Audio once, from the source
ffmpeg -i src.mkv -map 0:a:0 -c:a aac -b:a 384k audio.mka
# 4. Concat video chunks + mux audio/subs/chapters from the source
printf "file 'enc_%03d.mkv'\n" 0 1 2 > concat.txt
ffmpeg -f concat -safe 0 -i concat.txt -i audio.mka -i src.mkv -map 0:v:0 -map 1:a:0 -map 2:s? -map 2:t? \
       -map_metadata 2 -map_chapters 2 -c copy final.mkv
```
Verified results (30 s, 24 fps, 3 x 10 s chunks, 3 parallel x265 encodes): 720/720 frames, keyframes exactly at 0/10/20 s, DTS strictly monotonic across seams (9.875 -> 9.917 -> 9.958 -> 10.000), no irregular PTS deltas, full decode OK, VMAF 92 vs source, MP4 concat also clean. The concat demuxer with `-c copy` handles the B-frame reorder delay at each seam because every chunk starts with an IDR and the demuxer offsets timestamps by segment duration.

Constraints and lessons:
- **Chunk starts must be keyframes** (guaranteed by stream-copy segmenting). Segment lengths therefore vary with the source GOP (Blu-ray HEVC GOPs are ~1-2 s; web sources up to 10 s). Frame-exact cutting requires decoding (Av1an `lsmash/ffms2/hybrid` chunk methods, or ffmpeg `-ss` accurate seek + `-t`), which costs a decode per chunk; keyframe snapping is good enough for us.
- **Scene-aligned cuts hide the IDR cost.** Get scene changes with `-vf "scdet=threshold=10,metadata=mode=print:key=lavfi.scd.time:file=-"` (emits `lavfi.scd.time=<sec>` lines **[verified]**), intersect with the keyframe list, and pick cut points near the 60-120 s target (dve default 60 s, "<300 s"; Av1an `--extra-split-sec 10` on top of scene detection). Av1an encodes x265 with `--keyint -1 --scenecut 0` because chunks are already scene-aligned; we keep `keyint=10 x fps` and default scenecut inside a chunk since our chunks are longer.
- **Closed GOP** (`-flags +cgop`) on every chunk; x265 always starts a stream with an IDR, so open GOP inside chunks would technically concat, but closed GOP is required for reliable seeking and for DV/BD compliance anyway.
- **Rate control**: CRF is chunk-independent. 2-pass ABR across chunks is impossible (each chunk would need the global stats); per-chunk VBV is fine (needed for DV). Lookahead/cutree restart at each chunk start -> ~1-3 % size overhead for 10 s chunks, negligible at 60 s+.
- **Audio is never chunked** (AAC priming/frame alignment would glitch; Tdarr issue #700 flagged "audio can be choppy if done in chunks"); encode once from the source and mux at the end. Subs/attachments/chapters come from the source at mux time.
- **HDR**: HDR10 static params are per-encode constants -> pass identical strings to every chunk. DV RPUs travel per frame inside each stream-copied segment, so DV survives chunking (still needs VBV). HDR10+ `dhdr10-info` JSON is frame-indexed from 0 -> chunking not supported (fallback to single Job or drop HDR10+).
- **Concat tool**: Av1an prefers `mkvmerge` for x265 output ("ffmpeg concat can produce files with partially broken audio seeking") — not an issue when audio is muxed after concat; `mkvtoolnix` (GPL) can still be added to the image as an alternative concatenator/`mkvpropedit` for tag fix-ups.
- **Storage**: chunks need a shared RWX volume (NFS/CephFS) or object storage. Stream-copied segments total the source size; encoded chunks are small. Encodarr avoids shared storage by HTTP-transferring whole files to runners; dve got only ~2x with 3 hosts because transfer time dominated — keep chunks on shared storage and schedule workers where the storage is fast.
- **Verification per chunk**: encoded chunk packet count == segment packet count (Av1an warns on "frame mismatch"); then whole-file checks from section 8.
- **Resume**: Av1an keeps `done.json` (per-chunk frames/size + `audio_done`); we get this for free from the Indexed Job (succeeded indexes) plus chunk outputs on shared storage.

### 9.3 Projects surveyed
- **Av1an** (Rust): scene detection (`av-scenechange`, `--sc-method standard|fast`, `--min-scene-len`, `--extra-split-sec 10`), chunk methods `lsmash|ffms2|dgdecnv|bestsource|hybrid|select|segment`, per-chunk encoders (x265 via `--keyint -1 --scenecut 0`), concat `mkvmerge|ffmpeg|ivf`, audio once (`-c:a copy` default), `done.json` resume, target-quality mode (VMAF-driven CRF search). Gold standard for chunked encoding.
- **Tdarr** (Node.js): server + nodes over TCP 8266/8267, worker types CPU transcode / GPU transcode / health check, flow plugins, no single-file distribution (issue #700 closed as enhancement). Lesson: health-check workers as a separate cheap worker class; cache dir + replace-original step.
- **Unmanic** (Python): plugins return `exec_command` + `command_progress_parser` (percent); output staged in a cache dir, `.part` suffix then rename; remote workers via installation "link" (file upload API, poll status, fetch result).
- **Encodarr** (Go, MPL-2.0): controller + runners, runners receive the file over HTTP (no shared storage), UI-configured targets; plugin system "planned". Small project (~70 stars); the file-transfer design is the main takeaway (avoid).
- **dve** (bash): 1-min chunks over SSH + GNU parallel, ~2x with 3 hosts, MKV only.
- **NotEnoughEncodes** / **PyParallelEncode**: split at nearest keyframe, encode in parallel, concat; resume by skipping done chunks.
- "Chunkarr": no such project found. "fireshare" is a self-hosted video sharing app, not a distributed transcoder (not investigated further).

---

## 10. Go design for `pkg/transcode`

Process-based (exec `ffmpeg`/`ffprobe`), no cgo. `go-astiav` (libav cgo bindings, v0.42.0) is available but couples the binary to a libav ABI; avoid.

```go
package transcode

// ---------- Probe ----------
type Prober interface {
    Probe(ctx context.Context, path string) (*MediaInfo, error) // runs both ffprobe calls, decode-free except first frame
}

type MediaInfo struct {
    Path        string
    Format      FormatInfo
    Video       []VideoStream
    Audio       []AudioStream
    Subtitles   []SubtitleStream
    Attachments []AttachmentStream
    Chapters    []Chapter
    Tags        map[string]string
}

type FormatInfo struct {
    Name      string        // "matroska,webm"
    Duration  time.Duration
    SizeBytes int64
    BitRate   int64
}

type Rational struct{ Num, Den int64 }

type VideoStream struct {
    Index          int
    Codec          string   // "hevc","h264","av1","vc1","mpeg2video"
    Profile        string   // "Main 10"
    Level          int
    PixFmt         string   // "yuv420p10le"
    BitDepth       int      // derived from pix_fmt
    Width, Height  int
    SAR, DAR       Rational
    FrameRate      Rational // r_frame_rate
    AvgFrameRate   Rational
    FieldOrder     string   // "progressive"|"tt"|"bb"|"tb"|"bt"
    ColorRange     string
    ColorPrimaries string
    ColorTransfer  string
    ColorSpace     string
    HDR            HDRInfo
    Packets        int64    // nb_read_packets (count_packets)
    Duration       time.Duration
    Disposition    Disposition
    Language, Title string
    Keyframes      []time.Duration // optional, filled by Segmenter
}

type HDRFormat string
const (
    HDRNone        HDRFormat = "sdr"
    HDR10          HDRFormat = "hdr10"
    HDR10Plus      HDRFormat = "hdr10plus"
    HLG            HDRFormat = "hlg"
    DolbyVision    HDRFormat = "dolbyvision"
)

type HDRInfo struct {
    Format           HDRFormat
    MasteringDisplay *MasteringDisplay // nil if absent
    ContentLight     *ContentLight
    DolbyVision      *DoviConfig
    HasHDR10Plus     bool
}
type MasteringDisplay struct {
    RedX, RedY, GreenX, GreenY, BlueX, BlueY, WhiteX, WhiteY Rational // /50000
    MaxLuminance, MinLuminance                               Rational // /10000
}
func (m MasteringDisplay) X265() string // "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)"
type ContentLight struct{ MaxCLL, MaxFALL int }
type DoviConfig struct {
    Profile, Level                      int
    RPUPresent, ELPresent, BLPresent    bool
    BLSignalCompatibilityID             int    // 1=HDR10 compat (8.1), 2=SDR (8.2), 4=HLG (8.4), 0=P5
    MDCompression                       string // "none"|"limited"|"extended"
}

type AudioStream struct {
    Index         int
    Codec         string // "aac","eac3","ac3","truehd","dts","flac","opus","pcm_s24le"
    Profile       string // "LC","DTS-HD MA","Dolby Digital Plus + Dolby Atmos" (as ffprobe reports)
    Channels      int
    ChannelLayout string
    SampleRate    int
    BitRate       int64
    Lossless      bool // truehd, flac, pcm_*, dts with profile DTS-HD MA
    Atmos         bool // truehd/eac3 with Atmos/JOC marker in profile or title
    Language      string
    Title         string
    Disposition   Disposition
}
type SubtitleStream struct {
    Index int; Codec string; Bitmap bool; Language, Title string; Disposition Disposition
}
type AttachmentStream struct{ Index int; Filename, MimeType string }
type Chapter struct{ Start, End time.Duration; Title string }
type Disposition struct{ Default, Forced, Comment, HearingImpaired, VisualImpaired, Dub, Original bool }

// ---------- Plan ----------
type Planner interface {
    Plan(ctx context.Context, in *MediaInfo, p *Profile, caps NodeCapabilities) (*Plan, error)
}

type PlanKind string
const (
    PlanSkip   PlanKind = "skip"   // already compliant
    PlanRemux  PlanKind = "remux"  // video copy, audio/subs/container work only
    PlanEncode PlanKind = "encode"
    PlanReject PlanKind = "reject"
)

type Plan struct {
    Kind       PlanKind
    Reason     string
    Input      string
    Output     string          // final path; Runner writes Output+".part" then renames
    Container  string          // "matroska"|"mp4"
    Video      *VideoPlan      // nil for skip/remux
    Audio      []AudioPlan
    Subtitles  []StreamCopy
    Attach     []StreamCopy
    GlobalArgs []string        // -nostdin -y -nostats -loglevel error -progress pipe:1 ...
    Chunking   *ChunkPlan      // nil = single job
    Expected   Expectation     // for Verifier: packets, duration, streams
    Tags       map[string]string // CLUSTARR_PROFILE=...
}

type VideoPlan struct {
    Encoder      EncoderID      // x265 | nvenc | qsv | vaapi | vulkan | videotoolbox
    Filters      []string       // "bwdif=mode=send_frame", "scale=-2:1080:flags=lanczos", "setparams=..."
    Args         []string       // fully rendered -c:v ... -x265-params ... (from EncoderBackend)
    HDR          HDRInfo        // what we intend to write
    DolbyVision  bool           // -dolbyvision 1 + VBV
    HWDecode     bool           // -hwaccel cuda/qsv/vaapi chain
    OutWidth, OutHeight int
}
type AudioPlan struct {
    SourceIndex int
    Copy        bool
    Codec       string // "aac"
    BitRate     int64
    Channels    int
    Downmix     string // "" | "default" | "nightmode" | "dplii"
    Language    string
    Title       string
    Default     bool
}
type StreamCopy struct{ SourceIndex int; Language, Title string }

type Expectation struct {
    VideoPackets   int64
    Duration       time.Duration
    DurationTol    time.Duration
    Streams        int
    VideoCodec, VideoProfile, PixFmt string
    HDR            HDRFormat
    DolbyVision    bool
}

// EncoderBackend renders encoder-specific args. One implementation per EncoderID.
type EncoderBackend interface {
    ID() EncoderID
    Supports(v *VideoStream, target VideoTarget) error // e.g. nvenc cannot do DV; vaapi needs hw frames
    Args(v *VideoStream, target VideoTarget, out OutputGeometry, hdr HDRInfo, threads int) ([]string, []string /*filters*/, error)
    PixelFormat() string // yuv420p10le | p010le | vaapi
}

// ---------- Run ----------
type Progress struct {
    Frame        int64
    FPS          float64
    Bitrate      string
    TotalSize    int64
    OutTime      time.Duration // from out_time_us
    DupFrames    int64
    DropFrames   int64
    Speed        float64       // "9.24x" -> 9.24
    Percent      float64       // computed vs Expectation
    ETA          time.Duration
    Done         bool          // progress=end
}

type Runner interface {
    // Run executes one ffmpeg invocation. Progress is sent at -stats_period cadence; the channel is closed on exit.
    // ctx cancellation sends SIGINT (ffmpeg flushes/trailer), then SIGKILL after grace.
    Run(ctx context.Context, args []string, opts RunOptions) (<-chan Progress, <-chan Result)
}
type RunOptions struct {
    FFmpegPath   string
    Env          []string      // LIBVA_DRIVER_NAME=iHD, CUDA_VISIBLE_DEVICES, ...
    StatsPeriod  time.Duration // default 1s
    StderrTail   int           // bytes kept for Result.StderrTail
    Nice         int
    Timeout      time.Duration
}
type Result struct {
    ExitCode   int
    Err        error
    StderrTail string
    Wall       time.Duration
    Final      Progress
}

// ---------- Chunking ----------
type ChunkPlan struct {
    ChunkTarget time.Duration   // 60s..120s
    MinChunk    time.Duration   // 10s
    Cuts        []time.Duration // chosen keyframe timestamps (scene-aligned when available)
    Segments    []Segment       // filled after segmenting
    WorkDir     string          // shared storage
}
type Segment struct {
    Index     int
    Path      string          // seg_000.mkv (stream copy)
    Start     time.Duration
    Duration  time.Duration
    Packets   int64           // expected frames for this chunk
    Output    string          // enc_000.mkv
}

type Segmenter interface {
    // Cuts chooses keyframe-aligned cut points (optionally intersected with scdet scene changes).
    Cuts(ctx context.Context, in *MediaInfo, target, min time.Duration, sceneAlign bool) ([]time.Duration, error)
    // Split runs the segment muxer with -c copy -segment_times and returns per-segment packet counts.
    Split(ctx context.Context, in *MediaInfo, cuts []time.Duration, workDir string) ([]Segment, error)
}

type Concatenator interface {
    // Concat writes the ffconcat list, runs `-f concat -safe 0 -c copy` with the audio/sub/attachment inputs, then verifies.
    Concat(ctx context.Context, segs []Segment, audio []string, source *MediaInfo, plan *Plan) error
}

// ---------- Verify ----------
type Verifier interface {
    Verify(ctx context.Context, out string, exp Expectation, opts VerifyOptions) (*VerifyReport, error)
}
type VerifyOptions struct{ FullDecode bool; VMAF *VMAFOptions }
type VMAFOptions struct{ Model string; Subsample int; Threads int; MinMean, MinMin float64 }
type VerifyReport struct {
    OK           bool
    Problems     []string
    Packets      int64
    Duration     time.Duration
    VMAF         *VMAFResult // nil if not run
}
type VMAFResult struct{ Mean, Min, Max, HarmonicMean float64; Frames int }
```

Implementation notes:
- Progress parser: read stdout line by line (`bufio.Scanner`), accumulate `key=value` until `progress=`; `out_time_us` is authoritative; treat `N/A` values as zero. Stderr goes to a ring buffer for `Result.StderrTail`.
- Arg builder: plain `[]string` assembled in a fixed order (global -> inputs -> maps -> per-stream codecs -> metadata -> format -> output). `u2takey/ffmpeg-go` (v0.5.0) offers a filtergraph DSL and `Probe()`, but the arg ordering and per-stream specifiers we need are simpler by hand.
- `gopkg.in/vansante/go-ffprobe.v2` (v2.3.1) has typed `Stream`, `Format`, `Chapter`, `SideDataList` with `SideDataMasteringDisplayMetadata` / `SideDataContentLightLevel`, but **no** `DOVI configuration record` type and no frame probing; define Clustarr's own JSON structs (`encoding/json`, `map[string]any` for side data by `side_data_type`) — it is ~150 lines and keeps the DOVI/HDR10+ fields.
- Encoder capability detection at worker start: `ffmpeg -hide_banner -encoders`, `-hwaccels`, `nvidia-smi -L`/NVML, `/dev/dri/renderD*` presence -> `NodeCapabilities{Encoders []EncoderID, GPUs []GPUInfo, CPUs int}`; publish as node labels or in the worker's registration message.
- Threads: `threads := cpuLimitFromCgroup()` (read `/sys/fs/cgroup/cpu.max`) -> `pools=<threads>` and `-threads <threads>` for decode; nvenc jobs use `-threads 2`-4 for the CPU side.

---

## 11. `TranscodeProfile` data model

```go
type Profile struct {
    Name         string
    Version      int               // bump when defaults change; Hash() covers all fields
    Video        VideoTarget
    Audio        AudioTarget
    Subtitles    SubtitlePolicy
    Container    ContainerTarget
    Chunking     ChunkingPolicy
    Verification VerifyPolicy
    Skip         SkipPolicy
}

type VideoTarget struct {
    Codec        string            // "hevc" (only value for now; keep for av1 later)
    PixelFormat  string            // "yuv420p10le"
    Profile      string            // "main10"
    Preset       string            // "slow"
    Tune         string            // "" | "animation" | "grain"
    CRF          map[ResClass]float64 // {"sd":21,"720p":21,"1080p":22,"2160p":23}
    CRFHDROffset float64           // -1 for HDR classes
    X265Params   map[string]string // merged over defaults; "pools" is always overridden by the worker
    MaxWidth     int               // 0 = no downscale; e.g. 1920 for a "1080p max" profile
    MaxHeight    int
    KeepHDR      bool              // false = tonemap to SDR (libplacebo/zscale+tonemap; out of scope for v1 -> reject)
    DolbyVision  DVPolicy          // "passthrough" | "strip" | "reject"
    HDR10Plus    HDR10PlusPolicy   // "drop" | "preserve" (preserve => hdr10plus_tool + single job)
    Deinterlace  bool              // true = bwdif when field_order != progressive
    ClosedGOP    bool              // true
    KeyintSeconds int              // 10
    HWAccel      HWPreference
    VBV          *VBV              // required automatically when DolbyVision passthrough: {MaxRateKbps, BufSizeKbps}
}
type ResClass string // "sd","720p","1080p","2160p"
type HWPreference struct {
    Prefer   EncoderID   // "x265" | "nvenc" | "qsv" | "vaapi" | "auto"
    Fallback bool        // fall back to x265 when no HW node is available within FallbackAfter
    FallbackAfter time.Duration
    NvencCQ  float64     // 24
    NvencPreset string   // "p6"
    QsvICQ   int         // 22
    QsvPreset string     // "veryslow"
    VaapiQP  int         // 24
}
type VBV struct{ MaxRateKbps, BufSizeKbps int }

type AudioTarget struct {
    Codec              string   // "aac"
    BitratePerChannelKbps int   // 64  (stereo floor 128k applied)
    StereoBitrateKbps  int      // 160
    MaxChannels        int      // 6
    Languages          []string // ["eng","und"]; empty = all
    KeepOriginal       KeepPolicy // "never" | "lossless" | "atmos" | "always"
    StereoCompatTrack  bool
    Downmix            string   // "default" | "nightmode" | "dplii"
    DropCommentary     bool
    DefaultLanguage    string
}
type SubtitlePolicy struct {
    CopyText, CopyBitmap bool
    Languages            []string
    KeepForced           bool
    CopyAttachments      bool
}
type ContainerTarget struct{ Format string /* "matroska"|"mp4" */; FastStart bool }
type ChunkingPolicy struct {
    Enabled        bool
    MinDuration    time.Duration // only chunk files longer than this (e.g. 20m)
    TargetChunk    time.Duration // 90s
    MinChunk       time.Duration // 10s
    MaxParallel    int           // Indexed Job parallelism cap
    SceneAlign     bool
    OnlyForCPU     bool          // GPU jobs are fast enough single-shot
}
type VerifyPolicy struct {
    FullDecode bool
    VMAF       *VMAFOptions // nil = off
    SizeGuard  float64      // reject if output > SizeGuard * input (e.g. 0.95): "transcode made it bigger"
}
type SkipPolicy struct {
    IfCompliant     bool
    MaxBitrateKbps  map[ResClass]int64 // skip HEVC10 sources already below these (already small)
}
```

CRD sketch (`transcode.clustarr.io/v1alpha1`):

```yaml
apiVersion: transcode.clustarr.io/v1alpha1
kind: TranscodeProfile            # cluster-scoped, opinionated defaults shipped as manifests
metadata: { name: hevc10-aac-space }
spec:
  video: { codec: hevc, pixelFormat: yuv420p10le, profile: main10, preset: slow,
           crf: { sd: 21, 720p: 21, 1080p: 22, 2160p: 23 }, crfHDROffset: -1,
           closedGOP: true, keyintSeconds: 10, deinterlace: true,
           dolbyVision: passthrough, hdr10Plus: drop, keepHDR: true,
           hwAccel: { prefer: auto, fallback: true, fallbackAfter: 30m, nvencCQ: 24, qsvICQ: 22, vaapiQP: 24 } }
  audio: { codec: aac, bitratePerChannelKbps: 64, stereoBitrateKbps: 160, maxChannels: 6,
           languages: [eng, und], keepOriginal: atmos, stereoCompatTrack: false, dropCommentary: true }
  subtitles: { copyText: true, copyBitmap: true, keepForced: true, copyAttachments: true }
  container: { format: matroska }
  chunking: { enabled: true, minDuration: 20m, targetChunk: 90s, minChunk: 10s, maxParallel: 8, sceneAlign: true, onlyForCPU: true }
  verification: { fullDecode: false, sizeGuard: 0.95 }
  skip: { ifCompliant: true }
---
apiVersion: transcode.clustarr.io/v1alpha1
kind: TranscodeJob                 # namespaced, created by the inventory/import flow after a download completes
metadata: { name: movie-tt0133093-1 }
spec:
  source: { path: /media/movies/The Matrix (1999)/The Matrix (1999).mkv, volume: media }
  profileRef: hevc10-aac-space
  output: { path: /media/movies/The Matrix (1999)/The Matrix (1999) [hevc10].mkv, replaceSource: true }
  priority: 50
  chunkingOverride: null
status:
  phase: Pending|Probing|Planned|Segmenting|Encoding|Concatenating|Verifying|Finalizing|Succeeded|Failed|Skipped
  plan: { kind: encode, encoder: x265, chunks: 12, hdr: hdr10, reason: "" }
  progress: { percent: 37.5, fps: 6.1, speed: 0.25, eta: 2h11m, chunksDone: 4 }
  jobs: [ { name: movie-tt0133093-1-encode, kind: Indexed, succeeded: 4, failed: 0 } ]
  verification: { packets: 172800, durationDelta: 1ms, vmafMean: null }
  conditions: [...]
```

---

## 12. Kubernetes scheduling model

### 12.1 Shapes
- **Whole-file job**: `TranscodeJob` -> one `batch/v1 Job` (`completions: 1, parallelism: 1, backoffLimit: 2, ttlSecondsAfterFinished: 3600, activeDeadlineSeconds: ceil(duration_s / expected_speed) * 3`).
- **Chunked job**: three phases reconciled by the controller: (1) `segment` Job (stream copy, cheap; or done in-controller-worker), (2) **Indexed Job** `completionMode: Indexed, completions: N, parallelism: min(N, MaxParallel)`, each pod reads `JOB_COMPLETION_INDEX` (also annotation `batch.kubernetes.io/job-completion-index`) to pick `Segments[i]`; `backoffLimitPerIndex: 2, maxFailedIndexes: 0` (GA 1.33); (3) `concat+verify` Job. Elastic Indexed Jobs allow lowering `parallelism` live for throttling.
- **Pod failure policy** (GA 1.31): `onPodConditions: [{type: DisruptionTarget}] -> Ignore` (preemption/eviction retries free), `onExitCodes: {operator: In, values: [64,65,69]} -> FailJob` for our "invalid input / unsupported" exit codes (never retry), everything else `Count`.
- `successPolicy` (GA 1.33) not needed (all indexes must succeed). `managedBy` (GA 1.35) only matters if Kueue/MultiKueue takes over Job execution.

### 12.2 Resources / placement
```yaml
# CPU x265 worker pod template
containers:
- name: ffmpeg
  image: ghcr.io/clustarr/transcode-worker:<ffmpeg9>   # GPL build, no nonfree
  env:
  - { name: CPU_LIMIT, valueFrom: { resourceFieldRef: { resource: limits.cpu } } }   # -> x265 pools=
  resources:
    requests: { cpu: "8", memory: 2Gi, ephemeral-storage: 20Gi }
    limits:   { cpu: "8", memory: 3Gi }      # 2160p: memory 6-8Gi
  volumeMounts: [ { name: media, mountPath: /media }, { name: scratch, mountPath: /scratch } ]
volumes:
- { name: media,   persistentVolumeClaim: { claimName: media } }      # RWX (NFS/CephFS) — required for chunking
- { name: scratch, emptyDir: { sizeLimit: 40Gi } }                     # or a per-job RWX PVC for chunk outputs
priorityClassName: clustarr-batch
topologySpreadConstraints/affinity: spread across nodes; anti-affinity with interactive pods

# GPU (NVIDIA) worker additions
runtimeClassName: nvidia
nodeSelector: { nvidia.com/gpu.present: "true" }           # GFD labels; nvidia.com/gpu.product for a specific card
tolerations: [ { key: nvidia.com/gpu, operator: Exists, effect: NoSchedule } ]
resources: { limits: { nvidia.com/gpu: 1, cpu: "3", memory: 2Gi } }
# Intel worker additions
nodeSelector: { intel.feature.node.kubernetes.io/gpu: "true" }
resources: { limits: { gpu.intel.com/i915: 1 } }           # gpu.intel.com/xe for xe-driver GPUs (Battlemage)
securityContext: { supplementalGroups: [ <render gid> ] }  # VAAPI/QSV open /dev/dri/renderD128 (mounted by the device plugin)
env: [ { name: LIBVA_DRIVER_NAME, value: iHD } ]
```
GPU sharing: NVIDIA device plugin time-slicing (`sharing.timeSlicing.replicas: N`) advertises N `nvidia.com/gpu` per card; keep `N <= 8` (consumer NVENC session cap; 3 on GTX 1630) and typically 4 so decode/scale share the card sanely; no memory isolation. Intel plugin: `-shared-dev-num N` (balanced/packed allocation policies).

### 12.3 Queue-driven scaling
Option A (recommended, fits a CRD-first project) **[opinion]**: `TranscodeJob` CRs *are* the queue. The controller keeps per-pool budgets (`cpuSlots`, `nvencSlots`, `qsvSlots`) from a `TranscodePool` config, creates Jobs `suspend: true` immediately (so they show up as pending in kubectl), and un-suspends by priority as slots free (Job `suspend` is GA). Cluster Autoscaler/Karpenter scales GPU node groups on pending pods. If quota management grows, adopt **Kueue** (`kueue.x-k8s.io/queue-name` label, ClusterQueue with `cpu`/`nvidia.com/gpu` quotas, Job `managedBy` for multi-cluster) instead of hand-rolled slots.

Option B: KEDA `ScaledJob` with the `nats-jetstream` trigger (KEDA >= 2.8; metadata `natsServerMonitoringEndpoint: nats.nats.svc:8222, account: "$G", stream, consumer, lagThreshold, activationLagThreshold, useHttps`) -> `maxScale = min(maxReplicaCount, ceil(queueLength / lagThreshold))`, `newJobs = maxScale - runningJobCount` (strategy `default`; `eager` fills all slots; `accurate` for queues without message locking), fields `jobTargetRef` (JobSpec), `pollingInterval`, `minReplicaCount/maxReplicaCount`, `successfulJobsHistoryLimit/failedJobsHistoryLimit`, `rollout.strategy gradual`, `scalingStrategy.pendingPodConditions`. Workers pull one JetStream message (pull consumer, `AckWait` > expected runtime or `msg.InProgress()` heartbeats), run, ack, exit. Good if workers are decoupled from the controller; weaker for per-index chunk fan-out and for CR status.

Progress path in both: worker publishes `Progress` at 1 Hz to NATS (`clustarr.transcode.progress.<job>[.<chunk>]`) and/or a KV bucket keyed by job; the controller aggregates and writes `TranscodeJob.status.progress` at most every ~10 s (API churn). Final events (`succeeded`, `failed` + stderr tail) also go on the bus for the inventory service to swap files / update quality info.

Sizing heuristics: chunk only when `duration >= 20 min && encoder == x265`; `N = ceil(duration / targetChunk)`, `parallelism = min(N, MaxParallel, cpuSlots)`. A 2 h 4K x265-slow encode at ~2 fps single-node becomes ~2 h on 12 chunk workers with 8 CPUs each.

---

## 13. Go libraries (versions verified via `go list -m -versions`)

| Module | Version | Purpose |
|---|---|---|
| gopkg.in/vansante/go-ffprobe.v2 | v2.3.1 | typed ffprobe JSON (has MDCV/CLL side data types; lacks DOVI record & frame probing) — optional, prefer own structs |
| github.com/u2takey/ffmpeg-go | v0.5.0 | ffmpeg arg/filtergraph DSL + `Probe()`; optional |
| github.com/asticode/go-astiav | v0.42.0 | cgo libav bindings — avoid |
| github.com/nats-io/nats.go | v1.53.1 | progress/events/queue (JetStream, KV) |
| sigs.k8s.io/controller-runtime | v0.25.1 | controllers |
| k8s.io/api, k8s.io/client-go | v0.37.0 | batch/v1 Job, core/v1 |
| github.com/prometheus/client_golang | v1.24.1 | worker/controller metrics (fps, speed, queue depth) |
| golang.org/x/sync | v0.23.0 | errgroup for chunk fan-out / parallel probes |
| github.com/cenkalti/backoff/v5 | v5.0.3 | retries |
| github.com/google/uuid | v1.6.0 | job ids |
| github.com/spf13/cobra | v1.10.2 | worker/CLI entrypoints |
| github.com/stretchr/testify | v1.12.1 | tests (golden-file tests of arg builders) |
| github.com/tidwall/gjson | v1.19.0 | optional ad-hoc ffprobe JSON access |
| github.com/NVIDIA/go-nvml | v0.13.4-0 (pre-release only) | optional GPU introspection in workers |
| github.com/redis/go-redis/v9 | v9.22.0 | only if the queue team picks Redis |
| github.com/hibiken/asynq / github.com/riverqueue/river | v0.26.0 / v0.47.0 | Redis / Postgres task-queue alternatives (for the queue comparison, not needed here) |
| sigs.k8s.io/kueue | unverified | optional quota/queueing for Jobs |

Non-Go tooling for the worker image: ffmpeg >= 7.1 GPL build with libx265 (>= 3.5, build 4.x recommended), nvenc/qsv (libvpl)/vaapi enabled, libvmaf; `dovi_tool` and `hdr10plus_tool` (Rust, MIT/MIT); `mkvtoolnix` optional; NVIDIA container toolkit / Intel GPU plugin on nodes.

---

## 14. Sources

- Local runs: `ffmpeg -h encoder=...` dumps, HDR encode+probe, chunk experiment, NVENC tests, benchmark (scratchpad).
- FFmpeg 9.0 source: `libavcodec/libx265.c`, `libavcodec/dovi_rpuenc.c`, `libavcodec/nvenc.c`, `libavcodec/qsvenc.c`, `libavcodec/qsvenc_hevc.c`, `libavcodec/videotoolboxenc.c`, `libavcodec/aacenc.c`, `libavcodec/libfdk-aacenc.c`, `libavutil/side_data.c`, `libavcodec/packet.c`, `fftools/ffprobe.c`; Changelog https://raw.githubusercontent.com/FFmpeg/FFmpeg/master/Changelog
- x265: https://x265.readthedocs.io/en/master/cli.html ; `source/common/param.cpp`, `source/common/threadpool.cpp` (bitbucket multicoreware/x265_git); `x265 --fullhelp` (4.3)
- FFmpeg codecs doc (aac, libfdk_aac): https://ffmpeg.org/ffmpeg-codecs.html
- FFmpeg DeepWiki "Dolby Vision and HDR Metadata": https://deepwiki.com/FFmpeg/FFmpeg/5.5-dolby-vision-and-hdr-metadata ; libx265 DV commit https://github.com/FFmpeg/FFmpeg/commit/39ca87ed1ef876af9622a5aa331e18167fdfdf27
- dovi_tool: https://github.com/quietvoid/dovi_tool ; DoviCrop https://github.com/jessielw/DoviCrop ; P7->8.1 discussion https://github.com/quietvoid/dovi_tool/discussions/195 ; makemkv forum thread https://forum.makemkv.com/forum/viewtopic.php?t=26514
- HDR10/HDR10+ ffmpeg guide: https://codecalamity.com/encoding-uhd-4k-hdr10-videos-with-ffmpeg/
- x265 guides: https://silentaperture.gitlab.io/mdbook-guide/encoding/x265.html ; https://ffmpeg.party/guides/x265/ ; https://xcodecpack.com/hevc/settings/ ; https://gist.github.com/dvaupel/9bb532715d5167239487bdc93bb1de2d
- NVENC: https://videocardz.com/newz/nvdia-geforce-gpus-now-support-up-to-8-concurrent-nvenc-encoding-sessions ; https://docs.nvidia.com/video-technologies/video-codec-sdk/12.2/nvenc-application-note/index.html ; https://developer.nvidia.com/blog/nvidia-video-codec-sdk-13-0-powered-by-nvidia-blackwell/ ; https://developer.nvidia.com/blog/improving-video-quality-with-nvidia-video-codec-sdk-12-2-for-hevc/ ; UHQ+10-bit bug https://forums.developer.nvidia.com/t/nvenc-hevc-uhq-highbitdept-leads-to-artfiacts-in-ffmpeg-with-blackwell/354084
- Intel: https://jellyfin.org/docs/general/post-install/transcoding/hardware-acceleration/intel/ ; https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/gpu_plugin/README.md ; QSV ICQ notes https://salivity.github.io/ffmpeg/article/configure-hevc-qsv-rate-control-and-quality-in-ffmpeg
- GPU time-slicing: https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-sharing.html
- Kubernetes Jobs: https://kubernetes.io/docs/concepts/workloads/controllers/job/ ; successPolicy GA 1.33 https://kubernetes.io/blog/2025/05/15/kubernetes-1-33-jobs-success-policy-goes-ga/ ; backoffLimitPerIndex GA 1.33 https://kubernetes.io/blog/2025/05/13/kubernetes-v1-33-jobs-backoff-limit-per-index-goes-ga/ ; podFailurePolicy GA 1.31 https://kubernetes.io/blog/2024/08/19/kubernetes-1-31-pod-failure-policy-for-jobs-goes-ga/ ; managedBy KEP-4368 https://github.com/kubernetes/enhancements/issues/4368
- KEDA: https://keda.sh/docs/latest/scalers/nats-jetstream/ ; https://keda.sh/docs/latest/reference/scaledjob-spec/
- Distributed encoders: Av1an https://deepwiki.com/master-of-zen/Av1an ; Tdarr https://github.com/HaveAGitGat/Tdarr and issue https://github.com/HaveAGitGat/Tdarr/issues/700 ; Unmanic https://deepwiki.com/Unmanic/unmanic ; Encodarr https://github.com/BrenekH/encodarr ; dve https://github.com/tdaede/dve ; NotEnoughEncodes https://github.com/Alkl58/NotEnoughEncodes ; comparison https://sumguy.com/unmanic-vs-tdarr/
