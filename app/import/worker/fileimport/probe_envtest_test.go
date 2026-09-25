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

package fileimport_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// hdr10ClipArgs encodes a short 1920x1080 HEVC Main 10 clip carrying
// HDR10's colour tags and mastering-display metadata: pkg/mediainfo's
// hdr10FixtureArgs recipe at 1080 lines. test/data/mediainfo's HEVC sample
// is 320x240, which the probe corrects to 360p -- a resolution no quality
// definition holds, so every profile rejects it and the import renders no
// name to check.
var hdr10ClipArgs = []string{
	"-hide_banner", "-loglevel", "error", "-y",
	"-f", "lavfi", "-i", "testsrc2=duration=0.2:size=1920x1080:rate=25",
	"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
	"-x265-params", "log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:" +
		"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
}

// requireLibx265 skips unless ffmpeg with libx265 and ffprobe are both
// on PATH: pkg/transcode's helper of the same name, copied rather than
// imported from a test package.
func requireLibx265(t *testing.T) {
	t.Helper()
	requireFFmpeg(t)
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil || !strings.Contains(string(out), "libx265") {
		t.Skip("this ffmpeg has no libx265")
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

// encodeClip runs ffmpeg with args and the output file name, in a fresh
// temp directory, and returns the clip's path.
func encodeClip(t *testing.T, name string, args ...string) string {
	t.Helper()
	clip := filepath.Join(t.TempDir(), name)
	out, err := exec.Command("ffmpeg", append(args, clip)...).CombinedOutput()
	require.NoError(t, err, "ffmpeg: %s", out)
	return clip
}

// copyOf plants clip's bytes at the path importPlanted hands it.
func copyOf(t *testing.T, clip string) func(path string) {
	return func(path string) {
		b, err := os.ReadFile(clip)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, b, 0o644))
	}
}

// TestAnImportIsQualifiedAndNamedFromItsProbe: the file's name says 2160p
// x264, its stream is 1080 lines of 10-bit HEVC in HDR10. The import
// probes it, so the profile (which holds Bluray-1080p and no 2160p) admits
// it, spec.quality freezes the probe's resolution under the name's
// source, and the library name says what the file is -- [h265], since the
// release title names no x265 encode, and [HDR10], which the name never
// said at all.
func TestAnImportIsQualifiedAndNamedFromItsProbe(t *testing.T) {
	requireLibx265(t)
	ctx := context.Background()
	f := newFixture(t, "fi-probe-name")
	// The clip is a few hundred KiB, under the sample floor: that rule has
	// its own test (sample_envtest_test.go).
	f.worker.SampleMaxBytes = 0

	clip := encodeClip(t, "clip.mkv", hdr10ClipArgs...)
	mi, _, err := mediainfo.Probe(ctx, clip)
	require.NoError(t, err)
	res := mediainfo.ResolutionFromDimensions(mi.Width, mi.Height)
	require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr, "the clip must be HDR10, or its name's dynamic-range block proves nothing")

	const fileName = "The.Matrix.1999.2160p.BluRay.x264-SPARKS.mkv"
	mf := f.importPlanted(t, "probe-dl", fileName, copyOf(t, clip))

	assert.Equal(t, res, mf.Spec.Quality.Resolution, "the probe's resolution, not the name's 2160p")
	assert.Equal(t, commonv1.SourceBluray, mf.Spec.Quality.Source, "the probe names no source, so the name's stays")

	base := filepath.Base(mf.Spec.Path)
	assert.Contains(t, base, fmt.Sprintf("[Bluray-%dp]", res))
	assert.Contains(t, base, "[HDR10]")
	assert.Contains(t, base, "[h265]", "an HEVC stream whose release title says x264 is h265")
	for _, stale := range []string{"2160p", "x264", "x265"} {
		assert.NotContains(t, base, stale)
	}

	// And the whole path is the one the shared renderer gives the frozen
	// spec and the probe, so a later rename of this file renders it again.
	var movie catalogv1alpha1.Movie
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &movie))
	nctx, ok := catalogctx.Movie(&movie)
	require.True(t, ok)
	want, err := catalogctx.MovieFilePath(f.rootFolder, &movie, catalogctx.File(nctx, &mf.Spec, mi), catalogctx.ContainerExt(mi, fileName))
	require.NoError(t, err)
	assert.Equal(t, want, mf.Spec.Path)
}

// TestAProbedDVDRipIsStillDVD is ruling R10 through the import: a DVD rip
// names no resolution, and DVD is the one quality its source defines
// without one, so the probe's 576 lines must not move it off DVD onto a
// triple no profile tier holds. Before the fallback it froze as "Unknown"
// and every profile rejected it.
func TestAProbedDVDRipIsStillDVD(t *testing.T) {
	requireFFmpeg(t)
	ctx := context.Background()
	f := newFixture(t, "fi-probe-dvd")
	f.worker.SampleMaxBytes = 0
	var qp catalogv1alpha1.QualityProfile
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(f.profile), &qp))
	qp.Spec.Tiers = append(qp.Spec.Tiers, catalogv1alpha1.Tier{Name: "sd", Qualities: []string{"DVD"}})
	require.NoError(t, f.c.Update(ctx, &qp))
	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.QualityProfile
		return f.c.Get(ctx, client.ObjectKeyFromObject(&qp), &got) == nil && len(got.Spec.Tiers) == 2
	})

	clip := encodeClip(t, "clip.avi", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=duration=0.2:size=720x576:rate=25", "-c:v", "mpeg4", "-vtag", "XVID")
	mi, _, err := mediainfo.Probe(ctx, clip)
	require.NoError(t, err)
	require.Equal(t, commonv1.Resolution576p, mediainfo.ResolutionFromDimensions(mi.Width, mi.Height))

	mf := f.importPlanted(t, "dvd-dl", "The.Matrix.1999.DVDRip.XviD-GRP.avi", copyOf(t, clip))

	assert.Equal(t, "DVD", mf.Spec.Quality.Name)
	assert.Equal(t, commonv1.SourceDVD, mf.Spec.Quality.Source)
	base := filepath.Base(mf.Spec.Path)
	assert.Contains(t, base, "[DVD]")
	assert.Contains(t, base, "[XviD]")
	assert.NotContains(t, base, "576p")
	assert.Equal(t, ".avi", filepath.Ext(base))
}

// TestAnImportHeartbeatsImmediatelyBeforeItsProbe is ruling R17: the
// import extends the delivery's ack deadline immediately before it probes
// a file, so the probe's whole bound lies inside the deadline however long
// the file loop has gone since its last interval beat. The loop's own beat
// on the walk's first file is one; the probe's must be a second, since no
// interval beat falls due in between. The file is sparse, so the probe
// fails and the file imports under its name -- after the beat all the same.
func TestAnImportHeartbeatsImmediatelyBeforeItsProbe(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-probe-beat")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	dl := f.createDownload(t, "beat-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})

	msg := newImportTaskMessage(t, f.ns, dl.Name, "")
	require.NoError(t, f.worker.Handle(ctx, msg))
	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: dl.Name}, &got))
	require.NotNil(t, got.Status.Import)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, "message: %s", got.Status.Import.Message)
	assert.Equal(t, int64(2), msg.heartbeats.Load(), "the loop's beat on the first file, and one immediately before the probe")
}
