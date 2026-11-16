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

package wikipedia_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/wikipedia"
)

const fixtures = "../../../../test/data/metadata/wikipedia/"

// serve answers the Wikidata API and the Wikipedia summary route from the
// recorded fixtures (Muse, 2026-11-10), recording every request.
func serve(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		var name string
		status := http.StatusOK
		switch {
		case r.URL.Path == "/w/api.php" && r.URL.Query().Get("ids") == "Q22151":
			name = "muse_wikidata.json"
		case r.URL.Path == "/w/api.php" && r.URL.Query().Get("ids") == "Q999999999":
			name = "nosite_wikidata.json"
		case r.URL.Path == "/api/rest_v1/page/summary/Muse_(band)" || r.URL.Path == "/api/rest_v1/page/summary/Muse_%28band%29":
			name = "muse_summary.json"
		default:
			name, status = "summary_404.json", http.StatusNotFound
		}
		body, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func client(srv *httptest.Server) *wikipedia.Client {
	return wikipedia.New(wikipedia.Config{HTTPClient: srv.Client(), WikidataBaseURL: srv.URL, WikipediaBaseURL: srv.URL})
}

// A Wikidata url-rel (MusicBrainz's for Muse) leads through the entity's
// enwiki sitelink to the page summary, two requests.
func TestBiographyThroughWikidata(t *testing.T) {
	srv, seen := serve(t)
	text, source, err := client(srv).Biography(context.Background(), []metadata.Link{
		{Type: "discogs", URL: "https://www.discogs.com/artist/1003"},
		{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q22151"},
	})
	require.NoError(t, err)
	assert.Equal(t, wikipedia.Source, source)
	assert.True(t, len(text) > 100 && text[:28] == "Muse are an English rock ban", text)
	require.Len(t, *seen, 2)
	assert.Contains(t, (*seen)[0], "action=wbgetentities")
	assert.Contains(t, (*seen)[0], "ids=Q22151")
	assert.Contains(t, (*seen)[0], "sitefilter=enwiki")
	assert.Contains(t, (*seen)[1], "/api/rest_v1/page/summary/Muse_")
}

// A Wikipedia link of the client's language names the page itself: one
// request, the Wikidata hop skipped; another edition's link is ignored.
func TestBiographyThroughAWikipediaLink(t *testing.T) {
	srv, seen := serve(t)
	text, _, err := client(srv).Biography(context.Background(), []metadata.Link{
		{Type: wikipedia.LinkWikipedia, URL: "https://de.wikipedia.org/wiki/Muse_(Band)"},
		{Type: wikipedia.LinkWikipedia, URL: "https://en.wikipedia.org/wiki/Muse_(band)"},
		{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q22151"},
	})
	require.NoError(t, err)
	assert.Contains(t, text, "Teignmouth")
	require.Len(t, *seen, 1)
	assert.Contains(t, (*seen)[0], "/page/summary/Muse_")
}

// No usable link, an entity without an English page, and a page that is
// gone are each metadata.ErrNotFound, never a guess.
func TestNotFoundCases(t *testing.T) {
	srv, _ := serve(t)
	c := client(srv)
	_, _, err := c.Biography(context.Background(), []metadata.Link{{Type: "homepage", URL: "https://muse.mu/"}})
	assert.ErrorIs(t, err, metadata.ErrNotFound)
	_, _, err = c.Biography(context.Background(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q999999999"}})
	assert.ErrorIs(t, err, metadata.ErrNotFound)
	_, _, err = c.Biography(context.Background(), []metadata.Link{{Type: wikipedia.LinkWikipedia, URL: "https://en.wikipedia.org/wiki/Zzzz_nonexistent_page"}})
	assert.ErrorIs(t, err, metadata.ErrNotFound)
	_, _, err = c.Biography(context.Background(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Special:Search"}})
	assert.ErrorIs(t, err, metadata.ErrNotFound, "not an entity id")
}

// servePortraits answers Wikidata's wbgetclaims and Commons' imageinfo from
// responses recorded on 2026-11-16 (Three 6 Mafia's 688-pixel photo, which
// Commons serves whole; Com Truise's 3462-pixel one, served as its 1280-pixel
// rendition; an entity with no image), recording every request.
func servePortraits(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		q := r.URL.Query()
		var name string
		switch {
		case q.Get("action") == "wbgetclaims" && q.Get("property") == "P18" && q.Get("entity") == "Q1027165":
			name = "three6mafia_p18.json"
		case q.Get("action") == "wbgetclaims" && q.Get("property") == "P18" && q.Get("entity") == "Q1113156":
			name = "comtruise_p18.json"
		case q.Get("action") == "wbgetclaims" && q.Get("property") == "P18":
			name = "noimage_p18.json"
		case q.Get("action") == "query" && q.Get("titles") == "File:Three 6 Mafia.jpg" && q.Get("iiurlwidth") == "1000":
			name = "three6mafia_imageinfo.json"
		case q.Get("action") == "query" && q.Get("titles") == "File:Com-Truise-2018-03-09-0002.jpg" && q.Get("iiurlwidth") == "1000":
			name = "comtruise_imageinfo.json"
		default:
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// An artist's photo is its Wikidata entity's image (P18) at Commons'
// rendition of about 1000 pixels -- the whole file when it is smaller --
// never the uploaded original, without Commons' tracking parameters (owner,
// 2026-11-16: 157 of 387 artists had no fanart.tv portrait, 111 of them a
// Wikidata image).
func TestPortraitIsTheWikidataImageAtCommonsRendition(t *testing.T) {
	srv, seen := servePortraits(t)
	c := wikipedia.New(wikipedia.Config{HTTPClient: srv.Client(), WikidataBaseURL: srv.URL, CommonsBaseURL: srv.URL})

	img, err := c.Portrait(context.Background(), []metadata.Link{
		{Type: "discogs", URL: "https://www.discogs.com/artist/1"},
		{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q1113156"},
	})
	require.NoError(t, err)
	assert.Equal(t, metadata.Image{
		Type:  metadata.ImageTypePoster,
		URL:   "https://thumb.wikimedia.org/wikipedia/commons/thumb/4/4d/Com-Truise-2018-03-09-0002.jpg/1280px-Com-Truise-2018-03-09-0002.jpg",
		Width: 1000, Height: 667,
	}, img)
	assert.Len(t, *seen, 2, "the entity's image claim, then the file's rendition")

	img, err = c.Portrait(context.Background(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q1027165"}})
	require.NoError(t, err)
	assert.Equal(t, "https://upload.wikimedia.org/wikipedia/commons/d/df/Three_6_Mafia.jpg", img.URL, "a photo smaller than the rendition is served whole")

	_, err = c.Portrait(context.Background(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q100119314"}})
	require.ErrorIs(t, err, metadata.ErrNotFound, "an entity with no image")
	_, err = c.Portrait(context.Background(), []metadata.Link{{Type: "discogs", URL: "https://www.discogs.com/artist/1"}})
	require.ErrorIs(t, err, metadata.ErrNotFound, "no Wikidata link")
}
