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

package audiobook

import (
	"context"
	"time"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// publishItem publishes one catalog ItemEvent for m -- spec §5's
// clustarr.evt.catalog.<kind>.<added|updated|deleted>.<uid>, which the
// history sink (catalogarr/history) turns into an Event on the Audiobook. It is
// built by rollup.ItemEvent, so its envelope id is the same function of the
// edge for every kind, and it is best effort, as the Movie reconciler's is:
// the event is history and the status is the record, so a failed publish is
// logged and the reconcile carries on. A nil Bus publishes nothing.
func (r *Reconciler) publishItem(ctx context.Context, m *catalogv1alpha1.Audiobook, action string, now time.Time) {
	if r.Bus == nil {
		return
	}
	f := rollup.ItemFields{
		Monitored: ptr.Deref(m.Spec.Monitored, true),
		IDs:       map[string]string{metadata.KeyASIN: m.Spec.ASIN},
		AddSource: m.Spec.Source,
	}
	if md := m.Status.Metadata; md != nil {
		f.Title = md.Title
		if md.ReleaseDate != nil {
			f.Year = int32(md.ReleaseDate.UTC().Year())
		}
	}
	log := logging.FromContext(ctx)
	subject, env, err := rollup.ItemEvent(m, commonv1.MediaKindAudiobook, action, f, now)
	if err != nil {
		log.Warn("audiobook: could not encode a catalog item event", "action", action, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("audiobook: could not publish a catalog item event", "action", action, "subject", subject, "error", err)
	}
}

// publishFile publishes one MediaFileEvent for m -- spec §5's
// clustarr.evt.catalog.mediafile.<imported|replaced|deleted>.<uid>, where
// <uid> is the audiobook's -- through rollup.MediaFileEvent, as the Movie and
// Episode reconcilers do. It is best effort for the same reason publishItem
// is, and a nil Bus publishes nothing.
func (r *Reconciler) publishFile(ctx context.Context, m *catalogv1alpha1.Audiobook, action, file string, mf *catalogv1alpha1.MediaFile, now time.Time) {
	if r.Bus == nil {
		return
	}
	log := logging.FromContext(ctx)
	media := commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: m.Name}
	subject, env, err := rollup.MediaFileEvent(m, media, action, file, mf, now)
	if err != nil {
		log.Warn("audiobook: could not encode a media-file event", "action", action, "mediafile", file, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("audiobook: could not publish a media-file event", "action", action, "mediafile", file, "subject", subject, "error", err)
	}
}
