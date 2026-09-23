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

package movie

import (
	"context"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// activeDownload is the Download status.activeDownloadRef names (ruling
// R-5): the oldest non-terminal Download whose spec.target is this Movie and
// which this Movie owns, by UID. Matching the owner's UID as well as the
// target's name keeps a Download that belonged to a deleted Movie of the same
// name -- one the garbage collector has not reached yet -- from being
// adopted by its successor.
func (r *Reconciler) activeDownload(ctx context.Context, m *catalogv1alpha1.Movie) (*downloadv1alpha1.Download, error) {
	var list downloadv1alpha1.DownloadList
	if err := r.List(ctx, &list, client.InNamespace(m.Namespace), client.MatchingFields{downloadByMovieIndexKey: m.Name}); err != nil {
		return nil, err
	}
	return rollup.ActiveDownload(list.Items, func(d *downloadv1alpha1.Download) bool {
		return k8s.IsOwnedBy(d, m)
	}), nil
}

// publishItem publishes one ItemEvent for m. It is best effort: the event is
// history and the status is the record, so a failed publish is logged and
// the reconcile carries on -- the same stance grabarr's DownloadEvent
// producer takes. A nil Bus publishes nothing.
func (r *Reconciler) publishItem(ctx context.Context, m *catalogv1alpha1.Movie, action string, now time.Time) {
	if r.Bus == nil {
		return
	}
	f := rollup.ItemFields{
		Monitored: ptr.Deref(m.Spec.Monitored, true),
		AddSource: m.Spec.Source,
		IDs:       map[string]string{metadata.KeyTMDB: strconv.FormatInt(m.Spec.TmdbID, 10)},
	}
	if md := m.Status.Metadata; md != nil {
		f.Title, f.Year = md.Title, md.Year
		if imdb := md.ExternalIDs[metadata.KeyIMDb]; imdb != "" {
			f.IDs[metadata.KeyIMDb] = imdb
		}
	}
	subject, env, err := rollup.ItemEvent(m, commonv1.MediaKindMovie, action, f, now)
	r.publish(ctx, subject, env, err, "action", action)
}

// publishFile publishes one MediaFileEvent for m; see rollup.FileTransition
// for the edges and publishItem for why it is best effort.
func (r *Reconciler) publishFile(ctx context.Context, m *catalogv1alpha1.Movie, action, file string, mf *catalogv1alpha1.MediaFile, now time.Time) {
	if r.Bus == nil {
		return
	}
	media := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name}
	subject, env, err := rollup.MediaFileEvent(m, media, action, file, mf, now)
	r.publish(ctx, subject, env, err, "action", action, "mediafile", file)
}

func (r *Reconciler) publish(ctx context.Context, subject string, env *events.Envelope, buildErr error, attrs ...any) {
	log := logging.FromContext(ctx)
	if buildErr != nil {
		log.Warn("movie: could not encode a catalog event", append(attrs, "error", buildErr)...)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("movie: could not publish a catalog event", append(attrs, "subject", subject, "error", err)...)
	}
}

// normal and warn emit a Kubernetes Event on m through the recorder
// catalogarr hands in (mgr.GetEventRecorder("movie")). Every call site emits
// on an edge, never on a steady state; see rollup.Transitioned.
func (r *Reconciler) normal(m *catalogv1alpha1.Movie, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(m, nil, corev1.EventTypeNormal, reason, "Reconcile", format, args...)
	}
}

func (r *Reconciler) warn(m *catalogv1alpha1.Movie, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(m, nil, corev1.EventTypeWarning, reason, "Reconcile", format, args...)
	}
}
