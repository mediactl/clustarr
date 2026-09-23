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

// fakeTorrent is the smallest body that passes testlinktorrent's check: a
// bencoded dictionary starts with 'd'.
const fakeTorrent = "d4:infod4:name4:fakeee"

func TestEngineDownloadFallbackFetchesTheTorrentURL(t *testing.T) {
	var fetchedTorrent bool
	mux := http.NewServeMux()
	mux.HandleFunc("/torrent/1000001/Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(readTestdataBytes(t, "1337x-details.html"))
	})
	mux.HandleFunc("/torrent/ABCDEF0123456789ABCDEF0123456789ABCDEF01.torrent", func(w http.ResponseWriter, r *http.Request) {
		fetchedTorrent = true
		// A bencoded dictionary: testlinktorrent (default true) checks
		// the fetched file is a torrent before returning it.
		_, _ = w.Write([]byte(fakeTorrent))
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
	assert.Equal(t, fakeTorrent, string(body))
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

// TestEngineDownloadInfoHashComesBeforeSelectors pins Prowlarr's order
// (CardigannRequestGenerator.DownloadRequest: `if (download.Infohash !=
// null) ... else if (download.Selectors ...)`): a definition with both
// builds the magnet from the infohash and never follows a selector's link.
func TestEngineDownloadInfoHashComesBeforeSelectors(t *testing.T) {
	var fetchedLink bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dl/1.torrent" {
			fetchedLink = true
			_, _ = w.Write([]byte("d8:announce0:e"))
			return
		}
		_, _ = w.Write([]byte(`<a class="dl" href="/dl/1.torrent">get</a>` +
			`<div id="hash">ABCDEF0123456789ABCDEF0123456789ABCDEF01</div><div id="name">Some.Release</div>`))
	}))
	defer srv.Close()

	def := &cardigann.Definition{
		Links: []string{srv.URL + "/"},
		Download: &cardigann.DownloadBlock{
			Selectors: []cardigann.SelectorField{{Selector: "a.dl", Attribute: "href"}},
			InfoHash: &cardigann.InfoHashBlock{
				Hash:  cardigann.SelectorField{Selector: "#hash"},
				Title: cardigann.SelectorField{Selector: "#name"},
			},
		},
	}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)
	rc, err := cardigann.Engine{HTTP: srv.Client()}.Download(context.Background(), def, cfg, "/details/1")
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=Some.Release", string(body))
	assert.False(t, fetchedLink, "the selector is not consulted when the definition has an infohash")
}

// TestEngineDownloadBeforeIsNotFollowedAndFeedsTheInfoHash covers
// download.before as Prowlarr's HandleRequest sends it -- no redirect
// following, Referer set to the link, the path's own query kept beside the
// inputs -- and the infohash block's own usebeforeresponse (the level the
// bundled kinozal-magnet and magnetdownload set), which reads the before
// response rather than the details page.
func TestEngineDownloadBeforeIsNotFollowedAndFeedsTheInfoHash(t *testing.T) {
	var beforeQuery, beforeReferer string
	var followed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_srv_details.php":
			beforeQuery, beforeReferer = r.URL.RawQuery, r.Header.Get("Referer")
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte(`<ul><li>0123456789ABCDEF0123456789ABCDEF01234567</li></ul>`))
		case "/elsewhere":
			followed = true
			_, _ = w.Write([]byte(`<ul><li>FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF</li></ul>`))
		default:
			_, _ = w.Write([]byte(`<ul><li>EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE</li></ul><h1>Title.On.Page</h1>`))
		}
	}))
	defer srv.Close()

	def := &cardigann.Definition{
		Links: []string{srv.URL + "/"},
		Download: &cardigann.DownloadBlock{
			Before: &cardigann.BeforeBlock{
				Path:   "get_srv_details.php?action=2",
				Inputs: map[string]cardigann.Scalar{"id": "{{ .DownloadUri.Query.id }}"},
			},
			InfoHash: &cardigann.InfoHashBlock{
				UseBeforeResponse: true,
				Hash:              cardigann.SelectorField{Selector: "li:first-child"},
				Title:             cardigann.SelectorField{Selector: "h1"},
			},
		},
	}
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", nil)
	require.NoError(t, err)
	rc, err := cardigann.Engine{HTTP: srv.Client()}.Download(context.Background(), def, cfg, "/details.php?id=42")
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)

	assert.False(t, followed, "download.before does not follow a redirect")
	assert.Equal(t, "action=2&id=42", beforeQuery, "the path's own query is kept beside the inputs")
	assert.Equal(t, srv.URL+"/details.php?id=42", beforeReferer)
	// The block-level flag sends both halves to the before response, which
	// has no h1: a magnet with no dn, from the before response's hash.
	assert.Equal(t, "magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567", string(body))
}
