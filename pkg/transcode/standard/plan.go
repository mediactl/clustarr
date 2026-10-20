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

// Package standard decides transcode's fixed transcoding standard (spec
// docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md §1, its
// layout since Version 2 docs/superpowers/specs/2026-10-06-mp4-standard-design.md)
// for the in-process engine: one MP4 holding HEVC Main 10 at the source's
// resolution and frame rate, tagged hvc1; HDR10 and HLG kept; Dolby Vision
// stripped to its base layer, or skipped when it has none; per language a
// Dolby surround track (E-AC-3 or AC-3 copied, else AC-3 5.1) plus an AAC
// 2.0 companion, or AAC alone for mono or stereo; chapters and tags kept.
// Plan is pure: the controller plans from the probe summary, the worker
// from a live probe, and the two compare Hash.
package standard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// Version is the standard's revision, part of every profile's status.hash
// (app/transcode/jobspec.ProfileHash). Raise it when Plan or the engine starts
// writing different output for the same input -- a new encoder setting, a
// colour rule -- so files not yet transcoded are planned under the new
// standard. Files already transcoded are final and are never redone.
// 2: the MP4 layout (2026-10-06 MP4 standard design).
// 3: policy.keepCodecs -- 8-bit SDR H.264 copied when a profile keeps it;
// and a source transcoded elsewhere is remuxed, never encoded a second time.
// 4: clustarr's own transcodes in Matroska are remuxed into the MP4 layout.
// 5: an ASS or SSA track becomes a plain SubRip sidecar, not an .ass one
// (Plex's web player burns ASS into the video, which cluster-plex forbids).
// 6: an image subtitle (PGS, DVD) that a text subtitle of its language
// covers is dropped rather than holding the file for OCR (owner,
// 2026-10-20: Akira held for its English DVD track beside three English
// ASS ones).
const Version = 6

// Profile is what a TranscodeProfile decides under the standard.
type Profile struct {
	Name, Hash string // CLUSTARR_PROFILE = Name@Hash
	Quality    int32  // 0-51; mapped per encoder (quality.go)
	// Languages keeps only audio in these languages (commentary always,
	// and every track when none matches); empty keeps all.
	Languages []string
	// SubtitleLanguages keeps only subtitles in these languages; empty
	// keeps all.
	SubtitleLanguages       []string
	NeverTranscodeModifiers []string
	// Container is ignored since Version 2: the standard writes MP4.
	Container transcode.Container
	// MinDuration skips a source shorter than it (policy.minDuration):
	// trailers, extras, samples. Zero considers every file.
	MinDuration time.Duration
	// KeepCodecs (policy.keepCodecs) are the source video codecs kept, not
	// encoded: HEVC always; "h264" keeps 8-bit SDR H.264 as well.
	KeepCodecs []string
}

// Hardware is the encoding device: the pool class's tier and its limits.
type Hardware struct {
	Tier   transcode.Tier
	Limits transcode.Limits
}

// Decision is what Plan decided.
type Decision string

const (
	DecisionSkip      Decision = "skip"
	DecisionCopyVideo Decision = "copyVideo" // video copied, audio (or the container) processed
	DecisionEncode    Decision = "encode"
)

// ColorTags are the encoder's colour description.
type ColorTags struct {
	Primaries, Transfer, Matrix, Range string `json:",omitempty"`
}

// VideoPlan is what happens to the video stream.
type VideoPlan struct {
	SourceIndex int32  // type-relative: the Nth video stream
	Action      string // "copy" | "encode"
	Encoder     string `json:",omitempty"`
	// Options are the encoder's AVOptions, applied strictly.
	Options map[string]string `json:",omitempty"`
	// Decode is "cpu", "nvdec" or "vaapi" (frames stay on the GPU) or
	// "upload" (CPU decode, uploaded by Filter); the QSV tier decodes on
	// VA-API where its pod measured the format (transcode.VAAPIDecodes),
	// else on the CPU, uploading.
	Decode string `json:",omitempty"`
	Filter string `json:",omitempty"`
	// HDR is "sdr", "hdr10" or "hlg".
	HDR   string    `json:",omitempty"`
	Color ColorTags `json:",omitempty"`
	// StripSideData names frame side data removed before encoding:
	// "hdr10plus", "dovi".
	StripSideData []string `json:",omitempty"`
}

// AudioPlan is what happens to one kept audio track.
type AudioPlan struct {
	SourceIndex int32  // type-relative: the Nth audio stream
	Action      string // AudioCopy | AudioAAC | AudioAC3
	Channels    int32  `json:",omitempty"` // encoded
	Layout      string `json:",omitempty"` // encoded: the output layout
	BitRate     int64  `json:",omitempty"` // encoded
	Language    string `json:",omitempty"`
	Title       string `json:",omitempty"`
	Comment     bool   `json:",omitempty"`
	// Default makes the track the file's default audio track; no other
	// output track carries the flag.
	Default bool `json:",omitempty"`
}

// Subtitle actions (SubtitlePlan.Action) and the sidecar format
// (SidecarPlan.Format).
const (
	SubtitleCopy = "copy" // a graft's copy plan (CopyPlan) keeps every subtitle
	// SidecarSRT is every sidecar: SubRip copied out; ASS, SSA, WebVTT,
	// mov_text and text converted to plain SubRip. Plex's web player shows
	// an ASS subtitle only by burning it into the video, a video transcode
	// cluster-plex never allows, while it converts SubRip itself and copies
	// the video (talos, 2026-10-20).
	SidecarSRT = "srt"
)

// HoldImageSubtitles is the skip reason of a file with an image subtitle
// (PGS, DVD) that no text subtitle covers (textCovers): it waits for the
// OCR of the MP4 standard's phase 2, which raises Version, so the file is
// planned again then (ruling R1).
const HoldImageSubtitles = "image subtitles (PGS, DVD) with no text subtitle in their language wait for OCR: MP4 standard phase 2"

// SubtitlePlan is one subtitle track embedded in the output: only a graft's
// copy plan has any (spec §4.1: the standard writes every subtitle beside
// the MP4).
type SubtitlePlan struct {
	SourceIndex     int32  // type-relative: the Nth subtitle stream
	Action          string // SubtitleCopy
	Codec           string // the source's codec, which the converter reads
	Language        string `json:",omitempty"`
	Title           string `json:",omitempty"`
	HearingImpaired bool   `json:",omitempty"`
}

// SidecarPlan is one subtitle track written beside the MP4.
type SidecarPlan struct {
	SourceIndex int32  // type-relative subtitle stream
	Format      string // SidecarSRT
	Codec       string // the source's codec
	// Suffix is the file's name after the video's stem: "en.srt",
	// "en.forced.srt" (pkg/subtitles.ParseSidecar's grammar).
	Suffix string
}

// Expectation is what Verify checks the output against.
type Expectation struct {
	VideoStreams, AudioStreams, SubtitleStreams int32
	VideoCodec, PixelFormat                     string
	DurationMillis                              int64

	// Transfer and Primaries are the colour an HDR output must carry, by
	// FFmpeg's names (smpte2084 or arib-std-b67; bt2020); empty for SDR,
	// whose colour Verify does not check. MasteringDisplay and
	// ContentLight: the output must carry HDR10's static metadata, because
	// the source's first frame does. All four follow from Video.HDR and the
	// source's own metadata, which the stored summary the controller plans
	// from does not hold, so they are left out of Hash (json "-"): the
	// controller's plan and the worker's must still hash alike.
	Transfer, Primaries            string `json:"-"`
	MasteringDisplay, ContentLight bool   `json:"-"`
}

// Result is the standard's decision for one file: what Plan returns and
// the in-process engine runs.
type Result struct {
	Decision  Decision
	Reason    string
	Container transcode.Container
	Video     VideoPlan
	Audio     []AudioPlan
	Subtitles []SubtitlePlan // embedded in the output: none from Plan
	Sidecars  []SidecarPlan  `json:",omitempty"` // written beside it
	// Dropped names each source subtitle the output carries neither way.
	Dropped     []string `json:",omitempty"`
	Attachments bool     // every attachment the source has (a graft's copy plan); MP4 carries none
	Chapters    bool     // every chapter the source has (always: a summary has none to count)
	Tags        map[string]string
	Expect      Expectation
}

// Hash is the plan's identity: sha256 of its canonical JSON (struct fields
// in declaration order, map keys sorted by encoding/json), less what only a
// live probe knows -- the video's stream index (a cover-art stream before
// it) and the exact duration -- so the controller's plan from the stored
// summary and the worker's from a probe of the same file hash alike.
func (p Result) Hash() string {
	p.Video.SourceIndex = 0
	p.Expect.DurationMillis = 0
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Audio actions (AudioPlan.Action).
const (
	AudioCopy = "copy"
	AudioAAC  = "aac"
	AudioAC3  = "ac3"
)

// AC3BitRate and AC3Layout are a surround track encoded to AC-3 (spec §3;
// "5.1" has back surrounds, which FFmpeg's ac3 encoder takes);
// AACBitRatePerChannel makes AAC stereo 160 kbps and mono 80 kbps.
const (
	AC3BitRate           = 640000
	AC3Layout            = "5.1"
	AACBitRatePerChannel = 80000
)

// aacLayouts are the layouts the standard encodes AAC in.
var aacLayouts = map[int32]string{1: "mono", 2: "stereo"}

// keptH264Dropped reports info's video as H.264 the profile no longer keeps
// (policy.keepCodecs): a clustarr remux of it, tagged and final while the
// profile kept it, is encoded now.
func keptH264Dropped(info transcode.MediaInfo, profile Profile) bool {
	vi := primaryVideo(info.Video)
	return vi >= 0 && info.Video[vi].Codec == "h264" && !slices.Contains(profile.KeepCodecs, "h264")
}

// Plan decides the standard for info, under profile, on hw.
func Plan(info transcode.MediaInfo, profile Profile, hw Hardware) Result {
	p := Result{
		Container: transcode.ContainerMP4,
		Tags:      map[string]string{"CLUSTARR_PROFILE": profile.Name + "@" + profile.Hash},
	}
	// A transcoded file is final (spec §5), whichever profile or hash wrote
	// it: an edit or a new Version re-transcodes nothing. One written before
	// the MP4 layout (HEVC in Matroska, Version 1) is remuxed into it, its
	// video copied below and never encoded a second time (owner, 2026-10-15).
	tag := formatTag(info.Tags, "CLUSTARR_PROFILE")
	// The one exception: a remux that copied H.264 because the profile kept
	// it (policy.keepCodecs) is encoded once the profile no longer keeps
	// H.264 (owner, 2026-10-19). Its video is the release's, not clustarr's.
	reencode := tag != "" && keptH264Dropped(info, profile)
	if tag != "" && containerOf(info.Format.Name) == transcode.ContainerMP4 && !reencode {
		return skip(p, "already transcoded (CLUSTARR_PROFILE "+tag+")")
	}
	if d := info.Format.Duration; profile.MinDuration > 0 && d > 0 && d < profile.MinDuration {
		return skip(p, fmt.Sprintf("the source is %s long, under policy.minDuration %s", d.Round(time.Second), profile.MinDuration))
	}
	if info.Modifier != "" && slices.Contains(profile.NeverTranscodeModifiers, info.Modifier) {
		return skip(p, fmt.Sprintf("the source's %s modifier is never transcoded (policy.neverTranscodeModifiers)", info.Modifier))
	}
	vi := primaryVideo(info.Video)
	if vi < 0 {
		return skip(p, "no video stream")
	}
	v := info.Video[vi]

	hdr, strip, ok := hdrMode(v)
	if !ok {
		return skip(p, "Dolby Vision profile 5 has no HDR10 or HLG base layer: without the Dolby Vision layer it plays with wrong colours")
	}
	subLangs := resolveLanguages(profile.SubtitleLanguages, info.OriginalLanguage)
	for _, s := range info.Subtitles {
		if s.Bitmap && subLangs.keeps(s.Language) && !textCovers(info.Subtitles, s) {
			return skip(p, HoldImageSubtitles)
		}
	}
	var audioDropped []string
	p.Audio, audioDropped = planAudio(info.Audio, resolveLanguages(profile.Languages, info.OriginalLanguage))
	p.Subtitles = []SubtitlePlan{}
	p.Sidecars, p.Dropped = planSubtitles(info.Subtitles, subLangs)
	p.Dropped = append(audioDropped, p.Dropped...)
	p.Attachments = false // MP4 carries none
	p.Chapters = true
	p.Expect = Expectation{
		VideoStreams: 1, AudioStreams: int32(len(p.Audio)), SubtitleStreams: int32(len(p.Subtitles)),
		VideoCodec: "hevc", PixelFormat: "yuv420p10le",
		DurationMillis: info.Format.Duration.Milliseconds(),
	}

	if c := colorTags(hdr); c.Transfer != "" {
		p.Expect.Transfer, p.Expect.Primaries = c.Transfer, c.Primaries
		p.Expect.MasteringDisplay = hdr == "hdr10" && v.HDR.MasteringDisplay != nil
		p.Expect.ContentLight = hdr == "hdr10" && v.HDR.ContentLight != nil
	}

	eight := transcode.EightBitTarget(v)
	if eight {
		p.Expect.PixelFormat = "yuv420p"
	}
	// A 10-bit file is never encoded down to the 8-bit target.
	compliant := v.Codec == "hevc" && (tenBit(v) || (eight && v.PixFmt == "yuv420p")) && !isDolbyVision(v.HDR.Format)
	// A profile that keeps H.264 (policy.keepCodecs) copies 8-bit SDR H.264
	// as compliant HEVC is copied (owner, 2026-10-15). Hi10P and HDR H.264
	// are still encoded: many players, Apple's among them, cannot decode them;
	// so is Dolby Vision H.264 (profile 9), stripped as it is from HEVC.
	keptH264 := v.Codec == "h264" && slices.Contains(profile.KeepCodecs, "h264") &&
		v.PixFmt == "yuv420p" && hdr == "sdr" && !isDolbyVision(v.HDR.Format)
	compliant = compliant || keptH264
	audioAsIs := len(p.Audio) == len(info.Audio)
	for _, a := range p.Audio {
		audioAsIs = audioAsIs && a.Action == AudioCopy
	}
	subsAsIs := len(info.Subtitles) == 0 && len(info.Attachments) == 0
	sameContainer := containerOf(info.Format.Name) == p.Container
	switch {
	case keptH264 && audioAsIs && subsAsIs && sameContainer:
		return skip(p, "already H.264 in the MP4 standard's layout, kept (policy.keepCodecs)")
	case compliant && audioAsIs && subsAsIs && sameContainer:
		return skip(p, "already HEVC in the MP4 standard's layout")
	case compliant:
		p.Decision = DecisionCopyVideo
		switch {
		case tag != "":
			p.Reason = "clustarr's own Matroska transcode remuxed into the MP4 layout: its video copied, never encoded again"
		case keptH264:
			p.Reason = "H.264 video copied into the MP4 layout (policy.keepCodecs); audio and subtitles processed"
		default:
			p.Reason = "HEVC video copied into the MP4 layout; audio and subtitles processed"
		}
		p.Video = VideoPlan{SourceIndex: int32(vi), Action: "copy"}
		p.Expect.VideoCodec, p.Expect.PixelFormat = v.Codec, v.PixFmt
		return p
	}
	// A source another tool transcoded (an FFmpeg HEVC or AV1 encode:
	// commonv1.MediaInfo.TranscodedElsewhere, Tdarr's QSV MKVs on the owner's
	// library) is final: remuxed above when its video is compliant, never
	// encoded a second time (owner, 2026-10-15). So is clustarr's own.
	if tag != "" && !reencode {
		return skip(p, "already transcoded (CLUSTARR_PROFILE "+tag+"): final, never encoded a second time")
	}
	if (&commonv1.MediaInfo{VideoEncoder: v.Encoder}).TranscodedElsewhere() {
		return skip(p, "transcoded elsewhere ("+v.Encoder+"): final, never encoded a second time")
	}
	p.Decision = DecisionEncode
	p.Reason = "encode to HEVC Main 10"
	if eight {
		p.Reason = "encode to HEVC Main (8-bit: SDR at 1080p or less)"
	}
	p.Video = encodeVideo(v, int32(vi), profile.Quality, hw, hdr, strip, eight)
	return p
}

func skip(p Result, reason string) Result {
	return Result{Decision: DecisionSkip, Reason: reason, Container: p.Container, Tags: p.Tags}
}

// primaryVideo is the first video stream that is not cover art.
func primaryVideo(vs []transcode.VideoStream) int {
	for i, v := range vs {
		switch v.Codec {
		case "mjpeg", "png", "bmp", "gif", "webp":
			continue
		}
		return i
	}
	return -1
}

func tenBit(v transcode.VideoStream) bool {
	return v.BitDepth == 10 || v.PixFmt == "yuv420p10le" || v.PixFmt == "p010le"
}

func isDolbyVision(f commonv1.HdrFormat) bool {
	return strings.HasPrefix(string(f), "dolbyVision")
}

// hdrMode is the output's HDR and the side data to strip; ok is false for
// Dolby Vision profile 5 (format dolbyVision: no compatible base layer).
func hdrMode(v transcode.VideoStream) (hdr string, strip []string, ok bool) {
	switch v.HDR.Format {
	case commonv1.HdrFormatHDR10, commonv1.HdrFormatPQ10:
		return "hdr10", []string{"hdr10plus"}, true
	case commonv1.HdrFormatHDR10Plus:
		return "hdr10", []string{"hdr10plus"}, true
	case commonv1.HdrFormatHLG10:
		return "hlg", nil, true
	case commonv1.HdrFormatDolbyVisionHDR10, commonv1.HdrFormatDolbyVisionHDR10Plus:
		return "hdr10", []string{"dovi", "hdr10plus"}, true
	case commonv1.HdrFormatDolbyVisionHLG:
		return "hlg", []string{"dovi"}, true
	case commonv1.HdrFormatDolbyVisionSDR:
		return "sdr", []string{"dovi"}, true
	case commonv1.HdrFormatDolbyVision:
		if d := v.HDR.DolbyVision; d != nil && d.BLSignalCompatibilityID == 1 {
			return "hdr10", []string{"dovi", "hdr10plus"}, true
		}
		return "", nil, false
	}
	return "sdr", nil, true
}

func colorTags(hdr string) ColorTags {
	switch hdr {
	case "hdr10":
		return ColorTags{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}
	case "hlg":
		return ColorTags{Primaries: "bt2020", Transfer: "arib-std-b67", Matrix: "bt2020nc", Range: "tv"}
	}
	return ColorTags{}
}

// encodeVideo plans the encode on hw's tier: HEVC Main 10, or HEVC Main
// for the 8-bit target (eight: SDR at 1080p or less), with the surface
// format each filter names for it.
func encodeVideo(v transcode.VideoStream, index, quality int32, hw Hardware, hdr string, strip []string, eight bool) VideoPlan {
	vp := VideoPlan{SourceIndex: index, Action: "encode", HDR: hdr, Color: colorTags(hdr), StripSideData: strip}
	fmtFor := func(tenBit string) string {
		if eight {
			return "nv12"
		}
		return tenBit
	}
	switch hw.Tier {
	case transcode.TierNVENC:
		vp.Encoder = "hevc_nvenc"
		vp.Options = nvencOptions(quality, v.BitRateKbps)
		if transcode.NVDECDecodes(hw.Limits, v) {
			vp.Decode, vp.Filter = "nvdec", "scale_cuda=format="+fmtFor("p010le")
		} else {
			vp.Decode, vp.Filter = "upload", "hwupload,scale_cuda=format="+fmtFor("p010le")
		}
	case transcode.TierQSV:
		vp.Encoder, vp.Decode = "hevc_qsv", "cpu"
		// medium, not veryslow (2026-10-16): on the NUC's QSV, four
		// 3-minute x264 samples came out 1.8-5.4% larger at the same or a
		// slightly higher VMAF, in half the time once decoded on the GPU
		// (veryslow 13.5-16.6 s, medium 6.9-8.4 s; hack/presetbench).
		vp.Options = map[string]string{"global_quality": itoa(quality), "preset": "medium", "profile": "main10"}
		vp.Filter = "hwupload=extra_hw_frames=64,vpp_qsv=format=" + fmtFor("p010")
		if transcode.VAAPIDecodes(hw.Limits, v) {
			// Decoded on the GPU and mapped to QSV, no frame touching the CPU.
			vp.Decode, vp.Filter = "vaapi", "hwmap=derive_device=qsv,format=qsv,vpp_qsv=format="+fmtFor("p010")
		}
	case transcode.TierVAAPI:
		vp.Encoder, vp.Decode = "hevc_vaapi", "cpu"
		vp.Options = map[string]string{"qp": itoa(quality), "profile": "main10"}
		vp.Filter = "hwupload,scale_vaapi=format=" + fmtFor("p010")
	default:
		vp.Encoder, vp.Decode = "libx265", "cpu"
		vp.Options = x265Options(v, quality)
		vp.Filter = "format=yuv420p10le"
		if eight {
			vp.Filter = "format=yuv420p"
		}
	}
	if eight {
		vp.Options["profile"] = "main"
	}
	return vp
}

// nvencOptions are the owner's archival settings (spec, Spike result):
// preset p7, constant QP with spatial and temporal AQ. Under constqp NVENC
// ignores cq, so quality maps to qp, one below it (the archival qp 23 at
// the standard's default quality 24).
// NVENCMaxBitratePercent caps an NVENC encode's bit rate at this share of
// the source's video bit rate (the old engine's
// video.nvenc.maxBitratePercent default, restored 2026-10-05). Constant QP
// alone made a lean source larger: five jobs on kind-cluster-plex failed
// policy.maxOutputToSourcePercent, one at 133% of its source.
const NVENCMaxBitratePercent = 70

// nvencOptions is hevc_nvenc at the standard's quality: with the source's
// video bit rate known (sourceKbps > 0), VBR at cq = quality - 1 with no
// target bit rate, capped at NVENCMaxBitratePercent of the source with a
// two-second-of-cap buffer; unknown, constant QP at the same value. (Under
// constqp NVENC ignores cq and any maxrate, which is why the cap needs VBR.)
func nvencOptions(quality, sourceKbps int32) map[string]string {
	q := itoa(max(quality-1, 0))
	o := map[string]string{"preset": "p7", "spatial-aq": "1", "temporal-aq": "1", "profile": "main10"}
	if sourceKbps <= 0 {
		o["rc"], o["qp"] = "constqp", q
		return o
	}
	capKbps := max(int64(sourceKbps)*NVENCMaxBitratePercent/100, 1)
	o["rc"], o["cq"], o["b"] = "vbr", q, "0"
	o["maxrate"] = strconv.FormatInt(capKbps, 10) + "k"
	o["bufsize"] = strconv.FormatInt(2*capKbps, 10) + "k"
	return o
}

// x265Options are today's profile defaults: preset slow, 8 B-frames, 4
// references, a 40-frame lookahead and a keyframe every 10 seconds.
func x265Options(v transcode.VideoStream, quality int32) map[string]string {
	keyint := 240
	if v.FrameRate.Num > 0 && v.FrameRate.Den > 0 {
		keyint = int(math.Round(10 * float64(v.FrameRate.Num) / float64(v.FrameRate.Den)))
	}
	return map[string]string{
		"crf": itoa(quality), "preset": "slow", "profile": "main10",
		"x265-params": fmt.Sprintf("log-level=error:bframes=8:ref=4:rc-lookahead=40:keyint=%d", keyint),
	}
}

// planAudio is spec §3: after the languages filter, per language in the
// order the source first has it, the primary track -- E-AC-3 or AC-3
// copied, else AC-3 5.1, plus an AAC 2.0 companion when it is surround;
// AAC alone when it is mono or stereo -- then each commentary track as AAC
// of at most 2 channels. Every other track is dropped. The source's
// default language keeps the default (ruling R4).
func planAudio(as []transcode.AudioStream, languages wanted) ([]AudioPlan, []string) {
	type group struct {
		main, comments []int
	}
	var order []string
	groups := map[string]*group{}
	kept, dropped := keptAudio(as, languages)
	for _, i := range kept {
		lang := as[i].Language
		g := groups[lang]
		if g == nil {
			g = &group{}
			groups[lang] = g
			order = append(order, lang)
		}
		if commentary(as[i]) {
			g.comments = append(g.comments, i)
		} else {
			g.main = append(g.main, i)
		}
	}
	var out []AudioPlan
	for _, lang := range order {
		g := groups[lang]
		if p := primaryAudio(as, g.main); p >= 0 {
			out = append(out, primaryPlans(as[p], int32(p))...)
		}
		for _, i := range g.comments {
			out = append(out, commentaryPlan(as[i], int32(i)))
		}
	}
	markDefault(out, as)
	return out, dropped
}

// keptAudio is the languages filter: the tracks in a kept language, an
// untagged track (most often the original or English) and commentary in a
// kept language -- unless no track but commentary names a kept language,
// then every track, so a file never loses all its audio. Each track it
// drops is named.
func keptAudio(as []transcode.AudioStream, languages wanted) (keep []int, dropped []string) {
	matched := false
	for _, a := range as {
		if !commentary(a) && languages.names(a.Language) {
			matched = true
		}
	}
	for i, a := range as {
		if !matched || languages.keeps(a.Language) {
			keep = append(keep, i)
			continue
		}
		dropped = append(dropped, fmt.Sprintf("audio %d (%s): not a kept language", i, a.Language))
	}
	return keep, dropped
}

// OriginalLanguage is the language list entry that names the file's item's
// original language (MediaInfo.OriginalLanguage).
const OriginalLanguage = "original"

// wanted is a profile's language list resolved for one file: the base
// languages it keeps, or all of them.
type wanted struct {
	all  bool
	keep map[string]bool
}

// resolveLanguages resolves list for a file whose item's original language
// is original. An empty list keeps every language; so does one naming the
// original when that is unknown -- which track is the original cannot be
// told, and a dropped one is lost with the source. Codes are compared as
// languages: "en", "eng" and TMDB's "en" are one (pkg/lang).
func resolveLanguages(list []string, original string) wanted {
	if len(list) == 0 {
		return wanted{all: true}
	}
	w := wanted{keep: map[string]bool{}}
	for _, c := range list {
		if strings.EqualFold(strings.TrimSpace(c), OriginalLanguage) {
			b := baseLanguage(original)
			if b == "" {
				return wanted{all: true}
			}
			w.keep[b] = true
		} else if b := baseLanguage(c); b != "" {
			w.keep[b] = true
		}
	}
	return w
}

// names reports whether code is a language w keeps by name.
func (w wanted) names(code string) bool {
	b := baseLanguage(code)
	return b != "" && w.keep[b]
}

// keeps reports whether a track tagged code stays: every track when w
// keeps all, else one in a kept language or untagged.
func (w wanted) keeps(code string) bool {
	return w.all || baseLanguage(code) == "" || w.names(code)
}

// baseLanguage is code's ISO 639-1 base language ("ja" for "jpn" or
// "ja-JP"), "" for an untagged, "und" or unknown code.
func baseLanguage(code string) string {
	t, ok := lang.Normalize(code)
	if !ok {
		return ""
	}
	b, _, _ := strings.Cut(string(t), "-")
	return strings.ToLower(b)
}

// channels is a track's channel count, stereo when the probe has none.
func channels(a transcode.AudioStream) int32 {
	if a.Channels <= 0 {
		return 2
	}
	return a.Channels
}

// primaryAudio is the language's primary track among idx: the first
// surround E-AC-3, else the first surround AC-3, else the track with the
// most channels (the first at a tie); -1 for none.
func primaryAudio(as []transcode.AudioStream, idx []int) int {
	rank := func(a transcode.AudioStream) (tier int, ch int32) {
		if channels(a) > 2 {
			switch a.Codec {
			case "eac3":
				return 3, 0
			case "ac3":
				return 2, 0
			}
			return 1, channels(a)
		}
		return 0, channels(a)
	}
	best := -1
	for _, i := range idx {
		if best < 0 {
			best = i
			continue
		}
		t, c := rank(as[i])
		bt, bc := rank(as[best])
		if t > bt || (t == bt && c > bc) {
			best = i
		}
	}
	return best
}

func aacPlan(i int32, lang string, ch int32, title string) AudioPlan {
	return AudioPlan{
		SourceIndex: i, Action: AudioAAC, Channels: ch, Layout: aacLayouts[ch],
		BitRate: AACBitRatePerChannel * int64(ch), Language: lang, Title: title,
	}
}

func aacTitle(ch int32) string {
	if ch == 1 {
		return "Mono"
	}
	return "Stereo"
}

// primaryPlans is a primary track's one or two outputs: for surround, the
// AAC 2.0 companion first and the Dolby track last. A copied track keeps
// its title; an encoded one is named for what it is (ruling R5).
func primaryPlans(a transcode.AudioStream, i int32) []AudioPlan {
	ch := channels(a)
	if ch <= 2 {
		if a.Codec == "aac" {
			return []AudioPlan{{SourceIndex: i, Action: AudioCopy, Language: a.Language, Title: a.Title}}
		}
		return []AudioPlan{aacPlan(i, a.Language, ch, aacTitle(ch))}
	}
	surround := AudioPlan{SourceIndex: i, Action: AudioCopy, Language: a.Language, Title: a.Title}
	if a.Codec != "eac3" && a.Codec != "ac3" {
		surround = AudioPlan{
			SourceIndex: i, Action: AudioAC3, Channels: 6, Layout: AC3Layout, BitRate: AC3BitRate,
			Language: a.Language, Title: "Dolby Digital 5.1",
		}
	}
	// AAC first, the Dolby track after it (the owner's correction,
	// 2026-10-07): a player that takes a language's first track gets the
	// one every client decodes.
	return []AudioPlan{aacPlan(i, a.Language, 2, "Stereo"), surround}
}

// commentaryPlan is a commentary track as AAC of at most 2 channels,
// copied when it already is; its title stays.
func commentaryPlan(a transcode.AudioStream, i int32) AudioPlan {
	if a.Codec == "aac" && channels(a) <= 2 {
		return AudioPlan{SourceIndex: i, Action: AudioCopy, Language: a.Language, Title: a.Title, Comment: true}
	}
	p := aacPlan(i, a.Language, min(channels(a), 2), a.Title)
	p.Comment = true
	return p
}

// markDefault flags the first non-commentary output of the language whose
// source track carries the default flag, else of the first language
// (ruling R4); with only commentary, the first output.
func markDefault(out []AudioPlan, as []transcode.AudioStream) {
	if len(out) == 0 {
		return
	}
	lang, found := "", false
	for _, p := range out {
		if !p.Comment && as[p.SourceIndex].Disposition.Default {
			lang, found = p.Language, true
			break
		}
	}
	for i := range out {
		if !out[i].Comment && (!found || out[i].Language == lang) {
			out[i].Default = true
			return
		}
	}
	out[0].Default = true
}

// textSubtitles become .srt sidecars, ASS and SSA ones as plain text.
var textSubtitles = []string{"subrip", "srt", "text", "webvtt", "mov_text", "ass", "ssa"}

var (
	// forcedTitle and fullTitle read a track's title where its flags are
	// unset -- Matroska had no hearing-impaired flag until 2022, and many
	// releases leave forced unset -- "Forced", "Signs & Songs" (ruling R2),
	// but never a full track's "Dialogue + Signs & Songs" (final review I5).
	forcedTitle = regexp.MustCompile(`(?i)\b(forced|signs?|songs?)\b`)
	fullTitle   = regexp.MustCompile(`(?i)\b(full|dialogue)\b`)
	sdhTitle    = regexp.MustCompile(`(?i)\b(sdh|hi|cc|hearing[ -]impaired)\b`)
)

// forcedSubtitle is the forced flag, or a forced title that is not a full
// track's.
func forcedSubtitle(s transcode.SubtitleStream) bool {
	return s.Disposition.Forced || (forcedTitle.MatchString(s.Title) && !fullTitle.MatchString(s.Title))
}

// sdhSubtitle is the hearing-impaired flag, or an SDH title.
func sdhSubtitle(s transcode.SubtitleStream) bool {
	return s.Disposition.HearingImpaired || sdhTitle.MatchString(s.Title)
}

// sidecarLang is the language segment of a sidecar's name: lang.Normalize's
// base, the ISO 639-1 code where one exists (Plex reads 639-1 or 639-2/B),
// its region dropped; "" for an untagged or unknown language.
func sidecarLang(code string) string {
	tag, ok := lang.Normalize(code)
	if !ok {
		return ""
	}
	base, _, _ := strings.Cut(string(tag), "-")
	return base
}

// sidecarSuffix is <lang>[.forced|.sdh].<ext>, or [forced.|sdh.]<ext>
// with no language: Plex's local subtitle layout, and
// pkg/subtitles.ParseSidecar's grammar.
func sidecarSuffix(s transcode.SubtitleStream, ext string) string {
	var parts []string
	if l := sidecarLang(s.Language); l != "" {
		parts = append(parts, l)
	}
	switch {
	case forcedSubtitle(s):
		parts = append(parts, "forced")
	case sdhSubtitle(s):
		parts = append(parts, "sdh")
	}
	return strings.Join(append(parts, ext), ".")
}

// planSubtitles is spec §4.1 with rulings R2 and R3: every text subtitle,
// ASS and SSA included since Version 5, becomes an .srt sidecar, one per
// name, the first track to take a name keeping it; anything else is dropped
// and named. Plan holds a file with a kept bitmap stream no text track
// covers before it gets here, so a bitmap one here is covered (Version 6).
func planSubtitles(ss []transcode.SubtitleStream, languages wanted) (sidecars []SidecarPlan, dropped []string) {
	taken := map[string]bool{}
	for i, s := range ss {
		if !languages.keeps(s.Language) {
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s %q %s): not a kept language", i, s.Codec, s.Title, s.Language))
			continue
		}
		if s.Bitmap {
			// Plan holds the file unless a text track covers it.
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s %q %s): an image subtitle an %s text track covers", i, s.Codec, s.Title, s.Language, baseLanguage(s.Language)))
			continue
		}
		if !slices.Contains(textSubtitles, s.Codec) {
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s): a codec the MP4 standard does not carry", i, s.Codec))
			continue
		}
		format := SidecarSRT
		suffix := sidecarSuffix(s, format)
		if taken[suffix] {
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s %q): a second %s sidecar", i, s.Codec, s.Title, suffix))
			continue
		}
		taken[suffix] = true
		sidecars = append(sidecars, SidecarPlan{SourceIndex: int32(i), Format: format, Codec: s.Codec, Suffix: suffix})
	}
	return sidecars, dropped
}

// textCovers is whether a text subtitle that becomes a sidecar carries what
// the image subtitle img does: its language, and forced only as img is. A
// forced track shows only what the audio does not say, which a full one in
// its place would bury in every line. An untagged track's language is
// unknown, so it covers nothing and is covered by nothing.
func textCovers(ss []transcode.SubtitleStream, img transcode.SubtitleStream) bool {
	l := baseLanguage(img.Language)
	if l == "" {
		return false
	}
	for _, t := range ss {
		if !t.Bitmap && slices.Contains(textSubtitles, t.Codec) && baseLanguage(t.Language) == l &&
			forcedSubtitle(t) == forcedSubtitle(img) {
			return true
		}
	}
	return false
}

func commentary(a transcode.AudioStream) bool {
	return a.Disposition.Comment || strings.Contains(strings.ToLower(a.Title), "commentary")
}

// containerOf maps a container name to the one the standard writes: a
// demuxer's list ("matroska,webm", "mov,mp4,m4a,...") from a probe, or the
// stored summary's extension-like name ("mkv", "mp4").
func containerOf(format string) transcode.Container {
	for _, name := range strings.Split(format, ",") {
		switch strings.TrimSpace(name) {
		case "matroska", "webm", "mkv":
			return transcode.ContainerMKV
		case "mov", "mp4", "m4v", "m4a":
			return transcode.ContainerMP4
		}
	}
	return ""
}

func itoa(v int32) string { return strconv.Itoa(int(v)) }

// formatTag is tags' key compared without regard to case: Matroska keeps a
// tag's key as written, other muxers may change its case.
func formatTag(tags map[string]string, key string) string {
	if v, ok := tags[key]; ok {
		return v
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}
