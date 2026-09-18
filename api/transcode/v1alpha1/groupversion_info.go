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

// Package v1alpha1 contains the API Schema definitions for the
// transcode.clustarr.io v1alpha1 API group (owner: squasharr).
//
// +kubebuilder:object:generate=true
// +kubebuilder:ac:generate=true
// +kubebuilder:ac:output:package=../../applyconfiguration/transcode
// +groupName=transcode.clustarr.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "transcode.clustarr.io", Version: "v1alpha1"}

	// SchemeGroupVersion is an alias of GroupVersion kept for generators
	// (client-gen, applyconfiguration-gen) that expect this name.
	SchemeGroupVersion = GroupVersion

	// SchemeBuilder is used to add Go types to the GroupVersionKind scheme.
	//
	// controller-runtime deprecates this helper because an api package
	// should not depend on controller-runtime. §3 requires SchemeBuilder and
	// AddToScheme on every group, and replacing it with
	// runtime.NewSchemeBuilder would change the exported type that every
	// *_types.go init() registers into, so the deprecation is accepted here
	// and revisited if the helper is removed.
	//nolint:staticcheck // SA1019: see above.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource takes an unqualified resource name and returns a Group-qualified
// GroupResource.
func Resource(resource string) schema.GroupResource {
	return GroupVersion.WithResource(resource).GroupResource()
}
