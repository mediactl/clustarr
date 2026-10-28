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

package ui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The TV Guide (the direct-tuners and ui design §5.6): Dispatcharr's grid
// from the tuner's guide index, server-rendered six hours at a time. The
// canvas spans the index's seven days from its first; window k is
// [From + k·6h, From + (k+1)·6h), and a programme belongs to the window it
// starts in, drawn whole across the window's edge.

// guide is one request's guide: the provider, its index and the rows the
// filters leave.
type guide struct {
	query    url.Values
	provider string
	key      types.NamespacedName
	view     *guideView
	from     time.Time
	rows     []views.GuideChannel
	streams  map[string]string // number -> the tuner's stream URL
	page     views.GuidePage
}

// loadGuide reads the provider the query names (else the first), its
// guide index, and the channel rows the group and q filters leave.
func (s *Server) loadGuide(ctx context.Context, q url.Values) guide {
	in := s.liveTVInputs(ctx)
	g := guide{query: url.Values{}, provider: q.Get("provider"), streams: map[string]string{}}
	g.page.Q, g.page.Group = strings.TrimSpace(q.Get("q")), q.Get("group")
	for _, p := range in.Providers {
		v := p.Namespace + "/" + p.Name
		g.page.Providers = append(g.page.Providers, views.ProviderOption{Value: v, Label: p.Name})
		if g.provider == "" {
			g.provider = v
		}
	}
	g.page.Provider = g.provider
	ns, name, _ := strings.Cut(g.provider, "/")
	g.key = types.NamespacedName{Namespace: ns, Name: name}
	for k, v := range map[string]string{"provider": g.provider, "group": g.page.Group, "q": g.page.Q} {
		if v != "" {
			g.query.Set(k, v)
		}
	}
	groups, seen := map[string]string{}, map[string]bool{}
	for _, r := range projection.LiveTVChannels(in) {
		if r.Namespace != ns || r.Provider != name || !r.Active {
			continue
		}
		groups[r.Number], g.streams[r.Number] = r.Group, r.StreamURL
		if r.Group != "" && !seen[r.Group] {
			seen[r.Group] = true
			g.page.Groups = append(g.page.Groups, r.Group)
		}
	}
	v, ok := s.liveTV.GuideIndex(ctx, g.key)
	if !ok {
		return g
	}
	g.view, g.from = v, v.Index.From
	if g.from.IsZero() {
		g.from = s.now().UTC().Truncate(24 * time.Hour)
	}
	needle := strings.ToLower(g.page.Q)
	for _, c := range v.Index.Channels {
		if g.page.Group != "" && groups[c.Number] != g.page.Group {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(c.Name), needle) && !strings.HasPrefix(c.Number, g.page.Q) {
			continue
		}
		row := views.GuideChannel{Number: c.Number, Name: c.Name}
		if c.Logo != "" {
			row.LogoPath = projection.LogoPath(g.key, c.Logo)
		}
		g.rows = append(g.rows, row)
	}
	return g
}

// windowOf is the window a time falls in, -1 before the guide.
func (g guide) windowOf(t time.Time) int {
	if t.Before(g.from) {
		return -1
	}
	return int(t.Sub(g.from) / views.GuideWindow)
}

// window is window k's layer: its hours and the programmes starting in it.
func (g guide) window(k int) views.GuideWindowView {
	start := g.from.Add(time.Duration(k) * views.GuideWindow)
	w := views.GuideWindowView{K: k, Start: start, Rows: len(g.rows)}
	for h := range int(views.GuideWindow / time.Hour) {
		w.Hours = append(w.Hours, start.Add(time.Duration(h)*time.Hour))
	}
	for i, c := range g.rows {
		for _, p := range g.view.ByChannel[c.Number] {
			at := p.Start
			if at.Before(g.from) {
				at = g.from
			}
			if g.windowOf(at) != k || !p.Stop.After(at) {
				continue
			}
			w.Blocks = append(w.Blocks, views.GuideBlock{
				IndexProgramme: p, Row: i, Start: at,
				Left:  views.GuideMinutePx * at.Sub(start).Minutes(),
				Width: max(views.GuideMinutePx*p.Stop.Sub(at).Minutes()-2, 1),
				URL: "/livetv/guide/programme?" + url.Values{"provider": {g.provider}, "channel": {c.Number},
					"start": {p.Start.UTC().Format(time.RFC3339)}}.Encode(),
			})
		}
	}
	return w
}

// sentinel is the element that loads window k when it scrolls into view,
// nil outside the guide.
func (g guide) sentinel(k, dir int) *views.GuideSentinel {
	if k < 0 || k >= views.GuideWindows {
		return nil
	}
	q := url.Values{}
	for key, v := range g.query {
		q[key] = v
	}
	q.Set("k", strconv.Itoa(k))
	q.Set("dir", strconv.Itoa(dir))
	return &views.GuideSentinel{K: k, Dir: dir, URL: "/livetv/guide/window?" + q.Encode()}
}

// handleLiveTVGuide renders the TV Guide: the window holding now - 30 min,
// the one before it and the next, with a sentinel each way. The window
// before is drawn because a programme belongs to the window it starts in:
// without it, what began before the window's start and airs now (a game
// from 19:00 at 20:45, past 00:00 UTC) was missing at the now line, and
// the sentinel that loads it sat left of the view, which opens at now.
func (s *Server) handleLiveTVGuide(w http.ResponseWriter, r *http.Request) {
	g := s.loadGuide(r.Context(), r.URL.Query())
	p := g.page
	if g.view != nil {
		p.Built, p.From, p.Rows = true, g.from, g.rows
		k := min(max(g.windowOf(s.now().Add(-30*time.Minute)), 0), views.GuideWindows-1)
		for i := max(k-1, 0); i <= min(k+1, views.GuideWindows-1); i++ {
			p.Windows = append(p.Windows, g.window(i))
		}
		p.Before, p.After = g.sentinel(k-2, -1), g.sentinel(k+2, 1)
	}
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LiveTVGuide(p).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the TV Guide", "error", err)
	}
}

// handleLiveTVGuideWindow renders window k and the sentinel past it in its
// direction: nothing for a window outside the guide.
func (s *Server) handleLiveTVGuideWindow(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	k, err := strconv.Atoi(q.Get("k"))
	if err != nil {
		http.Error(w, "k is a window number", http.StatusBadRequest)
		return
	}
	dir, _ := strconv.Atoi(q.Get("dir"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	g := s.loadGuide(r.Context(), q)
	if g.view == nil || k < 0 || k >= views.GuideWindows {
		return
	}
	var next *views.GuideSentinel
	if dir == -1 || dir == 1 {
		next = g.sentinel(k+dir, dir)
	}
	if err := views.GuideWindowPart(g.window(k), next).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render a TV Guide window", "error", err)
	}
}

// handleLiveTVProgramme renders a programme's details dialog.
func (s *Server) handleLiveTVProgramme(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	g := s.loadGuide(r.Context(), q)
	start, err := time.Parse(time.RFC3339, q.Get("start"))
	if g.view == nil || err != nil {
		http.NotFound(w, r)
		return
	}
	channel := q.Get("channel")
	for _, p := range g.view.ByChannel[channel] {
		if !p.Start.Equal(start) {
			continue
		}
		d := views.ProgrammeDialog{IndexProgramme: p, Channel: channel, StreamURL: g.streams[channel], Image: s.searchArt.URL(p.Image)}
		for _, c := range g.view.Index.Channels {
			if c.Number == channel {
				d.ChannelName = c.Name
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := views.ProgrammeDialogView(d).Render(r.Context(), w); err != nil {
			logging.FromContext(r.Context()).Error("render a programme", "error", err)
		}
		return
	}
	http.NotFound(w, r)
}
