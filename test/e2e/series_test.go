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
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestSeriesAndEpisodes is Phase H scenario 5's catalog leg: three Series,
// one per numbering scheme M1 has to handle, driven through the real
// catalogarr Series controller, the real metadata gateway and the real NATS
// RPC, against the in-cluster TVDB stub.
//
//   - standard: TVDB 121361 (Game of Thrones), the recorded fixture, two
//     season/episode-numbered episodes;
//   - daily:    TVDB 900001, a fixture-owned series whose Episode objects
//     are named by air date rather than by number;
//   - anime:    TVDB 900002, a fixture-owned series the controller forces
//     onto absolute ordering (EffectiveEpisodeOrder, §4.2).
//
// The file leg of scenario 5 -- each Episode gaining a MediaFile -- is
// TestSeriesRootFolderScanAttributesEpisodes below, through importarr's
// series rescan (task X7a).
// fixtureEpisodesPerSeries is how many episodes each of the three fixture
// series carries in test/fixtures/tvdbstub/testdata. Both the recorded GoT
// list and the two fixture-owned lists hold two, which is deliberate: a
// single-episode list could not tell a completed fan-out apart from a
// half-finished one.
const fixtureEpisodesPerSeries = 2

func TestSeriesAndEpisodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-series-rf", catalogv1alpha1.RootFolderKindSeries, "tv")

	standard := createSeries(ctx, t, rf.Name, 121361, catalogv1alpha1.SeriesTypeStandard)
	daily := createSeries(ctx, t, rf.Name, 900001, catalogv1alpha1.SeriesTypeDaily)
	anime := createSeries(ctx, t, rf.Name, 900002, catalogv1alpha1.SeriesTypeAnime)

	standardLive := waitForSeriesReady(ctx, t, standard, fixtureEpisodesPerSeries)
	dailyLive := waitForSeriesReady(ctx, t, daily, fixtureEpisodesPerSeries)
	animeLive := waitForSeriesReady(ctx, t, anime, fixtureEpisodesPerSeries)

	// The naming dialect is pkg/naming's business, not this suite's, so the
	// assertion is structural: the resolved folder sits under the root
	// folder and is not the root folder itself.
	for _, s := range []catalogv1alpha1.Series{standardLive, dailyLive, animeLive} {
		require.True(t, strings.HasPrefix(s.Status.Path, rf.Spec.Path+"/"),
			"Series %s resolved status.path %q outside its root folder %q", s.Name, s.Status.Path, rf.Spec.Path)
		require.NotEqual(t, rf.Spec.Path, s.Status.Path)
	}

	// Standard numbering: two season/episode-numbered episodes straight from
	// the recorded TVDB fixture.
	got := requireEpisode(ctx, t, standard.Name+"-s01e01")
	require.Equal(t, int32(1), got.Spec.SeasonNumber)
	require.Equal(t, int32(1), got.Spec.EpisodeNumber)
	require.Equal(t, "Winter Is Coming", got.Status.Title)
	require.NotNil(t, got.Status.AirDate, "the recorded fixture carries aired=2011-04-17")
	require.Equal(t, "2011-04-17", got.Status.AirDate.UTC().Format("2006-01-02"))

	got = requireEpisode(ctx, t, standard.Name+"-s01e02")
	require.Equal(t, int32(2), got.Spec.EpisodeNumber)
	require.Equal(t, "The Kingsroad", got.Status.Title)

	// Daily numbering: the Episode object is named by air date (§4.2's
	// EpisodeName), which is the whole point of the daily series type.
	got = requireEpisode(ctx, t, daily.Name+"-2024-01-15")
	require.Equal(t, int32(1), got.Spec.SeasonNumber)
	require.Equal(t, int32(1), got.Spec.EpisodeNumber)
	require.Equal(t, "Episode for 2024-01-15", got.Status.Title)

	// Anime absolute numbering: the object is still named by season/episode,
	// and the absolute number lands in status where pkg/naming's {absolute}
	// token reads it (docs/research/naming.md:72).
	got = requireEpisode(ctx, t, anime.Name+"-s01e05")
	require.Equal(t, "Fixture Episode Five", got.Status.Title)
	require.NotNil(t, got.Status.AbsoluteNumber, "an anime Episode must carry status.absoluteNumber")
	require.Equal(t, int32(5), *got.Status.AbsoluteNumber)

	// status.episodeCount is asserted inside waitForSeriesReady's predicate,
	// not here: see that function's comment for why a snapshot assertion on
	// it races the Series reconciler's pre-fan-out rollup.
}

// TestSeriesRootFolderScanAttributesEpisodes is scenario 5's file leg through
// importarr's library rescan. Until task X7a the rescan refused every file
// under a series root folder as "not supported by library rescan" (this test
// was TestSeriesRootFolderScanIsNotSupportedYet and pinned that); it now
// attributes each file to an EXISTING Series and Episode (the Series by its
// resolved folder, the Episode by the numbering the file's name carries --
// app/import/worker/rescan/series.go) and creates a MediaFile for it.
//
// The never-guess invariant (CLAUDE.md) is the other half: a file naming an
// episode the series does not have is reported with a reason and nothing is
// invented for it -- no MediaFile, no Episode.
func TestSeriesRootFolderScanAttributesEpisodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-serscan-rf", catalogv1alpha1.RootFolderKindSeries, "tv")
	series := createSeries(ctx, t, rf.Name, 121361, catalogv1alpha1.SeriesTypeStandard)
	live := waitForSeriesReady(ctx, t, series, fixtureEpisodesPerSeries)

	// A real season pack unpacks into one file per episode; MediaFile is
	// "one file on disk", so a pack is two files, not one. The third names
	// an episode the fixture series does not have.
	packDir := path.Join(live.Status.Path, "Season 01")
	plantFiller(t, hostPath(path.Join(packDir, "Game.of.Thrones.S01E01.720p.BluRay.x264-DEMAND.mkv")))
	plantFiller(t, hostPath(path.Join(packDir, "Game.of.Thrones.S01E02.720p.BluRay.x264-DEMAND.mkv")))
	plantFiller(t, hostPath(path.Join(live.Status.Path, "Season 09", "Game.of.Thrones.S09E99.720p.BluRay.x264-DEMAND.mkv")))

	scan := runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)
	require.EqualValues(t, 2, scan.Status.FilesMatched,
		"both episode files should have been attributed; unmatched=%+v", scan.Status.Unmatched)

	// The never-guess half: the unknown episode is reported, with a reason.
	require.Len(t, scan.Status.Unmatched, 1, "unmatched=%+v", scan.Status.Unmatched)
	require.Equal(t, "Game.of.Thrones.S09E99.720p.BluRay.x264-DEMAND.mkv", path.Base(scan.Status.Unmatched[0].Path))
	require.NotEmpty(t, scan.Status.Unmatched[0].Reason, "an unmatched file must carry its reason")

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 2)
	byEpisode := map[string]string{}
	for _, mf := range files {
		require.Equal(t, commonv1.MediaKindEpisode, mf.Spec.MediaRef.Kind, "MediaFile %s", mf.Name)
		byEpisode[mf.Spec.MediaRef.Name] = path.Base(mf.Spec.Path)
	}
	require.Equal(t, map[string]string{
		series.Name + "-s01e01": "Game.of.Thrones.S01E01.720p.BluRay.x264-DEMAND.mkv",
		series.Name + "-s01e02": "Game.of.Thrones.S01E02.720p.BluRay.x264-DEMAND.mkv",
	}, byEpisode, "each file must back the Episode its name numbers")
}

// createSeries creates one Series and registers its cleanup. Deleting the
// Series is enough for the Episodes too: the Series reconciler owns them.
func createSeries(ctx context.Context, t *testing.T, rootFolder string, tvdbID int64, seriesType catalogv1alpha1.SeriesType) *catalogv1alpha1.Series {
	t.Helper()
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-series"), Namespace: Namespace},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID:            tvdbID,
			SeriesType:        seriesType,
			QualityProfileRef: QualityProfileName,
			RootFolderRef:     rootFolder,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, s))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), s) })
	return s
}

// waitForSeriesReady polls until the Series has its metadata, has fanned its
// episodes out, has resolved a path AND has rolled wantEpisodes up into
// status.episodeCount, then returns that live object. Every one of those is a
// separate field the reconciler settles independently, so anything asserted
// on the returned snapshot must first be IN this predicate.
//
// episodeCount in particular is not free-riding on Ready.
// app/catalog/controller/series/reconciler.go computes the rollup from the
// episode list it read BEFORE fanning out -- its own comment says the count
// is "completed by the very next reconcile" -- so the reconcile that flips
// Ready can legitimately carry episodeCount 0. It usually does not, because
// the path block is gated on status.metadata and the metadata refresh
// normally lands after the first fan-out; but if the episode-listing RPC
// fails once and retries on episodeSyncRPCBackoff while the metadata refresh
// lands first, the Ready reconcile carries a stale zero. Asserting it on the
// snapshot instead of waiting for it is a real race, not a theoretical one.
func waitForSeriesReady(ctx context.Context, t *testing.T, s *catalogv1alpha1.Series, wantEpisodes int32) catalogv1alpha1.Series {
	t.Helper()
	var live catalogv1alpha1.Series
	desc := fmt.Sprintf("Series %s Ready with a resolved path and episodeCount=%d", s.Name, wantEpisodes)
	waitFor(t, ctx, 5*time.Minute, desc, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.Path != "" &&
			live.Status.EpisodeCount == wantEpisodes &&
			isConditionTrue(live.Status.Conditions, catalogv1alpha1.SeriesConditionMetadataReady) &&
			isConditionTrue(live.Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced) &&
			live.Status.Phase == catalogv1alpha1.SeriesPhaseReady, nil
	}, describeSeries(client.ObjectKeyFromObject(s)))
	return live
}

// requireEpisode waits for one Episode by its deterministic object name
// (series.EpisodeName, §4.2) and returns it. Naming it rather than searching
// for it is deliberate: the name encodes the numbering scheme under test.
func requireEpisode(ctx context.Context, t *testing.T, name string) catalogv1alpha1.Episode {
	t.Helper()
	var ep catalogv1alpha1.Episode
	waitFor(t, ctx, 3*time.Minute, "Episode "+name, func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: name}, &ep); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		// The Series reconciler creates the object first and applies the
		// provider fields under a second field manager, so an Episode with
		// no title yet is half-written, not finished.
		return ep.Status.Title != "", nil
	})
	return ep
}

// describeSeries renders one Series' phase, path and conditions for a
// failure message. A Series that never becomes Ready has usually stalled on
// one specific condition, and naming it is the difference between a
// diagnosis and a shrug.
func describeSeries(key client.ObjectKey) func() string {
	return func() string {
		var live catalogv1alpha1.Series
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("Series %s could not be read back: %v", key.Name, err)
		}
		title := "<no status.metadata>"
		if live.Status.Metadata != nil {
			title = live.Status.Metadata.Title
		}
		out := fmt.Sprintf("Series %s phase=%q path=%q metadata.title=%q episodeCount=%d",
			key.Name, live.Status.Phase, live.Status.Path, title, live.Status.EpisodeCount)
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}
