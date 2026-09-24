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
)

// Condition types reported on a RootFolder.
const (
	// RootFolderConditionReady is True when the folder exists and is writable.
	RootFolderConditionReady = "Ready"
	// RootFolderConditionDiskSpaceOK is True while free space is above minFreeBytes.
	RootFolderConditionDiskSpaceOK = "DiskSpaceOK"
)

// RootFolderKind is the media kind a root folder stores.
//
// +kubebuilder:validation:Enum=movie;series;music;book;audiobook;comic
type RootFolderKind string

// Root folder kinds.
const (
	RootFolderKindMovie     RootFolderKind = "movie"
	RootFolderKindSeries    RootFolderKind = "series"
	RootFolderKindMusic     RootFolderKind = "music"
	RootFolderKindBook      RootFolderKind = "book"
	RootFolderKindAudiobook RootFolderKind = "audiobook"
	RootFolderKindComic     RootFolderKind = "comic"
)

// NamingDialect is the on-disk layout convention a media server expects.
//
// +kubebuilder:validation:Enum=jellyfin;plex;emby;kodi
type NamingDialect string

// Naming dialects.
const (
	NamingDialectJellyfin NamingDialect = "jellyfin"
	NamingDialectPlex     NamingDialect = "plex"
	NamingDialectEmby     NamingDialect = "emby"
	NamingDialectKodi     NamingDialect = "kodi"
)

// ColonReplacement says how a colon in a title is rendered on disk.
//
// +kubebuilder:validation:Enum=delete;dash;spaceDash;spaceDashSpace;smart
type ColonReplacement string

// Colon replacements.
const (
	ColonReplacementDelete         ColonReplacement = "delete"
	ColonReplacementDash           ColonReplacement = "dash"
	ColonReplacementSpaceDash      ColonReplacement = "spaceDash"
	ColonReplacementSpaceDashSpace ColonReplacement = "spaceDashSpace"
	ColonReplacementSmart          ColonReplacement = "smart"
)

// MultiEpisodeStyle says how a file covering several episodes is named.
//
// +kubebuilder:validation:Enum=extend;duplicate;repeat;scene;range;prefixedRange
type MultiEpisodeStyle string

// Multi-episode naming styles.
const (
	MultiEpisodeStyleExtend        MultiEpisodeStyle = "extend"
	MultiEpisodeStyleDuplicate     MultiEpisodeStyle = "duplicate"
	MultiEpisodeStyleRepeat        MultiEpisodeStyle = "repeat"
	MultiEpisodeStyleScene         MultiEpisodeStyle = "scene"
	MultiEpisodeStyleRange         MultiEpisodeStyle = "range"
	MultiEpisodeStylePrefixedRange MultiEpisodeStyle = "prefixedRange"
)

// Naming override token keys recognised in NamingSpec.Overrides.
const (
	NamingTokenMovieFolder     = "movieFolder"
	NamingTokenMovieFile       = "movieFile"
	NamingTokenSeriesFolder    = "seriesFolder"
	NamingTokenSeasonFolder    = "seasonFolder"
	NamingTokenEpisodeFile     = "episodeFile"
	NamingTokenAnimeFile       = "animeFile"
	NamingTokenDailyFile       = "dailyFile"
	NamingTokenArtistFolder    = "artistFolder"
	NamingTokenAlbumFolder     = "albumFolder"
	NamingTokenTrackFile       = "trackFile"
	NamingTokenAuthorFolder    = "authorFolder"
	NamingTokenBookFile        = "bookFile"
	NamingTokenAudiobookFolder = "audiobookFolder"
	NamingTokenAudiobookFile   = "audiobookFile"
	NamingTokenComicFolder     = "comicFolder"
	NamingTokenIssueFile       = "issueFile"
)

// RootDefaults are the per-folder defaults applied to items added under this
// root folder when the item itself does not set them.
type RootDefaults struct {
	// QualityProfileRef is the default QualityProfile name.
	// +optional
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`

	// TranscodeProfileRef is the default TranscodeProfile name.
	// +optional
	TranscodeProfileRef string `json:"transcodeProfileRef,omitempty"`

	// SubtitleProfileRef is the default SubtitleProfile name.
	// +optional
	SubtitleProfileRef string `json:"subtitleProfileRef,omitempty"`

	// DelayProfileRef is the default DelayProfile name.
	// +optional
	DelayProfileRef string `json:"delayProfileRef,omitempty"`

	// Monitored is the default monitored flag for new items.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MonitorNewItems is the default handling of newly discovered children.
	// +optional
	// +kubebuilder:default=all
	MonitorNewItems MonitorNewItemsMode `json:"monitorNewItems,omitempty"`

	// SearchOnAdd triggers a search as soon as an item is added.
	// +optional
	// +kubebuilder:default=true
	SearchOnAdd *bool `json:"searchOnAdd,omitempty"`

	// MinimumAvailability is the default minimum availability for movies.
	// +optional
	// +kubebuilder:default=released
	MinimumAvailability MinimumAvailability `json:"minimumAvailability,omitempty"`

	// SeriesType is the default series type for series added here.
	// +optional
	// +kubebuilder:default=standard
	SeriesType SeriesType `json:"seriesType,omitempty"`

	// SeasonFolder is the default season-folder flag for series added here.
	// +optional
	// +kubebuilder:default=true
	SeasonFolder *bool `json:"seasonFolder,omitempty"`

	// Tags are applied to every item added under this root folder.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Tags []string `json:"tags,omitempty"`
}

// NamingSpec controls how catalogarr lays files out under the root folder.
type NamingSpec struct {
	// Dialect is the media-server layout convention to follow.
	// +optional
	// +kubebuilder:default=jellyfin
	Dialect NamingDialect `json:"dialect,omitempty"`

	// ColonReplacement says how colons in titles are rendered on disk.
	// +optional
	// +kubebuilder:default=smart
	ColonReplacement ColonReplacement `json:"colonReplacement,omitempty"`

	// MultiEpisodeStyle says how multi-episode files are named.
	// +optional
	// +kubebuilder:default=prefixedRange
	MultiEpisodeStyle MultiEpisodeStyle `json:"multiEpisodeStyle,omitempty"`

	// Overrides replaces individual naming tokens of the dialect. Recognised
	// keys are movieFolder, movieFile, seriesFolder, seasonFolder, episodeFile,
	// animeFile, dailyFile, artistFolder, albumFolder, trackFile, authorFolder,
	// bookFile, audiobookFolder, audiobookFile, comicFolder and issueFile.
	// +optional
	// +kubebuilder:validation:MaxProperties=16
	Overrides map[string]string `json:"overrides,omitempty"`
}

// RecycleBin holds deleted files for a grace period instead of unlinking them.
type RecycleBin struct {
	// Path is where deleted files are moved.
	// +optional
	// +kubebuilder:default="/data/.recycle"
	Path string `json:"path,omitempty"`

	// CleanupDays is how long a recycled file is kept before it is removed;
	// 0 disables the cleanup, keeping recycled files until removed by hand
	// (Sonarr's RecycleBinCleanupDays: RecycleBinProvider.Cleanup returns
	// early at 0). A pointer so a Go client can send that 0: with omitempty
	// it would be dropped and defaulted back to 7. Unset means 7; read it
	// through CleanupDaysOrDefault.
	// +optional
	// +kubebuilder:default=7
	// +kubebuilder:validation:Minimum=0
	CleanupDays *int32 `json:"cleanupDays,omitempty"`
}

// Perms is the ownership and mode applied to imported files and folders.
type Perms struct {
	// FileMode is the octal mode applied to imported files.
	// +optional
	// +kubebuilder:default="0664"
	// +kubebuilder:validation:Pattern=`^0[0-7]{3}$`
	FileMode string `json:"fileMode,omitempty"`

	// DirMode is the octal mode applied to created directories.
	// +optional
	// +kubebuilder:default="0775"
	// +kubebuilder:validation:Pattern=`^0[0-7]{3}$`
	DirMode string `json:"dirMode,omitempty"`

	// Group is the numeric GID imported files are chgrp'd to.
	// +optional
	Group *int64 `json:"group,omitempty"`
}

// RootFolderSpec defines the desired state of RootFolder.
type RootFolderSpec struct {
	// Path is the absolute path of the root folder; it is immutable and must
	// live under /data/media/. It is a clean path: no "." or ".." segment
	// and no empty one ("//"), so "/data/media/../x" cannot pass the prefix
	// check and resolve outside the library.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="path is immutable"
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/data/media/')",message="path must start with /data/media/"
	// +kubebuilder:validation:XValidation:rule="!self.contains('/../') && !self.endsWith('/..') && !self.contains('/./') && !self.endsWith('/.') && !self.contains('//')",message="path must be clean: no '.' or '..' segment and no empty segment ('//')"
	Path string `json:"path"`

	// Kind is the media kind stored under this root folder.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="kind is immutable"
	Kind RootFolderKind `json:"kind"`

	// Defaults are applied to items added under this root folder.
	// +optional
	Defaults RootDefaults `json:"defaults,omitempty"`

	// Naming controls the on-disk layout.
	// +optional
	Naming NamingSpec `json:"naming,omitempty"`

	// RecycleBin configures the deletion grace period.
	// +optional
	RecycleBin RecycleBin `json:"recycleBin,omitempty"`

	// MinFreeBytes is the amount of free space that must remain for imports to
	// be allowed; 0 disables the check.
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	MinFreeBytes int64 `json:"minFreeBytes,omitempty"`

	// Permissions is the ownership and mode applied to imported files.
	// +optional
	Permissions Perms `json:"permissions,omitempty"`

	// ScanSchedule is a cron expression; importarr creates a LibraryScan per tick.
	// Empty means no periodic rescan.
	// +optional
	// +kubebuilder:validation:MaxLength=120
	ScanSchedule string `json:"scanSchedule,omitempty"`
}

// RootFolderStatus describes the observed state of RootFolder.
type RootFolderStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the root folder's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Accessible is true when the path exists and catalogarr can write to it.
	// +optional
	Accessible bool `json:"accessible,omitempty"`

	// FreeBytes is the free space on the filesystem backing the path.
	// +optional
	FreeBytes int64 `json:"freeBytes,omitempty"`

	// TotalBytes is the total size of the filesystem backing the path.
	// +optional
	TotalBytes int64 `json:"totalBytes,omitempty"`

	// UnmappedFolders lists folders under the path with no catalog item.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	UnmappedFolders []string `json:"unmappedFolders,omitempty"`

	// ItemCount is the number of catalog items rooted here.
	// +optional
	ItemCount int32 `json:"itemCount,omitempty"`

	// LastScannedAt is when the folder was last walked.
	// +optional
	LastScannedAt *metav1.Time `json:"lastScannedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=rf,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.spec.path`
// +kubebuilder:printcolumn:name="Items",type=integer,JSONPath=`.status.itemCount`
// +kubebuilder:printcolumn:name="Free",type=integer,JSONPath=`.status.freeBytes`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RootFolder is a library location on disk that catalog items are stored under.
type RootFolder struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RootFolderSpec   `json:"spec,omitempty"`
	Status RootFolderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// RootFolderList contains a list of RootFolder.
type RootFolderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RootFolder `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RootFolder{}, &RootFolderList{})
}
