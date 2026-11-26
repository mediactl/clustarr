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

package engine

import (
	"context"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/test/nativetest"
)

// ffmpeg9OrSkip skips unless FFmpeg 9's libraries are here, which the engine
// runs on and internal/testmedia makes and inspects the clips with -- and
// fails where they are required (test/nativetest: CLUSTARR_REQUIRE_TOOLS,
// which make test sets, and CLUSTARR_FFGO_REQUIRE), so a missing library is
// never a silent pass.
func ffmpeg9OrSkip(t *testing.T) {
	t.Helper()
	nativetest.FFmpeg(t, ffruntime.Needs{})
	testmedia.Require(t)
}

// everythingClip: H.264 video; English AC-3 5.1 (default); French AAC
// stereo "Commentary" (comment); English SRT (forced); a font; two
// chapters; container title "Clip". seconds long.
func everythingClip(t *testing.T, seconds int) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	d := strconv.Itoa(seconds)
	half := int64(seconds) * 500
	return testmedia.Make(t, "everything.mkv", testmedia.Spec{
		Tags: map[string]string{"title": "Clip"},
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24:duration=" + d, Options: map[string]string{"preset": "veryfast", "bf": "2"},
		}},
		Audio: []testmedia.Audio{
			{
				Source: "sine=frequency=440:sample_rate=48000:duration=" + d, Encoder: "ac3", Layout: "5.1",
				Stream: testmedia.Stream{Language: "eng", Disposition: []string{"default"}},
			},
			{
				Source: "sine=frequency=880:sample_rate=48000:duration=" + d,
				Stream: testmedia.Stream{Language: "fre", Title: "Commentary", Disposition: []string{"comment"}},
			},
		},
		Subtitles: []testmedia.Subtitle{{
			Codec:  "subrip",
			Events: []testmedia.Event{{Start: 500 * time.Millisecond, End: 1500 * time.Millisecond, Text: "Hello"}},
			Stream: testmedia.Stream{Language: "eng", Disposition: []string{"forced"}},
		}},
		Attachments: []testmedia.Attachment{{Name: "font.ttf", MIMEType: "application/x-truetype-font", Data: []byte("not really a font")}},
		Chapters: []testmedia.Chapter{
			{Start: 0, End: half, TimeBase: [2]int{1, 1000}, Title: "One"},
			{Start: half, End: 2 * half, TimeBase: [2]int{1, 1000}, Title: "Two"},
		},
	})
}

type probedStream struct {
	CodecName   string
	CodecType   string
	Profile     string
	PixFmt      string
	Channels    int
	Layout      string
	StartTime   string
	ColorTrc    string
	Tags        map[string]string
	Disposition map[string]int
}

type probedChapter struct {
	Tags map[string]string
}

type probed struct {
	Streams  []probedStream
	Chapters []probedChapter
	Format   struct {
		Duration string
		Tags     map[string]string
	}
}

// ffprobeJSON is path as ffprobe -show_streams -show_chapters -show_format
// printed it, read through internal/testmedia.Inspect: the tests were
// written against ffprobe's JSON and keep its field names.
func ffprobeJSON(t *testing.T, path string) probed {
	t.Helper()
	info := testmedia.Inspect(t, path)
	var p probed
	p.Format.Duration = strconv.FormatFloat(info.Duration.Seconds(), 'f', 6, 64)
	p.Format.Tags = info.Tags
	for _, s := range info.Streams {
		tags := maps.Clone(s.Tags)
		if tags == nil {
			tags = map[string]string{}
		}
		if s.Language != "" {
			tags["language"] = s.Language
		}
		if s.Title != "" {
			tags["title"] = s.Title
		}
		disposition := map[string]int{}
		for _, d := range s.Disposition {
			disposition[d] = 1
		}
		p.Streams = append(p.Streams, probedStream{
			CodecName: s.Codec, CodecType: s.Type, Profile: s.Profile, PixFmt: s.PixFmt, Channels: s.Channels, Layout: s.Layout,
			StartTime: strconv.FormatFloat(s.StartTime.Seconds(), 'f', 6, 64), ColorTrc: s.Color.Transfer,
			Tags: tags, Disposition: disposition,
		})
	}
	for _, c := range info.Chapters {
		p.Chapters = append(p.Chapters, probedChapter{Tags: map[string]string{"title": c.Title}})
	}
	return p
}

func (p probed) seconds(t *testing.T) float64 {
	t.Helper()
	d, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		t.Fatalf("duration %q", p.Format.Duration)
	}
	return d
}

// realProber is the in-process probe the worker plans from, skipping without
// FFmpeg 9 and the shim; T2a's nativetest.FFmpeg(t, native.Needs) replaces
// the skip (W8.7).
func realProber(t *testing.T) *native.Prober {
	t.Helper()
	p, err := native.New()
	if err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
	return p
}

// probeInfo is the plan input the worker builds for path: a live probe
// through pkg/mediainfo/native, mapped by transcode.FromProbe.
func probeInfo(t *testing.T, path string) transcode.MediaInfo {
	t.Helper()
	mi, raw, err := realProber(t).Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	info, err := transcode.FromProbe(mi, raw)
	if err != nil {
		t.Fatalf("plan input for %s: %v", path, err)
	}
	return info
}
