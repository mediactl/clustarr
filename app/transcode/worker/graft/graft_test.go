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

package graft_test

import (
	"context"
	"crypto/sha256"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/worker/graft"
	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

const (
	clip = 120   // seconds: four 20 s alignment windows, 30 s apart
	rate = 48000 // the bursts' sample rate
)

// bursts is seconds of seeded random tone bursts, 48 kHz mono.
func bursts(seed uint64, seconds float64) []float64 {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	x := make([]float64, int(seconds*rate))
	for at := 0.0; at < seconds; at += 0.1 + r.Float64()*0.3 {
		f, amp, start := 200+r.Float64()*2800, 0.2+r.Float64()*0.5, int(at*rate)
		for i := 0; i < rate/4 && start+i < len(x); i++ {
			x[start+i] += amp * math.Exp(-float64(i)/(rate/20)) * math.Sin(2*math.Pi*f*float64(i)/rate)
		}
	}
	return x
}

// delayed is x behind lead seconds of silence (ffmpeg's adelay).
func delayed(x []float64, lead float64) []float64 {
	return append(make([]float64, int(lead*rate)), x...)
}

// mixed is a*x + b*y sample by sample, as long as the longer (amix
// normalize=0 over volume-scaled inputs).
func mixed(a float64, x []float64, b float64, y []float64) []float64 {
	out := make([]float64, max(len(x), len(y)))
	for i := range out {
		if i < len(x) {
			out[i] += a * x[i]
		}
		if i < len(y) {
			out[i] += b * y[i]
		}
	}
	return out
}

// library lays out /data under a temp dir: the tv root folder, the target
// episode (H.264 + Japanese AAC of bursts 1, plus tracks from extra), and
// a donor -- video, Japanese and English, delayed by lead -- placed as
// import places one.
type library struct {
	dataDir string
	task    grafttask.Task
}

func newLibrary(t *testing.T, donorAudio []string, lead float64, targetExtra ...string) library {
	t.Helper()
	testmedia.Require(t)
	data := t.TempDir()
	show := filepath.Join(data, "tv", "Monster", "Season 01")
	donors := filepath.Join(data, "tv", ".clustarr", "donors", "uid-1")
	require.NoError(t, os.MkdirAll(show, 0o755))
	require.NoError(t, os.MkdirAll(donors, 0o755))
	video := testmedia.Video{Source: "testsrc2=size=160x90:rate=24", Encoder: "libx264", Options: map[string]string{"preset": "ultrafast"}}

	target := filepath.Join(show, "Monster - S01E02.mkv")
	audio := []testmedia.Audio{{Samples: &testmedia.Samples{Rate: rate, Mono: bursts(1, clip)}, Encoder: "aac",
		Stream: testmedia.Stream{Language: "jpn"}}}
	for i, e := range targetExtra {
		audio = append(audio, testmedia.Audio{Samples: &testmedia.Samples{Rate: rate, Mono: bursts(50+uint64(i), clip)},
			Encoder: "aac", Stream: testmedia.Stream{Language: e}})
	}
	require.NoError(t, testmedia.Write(target, testmedia.Spec{Duration: clip * time.Second, Video: []testmedia.Video{video}, Audio: audio}))

	// The donor: each of donorAudio is "jpn" (the target's bursts), "eng"
	// (a dub: the bursts under other dialogue), "other" (unrelated, tagged
	// jpn) or "other-eng" (unrelated, tagged eng); AC-3 stereo, lead late.
	donor := filepath.Join(donors, "monster-s01e02.mkv")
	var dub []testmedia.Audio
	for _, a := range donorAudio {
		x, lang := bursts(7, clip), "jpn"
		switch a {
		case "jpn":
			x = bursts(1, clip)
		case "eng":
			x, lang = mixed(0.5, bursts(1, clip), 0.8, bursts(2, clip)), "eng"
		case "other-eng":
			lang = "eng"
		}
		dub = append(dub, testmedia.Audio{Samples: &testmedia.Samples{Rate: rate, Mono: delayed(x, lead)},
			Encoder: "ac3", Layout: "stereo", Stream: testmedia.Stream{Language: lang}})
	}
	require.NoError(t, testmedia.Write(donor, testmedia.Spec{Duration: (clip + 2) * time.Second, Video: []testmedia.Video{video}, Audio: dub}))

	logical := func(p string) string { return "/data" + strings.TrimPrefix(p, data) }
	st, err := os.Stat(target)
	require.NoError(t, err)
	return library{dataDir: data, task: grafttask.Task{
		File: schema.Ref{Namespace: "clustarr-system", Name: "monster-s01e02", UID: "mf-uid"}, Root: "/data/tv",
		Target: logical(target), TargetProbeHash: mediainfo.ProbeHash(logical(target), st.Size(), st.ModTime()),
		Donor: logical(donor), Language: "en", Anchor: "ja", Default: true, RecycleBin: "/data/.recycle",
	}}
}

func (l library) path(logical string) string {
	return filepath.Join(l.dataDir, strings.TrimPrefix(logical, "/data"))
}

func sum(t *testing.T, p string) [32]byte {
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return sha256.Sum256(b)
}

func TestAGraftSwapsInTheAlignedDub(t *testing.T) {
	l := newLibrary(t, []string{"jpn", "eng"}, 1.5)
	start := time.Now()
	res := graft.Run(context.Background(), l.task, graft.Options{DataDir: l.dataDir})
	t.Logf("graft took %s: %+v", time.Since(start), res)
	require.Equal(t, grafttask.PhaseSucceeded, res.Phase, res.Message)
	assert.Equal(t, grafttask.ReasonGrafted, res.Reason)
	assert.Equal(t, "1", res.RateName)
	require.NotEmpty(t, res.Segments)
	assert.InDelta(t, 1500, res.Segments[0].DonorStartMillis-res.Segments[0].TargetStartMillis, 40, "the donor runs 1.5 s late")
	assert.LessOrEqual(t, res.ResidualMillis, int32(40))

	target := l.path(l.task.Target)
	tracks, err := engine.AudioTracks(target)
	require.NoError(t, err)
	require.Len(t, tracks, 2)
	assert.Equal(t, "jpn", tracks[0].Language)
	assert.False(t, tracks[0].Default)
	assert.Equal(t, "eng", tracks[1].Language)
	assert.Equal(t, "English (dub, grafted)", tracks[1].Title)
	assert.True(t, tracks[1].Default)

	tags := testmedia.Inspect(t, target).Tags
	assert.Equal(t, res.GraftTag, tags["CLUSTARR_GRAFT"])
	assert.Empty(t, tags["CLUSTARR_PROFILE"], "a graft is not a transcode")

	_, err = os.Stat(l.path(l.task.Donor))
	assert.ErrorIs(t, err, os.ErrNotExist, "the donor's video is gone")
	mka := l.path(res.DonorAudio)
	dt, err := engine.AudioTracks(mka)
	require.NoError(t, err)
	assert.Len(t, dt, 2, "the donor is kept as its two audio tracks")
	bin, err := os.ReadDir(l.path("/data/.recycle"))
	require.NoError(t, err)
	assert.NotEmpty(t, bin, "the original went to the recycle bin")

	// Again, from the kept donor: the target now carries English.
	st, err := os.Stat(target)
	require.NoError(t, err)
	again := l.task
	again.Donor = res.DonorAudio
	again.TargetProbeHash = mediainfo.ProbeHash(l.task.Target, st.Size(), st.ModTime())
	res2 := graft.Run(context.Background(), again, graft.Options{DataDir: l.dataDir})
	assert.Equal(t, grafttask.ReasonPresent, res2.Reason, res2.Message)
}

func TestUnrelatedAudioFailsAndLeavesTheTarget(t *testing.T) {
	l := newLibrary(t, []string{"other", "other-eng"}, 0)
	before := sum(t, l.path(l.task.Target))
	res := graft.Run(context.Background(), l.task, graft.Options{DataDir: l.dataDir})
	require.Equal(t, grafttask.PhaseFailed, res.Phase)
	assert.Equal(t, grafttask.ReasonAlignmentRejected, res.Reason, res.Message)
	assert.Equal(t, before, sum(t, l.path(l.task.Target)), "the target is untouched")
	parts, _ := filepath.Glob(filepath.Join(filepath.Dir(l.path(l.task.Target)), "*.part*"))
	assert.Empty(t, parts, "no part file is left behind")
}

func TestADonorWithoutTheLanguageFails(t *testing.T) {
	l := newLibrary(t, []string{"jpn"}, 0)
	res := graft.Run(context.Background(), l.task, graft.Options{DataDir: l.dataDir})
	assert.Equal(t, grafttask.ReasonDonorLacksLanguage, res.Reason, res.Message)
}

func TestAChangedTargetIsNotSwapped(t *testing.T) {
	l := newLibrary(t, []string{"jpn", "eng"}, 0)
	l.task.TargetProbeHash = "stale"
	before := sum(t, l.path(l.task.Target))
	res := graft.Run(context.Background(), l.task, graft.Options{DataDir: l.dataDir})
	assert.Equal(t, grafttask.ReasonTargetChanged, res.Reason, res.Message)
	assert.Equal(t, before, sum(t, l.path(l.task.Target)))
}

func TestATargetWithTheLanguageIsPresent(t *testing.T) {
	l := newLibrary(t, []string{"jpn", "eng"}, 0, "eng")
	res := graft.Run(context.Background(), l.task, graft.Options{DataDir: l.dataDir})
	assert.Equal(t, grafttask.PhaseSucceeded, res.Phase)
	assert.Equal(t, grafttask.ReasonPresent, res.Reason)
}

func TestAPathOutsideTheRootIsRefused(t *testing.T) {
	res := graft.Run(context.Background(), grafttask.Task{
		Target: "/data/movies/x.mkv", TargetProbeHash: "h", Root: "/data/tv", Donor: "/data/tv/.clustarr/d.mkv",
		Language: "en", Anchor: "ja",
	}, graft.Options{DataDir: t.TempDir()})
	assert.Equal(t, grafttask.ReasonInvalidTask, res.Reason)
}

// TestTheReducedDonorKeepsEveryWantedLanguage: a profile wanting two dubs
// grafts one per run; the donor reduced by the first keeps the second.
func TestTheReducedDonorKeepsEveryWantedLanguage(t *testing.T) {
	l := newLibrary(t, []string{"jpn", "eng", "other-eng"}, 0)
	// The third track, tagged eng above, stands in for a French dub: stream
	// 3, the third audio track behind the video.
	fre := testmedia.Remux(t, l.path(l.task.Donor), "donor.fre.mkv",
		testmedia.Retag{StreamMetadata: map[int]map[string]string{3: {"language": "fre"}}})
	require.NoError(t, os.Rename(fre, l.path(l.task.Donor)))
	l.task.Languages = []string{"en", "fr"}
	res := graft.Run(context.Background(), l.task, graft.Options{DataDir: l.dataDir})
	require.Equal(t, grafttask.PhaseSucceeded, res.Phase, res.Message)
	tracks, err := engine.AudioTracks(l.path(res.DonorAudio))
	require.NoError(t, err)
	var langs []string
	for _, tr := range tracks {
		langs = append(langs, tr.Language)
	}
	require.ElementsMatch(t, []string{"jpn", "eng", "fre"}, langs)
}
