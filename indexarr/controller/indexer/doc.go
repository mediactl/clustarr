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

// Package indexer reconciles index.clustarr.io Indexer objects: it validates
// the spec, probes Torznab caps, resolves the protocol, the privacy class and
// the session-Secret reference, and derives the Ready, Authenticated, Healthy
// and RateLimited conditions.
//
// The health and backoff ladder -- RecordFailure, RecordSuccess, Healthy,
// StartupGrace, EscalationTable -- is NOT here. It lives in indexarr/status,
// beside the declaration of the very fields it computes (ruling R35). It
// moved because indexarr's RSS poll, search fan-out and download verb all
// need it while this package imports indexarr/status, so a ladder here could
// never share a home with indexarr/status.ApplyEscalation, the one mapping
// from an Escalation onto an apply. This package only READS the result, to
// derive the Healthy condition and the requeue delay.
//
// # Field-manager split (design spec §2, Phase D1 rulings R6 and R31)
//
// Two writers reach IndexerStatus and each owns a disjoint set, because
// server-side apply REPLACES a manager's ownership set on every apply rather
// than merging it -- one shared name means each writer silently releases the
// other's fields.
//
//	k8s.ManagerIndexarr ("indexarr", this package):
//	    observedGeneration, conditions, protocol, privacy, caps,
//	    sessionSecretRef
//	k8s.ManagerIndexarrWorker ("indexarr-worker", indexarr's RSS poll and
//	search fan-out):
//	    escalationLevel, disabledUntil, initialFailureAt, lastFailureAt,
//	    lastFailure, queriesInWindow, grabsInWindow, lastRssAt,
//	    lastRssNewCount, indexedReleases
//
// This package never applies as ManagerIndexarrWorker. It reads the worker's
// fields to derive conditions and a requeue delay, nothing more.
//
// # One declaration of the owned set, and it is not here (ruling R31)
//
// The table above is documentation. The single machine-readable declaration
// of what k8s.ManagerIndexarr owns lives in indexarr/status.ControllerFields,
// and every apply this package makes goes through indexarr/status.Patch,
// which seeds the apply configuration from the live status and then runs this
// package's mutate. Two places declaring one manager's owned set is precisely
// how the two drift apart, silently, which is the release bug in its ninth
// form; Phase C found eight.
//
// The consequences for the code here are worth stating, because they are why
// Reconcile looks the way it does:
//
//   - Reconcile resolves the owned fields onto its LOCAL copy of idx.Status
//     before it applies. ControllerFields then seeds from that copy, so the
//     apply is a complete declaration by construction rather than by a
//     remembered discipline at seven return sites.
//   - There is exactly one call site, [Reconciler.patch], and every return in
//     Reconcile goes through it. An early return therefore re-sends the caps,
//     protocol and privacy already on the object instead of releasing them.
//     Phase C's most expensive SSA defect was an early-return path that built
//     a partial status and wiped the happy path's work -- and an early return
//     is always the transient path, so a healthy object got gutted by a blip.
//   - Conditions are set in exactly ONE place per apply. ControllerFields
//     deliberately does not seed them (this reconciler is their only writer
//     and derives all four every pass) and the generated WithConditions
//     APPENDS rather than replaces, so setting them in both the seed and the
//     mutate is rejected outright with `duplicate entries for key
//     [type="Ready"]`.
//
// The owned set may vary with the spec's SHAPE -- a definition-backed Indexer
// cannot resolve status.protocol, and the CRD's enum [torrent, usenet] makes
// sending "" an apiserver rejection, so it is omitted. It must never vary
// with a transient OUTCOME.
//
// # This reconciler never writes an escalation field
//
// escalationLevel, disabledUntil, initialFailureAt, lastFailureAt,
// lastFailure, queriesInWindow, grabsInWindow, lastRssAt, lastRssNewCount and
// indexedReleases belong to indexarr-worker. This package READS them (to
// derive the Healthy and RateLimited conditions and to choose a requeue
// delay); the workers compute the next set with indexarr/status's
// RecordFailure/RecordSuccess and apply it themselves. A
// caps-probe failure therefore moves conditions and the requeue delay and
// does not move escalationLevel: writing the escalation set here under
// indexarr-worker would release the counters this reconciler does not know.
//
// # What this controller does NOT do
//
// No Cardigann login test and no session-Secret creation (M6): the reference
// in status.sessionSecretRef is resolved and published, the Secret behind it
// is not created here. No RSS scheduling (that is the RSS worker's
// WithScheduleAt). No proxy routing. No bus publishing -- §8.2's
// indexer.disabled|recovered|limited events fire where the escalation
// transition is applied, which is the worker, not here.
//
// The RBAC markers below are package-level comments, separated from the
// package clause by a blank line. controller-gen collects +kubebuilder:rbac
// only from package-level comments and silently ignores one attached to a
// declaration; in Phase C that meant four controllers' rules never reached
// config/rbac/role.yaml, and no envtest could see it because envtest does not
// enforce RBAC. cmd/clustarr's TestRBACMarkersArePackageLevel is the guard.
//
// indexers/status is deliberately NOT granted here. indexarr/status/doc.go
// carries it, on the package that actually performs the write, which is the
// convention D1-0 set and the same reasoning as R31: one declaration, in the
// place that does the thing.
//
// The Events group is "" (core/v1), not events.k8s.io: the recorder comes
// from mgr.GetEventRecorderFor, which returns a
// k8s.io/client-go/tools/record.EventRecorder and writes core Events.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
package indexer
