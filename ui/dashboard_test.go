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
	"net/http"
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
		`data-list="Pending"`,
		"2 of 31", // one download and one transcode shown, 31 waiting
	} {
		assert.Contains(t, body, want, want)
	}
	assert.Contains(t, body, `hx-get="/dashboard/summary"`, "the summary refreshes itself")
	assert.Contains(t, body, "Metrics are not configured", "no --metrics-url")
	assert.Regexp(t, `<a href="/dashboard" [^>]*data-active[ >]`, body, "the sidebar marks the Dashboard")
}

func TestTheSummaryRefreshesAlone(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return dashboardFixture() }})

	rec := get(t, srv, "/dashboard/summary")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(strings.TrimSpace(body), `<div id="dashboard-summary"`), "a fragment, not a page")
	assert.Contains(t, body, `data-transcode="tenma"`)
	assert.NotContains(t, body, "<html")
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
	assert.Contains(t, body, `data-panel="transcode-workers"`)
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
