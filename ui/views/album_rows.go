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

// The Music pages (2026-11-10 design §9.2; plan W4b.1): an artist's page
// is Lidarr's, its albums a table grouped by type under collapsible
// headers, each row the book row's shape -- the monitor bookmark, the title
// opening the album's page, the release date, a type badge, the status
// cell and the two searches; an album's page is the episode page's
// analogue, with the tracks table, the Files section and the neighbouring
// albums. ui builds every view here (ui/artist.go, ui/album.go,
// ui/album_search.go); the views only render.

// AlbumRow is one album on an artist's albums table.
type AlbumRow struct {
	Namespace string
	Name      string
	// Artist is the artist's name, for the artist's page a form returns to.
	Artist string
	Title  string
	// Disambiguation distinguishes same-named release groups ("deluxe
	// edition"), shown muted beside the title.
	Disambiguation string
	// Type is the MusicBrainz primary release-group type, lower-cased:
	// album, ep, single, broadcast or other; "" before the metadata.
	Type string
	// SecondaryTypes are the release group's secondary types (live,
	// compilation, soundtrack...), as MusicBrainz spells them.
	SecondaryTypes []string
	// ReleaseDate is YYYY-MM-DD, or "" when unknown; ReleaseDateLabel is
	// how the table shows it (ui.relativeDate).
	ReleaseDate      string
	ReleaseDateLabel string
	Monitored        bool
	HasFile          bool
	// Quality is the lowest quality across the album's imported tracks,
	// "" without a file.
	Quality string
	// TrackSummary is status.trackSummary, "<files>/<tracks>", "" before
	// the tracks are known.
	TrackSummary string
	Phase        string
	// CutoffReason is status.cutoffReason (CutoffMet, CutoffUnmet,
	// TracksMissing, ProfileUnresolved, Pending).
	CutoffReason string
	// Status is the status cell (ui.albumStatus).
	Status RowStatus
	// Error is set on a row rendered as the reply to a toggle that failed.
	Error ActionFailure
}

// DisplayTitle is the album's title, or its object name before its
// metadata has arrived.
func (r AlbumRow) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

// URL is the album's page, and the base of its actions' routes.
func (r AlbumRow) URL() string { return fmt.Sprintf("/library/%s/album/%s", r.Namespace, r.Name) }

// MonitorURL is the album's monitor action, the existing per-item route.
func (r AlbumRow) MonitorURL() string { return r.URL() + "/monitor" }

// SearchURL is the album's automatic search, the per-item "search now".
func (r AlbumRow) SearchURL() string { return r.URL() + "/search" }

// InteractiveSearchURL starts an interactive search for the album.
func (r AlbumRow) InteractiveSearchURL() string { return r.URL() + "/search/interactive" }

// RefreshURL is the album's metadata refresh, the existing per-item route.
func (r AlbumRow) RefreshURL() string { return r.URL() + "/refresh" }

// DetailsURL is the album's details: the modal to htmx, the album's page
// otherwise.
func (r AlbumRow) DetailsURL() string { return r.URL() + "/details" }

// ReturnURL is the artist's page, where a form post from the row returns.
func (r AlbumRow) ReturnURL() string { return ArtistURL(r.Namespace, r.Artist) }

// ArtistURL is an artist's page.
func ArtistURL(namespace, name string) string {
	return fmt.Sprintf("/library/%s/artist/%s", namespace, name)
}

// TypeLabel is the type badge's text: "Album", "EP", "Single",
// "Broadcast", "Other"; "" before the metadata.
func (r AlbumRow) TypeLabel() string { return albumTypeLabel(r.Type) }

// albumGroupOrder is Lidarr's order of an artist's album groups, each the
// MusicBrainz primary type (lower-cased) and its heading. A type MusicBrainz
// names that is none of these, or none at all, is grouped under Other.
var albumGroupOrder = []struct{ Key, Label, Type string }{
	{"album", "Albums", "Album"},
	{"ep", "EPs", "EP"},
	{"single", "Singles", "Single"},
	{"broadcast", "Broadcasts", "Broadcast"},
	{"other", "Other", "Other"},
}

// albumTypeLabel is a primary type's badge text.
func albumTypeLabel(t string) string {
	if t == "" {
		return ""
	}
	for _, g := range albumGroupOrder {
		if g.Key == strings.ToLower(t) {
			return g.Type
		}
	}
	return "Other"
}

// albumGroupKey is the group a type falls into: its own, or other.
func albumGroupKey(t string) string {
	for _, g := range albumGroupOrder {
		if g.Key == strings.ToLower(t) {
			return g.Key
		}
	}
	return "other"
}

// AlbumGroup is one type's albums on an artist's page: Key the
// MusicBrainz primary type (album, ep, single, broadcast, other), Label
// its heading, Rows the albums in the artist page's order.
type AlbumGroup struct {
	Key   string
	Label string
	Rows  []AlbumRow
}

// AlbumGroups splits an artist's albums by type in Lidarr's order --
// Albums, EPs, Singles, Broadcasts, Other -- keeping each group's rows in
// the order given and leaving an empty group out.
func AlbumGroups(rows []AlbumRow) []AlbumGroup {
	byKey := map[string][]AlbumRow{}
	for _, r := range rows {
		k := albumGroupKey(r.Type)
		byKey[k] = append(byKey[k], r)
	}
	var groups []AlbumGroup
	for _, g := range albumGroupOrder {
		if rows := byKey[g.Key]; len(rows) > 0 {
			groups = append(groups, AlbumGroup{Key: g.Key, Label: g.Label, Rows: rows})
		}
	}
	return groups
}

// Count is the group's heading badge, "files / albums": how many of its
// albums have a file.
func (g AlbumGroup) Count() string {
	files := 0
	for _, r := range g.Rows {
		if r.HasFile {
			files++
		}
	}
	return fmt.Sprintf("%d / %d", files, len(g.Rows))
}

// ArtistView is everything an artist's page shows (design §9.2): the
// item's Detail -- the library card, its fanart, path, overview, genres,
// links, neighbours and the Edit dialog's profiles -- and the artist's own
// facts and albums.
type ArtistView struct {
	Detail
	// SortName is the name the library sorts by, shown when it differs.
	SortName string
	// Disambiguation distinguishes same-named artists ("UK rock band").
	Disambiguation string
	// Type is the MusicBrainz artist type (Person, Group), Country the
	// country's English name, YearsActive the life-span ("1965 – 2016",
	// "1990 – present").
	Type        string
	Country     string
	YearsActive string
	// Summary is the Status fact from the rollup: "12 albums, 3 missing".
	Summary string
	// Groups are the artist's albums by type (AlbumGroups).
	Groups []AlbumGroup
}

// AlbumCount counts the albums listed.
func (v ArtistView) AlbumCount() int {
	n := 0
	for _, g := range v.Groups {
		n += len(g.Rows)
	}
	return n
}

// TrackRow is one track on an album page's Tracks table: its disc and
// number, title and duration, and its file's audio cells when a MediaFile
// is attributed to it.
type TrackRow struct {
	Medium int32
	Number int32
	Title  string
	// Duration is the track's length as "m:ss"; "" when unknown.
	Duration string
	Explicit bool
	// File is the MediaFile attributed to the track (status.tracks[].fileRef),
	// nil for a track missing from disk.
	File *FileRow
}

// Position is the track's place: "1-03" on a multi-disc release when
// discs says so, else "3".
func (t TrackRow) Position(discs bool) string {
	if discs && t.Medium > 0 {
		return fmt.Sprintf("%d-%02d", t.Medium, t.Number)
	}
	return fmt.Sprint(t.Number)
}

// AlbumView is everything an album's page shows (design §9.2, the episode
// page's analogue).
type AlbumView struct {
	// Detail is what the shared sections read: Item is the artist's library
	// card (poster, art key, quality profile, tab), Backdrop its fanart;
	// Path, Overview, Genres, Links, Files, Prev and Next are the album's
	// own.
	Detail
	Row AlbumRow
	// Cover is the album's own cover through /art, "" when none has been
	// fetched (the hero then shows the artist's poster); CoverKey is its
	// image key, for the SSE art event.
	Cover    string
	CoverKey string
	// ArtistCredit is the release group's credit when it differs from the
	// artist's name ("Various Artists", "Simon & Garfunkel").
	ArtistCredit string
	// Label is the selected release's record label, Duration the tracks'
	// lengths summed ("47m", "1h 12m"), Format the release's media format.
	Label    string
	Duration string
	// TrackCount and TrackFileCount are the tracks known and the tracks
	// with a file.
	TrackCount     int32
	TrackFileCount int32
	Tracks         []TrackRow
	// Discs is whether the release has more than one medium, which puts
	// the disc before each track's number.
	Discs bool
}

// PageTitle is "Artist - Album".
func (v AlbumView) PageTitle() string { return v.Item.Title + " - " + v.Row.DisplayTitle() }

// AlbumDetail is an album's details modal, the book modal's counterpart:
// the album's summary and files on one tab, its searches on the other.
type AlbumDetail struct {
	Row AlbumRow
	// ArtistTitle is the artist's name as the library shows it.
	ArtistTitle string
	Overview    string
	Genres      []string
	// Files are the album's MediaFiles, none without one.
	Files []FileRow
	// Tab is the open tab, "details" or "search".
	Tab string
	// Search is the interactive search shown on the search tab, nil until
	// one is run.
	Search *SearchPanel
}

// AlbumSearchPanel is an album's interactive search panel: the shared
// SearchPanel, drawn with Lidarr's columns (AlbumSearchPanelView), polled
// and grabbed through the album's own routes so the columns hold across
// every refresh -- the shared /searches routes draw the video columns.
type AlbumSearchPanel struct {
	SearchPanel
	// Album is the album searched for, whose routes the panel uses;
	// ArtistTitle names its artist in the modal's title.
	Album       AlbumRow
	ArtistTitle string
}

// PanelURL is the panel's own route: GET answers the panel to htmx.
func (p AlbumSearchPanel) PanelURL() string { return p.Album.URL() + "/search/panel/" + p.Name }

// GrabURL is the download button's action on the album's panel.
func (p AlbumSearchPanel) GrabURL() string { return p.PanelURL() + "/grab" }

// Heading is the modal's title: "Interactive Search - Artist - Album".
func (p AlbumSearchPanel) Heading() string {
	title := p.Album.DisplayTitle()
	if p.ArtistTitle != "" {
		title = p.ArtistTitle + " - " + title
	}
	return "Interactive Search - " + title
}
