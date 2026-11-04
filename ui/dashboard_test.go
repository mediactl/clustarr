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

package ui_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/prom"
)

func TestTheIndexIsTheDashboard(t *testing.T) {
	rec := get(t, ui.NewServer(t.Context(), ui.Options{}), "/")

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/dashboard", rec.Header().Get("Location"))
}

func dashboardFixture() projection.Dashboard {
	row := func(name string, phase catalogv1.TranscodePhase) projection.TranscodeRow {
		return projection.TranscodeRow{
			File: types.NamespacedName{Namespace: "media", Name: name}, Name: name + ".mkv",
			ItemURL: "/library/media/episode/" + name, Phase: phase, Message: "why " + name, Percent: -1,
		}
	}
	grab := func(id string, phase commonv1.DownloadPhase) projection.DownloadRow {
		return projection.DownloadRow{
			Owner: schema.ItemRef{Namespace: "media", Name: "monster"}, OwnerKind: commonv1.MediaKindSeries,
			Entry: catalogv1.DownloadEntry{ID: id, UID: "uid-" + id, Phase: phase, Release: catalogv1.DownloadRelease{Title: "Monster.S01E" + id}},
		}
	}
	running := row("tenma", catalogv1.TranscodePhaseRunning)
	running.Percent = 42
	return projection.Dashboard{
		Transcodes: projection.TranscodeSummary{
			Running: projection.TranscodeList{Rows: []projection.TranscodeRow{running}, Total: 1},
			Pending: projection.TranscodeList{Rows: []projection.TranscodeRow{row("nina", catalogv1.TranscodePhasePlanned)}, Total: 30},
			Failed:  projection.TranscodeList{Rows: []projection.TranscodeRow{row("johan", catalogv1.TranscodePhaseFailed)}, Total: 1},
			Skipped: projection.TranscodeList{Rows: []projection.TranscodeRow{row("grimmer", catalogv1.TranscodePhaseSkipped)}, Total: 2},
		},
		Downloads: projection.DownloadSummary{
			Active:  projection.DownloadList{Rows: []projection.DownloadRow{grab("02", commonv1.DownloadPhaseDownloading)}, Total: 1},
			Pending: projection.DownloadList{Rows: []projection.DownloadRow{grab("03", commonv1.DownloadPhaseQueued)}, Total: 1},
			Failed:  projection.DownloadList{Rows: []projection.DownloadRow{grab("04", commonv1.DownloadPhaseFailed)}, Total: 1},
		},
	}
}

func TestTheDashboardShowsWhatIsRunningPendingFailedAndSkipped(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Dashboard: func(context.Context) projection.Dashboard { return dashboardFixture() },
		Metrics:   prom.New(""),
		TransferProgress: func(_ context.Context, uid string) (schema.DownloadProgress, bool, error) {
			return schema.DownloadProgress{DownRateBytesPerSec: 2 << 20, ETASeconds: 90}, true, nil
		},
	})

	rec := get(t, srv, "/dashboard")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{
		`data-transcode="tenma" data-phase="Running"`,
		`data-progress="42"`,
		`data-transcode="nina" data-phase="Planned"`,
		`data-transcode="johan" data-phase="Failed"`,
		`data-transcode="grimmer" data-phase="Skipped"`,
		`data-download="02" data-phase="Downloading"`,
		`data-download="03" data-phase="Queued"`,
		`data-download="04" data-phase="Failed"`,
		"2.00 MiB/s",
		"why johan",
	} {
		assert.Contains(t, body, want, want)
	}
	// A section per subject (owner, 2026-11-04), in the order work flows,
	// each with one card after shadcn's interactive chart: the title, then
	// a tab per state carrying its count, and each tab's rows in a panel;
	// Plex's and Live TV's are charts alone, which need --metrics-url.
	downloadsAt := strings.Index(body, `data-dashboard-section="downloads"`)
	transcodesAt := strings.Index(body, `data-dashboard-section="transcodes"`)
	require.True(t, downloadsAt >= 0 && transcodesAt > downloadsAt, "Downloads, then Transcodes")
	assert.NotContains(t, body, `data-dashboard-section="plex"`)
	downloads, transcodes := body[downloadsAt:transcodesAt], body[transcodesAt:]
	for section, tabs := range map[string]map[string]string{
		"downloads":  {"Running": "1", "Queued": "1", "Failed": "1"},
		"transcodes": {"Running": "1", "Pending": "30", "Failed": "1", "Skipped": "2"},
	} {
		html := map[string]string{"downloads": downloads, "transcodes": transcodes}[section]
		assert.Contains(t, html, `data-section-card="`+section+`"`)
		for label, n := range tabs {
			assert.Regexp(t, `role="tab"[^>]*data-count="`+n+`"[^>]*data-stat="`+label+`"`, html, section+": "+label)
		}
	}
	for _, want := range []string{`data-list="downloads-running"`, `data-list="downloads-queued"`, `data-list="downloads-failed"`, `data-download="03"`, `data-download="04"`} {
		assert.Contains(t, downloads, want, "downloads: "+want)
	}
	for _, want := range []string{`data-list="transcodes-running"`, `data-list="transcodes-pending"`, `data-list="transcodes-failed"`, `data-list="transcodes-skipped"`, `data-transcode="nina"`} {
		assert.Contains(t, transcodes, want, "transcodes: "+want)
	}
	assert.NotContains(t, transcodes, `data-download=`, "no download in the Transcodes section")
	// A card opens on its first tab with rows; every tab pages its rows,
	// four to a page, with a pager under them.
	assert.Regexp(t, `aria-selected="true"[^>]*data-templ-value="running"`, downloads)
	assert.Regexp(t, `data-list="transcodes-pending"[\s\S]*?data-dashboard-page-list="transcodes-pending" data-dashboard-page="1"[\s\S]*?data-pager="transcodes-pending"`, body)
	pending := body[strings.Index(body, `data-list="transcodes-pending"`):strings.Index(body, `data-list="transcodes-failed"`)]
	assert.Equal(t, 1, strings.Count(pending, `data-transcode="`), "the fixture's one pending row, of a total of 30, on the first page")
	// A row's percent sits at the right of the line under its bar.
	assert.Regexp(t, `>why tenma</span>\s*<span[^>]*data-percent>42%</span>`, body)
	assert.Regexp(t, `>2.00 MiB/s · ETA 1m30s</span>\s*<span[^>]*data-percent>0%</span>`, body)
	assert.Contains(t, body, `hx-get="/dashboard/summary"`, "the summary refreshes itself")
	assert.Contains(t, body, `<script src="/static/dashboard.js" defer>`, "the script that keeps a card's tab across refreshes")
	assert.Contains(t, body, "Metrics are not configured", "no --metrics-url")
	assert.Regexp(t, `<a href="/dashboard" [^>]*data-active[ >]`, body, "the sidebar marks the Dashboard")
}

func TestTheSummaryRefreshesAlone(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return dashboardFixture() }})

	rec := get(t, srv, "/dashboard/summary")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(strings.TrimSpace(body), `<div id="dashboard-live-downloads"`), "a fragment, not a page")
	for _, group := range []string{"downloads", "transcodes"} {
		assert.Regexp(t, `<div id="dashboard-live-`+group+`" class="contents" hx-swap-oob="true">`, body, "each section's live cards, out of band")
	}
	assert.Contains(t, body, `data-transcode="tenma"`)
	assert.NotContains(t, body, "<html")
}

// The summary poller says which tab each card shows (ui/static/dashboard.js),
// and the refresh keeps it open; a tab the card has not is the default.
func TestTheSummaryKeepsTheOpenTab(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return dashboardFixture() }})

	body := get(t, srv, "/dashboard/summary?tab-downloads=failed&tab-transcodes=skipped").Body.String()
	split := strings.Index(body, `id="dashboard-live-transcodes"`)
	require.Positive(t, split)
	assert.Regexp(t, `aria-selected="true"[^>]*data-templ-value="failed"`, body[:split])
	assert.Regexp(t, `aria-selected="true"[^>]*data-templ-value="skipped"`, body[split:])

	body = get(t, srv, "/dashboard/summary?tab-downloads=nope").Body.String()
	assert.Regexp(t, `aria-selected="true"[^>]*data-templ-value="running"`, body, "an unknown tab is the default")
}

// A running list is a row a line, four to a page, with the pages beneath
// (owner, 2026-11-04); the summary poller asks for the page a list shows,
// and a page past the last is the last.
func TestTheRunningListsPage(t *testing.T) {
	d := dashboardFixture()
	running := d.Transcodes.Running.Rows[0]
	d.Transcodes.Running.Rows = nil
	for i := range 7 {
		r := running
		r.File.Name = fmt.Sprintf("run%d", i)
		d.Transcodes.Running.Rows = append(d.Transcodes.Running.Rows, r)
	}
	d.Transcodes.Running.Total = 7
	srv := ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return d }})
	list := func(body string) string {
		at := strings.Index(body, `data-list="transcodes-running"`)
		require.Positive(t, at)
		end := strings.Index(body[at:], `data-list="transcodes-pending"`)
		require.Positive(t, end)
		return body[at : at+end]
	}

	first := list(get(t, srv, "/dashboard").Body.String())
	assert.Equal(t, 4, strings.Count(first, `data-transcode="run`), "four to a page")
	assert.Contains(t, first, `class="grid gap-3" data-dashboard-page-list="transcodes-running" data-dashboard-page="1"`, "a row a line")
	assert.Contains(t, first, `data-pager="transcodes-running"`)
	requireTag(t, first, `data-dashboard-goto="2"`, `data-dashboard-list="transcodes-running"`, `href="/dashboard?page-transcodes-running=2"`)
	assert.NotContains(t, first, `data-more`, "the pages say how many, not a See all")

	second := list(get(t, srv, "/dashboard/summary?page-transcodes-running=2").Body.String())
	assert.Equal(t, 3, strings.Count(second, `data-transcode="run`))
	assert.Contains(t, second, `data-transcode="run5"`)
	assert.Contains(t, second, `data-dashboard-page="2"`)

	past := list(get(t, srv, "/dashboard/summary?page-transcodes-running=9").Body.String())
	assert.Contains(t, past, `data-dashboard-page="2"`, "a page past the last is the last")

	one := list(get(t, ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return dashboardFixture() }}), "/dashboard").Body.String())
	assert.Contains(t, one, `data-pager="transcodes-running"`, "a pager under every list, one page or many")
	assert.Regexp(t, `<button[^>]*disabled[^>]*>[^<]*(<svg[\s\S]*?</svg>)?\s*<span[^>]*>Next</span>|data-dashboard-goto="2"[^>]*disabled`, one, "one page offers no second")
}

// fakeQuerier answers each query with the series registered for it.
type fakeQuerier struct {
	mu      sync.Mutex
	answers map[string][]prom.Series
	errs    map[string]error
	asked   []string
}

func (f *fakeQuerier) Configured() bool { return true }

func (f *fakeQuerier) QueryRange(_ context.Context, q string, _, _ time.Time, _ time.Duration) ([]prom.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, q)
	if err := f.errs[q]; err != nil {
		return nil, err
	}
	return f.answers[q], nil
}

func TestTheChartsQueryEveryPanelOverTheRange(t *testing.T) {
	at := time.Now().Truncate(time.Hour)
	f := &fakeQuerier{
		answers: map[string][]prom.Series{
			`sum by (tier) (max_over_time(clustarr_transcode_jobs_active[5m]))`: {
				{Labels: map[string]string{"tier": "intel"}, Points: []prom.Point{{T: at, V: 2}}},
				{Labels: map[string]string{"tier": "nvidia"}, Points: []prom.Point{{T: at, V: 1}}},
			},
		},
		errs: map[string]error{`sum(clustarr_transcode_backlog_files)`: errors.New("prom: 502 Bad Gateway")},
	}
	srv := ui.NewServer(t.Context(), ui.Options{Metrics: f})

	rec := get(t, srv, "/dashboard/charts?range=6h")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-range="6h"`)
	assert.Contains(t, body, `hx-get="/dashboard/charts?range=6h"`, "the charts refresh over the range picked")
	// Downloads' and Transcodes' charts are a card each behind tabs (owner,
	// 2026-11-04), a tab per chart or series carrying its figure over the
	// range: the workers' peak is 2 intel and 1 nvidia at once, and a chart
	// that failed or has nothing shows a dash.
	for label, figure := range map[string]string{"Finished": "–", "Peak workers": "3", "Backlog": "–", "Grabbed": "–", "Imported": "–"} {
		assert.Regexp(t, `role="tab"[^>]*data-count="`+figure+`"[^>]*data-stat="`+label+`"`, body, label)
	}
	// Each chart stands in its subject's section, swapped out of band:
	// Plex's and Live TV's as cards of their own.
	groups := []string{"downloads", "transcodes", "plex", "livetv"}
	panels := map[string][]string{"plex": {"plex-sessions", "plex-bandwidth", "plex-plays"}, "livetv": {"livetv"}}
	for i, group := range groups {
		open := `<div id="dashboard-charts-` + group + `" class="contents" hx-swap-oob="true">`
		at := strings.Index(body, open)
		require.GreaterOrEqual(t, at, 0, group)
		end := len(body)
		if i+1 < len(groups) {
			end = strings.Index(body, `id="dashboard-charts-`+groups[i+1]+`"`)
		}
		got := regexp.MustCompile(`data-panel="([^"]+)"`).FindAllStringSubmatch(body[at:end], -1)
		var ids []string
		for _, m := range got {
			ids = append(ids, m[1])
		}
		assert.Equal(t, panels[group], ids, group)
		if panels[group] == nil {
			assert.Contains(t, body[at:end], `data-section-card="`+group+`-charts"`, group)
		}
	}
	// A chart card keeps the tab its request says it shows.
	body = get(t, srv, "/dashboard/charts?range=6h&tab-transcodes-charts=backlog").Body.String()
	cardAt := strings.Index(body, `data-section-card="transcodes-charts"`)
	require.Positive(t, cardAt)
	assert.Regexp(t, `aria-selected="true"[^>]*data-templ-value="backlog"`, body[cardAt:])
	assert.Contains(t, body, `data-chart="chart-dash-transcode-workers"`)
	assert.Contains(t, body, "--color-s-intel", "one series per tier, keyed for its color")
	assert.Contains(t, body, "prom: 502 Bad Gateway", "a failing panel says why")
	assert.Contains(t, body, "No data in this range.")
	assert.Contains(t, f.asked, `sum by (outcome) (increase(clustarr_transcode_duration_seconds_count[5m]))`, "$step is the range's step")
}

func TestAnUnknownRangeIsTheDefault(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Metrics: &fakeQuerier{}})

	rec := get(t, srv, "/dashboard/charts?range=1y")

	assert.Contains(t, rec.Body.String(), `data-range="24h"`)
}
