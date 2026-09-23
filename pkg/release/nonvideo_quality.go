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

package release

import (
	"strings"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Non-video quality. Video titles get their quality from the Radarr/Sonarr
// source+resolution+modifier table (quality.go). Music, books, audiobooks and
// comics have no such tuple: their quality is a name, and each name is one of
// the upstream app's own qualities, so pkg/quality can place it on that
// kind's ladder.
//
//   - Music: Lidarr's QualityParser (src/NzbDrone.Core/Parser/QualityParser.cs,
//     develop, fetched 2026-09-23) -- CodecRegex, BitRateRegex,
//     SampleSizeRegex and WebRegex, and the codec x bitrate switch, ported
//     verbatim. Quality.Name is a Lidarr quality name ("MP3-320", "FLAC 24bit",
//     "OGG Vorbis Q8", ...), or "Unknown" as Lidarr returns it.
//   - Books and audiobooks: Readarr's QualityParser (same path, Readarr
//     develop): one CodecRegex over ebook and audio formats, so an audio
//     release for an ebook item is an audio quality its profile rejects,
//     not a guess.
//   - Comics: the file format, from the extension or a format token (CBZ,
//     CBR, PDF); Mylar and Kapowarr have no quality enum to port.
//
// The extension fallback both apps apply to a FILE name
// (MediaFileExtensions.GetQualityForExtension) is not ported: release titles
// rarely carry one, and importarr freezes a file's quality from the file.

// Lidarr's quality names (Qualities/Quality.cs).
const (
	qMusicUnknown = "Unknown"
	qMP3VBRV0     = "MP3-VBR-V0"
	qMP3VBRV2     = "MP3-VBR-V2"
	qFLAC         = "FLAC"
	qFLAC24       = "FLAC 24bit"
	qALAC         = "ALAC"
	qALAC24       = "ALAC 24bit"
	qWavPack      = "WavPack"
	qAPE          = "APE"
	qWMA          = "WMA"
	qWAV          = "WAV"
	qAACVBR       = "AAC-VBR"
)

// lidarrCodecRegex ports Lidarr's CodecRegex.
var lidarrCodecRegex = mustCompile(
	`\b(?:(?<MP1>MPEG Version \d(?:.5)? Audio, Layer 1|MP1)|(?<MP2>MPEG Version \d(?:.5)? Audio, Layer 2|MP2)|`+
		`(?<MP3VBR>MP3.*VBR|MPEG Version \d(?:.5)? Audio, Layer 3 vbr)|(?<MP3CBR>MP3|MPEG Version \d(?:.5)? Audio, Layer 3)|`+
		`(?<FLAC>(?:web)?flac(?:24(?:[-._ ]?bit)?)?|TR24)|(?<WAVPACK>wavpack|wv)|(?<ALAC>alac)|(?<WMA>WMA\d?)|(?<WAV>WAV|PCM)|`+
		`(?<AAC>M4A|M4P|M4B|AAC|mp4a|MPEG-4 Audio(?!.*alac))|(?<OGG>OGG|OGA|Vorbis))\b|`+
		`(?<APE>monkey's audio|[\[|\(].*\bape\b.*[\]|\)])|(?<OPUS>Opus Version \d(?:.5)? Audio|[\[|\(].*\bopus\b.*[\]|\)])`,
	regexp2.IgnoreCase,
)

// lidarrCodecOrder is ParseCodec's group-check order.
var lidarrCodecOrder = []string{"FLAC", "ALAC", "WMA", "WAV", "AAC", "OGG", "OPUS", "MP1", "MP2", "MP3VBR", "MP3CBR", "WAVPACK", "APE"}

// lidarrBitRateRegex ports Lidarr's BitRateRegex (written there with
// IgnorePatternWhitespace; the whitespace is dropped here, the literal "[ ]"
// spaces kept). Its BitRate.VBR member has no group in the pattern, so the
// branches Lidarr keys on it can never fire and are not ported.
var lidarrBitRateRegex = mustCompile(
	`\b(?:(?<B096>96[ ]?kbps|96|[\[\(].*96.*[\]\)])|(?<B128>128[ ]?kbps|128|[\[\(].*128.*[\]\)])|`+
		`(?<B160>160[ ]?kbps|160|[\[\(].*160.*[\]\)]|q5)|(?<B192>192[ ]?kbps|192|[\[\(].*192.*[\]\)]|q6)|`+
		`(?<B224>224[ ]?kbps|224|[\[\(].*224.*[\]\)]|q7)|(?<B256>256[ ]?kbps|256|itunes\splus|[\[\(].*256.*[\]\)]|q8)|`+
		`(?<B320>320[ ]?kbps|320|[\[\(].*320.*[\]\)]|q9)|(?<B500>500[ ]?kbps|500|[\[\(].*500.*[\]\)]|q10)|`+
		`(?<VBRV0>V0[ ]?kbps|V0|[\[\(].*V0.*[\]\)])|(?<VBRV2>V2[ ]?kbps|V2|[\[\(].*V2.*[\]\)]))\b`,
	regexp2.IgnoreCase,
)

var lidarrBitRateOrder = []string{"B096", "B128", "B160", "B192", "B224", "B256", "B320", "B500", "VBRV0", "VBRV2"}

// lidarrSampleSizeRegex ports Lidarr's SampleSizeRegex. Lidarr compiles it
// case-sensitive and runs it on the lower-cased name; so does this.
var lidarrSampleSizeRegex = mustCompile(
	`\b(?:(?<S24>24[-._ ]?bit|flac24(?:[-._ ]?bit)?|tr24|24-(?:44|48|96|192)|[\[\(].*24bit.*[\]\)]))\b`,
	regexp2.None,
)

// lidarrWebRegex ports Lidarr's WebRegex.
var lidarrWebRegex = mustCompile(`\b(?<web>WEB)(?:\b|$|[ .])`, regexp2.IgnoreCase)

// firstGroup returns which of names is the populated group of re's first
// match in s, in names' order, or "".
func firstGroup(re *regexp2.Regexp, s string, names []string) string {
	m, err := re.FindStringMatch(s)
	if err != nil || m == nil {
		return ""
	}
	for _, n := range names {
		if g := m.GroupByName(n); g != nil && len(g.Captures) > 0 {
			return n
		}
	}
	return ""
}

// musicQuality ports Lidarr's QualityParser.ParseQuality for a release name
// (no TagLib description, no file bitrate).
func musicQuality(title string) commonv1.Quality {
	name := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(title, "_", " ")))
	codec := firstGroup(lidarrCodecRegex, name, lidarrCodecOrder)
	bitrate := firstGroup(lidarrBitRateRegex, name, lidarrBitRateOrder)
	sample24 := matchesAny(name, lidarrSampleSizeRegex)

	q := qMusicUnknown
	switch codec {
	case "MP3VBR":
		switch bitrate {
		case "VBRV0":
			q = qMP3VBRV0
		case "VBRV2":
			q = qMP3VBRV2
		}
	case "MP3CBR":
		q = map[string]string{
			"B096": "MP3-96", "B128": "MP3-128", "B160": "MP3-160",
			"B192": "MP3-192", "B256": "MP3-256", "B320": "MP3-320",
		}[bitrate]
	case "FLAC":
		q = qFLAC
		if sample24 {
			q = qFLAC24
		}
	case "ALAC":
		q = qALAC
		if sample24 {
			q = qALAC24
		}
	case "WAVPACK":
		q = qWavPack
	case "APE":
		q = qAPE
	case "WMA":
		q = qWMA
	case "WAV":
		q = qWAV
	case "AAC":
		q = map[string]string{"B192": "AAC-192", "B256": "AAC-256", "B320": "AAC-320"}[bitrate]
		if q == "" {
			q = qAACVBR
		}
	case "OGG", "OPUS":
		q = map[string]string{
			"B160": "OGG Vorbis Q5", "B192": "OGG Vorbis Q6", "B224": "OGG Vorbis Q7",
			"B256": "OGG Vorbis Q8", "B320": "OGG Vorbis Q9", "B500": "OGG Vorbis Q10",
		}[bitrate]
	case "":
		q = map[string]string{"B192": "MP3-192", "B256": "MP3-256", "B320": "MP3-320"}[bitrate]
		if q == "" && matchesAny(name, lidarrWebRegex) {
			q = "MP3-320"
		}
	}
	if q == "" {
		q = qMusicUnknown
	}
	return commonv1.Quality{Name: q}
}

// AudioFileQuality ports Lidarr's QualityParser.FindQuality: the quality of
// an audio FILE from what a probe reports -- ffprobe's codec_name, the
// stream's bitrate in kbps and its bits per sample (0 when unknown). Like
// Lidarr it matches the bitrate EXACTLY for MP3, AAC and Vorbis (a nominal
// CBR rate such as 320), and bands it only for Opus; a VBR MP3's average
// rate is not a Lidarr value, and ffprobe cannot say an MP3 is VBR, so such
// a file is "Unknown" rather than a guessed tier. importarr freezes a music
// MediaFile's quality through this so the file and the releases it is
// compared with speak one vocabulary.
func AudioFileQuality(codec string, bitrateKbps, sampleBits int) commonv1.Quality {
	c := strings.ToLower(codec)
	q := qMusicUnknown
	switch {
	case c == "mp3":
		q = map[int]string{
			8: "MP3-8", 16: "MP3-16", 24: "MP3-24", 32: "MP3-32", 40: "MP3-40", 48: "MP3-48", 56: "MP3-56",
			64: "MP3-64", 80: "MP3-80", 96: "MP3-96", 112: "MP3-112", 128: "MP3-128", 160: "MP3-160",
			192: "MP3-192", 224: "MP3-224", 256: "MP3-256", 320: "MP3-320",
		}[bitrateKbps]
	case c == "flac":
		q = qFLAC
		if sampleBits == 24 {
			q = qFLAC24
		}
	case c == "alac":
		q = qALAC
		if sampleBits == 24 {
			q = qALAC24
		}
	case c == "wavpack":
		q = qWavPack
	case c == "ape":
		q = qAPE
	case strings.HasPrefix(c, "wma"):
		q = qWMA
	case strings.HasPrefix(c, "pcm_"):
		q = qWAV
	case c == "aac":
		q = map[int]string{192: "AAC-192", 256: "AAC-256", 320: "AAC-320"}[bitrateKbps]
		if q == "" {
			q = qAACVBR
		}
	case c == "vorbis":
		q = map[int]string{
			160: "OGG Vorbis Q5", 192: "OGG Vorbis Q6", 224: "OGG Vorbis Q7",
			256: "OGG Vorbis Q8", 320: "OGG Vorbis Q9", 500: "OGG Vorbis Q10",
		}[bitrateKbps]
	case c == "opus":
		switch {
		case bitrateKbps < 130:
		case bitrateKbps < 180:
			q = "OGG Vorbis Q5"
		case bitrateKbps < 205:
			q = "OGG Vorbis Q6"
		case bitrateKbps < 240:
			q = "OGG Vorbis Q7"
		case bitrateKbps < 290:
			q = "OGG Vorbis Q8"
		case bitrateKbps < 410:
			q = "OGG Vorbis Q9"
		default:
			q = "OGG Vorbis Q10"
		}
	}
	if q == "" {
		q = qMusicUnknown
	}
	return commonv1.Quality{Name: q}
}

// Readarr's quality names (Qualities/Quality.cs). Books and audiobooks share
// one enum there; Clustarr splits the ladder by profile kind but keeps the
// names.
const (
	qUnknownText  = "Unknown Text"
	qUnknownAudio = "Unknown Audio"
)

// readarrCodecRegex ports Readarr's CodecRegex.
var readarrCodecRegex = mustCompile(
	`\b(?:(?<PDF>PDF)|(?<MOBI>MOBI)|(?<EPUB>EPUB)|(?<AZW3>AZW3?)|(?<MP1>MPEG Version \d(?:.5)? Audio, Layer 1|MP1)|`+
		`(?<MP2>MPEG Version \d(?:.5)? Audio, Layer 2|MP2)|(?<MP3VBR>MP3.*VBR|MPEG Version \d(?:.5)? Audio, Layer 3 vbr)|`+
		`(?<MP3CBR>MP3|MPEG Version \d(?:.5)? Audio, Layer 3)|(?<FLAC>flac)|(?<WAVPACK>wavpack|wv)|(?<ALAC>alac)|(?<WMA>WMA\d?)|`+
		`(?<WAV>WAV|PCM)|(?<AAC>M4A|M4P|M4B|AAC|mp4a|MPEG-4 Audio(?!.*alac))|(?<OGG>OGG|OGA|Vorbis))\b|`+
		`(?<APE>monkey's audio|[\[|\(].*\bape\b.*[\]|\)])|(?<OPUS>Opus Version \d(?:.5)? Audio|[\[|\(].*\bopus\b.*[\]|\)])`,
	regexp2.IgnoreCase,
)

var readarrCodecOrder = []string{
	"PDF", "MOBI", "EPUB", "AZW3", "FLAC", "ALAC", "WMA", "WAV", "AAC", "OGG", "OPUS", "MP1", "MP2", "MP3VBR", "MP3CBR", "WAVPACK", "APE",
}

// readarrCodecQuality is Readarr's ParseQuality switch: every lossless codec
// is FLAC, every AAC container M4B, every other audio codec MP3.
var readarrCodecQuality = map[string]string{
	"PDF": "PDF", "EPUB": "EPUB", "MOBI": "MOBI", "AZW3": "AZW3",
	"FLAC": "FLAC", "ALAC": "FLAC", "WAVPACK": "FLAC",
	"AAC": "M4B",
	"MP1": "MP3", "MP2": "MP3", "MP3VBR": "MP3", "MP3CBR": "MP3", "APE": "MP3", "WMA": "MP3", "WAV": "MP3", "OGG": "MP3", "OPUS": "MP3",
}

// bookQuality ports Readarr's QualityParser.ParseQuality for a release name.
// Readarr's last fallback -- an audio newznab category makes an unknown
// codec "Unknown Audio" -- is the caller's, which knows the kind instead.
func bookQuality(title string) (commonv1.Quality, bool) {
	name := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(title, "_", " ")))
	q, ok := readarrCodecQuality[firstGroup(readarrCodecRegex, name, readarrCodecOrder)]
	return commonv1.Quality{Name: q}, ok
}

// unknownBookQuality is Readarr's unknown quality for kind: "Unknown Audio"
// for an audiobook (Readarr reaches it from an audio category), "Unknown
// Text" otherwise.
func unknownBookQuality(kind commonv1.MediaKind) commonv1.Quality {
	if kind == commonv1.MediaKindAudiobook {
		return commonv1.Quality{Name: qUnknownAudio}
	}
	return commonv1.Quality{Name: qUnknownText}
}

// comicFormatTokenRegex finds a comic format named in a release title that
// carries no file extension ("... (Digital) [CBZ]").
var comicFormatTokenRegex = mustCompile(`\b(?<fmt>CBZ|CBR|CB7|CBT|PDF)\b`, regexp2.IgnoreCase)

// comicQuality is a comic release's quality: its format, or "Unknown".
// Only CBZ, CBR and PDF are on the comic ladder; CB7 and CBT are real
// formats with no tier, so they are named rather than guessed into one.
func comicQuality(format, title string) commonv1.Quality {
	if format == "" {
		if m, err := comicFormatTokenRegex.FindStringMatch(title); err == nil && m != nil {
			format = strings.ToUpper(m.GroupByName("fmt").String())
		}
	}
	if format == "" {
		format = "Unknown"
	}
	return commonv1.Quality{Name: format}
}
