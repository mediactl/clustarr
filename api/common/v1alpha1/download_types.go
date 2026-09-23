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

// SeedCriteria describes when a completed torrent may stop seeding. Unset
// fields fall back to the download client's own defaults.
type SeedCriteria struct {
	// Ratio is the upload/download ratio to reach before the torrent may be removed,
	// written as a decimal string such as "2.0". Quantity is used instead of a float
	// so the value round-trips exactly across API clients.
	// +optional
	Ratio *resource.Quantity `json:"ratio,omitempty"`

	// SeedTime is how long a single-item torrent must seed before removal.
	// +optional
	SeedTime *metav1.Duration `json:"seedTime,omitempty"`

	// PackSeedTime is how long a pack (season, discography) torrent must seed before removal.
	// +optional
	PackSeedTime *metav1.Duration `json:"packSeedTime,omitempty"`

	// InactiveTime is how long a completed torrent may seed without uploading before its seed
	// goal counts as met, measured from the later of completion and the last upload
	// (qBittorrent's inactive seeding time limit). Like Ratio and SeedTime, reaching it alone
	// meets the goal. It is not a stall timeout: an incomplete torrent that receives no data
	// fails on the DownloadClient's torrent.stallTimeout instead.
	// +optional
	InactiveTime *metav1.Duration `json:"inactiveTime,omitempty"`
}

// RejectionType says whether a rejection is permanent or may clear later.
//
// +kubebuilder:validation:Enum=Permanent;Temporary
type RejectionType string

// Rejection types.
const (
	RejectionPermanent RejectionType = "Permanent"
	RejectionTemporary RejectionType = "Temporary"
)

// Rejection explains why a release was not grabbed or a file was not imported.
type Rejection struct {
	// Reason is a human-readable explanation of the rejection.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Type says whether the rejection is permanent or temporary.
	// +optional
	Type RejectionType `json:"type,omitempty"`
}

// Attempts tracks repeated attempts at an operation such as a search or grab.
type Attempts struct {
	// Initial is when the first attempt was made.
	// +optional
	Initial *metav1.Time `json:"initial,omitempty"`

	// Latest is when the most recent attempt was made.
	// +optional
	Latest *metav1.Time `json:"latest,omitempty"`

	// Count is the total number of attempts so far.
	// +optional
	Count int32 `json:"count,omitempty"`
}

// AddSource records how a catalog item came to exist.
type AddSource struct {
	// ImportListRef is the name of the ImportList that added the item. Empty
	// means the item was added manually.
	// +optional
	ImportListRef string `json:"importListRef,omitempty"`
}
