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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// workerStatus is the COMPLETE set of status fields
// k8s.ManagerCatalogarrWorker owns on a Movie or an Episode: spec §5's
// field-manager table assigns the worker activeDownloadRef, pendingGrab,
// lastSearchedAt and searchAttempts, and nothing else on those kinds.
//
// It is a value type rather than four separate patch methods because
// server-side apply replaces a field manager's ownership set on every apply
// instead of merging it. A patch that sent only pendingGrab would RELEASE
// activeDownloadRef, lastSearchedAt and searchAttempts -- which reads as
// "reset to zero" on the object -- and the search worker (Task C8), which
// shares this manager name and writes lastSearchedAt/searchAttempts, would
// release pendingGrab right back. The only safe shape is: read the live
// object, take the whole owned set off it, change the one thing this call
// means to change, and re-declare all four.
//
// Task C8 and anything else writing these fields under
// k8s.ManagerCatalogarrWorker must go through the same read-modify-declare
// cycle. Sending a subset is a data-loss bug that no test on a freshly
// created object can observe, because a blank object has nothing to release.
type workerStatus struct {
	ActiveDownloadRef *string
	PendingGrab       *catalogv1alpha1.PendingGrab
	LastSearchedAt    *metav1.Time
	SearchAttempts    commonv1.Attempts
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

	// get fetches the object, typed.
	get(ctx context.Context, c client.Client, ns, name string) (client.Object, error)

	// grabContext resolves the quality profile, delay profile and tags that
	// govern a grab for obj.
	grabContext(ctx context.Context, c client.Client, obj client.Object) (grabContext, error)

	// workerStatus reads the worker-owned status fields off a fetched object.
	workerStatus(obj client.Object) workerStatus

	// applyWorkerStatus declares the whole worker-owned set under
	// k8s.ManagerCatalogarrWorker. It never sends status.phase: Phase is
	// k8s.ManagerCatalogarr's, recomputed by the Movie/Episode reconciler
	// from the fields written here.
	applyWorkerStatus(ctx context.Context, c client.Client, ns, name string, ws workerStatus) error
}

// kindOpsFor returns the operations for a status-target kind. Only movie and
// episode have a PendingGrab/ActiveDownloadRef status to write, which is what
// bounds M1's scope; a Series is a grab TARGET but never a status target, so
// it is not here (see StatusTargets).
func kindOpsFor(kind commonv1.MediaKind) (kindOps, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return movieOps{}, nil
	case commonv1.MediaKindEpisode:
		return episodeOps{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKind, kind)
	}
}

type movieOps struct{}

func (movieOps) kind() commonv1.MediaKind { return commonv1.MediaKindMovie }

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
		ActiveDownloadRef: m.Status.ActiveDownloadRef,
		PendingGrab:       m.Status.PendingGrab,
		LastSearchedAt:    m.Status.LastSearchedAt,
		SearchAttempts:    m.Status.SearchAttempts,
	}
}

func (movieOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name string, ws workerStatus) error {
	status := catalogac.MovieStatus()
	if ws.ActiveDownloadRef != nil {
		status = status.WithActiveDownloadRef(*ws.ActiveDownloadRef)
	}
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Movie(name, ns).WithStatus(status))
	return err
}

type episodeOps struct{}

func (episodeOps) kind() commonv1.MediaKind { return commonv1.MediaKindEpisode }

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
		ActiveDownloadRef: ep.Status.ActiveDownloadRef,
		PendingGrab:       ep.Status.PendingGrab,
		LastSearchedAt:    ep.Status.LastSearchedAt,
		SearchAttempts:    ep.Status.SearchAttempts,
	}
}

func (episodeOps) applyWorkerStatus(ctx context.Context, c client.Client, ns, name string, ws workerStatus) error {
	status := catalogac.EpisodeStatus()
	if ws.ActiveDownloadRef != nil {
		status = status.WithActiveDownloadRef(*ws.ActiveDownloadRef)
	}
	if ws.PendingGrab != nil {
		status = status.WithPendingGrab(pendingGrabAC(ws.PendingGrab))
	}
	if ws.LastSearchedAt != nil {
		status = status.WithLastSearchedAt(*ws.LastSearchedAt)
	}
	if !isZeroAttempts(ws.SearchAttempts) {
		status = status.WithSearchAttempts(ws.SearchAttempts)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Episode(name, ns).WithStatus(status))
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
	case commonv1.MediaKindMovie:
		return movieOps{}.get(ctx, c, ns, ref.Name)
	case commonv1.MediaKindEpisode:
		return episodeOps{}.get(ctx, c, ns, ref.Name)
	case commonv1.MediaKindSeries:
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &s); err != nil {
			return nil, err
		}
		return &s, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedKind, ref.Kind)
	}
}
