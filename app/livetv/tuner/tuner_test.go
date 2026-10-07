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

package tuner_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var (
	key = types.NamespacedName{Namespace: "media", Name: "news"}
	now = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
)

// provider is the fixture IPTV provider: the M3U fixture (its stream URLs
// rewritten to this server), the XMLTV fixture, and an endless TS stream at
// every /live/ path. It counts requests per path.
type provider struct {
	srv          *httptest.Server
	hits         sync.Map // path -> *atomic.Int32
	failPlaylist atomic.Bool
	failGuide    atomic.Bool
}

func newProvider(t *testing.T) *provider {
	m3uBody, err := os.ReadFile("../../../test/data/iptv/m3u_plus.m3u")
	require.NoError(t, err)
	guideBody, err := os.ReadFile("../../../test/data/iptv/guide.xml")
	require.NoError(t, err)
	p := &provider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := p.hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		c.(*atomic.Int32).Add(1)
		switch {
		case r.URL.Path == "/get.php":
			if p.failPlaylist.Load() {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(bytes.ReplaceAll(m3uBody, []byte("http://provider.example"), []byte(p.srv.URL)))
		case r.URL.Path == "/guide.xml":
			if p.failGuide.Load() {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(guideBody)
		case strings.HasPrefix(r.URL.Path, "/live/"):
			w.Header().Set("Content-Type", "video/mp2t")
			pkt := make([]byte, 188)
			pkt[0] = 0x47
			for i := 0; r.Context().Err() == nil; i++ {
				if _, err := w.Write(pkt); err != nil {
					return
				}
				if i%64 == 0 {
					w.(http.Flusher).Flush()
					time.Sleep(time.Millisecond)
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) count(path string) int32 {
	c, ok := p.hits.Load(path)
	if !ok {
		return 0
	}
	return c.(*atomic.Int32).Load()
}

// The fixture provider's two blocks, as the controller packs them: Plex
// reaches each through its pod's proxy at 169.254.47.1:<port>.
const (
	hostA = "169.254.47.1:47012" // block 1001: 1001-1003
	hostB = "169.254.47.1:47013" // block 1004: 1004
)

var (
	deviceA = hdhr.BlockDeviceID("media", "news", "1001")
	deviceB = hdhr.BlockDeviceID("media", "news", "1004")
)

func config(srv string) tuner.Config {
	return tuner.Config{
		Key: key,
		Blocks: []tuner.BlockConfig{
			{Start: "1001", DeviceID: deviceA, FriendlyName: "Clustarr news 1001+", Port: 47012, Numbers: []string{"1001", "1002", "1003"}},
			{Start: "1004", DeviceID: deviceB, FriendlyName: "Clustarr news 1004+", Port: 47013, Numbers: []string{"1004"}},
		},
		Tuners: 1, UserAgent: "clustarr",
		PlaylistURL: srv + "/get.php?username=u&password=p", PlaylistRefresh: time.Hour, EPGSource: "XEPG",
		Guides: []tuner.GuideConfig{{Name: "main", URL: srv + "/guide.xml?token=secret", Refresh: time.Hour}},
		Filters: []filter.Rule{
			{Type: "groupTitle", Match: "UK | Entertainment", Enabled: true},
			{Type: "groupTitle", Match: "UK | News", Enabled: true},
			{Type: "custom", Match: "Sports 1", Enabled: true},
			{Type: "groupTitle", Match: "Misc", Enabled: true},
		},
		Channels: []tuner.ChannelConfig{
			{Object: "news-a", Key: "t:bbc1.uk", Number: "1001", Name: "BBC One", Active: true},           // auto-maps by tvg-id
			{Object: "news-b", Key: m3u.NameKey("UK | News", "News, Live"), Number: "1002", Active: true}, // auto-maps by display name
			{Object: "news-c", Key: "t:gone", Number: "1003", Active: true},                               // missing
			{Object: "news-d", Key: m3u.NameKey("Sports", "Sports 1"), Number: "1001", Active: true},      // duplicate number
			{
				Object: "news-e", Key: m3u.NameKey("Misc", "No Attributes"), Number: "1004", Active: true,
				Map: &xmltv.Mapping{Guide: "nope", ChannelID: "x"},
			}, // guide not found
		},
		Buffer: 1 << 20, Linger: time.Second,
	}
}

func newStore(t *testing.T) events.ObjectStore {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus.ObjectStore(events.BucketLiveTV)
}

type harness struct {
	tn      *tuner.Tuner
	front   *httptest.Server
	notifed atomic.Int32
}

func start(t *testing.T, store events.ObjectStore, cfg tuner.Config) *harness {
	h := &harness{}
	h.tn = tuner.New(tuner.Options{
		BindAddress: "127.0.0.1:0", Store: store, HTTP: http.DefaultClient,
		Now: func() time.Time { return now }, Debounce: 10 * time.Millisecond,
		Notify: func(types.NamespacedName) { h.notifed.Add(1) },
	})
	h.tn.Apply(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.tn.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.front = httptest.NewServer(h.tn.Handler())
	t.Cleanup(h.front.Close)
	return h
}

func (h *harness) get(t *testing.T, host, path string) (int, string) {
	req, err := http.NewRequest(http.MethodGet, h.front.URL+path, nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (h *harness) fetched(t *testing.T) tuner.Snapshot {
	var snap tuner.Snapshot
	require.Eventually(t, func() bool {
		s, ok := h.tn.Snapshot(key)
		snap = s
		return ok && s.PlaylistFetched && len(s.Guides) == 1 && !s.Guides[0].FetchedAt.IsZero() &&
			len(s.Blocks) == 2 && s.Blocks[0].LineupHash != "" && s.Blocks[0].GuideHash != "" && s.Blocks[1].GuideHash != ""
	}, 5*time.Second, 10*time.Millisecond)
	return snap
}

func lineupOf(t *testing.T, body string) []hdhr.LineupEntry {
	t.Helper()
	var l []hdhr.LineupEntry
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	return l
}

func TestALineupWaits503UntilThePlaylistIsFetched(t *testing.T) {
	tn := tuner.New(tuner.Options{Store: newStore(t), HTTP: http.DefaultClient, Now: func() time.Time { return now }, Debounce: time.Millisecond})
	tn.Apply(config("http://127.0.0.1:1"))
	front := httptest.NewServer(tn.Handler())
	defer front.Close()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/lineup.json", nil)
	req.Host = hostA
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestEachBlockServesOnlyItsNumbersUnderTheHostPlexUsed(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)

	code, body := h.get(t, hostA, "/lineup.json")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []hdhr.LineupEntry{
		{GuideNumber: "1001", GuideName: "BBC One", URL: "http://169.254.47.1:47012/stream/1001"},
		{GuideNumber: "1002", GuideName: "News, Live", URL: "http://169.254.47.1:47012/stream/1002"},
	}, lineupOf(t, body), "block 1001's numbers only; the duplicate 1001 (news-d sorts after news-a) and the missing 1003 are absent")
	code, body = h.get(t, hostB, "/lineup.json")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []hdhr.LineupEntry{
		{GuideNumber: "1004", GuideName: "No Attributes", URL: "http://169.254.47.1:47013/stream/1004"},
	}, lineupOf(t, body))

	code, body = h.get(t, "anything", "/livetv/media/news/1004/lineup.json")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []hdhr.LineupEntry{
		{GuideNumber: "1004", GuideName: "No Attributes", URL: "http://anything/livetv/media/news/1004/stream/1004"},
	}, lineupOf(t, body), "path routes carry the request's Host and path")

	for _, c := range []struct{ host, device, base string }{
		{hostA, deviceA, "http://169.254.47.1:47012"},
		{hostB, deviceB, "http://169.254.47.1:47013"},
	} {
		code, disc := h.get(t, c.host, "/discover.json")
		require.Equal(t, http.StatusOK, code)
		var d map[string]any
		require.NoError(t, json.Unmarshal([]byte(disc), &d))
		assert.Equal(t, c.device, d["DeviceID"])
		assert.Equal(t, c.base, d["BaseURL"])
		assert.Equal(t, c.base+"/lineup.json", d["LineupURL"])
		assert.Equal(t, float64(1), d["TunerCount"])
	}
}

func TestRootRoutesFindTheBlockByThePortInHost(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	for _, host := range []string{hostA, "LIVETV.MEDIA.SVC:47012", "livetv.media.svc.:47012", "[::1]:47012"} {
		code, body := h.get(t, host, "/discover.json")
		assert.Equal(t, http.StatusOK, code, host)
		assert.Contains(t, body, deviceA, "%s reaches block 1001", host)
	}
	code, _ := h.get(t, "169.254.47.1", "/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "a Host with no port names no block")
	code, _ = h.get(t, "169.254.47.1:47999", "/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "an unknown port is 404")
	code, _ = h.get(t, "anything", "/livetv/media/news/1001/discover.json")
	assert.Equal(t, http.StatusOK, code, "path routes work under any host")
	code, _ = h.get(t, "anything", "/livetv/media/news/1002/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "a start no block has")
	code, _ = h.get(t, hostA, "/livetv/media/other/1001/discover.json")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestSnapshotMarksDuplicateMissingAndGuideNotFound(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	s := h.fetched(t)
	assert.True(t, s.Channels["news-c"].Missing)
	assert.True(t, s.Channels["news-d"].Duplicate)
	assert.True(t, s.Channels["news-e"].GuideNotFound)
	assert.Equal(t, "main/bbc1.uk", s.Channels["news-a"].Guide)
	assert.Equal(t, "main/news.uk", s.Channels["news-b"].Guide)
	assert.Equal(t, "dummy/60", s.Channels["news-e"].Guide)
	assert.Equal(t, 3, s.Lineup.Active)
	assert.Equal(t, 1, s.Lineup.Missing)
	assert.Equal(t, 1, s.Lineup.Unmapped)
	assert.Equal(t, []int{2, 1}, []int{s.Blocks[0].Active, s.Blocks[1].Active})
	assert.Equal(t, []string{"1001", "1004"}, []string{s.Blocks[0].Start, s.Blocks[1].Start})
	assert.NotEqual(t, s.Blocks[0].LineupHash, s.Blocks[1].LineupHash)
	assert.Equal(t, 6, s.Playlist.Entries)
	assert.Equal(t, 4, s.Playlist.Candidates)
	assert.Equal(t, 4, s.Playlist.Groups, "distinct non-empty group titles")
	assert.Equal(t, 2, s.Guides[0].Channels)
	assert.Equal(t, 3, s.Guides[0].Programmes, "bbc1.uk's two programmes in the window, and news.uk's one: it is auto-mapped too")
}

func TestEachBlocksGuideHoldsOnlyItsChannels(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)

	code, a := h.get(t, hostA, "/xmltv.xml")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, a, `<channel id="1001">`)
	assert.Contains(t, a, `<channel id="1002">`)
	assert.NotContains(t, a, `<channel id="1004">`)
	assert.Contains(t, a, `<title lang="en">Six O'Clock News</title>`, "the source's programme, verbatim")
	code, b := h.get(t, hostB, "/xmltv.xml")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, b, `<channel id="1004">`)
	assert.NotContains(t, b, `<channel id="1001">`)
	code, all := h.get(t, "x", "/livetv/media/news/xmltv.xml")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, all, `<channel id="1001">`, "the provider-wide guide, for the UI and curl")
	assert.Contains(t, all, `<channel id="1004">`)

	cfg := config(p.srv.URL)
	cfg.EPGSource = "PMS"
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool {
		s, _ := h.tn.Snapshot(key)
		return len(s.Blocks) == 2 && s.Blocks[0].GuideHash == "" && s.Blocks[1].GuideHash == ""
	}, 2*time.Second, 10*time.Millisecond)
	code, _ = h.get(t, hostA, "/xmltv.xml")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = h.get(t, "x", "/livetv/media/news/xmltv.xml")
	assert.Equal(t, http.StatusNotFound, code)
	s, _ := h.tn.Snapshot(key)
	assert.Equal(t, "", s.Channels["news-a"].Guide, "no mapping under PMS")
}

func TestALineupHashDoesNotDependOnTheHost(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	before := h.fetched(t)
	h.get(t, hostA, "/lineup.json")
	h.get(t, "anything", "/livetv/media/news/1001/lineup.json")
	after, _ := h.tn.Snapshot(key)
	assert.Equal(t, before.Blocks[0].LineupHash, after.Blocks[0].LineupHash)

	other := start(t, newStore(t), config(p.srv.URL))
	again := other.fetched(t)
	assert.Equal(t, before.Blocks[0].LineupHash, again.Blocks[0].LineupHash, "a second tuner renders the same block identically")
	assert.Equal(t, before.Blocks[1].LineupHash, again.Blocks[1].LineupHash)
}

func TestAFailedRefreshKeepsServingTheLastLineup(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	_, before := h.get(t, hostA, "/lineup.json")
	p.failPlaylist.Store(true)
	cfg.PlaylistURL += "&type=m3u_plus"
	h.tn.Apply(cfg)
	var s tuner.Snapshot
	require.Eventually(t, func() bool { s, _ = h.tn.Snapshot(key); return s.Playlist.Error != "" }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, s.Playlist.Error, "/get.php")
	assert.NotContains(t, s.Playlist.Error, "password")
	assert.NotContains(t, s.Playlist.Error, "username=u")
	_, after := h.get(t, hostA, "/lineup.json")
	assert.Equal(t, before, after)
	assert.True(t, s.PlaylistFetched)
}

func TestABurstOfChannelEditsRendersOnce(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	first := h.fetched(t)
	time.Sleep(50 * time.Millisecond)
	base := h.notifed.Load()
	for i := 0; i < 20; i++ {
		cfg.Channels[0].Name = fmt.Sprintf("BBC One %d", i)
		h.tn.Apply(cfg)
	}
	require.Eventually(t, func() bool {
		_, body := h.get(t, hostA, "/lineup.json")
		return strings.Contains(body, "BBC One 19")
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	s, _ := h.tn.Snapshot(key)
	assert.NotEqual(t, first.Blocks[0].LineupHash, s.Blocks[0].LineupHash)
	assert.Equal(t, first.Blocks[1].LineupHash, s.Blocks[1].LineupHash, "the other block's lineup is unchanged")
	assert.LessOrEqual(t, h.notifed.Load()-base, int32(3))
}

func TestTheGuidesAreStoredAndServedAfterARestart(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	h := start(t, store, config(p.srv.URL))
	h.fetched(t)
	_, guide := h.get(t, hostA, "/xmltv.xml")
	require.Eventually(t, func() bool {
		_, err := store.Info(context.Background(), tuner.BlockGuideObject(key, "1001"))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	p.failGuide.Store(true)
	h2 := start(t, store, config(p.srv.URL))
	require.Eventually(t, func() bool { code, _ := h2.get(t, hostA, "/xmltv.xml"); return code == http.StatusOK }, 2*time.Second, 10*time.Millisecond)
	_, again := h2.get(t, hostA, "/xmltv.xml")
	assert.Equal(t, guide, again, "the stored copy, while the guide cannot be fetched")
	require.Eventually(t, func() bool { s, _ := h2.tn.Snapshot(key); return len(s.Guides) == 1 && s.Guides[0].Error != "" }, 5*time.Second, 10*time.Millisecond)
	_, still := h2.get(t, hostA, "/xmltv.xml")
	assert.Equal(t, guide, still, "a failed fetch never replaces the stored guide with an empty one")
}

func TestNoStoredOrReportedStringCarriesACredential(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	h := start(t, store, config(p.srv.URL))
	s := h.fetched(t)
	ctx := context.Background()
	require.Eventually(t, func() bool { _, err := store.Info(ctx, tuner.PlaylistObject(key)); return err == nil }, 2*time.Second, 10*time.Millisecond)
	infos, err := store.List(ctx, "")
	require.NoError(t, err)
	require.NotEmpty(t, infos)
	for _, info := range infos {
		_, rc, err := store.Get(ctx, info.Name)
		require.NoError(t, err)
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			body, _ = io.ReadAll(zr)
		}
		for _, secret := range []string{"/live/user/pass/", "password", "token=secret", "username=u"} {
			assert.NotContains(t, string(body), secret, info.Name)
		}
	}
	for _, secret := range []string{"/live/user/pass/", "password", "token=secret", "username=u"} {
		assert.NotContains(t, fmt.Sprintf("%+v", s), secret)
	}
	_, rc, err := store.Get(ctx, tuner.PlaylistObject(key))
	require.NoError(t, err)
	zr, err := gzip.NewReader(rc)
	require.NoError(t, err)
	var doc struct {
		Entries []map[string]any `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(zr).Decode(&doc))
	_ = rc.Close()
	require.Len(t, doc.Entries, 6)
	for _, e := range doc.Entries {
		for k := range e {
			assert.Contains(t, []string{"key", "name", "group", "tvgID", "tvgName", "logo"}, k)
		}
	}
	_, err = store.Info(ctx, tuner.GuideChannelsObject(key, "main"))
	assert.NoError(t, err)
}

func TestAStreamRequestReachesTheRelay(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	// Block 1004 does not hold 1001: a stream is the provider's, under any block.
	req, _ := http.NewRequest(http.MethodGet, h.front.URL+"/stream/1001", nil)
	req.Host = hostB
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "video/mp2t", resp.Header.Get("Content-Type"))
	head := make([]byte, 188*2)
	_, err = io.ReadFull(resp.Body, head)
	require.NoError(t, err)
	assert.Equal(t, byte(0x47), head[0])
	assert.Equal(t, byte(0x47), head[188])
	assert.Equal(t, int32(1), p.count("/live/user/pass/1001.ts"))
	s, _ := h.tn.Snapshot(key)
	assert.Equal(t, 1, s.TunersInUse)
	code, _ := h.get(t, hostA, "/stream/9999")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestANewRefreshValueRefetchesEverySource(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	pl, gd := p.count("/get.php"), p.count("/guide.xml")
	cfg.Refresh = "1"
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { return p.count("/get.php") == pl+1 && p.count("/guide.xml") == gd+1 }, 5*time.Second, 10*time.Millisecond)
	h.tn.Apply(cfg) // unchanged
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, pl+1, p.count("/get.php"))
	cfg.Refresh = "2"
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { return p.count("/get.php") == pl+2 }, 5*time.Second, 10*time.Millisecond)
}

func TestRemoveEndsTheProvider(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	h.tn.Remove(key)
	code, _ := h.get(t, hostA, "/discover.json")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = h.get(t, "x", "/livetv/media/news/1001/discover.json")
	assert.Equal(t, http.StatusNotFound, code)
	_, ok := h.tn.Snapshot(key)
	assert.False(t, ok)
}
