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

package worker

import (
	"context"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/app/squash/worker/inprocess"
	"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

const graftClip = 120 // seconds: four alignment windows

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
	p := filepath.Join(t.TempDir(), "b"+strconv.FormatUint(seed, 10)+".wav")
	require.NoError(t, os.WriteFile(p, b, 0o644))
	return p
}

func ffmpegRun(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command(ffmpegBin, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	require.NoError(t, err, "ffmpeg %s\n%s", strings.Join(args, " "), out)
}

// joinedGraftFixture is the worker fixture with a two-minute H.264 source
// whose Japanese track is bursts 1, and a donor under the root folder's
// .clustarr/donors with Japanese (bursts 1, or unrelated with other) and
// English, 1.5 s late; and the transcode task carrying the graft.
func joinedGraftFixture(t *testing.T, c client.Client, other bool) (*fixture, task.Task) {
	t.Helper()
	requireFFmpeg(t)
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg libraries: %v", err)
	}
	f := newFixtureWith(t, c, fixtureOptions{writeSource: func(t *testing.T, path string) {
		d := strconv.Itoa(graftClip)
		ffmpegRun(t, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration="+d, "-i", bursts(t, 1, graftClip),
			"-map", "0", "-map", "1", "-t", d, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac",
			"-metadata:s:a:0", "language=jpn", path)
	}})
	donorDir := filepath.Join(f.dataDir, "media/movies/.clustarr/donors/uid-1")
	require.NoError(t, os.MkdirAll(donorDir, 0o755))
	anchor := uint64(1)
	if other {
		anchor = 7
	}
	ffmpegRun(t, "-i", bursts(t, anchor, graftClip), "-i", bursts(t, 2, graftClip),
		"-filter_complex", "[0:a]adelay=1500[j];[1:a]adelay=1500[e]", "-map", "[j]", "-map", "[e]",
		"-c:a", "ac3", "-ac", "2", "-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng",
		filepath.Join(donorDir, "film-2020.mka"))

	ctx := context.Background()
	tj := f.get(t, c)
	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp))
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf))
	var folders catalogv1alpha1.RootFolderList
	require.NoError(t, c.List(ctx, &folders, client.InNamespace(tj.Namespace)))
	tk, err := BuildTask(tj, &tp, &mf, folders.Items, 1, tp.Spec.Hardware)
	require.NoError(t, err)
	tk.Engine = task.EngineFFgo
	tk.Graft = &grafttask.Task{
		Graft: f.ns + "/film-2020-audiograft", Target: f.logical, TargetProbeHash: f.probeHash, Root: "/data/media/movies",
		Donor: "/data/media/movies/.clustarr/donors/uid-1/film-2020.mka", Language: "en", Anchor: "ja", Default: true,
	}
	return f, tk
}

// TestATranscodeCarriesAJoinedGraftInOnePass (phase 4 addendum): a graft
// riding along with a transcode is aligned before the encode and muxed in
// with it -- one pass, one rewrite of the file: HEVC, the Japanese, the
// English dub, both tags.
func TestATranscodeCarriesAJoinedGraftInOnePass(t *testing.T) {
	c := requireCluster(t)
	f, tk := joinedGraftFixture(t, c, false)
	opts := f.options()
	eng, err := inprocess.New()
	require.NoError(t, err)
	opts.Engine = eng

	out := Process(context.Background(), tk, opts)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)
	require.NotNil(t, out.Graft)
	assert.Equal(t, grafttask.PhaseSucceeded, out.Graft.Phase, out.Graft.Message)
	assert.Equal(t, grafttask.ReasonGrafted, out.Graft.Reason)
	assert.NotEmpty(t, out.Graft.GraftTag)

	mi, raw, err := ffprobeexec.Probe(context.Background(), f.local)
	require.NoError(t, err)
	assert.Equal(t, "hevc", mi.VideoCodec)
	assert.Equal(t, f.profileName+"@"+f.profileHash, formatTag(raw, "CLUSTARR_PROFILE"), "a transcode")
	assert.Equal(t, out.Graft.GraftTag, formatTag(raw, "CLUSTARR_GRAFT"), "and a graft")
	tracks, err := engine.AudioTracks(f.local)
	require.NoError(t, err)
	require.Len(t, tracks, 2)
	assert.Equal(t, "jpn", tracks[0].Language)
	assert.Equal(t, "eng", tracks[1].Language)
	assert.True(t, tracks[1].Default)
}

// TestAJoinedGraftThatWillNotAlignLeavesTheTranscodeAlone: a bad donor
// never costs the transcode.
func TestAJoinedGraftThatWillNotAlignLeavesTheTranscodeAlone(t *testing.T) {
	c := requireCluster(t)
	f, tk := joinedGraftFixture(t, c, true)
	opts := f.options()
	eng, err := inprocess.New()
	require.NoError(t, err)
	opts.Engine = eng

	out := Process(context.Background(), tk, opts)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code, "the transcode itself succeeds")
	require.NotNil(t, out.Graft)
	assert.Equal(t, grafttask.PhaseFailed, out.Graft.Phase)
	assert.Equal(t, grafttask.ReasonAlignmentRejected, out.Graft.Reason)
	mi, raw, err := ffprobeexec.Probe(context.Background(), f.local)
	require.NoError(t, err)
	assert.Equal(t, "hevc", mi.VideoCodec)
	assert.Empty(t, formatTag(raw, "CLUSTARR_GRAFT"))
	tracks, err := engine.AudioTracks(f.local)
	require.NoError(t, err)
	assert.Len(t, tracks, 1)
}
