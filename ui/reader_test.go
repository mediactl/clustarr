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
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
)

// TestReadyzGatesOnWaitForSync is Task D3-0's own acceptance check for
// ruling R2: /readyz must answer 503 while the cluster reader has not
// finished its initial sync and 200 once it has.
//
// It does not need a real cluster to prove this -- Options.WaitForSync is a
// plain function, the same injection seam Options.Entries already is (see
// ui/server_test.go), so a controllable stub is enough to exercise
// handleReadyz's own logic deterministically. TestNewClusterReaderSeesRealObjects
// below is what proves the real ui.NewClusterReader wiring behind that seam
// actually works against a cluster.
func TestReadyzGatesOnWaitForSync(t *testing.T) {
	var synced atomic.Bool
	srv := ui.NewServer(t.Context(), ui.Options{
		WaitForSync: func(context.Context) bool { return synced.Load() },
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"/readyz must be 503 before the cache has synced")

	synced.Store(true)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code,
		"/readyz must be 200 once the cache has synced")
}

// TestReadyzGatesOnTheFirstProjectionRound is X14's /readyz gate: a synced
// cache is not yet a page with rows, so /readyz stays 503 until
// Options.Projected (projection.Projection.Projected in production) reports
// the first round done, even with WaitForSync already true. Before it, a
// fresh pod reported Ready and served empty pages until its first round
// landed.
func TestReadyzGatesOnTheFirstProjectionRound(t *testing.T) {
	var projected atomic.Bool
	srv := ui.NewServer(t.Context(), ui.Options{
		WaitForSync: func(context.Context) bool { return true },
		Projected:   projected.Load,
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"/readyz must be 503 before the first projection round, even with the cache synced")

	projected.Store(true)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code, "/readyz must be 200 once the first round has completed")
}

// TestReadyzDefaultsToReadyWithNoReaderConfigured is the "keep the
// no-cluster path working" half of Task D3-0's brief: Options.Reader nil
// must stay legal, and a ui process with no cluster configured at all (the
// common case for a developer running `clustarr ui` with no kubeconfig)
// must still become Ready rather than sitting at 503 forever with nothing
// that will ever flip it.
func TestReadyzDefaultsToReadyWithNoReaderConfigured(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestNilReaderStillServesThePipelinePage is the other half of that brief
// paragraph, pinned directly to Options.Reader rather than relying on
// ui/server_test.go's pre-existing Entries-only coverage to imply it: a nil
// Reader (and so, transitively, an Entries this task does not wire) must
// still serve /pipeline 200, exactly as it does today.
func TestNilReaderStillServesThePipelinePage(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Reader: nil})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestNewClusterReaderSeesRealObjects proves ui.NewClusterReader's wiring is
// real: built against an envtest apiserver, it must observe an object it did
// not create (a writer client stands in for whatever controller actually
// creates one in production -- ui never writes, so this test cannot use the
// reader for that half), and its WaitForCacheSync func must eventually
// report true.
//
// It needs the envtest control-plane binaries and skips without them, like
// every other envtest suite in this tree; `make test` sets
// KUBEBUILDER_ASSETS.
func TestNewClusterReaderSeesRealObjects(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	scheme, err := ui.NewReaderScheme()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader, waitForSync, err := ui.NewClusterReader(ctx, cfg, scheme)
	require.NoError(t, err)
	require.NotNil(t, reader)
	require.NotNil(t, waitForSync)

	// A plain client.Client, standing in for whatever writes a Movie in
	// production (importarr, or a person with kubectl) -- never
	// ui.NewClusterReader's own reader, which cannot write at all.
	writer, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: "default"},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: "hd-1080p", RootFolderRef: "movies",
		},
	}
	require.NoError(t, writer.Create(ctx, movie))

	var seen catalogv1alpha1.Movie
	require.Eventually(t, func() bool {
		var list catalogv1alpha1.MovieList
		if err := reader.List(ctx, &list); err != nil {
			return false
		}
		for i := range list.Items {
			if list.Items[i].Name == movie.Name {
				seen = list.Items[i]
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "ui's cluster reader never observed the Movie a separate writer created")

	// The reader strips managedFields (2026-09-24): on this library they are
	// a third of a 57 MB Episode list and the ui reads none of them.
	var direct catalogv1alpha1.Movie
	require.NoError(t, writer.Get(ctx, client.ObjectKeyFromObject(movie), &direct))
	require.NotEmpty(t, direct.ManagedFields, "the apiserver records managedFields on a created object")
	require.Empty(t, seen.ManagedFields, "the cached copy carries no managedFields")

	require.True(t, waitForSync(ctx), "WaitForCacheSync must report true once the informer above has synced")
}
