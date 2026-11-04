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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/components/chart"
	"github.com/mediactl/clustarr/ui/prom"
	"github.com/mediactl/clustarr/ui/views"
)

type oneAnswer []prom.Series

func (oneAnswer) Configured() bool { return true }
func (a oneAnswer) QueryRange(context.Context, string, time.Time, time.Time, time.Duration) ([]prom.Series, error) {
	return a, nil
}

// TestAPanelHasADatumForEveryStep: a series with gaps (a gauge absent while
// nothing plays) left out the times it had no sample, and the time axis
// jumped from 01:30 to 10:45 (talos, 2026-10-22).
func TestAPanelHasADatumForEveryStep(t *testing.T) {
	rg := dashRangeNamed("6h")
	end := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	start := end.Add(-rg.Span)
	q := oneAnswer{{Labels: map[string]string{"tier": "intel"}, Points: []prom.Point{{T: start.Add(rg.Step), V: 2}}}}
	p := panelDef{ID: "w", Kind: "area", Series: []panelSeries{{Query: "x", By: "tier"}}}

	out := buildPanel(context.Background(), q, p, rg, start, end)

	require.Len(t, out.Data, int(rg.Span/rg.Step)+1)
	assert.Equal(t, []string{"s-intel"}, out.Keys)
	assert.Equal(t, 0.0, out.Data[0]["s-intel"])
	assert.Equal(t, 2.0, out.Data[1]["s-intel"])
	assert.Equal(t, 0.0, out.Data[len(out.Data)-1]["s-intel"])
}

func TestAPanelWithNoSeriesHasNoData(t *testing.T) {
	rg := dashRangeNamed("6h")
	end := time.Now().Truncate(rg.Step)

	out := buildPanel(context.Background(), oneAnswer{}, panelDef{ID: "w", Series: []panelSeries{{Key: "k", Query: "x"}}}, rg, end.Add(-rg.Span), end)

	assert.Empty(t, out.Data, "the panel says there is no data rather than drawing a flat zero")
}

func TestSeriesKeysNameACSSProperty(t *testing.T) {
	assert.Equal(t, "s-clustarr-work-caption", seriesKey("CLUSTARR_WORK_CAPTION"))
	assert.Equal(t, "s-direct-play", seriesKey("direct play"))
	assert.Equal(t, "series", seriesKey("+++"))
	assert.Equal(t, "Direct play", seriesLabel("direct_play"))
	assert.Equal(t, "5m", promDuration(5*time.Minute))
	assert.Equal(t, "2h", promDuration(2*time.Hour))
}

// TestAPanelLeavesTheCachedAnswerAlone: the client's cache hands every
// render the same series, and a render that scaled them in place rescaled
// them on every refresh: Plex bandwidth's 40 Mb/s read 0 from the second
// render on (talos, 2026-10-22).
func TestAPanelLeavesTheCachedAnswerAlone(t *testing.T) {
	rg := dashRangeNamed("6h")
	end := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	start := end.Add(-rg.Span)
	q := oneAnswer{{Labels: map[string]string{}, Points: []prom.Point{{T: start, V: 5e6}}}}
	p := panelDef{ID: "bw", Kind: "area", Series: []panelSeries{{Key: "bandwidth", Query: "x", Scale: 8 / 1e6}}}

	first := buildPanel(context.Background(), q, p, rg, start, end)
	second := buildPanel(context.Background(), q, p, rg, start, end)

	assert.Equal(t, 40.0, first.Data[0]["bandwidth"])
	assert.Equal(t, 40.0, second.Data[0]["bandwidth"], "the same answer charts the same")
	assert.Equal(t, 5e6, q[0].Points[0].V)
}

func TestGroupDigits(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 7324: "7,324", 1234567: "1,234,567", -4500: "-4,500"} {
		assert.Equal(t, want, groupDigits(n), n)
	}
}

// A chart tab's figure is its panel's total, peak or latest step, over the
// series it shows: one series narrowed out of a panel keeps its colour and
// takes an id of its own.
func TestChartTabFigures(t *testing.T) {
	p := views.ChartPanel{
		ID: "grabs", Keys: []string{"grabbed", "imported"},
		Config: chart.Config{{Key: "grabbed", Label: "Grabbed", Color: "c1"}, {Key: "imported", Label: "Imported", Color: "c2"}},
		Data: []chart.Datum{
			{"t": "1", "grabbed": 2.0, "imported": 1.0},
			{"t": "2", "grabbed": 5.0, "imported": 4.0},
			{"t": "3", "grabbed": 1.0, "imported": 0.0},
		},
	}
	assert.InDelta(t, 13.0, totalOf(p), 0)
	assert.InDelta(t, 9.0, peakOf(p), 0)
	assert.InDelta(t, 1.0, latestOf(p), 0)

	grabbed := onlySeries(p, []string{"grabbed"}, "grabbed")
	assert.Equal(t, "grabs-grabbed", grabbed.ID)
	assert.Equal(t, []string{"grabbed"}, grabbed.Keys)
	assert.Equal(t, "c1", grabbed.Config[0].Color)
	assert.InDelta(t, 8.0, totalOf(grabbed), 0)
	assert.Equal(t, p, onlySeries(p, nil, "all"), "no keys is the whole panel")
}

func TestPageOf(t *testing.T) {
	rows := []int{1, 2, 3, 4, 5, 6, 7}
	for _, tc := range []struct {
		asked int
		want  []int
		page  views.ListPage
	}{
		{0, []int{1, 2, 3, 4, 5}, views.ListPage{Page: 1, Pages: 2}},
		{2, []int{6, 7}, views.ListPage{Page: 2, Pages: 2}},
		{9, []int{6, 7}, views.ListPage{Page: 2, Pages: 2}},
	} {
		got, page := pageOf(rows, tc.asked)
		assert.Equal(t, tc.want, got, tc.asked)
		assert.Equal(t, tc.page, page, tc.asked)
	}
	none, page := pageOf([]int(nil), 3)
	assert.Empty(t, none)
	assert.Equal(t, views.ListPage{Page: 1, Pages: 1}, page)
}
