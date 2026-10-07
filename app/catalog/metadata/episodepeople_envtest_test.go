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
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
)

// A Series refresh through the real Handler files the people of its
// episodes with a file: the episodes are listed from a real apiserver by
// Episode's selectable field spec.seriesRef -- a field the apiserver did
// not serve would fail the list, and nothing would be filed -- and the
// people come from the real TVDB client over TheTVDB's recorded episode.
func TestASeriesRefreshFilesItsEpisodesPeopleFromTheApiserver(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hpeople", "got"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 121361, QualityProfileRef: "web", RootFolderRef: "tv"},
	}))
	newEpisode := func(epName, seriesRef string, number int32, hasFile bool) *catalogv1alpha1.Episode {
		t.Helper()
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: epName, Namespace: ns},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: seriesRef, SeasonNumber: 1, EpisodeNumber: number},
		}
		require.NoError(t, c.Create(ctx, ep))
		_, err := k8s.PatchStatus(ctx, c, k8s.FieldManager("catalogarr"), catalogac.Episode(epName, ns).
			WithStatus(catalogac.EpisodeStatus().WithHasFile(hasFile).WithTvdbID(295294)))
		require.NoError(t, err)
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(ep), ep))
		return ep
	}
	withFile := newEpisode("got-s01e01", name, 1, true)
	noFile := newEpisode("got-s01e02", name, 2, false)
	otherSeries := newEpisode("other-s01e01", "other", 1, true)

	login, err := os.ReadFile("../../../test/data/metadata/tvdb/login.json")
	require.NoError(t, err)
	series, err := os.ReadFile("../../../test/data/metadata/tvdb/series_121361.json")
	require.NoError(t, err)
	people, err := os.ReadFile("../../../test/data/metadata/tvdb/episode-295294-extended.json")
	require.NoError(t, err)
	asked := 0
	tvdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			_, _ = w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			_, _ = w.Write(series)
		case r.URL.Path == "/episodes/295294/extended":
			asked++
			_, _ = w.Write(people)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(tvdbSrv.Close)

	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketMetadataExtended)
	h := &metadata.Handler{
		Client: c, Reader: c, Cache: noopCache{}, Extended: kv,
		Registry: &pkgmetadata.Registry{Series: []pkgmetadata.SeriesProvider{
			tvdb.New("test-key", "test-pin", tvdbSrv.Client(), tvdbSrv.URL, pkgmetadata.NewLimiter(1000, 1)),
		}},
	}
	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))

	e, err := kv.Get(ctx, extended.Key(commonv1.MediaKindEpisode, withFile.UID))
	require.NoError(t, err, "the episode with a file has its people")
	doc, err := extended.Decode(e.Value)
	require.NoError(t, err)
	require.Len(t, doc.Role, 9)
	require.Len(t, doc.Director, 1)
	require.Len(t, doc.Writer, 1)
	for _, ep := range []*catalogv1alpha1.Episode{noFile, otherSeries} {
		_, err := kv.Get(ctx, extended.Key(commonv1.MediaKindEpisode, ep.UID))
		require.ErrorIs(t, err, events.ErrKeyNotFound, ep.Name)
	}
	require.Equal(t, 1, asked)
}
