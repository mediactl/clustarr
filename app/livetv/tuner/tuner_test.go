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
	"image"
	"image/color"
	"image/png"
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
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var (
	key = types.NamespacedName{Namespace: "media", Name: "news"}
	now = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
)

// provider is the fixture Xtream panel: get.php serves the M3U fixture (its
// stream URLs rewritten to this server), xmltv.php the XMLTV fixture,
// player_api.php the account, and every /live/ path an endless TS stream. It
// counts requests per path.
type provider struct {
	srv           *httptest.Server
	hits          sync.Map // path -> *atomic.Int32
	failPlaylist  atomic.Bool
	failGuide     atomic.Bool
	emptyPlaylist atomic.Bool  // #EXTM3U alone: a panel in maintenance, an expired line
	headerDelay   atomic.Int64 // nanoseconds the playlist and guide take to answer
	maxConns      atomic.Int32 // the account's max_connections
	// newsIcon gives news.uk an icon in the panel's guide: 0 none, 1 one
	// that loads, 2 one that does not (a dead picon host).
	newsIcon atomic.Int32
}

func newProvider(t *testing.T) *provider {
	m3uBody, err := os.ReadFile("../../../test/data/iptv/m3u_plus.m3u")
	require.NoError(t, err)
	guideBody, err := os.ReadFile("../../../test/data/iptv/guide.xml")
	require.NoError(t, err)
	// A guideURL's guide: the fixture without news.uk (as iptv-epg.org
	// lacks Adult Swim), and a title of its own so a test can tell whose
	// programme it reads.
	var epgLines []string
	for _, l := range strings.Split(string(guideBody), "\n") {
		if !strings.Contains(l, "news.uk") {
			epgLines = append(epgLines, l)
		}
	}
	epgBody := []byte(strings.ReplaceAll(strings.Join(epgLines, "\n"), "The One Show", "The One Show (epg)"))
	p := &provider{}
	p.maxConns.Store(1)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := p.hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		c.(*atomic.Int32).Add(1)
		if r.URL.Path == "/get.php" || r.URL.Path == "/xmltv.php" {
			time.Sleep(time.Duration(p.headerDelay.Load()))
		}
		switch {
		case r.URL.Path == "/get.php":
			if r.URL.Query().Get("username") != "user" || r.URL.Query().Get("password") != "pass" ||
				r.URL.Query().Get("output") != "ts" || r.URL.Query().Get("type") != "m3u_plus" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if p.failPlaylist.Load() {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			if p.emptyPlaylist.Load() {
				_, _ = w.Write([]byte("#EXTM3U\n"))
				return
			}
			_, _ = w.Write(bytes.ReplaceAll(m3uBody, []byte("http://provider.example"), []byte(p.srv.URL)))
		case r.URL.Path == "/xmltv.php":
			if p.failGuide.Load() {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			body := guideBody
			if p.newsIcon.Load() != 0 {
				body = bytes.Replace(guideBody, []byte(`<channel id="news.uk">`),
					[]byte(`<channel id="news.uk"><icon src="`+p.srv.URL+`/picons/news.png"/>`), 1)
			}
			_, _ = w.Write(body)
		case r.URL.Path == "/picons/news.png":
			if p.newsIcon.Load() != 1 {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
		case strings.HasPrefix(r.URL.Path, "/epg/"):
			// A guideURL's guide, its placeholders expanded in the path and
			// the query.
			if r.URL.Path != "/epg/user/guide.xml" || r.URL.Query().Get("key") != "pass" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(epgBody)
		case r.URL.Path == "/lists/user.m3u":
			// An M3U provider's playlist URL, its {username} filled in.
			if r.URL.Query().Get("key") != "pass" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(bytes.ReplaceAll(m3uBody, []byte("http://provider.example"), []byte(p.srv.URL)))
		case r.URL.Path == "/player_api.php":
			_, _ = fmt.Fprintf(w, `{"user_info":{"username":"user","password":"pass","auth":1,"status":"Active","exp_date":"1792556379","active_cons":"0","max_connections":"%d"}}`, p.maxConns.Load())
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

// The fixture provider's two blocks, as the controller packs them: Plex is
// given each at /hdhr/<deviceID> on the livetv Service.
var (
	deviceA = hdhr.BlockDeviceID("media", "news", "1001")
	deviceB = hdhr.BlockDeviceID("media", "news", "1004")
)

func config(srv string) tuner.Config {
	return tuner.Config{
		Key: key, Server: srv, Username: "user", Password: "pass",
		Blocks: []tuner.BlockConfig{
			{Start: "1001", DeviceID: deviceA, FriendlyName: "Clustarr news 1001+", Numbers: []string{"1001", "1002", "1003"}},
			{Start: "1004", DeviceID: deviceB, FriendlyName: "Clustarr news 1004+", Numbers: []string{"1004"}},
		},
		UserAgent: "clustarr", Every: time.Hour, EPGSource: "XEPG",
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
				Map: &xmltv.Mapping{Guide: tuner.GuideName, ChannelID: "x"},
			}, // mapped to a channel the guide does not have
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
	tuners  atomic.Int32 // tuners-in-use notices
	clock   atomic.Int64 // UnixNano; the tuner's Now
}

func start(t *testing.T, store events.ObjectStore, cfg tuner.Config) *harness {
	return startAt(t, store, cfg, now)
}

// startAt starts a tuner whose clock reads at, until the test moves it.
func startAt(t *testing.T, store events.ObjectStore, cfg tuner.Config, at time.Time) *harness {
	return startWith(t, store, cfg, at, nil)
}

// startWith is startAt with edit applied to the tuner's options.
func startWith(t *testing.T, store events.ObjectStore, cfg tuner.Config, at time.Time, edit func(*tuner.Options)) *harness {
	h := &harness{}
	h.clock.Store(at.UnixNano())
	o := tuner.Options{
		BindAddress: "127.0.0.1:0", Store: store, HTTP: http.DefaultClient,
		Now: func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }, Debounce: 10 * time.Millisecond,
		TunersDebounce: 10 * time.Millisecond,
		Notify:         func(types.NamespacedName) { h.notifed.Add(1) },
		NotifyTuners:   func(types.NamespacedName) { h.tuners.Add(1) },
	}
	if edit != nil {
		edit(&o)
	}
	h.tn = tuner.New(o)
	h.tn.Apply(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.tn.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	h.front = httptest.NewServer(h.tn.Handler())
	t.Cleanup(h.front.Close)
	return h
}

// do sends one request with host as its Host and returns it whole.
func (h *harness) do(t *testing.T, host, path string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.front.URL+path, nil)
	require.NoError(t, err)
	req.Host = host
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func (h *harness) get(t *testing.T, host, path string) (int, string) {
	t.Helper()
	resp := h.do(t, host, path, nil)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// fetched waits for the playlist, the guide and the account, and the render
// that follows them.
func (h *harness) fetched(t *testing.T) tuner.Snapshot {
	var snap tuner.Snapshot
	require.Eventually(t, func() bool {
		s, ok := h.tn.Snapshot(key)
		snap = s
		return ok && s.PlaylistFetched && !s.Guide.FetchedAt.IsZero() && !s.Account.FetchedAt.IsZero() && s.Lineup.Active > 0 &&
			len(s.Blocks) == 2 && s.Blocks[0].LineupHash != "" && s.Blocks[0].GuideHash != "" && s.Blocks[1].GuideHash != ""
	}, 5*time.Second, 10*time.Millisecond)
	return snap
}

// device is a path under block id's device, /hdhr/<id>.
func device(id, file string) string { return "/hdhr/" + id + "/" + file }

func lineupOf(t *testing.T, body string) []hdhr.LineupEntry {
	t.Helper()
	var l []hdhr.LineupEntry
	require.NoError(t, json.Unmarshal([]byte(body), &l))
	return l
}

func numbers(l []hdhr.LineupEntry) []string {
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, e.GuideNumber)
	}
	return out
}

func TestALineupWaits503UntilThePlaylistIsFetched(t *testing.T) {
	tn := tuner.New(tuner.Options{Store: newStore(t), HTTP: http.DefaultClient, Now: func() time.Time { return now }, Debounce: time.Millisecond})
	tn.Apply(config("http://127.0.0.1:1"))
	front := httptest.NewServer(tn.Handler())
	defer front.Close()
	resp, err := http.Get(front.URL + "/livetv/media/news/lineup.json")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestTheProviderIsFetchedFromItsPanel: the playlist is get.php with the
// account's credentials, output=ts and type=m3u_plus; the guide xmltv.php;
// the account player_api.php.
func TestTheProviderIsFetchedFromItsPanel(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	s := h.fetched(t)
	for _, path := range []string{"/get.php", "/xmltv.php", "/player_api.php"} {
		assert.Equal(t, int32(1), p.count(path), path)
	}
	assert.Equal(t, "Active", s.Account.Status)
	assert.Equal(t, time.Unix(1792556379, 0).UTC(), s.Account.ExpiresAt)
	assert.Equal(t, 1, s.Account.MaxConnections)
}

// TestAPlaylistMayAnswerSlowerThanAStream: playlists and guides are fetched
// through Options.Fetch, whose headers may take longer than a stream's. On
// 2026-10-20 tvload.win took 13 s to start its m3u_plus, and with the
// stream's 10 s for both, every fetch failed; once the manager restarted, the
// lineup answered 503 and every tune 404, which Plex shows as "Could not tune
// channel".
func TestAPlaylistMayAnswerSlowerThanAStream(t *testing.T) {
	p := newProvider(t)
	p.headerDelay.Store(int64(300 * time.Millisecond))
	h := startWith(t, newStore(t), config(p.srv.URL), now, func(o *tuner.Options) {
		o.HTTP = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 100 * time.Millisecond}}
		o.Fetch = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 5 * time.Second}}
	})
	h.fetched(t)
	code, body := h.get(t, "x", "/livetv/media/news/lineup.json")
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, lineupOf(t, body), 3)
}

// TestABlockIsServedUnderHDHRByItsID: Plex is given each block at
// /hdhr/<deviceID> on the livetv Service, and every URL the device hands out
// is built from the Host Plex dialled and that path.
func TestABlockIsServedUnderHDHRByItsID(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)

	resp := h.do(t, "clustarr-livetv.media.svc.cluster.local:80", device(deviceA, "lineup.json"), nil)
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, []hdhr.LineupEntry{
		{GuideNumber: "1001", GuideName: "BBC One", URL: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/" + deviceA + "/stream/1001"},
		{GuideNumber: "1002", GuideName: "News, Live", URL: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/" + deviceA + "/stream/1002"},
	}, lineupOf(t, string(b)), "the Host as PMS dialled it, the device's path, block 1001's numbers only "+
		"(the duplicate 1001, news-d, sorts after news-a; the missing 1003 is absent)")
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	for _, id := range []string{deviceA, deviceB} {
		_, body := h.get(t, "anything", device(id, "discover.json"))
		var d map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &d))
		assert.Equal(t, id, d["DeviceID"])
		assert.Equal(t, "http://anything/hdhr/"+id, d["BaseURL"])
		assert.Equal(t, "http://anything/hdhr/"+id+"/lineup.json", d["LineupURL"])
		assert.Equal(t, float64(1+tuner.SpareTuners), d["TunerCount"], "PMS may hold grabs the relay no longer streams; the relay's 503 is the limit")
	}

	code, _ := h.get(t, "x", device("FFFFFFFF", "discover.json"))
	assert.Equal(t, http.StatusNotFound, code, "a device no provider has")
	code, _ = h.get(t, "x", "/hdhr/"+deviceA)
	assert.Equal(t, http.StatusNotFound, code, "a device names a file")
	code, _ = h.get(t, "x", "/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "no root route")
	code, _ = h.get(t, "x", "/livetv/media/news/devices/"+deviceA+"/discover.json")
	assert.Equal(t, http.StatusNotFound, code, "the old device route is gone")
	assert.Equal(t, "/hdhr/"+deviceA, tuner.DeviceEndpoint(deviceA))
}

// TestForwardedHostAndProtoAreHonouredAndPrefixIsNot: a Gateway in front of
// the tuner may name the host and scheme the client used; the path is the
// request's own.
func TestForwardedHostAndProtoAreHonouredAndPrefixIsNot(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	resp := h.do(t, "internal", device(deviceA, "discover.json"),
		http.Header{"X-Forwarded-Host": {"tv.example.com"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-Prefix": {"/elsewhere"}})
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var d map[string]any
	require.NoError(t, json.Unmarshal(b, &d))
	assert.Equal(t, "https://tv.example.com/hdhr/"+deviceA, d["BaseURL"], "the path is the request's; a prefix header changes nothing")
}

// TestTwoProvidersBlocksNeverAnswerEachOther: /hdhr/<id> names one block of
// one provider, without the namespace or name, since DeviceIDs are unique
// across the cluster.
func TestTwoProvidersBlocksNeverAnswerEachOther(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	sportKey := types.NamespacedName{Namespace: "media", Name: "sport"}
	sportID := hdhr.BlockDeviceID("media", "sport", "2001")
	h.tn.Apply(tuner.Config{
		Key: sportKey, Server: p.srv.URL, Username: "user", Password: "pass",
		Blocks:    []tuner.BlockConfig{{Start: "2001", DeviceID: sportID, FriendlyName: "Clustarr sport 2001+", Numbers: []string{"2001"}}},
		UserAgent: "clustarr", Every: time.Hour, EPGSource: "XEPG",
		Filters:  []filter.Rule{{Type: "custom", Match: "Sports 1", Enabled: true}},
		Channels: []tuner.ChannelConfig{{Object: "sport-a", Key: m3u.NameKey("Sports", "Sports 1"), Number: "2001", Active: true}},
		Buffer:   1 << 20, Linger: time.Second,
	})
	h.fetched(t)
	var sport []hdhr.LineupEntry
	require.Eventually(t, func() bool {
		code, body := h.get(t, "x", device(sportID, "lineup.json"))
		if code != http.StatusOK {
			return false
		}
		sport = lineupOf(t, body)
		return len(sport) > 0
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []hdhr.LineupEntry{{GuideNumber: "2001", GuideName: "Sports 1", URL: "http://x/hdhr/" + sportID + "/stream/2001"}}, sport)
	_, body := h.get(t, "x", device(deviceA, "lineup.json"))
	var numbers []string
	for _, e := range lineupOf(t, body) {
		numbers = append(numbers, e.GuideNumber)
	}
	assert.Equal(t, []string{"1001", "1002"}, numbers, "news's block answers its own numbers only")
}

func TestALineupHashDoesNotDependOnTheHost(t *testing.T) {
	// The hash tells cluster-plex to save a DVR's map; a hash that moved with
	// the URL Plex came by would save it on every pod.
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	s := h.fetched(t)
	_, _ = h.get(t, "a", device(deviceA, "lineup.json"))
	_, _ = h.get(t, "b", device(deviceA, "lineup.json"))
	again, _ := h.tn.Snapshot(key)
	assert.Equal(t, s.Blocks, again.Blocks)
	assert.NotEqual(t, s.Blocks[0].LineupHash, s.Blocks[1].LineupHash)
}

// TestARangeServesOnlyItsChannels: any range of the provider's lineup, its
// device named by the range's start, its lineup URL carrying the range.
func TestARangeServesOnlyItsChannels(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)

	_, body := h.get(t, "anything", "/livetv/media/news/lineup.json")
	assert.Equal(t, []string{"1001", "1002", "1004"}, numbers(lineupOf(t, body)), "the whole lineup")
	_, body = h.get(t, "anything", "/livetv/media/news/lineup.json?from=1002&to=1004")
	assert.Equal(t, []hdhr.LineupEntry{
		{GuideNumber: "1002", GuideName: "News, Live", URL: "http://anything/livetv/media/news/stream/1002"},
		{GuideNumber: "1004", GuideName: "No Attributes", URL: "http://anything/livetv/media/news/stream/1004"},
	}, lineupOf(t, body))
	_, body = h.get(t, "anything", "/livetv/media/news/lineup.json?to=1001")
	assert.Equal(t, []string{"1001"}, numbers(lineupOf(t, body)), "either end may be left out")
	_, body = h.get(t, "anything", "/livetv/media/news/lineup.json?from=2000")
	assert.Equal(t, "[]", body)

	_, body = h.get(t, "anything", "/livetv/media/news/discover.json?from=1001&to=1002")
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &d))
	assert.Equal(t, hdhr.BlockDeviceID("media", "news", "1001"), d["DeviceID"])
	assert.Equal(t, "http://anything/livetv/media/news/lineup.json?from=1001&to=1002", d["LineupURL"])
	assert.Equal(t, "Clustarr news 1001-1002", d["FriendlyName"])

	for _, path := range []string{"/livetv/media/other/discover.json", "/livetv/media/news/nope.json"} {
		code, _ := h.get(t, "x", path)
		assert.Equal(t, http.StatusNotFound, code, path)
	}
}

func TestSnapshotMarksDuplicateAndMissing(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	s := h.fetched(t)
	assert.True(t, s.Channels["news-c"].Missing)
	assert.True(t, s.Channels["news-d"].Duplicate)
	assert.Equal(t, "xmltv/bbc1.uk", s.Channels["news-a"].Guide)
	assert.Equal(t, "xmltv/news.uk", s.Channels["news-b"].Guide)
	assert.Equal(t, "xmltv/x", s.Channels["news-e"].Guide)
	assert.Equal(t, tuner.LineupState{Active: 3, Missing: 1, First: "1001", Last: "1004"}, s.Lineup)
	assert.Equal(t, []int{2, 1}, []int{s.Blocks[0].Active, s.Blocks[1].Active})
	assert.Equal(t, []string{"1001", "1004"}, []string{s.Blocks[0].Start, s.Blocks[1].Start})
	assert.Equal(t, 6, s.Playlist.Entries)
	assert.Equal(t, 4, s.Playlist.Candidates)
	assert.Equal(t, 4, s.Playlist.Groups, "distinct non-empty group titles")
	assert.Equal(t, 2, s.Guide.Channels)
	assert.Equal(t, 3, s.Guide.Programmes, "bbc1.uk's two programmes in the window, and news.uk's one: it is auto-mapped too")
	assert.Equal(t, 1, s.Tuners)
}

// TestARangesGuideHoldsOnlyItsChannelsAndCarriesAnETag: cluster-plex reloads
// a DVR's guide when its ETag moves, and asks with If-None-Match.
func TestARangesGuideHoldsOnlyItsChannelsAndCarriesAnETag(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	resp := h.do(t, "x", "/livetv/media/news/xmltv.xml?from=1001&to=1002", nil)
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	guide := string(b)
	tag := resp.Header.Get("ETag")
	require.NotEmpty(t, tag)
	assert.Contains(t, guide, `<channel id="1001">`)
	assert.Contains(t, guide, `<channel id="1002">`)
	assert.NotContains(t, guide, `<channel id="1004">`)

	resp = h.do(t, "x", "/livetv/media/news/xmltv.xml?from=1001&to=1002", http.Header{"If-None-Match": {tag}})
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)

	_, whole := h.get(t, "x", "/livetv/media/news/xmltv.xml")
	assert.Contains(t, whole, `<channel id="1004">`)

	resp = h.do(t, "x", device(deviceB, "xmltv.xml"), nil)
	b, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Contains(t, string(b), `<channel id="1004">`, "a device's guide holds its block's channels")
	assert.NotContains(t, string(b), `<channel id="1001">`)
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	resp = h.do(t, "x", "/livetv/media/news/lineup.json?from=1001&to=1002", nil)
	_ = resp.Body.Close()
	assert.NotEmpty(t, resp.Header.Get("ETag"), "a lineup carries one too")
}

// TestAMappedChannelTakesItsGuidesIcon: a channel mapped to a guide channel
// with an <icon> shows that icon itself, in every guide and the playlist
// (owner, 2026-10-27: iptv-epg.org's logos are public and made for a dark
// guide, where PMS refuses the tuner's private logo route); the stored copy
// the ui reads keeps it too.
func TestAMappedChannelTakesItsGuidesIcon(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	h := start(t, store, config(p.srv.URL))
	h.fetched(t)
	_, guide := h.get(t, "x", device(deviceA, "xmltv.xml"))
	assert.Contains(t, guide, `<icon src="http://logo.example/bbc1.png"`, "1001 maps to bbc1.uk, whose guide gives an icon")
	assert.NotContains(t, guide, `/logos/1001.png`)
	_, whole := h.get(t, "x", "/livetv/media/news/xmltv.xml")
	assert.Contains(t, whole, `<icon src="http://logo.example/bbc1.png"`)
}

// TestEveryLogoIsOneTheServerFetches: a channel's own spec.logo, else its
// guide channel's icon, else the playlist's logo goes into the guide as
// the URL itself, never the tuner's logos/<n>.png, whose private address
// PMS refuses (owner, 2026-10-27: the provider's own images where
// iptv-epg.org has none).
func TestEveryLogoIsOneTheServerFetches(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	for i := range cfg.Channels {
		switch cfg.Channels[i].Object {
		case "news-a": // the playlist gives a logo; its guide channel none
			cfg.Channels[i].Map = &xmltv.Mapping{Guide: tuner.GuideName, ChannelID: "news.uk"}
		case "news-e": // a channel's own logo, on a channel no guide holds
			cfg.Channels[i].Logo = "https://logos.example/own.png"
		}
	}
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	_, a := h.get(t, "x", device(deviceA, "xmltv.xml"))
	assert.Contains(t, a, `<icon src="http://logo.example/bbc1.png"`, "the playlist's logo, as it is")
	_, b := h.get(t, "x", device(deviceB, "xmltv.xml"))
	assert.Contains(t, b, `<icon src="https://logos.example/own.png"`)
	assert.NotContains(t, a+b, "/logos/")
}

func TestAPlaylistM3UListsTheRange(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	code, body := h.get(t, "x", "/livetv/media/news/playlist.m3u?from=1001&to=1002")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, `#EXTM3U
#EXTINF:-1 tvg-id="1001" tvg-chno="1001" tvg-name="BBC One" tvg-logo="http://logo.example/bbc1.png",BBC One
http://x/livetv/media/news/stream/1001
#EXTINF:-1 tvg-id="1002" tvg-chno="1002" tvg-name="News, Live" tvg-logo="",News, Live
http://x/livetv/media/news/stream/1002
`, body)
}

func TestAFailedRefreshKeepsServingTheLastLineup(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	_, before := h.get(t, "x", "/livetv/media/news/lineup.json")
	p.failPlaylist.Store(true)
	cfg.Refresh = "now"
	h.tn.Apply(cfg)
	var s tuner.Snapshot
	require.Eventually(t, func() bool { s, _ = h.tn.Snapshot(key); return s.Playlist.Error != "" }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, s.Playlist.Error, "/get.php")
	assert.NotContains(t, s.Playlist.Error, "pass")
	assert.NotContains(t, s.Playlist.Error, "username=user")
	_, after := h.get(t, "x", "/livetv/media/news/lineup.json")
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
		_, body := h.get(t, "x", "/livetv/media/news/lineup.json")
		return strings.Contains(body, "BBC One 19")
	}, 2*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	s, _ := h.tn.Snapshot(key)
	assert.NotEqual(t, first.Blocks[0].LineupHash, s.Blocks[0].LineupHash)
	assert.Equal(t, first.Blocks[1].LineupHash, s.Blocks[1].LineupHash, "the other block's lineup is unchanged")
	assert.LessOrEqual(t, h.notifed.Load()-base, int32(3))
}

// TestAProviderStartsFromItsCache: a restart serves the cached playlist and
// guide at once -- the lineup, the streams and the guide -- and fetches
// neither while it is younger than the refresh, even with the panel down
// (talos, 2026-10-20: a restart while the panel answered too slowly left
// every tune 404).
func TestAProviderStartsFromItsCache(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	cfg := config(p.srv.URL)
	h := start(t, store, cfg)
	first := h.fetched(t)
	_, lineup := h.get(t, "x", "/livetv/media/news/lineup.json")
	_, guide := h.get(t, "x", "/livetv/media/news/xmltv.xml")
	ctx := context.Background()
	require.Eventually(t, func() bool {
		_, e1 := store.Info(ctx, livetvstore.PlaylistObject(key))
		_, e2 := store.Info(ctx, livetvstore.GuideCacheObject(key))
		return e1 == nil && e2 == nil
	}, 2*time.Second, 10*time.Millisecond)

	p.failPlaylist.Store(true)
	p.failGuide.Store(true)
	playlists, guides := p.count("/get.php"), p.count("/xmltv.php")
	h2 := startAt(t, store, cfg, now.Add(30*time.Minute))
	require.Eventually(t, func() bool {
		code, body := h2.get(t, "x", "/livetv/media/news/lineup.json")
		return code == http.StatusOK && body == lineup
	}, 2*time.Second, 10*time.Millisecond, "the cached lineup is the fetched one")
	_, guide2 := h2.get(t, "x", "/livetv/media/news/xmltv.xml")
	assert.Equal(t, guide, guide2, "and so is the guide")

	resp := h2.do(t, "x", "/livetv/media/news/stream/1001", nil)
	head := make([]byte, 188)
	_, err := io.ReadFull(resp.Body, head)
	_ = resp.Body.Close()
	require.NoError(t, err, "a cached stream URL, its credentials put back, streams")
	assert.Equal(t, byte(0x47), head[0])

	var s tuner.Snapshot
	require.Eventually(t, func() bool { s, _ = h2.tn.Snapshot(key); return len(s.Blocks) == 2 && s.Blocks[1].GuideHash != "" }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, first.Blocks, s.Blocks, "the blocks' hashes hold, so cluster-plex saves no map and reloads no guide")
	assert.Equal(t, first.Playlist, s.Playlist, "the whole playlist's counts and its fetch")
	assert.Equal(t, first.Guide.Programmes, s.Guide.Programmes)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, playlists, p.count("/get.php"), "a fresh cache is not fetched again")
	assert.Equal(t, guides, p.count("/xmltv.php"))
}

func TestAStaleCacheIsServedAndFetchedAgain(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	cfg := config(p.srv.URL)
	h := start(t, store, cfg)
	h.fetched(t)
	require.Eventually(t, func() bool {
		_, err := store.Info(context.Background(), livetvstore.PlaylistObject(key))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	playlists := p.count("/get.php")
	h2 := startAt(t, store, cfg, now.Add(2*time.Hour))
	code, _ := h2.get(t, "x", "/livetv/media/news/lineup.json")
	assert.Equal(t, http.StatusOK, code)
	require.Eventually(t, func() bool { return p.count("/get.php") == playlists+1 }, 2*time.Second, 10*time.Millisecond, "older than the refresh: fetched again")
}

func TestACacheOfAnotherAccountIsNotServed(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	cfg := config(p.srv.URL)
	h := start(t, store, cfg)
	h.fetched(t)
	require.Eventually(t, func() bool {
		_, err := store.Info(context.Background(), livetvstore.PlaylistObject(key))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	other := cfg
	other.Username = "someone"
	h2 := start(t, store, other)
	code, _ := h2.get(t, "x", "/livetv/media/news/lineup.json")
	assert.Equal(t, http.StatusServiceUnavailable, code, "another account's streams are not this one's")
}

func TestNoStoredOrReportedStringCarriesACredential(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	h := start(t, store, config(p.srv.URL))
	s := h.fetched(t)
	ctx := context.Background()
	require.Eventually(t, func() bool { _, err := store.Info(ctx, livetvstore.PlaylistObject(key)); return err == nil }, 2*time.Second, 10*time.Millisecond)
	infos, err := store.List(ctx, "")
	require.NoError(t, err)
	require.NotEmpty(t, infos)
	secrets := []string{"/live/user/pass/", "username=user", "password=pass", "/user/", "/pass/"}
	for _, info := range infos {
		_, rc, err := store.Get(ctx, info.Name)
		require.NoError(t, err)
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			body, _ = io.ReadAll(zr)
		}
		for _, secret := range secrets {
			assert.NotContains(t, string(body), secret, info.Name)
		}
	}
	for _, secret := range secrets {
		assert.NotContains(t, fmt.Sprintf("%+v", s), secret)
	}
	_, rc, err := store.Get(ctx, livetvstore.PlaylistObject(key))
	require.NoError(t, err)
	zr, err := gzip.NewReader(rc)
	require.NoError(t, err)
	var doc struct {
		Entries []map[string]string `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(zr).Decode(&doc))
	_ = rc.Close()
	require.Len(t, doc.Entries, 6, "every live entry")
	assert.Equal(t, p.srv.URL+"/live/{username}/{password}/1001.ts", doc.Entries[0]["url"])
}

func TestAStreamRequestReachesTheRelay(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	// A stream is the provider's: block 1004's device serves 1001 too.
	resp := h.do(t, "livetv.media.svc", device(deviceB, "stream/1001"), nil)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "video/mp2t", resp.Header.Get("Content-Type"))
	head := make([]byte, 188*2)
	_, err := io.ReadFull(resp.Body, head)
	require.NoError(t, err)
	assert.Equal(t, byte(0x47), head[0])
	assert.Equal(t, byte(0x47), head[188])
	assert.Equal(t, int32(1), p.count("/live/user/pass/1001.ts"))
	s, _ := h.tn.Snapshot(key)
	assert.Equal(t, 1, s.TunersInUse)
	code, _ := h.get(t, "x", "/livetv/media/news/stream/9999")
	assert.Equal(t, http.StatusNotFound, code)
}

// TestTheAccountSetsTheTuners: the relay's limit is the account's
// max_connections unless spec.tuners overrides it.
func TestTheAccountSetsTheTuners(t *testing.T) {
	p := newProvider(t)
	p.maxConns.Store(3)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	s := h.fetched(t)
	assert.Equal(t, 3, s.Tuners)
	_, body := h.get(t, "x", device(deviceA, "discover.json"))
	assert.Contains(t, body, fmt.Sprintf(`"TunerCount":%d`, 3+tuner.SpareTuners))

	cfg.Tuners = 2
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return s.Tuners == 2 }, 2*time.Second, 10*time.Millisecond, "the override wins")
}

func TestANewRefreshValueRefetchesEverySource(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	pl, gd, ac := p.count("/get.php"), p.count("/xmltv.php"), p.count("/player_api.php")
	cfg.Refresh = "1"
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool {
		return p.count("/get.php") == pl+1 && p.count("/xmltv.php") == gd+1 && p.count("/player_api.php") == ac+1
	}, 5*time.Second, 10*time.Millisecond)
	h.tn.Apply(cfg) // unchanged
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, pl+1, p.count("/get.php"))
	cfg.Refresh = "2"
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { return p.count("/get.php") == pl+2 }, 5*time.Second, 10*time.Millisecond)
}

// guideURL is the fixture panel's guide served under another path, its
// credentials named by placeholders.
func guideURL(p *provider) string { return p.srv.URL + "/epg/{username}/guide.xml?key={password}" }

// TestAGuideURLIsFetchedInsteadOfThePanels: spec.guideURL replaces the
// panel's xmltv.php, its placeholders filled with the account's credentials
// (owner, 2026-10-27: iptv-epg.org's US guide on argon-tv).
func TestAGuideURLIsFetchedInsteadOfThePanels(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	cfg.GuideURL = guideURL(p)
	h := start(t, newStore(t), cfg)
	s := h.fetched(t)
	assert.Equal(t, int32(1), p.count("/epg/user/guide.xml"))
	assert.Positive(t, s.Guide.Programmes)
	assert.Empty(t, s.Guide.Error)
	_, guide := h.get(t, "x", device(deviceA, "xmltv.xml"))
	assert.Contains(t, guide, "The One Show (epg)", "a channel both guides hold reads the guideURL's")
	assert.NotContains(t, guide, ">The One Show<")
}

// TestAChannelTheGuideURLLacksReadsThePanelsGuide: the panel's own
// xmltv.php is fetched beside a guideURL, and a channel the guideURL's guide
// does not hold takes its programmes from it (owner, 2026-10-27: Adult Swim
// is not in iptv-epg.org's US guide).
func TestAChannelTheGuideURLLacksReadsThePanelsGuide(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	cfg.GuideURL = guideURL(p)
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	assert.Equal(t, int32(1), p.count("/xmltv.php"), "the panel's guide is the fallback")
	_, guide := h.get(t, "x", device(deviceA, "xmltv.xml"))
	assert.Contains(t, guide, ">Bulletin<", "1002 auto-maps to news.uk, which only the panel's guide has")
	assert.Contains(t, guide, "The One Show (epg)")
}

// TestAFallbackChannelTakesThePanelsIconOnlyWhenItLoads: a channel the
// panel's guide fills takes that guide's icon as its logo (owner,
// 2026-10-27: the provider's own image where iptv-epg.org has none), and
// an icon that does not load is dropped at the fetch, so the playlist's
// logo, or none, stands instead (the panel's 103.176.90.95 and .118 are
// dead).
func TestAFallbackChannelTakesThePanelsIconOnlyWhenItLoads(t *testing.T) {
	for _, tc := range []struct {
		state int32
		want  bool
	}{{1, true}, {2, false}} {
		p := newProvider(t)
		p.newsIcon.Store(tc.state)
		cfg := config(p.srv.URL)
		cfg.GuideURL = guideURL(p)
		h := start(t, newStore(t), cfg)
		h.fetched(t)
		_, guide := h.get(t, "x", device(deviceA, "xmltv.xml"))
		require.Contains(t, guide, ">Bulletin<")
		assert.Equal(t, tc.want, strings.Contains(guide, `/picons/news.png"`), "icon state %d", tc.state)
	}
}

// TestAFailedFallbackKeepsTheGuideURLsGuide: the panel's half failing is no
// guide failure; the guideURL's channels are served as fetched.
func TestAFailedFallbackKeepsTheGuideURLsGuide(t *testing.T) {
	p := newProvider(t)
	p.failGuide.Store(true)
	cfg := config(p.srv.URL)
	cfg.GuideURL = guideURL(p)
	h := start(t, newStore(t), cfg)
	s := h.fetched(t)
	assert.Empty(t, s.Guide.Error)
	_, guide := h.get(t, "x", device(deviceA, "xmltv.xml"))
	assert.Contains(t, guide, "The One Show (epg)")
	assert.NotContains(t, guide, ">Bulletin<")
}

// TestAChangedGuideURLRefetchesTheGuideAlone: a new guideURL is fetched at
// once, and the playlist and the account are not.
func TestAChangedGuideURLRefetchesTheGuideAlone(t *testing.T) {
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	pl, ac := p.count("/get.php"), p.count("/player_api.php")
	cfg.GuideURL = guideURL(p)
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { return p.count("/epg/user/guide.xml") == 1 }, 5*time.Second, 10*time.Millisecond)
	h.tn.Apply(cfg) // unchanged
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(1), p.count("/epg/user/guide.xml"))
	assert.Equal(t, pl, p.count("/get.php"))
	assert.Equal(t, ac, p.count("/player_api.php"))
}

// TestACachedGuideOfAnotherSourceIsFetchedAgain: a restart whose guideURL
// differs from the cached guide's source serves the cache and fetches the
// new source at once, however fresh the cache is -- a deploy that changes
// the URL restarts the manager too.
func TestACachedGuideOfAnotherSourceIsFetchedAgain(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	cfg := config(p.srv.URL)
	h := start(t, store, cfg)
	h.fetched(t)
	require.Eventually(t, func() bool {
		_, err := store.Info(context.Background(), livetvstore.GuideCacheObject(key))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	playlists := p.count("/get.php")
	other := cfg
	other.GuideURL = guideURL(p)
	h2 := startAt(t, store, other, now.Add(30*time.Minute))
	code, _ := h2.get(t, "x", "/livetv/media/news/xmltv.xml")
	assert.Equal(t, http.StatusOK, code, "the cached guide is served meanwhile")
	require.Eventually(t, func() bool { return p.count("/epg/user/guide.xml") == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, playlists, p.count("/get.php"), "the fresh playlist cache stands")
}

func TestRemoveEndsTheProvider(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	h.tn.Remove(key)
	code, _ := h.get(t, "x", device(deviceA, "discover.json"))
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = h.get(t, "x", "/livetv/media/news/discover.json")
	assert.Equal(t, http.StatusNotFound, code)
	_, ok := h.tn.Snapshot(key)
	assert.False(t, ok)
}

func TestAFailedFirstFetchIsRetriedLongBeforeTheRefresh(t *testing.T) {
	// A manager restart whose first fetch fails once (a 5xx, a timeout, DNS
	// not ready) must not leave Live TV down until the next refresh.
	p := newProvider(t)
	p.failPlaylist.Store(true)
	p.failGuide.Store(true)
	cfg := config(p.srv.URL)
	cfg.Every = 24 * time.Hour
	h := start(t, newStore(t), cfg)
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return s.Playlist.Error != "" }, 5*time.Second, 10*time.Millisecond)

	p.failPlaylist.Store(false)
	h.clock.Add(int64(time.Minute))
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return s.PlaylistFetched }, 5*time.Second, 10*time.Millisecond, "the playlist is retried within a minute")
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return s.Guide.Error != "" }, 5*time.Second, 10*time.Millisecond)
	p.failGuide.Store(false)
	h.clock.Add(int64(time.Minute))
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return !s.Guide.FetchedAt.IsZero() }, 5*time.Second, 10*time.Millisecond, "and so is the guide")
}

func TestAPlaylistWithNoLiveEntriesKeepsTheLastOne(t *testing.T) {
	// An Xtream panel in maintenance, or a line just expired, answers
	// #EXTM3U and nothing else; that is a failed fetch, not an empty lineup.
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := start(t, newStore(t), cfg)
	before := h.fetched(t)
	_, lineup := h.get(t, "x", "/livetv/media/news/lineup.json")

	p.emptyPlaylist.Store(true)
	cfg.Refresh = "now"
	h.tn.Apply(cfg)
	var s tuner.Snapshot
	require.Eventually(t, func() bool { s, _ = h.tn.Snapshot(key); return s.Playlist.Error != "" }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, before.Playlist.Entries, s.Playlist.Entries)
	assert.Equal(t, before.Channels, s.Channels, "no channel goes Missing")
	_, again := h.get(t, "x", "/livetv/media/news/lineup.json")
	assert.Equal(t, lineup, again)
}

func TestTheGuideHashMovesWithTheGuideNotTheClock(t *testing.T) {
	// cluster-plex reloads a DVR's guide whenever its hash changes, and each
	// reload is a provider.epg.load in PMS: a hash that changes as
	// programmes end would reload every guide all day.
	p := newProvider(t)
	cfg := config(p.srv.URL)
	h := startAt(t, newStore(t), cfg, now.Add(10*time.Hour))
	before := h.fetched(t)
	time.Sleep(50 * time.Millisecond)
	notified := h.notifed.Load()

	h.clock.Add(int64(9*time.Hour + 30*time.Minute)) // 19:30: one programme has ended, the dummy hours moved
	h.tn.Apply(cfg)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, notified, h.notifed.Load(), "an unchanged configuration renders nothing")

	cfg.Tuners = 2 // a change that does not touch the guide
	h.tn.Apply(cfg)
	var s tuner.Snapshot
	require.Eventually(t, func() bool { s, _ = h.tn.Snapshot(key); return s.Tuners == 2 }, 2*time.Second, 10*time.Millisecond)
	require.Len(t, s.Blocks, 2)
	assert.Equal(t, before.Blocks[0].GuideHash, s.Blocks[0].GuideHash)
	assert.Equal(t, before.Blocks[1].GuideHash, s.Blocks[1].GuideHash, "the dummy guide's hours are the day's")
}

func TestNoChannelReadsMissingBeforeTheFirstFetch(t *testing.T) {
	// After a manager restart every channel would otherwise flip to Missing
	// and back: a status write each way per channel.
	tn := tuner.New(tuner.Options{Store: newStore(t), HTTP: http.DefaultClient, Now: func() time.Time { return now }, Debounce: time.Millisecond})
	tn.Apply(config("http://127.0.0.1:1"))
	var s tuner.Snapshot
	require.Eventually(t, func() bool { var ok bool; s, ok = tn.Snapshot(key); return ok && len(s.Blocks) == 2 }, 2*time.Second, time.Millisecond)
	assert.False(t, s.PlaylistFetched)
	for name, st := range s.Channels {
		assert.False(t, st.Missing, "%s: not Missing before the playlist is fetched", name)
	}
	assert.Zero(t, s.Lineup.Missing)
}

func TestATunerCountChangeIsItsOwnNotice(t *testing.T) {
	// A tune changes no channel's status: it must not wake every channel.
	p := newProvider(t)
	h := start(t, newStore(t), config(p.srv.URL))
	h.fetched(t)
	time.Sleep(50 * time.Millisecond)
	snapshots := h.notifed.Load()

	resp := h.do(t, "x", "/livetv/media/news/stream/1001", nil)
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	require.Eventually(t, func() bool { return h.tuners.Load() > 0 }, 2*time.Second, 10*time.Millisecond)
	_ = resp.Body.Close()
	assert.Equal(t, snapshots, h.notifed.Load(), "no snapshot notice for a tune")
}

// ADR-0021 §2: every object in a store describes itself -- its store's
// meta-version, what it is and who wrote it -- so an audit can tell objects
// apart without parsing their names.
func TestEveryStoredObjectDescribesItself(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	h := start(t, store, config(p.srv.URL))
	h.fetched(t)
	ctx := context.Background()
	require.Eventually(t, func() bool {
		for _, name := range []string{livetvstore.PlaylistObject(key), livetvstore.GuideCacheObject(key), livetvstore.GuideChannelsObject(key),
			livetvstore.GuideObject(key), livetvstore.GuideIndexObject(key)} {
			if _, err := store.Info(ctx, name); err != nil {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)
	infos, err := store.List(ctx, "")
	require.NoError(t, err)
	kinds := map[string]bool{}
	for _, info := range infos {
		md := info.Metadata
		assert.Equal(t, events.LiveTVMetaVersion, md[events.ObjectMetaKeyVersion], info.Name)
		kind, _, _ := strings.Cut(info.Name, "/")
		assert.Equal(t, kind, md[events.ObjectMetaKeyKind], info.Name)
		assert.True(t, strings.HasPrefix(md[events.ObjectMetaKeyWrittenBy], "manager@"), "%s: %q", info.Name, md[events.ObjectMetaKeyWrittenBy])
		kinds[kind] = true
	}
	assert.Equal(t, map[string]bool{
		events.LiveTVMetaKindPlaylist: true, events.LiveTVMetaKindGuideCache: true,
		events.LiveTVMetaKindGuideChannels: true, events.LiveTVMetaKindGuide: true, events.LiveTVMetaKindGuideIndex: true,
	}, kinds)
}

// TestTheGuideIndexIsStoredWithTheGuide: each render that stores the
// provider's XEPG guide stores its index beside it, for the ui's TV Guide,
// with the logos the lineup gives Plex.
func TestTheGuideIndexIsStoredWithTheGuide(t *testing.T) {
	p := newProvider(t)
	store := newStore(t)
	h := start(t, store, config(p.srv.URL))
	h.fetched(t)
	var ix livetvstore.GuideIndex
	require.Eventually(t, func() bool {
		_, rc, err := store.Get(context.Background(), livetvstore.GuideIndexObject(key))
		if err != nil {
			return false
		}
		defer func() { _ = rc.Close() }()
		return livetvstore.Decode(rc, &ix) == nil
	}, 5*time.Second, 10*time.Millisecond)
	var numbers []string
	logos := map[string]string{}
	for _, c := range ix.Channels {
		numbers = append(numbers, c.Number)
		logos[c.Number] = c.Logo
	}
	assert.Equal(t, []string{"1001", "1002", "1004"}, numbers)
	assert.Equal(t, "http://logo.example/bbc1.png", logos["1001"], "1001's guide channel's icon, as Plex is given")
	perChannel := map[string]int{}
	for _, pr := range ix.Programmes {
		perChannel[pr.Channel]++
	}
	assert.Positive(t, perChannel["1001"], "the mapped channel's programmes")
	assert.Equal(t, now.UTC().Truncate(24*time.Hour), ix.From)
}

// TestTheStoredGuideCarriesTheLogosPlexIsGiven: the whole-provider guide in
// the store is the one Plex's blocks are built from, its logos included --
// a mapped channel's guide icon, not its playlist logo.
func TestTheStoredGuideCarriesTheLogosPlexIsGiven(t *testing.T) {
	p := newProvider(t)
	p.newsIcon.Store(1)
	cfg := config(p.srv.URL)
	for i := range cfg.Channels {
		if cfg.Channels[i].Object == "news-a" { // the playlist's logo is bbc1.png
			cfg.Channels[i].Map = &xmltv.Mapping{Guide: tuner.GuideName, ChannelID: "news.uk"}
		}
	}
	store := newStore(t)
	h := start(t, store, cfg)
	h.fetched(t)
	var whole []byte
	require.Eventually(t, func() bool {
		_, rc, err := store.Get(context.Background(), livetvstore.GuideObject(key))
		if err != nil {
			return false
		}
		defer func() { _ = rc.Close() }()
		zr, err := gzip.NewReader(rc)
		if err != nil {
			return false
		}
		whole, err = io.ReadAll(zr)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, string(whole), `<channel id="1001"><display-name>BBC One</display-name><icon src="`+p.srv.URL+`/picons/news.png">`)
}

// logoServer serves a dark wordmark on transparency at /dark.png, a red one
// at /red.png and 404 elsewhere, counting requests by path.
func logoServer(t *testing.T) (*httptest.Server, func(path string) int32) {
	t.Helper()
	encode := func(c color.NRGBA) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
		for y := 6; y < 14; y++ {
			for x := 4; x < 36; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
		var b bytes.Buffer
		require.NoError(t, png.Encode(&b, img))
		return b.Bytes()
	}
	dark, red := encode(color.NRGBA{A: 255}), encode(color.NRGBA{R: 200, G: 30, B: 30, A: 255})
	var hits sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := hits.LoadOrStore(r.URL.Path, new(atomic.Int32))
		c.(*atomic.Int32).Add(1)
		switch r.URL.Path {
		case "/dark.png":
			_, _ = w.Write(dark)
		case "/red.png":
			_, _ = w.Write(red)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func(path string) int32 {
		c, ok := hits.Load(path)
		if !ok {
			return 0
		}
		return c.(*atomic.Int32).Load()
	}
}

// withLogos gives news-a and news-b logos of their own on srv.
func withLogos(cfg tuner.Config, a, b string) tuner.Config {
	for i := range cfg.Channels {
		switch cfg.Channels[i].Object {
		case "news-a":
			cfg.Channels[i].Logo = a
		case "news-b":
			cfg.Channels[i].Logo = b
		}
	}
	return cfg
}

// TestEveryActiveChannelsLogoIsStored: the tuner keeps each active channel's
// logo, processed as the logos/<n>.png route serves it, in the store, so the
// ui shows it without a request to the logo's host; a logo already stored
// is not fetched again.
func TestEveryActiveChannelsLogoIsStored(t *testing.T) {
	p := newProvider(t)
	logos, hits := logoServer(t)
	cfg := withLogos(config(p.srv.URL), logos.URL+"/dark.png", logos.URL+"/red.png")
	store := newStore(t)
	h := start(t, store, cfg)
	h.fetched(t)
	ctx := context.Background()
	for _, src := range []string{logos.URL + "/dark.png", logos.URL + "/red.png"} {
		require.Eventually(t, func() bool {
			_, err := store.Info(ctx, livetvstore.LogoObject(key, src))
			return err == nil
		}, 5*time.Second, 10*time.Millisecond, src)
	}
	_, rc, err := store.Get(ctx, livetvstore.LogoObject(key, logos.URL+"/dark.png"))
	require.NoError(t, err)
	img, err := png.Decode(rc)
	_ = rc.Close()
	require.NoError(t, err)
	assert.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBAModel.Convert(img.At(10, 10)), "the dark wordmark reads white")

	cfg.Tuners = 3 // a config change renders again
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return s.Tuners == 3 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), hits("/dark.png"), "a stored logo is not fetched again")
	assert.Equal(t, int32(1), hits("/red.png"))
}

// TestALogoThatFailsWaitsAnHourBeforeItIsFetchedAgain.
func TestALogoThatFailsWaitsAnHourBeforeItIsFetchedAgain(t *testing.T) {
	p := newProvider(t)
	logos, hits := logoServer(t)
	cfg := withLogos(config(p.srv.URL), logos.URL+"/gone.png", "")
	h := start(t, newStore(t), cfg)
	h.fetched(t)
	require.Eventually(t, func() bool { return hits("/gone.png") == 1 }, 5*time.Second, 10*time.Millisecond)

	cfg.Tuners = 3
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { s, _ := h.tn.Snapshot(key); return s.Tuners == 3 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), hits("/gone.png"), "not again within the hour")

	h.clock.Add(int64(61 * time.Minute))
	cfg.Tuners = 4
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool { return hits("/gone.png") == 2 }, 5*time.Second, 10*time.Millisecond, "asked again after the hour")
}

// m3uConfig is config's lineup on an M3U provider: a playlist URL naming
// the account's credentials, no Xtream panel.
func m3uConfig(srv string) tuner.Config {
	c := config(srv)
	c.Type, c.Server = "M3U", ""
	c.PlaylistURLTemplate = srv + "/lists/{username}.m3u?key={password}"
	return c
}

// TestAnM3UProviderFetchesItsPlaylistURLAndNoAccount: an M3U provider's
// playlist is its URL with the credentials filled in; it has no panel, so
// no player_api.php and no xmltv.php, and its guide is spec.guideURL's
// alone.
func TestAnM3UProviderFetchesItsPlaylistURLAndNoAccount(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), m3uConfig(p.srv.URL))
	require.Eventually(t, func() bool {
		s, ok := h.tn.Snapshot(key)
		return ok && s.PlaylistFetched && s.Lineup.Active > 0
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Positive(t, p.count("/lists/user.m3u"))
	assert.Zero(t, p.count("/player_api.php"), "no account to ask")
	assert.Zero(t, p.count("/xmltv.php"), "no panel guide")
	assert.Zero(t, p.count("/get.php"))

	withGuide := m3uConfig(p.srv.URL)
	withGuide.GuideURL = p.srv.URL + "/epg/{username}/guide.xml?key={password}"
	h.tn.Apply(withGuide)
	require.Eventually(t, func() bool { return p.count("/epg/user/guide.xml") > 0 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, p.count("/xmltv.php"), "no panel fallback either")
}

// TestAnM3UProviderWithoutAGuideURLHasDummyGuides: with no guide, every
// channel gets the dummy guide, titled by its name.
func TestAnM3UProviderWithoutAGuideURLHasDummyGuides(t *testing.T) {
	p := newProvider(t)
	h := start(t, newStore(t), m3uConfig(p.srv.URL))
	var guide string
	require.Eventually(t, func() bool {
		code, body := h.get(t, "x", device(deviceA, "xmltv.xml"))
		guide = body
		return code == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, guide, "<title>BBC One</title>", "hourly blocks titled by the channel")
	s, _ := h.tn.Snapshot(key)
	assert.Zero(t, s.Tuners-1, "spec.tuners unset and no account: one tuner")
}
