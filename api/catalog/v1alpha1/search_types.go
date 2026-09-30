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

// Condition types reported on a Search.
const (
	// SearchConditionCompleted is True once every selected indexer has answered
	// or timed out.
	SearchConditionCompleted = "Completed"
	// SearchConditionFailed is True when the search could not be run at all.
	SearchConditionFailed = "Failed"
	// SearchConditionReady is True when status.results reflects spec.
	SearchConditionReady = "Ready"
)

// SearchPhase is the coarse lifecycle state of a search.
//
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type SearchPhase string

// Search phases.
const (
	SearchPhasePending   SearchPhase = "Pending"
	SearchPhaseRunning   SearchPhase = "Running"
	SearchPhaseCompleted SearchPhase = "Completed"
	SearchPhaseFailed    SearchPhase = "Failed"
)

// IndexerOutcomeState is how one indexer answered a search.
//
// +kubebuilder:validation:Enum=ok;timeout;error;skipped
type IndexerOutcomeState string

// Indexer outcome states.
const (
	IndexerOutcomeOK      IndexerOutcomeState = "ok"
	IndexerOutcomeTimeout IndexerOutcomeState = "timeout"
	IndexerOutcomeError   IndexerOutcomeState = "error"
	IndexerOutcomeSkipped IndexerOutcomeState = "skipped"
)

// IndexerOutcome records how one indexer fared during the search.
type IndexerOutcome struct {
	// Name is the Indexer the outcome belongs to.
	// +required
	Name string `json:"name"`

	// State is how the indexer answered.
	// +optional
	State IndexerOutcomeState `json:"state,omitempty"`

	// Count is how many releases the indexer returned.
	// +optional
	Count int32 `json:"count,omitempty"`

	// DurationMs is how long the indexer took, in milliseconds.
	// +optional
	DurationMs int32 `json:"durationMs,omitempty"`

	// Error is the error the indexer returned, if any.
	// +optional
	Error string `json:"error,omitempty"`
}

// GrabResult reports what happened to one requested grab.
type GrabResult struct {
	// GUID is the release GUID the user asked to grab.
	// +required
	GUID string `json:"guid"`

	// DownloadRef is the Download that was created.
	// +optional
	DownloadRef string `json:"downloadRef,omitempty"`

	// Error is why the grab failed, if it did.
	// +optional
	Error string `json:"error,omitempty"`
}

// SearchSpec defines the desired state of Search.
//
// +kubebuilder:validation:XValidation:rule="(has(self.query) ? 1 : 0) + (has(self.mediaRef) ? 1 : 0) == 1",message="exactly one of query or mediaRef must be set"
type SearchSpec struct {
	// Query is a free-text search term.
	// +optional
	Query *string `json:"query,omitempty"`

	// MediaRef searches for a specific catalog item, using its provider IDs.
	// +optional
	MediaRef *commonv1.MediaRef `json:"mediaRef,omitempty"`

	// Kinds restricts the search to these media kinds.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Kinds []commonv1.MediaKind `json:"kinds,omitempty"`

	// IndexerRefs restricts the search to these Indexers; empty means all
	// indexers that support interactive search.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	IndexerRefs []string `json:"indexerRefs,omitempty"`

	// Categories restricts the search to these Newznab/Torznab category IDs.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Categories []int32 `json:"categories,omitempty"`

	// Limit caps how many results are kept.
	// +optional
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=200
	Limit int32 `json:"limit,omitempty"`

	// Grab lists the release GUIDs the user wants grabbed; the controller
	// creates a Download for each.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Grab []string `json:"grab,omitempty"`

	// Override allows grabbing results that were permanently rejected.
	// +optional
	Override bool `json:"override,omitempty"`

	// GrabBest grabs the top-ranked approved result once the results are
	// in, as the automatic search would: Readarr's and Lidarr's "search
	// monitored". A release with any rejection, temporary included, is
	// never grabbed this way, so an item already queued, downloading or at
	// cutoff is not grabbed again. The Download is grabbedBy search and not
	// manual, so the importer applies its upgrade rules. A Search on an
	// author, artist or comic passes it to each child Search.
	// +optional
	GrabBest bool `json:"grabBest,omitempty"`

	// TTL is how long the Search object lives after it completes. A Go
	// client (the UI's "search now" among them) always sends a Duration, so
	// the Search controller floors a zero (or negative) one to this default
	// rather than delete a Search the moment it answers.
	// +optional
	// +kubebuilder:default="1h"
	TTL metav1.Duration `json:"ttl,omitempty"`
}

// SearchChildren counts a container Search's child Searches: one per
// monitored book, album or issue of the author, artist or comic it names.
type SearchChildren struct {
	// Total is how many child Searches the container Search created.
	Total int32 `json:"total"`
	// Running is how many have not finished.
	// +optional
	Running int32 `json:"running,omitempty"`
	// Completed is how many finished with results.
	// +optional
	Completed int32 `json:"completed,omitempty"`
	// Failed is how many failed.
	// +optional
	Failed int32 `json:"failed,omitempty"`
	// Grabbed is how many created a Download.
	// +optional
	Grabbed int32 `json:"grabbed,omitempty"`
}

// LabelParentSearch names the container Search a child Search was created
// by. A child is owned by that Search, deleted with it, and never expires
// on its own TTL.
const LabelParentSearch = "catalog.clustarr.io/parent-search"

// MaxContainerChildren caps a container Search's fan-out: more monitored
// children than this fails the Search and creates nothing.
const MaxContainerChildren = 200

// ContainerKind reports whether a Search on kind fans out into its
// monitored children -- an author's books, an artist's albums, a comic's
// issues -- rather than searching for the item itself. A series is not one:
// television wants season packs, which this fan-out does not do.
func ContainerKind(kind commonv1.MediaKind) bool {
	switch kind {
	case commonv1.MediaKindAuthor, commonv1.MediaKindArtist, commonv1.MediaKindComic:
		return true
	default:
		return false
	}
}

// SearchStatus describes the observed state of Search.
type SearchStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the search's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Phase is the coarse lifecycle state of the search.
	// +optional
	Phase SearchPhase `json:"phase,omitempty"`

	// StartedAt is when the search began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the search finished.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// IndexerOutcomes records how each selected indexer fared.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	IndexerOutcomes []IndexerOutcome `json:"indexerOutcomes,omitempty"`

	// Results is the ranked result set with the decision engine's verdicts.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Results []commonv1.ReleaseDecision `json:"results,omitempty"`

	// Grabbed reports what happened to each requested grab.
	// +optional
	// +listType=map
	// +listMapKey=guid
	// +kubebuilder:validation:MaxItems=50
	Grabbed []GrabResult `json:"grabbed,omitempty"`

	// Children counts a container Search's child Searches (author, artist
	// or comic); absent on any other Search.
	// +optional
	Children *SearchChildren `json:"children,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=srch,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Results",type=integer,JSONPath=`.status.indexerOutcomes[0].count`
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startedAt`
// +kubebuilder:printcolumn:name="Finished",type=date,JSONPath=`.status.finishedAt`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Search is a short-lived interactive search. It is deleted once spec.ttl has
// elapsed after the search completed.
type Search struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SearchSpec   `json:"spec,omitempty"`
	Status SearchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// SearchList contains a list of Search.
type SearchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Search `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Search{}, &SearchList{})
}
