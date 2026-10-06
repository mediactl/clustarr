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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/plex"
)

const backToTheFuture = "5d7768244de0ee001fcc7fed"

func collectionClient(t *testing.T) *plex.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, token, r.Header.Get("X-Plex-Token"))
		id := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
		switch id {
		case backToTheFuture, arrival:
			_, _ = w.Write(fixture(t, "metadata_"+id+".json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)
	return c
}

// Plex cannot look a collection up by its TMDB id, but a member movie's own
// Plex metadata names its collection's plex:// GUID (recorded 2026-10-06).
func TestMovieCollectionIsReadFromTheMoviesPlexMetadata(t *testing.T) {
	got, err := collectionClient(t).MovieCollection(context.Background(), backToTheFuture)
	require.NoError(t, err)
	require.Equal(t, "5ec2eb574592b6004137f444", got)
}

// A movie in no collection answers "", not an error.
func TestMovieCollectionOfAMovieInNoneIsEmpty(t *testing.T) {
	got, err := collectionClient(t).MovieCollection(context.Background(), arrival)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestMovieCollectionOfAnUnknownMovieIsAnError(t *testing.T) {
	_, err := collectionClient(t).MovieCollection(context.Background(), "5d776b83fb0d55001f56a0ff")
	require.Error(t, err)
}

func TestMovieCollectionRefusesAMalformedPlexID(t *testing.T) {
	_, err := collectionClient(t).MovieCollection(context.Background(), "../matches")
	require.Error(t, err)
}

var _ metadata.PlexCollectionProvider = (*plex.Client)(nil)
