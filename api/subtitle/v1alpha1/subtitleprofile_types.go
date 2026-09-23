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

// HIPolicy says how hearing-impaired (SDH) subtitles are treated for one
// language of a profile.
//
// +kubebuilder:validation:Enum=required;prefer;excluded
type HIPolicy string

// Hearing-impaired policies.
const (
	// HIPolicyRequired only accepts hearing-impaired subtitles.
	HIPolicyRequired HIPolicy = "required"
	// HIPolicyPrefer accepts both, scoring hearing-impaired higher.
	HIPolicyPrefer HIPolicy = "prefer"
	// HIPolicyExcluded rejects hearing-impaired subtitles.
	HIPolicyExcluded HIPolicy = "excluded"
)

// HIExtension is the filename infix written before the extension for a
// hearing-impaired sidecar, e.g. Movie.en.sdh.srt.
//
// +kubebuilder:validation:Enum=sdh;hi;cc
type HIExtension string

// Hearing-impaired filename infixes.
const (
	HIExtensionSDH HIExtension = "sdh"
	HIExtensionHI  HIExtension = "hi"
	HIExtensionCC  HIExtension = "cc"
)

// SubtitleMod names a post-download transformation applied to a downloaded
// subtitle before it is written to disk.
//
// +kubebuilder:validation:Enum=removeHI;removeTags;ocrFixes;common;fixUppercase;reverseRTL;color
type SubtitleMod string

// Subtitle mods.
const (
	SubtitleModRemoveHI     SubtitleMod = "removeHI"
	SubtitleModRemoveTags   SubtitleMod = "removeTags"
	SubtitleModOCRFixes     SubtitleMod = "ocrFixes"
	SubtitleModCommon       SubtitleMod = "common"
	SubtitleModFixUppercase SubtitleMod = "fixUppercase"
	SubtitleModReverseRTL   SubtitleMod = "reverseRTL"
	SubtitleModColor        SubtitleMod = "color"
)

// SyncTool is the external tool used to re-time a subtitle against its audio.
//
// +kubebuilder:validation:Enum=ffsubsync;alass
type SyncTool string

// Sync tools.
const (
	SyncToolFFSubsync SyncTool = "ffsubsync"
	SyncToolAlass     SyncTool = "alass"
)

// SubtitleProfile condition types.
const (
	// SubtitleProfileConditionReady is True once the profile has been validated
	// and its wanted language keys computed.
	SubtitleProfileConditionReady = "Ready"
	// SubtitleProfileConditionInvalid is True when the profile cannot be used,
	// for example because it is a second default profile or its cutoff does not
	// name one of its languages.
	SubtitleProfileConditionInvalid = "Invalid"
)

// LanguageItem is one wanted language of a SubtitleProfile, together with the
// forced / hearing-impaired / audio flags that make it distinct from another
// entry for the same BCP-47 language.
type LanguageItem struct {
	// Key is the stable identity of this entry inside the profile, of the form
	// <language>[:forced][:hi], e.g. "en", "en:forced" or "pt-BR:hi". It is the
	// langKey referenced by spec.cutoff, SubtitleRequest.spec.languages and
	// SubtitleRequest.status.items[].langKey.
	//
	// The canonical key is derivable from language, forced and hi; the
	// controller rejects a key that does not match them.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*(:forced)?(:hi)?$`
	Key string `json:"key"`

	// Language is the BCP-47 language tag of the wanted subtitle, e.g. "en",
	// "pt-BR" or "zh-Hant".
	// +required
	// +kubebuilder:validation:MinLength=2
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*$`
	Language string `json:"language"`

	// Forced wants only forced subtitles (foreign-dialogue-only tracks) for
	// this entry.
	// +optional
	// +kubebuilder:default=false
	Forced bool `json:"forced,omitempty"`

	// HI says how hearing-impaired (SDH) subtitles are treated for this entry.
	// +optional
	// +kubebuilder:default="prefer"
	HI HIPolicy `json:"hi,omitempty"`

	// AudioExclude skips this entry when the media file already has an audio
	// track in the same language.
	// +optional
	// +kubebuilder:default=false
	AudioExclude bool `json:"audioExclude,omitempty"`

	// AudioOnlyInclude only wants this entry when the media file has an audio
	// track in the same language. It is the inverse of audioExclude and the two
	// may not both be true.
	// +optional
	// +kubebuilder:default=false
	AudioOnlyInclude bool `json:"audioOnlyInclude,omitempty"`
}

// ScorePct holds a percentage threshold per media kind. Values are whole
// percentages in the range 0-100; fractional thresholds are not supported
// because CRD schemas do not allow floating-point values.
type ScorePct struct {
	// Episode is the threshold applied to episode media files.
	// +optional
	// +kubebuilder:default=90
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Episode int32 `json:"episode,omitempty"`

	// Movie is the threshold applied to movie media files.
	// +optional
	// +kubebuilder:default=70
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Movie int32 `json:"movie,omitempty"`
}

// EmbeddedSpec controls how subtitle tracks already inside the container are
// treated when deciding whether a language is satisfied.
type EmbeddedSpec struct {
	// Extract writes a matching embedded track out as a sidecar instead of
	// searching providers for it. A pointer so a Go client can send an explicit
	// false; unset means true.
	// +optional
	// +kubebuilder:default=true
	Extract *bool `json:"extract,omitempty"`

	// IgnorePGS ignores image-based PGS tracks when matching embedded subtitles.
	// +optional
	// +kubebuilder:default=false
	IgnorePGS bool `json:"ignorePGS,omitempty"`

	// IgnoreVobSub ignores image-based VobSub tracks when matching embedded subtitles.
	// +optional
	// +kubebuilder:default=false
	IgnoreVobSub bool `json:"ignoreVobSub,omitempty"`

	// IgnoreASS ignores ASS/SSA tracks when matching embedded subtitles.
	// +optional
	// +kubebuilder:default=false
	IgnoreASS bool `json:"ignoreASS,omitempty"`

	// SkipCommentary ignores tracks whose title marks them as commentary.
	// A pointer so a Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	SkipCommentary *bool `json:"skipCommentary,omitempty"`
}

// SearchSpec paces provider searches for languages that are still wanted.
type SearchSpec struct {
	// Interval is the base delay between searches for a wanted language. A Go
	// client always sends a Duration, so captionarr floors a zero (or negative)
	// one to this default rather than treat it as a request to search
	// continuously.
	// +optional
	// +kubebuilder:default="6h"
	Interval metav1.Duration `json:"interval,omitempty"`

	// AdaptiveDelay is how long after a media file's release date searches keep
	// running at the base interval before backing off. A Go client always sends
	// a Duration, so captionarr floors a zero (or negative) one to this default
	// rather than treat it as a request; "1s" backs off after the first search.
	// +optional
	// +kubebuilder:default="504h"
	AdaptiveDelay metav1.Duration `json:"adaptiveDelay,omitempty"`

	// AdaptiveDelta is the interval used once adaptiveDelay has elapsed. A Go
	// client always sends a Duration, so captionarr floors a zero (or negative)
	// one to this default rather than treat it as a request.
	// +optional
	// +kubebuilder:default="168h"
	AdaptiveDelta metav1.Duration `json:"adaptiveDelta,omitempty"`
}

// UpgradeSpec controls re-searching languages that are already downloaded but
// scored below the cutoff.
type UpgradeSpec struct {
	// Enabled turns upgrade searches on. A pointer so a Go client can send an
	// explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Interval is the delay between upgrade searches for one language. A Go
	// client always sends a Duration, so captionarr floors a zero (or negative)
	// one to this default rather than treat it as a request to search
	// continuously.
	// +optional
	// +kubebuilder:default="12h"
	Interval metav1.Duration `json:"interval,omitempty"`

	// LookbackDays is how many days after the original download upgrades are
	// still attempted.
	// +optional
	// +kubebuilder:default=7
	// +kubebuilder:validation:Minimum=0
	LookbackDays int32 `json:"lookbackDays,omitempty"`

	// MinDeltaPoints is the minimum score improvement, in score points, that a
	// candidate must offer before it replaces the current subtitle.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	MinDeltaPoints int32 `json:"minDeltaPoints,omitempty"`
}

// SyncSpec configures subtitle re-timing. Deferred: it is parsed and validated
// in v1alpha1 but sync.enabled must be false.
//
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled",message="subtitle sync is not supported in v1alpha1: sync.enabled must be false"
type SyncSpec struct {
	// Enabled turns re-timing on. Must be false in v1alpha1.
	// +optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Tool is the re-timing tool to run.
	// +optional
	Tool SyncTool `json:"tool,omitempty"`

	// ThresholdPercent is the minimum subtitle score, per media kind, below
	// which a subtitle is not worth re-timing.
	// +optional
	// +kubebuilder:default={}
	ThresholdPercent ScorePct `json:"thresholdPercent,omitempty"`

	// MaxOffsetSeconds is the largest shift, in seconds, that may be applied
	// before the result is rejected as a mismatch.
	// +optional
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=0
	MaxOffsetSeconds int32 `json:"maxOffsetSeconds,omitempty"`

	// GSS uses golden-section search when aligning (ffsubsync). A pointer so a
	// Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	GSS *bool `json:"gss,omitempty"`

	// NoFixFramerate disables framerate-ratio correction during alignment.
	// A pointer so a Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	NoFixFramerate *bool `json:"noFixFramerate,omitempty"`
}

// WhisperSpec configures speech-to-text subtitle generation as a last resort.
// Deferred: it is parsed and validated in v1alpha1 but whisper.enabled must
// be false.
//
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled",message="whisper generation is not supported in v1alpha1: whisper.enabled must be false"
type WhisperSpec struct {
	// Enabled turns generation on. Must be false in v1alpha1.
	// +optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// ProviderRef is the name of the SubtitleProvider of type whisper to use.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ProviderRef string `json:"providerRef,omitempty"`
}

// SubtitleProfileSpec defines the desired state of SubtitleProfile.
//
// +kubebuilder:validation:XValidation:rule="!has(self.cutoff) || self.languages.exists(l, l.key == self.cutoff)",message="cutoff must be one of spec.languages[].key"
// +kubebuilder:validation:XValidation:rule="self.languages.all(l, !(l.audioExclude && l.audioOnlyInclude))",message="a language may not set both audioExclude and audioOnlyInclude"
type SubtitleProfileSpec struct {
	// Default marks this profile as the cluster default, used by media files no
	// other profile selects. Exactly one profile may be the default; the
	// controller sets Invalid on the newer one.
	// +optional
	// +kubebuilder:default=false
	Default bool `json:"default,omitempty"`

	// Selector matches MediaFile labels this profile applies to. Only video
	// kinds (movie, episode) are eligible; enforced by the controller.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Languages lists the wanted languages, keyed by langKey.
	// +required
	// +listType=map
	// +listMapKey=key
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=20
	Languages []LanguageItem `json:"languages"`

	// Cutoff is the langKey whose satisfaction stops further searching for the
	// media file. Unset means any wanted language satisfies the cutoff.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Cutoff *string `json:"cutoff,omitempty"`

	// MustContain lists regular expressions that must all match the release
	// information of a candidate subtitle for it to be accepted.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=256
	MustContain []string `json:"mustContain,omitempty"`

	// MustNotContain lists regular expressions that must none match the release
	// information of a candidate subtitle for it to be accepted.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=256
	MustNotContain []string `json:"mustNotContain,omitempty"`

	// OriginalFormat writes the subtitle in the format the provider served it
	// instead of converting it to SubRip.
	// +optional
	// +kubebuilder:default=false
	OriginalFormat bool `json:"originalFormat,omitempty"`

	// HIExtension is the filename infix used for hearing-impaired sidecars.
	// +optional
	// +kubebuilder:default="sdh"
	HIExtension HIExtension `json:"hiExtension,omitempty"`

	// LanguageEquals lists "<from>:<to>" aliases that make two language tags
	// interchangeable when matching candidates, e.g. "pt-BR:pt".
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:Pattern=`^[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*:[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*$`
	LanguageEquals []string `json:"languageEquals,omitempty"`

	// MinScorePercent is the minimum candidate score, as a percentage of the
	// maximum achievable score, required before a subtitle is downloaded.
	// +optional
	// +kubebuilder:default={}
	MinScorePercent ScorePct `json:"minScorePercent,omitempty"`

	// Embedded controls how subtitle tracks inside the container are used.
	// +optional
	// +kubebuilder:default={}
	Embedded EmbeddedSpec `json:"embedded,omitempty"`

	// Search paces provider searches for wanted languages.
	// +optional
	// +kubebuilder:default={}
	Search SearchSpec `json:"search,omitempty"`

	// Upgrade controls re-searching already downloaded languages.
	// +optional
	// +kubebuilder:default={}
	Upgrade UpgradeSpec `json:"upgrade,omitempty"`

	// Sync configures subtitle re-timing. Deferred in v1alpha1.
	// +optional
	Sync *SyncSpec `json:"sync,omitempty"`

	// Whisper configures speech-to-text generation. Deferred in v1alpha1.
	// +optional
	Whisper *WhisperSpec `json:"whisper,omitempty"`

	// Mods lists the transformations applied to a downloaded subtitle, in order.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=7
	Mods []SubtitleMod `json:"mods,omitempty"`

	// Providers is the ordered list of SubtitleProvider names to search. Empty
	// means every enabled provider, in priority order.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=253
	Providers []string `json:"providers,omitempty"`
}

// SubtitleProfileStatus defines the observed state of SubtitleProfile.
type SubtitleProfileStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// WantedKeys is the resolved, canonical set of langKeys this profile wants.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=64
	WantedKeys []string `json:"wantedKeys,omitempty"`

	// MatchingFiles is the number of MediaFiles selected by this profile.
	// +optional
	MatchingFiles int32 `json:"matchingFiles,omitempty"`

	// Conditions holds Ready and Invalid.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// SubtitleProfile describes the subtitle languages wanted for the MediaFiles
// it selects, and how candidates are scored, filtered and written.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Cluster,categories=clustarr
// +kubebuilder:printcolumn:name="Default",type="boolean",JSONPath=".spec.default"
// +kubebuilder:printcolumn:name="Cutoff",type="string",JSONPath=".spec.cutoff"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Matching",type="integer",JSONPath=".status.matchingFiles"
// +kubebuilder:printcolumn:name="Wanted",type="string",JSONPath=".status.wantedKeys",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type SubtitleProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec SubtitleProfileSpec `json:"spec,omitempty"`
	// +optional
	Status SubtitleProfileStatus `json:"status,omitempty"`
}

// SubtitleProfileList contains a list of SubtitleProfile.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type SubtitleProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubtitleProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubtitleProfile{}, &SubtitleProfileList{})
}
