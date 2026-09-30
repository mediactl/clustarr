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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

func TestContentRatingPrefixesOutsideTheUS(t *testing.T) {
	certs := []catalogv1.Certification{{Country: "GB", Rating: "18"}, {Country: "US", Rating: "R"}}
	us, gb := certs[1], certs[0]
	assert.Equal(t, "R", contentRating(certs, us, locale{Country: "US"}))
	assert.Equal(t, "gb/18", contentRating(certs, us, locale{Country: "GB"}))
	assert.Equal(t, "gb/18", contentRating(certs[:1], gb, locale{Country: "FR"}), "no FR rating: the stored one, prefixed with its country")
	assert.Equal(t, "R", contentRating(certs[1:], us, locale{Country: "FR"}))
	assert.Equal(t, "R", contentRating(certs, us, locale{}), "no country asked: the stored rating")
	assert.Equal(t, "PG", contentRating(nil, catalogv1.Certification{Rating: "PG"}, locale{}), "no list: the stored rating as it is")
	assert.Equal(t, "", contentRating(nil, catalogv1.Certification{}, locale{Country: "GB"}))
}

// The stored rating is prefixed with the country the gateway chose it for,
// not the first country that happens to share its value: Ireland and the UK
// both rate films "15", and a GB install listing IE first was shown "ie/15".
func TestContentRatingPrefixesTheStoredRatingsOwnCountry(t *testing.T) {
	certs := []catalogv1.Certification{{Country: "IE", Rating: "15"}, {Country: "GB", Rating: "15"}}
	stored := catalogv1.Certification{Country: "GB", Rating: "15"}
	assert.Equal(t, "gb/15", contentRating(certs, stored, locale{Country: "FR"}))
	assert.Equal(t, "gb/15", contentRating(certs, stored, locale{}))
	assert.Equal(t, "ie/15", contentRating(certs, catalogv1.Certification{Rating: "15"}, locale{}),
		"a document written before the gateway stored the country falls back to the first match")
}

func TestLocaleHeaderBeatsQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?X-Plex-Country=DE&X-Plex-Language=de-DE", nil)
	r.Header.Set("X-Plex-Country", "GB")
	assert.Equal(t, locale{Country: "GB", Language: "de"}, localeOf(r))

	r = httptest.NewRequest("GET", "/x?X-Plex-Language=ja-JP", nil)
	assert.Equal(t, locale{Country: "JP", Language: "ja"}, localeOf(r), "the language's region when no country is sent")

	assert.Equal(t, locale{}, localeOf(httptest.NewRequest("GET", "/x", nil)))
}
