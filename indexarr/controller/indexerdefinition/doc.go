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

// Package indexerdefinition reconciles IndexerDefinition: it validates
// spec.yaml against the bundled Cardigann v11 schema, decodes it, and reports
// what it found.
//
// # What this controller does NOT do
//
// It does not instantiate a cardigann.Engine, does not log in to a tracker,
// does not run a search, and does not register the definition into any live
// indexer set. Wiring a definition into an Indexer is M6 (Phase G), and Phase
// G inherits a recorded list of eleven unimplemented Cardigann features.
// Everything here is parse-and-report against pkg/cardigann's two pure entry
// points, Validate and Load.
//
// # Wiring (Task D1-8)
//
// The exact call indexarr/run.go must make:
//
//	if err := indexerdefinition.NewReconciler(
//	    mgr.GetClient(),
//	    mgr.GetEventRecorder("indexerdefinition"), // events.EventRecorder, events.k8s.io/v1
//	).SetupWithManager(mgr); err != nil {
//	    return err
//	}
//
// IndexerDefinition is CLUSTER-scoped, so reconcile.Request carries an empty
// Namespace and the apply configuration is built with one argument.
//
// # The owned field set
//
// IndexerDefinition.status has exactly one writer, k8s.ManagerIndexarr, so the
// two-manager split in indexarr/status (which covers Indexer, not this kind)
// does not apply. What does apply is the rule that made that package
// necessary: server-side apply REPLACES a field manager's ownership set on
// every apply rather than merging into it, so a field this manager sent before
// and omits now is released -- which reads as "reset to zero" on the object.
// Phase C hit that eight times, three of them on an early-return path that
// built a conditions-only apply and gutted a healthy object over a transient
// blip.
//
// Every apply this package makes therefore declares all of:
//
//	status.observedGeneration
//	status.conditions          (Valid; the CRD defines no other condition type)
//	status.id
//	status.name
//	status.language
//	status.type                (see the shape exception below)
//	status.protocol            (see the shape exception below)
//	status.sha256
//	status.caps.modes
//	status.caps.categories
//
// and does so from ONE place, statusFor, which seeds from the live status so
// that a path which cannot recompute a field still re-sends the value already
// on the object. There is no second apply and no partial apply, on any path,
// including the invalid-spec early return.
//
// The two shape exceptions, and why they are not the release bug in disguise:
// status.type carries enum [public, semiPrivate, private] and status.protocol
// carries enum [torrent, usenet] in the generated CRD, so sending "" is an
// apiserver rejection rather than a no-op. Both are therefore omitted while
// empty -- which only happens before the first successful parse. That is the
// same rule indexarr/status states: the owned set may vary with the spec's
// SHAPE, never with a transient OUTCOME.
//
// # Values that deliberately go stale rather than reset
//
// status.sha256 is documented as "the hex digest of spec.yaml as LAST
// VALIDATED". When an edit makes spec.yaml invalid, the digest, the id, the
// name and the caps all keep their last-validated values and the Valid
// condition (False, with the schema error) is what says the object is no
// longer describing spec.yaml. Overwriting them with the invalid document's
// values, or clearing them, would both destroy the only record of what the
// definition last resolved to.
//
// The RBAC markers below are package-level comments, separated from the
// package clause by a blank line. controller-gen collects +kubebuilder:rbac
// only from package-level comments and silently ignores one attached to a
// declaration; in Phase C that meant four controllers' rules never reached
// config/rbac/role.yaml, and no envtest could see it because envtest does not
// enforce RBAC. cmd/clustarr's TestRBACMarkersArePackageLevel is the guard.
//
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerdefinitions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package indexerdefinition
