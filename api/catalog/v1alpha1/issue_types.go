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

// Condition types reported on an Issue.
const (
	// IssueConditionReady is True when the issue is fully reconciled.
	IssueConditionReady = "Ready"
	// The HasFile, Released and CutoffMet conditions were retired on
	// 2026-11-10 (design D4): status.phase, hasFile and cutoffReason say
	// what they restated, as Episode's do since 2026-11-03.
)

// The issue's phase values, commonv1.LeafPhase since 2026-11-10 (design
// D4), which replaced Mylar's state names (wanted, delayed, skipped,
// snatched, downloaded; archived, ignored and failed were never produced).
const (
	IssuePhasePending           = commonv1.LeafPhasePending
	IssuePhaseUnavailable       = commonv1.LeafPhaseUnavailable
	IssuePhaseWanted            = commonv1.LeafPhaseWanted
	IssuePhaseDelayed           = commonv1.LeafPhaseDelayed
	IssuePhaseDownloading       = commonv1.LeafPhaseDownloading
	IssuePhaseImported          = commonv1.LeafPhaseImported
	IssuePhaseCutoffUnmet       = commonv1.LeafPhaseCutoffUnmet
	IssuePhaseCutoffUnevaluated = commonv1.LeafPhaseCutoffUnevaluated
	IssuePhaseUnmonitored       = commonv1.LeafPhaseUnmonitored
)

// IssueSpec defines the desired state of Issue. Issues are created and owned
// by their Comic; only Monitored is meant to be edited by users.
type IssueSpec struct {
	// ComicRef is the name of the owning Comic in the same namespace.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="comicRef is immutable"
	ComicRef string `json:"comicRef"`

	// Number is the issue number exactly as the provider prints it, e.g. "12",
	// "12.HU" or "Annual 1".
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="number is immutable"
	Number string `json:"number"`

	// CalculatedNumberCentis is the sortable numeric form of Number in
	// hundredths, so issue 12 is 1200 and issue 12.5 is 1250. A scaled integer
	// is used instead of a float because CRD schemas do not allow floats.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="calculatedNumberCentis is immutable"
	CalculatedNumberCentis int32 `json:"calculatedNumberCentis"`

	// Monitored enables automatic searching for this issue. The Comic
	// controller sets it at creation and per the comic's monitorNewIssues;
	// after that it belongs to the user.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`
}

// IssueStatus describes the observed state of Issue.
type IssueStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the issue's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// SourceID is the provider's ID for this issue.
	// +optional
	SourceID string `json:"sourceID,omitempty"`

	// Title is the issue title.
	// +optional
	Title string `json:"title,omitempty"`

	// Date is the issue's cover date; StoreDate the day it reached shops,
	// when the provider has one.
	// +optional
	Date *metav1.Time `json:"date,omitempty"`
	// +optional
	StoreDate *metav1.Time `json:"storeDate,omitempty"`

	// Overview is the issue's description.
	// +optional
	Overview string `json:"overview,omitempty"`

	// CoverURL is the provider's cover image, served through the ui's
	// /art/search as episode stills are.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	CoverURL string `json:"coverURL,omitempty"`

	// Credits are the people credited on the issue.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Credits []Credit `json:"credits,omitempty"`

	// SourceIDs maps provider names (comicvine, metron, mangadex) to the
	// issue's ids there.
	// +optional
	SourceIDs map[string]string `json:"sourceIDs,omitempty"`

	// Phase is the coarse lifecycle state of the issue (commonv1.LeafPhase,
	// 2026-11-10 design D4).
	// +optional
	Phase commonv1.LeafPhase `json:"phase,omitempty"`

	// CutoffReason says why CutoffMet reads as it does: Pending (no file),
	// ProfileUnresolved, CutoffMet, CutoffUnmet (2026-11-10 design D4).
	// +optional
	// +kubebuilder:validation:MaxLength=64
	CutoffReason string `json:"cutoffReason,omitempty"`

	// HasFile is true while a MediaFile backs the issue.
	// +optional
	HasFile bool `json:"hasFile,omitempty"`

	// FileRef is the name of the MediaFile backing the issue.
	// +optional
	FileRef *string `json:"fileRef,omitempty"`

	// FileQuality is the quality of the imported file; FileFormat its format
	// as the comic ladder names it (CBZ, CBR, PDF) and FileFormatScore its
	// custom-format score (2026-11-10 design D5).
	// +optional
	FileQuality *commonv1.Quality `json:"fileQuality,omitempty"`
	// +optional
	FileFormat string `json:"fileFormat,omitempty"`
	// +optional
	FileFormatScore int32 `json:"fileFormatScore,omitempty"`

	// CutoffMet is true when the imported file meets the cutoff of the owning
	// Comic's quality profile, so the issue is no longer an upgrade candidate.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the id of the grab entry currently working on this issue.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// DownloadPhase is the phase of the entry on the Comic covering this
	// issue, "" when none: a print column and a selectable field (ADR-0019
	// §6.2).
	// +optional
	DownloadPhase commonv1.DownloadPhase `json:"downloadPhase,omitempty"`

	// SearchDispatch is the outstanding search task (ADR-0019 §7.3), written
	// under catalog-grab.
	// +optional
	SearchDispatch *Dispatch `json:"searchDispatch,omitempty"`

	// PendingGrab is a chosen release waiting out a DelayProfile delay.
	// +optional
	PendingGrab *PendingGrab `json:"pendingGrab,omitempty"`

	// LastSearchedAt is when the issue was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// LastLiveSearchAt is when a search that asked the indexers was last
	// dispatched for the issue -- its first search, a redownload, a backlog
	// search, or its season's search folded in -- where lastSearchedAt
	// moves for the searches the release index answers too. A
	// BacklogSearch's cooldown runs from it.
	// +optional
	LastLiveSearchAt *metav1.Time `json:"lastLiveSearchAt,omitempty"`

	// SearchAttempts counts the searches made for this issue.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=iss,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.comicRef`
// +kubebuilder:selectablefield:JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Comic",type=string,JSONPath=`.spec.comicRef`
// +kubebuilder:printcolumn:name="Number",type=string,JSONPath=`.spec.number`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.title`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Date",type=date,JSONPath=`.status.date`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Download",type=string,JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Issue is one issue of a Comic. Issues are created and owned by the Comic
// controller and named <comic>-<calculatedNumber padded to 5.1> for a plain
// number ("12.5" is saga-012.5). An integer followed by lower-case letters
// appends them ("12a" is saga-012.0-a), and any other number appends a
// DNS-safe readable part and a 10-character digest of the number ("12.HU" is
// saga-012.1-hu-<digest>), so distinct numbers never share a name; past the
// name limit the readable part is dropped (comic.IssueName).
type Issue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IssueSpec   `json:"spec,omitempty"`
	Status IssueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// IssueList contains a list of Issue.
type IssueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Issue `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Issue{}, &IssueList{})
}
