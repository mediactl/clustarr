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

package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// A query's turn on its indexer's rate limiter is ours to give, not the
// indexer's to answer: waiting for it must not spend spec.timeout, and a turn
// that never comes inside the search's budget is a skip, not a failure that
// escalates the indexer. On 2026-10-06 a wanted sweep of 104 searches queued
// on nzbgeek's 2s spacing at once; every query past the fifteenth or so ran
// out of its 30s timeout before it was sent, was recorded as nzbgeek timing
// out, and disabled it.

const pacedFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>
<item><title>Inception.2010.1080p.BluRay.x264-GRP</title><guid>g1</guid>
<link>https://idx.example/get/1</link><pubDate>Mon, 05 Oct 2026 00:00:00 +0000</pubDate>
<enclosure url="https://idx.example/get/1" length="1000" type="application/x-nzb"/>
<newznab:attr name="category" value="2000"/><newznab:attr name="size" value="1000"/></item>
</channel></rss>`

// pacedService is a Service whose one indexer is a real torznab client, paced
// at one request every spacing, with a spec.timeout of perRequest, against an
// httptest indexer that answers at once.
func pacedService(t *testing.T, spacing, perRequest time.Duration) (*Service, indexv1alpha1.Indexer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(pacedFeed))
	}))
	t.Cleanup(srv.Close)

	idx := healthyIndexer("paced")
	idx.UID = types.UID("uid-paced")
	idx.Spec.BaseURL = srv.URL
	idx.Spec.Timeout = metav1.Duration{Duration: perRequest}
	lim := ratelimit.New(ratelimit.Config{RPS: float64(time.Second) / float64(spacing), Burst: 1})
	cli, err := torznab.NewClient(srv.URL, "key", torznab.WithRateLimit(lim), torznab.WithTimeout(perRequest))
	require.NoError(t, err)
	s := &Service{
		Client: newFakeClient(&idx),
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (IndexerClient, error) {
			return cli, nil
		},
	}
	return s, idx
}

// searchConcurrently runs n single-indexer searches at once, as indexarr's
// concurrent Serve does for a wanted sweep, and returns each one's outcome.
func searchConcurrently(t *testing.T, s *Service, idx indexv1alpha1.Indexer, n int, budget time.Duration) []schema.SearchOutcome {
	t.Helper()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []schema.SearchOutcome
	)
	for range n {
		wg.Go(func() {
			cands := selectCandidates([]indexv1alpha1.Indexer{idx}, movieRequest(), torznab.ModeMovieSearch, selectNow)
			outcomes, _ := s.fanOut(context.Background(), cands, movieRequest(), torznab.ModeMovieSearch, budget)
			mu.Lock()
			out = append(out, outcomes...)
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

func TestWaitingForTheRateLimiterDoesNotSpendTheIndexersTimeout(t *testing.T) {
	// Six searches at one request per 50ms: the last is sent ~250ms after the
	// first, well past the 100ms spec.timeout, yet each request itself is
	// answered at once, and the 2s budget has room for all of them.
	s, idx := pacedService(t, 50*time.Millisecond, 100*time.Millisecond)
	outcomes := searchConcurrently(t, s, idx, 6, 2*time.Second)

	require.Len(t, outcomes, 6)
	for _, o := range outcomes {
		require.Equal(t, schema.SearchOutcomeOK, o.Status, "error: %s", o.Error)
	}
}

func TestAQueryTheLimiterCannotSendInTheBudgetIsSkippedNotFailed(t *testing.T) {
	// One request per 200ms and a 300ms budget: two searches get a turn, the
	// rest cannot, and are skipped as paced -- and the indexer, which never
	// saw them, is neither failed nor escalated.
	s, idx := pacedService(t, 200*time.Millisecond, time.Second)
	outcomes := searchConcurrently(t, s, idx, 6, 300*time.Millisecond)

	var ok, paced int
	for _, o := range outcomes {
		switch {
		case o.Status == schema.SearchOutcomeOK:
			ok++
		case o.Status == schema.SearchOutcomeSkipped && o.Error == skipPaced:
			paced++
		default:
			t.Errorf("outcome %s (%s): a query that was never sent is neither an error nor a timeout", o.Status, o.Error)
		}
	}
	require.Positive(t, ok, "the first turns are sent")
	require.Positive(t, paced, "the rest are skipped as paced")

	var got indexv1alpha1.Indexer
	require.NoError(t, s.Client.Get(context.Background(), types.NamespacedName{Namespace: idx.Namespace, Name: idx.Name}, &got))
	require.Zero(t, got.Status.EscalationLevel, "a paced skip is not the indexer's failure")
	require.Empty(t, got.Status.LastFailure)
}
