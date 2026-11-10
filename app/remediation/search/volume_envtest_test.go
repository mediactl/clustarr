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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
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

// ensureComicProfile creates the built-in comic profile once.
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

// comic creates Saga with issues monitored issues, each released a week ago
// and wanted, the Comic's rollup counting them missing.
func (f *fixture) comic(t *testing.T, issues int) *catalogv1alpha1.Comic {
	t.Helper()
	ensureComicProfile(t, f.API)
	co := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: f.ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", Monitored: ptr.To(true),
			QualityProfileRef: "comic", RootFolderRef: "comics",
		},
	}
	require.NoError(t, f.API.Create(f.Ctx, co))
	released := metav1.NewTime(f.Clock.Now().Add(-7 * 24 * time.Hour))
	for n := 1; n <= issues; n++ {
		iss := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("saga-%d", n), Namespace: f.ns},
			Spec: catalogv1alpha1.IssueSpec{
				ComicRef: "saga", Number: fmt.Sprint(n), CalculatedNumberCentis: int32(n * 100), Monitored: ptr.To(true), //nolint:gosec // small
			},
		}
		require.NoError(t, f.API.Create(f.Ctx, iss))
		seedStatus(t, f.API, iss, k8s.ManagerCatalog, map[string]any{"phase": "Wanted", "date": released.UTC().Format(time.RFC3339)})
		f.Synced(t, iss)
	}
	seedStatus(t, f.API, co, k8s.ManagerCatalog, map[string]any{"missingChildCount": int64(issues)})
	f.Synced(t, co)
	return co
}

func comicKey(ns string) remediation.Key {
	return remediation.Key{Kind: remediation.KindComic, Namespace: ns, Name: "saga"}
}

// answerVolume writes the search agent's answer to the volume task at the
// Comic's season key.
func answerVolume(t *testing.T, f *fixture, co *catalogv1alpha1.Comic, task schema.SearchTask, approved int) {
	t.Helper()
	w := records.NewWriter(f.Bus.KV(events.BucketSearches), agentrecords.Searches(), "search-agent-0", "test")
	season := *task.Season
	rec := &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &schema.ItemRef{Kind: "Comic", Ref: schema.Ref{Namespace: f.ns, Name: co.Name, UID: string(co.UID)}},
			Sub:  schema.SeasonSub(season), Seq: task.Seq, State: records.StateAnswered,
		},
		Task: string(co.UID), FinishedAt: f.Clock.Now(), Season: &season,
	}
	for range approved {
		rec.Candidates = append(rec.Candidates, schema.ScoredRelease{Decision: commonv1.ReleaseDecision{Approved: true}})
	}
	_, err := w.Write(f.Ctx, agentrecords.SeasonSearchKey(string(co.UID), season), rec)
	require.NoError(t, err)
}

// TestAComicIsSearchedAsAVolumeBeforeItsIssues is 2026-11-10 design D10 in
// the loop: a Comic with two or more issues missing is asked for as a
// volume pack first -- one high-lane task naming the Comic, mirrored in
// status.volumeSearch -- while its Issues wait; the volume's answer wakes
// the Comic (S29), is incorporated, and folds into each Issue's
// lastSearchedAt and lastLiveSearchAt as its search, so the Issue's next
// search is index-only; and the new leader's ledger rebuilds the volume
// dispatch in flight.
func TestAComicIsSearchedAsAVolumeBeforeItsIssues(t *testing.T) {
	f := newFixture(t, "rs-volume", looptest.Options{})
	co := f.comic(t, 3)
	key := comicKey(f.ns)
	issKey := remediation.Key{Kind: remediation.KindIssue, Namespace: f.ns, Name: "saga-1"}

	f.Pass(t, remediation.KindComic, f.ns, "saga")
	tasks := searchTasks(t, f.Loop)
	require.Len(t, tasks, 1, "one volume task")
	task := tasks[0]
	require.Equal(t, commonv1.MediaKindComic, task.MediaRef.Kind)
	require.Equal(t, "saga", task.MediaRef.Name)
	require.Equal(t, "Comic", task.Item.Kind, "no Issue search is published for a volume")
	require.Equal(t, searchplan.VolumeNumber, *task.Season)
	require.Equal(t, schema.SearchReasonSeason, task.Reason)
	require.False(t, task.UserInvoked, "the automatic volume search is no person's")
	require.Equal(t, events.WorkSearchSubject(events.PriorityHigh, events.MediaKey("comic", f.ns, "saga")),
		f.Bus.Published("clustarr.work.catalog.search.")[0].Subject, "the high lane")
	f.get(t, co)
	require.NotNil(t, co.Status.VolumeSearch)
	require.True(t, searchplan.Automatic(co.Status.VolumeSearch))
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, co.Status.VolumeSearch.Seasons[0].Phase)
	require.True(t, co.Status.VolumeSearch.Dispatch.InFlight())
	f.Synced(t, co)

	// An Issue waits for the pending volume: nothing of its own is published.
	f.Pass(t, remediation.KindIssue, f.ns, "saga-1")
	require.Len(t, searchTasks(t, f.Loop), 1, "the issue waits for its volume")

	// The ledger rebuilds the volume dispatch after a leader change.
	all, err := search.SeasonLedgerSource{}.Rebuild(f.Ctx, f.C)
	require.NoError(t, err)
	found := false
	for _, o := range all {
		if o.Key.Namespace == f.ns && o.Key.Kind == "Comic" {
			found = true
			require.Equal(t, co.Status.VolumeSearch.Dispatch.Seq, o.Seq)
		}
	}
	require.True(t, found, "the Comic's volume dispatch is outstanding")

	// The answer wakes the Comic (S29) and is incorporated; its Issues are
	// woken (S33) and fold it.
	woken, issWoken := f.Woken.Count(key), f.Woken.Count(issKey)
	answerVolume(t, f, co, task, 2)
	require.Eventually(t, func() bool { return f.Woken.Count(key) > woken }, 5*time.Second, 20*time.Millisecond, "S29 never woke the Comic on its volume record")
	f.Pass(t, remediation.KindComic, f.ns, "saga")
	f.get(t, co)
	se := co.Status.VolumeSearch.Seasons[0]
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, se.Phase)
	require.EqualValues(t, 2, se.Approved)
	require.NotNil(t, co.Status.VolumeSearch.FinishedAt)
	require.Eventually(t, func() bool { return f.Woken.Count(issKey) > issWoken }, 5*time.Second, 20*time.Millisecond, "S33 never woke the issues on the volume's answer")
	f.Synced(t, co)

	iss := &catalogv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "saga-1", Namespace: f.ns}}
	f.Pass(t, remediation.KindIssue, f.ns, iss.Name)
	f.get(t, iss)
	require.EqualValues(t, 1, iss.Status.SearchAttempts.Count, "the volume's answer is the issue's search, once")
	require.NotNil(t, iss.Status.LastSearchedAt)
	require.NotNil(t, iss.Status.LastLiveSearchAt, "the volume asked the indexers: the issue's live search too")
	require.Len(t, searchTasks(t, f.Loop), 1, "no issue search in the same pass: its next is index-only, on its backoff")
}

// volumeSearch creates a person's volume Search of the comic (design §9.4:
// the comic page's "Search Volume" with grabBest, its interactive volume
// search without), a Search naming the Comic with spec.season the volume --
// which the Search CRD admits since C6 (the CEL rule, pkg/crdcheck).
func (f *fixture) volumeSearch(t *testing.T, name string, grabBest bool) *catalogv1alpha1.Search {
	t.Helper()
	s := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"},
			GrabBest: grabBest, Season: ptr.To(searchplan.VolumeNumber), TTL: metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.API.Create(f.Ctx, s))
	f.Synced(t, s)
	return s
}

// A planned volume Search -- a Search naming the Comic with spec.season (the
// volume) and grabBest, the comic page's "Search Volume" (design §9.4, D10;
// the W4b.3 leftover) -- is served by the Comic key's volume planner as a
// Series' planned Search is: S31 wakes the Comic, the pass takes the
// request into status.volumeSearch (the state names the Search), one
// season task goes out for the volume as a person's, its Issues wait for
// it, the answer finishes the state and folds into each Issue, and the
// finished state waits for the Search's handshake. The Search's own
// status.seasons is the Search controller's mirror, not run here. A volume
// Search without grabBest is interactive and no request of the planner's.
func TestAPlannedVolumeSearchIsServedByTheComic(t *testing.T) {
	f := newFixture(t, "rs-planned-volume", looptest.Options{})
	co := f.comic(t, 2)
	key := comicKey(f.ns)
	woken := f.Woken.Count(key)
	srch := f.volumeSearch(t, "volume", true)
	require.Eventually(t, func() bool { return f.Woken.Count(key) > woken }, 5*time.Second, 20*time.Millisecond, "S31 never woke the Comic on its planned volume Search")

	f.Pass(t, remediation.KindComic, f.ns, "saga")
	tasks := searchTasks(t, f.Loop)
	require.Len(t, tasks, 1, "one volume task")
	task := tasks[0]
	require.Equal(t, commonv1.MediaKindComic, task.MediaRef.Kind)
	require.Equal(t, "saga", task.MediaRef.Name)
	require.Equal(t, "Comic", task.Item.Kind, "no Issue search is published for the volume")
	require.Equal(t, searchplan.VolumeNumber, *task.Season)
	require.Equal(t, schema.SearchReasonSeason, task.Reason)
	require.True(t, task.UserInvoked, "a person asked for the volume")
	f.get(t, co)
	st := co.Status.VolumeSearch
	require.NotNil(t, st)
	require.False(t, searchplan.Automatic(st), "the state serves the Search, not the automatic volume search")
	require.Equal(t, catalogv1alpha1.SeasonSearchRequest{Name: srch.Name, UID: string(srch.UID)}, st.Request)
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, st.Seasons[0].Phase)
	require.True(t, st.Dispatch.InFlight())
	f.Synced(t, co)

	f.Pass(t, remediation.KindIssue, f.ns, "saga-1")
	require.Len(t, searchTasks(t, f.Loop), 1, "the issue waits for its volume")

	woken = f.Woken.Count(key)
	answerVolume(t, f, co, task, 2)
	require.Eventually(t, func() bool { return f.Woken.Count(key) > woken }, 5*time.Second, 20*time.Millisecond, "S29 never woke the Comic on its volume record")
	f.Pass(t, remediation.KindComic, f.ns, "saga")
	f.get(t, co)
	st = co.Status.VolumeSearch
	require.Equal(t, string(srch.UID), st.Request.UID)
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, st.Seasons[0].Phase)
	require.EqualValues(t, 2, st.Seasons[0].Approved)
	require.NotNil(t, st.FinishedAt)
	f.Synced(t, co)

	// The handshake: the Search has not settled (nothing mirrors it here),
	// so another pass serves nothing new and keeps the finished state.
	f.Pass(t, remediation.KindComic, f.ns, "saga")
	f.get(t, co)
	require.Equal(t, string(srch.UID), co.Status.VolumeSearch.Request.UID)
	require.Len(t, searchTasks(t, f.Loop), 1)

	// Each issue folds the volume's answer as its own search (S33 wakes
	// them; here each is passed), so none waits for a first search and
	// no automatic volume search follows the planned one.
	for _, name := range []string{"saga-1", "saga-2"} {
		iss := &catalogv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns}}
		f.Pass(t, remediation.KindIssue, f.ns, iss.Name)
		f.get(t, iss)
		require.EqualValues(t, 1, iss.Status.SearchAttempts.Count, "the volume's answer is the issue's search")
		require.NotNil(t, iss.Status.LastLiveSearchAt)
		f.Synced(t, iss)
	}
	require.Len(t, searchTasks(t, f.Loop), 1)

	// An interactive volume Search (no grabBest) is the Search controller's
	// own, never a request of the planner's: with the planned Search gone
	// the state stays the finished one, and nothing is published.
	interactive := f.volumeSearch(t, "interactive", false)
	require.NoError(t, f.API.Delete(f.Ctx, srch))
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(f.C.Get(f.Ctx, client.ObjectKeyFromObject(srch), &catalogv1alpha1.Search{}))
	}, 5*time.Second, 20*time.Millisecond)
	f.Pass(t, remediation.KindComic, f.ns, "saga")
	f.get(t, co)
	require.Equal(t, string(srch.UID), co.Status.VolumeSearch.Request.UID, "the interactive Search is no planned request")
	require.NotEqual(t, string(interactive.UID), co.Status.VolumeSearch.Request.UID)
	require.NotNil(t, co.Status.VolumeSearch.FinishedAt)
	require.Len(t, searchTasks(t, f.Loop), 1)
}
