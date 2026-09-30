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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func book(name, author string, monitored, hasFile, cutoffMet bool) *catalogv1alpha1.Book {
	return &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec:       catalogv1alpha1.BookSpec{AuthorRef: ptr.To(author), Monitored: ptr.To(monitored)},
		Status:     catalogv1alpha1.BookStatus{HasFile: hasFile, CutoffMet: cutoffMet},
	}
}

func authorSearch() *catalogv1alpha1.Search {
	return &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "dostoevsky-x", Namespace: "media", UID: "parent-uid"},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindAuthor, Name: "dostoevsky"},
			GrabBest: true, IndexerRefs: []string{"nzbgeek"}, Limit: 50,
		},
	}
}

func containerReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithStatusSubresource(&catalogv1alpha1.Search{}).
		WithIndex(&catalogv1alpha1.Book{}, bookByAuthorRefIndex, func(o client.Object) []string {
			if ref := o.(*catalogv1alpha1.Book).Spec.AuthorRef; ref != nil {
				return []string{*ref}
			}
			return nil
		}).
		WithIndex(&catalogv1alpha1.Album{}, albumByArtistRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Album).Spec.ArtistRef}
		}).
		WithIndex(&catalogv1alpha1.Issue{}, issueByComicRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Issue).Spec.ComicRef}
		}).Build()
	return &Reconciler{Client: c, Scheme: c.Scheme()}, c
}

func children(t *testing.T, c client.Client) []catalogv1alpha1.Search {
	t.Helper()
	var list catalogv1alpha1.SearchList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("media"),
		client.MatchingLabels{catalogv1alpha1.LabelParentSearch: "dostoevsky-x"}))
	return list.Items
}

func parent(t *testing.T, c client.Client) catalogv1alpha1.Search {
	t.Helper()
	var s catalogv1alpha1.Search
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "dostoevsky-x"}, &s))
	return s
}

// TestAnAuthorSearchFansOutToItsWantedMonitoredBooks: one child Search per
// monitored book that is missing or below cutoff -- not one at cutoff, not
// an unmonitored one, not another author's -- each auto-grabbing and owned
// by the parent; a second reconcile creates nothing more.
func TestAnAuthorSearchFansOutToItsWantedMonitoredBooks(t *testing.T) {
	r, c := containerReconciler(t, authorSearch(),
		book("the-idiot", "dostoevsky", true, false, false),   // missing
		book("demons", "dostoevsky", true, true, false),       // cutoff unmet
		book("poor-folk", "dostoevsky", true, true, true),     // at cutoff
		book("the-double", "dostoevsky", false, false, false), // unmonitored
		book("the-stranger", "camus", true, false, false))     // another author

	reconcileSearch(t, r, "dostoevsky-x")
	reconcileSearch(t, r, "dostoevsky-x")

	kids := children(t, c)
	require.Len(t, kids, 2)
	names := map[string]bool{}
	for _, k := range kids {
		names[k.Spec.MediaRef.Name] = true
		require.Equal(t, commonv1.MediaKindBook, k.Spec.MediaRef.Kind)
		require.True(t, k.Spec.GrabBest)
		require.Equal(t, []string{"nzbgeek"}, k.Spec.IndexerRefs)
		require.EqualValues(t, 50, k.Spec.Limit)
		require.Len(t, k.OwnerReferences, 1)
		require.Equal(t, "dostoevsky-x", k.OwnerReferences[0].Name)
		require.True(t, *k.OwnerReferences[0].Controller)
	}
	require.Equal(t, map[string]bool{"the-idiot": true, "demons": true}, names)

	p := parent(t, c)
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, p.Status.Phase)
	require.NotNil(t, p.Status.StartedAt)
	require.Equal(t, &catalogv1alpha1.SearchChildren{Total: 2, Running: 2}, p.Status.Children)
}

// TestAContainerSearchCompletesWhenItsChildrenFinish: the parent counts
// its children and completes, with finishedAt, once none is running.
func TestAContainerSearchCompletesWhenItsChildrenFinish(t *testing.T) {
	r, c := containerReconciler(t, authorSearch(),
		book("the-idiot", "dostoevsky", true, false, false),
		book("demons", "dostoevsky", true, true, false))
	reconcileSearch(t, r, "dostoevsky-x")

	kids := children(t, c)
	require.Len(t, kids, 2)
	setChildStatus(t, c, kids[0].Name, catalogac.SearchStatus().WithPhase(catalogv1alpha1.SearchPhaseCompleted).
		WithGrabbed(catalogac.GrabResult().WithGUID("g").WithDownloadRef("the-idiot-abc")))
	reconcileSearch(t, r, "dostoevsky-x")
	p := parent(t, c)
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, p.Status.Phase, "one child still running")
	require.Equal(t, &catalogv1alpha1.SearchChildren{Total: 2, Running: 1, Completed: 1, Grabbed: 1}, p.Status.Children)

	setChildStatus(t, c, kids[1].Name, catalogac.SearchStatus().WithPhase(catalogv1alpha1.SearchPhaseFailed))
	reconcileSearch(t, r, "dostoevsky-x")
	p = parent(t, c)
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, p.Status.Phase)
	require.NotNil(t, p.Status.FinishedAt)
	require.NotNil(t, p.Status.StartedAt, "the completing apply still declares startedAt")
	require.Equal(t, &catalogv1alpha1.SearchChildren{Total: 2, Completed: 1, Failed: 1, Grabbed: 1}, p.Status.Children)
}

// setChildStatus writes a child Search's status as its own writers do.
func setChildStatus(t *testing.T, c client.Client, name string, st *catalogac.SearchStatusApplyConfiguration) {
	t.Helper()
	_, err := k8s.PatchStatus(context.Background(), c, k8s.ManagerCatalogarrWorker,
		catalogac.Search(name, "media").WithStatus(st))
	require.NoError(t, err)
}

func TestAContainerSearchWithNothingWantedCompletesAtOnce(t *testing.T) {
	r, c := containerReconciler(t, authorSearch(), book("poor-folk", "dostoevsky", true, true, true))
	reconcileSearch(t, r, "dostoevsky-x")
	require.Empty(t, children(t, c))
	p := parent(t, c)
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, p.Status.Phase)
	require.NotNil(t, p.Status.FinishedAt)
	require.Equal(t, &catalogv1alpha1.SearchChildren{}, p.Status.Children)
}

func TestAContainerSearchOverTheCapCreatesNothing(t *testing.T) {
	objs := []client.Object{authorSearch()}
	for i := range catalogv1alpha1.MaxContainerChildren + 1 {
		objs = append(objs, book(fmt.Sprintf("book-%03d", i), "dostoevsky", true, false, false))
	}
	r, c := containerReconciler(t, objs...)
	reconcileSearch(t, r, "dostoevsky-x")
	require.Empty(t, children(t, c))
	require.Equal(t, catalogv1alpha1.SearchPhaseFailed, parent(t, c).Status.Phase)
}

// TestAChildSearchNeverExpiresOnItsOwn: the parent's counts must not lose a
// child to the child's own TTL; it goes with its parent.
func TestAChildSearchNeverExpiresOnItsOwn(t *testing.T) {
	long := metav1.NewTime(time.Now().Add(-3 * time.Hour))
	child := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{
			Name: "child", Namespace: "media",
			Labels: map[string]string{catalogv1alpha1.LabelParentSearch: "dostoevsky-x"},
		},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "the-idiot"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
		Status: catalogv1alpha1.SearchStatus{Phase: catalogv1alpha1.SearchPhaseCompleted, StartedAt: &long, FinishedAt: &long},
	}
	r, c := containerReconciler(t, child)
	reconcileSearch(t, r, "child")
	var got catalogv1alpha1.Search
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "child"}, &got))
}

// TestWantedChild: missing or below cutoff, for each child kind -- an
// album is missing while any track lacks a file.
func TestWantedChild(t *testing.T) {
	tracks := []catalogv1alpha1.Track{{}, {}}
	for name, tc := range map[string]struct {
		obj  client.Object
		want bool
	}{
		"book missing":      {book("b", "a", true, false, false), true},
		"book cutoff unmet": {book("b", "a", true, true, false), true},
		"book at cutoff":    {book("b", "a", true, true, true), false},
		"book unmonitored":  {book("b", "a", false, false, false), false},
		"album partly missing": {&catalogv1alpha1.Album{
			Spec:   catalogv1alpha1.AlbumSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.AlbumStatus{Tracks: tracks, TrackFileCount: 1, CutoffMet: true},
		}, true},
		"album complete at cutoff": {&catalogv1alpha1.Album{
			Spec:   catalogv1alpha1.AlbumSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.AlbumStatus{Tracks: tracks, TrackFileCount: 2, CutoffMet: true},
		}, false},
		"issue missing": {&catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Monitored: ptr.To(true)}}, true},
		// Not released yet is not missing, as the *arrs' Missing lists
		// filter release date <= now (final review, 2026-09-30).
		"book not released yet": {func() client.Object {
			b := book("b", "a", true, false, false)
			b.Status.Metadata = &catalogv1alpha1.BookMetadata{ReleaseDate: &metav1.Time{Time: time.Now().Add(30 * 24 * time.Hour)}}
			return b
		}(), false},
		"issue not on sale yet": {&catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.IssueStatus{Date: &metav1.Time{Time: time.Now().Add(24 * time.Hour)}}}, false},
		"issue at cutoff": {&catalogv1alpha1.Issue{
			Spec:   catalogv1alpha1.IssueSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.IssueStatus{HasFile: true, CutoffMet: true},
		}, false},
	} {
		require.Equal(t, tc.want, wantedChild(tc.obj, time.Now()), name)
	}
}

// TestAParentWaitsForAChildsAutoGrab: a child's grab lands one reconcile
// after it turns Completed, so a Completed child whose best release is not
// yet in status.grabbed still counts as running -- else the parent completed
// before it and read "0 grabbed" (final review, 2026-09-30).
func TestAParentWaitsForAChildsAutoGrab(t *testing.T) {
	r, c := containerReconciler(t, authorSearch(), book("the-idiot", "dostoevsky", true, false, false))
	reconcileSearch(t, r, "dostoevsky-x")
	kid := children(t, c)[0]

	setChildStatus(t, c, kid.Name, catalogac.SearchStatus().WithPhase(catalogv1alpha1.SearchPhaseCompleted).
		WithResults(release("best", true)))
	reconcileSearch(t, r, "dostoevsky-x")
	p := parent(t, c)
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, p.Status.Phase, "its grab is still to come")

	setChildStatus(t, c, kid.Name, catalogac.SearchStatus().WithPhase(catalogv1alpha1.SearchPhaseCompleted).
		WithResults(release("best", true)).
		WithGrabbed(catalogac.GrabResult().WithGUID("best").WithDownloadRef("the-idiot-abc")))
	reconcileSearch(t, r, "dostoevsky-x")
	p = parent(t, c)
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, p.Status.Phase)
	require.Equal(t, &catalogv1alpha1.SearchChildren{Total: 1, Completed: 1, Grabbed: 1}, p.Status.Children)
}
