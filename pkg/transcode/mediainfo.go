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
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// This file defines pkg/transcode's own encoding-input model: MediaInfo and
// its nested stream/format types. It is not a copy of pkg/mediainfo's shape
// -- it is what Plan and the argv goldens are built against, with the exact
// per-field detail an ffmpeg command line needs (color tags, dispositions,
// per-stream HDR/Dolby-Vision side data) that api/common/v1alpha1.MediaInfo
// has no room for. FromProbe, below, adapts a real pkg/mediainfo probe into
// this shape; the two are intentionally decoupled so this package's tests
// never need a real ffprobe binary to build a MediaInfo fixture.

// Rational is a plain numerator/denominator pair, used for frame rates
// ("24000/1001") without ever introducing a float.
type Rational struct{ Num, Den int64 }

// Disposition mirrors ffprobe's per-stream disposition flags this package
// cares about.
type Disposition struct {
	Default, Forced, Comment, HearingImpaired, VisualImpaired, Dub, Original bool
}

// hdrBucket is the five-way classification Plan switches on: none, HDR10
// (including PQ10, which carries no static metadata but is graded the same
// way), HDR10+, HLG, and Dolby Vision (every dolbyVision* CRD value; Plan
// distinguishes the Dolby Vision sub-cases by DoviRecord.Profile and
// BLSignalCompatibilityID, not by which of the five dolbyVision* enum values
// was reported).
type hdrBucket int

const (
	hdrNone hdrBucket = iota
	hdrHDR10
	hdrHDR10Plus
	hdrHLG
	hdrDolbyVision
)

// hdrClass collapses the ten commonv1.HdrFormat values the CRD accepts into
// the five cases Plan's renderer switches on.
func hdrClass(f commonv1.HdrFormat) hdrBucket {
	switch f {
	case commonv1.HdrFormatPQ10, commonv1.HdrFormatHDR10:
		return hdrHDR10
	case commonv1.HdrFormatHDR10Plus:
		return hdrHDR10Plus
	case commonv1.HdrFormatHLG10:
		return hdrHLG
	case commonv1.HdrFormatDolbyVision, commonv1.HdrFormatDolbyVisionHDR10,
		commonv1.HdrFormatDolbyVisionSDR, commonv1.HdrFormatDolbyVisionHLG,
		commonv1.HdrFormatDolbyVisionHDR10Plus:
		return hdrDolbyVision
	default: // HdrFormatNone, "", and anything unrecognised
		return hdrNone
	}
}

// HDRInfo carries a video stream's HDR classification and metadata, using
// pkg/mediainfo's real side-data types and the shared commonv1.HdrFormat
// vocabulary rather than a local placeholder copy of either.
type HDRInfo struct {
	Format           commonv1.HdrFormat
	MasteringDisplay *mediainfo.MasteringDisplay
	ContentLight     *mediainfo.ContentLight
	DolbyVision      *mediainfo.DoviRecord
	HasHDR10Plus     bool
}

// VideoStream is one video stream of a MediaInfo.
type VideoStream struct {
	Index                                                 int32
	Codec, Profile                                        string
	Level                                                  int32
	PixFmt                                                 string
	BitDepth                                               int32
	Width, Height                                          int32
	FrameRate                                              Rational
	FieldOrder                                             string // "progressive"|"tt"|"bb"|"tb"|"bt"
	ColorRange, ColorPrimaries, ColorTransfer, ColorSpace  string
	HDR                                                    HDRInfo
	Packets                                                int64
	Duration                                               time.Duration
	Disposition                                            Disposition
	Language, Title                                        string
}

// AudioStream is one audio stream of a MediaInfo.
type AudioStream struct {
	Index           int32
	Codec, Profile  string
	Channels        int32
	ChannelLayout   string
	SampleRate      int32
	BitRateKbps     int32
	Lossless, Atmos bool
	Language, Title string
	Disposition     Disposition
}

// SubtitleStream is one subtitle stream of a MediaInfo.
type SubtitleStream struct {
	Index           int32
	Codec           string
	Bitmap          bool
	Language, Title string
	Disposition     Disposition
}

// AttachmentStream is one attachment (font, cover art, ...) of a MediaInfo.
type AttachmentStream struct {
	Index              int32
	Filename, MimeType string
}

// Chapter is one chapter mark of a MediaInfo.
type Chapter struct {
	Start, End time.Duration
	Title      string
}

// FormatInfo is the container-level detail of a MediaInfo.
type FormatInfo struct {
	Name        string
	Duration    time.Duration
	SizeBytes   int64
	BitRateKbps int64
}

// MediaInfo is pkg/transcode's own technical description of one media file:
// everything Plan needs to decide skip/remuxOnly/encode/reject and render an
// argv, plus Modifier, which ffprobe cannot derive at all.
type MediaInfo struct {
	Path        string
	Format      FormatInfo
	Video       []VideoStream
	Audio       []AudioStream
	Subtitles   []SubtitleStream
	Attachments []AttachmentStream
	Chapters    []Chapter
	Tags        map[string]string

	// Modifier is the source's quality modifier ("remux", "brdisk", ...) as
	// already classified by pkg/quality/pkg/release and stored on the
	// MediaFile before squasharr ever sees it -- NOT something ffprobe can
	// derive. The controller attaches it when it builds the MediaInfo it
	// passes to Plan. Needed for PolicySpec.NeverTranscodeModifiers.
	Modifier string
}

// FromProbe builds Plan's input model from a pkg/mediainfo probe. mi
// supplies the CRD-level summary (bit depth, HDR classification, DV
// profile); raw supplies per-stream detail. Fields the probe cannot supply
// (Packets, Modifier) stay zero -- the controller fills Modifier separately
// from the quality classifier before calling Plan.
func FromProbe(mi *commonv1.MediaInfo, raw *mediainfo.Raw) (MediaInfo, error) {
	if mi == nil {
		return MediaInfo{}, fmt.Errorf("transcode: FromProbe: mi is nil")
	}
	if raw == nil {
		return MediaInfo{}, fmt.Errorf("transcode: FromProbe: raw is nil")
	}

	out := MediaInfo{}
	if raw.Format != nil {
		out.Format = FormatInfo{
			Name:        raw.Format.FormatName,
			Duration:    durationFromSeconds(raw.Format.DurationSeconds),
			SizeBytes:   parseInt64(raw.Format.Size),
			BitRateKbps: parseInt64(raw.Format.BitRate) / 1000,
		}
		out.Tags = tagsToMap(raw.Format.TagList)
	}

	haveFirstVideo := false
	for _, s := range raw.Streams {
		if s == nil {
			continue
		}
		switch ffprobe.StreamType(s.CodecType) {
		case ffprobe.StreamVideo:
			vs := VideoStream{
				Index:       int32(s.Index),
				Codec:       s.CodecName,
				Profile:     s.Profile,
				Level:       int32(s.Level),
				PixFmt:      s.PixFmt,
				Width:       int32(s.Width),
				Height:      int32(s.Height),
				FrameRate:   parseRational(s.RFrameRate),
				FieldOrder:  s.FieldOrder,
				Duration:    durationFromSecondsString(s.Duration),
				Disposition: dispositionFrom(s.Disposition),
				Language:    tagString(s.TagList, "language"),
				Title:       tagString(s.TagList, "title"),
			}
			if !haveFirstVideo {
				haveFirstVideo = true
				vs.ColorRange = firstNonEmpty(raw.ColorRange, s.ColorRange)
				vs.ColorPrimaries = firstNonEmpty(raw.ColorPrimaries, s.ColorPrimaries)
				vs.ColorTransfer = firstNonEmpty(raw.ColorTransfer, s.ColorTransfer)
				vs.ColorSpace = firstNonEmpty(raw.ColorSpace, s.ColorSpace)
				vs.BitDepth = mi.VideoBitDepth
				vs.HDR = HDRInfo{
					Format:           mi.Hdr,
					MasteringDisplay: raw.MasteringDisplay,
					ContentLight:     raw.ContentLight,
					DolbyVision:      raw.Dovi,
					HasHDR10Plus:     raw.HasHDR10Plus,
				}
			} else {
				vs.ColorRange = s.ColorRange
				vs.ColorPrimaries = s.ColorPrimaries
				vs.ColorTransfer = s.ColorTransfer
				vs.ColorSpace = s.ColorSpace
			}
			out.Video = append(out.Video, vs)

		case ffprobe.StreamAudio:
			codec := s.CodecName
			profile := s.Profile
			title := tagString(s.TagList, "title")
			out.Audio = append(out.Audio, AudioStream{
				Index:         int32(s.Index),
				Codec:         codec,
				Profile:       profile,
				Channels:      int32(s.Channels),
				ChannelLayout: s.ChannelLayout,
				SampleRate:    int32(parseInt64(s.SampleRate)),
				BitRateKbps:   int32(parseInt64(s.BitRate) / 1000),
				Lossless:      isLosslessAudio(codec, profile),
				Atmos:         isAtmos(profile, title),
				Language:      tagString(s.TagList, "language"),
				Title:         title,
				Disposition:   dispositionFrom(s.Disposition),
			})

		case ffprobe.StreamSubtitle:
			out.Subtitles = append(out.Subtitles, SubtitleStream{
				Index:       int32(s.Index),
				Codec:       s.CodecName,
				Bitmap:      isBitmapSubtitle(s.CodecName),
				Language:    tagString(s.TagList, "language"),
				Title:       tagString(s.TagList, "title"),
				Disposition: dispositionFrom(s.Disposition),
			})

		case ffprobe.StreamAttachment:
			out.Attachments = append(out.Attachments, AttachmentStream{
				Index:    int32(s.Index),
				Filename: tagString(s.TagList, "filename"),
				MimeType: tagString(s.TagList, "mimetype"),
			})
		}
	}

	if !haveFirstVideo {
		return MediaInfo{}, fmt.Errorf("transcode: FromProbe: raw has no video stream")
	}

	for _, c := range raw.Chapters {
		if c == nil {
			continue
		}
		out.Chapters = append(out.Chapters, Chapter{
			Start: durationFromSeconds(c.StartTimeSeconds),
			End:   durationFromSeconds(c.EndTimeSeconds),
			Title: tagString(c.TagList, "title"),
		})
	}

	return out, nil
}

// -- small, panic-free parsing helpers -------------------------------------

func durationFromSeconds(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second))
}

func durationFromSecondsString(s string) time.Duration {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return durationFromSeconds(f)
}

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseRational parses ffprobe's "num/den" rate strings (r_frame_rate,
// avg_frame_rate). Malformed input yields the zero Rational rather than
// panicking.
func parseRational(s string) Rational {
	num, den, ok := strings.Cut(s, "/")
	n := parseInt64(strings.TrimSpace(num))
	if !ok {
		return Rational{Num: n, Den: 1}
	}
	d := parseInt64(strings.TrimSpace(den))
	if d == 0 {
		d = 1
	}
	return Rational{Num: n, Den: d}
}

func dispositionFrom(d ffprobe.StreamDisposition) Disposition {
	return Disposition{
		Default:         d.Default != 0,
		Forced:          d.Forced != 0,
		Comment:         d.Comment != 0,
		HearingImpaired: d.HearingImpaired != 0,
		VisualImpaired:  d.VisualImpaired != 0,
		Dub:             d.Dub != 0,
		Original:        d.Original != 0,
	}
}

func tagString(t ffprobe.Tags, key string) string {
	if t == nil {
		return ""
	}
	if v, ok := t[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func tagsToMap(t ffprobe.Tags) map[string]string {
	if len(t) == 0 {
		return nil
	}
	out := make(map[string]string, len(t))
	for k, v := range t {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isLosslessAudio(codec, profile string) bool {
	switch codec {
	case "flac", "alac", "truehd", "mlp":
		return true
	}
	if strings.HasPrefix(codec, "pcm_") {
		return true
	}
	return codec == "dts" && profile == "DTS-HD MA"
}

func isAtmos(profile, title string) bool {
	return strings.Contains(strings.ToLower(profile), "atmos") ||
		strings.Contains(strings.ToLower(title), "atmos")
}

func isBitmapSubtitle(codec string) bool {
	switch codec {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub":
		return true
	default:
		return false
	}
}
