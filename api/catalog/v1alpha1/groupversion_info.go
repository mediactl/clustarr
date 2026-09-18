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
// catalog.clustarr.io v1alpha1 API group: the media catalog (Movie, Series,
// Episode, Artist, Album, Author, Book, Audiobook, Comic, Issue), the files
// backing it (MediaFile), the configuration that drives it (RootFolder,
// QualityProfile, DelayProfile, MetadataProvider, ImportList,
// ImportExclusion) and interactive Search (owned by catalogarr).
//
// +kubebuilder:object:generate=true
// +kubebuilder:ac:generate=true
// +kubebuilder:ac:output:package=../../applyconfiguration/catalog
// +groupName=catalog.clustarr.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "catalog.clustarr.io", Version: "v1alpha1"}

	// SchemeGroupVersion is an alias of GroupVersion kept for generated
	// clients and the applyconfiguration generator.
	SchemeGroupVersion = GroupVersion

	// SchemeBuilder is used to add Go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource takes an unqualified resource name and returns a Group-qualified
// GroupResource.
func Resource(resource string) schema.GroupResource {
	return GroupVersion.WithResource(resource).GroupResource()
}
