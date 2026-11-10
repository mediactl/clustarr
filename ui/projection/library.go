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
	"slices"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
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

	// MissingEpisodes and DownloadingEpisodes are a Series' counts of
	// episodes missing (monitored, aired, no file) and with a grab in
	// flight, specials left out; zero for every other kind. A Series'
	// status is read from them (see [LibraryStatus]).
	MissingEpisodes, DownloadingEpisodes int32

	// MissingChildren, DownloadingChildren and CutoffUnmetChildren are a
	// container's rollup (2026-11-10 design §4.1, §9.1): an Artist's
	// albums, an Author's books, a Comic's issues that are missing
	// (monitored, released, no file), with a grab in flight, or on disk
	// below the cutoff; zero for every other kind. A container's status is
	// read from them as a Series' is from its episodes ([containerStatus]).
	MissingChildren, DownloadingChildren, CutoffUnmetChildren int32

	// DeleteMode is the item's catalog.clustarr.io/delete ("files" or
	// "records"), "" when no delete is pending; DeleteExclude its
	// delete-add-exclusion and DeleteError import's delete-error.
	DeleteMode    string
	DeleteExclude bool
	DeleteError   string

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
	// ArtKey is the poster's image key, "<kind>/<uid>/poster" ([ArtKey]):
	// the key of the SSE art event (artwork design §B.8 as amended
	// 2026-10-07), which swaps every img carrying it as data-art to the
	// digest /art now serves, without waiting for the next projection tick.
	ArtKey string
	// QualityProfileRef is spec.qualityProfileRef, or "" for a kind that
	// inherits one (a Book with no profile of its own).
	QualityProfileRef string

	// ProviderID is the item's id at its metadata provider -- TMDB for a
	// Movie, TVDB for a Series, MusicBrainz for an Artist, Open Library
	// for an Author -- and "" for every other kind. Add New marks a search
	// hit already in the library by it (2026-09-29).
	ProviderID string

	// What the library's custom filters and sorts read (owner,
	// 2026-11-10, Radarr's and Sonarr's): zero where the kind has no such
	// field. Slices are the object's own, never copied or modified.
	//
	// Added is metadata.creationTimestamp; RootFolder spec.rootFolderRef;
	// Path status.path; Tags spec.tags.
	Added      time.Time
	RootFolder string
	Path       string
	Tags       []string
	// From status.metadata: Genres (a series' sub-genres apart),
	// Keywords, Studios, Networks (a series'), Countries, the original
	// language as a pkg/lang tag, Certification, RuntimeMinutes, Ratings
	// and Collection (a movie's TMDB collection).
	Genres, SubGenres, Keywords []string
	Studios, Networks           []string
	Countries                   []string
	OriginalLanguage            string
	Certification               string
	RuntimeMinutes              int32
	Ratings                     []catalogv1.Rating
	Collection                  string
	// ReleaseStatus is a movie's status.metadata.status (tba, announced,
	// inCinemas, released) or a series' (continuing, ended, upcoming).
	ReleaseStatus string
	// A movie's spec.minimumAvailability and release dates.
	MinimumAvailability                        string
	InCinemas, DigitalRelease, PhysicalRelease time.Time
	// Released is a book's or an audiobook's publication date (2026-11-10
	// design §9.1); zero for a kind whose release is one of the movie's
	// three dates above, or a comic's, which has only its start Year.
	Released time.Time
	// A series' spec.seriesType, first, next and previous airing, and
	// counts: seasons (specials left out), episodes and episodes on disk.
	SeriesType                             string
	FirstAired, NextAiring, PreviousAiring time.Time
	SeasonCount                            int32
	EpisodeCount, EpisodeFileCount         int32
	// SizeBytes is the item's files on disk: a movie's file, every
	// distinct file of a series' episodes.
	SizeBytes int64
	// A movie's file: its quality's name, the probe's video codec and
	// HDR format, and its audio tracks' languages as pkg/lang tags.
	Quality        string
	VideoCodec     string
	HDR            string
	AudioLanguages []string
}

// Tab is one of the library page's tabs. Every parent kind belongs to
// exactly one; a child kind (Episode, Album, an Author's Book, Issue)
// belongs to none and produces no card.
type Tab string

// The six tabs (2026-11-10 design D1: Movies, TV, Music, Books, Comics,
// Audiobooks). A Comic is carded on Comics and an Audiobook on Audiobooks,
// where until then both sat on Books (2026-09-23 library page design,
// superseded there).
const (
	TabMovies     Tab = "movies"
	TabTV         Tab = "tv"
	TabMusic      Tab = "music"
	TabBooks      Tab = "books"
	TabComics     Tab = "comics"
	TabAudiobooks Tab = "audiobooks"
)

// Tabs lists the tabs in the order the page shows them: the sidebar's
// Library sub-menu and the Add New menu both iterate it.
func Tabs() []Tab { return []Tab{TabMovies, TabTV, TabMusic, TabBooks, TabComics, TabAudiobooks} }

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
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindBook}
	case TabComics:
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindComic}
	case TabAudiobooks:
		return []catalogv1.RootFolderKind{catalogv1.RootFolderKindAudiobook}
	}
	return nil
}

// UnmatchedEntry is one row on the Unmatched page (amendment §A3.4): one file
// from one LibraryScan's status.unmatched list (A1.5's never-guess output),
// plus enough of the scan's own identity for the row to be useful without a
// second lookup. This task's Unmatched page is read-only (G3-4 adds the
// manual-assign action once G2-4 defines its mechanism in import), so
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
func buildLibraryItems(items []client.Object, entries []pipeline.Entry, fileOf func(client.Object) *catalogv1.MediaFile) []LibraryItem {
	out := make([]LibraryItem, 0, len(items))
	for i, item := range items {
		card, ok := describeLibraryItem(item)
		if !ok {
			continue
		}
		ann := item.GetAnnotations()
		li := card.item
		li.DeleteMode = ann[catalogv1.AnnotationDelete]
		li.DeleteExclude = ann[catalogv1.AnnotationDeleteAddExclusion] == "true"
		li.DeleteError = ann[catalogv1.AnnotationDeleteError]
		li.Ref, li.Kind, li.Title = entries[i].Ref, entries[i].Kind, entries[i].Title
		li.ArtKey = ArtKey(entries[i].Kind, item.GetUID(), catalogv1.ImageTypePoster)
		li.Added = item.GetCreationTimestamp().Time
		if card.ownFile && fileOf != nil {
			withFile(&li, fileOf(item))
		}
		out = append(out, li)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
	return out
}

// withFile fills what a movie's card reads from its file: the size and,
// from the probe, the video codec, HDR format and audio languages.
func withFile(li *LibraryItem, mf *catalogv1.MediaFile) {
	if mf == nil {
		return
	}
	li.SizeBytes = mf.Spec.SizeBytes
	mi := mf.Status.MediaInfo
	if mi == nil {
		return
	}
	li.VideoCodec = mi.VideoCodec
	li.HDR = string(mi.Hdr)
	for _, a := range mi.Audio {
		if tag, ok := lang.Normalize(a.Language); ok && !slices.Contains(li.AudioLanguages, string(tag)) {
			li.AudioLanguages = append(li.AudioLanguages, string(tag))
		}
	}
}

// libraryCard is what a parent kind contributes to its card beyond the Ref,
// Kind, Title and annotations: item is the LibraryItem with every field
// the kind has filled, and ownFile says the item's file is its own (a
// Movie's), whose size and probe the card reads.
type libraryCard struct {
	item    LibraryItem
	ownFile bool
}

// describeLibraryItem reads one catalog item's card, and false for a kind
// that has no card: Episode, Album and Issue are children of a Series, an
// Artist and a Comic, and so is a Book with an authorRef (one without is its
// own parent). Movie, Series, Book and Audiobook carry status.phase
// directly; Artist, Author and Comic are containers whose phase is the
// ContainerRollup's (Pending, Ready, Unmonitored), whose hasFile is
// whether any child has an imported file yet (AlbumFileCount,
// BookFileCount, IssueFileCount) and whose missing, downloading and
// cutoff-unmet children the card's status reads ([containerStatus]).
//
// The card's year (2026-11-10 design §9.1) reads an Artist's and an
// Author's nothing, a Comic's start year, an Audiobook's and a standalone
// Book's release year. ProviderID is a Comic's "<source>:<sourceID>"
// ("comicvine:4050-18166"), an Audiobook's "asin:<asin>" and a Book's
// "olid:<workID>" ([ProviderIDASIN], [ProviderIDOpenLibrary]), which the
// typeahead's prefixed queries match ([Find]); an Artist's and an Author's
// stay the bare MusicBrainz and Open Library ids Add New compares.
func describeLibraryItem(item client.Object) (libraryCard, bool) {
	switch v := item.(type) {
	case *catalogv1.Movie:
		li := LibraryItem{
			Tab: TabMovies, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), HasFile: v.Status.HasFile, QualityProfileRef: v.Spec.QualityProfileRef,
			Poster:     posterArt(commonv1.MediaKindMovie, v.GetUID(), v.Status.Artwork, v.Status.Overlay),
			ProviderID: strconv.FormatInt(v.Spec.TmdbID, 10),
			RootFolder: v.Spec.RootFolderRef, Path: v.Status.Path, Tags: v.Spec.Tags,
			MinimumAvailability: string(v.Spec.MinimumAvailability),
		}
		if q := v.Status.FileQuality; q != nil && v.Status.HasFile {
			li.Quality = q.Name
		}
		if md := v.Status.Metadata; md != nil {
			li.Year = md.Year
			li.Genres, li.Keywords, li.Studios, li.Countries = md.Genres, md.Keywords, md.Studios, md.Countries
			li.OriginalLanguage = langTag(md.OriginalLanguage)
			li.Certification, li.RuntimeMinutes, li.Ratings = md.Certification, md.RuntimeMinutes, md.Ratings
			li.ReleaseStatus = string(md.Status)
			li.InCinemas, li.DigitalRelease, li.PhysicalRelease = momentOf(md.InCinemas), momentOf(md.DigitalRelease), momentOf(md.PhysicalRelease)
			if md.Collection != nil {
				li.Collection = md.Collection.Name
			}
		}
		return libraryCard{item: li, ownFile: true}, true
	case *catalogv1.Series:
		li := LibraryItem{
			Tab: TabTV, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), QualityProfileRef: v.Spec.QualityProfileRef,
			HasFile:         v.Status.EpisodeFileCount > 0,
			MissingEpisodes: v.Status.MissingEpisodeCount, DownloadingEpisodes: v.Status.DownloadingEpisodeCount,
			Poster:     posterArt(commonv1.MediaKindSeries, v.GetUID(), v.Status.Artwork, v.Status.Overlay),
			ProviderID: strconv.FormatInt(v.Spec.TvdbID, 10),
			RootFolder: v.Spec.RootFolderRef, Path: v.Status.Path, Tags: v.Spec.Tags,
			SeriesType:   string(v.Spec.SeriesType),
			EpisodeCount: v.Status.EpisodeCount, EpisodeFileCount: v.Status.EpisodeFileCount,
			NextAiring: momentOf(v.Status.NextAiring), PreviousAiring: momentOf(v.Status.PreviousAiring),
		}
		for _, s := range v.Status.Seasons {
			li.SizeBytes += s.SizeBytes
			if s.Number > 0 {
				li.SeasonCount++
			}
		}
		if md := v.Status.Metadata; md != nil {
			li.Year = md.Year
			li.Genres, li.SubGenres, li.Keywords = md.Genres, md.SubGenres, md.Keywords
			li.Studios, li.Countries = md.Studios, md.Countries
			li.Networks = md.Networks
			if len(li.Networks) == 0 && md.Network != "" {
				li.Networks = []string{md.Network}
			}
			li.OriginalLanguage = langTag(md.OriginalLanguage)
			li.Certification, li.RuntimeMinutes, li.Ratings = md.Certification, md.RuntimeMinutes, md.Ratings
			li.ReleaseStatus = string(md.Status)
			li.FirstAired = momentOf(md.FirstAired)
		}
		return libraryCard{item: li}, true
	case *catalogv1.Artist:
		li := LibraryItem{
			Tab: TabMusic, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), HasFile: v.Status.AlbumFileCount > 0, QualityProfileRef: v.Spec.QualityProfileRef,
			Poster:     posterArt(commonv1.MediaKindArtist, v.GetUID(), v.Status.Artwork, nil),
			ProviderID: v.Spec.MusicBrainzID, Tags: v.Spec.Tags, RootFolder: v.Spec.RootFolderRef, Path: v.Status.Path,
		}
		withRollup(&li, v.Status.ContainerRollup)
		if md := v.Status.Metadata; md != nil {
			li.Genres, li.Ratings = md.Genres, md.Ratings
			li.ReleaseStatus = string(md.Status)
			if md.Country != "" {
				li.Countries = []string{md.Country}
			}
		}
		return libraryCard{item: li}, true
	case *catalogv1.Author:
		li := LibraryItem{
			Tab: TabBooks, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), HasFile: v.Status.BookFileCount > 0, QualityProfileRef: v.Spec.QualityProfileRef,
			Poster:     posterArt(commonv1.MediaKindAuthor, v.GetUID(), v.Status.Artwork, nil),
			ProviderID: v.Spec.OpenLibraryID, Tags: v.Spec.Tags, RootFolder: v.Spec.RootFolderRef, Path: v.Status.Path,
		}
		withRollup(&li, v.Status.ContainerRollup)
		if md := v.Status.Metadata; md != nil {
			li.Genres, li.Ratings = md.Genres, md.Ratings
		}
		return libraryCard{item: li}, true
	case *catalogv1.Book:
		if v.Spec.AuthorRef != nil && *v.Spec.AuthorRef != "" {
			return libraryCard{}, false
		}
		li := LibraryItem{
			Tab: TabBooks, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), HasFile: v.Status.HasFile,
			Poster:     posterArt(commonv1.MediaKindBook, v.GetUID(), v.Status.Artwork, nil),
			ProviderID: prefixedID(ProviderIDOpenLibrary, v.Spec.WorkID), Path: v.Status.Path,
		}
		if v.Spec.QualityProfileRef != nil {
			li.QualityProfileRef = *v.Spec.QualityProfileRef
		}
		if v.Spec.RootFolderRef != nil {
			li.RootFolder = *v.Spec.RootFolderRef
		}
		if q := v.Status.FileQuality; q != nil && v.Status.HasFile {
			li.Quality = q.Name
		}
		if md := v.Status.Metadata; md != nil {
			li.Released = momentOf(md.ReleaseDate)
			li.Year = yearOf(md.ReleaseDate)
			li.Genres, li.Keywords, li.Ratings = md.Genres, md.Subjects, md.Ratings
			if md.Publisher != "" {
				li.Studios = []string{md.Publisher}
			}
		}
		return libraryCard{item: li}, true
	case *catalogv1.Audiobook:
		li := LibraryItem{
			Tab: TabAudiobooks, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), HasFile: v.Status.HasFile, QualityProfileRef: v.Spec.QualityProfileRef,
			Poster:     posterArt(commonv1.MediaKindAudiobook, v.GetUID(), v.Status.Artwork, nil),
			ProviderID: prefixedID(ProviderIDASIN, v.Spec.ASIN), Tags: v.Spec.Tags, RootFolder: v.Spec.RootFolderRef, Path: v.Status.Path,
		}
		if q := v.Status.FileQuality; q != nil && v.Status.HasFile {
			li.Quality = q.Name
		}
		if md := v.Status.Metadata; md != nil {
			li.Released = momentOf(md.ReleaseDate)
			li.Year = yearOf(md.ReleaseDate)
			li.Genres, li.Ratings, li.RuntimeMinutes = md.Genres, md.Ratings, md.RuntimeMinutes
			li.OriginalLanguage = langTag(md.Language)
			if md.Publisher != "" {
				li.Studios = []string{md.Publisher}
			}
		}
		return libraryCard{item: li}, true
	case *catalogv1.Comic:
		li := LibraryItem{
			Tab: TabComics, Monitored: monitoredOrDefault(v.Spec.Monitored),
			Phase: string(v.Status.Phase), HasFile: v.Status.IssueFileCount > 0, QualityProfileRef: v.Spec.QualityProfileRef,
			Poster:     posterArt(commonv1.MediaKindComic, v.GetUID(), v.Status.Artwork, nil),
			ProviderID: prefixedID(string(v.Spec.Source), v.Spec.SourceID), Tags: v.Spec.Tags, RootFolder: v.Spec.RootFolderRef, Path: v.Status.Path,
		}
		withRollup(&li, v.Status.ContainerRollup)
		if md := v.Status.Metadata; md != nil {
			li.Year = md.Year
			li.Genres, li.Ratings, li.Certification = md.Genres, md.Ratings, md.AgeRating
			li.ReleaseStatus = string(md.Status)
			if md.Publisher != "" {
				li.Studios = []string{md.Publisher}
			}
		}
		return libraryCard{item: li}, true
	default:
		return libraryCard{}, false
	}
}

// The prefixes a Book's and an Audiobook's ProviderID carry, so an id of
// one provider is never read as another's (a Comic's is its spec.source,
// "comicvine" or "mangadex").
const (
	ProviderIDOpenLibrary = "olid"
	ProviderIDASIN        = "asin"
)

// prefixedID is "<prefix>:<id>", or "" for an item with no id yet.
func prefixedID(prefix, id string) string {
	if id == "" {
		return ""
	}
	return prefix + ":" + id
}

// withRollup reads a container's children counts and release dates into
// its card: the next and previous release dates stand where a series'
// next and previous airing do, so the same sorts and filters read them.
func withRollup(li *LibraryItem, r catalogv1.ContainerRollup) {
	li.MissingChildren, li.DownloadingChildren, li.CutoffUnmetChildren = r.MissingChildCount, r.DownloadingChildCount, r.CutoffUnmetChildCount
	li.NextAiring, li.PreviousAiring = momentOf(r.NextReleaseDate), momentOf(r.PreviousReleaseDate)
}

// momentOf is t's time, zero for nil.
func momentOf(t *metav1.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.Time
}

// yearOf is t's calendar year in UTC, 0 for nil: a metav1.Time comes back
// from the apiserver in the replica's local zone, so a date read in its
// own zone is a year off near New Year west of UTC (CLAUDE.md).
func yearOf(t *metav1.Time) int32 {
	if t == nil {
		return 0
	}
	return int32(t.UTC().Year()) //nolint:gosec // a year
}

// langTag is a provider's language code as a pkg/lang tag, the vocabulary
// the probe's audio languages are read into, so the two filter alike; a
// code pkg/lang cannot read is kept as it is.
func langTag(code string) string {
	if tag, ok := lang.Normalize(code); ok {
		return string(tag)
	}
	return code
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

// ArtNoWebP is the [ArtURL] query parameter ("&nowebp=1") asking handleArt
// for a WebP object in a format every Plex component decodes: JPEG, or PNG
// when it has transparency. Any other object is served as it is. PMS keeps
// the bytes it downloads, and its UltraBlur processor decodes no WebP: on
// talos 1,151 movies and shows whose stored background was a WebP had no
// UltraBlur colours, and one night's maintenance failed to decode 704 of
// them (2026-11-03).
const ArtNoWebP = "nowebp"

// ArtKey is an image's key: "<kind>/<uid>/<type>", the path [ArtURL] serves
// it at under /art/ and the key of the SSE art event.
func ArtKey(kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType) string {
	return string(kind) + "/" + string(uid) + "/" + string(t)
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
func listLibraryScans(ctx context.Context, r client.Reader, opts ...client.ListOption) ([]catalogv1.LibraryScan, error) {
	var scans catalogv1.LibraryScanList
	if err := r.List(ctx, &scans, opts...); err != nil {
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
