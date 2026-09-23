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

package gestdownstub

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestSearchAndDownloadRoundTripThroughTheRealClient is this stub's own
// guard rail, mirroring torznabstub's TestEmbeddedDocumentsParse: every
// shape this fixture serves is exercised through the SAME client package
// captionarr's fetch worker uses.
func TestSearchAndDownloadRoundTripThroughTheRealClient(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})

	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"tvdb": "121361"},
		Season: 1, Episode: 1, Languages: []subtitles.LangKey{"en"},
	})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	c := cands[0]
	assert.Equal(t, "gestdown", c.Provider)
	assert.Equal(t, FixtureSubtitleID, c.ID)
	assert.Equal(t, "/subtitles/download/"+FixtureSubtitleID, c.FetchID)
	assert.Equal(t, FixtureVersion, c.ReleaseInfo)
	assert.False(t, c.HI)

	raw, name, err := p.Download(context.Background(), c)
	require.NoError(t, err)
	assert.Contains(t, name, ".srt")

	out, err := subtitles.PostProcess(raw, "en", nil, true)
	require.NoError(t, err)
	assert.Contains(t, string(out), "Hello from the Gestdown fixture provider.")
}

// TestSearchIsPermissiveOfAnyTVDBID pins that this stub, unlike
// pkg/subtitles/providers/gestdown's own unit tests, never answers 404 --
// its whole job in scenario 13 is to be the provider that SUCCEEDS once
// OpenSubtitles.com is throttled, and a fixture that could itself fail
// would just add a second thing that could go wrong in that proof.
func TestSearchIsPermissiveOfAnyTVDBID(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"tvdb": "999999999"},
		Season: 3, Episode: 7, Languages: []subtitles.LangKey{"fr"},
	})
	require.NoError(t, err)
	require.Len(t, cands, 1)
}

func TestRequestLogSkipsProbesAndRecordsRealRequests(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	srv := httptest.NewServer(NewHandler(logPath, discardLogger()))
	t.Cleanup(srv.Close)

	probe, err := http.NewRequest(http.MethodGet, srv.URL+"/shows/external/tvdb/121361", nil) //nolint:noctx // httptest
	require.NoError(t, err)
	probe.Header.Set("User-Agent", "kube-probe/1.33")
	resp, err := http.DefaultClient.Do(probe)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NoFileExists(t, logPath, "a kubelet probe writes no line")

	real, err := http.NewRequest(http.MethodGet, srv.URL+"/shows/external/tvdb/121361", nil) //nolint:noctx // httptest
	require.NoError(t, err)
	real.Header.Set("User-Agent", "clustarr/captionarr-worker")
	resp, err = http.DefaultClient.Do(real)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	f, err := os.Open(logPath)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	require.True(t, sc.Scan())
	var e Entry
	require.NoError(t, json.Unmarshal(sc.Bytes(), &e))
	assert.Equal(t, "/shows/external/tvdb/121361", e.Path)
	assert.Equal(t, http.StatusOK, e.Status)
	assert.False(t, e.At.IsZero())
}

func TestUnrecognisedRouteIs404(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/nope") //nolint:noctx // httptest
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
