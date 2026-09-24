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

// Package status is the single place indexarr declares what each field
// manager owns on Indexer.status.
//
// It exists because Indexer.status has three writer paths where the design
// spec's §2 table assumed one: the Indexer reconciler, the RSS poll, and the
// search fan-out. Server-side apply replaces a field manager's ownership set
// on every apply rather than merging into it, so any field a manager sent
// before and omits now is released -- which reads as "reset to zero" on the
// object. That hazard took eight distinct forms in Phase C, and the remedy
// that worked twice was distinct managers with disjoint ownership.
//
// The reconciler writes as k8s.ManagerIndexarr. Both worker paths share
// k8s.ManagerIndexarrWorker, so they cannot be separated by manager name --
// they are separated instead by having exactly one definition of what that
// manager owns, WorkerFields, which both call. Two callers hand-building
// their own apply configurations for one manager is precisely how each
// deletes the other's fields.
//
// # The escalation ladder lives here too (ruling R35)
//
// health.go holds Prowlarr's backoff ladder -- StartupGrace,
// EscalationTable, [Escalation], [RecordFailure], [RecordSuccess],
// [Healthy] -- and [ApplyEscalation] maps an Escalation onto a
// WorkerFields-seeded apply. [SupportsMode] sits beside [Healthy] for the
// same reason (ruling R39): both are read-only predicates over IndexerStatus
// that decide whether an indexer may be queried, and the search fan-out
// gates on both. It started in app/indexer/controller/indexer, which
// left it with no shared home: that package imports this one, so the mapping
// could not live beside the field declarations it writes, and every consumer
// (the RSS poll, the search fan-out, the download verb) had to import a
// CONTROLLER to compute a backoff. Co-locating the transition with the
// declaration of the fields the transition writes is the same argument that
// put WorkerFields in one place.
//
// # So does the producer of what the ladder reports
//
// event.go publishes design spec §5's clustarr.evt.index.indexer.
// <disabled|recovered|limited>.<uid> from those same transitions:
// [PublishTransitions] is called by each worker writer after its apply lands,
// with the status the apply was seeded from, so the event and the status
// cannot disagree about what changed.
package status

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// ControllerFields returns the complete set k8s.ManagerIndexarr owns, seeded
// from the live status so that an apply which changes one field still
// declares the other five.
//
// "Complete" reaches inside status.caps as well: server-side apply tracks
// ownership per leaf, so capsAC below declares all five of caps' fields
// rather than just the modes map. It deliberately does NOT seed Conditions.
// The Indexer reconciler is their only writer and derives all four on every
// pass, so there is nothing to carry forward -- and the generated
// WithConditions APPENDS rather than replaces, so a seeded set plus a
// caller's set is rejected outright with `duplicate entries for key
// [type="Ready"]`. Conditions are set in exactly one place per apply: the
// caller's mutate.
//
// Protocol is deliberately omitted when empty rather than sent as "": the
// generated CRD marks it enum [torrent, usenet], so an empty string is
// rejected outright. It is empty exactly while the Indexer has never
// resolved one: a spec.generic Indexer takes it from spec.generic.protocol
// once its Secret first reads, and a definition-backed one (G1-1) is
// "torrent" once its definition first loads -- and keeps it, because a
// definition that later vanishes leaves the resolved fields as they were
// (indexer.reconcileDefinition). That is the one legitimate variation in
// this set, and the rule it follows is worth stating: the owned set may vary
// with what the spec has ever RESOLVED, never with a transient OUTCOME.
// Omitting caps because this reconcile's probe failed would be outcome, and
// that is the release bug.
func ControllerFields(st indexv1alpha1.IndexerStatus) *indexac.IndexerStatusApplyConfiguration {
	ac := indexac.IndexerStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithPrivacy(st.Privacy).
		WithSessionSecretRef(st.SessionSecretRef)
	if st.Protocol != "" {
		ac = ac.WithProtocol(st.Protocol)
	}
	if st.Caps != nil {
		ac = ac.WithCaps(capsAC(st.Caps))
	}
	return ac
}

// WorkerFields returns the complete set k8s.ManagerIndexarrWorker owns,
// seeded from the live status.
//
// Every apply under that manager starts here, from BOTH the RSS poll and the
// search fan-out. If the two declared different sets, each apply would
// release whatever the other had written -- the RSS worker's lastRssAt would
// vanish on the next search, and the search's queriesInWindow would vanish on
// the next poll, with nothing logged either time.
func WorkerFields(st indexv1alpha1.IndexerStatus) *indexac.IndexerStatusApplyConfiguration {
	ac := indexac.IndexerStatus().
		WithEscalationLevel(st.EscalationLevel).
		WithLastFailure(st.LastFailure).
		WithQueriesInWindow(st.QueriesInWindow).
		WithGrabsInWindow(st.GrabsInWindow).
		WithLastRssNewCount(st.LastRssNewCount).
		WithIndexedReleases(st.IndexedReleases)
	if st.DisabledUntil != nil {
		ac = ac.WithDisabledUntil(*st.DisabledUntil)
	}
	if st.InitialFailureAt != nil {
		ac = ac.WithInitialFailureAt(*st.InitialFailureAt)
	}
	if st.LastFailureAt != nil {
		ac = ac.WithLastFailureAt(*st.LastFailureAt)
	}
	if st.LastRssAt != nil {
		ac = ac.WithLastRssAt(*st.LastRssAt)
	}
	return ac
}

// ApplyEscalation writes esc's fields onto a [WorkerFields]-seeded apply.
//
// It is the one mapping from the escalation ladder to the apply, and it lives
// beside WorkerFields because it is the only thing that can UNDO that seed.
// The RSS poll, the search fan-out and the download verb all reach it here
// rather than each keeping a copy, which is the same argument that put
// WorkerFields in one place: two hand-rolled versions of one manager's write
// is exactly how each releases the other's fields.
//
// The pointer fields are ASSIGNED rather than set through the generated With*
// helpers, because a With* helper cannot express "clear this": WorkerFields
// seeds DisabledUntil, InitialFailureAt and LastFailureAt from the LIVE
// status, so a recovered indexer whose disabledUntil is now nil must have
// that seed REMOVED, not carried forward. LastFailure is the same hazard in
// its non-pointer form -- WorkerFields sends it unconditionally, so a clear
// has to send "" explicitly, which WithLastFailure(esc.LastFailureMsg) does
// because RecordSuccess leaves the message empty.
//
// Get this wrong and a recovered indexer keeps a stale disable: nothing
// clears it, nothing logs it, and the indexer never polls again.
//
// InitialFailureAt comes from [Escalation.InitialFailure], which defines the
// 0 -> 1 transition beside the ladder that defines every other transition.
func ApplyEscalation(
	ac *indexac.IndexerStatusApplyConfiguration,
	esc Escalation,
	cur indexv1alpha1.IndexerStatus,
) {
	ac.WithEscalationLevel(esc.FailureLevel).WithLastFailure(esc.LastFailureMsg)
	ac.DisabledUntil = esc.DisabledUntil
	ac.LastFailureAt = esc.LastFailureAt
	ac.InitialFailureAt = esc.InitialFailure(cur)
}

// Patch applies a complete status declaration under mgr.
//
// The caller mutates the seeded apply configuration rather than building one,
// which is what makes the complete-declaration rule enforceable in one place.
// mgr must be k8s.ManagerIndexarr or k8s.ManagerIndexarrWorker; anything else
// is a programming error and is refused rather than allowed to claim fields
// no part of the split accounts for.
func Patch(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	idx *indexv1alpha1.Indexer,
	mutate func(*indexac.IndexerStatusApplyConfiguration),
) error {
	var ac *indexac.IndexerStatusApplyConfiguration
	switch mgr {
	case k8s.ManagerIndexarr:
		ac = ControllerFields(idx.Status)
	case k8s.ManagerIndexarrWorker:
		ac = WorkerFields(idx.Status)
	default:
		return fmt.Errorf("status: %q owns no part of Indexer.status", mgr)
	}
	if mutate != nil {
		mutate(ac)
	}

	obj := indexac.Indexer(idx.Name, idx.Namespace).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}

// capsAC renders status.caps as an apply configuration, and renders EVERY
// field of it, zero values included.
//
// Server-side apply tracks ownership per leaf inside a struct, not for the
// sub-object as a whole, so this is the same complete-declaration rule one
// level down: a renderer that sent only caps.modes would release
// caps.limitsMax, caps.limitsDefault, caps.supportsRawSearch and
// caps.categories on the next apply that did not re-probe -- and the Indexer
// reconciler re-applies status every 15 minutes while re-probing caps only
// every 12 hours, so four of the five fields would have been zeroed within
// one tick of the probe that set them. That is why limitsMax is sent even
// when it is 0: an indexer reporting 0 is a different thing from the field
// being released, and only one of the two is a bug.
//
// Modes and Categories are the exception, and it is a shape exception rather
// than an outcome one: the generated WithModes merges entries and
// WithCategories APPENDS, so calling either with an empty collection is a
// no-op that cannot express "empty" anyway. An unprobed or mode-less indexer
// omits them; there is nothing on the object to release.
func capsAC(c *indexv1alpha1.Caps) *indexac.CapsApplyConfiguration {
	ac := indexac.Caps().
		WithLimitsMax(c.LimitsMax).
		WithLimitsDefault(c.LimitsDefault).
		WithSupportsRawSearch(c.SupportsRawSearch)
	if len(c.Modes) > 0 {
		ac = ac.WithModes(c.Modes)
	}
	for _, cat := range c.Categories {
		catAC := indexac.Category().WithID(cat.ID).WithName(cat.Name)
		for _, sub := range cat.Sub {
			catAC = catAC.WithSub(indexac.SubCategory().WithID(sub.ID).WithName(sub.Name))
		}
		ac = ac.WithCategories(catAC)
	}
	return ac
}
