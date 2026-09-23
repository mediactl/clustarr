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

package metadata

import (
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// mapImageType maps pkg/metadata's image roles onto catalog.clustarr.io's
// ImageType enum (api/catalog/v1alpha1/shared_types.go), which since X1 has
// exactly the same nine values -- pkg/crdcheck's
// TestImageTypeEnumMatchesPkgMetadata holds the two lists equal. It is still
// an explicit switch, not a string conversion: a value outside the enum (an
// empty Type from a provider that did not classify an image, or a tenth
// role added to pkg/metadata first) would fail CRD enum validation and
// reject the whole status apply, so it is dropped here instead. A wrong
// type is worse than a missing image.
func mapImageType(t pkgmetadata.ImageType) (catalogv1alpha1.ImageType, bool) {
	switch t {
	case pkgmetadata.ImageTypePoster:
		return catalogv1alpha1.ImageTypePoster, true
	case pkgmetadata.ImageTypeFanart:
		return catalogv1alpha1.ImageTypeFanart, true
	case pkgmetadata.ImageTypeBanner:
		return catalogv1alpha1.ImageTypeBanner, true
	case pkgmetadata.ImageTypeLogo:
		return catalogv1alpha1.ImageTypeLogo, true
	case pkgmetadata.ImageTypeClearart:
		return catalogv1alpha1.ImageTypeClearart, true
	case pkgmetadata.ImageTypeThumb:
		return catalogv1alpha1.ImageTypeThumb, true
	case pkgmetadata.ImageTypeScreenshot:
		return catalogv1alpha1.ImageTypeScreenshot, true
	case pkgmetadata.ImageTypeDisc:
		return catalogv1alpha1.ImageTypeDisc, true
	case pkgmetadata.ImageTypeHeadshot:
		return catalogv1alpha1.ImageTypeHeadshot, true
	default:
		return "", false
	}
}

// buildMovieMetadataAC maps a fetched provider Movie onto
// MovieStatus.metadata, truncating every list to the CRD's own
// +kubebuilder:validation:MaxItems cap (CLAUDE.md: "cap every status
// list").
func buildMovieMetadataAC(m *pkgmetadata.Movie, now time.Time) *catalogac.MovieMetadataApplyConfiguration {
	ac := catalogac.MovieMetadata().
		WithTitle(m.Title).
		WithOriginalTitle(m.OriginalTitle).
		WithSortTitle(m.SortTitle).
		WithOriginalLanguage(m.OriginalLanguage).
		WithOverview(m.Overview).
		WithCertification(m.Certification).
		WithYear(m.Year).
		WithSecondaryYear(m.SecondaryYear).
		WithRuntimeMinutes(m.Runtime).
		WithExternalIDs(m.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if m.Status != "" {
		// MovieReleaseStatus is a CRD enum with no empty member: setting it
		// unconditionally would send "" through SSA and fail CEL/enum
		// validation on a Movie whose provider Status was never populated
		// (for example a cache hit fixture in a test, or a provider that
		// omits status). status.metadata.status stays simply absent instead.
		ac.WithStatus(catalogv1alpha1.MovieReleaseStatus(m.Status))
	}
	if len(m.Genres) > 0 {
		ac.WithGenres(capStrings(m.Genres, 30)...) // MovieMetadata.Genres: +kubebuilder:validation:MaxItems=30
	}
	if m.InCinemas != nil {
		ac.WithInCinemas(metav1.NewTime(*m.InCinemas))
	}
	if m.DigitalRelease != nil {
		ac.WithDigitalRelease(metav1.NewTime(*m.DigitalRelease))
	}
	if m.PhysicalRelease != nil {
		ac.WithPhysicalRelease(metav1.NewTime(*m.PhysicalRelease))
	}
	for _, rd := range m.ReleaseDates {
		if len(ac.ReleaseDates) >= 60 {
			break
		}
		ac.WithReleaseDates(catalogac.ReleaseDate().
			WithCountry(rd.Country).WithType(int32(rd.Type)).WithDate(metav1.NewTime(rd.Date)))
	}
	for _, img := range m.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	for _, at := range m.AlternateTitles {
		if len(ac.AlternateTitles) >= 50 {
			break
		}
		ac.WithAlternateTitles(at.Title)
	}
	if m.Collection != nil {
		if tmdbID, ok := m.Collection.IDs[pkgmetadata.KeyTMDB]; ok {
			if id, err := strconv.ParseInt(tmdbID, 10, 64); err == nil {
				ac.WithCollection(catalogac.CollectionRef().WithTmdbID(id).WithName(m.Collection.Title))
			}
		}
	}
	return ac
}

// buildSeriesMetadataAC maps a fetched provider Series onto
// SeriesStatus.metadata. Unlike Movie, Series' CRD AlternateTitles is
// []AltTitle{Title, SceneSeason}, not []string -- map the struct, not just
// the title.
func buildSeriesMetadataAC(s *pkgmetadata.Series, now time.Time) *catalogac.SeriesMetadataApplyConfiguration {
	ac := catalogac.SeriesMetadata().
		WithTitle(s.Title).
		WithSortTitle(s.SortTitle).
		WithNetwork(s.Network).
		WithAirTime(s.AirTime).
		WithOverview(s.Overview).
		WithCertification(s.Certification).
		WithOriginalLanguage(s.OriginalLanguage).
		WithYear(s.Year).
		WithRuntimeMinutes(s.Runtime).
		WithExternalIDs(s.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if s.Status != "" {
		// SeriesRunStatus is a CRD enum with no empty member; see the
		// matching guard in buildMovieMetadataAC above.
		ac.WithStatus(catalogv1alpha1.SeriesRunStatus(s.Status))
	}
	if len(s.Genres) > 0 {
		ac.WithGenres(capStrings(s.Genres, 30)...) // SeriesMetadata.Genres: +kubebuilder:validation:MaxItems=30
	}
	for _, img := range s.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	for _, at := range s.AlternateTitles {
		if len(ac.AlternateTitles) >= 100 {
			break
		}
		alt := catalogac.AltTitle().WithTitle(at.Title)
		if at.SceneSeason != nil {
			alt.WithSceneSeason(*at.SceneSeason)
		}
		ac.WithAlternateTitles(alt)
	}
	return ac
}

// buildArtistMetadataAC maps a fetched provider Artist onto
// ArtistStatus.metadata. pkg/metadata.Artist carries Begin/End (the
// MusicBrainz life-span) and Tags/Aliases/Links/Ratings, none of which
// ArtistMetadata has a field for (api/catalog/v1alpha1/artist_types.go)
// -- they are read, not invented a home for.
func buildArtistMetadataAC(a *pkgmetadata.Artist, now time.Time) *catalogac.ArtistMetadataApplyConfiguration {
	ac := catalogac.ArtistMetadata().
		WithName(a.Name).
		WithSortName(a.SortName).
		WithDisambiguation(a.Disambiguation).
		WithType(a.Type).
		WithOverview(a.Overview).
		WithExternalIDs(a.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if a.Status != "" {
		// ArtistRunStatus is a CRD enum with no empty member
		// (continuing;ended), matching the guard in buildMovieMetadataAC.
		// pkg/metadata/clients/musicbrainz.Client's mapArtist does not
		// currently set Artist.Status at all, so this is always skipped
		// against the real provider today -- honest given the data
		// available, not a redundant guard.
		ac.WithStatus(catalogv1alpha1.ArtistRunStatus(a.Status))
	}
	if len(a.Genres) > 0 {
		ac.WithGenres(capStrings(a.Genres, 30)...) // Genres: +kubebuilder:validation:MaxItems=30 on every kind that has this field
	}
	for _, img := range a.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	return ac
}

// buildAlbumMetadataAC maps a fetched provider Album onto
// AlbumStatus.metadata.
//
// Field-manager split for Album (and buildBookMetadataAC's Book, below) --
// the question G1-0 (pkg/k8s/fieldmanager.go's ManagerCatalogarrFanout doc
// comment) left for this task to answer, because Album and Book are the two
// non-video kinds that both (a) have a parent whose own fan-out (task G2-2,
// ManagerCatalogarrFanout) discovers and creates them, the way Series
// discovers and creates Episode, and (b) carry their own independent
// status.metadata + ConditionMetadataReady, the way Movie and Series do and
// Episode/Issue do not (see target.go's errUnsupportedKind comment and
// pkg/pipeline/project.go's describeItem). Episode and Issue have no nested
// Metadata struct to split, so this question does not arise for them.
//
// The answer: **the metadata gateway (ManagerCatalogarrMetadata) is the
// sole writer of every leaf of AlbumStatus.metadata and BookStatus.metadata,
// exactly as it is for Movie, Series, Artist, Author, Audiobook and Comic.
// ManagerCatalogarrFanout must never apply to status.metadata on Album or
// Book.** Its write set there is empty by construction, not merely
// disjoint-by-convention. Concretely, Artist's fan-out (G2-2) creates an
// Album the same way Series creates an Episode -- but where Series can
// safely seed EpisodeStatus.Title/Overview/AirDate/etc. directly (Episode
// has no independent metadata fetch to race with), Artist's own discovery
// call, ArtistProvider.Albums(mbArtistID), and the gateway's per-item fetch,
// ArtistProvider.Album(mbReleaseGroupID), both bottom out in the SAME
// mapAlbum() function in pkg/metadata/clients/musicbrainz -- so a fan-out
// write and a gateway write would derive identical values from identical
// data, which is exactly the "two managers can co-own a field while values
// happen to match" trap CLAUDE.md warns makes a release invisible (a test
// that does not deliberately drop the co-owner reports a false pass). The
// clean way to avoid that trap is to not create it: G2-2's Artist/Author
// fan-out creates Album/Book objects by setting spec fields only
// (spec.artistRef/spec.releaseGroupID for Album, spec.authorRef/spec.workID
// for Book, spec.monitored once at creation -- mirroring
// ArtistAddOptions/AuthorAddOptions), and leaves status.metadata untouched;
// the newly created object then flows through the exact same
// MetadataTask -> Handler -> ManagerCatalogarrMetadata pipeline as any
// Movie or Series, which is what this file exists to build.
func buildAlbumMetadataAC(a *pkgmetadata.Album, now time.Time) *catalogac.AlbumMetadataApplyConfiguration {
	ac := catalogac.AlbumMetadata().
		WithTitle(a.Title).
		WithDisambiguation(a.Disambiguation).
		WithOverview(a.Overview).
		WithRefreshedAt(metav1.NewTime(now))

	if a.PrimaryType != "" {
		ac.WithAlbumType(a.PrimaryType)
	}
	if len(a.SecondaryTypes) > 0 {
		ac.WithSecondaryTypes(capStrings(a.SecondaryTypes, 16)...) // AlbumMetadata.SecondaryTypes: +kubebuilder:validation:MaxItems=16
	}
	if a.ReleaseDate != nil {
		ac.WithReleaseDate(metav1.NewTime(*a.ReleaseDate))
	}
	for _, rel := range a.Releases {
		if len(ac.Releases) >= 50 {
			break
		}
		// ReleaseSummary.ID is +required and this list is +listMapKey=id,
		// exactly like Book's Editions below: a release this client cannot
		// identify by its MusicBrainz release MBID is dropped rather than
		// sent with a blank id that would collide with every other dropped
		// entry.
		id := idOf(rel.IDs, pkgmetadata.KeyMBRelease)
		if id == "" {
			continue
		}
		rs := catalogac.ReleaseSummary().WithID(id).WithStatus(rel.Status)
		if len(rel.Country) > 0 {
			rs.WithCountry(rel.Country[0])
		}
		if len(rel.Labels) > 0 {
			rs.WithLabel(rel.Labels[0])
		}
		if rel.TrackCount > 0 {
			rs.WithTrackCount(rel.TrackCount)
		}
		for _, m := range rel.Media {
			if len(rs.Media) >= 20 {
				break
			}
			if m.Position < 1 {
				continue // Medium.Number is +required with Minimum=1.
			}
			rs.WithMedia(catalogac.Medium().WithNumber(m.Position).WithFormat(m.Format))
		}
		ac.WithReleases(rs)
	}
	for _, img := range a.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	return ac
}

// buildAuthorMetadataAC maps a fetched provider Author onto
// AuthorStatus.metadata. pkg/metadata.Author carries Born/Died and Links,
// none of which AuthorMetadata has a field for
// (api/catalog/v1alpha1/author_types.go) -- read, not invented a home for.
func buildAuthorMetadataAC(a *pkgmetadata.Author, now time.Time) *catalogac.AuthorMetadataApplyConfiguration {
	ac := catalogac.AuthorMetadata().
		WithName(a.Name).
		WithSortName(a.SortName).
		WithDisambiguation(a.Disambiguation).
		WithOverview(a.Overview).
		WithExternalIDs(a.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if len(a.Genres) > 0 {
		ac.WithGenres(capStrings(a.Genres, 30)...) // Genres: +kubebuilder:validation:MaxItems=30 on every kind that has this field
	}
	for _, img := range a.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	return ac
}

// buildBookMetadataAC maps a fetched provider Book onto BookStatus.metadata.
// See buildAlbumMetadataAC's doc comment for the field-manager split this
// shares with Album. pkg/metadata.Book carries no top-level Subtitle or
// Images (both live on an Edition, fetched separately by
// BookProvider.Edition, out of this task's scope) and no top-level Ratings
// home in BookMetadata -- read, not invented a home for. Book.Editions is
// filled by openlibrary.Client.Book (one page of the work's editions); a
// Books() listing entry carries none.
func buildBookMetadataAC(b *pkgmetadata.Book, now time.Time) *catalogac.BookMetadataApplyConfiguration {
	ac := catalogac.BookMetadata().
		WithTitle(b.Title).
		WithOverview(b.Overview).
		WithExternalIDs(b.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if b.FirstPublished != nil {
		ac.WithReleaseDate(metav1.NewTime(*b.FirstPublished))
	}
	if len(b.Genres) > 0 {
		ac.WithGenres(capStrings(b.Genres, 30)...) // BookMetadata.Genres: +kubebuilder:validation:MaxItems=30
	}
	for _, sl := range b.Series {
		if len(ac.SeriesLinks) >= 10 {
			break
		}
		link := catalogac.SeriesLink().WithSeries(sl.Series).WithPrimary(sl.Primary)
		if sl.Position != "" {
			link.WithPosition(sl.Position)
		}
		ac.WithSeriesLinks(link)
	}
	for _, ed := range b.Editions {
		if len(ac.Editions) >= 100 {
			break
		}
		// Edition.ID is +required and this list is +listMapKey=id: an
		// edition this client cannot identify by its Open Library edition
		// key cannot be represented, so it is dropped rather than sent with
		// a blank id that would collide with every other dropped entry.
		id := ed.IDs[pkgmetadata.KeyOpenLibraryEdition]
		if id == "" {
			continue
		}
		edAC := catalogac.Edition().WithID(id).WithTitle(ed.Title).WithLanguage(ed.Language).
			WithFormat(ed.Format).WithPublisher(ed.Publisher)
		if isbn13 := ed.IDs[pkgmetadata.KeyISBN13]; isbn13 != "" {
			edAC.WithISBN13(isbn13)
		}
		if asin := ed.IDs[pkgmetadata.KeyASIN]; asin != "" {
			edAC.WithASIN(asin)
		}
		if ed.PageCount > 0 {
			edAC.WithPageCount(ed.PageCount)
		}
		if ed.ReleaseDate != nil {
			edAC.WithReleaseDate(metav1.NewTime(*ed.ReleaseDate))
		}
		ac.WithEditions(edAC)
	}
	return ac
}

// buildAudiobookMetadataAC maps a fetched provider Audiobook onto
// AudiobookStatus.metadata. pkg/metadata.Audiobook carries Summary (a short
// blurb distinct from Description/Overview), Tags, Rating and IsAdult, none
// of which AudiobookMetadata has a field for
// (api/catalog/v1alpha1/audiobook_types.go) -- read, not invented a home
// for. Runtime is a time.Duration; AudiobookMetadata.RuntimeMinutes is an
// int32 of whole minutes, so it is floor-divided, not rounded, matching
// mediainfo's own duration-to-minutes convention elsewhere in this project.
func buildAudiobookMetadataAC(a *pkgmetadata.Audiobook, now time.Time) *catalogac.AudiobookMetadataApplyConfiguration {
	overview := a.Description
	if overview == "" {
		overview = a.Summary
	}
	ac := catalogac.AudiobookMetadata().
		WithTitle(a.Title).
		WithSubtitle(a.Subtitle).
		WithPublisher(a.Publisher).
		WithLanguage(a.Language).
		WithOverview(overview).
		WithExternalIDs(a.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if len(a.Authors) > 0 {
		authors := a.Authors
		if len(authors) > 30 {
			authors = authors[:30] // AudiobookMetadata.Authors: +kubebuilder:validation:MaxItems=30
		}
		for _, au := range authors {
			ref := catalogac.NamedRef().WithName(au.Name)
			if au.ASIN != "" {
				ref.WithASIN(au.ASIN)
			}
			ac.WithAuthors(ref)
		}
	}
	if len(a.Narrators) > 0 {
		ac.WithNarrators(capStrings(a.Narrators, 30)...) // AudiobookMetadata.Narrators: +kubebuilder:validation:MaxItems=30
	}
	if len(a.Series) > 0 {
		sl := a.Series[0] // AudiobookMetadata.Series is a single SeriesLink, not a list.
		link := catalogac.SeriesLink().WithSeries(sl.Series).WithPrimary(sl.Primary)
		if sl.Position != "" {
			link.WithPosition(sl.Position)
		}
		ac.WithSeries(link)
	}
	if a.ReleaseDate != nil {
		ac.WithReleaseDate(metav1.NewTime(*a.ReleaseDate))
	}
	if a.Runtime > 0 {
		ac.WithRuntimeMinutes(int32(a.Runtime / time.Minute))
	}
	if len(a.Genres) > 0 {
		ac.WithGenres(capStrings(a.Genres, 30)...) // Genres: +kubebuilder:validation:MaxItems=30 on every kind that has this field
	}
	for _, ch := range a.Chapters {
		if len(ac.Chapters) >= 200 {
			break
		}
		ac.WithChapters(catalogac.Chapter().WithTitle(ch.Title).WithStartMs(ch.StartOffsetMs))
	}
	if a.Image != nil {
		if t, ok := mapImageType(a.Image.Type); ok {
			ac.WithImages(catalogac.Image().WithType(t).WithURL(a.Image.URL))
		}
	}
	return ac
}

// buildComicMetadataAC maps a fetched provider ComicVolume onto
// ComicStatus.metadata. pkg/metadata.ComicVolume carries SortTitle,
// AltTitles, EndYear, OriginalLanguage and Tags, none of which ComicMetadata
// has a field for (api/catalog/v1alpha1/comic_types.go) -- read, not
// invented a home for. ComicMetadata.VolumeNumber has no counterpart on
// ComicVolume (ComicVine has no per-volume "volume number within the title"
// concept this client surfaces) and stays unset for the same reason, in the
// other direction.
func buildComicMetadataAC(v *pkgmetadata.ComicVolume, now time.Time) *catalogac.ComicMetadataApplyConfiguration {
	ac := catalogac.ComicMetadata().
		WithTitle(v.Title).
		WithPublisher(v.Publisher).
		WithOverview(v.Description).
		WithExternalIDs(v.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if v.StartYear != nil {
		ac.WithYear(*v.StartYear)
	}
	if v.IssueCount > 0 {
		ac.WithIssueCount(v.IssueCount)
	}
	if v.AgeRating != "" {
		ac.WithAgeRating(v.AgeRating)
	}
	if v.Kind != "" {
		// MangaFlag has an explicit "unknown" member for exactly the
		// no-signal case, but ComicVolume.Kind carries only "comic"/"manga"
		// (matching ComicSpec's own ComicKind enum) -- no right-to-left
		// signal -- so this maps Yes/No when the provider actually says
		// something and otherwise leaves Manga unset rather than sending
		// "unknown" as a stand-in for "we didn't ask": the enum member and
		// an absent leaf both already mean "not established" to a reader,
		// so there is nothing an explicit "unknown" would add here.
		if v.Kind == "manga" {
			ac.WithManga(catalogv1alpha1.MangaFlagYes)
		} else {
			ac.WithManga(catalogv1alpha1.MangaFlagNo)
		}
	}
	for _, img := range v.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	return ac
}

// idOf returns ids[key], or "" when absent -- a small helper so
// buildAlbumMetadataAC's release loop reads as one expression per field like
// every other builder in this file, rather than an if/ok pair per release.
func idOf(ids pkgmetadata.ExternalIDs, key string) string {
	return ids[key]
}

// capStrings truncates s to at most max entries, so a builder can pass a
// provider's Genres slice straight to a *_.WithGenres(...) that owns a CRD
// +kubebuilder:validation:MaxItems cap without a bespoke loop-and-break for
// a field that (unlike Images, AlternateTitles or Editions) never needs
// anything more than truncation -- no per-entry mapping, no per-entry drop
// condition. Every kind's Genres field on ArtistMetadata, AuthorMetadata,
// BookMetadata, AudiobookMetadata -- and, until this helper existed,
// MovieMetadata and SeriesMetadata too, which sent every genre uncapped -- is
// MaxItems=30.
func capStrings(s []string, max int) []string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
