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

package tuner

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

func renderGen(p *provider) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renderGen
}

// The same channels in another order are the same configuration: the
// cache's index lists a provider's channels in no order, and every reconcile
// of an unchanged provider rendered the guide again (talos, 2026-11-03: a
// 3.7 GB render every ten minutes).
func TestTheSameChannelsInAnotherOrderRenderNothing(t *testing.T) {
	key := types.NamespacedName{Namespace: "media", Name: "news"}
	tn := New(Options{Debounce: time.Millisecond})
	a := ChannelConfig{Object: "a", Key: "t:a", Number: "1", Active: true}
	b := ChannelConfig{Object: "b", Key: "t:b", Number: "2", Active: true, Map: &xmltv.Mapping{Guide: GuideName, ChannelID: "b"}}
	tn.Apply(Config{Key: key, Tuners: 1, Channels: []ChannelConfig{a, b}})
	p := tn.providers[key]
	require.Eventually(t, func() bool { return renderGen(p) == 1 }, 2*time.Second, time.Millisecond)

	tn.Apply(Config{Key: key, Tuners: 1, Channels: []ChannelConfig{b, a}})
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, uint64(1), renderGen(p), "the channels in another order rendered again")

	b.Name = "B News"
	tn.Apply(Config{Key: key, Tuners: 1, Channels: []ChannelConfig{b, a}})
	require.Eventually(t, func() bool { return renderGen(p) == 2 }, 2*time.Second, time.Millisecond, "a changed channel renders")
}

// fetchedProvider is a provider with a playlist, so a render builds guides.
func fetchedProvider(t *testing.T, tn *Tuner, blocks []BlockConfig) *provider {
	t.Helper()
	key := types.NamespacedName{Namespace: "media", Name: "news"}
	tn.Apply(Config{
		Key: key, Tuners: 1, Blocks: blocks,
		Filters: []filter.Rule{{Name: "all", Type: filter.TypeCustom, Enabled: true}}, // every entry is a candidate
		Channels: []ChannelConfig{
			{Object: "a", Key: "t:a", Number: "1", Active: true},
			{Object: "b", Key: "t:b", Number: "2", Active: true},
		},
	})
	p := tn.providers[key]
	p.mu.Lock()
	p.renderTimer.Stop()
	p.renderTimer = nil
	p.entries = []m3u.Entry{{Key: "t:a", Name: "A", TvgID: "a", URL: "http://upstream/1"}, {Key: "t:b", Name: "B", TvgID: "b", URL: "http://upstream/2"}}
	p.playlistFetched = true
	p.mu.Unlock()
	return p
}

// One render at a time: a render asked for while one runs follows it, where
// two ran side by side (each a few GB) and the older's result was dropped.
func TestARenderAskedForDuringARenderFollowsIt(t *testing.T) {
	var running, peak, builds atomic.Int32
	entered, release := make(chan struct{}, 8), make(chan struct{})
	orig := buildGuide
	buildGuide = func(w io.Writer, lc []xmltv.LineupChannel, g map[string]xmltv.Guide, from time.Time, days int, e *xmltv.Enricher) error {
		if n := running.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		builds.Add(1)
		entered <- struct{}{}
		<-release
		running.Add(-1)
		return orig(w, lc, g, from, days, e)
	}
	t.Cleanup(func() { buildGuide = orig })

	tn := New(Options{Debounce: time.Millisecond})
	p := fetchedProvider(t, tn, nil)
	done := make(chan struct{})
	go func() { tn.render(p); close(done) }()
	<-entered
	tn.render(p) // asked for during the first: returns at once, queued
	p.mu.Lock()
	assert.True(t, p.renderAgain)
	p.mu.Unlock()
	assert.Equal(t, int32(1), builds.Load(), "the second render built nothing while the first ran")
	close(release)
	<-done
	require.Eventually(t, func() bool { return builds.Load() == 2 }, 2*time.Second, time.Millisecond, "the queued render ran after the first")
	assert.Equal(t, int32(1), peak.Load(), "two renders built guides at once")
}

// A block holding the whole lineup takes the whole provider's guide: the
// same channels in the same order build the same bytes, and the owner's one
// block had the guide built twice per render.
func TestABlockHoldingTheWholeLineupTakesTheProvidersGuide(t *testing.T) {
	var builds atomic.Int32
	orig := buildGuide
	buildGuide = func(w io.Writer, lc []xmltv.LineupChannel, g map[string]xmltv.Guide, from time.Time, days int, e *xmltv.Enricher) error {
		builds.Add(1)
		return orig(w, lc, g, from, days, e)
	}
	t.Cleanup(func() { buildGuide = orig })

	tn := New(Options{Debounce: time.Hour})
	p := fetchedProvider(t, tn, []BlockConfig{{DeviceID: "dev1", Start: "1"}})
	tn.render(p)
	assert.Equal(t, int32(1), builds.Load(), "the one block's guide was built beside the provider's")
	p.mu.Lock()
	defer p.mu.Unlock()
	require.NotNil(t, p.blocks["dev1"])
	assert.Equal(t, p.storedGuideHash, p.blocks["dev1"].guideHash, "the block serves the provider's guide")
	assert.Len(t, p.blocks["dev1"].entries, 2)

	builds.Store(0)
	tn2 := New(Options{Debounce: time.Hour})
	p2 := fetchedProvider(t, tn2, []BlockConfig{{DeviceID: "dev1", Start: "1", Numbers: []string{"1"}}, {DeviceID: "dev2", Start: "2"}})
	tn2.render(p2)
	assert.Equal(t, int32(3), builds.Load(), "two blocks each build their own guide beside the provider's")
}
