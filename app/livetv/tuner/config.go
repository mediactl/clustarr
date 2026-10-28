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
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// languageCode is a Config or ChannelConfig language as the relay declares
// it: ISO 639-2's three letters, or "" for none or an unknown code.
func languageCode(s string) string {
	if iso, ok := iptv.ISO6392(s); ok {
		return iso
	}
	return ""
}

// GuideName is the name the provider's one guide, Config.GuideURL's,
// goes by in channel mappings (xmltv.Mapping.Guide).
const GuideName = "xmltv"

// Config is one provider as the IPTVProvider controller resolves it: its
// credentials already read, its channels already listed. The tuner never
// reads the cluster.
type Config struct {
	Key types.NamespacedName
	// Blocks are the provider's devices, as the controller packed them (Live
	// TV spec §3.1): each serves the active channels whose numbers it holds,
	// at its DeviceEndpoint.
	Blocks []BlockConfig
	// Server is the Xtream Codes panel's root, scheme and host[:port],
	// without a trailing slash.
	Server             string
	Username, Password string
	// GuideURL is spec.guideURL, a template that may name {username} and
	// {password}; empty is the panel's xmltv.php (Config.GuideSource). With
	// it set, the panel's guide is still fetched, for the channels this one
	// lacks (fallbackGuide).
	GuideURL string
	// Tuners overrides the account's max_connections; 0 uses it.
	Tuners    int
	UserAgent string
	// Every is how often the playlist, the guide and the account are fetched
	// again, and how old a cached one may be at a restart.
	Every     time.Duration
	EPGSource string // "XEPG" | "PMS"
	Filters   []filter.Rule
	Channels  []ChannelConfig
	Buffer    int
	Linger    time.Duration
	// Language is the language, as a BCP-47 or ISO 639-2 code, the relay
	// declares for an audio or subtitle track whose program map names none
	// or an undetermined one; empty declares nothing.
	Language string
	// Refresh is the clustarr.io/livetv-refresh annotation's value. A new
	// value makes every source of the provider due now (spec §5.1).
	Refresh string
}

// BlockConfig is one block: one emulated HDHomeRun, served at its
// DeviceEndpoint, which the Plex pods' proxies send the block's port to.
type BlockConfig struct {
	Start, DeviceID, FriendlyName string
	// Numbers are the block's active channel numbers.
	Numbers []string
}

// DeviceEndpoint is the path a block's device is served under, on the
// livetv Service.
func DeviceEndpoint(key types.NamespacedName, deviceID string) string {
	return "/livetv/" + key.Namespace + "/" + key.Name + "/devices/" + deviceID
}

// ChannelConfig is one IPTVChannel. Map nil with DummyMinutes > 0 is an
// explicit dummy guide; Map nil with DummyMinutes 0 is mapped by tvg-id or
// display name, else a 60-minute dummy.
type ChannelConfig struct {
	Object, Key, Number, Name, Group, Logo string
	Active                                 bool
	Map                                    *xmltv.Mapping
	DummyMinutes, TimeshiftMinutes         int
	// Language replaces the audio language the channel's stream declares;
	// empty takes Config.Language, which only fills a missing one.
	Language string
}

// PlaylistURL is the panel's live playlist: get.php in m3u_plus, streams as
// MPEG-TS.
func (c Config) PlaylistURL() string { return c.endpoint("get.php") + "&output=ts&type=m3u_plus" }

// GuideSource is the guide the tuner fetches: Config.GuideURL with the
// account's credentials put in, else the panel's xmltv.php.
func (c Config) GuideSource() string {
	if c.GuideURL == "" {
		return c.PanelGuideURL()
	}
	return expandURL(strings.TrimSpace(c.GuideURL), c.Username, c.Password)
}

// PanelGuideURL is the panel's own XMLTV guide, xmltv.php: the guide when
// no GuideURL is set, else the fallback for channels that one lacks.
func (c Config) PanelGuideURL() string { return c.endpoint("xmltv.php") }

// guideFingerprint names the guide a cache was fetched from without
// carrying a credential: the panel's account, or the configured URL as
// written (its placeholders unexpanded) with the panel's guide as its
// fallback.
func guideFingerprint(c Config) string {
	src := "panel\x00" + accountFingerprint(c)
	if c.GuideURL != "" {
		src = "url+panel\x00" + strings.TrimSpace(c.GuideURL) + "\x00" + accountFingerprint(c)
	}
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:8])
}

// AccountURL is the panel's account report.
func (c Config) AccountURL() string { return c.endpoint("player_api.php") }

func (c Config) endpoint(file string) string {
	return c.Server + "/" + file + "?username=" + url.QueryEscape(c.Username) + "&password=" + url.QueryEscape(c.Password)
}

// Snapshot is what the tuner knows of a provider, for both controllers'
// status.
type Snapshot struct {
	PlaylistFetched bool
	Playlist        PlaylistState
	Guide           GuideState
	Account         AccountState
	Lineup          LineupState
	// Blocks are each block's lineup and guide, in Config.Blocks' order.
	Blocks []BlockState
	// Tuners is the relay's limit: Config.Tuners, else the account's
	// max_connections, else 1.
	Tuners      int
	TunersInUse int
	// Channels is keyed by IPTVChannel object name.
	Channels map[string]ChannelState
}

// PlaylistState describes the playlist served.
type PlaylistState struct {
	Entries, Groups, Candidates int
	FetchedAt                   time.Time
	Hash, Error                 string
}

// GuideState describes the guide served.
type GuideState struct {
	Channels, Programmes int
	FetchedAt            time.Time
	Error                string
}

// AccountState is the panel's report of the account (player_api.php).
type AccountState struct {
	Status                 string
	ExpiresAt              time.Time
	MaxConnections, Active int
	FetchedAt              time.Time
	Error                  string
}

// LineupState counts the rendered lineup; First and Last are the numbers
// its active channels span.
type LineupState struct {
	Active, Unmapped, Missing int
	First, Last               string
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
	Missing, Duplicate bool
	Guide              string
}

// PlaylistObject is the clustarr-livetv object caching a provider's
// playlist: every live entry, its stream URL with the account's username
// and password replaced by placeholders, so no credential is stored.
func PlaylistObject(key types.NamespacedName) string {
	return "playlist/" + key.Namespace + "/" + key.Name
}

// GuideCacheObject is the object caching a provider's parsed guide: every
// channel, and programmes for the channels the lineup maps.
func GuideCacheObject(key types.NamespacedName) string {
	return "guide-cache/" + key.Namespace + "/" + key.Name
}

// GuideChannelsObject is the object listing the guide's channels, for the
// ui's mapping dropdown.
func GuideChannelsObject(key types.NamespacedName) string {
	return "guide-channels/" + key.Namespace + "/" + key.Name + "/" + GuideName
}

// GuideObject is the object holding a provider's whole rendered XEPG guide,
// for the UI and curl.
func GuideObject(key types.NamespacedName) string {
	return "guide/" + key.Namespace + "/" + key.Name
}
