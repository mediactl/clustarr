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
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An album's page (2026-11-10 design §9.2; plan W4b.1) is the episode
// page's analogue, filled from what the cluster already holds: the Album,
// its artist's library card and fanart, and the MediaFiles its tracks name.
// Its details modal is the book modal's shape for a row on the artist's
// page. Nothing here fetches from a provider, and nothing here writes.

// handleAlbumPage serves GET /library/{namespace}/album/{name}, the page an
// album's title opens (and preloads).
func (s *Server) handleAlbumPage(w http.ResponseWriter, r *http.Request) {
	s.renderAlbumPage(w, r, true)
}

// renderAlbumPage answers the album's page, preloadable when the route is
// one a link preloads; not found when the album, its artist's card or the
// reader is missing.
func (s *Server) renderAlbumPage(w http.ResponseWriter, r *http.Request, preload bool) {
	v, ok := s.albumPage(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if preload {
		preloadable(w)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AlbumPage(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render album page", "error", err)
	}
}

// handleAlbumDetails serves GET /library/{namespace}/album/{name}/details:
// the modal on its Details tab, or the Search tab with ?tab=search, to
// htmx, and the album's page to anyone else.
func (s *Server) handleAlbumDetails(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		s.renderAlbumPage(w, r, false)
		return
	}
	d, ok := s.albumDetail(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("tab") == "search" {
		d.Tab = "search"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AlbumModal(d).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render album details", "error", err)
	}
}

// albumWithArtist reads an album and its artist's library card. False when
// the album, that card or the reader is missing.
func (s *Server) albumWithArtist(ctx context.Context, ns, name string) (*catalogv1.Album, projection.LibraryItem, bool) {
	var a catalogv1.Album
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &a) {
		return nil, projection.LibraryItem{}, false
	}
	item, ok := findLibraryItem(s.opts.Library(ctx), ns, string(commonv1.MediaKindArtist), a.Spec.ArtistRef)
	if !ok {
		return nil, projection.LibraryItem{}, false
	}
	return &a, item, true
}

// albumDetail builds the album's details modal on its Details tab: the
// row, the artist's name, the overview, the genres and the files.
func (s *Server) albumDetail(ctx context.Context, ns, name string) (views.AlbumDetail, bool) {
	a, item, ok := s.albumWithArtist(ctx, ns, name)
	if !ok {
		return views.AlbumDetail{}, false
	}
	d := views.AlbumDetail{Row: albumTableRow(a, time.Now()), ArtistTitle: item.Title, Tab: "details"}
	if md := a.Status.Metadata; md != nil {
		d.Overview, d.Genres = md.Overview, md.Genres
	}
	d.Files = albumFileRows(a, s.albumFiles(ctx, a))
	return d, true
}

// albumPage builds an album's page: its row (title, type, release date,
// status), its artist's card and fanart, its cover, label, duration and
// track summary, the tracks with their files, the Files section, the
// provider links and the neighbouring albums.
func (s *Server) albumPage(ctx context.Context, ns, name string) (views.AlbumView, bool) {
	a, item, ok := s.albumWithArtist(ctx, ns, name)
	if !ok {
		return views.AlbumView{}, false
	}
	row := albumTableRow(a, time.Now())
	v := views.AlbumView{
		Detail: views.Detail{Item: albumItem(a, item, row), CanScan: hasFolder(item.Kind), HasFolder: hasFolder(item.Kind)},
		Artist: item,
		Row:    row,
	}
	var artist catalogv1.Artist
	if s.getObject(ctx, item.Ref, &artist) {
		v.Backdrop = backdropArt(commonv1.MediaKindArtist, artist.GetUID(), artist.Status.Artwork)
		// The album's files live in the artist's folder, which import leaves
		// standing: the Delete dialog names the album's own folder only
		// (views.DeleteFolder; librarydelete's Target.Keep).
		v.SharedFolder = artist.Status.Path
	}
	v.Path = a.Status.Path
	v.Links = albumLinks(a)
	if md := a.Status.Metadata; md != nil {
		v.Overview, v.Genres, v.Label = md.Overview, md.Genres, md.Label
		if md.ArtistCredit != "" && md.ArtistCredit != item.Title {
			v.ArtistCredit = md.ArtistCredit
		}
	}
	v.Cover, v.CoverKey = albumCover(a)
	files := s.albumFiles(ctx, a)
	v.Tracks, v.Discs = trackRows(a, files)
	v.TrackCount, v.TrackFileCount = int32(len(a.Status.Tracks)), albumTrackFileCount(a, files) //nolint:gosec // at most 200 tracks
	v.Duration = durationLabel(a.Status.Tracks)
	v.Files = albumFileRows(a, files)
	v.Prev, v.Next = s.albumNeighbours(ctx, a)
	return v, true
}

// albumItem is the album's own library card as its page reads it
// (views.Detail.Item; design §9.2): the album's ref, kind and title over
// its artist's tab and root folder, the quality profile its spec overrides
// else the artist's, its cover (through /art, ADR-0011), its release year,
// the live Album's monitoring, phase and file, and its pending delete --
// the three delete annotations, as the projection reads a card's -- so the
// Delete button hides while import carries one out and the status fact
// offers Retry and Cancel. The Edit and Delete dialogs' triggers, forms
// and returns are built from it (views.editDialog, views.deleteDialog), and
// the hero's profile fact reads it; the artist's own card is
// AlbumView.Artist, for the poster, the fanart, the artist link and the
// rename pair.
func albumItem(a *catalogv1.Album, artist projection.LibraryItem, row views.AlbumRow) projection.LibraryItem {
	ann := a.GetAnnotations()
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}, Kind: commonv1.MediaKindAlbum,
		Title: row.DisplayTitle(), Tab: artist.Tab, RootFolder: artist.RootFolder, QualityProfileRef: artist.QualityProfileRef,
		Monitored: row.Monitored, Phase: row.Phase, HasFile: row.HasFile,
		DeleteMode:    ann[catalogv1.AnnotationDelete],
		DeleteExclude: ann[catalogv1.AnnotationDeleteAddExclusion] == "true",
		DeleteError:   ann[catalogv1.AnnotationDeleteError],
	}
	if ref := a.Spec.QualityProfileRef; ref != nil && *ref != "" {
		item.QualityProfileRef = *ref
	}
	item.Poster, item.ArtKey = albumCover(a)
	if md := a.Status.Metadata; md != nil && md.ReleaseDate != nil {
		item.Year = int32(md.ReleaseDate.UTC().Year()) //nolint:gosec // a year
	}
	return item
}

// albumTableRow is one album's row on its artist's albums table, and the
// album pages' own facts: the title, type and release date from the
// metadata, the file's quality and track summary from the status, and the
// status cell (albumStatus).
func albumTableRow(a *catalogv1.Album, now time.Time) views.AlbumRow {
	row := views.AlbumRow{
		Namespace: a.Namespace, Name: a.Name, Artist: a.Spec.ArtistRef,
		Monitored: monitoredOrDefault(a.Spec.Monitored), HasFile: albumHasFile(a),
		TrackSummary: a.Status.TrackSummary, Phase: string(a.Status.Phase), CutoffReason: a.Status.CutoffReason,
	}
	if md := a.Status.Metadata; md != nil {
		row.Title, row.Disambiguation = md.Title, md.Disambiguation
		row.Type, row.SecondaryTypes = strings.ToLower(md.AlbumType), md.SecondaryTypes
		if md.ReleaseDate != nil {
			row.ReleaseDate = md.ReleaseDate.UTC().Format(time.DateOnly)
			row.ReleaseDateLabel = relativeDate(md.ReleaseDate.UTC(), now)
		}
	}
	if q := a.Status.FileQuality; q != nil {
		row.Quality = q.Name
	}
	row.Status = albumStatus(a, row.Monitored, now)
	return row
}

// albumHasFile is whether any track has an imported file: status.hasFile,
// or a track-file count the rollup wrote before the flag.
func albumHasFile(a *catalogv1.Album) bool {
	return a.Status.HasFile || a.Status.TrackFileCount > 0
}

// albumStatus is the episode's status cell for an album (design §9.2), the
// first case that applies: a grab in flight, a grab waiting out its delay,
// the file's quality with "files/tracks" -- amber below the cutoff, with
// tracks missing or with a profile that could not be resolved -- not yet
// released, unmonitored, else missing. An album without a release date is
// one MusicBrainz has not dated, not one still to come, so it reads
// missing.
func albumStatus(a *catalogv1.Album, monitored bool, now time.Time) views.RowStatus {
	st := a.Status
	switch {
	case st.Phase == catalogv1.AlbumPhaseDownloading || st.ActiveDownloadRef != nil:
		return views.RowStatus{Kind: "downloading", Label: "Downloading", Title: "Album is downloading"}
	case st.Phase == catalogv1.AlbumPhaseDelayed || st.PendingGrab != nil:
		return views.RowStatus{Kind: "pending", Label: "Pending", Title: "A release waits out its delay profile"}
	case albumHasFile(a):
		s := views.RowStatus{Kind: "file", Label: "Unknown", Title: "Album on disk"}
		if st.FileQuality != nil && st.FileQuality.Name != "" {
			s.Label = st.FileQuality.Name
		}
		if st.TrackSummary != "" {
			s.Label += " · " + st.TrackSummary
		}
		switch {
		case st.CutoffReason == "TracksMissing":
			s.Warn, s.Title = true, "Tracks are missing from disk"
			if st.TrackSummary != "" {
				s.Title = "Tracks are missing: " + st.TrackSummary + " on disk"
			}
		case st.Phase == catalogv1.AlbumPhaseCutoffUnmet:
			s.Warn, s.Title = true, "Quality cutoff has not been met"
		case st.Phase == catalogv1.AlbumPhaseCutoffUnevaluated:
			s.Warn, s.Title = true, "Quality profile could not be resolved"
		}
		return s
	case st.Metadata != nil && st.Metadata.ReleaseDate != nil && st.Metadata.ReleaseDate.After(now):
		return views.RowStatus{Kind: "unreleased", Label: "Unreleased", Title: "Album has not been released"}
	case !monitored:
		return views.RowStatus{Kind: "unmonitored", Label: "Unmonitored", Title: "Album is not monitored"}
	default:
		return views.RowStatus{Kind: "missing", Label: "Missing", Title: "Album missing from disk"}
	}
}

// albumLinks are the album's provider pages: its MusicBrainz release
// group, the selected release, and its Discogs master when the metadata
// knows one.
func albumLinks(a *catalogv1.Album) []views.Link {
	var links []views.Link
	if id := a.Spec.ReleaseGroupID; id != "" {
		links = append(links, views.Link{Label: "MusicBrainz (release group)", Href: "https://musicbrainz.org/release-group/" + id, Provider: views.ProviderMusicBrainz})
	}
	md := a.Status.Metadata
	if md == nil {
		return links
	}
	if id := md.SelectedReleaseID; id != "" {
		links = append(links, views.Link{Label: "MusicBrainz (release)", Href: "https://musicbrainz.org/release/" + id, Provider: views.ProviderMusicBrainz})
	}
	if id := md.ExternalIDs["discogs"]; id != "" {
		links = append(links, views.Link{Label: "Discogs", Href: "https://www.discogs.com/master/" + id, Provider: views.ProviderDiscogs})
	}
	return links
}

// albumCover is the album's own cover through /art (the poster entry of
// status.artwork, which the Cover Art Archive's front image is filed under)
// and its image key; "" when none has been fetched yet. Never a provider's
// own URL (ADR-0011).
func albumCover(a *catalogv1.Album) (url, key string) {
	for _, e := range a.Status.Artwork {
		if e.Type == catalogv1.ImageTypePoster {
			return projection.ArtURL(commonv1.MediaKindAlbum, a.GetUID(), catalogv1.ImageTypePoster, e.Digest),
				projection.ArtKey(commonv1.MediaKindAlbum, a.GetUID(), catalogv1.ImageTypePoster)
		}
	}
	return "", ""
}

// albumFiles reads the MediaFiles the album's tracks name
// (status.tracks[].fileRef), by name: a cached Get per distinct file, never
// a List. A file the reader cannot find is left out.
func (s *Server) albumFiles(ctx context.Context, a *catalogv1.Album) map[string]*catalogv1.MediaFile {
	files := map[string]*catalogv1.MediaFile{}
	for _, t := range a.Status.Tracks {
		if t.FileRef == nil || *t.FileRef == "" {
			continue
		}
		if _, seen := files[*t.FileRef]; seen {
			continue
		}
		var f catalogv1.MediaFile
		if s.getObject(ctx, types.NamespacedName{Namespace: a.Namespace, Name: *t.FileRef}, &f) {
			files[*t.FileRef] = &f
		}
	}
	return files
}

// albumFileRows are the album's Files rows, each distinct file once, in
// track order, paths relative to the album's folder.
func albumFileRows(a *catalogv1.Album, files map[string]*catalogv1.MediaFile) []views.FileRow {
	var rows []views.FileRow
	seen := map[string]bool{}
	for _, t := range sortedTracks(a) {
		if t.FileRef == nil || seen[*t.FileRef] {
			continue
		}
		f := files[*t.FileRef]
		if f == nil {
			continue
		}
		seen[*t.FileRef] = true
		r, _ := fileRows(a.Status.Path, f)
		rows = append(rows, r[0])
	}
	return rows
}

// albumTrackFileCount counts the tracks with a file the reader found, else
// the status's own count.
func albumTrackFileCount(a *catalogv1.Album, files map[string]*catalogv1.MediaFile) int32 {
	var n int32
	for _, t := range a.Status.Tracks {
		if t.FileRef != nil && files[*t.FileRef] != nil {
			n++
		}
	}
	if n == 0 {
		return a.Status.TrackFileCount
	}
	return n
}

// sortedTracks is the album's tracks in disc and number order.
func sortedTracks(a *catalogv1.Album) []catalogv1.Track {
	tracks := append([]catalogv1.Track(nil), a.Status.Tracks...)
	sort.SliceStable(tracks, func(i, j int) bool {
		x, y := tracks[i], tracks[j]
		if x.Medium != y.Medium {
			return x.Medium < y.Medium
		}
		if x.Number != y.Number {
			return x.Number < y.Number
		}
		return x.AbsoluteNumber < y.AbsoluteNumber
	})
	return tracks
}

// trackRows are the Tracks table's rows, in disc and number order, each
// with its file's cells when one is attributed to it; discs is whether the
// release has more than one medium.
func trackRows(a *catalogv1.Album, files map[string]*catalogv1.MediaFile) ([]views.TrackRow, bool) {
	tracks := sortedTracks(a)
	media := map[int32]bool{}
	rows := make([]views.TrackRow, 0, len(tracks))
	for _, t := range tracks {
		media[t.Medium] = true
		row := views.TrackRow{Medium: t.Medium, Number: t.Number, Title: t.Title, Explicit: t.Explicit}
		if row.Number == 0 {
			row.Number = t.AbsoluteNumber
		}
		if t.DurationMs > 0 {
			row.Duration = clock(int64(t.DurationMs))
		}
		if t.FileRef != nil {
			if f := files[*t.FileRef]; f != nil {
				r, _ := fileRows(a.Status.Path, f)
				row.File = &r[0]
			}
		}
		rows = append(rows, row)
	}
	return rows, len(media) > 1
}

// durationLabel is the tracks' lengths summed as the hero says it: "47m",
// "1h 12m"; "" when no track has a length.
func durationLabel(tracks []catalogv1.Track) string {
	var ms int64
	for _, t := range tracks {
		ms += int64(t.DurationMs)
	}
	if ms <= 0 {
		return ""
	}
	minutes := (ms + 30_000) / 60_000
	if minutes < 1 {
		minutes = 1
	}
	return runtimeLabel(int32(minutes)) //nolint:gosec // 200 tracks fit
}

// replyAlbumRow answers an htmx album toggle with the artist page's row
// re-rendered, replyBookRow's counterpart: from the Album the patch
// returned on success, or from the Album as it still stands with the
// failure inside it. handleSetMonitored's album case is to call it (W4c);
// until then an album toggle answers the children component's row.
func (s *Server) replyAlbumRow(w http.ResponseWriter, r *http.Request, ns, name string, patched client.Object, err error) {
	var a *catalogv1.Album
	if got, ok := patched.(*catalogv1.Album); ok && err == nil {
		a = got
	} else {
		var current catalogv1.Album
		if s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &current) {
			a = &current
		}
	}
	if a == nil {
		a = &catalogv1.Album{}
		a.Namespace, a.Name = ns, name
	}
	row := albumTableRow(a, time.Now())
	if err != nil {
		row.Error = failureOf(r, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rerr := views.AlbumRowView(row).Render(r.Context(), w); rerr != nil {
		logging.FromContext(r.Context()).Error("render album row", "error", rerr)
	}
}

// albumNeighbours are the pages of the albums either side of a in its
// artist's catalogue, oldest first, an undated album last, then by title.
func (s *Server) albumNeighbours(ctx context.Context, a *catalogv1.Album) (prev, next string) {
	albums, err := s.listAlbums(ctx, a.Namespace, a.Spec.ArtistRef)
	if err != nil {
		logging.FromContext(ctx).Error("list albums for an album's neighbours", "error", err)
		return "", ""
	}
	type ordered struct{ name, date, title string }
	order := make([]ordered, 0, len(albums))
	for i := range albums {
		o := ordered{name: albums[i].Name, title: albums[i].Name}
		if md := albums[i].Status.Metadata; md != nil {
			if md.Title != "" {
				o.title = md.Title
			}
			if md.ReleaseDate != nil {
				o.date = md.ReleaseDate.UTC().Format(time.DateOnly)
			}
		}
		order = append(order, o)
	}
	sort.SliceStable(order, func(i, j int) bool {
		x, y := order[i], order[j]
		switch {
		case x.date == "" && y.date != "":
			return false
		case x.date != "" && y.date == "":
			return true
		case x.date != y.date:
			return x.date < y.date
		}
		return x.title < y.title
	})
	url := func(name string) string { return views.AlbumRow{Namespace: a.Namespace, Name: name}.URL() }
	for i := range order {
		if order[i].name != a.Name {
			continue
		}
		if i > 0 {
			prev = url(order[i-1].name)
		}
		if i+1 < len(order) {
			next = url(order[i+1].name)
		}
		break
	}
	return prev, next
}
