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
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newTranscodeProfile(t *testing.T, ctx context.Context, c client.Client, name string) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	require.NoError(t, c.Create(ctx, tp))
	return tp
}

// This is the test the TranscodeProfile half of this package exists for.
//
// TranscodeProfile.status has one writer, so there is no sibling to release
// fields out from under -- but PatchProfile still must re-declare every field
// on every apply, or a reconcile that changes only runningJobs would drop
// hash, matchingFiles and pendingJobs right back to zero on the object.
func TestTheProfileManagerDeclarationIsComplete(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	tp := newTranscodeProfile(t, ctx, c, "split-profile")
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	// EncoderLimits is seeded from the status PatchProfile reads, never set
	// through the mutate (ProfileFields), so the steady state carries one.
	tp.Status.EncoderLimits = []transcodev1alpha1.EncoderLimit{
		{Class: transcodev1alpha1.HardwareNVIDIA, Node: "gpu-1", NVDEC: []string{"h264:8"}},
	}

	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerTranscode, tp,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithHash("deadbeef").
				WithMatchingFiles(10).
				WithPendingJobs(4).
				WithRunningJobs(2).
				WithConditions(k8s.ConditionAC(metav1.Condition{
					Type: transcodev1alpha1.TranscodeProfileConditionReady, Status: metav1.ConditionTrue,
					Reason: "Ready", LastTransitionTime: now, ObservedGeneration: 1,
				}))
		}))

	var seeded transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tp), &seeded))
	require.Equal(t, "deadbeef", seeded.Status.Hash, "setup: the steady state did not land")

	// Re-apply, changing only runningJobs. Conditions is set again with the
	// SAME content: a real reconciler recomputes and resends its full
	// condition set on every pass (ProfileFields leaves Conditions unseeded
	// for exactly this reason -- see the package doc), so a caller that
	// dropped the WithConditions call here would be simulating a reconciler
	// bug, not testing this package.
	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerTranscode, &seeded,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) {
			ac.WithRunningJobs(3).WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: transcodev1alpha1.TranscodeProfileConditionReady, Status: metav1.ConditionTrue,
				Reason: "Ready", LastTransitionTime: now, ObservedGeneration: 1,
			}))
		}))

	var after transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tp), &after))
	assert.EqualValues(t, 1, after.Status.ObservedGeneration, "the re-apply released observedGeneration")
	assert.Equal(t, "deadbeef", after.Status.Hash, "the re-apply released hash")
	assert.EqualValues(t, 10, after.Status.MatchingFiles, "the re-apply released matchingFiles")
	assert.EqualValues(t, 4, after.Status.PendingJobs, "the re-apply released pendingJobs")
	assert.EqualValues(t, 3, after.Status.RunningJobs, "the re-apply did not update runningJobs")
	if assert.Len(t, after.Status.EncoderLimits, 1, "the re-apply released encoderLimits") {
		assert.Equal(t, "gpu-1", after.Status.EncoderLimits[0].Node)
		assert.Equal(t, []string{"h264:8"}, after.Status.EncoderLimits[0].NVDEC, "the re-apply released a limit's leaf")
	}
	if assert.Len(t, after.Status.Conditions, 1, "the re-apply released conditions") {
		assert.Equal(t, "Ready", after.Status.Conditions[0].Reason)
	}

	assertProfileManagedFields(t, &after)
}

// assertProfileManagedFields is assertJobManagedFields' counterpart for
// TranscodeProfile.
func assertProfileManagedFields(t *testing.T, tp *transcodev1alpha1.TranscodeProfile) {
	t.Helper()

	var found bool
	for _, entry := range tp.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil || entry.Manager != string(k8s.ManagerTranscode) {
			continue
		}
		found = true

		var fields map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields))
		st, ok := fields["f:status"].(map[string]any)
		require.True(t, ok, "transcode has a status managedFields entry with no f:status")

		var got []string
		for key := range st {
			got = append(got, strings.TrimPrefix(key, "f:"))
		}
		sort.Strings(got)
		assert.Equal(t, jsonNames(transcodev1alpha1.TranscodeProfileStatus{}, profileOwned), got,
			"the apiserver records transcode as owning a different set than it declares")
	}
	assert.True(t, found, "transcode owns nothing on TranscodeProfile.status; its apply never landed")
}

// jsonNames maps Go field names onto the JSON names managedFields uses, for
// whichever status struct typ is.
func jsonNames(typ any, goNames []string) []string {
	t := reflect.TypeOf(typ)
	out := make([]string, 0, len(goNames))
	for _, name := range goNames {
		f, ok := t.FieldByName(name)
		if !ok {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// PatchProfile must refuse every manager but k8s.ManagerTranscode.
func TestPatchProfileRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{k8s.ManagerTranscodePool, k8s.ManagerCatalog, k8s.FieldManager("nonsense")} {
		err := status.PatchProfile(context.Background(), nil, mgr,
			&transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, nil)
		require.ErrorContains(t, err, "owns no part of TranscodeProfile.status")
	}
}
