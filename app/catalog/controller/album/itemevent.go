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

package album

import (
	"context"
	"time"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// publishItem publishes one catalog ItemEvent for alb -- spec §5's
// clustarr.evt.catalog.<kind>.<added|updated|deleted>.<uid>, which the
// history sink (app/catalog/history) turns into an Event on the Album. It is
// built by rollup.ItemEvent, so its envelope id is the same function of the
// edge for every kind, and it is best effort, as the Movie reconciler's is:
// the event is history and the status is the record, so a failed publish is
// logged and the reconcile carries on. A nil Bus publishes nothing.
func (r *Reconciler) publishItem(ctx context.Context, alb *catalogv1alpha1.Album, action string, now time.Time) {
	if r.Bus == nil {
		return
	}
	f := rollup.ItemFields{
		Monitored: ptr.Deref(alb.Spec.Monitored, true),
		IDs:       map[string]string{pkgmetadata.KeyMBReleaseGroup: alb.Spec.ReleaseGroupID},
	}
	if md := alb.Status.Metadata; md != nil {
		f.Title, f.Year = md.Title, int32(ReleaseYear(md.ReleaseDate))
	}
	log := logging.FromContext(ctx)
	subject, env, err := rollup.ItemEvent(alb, commonv1.MediaKindAlbum, action, f, now)
	if err != nil {
		log.Warn("album: could not encode a catalog item event", "action", action, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("album: could not publish a catalog item event", "action", action, "subject", subject, "error", err)
	}
}

// publishFile publishes one MediaFileEvent for alb -- spec §5's
// clustarr.evt.catalog.mediafile.<imported|replaced|deleted>.<uid>, where
// <uid> is the album's -- through rollup.MediaFileEvent, as the Movie and
// Episode reconcilers do. It is best effort for the same reason publishItem
// is, and a nil Bus publishes nothing.
func (r *Reconciler) publishFile(ctx context.Context, alb *catalogv1alpha1.Album, action, file string, mf *catalogv1alpha1.MediaFile, now time.Time) {
	if r.Bus == nil {
		return
	}
	log := logging.FromContext(ctx)
	media := commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name}
	subject, env, err := rollup.MediaFileEvent(alb, media, action, file, mf, now)
	if err != nil {
		log.Warn("album: could not encode a media-file event", "action", action, "mediafile", file, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("album: could not publish a media-file event", "action", action, "mediafile", file, "subject", subject, "error", err)
	}
}
