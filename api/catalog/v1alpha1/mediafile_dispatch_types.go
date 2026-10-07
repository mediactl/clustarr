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

	// Destination is the durable, pool or engine instance the task went to
	// (ADR-0019 §8.3).
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Destination string `json:"destination,omitempty"`
	// Delivery is what the manager knows about the task between publish
	// and answer. Written on transitions only (ADR-0019 §8.3), never per
	// nak.
	// +optional
	Delivery *DeliveryState `json:"delivery,omitempty"`
}

// DeliveryPhase is where a dispatched task is between its publish and its
// answer (ADR-0019 §8.3).
// +kubebuilder:validation:Enum=waiting;published;claimed;retrying;deadLettered
type DeliveryPhase string

// Delivery phases.
const (
	// DeliveryWaiting means the task is due but admission refused it.
	DeliveryWaiting DeliveryPhase = "waiting"
	// DeliveryPublished means the task was published and acknowledged by
	// the stream.
	DeliveryPublished DeliveryPhase = "published"
	// DeliveryClaimed means the task's record says an agent claimed it.
	DeliveryClaimed DeliveryPhase = "claimed"
	// DeliveryRetrying means an agent nak'd the task.
	DeliveryRetrying DeliveryPhase = "retrying"
	// DeliveryDeadLettered means the task was terminated or exhausted its
	// deliveries and copied to the dead-letter subject.
	DeliveryDeadLettered DeliveryPhase = "deadLettered"
)

// Reasons a task is waiting or retrying (DeliveryState.Reason).
const (
	// DeliveryReasonBudget means the durable's admission budget is spent.
	DeliveryReasonBudget = "Budget"
	// DeliveryReasonPaced means a pacing class refused it.
	DeliveryReasonPaced = "Paced"
	// DeliveryReasonNoAgent means no agent of the durable's domain is
	// present.
	DeliveryReasonNoAgent = "NoAgent"
	// DeliveryReasonNoCapableAgent means no present agent can take it.
	DeliveryReasonNoCapableAgent = "NoCapableAgent"
	// DeliveryReasonEngineNotReady means the entry's engine is not ready.
	DeliveryReasonEngineNotReady = "EngineNotReady"
	// DeliveryReasonRebuilding means the dispatch ledger is still being
	// rebuilt from status after a leader change.
	DeliveryReasonRebuilding = "Rebuilding"
	// DeliveryReasonNak means an agent nak'd the delivery.
	DeliveryReasonNak = "Nak"
)

// DeliveryState is the manager's view of a task between its publish and
// its answer (ADR-0019 §8.3).
type DeliveryState struct {
	State DeliveryPhase `json:"state"`
	// Reason when waiting or retrying: Budget, Paced, NoAgent,
	// NoCapableAgent, EngineNotReady, Rebuilding, Nak.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Message string `json:"message,omitempty"`
	// Attempts is the deliveries count of the last nak the manager recorded.
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	LastNakAt *metav1.Time `json:"lastNakAt,omitempty"`
	// +optional
	DeadLetteredAt *metav1.Time `json:"deadLetteredAt,omitempty"`
}

// InFlight reports whether d's task is outstanding (Seq > AnsweredSeq):
// subtitles' searching, transcode's Queued and Running, graft's Queued and
// Running all read it. A nil Dispatch is never in flight.
func (d *Dispatch) InFlight() bool {
	return d != nil && d.Seq > d.AnsweredSeq
}
