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

package paging_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/paging"
)

// TestParseDefaultsAndCaps: a page URL's ?page and ?per are optional and
// never trusted -- a missing, malformed or out-of-range value falls back
// rather than failing the request, and per is capped so a URL cannot ask
// for the whole library in one response.
func TestParseDefaultsAndCaps(t *testing.T) {
	for name, tc := range map[string]struct {
		query string
		want  paging.Request
	}{
		"empty":          {"", paging.Request{Number: 1, Per: paging.DefaultPer}},
		"explicit":       {"page=3&per=25", paging.Request{Number: 3, Per: 25}},
		"per capped":     {"per=9999", paging.Request{Number: 1, Per: paging.MaxPer}},
		"per zero":       {"per=0", paging.Request{Number: 1, Per: paging.DefaultPer}},
		"page negative":  {"page=-2", paging.Request{Number: 1, Per: paging.DefaultPer}},
		"page malformed": {"page=abc&per=x", paging.Request{Number: 1, Per: paging.DefaultPer}},
	} {
		t.Run(name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			require.NoError(t, err)
			require.Equal(t, tc.want, paging.Parse(q))
		})
	}
}

// TestPaginateClampsAndWindows pins the window arithmetic every page and
// stream slices with: offsets, the short last page, a page past the end
// clamping to the last one, and an empty list reading as page 1 of 1
// showing nothing.
func TestPaginateClampsAndWindows(t *testing.T) {
	for name, tc := range map[string]struct {
		total, number, per int
		want               paging.Page
		from, to           int
	}{
		"first of three": {120, 1, 50, paging.Page{Number: 1, Per: 50, Total: 120, Last: 3, Offset: 0, Count: 50}, 1, 50},
		"short last":     {120, 3, 50, paging.Page{Number: 3, Per: 50, Total: 120, Last: 3, Offset: 100, Count: 20}, 101, 120},
		"past the end":   {120, 9, 50, paging.Page{Number: 3, Per: 50, Total: 120, Last: 3, Offset: 100, Count: 20}, 101, 120},
		"middle":         {120, 2, 50, paging.Page{Number: 2, Per: 50, Total: 120, Last: 3, Offset: 50, Count: 50}, 51, 100},
		"exact fit":      {100, 2, 50, paging.Page{Number: 2, Per: 50, Total: 100, Last: 2, Offset: 50, Count: 50}, 51, 100},
		"empty":          {0, 1, 50, paging.Page{Number: 1, Per: 50, Total: 0, Last: 1, Offset: 0, Count: 0}, 0, 0},
		"empty past end": {0, 4, 50, paging.Page{Number: 1, Per: 50, Total: 0, Last: 1, Offset: 0, Count: 0}, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			p := paging.Paginate(tc.total, tc.number, tc.per)
			require.Equal(t, tc.want, p)
			require.Equal(t, tc.from, p.From())
			require.Equal(t, tc.to, p.To())
			require.Equal(t, p, paging.Request{Number: tc.number, Per: tc.per}.Page(tc.total))
		})
	}
}

// TestWindowSlices: Window returns exactly the page's items, and never
// panics on a page computed for a longer list than the one given.
func TestWindowSlices(t *testing.T) {
	items := make([]int, 120)
	for i := range items {
		items[i] = i
	}
	got := paging.Window(items, paging.Paginate(120, 3, 50))
	require.Len(t, got, 20)
	require.Equal(t, 100, got[0])
	require.Equal(t, 119, got[19])

	require.Empty(t, paging.Window(items[:10], paging.Paginate(120, 3, 50)), "a window past a shorter list is empty, not a panic")
	require.Equal(t, items[50:60], paging.Window(items[:60], paging.Paginate(120, 2, 50)), "a window overlapping the end is cut, not a panic")
}

// TestNumbersShowsNeighboursWithGaps: the pager shows the first and last
// page, the current one and its neighbours, and a gap (0) where pages are
// skipped -- so a 17-page library is a short strip, not seventeen links.
func TestNumbersShowsNeighboursWithGaps(t *testing.T) {
	for name, tc := range map[string]struct {
		total, number, per int
		want               []int
	}{
		"one page":      {10, 1, 50, []int{1}},
		"three pages":   {120, 2, 50, []int{1, 2, 3}},
		"middle of ten": {500, 5, 50, []int{1, 0, 4, 5, 6, 0, 10}},
		"start of ten":  {500, 1, 50, []int{1, 2, 0, 10}},
		"end of ten":    {500, 10, 50, []int{1, 0, 9, 10}},
		"no single gap": {500, 3, 50, []int{1, 2, 3, 4, 0, 10}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, paging.Paginate(tc.total, tc.number, tc.per).Numbers())
		})
	}
}

// TestQueryCarriesPer: every pager link keeps the chosen page size, so
// moving between pages never silently resets it.
func TestQueryCarriesPer(t *testing.T) {
	p := paging.Paginate(120, 2, 25)
	require.Equal(t, "?page=3&per=25", p.Query(3))
	require.True(t, p.HasPrev())
	require.True(t, p.HasNext())
	require.False(t, paging.Paginate(120, 1, 50).HasPrev())
	require.False(t, paging.Paginate(120, 3, 50).HasNext())
}
