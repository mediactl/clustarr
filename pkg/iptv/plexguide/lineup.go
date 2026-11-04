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

package plexguide

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// BaseURL is Plex's cloud guide, which answers a lineup and its stations
// to a Plex account token (X-Plex-Token).
const BaseURL = "https://epg.provider.plex.tv"

// lineupPrefix starts every Plex cloud lineup's uuid, as PMS lists them.
const lineupPrefix = "lineup://tv.plex.providers.epg.cloud/"

// LineupURL answers the lineup itself: its title, under
// MediaContainer.Lineup[0].
func LineupURL(id string) string { return BaseURL + "/lineups/" + url.PathEscape(id) }

// ChannelsURL answers the lineup's stations, under MediaContainer.Channel.
// Plex lists at most 1,000 (DIRECTV Mobile's 1,009 are cut to 1,000), and
// pages by no query parameter.
func ChannelsURL(id string) string { return LineupURL(id) + "/channels" }

// LineupURI is the lineup as PMS takes it in POST /livetv/dvrs:
// lineup://tv.plex.providers.epg.cloud/<id>#<title>, the title escaped as
// PMS's own lineup list escapes it (spaces %20, parentheses %28 and %29).
func LineupURI(id, title string) string {
	return lineupPrefix + id + "#" + strings.ReplaceAll(url.QueryEscape(title), "+", "%20")
}

// Station is one channel of a lineup: a station at its number there.
type Station struct {
	// ID is the lineup channel, <lineup id>-<gridKey>: what PMS's channel
	// map takes as channelMappingByKey.
	ID string `json:"id"`
	// GridKey is the station, the same in every lineup.
	GridKey           string `json:"gridKey"`
	CallSign          string `json:"callSign"`
	AffiliateCallSign string `json:"affiliateCallSign,omitempty"`
	Title             string `json:"title"`
	ShortTitle        string `json:"shortTitle,omitempty"`
	// VCN is the station's number in the lineup, which Plex shows: what the
	// channel map takes as channelMapping. Two stations may share one
	// (WEAR and WEARDT are both 003).
	VCN   string `json:"vcn"`
	Thumb string `json:"thumb,omitempty"`
	HD    bool   `json:"isHd,omitempty"`
}

// Lineup is a lineup's stations as the tuner fetched them.
type Lineup struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Stations  []Station `json:"stations"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// ErrNoLineup is a lineup answer that lists no lineup.
var ErrNoLineup = errors.New("plexguide: the answer lists no lineup")

// DecodeChannels reads ChannelsURL's answer; a hidden station is left out.
// The caller caps the body.
func DecodeChannels(r io.Reader) ([]Station, error) {
	var out struct {
		MediaContainer struct {
			Channel []struct {
				Station
				Hidden bool `json:"hidden"`
			} `json:"Channel"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return nil, fmt.Errorf("plexguide: decode the lineup's stations: %w", err)
	}
	stations := make([]Station, 0, len(out.MediaContainer.Channel))
	for _, c := range out.MediaContainer.Channel {
		if !c.Hidden && c.GridKey != "" {
			stations = append(stations, c.Station)
		}
	}
	return stations, nil
}

// DecodeTitle reads LineupURL's answer: the lineup's title.
func DecodeTitle(r io.Reader) (string, error) {
	var out struct {
		MediaContainer struct {
			Lineup []struct {
				Title string `json:"title"`
			} `json:"Lineup"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return "", fmt.Errorf("plexguide: decode the lineup: %w", err)
	}
	if len(out.MediaContainer.Lineup) == 0 {
		return "", ErrNoLineup
	}
	return out.MediaContainer.Lineup[0].Title, nil
}
