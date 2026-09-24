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

package projection

import (
	"slices"
	"strings"
)

// The library toolbar's view (design 2026-09-24, after Radarr's): a sort
// key with a direction and a filter preset, applied by Arrange to a tab's
// items. Everything here is a pure function of the projection's rows, so
// the page and its SSE stream apply one rule and a test needs no server.

// LibraryStatus is the state Radarr's stripe under a poster shows, from
// the item's on-disk, phase and monitored fields in that priority:
// "cutoff-unmet" (on disk, below the profile's cutoff or not yet
// evaluated), "downloaded" (on disk), "downloading" (a grab in flight,
// delayed or not), "missing" (monitored, nothing on disk) or
// "unmonitored".
func LibraryStatus(li LibraryItem) string {
	switch {
	case li.HasFile && (li.Phase == "CutoffUnmet" || li.Phase == "CutoffUnevaluated"):
		return "cutoff-unmet"
	case li.HasFile:
		return "downloaded"
	case li.Phase == "Downloading" || li.Phase == "Delayed" || li.Phase == "Grabbed":
		return "downloading"
	case li.Monitored:
		return "missing"
	default:
		return "unmonitored"
	}
}

// statusRank orders LibraryStatus by what needs attention first.
func statusRank(status string) int {
	switch status {
	case "missing":
		return 0
	case "downloading":
		return 1
	case "cutoff-unmet":
		return 2
	case "downloaded":
		return 3
	default:
		return 4
	}
}

// LibrarySort is a key the library grid can be ordered by; its string is
// the ?sort value.
type LibrarySort string

const (
	// SortTitle is the default: the projection's own order, by title.
	SortTitle LibrarySort = "title"
	// SortYear orders by release year, unknown (0) first.
	SortYear LibrarySort = "year"
	// SortProfile orders by quality profile name.
	SortProfile LibrarySort = "profile"
	// SortStatus orders by LibraryStatus, missing first.
	SortStatus LibrarySort = "status"
)

var librarySorts = []LibrarySort{SortTitle, SortYear, SortProfile, SortStatus}

// LibrarySorts lists the sort keys in the menu's order.
func LibrarySorts() []LibrarySort { return slices.Clone(librarySorts) }

// ParseLibrarySort maps a ?sort value onto its key; anything unknown,
// the empty string included, is SortTitle with ok false.
func ParseLibrarySort(s string) (LibrarySort, bool) {
	if slices.Contains(librarySorts, LibrarySort(s)) {
		return LibrarySort(s), true
	}
	return SortTitle, false
}

// Label is the sort's menu entry.
func (s LibrarySort) Label() string {
	switch s {
	case SortYear:
		return "Year"
	case SortProfile:
		return "Quality Profile"
	case SortStatus:
		return "Status"
	default:
		return "Title"
	}
}

// LibraryFilter is one of Radarr's filter presets; its string is the
// ?filter value.
type LibraryFilter string

const (
	// FilterAll keeps everything.
	FilterAll LibraryFilter = "all"
	// FilterMonitored keeps monitored items.
	FilterMonitored LibraryFilter = "monitored"
	// FilterUnmonitored keeps unmonitored items.
	FilterUnmonitored LibraryFilter = "unmonitored"
	// FilterMissing keeps monitored items with nothing on disk.
	FilterMissing LibraryFilter = "missing"
	// FilterWanted keeps FilterMissing's items that are out (not Pending
	// metadata, not Unavailable yet).
	FilterWanted LibraryFilter = "wanted"
	// FilterCutoffUnmet keeps monitored items on disk below their
	// profile's cutoff.
	FilterCutoffUnmet LibraryFilter = "cutoff-unmet"
)

var libraryFilters = []LibraryFilter{FilterAll, FilterMonitored, FilterUnmonitored, FilterMissing, FilterWanted, FilterCutoffUnmet}

// LibraryFilters lists the presets in the menu's order.
func LibraryFilters() []LibraryFilter { return slices.Clone(libraryFilters) }

// ParseLibraryFilter maps a ?filter value onto its preset; anything
// unknown, the empty string included, is FilterAll with ok false.
func ParseLibraryFilter(s string) (LibraryFilter, bool) {
	if slices.Contains(libraryFilters, LibraryFilter(s)) {
		return LibraryFilter(s), true
	}
	return FilterAll, false
}

// Label is the preset's menu entry, Radarr's wording.
func (f LibraryFilter) Label() string {
	switch f {
	case FilterMonitored:
		return "Monitored Only"
	case FilterUnmonitored:
		return "Unmonitored Only"
	case FilterMissing:
		return "Missing"
	case FilterWanted:
		return "Wanted"
	case FilterCutoffUnmet:
		return "Cutoff Unmet"
	default:
		return "All"
	}
}

// Matches reports whether the preset keeps li.
func (f LibraryFilter) Matches(li LibraryItem) bool {
	switch f {
	case FilterMonitored:
		return li.Monitored
	case FilterUnmonitored:
		return !li.Monitored
	case FilterMissing:
		return li.Monitored && !li.HasFile
	case FilterWanted:
		return li.Monitored && !li.HasFile && li.Phase != "Pending" && li.Phase != "Unavailable"
	case FilterCutoffUnmet:
		return li.Monitored && LibraryStatus(li) == "cutoff-unmet"
	default:
		return true
	}
}

// Arrange is the toolbar's view applied: the items filter keeps, ordered
// by sort (reversed when desc) with the title, then the name, as the
// tiebreak either way. The input is left as it was, and an empty result is
// an empty slice, never nil, so a stream frame still renders.
func Arrange(items []LibraryItem, sort LibrarySort, desc bool, filter LibraryFilter) []LibraryItem {
	out := make([]LibraryItem, 0, len(items))
	for _, li := range items {
		if filter.Matches(li) {
			out = append(out, li)
		}
	}
	key := func(a, b LibraryItem) int {
		switch sort {
		case SortYear:
			return int(a.Year) - int(b.Year)
		case SortProfile:
			return strings.Compare(a.QualityProfileRef, b.QualityProfileRef)
		case SortStatus:
			return statusRank(LibraryStatus(a)) - statusRank(LibraryStatus(b))
		default:
			return strings.Compare(a.Title, b.Title)
		}
	}
	slices.SortStableFunc(out, func(a, b LibraryItem) int {
		if c := key(a, b); c != 0 {
			if desc {
				return -c
			}
			return c
		}
		if c := strings.Compare(a.Title, b.Title); c != 0 {
			return c
		}
		return strings.Compare(a.Ref.Name, b.Ref.Name)
	})
	return out
}
