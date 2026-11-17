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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A PlexHubLayout is one Plex library's hubs declared as a resource
// (docs/superpowers/specs/2026-11-16-plex-hub-layouts-design.md): Plex
// Web's "Manage Recommendations" -- the order of the library's hubs and
// where each shows. Like a PlexCollection, its spec is the UI's, kubectl's
// or Terraform's, and cluster-plex reconciles it into Plex and is the only
// writer of its status (field manager cluster-plex); clustarr's manager
// never reconciles or writes it. It carries no finalizer: deleting one
// leaves Plex's hubs as they are.

const (
	// PlexHubLayoutConditionReady is True once Plex holds the layout the
	// spec states.
	PlexHubLayoutConditionReady = "Ready"

	// PlexHubLayoutStatusManager is the field manager cluster-plex writes
	// status under.
	PlexHubLayoutStatusManager = "cluster-plex"

	// AnnotationPlexHubsReset asks cluster-plex to reset the library's hub
	// customization once (Plex Web's "Reset Customization"). Its value is
	// an RFC 3339 time, read as a nonce: cluster-plex resets when the value
	// differs from status.resetHandled and then records it there.
	AnnotationPlexHubsReset = "catalog.clustarr.io/hubs-reset"
)

// PlexHubKind says what a hub of a library is.
// +kubebuilder:validation:Enum=builtin;collection
type PlexHubKind string

const (
	// PlexHubBuiltin is one of Plex's own hubs ("Recently Released
	// Movies", "Top Movies in (Genre)").
	PlexHubBuiltin PlexHubKind = "builtin"
	// PlexHubCollection is a collection promoted as a hub.
	PlexHubCollection PlexHubKind = "collection"
)

// PlexHubEntry is one hub of a layout: exactly one of Identifier,
// Collection and CollectionTitle names it, and the three booleans say
// where Plex shows it. All three are always sent.
// +kubebuilder:validation:XValidation:rule="(has(self.identifier) ? 1 : 0) + (has(self.collection) ? 1 : 0) + (has(self.collectionTitle) ? 1 : 0) == 1",message="a hub is exactly one of identifier, collection and collectionTitle"
type PlexHubEntry struct {
	// Identifier names one of Plex's built-in hubs by its identifier:
	// "movie.recentlyreleased", "tv.toprated".
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Identifier string `json:"identifier,omitempty"`

	// Collection names a PlexCollection in the layout's namespace. A
	// collection with no hub yet gets one created, and a collection a
	// layout lists takes its visibility from the layout, not from the
	// PlexCollection's spec.visibility.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Collection string `json:"collection,omitempty"`

	// CollectionTitle names a Plex collection no resource manages, by its
	// title in the library.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	CollectionTitle string `json:"collectionTitle,omitempty"`

	// LibraryRecommended shows the hub on the library's Recommended tab.
	LibraryRecommended bool `json:"libraryRecommended"`
	// Home shows the hub on the server owner's Home.
	Home bool `json:"home"`
	// FriendsHome shows the hub on shared users' Home.
	FriendsHome bool `json:"friendsHome"`
}

// PlexHubLayoutSpec is a library's hub layout.
type PlexHubLayoutSpec struct {
	// Library is the Plex library section, by title. cluster-plex also
	// takes a library's former title, so nothing checks it here.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="library is immutable"
	Library string `json:"library"`

	// Hubs are the library's hubs in the order wanted. The listed hubs
	// come first, in this order; a hub of the library not listed keeps
	// Plex's relative order after them, and its visibility. Empty leaves
	// the library's hubs as Plex has them.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:XValidation:rule="self.all(h, !has(h.identifier) || self.exists_one(o, has(o.identifier) && o.identifier == h.identifier))",message="a built-in hub is listed once"
	// +kubebuilder:validation:XValidation:rule="self.all(h, !has(h.collection) || self.exists_one(o, has(o.collection) && o.collection == h.collection))",message="a collection is listed once"
	// +kubebuilder:validation:XValidation:rule="self.all(h, !has(h.collectionTitle) || self.exists_one(o, has(o.collectionTitle) && o.collectionTitle == h.collectionTitle))",message="a collection title is listed once"
	Hubs []PlexHubEntry `json:"hubs,omitempty"`
}

// PlexHubStatus is one hub as Plex holds it.
type PlexHubStatus struct {
	// Identifier is Plex's identifier of the hub.
	Identifier string `json:"identifier"`

	// Title is the hub's title in Plex.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Title string `json:"title,omitempty"`

	// Kind is builtin for one of Plex's own hubs, collection for a
	// collection's.
	Kind PlexHubKind `json:"kind"`

	// Collection is the PlexCollection that holds the hub's collection,
	// when a resource does.
	// +optional
	Collection string `json:"collection,omitempty"`

	// RatingKey is the Plex collection's, for a collection hub.
	// +optional
	RatingKey string `json:"ratingKey,omitempty"`

	LibraryRecommended bool `json:"libraryRecommended"`
	Home               bool `json:"home"`
	FriendsHome        bool `json:"friendsHome"`
}

// PlexHubLayoutStatus is cluster-plex's, alone.
type PlexHubLayoutStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SectionKey is the Plex library section the layout resolved to.
	// +optional
	SectionKey string `json:"sectionKey,omitempty"`

	// ResetHandled is the value of the hubs-reset annotation cluster-plex
	// last honoured.
	// +optional
	ResetHandled string `json:"resetHandled,omitempty"`

	// Hubs is the library's whole hub list as Plex holds it, in Plex's
	// order, listed in the spec or not.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	Hubs []PlexHubStatus `json:"hubs,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=phub,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Library",type=string,JSONPath=`.spec.library`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PlexHubLayout is one Plex library's hubs -- their order and where each
// shows -- declared as a resource; cluster-plex reconciles it into Plex.
type PlexHubLayout struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlexHubLayoutSpec   `json:"spec,omitempty"`
	Status PlexHubLayoutStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// PlexHubLayoutList contains a list of PlexHubLayout.
type PlexHubLayoutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlexHubLayout `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PlexHubLayout{}, &PlexHubLayoutList{})
}
