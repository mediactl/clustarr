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

package opensubtitlesstub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestModeOKServesACandidateTheRealClientAcceptsAndDownloads is this stub's
// own guard rail, the same discipline torznabstub's
// TestEmbeddedDocumentsParse follows: every shape this fixture serves is
// exercised here through the SAME client package captionarr's fetch worker
// uses, so a typo in a JSON field name fails `go test` instead of showing up
// fifteen minutes into a kind run as "opensubtitlescom: search: decode:
// ...".
func TestModeOKServesACandidateTheRealClientAcceptsAndDownloads(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: APIKey, Username: "u", Password: "p", Endpoint: srv.URL, UserAgent: "clustarr-test",
	})

	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"},
		Languages: []subtitles.LangKey{"en"},
	})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	c := cands[0]
	assert.Equal(t, "opensubtitlescom", c.Provider)
	assert.Equal(t, "en", c.Language)
	assert.False(t, c.HI)
	assert.False(t, c.Forced)
	assert.Equal(t, FixtureReleaseInfo, c.ReleaseInfo)
	assert.True(t, c.Matches[subtitles.MatchHash], "moviehash_match:true must set MatchHash")
	assert.True(t, c.Trusted)

	raw, name, err := p.Download(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, FixtureFileName, name)

	// PostProcess is what the fetch worker actually runs over a download
	// before writing the sidecar -- proving THIS decodes and parses is what
	// makes the fixture srt body a real guard rail, not merely valid JSON.
	out, err := subtitles.PostProcess(raw, "en", nil, true)
	require.NoError(t, err)
	assert.Contains(t, string(out), "Hello from the OpenSubtitles.com fixture provider.")
}

func TestModeThrottledAnswers429AndModeQuotaExhaustedAnswers406(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	setMode := func(t *testing.T, mode Mode) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/__mode__?mode="+string(mode), "", nil) //nolint:noctx // httptest
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}

	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: APIKey, Username: "u", Password: "p", Endpoint: srv.URL})

	t.Run("429", func(t *testing.T) {
		setMode(t, ModeThrottled)
		t.Cleanup(func() { setMode(t, ModeOK) })

		_, err := p.Search(context.Background(), subtitles.Query{Kind: commonv1.MediaKindMovie, Languages: []subtitles.LangKey{"en"}})
		require.Error(t, err)
		assert.True(t, subtitles.IsRateLimited(err), "a 429 must classify as KindTooManyRequests: %v", err)
	})

	t.Run("quota", func(t *testing.T) {
		setMode(t, ModeQuotaExhausted)
		t.Cleanup(func() { setMode(t, ModeOK) })

		_, err := p.Search(context.Background(), subtitles.Query{Kind: commonv1.MediaKindMovie, Languages: []subtitles.LangKey{"en"}})
		require.Error(t, err)
		assert.True(t, subtitles.IsQuotaExceeded(err), "a 406 must classify as KindDownloadLimitExceeded: %v", err)
	})

	t.Run("mode rejects an unknown value", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/__mode__?mode=bogus", "", nil) //nolint:noctx // httptest
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestLoginRejectsTheWrongAPIKey(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: "wrong-key", Username: "u", Password: "p", Endpoint: srv.URL})
	err := p.EnsureLoggedIn(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, subtitles.ErrAuth), "a bad Api-Key must classify as KindAuth: %v", err)
}

func TestModeGetReadinessRouteAlwaysAnswers200RegardlessOfMode(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(NewHandler(filepath.Join(dir, "requests.jsonl"), discardLogger()))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/__mode__?mode="+string(ModeThrottled), "", nil) //nolint:noctx // httptest
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	// This is the Deployment's readinessProbe path: it must stay healthy
	// even while every domain route is deliberately failing, or a scenario
	// that sets ModeThrottled would flap the pod's own Ready condition.
	resp, err = http.Get(srv.URL + "/__mode__") //nolint:noctx // httptest
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, string(ModeThrottled), body["mode"])
}

func TestRequestLogSkipsProbesAndRecordsRealRequests(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.jsonl")
	srv := httptest.NewServer(NewHandler(logPath, discardLogger()))
	t.Cleanup(srv.Close)

	probe, err := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader(`{}`)) //nolint:noctx // httptest
	require.NoError(t, err)
	probe.Header.Set("User-Agent", "kube-probe/1.33")
	probe.Header.Set("Api-Key", APIKey)
	resp, err := http.DefaultClient.Do(probe)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NoFileExists(t, logPath, "a kubelet probe writes no line")

	real, err := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader(`{"username":"u","password":"p"}`)) //nolint:noctx // httptest
	require.NoError(t, err)
	real.Header.Set("User-Agent", "clustarr/captionarr-worker")
	real.Header.Set("Api-Key", APIKey)
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
	assert.Equal(t, "/login", e.Path)
	assert.Equal(t, http.StatusOK, e.Status)
	assert.False(t, e.At.IsZero())
}
