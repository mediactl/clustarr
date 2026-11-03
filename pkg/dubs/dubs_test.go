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

package dubs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// list is a dubInfo.json of the list's shape (its license and origin
// fields, then the two id lists), with ids of our own choosing.
const list = `{"_license":"AGPL-3.0","_origin":"https://github.com/mal-dubs/mal-dubs",` +
	`"dubbed":[1,5,41457],"incomplete":[8,56352]}`

func TestParseReadsBothListsAndRefusesAnotherShape(t *testing.T) {
	d, in, err := Parse([]byte(list))
	require.NoError(t, err)
	assert.Equal(t, []int{1, 5, 41457}, d)
	assert.Equal(t, []int{8, 56352}, in)
	_, _, err = Parse([]byte(`{"dubbed":[1]}`))
	assert.Error(t, err, "a file without the incomplete list is no list")
	_, _, err = Parse([]byte(`[1,2]`))
	assert.Error(t, err)
}

// The index answers Ungoverned until it has read a list, so a fetch that
// never succeeded changes no item's policy; then an anime with no MAL id,
// or one the list does not name, is NotDubbed.
func TestTheIndexAnswersEachState(t *testing.T) {
	ix := NewIndex()
	assert.Equal(t, Ungoverned, ix.Lookup("41457"), "no list read yet")
	var none *Index
	assert.Equal(t, Ungoverned, none.Lookup("41457"))

	ix.Set(Record{Dubbed: []int{1, 5, 41457}, Incomplete: []int{8, 56352}})
	for mal, want := range map[string]State{
		"41457": Dubbed, "56352": Incomplete, "53802": NotDubbed, "": NotDubbed, "x": NotDubbed,
	} {
		assert.Equal(t, want, ix.Lookup(mal), "mal %q", mal)
	}
}

// Only an anime is governed: a series by its type, a movie by its anime
// classification, each by status.metadata.externalIDs["mal"].
func TestOnlyAnAnimeIsGoverned(t *testing.T) {
	ix := NewIndex()
	ix.Set(Record{Dubbed: []int{41457}, Incomplete: []int{56352}})
	series := func(typ catalogv1alpha1.SeriesType, mal string) *catalogv1alpha1.Series {
		s := &catalogv1alpha1.Series{}
		s.Spec.SeriesType = typ
		if mal != "" {
			s.Status.Metadata = &catalogv1alpha1.SeriesMetadata{ExternalIDs: map[string]string{"mal": mal}}
		}
		return s
	}
	assert.Equal(t, Dubbed, ix.Series(series(catalogv1alpha1.SeriesTypeAnime, "41457")))
	assert.Equal(t, Incomplete, ix.Series(series(catalogv1alpha1.SeriesTypeAnime, "56352")))
	assert.Equal(t, NotDubbed, ix.Series(series(catalogv1alpha1.SeriesTypeAnime, "")), "an anime without a MAL id")
	assert.Equal(t, Ungoverned, ix.Series(series(catalogv1alpha1.SeriesTypeStandard, "")), "a K-drama is no anime")
	assert.Equal(t, Ungoverned, ix.Series(nil))

	movie := func(anime bool, mal string) *catalogv1alpha1.Movie {
		m := &catalogv1alpha1.Movie{}
		m.Status.Classification = &catalogv1alpha1.MovieClassification{Anime: anime}
		m.Status.Metadata = &catalogv1alpha1.MovieMetadata{ExternalIDs: map[string]string{"mal": mal}}
		return m
	}
	assert.Equal(t, Dubbed, ix.Movie(movie(true, "41457")))
	assert.Equal(t, NotDubbed, ix.Movie(movie(true, "1234")))
	assert.Equal(t, Ungoverned, ix.Movie(movie(false, "41457")))
	assert.Equal(t, Ungoverned, ix.Movie(&catalogv1alpha1.Movie{}), "never classified")
}

func bucket(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketMALDubs)
}

func stored(t *testing.T, kv events.KV) Record {
	t.Helper()
	e, err := kv.Get(context.Background(), Key)
	require.NoError(t, err)
	var r Record
	require.NoError(t, json.Unmarshal(e.Value, &r))
	return r
}

// The refresher fetches at once when nothing is stored, keeps the list a
// day, then asks by its ETag: a 304 keeps the stored lists and restamps
// them, and a failed fetch leaves the stored list standing.
func TestTheRefresherFetchesDailyByETag(t *testing.T) {
	var calls, fail atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() == 1 {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(list))
	}))
	defer srv.Close()

	kv := bucket(t)
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	r := &Refresher{KV: kv, HTTP: srv.Client(), URL: srv.URL, Now: func() time.Time { return now }}
	ctx := context.Background()

	assert.Equal(t, RefreshInterval, r.refresh(ctx))
	got := stored(t, kv)
	assert.Equal(t, []int{1, 5, 41457}, got.Dubbed)
	assert.Equal(t, `"v1"`, got.ETag)
	assert.Equal(t, int32(1), calls.Load())

	now = now.Add(time.Hour)
	assert.Equal(t, 23*time.Hour, r.refresh(ctx), "not due for a day")
	assert.Equal(t, int32(1), calls.Load())

	now = now.Add(23 * time.Hour)
	assert.Equal(t, RefreshInterval, r.refresh(ctx))
	got = stored(t, kv)
	assert.Equal(t, []int{8, 56352}, got.Incomplete, "a 304 keeps the stored lists")
	assert.Equal(t, now, got.FetchedAt.UTC())
	assert.Equal(t, int32(2), calls.Load())

	now = now.Add(RefreshInterval)
	fail.Store(1)
	assert.Equal(t, RetryInterval, r.refresh(ctx))
	assert.Equal(t, []int{1, 5, 41457}, stored(t, kv).Dubbed, "a failed fetch leaves the list")
}

// A fetched file over the cap is refused, never parsed.
func TestFetchCapsTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"dubbed":[` + strings.Repeat("1,", maxBody/2) + `1],"incomplete":[]}`))
	}))
	defer srv.Close()
	_, _, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

// The index follows the bucket: every process reads the leader's fetch.
func TestTheIndexWatchesTheBucket(t *testing.T) {
	kv := bucket(t)
	ix := NewIndex()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ix.Run(ctx, kv) }()
	b, err := json.Marshal(Record{Dubbed: []int{41457}, Incomplete: []int{56352}})
	require.NoError(t, err)
	_, err = kv.Put(ctx, Key, b)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return ix.Lookup("56352") == Incomplete }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, ix, From(WithIndex(ctx, ix)))
	assert.Nil(t, From(ctx))
}
