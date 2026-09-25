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
	"sync/atomic"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
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

// serveImdbResolve answers the metadata gateway's resolve RPC with tmdbID
// for any imdb id, counting the calls. The handler runs on membus's own
// goroutine, so it asserts and returns its error rather than stopping the
// test from there.
func serveImdbResolve(t *testing.T, f *fixture, tmdbID string) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	require.NoError(t, f.bus.Serve(events.RPCMetadataResolve, "test",
		func(_ context.Context, data []byte) ([]byte, error) {
			calls.Add(1)
			var req schema.MetadataRequest
			if err := schema.Decode("", data, &req); !assert.NoError(t, err) {
				return nil, err
			}
			_, out, err := schema.Encode(schema.MetadataResponse{Kind: req.Kind, IDs: map[string]string{"tmdb": tmdbID}})
			assert.NoError(t, err)
			return out, err
		}))
	return &calls
}

// A junk-named file's item folder carrying an id attributes the file to a
// movie the catalogue lacks exactly as the same id in a filename would
// (ruling R15): a tmdb id is the identity, and an imdb id is resolved
// through the metadata gateway.
func TestHandleCreatesAMovieFromItsFoldersOwnID(t *testing.T) {
	for i, tc := range []struct {
		name, folder string
		resolves     bool
	}{
		{name: "the folder's tmdb id", folder: "A Scanner Darkly (2006) {tmdb-3509}"},
		{name: "the folder's imdb id, resolved", folder: "A Scanner Darkly (2006) {imdb-tt0405296}", resolves: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, ctx, fmt.Sprintf("rw-folder-create-%d", i), catalogv1alpha1.RootFolderKindMovie,
				"hd-bluray-web", catalogv1alpha1.ScanModeFull)
			calls := serveImdbResolve(t, f, "3509")
			// A sparse file: the decision is made before the probe, which
			// fails on it and leaves the name-derived quality.
			mustWriteFile(t, filepath.Join(f.root, tc.folder, junkName), sampleFloor)

			require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
			got := readProgress(t, ctx, f.bus, string(f.scan.UID))
			require.Empty(t, got.Error)
			assert.Empty(t, got.Unmatched)
			assert.Equal(t, int64(1), got.ItemsCreated)
			assert.Equal(t, tc.resolves, calls.Load() > 0, "the gateway resolves an imdb id, and only that")
			movie := movieCountIs(t, ctx, f, 1)[0]
			assert.Equal(t, int64(3509), movie.Spec.TmdbID)
			mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
			assert.Equal(t, movie.Name, mf.Spec.MediaRef.Name)
		})
	}
}

// A junk-named file is attributed by its item folder alone (ruling R14):
// an id on a further ancestor -- a collection folder's, which is no
// movie's -- neither creates a movie nor attaches the file to one that
// carries it. Without an id of its own the item folder matches by its
// title and year among existing movies, or the file is unmatched.
func TestHandleNeverReadsAGrandparentsIDForAJunkNamedFile(t *testing.T) {
	rel := filepath.Join("Alien Collection {tmdb-8091}", "Alien (1979)", junkName)
	t.Run("no movie of the folder's title: unmatched, nothing created", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t, ctx, "rw-folder-grandparent-0", catalogv1alpha1.RootFolderKindMovie,
			"hd-bluray-web", catalogv1alpha1.ScanModeFull)
		mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)

		before := noMatches(t)
		require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
		got := readProgress(t, ctx, f.bus, string(f.scan.UID))
		require.Empty(t, got.Error)
		_, ok := unmatchedByPath(got)[rel]
		require.True(t, ok, "reported unmatched: %+v", got.Unmatched)
		assert.InDelta(t, 1, noMatches(t)-before, 0, "unmatched as %s", rescan.CodeNoMatch)
		assert.Zero(t, got.ItemsCreated)
		movieCountIs(t, ctx, f, 0)
		noMediaFiles(t, ctx, f)
	})
	t.Run("a movie carries the collection's id: attached by title and year, never by that id", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t, ctx, "rw-folder-grandparent-1", catalogv1alpha1.RootFolderKindMovie,
			"hd-bluray-web", catalogv1alpha1.ScanModeFull)
		byID := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "collection-8091", Namespace: f.ns},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 8091, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
		}
		require.NoError(t, f.c.Create(ctx, byID))
		alien := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "alien-348", Namespace: f.ns},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 348, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
		}
		require.NoError(t, f.c.Create(ctx, alien))
		_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Movie(alien.Name, f.ns).WithStatus(
			catalogac.MovieStatus().WithMetadata(catalogac.MovieMetadata().WithTitle("Alien").WithYear(1979))))
		require.NoError(t, err)
		waitCached(t, ctx, f.c, byID, func() bool { return true })
		waitCached(t, ctx, f.c, alien, func() bool { return alien.Status.Metadata != nil })
		mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)

		require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
		got := readProgress(t, ctx, f.bus, string(f.scan.UID))
		require.Empty(t, got.Error)
		assert.Empty(t, got.Unmatched)
		assert.Zero(t, got.ItemsCreated)
		mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
		assert.Equal(t, alien.Name, mf.Spec.MediaRef.Name, "the item folder's title and year, not the collection's id")
		movieCountIs(t, ctx, f, 2)
	})
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

// The walk extends the delivery's ack deadline immediately before it
// probes a file (ruling R17), so the probe's whole bound lies inside the
// deadline however long the walk has gone since its last interval beat.
// The walk's own beat on its first file is the one before; the probe must
// see a second.
func TestHandleHeartbeatsImmediatelyBeforeAProbe(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-probe-beat", catalogv1alpha1.RootFolderKindMovie,
		"hd-bluray-web", catalogv1alpha1.ScanModeFull)
	mustWriteFile(t, filepath.Join(f.root, "Heat (1995) {tmdb-949}", "Heat.1995.1080p.BluRay.x264-GRP.mkv"), sampleFloor)

	msg := newFakeMessage(t, f.task(false))
	var atProbe []int64
	w := rescan.NewWorker(f.c, f.bus)
	w.ProbeVideo = func(context.Context, string) (*commonv1.MediaInfo, error) {
		atProbe = append(atProbe, msg.heartbeats.Load())
		return &commonv1.MediaInfo{Width: 1920, Height: 1080}, nil
	}
	require.NoError(t, w.Handle(ctx, msg))
	require.Empty(t, readProgress(t, ctx, f.bus, string(f.scan.UID)).Error)
	assert.Equal(t, []int64{2}, atProbe, "one probe, after the walk's beat on the file and one of its own")
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
