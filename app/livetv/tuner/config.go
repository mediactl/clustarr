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

package tuner

import (
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// Config is one provider as the IPTVProvider controller resolves it: its
// Secrets already read, its channels already listed. The tuner never reads
// the cluster.
type Config struct {
	Key types.NamespacedName
	// Blocks are the provider's devices, as the controller packed them (Live
	// TV spec §3.1): each serves the active channels whose numbers it holds,
	// on its own port.
	Blocks          []BlockConfig
	Tuners          int
	UserAgent       string
	PlaylistURL     string
	PlaylistRefresh time.Duration
	EPGSource       string // "XEPG" | "PMS"
	Guides          []GuideConfig
	Filters         []filter.Rule
	Channels        []ChannelConfig
	Buffer          int
	Linger          time.Duration
	// Refresh is the clustarr.io/livetv-refresh annotation's value. A new
	// value makes every source of the provider due now (spec §5.1).
	Refresh string
}

// BlockConfig is one block: one emulated HDHomeRun, reached through the
// Plex pods' proxies on Port (spec §5.0).
type BlockConfig struct {
	Start, DeviceID, FriendlyName string
	Port                          int32
	// Numbers are the block's active channel numbers.
	Numbers []string
}

// GuideConfig is one XMLTV guide.
type GuideConfig struct {
	Name    string
	URL     string
	Refresh time.Duration
}

// ChannelConfig is one IPTVChannel. Map nil with DummyMinutes > 0 is an
// explicit dummy guide; Map nil with DummyMinutes 0 is mapped by tvg-id or
// display name, else a 60-minute dummy.
type ChannelConfig struct {
	Object, Key, Number, Name, Group, Logo string
	Active                                 bool
	Map                                    *xmltv.Mapping
	DummyMinutes, TimeshiftMinutes         int
}

// Snapshot is what the tuner knows of a provider, for both controllers'
// status.
type Snapshot struct {
	PlaylistFetched bool
	Playlist        PlaylistState
	Guides          []GuideState
	Lineup          LineupState
	// Blocks are each block's lineup and guide, in Config.Blocks' order.
	Blocks      []BlockState
	TunersInUse int
	// Channels is keyed by IPTVChannel object name.
	Channels map[string]ChannelState
}

// PlaylistState describes the last playlist fetch.
type PlaylistState struct {
	Entries, Groups, Candidates int
	FetchedAt                   time.Time
	Hash, Error                 string
}

// GuideState describes one guide's last fetch.
type GuideState struct {
	Name                 string
	Channels, Programmes int
	FetchedAt            time.Time
	Error                string
}

// LineupState counts the rendered lineup across every block.
type LineupState struct {
	Active, Unmapped, Missing int
}

// BlockState is one block's rendered lineup and guide. A change in a hash
// tells cluster-plex to save that DVR's map or reload its guide.
type BlockState struct {
	Start                 string
	Active                int
	LineupHash, GuideHash string
}

// ChannelState is one channel's place in the lineup. Guide is the mapping
// used: "<guide>/<channelID>", "dummy/<minutes>", or "" under PMS or when
// the channel is not in the lineup.
type ChannelState struct {
	Missing, Duplicate, GuideNotFound bool
	Guide                             string
}

// PlaylistObject is the clustarr-livetv object holding a provider's
// playlist, without stream URLs (spec §5.4).
func PlaylistObject(key types.NamespacedName) string {
	return "playlist/" + key.Namespace + "/" + key.Name
}

// GuideChannelsObject is the object listing one guide's channels.
func GuideChannelsObject(key types.NamespacedName, guide string) string {
	return "guide-channels/" + key.Namespace + "/" + key.Name + "/" + guide
}

// GuideObject is the object holding a provider's whole rendered XEPG guide,
// for the UI and curl.
func GuideObject(key types.NamespacedName) string {
	return "guide/" + key.Namespace + "/" + key.Name
}

// BlockGuideObject is the object holding one block's rendered guide, served
// across restarts until the guides are fetched again.
func BlockGuideObject(key types.NamespacedName, start string) string {
	return "guide/" + key.Namespace + "/" + key.Name + "/" + start
}
