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

// Package iptvstub is the e2e IPTV provider (Live TV spec 2026-10-07,
// scenario 19): an Xtream-style m3u_plus playlist of three channels in group
// E2E, an XMLTV guide for them over today and the next 7 days, endless MPEG-TS
// streams of null packets, and /stats, the open upstream connections per
// channel, which is how the scenario sees that two viewers share one.
package iptvstub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// channels is the stub's lineup, by stream number.
var channels = []string{"1", "2", "3"}

type server struct {
	base string
	mu   sync.Mutex
	open map[string]int
}

// NewHandler serves the stub, building stream URLs on base.
func NewHandler(base string) http.Handler {
	s := &server{base: strings.TrimRight(base, "/"), open: map[string]int{}}
	mux := http.NewServeMux()
	// An Xtream Codes panel for the account e2e/secret.
	mux.HandleFunc("GET /get.php", s.playlist)
	mux.HandleFunc("GET /xmltv.php", s.guide)
	mux.HandleFunc("GET /player_api.php", s.account)
	mux.HandleFunc("GET /live/e2e/secret/{file}", s.stream)
	mux.HandleFunc("GET /stats", s.stats)
	return mux
}

func (s *server) playlist(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U url-tvg=\"%s/xmltv.php\"\n", s.base)
	for _, n := range channels {
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"e2e.%s\" tvg-name=\"E2E %s\" group-title=\"E2E\",E2E %s\n%s/live/e2e/secret/%s.ts\n", n, n, n, s.base, n)
	}
	_, _ = w.Write([]byte(b.String()))
}

// account is player_api.php's report: one connection, no expiry.
func (s *server) account(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"user_info":{"username":"e2e","password":"secret","auth":1,"status":"Active","exp_date":null,"active_cons":"0","max_connections":"1"}}`))
}

func (s *server) guide(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	day := time.Now().UTC().Truncate(24 * time.Hour)
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<tv generator-info-name=\"iptv-stub\">\n")
	for _, n := range channels {
		fmt.Fprintf(&b, "  <channel id=\"e2e.%s\"><display-name>E2E %s</display-name></channel>\n", n, n)
	}
	const layout = "20060102150405 -0700"
	for _, n := range channels {
		for h := 0; h < 8*24; h++ {
			start := day.Add(time.Duration(h) * time.Hour)
			fmt.Fprintf(&b, "  <programme start=\"%s\" stop=\"%s\" channel=\"e2e.%s\"><title>E2E %s hour %d</title></programme>\n",
				start.Format(layout), start.Add(time.Hour).Format(layout), n, n, h)
		}
	}
	b.WriteString("</tv>\n")
	_, _ = w.Write([]byte(b.String()))
}

// nullPackets is seven MPEG-TS null packets (PID 0x1FFF), one UDP-sized write.
var nullPackets = func() []byte {
	p := make([]byte, 7*188)
	for i := 0; i < len(p); i += 188 {
		p[i], p[i+1], p[i+2], p[i+3] = 0x47, 0x1F, 0xFF, 0x10
		for j := i + 4; j < i+188; j++ {
			p[j] = 0xFF
		}
	}
	return p
}()

// stream writes null packets, about 1.3 MB/s, until the viewer goes.
func (s *server) stream(w http.ResponseWriter, r *http.Request) {
	n, ok := strings.CutSuffix(r.PathValue("file"), ".ts")
	if !ok || !known(n) {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.open[n]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.open[n]--
		s.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "video/mp2t")
	flush, _ := w.(http.Flusher)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := w.Write(nullPackets); err != nil {
			return
		}
		if flush != nil {
			flush.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

func (s *server) stats(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	open := map[string]int{}
	for n, c := range s.open {
		if c > 0 {
			open[n] = c
		}
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"connections": open})
}

func known(n string) bool {
	for _, c := range channels {
		if c == n {
			return true
		}
	}
	return false
}
