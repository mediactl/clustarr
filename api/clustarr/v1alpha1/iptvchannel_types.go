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

	// Category is the lineup category the channel's block follows. Nil is
	// automatic; the empty string puts the channel on no DVR (§5.1).
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Category *string `json:"category,omitempty"`

	// TimeshiftMinutes moves this channel's programmes.
	// +optional
	// +kubebuilder:validation:Minimum=-720
	// +kubebuilder:validation:Maximum=720
	TimeshiftMinutes int32 `json:"timeshiftMinutes,omitempty"`

	// Language is the language the relay declares for this channel's audio
	// and subtitle tracks, as a BCP-47 or ISO 639-2 code, replacing whatever
	// the stream says; empty takes the provider's stream.language, which
	// only fills a missing or undetermined one.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Language string `json:"language,omitempty"`
}

// ChannelEPG maps a channel to a guide: ChannelID is an XMLTV channel id in
// the provider's guide (its panel's xmltv.php); or Dummy draws DummyMinutes
// blocks titled by the channel's name; neither maps by tvg-id, as an absent
// epg does. Plex is the channel's pairing with its provider's Plex lineup,
// beside either.
//
// +kubebuilder:validation:XValidation:rule="!((has(self.channelID) && size(self.channelID) > 0) && (has(self.dummy) && self.dummy))",message="set at most one of channelID and dummy"
type ChannelEPG struct {
	// ChannelID is an XMLTV channel id in the provider's guide.
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

	// Plex overrides the automatic pairing with the provider's Plex lineup
	// (spec.plexGuide): a station, or off. Unset, the channel is paired by
	// its guide id's call sign and its name (the Plex native guide design
	// §5.3).
	// +optional
	Plex *ChannelPlex `json:"plex,omitempty"`
}

// ChannelPlex is a channel's pairing chosen by hand.
//
// +kubebuilder:validation:XValidation:rule="(has(self.station) && size(self.station) > 0) != (has(self.off) && self.off)",message="set exactly one of station and off"
type ChannelPlex struct {
	// Station is a station's gridKey, the same in every Plex lineup: the
	// part of a lineup channel's id after its dash.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{24}$`
	Station string `json:"station,omitempty"`

	// Off keeps the channel off Plex.
	// +optional
	Off bool `json:"off,omitempty"`
}

// PlexMatch is how a channel was paired with its provider's Plex lineup.
// Only Manual and Automatic channels reach Plex.
// +kubebuilder:validation:Enum=Manual;Automatic;Ambiguous;Taken;Timeshifted;NotInLineup;Off;NoLineup
type PlexMatch string

const (
	PlexMatchManual      PlexMatch = "Manual"
	PlexMatchAutomatic   PlexMatch = "Automatic"
	PlexMatchAmbiguous   PlexMatch = "Ambiguous"
	PlexMatchTaken       PlexMatch = "Taken"
	PlexMatchTimeshifted PlexMatch = "Timeshifted"
	PlexMatchNotInLineup PlexMatch = "NotInLineup"
	PlexMatchOff         PlexMatch = "Off"
	PlexMatchNoLineup    PlexMatch = "NoLineup"
)

// ChannelPlexStatus is a channel's pairing with its provider's Plex lineup.
type ChannelPlexStatus struct {
	// Match is how the channel was paired, or why it is off Plex.
	Match PlexMatch `json:"match"`
	// Station is the paired station's gridKey.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Station string `json:"station,omitempty"`
	// ID is the lineup channel's id, <lineup id>-<gridKey>.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id,omitempty"`
	// CallSign is the station's call sign.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	CallSign string `json:"callSign,omitempty"`
	// Title is the station's title.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Title string `json:"title,omitempty"`
	// VCN is the station's channel number in the lineup, which Plex shows.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	VCN string `json:"vcn,omitempty"`
	// TakenBy names the channel holding the station, for Taken.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	TakenBy string `json:"takenBy,omitempty"`
	// Candidates are the stations an Ambiguous match named, each
	// "<callSign> <vcn> <title>".
	// +optional
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:MaxLength=320
	Candidates []string `json:"candidates,omitempty"`
	// Lineup is the Plex lineup the station was taken from.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Lineup string `json:"lineup,omitempty"`
	// MissingSince is when the paired station was first absent from the
	// lineup; the pairing stands for 7 days of absence.
	// +optional
	MissingSince *metav1.Time `json:"missingSince,omitempty"`
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

	// Guide is the mapping actually used: "<guide>/<channelID>" or
	// "dummy/<minutes>".
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Guide string `json:"guide,omitempty"`

	// Plex is the channel's pairing with its provider's Plex lineup; unset
	// when the provider names none.
	// +optional
	Plex *ChannelPlexStatus `json:"plex,omitempty"`

	// Lineup is what the lineup planner decided for the channel (§5.1).
	// +optional
	Lineup *ChannelLineupStatus `json:"lineup,omitempty"`
}

// ChannelLineupStatus is the planner's verdict on one channel (§5.1).
type ChannelLineupStatus struct {
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Category string `json:"category,omitempty"`
	// Rule is the rule that took the entry.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Rule string `json:"rule,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Number string `json:"number,omitempty"`
	// DuplicateOf is the key of the winning entry this one duplicates.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	DuplicateOf string `json:"duplicateOf,omitempty"`
	// +optional
	MissingSince *metav1.Time `json:"missingSince,omitempty"`
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
