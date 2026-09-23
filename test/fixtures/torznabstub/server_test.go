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
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// discardLogger keeps the stub's warnings out of `go test` output while
// still exercising every logging call site.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestEmbeddedDocumentsParse is the fixture's own guard rail: every
// document this stub serves is parsed here by the SAME pkg/torznab
// functions indexarr will use, so a typo in the XML fails in `make test`
// instead of twenty minutes into `hack/e2e.sh` as an Indexer that never
// goes Ready.
func TestEmbeddedDocumentsParse(t *testing.T) {
	caps, err := torznab.ParseCaps(bytes.NewReader(capsXML))
	require.NoError(t, err)
	require.True(t, caps.Supports(torznab.ModeMovieSearch, "tmdbid"))
	require.True(t, caps.Supports(torznab.ModeTVSearch, "tvdbid"))
	require.False(t, caps.Modes[torznab.ModeMusicSearch].Available)
	require.Equal(t, 100, caps.LimitsMax)
	require.Equal(t, 50, caps.LimitsDefault)
	require.Equal(t, "raw", caps.Modes[torznab.ModeSearch].SearchEngine)

	movies, err := torznab.ParseResults(bytes.NewReader(movie900100XML))
	require.NoError(t, err)
	require.Len(t, movies, 3)
	require.Equal(t, "Fixture.Search.Film.2019.480p.DVDRip.XviD-CLUSTARR", movies[0].Title,
		"the feed is deliberately worst-first; RankAndCap is what must reorder it")
	for _, rel := range movies {
		require.Equal(t, "900100", rel.IDs[commonv1.IDKeyTMDB])
		require.NotZero(t, rel.Size)
		require.NotNil(t, rel.Seeders)
		require.False(t, rel.PubDate.IsZero(), "a malformed pubDate fails the WHOLE feed")
	}

	rss, err := torznab.ParseResults(bytes.NewReader(rssXML))
	require.NoError(t, err)
	require.Len(t, rss, 1)
	require.Equal(t, "900101", rss[0].IDs[commonv1.IDKeyTMDB])

	empty, err := torznab.ParseResults(bytes.NewReader(emptyXML))
	require.NoError(t, err)
	require.Empty(t, empty)

	e, err := torznab.ParseError(bytes.NewReader(error100XML))
	require.NoError(t, err)
	require.NotNil(t, e)
	require.EqualValues(t, 100, e.Code)
}

// TestEmbeddedTitlesParseToTheExpectedQualities pins the qualities the e2e
// scenario's two-tier QualityProfile is written against. The profile names
// "Bluray-1080p", "WEBRip-720p" and "WEBDL-1080p" as literal strings, and a
// change in pkg/release's parse of any of these titles would turn a ranking
// assertion on a real cluster into an unexplained "nothing was approved".
func TestEmbeddedTitlesParseToTheExpectedQualities(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"Fixture.Search.Film.2019.1080p.BluRay.x264-CLUSTARR", "Bluray-1080p"},
		{"Fixture.Search.Film.2019.720p.WEBRip.x264-CLUSTARR", "WEBRip-720p"},
		{"Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR", "WEBDL-1080p"},
	} {
		parsed, err := release.Parse(tc.title, release.Options{Kind: commonv1.MediaKindMovie})
		require.NoError(t, err, tc.title)
		require.Equal(t, tc.want, parsed.Quality.Name, tc.title)
	}

	// The 480p release must parse, and must NOT be one of the qualities the
	// scenario's profile accepts: the whole point of keeping it in the feed
	// is that the decision engine reports it rejected-with-a-reason rather
	// than dropping it.
	parsed, err := release.Parse("Fixture.Search.Film.2019.480p.DVDRip.XviD-CLUSTARR",
		release.Options{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	require.NotEmpty(t, parsed.Quality.Name)
	require.NotContains(t, []string{"Bluray-1080p", "WEBRip-720p", "WEBDL-720p"}, parsed.Quality.Name)
}

// TestEmbeddedSizesClearTheProfileMinimums is the other half of the same
// guard rail. pkg/decision rejects a release below its quality's
// MinMBPerMin, computed against the item's runtime; the fixture TMDB movies
// carry runtime 148, and the sizes in the feeds were chosen to clear the
// floor at that runtime AND at pkg/decision's 110-minute fallback, so a
// metadata refresh that had not landed yet cannot change the verdict.
func TestEmbeddedSizesClearTheProfileMinimums(t *testing.T) {
	p := quality.Profile{Sizes: quality.MovieSizeTable()}
	for _, tc := range []struct {
		name    string
		title   string
		size    int64
		minutes int
	}{
		{"1080p bluray at fixture runtime", "Fixture.Search.Film.2019.1080p.BluRay.x264-CLUSTARR", 8589934592, 148},
		{"1080p bluray at the fallback runtime", "Fixture.Search.Film.2019.1080p.BluRay.x264-CLUSTARR", 8589934592, 110},
		{"720p webrip at fixture runtime", "Fixture.Search.Film.2019.720p.WEBRip.x264-CLUSTARR", 4294967296, 148},
		{"1080p webdl at fixture runtime", "Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR", 6442450944, 148},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := release.Parse(tc.title, release.Options{Kind: commonv1.MediaKindMovie})
			require.NoError(t, err)
			minBytes, maxBytes := quality.SizeLimits(p, parsed.Quality, tc.minutes)
			require.GreaterOrEqual(t, tc.size, minBytes,
				"%s is under the %s floor at %d minutes", tc.title, parsed.Quality.Name, tc.minutes)
			if maxBytes > 0 {
				require.LessOrEqual(t, tc.size, maxBytes)
			}
		})
	}
}

func TestHandlerRoutes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	srv := httptest.NewServer(NewHandler(logPath, discardLogger()))
	t.Cleanup(srv.Close)

	get := func(t *testing.T, path, query string) (*http.Response, []byte) {
		t.Helper()
		resp, err := http.Get(srv.URL + path + "?" + query) //nolint:noctx // httptest, no cancellation to model
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp, body
	}

	t.Run("no apikey is Newznab error 100 under HTTP 200", func(t *testing.T) {
		resp, body := get(t, PathHealthy, "t=caps")
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"a credential failure is an <error> element under HTTP 200, not a 401")
		e, err := torznab.ParseError(bytes.NewReader(body))
		require.NoError(t, err)
		require.NotNil(t, e)
		require.EqualValues(t, torznab.ErrIncorrectCredentials, e.Code)
	})

	t.Run("caps", func(t *testing.T) {
		resp, body := get(t, PathHealthy, "t=caps&apikey="+APIKey)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		caps, err := torznab.ParseCaps(bytes.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, 100, caps.LimitsMax)
	})

	t.Run("movie by tmdbid", func(t *testing.T) {
		_, body := get(t, PathHealthy, "t=movie&tmdbid=900100&apikey="+APIKey)
		rels, err := torznab.ParseResults(bytes.NewReader(body))
		require.NoError(t, err)
		require.Len(t, rels, 3)
	})

	t.Run("movie by unknown id is an empty feed", func(t *testing.T) {
		_, body := get(t, PathHealthy, "t=movie&tmdbid=999&apikey="+APIKey)
		rels, err := torznab.ParseResults(bytes.NewReader(body))
		require.NoError(t, err)
		require.Empty(t, rels)
	})

	t.Run("search is the RSS feed", func(t *testing.T) {
		_, body := get(t, PathHealthy, "t=search&apikey="+APIKey)
		rels, err := torznab.ParseResults(bytes.NewReader(body))
		require.NoError(t, err)
		require.Len(t, rels, 1)
		require.Equal(t, "Fixture.Firehose.Film.2019.1080p.WEB-DL.x264-CLUSTARR", rels[0].Title)
	})

	t.Run("searchdown answers caps and fails searches", func(t *testing.T) {
		resp, body := get(t, PathSearchDown, "t=caps&apikey="+APIKey)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		caps, err := torznab.ParseCaps(bytes.NewReader(body))
		require.NoError(t, err)
		require.True(t, caps.Supports(torznab.ModeMovieSearch, "tmdbid"))

		resp, _ = get(t, PathSearchDown, "t=search&apikey="+APIKey)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		resp, _ = get(t, PathSearchDown, "t=movie&tmdbid=900100&apikey="+APIKey)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("down fails everything", func(t *testing.T) {
		resp, _ := get(t, PathDown, "t=caps&apikey="+APIKey)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("unrecognised route", func(t *testing.T) {
		resp, _ := get(t, "/nope", "")
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestRequestLogSkipsProbesAndRecordsRealRequests(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	srv := httptest.NewServer(NewHandler(logPath, discardLogger()))
	t.Cleanup(srv.Close)

	probe, err := http.NewRequest(http.MethodGet, srv.URL+PathHealthy+"?t=caps&apikey="+APIKey, nil) //nolint:noctx // httptest
	require.NoError(t, err)
	probe.Header.Set("User-Agent", "kube-probe/1.33")
	resp, err := http.DefaultClient.Do(probe)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.NoFileExists(t, logPath,
		"a kubelet probe writes no line: the probe runs every few seconds for the life of the cluster")

	real, err := http.NewRequest(http.MethodGet, srv.URL+PathHealthy+"?t=movie&tmdbid=900100&apikey="+APIKey, nil) //nolint:noctx // httptest
	require.NoError(t, err)
	real.Header.Set("User-Agent", "clustarr/indexarr")
	resp, err = http.DefaultClient.Do(real)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	entries := readLog(t, logPath)
	require.Len(t, entries, 1)
	require.Equal(t, PathHealthy, entries[0].Path)
	require.Equal(t, "movie", entries[0].T)
	require.Equal(t, http.StatusOK, entries[0].Status)
	require.Contains(t, entries[0].Query, "tmdbid=900100")
	require.False(t, entries[0].At.IsZero())
}

func readLog(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var out []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		out = append(out, e)
	}
	require.NoError(t, sc.Err())
	return out
}
