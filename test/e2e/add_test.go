//go:build e2e

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

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
)

// TestAddNewSearchesAndAddsAMovie is scenario 19, Add New
// (docs/superpowers/specs/2026-09-29-add-new-design.md): through the ui
// Service, a movie search reaches catalogarr's metadata RPC and the
// in-cluster TMDB stub, which answers the recorded "inception" search;
// adding its second hit, "Inception: The Cobol Job" (613092, owned by no
// other scenario), creates the Movie beside its root folder, spec only,
// owned by clustarr-ui and never on status; and adding it again opens it.
func TestAddNewSearchesAndAddsAMovie(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	base, _ := portForwardService(ctx, t, "ui", uiServicePort)
	c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rf := newRootFolder(ctx, t, "e2e19-add-rf", catalogv1alpha1.RootFolderKindMovie, "movies")

	waitFor(t, ctx, uiPageWaitTimeout, "the movie search shows the stub's hits", func(ctx context.Context) (bool, error) {
		body, status, err := httpGetString(ctx, c, base+"/library/movies/add/search?q=inception")
		if err != nil || status != http.StatusOK {
			//nolint:nilerr // keep polling; a port-forward hiccup is transient
			return false, nil
		}
		return strings.Contains(body, `data-add-hit="613092"`) && !strings.Contains(body, "image.tmdb.org/t/p"), nil
	})

	form := url.Values{
		"title": {"Inception: The Cobol Job"}, "id": {"613092"},
		"rootFolder": {rf.Namespace + "/" + rf.Name}, "qualityProfile": {QualityProfileName},
		"monitored": {"false", "true"}, "monitor": {"movieOnly"}, "minimumAvailability": {"released"},
		"searchOnAdd": {"false"},
	}
	body, status, err := httpPostForm(ctx, c, base+"/library/movies/add", form)
	require.NoError(t, err)
	if skipIfNoWriter(t, status, body) {
		return
	}
	require.Equal(t, http.StatusSeeOther, status, "unexpected add response: %s", body)

	name := names.Movie("Inception: The Cobol Job", 613092)
	var m catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: rf.Namespace, Name: name}, &m))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &m) })
	require.Equal(t, int64(613092), m.Spec.TmdbID)
	require.Equal(t, rf.Name, m.Spec.RootFolderRef)
	for _, e := range m.GetManagedFields() {
		if e.Manager == "clustarr-ui" {
			require.Empty(t, e.Subresource, "clustarr-ui never writes status")
			require.NotContains(t, e.FieldsV1.GetRawString(), `"f:status"`)
		}
	}

	_, status, err = httpPostForm(ctx, c, base+"/library/movies/add", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, status, "adding it again opens it")
}
