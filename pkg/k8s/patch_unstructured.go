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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PatchStatusUnstructured is PatchStatus for a status the caller holds as
// field names and values rather than a generated apply configuration: the
// remediation loop's item sets (ADR-0019 §7.0, ruling R5), one manager's
// complete set on an item kind none of whose renderers it shares. u carries
// apiVersion, kind, name, namespace, the status to apply and, when it should
// be a precondition, metadata.resourceVersion. It applies with
// force-ownership on, as every apply in this package does, and returns the
// apiserver's answer (its resourceVersion is the next apply's
// precondition).
func PatchStatusUnstructured(ctx context.Context, c client.Client, fm FieldManager, u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if err := fm.Validate(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("k8s: nil client")
	}
	if u == nil {
		return nil, fmt.Errorf("k8s: nil unstructured status")
	}
	switch {
	case u.GetAPIVersion() == "":
		return nil, fmt.Errorf("k8s: unstructured status has no apiVersion")
	case u.GetKind() == "":
		return nil, fmt.Errorf("k8s: unstructured status has no kind")
	case u.GetName() == "":
		return nil, fmt.Errorf("k8s: unstructured status has no name")
	}
	ac := client.ApplyConfigurationFromUnstructured(u)
	if err := c.Status().Apply(ctx, ac, client.FieldOwner(fm.String()), client.ForceOwnership); err != nil {
		return nil, fmt.Errorf("k8s: apply %s/%s status as %q: %w", u.GetNamespace(), u.GetName(), fm, err)
	}
	return u, nil
}
