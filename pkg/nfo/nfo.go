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

// Package nfo renders the Kodi-shape artist.nfo and album.nfo documents
// Plex's music agent reads from a music library's folders (PMS 1.43.5,
// PM-6153; docs/superpowers/specs/2026-11-10-non-video-libraries-design.md
// §8). The manager renders every byte here, from the catalog's own
// objects, and the import agent writes them (app/import/worker/nfo); the
// documents never carry a credential or a cluster address.
//
// Shapes: kodi.wiki/view/NFO_files/Artists and /Albums, plus the
// Jellyfin-style <artist>/<albumartist> fallbacks the Plex developer said
// the agent would read (forums.plex.tv/t/plex-nfo-agent-forum-preview,
// post 540) and the <track> list MediaElch and Jellyfin write, which the
// agent was observed to accept. An element whose value is empty is left
// out, as Kodi's exporter leaves it out. An Author is rendered as an
// artist and an Audiobook as an album, the convention Plex's music
// libraries have always kept for audiobooks.
package nfo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// The file names the import agent writes in an item's folder.
const (
	FileArtist      = "artist.nfo"
	FileArtistImage = "artist.jpg"
	FileAlbum       = "album.nfo"
	FileAlbumImage  = "folder.jpg"
)

// Header is the XML declaration every document starts with, Kodi's.
const Header = `<?xml version="1.0" encoding="UTF-8" standalone="yes" ?>` + "\n"

// Part is one audio part of an audiobook as the album's track list names
// it: its 1-based index, its title (a chapter's when the parts and chapters
// align one to one, else "Part N") and its probed duration.
type Part struct {
	Index      int32
	Title      string
	DurationMs int64
}

type artistDoc struct {
	XMLName        xml.Name    `xml:"artist"`
	Name           string      `xml:"name,omitempty"`
	MusicBrainzID  string      `xml:"musicBrainzArtistID,omitempty"`
	SortName       string      `xml:"sortname,omitempty"`
	Type           string      `xml:"type,omitempty"`
	Disambiguation string      `xml:"disambiguation,omitempty"`
	Genres         []string    `xml:"genre,omitempty"`
	YearsActive    string      `xml:"yearsactive,omitempty"`
	Born           string      `xml:"born,omitempty"`
	Formed         string      `xml:"formed,omitempty"`
	Biography      string      `xml:"biography,omitempty"`
	Died           string      `xml:"died,omitempty"`
	Disbanded      string      `xml:"disbanded,omitempty"`
	Thumb          *thumb      `xml:"thumb,omitempty"`
	Albums         []albumLink `xml:"album,omitempty"`
}

type thumb struct {
	Aspect string `xml:"aspect,attr,omitempty"`
	URL    string `xml:",chardata"`
}

type albumLink struct {
	Title          string `xml:"title,omitempty"`
	Year           string `xml:"year,omitempty"`
	ReleaseGroupID string `xml:"musicbrainzreleasegroupid,omitempty"`
}

type albumDoc struct {
	XMLName             xml.Name       `xml:"album"`
	Title               string         `xml:"title,omitempty"`
	MusicBrainzAlbumID  string         `xml:"musicbrainzalbumid,omitempty"`
	ReleaseGroupID      string         `xml:"musicbrainzreleasegroupid,omitempty"`
	ArtistDesc          string         `xml:"artistdesc,omitempty"`
	Genres              []string       `xml:"genre,omitempty"`
	Compilation         string         `xml:"compilation,omitempty"`
	Review              string         `xml:"review,omitempty"`
	Type                string         `xml:"type,omitempty"`
	ReleaseStatus       string         `xml:"releasestatus,omitempty"`
	ReleaseDate         string         `xml:"releasedate,omitempty"`
	OriginalReleaseDate string         `xml:"originalreleasedate,omitempty"`
	Label               string         `xml:"label,omitempty"`
	Year                string         `xml:"year,omitempty"`
	Rating              *rating        `xml:"rating,omitempty"`
	Votes               string         `xml:"votes,omitempty"`
	Thumb               *thumb         `xml:"thumb,omitempty"`
	AlbumArtistCredits  []artistCredit `xml:"albumArtistCredits,omitempty"`
	Artist              string         `xml:"artist,omitempty"`
	AlbumArtist         string         `xml:"albumartist,omitempty"`
	Tracks              []track        `xml:"track,omitempty"`
}

type rating struct {
	Max   string `xml:"max,attr"`
	Value string `xml:",chardata"`
}

type artistCredit struct {
	Artist        string `xml:"artist,omitempty"`
	MusicBrainzID string `xml:"musicBrainzArtistID,omitempty"`
}

type track struct {
	Disc          string `xml:"disc,omitempty"`
	Position      string `xml:"position,omitempty"`
	Title         string `xml:"title,omitempty"`
	Duration      string `xml:"duration,omitempty"`
	MusicBrainzID string `xml:"musicBrainzTrackID,omitempty"`
}

// Artist renders artist.nfo for a (its albums listed as Kodi's scraped
// discography is). thumbURL is the artist image's source URL, the
// provider's, or "" when none is known.
func Artist(a *catalogv1.Artist, albums []catalogv1.Album, thumbURL string) []byte {
	d := artistDoc{MusicBrainzID: a.Spec.MusicBrainzID}
	if m := a.Status.Metadata; m != nil {
		d.Name, d.SortName, d.Type, d.Disambiguation = m.Name, m.SortName, m.Type, m.Disambiguation
		d.Genres = append(d.Genres, m.Genres...)
		d.Biography = m.Overview
		d.YearsActive = yearsActive(m.Begin, m.End)
		if strings.EqualFold(m.Type, "Person") {
			d.Born, d.Died = m.Begin, m.End
		} else {
			d.Formed, d.Disbanded = m.Begin, m.End
		}
	}
	if thumbURL != "" {
		d.Thumb = &thumb{Aspect: "poster", URL: thumbURL}
	}
	for i := range albums {
		al := &albums[i]
		link := albumLink{ReleaseGroupID: al.Spec.ReleaseGroupID}
		if m := al.Status.Metadata; m != nil {
			link.Title = m.Title
			link.Year = year(m.ReleaseDate)
		}
		d.Albums = append(d.Albums, link)
	}
	return render(d)
}

// Album renders album.nfo for al by artist (nil when the artist is not
// known). thumbURL is the cover's source URL or "".
func Album(al *catalogv1.Album, artist *catalogv1.Artist, thumbURL string) []byte {
	d := albumDoc{ReleaseGroupID: al.Spec.ReleaseGroupID}
	var artistName, artistID string
	if artist != nil {
		artistID = artist.Spec.MusicBrainzID
		if artist.Status.Metadata != nil {
			artistName = artist.Status.Metadata.Name
		}
	}
	if m := al.Status.Metadata; m != nil {
		d.Title, d.MusicBrainzAlbumID, d.Review, d.Type, d.Label = m.Title, m.SelectedReleaseID, m.Overview, m.AlbumType, m.Label
		d.Genres = append(d.Genres, m.Genres...)
		d.Compilation = boolString(hasFold(m.SecondaryTypes, "compilation"))
		d.OriginalReleaseDate = date(m.ReleaseDate)
		d.ReleaseDate = d.OriginalReleaseDate
		for _, r := range m.Releases {
			if r.ID == m.SelectedReleaseID {
				d.ReleaseStatus = r.Status
				if r.ReleaseDate != nil {
					d.ReleaseDate = date(r.ReleaseDate)
				}
				if d.Label == "" {
					d.Label = r.Label
				}
			}
		}
		d.Year = year(m.ReleaseDate)
		if m.ArtistCredit != "" {
			artistName = m.ArtistCredit
		}
		for _, r := range m.Ratings {
			if r.Source == catalogv1.RatingSourceMusicBrainz {
				d.Rating = &rating{Max: "10", Value: centis(r.ValueCentis)}
				if r.Votes > 0 {
					d.Votes = fmt.Sprint(r.Votes)
				}
			}
		}
	}
	if thumbURL != "" {
		d.Thumb = &thumb{URL: thumbURL}
	}
	if artistName != "" || artistID != "" {
		d.ArtistDesc = artistName
		d.AlbumArtistCredits = []artistCredit{{Artist: artistName, MusicBrainzID: artistID}}
		d.Artist, d.AlbumArtist = artistName, artistName
	}
	for _, t := range al.Status.Tracks {
		d.Tracks = append(d.Tracks, track{
			Disc:          positive(t.Medium),
			Position:      positive(t.Number),
			Title:         t.Title,
			Duration:      duration(int64(t.DurationMs)),
			MusicBrainzID: t.RecordingID,
		})
	}
	return render(d)
}

// AuthorAsArtist renders an Author as artist.nfo for an audiobook root:
// the author is the library's artist, each Audiobook an album of theirs.
func AuthorAsArtist(au *catalogv1.Author, audiobooks []catalogv1.Audiobook, thumbURL string) []byte {
	d := artistDoc{Type: "Person"}
	if m := au.Status.Metadata; m != nil {
		d.Name, d.SortName, d.Disambiguation, d.Biography = m.Name, m.SortName, m.Disambiguation, m.Overview
		d.Genres = append(d.Genres, m.Genres...)
		d.Born, d.Died = m.Born, m.Died
	}
	if thumbURL != "" {
		d.Thumb = &thumb{Aspect: "poster", URL: thumbURL}
	}
	for i := range audiobooks {
		if m := audiobooks[i].Status.Metadata; m != nil {
			d.Albums = append(d.Albums, albumLink{Title: m.Title, Year: year(m.ReleaseDate)})
		}
	}
	return render(d)
}

// AudiobookAsAlbum renders an Audiobook as album.nfo: its authors are the
// album artists, the publisher its label, its parts its tracks. authorName
// is the Author object's name when one is linked, else "" (the metadata's
// first credited author is used then).
func AudiobookAsAlbum(ab *catalogv1.Audiobook, authorName string, parts []Part, thumbURL string) []byte {
	d := albumDoc{Type: "Audiobook"}
	if m := ab.Status.Metadata; m != nil {
		d.Title, d.Review, d.Label = m.Title, m.Overview, m.Publisher
		if m.Subtitle != "" {
			d.Title = m.Title + ": " + m.Subtitle
		}
		d.Genres = append(d.Genres, m.Genres...)
		d.ReleaseDate = date(m.ReleaseDate)
		d.OriginalReleaseDate = d.ReleaseDate
		d.Year = year(m.ReleaseDate)
		for _, a := range m.Authors {
			d.AlbumArtistCredits = append(d.AlbumArtistCredits, artistCredit{Artist: a.Name})
		}
		if authorName == "" && len(m.Authors) > 0 {
			authorName = m.Authors[0].Name
		}
		for _, r := range m.Ratings {
			if r.Source == catalogv1.RatingSourceAudnexus {
				d.Rating = &rating{Max: "10", Value: centis(r.ValueCentis)}
				if r.Votes > 0 {
					d.Votes = fmt.Sprint(r.Votes)
				}
			}
		}
	}
	if authorName != "" {
		d.ArtistDesc, d.Artist, d.AlbumArtist = authorName, authorName, authorName
		if len(d.AlbumArtistCredits) == 0 {
			d.AlbumArtistCredits = []artistCredit{{Artist: authorName}}
		}
	}
	if thumbURL != "" {
		d.Thumb = &thumb{URL: thumbURL}
	}
	for _, p := range parts {
		title := p.Title
		if title == "" {
			title = fmt.Sprintf("Part %d", p.Index)
		}
		d.Tracks = append(d.Tracks, track{Disc: "1", Position: positive(p.Index), Title: title, Duration: duration(p.DurationMs)})
	}
	return render(d)
}

// Hash is the hex SHA-256 over the rendered documents and the image
// objects' digests, in order: what status.nfo.hash records, so a write is
// planned once per change of either.
func Hash(docs [][]byte, imageDigests []string) string {
	h := sha256.New()
	for _, d := range docs {
		fmt.Fprintf(h, "%d:", len(d))
		h.Write(d)
	}
	for _, dg := range imageDigests {
		fmt.Fprintf(h, "img:%s;", dg)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func render(doc any) []byte {
	var buf bytes.Buffer
	buf.WriteString(Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		// The documents are structs of strings; the encoder fails only on
		// an invalid UTF-8 rune, which the API's validated strings never
		// carry. Render what the header alone says rather than nothing.
		return []byte(Header)
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// date and year read every calendar field in UTC: metav1.Time comes back
// from the apiserver in the replica's zone, and a release dated 1 January
// 00:30 UTC would otherwise lose a year west of UTC (the CLAUDE.md gotcha).
func date(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

func year(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return fmt.Sprint(t.UTC().Year())
}

// yearsActive is Kodi's "YYYY-YYYY", "YYYY-" while active, "" unknown.
func yearsActive(begin, end string) string {
	b, e := yearOf(begin), yearOf(end)
	switch {
	case b == "" && e == "":
		return ""
	case e == "":
		return b + "-"
	default:
		return b + "-" + e
	}
}

func yearOf(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return d
}

func duration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	s := ms / int64(time.Second/time.Millisecond)
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func centis(v int32) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%d.%02d", v/100, v%100), "0"), ".")
}

func positive(n int32) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprint(n)
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func hasFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
