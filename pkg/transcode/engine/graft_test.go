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
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// burstsWAV writes seconds of random decaying tone bursts (a soundtrack
// with onsets, unlike a sine) as 48 kHz mono 16-bit WAV, from seed.
func burstsWAV(t *testing.T, seed uint64, seconds float64) string {
	t.Helper()
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
	b := make([]byte, 44+2*len(x))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(x)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(x)))
	for i, v := range x {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(max(-1, min(1, v))*32000)))
	}
	p := filepath.Join(t.TempDir(), "bursts-"+strconv.FormatUint(seed, 10)+".wav")
	require.NoError(t, os.WriteFile(p, b, 0o644))
	return p
}

// burstsRef is burstsWAV as FLAC with a mono layout, for decoding as a
// reference: a WAV's layout is unspecified, which the resampler (as the
// transcoder's audio stage) refuses.
func burstsRef(t *testing.T, seed uint64, seconds float64) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "ref.flac")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", burstsWAV(t, seed, seconds),
		"-af", "aformat=channel_layouts=mono", out)
	return out
}

// graftTarget is a seconds-long Matroska file: H.264 video and one AAC
// Japanese track of bursts seed 1 (default), an English subtitle, a
// container title.
func graftTarget(t *testing.T, seconds int) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	srt := filepath.Join(dir, "sub.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o644))
	out := filepath.Join(dir, "target.mkv")
	d := strconv.Itoa(seconds)
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration="+d,
		"-i", burstsWAV(t, 1, float64(seconds)), "-i", srt,
		"-map", "0", "-map", "1", "-map", "2", "-t", d,
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=jpn", "-disposition:a:0", "default",
		"-metadata:s:s:0", "language=eng", "-metadata", "title=Target", "-metadata", "CLUSTARR_PROFILE=p@h", out)
	return out
}

// graftDonor is an audio-only donor: Japanese (bursts 1) and English
// (bursts 2), both delayed by lead seconds, seconds long in all.
func graftDonor(t *testing.T, seconds, lead float64) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	out := filepath.Join(t.TempDir(), "donor.mkv")
	ms := strconv.Itoa(int(lead * 1000))
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-i", burstsWAV(t, 1, seconds), "-i", burstsWAV(t, 2, seconds),
		"-filter_complex", "[0:a]adelay="+ms+"[j];[1:a]adelay="+ms+"[e]",
		"-map", "[j]", "-map", "[e]", "-t", strconv.FormatFloat(seconds, 'f', 3, 64),
		"-c:a", "ac3", "-ac", "2", "-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng", out)
	return out
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
	src := filepath.Join(t.TempDir(), "late.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=6",
		"-itsoffset", "0.5", "-i", burstsWAV(t, 1, 5),
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", src)
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
	donor := graftDonor(t, 4, 0)
	withVideo := filepath.Join(t.TempDir(), "donor-video.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=4", "-i", donor,
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "copy", "-shortest", withVideo)
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
	target := filepath.Join(dir, "target.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=120,noise=alls=20:allf=t",
		"-i", burstsWAV(t, 1, 120), "-map", "0", "-map", "1", "-t", "120",
		"-c:v", "libx264", "-preset", "ultrafast", "-b:v", "2M", "-c:a", "aac", "-metadata:s:a:0", "language=jpn", target)
	donor := graftDonor(t, 120, 0)
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	out := filepath.Join(dir, "target.part.mkv")
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &GraftAudio{
		Donor: donor, Stream: 1, Language: "eng", Map: func(s float64) (float64, bool) { return s, true },
	}})
	require.NoError(t, err)

	raw := run(t, "ffprobe", "-v", "error", "-show_entries", "packet=stream_index,pts_time,pos", "-of", "csv=p=0", out)
	type pkt struct {
		at  float64
		pos int64
	}
	var video, dub []pkt
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		at, err1 := strconv.ParseFloat(f[1], 64)
		pos, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		switch f[0] {
		case "0":
			video = append(video, pkt{at, pos})
		case "2":
			dub = append(dub, pkt{at, pos})
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
	out := filepath.Join(t.TempDir(), "donor51.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-i", burstsWAV(t, 1, seconds), "-i", burstsWAV(t, 2, seconds),
		"-map", "0:a", "-map", "1:a", "-t", strconv.FormatFloat(seconds, 'f', 3, 64),
		"-c:a", "ac3", "-ac:a:0", "2", "-ac:a:1", "6",
		"-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng", out)
	return out
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
