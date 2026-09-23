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

package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// refreshable is every kind with a status.metadata of its own -- the kinds
// catalogarr's refresher watches. Episode and Issue are refreshed through
// their parent and are refused here.
var refreshable = map[commonv1.MediaKind]bool{
	commonv1.MediaKindMovie: true, commonv1.MediaKindSeries: true,
	commonv1.MediaKindArtist: true, commonv1.MediaKindAlbum: true,
	commonv1.MediaKindAuthor: true, commonv1.MediaKindBook: true,
	commonv1.MediaKindAudiobook: true, commonv1.MediaKindComic: true,
}

// refreshPatch is the merge patch RefreshMetadata sends: the one annotation,
// nothing else.
type refreshPatch struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
}

// RefreshMetadata is the "Refresh metadata" action (design
// 2026-09-23-library-page-design, "Metadata refresh"): it writes
// catalogv1alpha1.AnnotationRefreshMetadata with at's Unix time on the
// item, as a merge patch under [FieldManager], and catalogarr's refresher
// does the rest. It uses the item's existing patch grant, so Grants() is
// unchanged.
func RefreshMetadata(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string, at time.Time,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.RefreshMetadata")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if !refreshable[kind] {
		err := fmt.Errorf("%w: a %s has no metadata of its own to refresh; refresh its parent", ErrInvalid, kind)
		tracing.RecordError(span, err)
		return nil, err
	}

	var body refreshPatch
	body.Metadata.Annotations = map[string]string{
		catalogv1alpha1.AnnotationRefreshMetadata: strconv.FormatInt(at.Unix(), 10),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		err = fmt.Errorf("actions: encode refresh annotation patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: request metadata refresh of %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: metadata refresh requested",
		"namespace", namespace, "kind", kind, "item", name, "epoch", at.Unix())
	return obj, nil
}

// RefreshMetadata is [RefreshMetadata] over the Actions' writer, at the
// current time; ErrNoWriter without one.
func (a *Actions) RefreshMetadata(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return RefreshMetadata(ctx, a.w, namespace, kind, name, time.Now())
}
