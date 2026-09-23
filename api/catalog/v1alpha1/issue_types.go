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
	// IssueConditionHasFile is True while a MediaFile backs the issue.
	IssueConditionHasFile = "HasFile"
	// IssueConditionReleased is True once the issue's cover date has passed.
	IssueConditionReleased = "Released"
	// IssueConditionCutoffMet is True when the imported file meets the
	// quality cutoff of the owning Comic's profile.
	IssueConditionCutoffMet = "CutoffMet"
)

// IssueState is the acquisition state of a single issue.
//
// +kubebuilder:validation:Enum=wanted;skipped;snatched;downloaded;archived;ignored;failed
type IssueState string

// Issue states.
const (
	IssueStateWanted     IssueState = "wanted"
	IssueStateSkipped    IssueState = "skipped"
	IssueStateSnatched   IssueState = "snatched"
	IssueStateDownloaded IssueState = "downloaded"
	IssueStateArchived   IssueState = "archived"
	IssueStateIgnored    IssueState = "ignored"
	IssueStateFailed     IssueState = "failed"
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

	// Date is the issue's cover or store date.
	// +optional
	Date *metav1.Time `json:"date,omitempty"`

	// State is the acquisition state of the issue.
	// +optional
	State IssueState `json:"state,omitempty"`

	// HasFile is true while a MediaFile backs the issue.
	// +optional
	HasFile bool `json:"hasFile,omitempty"`

	// FileRef is the name of the MediaFile backing the issue.
	// +optional
	FileRef *string `json:"fileRef,omitempty"`

	// FileQuality is the quality of the imported file.
	// +optional
	FileQuality *commonv1.Quality `json:"fileQuality,omitempty"`

	// CutoffMet is true when the imported file meets the cutoff of the owning
	// Comic's quality profile, so the issue is no longer an upgrade candidate.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the Download currently working on this issue.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// LastSearchedAt is when the issue was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// SearchAttempts counts the searches made for this issue.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=iss,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.comicRef`
// +kubebuilder:printcolumn:name="Comic",type=string,JSONPath=`.spec.comicRef`
// +kubebuilder:printcolumn:name="Number",type=string,JSONPath=`.spec.number`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.title`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Issue is one issue of a Comic. Issues are created and owned by the Comic
// controller and named <comic>-<calculatedNumber padded to 5.1>.
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
