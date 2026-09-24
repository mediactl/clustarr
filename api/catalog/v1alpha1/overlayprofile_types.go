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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on an OverlayProfile.
const (
	// OverlayProfileConditionReady is True once the profile is usable.
	OverlayProfileConditionReady = "Ready"
)

// OverlayCorner is where a profile's badge stack is anchored on the poster.
//
// +kubebuilder:validation:Enum=bottomRight;bottomLeft;topRight;topLeft
type OverlayCorner string

// Overlay corners.
const (
	OverlayCornerBottomRight OverlayCorner = "bottomRight"
	OverlayCornerBottomLeft  OverlayCorner = "bottomLeft"
	OverlayCornerTopRight    OverlayCorner = "topRight"
	OverlayCornerTopLeft     OverlayCorner = "topLeft"
)

// OverlayBadge is one rating source drawn as a badge in the stack.
type OverlayBadge struct {
	// Source is the rating source this badge renders.
	// +required
	Source RatingSource `json:"source"`
}

// OverlayGeometry sizes and positions the badge stack. Every field is a
// pointer with an *OrDefault accessor (see defaults.go) rather than a
// +kubebuilder:default -- the typed-client defaulting trap (CLAUDE.md): a
// scalar default is unreachable from a Go client that always sends its zero
// value, so every consumer must read these through the accessor, never the
// field directly.
type OverlayGeometry struct {
	// WidthPercent is the badge stack's width, as a percentage of poster
	// width; each badge's height follows at the aspect of Plex's
	// episode-count box (237:207). Unset means 19, which reproduces that box
	// (WidthPercentOrDefault, DefaultOverlayWidthPercent).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	WidthPercent *int32 `json:"widthPercent,omitempty"`

	// RadiusPercent is the radius of each badge's one rounded corner, the
	// one diagonally opposite Corner, as a percentage of poster width.
	// Unset means 2 (RadiusPercentOrDefault, DefaultOverlayRadiusPercent).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	RadiusPercent *int32 `json:"radiusPercent,omitempty"`

	// PaddingPercent is the padding inside each badge, as a percentage of
	// poster width. Unset means 2 (PaddingPercentOrDefault,
	// DefaultOverlayPaddingPercent).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	PaddingPercent *int32 `json:"paddingPercent,omitempty"`

	// LogoPercent is the provider logo's height, as a percentage of the
	// badge's box height; the logo sits left of the score, the two centred
	// in the box as one row. Unset means 32 (LogoPercentOrDefault,
	// DefaultOverlayLogoPercent).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	LogoPercent *int32 `json:"logoPercent,omitempty"`

	// ScorePercent is the score text's cap height, as a percentage of the
	// badge's box height. Unset means 27, the height of Plex's episode count
	// on its box (ScorePercentOrDefault, DefaultOverlayScorePercent).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	ScorePercent *int32 `json:"scorePercent,omitempty"`

	// OpacityPercent is the opacity of the badges' black background. Unset
	// means 80, Plex's (OpacityPercentOrDefault, DefaultOverlayOpacityPercent).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	OpacityPercent *int32 `json:"opacityPercent,omitempty"`
}

// OverlayProfileSpec defines the desired state of OverlayProfile.
//
// Kinds is restricted to movie and series by CEL rather than a narrower
// schema enum: MediaKind's own +kubebuilder:validation:Enum already lists
// every catalog kind, and controller-gen has no way to generate a tighter
// items enum for a field whose element type is a shared named type that
// already carries its own type-level Enum marker (confirmed empirically
// against controller-gen v0.22.0: a field- or items-level Enum marker on
// such a field is silently ignored, unlike on a plain []string as
// ImportListSpec.Kinds uses).
// +kubebuilder:validation:XValidation:rule="!has(self.kinds) || self.kinds.all(k, k == 'movie' || k == 'series')",message="kinds accepts only movie and series"
type OverlayProfileSpec struct {
	// Selector matches Movie and Series labels. Nil selects nothing.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Kinds limits the selection; unset means both movie and series. Only
	// movie and series are accepted; see the CEL rule on OverlayProfileSpec.
	// +optional
	// +kubebuilder:validation:MaxItems=2
	Kinds []commonv1.MediaKind `json:"kinds,omitempty"`

	// Badges are drawn bottom-up along the chosen corner. Unset means one
	// badge, metacritic.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +listType=map
	// +listMapKey=source
	Badges []OverlayBadge `json:"badges,omitempty"`

	// Corner is where the badge stack is anchored.
	// +optional
	// +kubebuilder:default=bottomRight
	Corner OverlayCorner `json:"corner,omitempty"`

	// Geometry overrides the badge stack's size and position.
	// +optional
	Geometry *OverlayGeometry `json:"geometry,omitempty"`
}

// OverlayProfileStatus describes the observed state of OverlayProfile.
type OverlayProfileStatus struct {
	// Hash is the sha256 of the render-relevant spec (overlay.TemplateSpec),
	// the same conversion the renderer draws from.
	// +optional
	Hash string `json:"hash,omitempty"`

	// Selected is the number of Movies and Series this profile currently selects.
	// +optional
	Selected int32 `json:"selected,omitempty"`

	// Conditions holds Ready and Overlap.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=ovp,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Hash",type=string,JSONPath=`.status.hash`
// +kubebuilder:printcolumn:name="Selected",type=integer,JSONPath=`.status.selected`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OverlayProfile selects Movies and Series to render rating-badge overlays onto.
type OverlayProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OverlayProfileSpec   `json:"spec,omitempty"`
	Status OverlayProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// OverlayProfileList contains a list of OverlayProfile.
type OverlayProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OverlayProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OverlayProfile{}, &OverlayProfileList{})
}
