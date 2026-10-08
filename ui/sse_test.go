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

package ui_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// openStream GETs path on srv as an event stream, for up to 10 s, and
// returns its reader.
func openStream(t *testing.T, srv *httptest.Server, path string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))
	return bufio.NewReader(resp.Body)
}

// readArtEvent reads events until an "art" one and decodes its data.
func readArtEvent(t *testing.T, r *bufio.Reader) (key, digest string) {
	t.Helper()
	for {
		ev := readSSEEvent(t, r)
		if !strings.HasPrefix(strings.TrimSpace(ev), "event: art") {
			continue
		}
		var data struct{ Key, V string }
		for _, line := range strings.Split(ev, "\n") {
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				require.NoError(t, json.Unmarshal([]byte(rest), &data), "an art event's data is one JSON line")
			}
		}
		return data.Key, data.V
	}
}

// TestLibraryStreamCarriesArtEventsForItsPage: a library tab's stream
// carries the SSE art event for the items on the page it last sent, and
// only those (W4.109): a changed cover of movie u1, on the page, arrives as
// `event: art`; a changed cover of u2, which is not, does not. u2 changes
// first, so had its event been relayed it would be read first.
func TestLibraryStreamCarriesArtEventsForItsPage(t *testing.T) {
	store := newArtStore(t)
	onPage := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Arrival", Monitored: true, ArtKey: "movie/u1/poster",
	}
	frames := make(chan []projection.LibraryItem, 1)
	frames <- []projection.LibraryItem{onPage}
	s := newSyncedArtServer(t, ui.Options{
		Artwork:          store,
		SubscribeLibrary: func() (<-chan []projection.LibraryItem, func()) { return frames, func() {} },
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	r := openStream(t, srv, "/events/library/movies")
	require.Contains(t, readSSEEvent(t, r), `data-ref="default/arrival"`, "the page's first frame")

	putArt(t, store, posterKey(commonv1.MediaKindMovie, "u2", events.ArtworkVariantOriginal), "image/png", "not on the page")
	u1 := putArt(t, store, posterKey(commonv1.MediaKindMovie, "u1", events.ArtworkVariantOriginal), "image/png", "on the page")
	key, v := readArtEvent(t, r)
	require.Equal(t, "movie/u1/poster", key, "only an item on the page gets an art event")
	require.Equal(t, u1.Digest, v)
}

// TestArtEventsStreamOnlyTheAskedKeys: GET /events/art streams the art
// events of the keys it names and no others (the item page's stream,
// W4.109); a malformed key, no key and more than 16 keys are 400.
func TestArtEventsStreamOnlyTheAskedKeys(t *testing.T) {
	store := newArtStore(t)
	s := newSyncedArtServer(t, ui.Options{Artwork: store})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for name, query := range map[string]string{
		"no key":        "",
		"a bad kind":    "?key=film/u1/poster",
		"no uid":        "?key=movie//poster",
		"a bad type":    "?key=movie/u1/backdrop",
		"a variant too": "?key=movie/u1/poster/original",
		"17 keys":       "?key=" + strings.Repeat("movie/u1/poster&key=", 16) + "movie/u1/poster",
	} {
		resp, err := srv.Client().Get(srv.URL + "/events/art" + query)
		require.NoError(t, err, name)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, name)
	}

	r := openStream(t, srv, "/events/art?key="+url.QueryEscape("movie/u1/poster")+"&key=series/u9/fanart")
	putArt(t, store, posterKey(commonv1.MediaKindMovie, "u2", events.ArtworkVariantOriginal), "image/png", "not asked")
	u1 := putArt(t, store, posterKey(commonv1.MediaKindMovie, "u1", events.ArtworkVariantOriginal), "image/png", "asked")
	key, v := readArtEvent(t, r)
	require.Equal(t, "movie/u1/poster", key, "a key the stream did not ask for is not relayed")
	require.Equal(t, u1.Digest, v)
}
