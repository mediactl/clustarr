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
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/components/chart"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/prom"
	"github.com/mediactl/clustarr/ui/views"
)

// dashRange is one range the dashboard charts: how far back, at what step,
// and how a sample time is labelled.
type dashRange struct {
	Name   string
	Span   time.Duration
	Step   time.Duration
	Layout string
}

var dashRanges = []dashRange{
	{"6h", 6 * time.Hour, 5 * time.Minute, "15:04"},
	{"24h", 24 * time.Hour, 15 * time.Minute, "15:04"},
	{"7d", 7 * 24 * time.Hour, 2 * time.Hour, "Mon 15h"},
}

const defaultDashRange = "24h"

// dashRangeNamed is the range r names, or the default.
func dashRangeNamed(name string) dashRange {
	for _, r := range dashRanges {
		if r.Name == name {
			return r
		}
	}
	return dashRangeNamed(defaultDashRange)
}

// panelSeries is one query of a panel: a fixed series (Key and Label), or
// one series per value of the By label. $step in Query is the range's step.
type panelSeries struct {
	Key   string
	Label string
	Query string
	By    string
	Scale float64
}

// panelDef is one chart of the dashboard.
type panelDef struct {
	ID          string
	Title       string
	Description string
	Kind        string
	Stacked     bool
	Series      []panelSeries
}

// dashPanels are the dashboard's charts, over cluster-plex's Plex metrics
// (clusterplex_plex_*) and clustarr's own (clustarr_*). A gauge is charted
// as each step's peak (max_over_time): sampled once a step, the 7-day view's
// two-hour points missed every stream shorter than that.
var dashPanels = []panelDef{
	{ID: "plex-sessions", Title: "Plex sessions", Description: "Peak playback sessions, and those Plex transcodes", Kind: "area", Series: []panelSeries{
		{Key: "sessions", Label: "Sessions", Query: `sum(max_over_time(clusterplex_plex_sessions[$step]))`},
		{Key: "transcodes", Label: "Transcodes", Query: `sum(max_over_time(clusterplex_plex_transcode_sessions[$step]))`},
	}},
	{ID: "plex-bandwidth", Title: "Plex bandwidth", Description: "Peak streaming to clients, Mb/s", Kind: "area", Series: []panelSeries{
		{Key: "bandwidth", Label: "Mb/s", Query: `sum(max_over_time(clusterplex_plex_stream_bandwidth_bytes_per_second[$step]))`, Scale: 8 / 1e6},
	}},
	{ID: "plex-plays", Title: "Plex plays", Description: "Plays started, by media type", Kind: "bar", Stacked: true, Series: []panelSeries{
		{Query: `sum by (media_type) (increase(clusterplex_plex_plays_total[$step]))`, By: "media_type"},
	}},
	{ID: "livetv", Title: "Live TV", Description: "Peak viewers, and the provider's tuners in use", Kind: "line", Series: []panelSeries{
		{Key: "viewers", Label: "Viewers", Query: `sum(max_over_time(clustarr_livetv_viewers[$step]))`},
		{Key: "tuners", Label: "Tuners", Query: `sum(max_over_time(clustarr_livetv_tuners_in_use[$step]))`},
	}},
	{ID: "transcodes", Title: "Transcodes finished", Description: "By outcome", Kind: "bar", Stacked: true, Series: []panelSeries{
		{Query: `sum by (outcome) (increase(clustarr_transcode_duration_seconds_count[$step]))`, By: "outcome"},
	}},
	{ID: "transcode-workers", Title: "Transcode workers", Description: "Peak jobs running, by hardware tier", Kind: "area", Stacked: true, Series: []panelSeries{
		{Query: `sum by (tier) (max_over_time(clustarr_transcode_jobs_active[$step]))`, By: "tier"},
	}},
	{ID: "transcode-backlog", Title: "Transcode backlog", Description: "Files waiting outside the window", Kind: "line", Series: []panelSeries{
		{Key: "backlog", Label: "Files", Query: `sum(clustarr_transcode_backlog_files)`},
	}},
	{ID: "grabs", Title: "Grabs and imports", Description: "Releases grabbed and files imported", Kind: "bar", Series: []panelSeries{
		{Key: "grabbed", Label: "Grabbed", Query: `sum(increase(clustarr_search_decisions_total{decision="grabbed"}[$step]))`},
		{Key: "imported", Label: "Imported", Query: `sum(increase(clustarr_import_files_total{outcome="imported"}[$step]))`},
	}},
}

// maxPanelSeries caps a By panel's series, keeping the largest.
const maxPanelSeries = 6

// panelColors are the series colors: the theme's accent first, then hues
// far apart -- the theme's own chart palette is shades of one orange, so two
// series drawn from it could not be told apart.
var panelColors = []string{
	"var(--chart-1)", "oklch(0.68 0.14 245)", "oklch(0.72 0.15 150)", "oklch(0.65 0.2 25)",
	"oklch(0.7 0.14 300)", "var(--chart-4)", "var(--chart-2)", "var(--chart-3)",
}

// chartsTimeout bounds one render's queries.
const chartsTimeout = 15 * time.Second

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	v := views.DashboardView{
		Summary: s.dashboardSummary(r.Context()),
		Charts:  s.dashboardCharts(r.Context(), dashRangeNamed(r.URL.Query().Get("range"))),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Dashboard(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render dashboard", "error", err)
	}
}

func (s *Server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.DashboardSummary(s.dashboardSummary(r.Context())).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render dashboard summary", "error", err)
	}
}

func (s *Server) handleDashboardCharts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	v := s.dashboardCharts(r.Context(), dashRangeNamed(r.URL.Query().Get("range")))
	if err := views.DashboardCharts(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render dashboard charts", "error", err)
	}
}

// dashboardSummary is the projection's dashboard, with the active
// downloads' transfer telemetry.
func (s *Server) dashboardSummary(ctx context.Context) views.DashboardSummaryView {
	var d projection.Dashboard
	if s.opts.Dashboard != nil {
		d = s.opts.Dashboard(ctx)
	}
	return views.DashboardSummaryView{Summary: d, Active: s.downloadViews(ctx, d.Downloads.Active.Rows)}
}

// dashboardCharts queries every panel over rg, all at once.
func (s *Server) dashboardCharts(ctx context.Context, rg dashRange) views.DashboardChartsView {
	v := views.DashboardChartsView{Range: rg.Name}
	for _, r := range dashRanges {
		v.Ranges = append(v.Ranges, r.Name)
	}
	q := s.opts.Metrics
	if q == nil || !q.Configured() {
		return v
	}
	v.Configured = true
	ctx, cancel := context.WithTimeout(ctx, chartsTimeout)
	defer cancel()
	end := time.Now().Truncate(rg.Step)
	start := end.Add(-rg.Span)
	v.Panels = make([]views.ChartPanel, len(dashPanels))
	var wg sync.WaitGroup
	for i, p := range dashPanels {
		wg.Go(func() { v.Panels[i] = buildPanel(ctx, q, p, rg, start, end) })
	}
	wg.Wait()
	return v
}

// buildPanel runs p's queries and lays their series out as one datum per
// step of the range, so the time axis is even; a missing sample reads 0
// (these metrics are absent while nothing happens).
func buildPanel(ctx context.Context, q prom.Querier, p panelDef, rg dashRange, start, end time.Time) views.ChartPanel {
	out := views.ChartPanel{ID: p.ID, Title: p.Title, Description: p.Description, Kind: p.Kind, Stacked: p.Stacked}
	type line struct {
		key, label string
		points     []prom.Point
		total      float64
	}
	var lines []line
	for _, ps := range p.Series {
		series, err := q.QueryRange(ctx, strings.ReplaceAll(ps.Query, "$step", promDuration(rg.Step)), start, end, rg.Step)
		if err != nil {
			out.Err = panelError(err)
			return out
		}
		var found []line
		for _, s := range series {
			// A copy: the client's cache hands every render the same
			// points, and scaling them in place rescaled them each time.
			l := line{key: ps.Key, label: ps.Label, points: append([]prom.Point(nil), s.Points...)}
			if ps.By != "" {
				value := s.Labels[ps.By]
				if value == "" {
					value = "other"
				}
				l.key, l.label = seriesKey(value), seriesLabel(value)
			}
			scale := ps.Scale
			if scale == 0 {
				scale = 1
			}
			for i := range l.points {
				l.points[i].V *= scale
				l.total += l.points[i].V
			}
			found = append(found, l)
		}
		if ps.By != "" {
			sort.SliceStable(found, func(i, j int) bool { return found[i].total > found[j].total })
			found = found[:min(len(found), maxPanelSeries)]
			sort.SliceStable(found, func(i, j int) bool { return found[i].label < found[j].label })
		}
		lines = append(lines, found...)
	}
	if len(lines) == 0 {
		return out
	}
	times := map[int64]chart.Datum{}
	for t := start; !t.After(end); t = t.Add(rg.Step) {
		d := chart.Datum{"t": t.Local().Format(rg.Layout)}
		times[t.Unix()] = d
		out.Data = append(out.Data, d)
	}
	for i, l := range lines {
		out.Keys = append(out.Keys, l.key)
		out.Config = append(out.Config, chart.Series{Key: l.key, Label: l.label, Color: panelColors[i%len(panelColors)]})
		for _, d := range out.Data {
			d[l.key] = 0.0
		}
		for _, pt := range l.points {
			if d := times[pt.T.Unix()]; d != nil {
				d[l.key] = math.Round(pt.V*100) / 100
			}
		}
	}
	return out
}

// panelError is the line a failed panel shows.
func panelError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "The metrics store did not answer in time."
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// promDuration is d in PromQL's duration syntax.
func promDuration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
}

// seriesKey is a label value as a series key, which names a CSS custom
// property (--color-<key>): lower case letters, digits and dashes.
func seriesKey(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	key := strings.TrimSuffix(b.String(), "-")
	if key == "" {
		return "series"
	}
	return "s-" + key
}

// seriesLabel is a label value as a legend label.
func seriesLabel(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
