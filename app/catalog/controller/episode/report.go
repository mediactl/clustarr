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
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// downloadView is the Episode's view of its Series' grab entries (ADR-0019
// §6.1): the entries covering it -- the downloads stage's view this pass,
// else read off the Series in hand -- their active one (whose id is
// status.activeDownloadRef and whose phase is status.downloadPhase) and
// whether an audio donor entry covering it is open.
func downloadView(ctx context.Context, ep *catalogv1alpha1.Episode, s *catalogv1alpha1.Series) (*catalogv1alpha1.DownloadEntry, bool) {
	var covering []catalogv1alpha1.DownloadEntry
	if p := itempass.From(ctx); p != nil && p.Contribution.Viewed {
		covering = p.Contribution.Covering
	} else if s != nil {
		for i := range s.Status.Downloads {
			if rollup.CoversEpisode(&s.Status.Downloads[i], ep.Name, ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber) {
				covering = append(covering, s.Status.Downloads[i])
			}
		}
	}
	return rollup.ActiveEntry(covering, nil), rollup.DonorEntryOpen(covering, nil)
}

// audioGraft is ep's AudioGraft, nil when it has none.
func (r *Reconciler) audioGraft(ctx context.Context, ep *catalogv1alpha1.Episode) (*transcodev1alpha1.AudioGraft, error) {
	var g transcodev1alpha1.AudioGraft
	if err := r.Get(ctx, client.ObjectKey{Namespace: ep.Namespace, Name: k8s.AudioGraftName(ep.Name)}, &g); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &g, nil
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
