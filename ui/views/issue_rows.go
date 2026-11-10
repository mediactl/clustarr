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
	"fmt"
	"strings"
)

// A comic's issues are Mylar's issues table (2026-11-10 design §9.4): each
// row has the monitor bookmark, the issue number, the title (opening the
// issue's page), the cover and store dates, the status cell -- the
// episode's cases over the Issue's phase, cutoff reason, file and grabs --
// and the two searches, kind "issue", whose modal opens in the page's
// #issue-modal. ui builds every row from the Issue (ui/issue.go); the view
// only renders.

// IssueRow is one issue on a comic's issues table.
type IssueRow struct {
	Namespace string
	Name      string
	// Comic is the comic's name, for the comic's page a form returns to.
	Comic string
	// Number is the issue number as the provider prints it ("12", "12.HU",
	// "Annual 1"); NumberCentis its sortable form (spec.calculatedNumberCentis).
	Number       string
	NumberCentis int32
	Title        string
	// CoverDate and StoreDate are YYYY-MM-DD, or "" when unknown; their
	// labels are how the table shows them (ui.relativeDate).
	CoverDate      string
	CoverDateLabel string
	StoreDate      string
	StoreDateLabel string
	Monitored      bool
	HasFile        bool
	// Format is the file's format as the comic ladder names it (CBZ, CBR,
	// PDF), "" without a file.
	Format string
	Phase  string
	// Status is the status cell (ui.issueStatus).
	Status RowStatus
	// Error is set on a row rendered as the reply to a toggle that failed.
	Error ActionFailure
}

// Label is the issue's number as Mylar prints it, "#12".
func (r IssueRow) Label() string { return "#" + r.Number }

// DisplayTitle is the issue's title, or its number before the provider
// names it ("#12").
func (r IssueRow) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Label()
}

// URL is the issue's page, and the base of its actions' routes.
func (r IssueRow) URL() string { return fmt.Sprintf("/library/%s/issue/%s", r.Namespace, r.Name) }

// MonitorURL is the issue's monitor action, the existing per-item route.
func (r IssueRow) MonitorURL() string { return r.URL() + "/monitor" }

// SearchURL is the issue's automatic search, the per-item "search now".
func (r IssueRow) SearchURL() string { return r.URL() + "/search" }

// InteractiveSearchURL starts an interactive search for the issue.
func (r IssueRow) InteractiveSearchURL() string { return r.URL() + "/search/interactive" }

// DetailsURL is the issue's details: the modal to htmx, the page otherwise.
func (r IssueRow) DetailsURL() string { return r.URL() + "/details" }

// ReturnURL is the comic's page, where a form post from the row returns.
func (r IssueRow) ReturnURL() string { return ComicURL(r.Namespace, r.Comic) }

// ComicURL is a comic's page.
func ComicURL(namespace, comic string) string {
	return fmt.Sprintf("/library/%s/comic/%s", namespace, comic)
}

// IssueCredit is one person credited on an issue (status.credits): the
// name and the role as the provider names it.
type IssueCredit struct {
	Name string
	Role string
}

// String is the credit as the page prints it: "Brian K. Vaughan (writer)",
// or the name alone without a role.
func (c IssueCredit) String() string {
	if c.Role == "" {
		return c.Name
	}
	return c.Name + " (" + c.Role + ")"
}

// IssueDetail is an issue's details modal, the episode modal's
// counterpart: the issue's overview, credits and file on one tab, its
// searches on the other.
type IssueDetail struct {
	Row IssueRow
	// ComicTitle is the comic's title as the library shows it; ComicURL its
	// page.
	ComicTitle string
	ComicURL   string
	Overview   string
	Credits    []IssueCredit
	// File is the issue's file, nil without one.
	File *FileRow
	// Tab is the open tab, "details" or "search".
	Tab string
	// Search is the interactive search shown on the search tab, nil until
	// one is run: the shared panel, which draws comic columns for an
	// issue's Search and polls and grabs through the shared /searches
	// routes.
	Search *SearchPanel
}

// ComicView is everything a comic's page shows beyond its library card
// (design §9.4): the hero's facts, the issues table and the volume
// search's badge.
type ComicView struct {
	// Detail is what the hero's shared pieces read: Item is the comic's
	// library card (poster, art key, quality profile, tab), Backdrop its
	// background when a provider has one, Path, Overview, Genres, Language
	// and Links its metadata's, Prev and Next the neighbouring comics;
	// CanScan, Profiles and the dialogs' inputs are the action bar's.
	Detail
	Publisher string
	// StartYear and EndYear are the run's years; EndYear 0 while it
	// continues.
	StartYear int32
	EndYear   int32
	// VolumeNumber is the volume's number within its title, 0 when
	// unnumbered; IssueCount the provider's count of issues; IssueFiles how
	// many have a file (status.issueFileCount).
	VolumeNumber int32
	IssueCount   int32
	IssueFiles   int32
	// Kind is spec.kind, "comic" or "manga"; Manga status.metadata.manga
	// (unknown, no, yes, yesAndRightToLeft).
	Kind  string
	Manga string
	// RunStatus is status.metadata.status: continuing or ended.
	RunStatus string
	AgeRating string
	// NextPull is status.nextPullDate as YYYY-MM-DD, "" when none.
	NextPull string
	// Issues are the comic's issues in number order.
	Issues []IssueRow
	// Volume is the volume search's badge, from status.volumeSearch
	// (ui.comicVolumeBadge), as a season header shows its season's.
	Volume SeasonSearchBadge
}

// VolumeSearchURL is the action bar's "Search Volume": the pack search of
// every missing issue.
func (v ComicView) VolumeSearchURL() string {
	return ComicURL(v.Item.Ref.Namespace, v.Item.Ref.Name) + "/volume/search"
}

// YearsLabel is the run's years as Mylar prints them: "2012–2018", "2012–"
// for a run that continues, or "" before the metadata arrives.
func (v ComicView) YearsLabel() string {
	switch {
	case v.StartYear <= 0:
		return ""
	case v.EndYear > 0 && v.EndYear != v.StartYear:
		return fmt.Sprintf("%d–%d", v.StartYear, v.EndYear)
	case v.EndYear > 0:
		return fmt.Sprint(v.StartYear)
	default:
		return fmt.Sprintf("%d–", v.StartYear)
	}
}

// KindLabel is the kind badge: "Manga, right to left" when the metadata
// says so, "Manga" for a manga kind or flag, else "Comic".
func (v ComicView) KindLabel() string {
	switch {
	case v.Manga == "yesAndRightToLeft":
		return "Manga, right to left"
	case v.Manga == "yes" || v.Kind == "manga":
		return "Manga"
	default:
		return "Comic"
	}
}

// RunStatusLabel is the publication fact: "Continuing", "Ended", or ""
// when the provider says nothing.
func (v ComicView) RunStatusLabel() string {
	if v.RunStatus == "" {
		return ""
	}
	return strings.ToUpper(v.RunStatus[:1]) + v.RunStatus[1:]
}

// IssuesLabel is the issues fact, Sonarr's "files / count": the issues
// with a file over the issues the comic lists.
func (v ComicView) IssuesLabel() string {
	return fmt.Sprintf("%d / %d", v.IssueFiles, max(v.IssueCount, int32(len(v.Issues)))) //nolint:gosec // a count
}

// IssueView is everything an issue's page shows (design §9.4): the movie
// page for one issue.
type IssueView struct {
	// Detail is what the movie page's sections read: Item is the comic's
	// library card (poster, art key, quality profile, tab), Backdrop its
	// background, Genres and Language its metadata's; Overview, Path, Links
	// and Files are the issue's own, Prev and Next the neighbouring issues.
	Detail
	Row IssueRow
	// Cover is the issue's cover through /art/search, "" without one on a
	// host the proxy fetches from; the comic's poster stands in.
	Cover string
	// Publisher is the comic's publisher.
	Publisher string
	Credits   []IssueCredit
	// CutoffReason is status.cutoffReason, said beside a file below the
	// cutoff.
	CutoffReason string
	// ComicLinks are the comic's provider pages, shown after the issue's
	// own (Links) under a "Comic" caption, since both may carry one logo.
	ComicLinks []Link
}
