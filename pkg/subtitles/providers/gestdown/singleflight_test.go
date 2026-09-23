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

package gestdown_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
)

// gatedServer serves the two-step flow, but holds every show lookup until
// release is called, so a test can pile concurrent searches up behind one
// cold-cache lookup. arrived receives once per show lookup that reaches the
// handler. release is idempotent and also runs at cleanup, before the
// server closes, so a test that fails early never hangs in srv.Close on a
// handler still held open.
func gatedServer(t *testing.T, showStatus int, showsBody []byte) (srv *httptest.Server, lookups, searches *atomic.Int32, arrived chan struct{}, release func()) {
	t.Helper()
	lookups, searches = &atomic.Int32{}, &atomic.Int32{}
	arrived = make(chan struct{}, 64)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	searchBody := readFixture(t, "search.json")
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/shows/external/tvdb/"):
			lookups.Add(1)
			arrived <- struct{}{}
			<-gate
			w.WriteHeader(showStatus)
			_, _ = w.Write(showsBody)
		case strings.HasPrefix(r.URL.Path, "/subtitles/get/"):
			searches.Add(1)
			_, _ = w.Write(searchBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(release) // LIFO: runs before srv.Close
	return srv, lookups, searches, arrived, release
}

// awaitArrival waits for the next show lookup to reach the server, failing
// the test instead of hanging when none comes.
func awaitArrival(t *testing.T, arrived <-chan struct{}) {
	t.Helper()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("no show lookup reached the server")
	}
}

// awaitErr receives from ch, failing the test instead of hanging.
func awaitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("search did not return")
		return nil
	}
}

// searchConcurrently starts n searches for one show at once and returns
// their errors once all have finished. The server's release must be called
// for any of them to finish.
func searchConcurrently(p *gestdown.Provider, n int) (wait func() []error) {
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = p.Search(context.Background(), subtitles.Query{
				Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: i + 1,
				Languages: []subtitles.LangKey{"en"},
			})
		}()
	}
	close(start)
	return func() []error { wg.Wait(); return errs }
}

// TestConcurrentColdCacheSearchesShareOneShowLookup is the carried item
// "Gestdown show-id cache single-flight": N searches for episodes of one
// show on a cold cache must cost one show lookup, not N.
//
// The server holds the first lookup open, and the test gives the other
// searches a window to reach the handler before releasing it. Without
// single-flight every one of them misses the cache and issues its own
// lookup inside that window; with it they wait on the first.
func TestConcurrentColdCacheSearchesShareOneShowLookup(t *testing.T) {
	const n = 8
	srv, lookups, searches, arrived, release := gatedServer(t, http.StatusOK, readFixture(t, "shows.json"))
	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})

	wait := searchConcurrently(p, n)
	awaitArrival(t, arrived)
	time.Sleep(200 * time.Millisecond) // the window a duplicate lookup would arrive in
	release()

	for _, err := range wait() {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), lookups.Load(), "concurrent searches for one show must share a single show-id lookup")
	assert.Equal(t, int32(n), searches.Load(), "every search still runs its own subtitle query")
}

// TestAFailedSharedLookupReachesEveryWaiterAndIsNotCached: the waiters get
// the leader's answer, including a failure, and a failure is not cached --
// the next search looks the show up again.
func TestAFailedSharedLookupReachesEveryWaiterAndIsNotCached(t *testing.T) {
	const n = 4
	srv, lookups, _, arrived, release := gatedServer(t, http.StatusNotFound, []byte(`"Couldn't find show: 81189"`))
	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})

	wait := searchConcurrently(p, n)
	awaitArrival(t, arrived)
	time.Sleep(200 * time.Millisecond)
	release()

	for _, err := range wait() {
		require.Error(t, err)
		assert.True(t, subtitles.IsNotFound(err), "every waiter sees the shared lookup's NotFound: %v", err)
	}
	assert.Equal(t, int32(1), lookups.Load())

	_, err := p.Search(context.Background(), subtitles.Query{
		Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1, Languages: []subtitles.LangKey{"en"},
	})
	require.Error(t, err)
	assert.Equal(t, int32(2), lookups.Load(), "a failed lookup must not be cached")
}

// TestAWaiterIsBoundedByItsOwnContext: a search waiting on another caller's
// lookup returns when its own context ends, without waiting for the lookup
// and without disturbing it.
func TestAWaiterIsBoundedByItsOwnContext(t *testing.T) {
	srv, lookups, _, arrived, release := gatedServer(t, http.StatusOK, readFixture(t, "shows.json"))
	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	q := subtitles.Query{Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1, Languages: []subtitles.LangKey{"en"}}

	leaderDone := make(chan error, 1)
	go func() {
		_, err := p.Search(context.Background(), q)
		leaderDone <- err
	}()
	awaitArrival(t, arrived)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Search(ctx, q)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	release()
	require.NoError(t, awaitErr(t, leaderDone))
	assert.Equal(t, int32(1), lookups.Load())
}

// TestAWaiterRetriesWhenOnlyTheLeadersContextEnded: the leader's
// cancellation is not the waiter's; a waiter whose own context is live
// runs its own lookup instead of inheriting context.Canceled.
func TestAWaiterRetriesWhenOnlyTheLeadersContextEnded(t *testing.T) {
	srv, lookups, _, arrived, release := gatedServer(t, http.StatusOK, readFixture(t, "shows.json"))
	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	q := subtitles.Query{Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1, Languages: []subtitles.LangKey{"en"}}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := p.Search(leaderCtx, q)
		leaderDone <- err
	}()
	awaitArrival(t, arrived)

	waiterDone := make(chan error, 1)
	go func() {
		_, err := p.Search(context.Background(), q)
		waiterDone <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the waiter join the leader's lookup

	cancelLeader()
	require.ErrorIs(t, awaitErr(t, leaderDone), context.Canceled)
	awaitArrival(t, arrived) // the waiter's own retry reaches the server
	release()

	require.NoError(t, awaitErr(t, waiterDone), "the waiter must retry, not inherit the leader's cancellation")
	assert.Equal(t, int32(2), lookups.Load())
}
