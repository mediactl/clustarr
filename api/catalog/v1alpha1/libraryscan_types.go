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

// ScanMode selects how much of the tree a LibraryScan examines.
//
// +kubebuilder:validation:Enum=full;incremental
type ScanMode string

// Scan modes.
const (
	// ScanModeFull walks and re-probes every file under the root folder.
	ScanModeFull ScanMode = "full"
	// ScanModeIncremental skips files whose size and mtime match a known
	// MediaFile fingerprint.
	ScanModeIncremental ScanMode = "incremental"
)

// ScanPhase is the coarse lifecycle state of a LibraryScan.
//
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type ScanPhase string

// Scan phases.
const (
	ScanPhasePending   ScanPhase = "Pending"
	ScanPhaseRunning   ScanPhase = "Running"
	ScanPhaseCompleted ScanPhase = "Completed"
	ScanPhaseFailed    ScanPhase = "Failed"
)

// UnmatchedFile is a file the scanner could not attribute to a catalog item.
// The scanner never guesses: an unattributable file is recorded here with the
// reason instead of becoming a speculative item.
type UnmatchedFile struct {
	// Path is the file path relative to the root folder.
	// +required
	Path string `json:"path"`

	// Reason explains why the file could not be matched.
	// +required
	Reason string `json:"reason"`

	// Candidates lists catalog items the scanner considered but could not
	// confidently match to.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Candidates []string `json:"candidates,omitempty"`

	// SeenAt is when the scanner last observed this file.
	// +required
	SeenAt metav1.Time `json:"seenAt"`
}

// LibraryScanSpec asks importarr to walk a root folder and reconcile what it finds.
type LibraryScanSpec struct {
	// RootFolderRef names the RootFolder to scan.
	//
	// A plain string, like every other rootFolderRef in this group
	// (Movie, Series, Artist, Author, Comic, Audiobook, Book, ImportList):
	// corev1.LocalObjectReference is reserved here for references to CORE
	// objects -- Secrets and ConfigMaps -- so a reader can tell at a glance
	// which side of the boundary a reference points at.
	// +required
	RootFolderRef string `json:"rootFolderRef"`

	// Mode selects how much of the tree is examined. Incremental skips files
	// whose size and mtime match a known MediaFile fingerprint.
	// +optional
	// +kubebuilder:default=incremental
	Mode ScanMode `json:"mode,omitempty"`

	// Subpath restricts the scan to one directory beneath the root folder.
	// +optional
	Subpath string `json:"subpath,omitempty"`

	// DryRun reports what would change without creating or updating anything.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// TTLSecondsAfterFinished deletes the scan once it has settled.
	// +optional
	// +kubebuilder:default=3600
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// LibraryScanStatus reports progress. All lists are capped per the §4 legend.
type LibraryScanStatus struct {
	// Phase is the coarse lifecycle state of the scan.
	// +optional
	Phase ScanPhase `json:"phase,omitempty"`

	// StartedAt is when the scan began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the scan finished.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// FilesSeen is how many files the scanner walked.
	// +optional
	FilesSeen int64 `json:"filesSeen,omitempty"`

	// FilesMatched is how many files were attributed to a catalog item.
	// +optional
	FilesMatched int64 `json:"filesMatched,omitempty"`

	// ItemsCreated is how many catalog items the scan created.
	// +optional
	ItemsCreated int64 `json:"itemsCreated,omitempty"`

	// ItemsUpdated is how many catalog items the scan updated.
	// +optional
	ItemsUpdated int64 `json:"itemsUpdated,omitempty"`

	// FilesSkipped is how many files the scan skipped, such as an incremental
	// scan skipping an unchanged fingerprint.
	// +optional
	FilesSkipped int64 `json:"filesSkipped,omitempty"`

	// Unmatched lists files the scanner could not attribute, newest first.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Unmatched []UnmatchedFile `json:"unmatched,omitempty"`

	// Conditions represent the latest available observations of the scan's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:shortName=lscan,categories=clustarr
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Files Seen",type=integer,JSONPath=`.status.filesSeen`
// +kubebuilder:printcolumn:name="Items Created",type=integer,JSONPath=`.status.itemsCreated`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LibraryScan is a short-lived resource that asks importarr to walk a
// RootFolder and reconcile what it finds. Like Search, it is a request that
// is observable with kubectl and whose progress survives a restart; it is
// deleted once spec.ttlSecondsAfterFinished has elapsed after it settled.
type LibraryScan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LibraryScanSpec   `json:"spec,omitempty"`
	Status LibraryScanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// LibraryScanList contains a list of LibraryScan.
type LibraryScanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LibraryScan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LibraryScan{}, &LibraryScanList{})
}
