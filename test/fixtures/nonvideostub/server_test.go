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

package nonvideostub

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

const recordedDir = "../../data/metadata"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func noLimit() *rate.Limiter { return metadata.NewLimiter(rate.Inf, 1) }

// TestMusicBrainzArtistAndAlbums drives the fixture with the real
// pkg/metadata/clients/musicbrainz client -- the same code catalogarr's
// metadata gateway runs -- for the artist lookup and both release-group
// shapes (browse and single lookup) the "/release-group/" route answers.
func TestMusicBrainzArtistAndAlbums(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL+"/musicbrainz", noLimit())
	require.NoError(t, err)

	a, err := c.Artist(t.Context(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")
	require.NoError(t, err)
	require.Equal(t, "Radiohead", a.Name)

	albums, err := c.Albums(t.Context(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")
	require.NoError(t, err)
	require.Len(t, albums, 1)
	require.Equal(t, "Kid A", albums[0].Title)

	album, err := c.Album(t.Context(), "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6")
	require.NoError(t, err)
	require.Equal(t, "Kid A", album.Title)
	// The release browse is Kid A's own, not another album's.
	require.Len(t, album.Releases, 3)
	require.NotEmpty(t, album.Releases[0].Media)
	require.NotEmpty(t, album.Releases[0].Media[0].Tracks)
	require.Equal(t, "Everything in Its Right Place", album.Releases[0].Media[0].Tracks[0].Title)
}

// TestOpenLibraryAuthorWorksAndISBN drives the fixture with the real
// pkg/metadata/clients/openlibrary client across every route it uses.
func TestOpenLibraryAuthorWorksAndISBN(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL+"/openlibrary", noLimit())

	author, err := c.Author(t.Context(), metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL21594A"})
	require.NoError(t, err)
	require.NotEmpty(t, author.Name)

	books, err := c.Books(t.Context(), "OL21594A")
	require.NoError(t, err)
	require.NotEmpty(t, books)

	book, err := c.Book(t.Context(), metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL138052W"})
	require.NoError(t, err)
	require.NotEmpty(t, book.Title)

	edition, err := c.Edition(t.Context(), metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})
	require.NoError(t, err)
	require.NotEmpty(t, edition.Title)

	hits, err := c.SearchBooks(t.Context(), "Pride and Prejudice")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
}

// TestAudnexusBookAndChapters drives the fixture with the real
// pkg/metadata/clients/audnexus client.
func TestAudnexusBookAndChapters(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c := audnexus.New(srv.Client(), srv.URL+"/audnexus", noLimit())

	book, err := c.Audiobook(t.Context(), "B0036I54I6", "us")
	require.NoError(t, err)
	require.NotEmpty(t, book.Title)

	chapters, err := c.Chapters(t.Context(), "B0036I54I6", "us")
	require.NoError(t, err)
	require.NotEmpty(t, chapters)
}

// TestComicVineRequiresTheAPIKey drives the fixture with the real
// pkg/metadata/clients/comicvine client, and proves the api_key gate is
// real: the wrong key must fail, the right key must succeed.
func TestComicVineRequiresTheAPIKey(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	good := comicvine.New(ComicVineAPIKey, srv.Client(), srv.URL+"/comicvine", noLimit())
	vol, err := good.Volume(t.Context(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})
	require.NoError(t, err)
	require.NotEmpty(t, vol.Title)

	issues, err := good.Issues(t.Context(), "4050-18257")
	require.NoError(t, err)
	require.NotEmpty(t, issues)

	hits, err := good.SearchVolumes(t.Context(), "Batman")
	require.NoError(t, err)
	require.NotEmpty(t, hits)

	bad := comicvine.New("wrong-key", srv.Client(), srv.URL+"/comicvine", noLimit())
	_, err = bad.Volume(t.Context(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})
	require.Error(t, err)
}

// TestComicVineIssuesRejectsThePrefixedGuidFilter proves the fixture
// enforces ComicVine's real filter=volume:{id} shape -- the bare numeric
// id, never the prefixed guid -- rather than accepting either, which is
// the exact blind spot that let G2-5's Comic->Issue shape defect ship: a
// fake that accepts both a resource's own id form and its filter form
// cannot distinguish "the caller sent the right shape" from "the caller
// sent the shape the OTHER endpoint wants."
func TestComicVineIssuesRejectsThePrefixedGuidFilter(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/comicvine/issues/?api_key=" + ComicVineAPIKey + "&filter=volume:4050-18257") //nolint:noctx,gosec
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestComicVineVolumeWithoutItsSlashIsRedirected: the canonical volume path
// carries ComicVine's trailing slash, and the unslashed one gets the 301 to
// it the real API answers; the bare numeric id is still not a volume.
func TestComicVineVolumeWithoutItsSlashIsRedirected(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get(srv.URL + "/comicvine/volume/4050-18257?api_key=" + ComicVineAPIKey) //nolint:noctx
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	require.Equal(t, "/comicvine/volume/4050-18257/?api_key="+ComicVineAPIKey, resp.Header.Get("Location"))

	resp, err = noFollow.Get(srv.URL + "/comicvine/volume/18257/?api_key=" + ComicVineAPIKey) //nolint:noctx
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestUnrecordedRouteFourOhFours proves an id this fixture never recorded
// 404s loudly rather than hanging a caller.
func TestUnrecordedRouteFourOhFours(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/musicbrainz/artist/00000000-0000-0000-0000-000000000000") //nolint:noctx,gosec
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
