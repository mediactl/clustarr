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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/livetv/relay"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// SpareTuners is how many tuners discover.json advertises beyond the
// provider's own: the relay is the limit that counts. It shares one upstream
// among a channel's viewers, gives a lingering stream's tuner to a new
// channel, and answers 503 at once when every connection streams. PMS's own
// count only ever refused a tune the relay would have served. On talos on
// 2026-10-20, a grab PMS never ended held the one tuner from about 15:00
// UTC while the relay streamed nothing. Every later tune waited 300 s and
// failed ("Tuner in use by another process", then "The recording conflicts
// with other recordings"). The spares tolerate that many such grabs until
// cluster-plex ends them. A recording that clashes is refused with a 503
// when it starts, not by PMS's conflict check when it is scheduled.
const SpareTuners = 3

// AdvertisedTuners is discover.json's TunerCount for a provider of tuners.
func AdvertisedTuners(tuners int) int { return tuners + SpareTuners }

// Handler serves every provider's HDHomeRun surfaces (spec §5.0, §5.3,
// amended by 2026-10-20-iptv-xtream-ranges-design.md):
//
//   - /livetv/<ns>/<name>/devices/<deviceID>/<file>: one block's device,
//     the endpoint its port's proxy in each Plex pod sends every request
//     below;
//   - /livetv/<ns>/<name>/<file>?from=<n>&to=<n>: any range of the
//     provider's lineup, either end optional, for any client;
//
// with <file> discover.json, lineup_status.json, lineup.json, lineup.post,
// device.xml, xmltv.xml, playlist.m3u or stream/<number>. Every URL handed
// out is built from the request -- X-Forwarded-Host and X-Forwarded-Prefix
// when a proxy names them, else its Host and path -- so Plex's later
// requests come back through the proxy port it first came by. lineup.json
// and xmltv.xml carry an ETag of their body.
func (t *Tuner) Handler() http.Handler { return http.HandlerFunc(t.serveHTTP) }

// channelRange is a request's from and to.
type channelRange struct{ from, to string }

func rangeOf(q url.Values) channelRange {
	return channelRange{from: strings.TrimSpace(q.Get("from")), to: strings.TrimSpace(q.Get("to"))}
}

// holds reports whether number is in the range.
func (r channelRange) holds(number string) bool {
	return (r.from == "" || xmltv.CompareNumbers(r.from, number) <= 0) &&
		(r.to == "" || xmltv.CompareNumbers(number, r.to) <= 0)
}

func (r channelRange) key() string { return r.from + "|" + r.to }

// query is the range as the query a handed-out URL carries; "" for none.
func (r channelRange) query() string {
	v := url.Values{}
	if r.from != "" {
		v.Set("from", r.from)
	}
	if r.to != "" {
		v.Set("to", r.to)
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// baseURL is where the client reached prefix: the proxy's X-Forwarded-Host
// and X-Forwarded-Prefix when it sends them, else the request's own Host and
// prefix.
func baseURL(r *http.Request, prefix string) string {
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	scheme := "http"
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	if pre, ok := r.Header["X-Forwarded-Prefix"]; ok && len(pre) > 0 {
		prefix = strings.TrimRight(pre[0], "/")
	}
	return scheme + "://" + host + prefix
}

func (t *Tuner) serveHTTP(w http.ResponseWriter, r *http.Request) {
	after, ok := strings.CutPrefix(r.URL.Path, "/livetv/")
	parts := strings.SplitN(after, "/", 3)
	if !ok || len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	key, rest := types.NamespacedName{Namespace: parts[0], Name: parts[1]}, parts[2]
	prefix := "/livetv/" + parts[0] + "/" + parts[1]
	p := t.lookup(key)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if dev, file, isDevice := strings.Cut(strings.TrimPrefix(rest, "devices/"), "/"); strings.HasPrefix(rest, "devices/") && isDevice {
		t.serveDevice(w, r, p, key, dev, file, prefix+"/devices/"+dev)
		return
	}
	t.serveRange(w, r, p, key, rest, prefix)
}

// serveDevice serves one block's device.
func (t *Tuner) serveDevice(w http.ResponseWriter, r *http.Request, p *provider, key types.NamespacedName, deviceID, file, prefix string) {
	base := baseURL(r, prefix)
	p.mu.Lock()
	sf := p.blocks[deviceID]
	cfg, fetched, tuners := p.cfg, p.playlistFetched, effectiveTuners(p.cfg, p.account)
	var (
		entries []hdhr.LineupEntry
		guide   []byte
		block   BlockConfig
	)
	if sf != nil {
		entries, guide, block = sf.entries, sf.guideXML, sf.block
	}
	p.mu.Unlock()
	if sf == nil {
		http.NotFound(w, r)
		return
	}
	device := hdhr.Device{FriendlyName: block.FriendlyName, DeviceID: block.DeviceID, BaseURL: base, TunerCount: AdvertisedTuners(tuners)}
	switch {
	case file == "lineup.json":
		if !fetched {
			http.Error(w, "the playlist has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		writeTagged(w, r, "application/json", hdhr.Lineup(absolute(entries, base)))
	case file == "playlist.m3u":
		if !fetched {
			http.Error(w, "the playlist has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		writeBody(w, "audio/x-mpegurl", withBase(playlistM3U(entries, t.logos(p), base), base))
	case file == "xmltv.xml":
		if cfg.EPGSource == "PMS" || guide == nil {
			http.NotFound(w, r)
			return
		}
		writeTagged(w, r, "application/xml", withBase(guide, base))
	default:
		t.serveCommon(w, r, p, device, file)
	}
}

// serveRange serves a range of the provider's lineup.
func (t *Tuner) serveRange(w http.ResponseWriter, r *http.Request, p *provider, key types.NamespacedName, file, prefix string) {
	rg := rangeOf(r.URL.Query())
	base := baseURL(r, prefix)
	p.mu.Lock()
	cfg, fetched, tuners := p.cfg, p.playlistFetched, effectiveTuners(p.cfg, p.account)
	var entries []hdhr.LineupEntry
	if file == "lineup.json" || file == "playlist.m3u" {
		for _, e := range p.lineup {
			if rg.holds(e.GuideNumber) {
				entries = append(entries, e)
			}
		}
	}
	p.mu.Unlock()

	friendly := "Clustarr " + key.Name
	if rg.from != "" || rg.to != "" {
		friendly += " " + rg.from + "-" + rg.to
	}
	device := hdhr.Device{
		FriendlyName: friendly, DeviceID: hdhr.BlockDeviceID(key.Namespace, key.Name, rg.from),
		BaseURL: base, LineupURL: base + "/lineup.json" + rg.query(), TunerCount: AdvertisedTuners(tuners),
	}
	switch {
	case file == "lineup.json":
		if !fetched {
			http.Error(w, "the playlist has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		writeTagged(w, r, "application/json", hdhr.Lineup(absolute(entries, base)))
	case file == "playlist.m3u":
		if !fetched {
			http.Error(w, "the playlist has not been fetched yet", http.StatusServiceUnavailable)
			return
		}
		writeBody(w, "audio/x-mpegurl", withBase(playlistM3U(entries, t.logos(p), base), base))
	case file == "xmltv.xml":
		if cfg.EPGSource == "PMS" || !fetched {
			http.NotFound(w, r)
			return
		}
		writeTagged(w, r, "application/xml", withBase(t.rangeGuide(p, rg), base))
	default:
		t.serveCommon(w, r, p, device, file)
	}
}

// serveCommon answers what a device and a range serve alike.
func (t *Tuner) serveCommon(w http.ResponseWriter, r *http.Request, p *provider, device hdhr.Device, file string) {
	switch number, isStream := strings.CutPrefix(file, "stream/"); {
	case file == "discover.json":
		writeBody(w, "application/json", hdhr.Discover(device))
	case file == "lineup_status.json":
		writeBody(w, "application/json", hdhr.LineupStatus())
	case file == "lineup.post":
		w.WriteHeader(http.StatusOK)
	case file == "device.xml":
		writeBody(w, "application/xml", hdhr.DeviceXML(device))
	case strings.HasPrefix(file, "logos/") && strings.HasSuffix(file, ".png"):
		t.serveLogo(w, r, p, strings.TrimSuffix(strings.TrimPrefix(file, "logos/"), ".png"))
	case isStream:
		// A stream is the provider's, under any device or range.
		p.mu.Lock()
		upstream, ok := p.streams[number]
		lang := p.languages[number]
		rl := p.relay
		p.mu.Unlock()
		if !ok || rl == nil {
			http.NotFound(w, r)
			return
		}
		// A channel's own language is forced over the stream's; none
		// leaves the provider's default to fill a missing one.
		rl.ServeWith(w, r, number, upstream, relay.Language{Code: lang, Force: lang != ""})
	default:
		http.NotFound(w, r)
	}
}

// absolute is entries with base in front of each relative URL.
func absolute(entries []hdhr.LineupEntry, base string) []hdhr.LineupEntry {
	out := make([]hdhr.LineupEntry, len(entries))
	for i, e := range entries {
		e.URL = base + "/" + e.URL
		out[i] = e
	}
	return out
}

// rangeGuide is the XEPG guide of the lineup's channels in rg, built once
// per range and render.
func (t *Tuner) rangeGuide(p *provider, rg channelRange) []byte {
	p.mu.Lock()
	if b, ok := p.ranges[rg.key()]; ok {
		p.mu.Unlock()
		return b
	}
	gen := p.renderGen
	var chans []xmltv.LineupChannel
	for _, lc := range p.lineupChans {
		if rg.holds(lc.Number) {
			chans = append(chans, lc)
		}
	}
	loaded, from := p.loaded, p.guideFrom
	enricher := t.enricher(p)
	p.mu.Unlock()

	var b bytes.Buffer
	_ = buildGuide(&b, chans, loaded, from, guideDays, enricher)
	p.mu.Lock()
	if p.renderGen == gen {
		if p.ranges == nil {
			p.ranges = map[string][]byte{}
		}
		p.ranges[rg.key()] = b.Bytes()
	}
	p.mu.Unlock()
	return b.Bytes()
}

// logos is each lineup channel's logo by number.
func (t *Tuner) logos(p *provider) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string, len(p.lineupChans))
	for _, lc := range p.lineupChans {
		out[lc.Number] = lc.Logo
	}
	return out
}

// playlistM3U is the lineup as an M3U playlist: each channel's tvg-id is
// its number, the channel id the XEPG guide uses, and its URL the relay's.
func playlistM3U(lineup []hdhr.LineupEntry, logos map[string]string, base string) []byte {
	attr := func(s string) string { return strings.ReplaceAll(s, `"`, "'") }
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n")
	for _, e := range lineup {
		b.WriteString(`#EXTINF:-1 tvg-id="` + attr(e.GuideNumber) + `" tvg-chno="` + attr(e.GuideNumber) +
			`" tvg-name="` + attr(e.GuideName) + `" tvg-logo="` + attr(logos[e.GuideNumber]) + `",` +
			strings.ReplaceAll(e.GuideName, "\n", " ") + "\n")
		b.WriteString(base + "/" + e.URL + "\n")
	}
	return b.Bytes()
}

func writeBody(w http.ResponseWriter, contentType string, b []byte) {
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

// writeTagged writes b with an ETag of it, or 304 when the request already
// holds it.
func writeTagged(w http.ResponseWriter, r *http.Request, contentType string, b []byte) {
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", tag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeBody(w, contentType, b)
}
