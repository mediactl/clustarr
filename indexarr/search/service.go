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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// IndexerClient is the one call the fan-out makes against a live indexer.
//
// It is an interface rather than *torznab.Client so the fan-out is testable
// without a network, and so a Cardigann-backed client drops in unchanged --
// which it does (ruling R5): indexarr/controller/indexer's Cardigann engine
// adapter satisfies this interface, so a definition-backed indexer inherits
// this package's dedupe, query-limit window and health/backoff instead of
// getting a parallel path that skips them. A tracker's search.error page
// reaches Search as an error (ruling R6), and is escalated like any other.
type IndexerClient interface {
	Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error)
}

// ClientFor returns the wire client for one Indexer, ALREADY carrying that
// host's injected ratelimit.Limiter and its timeout.
//
// In production it is indexarr/controller/indexer.ClientCache.For, and that
// is load-bearing rather than incidental: it shares the reconciler's own
// builders, so the caps probe (or Cardigann login) and every search use one
// construction. The proxy is the reason to care: spec.proxyRef is applied in
// that one builder, so the probe and the fan-out gain it together. A second
// copy here would let the probe honour the operator's proxy while every
// search bypassed it -- leaking the real IP to a private tracker while status
// reported the proxy healthy.
//
// This package never constructs a ratelimit.Limiter, never calls
// torznab.NewClient and never writes a limiter Config. The Limiter is built
// by indexarr/run.go and each host's Config is written by the Indexer
// reconciler alone, since it is the only reader of spec.requestDelay; two
// spellings of a host key are not "paced twice as fast", they are completely
// unpaced.
type ClientFor func(ctx context.Context, idx *indexv1alpha1.Indexer) (IndexerClient, error)

// DownloadFn and QueryFn are the other two verbs' bodies, supplied by
// indexarr/download and indexarr/query. A nil one answers with a populated
// Error field rather than a handler error, because those two payloads have an
// Error field and a transport error means something else entirely to their
// callers: "the request never reached indexarr".
type DownloadFn func(ctx context.Context, req schema.DownloadRequest) schema.DownloadResponse

// QueryFn is the rpc.indexarr.query body. See [DownloadFn].
type QueryFn func(ctx context.Context, req schema.QueryRequest) schema.QueryResponse

// Service answers indexarr's three RPC verbs.
type Service struct {
	// Client reads Indexer objects and writes their status. It is the
	// manager's cached client.
	Client client.Client

	// ClientFor builds the wire client for one Indexer.
	ClientFor ClientFor

	// Store is the local release index. A nil Store disables the
	// local-index side effect; the search still answers.
	Store relindex.Store

	// Bus carries the query ring in clustarr-indexer-limits. A nil Bus
	// disables query accounting rather than failing the search. Serve fills
	// it from the bus it registers on when it is nil, so a service built by
	// run.go always has one.
	Bus events.Bus

	// Download and Query are the other two verbs' bodies.
	Download DownloadFn
	Query    QueryFn

	// Now is the clock. nil means time.Now.
	Now func() time.Time

	// srvCtx is cancelled when stop runs. It bounds stragglers -- fan-out
	// workers still running after the reply went out -- so a shutdown does
	// not leave them holding the process open.
	srvCtx   context.Context
	stopOnce sync.Once

	// inflight counts stragglers, and mu/stopping gate every Add to it.
	//
	// The gate is not optional. Serve's own doc says it CANNOT deregister
	// the responders -- events.Requester.Serve has no unsubscribe -- so a
	// search can arrive while stop is already blocked in inflight.Wait().
	// sync.WaitGroup.Add PANICS when it takes the counter up from zero
	// concurrently with a Wait, and that panic is process-fatal, so the
	// window has to be closed rather than narrowed: a plain
	// `if s.srvCtx.Err() != nil` check before the Add still leaves the
	// cancel-between-check-and-Add interleaving open. Once stopping is set
	// under mu, no further Add can happen, and Wait runs only after that.
	mu       sync.Mutex
	stopping bool
	inflight sync.WaitGroup
}

// addInflight registers one straggler, or reports false when the service is
// stopping and the worker must not be launched at all. See Service.inflight.
func (s *Service) addInflight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.inflight.Add(1)
	return true
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ListOutcomeName is the reserved outcome name used when the failure is
// indexarr's own and no indexer can be named. It must be non-empty or the
// caller drops the entry silently (status.indexerOutcomes is listType=map
// keyed by name).
const ListOutcomeName = "indexarr"

// maxOutcomes bounds the reply at what the caller can actually persist.
// catalogarr drops everything past its own MaxIndexerOutcomes, first-wins, so
// THIS side chooses which hundred matter.
const maxOutcomes = 100

// Search is the body of clustarr.rpc.indexarr.search.
//
// It NEVER returns an error, by design: the caller returns immediately on an
// RPC error and therefore never writes status.indexerOutcomes, so an error
// reply would throw away the only record of WHY nothing was found. Every
// failure is a named outcome instead.
func (s *Service) Search(ctx context.Context, req schema.SearchRequest) schema.SearchResponse {
	ctx, span := tracing.Start(ctx, "indexarr.search.fanout")
	defer span.End()

	mode := modeFor(req.Kind)
	budget := fanoutBudget(req)
	log := logging.FromContext(ctx)

	if s.Client == nil {
		return schema.SearchResponse{Outcomes: []schema.SearchOutcome{{
			IndexerRef:  schema.Ref{Name: ListOutcomeName},
			IndexerName: ListOutcomeName,
			Status:      schema.SearchOutcomeError,
			Error:       "indexarr/search: no client is configured",
		}}}
	}

	var list indexv1alpha1.IndexerList
	var opts []client.ListOption
	if ns := requestNamespace(req, log); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.Client.List(ctx, &list, opts...); err != nil {
		// The only failure with no indexer to name. Reported as one outcome
		// under a reserved name rather than as an error, so the operator
		// still sees a reason.
		tracing.RecordError(span, err)
		return schema.SearchResponse{Outcomes: []schema.SearchOutcome{{
			IndexerRef:  schema.Ref{Name: ListOutcomeName},
			IndexerName: ListOutcomeName,
			Status:      schema.SearchOutcomeError,
			Error:       truncateUTF8(err.Error(), maxOutcomeError),
		}}}
	}

	cands := selectCandidates(list.Items, req, mode, s.now())
	outcomes, results := s.fanOut(ctx, cands, req, mode, budget)

	fetched := 0
	for _, r := range results {
		fetched += len(r.Releases)
	}
	rels, truncated := mergeReleases(results, limitOf(req))

	span.SetAttributes(
		attribute.Int("search.candidates", len(cands)),
		attribute.Int("search.releases", len(rels)),
		attribute.Bool("search.truncated", truncated),
	)
	// fetched alongside releases is the only visibility into the merge:
	// spec §6.2's "alsoOn" provenance -- which OTHER indexers offered a
	// release that was collapsed -- has no field on schema.Release or
	// commonv1.ReleaseInfo, and the payload is frozen. Carried item.
	log.Info("indexarr/search: replied",
		"kind", req.Kind, "candidates", len(cands), "fetched", fetched,
		"releases", len(rels), "truncated", truncated)
	return schema.SearchResponse{
		Releases:  rels,
		Outcomes:  capOutcomes(outcomes),
		Truncated: truncated,
	}
}

// limitOf clamps the requested reply size to [1, MaxSearchReleases].
func limitOf(req schema.SearchRequest) int {
	n := int(req.Limit)
	if n <= 0 || n > schema.MaxSearchReleases {
		return schema.MaxSearchReleases
	}
	return n
}

// requestNamespace scopes the Indexer list.
//
// schema.SearchRequest.Namespace is the authoritative answer and catalogarr
// always sets it. The IndexerRefs fallback covers a producer older than that
// field: the refs agree on a namespace for an interactive search, which is
// the only kind that populates them.
//
// An empty result means cluster-wide, which serves one namespace's media from
// another's indexer, with that indexer's credentials, counted against its
// limits. It is a fallback rather than a refusal so that an older producer
// keeps working, and it WARNS, because the payload's own doc comment promises
// the old behaviour stays observable rather than silent.
func requestNamespace(req schema.SearchRequest, log *slog.Logger) string {
	if req.Namespace != "" {
		return req.Namespace
	}
	ns := ""
	for _, r := range req.IndexerRefs {
		if r.Namespace == "" {
			ns = ""
			break
		}
		if ns == "" {
			ns = r.Namespace
		} else if ns != r.Namespace {
			ns = ""
			break
		}
	}
	if ns == "" {
		log.Warn("indexarr/search: request carries no namespace; listing indexers cluster-wide",
			"kind", req.Kind, "userInvoked", req.UserInvoked, "indexerRefs", len(req.IndexerRefs))
	}
	return ns
}

// capOutcomes bounds the reply at what the caller can persist.
//
// catalogarr drops everything past MaxIndexerOutcomes = 100, first-wins, so
// this side decides which hundred matter: the indexers that were actually
// queried come first and skipped ones fill what is left. A hundred "you did
// not ask for me" entries would otherwise push out every real result.
func capOutcomes(in []schema.SearchOutcome) []schema.SearchOutcome {
	if len(in) <= maxOutcomes {
		return in
	}
	out := make([]schema.SearchOutcome, 0, maxOutcomes)
	for _, o := range in {
		if o.Status != schema.SearchOutcomeSkipped && len(out) < maxOutcomes {
			out = append(out, o)
		}
	}
	for _, o := range in {
		if o.Status == schema.SearchOutcomeSkipped && len(out) < maxOutcomes {
			out = append(out, o)
		}
	}
	return out
}

// Serve registers all three RPC verbs under queue group "indexarr".
//
// run.go calls this once, from a k8s.EveryReplica runnable, so there is a
// window in which indexarr reports Ready while no responder is registered and
// a caller gets events.ErrNoResponders.
//
// That window is deliberately NOT closed with a fourth readiness gate, and
// the reason is not "§13 does not list one". A readiness gate CANNOT close it:
// this RPC does not travel through the Kubernetes Service. catalogarr reaches
// indexarr over NATS request/reply on the "indexarr" queue group, so whether
// the pod is in the Service's endpoints has no bearing on whether a responder
// exists. A gate would delay `kubectl rollout status` and prevent not one
// ErrNoResponders -- and with §3's Recreate strategy at one replica there is
// an unavoidable gap across every rollout regardless.
//
// It is handled at the layer that can handle it. catalogarr/worker/search's
// busSearchRPC turns events.ErrNoResponders into an events.Retry with a 15s
// delay (§8.8's worker error handling), which covers a rollout, a pod that is
// not scheduled and a NATS partition alike -- none of which readiness
// touches.
//
// stop drains in-flight fan-outs, including the stragglers that outlived
// their reply. It cannot deregister the responders: events.Requester.Serve
// has no unsubscribe and its handlers run until the bus is closed. It is
// idempotent.
func Serve(ctx context.Context, bus events.Bus, s *Service) (func(), error) {
	switch {
	case bus == nil:
		return nil, errors.New("indexarr/search: nil bus")
	case s == nil:
		return nil, errors.New("indexarr/search: nil Service")
	case s.Client == nil:
		return nil, errors.New("indexarr/search: nil Service.Client")
	case s.ClientFor == nil:
		return nil, errors.New("indexarr/search: nil Service.ClientFor")
	}
	if s.Bus == nil {
		// Query accounting needs a bus and the caller already handed us one.
		// Requiring it to be set twice is how it ends up set once.
		s.Bus = bus
	}
	srvCtx, cancel := context.WithCancel(ctx)
	s.srvCtx = srvCtx

	verbs := []struct {
		subject string
		span    string
		handler func(context.Context, []byte) ([]byte, error)
	}{
		{events.RPCIndexSearch, "indexarr.rpc.serve.search", s.handleSearch},
		{events.RPCIndexDownload, "indexarr.rpc.serve.download", s.handleDownload},
		{events.RPCIndexQuery, "indexarr.rpc.serve.query", s.handleQuery},
	}
	for _, v := range verbs {
		// The bus hands every inbound RPC a bare context, so without a span
		// started here every RPC-triggered indexer call would be an orphaned
		// root.
		if err := bus.Serve(v.subject, events.QueueGroupIndexarr,
			func(ctx context.Context, data []byte) ([]byte, error) {
				ctx, span := tracing.Start(ctx, v.span)
				defer span.End()
				out, err := v.handler(ctx, data)
				if err != nil {
					tracing.RecordError(span, err)
				}
				return out, err
			}); err != nil {
			cancel()
			return nil, fmt.Errorf("indexarr/search: serve %s: %w", v.subject, err)
		}
	}
	return func() {
		s.stopOnce.Do(func() {
			cancel()
			s.mu.Lock()
			s.stopping = true
			s.mu.Unlock()
			s.inflight.Wait()
		})
	}, nil
}

// handleSearch decodes and dispatches the search verb.
//
// A malformed request is the one case with no better answer than an error:
// the caller naks, its ladder runs out at MaxDeliver and the message lands in
// the DLQ, which is correct for an input that cannot be parsed. Everything
// after the decode is a named outcome.
func (s *Service) handleSearch(ctx context.Context, data []byte) ([]byte, error) {
	var req schema.SearchRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("indexarr/search: decode SearchRequest: %w", err)
	}
	if req.Kind == "" {
		return nil, errors.New("indexarr/search: SearchRequest.kind is required")
	}
	return json.Marshal(s.Search(ctx, req))
}

func (s *Service) handleDownload(ctx context.Context, data []byte) ([]byte, error) {
	var req schema.DownloadRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("indexarr/search: decode DownloadRequest: %w", err)
	}
	if s.Download == nil {
		return json.Marshal(schema.DownloadResponse{
			Error: "indexarr: download verb is not configured",
		})
	}
	return json.Marshal(s.Download(ctx, req))
}

func (s *Service) handleQuery(ctx context.Context, data []byte) ([]byte, error) {
	var req schema.QueryRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("indexarr/search: decode QueryRequest: %w", err)
	}
	if s.Query == nil {
		return json.Marshal(schema.QueryResponse{
			Error: "indexarr: query verb is not configured",
		})
	}
	return json.Marshal(s.Query(ctx, req))
}
