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

package plex

import (
	"regexp"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// plexScheme is the scheme of Plex's own GUIDs, and the externalIDs key
// the metadata gateway records an item's Plex id under.
const plexScheme = "plex"

// plexIDPattern is a Plex metadata id: 24 lowercase hex digits. It can
// never be a clustarr ratingKey, a 36-character UUID ([ratingKeyPattern]).
var plexIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// guid is the guid an item is answered with: plex://<type>/<plexID> when
// the provider answers with Plex's GUIDs and plexID is a well-formed Plex
// id, else clustarr's own [GUID]. Plex Web offers Watchlist only for an
// item whose own guid is plex://, and PMS 1.43.4 accepts one from a custom
// provider while still reading the item from it
// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md §2).
func (u urls) guid(identifier, metadataType, ratingKey, plexID string) string {
	if u.plexGUIDs && plexIDPattern.MatchString(plexID) {
		return plexScheme + "://" + metadataType + "/" + plexID
	}
	return GUID(identifier, metadataType, ratingKey)
}

func moviePlexID(m *catalogv1.Movie) string {
	if md := m.Status.Metadata; md != nil {
		return md.ExternalIDs[plexScheme]
	}
	return ""
}

func seriesPlexID(s *catalogv1.Series) string {
	if md := s.Status.Metadata; md != nil {
		return md.ExternalIDs[plexScheme]
	}
	return ""
}

func seasonPlexID(s *catalogv1.Series, number int32) string {
	if md := s.Status.Metadata; md != nil {
		for _, p := range md.PlexSeasons {
			if p.Number == number {
				return p.ID
			}
		}
	}
	return ""
}

func episodePlexID(e *catalogv1.Episode) string { return e.Status.PlexID }
