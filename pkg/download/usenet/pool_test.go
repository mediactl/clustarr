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

package usenet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testConnectTimeout = 5 * time.Second
	testIOTimeout      = 10 * time.Second
)

func testPool(t *testing.T, depth int, providers ...Provider) *Pool {
	t.Helper()
	p, err := NewPool(providers, depth, testConnectTimeout, testIOTimeout, defaultMaxArticleBytes)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

// partPayload makes a deterministic, non-trivial part body. It deliberately
// contains bytes that yEnc must escape (NUL, CR, LF, '=') and a line that
// begins with '.', so the decode path exercises both escaping and NNTP
// dot-unstuffing rather than only plain ASCII.
func partPayload(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) ^ seed
	}
	copy(b, []byte("\r\n.=\x00 leading dot line\r\n"))
	return b
}

func TestFetchBatchFailsOverToTheSecondServerOn430(t *testing.T) {
	// The behaviour real-world completion rates hang on: an article one
	// provider answers 430 for is fetched from the next.
	//
	// The test is built so that neither "no failover at all" nor "always ask
	// the second server" can pass it: the primary is the ONLY server holding
	// four of the five articles, and the backup is the ONLY server holding
	// the fifth.
	primary := newStubServer(t)
	backup := newStubServer(t)

	const total = 5
	const gap = 2 // the article the primary refuses
	ids := make([]string, total)
	payloads := make([][]byte, total)
	for i := range total {
		ids[i] = fmt.Sprintf("seg%d@clustarr.test", i)
		payloads[i] = partPayload(byte(i), 700)
	}
	for i := range total {
		if i == gap {
			// Present on the backup only.
			backup.addArticle(ids[i], "movie.mkv", int64(total*700), int64(i*700), i+1, total, payloads[i])
			primary.refuse[ids[i]] = 430
			continue
		}
		primary.addArticle(ids[i], "movie.mkv", int64(total*700), int64(i*700), i+1, total, payloads[i])
	}

	pool := testPool(t, 4,
		primary.provider("primary", 4, 1),
		func() Provider { p := backup.provider("backup", 2, 1); p.Backup = true; return p }(),
	)

	res, err := pool.FetchBatch(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, res, total)

	for i, r := range res {
		require.NoErrorf(t, r.Err, "article %d should have been served by some provider", i)
		require.Equalf(t, payloads[i], r.Body, "article %d decoded wrong", i)
	}

	require.Equal(t, "backup", res[gap].Server,
		"the refused article must come from the backup -- that is the failover")
	for i, r := range res {
		if i == gap {
			continue
		}
		require.Equalf(t, "primary", r.Server,
			"article %d must be served by the primary, not by asking the backup for everything", i)
	}

	require.Equal(t, 1, backup.servedCount(ids[gap]))
	require.Equal(t, 0, backup.servedCount(ids[0]),
		"the backup must never be asked for an article the primary served")
	require.Equal(t, 0, primary.servedCount(ids[gap]))
}

func TestFetchBatchFailsOverWhenTheFirstServerRefusesTheConnection(t *testing.T) {
	// A 502 at greeting is the other failover: the whole batch moves rather
	// than one article.
	busy := newStubServer(t)
	busy.greetBusy = true
	good := newStubServer(t)

	ids := []string{"a@clustarr.test", "b@clustarr.test"}
	for i, id := range ids {
		good.addArticle(id, "movie.mkv", 1400, int64(i*700), i+1, 2, partPayload(byte(i), 700))
	}

	pool := testPool(t, 2,
		busy.provider("busy", 4, 1),
		good.provider("good", 4, 2),
	)

	res, err := pool.FetchBatch(context.Background(), ids)
	require.NoError(t, err)
	for _, r := range res {
		require.NoError(t, r.Err)
		require.Equal(t, "good", r.Server)
	}

	// And the refusal is remembered, so the next batch does not pay for it
	// again.
	require.True(t, pool.Stats()[0].Penalised, "a server that refused the connection must be penalised")
}

func TestFetchBatchReportsMissingWhenEveryServerRefuses(t *testing.T) {
	a := newStubServer(t)
	b := newStubServer(t)
	id := "gone@clustarr.test"
	a.refuse[id] = 430
	b.refuse[id] = 430

	pool := testPool(t, 2, a.provider("a", 2, 1), b.provider("b", 2, 2))

	r, err := pool.Fetch(context.Background(), id)
	require.NoError(t, err)
	require.ErrorIs(t, r.Err, ErrArticleMissing)
	require.Nil(t, r.Body)
}

func TestPoolNeverExceedsTheConnectionCap(t *testing.T) {
	// Providers enforce their plan's cap by disconnecting, so the cap has to
	// be real. The stub records the high-water mark of simultaneous
	// connections and this asserts against that, not against the pool's own
	// bookkeeping.
	const cap = 3
	const articles = 40

	srv := newStubServer(t)
	srv.bodyDelay = 5 * time.Millisecond

	ids := make([]string, articles)
	for i := range articles {
		ids[i] = fmt.Sprintf("cap%d@clustarr.test", i)
		srv.addArticle(ids[i], "movie.mkv", int64(articles*200), int64(i*200), i+1, articles, partPayload(byte(i), 200))
	}

	pool := testPool(t, 1, srv.provider("solo", cap, 1))

	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := range articles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := pool.Fetch(context.Background(), ids[i])
			if err != nil || r.Err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()

	require.Zero(t, failures.Load(), "every article should have been fetched")

	peak, accepted, _ := srv.stats()
	require.LessOrEqual(t, peak, cap,
		"the pool opened %d simultaneous connections against a cap of %d", peak, cap)
	require.Greater(t, peak, 1,
		"the test did not actually create concurrent demand, so the cap was never exercised")
	require.LessOrEqual(t, accepted, cap*2,
		"connections should be reused, not reopened per article (accepted %d)", accepted)
}

func TestPoolPipelinesWithinOneConnection(t *testing.T) {
	// Round-trip latency, not bandwidth, is the limit on a high-latency
	// provider. The stub notices a command that arrived before the previous
	// one was answered, which is pipelining observed rather than assumed.
	srv := newStubServer(t)
	const n = 8
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("pipe%d@clustarr.test", i)
		srv.addArticle(ids[i], "movie.mkv", int64(n*300), int64(i*300), i+1, n, partPayload(byte(i), 300))
	}

	pool := testPool(t, 4, srv.provider("solo", 1, 1))

	res, err := pool.FetchBatch(context.Background(), ids)
	require.NoError(t, err)
	for _, r := range res {
		require.NoError(t, r.Err)
	}

	_, accepted, pipelined := srv.stats()
	require.Equal(t, 1, accepted, "one batch should use one connection")
	require.True(t, pipelined,
		"no command reached the server before the previous response was written: the client is not pipelining")
}

func TestPoolSkipsAProviderWhoseQuotaIsSpent(t *testing.T) {
	spent := newStubServer(t)
	fresh := newStubServer(t)

	id := "quota@clustarr.test"
	payload := partPayload(9, 400)
	spent.addArticle(id, "movie.mkv", 400, 0, 1, 1, payload)
	fresh.addArticle(id, "movie.mkv", 400, 0, 1, 1, payload)

	p1 := spent.provider("spent", 2, 1)
	p1.QuotaBytes = 1 // a single article will blow through it
	pool := testPool(t, 1, p1, fresh.provider("fresh", 2, 2))

	// First fetch spends the quota on the primary.
	first, err := pool.Fetch(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, first.Err)
	require.Equal(t, "spent", first.Server)

	// Second fetch must skip it entirely.
	second, err := pool.Fetch(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, second.Err)
	require.Equal(t, "fresh", second.Server)
	require.Equal(t, 1, spent.servedCount(id), "an exhausted provider must not be asked again")

	stats := pool.Stats()
	require.True(t, stats[0].Exhausted)
	require.Greater(t, stats[0].UsedBytes, int64(0), "quota accounting must count what was actually read")
}

func TestFetchBatchRefusesAMessageIDThatWouldInjectACommand(t *testing.T) {
	// NZB segment ids are attacker-controlled input from an indexer. An id
	// carrying CRLF would otherwise put a second command on the wire.
	srv := newStubServer(t)
	good := "ok@clustarr.test"
	srv.addArticle(good, "movie.mkv", 300, 0, 1, 1, partPayload(1, 300))

	pool := testPool(t, 2, srv.provider("solo", 2, 1))

	res, err := pool.FetchBatch(context.Background(), []string{"evil\r\nQUIT", good, "<already@bracketed>"})
	require.NoError(t, err)

	require.Error(t, res[0].Err)
	require.NotErrorIs(t, res[0].Err, ErrArticleMissing,
		"an injection attempt is a protocol error, not a missing article")
	require.NoError(t, res[1].Err, "a valid id in the same batch must still be served")
	require.Error(t, res[2].Err)
}

func TestFetchBatchHonoursContextCancellation(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 500 * time.Millisecond
	ids := []string{"slow@clustarr.test"}
	srv.addArticle(ids[0], "movie.mkv", 300, 0, 1, 1, partPayload(3, 300))

	pool := testPool(t, 1, srv.provider("slow", 1, 1))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := pool.FetchBatch(ctx, ids)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"a cancelled fetch must stop, not block on the socket: got %v", err)
}

func TestPoolAuthenticates(t *testing.T) {
	srv := newStubServer(t)
	srv.requireAuth = true
	srv.user, srv.pass = "clustarr", "s3cret"

	id := "auth@clustarr.test"
	srv.addArticle(id, "movie.mkv", 300, 0, 1, 1, partPayload(4, 300))

	pool := testPool(t, 1, srv.provider("auth", 1, 1))
	r, err := pool.Fetch(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, r.Err)
}

func TestPoolReportsAuthFailureAndPenalisesTheProvider(t *testing.T) {
	srv := newStubServer(t)
	srv.requireAuth = true
	srv.user, srv.pass = "clustarr", "s3cret"

	p := srv.provider("auth", 1, 1)
	p.Password = "wrong"

	pool := testPool(t, 1, p)
	r, err := pool.Fetch(context.Background(), "whatever@clustarr.test")
	require.NoError(t, err)
	require.ErrorIs(t, r.Err, ErrArticleMissing)
	require.True(t, pool.Stats()[0].Penalised, "a rejected password must not be retried on a tight loop")
}

func TestExistsFindsMissingArticlesAcrossProviders(t *testing.T) {
	a := newStubServer(t)
	b := newStubServer(t)

	present := "here@clustarr.test"
	onlyOnB := "there@clustarr.test"
	gone := "nowhere@clustarr.test"

	a.addArticle(present, "movie.mkv", 300, 0, 1, 1, partPayload(5, 300))
	b.addArticle(onlyOnB, "movie.mkv", 300, 0, 1, 1, partPayload(6, 300))

	pool := testPool(t, 4, a.provider("a", 2, 1), b.provider("b", 2, 2))

	missing, err := pool.Exists(context.Background(), []string{present, onlyOnB, gone})
	require.NoError(t, err)
	require.Equal(t, []string{gone}, missing)
}
