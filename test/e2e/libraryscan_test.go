//go:build e2e

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

package e2e

import (
	"context"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

const (
	// fixtureTmdbID is the movie testdata/metadata/tmdb records, and the one
	// the in-cluster TMDB stub can answer for. It is Inception, not Fight
	// Club: 27205 is Inception's TMDB id and pkg/metadata's own tests assert
	// that title.
	fixtureTmdbID = 27205

	// fixtureMovieFolder embeds the provider id in the form pkg/release/ids.go
	// actually recognises. "{tmdb-N}" and "[tmdbid-N]" are patterns; the
	// mixed "{tmdbid-N}" spelling is NOT, and a folder named that way parses
	// with no id at all.
	fixtureMovieFolder = "Inception (2010) {tmdb-27205}"

	// fixtureMovieFile is the feature file inside it.
	fixtureMovieFile = "Inception.2010.1080p.BluRay.x264-GROUP.mkv"
)

// TestLibraryRescan is Phase H scenario 7's discovery half: files planted in
// a RootFolder with no pre-existing Movie -- matchable, a sample and an
// extra -- then a LibraryScan. A rescan's whole point is discovering a
// library the catalog has never seen (amendment §A1.1's "upsert ... for each
// file found"), so nothing is created up front but the RootFolder itself.
func TestLibraryRescan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-scan-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	root := rf.Spec.Path

	// The one file that must become a MediaFile: real, probeable bytes over
	// pkg/fsops's 50 MiB sample threshold.
	plantMedia(t, hostPath(path.Join(root, fixtureMovieFolder, fixtureMovieFile)))
	// A sample and an extra beside it. Neither may become the movie's file:
	// fsops classifies the first by its filename and the second by its
	// parent directory, and the walk skips everything that is not ClassMedia.
	plantMedia(t, hostPath(path.Join(root, fixtureMovieFolder, "Sample", "Inception.2010.sample.mkv")))
	plantMedia(t, hostPath(path.Join(root, fixtureMovieFolder, "Extras", "Behind.The.Scenes.mkv")))

	scan := runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)
	require.EqualValues(t, 1, scan.Status.FilesMatched,
		"exactly the feature file should have been attributed; unmatched=%+v", scan.Status.Unmatched)
	require.GreaterOrEqual(t, scan.Status.FilesSkipped, int64(2),
		"the sample and the extra must be skipped, not matched")

	// Exactly one MediaFile under this root, and it is the feature.
	files := waitForMediaFileCount(ctx, t, root, 1)
	mf := files[0]
	require.Equal(t, path.Join(root, fixtureMovieFolder, fixtureMovieFile), mf.Spec.Path)
	require.Equal(t, commonv1.MediaKindMovie, mf.Spec.MediaRef.Kind)
	require.Equal(t, "Bluray-1080p", mf.Spec.Quality.Name,
		"the quality frozen at import comes from pkg/release, not from a probe")
	require.NotZero(t, mf.Spec.SizeBytes)

	// The Movie the scan upserted, found through the file rather than by
	// listing on spec.tmdbID: two scenarios planting the same provider id
	// share one Movie object, and a namespace-wide lookup would be ambiguous.
	movie := requireMovie(ctx, t, mf.Spec.MediaRef.Name)
	require.EqualValues(t, fixtureTmdbID, movie.Spec.TmdbID)
	require.Equal(t, QualityProfileName, movie.Spec.QualityProfileRef,
		"a scanned Movie takes the root folder's default profile")
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), &movie) })

	// catalogarr's half: the MediaFile controller probed the real bytes with
	// real ffprobe, and the Movie reconciler rolled the file up.
	waitForMediaFileProbed(ctx, t, client.ObjectKeyFromObject(&mf))
	waitFor(t, ctx, 3*time.Minute, "Movie "+movie.Name+" status.hasFile", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Movie
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(&movie), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.HasFile && live.Status.FileRef != nil, nil
	})
}

// TestLibraryRescanUnmatchedAndSchedule is scenario 7's other half: what
// happens to files the scanner cannot attribute, and the RootFolder schedule
// firing a scan on its own. It is a separate Test so a failure in one half
// does not hide the other's planted-file state in the same cleanup stack.
func TestLibraryRescanUnmatchedAndSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-scan2-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	root := rf.Spec.Path

	// Two shapes of unattributable file, because importarr refuses them for
	// two different reasons and both must be reported rather than guessed:
	// one pkg/release cannot parse at all, and one that parses cleanly but
	// carries no provider id and matches no existing Movie.
	plantFiller(t, hostPath(path.Join(root, "Unsorted", "IMG_0002.mkv")))
	plantFiller(t, hostPath(path.Join(root, "Unsorted", "Some.Unknown.Fixture.Film.2019.1080p.WEB.x264-NOBODY.mkv")))

	scan := runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	byBase := map[string]catalogv1alpha1.UnmatchedFile{}
	for _, u := range scan.Status.Unmatched {
		byBase[path.Base(u.Path)] = u
	}
	for _, want := range []string{"IMG_0002.mkv", "Some.Unknown.Fixture.Film.2019.1080p.WEB.x264-NOBODY.mkv"} {
		u, ok := byBase[want]
		require.True(t, ok, "%q must be recorded in status.unmatched, never turned into a speculative item (have %+v)", want, scan.Status.Unmatched)
		require.NotEmpty(t, u.Reason, "%q was recorded without a reason", want)
	}
	require.Zero(t, scan.Status.ItemsCreated, "the scanner never guesses: no Movie may be invented for an unattributable file")
	require.Empty(t, mediaFilesUnder(ctx, t, root), "no MediaFile may exist for an unattributable file")

	// A schedule tick creates a second LibraryScan by itself. The generated
	// name is not this suite's to predict, so list and filter on
	// spec.rootFolderRef. */1 * * * * is the fastest a five-field cron can
	// fire, so the window is a minute plus the controller's own requeue.
	require.NoError(t, patchScanSchedule(ctx, rf, "*/1 * * * *"))

	var second catalogv1alpha1.LibraryScan
	waitFor(t, ctx, 4*time.Minute, "a second, schedule-triggered LibraryScan", func(ctx context.Context) (bool, error) {
		var list catalogv1alpha1.LibraryScanList
		if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace)); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		for _, s := range list.Items {
			if s.Spec.RootFolderRef == rf.Name && s.Name != scan.Name {
				second = s
				return true, nil
			}
		}
		return false, nil
	})
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), &second) })

	// Stop the cron before waiting, so a slow scan does not race a third
	// tick into the same assertion.
	require.NoError(t, patchScanSchedule(ctx, rf, ""))

	finished := waitForScanCompleted(ctx, t, client.ObjectKeyFromObject(&second))
	require.Equal(t, catalogv1alpha1.ScanModeIncremental, second.Spec.Mode,
		"a scheduled scan is incremental (importarr/controller/rootfolderschedule)")
	require.GreaterOrEqual(t, finished.Status.FilesSeen, int64(2),
		"the schedule-triggered scan must have walked the tree again")
}

// patchScanSchedule sets or clears RootFolder.spec.scanSchedule. It is a spec
// write, which is exactly what a user or the UI would do; nothing in this
// suite ever writes a status.
func patchScanSchedule(ctx context.Context, rf *catalogv1alpha1.RootFolder, schedule string) error {
	var live catalogv1alpha1.RootFolder
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rf), &live); err != nil {
		return err
	}
	patch := client.MergeFrom(live.DeepCopy())
	live.Spec.ScanSchedule = schedule
	return k8sClient.Patch(ctx, &live, patch)
}

// waitForMediaFileCount waits until exactly want MediaFiles exist under
// clusterPath and returns them. A count assertion needs a wait, not a bare
// List: the scan reports Completed as soon as the walk finishes, and the
// apiserver's own cache may still be catching up.
func waitForMediaFileCount(ctx context.Context, t *testing.T, clusterPath string, want int) []catalogv1alpha1.MediaFile {
	t.Helper()
	var got []catalogv1alpha1.MediaFile
	waitFor(t, ctx, 3*time.Minute, "exactly one MediaFile under "+clusterPath, func(ctx context.Context) (bool, error) {
		got = mediaFilesUnder(ctx, t, clusterPath)
		return len(got) == want, nil
	})
	for _, mf := range got {
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), mf.DeepCopy()) })
	}
	return got
}

// waitForMediaFileProbed waits until catalogarr's MediaFile controller has
// probed the file with real ffprobe and mirrored the result onto status.
func waitForMediaFileProbed(ctx context.Context, t *testing.T, key client.ObjectKey) catalogv1alpha1.MediaFile {
	t.Helper()
	var live catalogv1alpha1.MediaFile
	waitFor(t, ctx, 3*time.Minute, "MediaFile "+key.Name+" probed", func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.MediaInfo != nil && live.Status.ProbeHash != "", nil
	})
	require.Equal(t, "h264", live.Status.MediaInfo.VideoCodec, "ffprobe read the seeded clip's real video stream")
	require.EqualValues(t, 640, live.Status.MediaInfo.Width)
	require.True(t, isConditionTrue(live.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed),
		"MediaFile %s has a probe result but no Probed condition", key.Name)
	return live
}

// requireMovie fetches one Movie by name, failing the test when it is absent.
func requireMovie(ctx context.Context, t *testing.T, name string) catalogv1alpha1.Movie {
	t.Helper()
	var m catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: name}, &m),
		"the scan attributed a file to Movie %q but the object does not exist", name)
	return m
}
