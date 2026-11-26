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
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
)

// bursts is seconds of random decaying tone bursts (a soundtrack with
// onsets, unlike a sine) as 48 kHz mono samples, from seed, after lead
// seconds of silence (`adelay`) and cut to seconds in all.
func bursts(seed uint64, seconds, lead float64) *testmedia.Samples {
	const rate = 48000
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	x := make([]float64, int(seconds*rate))
	for at := 0.0; at < seconds; at += 0.1 + r.Float64()*0.3 {
		f := 200 + r.Float64()*2800
		amp := 0.2 + r.Float64()*0.5
		start := int(at * rate)
		for i := 0; i < rate/4 && start+i < len(x); i++ {
			x[start+i] += amp * math.Exp(-float64(i)/(rate/20)) * math.Sin(2*math.Pi*f*float64(i)/rate)
		}
	}
	for i, v := range x {
		x[i] = max(-1, min(1, v)) * 32000 / 32768 // the 16-bit WAV the tests wrote
	}
	delayed := append(make([]float64, int(lead*rate)), x...)
	return &testmedia.Samples{Rate: rate, Mono: delayed[:len(x)]}
}

// burstsRef is bursts as FLAC with a mono layout, for decoding as a
// reference.
func burstsRef(t *testing.T, seed uint64, seconds float64) string {
	t.Helper()
	return testmedia.Make(t, "ref.flac", testmedia.Spec{
		Format: "flac",
		Audio:  []testmedia.Audio{{Samples: bursts(seed, seconds, 0), Encoder: "flac", Layout: "mono"}},
	})
}

// graftTarget is a seconds-long Matroska file: H.264 video and one AAC
// Japanese track of bursts seed 1 (default), an English subtitle, a
// container title.
func graftTarget(t *testing.T, seconds int) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	return testmedia.Make(t, "target.mkv", testmedia.Spec{
		Duration: time.Duration(seconds) * time.Second,
		Tags:     map[string]string{"title": "Target", "CLUSTARR_PROFILE": "p@h"},
		Video:    []testmedia.Video{{Source: "testsrc2=size=160x90:rate=24", Options: map[string]string{"preset": "ultrafast"}}},
		Audio: []testmedia.Audio{{
			Samples: bursts(1, float64(seconds), 0),
			Stream:  testmedia.Stream{Language: "jpn", Disposition: []string{"default"}},
		}},
		Subtitles: []testmedia.Subtitle{{
			Codec:  "subrip",
			Events: []testmedia.Event{{Start: time.Second, End: 2 * time.Second, Text: "Hello"}},
			Stream: testmedia.Stream{Language: "eng"},
		}},
	})
}

// graftDonor is an audio-only donor: Japanese (bursts 1) and English
// (bursts 2), both delayed by lead seconds, seconds long in all.
func graftDonor(t *testing.T, seconds, lead float64) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	return testmedia.Make(t, "donor.mkv", testmedia.Spec{Audio: donorTracks(seconds, lead, "stereo")})
}

// donorTracks are a donor's two AC-3 tracks: Japanese (bursts 1) in stereo
// and English (bursts 2) in layout, both delayed by lead seconds.
func donorTracks(seconds, lead float64, layout string) []testmedia.Audio {
	return []testmedia.Audio{
		{Samples: bursts(1, seconds, lead), Encoder: "ac3", Layout: "stereo", Stream: testmedia.Stream{Language: "jpn"}},
		{Samples: bursts(2, seconds, lead), Encoder: "ac3", Layout: layout, Stream: testmedia.Stream{Language: "eng"}},
	}
}

// lagOf is the lag, in samples at 8 kHz within ±max, at which got best
// matches want (positive: got is late), by normalised cross-correlation
// over the first seconds of both.
func lagOf(want, got []float32, maxLag, seconds int) (int, float64) {
	n := min(len(want), len(got), seconds*8000)
	best, bestC := 0, -2.0
	for lag := -maxLag; lag <= maxLag; lag++ {
		var xy, xx, yy float64
		for i := maxLag; i < n-maxLag; i++ {
			a, b := float64(want[i]), float64(got[i+lag])
			xy += a * b
			xx += a * a
			yy += b * b
		}
		if c := xy / math.Sqrt(xx*yy+1e-12); c > bestC {
			best, bestC = lag, c
		}
	}
	return best, bestC
}

func rms(x []float32) float64 {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(max(len(x), 1)))
}

func TestDecodePCMStartsAtTheContainersStart(t *testing.T) {
	ffmpeg9OrSkip(t)
	src := testmedia.Make(t, "late.mkv", testmedia.Spec{
		Video: []testmedia.Video{{Source: "testsrc2=size=160x90:rate=24:duration=6", Options: map[string]string{"preset": "ultrafast"}}},
		Audio: []testmedia.Audio{{Samples: bursts(1, 5, 0), Offset: 500 * time.Millisecond}},
	})
	pcm, err := DecodePCM(context.Background(), src, 0, 8000)
	require.NoError(t, err)
	assert.InDelta(t, 5.5*8000, float64(len(pcm)), 0.1*8000, "the late start is silence, then the audio")
	assert.Less(t, rms(pcm[:int(0.45*8000)]), 1e-3, "the first 0.45 s is silent")

	ref, err := DecodePCM(context.Background(), burstsRef(t, 1, 5), 0, 8000)
	require.NoError(t, err)
	lag, c := lagOf(ref, pcm, 8000, 4)
	assert.InDelta(t, 4000, lag, 16, "the audio lands 0.5 s in (corr %.2f)", c)
}

func TestExtractAudioKeepsTheNamedTracks(t *testing.T) {
	ffmpeg9OrSkip(t)
	// The donor's two tracks behind a picture.
	withVideo := testmedia.Make(t, "donor-video.mkv", testmedia.Spec{
		Shortest: true,
		Video:    []testmedia.Video{{Source: "testsrc2=size=160x90:rate=24:duration=4", Options: map[string]string{"preset": "ultrafast"}}},
		Audio:    donorTracks(4, 0, "stereo"),
	})
	out := filepath.Join(t.TempDir(), "donor.mka")
	require.NoError(t, ExtractAudio(context.Background(), withVideo, out, []int{1}))
	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 1)
	assert.Equal(t, "audio", p.Streams[0].CodecType)
	assert.Equal(t, "ac3", p.Streams[0].CodecName, "copied, not re-encoded")
	assert.Equal(t, "eng", p.Streams[0].Tags["language"])
}

// TestAGraftPlacesTheDonorsTrackOnTheTargetsTimeline: the donor's English
// runs 1.5 s late, the map says so, and the grafted track lands on the
// target's clock -- every target stream copied, the tags kept, the dub
// the default.
func TestAGraftPlacesTheDonorsTrackOnTheTargetsTimeline(t *testing.T) {
	target := graftTarget(t, 12)
	donor := graftDonor(t, 14, 1.5)
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	plan.Tags = map[string]string{"CLUSTARR_GRAFT": "abc123"}
	out := filepath.Join(t.TempDir(), "target.part.mkv")
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &GraftAudio{
		Donor: donor, Stream: 1, Language: "eng", Title: "English (dub, grafted)", Default: true,
		Map: func(s float64) (float64, bool) { return s + 1.5, true },
	}})
	require.NoError(t, err)

	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 4, "video, the Japanese, the graft, the subtitle")
	assert.Equal(t, "h264", p.Streams[0].CodecName)
	assert.Equal(t, "jpn", p.Streams[1].Tags["language"])
	assert.Equal(t, 0, p.Streams[1].Disposition["default"], "the copied track gives up the default flag")
	assert.Equal(t, "aac", p.Streams[2].CodecName)
	assert.Equal(t, 2, p.Streams[2].Channels)
	assert.Equal(t, "eng", p.Streams[2].Tags["language"])
	assert.Equal(t, "English (dub, grafted)", p.Streams[2].Tags["title"])
	assert.Equal(t, 1, p.Streams[2].Disposition["default"])
	assert.Equal(t, 1, p.Streams[2].Disposition["dub"])
	assert.Equal(t, "subrip", p.Streams[3].CodecName)
	assert.Equal(t, "abc123", p.Format.Tags["CLUSTARR_GRAFT"])
	assert.Equal(t, "p@h", p.Format.Tags["CLUSTARR_PROFILE"], "the target's container tags are kept")
	assert.InDelta(t, ffprobeJSON(t, target).seconds(t), p.seconds(t), 0.1)

	got, err := DecodePCM(context.Background(), out, 1, 8000)
	require.NoError(t, err)
	want, err := DecodePCM(context.Background(), burstsRef(t, 2, 14), 0, 8000)
	require.NoError(t, err)
	lag, c := lagOf(want, got, 800, 10)
	assert.Greater(t, c, 0.8, "the graft is the donor's English")
	assert.InDelta(t, 0, lag, 8, "within 1 ms of the target's clock (lag %d samples)", lag)
	assert.InDelta(t, 12*8000, len(got), 0.1*8000, "as long as the target")
}

func TestAGraftIsSilentWhereTheMapHasNothing(t *testing.T) {
	target := graftTarget(t, 10)
	donor := graftDonor(t, 10, 0)
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "target.part.mkv")
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &GraftAudio{
		Donor: donor, Stream: 1, Language: "eng",
		Map: func(s float64) (float64, bool) { return s, s < 4 || s >= 7 },
	}})
	require.NoError(t, err)
	got, err := DecodePCM(context.Background(), out, 1, 8000)
	require.NoError(t, err)
	assert.Greater(t, rms(got[1*8000:3*8000]), 0.05, "the donor plays where the map covers")
	assert.Less(t, rms(got[int(4.2*8000):int(6.8*8000)]), 1e-3, "silence in the gap")
	assert.Greater(t, rms(got[int(7.5*8000):9*8000]), 0.05, "and again after it")
}

func TestAShortDonorIsPaddedToTheTarget(t *testing.T) {
	target := graftTarget(t, 10)
	donor := graftDonor(t, 4, 0)
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "target.part.mkv")
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &GraftAudio{
		Donor: donor, Stream: 1, Language: "eng", Map: func(s float64) (float64, bool) { return s, true },
	}})
	require.NoError(t, err)
	got, err := DecodePCM(context.Background(), out, 1, 8000)
	require.NoError(t, err)
	assert.InDelta(t, 10*8000, len(got), 0.1*8000, "the track runs the target's length")
	assert.Less(t, rms(got[5*8000:9*8000]), 1e-3, "silence past the donor's end")
}

// TestAGraftedTrackIsInterleavedWithTheVideo: the dub's packets lie among
// the video's of the same time, not after them -- a player that switches to
// the dub mid-file must not read the whole file to find it. The donor's
// demuxer runs apart from the target's, and copying outruns decoding and
// encoding: unpaced, the whole dub landed after the video (final review).
func TestAGraftedTrackIsInterleavedWithTheVideo(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	target := testmedia.Make(t, "target.mkv", testmedia.Spec{
		Duration: 120 * time.Second,
		Video: []testmedia.Video{{
			Source: "testsrc2=size=640x360:rate=24,noise=alls=20:allf=t", Options: map[string]string{"preset": "ultrafast", "b": "2M"},
		}},
		Audio: []testmedia.Audio{{Samples: bursts(1, 120, 0), Stream: testmedia.Stream{Language: "jpn"}}},
	})
	donor := graftDonor(t, 120, 0)
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	out := filepath.Join(dir, "target.part.mkv")
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &GraftAudio{
		Donor: donor, Stream: 1, Language: "eng", Map: func(s float64) (float64, bool) { return s, true },
	}})
	require.NoError(t, err)

	type pkt struct {
		at  float64
		pos int64
	}
	var video, dub []pkt
	for _, p := range testmedia.Packets(t, out) {
		switch p.Stream {
		case 0:
			video = append(video, pkt{p.PTS.Seconds(), p.Pos})
		case 2:
			dub = append(dub, pkt{p.PTS.Seconds(), p.Pos})
		}
	}
	require.NotEmpty(t, video)
	require.NotEmpty(t, dub)
	// Each dub packet must sit between the video packets 5 s before and 5 s
	// after it in the file.
	posAt := func(at float64) (lo, hi int64) {
		lo, hi = 0, video[len(video)-1].pos
		for _, v := range video {
			if v.at <= at-5 && v.pos > lo {
				lo = v.pos
			}
			if v.at >= at+5 && v.pos < hi {
				hi = v.pos
			}
		}
		return lo, hi
	}
	near := 0
	for _, d := range dub {
		if lo, hi := posAt(d.at); d.pos >= lo && d.pos <= hi {
			near++
		}
	}
	assert.GreaterOrEqual(t, float64(near)/float64(len(dub)), 0.95, "%d of %d dub packets lie among the video of their time", near, len(dub))
}

// A copy plan carries each track's default and commentary flags: the plan
// owns them (audioOptions), so a graft that is not the default keeps the
// target's own.
func TestACopyPlanKeepsTheTracksFlags(t *testing.T) {
	src := everythingClip(t, 2)
	plan, err := CopyPlan(src)
	require.NoError(t, err)
	require.Len(t, plan.Audio, 2)
	assert.True(t, plan.Audio[0].Default)
	assert.False(t, plan.Audio[0].Comment)
	assert.True(t, plan.Audio[1].Comment)
	assert.False(t, plan.Audio[1].Default)
}

// graftDonorSurround is graftDonor with its English in 5.1 (the bursts in
// the centre channel, where ffmpeg upmixes mono), as a surround dub is.
func graftDonorSurround(t *testing.T, seconds float64) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	return testmedia.Make(t, "donor51.mkv", testmedia.Spec{Audio: donorTracks(seconds, 0, "5.1")})
}

// A surround dub is grafted as an AAC 2.0 companion and AC-3 5.1, AAC
// first (MP4 standard spec §3), both on the target's clock, the AAC the
// default.
func TestASurroundDubIsGraftedAsAC3AndAAC(t *testing.T) {
	target := graftTarget(t, 12)
	donor := graftDonorSurround(t, 12)
	g := GraftAudio{
		Donor: donor, Stream: 1, Language: "eng", Title: "English", Default: true, Surround: true,
		Map: func(s float64) (float64, bool) { return s, true },
	}
	require.Equal(t, 2, g.Tracks())
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "target.part.mkv")
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &g})
	require.NoError(t, err)

	var auds []string
	for _, s := range ffprobeJSON(t, out).Streams {
		if s.CodecType == "audio" {
			auds = append(auds, fmt.Sprintf("%s/%d/%s/%s/%d/%d", s.CodecName, s.Channels, s.Tags["language"], s.Tags["title"], s.Disposition["default"], s.Disposition["dub"]))
		}
	}
	assert.Equal(t, []string{"aac/1/jpn//0/0", "aac/2/eng/English (Stereo)/1/1", "ac3/6/eng/English/0/1"}, auds)
	want, err := DecodePCM(context.Background(), burstsRef(t, 2, 12), 0, 8000)
	require.NoError(t, err)
	for _, i := range []int{1, 2} {
		got, err := DecodePCM(context.Background(), out, i, 8000)
		require.NoError(t, err)
		lag, c := lagOf(want, got, 800, 10)
		assert.Greater(t, c, 0.8, "track %d is the donor's English", i)
		assert.InDelta(t, 0, lag, 8, "track %d on the target's clock (lag %d samples)", i, lag)
	}
}
