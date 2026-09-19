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
// Protocol is deliberately omitted when empty rather than sent as "": the
// generated CRD marks it enum [torrent, usenet], so an empty string is
// rejected outright, and a definition-backed Indexer (M6) cannot resolve a
// protocol until its definition loads. That is the one legitimate variation
// in this set, and the rule it follows is worth stating: the owned set may
// vary with the spec's SHAPE, never with a transient OUTCOME. Omitting
// protocol because the spec cannot resolve one is shape. Omitting caps
// because this reconcile's probe failed would be outcome, and that is the
// release bug.
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

func capsAC(c *indexv1alpha1.Caps) *indexac.CapsApplyConfiguration {
	ac := indexac.Caps()
	if len(c.Modes) > 0 {
		ac = ac.WithModes(c.Modes)
	}
	return ac
}
