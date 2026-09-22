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
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/indexarr/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

const (
	// MaxRPCBudget is spec §5's "single reply at min(deadline, 45s)". It is
	// also natsbus' default request timeout, which bounds the caller's wait.
	MaxRPCBudget = 45 * time.Second

	// DefaultIndexerTimeout mirrors Indexer.spec.timeout's
	// +kubebuilder:default="30s". It is restated rather than relied on
	// because a typed Go client never gets that default: metav1.Duration is
	// a struct, `omitempty` does nothing to a struct field, so a typed
	// create always marshals "0s" and the apiserver has no absent field to
	// fill. A zero HTTP timeout means NO timeout, which has no reading
	// anyone wants, so it is floored.
	DefaultIndexerTimeout = 30 * time.Second

	// replyMargin is reserved out of the budget for merging, marshalling and
	// the per-indexer status applies, so the reply is written before the
	// caller's own clock runs out.
	replyMargin = 2 * time.Second

	// minFanoutBudget keeps a pathological deadline from producing a zero
	// budget, which would time out every indexer before it started.
	minFanoutBudget = time.Second

	// sideEffectTimeout bounds the work that happens AFTER an indexer
	// answered -- the local-index upsert and the status apply. It is
	// deliberately not the per-indexer timeout: that one is already expired
	// on the path that matters most, the indexer that timed out, and an
	// expired context would silently skip recording the failure that the
	// escalation ladder exists to react to.
	sideEffectTimeout = 10 * time.Second

	// maxOutcomeError bounds one failure message. catalogarr truncates at
	// the same 512 bytes; doing it here too keeps the reply small when fifty
	// indexers fail with verbose XML errors.
	maxOutcomeError = 512
)

// The metric outcome label vocabulary. Closed, five values, never derived
// from a torznab.Error.Description or any other indexer-supplied string.
const (
	metricOK          = "ok"
	metricError       = "error"
	metricTimeout     = "timeout"
	metricSkipped     = "skipped"
	metricRateLimited = "rate_limited"
)

// fanoutBudget is how long the whole fan-out may take.
//
// The shipped caller always sends 45000 and sets no context deadline of its
// own, so the real outer bound is its consumer's AckWait: indexarr owns this
// budget and must keep it.
func fanoutBudget(req schema.SearchRequest) time.Duration {
	b := MaxRPCBudget
	if req.DeadlineMillis > 0 {
		if d := time.Duration(req.DeadlineMillis) * time.Millisecond; d < b {
			b = d
		}
	}
	if b -= replyMargin; b < minFanoutBudget {
		b = minFanoutBudget
	}
	return b
}

// indexerDeadline divides the budget: each indexer gets its own spec.timeout,
// never more than the whole fan-out has left. One slow indexer therefore
// cannot consume the budget -- every other indexer's slot is already filled
// and the reply goes out on time with the straggler reported as a timeout.
func indexerDeadline(idx *indexv1alpha1.Indexer, budget time.Duration) time.Duration {
	t := idx.Spec.Timeout.Duration
	if t <= 0 {
		t = DefaultIndexerTimeout
	}
	return min(t, budget)
}

// queryLimit clamps the requested page size to what the indexer will accept.
func queryLimit(req schema.SearchRequest, idx *indexv1alpha1.Indexer) int {
	n := int(req.Limit)
	if n <= 0 || n > schema.MaxSearchReleases {
		n = schema.MaxSearchReleases
	}
	if caps := idx.Status.Caps; caps != nil && caps.LimitsMax > 0 && int(caps.LimitsMax) < n {
		n = int(caps.LimitsMax)
	}
	return n
}

// newOutcome names the outcome BEFORE the indexer is touched.
//
// The caller DROPS a nameless outcome silently -- status.indexerOutcomes is
// listType=map keyed by name, so a keyless entry would make the apiserver
// reject the whole status apply, and catalogarr filters them out rather than
// risk that. An indexer that fails while its client is being built would
// otherwise vanish from the operator's view entirely.
//
// Both IndexerRef.Name and IndexerName are set: the caller prefers the
// former and falls back to the latter.
//
// The default status is "timeout" because that is what is TRUE before a
// worker writes its slot: still running when the reply had to be sent.
func newOutcome(idx *indexv1alpha1.Indexer) schema.SearchOutcome {
	return schema.SearchOutcome{
		IndexerRef:  schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
		IndexerName: idx.Name,
		Status:      schema.SearchOutcomeTimeout,
	}
}

// classifyFailure maps one failure onto the wire status, the closed metric
// label and a bounded message.
func classifyFailure(err error) (schema.SearchOutcomeStatus, string, string) {
	msg := truncateUTF8(err.Error(), maxOutcomeError)
	var te *torznab.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return schema.SearchOutcomeTimeout, metricTimeout, msg
	case errors.As(err, &te) && (te.Code == torznab.ErrRequestLimitReached ||
		te.Code == torznab.ErrDownloadLimitReached ||
		te.HTTPStatus == http.StatusTooManyRequests):
		// Still an "error" on the wire: schema.SearchOutcomeStatus has no
		// rate-limited value and the payload is frozen. The distinction
		// survives in the metric, whose vocabulary is ours.
		return schema.SearchOutcomeError, metricRateLimited, msg
	default:
		return schema.SearchOutcomeError, metricError, msg
	}
}

// truncateUTF8 shortens s to at most maxBytes, backing up to a rune boundary
// so a multi-byte sequence is never cut in half -- an invalid UTF-8 byte in a
// status string is rejected by the apiserver outright.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// fanOut queries every non-skipped candidate in parallel and returns one
// outcome per candidate, in candidate order, plus each indexer's releases.
//
// It returns as soon as every worker has finished OR the budget expires,
// whichever comes first. Stragglers are NOT abandoned: their context is
// rooted at context.WithoutCancel(ctx), so returning from the RPC handler --
// which the bus cancels the moment the reply is written -- does not kill a
// status apply or a relindex upsert halfway through. They stay bounded by
// their own per-indexer deadline and by the service lifetime, through
// context.AfterFunc(s.srvCtx, ...).
func (s *Service) fanOut(
	ctx context.Context,
	cands []candidate,
	req schema.SearchRequest,
	mode torznab.SearchMode,
	budget time.Duration,
) ([]schema.SearchOutcome, []indexerResult) {
	outcomes := make([]schema.SearchOutcome, len(cands))
	results := make([]indexerResult, len(cands))
	for i, c := range cands {
		// Named FIRST, before anything can fail. See newOutcome.
		outcomes[i] = newOutcome(c.Indexer)
		results[i] = indexerResult{Name: c.Indexer.Name, Priority: c.Indexer.Spec.Priority}
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	work, cancelWork := context.WithCancel(context.WithoutCancel(ctx))
	stopAfter := func() bool { return false }
	if s.srvCtx != nil {
		stopAfter = context.AfterFunc(s.srvCtx, cancelWork)
	}

	for i, c := range cands {
		if c.Skip != "" {
			outcomes[i].Status = schema.SearchOutcomeSkipped
			outcomes[i].Error = c.Skip
			metrics.IndexerQueriesTotal.WithLabelValues(c.Indexer.Name, metricSkipped).Inc()
			continue
		}
		q, skip := resolveQuery(c, req, mode, queryLimit(req, c.Indexer))
		if skip != "" {
			outcomes[i].Status = schema.SearchOutcomeSkipped
			outcomes[i].Error = skip
			metrics.IndexerQueriesTotal.WithLabelValues(c.Indexer.Name, metricSkipped).Inc()
			continue
		}
		wg.Add(1)
		s.inflight.Add(1)
		go func(i int, idx *indexv1alpha1.Indexer, q torznab.Query) {
			defer wg.Done()
			defer s.inflight.Done()
			out, rels := s.queryOne(work, idx, q, indexerDeadline(idx, budget))
			mu.Lock()
			outcomes[i] = out
			results[i].Releases = rels
			mu.Unlock()
		}(i, c.Indexer, q)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		stopAfter()
		cancelWork()
		close(done)
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		logging.FromContext(ctx).Warn("indexarr/search: replying before every indexer finished",
			"budget", budget, "candidates", len(cands))
	}

	// Copy under the lock. A straggler keeps writing its slot after the
	// reply, and the copy is what makes that safe.
	mu.Lock()
	defer mu.Unlock()
	return append([]schema.SearchOutcome(nil), outcomes...), append([]indexerResult(nil), results...)
}

// queryOne runs one indexer end to end. It never returns an error: the
// outcome IS the error channel, because an error reply would discard every
// outcome (see Service.Search).
//
// ctx here is the fan-out's straggler-safe work context, NOT the handler's.
// The per-indexer timeout is applied inside, around the outbound call alone,
// so the status apply for an indexer that TIMED OUT still has a live context
// to run on -- recording that failure is the whole input to the escalation
// ladder, and an already-expired context would skip it silently.
func (s *Service) queryOne(
	ctx context.Context,
	idx *indexv1alpha1.Indexer,
	q torznab.Query,
	timeout time.Duration,
) (schema.SearchOutcome, []schema.Release) {
	out := newOutcome(idx)
	ctx, span := tracing.Start(ctx, "indexarr.search.indexer",
		trace.WithAttributes(
			attribute.String("indexer.name", idx.Name),
			attribute.String("indexer.namespace", idx.Namespace),
			attribute.String("search.mode", string(q.Type)),
		))
	defer span.End()

	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := s.now()
	cli, err := s.ClientFor(qctx, idx)
	if err != nil {
		// No request reached the indexer, so no query is counted -- but the
		// failure is still the indexer's configuration and still escalates.
		return s.failOutcome(ctx, out, started, nil, err), nil
	}

	// Counted BEFORE the request, because a request that times out still hit
	// the indexer and still spends its budget. countQuery is non-fatal: a
	// nil count means "leave status.queriesInWindow exactly as it is".
	queries := s.countQuery(qctx, idx)

	raw, err := cli.Search(qctx, q)
	elapsed := s.now().Sub(started)
	metrics.IndexerQueryDuration.WithLabelValues(idx.Name, string(q.Type)).Observe(elapsed.Seconds())
	if err != nil {
		return s.failOutcome(ctx, out, started, queries, err), nil
	}

	rels := s.project(idx, raw)

	// The side effects run on their own clock. qctx may be nearly spent by a
	// slow-but-successful indexer, and dropping the index write or the
	// status apply on that account would lose work the indexer already did.
	sideCtx, sideCancel := context.WithTimeout(ctx, sideEffectTimeout)
	defer sideCancel()

	inserted := s.index(sideCtx, idx, rels)
	metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, metricOK).Inc()
	metrics.IndexerReleasesReturned.WithLabelValues(idx.Name).Observe(float64(len(rels)))

	if err := s.recordOutcome(sideCtx, out.IndexerRef, true, "", queries, inserted); err != nil {
		logging.FromContext(ctx).Warn("indexarr/search: recording a successful query failed",
			"indexer", idx.Name, "err", err)
	}

	out.Status = schema.SearchOutcomeOK
	out.Releases = int32(len(rels))
	out.ElapsedMillis = elapsed.Milliseconds()
	span.SetAttributes(attribute.Int("search.releases", len(rels)))
	return out, rels
}

// failOutcome classifies one failure, counts it and records the escalation.
// It is the single place a failed query becomes both a metric and a status
// write, so the two can never disagree about what happened.
func (s *Service) failOutcome(
	ctx context.Context,
	out schema.SearchOutcome,
	started time.Time,
	queries *int32,
	err error,
) schema.SearchOutcome {
	status, metric, msg := classifyFailure(err)
	metrics.IndexerQueriesTotal.WithLabelValues(out.IndexerRef.Name, metric).Inc()

	sideCtx, cancel := context.WithTimeout(ctx, sideEffectTimeout)
	defer cancel()
	if rerr := s.recordOutcome(sideCtx, out.IndexerRef, false, msg, queries, 0); rerr != nil {
		logging.FromContext(ctx).Warn("indexarr/search: recording a failed query failed",
			"indexer", out.IndexerRef.Name, "err", rerr)
	}

	out.Status = status
	out.Error = msg
	out.ElapsedMillis = s.now().Sub(started).Milliseconds()
	return out
}

// project turns the wire releases into the firehose payload.
//
// rss.ProjectRelease is the ONE torznab.Release -> schema.Release projection
// in this repo (task D1-7 owns it). Building a second one here is the Phase C
// download-source defect repeated, where two tasks each built one mapping and
// the two disagreed on a field neither could change afterwards.
func (s *Service) project(idx *indexv1alpha1.Indexer, raw []torznab.Release) []schema.Release {
	protocol := idx.Status.Protocol
	if protocol == "" && idx.Spec.Generic != nil {
		// A generic upstream declares its protocol in the spec, so a search
		// that beats the first reconcile still ships a routable release
		// rather than one with an empty protocol.
		protocol = idx.Spec.Generic.Protocol
	}
	now := s.now()
	out := make([]schema.Release, 0, len(raw))
	for _, r := range raw {
		rel := rss.ProjectRelease(r, idx.Name, string(protocol))
		// ProjectRelease leaves FetchedAt zero on purpose: exactly one place
		// stamps it, and for a search that place is here.
		rel.FetchedAt = now
		out = append(out, rel)
	}
	return out
}

// index upserts everything the indexer returned into the local release
// index, so the SQLite store reflects what was seen and
// rpc.indexarr.query can replay it.
//
// A store failure is logged and swallowed: the indexer answered correctly,
// and turning a full disk into a failed search would nak the caller's task
// and re-run the whole fan-out.
func (s *Service) index(ctx context.Context, idx *indexv1alpha1.Indexer, rels []schema.Release) int64 {
	if s.Store == nil || len(rels) == 0 {
		return 0
	}
	log := logging.FromContext(ctx)
	rows := make([]relindex.Release, 0, len(rels))
	for _, r := range rels {
		info, err := json.Marshal(r)
		if err != nil {
			log.Warn("indexarr/search: skipping a release that will not encode",
				"indexer", idx.Name, "err", err)
			continue
		}
		row := relindex.Release{
			Indexer: idx.Name,
			GUID:    r.Info.GUID,
			Title:   r.Info.Title,
			// TitleNorm is release.CleanTitle, NOT release.Normalize:
			// relindex stores what it is given and escapes Query.Text
			// without normalising it, so the indexed column and the query
			// must go through ONE function or the index answers nothing --
			// silently, with an empty result set rather than an error.
			// indexarr/worker/rss writes the same function and
			// indexarr/query reads with it.
			TitleNorm:  release.CleanTitle(r.Info.Title),
			Group:      r.Info.ReleaseGroup,
			Protocol:   string(r.Info.Protocol),
			Categories: intsOf(r.Info.Categories),
			SizeBytes:  r.Info.SizeBytes,
			// nil stays nil. relindex.Upsert rejects a pointer to the zero
			// time outright: absence must stay absence rather than become a
			// date that sorts as ancient.
			PublishedAt: timePtr(r.Info.PublishedAt),
			FetchedAt:   r.FetchedAt,
			InfoJSON:    info,
		}
		if reason := indexRejectReason(row); reason != "" {
			// ONE hostile row must not take the page down with it:
			// relindex.Upsert validates the whole batch before it opens its
			// transaction and refuses all of it, so an untitled or
			// guid-less release would cost every good release beside it.
			metrics.IndexerReleasesDropped.WithLabelValues(idx.Name).Inc()
			log.Warn("indexarr/search: dropping a release the index cannot store",
				"indexer", idx.Name, "reason", reason)
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return 0
	}
	n, err := s.Store.Upsert(ctx, rows)
	if err != nil {
		log.Warn("indexarr/search: upserting the release index failed",
			"indexer", idx.Name, "rows", len(rows), "err", err)
		return 0
	}
	return int64(n)
}

// indexRejectReason names why relindex.Upsert would refuse row, or "" when
// it would accept it.
//
// It mirrors relindex's own validate (pkg/relindex/upsert.go), as
// indexarr/worker/rss's rejectReason does for the poll path. The mirror is
// held to the real store by TestIndexRejectReasonAgreesWithTheRealStore,
// which zeroes every field of a valid row in turn, rather than by this
// comment.
func indexRejectReason(row relindex.Release) string {
	switch {
	case row.Indexer == "":
		return "the indexer name is empty"
	case row.GUID == "":
		return "the indexer reported no guid"
	case row.TitleNorm == "":
		// release.CleanTitle strips everything outside [a-z0-9 ], so a title
		// made only of punctuation, symbols or non-Latin script normalises
		// to nothing and the row would be invisible to every text search.
		return "the title normalises to nothing, so the row would be unsearchable"
	case row.FetchedAt.IsZero():
		return "fetchedAt is the zero time"
	case row.PublishedAt != nil && row.PublishedAt.IsZero():
		return "publishedAt points at the zero time; absence must be nil"
	}
	return ""
}

// countQuery projects one query onto the ring, returning the window count to
// apply or nil to leave status.queriesInWindow alone.
//
// A nil Bus disables accounting rather than failing the search, exactly as
// the download verb's grab accounting does; Serve supplies the bus it was
// given, so a service built by run.go always has one.
func (s *Service) countQuery(ctx context.Context, idx *indexv1alpha1.Indexer) *int32 {
	if s.Bus == nil {
		return nil
	}
	n, err := countQuery(ctx, s.Bus.KV(events.BucketIndexerLimits), idx, s.now())
	if err != nil {
		logging.FromContext(ctx).Warn("indexarr/search: query accounting failed",
			"indexer", idx.Name, "err", err)
		return nil
	}
	return &n
}

// recordOutcome runs the escalation ladder and applies the result under
// k8s.ManagerIndexarrWorker.
//
// It re-reads the live Indexer first. A fan-out is an HTTP round trip per
// indexer -- seconds, not milliseconds -- and indexarr/status seeds the apply
// from the status it is handed and re-sends EVERY field this manager owns,
// so applying the snapshot the fan-out started from would roll back whatever
// else wrote under the shared manager in the meantime: the RSS poll's
// lastRssAt, the download verb's grabsInWindow, another indexer's... no, its
// own object's earlier query. disabledUntil is the one that turns a lost
// update into a correctness bug rather than a counter blip: WorkerFields
// emits it only when non-nil, so a stale nil snapshot does not roll it back,
// it CLEARS it -- silently re-enabling an indexer another writer had just
// put into backoff.
//
// That is a lost update rather than a server-side-apply release, which is
// why no "manager X released field Y" test can see it. One Get per outcome
// closes the window, as indexarr/worker/rss and indexarr/download do.
//
// queries is nil when accounting could not run; newlyIndexed is what
// relindex.Upsert actually inserted, so indexedReleases counts distinct
// releases seen rather than rows returned.
func (s *Service) recordOutcome(
	ctx context.Context,
	ref schema.Ref,
	ok bool,
	reason string,
	queries *int32,
	newlyIndexed int64,
) error {
	var live indexv1alpha1.Indexer
	key := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
	if err := s.Client.Get(ctx, key, &live); err != nil {
		return fmt.Errorf("indexarr/search: read indexer %s: %w", key, err)
	}

	now := s.now()
	var esc idxstatus.Escalation
	if ok {
		esc = idxstatus.RecordSuccess(live.Status, now)
	} else {
		esc = idxstatus.RecordFailure(live.Status, now, reason)
	}

	// ONE apply, through indexarr/status, so the indexarr-worker owned set
	// is declared in exactly one place (ruling R14). Patch seeds every field
	// this manager owns from the status it is handed; the mutate changes
	// only what this query moved. ApplyEscalation is the only thing that can
	// UNDO that seed -- a recovered indexer's cleared disabledUntil has to
	// remove the seeded value rather than carry it forward, which no
	// generated With* helper can express.
	return idxstatus.Patch(ctx, s.Client, k8s.ManagerIndexarrWorker, &live,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			if queries != nil {
				ac.WithQueriesInWindow(*queries)
			}
			if newlyIndexed > 0 {
				ac.WithIndexedReleases(live.Status.IndexedReleases + newlyIndexed)
			}
			idxstatus.ApplyEscalation(ac, esc, live.Status)
		})
}

// intsOf converts the wire's []int32 categories to the index's []int,
// preserving nil.
func intsOf(in []int32) []int {
	if len(in) == 0 {
		return nil
	}
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}

// timePtr unwraps a *metav1.Time, keeping nil as nil and refusing to hand
// the store a pointer to the zero time.
func timePtr(t *metav1.Time) *time.Time {
	if t == nil || t.Time.IsZero() {
		return nil
	}
	return ptr.To(t.Time)
}
