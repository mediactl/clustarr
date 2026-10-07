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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EPGSource is where Plex reads a provider's guide.
// +kubebuilder:validation:Enum=XEPG;PMS
type EPGSource string

const (
	// EPGSourceXEPG serves this provider's own XMLTV guide, built from its
	// guides and channel mappings.
	EPGSourceXEPG EPGSource = "XEPG"
	// EPGSourcePMS leaves the guide to Plex's own lineup; mappings' epg is
	// ignored.
	EPGSourcePMS EPGSource = "PMS"
)

// FilterType is how a ChannelFilter matches a playlist entry.
// +kubebuilder:validation:Enum=groupTitle;custom
type FilterType string

const (
	// FilterGroupTitle matches an entry's group-title exactly.
	FilterGroupTitle FilterType = "groupTitle"
	// FilterCustom matches Match as a substring of the channel name.
	FilterCustom FilterType = "custom"
)

// IPTVProviderSpec is one IPTV provider: one emulated HDHomeRun device and
// one Plex DVR (Live TV spec §3).
type IPTVProviderSpec struct {
	// Enabled false stops this provider's tuner; Plex then sees the device
	// gone. Default true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Playlist is the provider's M3U.
	// +required
	Playlist PlaylistSource `json:"playlist"`

	// Tuners is how many distinct channels may stream at once: the
	// provider's connection limit, and discover.json's TunerCount.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=64
	Tuners int32 `json:"tuners"`

	// EPGSource is where Plex reads the guide: XEPG (this provider's
	// /xmltv.xml, built from Guides and the channel mappings) or PMS
	// (Plex's own lineup guide; mappings' epg is ignored). Default XEPG.
	// +optional
	EPGSource EPGSource `json:"epgSource,omitempty"`

	// Guides are the XMLTV sources ("Add EPG URL").
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Guides []XMLTVSource `json:"guides,omitempty"`

	// Filters choose which playlist entries reach the mapping table. An
	// entry is a candidate when any enabled filter matches it; with no
	// filter, nothing is a candidate (xTeVe's rule).
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Filters []ChannelFilter `json:"filters,omitempty"`

	// ChannelNumberStart is where the UI's auto-numbering begins. Default 1000.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=99999
	ChannelNumberStart *int32 `json:"channelNumberStart,omitempty"`

	// Device names the emulated HDHomeRun.
	// +optional
	Device DeviceSpec `json:"device,omitempty"`

	// Stream tunes the relay.
	// +optional
	Stream StreamSpec `json:"stream,omitempty"`
}

// PlaylistSource is a provider's M3U playlist.
// +kubebuilder:validation:XValidation:rule="has(self.url) != has(self.urlFrom)",message="set exactly one of url and urlFrom"
type PlaylistSource struct {
	// URL is the playlist's URL. Use URLFrom when the URL carries
	// credentials (an Xtream Codes get.php?username=…&password=…).
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	URL string `json:"url,omitempty"`

	// URLFrom names a key of a Secret in the provider's namespace holding
	// the URL.
	// +optional
	URLFrom *corev1.SecretKeySelector `json:"urlFrom,omitempty"`

	// UserAgent is sent with playlist, guide and stream requests. Default
	// "clustarr".
	// +optional
	// +kubebuilder:validation:MaxLength=256
	UserAgent string `json:"userAgent,omitempty"`

	// Refresh is how often the playlist is fetched again. Default 24h.
	// +optional
	Refresh *metav1.Duration `json:"refresh,omitempty"`
}

// XMLTVSource is one XMLTV guide ("Add EPG URL").
// +kubebuilder:validation:XValidation:rule="has(self.url) != has(self.urlFrom)",message="set exactly one of url and urlFrom"
type XMLTVSource struct {
	// Name identifies the guide in channel mappings.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`
	Name string `json:"name"`

	// URL is the guide's URL, plain XML or gzip.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	URL string `json:"url,omitempty"`

	// URLFrom names a key of a Secret in the provider's namespace holding
	// the URL.
	// +optional
	URLFrom *corev1.SecretKeySelector `json:"urlFrom,omitempty"`

	// Refresh is how often the guide is fetched again. Default 12h.
	// +optional
	Refresh *metav1.Duration `json:"refresh,omitempty"`
}

// ChannelFilter chooses playlist entries for the mapping table, with
// xTeVe's semantics (Live TV spec §4.2).
type ChannelFilter struct {
	// Name identifies the filter.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Type groupTitle matches an entry's group-title exactly; custom
	// matches Match as a substring of the channel name.
	// +required
	Type FilterType `json:"type"`

	// Match is the group title or the name substring.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Match string `json:"match"`

	// Include: the name must contain at least one of these words.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=128
	Include []string `json:"include,omitempty"`

	// Exclude: the name must contain none of these words.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=128
	Exclude []string `json:"exclude,omitempty"`

	// CaseSensitive makes Match, Include and Exclude match case.
	// +optional
	CaseSensitive bool `json:"caseSensitive,omitempty"`

	// Enabled false keeps the filter but matches nothing. Default true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// DeviceSpec names the emulated HDHomeRun.
type DeviceSpec struct {
	// FriendlyName is the name Plex shows. Default "Clustarr <name>".
	// +optional
	// +kubebuilder:validation:MaxLength=128
	FriendlyName string `json:"friendlyName,omitempty"`
}

// StreamSpec tunes the stream relay.
type StreamSpec struct {
	// Buffer is each channel's ring buffer. Default 8Mi.
	// +optional
	Buffer *resource.Quantity `json:"buffer,omitempty"`

	// Linger keeps an upstream open after its last viewer leaves, so a
	// channel flip back costs no reconnect. Default 10s.
	// +optional
	Linger *metav1.Duration `json:"linger,omitempty"`
}

// IPTVProviderStatus is written by the IPTVProvider controller alone, under
// k8s.ManagerLiveTV, from the tuner's snapshot (Live TV spec §3.2).
type IPTVProviderStatus struct {
	// ObservedGeneration is the generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Ready, Valid, PlaylistFetched, GuidesFetched.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DeviceID is the emulated HDHomeRun's id: eight hex digits from a hash
	// of namespace/name, so a recreated provider is the same device to Plex.
	// +optional
	DeviceID string `json:"deviceID,omitempty"`

	// Address is the provider's Service ClusterIP, the device address
	// cluster-plex registers with PMS.
	// +optional
	Address string `json:"address,omitempty"`

	// GuideURL is the XEPG guide's URL; empty under PMS.
	// +optional
	GuideURL string `json:"guideURL,omitempty"`

	// Playlist describes the last playlist fetch.
	// +optional
	Playlist *PlaylistStatus `json:"playlist,omitempty"`

	// Guides describes each guide's last fetch.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Guides []GuideStatus `json:"guides,omitempty"`

	// Lineup describes the rendered lineup.json.
	// +optional
	Lineup *LineupStatus `json:"lineup,omitempty"`

	// GuideHash is a hash of the rendered XEPG guide. A change in it tells
	// cluster-plex to reload Plex's guide.
	// +optional
	GuideHash string `json:"guideHash,omitempty"`

	// Tuners counts the provider's tuners.
	// +optional
	Tuners *TunerStatus `json:"tuners,omitempty"`
}

// PlaylistStatus describes the last playlist fetch.
type PlaylistStatus struct {
	Entries    int32 `json:"entries"`
	Groups     int32 `json:"groups"`
	Candidates int32 `json:"candidates"`
	// +optional
	FetchedAt *metav1.Time `json:"fetchedAt,omitempty"`
	// +optional
	Hash string `json:"hash,omitempty"`
	// Error is the last fetch's error, redacted; the last good playlist is
	// still served.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Error string `json:"error,omitempty"`
}

// GuideStatus describes one guide's last fetch.
type GuideStatus struct {
	Name       string `json:"name"`
	Channels   int32  `json:"channels"`
	Programmes int32  `json:"programmes"`
	// +optional
	FetchedAt *metav1.Time `json:"fetchedAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Error string `json:"error,omitempty"`
}

// LineupStatus describes the rendered lineup.
type LineupStatus struct {
	Active   int32 `json:"active"`
	Unmapped int32 `json:"unmapped"`
	Missing  int32 `json:"missing"`
	// Hash is a hash of the rendered lineup.json. A change in it tells
	// cluster-plex to save the DVR's channel map again.
	// +optional
	Hash string `json:"hash,omitempty"`
}

// TunerStatus counts a provider's tuners.
type TunerStatus struct {
	Total int32 `json:"total"`
	InUse int32 `json:"inUse"`
}

// IPTVProvider is one IPTV provider routed to Plex as an emulated HDHomeRun
// (Live TV spec §3). Its tuner runs in the manager; its controller gives it
// one Service, its address in Plex.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=iptv,categories=clustarr
// +kubebuilder:printcolumn:name="Tuners",type="integer",JSONPath=".spec.tuners"
// +kubebuilder:printcolumn:name="Active",type="integer",JSONPath=".status.lineup.active"
// +kubebuilder:printcolumn:name="Candidates",type="integer",JSONPath=".status.playlist.candidates"
// +kubebuilder:printcolumn:name="EPG",type="string",JSONPath=".spec.epgSource"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Address",type="string",JSONPath=".status.address"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type IPTVProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPTVProviderSpec   `json:"spec,omitempty"`
	Status IPTVProviderStatus `json:"status,omitempty"`
}

// IPTVProviderList contains a list of IPTVProvider.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type IPTVProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPTVProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IPTVProvider{}, &IPTVProviderList{})
}
