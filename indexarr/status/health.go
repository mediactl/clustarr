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

package status

import (
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// StartupGrace is Prowlarr's MinimumTimeSinceStartup: a failure inside this
// window of process start does not escalate, so a restart does not disable
// every indexer at once.
const StartupGrace = 15 * time.Minute

// maxFailureMsg caps status.lastFailure. The CRD carries no MaxLength and an
// indexer's error body can be arbitrarily long; an unbounded status string is
// how operators melt etcd.
const maxFailureMsg = 512

// escalationTable is Prowlarr's EscalationBackOff.Periods
// [0,60,300,900,1800,3600,10800,21600,43200,86400]s (design spec §6.2).
var escalationTable = [...]time.Duration{
	0, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// maxEscalationLevel is len(escalationTable)-1.
const maxEscalationLevel = int32(len(escalationTable)) - 1

// EscalationTable returns a copy of the ladder, in order.
//
// A copy, not the array: the interface contract names the table in prose
// without a signature, and a caller holding the backing array could reorder
// the ladder for every indexer in the process.
func EscalationTable() []time.Duration {
	out := make([]time.Duration, len(escalationTable))
	copy(out, escalationTable[:])
	return out
}

// processStart is when this process started. RecordFailure's signature is
// fixed by the Phase D1 interface contract and takes no start parameter, so
// the grace window is measured against this package variable. In-package
// tests assign it directly; nothing outside the package can.
var processStart = time.Now()

// truncate shortens s to at most n bytes, backing up to a rune boundary so a
// multi-byte sequence is never cut in half. An invalid UTF-8 byte in a status
// string is rejected by the apiserver, which would turn "the indexer sent a
// long error" into "this object's status can no longer be written".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Escalation is the complete escalation field set a caller must apply. Every
// field is always populated: the caller applies all of them, plus
// [Escalation.InitialFailure], under k8s.ManagerIndexarrWorker in ONE apply
// through [WorkerFields], because server-side apply releases the fields an
// apply omits. [ApplyEscalation] is that mapping, and it lives in this
// package for the same reason the ladder does -- the transition and the
// declaration of the fields it writes belong together.
//
// The struct is pinned by the Phase D1 interface contract; the RSS worker,
// the search fan-out and the download verb all build their applies from it.
type Escalation struct {
	// FailureLevel is status.escalationLevel, already clamped to the
	// ladder's bounds.
	FailureLevel int32

	// DisabledUntil is status.disabledUntil, nil when the indexer is not
	// disabled at all.
	DisabledUntil *metav1.Time

	// LastFailureAt is status.lastFailureAt, nil once the run is over.
	LastFailureAt *metav1.Time

	// LastFailureMsg is status.lastFailure, capped at maxFailureMsg.
	LastFailureMsg string

	// Changed is false when applying this would be a no-op, so a caller can
	// skip the apply entirely.
	Changed bool
}

// InitialFailure derives status.initialFailureAt, the remaining field of the
// escalation set. Escalation has no field for it because the interface
// contract fixes the struct, but IndexerStatus does, so the caller must send
// it or SSA releases it.
func (e Escalation) InitialFailure(cur indexv1alpha1.IndexerStatus) *metav1.Time {
	if e.LastFailureAt == nil {
		return nil // the run is over
	}
	if cur.InitialFailureAt != nil {
		return cur.InitialFailureAt
	}
	return e.LastFailureAt
}

// RecordFailure returns the escalation fields the caller must apply.
//
// It is pure: it reads cur and returns the next set, and never touches the
// apiserver, so the caller decides which field manager applies it. The
// Indexer reconciler never does -- the escalation set belongs to
// k8s.ManagerIndexarrWorker, and the reconciler applies as
// k8s.ManagerIndexarr.
func RecordFailure(cur indexv1alpha1.IndexerStatus, now time.Time, reason string) Escalation {
	level := cur.EscalationLevel
	if level > maxEscalationLevel {
		// A status written by an older build, or edited by hand, must not
		// index past the ladder.
		level = maxEscalationLevel
	}
	if now.Sub(processStart) >= StartupGrace {
		level = min(level+1, maxEscalationLevel)
	}
	if level < 0 {
		level = 0
	}
	at := metav1.NewTime(now)
	e := Escalation{
		FailureLevel:   level,
		LastFailureAt:  &at,
		LastFailureMsg: truncate(reason, maxFailureMsg),
		Changed:        true,
	}
	// A zero period is not a disable. Prowlarr stores DisabledTill = now,
	// which makes "is it disabled?" an equality edge case; nil is
	// unambiguous and is what Healthy reads.
	if d := escalationTable[level]; d > 0 {
		until := metav1.NewTime(now.Add(d))
		e.DisabledUntil = &until
	}
	return e
}

// RecordSuccess clears the escalation. It returns the zero Escalation when
// the indexer was already healthy, so a caller can skip a no-op apply.
//
// It is pure, like RecordFailure, and like RecordFailure the Indexer
// reconciler never applies the result -- the escalation set belongs to
// k8s.ManagerIndexarrWorker.
func RecordSuccess(cur indexv1alpha1.IndexerStatus, now time.Time) Escalation {
	_ = now // the ladder de-escalates by step, not by elapsed time
	if cur.EscalationLevel <= 0 && cur.DisabledUntil == nil &&
		cur.LastFailureAt == nil && cur.InitialFailureAt == nil && cur.LastFailure == "" {
		return Escalation{}
	}
	level := cur.EscalationLevel - 1
	if level < 0 {
		level = 0
	}
	if level > maxEscalationLevel {
		level = maxEscalationLevel
	}
	e := Escalation{FailureLevel: level, Changed: true}
	if level > 0 {
		// Still climbing down; keep the run's history so the applied set
		// stays a complete declaration.
		e.LastFailureAt = cur.LastFailureAt
		e.LastFailureMsg = cur.LastFailure
	}
	return e
}

// Healthy reports whether the indexer may be queried right now. It looks only
// at status.disabledUntil: spec.enabled is the operator's switch and the
// limit windows are the RateLimited condition's business, and a caller that
// conflates the three cannot tell an operator why an indexer went quiet.
func Healthy(st indexv1alpha1.IndexerStatus, now time.Time) bool {
	return st.DisabledUntil == nil || !now.Before(st.DisabledUntil.Time)
}
