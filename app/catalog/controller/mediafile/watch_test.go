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

package mediafile

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestExtractTranscodeJobPhase(t *testing.T) {
	tj := &transcodev1alpha1.TranscodeJob{Status: transcodev1alpha1.TranscodeJobStatus{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}}
	assert.Equal(t, "Succeeded", extractTranscodeJobPhase(tj))

	other := &subtitlev1alpha1.SubtitleRequest{}
	assert.Equal(t, "", extractTranscodeJobPhase(other), "wrong type returns the zero value, never panics")
}

func TestMediaFileForTranscodeJob(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc12345", Namespace: "media"},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: "inception-abc1234567"},
	}

	reqs := (&Reconciler{Client: c}).mediaFileForTranscodeJob(t.Context(), tj)

	want := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "media", Name: "inception-abc1234567"}}}
	assert.Equal(t, want, reqs)
}

// namingFixture is a library of two root folders: a movie under "movies",
// and under "tv" a series whose first two episodes share one multi-episode
// file, beside another series under another root with a file of its own.
func namingFixture(t *testing.T) *Reconciler {
	t.Helper()
	const ns = "media"
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: ns} }
	file := func(name string, ref commonv1.MediaRef) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: meta(name), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: ref}}
	}
	episode := func(name, series string) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{ObjectMeta: meta(name), Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: series}}
	}
	objs := []client.Object{
		&catalogv1alpha1.Movie{ObjectMeta: meta("heat"), Spec: catalogv1alpha1.MovieSpec{RootFolderRef: "movies"}},
		&catalogv1alpha1.Series{ObjectMeta: meta("firefly"), Spec: catalogv1alpha1.SeriesSpec{RootFolderRef: "tv"}},
		&catalogv1alpha1.Series{ObjectMeta: meta("other"), Spec: catalogv1alpha1.SeriesSpec{RootFolderRef: "anime"}},
		episode("firefly-s01e01", "firefly"), episode("firefly-s01e02", "firefly"), episode("other-s01e01", "other"),
		file("heat-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}),
		file("firefly-e01e02-file", commonv1.MediaRef{
			Kind: commonv1.MediaKindEpisode, Name: "firefly-s01e01",
			Keys: []string{"firefly-s01e01", "firefly-s01e02"},
		}),
		file("other-file", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "other-s01e01"}),
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.MediaFile{}, mediaFileByOwnerIndex, indexMediaFileByOwner).
		WithIndex(&catalogv1alpha1.Episode{}, episodeBySeriesIndex, indexEpisodeBySeries).
		WithIndex(&catalogv1alpha1.Movie{}, movieByRootFolderIndex, indexMovieByRootFolder).
		WithIndex(&catalogv1alpha1.Series{}, seriesByRootFolderIndex, indexSeriesByRootFolder).
		Build()
	return &Reconciler{Client: c}
}

func requestNames(reqs []reconcile.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Namespace+"/"+r.Name)
	}
	slices.Sort(out)
	return out
}

// TestNamingWatchesMapToTheFilesTheyRename covers the four naming map
// functions, each through the index SetupWithManager registers: an item's
// own files, a series' episodes' files (the multi-episode file once, not
// once per episode) and a root folder's every item's files, and nothing of
// another series or root folder.
func TestNamingWatchesMapToTheFilesTheyRename(t *testing.T) {
	r := namingFixture(t)
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: "media"} }
	ctx := t.Context()

	assert.Equal(t, []string{"media/heat-file"},
		requestNames(r.mediaFilesForMovie(ctx, &catalogv1alpha1.Movie{ObjectMeta: meta("heat")})))
	assert.Equal(t, []string{"media/firefly-e01e02-file"},
		requestNames(r.mediaFilesForEpisode(ctx, &catalogv1alpha1.Episode{ObjectMeta: meta("firefly-s01e02")})),
		"a multi-episode file is reached through every episode it covers, not only the one it names")
	assert.Equal(t, []string{"media/firefly-e01e02-file"},
		requestNames(r.mediaFilesForSeries(ctx, &catalogv1alpha1.Series{ObjectMeta: meta("firefly")})))
	assert.Equal(t, []string{"media/firefly-e01e02-file"},
		requestNames(r.mediaFilesForRootFolder(ctx, &catalogv1alpha1.RootFolder{ObjectMeta: meta("tv")})))
	assert.Equal(t, []string{"media/heat-file"},
		requestNames(r.mediaFilesForRootFolder(ctx, &catalogv1alpha1.RootFolder{ObjectMeta: meta("movies")})))
	assert.Empty(t, r.mediaFilesForRootFolder(ctx, &catalogv1alpha1.RootFolder{ObjectMeta: meta("music")}))
	assert.Empty(t, r.mediaFilesForMovie(ctx, &catalogv1alpha1.Series{ObjectMeta: meta("heat")}), "wrong type maps to nothing")
}

// TestNamingInputsIgnoreStatusChurn holds the naming watches' predicates to
// the fields renderNaming reads: a Movie's phase moving must not wake every
// file's reconcile, a title change must.
func TestNamingInputsIgnoreStatusChurn(t *testing.T) {
	movie := func(title string, phase catalogv1alpha1.MoviePhase) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
			Phase:    phase,
			Metadata: &catalogv1alpha1.MovieMetadata{Title: title, Year: 1995},
		}}
	}
	assert.Equal(t, movieNamingInputs(movie("Heat", "")), movieNamingInputs(movie("Heat", catalogv1alpha1.MoviePhaseDownloading)))
	assert.NotEqual(t, movieNamingInputs(movie("Heat", "")), movieNamingInputs(movie("Heat (Director's Cut)", "")))

	series := func(path string) *catalogv1alpha1.Series {
		return &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Path: path}}
	}
	assert.NotEqual(t, seriesNamingInputs(series("/data/media/tv/A")), seriesNamingInputs(series("/data/media/tv/B")),
		"the series folder is part of every episode's path")

	episode := func(title string) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{Status: catalogv1alpha1.EpisodeStatus{Title: title}}
	}
	assert.NotEqual(t, episodeNamingInputs(episode("Serenity")), episodeNamingInputs(episode("The Train Job")))
}

// TestMarkNamingCurrent covers the one mapping from status.naming to the
// NamingCurrent condition, including the Current=True arm no envtest can
// reach (a spec.path under /data/media/ needs a real library mount).
func TestMarkNamingCurrent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		naming *catalogv1alpha1.NamingStatus
		status metav1.ConditionStatus
		reason string
	}{
		{"current", &catalogv1alpha1.NamingStatus{ExpectedPath: "/data/media/movies/Heat (1995)/Heat (1995).mkv", Current: true}, metav1.ConditionTrue, "Current"},
		{"stale", &catalogv1alpha1.NamingStatus{ExpectedPath: "/data/media/movies/Heat (1995)/Heat (1995).mkv"}, metav1.ConditionFalse, "Stale"},
		{"held", &catalogv1alpha1.NamingStatus{Reason: catalogv1alpha1.NamingReasonTranscodePending}, metav1.ConditionUnknown, "TranscodePending"},
		{"unrenderable", &catalogv1alpha1.NamingStatus{Reason: catalogv1alpha1.NamingReasonUnrenderable}, metav1.ConditionUnknown, "Unrenderable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf := &catalogv1alpha1.MediaFile{}
			conditions := []metav1.Condition{{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionTrue, Reason: "Current"}}
			markNamingCurrent(mf, &conditions, tc.naming)
			assert.Len(t, conditions, 1, "the condition is replaced, never appended beside the old one")
			got := k8s.FindCondition(conditions, catalogv1alpha1.ConditionNamingCurrent)
			assert.Equal(t, tc.status, got.Status)
			assert.Equal(t, tc.reason, got.Reason)
			if tc.status == metav1.ConditionFalse {
				assert.True(t, strings.Contains(got.Message, tc.naming.ExpectedPath), "a stale file's condition names where it belongs")
			}
		})
	}

	var none []metav1.Condition
	markNamingCurrent(&catalogv1alpha1.MediaFile{}, &none, nil)
	assert.Empty(t, none, "a kind this phase does not name gets no condition")
}
