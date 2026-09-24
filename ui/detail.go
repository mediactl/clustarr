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
	"path/filepath"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An item's page (design 2026-09-24, after Radarr's movie page) is filled
// from what the cluster already holds: the projection's item and its
// neighbours on the tab, and -- through the reader -- the item's gathered
// metadata and, for a movie, its MediaFile. Nothing here fetches from a
// provider, and nothing here writes.

// itemDetail builds views.Detail for item. With no reader (no cluster) the
// page shows the projection's item alone.
func (s *Server) itemDetail(ctx context.Context, item projection.LibraryItem) views.Detail {
	d := views.Detail{Item: item, CanScan: hasFolder(item.Kind), ShowFiles: item.Kind == commonv1.MediaKindMovie}
	d.Prev, d.Next = neighbours(s.opts.Library(ctx), item)
	if s.opts.Reader == nil {
		return d
	}
	switch item.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1.Movie
		if s.getObject(ctx, item.Ref, &m) {
			fillMovie(&d, &m)
			if ref := m.Status.FileRef; ref != nil && *ref != "" {
				var f catalogv1.MediaFile
				if s.getObject(ctx, types.NamespacedName{Namespace: item.Ref.Namespace, Name: *ref}, &f) {
					d.Files, d.Extras = fileRows(m.Status.Path, &f)
				}
			}
		}
	case commonv1.MediaKindSeries:
		var sr catalogv1.Series
		if s.getObject(ctx, item.Ref, &sr) {
			fillSeries(&d, &sr)
		}
	case commonv1.MediaKindArtist:
		var a catalogv1.Artist
		if s.getObject(ctx, item.Ref, &a) {
			d.Path = a.Status.Path
			if md := a.Status.Metadata; md != nil {
				d.Overview, d.Genres, d.Backdrop = md.Overview, md.Genres, imageOf(md.Images, catalogv1.ImageTypeFanart)
			}
		}
	case commonv1.MediaKindAuthor:
		var a catalogv1.Author
		if s.getObject(ctx, item.Ref, &a) {
			d.Path = a.Status.Path
			if md := a.Status.Metadata; md != nil {
				d.Overview, d.Genres, d.Backdrop = md.Overview, md.Genres, imageOf(md.Images, catalogv1.ImageTypeFanart)
			}
		}
	}
	return d
}

// hasFolder reports whether a kind has a folder of its own under a
// RootFolder (status.path), which "Refresh & Scan" can rescan.
func hasFolder(kind commonv1.MediaKind) bool {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAuthor:
		return true
	default:
		return false
	}
}

// getObject reads one object through the reader, reporting whether it was
// there; an error other than not-found is logged.
func (s *Server) getObject(ctx context.Context, ref types.NamespacedName, obj client.Object) bool {
	if s.opts.Reader == nil {
		return false
	}
	if err := s.opts.Reader.Get(ctx, ref, obj); err != nil {
		if !apierrors.IsNotFound(err) {
			logging.FromContext(ctx).Error("get item for its page", "ref", ref.String(), "error", err)
		}
		return false
	}
	return true
}

func fillMovie(d *views.Detail, m *catalogv1.Movie) {
	d.Path = m.Status.Path
	if m.Spec.TmdbID > 0 {
		d.Links = append(d.Links, views.Link{Label: "TMDB", Href: fmt.Sprintf("https://www.themoviedb.org/movie/%d", m.Spec.TmdbID)})
	}
	md := m.Status.Metadata
	if md == nil {
		return
	}
	d.Certification, d.Runtime, d.Overview, d.Genres = md.Certification, runtimeLabel(md.RuntimeMinutes), md.Overview, md.Genres
	d.Language = languageName(md.OriginalLanguage)
	d.Backdrop = imageOf(md.Images, catalogv1.ImageTypeFanart)
	d.AltTitles = md.AlternateTitles
	if id := md.ExternalIDs[commonv1.IDKeyIMDB]; id != "" {
		d.Links = append(d.Links, views.Link{Label: "IMDb", Href: "https://www.imdb.com/title/" + id + "/"})
	}
}

func fillSeries(d *views.Detail, sr *catalogv1.Series) {
	d.Path = sr.Status.Path
	if sr.Spec.TvdbID > 0 {
		d.Links = append(d.Links, views.Link{Label: "TVDB", Href: fmt.Sprintf("https://thetvdb.com/dereferrer/series/%d", sr.Spec.TvdbID)})
	}
	md := sr.Status.Metadata
	if md == nil {
		return
	}
	d.Certification, d.Runtime, d.Overview, d.Genres = md.Certification, runtimeLabel(md.RuntimeMinutes), md.Overview, md.Genres
	d.Language, d.Network = languageName(md.OriginalLanguage), md.Network
	d.Backdrop = imageOf(md.Images, catalogv1.ImageTypeFanart)
	for _, t := range md.AlternateTitles {
		d.AltTitles = append(d.AltTitles, t.Title)
	}
	if id := md.ExternalIDs[commonv1.IDKeyIMDB]; id != "" {
		d.Links = append(d.Links, views.Link{Label: "IMDb", Href: "https://www.imdb.com/title/" + id + "/"})
	}
}

// imageOf is the first image of the type, or "".
func imageOf(images []catalogv1.Image, t catalogv1.ImageType) string {
	for _, img := range images {
		if img.Type == t {
			return img.URL
		}
	}
	return ""
}

// runtimeLabel is Radarr's "1h 36m"; "" for no runtime.
func runtimeLabel(minutes int32) string {
	if minutes <= 0 {
		return ""
	}
	h, m := minutes/60, minutes%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh %dm", h, m)
	}
}

// languageName is the language's English name ("en" reads English); a
// code x/text does not know is shown as it is.
func languageName(code string) string {
	if code == "" {
		return ""
	}
	tag, err := language.Parse(code)
	if err != nil {
		return code
	}
	if name := display.English.Languages().Name(tag); name != "" {
		return name
	}
	return code
}

// neighbours are the items either side of item on its tab, in the tab's
// order, as page paths; "" at either end.
func neighbours(all []projection.LibraryItem, item projection.LibraryItem) (prev, next string) {
	tab := projection.ForTab(all, item.Tab)
	for i, it := range tab {
		if it.Ref != item.Ref || it.Kind != item.Kind {
			continue
		}
		if i > 0 {
			prev = views.DetailPath(tab[i-1])
		}
		if i+1 < len(tab) {
			next = views.DetailPath(tab[i+1])
		}
		return prev, next
	}
	return "", ""
}

// fileRows turns a movie's MediaFile into its Files row and its extra
// files, paths relative to the movie's folder as Radarr shows them.
func fileRows(folder string, f *catalogv1.MediaFile) ([]views.FileRow, []views.ExtraRow) {
	rel := func(p string) string {
		if folder != "" {
			if r, ok := strings.CutPrefix(p, folder+"/"); ok {
				return r
			}
		}
		return filepath.Base(p)
	}
	row := views.FileRow{
		Name: f.Name, RelativePath: rel(f.Spec.Path), SizeBytes: f.Spec.SizeBytes,
		Languages: f.Spec.Languages, Quality: f.Spec.Quality.Name, ReleaseGroup: f.Spec.ReleaseGroup,
		Formats: f.Spec.MatchedFormats, Score: f.Spec.FormatScore,
	}
	if mi := f.Status.MediaInfo; mi != nil {
		row.VideoCodec = mi.VideoCodec
		if len(mi.Audio) > 0 {
			row.Audio = audioLabel(mi.Audio[0])
		}
	}
	var extras []views.ExtraRow
	for _, sc := range f.Status.Sidecars {
		e := views.ExtraRow{Path: rel(sc.Path), Kind: "Subtitle", Language: sc.Language}
		if sc.Forced {
			e.Language += " (forced)"
		}
		if sc.HI {
			e.Language += " (HI)"
		}
		extras = append(extras, e)
	}
	return []views.FileRow{row}, extras
}

// audioLabel is Radarr's "DTS - 5.1": the codec and the channel layout.
func audioLabel(a commonv1.AudioStream) string {
	codec := strings.ToUpper(a.Codec)
	layout := a.ChannelLayout
	if i := strings.Index(layout, "("); i >= 0 {
		layout = layout[:i]
	}
	if layout == "" {
		switch a.Channels {
		case 0:
			return codec
		case 1:
			layout = "1.0"
		case 2:
			layout = "2.0"
		case 6:
			layout = "5.1"
		case 8:
			layout = "7.1"
		default:
			layout = fmt.Sprintf("%d ch", a.Channels)
		}
	}
	if codec == "" {
		return layout
	}
	return codec + " - " + layout
}

// itemFolder is the RootFolder an item lives under and its folder
// relative to it, for "Refresh & Scan": false for a kind with no folder,
// an item with nothing on disk yet, or a path outside its RootFolder.
func (s *Server) itemFolder(ctx context.Context, namespace string, kind commonv1.MediaKind, name string) (rootFolder, subpath string, ok bool) {
	ref := types.NamespacedName{Namespace: namespace, Name: name}
	var itemPath, rootRef string
	switch kind {
	case commonv1.MediaKindMovie:
		var m catalogv1.Movie
		if s.getObject(ctx, ref, &m) {
			itemPath, rootRef = m.Status.Path, m.Spec.RootFolderRef
		}
	case commonv1.MediaKindSeries:
		var sr catalogv1.Series
		if s.getObject(ctx, ref, &sr) {
			itemPath, rootRef = sr.Status.Path, sr.Spec.RootFolderRef
		}
	case commonv1.MediaKindArtist:
		var a catalogv1.Artist
		if s.getObject(ctx, ref, &a) {
			itemPath, rootRef = a.Status.Path, a.Spec.RootFolderRef
		}
	case commonv1.MediaKindAuthor:
		var a catalogv1.Author
		if s.getObject(ctx, ref, &a) {
			itemPath, rootRef = a.Status.Path, a.Spec.RootFolderRef
		}
	}
	if itemPath == "" || rootRef == "" {
		return "", "", false
	}
	var rf catalogv1.RootFolder
	if !s.getObject(ctx, types.NamespacedName{Namespace: namespace, Name: rootRef}, &rf) || rf.Spec.Path == "" {
		return "", "", false
	}
	rel, err := filepath.Rel(rf.Spec.Path, itemPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	return rootRef, rel, true
}
