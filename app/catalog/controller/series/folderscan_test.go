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

package series_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/folderscan"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// countingFolders answers HoldsFiles for one folder and counts the calls.
type countingFolders struct {
	folder string
	holds  bool
	calls  int
}

func (f *countingFolders) HoldsFiles(_ context.Context, path string) (bool, error) {
	f.calls++
	return f.holds && path == f.folder, nil
}

const scannedFolder = "Breaking Bad (2008) [tvdbid-81189]"

// scannedSeries is a Series as a library scan creates it (pinned to the
// folder whose TheTVDB id named it, monitoring nothing), once the metadata
// gateway has answered and before any Episode exists.
func scannedSeries(ns string, status catalogv1alpha1.SeriesStatus) *catalogv1alpha1.Series {
	now := time.Now().UTC()
	status.ObservedGeneration, status.AddOptionsApplied = 1, true
	status.Metadata = &catalogv1alpha1.SeriesMetadata{
		Title: "Breaking Bad", Year: 2008, RefreshedAt: metav1.NewTime(now),
		Status: catalogv1alpha1.SeriesRunStatusEnded, SchemaVersion: metadata.SchemaVersion,
	}
	return &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "breaking-bad-81189", UID: "series-uid", Generation: 1},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 81189, QualityProfileRef: "hd", RootFolderRef: "tv", Folder: ptr.To(scannedFolder),
			SeriesType: catalogv1alpha1.SeriesTypeStandard, MonitorNewItems: catalogv1alpha1.MonitorNewChildrenAll,
		},
		Status: status,
	}
}

func folderScanFixture(t *testing.T, s *catalogv1alpha1.Series, objs ...client.Object) (client.Client, *series.Reconciler, *countingFolders) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.Series{}, &catalogv1alpha1.Episode{}).
		WithIndex(&catalogv1alpha1.Episode{}, ".spec.seriesRef", func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).
		WithObjects(append([]client.Object{s, testRootFolder(s.Namespace, "tv", "/data/media/tv")}, objs...)...).
		Build()
	air := time.Now().Add(-48 * time.Hour)
	rpc := fakeEpisodeRPC{episodes: []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: &air},
		{SeasonNumber: 1, EpisodeNumber: 2, AirDate: &air},
	}}
	folders := &countingFolders{folder: "/data/media/tv/" + scannedFolder, holds: true}
	r := &series.Reconciler{Client: c, Scheme: c.Scheme(), Bus: combinedBus{Publisher: &subjectRecorder{}, requester: rpc},
		Folders: folders}
	return c, r, folders
}

func libraryScans(t *testing.T, c client.Client, ns string) []catalogv1alpha1.LibraryScan {
	t.Helper()
	var list catalogv1alpha1.LibraryScanList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace(ns)))
	return list.Items
}

// TestASeriesScansItsFolderOnceItsFirstEpisodesSettle is smoke finding O8:
// on a library with no items, the first scan created 131 Series and matched
// 433 of 11,122 files -- a new series has no Episodes until its reconciler
// fans them out, so the scan counted its files as awaiting episodes, and they
// were attributed only by a second, manual scan. Now the pass that first
// lists a series' episodes owes its folder a scan, and once they have
// settled into the import agent's cache the series asks for one LibraryScan
// of its own folder, recorded in status.conditions so it never repeats.
func TestASeriesScansItsFolderOnceItsFirstEpisodesSettle(t *testing.T) {
	ctx := context.Background()
	const ns = "tv"
	c, r, folders := folderScanFixture(t, scannedSeries(ns, catalogv1alpha1.SeriesStatus{}))
	nn := types.NamespacedName{Namespace: ns, Name: "breaking-bad-81189"}
	get := func() *catalogv1alpha1.Series {
		var s catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, nn, &s))
		return &s
	}

	// The fan-out pass: the episodes exist, the scan is owed and settling.
	res, err := r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	s := get()
	require.Len(t, s.Status.Seasons, 1, "the episodes were fanned out")
	cond := k8s.FindCondition(s.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned)
	require.NotNil(t, cond, "the first episodes record the folder scan they owe")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Positive(t, res.RequeueAfter, "the series comes back for its scan")
	assert.LessOrEqual(t, res.RequeueAfter, folderscan.SettleAfter)
	assert.Empty(t, libraryScans(t, c, ns), "no scan before the new Episodes have settled")

	// The settle is up (cond points into s.Status.Conditions): one scan of
	// the series' own folder.
	cond.LastTransitionTime = metav1.NewTime(time.Now().Add(-time.Minute))
	require.NoError(t, c.Status().Update(ctx, s))
	_, err = r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	scans := libraryScans(t, c, ns)
	require.Len(t, scans, 1)
	assert.Equal(t, catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: "tv", Subpath: scannedFolder, Mode: catalogv1alpha1.ScanModeIncremental,
	}, scans[0].Spec)
	cond = k8s.FindCondition(get().Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, folderscan.ReasonScanRequested, cond.Reason)

	// Never again: a later pass reads nothing and creates nothing.
	calls := folders.calls
	_, err = r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.Equal(t, calls, folders.calls)
	assert.Len(t, libraryScans(t, c, ns), 1)
	assert.Equal(t, metav1.ConditionTrue,
		k8s.FindCondition(get().Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned).Status)
}

// A series whose status already recorded its episodes -- every series in the
// library when this shipped -- owes no scan: an upgrade starts no storm.
func TestASeriesWithEpisodesAlreadyRecordedScansNothing(t *testing.T) {
	ctx := context.Background()
	const ns = "tv"
	s := scannedSeries(ns, catalogv1alpha1.SeriesStatus{
		Phase:   catalogv1alpha1.SeriesPhaseUnmonitored,
		Seasons: []catalogv1alpha1.SeasonStatus{{Number: 1, EpisodeCount: 2}},
	})
	ep := func(n int32) client.Object {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns,
				Name: series.EpisodeName(s.Name, catalogv1alpha1.SeriesTypeStandard, 1, n, nil)},
			Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: s.Name, SeasonNumber: 1, EpisodeNumber: n},
		}
	}
	c, r, folders := folderScanFixture(t, s, ep(1), ep(2))
	nn := types.NamespacedName{Namespace: ns, Name: s.Name}
	_, err := r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, nn, &got))
	assert.Nil(t, k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionFolderScanned))
	assert.Zero(t, folders.calls)
	assert.Empty(t, libraryScans(t, c, ns))
}
