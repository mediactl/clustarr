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
	"cmp"
	"slices"
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Reasons are the queues, in the order a cycle serves them: the missing
// items first.
var Reasons = []schema.SearchReason{schema.SearchReasonMissing, schema.SearchReasonCutoffUnmet}

// Candidate is one backlog item as a cycle reads it: monitored, wanted for
// Reason, searched before, nothing in flight.
type Candidate struct {
	Item   schema.ItemRef
	Reason schema.SearchReason
	// LastLive is status.lastLiveSearchAt, nil when the item has none.
	LastLive *time.Time
	// LastSearched is status.lastSearchedAt, the tie-break among items
	// with no live search recorded.
	LastSearched *time.Time
	// Series and Season place an Episode in its season: its due episodes
	// may be searched as one season query. Nil for every other kind.
	Series *schema.ItemRef
	Season *int32
}

// Queue is one queue's settings.
type Queue struct {
	PerCycle int
	Cooldown time.Duration
}

// Config is a cycle's settings.
type Config struct {
	Queues map[schema.SearchReason]Queue
	// Room is how many searches the hour has left (hourlyLimit less the
	// last hour's).
	Room int
	// SeasonSearch takes two or more due episodes of a season as one unit.
	SeasonSearch bool
}

// Unit is one search a cycle starts: an item, or a season of a series.
type Unit struct {
	Reason schema.SearchReason
	Items  []Candidate
	// Series and Season make a season unit: the Series' automatic season
	// search asks for the season, and Items wait for its answer.
	Series *schema.ItemRef
	Season *int32
}

// Counts is one queue as a cycle found it.
type Counts struct {
	Candidates, Due int
	// OldestLive is the oldest last live search among the due candidates
	// that have one.
	OldestLive *time.Time
}

// Select is a cycle's choice (design §"What it is"): per queue, missing
// first, the due candidates -- past the queue's cooldown since their last
// live search -- oldest last live search first, up to the queue's perCycle
// and the hour's room in all. With SeasonSearch an Episode due beside
// another due episode of its season (not the specials) is taken with every
// such episode, of either queue, as one unit.
func Select(cands []Candidate, cfg Config, now time.Time) ([]Unit, map[schema.SearchReason]Counts) {
	counts := map[schema.SearchReason]Counts{}
	due := map[schema.SearchReason][]int{}
	for i := range cands {
		c := &cands[i]
		q, ok := cfg.Queues[c.Reason]
		if !ok {
			continue
		}
		n := counts[c.Reason]
		n.Candidates++
		if c.LastLive == nil || now.Sub(*c.LastLive) >= q.Cooldown {
			n.Due++
			due[c.Reason] = append(due[c.Reason], i)
			if c.LastLive != nil && (n.OldestLive == nil || c.LastLive.Before(*n.OldestLive)) {
				t := *c.LastLive
				n.OldestLive = &t
			}
		}
		counts[c.Reason] = n
	}

	// A season's due episodes, of both queues, by series uid and season.
	type seasonKey struct {
		series string
		season int32
	}
	seasons := map[seasonKey][]int{}
	if cfg.SeasonSearch {
		for _, r := range Reasons {
			for _, i := range due[r] {
				c := &cands[i]
				if c.Series == nil || c.Season == nil || *c.Season == 0 {
					continue
				}
				k := seasonKey{seriesKey(c.Series), *c.Season}
				seasons[k] = append(seasons[k], i)
			}
		}
	}

	var units []Unit
	taken := map[int]bool{}
	for _, r := range Reasons {
		idx := due[r]
		slices.SortStableFunc(idx, func(a, b int) int { return older(&cands[a], &cands[b]) })
		started := 0
		for _, i := range idx {
			if started >= cfg.Queues[r].PerCycle || len(units) >= cfg.Room {
				break
			}
			if taken[i] {
				continue
			}
			c := cands[i]
			u := Unit{Reason: r, Items: []Candidate{c}}
			if c.Series != nil && c.Season != nil {
				if group := seasons[seasonKey{seriesKey(c.Series), *c.Season}]; len(group) >= 2 {
					u.Items = u.Items[:0]
					for _, j := range group {
						if !taken[j] {
							u.Items = append(u.Items, cands[j])
							taken[j] = true
						}
					}
					s, n := *c.Series, *c.Season
					u.Series, u.Season = &s, &n
				}
			}
			taken[i] = true
			units = append(units, u)
			started++
		}
	}
	return units, counts
}

// older orders a before b when a's last live search is older: none first,
// then the older lastSearchedAt (none first), then the name.
func older(a, b *Candidate) int {
	if c := cmpTime(a.LastLive, b.LastLive); c != 0 {
		return c
	}
	if c := cmpTime(a.LastSearched, b.LastSearched); c != 0 {
		return c
	}
	return cmp.Or(cmp.Compare(a.Item.Kind, b.Item.Kind), cmp.Compare(a.Item.Name, b.Item.Name))
}

// cmpTime orders nil before any time, then by time.
func cmpTime(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return a.Compare(*b)
}

func seriesKey(r *schema.ItemRef) string {
	if r.UID != "" {
		return r.UID
	}
	return r.Namespace + "/" + r.Name
}
