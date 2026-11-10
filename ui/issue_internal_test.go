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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/ui/views"
)

// issueStatus is the episode's status cell for an issue (2026-11-10 design
// §9.4), in the episode's order: a grab in flight, a pending grab, the
// file's format (warned below the cutoff), no date (TBA), not out yet,
// unmonitored, else missing. The store date is the release date when the
// provider has one, else the cover date.
func TestIssueStatusIsTheEpisodes(t *testing.T) {
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.AddDate(0, -1, 0))
	future := metav1.NewTime(now.AddDate(0, 1, 0))
	for name, tc := range map[string]struct {
		status    catalogv1.IssueStatus
		monitored bool
		kind      string
		label     string
		warn      bool
	}{
		"downloading by phase":    {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseDownloading, HasFile: true}, true, "downloading", "Downloading", false},
		"downloading by entry":    {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted, ActiveDownloadRef: new("dl")}, true, "downloading", "Downloading", false},
		"pending by phase":        {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseDelayed}, true, "pending", "Pending", false},
		"pending by grab":         {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted, PendingGrab: &catalogv1.PendingGrab{}}, true, "pending", "Pending", false},
		"file at the cutoff":      {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseImported, HasFile: true, FileFormat: "CBZ"}, true, "file", "CBZ", false},
		"file below the cutoff":   {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseCutoffUnmet, HasFile: true, FileFormat: "CBR"}, true, "file", "CBR", true},
		"file unjudged":           {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseCutoffUnevaluated, HasFile: true, FileQuality: &commonv1.Quality{Name: "PDF"}}, true, "file", "PDF", true},
		"file of unknown format":  {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseImported, HasFile: true}, true, "file", "Unknown", false},
		"file, unmonitored":       {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseUnmonitored, HasFile: true, FileFormat: "CBZ"}, false, "file", "CBZ", false},
		"no date":                 {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted}, true, "tba", "TBA", false},
		"unavailable by phase":    {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseUnavailable, Date: &past}, true, "unreleased", "Unreleased", false},
		"store date to come":      {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted, Date: &past, StoreDate: &future}, true, "unreleased", "Unreleased", false},
		"cover date to come":      {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted, Date: &future}, true, "unreleased", "Unreleased", false},
		"out, unmonitored":        {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseUnmonitored, Date: &past}, false, "unmonitored", "Unmonitored", false},
		"out, missing":            {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted, Date: &past}, true, "missing", "Missing", false},
		"store date out, missing": {catalogv1.IssueStatus{Phase: catalogv1.IssuePhaseWanted, Date: &future, StoreDate: &past}, true, "missing", "Missing", false},
	} {
		iss := &catalogv1.Issue{Status: tc.status}
		got := issueStatus(iss, tc.monitored, now)
		assert.Equal(t, tc.kind, got.Kind, name)
		assert.Equal(t, tc.label, got.Label, name)
		assert.Equal(t, tc.warn, got.Warn, name)
		assert.NotEmpty(t, got.Title, name)
	}
}

// The volume number the comic page reads status.volumeSearch by is the
// season planner's (app/catalog/searchplan.VolumeNumber), which the ui
// does not import.
func TestComicVolumeNumberIsTheSearchPlanners(t *testing.T) {
	require.Equal(t, searchplan.VolumeNumber, comicVolumeNumber)
}

// comicVolumeBadge is seasonSearchBadge over the one volume: Queued,
// Searching, "Searched: N approved" with its age; nothing for no state, a
// final phase that is not Answered, or another season's number.
func TestComicVolumeBadge(t *testing.T) {
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	answeredAt := metav1.NewTime(now.Add(-2 * time.Hour))
	state := func(seasons ...catalogv1.SeasonSearchSeason) *catalogv1.SeasonSearchState {
		return &catalogv1.SeasonSearchState{Seasons: seasons}
	}
	for name, tc := range map[string]struct {
		state *catalogv1.SeasonSearchState
		want  views.SeasonSearchBadge
	}{
		"no state":  {nil, views.SeasonSearchBadge{}},
		"queued":    {state(catalogv1.SeasonSearchSeason{Number: 1, Phase: catalogv1.SeasonSearchQueued}), views.SeasonSearchBadge{Phase: "Queued", Label: "Queued", Title: "The volume waits its turn to be searched"}},
		"searching": {state(catalogv1.SeasonSearchSeason{Number: 1, Phase: catalogv1.SeasonSearchSearching}), views.SeasonSearchBadge{Phase: "Searching", Label: "Searching", Title: "The volume is being searched for a pack"}},
		"answered": {
			state(catalogv1.SeasonSearchSeason{Number: 1, Phase: catalogv1.SeasonSearchAnswered, AnsweredAt: &answeredAt, Candidates: 12, Approved: 3}),
			views.SeasonSearchBadge{Phase: "Answered", Label: "Searched: 3 approved", Title: "12 releases, 3 approved, 2 hours ago"},
		},
		"skipped":        {state(catalogv1.SeasonSearchSeason{Number: 1, Phase: catalogv1.SeasonSearchSkipped, Reason: catalogv1.SeasonSearchNothingWanted}), views.SeasonSearchBadge{}},
		"another season": {state(catalogv1.SeasonSearchSeason{Number: 2, Phase: catalogv1.SeasonSearchSearching}), views.SeasonSearchBadge{}},
	} {
		assert.Equal(t, tc.want, comicVolumeBadge(tc.state, now), name)
	}
}

// comicLinks are the comic's provider pages: the source's own page from
// spec.source and spec.sourceID (ComicVine's guid form, a bare id
// prefixed), the externalIDs' others, and no Metron link, whose site is
// addressed by slug.
func TestComicLinksFollowTheSourceAndTheExternalIDs(t *testing.T) {
	comicvine := &catalogv1.Comic{Spec: catalogv1.ComicSpec{Source: catalogv1.ComicSourceComicVine, SourceID: "56582"}}
	comicvine.Status.Metadata = &catalogv1.ComicMetadata{ExternalIDs: map[string]string{"metron": "12", "anilist": "98765", "mal": "4"}}
	links := comicLinks(comicvine)
	require.Len(t, links, 3)
	assert.Equal(t, views.Link{Label: "ComicVine", Href: "https://comicvine.gamespot.com/volume/4050-56582/", Provider: views.ProviderComicVine}, links[0])
	assert.Equal(t, views.Link{Label: "AniList", Href: "https://anilist.co/manga/98765", Provider: views.ProviderAniList}, links[1])
	assert.Equal(t, views.Link{Label: "MyAnimeList", Href: "https://myanimelist.net/manga/4", Provider: views.ProviderMAL}, links[2])

	mangadex := &catalogv1.Comic{Spec: catalogv1.ComicSpec{Source: catalogv1.ComicSourceMangaDex, SourceID: "a1b2c3"}}
	links = comicLinks(mangadex)
	require.Len(t, links, 1, "no metadata yet: the source alone")
	assert.Equal(t, "https://mangadex.org/title/a1b2c3", links[0].Href)
	assert.Equal(t, views.ProviderMangaDex, links[0].Provider)

	guid := &catalogv1.Comic{Spec: catalogv1.ComicSpec{Source: catalogv1.ComicSourceComicVine, SourceID: "4050-18257"}}
	assert.Equal(t, "https://comicvine.gamespot.com/volume/4050-18257/", comicLinks(guid)[0].Href, "a guid is kept")

	// An issue's links: the ComicVine issue page by its sourceIDs' id, else
	// a ComicVine comic's source id; a MangaDex chapter likewise.
	iss := &catalogv1.Issue{Status: catalogv1.IssueStatus{SourceID: "4000-1", SourceIDs: map[string]string{"comicvine": "4000-2"}}}
	assert.Equal(t, "https://comicvine.gamespot.com/issue/4000-2/", issueLinks(iss, comicvine)[0].Href, "sourceIDs win")
	iss = &catalogv1.Issue{Status: catalogv1.IssueStatus{SourceID: "99"}}
	assert.Equal(t, "https://comicvine.gamespot.com/issue/4000-99/", issueLinks(iss, comicvine)[0].Href, "the source id, prefixed")
	assert.Equal(t, "https://mangadex.org/chapter/99", issueLinks(iss, mangadex)[0].Href)
	assert.Empty(t, issueLinks(&catalogv1.Issue{}, &catalogv1.Comic{}), "nothing to link")
	assert.Equal(t, []views.Link{{Label: "ComicVine (comic)", Href: links[0].Href, Provider: links[0].Provider}},
		captionedLinks([]views.Link{{Label: "ComicVine", Href: links[0].Href, Provider: links[0].Provider}}, "comic"))
}

// issueFormat is the comic ladder's name of the file (status.fileFormat),
// else the frozen quality's, else nothing; a file-less issue has none
// whatever the fields say.
func TestIssueFormatPrefersTheLadderName(t *testing.T) {
	assert.Equal(t, "CBZ", issueFormat(&catalogv1.Issue{Status: catalogv1.IssueStatus{HasFile: true, FileFormat: "CBZ", FileQuality: &commonv1.Quality{Name: "CBR"}}}))
	assert.Equal(t, "CBR", issueFormat(&catalogv1.Issue{Status: catalogv1.IssueStatus{HasFile: true, FileQuality: &commonv1.Quality{Name: "CBR"}}}))
	assert.Equal(t, "", issueFormat(&catalogv1.Issue{Status: catalogv1.IssueStatus{HasFile: true}}))
	assert.Equal(t, "", issueFormat(&catalogv1.Issue{Status: catalogv1.IssueStatus{FileFormat: "CBZ"}}), "no file, no format")
}

// sortIssues is Mylar's order: by the calculated number, then by the number
// as printed.
func TestSortIssuesIsByCalculatedNumber(t *testing.T) {
	iss := func(number string, centis int32) catalogv1.Issue {
		return catalogv1.Issue{Spec: catalogv1.IssueSpec{Number: number, CalculatedNumberCentis: centis}}
	}
	issues := []catalogv1.Issue{iss("12.HU", 1210), iss("Annual 1", 100000), iss("2", 200), iss("12", 1200), iss("1", 100), iss("12a", 1200)}
	sortIssues(issues)
	var numbers []string
	for _, i := range issues {
		numbers = append(numbers, i.Spec.Number)
	}
	assert.Equal(t, []string{"1", "2", "12", "12a", "12.HU", "Annual 1"}, numbers)
}
