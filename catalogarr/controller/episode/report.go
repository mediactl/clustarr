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

package episode

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// activeDownload is the Download status.activeDownloadRef names (ruling
// R-5): the oldest non-terminal Download covering ep that the right object
// owns -- ep itself for a single-episode grab, ep's Series (s; nil when it
// is gone) for a season pack, which the grab path creates with the Series as
// owner and the covered Episodes in spec.target.keys. Matching the owner's
// UID keeps a Download left by a deleted-and-recreated namesake from being
// adopted.
func (r *Reconciler) activeDownload(ctx context.Context, ep *catalogv1alpha1.Episode, s *catalogv1alpha1.Series) (*downloadv1alpha1.Download, error) {
	var list downloadv1alpha1.DownloadList
	if err := r.List(ctx, &list, client.InNamespace(ep.Namespace), client.MatchingFields{downloadByEpisodeIndexKey: ep.Name}); err != nil {
		return nil, err
	}
	return rollup.ActiveDownload(list.Items, func(d *downloadv1alpha1.Download) bool {
		switch d.Spec.Target.Kind {
		case commonv1.MediaKindEpisode:
			return d.Spec.Target.Name == ep.Name && k8s.IsOwnedBy(d, ep)
		case commonv1.MediaKindSeries:
			return s != nil && d.Spec.Target.Name == s.Name && k8s.IsOwnedBy(d, s)
		default:
			return false
		}
	}), nil
}

// publishFile publishes one MediaFileEvent for ep. It is best effort -- the
// event is history, the status is the record -- and a nil Bus publishes
// nothing.
func (r *Reconciler) publishFile(ctx context.Context, ep *catalogv1alpha1.Episode, action, file string, mf *catalogv1alpha1.MediaFile, now time.Time) {
	if r.Bus == nil {
		return
	}
	log := logging.FromContext(ctx)
	media := commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep.Name}
	subject, env, err := rollup.MediaFileEvent(ep, media, action, file, mf, now)
	if err != nil {
		log.Warn("episode: could not encode a media-file event", "action", action, "mediafile", file, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("episode: could not publish a media-file event", "action", action, "mediafile", file, "subject", subject, "error", err)
	}
}

// normal and warn emit a Kubernetes Event on ep, on an edge only; see
// rollup.Transitioned.
func (r *Reconciler) normal(ep *catalogv1alpha1.Episode, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(ep, nil, corev1.EventTypeNormal, reason, "Reconcile", format, args...)
	}
}

func (r *Reconciler) warn(ep *catalogv1alpha1.Episode, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(ep, nil, corev1.EventTypeWarning, reason, "Reconcile", format, args...)
	}
}
