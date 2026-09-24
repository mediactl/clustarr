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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on a MetadataProvider.
const (
	// MetadataProviderConditionReady is True when the provider is usable.
	MetadataProviderConditionReady = "Ready"
	// MetadataProviderConditionAuthenticated is True when the provider accepted the credentials.
	MetadataProviderConditionAuthenticated = "Authenticated"
	// MetadataProviderConditionThrottled is True while the provider's quota is exhausted.
	MetadataProviderConditionThrottled = "Throttled"
)

// MetadataProviderType is the upstream metadata service a provider talks to.
//
// +kubebuilder:validation:Enum=tmdb;tvdb;musicbrainz;coverart;fanart;openlibrary;hardcover;audnexus;comicvine;metron;mangadex;anilist;kitsu;animelists;mdblist;omdb
type MetadataProviderType string

// Metadata provider types.
const (
	MetadataProviderTMDB        MetadataProviderType = "tmdb"
	MetadataProviderTVDB        MetadataProviderType = "tvdb"
	MetadataProviderMusicBrainz MetadataProviderType = "musicbrainz"
	MetadataProviderCoverArt    MetadataProviderType = "coverart"
	MetadataProviderFanart      MetadataProviderType = "fanart"
	MetadataProviderOpenLibrary MetadataProviderType = "openlibrary"
	MetadataProviderHardcover   MetadataProviderType = "hardcover"
	MetadataProviderAudnexus    MetadataProviderType = "audnexus"
	MetadataProviderComicVine   MetadataProviderType = "comicvine"
	MetadataProviderMetron      MetadataProviderType = "metron"
	MetadataProviderMangaDex    MetadataProviderType = "mangadex"
	MetadataProviderAniList     MetadataProviderType = "anilist"
	MetadataProviderKitsu       MetadataProviderType = "kitsu"
	MetadataProviderAnimeLists  MetadataProviderType = "animelists"
	// MetadataProviderMDBList is ratings-only (spec §C.2): imdb, tmdb,
	// rottenTomatoesCritic, rottenTomatoesAudience, metacritic, trakt,
	// letterboxd. Takes secretRef key apiKey.
	MetadataProviderMDBList MetadataProviderType = "mdblist"
	// MetadataProviderOMDb is ratings-only (spec §C.2): imdb,
	// rottenTomatoesCritic, metacritic. Takes secretRef key apiKey.
	MetadataProviderOMDb MetadataProviderType = "omdb"
)

// Secret keys recognised in MetadataProviderSpec.SecretRef.
const (
	MetadataSecretKeyAPIKey = "apiKey"
	MetadataSecretKeyPin    = "pin"
	MetadataSecretKeyBearer = "bearer"
)

// RateLimit caps how fast a provider may be called.
type RateLimit struct {
	// RequestsPerSecond is the sustained request rate, written as a decimal
	// quantity such as "1.5" rather than a float, which CRD schemas do not allow.
	// +optional
	RequestsPerSecond *resource.Quantity `json:"requestsPerSecond,omitempty"`

	// Burst is how many requests may be issued back to back.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Burst int32 `json:"burst,omitempty"`

	// PerDay caps the total number of requests per calendar day.
	// +optional
	// +kubebuilder:validation:Minimum=1
	PerDay *int32 `json:"perDay,omitempty"`
}

// CacheTTL sets how long each class of provider answer is cached.
type CacheTTL struct {
	// Announced is the TTL for items that are announced but not yet released.
	// +optional
	Announced metav1.Duration `json:"announced,omitempty"`

	// Released is the TTL for items that have been released.
	// +optional
	Released metav1.Duration `json:"released,omitempty"`

	// Ended is the TTL for items whose production has ended.
	// +optional
	Ended metav1.Duration `json:"ended,omitempty"`

	// Search is the TTL for search results.
	// +optional
	Search metav1.Duration `json:"search,omitempty"`

	// Crosswalk is the TTL for ID crosswalk lookups between providers.
	// +optional
	Crosswalk metav1.Duration `json:"crosswalk,omitempty"`
}

// MetadataProviderSpec defines the desired state of MetadataProvider.
//
// +kubebuilder:validation:XValidation:rule="!(self.type in ['musicbrainz','openlibrary']) || (has(self.contactUserAgent) && self.contactUserAgent.size() != 0)",message="contactUserAgent is required for musicbrainz and openlibrary"
type MetadataProviderSpec struct {
	// Type is the upstream metadata service; it identifies the provider and is
	// immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type MetadataProviderType `json:"type"`

	// Enabled turns the provider on or off without deleting it.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Priority orders providers that can answer the same question; lower wins.
	// +optional
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Priority int32 `json:"priority,omitempty"`

	// SecretRef names a Secret in the same namespace holding the credentials.
	// Recognised keys are apiKey, pin and bearer.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// BaseURL overrides the provider's default endpoint.
	// +optional
	BaseURL *string `json:"baseURL,omitempty"`

	// Region is the ISO 3166-1 region used for release dates and certifications.
	// +optional
	// +kubebuilder:default="US"
	Region string `json:"region,omitempty"`

	// Language is the BCP-47 language metadata is requested in.
	// +optional
	// +kubebuilder:default="en"
	Language string `json:"language,omitempty"`

	// RateLimit caps how fast the provider may be called.
	// +optional
	RateLimit *RateLimit `json:"rateLimit,omitempty"`

	// ContactUserAgent is the contact string sent in the User-Agent header. It
	// is required by musicbrainz and openlibrary.
	// +optional
	ContactUserAgent string `json:"contactUserAgent,omitempty"`

	// CacheTTL sets how long each class of answer is cached.
	// +optional
	CacheTTL *CacheTTL `json:"cacheTTL,omitempty"`
}

// MetadataProviderStatus describes the observed state of MetadataProvider.
type MetadataProviderStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the provider's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// QuotaRemaining is how many requests are left in the current quota window.
	// +optional
	QuotaRemaining *int32 `json:"quotaRemaining,omitempty"`

	// QuotaResetAt is when the quota window rolls over.
	// +optional
	QuotaResetAt *metav1.Time `json:"quotaResetAt,omitempty"`

	// TokenExpiresAt is when the current access token expires.
	// +optional
	TokenExpiresAt *metav1.Time `json:"tokenExpiresAt,omitempty"`

	// ThrottledUntil is when calls to the provider may resume.
	// +optional
	ThrottledUntil *metav1.Time `json:"throttledUntil,omitempty"`

	// LastSuccessAt is when the provider last answered successfully.
	// +optional
	LastSuccessAt *metav1.Time `json:"lastSuccessAt,omitempty"`

	// LastError is the most recent error returned by the provider.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=mdp,categories=clustarr;catalog
// +kubebuilder:selectablefield:JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Throttled",type=string,JSONPath=`.status.conditions[?(@.type=="Throttled")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MetadataProvider is a configured upstream metadata service.
type MetadataProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MetadataProviderSpec   `json:"spec,omitempty"`
	Status MetadataProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// MetadataProviderList contains a list of MetadataProvider.
type MetadataProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MetadataProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MetadataProvider{}, &MetadataProviderList{})
}
