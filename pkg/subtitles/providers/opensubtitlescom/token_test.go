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

package opensubtitlescom_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

// jwt builds a token shaped like the one OpenSubtitles.com issues: three
// base64url segments whose payload carries iat and exp.
func jwt(iat, exp time.Time) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." +
		enc.EncodeToString(fmt.Appendf(nil, `{"sub":"424242","iat":%d,"exp":%d}`, iat.Unix(), exp.Unix())) + ".c2ln"
}

type storedToken struct {
	token     string
	expiresAt time.Time
}

// memCache is an in-memory TokenCache standing in for captionarr's KV
// adapter. storeErr makes StoreToken fail and leave the cache as it was.
type memCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	loadErr   error
	storeErr  error
	stores    []storedToken
}

func (c *memCache) LoadToken(context.Context) (string, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.expiresAt, c.loadErr
}

func (c *memCache) StoreToken(_ context.Context, token string, expiresAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stores = append(c.stores, storedToken{token, expiresAt})
	if c.storeErr != nil {
		return c.storeErr
	}
	c.token, c.expiresAt = token, expiresAt
	return nil
}

func (c *memCache) storedTokens() []storedToken {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]storedToken(nil), c.stores...)
}

// authServer is an OpenSubtitles.com stand-in. /login issues the next of
// loginTokens (repeating the last); /subtitles and /download accept only the
// bearer tokens in valid, and answer any other with 401. When
// refuseBarrier is above one, each refusal is held until that many have
// arrived (or a second has passed), so a test can make its concurrent
// requests all be refused at once. It counts logins and records the bearer
// and body of every API call.
type authServer struct {
	logins  atomic.Int32
	mu      sync.Mutex
	bearers []string
	bodies  []string
}

type authServerOpts struct {
	loginTokens   []string
	valid         []string
	refuseBarrier int
}

func newAuthServer(t *testing.T, loginToken string, valid ...string) (*authServer, string) {
	t.Helper()
	return newAuthServerOpts(t, authServerOpts{loginTokens: []string{loginToken}, valid: valid})
}

func newAuthServerOpts(t *testing.T, o authServerOpts) (*authServer, string) {
	t.Helper()
	as := &authServer{}
	ok := map[string]bool{}
	for _, v := range o.valid {
		ok[v] = true
	}
	var refusals atomic.Int32
	allRefused := make(chan struct{})
	// The CDN leg of a download carries no bearer, so it has its own server.
	cdn := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHi.\n"))
	})
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			n := int(as.logins.Add(1))
			tok := o.loginTokens[min(n, len(o.loginTokens))-1]
			_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "base_url": "api.opensubtitles.com", "user": map[string]any{"vip": false}})
			return
		}
		bearer := r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		as.mu.Lock()
		as.bearers = append(as.bearers, bearer)
		as.bodies = append(as.bodies, string(body))
		as.mu.Unlock()
		if len(bearer) < 7 || !ok[bearer[7:]] {
			if n := int(refusals.Add(1)); n <= o.refuseBarrier {
				if n == o.refuseBarrier {
					close(allRefused)
				}
				select {
				case <-allRefused:
				case <-time.After(time.Second):
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"You cannot consume this service"}`))
			return
		}
		switch r.URL.Path {
		case "/subtitles":
			_, _ = w.Write(readFixture(t, "search.json"))
		case "/download":
			_, _ = w.Write([]byte(`{"link":"` + cdn.URL + `/file.srt","file_name":"x.srt"}`))
		default:
			http.NotFound(w, r)
		}
	})
	return as, srv.URL
}

func (as *authServer) calls() (bearers, bodies []string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]string(nil), as.bearers...), append([]string(nil), as.bodies...)
}

func newProvider(endpoint string, cache opensubtitlescom.TokenCache) *opensubtitlescom.Provider {
	return opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", Endpoint: endpoint, TokenCache: cache,
	})
}

func movieQuery() subtitles.Query {
	return subtitles.Query{Kind: "movie", IDs: map[string]string{"imdb": "1375666"}, Languages: []subtitles.LangKey{"en"}}
}

// A replica that finds a fresh token in the shared cache uses it and never
// logs in: the whole point of the cache, since OpenSubtitles.com rate-limits
// logins far harder than searches.
func TestAFreshSharedTokenIsUsedWithoutLoggingIn(t *testing.T) {
	now := time.Now()
	shared := jwt(now.Add(-time.Hour), now.Add(23*time.Hour))
	as, endpoint := newAuthServer(t, "never-issued", shared)
	cache := &memCache{token: shared, expiresAt: now.Add(23 * time.Hour)}

	_, err := newProvider(endpoint, cache).Search(context.Background(), movieQuery())
	require.NoError(t, err)

	assert.Zero(t, as.logins.Load(), "a fresh shared token must be used, not replaced")
	bearers, _ := as.calls()
	assert.Equal(t, []string{"Bearer " + shared}, bearers)
	assert.Empty(t, cache.storedTokens())
}

// Bazarr reuses a token for 12 of its 24 hours. A shared token past that
// half-way point is replaced by a login, and the new token -- with the
// expiry its own exp claim states -- is stored for the other replicas.
func TestAStaleSharedTokenIsReplacedAndTheReplacementShared(t *testing.T) {
	now := time.Now()
	stale := jwt(now.Add(-13*time.Hour), now.Add(11*time.Hour))
	fresh := jwt(now, now.Add(24*time.Hour))
	// The server would still accept the stale token: only the client's
	// own freshness rule can be what replaces it.
	as, endpoint := newAuthServer(t, fresh, fresh, stale)
	cache := &memCache{token: stale, expiresAt: now.Add(11 * time.Hour)}

	_, err := newProvider(endpoint, cache).Search(context.Background(), movieQuery())
	require.NoError(t, err)

	assert.Equal(t, int32(1), as.logins.Load())
	stored := cache.storedTokens()
	require.Len(t, stored, 1)
	assert.Equal(t, fresh, stored[0].token)
	assert.True(t, stored[0].expiresAt.Equal(time.Unix(now.Add(24*time.Hour).Unix(), 0)), "expiry is the JWT's exp claim, got %s", stored[0].expiresAt)
}

// A token that is not a readable JWT (the login.json fixture's) is stored
// with the documented 24-hour life, and reused for the first 12.
func TestAnOpaqueTokenGetsTheDocumentedLifetime(t *testing.T) {
	as, endpoint := newAuthServer(t, "opaque-token", "opaque-token")
	cache := &memCache{}
	p := newProvider(endpoint, cache)

	before := time.Now()
	_, err := p.Search(context.Background(), movieQuery())
	require.NoError(t, err)
	_, err = p.Search(context.Background(), movieQuery())
	require.NoError(t, err)

	assert.Equal(t, int32(1), as.logins.Load())
	stored := cache.storedTokens()
	require.Len(t, stored, 1)
	assert.WithinDuration(t, before.Add(24*time.Hour), stored[0].expiresAt, 5*time.Second)
}

// An unreadable cache costs a login, not the search; a cache that cannot
// store costs the other replicas their own login, not this search.
func TestACacheFailureNeverFailsTheSearch(t *testing.T) {
	as, endpoint := newAuthServer(t, "tok", "tok")
	cache := &memCache{loadErr: errors.New("kv down"), storeErr: errors.New("kv down")}

	_, err := newProvider(endpoint, cache).Search(context.Background(), movieQuery())
	require.NoError(t, err)
	assert.Equal(t, int32(1), as.logins.Load())
}

// Bazarr's checked(): a 401 resets the token, logs in once and retries. The
// replacement is shared.
func TestA401LogsInAgainOnceAndRetries(t *testing.T) {
	now := time.Now()
	revoked := jwt(now.Add(-time.Hour), now.Add(23*time.Hour))
	fresh := jwt(now, now.Add(24*time.Hour))
	as, endpoint := newAuthServer(t, fresh, fresh)
	cache := &memCache{token: revoked, expiresAt: now.Add(23 * time.Hour)}

	cands, err := newProvider(endpoint, cache).Search(context.Background(), movieQuery())
	require.NoError(t, err)
	require.Len(t, cands, 1)

	assert.Equal(t, int32(1), as.logins.Load())
	bearers, _ := as.calls()
	assert.Equal(t, []string{"Bearer " + revoked, "Bearer " + fresh}, bearers)
	stored := cache.storedTokens()
	require.Len(t, stored, 1)
	assert.Equal(t, fresh, stored[0].token)
}

// The retry must not pick the refused token straight back up from the
// cache -- here the cache could not store the replacement, so it still
// holds the refused one.
func TestA401NeverReadoptsTheRefusedTokenFromTheCache(t *testing.T) {
	now := time.Now()
	revoked := jwt(now.Add(-time.Hour), now.Add(23*time.Hour))
	as, endpoint := newAuthServer(t, "replacement", "replacement")
	cache := &memCache{token: revoked, expiresAt: now.Add(23 * time.Hour), storeErr: errors.New("kv down")}

	_, err := newProvider(endpoint, cache).Search(context.Background(), movieQuery())
	require.NoError(t, err)
	assert.Equal(t, int32(1), as.logins.Load())
}

// A second 401 -- the new login's token refused too -- is an Auth error
// after exactly one re-login, not a loop.
func TestASecond401IsAnAuthError(t *testing.T) {
	as, endpoint := newAuthServer(t, "refused-too")

	_, err := newProvider(endpoint, nil).Search(context.Background(), movieQuery())
	require.Error(t, err)
	assert.ErrorIs(t, err, subtitles.ErrAuth)
	assert.Equal(t, int32(2), as.logins.Load(), "the first login, then one re-login")
	bearers, _ := as.calls()
	assert.Len(t, bearers, 2)
}

// Concurrent requests refused with the same token share one re-login: the
// first to re-login replaces the token, and the rest find it already
// replaced. No cache here, so nothing but that rule can stop each refused
// request logging in again.
func TestConcurrent401sShareOneRelogin(t *testing.T) {
	const n = 8
	as, endpoint := newAuthServerOpts(t, authServerOpts{
		loginTokens: []string{"refused", "replacement"}, valid: []string{"replacement"}, refuseBarrier: n,
	})
	p := newProvider(endpoint, nil)
	require.NoError(t, p.EnsureLoggedIn(context.Background()))

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = p.Search(context.Background(), movieQuery())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(2), as.logins.Load(), "the first login, then one shared re-login")
}

// Freshness follows Bazarr's 12 hours of a 24-hour token: a shared token is
// used for the first half of its life and replaced after. A JWT's own
// claims decide when it has them; the cache's recorded expiry decides for
// an opaque token.
func TestSharedTokenFreshnessFollowsBazarrsTwelveHours(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		token     string
		expiresAt time.Time
		used      bool
	}{
		{"jwt 11h into a 24h life", jwt(now.Add(-11*time.Hour), now.Add(13*time.Hour)), now.Add(13 * time.Hour), true},
		{"jwt 13h into a 24h life", jwt(now.Add(-13*time.Hour), now.Add(11*time.Hour)), now.Add(11 * time.Hour), false},
		// A life other than 24h is halved too, rather than held to a fixed
		// 12h margin that a short token could never clear.
		{"jwt 10m into a 2h life", jwt(now.Add(-10*time.Minute), now.Add(110*time.Minute)), now.Add(110 * time.Minute), true},
		{"jwt past half of a 2h life", jwt(now.Add(-90*time.Minute), now.Add(30*time.Minute)), now.Add(30 * time.Minute), false},
		{"jwt with exp only, 13h left", jwtExpOnly(now.Add(13 * time.Hour)), now.Add(13 * time.Hour), true},
		{"jwt with exp only, 11h left", jwtExpOnly(now.Add(11 * time.Hour)), now.Add(11 * time.Hour), false},
		{"the claim outranks the cache's expiry", jwtExpOnly(now.Add(11 * time.Hour)), now.Add(23 * time.Hour), false},
		{"opaque, 13h left", "opaque", now.Add(13 * time.Hour), true},
		{"opaque, 11h left", "opaque", now.Add(11 * time.Hour), false},
		{"opaque, no recorded expiry", "opaque", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as, endpoint := newAuthServer(t, "new", "new", tc.token)
			cache := &memCache{token: tc.token, expiresAt: tc.expiresAt}

			_, err := newProvider(endpoint, cache).Search(context.Background(), movieQuery())
			require.NoError(t, err)
			if tc.used {
				assert.Zero(t, as.logins.Load(), "a fresh shared token must be used")
			} else {
				assert.Equal(t, int32(1), as.logins.Load(), "a stale shared token must be replaced")
			}
		})
	}
}

// jwtExpOnly is a token whose claims carry exp but no iat.
func jwtExpOnly(exp time.Time) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." +
		enc.EncodeToString(fmt.Appendf(nil, `{"exp":%d}`, exp.Unix())) + ".c2ln"
}

// The /download POST is rebuilt for the retry: the replayed request still
// carries its file_id.
func TestA401OnDownloadRetriesWithTheBodyIntact(t *testing.T) {
	now := time.Now()
	revoked := jwt(now.Add(-time.Hour), now.Add(23*time.Hour))
	as, endpoint := newAuthServer(t, "replacement", "replacement")
	cache := &memCache{token: revoked, expiresAt: now.Add(23 * time.Hour)}

	raw, _, err := newProvider(endpoint, cache).Download(context.Background(), subtitles.Candidate{FetchID: "998877"})
	require.NoError(t, err)
	assert.NotEmpty(t, raw)

	_, bodies := as.calls()
	require.Len(t, bodies, 2)
	for _, b := range bodies {
		assert.JSONEq(t, `{"file_id":998877,"sub_format":"srt"}`, b)
	}
}
