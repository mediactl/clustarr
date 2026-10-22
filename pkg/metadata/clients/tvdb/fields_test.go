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

package tvdb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// recordedTVDB serves the responses hack/record-metadata-fixtures recorded
// from TheTVDB for Doctor Who (2005, 78804) and its episode "Rose".
func recordedTVDB(t *testing.T) *tvdb.Client {
	t.Helper()
	files := map[string]string{
		"/login":                             "login.json",
		"/series/78804/extended":             "series-78804-extended.json",
		"/series/78804/episodes/default/eng": "episodes-78804.json",
		"/episodes/295294/extended":          "episode-295294-extended.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile("../../../../test/data/metadata/tvdb/" + name)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
}

func TestSeriesMapsTheFullMetadataFields(t *testing.T) {
	s, err := recordedTVDB(t).WithLocale("en", "US").Series(context.Background(), "78804")
	require.NoError(t, err)

	assert.Equal(t, []string{"BBC One", "BBC Two", "BBC Four"}, s.Networks)
	assert.Equal(t, "BBC One", s.Network)
	assert.Empty(t, s.Studios, "every company TVDB lists for it is a network")
	assert.Equal(t, []string{"United Kingdom"}, s.Countries)
	assert.Equal(t, []metadata.Certification{{Country: "US", Rating: "TV-PG"}}, s.Certifications)
	assert.Equal(t, "TV-PG", s.Certification)
	assert.Equal(t, "US", s.CertificationCountry)
	assert.Equal(t, "en", s.Language, "the language the titles were fetched in, as BCP-47")

	var cast []metadata.Person
	for _, p := range s.People {
		if p.Kind == metadata.PersonCast {
			cast = append(cast, p)
		}
	}
	require.Len(t, cast, 30)
	var tennant metadata.Person
	for _, p := range cast {
		if p.Name == "David Tennant" {
			tennant = p
		}
	}
	assert.Equal(t, "The Tenth Doctor", tennant.Character)
	assert.EqualValues(t, 6, tennant.Order)
	assert.Equal(t, "https://artworks.thetvdb.com/banners/person/297153/62107884.jpg", tennant.ImageURL)

	var seasonOne *metadata.Image
	seasons := map[string]map[int32]bool{}
	for i, img := range s.Images {
		if img.Season == nil || img.Type != metadata.ImageTypePoster {
			continue
		}
		if seasons[img.SeasonOrder] == nil {
			seasons[img.SeasonOrder] = map[int32]bool{}
		}
		seasons[img.SeasonOrder][*img.Season] = true
		if seasonOne == nil && *img.Season == 1 && img.SeasonOrder == "official" {
			seasonOne = &s.Images[i]
		}
	}
	assert.Equal(t, map[string]int{"official": 14, "dvd": 10}, map[string]int{"official": len(seasons["official"]), "dvd": len(seasons["dvd"])},
		"every stored order's seasons have posters, each naming its order")
	assert.Len(t, seasons, 2, "alternate orders clustarr never stores are left out")
	require.NotNil(t, seasonOne)
	assert.Equal(t, "https://artworks.thetvdb.com/banners/seasons/5bbd0422a8706.jpg", seasonOne.URL)
	assert.Contains(t, s.SeasonTypes, metadata.SeasonTypeRef{ID: "official", Name: "Aired Order"})
	assert.Len(t, s.SeasonTypes, 4)
}

// TheTVDB's ClearLogo (23) and ClearArt (22) are the series' logo and clear
// art. Types 6 and 7 are a season's Banner and Poster, which were read as
// clear art and logo until 2026-10-21: on talos every series' Plex clearLogo
// was a season poster (Family Guy's, byte for byte). A logo carries its
// title's text, so only those in the titles' language or in none are kept,
// as TMDB's include_image_language keeps them: Doctor Who's Russian logo
// and clear art are left out.
func TestSeriesLogoAndClearArtAreTheTVDBsClearLogoAndClearArt(t *testing.T) {
	s, err := recordedTVDB(t).WithLocale("en", "US").Series(context.Background(), "78804")
	require.NoError(t, err)

	own := map[metadata.ImageType][]metadata.Image{}
	for _, img := range s.Images {
		if img.Season != nil {
			continue
		}
		assert.NotRegexp(t, `/seasons?(wide)?/`, img.URL, "a season's artwork is never the series' own")
		own[img.Type] = append(own[img.Type], img)
	}
	require.Len(t, own[metadata.ImageTypeLogo], 14)
	assert.Equal(t, "https://artworks.thetvdb.com/banners/v4/series/78804/clearlogo/611b52389b2a5.png",
		own[metadata.ImageTypeLogo][0].URL, "TheTVDB's first, its highest scored")
	for _, img := range own[metadata.ImageTypeLogo] {
		assert.Contains(t, img.URL, "/clearlogo/")
		assert.Equal(t, "en", img.Language)
	}
	require.Len(t, own[metadata.ImageTypeClearart], 10)
	assert.Equal(t, "https://artworks.thetvdb.com/banners/v4/series/78804/clearart/611b52668104f.png",
		own[metadata.ImageTypeClearart][0].URL)
	for _, img := range own[metadata.ImageTypeClearart] {
		assert.Contains(t, img.URL, "/clearart/")
		assert.Equal(t, "en", img.Language)
	}
}

// A season's own Banner (6) and Poster (7) join their season through
// seasonId, filed under its number and order: the season list's poster
// first -- the one the Plex provider sends -- then TheTVDB's artworks in the
// order published, a URL once, at most three of a type per season and
// order. Doctor Who has 23 posters for its first season alone, and
// seasonImages holds 400 in all.
func TestSeasonsTakeTheirOwnBannersAndPosters(t *testing.T) {
	s, err := recordedTVDB(t).WithLocale("en", "US").Series(context.Background(), "78804")
	require.NoError(t, err)

	type key struct {
		season int32
		order  string
		kind   metadata.ImageType
	}
	per := map[key][]string{}
	for _, img := range s.Images {
		if img.Season == nil {
			continue
		}
		k := key{*img.Season, img.SeasonOrder, img.Type}
		per[k] = append(per[k], img.URL)
	}
	for k, urls := range per {
		assert.LessOrEqual(t, len(urls), 3, "%+v", k)
	}
	assert.Equal(t, []string{
		"https://artworks.thetvdb.com/banners/series/78804/seasons/1802912/posters/62078006.jpg",
		"https://artworks.thetvdb.com/banners/series/78804/seasons/1802912/posters/62024686.jpg",
		"https://artworks.thetvdb.com/banners/series/78804/seasons/1802912/posters/62078005.jpg",
	}, per[key{12, "official", metadata.ImageTypePoster}], "the season list's poster, which is also its first artwork, once")
	assert.Equal(t, "https://artworks.thetvdb.com/banners/seasons/5bbd0422a8706.jpg",
		per[key{1, "official", metadata.ImageTypePoster}][0], "the season list's poster first")
	assert.Len(t, per[key{1, "official", metadata.ImageTypePoster}], 3)
	assert.Equal(t, []string{"https://artworks.thetvdb.com/banners/seasonswide/78804-1.jpg"},
		per[key{1, "official", metadata.ImageTypeBanner}])
	assert.Len(t, per[key{0, "dvd", metadata.ImageTypeBanner}], 3)
}

func TestEpisodesCarryTheirStill(t *testing.T) {
	eps, err := recordedTVDB(t).Episodes(context.Background(), "78804", "default")
	require.NoError(t, err)
	for _, e := range eps {
		if e.SeasonNumber == 1 && e.EpisodeNumber == 1 {
			require.NotNil(t, e.Image)
			assert.Equal(t, metadata.ImageTypeScreenshot, e.Image.Type)
			assert.Equal(t, "https://artworks.thetvdb.com/banners/episodes/78804/64e9f6d45a0b7.jpg", e.Image.URL)
			assert.Equal(t, "295294", e.IDs[metadata.KeyTVDB], "the episode's own TVDB id, which Episode status.tvdbID reads")
			return
		}
	}
	t.Fatal("no S01E01 in the fixture")
}

func TestEpisodePeopleFilesGuestsDirectorsAndWriters(t *testing.T) {
	people, err := recordedTVDB(t).EpisodePeople(context.Background(), "295294")
	require.NoError(t, err)
	byKind := map[metadata.PersonKind][]string{}
	for _, p := range people {
		byKind[p.Kind] = append(byKind[p.Kind], p.Name)
	}
	assert.Equal(t, []string{"Keith Boak"}, byKind[metadata.PersonDirector])
	assert.Equal(t, []string{"Russell T. Davies"}, byKind[metadata.PersonWriter])
	assert.Contains(t, byKind[metadata.PersonCast], "Mark Benton")
}
