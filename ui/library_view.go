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
	"net/url"
	"strconv"

	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// libraryView is the library toolbar's state (design 2026-09-24, after
// Radarr's): ?sort with ?dir, and ?filter. The page and its SSE stream
// parse it the same way and apply it through projection.Arrange, so a
// frame never shows rows the page would not.
type libraryView struct {
	Sort   projection.LibrarySort
	Desc   bool
	Filter projection.LibraryFilter
}

// parseLibraryView reads the view off a query; anything unknown is the
// default (title ascending, everything).
func parseLibraryView(q url.Values) libraryView {
	var v libraryView
	v.Sort, _ = projection.ParseLibrarySort(q.Get("sort"))
	v.Desc = q.Get("dir") == "desc"
	v.Filter, _ = projection.ParseLibraryFilter(q.Get("filter"))
	return v
}

// values are the view's parameters with the defaults left off, so a link
// to the default view is the plain page URL and two links to one view are
// the same string.
func (v libraryView) values() url.Values {
	out := url.Values{}
	if v.Sort != projection.SortTitle {
		out.Set("sort", string(v.Sort))
	}
	if v.Desc {
		out.Set("dir", "desc")
	}
	if v.Filter != projection.FilterAll {
		out.Set("filter", string(v.Filter))
	}
	return out
}

func (v libraryView) dir() string {
	if v.Desc {
		return "desc"
	}
	return "asc"
}

// arrange applies the view to a tab's items.
func (v libraryView) arrange(items []projection.LibraryItem) []projection.LibraryItem {
	return projection.Arrange(items, v.Sort, v.Desc, v.Filter)
}

// toolbar builds the Sort and Filter menus for a page at base with per
// items a page: every entry links to its view at page 1, the current sort's
// entry to the same sort the other way round, as Radarr flips a sort by
// choosing it again.
func (v libraryView) toolbar(base string, per int) views.LibraryToolbar {
	link := func(w libraryView) string {
		q := w.values()
		q.Set("per", strconv.Itoa(per))
		return base + "?" + q.Encode()
	}
	tb := views.LibraryToolbar{Sort: string(v.Sort), Dir: v.dir(), Filter: string(v.Filter)}
	for _, s := range projection.LibrarySorts() {
		w := libraryView{Sort: s, Filter: v.Filter}
		c := views.Choice{Key: string(s), Label: s.Label(), Active: s == v.Sort}
		if c.Active {
			w.Desc = !v.Desc
			c.Dir = v.dir()
		}
		c.Href = link(w)
		tb.Sorts = append(tb.Sorts, c)
	}
	for _, f := range projection.LibraryFilters() {
		w := v
		w.Filter = f
		tb.Filters = append(tb.Filters, views.Choice{Key: string(f), Label: f.Label(), Href: link(w), Active: f == v.Filter})
	}
	return tb
}
