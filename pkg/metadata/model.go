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

import "time"

// ImageType is the role an Image plays for the entity it is attached to.
type ImageType string

// Recognised image types.
const (
	ImageTypePoster     ImageType = "poster"
	ImageTypeFanart     ImageType = "fanart"
	ImageTypeBanner     ImageType = "banner"
	ImageTypeLogo       ImageType = "logo"
	ImageTypeClearart   ImageType = "clearart"
	ImageTypeThumb      ImageType = "thumb"
	ImageTypeScreenshot ImageType = "screenshot"
	ImageTypeDisc       ImageType = "disc"
	ImageTypeHeadshot   ImageType = "headshot"
)

// Image is a single piece of provider-hosted artwork.
type Image struct {
	Type     ImageType `json:"type"`
	URL      string    `json:"url"`
	Language string    `json:"language,omitempty"`
	Width    int       `json:"width,omitempty"`
	Height   int       `json:"height,omitempty"`
	// Season is set only for a season-specific image (a season poster or
	// banner); nil for an image that belongs to the whole series.
	Season *int32 `json:"season,omitempty"`
}

// Rating is a single source's rating of an entity, scaled by 100
// (hundredths) on the scale catalogv1alpha1.Rating stores: out of 10 for
// imdb, tmdb, trakt and letterboxd (ValueCentis 837 means 8.37/10; a source
// reported on another scale is normalized to /10 first -- MusicBrainz and
// Letterboxd (0-5) multiply by 2), and out of 100 for metacritic and the
// Rotten Tomatoes pair (ValueCentis 7400 means 74/100). pkg/overlay's
// FormatScore and ui/plex read it back on exactly those two scales. (Until
// 2026-09-24 this comment said every source was normalized to /10; nothing
// had filled a /100 source to contradict it.) This is int32, not
// float64, so a rating value can be copied into a CRD status field without
// crossing CLAUDE.md's "no float32/float64 under api/" line and so it
// round-trips through JSON and etcd without drift.
type Rating struct {
	Source      string `json:"source"`
	ValueCentis int32  `json:"valueCentis"`
	Votes       int32  `json:"votes,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

// Ratings holds one Rating per source, keyed by Rating.Source.
type Ratings map[string]Rating

// Rating source values. These are plain strings, like every other
// pkg/metadata enum (ImageType, MovieStatus, ...), not
// catalogv1alpha1.RatingSource -- pkg/metadata never imports api/catalog,
// so the mapping onto the CRD's typed enum happens only at the gateway
// boundary (app/catalog/metadata/enrich.go's enrichRatings), the same
// pattern mapImageType uses for ImageType. The values match
// catalogv1alpha1.RatingSource's CRD enum exactly;
// pkg/crdcheck.TestRatingSourceEnumMatchesPkgMetadata holds the two lists
// equal, mirroring TestImageTypeEnumMatchesPkgMetadata.
const (
	RatingSourceIMDb       = "imdb"
	RatingSourceTMDB       = "tmdb"
	RatingSourceRTCritic   = "rottenTomatoesCritic"
	RatingSourceRTAudience = "rottenTomatoesAudience"
	RatingSourceMetacritic = "metacritic"
	RatingSourceTrakt      = "trakt"
	RatingSourceLetterboxd = "letterboxd"
)

// AltTitle is a title an entity is also known by, optionally scoped to a
// language, country or scene-release context.
type AltTitle struct {
	Title       string `json:"title"`
	Language    string `json:"language,omitempty"`
	Country     string `json:"country,omitempty"`
	Type        string `json:"type,omitempty"`
	SceneSeason *int32 `json:"sceneSeason,omitempty"`
}

// Translation is a single language's title and overview for an entity.
type Translation struct {
	Language string `json:"language"`
	Title    string `json:"title,omitempty"`
	Overview string `json:"overview,omitempty"`
}

// Person is a cast or crew credit.
type Person struct {
	Name      string `json:"name"`
	Role      string `json:"role,omitempty"`
	Character string `json:"character,omitempty"`
	ImageURL  string `json:"imageUrl,omitempty"`
}

// Link is a typed external URL (homepage, wiki, social profile, ...).
type Link struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Provenance records which provider supplied a fetched record and when, so
// a caller merging several providers' output can attribute and expire each
// field's source independently.
type Provenance struct {
	Provider  string    `json:"provider"`
	FetchedAt time.Time `json:"fetchedAt"`
	ETag      string    `json:"etag,omitempty"`
}

// NamedRef is a lightweight reference to an entity by name, optionally with
// its own ASIN (used for audiobook authors, which Audnexus returns inline
// rather than as a full Author record).
type NamedRef struct {
	Name string `json:"name"`
	ASIN string `json:"asin,omitempty"`
}

// SeriesLink places a Book or Audiobook within a named series at an
// optional position ("1", "2.5", ...).
type SeriesLink struct {
	Series   string `json:"series"`
	Position string `json:"position,omitempty"`
	Primary  bool   `json:"primary,omitempty"`
}

// ReleaseType distinguishes the kind of release a ReleaseDate records, using
// TMDB's own release_dates.type enumeration.
type ReleaseType int32

// TMDB release types (see docs/research/metadata.md §2.1).
const (
	ReleaseTypePremiere          ReleaseType = 1
	ReleaseTypeTheatricalLimited ReleaseType = 2
	ReleaseTypeTheatrical        ReleaseType = 3
	ReleaseTypeDigital           ReleaseType = 4
	ReleaseTypePhysical          ReleaseType = 5
	ReleaseTypeTV                ReleaseType = 6
)

// ReleaseDate is one country's release of a Movie at a given ReleaseType.
type ReleaseDate struct {
	Country       string      `json:"country"`
	Type          ReleaseType `json:"type"`
	Date          time.Time   `json:"date"`
	Certification string      `json:"certification,omitempty"`
	Note          string      `json:"note,omitempty"`
}

// MovieStatus is the release-cycle status Radarr derives for a movie.
type MovieStatus string

// Movie statuses.
const (
	MovieStatusTBA       MovieStatus = "tba"
	MovieStatusAnnounced MovieStatus = "announced"
	MovieStatusInCinemas MovieStatus = "inCinemas"
	MovieStatusReleased  MovieStatus = "released"
)

// Collection is the movie collection (franchise) a Movie belongs to.
type Collection struct {
	IDs      ExternalIDs `json:"ids,omitempty"`
	Title    string      `json:"title"`
	Overview string      `json:"overview,omitempty"`
	Images   []Image     `json:"images,omitempty"`
}

// MovieHit is a single result from MovieProvider.SearchMovies.
type MovieHit struct {
	IDs    ExternalIDs `json:"ids"`
	Title  string      `json:"title"`
	Year   int32       `json:"year,omitempty"`
	Poster string      `json:"poster,omitempty"`
}

// Movie is the normalized model for a single film.
type Movie struct {
	IDs ExternalIDs `json:"ids"`

	Title            string `json:"title"`
	OriginalTitle    string `json:"originalTitle,omitempty"`
	SortTitle        string `json:"sortTitle,omitempty"`
	OriginalLanguage string `json:"originalLanguage,omitempty"`
	Overview         string `json:"overview,omitempty"`

	Year int32 `json:"year,omitempty"`
	// SecondaryYear is a second year the film is known by (Radarr's
	// MovieMetadata.SecondaryYear), 0 when there is none. See
	// DeriveSecondaryYear for how a provider derives it.
	SecondaryYear int32 `json:"secondaryYear,omitempty"`
	Runtime       int32 `json:"runtime,omitempty"`

	Genres  []string `json:"genres,omitempty"`
	Ratings Ratings  `json:"ratings,omitempty"`

	Certification string        `json:"certification,omitempty"`
	ReleaseDates  []ReleaseDate `json:"releaseDates,omitempty"`

	InCinemas       *time.Time  `json:"inCinemas,omitempty"`
	DigitalRelease  *time.Time  `json:"digitalRelease,omitempty"`
	PhysicalRelease *time.Time  `json:"physicalRelease,omitempty"`
	Status          MovieStatus `json:"status,omitempty"`

	Collection *Collection `json:"collection,omitempty"`
	Images     []Image     `json:"images,omitempty"`
	People     []Person    `json:"people,omitempty"`

	AlternateTitles []AltTitle    `json:"alternateTitles,omitempty"`
	Translations    []Translation `json:"translations,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// SeriesStatus is a TV series' airing status.
type SeriesStatus string

// Series statuses.
const (
	SeriesStatusContinuing SeriesStatus = "continuing"
	SeriesStatusEnded      SeriesStatus = "ended"
	SeriesStatusUpcoming   SeriesStatus = "upcoming"
)

// SeasonOrder names one of a series' alternate episode orderings (TheTVDB
// exposes several for the same series: broadcast/"official" order, DVD
// order, absolute order, and per-region alternates).
type SeasonOrder string

// Season orders.
const (
	SeasonOrderDefault   SeasonOrder = "default"
	SeasonOrderOfficial  SeasonOrder = "official"
	SeasonOrderDVD       SeasonOrder = "dvd"
	SeasonOrderAbsolute  SeasonOrder = "absolute"
	SeasonOrderAlternate SeasonOrder = "alternate"
	SeasonOrderRegional  SeasonOrder = "regional"
)

// Season is a single season of a Series under one SeasonOrder.
type Season struct {
	Number int32       `json:"number"`
	Images []Image     `json:"images,omitempty"`
	Order  SeasonOrder `json:"order,omitempty"`
}

// Episode is a single episode of a Series.
type Episode struct {
	IDs ExternalIDs `json:"ids,omitempty"`

	SeasonNumber   int32  `json:"seasonNumber"`
	EpisodeNumber  int32  `json:"episodeNumber"`
	AbsoluteNumber *int32 `json:"absoluteNumber,omitempty"`

	Title    string `json:"title"`
	Overview string `json:"overview,omitempty"`

	AirDate *time.Time `json:"airDate,omitempty"`
	Runtime int32      `json:"runtime,omitempty"`

	FinaleType string  `json:"finaleType,omitempty"`
	Image      *Image  `json:"image,omitempty"`
	Ratings    Ratings `json:"ratings,omitempty"`
}

// Series is the normalized model for a single TV series.
type Series struct {
	IDs ExternalIDs `json:"ids"`

	Title     string `json:"title"`
	SortTitle string `json:"sortTitle,omitempty"`
	Overview  string `json:"overview,omitempty"`

	Status SeriesStatus `json:"status,omitempty"`

	Network          string `json:"network,omitempty"`
	OriginalLanguage string `json:"originalLanguage,omitempty"`
	AirTime          string `json:"airTime,omitempty"`

	Runtime int32 `json:"runtime,omitempty"`
	Year    int32 `json:"year,omitempty"`

	FirstAired *time.Time `json:"firstAired,omitempty"`
	LastAired  *time.Time `json:"lastAired,omitempty"`
	NextAired  *time.Time `json:"nextAired,omitempty"`

	Genres        []string `json:"genres,omitempty"`
	Certification string   `json:"certification,omitempty"`
	Ratings       Ratings  `json:"ratings,omitempty"`

	Images []Image  `json:"images,omitempty"`
	People []Person `json:"people,omitempty"`

	AlternateTitles []AltTitle `json:"alternateTitles,omitempty"`

	Seasons      []Season    `json:"seasons,omitempty"`
	DefaultOrder SeasonOrder `json:"defaultOrder,omitempty"`
	Episodes     []Episode   `json:"episodes,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// Artist is the normalized model for a musical artist or group.
type Artist struct {
	IDs ExternalIDs `json:"ids"`

	Name           string `json:"name"`
	SortName       string `json:"sortName,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Overview       string `json:"overview,omitempty"`

	Type    string `json:"type,omitempty"`
	Status  string `json:"status,omitempty"`
	Country string `json:"country,omitempty"`

	Begin *time.Time `json:"begin,omitempty"`
	End   *time.Time `json:"end,omitempty"`

	Genres  []string `json:"genres,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Aliases []string `json:"aliases,omitempty"`

	Links   []Link  `json:"links,omitempty"`
	Images  []Image `json:"images,omitempty"`
	Ratings Ratings `json:"ratings,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// Track is a single recording on a Medium.
type Track struct {
	IDs ExternalIDs `json:"ids,omitempty"`

	Title        string        `json:"title"`
	Position     int32         `json:"position"`
	MediumNumber int32         `json:"mediumNumber"`
	Duration     time.Duration `json:"duration,omitempty"`
	ArtistCredit string        `json:"artistCredit,omitempty"`
}

// Medium is a single disc/side within an AlbumRelease.
type Medium struct {
	Position   int32   `json:"position"`
	Format     string  `json:"format,omitempty"`
	Name       string  `json:"name,omitempty"`
	TrackCount int32   `json:"trackCount,omitempty"`
	Tracks     []Track `json:"tracks,omitempty"`
}

// AlbumRelease is one physical/digital release of an Album (MusicBrainz
// release-group -> release).
type AlbumRelease struct {
	IDs ExternalIDs `json:"ids"`

	Title          string `json:"title"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Status         string `json:"status,omitempty"`

	Date *time.Time `json:"date,omitempty"`

	Country []string `json:"country,omitempty"`
	Labels  []string `json:"labels,omitempty"`

	CatalogNo string `json:"catalogNo,omitempty"`

	Media      []Medium `json:"media,omitempty"`
	TrackCount int32    `json:"trackCount,omitempty"`
}

// Album is the normalized model for a MusicBrainz release group.
type Album struct {
	IDs       ExternalIDs `json:"ids"`
	ArtistIDs []string    `json:"artistIds,omitempty"`

	Title          string `json:"title"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Overview       string `json:"overview,omitempty"`

	PrimaryType    string   `json:"primaryType,omitempty"`
	SecondaryTypes []string `json:"secondaryTypes,omitempty"`

	ReleaseDate *time.Time `json:"releaseDate,omitempty"`

	Genres  []string `json:"genres,omitempty"`
	Images  []Image  `json:"images,omitempty"`
	Ratings Ratings  `json:"ratings,omitempty"`
	Links   []Link   `json:"links,omitempty"`

	Releases []AlbumRelease `json:"releases,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// Author is the normalized model for a book's author.
type Author struct {
	IDs ExternalIDs `json:"ids"`

	Name           string `json:"name"`
	SortName       string `json:"sortName,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Overview       string `json:"overview,omitempty"`

	Born *time.Time `json:"born,omitempty"`
	Died *time.Time `json:"died,omitempty"`

	Links  []Link  `json:"links,omitempty"`
	Images []Image `json:"images,omitempty"`

	Genres  []string `json:"genres,omitempty"`
	Ratings Ratings  `json:"ratings,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// Edition is a single published edition of a Book (a specific ISBN).
type Edition struct {
	IDs ExternalIDs `json:"ids"`

	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	Language  string `json:"language,omitempty"`
	Publisher string `json:"publisher,omitempty"`

	Format    string `json:"format,omitempty"`
	PageCount int32  `json:"pageCount,omitempty"`

	ReleaseDate *time.Time `json:"releaseDate,omitempty"`

	Images  []Image `json:"images,omitempty"`
	Ratings Ratings `json:"ratings,omitempty"`
}

// Book is the normalized model for a work (the Open Library "work", which
// may span several Editions).
type Book struct {
	IDs       ExternalIDs `json:"ids"`
	AuthorIDs []string    `json:"authorIds,omitempty"`

	Title     string `json:"title"`
	SortTitle string `json:"sortTitle,omitempty"`
	Overview  string `json:"overview,omitempty"`

	FirstPublished *time.Time `json:"firstPublished,omitempty"`

	Genres   []string `json:"genres,omitempty"`
	Subjects []string `json:"subjects,omitempty"`

	Series []SeriesLink `json:"series,omitempty"`

	Ratings Ratings `json:"ratings,omitempty"`
	Links   []Link  `json:"links,omitempty"`

	Editions []Edition `json:"editions,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// Chapter is a single chapter's position within an Audiobook's runtime.
type Chapter struct {
	Title         string `json:"title"`
	StartOffsetMs int64  `json:"startOffsetMs"`
	LengthMs      int64  `json:"lengthMs"`
}

// Audiobook is the normalized model for a single audiobook edition (keyed
// by ASIN, region-scoped -- the same title can have a different ASIN per
// Audible marketplace).
type Audiobook struct {
	IDs    ExternalIDs `json:"ids"`
	Region string      `json:"region,omitempty"`

	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`

	Authors   []NamedRef `json:"authors,omitempty"`
	Narrators []string   `json:"narrators,omitempty"`

	Publisher   string     `json:"publisher,omitempty"`
	ReleaseDate *time.Time `json:"releaseDate,omitempty"`

	Runtime time.Duration `json:"runtime,omitempty"`

	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`

	Language string `json:"language,omitempty"`
	Format   string `json:"format,omitempty"`

	Genres []string `json:"genres,omitempty"`
	Tags   []string `json:"tags,omitempty"`

	Series []SeriesLink `json:"series,omitempty"`

	Rating *Rating `json:"rating,omitempty"`
	Image  *Image  `json:"image,omitempty"`

	IsAdult bool `json:"isAdult,omitempty"`

	Chapters []Chapter `json:"chapters,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}

// ComicIssue is a single issue within a ComicVolume.
type ComicIssue struct {
	IDs ExternalIDs `json:"ids"`

	Number string `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`

	CoverDate *time.Time `json:"coverDate,omitempty"`
	StoreDate *time.Time `json:"storeDate,omitempty"`

	Image *Image `json:"image,omitempty"`
}

// ComicVolume is the normalized model for a comic series (ComicVine
// "volume").
type ComicVolume struct {
	IDs ExternalIDs `json:"ids"`

	Kind string `json:"kind,omitempty"`

	Title       string `json:"title"`
	SortTitle   string `json:"sortTitle,omitempty"`
	Description string `json:"description,omitempty"`

	AltTitles []AltTitle `json:"altTitles,omitempty"`

	Publisher string `json:"publisher,omitempty"`

	StartYear *int32 `json:"startYear,omitempty"`
	EndYear   *int32 `json:"endYear,omitempty"`

	IssueCount int32 `json:"issueCount,omitempty"`

	Status    string `json:"status,omitempty"`
	AgeRating string `json:"ageRating,omitempty"`

	OriginalLanguage string `json:"originalLanguage,omitempty"`

	Genres []string `json:"genres,omitempty"`
	Tags   []string `json:"tags,omitempty"`

	Images  []Image `json:"images,omitempty"`
	Ratings Ratings `json:"ratings,omitempty"`

	Issues []ComicIssue `json:"issues,omitempty"`

	Provenance []Provenance `json:"provenance,omitempty"`
}
