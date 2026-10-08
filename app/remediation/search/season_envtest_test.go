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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest/looptest"
	"github.com/mediactl/clustarr/app/remediation/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// testCfg is the package's envtest control plane; nil when
// KUBEBUILDER_ASSETS is unset, and every envtest then skips.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

// fixture is one namespace's Breaking Bad under the loop.
type fixture struct {
	*looptest.Loop
	ns string
}

func newFixture(t *testing.T, ns string, o looptest.Options) *fixture {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	l := looptest.Start(t, testCfg, o)
	require.NoError(t, l.API.Create(l.Ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	ensureProfile(t, l.API)
	return &fixture{Loop: l, ns: ns}
}

func ensureProfile(t *testing.T, c client.Client) {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/hd-bluray-web.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	err = c.Create(context.Background(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd-bluray-web"}, Spec: seeds[0].Spec})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
}

// seedStatus applies status under manager, standing in for the writer the
// test does not run.
func seedStatus(t *testing.T, c client.Client, obj client.Object, manager k8s.FieldManager, status map[string]any) {
	t.Helper()
	gvk, err := apiutil.GVKForObject(obj, c.Scheme())
	require.NoError(t, err)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace()},
		"status":   status,
	}}
	_, err = k8s.PatchStatusUnstructured(context.Background(), c, manager, u)
	require.NoError(t, err)
}

// series creates Breaking Bad with seasons episodes each, every one aired
// a week ago and wanted.
func (f *fixture) series(t *testing.T, seasons, episodes int) *catalogv1alpha1.Series {
	t.Helper()
	ser := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad", Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
	}
	require.NoError(t, f.API.Create(f.Ctx, ser))
	aired := metav1.NewTime(f.Clock.Now().Add(-7 * 24 * time.Hour))
	for s := 1; s <= seasons; s++ {
		for e := 1; e <= episodes; e++ {
			ep := &catalogv1alpha1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("breaking-bad-s%02de%02d", s, e), Namespace: f.ns},
				Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "breaking-bad", SeasonNumber: int32(s), EpisodeNumber: int32(e)},
			}
			require.NoError(t, f.API.Create(f.Ctx, ep))
			seedStatus(t, f.API, ep, k8s.ManagerCatalog, map[string]any{"phase": "Wanted", "airDate": aired.UTC().Format(time.RFC3339)})
			f.Synced(t, ep)
		}
	}
	f.Synced(t, ser)
	return ser
}

func (f *fixture) get(t *testing.T, obj client.Object) {
	t.Helper()
	require.NoError(t, f.API.Get(f.Ctx, client.ObjectKeyFromObject(obj), obj))
}

// plannedSearch creates a person's planned Search of the series (one
// season when season is set).
func (f *fixture) plannedSearch(t *testing.T, name string, season *int32) *catalogv1alpha1.Search {
	t.Helper()
	s := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "breaking-bad"},
			GrabBest: true, Season: season, TTL: metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.API.Create(f.Ctx, s))
	f.Synced(t, s)
	return s
}

func seriesKey(ns string) remediation.Key {
	return remediation.Key{Kind: remediation.KindSeries, Namespace: ns, Name: "breaking-bad"}
}

// searchTasks are the published search tasks, decoded.
func searchTasks(t *testing.T, l *looptest.Loop) []schema.SearchTask {
	t.Helper()
	var out []schema.SearchTask
	for _, p := range l.Bus.Published("clustarr.work.catalog.search.") {
		var task schema.SearchTask
		require.NoError(t, schema.Decode(p.Env.Schema, p.Env.Data, &task))
		out = append(out, task)
	}
	return out
}

// answerSeason writes the search agent's answer to the season task.
func answerSeason(t *testing.T, f *fixture, ser *catalogv1alpha1.Series, task schema.SearchTask, allPaced bool) {
	t.Helper()
	answerSeasonWith(t, f, ser, task, func(r *schema.SearchRecord) { r.AllPaced = allPaced })
}

// answerSeasonWith writes the search agent's answer to the season task,
// shaped by with.
func answerSeasonWith(t *testing.T, f *fixture, ser *catalogv1alpha1.Series, task schema.SearchTask, with func(*schema.SearchRecord)) {
	t.Helper()
	w := records.NewWriter(f.Bus.KV(events.BucketSearches), agentrecords.Searches(), "search-agent-0", "test")
	season := *task.Season
	rec := &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: f.ns, Name: ser.Name, UID: string(ser.UID)}},
			Sub:  schema.SeasonSub(season), Seq: task.Seq, State: records.StateAnswered,
		},
		Task: string(ser.UID), FinishedAt: f.Clock.Now(), Season: &season,
	}
	with(rec)
	_, err := w.Write(f.Ctx, agentrecords.SeasonSearchKey(string(ser.UID), season), rec)
	require.NoError(t, err)
}

// TestASeriesSearchPlansOneSeasonAtATime is series search design §4.4 in
// the loop: a planned Search of a three-season Series publishes one
// high-lane season task per pass, the next only after the previous one's
// answer and behind its own pace slot; the Search wakes the Series (S31),
// and each season answer wakes it again (S29).
func TestASeriesSearchPlansOneSeasonAtATime(t *testing.T) {
	f := newFixture(t, "rs-one-season", looptest.Options{})
	ser := f.series(t, 3, 2)
	key := seriesKey(f.ns)
	f.plannedSearch(t, "all-seasons", nil)
	require.Eventually(t, func() bool { return f.Woken.Count(key) > 0 }, 5*time.Second, 20*time.Millisecond, "S31 never woke the Series")

	f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
	tasks := searchTasks(t, f.Loop)
	require.Len(t, tasks, 1)
	require.Equal(t, int32(1), *tasks[0].Season)
	require.Equal(t, schema.SearchReasonSeason, tasks[0].Reason)
	require.Equal(t, events.WorkSearchSubject(events.PriorityHigh, events.MediaKey("series", f.ns, "breaking-bad")),
		f.Bus.Published("clustarr.work.catalog.search.")[0].Subject, "the high lane")
	f.get(t, ser)
	require.NotNil(t, ser.Status.SeasonSearch)
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, ser.Status.SeasonSearch.Seasons[0].Phase)
	f.Synced(t, ser)

	// No answer: a pass publishes nothing new.
	f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
	require.Len(t, searchTasks(t, f.Loop), 1)

	for season := int32(2); season <= 3; season++ {
		woken := f.Woken.Count(key)
		answerSeason(t, f, ser, tasks[len(tasks)-1], false)
		require.Eventually(t, func() bool { return f.Woken.Count(key) > woken }, 5*time.Second, 20*time.Millisecond, "S29 never woke the Series on its season record")

		// The answer is incorporated, but the next season waits its pace
		// slot (20 per minute: one every 3 s).
		f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
		require.Len(t, searchTasks(t, f.Loop), int(season)-1, "season %d waits its pace slot", season)
		f.get(t, ser)
		require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, ser.Status.SeasonSearch.Seasons[season-2].Phase)
		f.Synced(t, ser)

		f.Clock.Advance(3 * time.Second)
		f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
		tasks = searchTasks(t, f.Loop)
		require.Len(t, tasks, int(season))
		require.Equal(t, season, *tasks[len(tasks)-1].Season)
		f.get(t, ser)
		searching := 0
		for _, se := range ser.Status.SeasonSearch.Seasons {
			if se.Phase == catalogv1alpha1.SeasonSearchSearching {
				searching++
			}
		}
		require.Equal(t, 1, searching, "one season in flight")
		f.Synced(t, ser)
	}
	for _, task := range tasks {
		require.Equal(t, "Series", task.Item.Kind, "no Episode search is published for a season")
	}
}

// TestS31WakesTheSeriesOnAPlannedSearch is design §4.6's source: a planned
// Search's create, change and delete each wake the Series it names; an
// interactive (not planned) Search does not.
func TestS31WakesTheSeriesOnAPlannedSearch(t *testing.T) {
	f := newFixture(t, "rs-s31", looptest.Options{})
	f.series(t, 1, 1)
	key := seriesKey(f.ns)
	settle := func() int {
		time.Sleep(300 * time.Millisecond)
		return f.Woken.Count(key)
	}
	base := settle()

	interactive := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "interactive", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "breaking-bad"}, Season: new(int32(1)),
			TTL: metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.API.Create(f.Ctx, interactive))
	require.Equal(t, base, settle(), "an interactive Search is the Search controller's, not the Series'")

	s := f.plannedSearch(t, "planned", nil)
	require.Eventually(t, func() bool { return f.Woken.Count(key) > base }, 5*time.Second, 20*time.Millisecond, "create")
	created := settle()

	f.get(t, s)
	seedStatus(t, f.API, s, k8s.ManagerCatalog, map[string]any{"phase": "Completed"})
	require.Eventually(t, func() bool { return f.Woken.Count(key) > created }, 5*time.Second, 20*time.Millisecond, "the handshake (phase settling)")
	changed := settle()

	require.NoError(t, f.API.Delete(f.Ctx, s))
	require.Eventually(t, func() bool { return f.Woken.Count(key) > changed }, 5*time.Second, 20*time.Millisecond, "delete")
}

// TestS24WakesTheChangedSeasonsEpisodes is design §7's wake: a change of a
// Series' season-search phases wakes the Episodes of the seasons that
// changed, and only those.
func TestS24WakesTheChangedSeasonsEpisodes(t *testing.T) {
	f := newFixture(t, "rs-s24", looptest.Options{})
	ser := f.series(t, 2, 2)
	time.Sleep(300 * time.Millisecond)
	ep := func(s, e int) remediation.Key {
		return remediation.Key{Kind: remediation.KindEpisode, Namespace: f.ns, Name: fmt.Sprintf("breaking-bad-s%02de%02d", s, e)}
	}
	before := map[remediation.Key]int{}
	for _, k := range []remediation.Key{ep(1, 1), ep(1, 2), ep(2, 1), ep(2, 2)} {
		before[k] = f.Woken.Count(k)
	}
	state := func(p1, p2 catalogv1alpha1.SeasonSearchPhase) map[string]any {
		return map[string]any{"seasonSearch": map[string]any{
			"request":   map[string]any{"name": "planned", "uid": "uid-planned"},
			"startedAt": f.Clock.Now().UTC().Format(time.RFC3339),
			"seasons":   []any{map[string]any{"number": int64(1), "phase": string(p1)}, map[string]any{"number": int64(2), "phase": string(p2)}},
		}}
	}
	seedStatus(t, f.API, ser, k8s.ManagerCatalogGrab, state(catalogv1alpha1.SeasonSearchSearching, catalogv1alpha1.SeasonSearchQueued))
	f.Synced(t, ser)
	time.Sleep(300 * time.Millisecond)
	for k := range before {
		before[k] = f.Woken.Count(k)
	}

	// Season 1 answered; season 2 unchanged.
	seedStatus(t, f.API, ser, k8s.ManagerCatalogGrab, state(catalogv1alpha1.SeasonSearchAnswered, catalogv1alpha1.SeasonSearchQueued))
	require.Eventually(t, func() bool {
		return f.Woken.Count(ep(1, 1)) > before[ep(1, 1)] && f.Woken.Count(ep(1, 2)) > before[ep(1, 2)]
	}, 5*time.Second, 20*time.Millisecond, "season 1's episodes were never woken")
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, before[ep(2, 1)], f.Woken.Count(ep(2, 1)), "season 2 did not change")
	require.Equal(t, before[ep(2, 2)], f.Woken.Count(ep(2, 2)))
}

// TestTheSeasonLedgerRebuildsAfterALeaderChange is design §4.5: a new
// leader's dispatch ledger rebuilds catalog-search-high's outstanding
// season dispatches from every Series' seasonSearch.dispatch, so a season
// in flight keeps its slot and is never dispatched twice.
func TestTheSeasonLedgerRebuildsAfterALeaderChange(t *testing.T) {
	f := newFixture(t, "rs-ledger", looptest.Options{})
	ser := f.series(t, 2, 1)
	f.plannedSearch(t, "all-seasons", nil)
	f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
	f.get(t, ser)
	d := ser.Status.SeasonSearch.Dispatch
	require.True(t, d.InFlight())

	// The leader changes: a new loop over the same cluster, its ledger
	// rebuilt from status, its publish book empty.
	g := &fixture{Loop: looptest.Start(t, testCfg, looptest.Options{Start: f.Clock.Now()}), ns: f.ns}
	g.Synced(t, ser)
	all, err := search.SeasonLedgerSource{}.Rebuild(g.Ctx, g.C)
	require.NoError(t, err)
	var out []dispatch.Outstanding
	for _, o := range all {
		if o.Key.Namespace == f.ns {
			out = append(out, o)
		}
	}
	require.Len(t, out, 1)
	require.Equal(t, d.Seq, out[0].Seq)
	require.Equal(t, "season-search", out[0].Key.Sub)

	g.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
	tasks := searchTasks(t, g.Loop)
	require.Len(t, tasks, 1, "the new leader republishes the season in flight (rebuilt from status), and only it")
	require.Equal(t, d.Seq, tasks[0].Seq)
	require.Equal(t, int32(1), *tasks[0].Season)
	f.get(t, ser)
	require.Equal(t, d.Seq, ser.Status.SeasonSearch.Dispatch.Seq, "the season was not dispatched again")
}

// TestTheSeriesSetIsCompleteOnEveryPath is design §16 item 3: catalog-grab
// declares a Series' pendingGrabs, seasonSearch and lastSearchedAt
// together on every path -- a failed stage applies none of the set, a
// deleting Series keeps them -- and owns exactly those paths.
func TestTheSeriesSetIsCompleteOnEveryPath(t *testing.T) {
	f := newFixture(t, "rs-set", looptest.Options{})
	ser := f.series(t, 2, 1)
	f.plannedSearch(t, "all-seasons", nil)
	f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
	f.get(t, ser)
	require.NotNil(t, ser.Status.SeasonSearch)
	pending := catalogv1alpha1.PendingGrab{ReleaseTitle: "Breaking.Bad.S02.720p.HDTV.x264-GRP", GrabAt: metav1.NewTime(f.Clock.Now().Add(time.Hour).Truncate(time.Second))}
	pm, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pending)
	require.NoError(t, err)
	sm, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ser.Status.SeasonSearch)
	require.NoError(t, err)
	seedStatus(t, f.API, ser, k8s.ManagerCatalogGrab, map[string]any{"pendingGrabs": []any{pm}, "seasonSearch": sm})
	f.Synced(t, ser)
	f.get(t, ser)
	steady := ser.Status.DeepCopy()
	require.Len(t, steady.PendingGrabs, 1)

	owned := func() []string {
		var out []string
		var fresh catalogv1alpha1.Series
		require.NoError(t, f.API.Get(f.Ctx, client.ObjectKeyFromObject(ser), &fresh))
		for _, mf := range fresh.ManagedFields {
			if mf.Manager != string(k8s.ManagerCatalogGrab) || mf.Subresource != "status" {
				continue
			}
			var m map[string]any
			require.NoError(t, json.Unmarshal(mf.FieldsV1.Raw, &m))
			for k := range m["f:status"].(map[string]any) {
				out = append(out, k)
			}
		}
		return out
	}

	t.Run("a steady pass keeps every field", func(t *testing.T) {
		f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
		f.get(t, ser)
		require.Len(t, ser.Status.PendingGrabs, 1)
		require.NotNil(t, ser.Status.SeasonSearch)
		require.ElementsMatch(t, []string{"f:pendingGrabs", "f:seasonSearch"}, owned())
		f.Synced(t, ser)
	})
	t.Run("a failed search stage applies none of the set", func(t *testing.T) {
		// The search stage fails transiently: clustarr-searches is gone.
		f.Bus.FailKV(events.BucketSearches, errors.New("the bucket is unreachable"))
		defer f.Bus.FailKV(events.BucketSearches, nil)
		before := ser.DeepCopy()
		f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
		f.get(t, ser)
		require.Equal(t, before.Status.PendingGrabs, ser.Status.PendingGrabs)
		require.Equal(t, before.Status.SeasonSearch, ser.Status.SeasonSearch)
		require.ElementsMatch(t, []string{"f:pendingGrabs", "f:seasonSearch"}, owned())
	})
	t.Run("a deleting Series keeps both", func(t *testing.T) {
		// A finalizer of the test's own holds the Series deleting.
		f.get(t, ser)
		ser.Finalizers = append(ser.Finalizers, "test.clustarr.io/hold")
		require.NoError(t, f.API.Update(f.Ctx, ser))
		require.NoError(t, f.API.Delete(f.Ctx, ser))
		f.Synced(t, ser)
		f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
		f.get(t, ser)
		require.Len(t, ser.Status.PendingGrabs, 1)
		require.NotNil(t, ser.Status.SeasonSearch)
	})
}

// TestAPacedSeasonAnswerCountsForNoEpisode is design §7: a season every
// indexer paced twice reads Answered, AllPaced, and no Episode of it folds
// that answer as a search -- nothing was queried -- while one the indexers
// answered is folded once. A season answered with no indexer to ask reads
// Answered, NoIndexer, and is folded by no Episode either (smoke finding 7).
func TestAPacedSeasonAnswerCountsForNoEpisode(t *testing.T) {
	for _, tc := range []struct {
		name              string
		paced, noIndexer  bool
		answers           int
		reason            catalogv1alpha1.SeasonSearchReason
		foldsIntoEpisodes bool
	}{
		// Paced: once to the back of the queue, then Answered.
		{name: "allPaced=true", paced: true, answers: 2, reason: catalogv1alpha1.SeasonSearchAllPaced},
		{name: "allPaced=false", answers: 1, foldsIntoEpisodes: true},
		{name: "noIndexer", noIndexer: true, answers: 1, reason: catalogv1alpha1.SeasonSearchNoIndexer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "rs-paced-"+strings.ReplaceAll(strings.ToLower(tc.name), "=", "-"), looptest.Options{})
			ser := f.series(t, 1, 2)
			f.plannedSearch(t, "season-1", new(int32(1)))
			f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
			for range tc.answers {
				tasks := searchTasks(t, f.Loop)
				answerSeasonWith(t, f, ser, tasks[len(tasks)-1], func(r *schema.SearchRecord) {
					r.AllPaced, r.NoIndexer = tc.paced, tc.noIndexer
				})
				f.Clock.Advance(3 * time.Second)
				f.Synced(t, ser)
				f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
				f.Synced(t, ser)
				f.Clock.Advance(3 * time.Second)
				f.Pass(t, remediation.KindSeries, f.ns, "breaking-bad")
				f.get(t, ser)
				f.Synced(t, ser)
			}
			se := ser.Status.SeasonSearch.Seasons[0]
			require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, se.Phase)
			require.Equal(t, tc.reason, se.Reason)

			ep := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad-s01e01", Namespace: f.ns}}
			f.Pass(t, remediation.KindEpisode, f.ns, ep.Name)
			f.get(t, ep)
			if !tc.foldsIntoEpisodes {
				require.Zero(t, ep.Status.SearchAttempts.Count, "a season answer no indexer gave is no episode's search")
				require.Nil(t, ep.Status.LastSearchedAt)
			} else {
				require.EqualValues(t, 1, ep.Status.SearchAttempts.Count, "the season's answer is the episode's search, once")
				require.NotNil(t, ep.Status.LastSearchedAt)
			}
		})
	}
}

// TestAnEpisodeSearchRecordWakesTheEpisodeAndItsSeries is S29 (A4.2 step
// 3, R7): an episode's search record wakes the Episode, whose search stage
// incorporates it, and its Series, whose grab stage reads it.
func TestAnEpisodeSearchRecordWakesTheEpisodeAndItsSeries(t *testing.T) {
	f := newFixture(t, "rs-s29", looptest.Options{})
	ser := f.series(t, 1, 1)
	ep := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad-s01e01", Namespace: f.ns}}
	f.get(t, ep)
	epKey := remediation.Key{Kind: remediation.KindEpisode, Namespace: f.ns, Name: ep.Name}
	time.Sleep(300 * time.Millisecond)
	beforeEp, beforeSer := f.Woken.Count(epKey), f.Woken.Count(seriesKey(f.ns))

	w := records.NewWriter(f.Bus.KV(events.BucketSearches), agentrecords.Searches(), "search-agent-0", "test")
	_, err := w.Write(f.Ctx, agentrecords.SearchKey(string(ep.UID)), &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: f.ns, Name: ep.Name, UID: string(ep.UID)}},
			Seq:  1, State: records.StateAnswered,
		},
		Task:       string(ep.UID),
		Container:  &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: f.ns, Name: ser.Name, UID: string(ser.UID)}},
		FinishedAt: f.Clock.Now(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return f.Woken.Count(epKey) > beforeEp && f.Woken.Count(seriesKey(f.ns)) > beforeSer
	}, 5*time.Second, 20*time.Millisecond, "the record did not wake both")

	t.Run("a Search's record wakes no item", func(t *testing.T) {
		time.Sleep(300 * time.Millisecond)
		ep0, ser0 := f.Woken.Count(epKey), f.Woken.Count(seriesKey(f.ns))
		_, err := w.Write(f.Ctx, agentrecords.SearchKey("uid-a-search"), &schema.SearchRecord{
			RecordHeader: schema.RecordHeader{Item: &schema.ItemRef{Kind: "Search", Ref: schema.Ref{Namespace: f.ns, Name: "s", UID: "uid-a-search"}}, Seq: 1, State: records.StateAnswered},
			Task:         "uid-a-search", FinishedAt: f.Clock.Now(),
		})
		require.NoError(t, err)
		time.Sleep(500 * time.Millisecond)
		require.Equal(t, ep0, f.Woken.Count(epKey))
		require.Equal(t, ser0, f.Woken.Count(seriesKey(f.ns)))
	})
}

// TestNoBurstOfLiveSearchesReachesAnIndexer is CLAUDE.md's 2026-10-06
// gotcha in the loop: 104 never-searched items due at once in one
// namespace send at most 20 live searches in the first minute (the
// namespace's live-search pace), each pass re-run at the Due it asked for.
func TestNoBurstOfLiveSearchesReachesAnIndexer(t *testing.T) {
	f := newFixture(t, "rs-burst", looptest.Options{})
	f.series(t, 4, 26)
	start := f.Clock.Now()
	var keys []string
	for s := 1; s <= 4; s++ {
		for e := 1; e <= 26; e++ {
			keys = append(keys, fmt.Sprintf("breaking-bad-s%02de%02d", s, e))
		}
	}
	next := map[string]time.Time{}
	for _, k := range keys {
		next[k] = start
	}
	for {
		now := f.Clock.Now()
		if now.Sub(start) >= time.Minute {
			break
		}
		soonest := start.Add(time.Minute)
		for _, k := range keys {
			if next[k].After(now) {
				soonest = minTime(soonest, next[k])
				continue
			}
			res := f.Pass(t, remediation.KindEpisode, f.ns, k)
			at := now.Add(time.Hour)
			if res.RequeueAfter > 0 {
				at = now.Add(res.RequeueAfter)
			}
			next[k] = at
			soonest = minTime(soonest, at)
		}
		f.Clock.Advance(max(soonest.Sub(now), time.Second))
	}
	live := 0
	for _, task := range searchTasks(t, f.Loop) {
		if task.Item.Kind == "Episode" && !task.IndexOnly {
			live++
		}
	}
	t.Logf("live searches in the first minute: %d", live)
	require.Positive(t, live, "the items were due")
	require.LessOrEqual(t, live, 20, "a pool of searches must never be fired at an indexer in one burst")
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
