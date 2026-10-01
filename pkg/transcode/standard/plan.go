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

// Package standard decides squasharr's fixed transcoding standard (spec
// docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md §1) for the
// in-process engine: HEVC Main 10 at the source's resolution and frame
// rate; HDR10 and HLG kept; Dolby Vision stripped to its base layer, or
// skipped when it has none; Apple TV's direct-play audio (AAC, AC-3,
// E-AC-3) copied and everything else AAC at 64 kbps per channel, at most
// 5.1; every subtitle, attachment, chapter and tag copied. Plan is pure:
// the controller plans from the probe summary, the worker from a live
// probe, and the two compare Hash.
package standard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// Profile is what a TranscodeProfile decides under the standard.
type Profile struct {
	Name, Hash string // CLUSTARR_PROFILE = Name@Hash
	Quality    int32  // 0-51; mapped per encoder (quality.go)
	// Languages keeps only audio in these languages (commentary always,
	// and every track when none matches); empty keeps all.
	Languages               []string
	NeverTranscodeModifiers []string
	Container               transcode.Container
	// MinDuration skips a source shorter than it (policy.minDuration):
	// trailers, extras, samples. Zero considers every file.
	MinDuration time.Duration
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
	// Decode is "cpu", "nvdec" (frames stay on the GPU) or "upload" (CPU
	// decode, uploaded by Filter); Intel tiers decode on the CPU and upload.
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
	Action      string // "copy" | "aac"
	Channels    int32  `json:",omitempty"` // aac
	Layout      string `json:",omitempty"` // aac: the output layout, canonical for Channels (aacLayouts)
	BitRate     int64  `json:",omitempty"` // aac
	Language    string `json:",omitempty"`
	Title       string `json:",omitempty"`
	Comment     bool   `json:",omitempty"`
}

// Expectation is what Verify checks the output against.
type Expectation struct {
	VideoStreams, AudioStreams, SubtitleStreams int32
	VideoCodec, PixelFormat                     string
	DurationMillis                              int64
}

// Result is the standard's decision for one file: what Plan returns and
// the in-process engine runs.
type Result struct {
	Decision    Decision
	Reason      string
	Container   transcode.Container
	Video       VideoPlan
	Audio       []AudioPlan
	Subtitles   []int32 // type-relative: every one MKV, the mov_text ones MP4
	Attachments bool    // every attachment the source has; MP4 carries none
	Chapters    bool    // every chapter the source has (always: a summary has none to count)
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

// aacLayouts are the layouts FFmpeg's AAC encoder takes, by channel count
// ("5.1" has back surrounds; "5.1(side)" is not among them).
var aacLayouts = map[int32]string{1: "mono", 2: "stereo", 3: "3.0", 4: "4.0", 5: "5.0", 6: "5.1"}

// directPlay are the audio codecs Apple TV plays directly: copied as is.
var directPlay = []string{"aac", "ac3", "eac3"}

// Plan decides the standard for info, under profile, on hw.
func Plan(info transcode.MediaInfo, profile Profile, hw Hardware) Result {
	p := Result{
		Container: profile.Container,
		Tags:      map[string]string{"CLUSTARR_PROFILE": profile.Name + "@" + profile.Hash},
	}
	if p.Container == "" {
		p.Container = transcode.ContainerMKV
	}
	if tag := formatTag(info.Tags, "CLUSTARR_PROFILE"); tag != "" && tag == p.Tags["CLUSTARR_PROFILE"] {
		return skip(p, "this profile already wrote this file (CLUSTARR_PROFILE "+tag+")")
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
	p.Audio = planAudio(info.Audio, profile.Languages)
	p.Subtitles = []int32{}
	for i, s := range info.Subtitles {
		if p.Container != transcode.ContainerMP4 || s.Codec == "mov_text" {
			p.Subtitles = append(p.Subtitles, int32(i))
		}
	}
	p.Attachments = p.Container == transcode.ContainerMKV
	p.Chapters = true
	p.Expect = Expectation{
		VideoStreams: 1, AudioStreams: int32(len(p.Audio)), SubtitleStreams: int32(len(p.Subtitles)),
		VideoCodec: "hevc", PixelFormat: "yuv420p10le",
		DurationMillis: info.Format.Duration.Milliseconds(),
	}

	eight := transcode.EightBitTarget(v)
	if eight {
		p.Expect.PixelFormat = "yuv420p"
	}
	// A 10-bit file is never encoded down to the 8-bit target.
	compliant := v.Codec == "hevc" && (tenBit(v) || (eight && v.PixFmt == "yuv420p")) && !isDolbyVision(v.HDR.Format)
	audioAsIs := len(p.Audio) == len(info.Audio)
	for _, a := range p.Audio {
		audioAsIs = audioAsIs && a.Action == "copy"
	}
	sameContainer := containerOf(info.Format.Name) == p.Container
	switch {
	case compliant && audioAsIs && sameContainer:
		return skip(p, "already HEVC Main 10 with Apple TV direct-play audio")
	case compliant:
		p.Decision = DecisionCopyVideo
		p.Reason = "HEVC Main 10 video copied; audio or container processed"
		p.Video = VideoPlan{SourceIndex: int32(vi), Action: "copy"}
		p.Expect.PixelFormat = v.PixFmt
		return p
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
		vp.Options = nvencOptions(quality)
		if transcode.NVDECDecodes(hw.Limits, v) {
			vp.Decode, vp.Filter = "nvdec", "scale_cuda=format="+fmtFor("p010le")
		} else {
			vp.Decode, vp.Filter = "upload", "hwupload,scale_cuda=format="+fmtFor("p010le")
		}
	case transcode.TierQSV:
		vp.Encoder, vp.Decode = "hevc_qsv", "cpu"
		vp.Options = map[string]string{"global_quality": itoa(quality), "preset": "veryslow", "profile": "main10"}
		vp.Filter = "hwupload=extra_hw_frames=64,vpp_qsv=format=" + fmtFor("p010")
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
func nvencOptions(quality int32) map[string]string {
	return map[string]string{
		"preset": "p7", "rc": "constqp", "qp": itoa(max(quality-1, 0)),
		"spatial-aq": "1", "temporal-aq": "1", "profile": "main10",
	}
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

func planAudio(as []transcode.AudioStream, languages []string) []AudioPlan {
	keep := func(a transcode.AudioStream) bool {
		return len(languages) == 0 || commentary(a) || slices.Contains(languages, a.Language)
	}
	any := false
	for _, a := range as {
		if len(languages) > 0 && !commentary(a) && slices.Contains(languages, a.Language) {
			any = true
		}
	}
	var out []AudioPlan
	for i, a := range as {
		if any && !keep(a) {
			continue
		}
		ap := AudioPlan{SourceIndex: int32(i), Action: "copy", Language: a.Language, Title: a.Title, Comment: commentary(a)}
		if !slices.Contains(directPlay, a.Codec) {
			ch := min(a.Channels, 6)
			if ch <= 0 {
				ch = 2
			}
			ap.Action, ap.Channels, ap.BitRate = "aac", ch, 64000*int64(ch)
			ap.Layout = aacLayouts[ch]
		}
		out = append(out, ap)
	}
	return out
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
