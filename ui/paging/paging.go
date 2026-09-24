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

// Package paging is the one window arithmetic the ui's list pages and
// their SSE streams share: a request's ?page and ?per, clamped against
// the list's length into a [Page], and the slice of the list that page
// shows. It is pure Go with no view or cluster dependency, so ui/views
// renders a pager from a Page and ui/routes.go slices with the same one,
// and neither can disagree about which rows are on page 3.
package paging

import (
	"net/url"
	"slices"
	"strconv"
)

const (
	// DefaultPer is the page size when the URL names none.
	DefaultPer = 50
	// MaxPer caps ?per, so one URL cannot ask for the whole library.
	MaxPer = 500
	// MaxPages caps ?pages, the library's scrolled window, for the same
	// reason.
	MaxPages = 100
)

// Request is what the URL asked for, before it is clamped against a list:
// a 1-based page number and a page size. [Parse] never returns a value a
// page could not honour.
type Request struct {
	Number int
	Per    int
	// Pages is how many pages from Number the window spans: 1 for a paged
	// list, more for the library's infinite scroll, which widens its window
	// by one page per load so a stream frame carries everything on screen.
	Pages int
	// Params are the query parameters every link of the page carries
	// besides page and per -- the library's sort and filter. Parse leaves
	// it nil; the caller sets the parameters it recognised, already
	// canonical, and [Page.Values] and [Page.Query] carry them.
	Params url.Values
}

// Parse reads ?page and ?per. A missing, malformed or non-positive value
// falls back to page 1 or [DefaultPer]; a per above [MaxPer] is capped.
func Parse(q url.Values) Request {
	r := Request{Number: 1, Per: DefaultPer}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 0 {
		r.Number = n
	}
	if per, err := strconv.Atoi(q.Get("per")); err == nil && per > 0 {
		r.Per = min(per, MaxPer)
	}
	r.Pages = 1
	if pages, err := strconv.Atoi(q.Get("pages")); err == nil && pages > 1 {
		r.Pages = min(pages, MaxPages)
	}
	return r
}

// Page returns the window r selects over a list of total items: Pages
// pages from page Number, cut at the end of the list.
func (r Request) Page(total int) Page {
	p := Paginate(total, r.Number, r.Per)
	p.Params = r.Params
	if r.Pages > 1 {
		p.Pages = r.Pages
		p.Count = max(min(p.Per*p.Pages, p.Total-p.Offset), 0)
	}
	return p
}

// Page is one window over a list: the items from Offset for Count, on
// page Number of Last. Number is clamped into [1, Last] and Last is at
// least 1, so an empty list is page 1 of 1 showing nothing, and a page past
// the end is the last page rather than an empty one.
type Page struct {
	Number int
	Per    int
	Total  int
	Last   int
	Offset int
	Count  int
	// Pages is [Request.Pages]: how many pages from Number the window
	// spans; 1 from [Paginate].
	Pages int
	// Params are [Request.Params], carried by every link; nil from
	// [Paginate].
	Params url.Values
}

// Paginate clamps number against total items of per each. A non-positive
// per reads as [DefaultPer].
func Paginate(total, number, per int) Page {
	if per <= 0 {
		per = DefaultPer
	}
	total = max(total, 0)
	last := max((total+per-1)/per, 1)
	number = min(max(number, 1), last)
	offset := (number - 1) * per
	count := min(per, total-offset)
	return Page{Number: number, Per: per, Total: total, Last: last, Offset: offset, Count: max(count, 0), Pages: 1}
}

// Window is the slice of items p shows. A list shorter than the one p was
// computed for is cut, never over-read: a stream's list can shrink
// between the page load and the next push.
func Window[T any](items []T, p Page) []T {
	if p.Offset >= len(items) {
		return nil
	}
	end := min(p.Offset+p.Count, len(items))
	return items[p.Offset:end]
}

// From is the 1-based index of the first item shown; 0 when nothing is.
func (p Page) From() int {
	if p.Count == 0 {
		return 0
	}
	return p.Offset + 1
}

// To is the 1-based index of the last item shown; 0 when nothing is.
func (p Page) To() int {
	if p.Count == 0 {
		return 0
	}
	return p.Offset + p.Count
}

// HasPrev reports whether a page precedes this one.
func (p Page) HasPrev() bool { return p.Number > 1 }

// HasNext reports whether the list goes on past this window.
func (p Page) HasNext() bool { return p.Offset+p.Count < p.Total }

// Values are the query parameters selecting page n at this page's size
// and span, with Params, as a fresh copy the caller may edit -- the A-Z
// bar swaps page for jump, the scroll sentinel widens pages.
func (p Page) Values(n int) url.Values {
	v := url.Values{"page": {strconv.Itoa(n)}, "per": {strconv.Itoa(p.Per)}}
	if p.Pages > 1 {
		v.Set("pages", strconv.Itoa(p.Pages))
	}
	for k, vs := range p.Params {
		v[k] = slices.Clone(vs)
	}
	return v
}

// Query is the query string selecting page n at this page's size with
// Params, so a pager link never drops the size the reader chose nor the
// view they are in. url.Values encodes keys in sorted order, so two links
// to one view are byte-identical.
func (p Page) Query(n int) string {
	return "?" + p.Values(n).Encode()
}

// Numbers is the strip of page numbers a pager shows: the first and last
// page, the current one and its neighbours, with 0 where pages are
// skipped. A single skipped page is shown rather than replaced by a gap
// that would take the same room.
func (p Page) Numbers() []int {
	show := func(n int) bool {
		return n == 1 || n == p.Last || (n >= p.Number-1 && n <= p.Number+1)
	}
	var out []int
	prev := 0
	for n := 1; n <= p.Last; n++ {
		if !show(n) {
			continue
		}
		switch n - prev {
		case 1, 0:
		case 2:
			out = append(out, n-1)
		default:
			out = append(out, 0)
		}
		out = append(out, n)
		prev = n
	}
	return out
}
