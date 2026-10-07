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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// These tests run Process on a hand-built task against a scripted engine:
// no cluster, no FFmpeg. What they pin is the worker's own judgement --
// whether it encodes at all, and what it does to the files beside the
// source -- not the engine's.

const safetyJobUID = "0123abcd-0000-4000-8000-000000000000"

// scriptedEngine answers the source's probe with a scripted reading, and
// encodes by writing a stand-in output it always verifies.
type scriptedEngine struct {
	source string // the source's local path
	mi     *commonv1alpha1.MediaInfo
	raw    *mediainfo.Raw
	err    error

	mu      sync.Mutex
	encodes int
	// atEncode lists the folder's entries when the encode starts.
	atEncode []string
}

func (e *scriptedEngine) Probe(_ context.Context, path string) (*commonv1alpha1.MediaInfo, *mediainfo.Raw, error) {
	if path == e.source {
		return e.mi, e.raw, e.err
	}
	return nil, nil, errors.New("an output: not probed in these tests")
}

func (e *scriptedEngine) Encode(_ context.Context, _ standard.Result, _ transcode.Tier, _, output string,
	_ func(transcode.Progress),
) (string, error) {
	e.mu.Lock()
	e.encodes++
	e.atEncode = listDir(filepath.Dir(output))
	e.mu.Unlock()
	return "", os.WriteFile(output, []byte("transcoded"), 0o644)
}

func (e *scriptedEngine) Verify(_ context.Context, _, output string, _ standard.Expectation) (*transcode.Report, error) {
	st, err := os.Stat(output)
	if err != nil {
		return nil, err
	}
	return &transcode.Report{OK: true, SizeBytes: st.Size()}, nil
}

func (e *scriptedEngine) Measure(context.Context, transcode.Hardware) (transcode.Measurement, error) {
	return transcode.Measurement{Tier: transcode.TierCPUx265}, nil
}

func listDir(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// uhdReading is a probe of a 2160p 10-bit H.264 source (encoded under the
// standard whatever its HDR), as the probe would map it: HDR10 when the
// first frame said PQ with a mastering display, SDR when it said nothing.
func uhdReading(path string, hdr string) (*commonv1alpha1.MediaInfo, *mediainfo.Raw) {
	raw := &mediainfo.Raw{
		Format: &ffprobe.Format{Filename: path, FormatName: "matroska,webm", DurationSeconds: 3600},
		Streams: []*ffprobe.Stream{{
			Index: 0, CodecType: string(ffprobe.StreamVideo), CodecName: "h264", PixFmt: "yuv420p10le",
			Width: 3840, Height: 2160, RFrameRate: "24/1",
		}},
	}
	switch hdr {
	case "hdr10":
		raw.ColorPrimaries, raw.ColorTransfer, raw.ColorSpace = "bt2020", "smpte2084", "bt2020nc"
		raw.MasteringDisplay = &mediainfo.MasteringDisplay{MaxLuminance: 10000000, MinLuminance: 1}
	case "hlg":
		raw.ColorPrimaries, raw.ColorTransfer, raw.ColorSpace = "bt2020", "arib-std-b67", "bt2020nc"
	}
	return mediainfo.FromRaw(raw), raw
}

type safetyCase struct {
	dir, local, logical string
	tk                  task.Task
	eng                 *scriptedEngine
}

// newSafetyCase is a source under a movie root, a task planned (by hash)
// from planned's reading of it, and an engine whose live probe reads it as
// live.
func newSafetyCase(t *testing.T, planned, live string) *safetyCase {
	t.Helper()
	dataDir := t.TempDir()
	logical := "/data/media/movies/Film (2020)/Film.2020.2160p.mp4"
	local := filepath.Join(dataDir, "media/movies/Film (2020)/Film.2020.2160p.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o755))
	require.NoError(t, os.WriteFile(local, []byte("the original, byte for byte"), 0o644))
	st, err := os.Stat(local)
	require.NoError(t, err)

	tk := task.Task{
		Job:     schema.Ref{Namespace: "media", Name: "film-abc", UID: safetyJobUID},
		Attempt: 2, Class: "cpu",
		Profile:    task.Profile{Name: "hevc", Hash: "abc", Spec: transcodev1alpha1.TranscodeProfileSpec{}},
		SourcePath: logical, SourceProbeHash: mediainfo.ProbeHash(logical, st.Size(), st.ModTime()),
		SourceSizeBytes: st.Size(), OutputPath: logical,
		Root:   task.RootFolder{Path: "/data/media/movies", RecycleBin: "/data/.recycle"},
		Engine: task.EngineFFgo,
	}
	pmi, praw := uhdReading(local, planned)
	tk.PlanHash = planHashOf(t, pmi, praw, tk)

	mi, raw := uhdReading(local, live)
	return &safetyCase{
		dir: filepath.Dir(local), local: local, logical: logical, tk: tk,
		eng: &scriptedEngine{source: local, mi: mi, raw: raw},
	}
}

// planHashOf is the hash the controller records: the standard's plan of
// the reading, as the worker plans it.
func planHashOf(t *testing.T, mi *commonv1alpha1.MediaInfo, raw *mediainfo.Raw, tk task.Task) string {
	t.Helper()
	info, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	p := standard.Plan(info, jobspec.StandardProfile(tk.Profile.Name, tk.Profile.Hash, tk.Profile.Spec),
		standard.Hardware{Tier: transcode.TierCPUx265})
	require.Equal(t, standard.DecisionEncode, p.Decision, p.Reason)
	return p.Hash()
}

func (c *safetyCase) process(t *testing.T) Outcome {
	t.Helper()
	dataDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(c.local))))
	return Process(context.Background(), c.tk, Options{
		DataDir: dataDir, Engine: c.eng, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265},
	})
}

func (c *safetyCase) requireSourceUntouched(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(c.local)
	require.NoError(t, err)
	assert.Equal(t, "the original, byte for byte", string(got))
	for _, name := range listDir(c.dir) {
		_, isPart := fsopsPart(name)
		assert.Falsef(t, isPart, "no part file of this attempt is left: %s", name)
	}
}

// A planned HDR source that the live probe reads as SDR is not encoded:
// the encode would be an SDR file (8-bit at 1080p) that replaces the HDR
// original for good. The task is retriable -- the next probe may read the
// frame -- and names both readings.
func TestAWorkerRefusesToEncodeAPlannedHDRSourceItReadsAsSDR(t *testing.T) {
	for _, planned := range []string{"hdr10", "hlg"} {
		t.Run(planned, func(t *testing.T) {
			c := newSafetyCase(t, planned, "sdr")
			out := c.process(t)
			require.Error(t, out.Err)
			assert.Equal(t, ExitRetriable, out.Code, "%v", out.Err)
			assert.Contains(t, out.Err.Error(), planned)
			assert.Zero(t, c.eng.encodes, "nothing may be encoded")
			c.requireSourceUntouched(t)
		})
	}
}

// The check is about losing HDR, not about any difference: a plan that
// matches, or a live reading that is HDR where the plan was SDR, encodes.
func TestAWorkerEncodesWhenTheLiveReadingLosesNoHDR(t *testing.T) {
	for name, c := range map[string]struct{ planned, live string }{
		"as planned, SDR":     {"sdr", "sdr"},
		"as planned, HDR10":   {"hdr10", "hdr10"},
		"HDR where SDR was":   {"sdr", "hdr10"},
		"HLG where HDR10 was": {"hdr10", "hlg"},
	} {
		t.Run(name, func(t *testing.T) {
			sc := newSafetyCase(t, c.planned, c.live)
			out := sc.process(t)
			require.NoError(t, out.Err)
			assert.Equal(t, 1, sc.eng.encodes)
			got, err := os.ReadFile(sc.local)
			require.NoError(t, err)
			assert.Equal(t, "transcoded", string(got))
		})
	}
}

// A source whose first frame the live probe could not read has an unknown
// HDR format, whatever its stream says: the worker writes a final file, so
// it refuses -- retriable -- rather than encode it as SDR. So does a probe
// that reported itself incomplete, which is never an invalid source.
func TestAWorkerRefusesASourceWhoseFirstFrameItCouldNotRead(t *testing.T) {
	t.Run("recorded on the reading", func(t *testing.T) {
		c := newSafetyCase(t, "sdr", "sdr")
		c.eng.raw.FrameErr = errors.New("no video frame decoded in 2000 packets")
		out := c.process(t)
		require.Error(t, out.Err)
		assert.Equal(t, ExitRetriable, out.Code, "%v", out.Err)
		assert.Contains(t, out.Err.Error(), "first video frame")
		assert.Zero(t, c.eng.encodes)
		c.requireSourceUntouched(t)
	})
	t.Run("the probe's error", func(t *testing.T) {
		c := newSafetyCase(t, "hdr10", "hdr10")
		c.eng.mi, c.eng.raw = nil, nil
		c.eng.err = fmt.Errorf("inprocess: probe x: %w: the stream says side data Mastering display metadata: %w",
			mediainfo.ErrIncompleteProbe, errors.New("decoded nothing"))
		out := c.process(t)
		require.Error(t, out.Err)
		assert.Equal(t, ExitRetriable, out.Code, "an incomplete probe is not an invalid source: %v", out.Err)
		assert.ErrorIs(t, out.Err, mediainfo.ErrIncompleteProbe)
		assert.Zero(t, c.eng.encodes)
		c.requireSourceUntouched(t)
	})
}

// The output is fsynced before it is renamed over the source: when the
// sync runs, the source still holds the original and the part holds the
// encode. A sync that fails -- a delayed write error -- keeps the original
// and removes the part, retriable.
func TestTheOutputIsSyncedBeforeItReplacesTheSource(t *testing.T) {
	old := syncPart
	t.Cleanup(func() { syncPart = old })

	t.Run("synced, then swapped", func(t *testing.T) {
		c := newSafetyCase(t, "sdr", "sdr")
		var synced []string
		syncPart = func(path string) error {
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, "transcoded", string(got), "the part is synced, with the encode in it")
			src, err := os.ReadFile(c.local)
			require.NoError(t, err)
			assert.Equal(t, "the original, byte for byte", string(src), "the sync comes before the rename")
			synced = append(synced, path)
			return old(path)
		}
		out := c.process(t)
		require.NoError(t, out.Err)
		require.Len(t, synced, 1)
		p, ok := fsopsPart(filepath.Base(synced[0]))
		require.True(t, ok, "the attempt's part is what is synced: %s", synced[0])
		assert.Equal(t, 2, p.Attempt)
		got, err := os.ReadFile(c.local)
		require.NoError(t, err)
		assert.Equal(t, "transcoded", string(got))
	})

	t.Run("a failed sync keeps the original", func(t *testing.T) {
		c := newSafetyCase(t, "sdr", "sdr")
		eio := errors.New("input/output error")
		syncPart = func(string) error { return eio }
		out := c.process(t)
		require.ErrorIs(t, out.Err, eio)
		assert.Equal(t, ExitRetriable, out.Code)
		assert.Equal(t, 1, c.eng.encodes)
		c.requireSourceUntouched(t)
	})
}

// Before it encodes, the worker -- holding this job's lease for this
// attempt -- removes the part files this job's earlier attempts left
// beside the output (an OOM kill, a lost node): nothing else. A later
// attempt's, another job's, another source's, the generic form, a
// non-part and a symlink all stay.
func TestAWorkerRemovesItsJobsEarlierAttemptsPartFilesBeforeItEncodes(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	stem := "Film.2020.2160p"
	stale := []string{stem + ".part-0123abcd-1.mkv", stem + ".part-0123abcd-0.mp4"}
	kept := []string{
		stem + ".part-0123abcd-3.mkv",    // a later attempt: not this worker's to judge
		stem + ".part-89abcdef-1.mkv",    // another job's, same output
		"Other.2021.part-0123abcd-1.mkv", // another source's
		stem + ".part.mkv",               // the generic form
		stem + ".part-two.mkv",           // a release name, not a part
		stem + ".nfo",
	}
	for _, name := range append(append([]string{}, stale...), kept...) {
		require.NoError(t, os.WriteFile(filepath.Join(c.dir, name), []byte(name), 0o644))
	}
	target := filepath.Join(t.TempDir(), "elsewhere.mkv")
	require.NoError(t, os.WriteFile(target, []byte("not ours"), 0o644))
	link := stem + ".part-0123abcd-0.mkv"
	require.NoError(t, os.Symlink(target, filepath.Join(c.dir, link)))
	kept = append(kept, link)

	out := c.process(t)
	require.NoError(t, out.Err)
	require.Equal(t, 1, c.eng.encodes)
	for _, name := range stale {
		assert.NotContains(t, c.eng.atEncode, name, "an earlier attempt's part is gone before the encode")
	}
	for _, name := range kept {
		assert.Contains(t, c.eng.atEncode, name)
		assert.Contains(t, listDir(c.dir), name)
	}
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "not ours", string(got), "a symlink is never followed")
}

// fsopsPart reads a part file's name as the sweep does.
func fsopsPart(name string) (fsops.TranscodePart, bool) { return fsops.ParseTranscodePart(name) }
