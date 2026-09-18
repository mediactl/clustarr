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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on a QualityProfile.
const (
	// QualityProfileConditionReady is True when the profile resolved against the format catalogue.
	QualityProfileConditionReady = "Ready"
	// QualityProfileConditionInvalid is True when the profile references unknown formats or qualities.
	QualityProfileConditionInvalid = "Invalid"
)

// ProfileMediaKind is the family of media a QualityProfile applies to.
//
// +kubebuilder:validation:Enum=video;music;book;audiobook;comic
type ProfileMediaKind string

// Quality profile media kinds.
const (
	ProfileMediaKindVideo     ProfileMediaKind = "video"
	ProfileMediaKindMusic     ProfileMediaKind = "music"
	ProfileMediaKindBook      ProfileMediaKind = "book"
	ProfileMediaKindAudiobook ProfileMediaKind = "audiobook"
	ProfileMediaKindComic     ProfileMediaKind = "comic"
)

// ScoreSet selects which TRaSH custom-format score column is applied.
//
// +kubebuilder:validation:Enum=default;anime-radarr;anime-sonarr
type ScoreSet string

// Score sets.
const (
	ScoreSetDefault     ScoreSet = "default"
	ScoreSetAnimeRadarr ScoreSet = "anime-radarr"
	ScoreSetAnimeSonarr ScoreSet = "anime-sonarr"
)

// ProperPolicy says how propers and repacks are treated.
//
// +kubebuilder:validation:Enum=preferAndUpgrade;doNotUpgrade;doNotPrefer
type ProperPolicy string

// Proper policies.
const (
	ProperPolicyPreferAndUpgrade ProperPolicy = "preferAndUpgrade"
	ProperPolicyDoNotUpgrade     ProperPolicy = "doNotUpgrade"
	ProperPolicyDoNotPrefer      ProperPolicy = "doNotPrefer"
)

// SizeTable selects the built-in size limit table used when no explicit
// SizeLimit overrides a quality.
//
// +kubebuilder:validation:Enum=movie;series;anime;none
type SizeTable string

// Size tables.
const (
	SizeTableMovie  SizeTable = "movie"
	SizeTableSeries SizeTable = "series"
	SizeTableAnime  SizeTable = "anime"
	SizeTableNone   SizeTable = "none"
)

// PreferredProtocol is the transfer protocol a profile ranks above the other.
//
// +kubebuilder:validation:Enum=usenet;torrent;any
type PreferredProtocol string

// Preferred protocols.
const (
	PreferredProtocolUsenet  PreferredProtocol = "usenet"
	PreferredProtocolTorrent PreferredProtocol = "torrent"
	PreferredProtocolAny     PreferredProtocol = "any"
)

// Optional TRaSH custom-format families that may be switched on per profile.
const (
	FormatGroupAudio            = "audio"
	FormatGroupMovieVersions    = "movieVersions"
	FormatGroupStreamingBoost   = "streamingBoost"
	FormatGroupSeasonPack       = "seasonPack"
	FormatGroupUnwantedOptional = "unwantedOptional"
)

// Tier is one rung of a quality profile; every quality in a tier is considered
// equivalent, and tiers are listed best first.
type Tier struct {
	// Name is the tier name; it is what Cutoff refers to.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// Qualities lists the canonical quality names or aliases in this tier.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=64
	Qualities []string `json:"qualities"`
}

// FormatScore overrides the catalogue score of one custom format.
type FormatScore struct {
	// Format is the custom-format catalogue slug.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Format string `json:"format"`

	// Score is the score applied when the format matches.
	// +required
	Score int32 `json:"score"`
}

// SizeLimit overrides the size table for one quality. The values are megabytes
// per minute of runtime, written as decimal quantities such as "12.5" rather
// than floats, which CRD schemas do not allow.
type SizeLimit struct {
	// Quality is the canonical quality name the limits apply to.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Quality string `json:"quality"`

	// MinMBPerMinute is the smallest accepted size in MB per runtime minute.
	// +optional
	MinMBPerMinute *resource.Quantity `json:"minMBPerMinute,omitempty"`

	// PreferredMBPerMinute is the ideal size in MB per runtime minute.
	// +optional
	PreferredMBPerMinute *resource.Quantity `json:"preferredMBPerMinute,omitempty"`

	// MaxMBPerMinute is the largest accepted size in MB per runtime minute.
	// +optional
	MaxMBPerMinute *resource.Quantity `json:"maxMBPerMinute,omitempty"`
}

// QualityProfileSpec defines the desired state of QualityProfile.
//
// +kubebuilder:validation:XValidation:rule="!oldSelf.builtIn || self == oldSelf",message="built-in profiles are immutable; copy the profile instead"
// +kubebuilder:validation:XValidation:rule="self.tiers.exists(t, t.name == self.cutoff)",message="cutoff must be the name of one of the tiers"
type QualityProfileSpec struct {
	// MediaKind is the family of media this profile applies to.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mediaKind is immutable"
	MediaKind ProfileMediaKind `json:"mediaKind"`

	// BuiltIn marks a profile shipped by the chart; built-ins are immutable and
	// are meant to be copied rather than edited.
	// +optional
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="builtIn is immutable"
	BuiltIn bool `json:"builtIn"`

	// Tiers lists the quality tiers best first, in TRaSH order.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=40
	Tiers []Tier `json:"tiers"`

	// Cutoff is the name of the tier at which upgrading stops.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Cutoff string `json:"cutoff"`

	// UpgradeAllowed lets an already-imported item be replaced by a better release.
	// +optional
	// +kubebuilder:default=true
	UpgradeAllowed *bool `json:"upgradeAllowed,omitempty"`

	// MinFormatScore is the lowest custom-format score a release may have.
	// +optional
	// +kubebuilder:default=0
	MinFormatScore int32 `json:"minFormatScore,omitempty"`

	// CutoffFormatScore is the score at which format upgrading stops.
	// +optional
	// +kubebuilder:default=10000
	CutoffFormatScore int32 `json:"cutoffFormatScore,omitempty"`

	// MinUpgradeFormatScore is the score improvement an upgrade must deliver.
	// +optional
	// +kubebuilder:default=1
	MinUpgradeFormatScore int32 `json:"minUpgradeFormatScore,omitempty"`

	// ScoreSet selects which TRaSH score column is applied.
	// +optional
	// +kubebuilder:default=default
	ScoreSet ScoreSet `json:"scoreSet,omitempty"`

	// EnabledFormatGroups switches on optional TRaSH custom-format families.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Enum=audio;movieVersions;streamingBoost;seasonPack;unwantedOptional
	EnabledFormatGroups []string `json:"enabledFormatGroups,omitempty"`

	// FormatScores overrides individual catalogue scores. An unknown slug raises
	// the Invalid condition.
	// +optional
	// +listType=map
	// +listMapKey=format
	// +kubebuilder:validation:MaxItems=200
	FormatScores []FormatScore `json:"formatScores,omitempty"`

	// Language is the accepted language: "original", "any" or a BCP-47 tag.
	// +optional
	// +kubebuilder:default="original"
	// +kubebuilder:validation:MaxLength=64
	Language string `json:"language,omitempty"`

	// ProperPolicy says how propers and repacks are treated.
	// +optional
	// +kubebuilder:default=preferAndUpgrade
	ProperPolicy ProperPolicy `json:"properPolicy,omitempty"`

	// SizeTable selects the built-in size limit table.
	// +optional
	// +kubebuilder:default=movie
	SizeTable SizeTable `json:"sizeTable,omitempty"`

	// SizeLimits overrides the size table per quality.
	// +optional
	// +listType=map
	// +listMapKey=quality
	// +kubebuilder:validation:MaxItems=200
	SizeLimits []SizeLimit `json:"sizeLimits,omitempty"`

	// PreferredProtocol ranks one protocol above the other. It is a ranking key
	// only; grab delays live in DelayProfile.
	// +optional
	// +kubebuilder:default=any
	PreferredProtocol PreferredProtocol `json:"preferredProtocol,omitempty"`
}

// QualityProfileStatus describes the observed state of QualityProfile.
type QualityProfileStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the profile's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// CatalogueVersion is the custom-format catalogue the profile resolved against.
	// +optional
	CatalogueVersion string `json:"catalogueVersion,omitempty"`

	// ResolvedFormats is the number of custom formats the profile scores.
	// +optional
	ResolvedFormats int32 `json:"resolvedFormats,omitempty"`

	// QualityOrder is the flattened canonical quality order, best first. It is
	// capped at tiers (40) times qualities per tier (8).
	// +optional
	// +kubebuilder:validation:MaxItems=320
	QualityOrder []string `json:"qualityOrder,omitempty"`

	// Hash identifies the resolved profile; MediaFile records it at import so
	// that a profile change can be detected.
	// +optional
	Hash string `json:"hash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Cluster,shortName=qp,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Media",type=string,JSONPath=`.spec.mediaKind`
// +kubebuilder:printcolumn:name="Cutoff",type=string,JSONPath=`.spec.cutoff`
// +kubebuilder:printcolumn:name="Score Set",type=string,JSONPath=`.spec.scoreSet`
// +kubebuilder:printcolumn:name="Built-In",type=boolean,JSONPath=`.spec.builtIn`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// QualityProfile is an opinionated, TRaSH-shaped ranking of qualities and
// custom formats.
type QualityProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QualityProfileSpec   `json:"spec,omitempty"`
	Status QualityProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// QualityProfileList contains a list of QualityProfile.
type QualityProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QualityProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&QualityProfile{}, &QualityProfileList{})
}
