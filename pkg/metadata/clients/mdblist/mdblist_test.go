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

package mdblist_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mdblist"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "data", "metadata", "mdblist", name))
	require.NoError(t, err)
	return b
}

// fakeMDBList serves the recorded responses by path, answers each key as
// its script says, and counts the requests each key made.
type fakeMDBList struct {
	t      *testing.T
	bodies map[string][]byte // path -> body; a missing path is the recorded 404

	mu     sync.Mutex
	status map[string]int         // key -> status to answer instead (401, 429)
	header map[string]http.Header // key -> extra response headers
	calls  map[string]int         // key -> requests seen
	paths  []string
}

func newFake(t *testing.T, bodies map[string][]byte) (*fakeMDBList, *httptest.Server) {
	f := &fakeMDBList{t: t, bodies: bodies, status: map[string]int{}, header: map[string]http.Header{}, calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMDBList) serve(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("apikey")
	f.mu.Lock()
	f.calls[key]++
	f.paths = append(f.paths, r.URL.Path)
	status, extra := f.status[key], f.header[key]
	f.mu.Unlock()

	for k, vs := range extra {
		w.Header()[k] = vs
	}
	w.Header().Set("Content-Type", "application/json")
	switch status {
	case http.StatusUnauthorized:
		w.WriteHeader(status)
		_, _ = w.Write(fixture(f.t, "invalid_key.json"))
		return
	case http.StatusTooManyRequests:
		w.WriteHeader(status)
		return
	}
	body, ok := f.bodies[r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(fixture(f.t, "movie_notfound.json"))
		return
	}
	_, _ = w.Write(body)
}

func (f *fakeMDBList) callsFor(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

func (f *fakeMDBList) script(key string, status int, h http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[key], f.header[key] = status, h
}

func newClient(t *testing.T, srv *httptest.Server, now func() time.Time, keys ...string) *mdblist.Client {
	t.Helper()
	c, err := mdblist.New(mdblist.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, APIKeys: keys, Now: now})
	require.NoError(t, err)
	return c
}

// Every value is the recorded Inception document's, on the CRD's scale:
// out of 10 to 0-1000, out of 100 to 0-10000 (catalogv1alpha1.Rating), the
// scale pkg/overlay.FormatScore and ui/plex read back.
func TestRatingsMapsEverySourceOfTheRecordedMovie(t *testing.T) {
	f, srv := newFake(t, map[string][]byte{"/tmdb/movie/27205": fixture(t, "movie_27205.json")})
	c := newClient(t, srv, nil, "key-one")

	got, err := c.Ratings(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "27205", metadata.KeyIMDb: "tt1375666"})
	require.NoError(t, err)
	assert.Equal(t, metadata.Ratings{
		metadata.RatingSourceIMDb:       {Source: metadata.RatingSourceIMDb, ValueCentis: 880, Votes: 2871757},
		metadata.RatingSourceTMDB:       {Source: metadata.RatingSourceTMDB, ValueCentis: 830, Votes: 40254},
		metadata.RatingSourceTrakt:      {Source: metadata.RatingSourceTrakt, ValueCentis: 870, Votes: 68590},
		metadata.RatingSourceLetterboxd: {Source: metadata.RatingSourceLetterboxd, ValueCentis: 840, Votes: 4470284},
		metadata.RatingSourceMetacritic: {Source: metadata.RatingSourceMetacritic, ValueCentis: 7400, Votes: 42},
		metadata.RatingSourceRTCritic:   {Source: metadata.RatingSourceRTCritic, ValueCentis: 8600, Votes: 364},
		metadata.RatingSourceRTAudience: {Source: metadata.RatingSourceRTAudience, ValueCentis: 9100, Votes: 41618},
	}, got)
	assert.Equal(t, []string{"/tmdb/movie/27205"}, f.paths)
	assert.Equal(t, 1, f.callsFor("key-one"))
}

// The recorded Game of Thrones document has a null letterboxd value (left
// out) and a null popcorn vote count (kept, with no votes). A Series here
// is TVDB-keyed; /tvdb/show answers the same document.
func TestRatingsKeysASeriesByTVDBWhenItHasNoTMDBID(t *testing.T) {
	show := fixture(t, "show_1399.json")
	f, srv := newFake(t, map[string][]byte{"/tvdb/show/121361": show, "/tmdb/show/1399": show})
	c := newClient(t, srv, nil, "key-one")

	got, err := c.Ratings(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "121361"})
	require.NoError(t, err)
	assert.Equal(t, metadata.Rating{Source: metadata.RatingSourceMetacritic, ValueCentis: 8600, Votes: 171}, got[metadata.RatingSourceMetacritic])
	assert.Equal(t, metadata.Rating{Source: metadata.RatingSourceRTAudience, ValueCentis: 8500}, got[metadata.RatingSourceRTAudience])
	assert.NotContains(t, got, metadata.RatingSourceLetterboxd, "a null value is no rating")
	assert.Len(t, got, 6)

	_, err = c.Ratings(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "121361", metadata.KeyTMDB: "1399"})
	require.NoError(t, err)
	assert.Equal(t, []string{"/tvdb/show/121361", "/tmdb/show/1399"}, f.paths, "a TMDB id is preferred when there is one")
}

func TestRatingsRefusesWhatItCannotKey(t *testing.T) {
	f, srv := newFake(t, nil)
	c := newClient(t, srv, nil, "key-one")
	ctx := context.Background()

	_, err := c.Ratings(ctx, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTVDB: "113"})
	require.ErrorIs(t, err, metadata.ErrUnsupported, "a movie is keyed by TMDB id only")
	_, err = c.Ratings(ctx, commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyTMDB: "1"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Ratings(ctx, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "27205/../../user"})
	require.ErrorIs(t, err, mdblist.ErrInvalidID)
	assert.Empty(t, f.paths, "nothing is requested for an id that cannot be keyed")

	assert.Len(t, c.RatingSources(commonv1.MediaKindMovie), 7)
	assert.Len(t, c.RatingSources(commonv1.MediaKindSeries), 7)
	assert.Nil(t, c.RatingSources(commonv1.MediaKindAlbum))
}

func TestRatingsMapsTheRecordedErrors(t *testing.T) {
	f, srv := newFake(t, nil)
	c := newClient(t, srv, nil, "secret-key-one")

	_, err := c.Ratings(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "999999999"})
	require.ErrorIs(t, err, metadata.ErrNotFound)

	f.script("secret-key-one", http.StatusUnauthorized, nil)
	_, err = c.Ratings(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "27205"})
	require.ErrorIs(t, err, metadata.ErrAuth)
	assert.NotContains(t, err.Error(), "secret-key-one", "the key travels in the query, which errors never show")
}

// A key that is out rests until its X-RateLimit-Reset while the next key
// carries on, and is used again once the reset has passed.
func TestA429MovesToTheNextKeyUntilTheFirstResets(t *testing.T) {
	f, srv := newFake(t, map[string][]byte{"/tmdb/movie/27205": fixture(t, "movie_27205.json")})
	now := time.Unix(1_790_280_000, 0)
	clock := func() time.Time { return now }
	c := newClient(t, srv, clock, "primary", "secondary")
	ids := metadata.ExternalIDs{metadata.KeyTMDB: "27205"}
	ctx := context.Background()

	reset := now.Add(3 * time.Hour)
	f.script("primary", http.StatusTooManyRequests, http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}})
	got, err := c.Ratings(ctx, commonv1.MediaKindMovie, ids)
	require.NoError(t, err)
	assert.Contains(t, got, metadata.RatingSourceMetacritic)
	assert.Equal(t, 1, f.callsFor("primary"))
	assert.Equal(t, 1, f.callsFor("secondary"))

	_, err = c.Ratings(ctx, commonv1.MediaKindMovie, ids)
	require.NoError(t, err)
	assert.Equal(t, 1, f.callsFor("primary"), "a resting key is not asked again")
	assert.Equal(t, 2, f.callsFor("secondary"))

	f.script("primary", 0, nil)
	now = reset
	_, err = c.Ratings(ctx, commonv1.MediaKindMovie, ids)
	require.NoError(t, err)
	assert.Equal(t, 2, f.callsFor("primary"), "back to the first key once it has reset")
}

// A success that reports nothing remaining rests the key before it is
// spent on a request that would only be refused.
func TestNoneRemainingRestsTheKeyWithoutA429(t *testing.T) {
	f, srv := newFake(t, map[string][]byte{"/tmdb/movie/27205": fixture(t, "movie_27205.json")})
	now := time.Unix(1_790_280_000, 0)
	c := newClient(t, srv, func() time.Time { return now }, "primary", "secondary")
	ids := metadata.ExternalIDs{metadata.KeyTMDB: "27205"}

	f.script("primary", 0, http.Header{
		"X-Ratelimit-Remaining": {"0"},
		"X-Ratelimit-Reset":     {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
	})
	_, err := c.Ratings(context.Background(), commonv1.MediaKindMovie, ids)
	require.NoError(t, err)
	_, err = c.Ratings(context.Background(), commonv1.MediaKindMovie, ids)
	require.NoError(t, err)
	assert.Equal(t, 1, f.callsFor("primary"))
	assert.Equal(t, 1, f.callsFor("secondary"))
}

func TestEveryKeyOutIsRateLimitedUntilTheSoonestReset(t *testing.T) {
	f, srv := newFake(t, map[string][]byte{"/tmdb/movie/27205": fixture(t, "movie_27205.json")})
	now := time.Unix(1_790_280_000, 0)
	c := newClient(t, srv, func() time.Time { return now }, "primary", "secondary")
	ids := metadata.ExternalIDs{metadata.KeyTMDB: "27205"}

	f.script("primary", http.StatusTooManyRequests, http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10)}})
	f.script("secondary", http.StatusTooManyRequests, http.Header{"Retry-After": {"1800"}})
	_, err := c.Ratings(context.Background(), commonv1.MediaKindMovie, ids)
	require.ErrorIs(t, err, metadata.ErrRateLimited)
	var rl *metadata.RateLimitedError
	require.True(t, errors.As(err, &rl))
	assert.Equal(t, 30*time.Minute, rl.RetryAfter, "the secondary's Retry-After is the sooner of the two")

	_, err = c.Ratings(context.Background(), commonv1.MediaKindMovie, ids)
	require.ErrorIs(t, err, metadata.ErrRateLimited)
	assert.Equal(t, 1, f.callsFor("primary"), "no request while every key rests")
	assert.Equal(t, 1, f.callsFor("secondary"))
}

func TestNewNeedsAKeyAndDropsABlankSecondOne(t *testing.T) {
	_, err := mdblist.New(mdblist.Config{})
	require.ErrorIs(t, err, mdblist.ErrNoAPIKey)
	_, err = mdblist.New(mdblist.Config{APIKeys: []string{"", "secondary"}})
	require.ErrorIs(t, err, mdblist.ErrNoAPIKey, "the first key is the required one")

	f, srv := newFake(t, map[string][]byte{"/user": fixture(t, "user.json")})
	c := newClient(t, srv, nil, "primary", "")
	require.NoError(t, c.Ping(context.Background()))
	assert.Equal(t, 1, f.callsFor("primary"))
	assert.Zero(t, f.callsFor(""), "a blank key is no key, not a key that fails")
}

// Ping probes every key through GET /user, which does not spend quota, and
// names a rejected key by position only.
func TestPingProbesEveryKey(t *testing.T) {
	f, srv := newFake(t, map[string][]byte{"/user": fixture(t, "user.json")})
	c := newClient(t, srv, nil, "primary", "secret-secondary")
	require.NoError(t, c.Ping(context.Background()))
	assert.Equal(t, 1, f.callsFor("primary"))
	assert.Equal(t, 1, f.callsFor("secret-secondary"))

	f.script("secret-secondary", http.StatusUnauthorized, nil)
	err := c.Ping(context.Background())
	require.ErrorIs(t, err, metadata.ErrAuth)
	assert.ErrorContains(t, err, "key 2 of 2")
	assert.NotContains(t, err.Error(), "secret-secondary")
}
