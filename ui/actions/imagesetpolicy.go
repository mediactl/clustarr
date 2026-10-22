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
	"regexp"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// setIDRE is a MediUX set id: the CRD's pattern for spec.imageSetID.
var setIDRE = regexp.MustCompile(`^[0-9]{1,64}$`)

// SetImageSetPolicy writes how the mediux MetadataProvider treats an item
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md)
// as one JSON merge patch of spec.imageSet and spec.imageSetID, made as
// [FieldManager]: auto chooses a set, none opts the item out (its
// status.imageSet is cleared), and setID pins one set by its MediUX id
// (empty unpins). A movie or a series only.
func SetImageSetPolicy(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string,
	mode catalogv1alpha1.ImageSetMode, setID string,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetImageSetPolicy")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if kind != commonv1.MediaKindMovie && kind != commonv1.MediaKindSeries {
		err := fmt.Errorf("%w: a %s takes no image set", ErrInvalid, kind)
		tracing.RecordError(span, err)
		return nil, err
	}
	if mode != catalogv1alpha1.ImageSetAuto && mode != catalogv1alpha1.ImageSetNone {
		err := fmt.Errorf("%w: image set mode %q is not auto or none", ErrInvalid, mode)
		tracing.RecordError(span, err)
		return nil, err
	}
	if setID != "" && !setIDRE.MatchString(setID) {
		err := fmt.Errorf("%w: %q is not a MediUX set id", ErrInvalid, setID)
		tracing.RecordError(span, err)
		return nil, err
	}
	spec := map[string]any{"imageSet": nil, "imageSetID": nil}
	if mode == catalogv1alpha1.ImageSetNone {
		spec["imageSet"] = mode
	}
	if setID != "" {
		spec["imageSetID"] = setID
	}
	raw, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		err = fmt.Errorf("actions: encode image set policy patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: set image set policy on %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: image set policy set",
		"namespace", namespace, "kind", kind, "item", name, "mode", mode, "setID", setID)
	return obj, nil
}

// SetImageSetPolicy is [SetImageSetPolicy] over the Actions' writer.
func (a *Actions) SetImageSetPolicy(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string,
	mode catalogv1alpha1.ImageSetMode, setID string,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SetImageSetPolicy(ctx, a.w, namespace, kind, name, mode, setID)
}
