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
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/worker/graft"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

const clip = 120 // seconds: four 20 s alignment windows, 30 s apart

func ffmpegOrSkip(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("no %s to make test clips", bin)
		}
	}
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg libraries: %v", err)
	}
	if _, avc, _ := ffgo.Version(); avc>>16 != 63 {
		t.Skip("not FFmpeg 9")
	}
}

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	require.NoError(t, err, "ffmpeg %s\n%s", strings.Join(args, " "), out)
}

// bursts writes seconds of seeded random tone bursts, 48 kHz mono WAV.
func bursts(t *testing.T, seed uint64, seconds float64) string {
	t.Helper()
	const rate = 48000
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	x := make([]float64, int(seconds*rate))
	for at := 0.0; at < seconds; at += 0.1 + r.Float64()*0.3 {
		f, amp, start := 200+r.Float64()*2800, 0.2+r.Float64()*0.5, int(at*rate)
		for i := 0; i < rate/4 && start+i < len(x); i++ {
			x[start+i] += amp * math.Exp(-float64(i)/(rate/20)) * math.Sin(2*math.Pi*f*float64(i)/rate)
		}
	}
	b := make([]byte, 44+2*len(x))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(x)))
	copy(b[8:], "WAVEfmt ")
	for i, v := range []uint32{16} {
		binary.LittleEndian.PutUint32(b[16+4*i:], v)
	}
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
	p := filepath.Join(t.TempDir(), "b"+strconv.FormatUint(seed, 10)+".wav")
	require.NoError(t, os.WriteFile(p, b, 0o644))
	return p
}

// library lays out /data under a temp dir: the tv root folder, the target
// episode (H.264 + Japanese AAC of bursts mne, plus tracks from extra), and
// a donor -- video, Japanese and English, delayed by lead -- placed as
// importarr places one.
type library struct {
	dataDir string
	task    grafttask.Task
}

func newLibrary(t *testing.T, donorAudio []string, lead float64, targetExtra ...string) library {
	t.Helper()
	ffmpegOrSkip(t)
	data := t.TempDir()
	show := filepath.Join(data, "tv", "Monster", "Season 01")
	donors := filepath.Join(data, "tv", ".clustarr", "donors", "uid-1")
	require.NoError(t, os.MkdirAll(show, 0o755))
	require.NoError(t, os.MkdirAll(donors, 0o755))
	target := filepath.Join(show, "Monster - S01E02.mkv")
	d := strconv.Itoa(clip)
	args := []string{"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=" + d, "-i", bursts(t, 1, clip)}
	maps := []string{"-map", "0", "-map", "1"}
	meta := []string{"-metadata:s:a:0", "language=jpn"}
	for i, e := range targetExtra {
		args = append(args, "-i", bursts(t, 50+uint64(i), clip))
		maps = append(maps, "-map", strconv.Itoa(2+i))
		meta = append(meta, "-metadata:s:a:"+strconv.Itoa(1+i), "language="+e)
	}
	args = append(append(append(args, maps...), "-t", d, "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac"), meta...)
	ffmpeg(t, append(args, target)...)

	// The donor: each of donorAudio is "jpn" (the target's bursts), "eng"
	// (a dub: the bursts under other dialogue) or "other" (unrelated).
	donor := filepath.Join(donors, "monster-s01e02.mkv")
	ms := strconv.Itoa(int(lead * 1000))
	dargs := []string{"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=" + strconv.Itoa(clip+2),
		"-i", bursts(t, 1, clip), "-i", bursts(t, 2, clip), "-i", bursts(t, 7, clip)}
	var graph []string
	var dmaps, dmeta []string
	for i, a := range donorAudio {
		out := "a" + strconv.Itoa(i)
		switch a {
		case "jpn":
			graph = append(graph, "[1:a]adelay="+ms+"["+out+"]")
		case "eng":
			graph = append(graph, "[1:a]volume=0.5[m"+out+"];[2:a]volume=0.8[d"+out+"];[m"+out+"][d"+out+"]amix=inputs=2:normalize=0,adelay="+ms+"["+out+"]")
		default:
			graph = append(graph, "[3:a]adelay="+ms+"["+out+"]")
			a = "jpn"
		}
		dmaps = append(dmaps, "-map", "["+out+"]")
		if donorAudio[i] == "other-eng" {
			a = "eng"
		}
		dmeta = append(dmeta, "-metadata:s:a:"+strconv.Itoa(i), "language="+a)
	}
	dargs = append(append(dargs, "-filter_complex", strings.Join(graph, ";"), "-map", "0"), dmaps...)
	dargs = append(append(dargs, "-t", strconv.Itoa(clip+2), "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "ac3", "-ac", "2"), dmeta...)
	ffmpeg(t, append(dargs, donor)...)

	logical := func(p string) string { return "/data" + strings.TrimPrefix(p, data) }
	st, err := os.Stat(target)
	require.NoError(t, err)
	return library{dataDir: data, task: grafttask.Task{
		Graft: "clustarr-system/monster-s01e02-audiograft", Root: "/data/tv",
		Target: logical(target), TargetProbeHash: mediainfo.ProbeHash(logical(target), st.Size(), st.ModTime()),
		Donor: logical(donor), Language: "en", Anchor: "ja", Default: true, RecycleBin: "/data/.recycle",
	}}
}

func (l library) path(logical string) string { return filepath.Join(l.dataDir, strings.TrimPrefix(logical, "/data")) }

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

	out, err := exec.Command("ffprobe", "-v", "error", "-show_format", "-of", "json", target).Output()
	require.NoError(t, err)
	var p struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	require.NoError(t, json.Unmarshal(out, &p))
	assert.Equal(t, res.GraftTag, p.Format.Tags["CLUSTARR_GRAFT"])
	assert.Empty(t, p.Format.Tags["CLUSTARR_PROFILE"], "a graft is not a transcode")

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
