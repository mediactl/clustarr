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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

func movie(title string, year int32, monitored bool, providerID string) projection.LibraryItem {
	return projection.LibraryItem{
		Ref:  types.NamespacedName{Namespace: "media", Name: fmt.Sprintf("%s-%d", title, year)},
		Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies,
		Title: title, Year: year, Monitored: monitored, ProviderID: providerID,
	}
}

func titles(items []projection.LibraryItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

func TestFindRanksExactThenPrefixThenWordThenAnywhere(t *testing.T) {
	items := []projection.LibraryItem{
		movie("The Last Blade", 2001, true, "1"),
		movie("Bladerunner Remix", 2010, true, "2"),
		movie("Blade Runner 2049", 2017, true, "3"),
		movie("Blade", 1998, true, "4"),
		movie("Switchblade Romance", 2003, true, "5"),
		movie("Blade Runner", 1982, true, "6"),
	}
	assert.Equal(t, []string{
		"Blade",               // exact
		"Blade Runner",        // title prefix, shorter first
		"Blade Runner 2049",   // title prefix
		"Bladerunner Remix",   // title prefix
		"The Last Blade",      // a later word starts with it ("the" is dropped, "last blade")
		"Switchblade Romance", // anywhere
	}, titles(projection.Find(items, "blade", 10)))
}

func TestFindFoldsCaseAccentsPunctuationAndArticles(t *testing.T) {
	items := []projection.LibraryItem{
		movie("Amélie", 2001, true, "194"),
		movie("Spider-Man: No Way Home", 2021, true, "634649"),
		movie("The Matrix", 1999, true, "603"),
		movie("Матрица", 1999, true, "999"),
	}
	assert.Equal(t, []string{"Amélie"}, titles(projection.Find(items, "AMELIE", 10)))
	assert.Equal(t, []string{"Spider-Man: No Way Home"}, titles(projection.Find(items, "spiderman", 10)))
	assert.Equal(t, []string{"Spider-Man: No Way Home"}, titles(projection.Find(items, "spider-man no", 10)))
	assert.Equal(t, []string{"The Matrix"}, titles(projection.Find(items, "the matrix", 10)), "the article is dropped on both sides")
	assert.Equal(t, []string{"Матрица"}, titles(projection.Find(items, "матр", 10)), "a non-Latin title is found in its own script")
}

func TestFindMatchesAProviderID(t *testing.T) {
	items := []projection.LibraryItem{
		movie("The Matrix", 1999, true, "603"),
		movie("Matrix Revolutions 603", 2003, true, "605"),
	}
	assert.Equal(t, []string{"The Matrix"}, titles(projection.Find(items, "tmdb:603", 10)), "a prefixed id finds only that item")
	assert.Equal(t, []string{"The Matrix", "Matrix Revolutions 603"}, titles(projection.Find(items, "603", 10)),
		"a bare id ranks its item first, then titles that contain the text")
}

func TestFindKeepsOnlyMonitoredItems(t *testing.T) {
	items := []projection.LibraryItem{
		movie("Heat", 1995, false, "949"),
		movie("Heathers", 1988, true, "2640"),
	}
	assert.Equal(t, []string{"Heathers"}, titles(projection.Find(items, "heat", 10)))
	assert.Empty(t, projection.Find(items, "tmdb:949", 10), "an unmonitored item is not found by its id either")
}

func TestFindCapsTheResults(t *testing.T) {
	var items []projection.LibraryItem
	for i := range 25 {
		items = append(items, movie(fmt.Sprintf("Friday %02d", i), 2000, true, fmt.Sprint(i)))
	}
	got := projection.Find(items, "friday", 10)
	assert.Len(t, got, 10)
	assert.Equal(t, "Friday 00", got[0].Title, "ties go to the shorter, then alphabetical, title")
}

func TestFindAnEmptyOrBlankQueryFindsNothing(t *testing.T) {
	items := []projection.LibraryItem{movie("Up", 2009, true, "14160")}
	assert.Empty(t, projection.Find(items, "", 10))
	assert.Empty(t, projection.Find(items, "   ", 10))
	assert.Empty(t, projection.Find(items, ":-", 10))
}
