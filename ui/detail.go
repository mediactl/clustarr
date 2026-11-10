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
	"slices"
	"sort"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
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
	if views.Editable(d) {
		for _, qp := range s.listQualityProfiles(ctx) {
			d.Profiles = append(d.Profiles, views.AddOption{Value: qp.Name, Label: qp.Name})
		}
	}
	// known is the files a container's rollup counts, read off the object
	// the switch fetches anyway, which bounds containerFiles below.
	var known int32
	switch item.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1.Movie
		if s.getObject(ctx, item.Ref, &m) {
			d.MinimumAvailability = string(m.Spec.MinimumAvailability)
			fillMovie(&d, &m)
			d.ImageSet = imageSetNote(m.Spec.Artwork, nil, m.Status.ImageSet)
			d.ImageSetAuto, d.ImageSetPinned = m.Spec.ImageSet != catalogv1.ImageSetNone, m.Spec.ImageSetID
			d.Audio = audioNote(m.Status.Audio)
			if ref := m.Status.FileRef; ref != nil && *ref != "" {
				var f catalogv1.MediaFile
				if s.getObject(ctx, types.NamespacedName{Namespace: item.Ref.Namespace, Name: *ref}, &f) {
					d.Files, d.Extras = fileRows(m.Status.Path, &f)
					d.SubtitlesPhase, d.Subtitles = subtitleRows(&f)
				}
			}
		}
	case commonv1.MediaKindSeries:
		var sr catalogv1.Series
		if s.getObject(ctx, item.Ref, &sr) {
			d.SeriesType = string(sr.Spec.SeriesType)
			known = sr.Status.EpisodeFileCount
			fillSeries(&d, &sr)
			d.ImageSet = imageSetNote(sr.Spec.Artwork, sr.Spec.SeasonArtwork, sr.Status.ImageSet)
			d.ImageSetAuto, d.ImageSetPinned = sr.Spec.ImageSet != catalogv1.ImageSetNone, sr.Spec.ImageSetID
		}
	case commonv1.MediaKindArtist:
		var a catalogv1.Artist
		if s.getObject(ctx, item.Ref, &a) {
			d.Path = a.Status.Path
			known = a.Status.AlbumFileCount // albums with a file: a floor on the track files
			if md := a.Status.Metadata; md != nil {
				d.Overview, d.Genres = md.Overview, md.Genres
			}
			d.Backdrop = backdropArt(commonv1.MediaKindArtist, a.GetUID(), a.Status.Artwork)
		}
	case commonv1.MediaKindAuthor:
		var a catalogv1.Author
		if s.getObject(ctx, item.Ref, &a) {
			d.Path = a.Status.Path
			known = a.Status.BookFileCount
			if md := a.Status.Metadata; md != nil {
				d.Overview, d.Genres = md.Overview, md.Genres
			}
			d.Backdrop = backdropArt(commonv1.MediaKindAuthor, a.GetUID(), a.Status.Artwork)
		}
	case commonv1.MediaKindComic:
		var c catalogv1.Comic
		if s.getObject(ctx, item.Ref, &c) {
			d.Path = c.Status.Path
			known = c.Status.IssueFileCount
		}
	}
	// A container's files, for its Delete dialog and its Size fact (the
	// comic page fills the rest of its Detail itself), bounded by
	// maxContainerFiles.
	if projection.IsContainer(item.Kind) || item.Kind == commonv1.MediaKindSeries {
		d.Files = s.containerFiles(ctx, item, d.Path, known)
	}
	return d
}

// hasFolder reports whether a kind has a folder of its own under a
// RootFolder (status.path), which "Refresh & Scan" can rescan: the video
// kinds, the containers and, since 2026-11-10 (design §9.1), a Comic and
// an Audiobook. An Album's folder is its Artist's; a Book's is its
// Author's unless it is standalone, which the kind alone cannot tell, so
// a Book reads bookHasFolder.
func hasFolder(kind commonv1.MediaKind) bool {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAuthor,
		commonv1.MediaKindComic, commonv1.MediaKindAudiobook:
		return true
	default:
		return false
	}
}

// bookHasFolder reports whether b has a folder of its own: a standalone
// Book (2026-11-10 design §9.3) -- no spec.authorRef -- whose
// spec.rootFolderRef names its root and whose folder under it "Refresh &
// Scan" and Rename work on (itemFolder), and whose profile and root the
// Edit dialog edits (views.Editable). An author's book lives in its
// author's folder and has none of the three, even when its own
// spec.rootFolderRef overrides the author's root (which the CRD allows):
// its status.path is then the author's folder under that root, and a scan
// or a rename from its page would work the author's whole folder.
func bookHasFolder(b *catalogv1.Book) bool {
	return (b.Spec.AuthorRef == nil || *b.Spec.AuthorRef == "") &&
		b.Spec.RootFolderRef != nil && *b.Spec.RootFolderRef != ""
}

// bookFileDir is the directory of the book's file on disk (status.fileRef's
// MediaFile), "" without one the reader holds.
func (s *Server) bookFileDir(ctx context.Context, b *catalogv1.Book) string {
	if b.Status.FileRef == nil || *b.Status.FileRef == "" {
		return ""
	}
	var mf catalogv1.MediaFile
	if !s.getObject(ctx, types.NamespacedName{Namespace: b.Namespace, Name: *b.Status.FileRef}, &mf) || mf.Spec.Path == "" {
		return ""
	}
	return filepath.Dir(mf.Spec.Path)
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
		d.Links = append(d.Links, views.Link{Label: "TMDB", Href: fmt.Sprintf("https://www.themoviedb.org/movie/%d", m.Spec.TmdbID), Provider: views.ProviderTMDB})
	}
	md := m.Status.Metadata
	if md == nil {
		return
	}
	d.Certification, d.Runtime, d.Overview, d.Genres = md.Certification, runtimeLabel(md.RuntimeMinutes), md.Overview, md.Genres
	d.Language = languageName(md.OriginalLanguage)
	d.Backdrop = backdropArt(commonv1.MediaKindMovie, m.GetUID(), m.Status.Artwork)
	d.AltTitles = md.AlternateTitles
	if id := md.ExternalIDs[commonv1.IDKeyIMDB]; id != "" {
		d.Links = append(d.Links, views.Link{Label: "IMDb", Href: "https://www.imdb.com/title/" + id + "/", Provider: views.ProviderIMDb})
	}
}

func fillSeries(d *views.Detail, sr *catalogv1.Series) {
	d.Path = sr.Status.Path
	if sr.Spec.TvdbID > 0 {
		d.Links = append(d.Links, views.Link{Label: "TVDB", Href: fmt.Sprintf("https://thetvdb.com/dereferrer/series/%d", sr.Spec.TvdbID), Provider: views.ProviderTVDB})
	}
	md := sr.Status.Metadata
	if md == nil {
		return
	}
	d.Certification, d.Runtime, d.Overview, d.Genres = md.Certification, runtimeLabel(md.RuntimeMinutes), md.Overview, md.Genres
	d.Language, d.Network = languageName(md.OriginalLanguage), md.Network
	d.Backdrop = backdropArt(commonv1.MediaKindSeries, sr.GetUID(), sr.Status.Artwork)
	for _, t := range md.AlternateTitles {
		d.AltTitles = append(d.AltTitles, t.Title)
	}
	if id := md.ExternalIDs[commonv1.IDKeyIMDB]; id != "" {
		d.Links = append(d.Links, views.Link{Label: "IMDb", Href: "https://www.imdb.com/title/" + id + "/", Provider: views.ProviderIMDb})
	}
}

// imageSetNote is the Images fact: the owner's pasted set when spec names
// any override (it wins wherever both are read), else the mediux
// provider's anchor set and how many more sets filled what it lacks, else
// none.
func imageSetNote(artwork []catalogv1.ArtworkOverride, seasons []catalogv1.SeasonArtworkOverride, set *catalogv1.ImageSetStatus) *views.ImageSetNote {
	if len(artwork) > 0 || len(seasons) > 0 {
		return &views.ImageSetNote{Label: "pasted set", Pasted: true}
	}
	if set == nil || set.NotFound || set.SetID == "" {
		return nil
	}
	label := "MediUX set"
	if set.Title != "" {
		label += " " + set.Title
	}
	if set.Creator != "" {
		label += " by " + set.Creator
	}
	switch more := len(set.Sets) - 1; {
	case more == 1:
		label += " and 1 more set"
	case more > 1:
		label += fmt.Sprintf(" and %d more sets", more)
	}
	return &views.ImageSetNote{Label: label, Href: imageSetHref(set)}
}

// imageSetHref is the set's page. A mediux set's is built from its id
// (metadata.MediUXSetURL; the ui links no provider client, test/guards' R6),
// never read from status.imageSet.url: until 2026-11-04
// the agent recorded AURA's https://mediux.io/<kind>-set/<id>, which
// mediux.io no longer serves (a Cloudflare tunnel error; owner), and an
// item keeps that link until the stage next reconsiders it. Any other
// provider's link is the stored one.
func imageSetHref(set *catalogv1.ImageSetStatus) string {
	if set.Provider == "mediux" || set.Provider == "" {
		return metadata.MediUXSetURL(set.SetID)
	}
	return set.URL
}

// backdropArt is a detail page's Backdrop: the [projection.ArtURL] of the
// fanart entry in status.artwork, or "" when none has been fetched yet. It
// never reads status.metadata.images directly -- every image this ui ever
// links to comes from the object store through GET /art, not a provider's
// own URL (ADR-0011).
func backdropArt(kind commonv1.MediaKind, uid types.UID, artwork []catalogv1.ArtworkEntry) string {
	for _, a := range artwork {
		if a.Type == catalogv1.ImageTypeFanart {
			return projection.ArtURL(kind, uid, catalogv1.ImageTypeFanart, a.Digest)
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

// fileRows turns an item's MediaFile into its Files row and its extra
// files, paths relative to the item's folder as Radarr shows them. The row
// carries every kind's cells (views.FileRow, 2026-11-10 design §9.6): the
// video codec and first audio track of a video, the first track's codec,
// bitrate, sample rate and bit depth of an album's or audiobook's file, the
// format and page count of a book's or issue's; its Kind, the file's
// mediaRef kind, picks which the table shows.
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
		Name: f.Name, Kind: f.Spec.MediaRef.Kind, Path: f.Spec.Path, RelativePath: rel(f.Spec.Path), SizeBytes: f.Spec.SizeBytes,
		Languages: f.Spec.Languages, Quality: f.Spec.Quality.Name, ReleaseGroup: f.Spec.ReleaseGroup,
		Formats: f.Spec.MatchedFormats, Score: f.Spec.FormatScore,
		Format: fileFormat(f),
	}
	if mi := f.Status.MediaInfo; mi != nil {
		row.VideoCodec = mi.VideoCodec
		row.Pages = mi.PageCount
		if len(mi.Audio) > 0 {
			a := mi.Audio[0]
			row.Audio = audioLabel(a)
			row.AudioCodec = strings.ToUpper(a.Codec)
			row.BitrateKbps, row.SampleRateHz, row.BitDepth = a.BitrateKbps, a.SampleRateHz, a.BitDepth
		}
	}
	row.Markers = markersLabel(f.Status.Markers)
	if t := f.Status.Transcode; t != nil {
		row.Transcode = string(t.Phase)
		if t.Reason != "" {
			row.Transcode += " " + t.Reason
		}
	}
	var extras []views.ExtraRow
	for _, sc := range f.Status.Sidecars {
		p, ok := sc.PathBeside(f.Spec.Path)
		if !ok {
			continue
		}
		e := views.ExtraRow{Path: rel(p), Kind: "Subtitle", Language: sc.Language}
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

// fileFormat is a book's or an issue's format as the quality ladder names
// it (the frozen quality, EPUB or CBZ), else the file's extension upper-cased
// (2026-11-10 design D5: fileFormat is "the extension as the quality ladder
// names it").
func fileFormat(f *catalogv1.MediaFile) string {
	if f.Spec.Quality.Name != "" {
		return f.Spec.Quality.Name
	}
	ext := strings.TrimPrefix(filepath.Ext(f.Spec.Path), ".")
	return strings.ToUpper(ext)
}

// subtitleRows is the file's status.subtitles: its phase, and one row per
// language (loop spec §6.9). A file with no block yet has neither.
func subtitleRows(f *catalogv1.MediaFile) (string, []views.SubtitleRow) {
	st := f.Status.Subtitles
	if st == nil {
		return "", nil
	}
	rows := make([]views.SubtitleRow, 0, len(st.Items))
	for _, it := range st.Items {
		r := views.SubtitleRow{LangKey: it.LangKey, State: string(it.State), Provider: it.Provider, LastError: it.LastError}
		if it.ScoreOutOf > 0 {
			r.Score = fmt.Sprintf("%d/%d", it.Score, it.ScoreOutOf)
		}
		if it.NextSearchAt != nil {
			r.NextSearch = it.NextSearchAt.UTC().Format("2006-01-02 15:04")
		}
		rows = append(rows, r)
	}
	return string(st.Phase), rows
}

// audioLabel is Radarr's "DTS - 5.1": the codec and the channel layout,
// ffprobe's "stereo" and "mono" written as 2.0 and 1.0 and a layout's
// variant ("5.1(side)") dropped.
func audioLabel(a commonv1.AudioStream) string {
	codec := strings.ToUpper(a.Codec)
	layout := a.ChannelLayout
	if i := strings.Index(layout, "("); i >= 0 {
		layout = layout[:i]
	}
	switch layout {
	case "stereo":
		layout = "2.0"
	case "mono":
		layout = "1.0"
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
	// fileDir, for a standalone book, is the folder its file is in, read
	// only when status.path is no folder under the root (below).
	var fileDir func() string
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
	case commonv1.MediaKindComic:
		var c catalogv1.Comic
		if s.getObject(ctx, ref, &c) {
			itemPath, rootRef = c.Status.Path, c.Spec.RootFolderRef
		}
	case commonv1.MediaKindAudiobook:
		var a catalogv1.Audiobook
		if s.getObject(ctx, ref, &a) {
			itemPath, rootRef = a.Status.Path, a.Spec.RootFolderRef
		}
	case commonv1.MediaKindBook:
		// A standalone book's own root and folder (bookHasFolder); an
		// author's book has no folder of its own.
		var b catalogv1.Book
		if s.getObject(ctx, ref, &b) && bookHasFolder(&b) {
			itemPath, rootRef = b.Status.Path, *b.Spec.RootFolderRef
			fileDir = func() string { return s.bookFileDir(ctx, &b) }
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
	if (err != nil || rel == ".") && fileDir != nil {
		// The Book reconciler writes a standalone book's status.path as
		// its root's own path (app/catalog/controller/book.Path renders
		// the author folder, "{Author Name}", which is "" without an
		// author), while the import places its file at <root>/{Book
		// Title}/<file> (catalogctx.BookFilePath): the folder is the
		// file's, and a standalone book without a file has none on disk
		// yet.
		if dir := fileDir(); dir != "" {
			rel, err = filepath.Rel(rf.Spec.Path, dir)
		}
	}
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	return rootRef, rel, true
}

// markersLabel lists a file's skip segments with where each came from:
// "intro 3:48–4:06 (TheIntroDB), credits 57:11–58:00 (detected)"; "none
// found" when TheIntroDB or clustarr's own analysis looked and found none;
// "" before either has looked.
func markersLabel(m *catalogv1.FileMarkers) string {
	if m == nil {
		return ""
	}
	if len(m.Segments) > 0 {
		parts := make([]string, 0, len(m.Segments))
		for _, s := range m.Segments {
			parts = append(parts, string(s.Kind)+" "+clock(s.StartMs)+"–"+clock(s.EndMs)+" ("+sourceLabel(s.Source)+")")
		}
		return strings.Join(parts, ", ")
	}
	if m.Result == catalogv1.MarkersError {
		return "TheIntroDB unavailable"
	}
	if m.Result == catalogv1.MarkersNotFound || (m.Analysis != nil && m.Analysis.Result == catalogv1.MarkersNotFound) {
		return "none found"
	}
	return ""
}

// sourceLabel names a segment's source for the files table; an untagged
// segment predates sources and is TheIntroDB's.
func sourceLabel(s catalogv1.SegmentSource) string {
	switch s {
	case catalogv1.SegmentSourceChapters:
		return "chapters"
	case catalogv1.SegmentSourceAnalysis:
		return "detected"
	}
	return "TheIntroDB"
}

// clock formats milliseconds as m:ss, or h:mm:ss from an hour.
func clock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// maxContainerFiles bounds what containerFiles reads for one page: a
// container's page (and its hover preload) lists its children through the
// reader's index and then Gets each file its children name from the cache,
// one copy each, so a 1,100-episode series would cost 1,100 Episode and
// 1,100 MediaFile copies a render. Up to this many files the rows are read
// in full (BenchmarkContainerFiles, over the fake reader, which decodes
// JSON per Get: a series page renders in 3.95 ms at 50 files, 12.7 ms at
// 500 and 2.9 ms at 1,100 with nothing read, 2026-11-10; the ui's cache
// deep-copies instead, cheaper); above it nothing is read -- the rollup's count
// says so before any List when it can -- and the page shows no Size fact,
// while its Delete dialog counts from the rollup where one is at hand (a
// series' status.episodeFileCount and its seasons' sizes; deleteDialog).
// The owner's largest series (Pokemon, 1,457 episodes; One Piece) are over
// it, nearly every other container under.
const maxContainerFiles = 500

// containerFiles lists a container's files -- a series' episodes', an
// artist's albums', an author's books', a comic's issues' -- for its
// page's Delete dialog (the count and size the delete removes with the
// folder; 2026-11-10 design §9.6) and its Size fact: the children through
// the reader's parent indexes (never a namespace List filtered in
// memory), then each file its status names, one cached Get each, as
// rows relative to folder. known is the files the container's rollup
// counts (an artist's a floor, its albums with a file), which skips the
// List outright when it is over maxContainerFiles; more file refs than
// that skip the Gets. A kind that is no container, or no reader, has
// none.
func (s *Server) containerFiles(ctx context.Context, item projection.LibraryItem, folder string, known int32) []views.FileRow {
	if s.opts.Reader == nil || known > maxContainerFiles {
		return nil
	}
	refs := s.childFileRefs(ctx, item)
	if len(refs) == 0 || len(refs) > maxContainerFiles {
		return nil
	}
	rows := make([]views.FileRow, 0, len(refs))
	for _, ref := range refs {
		var f catalogv1.MediaFile
		if !s.getObject(ctx, types.NamespacedName{Namespace: item.Ref.Namespace, Name: ref}, &f) {
			continue
		}
		fileRows, _ := fileRows(folder, &f)
		rows = append(rows, fileRows...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows
}

// childFileRefs is the names of the MediaFiles a container's children
// hold, each once, in a stable order.
func (s *Server) childFileRefs(ctx context.Context, item projection.LibraryItem) []string {
	ns, name := item.Ref.Namespace, item.Ref.Name
	var refs []string
	add := func(ref *string) {
		if ref != nil && *ref != "" && !slices.Contains(refs, *ref) {
			refs = append(refs, *ref)
		}
	}
	switch item.Kind {
	case commonv1.MediaKindSeries:
		var list catalogv1.EpisodeList
		if s.listChildren(ctx, &list, ns, EpisodeSeriesRefField, name) {
			for i := range list.Items {
				add(list.Items[i].Status.FileRef)
			}
		}
	case commonv1.MediaKindArtist:
		var list catalogv1.AlbumList
		if s.listChildren(ctx, &list, ns, AlbumArtistRefField, name) {
			for i := range list.Items {
				for _, t := range list.Items[i].Status.Tracks {
					add(t.FileRef)
				}
			}
		}
	case commonv1.MediaKindAuthor:
		var list catalogv1.BookList
		if s.listChildren(ctx, &list, ns, BookAuthorRefField, name) {
			for i := range list.Items {
				add(list.Items[i].Status.FileRef)
			}
		}
	case commonv1.MediaKindComic:
		var list catalogv1.IssueList
		if s.listChildren(ctx, &list, ns, IssueComicRefField, name) {
			for i := range list.Items {
				add(list.Items[i].Status.FileRef)
			}
		}
	}
	sort.Strings(refs)
	return refs
}

// listChildren lists a parent's children into list through the reader's
// field index, reporting whether it could; a failure (a reader without
// the index, as a test's fake may be) is logged and reads as none.
func (s *Server) listChildren(ctx context.Context, list client.ObjectList, ns, field, parent string) bool {
	if err := s.opts.Reader.List(ctx, list, client.InNamespace(ns), client.MatchingFields{field: parent}); err != nil {
		logging.FromContext(ctx).Warn("list a container's children for its page", "namespace", ns, "field", field, "parent", parent, "error", err)
		return false
	}
	return true
}
