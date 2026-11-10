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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audible"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/coverart"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
	"github.com/mediactl/clustarr/pkg/metadata/clients/wikipedia"
)

const recordedDir = "../../data/metadata"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func noLimit() *rate.Limiter { return metadata.NewLimiter(rate.Inf, 1) }

// TestMusicBrainzArtistAndAlbums drives the fixture with the real
// pkg/metadata/clients/musicbrainz client -- the same code catalog's
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

// TestMusicBrainzArtistAnswersTheFullRecording: the artist lookup serves
// the recording made with every sub-resource the gateway asks for
// (musicbrainz.go's artistIncludes), so the real client maps the genres,
// aliases and url-rels -- the wikidata one among them -- that
// ArtistMetadata and artist.nfo read (2026-11-10 design §5.4, §8.1).
func TestMusicBrainzArtistAnswersTheFullRecording(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL+"/musicbrainz", noLimit())
	require.NoError(t, err)

	a, err := c.Artist(t.Context(), RadioheadMBID)
	require.NoError(t, err)
	require.Equal(t, "Radiohead", a.Name)
	require.NotEmpty(t, a.Genres, "the full recording carries genres")
	require.NotEmpty(t, a.Aliases, "the full recording carries aliases")
	var wikidata string
	for _, l := range a.Links {
		if l.Type == wikipedia.LinkWikidata {
			wikidata = l.URL
		}
	}
	require.Equal(t, "https://www.wikidata.org/wiki/"+RadioheadWikidataID, wikidata,
		"the wikidata url-rel is what the biography lookup follows")
}

// TestMusicBrainzAlbumsPagesEveryReleaseGroup drives the release-group
// browse the way the gateway does -- pkg/metadata/clients/musicbrainz's
// Albums, 100 a page, offset by what came before -- against the artist
// whose recording spans two pages, and proves the stub serves each page by
// its offset: 175 release groups, none twice.
func TestMusicBrainzAlbumsPagesEveryReleaseGroup(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL+"/musicbrainz", noLimit())
	require.NoError(t, err)

	a, err := c.Artist(t.Context(), MuseMBID)
	require.NoError(t, err)
	require.Equal(t, "Muse", a.Name)

	albums, err := c.Albums(t.Context(), MuseMBID)
	require.NoError(t, err)
	require.Len(t, albums, MuseReleaseGroupCount)
	seen := map[string]bool{}
	for _, al := range albums {
		require.False(t, seen[al.IDs[metadata.KeyMBReleaseGroup]], "release group %s listed twice: a page served for the wrong offset", al.IDs[metadata.KeyMBReleaseGroup])
		seen[al.IDs[metadata.KeyMBReleaseGroup]] = true
	}

	// An offset no page starts at is refused, never answered with the
	// first page again.
	resp, err := http.Get(srv.URL + "/musicbrainz/release-group/?artist=" + MuseMBID + "&offset=50&limit=100") //nolint:noctx,gosec
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestCoverArtArchiveServesKidAsFrontCover drives the Cover Art Archive
// route with the real pkg/metadata/clients/coverart client: Kid A's
// release group answers its approved front cover as the poster, and a
// release group the archive has no art for is metadata.ErrNotFound, as the
// live archive's 404 reads.
func TestCoverArtArchiveServesKidAsFrontCover(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL + "/coverart", Limiter: noLimit()})

	imgs, err := c.Artwork(t.Context(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: KidAReleaseGroupMBID})
	require.NoError(t, err)
	require.Len(t, imgs, 1)
	require.Equal(t, metadata.ImageTypePoster, imgs[0].Type)
	require.Contains(t, imgs[0].URL, "57e1d657-a38d-3de5-b33e-45b661b0d5b0", "the cover is Kid A's own GB release's")

	// The recorded Portishead index is served under its own id too.
	imgs, err = c.Artwork(t.Context(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: "1b022e01-4da6-387b-8658-8678046e4cef"})
	require.NoError(t, err)
	require.NotEmpty(t, imgs)

	_, err = c.Artwork(t.Context(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: "00000000-0000-0000-0000-000000000000"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

// TestWikipediaBiographyThroughWikidata drives both halves of the
// biography lookup -- the Wikidata entity's sitelink, then the page's
// summary -- with the real pkg/metadata/clients/wikipedia client pointed at
// the stub for both hosts, for the Radiohead link the full MusicBrainz
// recording carries and for the recorded Muse one; an entity the stub has
// no sitelink for is metadata.ErrNotFound.
func TestWikipediaBiographyThroughWikidata(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c := wikipedia.New(wikipedia.Config{
		HTTPClient: srv.Client(), WikidataBaseURL: srv.URL + "/wikipedia", WikipediaBaseURL: srv.URL + "/wikipedia", Limiter: noLimit(),
	})

	text, source, err := c.Biography(t.Context(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/" + RadioheadWikidataID}})
	require.NoError(t, err)
	require.Equal(t, wikipedia.Source, source)
	require.Contains(t, text, "Radiohead are an English rock band")

	text, _, err = c.Biography(t.Context(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/" + MuseWikidataID}})
	require.NoError(t, err)
	require.Contains(t, text, "Muse")

	_, _, err = c.Biography(t.Context(), []metadata.Link{{Type: wikipedia.LinkWikidata, URL: "https://www.wikidata.org/wiki/Q999999999"}})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

// TestAudibleSearchAnswersTheRecordedCatalog drives the catalog search
// with the real pkg/metadata/clients/audible client: the recorded title
// answers its products, keyed by ASIN, and a title the stub never recorded
// answers no hits rather than an error.
func TestAudibleSearchAnswersTheRecordedCatalog(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c := audible.New(audible.Config{HTTPClient: srv.Client(), BaseURL: srv.URL + "/audible", Limiter: noLimit()})

	hits, err := c.SearchAudiobooks(t.Context(), "The Left Hand of Darkness", "us")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Equal(t, "B01NH0D987", hits[0].IDs[metadata.KeyASIN])
	require.Equal(t, "The Left Hand of Darkness", hits[0].Title)

	hits, err = c.SearchAudiobooks(t.Context(), "A Title Nobody Recorded", "us")
	require.NoError(t, err)
	require.Empty(t, hits)
}

// TestAudnexusAuthor drives the author route with the real
// pkg/metadata/clients/audnexus client (AudiobookAuthor, the registry's
// AudiobookAuthors provider).
func TestAudnexusAuthor(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	c := audnexus.New(srv.Client(), srv.URL+"/audnexus", noLimit())

	author, err := c.AudiobookAuthor(t.Context(), AudnexusAuthorASIN, "us")
	require.NoError(t, err)
	require.Equal(t, "J.K. Rowling", author.Name)
}
