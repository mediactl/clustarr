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

package indexer_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/indexarr/controller/indexer"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// expiringTracker serves login-form.yml's tracker with a session it can
// kill: /browse answers with one result row for the CURRENT session cookie
// and redirects anything else to /login, which is how a real tracker tells a
// client its session is gone. Each successful login mints a new session.
type expiringTracker struct {
	srv     *httptest.Server
	submits atomic.Int32
	mu      sync.Mutex
	valid   string
}

func newExpiringTracker(t *testing.T) *expiringTracker {
	t.Helper()
	et := &expiringTracker{}
	page := cardigannYAML(t, "login-form.html")
	et.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, page)
		case r.URL.Path == "/login" && r.Method == http.MethodPost:
			n := et.submits.Add(1)
			_ = r.ParseForm()
			if r.PostForm.Get("csrf_token") != "tok-abc123" ||
				r.PostForm.Get("username") != "alice" || r.PostForm.Get("password") != "hunter22" {
				_, _ = io.WriteString(w, `<html><body><div class="error">Invalid username or password</div></body></html>`)
				return
			}
			sess := fmt.Sprintf("sess-%d", n)
			et.mu.Lock()
			et.valid = sess
			et.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "uid", Value: sess})
			_, _ = io.WriteString(w, `<html><body><a class="logout" href="/logout">logout</a></body></html>`)
		case r.URL.Path == "/browse":
			ck, err := r.Cookie("uid")
			et.mu.Lock()
			ok := err == nil && ck.Value == et.valid && et.valid != ""
			et.mu.Unlock()
			if !ok {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, `<html><body><table><tr>`+
				`<td class="title"><a href="/dl/1.torrent">Some.Movie.2024.1080p</a></td>`+
				`<td class="size">1 GB</td><td class="seeders">9</td></tr></table></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(et.srv.Close)
	return et
}

// A search the tracker redirects to its login page logs in again and retries
// once -- it is a killed session, not a failing indexer, so it must not reach
// the fan-out as an error that escalates health -- and the renewed session is
// persisted where the reconciler and the generic fetcher read it.
func TestAnExpiredSessionLogsInAgainInsteadOfFailingTheSearch(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-relogin")
	createDefinition(t, c, "synthetic-form-relogin", "login-form.yml")
	tracker := newExpiringTracker(t)
	name := loginIndexer(t, c, ns, "expiring", "synthetic-form-relogin", tracker.srv.URL, "hunter22")
	idx := mustGet(t, c, name)

	// Steady state: a session the tracker has since killed.
	store := indexer.NewSessionStore(c, newMemBus(t))
	require.NoError(t, store.Save(ctx, &idx, &cardigann.Session{Cookies: []*http.Cookie{{Name: "uid", Value: "killed"}}}))

	cc := indexer.NewClientCache(c, nil)
	cc.Sessions = store
	cli, err := cc.For(ctx, &idx)
	require.NoError(t, err)

	rels, err := cli.Search(ctx, torznab.Query{Type: torznab.ModeSearch, Q: "movie"})
	require.NoError(t, err, "an expired session must be renewed, not reported as an indexer failure")
	require.Len(t, rels, 1)
	require.Equal(t, int32(1), tracker.submits.Load())

	persisted, err := store.Load(ctx, &idx)
	require.NoError(t, err)
	require.Equal(t, "uid=sess-1", persisted.CookieHeader(), "the renewed session was not persisted")

	// Concurrent searches on the renewed session log in no further.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, serr := cli.Search(ctx, torznab.Query{Type: torznab.ModeSearch, Q: "movie"})
			errs <- serr
		}()
	}
	wg.Wait()
	close(errs)
	for serr := range errs {
		require.NoError(t, serr)
	}
	require.Equal(t, int32(1), tracker.submits.Load())
}

// When the re-login itself fails -- the credentials no longer work -- that IS
// an indexer failure and reaches the caller; and the killed session is
// dropped, so the next reconcile logs in and reports the problem on the
// Authenticated condition rather than reusing a dead session until it ages
// out.
func TestAFailedReLoginFailsTheSearchAndDropsTheSession(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-relogin-fail")
	createDefinition(t, c, "synthetic-form-relogin-fail", "login-form.yml")
	tracker := newExpiringTracker(t)
	name := loginIndexer(t, c, ns, "rotated", "synthetic-form-relogin-fail", tracker.srv.URL, "wrong-now")
	idx := mustGet(t, c, name)

	store := indexer.NewSessionStore(c, newMemBus(t))
	require.NoError(t, store.Save(ctx, &idx, &cardigann.Session{Cookies: []*http.Cookie{{Name: "uid", Value: "killed"}}}))
	cc := indexer.NewClientCache(c, nil)
	cc.Sessions = store
	cli, err := cc.For(ctx, &idx)
	require.NoError(t, err)

	_, err = cli.Search(ctx, torznab.Query{Type: torznab.ModeSearch, Q: "movie"})
	require.Error(t, err)
	var le *cardigann.LoginError
	require.True(t, errors.As(err, &le), "want the login failure, got %v", err)
	require.False(t, errors.Is(err, cardigann.ErrSessionExpired))

	sess, err := store.Load(ctx, &idx)
	require.NoError(t, err)
	require.Nil(t, sess, "the killed session must be dropped so the reconciler logs in again")
}
