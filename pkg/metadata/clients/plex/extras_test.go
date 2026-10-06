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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/plex"
)

// Arrival's extras as recorded from metadata.provider.plex.tv pass through
// byte for byte, so every field PMS reads (extraType, Media.url to Internet
// Video Archive) reaches it unchanged.
func TestExtrasAreTheItemsOwnAsPlexSendsThem(t *testing.T) {
	recordedBody := fixture(t, "extras_"+arrival+".json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Plex-Token"))
		require.Equal(t, "/library/metadata/"+arrival+"/extras", r.URL.Path)
		_, _ = w.Write(recordedBody)
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)

	got, err := c.Extras(context.Background(), arrival)
	require.NoError(t, err)

	var want struct {
		MediaContainer struct {
			Metadata []json.RawMessage `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, json.Unmarshal(recordedBody, &want))
	require.Len(t, got, len(want.MediaContainer.Metadata))
	for i := range got {
		require.JSONEq(t, string(want.MediaContainer.Metadata[i]), string(got[i]))
	}
	require.Contains(t, string(got[0]), "internetvideoarchive")
}

// Plex's service refuses a page over 100 with 400 Bad Request (2026-10-06),
// so a long list is read in pages no larger than that.
func TestExtrasPageThroughPlexsLimit(t *testing.T) {
	const total = 230
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, _ := strconv.Atoi(q.Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(q.Get("X-Plex-Container-Size"))
		if size > 100 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sizes = append(sizes, size)
		var items []string
		for i := start; i < min(start+size, total); i++ {
			items = append(items, fmt.Sprintf(`{"title":"extra %d"}`, i))
		}
		_, _ = fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[%s]}}`, total, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)

	got, err := c.Extras(context.Background(), arrival)
	require.NoError(t, err)
	require.Len(t, got, total)
	require.JSONEq(t, `{"title":"extra 229"}`, string(got[total-1]))
	require.Equal(t, []int{100, 100, 100}, sizes)
}

// An item Plex has no extras for is an empty list, not an error: "none" is
// an answer the gateway stores.
func TestExtrasOfAnItemWithNoneAreEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":0}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)

	got, err := c.Extras(context.Background(), arrival)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}

// A failed request is an error, never an empty list: an empty answer would
// tell PMS to delete the trailers it holds.
func TestExtrasAreAnErrorWhenPlexFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)

	got, err := c.Extras(context.Background(), arrival)
	require.Error(t, err)
	require.Nil(t, got)
}

func TestExtrasRefuseAMalformedPlexID(t *testing.T) {
	c, err := plex.New(plex.Config{Token: token, BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	_, err = c.Extras(context.Background(), "../matches")
	require.Error(t, err)
}

var _ metadata.PlexExtrasProvider = (*plex.Client)(nil)
