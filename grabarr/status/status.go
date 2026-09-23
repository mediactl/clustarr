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

// Package status is the single place grabarr declares what each field manager
// owns on Download.status.
//
// Download.status has three writers, which is one more than any other object
// in the project: the grabarr controller, an engine pod, and -- across a
// service boundary -- importarr's file-import worker. Server-side apply
// replaces a field manager's ownership set on every apply rather than merging
// into it, so any field a manager sent before and omits now is RELEASED, and a
// released field nobody else owns is deleted from the object. That reads as
// "reset to zero". It took eight distinct forms in Phase C and three more in
// D1, and the remedy that worked every time was distinct managers with
// disjoint, completely-declared sets.
//
// pkg/k8s.PatchStatus applies with ForceOwnership, so a genuine double-claim
// does NOT surface as a conflict: the apiserver hands the field over in
// silence. There is no loud failure waiting to catch a mistake here. The split
// below is the only thing enforcing it.
//
// # The split, and why each field falls where it does
//
// The rule is not "lifecycle versus telemetry" -- that phrasing does not
// decide DownloadID or OutputPath. The rule is WHO CAN KNOW THE VALUE.
//
// [ControllerFields], k8s.ManagerGrabarr -- nine fields. Every one of them is
// a statement about the Download OBJECT that only something watching the
// object can make:
//
//   - observedGeneration: which spec generation was reconciled. An engine does
//     not reconcile.
//   - phase: the lifecycle, and a pinned contract. catalogarr's rollup overlay
//     switches on it (plan ruling R1), so an engine that could write it could
//     move a Movie between phases from inside a transfer loop.
//   - engine: the assignment itself. The controller chooses the ordinal from
//     spec.replicas; an engine writing it would be claiming its own work.
//   - failureReason: the justification for phase=Failed (or Blocklisted),
//     and it must be written in the same apply as the phase it explains. The
//     engine REPORTS the reason as status.engineFailureReason, one of its own
//     fields, and the controller copies it here.
//   - blocklistedUntil: a policy decision with a default TTL, swept by the
//     controller.
//   - startedAt, completedAt, seedGoalMetAt: transition timestamps. They are
//     derived from phase edges, so the writer of phase must be the writer of
//     these or the two disagree at every boundary.
//   - conditions: Assigned, Downloaded, SeedGoalMet, Imported, Failed. Two of
//     the five are about the object's relationship to other objects, not to
//     the transfer.
//
// [EngineFields], k8s.ManagerGrabarrEngine -- twenty-five fields: stage,
// downloadID, outputPath, contentRoot, files, the byte counters, the rates,
// etaSeconds, progressPercent, seeders, peers, ratioMilli, seedTimeSeconds,
// health, isEncrypted, canMoveFiles, canBeRemoved, message, lastProgressAt,
// engineFailureReason and seedGoalReached. Each is an observation of the
// TRANSFER, and the controller has no way to produce any of them: it cannot
// know the info hash the payload resolved to, the directory the engine
// published into, how many articles were missing, or whether a disk write
// failed. downloadID, outputPath and contentRoot are here for that reason
// rather than because they read like telemetry; engineFailureReason and
// seedGoalReached (gap fix Y2) are the engine's reports behind the
// controller's failureReason and seedGoalMetAt, split so that neither field
// has two writers.
//
// status.import belongs to NEITHER, and is named here so that it cannot be
// claimed by accident. It is the project's only cross-group status write:
// importarr's file-import worker (D2-7) applies it under
// k8s.ManagerImportarr, and grabarr only reads it, to decide when a Download
// may be removed or blocklisted. (Two comments in the tree still say
// catalogarr writes it -- DownloadStatus' own doc comment and
// k8s.ManagerCatalogarr's. k8s.ManagerImportarr's comment and plan task D2-7
// both say importarr. Neither field manager is used by any writer yet, so no
// code has ever settled it; this package follows ManagerImportarr and the
// plan.)
//
// # One declaration per manager, not one per caller
//
// [ControllerFields] and [EngineFields] are complete declarations seeded from
// the live status, so an apply that changes one field still re-sends the other
// eight or twenty-four. Callers mutate the seeded configuration rather than
// building one, which is what makes the complete-declaration rule enforceable
// in a single place: two callers hand-building their own apply configurations
// for one manager is precisely how each deletes the other's fields.
//
// EngineFields does not restate the engine's field list. It is
// download.ApplyStatus(download.ItemFromStatus(st)) -- the same declaration
// the engines write through, reached from the object instead of from a fresh
// observation -- so the seed and the write cannot drift apart. The round trip
// is exact on the status side, which is all a seed needs: its whole job is to
// re-declare what the object already holds.
//
// # Hazards this package does not remove
//
//   - A lost update is not a release, and no release-regression test can see
//     one. Any path that Gets an object, does slow work and then applies must
//     re-Get immediately before the apply. An engine writing telemetry after a
//     long transfer is exactly that shape. [Patch] takes the object it seeds
//     from, so the caller chooses how fresh it is -- and the caller is wrong
//     by default.
//   - Two managers can CO-OWN a field. Server-side apply only needs force when
//     their values differ, so while a second manager keeps applying the same
//     value, a release by the first leaves the field standing and the bug is
//     invisible. A release test that does not deliberately drop the co-owner
//     reports a false pass.
package status

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// ControllerFields returns the complete set k8s.ManagerGrabarr owns, seeded
// from the live status so that an apply which changes one field still declares
// the other eight.
//
// It deliberately does NOT seed Conditions. The generated WithConditions
// APPENDS rather than replaces, so a seeded set plus the caller's set is
// rejected outright with `duplicate entries for key [type="Assigned"]`.
// Conditions are set in exactly one place per apply: the caller's mutate, from
// the full five-condition computation. There is nothing to carry forward
// because the reconciler derives all five on every pass.
//
// The five enum and pointer fields are omitted while empty, and that is a
// property of the object's SHAPE rather than of a reconcile's OUTCOME. An
// unassigned Download has no engine, a Download that has never failed has no
// failureReason, a Download that has never started has no startedAt -- and in
// each case the field is absent from the object, so there is nothing to
// release. phase and failureReason additionally could not be sent empty: both
// are CRD enums that reject "". Omitting a field because THIS reconcile could
// not compute it would be the outcome case, and that is the release bug.
//
// To CLEAR a field rather than carry it forward -- un-blocklisting, or
// clearing a failure on a retry -- assign the apply configuration's field
// directly (ac.BlocklistedUntil = nil, ac.FailureReason = nil). A With*
// helper cannot express "clear this", and the seed means omission by the
// caller is not a clear either: it is "keep what is there".
func ControllerFields(st downloadv1alpha1.DownloadStatus) *downloadac.DownloadStatusApplyConfiguration {
	ac := downloadac.DownloadStatus().WithObservedGeneration(st.ObservedGeneration)
	if st.Phase != "" {
		ac = ac.WithPhase(st.Phase)
	}
	if st.Engine != "" {
		ac = ac.WithEngine(st.Engine)
	}
	if st.FailureReason != "" {
		ac = ac.WithFailureReason(st.FailureReason)
	}
	if st.BlocklistedUntil != nil {
		ac = ac.WithBlocklistedUntil(*st.BlocklistedUntil)
	}
	if st.StartedAt != nil {
		ac = ac.WithStartedAt(*st.StartedAt)
	}
	if st.CompletedAt != nil {
		ac = ac.WithCompletedAt(*st.CompletedAt)
	}
	if st.SeedGoalMetAt != nil {
		ac = ac.WithSeedGoalMetAt(*st.SeedGoalMetAt)
	}
	return ac
}

// EngineFields returns the complete set k8s.ManagerGrabarrEngine owns, seeded
// from the live status.
//
// It is download.ApplyStatus over the inverse of itself, not a second copy of
// the field list, because a second copy is the failure this package exists to
// prevent: the engine writes through ApplyStatus and any field EngineFields
// forgot would be released by every controller-adjacent apply that used the
// seed, with nothing logged.
//
// The normal engine path does not need the seed at all -- an engine has a
// fresh download.Item and renders it directly -- so this is for the callers
// that must change one engine-owned field without a full observation.
func EngineFields(st downloadv1alpha1.DownloadStatus) *downloadac.DownloadStatusApplyConfiguration {
	return download.ApplyStatus(download.ItemFromStatus(st))
}

// Patch applies a complete status declaration under mgr.
//
// The caller mutates the seeded apply configuration rather than building one,
// which is what makes the complete-declaration rule enforceable in one place.
// mgr must be k8s.ManagerGrabarr or k8s.ManagerGrabarrEngine; anything else is
// a programming error and is refused rather than allowed to claim fields no
// part of the split accounts for. k8s.ManagerImportarr is refused too, even
// though it legitimately writes status.import -- that write is importarr's,
// made from importarr's own code, and routing it through grabarr's declaration
// would give it grabarr's owned set as well.
//
// dl is both the seed and the target. It must be FRESHLY READ: the seed is
// only as complete as the status it was taken from, so an object fetched
// before a slow transfer re-declares stale values and silently reverts
// whatever landed in between. A lost update is not a release and no
// release-regression test can see one.
//
// Two of the seeded fields are APPENDING lists and must not be re-sent through
// their With* helpers inside mutate. WithConditions appends, and
// ControllerFields leaves Conditions unseeded precisely so the caller can set
// them once. WithFiles appends, and EngineFields DOES seed Files, so a mutate
// that needs a different file list assigns ac.Files rather than calling
// WithFiles -- otherwise every entry appears twice and the listType=map merge
// fails on the duplicate keys.
func Patch(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	dl *downloadv1alpha1.Download,
	mutate func(*downloadac.DownloadStatusApplyConfiguration),
) error {
	var ac *downloadac.DownloadStatusApplyConfiguration
	switch mgr {
	case k8s.ManagerGrabarr:
		ac = ControllerFields(dl.Status)
	case k8s.ManagerGrabarrEngine:
		ac = EngineFields(dl.Status)
	default:
		return fmt.Errorf("status: %q owns no part of Download.status", mgr)
	}
	if mutate != nil {
		mutate(ac)
	}

	obj := downloadac.Download(dl.Name, dl.Namespace).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}
