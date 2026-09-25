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

package rescan_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// hevcFixture is test/data/mediainfo's HEVC Main 10 sample, 320x240.
const hevcFixture = "../../../../test/data/mediainfo/sample_hevc_10bit.mkv"

// junkName is an obfuscated usenet download name: 32 hex digits, which
// pkg/release cannot parse as a movie.
const junkName = "2ef6f194995e4a11b055d0f2354ef0ba.mkv"

// requireFFmpeg skips unless ffmpeg and ffprobe are both on PATH:
// app/import/worker/fileimport's helper of the same name, copied rather
// than imported from a test package.
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
	out, err := exec.Command("ffmpeg", append(args, clip)...).CombinedOutput() //nolint:gosec // test-built args
	require.NoError(t, err, "ffmpeg: %s", out)
	return clip
}

// plantCopy writes clip's bytes to path.
func plantCopy(t *testing.T, clip, path string) {
	t.Helper()
	b, err := os.ReadFile(clip) //nolint:gosec // a test fixture path
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, b, 0o644)) //nolint:gosec // a test library file
}

// probedResolution is the resolution the real probe reads off clip: the
// expectation every test here builds from, never a hard-coded number.
func probedResolution(t *testing.T, ctx context.Context, clip string) int32 {
	t.Helper()
	mi, _, err := mediainfo.Probe(ctx, clip)
	require.NoError(t, err)
	res := mediainfo.ResolutionFromDimensions(mi.Width, mi.Height)
	require.NotEqual(t, commonv1.ResolutionUnknown, res, "the clip must probe to a resolution")
	return res
}

// clipWorker is NewWorker with the size floor off: a probeable clip is a
// few hundred KiB, under fsops.DefaultSampleMaxBytes, and that rule has its
// own test (sample_envtest_test.go).
func clipWorker(f *fixture) *rescan.Worker {
	w := rescan.NewWorker(f.c, f.bus)
	w.SampleMaxBytes = 0
	return w
}

// A junk-named file inside its movie's own folder is attributed by the
// folder, to the movie the catalogue already has: the folder's tmdb id
// names it. What the folder cannot say about the file -- its quality --
// comes from the probe, and nothing a folder name might coincidentally
// match is frozen as the file's release group.
func TestHandleAttributesAJunkNamedFileByItsItemFolder(t *testing.T) {
	requireFFmpeg(t)
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-folder-attach", catalogv1alpha1.RootFolderKindMovie,
		"hd-bluray-web", catalogv1alpha1.ScanModeFull)
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "a-scanner-darkly", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 3509, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, movie))
	waitCached(t, ctx, f.c, movie, func() bool { return true })

	res := probedResolution(t, ctx, hevcFixture)
	path := filepath.Join(f.root, "A Scanner Darkly (2006) {tmdb-3509}", junkName)
	plantCopy(t, hevcFixture, path)

	require.NoError(t, clipWorker(f).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	assert.Empty(t, got.Unmatched)
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Equal(t, int64(1), got.ItemsUpdated)
	assert.Zero(t, got.ItemsCreated)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}, mf.Spec.MediaRef)
	assert.Equal(t, path, mf.Spec.Path)
	assert.Equal(t, res, mf.Spec.Quality.Resolution, "the probe's resolution: the folder carries no quality")
	assert.Equal(t, commonv1.SourceUnknown, mf.Spec.Quality.Source, "the probe names no source")
	assert.Empty(t, mf.Spec.ReleaseGroup, "a folder name carries no release group")
	movieCountIs(t, ctx, f, 1)
}

// A junk-named file in a folder naming a movie the catalogue lacks, with
// no id, is unmatched: a folder is evidence of identity, and a title and
// year alone never create an item.
func TestHandleLeavesAJunkNamedFileInAnUnknownFolderUnmatched(t *testing.T) {
	requireEnvtest(t)
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-folder-unknown", catalogv1alpha1.RootFolderKindMovie,
		"hd-bluray-web", catalogv1alpha1.ScanModeFull)
	plantCopy(t, hevcFixture, filepath.Join(f.root, "Waking Life (2001)", junkName))

	before := noMatches(t)
	require.NoError(t, clipWorker(f).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	_, ok := unmatchedByPath(got)[filepath.Join("Waking Life (2001)", junkName)]
	require.True(t, ok, "reported unmatched: %+v", got.Unmatched)
	assert.InDelta(t, 1, noMatches(t)-before, 0, "unmatched as %s", rescan.CodeNoMatch)
	assert.Zero(t, got.FilesMatched)
	assert.Zero(t, got.ItemsCreated)
	movieCountIs(t, ctx, f, 0)
	noMediaFiles(t, ctx, f)
}

// A folder attributes a junk-named file to a movie the catalogue lacks
// only by the folder's own tmdb id. An imdb id resolved through the
// metadata gateway binds to an existing movie, but does not create one
// from a folder.
func TestHandleCreatesAMovieFromAFolderOnlyByItsTmdbID(t *testing.T) {
	for i, tc := range []struct {
		name, folder string
		created      bool
	}{
		{name: "the folder's tmdb id", folder: "A Scanner Darkly (2006) {tmdb-3509}", created: true},
		{name: "the folder's imdb id, resolved", folder: "A Scanner Darkly (2006) {imdb-tt0405296}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, ctx, fmt.Sprintf("rw-folder-create-%d", i), catalogv1alpha1.RootFolderKindMovie,
				"hd-bluray-web", catalogv1alpha1.ScanModeFull)
			require.NoError(t, f.bus.Serve(events.RPCMetadataResolve, "test",
				func(_ context.Context, data []byte) ([]byte, error) {
					var req schema.MetadataRequest
					require.NoError(t, schema.Decode("", data, &req))
					assert.Equal(t, "tt0405296", req.IDs["imdb"])
					_, out, err := schema.Encode(schema.MetadataResponse{Kind: req.Kind, IDs: map[string]string{"tmdb": "3509"}})
					return out, err
				}))
			// A sparse file: the decision is made before the probe, which
			// fails on it and leaves the name-derived quality.
			rel := filepath.Join(tc.folder, junkName)
			mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)

			before := noMatches(t)
			require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
			got := readProgress(t, ctx, f.bus, string(f.scan.UID))
			require.Empty(t, got.Error)
			if tc.created {
				assert.Empty(t, got.Unmatched)
				assert.Equal(t, int64(1), got.ItemsCreated)
				assert.Equal(t, int64(3509), movieCountIs(t, ctx, f, 1)[0].Spec.TmdbID)
				mediaFilesIn(t, ctx, f.c, f.ns, 1)
				return
			}
			_, ok := unmatchedByPath(got)[rel]
			require.True(t, ok, "reported unmatched: %+v", got.Unmatched)
			assert.InDelta(t, 1, noMatches(t)-before, 0, "unmatched as %s", rescan.CodeNoMatch)
			assert.Zero(t, got.ItemsCreated)
			movieCountIs(t, ctx, f, 0)
			noMediaFiles(t, ctx, f)
		})
	}
}

// A file whose name says 2160p and whose stream is 1080 lines freezes the
// quality its stream is: the probe corrects the resolution, onto the
// ladder, under the name's source.
func TestHandleCorrectsANamedQualityFromTheProbe(t *testing.T) {
	requireFFmpeg(t)
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-probe-quality", catalogv1alpha1.RootFolderKindMovie,
		"hd-bluray-web", catalogv1alpha1.ScanModeFull)
	clip := encodeClip(t, "clip.mkv", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=duration=0.2:size=1920x1080:rate=25", "-c:v", "mpeg4")
	res := probedResolution(t, ctx, clip)
	plantCopy(t, clip, filepath.Join(f.root, "The Matrix (1999) {tmdb-603}", "The.Matrix.1999.2160p.BluRay.x265-GRP.mkv"))

	require.NoError(t, clipWorker(f).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	assert.Empty(t, got.Unmatched)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, res, mf.Spec.Quality.Resolution, "the probe's resolution, not the name's 2160p")
	assert.Equal(t, commonv1.SourceBluray, mf.Spec.Quality.Source, "the probe names no source, so the name's stays")
	assert.Equal(t, fmt.Sprintf("Bluray-%dp", res), mf.Spec.Quality.Name)
	assert.Equal(t, "Bluray-1080p", mf.Spec.Quality.Name)
	assert.Equal(t, "GRP", mf.Spec.ReleaseGroup, "a name that parses keeps its own group")
}

// noMatches reads clustarr_import_unmatched_total for a movie root folder's
// [rescan.CodeNoMatch]: the reason code reaches only the metric, never the
// recorded entry, so a test that means the code reads it there. No test in
// this package runs in parallel, so a delta around one Handle is that
// Handle's. It reads the counter through client_model rather than
// prometheus/testutil, whose godebug dependency go.mod does not declare
// (app/indexer/worker/rss's counterValue says the same).
func noMatches(t *testing.T) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, metrics.ImportUnmatchedTotal.WithLabelValues(
		string(catalogv1alpha1.RootFolderKindMovie), rescan.CodeNoMatch).Write(&m))
	return m.GetCounter().GetValue()
}

// movieCountIs asserts the namespace holds want Movies, read past the
// cache -- the worker has returned, so the apiserver already holds every
// Movie it created, and a cache that has not caught up would read a
// creation as none -- and returns them.
func movieCountIs(t *testing.T, ctx context.Context, f *fixture, want int) []catalogv1alpha1.Movie {
	t.Helper()
	var movies catalogv1alpha1.MovieList
	require.NoError(t, f.api(t).List(ctx, &movies, client.InNamespace(f.ns)))
	require.Len(t, movies.Items, want)
	return movies.Items
}

// noMediaFiles asserts, past the cache, that the namespace holds no
// MediaFile.
func noMediaFiles(t *testing.T, ctx context.Context, f *fixture) {
	t.Helper()
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.api(t).List(ctx, &files, client.InNamespace(f.ns)))
	assert.Empty(t, files.Items)
}
