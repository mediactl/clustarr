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

// An audiobook's page (2026-11-10 design §9.5) is the movie page for one
// recording: the action bar, a hero on its cover -- title, subtitle,
// authors, narrators, series, runtime, publisher, release date, abridged,
// language and the provider logos -- the facts, the Parts table, the
// Chapters collapsible and the Files section. Its details modal (the
// book's shape: Details and Search tabs) is what a row on a future
// Author page opens, and what its interactive search answers.

// AudiobookView is everything an audiobook's page and its modal show. The
// ui package fills it from the Audiobook, its library card, its Book when
// bookRef names one and its parts' MediaFiles; nothing here is fetched.
type AudiobookView struct {
	// Detail is what the shared sections read: Item is the audiobook's
	// library card (cover, art key, quality profile, tab), Backdrop its
	// fanart, Path its folder, Links its provider pages, Overview and
	// Genres its metadata's, Files its parts as Files rows, Prev and Next
	// the neighbouring audiobooks on the tab, Profiles the Edit dialog's
	// choices.
	Detail
	Subtitle string
	// Authors are the credited authors' names; AuthorHref is the Author's
	// page when spec.authorRef names one, else "" and the names stand
	// alone.
	Authors    []string
	AuthorHref string
	Narrators  []string
	// Series is the reading order and position, "Earthsea #1", or "".
	Series string
	// Runtime is "11h 5m": the metadata's runtime, else the parts'
	// durations together; "" without either. RuntimeMinutes is the same
	// in minutes, which the search panel estimates a release's bitrate
	// from.
	Runtime        string
	RuntimeMinutes int32
	Publisher      string
	// ReleaseDate is YYYY-MM-DD, or "" when unknown; ReleaseDateLabel is
	// how the hero says it (ui.relativeDate).
	ReleaseDate      string
	ReleaseDateLabel string
	// HasMetadata is whether status.metadata has arrived; the abridged
	// badge shows only then, since the zero value reads as unabridged.
	HasMetadata bool
	Abridged    bool
	// Language is the recording's language by name, "" when unknown.
	Language string
	// Status is the status cell's case (ui.audiobookStatus), the Status
	// fact's stripe and, with CutoffReason and Phase, its label.
	Status       RowStatus
	CutoffReason string
	Phase        string
	// Quality is the recording's frozen quality (status.fileQuality), the
	// lowest-ranked part's; "" without a file.
	Quality string
	// PartCount is status.partCount, the parts on disk; PartRefs is how
	// many MediaFiles status.fileRefs names. The Parts fact reads "n of m".
	PartCount int32
	PartRefs  int
	Parts     []AudiobookPart
	Chapters  []AudiobookChapter
	// Tab is the modal's open tab, "details" or "search"; Search the
	// interactive search shown on the search tab, nil until one is run.
	Tab    string
	Search *AudiobookSearchPanel
}

// AudiobookPart is one row of the Parts table: a MediaFile of the
// audiobook in play order.
type AudiobookPart struct {
	// Name is the MediaFile's name; Index its spec.partIndex.
	Name  string
	Index int32
	// Title is the chapter's title when the parts and the chapters align
	// (one chapter per part), else "Part N".
	Title string
	// Duration is the probe's runtime as a clock, "1:02:03"; "" unprobed.
	Duration string
	// Codec and Bitrate are the first audio track's, as the Files table's
	// audio cells show them ("AAC", "64 kbps").
	Codec     string
	Bitrate   string
	SizeBytes int64
}

// AudiobookChapter is one chapter marker of status.metadata.chapters.
type AudiobookChapter struct {
	Title string
	// Start is the marker as a clock, "1:02:03".
	Start string
}

// AudiobookSearchPanel is an audiobook's interactive search: the shared
// panel's results under the audiobook's own columns -- Format (M4B, MP3,
// FLAC), Bitrate, Size, Age, Indexer, Peers, Score -- which the panel keeps
// through every poll and grab by polling and grabbing through the
// audiobook's own routes (PollURL, GrabURL) rather than /searches, whose
// panel draws Sonarr's columns.
type AudiobookSearchPanel struct {
	SearchPanel
	// Audiobook names the audiobook the search is for, whose routes the
	// panel polls and grabs through.
	Audiobook string
	// RuntimeMinutes is the audiobook's runtime, which a release's bitrate
	// is estimated from with its size (audiobookBitrate); 0 shows none.
	RuntimeMinutes int32
}

// AudiobookURL is an audiobook's page, and the base of its routes.
func AudiobookURL(namespace, name string) string {
	return fmt.Sprintf("/library/%s/audiobook/%s", namespace, name)
}

// URL is the audiobook's page.
func (v AudiobookView) URL() string { return AudiobookURL(v.Item.Ref.Namespace, v.Item.Ref.Name) }

// DetailsURL is the audiobook's details: the modal to htmx, the page
// otherwise.
func (v AudiobookView) DetailsURL() string { return v.URL() + "/details" }

// SearchURL is the audiobook's automatic search, the per-item "search now".
func (v AudiobookView) SearchURL() string { return v.URL() + "/search" }

// InteractiveSearchURL starts an interactive search for the audiobook.
func (v AudiobookView) InteractiveSearchURL() string { return v.URL() + "/search/interactive" }

// PanelURL is the audiobook's own route for a Search's results panel, by
// the Search's name: the panel under the audiobook's columns.
func (p AudiobookSearchPanel) PanelURL() string {
	return AudiobookURL(p.Namespace, p.Audiobook) + "/search/panel/" + p.Name
}

// PollURL is the route the panel asks again at: its own.
func (p AudiobookSearchPanel) PollURL() string { return p.PanelURL() }

// GrabURL is the download button's action: the audiobook's own grab route
// for the Search, which answers the panel under the audiobook's columns.
func (p AudiobookSearchPanel) GrabURL() string { return p.PanelURL() + "/grab" }

// audiobookPartsLabel is the Parts fact, "n of m": the parts on disk over
// the MediaFiles the status names, the larger of the two the whole.
func audiobookPartsLabel(partCount int32, partRefs int) string {
	return fmt.Sprintf("%d of %d", partCount, max(partCount, int32(partRefs)))
}

// audiobookBitrate estimates a release's bitrate from its size and the
// audiobook's runtime, "64 kbps", since an indexer reports a release's
// size and never its bitrate; "" when either is unknown.
func audiobookBitrate(sizeBytes int64, runtimeMinutes int32) string {
	if sizeBytes <= 0 || runtimeMinutes <= 0 {
		return ""
	}
	kbps := sizeBytes * 8 / (int64(runtimeMinutes) * 60) / 1000
	if kbps <= 0 {
		return ""
	}
	return fmt.Sprintf("%d kbps", kbps)
}

// audiobookStatusLabel is the Status fact in words, as the episode page's:
// a file is Downloaded or Cutoff Unmet with the reason the catalog recorded,
// else the status cell's own word.
func audiobookStatusLabel(v AudiobookView) string {
	s := v.Status
	if s.Kind != "file" {
		return s.Label
	}
	switch {
	case s.Warn && v.CutoffReason != "" && v.CutoffReason != "CutoffUnmet":
		return "Cutoff Unmet: " + v.CutoffReason
	case s.Warn:
		return "Cutoff Unmet"
	default:
		return "Downloaded"
	}
}

// audiobookHeading is the modal's title: the first author, then the title
// and subtitle.
func audiobookHeading(v AudiobookView) string {
	title := v.Item.Title
	if v.Subtitle != "" {
		title += ": " + v.Subtitle
	}
	if len(v.Authors) == 0 {
		return title
	}
	return v.Authors[0] + " - " + title
}

// audiobookChaptersLabel is the Chapters collapsible's trigger while
// closed: "Show 12 chapters".
func audiobookChaptersLabel(n int) string {
	if n == 1 {
		return "Show 1 chapter"
	}
	return fmt.Sprintf("Show %d chapters", n)
}
