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

package search

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	worker "github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// The volume fixture is Saga's volume under the built-in comic profile
// (CBZ the cutoff, then CBR and PDF), read through a client that indexes
// Issues by their Comic as the manager's cache does (comic.IssueByComicRefIndex).
// Every result is the search agent's own: the title through pkg/decision's
// EvaluateRelease against a volume search (Identity.SeasonSearch on a
// Comic), ranked and recorded by worker.RankScored.

func sagaComic(entries ...catalogv1alpha1.DownloadEntry) *catalogv1alpha1.Comic {
	return &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: seasonNS, UID: "uid-saga"},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", Monitored: ptr.To(true),
			QualityProfileRef: "comic", RootFolderRef: "comics",
		},
		Status: catalogv1alpha1.ComicStatus{
			Metadata:  &catalogv1alpha1.ComicMetadata{Title: "Saga"},
			Downloads: entries,
		},
	}
}

// sagaIssue is issue n of Saga, out releasedAgo before seasonNow (negative:
// not yet out).
func sagaIssue(n int, releasedAgo time.Duration) *catalogv1alpha1.Issue {
	at := metav1.NewTime(seasonNow.Add(-releasedAgo))
	return &catalogv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("saga-%d", n), Namespace: seasonNS},
		Spec: catalogv1alpha1.IssueSpec{
			ComicRef: "saga", Number: fmt.Sprint(n), CalculatedNumberCentis: int32(n * 100), Monitored: ptr.To(true), //nolint:gosec // small
		},
		Status: catalogv1alpha1.IssueStatus{Date: &at},
	}
}

// withIssueFile gives iss an imported file of quality name.
func withIssueFile(t *testing.T, iss *catalogv1alpha1.Issue, name string) (*catalogv1alpha1.Issue, *catalogv1alpha1.MediaFile) {
	t.Helper()
	d, ok := quality.Lookup("comic", name)
	require.True(t, ok, name)
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: iss.Name + "-file", Namespace: seasonNS},
		Spec: catalogv1alpha1.MediaFileSpec{
			Path: "/data/comics/Saga/" + iss.Name + ".cbz", Quality: d.Quality,
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: iss.Name},
		},
	}
	iss.Status.HasFile, iss.Status.FileRef = true, ptr.To(mf.Name)
	return iss, mf
}

func volumeReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	objs = append(objs, builtinProfile(t, "comic"))
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.Issue{}, comic.IssueByComicRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Issue).Spec.ComicRef}
		}).
		WithStatusSubresource(&catalogv1alpha1.Search{}).Build()
	return &Reconciler{Client: c, Scheme: c.Scheme(), Clock: clockwork.NewFakeClockAt(seasonNow)}
}

// interactiveVolumeSearch is a person's interactive volume search of Saga
// (spec.season the volume, no grabBest).
func interactiveVolumeSearch() *catalogv1alpha1.Search {
	return &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "saga-v1", Namespace: seasonNS, UID: "uid-search"},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"},
			Season:   ptr.To(catalogv1alpha1.VolumeSearchSeason),
		},
	}
}

// volumeAnswer is the search agent's answer to a volume search of Saga.
func volumeAnswer(t *testing.T, titles ...string) []schema.ScoredRelease {
	t.Helper()
	target := decision.Target{
		Kind: commonv1.MediaKindComic, Monitored: true, Available: true,
		Identity: decision.Identity{Titles: []string{"Saga"}, SeasonSearch: true},
	}
	rels := make([]commonv1.ReleaseInfo, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, commonv1.ReleaseInfo{GUID: "guid-" + title, IndexerRef: "nzbgeek", Title: title, Protocol: commonv1.ProtocolUsenet})
	}
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"usenet": true}}
	prof, errs := quality.FromCRD(builtinProfile(t, "comic"), catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return worker.RankScored(decision.EvaluateRelease(context.Background(), target, prof, catalogue.LoadedCatalogue(), rels, o), o, nil, 0)
}

// liveComicEntry is a Comic's grab of title in flight, covering issues.
func liveComicEntry(t *testing.T, title string, issues ...string) catalogv1alpha1.DownloadEntry {
	t.Helper()
	sr := volumeAnswer(t, title)
	require.Len(t, sr, 1)
	info := sr[0].Decision.ReleaseInfo
	return catalogv1alpha1.DownloadEntry{
		ID: "entry-" + info.GUID, UID: "uid-entry",
		Release: catalogv1alpha1.DownloadRelease{
			Title: info.Title, GUID: info.GUID, IndexerRef: info.IndexerRef, Protocol: info.Protocol, Quality: info.Quality,
		},
		GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(seasonNow.Add(-time.Hour)),
		Phase: commonv1.DownloadPhaseDownloading, Issues: issues,
	}
}

// TestAVolumeResultIsRejectedAsThePlannerWould is the non-video design
// D10 and §9.4 in the Search controller: a comic's interactive volume
// search shows each result as the Comic's grab planner would judge it --
// the release covers the Issue its parsed number names (grabplan's
// issueFacts), so an issue the comic has is approved, one the comic lacks
// is refused WrongItem, one a live grab of equal preference covers
// QueueHigherPreference, one at the cutoff refused over its file, and
// a release naming another series is left as the agent judged it. No
// comic release is a season pack (CoversSeason).
func TestAVolumeResultIsRejectedAsThePlannerWould(t *testing.T) {
	const (
		first   = "Saga 001 (2012) (Digital) (Zone-Empire).cbz"
		second  = "Saga 002 (2012) (Digital) (Zone-Empire).cbz"
		third   = "Saga 003 (2012) (Digital) (Zone-Empire).cbz"
		seventh = "Saga 007 (2013) (Digital) (Zone-Empire).cbz"
	)
	answer := volumeAnswer(t, first, second, third, seventh)
	require.Len(t, answer, 4)
	for _, sr := range answer {
		require.True(t, sr.Decision.Approved, "the agent approves every Saga release for the volume: %s %+v", sr.Decision.Title, sr.Decision.Rejections)
	}
	issues := func() []client.Object {
		var out []client.Object
		for n := 1; n <= 3; n++ {
			out = append(out, sagaIssue(n, time.Duration(4-n)*7*24*time.Hour))
		}
		return out
	}

	t.Run("each release covers the issue its number names", func(t *testing.T) {
		objs := issues()
		i3, f3 := withIssueFile(t, objs[2].(*catalogv1alpha1.Issue), "CBZ")
		objs[2] = i3
		live := liveComicEntry(t, "Saga 002 (2012) (Digital) (Other).cbz", "2")
		r := volumeReconciler(t, append(objs, sagaComic(live), f3)...)
		got, other := r.seasonItemState(context.Background(), interactiveVolumeSearch(), answer)
		res := byTitle(got)
		require.Len(t, res, 4)
		require.True(t, res[first].Approved, "issue 1 is wanted: %+v", res[first].Rejections)
		rejectedAs(t, res[second], decision.ReasonQueueHigherPreference)
		rejectedAs(t, res[third], decision.ReasonExistingFormatScore)
		rejectedAs(t, res[seventh], decision.ReasonWrongItem)
		for _, rd := range got {
			require.False(t, rd.CoversSeason, "%s: a comic release is no season pack", rd.Title)
		}
		require.Equal(t, first, got[0].Title, "the approved release ranks first, the rejected after it")
		require.Equal(t, seventh, got[3].Title, "an issue the comic lacks is an other title, listed last")
		require.EqualValues(t, 1, other)
	})
	t.Run("another series is left as the agent judged it", func(t *testing.T) {
		wrong := volumeAnswer(t, "Paper Girls 001 (2015) (Digital) (Zone-Empire).cbz")
		r := volumeReconciler(t, append(issues(), sagaComic())...)
		got, other := r.seasonItemState(context.Background(), interactiveVolumeSearch(), wrong)
		require.EqualValues(t, 1, other)
		rejectedAs(t, got[0], decision.ReasonWrongItem)
		require.Len(t, got[0].Rejections, 1)
	})
	t.Run("a comic the cache lacks runs no item-state rule", func(t *testing.T) {
		r := volumeReconciler(t, issues()...)
		got, _ := r.seasonItemState(context.Background(), interactiveVolumeSearch(), answer)
		require.Len(t, got, 4)
		for _, rd := range got {
			require.True(t, rd.Approved, "%s: banded as the agent answered", rd.Title)
		}
	})
}

// TestAComicWakesItsPlannedVolumeSearchesAlone is the Comic watch's map
// function (design D10): a Comic's change reconciles the planned volume
// Searches naming it -- not an interactive one, which is no planned
// request, and not a same-named Series' planned Searches, since
// seriessearch's index keys by name alone -- and a Series' change its own.
func TestAComicWakesItsPlannedVolumeSearchesAlone(t *testing.T) {
	planned := func(name string, kind commonv1.MediaKind, season *int32, grabBest bool) *catalogv1alpha1.Search {
		return &catalogv1alpha1.Search{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: seasonNS},
			Spec:       catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: kind, Name: "saga"}, Season: season, GrabBest: grabBest},
		}
	}
	vol := ptr.To(catalogv1alpha1.VolumeSearchSeason)
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(
			planned("comic-volume", commonv1.MediaKindComic, vol, true),
			planned("comic-interactive", commonv1.MediaKindComic, vol, false),
			planned("series-planned", commonv1.MediaKindSeries, nil, true),
		).
		WithIndex(&catalogv1alpha1.Search{}, seriessearch.IndexSearchesBySeries, func(o client.Object) []string {
			if s := o.(*catalogv1alpha1.Search); s.Spec.PlannedBySeries() {
				return []string{s.Spec.MediaRef.Name}
			}
			return nil
		}).Build()
	r := &Reconciler{Client: c, Scheme: c.Scheme()}
	names := func(reqs []reconcile.Request) []string {
		var out []string
		for _, rq := range reqs {
			out = append(out, rq.Name)
		}
		return out
	}
	comicReqs := r.searchesOfContainer(context.Background(), &catalogv1alpha1.Comic{ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: seasonNS}})
	require.Equal(t, []string{"comic-volume"}, names(comicReqs))
	seriesReqs := r.searchesOfContainer(context.Background(), &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: seasonNS}})
	require.Equal(t, []string{"series-planned"}, names(seriesReqs))
}
