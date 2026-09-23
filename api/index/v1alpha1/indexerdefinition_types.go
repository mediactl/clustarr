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

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on an IndexerDefinition.
const (
	// IndexerDefinitionConditionValid is True when spec.yaml parses and
	// validates against the bundled Cardigann schema.
	IndexerDefinitionConditionValid = "Valid"
)

// DefinitionType is the privacy class of a Cardigann definition.
//
// +kubebuilder:validation:Enum=public;semiPrivate;private
type DefinitionType string

// Definition types.
const (
	DefinitionTypePublic      DefinitionType = "public"
	DefinitionTypeSemiPrivate DefinitionType = "semiPrivate"
	DefinitionTypePrivate     DefinitionType = "private"
)

// IndexerDefinitionSpec defines the desired state of IndexerDefinition.
type IndexerDefinitionSpec struct {
	// YAML is the full Cardigann definition. It is validated by the
	// controller against the bundled schema.json (v11).
	// +required
	// +kubebuilder:validation:MaxLength=1048576
	YAML string `json:"yaml"`

	// Replaces names the bundled definition id this definition overrides.
	// When unset the definition is added alongside the bundled set.
	// +optional
	Replaces *string `json:"replaces,omitempty"`
}

// CapsSummary is the capabilities parsed from a Cardigann definition.
type CapsSummary struct {
	// Modes maps a search mode to the query parameters it supports.
	//
	// The keys are Torznab's own wire values, which pkg/torznab implements
	// and torznab.Caps.Supports compares against: "search", "tvsearch",
	// "movie", "music", "audio", "book" (pkg/torznab/caps.go). They are NOT
	// "tv-search"/"movie-search" -- this comment said so until Phase D1, and
	// because there is no enum marker on the map key nothing would have
	// caught it: the caps gate would simply never match, and no indexer
	// would ever be queried.
	// +optional
	Modes map[string][]string `json:"modes,omitempty"`

	// Categories lists the Newznab category ids the definition maps.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Categories []int32 `json:"categories,omitempty"`
}

// IndexerDefinitionStatus defines the observed state of IndexerDefinition.
type IndexerDefinitionStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the definition's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ID is the Cardigann definition id parsed from spec.yaml.
	// +optional
	ID string `json:"id,omitempty"`

	// Replaces is the list of older definition ids spec.yaml's own
	// `replaces` key says this definition supersedes (a renamed tracker).
	// An Indexer whose spec.definition names one of them resolves to this
	// definition. Not spec.replaces, which overrides an id rather than
	// aliasing a retired one.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Replaces []string `json:"replaces,omitempty"`

	// Name is the human-readable indexer name parsed from spec.yaml.
	// +optional
	Name string `json:"name,omitempty"`

	// Language is the definition's primary language (e.g. en-US).
	// +optional
	Language string `json:"language,omitempty"`

	// Type is the privacy class of the definition.
	// +optional
	Type DefinitionType `json:"type,omitempty"`

	// Protocol is the transfer protocol the definition serves.
	// +optional
	Protocol commonv1alpha1.Protocol `json:"protocol,omitempty"`

	// Sha256 is the hex digest of spec.yaml as last validated.
	// +optional
	Sha256 string `json:"sha256,omitempty"`

	// Caps summarises the search modes and categories the definition supports.
	// +optional
	Caps CapsSummary `json:"caps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Cluster,shortName=idxdef,categories=clustarr
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.status.protocol`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.status.type`
// +kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IndexerDefinition is a custom or overriding Cardigann definition. The
// bundled definitions ship in the image and are not represented as objects.
type IndexerDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IndexerDefinitionSpec   `json:"spec,omitempty"`
	Status IndexerDefinitionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// IndexerDefinitionList contains a list of IndexerDefinition.
type IndexerDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IndexerDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IndexerDefinition{}, &IndexerDefinitionList{})
}
