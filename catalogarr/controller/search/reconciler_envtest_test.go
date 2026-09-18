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
	"sync"
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// recordingPublisher records every publish and can be told to fail, so the
// controller's ErrQueueFull branch is exercised without a broker.
type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
	envs     []*events.Envelope
	err      error
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return events.Receipt{}, p.err
	}
	p.subjects = append(p.subjects, subject)
	p.envs = append(p.envs, e)
	return events.Receipt{Stream: events.StreamWorkCatalogarr, Seq: uint64(len(p.envs))}, nil
}

func (p *recordingPublisher) snapshot() ([]string, []*events.Envelope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.subjects...), append([]*events.Envelope(nil), p.envs...)
}

type fixture struct {
	c     client.Client
	pub   *recordingPublisher
	clock *clockwork.FakeClock
	r     *search.Reconciler
	ns    string
}

func newFixture(t *testing.T, ns string) *fixture {
	t.Helper()
	c := newTestClient(t)
	newNamespace(t, context.Background(), c, ns)
	pub := &recordingPublisher{}
	clock := clockwork.NewFakeClockAt(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	r := search.NewReconciler(c, pub, record.NewFakeRecorder(32))
	r.Clock = clock
	return &fixture{c: c, pub: pub, clock: clock, r: r, ns: ns}
}

func (f *fixture) reconcile(t *testing.T, name string) reconcile.Result {
	t.Helper()
	res, err := f.r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: f.ns, Name: name},
	})
	require.NoError(t, err)
	return res
}

func (f *fixture) get(t *testing.T, name string) *catalogv1alpha1.Search {
	t.Helper()
	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: name}, got))
	return got
}

func (f *fixture) createSearch(t *testing.T, name string, spec catalogv1alpha1.SearchSpec) *catalogv1alpha1.Search {
	t.Helper()
	s := &catalogv1alpha1.Search{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns}, Spec: spec}
	require.NoError(t, f.c.Create(context.Background(), s))
	return s
}

func movieRef(name string) *commonv1.MediaRef {
	return &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}
}

func TestReconcilePublishesOneInteractiveSearchTaskAtHighPriority(t *testing.T) {
	f := newFixture(t, "search-start")
	s := f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: time.Hour}})

	f.reconcile(t, "srch")

	subjects, envs := f.pub.snapshot()
	require.Len(t, subjects, 1)
	wantKey := events.MediaKey(string(commonv1.MediaKindMovie), f.ns, "the-matrix")
	require.Equal(t, events.WorkSearchSubject(events.PriorityHigh, wantKey), subjects[0],
		"an interactive search is high priority and keyed by the canonical mediaKey")
	require.Equal(t, f.ns+"/srch", envs[0].Key, "the envelope key names the owning Search")
	require.Equal(t, events.MsgIDForObject(string(s.UID), s.Generation, "search"), envs[0].ID)

	var task schema.SearchTask
	require.NoError(t, json.Unmarshal(envs[0].Data, &task))
	require.Equal(t, schema.SearchReasonInteractive, task.Reason)
	require.True(t, task.UserInvoked)
	require.NotNil(t, task.SearchRef)
	require.Equal(t, "srch", task.SearchRef.Name)
	require.Equal(t, f.ns, task.SearchRef.Namespace)

	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase)
	require.NotNil(t, got.Status.StartedAt)
	require.Equal(t, got.Generation, got.Status.ObservedGeneration)

	// A second reconcile must not publish again: the phase is no longer empty.
	f.reconcile(t, "srch")
	subjects, _ = f.pub.snapshot()
	require.Len(t, subjects, 1)
}

func TestReconcileQueryModeFailsWithAnExplanation(t *testing.T) {
	f := newFixture(t, "search-query")
	q := "the matrix"
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{Query: &q, TTL: metav1.Duration{Duration: time.Hour}})

	f.reconcile(t, "srch")

	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseFailed, got.Status.Phase)
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SearchConditionFailed)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, "NotImplemented", cond.Reason)
	require.Contains(t, cond.Message, "rpc.indexarr.query")
	require.NotNil(t, got.Status.StartedAt, "a failed Search still needs a TTL anchor")

	subjects, _ := f.pub.snapshot()
	require.Empty(t, subjects, "query mode must not reach the work queue")
}

func TestReconcileQueueFullLeavesThePhaseUnsetAndRequeues(t *testing.T) {
	f := newFixture(t, "search-queuefull")
	f.pub.err = events.ErrQueueFull
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: time.Hour}})

	res := f.reconcile(t, "srch")
	require.Equal(t, time.Minute, res.RequeueAfter)

	got := f.get(t, "srch")
	require.Empty(t, string(got.Status.Phase), "back-pressure must leave the object retryable")
	cond := k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady)
	require.NotNil(t, cond)
	require.Equal(t, "QueueFull", cond.Reason)

	// Once the queue drains, the same object publishes normally.
	f.pub.err = nil
	f.reconcile(t, "srch")
	subjects, _ := f.pub.snapshot()
	require.Len(t, subjects, 1)
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, f.get(t, "srch").Status.Phase)
}

// workerWrote simulates the search worker's own status write: the disjoint
// field set it owns, under its own field manager.
func workerWrote(t *testing.T, c client.Client, ns, name string, finishedAt metav1.Time, results ...catalogv1alpha1.ReleaseDecision) {
	t.Helper()
	_, err := k8s.PatchStatus(context.Background(), c, k8s.ManagerCatalogarrWorker,
		search.Search(name, ns).WithStatus(
			search.SearchStatus().
				WithFinishedAt(finishedAt).
				WithIndexerOutcomes(catalogv1alpha1.IndexerOutcome{
					Name: "idx", State: catalogv1alpha1.IndexerOutcomeOK, Count: int32(len(results)),
				}).
				WithResults(results...)))
	require.NoError(t, err)
}

func approvedResult(guid string) catalogv1alpha1.ReleaseDecision {
	return catalogv1alpha1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			GUID: guid, IndexerRef: "idx", Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP",
			Protocol: commonv1.ProtocolTorrent, DownloadURL: "https://idx.example/dl/" + guid,
			PublishedAt: ptr.To(metav1.Now()),
		},
		Approved: true, Rank: 1,
	}
}

func rejectedResult(guid string) catalogv1alpha1.ReleaseDecision {
	return catalogv1alpha1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			GUID: guid, IndexerRef: "idx", Title: "The.Matrix.1999.480p.CAM-BAD",
			Protocol: commonv1.ProtocolTorrent, DownloadURL: "https://idx.example/dl/" + guid,
			PublishedAt: ptr.To(metav1.Now()),
		},
		Rejections: []commonv1.Rejection{{Reason: "release group is unwanted", Type: commonv1.RejectionPermanent}},
		Rank:       2,
	}
}

// drive takes a Search all the way to its steady state -- Running, then the
// worker's results, then Completed -- so a later failure path can be shown to
// PRESERVE that state rather than release it. A blank object cannot observe a
// server-side-apply release at all.
func (f *fixture) drive(t *testing.T, name string, results ...catalogv1alpha1.ReleaseDecision) *catalogv1alpha1.Search {
	t.Helper()
	f.reconcile(t, name)
	workerWrote(t, f.c, f.ns, name, metav1.NewTime(f.clock.Now()), results...)
	f.reconcile(t, name)
	got := f.get(t, name)
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	return got
}

func TestReconcileCompletesOnceTheWorkerHasWrittenItsResults(t *testing.T) {
	f := newFixture(t, "search-complete")
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: time.Hour}})

	got := f.drive(t, "srch", approvedResult("g1"))

	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.SearchConditionCompleted))
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	require.NotNil(t, got.Status.StartedAt, "the controller's own startedAt survived the worker's apply")
	require.NotNil(t, got.Status.FinishedAt, "the worker's finishedAt survived the controller's apply")
	require.Len(t, got.Status.Results, 1, "the worker's results survived the controller's apply")
	require.Len(t, got.Status.IndexerOutcomes, 1)
}

func TestReconcileFailsAStuckRunningSearchWithoutReleasingItsSteadyState(t *testing.T) {
	f := newFixture(t, "search-stuck")
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: time.Hour}})
	f.reconcile(t, "srch")
	before := f.get(t, "srch")
	require.NotNil(t, before.Status.StartedAt)

	f.clock.Advance(search.SearchRunningTimeout + time.Minute)
	f.reconcile(t, "srch")

	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseFailed, got.Status.Phase)
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SearchConditionFailed)
	require.NotNil(t, cond)
	require.Equal(t, "Timeout", cond.Reason)
	require.NotNil(t, got.Status.StartedAt, "the failure path must re-send every field it owns")
	require.Equal(t, before.Status.StartedAt.UTC(), got.Status.StartedAt.UTC())
}

func TestReconcileHandlesSpecGrab(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "search-grab")
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{
		MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: time.Hour},
	})
	require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: f.ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies",
		},
	}))
	before := f.drive(t, "srch", approvedResult("g1"), rejectedResult("g2"))

	// The user picks both: one approved, one permanently rejected.
	patch := client.MergeFrom(before.DeepCopy())
	before.Spec.Grab = []string{"g1", "g2"}
	require.NoError(t, f.c.Patch(ctx, before, patch))
	f.reconcile(t, "srch")

	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase,
		"the grab path re-sent the phase it owns instead of releasing it")
	require.NotNil(t, got.Status.StartedAt)
	require.Len(t, got.Status.Grabbed, 2)
	require.Equal(t, "g1", got.Status.Grabbed[0].GUID)
	require.Equal(t, k8s.ChildName("the-matrix", "g1"), got.Status.Grabbed[0].DownloadRef)
	require.Empty(t, got.Status.Grabbed[0].Error)
	require.Equal(t, "g2", got.Status.Grabbed[1].GUID)
	require.Empty(t, got.Status.Grabbed[1].DownloadRef)
	require.Contains(t, got.Status.Grabbed[1].Error, "spec.override")

	var dls downloadv1alpha1.DownloadList
	require.NoError(t, f.c.List(ctx, &dls, client.InNamespace(f.ns)))
	require.Len(t, dls.Items, 1, "a permanently rejected release must not be grabbed without override")
	dl := dls.Items[0]
	require.Equal(t, k8s.ChildName("the-matrix", "g1"), dl.Name)
	require.Equal(t, commonv1.ProtocolTorrent, dl.Spec.Protocol)
	require.NotNil(t, dl.Spec.Source.IndexerDownload)
	require.Equal(t, "g1", dl.Spec.Source.IndexerDownload.GUID)
	require.Equal(t, downloadv1alpha1.GrabSourceInteractive, dl.Spec.GrabbedBy)
	require.Equal(t, "hd-bluray-web", dl.Spec.QualityProfileRef)
	require.Len(t, dl.OwnerReferences, 1, "a Download is owned by its catalog item, never by the Search")
	require.Equal(t, "Movie", dl.OwnerReferences[0].Kind)

	// Flipping spec.override retries the failed entry without editing spec.grab.
	got.Spec.Override = true
	require.NoError(t, f.c.Update(ctx, got))
	f.reconcile(t, "srch")

	after := f.get(t, "srch")
	require.Len(t, after.Status.Grabbed, 2)
	require.Empty(t, after.Status.Grabbed[1].Error)
	require.Equal(t, k8s.ChildName("the-matrix", "g2"), after.Status.Grabbed[1].DownloadRef)
	require.NoError(t, f.c.List(ctx, &dls, client.InNamespace(f.ns)))
	require.Len(t, dls.Items, 2)

	// A further reconcile is idempotent: server-side apply re-sends the same
	// immutable spec, which the CEL rules accept.
	f.reconcile(t, "srch")
	require.NoError(t, f.c.List(ctx, &dls, client.InNamespace(f.ns)))
	require.Len(t, dls.Items, 2)
}

func TestReconcileDeletesTheSearchOnceItsTTLHasElapsed(t *testing.T) {
	f := newFixture(t, "search-ttl")
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{
		MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: 30 * time.Minute},
	})
	f.drive(t, "srch", approvedResult("g1"))

	res := f.reconcile(t, "srch")
	require.Greater(t, res.RequeueAfter, time.Duration(0), "still inside the TTL")

	f.clock.Advance(31 * time.Minute)
	f.reconcile(t, "srch")

	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	require.True(t, apierrors.IsNotFound(err), "err = %v", err)
}

// TestReconcileDeletesAFailedQueryModeSearchOnceItsTTLHasElapsed is the
// regression test for an ordering bug: failQueryMode returns before the TTL
// branch, so checking the query first would have left every rejected
// free-text Search on the cluster forever.
func TestReconcileDeletesAFailedQueryModeSearchOnceItsTTLHasElapsed(t *testing.T) {
	f := newFixture(t, "search-query-ttl")
	q := "the matrix"
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{Query: &q, TTL: metav1.Duration{Duration: 15 * time.Minute}})

	f.reconcile(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseFailed, f.get(t, "srch").Status.Phase)

	f.clock.Advance(16 * time.Minute)
	f.reconcile(t, "srch")

	err := f.c.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	require.True(t, apierrors.IsNotFound(err), "err = %v", err)
}

func TestReconcileIgnoresAMissingSearch(t *testing.T) {
	f := newFixture(t, "search-gone")
	res, err := f.r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: f.ns, Name: "does-not-exist"},
	})
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter)
}
