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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

func TestGuidesAreBuiltOutsideTheProvidersLock(t *testing.T) {
	// Building a few thousand channels' guides takes seconds; a lineup or
	// a tune waiting behind it on the provider's lock would stall Plex.
	entered, release := make(chan struct{}, 8), make(chan struct{})
	orig := buildGuide
	buildGuide = func(w io.Writer, lc []xmltv.LineupChannel, g map[string]xmltv.Guide, from time.Time, days int) error {
		entered <- struct{}{}
		<-release
		return orig(w, lc, g, from, days)
	}
	t.Cleanup(func() { buildGuide = orig })

	key := types.NamespacedName{Namespace: "media", Name: "news"}
	tn := New(Options{Debounce: time.Hour})
	tn.Apply(Config{
		Key: key, Tuners: 1, EPGSource: "XEPG",
		Blocks:   []BlockConfig{{Start: "1", Port: 47000, Numbers: []string{"1"}}},
		Channels: []ChannelConfig{{Object: "a", Key: "t:a", Number: "1", Active: true}},
	})
	p := tn.providers[key]
	p.mu.Lock()
	p.entries, p.playlistFetched = []m3u.Entry{{Key: "t:a", Name: "A", TvgID: "a", URL: "http://upstream/1"}}, true
	p.mu.Unlock()

	done := make(chan struct{})
	go func() { tn.render(p); close(done) }()
	<-entered
	got := make(chan bool)
	go func() { _, ok := tn.Snapshot(key); got <- ok }()
	select {
	case ok := <-got:
		require.True(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("Snapshot waited on a guide being built")
	}
	close(release)
	<-done
}
