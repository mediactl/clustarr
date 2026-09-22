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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	searchworker "github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/indexarr/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// stub is one indexer's wire behaviour for the bus tests.
type stub struct {
	releases []torznab.Release
	block    bool
}

func (s stub) Search(ctx context.Context, _ torznab.Query) ([]torznab.Release, error) {
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.releases, nil
}

func stubClientFor(s stub) search.ClientFor {
	return func(context.Context, *indexv1alpha1.Indexer) (search.IndexerClient, error) {
		return s, nil
	}
}

func twoReleases() []torznab.Release {
	return []torznab.Release{
		{Title: "Inception.2010.1080p.BluRay.x264-GRP", GUID: "g1", Size: 1 << 30},
		{Title: "Inception.2010.2160p.UHD.BluRay.x265-GRP", GUID: "g2", Size: 4 << 30},
	}
}

func usenetIndexer() *indexv1alpha1.Indexer {
	return &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nzbgeek", UID: "uid-nzbgeek"},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://example.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolUsenet},
		},
		Status: indexv1alpha1.IndexerStatus{
			Protocol: commonv1.ProtocolUsenet,
			Caps: &indexv1alpha1.Caps{
				Modes:      map[string][]string{"movie": {"imdbid", "q", "tmdbid"}},
				Categories: []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}},
			},
		},
	}
}

func newBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus
}

func fakeClientWith(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(objs...).WithStatusSubresource(objs...).Build()
}

// The whole anti-mismatch guarantee of this task, in one test: the SHIPPED
// caller's own client and request builder, over a real bus, against this
// server. Nothing here constructs a schema.SearchRequest literal -- if it
// did, a divergence between catalogarr's request shape and this server's
// expectations would pass here and fail in production.
func TestServeAnswersTheShippedCaller(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	svc := &search.Service{
		Client:    fakeClientWith(idx),
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)
	t.Cleanup(stop)

	rpc := searchworker.NewBusSearchRPC(bus)
	req := searchworker.BuildSearchRequest(
		"media",
		commonv1.MediaKindMovie,
		searchworker.TargetIDs{TmdbID: 27205, ImdbID: "tt1375666", Year: 2010},
		schema.MaxSearchReleases, false, nil, nil)

	require.Equal(t, int64(45000), req.DeadlineMillis)
	require.Empty(t, req.Text, "the caller is ids-only; the t=search fallback is unreachable")
	require.Equal(t, "media", req.Namespace)

	resp, err := rpc.Search(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Releases, 2)
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status)
	require.Equal(t, "nzbgeek", resp.Outcomes[0].IndexerRef.Name)
	require.Equal(t, "media", resp.Outcomes[0].IndexerRef.Namespace)
	require.NotEmpty(t, resp.Outcomes[0].IndexerName,
		"a nameless outcome is dropped silently by the caller")
	require.Equal(t, int32(2), resp.Outcomes[0].Releases)
	require.False(t, resp.Truncated)

	// The projection is rss.ProjectRelease's, not a second one: it carries
	// the indexer's own protocol and the object's name as the ref.
	require.Equal(t, commonv1.ProtocolUsenet, resp.Releases[0].Info.Protocol)
	require.Equal(t, "nzbgeek", resp.Releases[0].Info.IndexerRef)
	require.NotEmpty(t, resp.Releases[0].ParsedTitle)
}

// What the cluster does before run.go wires Serve, executably: the caller
// sees ErrNoResponders and has already wrapped it as a retry, so a search
// task backs off for 15s instead of dead-lettering.
func TestWithoutServeTheCallerSeesNoResponders(t *testing.T) {
	bus := newBus(t)
	req := searchworker.BuildSearchRequest("media", commonv1.MediaKindMovie,
		searchworker.TargetIDs{TmdbID: 27205}, 10, false, nil, nil)

	_, err := searchworker.NewBusSearchRPC(bus).Search(t.Context(), req)
	require.Error(t, err)
	require.ErrorIs(t, err, events.ErrNoResponders)

	var retry *events.RetryError
	require.ErrorAs(t, err, &retry, "the caller must back off rather than fail the task")
	require.Positive(t, retry.After)
}

// A blocked indexer must not hold the reply past the caller's deadline: the
// single reply goes out on the budget with the straggler reported as a
// named timeout.
func TestServeRepliesOnTheDeadlineWithANamedTimeout(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	svc := &search.Service{
		Client:    fakeClientWith(idx),
		ClientFor: stubClientFor(stub{block: true}),
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)
	t.Cleanup(stop)

	req := searchworker.BuildSearchRequest("media", commonv1.MediaKindMovie,
		searchworker.TargetIDs{TmdbID: 27205}, 10, false, nil, nil)
	req.DeadlineMillis = 3000

	started := time.Now()
	resp, err := searchworker.NewBusSearchRPC(bus).Search(ctx, req)
	elapsed := time.Since(started)

	require.NoError(t, err)
	require.Less(t, elapsed, 3*time.Second, "the reply outlived the caller's deadline")
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeTimeout, resp.Outcomes[0].Status)
	require.Equal(t, "nzbgeek", resp.Outcomes[0].IndexerRef.Name)
	require.NotEmpty(t, resp.Outcomes[0].IndexerName)
	require.Empty(t, resp.Releases)
}

// A request that cannot be decoded is the ONE case answered with a handler
// error: the caller naks and the message dead-letters, which is right for an
// input that is not a SearchRequest at all.
func TestAMalformedRequestIsATransportError(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	svc := &search.Service{
		Client:    fakeClientWith(idx),
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)
	t.Cleanup(stop)

	var resp schema.SearchResponse
	// A well-formed envelope with no kind: nothing can be searched for.
	err = bus.Request(ctx, events.RPCIndexSearch, schema.SearchRequest{}, &resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "kind")
}

// The other two verbs answer with a populated Error rather than a transport
// error when their bodies are not wired: "the fetch failed" and "the request
// never reached indexarr" mean different things to grabarr.
func TestUnconfiguredVerbsAnswerWithAnError(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	svc := &search.Service{
		Client:    fakeClientWith(idx),
		ClientFor: stubClientFor(stub{}),
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)
	t.Cleanup(stop)

	var dl schema.DownloadResponse
	require.NoError(t, bus.Request(ctx, events.RPCIndexDownload,
		schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "nzbgeek"}, GUID: "g"}, &dl))
	require.Contains(t, dl.Error, "not configured")

	var q schema.QueryResponse
	require.NoError(t, bus.Request(ctx, events.RPCIndexQuery, schema.QueryRequest{Text: "x"}, &q))
	require.Contains(t, q.Error, "not configured")
}

// ...and dispatch to them when they ARE wired, on the subjects run.go will
// hand them to.
func TestServeDispatchesTheOtherTwoVerbs(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	svc := &search.Service{
		Client:    fakeClientWith(idx),
		ClientFor: stubClientFor(stub{}),
		Download: func(_ context.Context, req schema.DownloadRequest) schema.DownloadResponse {
			return schema.DownloadResponse{MagnetURL: "magnet:?xt=" + req.GUID}
		},
		Query: func(_ context.Context, req schema.QueryRequest) schema.QueryResponse {
			return schema.QueryResponse{Total: int64(len(req.Text))}
		},
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)
	t.Cleanup(stop)

	var dl schema.DownloadResponse
	require.NoError(t, bus.Request(ctx, events.RPCIndexDownload,
		schema.DownloadRequest{IndexerRef: schema.Ref{Namespace: "media", Name: "nzbgeek"}, GUID: "abc"}, &dl))
	require.Equal(t, "magnet:?xt=abc", dl.MagnetURL)

	var q schema.QueryResponse
	require.NoError(t, bus.Request(ctx, events.RPCIndexQuery, schema.QueryRequest{Text: "hello"}, &q))
	require.Equal(t, int64(5), q.Total)
}

func TestServeRefusesAnIncompleteService(t *testing.T) {
	bus := newBus(t)
	idx := usenetIndexer()

	_, err := search.Serve(t.Context(), bus, nil)
	require.Error(t, err)

	_, err = search.Serve(t.Context(), nil, &search.Service{})
	require.Error(t, err)

	_, err = search.Serve(t.Context(), bus, &search.Service{ClientFor: stubClientFor(stub{})})
	require.ErrorContains(t, err, "Client")

	_, err = search.Serve(t.Context(), bus, &search.Service{Client: fakeClientWith(idx)})
	require.ErrorContains(t, err, "ClientFor")
}

// stop drains the stragglers and is idempotent. It cannot deregister the
// responder -- events.Requester.Serve has no unsubscribe -- so it is only
// ever about the in-flight work.
func TestStopIsIdempotentAndDrains(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	svc := &search.Service{
		Client:    fakeClientWith(idx),
		ClientFor: stubClientFor(stub{block: true}),
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)

	req := searchworker.BuildSearchRequest("media", commonv1.MediaKindMovie,
		searchworker.TargetIDs{TmdbID: 27205}, 10, false, nil, nil)
	req.DeadlineMillis = 1200
	_, err = searchworker.NewBusSearchRPC(bus).Search(ctx, req)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		stop()
		stop() // idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not drain the straggler")
	}
}

// hangingGetClient blocks every Get on its context. That is exactly where a
// straggler sits after the reply has gone out: its status apply re-reads the
// Indexer before applying.
type hangingGetClient struct {
	client.Client
	reached chan struct{}
	once    sync.Once
}

func (h *hangingGetClient) Get(
	ctx context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption,
) error {
	h.once.Do(func() { close(h.reached) })
	<-ctx.Done()
	return ctx.Err()
}

// stop must CANCEL the stragglers, not merely wait for them.
//
// This is the regression test for rooting the straggler bound in
// context.AfterFunc(s.srvCtx, cancelWork) rather than in
// `defer context.AfterFunc(...)()`. The deferred form deregisters the hook
// the instant fanOut returns -- which is precisely when the stragglers it
// exists to bound are still running -- so cancelling the service would reach
// nothing and every straggler would hold the drain open for its full
// sideEffectTimeout against an apiserver that may already be gone.
//
// TestStopIsIdempotentAndDrains cannot see this: there the straggler is
// bounded by its own per-indexer deadline, which expires long before the
// drain allowance either way. Here the straggler is blocked in its status
// apply, which only the service context can end.
func TestStopCancelsAStragglerRatherThanWaitingForIt(t *testing.T) {
	ctx := t.Context()
	bus := newBus(t)
	idx := usenetIndexer()

	hang := &hangingGetClient{Client: fakeClientWith(idx), reached: make(chan struct{})}
	svc := &search.Service{
		Client:    hang,
		ClientFor: stubClientFor(stub{releases: twoReleases()}),
	}
	stop, err := search.Serve(ctx, bus, svc)
	require.NoError(t, err)

	req := searchworker.BuildSearchRequest("media", commonv1.MediaKindMovie,
		searchworker.TargetIDs{TmdbID: 27205}, 10, false, nil, nil)
	req.DeadlineMillis = 1200
	resp, err := searchworker.NewBusSearchRPC(bus).Search(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Outcomes, 1)

	select {
	case <-hang.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the straggler never reached its status apply, so the drain proves nothing")
	}

	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("stop did not cancel the straggler; it waited out its own side-effect timeout")
	}
}
