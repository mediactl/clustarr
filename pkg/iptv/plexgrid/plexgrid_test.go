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

package plexgrid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

func sample(t *testing.T) []byte {
	b, err := os.ReadFile("../../../test/data/plexgrid/grid-sample.json")
	require.NoError(t, err)
	return b
}

type countLimiter struct{ n atomic.Int32 }

func (c *countLimiter) Wait(context.Context) error { c.n.Add(1); return nil }

func TestGridReducesTheRecordedAnswer(t *testing.T) {
	body := sample(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	ps, err := c.Grid(context.Background(), "k", time.Now(), time.Now())
	require.NoError(t, err)
	require.Len(t, ps, 6)
	for i := 1; i < len(ps); i++ {
		require.False(t, ps[i].Start.Before(ps[i-1].Start))
	}
	var ep, mv *Programme
	for i := range ps {
		if ps[i].Kind == xmltv.KindEpisode && ep == nil {
			ep = &ps[i]
		}
		if ps[i].Kind == xmltv.KindMovie && mv == nil {
			mv = &ps[i]
		}
	}
	require.NotNil(t, ep)
	require.NotNil(t, mv)
	require.Equal(t, "The Outer Limits", ep.Title)
	require.Equal(t, "The Shroud", ep.SubTitle)
	require.Equal(t, 5, ep.Season)
	require.Equal(t, 10, ep.Episode)
	require.Equal(t, "plex://episode/5fc6bf9abcd67c002d0836c8", ep.PlexGUID)
	require.Equal(t, "plex://show/5fc698547c6557002ed5d7bb", ep.PlexShowGUID)
	require.Equal(t, "plex://season/5fc698547c6557002ed5d7bb/5", ep.PlexSeasonGUID)
	require.Equal(t, "https://metadata-static.plex.tv/f/gracenote/f8f45f269dac8cc224b6f25feeb19b5b.jpg", ep.Poster)
	require.Equal(t, "https://metadata-static.plex.tv/2/gracenote/2ced83ca83eec9edc7790afee30a1898.jpg", ep.Still)
	require.Equal(t, "1999-04-30", ep.OriginallyAvailableAt)
	require.Equal(t, "VCHIP", ep.RatingSystem)
	require.Equal(t, 45, ep.LengthMinutes)
	require.True(t, ep.PreviouslyShown)
	require.True(t, ep.Stop.After(ep.Start))
	require.Equal(t, "Alien Predator", mv.Title)
	require.Empty(t, mv.SubTitle)
	require.Zero(t, mv.Season)
	require.Empty(t, mv.Still)
	require.Equal(t, "2018", mv.Date)
	require.Contains(t, mv.Categories, "Action")
}

func TestReduceSportsPremiereAndSeasonGUID(t *testing.T) {
	ps := Reduce([]Item{{
		Type: "episode", Title: "Game", GrandparentTitle: "NFL", ParentGUID: "plex://season/abc", ContentRating: "PG",
		Team:  []Tag{{Tag: "Eagles"}},
		Media: []Media{{BeginsAt: 200, EndsAt: 300, Live: true, Premiere: true}, {BeginsAt: 100, EndsAt: 150}},
	}})
	require.Len(t, ps, 2)
	require.Equal(t, int64(100), ps[0].Start.Unix())
	require.True(t, ps[0].PreviouslyShown)
	require.False(t, ps[0].New)
	require.True(t, ps[1].Live && ps[1].Premiere && ps[1].New && !ps[1].PreviouslyShown)
	require.Equal(t, []string{"Sports"}, ps[0].Categories)
	require.Equal(t, "plex://season/abc", ps[0].PlexSeasonGUID)
	require.Equal(t, "MPAA", ps[0].RatingSystem)
}

func TestGridHonoursTheLimiterAndSendsTheToken(t *testing.T) {
	var tok, key, from, to string
	body := sample(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok = r.Header.Get("X-Plex-Token")
		key, from, to = r.URL.Query().Get("channelGridKey"), r.URL.Query().Get("startDate"), r.URL.Query().Get("endDate")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	lim := &countLimiter{}
	c := &Client{BaseURL: srv.URL, Token: "secret", HTTP: srv.Client(), Limiter: lim}
	d := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	_, err := c.Grid(context.Background(), "grid-1", d, d.AddDate(0, 0, 7))
	require.NoError(t, err)
	require.Equal(t, "secret", tok)
	require.Equal(t, "grid-1", key)
	require.Equal(t, "2026-11-10", from)
	require.Equal(t, "2026-11-17", to)
	require.EqualValues(t, 1, lim.n.Load())
}

func TestA429IsARateLimitedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, HTTP: srv.Client()}).Grid(context.Background(), "k", time.Now(), time.Now())
	var rl *RateLimitedError
	require.True(t, errors.As(err, &rl))
	require.Equal(t, 90*time.Second, rl.RetryAfter)
}

func TestA404IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, HTTP: srv.Client()}).Grid(context.Background(), "k", time.Now(), time.Now())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestABodyOverTheCapIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"MediaContainer":{"Metadata":[],"pad":"`))
		chunk := []byte(strings.Repeat("a", 1<<20))
		for i := 0; i < 33; i++ {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}}`))
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, HTTP: srv.Client()}).Grid(context.Background(), "k", time.Now(), time.Now())
	require.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestAContextCancellationKeepsItsChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Client{BaseURL: srv.URL, Token: "secret", HTTP: srv.Client()}).Grid(ctx, "k", time.Now(), time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, err.Error(), "secret")
}

func TestAnAiringWithABadEndTakesItsDurationOrIsSkipped(t *testing.T) {
	ps := Reduce([]Item{{Type: "movie", Title: "M", Genre: []Tag{{Tag: "Drama"}}, Media: []Media{
		{BeginsAt: 1000, Duration: 600000},                // end missing
		{BeginsAt: 2000, EndsAt: 1500, Duration: 1800000}, // end before begin
		{BeginsAt: 3000},                                 // no end, no duration: skipped
		{BeginsAt: 4000, EndsAt: 4000},                   // not after, no duration: skipped
		{BeginsAt: 5000, EndsAt: 5100, Duration: 999000}, // good end wins
	}}})
	require.Len(t, ps, 3)
	require.Equal(t, int64(1600), ps[0].Stop.Unix())
	require.Equal(t, int64(3800), ps[1].Stop.Unix())
	require.Equal(t, int64(5100), ps[2].Stop.Unix())
	ps[0].Categories[0] = "X"
	require.Equal(t, "Drama", ps[1].Categories[0])
}

func TestANegativeRetryAfterIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "-5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, HTTP: srv.Client()}).Grid(context.Background(), "k", time.Now(), time.Now())
	var rl *RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Zero(t, rl.RetryAfter)
}
