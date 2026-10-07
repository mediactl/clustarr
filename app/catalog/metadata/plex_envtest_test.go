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

package metadata_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// stubPlex stands in for the plex client: a resolver and a PlexProvider
// that can be switched to failing, as Plex's cloud throttling would.
type stubPlex struct{ fail *bool }

func (stubPlex) Name() string                           { return "plex" }
func (stubPlex) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubPlex) Resolve(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) (pkgmetadata.ExternalIDs, error) {
	if *p.fail {
		return nil, &pkgmetadata.RateLimitedError{Provider: "plex"}
	}
	return pkgmetadata.ExternalIDs{pkgmetadata.KeyPlex: "5d9c086c46115600200aa2fe"}, nil
}

func (p stubPlex) ShowChildren(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.PlexChildren, error) {
	if *p.fail {
		return nil, errors.New("plex: unexpected status 502")
	}
	return &pkgmetadata.PlexChildren{ShowID: "5d9c086c46115600200aa2fe", Seasons: []pkgmetadata.PlexSeason{
		{Number: 0, ID: "5d9c09dd3c3f87001f36250e"},
		{Number: 1, ID: "5d9c09dd3c3f87001f36250a"},
	}}, nil
}

// A Series refresh lands its Plex id in externalIDs and its seasons' in
// plexSeasons, both under the gateway's manager; a second refresh with
// Plex failing releases neither (spec §4: a failed lookup keeps the ids
// stored before).
func TestHandlerLandsASeriesPlexIDsAndKeepsThemWhenPlexFails(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hplex", "got"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 121361, QualityProfileRef: "web", RootFolderRef: "tv"},
	}))
	login, err := os.ReadFile("../../../test/data/metadata/tvdb/login.json")
	require.NoError(t, err)
	series, err := os.ReadFile("../../../test/data/metadata/tvdb/series_121361.json")
	require.NoError(t, err)
	tvdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			_, _ = w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			_, _ = w.Write(series)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(tvdbSrv.Close)
	tv := tvdb.New("test-key", "test-pin", tvdbSrv.Client(), tvdbSrv.URL, pkgmetadata.NewLimiter(1000, 1))

	fail := false
	p := stubPlex{fail: &fail}
	h := &metadata.Handler{
		Client: c, Reader: c, Cache: noopCache{},
		Registry: &pkgmetadata.Registry{
			Series:    []pkgmetadata.SeriesProvider{tv},
			Resolvers: []pkgmetadata.IDResolver{p},
			Plex:      []pkgmetadata.PlexProvider{p},
		},
	}
	key := types.NamespacedName{Namespace: ns, Name: name}
	want := []catalogv1alpha1.PlexSeasonRef{{Number: 0, ID: "5d9c09dd3c3f87001f36250e"}, {Number: 1, ID: "5d9c09dd3c3f87001f36250a"}}

	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))
	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, key, &got))
	require.Equal(t, "5d9c086c46115600200aa2fe", got.Status.Metadata.ExternalIDs["plex"])
	require.Equal(t, want, got.Status.Metadata.PlexSeasons)
	requireManagerOwns(t, got.ManagedFields, k8s.ManagerCatalogarrMetadata, `"f:plexSeasons"`, `"f:plex"`)

	fail = true
	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))
	require.NoError(t, c.Get(ctx, key, &got))
	require.Equal(t, "5d9c086c46115600200aa2fe", got.Status.Metadata.ExternalIDs["plex"], "a failed lookup must not release the plex id")
	require.Equal(t, want, got.Status.Metadata.PlexSeasons, "a failed lookup must not release plexSeasons")
	require.Equal(t, "Game of Thrones", got.Status.Metadata.Title)
}

// requireManagerOwns asserts that manager's managedFields entry names
// every field path fragment in paths: an over- or under-claim is visible
// only there (CLAUDE.md, "A double-claim is silent").
func requireManagerOwns(t *testing.T, mfs []metav1.ManagedFieldsEntry, manager k8s.FieldManager, paths ...string) {
	t.Helper()
	for _, mf := range mfs {
		if mf.Manager != string(manager) || mf.FieldsV1 == nil {
			continue
		}
		for _, p := range paths {
			require.True(t, strings.Contains(mf.FieldsV1.GetRawString(), p), "%s owns no %s: %s", manager, p, mf.FieldsV1.GetRawString())
		}
		return
	}
	t.Fatalf("no managedFields entry for %s", manager)
}
