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

package v1alpha1_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestNewMetadataFieldsRoundTrip pins the JSON names ui/plex and the
// metadata gateway rely on for the full Plex Metadata Response.
func TestNewMetadataFieldsRoundTrip(t *testing.T) {
	m := catalogv1.MovieMetadata{
		Tagline: "t", Studios: []string{"Film4"}, Countries: []string{"United Kingdom"}, Adult: true,
		Certifications: []catalogv1.Certification{{Country: "GB", Rating: "18"}},
		OriginalGenres: []string{"Drame"},
		ReleaseDates:   []catalogv1.ReleaseDate{{Country: "GB", Type: 3, Certification: "18"}},
		Images:         []catalogv1.Image{{Type: catalogv1.ImageTypePoster, URL: "u", Language: "en"}},
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	for _, k := range []string{
		`"tagline"`, `"studios"`, `"countries"`, `"adult"`, `"certifications"`,
		`"originalGenres"`, `"certification":"18"`, `"language":"en"`,
	} {
		require.Contains(t, string(b), k)
	}
	s := catalogv1.SeriesMetadata{
		Tagline: "t", Networks: []string{"BBC One"}, Studios: []string{"BBC"}, Countries: []string{"United Kingdom"},
		Certifications: []catalogv1.Certification{{Country: "GB", Rating: "12"}}, OriginalGenres: []string{"x"},
		SeasonImages: []catalogv1.SeasonImage{{Season: 1, Type: catalogv1.ImageTypePoster, URL: "u"}},
		SeasonTypes:  []catalogv1.SeasonTypeRef{{ID: "official", Name: "Aired Order"}},
	}
	b, err = json.Marshal(s)
	require.NoError(t, err)
	for _, k := range []string{`"networks"`, `"seasonImages"`, `"seasonTypes"`, `"certifications"`, `"originalGenres"`, `"tagline"`} {
		require.Contains(t, string(b), k)
	}
	b, err = json.Marshal(catalogv1.EpisodeStatus{Images: []catalogv1.Image{{Type: catalogv1.ImageTypeScreenshot, URL: "u"}}})
	require.NoError(t, err)
	require.Contains(t, string(b), `"images"`)
}
