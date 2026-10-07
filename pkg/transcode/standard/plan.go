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
// (app/squash/worker.ProfileHash). Raise it when Plan or the engine starts
// writing different output for the same input -- a new encoder setting, a
// colour rule -- so files not yet transcoded are planned under the new
// standard. Files already transcoded are final and are never redone.
// 2: the MP4 layout (2026-10-06 MP4 standard design).
const Version = 2

// Profile is what a TranscodeProfile decides under the standard.
type Profile struct {
	Name, Hash string // CLUSTARR_PROFILE = Name@Hash
	Quality    int32  // 0-51; mapped per encoder (quality.go)
	// Languages keeps only audio in these languages (commentary always,
	// and every track when none matches); empty keeps all.
	Languages               []string
	NeverTranscodeModifiers []string
	// Container is ignored since Version 2: the standard writes MP4.
	Container transcode.Container
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

// Subtitle actions (SubtitlePlan.Action) and sidecar formats
// (SidecarPlan.Format).
const (
	SubtitleCopy = "copy" // a graft's copy plan (CopyPlan) keeps every subtitle
	SidecarASS   = "ass"  // the stream copied out to <stem>.<suffix>
	SidecarSRT   = "srt"  // SubRip copied out; WebVTT, mov_text and text converted
)

// HoldImageSubtitles is the skip reason of a file with an image subtitle
// (PGS, DVD): it waits for the OCR of the MP4 standard's phase 2, which
// raises Version, so the file is planned again then (ruling R1).
const HoldImageSubtitles = "image subtitles (PGS, DVD) wait for OCR: MP4 standard phase 2"

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
	Format      string // SidecarASS | SidecarSRT
	Codec       string // the source's codec
	// Suffix is the file's name after the video's stem: "en.ass",
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

// Plan decides the standard for info, under profile, on hw.
func Plan(info transcode.MediaInfo, profile Profile, hw Hardware) Result {
	p := Result{
		Container: transcode.ContainerMP4,
		Tags:      map[string]string{"CLUSTARR_PROFILE": profile.Name + "@" + profile.Hash},
	}
	// A transcoded file is final (spec §5), whichever profile or hash wrote
	// it: an edit or a new Version re-transcodes nothing.
	if tag := formatTag(info.Tags, "CLUSTARR_PROFILE"); tag != "" {
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
	for _, s := range info.Subtitles {
		if s.Bitmap {
			return skip(p, HoldImageSubtitles)
		}
	}
	p.Audio = planAudio(info.Audio, profile.Languages)
	p.Subtitles = []SubtitlePlan{}
	p.Sidecars, p.Dropped = planSubtitles(info.Subtitles)
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
	audioAsIs := len(p.Audio) == len(info.Audio)
	for _, a := range p.Audio {
		audioAsIs = audioAsIs && a.Action == AudioCopy
	}
	subsAsIs := len(info.Subtitles) == 0 && len(info.Attachments) == 0
	sameContainer := containerOf(info.Format.Name) == p.Container
	switch {
	case compliant && audioAsIs && subsAsIs && sameContainer:
		return skip(p, "already HEVC in the MP4 standard's layout")
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
		vp.Options = nvencOptions(quality, v.BitRateKbps)
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
func planAudio(as []transcode.AudioStream, languages []string) []AudioPlan {
	type group struct {
		main, comments []int
	}
	var order []string
	groups := map[string]*group{}
	for _, i := range keptAudio(as, languages) {
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
	return out
}

// keptAudio is the languages filter: only tracks in languages (commentary
// always), unless none matches, then every track.
func keptAudio(as []transcode.AudioStream, languages []string) []int {
	matched := false
	for _, a := range as {
		if len(languages) > 0 && !commentary(a) && slices.Contains(languages, a.Language) {
			matched = true
		}
	}
	var keep []int
	for i, a := range as {
		if !matched || commentary(a) || slices.Contains(languages, a.Language) {
			keep = append(keep, i)
		}
	}
	return keep
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

// primaryPlans is a primary track's one or two outputs. A copied track
// keeps its title; an encoded one is named for what it is (ruling R5).
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
	return []AudioPlan{surround, aacPlan(i, a.Language, 2, "Stereo")}
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

// textSubtitles become .srt sidecars and assSubtitles .ass ones.
var (
	textSubtitles = []string{"subrip", "srt", "text", "webvtt", "mov_text"}
	assSubtitles  = []string{"ass", "ssa"}
)

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

// planSubtitles is spec §4.1 with rulings R2 and R3: every text subtitle
// becomes an .srt sidecar and every ASS one an .ass sidecar, one per name;
// anything else is dropped and named. Plan holds a file with a bitmap
// stream before it gets here.
func planSubtitles(ss []transcode.SubtitleStream) (sidecars []SidecarPlan, dropped []string) {
	taken := map[string]bool{}
	for i, s := range ss {
		format := ""
		switch {
		case slices.Contains(assSubtitles, s.Codec):
			format = SidecarASS
		case slices.Contains(textSubtitles, s.Codec):
			format = SidecarSRT
		default:
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s): a codec the MP4 standard does not carry", i, s.Codec))
			continue
		}
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
