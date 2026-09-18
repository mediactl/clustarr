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

package cardigann_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// testClock is the fixed instant every Search/Download/Login test in this
// file passes as Engine.Now, so date-relative fixtures ("12:25am") assert
// an exact result instead of one that depends on the day the suite runs.
var testClock = time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)

func TestEngineSearch1337xHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(readTestdataBytes(t, "1337x-search.html"))
	}))
	defer srv.Close()

	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client(), Now: func() time.Time { return testClock }}
	rels, err := eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "some movie"})
	require.NoError(t, err)
	// 1337x.yml declares four SearchPathBlock entries (movies/tv/music/other
	// pages) with no `categories:` restriction on any of them, so all four
	// always fire regardless of the query — this is what the real file does,
	// not a simplification (see search.go's pathMatches doc comment). The
	// fixture server answers every request with the same 3-row table, so a
	// correct Engine.Search returns 4*3 = 12 raw rows; Engine never dedupes
	// across paths or indexers (that is indexarr's search worker's job, per
	// note §5's DeDupeReleases/infohash-then-guid rule), so 12 is the right
	// assertion here, not 3.
	require.Len(t, rels, 12)

	movie := findRelease(t, rels, "Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP")
	assert.Contains(t, movie.Categories, newznab.CatMoviesHD)
	size, err := cardigann.GetBytes("4.2 GB")
	require.NoError(t, err)
	assert.Equal(t, size, movie.Size)
	require.NotNil(t, movie.Seeders)
	assert.EqualValues(t, 128, *movie.Seeders)
	assert.Equal(t, 2026, movie.PubDate.Year()) // 12:25am -> fuzzytime "today", relative to the fixed Engine.Now

	truncated := findRelease(t, rels, "Another.Show.S02E05.720p.HDTV.x264-GRP2") // title_optional wins: href-derived, not the "..." display text
	assert.Contains(t, truncated.Categories, newznab.CatTVHD)
	assert.Equal(t, time.September, truncated.PubDate.Month()) // "7am Sep. 14th" -> dateparse "htt MMM. d" (no year token -> filled from Engine.Now, not asserted)

	old := findRelease(t, rels, "Old.Documentary.2019.DVDRip.x264-GRP3")
	assert.Contains(t, old.Categories, newznab.CatMoviesDVD)
	assert.Equal(t, 2011, old.PubDate.Year()) // "Apr. 18th '11" -> dateparse "MMM. d yy"
}

func findRelease(t *testing.T, rels []torznab.Release, title string) torznab.Release {
	t.Helper()
	for _, r := range rels {
		if r.Title == title {
			return r
		}
	}
	t.Fatalf("no release titled %q in %d results", title, len(rels))
	return torznab.Release{}
}

func TestBuildKeywordsAppendsSeasonEpisodeOnlyWhenBothSet(t *testing.T) {
	cases := []struct {
		name  string
		query cardigann.Query
		want  string
	}{
		{"q only", cardigann.Query{Q: "some movie"}, "some movie"},
		{"q plus season and episode", cardigann.Query{Q: "some show", Season: "1", Ep: "5"}, "some show S01E05"},
		{"q plus year leaves keywords alone", cardigann.Query{Q: "some movie", Year: 2024}, "some movie"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// buildKeywords is unexported; exercised indirectly through
			// Engine.Search's own request construction would need a live
			// server per case, so this asserts through a definition whose
			// single search path echoes .Keywords straight into the URL.
			def := &cardigann.Definition{
				Search: cardigann.SearchBlock{
					Path: "search/{{ .Keywords }}/",
					Rows: cardigann.RowsBlock{SelectorBlock: cardigann.SelectorBlock{Selector: "tr"}},
					Fields: cardigann.OrderedFields{
						{Name: "title", Block: cardigann.SelectorBlock{Selector: "td"}},
						{Name: "size", Block: cardigann.SelectorBlock{Text: scalarPtr("0")}},
						{Name: "seeders", Block: cardigann.SelectorBlock{Text: scalarPtr("0")}},
						{Name: "category", Block: cardigann.SelectorBlock{Text: scalarPtr("1")}},
						{Name: "download", Block: cardigann.SelectorBlock{Text: scalarPtr("magnet:?xt=urn:btih:x")}},
					},
				},
			}
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Write([]byte(`<table></table>`))
			}))
			defer srv.Close()
			cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
			require.NoError(t, err)
			eng := cardigann.Engine{HTTP: srv.Client()}
			_, err = eng.Search(context.Background(), def, cfg, tc.query)
			require.NoError(t, err)
			assert.Equal(t, "/search/"+tc.want+"/", gotPath)
		})
	}
}

func TestEngineSearch0dayfilesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.Empty(t, r.URL.Query().Get("seasonNumber")) // AllowEmptyInputs is unset -> omitted, not "seasonNumber="
		w.Header().Set("Content-Type", "application/json")
		w.Write(readTestdataBytes(t, "0dayfiles-search.json"))
	}))
	defer srv.Close()

	def, err := cardigann.Load(readTestdata(t, "0dayfiles-api.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"apikey": "test-key"})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	rels, err := eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "movie-search", Q: "some movie"})
	require.NoError(t, err)
	require.Len(t, rels, 2)

	movie := findRelease(t, rels, "Some.Movie.2024.2160p.UHD.BluRay.x265-GRP.mkv") // single file -> title_filename wins
	assert.Contains(t, movie.Categories, newznab.CatMoviesUHD)
	assert.Equal(t, int64(21474836480), movie.Size) // bare JSON number, already bytes
	require.NotNil(t, movie.DownloadVolumeFactor)
	assert.Equal(t, 0.0, *movie.DownloadVolumeFactor) // 100% freeleech
	require.NotNil(t, movie.UploadVolumeFactor)
	assert.Equal(t, 1.0, *movie.UploadVolumeFactor) // double_upload:false
	assert.Equal(t, 2021, movie.PubDate.Year())
	// The brief's own quoted assertion here is "0133093" (bare digits, no
	// "tt" prefix); torznab.Release.IDs' own doc comment ("the IMDb value
	// carries the canonical tt prefix") and the note's field-mapping text
	// both say imdb/imdbid normalizes to canonical tt%07d form, so this
	// asserts the prefixed form instead — see the task report's deviations
	// section.
	assert.Equal(t, "tt0133093", movie.IDs["imdb"])

	show := findRelease(t, rels, "Some.Show.S01E01.480p.WEB.x264-GRP") // 3 files -> title_optional wins, not the filename
	assert.Contains(t, show.Categories, newznab.CatTVSD)
	require.NotNil(t, show.UploadVolumeFactor)
	assert.Equal(t, 2.0, *show.UploadVolumeFactor) // double_upload:true
	assert.Equal(t, "", show.IDs["imdb"])          // key exists in the JSON with an empty value: a real match, not a miss
}
