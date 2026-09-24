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

package projection_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui/projection"
)

// The library toolbar's Sort and Filter menus (design 2026-09-24, after
// Radarr's) are pure functions over the projection's items: LibraryStatus
// is the stripe under a poster, a LibraryFilter keeps the items Radarr's
// preset of that name keeps, and Arrange filters then orders a tab.

func viewItem(name string, monitored, hasFile bool, phase string, year int32, profile string) projection.LibraryItem {
	return projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: name}, Title: name,
		Monitored: monitored, HasFile: hasFile, Phase: phase, Year: year, QualityProfileRef: profile,
	}
}

func names(items []projection.LibraryItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Ref.Name)
	}
	return out
}

func TestLibraryStatusFollowsRadarrsStripe(t *testing.T) {
	for name, tc := range map[string]struct {
		item projection.LibraryItem
		want string
	}{
		"on disk":            {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "Imported"}, "downloaded"},
		"transcoded":         {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "Transcoded"}, "downloaded"},
		"cutoff unmet":       {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "CutoffUnmet"}, "cutoff-unmet"},
		"cutoff unevaluated": {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "CutoffUnevaluated"}, "cutoff-unmet"},
		"downloading":        {projection.LibraryItem{Monitored: true, Phase: "Downloading"}, "downloading"},
		"delayed":            {projection.LibraryItem{Monitored: true, Phase: "Delayed"}, "downloading"},
		"grabbed":            {projection.LibraryItem{Monitored: true, Phase: "Grabbed"}, "downloading"},
		"missing":            {projection.LibraryItem{Monitored: true, Phase: "Wanted"}, "missing"},
		"unmonitored":        {projection.LibraryItem{Monitored: false, Phase: "Unmonitored"}, "unmonitored"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, projection.LibraryStatus(tc.item))
		})
	}
}

func TestParseLibrarySortAndFilterFallBackToTheDefault(t *testing.T) {
	s, ok := projection.ParseLibrarySort("year")
	require.True(t, ok)
	require.Equal(t, projection.SortYear, s)
	s, ok = projection.ParseLibrarySort("")
	require.False(t, ok)
	require.Equal(t, projection.SortTitle, s, "an absent sort is the title")
	s, ok = projection.ParseLibrarySort("runtime")
	require.False(t, ok)
	require.Equal(t, projection.SortTitle, s, "an unknown sort is the title")

	f, ok := projection.ParseLibraryFilter("cutoff-unmet")
	require.True(t, ok)
	require.Equal(t, projection.FilterCutoffUnmet, f)
	f, ok = projection.ParseLibraryFilter("nope")
	require.False(t, ok)
	require.Equal(t, projection.FilterAll, f, "an unknown filter shows everything")

	require.Equal(t, []projection.LibrarySort{projection.SortTitle, projection.SortYear, projection.SortProfile, projection.SortStatus}, projection.LibrarySorts())
	require.Equal(t, []projection.LibraryFilter{
		projection.FilterAll, projection.FilterMonitored, projection.FilterUnmonitored,
		projection.FilterMissing, projection.FilterWanted, projection.FilterCutoffUnmet,
	}, projection.LibraryFilters())
	for _, s := range projection.LibrarySorts() {
		require.NotEmpty(t, s.Label(), "sort %q has a menu label", s)
	}
	for _, f := range projection.LibraryFilters() {
		require.NotEmpty(t, f.Label(), "filter %q has a menu label", f)
	}
	require.Equal(t, "Quality Profile", projection.SortProfile.Label())
	require.Equal(t, "Monitored Only", projection.FilterMonitored.Label())
	require.Equal(t, "Cutoff Unmet", projection.FilterCutoffUnmet.Label())
}

// TestLibraryFilterMatchesRadarrsPresets pins each preset's rule: Missing
// is monitored with nothing on disk, Wanted is Missing for a released
// item (not Pending, not Unavailable), Cutoff Unmet is monitored, on disk
// and below the profile's cutoff.
func TestLibraryFilterMatchesRadarrsPresets(t *testing.T) {
	imported := viewItem("imported", true, true, "Imported", 2016, "hd")
	cutoff := viewItem("cutoff", true, true, "CutoffUnmet", 2016, "hd")
	cutoffUnmonitored := viewItem("cutoff-unmonitored", false, true, "CutoffUnmet", 2016, "hd")
	wanted := viewItem("wanted", true, false, "Wanted", 2016, "hd")
	pending := viewItem("pending", true, false, "Pending", 0, "hd")
	unavailable := viewItem("unavailable", true, false, "Unavailable", 2027, "hd")
	unmonitoredMissing := viewItem("unmonitored-missing", false, false, "Unmonitored", 2016, "hd")

	for name, tc := range map[string]struct {
		filter projection.LibraryFilter
		want   map[string]bool
	}{
		"all": {projection.FilterAll, map[string]bool{
			"imported": true, "cutoff": true, "cutoff-unmonitored": true, "wanted": true, "pending": true, "unavailable": true, "unmonitored-missing": true,
		}},
		"monitored": {projection.FilterMonitored, map[string]bool{
			"imported": true, "cutoff": true, "wanted": true, "pending": true, "unavailable": true,
		}},
		"unmonitored": {projection.FilterUnmonitored, map[string]bool{
			"cutoff-unmonitored": true, "unmonitored-missing": true,
		}},
		"missing": {projection.FilterMissing, map[string]bool{
			"wanted": true, "pending": true, "unavailable": true,
		}},
		"wanted": {projection.FilterWanted, map[string]bool{
			"wanted": true,
		}},
		"cutoff unmet": {projection.FilterCutoffUnmet, map[string]bool{
			"cutoff": true,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, it := range []projection.LibraryItem{imported, cutoff, cutoffUnmonitored, wanted, pending, unavailable, unmonitoredMissing} {
				require.Equal(t, tc.want[it.Ref.Name], tc.filter.Matches(it), "%s under %s", it.Ref.Name, tc.filter)
			}
		})
	}
}

// TestArrangeSortsWithTitleAsTheTiebreak: every sort key orders by that
// key, then by title; desc reverses the key, not the tiebreak; the input
// is never modified.
func TestArrangeSortsWithTitleAsTheTiebreak(t *testing.T) {
	items := []projection.LibraryItem{
		viewItem("Heat", true, true, "CutoffUnmet", 1995, "hd"),
		viewItem("Alien", false, false, "Unmonitored", 1979, "sd"),
		viewItem("Arrival", true, true, "Imported", 2016, "uhd"),
		viewItem("Dune", true, false, "Downloading", 2021, "uhd"),
		viewItem("Brazil", true, false, "Wanted", 1985, "hd"),
	}
	before := names(items)

	for name, tc := range map[string]struct {
		sort projection.LibrarySort
		desc bool
		want []string
	}{
		"title":        {projection.SortTitle, false, []string{"Alien", "Arrival", "Brazil", "Dune", "Heat"}},
		"title desc":   {projection.SortTitle, true, []string{"Heat", "Dune", "Brazil", "Arrival", "Alien"}},
		"year":         {projection.SortYear, false, []string{"Alien", "Brazil", "Heat", "Arrival", "Dune"}},
		"year desc":    {projection.SortYear, true, []string{"Dune", "Arrival", "Heat", "Brazil", "Alien"}},
		"profile":      {projection.SortProfile, false, []string{"Brazil", "Heat", "Alien", "Arrival", "Dune"}},
		"profile desc": {projection.SortProfile, true, []string{"Arrival", "Dune", "Alien", "Brazil", "Heat"}},
		// Status: what needs attention first -- missing, downloading,
		// cutoff unmet, on disk, unmonitored.
		"status":      {projection.SortStatus, false, []string{"Brazil", "Dune", "Heat", "Arrival", "Alien"}},
		"status desc": {projection.SortStatus, true, []string{"Alien", "Arrival", "Heat", "Dune", "Brazil"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := projection.Arrange(items, tc.sort, tc.desc, projection.FilterAll)
			require.Equal(t, tc.want, names(got))
			require.Equal(t, before, names(items), "Arrange does not reorder its input")
		})
	}
}

func TestArrangeFiltersBeforeSorting(t *testing.T) {
	items := []projection.LibraryItem{
		viewItem("Heat", true, true, "CutoffUnmet", 1995, "hd"),
		viewItem("Alien", false, false, "Unmonitored", 1979, "sd"),
		viewItem("Dune", true, false, "Downloading", 2021, "uhd"),
		viewItem("Brazil", true, false, "Wanted", 1985, "hd"),
	}
	got := projection.Arrange(items, projection.SortYear, true, projection.FilterMissing)
	require.Equal(t, []string{"Dune", "Brazil"}, names(got))
	require.Empty(t, projection.Arrange(nil, projection.SortTitle, false, projection.FilterAll))
	require.NotNil(t, projection.Arrange(nil, projection.SortTitle, false, projection.FilterAll), "an empty tab is an empty slice, not nil, so the SSE frame renders")
}

// TestJumpLetterFilesTitles: the A-Z bar files a title under its first
// letter, upper-cased, and everything else -- digits, punctuation,
// letters outside A-Z, nothing -- under #.
func TestJumpLetterFilesTitles(t *testing.T) {
	for title, want := range map[string]string{
		"Arrival": "A", "zulu": "Z", "'Round Midnight": "#", "2001: A Space Odyssey": "#", "Éclair": "#", "": "#",
	} {
		require.Equal(t, want, projection.JumpLetter(title), title)
	}
}
