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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on a Series.
const (
	// SeriesConditionReady is True when the series is fully reconciled.
	SeriesConditionReady = "Ready"
	// SeriesConditionMetadataReady is True once metadata has been fetched.
	SeriesConditionMetadataReady = "MetadataReady"
	// SeriesConditionEpisodesSynced is True once the Episode objects match the metadata.
	SeriesConditionEpisodesSynced = "EpisodesSynced"
)

// SeriesPhase is the coarse lifecycle state of a series.
//
// +kubebuilder:validation:Enum=Pending;Ready;Unmonitored
type SeriesPhase string

// Series phases.
const (
	SeriesPhasePending     SeriesPhase = "Pending"
	SeriesPhaseReady       SeriesPhase = "Ready"
	SeriesPhaseUnmonitored SeriesPhase = "Unmonitored"
)

// SeriesRunStatus is the upstream production status of a series.
//
// +kubebuilder:validation:Enum=continuing;ended;upcoming
type SeriesRunStatus string

// Series run statuses.
const (
	SeriesRunStatusContinuing SeriesRunStatus = "continuing"
	SeriesRunStatusEnded      SeriesRunStatus = "ended"
	SeriesRunStatusUpcoming   SeriesRunStatus = "upcoming"
)

// EpisodeOrder selects the numbering scheme episodes are matched against.
//
// +kubebuilder:validation:Enum=official;dvd;absolute
type EpisodeOrder string

// Episode orders.
const (
	EpisodeOrderOfficial EpisodeOrder = "official"
	EpisodeOrderDVD      EpisodeOrder = "dvd"
	EpisodeOrderAbsolute EpisodeOrder = "absolute"
)

// SeriesMonitorMode says which episodes are monitored when a series is added.
//
// +kubebuilder:validation:Enum=all;future;missing;existing;firstSeason;lastSeason;pilot;recent;monitorSpecials;unmonitorSpecials;none;skip
type SeriesMonitorMode string

// Series monitor modes.
const (
	SeriesMonitorAll               SeriesMonitorMode = "all"
	SeriesMonitorFuture            SeriesMonitorMode = "future"
	SeriesMonitorMissing           SeriesMonitorMode = "missing"
	SeriesMonitorExisting          SeriesMonitorMode = "existing"
	SeriesMonitorFirstSeason       SeriesMonitorMode = "firstSeason"
	SeriesMonitorLastSeason        SeriesMonitorMode = "lastSeason"
	SeriesMonitorPilot             SeriesMonitorMode = "pilot"
	SeriesMonitorRecent            SeriesMonitorMode = "recent"
	SeriesMonitorMonitorSpecials   SeriesMonitorMode = "monitorSpecials"
	SeriesMonitorUnmonitorSpecials SeriesMonitorMode = "unmonitorSpecials"
	SeriesMonitorNone              SeriesMonitorMode = "none"
	SeriesMonitorSkip              SeriesMonitorMode = "skip"
)

// SeasonSpec turns monitoring on or off for one season.
type SeasonSpec struct {
	// Number is the season number; 0 is the specials season.
	// +required
	// +kubebuilder:validation:Minimum=0
	Number int32 `json:"number"`

	// Monitored enables automatic searching for the season's episodes.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`
}

// SeasonStatus is the observed state of one season.
type SeasonStatus struct {
	// Number is the season number; 0 is the specials season.
	// +required
	// +kubebuilder:validation:Minimum=0
	Number int32 `json:"number"`

	// Monitored is true when any of the season's episodes is monitored.
	// +optional
	Monitored bool `json:"monitored,omitempty"`

	// AppliedMonitored is the spec.seasons override the Series controller
	// last set on every episode of the season. A season override is applied
	// once per change, as Sonarr's season toggle is, so an episode toggled
	// on its own afterwards keeps its own flag.
	// +optional
	AppliedMonitored *bool `json:"appliedMonitored,omitempty"`

	// EpisodeCount is the number of episodes in the season.
	// +optional
	EpisodeCount int32 `json:"episodeCount,omitempty"`

	// EpisodeFileCount is the number of episodes with an imported file.
	// +optional
	EpisodeFileCount int32 `json:"episodeFileCount,omitempty"`

	// SizeBytes is the total size of the season's imported files.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// NextAiring is when the next episode of the season airs.
	// +optional
	NextAiring *metav1.Time `json:"nextAiring,omitempty"`
}

// SeriesAddOptions are applied exactly once, when the series is first
// reconciled; status.addOptionsApplied records that this has happened.
type SeriesAddOptions struct {
	// Monitor selects which episodes start out monitored. The default,
	// none, searches nothing until a season or an episode is turned on.
	// +optional
	// +kubebuilder:default=none
	Monitor SeriesMonitorMode `json:"monitor,omitempty"`

	// IgnoreEpisodesWithFiles leaves episodes that already have a file unmonitored.
	// +optional
	IgnoreEpisodesWithFiles bool `json:"ignoreEpisodesWithFiles,omitempty"`

	// IgnoreEpisodesWithoutFiles leaves episodes without a file unmonitored.
	// +optional
	IgnoreEpisodesWithoutFiles bool `json:"ignoreEpisodesWithoutFiles,omitempty"`

	// SearchForMissing searches for missing episodes on add.
	// +optional
	// +kubebuilder:default=true
	SearchForMissing *bool `json:"searchForMissing,omitempty"`

	// SearchForCutoffUnmet searches for episodes below the profile cutoff on add.
	// +optional
	// +kubebuilder:default=false
	SearchForCutoffUnmet *bool `json:"searchForCutoffUnmet,omitempty"`
}

// SeriesMetadata is the provider metadata cached on the series.
type SeriesMetadata struct {
	// Title is the localised title.
	// +optional
	Title string `json:"title,omitempty"`

	// SortTitle is the title used for sorting.
	// +optional
	SortTitle string `json:"sortTitle,omitempty"`

	// Network is the broadcaster or streaming service.
	// +optional
	Network string `json:"network,omitempty"`

	// AirTime is the local broadcast time, e.g. "21:00".
	// +optional
	AirTime string `json:"airTime,omitempty"`

	// Overview is the synopsis.
	// +optional
	Overview string `json:"overview,omitempty"`

	// Certification is the content rating in the configured region.
	// +optional
	Certification string `json:"certification,omitempty"`

	// Language is the BCP-47 language the metadata was fetched in (the
	// MetadataProvider's spec.language, "en" by default): the language of
	// Title and Overview. The Plex provider answers a request naming no
	// language as though it named this one.
	// +optional
	// +kubebuilder:validation:MaxLength=35
	Language string `json:"language,omitempty"`

	// CertificationCountry is the ISO 3166-1 alpha-2 country Certification
	// was chosen from (the region's, else the origin's, else the US), which
	// the Plex provider prefixes the rating with outside the US.
	// +optional
	// +kubebuilder:validation:MaxLength=2
	CertificationCountry string `json:"certificationCountry,omitempty"`

	// OriginalLanguage is the BCP-47 tag of the original language.
	// +optional
	OriginalLanguage string `json:"originalLanguage,omitempty"`

	// Year is the first-aired year.
	// +optional
	Year int32 `json:"year,omitempty"`

	// RuntimeMinutes is the nominal episode runtime in minutes.
	// +optional
	RuntimeMinutes int32 `json:"runtimeMinutes,omitempty"`

	// Status is the upstream production status.
	// +optional
	Status SeriesRunStatus `json:"status,omitempty"`

	// Genres lists the genres.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Genres []string `json:"genres,omitempty"`

	// ExternalIDs maps provider names (tvdb, tmdb, imdb, tvmaze, anidb,
	// anilist, mal, kitsu) to their IDs.
	// +optional
	ExternalIDs map[string]string `json:"externalIDs,omitempty"`

	// Images lists the artwork published by the provider.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Images []Image `json:"images,omitempty"`

	// AlternateTitles lists other titles the series is released under, with the
	// scene season each numbers against where relevant.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	AlternateTitles []AltTitle `json:"alternateTitles,omitempty"`

	// RefreshedAt is when the metadata was last fetched.
	// +optional
	RefreshedAt metav1.Time `json:"refreshedAt,omitempty"`

	// SchemaVersion is the metadata gateway's pkg/metadata.SchemaVersion
	// when it wrote this document. The gateway raises it when it learns a
	// field, and the item's reconciler refreshes an item whose document is
	// older once, keeping it ready meanwhile, rather than leaving it
	// without the new field until its RefreshTTL -- weeks for a released
	// film.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SchemaVersion int32 `json:"schemaVersion,omitempty"`

	// FirstAired is the date the series first aired, from TVDB firstAired or
	// TMDB first_air_date. Plex requires it.
	// +optional
	FirstAired *metav1.Time `json:"firstAired,omitempty"`

	// Ratings lists the scores gathered from the enabled ratings providers,
	// one entry per source (pkg/metadata.RatingsProvider).
	// +optional
	// +kubebuilder:validation:MaxItems=7
	// +listType=map
	// +listMapKey=source
	Ratings []Rating `json:"ratings,omitempty"`

	// Tagline is the series' promotional line (TMDB tv, when a TMDB id and
	// key exist).
	// +optional
	Tagline string `json:"tagline,omitempty"`

	// Networks are the networks the series aired on, original first; Network
	// stays the first of them.
	// +optional
	// +kubebuilder:validation:MaxItems=5
	Networks []string `json:"networks,omitempty"`

	// Studios are the production companies (TVDB companies).
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Studios []string `json:"studios,omitempty"`

	// Countries are the countries of origin, as full names.
	// +optional
	// +kubebuilder:validation:MaxItems=5
	Countries []string `json:"countries,omitempty"`

	// Certifications are the series' age ratings, one per country (TVDB
	// contentRatings). Certification is the one chosen for the configured
	// region.
	// +optional
	// +kubebuilder:validation:MaxItems=60
	// +listType=map
	// +listMapKey=country
	Certifications []Certification `json:"certifications,omitempty"`

	// OriginalGenres are Genres in the series' original language, in the
	// same order, when that language differs from the configured one.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	OriginalGenres []string `json:"originalGenres,omitempty"`

	// SeasonImages are per-season artwork (TVDB season posters and
	// backgrounds). They live here rather than on status.seasons so the
	// Series controller stays the only writer of status.seasons.
	// +optional
	// +kubebuilder:validation:MaxItems=400
	SeasonImages []SeasonImage `json:"seasonImages,omitempty"`

	// PlexSeasons are each season's id in Plex's own metadata service,
	// from a plex MetadataProvider. They live here rather than on
	// status.seasons for the same reason as SeasonImages.
	// +optional
	// +kubebuilder:validation:MaxItems=400
	PlexSeasons []PlexSeasonRef `json:"plexSeasons,omitempty"`

	// SeasonTypes are the episode orderings the provider offers.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	SeasonTypes []SeasonTypeRef `json:"seasonTypes,omitempty"`
}

// SeriesSpec defines the desired state of Series.
type SeriesSpec struct {
	// TvdbID is the TheTVDB series ID; it identifies the series and is immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tvdbID is immutable"
	TvdbID int64 `json:"tvdbID"`

	// SeriesType selects the numbering and naming dialect.
	// +optional
	// +kubebuilder:default=standard
	SeriesType SeriesType `json:"seriesType,omitempty"`

	// Monitored enables automatic searching and importing for this series.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MonitorNewItems says what happens to seasons and episodes discovered
	// after the series was added.
	// +optional
	// +kubebuilder:default=all
	MonitorNewItems MonitorNewChildrenMode `json:"monitorNewItems,omitempty"`

	// Seasons overrides monitoring per season. A change to an entry sets
	// spec.monitored on every episode of that season once (Sonarr's season
	// toggle), and a new episode of the season takes the override rather
	// than monitorNewItems.
	// +optional
	// +listType=map
	// +listMapKey=number
	// +kubebuilder:validation:MaxItems=200
	Seasons []SeasonSpec `json:"seasons,omitempty"`

	// SeasonFolder stores each season in its own folder.
	// +optional
	// +kubebuilder:default=true
	SeasonFolder *bool `json:"seasonFolder,omitempty"`

	// EpisodeOrder is the numbering scheme releases are matched against.
	// Anime series are forced to absolute ordering.
	// +optional
	// +kubebuilder:default=official
	EpisodeOrder EpisodeOrder `json:"episodeOrder,omitempty"`

	// QualityProfileRef is the QualityProfile episodes are ranked against.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder the series is stored under.
	// +required
	RootFolderRef string `json:"rootFolderRef"`

	// DelayProfileRef pins a DelayProfile; unset falls back to tag matching.
	// +optional
	DelayProfileRef *string `json:"delayProfileRef,omitempty"`

	// TranscodeProfileRef pins a TranscodeProfile.
	// +optional
	TranscodeProfileRef *string `json:"transcodeProfileRef,omitempty"`

	// SubtitleProfileRef pins a SubtitleProfile.
	// +optional
	SubtitleProfileRef *string `json:"subtitleProfileRef,omitempty"`

	// Folder overrides the folder name under the root folder.
	// +optional
	Folder *string `json:"folder,omitempty"`

	// AddOptions are applied once, when the series is first reconciled.
	// +optional
	AddOptions SeriesAddOptions `json:"addOptions,omitempty"`

	// Tags select DelayProfiles, TranscodeProfiles and SubtitleProfiles.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Tags []string `json:"tags,omitempty"`

	// Source records the ImportList that added the series, if any.
	// +optional
	Source *commonv1.AddSource `json:"source,omitempty"`

	// Artwork overrides the provider's image for a type. One entry per type.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkOverride `json:"artwork,omitempty"`
}

// AnnotationClassify set to "off" skips anime classification for a Series.
const AnnotationClassify = "catalog.clustarr.io/classify"

// SeriesClassification is the result of the one-time anime detection
// (docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md §4).
type SeriesClassification struct {
	// Anime is true when the series' metadata named it anime.
	Anime bool `json:"anime"`

	// AppliedAt is when it was classified.
	AppliedAt metav1.Time `json:"appliedAt"`

	// QualityProfileRef is the profile applied, empty when none was.
	// +optional
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`

	// SeriesType is the series type applied, empty when none was.
	// +optional
	SeriesType SeriesType `json:"seriesType,omitempty"`
}

// SeriesStatus describes the observed state of Series.
type SeriesStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the series' state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Classification records the one-time anime detection: once set, the
	// Series reconciler never classifies the series again, so a profile or
	// type the owner changes afterwards stays.
	// +optional
	Classification *SeriesClassification `json:"classification,omitempty"`

	// Phase is the coarse lifecycle state of the series.
	// +optional
	Phase SeriesPhase `json:"phase,omitempty"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *SeriesMetadata `json:"metadata,omitempty"`

	// AddOptionsApplied is true once spec.addOptions has been acted on.
	// +optional
	AddOptionsApplied bool `json:"addOptionsApplied,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// Seasons is the per-season rollup.
	// +optional
	// +listType=map
	// +listMapKey=number
	// +kubebuilder:validation:MaxItems=200
	Seasons []SeasonStatus `json:"seasons,omitempty"`

	// EpisodeCount is the number of Episode objects owned by the series.
	// +optional
	EpisodeCount int32 `json:"episodeCount,omitempty"`

	// EpisodeFileCount is the number of episodes with an imported file.
	// +optional
	EpisodeFileCount int32 `json:"episodeFileCount,omitempty"`

	// MissingEpisodeCount is the number of episodes Sonarr counts missing:
	// monitored (with the series), aired and without a file -- phase
	// Wanted. Specials are left out, as from every series total.
	// +optional
	MissingEpisodeCount int32 `json:"missingEpisodeCount,omitempty"`

	// DownloadingEpisodeCount is the number of episodes with a download in
	// flight or a grab pending (phase Downloading or Delayed), specials
	// left out.
	// +optional
	DownloadingEpisodeCount int32 `json:"downloadingEpisodeCount,omitempty"`

	// NextAiring is when the next episode airs.
	// +optional
	NextAiring *metav1.Time `json:"nextAiring,omitempty"`

	// PreviousAiring is when the most recent episode aired.
	// +optional
	PreviousAiring *metav1.Time `json:"previousAiring,omitempty"`

	// LastSearchedAt is when the series was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// Artwork lists the images fetched into the artwork store, one per type.
	// Written by the metadata gateway.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkEntry `json:"artwork,omitempty"`

	// Overlay is the rating-badge overlay rendered onto the poster. Written
	// by the renderer under k8s.ManagerCatalogarrArtwork.
	// +optional
	Overlay *OverlayEntry `json:"overlay,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=ser,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.tvdbID`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.seriesType`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Episodes",type=integer,JSONPath=`.status.episodeCount`
// +kubebuilder:printcolumn:name="Files",type=integer,JSONPath=`.status.episodeFileCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Series is a monitored television series in the catalog.
type Series struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeriesSpec   `json:"spec,omitempty"`
	Status SeriesStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// SeriesList contains a list of Series.
type SeriesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Series `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Series{}, &SeriesList{})
}
