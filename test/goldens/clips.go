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

package goldens

import (
	"strings"
	"time"

	"github.com/mediactl/clustarr/internal/testmedia"
)

// ClipSpec is one golden clip: its name, the kinds of golden recorded for
// it, and the testmedia.Spec it is made from -- or, for a file committed
// elsewhere, its module-relative Path in place of a Spec.
type ClipSpec struct {
	Name  string
	Kinds []string
	Spec  testmedia.Spec
	Path  string
}

// ClipSpecs are the clips W6.23 recorded goldens for: (a) W7.6's seven
// decode fixtures, (b) W8.4's probe parity fixtures and the in-process
// probe's, (c) W8.8's extraction fixtures, each translated by Wave 6b's
// conversion table at 320x180 or smaller, ultrafast and crf 40 where the
// encoder takes them, plus the three committed test/data/mediainfo files.
// Names are the parity tasks' file names; W8.4's "late.mkv" (chapters at
// 9300 s) is chapters9300.mkv, since W7.6 has a late.mkv of its own.
// Not made: W8.4's "flac 24-bit" (FLAC opens in s16 through ffgo; the
// committed audio_flac_24bit.flac stands in) and W8.8's webvtt and
// mov_text streams (ffgo's muxer writes subrip and ass only).
func ClipSpecs() []ClipSpec {
	decodeProbe := []string{KindDecode, KindProbe}
	probe := []string{KindProbe}
	extract := []string{KindExtract, KindProbe}
	small := map[string]string{"preset": "ultrafast", "crf": "40"}
	// The 30 s decode clips take the encoder's lowest quality to stay
	// under 1 MiB each.
	lean := map[string]string{"preset": "ultrafast", "crf": "51"}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	x265 := func(base map[string]string, params string) map[string]string {
		return with(base, "x265-params", "log-level=error:"+params)
	}
	thirty := func(rate string) testmedia.Video {
		return testmedia.Video{Source: "testsrc2=d=30:s=320x180:r=" + rate, Encoder: "libx264", Options: with(lean, "g", rate)}
	}
	ac3 := func(freq string) testmedia.Audio {
		return testmedia.Audio{Source: "sine=d=30:f=" + freq, Layout: "5.1(side)", Encoder: "ac3", BitrateKbps: 96}
	}
	aac := func(src string) testmedia.Audio { return testmedia.Audio{Source: src, Encoder: "aac", BitrateKbps: 32} }
	v := testmedia.Video{Source: "testsrc2=d=2:s=320x180:r=24", Encoder: "libx264", Options: small}
	sine := func(chain string) string { return "sine=d=2:sample_rate=48000" + chain }
	srtVideo := testmedia.Video{Source: "testsrc=d=3:s=160x120:r=10", Encoder: "libx264", Options: small}
	assHeader := "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
		"Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,-1,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }

	return []ClipSpec{
		// (a) W7.6: 30 s each, the windows at 0, 12.05, 12.5, 15 and 29 s.
		// H.264 with B-frames (-bf 3) and a keyframe a second, AAC.
		{Name: "h264.mkv", Kinds: decodeProbe, Spec: testmedia.Spec{Shortest: true,
			Video: []testmedia.Video{{Source: "testsrc2=d=30:s=320x180:r=25", Encoder: "libx264", Options: with(lean, "bf", "3", "g", "25")}},
			Audio: []testmedia.Audio{aac("sine=d=30:f=440")}}},
		// HEVC 10-bit BT.709 with 5.1(side) AC-3.
		{Name: "hevc709.mkv", Kinds: decodeProbe, Spec: testmedia.Spec{Shortest: true,
			Video: []testmedia.Video{{Source: "testsrc2=d=30:s=320x180:r=24", Encoder: "libx265", PixFmt: "yuv420p10le", Options: x265(lean, "keyint=24"),
				Color: testmedia.Color{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"}}},
			Audio: []testmedia.Audio{ac3("330")}}},
		// HEVC 10-bit BT.2020/PQ, same audio.
		{Name: "hevcpq.mkv", Kinds: decodeProbe, Spec: testmedia.Spec{Shortest: true,
			Video: []testmedia.Video{{Source: "testsrc2=d=30:s=320x180:r=24", Encoder: "libx265", PixFmt: "yuv420p10le", Options: x265(lean, "keyint=24"),
				Color: testmedia.Color{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc"}}},
			Audio: []testmedia.Audio{ac3("330")}}},
		// 5.1 AC-3 with non-default mix levels: the decoder's DOWNMIX_INFO
		// then differs from swr's defaults.
		{Name: "ac3mix.mkv", Kinds: decodeProbe, Spec: testmedia.Spec{Shortest: true,
			Video: []testmedia.Video{thirty("25")},
			Audio: []testmedia.Audio{{Source: "sine=d=30:f=550", Layout: "5.1(side)", Encoder: "ac3", BitrateKbps: 96,
				Options: map[string]string{"center_mixlev": "0.5", "surround_mixlev": "0.5"}}}}},
		// Audio starting 0.7 s late (-itsoffset 0.7).
		{Name: "late.mkv", Kinds: decodeProbe, Spec: testmedia.Spec{
			Video: []testmedia.Video{thirty("25")},
			Audio: []testmedia.Audio{{Source: "sine=d=29:f=440", Encoder: "aac", BitrateKbps: 32, Offset: 700 * time.Millisecond}}}},
		{Name: "h264.mp4", Kinds: decodeProbe, Spec: testmedia.Spec{Shortest: true,
			Video: []testmedia.Video{thirty("25")}, Audio: []testmedia.Audio{aac("sine=d=30:f=440")}}},
		// MPEG-TS with a start offset and no wrap or discontinuity.
		{Name: "offset.ts", Kinds: decodeProbe, Spec: testmedia.Spec{Shortest: true, StartOffset: 1400 * time.Millisecond, Format: "mpegts",
			Video: []testmedia.Video{thirty("25")}, Audio: []testmedia.Audio{aac("sine=d=30:f=440")}}},

		// (b) the in-process probe's fixtures (app/transcode/worker/inprocess).
		{Name: "everything.mkv", Kinds: []string{KindProbe, KindExtract}, Spec: testmedia.Spec{
			Tags:  map[string]string{"title": "Clip", "CLUSTARR_PROFILE": "p@h"},
			Video: []testmedia.Video{{Source: "testsrc2=size=320x180:rate=24:duration=2", Encoder: "libx264", Options: small}},
			Audio: []testmedia.Audio{
				{Source: "sine=frequency=440:sample_rate=48000:duration=2,aformat=channel_layouts=5.1", Encoder: "ac3", Stream: testmedia.Stream{Language: "eng", Disposition: []string{"default"}}},
				{Source: "sine=frequency=880:sample_rate=48000:duration=2", Encoder: "aac", Stream: testmedia.Stream{Language: "fre", Title: "Commentary", Disposition: []string{"comment"}}},
				{Source: "sine=frequency=660:sample_rate=48000:duration=2,aformat=channel_layouts=5.1(side)", Encoder: "eac3", Stream: testmedia.Stream{Language: "ger"}},
			},
			Subtitles:   []testmedia.Subtitle{{Codec: "subrip", Events: []testmedia.Event{{Start: ms(500), End: ms(1500), Text: "Hello"}}, Stream: testmedia.Stream{Language: "eng", Disposition: []string{"forced"}}}},
			Attachments: []testmedia.Attachment{{Name: "f.ttf", MIMEType: "application/x-truetype-font", Data: []byte("not really a font")}},
			Chapters:    []testmedia.Chapter{{Start: 0, End: 1000, TimeBase: [2]int{1, 1000}, Title: "One"}, {Start: 1000, End: 2000, TimeBase: [2]int{1, 1000}, Title: "Two"}},
		}},
		{Name: "hdr10.mkv", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{{Source: "testsrc2=duration=2:size=640x480:rate=25", Encoder: "libx265", PixFmt: "yuv420p10le",
			Options: x265(small, "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400")}}}},
		{Name: "hlg.mkv", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{{Source: "testsrc2=duration=2:size=640x480:rate=25", Encoder: "libx265", PixFmt: "yuv420p10le",
			Options: x265(small, "colorprim=bt2020:transfer=arib-std-b67:colormatrix=bt2020nc")}}}},
		{Name: "hi10p.mkv", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{{Source: "testsrc2=duration=2:size=320x180:rate=24", Encoder: "libx264", PixFmt: "yuv420p10le", Options: small}}}},
		{Name: "clip.mp4", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{{Source: "testsrc2=duration=2:size=320x180:rate=24", Encoder: "libx264", Options: small}},
			Audio: []testmedia.Audio{aac("sine=duration=2")}}},

		// (b) W8.4's parity list.
		{Name: "pcm.mkv", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Video: []testmedia.Video{v}, Audio: []testmedia.Audio{{Source: sine(""), Encoder: "pcm_s16le"}}}},
		{Name: "chapters9300.mkv", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{v},
			Chapters: []testmedia.Chapter{{Start: 9300000000000, End: 9400000000000, TimeBase: [2]int{1, 1000000000}, Title: "Late"}}}},
		{Name: "interlaced.ts", Kinds: probe, Spec: testmedia.Spec{Format: "mpegts", Video: []testmedia.Video{{Source: "testsrc2=d=2:s=720x576:r=25,setfield=tff", Encoder: "libx264",
			Options: with(small, "flags", "+ildct+ilme", "x264-params", "tff=1")}}}},
		{Name: "dts.mkv", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Video: []testmedia.Video{v},
			Audio: []testmedia.Audio{{Source: sine(",aformat=channel_layouts=5.1"), Layout: "5.1(side)", Encoder: "dca", BitrateKbps: 1411, Options: map[string]string{"strict": "-2"}}}}},
		{Name: "eac3.mp4", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Video: []testmedia.Video{v},
			Audio: []testmedia.Audio{{Source: sine(",aformat=channel_layouts=5.1"), Encoder: "eac3"}}}},
		{Name: "truehd.mkv", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Video: []testmedia.Video{v},
			Audio: []testmedia.Audio{{Source: sine(",aformat=channel_layouts=5.1(side)"), Layout: "5.1(side)", Encoder: "truehd", Options: map[string]string{"strict": "-2"}}}}},
		{Name: "vfr.mkv", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{{Source: `testsrc2=d=4:s=320x180:r=30,select='not(mod(n\,3))'`, Encoder: "libx264", Options: small}}}},
		{Name: "mpeg2i.mkv", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{{Source: "testsrc2=d=2:s=720x576:r=25,setfield=tff", Encoder: "mpeg2video",
			Options: map[string]string{"flags": "+ildct+ilme"}}}}},
		{Name: "mp3.avi", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Video: []testmedia.Video{{Source: "testsrc2=d=2:s=320x180:r=24", Encoder: "mpeg4"}},
			Audio: []testmedia.Audio{{Source: sine(""), Encoder: "libmp3lame"}}}},
		{Name: "pcm24.mov", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Video: []testmedia.Video{v}, Audio: []testmedia.Audio{{Source: sine(""), Encoder: "pcm_s24le"}}}},
		{Name: "alac24.m4a", Kinds: probe, Spec: testmedia.Spec{Format: "ipod", Audio: []testmedia.Audio{{Source: "sine=d=2:sample_rate=96000", Encoder: "alac",
			Options: map[string]string{"bits_per_raw_sample": "24"}}}}},
		{Name: "tagged.mp4", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{v}, Tags: map[string]string{"CLUSTARR_PROFILE": "p@h"},
			MuxerOptions: map[string]string{"movflags": "+use_metadata_tags"}}},
		{Name: "bps.mkv", Kinds: probe, Spec: testmedia.Spec{Shortest: true,
			Video: []testmedia.Video{{Source: v.Source, Encoder: "libx264", Options: small, Stream: testmedia.Stream{Metadata: map[string]string{"BPS": "1234567", "BPS-eng": "1234567"}}}},
			Audio: []testmedia.Audio{aac(sine(""))}}},
		{Name: "cover.mp4", Kinds: probe, Spec: testmedia.Spec{Video: []testmedia.Video{v,
			{Source: "color=c=red:s=16x16:d=1", Encoder: "mjpeg", PixFmt: "yuvj420p", Frames: 1, Stream: testmedia.Stream{Disposition: []string{"attached_pic"}}}}}},
		{Name: "badutf8.mkv", Kinds: probe, Spec: testmedia.Spec{Shortest: true, Tags: map[string]string{"title": "Caf\xe9"},
			Video:    []testmedia.Video{{Source: v.Source, Encoder: "libx264", Options: small, Stream: testmedia.Stream{Title: "a\xc0\xafb"}}},
			Audio:    []testmedia.Audio{{Source: sine(""), Encoder: "aac", Stream: testmedia.Stream{Title: "a\xed\xa0\x80b", Metadata: map[string]string{"comment": "a\xef\xbf\xbeb"}}}},
			Chapters: []testmedia.Chapter{{Start: 0, End: 1000, TimeBase: [2]int{1, 1000}, Title: strings.Repeat("\xe9", 140)}}}},

		// (c) W8.8's extraction fixtures.
		{Name: "subrip.mkv", Kinds: extract, Spec: testmedia.Spec{Video: []testmedia.Video{srtVideo},
			Subtitles: []testmedia.Subtitle{{Codec: "subrip", Events: []testmedia.Event{
				{Start: ms(500), End: ms(1500), Text: "<i>Hello</i> there"}, {Start: ms(1600), End: ms(2400), Text: "Second"}}}}}},
		{Name: "ass.mkv", Kinds: extract, Spec: testmedia.Spec{Video: []testmedia.Video{srtVideo},
			Subtitles: []testmedia.Subtitle{{Codec: "ass", Header: assHeader, Events: []testmedia.Event{{Start: ms(500), End: ms(1500), Text: "Hello there"}}}}}},
		// An empty cue, a cue past a 30 s gap, and a second stream that
		// starts late.
		{Name: "edges.mkv", Kinds: extract, Spec: testmedia.Spec{Video: []testmedia.Video{srtVideo},
			Subtitles: []testmedia.Subtitle{
				{Codec: "subrip", Stream: testmedia.Stream{Language: "eng"}, Events: []testmedia.Event{
					{Start: ms(500), End: ms(1500), Text: "First"}, {Start: ms(1600), End: ms(1700), Text: " "},
					{Start: ms(40000), End: ms(41500), Text: "After a long gap"}}},
				{Codec: "subrip", Stream: testmedia.Stream{Language: "fre"}, Events: []testmedia.Event{
					{Start: ms(12000), End: ms(13000), Text: "Late start"}}},
			}}},

		// Committed in place.
		{Name: "audio_flac_24bit.flac", Kinds: probe, Path: "test/data/mediainfo/audio_flac_24bit.flac"},
		{Name: "sample_h264_8bit.mp4", Kinds: probe, Path: "test/data/mediainfo/sample_h264_8bit.mp4"},
		{Name: "sample_hevc_10bit.mkv", Kinds: probe, Path: "test/data/mediainfo/sample_hevc_10bit.mkv"},
	}
}
