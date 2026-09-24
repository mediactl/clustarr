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
	"fmt"
	"sync"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// QueryRPC is the release-index half of clustarr.rpc.indexarr.query this
// package depends on. indexarr (Phase D1) serves that subject with exactly
// schema.QueryRequest -> schema.QueryResponse, pinned in
// pkg/events/schema/index.go; this package adds no new payload type, only
// this narrow client interface plus a fake for tests -- the same shape
// app/catalog/worker/search's SearchRPC uses for clustarr.rpc.indexarr.search.
//
// app/indexer/query.Service.Handle "never returns an error, by design: after a
// successful decode every failure is a populated QueryResponse.Error", so the
// only error THIS interface can produce is the RPC transport's own --
// events.ErrNoResponders when indexarr is not up, or a context deadline.
type QueryRPC interface {
	Query(ctx context.Context, req schema.QueryRequest) (schema.QueryResponse, error)
}

type busQueryRPC struct{ r events.Requester }

// NewBusQueryRPC wraps a bus's request/reply half as a QueryRPC.
func NewBusQueryRPC(r events.Requester) QueryRPC { return &busQueryRPC{r: r} }

func (b *busQueryRPC) Query(ctx context.Context, req schema.QueryRequest) (schema.QueryResponse, error) {
	var resp schema.QueryResponse
	if err := b.r.Request(ctx, events.RPCIndexQuery, req, &resp); err != nil {
		return schema.QueryResponse{}, fmt.Errorf("query RPC: %w", err)
	}
	return resp, nil
}

// FakeQueryRPC is a QueryRPC test double: it returns Response/Err
// unconditionally and records every request it saw. Safe for concurrent use,
// mirroring app/catalog/worker/search's FakeSearchRPC.
type FakeQueryRPC struct {
	Response schema.QueryResponse
	Err      error

	mu       sync.Mutex
	requests []schema.QueryRequest
}

// Query implements QueryRPC.
func (f *FakeQueryRPC) Query(_ context.Context, req schema.QueryRequest) (schema.QueryResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.Err != nil {
		return schema.QueryResponse{}, f.Err
	}
	return f.Response, nil
}

// Requests returns a copy of every request the fake has seen, oldest first.
func (f *FakeQueryRPC) Requests() []schema.QueryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]schema.QueryRequest(nil), f.requests...)
}
