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

// Condition types reported on a Movie.
const (
	// MovieConditionReady is True when the movie is fully reconciled.
	MovieConditionReady = "Ready"
	// MovieConditionMetadataReady is True once metadata has been fetched.
	MovieConditionMetadataReady = "MetadataReady"
	// MovieConditionAvailable is True once minimumAvailability has been reached.
	MovieConditionAvailable = "Available"
	// MovieConditionHasFile is True while a MediaFile backs the movie.
	MovieConditionHasFile = "HasFile"
	// MovieConditionCutoffMet is True when the imported file meets the profile cutoff.
	MovieConditionCutoffMet = "CutoffMet"
	// MovieConditionWrongLanguage is True when the file's probed audio
	// lacks the language the profile wants (decision.LacksLanguage).
	MovieConditionWrongLanguage = "WrongLanguage"
	// MovieConditionQueueFull is True while grabs are being throttled.
	MovieConditionQueueFull = "QueueFull"
)

// MoviePhase is the coarse lifecycle state of a movie.
//
// CutoffUnevaluated is CutoffUnmet's twin for a file whose QualityProfile
// could not be resolved (no reference, a dangling one, or a profile that does
// not parse): the file exists but was never ranked against a cutoff, so it is
// neither "below the cutoff" nor "met". Reporting CutoffUnmet for it read as a
// legitimate upgrade candidate in `kubectl get` and put the item in the
// cutoff-unmet search rotation. The CutoffMet condition's ProfileUnresolved
// reason carries the detail.
//
// Transcoded is Imported's twin for a file squasharr transcoded -- one that
// carries the CLUSTARR_PROFILE tag, or that a transcode swap replaced
// (app/catalog/controller/rollup.Transcoded). A transcoded file is the final
// destination: it reads Transcoded where it would otherwise read Imported,
// CutoffUnmet or CutoffUnevaluated, counts as meeting the cutoff (the
// CutoffMet condition is True with reason Transcoded), is never selected by
// the wanted sweep and is never upgraded automatically. Only a Download in
// flight (Downloading), Unmonitored and Pending outrank it.
//
// +kubebuilder:validation:Enum=Pending;Unavailable;Wanted;Delayed;Downloading;Imported;Transcoded;CutoffUnmet;CutoffUnevaluated;Unmonitored
type MoviePhase string

// Movie phases.
const (
	MoviePhasePending     MoviePhase = "Pending"
	MoviePhaseUnavailable MoviePhase = "Unavailable"
	MoviePhaseWanted      MoviePhase = "Wanted"
	MoviePhaseDelayed     MoviePhase = "Delayed"
	MoviePhaseDownloading MoviePhase = "Downloading"
	MoviePhaseImported    MoviePhase = "Imported"
	// MoviePhaseTranscoded: the file is transcoded, and so final. See
	// MoviePhase.
	MoviePhaseTranscoded  MoviePhase = "Transcoded"
	MoviePhaseCutoffUnmet MoviePhase = "CutoffUnmet"
	// MoviePhaseCutoffUnevaluated: a file is imported but the quality
	// profile could not be resolved, so its cutoff was never evaluated.
	MoviePhaseCutoffUnevaluated MoviePhase = "CutoffUnevaluated"
	MoviePhaseUnmonitored       MoviePhase = "Unmonitored"
)

// MovieReleaseStatus is where a movie sits in its release cycle upstream.
//
// +kubebuilder:validation:Enum=tba;announced;inCinemas;released
type MovieReleaseStatus string

// Movie release statuses.
const (
	MovieReleaseStatusTBA       MovieReleaseStatus = "tba"
	MovieReleaseStatusAnnounced MovieReleaseStatus = "announced"
	MovieReleaseStatusInCinemas MovieReleaseStatus = "inCinemas"
	MovieReleaseStatusReleased  MovieReleaseStatus = "released"
)

// MovieMonitorMode says what is monitored when a movie is added.
//
// +kubebuilder:validation:Enum=movieOnly;movieAndCollection;none
type MovieMonitorMode string

// Movie monitor modes.
const (
	MovieMonitorMovieOnly          MovieMonitorMode = "movieOnly"
	MovieMonitorMovieAndCollection MovieMonitorMode = "movieAndCollection"
	MovieMonitorNone               MovieMonitorMode = "none"
)

// MovieAddMethod records how the movie entered the catalog.
//
// +kubebuilder:validation:Enum=manual;list;collection;scan
type MovieAddMethod string

// Movie add methods.
const (
	MovieAddMethodManual     MovieAddMethod = "manual"
	MovieAddMethodList       MovieAddMethod = "list"
	MovieAddMethodCollection MovieAddMethod = "collection"
	// MovieAddMethodScan marks a movie importarr discovered by scanning a root
	// folder. It is deliberately distinct from manual: nobody added it by hand.
	MovieAddMethodScan MovieAddMethod = "scan"
)

// MovieAddOptions are applied exactly once, when the movie is first reconciled;
// status.addOptionsApplied records that this has happened.
type MovieAddOptions struct {
	// Monitor says whether the collection is monitored alongside the movie.
	// +optional
	// +kubebuilder:default=movieOnly
	Monitor MovieMonitorMode `json:"monitor,omitempty"`

	// SearchForMovie triggers an automatic search on add.
	// +optional
	// +kubebuilder:default=true
	SearchForMovie *bool `json:"searchForMovie,omitempty"`

	// AddMethod records how the movie was added.
	// +optional
	// +kubebuilder:default=manual
	AddMethod MovieAddMethod `json:"addMethod,omitempty"`
}

// CollectionOpts are the defaults applied to movies pulled in from this
// movie's TMDB collection.
type CollectionOpts struct {
	// Monitor monitors the other movies in the collection.
	// +optional
	Monitor bool `json:"monitor,omitempty"`

	// QualityProfileRef is the QualityProfile for collection members.
	// +optional
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`

	// RootFolderRef is the RootFolder for collection members.
	// +optional
	RootFolderRef string `json:"rootFolderRef,omitempty"`

	// MinimumAvailability is the minimum availability for collection members.
	// +optional
	MinimumAvailability MinimumAvailability `json:"minimumAvailability,omitempty"`

	// SearchOnAdd searches for collection members as they are added.
	// +optional
	SearchOnAdd bool `json:"searchOnAdd,omitempty"`
}

// ReleaseDate is one regional release date reported by TMDB.
type ReleaseDate struct {
	// Country is the ISO 3166-1 country code of the release.
	// +required
	Country string `json:"country"`

	// Type is the TMDB release type: 1 premiere, 2 limited, 3 theatrical,
	// 4 digital, 5 physical, 6 TV.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=6
	Type int32 `json:"type"`

	// Date is the release date.
	// +required
	Date metav1.Time `json:"date"`

	// Certification is the age rating that release carried in its country
	// (TMDB release_dates), empty when TMDB records none.
	// +optional
	Certification string `json:"certification,omitempty"`
}

// CollectionRef identifies the TMDB collection a movie belongs to.
type CollectionRef struct {
	// TmdbID is the TMDB collection ID.
	// +required
	TmdbID int64 `json:"tmdbID"`

	// Name is the collection name.
	// +optional
	Name string `json:"name,omitempty"`

	// PlexID is the collection's 24-hex id in Plex's own metadata service,
	// without the plex://collection/ prefix, read from the movie's Plex
	// metadata (Plex cannot look a collection up by its TMDB id). The Plex
	// provider answers the collection with its plex:// GUID when set.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{24}$`
	PlexID string `json:"plexID,omitempty"`
}

// MovieMetadata is the provider metadata cached on the movie.
type MovieMetadata struct {
	// Title is the localised title.
	// +optional
	Title string `json:"title,omitempty"`

	// OriginalTitle is the title in the original language.
	// +optional
	OriginalTitle string `json:"originalTitle,omitempty"`

	// SortTitle is the title used for sorting.
	// +optional
	SortTitle string `json:"sortTitle,omitempty"`

	// OriginalLanguage is the BCP-47 tag of the original language.
	// +optional
	OriginalLanguage string `json:"originalLanguage,omitempty"`

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

	// Year is the release year.
	// +optional
	Year int32 `json:"year,omitempty"`

	// SecondaryYear is a second year the film is known by, where the
	// provider exposes one -- a festival premiere and a general release, or
	// two regions, that disagree on the year (Radarr's
	// MovieMetadata.SecondaryYear). Release identity accepts a title naming
	// Year or SecondaryYear. Zero or absent means there is none.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SecondaryYear int32 `json:"secondaryYear,omitempty"`

	// RuntimeMinutes is the runtime in minutes.
	// +optional
	RuntimeMinutes int32 `json:"runtimeMinutes,omitempty"`

	// Genres lists the genres.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Genres []string `json:"genres,omitempty"`

	// Status is where the movie sits in its release cycle.
	// +optional
	Status MovieReleaseStatus `json:"status,omitempty"`

	// InCinemas is the theatrical release date.
	// +optional
	InCinemas *metav1.Time `json:"inCinemas,omitempty"`

	// DigitalRelease is the digital release date.
	// +optional
	DigitalRelease *metav1.Time `json:"digitalRelease,omitempty"`

	// PhysicalRelease is the disc release date.
	// +optional
	PhysicalRelease *metav1.Time `json:"physicalRelease,omitempty"`

	// ReleaseDates lists the per-country release dates.
	// +optional
	// +kubebuilder:validation:MaxItems=60
	ReleaseDates []ReleaseDate `json:"releaseDates,omitempty"`

	// Collection is the TMDB collection the movie belongs to.
	// +optional
	Collection *CollectionRef `json:"collection,omitempty"`

	// ExternalIDs maps provider names (tmdb, imdb, ...) to their IDs.
	// +optional
	ExternalIDs map[string]string `json:"externalIDs,omitempty"`

	// Images lists the artwork published by the provider.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Images []Image `json:"images,omitempty"`

	// AlternateTitles lists other titles the movie is released under.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	AlternateTitles []string `json:"alternateTitles,omitempty"`

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

	// Ratings lists the scores gathered from the enabled ratings providers,
	// one entry per source (pkg/metadata.RatingsProvider).
	// +optional
	// +kubebuilder:validation:MaxItems=7
	// +listType=map
	// +listMapKey=source
	Ratings []Rating `json:"ratings,omitempty"`

	// Tagline is the film's promotional line (TMDB tagline).
	// +optional
	Tagline string `json:"tagline,omitempty"`

	// Studios are the production companies, in TMDB's order; the first is
	// the Plex provider's "studio".
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Studios []string `json:"studios,omitempty"`

	// Countries are the production countries' full names.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Countries []string `json:"countries,omitempty"`

	// Adult is TMDB's adult flag; the Plex provider's isAdult.
	// +optional
	Adult bool `json:"adult,omitempty"`

	// Certifications are the film's age ratings, one per country: that
	// country's theatrical certification, else its first. Certification is
	// the one chosen for the configured region.
	// +optional
	// +kubebuilder:validation:MaxItems=60
	// +listType=map
	// +listMapKey=country
	Certifications []Certification `json:"certifications,omitempty"`

	// OriginalGenres are Genres in the film's original language, in the
	// same order, when that language differs from the configured one.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	OriginalGenres []string `json:"originalGenres,omitempty"`
}

// MovieSpec defines the desired state of Movie.
type MovieSpec struct {
	// TmdbID is the TMDB movie ID; it identifies the movie and is immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tmdbID is immutable"
	TmdbID int64 `json:"tmdbID"`

	// Monitored enables automatic searching and importing for this movie.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MinimumAvailability is how far into its release cycle the movie must be
	// before it is searched for automatically.
	// +optional
	// +kubebuilder:default=released
	MinimumAvailability MinimumAvailability `json:"minimumAvailability,omitempty"`

	// AvailabilityDelayDays shifts the availability date later (or, when
	// negative, earlier) by this many days.
	// +optional
	// +kubebuilder:default=0
	AvailabilityDelayDays int32 `json:"availabilityDelayDays,omitempty"`

	// QualityProfileRef is the QualityProfile this movie is ranked against.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder the movie is stored under.
	// +required
	RootFolderRef string `json:"rootFolderRef"`

	// DelayProfileRef pins a DelayProfile; unset falls back to tag matching.
	// +optional
	DelayProfileRef *string `json:"delayProfileRef,omitempty"`

	// TranscodeProfileRef pins a TranscodeProfile; unset falls back to the
	// RootFolder defaults and transcodarr's selectors.
	// +optional
	TranscodeProfileRef *string `json:"transcodeProfileRef,omitempty"`

	// SubtitleProfileRef pins a SubtitleProfile; unset falls back to the
	// RootFolder defaults and selectors.
	// +optional
	SubtitleProfileRef *string `json:"subtitleProfileRef,omitempty"`

	// Folder overrides the folder name under the root folder.
	// +optional
	Folder *string `json:"folder,omitempty"`

	// Region is the ISO 3166-1 region whose release dates and certification are used.
	// +optional
	// +kubebuilder:default="US"
	Region string `json:"region,omitempty"`

	// AddOptions are applied once, when the movie is first reconciled.
	// +optional
	AddOptions MovieAddOptions `json:"addOptions,omitempty"`

	// Collection holds the defaults used for movies added from this movie's
	// TMDB collection.
	// +optional
	Collection *CollectionOpts `json:"collection,omitempty"`

	// Tags select DelayProfiles, TranscodeProfiles and SubtitleProfiles.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Tags []string `json:"tags,omitempty"`

	// Source records the ImportList that added the movie, if any.
	// +optional
	Source *commonv1.AddSource `json:"source,omitempty"`

	// Artwork overrides the provider's image for a type. One entry per type.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkOverride `json:"artwork,omitempty"`
}

// MovieStatus describes the observed state of Movie.
type MovieStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the movie's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=12
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Phase is the coarse lifecycle state of the movie.
	// +optional
	Phase MoviePhase `json:"phase,omitempty"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *MovieMetadata `json:"metadata,omitempty"`

	// AddOptionsApplied is true once spec.addOptions has been acted on.
	// +optional
	AddOptionsApplied bool `json:"addOptionsApplied,omitempty"`

	// Available is true once minimumAvailability has been reached.
	// +optional
	Available bool `json:"available,omitempty"`

	// AvailableAt is when the movie became, or will become, available.
	// +optional
	AvailableAt *metav1.Time `json:"availableAt,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// HasFile is true while a MediaFile backs the movie.
	// +optional
	HasFile bool `json:"hasFile,omitempty"`

	// FileRef is the name of the MediaFile backing the movie.
	// +optional
	FileRef *string `json:"fileRef,omitempty"`

	// FileQuality is the quality of the imported file.
	// +optional
	FileQuality *commonv1.Quality `json:"fileQuality,omitempty"`

	// FileFormatScore is the custom-format score of the imported file.
	// +optional
	FileFormatScore int32 `json:"fileFormatScore,omitempty"`

	// CutoffMet is true when the imported file meets the profile cutoff.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the Download currently working on this movie.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// Downloads is every grab of this item the system is responsible for, live
	// entries only (ADR-0019 §6.4); written by the remediation loop's item key
	// under catalogarr.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	Downloads []DownloadEntry `json:"downloads,omitempty"`

	// DownloadPhase is the active entry's phase, "" when none: a print column
	// and a selectable field (ADR-0019 §6.2).
	// +optional
	DownloadPhase commonv1.DownloadPhase `json:"downloadPhase,omitempty"`

	// DownloadNonces records the one-shot download intents last handled
	// (ADR-0019 §6.10).
	// +optional
	DownloadNonces *DownloadNonces `json:"downloadNonces,omitempty"`

	// LegacyDownloads is release N's record of the Downloads this item
	// adopted (ADR-0019 §10.2); release N+1 removes it.
	// +optional
	LegacyDownloads *LegacyDownloads `json:"legacyDownloads,omitempty"`

	// SearchDispatch is the outstanding search task (ADR-0019 §7.3), written
	// under catalogarr-grab.
	// +optional
	SearchDispatch *Dispatch `json:"searchDispatch,omitempty"`

	// PendingGrab is a chosen release waiting out a DelayProfile delay.
	// +optional
	PendingGrab *PendingGrab `json:"pendingGrab,omitempty"`

	// LastSearchedAt is when the movie was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// SearchAttempts counts the searches made for this movie.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`

	// DonorSearchAttempts counts the audio donor searches made for this
	// movie (anime dual-audio spec §6.1): their own backoff, beside the
	// video's. Written by the grab worker with searchAttempts.
	// +optional
	DonorSearchAttempts commonv1.Attempts `json:"donorSearchAttempts,omitempty"`

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

	// Audio is what the file's audio carries against the profile's audio
	// policy; unset when the profile has none (anime dual-audio spec §5.3).
	// +optional
	Audio *AudioState `json:"audio,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=mov,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.tmdbID`
// +kubebuilder:selectablefield:JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Year",type=integer,JSONPath=`.status.metadata.year`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Quality",type=string,JSONPath=`.status.fileQuality.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Download",type=string,JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Movie is a monitored film in the catalog.
type Movie struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MovieSpec   `json:"spec,omitempty"`
	Status MovieStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// MovieList contains a list of Movie.
type MovieList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Movie `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Movie{}, &MovieList{})
}
