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
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
)

// movieListCounter counts the MovieList reads the Plex provider makes --
// one per catalogue index build -- and records whether each asked to skip
// the deep copy.
type movieListCounter struct {
	client.Reader
	lists    atomic.Int32
	deepCopy atomic.Int32
}

func (r *movieListCounter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*catalogv1.MovieList); ok {
		r.lists.Add(1)
		var lo client.ListOptions
		lo.ApplyOptions(opts)
		if lo.UnsafeDisableDeepCopy == nil || !*lo.UnsafeDisableDeepCopy {
			r.deepCopy.Add(1)
		}
	}
	return r.Reader.List(ctx, list, opts...)
}

// TestPlexRequestsShareOneCatalogueIndex is the review's per-request rebuild
// seen through the real Server: two Plex requests inside
// projection.IndexTTL list the catalogue once, and list it without a deep
// copy -- the index is read-only. (The TTL's expiry is
// ui/projection/index_memo_test.go's, against a settable clock.)
func TestPlexRequestsShareOneCatalogueIndex(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default", UID: "11111111-1111-1111-1111-111111111111"},
		Spec:       catalogv1.MovieSpec{TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "Heat", Year: 1995}},
	}
	reader := &movieListCounter{
		Reader: fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(movie).Build(),
	}
	h := ui.NewServer(context.Background(), ui.Options{
		Reader: reader,
		Plex:   &ui.PlexOptions{ExternalURL: "http://ui.example"},
	}).Handler()

	for range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plex/movies/library/metadata/"+string(movie.UID), nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	require.EqualValues(t, 1, reader.lists.Load(), "two Plex requests within the TTL built the index twice")
	require.Zero(t, reader.deepCopy.Load(), "the read-only index was listed with a deep copy")
}
