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

	// Monitored mirrors the effective monitored flag of the season.
	// +optional
	Monitored bool `json:"monitored,omitempty"`

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
	// Monitor selects which episodes start out monitored.
	// +optional
	// +kubebuilder:default=all
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

	// Seasons overrides monitoring per season.
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

	// NextAiring is when the next episode airs.
	// +optional
	NextAiring *metav1.Time `json:"nextAiring,omitempty"`

	// PreviousAiring is when the most recent episode aired.
	// +optional
	PreviousAiring *metav1.Time `json:"previousAiring,omitempty"`

	// LastSearchedAt is when the series was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`
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
