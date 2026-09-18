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

package importlist

import "errors"

// TraktListType is the kind of Trakt list being followed.
type TraktListType string

// Trakt list types.
const (
	TraktListTypeWatchlist  TraktListType = "watchlist"
	TraktListTypeList       TraktListType = "list"
	TraktListTypeCollection TraktListType = "collection"
	TraktListTypePopular    TraktListType = "popular"
	TraktListTypeTrending   TraktListType = "trending"
)

// TraktConfig configures a pkg/importlist/trakt.List.
type TraktConfig struct {
	ListType TraktListType
	Username string

	// ListSlug matches api/catalog/v1alpha1.TraktList.ListSlug.
	ListSlug string
	Limit    int32
}

// PlexConfig configures a pkg/importlist/plex.Watchlist. It has no settings
// of its own; credentials come from the ImportList's secretRef.
type PlexConfig struct{}

// TmdbConfig configures a pkg/importlist/tmdb.List.
type TmdbConfig struct {
	ListID   *string
	Discover map[string]string
}

// MdblistConfig configures a pkg/importlist/mdblist.List.
type MdblistConfig struct {
	URL string
}

// StevenLuConfig configures a pkg/importlist/stevenlu.List. It has no
// settings of its own.
type StevenLuConfig struct{}

// ImdbCSVConfig configures a pkg/importlist/imdbcsv.List. URL is fetched
// directly by this package; the ImportList controller resolves the CRD's
// ConfigMapRef to a URL (or serves it locally) before constructing this
// config.
type ImdbCSVConfig struct {
	URL string
}

// CustomListFormat is the wire format a custom list is served in.
type CustomListFormat string

// Custom list formats.
const (
	CustomListFormatJSON CustomListFormat = "json"
	CustomListFormatRSS  CustomListFormat = "rss"
)

// CustomConfig configures a pkg/importlist/custom.List.
type CustomConfig struct {
	URL    string
	Format CustomListFormat
}

// ArrKind is the *arr flavour an ArrConfig points at.
type ArrKind string

// Arr kinds.
const (
	ArrKindRadarr   ArrKind = "radarr"
	ArrKindSonarr   ArrKind = "sonarr"
	ArrKindLidarr   ArrKind = "lidarr"
	ArrKindReadarr  ArrKind = "readarr"
	ArrKindClustarr ArrKind = "clustarr"
)

// ArrConfig configures a pkg/importlist/arr.List.
type ArrConfig struct {
	BaseURL string
	Kind    ArrKind
}

// Config is a plain-Go mirror of api/catalog/v1alpha1.ImportListSpec's
// provider union. Exactly one field must be set; Validate enforces that,
// mirroring the CRD's XValidation CEL rule.
//
// One Config produces one ImportList per Kind in the CRD's spec.kinds; a
// list that declares kinds:[movie,series] becomes two ImportList instances
// (e.g. two trakt.List values, one per Kind), because Trakt, Plex, MDBList
// and IMDb CSV each expose separate movie and show endpoints or filters.
// Wiring a Config to a concrete provider constructor (reading
// SecretRef/ConfigMapRef) is the importarr ImportList controller's job
// (Phase G/M6), not this package's — this package has no Kubernetes
// dependency.
type Config struct {
	Trakt    *TraktConfig
	Plex     *PlexConfig
	Tmdb     *TmdbConfig
	Mdblist  *MdblistConfig
	StevenLu *StevenLuConfig
	ImdbCSV  *ImdbCSVConfig
	Custom   *CustomConfig
	Arr      *ArrConfig
}

// ErrConfigExactlyOne is returned by Config.Validate when zero or more than
// one provider field is set.
var ErrConfigExactlyOne = errors.New("importlist: exactly one provider must be set")

// Validate checks that exactly one provider field is set.
func (c Config) Validate() error {
	n := 0
	for _, set := range []bool{
		c.Trakt != nil, c.Plex != nil, c.Tmdb != nil, c.Mdblist != nil,
		c.StevenLu != nil, c.ImdbCSV != nil, c.Custom != nil, c.Arr != nil,
	} {
		if set {
			n++
		}
	}
	if n != 1 {
		return ErrConfigExactlyOne
	}
	return nil
}
