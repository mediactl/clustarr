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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Dispatch fences one remediation's task and the record that answers it
// (loop spec §2.3). A dispatch takes seq = records.NextSeq(record.Seq,
// status.lastSeq, now), and the apply that moves its block in flight writes
// status.lastSeq = seq beside it; records are also matched by their inputs.
type Dispatch struct {
	// Seq is the sequence of the last task issued for this remediation.
	// +kubebuilder:validation:Minimum=1
	Seq int64 `json:"seq"`
	// AnsweredSeq is the Seq of the last dispatch closed: by an incorporated
	// answer, a withdrawal or a timeout. A task is in flight while
	// Seq > AnsweredSeq.
	// +optional
	// +kubebuilder:validation:Minimum=0
	AnsweredSeq int64 `json:"answeredSeq,omitempty"`
	// DispatchedAt is when Seq was issued; the republish window and the
	// request timeout run from it.
	DispatchedAt metav1.Time `json:"dispatchedAt"`
	// Withdrawn is true when the dispatch at Seq was closed by a withdrawal,
	// not by an answer. For transcode and graft a fact (a change already
	// made on disk) can still land after that: the planner keeps reading
	// the record while Withdrawn is true and now < DispatchedAt +
	// records.FactWindow, and incorporates a fact at Seq whatever the phase
	// (§4.10, §5.9). Incorporating it clears Withdrawn.
	// +optional
	Withdrawn bool `json:"withdrawn,omitempty"`
}

// InFlight reports whether d's task is outstanding (Seq > AnsweredSeq):
// subtitles' searching, transcode's Queued and Running, graft's Queued and
// Running all read it. A nil Dispatch is never in flight.
func (d *Dispatch) InFlight() bool {
	return d != nil && d.Seq > d.AnsweredSeq
}
