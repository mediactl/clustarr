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

	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// -----------------------------------------------------------------------
// SubtitleProfile
// -----------------------------------------------------------------------

// ProfileFields returns the complete set k8s.ManagerCaption owns on
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
// mgr must be k8s.ManagerCaption; every other manager is refused.
// SubtitleProfile has exactly one legitimate writer.
func PatchProfile(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	profile *subtitlev1alpha1.SubtitleProfile,
	mutate func(*subtitleac.SubtitleProfileStatusApplyConfiguration),
) error {
	if mgr != k8s.ManagerCaption {
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

// ProviderFields returns the complete set k8s.ManagerCaption owns on
// SubtitleProvider.status -- all of it. Ruling R2 makes this the load-bearing
// half of the package: workers observe throttles into the shared
// clustarr-provider-throttle KV bucket, never onto this object directly, and
// only the provider controller projects that KV state into
// status.throttledUntil/throttleReason/quota/errorsLast120s and the
// conditions. [PatchProvider] enforces that by refusing every other
// manager.
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
// mgr must be k8s.ManagerCaption; every other manager is refused. See
// ruling R2 and [ProviderFields].
func PatchProvider(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	provider *subtitlev1alpha1.SubtitleProvider,
	mutate func(*subtitleac.SubtitleProviderStatusApplyConfiguration),
) error {
	if mgr != k8s.ManagerCaption {
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
