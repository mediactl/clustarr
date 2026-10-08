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

package torznabstub

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/torznab"
	"github.com/mediactl/clustarr/test/fixtures/seeder"
)

// testPayloads are the in-cluster payloads config/e2e points the stub at,
// with the info hash of a real seeder content file.
func testPayloads(t *testing.T) Payloads {
	t.Helper()
	clip := filepath.Join(t.TempDir(), "tiny.mkv")
	require.NoError(t, os.WriteFile(clip, bytes.Repeat([]byte("clip"), 300_000), 0o600))
	h, err := seeder.InfoHash(clip)
	require.NoError(t, err)
	return Payloads{
		TorrentURL:      "http://seeder.clustarr-system.svc/fixture.torrent",
		TorrentInfoHash: h.HexString(),
		NZBURL:          "http://nntp-stub-a.clustarr-system.svc/fixture.nzb",
		GoneURL:         "http://torznab-stub.clustarr-system.svc" + PathGone + GUIDGone + ".torrent",
	}
}

func getFrom(t *testing.T, srv *httptest.Server, pathAndQuery string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(srv.URL + pathAndQuery) //nolint:noctx // httptest, no cancellation to model
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode, body
}

func byGUID(rels []torznab.Release) map[string]torznab.Release {
	out := make(map[string]torznab.Release, len(rels))
	for _, r := range rels {
		out[r.GUID] = r
	}
	return out
}

// A movie search for Inception answers releases whose links are the real
// payloads, parsed by the same pkg/torznab the index agent uses: the
// seeder's .torrent under its real info hash (the engine refuses a payload
// whose hash differs) and the gone release on the torrent indexer, the
// NNTP stub's .nzb with no hash on the usenet one. By tmdb id or by imdb id,
// as the agent may send either.
func TestTheDownloadReleasesLinkToRealPayloads(t *testing.T) {
	p := testPayloads(t)
	srv := httptest.NewServer(NewHandler(filepath.Join(t.TempDir(), "requests.jsonl"), discardLogger(), p))
	t.Cleanup(srv.Close)

	for _, q := range []string{"tmdbid=" + InceptionTmdbID, "imdbid=" + InceptionImdbID, "imdbid=tt" + InceptionImdbID} {
		status, body := getFrom(t, srv, PathHealthy+"?t=movie&"+q+"&apikey="+APIKey)
		require.Equal(t, http.StatusOK, status, q)
		rels, err := torznab.ParseResults(bytes.NewReader(body))
		require.NoError(t, err, q)
		require.Len(t, rels, 2, q)
		got := byGUID(rels)
		seed, gone := got[GUIDSeeder], got[GUIDGone]
		assert.Equal(t, TitleSeeder, seed.Title)
		assert.Equal(t, p.TorrentURL, seed.Link)
		assert.Equal(t, p.TorrentInfoHash, seed.InfoHash, "the seeder release must carry the seeder's real hash")
		assert.Equal(t, InceptionTmdbID, seed.IDs[commonv1.IDKeyTMDB])
		assert.Equal(t, "tt"+InceptionImdbID, seed.IDs[commonv1.IDKeyIMDB])
		assert.Equal(t, releaseBytes, seed.Size)
		require.NotNil(t, seed.Seeders)
		assert.Equal(t, TitleGone, gone.Title)
		assert.Equal(t, p.GoneURL, gone.Link)
	}

	status, body := getFrom(t, srv, PathUsenet+"?t=movie&tmdbid="+InceptionTmdbID+"&apikey="+APIKey)
	require.Equal(t, http.StatusOK, status)
	rels, err := torznab.ParseResults(bytes.NewReader(body))
	require.NoError(t, err)
	require.Len(t, rels, 1)
	assert.Equal(t, GUIDNZB, rels[0].GUID)
	assert.Equal(t, TitleNZB, rels[0].Title)
	assert.Equal(t, p.NZBURL, rels[0].Link)
	assert.Empty(t, rels[0].InfoHash, "a usenet release carries no info hash")
	assert.Equal(t, InceptionTmdbID, rels[0].IDs[commonv1.IDKeyTMDB])

	// The usenet personality is a whole indexer: caps, an empty RSS feed,
	// and the apikey demanded like the torrent one.
	status, body = getFrom(t, srv, PathUsenet+"?t=caps&apikey="+APIKey)
	require.Equal(t, http.StatusOK, status)
	caps, err := torznab.ParseCaps(bytes.NewReader(body))
	require.NoError(t, err)
	assert.True(t, caps.Supports(torznab.ModeMovieSearch, "tmdbid"))
	_, body = getFrom(t, srv, PathUsenet+"?t=search&apikey="+APIKey)
	rels, err = torznab.ParseResults(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Empty(t, rels)
	_, body = getFrom(t, srv, PathUsenet+"?t=caps")
	e, err := torznab.ParseError(bytes.NewReader(body))
	require.NoError(t, err)
	require.NotNil(t, e)

	// The gone release's link is a 404, so its grab fails payloadUnavailable.
	status, _ = getFrom(t, srv, PathGone+GUIDGone+".torrent")
	assert.Equal(t, http.StatusNotFound, status)

	// The other personalities and ids answer as before.
	_, body = getFrom(t, srv, PathHealthy+"?t=movie&tmdbid=900100&apikey="+APIKey)
	rels, err = torznab.ParseResults(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Len(t, rels, 3)
}

// A stub with nowhere to point a release advertises none: it never offers a
// payload it cannot deliver.
func TestAStubWithoutPayloadsAdvertisesNoDownloads(t *testing.T) {
	srv := httptest.NewServer(NewHandler("", discardLogger(), Payloads{}))
	t.Cleanup(srv.Close)
	for _, path := range []string{PathHealthy, PathUsenet} {
		_, body := getFrom(t, srv, path+"?t=movie&tmdbid="+InceptionTmdbID+"&apikey="+APIKey)
		rels, err := torznab.ParseResults(bytes.NewReader(body))
		require.NoError(t, err, path)
		assert.Empty(t, rels, path)
	}
}

// The download releases parse as the Inception releases the scenarios
// expect: Bluray-1080p, the seeder's a REPACK (a revision upgrade over a
// planted Bluray-1080p v1 file, import_test.go) and the gone one v1 (never
// an upgrade over it), every size clear of the quality's floor at the
// film's 148 minutes and at pkg/decision's 110-minute fallback -- the
// smoke test's first feed advertised the clip's real 57 MiB and was refused
// BelowMinimumSize.
func TestTheDownloadReleasesParseAsInceptionAt1080p(t *testing.T) {
	p := quality.Profile{Sizes: quality.MovieSizeTable()}
	for _, tc := range []struct {
		title   string
		version int32
	}{{TitleSeeder, 2}, {TitleGone, 1}, {TitleNZB, 1}} {
		parsed, err := release.Parse(tc.title, release.Options{Kind: commonv1.MediaKindMovie})
		require.NoError(t, err, tc.title)
		assert.Equal(t, "Inception", parsed.Title, tc.title)
		assert.Equal(t, 2010, parsed.Year, tc.title)
		assert.Equal(t, "Bluray-1080p", parsed.Quality.Name, tc.title)
		assert.Equal(t, tc.version, max(parsed.Revision.Version, 1), tc.title)
		for _, minutes := range []int{148, 110} {
			minBytes, maxBytes := quality.SizeLimits(p, parsed.Quality, minutes)
			assert.GreaterOrEqual(t, releaseBytes, minBytes, "%s at %d minutes", tc.title, minutes)
			if maxBytes > 0 {
				assert.LessOrEqual(t, releaseBytes, maxBytes, "%s at %d minutes", tc.title, minutes)
			}
		}
	}
}
