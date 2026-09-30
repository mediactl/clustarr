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
	assert.Equal(t, "R", contentRating(certs, "R", locale{Country: "US"}))
	assert.Equal(t, "gb/18", contentRating(certs, "R", locale{Country: "GB"}))
	assert.Equal(t, "gb/18", contentRating(certs[:1], "18", locale{Country: "FR"}), "no FR rating: the stored one, prefixed with its country")
	assert.Equal(t, "R", contentRating(certs[1:], "R", locale{Country: "FR"}))
	assert.Equal(t, "R", contentRating(certs, "R", locale{}), "no country asked: the stored rating")
	assert.Equal(t, "PG", contentRating(nil, "PG", locale{}), "no list: the stored rating as it is")
	assert.Equal(t, "", contentRating(nil, "", locale{Country: "GB"}))
}

func TestLocaleHeaderBeatsQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?X-Plex-Country=DE&X-Plex-Language=de-DE", nil)
	r.Header.Set("X-Plex-Country", "GB")
	assert.Equal(t, locale{Country: "GB", Language: "de"}, localeOf(r))

	r = httptest.NewRequest("GET", "/x?X-Plex-Language=ja-JP", nil)
	assert.Equal(t, locale{Country: "JP", Language: "ja"}, localeOf(r), "the language's region when no country is sent")

	assert.Equal(t, locale{}, localeOf(httptest.NewRequest("GET", "/x", nil)))
}
