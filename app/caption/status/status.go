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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// -----------------------------------------------------------------------
// SubtitleRequest
// -----------------------------------------------------------------------

// IsLive reports whether it is a LIVE item under the item-liveness protocol,
// the one rule both SubtitleRequest managers use to decide which entries of
// status.items exist:
//
//  1. An item is live if and only if the controller currently owns its
//     nextSearchAt. The controller sends a non-empty nextSearchAt for every
//     item it wants -- satisfied ones included -- and stops sending an item
//     it no longer wants.
//  2. The controller creates items with {langKey, nextSearchAt, attempts}
//     only.
//  3. The worker, on every apply, after its re-read, re-sends its leaves
//     ONLY for live items. It drops a non-live item from its apply, which
//     releases its leaves, so no manager owns the entry any more and
//     server-side apply deletes it. If a fetch task's own langKey is not
//     live, the want was withdrawn: the worker records nothing and acks.
//     The worker never creates an item.
//
// Why a protocol at all: a listType=map entry exists for as long as ANY
// manager owns ANY of its fields. Ruling R4 has each manager re-declare
// every item on every apply, so before this rule a language the controller
// stopped wanting lived forever -- the worker kept re-sending the entry it
// had once written, and nothing could ever remove it.
//
// Liveness is read from the object, not from managedFields: nextSearchAt
// has exactly one writer (the controller, per [RequestControllerFields]),
// so a non-empty value on a freshly read object is the controller's latest
// declaration. The read must be fresh for the same reason every seed must
// be (see [PatchRequest]).
//
// The controller's half of rule 1 had a trap of its own: rendering an entry
// without nextSearchAt as a bare langKey is still a claim on the entry, so a
// controller that seeded from a status still holding a withdrawn item kept
// it alive forever. [RequestControllerFields] therefore renders only live
// items too. That makes withdrawing an item a matter of leaving it out, or of
// clearing its nextSearchAt in the source -- either way nothing is sent --
// and it means a caller that wants an item MUST give it a non-empty
// nextSearchAt in the source status, not set one through the returned
// configuration after the fact.
func IsLive(it subtitlev1alpha1.SubtitleItem) bool {
	return it.NextSearchAt != nil && !it.NextSearchAt.IsZero()
}

// LiveItemKeys returns the langKeys of st's live items (see [IsLive]).
func LiveItemKeys(st subtitlev1alpha1.SubtitleRequestStatus) sets.Set[string] {
	out := sets.New[string]()
	for _, it := range st.Items {
		if IsLive(it) {
			out.Insert(it.LangKey)
		}
	}
	return out
}

// RequestControllerFields returns the complete set k8s.ManagerCaptionarr owns
// on SubtitleRequest.status, seeded from the live status so that an apply
// which changes one field still declares the others.
//
// It deliberately does NOT seed Conditions -- see the package doc's "One
// declaration per manager" section. Phase is an enum the CRD does not admit
// "" for, so a request that has not been planned yet omits it instead of
// sending a value the apiserver would reject; that is a property of the
// object's SHAPE (never planned) rather than of this reconcile's OUTCOME, the
// same exception app/indexer/status makes for Indexer.status.protocol and
// pkg/download.ApplyStatus makes for Download.status.stage.
// ObservedGeneration, ProfileGeneration and ProbeHash are plain,
// non-enum values, so -- like pkg/download.ApplyStatus's Message and
// OutputPath -- they are sent unconditionally, zero value included: an empty
// probeHash before the first probe is exactly as real a value as a populated
// one, and omitting it because this reconcile has nothing new to say would be
// the outcome-driven release CLAUDE.md warns about.
//
// Existing is rendered in full on every call, the same way
// pkg/download.ApplyStatus renders Files: looping and appending is safe here
// specifically because ac is freshly constructed inside this function and
// never seeded a second time.
//
// Items is the fragile half (ruling R4). Every LIVE entry in st.Items
// ([IsLive]) is rendered, and for each only langKey (the shared map key) and
// this manager's two owned leaves -- attempts and nextSearchAt -- are set. A
// non-live entry is not rendered at all, not even as a bare langKey, which
// would still claim the entry and keep a withdrawn item alive (see
// [IsLive]). A caller that needs to change one item's controller-owned
// leaves must mutate the SOURCE SubtitleRequestStatus (or assign directly
// into the returned configuration's Items slice) rather than call
// ac.WithItems again, which would append and double every entry -- the exact
// hazard [MediaFileStatus.Sidecars] hit in Phase C.
func RequestControllerFields(st subtitlev1alpha1.SubtitleRequestStatus) *subtitleac.SubtitleRequestStatusApplyConfiguration {
	ac := subtitleac.SubtitleRequestStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithProfileGeneration(st.ProfileGeneration).
		WithProbeHash(st.ProbeHash)
	if st.Phase != "" {
		ac = ac.WithPhase(st.Phase)
	}
	if st.FileFingerprint != nil {
		ac = ac.WithFileFingerprint(*st.FileFingerprint)
	}
	for _, e := range st.Existing {
		exAC := subtitleac.ExistingSub().WithLangKey(e.LangKey).WithSource(e.Source).WithPath(e.Path)
		if e.StreamIndex != nil {
			exAC = exAC.WithStreamIndex(*e.StreamIndex)
		}
		ac = ac.WithExisting(exAC)
	}

	items := make([]*subtitleac.SubtitleItemApplyConfiguration, 0, len(st.Items))
	for _, it := range st.Items {
		if !IsLive(it) {
			continue
		}
		items = append(items, requestControllerItemAC(it))
	}
	ac.Items = itemSlice(items)
	return ac
}

// requestControllerItemAC renders the k8s.ManagerCaptionarr half of one
// SubtitleItem: the shared map key plus attempts and nextSearchAt, and
// nothing else. Both are pointer-shaped (Attempts is rendered as a whole
// value, the same atomic treatment pkg/download.ApplyStatus gives Health) and
// omitted while zero/nil -- a language the controller has never scheduled a
// search for has neither, and that absence is the CRD's documented shape for
// "not yet attempted", not an outcome this reconcile failed to compute.
func requestControllerItemAC(it subtitlev1alpha1.SubtitleItem) *subtitleac.SubtitleItemApplyConfiguration {
	ac := subtitleac.SubtitleItem().WithLangKey(it.LangKey)
	if it.Attempts != (commonv1alpha1.Attempts{}) {
		ac = ac.WithAttempts(it.Attempts)
	}
	if it.NextSearchAt != nil {
		ac = ac.WithNextSearchAt(*it.NextSearchAt)
	}
	return ac
}

// RequestWorkerFields returns the complete set k8s.ManagerCaptionarrWorker
// owns on SubtitleRequest.status: every leaf of every LIVE item except
// nextSearchAt and attempts, which belong to the controller.
//
// Every live entry in st.Items is rendered, for the same reason
// [RequestControllerFields] renders every entry: ruling R4 requires each
// manager to re-send every leaf it owns for every item on every apply, not
// just the item this call is actually updating -- a worker that only rendered
// the item it just searched would release every other item's state on its
// next write.
//
// A non-live entry is deliberately NOT rendered: that release is rule 3 of
// the item-liveness protocol ([IsLive]). Once the controller stops sending
// an item's nextSearchAt, this apply releases the worker's leaves on it,
// nothing owns the entry, and server-side apply deletes it.
func RequestWorkerFields(st subtitlev1alpha1.SubtitleRequestStatus) *subtitleac.SubtitleRequestStatusApplyConfiguration {
	ac := subtitleac.SubtitleRequestStatus()
	items := make([]*subtitleac.SubtitleItemApplyConfiguration, 0, len(st.Items))
	for _, it := range st.Items {
		if !IsLive(it) {
			continue
		}
		items = append(items, requestWorkerItemAC(it))
	}
	ac.Items = itemSlice(items)
	return ac
}

// requestWorkerItemAC renders the k8s.ManagerCaptionarrWorker half of one
// SubtitleItem.
//
// State is an enum the CRD does not admit "" for, so it is sent only once a
// worker has actually observed one -- the same shape exception
// requestControllerItemAC and pkg/download.ApplyStatus make for their own
// enums. Score and scoreOutOf are plain, non-pointer integers and are sent
// UNCONDITIONALLY, zero included, matching pkg/download.ApplyStatus's
// treatment of its own telemetry counters: a candidate that genuinely scored
// 0 and "no candidate chosen yet" are indistinguishable if either is allowed
// to omit the field, and omitting on a transient outcome is the release bug.
// Provider, subtitleID, path and lastError are plain strings and are sent
// unconditionally for the same reason pkg/download.ApplyStatus sends Message
// and OutputPath unconditionally. downloadedAt is a pointer and is omitted
// while nil -- a language never downloaded has no downloadedAt, which is the
// CRD's documented shape.
func requestWorkerItemAC(it subtitlev1alpha1.SubtitleItem) *subtitleac.SubtitleItemApplyConfiguration {
	ac := subtitleac.SubtitleItem().
		WithLangKey(it.LangKey).
		WithScore(it.Score).
		WithScoreOutOf(it.ScoreOutOf).
		WithProvider(it.Provider).
		WithSubtitleID(it.SubtitleID).
		WithPath(it.Path).
		WithLastError(it.LastError)
	if it.State != "" {
		ac = ac.WithState(it.State)
	}
	if it.DownloadedAt != nil {
		ac = ac.WithDownloadedAt(*it.DownloadedAt)
	}
	return ac
}

// itemSlice dereferences a slice of item apply configurations into the value
// slice the generated Items field holds, or nil for an empty input so an
// empty status renders no items key at all rather than an empty list -- items
// is optional, and there is nothing to declare for a request with none.
func itemSlice(items []*subtitleac.SubtitleItemApplyConfiguration) []subtitleac.SubtitleItemApplyConfiguration {
	if len(items) == 0 {
		return nil
	}
	out := make([]subtitleac.SubtitleItemApplyConfiguration, len(items))
	for i, it := range items {
		out[i] = *it
	}
	return out
}

// PatchRequest applies a complete SubtitleRequest.status declaration under
// mgr.
//
// The caller mutates the seeded apply configuration rather than building one,
// which is what makes the complete-declaration rule enforceable in one place.
// mgr must be k8s.ManagerCaptionarr or k8s.ManagerCaptionarrWorker; anything
// else is a programming error and is refused rather than allowed to claim
// fields no part of the split accounts for.
//
// req is both the seed and the target. It must be FRESHLY READ: the seed is
// only as complete as the status it was taken from, so an object fetched
// before the worker's slow provider search re-declares stale item state and
// silently reverts whatever landed in between. A lost update is not a release
// and no release-regression test can see one.
//
// mutate must not call ac.WithItems: both [RequestControllerFields] and
// [RequestWorkerFields] already seed Items exactly once, and WithItems
// appends, so a second call doubles every entry. A mutate that needs
// different items assigns ac.Items directly.
func PatchRequest(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	req *subtitlev1alpha1.SubtitleRequest,
	mutate func(*subtitleac.SubtitleRequestStatusApplyConfiguration),
) error {
	var ac *subtitleac.SubtitleRequestStatusApplyConfiguration
	switch mgr {
	case k8s.ManagerCaptionarr:
		ac = RequestControllerFields(req.Status)
	case k8s.ManagerCaptionarrWorker:
		ac = RequestWorkerFields(req.Status)
	default:
		return fmt.Errorf("status: %q owns no part of SubtitleRequest.status", mgr)
	}
	if mutate != nil {
		mutate(ac)
	}

	obj := subtitleac.SubtitleRequest(req.Name, req.Namespace).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}

// -----------------------------------------------------------------------
// SubtitleProfile
// -----------------------------------------------------------------------

// ProfileFields returns the complete set k8s.ManagerCaptionarr owns on
// SubtitleProfile.status -- all of it, since the profile reconciler is its
// only writer.
//
// It still follows the seed-from-live-status shape the split-manager kinds
// use, for the same reason app/indexer/status.ControllerFields does despite
// Indexer's config half also having one writer: one declaration in one place
// is what stops a second call site from hand-building its own apply
// configuration and forgetting a field. WantedKeys and Conditions are left
// unseeded -- both are rendered through generated With* methods that APPEND
// rather than replace, and the profile reconciler recomputes both from
// scratch on every reconcile, so there is nothing to carry forward and
// seeding them would only risk a caller doubling them via a second With*
// call, the same hazard the package doc describes for status.items.
// MatchingFiles is a plain, non-pointer counter and is sent unconditionally,
// zero included, matching pkg/download.ApplyStatus's telemetry fields: a
// profile that currently matches no files is a real value, not an absence.
func ProfileFields(st subtitlev1alpha1.SubtitleProfileStatus) *subtitleac.SubtitleProfileStatusApplyConfiguration {
	return subtitleac.SubtitleProfileStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithMatchingFiles(st.MatchingFiles)
}

// PatchProfile applies a complete SubtitleProfile.status declaration under
// mgr.
//
// mgr must be k8s.ManagerCaptionarr; every other manager, including
// k8s.ManagerCaptionarrWorker, is refused. SubtitleProfile has exactly one
// legitimate writer.
func PatchProfile(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	profile *subtitlev1alpha1.SubtitleProfile,
	mutate func(*subtitleac.SubtitleProfileStatusApplyConfiguration),
) error {
	if mgr != k8s.ManagerCaptionarr {
		return fmt.Errorf("status: %q owns no part of SubtitleProfile.status", mgr)
	}
	ac := ProfileFields(profile.Status)
	if mutate != nil {
		mutate(ac)
	}

	obj := subtitleac.SubtitleProfile(profile.Name).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}

// -----------------------------------------------------------------------
// SubtitleProvider
// -----------------------------------------------------------------------

// ProviderFields returns the complete set k8s.ManagerCaptionarr owns on
// SubtitleProvider.status -- all of it. Ruling R2 makes this the load-bearing
// half of the package: workers observe throttles into the shared
// clustarr-provider-throttle KV bucket, never onto this object directly, and
// only the provider controller projects that KV state into
// status.throttledUntil/throttleReason/quota/errorsLast120s and the
// conditions. [PatchProvider] enforces that by refusing
// k8s.ManagerCaptionarrWorker the same way it refuses every manager outside
// the split.
//
// ThrottledUntil, TokenExpiresAt and LastSuccessAt are pointers and are
// omitted while nil -- an unthrottled, never-authenticated or never-queried
// provider legitimately has none, which is the shape a fresh SubtitleProvider
// is in. To CLEAR one of these once set -- the provider's throttle window
// elapsing, for example -- a caller assigns the returned configuration's
// field directly (ac.ThrottledUntil = nil), the same pattern
// app/grab/status.ControllerFields documents for BlocklistedUntil: the seed
// means omission by mutate is "keep what is there", not "clear this", and a
// With* helper cannot express a clear either.
//
// ThrottleReason, ErrorsLast120s and HIVerifiable are plain, non-pointer
// values and are sent unconditionally, matching pkg/download.ApplyStatus's
// treatment of its own telemetry: an empty reason or a zero error count is a
// real value once a provider exists, not an absence.
//
// Quota, when present, is rendered in full through [providerQuotaAC] --
// every leaf, zero values included -- the same discipline app/indexer/status's
// capsAC applies to Indexer.status.caps: server-side apply tracks ownership
// per leaf inside a struct, so a renderer that sent only Remaining would
// release ResetAt on every apply that did not also recompute it.
func ProviderFields(st subtitlev1alpha1.SubtitleProviderStatus) *subtitleac.SubtitleProviderStatusApplyConfiguration {
	ac := subtitleac.SubtitleProviderStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithThrottleReason(st.ThrottleReason).
		WithErrorsLast120s(st.ErrorsLast120s).
		WithHIVerifiable(st.HIVerifiable)
	if st.ThrottledUntil != nil {
		ac = ac.WithThrottledUntil(*st.ThrottledUntil)
	}
	if st.TokenExpiresAt != nil {
		ac = ac.WithTokenExpiresAt(*st.TokenExpiresAt)
	}
	if st.LastSuccessAt != nil {
		ac = ac.WithLastSuccessAt(*st.LastSuccessAt)
	}
	if st.Quota != nil {
		ac = ac.WithQuota(providerQuotaAC(st.Quota))
	}
	return ac
}

// providerQuotaAC renders status.quota with every leaf set, zero values
// included, the same complete-declaration discipline app/indexer/status.capsAC
// documents for status.caps.
func providerQuotaAC(q *subtitlev1alpha1.ProviderQuota) *subtitleac.ProviderQuotaApplyConfiguration {
	return subtitleac.ProviderQuota().WithRemaining(q.Remaining).WithResetAt(q.ResetAt)
}

// PatchProvider applies a complete SubtitleProvider.status declaration under
// mgr.
//
// mgr must be k8s.ManagerCaptionarr; every other manager, including
// k8s.ManagerCaptionarrWorker, is refused. See ruling R2 and [ProviderFields].
func PatchProvider(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	provider *subtitlev1alpha1.SubtitleProvider,
	mutate func(*subtitleac.SubtitleProviderStatusApplyConfiguration),
) error {
	if mgr != k8s.ManagerCaptionarr {
		return fmt.Errorf("status: %q owns no part of SubtitleProvider.status", mgr)
	}
	ac := ProviderFields(provider.Status)
	if mutate != nil {
		mutate(ac)
	}

	obj := subtitleac.SubtitleProvider(provider.Name, provider.Namespace).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}
