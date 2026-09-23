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

package grab

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// workerStatus is the COMPLETE set of status fields
// k8s.ManagerCatalogarrGrab owns on a Movie or an Episode: pendingGrab,
// lastSearchedAt and searchAttempts, and nothing else on those kinds. It never
// carries status.phase, which is the Movie/Episode reconciler's under
// k8s.ManagerCatalogarr, or status.metadata, which is the gateway's under
// k8s.ManagerCatalogarrMetadata.
//
// Nor does it carry status.activeDownloadRef. Spec §2's field-manager table
// once gave that field to this manager too, while the reconcilers also wrote
// it; under ForceOwnership its ownership migrated to whichever applied last,
// and the reconciler's "omit to clear on a terminal Download" only worked
// while it happened to hold the field. Gap-fix ruling R-5 gives it one writer,
// the reconciler, deriving it from the item's non-terminal Download. This
// package no longer sends it -- so its first apply after that change releases
// whatever this manager still owned of it, and the reconciler re-derives it --
// and its double-grab guard looks the Downloads up itself (see
// guardExistingDownloads) instead of reading the ref.
//
// It is a value type rather than separate patch methods because server-side
// apply replaces a field manager's ownership set on every apply instead of
// merging it. A patch that sent only pendingGrab would RELEASE lastSearchedAt
// and searchAttempts -- which reads as "reset to zero" on the object. The
// only safe shape is updateWorkerStatus's: read the live object, take the
// whole owned set off it, change the one thing this call means to change, and
// re-declare all of it, conditional on nothing having written the object in
// between.
//
// Anything else writing these fields under k8s.ManagerCatalogarrGrab must go
// through updateWorkerStatus. Sending a subset is a data-loss bug that no test
// on a freshly created object can observe, because a blank object has nothing
// to release; declaring the set from a stale read is a lost update that no
// release test can observe either.
type workerStatus struct {
	PendingGrab    *catalogv1alpha1.PendingGrab
	LastSearchedAt *metav1.Time
	SearchAttempts commonv1.Attempts
}

// grabContext is the configuration governing a grab for one catalog item.
// An Episode has none of these fields on its own spec, so episodeOps reads
// them off the owning Series.
type grabContext struct {
	QualityProfileRef string
	DelayProfileRef   *string
	Tags              []string
}

// kindOps is the per-kind glue Decide, performGrab and the RSS matcher share:
// how to fetch one catalog object, where its grab configuration comes from,
// and how to read and write the worker-owned slice of its status.
type kindOps interface {
	// kind is the media kind this implementation serves.
	kind() commonv1.MediaKind

	// recordsPendingGrab reports whether the kind's status has a
	// pendingGrab field for Decide to record a delayed grab in. Every kind
	// but Issue does.
	recordsPendingGrab() bool

	// get fetches the object, typed.
	get(ctx context.Context, c client.Client, ns, name string) (client.Object, error)

	// grabContext resolves the quality profile, delay profile and tags that
	// govern a grab for obj.
	grabContext(ctx context.Context, c client.Client, obj client.Object) (grabContext, error)

	// workerStatus reads the worker-owned status fields off a fetched object.
	workerStatus(obj client.Object) workerStatus

	// applyWorkerStatus declares the whole worker-owned set under
	// k8s.ManagerCatalogarrGrab, conditional on the object still being at
	// resourceVersion. It never sends status.phase: Phase is
	// k8s.ManagerCatalogarr's, recomputed by the Movie/Episode reconciler
	// from the fields written here. Only updateWorkerStatus calls it.
	applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error
}

// kindOpsFor returns the operations for a status-target kind: every kind
// that is one release's worth of content and carries the grab path's status
// fields -- movie, episode, album, book, audiobook and issue. A Series is a
// grab TARGET but never a status target, and Artist, Author and Comic are
// containers that are never grabbed at all, so none of them is here (see
// StatusTargets).
func kindOpsFor(kind commonv1.MediaKind) (kindOps, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return movieOps{}, nil
	case commonv1.MediaKindEpisode:
		return episodeOps{}, nil
	case commonv1.MediaKindAlbum:
		return albumOps{}, nil
	case commonv1.MediaKindBook:
		return bookOps{}, nil
	case commonv1.MediaKindAudiobook:
		return audiobookOps{}, nil
	case commonv1.MediaKindIssue:
		return issueOps{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKind, kind)
	}
}

type movieOps struct{}

func (movieOps) kind() commonv1.MediaKind { return commonv1.MediaKindMovie }
func (movieOps) recordsPendingGrab() bool { return true }

func (movieOps) get(ctx context.Context, c client.Client, ns, name string) (client.Object, error) {
	var m catalogv1alpha1.Movie
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (movieOps) grabContext(_ context.Context, _ client.Client, obj client.Object) (grabContext, error) {
	m, ok := obj.(*catalogv1alpha1.Movie)
	if !ok {
		return grabContext{}, fmt.Errorf("grab: expected *Movie, got %T", obj)
	}
	return grabContext{
		QualityProfileRef: m.Spec.QualityProfileRef,
		DelayProfileRef:   m.Spec.DelayProfileRef,
		Tags:              m.Spec.Tags,
	}, nil
}

func (movieOps) workerStatus(obj client.Object) workerStatus {
	m, ok := obj.(*catalogv1alpha1.Movie)
	if !ok {
		return workerStatus{}
	}
	return workerStatus{
		PendingGrab:    m.Status.PendingGrab,
		LastSearchedAt: m.Status.LastSearchedAt,
		SearchAttempts: m.Status.SearchAttempts,
	}
}

func (movieOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error {
	status := catalogac.MovieStatus()
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
		catalogac.Movie(name, ns).WithResourceVersion(resourceVersion).WithStatus(status))
	return err
}

type episodeOps struct{}

func (episodeOps) kind() commonv1.MediaKind { return commonv1.MediaKindEpisode }
func (episodeOps) recordsPendingGrab() bool { return true }

func (episodeOps) get(ctx context.Context, c client.Client, ns, name string) (client.Object, error) {
	var ep catalogv1alpha1.Episode
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ep); err != nil {
		return nil, err
	}
	return &ep, nil
}

// grabContext for an Episode reads the owning Series: EpisodeSpec carries only
// SeriesRef, SeasonNumber, EpisodeNumber and Monitored -- no QualityProfileRef,
// no DelayProfileRef, no Tags -- so episodes are governed by their series'
// profiles, exactly as the Episode reconciler already resolves its quality
// profile.
func (episodeOps) grabContext(ctx context.Context, c client.Client, obj client.Object) (grabContext, error) {
	ep, ok := obj.(*catalogv1alpha1.Episode)
	if !ok {
		return grabContext{}, fmt.Errorf("grab: expected *Episode, got %T", obj)
	}
	var s catalogv1alpha1.Series
	if err := c.Get(ctx, client.ObjectKey{Namespace: ep.Namespace, Name: ep.Spec.SeriesRef}, &s); err != nil {
		return grabContext{}, fmt.Errorf("grab: get series %q for episode %q: %w", ep.Spec.SeriesRef, ep.Name, err)
	}
	return grabContext{
		QualityProfileRef: s.Spec.QualityProfileRef,
		DelayProfileRef:   s.Spec.DelayProfileRef,
		Tags:              s.Spec.Tags,
	}, nil
}

func (episodeOps) workerStatus(obj client.Object) workerStatus {
	ep, ok := obj.(*catalogv1alpha1.Episode)
	if !ok {
		return workerStatus{}
	}
	return workerStatus{
		PendingGrab:    ep.Status.PendingGrab,
		LastSearchedAt: ep.Status.LastSearchedAt,
		SearchAttempts: ep.Status.SearchAttempts,
	}
}

func (episodeOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name, resourceVersion string, ws workerStatus) error {
	status := catalogac.EpisodeStatus()
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
		catalogac.Episode(name, ns).WithResourceVersion(resourceVersion).WithStatus(status))
	return err
}

func pendingGrabAC(pg *catalogv1alpha1.PendingGrab) *catalogac.PendingGrabApplyConfiguration {
	return catalogac.PendingGrab().
		WithReleaseTitle(pg.ReleaseTitle).
		WithProtocol(pg.Protocol).
		WithGrabAt(pg.GrabAt)
}

// isZeroAttempts keeps an all-zero Attempts out of the apply configuration.
// Re-declaring a field whose incumbent value is the zero value preserves
// nothing (there is nothing to release), and sending `searchAttempts: {}`
// would claim ownership of a field this call never meant to touch.
func isZeroAttempts(a commonv1.Attempts) bool {
	return a.Initial == nil && a.Latest == nil && a.Count == 0
}

// getTarget fetches the object a grab TARGETS, which -- unlike a status target
// -- may be a Series (the parent of a season pack). It is the Download's
// ownerReference and the subject of the release.grabbed event.
func getTarget(ctx context.Context, c client.Client, ns string, ref commonv1.MediaRef) (client.Object, error) {
	switch ref.Kind {
	case commonv1.MediaKindSeries:
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &s); err != nil {
			return nil, err
		}
		return &s, nil
	default:
		// Every other grab target is its own status target.
		ops, err := kindOpsFor(ref.Kind)
		if err != nil {
			return nil, err
		}
		return ops.get(ctx, c, ns, ref.Name)
	}
}

// workerStatusBackoff paces updateWorkerStatus's retries after a Conflict.
// It is client-go's DefaultBackoff (10ms growing fivefold, four steps, about
// 1.5s in all), which is long enough for an informer cache to deliver the
// write that caused the conflict, so the re-read sees it.
var workerStatusBackoff = retry.DefaultBackoff

// updateWorkerStatus is the only way this package writes status: a
// compare-and-swap over the whole k8s.ManagerCatalogarrGrab-owned set.
//
// It reads st, hands mutate the owned set as read, and -- when mutate reports
// a change -- declares the whole set with the read's resourceVersion as a
// precondition. If anything wrote the object in between (another replica's
// grab, a concurrent RecordSearchAttempt, the item's reconciler), the
// apiserver answers Conflict and the cycle re-reads and re-decides, so mutate
// may run more than once and must derive its change from the ws it is given.
//
// The precondition is what makes read-modify-declare safe across replicas.
// Every replica runs the search, grab and RSS consumers, and each read is a
// cache read: a search worker on one replica could read a Movie from before
// another replica's grab, then declare that pre-grab pendingGrab under this
// manager with ForceOwnership -- resurrecting a pending grab the other
// replica had just consumed, and with it Phase=Delayed. Re-reading
// "immediately before" the declare narrows that window and closes nothing; a
// read stale by a millisecond loses the same way. A conditional write cannot
// lose it, because the apiserver refuses the declare instead of applying it.
//
// A missing object comes back as the Get's NotFound, wrapped; callers decide
// whether that is an error.
func updateWorkerStatus(ctx context.Context, c client.Client, ns string, st commonv1.MediaRef, mutate func(*workerStatus) bool) error {
	ops, err := kindOpsFor(st.Kind)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(workerStatusBackoff, func() error {
		obj, err := ops.get(ctx, c, ns, st.Name)
		if err != nil {
			return err
		}
		ws := ops.workerStatus(obj)
		if !mutate(&ws) {
			return nil
		}
		return ops.applyWorkerStatus(ctx, c, ns, st.Name, obj.GetResourceVersion(), ws)
	})
}

// RecordSearchAttempt stamps status.lastSearchedAt and advances
// status.searchAttempts on one catalog item, under k8s.ManagerCatalogarrGrab.
//
// It exists so the search worker does not have to reimplement the
// read-modify-declare cycle workerStatus documents. Server-side apply replaces
// a manager's whole ownership set on every apply, so a second package sending
// only these two fields would release status.pendingGrab -- stranding an item
// at Delayed with nothing to clear it, or dropping a scheduled grab's record.
// This helper goes through updateWorkerStatus like every other write here, so
// it re-declares the whole set and does so conditionally: a grab or a
// Decide that lands between its read and its write makes it re-read rather
// than roll that write back.
//
// Without a writer for these fields wantedcron.Backoff never grows past its
// six-hour floor, so every twelve-hourly sweep re-searches every still-wanted
// item rather than following the documented 6h*2^n ladder.
//
// ref is the catalog item searched for -- movie, episode, album, book,
// audiobook or issue. A Series pack ref
// is expanded to its Keys, so one interactive pack search stamps every episode
// it covered. A missing object is not an error: it was deleted between the
// search and this write, and there is nothing left to record against.
//
// at is when the attempt was made. Attempts.Initial is stamped once, on the
// first recorded attempt, and never moved afterwards.
func RecordSearchAttempt(ctx context.Context, c client.Client, ns string, ref commonv1.MediaRef, at time.Time) error {
	statusTargets, err := StatusTargets(ref, ref.Keys)
	if err != nil {
		return err
	}
	stamp := metav1.NewTime(at)
	for _, st := range statusTargets {
		err := updateWorkerStatus(ctx, c, ns, st, func(ws *workerStatus) bool {
			ws.LastSearchedAt = &stamp
			if ws.SearchAttempts.Initial == nil {
				ws.SearchAttempts.Initial = &stamp
			}
			ws.SearchAttempts.Latest = &stamp
			ws.SearchAttempts.Count++
			return true
		})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return fmt.Errorf("grab: record a search attempt on %s/%s: %w", st.Kind, st.Name, err)
		}
	}
	return nil
}
