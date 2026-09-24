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

package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// rpc.indexarr.download, end to end for a definition-backed Indexer: the
// verb resolves the Indexer, dispatches to ClientCache.DefinitionFetcherFor,
// and the REAL engine runs 1337x.yml's download block -- fetching the details
// page and picking the magnet link off it with the definition's selector. A
// plain GET of that URL would have handed grabarr an HTML page.
func TestTheDownloadVerbRunsTheDefinitionsDownloadBlock(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pages++
		_, _ = w.Write([]byte(cardigannFixture(t, "1337x-details.html")))
	}))
	defer srv.Close()

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "leetx", Namespace: "media", UID: "u-dl", ResourceVersion: "1"},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: srv.URL, DefinitionRef: ptr.To("leetx-def")},
	}
	c := fakeClient(t, idx, idxDefinition("leetx-def", cardigannFixture(t, "1337x.yml"), nil, ""))
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))

	svc := &download.Service{
		Client: c,
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (download.Fetcher, error) {
			t.Fatal("a definition-backed grab went through the plain fetcher")
			return nil, nil
		},
		Definitions: cc.DefinitionFetcherFor,
	}
	got := svc.Handle(context.Background(), schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "leetx"},
		GUID:       srv.URL + "/torrent/1000001/Some.Movie/",
		URL:        srv.URL + "/torrent/1000001/Some.Movie/",
	})
	require.Empty(t, got.Error)
	require.Contains(t, got.MagnetURL, "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01")
	require.Equal(t, 1, pages)
}
