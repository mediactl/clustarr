/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package transcode

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Decision is Plan's top-level verdict for one source file.
type Decision string

// Decisions.
const (
	DecisionSkip      Decision = "skip"
	DecisionRemuxOnly Decision = "remuxOnly"
	DecisionEncode    Decision = "encode"
	DecisionReject    Decision = "reject"
)

// Tier is the encoder backend a Plan renders against.
type Tier string

// Tiers.
const (
	TierCPUx265 Tier = "cpu-x265"
	TierNVENC   Tier = "nvenc"
	TierQSV     Tier = "qsv"
	TierVAAPI   Tier = "vaapi"
)

// AudioAction is what happens to one source audio track.
type AudioAction string

// Audio actions.
const (
	AudioActionEncode AudioAction = "encode"
	AudioActionCopy   AudioAction = "copy"
	AudioActionDrop   AudioAction = "drop"
)

// AudioTrackPlan is a superset of the CRD's status-facing AudioPlan (adds
// Language, needed to render -metadata:s:a:N language=...; the controller
// trims it when writing TranscodeJobStatus.Plan.AudioTracks).
type AudioTrackPlan struct {
	SourceIndex int32
	Action      AudioAction
	Codec       string
	BitrateKbps int32
	Language    string
	Default     bool
}

// HDRParams is the CRD status mirror of the HDR handling Plan chose.
type HDRParams struct {
	Mode               string // "none" | "hdr10" | "hlg" | "dolbyVision"
	MasterDisplay      string // rendered x265 master-display= value, "" if absent
	MaxCLL             string // rendered x265 max-cll= value, "" if absent
	DolbyVisionProfile int32  // 0 when not DV
	RequiresVBV        bool
}

// Expectation is what Verify checks the output against.
type Expectation struct {
	DurationTolMillis int64
	Streams           int32
	VideoCodec        string
	PixelFormat       string
	MinOutputBytes    int64
}

// PlanResult is the Plan function's full rendered decision for one source
// file. (Named PlanResult, not Plan, because the function that produces it
// is named Plan -- Go does not allow a type and a function to share an
// identifier in the same package; the task brief's Produces block shows
// both named "Plan", which is why this package deviates on the type's
// name.)
type PlanResult struct {
	Decision    Decision
	Reason      string
	Tier        Tier
	Container   Container
	Input       string
	Output      string // "<stem>.part.<ext>"; caller verifies then atomically replaces the source (pkg/fsops, outside this package)
	HWInit      []string
	Maps        []string
	Filters     []string
	VideoArgs   []string
	Audio       []AudioTrackPlan
	Subtitles   []int32 // source stream indexes copied
	Attachments bool
	HDR         HDRParams
	Tags        map[string]string // {"CLUSTARR_PROFILE": "<name>@<hash>"}
	Expect      Expectation
}

// PlanMeta carries the inputs Plan needs that are not part of the CRD spec:
// the profile's ObjectMeta.Name and controller-computed ProfileHash (Plan
// has no Kubernetes client to look these up itself), and the resolved x265
// thread-pool size (note §3.7: x265 reads host CPU count, not the cgroup
// quota, so the caller must resolve limits.cpu via the Downward API and
// pass it down explicitly).
type PlanMeta struct {
	ProfileName string
	ProfileHash string
	Threads     int32
}

// containerFromExt maps a lowercased file extension (without the leading
// dot) to a Container, reporting false when it is not one we recognise.
func containerFromExt(ext string) (Container, bool) {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "mkv":
		return ContainerMKV, true
	case "mp4":
		return ContainerMP4, true
	default:
		return "", false
	}
}

func videoCompliant(v VideoStream) bool {
	return v.Codec == "hevc" && v.Profile == "Main 10" && v.PixFmt == "yuv420p10le"
}

func audioCompliant(info MediaInfo, profile ProfileSpec) bool {
	for _, a := range info.Audio {
		if a.Codec != profile.Audio.Codec {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// SelectTier picks the encoder tier from profile.Hardware and info's HDR
// state alone (no capability awareness) -- Dolby Vision forces cpu-x265
// regardless of profile.Hardware (note §3.5: "No hardware encoder... writes
// RPUs -> DV becomes HDR10 (P8) or garbage (P5). Force libx265 for DV
// sources."). Exported so a worker can pre-validate a profile before
// creating a Job.
func SelectTier(profile ProfileSpec, info MediaInfo) (Tier, error) {
	if len(info.Video) > 0 && hdrClass(info.Video[0].HDR.Format) == hdrDolbyVision {
		return TierCPUx265, nil // note §3.5: never a hardware encoder for DV
	}
	switch profile.Hardware {
	case HardwareCPU, "":
		return TierCPUx265, nil
	case HardwareNVIDIA:
		return TierNVENC, nil
	case HardwareIntel:
		return TierQSV, nil // capability-aware fallback to vaapi happens in Plan via FallbackTier
	default:
		return "", fmt.Errorf("transcode: unknown hardware %q", profile.Hardware)
	}
}

// Plan decides skip / remuxOnly / encode / reject and renders every
// ffmpeg-facing field of Plan deterministically. Pure: no I/O, no
// filesystem access, no ffmpeg/ffprobe invocation. caps is precomputed once
// per worker by ProbeCapabilities and passed in as data so Plan itself
// never blocks.
func Plan(info MediaInfo, profile ProfileSpec, caps Capabilities, meta PlanMeta) (*PlanResult, error) {
	if len(info.Video) == 0 {
		return nil, fmt.Errorf("transcode: Plan: info has no video stream")
	}
	v0 := info.Video[0]

	container, hasContainer := containerFromExt(filepath.Ext(info.Path))

	plan := &PlanResult{Container: profile.Container, Input: info.Path}

	tagged := meta.ProfileName != "" && info.Tags != nil &&
		info.Tags["CLUSTARR_PROFILE"] == meta.ProfileName+"@"+meta.ProfileHash
	compliant := videoCompliant(v0) && audioCompliant(info, profile) && hasContainer && container == profile.Container

	if compliant {
		plan.Decision = DecisionSkip
		plan.Reason = "already compliant with profile"
		return plan, nil
	}
	if tagged {
		plan.Decision = DecisionSkip
		plan.Reason = "tagged with current profile hash"
		return plan, nil
	}

	if containsString(profile.Policy.NeverTranscodeModifiers, info.Modifier) {
		plan.Decision = DecisionSkip
		plan.Reason = fmt.Sprintf("modifier %q is in policy.neverTranscodeModifiers", info.Modifier)
		return plan, nil
	}

	if info.Format.Duration < profile.Policy.MinDuration {
		plan.Decision = DecisionSkip
		plan.Reason = "source duration below policy.minDuration"
		return plan, nil
	}

	class := hdrClass(v0.HDR.Format)
	if class == hdrDolbyVision {
		switch profile.HDR.DolbyVision {
		case DolbyVisionReject:
			plan.Decision = DecisionReject
			plan.Reason = "source is Dolby Vision and hdr.dolbyVision policy is reject"
			return plan, nil
		case DolbyVisionPassthrough:
			if profile.Video.MaxRateKbps == nil || profile.Video.BufSizeKbps == nil {
				plan.Decision = DecisionReject
				plan.Reason = "hdr.dolbyVision is passthrough but video.maxRateKbps/bufSizeKbps are unset (required for DV VBV)"
				return plan, nil
			}
		}
	}

	tier, err := SelectTier(profile, info)
	if err != nil {
		return nil, err
	}
	resolved, ok := FallbackTier(tier, caps)
	if !ok {
		plan.Decision = DecisionReject
		plan.Reason = fmt.Sprintf("no available encoder for tier %s and no fallback", tier)
		return plan, nil
	}
	plan.Tier = resolved

	if videoCompliant(v0) {
		plan.Decision = DecisionRemuxOnly
		plan.Reason = "video already compliant; remuxing audio/subtitles/container only"
	} else {
		plan.Decision = DecisionEncode
		reason := "video requires transcoding to hevc/main10/yuv420p10le"
		if class == hdrDolbyVision && v0.HDR.DolbyVision != nil && v0.HDR.DolbyVision.Profile == 7 {
			reason += ", profile 7 dual-layer Dolby Vision downgraded to HDR10 (enhancement layer dropped)"
		}
		plan.Reason = reason
	}

	renderPlan(plan, info, profile, meta, class)
	return plan, nil
}

// renderPlan fills every ffmpeg-facing field of an already-decided
// RemuxOnly/Encode plan: Output, Maps, Filters, VideoArgs, Audio,
// Subtitles, Attachments, HDR, Tags and Expect.
func renderPlan(plan *PlanResult, info MediaInfo, profile ProfileSpec, meta PlanMeta, class hdrBucket) {
	v0 := info.Video[0]

	stem := strings.TrimSuffix(info.Path, filepath.Ext(info.Path))
	plan.Output = stem + ".part." + containerExt(plan.Container)

	plan.Audio = buildAudioPlan(info, profile)
	plan.Subtitles = buildSubtitlePlan(info, profile, plan.Container)
	plan.Attachments = len(info.Attachments) > 0 && profile.Subtitles.CopyAttachments
	plan.Maps = buildMaps(plan.Audio, plan.Subtitles)

	dvMode := profile.HDR.DolbyVision
	if class != hdrNone {
		plan.Filters = append(plan.Filters, hdrSetParamsFilter(v0))
	}

	if plan.Decision == DecisionRemuxOnly {
		plan.VideoArgs = []string{"-c:v", "copy"}
	} else {
		switch plan.Tier {
		case TierNVENC:
			plan.VideoArgs = nvencVideoArgs(profile.Video)
		case TierQSV:
			plan.HWInit = qsvHWInit()
			plan.VideoArgs = qsvVideoArgs(profile.Video)
		case TierVAAPI:
			plan.HWInit = vaapiHWInit()
			plan.Filters = append([]string{"scale_vaapi=format=p010"}, plan.Filters...)
			plan.VideoArgs = vaapiVideoArgs(profile.Video)
		default: // TierCPUx265
			plan.VideoArgs = cpuVideoArgs(profile.Video, v0, class, dvMode, meta.Threads)
		}
	}

	if plan.Container == ContainerMP4 {
		plan.VideoArgs = append(plan.VideoArgs, "-tag:v", "hvc1")
	}

	plan.HDR = buildHDRParams(class, dvMode, v0.HDR)
	plan.Tags = map[string]string{"CLUSTARR_PROFILE": meta.ProfileName + "@" + meta.ProfileHash}
	plan.Expect = buildExpectation(plan, profile)
}

// -- audio/subtitle track planning -----------------------------------------

func buildAudioPlan(info MediaInfo, profile ProfileSpec) []AudioTrackPlan {
	var out []AudioTrackPlan
	for _, a := range info.Audio {
		if len(profile.Audio.Languages) > 0 && !containsString(profile.Audio.Languages, a.Language) {
			continue
		}
		if profile.Audio.DropCommentary && looksLikeCommentary(a) {
			continue
		}
		out = append(out, AudioTrackPlan{
			SourceIndex: a.Index,
			Action:      AudioActionEncode,
			Codec:       profile.Audio.Codec,
			BitrateKbps: BitrateForChannels(a.Channels, profile.Audio.BitratePerChannelKbps),
			Language:    a.Language,
			Default:     a.Disposition.Default,
		})
		if keepOriginalTrack(profile.Audio.KeepOriginal, a) {
			out = append(out, AudioTrackPlan{
				SourceIndex: a.Index,
				Action:      AudioActionCopy,
				Codec:       "copy",
				Language:    a.Language,
			})
		}
	}
	return out
}

func looksLikeCommentary(a AudioStream) bool {
	return a.Disposition.Comment || strings.Contains(strings.ToLower(a.Title), "commentary")
}

func keepOriginalTrack(policy KeepOriginalPolicy, a AudioStream) bool {
	switch policy {
	case KeepOriginalAlways:
		return true
	case KeepOriginalLossless:
		return a.Lossless
	case KeepOriginalAtmos:
		return a.Atmos
	default: // never, or unrecognised
		return false
	}
}

func buildSubtitlePlan(info MediaInfo, profile ProfileSpec, container Container) []int32 {
	var out []int32
	for _, s := range info.Subtitles {
		if s.Bitmap {
			// note §6: MP4 cannot carry bitmap (PGS/VobSub) subtitles.
			if !profile.Subtitles.CopyBitmap || container == ContainerMP4 {
				continue
			}
		} else if !profile.Subtitles.CopyText {
			continue
		}
		out = append(out, s.Index)
	}
	return out
}

func buildMaps(audio []AudioTrackPlan, subs []int32) []string {
	maps := []string{"-map", "0:v:0"}
	for _, a := range audio {
		maps = append(maps, "-map", fmt.Sprintf("0:a:%d", a.SourceIndex))
	}
	for _, s := range subs {
		maps = append(maps, "-map", fmt.Sprintf("0:s:%d", s))
	}
	maps = append(maps, "-map_metadata", "0", "-map_chapters", "0")
	return maps
}

// -- HDR / colour rendering --------------------------------------------------

// hdrSetParamsFilter renders the "-vf setparams=..." filter emitted
// whenever a source carries any HDR classification, so decoders downstream
// are immune to sources whose container tags are missing or wrong (note
// §3.4 pitfall paragraph).
func hdrSetParamsFilter(vs VideoStream) string {
	return fmt.Sprintf("setparams=color_primaries=%s:color_trc=%s:colorspace=%s:range=%s",
		vs.ColorPrimaries, vs.ColorTransfer, vs.ColorSpace, vs.ColorRange)
}

// x265Range maps ffprobe's color_range vocabulary ("tv"/"pc") to x265's
// ("limited"/"full"); anything else (including unset) defaults to limited,
// the overwhelmingly common case for broadcast/BD-sourced HDR.
func x265Range(colorRange string) string {
	if colorRange == "pc" {
		return "full"
	}
	return "limited"
}

// x265Params assembles -x265-params deterministically as an explicit
// ordered list -- never by ranging a map, which Go randomizes. The one
// exception, profile.Video.ExtraX265Params, is appended sorted by key.
func x265Params(threads int32, v VideoSpec, vs VideoStream, class hdrBucket, dvMode DolbyVisionMode) string {
	parts := []string{
		fmt.Sprintf("pools=%d", threads),
		"frame-threads=0",
		fmt.Sprintf("ref=%d", v.Refs),
		fmt.Sprintf("rc-lookahead=%d", v.RCLookahead),
	}
	switch {
	case class == hdrDolbyVision && dvMode == DolbyVisionPassthrough:
		// Profile 5 carries no base-layer HDR10 static metadata to tag; the
		// RPU itself carries dynamic metadata (note §3.5).
		parts = append(parts, "repeat-headers=1")
	case class == hdrHDR10 || class == hdrHDR10Plus || (class == hdrDolbyVision && dvMode == DolbyVisionDowngradeToHDR10):
		parts = append(parts, "hdr10=1", "hdr10-opt=1", "repeat-headers=1")
		if vs.ColorPrimaries != "" {
			parts = append(parts, "colorprim="+vs.ColorPrimaries)
		}
		if vs.ColorTransfer != "" {
			parts = append(parts, "transfer="+vs.ColorTransfer)
		}
		if vs.ColorSpace != "" {
			parts = append(parts, "colormatrix="+vs.ColorSpace)
		}
		parts = append(parts, "range="+x265Range(vs.ColorRange))
		if vs.HDR.MasteringDisplay != nil {
			parts = append(parts, "master-display="+vs.HDR.MasteringDisplay.X265())
		}
		if vs.HDR.ContentLight != nil {
			parts = append(parts, "max-cll="+vs.HDR.ContentLight.X265())
		}
	case class == hdrHLG:
		parts = append(parts, "repeat-headers=1")
	default: // hdrNone
		parts = append(parts, fmt.Sprintf("aq-mode=%d", v.AQMode))
	}

	if len(v.ExtraX265Params) > 0 {
		keys := make([]string, 0, len(v.ExtraX265Params))
		for k := range v.ExtraX265Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+"="+v.ExtraX265Params[k])
		}
	}
	return strings.Join(parts, ":")
}

func buildHDRParams(class hdrBucket, dvMode DolbyVisionMode, hdr HDRInfo) HDRParams {
	p := HDRParams{Mode: "none"}
	switch class {
	case hdrHDR10, hdrHDR10Plus:
		p.Mode = "hdr10"
	case hdrHLG:
		p.Mode = "hlg"
	case hdrDolbyVision:
		p.Mode = "dolbyVision"
		if hdr.DolbyVision != nil {
			p.DolbyVisionProfile = hdr.DolbyVision.Profile
		}
		p.RequiresVBV = dvMode == DolbyVisionPassthrough
	}
	if hdr.MasteringDisplay != nil {
		p.MasterDisplay = hdr.MasteringDisplay.X265()
	}
	if hdr.ContentLight != nil {
		p.MaxCLL = hdr.ContentLight.X265()
	}
	return p
}

// -- per-tier video argument builders ---------------------------------------

// roundFPS rounds a frame-rate Rational to the nearest whole frame per
// second (23.976 -> 24). The float64 division is a local, immediately
// discarded intermediate -- never an exported value.
func roundFPS(r Rational) int32 {
	if r.Den == 0 {
		return 0
	}
	return int32(math.Round(float64(r.Num) / float64(r.Den)))
}

func cpuVideoArgs(v VideoSpec, vs VideoStream, class hdrBucket, dvMode DolbyVisionMode, threads int32) []string {
	hdr := class != hdrNone
	crf := CRFFor(v.CRF, vs.Height, hdr)
	fpsRounded := roundFPS(vs.FrameRate)
	keyint := v.KeyintFactor * fpsRounded

	args := []string{"-c:v", "libx265", "-preset", v.Preset}
	if v.Tune != nil {
		args = append(args, "-tune", *v.Tune)
	}
	args = append(args,
		"-crf", strconv.Itoa(int(crf)),
		"-pix_fmt", v.PixelFormat,
		"-profile:v", v.Profile,
		"-flags", "+cgop",
		"-g", strconv.Itoa(int(keyint)),
		"-keyint_min", strconv.Itoa(int(fpsRounded)),
		"-bf", strconv.Itoa(int(v.BFrames)),
	)
	if class == hdrDolbyVision && dvMode == DolbyVisionPassthrough {
		// note §3.5: Dolby Vision requires VBV settings to enable HRD.
		args = append(args, "-dolbyvision", "1")
		if v.MaxRateKbps != nil {
			args = append(args, "-maxrate", fmt.Sprintf("%dk", *v.MaxRateKbps))
		}
		if v.BufSizeKbps != nil {
			args = append(args, "-bufsize", fmt.Sprintf("%dk", *v.BufSizeKbps))
		}
	}
	args = append(args, "-x265-params", x265Params(threads, v, vs, class, dvMode))
	return args
}

func nvencVideoArgs(v VideoSpec) []string {
	return []string{
		"-c:v", "hevc_nvenc",
		"-preset", v.NVENC.Preset,
		"-tune", v.NVENC.Tune,
		"-rc", "vbr",
		"-cq", strconv.Itoa(int(v.NVENC.CQ)),
		"-b:v", "0",
		"-multipass", v.NVENC.Multipass,
		"-bf", strconv.Itoa(int(v.BFrames)),
		"-b_ref_mode", v.NVENC.BRefMode,
		"-spatial-aq", "1",
		"-temporal-aq", "1",
		"-rc-lookahead", strconv.Itoa(int(v.RCLookahead)),
		"-profile:v", v.Profile,
		"-tier", "high",
		"-pix_fmt", "p010le",
	}
}

func qsvHWInit() []string {
	return []string{"-init_hw_device", "qsv=hw", "-filter_hw_device", "hw", "-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
}

func qsvVideoArgs(v VideoSpec) []string {
	return []string{
		"-c:v", "hevc_qsv",
		"-preset", v.QSV.Preset,
		"-global_quality", strconv.Itoa(int(v.QSV.GlobalQuality)),
		"-extbrc", "1",
		"-look_ahead_depth", strconv.Itoa(int(v.QSV.LookAheadDepth)),
		"-scenario", "archive",
		"-profile:v", v.Profile,
	}
}

func vaapiHWInit() []string {
	return []string{"-init_hw_device", "vaapi=va:/dev/dri/renderD128", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
}

// vaapiQP is VAAPI's fixed constant-QP value. ProfileSpec has no VAAPISpec
// (note §4.3: VAAPI is the vendor-neutral fallback with far fewer tunables
// than QSV/NVENC), so this is a package constant rather than a profile
// field.
const vaapiQP = 24

func vaapiVideoArgs(v VideoSpec) []string {
	return []string{
		"-c:v", "hevc_vaapi",
		"-profile:v", v.Profile,
		"-rc_mode", "CQP",
		"-qp", strconv.Itoa(vaapiQP),
		"-sei", "hdr",
	}
}

// -- output shape -------------------------------------------------------

func containerExt(c Container) string {
	if c == ContainerMP4 {
		return "mp4"
	}
	return "mkv"
}

func containerFormatName(c Container) string {
	if c == ContainerMP4 {
		return "mp4"
	}
	return "matroska"
}

func outputPixFmt(tier Tier, v VideoSpec) string {
	switch tier {
	case TierNVENC, TierVAAPI:
		return "p010le"
	default:
		return v.PixelFormat
	}
}

func buildExpectation(plan *PlanResult, profile ProfileSpec) Expectation {
	streams := int32(1) + int32(len(plan.Audio)) + int32(len(plan.Subtitles))
	return Expectation{
		DurationTolMillis: 1000,
		Streams:           streams,
		VideoCodec:        profile.Video.Codec,
		PixelFormat:       outputPixFmt(plan.Tier, profile.Video),
		MinOutputBytes:    1024,
	}
}

// resolutionClass buckets a source's frame height into the three CRFTable
// tiers.
func resolutionClass(height int32) string {
	switch {
	case height <= 576:
		return "sd"
	case height <= 1080:
		return "hd"
	default:
		return "uhd"
	}
}

// CRFFor picks the x265 constant-rate-factor for a source of the given
// height, applying the table's HDROffset when hdr is true. Exported so the
// Args golden fixtures can assert against it independently of Plan.
func CRFFor(t CRFTable, height int32, hdr bool) int32 {
	var base int32
	switch resolutionClass(height) {
	case "sd":
		base = t.SD
	case "hd":
		base = t.HD
	default:
		base = t.UHD
	}
	if hdr {
		base += t.HDROffset
	}
	return base
}
