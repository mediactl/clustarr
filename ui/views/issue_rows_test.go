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

// The comic hero's labels: the run's years as Mylar prints them, the kind
// badge from the kind and the manga flag, the publication status, and the
// issues fact as files over the issues listed.
func TestComicViewLabels(t *testing.T) {
	for name, tc := range map[string]struct {
		view  ComicView
		years string
		kind  string
		run   string
	}{
		"continuing western comic": {ComicView{StartYear: 2012, Kind: "comic", Manga: "no", RunStatus: "continuing"}, "2012–", "Comic", "Continuing"},
		"ended run":                {ComicView{StartYear: 2012, EndYear: 2018, Kind: "comic", RunStatus: "ended"}, "2012–2018", "Comic", "Ended"},
		"one-year run":             {ComicView{StartYear: 2015, EndYear: 2015, Kind: "comic"}, "2015", "Comic", ""},
		"manga by flag":            {ComicView{StartYear: 1997, Kind: "comic", Manga: "yesAndRightToLeft"}, "1997–", "Manga, right to left", ""},
		"manga by kind":            {ComicView{Kind: "manga", Manga: "unknown"}, "", "Manga", ""},
		"manga by flag alone":      {ComicView{Kind: "comic", Manga: "yes"}, "", "Manga", ""},
	} {
		assert.Equal(t, tc.years, tc.view.YearsLabel(), name)
		assert.Equal(t, tc.kind, tc.view.KindLabel(), name)
		assert.Equal(t, tc.run, tc.view.RunStatusLabel(), name)
	}

	v := ComicView{IssueFiles: 3, IssueCount: 72, Issues: make([]IssueRow, 10)}
	assert.Equal(t, "3 / 72", v.IssuesLabel(), "the provider's count")
	v.IssueCount = 0
	assert.Equal(t, "3 / 10", v.IssuesLabel(), "the issues listed when the provider gives no count")
	assert.Equal(t, "72 issues", issuesWord(72))
	assert.Equal(t, "1 issue", issuesWord(1))
}

// An issue row's routes hang off its page, and a form returns to the
// comic's; its label is Mylar's "#12" and an untitled issue shows it.
func TestIssueRowRoutesAndLabels(t *testing.T) {
	r := IssueRow{Namespace: "default", Name: "saga-012.1-hu-abc", Comic: "saga", Number: "12.HU"}
	assert.Equal(t, "#12.HU", r.Label())
	assert.Equal(t, "#12.HU", r.DisplayTitle())
	r.Title = "Hastings variant"
	assert.Equal(t, "Hastings variant", r.DisplayTitle())
	assert.Equal(t, "/library/default/issue/saga-012.1-hu-abc", r.URL())
	assert.Equal(t, "/library/default/issue/saga-012.1-hu-abc/monitor", r.MonitorURL())
	assert.Equal(t, "/library/default/issue/saga-012.1-hu-abc/search", r.SearchURL())
	assert.Equal(t, "/library/default/issue/saga-012.1-hu-abc/search/interactive", r.InteractiveSearchURL())
	assert.Equal(t, "/library/default/issue/saga-012.1-hu-abc/details", r.DetailsURL())
	assert.Equal(t, "/library/default/comic/saga", r.ReturnURL())
	assert.Equal(t, "/library/default/comic/saga/volume/search", ComicView{Detail: Detail{Item: projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "saga"}, Kind: commonv1.MediaKindComic,
	}}}.VolumeSearchURL())

	p := IssueSearchPanel{SearchPanel: SearchPanel{Namespace: "default", Name: "saga-004.0-abc12"}, Issue: "saga-004.0"}
	assert.Equal(t, "/library/default/issue/saga-004.0/search/saga-004.0-abc12", p.PanelURL())
	assert.Equal(t, "/library/default/issue/saga-004.0/search/saga-004.0-abc12/grab", p.GrabURL(), "the issue's own grab route, not the shared one")

	assert.Equal(t, "Brian K. Vaughan (writer)", IssueCredit{Name: "Brian K. Vaughan", Role: "writer"}.String())
	assert.Equal(t, "Fiona Staples", IssueCredit{Name: "Fiona Staples"}.String())
}

// The issue page's Status fact says a file's state in the movie page's
// words, the cutoff reason named when the catalog recorded one.
func TestIssueStatusLabel(t *testing.T) {
	file := RowStatus{Kind: "file", Label: "CBZ"}
	warn := RowStatus{Kind: "file", Label: "CBR", Warn: true}
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
	} {
		assert.Equal(t, tc.want, issueStatusLabel(IssueView{Row: IssueRow{Status: tc.status}, CutoffReason: tc.reason}), name)
	}
	assert.Equal(t, "In shops from Tomorrow", shopsLabel(IssueRow{StoreDateLabel: "Tomorrow", Status: RowStatus{Kind: "unreleased"}}))
	assert.Equal(t, "In shops Yesterday", shopsLabel(IssueRow{StoreDateLabel: "Yesterday", Status: RowStatus{Kind: "missing"}}))
}
