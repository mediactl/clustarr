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

// Package status is the single place transcode declares what its field
// manager owns on TranscodeProfile.status.
//
// It exists for the same reason app/grab/status and app/indexer/status do:
// server-side apply replaces a field manager's ownership set on every apply
// rather than merging into it, so any field a manager sent before and omits
// now is RELEASED, and a released field nobody else owns is deleted from the
// object. That reads as "reset to zero" on the object. It took eight distinct
// forms in Phase C and three more in D1, and the remedy that worked every
// time was one complete declaration per manager.
//
// pkg/k8s.PatchStatus applies with ForceOwnership, so a genuine double-claim
// does NOT surface as a conflict: the apiserver hands the field over in
// silence. There is no loud failure waiting to catch a mistake here. The
// declaration below is the only thing enforcing it.
//
// TranscodeJob.status has no writer any more: a file's transcode is its
// MediaFile's status.transcode, which the remediation loop alone writes
// (ADR-0016, loop spec §5.14). The TranscodeJob kind stays installed until
// the folded-kinds removal, with nothing reading or writing it.
//
// # TranscodeProfile has one writer
//
// TranscodeProfile.status has exactly one writer: the slim TranscodeProfile
// reconciler (k8s.ManagerTranscode), computing hash, the file counts and
// encoderLimits on every reconcile. [PatchProfile] refuses every other
// manager, for the same reason app/grab/status.Patch refuses k8s.ManagerImport
// even though it legitimately writes Download.status.import: a write that
// belongs to a specific piece of code should be routed through that code's
// own declaration, not through one that happens to compile. [ProfileFields]
// is that one declaration, seeded from the live status, so an apply that
// changes one field still re-sends the others.
//
// It deliberately does NOT seed Conditions. The generated WithConditions
// APPENDS rather than replaces, so a seeded set plus the caller's set is
// rejected outright with `duplicate entries for key [type="Ready"]`.
// Conditions are set in exactly one place per apply: the caller's mutate.
//
// # Hazards this package does not remove
//
//   - A lost update is not a release, and no release-regression test can see
//     one: seed [PatchProfile] from a FRESH read.
//   - A manager can CO-OWN a field with another. Server-side apply only needs
//     force when their values differ, so while a second manager keeps
//     applying the same value, a release by the first leaves the field
//     standing and the bug is invisible. A release test that does not
//     deliberately drop the co-owner reports a false pass.
package status

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// ProfileFields returns the complete set k8s.ManagerTranscode owns on
// TranscodeProfile.status, seeded from the live status so that an apply which
// changes one field still declares the other four.
//
// Every field but Conditions is computable on every reconcile pass from the
// profile's spec alone -- hash is a pure function of it, and matchingFiles/
// pendingJobs/runningJobs are counted fresh each time -- so, like
// observedGeneration, all four are sent unconditionally rather than omitted
// at their zero value. There is no "this profile legitimately has no hash
// yet" state the way an unassigned Download legitimately has no engine.
//
// EncoderLimits is seeded here too, from st, and a caller changes it by
// setting the status it seeds from -- never through the mutate: the
// generated WithEncoderLimits appends, as WithConditions does, so a seeded
// list plus a mutated one would duplicate every entry.
func ProfileFields(st transcodev1alpha1.TranscodeProfileStatus) *transcodeac.TranscodeProfileStatusApplyConfiguration {
	ac := transcodeac.TranscodeProfileStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithHash(st.Hash).
		WithMatchingFiles(st.MatchingFiles).
		WithPendingJobs(st.PendingJobs).
		WithRunningJobs(st.RunningJobs).
		WithWaitingFiles(st.WaitingFiles)
	for _, l := range st.EncoderLimits {
		e := transcodeac.EncoderLimit().WithClass(l.Class).WithNode(l.Node)
		if len(l.NVDEC) > 0 {
			e.WithNVDEC(l.NVDEC...)
		}
		if l.Healthy != nil {
			e.WithHealthy(*l.Healthy)
		}
		if l.Message != "" {
			e.WithMessage(l.Message)
		}
		ac.WithEncoderLimits(e)
	}
	return ac
}

// PatchProfile applies a complete TranscodeProfile status declaration under
// mgr.
//
// mgr must be k8s.ManagerTranscode; anything else is refused. TranscodeProfile
// has exactly one status writer today, so there is no sibling manager to
// release fields out from under -- but routing every write through this one
// declaration, rather than letting other code hand-build a
// TranscodeProfileStatusApplyConfiguration, is what keeps it that way.
//
// profile must be FRESHLY READ: a status seeded from a snapshot taken
// before slow work rolls back whatever changed since (the lost update
// CLAUDE.md warns no release test can see).
func PatchProfile(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	profile *transcodev1alpha1.TranscodeProfile,
	mutate func(*transcodeac.TranscodeProfileStatusApplyConfiguration),
) error {
	if mgr != k8s.ManagerTranscode {
		return fmt.Errorf("status: %q owns no part of TranscodeProfile.status", mgr)
	}
	ac := ProfileFields(profile.Status)
	if mutate != nil {
		mutate(ac)
	}

	obj := transcodeac.TranscodeProfile(profile.Name).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}
