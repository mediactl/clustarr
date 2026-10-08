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

package status_test

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// setFields returns the names of every non-nil field of an apply
// configuration.
func setFields(ac any) []string {
	v := reflect.ValueOf(ac).Elem()
	typ := v.Type()
	var out []string
	for i := range typ.NumField() {
		if !v.Field(i).IsZero() {
			out = append(out, typ.Field(i).Name)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------
// SubtitleProfile.status -- a single writer, declared here to keep
// "owned by nobody" impossible rather than merely unlikely.
// ---------------------------------------------------------------------

var (
	profileOwned    = []string{"ObservedGeneration", "MatchingFiles"}
	profileUnseeded = []string{"WantedKeys", "Conditions"}
)

func fullProfileStatus() subtitlev1alpha1.SubtitleProfileStatus {
	return subtitlev1alpha1.SubtitleProfileStatus{
		ObservedGeneration: 3,
		WantedKeys:         []string{"en", "en:forced"},
		MatchingFiles:      42,
		Conditions:         []metav1.Condition{{Type: subtitlev1alpha1.SubtitleProfileConditionReady}},
	}
}

func TestEverySubtitleProfileStatusFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(subtitlev1alpha1.SubtitleProfileStatus{})

	claimed := map[string]int{}
	for _, list := range [][]string{profileOwned, profileUnseeded} {
		for _, name := range list {
			claimed[name]++
		}
	}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name], "SubtitleProfileStatus.%s is claimed %d times", name, claimed[name])
	}
}

func TestProfileFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := status.ProfileFields(fullProfileStatus())
	want := append([]string(nil), profileOwned...)
	sort.Strings(want)
	assert.Equal(t, want, setFields(got))
}

// ---------------------------------------------------------------------
// SubtitleProvider.status -- a single writer; ruling R2 makes refusing
// every other manager (including the worker) load-bearing.
// ---------------------------------------------------------------------

var (
	providerOwned    = []string{"ObservedGeneration", "ThrottledUntil", "ThrottleReason", "Quota", "TokenExpiresAt", "LastSuccessAt", "ErrorsLast120s", "HIVerifiable"}
	providerUnseeded = []string{"Conditions"}
)

func fullProviderStatus() subtitlev1alpha1.SubtitleProviderStatus {
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	return subtitlev1alpha1.SubtitleProviderStatus{
		ObservedGeneration: 5,
		ThrottledUntil:     &at,
		ThrottleReason:     "5 errors in 120s",
		Quota:              &subtitlev1alpha1.ProviderQuota{Remaining: 0, ResetAt: at},
		TokenExpiresAt:     &at,
		LastSuccessAt:      &at,
		ErrorsLast120s:     5,
		HIVerifiable:       true,
		Conditions:         []metav1.Condition{{Type: subtitlev1alpha1.SubtitleProviderConditionReady}},
	}
}

func TestEverySubtitleProviderStatusFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(subtitlev1alpha1.SubtitleProviderStatus{})

	claimed := map[string]int{}
	for _, list := range [][]string{providerOwned, providerUnseeded} {
		for _, name := range list {
			claimed[name]++
		}
	}
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name], "SubtitleProviderStatus.%s is claimed %d times", name, claimed[name])
	}
}

func TestProviderFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := status.ProviderFields(fullProviderStatus())
	want := append([]string(nil), providerOwned...)
	sort.Strings(want)
	assert.Equal(t, want, setFields(got))

	// Quota is a struct-typed leaf: both its own fields must be present even
	// though Remaining is the zero value, the same complete-declaration
	// discipline app/indexer/status.capsAC documents for status.caps.
	require.NotNil(t, got.Quota)
	assert.Equal(t, []string{"Remaining", "ResetAt"}, setFields(got.Quota))
}

// ---------------------------------------------------------------------
// Patch refuses every manager outside each kind's split.
// ---------------------------------------------------------------------

func TestPatchProfileRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{k8s.ManagerCatalog, k8s.ManagerImportWorker, ""} {
		err := status.PatchProfile(t.Context(), nil, mgr,
			&subtitlev1alpha1.SubtitleProfile{}, nil)
		require.ErrorContains(t, err, "owns no part of SubtitleProfile.status")
	}
}

func TestPatchProviderRefusesAManagerOutsideTheSplit(t *testing.T) {
	// A worker manager is deliberately in this list: ruling R2 forbids a
	// worker from writing SubtitleProvider.status directly. caption-worker is
	// retired (the caption agent writes no Kubernetes object, ADR-0019), so
	// import-worker, a legitimate manager elsewhere, stands in for it.
	for _, mgr := range []k8s.FieldManager{k8s.ManagerCatalog, k8s.ManagerImportWorker, ""} {
		err := status.PatchProvider(t.Context(), nil, mgr,
			&subtitlev1alpha1.SubtitleProvider{}, nil)
		require.ErrorContains(t, err, "owns no part of SubtitleProvider.status")
	}
}
