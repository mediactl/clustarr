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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// noRespondersRetryAfter is how long the worker waits before asking again
// when nothing is serving clustarr.rpc.indexarr.search. It is short because
// the usual cause is a rolling restart of indexarr, and long enough that a
// full search backlog does not hammer the subject while indexarr is down or,
// before Phase D, not built at all.
const noRespondersRetryAfter = 15 * time.Second

// SearchRPC is the federated-search half of clustarr.rpc.indexarr.search that
// this package depends on. indexarr (Phase D) MUST serve that subject with
// exactly schema.SearchRequest -> schema.SearchResponse, already pinned in
// pkg/events/schema/index.go; this package adds no new payload type, only
// this narrow client interface plus a fake for tests.
type SearchRPC interface {
	Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error)
}

type busSearchRPC struct{ r events.Requester }

// NewBusSearchRPC wraps a bus's request/reply half as a SearchRPC. Spec §5:
// "micro, queue group indexarr, single reply at min(deadline, 45s)" --
// events.Requester.Request already implements the single-reply semantics, so
// this wrapper only translates events.ErrNoResponders (indexarr is down, or
// not built yet) into a retryable error instead of a hard failure, per §8.8's
// worker error handling. Everything else is returned verbatim so the
// subscription's own backoff schedule applies.
func NewBusSearchRPC(r events.Requester) SearchRPC { return &busSearchRPC{r: r} }

func (b *busSearchRPC) Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error) {
	var resp schema.SearchResponse
	if err := b.r.Request(ctx, events.RPCIndexSearch, req, &resp); err != nil {
		if errors.Is(err, events.ErrNoResponders) {
			return schema.SearchResponse{}, events.Retry(noRespondersRetryAfter, fmt.Errorf("search RPC: %w", err))
		}
		return schema.SearchResponse{}, fmt.Errorf("search RPC: %w", err)
	}
	return resp, nil
}

// FakeSearchRPC is a SearchRPC test double: it returns Response/Err
// unconditionally and records every request it saw. It is exported so the
// tasks that consume this package (the wanted cron, the delay/grab path) can
// reuse it instead of writing their own; it is safe for concurrent use
// because a worker under test may be driven from several goroutines.
type FakeSearchRPC struct {
	Response schema.SearchResponse
	Err      error

	mu       sync.Mutex
	requests []schema.SearchRequest
}

// Search implements SearchRPC.
func (f *FakeSearchRPC) Search(_ context.Context, req schema.SearchRequest) (schema.SearchResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.Err != nil {
		return schema.SearchResponse{}, f.Err
	}
	return f.Response, nil
}

// Requests returns a copy of every request the fake has seen, oldest first.
func (f *FakeSearchRPC) Requests() []schema.SearchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]schema.SearchRequest(nil), f.requests...)
}
