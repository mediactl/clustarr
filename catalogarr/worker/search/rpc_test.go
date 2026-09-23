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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestBusSearchRPCRoundTrips(t *testing.T) {
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var seen schema.SearchRequest
	require.NoError(t, bus.Serve(events.RPCIndexSearch, "indexarr", func(_ context.Context, data []byte) ([]byte, error) {
		if err := json.Unmarshal(data, &seen); err != nil {
			return nil, err
		}
		return json.Marshal(schema.SearchResponse{Releases: []schema.Release{{ParsedTitle: "The Matrix"}}})
	}))

	resp, err := NewBusSearchRPC(bus).Search(ctx, schema.SearchRequest{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	require.Len(t, resp.Releases, 1)
	require.Equal(t, "The Matrix", resp.Releases[0].ParsedTitle)
	require.Equal(t, commonv1.MediaKindMovie, seen.Kind)
}

func TestBusSearchRPCNoRespondersIsRetryable(t *testing.T) {
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := NewBusSearchRPC(bus).Search(ctx, schema.SearchRequest{Kind: commonv1.MediaKindMovie})
	require.Error(t, err)

	var re *events.RetryError
	require.ErrorAs(t, err, &re,
		"a missing indexarr must nak with a delay, not dead-letter the task")
	require.Equal(t, noRespondersRetryAfter, re.After)
	require.ErrorIs(t, err, events.ErrNoResponders, "the cause must survive the wrapping")
}

// deadlineRequester records the deadline the request context carried and
// then blocks until that context ends, as a hung indexarr would.
type deadlineRequester struct {
	deadline time.Time
	has      bool
}

func (d *deadlineRequester) Request(ctx context.Context, _ string, _, _ any) error {
	d.deadline, d.has = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}

func (*deadlineRequester) Serve(string, string, func(context.Context, []byte) ([]byte, error)) error {
	return errors.New("deadlineRequester serves nothing")
}

// TestBusSearchRPCHonoursDeadlineMillis pins the carried D1 defect: the RPC
// advertised DeadlineMillis to indexarr but never bounded its own wait, so a
// hung reply was bounded only by whatever the transport or the consumer's
// AckWait happened to allow.
func TestBusSearchRPCHonoursDeadlineMillis(t *testing.T) {
	t.Run("the wait is bounded by the request's own deadline", func(t *testing.T) {
		req := &deadlineRequester{}
		// A parent context far longer than the advertised deadline, as the
		// consumer's AckWait is.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		start := time.Now()
		_, err := NewBusSearchRPC(req).Search(ctx, schema.SearchRequest{
			Kind: commonv1.MediaKindMovie, DeadlineMillis: 50,
		})
		elapsed := time.Since(start)

		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.True(t, req.has, "the request context must carry a deadline")
		require.WithinDuration(t, start.Add(50*time.Millisecond), req.deadline, 40*time.Millisecond,
			"the deadline must be DeadlineMillis from the call, not the parent's minute")
		require.Less(t, elapsed, 5*time.Second)
	})

	t.Run("a shorter deadline already on the context wins", func(t *testing.T) {
		req := &deadlineRequester{}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		parent, _ := ctx.Deadline()

		_, err := NewBusSearchRPC(req).Search(ctx, schema.SearchRequest{
			Kind: commonv1.MediaKindMovie, DeadlineMillis: SearchDeadline.Milliseconds(),
		})
		require.Error(t, err)
		require.Equal(t, parent, req.deadline)
	})

	t.Run("no advertised deadline leaves the context alone", func(t *testing.T) {
		req := &deadlineRequester{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = NewBusSearchRPC(req).Search(ctx, schema.SearchRequest{Kind: commonv1.MediaKindMovie})
		require.False(t, req.has)
	})
}

func TestFakeSearchRPCRecordsRequestsAndReturnsCannedResponse(t *testing.T) {
	fake := &FakeSearchRPC{Response: schema.SearchResponse{Releases: []schema.Release{{ParsedTitle: "canned"}}}}

	resp, err := fake.Search(context.Background(), schema.SearchRequest{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	require.Len(t, resp.Releases, 1)
	require.Equal(t, "canned", resp.Releases[0].ParsedTitle)
	require.Len(t, fake.Requests(), 1)
	require.Equal(t, commonv1.MediaKindMovie, fake.Requests()[0].Kind)

	fake.Err = errors.New("boom")
	_, err = fake.Search(context.Background(), schema.SearchRequest{Kind: commonv1.MediaKindEpisode})
	require.EqualError(t, err, "boom")
	require.Len(t, fake.Requests(), 2, "a failing call is still recorded")
}
