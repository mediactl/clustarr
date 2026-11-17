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

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// recordedProvider serves a provider's recorded responses by path.
func recordedProvider(t *testing.T, dir string, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile("../../../test/data/metadata/" + dir + "/" + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A series whose TheTVDB record has no logo in the titles' language takes
// its TMDB tv record's, and its titled poster is drawn on a poster that
// says it has no text (owner, 2026-11-17). A Livid Lady's Guide to Getting
// Even, both records as recorded that day: TheTVDB lists a Japanese logo
// alone and scores a Japanese poster first, so the series had no logo, no
// titled poster and that poster in Plex. Through the real clients, enrich
// and the status selection: the Japanese poster is no candidate, TheTVDB's
// two posters without text lead TMDB's two, and the logo is TMDB's English
// one.
func TestASeriesTakesItsLogoAndTextlessPostersFromTMDB(t *testing.T) {
	tvdbSrv := recordedProvider(t, "tvdb", map[string]string{
		"/login":                  "login.json",
		"/series/467005/extended": "series-467005-extended.json",
	})
	tmdbSrv := recordedProvider(t, "tmdb", map[string]string{"/tv/298831": "tv-298831.json"})
	series := tvdb.New("test-key", "test-pin", tvdbSrv.Client(), tvdbSrv.URL, pkgmetadata.NewLimiter(rate.Inf, 1)).WithLocale("en", "US")
	extras, err := tmdb.New("test-key", tmdbSrv.Client(), tmdbSrv.URL, pkgmetadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	doc, err := series.Series(context.Background(), "467005")
	require.NoError(t, err)
	require.Equal(t, "298831", doc.IDs[pkgmetadata.KeyTMDB], "the record names its TMDB id")

	titled := func(doc *pkgmetadata.Series) (posters []string, logo string, ok bool) {
		var images []catalogv1alpha1.Image
		for _, img := range selectImages(doc.Images, maxItemImages) {
			if img.Season != nil {
				continue
			}
			typ, known := mapImageType(img.Type)
			require.True(t, known)
			images = append(images, catalogv1alpha1.Image{Type: typ, URL: img.URL, Language: img.Language})
		}
		ps, l, ok := catalogartwork.Titled(images, doc.Language)
		for _, p := range ps {
			posters = append(posters, p.URL)
		}
		return posters, l.URL, ok
	}

	_, _, ok := titled(doc)
	require.False(t, ok, "TheTVDB alone: no logo in the titles' language, so no titled poster")

	enrich(context.Background(), &pkgmetadata.Registry{Extras: []pkgmetadata.SeriesExtrasProvider{extras}}, commonv1.MediaKindSeries, nil, nil, doc)

	posters, logo, ok := titled(doc)
	require.True(t, ok)
	require.Equal(t, "https://image.tmdb.org/t/p/original/6QtzCX46kZ6TbvxiSA3Qeok53Fz.png", logo)
	require.Equal(t, []string{
		"https://artworks.thetvdb.com/banners/v4/series/467005/posters/694e6f156a072.jpg",
		"https://artworks.thetvdb.com/banners/v4/series/467005/posters/6a50319ee4210.jpg",
		"https://image.tmdb.org/t/p/w500/iSunmtMOna6Mj2DNX6mtAt01aRP.jpg",
		"https://image.tmdb.org/t/p/w500/pB5hzvvNQlZKmOmdQzFFwn3tNBU.jpg",
	}, posters, "no poster with a language's text is tried")
	require.Equal(t, []string{"anime", "based on light novel"}, doc.Keywords, "the same request's keywords")

	// The poster it falls back to, should every candidate carry text, is
	// TMDB's English one: TheTVDB's own first is the Japanese one.
	item := catalogartwork.Item{TitledPoster: true, Language: doc.Language}
	for _, img := range selectImages(doc.Images, maxItemImages) {
		if typ, known := mapImageType(img.Type); known && img.Season == nil {
			item.Images = append(item.Images, catalogv1alpha1.Image{Type: typ, URL: img.URL, Language: img.Language})
		}
	}
	plain := item.Sources()[catalogv1alpha1.ImageTypePoster].Plain
	require.NotNil(t, plain)
	require.Equal(t, "https://image.tmdb.org/t/p/w500/qKICBVfmdC1M9A4NdzQrUabUBtW.jpg", plain.URL)
}

// A series' logo and the poster it falls back to are TMDB's in the titles'
// language, ahead of the series provider's own (owner, 2026-11-17: "we can
// standardize on TMDB for english logos", "tmdb may also be used for
// falling back to english language posters"); its background stays the
// series provider's, and what else TMDB lists -- the posters without text,
// a logo in no language -- follows. A URL both list is kept once.
func TestASeriesLogoIsTMDBsInTheTitlesLanguage(t *testing.T) {
	img := func(typ pkgmetadata.ImageType, lang, url string) pkgmetadata.Image {
		return pkgmetadata.Image{Type: typ, Language: lang, URL: url}
	}
	own := []pkgmetadata.Image{
		img(pkgmetadata.ImageTypePoster, "en", "tvdb/poster"),
		img(pkgmetadata.ImageTypeLogo, "en", "tvdb/logo"),
		img(pkgmetadata.ImageTypeFanart, "", "tvdb/fanart"),
	}
	extra := []pkgmetadata.Image{
		img(pkgmetadata.ImageTypeLogo, "", "tmdb/logo-untagged"),
		img(pkgmetadata.ImageTypeLogo, "en", "tmdb/logo-en"),
		img(pkgmetadata.ImageTypePoster, "", "tmdb/poster-textless"),
		img(pkgmetadata.ImageTypePoster, "en", "tmdb/poster-en"),
		img(pkgmetadata.ImageTypeFanart, "", "tvdb/fanart"),
		img(pkgmetadata.ImageTypeFanart, "", "tmdb/fanart"),
	}
	require.Equal(t, []pkgmetadata.Image{
		img(pkgmetadata.ImageTypeLogo, "en", "tmdb/logo-en"),
		img(pkgmetadata.ImageTypePoster, "en", "tmdb/poster-en"),
		img(pkgmetadata.ImageTypePoster, "en", "tvdb/poster"),
		img(pkgmetadata.ImageTypeLogo, "en", "tvdb/logo"),
		img(pkgmetadata.ImageTypeFanart, "", "tvdb/fanart"),
		img(pkgmetadata.ImageTypeLogo, "", "tmdb/logo-untagged"),
		img(pkgmetadata.ImageTypePoster, "", "tmdb/poster-textless"),
		img(pkgmetadata.ImageTypeFanart, "", "tmdb/fanart"),
	}, withExtraImages(own, extra, "en"))
	require.Equal(t, own, withExtraImages(own, nil, "en"), "TMDB answered nothing: the series' own")
}
