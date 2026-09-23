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

// SubtitleProviderType is the upstream subtitle source a SubtitleProvider
// talks to.
//
// +kubebuilder:validation:Enum=opensubtitlescom;gestdown;subdl;subsource;embedded;whisper
type SubtitleProviderType string

// Subtitle provider types.
const (
	// SubtitleProviderOpenSubtitlesCom is the opensubtitles.com REST API.
	SubtitleProviderOpenSubtitlesCom SubtitleProviderType = "opensubtitlescom"
	// SubtitleProviderGestdown is the gestdown.info (Addic7ed) mirror.
	SubtitleProviderGestdown SubtitleProviderType = "gestdown"
	// SubtitleProviderSubDL is subdl.com.
	SubtitleProviderSubDL SubtitleProviderType = "subdl"
	// SubtitleProviderSubSource is subsource.net.
	SubtitleProviderSubSource SubtitleProviderType = "subsource"
	// SubtitleProviderEmbedded extracts subtitles already inside the container.
	SubtitleProviderEmbedded SubtitleProviderType = "embedded"
	// SubtitleProviderWhisper generates subtitles from the audio track.
	SubtitleProviderWhisper SubtitleProviderType = "whisper"
)

// SubtitleProvider condition types.
const (
	// SubtitleProviderConditionReady is True when the provider is enabled,
	// authenticated and not throttled.
	SubtitleProviderConditionReady = "Ready"
	// SubtitleProviderConditionAuthenticated is True once credentials from the
	// referenced Secret have been accepted by the upstream provider.
	SubtitleProviderConditionAuthenticated = "Authenticated"
	// SubtitleProviderConditionThrottled is True while the provider is backing
	// off because of rate limits, quota exhaustion or upstream errors.
	SubtitleProviderConditionThrottled = "Throttled"
)

// Well-known keys of the Secret named by SubtitleProviderSpec.SecretRef.
const (
	// ProviderSecretKeyAPIKey holds the provider API key.
	ProviderSecretKeyAPIKey = "apiKey"
	// ProviderSecretKeyUsername holds the provider account username.
	ProviderSecretKeyUsername = "username"
	// ProviderSecretKeyPassword holds the provider account password.
	ProviderSecretKeyPassword = "password"
)

// Well-known keys of SubtitleProviderSpec.Options.
const (
	// ProviderOptionAITranslated controls machine-translated results
	// ("exclude" or "include").
	ProviderOptionAITranslated = "aiTranslated"
	// ProviderOptionTrustedSources restricts results to trusted uploaders.
	ProviderOptionTrustedSources = "trustedSources"
	// ProviderOptionVIP says the account has VIP/premium status.
	ProviderOptionVIP = "vip"
)

// ProviderQuota is the download allowance the provider last reported.
type ProviderQuota struct {
	// Remaining is the number of downloads left in the current quota window.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Remaining int32 `json:"remaining,omitempty"`

	// ResetAt is when the quota window rolls over.
	// +optional
	ResetAt metav1.Time `json:"resetAt,omitempty"`
}

// SubtitleProviderSpec defines the desired state of SubtitleProvider.
type SubtitleProviderSpec struct {
	// Type is the upstream subtitle source. It is immutable: point a new
	// SubtitleProvider at a different source instead.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="type is immutable"
	Type SubtitleProviderType `json:"type"`

	// Enabled allows searches against this provider. A pointer so a Go client
	// can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Priority orders providers when a profile does not list them explicitly;
	// lower is searched first.
	// +optional
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Priority int32 `json:"priority,omitempty"`

	// SecretRef names a Secret in the same namespace holding the provider
	// credentials. Recognised keys: apiKey, username, password. Credentials are
	// never inlined in the spec.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Endpoint overrides the provider base URL, for mirrors or local instances.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Endpoint *string `json:"endpoint,omitempty"`

	// Options holds non-secret provider settings such as aiTranslated=exclude,
	// trustedSources=true or vip=true.
	// +optional
	Options map[string]string `json:"options,omitempty"`

	// Languages restricts this provider to the given BCP-47 language tags.
	// Empty means the provider is searched for every wanted language.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=32
	Languages []string `json:"languages,omitempty"`

	// RequestsPerSecondMilli is the client-side rate limit in thousandths of a
	// request per second, so 5 requests per second is 5000. A scaled integer is
	// used because CRD schemas do not allow floating-point values.
	// +optional
	// +kubebuilder:default=5000
	// +kubebuilder:validation:Minimum=1
	RequestsPerSecondMilli int32 `json:"requestsPerSecondMilli,omitempty"`
}

// SubtitleProviderStatus defines the observed state of SubtitleProvider.
type SubtitleProviderStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ThrottledUntil is when the provider may be searched again. Unset means
	// the provider is not throttled.
	// +optional
	ThrottledUntil *metav1.Time `json:"throttledUntil,omitempty"`

	// ThrottleReason explains the current throttle.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	ThrottleReason string `json:"throttleReason,omitempty"`

	// Quota is the download allowance the provider last reported.
	// +optional
	Quota *ProviderQuota `json:"quota,omitempty"`

	// TokenExpiresAt is when the cached login token stops being valid.
	// +optional
	TokenExpiresAt *metav1.Time `json:"tokenExpiresAt,omitempty"`

	// LastSuccessAt is when a request to this provider last succeeded.
	// +optional
	LastSuccessAt *metav1.Time `json:"lastSuccessAt,omitempty"`

	// ErrorsLast120s is the number of failed requests in the last two minutes,
	// used to decide when to throttle.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ErrorsLast120s int32 `json:"errorsLast120s,omitempty"`

	// HIVerifiable is true when this provider reports a trustworthy
	// hearing-impaired flag, so HI policies can be enforced against it.
	// +optional
	HIVerifiable bool `json:"hiVerifiable,omitempty"`

	// Conditions holds Ready, Authenticated and Throttled.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// SubtitleProvider is one upstream subtitle source, its credentials and its
// current rate-limit state.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,categories=clustarr
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Enabled",type="boolean",JSONPath=".spec.enabled"
// +kubebuilder:printcolumn:name="Priority",type="integer",JSONPath=".spec.priority"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Throttled",type="string",JSONPath=".status.conditions[?(@.type==\"Throttled\")].status",priority=1
// +kubebuilder:printcolumn:name="Quota",type="integer",JSONPath=".status.quota.remaining",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type SubtitleProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec SubtitleProviderSpec `json:"spec,omitempty"`
	// +optional
	Status SubtitleProviderStatus `json:"status,omitempty"`
}

// SubtitleProviderList contains a list of SubtitleProvider.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type SubtitleProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubtitleProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubtitleProvider{}, &SubtitleProviderList{})
}
