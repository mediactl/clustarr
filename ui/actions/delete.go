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

// RequestDelete is the library Delete button (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md): a merge patch of the item's
// annotations under [FieldManager] -- catalog.clustarr.io/delete ("files"
// with files, else "records"), delete-add-exclusion "true" with exclude
// (removed otherwise), and delete-error removed, so a Retry renews the
// request. importarr carries it out. It uses the item's existing patch
// grant; the ui never deletes.
func RequestDelete(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string, files, exclude bool,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.RequestDelete")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if !slices.Contains(catalogv1alpha1.DeletableKinds(), kind) {
		err := fmt.Errorf("%w: a %s is deleted with its parent", ErrInvalid, kind)
		tracing.RecordError(span, err)
		return nil, err
	}
	mode := catalogv1alpha1.DeleteRecords
	if files {
		mode = catalogv1alpha1.DeleteFiles
	}
	annotations := map[string]any{
		catalogv1alpha1.AnnotationDelete:             mode,
		catalogv1alpha1.AnnotationDeleteAddExclusion: nil,
		catalogv1alpha1.AnnotationDeleteError:        nil,
	}
	if exclude {
		annotations[catalogv1alpha1.AnnotationDeleteAddExclusion] = "true"
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": annotations}})
	if err != nil {
		err = fmt.Errorf("actions: encode delete request patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}
	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: request delete of %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: delete requested",
		"namespace", namespace, "kind", string(kind), "name", name, "files", files, "exclude", exclude)
	return obj, nil
}

// RequestDelete is [RequestDelete] over the Actions' own client.
func (a *Actions) RequestDelete(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string, files, exclude bool,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return RequestDelete(ctx, a.w, namespace, kind, name, files, exclude)
}

// CancelDelete withdraws a pending or refused library delete: a merge patch
// removing the three delete annotations under [FieldManager]. A delete
// importarr has already carried out is not undone; one it is carrying out
// finishes.
func CancelDelete(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.CancelDelete")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{
		catalogv1alpha1.AnnotationDelete:             nil,
		catalogv1alpha1.AnnotationDeleteAddExclusion: nil,
		catalogv1alpha1.AnnotationDeleteError:        nil,
	}}})
	if err != nil {
		err = fmt.Errorf("actions: encode delete cancel patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}
	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: cancel delete of %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: delete cancelled", "namespace", namespace, "kind", string(kind), "name", name)
	return obj, nil
}

// CancelDelete is [CancelDelete] over the Actions' own client.
func (a *Actions) CancelDelete(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return CancelDelete(ctx, a.w, namespace, kind, name)
}
