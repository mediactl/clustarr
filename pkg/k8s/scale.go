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

package k8s

import (
	"context"
	"fmt"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ScaleTo sets obj's scale subresource to replicas under fm: a Get of the
// Scale, then an Update carrying its resourceVersion, so a concurrent
// writer (the HPA controller) is a Conflict for the caller to retry rather
// than a lost update. It does nothing when the Scale already reads
// replicas.
//
// It lives here because .golangci.yml's forbidigo rule matches
// client.SubResourceWriter.Update by its resolved name and cannot tell a
// scale write from a status write; pkg/k8s is the one package excluded
// from it, as it is for PatchStatus.
func ScaleTo(ctx context.Context, c client.Client, fm FieldManager, obj client.Object, replicas int32) error {
	if err := fm.Validate(); err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("k8s: nil client")
	}
	scale := &autoscalingv1.Scale{}
	if err := c.SubResource("scale").Get(ctx, obj, scale); err != nil {
		return fmt.Errorf("k8s: read the scale of %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}
	if scale.Spec.Replicas == replicas {
		return nil
	}
	scale.Spec.Replicas = replicas
	if err := c.SubResource("scale").Update(ctx, obj,
		client.WithSubResourceBody(scale), client.FieldOwner(fm.String())); err != nil {
		return fmt.Errorf("k8s: scale %s/%s to %d as %q: %w", obj.GetNamespace(), obj.GetName(), replicas, fm, err)
	}
	return nil
}
