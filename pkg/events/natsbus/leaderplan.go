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

package natsbus

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// The leader balancer's arithmetic, kept pure so it is testable without a
// cluster. Every asset is R3 on three servers, so every server holds every
// stream and the leaders are the only thing to spread (owner, 2026-11-03).
// An asset's weight is measured, never declared: a third for its traffic
// (messages appended to a stream, or delivered by a durable, per second, as
// an hourly EWMA), a third for its stored bytes (an object store's reads and
// listings land on its leader: listing clustarr-fingerprints took its leader
// from 399 to 935 MiB, 2026-10-27), and a third for being one more RAFT
// group. Each third is the asset's share of that dimension, so the weights
// sum to one and a server's load is the share of the work it leads.

// leaderKey names a stream, or a durable consumer on one.
type leaderKey struct {
	Stream, Consumer string
}

func (k leaderKey) kind() string {
	if k.Consumer != "" {
		return "consumer"
	}
	return "stream"
}

func (k leaderKey) String() string {
	if k.Consumer != "" {
		return k.Stream + "/" + k.Consumer
	}
	return k.Stream
}

// leaderAsset is one replicated asset as a pass sees it.
type leaderAsset struct {
	Key    leaderKey
	Leader string
	// Ready are the followers a leadership may move to: current and online.
	Ready []string
	// Counter is the stream's last sequence or the durable's delivered
	// consumer sequence; Bytes the stream's stored bytes (0 for a durable).
	Counter, Bytes uint64
}

// leaderMove moves Key's leadership From one server To another.
type leaderMove struct {
	Key      leaderKey
	From, To string
	Weight   float64
}

// leaderHalfLife is the EWMA half-life of an asset's measured traffic: long
// enough that a quiet night does not forget the day's grab storms, short
// enough that a new heavy writer counts within the day.
const leaderHalfLife = time.Hour

// leaderMeter measures each asset's traffic between samples.
type leaderMeter struct {
	last map[leaderKey]meterSample
	rate map[leaderKey]float64
}

type meterSample struct {
	at      time.Time
	counter uint64
}

func newLeaderMeter() *leaderMeter {
	return &leaderMeter{last: map[leaderKey]meterSample{}, rate: map[leaderKey]float64{}}
}

// observe folds one sample of every asset into its traffic rate. A counter
// that went backwards is a re-created asset: it restarts from that sample.
func (m *leaderMeter) observe(at time.Time, assets []leaderAsset) {
	seen := make(map[leaderKey]bool, len(assets))
	for _, a := range assets {
		seen[a.Key] = true
		prev, ok := m.last[a.Key]
		m.last[a.Key] = meterSample{at: at, counter: a.Counter}
		dt := at.Sub(prev.at).Seconds()
		if !ok || dt <= 0 || a.Counter < prev.counter {
			continue
		}
		r := float64(a.Counter-prev.counter) / dt
		old, measured := m.rate[a.Key]
		if !measured {
			m.rate[a.Key] = r
			continue
		}
		alpha := 1 - math.Exp2(-dt/leaderHalfLife.Seconds())
		m.rate[a.Key] = old + alpha*(r-old)
	}
	for k := range m.last {
		if !seen[k] {
			delete(m.last, k)
			delete(m.rate, k)
		}
	}
}

// ready reports whether any traffic has been measured: a pass needs one
// interval between two samples first.
func (m *leaderMeter) ready() bool { return len(m.rate) > 0 }

// weights is each asset's weight. An asset sampled once, which has no rate
// yet, counts as no traffic.
func (m *leaderMeter) weights(assets []leaderAsset) map[leaderKey]float64 {
	var traffic, bytes float64
	for _, a := range assets {
		traffic += m.rate[a.Key]
		bytes += float64(a.Bytes)
	}
	share := func(x, total float64) float64 {
		if total <= 0 {
			return 0
		}
		return x / total
	}
	out := make(map[leaderKey]float64, len(assets))
	for _, a := range assets {
		out[a.Key] = (share(m.rate[a.Key], traffic) + share(float64(a.Bytes), bytes) + share(1, float64(len(assets)))) / 3
	}
	return out
}

// planLeaderMoves moves leaderships off the most loaded server until no two
// servers' loads differ by more than tolerance, at most maxMoves, each to a
// server that is ready to lead the asset. Of the assets that would help, it
// moves the one nearest half the gap, so each move closes as much of it as
// one asset can, and it never moves one that would only open the gap the
// other way. held skips assets moved recently.
func planLeaderMoves(assets []leaderAsset, weight map[leaderKey]float64, servers []string,
	maxMoves int, tolerance float64, held func(leaderKey) bool,
) []leaderMove {
	load := make(map[string]float64, len(servers))
	for _, s := range servers {
		load[s] = 0
	}
	leader := make(map[leaderKey]string, len(assets))
	for _, a := range assets {
		leader[a.Key] = a.Leader
		load[a.Leader] += weight[a.Key]
	}
	byLoad := func() []string {
		names := make([]string, 0, len(load))
		for s := range load {
			names = append(names, s)
		}
		slices.SortFunc(names, func(a, b string) int {
			return cmp.Or(cmp.Compare(load[a], load[b]), cmp.Compare(a, b))
		})
		return names
	}
	moved := map[leaderKey]bool{}
	var moves []leaderMove
	for len(moves) < maxMoves {
		order := byLoad()
		if len(order) < 2 {
			break
		}
		hi := order[len(order)-1]
		if load[hi]-load[order[0]] <= tolerance {
			break
		}
		var pick *leaderMove
		for _, to := range order[:len(order)-1] {
			gap := load[hi] - load[to]
			best := math.Inf(1)
			for _, a := range assets {
				w := weight[a.Key]
				if leader[a.Key] != hi || moved[a.Key] || w <= 0 || w >= gap ||
					!slices.Contains(a.Ready, to) || (held != nil && held(a.Key)) {
					continue
				}
				if d := math.Abs(gap/2 - w); d < best {
					best = d
					pick = &leaderMove{Key: a.Key, From: hi, To: to, Weight: w}
				}
			}
			if pick != nil {
				break
			}
		}
		if pick == nil {
			break
		}
		moves = append(moves, *pick)
		moved[pick.Key] = true
		leader[pick.Key] = pick.To
		load[pick.From] -= pick.Weight
		load[pick.To] += pick.Weight
	}
	return moves
}

// serverLoads is each server's share of the weight it leads.
func serverLoads(assets []leaderAsset, weight map[leaderKey]float64) map[string]float64 {
	out := map[string]float64{}
	for _, a := range assets {
		out[a.Leader] += weight[a.Key]
	}
	return out
}
