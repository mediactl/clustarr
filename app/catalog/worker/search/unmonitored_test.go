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

package search_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/version"
)

// builderIndexer registers an index on a fake client builder.
type builderIndexer struct{ b *fake.ClientBuilder }

func (i builderIndexer) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

type countingRPC struct{ n atomic.Int32 }

func (r *countingRPC) Search(context.Context, schema.SearchRequest) (schema.SearchResponse, error) {
	r.n.Add(1)
	return schema.SearchResponse{}, nil
}

// An automatic search of an episode that is no longer monitored -- itself
// or through its Series -- spends no indexer query: a task queued before the
// owner turned the season off is dropped when it is picked up (Sonarr's
// MonitoredEpisodeSpecification).
func TestWorkerSkipsAnAutomaticSearchOfAnUnmonitoredEpisode(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		seriesMonitored, episodeMonitor bool
		wantQueries                     int32
	}{
		{"both monitored", true, true, 1},
		{"series unmonitored", false, true, 0},
		{"episode unmonitored", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aired := metav1.NewTime(time.Now().Add(-48 * time.Hour))
			b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
				WithStatusSubresource(&catalogv1alpha1.Episode{})
			require.NoError(t, search.RegisterDownloadIndexes(context.Background(), builderIndexer{b}))
			c := b.
				WithObjects(
					testQualityProfile("hd"),
					&catalogv1alpha1.Series{
						ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "show"},
						Spec: catalogv1alpha1.SeriesSpec{
							TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: "tv",
							Monitored: ptr.To(tc.seriesMonitored),
						},
					},
					&catalogv1alpha1.Episode{
						ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "show-s01e01"},
						Spec: catalogv1alpha1.EpisodeSpec{
							SeriesRef: "show", SeasonNumber: 1, EpisodeNumber: 1,
							Monitored: ptr.To(tc.episodeMonitor),
						},
						Status: catalogv1alpha1.EpisodeStatus{AirDate: &aired},
					}).
				Build()
			rpc := &countingRPC{}
			w := search.NewWorker(c, rpc, catalogue.LoadedCatalogue())
			task := schema.SearchTask{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "show-s01e01"},
				Reason:   schema.SearchReasonMissing,
			}
			schemaName, data, err := schema.Encode(task)
			require.NoError(t, err)
			env := &events.Envelope{
				ID: "test", Type: "catalog.SearchTask", Schema: schemaName,
				Source: "catalogarr@" + version.String(), Key: "tv/show-s01e01", Time: time.Now().UTC(), Data: data,
			}
			_ = w.Handle(context.Background(), testMessage{env: env})
			assert.Equal(t, tc.wantQueries, rpc.n.Load())
		})
	}
}
