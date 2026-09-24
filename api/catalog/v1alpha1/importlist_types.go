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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on an ImportList.
const (
	// ImportListConditionReady is True when the list is configured and reachable.
	ImportListConditionReady = "Ready"
	// ImportListConditionAuthenticated is True when the list's credentials are accepted.
	ImportListConditionAuthenticated = "Authenticated"
	// ImportListConditionSynced is True when the last sync completed.
	ImportListConditionSynced = "Synced"
)

// SyncLevel says what happens to catalog items that have fallen off the list.
//
// +kubebuilder:validation:Enum=disabled;logOnly;keepAndUnmonitor;removeAndKeep;removeAndDelete
type SyncLevel string

// Sync levels.
const (
	SyncLevelDisabled         SyncLevel = "disabled"
	SyncLevelLogOnly          SyncLevel = "logOnly"
	SyncLevelKeepAndUnmonitor SyncLevel = "keepAndUnmonitor"
	SyncLevelRemoveAndKeep    SyncLevel = "removeAndKeep"
	SyncLevelRemoveAndDelete  SyncLevel = "removeAndDelete"
)

// TraktListType is the kind of Trakt list being followed.
//
// +kubebuilder:validation:Enum=watchlist;list;collection;popular;trending
type TraktListType string

// Trakt list types.
const (
	TraktListTypeWatchlist  TraktListType = "watchlist"
	TraktListTypeList       TraktListType = "list"
	TraktListTypeCollection TraktListType = "collection"
	TraktListTypePopular    TraktListType = "popular"
	TraktListTypeTrending   TraktListType = "trending"
)

// CustomListFormat is the wire format a custom list is served in.
//
// +kubebuilder:validation:Enum=json;rss
type CustomListFormat string

// Custom list formats.
const (
	CustomListFormatJSON CustomListFormat = "json"
	CustomListFormatRSS  CustomListFormat = "rss"
)

// ArrKind is the *arr flavour an ArrList points at.
//
// +kubebuilder:validation:Enum=radarr;sonarr;lidarr;readarr;clustarr
type ArrKind string

// Arr kinds.
const (
	ArrKindRadarr   ArrKind = "radarr"
	ArrKindSonarr   ArrKind = "sonarr"
	ArrKindLidarr   ArrKind = "lidarr"
	ArrKindReadarr  ArrKind = "readarr"
	ArrKindClustarr ArrKind = "clustarr"
)

// DeviceAuthState is where a device-code authorization flow has got to.
//
// +kubebuilder:validation:Enum=none;pending;authorized;expired
type DeviceAuthState string

// Device auth states.
const (
	DeviceAuthStateNone       DeviceAuthState = "none"
	DeviceAuthStatePending    DeviceAuthState = "pending"
	DeviceAuthStateAuthorized DeviceAuthState = "authorized"
	DeviceAuthStateExpired    DeviceAuthState = "expired"
)

// TraktList follows a list on Trakt.
type TraktList struct {
	// ListType is the kind of Trakt list being followed.
	// +required
	ListType TraktListType `json:"listType"`

	// Username is the Trakt user the list belongs to.
	// +optional
	Username string `json:"username,omitempty"`

	// ListSlug is the slug of a user list, for listType "list".
	// +optional
	ListSlug string `json:"listSlug,omitempty"`

	// Limit caps how many entries are taken from the list.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Limit int32 `json:"limit,omitempty"`
}

// PlexWatchlist follows the authenticated user's Plex watchlist. It has no
// settings of its own; credentials come from the list's secretRef.
type PlexWatchlist struct{}

// TmdbList follows a TMDB list or a TMDB discover query.
type TmdbList struct {
	// ListID is the numeric ID of a TMDB list.
	// +optional
	ListID *string `json:"listID,omitempty"`

	// Discover is a TMDB discover query, as raw query parameters.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	Discover map[string]string `json:"discover,omitempty"`
}

// MdbList follows a list hosted on mdblist.com.
type MdbList struct {
	// URL is the mdblist list URL.
	// +required
	URL string `json:"url"`
}

// StevenLu follows the Steven Lu popular-movies feed. It has no settings.
type StevenLu struct{}

// CSVList follows a list of IMDb IDs stored in a ConfigMap.
type CSVList struct {
	// ConfigMapRef names the ConfigMap in the same namespace holding the CSV.
	// +required
	ConfigMapRef corev1.LocalObjectReference `json:"configMapRef"`
}

// CustomList follows an arbitrary JSON or RSS feed.
type CustomList struct {
	// URL is the feed URL.
	// +required
	URL string `json:"url"`

	// Format is the wire format the feed is served in.
	// +optional
	// +kubebuilder:default=json
	Format CustomListFormat `json:"format,omitempty"`
}

// ArrList follows the library of another *arr instance, including another
// Clustarr.
type ArrList struct {
	// BaseURL is the instance's base URL.
	// +required
	BaseURL string `json:"baseURL"`

	// Kind is the *arr flavour being followed.
	// +required
	Kind ArrKind `json:"kind"`
}

// ListDefaults are applied to every catalog item this list adds.
type ListDefaults struct {
	// QualityProfileRef is the QualityProfile added items are ranked against.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder added items are stored under.
	// +required
	RootFolderRef string `json:"rootFolderRef"`

	// DelayProfileRef pins a DelayProfile on added items.
	// +optional
	DelayProfileRef *string `json:"delayProfileRef,omitempty"`

	// Monitored is the monitored flag put on added items.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MonitorNewItems is the monitorNewItems mode put on added parents.
	// +optional
	// +kubebuilder:default=all
	MonitorNewItems MonitorNewItemsMode `json:"monitorNewItems,omitempty"`

	// MinimumAvailability is the minimum availability put on added movies.
	// +optional
	// +kubebuilder:default=released
	MinimumAvailability MinimumAvailability `json:"minimumAvailability,omitempty"`

	// SeriesType is the series type put on added series.
	// +optional
	// +kubebuilder:default=standard
	SeriesType SeriesType `json:"seriesType,omitempty"`

	// SeasonFolder is the season-folder flag put on added series.
	// +optional
	// +kubebuilder:default=true
	SeasonFolder *bool `json:"seasonFolder,omitempty"`

	// SearchOnAdd searches for an item as soon as the list adds it.
	// +optional
	// +kubebuilder:default=true
	SearchOnAdd *bool `json:"searchOnAdd,omitempty"`

	// Tags are applied to every item the list adds.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Tags []string `json:"tags,omitempty"`
}

// DeviceAuth tracks a device-code authorization flow, as Trakt and Plex use.
type DeviceAuth struct {
	// State is where the flow has got to.
	// +optional
	// +kubebuilder:default=none
	State DeviceAuthState `json:"state,omitempty"`

	// UserCode is the code the user types at the verification URL.
	// +optional
	UserCode string `json:"userCode,omitempty"`

	// VerificationURL is where the user goes to approve the flow.
	// +optional
	VerificationURL string `json:"verificationURL,omitempty"`

	// ExpiresAt is when the user code stops being accepted.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// TokenExpiresAt is when the granted access token expires.
	// +optional
	TokenExpiresAt *metav1.Time `json:"tokenExpiresAt,omitempty"`
}

// ImportListSpec defines the desired state of ImportList.
//
// +kubebuilder:validation:XValidation:rule="(has(self.trakt) ? 1 : 0) + (has(self.plex) ? 1 : 0) + (has(self.tmdb) ? 1 : 0) + (has(self.mdblist) ? 1 : 0) + (has(self.stevenLu) ? 1 : 0) + (has(self.imdbCSV) ? 1 : 0) + (has(self.custom) ? 1 : 0) + (has(self.arr) ? 1 : 0) == 1",message="exactly one of trakt, plex, tmdb, mdblist, stevenLu, imdbCSV, custom or arr must be set"
//
// A list may name only kinds its provider can yield (gap-fix ruling R-10):
// such a list is refused at admission, not skipped at sync time. The table
// is app/import/worker/importlist.YieldableKinds', and
// app/import/controller/importlist's TestAdmissionMatchesYieldableKinds holds
// the admission rules to it for every provider and kind.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.trakt) || has(self.plex) || has(self.tmdb) || has(self.mdblist) || has(self.imdbCSV)) || self.kinds.all(k, k == 'movie' || k == 'series')",message="trakt, plex, tmdb, mdblist and imdbCSV lists yield only movie and series"
// +kubebuilder:validation:XValidation:rule="!has(self.stevenLu) || self.kinds.all(k, k == 'movie')",message="a stevenLu list yields only movie"
// +kubebuilder:validation:XValidation:rule="!has(self.arr) || self.kinds.all(k, self.arr.kind == 'clustarr' || (self.arr.kind == 'radarr' && k == 'movie') || (self.arr.kind == 'sonarr' && k == 'series') || (self.arr.kind == 'lidarr' && k == 'album') || (self.arr.kind == 'readarr' && (k == 'book' || k == 'audiobook')))",message="an arr list yields only its instance's kinds: radarr movie, sonarr series, lidarr album, readarr book or audiobook, clustarr any"
type ImportListSpec struct {
	// Kinds are the catalog kinds this list may add.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=6
	// +kubebuilder:validation:items:Enum=movie;series;album;book;audiobook;comic
	Kinds []string `json:"kinds"`

	// Enabled turns the list on or off without deleting it.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// AutomaticAdd adds items as they appear; when false the list is only reported.
	// +optional
	// +kubebuilder:default=true
	AutomaticAdd *bool `json:"automaticAdd,omitempty"`

	// RefreshInterval is how often the list is polled. It is clamped up to the
	// provider minimum: 12h for trakt, tmdb and mdblist, 6h for plex and
	// custom, 15m for arr, 24h for stevenLu.
	// +optional
	RefreshInterval metav1.Duration `json:"refreshInterval,omitempty"`

	// SecretRef names a Secret in the same namespace holding the credentials.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Trakt follows a list on Trakt.
	// +optional
	Trakt *TraktList `json:"trakt,omitempty"`

	// Plex follows the authenticated user's Plex watchlist.
	// +optional
	Plex *PlexWatchlist `json:"plex,omitempty"`

	// Tmdb follows a TMDB list or discover query.
	// +optional
	Tmdb *TmdbList `json:"tmdb,omitempty"`

	// Mdblist follows a list hosted on mdblist.com.
	// +optional
	Mdblist *MdbList `json:"mdblist,omitempty"`

	// StevenLu follows the Steven Lu popular-movies feed.
	// +optional
	StevenLu *StevenLu `json:"stevenLu,omitempty"`

	// ImdbCSV follows a list of IMDb IDs stored in a ConfigMap.
	// +optional
	ImdbCSV *CSVList `json:"imdbCSV,omitempty"`

	// Custom follows an arbitrary JSON or RSS feed.
	// +optional
	Custom *CustomList `json:"custom,omitempty"`

	// Arr follows the library of another *arr instance.
	// +optional
	Arr *ArrList `json:"arr,omitempty"`

	// Defaults are applied to every catalog item this list adds.
	// +required
	Defaults ListDefaults `json:"defaults"`

	// SyncLevel says what happens to items that have fallen off the list.
	// +optional
	// +kubebuilder:default=logOnly
	SyncLevel SyncLevel `json:"syncLevel,omitempty"`
}

// ImportListStatus describes the observed state of ImportList. The list's own
// items live in the clustarr-importlist KV bucket, not here.
type ImportListStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the list's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastSyncAt is when the list was last polled.
	// +optional
	LastSyncAt *metav1.Time `json:"lastSyncAt,omitempty"`

	// NextSyncAt is when the list will next be polled.
	// +optional
	NextSyncAt *metav1.Time `json:"nextSyncAt,omitempty"`

	// ItemCount is how many entries the last sync returned.
	// +optional
	ItemCount int32 `json:"itemCount,omitempty"`

	// AddedCount is how many catalog items the last sync created.
	// +optional
	AddedCount int32 `json:"addedCount,omitempty"`

	// ExcludedCount is how many entries an ImportExclusion suppressed.
	// +optional
	ExcludedCount int32 `json:"excludedCount,omitempty"`

	// RemovedCount is how many catalog items the last sync removed or unmonitored.
	// +optional
	RemovedCount int32 `json:"removedCount,omitempty"`

	// Auth tracks an in-flight device-code authorization flow.
	// +optional
	Auth *DeviceAuth `json:"auth,omitempty"`

	// LastError is the most recent sync error.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=il,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Sync",type=string,JSONPath=`.spec.syncLevel`
// +kubebuilder:printcolumn:name="Items",type=integer,JSONPath=`.status.itemCount`
// +kubebuilder:printcolumn:name="Added",type=integer,JSONPath=`.status.addedCount`
// +kubebuilder:printcolumn:name="Last Sync",type=date,JSONPath=`.status.lastSyncAt`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ImportList is a remote list of desired media that catalogarr keeps the
// catalog in step with.
type ImportList struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImportListSpec   `json:"spec,omitempty"`
	Status ImportListStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// ImportListList contains a list of ImportList.
type ImportListList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImportList `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImportList{}, &ImportListList{})
}
