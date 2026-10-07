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
	"net"
	"net/http"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
)

// Handler serves every block's HDHomeRun surface (spec §5.0, §5.3):
//
//   - at the root, /discover.json, /lineup_status.json, /lineup.json,
//     /lineup.post, /device.xml, /xmltv.xml and /stream/<number> for the
//     block whose port the request's Host names: Plex reaches each block
//     through a proxy port in its own pod, and the proxy passes Host
//     untouched;
//   - under /livetv/<ns>/<name>/<start>/, the same for any Host, for curl and
//     the e2e tests, and /livetv/<ns>/<name>/xmltv.xml for the whole
//     provider's guide.
//
// Every URL handed out is built from the request's Host, so later requests
// come back the way the first one came.
func (t *Tuner) Handler() http.Handler { return http.HandlerFunc(t.serveHTTP) }

func (t *Tuner) serveHTTP(w http.ResponseWriter, r *http.Request) {
	var (
		p           *provider
		start, rest string
		base        string
	)
	if after, ok := strings.CutPrefix(r.URL.Path, "/livetv/"); ok {
		parts := strings.SplitN(after, "/", 4)
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		t.mu.Lock()
		p = t.providers[types.NamespacedName{Namespace: parts[0], Name: parts[1]}]
		t.mu.Unlock()
		if p != nil && len(parts) == 3 && parts[2] == "xmltv.xml" {
			t.serveProviderGuide(w, r, p)
			return
		}
		if len(parts) < 4 {
			http.NotFound(w, r)
			return
		}
		start, rest = parts[2], parts[3]
		base = "http://" + r.Host + "/livetv/" + parts[0] + "/" + parts[1] + "/" + start
	} else {
		p, start = t.byPort(portOf(r.Host))
		rest = strings.TrimPrefix(r.URL.Path, "/")
		base = "http://" + r.Host
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	cfg := p.cfg
	var block *BlockConfig
	for i := range cfg.Blocks {
		if cfg.Blocks[i].Start == start {
			block = &cfg.Blocks[i]
			break
		}
	}
	sf := p.blocks[start]
	var entries []hdhr.LineupEntry
	var guideXML []byte
	if sf != nil {
		entries, guideXML = sf.entries, sf.guideXML
	}
	fetched := p.playlistFetched
	upstream, streamOK := "", false
	number, isStream := strings.CutPrefix(rest, "stream/")
	if isStream {
		upstream, streamOK = p.streams[number]
	}
	rl := p.relay
	p.mu.Unlock()
	if block == nil {
		http.NotFound(w, r)
		return
	}

	device := hdhr.Device{FriendlyName: block.FriendlyName, DeviceID: block.DeviceID, BaseURL: base, TunerCount: cfg.Tuners}
	switch {
	case rest == "discover.json":
		writeBody(w, "application/json", hdhr.Discover(device))
	case rest == "lineup_status.json":
		writeBody(w, "application/json", hdhr.LineupStatus())
	case rest == "lineup.json":
		if !fetched {
			http.Error(w, "the playlist has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		out := make([]hdhr.LineupEntry, len(entries))
		for i, e := range entries {
			e.URL = base + "/" + e.URL
			out[i] = e
		}
		writeBody(w, "application/json", hdhr.Lineup(out))
	case rest == "lineup.post":
		w.WriteHeader(http.StatusOK)
	case rest == "device.xml":
		writeBody(w, "application/xml", hdhr.DeviceXML(device))
	case rest == "xmltv.xml":
		if cfg.EPGSource == "PMS" || guideXML == nil {
			http.NotFound(w, r)
			return
		}
		writeBody(w, "application/xml", guideXML)
	case isStream && streamOK:
		rl.Serve(w, r, number, upstream)
	default:
		http.NotFound(w, r)
	}
}

// serveProviderGuide answers /livetv/<ns>/<name>/xmltv.xml: every block's
// channels in one guide.
func (t *Tuner) serveProviderGuide(w http.ResponseWriter, r *http.Request, p *provider) {
	p.mu.Lock()
	pms, guideXML := p.cfg.EPGSource == "PMS", p.guideXML
	p.mu.Unlock()
	if pms || guideXML == nil {
		http.NotFound(w, r)
		return
	}
	writeBody(w, "application/xml", guideXML)
}

func writeBody(w http.ResponseWriter, contentType string, b []byte) {
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

// portOf is a Host header's port, or 0 when it names none.
func portOf(host string) int32 {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(port, 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return int32(n)
}
