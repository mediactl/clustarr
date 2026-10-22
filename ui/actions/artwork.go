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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// SetArtwork writes an item's artwork overrides -- an image set's
// (docs/superpowers/specs/2026-10-21-image-sets-design.md) -- as one JSON
// merge patch of spec.artwork and, for a series, spec.seasonArtwork, made as
// [FieldManager]. A merge patch replaces a list whole, so the caller passes
// the complete lists it wants, the item's kept overrides merged in. A nil
// seasonArtwork on a series leaves that list as it is; on any other kind it
// must be nil. A movie or a series only: the kinds with a Plex provider.
func SetArtwork(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string,
	artwork []catalogv1alpha1.ArtworkOverride, seasonArtwork []catalogv1alpha1.SeasonArtworkOverride,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetArtwork")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	switch kind {
	case commonv1.MediaKindMovie:
		if seasonArtwork != nil {
			err := fmt.Errorf("%w: a movie has no seasons", ErrInvalid)
			tracing.RecordError(span, err)
			return nil, err
		}
	case commonv1.MediaKindSeries:
	default:
		err := fmt.Errorf("%w: a %s takes no image set", ErrInvalid, kind)
		tracing.RecordError(span, err)
		return nil, err
	}
	if len(artwork) == 0 && len(seasonArtwork) == 0 {
		err := fmt.Errorf("%w: no images to set", ErrInvalid)
		tracing.RecordError(span, err)
		return nil, err
	}
	spec := map[string]any{}
	if artwork != nil {
		spec["artwork"] = artwork
	}
	if seasonArtwork != nil {
		spec["seasonArtwork"] = seasonArtwork
	}
	raw, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		err = fmt.Errorf("actions: encode artwork patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: set artwork on %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: artwork set",
		"namespace", namespace, "kind", kind, "item", name, "images", len(artwork), "seasonImages", len(seasonArtwork))
	return obj, nil
}

// SetArtwork is [SetArtwork] over the Actions' writer.
func (a *Actions) SetArtwork(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string,
	artwork []catalogv1alpha1.ArtworkOverride, seasonArtwork []catalogv1alpha1.SeasonArtworkOverride,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetArtwork(ctx, a.w, namespace, kind, name, artwork, seasonArtwork)
}
