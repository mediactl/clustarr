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
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var threeServers = []string{"nats-0", "nats-1", "nats-2"}

func others(leader string) []string {
	var out []string
	for _, s := range threeServers {
		if s != leader {
			out = append(out, s)
		}
	}
	return out
}

func asset(stream, leader string) leaderAsset {
	return leaderAsset{Key: leaderKey{Stream: stream}, Leader: leader, Ready: others(leader)}
}

// spread is the gap between the most and least loaded servers.
func spread(loads map[string]float64) float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range threeServers {
		lo, hi = min(lo, loads[s]), max(hi, loads[s])
	}
	return hi - lo
}

// apply plays moves onto assets.
func apply(assets []leaderAsset, moves []leaderMove) []leaderAsset {
	out := append([]leaderAsset(nil), assets...)
	for _, m := range moves {
		for i := range out {
			if out[i].Key == m.Key {
				out[i].Leader = m.To
			}
		}
	}
	return out
}

// A stream's traffic is its appends per second between samples, folded into
// an hourly EWMA: the first interval seeds it, a quiet interval decays it by
// the half-life's share, and a counter that went backwards (a re-created
// stream) changes nothing.
func TestTheLeaderMeterMeasuresTrafficAsAnHourlyEWMA(t *testing.T) {
	m := newLeaderMeter()
	t0 := time.Unix(1_800_000_000, 0)
	k := leaderKey{Stream: "KV_clustarr-progress"}
	at := func(d time.Duration, n uint64) { m.observe(t0.Add(d), []leaderAsset{{Key: k, Counter: n}}) }

	at(0, 1000)
	require.False(t, m.ready(), "one sample is no interval")
	at(5*time.Minute, 1000+2400)
	require.True(t, m.ready())
	require.InDelta(t, 8.0, m.rate[k], 1e-9, "2,400 appends in 300 s seeds 8/s")

	at(10*time.Minute, 3400)
	alpha := 1 - math.Exp2(-300.0/3600)
	assert.InDelta(t, 8.0*(1-alpha), m.rate[k], 1e-9, "a quiet interval decays by one twelfth of the half-life")

	before := m.rate[k]
	at(15*time.Minute, 10)
	assert.Equal(t, before, m.rate[k], "a counter that went backwards is a new stream, not negative traffic")
}

// The three thirds -- traffic, bytes, being one more group -- each sum to one
// across the assets, so the weights do too, and an asset gone from a sample
// is forgotten.
func TestLeaderWeightsAreSharesOfTrafficBytesAndCount(t *testing.T) {
	m := newLeaderMeter()
	t0 := time.Unix(1_800_000_000, 0)
	progress := leaderAsset{Key: leaderKey{Stream: "KV_clustarr-progress"}}
	artwork := leaderAsset{Key: leaderKey{Stream: "OBJ_clustarr-artwork"}, Bytes: 3 << 30}
	durable := leaderAsset{Key: leaderKey{Stream: "CLUSTARR_WORK_PROBE", Consumer: "import-probe-high"}}
	m.observe(t0, []leaderAsset{progress, artwork, durable})
	progress.Counter, durable.Counter = 300*9, 300
	m.observe(t0.Add(5*time.Minute), []leaderAsset{progress, artwork, durable})

	w := m.weights([]leaderAsset{progress, artwork, durable})
	assert.InDelta(t, 1.0, w[progress.Key]+w[artwork.Key]+w[durable.Key], 1e-9)
	assert.InDelta(t, (0.9+0+1.0/3)/3, w[progress.Key], 1e-9, "nine tenths of the traffic")
	assert.InDelta(t, (0+1+1.0/3)/3, w[artwork.Key], 1e-9, "all of the bytes")
	assert.InDelta(t, (0.1+0+1.0/3)/3, w[durable.Key], 1e-9, "a tenth of the traffic")

	m.observe(t0.Add(10*time.Minute), []leaderAsset{progress})
	assert.NotContains(t, m.rate, artwork.Key)
	assert.NotContains(t, m.last, durable.Key)
}

// talos on 2026-11-03: nats-1 led the 2.3 GiB of object stores, nats-2 the
// telemetry bucket that takes nearly every write, nats-0 little but many
// small groups. The plan spreads the load to within tolerance, never moves
// the telemetry bucket (heavier than any gap it could close), and leaves the
// two heavy assets apart.
func TestPlanLeaderMovesSpreadsTheMeasuredLoad(t *testing.T) {
	var assets []leaderAsset
	weight := map[leaderKey]float64{}
	add := func(a leaderAsset, w float64) {
		assets = append(assets, a)
		weight[a.Key] = w
	}
	add(asset("KV_clustarr-progress", "nats-2"), 0.32)
	add(asset("OBJ_clustarr-artwork", "nats-1"), 0.22)
	add(asset("OBJ_clustarr-fingerprints", "nats-2"), 0.10)
	for i := range 18 {
		add(asset(fmt.Sprintf("KV_small-%d", i), "nats-0"), 0.008)
	}
	for i := range 12 {
		add(asset(fmt.Sprintf("CLUSTARR_WORK_%d", i), "nats-1"), 0.012)
	}
	before := serverLoads(assets, weight)
	moves := planLeaderMoves(assets, weight, threeServers, 32, 0.05, nil)
	require.NotEmpty(t, moves)
	after := serverLoads(apply(assets, moves), weight)
	assert.Less(t, spread(after), spread(before))
	assert.LessOrEqual(t, spread(after), 0.05+1e-9, "loads %v", after)
	for _, m := range moves {
		assert.NotEqual(t, "KV_clustarr-progress", m.Key.Stream, "the telemetry bucket stays where it is")
		assert.Contains(t, others(m.From), m.To)
	}
	final := apply(assets, moves)
	leaderOf := func(stream string) string {
		for _, a := range final {
			if a.Key.Stream == stream {
				return a.Leader
			}
		}
		return ""
	}
	assert.NotEqual(t, leaderOf("KV_clustarr-progress"), leaderOf("OBJ_clustarr-fingerprints"),
		"the two heaviest of nats-2's assets end on different servers")
}

// After a restart the server that came back leads nothing: the plan gives it
// a third, from both of the others.
func TestPlanLeaderMovesFillsAServerThatCameBackEmpty(t *testing.T) {
	var assets []leaderAsset
	weight := map[leaderKey]float64{}
	for i := range 30 {
		a := asset(fmt.Sprintf("S%02d", i), threeServers[1+i%2])
		assets = append(assets, a)
		weight[a.Key] = 1.0 / 30
	}
	moves := planLeaderMoves(assets, weight, threeServers, 32, 0.05, nil)
	after := serverLoads(apply(assets, moves), weight)
	assert.InDelta(t, 1.0/3, after["nats-0"], 0.05)
	assert.LessOrEqual(t, spread(after), 0.05+1e-9, "loads %v", after)
	assert.Len(t, moves, 10, "a third of thirty, one move each")
}

// Balanced loads move nothing, and so does a gap only a move to a follower
// that is not current could close, an asset held after a recent move, or one
// too heavy to close it; maxMoves bounds a pass.
func TestPlanLeaderMovesRespectsHealthHoldsAndBounds(t *testing.T) {
	even := []leaderAsset{asset("A", "nats-0"), asset("B", "nats-1"), asset("C", "nats-2")}
	w := map[leaderKey]float64{{Stream: "A"}: 1.0 / 3, {Stream: "B"}: 1.0 / 3, {Stream: "C"}: 1.0 / 3}
	assert.Empty(t, planLeaderMoves(even, w, threeServers, 8, 0.05, nil), "balanced")

	skew := func() ([]leaderAsset, map[leaderKey]float64) {
		var a []leaderAsset
		w := map[leaderKey]float64{}
		for i := range 9 {
			x := asset(fmt.Sprintf("S%d", i), "nats-0")
			a = append(a, x)
			w[x.Key] = 1.0 / 9
		}
		return a, w
	}
	a, w := skew()
	for i := range a {
		a[i].Ready = nil
	}
	assert.Empty(t, planLeaderMoves(a, w, threeServers, 8, 0.05, nil), "no follower is current")

	a, w = skew()
	assert.Empty(t, planLeaderMoves(a, w, threeServers, 8, 0.05, func(leaderKey) bool { return true }), "all held")

	a, w = skew()
	assert.Len(t, planLeaderMoves(a, w, threeServers, 2, 0.05, nil), 2, "maxMoves")

	heavy := []leaderAsset{asset("HEAVY", "nats-0"), asset("B", "nats-1"), asset("C", "nats-2")}
	hw := map[leaderKey]float64{{Stream: "HEAVY"}: 0.6, {Stream: "B"}: 0.2, {Stream: "C"}: 0.2}
	assert.Empty(t, planLeaderMoves(heavy, hw, threeServers, 8, 0.05, nil),
		"moving the one heavy asset would only open the gap the other way")
}

// A durable whose leader moves under a waiting pull ends that pull with
// ErrConsumerLeadershipChanged (TestAConsumerStepdownEndsAWaitingPull shows
// nats.go reports it so). The subscription pulls again at once rather than
// logging a failure and waiting fetchRetry.
func TestALeadershipChangeIsABenignFetchEnd(t *testing.T) {
	assert.True(t, benignFetchError(fmt.Errorf("fetch: %w", jetstream.ErrConsumerLeadershipChanged)))
	assert.False(t, consumerGone(jetstream.ErrConsumerLeadershipChanged), "the durable is still there")
}
