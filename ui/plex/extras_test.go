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

package plex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

// plexTV is Plex's metadata service answering an item's extras with a
// response recorded from it (Arrival, 2026-10-06, two of its 44 extras).
type plexTV struct {
	srv    *httptest.Server
	calls  atomic.Int32
	path   atomic.Value
	token  atomic.Value
	status int
}

func newPlexTV(t *testing.T, status int) *plexTV {
	t.Helper()
	body, err := os.ReadFile("testdata/plextv_extras_arrival.json")
	require.NoError(t, err)
	p := &plexTV{status: status}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		p.path.Store(r.URL.Path)
		p.token.Store(r.Header.Get("X-Plex-Token"))
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "<MediaContainer/>", http.StatusOK)
			return
		}
		// Plex's metadata service refuses a page over 100 (101 is a 400,
		// 2026-10-06).
		if n, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size")); err != nil || n > 100 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if p.status != http.StatusOK {
			w.WriteHeader(p.status)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func extrasHandler(t *testing.T, extras func(context.Context, string) ([]plex.Extra, error), objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://clustarr.example",
		Extras:      extras,
		Index:       func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
	})
}

func movieWithPlexID(id string) *catalogv1.Movie {
	m := fixtureMovie()
	m.Status.Metadata.ExternalIDs = map[string]string{"plex": id}
	return m
}

type extrasResponse struct {
	MediaContainer struct {
		Identifier string `json:"identifier"`
		Size       int    `json:"size"`
		TotalSize  int    `json:"totalSize"`
		Offset     int    `json:"offset"`
		Metadata   []struct {
			Title     string `json:"title"`
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			ExtraType int    `json:"extraType"`
			Media     []struct {
				URL string `json:"url"`
			} `json:"Media"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

// PMS asks a custom provider for an item's extras on every refresh, a
// route Plex's provider docs never mention. It read clustarr's 404 as "no
// extras" and deleted the Internet Video Archive trailers it held for the
// item (kind-cluster-plex, 2026-10-06). The route answers Plex's own extras
// for the item's Plex id, IVA URLs and all.
func TestExtrasArePlexsOwnForTheItemsPlexID(t *testing.T) {
	tv := newPlexTV(t, http.StatusOK)
	src := &plex.PlexTVExtras{BaseURL: tv.srv.URL, Token: "server-token"}
	h := extrasHandler(t, src.Extras, movieWithPlexID(moviePlex))

	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out extrasResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	mc := out.MediaContainer
	assert.Equal(t, plex.MoviesIdentifier, mc.Identifier)
	assert.Equal(t, 2, mc.Size)
	assert.Equal(t, 2, mc.TotalSize)
	require.Len(t, mc.Metadata, 2)
	assert.Equal(t, "clip", mc.Metadata[0].Type)
	assert.Equal(t, "trailer", mc.Metadata[0].Subtype)
	assert.Equal(t, 1, mc.Metadata[0].ExtraType)
	require.NotEmpty(t, mc.Metadata[0].Media)
	assert.Contains(t, mc.Metadata[0].Media[0].URL, "video.internetvideoarchive.net")

	assert.Equal(t, "/library/metadata/"+moviePlex+"/extras", tv.path.Load())
	assert.Equal(t, "server-token", tv.token.Load())
}

// PMS reaches an item it holds under its plex:// GUID by the Plex id.
func TestExtrasResolveAPlexIDRatingKey(t *testing.T) {
	tv := newPlexTV(t, http.StatusOK)
	src := &plex.PlexTVExtras{BaseURL: tv.srv.URL, Token: "server-token"}
	h := extrasHandler(t, src.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+moviePlex+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestExtrasPageAsPMSAsks(t *testing.T) {
	tv := newPlexTV(t, http.StatusOK)
	src := &plex.PlexTVExtras{BaseURL: tv.srv.URL, Token: "server-token"}
	h := extrasHandler(t, src.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras?X-Plex-Container-Start=1&X-Plex-Container-Size=50")
	var out extrasResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	assert.Equal(t, 1, out.MediaContainer.Size)
	assert.Equal(t, 2, out.MediaContainer.TotalSize)
	assert.Equal(t, 1, out.MediaContainer.Offset)
	require.Len(t, out.MediaContainer.Metadata, 1)
}

// A refresh of the whole library asks once per item; Plex is asked once.
func TestExtrasAreCached(t *testing.T) {
	tv := newPlexTV(t, http.StatusOK)
	src := &plex.PlexTVExtras{BaseURL: tv.srv.URL, Token: "server-token"}
	h := extrasHandler(t, src.Extras, movieWithPlexID(moviePlex))
	for range 3 {
		rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
		require.Equal(t, http.StatusOK, rec.Code)
	}
	assert.Equal(t, int32(1), tv.calls.Load())
}

// An item Plex does not know has no extras of Plex's to keep.
func TestExtrasOfAnItemWithoutAPlexIDAreNone(t *testing.T) {
	tv := newPlexTV(t, http.StatusOK)
	src := &plex.PlexTVExtras{BaseURL: tv.srv.URL, Token: "server-token"}
	h := extrasHandler(t, src.Extras, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"`+plex.MoviesIdentifier+`","size":0,"Metadata":[]}}`, rec.Body.String())
	assert.Zero(t, tv.calls.Load())
}

// When Plex cannot be asked the answer is an error, never 404 or an empty
// list: both read to PMS as "no extras", and it deletes the ones it has.
func TestExtrasAreAnErrorWhenPlexCannotBeAsked(t *testing.T) {
	tv := newPlexTV(t, http.StatusInternalServerError)
	src := &plex.PlexTVExtras{BaseURL: tv.srv.URL, Token: "server-token"}
	h := extrasHandler(t, src.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), "server-token")
}

func TestExtrasWithoutASourceAreUnavailable(t *testing.T) {
	h := extrasHandler(t, nil, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestExtrasOfAnUnknownItemAreNotFound(t *testing.T) {
	h := extrasHandler(t, nil)
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// An item with more extras than one page holds -- Plex serves at most 100
// per request -- answers all of them.
func TestExtrasPageThroughPlexsLimit(t *testing.T) {
	const total = 150
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		size, err := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		if err != nil || size > 100 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var items []map[string]any
		for i := start; i < min(start+size, total); i++ {
			items = append(items, map[string]any{"title": fmt.Sprintf("Extra %d", i), "type": "clip", "extraType": 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{
			"offset": start, "size": len(items), "totalSize": total, "Metadata": items,
		}})
	}))
	t.Cleanup(srv.Close)

	src := &plex.PlexTVExtras{BaseURL: srv.URL, Token: "server-token"}
	items, err := src.Extras(context.Background(), moviePlex)
	require.NoError(t, err)
	require.Len(t, items, total)
	assert.Contains(t, string(items[total-1]), "Extra 149")
	assert.Equal(t, int32(2), calls.Load())
}
