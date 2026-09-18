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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func TestEngineDownloadPrefersMagnetByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(readTestdataBytes(t, "1337x-details.html"))
	}))
	defer srv.Close()

	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{}) // primarydownloadlink defaults to "magnet:"
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	rc, err := eng.Download(context.Background(), def, cfg, "/torrent/1000001/Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP/")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Contains(t, string(body), "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01")
}

func TestEngineDownloadFallbackFetchesTheTorrentURL(t *testing.T) {
	var fetchedTorrent bool
	mux := http.NewServeMux()
	mux.HandleFunc("/torrent/1000001/Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(readTestdataBytes(t, "1337x-details.html"))
	})
	mux.HandleFunc("/torrent/ABCDEF0123456789ABCDEF0123456789ABCDEF01.torrent", func(w http.ResponseWriter, r *http.Request) {
		fetchedTorrent = true
		_, _ = w.Write([]byte("FAKETORRENTBYTES"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)

	// The definition's own "replace" filter rewrites the extracted
	// itorrents.org href to https://itorrents.net/ — a real external host
	// this task's no-network-in-tests rule forbids resolving. This test
	// technique (start from the real corpus definition, then override one
	// filter argument so the fixture stays local) is the brief's own
	// documented fix: rewrite it to the local test server's /torrent/
	// prefix instead, so the same "primary selector matches an itorrents
	// link, then Download actually fetches it" flow runs end to end
	// against httptest.
	require.NotEmpty(t, def.Download.Selectors)
	def.Download.Selectors[0].Filters[0].Args = cardigann.ScalarList{"http://itorrents.org/torrent/", srv.URL + "/torrent/"}

	// primarydownloadlink is a `select` setting whose options are
	// "//itorrents." and "magnet:"; overriding it to the itorrents option
	// makes the *first* download selector target the .torrent link
	// instead of the magnet link exercised by the test above.
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{"primarydownloadlink": "//itorrents."})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	rc, err := eng.Download(context.Background(), def, cfg, "/torrent/1000001/Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP/")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "FAKETORRENTBYTES", string(body))
	assert.True(t, fetchedTorrent)
}

// TestEngineDownloadBuildsMagnetFromInfoHash covers the InfoHashBlock path
// (Done-when checklist: "builds a magnet URI from InfoHashBlock when no
// selector matches"), which has no fixture in either bundled definition —
// a small synthetic Definition, the same technique the login fixtures use.
func TestEngineDownloadBuildsMagnetFromInfoHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<div id="hash">ABCDEF0123456789ABCDEF0123456789ABCDEF01</div><div id="name">Some.Release.Name</div>`))
	}))
	defer srv.Close()

	def := &cardigann.Definition{
		Links: []string{srv.URL + "/"},
		Download: &cardigann.DownloadBlock{
			// No Selectors at all -> falls straight through to InfoHash.
			InfoHash: &cardigann.InfoHashBlock{
				Hash:  cardigann.SelectorField{Selector: "#hash"},
				Title: cardigann.SelectorField{Selector: "#name"},
			},
		},
	}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client()}
	rc, err := eng.Download(context.Background(), def, cfg, "/details/1")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=Some.Release.Name", string(body))
}

// TestEngineDownloadMagnetShortCircuitsBeforeSessionCheck asserts that a
// magnet link is returned without ever touching the network or the
// ErrSessionRequired gate, even for a definition whose login method
// would otherwise require an established Session first. No httptest
// server is set up at all — if Download tried to make a network call
// here, there would be nothing listening and the test would fail with a
// dial error instead of succeeding.
func TestEngineDownloadMagnetShortCircuitsBeforeSessionCheck(t *testing.T) {
	def := &cardigann.Definition{
		Links: []string{"https://example.invalid/"},
		Login: &cardigann.LoginBlock{Method: "cookie", Cookies: []string{"session_id"}},
	}
	cfg, err := cardigann.NewConfig(def, "https://example.invalid/", nil)
	require.NoError(t, err)
	require.Nil(t, cfg.Session) // never logged in

	eng := cardigann.Engine{}
	rc, err := eng.Download(context.Background(), def, cfg, "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", string(body))
}
