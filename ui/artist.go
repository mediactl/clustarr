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
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An artist's page (2026-11-10 design §9.2; plan W4b.1) is Lidarr's: the
// item page's hero on the artist's fanart with its facts and the rollup's
// status, the action bar, and the Albums table grouped by type. It is
// filled from what the cluster already holds -- the projection's card, the
// Artist and its Albums through the reader -- and nothing here writes.

// handleArtistPage serves GET /library/{namespace}/artist/{name}. An artist
// the projection does not list is not found, unless Add New just created
// it, when the page says so and reloads.
func (s *Server) handleArtistPage(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(commonv1.MediaKindArtist), name)
	if !ok {
		if s.justAdded(r.Context(), ns, string(commonv1.MediaKindArtist), name) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := views.AddPending(name).Render(r.Context(), w); err != nil {
				logging.FromContext(r.Context()).Error("render add pending page", "error", err)
			}
			return
		}
		http.NotFound(w, r)
		return
	}
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ArtistPage(s.artistPage(r.Context(), item)).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render artist page", "error", err)
	}
}

// artistPage builds an artist's page: the item's Detail (itemDetail: path,
// biography, genres, fanart, neighbours, the Edit dialog's profiles), the
// artist's own facts and links, and its albums grouped by type. With no
// reader the card alone.
func (s *Server) artistPage(ctx context.Context, item projection.LibraryItem) views.ArtistView {
	v := views.ArtistView{Detail: s.itemDetail(ctx, item)}
	var a catalogv1.Artist
	if !s.getObject(ctx, item.Ref, &a) {
		return v
	}
	rows, err := s.albumRows(ctx, item.Ref.Namespace, item.Ref.Name)
	if err != nil {
		logging.FromContext(ctx).Error("list an artist's albums", "artist", item.Ref.String(), "error", err)
	}
	fillArtist(&v, &a, len(rows))
	v.Groups = views.AlbumGroups(rows)
	return v
}

// fillArtist reads the artist's facts off the Artist: the rollup's summary,
// the provider links, and the metadata's sort name, disambiguation, type,
// country and years active. listed is how many albums the page lists, the
// summary's count when the rollup has none yet.
func fillArtist(v *views.ArtistView, a *catalogv1.Artist, listed int) {
	v.Summary = artistSummary(a, listed)
	v.Links = artistLinks(a)
	md := a.Status.Metadata
	if md == nil {
		return
	}
	v.SortName, v.Disambiguation, v.Type = md.SortName, md.Disambiguation, md.Type
	v.Country = countryName(md.Country)
	v.YearsActive = yearsActive(md.Begin, md.End, md.Status)
}

// artistSummary is the Status fact from the container rollup (design
// §9.2): "12 albums, 3 missing", with the downloading and below-cutoff
// counts when any; "No albums yet" before the metadata brings them.
func artistSummary(a *catalogv1.Artist, listed int) string {
	n := int(a.Status.AlbumCount)
	if n == 0 {
		n = listed
	}
	if n == 0 {
		return "No albums yet"
	}
	parts := []string{plural(n, "album")}
	if m := a.Status.MissingChildCount; m > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", m))
	}
	if d := a.Status.DownloadingChildCount; d > 0 {
		parts = append(parts, fmt.Sprintf("%d downloading", d))
	}
	if c := a.Status.CutoffUnmetChildCount; c > 0 {
		parts = append(parts, fmt.Sprintf("%d below cutoff", c))
	}
	return strings.Join(parts, ", ")
}

// artistLinks are the artist's provider pages: its MusicBrainz page, then
// the metadata's url-rels -- Discogs and Wikipedia with their logos, every
// other site (homepage, Bandcamp, Last.fm, Spotify...) as a labelled link.
// A Wikidata entity is no page a person reads and is left out.
func artistLinks(a *catalogv1.Artist) []views.Link {
	var links []views.Link
	if id := a.Spec.MusicBrainzID; id != "" {
		links = append(links, views.Link{Label: "MusicBrainz", Href: "https://musicbrainz.org/artist/" + id, Provider: views.ProviderMusicBrainz})
	}
	md := a.Status.Metadata
	if md == nil {
		return links
	}
	for _, l := range md.Links {
		if l.URL == "" {
			continue
		}
		switch t := strings.ToLower(l.Type); t {
		case "discogs":
			links = append(links, views.Link{Label: "Discogs", Href: l.URL, Provider: views.ProviderDiscogs})
		case "wikipedia":
			links = append(links, views.Link{Label: "Wikipedia", Href: l.URL, Provider: views.ProviderWikipedia})
		case "wikidata", "image", "musicbrainz":
		default:
			links = append(links, views.Link{Label: linkLabel(t), Href: l.URL})
		}
	}
	return links
}

// linkLabel names a url-rel's site as people write it; a type with no
// spelling of its own is capitalised.
func linkLabel(t string) string {
	switch t {
	case "homepage":
		return "Homepage"
	case "bandcamp":
		return "Bandcamp"
	case "lastfm":
		return "Last.fm"
	case "spotify":
		return "Spotify"
	case "apple":
		return "Apple Music"
	case "youtube":
		return "YouTube"
	case "soundcloud":
		return "SoundCloud"
	case "allmusic":
		return "AllMusic"
	case "":
		return "Link"
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

// countryName is the country's English name for its ISO 3166-1 code ("GB"
// reads United Kingdom); a code x/text does not know is shown as it is.
func countryName(code string) string {
	if code == "" {
		return ""
	}
	reg, err := language.ParseRegion(code)
	if err != nil {
		return code
	}
	if name := display.English.Regions().Name(reg); name != "" {
		return name
	}
	return code
}

// yearsActive is the artist's life-span as the hero says it, from
// MusicBrainz's begin and end dates ("1965-10-09", "1990"): "1965 – 2016"
// once ended, "1990 – present" while continuing, the begin year alone for
// an ended artist with no end date, and "" with neither.
func yearsActive(begin, end string, status catalogv1.ArtistRunStatus) string {
	b, e := artistYear(begin), artistYear(end)
	switch {
	case b == "" && e == "":
		return ""
	case e != "" && b == "":
		return "– " + e
	case e != "":
		return b + " – " + e
	case status == catalogv1.ArtistRunStatusEnded:
		return b
	default:
		return b + " – present"
	}
}

// artistYear is the year of a MusicBrainz date, which may be a year, a
// month or a day.
func artistYear(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return date
}

// albumRows lists an artist's Albums through the reader, by spec.artistRef
// (the ui cache's index, AlbumArtistRefField, or Album's selectable field
// on a raw client): newest first, an undated album last, then by title.
func (s *Server) albumRows(ctx context.Context, ns, artist string) ([]views.AlbumRow, error) {
	albums, err := s.listAlbums(ctx, ns, artist)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows := make([]views.AlbumRow, 0, len(albums))
	for i := range albums {
		rows = append(rows, albumTableRow(&albums[i], now))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch {
		case a.ReleaseDate == "" && b.ReleaseDate != "":
			return false
		case a.ReleaseDate != "" && b.ReleaseDate == "":
			return true
		case a.ReleaseDate != b.ReleaseDate:
			return a.ReleaseDate > b.ReleaseDate
		}
		return a.DisplayTitle() < b.DisplayTitle()
	})
	return rows, nil
}

// listAlbums is one artist's Albums through the reader's index; none
// without a reader.
func (s *Server) listAlbums(ctx context.Context, ns, artist string) ([]catalogv1.Album, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.AlbumList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(ns),
		client.MatchingFields{AlbumArtistRefField: artist}); err != nil {
		return nil, err
	}
	return list.Items, nil
}
