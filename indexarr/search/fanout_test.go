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
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	searchworker "github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// ------------------------------------------------------------- stubs ---

// stubClient is one indexer's wire behaviour: releases, an error, or a block
// until the test releases it or the context is done.
//
// The block is gated on a CHANNEL rather than on wall-clock so a test can
// separate "this indexer is still running" from "this goroutine has not been
// scheduled yet". started is closed on entry, so a test can prove the worker
// really ran before it asserts on a timeout.
type stubClient struct {
	releases []torznab.Release
	err      error
	started  chan struct{}
	release  chan struct{}
}

func (c stubClient) Search(ctx context.Context, _ torznab.Query) ([]torznab.Release, error) {
	if c.started != nil {
		close(c.started)
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.releases, nil
}

// blockedClient is a stub that never answers until its context is cancelled.
// The test owns the release channel and closes it on cleanup, so nothing is
// left blocked if an assertion fails early.
func blockedClient(t *testing.T) stubClient {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return stubClient{started: make(chan struct{}), release: release}
}

// stubClientFor dispatches on the Indexer's name, so one table can give each
// indexer a different personality.
func stubClientFor(byName map[string]stubClient) ClientFor {
	return func(_ context.Context, idx *indexv1alpha1.Indexer) (IndexerClient, error) {
		c, ok := byName[idx.Name]
		if !ok {
			return nil, fmt.Errorf("no stub for %s", idx.Name)
		}
		return c, nil
	}
}

func wireReleases(n int) []torznab.Release {
	out := make([]torznab.Release, n)
	for i := range out {
		out[i] = torznab.Release{
			Title:   "Some.Movie." + strconv.Itoa(2000+i) + ".1080p.BluRay.x264-GRP",
			GUID:    "guid-" + strconv.Itoa(i),
			Size:    int64(i+1) * 1024,
			PubDate: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		}
	}
	return out
}

// failingStore is a relindex.Store whose Upsert always fails. Everything else
// panics: nothing in the search path may call it.
type failingStore struct{ relindex.Store }

func (failingStore) Upsert(context.Context, []relindex.Release) (int, error) {
	return 0, errors.New("disk is full")
}

// countingStore records what it was asked to store and reports an insert for
// each row.
type countingStore struct {
	relindex.Store
	rows []relindex.Release
}

func (s *countingStore) Upsert(_ context.Context, rels []relindex.Release) (int, error) {
	s.rows = append(s.rows, rels...)
	return len(rels), nil
}

func newFakeClient(objs ...client.Object) client.Client {
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme())
	if len(objs) > 0 {
		b = b.WithObjects(objs...).WithStatusSubresource(objs...)
	}
	return b.Build()
}

// ------------------------------------------------------------ budgets ---

func TestFanoutBudget(t *testing.T) {
	// The shipped caller always sends 45000 and sets NO context deadline, so
	// the real outer bound is its consumer's AckWait: indexarr owns this.
	require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 45000}))
	require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{}))
	require.Equal(t, 8*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 10000}))
	require.Equal(t, time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 500}))
	require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: -1}))
	require.Equal(t, 43*time.Second, fanoutBudget(schema.SearchRequest{DeadlineMillis: 999999}),
		"a caller that asks for longer than 45s still gets 45s")
}

func TestIndexerDeadlineIsCappedByTheFanoutBudget(t *testing.T) {
	idx := &indexv1alpha1.Indexer{Spec: indexv1alpha1.IndexerSpec{
		Timeout: metav1.Duration{Duration: 30 * time.Second},
	}}
	require.Equal(t, 8*time.Second, indexerDeadline(idx, 8*time.Second))
	require.Equal(t, 30*time.Second, indexerDeadline(idx, 43*time.Second))
	// A typed client never gets spec.timeout's CRD default, so the zero it
	// always sends is floored here rather than becoming "no timeout".
	require.Equal(t, 30*time.Second, indexerDeadline(&indexv1alpha1.Indexer{}, 43*time.Second))
	require.Equal(t, time.Second, indexerDeadline(&indexv1alpha1.Indexer{}, time.Second))
}

func TestQueryLimitClampsToCaps(t *testing.T) {
	bare := &indexv1alpha1.Indexer{}
	require.Equal(t, 500, queryLimit(schema.SearchRequest{}, bare))
	require.Equal(t, 500, queryLimit(schema.SearchRequest{Limit: 9999}, bare))
	require.Equal(t, 20, queryLimit(schema.SearchRequest{Limit: 20}, bare))

	small := &indexv1alpha1.Indexer{Status: indexv1alpha1.IndexerStatus{
		Caps: &indexv1alpha1.Caps{LimitsMax: 100},
	}}
	require.Equal(t, 100, queryLimit(schema.SearchRequest{}, small))
	require.Equal(t, 20, queryLimit(schema.SearchRequest{Limit: 20}, small))
}

func TestLimitOf(t *testing.T) {
	require.Equal(t, schema.MaxSearchReleases, limitOf(schema.SearchRequest{}))
	require.Equal(t, schema.MaxSearchReleases, limitOf(schema.SearchRequest{Limit: 10000}))
	require.Equal(t, 7, limitOf(schema.SearchRequest{Limit: 7}))
}

// ------------------------------------------------------------ outcomes ---

// The caller DROPS a nameless outcome silently, so the name must exist
// before anything can fail -- an indexer whose client will not build would
// otherwise vanish from the operator's view entirely.
func TestNewOutcomeIsNamedBeforeAnythingCanFail(t *testing.T) {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{
		Namespace: "media", Name: "nzbgeek", UID: "uid-1",
	}}
	got := newOutcome(idx)
	require.Equal(t, schema.Ref{Namespace: "media", Name: "nzbgeek", UID: "uid-1"}, got.IndexerRef)
	require.Equal(t, "nzbgeek", got.IndexerName)
	// "timeout" is what is TRUE before a worker writes its slot: still
	// running when the reply had to be sent.
	require.Equal(t, schema.SearchOutcomeTimeout, got.Status)
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus schema.SearchOutcomeStatus
		wantMetric string
	}{
		{"deadline", context.DeadlineExceeded, schema.SearchOutcomeTimeout, metricTimeout},
		{"cancelled", context.Canceled, schema.SearchOutcomeTimeout, metricTimeout},
		{
			"wrapped deadline", fmt.Errorf("torznab: %w", context.DeadlineExceeded),
			schema.SearchOutcomeTimeout, metricTimeout,
		},
		{
			"request limit", &torznab.Error{Code: torznab.ErrRequestLimitReached},
			schema.SearchOutcomeError, metricRateLimited,
		},
		{
			"download limit", &torznab.Error{Code: torznab.ErrDownloadLimitReached},
			schema.SearchOutcomeError, metricRateLimited,
		},
		{
			"http 429", &torznab.Error{HTTPStatus: http.StatusTooManyRequests},
			schema.SearchOutcomeError, metricRateLimited,
		},
		{
			"other torznab error", &torznab.Error{Code: torznab.ErrIncorrectCredentials},
			schema.SearchOutcomeError, metricError,
		},
		{"plain", errors.New("boom"), schema.SearchOutcomeError, metricError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, metric, msg := classifyFailure(tt.err)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantMetric, metric)
			require.NotEmpty(t, msg)
			require.LessOrEqual(t, len(msg), maxOutcomeError)
		})
	}
}

// A message is bounded, and cut on a rune boundary: an invalid UTF-8 byte in
// a status string is rejected by the apiserver outright, which turns "the
// indexer sent a long error" into "this object's status cannot be written".
func TestClassifyFailureBoundsTheMessageOnARuneBoundary(t *testing.T) {
	_, _, msg := classifyFailure(errors.New(strings.Repeat("é", 1000)))
	require.LessOrEqual(t, len(msg), maxOutcomeError)
	require.True(t, isValidUTF8(msg))
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestCapOutcomes(t *testing.T) {
	in := make([]schema.SearchOutcome, 0, 150)
	for i := range 100 {
		in = append(in, schema.SearchOutcome{
			IndexerRef: schema.Ref{Name: "skipped-" + strconv.Itoa(i)},
			Status:     schema.SearchOutcomeSkipped,
		})
	}
	for i := range 50 {
		in = append(in, schema.SearchOutcome{
			IndexerRef: schema.Ref{Name: "queried-" + strconv.Itoa(i)},
			Status:     schema.SearchOutcomeOK,
		})
	}

	got := capOutcomes(in)
	require.Len(t, got, maxOutcomes)
	// Every indexer that was actually QUERIED survives; the skipped ones
	// fill what is left.
	queried := 0
	for _, o := range got {
		if o.Status == schema.SearchOutcomeOK {
			queried++
		}
	}
	require.Equal(t, 50, queried, "a queried indexer was pushed out by a skipped one")
	require.Len(t, capOutcomes(in[:10]), 10)
}

// The two constants describe the same limit from opposite ends of one RPC.
// This is a TEST-ONLY import of the caller: production code in indexarr must
// never import catalogarr.
func TestOutcomeCapMatchesTheCaller(t *testing.T) {
	require.Equal(t, searchworker.MaxIndexerOutcomes, maxOutcomes)
}

// --------------------------------------------------------------- fanout ---

func fanoutIndexers(names ...string) []indexv1alpha1.Indexer {
	out := make([]indexv1alpha1.Indexer, 0, len(names))
	for _, n := range names {
		idx := healthyIndexer(n)
		idx.UID = types.UID("uid-" + n)
		out = append(out, idx)
	}
	return out
}

func serviceFor(idxs []indexv1alpha1.Indexer, byName map[string]stubClient) *Service {
	objs := make([]client.Object, 0, len(idxs))
	for i := range idxs {
		objs = append(objs, &idxs[i])
	}
	return &Service{Client: newFakeClient(objs...), ClientFor: stubClientFor(byName)}
}

// Each indexer's own outcome, with NO wall-clock dependency: the budget is
// far longer than the stubs take, so fanOut returns the moment both workers
// finish. An earlier version of this test shared a 300ms budget with the
// straggler case below and hard-asserted "ok" for the fast indexer, which
// flaked roughly one run in ten under `-p 4 -race` -- the fast goroutine
// simply had not been scheduled inside the margin. A flaky test in the gate
// is worse than no test, because it trains people to re-run.
func TestFanOutReportsEachIndexersOwnOutcome(t *testing.T) {
	idxs := fanoutIndexers("fast", "broken")
	s := serviceFor(idxs, map[string]stubClient{
		"fast":   {releases: wireReleases(2)},
		"broken": {err: errors.New("tracker exploded")},
	})
	cands := selectCandidates(idxs, movieRequest(), torznab.ModeMovieSearch, selectNow)
	require.Len(t, cands, 2)

	outcomes, results := s.fanOut(context.Background(), cands, movieRequest(),
		torznab.ModeMovieSearch, 30*time.Second)
	require.Len(t, outcomes, 2)
	require.Len(t, results, 2)

	byName := map[string]schema.SearchOutcome{}
	for _, o := range outcomes {
		require.NotEmpty(t, o.IndexerRef.Name, "a nameless outcome is dropped silently by the caller")
		require.NotEmpty(t, o.IndexerName, "a nameless outcome is dropped silently by the caller")
		byName[o.IndexerRef.Name] = o
	}

	require.Equal(t, schema.SearchOutcomeOK, byName["fast"].Status)
	require.Equal(t, int32(2), byName["fast"].Releases)
	require.Len(t, results[0].Releases, 2)

	require.Equal(t, schema.SearchOutcomeError, byName["broken"].Status)
	require.Contains(t, byName["broken"].Error, "tracker exploded")
	require.Empty(t, results[1].Releases)
}

// G1-6: the outcome records which parameter set the actual query used, all
// the way through the real fan-out -- selectCandidates, resolveQuery,
// buildQuery and the goroutine that stamps the outcome fanOut hands back.
// Ids stay preferred wherever the indexer supports one, even when the
// request also carries a resolved-title fallback.
func TestFanOutRecordsQueryMode(t *testing.T) {
	t.Run("id wins when the indexer supports one, even with text also set", func(t *testing.T) {
		idxs := fanoutIndexers("a")
		s := serviceFor(idxs, map[string]stubClient{"a": {releases: wireReleases(1)}})
		req := movieRequest()
		req.Text = "Inception 2010"
		cands := selectCandidates(idxs, req, torznab.ModeMovieSearch, selectNow)
		require.Len(t, cands, 1)

		outcomes, _ := s.fanOut(context.Background(), cands, req, torznab.ModeMovieSearch, 2*time.Second)
		require.Len(t, outcomes, 1)
		require.Equal(t, schema.SearchOutcomeOK, outcomes[0].Status)
		require.Equal(t, schema.SearchQueryModeID, outcomes[0].QueryMode)
	})

	t.Run("text is the fallback when the indexer supports no id parameter", func(t *testing.T) {
		idx := healthyIndexer("b")
		idx.Status.Caps.Modes = map[string][]string{"movie": {"q"}}
		idxs := []indexv1alpha1.Indexer{idx}
		s := serviceFor(idxs, map[string]stubClient{"b": {releases: wireReleases(1)}})
		req := movieRequest()
		req.IDs = nil
		req.Text = "Inception 2010"
		cands := selectCandidates(idxs, req, torznab.ModeMovieSearch, selectNow)
		require.Len(t, cands, 1)
		require.Empty(t, cands[0].Skip, "a caps-advertised q parameter must not be skipped as unsupported")

		outcomes, _ := s.fanOut(context.Background(), cands, req, torznab.ModeMovieSearch, 2*time.Second)
		require.Len(t, outcomes, 1)
		require.Equal(t, schema.SearchOutcomeOK, outcomes[0].Status)
		require.Equal(t, schema.SearchQueryModeText, outcomes[0].QueryMode)
	})

	t.Run("no id and no title known: skipped, not a guessed query", func(t *testing.T) {
		idx := healthyIndexer("c")
		idx.Status.Caps.Modes = map[string][]string{"movie": {"q"}}
		idxs := []indexv1alpha1.Indexer{idx}
		s := serviceFor(idxs, map[string]stubClient{"c": {releases: wireReleases(1)}})
		req := movieRequest()
		req.IDs = nil // no text set either: nothing this indexer can be asked

		cands := selectCandidates(idxs, req, torznab.ModeMovieSearch, selectNow)
		require.Len(t, cands, 1)

		outcomes, _ := s.fanOut(context.Background(), cands, req, torznab.ModeMovieSearch, 2*time.Second)
		require.Len(t, outcomes, 1)
		require.Equal(t, schema.SearchOutcomeSkipped, outcomes[0].Status)
		require.Equal(t, skipNoIDParam, outcomes[0].Error)
		require.Empty(t, outcomes[0].QueryMode)
	})
}

// One slow indexer must not consume the budget: the reply goes out on time
// with the straggler reported as a NAMED timeout.
//
// Only the straggler is in this table, so there is nothing whose outcome
// depends on the scheduler beating a deadline. The started channel proves
// the worker really ran, so "timeout" is an observed outcome rather than a
// pre-named default nothing ever touched.
func TestFanOutRepliesOnTheBudgetWithAStragglerNamed(t *testing.T) {
	idxs := fanoutIndexers("slow")
	blocked := blockedClient(t)
	s := serviceFor(idxs, map[string]stubClient{"slow": blocked})
	cands := selectCandidates(idxs, movieRequest(), torznab.ModeMovieSearch, selectNow)

	const budget = 300 * time.Millisecond
	started := time.Now()
	outcomes, _ := s.fanOut(context.Background(), cands, movieRequest(),
		torznab.ModeMovieSearch, budget)
	elapsed := time.Since(started)

	select {
	case <-blocked.started:
	default:
		t.Fatal("the worker never reached the indexer, so the timeout proves nothing")
	}
	require.GreaterOrEqual(t, elapsed, budget, "the reply beat the budget it was given")
	require.Less(t, elapsed, 5*time.Second, "one blocked indexer held the reply")

	require.Len(t, outcomes, 1)
	require.Equal(t, schema.SearchOutcomeTimeout, outcomes[0].Status,
		"an indexer still running when the reply went out is a timeout")
	require.Equal(t, "slow", outcomes[0].IndexerRef.Name)
	require.Equal(t, "slow", outcomes[0].IndexerName)
}

// A skipped candidate is named and reported without ever being queried.
func TestFanOutReportsSkipsWithoutQuerying(t *testing.T) {
	idx := healthyIndexer("off")
	idx.Spec.Enabled = ptr.To(false)
	s := &Service{
		Client: newFakeClient(&idx),
		ClientFor: func(context.Context, *indexv1alpha1.Indexer) (IndexerClient, error) {
			t.Fatal("a skipped candidate must never build a client")
			return nil, nil
		},
	}
	cands := selectCandidates([]indexv1alpha1.Indexer{idx}, movieRequest(),
		torznab.ModeMovieSearch, selectNow)
	outcomes, _ := s.fanOut(context.Background(), cands, movieRequest(),
		torznab.ModeMovieSearch, time.Second)

	require.Len(t, outcomes, 1)
	require.Equal(t, schema.SearchOutcomeSkipped, outcomes[0].Status)
	require.Equal(t, skipDisabled, outcomes[0].Error)
	require.Equal(t, "off", outcomes[0].IndexerName)
}

// D8: the local index is a side effect that can never fail the search. A
// full disk must not turn a good answer into a nak storm.
func TestAFailingStoreStillYieldsAnOKOutcome(t *testing.T) {
	idx := healthyIndexer("fast")
	s := &Service{
		Client:    newFakeClient(&idx),
		Store:     failingStore{},
		ClientFor: stubClientFor(map[string]stubClient{"fast": {releases: wireReleases(3)}}),
	}
	cands := selectCandidates([]indexv1alpha1.Indexer{idx}, movieRequest(),
		torznab.ModeMovieSearch, selectNow)
	outcomes, results := s.fanOut(context.Background(), cands, movieRequest(),
		torznab.ModeMovieSearch, 2*time.Second)

	require.Len(t, outcomes, 1)
	require.Equal(t, schema.SearchOutcomeOK, outcomes[0].Status)
	require.Equal(t, int32(3), outcomes[0].Releases)
	require.Len(t, results[0].Releases, 3)
}

// The index write and the query read must use ONE normaliser or the index
// answers nothing -- silently, with an empty result set rather than an error.
// indexarr/query reads with release.TitleNorm, so the rows must be written
// with it.
func TestIndexRowsAreNormalisedWithTitleNorm(t *testing.T) {
	idx := healthyIndexer("fast")
	store := &countingStore{}
	s := &Service{
		Client:    newFakeClient(&idx),
		Store:     store,
		ClientFor: stubClientFor(map[string]stubClient{"fast": {releases: wireReleases(2)}}),
	}
	cands := selectCandidates([]indexv1alpha1.Indexer{idx}, movieRequest(),
		torznab.ModeMovieSearch, selectNow)
	_, _ = s.fanOut(context.Background(), cands, movieRequest(), torznab.ModeMovieSearch, 2*time.Second)

	require.Len(t, store.rows, 2)
	for _, row := range store.rows {
		require.Equal(t, "fast", row.Indexer)
		require.NotEmpty(t, row.GUID)
		require.NotEmpty(t, row.TitleNorm)
		require.Equal(t, release.TitleNorm(row.Title), row.TitleNorm)
		require.False(t, row.FetchedAt.IsZero(), "relindex refuses a zero FetchedAt")
		require.NotEmpty(t, row.InfoJSON)
		require.Empty(t, indexRejectReason(row))
	}
}

// ONE hostile row must not take the page down with it: relindex validates
// the whole batch before it opens its transaction and refuses all of it.
func TestAnUnstorableRowDoesNotCostThePage(t *testing.T) {
	idx := healthyIndexer("fast")
	store := &countingStore{}
	good := wireReleases(2)
	// A title with no ASCII alphanumerics normalises to "", which relindex
	// refuses; the release is still returned to the caller.
	junk := torznab.Release{Title: "???", GUID: "guid-junk"}

	s := &Service{
		Client:    newFakeClient(&idx),
		Store:     store,
		ClientFor: stubClientFor(map[string]stubClient{"fast": {releases: append(good, junk)}}),
	}
	cands := selectCandidates([]indexv1alpha1.Indexer{idx}, movieRequest(),
		torznab.ModeMovieSearch, selectNow)
	outcomes, results := s.fanOut(context.Background(), cands, movieRequest(),
		torznab.ModeMovieSearch, 2*time.Second)

	require.Equal(t, schema.SearchOutcomeOK, outcomes[0].Status)
	require.Len(t, results[0].Releases, 3, "the release is still answered to the caller")
	require.Len(t, store.rows, 2, "the storable rows survived the unstorable one")
}

// --------------------------------------------------------------- Search ---

// D9's success-shaped rows. Search NEVER returns an error and NEVER panics:
// an error reply would discard every outcome, and the outcomes are the
// operator's whole diagnosis.
func TestSearchShapes(t *testing.T) {
	t.Run("no indexer objects at all", func(t *testing.T) {
		s := &Service{Client: newFakeClient(), ClientFor: stubClientFor(nil)}
		resp := s.Search(context.Background(), movieRequest())
		require.Empty(t, resp.Releases)
		require.Empty(t, resp.Outcomes)
		require.False(t, resp.Truncated)
	})

	t.Run("one healthy indexer", func(t *testing.T) {
		idx := healthyIndexer("a")
		s := &Service{
			Client:    newFakeClient(&idx),
			ClientFor: stubClientFor(map[string]stubClient{"a": {releases: wireReleases(4)}}),
			Now:       func() time.Time { return selectNow },
		}
		resp := s.Search(context.Background(), movieRequest())
		require.Len(t, resp.Outcomes, 1)
		require.Equal(t, schema.SearchOutcomeOK, resp.Outcomes[0].Status)
		require.Len(t, resp.Releases, 4)
	})

	t.Run("one disabled indexer", func(t *testing.T) {
		idx := healthyIndexer("a")
		idx.Spec.Enabled = ptr.To(false)
		s := &Service{
			Client:    newFakeClient(&idx),
			ClientFor: stubClientFor(nil),
			Now:       func() time.Time { return selectNow },
		}
		resp := s.Search(context.Background(), movieRequest())
		require.Len(t, resp.Outcomes, 1)
		require.Equal(t, schema.SearchOutcomeSkipped, resp.Outcomes[0].Status)
		require.Equal(t, "a", resp.Outcomes[0].IndexerRef.Name)
		require.Empty(t, resp.Releases)
	})

	t.Run("every indexer failed", func(t *testing.T) {
		a, b := healthyIndexer("a"), healthyIndexer("b")
		s := &Service{
			Client: newFakeClient(&a, &b),
			ClientFor: stubClientFor(map[string]stubClient{
				"a": {err: errors.New("a exploded")},
				"b": {err: errors.New("b exploded")},
			}),
			Now: func() time.Time { return selectNow },
		}
		resp := s.Search(context.Background(), movieRequest())
		require.Len(t, resp.Outcomes, 2)
		for _, o := range resp.Outcomes {
			// A SUCCESS reply carrying error outcomes. An error reply would
			// make the caller return before it wrote any of them.
			require.Equal(t, schema.SearchOutcomeError, o.Status)
			require.NotEmpty(t, o.Error)
			require.NotEmpty(t, o.IndexerName)
		}
		require.Empty(t, resp.Releases)
	})
}

// The request's namespace scopes the list. Without it a Movie in one
// namespace is served by another namespace's indexer, with that indexer's
// credentials.
func TestSearchScopesToTheRequestNamespace(t *testing.T) {
	here := healthyIndexer("a")
	there := healthyIndexer("a")
	there.Namespace = "elsewhere"

	s := &Service{
		Client:    newFakeClient(&here, &there),
		ClientFor: stubClientFor(map[string]stubClient{"a": {releases: wireReleases(1)}}),
		Now:       func() time.Time { return selectNow },
	}

	req := movieRequest()
	req.Namespace = "media"
	resp := s.Search(context.Background(), req)
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, "media", resp.Outcomes[0].IndexerRef.Namespace)

	// No namespace at all still answers -- cluster-wide, with a warning --
	// so a producer older than the field keeps working.
	resp = s.Search(context.Background(), movieRequest())
	require.Len(t, resp.Outcomes, 2)
}

// discardLogger keeps the cluster-wide fallback's warning out of the test
// output without muting the code path that emits it.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRequestNamespace(t *testing.T) {
	log := discardLogger()
	require.Equal(t, "media", requestNamespace(schema.SearchRequest{Namespace: "media"}, log))
	require.Equal(t, "media", requestNamespace(schema.SearchRequest{
		IndexerRefs: []schema.Ref{{Namespace: "media", Name: "a"}, {Namespace: "media", Name: "b"}},
	}, log))
	require.Empty(t, requestNamespace(schema.SearchRequest{
		IndexerRefs: []schema.Ref{{Namespace: "media", Name: "a"}, {Namespace: "other", Name: "b"}},
	}, log))
	require.Empty(t, requestNamespace(schema.SearchRequest{
		IndexerRefs: []schema.Ref{{Name: "a"}},
	}, log))
	require.Empty(t, requestNamespace(schema.SearchRequest{}, log))
	// The explicit field wins over a disagreeing set of refs.
	require.Equal(t, "media", requestNamespace(schema.SearchRequest{
		Namespace:   "media",
		IndexerRefs: []schema.Ref{{Namespace: "other", Name: "a"}},
	}, log))
}

// Search is exported so D1-9's e2e and the contract tests can drive the
// fan-out without a bus, which means it can be reached on a Service that
// Serve never validated. A nil dependency must fail one search, not panic
// inside a worker goroutine and take the process with it.
func TestSearchOnAnIncompleteServiceFailsRatherThanPanics(t *testing.T) {
	resp := (&Service{}).Search(context.Background(), movieRequest())
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, ListOutcomeName, resp.Outcomes[0].IndexerName)
	require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)

	idx := healthyIndexer("a")
	resp = (&Service{Client: newFakeClient(&idx), Now: func() time.Time { return selectNow }}).
		Search(context.Background(), movieRequest())
	require.Len(t, resp.Outcomes, 1)
	require.Equal(t, schema.SearchOutcomeError, resp.Outcomes[0].Status)
	require.Contains(t, resp.Outcomes[0].Error, "client factory")
}
