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
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// An audiobook's page (2026-11-10 design §9.5) is filled as the movie page
// is, from what the cluster already holds: the Audiobook, its library
// card, the Book its bookRef names (for the Open Library logo) and the
// MediaFiles its status.fileRefs name, read one by one through the cache
// (a Get per part, never a List). Nothing here fetches from a provider,
// and nothing here writes.

// handleAudiobookPage serves GET /library/{namespace}/audiobook/{name}, the
// page an audiobook's card opens (and preloads). An audiobook Add New just
// created is in the cluster before the library view lists it, and is told
// to wait rather than 404.
func (s *Server) handleAudiobookPage(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	v, ok := s.audiobookView(r.Context(), ns, name)
	if !ok {
		s.notFoundUnlessJustAdded(w, r, ns, string(commonv1.MediaKindAudiobook), name)
		return
	}
	preloadable(w)
	s.renderAudiobook(w, r, v, false)
}

// handleAudiobookDetails serves GET /library/{namespace}/audiobook/{name}/details:
// the audiobook's modal to htmx, on the Details tab or the Search tab with
// ?tab=search; the audiobook's page to anyone else.
func (s *Server) handleAudiobookDetails(w http.ResponseWriter, r *http.Request) {
	v, ok := s.audiobookView(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("tab") == "search" {
		v.Tab = "search"
	}
	s.renderAudiobook(w, r, v, isHTMX(r))
}

// renderAudiobook answers the audiobook's modal when modal is set, else
// its page.
func (s *Server) renderAudiobook(w http.ResponseWriter, r *http.Request, v views.AudiobookView, modal bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := views.AudiobookPage(v)
	if modal {
		view = views.AudiobookModal(v)
	}
	if err := view.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render audiobook", "error", err)
	}
}

// audiobookView builds an audiobook's page and modal: its card, metadata,
// provider links, status, parts, chapters and files, and the audiobooks
// either side of it on the tab. False when the audiobook, its card or the
// reader is missing.
func (s *Server) audiobookView(ctx context.Context, ns, name string) (views.AudiobookView, bool) {
	var a catalogv1.Audiobook
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &a) {
		return views.AudiobookView{}, false
	}
	item, ok := findLibraryItem(s.opts.Library(ctx), ns, string(commonv1.MediaKindAudiobook), name)
	if !ok {
		return views.AudiobookView{}, false
	}
	now := time.Now()
	v := views.AudiobookView{
		Detail:       views.Detail{Item: item, CanScan: hasFolder(item.Kind), HasFolder: hasFolder(item.Kind), ShowFiles: true, Path: a.Status.Path},
		CutoffReason: a.Status.CutoffReason,
		Phase:        string(a.Status.Phase),
		PartCount:    a.Status.PartCount,
		PartRefs:     len(a.Status.FileRefs),
		Tab:          "details",
	}
	v.Prev, v.Next = neighbours(s.opts.Library(ctx), item)
	v.Status = audiobookStatus(&a, item.Monitored, now)
	if q := a.Status.FileQuality; q != nil && a.Status.HasFile {
		v.Quality = q.Name
	}
	if views.Editable(v.Detail) {
		for _, qp := range s.listQualityProfiles(ctx) {
			v.Profiles = append(v.Profiles, views.AddOption{Value: qp.Name, Label: qp.Name})
		}
	}
	v.Backdrop = backdropArt(commonv1.MediaKindAudiobook, a.GetUID(), a.Status.Artwork)
	if ref := a.Spec.AuthorRef; ref != nil && *ref != "" {
		v.AuthorHref = "/library/" + ns + "/author/" + url.PathEscape(*ref)
	}
	var book *catalogv1.Book
	if ref := a.Spec.BookRef; ref != nil && *ref != "" {
		var b catalogv1.Book
		if s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: *ref}, &b) {
			book = &b
		}
	}
	v.Links = audiobookLinks(&a, book)

	files := s.audiobookFiles(ctx, &a)
	for _, f := range files {
		rows, _ := fileRows(a.Status.Path, f)
		v.Files = append(v.Files, rows...)
	}

	var chapters []catalogv1.Chapter
	if md := a.Status.Metadata; md != nil {
		v.HasMetadata = true
		v.Subtitle, v.Publisher, v.Abridged = md.Subtitle, md.Publisher, md.Abridged
		v.Overview, v.Genres = md.Overview, md.Genres
		v.Language = languageName(md.Language)
		for _, author := range md.Authors {
			if author.Name != "" {
				v.Authors = append(v.Authors, author.Name)
			}
		}
		v.Narrators = md.Narrators
		if md.Series != nil {
			v.Series = bookSeries([]catalogv1.SeriesLink{*md.Series})
		}
		if md.ReleaseDate != nil {
			v.ReleaseDate = md.ReleaseDate.UTC().Format(time.DateOnly)
			v.ReleaseDateLabel = relativeDate(md.ReleaseDate.UTC(), now)
		}
		v.RuntimeMinutes = md.RuntimeMinutes
		chapters = md.Chapters
	}
	if v.RuntimeMinutes <= 0 {
		v.RuntimeMinutes = audiobookPartsMinutes(files)
	}
	v.Runtime = runtimeLabel(v.RuntimeMinutes)
	for _, c := range chapters {
		v.Chapters = append(v.Chapters, views.AudiobookChapter{Title: c.Title, Start: clock(c.StartMs)})
	}
	v.Parts = audiobookParts(files, chapters)
	return v, true
}

// audiobookFiles reads the audiobook's parts, the MediaFiles
// status.fileRefs names, a Get per file from the ui's cache, in play order
// (spec.partIndex, then the status's order for parts without one). A file
// the cache does not hold is left out.
func (s *Server) audiobookFiles(ctx context.Context, a *catalogv1.Audiobook) []*catalogv1.MediaFile {
	if !a.Status.HasFile {
		return nil
	}
	files := make([]*catalogv1.MediaFile, 0, len(a.Status.FileRefs))
	for _, ref := range a.Status.FileRefs {
		if ref == "" {
			continue
		}
		var f catalogv1.MediaFile
		if s.getObject(ctx, types.NamespacedName{Namespace: a.Namespace, Name: ref}, &f) {
			files = append(files, &f)
		}
	}
	// Play order by the one rule the Audiobook controller's OrderParts, the
	// import and the naming owner share (catalogctx.PartNumbers: numbered
	// parts by their key, then the unnumbered, ties by path), whatever order
	// status.fileRefs came in; spec.partIndex is the ordering key
	// (disc*1000+track for a tagged part), never a number to show.
	parts := make([]catalogctx.Part, len(files))
	for i, f := range files {
		parts[i] = catalogctx.Part{Index: f.Spec.PartIndex, Path: f.Spec.Path}
	}
	numbers := catalogctx.PartNumbers(parts) // aligned with files; a lone part reads 0
	if len(files) < 2 {
		return files
	}
	ordered := make([]*catalogv1.MediaFile, len(files))
	for i, f := range files {
		ordered[numbers[i]-1] = f
	}
	return ordered
}

// audiobookParts turns the audiobook's files into its Parts rows: the
// part's place in play order (1..n, as {PartNumber} names the file;
// spec.partIndex is the ordering key and is never shown), the chapter's
// title when the chapters and the parts align one to one, else "Part N",
// and the probe's duration, codec and bitrate.
func audiobookParts(files []*catalogv1.MediaFile, chapters []catalogv1.Chapter) []views.AudiobookPart {
	if len(files) == 0 {
		return nil
	}
	aligned := len(chapters) == len(files)
	parts := make([]views.AudiobookPart, 0, len(files))
	for i, f := range files {
		p := views.AudiobookPart{Name: f.Name, Index: int32(i + 1), SizeBytes: f.Spec.SizeBytes}
		p.Title = "Part " + strconv.Itoa(int(p.Index))
		if aligned && chapters[i].Title != "" {
			p.Title = chapters[i].Title
		}
		if mi := f.Status.MediaInfo; mi != nil {
			if mi.RuntimeMillis > 0 {
				p.Duration = clock(mi.RuntimeMillis)
			}
			if len(mi.Audio) > 0 {
				p.Codec = strings.ToUpper(mi.Audio[0].Codec)
				if mi.Audio[0].BitrateKbps > 0 {
					p.Bitrate = strconv.Itoa(int(mi.Audio[0].BitrateKbps)) + " kbps"
				}
			}
		}
		parts = append(parts, p)
	}
	return parts
}

// audiobookPartsMinutes is the parts' probed durations together, in
// minutes: the runtime of an audiobook whose metadata names none.
func audiobookPartsMinutes(files []*catalogv1.MediaFile) int32 {
	var ms int64
	for _, f := range files {
		if mi := f.Status.MediaInfo; mi != nil {
			ms += mi.RuntimeMillis
		}
	}
	return int32(ms / 60000)
}

// audiobookStatus is the episode's status cell for an audiobook, the first
// case that applies: a grab in flight, a grab waiting out its delay, the
// file's quality (warned below the cutoff), not yet released, unmonitored,
// else missing. An undated audiobook reads missing, as a book does.
func audiobookStatus(a *catalogv1.Audiobook, monitored bool, now time.Time) views.RowStatus {
	st := a.Status
	switch {
	case st.Phase == catalogv1.AudiobookPhaseDownloading || st.ActiveDownloadRef != nil:
		return views.RowStatus{Kind: "downloading", Label: "Downloading", Title: "Audiobook is downloading"}
	case st.Phase == catalogv1.AudiobookPhaseDelayed || st.PendingGrab != nil:
		return views.RowStatus{Kind: "pending", Label: "Pending", Title: "A release waits out its delay profile"}
	case st.HasFile:
		s := views.RowStatus{Kind: "file", Label: "Unknown", Title: "Audiobook on disk"}
		if st.FileQuality != nil && st.FileQuality.Name != "" {
			s.Label = st.FileQuality.Name
		}
		if st.Phase == catalogv1.AudiobookPhaseCutoffUnmet {
			s.Warn, s.Title = true, "Quality cutoff has not been met"
		}
		return s
	case st.Metadata != nil && st.Metadata.ReleaseDate != nil && st.Metadata.ReleaseDate.After(now):
		return views.RowStatus{Kind: "unreleased", Label: "Unreleased", Title: "Audiobook has not been released"}
	case !monitored:
		return views.RowStatus{Kind: "unmonitored", Label: "Unmonitored", Title: "Audiobook is not monitored"}
	default:
		return views.RowStatus{Kind: "missing", Label: "Missing", Title: "Audiobook missing from disk"}
	}
}

// audiobookLinks are the audiobook's provider pages (design §9.5): its
// Audible page by ASIN in its region's marketplace, its Audnexus record,
// and the Open Library work of the Book its bookRef names.
func audiobookLinks(a *catalogv1.Audiobook, book *catalogv1.Book) []views.Link {
	var links []views.Link
	if asin := a.Spec.ASIN; asin != "" {
		region := a.Spec.Region
		links = append(links,
			views.Link{Label: "Audible", Href: audibleURL(asin, region), Provider: views.ProviderAudible},
			views.Link{Label: "Audnexus", Href: audnexusURL(asin, region), Provider: views.ProviderAudnexus},
		)
	}
	if book != nil && book.Spec.WorkID != "" {
		links = append(links, views.Link{Label: "Open Library", Href: "https://openlibrary.org/works/" + url.PathEscape(book.Spec.WorkID), Provider: views.ProviderOpenLibrary})
	}
	return links
}

// audibleDomains are Audible's marketplaces by the Audiobook's region.
var audibleDomains = map[catalogv1.AudiobookRegion]string{
	catalogv1.AudiobookRegionUS: "www.audible.com",
	catalogv1.AudiobookRegionUK: "www.audible.co.uk",
	catalogv1.AudiobookRegionCA: "www.audible.ca",
	catalogv1.AudiobookRegionAU: "www.audible.com.au",
	catalogv1.AudiobookRegionDE: "www.audible.de",
	catalogv1.AudiobookRegionFR: "www.audible.fr",
	catalogv1.AudiobookRegionES: "www.audible.es",
	catalogv1.AudiobookRegionIN: "www.audible.in",
	catalogv1.AudiobookRegionIT: "www.audible.it",
	catalogv1.AudiobookRegionJP: "www.audible.co.jp",
}

// audibleURL is the audiobook's page on its region's Audible marketplace,
// the US one for a region not listed.
func audibleURL(asin string, region catalogv1.AudiobookRegion) string {
	host, ok := audibleDomains[region]
	if !ok {
		host = audibleDomains[catalogv1.AudiobookRegionUS]
	}
	return "https://" + host + "/pd/" + url.PathEscape(asin)
}

// audnexusURL is the audiobook's Audnexus record, in its region when that
// is not Audnexus's default (us).
func audnexusURL(asin string, region catalogv1.AudiobookRegion) string {
	u := "https://api.audnex.us/books/" + url.PathEscape(asin)
	if region != "" && region != catalogv1.AudiobookRegionUS {
		u += "?region=" + string(region)
	}
	return u
}
