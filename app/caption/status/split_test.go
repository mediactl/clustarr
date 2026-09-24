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
	"k8s.io/apimachinery/pkg/util/sets"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// ---------------------------------------------------------------------
// SubtitleRequest.status -- the request-level split
// ---------------------------------------------------------------------

// The top-level split, restated as data so a field added to
// SubtitleRequestStatus later cannot quietly belong to nobody. Items is
// listed separately below: it is a list, not a scalar, and its OWN leaves
// split between the two managers per ruling R4.
var (
	requestControllerOwned = []string{
		"ObservedGeneration", "Phase", "ProfileGeneration", "ProbeHash",
		"FileFingerprint", "Existing", "Conditions",
	}
	requestSpecialFields = []string{"Items"}
)

// The per-item split (ruling R4). LangKey is the listType=map key and is
// listed separately: both managers send it, deliberately, to identify the
// entry they are touching.
var (
	itemControllerOwned = []string{"Attempts", "NextSearchAt"}
	itemWorkerOwned     = []string{"State", "Score", "ScoreOutOf", "Provider", "SubtitleID", "Path", "LastError", "DownloadedAt"}
	itemSharedKey       = []string{"LangKey"}
)

// fullRequestStatus sets every field of SubtitleRequestStatus, including one
// item with every leaf non-zero, so "did the declaration send this?" is
// answerable by looking at whether the apply configuration's pointer is nil.
//
// A blank status would make every conditional field look unsent and the test
// would pass whatever the declarations did -- the same shape as a
// release-regression test run against a blank object, which is how this class
// of defect survived three reviews in Phase C.
func fullRequestStatus() subtitlev1alpha1.SubtitleRequestStatus {
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	streamIdx := int32(2)
	return subtitlev1alpha1.SubtitleRequestStatus{
		ObservedGeneration: 4,
		Phase:              subtitlev1alpha1.SubtitleRequestPhaseSearching,
		ProfileGeneration:  2,
		ProbeHash:          "sha256:abc123",
		FileFingerprint:    &commonv1alpha1.FileFingerprint{SizeBytes: 7 << 30, ModTime: &at},
		Existing: []subtitlev1alpha1.ExistingSub{
			{LangKey: "en", Source: subtitlev1alpha1.SubtitleSourceEmbedded, Path: "", StreamIndex: &streamIdx},
		},
		Items: []subtitlev1alpha1.SubtitleItem{
			{
				LangKey:      "en",
				State:        subtitlev1alpha1.SubtitleItemDownloaded,
				Score:        95,
				ScoreOutOf:   100,
				Provider:     "opensubtitlescom",
				SubtitleID:   "12345",
				Path:         "Movie.en.srt",
				Attempts:     commonv1alpha1.Attempts{Initial: &at, Latest: &at, Count: 3},
				NextSearchAt: &at,
				LastError:    "temporary provider error",
				DownloadedAt: &at,
			},
		},
		Conditions: []metav1.Condition{{Type: subtitlev1alpha1.SubtitleRequestConditionPlanned}},
	}
}

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

// Every field of SubtitleRequestStatus must be accounted for by exactly one
// of the lists above. Without this, a field added later would be owned by
// nobody: no writer sends it, so it can never be released, and the defect is
// invisible until someone notices the field never changes -- or, worse, a
// later change claims it in both declarations and each apply starts deleting
// the other's value.
func TestEverySubtitleRequestStatusFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(subtitlev1alpha1.SubtitleRequestStatus{})

	claimed := map[string]int{}
	for _, list := range [][]string{requestControllerOwned, requestSpecialFields} {
		for _, name := range list {
			claimed[name]++
		}
	}

	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name],
			"SubtitleRequestStatus.%s is claimed %d times across {captionarr, Items}; it must be exactly one",
			name, claimed[name])
	}
	require.Len(t, requestControllerOwned, 7)
}

// Every field of SubtitleItem must be accounted for by exactly one of the
// controller, worker or shared-key lists.
func TestEverySubtitleItemFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(subtitlev1alpha1.SubtitleItem{})

	claimed := map[string]int{}
	for _, list := range [][]string{itemControllerOwned, itemWorkerOwned, itemSharedKey} {
		for _, name := range list {
			claimed[name]++
		}
	}

	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name],
			"SubtitleItem.%s is claimed %d times across {controller, worker, shared key}; it must be exactly one",
			name, claimed[name])
	}
	require.Len(t, itemControllerOwned, 2)
	require.Len(t, itemWorkerOwned, 8)
}

// RequestControllerFields must declare its whole top-level set, plus exactly
// the shared key and its two owned leaves on every item -- and nothing else.
// Conditions is the documented exception: the generated WithConditions
// appends, so seeding it would collide with the caller's own set.
func TestRequestControllerFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := status.RequestControllerFields(fullRequestStatus())

	want := make([]string, 0, len(requestControllerOwned))
	for _, n := range requestControllerOwned {
		if n == "Conditions" {
			continue
		}
		want = append(want, n)
	}
	want = append(want, "Items")
	sort.Strings(want)

	assert.Equal(t, want, setFields(got))

	require.Len(t, got.Items, 1)
	wantItem := append(append([]string(nil), itemControllerOwned...), itemSharedKey...)
	sort.Strings(wantItem)
	assert.Equal(t, wantItem, setFields(&got.Items[0]))
}

// RequestWorkerFields must declare Items only -- nothing at the request
// level -- and, per item, exactly the shared key plus its eight owned leaves.
func TestRequestWorkerFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := status.RequestWorkerFields(fullRequestStatus())

	assert.Equal(t, []string{"Items"}, setFields(got))

	require.Len(t, got.Items, 1)
	wantItem := append(append([]string(nil), itemWorkerOwned...), itemSharedKey...)
	sort.Strings(wantItem)
	assert.Equal(t, wantItem, setFields(&got.Items[0]))
}

// The item-liveness protocol (IsLive's doc): an item is live exactly when
// it carries a non-empty nextSearchAt.
func TestIsLiveAndLiveItemKeys(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC))
	var zero metav1.Time
	st := subtitlev1alpha1.SubtitleRequestStatus{Items: []subtitlev1alpha1.SubtitleItem{
		{LangKey: "en", NextSearchAt: &at},
		{LangKey: "es", State: subtitlev1alpha1.SubtitleItemDownloaded, Path: "Movie.es.srt"},
		{LangKey: "fr", NextSearchAt: &zero},
	}}

	assert.True(t, status.IsLive(st.Items[0]))
	assert.False(t, status.IsLive(st.Items[1]), "a worker-only entry is a withdrawn want, however complete")
	assert.False(t, status.IsLive(st.Items[2]), "a zero time serialises as null: not a declaration")
	assert.Equal(t, []string{"en"}, sets.List(status.LiveItemKeys(st)))
}

// Rule 3 of the protocol: the worker renders only live items, so a withdrawn
// entry is released by the worker's next apply and deleted.
func TestRequestWorkerFieldsRendersOnlyLiveItems(t *testing.T) {
	st := fullRequestStatus()
	st.Items = append(st.Items, subtitlev1alpha1.SubtitleItem{
		LangKey: "es", State: subtitlev1alpha1.SubtitleItemDownloaded, Score: 80, Path: "Movie.es.srt",
	})

	got := status.RequestWorkerFields(st)
	require.Len(t, got.Items, 1)
	assert.Equal(t, "en", *got.Items[0].LangKey)
}

// The two declarations' PER-ITEM leaves must not overlap outside the shared
// key. An overlap would not fail loudly at runtime: pkg/k8s.PatchStatus
// applies with ForceOwnership, so the apiserver transfers a contested field
// in silence and the losing manager's next apply simply takes it back.
func TestTheItemDeclarationsAreDisjointOutsideTheSharedKey(t *testing.T) {
	st := fullRequestStatus()
	controller := setFields(&status.RequestControllerFields(st).Items[0])
	worker := setFields(&status.RequestWorkerFields(st).Items[0])

	workerSet := map[string]bool{}
	for _, n := range worker {
		workerSet[n] = true
	}
	for _, n := range controller {
		if n == "LangKey" {
			assert.True(t, workerSet[n], "LangKey must be co-owned: the worker declaration omitted it")
			continue
		}
		assert.Falsef(t, workerSet[n], "%s is declared by BOTH the controller and worker item views", n)
	}
}

// The item-liveness protocol ([status.IsLive]) on the controller's side. This
// test used to assert the opposite -- that an item with neither attempts nor
// nextSearchAt still rendered as a bare langKey, so that R4's "re-declare
// every item" could not accidentally drop it. That bare langKey was exactly
// the bug: it is still an ownership claim on the list entry, so an item the
// controller withdrew (by no longer scheduling it) stayed on the object
// forever, and with items[].state once required, an entry left holding only
// langKey after the worker released its leaves failed validation outright.
// Now a non-live item is not rendered at all, and a live one with only its
// schedule renders exactly langKey and nextSearchAt.
func TestRequestControllerFieldsRendersOnlyLiveItems(t *testing.T) {
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	st := subtitlev1alpha1.SubtitleRequestStatus{
		Items: []subtitlev1alpha1.SubtitleItem{
			{LangKey: "es", State: subtitlev1alpha1.SubtitleItemPending},
			{LangKey: "fr", NextSearchAt: &metav1.Time{}},
			{LangKey: "de", NextSearchAt: &at},
		},
	}
	got := status.RequestControllerFields(st)
	require.Len(t, got.Items, 1, "only the live item renders; es has no nextSearchAt and fr a zero one")
	assert.Equal(t, "de", *got.Items[0].LangKey)
	assert.Equal(t, []string{"LangKey", "NextSearchAt"}, setFields(&got.Items[0]))
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
	// discipline indexarr/status.capsAC documents for status.caps.
	require.NotNil(t, got.Quota)
	assert.Equal(t, []string{"Remaining", "ResetAt"}, setFields(got.Quota))
}

// ---------------------------------------------------------------------
// Patch refuses every manager outside each kind's split.
// ---------------------------------------------------------------------

func TestPatchRequestRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{k8s.ManagerCatalogarr, k8s.FieldManager("captionarr-bogus"), ""} {
		err := status.PatchRequest(t.Context(), nil, mgr,
			&subtitlev1alpha1.SubtitleRequest{}, nil)
		require.ErrorContains(t, err, "owns no part of SubtitleRequest.status")
	}
}

func TestPatchProfileRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{k8s.ManagerCatalogarr, k8s.ManagerCaptionarrWorker, ""} {
		err := status.PatchProfile(t.Context(), nil, mgr,
			&subtitlev1alpha1.SubtitleProfile{}, nil)
		require.ErrorContains(t, err, "owns no part of SubtitleProfile.status")
	}
}

func TestPatchProviderRefusesAManagerOutsideTheSplit(t *testing.T) {
	// captionarr-worker is deliberately in this list: ruling R2 forbids a
	// worker from writing SubtitleProvider.status directly, even though it is
	// a legitimate manager elsewhere in the project.
	for _, mgr := range []k8s.FieldManager{k8s.ManagerCatalogarr, k8s.ManagerCaptionarrWorker, ""} {
		err := status.PatchProvider(t.Context(), nil, mgr,
			&subtitlev1alpha1.SubtitleProvider{}, nil)
		require.ErrorContains(t, err, "owns no part of SubtitleProvider.status")
	}
}
