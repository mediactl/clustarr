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
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
)

// Handler serves every provider's HDHomeRun surface (spec §5.3):
//
//   - at the root, /discover.json, /lineup_status.json, /lineup.json,
//     /lineup.post and /device.xml for the provider whose address is the
//     request's Host, since Plex adds a device by address and reads its
//     root;
//   - under /livetv/<ns>/<name>/, the same plus xmltv.xml and
//     stream/<number>, for any Host: every URL the tuner hands out is one
//     of these.
func (t *Tuner) Handler() http.Handler { return http.HandlerFunc(t.serveHTTP) }

func (t *Tuner) serveHTTP(w http.ResponseWriter, r *http.Request) {
	var (
		p    *provider
		rest string
	)
	if after, ok := strings.CutPrefix(r.URL.Path, "/livetv/"); ok {
		parts := strings.SplitN(after, "/", 3)
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		t.mu.Lock()
		p = t.providers[types.NamespacedName{Namespace: parts[0], Name: parts[1]}]
		t.mu.Unlock()
		rest = parts[2]
	} else {
		p = t.byHost(hostOnly(r.Host))
		rest = strings.TrimPrefix(r.URL.Path, "/")
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	p.mu.Lock()
	cfg := p.cfg
	fetched, lineup, guideXML := p.playlistFetched, p.lineup, p.guideXML
	upstream, streamOK := "", false
	number, isStream := strings.CutPrefix(rest, "stream/")
	if isStream {
		upstream, streamOK = p.streams[number]
	}
	rl := p.relay
	p.mu.Unlock()

	device := hdhr.Device{FriendlyName: cfg.FriendlyName, DeviceID: cfg.DeviceID, BaseURL: baseURL(cfg), TunerCount: cfg.Tuners}
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
		writeBody(w, "application/json", lineup)
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

func writeBody(w http.ResponseWriter, contentType string, b []byte) {
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

// byHost is the provider whose address is host.
func (t *Tuner) byHost(host string) *provider {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.providers {
		p.mu.Lock()
		addr := strings.ToLower(p.cfg.Address)
		p.mu.Unlock()
		if addr != "" && addr == host {
			return p
		}
	}
	return nil
}

// hostOnly is a Host header without its port, lowercased.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}
