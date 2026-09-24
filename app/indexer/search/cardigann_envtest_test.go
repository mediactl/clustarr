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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/app/indexer/search"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// cardigannTracker serves one fixture page for every request.
func cardigannTracker(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "data", "cardigann", fixture))
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cardigannIndexer stands up a definition-backed Indexer the way production
// does: an IndexerDefinition, an Indexer naming it, and ONE pass of the real
// Indexer reconciler to resolve its caps, protocol and privacy from the
// definition. Nothing is hand-seeded, so the fan-out's caps gate sees exactly
// what the reconciler writes.
func cardigannIndexer(t *testing.T, ctx context.Context, c client.Client, ns, name, baseURL string) *indexv1alpha1.Indexer {
	t.Helper()
	newNamespace(t, ctx, c, ns)
	yaml, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "data", "cardigann", "search-error.yml"))
	require.NoError(t, err)
	def := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: ns + "-def"},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: string(yaml)},
	}
	require.NoError(t, c.Create(ctx, def))
	t.Cleanup(func() { _ = c.Delete(context.Background(), def) })

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: baseURL, DefinitionRef: ptr.To(def.Name)},
	}
	require.NoError(t, c.Create(ctx, idx))

	r := indexer.NewReconciler(c, k8sevents.NewFakeRecorder(10), nil, nil)
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.NotNil(t, idx.Status.Caps, "the reconciler did not resolve caps from the definition")
	return idx
}

// cardigannService is the fan-out wired the way indexarr/run.go wires it:
// ClientFor is the production ClientCache, not a stub, so the Cardigann
// engine is reached through the SAME seam a Torznab client is (ruling R5).
func cardigannService(c client.Client) *search.Service {
	cc := indexer.NewClientCache(c, ratelimit.New(ratelimit.Config{}))
	return &search.Service{
		Client: c,
		ClientFor: func(ctx context.Context, idx *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			cli, err := cc.For(ctx, idx)
			if err != nil {
				return nil, err
			}
			return cli, nil
		},
		Now: pastTheStartupGrace(),
	}
}

// Ruling R6, end to end. A Cardigann tracker answering with its error page
// ("you are rate limited") used to parse as zero rows -- a successful search
// with nothing in it -- so the escalation ladder never saw a failure and a
// failing tracker was queried at full rate forever. It must now be an error
// outcome AND a recorded failure that escalates the indexer.
func TestACardigannErrorPageEscalatesTheIndexer(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	tracker := cardigannTracker(t, "search-error.html")
	idx := cardigannIndexer(t, ctx, c, "search-cardigann-err", "ratelimited", tracker.URL)
	require.Zero(t, idx.Status.EscalationLevel)

	req := searchRequest(idx.Namespace)
	req.Text = "Inception"
	resp := cardigannService(c).Search(ctx, req)

	require.Len(t, resp.Outcomes, 1)
	out := resp.Outcomes[0]
	require.Equal(t, schema.SearchOutcomeError, out.Status,
		"a tracker error page was reported as %q -- the R6 defect: failure reads as no results", out.Status)
	require.Contains(t, out.Error, "rate limited")
	require.Empty(t, resp.Releases)

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Equal(t, int32(1), got.Status.EscalationLevel, "the failure did not escalate the indexer")
	require.NotNil(t, got.Status.DisabledUntil, "the escalation did not start a backoff")
	require.NotNil(t, got.Status.LastFailureAt)
	require.Contains(t, got.Status.LastFailure, "Too many requests")
}

// The mirror: the same definition against a results page is an ordinary
// successful search with its releases, projected for the firehose and
// carrying the definition's protocol.
func TestACardigannResultsPageIsAnOrdinarySearch(t *testing.T) {
	ctx := t.Context()
	c := requireEnvtest(t)
	tracker := cardigannTracker(t, "search-error-results.html")
	idx := cardigannIndexer(t, ctx, c, "search-cardigann-ok", "healthy", tracker.URL)

	req := searchRequest(idx.Namespace)
	req.Text = "Some Movie"
	resp := cardigannService(c).Search(ctx, req)

	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status, resp.Outcomes[0].Error)
	require.Len(t, resp.Releases, 2)
	for _, r := range resp.Releases {
		require.Equal(t, "torrent", string(r.Info.Protocol))
		require.Equal(t, "healthy", r.Info.IndexerRef)
	}

	var got indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &got))
	require.Zero(t, got.Status.EscalationLevel)
}
