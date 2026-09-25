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

package projection

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// LibraryItem is one row on the Library page (amendment §A3.4): a thin,
// generic view over any catalog kind the shared projection already lists for
// the Pipeline page (Task G3-3). It deliberately does not duplicate
// pkg/pipeline's per-kind title derivation -- Ref, Kind and Title come
// straight from the [pipeline.Entry] built for the same item in the same
// tick, per [buildLibraryItems] -- and adds only what the Pipeline page has
// no use for: whether the item is monitored, and its own status.phase and
// hasFile (or their closest equivalent for a collection kind that has
// neither -- see [describeLibraryItem]).
type LibraryItem struct {
	// Ref identifies the catalog item, exactly as pipeline.Entry.Ref does.
	Ref types.NamespacedName

	// Kind is the item's media kind.
	Kind commonv1.MediaKind

	// Title is the item's display title.
	Title string

	// Monitored is spec.monitored, defaulting to true for a nil pointer --
	// every catalog kind's CRD defaults spec.monitored=true, so a hand-built
	// object with the field left nil (a test fixture, or an object read
	// before a defaulting webhook/apiserver default has applied) renders the
	// same as the CRD default rather than as unmonitored.
	Monitored bool

	// Phase is status.phase, or "" for a kind with no phase concept: Artist,
	// Author and Comic are collection parents (they own Albums, Books and
	// Issues respectively) and report counts instead; Issue has no phase
	// field at all.
	Phase string

	// HasFile is true once at least one file is imported for this item, or
	// the closest available proxy for a collection kind with no hasFile
	// field of its own (Artist, Author and Comic report it via their
	// child-file counts instead -- see [describeLibraryItem]).
	HasFile bool

	// Tab is the library tab the item's kind belongs to; see [Tab].
	Tab Tab
	// Year is status.metadata.year where the kind has one, else 0.
	Year int32
	// Poster is the [ArtURL] of the item's poster: status.overlay's when the
	// renderer has written one (Movie and Series only), else the poster
	// entry in status.artwork, else "" when neither has arrived yet. It
	// always points at this ui's own /art route -- never a provider's URL
	// directly -- so the browser never hotlinks a metadata provider's CDN
	// (ADR-0011, superseding spec 2026-09-23-library-page-design decision 1;
	// see that spec's dated note).
	Poster string
	// QualityProfileRef is spec.qualityProfileRef, or "" for a kind that
	// inherits one (a Book with no profile of its own).
	QualityProfileRef string
}

// Tab is one of the library page's tabs. Every parent kind belongs to
// exactly one; a child kind (Episode, Album, an Author's Book, Issue)
// belongs to none and produces no card.
type Tab string

const (
	TabMovies Tab = "movies"
	TabTV     Tab = "tv"
	TabMusic  Tab = "music"
	TabBooks  Tab = "books"
)

// Tabs lists the tabs in the order the page shows them.
func Tabs() []Tab { return []Tab{TabMovies, TabTV, TabMusic, TabBooks} }

// ParseTab turns a URL path segment into a Tab, refusing anything that is
// not exactly one of [Tabs].
func ParseTab(s string) (Tab, bool) {
	for _, tab := range Tabs() {
		if s == string(tab) {
			return tab, true
		}
	}
	return "", false
}

// ForTab keeps the items whose Tab is tab, in their existing order.
func ForTab(items []LibraryItem, tab Tab) []LibraryItem {
	out := make([]LibraryItem, 0, len(items))
	for _, item := range items {
		if item.Tab == tab {
			out = append(out, item)
		}
	}
	return out
}

// RootFolderKinds lists the RootFolder kinds whose folders hold a tab's
// items: the page's single Rescan (2026-09-24) fans out to every
// RootFolder of these kinds. Every RootFolderKind belongs to exactly one
// tab, mirroring the parent kinds Build puts in each.
func RootFolderKinds(tab Tab) []catalogv1.RootFolderKind {
	switch tab {
	case TabMovies:
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindMovie}
	case TabTV:
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindSeries}
	case TabMusic:
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindMusic}
	case TabBooks:
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindBook, catalogv1.RootFolderKindAudiobook, catalogv1.RootFolderKindComic}
	}
	return nil
}

// UnmatchedEntry is one row on the Unmatched page (amendment §A3.4): one file
// from one LibraryScan's status.unmatched list (A1.5's never-guess output),
// plus enough of the scan's own identity for the row to be useful without a
// second lookup. This task's Unmatched page is read-only (G3-4 adds the
// manual-assign action once G2-4 defines its mechanism in importarr), so
// this type carries no action-oriented fields.
type UnmatchedEntry struct {
	// ScanRef identifies the LibraryScan this file was reported by.
	ScanRef types.NamespacedName

	// RootFolder is the scan's spec.rootFolderRef: which RootFolder was
	// being walked when this file turned up unattributable.
	RootFolder string

	// Path is the file path relative to the root folder.
	Path string

	// Reason explains why the scanner could not attribute the file.
	Reason string

	// Candidates lists catalog items the scanner considered but could not
	// confidently match to.
	Candidates []string

	// SeenAt is when the scanner last observed this file.
	SeenAt metav1.Time
}

// buildLibraryItems derives one [LibraryItem] per item, reusing the
// [pipeline.Entry] already computed for the same item in the same tick
// (project's own loop) for Ref, Kind and Title rather than re-deriving them:
// pkg/pipeline's describeItem already solves per-kind title extraction (the
// provider metadata's title, falling back to the object's own name) and
// nothing here needs to duplicate that. items and entries must be the same
// length and in the same order -- project's loop guarantees this by
// construction, building both from one range over items.
func buildLibraryItems(items []client.Object, entries []pipeline.Entry) []LibraryItem {
	out := make([]LibraryItem, 0, len(items))
	for i, item := range items {
		card, ok := describeLibraryItem(item)
		if !ok {
			continue
		}
		out = append(out, LibraryItem{
			Ref:               entries[i].Ref,
			Kind:              entries[i].Kind,
			Title:             entries[i].Title,
			Monitored:         card.monitored,
			Phase:             card.phase,
			HasFile:           card.hasFile,
			Tab:               card.tab,
			Year:              card.year,
			Poster:            card.poster,
			QualityProfileRef: card.profile,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
	return out
}

// libraryCard is what a parent kind contributes to its card beyond the Ref,
// Kind and Title the pipeline entry already carries.
type libraryCard struct {
	tab       Tab
	monitored bool
	phase     string
	hasFile   bool
	year      int32
	poster    string
	profile   string
}

// describeLibraryItem reads one catalog item's card, and false for a kind
// that has no card: Episode, Album and Issue are children of a Series, an
// Artist and a Comic, and so is a Book with an authorRef (one without is its
// own parent). Movie, Series, Book and Audiobook carry status.phase
// directly; Artist, Author and Comic are collection parents with none, and
// report hasFile via whether any child has an imported file yet
// (AlbumFileCount, BookFileCount, IssueFileCount), the closest reading of
// "this collection has something on disk" their status offers.
func describeLibraryItem(item client.Object) (libraryCard, bool) {
	switch v := item.(type) {
	case *catalogv1.Movie:
		c := libraryCard{
			tab: TabMovies, monitored: monitoredOrDefault(v.Spec.Monitored),
			phase: string(v.Status.Phase), hasFile: v.Status.HasFile, profile: v.Spec.QualityProfileRef,
			poster: posterArt(commonv1.MediaKindMovie, v.GetUID(), v.Status.Artwork, v.Status.Overlay),
		}
		if md := v.Status.Metadata; md != nil {
			c.year = md.Year
		}
		return c, true
	case *catalogv1.Series:
		c := libraryCard{
			tab: TabTV, monitored: monitoredOrDefault(v.Spec.Monitored),
			phase: string(v.Status.Phase), profile: v.Spec.QualityProfileRef,
			poster: posterArt(commonv1.MediaKindSeries, v.GetUID(), v.Status.Artwork, v.Status.Overlay),
		}
		if md := v.Status.Metadata; md != nil {
			c.year = md.Year
		}
		return c, true
	case *catalogv1.Artist:
		c := libraryCard{
			tab: TabMusic, monitored: monitoredOrDefault(v.Spec.Monitored),
			hasFile: v.Status.AlbumFileCount > 0, profile: v.Spec.QualityProfileRef,
			poster: posterArt(commonv1.MediaKindArtist, v.GetUID(), v.Status.Artwork, nil),
		}
		return c, true
	case *catalogv1.Author:
		c := libraryCard{
			tab: TabBooks, monitored: monitoredOrDefault(v.Spec.Monitored),
			hasFile: v.Status.BookFileCount > 0, profile: v.Spec.QualityProfileRef,
			poster: posterArt(commonv1.MediaKindAuthor, v.GetUID(), v.Status.Artwork, nil),
		}
		return c, true
	case *catalogv1.Book:
		if v.Spec.AuthorRef != nil && *v.Spec.AuthorRef != "" {
			return libraryCard{}, false
		}
		c := libraryCard{
			tab: TabBooks, monitored: monitoredOrDefault(v.Spec.Monitored),
			phase: string(v.Status.Phase), hasFile: v.Status.HasFile,
			poster: posterArt(commonv1.MediaKindBook, v.GetUID(), v.Status.Artwork, nil),
		}
		if v.Spec.QualityProfileRef != nil {
			c.profile = *v.Spec.QualityProfileRef
		}
		return c, true
	case *catalogv1.Audiobook:
		c := libraryCard{
			tab: TabBooks, monitored: monitoredOrDefault(v.Spec.Monitored),
			phase: string(v.Status.Phase), hasFile: v.Status.HasFile, profile: v.Spec.QualityProfileRef,
			poster: posterArt(commonv1.MediaKindAudiobook, v.GetUID(), v.Status.Artwork, nil),
		}
		return c, true
	case *catalogv1.Comic:
		c := libraryCard{
			tab: TabBooks, monitored: monitoredOrDefault(v.Spec.Monitored),
			hasFile: v.Status.IssueFileCount > 0, profile: v.Spec.QualityProfileRef,
			poster: posterArt(commonv1.MediaKindComic, v.GetUID(), v.Status.Artwork, nil),
		}
		if md := v.Status.Metadata; md != nil {
			c.year = md.Year
		}
		return c, true
	default:
		return libraryCard{}, false
	}
}

// ArtURL is the URL ui/art.go's handleArt serves the artwork object
// (kind, uid, t) from: /art/<kind>/<uid>/<type>?v=<digest>. It is the only
// way a page ever links to a catalog item's artwork -- never a provider's
// own URL -- so the browser's request always lands on this ui, not on
// whatever CDN status.metadata.images or status.artwork.sourceURL names
// (ADR-0011).
//
// digest is the digest of whichever object handleArt is expected to serve
// right now (the caller decides which -- see [posterArt]): handleArt
// compares it against what it actually served and answers
// "Cache-Control: public, max-age=31536000, immutable" only on a match,
// "no-cache" otherwise, so a stale digest here costs a cache miss, never a
// wrong image.
func ArtURL(kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType, digest string) string {
	return fmt.Sprintf("/art/%s/%s/%s?v=%s", kind, uid, t, digest)
}

// posterArt is a card's poster [ArtURL]: the rating-badge overlay when one
// has been rendered (overlay is non-nil only for Movie and Series, the only
// two kinds status.overlay exists on, spec §B.3), else the poster entry in
// status.artwork, else "" -- which the templates already render as a
// placeholder rather than an empty <img> (posterFill, poster in
// ui/views/library.templ).
func posterArt(kind commonv1.MediaKind, uid types.UID, artwork []catalogv1.ArtworkEntry, overlay *catalogv1.OverlayEntry) string {
	if overlay != nil {
		return ArtURL(kind, uid, catalogv1.ImageTypePoster, overlay.Digest)
	}
	for _, a := range artwork {
		if a.Type == catalogv1.ImageTypePoster {
			return ArtURL(kind, uid, catalogv1.ImageTypePoster, a.Digest)
		}
	}
	return ""
}

// monitoredOrDefault reads a spec.monitored pointer, defaulting a nil one to
// true -- every catalog kind's CRD sets +kubebuilder:default=true on this
// field, so a nil pointer (a hand-built object in a test, or one read before
// defaulting applied) means the same thing the apiserver would eventually
// write, not "false".
func monitoredOrDefault(m *bool) bool {
	return m == nil || *m
}

// listLibraryScans lists every current LibraryScan through r -- one List
// call, added to the same tick [project] already performs for the Pipeline
// and Downloads streams, per ruling R4: a third stream shares the one list
// round rather than starting a ticker of its own.
func listLibraryScans(ctx context.Context, r client.Reader) ([]catalogv1.LibraryScan, error) {
	var scans catalogv1.LibraryScanList
	if err := r.List(ctx, &scans); err != nil {
		return nil, fmt.Errorf("projection: list library scans: %w", err)
	}
	return scans.Items, nil
}

// unmatchedFromScans flattens every listed LibraryScan's status.unmatched
// into one slice, newest first across scans (each scan's own list is already
// newest-first per its doc comment, but multiple scans' entries must be
// merged and re-sorted rather than concatenated in list order, which is
// undefined across separate objects).
func unmatchedFromScans(scans []catalogv1.LibraryScan) []UnmatchedEntry {
	var out []UnmatchedEntry
	for i := range scans {
		scan := &scans[i]
		for _, u := range scan.Status.Unmatched {
			out = append(out, UnmatchedEntry{
				ScanRef:    types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name},
				RootFolder: scan.Spec.RootFolderRef,
				Path:       u.Path,
				Reason:     u.Reason,
				Candidates: u.Candidates,
				SeenAt:     u.SeenAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeenAt.After(out[j].SeenAt.Time) })
	return out
}
