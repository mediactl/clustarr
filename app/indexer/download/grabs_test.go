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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	return bus
}

func testIndexer(ns, name, uid string, unit indexv1alpha1.LimitUnit) *indexv1alpha1.Indexer {
	return &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: k8stypes.UID(uid)},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "https://tr.example",
			Limits:  &indexv1alpha1.Limits{Unit: unit},
		},
	}
}

// countingFetcher answers every fetch with a magnet link, or err, and counts
// the requests that reached the indexer.
type countingFetcher struct {
	calls *atomic.Int32
	err   error
}

func (f countingFetcher) Fetch(context.Context, string) (*FetchResult, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &FetchResult{MagnetURL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}, nil
}
func (countingFetcher) Scrub(s string) string { return s }

func grabRequest(guid string) schema.DownloadRequest {
	return schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "tr"}, GUID: guid, URL: "https://tr.example/dl/" + guid,
	}
}

// spec.limits.grabLimit was counted and never enforced. The verb now
// reserves the grab on the ring before it asks the indexer, and a grab over
// the limit is refused -- with an Error GrabLimited recognises, naming when
// the window next has room -- and never reaches the indexer. A GUID already
// in the window is the same grab and passes.
func TestTheVerbRefusesAGrabOverTheLimitWithoutAskingTheIndexer(t *testing.T) {
	ctx := context.Background()
	idx := testIndexer("media", "tr", "uid-grablimit", indexv1alpha1.LimitUnitDay)
	idx.Spec.Limits.GrabLimit = ptr.To[int32](1)
	var calls atomic.Int32
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	clock := t0
	s := &Service{
		Client: fakeClient(t, idx),
		Bus:    newTestBus(t),
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return countingFetcher{calls: &calls}, nil
		},
		Now: func() time.Time { return clock },
	}

	resp, result, _ := s.handle(ctx, grabRequest("a"))
	require.Empty(t, resp.Error)
	require.Equal(t, resultMagnet, result)
	require.EqualValues(t, 1, calls.Load())

	clock = t0.Add(time.Minute)
	resp, result, label := s.handle(ctx, grabRequest("b"))
	require.Equal(t, limits.ResultGrabLimited, result)
	require.Equal(t, "tr", label)
	retryAt, limited := limits.GrabLimited(resp.Error)
	require.True(t, limited, "the refusal must be recognisable: %q", resp.Error)
	require.Equal(t, t0.Add(24*time.Hour+time.Second), retryAt)
	require.EqualValues(t, 1, calls.Load(), "a grab over the limit must not reach the indexer")

	resp, _, _ = s.handle(ctx, grabRequest("a"))
	require.Empty(t, resp.Error, "the grab already in the window is the same grab")
	require.EqualValues(t, 2, calls.Load())

	clock = retryAt
	resp, _, _ = s.handle(ctx, grabRequest("b"))
	require.Empty(t, resp.Error, "the window has room again")
}

// A grab that did not happen gives its slot back: a failed fetch is not a
// grab against the limit.
func TestAFailedFetchGivesItsSlotBack(t *testing.T) {
	ctx := context.Background()
	idx := testIndexer("media", "tr", "uid-giveback", indexv1alpha1.LimitUnitDay)
	idx.Spec.Limits.GrabLimit = ptr.To[int32](1)
	var calls atomic.Int32
	failing := true
	s := &Service{
		Client: fakeClient(t, idx),
		Bus:    newTestBus(t),
		Fetch: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			if failing {
				return countingFetcher{calls: &calls, err: errors.New("connection reset")}, nil
			}
			return countingFetcher{calls: &calls}, nil
		},
	}

	resp, _, _ := s.handle(ctx, grabRequest("a"))
	require.NotEmpty(t, resp.Error)
	_, limited := limits.GrabLimited(resp.Error)
	require.False(t, limited)

	u, err := limits.Grabs(ctx, s.Bus.KV(events.BucketIndexerLimits), idx, time.Now())
	require.NoError(t, err)
	require.Zero(t, u.Count, "the failed grab kept its slot")

	failing = false
	resp, _, _ = s.handle(ctx, grabRequest("b"))
	require.Empty(t, resp.Error)
}
