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

package views

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// The book page's Status fact says a file's state in the movie page's
// words, with the catalog's cutoff reason when it names one; a book has
// no Transcoded.
func TestBookXStatusLabel(t *testing.T) {
	file := RowStatus{Kind: "file", Label: "EPUB"}
	warn := RowStatus{Kind: "file", Label: "MOBI", Warn: true}
	for name, tc := range map[string]struct {
		status RowStatus
		reason string
		want   string
	}{
		"downloaded":                 {file, "CutoffMet", "Downloaded"},
		"cutoff unmet":               {warn, "CutoffUnmet", "Cutoff Unmet"},
		"cutoff unmet with a reason": {warn, "ProfileUnresolved", "Cutoff Unmet: ProfileUnresolved"},
		"missing":                    {RowStatus{Kind: "missing", Label: "Missing"}, "Pending", "Missing"},
		"unreleased":                 {RowStatus{Kind: "unreleased", Label: "Unreleased"}, "Pending", "Unreleased"},
		"pending grab":               {RowStatus{Kind: "pending", Label: "Pending"}, "Pending", "Pending"},
	} {
		v := BookXView{Row: BookRow{Status: tc.status}, CutoffReason: tc.reason}
		assert.Equal(t, tc.want, bookXStatusLabel(v), name)
	}
}

// The trail names the author between the tab and the book, and nothing
// between them for a standalone book; the title carries the author and the
// subtitle.
func TestBookXCrumbsAndTitle(t *testing.T) {
	item := projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "dispossessed"}, Kind: commonv1.MediaKindBook, Tab: projection.TabBooks, Title: "The Dispossessed"}
	v := BookXView{Detail: Detail{Item: item}, Row: BookRow{Name: "dispossessed", Title: "The Dispossessed", Subtitle: "An Ambiguous Utopia"},
		AuthorTitle: "Ursula K. Le Guin", AuthorURL: "/library/default/author/le-guin"}
	assert.Equal(t, []Crumb{
		{Label: "Library", Href: "/library"}, {Label: "Books", Href: "/library/books"},
		{Label: "Ursula K. Le Guin", Href: "/library/default/author/le-guin"}, {Label: "The Dispossessed"},
	}, bookXCrumbs(v))
	assert.Equal(t, "Ursula K. Le Guin - The Dispossessed: An Ambiguous Utopia", bookXPageTitle(v))

	v.Standalone, v.AuthorTitle, v.AuthorURL = true, "", ""
	assert.Equal(t, itemCrumbs(item), bookXCrumbs(v))
	assert.Equal(t, "The Dispossessed: An Ambiguous Utopia", bookXPageTitle(v))

	assert.Equal(t, "Releases Tomorrow", bookXReleasedLabel(BookRow{ReleaseDateLabel: "Tomorrow", Status: RowStatus{Kind: "unreleased"}}))
	assert.Equal(t, "Released May 1 1974", bookXReleasedLabel(BookRow{ReleaseDateLabel: "May 1 1974", Status: RowStatus{Kind: "file"}}))
	assert.Equal(t, "https://openlibrary.org/books/OL7353617M", BookXEdition{ID: "OL7353617M"}.URL())
}
