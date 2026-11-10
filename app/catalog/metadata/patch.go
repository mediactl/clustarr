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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/textcase"
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

// ratingsMaxItems is MovieMetadata.ratings' and SeriesMetadata.ratings'
// shared +kubebuilder:validation:MaxItems (api/catalog/v1alpha1/movie_types.go,
// series_types.go) -- one entry per video RatingSource, of which the CRD
// enum has exactly seven; nonVideoRatingsMaxItems is the six non-video
// kinds' (2026-11-10 design §4.1: "ratings []Rating (MaxItems 8)").
const (
	ratingsMaxItems         = 7
	nonVideoRatingsMaxItems = 8
)

// renderRatings maps enrichRatings' result onto the CRD's
// RatingApplyConfiguration list, sorted by Source for a deterministic apply
// (spec §C.2's own wording: "patch.go renders ... sorted by source") and
// capped at the kind's MaxItems like every other list this file caps. An
// empty input renders nothing, which is safe precisely because
// enrichRatings never returns fewer sources than status.metadata.ratings
// already carried (unfilled sources are carried forward from prior) -- so
// omitting WithRatings entirely here can only happen when there was never
// anything to release in the first place.
func renderRatings(ratings []catalogv1alpha1.Rating, limit int) []*catalogac.RatingApplyConfiguration {
	if len(ratings) == 0 {
		return nil
	}
	sorted := append([]catalogv1alpha1.Rating(nil), ratings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Source < sorted[j].Source })
	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	out := make([]*catalogac.RatingApplyConfiguration, 0, len(sorted))
	for _, r := range sorted {
		out = append(out, catalogac.Rating().WithSource(r.Source).WithValueCentis(r.ValueCentis).WithVotes(r.Votes))
	}
	return out
}

// buildMovieMetadataAC maps a fetched provider Movie onto
// MovieStatus.metadata, truncating every list to the CRD's own
// +kubebuilder:validation:MaxItems cap (CLAUDE.md: "cap every status
// list"). ratings is enrichRatings' result (app/catalog/metadata/enrich.go),
// computed separately from m because RatingsProvider is independent of
// MovieProvider -- not every source m.Ratings might carry (today, only
// "tmdb" from the fetch that produced m) is the whole story.
func buildMovieMetadataAC(m *pkgmetadata.Movie, ratings []catalogv1alpha1.Rating, now time.Time) *catalogac.MovieMetadataApplyConfiguration {
	ac := catalogac.MovieMetadata().
		WithTitle(m.Title).
		WithOriginalTitle(m.OriginalTitle).
		WithSortTitle(m.SortTitle).
		WithOriginalLanguage(m.OriginalLanguage).
		WithOverview(m.Overview).
		WithCertification(m.Certification).
		WithCertificationCountry(m.CertificationCountry).
		WithLanguage(m.Language).
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
		rdac := catalogac.ReleaseDate().
			WithCountry(rd.Country).WithType(int32(rd.Type)).WithDate(metav1.NewTime(rd.Date))
		if rd.Certification != "" {
			rdac.WithCertification(rd.Certification)
		}
		ac.WithReleaseDates(rdac)
	}
	for _, img := range selectImages(m.Images, maxItemImages) {
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	// The full Plex Metadata Response (spec 2026-09-30 §3.3), each list at
	// its CRD MaxItems.
	ac.WithTagline(m.Tagline).WithAdult(m.Adult).WithSchemaVersion(pkgmetadata.SchemaVersion)
	if len(m.Studios) > 0 {
		ac.WithStudios(capStrings(m.Studios, 10)...)
	}
	if len(m.Countries) > 0 {
		ac.WithCountries(capStrings(m.Countries, 10)...)
	}
	if len(m.OriginalGenres) > 0 {
		ac.WithOriginalGenres(capStrings(m.OriginalGenres, 30)...)
	}
	if len(m.Keywords) > 0 {
		ac.WithKeywords(capStrings(m.Keywords, 50)...) // MovieMetadata.Keywords: +kubebuilder:validation:MaxItems=50
	}
	ac.WithCertifications(certificationsAC(m.Certifications)...)
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
	if rs := renderRatings(ratings, ratingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
	}
	return ac
}

// buildSeriesMetadataAC maps a fetched provider Series onto
// SeriesStatus.metadata. Unlike Movie, Series' CRD AlternateTitles is
// []AltTitle{Title, SceneSeason}, not []string -- map the struct, not just
// the title. FirstAired is TVDB's own firstAired (pkg/metadata/clients/tvdb),
// carried straight through: spec §C.1 also names TMDB's first_air_date as an
// acceptable source, but pkg/metadata/clients/tmdb has no SeriesProvider to
// fetch it from (see that package's RatingSources doc comment), so TVDB is
// the only source today. ratings is enrichRatings' result, exactly as
// buildMovieMetadataAC's is.
func buildSeriesMetadataAC(s *pkgmetadata.Series, ratings []catalogv1alpha1.Rating, now time.Time) *catalogac.SeriesMetadataApplyConfiguration {
	ac := catalogac.SeriesMetadata().
		WithTitle(s.Title).
		WithSortTitle(s.SortTitle).
		WithNetwork(s.Network).
		WithAirTime(s.AirTime).
		WithOverview(s.Overview).
		WithCertification(s.Certification).
		WithCertificationCountry(s.CertificationCountry).
		WithLanguage(s.Language).
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
	var own []pkgmetadata.Image
	for _, img := range s.Images {
		if img.Season != nil {
			// A season's artwork (spec 2026-09-30 §3.4) goes to
			// seasonImages, never to the series' own images.
			t, ok := mapImageType(img.Type)
			if !ok || len(ac.SeasonImages) >= 400 {
				continue
			}
			si := catalogac.SeasonImage().WithSeason(*img.Season).WithType(t).WithURL(img.URL)
			if img.SeasonOrder != "" {
				si.WithOrder(img.SeasonOrder)
			}
			ac.WithSeasonImages(si)
			continue
		}
		own = append(own, img)
	}
	for _, img := range selectImages(own, maxItemImages) {
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	ac.WithTagline(s.Tagline).WithSchemaVersion(pkgmetadata.SchemaVersion)
	if len(s.Networks) > 0 {
		ac.WithNetworks(capStrings(s.Networks, 5)...)
	}
	if len(s.Studios) > 0 {
		ac.WithStudios(capStrings(s.Studios, 10)...)
	}
	if len(s.Countries) > 0 {
		ac.WithCountries(capStrings(s.Countries, 5)...)
	}
	if len(s.OriginalGenres) > 0 {
		ac.WithOriginalGenres(capStrings(s.OriginalGenres, 30)...)
	}
	if len(s.SubGenres) > 0 {
		ac.WithSubGenres(capStrings(s.SubGenres, 30)...) // SeriesMetadata.SubGenres: +kubebuilder:validation:MaxItems=30
	}
	if len(s.Keywords) > 0 {
		ac.WithKeywords(capStrings(s.Keywords, 50)...) // SeriesMetadata.Keywords: +kubebuilder:validation:MaxItems=50
	}
	ac.WithCertifications(certificationsAC(s.Certifications)...)
	for _, se := range s.Seasons {
		if se.Title == "" || len(ac.SeasonTitles) >= 200 { // SeriesMetadata.SeasonTitles: +kubebuilder:validation:MaxItems=200
			continue
		}
		st := catalogac.SeasonTitle().WithSeason(se.Number).WithTitle(se.Title)
		if se.OriginalTitle != "" && se.OriginalTitle != se.Title {
			st.WithOriginalTitle(se.OriginalTitle)
		}
		if se.Order != "" {
			st.WithOrder(string(se.Order))
		}
		ac.WithSeasonTitles(st)
	}
	for _, st := range s.SeasonTypes {
		if len(ac.SeasonTypes) >= 10 {
			break
		}
		ac.WithSeasonTypes(catalogac.SeasonTypeRef().WithID(st.ID).WithName(st.Name))
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
	if s.FirstAired != nil {
		ac.WithFirstAired(metav1.NewTime(*s.FirstAired))
	}
	if rs := renderRatings(ratings, ratingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
	}
	return ac
}

// withLanguage is the fetch language every non-video builder writes
// (2026-11-10 design §5.4: MetadataProvider.spec.language, else "en", which
// the gateway's FetchLanguages settles); an empty one is left unset, so a
// test that builds without one sends no "language": "".
func withLanguage(lang string) string { return strings.TrimSpace(lang) }

// buildArtistMetadataAC maps a fetched provider Artist onto
// ArtistStatus.metadata. pkg/metadata.Artist's Tags, Members and Similar
// have no field there: the tags are read, not invented a home for, and the
// members and similar artists go to the extended document
// (pkg/metadata/extended.FromArtist). ratings is enrichRatings' result, as
// buildMovieMetadataAC's is (MusicBrainz's own rating seeds it); lang the
// fetch language (withLanguage).
func buildArtistMetadataAC(a *pkgmetadata.Artist, ratings []catalogv1alpha1.Rating, lang string, now time.Time) *catalogac.ArtistMetadataApplyConfiguration {
	ac := catalogac.ArtistMetadata().
		WithName(a.Name).
		WithSortName(a.SortName).
		WithDisambiguation(a.Disambiguation).
		WithType(a.Type).
		WithOverview(a.Overview).
		WithExternalIDs(a.IDs).
		WithRefreshedAt(metav1.NewTime(now))
	if l := withLanguage(lang); l != "" {
		ac.WithLanguage(l)
	}

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
	// The 2026-11-10 design's fields (§4.2): the life-span, country,
	// aliases, links, the biography's source and the ratings.
	ac.WithSchemaVersion(pkgmetadata.SchemaVersion)
	if a.Begin != nil {
		ac.WithBegin(a.Begin.UTC().Format("2006-01-02"))
	}
	if a.End != nil {
		ac.WithEnd(a.End.UTC().Format("2006-01-02"))
	}
	if a.Country != "" {
		ac.WithCountry(a.Country)
	}
	if len(a.Aliases) > 0 {
		ac.WithAliases(capStrings(a.Aliases, 32)...)
	}
	if links := linksAC(a.Links, 32); len(links) > 0 {
		ac.WithLinks(links...)
	}
	if a.BiographySource != "" {
		ac.WithBiographySource(a.BiographySource)
	}
	if rs := renderRatings(ratings, nonVideoRatingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
	}
	for _, img := range selectImages(a.Images, maxItemImages) {
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	return ac
}

// buildAlbumMetadataAC maps a fetched provider Album onto
// AlbumStatus.metadata.
//
// Field-manager split for Album (and buildBookMetadataAC's Book, below) --
// the question G1-0 (pkg/k8s/fieldmanager.go's ManagerCatalogFanout doc
// comment) left for this task to answer, because Album and Book are the two
// non-video kinds that both (a) have a parent whose own fan-out (task G2-2,
// ManagerCatalogFanout) discovers and creates them, the way Series
// discovers and creates Episode, and (b) carry their own independent
// status.metadata + ConditionMetadataReady, the way Movie and Series do and
// Episode/Issue do not (see target.go's errUnsupportedKind comment and
// pkg/pipeline/project.go's describeItem). Episode and Issue have no nested
// Metadata struct to split, so this question does not arise for them.
//
// The answer: **the metadata gateway (ManagerCatalogMetadata) is the
// sole writer of every leaf of AlbumStatus.metadata and BookStatus.metadata,
// exactly as it is for Movie, Series, Artist, Author, Audiobook and Comic.
// ManagerCatalogFanout must never apply to status.metadata on Album or
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
// MetadataTask -> Handler -> ManagerCatalogMetadata pipeline as any
// Movie or Series, which is what this file exists to build.
//
// One leaf is the exception, and it is not the fan-out's: since gap fix X5b
// the Album reconciler (ManagerCatalog) writes
// AlbumStatus.metadata.selectedReleaseID, because only it knows which of the
// album's releases the files on disk belong to (Lidarr's
// RefreshAlbumService.MonitorSingleRelease). SSA tracks ownership per leaf,
// so this renderer never sends that leaf and the gateway owns every other
// one; the album envtest checks the split in managedFields.
func buildAlbumMetadataAC(a *pkgmetadata.Album, ratings []catalogv1alpha1.Rating, lang string, now time.Time) *catalogac.AlbumMetadataApplyConfiguration {
	ac := catalogac.AlbumMetadata().
		WithTitle(a.Title).
		WithDisambiguation(a.Disambiguation).
		WithOverview(a.Overview).
		WithRefreshedAt(metav1.NewTime(now))
	if l := withLanguage(lang); l != "" {
		ac.WithLanguage(l)
	}

	if a.PrimaryType != "" {
		ac.WithAlbumType(a.PrimaryType)
	}
	if len(a.SecondaryTypes) > 0 {
		ac.WithSecondaryTypes(capStrings(a.SecondaryTypes, 16)...) // AlbumMetadata.SecondaryTypes: +kubebuilder:validation:MaxItems=16
	}
	if a.ReleaseDate != nil {
		ac.WithReleaseDate(metav1.NewTime(*a.ReleaseDate))
	}
	// The 2026-11-10 design's fields (§4.3): genres, ids, the artist
	// credit, labels and ratings; the label and catalogue number are the
	// first release's, the selected release's own ride on its summary.
	ac.WithSchemaVersion(pkgmetadata.SchemaVersion)
	if len(a.Genres) > 0 {
		ac.WithGenres(capStrings(a.Genres, 30)...)
	}
	if len(a.IDs) > 0 {
		ac.WithExternalIDs(a.IDs)
	}
	if a.ArtistCredit != "" {
		ac.WithArtistCredit(a.ArtistCredit)
	}
	var labels []string
	for _, rel := range a.Releases {
		for _, l := range rel.Labels {
			if l != "" && !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
	}
	if len(labels) > 0 {
		ac.WithLabel(labels[0]).WithLabels(capStrings(labels, 8)...)
	}
	for _, rel := range a.Releases {
		if rel.CatalogNo != "" {
			ac.WithCatalogNumber(rel.CatalogNo)
			break
		}
	}
	if rs := renderRatings(ratings, nonVideoRatingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
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
		if rel.Title != "" && rel.Title != a.Title {
			rs.WithTitle(rel.Title)
		}
		if rel.Disambiguation != "" {
			rs.WithDisambiguation(rel.Disambiguation)
		}
		if rel.CatalogNo != "" {
			rs.WithCatalogNumber(rel.CatalogNo)
		}
		if len(rel.Country) > 0 {
			rs.WithCountry(rel.Country[0])
		}
		if len(rel.Labels) > 0 {
			rs.WithLabel(rel.Labels[0])
		}
		// The release's own date, not the group's earliest: a remaster or a
		// reissue is dated years after the original, and a release named
		// by that later year is still this album (Lidarr's
		// AlbumYearMatcher.Match(Album, int?) checks each release's date).
		if rel.Date != nil {
			rs.WithReleaseDate(metav1.NewTime(*rel.Date))
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
	for _, img := range selectImages(a.Images, maxItemImages) {
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	return ac
}

// buildAuthorMetadataAC maps a fetched provider Author onto
// AuthorStatus.metadata. ratings is enrichRatings' result and lang the
// fetch language, as on every builder.
func buildAuthorMetadataAC(a *pkgmetadata.Author, ratings []catalogv1alpha1.Rating, lang string, now time.Time) *catalogac.AuthorMetadataApplyConfiguration {
	ac := catalogac.AuthorMetadata().
		WithName(a.Name).
		WithSortName(a.SortName).
		WithDisambiguation(a.Disambiguation).
		WithOverview(a.Overview).
		WithExternalIDs(a.IDs).
		WithRefreshedAt(metav1.NewTime(now))
	if l := withLanguage(lang); l != "" {
		ac.WithLanguage(l)
	}

	if len(a.Genres) > 0 {
		ac.WithGenres(capStrings(a.Genres, 30)...) // Genres: +kubebuilder:validation:MaxItems=30 on every kind that has this field
	}
	// The 2026-11-10 design's fields (§4.4): dates, links and ratings.
	ac.WithSchemaVersion(pkgmetadata.SchemaVersion)
	if a.Born != nil {
		ac.WithBorn(a.Born.UTC().Format("2006-01-02"))
	}
	if a.Died != nil {
		ac.WithDied(a.Died.UTC().Format("2006-01-02"))
	}
	if links := linksAC(a.Links, 32); len(links) > 0 {
		ac.WithLinks(links...)
	}
	if rs := renderRatings(ratings, nonVideoRatingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
	}
	for _, img := range selectImages(a.Images, maxItemImages) {
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	return ac
}

// bookTitle is b's title in English title case (textcase.Title) when the
// book is English or carries no language, and as catalogued otherwise:
// Open Library holds many works in sentence or lower case ("The eternal
// husband"), and other languages capitalise by rules of their own. It is
// applied here, on every render, so an answer already in the metadata
// cache is cased too; a Book's name is its work id, so nothing is renamed.
func bookTitle(b *pkgmetadata.Book) string {
	if len(b.Languages) > 0 && !slices.Contains(b.Languages, "en") {
		return b.Title
	}
	return textcase.Title(b.Title)
}

// buildBookMetadataAC maps a fetched provider Book onto BookStatus.metadata.
// See buildAlbumMetadataAC's doc comment for the field-manager split this
// shares with Album. pkg/metadata.Book carries no top-level Subtitle (it
// lives on an Edition, fetched separately by BookProvider.Edition, out of
// this task's scope). Book.Editions is filled by openlibrary.Client.Book
// (one page of the work's editions); a Books() listing entry carries none.
// ratings is enrichRatings' result (Hardcover's, by the work's id or its
// first edition's ISBN) and lang the fetch language, as on every builder.
func buildBookMetadataAC(b *pkgmetadata.Book, ratings []catalogv1alpha1.Rating, lang string, now time.Time) *catalogac.BookMetadataApplyConfiguration {
	ac := catalogac.BookMetadata().
		WithTitle(bookTitle(b)).
		WithOverview(b.Overview).
		WithExternalIDs(b.IDs).
		WithRefreshedAt(metav1.NewTime(now))
	if l := withLanguage(lang); l != "" {
		ac.WithLanguage(l)
	}

	if b.FirstPublished != nil {
		ac.WithReleaseDate(metav1.NewTime(*b.FirstPublished))
	}
	if len(b.Genres) > 0 {
		ac.WithGenres(capStrings(b.Genres, 30)...) // BookMetadata.Genres: +kubebuilder:validation:MaxItems=30
	}
	// The 2026-11-10 design's fields (§4.5): languages, subjects, the
	// representative edition's pages and publisher, links and ratings.
	ac.WithSchemaVersion(pkgmetadata.SchemaVersion)
	if len(b.Languages) > 0 {
		ac.WithLanguages(capStrings(b.Languages, 16)...)
	}
	if len(b.Subjects) > 0 {
		ac.WithSubjects(capStrings(b.Subjects, 50)...)
	}
	for _, ed := range b.Editions {
		if ed.PageCount > 0 {
			ac.WithPageCount(ed.PageCount)
			break
		}
	}
	for _, ed := range b.Editions {
		if ed.Publisher != "" {
			ac.WithPublisher(ed.Publisher)
			break
		}
	}
	if links := linksAC(b.Links, 16); len(links) > 0 {
		ac.WithLinks(links...)
	}
	if rs := renderRatings(ratings, nonVideoRatingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
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
	for _, img := range selectImages(b.Images, maxItemImages) { // BookMetadata.Images: +kubebuilder:validation:MaxItems=50
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	return ac
}

// buildAudiobookMetadataAC maps a fetched provider Audiobook onto
// AudiobookStatus.metadata. pkg/metadata.Audiobook carries Summary (a short
// blurb distinct from Description/Overview), which has no field there and
// stands in for an empty Description. Runtime is a time.Duration;
// AudiobookMetadata.RuntimeMinutes is an int32 of whole minutes, so it is
// floor-divided, not rounded, matching mediainfo's own duration-to-minutes
// convention elsewhere in this project. ratings is enrichRatings' result
// (Audnexus's own rating seeds it); lang the fetch language, written as
// fetchLanguage since language is the recording's own.
func buildAudiobookMetadataAC(a *pkgmetadata.Audiobook, ratings []catalogv1alpha1.Rating, lang string, now time.Time) *catalogac.AudiobookMetadataApplyConfiguration {
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
	if l := withLanguage(lang); l != "" {
		ac.WithFetchLanguage(l)
	}

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
	// The 2026-11-10 design's fields (§4.6): abridged, the ISBN, tags, the
	// adult flag and the rating.
	ac.WithSchemaVersion(pkgmetadata.SchemaVersion)
	if a.Abridged || strings.EqualFold(strings.TrimSpace(a.Format), "abridged") {
		ac.WithAbridged(true)
	}
	if isbn := a.IDs[pkgmetadata.KeyISBN13]; isbn != "" {
		ac.WithISBN(isbn)
	}
	if len(a.Tags) > 0 {
		ac.WithTags(capStrings(a.Tags, 32)...)
	}
	if a.IsAdult {
		ac.WithIsAdult(true)
	}
	if rs := renderRatings(ratings, nonVideoRatingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
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
// ComicStatus.metadata. pkg/metadata.ComicVolume's Tags have no field there
// and are read, not invented a home for. ComicMetadata.VolumeNumber has no
// counterpart on ComicVolume (ComicVine has no per-volume "volume number
// within the title" concept this client surfaces) and stays unset for the
// same reason, in the other direction. ratings is enrichRatings' result
// (AniList's seeds it when the manga's resolver carries one) and lang the
// fetch language, as on every builder.
func buildComicMetadataAC(v *pkgmetadata.ComicVolume, ratings []catalogv1alpha1.Rating, lang string, now time.Time) *catalogac.ComicMetadataApplyConfiguration {
	ac := catalogac.ComicMetadata().
		WithTitle(v.Title).
		WithPublisher(v.Publisher).
		WithOverview(v.Description).
		WithExternalIDs(v.IDs).
		WithRefreshedAt(metav1.NewTime(now))
	if l := withLanguage(lang); l != "" {
		ac.WithLanguage(l)
	}

	if v.StartYear != nil {
		ac.WithYear(*v.StartYear)
	}
	if v.IssueCount > 0 {
		ac.WithIssueCount(v.IssueCount)
	}
	if v.AgeRating != "" {
		ac.WithAgeRating(v.AgeRating)
	}
	// The 2026-11-10 design's fields (§4.7): genres, the run status, the
	// other titles, the end year, the original language, the sort title
	// and the ratings.
	ac.WithSchemaVersion(pkgmetadata.SchemaVersion)
	if len(v.Genres) > 0 {
		ac.WithGenres(capStrings(v.Genres, 30)...)
	}
	switch strings.ToLower(v.Status) {
	case "continuing":
		ac.WithStatus(catalogv1alpha1.ComicRunStatusContinuing)
	case "ended":
		ac.WithStatus(catalogv1alpha1.ComicRunStatusEnded)
	}
	var alt []string
	for _, t := range v.AltTitles {
		if t.Title != "" && t.Title != v.Title && !slices.Contains(alt, t.Title) {
			alt = append(alt, t.Title)
		}
	}
	if len(alt) > 0 {
		ac.WithAlternateTitles(capStrings(alt, 16)...)
	}
	if v.EndYear != nil && *v.EndYear > 0 {
		ac.WithEndYear(*v.EndYear)
	}
	if v.OriginalLanguage != "" {
		ac.WithOriginalLanguage(v.OriginalLanguage)
	}
	if v.SortTitle != "" && v.SortTitle != v.Title {
		ac.WithSortTitle(v.SortTitle)
	}
	if rs := renderRatings(ratings, nonVideoRatingsMaxItems); len(rs) > 0 {
		ac.WithRatings(rs...)
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
	for _, img := range selectImages(v.Images, maxItemImages) {
		if ic, ok := imageAC(img); ok {
			ac.WithImages(ic)
		}
	}
	// v.Links: Metron's series page, by the slug no id carries (2026-11-10
	// design §9.4), which the comic page's logos read as type "metron".
	if links := linksAC(v.Links, 16); len(links) > 0 {
		ac.WithLinks(links...)
	}
	return ac
}

// idOf returns ids[key], or "" when absent -- a small helper so
// buildAlbumMetadataAC's release loop reads as one expression per field like
// every other builder in this file, rather than an if/ok pair per release.
// imageAC renders one image with its language; an image type outside the
// CRD's enum is left out.
// maxItemImages is MovieMetadata's and SeriesMetadata's Images MaxItems.
const maxItemImages = 50

// imagesPerKey is how many images of one type in one language selectImages
// keeps. Plex offers a few of each to choose from; TMDB lists dozens.
const imagesPerKey = 5

// selectImages chooses at most limit images the CRD can hold, in their
// original order, so that every type in every language is represented
// (every builder's since the 2026-11-10 design §5.4; until then the six
// non-video builders took the first fifty in provider order):
// first the first image of each (type, language), then up to imagesPerKey
// of each. Taking the first limit instead let one prolific source crowd out
// the rest -- Your Name's English and untagged TMDB posters alone filled
// fifty, so none of its Japanese images (Plex's OriginalImage) and none of
// fanart.tv's, appended after TMDB's, reached status. A URL is kept once,
// and an image outside the CRD's type enum is never counted.
func selectImages(imgs []pkgmetadata.Image, limit int) []pkgmetadata.Image {
	type key struct {
		t    pkgmetadata.ImageType
		lang string
	}
	eligible := make([]bool, len(imgs))
	seen := make(map[string]bool, len(imgs))
	for i, img := range imgs {
		if _, ok := mapImageType(img.Type); !ok || img.URL == "" || seen[img.URL] {
			continue
		}
		seen[img.URL] = true
		eligible[i] = true
	}
	chosen := make([]bool, len(imgs))
	per := map[key]int{}
	n := 0
	for pass := 1; pass <= 2; pass++ {
		for i, img := range imgs {
			k := key{img.Type, img.Language}
			if n >= limit || !eligible[i] || chosen[i] {
				continue
			}
			if (pass == 1 && per[k] > 0) || per[k] >= imagesPerKey {
				continue
			}
			chosen[i] = true
			per[k]++
			n++
		}
	}
	out := make([]pkgmetadata.Image, 0, n)
	for i, img := range imgs {
		if chosen[i] {
			out = append(out, img)
		}
	}
	return out
}

func imageAC(img pkgmetadata.Image) (*catalogac.ImageApplyConfiguration, bool) {
	t, ok := mapImageType(img.Type)
	if !ok {
		return nil, false
	}
	ic := catalogac.Image().WithType(t).WithURL(img.URL)
	if img.Language != "" {
		ic.WithLanguage(img.Language)
	}
	return ic, true
}

// certificationsAC renders the per-country certifications, one per country
// (the CRD's listMapKey) and at most 60 (its MaxItems).
func certificationsAC(certs []pkgmetadata.Certification) []*catalogac.CertificationApplyConfiguration {
	seen := map[string]bool{}
	var out []*catalogac.CertificationApplyConfiguration
	for _, c := range certs {
		if len(out) >= 60 || c.Country == "" || c.Rating == "" || seen[c.Country] {
			continue
		}
		seen[c.Country] = true
		out = append(out, catalogac.Certification().WithCountry(c.Country).WithRating(c.Rating))
	}
	return out
}

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

// linksAC renders an item's links for a listType=map keyed by type: the
// first link of each type, at most limit (2026-11-10 design §4).
func linksAC(links []pkgmetadata.Link, limit int) []*catalogac.LinkApplyConfiguration {
	var out []*catalogac.LinkApplyConfiguration
	seen := map[string]bool{}
	for _, l := range links {
		if l.Type == "" || l.URL == "" || seen[l.Type] || len(l.Type) > 32 || len(l.URL) > 512 {
			continue
		}
		seen[l.Type] = true
		out = append(out, catalogac.Link().WithType(l.Type).WithURL(l.URL))
		if len(out) == limit {
			break
		}
	}
	return out
}
