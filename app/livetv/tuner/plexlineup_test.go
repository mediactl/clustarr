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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
)

const (
	plexLineupID = "657c8183afd0f02cd353633f"
	plexToken    = "plex-token-secret"
	plexLineup   = "lineup://tv.plex.providers.epg.cloud/" + plexLineupID + "#Test%20Lineup"
)

var plexDevice = hdhr.BlockDeviceID("media", "news", "plex/"+plexLineupID+"/1001")

// plexCloud is Plex's cloud guide: the lineup and its stations, BBC One's
// and News, Live's among them.
type plexCloud struct {
	srv   *httptest.Server
	token atomic.Value // the last X-Plex-Token
	fail  atomic.Bool
}

func newPlexCloud(t *testing.T) *plexCloud {
	c := &plexCloud{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.token.Store(r.Header.Get("X-Plex-Token"))
		if c.fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/lineups/" + plexLineupID:
			_, _ = w.Write([]byte(`{"MediaContainer":{"Lineup":[{"id":"` + plexLineupID + `","title":"Test Lineup"}]}}`))
		case "/lineups/" + plexLineupID + "/channels":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Channel":[
				{"id":"` + plexLineupID + `-aaaaaaaaaaaaaaaaaaaaaaa1","gridKey":"aaaaaaaaaaaaaaaaaaaaaaa1","callSign":"BBCONEH","title":"BBC One HD","vcn":"101","isHd":true},
				{"id":"` + plexLineupID + `-aaaaaaaaaaaaaaaaaaaaaaa2","gridKey":"aaaaaaaaaaaaaaaaaaaaaaa2","callSign":"NEWSLV","title":"News, Live","vcn":"102"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// redirect sends epg.provider.plex.tv to the fake cloud, everything else
// where it was going.
type redirect struct{ to *url.URL }

func (rt redirect) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "epg.provider.plex.tv" {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = rt.to.Scheme, rt.to.Host
	}
	return http.DefaultTransport.RoundTrip(r)
}

// plexConfig is config with the first block on the Plex lineup.
func plexConfig(srv string) tuner.Config {
	cfg := config(srv)
	cfg.Plex = &tuner.PlexConfig{LineupID: plexLineupID, Token: plexToken, Enabled: true}
	cfg.Blocks[0] = tuner.BlockConfig{Start: "1001", DeviceID: plexDevice, FriendlyName: "Clustarr news 1001+", Numbers: []string{"1001", "1002"}, Lineup: plexLineup}
	return cfg
}

func startPlex(t *testing.T, p *provider, cloud *plexCloud, cfg tuner.Config) *harness {
	u, err := url.Parse(cloud.srv.URL)
	require.NoError(t, err)
	return startWith(t, newStore(t), cfg, now, func(o *tuner.Options) {
		o.Fetch = &http.Client{Transport: redirect{to: u}, Timeout: 10 * time.Second}
	})
}

// paired waits for a render that paired the channels with the lineup.
func (h *harness) paired(t *testing.T) tuner.Snapshot {
	t.Helper()
	var snap tuner.Snapshot
	require.Eventually(t, func() bool {
		s, ok := h.tn.Snapshot(key)
		snap = s
		a := s.Channels["news-a"]
		return ok && s.PlaylistFetched && s.Plex.Loaded && a.Plex != nil && len(s.Blocks) == 2 && s.Blocks[0].LineupHash != ""
	}, 5*time.Second, 10*time.Millisecond)
	return snap
}

func TestPlexMapIsServedForAPlexBlockOnly(t *testing.T) {
	p, cloud := newProvider(t), newPlexCloud(t)
	h := startPlex(t, p, cloud, plexConfig(p.srv.URL))
	snap := h.paired(t)
	assert.Equal(t, plexguide.Automatic, snap.Channels["news-a"].Plex.Match)
	assert.Equal(t, "BBCONEH", snap.Channels["news-a"].Plex.Station.CallSign)
	assert.Equal(t, "Test Lineup", snap.Plex.Title)
	assert.Equal(t, 2, snap.Plex.Stations)
	assert.Empty(t, snap.Blocks[0].GuideHash, "a Plex block's guide is Plex's")

	code, body := h.get(t, "tuner.example", device(plexDevice, "plexmap.json"))
	require.Equal(t, http.StatusOK, code)
	var m []tuner.PlexMapping
	require.NoError(t, json.Unmarshal([]byte(body), &m))
	require.Len(t, m, 2)
	assert.Equal(t, tuner.PlexMapping{DeviceIdentifier: "1001", ChannelKey: plexLineupID + "-aaaaaaaaaaaaaaaaaaaaaaa1", LineupIdentifier: "101"}, m[0])
	assert.Equal(t, "1002", m[1].DeviceIdentifier)

	code, body = h.get(t, "tuner.example", device(plexDevice, "lineup.json"))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"1001", "1002"}, numbers(lineupOf(t, body)), "only its paired channels")
	code, _ = h.get(t, "tuner.example", device(plexDevice, "xmltv.xml"))
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = h.get(t, "tuner.example", device(deviceB, "plexmap.json"))
	assert.Equal(t, http.StatusNotFound, code, "an XMLTV block has no Plex map")
}

func TestTheLineupHashCoversThePlexMap(t *testing.T) {
	p, cloud := newProvider(t), newPlexCloud(t)
	cfg := plexConfig(p.srv.URL)
	h := startPlex(t, p, cloud, cfg)
	before := h.paired(t).Blocks[0].LineupHash

	// News, Live renumbered within the block: the map's device number moves.
	cfg.Channels[1].Number = "1003"
	cfg.Blocks[0].Numbers = []string{"1001", "1003"}
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool {
		s, _ := h.tn.Snapshot(key)
		return len(s.Blocks) > 0 && s.Blocks[0].LineupHash != before
	}, 5*time.Second, 10*time.Millisecond)
	_, body := h.get(t, "tuner.example", device(plexDevice, "plexmap.json"))
	assert.Contains(t, body, `"deviceIdentifier":"1003"`)
}

func TestTheLineupFetchSendsTheTokenAndRedactsIt(t *testing.T) {
	p, cloud := newProvider(t), newPlexCloud(t)
	cloud.fail.Store(true)
	h := startPlex(t, p, cloud, plexConfig(p.srv.URL))
	var snap tuner.Snapshot
	require.Eventually(t, func() bool {
		s, ok := h.tn.Snapshot(key)
		snap = s
		return ok && s.Plex.Error != ""
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, plexToken, cloud.token.Load())
	assert.NotContains(t, snap.Plex.Error, plexToken)
	assert.False(t, snap.Plex.Loaded)
	for _, c := range snap.Channels {
		if c.Plex != nil {
			assert.Equal(t, plexguide.NoLineup, c.Plex.Match, "no lineup held: nothing paired")
		}
	}
}

func TestARecordedPairingIsServedUntilTheLineupLands(t *testing.T) {
	p, cloud := newProvider(t), newPlexCloud(t)
	cloud.fail.Store(true)
	cfg := plexConfig(p.srv.URL)
	st := plexguide.Station{ID: plexLineupID + "-aaaaaaaaaaaaaaaaaaaaaaa1", GridKey: "aaaaaaaaaaaaaaaaaaaaaaa1", CallSign: "BBCONEH", VCN: "101"}
	cfg.Channels[0].PlexPrev = plexguide.Prev{Match: plexguide.Automatic, Station: st}
	h := startPlex(t, p, cloud, cfg)
	require.Eventually(t, func() bool {
		code, body := h.get(t, "tuner.example", device(plexDevice, "plexmap.json"))
		return code == http.StatusOK && strings.Contains(body, `"channelKey":"`+st.ID+`"`)
	}, 5*time.Second, 10*time.Millisecond, "a restart with no lineup keeps the block's map")
}

// TestALineupFetchOverTakenByAnotherLineupIsDropped: a fetch of the old
// lineup that lands after the configuration named another is never held,
// paired against or stored.
func TestALineupFetchOverTakenByAnotherLineupIsDropped(t *testing.T) {
	const other = "657cb9b8a698374a7a2dbca0"
	p := newProvider(t)
	release, releaseNew := make(chan struct{}), make(chan struct{})
	var oldDone atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lineups/" + plexLineupID:
			<-release // the old lineup's fetch is in flight when the configuration moves
			_, _ = w.Write([]byte(`{"MediaContainer":{"Lineup":[{"title":"Old"}]}}`))
		case "/lineups/" + plexLineupID + "/channels":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Channel":[{"id":"x-a","gridKey":"aaaaaaaaaaaaaaaaaaaaaaa1","callSign":"BBCONEH","title":"BBC One HD","vcn":"101"}]}}`))
			oldDone.Add(1)
		case "/lineups/" + other:
			<-releaseNew
			_, _ = w.Write([]byte(`{"MediaContainer":{"Lineup":[{"title":"New"}]}}`))
		case "/lineups/" + other + "/channels":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Channel":[{"id":"y-a","gridKey":"aaaaaaaaaaaaaaaaaaaaaaa1","callSign":"BBCONEH","title":"BBC One HD","vcn":"201"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cloud.Close)
	cfg := plexConfig(p.srv.URL)
	h := startPlex(t, p, &plexCloud{srv: cloud}, cfg)
	time.Sleep(100 * time.Millisecond) // the old lineup's fetch is waiting
	cfg.Plex.LineupID = other
	h.tn.Apply(cfg)
	close(release)
	require.Eventually(t, func() bool { return oldDone.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond) // the old fetch's result, and any render it asked for
	s, _ := h.tn.Snapshot(key)
	assert.NotEqual(t, plexLineupID, s.Plex.LineupID, "the old lineup is never held")
	close(releaseNew)
	require.Eventually(t, func() bool {
		s, _ := h.tn.Snapshot(key)
		return s.Plex.Loaded && s.Plex.LineupID == other
	}, 5*time.Second, 10*time.Millisecond)
	s, _ = h.tn.Snapshot(key)
	assert.Equal(t, "New", s.Plex.Title)
	for obj, c := range s.Channels {
		if c.Plex != nil && c.Plex.Match.OnPlex() {
			assert.Equal(t, "201", c.Plex.Station.VCN, "%s paired against the new lineup only", obj)
		}
	}
}
