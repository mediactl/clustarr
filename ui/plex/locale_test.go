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

// A request with no language is answered as the document was written: in
// the language the gateway fetched it in (spec 2026-09-30 §5.3, "as for
// spec.language"), not in English.
func TestWantsOriginalTakesTheDocumentsLanguageWhenPlexNamesNone(t *testing.T) {
	assert.True(t, locale{}.wantsOriginal("en", "de"), "a German install asked in nothing: the English original is due")
	assert.False(t, locale{}.wantsOriginal("ja", "ja"), "a Japanese install asked in nothing: the document is already original")
	assert.True(t, locale{}.wantsOriginal("ja", ""), "a document from before the gateway recorded its language: English")
	assert.False(t, locale{Language: "ja"}.wantsOriginal("ja", "en"), "the request's own language wins")
	assert.False(t, locale{}.wantsOriginal("", "de"), "no original language: nothing to add")
}

// A DVD-ordered series shows its DVD seasons' posters, falling back to the
// aired order's for a season TheTVDB has no DVD poster for; a document from
// before season posters named their order counts as the aired order's.
func TestSeasonPostersFollowTheStoredOrder(t *testing.T) {
	s := &catalogv1.Series{Spec: catalogv1.SeriesSpec{EpisodeOrder: catalogv1.EpisodeOrderDVD}, Status: catalogv1.SeriesStatus{
		Metadata: &catalogv1.SeriesMetadata{SeasonImages: []catalogv1.SeasonImage{
			{Season: 1, Type: catalogv1.ImageTypePoster, Order: "official", URL: "https://artworks.thetvdb.com/s1-aired.jpg"},
			{Season: 1, Type: catalogv1.ImageTypePoster, Order: "dvd", URL: "https://artworks.thetvdb.com/s1-dvd.jpg"},
			{Season: 2, Type: catalogv1.ImageTypePoster, URL: "https://artworks.thetvdb.com/s2-legacy.jpg"},
		}},
	}}
	assert.Equal(t, "https://artworks.thetvdb.com/s1-dvd.jpg", seasonPoster(s, 1))
	assert.Equal(t, "https://artworks.thetvdb.com/s2-legacy.jpg", seasonPoster(s, 2), "no DVD poster: the aired order's")
	s.Spec.EpisodeOrder = ""
	assert.Equal(t, "https://artworks.thetvdb.com/s1-aired.jpg", seasonPoster(s, 1))
}
