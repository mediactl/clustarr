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

package search_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	worker "github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// ensureComicProfile creates the built-in comic QualityProfile (cluster
// scoped, shared by every test in the package).
func ensureComicProfile(t *testing.T, c client.Client) {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/comic.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	err = c.Create(context.Background(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "comic"}, Spec: seeds[0].Spec})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
}

func comicProfile(t *testing.T) quality.Profile {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/comic.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "comic"}, Spec: seeds[0].Spec}, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return p
}

// comic creates Saga with issues monitored issues, each out a week ago and
// without a file (wanted), as the loop's volume envtest does.
func (f *recFixture) comic(t *testing.T, issues int) *catalogv1alpha1.Comic {
	t.Helper()
	ctx := context.Background()
	ensureComicProfile(t, f.c)
	co := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: f.ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", Monitored: ptr.To(true),
			QualityProfileRef: "comic", RootFolderRef: "comics",
		},
	}
	require.NoError(t, f.c.Create(ctx, co))
	seedStatus(t, f.c, co, k8s.ManagerCatalog, map[string]any{"metadata": map[string]any{"title": "Saga"}, "missingChildCount": int64(issues)})
	released := metav1.NewTime(f.clock.Now().Add(-7 * 24 * time.Hour))
	for n := 1; n <= issues; n++ {
		iss := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("saga-%d", n), Namespace: f.ns},
			Spec: catalogv1alpha1.IssueSpec{
				ComicRef: "saga", Number: fmt.Sprint(n), CalculatedNumberCentis: int32(n * 100), Monitored: ptr.To(true), //nolint:gosec // small
			},
		}
		require.NoError(t, f.c.Create(ctx, iss))
		seedStatus(t, f.c, iss, k8s.ManagerCatalog, map[string]any{"phase": "Wanted", "date": released.UTC().Format(time.RFC3339)})
	}
	require.NoError(t, f.c.Get(ctx, client.ObjectKeyFromObject(co), co))
	return co
}

func (f *recFixture) getComic(t *testing.T) *catalogv1alpha1.Comic {
	t.Helper()
	co := &catalogv1alpha1.Comic{}
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: "saga"}, co))
	return co
}

// volumeSearch creates a Search of Saga's volume: planned by the Comic
// with grabBest, interactive without.
func (f *recFixture) volumeSearch(t *testing.T, name string, grabBest bool) *catalogv1alpha1.Search {
	t.Helper()
	return f.createSearch(t, name, catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"},
		Season:   ptr.To(catalogv1alpha1.VolumeSearchSeason), GrabBest: grabBest, TTL: metav1.Duration{Duration: time.Hour},
	})
}

// comicState renders a Comic's status.volumeSearch serving req.
func comicState(req *catalogv1alpha1.Search, started time.Time, seasons []catalogv1alpha1.SeasonSearchSeason, finished *time.Time, dispatchedAt *time.Time) map[string]any {
	st := seriesState(req, started, seasons, finished, dispatchedAt)
	return map[string]any{"volumeSearch": st["seasonSearch"]}
}

// comicAnswer is the search agent's ranked answer to a volume search of
// Saga over titles.
func comicAnswer(t *testing.T, titles ...string) []schema.ScoredRelease {
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
	return worker.RankScored(decision.EvaluateRelease(context.Background(), target, comicProfile(t), catalogue.LoadedCatalogue(), rels, o), o, nil, 0)
}

// childSearches lists the child Searches a container Search fanned out
// to, by its label.
func (f *recFixture) childSearches(t *testing.T, parent string) []catalogv1alpha1.Search {
	t.Helper()
	var list catalogv1alpha1.SearchList
	require.NoError(t, f.c.List(context.Background(), &list, client.InNamespace(f.ns), client.MatchingLabels{catalogv1alpha1.LabelParentSearch: parent}))
	return list.Items
}

// TestAPlannedVolumeSearchMirrorsTheComic is the non-video design D10 and
// §9.4 in the Search controller: a comic's planned volume Search (the
// comic page's "Search Volume") is the Comic key's to serve, so the
// controller reads the Comic -- never a Series of the comic's name, which
// failed every one at once as SeriesGone -- waits Queued until the Comic
// takes it, mirrors status.volumeSearch into status.seasons as a Series'
// seasonSearch is mirrored (the volume the one season, its grabs the
// Comic's entries), reads Completed with the state's finishedAt, and
// fails ComicGone naming the comic when the Comic no longer exists.
func TestAPlannedVolumeSearchMirrorsTheComic(t *testing.T) {
	ctx := context.Background()
	f := newRecFixture(t, "search-planned-volume")
	co := f.comic(t, 2)
	s := f.volumeSearch(t, "volume", true)
	now := f.clock.Now()

	// Not taken yet: Queued, never Failed.
	f.reconcile(t, "volume")
	got := f.get(t, "volume")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase, "a planned volume Search reaches the Comic's planner")
	require.Equal(t, "Queued", readyReason(got))
	require.Equal(t, "waiting for the comic to take the search", k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady).Message)
	require.Empty(t, f.childSearches(t, "volume"), "a planned volume Search fans out to no Issue")
	subjects, _ := f.pub.snapshot()
	require.Empty(t, subjects, "the Comic publishes the volume task, never this Search")

	// The Comic took it: the volume searching.
	searching := []catalogv1alpha1.SeasonSearchSeason{{Number: catalogv1alpha1.VolumeSearchSeason, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7, Attempts: 1}}
	seedStatus(t, f.c, co, k8s.ManagerCatalogGrab, comicState(s, now, searching, nil, &now))
	f.reconcile(t, "volume")
	got = f.get(t, "volume")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase)
	require.Equal(t, "Searching", readyReason(got))
	require.Len(t, got.Status.Seasons, 1)
	require.Equal(t, catalogv1alpha1.VolumeSearchSeason, got.Status.Seasons[0].Number)
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, got.Status.Seasons[0].Phase)
	evs := drainEvents(f.fixture)
	require.Equal(t, 1, countPrefix(evs, "Normal SeriesSearchStarted"))
	require.Contains(t, strings.Join(evs, "\n"), "the comic took the search: 1 volume")

	// The volume's answer: two approved releases; the Comic grabbed one of
	// them after the answer, and holds an older grab of the other.
	answeredAt := now.Add(time.Minute)
	cands := comicAnswer(t, "Saga 001 (2012) (Digital) (Zone-Empire).cbz", "Saga 002 (2012) (Digital) (Zone-Empire).cbz")
	require.Len(t, cands, 2)
	season := catalogv1alpha1.VolumeSearchSeason
	_, err := f.w.Write(ctx, agentrecords.SeasonSearchKey(string(co.UID), season), &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &schema.ItemRef{Kind: "Comic", Ref: schema.Ref{Namespace: f.ns, Name: co.Name, UID: string(co.UID)}},
			Sub:  schema.SeasonSub(season), Seq: 7, State: records.StateAnswered,
		},
		Task: string(co.UID), FinishedAt: answeredAt, Candidates: cands, Season: &season,
	})
	require.NoError(t, err)
	grab := func(guid string, at time.Time, issue string) map[string]any {
		e := catalogv1alpha1.DownloadEntry{
			ID: "saga-" + issue, UID: "uid-saga-" + issue,
			Release: catalogv1alpha1.DownloadRelease{Title: "x", GUID: guid, IndexerRef: "nzbgeek", Protocol: commonv1.ProtocolUsenet},
			Source:  commonv1.DownloadSource{IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "nzbgeek", GUID: guid}},
			Client:  "usenet", GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(at),
			Phase: commonv1.DownloadPhasePending, RemoveOnImport: true, RemoveDataOnDelete: true, Issues: []string{issue},
			Dispatch: catalogv1alpha1.Dispatch{Seq: 1, DispatchedAt: metav1.NewTime(at)},
		}
		return toMap(t, &e)
	}
	ans := metav1.NewTime(answeredAt.Truncate(time.Second))
	answered := []catalogv1alpha1.SeasonSearchSeason{{
		Number: season, Phase: catalogv1alpha1.SeasonSearchAnswered, Seq: 7, Attempts: 1, AnsweredAt: &ans, Candidates: 2, Approved: 2,
	}}
	finished := answeredAt.Add(time.Minute)
	status := comicState(s, now, answered, &finished, &now)
	status["downloads"] = []any{grab(cands[0].Decision.GUID, answeredAt.Add(30*time.Second), "1"), grab(cands[1].Decision.GUID, now.Add(-time.Hour), "2")}
	seedStatus(t, f.c, co, k8s.ManagerCatalogGrab, status)

	f.reconcile(t, "volume")
	got = f.get(t, "volume")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.Equal(t, finished.Truncate(time.Second), got.Status.FinishedAt.UTC())
	require.Len(t, got.Status.Seasons, 1)
	require.EqualValues(t, 2, got.Status.Seasons[0].Results)
	require.EqualValues(t, 2, got.Status.Seasons[0].Approved)
	require.EqualValues(t, 1, got.Status.Seasons[0].Grabbed, "only the Comic's grab made of the answer counts")
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	require.Equal(t, "searched 1 volume, skipped 0, grabbed 1 releases", k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SearchConditionCompleted).Message)
	require.Equal(t, 1, countPrefix(drainEvents(f.fixture), "Normal SeriesSearchFinished"))
	require.Empty(t, got.Status.Results, "a planned volume Search incorporates no record")
	require.NotNil(t, f.getComic(t).Status.VolumeSearch, "the Comic's state is its own")

	t.Run("comic gone", func(t *testing.T) {
		f := newRecFixture(t, "search-planned-volume-gone")
		f.volumeSearch(t, "volume", true)
		f.reconcile(t, "volume")
		got := f.get(t, "volume")
		require.Equal(t, catalogv1alpha1.SearchPhaseFailed, got.Status.Phase)
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SearchConditionFailed)
		require.NotNil(t, cond)
		require.Equal(t, "ComicGone", cond.Reason)
		require.Equal(t, "the comic saga no longer exists", cond.Message)
	})
}

// TestAnInteractiveVolumeSearchIsOneSeasonTask is the non-video design D10
// and §9.4 in the Search controller: a comic's interactive volume Search
// (spec.season the volume, no grabBest; the comic page's "Interactive")
// is the Comic's own search, as a series' interactive season search is --
// one season task of the Comic on the high lane, no child Search per
// Issue (it is no container search, SearchSpec.FansOut) -- and the
// agent's answer lands in its own status.results, banded, so the volume
// modal has releases to pick from.
func TestAnInteractiveVolumeSearchIsOneSeasonTask(t *testing.T) {
	f := newRecFixture(t, "search-interactive-volume")
	f.comic(t, 2)
	s := f.volumeSearch(t, "volume", false)

	f.reconcile(t, "volume")
	got := f.get(t, "volume")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase)
	require.Empty(t, f.childSearches(t, "volume"), "an interactive volume Search fans out to no Issue")
	require.Nil(t, got.Status.Children)
	subjects, envs := f.pub.snapshot()
	require.Len(t, subjects, 1, "one season task of the Comic")
	require.Equal(t, events.WorkSearchSubject(events.PriorityHigh, events.MediaKey("comic", f.ns, "saga")), subjects[0])
	var task schema.SearchTask
	require.NoError(t, schema.Decode(envs[0].Schema, envs[0].Data, &task))
	require.Equal(t, commonv1.MediaKindComic, task.MediaRef.Kind)
	require.NotNil(t, task.Season)
	require.Equal(t, catalogv1alpha1.VolumeSearchSeason, *task.Season)
	require.Equal(t, "Comic", task.Item.Kind)
	require.Nil(t, task.Container)
	require.True(t, task.UserInvoked)
	require.Equal(t, string(s.UID), task.SearchRef.UID)

	cands := comicAnswer(t, "Saga 001 (2012) (Digital) (Zone-Empire).cbz", "Saga 002 (2012) (Digital) (Zone-Empire).cbz")
	require.Len(t, cands, 2)
	f.answer(t, f.get(t, "volume"), f.clock.Now(), cands)
	f.reconcile(t, "volume")
	f.reconcile(t, "volume")
	got = f.get(t, "volume")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.NotNil(t, got.Status.FinishedAt)
	require.Len(t, got.Status.Results, 2, "the answer is the Search's own")
	for i, rd := range got.Status.Results {
		require.EqualValues(t, i+1, rd.Rank, "banded and ranked")
	}
	require.Empty(t, f.childSearches(t, "volume"))
	subjects, _ = f.pub.snapshot()
	require.Len(t, subjects, 1, "no second task and no pick")
}
