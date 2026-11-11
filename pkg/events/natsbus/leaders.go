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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// Leader balancing (owner, 2026-11-03: "Explore evenly distributing our
// NATS.io streams and consumers over the three replicas from the manager";
// measured weights, after restarts, no system-account credentials).
// nats-server places replicas but never moves a leader, so whichever server
// comes up first after a restart leads most of the cluster: on 2026-10-27
// nats-2 led 26 of 44 streams, the fingerprint store among them, and on
// 2026-11-03 nats-1 led 2.3 GiB to nats-0's 135 MiB while nats-2 took every
// telemetry write. The account-level stepdown requests name a preferred
// leader (nats-server 2.11+, jetstream_api.go getStepDownPreferredPlacement),
// which RAFT hands over gracefully when that follower is healthy; the meta
// leader's stepdown needs the system account, which clustarr's clients do
// not hold, so it is left where it is.

// LeaderBalance tunes [Bus.BalanceLeaders].
type LeaderBalance struct {
	// SampleEvery is how often every asset's traffic is sampled.
	SampleEvery time.Duration
	// Settle is how long leader elections nobody asked for -- a server
	// restarted -- must have been quiet before a pass.
	Settle time.Duration
	// MaxMoves bounds the moves one pass makes.
	MaxMoves int
	// Tolerance is the spread of the servers' loads, as a share of all the
	// weight, below which nothing moves.
	Tolerance float64
	// Hold keeps a moved asset where it was put for this long.
	Hold time.Duration
	// Confirm is how long a move may take to show its new leader.
	Confirm time.Duration
}

// DefaultLeaderBalance is what the manager runs with.
func DefaultLeaderBalance() LeaderBalance {
	return LeaderBalance{
		SampleEvery: 5 * time.Minute,
		Settle:      2 * time.Minute,
		MaxMoves:    8,
		Tolerance:   0.05,
		Hold:        30 * time.Minute,
		Confirm:     15 * time.Second,
	}
}

const (
	// leaderCallTimeout bounds one JetStream API call: during an election a
	// request can go unanswered.
	leaderCallTimeout = 5 * time.Second
	// leaderPoll is how often a move polls for its new leader.
	leaderPoll = 250 * time.Millisecond

	advisoryStreamElected   = "$JS.EVENT.ADVISORY.STREAM.LEADER_ELECTED.>"
	advisoryConsumerElected = "$JS.EVENT.ADVISORY.CONSUMER.LEADER_ELECTED.>"
)

// BalanceLeaders keeps the leaderships of t's replicated streams, buckets,
// object stores and their durables spread over the NATS servers while ctx
// lives: the manager's leader-only runnable, beside KeepTopology. It samples
// every asset each o.SampleEvery to measure its traffic, and passes once
// the leader elections it did not ask for have been quiet for o.Settle -- a
// NATS server restarted -- and once after its own start, since whatever the
// servers did meanwhile is unknown. A pass moves at most o.MaxMoves
// leaderships, one at a time, each to a follower that is current, until the
// servers' measured loads are within o.Tolerance. Failures are logged and
// left for the next pass, never returned.
func (b *Bus) BalanceLeaders(ctx context.Context, t events.Topology, o LeaderBalance) error {
	bal := &balancer{
		b: b, t: t, o: o, meter: newLeaderMeter(),
		expect: map[leaderKey]expectedLeader{}, moved: map[leaderKey]time.Time{},
		dirty: true,
	}
	for _, subj := range []string{advisoryStreamElected, advisoryConsumerElected} {
		sub, err := b.nc.Subscribe(subj, bal.onElected)
		if err != nil {
			return fmt.Errorf("natsbus: subscribe to %s: %w", subj, err)
		}
		defer func() { _ = sub.Unsubscribe() }()
	}
	log := logging.FromContext(ctx)
	bal.sample(ctx)
	sampleTick := time.NewTicker(o.SampleEvery)
	defer sampleTick.Stop()
	checkTick := time.NewTicker(max(o.Settle/8, time.Second))
	defer checkTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sampleTick.C:
			bal.sample(ctx)
		case <-checkTick.C:
			if bal.due(time.Now()) {
				if err := bal.pass(ctx); err != nil && ctx.Err() == nil {
					log.Warn("bus: balancing the JetStream leaders failed; the next election or start retries", "err", err)
				}
			}
		}
	}
}

// expectedLeader is a move the balancer asked for, whose election it ignores.
type expectedLeader struct {
	to    string
	until time.Time
}

type balancer struct {
	b     *Bus
	t     events.Topology
	o     LeaderBalance
	meter *leaderMeter

	mu           sync.Mutex
	expect       map[leaderKey]expectedLeader
	moved        map[leaderKey]time.Time
	dirty        bool
	lastElection time.Time
}

// onElected marks the balance dirty for a leader election it did not ask for.
// An R1 asset -- an ordered consumer behind a KV watch or an object read,
// made by the hundred -- has no replicas and is never balanced, so its
// elections are ignored.
func (bal *balancer) onElected(m *nats.Msg) {
	var adv struct {
		Stream   string            `json:"stream"`
		Consumer string            `json:"consumer"`
		Leader   string            `json:"leader"`
		Replicas []json.RawMessage `json:"replicas"`
	}
	if json.Unmarshal(m.Data, &adv) != nil || len(adv.Replicas) == 0 {
		return
	}
	k := leaderKey{Stream: adv.Stream, Consumer: adv.Consumer}
	now := time.Now()
	bal.mu.Lock()
	defer bal.mu.Unlock()
	if e, ok := bal.expect[k]; ok && e.to == adv.Leader && now.Before(e.until) {
		delete(bal.expect, k)
		return
	}
	bal.dirty, bal.lastElection = true, now
}

// due reports, and clears, a pass that is owed and has settled.
func (bal *balancer) due(now time.Time) bool {
	bal.mu.Lock()
	defer bal.mu.Unlock()
	if !bal.dirty || !bal.meter.ready() || now.Sub(bal.lastElection) < bal.o.Settle {
		return false
	}
	bal.dirty = false
	return true
}

// sample measures every asset once and publishes the leader gauges.
func (bal *balancer) sample(ctx context.Context) {
	assets, servers, err := bal.b.leaderAssets(ctx, bal.t)
	if err != nil {
		if ctx.Err() == nil {
			logging.FromContext(ctx).Debug("bus: sampling the JetStream leaders failed", "err", err)
		}
		return
	}
	bal.mu.Lock()
	bal.meter.observe(time.Now(), assets)
	weight := bal.meter.weights(assets)
	bal.mu.Unlock()
	publishLeaders(assets, servers, weight)
}

// pass moves leaderships until the servers' loads are within tolerance.
func (bal *balancer) pass(ctx context.Context) error {
	log := logging.FromContext(ctx)
	assets, servers, err := bal.b.leaderAssets(ctx, bal.t)
	if err != nil {
		return err
	}
	now := time.Now()
	bal.mu.Lock()
	bal.meter.observe(now, assets)
	weight := bal.meter.weights(assets)
	held := func(k leaderKey) bool { return now.Sub(bal.moved[k]) < bal.o.Hold }
	moves := planLeaderMoves(assets, weight, servers, bal.o.MaxMoves, bal.o.Tolerance, held)
	bal.mu.Unlock()
	before := serverLoads(assets, weight)
	if len(moves) == 0 {
		publishLeaders(assets, servers, weight)
		log.Debug("bus: the JetStream leaders are balanced", "loads", roundLoads(before))
		return nil
	}
	moved := 0
	for _, m := range moves {
		result := bal.move(ctx, m)
		metrics.NATSLeaderMovesTotal.WithLabelValues(m.Key.kind(), result).Inc()
		if result != "moved" {
			log.Warn("bus: a JetStream leadership did not move", "asset", m.Key.String(),
				"from", m.From, "to", m.To, "result", result)
			continue
		}
		moved++
		for i := range assets {
			if assets[i].Key == m.Key {
				assets[i].Leader = m.To
			}
		}
	}
	publishLeaders(assets, servers, weight)
	log.Info("bus: balanced the JetStream leaders", "planned", len(moves), "moved", moved,
		"before", roundLoads(before), "after", roundLoads(serverLoads(assets, weight)))
	return nil
}

// move asks m.Key's leader to hand over to m.To and waits for the election.
func (bal *balancer) move(ctx context.Context, m leaderMove) string {
	bal.mu.Lock()
	bal.expect[m.Key] = expectedLeader{to: m.To, until: time.Now().Add(bal.o.Confirm + time.Minute)}
	bal.moved[m.Key] = time.Now()
	bal.mu.Unlock()
	if err := bal.b.stepDown(ctx, m.Key, m.To); err != nil {
		var refused *stepDownRefused
		if errors.As(err, &refused) {
			logging.FromContext(ctx).Debug("bus: stepdown refused", "asset", m.Key.String(), "err", err)
			return "refused"
		}
		logging.FromContext(ctx).Debug("bus: stepdown failed", "asset", m.Key.String(), "err", err)
		return "failed"
	}
	deadline := time.Now().Add(bal.o.Confirm)
	for {
		leader, err := bal.b.leaderOf(ctx, m.Key)
		switch {
		case err == nil && leader == m.To:
			return "moved"
		case err == nil && leader != "" && leader != m.From:
			return "elsewhere"
		}
		if time.Now().After(deadline) {
			return "unconfirmed"
		}
		select {
		case <-ctx.Done():
			return "unconfirmed"
		case <-time.After(leaderPoll):
		}
	}
}

// leaderStreams are the stream names t's streams, buckets and object stores
// live in.
func leaderStreams(t events.Topology) []string {
	var out []string
	for _, o := range t.Objects() {
		var name string
		switch o.Kind {
		case events.TopologyStream:
			name = o.Name
		case events.TopologyBucket:
			name = "KV_" + o.Name
		case events.TopologyObjectStore:
			name = "OBJ_" + o.Name
		default:
			continue
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// leaderAssets reads every replicated stream of t and its replicated
// durables, and every server any of them names. A stream not on the broker
// is KeepTopology's to re-create, so it is passed over.
func (b *Bus) leaderAssets(ctx context.Context, t events.Topology) ([]leaderAsset, []string, error) {
	var assets []leaderAsset
	var servers []string
	note := func(c *jetstream.ClusterInfo) []string {
		if !slices.Contains(servers, c.Leader) {
			servers = append(servers, c.Leader)
		}
		var ready []string
		for _, p := range c.Replicas {
			if p == nil {
				continue
			}
			if !slices.Contains(servers, p.Name) {
				servers = append(servers, p.Name)
			}
			if p.Current && !p.Offline {
				ready = append(ready, p.Name)
			}
		}
		return ready
	}
	for _, name := range leaderStreams(t) {
		cctx, cancel := context.WithTimeout(ctx, leaderCallTimeout)
		s, err := b.js.Stream(cctx, name)
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			cancel()
			continue
		}
		if err != nil {
			cancel()
			return nil, nil, fmt.Errorf("natsbus: stream %s: %w", name, err)
		}
		info := s.CachedInfo()
		if info.Cluster == nil || info.Cluster.Leader == "" || len(info.Cluster.Replicas) == 0 {
			cancel()
			continue
		}
		assets = append(assets, leaderAsset{
			Key: leaderKey{Stream: name}, Leader: info.Cluster.Leader, Ready: note(info.Cluster),
			Counter: info.State.LastSeq, Bytes: info.State.Bytes,
		})
		lister := s.ListConsumers(cctx)
		for ci := range lister.Info() {
			if ci.Config.Durable == "" || ci.Cluster == nil || ci.Cluster.Leader == "" || len(ci.Cluster.Replicas) == 0 {
				continue
			}
			assets = append(assets, leaderAsset{
				Key: leaderKey{Stream: name, Consumer: ci.Name}, Leader: ci.Cluster.Leader,
				Ready: note(ci.Cluster), Counter: ci.Delivered.Consumer,
			})
		}
		err = lister.Err()
		cancel()
		if err != nil {
			return nil, nil, fmt.Errorf("natsbus: consumers of %s: %w", name, err)
		}
	}
	slices.Sort(servers)
	return assets, servers, nil
}

// leaderOf reads k's current leader.
func (b *Bus) leaderOf(ctx context.Context, k leaderKey) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, leaderCallTimeout)
	defer cancel()
	var c *jetstream.ClusterInfo
	if k.Consumer != "" {
		cons, err := b.js.Consumer(cctx, k.Stream, k.Consumer)
		if err != nil {
			return "", err
		}
		c = cons.CachedInfo().Cluster
	} else {
		s, err := b.js.Stream(cctx, k.Stream)
		if err != nil {
			return "", err
		}
		c = s.CachedInfo().Cluster
	}
	if c == nil {
		return "", nil
	}
	return c.Leader, nil
}

// stepDownRefused is the server's own refusal of a stepdown.
type stepDownRefused struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *stepDownRefused) Error() string {
	return fmt.Sprintf("stepdown refused: %s (%d/%d)", e.Description, e.Code, e.ErrCode)
}

// stepDown asks k's leader to hand its leadership to the server named to.
func (b *Bus) stepDown(ctx context.Context, k leaderKey, to string) error {
	subj := b.apiPrefix() + "STREAM.LEADER.STEPDOWN." + k.Stream
	if k.Consumer != "" {
		subj = b.apiPrefix() + "CONSUMER.LEADER.STEPDOWN." + k.Stream + "." + k.Consumer
	}
	req, err := json.Marshal(map[string]any{"placement": map[string]string{"preferred": to}})
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, leaderCallTimeout)
	defer cancel()
	msg, err := b.nc.RequestWithContext(cctx, subj, req)
	if err != nil {
		return err
	}
	var resp struct {
		Success bool             `json:"success"`
		Error   *stepDownRefused `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return fmt.Errorf("stepdown answer: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if !resp.Success {
		return errors.New("stepdown answered without success")
	}
	return nil
}

// apiPrefix is the JetStream API prefix the bus's own context uses.
func (b *Bus) apiPrefix() string {
	switch {
	case b.opts.domain != "":
		return "$JS." + b.opts.domain + ".API."
	case b.opts.apiPrefix != "":
		return strings.TrimSuffix(b.opts.apiPrefix, ".") + "."
	default:
		return jetstream.DefaultAPIPrefix
	}
}

// publishLeaders sets the leader count and load gauges from one sample.
func publishLeaders(assets []leaderAsset, servers []string, weight map[leaderKey]float64) {
	metrics.NATSLeaders.Reset()
	metrics.NATSLeaderLoadRatio.Reset()
	loads := serverLoads(assets, weight)
	for _, s := range servers {
		metrics.NATSLeaders.WithLabelValues(s, "stream").Set(0)
		metrics.NATSLeaders.WithLabelValues(s, "consumer").Set(0)
		metrics.NATSLeaderLoadRatio.WithLabelValues(s).Set(loads[s])
	}
	for _, a := range assets {
		metrics.NATSLeaders.WithLabelValues(a.Leader, a.Key.kind()).Inc()
	}
}

// roundLoads is loads to two places, for a log line.
func roundLoads(loads map[string]float64) map[string]string {
	out := make(map[string]string, len(loads))
	for s, l := range loads {
		out[s] = fmt.Sprintf("%.2f", l)
	}
	return out
}
