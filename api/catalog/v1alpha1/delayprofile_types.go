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

// Condition types reported on a DelayProfile.
const (
	// DelayProfileConditionReady is True when the profile is usable.
	DelayProfileConditionReady = "Ready"
)

// DelayPreferredProtocol is the protocol a delay profile waits for.
//
// +kubebuilder:validation:Enum=usenet;torrent
type DelayPreferredProtocol string

// Delay profile preferred protocols.
const (
	DelayPreferredProtocolUsenet  DelayPreferredProtocol = "usenet"
	DelayPreferredProtocolTorrent DelayPreferredProtocol = "torrent"
)

// DelayProfileSpec defines the desired state of DelayProfile.
type DelayProfileSpec struct {
	// EnableUsenet allows usenet releases to be grabbed for matching items.
	// +optional
	// +kubebuilder:default=true
	EnableUsenet *bool `json:"enableUsenet,omitempty"`

	// EnableTorrent allows torrent releases to be grabbed for matching items.
	// +optional
	// +kubebuilder:default=true
	EnableTorrent *bool `json:"enableTorrent,omitempty"`

	// PreferredProtocol is the protocol preferred once both delays have expired.
	// +optional
	// +kubebuilder:default=usenet
	PreferredProtocol DelayPreferredProtocol `json:"preferredProtocol,omitempty"`

	// UsenetDelayMinutes is how long to wait before grabbing a usenet release.
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	UsenetDelayMinutes int32 `json:"usenetDelayMinutes,omitempty"`

	// TorrentDelayMinutes is how long to wait before grabbing a torrent release.
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	TorrentDelayMinutes int32 `json:"torrentDelayMinutes,omitempty"`

	// BypassIfHighestQuality grabs immediately when the release is already in
	// the top tier of the quality profile.
	// +optional
	// +kubebuilder:default=true
	BypassIfHighestQuality *bool `json:"bypassIfHighestQuality,omitempty"`

	// BypassIfAboveFormatScore grabs immediately when the release scores at or
	// above MinimumFormatScore.
	// +optional
	// +kubebuilder:default=false
	BypassIfAboveFormatScore *bool `json:"bypassIfAboveFormatScore,omitempty"`

	// MinimumFormatScore is the score that triggers BypassIfAboveFormatScore.
	// +optional
	// +kubebuilder:default=0
	MinimumFormatScore int32 `json:"minimumFormatScore,omitempty"`

	// Order breaks ties between profiles that match the same item; lowest wins.
	// The chart installs a catch-all "default" profile with order 1000.
	// +optional
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	Order int32 `json:"order,omitempty"`

	// Tags matches the tags on a catalog item; empty makes the profile a catch-all.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Tags []string `json:"tags,omitempty"`
}

// DelayProfileStatus describes the observed state of DelayProfile.
type DelayProfileStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the profile's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// PendingCount is the number of grabs currently waiting out this profile's delay.
	// +optional
	PendingCount int32 `json:"pendingCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=dp,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Order",type=integer,JSONPath=`.spec.order`
// +kubebuilder:printcolumn:name="Preferred",type=string,JSONPath=`.spec.preferredProtocol`
// +kubebuilder:printcolumn:name="Usenet",type=integer,JSONPath=`.spec.usenetDelayMinutes`
// +kubebuilder:printcolumn:name="Torrent",type=integer,JSONPath=`.spec.torrentDelayMinutes`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DelayProfile holds a grab back for a while so that a better release can
// appear on the preferred protocol first.
type DelayProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DelayProfileSpec   `json:"spec,omitempty"`
	Status DelayProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// DelayProfileList contains a list of DelayProfile.
type DelayProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DelayProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DelayProfile{}, &DelayProfileList{})
}
