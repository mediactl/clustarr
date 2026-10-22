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

package mediuxcache_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mediux"
	"github.com/mediactl/clustarr/pkg/metadata/mediuxcache"
)

// fake is MediUX as the cache sees it, counting every question.
type fake struct {
	calls   map[string]int
	shows   map[string]*mediux.Show
	tvdb    map[string]string
	content *mediux.ContentIDs
	fail    error
}

func (f *fake) ShowByTVDB(_ context.Context, tvdb string) (string, error) {
	f.calls["tvdb"]++
	if f.fail != nil {
		return "", f.fail
	}
	if id, ok := f.tvdb[tvdb]; ok {
		return id, nil
	}
	return "", fmt.Errorf("x: %w", metadata.ErrNotFound)
}

func (f *fake) ShowSets(_ context.Context, tmdb string) (*mediux.Show, error) {
	f.calls["show"]++
	if f.fail != nil {
		return nil, f.fail
	}
	if s, ok := f.shows[tmdb]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("x: %w", metadata.ErrNotFound)
}

func (f *fake) MovieSets(context.Context, string) (*mediux.Movie, error) {
	f.calls["movie"]++
	return nil, fmt.Errorf("x: %w", metadata.ErrNotFound)
}

func (f *fake) CollectionSets(context.Context, string) (*mediux.Collection, error) {
	f.calls["collection"]++
	return &mediux.Collection{ID: "404609", Name: "John Wick Collection"}, nil
}

func (f *fake) ContentIDs(context.Context) (*mediux.ContentIDs, error) {
	f.calls["content"]++
	if f.fail != nil {
		return nil, f.fail
	}
	return f.content, nil
}

func harness(t *testing.T) (*mediuxcache.Cache, *fake, *clockwork.FakeClock) {
	t.Helper()
	clock := clockwork.NewFakeClockAt(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC))
	bus := membus.New(clock)
	ctx := context.Background()
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	f := &fake{
		calls:   map[string]int{},
		shows:   map[string]*mediux.Show{"105248": {ID: "105248", Title: "Cyberpunk: Edgerunners", Sets: []mediux.ShowSet{{ID: "41471"}}}},
		tvdb:    map[string]string{"384541": "105248"},
		content: &mediux.ContentIDs{Shows: map[string]bool{"105248": true}, Movies: map[string]bool{"328387": true}},
	}
	c := &mediuxcache.Cache{KV: bus.KV(events.BucketMediux), Lookup: f, Now: clock.Now}
	return c, f, clock
}

func TestAShowsSetsAreAskedOnceAWeek(t *testing.T) {
	c, f, clock := harness(t)
	ctx := context.Background()
	show, err := c.ShowSets(ctx, "105248")
	require.NoError(t, err)
	require.Equal(t, "41471", show.Sets[0].ID)
	for range 5 {
		_, err = c.ShowSets(ctx, "105248")
		require.NoError(t, err)
	}
	require.Equal(t, 1, f.calls["show"], "five more reads, no more questions")
	require.Equal(t, 1, f.calls["content"], "the content list was fetched once, before the first set query")
	clock.Advance(8 * 24 * time.Hour)
	_, err = c.ShowSets(ctx, "105248")
	require.NoError(t, err)
	require.Equal(t, 2, f.calls["show"], "a week on, asked again")
}

func TestAnItemNotInTheContentListIsNeverAsked(t *testing.T) {
	c, f, clock := harness(t)
	ctx := context.Background()
	_, err := c.ShowSets(ctx, "777")
	require.ErrorIs(t, err, metadata.ErrNotFound)
	require.Equal(t, 0, f.calls["show"], "the content list answered")
	require.Equal(t, 1, f.calls["content"])
	clock.Advance(2 * time.Hour)
	_, err = c.ShowSets(ctx, "777")
	require.ErrorIs(t, err, metadata.ErrNotFound)
	require.Equal(t, 1, f.calls["content"], "the day's list, and the negative entry, stand")
	// The next day MediUX may have gained a set: the list is fetched again
	// and the item is in it now.
	f.content.Shows["777"] = true
	f.shows["777"] = &mediux.Show{ID: "777"}
	clock.Advance(23 * time.Hour)
	show, err := c.ShowSets(ctx, "777")
	require.NoError(t, err)
	require.Equal(t, "777", show.ID)
	require.Equal(t, 2, f.calls["content"])
	require.Equal(t, 1, f.calls["show"])
}

func TestAMappingIsKeptForGoodAndAMissForADay(t *testing.T) {
	c, f, clock := harness(t)
	ctx := context.Background()
	id, err := c.ShowTMDB(ctx, "384541")
	require.NoError(t, err)
	require.Equal(t, "105248", id)
	clock.Advance(400 * 24 * time.Hour)
	id, err = c.ShowTMDB(ctx, "384541")
	require.NoError(t, err)
	require.Equal(t, "105248", id)
	require.Equal(t, 1, f.calls["tvdb"], "an id mapping is never asked twice")
	_, err = c.ShowTMDB(ctx, "1")
	require.ErrorIs(t, err, metadata.ErrNotFound)
	_, err = c.ShowTMDB(ctx, "1")
	require.ErrorIs(t, err, metadata.ErrNotFound)
	require.Equal(t, 2, f.calls["tvdb"], "a miss is asked once a day")
	clock.Advance(25 * time.Hour)
	_, _ = c.ShowTMDB(ctx, "1")
	require.Equal(t, 3, f.calls["tvdb"])
}

func TestAStaleAnswerStandsWhenMediUXCannotBeAsked(t *testing.T) {
	c, f, clock := harness(t)
	ctx := context.Background()
	_, err := c.ShowSets(ctx, "105248")
	require.NoError(t, err)
	clock.Advance(8 * 24 * time.Hour)
	f.fail = errors.New("502")
	show, err := c.ShowSets(ctx, "105248")
	require.NoError(t, err, "the week-old answer over none")
	require.Equal(t, "41471", show.Sets[0].ID)
	// Without any answer, the error is the caller's.
	_, err = c.ShowSets(ctx, "999")
	require.Error(t, err)
}

func TestACollectionIsCachedWithoutTheContentList(t *testing.T) {
	c, f, _ := harness(t)
	ctx := context.Background()
	for range 3 {
		col, err := c.CollectionSets(ctx, "404609")
		require.NoError(t, err)
		require.Equal(t, "John Wick Collection", col.Name)
	}
	require.Equal(t, 1, f.calls["collection"])
	require.Equal(t, 0, f.calls["content"], "collections are not in the content list")
}
