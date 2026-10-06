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
	"errors"
	"net/http"
	"os"
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

// gateway is the metadata gateway answering rpc.catalogarr.metadata.extras
// with a response recorded from Plex's metadata service (Arrival,
// 2026-10-06, two of its 44 extras), recording the Plex id it was asked for.
type gateway struct {
	calls atomic.Int32
	asked atomic.Value
	err   error
	items []plex.Extra
}

func newGateway(t *testing.T, err error) *gateway {
	t.Helper()
	body, readErr := os.ReadFile("../../test/data/metadata/plex/extras_" + moviePlex + ".json")
	require.NoError(t, readErr)
	var recorded struct {
		MediaContainer struct {
			Metadata []plex.Extra `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, json.Unmarshal(body, &recorded))
	return &gateway{err: err, items: recorded.MediaContainer.Metadata}
}

func (g *gateway) Extras(_ context.Context, plexID string) ([]plex.Extra, error) {
	g.calls.Add(1)
	g.asked.Store(plexID)
	if g.err != nil {
		return nil, g.err
	}
	return g.items, nil
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
// for the item's Plex id, IVA URLs and all, as the metadata gateway holds
// them.
func TestExtrasArePlexsOwnForTheItemsPlexID(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))

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
	assert.Equal(t, moviePlex, gw.asked.Load())
}

// PMS reaches an item it holds under its plex:// GUID by the Plex id.
func TestExtrasResolveAPlexIDRatingKey(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+moviePlex+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestExtrasPageAsPMSAsks(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras?X-Plex-Container-Start=1&X-Plex-Container-Size=50")
	var out extrasResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	assert.Equal(t, 1, out.MediaContainer.Size)
	assert.Equal(t, 2, out.MediaContainer.TotalSize)
	assert.Equal(t, 1, out.MediaContainer.Offset)
	require.Len(t, out.MediaContainer.Metadata, 1)
}

// An item Plex does not know has no extras of Plex's to keep, and the
// gateway is not asked.
func TestExtrasOfAnItemWithoutAPlexIDAreNone(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"`+plex.MoviesIdentifier+`","size":0,"Metadata":[]}}`, rec.Body.String())
	assert.Zero(t, gw.calls.Load())
}

// When the gateway cannot answer, the route answers an error, never 404 or
// an empty list: both read to PMS as "no extras", and it deletes the ones
// it has.
func TestExtrasAreAnErrorWhenTheGatewayCannotAnswer(t *testing.T) {
	gw := newGateway(t, errors.New("nats: no responders available for request"))
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestExtrasWithoutAGatewayAreUnavailable(t *testing.T) {
	h := extrasHandler(t, nil, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestExtrasOfAnUnknownItemAreNotFound(t *testing.T) {
	h := extrasHandler(t, nil)
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
