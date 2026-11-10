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

package backlog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func movie(name string, r schema.SearchReason, live *time.Time) Candidate {
	return Candidate{Item: schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: name, UID: name}}, Reason: r, LastLive: live}
}

func episode(series string, season int32, name string, r schema.SearchReason, live *time.Time) Candidate {
	s := season
	return Candidate{
		Item:   schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: name, UID: name}},
		Reason: r, LastLive: live,
		Series: &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: series, UID: series}},
		Season: &s,
	}
}

func cfg(missing, cutoff, room int) Config {
	return Config{
		Queues: map[schema.SearchReason]Queue{
			schema.SearchReasonMissing:     {PerCycle: missing, Cooldown: 7 * 24 * time.Hour},
			schema.SearchReasonCutoffUnmet: {PerCycle: cutoff, Cooldown: 14 * 24 * time.Hour},
		},
		Room: room, SeasonSearch: true,
	}
}

func names(units []Unit) []string {
	var out []string
	for _, u := range units {
		for _, c := range u.Items {
			out = append(out, c.Item.Name)
		}
	}
	return out
}

func TestACycleTakesTheOldestLiveSearchesFirstAndSkipsTheCoolingDown(t *testing.T) {
	cands := []Candidate{
		movie("recent", schema.SearchReasonMissing, ago(24*time.Hour)), // cooling down
		movie("old", schema.SearchReasonMissing, ago(30*24*time.Hour)),
		movie("older", schema.SearchReasonMissing, ago(60*24*time.Hour)),
		movie("never", schema.SearchReasonMissing, nil),
	}
	units, counts := Select(cands, cfg(2, 0, 10), now)
	assert.Equal(t, []string{"never", "older"}, names(units), "none first, then the oldest, up to perCycle")
	assert.Equal(t, 4, counts[schema.SearchReasonMissing].Candidates)
	assert.Equal(t, 3, counts[schema.SearchReasonMissing].Due)
	assert.Equal(t, *ago(60 * 24 * time.Hour), *counts[schema.SearchReasonMissing].OldestLive)
}

func TestMissingComeFirstAndTheHourCapsTheCycle(t *testing.T) {
	cands := []Candidate{
		movie("u1", schema.SearchReasonCutoffUnmet, nil),
		movie("m1", schema.SearchReasonMissing, nil),
		movie("m2", schema.SearchReasonMissing, nil),
		movie("m3", schema.SearchReasonMissing, nil),
	}
	units, _ := Select(cands, cfg(2, 1, 10), now)
	assert.Equal(t, []string{"m1", "m2", "u1"}, names(units))
	assert.Equal(t, schema.SearchReasonCutoffUnmet, units[2].Reason)

	units, _ = Select(cands, cfg(2, 1, 1), now)
	assert.Equal(t, []string{"m1"}, names(units), "one search left this hour")
	units, _ = Select(cands, cfg(2, 1, 0), now)
	assert.Empty(t, units, "none left")
}

func TestASeasonsDueEpisodesAreOneSearch(t *testing.T) {
	cands := []Candidate{
		episode("show", 1, "s1e1", schema.SearchReasonMissing, nil),
		episode("show", 1, "s1e2", schema.SearchReasonMissing, nil),
		episode("show", 1, "s1e3", schema.SearchReasonCutoffUnmet, nil),
		episode("show", 1, "s1e4", schema.SearchReasonMissing, ago(24*time.Hour)), // cooling down
		episode("show", 2, "s2e1", schema.SearchReasonMissing, nil),               // alone in its season
		episode("show", 0, "s0e1", schema.SearchReasonMissing, nil),               // a special
		episode("show", 0, "s0e2", schema.SearchReasonMissing, nil),
	}
	units, _ := Select(cands, cfg(4, 1, 10), now)
	require.Len(t, units, 4)
	assert.Equal(t, []string{"s0e1"}, names(units[:1]), "the specials search one by one")
	assert.Nil(t, units[0].Season)
	assert.Equal(t, []string{"s0e2"}, names(units[1:2]))
	assert.Equal(t, []string{"s1e1", "s1e2", "s1e3"}, names(units[2:3]), "both queues' due episodes of the season")
	require.NotNil(t, units[2].Season)
	assert.Equal(t, int32(1), *units[2].Season)
	assert.Equal(t, "show", units[2].Series.Name)
	assert.Equal(t, []string{"s2e1"}, names(units[3:]), "alone in its season")
	assert.Equal(t, 1, countReason(units, schema.SearchReasonCutoffUnmet), "s1e3 went with its season")

	off := cfg(3, 1, 10)
	off.SeasonSearch = false
	units, _ = Select(cands, off, now)
	assert.Equal(t, []string{"s0e1", "s0e2", "s1e1", "s1e3"}, names(units), "one by one without season search: three missing, one cutoff")
}

func countReason(units []Unit, r schema.SearchReason) int {
	n := 0
	for _, u := range units {
		for _, c := range u.Items {
			if c.Reason == r {
				n++
			}
		}
	}
	return n
}

func TestAQueueOffStartsNothingButIsCounted(t *testing.T) {
	cands := []Candidate{movie("u1", schema.SearchReasonCutoffUnmet, nil)}
	units, counts := Select(cands, cfg(5, 0, 10), now)
	assert.Empty(t, units)
	assert.Equal(t, 1, counts[schema.SearchReasonCutoffUnmet].Due)
}

func TestTheBookGrantsUntilALapseAndSpends(t *testing.T) {
	b := NewBook()
	b.Grant("a", Grant{Reason: schema.SearchReasonMissing, At: now, Until: now.Add(time.Hour)})
	g, ok := b.Granted("a", now.Add(30*time.Minute))
	require.True(t, ok)
	assert.Equal(t, schema.SearchReasonMissing, g.Reason)
	_, ok = b.Granted("a", now.Add(time.Hour))
	assert.False(t, ok, "lapsed")
	b.Spend("a")
	_, ok = b.Granted("a", now)
	assert.False(t, ok, "spent")

	b.Grant("old", Grant{At: now, Until: now.Add(time.Minute)})
	b.Grant("new", Grant{At: now.Add(time.Hour), Until: now.Add(2 * time.Hour)})
	assert.Equal(t, 1, b.Len(), "a grant prunes the lapsed ones")

	var nilBook *Book
	_, ok = nilBook.Granted("a", now)
	assert.False(t, ok)
	nilBook.Spend("a")
	require.NoError(t, nilBook.Wake(context.Background(), schema.ItemRef{}))
	assert.Nil(t, nilBook.Wakes())
}
