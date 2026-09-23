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
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))
}

func newSubtitleRequest(t *testing.T, ctx context.Context, c client.Client, ns, name string) *subtitlev1alpha1.SubtitleRequest {
	t.Helper()
	req := &subtitlev1alpha1.SubtitleRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: name},
	}
	require.NoError(t, c.Create(ctx, req))
	return req
}

// This is the test ruling R4 asks for: the controller and worker halves of
// SubtitleRequest.status.items must not release each other's leaves, on an
// object that already carries BOTH managers' state before either re-applies.
//
// The object is driven to a populated steady state by BOTH writers first --
// two items, "en" and "es", each fully described by the worker and each
// given controller-owned leaves too -- before either applies a second time. A
// test that starts from a blank object cannot observe a release, because
// there is nothing to release.
//
// Every value assertion below is repeated by [assertRequestManagedFieldsSplit]
// at the end, which reads the apiserver's own managedFields record instead of
// the object's values. That second pass is not redundant: pkg/k8s.PatchStatus
// applies with ForceOwnership, so an over-claim -- a manager starting to
// declare a leaf outside its half -- hands the leaf over in silence and every
// value assertion above it keeps passing. Only managedFields shows it.
func TestTheRequestManagersDoNotReleaseEachOthersItemLeaves(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "captionarr-status-split"
	newNamespace(t, ctx, c, ns)

	req := newSubtitleRequest(t, ctx, c, ns, "split")
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))

	// Steady state, half one: the worker's two items, "en" and "es", each
	// with every worker-owned leaf set.
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarrWorker, req,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			ac.Items = []subtitleac.SubtitleItemApplyConfiguration{
				*subtitleac.SubtitleItem().WithLangKey("en").WithState(subtitlev1alpha1.SubtitleItemDownloaded).
					WithScore(95).WithScoreOutOf(100).WithProvider("opensubtitlescom").
					WithSubtitleID("os-en-1").WithPath("split.en.srt").WithLastError(""),
				*subtitleac.SubtitleItem().WithLangKey("es").WithState(subtitlev1alpha1.SubtitleItemDownloaded).
					WithScore(80).WithScoreOutOf(100).WithProvider("gestdown").
					WithSubtitleID("gd-es-1").WithPath("split.es.srt").WithLastError(""),
			}
		}))

	// Steady state, half two: the controller's request-level fields, plus
	// attempts/nextSearchAt for "en" and nextSearchAt alone for "es" -- es
	// carries no attempts, so the split is also proven on an item where the
	// controller owns only its key and its schedule. Both items must be
	// scheduled here: an item without the controller's nextSearchAt is not
	// live (IsLive), and the worker's next apply would rightly delete it.
	//
	// The schedule is set on the SOURCE status, not through the returned
	// configuration: RequestControllerFields renders only live items, and
	// both are not live until this very apply makes them so.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(req), req))
	for i := range req.Status.Items {
		req.Status.Items[i].NextSearchAt = &now
		if req.Status.Items[i].LangKey == "en" {
			req.Status.Items[i].Attempts = commonv1alpha1.Attempts{Initial: &now, Latest: &now, Count: 1}
		}
	}
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarr, req,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).WithPhase(subtitlev1alpha1.SubtitleRequestPhaseSearching).
				WithConditions(k8s.ConditionAC(metav1.Condition{
					Type: subtitlev1alpha1.SubtitleRequestConditionPlanned, Status: metav1.ConditionTrue,
					Reason: "Planned", LastTransitionTime: now, ObservedGeneration: 1,
				}))
		}))

	var seeded subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(req), &seeded))
	require.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, seeded.Status.Phase, "setup: the controller half did not land")
	require.Len(t, seeded.Status.Items, 2, "setup: the worker half did not land")
	enSeeded := findItem(t, seeded.Status.Items, "en")
	require.NotNil(t, enSeeded.NextSearchAt, "setup: the controller's per-item half did not land")
	esSeeded := findItem(t, seeded.Status.Items, "es")
	require.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, esSeeded.State, "setup: the worker's es item did not land")

	// Now each manager applies again, changing only its own fields, and on
	// only ONE of the two items -- exactly the shape that would leak an
	// under-declared "every item" loop: a WorkerFields that only rendered the
	// item it just changed would release the OTHER item's worker leaves.
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarrWorker, &seeded,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			for i := range ac.Items {
				if ac.Items[i].LangKey != nil && *ac.Items[i].LangKey == "en" {
					ac.Items[i].Score = ptrInt32(97)
				}
			}
		}))

	var afterWorker subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(req), &afterWorker))
	assertControllerHalfIntact(t, afterWorker.Status, "the worker apply")
	esAfterWorker := findItem(t, afterWorker.Status.Items, "es")
	assertWorkerItemIntact(t, esAfterWorker, "the worker's own re-apply (es untouched this round)", 80)
	enAfterWorker := findItem(t, afterWorker.Status.Items, "en")
	assert.EqualValues(t, 97, enAfterWorker.Score, "the worker's own field did not update")

	// And the controller applies again, changing observedGeneration and
	// rescheduling "es", while "en" keeps its existing controller-owned
	// leaves unmodified in this call's mutate.
	later := metav1.NewTime(now.Add(time.Hour))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(req), req))
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarr, req,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			// Conditions is unseeded by design (see RequestControllerFields'
			// doc), so a real caller recomputes and resends the full set on
			// every apply. Omitting this call here would be a caller bug --
			// releasing conditions -- not something the package should paper
			// over, so the test mirrors real usage rather than skip it.
			ac.WithObservedGeneration(2).WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: subtitlev1alpha1.SubtitleRequestConditionPlanned, Status: metav1.ConditionTrue,
				Reason: "Planned", LastTransitionTime: now, ObservedGeneration: 2,
			}))
			for i := range ac.Items {
				if ac.Items[i].LangKey != nil && *ac.Items[i].LangKey == "es" {
					ac.Items[i].NextSearchAt = &later
				}
			}
		}))

	var afterController subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(req), &afterController))
	assert.EqualValues(t, 2, afterController.Status.ObservedGeneration, "the controller's own field did not update")
	enAfterController := findItem(t, afterController.Status.Items, "en")
	assertWorkerItemIntact(t, enAfterController, "the controller apply", 97)
	assert.NotNil(t, enAfterController.NextSearchAt, "the controller apply released its own prior nextSearchAt on en")
	if assert.NotEqual(t, commonv1alpha1.Attempts{}, enAfterController.Attempts,
		"the controller's own re-apply released its own prior attempts on en") {
		assert.EqualValues(t, 1, enAfterController.Attempts.Count)
	}
	esAfterController := findItem(t, afterController.Status.Items, "es")
	assertWorkerItemIntact(t, esAfterController, "the controller apply", 80)
	if assert.NotNil(t, esAfterController.NextSearchAt, "the controller released its own nextSearchAt on es") {
		assert.True(t, esAfterController.NextSearchAt.Equal(&later), "the controller's reschedule did not land on es")
	}

	// Finally, assert OWNERSHIP rather than values. Every assertion above
	// compares what is on the object, and that class of assertion
	// structurally cannot see an over-claim: if the controller started
	// declaring, say, items[].state, server-side apply would hand the leaf
	// over under ForceOwnership, the value would be identical (both managers
	// would be sending "downloaded"), and nothing on the object would change.
	// managedFields is the only place that is visible. Verified: adding a
	// State claim to requestControllerItemAC leaves every value assertion
	// above still passing.
	assertRequestManagedFieldsSplit(t, &afterController)
}

func ptrInt32(v int32) *int32 { return &v }

func findItem(t *testing.T, items []subtitlev1alpha1.SubtitleItem, langKey string) subtitlev1alpha1.SubtitleItem {
	t.Helper()
	for _, it := range items {
		if it.LangKey == langKey {
			return it
		}
	}
	t.Fatalf("no item with langKey %q", langKey)
	return subtitlev1alpha1.SubtitleItem{}
}

func assertControllerHalfIntact(t *testing.T, st subtitlev1alpha1.SubtitleRequestStatus, who string) {
	t.Helper()
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseSearching, st.Phase, who+" released phase")
	assert.NotEmpty(t, st.Conditions, who+" released conditions")
}

func assertWorkerItemIntact(t *testing.T, it subtitlev1alpha1.SubtitleItem, who string, wantScore int32) {
	t.Helper()
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, it.State, who+" released state on "+it.LangKey)
	assert.EqualValues(t, wantScore, it.Score, who+" released score on "+it.LangKey)
	assert.EqualValues(t, 100, it.ScoreOutOf, who+" released scoreOutOf on "+it.LangKey)
	assert.NotEmpty(t, it.Provider, who+" released provider on "+it.LangKey)
	assert.NotEmpty(t, it.SubtitleID, who+" released subtitleID on "+it.LangKey)
	assert.NotEmpty(t, it.Path, who+" released path on "+it.LangKey)
}

// assertRequestManagedFieldsSplit reads the apiserver's own record of who
// owns what and holds it to the declared split, leaf for leaf, per item.
//
// managedFields encodes a listType=map entry as a synthetic key of the form
// `k:{"langKey":"en"}` mapping to an object whose keys are `f:<leaf>` for
// every leaf that manager owns on that entry, plus a `.` marker for the
// entry's own existence. That shape was confirmed against a real embedded
// apiserver before this assertion was written, rather than assumed.
func assertRequestManagedFieldsSplit(t *testing.T, req *subtitlev1alpha1.SubtitleRequest) {
	t.Helper()

	wantItemLeaves := map[k8s.FieldManager]map[string][]string{
		k8s.ManagerCaptionarr: {
			"en": {"langKey", "nextSearchAt", "attempts"},
			"es": {"langKey", "nextSearchAt"},
		},
		k8s.ManagerCaptionarrWorker: {
			"en": {"langKey", "state", "score", "scoreOutOf", "provider", "subtitleID", "path", "lastError"},
			"es": {"langKey", "state", "score", "scoreOutOf", "provider", "subtitleID", "path", "lastError"},
		},
	}
	wantTopLevel := map[k8s.FieldManager][]string{
		k8s.ManagerCaptionarr:       {"observedGeneration", "profileGeneration", "probeHash", "phase", "conditions", "items"},
		k8s.ManagerCaptionarrWorker: {"items"},
	}

	seen := map[k8s.FieldManager]bool{}
	for _, entry := range req.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil {
			continue
		}
		mgr := k8s.FieldManager(entry.Manager)
		wantTop, ok := wantTopLevel[mgr]
		if !ok {
			continue // a manager outside this test's concern, e.g. the client's own create.
		}
		seen[mgr] = true

		var raw map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &raw))
		statusFields, ok := raw["f:status"].(map[string]any)
		require.Truef(t, ok, "%q has a status managedFields entry with no f:status", mgr)

		var gotTop []string
		for key := range statusFields {
			if key == "f:items" {
				gotTop = append(gotTop, "items")
				continue
			}
			gotTop = append(gotTop, strings.TrimPrefix(key, "f:"))
		}
		sort.Strings(gotTop)
		want := append([]string(nil), wantTop...)
		sort.Strings(want)
		assert.Equalf(t, want, gotTop, "%q's top-level status leaves do not match the declared split", mgr)

		itemsField, ok := statusFields["f:items"].(map[string]any)
		if !ok {
			require.Emptyf(t, wantItemLeaves[mgr], "%q declares no items in managedFields but the split expects some", mgr)
			continue
		}
		wantItems := wantItemLeaves[mgr]
		gotItems := map[string][]string{}
		for key, v := range itemsField {
			langKey := mapKeyLangKey(t, key)
			entryFields, ok := v.(map[string]any)
			require.Truef(t, ok, "%q's items[%s] managedFields entry is not an object", mgr, langKey)
			var leaves []string
			for leaf := range entryFields {
				if leaf == "." {
					continue
				}
				leaves = append(leaves, strings.TrimPrefix(leaf, "f:"))
			}
			sort.Strings(leaves)
			gotItems[langKey] = leaves
		}
		for langKey, want := range wantItems {
			wantSorted := append([]string(nil), want...)
			sort.Strings(wantSorted)
			assert.Equalf(t, wantSorted, gotItems[langKey],
				"%q's items[%s] managedFields leaves do not match the declared split", mgr, langKey)
		}
		assert.Lenf(t, gotItems, len(wantItems), "%q owns managedFields on an unexpected number of items", mgr)
	}
	for mgr := range wantTopLevel {
		assert.Truef(t, seen[mgr], "%q owns nothing on SubtitleRequest.status; its apply never landed", mgr)
	}
}

// mapKeyLangKey extracts the langKey value out of a listType=map synthetic
// key of the form `k:{"langKey":"en"}`.
func mapKeyLangKey(t *testing.T, key string) string {
	t.Helper()
	require.Truef(t, strings.HasPrefix(key, "k:"), "not a listType=map key: %q", key)
	var parsed struct {
		LangKey string `json:"langKey"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(key, "k:")), &parsed))
	require.NotEmpty(t, parsed.LangKey, "map key %q has no langKey", key)
	return parsed.LangKey
}

// A manager outside either split must be refused rather than allowed to
// claim fields no part of it accounts for.
func TestPatchRequestAgainstARealAPIServerRefusesAManagerOutsideTheSplit(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "captionarr-status-refuse"
	newNamespace(t, ctx, c, ns)
	req := newSubtitleRequest(t, ctx, c, ns, "refuse")

	err := status.PatchRequest(ctx, c, k8s.ManagerCatalogarr, req, nil)
	require.ErrorContains(t, err, "owns no part of SubtitleRequest.status")
}

// SubtitleProfile and SubtitleProvider each have exactly one legitimate
// writer, but the same complete-declaration and managedFields-visibility
// rules apply: a second call site that forgot a field would silently narrow
// what is on the object, and only managedFields would show ManagerCaptionarr
// owning less than it should.
func TestSubtitleProfileAndProviderRoundTripThroughManagerCaptionarr(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "captionarr-status-profile-provider"
	newNamespace(t, ctx, c, ns)

	profile := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	require.NoError(t, c.Create(ctx, profile))
	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerCaptionarr, profile,
		func(ac *subtitleac.SubtitleProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).WithWantedKeys("en").WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: subtitlev1alpha1.SubtitleProfileConditionReady, Status: metav1.ConditionTrue,
				Reason: "Ready", LastTransitionTime: metav1.Now(), ObservedGeneration: 1,
			}))
		}))

	var gotProfile subtitlev1alpha1.SubtitleProfile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(profile), &gotProfile))
	assert.EqualValues(t, 1, gotProfile.Status.ObservedGeneration)
	assert.Equal(t, []string{"en"}, gotProfile.Status.WantedKeys)
	assert.NotEmpty(t, gotProfile.Status.Conditions)

	provider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "opensubtitles", Namespace: ns},
		Spec:       subtitlev1alpha1.SubtitleProviderSpec{Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom},
	}
	require.NoError(t, c.Create(ctx, provider))
	require.NoError(t, status.PatchProvider(ctx, c, k8s.ManagerCaptionarr, provider,
		func(ac *subtitleac.SubtitleProviderStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).WithErrorsLast120s(0).WithHIVerifiable(true)
		}))

	var gotProvider subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(provider), &gotProvider))
	assert.EqualValues(t, 1, gotProvider.Status.ObservedGeneration)
	assert.True(t, gotProvider.Status.HIVerifiable)

	// A re-apply that only touches ThrottleReason must not release
	// HIVerifiable or ErrorsLast120s -- the same "survive its own re-apply"
	// property grabarr/status.status_envtest_test proves for the controller
	// and engine halves of Download.status.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(provider), provider))
	require.NoError(t, status.PatchProvider(ctx, c, k8s.ManagerCaptionarr, provider,
		func(ac *subtitleac.SubtitleProviderStatusApplyConfiguration) {
			ac.WithThrottleReason("5 errors in 120s")
		}))

	var afterReapply subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(provider), &afterReapply))
	assert.True(t, afterReapply.Status.HIVerifiable, "the re-apply released hiVerifiable")
	assert.Equal(t, "5 errors in 120s", afterReapply.Status.ThrottleReason)

	err := status.PatchProvider(ctx, c, k8s.ManagerCaptionarrWorker, provider, nil)
	require.ErrorContains(t, err, "owns no part of SubtitleProvider.status")
}

// Rule 3 of the item-liveness protocol (IsLive), on a real apiserver: once
// the controller stops sending an item, the worker's next apply -- seeded
// from a fresh read -- stops sending it too, no manager owns any leaf of
// the entry, and server-side apply deletes it. Before the protocol the
// worker re-declared every entry it had ever written, so a withdrawn
// language lived forever.
//
// Falsified: making RequestWorkerFields render non-live items again leaves
// "es" on the object with the worker still owning its leaves.
func TestAWithdrawnItemIsDeletedByTheWorkersNextApply(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "captionarr-status-withdraw"
	newNamespace(t, ctx, c, ns)
	req := newSubtitleRequest(t, ctx, c, ns, "withdraw")
	key := client.ObjectKeyFromObject(req)
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))

	// Steady state: both items live and fully described. The worker writes
	// its half first only because the CRD still requires items[].state
	// until F-4 relaxes it (rule 2 then lets the controller create items).
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarrWorker, req,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			ac.Items = []subtitleac.SubtitleItemApplyConfiguration{
				*subtitleac.SubtitleItem().WithLangKey("en").WithState(subtitlev1alpha1.SubtitleItemDownloaded).
					WithScore(95).WithScoreOutOf(100).WithProvider("os").WithSubtitleID("en-1").
					WithPath("withdraw.en.srt").WithLastError(""),
				*subtitleac.SubtitleItem().WithLangKey("es").WithState(subtitlev1alpha1.SubtitleItemDownloaded).
					WithScore(80).WithScoreOutOf(100).WithProvider("os").WithSubtitleID("es-1").
					WithPath("withdraw.es.srt").WithLastError(""),
			}
		}))
	require.NoError(t, c.Get(ctx, key, req))
	for i := range req.Status.Items {
		req.Status.Items[i].NextSearchAt = &now
	}
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarr, req, nil))

	// The controller withdraws "es": its next apply simply omits it.
	require.NoError(t, c.Get(ctx, key, req))
	req.Status.Items = slices.DeleteFunc(req.Status.Items, func(it subtitlev1alpha1.SubtitleItem) bool {
		return it.LangKey == "es"
	})
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarr, req, nil))

	var mid subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, key, &mid))
	esMid := findItem(t, mid.Status.Items, "es")
	require.False(t, status.IsLive(esMid), "setup: the controller's withdrawal did not land")
	require.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, esMid.State, "setup: the worker should still hold es")

	// The worker's next apply, from a fresh read.
	require.NoError(t, status.PatchRequest(ctx, c, k8s.ManagerCaptionarrWorker, &mid, nil))

	var after subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, key, &after))
	require.Len(t, after.Status.Items, 1, "the withdrawn item must be gone")
	en := after.Status.Items[0]
	assert.Equal(t, "en", en.LangKey)
	assertWorkerItemIntact(t, en, "the worker apply that dropped es", 95)
	assert.True(t, status.IsLive(en), "the live item keeps the controller's schedule")

	for _, entry := range after.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil {
			continue
		}
		var raw map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &raw))
		st, _ := raw["f:status"].(map[string]any)
		items, _ := st["f:items"].(map[string]any)
		for k := range items {
			assert.NotEqualf(t, "es", mapKeyLangKey(t, k), "%q still owns a leaf of the withdrawn item", entry.Manager)
		}
	}
}
