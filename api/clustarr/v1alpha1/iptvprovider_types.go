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

// ProviderType is what an IPTVProvider's playlist comes from.
//
// +kubebuilder:validation:Enum=Xtream;M3U
type ProviderType string

const (
	// ProviderTypeXtream is an Xtream Codes account: its playlist is the
	// panel's get.php, its guide its xmltv.php unless guideURL names
	// another, its tuner limit the account's max_connections.
	ProviderTypeXtream ProviderType = "Xtream"
	// ProviderTypeM3U is a plain M3U playlist URL, with an optional XMLTV
	// guide URL and no account (the direct-tuners and ui design §5.7).
	ProviderTypeM3U ProviderType = "M3U"
)

// IPTVProviderSpec is one IPTV source: an Xtream Codes account
// (docs/superpowers/specs/2026-10-20-iptv-xtream-ranges-design.md), whose
// playlist is <server>/get.php with output=ts and type=m3u_plus and guide
// <server>/xmltv.php unless guideURL names another, or a plain M3U
// playlist URL with an optional guideURL (the direct-tuners and ui design
// §5.7); both fetched daily and cached. Its active channels are split into
// blocks of contiguous numbers, each one emulated HDHomeRun and one Plex
// DVR (Live TV spec §3, §3.1).
//
// +kubebuilder:validation:XValidation:rule="(has(self.type) && self.type == 'M3U') ? (has(self.playlistURL) && size(self.playlistURL) > 0) : (has(self.server) && size(self.server) > 0 && has(self.credentialsRef) && has(self.credentialsRef.name) && size(self.credentialsRef.name) > 0)",message="an Xtream provider needs server and credentialsRef; an M3U provider needs playlistURL"
type IPTVProviderSpec struct {
	// Enabled false stops this provider's tuner; Plex then sees its devices
	// gone. Default true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Type is Xtream (an Xtream Codes account, the default) or M3U (a plain
	// playlist URL).
	// +optional
	// +kubebuilder:default=Xtream
	Type ProviderType `json:"type,omitempty"`

	// Server is the Xtream Codes panel: a host ("tvload.win"), a host:port,
	// or a URL. Without a scheme it is https. Required for Xtream.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Server string `json:"server,omitempty"`

	// PlaylistURL is an M3U provider's playlist, http or https; {username}
	// and {password} in it take the credentials Secret's. Required for M3U.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	PlaylistURL string `json:"playlistURL,omitempty"`

	// CredentialsRef names a Secret in the provider's namespace holding the
	// account's username and password, under the keys "username" and
	// "password". They are sent as the panel's query parameters (or put in
	// an M3U provider's URLs) and never stored or reported. Required for
	// Xtream; an M3U provider names one only when its URLs need it.
	// +optional
	CredentialsRef corev1.LocalObjectReference `json:"credentialsRef,omitempty"`

	// Tuners overrides how many distinct channels may stream at once, which
	// the relay enforces with a 503. Unset, it is the account's
	// max_connections (player_api.php), else 1. discover.json advertises
	// tuner.SpareTuners more, so a grab PMS never ended cannot block an idle
	// relay.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=64
	Tuners *int32 `json:"tuners,omitempty"`

	// Refresh is how often the playlist, the guide and the account are
	// fetched again. Each is cached, and a restart fetches only what is
	// older than this. Default 24h.
	// +optional
	Refresh *metav1.Duration `json:"refresh,omitempty"`

	// GuideURL is an XMLTV guide fetched instead of the panel's xmltv.php
	// (owner, 2026-10-27: iptv-epg.org's US guide, whose programmes carry
	// icons the panel's have none of). An http or https URL; "{username}"
	// and "{password}" in it are replaced with the account's, query-escaped,
	// so a panel's own guide under another path can be named without its
	// credentials. Channels map to its channel ids through each
	// IPTVChannel's spec.epg.channelID, so a change of guide needs the
	// lineup's ids mapped to the new guide's. Unset, the panel's xmltv.php.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	GuideURL string `json:"guideURL,omitempty"`

	// UserAgent is sent with playlist, guide and stream requests. Default
	// "clustarr".
	// +optional
	// +kubebuilder:validation:MaxLength=256
	UserAgent string `json:"userAgent,omitempty"`

	// EPGSource is where Plex reads the guide: XEPG (this provider's
	// xmltv.xml, built from the panel's guide and the channel mappings) or
	// PMS (Plex's own lineup guide; mappings' epg is ignored). Default XEPG.
	// +optional
	EPGSource EPGSource `json:"epgSource,omitempty"`

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

	// Device names the emulated HDHomeRuns, one per block.
	// +optional
	Device DeviceSpec `json:"device,omitempty"`

	// DVR sizes the blocks the lineup is split into, one Plex DVR each.
	// +optional
	DVR DVRSpec `json:"dvr,omitempty"`

	// Stream tunes the relay.
	// +optional
	Stream StreamSpec `json:"stream,omitempty"`
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

// DeviceSpec names the emulated HDHomeRuns.
type DeviceSpec struct {
	// FriendlyName is the name Plex shows, before each block's "<start>+".
	// Default "Clustarr <name>".
	// +optional
	// +kubebuilder:validation:MaxLength=128
	FriendlyName string `json:"friendlyName,omitempty"`

	// ID pins the first block's DeviceID when the provider's blocks are
	// first packed: eight upper-case hex digits, unique among every
	// provider's blocks. Unset, every DeviceID is a hash of namespace, name
	// and the block's start (Live TV spec §3.1).
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9A-F]{8}$`
	ID string `json:"id,omitempty"`
}

// DVRSpec sizes the blocks a provider's lineup is split into (Live TV spec
// §3.1). PMS takes at most ~450 channels per device: one channel-map request
// is capped at 32 KiB of request line.
type DVRSpec struct {
	// MaxChannels is a block's most channels. A block is also held under
	// 30,000 bytes of channel map, whichever comes first. Default 400.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=450
	MaxChannels *int32 `json:"maxChannels,omitempty"`
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

	// Language is the language the relay declares for every audio and
	// subtitle track whose program map names none or an undetermined one
	// ("und"), as a BCP-47 or ISO 639-2 code; Plex shows such a track as
	// Unknown otherwise (2026-10-27: tvload.win's ESPN said und, its Weather
	// Channel eng). A channel's own language (IPTVChannel spec.language)
	// replaces whatever the stream declares. Default "eng".
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Language string `json:"language,omitempty"`
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

	// Address is the tuner Service's cluster FQDN and port,
	// <release>-livetv.<namespace>.svc.cluster.local:80: each block's Path
	// is under it, and /livetv/<namespace>/<name>/<file>?from=<n>&to=<n>
	// serves any range of the lineup.
	// +optional
	Address string `json:"address,omitempty"`

	// Blocks are the lineup's runs of contiguous numbers, one emulated
	// HDHomeRun and one Plex DVR each, ordered by start (Live TV spec §3.1).
	// +optional
	// +listType=map
	// +listMapKey=start
	// +kubebuilder:validation:MaxItems=32
	Blocks []BlockStatus `json:"blocks,omitempty"`

	// Account is the panel's report of the account (player_api.php).
	// +optional
	Account *AccountStatus `json:"account,omitempty"`

	// Playlist describes the playlist the tuner serves.
	// +optional
	Playlist *PlaylistStatus `json:"playlist,omitempty"`

	// Guide describes the guide the tuner serves.
	// +optional
	Guide *GuideStatus `json:"guide,omitempty"`

	// Lineup counts the rendered lineup.
	// +optional
	Lineup *LineupStatus `json:"lineup,omitempty"`

	// Tuners counts the provider's tuners.
	// +optional
	Tuners *TunerStatus `json:"tuners,omitempty"`
}

// AccountStatus is an Xtream account as its panel reports it.
type AccountStatus struct {
	// Status is the panel's word for the account: Active, Expired,
	// Banned, Disabled.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Status string `json:"status,omitempty"`
	// ExpiresAt is when the account ends; unset for one that does not.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// MaxConnections is how many streams the account may hold at once.
	// +optional
	MaxConnections int32 `json:"maxConnections,omitempty"`
	// ActiveConnections is how many it held when last asked.
	// +optional
	ActiveConnections int32 `json:"activeConnections,omitempty"`
	// FetchedAt is when the panel was last asked.
	// +optional
	FetchedAt *metav1.Time `json:"fetchedAt,omitempty"`
	// Error is the last ask's error, redacted.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Error string `json:"error,omitempty"`
}

// PlaylistStatus describes the playlist the tuner serves.
type PlaylistStatus struct {
	Entries    int32 `json:"entries"`
	Groups     int32 `json:"groups"`
	Candidates int32 `json:"candidates"`
	// FetchedAt is when the playlist served was fetched, from the panel or
	// before a restart.
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

// GuideStatus describes the guide the tuner serves: spec.guideURL, else
// the panel's xmltv.php, parsed for the active channels.
type GuideStatus struct {
	Channels   int32 `json:"channels"`
	Programmes int32 `json:"programmes"`
	// +optional
	FetchedAt *metav1.Time `json:"fetchedAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Error string `json:"error,omitempty"`
}

// LineupStatus counts the rendered lineup.
type LineupStatus struct {
	Active   int32 `json:"active"`
	Unmapped int32 `json:"unmapped"`
	Missing  int32 `json:"missing"`
	// First and Last are the numbers the active channels span.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	First string `json:"first,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Last string `json:"last,omitempty"`
}

// BlockStatus is one block: one emulated HDHomeRun and one Plex DVR (Live TV
// spec §3.1, §3.2).
type BlockStatus struct {
	// Start is the block's boundary and identity: the number its channels
	// begin at. A channel belongs to the last block whose start is at or
	// below its number.
	// +required
	// +kubebuilder:validation:MaxLength=16
	Start string `json:"start"`

	// Channels counts the block's active channels.
	// +optional
	Channels int32 `json:"channels,omitempty"`

	// First and Last are the numbers the block's channels span.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	First string `json:"first,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=16
	Last string `json:"last,omitempty"`

	// MapBytes is the size of the block's channel-map query.
	// +optional
	MapBytes int32 `json:"mapBytes,omitempty"`

	// DeviceID is the block's HDHomeRun id: spec.device.id for the first
	// block of a first packing, else a hash of namespace, name and start.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9A-F]{8}$`
	DeviceID string `json:"deviceID,omitempty"`

	// Path is the block's device on Address: /hdhr/<deviceID>, which Plex
	// is given (with Address) as the device's network address.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path,omitempty"`

	// URI is the block's device address as Plex registers it:
	// http://<address><path>.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	URI string `json:"uri,omitempty"`

	// FriendlyName is the name Plex shows for the block's device and DVR.
	// +optional
	// +kubebuilder:validation:MaxLength=160
	FriendlyName string `json:"friendlyName,omitempty"`

	// GuidePath is the block's XEPG guide path under its path; empty under
	// PMS.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	GuidePath string `json:"guidePath,omitempty"`

	// Owner is the cluster-plex installation registered for the block,
	// read from its Lease. Empty while none holds it.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Owner string `json:"owner,omitempty"`

	// LineupHash is a hash of the block's lineup.json. A change tells
	// cluster-plex to save that DVR's channel map again.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	LineupHash string `json:"lineupHash,omitempty"`

	// GuideHash is a hash of the block's guide. A change tells cluster-plex
	// to reload that DVR's guide.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	GuideHash string `json:"guideHash,omitempty"`
}

// TunerStatus counts a provider's tuners.
type TunerStatus struct {
	Total int32 `json:"total"`
	InUse int32 `json:"inUse"`
}

// IPTVProvider is one Xtream Codes IPTV account routed to Plex as emulated
// HDHomeRuns, one per block of its channels (Live TV spec §3). Its tuner runs
// in the manager, behind one livetv Service; Plex is given each block at its
// own path on that Service (status.blocks[].uri).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=iptv,categories=clustarr
// +kubebuilder:printcolumn:name="Server",type="string",JSONPath=".spec.server"
// +kubebuilder:printcolumn:name="Tuners",type="integer",JSONPath=".status.tuners.total"
// +kubebuilder:printcolumn:name="Active",type="integer",JSONPath=".status.lineup.active"
// +kubebuilder:printcolumn:name="Candidates",type="integer",JSONPath=".status.playlist.candidates"
// +kubebuilder:printcolumn:name="EPG",type="string",JSONPath=".spec.epgSource"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Expires",type="string",JSONPath=".status.account.expiresAt"
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
