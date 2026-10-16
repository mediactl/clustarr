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

package download

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// An indexer that answers a payload fetch with 429 is not asked again until
// its Retry-After has passed: the verb refuses locally with the retry time,
// so the engines' retries stop spending the allowance the indexer gives
// back (nzb-life, three grabs, seven hours of 429s, 2026-10-16).
func TestA429PausesTheIndexersGrabsUntilRetryAfter(t *testing.T) {
	ctx := context.Background()
	idx := testIndexer("media", "tr", "uid-429", indexv1alpha1.LimitUnitDay)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	fetches := 0
	res := &FetchResult{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"600"}}, Body: io.NopCloser(strings.NewReader(""))}
	s := &Service{
		Client: fakeClient(t, idx), Bus: bus, Now: func() time.Time { return now },
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			fetches++
			return stubFetcher{res: res}, nil
		},
	}
	req := func(guid string) schema.DownloadRequest {
		return schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: guid, URL: "https://x/y"}
	}

	first := s.Handle(ctx, req("g1"))
	require.NotEmpty(t, first.Error)
	require.NotNil(t, first.RetryAt, "a 429 names when to retry")
	require.Equal(t, now.Add(600*time.Second), first.RetryAt.UTC())
	_, limited := limits.GrabLimited(first.Error)
	require.True(t, limited, "the 429 reads as a grab limit so the facade answers 429 with Retry-After: %q", first.Error)
	require.False(t, first.Gone)

	second := s.Handle(ctx, req("g2"))
	require.Equal(t, 1, fetches, "the paused indexer must not be asked again")
	require.NotNil(t, second.RetryAt)
	require.Equal(t, first.RetryAt.UTC(), second.RetryAt.UTC())

	now = first.RetryAt.Add(time.Second)
	res = &FetchResult{MagnetURL: "magnet:?xt=urn:btih:z"}
	third := s.Handle(ctx, req("g3"))
	require.Empty(t, third.Error, "the pause ended")
	require.Equal(t, 2, fetches)
	_, paused, err := limits.GrabPaused(ctx, bus.KV(events.BucketIndexerLimits), idx, now)
	require.NoError(t, err)
	require.False(t, paused, "a successful grab clears the pause")
}
