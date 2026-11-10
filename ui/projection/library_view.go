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
	"time"
	"unicode"
	"unicode/utf8"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
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
	if li.DeleteMode != "" {
		return "deleting"
	}
	if li.Kind == commonv1.MediaKindSeries {
		return seriesStatus(li)
	}
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

// seriesStatus is a Series' LibraryStatus, Sonarr's reading of its
// episodes: Downloading while an episode's grab is in flight, Missing while
// a monitored, aired episode has no file, Downloaded once files are on disk
// and nothing is missing, and Unmonitored with nothing on disk and nothing
// wanted. A Series has no file of its own and its phase is only Ready,
// Pending or Unmonitored, so the rule for the other kinds read every
// monitored series as Missing.
func seriesStatus(li LibraryItem) string {
	switch {
	case li.DownloadingEpisodes > 0:
		return "downloading"
	case li.MissingEpisodes > 0:
		return "missing"
	case li.HasFile:
		return "downloaded"
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
	// The rest (owner, 2026-11-10, after Radarr's and Sonarr's sorts):
	// an unknown value (no rating, no next airing) sorts last either way.
	SortAdded          LibrarySort = "added"
	SortSize           LibrarySort = "size"
	SortRating         LibrarySort = "rating"
	SortRuntime        LibrarySort = "runtime"
	SortReleased       LibrarySort = "released"
	SortNetwork        LibrarySort = "network"
	SortNextAiring     LibrarySort = "nextAiring"
	SortPreviousAiring LibrarySort = "previousAiring"
	SortEpisodes       LibrarySort = "episodes"
)

var librarySorts = []LibrarySort{SortTitle, SortYear, SortProfile, SortStatus, SortAdded}

// tabSorts are a tab's sorts after the shared ones.
var tabSorts = map[Tab][]LibrarySort{
	TabMovies: {SortReleased, SortRuntime, SortRating, SortSize},
	TabTV:     {SortNetwork, SortNextAiring, SortPreviousAiring, SortEpisodes, SortRuntime, SortRating, SortSize},
}

// LibrarySorts lists a tab's sort keys in the menu's order.
func LibrarySorts(tab Tab) []LibrarySort {
	return append(slices.Clone(librarySorts), tabSorts[tab]...)
}

// ParseLibrarySort maps a ?sort value onto one of tab's keys; anything
// else, the empty string included, is SortTitle with ok false.
func ParseLibrarySort(tab Tab, s string) (LibrarySort, bool) {
	if slices.Contains(LibrarySorts(tab), LibrarySort(s)) {
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
	case SortAdded:
		return "Added"
	case SortSize:
		return "Size on Disk"
	case SortRating:
		return "Rating"
	case SortRuntime:
		return "Runtime"
	case SortReleased:
		return "Release Date"
	case SortNetwork:
		return "Network"
	case SortNextAiring:
		return "Next Airing"
	case SortPreviousAiring:
		return "Previous Airing"
	case SortEpisodes:
		return "Episode Progress"
	default:
		return "Title"
	}
}

// LibraryFilter is one of Radarr's (and Sonarr's) filter presets; its
// string is the ?filter value.
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
	// FilterDownloading keeps items with a grab in flight.
	FilterDownloading LibraryFilter = "downloading"
	// FilterContinuing and FilterEnded are Sonarr's "Continuing Only" and
	// "Ended Only": a series by its run status.
	FilterContinuing LibraryFilter = "continuing"
	FilterEnded      LibraryFilter = "ended"
)

var libraryFilters = []LibraryFilter{FilterAll, FilterMonitored, FilterUnmonitored, FilterMissing, FilterWanted, FilterCutoffUnmet, FilterDownloading}

// tabFilters are a tab's presets after the shared ones.
var tabFilters = map[Tab][]LibraryFilter{
	TabTV: {FilterContinuing, FilterEnded},
}

// LibraryFilters lists a tab's presets in the menu's order.
func LibraryFilters(tab Tab) []LibraryFilter {
	return append(slices.Clone(libraryFilters), tabFilters[tab]...)
}

// ParseLibraryFilter maps a ?filter value onto one of tab's presets;
// anything else, the empty string included, is FilterAll with ok false.
func ParseLibraryFilter(tab Tab, s string) (LibraryFilter, bool) {
	if slices.Contains(LibraryFilters(tab), LibraryFilter(s)) {
		return LibraryFilter(s), true
	}
	return FilterAll, false
}

// Label is the preset's menu entry, Radarr's and Sonarr's wording.
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
	case FilterDownloading:
		return "Downloading"
	case FilterContinuing:
		return "Continuing Only"
	case FilterEnded:
		return "Ended Only"
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
	case FilterMissing, FilterWanted:
		if li.Kind == commonv1.MediaKindSeries {
			// A series is missing, and wanted, while any episode is
			// (Sonarr's Wanted > Missing): episodes are counted missing
			// only once aired, and only while their series is monitored.
			return li.MissingEpisodes > 0 || li.DownloadingEpisodes > 0
		}
		if f == FilterMissing {
			return li.Monitored && !li.HasFile
		}
		return li.Monitored && !li.HasFile && li.Phase != "Pending" && li.Phase != "Unavailable"
	case FilterCutoffUnmet:
		return li.Monitored && LibraryStatus(li) == "cutoff-unmet"
	case FilterDownloading:
		return LibraryStatus(li) == "downloading"
	case FilterContinuing:
		return li.ReleaseStatus == "continuing" || li.ReleaseStatus == "upcoming"
	case FilterEnded:
		return li.ReleaseStatus == "ended"
	default:
		return true
	}
}

// LibraryQuery is the toolbar's whole view of a tab: the sort and its
// direction, the preset, the custom filter's conditions (every one kept)
// and the moment date conditions measure from.
type LibraryQuery struct {
	Tab        Tab
	Sort       LibrarySort
	Desc       bool
	Filter     LibraryFilter
	Conditions []Condition
	Now        time.Time
}

// Keeps reports whether the query's preset and every condition keep li.
func (q LibraryQuery) Keeps(li LibraryItem) bool {
	if q.Filter != "" && !q.Filter.Matches(li) {
		return false
	}
	for _, c := range q.Conditions {
		if !c.Matches(q.Tab, li, q.Now) {
			return false
		}
	}
	return true
}

// sortValue is one item's sort key: a number or a string, or unknown,
// which sorts last in either direction.
type sortValue struct {
	unknown bool
	n       float64
	s       string
}

func sortValueOf(sort LibrarySort, li LibraryItem) sortValue {
	date := func(t time.Time) sortValue {
		if t.IsZero() {
			return sortValue{unknown: true}
		}
		return sortValue{n: float64(t.Unix())}
	}
	switch sort {
	case SortYear:
		return sortValue{n: float64(li.Year)}
	case SortProfile:
		return sortValue{s: li.QualityProfileRef}
	case SortStatus:
		return sortValue{n: float64(statusRank(LibraryStatus(li)))}
	case SortAdded:
		return date(li.Added)
	case SortSize:
		return sortValue{n: float64(li.SizeBytes)}
	case SortRating:
		for _, src := range []catalogv1.RatingSource{catalogv1.RatingSourceTMDB, catalogv1.RatingSourceIMDb} {
			if v, ok := rating(src)(li); ok {
				return sortValue{n: v}
			}
		}
		return sortValue{unknown: true}
	case SortRuntime:
		if li.RuntimeMinutes <= 0 {
			return sortValue{unknown: true}
		}
		return sortValue{n: float64(li.RuntimeMinutes)}
	case SortReleased:
		for _, t := range []time.Time{li.DigitalRelease, li.PhysicalRelease, li.InCinemas} {
			if !t.IsZero() {
				return date(t)
			}
		}
		return sortValue{unknown: true}
	case SortNetwork:
		if len(li.Networks) == 0 {
			return sortValue{unknown: true}
		}
		return sortValue{s: strings.ToLower(li.Networks[0])}
	case SortNextAiring:
		return date(li.NextAiring)
	case SortPreviousAiring:
		return date(li.PreviousAiring)
	case SortEpisodes:
		if v, ok := episodeProgress(li); ok {
			return sortValue{n: v}
		}
		return sortValue{unknown: true}
	default:
		return sortValue{s: li.Title}
	}
}

func compareSortValues(a, b sortValue) int {
	if a.n != b.n {
		if a.n < b.n {
			return -1
		}
		return 1
	}
	return strings.Compare(a.s, b.s)
}

// Arrange is the toolbar's view applied: the items the preset and the
// conditions keep, ordered by the sort (reversed when desc, an unknown
// value last either way) with the title, then the name, as the tiebreak.
// The input is left as it was, and an empty result is an empty slice, never
// nil, so a stream frame still renders.
func Arrange(items []LibraryItem, q LibraryQuery) []LibraryItem {
	type keyed struct {
		li  LibraryItem
		key sortValue
	}
	kept := make([]keyed, 0, len(items))
	for _, li := range items {
		if q.Keeps(li) {
			kept = append(kept, keyed{li, sortValueOf(q.Sort, li)})
		}
	}
	slices.SortStableFunc(kept, func(a, b keyed) int {
		switch {
		case a.key.unknown != b.key.unknown:
			if a.key.unknown {
				return 1
			}
			return -1
		case !a.key.unknown:
			if c := compareSortValues(a.key, b.key); c != 0 {
				if q.Desc {
					return -c
				}
				return c
			}
		}
		if c := strings.Compare(a.li.Title, b.li.Title); c != 0 {
			return c
		}
		return strings.Compare(a.li.Ref.Name, b.li.Ref.Name)
	})
	out := make([]LibraryItem, len(kept))
	for i, k := range kept {
		out[i] = k.li
	}
	return out
}

// JumpLetter is the letter a title files under on the A-Z bar: its first
// rune upper-cased when that is A-Z, else "#". The bar's links and the
// jump redirect both use it.
func JumpLetter(title string) string {
	r, _ := utf8.DecodeRuneInString(title)
	r = unicode.ToUpper(r)
	if r >= 'A' && r <= 'Z' {
		return string(r)
	}
	return "#"
}
