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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// A render keeps no guide in memory: the parsed programmes are dropped
// once the guides are built and read back from the guide cache object when
// the next render or a range needs them, and a device's guide is served
// from the store, its ETag answered without a read (2026-11-03: the
// programmes and the rendered guide were 84 MB of the manager's heap).
func TestARenderKeepsNoGuideInMemory(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	store := bus.ObjectStore(events.BucketLiveTV)
	now := time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)
	tn := New(Options{Store: store, Debounce: time.Hour, Now: func() time.Time { return now }})
	key := types.NamespacedName{Namespace: "media", Name: "news"}
	tn.Apply(Config{
		Key: key, Tuners: 1, EPGSource: "XEPG", Blocks: []BlockConfig{{DeviceID: "dev1", Start: "1"}},
		Filters:  []filter.Rule{{Name: "all", Type: filter.TypeCustom, Enabled: true}},
		Channels: []ChannelConfig{{Object: "a", Key: "t:a", Number: "1", Active: true, Map: &xmltv.Mapping{Guide: GuideName, ChannelID: "a"}}},
	})
	p := tn.providers[key]
	g := xmltv.Guide{
		Channels: []xmltv.Channel{{ID: "a", DisplayNames: []string{"A"}}},
		Programmes: map[string][]xmltv.Programme{"a": {
			{Channel: "a", Start: now, Stop: now.Add(time.Hour), Inner: []byte("<title>News at Three</title>")},
		}},
	}
	tn.putGuideCache(context.Background(), p, p.cfg, g, map[string]bool{"a": true}, now)
	p.mu.Lock()
	p.renderTimer.Stop()
	p.renderTimer = nil
	p.entries = []m3u.Entry{{Key: "t:a", Name: "A", TvgID: "a", URL: "http://upstream/1"}}
	p.playlistFetched = true
	p.guide = guide{g: g, loaded: true, index: guideIndex(g), wanted: map[string]bool{"a": true}}
	p.mu.Unlock()

	tn.render(p)
	p.mu.Lock()
	sf := p.blocks["dev1"]
	require.NotNil(t, sf)
	first := sf.guideHash
	assert.Equal(t, livetvstore.GuideObject(key), sf.guideObject, "the one block serves the provider's stored guide")
	assert.Nil(t, p.guide.g.Programmes, "the programmes are dropped after the render")
	assert.Len(t, p.guide.g.Channels, 1, "the channels stay")
	p.mu.Unlock()
	require.NotEmpty(t, first)

	srv := httptest.NewServer(tn.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/hdhr/dev1/xmltv.xml")
	require.NoError(t, err)
	body := make([]byte, 1<<20)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, etagOf(first), resp.Header.Get("ETag"))
	assert.Contains(t, string(body[:n]), "News at Three", "the guide is served from the store")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/hdhr/dev1/xmltv.xml", nil)
	req.Header.Set("If-None-Match", etagOf(first))
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)

	// The next render reads the programmes back and builds the same guide.
	tn.render(p)
	p.mu.Lock()
	assert.Equal(t, first, p.blocks["dev1"].guideHash, "a render from the cache object builds the same guide")
	assert.Nil(t, p.guide.g.Programmes)
	p.mu.Unlock()

	// So does a range.
	resp, err = http.Get(srv.URL + "/livetv/media/news/xmltv.xml?from=1&to=1")
	require.NoError(t, err)
	n, _ = resp.Body.Read(body)
	_ = resp.Body.Close()
	assert.Contains(t, string(body[:n]), "News at Three", "a range reads the programmes back")
}
