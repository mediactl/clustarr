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
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// EditRequest is what the item page's Edit dialog saves (Radarr's Edit
// Movie, Sonarr's Edit Series): the item's quality profile, whether it is
// monitored, and the one more field each video kind has.
type EditRequest struct {
	// QualityProfileRef is the QualityProfile to rank the item against.
	// Required: every editable kind's spec requires one.
	QualityProfileRef string
	// Monitored, when set, is written too.
	Monitored *bool
	// SeriesType is a series' spec.seriesType (standard, daily, anime);
	// empty leaves it. Set on any other kind, the request is refused.
	SeriesType string
	// MinimumAvailability is a movie's spec.minimumAvailability (tba,
	// announced, inCinemas, released); empty leaves it. Set on any other
	// kind, the request is refused.
	MinimumAvailability string
	// Seasons, when set, is a series' whole spec.seasons, sent under
	// ResourceVersion (the series as read): the Edit dialog switching an
	// unmonitored series to monitored monitors every season but the
	// specials ([AllSeasonsMonitored]). Set on any other kind, the request
	// is refused.
	Seasons         []catalogv1alpha1.SeasonSpec
	ResourceVersion string
}

// editables are the kinds with a top-level spec.qualityProfileRef: the
// ones the Edit dialog is for. An Episode, Album, Book or Issue ranks under
// its parent's profile and has no dialog.
var editables = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
	commonv1.MediaKindAuthor, commonv1.MediaKindComic, commonv1.MediaKindAudiobook,
}

// EditableKinds returns, sorted, every kind [EditItem] accepts.
func EditableKinds() []commonv1.MediaKind {
	kinds := slices.Clone(editables)
	slices.Sort(kinds)
	return kinds
}

var (
	seriesTypes           = []string{"standard", "daily", "anime"}
	minimumAvailabilities = []string{"tba", "announced", "inCinemas", "released"}
)

// EditItem saves the Edit dialog: one JSON merge patch of the spec leaves
// req names on the catalog item kind/name in namespace, made as
// [FieldManager], returning the object as the apiserver returned it. A
// field req leaves empty is not sent, so it keeps its value; the patch is
// exactly {"spec":{...}} of what is sent. A missing item is the
// apiserver's NotFound, wrapped; nothing is ever created.
func EditItem(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string, req EditRequest,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.EditItem")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if !slices.Contains(editables, kind) {
		err := fmt.Errorf("%w: a %s has no quality profile of its own", ErrInvalid, kind)
		tracing.RecordError(span, err)
		return nil, err
	}
	if req.QualityProfileRef == "" {
		err := fmt.Errorf("%w: a quality profile is required", ErrInvalid)
		tracing.RecordError(span, err)
		return nil, err
	}
	spec := map[string]any{"qualityProfileRef": req.QualityProfileRef}
	if req.Monitored != nil {
		spec["monitored"] = *req.Monitored
	}
	if req.SeriesType != "" {
		if kind != commonv1.MediaKindSeries || !slices.Contains(seriesTypes, req.SeriesType) {
			err := fmt.Errorf("%w: series type %q on a %s", ErrInvalid, req.SeriesType, kind)
			tracing.RecordError(span, err)
			return nil, err
		}
		spec["seriesType"] = req.SeriesType
	}
	if req.MinimumAvailability != "" {
		if kind != commonv1.MediaKindMovie || !slices.Contains(minimumAvailabilities, req.MinimumAvailability) {
			err := fmt.Errorf("%w: minimum availability %q on a %s", ErrInvalid, req.MinimumAvailability, kind)
			tracing.RecordError(span, err)
			return nil, err
		}
		spec["minimumAvailability"] = req.MinimumAvailability
	}
	body := map[string]any{"spec": spec}
	if req.Seasons != nil {
		if kind != commonv1.MediaKindSeries || req.ResourceVersion == "" {
			err := fmt.Errorf("%w: seasons need a series as read, not a %s", ErrInvalid, kind)
			tracing.RecordError(span, err)
			return nil, err
		}
		spec["seasons"] = req.Seasons
		body["metadata"] = map[string]any{"resourceVersion": req.ResourceVersion}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		err = fmt.Errorf("actions: encode edit patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: edit %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: item edited",
		"namespace", namespace, "kind", kind, "item", name, "qualityProfile", req.QualityProfileRef)
	return obj, nil
}

// EditItem is [EditItem] over the Actions' writer.
func (a *Actions) EditItem(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string, req EditRequest,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return EditItem(ctx, a.w, namespace, kind, name, req)
}
