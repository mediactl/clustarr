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

// SubtitlesPhase is the queue view of one file's subtitles (loop spec §2.4).
// +kubebuilder:validation:Enum=NotWanted;Wanted;Searching;Satisfied;Blocked
type SubtitlesPhase string

// Subtitles phases.
const (
	SubtitlesPhaseNotWanted SubtitlesPhase = "NotWanted" // the profile wants nothing for this file's audio, and no item remains
	SubtitlesPhaseWanted    SubtitlesPhase = "Wanted"    // a language is missing and waits for its next search
	SubtitlesPhaseSearching SubtitlesPhase = "Searching" // a fetch is outstanding for at least one language
	SubtitlesPhaseSatisfied SubtitlesPhase = "Satisfied" // every wanted language is present
	SubtitlesPhaseBlocked   SubtitlesPhase = "Blocked"   // no plan is possible; reason says why
)

// SubtitleState is one language's state, derived by the loop on every plan.
// Its first value is pending; searching means dispatch.seq >
// dispatch.answeredSeq.
// +kubebuilder:validation:Enum=pending;searching;downloaded;upgradable;unavailable;failed
type SubtitleState string

// Subtitle states.
const (
	SubtitleStatePending     SubtitleState = "pending"
	SubtitleStateSearching   SubtitleState = "searching"
	SubtitleStateDownloaded  SubtitleState = "downloaded"
	SubtitleStateUpgradable  SubtitleState = "upgradable"
	SubtitleStateUnavailable SubtitleState = "unavailable"
	SubtitleStateFailed      SubtitleState = "failed"
)

// SubtitlesStatus is the file's subtitles, written by the remediation loop's
// subtitles planner (loop spec §2.4; ADR-0016). It replaces SubtitleRequest.
type SubtitlesStatus struct {
	// +required
	Phase SubtitlesPhase `json:"phase"`
	// Reason is why the phase is Blocked: ItemNotFound, NoProfile,
	// ProfileInvalid, MediaFileNotOnDataVolume, MediaDirUnreadable or
	// MediaFileNotOnDisk.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// Profile is the SubtitleProfile the plan used.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Profile string `json:"profile,omitempty"`
	// ProbeHash is the status.probeHash the items describe; a new one resets
	// every language's attempts. Written only while items exist, so a
	// NotWanted block with no items does not change with the bytes.
	// (No profileGeneration: nothing reads it from status, and on a block
	// every probed video file carries it would make one SubtitleProfile
	// edit rewrite every MediaFile. The task and the record carry it, §6.6.)
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// Wanted is the langKeys still missing.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=64
	Wanted []string `json:"wanted,omitempty"`
	// CutoffMet is sent false as well as true (no omitempty, no default).
	CutoffMet bool `json:"cutoffMet"`
	// Items is one entry per wanted language or language on disk, sorted by langKey.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Items []SubtitleItemStatus `json:"items,omitempty"`
}

// SubtitleItemStatus is one language of a file's subtitles.
type SubtitleItemStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	LangKey string `json:"langKey"`
	// +required
	State SubtitleState `json:"state"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Score int32 `json:"score,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ScoreOutOf int32 `json:"scoreOutOf,omitempty"`
	// Provider is the SubtitleProvider's name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Provider string `json:"provider,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	SubtitleID string `json:"subtitleID,omitempty"`
	// Name is the sidecar written for this language: a file name in the media
	// file's directory.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`
	// +optional
	DownloadedAt *metav1.Time `json:"downloadedAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=512
	LastError string `json:"lastError,omitempty"`
	// +optional
	Attempts commonv1.Attempts `json:"attempts,omitempty"`
	// +optional
	NextSearchAt *metav1.Time `json:"nextSearchAt,omitempty"`
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
}
