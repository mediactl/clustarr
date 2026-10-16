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
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

// Fetches to one indexer are single-flight: a burst that arrives together
// asks the indexer once, and the rest read the pause the first fetch
// recorded instead of each spending a hit (about 35 entries retry together
// when a pause ends).
func TestABurstAsksAPausedIndexerOnce(t *testing.T) {
	ctx := context.Background()
	idx := testIndexer("media", "tr", "uid-burst", indexv1alpha1.LimitUnitDay)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	var fetches atomic.Int32
	s := &Service{
		Client: fakeClient(t, idx), Bus: bus,
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			fetches.Add(1)
			return slowFetcher{res: &FetchResult{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"600"}}, Body: io.NopCloser(strings.NewReader(""))}}, nil
		},
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := s.Handle(ctx, schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: fmt.Sprintf("g%d", i), URL: "https://x/y"})
			require.NotNil(t, resp.RetryAt)
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), fetches.Load(), "the burst must ask the indexer once")
}

// slowFetcher answers after a pause, so concurrent callers overlap.
type slowFetcher struct{ res *FetchResult }

func (f slowFetcher) Fetch(context.Context, string) (*FetchResult, error) {
	time.Sleep(30 * time.Millisecond)
	return f.res, nil
}
func (slowFetcher) Scrub(s string) string { return s }

// nzb-life answers a second fetch of an NZB within 24 h with 429 and
// "Already Downloaded": the cooldown is that release's, not the account's,
// and an indexer-wide pause on it held every nzb-life grab for the longest
// cooldown among the entries retrying (2026-10-16). Such a 429 pauses the
// release alone -- refused locally until its Retry-After -- and the next
// release is fetched.
func TestAnAlreadyDownloadedAnswerPausesOnlyThatRelease(t *testing.T) {
	ctx := context.Background()
	idx := testIndexer("media", "nzb-life", "uid-cooldown", indexv1alpha1.LimitUnitDay)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	now := time.Date(2026, 10, 16, 20, 35, 0, 0, time.UTC)

	const cooldown = `<html><body><h1>Already Downloaded</h1><p>You already downloaded this NZB in the last 24 hours.
The same NZB can be downloaded again after the cooldown expires. You can download this same NZB again in 871 minute(s).</p></body></html>`
	fetched := map[string]int{}
	s := &Service{
		Client: fakeClient(t, idx), Bus: bus, Now: func() time.Time { return now },
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return fetcherFunc(func(_ context.Context, rawURL string) (*FetchResult, error) {
				fetched[rawURL]++
				if strings.HasSuffix(rawURL, "/cooled") {
					return &FetchResult{
						Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"52260"}},
						Body: io.NopCloser(strings.NewReader(cooldown)),
					}, nil
				}
				return &FetchResult{
					Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-nzb"}},
					ContentLen: -1, Body: io.NopCloser(strings.NewReader(`<?xml version="1.0"?><nzb/>`)),
				}, nil
			}), nil
		},
	}
	req := func(guid, u string) schema.DownloadRequest {
		return schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "nzb-life"}, GUID: guid, URL: u}
	}

	first := s.Handle(ctx, req("g-cooled", "https://x/cooled"))
	require.NotEmpty(t, first.Error)
	require.NotNil(t, first.RetryAt, "the cooldown names when to retry")
	require.Equal(t, now.Add(52260*time.Second), first.RetryAt.UTC())
	require.False(t, first.Gone)

	again := s.Handle(ctx, req("g-cooled", "https://x/cooled"))
	require.Equal(t, 1, fetched["https://x/cooled"], "a release in cooldown is not asked for again")
	require.NotNil(t, again.RetryAt)
	require.Equal(t, first.RetryAt.UTC(), again.RetryAt.UTC())

	other := s.Handle(ctx, req("g-new", "https://x/new"))
	require.Empty(t, other.Error, "the indexer's other releases are still fetched: %q", other.Error)
	require.Equal(t, 1, fetched["https://x/new"])
	_, paused, err := limits.GrabPaused(ctx, bus.KV(events.BucketIndexerLimits), idx, now)
	require.NoError(t, err)
	require.False(t, paused, "a release's cooldown must not pause the indexer")
}

// fetcherFunc answers each URL its own way.
type fetcherFunc func(ctx context.Context, rawURL string) (*FetchResult, error)

func (f fetcherFunc) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	return f(ctx, rawURL)
}
func (fetcherFunc) Scrub(s string) string { return s }
