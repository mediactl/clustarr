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

// SeasonSearchState is the person-requested series or season search a
// Series serves or last served (Wave S): one season task in flight at a
// time, paced. Written by the remediation loop's search stage under
// catalog-grab, one complete set with pendingGrabs.
type SeasonSearchState struct {
	// Request is the Search this state serves.
	Request SeasonSearchRequest `json:"request"`
	// StartedAt is when the Series took the request.
	StartedAt metav1.Time `json:"startedAt"`
	// FinishedAt is when every season reached a final phase.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Seasons are the seasons asked for, ascending, each with its outcome.
	// +listType=map
	// +listMapKey=number
	// +kubebuilder:validation:MaxItems=200
	Seasons []SeasonSearchSeason `json:"seasons"`
	// Dispatch is the season task in flight (the season whose phase is
	// Searching); Seq > AnsweredSeq while one is out.
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
}

// SeasonSearchRequest names the Search a SeasonSearchState serves.
type SeasonSearchRequest struct {
	// Name is the Search's name.
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// UID is the Search's uid: a Search recreated under the same name is
	// another request.
	// +kubebuilder:validation:MaxLength=36
	UID string `json:"uid"`
}

// SeasonSearchPhase is where one season of a series search is.
// +kubebuilder:validation:Enum=Queued;Searching;Answered;Skipped;Failed;Canceled
type SeasonSearchPhase string

// Season search phases.
const (
	SeasonSearchQueued    SeasonSearchPhase = "Queued"
	SeasonSearchSearching SeasonSearchPhase = "Searching"
	SeasonSearchAnswered  SeasonSearchPhase = "Answered"
	SeasonSearchSkipped   SeasonSearchPhase = "Skipped"
	SeasonSearchFailed    SeasonSearchPhase = "Failed"
	SeasonSearchCanceled  SeasonSearchPhase = "Canceled"
)

// Final reports a phase no season leaves: Answered, Skipped, Failed or
// Canceled.
func (p SeasonSearchPhase) Final() bool {
	switch p {
	case SeasonSearchAnswered, SeasonSearchSkipped, SeasonSearchFailed, SeasonSearchCanceled:
		return true
	}
	return false
}

// SeasonSearchReason says why a season was skipped, failed or canceled.
// +kubebuilder:validation:Enum=NotMonitored;NothingWanted;AllDownloading;NoSuchSeason;AllPaced;TimedOut;AgentFailed;SearchDeleted
type SeasonSearchReason string

// Season search reasons.
const (
	// SeasonSearchNotMonitored: the series, or every episode of the season,
	// is unmonitored (Sonarr's monitoredOnly).
	SeasonSearchNotMonitored SeasonSearchReason = "NotMonitored"
	// SeasonSearchNothingWanted: no monitored, aired episode of the season
	// is missing or below its cutoff.
	SeasonSearchNothingWanted SeasonSearchReason = "NothingWanted"
	// SeasonSearchAllDownloading: every wanted episode is covered by a live
	// grab.
	SeasonSearchAllDownloading SeasonSearchReason = "AllDownloading"
	// SeasonSearchNoSuchSeason: the series has no such season.
	SeasonSearchNoSuchSeason SeasonSearchReason = "NoSuchSeason"
	// SeasonSearchAllPaced: every indexer was paced twice; the answer
	// counts for no episode.
	SeasonSearchAllPaced SeasonSearchReason = "AllPaced"
	// SeasonSearchTimedOut: no answer came within the dispatch timeout.
	SeasonSearchTimedOut SeasonSearchReason = "TimedOut"
	// SeasonSearchAgentFailed: the search agent could not run the search.
	SeasonSearchAgentFailed SeasonSearchReason = "AgentFailed"
	// SeasonSearchSearchDeleted: the Search was deleted before the season
	// was searched.
	SeasonSearchSearchDeleted SeasonSearchReason = "SearchDeleted"
)

// SeasonSearchSeason is one season of a series search.
type SeasonSearchSeason struct {
	// Number is the season number; 0 is the specials.
	// +kubebuilder:validation:Minimum=0
	Number int32 `json:"number"`
	// Phase is where the season is.
	Phase SeasonSearchPhase `json:"phase"`
	// Reason says why the season was skipped, failed or canceled.
	// +optional
	Reason SeasonSearchReason `json:"reason,omitempty"`
	// Seq is the dispatch seq this season's task carried; its record
	// answers at it, and the grab stage reads the record only at it.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Seq int64 `json:"seq,omitempty"`
	// Attempts counts the season's dispatches: a season every indexer
	// paced goes once more to the back of the queue.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2
	Attempts int32 `json:"attempts,omitempty"`
	// AnsweredAt is when the season's answer was incorporated.
	// +optional
	AnsweredAt *metav1.Time `json:"answeredAt,omitempty"`
	// Candidates is how many releases the answer held.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Candidates int32 `json:"candidates,omitempty"`
	// Approved is how many of them were approved.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Approved int32 `json:"approved,omitempty"`
}
