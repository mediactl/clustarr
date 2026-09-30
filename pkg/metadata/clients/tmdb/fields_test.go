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

package tmdb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

// recorded serves the fixtures hack/record-metadata-fixtures recorded from
// the live API, keyed by path and the language query parameter, and counts
// the requests it answers.
type recorded struct {
	mu       sync.Mutex
	requests []string
}

func (rec *recorded) client(t *testing.T) *tmdb.Client {
	t.Helper()
	files := map[string]string{
		"/movie/79120|en-US":  "movie-79120.json",
		"/movie/79120|en-GB":  "movie-79120.json",
		"/movie/105|en-US":    "movie-105.json",
		"/movie/372058|en-US": "movie-372058.json",
		"/movie/372058|ja":    "movie-372058-ja.json",
		"/tv/57243|en-US":     "tv-57243.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path + "|" + r.URL.Query().Get("language")
		rec.mu.Lock()
		rec.requests = append(rec.requests, key)
		rec.mu.Unlock()
		name, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile("../../../../test/data/metadata/tmdb/" + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	return c
}

func (rec *recorded) Requests() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.requests...)
}

func people(m *metadata.Movie, kind metadata.PersonKind) []metadata.Person {
	var out []metadata.Person
	for _, p := range m.People {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// TestWeekendGetsItsCertification is the bug the full-metadata spec opened
// with: no Movie ever had a certification. Weekend (2011) is rated 18 in
// the UK and NR in the US; with no region its origin (GB) decides.
func TestWeekendGetsItsCertification(t *testing.T) {
	rec := &recorded{}
	c := rec.client(t)

	m, err := c.Movie(context.Background(), "79120", "")
	require.NoError(t, err)
	assert.Equal(t, "18", m.Certification, "no region: the origin country's rating")
	assert.Equal(t, "GB", m.CertificationCountry, "the chosen rating's own country, which Plex's contentRating prefixes")
	assert.Equal(t, "en", m.Language, "the language the document was fetched in")
	assert.Contains(t, m.Certifications, metadata.Certification{Country: "GB", Rating: "18"})
	assert.Contains(t, m.Certifications, metadata.Certification{Country: "US", Rating: "NR"})

	m, err = c.Movie(context.Background(), "79120", "GB")
	require.NoError(t, err)
	assert.Equal(t, "18", m.Certification)

	m, err = c.WithLocale("en", "US").Movie(context.Background(), "79120", "")
	require.NoError(t, err)
	assert.Equal(t, "NR", m.Certification, "the configured region wins when it has a rating")
	assert.Equal(t, "US", m.CertificationCountry)
}

func TestMovieMapsTheFullMetadataFields(t *testing.T) {
	rec := &recorded{}
	m, err := rec.client(t).Movie(context.Background(), "79120", "")
	require.NoError(t, err)

	assert.Equal(t, "A (sort of) love story between two guys over a cold weekend in October.", m.Tagline)
	assert.Equal(t, []string{"The Bureau", "Glendale Picture Company", "Synchronicity Films"}, m.Studios[:3])
	assert.Equal(t, []string{"United Kingdom"}, m.Countries)
	assert.False(t, m.Adult)

	cast := people(m, metadata.PersonCast)
	require.Len(t, cast, 16)
	assert.EqualValues(t, 0, cast[0].Order)
	assert.NotEmpty(t, cast[0].Character)
	directors := people(m, metadata.PersonDirector)
	require.NotEmpty(t, directors)
	assert.Equal(t, "Andrew Haigh", directors[0].Name)
	assert.NotEmpty(t, people(m, metadata.PersonWriter))

	require.Len(t, m.Similar, 20)
	for _, s := range m.Similar {
		assert.NotEmpty(t, s.IDs[metadata.KeyTMDB], s.Title)
	}
	assert.Equal(t, []string{"/movie/79120|en-US"}, rec.Requests(),
		"original language en is the configured one: no second call")
}

func TestMovieKeepsLanguageTaggedLogos(t *testing.T) {
	rec := &recorded{}
	m, err := rec.client(t).Movie(context.Background(), "105", "US")
	require.NoError(t, err)
	assert.Equal(t, "PG", m.Certification)
	var logos int
	for _, img := range m.Images {
		if img.Type == metadata.ImageTypeLogo {
			logos++
			assert.NotEmpty(t, img.Language)
		}
	}
	assert.Positive(t, logos)
}

// TestMovieFetchesItsOriginalLanguage: Your Name (2016) is Japanese, so a
// second call in ja brings the original genres and the Japanese images.
func TestMovieFetchesItsOriginalLanguage(t *testing.T) {
	rec := &recorded{}
	m, err := rec.client(t).Movie(context.Background(), "372058", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"/movie/372058|en-US", "/movie/372058|ja"}, rec.Requests())
	assert.Equal(t, []string{"アニメーション", "ロマンス", "ドラマ"}, m.OriginalGenres)
	var ja int
	for _, img := range m.Images {
		if img.Language == "ja" {
			ja++
		}
	}
	assert.Positive(t, ja)
}

// A series comes from TVDB, which has no tagline; TMDB's tv record does
// (spec 2026-09-30 §3.4), and the gateway asks for it by the TMDB id the
// TVDB record carries.
func TestSeriesTaglineComesFromTMDBsTVRecord(t *testing.T) {
	rec := &recorded{}
	tagline, err := rec.client(t).SeriesTagline(context.Background(), metadata.ExternalIDs{metadata.KeyTMDB: "57243"})
	require.NoError(t, err)
	assert.Equal(t, "Space. For all.", tagline)
	assert.Equal(t, []string{"/tv/57243|en-US"}, rec.Requests())

	tagline, err = rec.client(t).SeriesTagline(context.Background(), metadata.ExternalIDs{metadata.KeyTVDB: "78804"})
	require.NoError(t, err, "no TMDB id: nothing to ask")
	assert.Empty(t, tagline)
}
