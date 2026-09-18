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
	"reflect"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplyConfiguration is what controller-runtime v0.25.1 actually requires of a
// value handed to client.Apply or client.Status().Apply.
//
// The public signature is runtime.ApplyConfiguration, which only carries the
// marker method IsApplyConfiguration. The client then type-asserts to an
// unexported interface with these four accessors to derive the GroupVersionKind
// (from apiVersion/kind) and the request path (from name/namespace); a value
// that satisfies runtime.ApplyConfiguration but not the accessors fails at
// runtime with "is a runtime.ApplyConfiguration but not an applyConfiguration".
// Restating the accessors here turns that into a compile-time constraint for
// callers and a clear error for the reflective paths.
//
// Every configuration generated into api/applyconfiguration satisfies it: the
// generated constructors (for example catalogac.Movie(name, namespace)) set
// name, namespace, kind and apiVersion up front.
type ApplyConfiguration interface {
	runtime.ApplyConfiguration

	GetName() *string
	GetNamespace() *string
	GetKind() *string
	GetAPIVersion() *string
}

// PatchStatus applies ac to the status subresource of the object it names,
// under the field manager fm and with force-ownership on.
//
// This is the only status write in Clustarr; §3's single-writer rule and the
// forbidigo rules in .golangci.yml exist to keep it that way. Force-ownership
// is deliberate and always on: a controller that re-applies a field it already
// owns must win, and the conflict it would otherwise get is never actionable
// because §5 gives each field exactly one legitimate manager. Two managers
// fighting over one field is a spec bug, not something to resolve at runtime.
//
// ac is round-tripped: the apiserver's response is decoded back into it, so on
// success ac holds the accepted configuration.
//
// The generic parameter exists so callers keep their concrete type:
//
//	movie := catalogac.Movie(name, ns).WithStatus(...)
//	movie, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, movie)
func PatchStatus[T ApplyConfiguration](
	ctx context.Context,
	c client.Client,
	fm FieldManager,
	ac T,
	opts ...client.SubResourceApplyOption,
) (T, error) {
	if err := fm.Validate(); err != nil {
		return ac, err
	}
	if c == nil {
		return ac, fmt.Errorf("k8s: nil client")
	}
	if err := validateApplyConfiguration(ac); err != nil {
		return ac, err
	}

	all := make([]client.SubResourceApplyOption, 0, len(opts)+2)
	all = append(all, client.FieldOwner(fm.String()), client.ForceOwnership)
	all = append(all, opts...)

	if err := c.Status().Apply(ctx, ac, all...); err != nil {
		return ac, fmt.Errorf("k8s: apply %s/%s status as %q: %w",
			derefOr(ac.GetNamespace(), ""), derefOr(ac.GetName(), ""), fm, err)
	}
	return ac, nil
}

// Apply is PatchStatus' counterpart for the main resource. Controllers use it
// to create or update the objects they own -- a squasharr TranscodeJob, a
// grabarr StatefulSet -- without a read-modify-write race. It is not subject to
// the single-writer rule, but it shares the field-manager vocabulary so
// `kubectl get -o yaml --show-managed-fields` reads the same either way.
func Apply[T ApplyConfiguration](
	ctx context.Context,
	c client.Client,
	fm FieldManager,
	ac T,
	opts ...client.ApplyOption,
) (T, error) {
	if err := fm.Validate(); err != nil {
		return ac, err
	}
	if c == nil {
		return ac, fmt.Errorf("k8s: nil client")
	}
	if err := validateApplyConfiguration(ac); err != nil {
		return ac, err
	}

	all := make([]client.ApplyOption, 0, len(opts)+2)
	all = append(all, client.FieldOwner(fm.String()), client.ForceOwnership)
	all = append(all, opts...)

	if err := c.Apply(ctx, ac, all...); err != nil {
		return ac, fmt.Errorf("k8s: apply %s/%s as %q: %w",
			derefOr(ac.GetNamespace(), ""), derefOr(ac.GetName(), ""), fm, err)
	}
	return ac, nil
}

// validateApplyConfiguration rejects the configurations the client would only
// reject after a round trip, or would silently send to the wrong path.
func validateApplyConfiguration(ac ApplyConfiguration) error {
	// A typed nil (for example (*MovieApplyConfiguration)(nil)) satisfies the
	// interface but panics in the generated getters, so check both shapes.
	if ac == nil {
		return fmt.Errorf("k8s: nil apply configuration")
	}
	if v := reflect.ValueOf(ac); v.Kind() == reflect.Pointer && v.IsNil() {
		return fmt.Errorf("k8s: nil %T apply configuration", ac)
	}

	switch {
	case derefOr(ac.GetAPIVersion(), "") == "":
		return fmt.Errorf("k8s: apply configuration %T has no apiVersion", ac)
	case derefOr(ac.GetKind(), "") == "":
		return fmt.Errorf("k8s: apply configuration %T has no kind", ac)
	case derefOr(ac.GetName(), "") == "":
		return fmt.Errorf("k8s: apply configuration %T has no name", ac)
	}
	return nil
}

func derefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
