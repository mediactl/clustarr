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

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestAgentCacheNeverHoldsSecretsOrConfigMaps: no domain lists or watches a
// Secret or a ConfigMap -- every read is a by-name Get, which the agent roles
// allow, where an informer would need list and watch grants no agent role
// holds (§3.5.2 step 5). And an agent never elects (R9).
func TestAgentCacheNeverHoldsSecretsOrConfigMaps(t *testing.T) {
	o := DefaultOptions()
	o.Domain = "import"
	opts := managerOptions(o)
	require.NotNil(t, opts.Client.Cache)
	var kinds []string
	for _, obj := range opts.Client.Cache.DisableFor {
		kinds = append(kinds, fmt.Sprintf("%T", obj))
	}
	assert.ElementsMatch(t, []string{"*v1.Secret", "*v1.ConfigMap"}, kinds)
	assert.False(t, opts.LeaderElection)
	require.NotNil(t, opts.Controller.NeedLeaderElection)
	assert.False(t, *opts.Controller.NeedLeaderElection)
	assert.Nil(t, opts.Controller.SkipNameValidation, "production validates controller names")

	o.SkipNameValidation = true
	opts = managerOptions(o)
	require.NotNil(t, opts.Controller.SkipNameValidation)
	assert.True(t, *opts.Controller.SkipNameValidation, "the test seam reaches the manager")
}

// TestDrainDefaultsToTheLongestAckWait: a domain's drain is the longest
// declared AckWait among its durables (§3.5.6), so a handler mid-message at
// shutdown keeps its context as long as the server would wait for its ack.
// The engines own no durable.
func TestDrainDefaultsToTheLongestAckWait(t *testing.T) {
	for domain, want := range map[string]time.Duration{
		"catalog": 120 * time.Second, "caption": 90 * time.Second, "metadata": 60 * time.Second,
		"import": 60 * time.Second, "index": 60 * time.Second, "events": 30 * time.Second,
		"torrent-engine": 0, "usenet-engine": 0,
	} {
		assert.Equal(t, want, drainFor(domain), domain)
	}

	// The command applies it, and graceful shutdown is drain + 15 s, unless
	// either is given.
	var got Options
	run := func(_ context.Context, o Options) error { got = o; return nil }
	for _, tc := range []struct {
		args            []string
		drain, graceful time.Duration
	}{
		{args: []string{"--domain=catalog"}, drain: 120 * time.Second, graceful: 135 * time.Second},
		{args: []string{"--domain=catalog", "--drain-timeout=5s"}, drain: 5 * time.Second, graceful: 20 * time.Second},
		{args: []string{"--domain=catalog", "--graceful-shutdown-timeout=7s"}, drain: 120 * time.Second, graceful: 7 * time.Second},
	} {
		t.Setenv("POD_NAMESPACE", "default")
		cmd := NewCommandWith(run)
		cmd.SetArgs(tc.args)
		require.NoError(t, cmd.Execute(), "%v", tc.args)
		assert.Equal(t, tc.drain, got.DrainTimeout, "%v", tc.args)
		assert.Equal(t, tc.graceful, got.GracefulShutdownTimeout, "%v", tc.args)
	}
}

// recordingBus records every subscription made through it.
type recordingBus struct {
	events.Bus
	subs []events.Subscription
}

func (b *recordingBus) Subscribe(_ context.Context, s events.Subscription, _ events.Handler) (func(), error) {
	b.subs = append(b.subs, s)
	return func() {}, nil
}

// TestDomainBusAppliesDrainAndSlotOverrides: every subscription a domain
// makes gets the domain's drain unless it names its own, and
// CLUSTARR_CONSUMER_SLOTS overrides its durable's slots (§9.2).
func TestDomainBusAppliesDrainAndSlotOverrides(t *testing.T) {
	rec := &recordingBus{}
	b := domainBus{Bus: rec, drain: 90 * time.Second, slots: map[string]int{events.ConsumerCaptionFetchHigh: 3}}
	_, err := b.Subscribe(context.Background(), events.Subscription{Durable: events.ConsumerCaptionFetchHigh, MaxInFlight: 1}, nil)
	require.NoError(t, err)
	_, err = b.Subscribe(context.Background(), events.Subscription{Durable: events.ConsumerCaptionFetchNormal, MaxInFlight: 1, Drain: time.Second}, nil)
	require.NoError(t, err)
	require.Len(t, rec.subs, 2)
	assert.Equal(t, 90*time.Second, rec.subs[0].Drain)
	assert.Equal(t, 3, rec.subs[0].MaxInFlight, "CLUSTARR_CONSUMER_SLOTS overrides the durable's Slots")
	assert.Equal(t, time.Second, rec.subs[1].Drain, "an explicit Drain stays")
	assert.Equal(t, 1, rec.subs[1].MaxInFlight)
}

// TestEngineClientSplitsAtTheLastHyphen: the engine identity's client half
// is everything before the last hyphen, since a client's own name may hold
// hyphens (moved with app/grab's splitEngineIdentity).
func TestEngineClientSplitsAtTheLastHyphen(t *testing.T) {
	for engine, want := range map[string]string{
		"torrents-0": "torrents", "sabnzbd-0": "sabnzbd", "my-torrent-client-12": "my-torrent-client",
	} {
		got, err := engineClient(engine)
		require.NoError(t, err, engine)
		assert.Equal(t, want, got, engine)
	}
	for _, bad := range []string{"", "noordinal", "-0", "torrents-"} {
		_, err := engineClient(bad)
		assert.Error(t, err, bad)
	}
}

// TestPieceCompletionMatchesTheBuild: R13 on the built artifact -- the
// constant --self-check reports agrees with what anacrolix links, so a
// cgo-free build, under which every seeding torrent would rehash after a
// restart, cannot report sqlite.
func TestPieceCompletionMatchesTheBuild(t *testing.T) {
	pc, err := storage.NewDefaultPieceCompletionForDir(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	assert.Equal(t, pieceCompletion == "sqlite", strings.Contains(strings.ToLower(fmt.Sprintf("%T", pc)), "sqlite"), "%T", pc)
}
