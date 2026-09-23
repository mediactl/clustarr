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

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on an Indexer.
const (
	// IndexerConditionReady is True when the indexer is configured, reachable and usable.
	IndexerConditionReady = "Ready"
	// IndexerConditionAuthenticated is True when the indexer accepted the configured credentials.
	IndexerConditionAuthenticated = "Authenticated"
	// IndexerConditionHealthy is True when recent requests to the indexer succeeded.
	IndexerConditionHealthy = "Healthy"
	// IndexerConditionRateLimited is True while the indexer's query or grab limit is exhausted.
	IndexerConditionRateLimited = "RateLimited"
)

// LimitUnit is the window over which query and grab limits are counted.
//
// +kubebuilder:validation:Enum=day;hour
type LimitUnit string

// Limit units.
const (
	LimitUnitDay  LimitUnit = "day"
	LimitUnitHour LimitUnit = "hour"
)

// GenericNewznab configures a plain Newznab/Torznab upstream, including
// Prowlarr and Jackett, instead of a Cardigann definition.
type GenericNewznab struct {
	// Protocol is the transfer protocol the upstream serves.
	// +required
	Protocol commonv1alpha1.Protocol `json:"protocol"`

	// APIPath is the path of the Newznab/Torznab API relative to baseURL.
	// +optional
	// +kubebuilder:default="/api"
	APIPath string `json:"apiPath,omitempty"`
}

// Limits caps how many queries and grabs may be issued per window.
type Limits struct {
	// QueryLimit is the maximum number of search queries per unit.
	// +optional
	QueryLimit *int32 `json:"queryLimit,omitempty"`

	// GrabLimit is the maximum number of grabs per unit.
	// +optional
	GrabLimit *int32 `json:"grabLimit,omitempty"`

	// Unit is the window the limits apply to.
	// +optional
	// +kubebuilder:default=day
	Unit LimitUnit `json:"unit,omitempty"`
}

// IndexerSpec defines the desired state of Indexer.
//
// +kubebuilder:validation:XValidation:rule="(has(self.definition) ? 1 : 0) + (has(self.definitionRef) ? 1 : 0) + (has(self.generic) ? 1 : 0) == 1",message="exactly one of definition, definitionRef or generic must be set"
type IndexerSpec struct {
	// Definition is the id of a bundled Cardigann definition, e.g. "1337x".
	// +optional
	Definition *string `json:"definition,omitempty"`

	// DefinitionRef is the name of an IndexerDefinition to use.
	// +optional
	DefinitionRef *string `json:"definitionRef,omitempty"`

	// Generic configures a plain Newznab/Torznab upstream.
	// +optional
	Generic *GenericNewznab `json:"generic,omitempty"`

	// BaseURL is the indexer's base URL.
	// +required
	BaseURL string `json:"baseURL"`

	// Enabled turns the indexer on or off without deleting it.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Priority orders indexers when the same release is offered by several; lower wins.
	// +optional
	// +kubebuilder:default=25
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	Priority int32 `json:"priority,omitempty"`

	// Settings holds the non-secret Cardigann settings for the definition.
	// +optional
	Settings map[string]string `json:"settings,omitempty"`

	// SecretRef names a Secret in the same namespace holding secret settings.
	// Recognised keys: apikey, username, password, cookie, passkey, rss_key.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// EnableRss allows the indexer to be polled for new releases.
	// +optional
	// +kubebuilder:default=true
	EnableRss *bool `json:"enableRss,omitempty"`

	// EnableAutomaticSearch allows the indexer to be used by automatic searches.
	// +optional
	// +kubebuilder:default=true
	EnableAutomaticSearch *bool `json:"enableAutomaticSearch,omitempty"`

	// EnableInteractiveSearch allows the indexer to be used by interactive searches.
	// +optional
	// +kubebuilder:default=true
	EnableInteractiveSearch *bool `json:"enableInteractiveSearch,omitempty"`

	// RssInterval is how often the RSS feed is polled. A Go client always
	// sends a Duration, so the RSS worker floors a zero (or negative) one to
	// this default: nobody means to poll an indexer infinitely often.
	// +optional
	// +kubebuilder:default="15m"
	RssInterval metav1.Duration `json:"rssInterval,omitempty"`

	// Limits caps queries and grabs per window.
	// +optional
	Limits *Limits `json:"limits,omitempty"`

	// RequestDelay is the minimum delay between requests to the indexer. It
	// is raised to the definition's requestDelay when that is larger. "0s"
	// is a supported "do not pace this indexer", so this is a pointer rather
	// than floored: a Go client always sends a non-pointer Duration, and the
	// 2s default would never reach an Indexer created from Go. Unset means 2s.
	// +optional
	// +kubebuilder:default="2s"
	RequestDelay *metav1.Duration `json:"requestDelay,omitempty"`

	// Timeout is the per-request HTTP timeout. A Go client always sends a
	// Duration, so every consumer floors a zero (or negative) one to this
	// default: a request that may never time out is never meant.
	// +optional
	// +kubebuilder:default="30s"
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// ProxyRef names an IndexerProxy in the same namespace to route requests through.
	// +optional
	ProxyRef *string `json:"proxyRef,omitempty"`

	// Categories are the Newznab category ids to search.
	// +optional
	Categories []int32 `json:"categories,omitempty"`

	// AnimeCategories are the Newznab category ids to search for anime.
	// +optional
	AnimeCategories []int32 `json:"animeCategories,omitempty"`

	// AnimeStandardFormatSearch also searches anime using standard SxxEyy numbering.
	// +optional
	AnimeStandardFormatSearch bool `json:"animeStandardFormatSearch,omitempty"`

	// MinimumSeeders is the fewest seeders a torrent release may have to be considered.
	// +optional
	// +kubebuilder:default=1
	MinimumSeeders int32 `json:"minimumSeeders,omitempty"`

	// SeedCriteria overrides the download client's seeding limits for releases from this indexer.
	// +optional
	SeedCriteria *commonv1alpha1.SeedCriteria `json:"seedCriteria,omitempty"`

	// DownloadClientRef names the DownloadClient to send grabs from this indexer to.
	// +optional
	DownloadClientRef *string `json:"downloadClientRef,omitempty"`

	// Tags are free-form labels used to match the indexer to catalog items.
	// +optional
	Tags []string `json:"tags,omitempty"`
}

// SubCategory is a leaf Newznab category, such as 2040 "Movies/HD" under the
// 2000 "Movies" parent.
type SubCategory struct {
	// ID is the Newznab category id.
	// +optional
	ID int32 `json:"id,omitempty"`

	// Name is the sub-category's display name.
	// +optional
	Name string `json:"name,omitempty"`
}

// Category is a Newznab category with its sub-categories.
//
// The spec writes Sub as []Category, i.e. an arbitrarily deep tree. A CRD
// schema cannot be recursive: controller-gen truncates the recursion to
// `items: {}` and the apiserver then rejects the CRD with "must not be empty
// for specified array items". The Newznab category tree is exactly two levels
// deep (a 1000-aligned parent and its leaves), so Sub is []SubCategory, which
// carries the same id/name payload with a schema the apiserver accepts.
type Category struct {
	// ID is the Newznab category id.
	// +optional
	ID int32 `json:"id,omitempty"`

	// Name is the category's display name.
	// +optional
	Name string `json:"name,omitempty"`

	// Sub lists the sub-categories of this category.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Sub []SubCategory `json:"sub,omitempty"`
}

// Caps is the capability set reported by the indexer.
type Caps struct {
	// Modes maps a search mode to the query parameters it supports.
	//
	// The keys are Torznab's own wire values, which pkg/torznab implements
	// and torznab.Caps.Supports compares against: "search", "tvsearch",
	// "movie", "music", "audio", "book" (pkg/torznab/caps.go). They are NOT
	// "tv-search"/"movie-search" -- this comment said so until Phase D1, and
	// because there is no enum marker on the map key nothing would have
	// caught it: the caps gate would simply never match, and no indexer
	// would ever be queried.
	// +optional
	Modes map[string][]string `json:"modes,omitempty"`

	// Categories is the category tree the indexer exposes.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Categories []Category `json:"categories,omitempty"`

	// LimitsMax is the maximum page size the indexer accepts.
	// +optional
	LimitsMax int32 `json:"limitsMax,omitempty"`

	// LimitsDefault is the page size the indexer uses when none is requested.
	// +optional
	LimitsDefault int32 `json:"limitsDefault,omitempty"`

	// SupportsRawSearch is true when the indexer accepts free-text queries.
	// +optional
	SupportsRawSearch bool `json:"supportsRawSearch,omitempty"`
}

// IndexerStatus defines the observed state of Indexer.
type IndexerStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the indexer's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Protocol is the transfer protocol resolved from the definition.
	// +optional
	Protocol commonv1alpha1.Protocol `json:"protocol,omitempty"`

	// Privacy is the privacy class resolved from the definition.
	// +optional
	Privacy string `json:"privacy,omitempty"`

	// Caps is the capability set last fetched from the indexer.
	// +optional
	Caps *Caps `json:"caps,omitempty"`

	// EscalationLevel is the current back-off step after repeated failures.
	// +optional
	EscalationLevel int32 `json:"escalationLevel,omitempty"`

	// DisabledUntil is when the indexer will be retried after back-off.
	// +optional
	DisabledUntil *metav1.Time `json:"disabledUntil,omitempty"`

	// InitialFailureAt is when the current run of failures began.
	// +optional
	InitialFailureAt *metav1.Time `json:"initialFailureAt,omitempty"`

	// LastFailureAt is when the most recent failure happened.
	// +optional
	LastFailureAt *metav1.Time `json:"lastFailureAt,omitempty"`

	// LastFailure describes the most recent failure.
	// +optional
	LastFailure string `json:"lastFailure,omitempty"`

	// QueriesInWindow is the number of queries issued in the current limit window.
	// +optional
	QueriesInWindow int32 `json:"queriesInWindow,omitempty"`

	// GrabsInWindow is the number of grabs issued in the current limit window.
	// +optional
	GrabsInWindow int32 `json:"grabsInWindow,omitempty"`

	// LastRssAt is when the RSS feed was last polled.
	// +optional
	LastRssAt *metav1.Time `json:"lastRssAt,omitempty"`

	// LastRssNewCount is how many new releases the last RSS poll returned.
	// +optional
	LastRssNewCount int32 `json:"lastRssNewCount,omitempty"`

	// IndexedReleases is the total number of releases seen from this indexer.
	// +optional
	IndexedReleases int64 `json:"indexedReleases,omitempty"`

	// SessionSecretRef names the Secret holding the indexer's login session.
	// +optional
	SessionSecretRef string `json:"sessionSecretRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=idx,categories=clustarr
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.status.protocol`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Healthy",type=string,JSONPath=`.status.conditions[?(@.type=="Healthy")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Indexer is a configured torrent or usenet indexer.
type Indexer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IndexerSpec   `json:"spec,omitempty"`
	Status IndexerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// IndexerList contains a list of Indexer.
type IndexerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Indexer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Indexer{}, &IndexerList{})
}
