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

import "fmt"

// SeasonRow is one season component on a series page (spec
// 2026-09-23-library-page-design): the rollup's counts and the season's
// effective monitored flag, plus the URLs its lazy load and its own page
// use. ui builds it from the Series (ui/series.go); the view only renders.
type SeasonRow struct {
	Series    string // the series' name, for the toggle's target
	Namespace string
	Number    int32
	Monitored bool
	Episodes  int32
	Files     int32
	// SizeBytes is the season's files' size on disk.
	SizeBytes int64
	// NextAiring is the next air date as YYYY-MM-DD, or "".
	NextAiring string
	// Error is set on a component rendered as the reply to a toggle that
	// failed: the stable code (data-action-error) and the detail.
	Error ActionFailure
}

// ActionFailure is an action's failure rendered inside the component it
// was meant to change, so the page stays usable and the failure is visible
// where it happened. Code is the stable reason ("no-writer", "invalid",
// "failed"); Message the detail. A zero value is no failure.
type ActionFailure struct {
	Code    string
	Message string
}

// MonitorURL is the season's monitor action.
func (r SeasonRow) MonitorURL() string { return r.URL() + "/monitor" }

// URL is the season's own route, which serves the episodes component to
// htmx and a page to anyone else.
func (r SeasonRow) URL() string {
	return fmt.Sprintf("/library/%s/series/%s/seasons/%d", r.Namespace, r.Series, r.Number)
}

// CountLabel is the season's badge, Sonarr's "files / episodes".
func (r SeasonRow) CountLabel() string { return fmt.Sprintf("%d / %d", r.Files, r.Episodes) }

// CountTone colours the badge as Sonarr does: green when every episode
// has its file, amber for an unmonitored season, red when a monitored one
// is missing some.
func (r SeasonRow) CountTone() string {
	switch {
	case r.Episodes > 0 && r.Files >= r.Episodes:
		return "bg-emerald-500/20 text-emerald-300"
	case !r.Monitored:
		return "bg-amber-500/20 text-amber-300"
	default:
		return "bg-red-500/20 text-red-300"
	}
}

// Label is the season's heading; season 0 is the specials.
func (r SeasonRow) Label() string {
	if r.Number == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", r.Number)
}

// EpisodeRow is one episode on a season's episodes table, Sonarr's
// episode row: the monitor bookmark, number, title, air date, status and
// the two searches.
type EpisodeRow struct {
	Namespace string
	Name      string
	// Series is the series' name, for the season page a form returns to.
	Series string
	Season int32
	Number int32
	Title  string
	// FinaleType is TVDB's finale label ("season", "series",
	// "midseason"), or "".
	FinaleType string
	// AirDate is YYYY-MM-DD, or "" when unknown; AirDateLabel is how the
	// table shows it (Sonarr's relative date: Today, Yesterday, Tomorrow,
	// else "Sep 21 2022").
	AirDate      string
	AirDateLabel string
	Monitored    bool
	HasFile      bool
	// Quality is the file's quality name, "" without a file.
	Quality string
	Phase   string
	// Status is the status cell (ui.episodeStatus).
	Status RowStatus
	// Audio is the episode's status.audio as a note (ui.audioNote).
	Audio AudioNote
	// Error is set on a row rendered as the reply to a toggle that failed.
	Error ActionFailure
}

// RowStatus is Sonarr's status cell (EpisodeStatus.tsx) on an episode's
// row and Readarr's on a book's: the first that applies of downloading, a
// pending grab, the file's quality, no air date yet (TBA), not aired or
// released, unmonitored, else missing.
type RowStatus struct {
	// Kind is the case: "downloading", "pending", "file", "tba",
	// "unaired", "unreleased", "unmonitored" or "missing".
	Kind string
	// Label is the cell's text; Title its tooltip.
	Label string
	Title string
	// Warn tones a file whose quality is below the profile's cutoff.
	Warn bool
}

// Code is the episode's Sonarr number, "1x03".
func (r EpisodeRow) Code() string { return fmt.Sprintf("%dx%02d", r.Season, r.Number) }

// FinaleLabel is the finale badge's text, "" for no finale.
func (r EpisodeRow) FinaleLabel() string {
	switch r.FinaleType {
	case "season":
		return "Season Finale"
	case "series":
		return "Series Finale"
	case "midseason":
		return "Midseason Finale"
	}
	return ""
}

// URL is the episode's base route.
func (r EpisodeRow) URL() string { return fmt.Sprintf("/library/%s/episode/%s", r.Namespace, r.Name) }

// MonitorURL is the episode's monitor action, the existing per-item route.
func (r EpisodeRow) MonitorURL() string { return r.URL() + "/monitor" }

// SearchURL is the episode's automatic search, the per-item "search now".
func (r EpisodeRow) SearchURL() string { return r.URL() + "/search" }

// InteractiveSearchURL starts an interactive search for the episode.
func (r EpisodeRow) InteractiveSearchURL() string { return r.URL() + "/search/interactive" }

// ReturnURL is the season page, where a form post from the row returns.
func (r EpisodeRow) ReturnURL() string {
	return fmt.Sprintf("/library/%s/series/%s/seasons/%d", r.Namespace, r.Series, r.Season)
}

// DetailsURL is the episode's details: the modal to htmx, a page otherwise.
func (r EpisodeRow) DetailsURL() string { return r.URL() + "/details" }

// EpisodeDetail is Sonarr's episode details modal: the episode's summary
// and file on one tab, its searches on the other.
type EpisodeDetail struct {
	Row         EpisodeRow
	SeriesTitle string
	// SeriesURL is the series' page.
	SeriesURL string
	Overview  string
	Runtime   string
	// File is the episode's file, nil without one.
	File *FileRow
	// Tab is the open tab, "details" or "search".
	Tab string
	// Search is the interactive search shown on the search tab, nil until
	// one is run.
	Search *SearchPanel
}

// SearchPanel is an interactive search's results (Sonarr's
// InteractiveSearch): the ranked releases with the decision engine's
// verdicts and each one's download button. The panel polls itself while
// Poll is set.
type SearchPanel struct {
	// Namespace and Name are the Search's.
	Namespace string
	Name      string
	Phase     string
	// Gone is set when the Search no longer exists (its TTL expired).
	Gone bool
	// Failed is the reason a failed Search gives.
	Failed string
	// Indexers counts the indexers asked; IndexerErrors those that failed
	// or timed out.
	Indexers      int
	IndexerErrors int
	Results       []ReleaseRow
	// Poll is whether the panel asks again: the results, or a requested
	// grab's outcome, are not in yet.
	Poll bool
	// Error is a failed grab's, rendered above the table.
	Error ActionFailure
}

// Pending is whether the Search has not answered yet.
func (p SearchPanel) Pending() bool {
	return !p.Gone && p.Phase != "Completed" && p.Phase != "Failed"
}

// URL is the panel's own route: the panel to htmx, a page otherwise.
func (p SearchPanel) URL() string { return fmt.Sprintf("/searches/%s/%s", p.Namespace, p.Name) }

// GrabURL is the download button's action.
func (p SearchPanel) GrabURL() string { return p.URL() + "/grab" }

// ReleaseRow is one interactive search result, Sonarr's columns: source,
// age, title, indexer, size, peers, languages, quality, score, rejections
// and the download button.
type ReleaseRow struct {
	GUID     string
	Protocol string
	// Age is "3 days", "5 hours" or "12 minutes"; "" when unpublished.
	Age     string
	Title   string
	InfoURL string
	Indexer string
	Size    int64
	// Peers is "seeders/leechers" for a torrent, "" otherwise.
	Peers     string
	Languages []string
	Quality   string
	Score     int32
	Formats   []string
	Approved  bool
	// Rejections are the decision engine's reasons, each "reason" with
	// "(temporary)" for one that may pass later.
	Rejections []string
	// Override is whether grabbing it needs spec.override: a permanent
	// rejection. The button then asks first.
	Override bool
	// Grab is "" (not requested), "requested", "grabbed" or "failed";
	// GrabError the failure.
	Grab      string
	GrabError string
}

// AudioNote is an item's audio languages as one short note: the dub it
// lacks and what the graft is doing (anime dual-audio spec §9), with a
// failure's reason as Title. Warn tones it as a problem.
type AudioNote struct {
	Text  string
	Title string
	Warn  bool
}

// ChildRow is one album on an artist's children component: the episode
// row's old shape for a kind with a title and a year instead of a number.
// An author's books are [BookRow]s, the season table's shape.
type ChildRow struct {
	Namespace string
	Name      string
	Kind      string // "album", the per-item action's kind
	Title     string
	Year      int32
	Monitored bool
	HasFile   bool
	// Quality is the album's quality name, "" without a file.
	Quality string
	Phase   string
	Error   ActionFailure
}

// MonitorURL is the child's monitor action, the existing per-item route.
func (r ChildRow) MonitorURL() string {
	return fmt.Sprintf("/library/%s/%s/%s/monitor", r.Namespace, r.Kind, r.Name)
}

// ChildrenURL is a parent's children route: the children component to
// htmx, a page to anyone else.
func ChildrenURL(namespace, kind, name string) string {
	return fmt.Sprintf("/library/%s/%s/%s/children", namespace, kind, name)
}
