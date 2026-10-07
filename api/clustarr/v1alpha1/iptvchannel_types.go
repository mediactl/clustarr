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

// IPTVChannelSpec is one mapped channel: a playlist candidate the owner
// activated or customised (Live TV spec §3, §3.0).
type IPTVChannelSpec struct {
	// ProviderRef names the IPTVProvider in the same namespace. Immutable.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"
	ProviderRef string `json:"providerRef"`

	// Key is the playlist entry's identity (pkg/iptv/m3u: "t:<tvg-id>" or
	// "n:<hash of group and name>"). Immutable.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="key is immutable"
	Key string `json:"key"`

	// Active puts the channel in the lineup. An inactive IPTVChannel keeps
	// its mapping for later.
	// +optional
	Active bool `json:"active,omitempty"`

	// Number is the GuideNumber Plex shows: "1001" or "5.1".
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9]{1,5}(\.[0-9]{1,3})?$`
	Number string `json:"number"`

	// Name overrides the playlist's channel name.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name,omitempty"`

	// Group overrides the playlist's group-title.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Group string `json:"group,omitempty"`

	// Logo overrides the playlist's tvg-logo.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Logo string `json:"logo,omitempty"`

	// EPG is the guide mapping; unset means by tvg-id, else a dummy guide.
	// +optional
	EPG *ChannelEPG `json:"epg,omitempty"`

	// TimeshiftMinutes moves this channel's programmes.
	// +optional
	// +kubebuilder:validation:Minimum=-720
	// +kubebuilder:validation:Maximum=720
	TimeshiftMinutes int32 `json:"timeshiftMinutes,omitempty"`
}

// ChannelEPG maps a channel to a guide: Guide names one of the provider's
// guides and ChannelID an XMLTV channel id in it; or Dummy draws
// DummyMinutes blocks titled by the channel's name.
//
// +kubebuilder:validation:XValidation:rule="(has(self.guide) && size(self.guide) > 0) != (has(self.dummy) && self.dummy)",message="set exactly one of guide and dummy"
// +kubebuilder:validation:XValidation:rule="!has(self.guide) || size(self.guide) == 0 || (has(self.channelID) && size(self.channelID) > 0)",message="channelID is required with guide"
type ChannelEPG struct {
	// Guide names one of the provider's spec.guides.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Guide string `json:"guide,omitempty"`

	// ChannelID is an XMLTV channel id in Guide.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	ChannelID string `json:"channelID,omitempty"`

	// Dummy draws a placeholder guide.
	// +optional
	Dummy bool `json:"dummy,omitempty"`

	// DummyMinutes is the dummy guide's block length. Default 60.
	// +optional
	// +kubebuilder:validation:Enum=30;60;120;180
	DummyMinutes *int32 `json:"dummyMinutes,omitempty"`
}

// IPTVChannelStatus is written by the IPTVChannel controller alone, under
// k8s.ManagerLiveTVChannel (Live TV spec §3.0).
type IPTVChannelStatus struct {
	// ObservedGeneration is the generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, whose reason is Active, Inactive, Missing,
	// DuplicateNumber, GuideNotFound or ProviderNotFound.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=4
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Guide is the mapping actually used: "<guide>/<channelID>",
	// "dummy/<minutes>", or "" under PMS.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Guide string `json:"guide,omitempty"`
}

// IPTVChannel is one mapped channel of an IPTVProvider (Live TV spec §3).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=iptvch,categories=clustarr
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=".spec.providerRef"
// +kubebuilder:printcolumn:name="Number",type="string",JSONPath=".spec.number"
// +kubebuilder:printcolumn:name="Name",type="string",JSONPath=".spec.name"
// +kubebuilder:printcolumn:name="Active",type="boolean",JSONPath=".spec.active"
// +kubebuilder:printcolumn:name="Guide",type="string",JSONPath=".status.guide"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].reason"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type IPTVChannel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPTVChannelSpec   `json:"spec,omitempty"`
	Status IPTVChannelStatus `json:"status,omitempty"`
}

// IPTVChannelList contains a list of IPTVChannel.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type IPTVChannelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPTVChannel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IPTVChannel{}, &IPTVChannelList{})
}
