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
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

// TestRootFolderNamingChanged holds the RootFolder watch to the two render
// inputs: a path or naming change re-renders every file under the folder, a
// free-space floor edit does not.
func TestRootFolderNamingChanged(t *testing.T) {
	p := rootFolderNamingChanged()
	base := &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{
		Path: "/data/media/movies", Naming: catalogv1alpha1.NamingSpec{Dialect: "jellyfin"},
	}}
	update := func(edit func(*catalogv1alpha1.RootFolder)) bool {
		next := base.DeepCopy()
		edit(next)
		return p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: next})
	}
	assert.False(t, update(func(rf *catalogv1alpha1.RootFolder) { rf.Spec.MinFreeBytes = 1 << 30 }))
	assert.False(t, update(func(rf *catalogv1alpha1.RootFolder) { rf.Status.FreeBytes = 42 }))
	assert.True(t, update(func(rf *catalogv1alpha1.RootFolder) { rf.Spec.Naming.Dialect = "plex" }))
	assert.True(t, update(func(rf *catalogv1alpha1.RootFolder) {
		rf.Spec.Naming.Overrides = map[string]string{"movieFile": "{Movie CleanTitle}"}
	}))
	assert.True(t, update(func(rf *catalogv1alpha1.RootFolder) { rf.Spec.Path = "/data/media/films" }))
	assert.True(t, p.Create(event.CreateEvent{Object: base}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: base}), "a deleted RootFolder turns its files Unrenderable")
}

// TestTranscodeHolds covers the two things Reconcile derives from its one
// TranscodeJob list (ruling R18): the unincorporated swap, and whether any
// job still holds the file -- every non-terminal phase, the unset one a
// just-created job has included.
func TestTranscodeHolds(t *testing.T) {
	job := func(phase transcodev1alpha1.TranscodeJobPhase, finished *metav1.Time) transcodev1alpha1.TranscodeJob {
		return transcodev1alpha1.TranscodeJob{Status: transcodev1alpha1.TranscodeJobStatus{Phase: phase, FinishedAt: finished}}
	}
	probed := metav1.NewTime(metav1.Now().Add(-time.Hour))
	before, after := metav1.NewTime(probed.Add(-time.Minute)), metav1.NewTime(probed.Add(time.Minute))

	for _, phase := range []transcodev1alpha1.TranscodeJobPhase{
		"", transcodev1alpha1.TranscodeJobPhasePending,
		transcodev1alpha1.TranscodeJobPhasePlanned, transcodev1alpha1.TranscodeJobPhaseQueued,
		transcodev1alpha1.TranscodeJobPhaseRunning, transcodev1alpha1.TranscodeJobPhaseVerifying,
	} {
		assert.True(t, transcodeInFlight([]transcodev1alpha1.TranscodeJob{job(phase, nil)}), "phase %q holds the file", phase)
	}
	assert.False(t, transcodeInFlight([]transcodev1alpha1.TranscodeJob{
		job(transcodev1alpha1.TranscodeJobPhaseSucceeded, &after), job(transcodev1alpha1.TranscodeJobPhaseFailed, nil),
		job(transcodev1alpha1.TranscodeJobPhaseSkipped, nil),
	}))

	old := job(transcodev1alpha1.TranscodeJobPhaseSucceeded, &before)
	fresh := job(transcodev1alpha1.TranscodeJobPhaseSucceeded, &after)
	assert.Nil(t, latestUnincorporatedTranscode([]transcodev1alpha1.TranscodeJob{old}, &probed), "finished before the probe: incorporated")
	got := latestUnincorporatedTranscode([]transcodev1alpha1.TranscodeJob{old, fresh}, &probed)
	if assert.NotNil(t, got) {
		assert.Equal(t, after, *got.Status.FinishedAt)
	}
	assert.NotNil(t, latestUnincorporatedTranscode([]transcodev1alpha1.TranscodeJob{old}, nil), "never probed: every Succeeded job counts")
}

// TestKeepNamingRejudgesCurrent: a proposal kept over a failed lookup still
// says whether the file is at it, against spec.path as this apply leaves it.
func TestKeepNamingRejudgesCurrent(t *testing.T) {
	const want = "/data/media/movies/Heat (1995)/Heat (1995).mkv"
	prev := &catalogv1alpha1.NamingStatus{ExpectedPath: want, Current: false}
	kept := keepNaming(prev, "/data/media/movies/Heat (1995)/./Heat (1995).mkv")
	assert.True(t, kept.Current, "a swap moved the file onto its proposal")
	assert.False(t, prev.Current, "the object's own status is not mutated")
	assert.False(t, keepNaming(&catalogv1alpha1.NamingStatus{ExpectedPath: want, Current: true}, "/tmp/elsewhere.mkv").Current)
	held := &catalogv1alpha1.NamingStatus{Reason: catalogv1alpha1.NamingReasonProbePending}
	assert.Equal(t, held, keepNaming(held, "/tmp/x.mkv"))
	assert.Nil(t, keepNaming(nil, "/tmp/x.mkv"))

	assert.Equal(t, 30*time.Second, withNamingRetry(ctrl.Result{}, true).RequeueAfter)
	assert.Equal(t, 30*time.Second, withNamingRetry(ctrl.Result{RequeueAfter: TranscodedRecheckInterval}, true).RequeueAfter)
	assert.Equal(t, 10*time.Second, withNamingRetry(ctrl.Result{RequeueAfter: 10 * time.Second}, true).RequeueAfter)
	assert.Equal(t, TranscodedRecheckInterval, withNamingRetry(ctrl.Result{RequeueAfter: TranscodedRecheckInterval}, false).RequeueAfter)
}

// TestNamingOwnerOfAnEpisodeRefNamingNothing: an episode reference with no
// name and no keys covers no episode; it is Unrenderable, not a panic on
// the first of none.
func TestNamingOwnerOfAnEpisodeRefNamingNothing(t *testing.T) {
	r := namingFixture(t)
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "media"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode}},
	}
	owner, reason, err := r.namingOwner(t.Context(), mf)
	assert.NoError(t, err)
	assert.Nil(t, owner)
	assert.Equal(t, catalogv1alpha1.NamingReasonUnrenderable, reason)
}
