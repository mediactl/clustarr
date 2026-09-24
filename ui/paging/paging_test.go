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
		"empty":          {"", paging.Request{Number: 1, Per: paging.DefaultPer, Pages: 1}},
		"explicit":       {"page=3&per=25", paging.Request{Number: 3, Per: 25, Pages: 1}},
		"per capped":     {"per=9999", paging.Request{Number: 1, Per: paging.MaxPer, Pages: 1}},
		"per zero":       {"per=0", paging.Request{Number: 1, Per: paging.DefaultPer, Pages: 1}},
		"page negative":  {"page=-2", paging.Request{Number: 1, Per: paging.DefaultPer, Pages: 1}},
		"page malformed": {"page=abc&per=x", paging.Request{Number: 1, Per: paging.DefaultPer, Pages: 1}},
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
		"first of three": {120, 1, 50, paging.Page{Number: 1, Per: 50, Total: 120, Last: 3, Offset: 0, Count: 50, Pages: 1}, 1, 50},
		"short last":     {120, 3, 50, paging.Page{Number: 3, Per: 50, Total: 120, Last: 3, Offset: 100, Count: 20, Pages: 1}, 101, 120},
		"past the end":   {120, 9, 50, paging.Page{Number: 3, Per: 50, Total: 120, Last: 3, Offset: 100, Count: 20, Pages: 1}, 101, 120},
		"middle":         {120, 2, 50, paging.Page{Number: 2, Per: 50, Total: 120, Last: 3, Offset: 50, Count: 50, Pages: 1}, 51, 100},
		"exact fit":      {100, 2, 50, paging.Page{Number: 2, Per: 50, Total: 100, Last: 2, Offset: 50, Count: 50, Pages: 1}, 51, 100},
		"empty":          {0, 1, 50, paging.Page{Number: 1, Per: 50, Total: 0, Last: 1, Offset: 0, Count: 0, Pages: 1}, 0, 0},
		"empty past end": {0, 4, 50, paging.Page{Number: 1, Per: 50, Total: 0, Last: 1, Offset: 0, Count: 0, Pages: 1}, 0, 0},
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

// TestQueryCarriesParams: a page's links keep every parameter the caller
// asked to carry -- the library's sort and filter -- in url.Values' canonical
// (sorted) order, so two links to the same view are byte-identical, and a
// page with nothing to carry still renders the old "?page=N&per=M".
func TestQueryCarriesParams(t *testing.T) {
	req := paging.Parse(url.Values{"page": {"2"}, "per": {"25"}})
	req.Params = url.Values{"sort": {"year"}, "filter": {"missing"}}
	p := req.Page(120)
	require.Equal(t, "?filter=missing&page=3&per=25&sort=year", p.Query(3))
	require.Equal(t, "?page=3&per=25", paging.Paginate(120, 2, 25).Query(3), "no params: unchanged")

	v := p.Values(1)
	require.Equal(t, url.Values{"page": {"1"}, "per": {"25"}, "sort": {"year"}, "filter": {"missing"}}, v)
	v.Set("jump", "M")
	v.Del("page")
	require.Equal(t, "?filter=missing&page=1&per=25&sort=year", p.Query(1), "Values returns a copy")
	require.Equal(t, "filter=missing&jump=M&per=25&sort=year", v.Encode())

	req.Params = url.Values{"filter": {"#"}}
	require.Equal(t, "?filter=%23&page=1&per=25", req.Page(1).Query(1), "values are escaped")
}

// TestPagesGrowTheWindow: ?pages=k widens a window to k pages from its
// first one -- what the library's infinite scroll has loaded so far, so
// its stream frames carry everything on screen. Pages defaults to 1, is
// capped, and shows in a link only when it is more than 1.
func TestPagesGrowTheWindow(t *testing.T) {
	req := paging.Parse(url.Values{"page": {"2"}, "per": {"25"}, "pages": {"3"}})
	require.Equal(t, paging.Request{Number: 2, Per: 25, Pages: 3}, req)
	p := req.Page(120)
	require.Equal(t, 25, p.Offset)
	require.Equal(t, 75, p.Count, "three pages of 25 from the second")
	require.Equal(t, 3, p.Pages)
	require.True(t, p.HasNext(), "100 of 120 shown")
	require.Equal(t, 100, p.To())
	require.Equal(t, "?page=2&pages=3&per=25", p.Query(2))
	require.Equal(t, []int{25, 100}, []int{p.Offset, p.Offset + p.Count})

	req.Pages = 4
	p = req.Page(120)
	require.Equal(t, 95, p.Count, "the window is cut at the end")
	require.False(t, p.HasNext())

	require.Equal(t, 1, paging.Parse(url.Values{"pages": {"0"}}).Pages)
	require.Equal(t, 1, paging.Parse(url.Values{"pages": {"x"}}).Pages)
	require.Equal(t, paging.MaxPages, paging.Parse(url.Values{"pages": {"9999"}}).Pages)
	require.Equal(t, 1, paging.Parse(url.Values{}).Pages)
	require.Equal(t, "?page=1&per=50", paging.Parse(url.Values{}).Page(10).Query(1), "one page: no pages parameter")
	require.Equal(t, 1, paging.Paginate(120, 1, 50).Pages)
	require.Equal(t, 50, paging.Paginate(120, 1, 50).Count)
}
